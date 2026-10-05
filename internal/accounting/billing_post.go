package accounting

// EP-17 / EP-21 postings of billing (contracts K2, K4, K8). The billing
// source documents are read through the reporting.acc_* views and posted
// once each (accounting.posted_sources):
//
//   - folio lines (charges): Dr Guest Ledger / Cr revenue per all-in
//     component (caddy fee held as liability), service charge payable, tax
//     per tax code; folio transfers have no ledger effect; a line voided
//     after posting is reversed;
//   - payments: Dr cash / bank / clearing per method, Cr Guest Ledger,
//     deposits or city ledger per purpose; refunds the other way round;
//   - deposits applied to folios: Dr Customer Deposits / Cr Guest Ledger;
//   - partner payouts: Dr caddy fee / instructor fee payable / Cr cash, bank;
//   - deferred revenue sub-ledger: deferral, recognition, breakage, reversal;
//   - invoices (K2): issue Dr AR – Invoiced / Cr City Ledger, payment
//     allocations, credit notes, write-offs and voids, so the AR control
//     account equals the open invoices of billing (FR-AR-04/06).
//
// The business day close (K4) posts what is not yet posted up to that day
// in one daily journal per kind (daily summary); in per-transaction mode a
// settled payment posts its folio at once (FR-PST-03).

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/platform/handle"
)

type folioLineRow struct {
	ID               uuid.UUID  `db:"id"`
	FolioID          uuid.UUID  `db:"folio_id"`
	FolioNumber      string     `db:"folio_number"`
	BusinessDate     *time.Time `db:"business_date"`
	BusinessLine     string     `db:"business_line"`
	RevenueComponent string     `db:"revenue_component"`
	ChargeType       string     `db:"charge_type"`
	Liability        bool       `db:"liability"`
	Net              string     `db:"net"`
	Service          string     `db:"service"`
	Tax              string     `db:"tax"`
	Total            string     `db:"total"`
	Components       []byte     `db:"components"`
	TaxLines         []byte     `db:"tax_lines"`
	ReferenceType    *string    `db:"reference_type"`
	Description      string     `db:"description"`
}

const lineCols = `l.id, l.folio_id, l.folio_number, l.business_date, l.business_line, l.revenue_component, l.charge_type, l.liability,
	l.net_amount::text AS net, l.service_amount::text AS service, l.tax_amount::text AS tax, l.total::text AS total, l.components, l.tax_lines,
	l.reference_type, l.description`

type comp struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	Amount    string `json:"amount"`
	Liability bool   `json:"liability"`
}

type taxLine struct {
	Code   string `json:"code"`
	Kind   string `json:"kind"`
	Amount string `json:"amount"`
}

// lineItems are the posting items of a folio line (sign = 1, or -1 for
// the reversal of a voided line).
func lineItems(l folioLineRow, sign int64, alloc []comp) []Item {
	if l.ReferenceType != nil && *l.ReferenceType == "billing.folio_transfer" {
		return nil // moved between folios: no ledger effect
	}
	s := decimal.NewFromInt(sign)
	base := func(part, component string, liability bool) map[string]string {
		return map[string]string{"part": part, "businessLine": l.BusinessLine, "revenueComponent": l.RevenueComponent, "component": component,
			"chargeType": l.ChargeType, "liability": boolStr(liability)}
	}
	dims := func(component string) Dims {
		return Dims{BusinessLine: l.BusinessLine, RevenueComponent: l.RevenueComponent, Component: component}
	}
	desc := l.FolioNumber + " · " + l.Description
	src := l.ID.String()
	var items []Item
	add := func(part, component, taxCode string, liability bool, amt decimal.Decimal) {
		if amt.IsZero() {
			return
		}
		a := base(part, component, liability)
		if taxCode != "" {
			a["taxCode"] = taxCode
		}
		items = append(items, Item{Source: "billing.folio_line", Attrs: a, Amount: amt.Mul(s), Description: desc, Dims: dims(component), DimSide: "credit",
			SourceType: "billing.folio_line", SourceID: src, FallbackDebit: "guest_ledger"})
	}
	var comps []comp
	_ = json.Unmarshal(l.Components, &comps)
	if len(comps) == 0 {
		comps = alloc // revenue allocation rule of a bundled charge (FR-REV-01)
	}
	net := dec(l.Net)
	if len(comps) == 0 {
		add("net", l.RevenueComponent, "", l.Liability, net)
	} else {
		sum := decimal.Zero
		for _, c := range comps {
			amt := dec(c.Amount)
			sum = sum.Add(amt)
			add("net", c.Code, "", c.Liability || l.Liability, amt)
		}
		add("net", l.RevenueComponent, "", l.Liability, net.Sub(sum))
	}
	var tls []taxLine
	_ = json.Unmarshal(l.TaxLines, &tls)
	tax, svc := decimal.Zero, decimal.Zero
	for _, t := range tls {
		amt := dec(t.Amount)
		if t.Kind == "service" {
			svc = svc.Add(amt)
			add("service", l.RevenueComponent, t.Code, false, amt)
		} else {
			tax = tax.Add(amt)
			add("tax", l.RevenueComponent, t.Code, false, amt)
		}
	}
	add("tax", l.RevenueComponent, "", false, dec(l.Tax).Sub(tax))
	add("service", l.RevenueComponent, "", false, dec(l.Service).Sub(svc))
	return items
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

type paymentRow struct {
	ID           uuid.UUID  `db:"id"`
	Number       string     `db:"number"`
	FolioID      *uuid.UUID `db:"folio_id"`
	AccountID    *uuid.UUID `db:"account_id"`
	MethodType   string     `db:"method_type"`
	Purpose      string     `db:"purpose"`
	Channel      string     `db:"channel"`
	Amount       string     `db:"amount"`
	BusinessDate *time.Time `db:"business_date"`
	BusinessLine *string    `db:"folio_business_line"`
}

const paymentCols = `p.id, p.number, p.folio_id, p.account_id, p.method_type, p.purpose, p.channel, p.amount::text AS amount, p.business_date,
	p.folio_business_line`

func paymentItems(p paymentRow) []Item {
	attrs := map[string]string{"methodType": p.MethodType, "purpose": p.Purpose, "channel": p.Channel, "businessLine": deref(p.BusinessLine)}
	amt := dec(p.Amount)
	desc := "Payment " + p.Number
	src := p.ID.String()
	return []Item{
		{Source: "billing.payment_method", Attrs: attrs, Amount: amt, Description: desc, SourceType: "billing.payment", SourceID: src,
			FallbackCredit: "posting_clearing"},
		{Source: "billing.payment_purpose", Attrs: attrs, Amount: amt, Description: desc, SourceType: "billing.payment", SourceID: src,
			FallbackDebit: "posting_clearing"},
	}
}

type refundRow struct {
	ID           uuid.UUID  `db:"id"`
	Number       string     `db:"number"`
	PaymentID    uuid.UUID  `db:"payment_id"`
	Amount       string     `db:"amount"`
	Destination  string     `db:"destination"`
	BusinessDate *time.Time `db:"business_date"`
	MethodType   string     `db:"method_type"`
	Purpose      string     `db:"purpose"`
}

func refundItems(r refundRow) []Item {
	attrs := map[string]string{"methodType": r.MethodType, "purpose": r.Purpose, "destination": r.Destination}
	amt := dec(r.Amount)
	desc := "Refund " + r.Number
	src := r.ID.String()
	return []Item{
		{Source: "billing.refund_purpose", Attrs: attrs, Amount: amt, Description: desc, SourceType: "billing.refund", SourceID: src,
			FallbackCredit: "posting_clearing"},
		{Source: "billing.refund_method", Attrs: attrs, Amount: amt, Description: desc, SourceType: "billing.refund", SourceID: src,
			FallbackDebit: "posting_clearing"},
	}
}

// sweepFilter selects the billing documents to post.
type sweepFilter struct {
	UpTo      *time.Time // business date (inclusive): daily summary
	FolioID   *uuid.UUID // per transaction
	RefundID  *uuid.UUID
	VoucherID *uuid.UUID
	Liability string // deferred entries of one liability type
	InvoiceID *uuid.UUID
	Revenue   bool // folio lines, payments, refunds, deposits
	Deferred  bool
	Payouts   bool
	AR        bool
}

// sweepHeader is the journal header of a sweep.
type sweepHeader struct {
	Date        time.Time
	SourceType  string
	SourceID    string
	SourceRef   string
	EventID     *uuid.UUID
	Description string
}

func (m *Module) cutOver(ctx context.Context, tx pgx.Tx, property uuid.UUID) time.Time {
	b, err := GetBook(ctx, tx, property)
	if err != nil {
		return time.Time{}
	}
	t, _ := time.Parse("2006-01-02", b.CutOverDate)
	return t
}

// sweepBilling posts the unposted billing documents selected by f.
func (m *Module) sweepBilling(ctx context.Context, tx pgx.Tx, property uuid.UUID, f sweepFilter, h sweepHeader) (outcome, error) {
	var out outcome
	cut := m.cutOver(ctx, tx, property)
	upTo := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	if f.UpTo != nil {
		upTo = *f.UpTo
	}
	if f.Revenue {
		j, missing, err := m.sweepRevenue(ctx, tx, property, f, h, cut, upTo)
		if err != nil {
			return out, err
		}
		out.add(j, missing)
	}
	if f.Deferred {
		j, missing, err := m.sweepDeferred(ctx, tx, property, f, h, cut, upTo)
		if err != nil {
			return out, err
		}
		out.add(j, missing)
	}
	if f.Payouts {
		j, missing, err := m.sweepPayouts(ctx, tx, property, h, cut, upTo)
		if err != nil {
			return out, err
		}
		out.add(j, missing)
	}
	if f.Revenue && f.UpTo != nil {
		j, missing, err := m.sweepCash(ctx, tx, property, h, cut, upTo)
		if err != nil {
			return out, err
		}
		out.add(j, missing)
	}
	if f.AR {
		if f.UpTo != nil && f.InvoiceID == nil {
			o, err := m.sweepAR(ctx, tx, property, upTo, h)
			if err != nil {
				return out, err
			}
			out.Journals = append(out.Journals, o.Journals...)
			out.Missing = append(out.Missing, o.Missing...)
			if len(o.Journals) > 0 {
				out.Status = "posted"
			}
		} else {
			j, missing, err := m.sweepAllocations(ctx, tx, property, f, h, upTo)
			if err != nil {
				return out, err
			}
			out.add(j, missing)
		}
	}
	return out, nil
}

func (m *Module) sweepRevenue(ctx context.Context, tx pgx.Tx, property uuid.UUID, f sweepFilter, h sweepHeader, cut, upTo time.Time) (*AccountingJournal, []Missing, error) {
	var sources []sourceMark
	notPosted := func(alias, typ string) string {
		return ` AND NOT EXISTS (SELECT 1 FROM accounting.posted_sources s WHERE s.source_type = '` + typ + `' AND s.source_id = ` + alias + `.id AND s.part = 0)`
	}
	var folio any
	if f.FolioID != nil {
		folio = *f.FolioID
	}
	if f.RefundID == nil {
		// charges
		lines, err := handle.List[folioLineRow](tx.Query(ctx, `SELECT `+lineCols+` FROM reporting.acc_folio_lines l WHERE l.property_id = $1
			AND l.voided_at IS NULL AND l.business_date >= $2::date AND l.business_date <= $3::date AND ($4::uuid IS NULL OR l.folio_id = $4)`+notPosted("l", "billing.folio_line")+`
			ORDER BY l.business_date, l.posted_at LIMIT 20000`, property, cut, upTo, folio))
		if err != nil {
			return nil, nil, err
		}
		alloc, err := loadAllocator(ctx, tx)
		if err != nil {
			return nil, nil, err
		}
		cur := currency(ctx, tx)
		split := func(l folioLineRow) []comp {
			return alloc.split(l.BusinessLine, l.RevenueComponent, l.BusinessDate, dec(l.Net), cur)
		}
		for _, l := range lines {
			sources = append(sources, sourceMark{Type: "billing.folio_line", ID: l.ID, BusinessDate: l.BusinessDate, Key1: l.BusinessLine,
				Key2: l.RevenueComponent, Amount: dec(l.Total), Items: lineItems(l, 1, split(l))})
		}
		// lines voided after they were posted are reversed
		voided, err := handle.List[folioLineRow](tx.Query(ctx, `SELECT `+lineCols+` FROM reporting.acc_folio_lines l
			JOIN accounting.posted_sources s ON s.source_type = 'billing.folio_line' AND s.source_id = l.id AND s.part = 0
			WHERE l.property_id = $1 AND l.voided_at IS NOT NULL AND ($2::uuid IS NULL OR l.folio_id = $2)
			AND NOT EXISTS (SELECT 1 FROM accounting.posted_sources v WHERE v.source_type = 'billing.folio_line' AND v.source_id = l.id AND v.part = 1)
			LIMIT 5000`, property, folio))
		if err != nil {
			return nil, nil, err
		}
		for _, l := range voided {
			sources = append(sources, sourceMark{Type: "billing.folio_line", ID: l.ID, Part: 1, BusinessDate: l.BusinessDate, Key1: l.BusinessLine,
				Key2: l.RevenueComponent, Amount: dec(l.Total).Neg(), Items: lineItems(l, -1, split(l))})
		}
		// payments (gross; refunds are posted on their own)
		pays, err := handle.List[paymentRow](tx.Query(ctx, `SELECT `+paymentCols+` FROM reporting.acc_payments p WHERE p.property_id = $1
			AND p.status IN ('completed', 'refunded') AND p.business_date >= $2::date AND p.business_date <= $3::date AND ($4::uuid IS NULL OR p.folio_id = $4)`+
			notPosted("p", "billing.payment")+` ORDER BY p.business_date, p.created_at LIMIT 20000`, property, cut, upTo, folio))
		if err != nil {
			return nil, nil, err
		}
		for _, p := range pays {
			sources = append(sources, sourceMark{Type: "billing.payment", ID: p.ID, BusinessDate: p.BusinessDate, Key1: p.MethodType, Key2: p.Purpose,
				Amount: dec(p.Amount), Items: paymentItems(p)})
		}
		// deposits applied to folios (cumulative applied amount)
		type depRow struct {
			ID      uuid.UUID `db:"id"`
			Number  string    `db:"number"`
			Applied string    `db:"applied"`
			Posted  string    `db:"posted"`
			Parts   int       `db:"parts"`
		}
		deps, err := handle.List[depRow](tx.Query(ctx, `SELECT d.id, d.number, d.applied_amount::text AS applied,
			coalesce((SELECT sum(s.amount) FROM accounting.posted_sources s WHERE s.source_type = 'billing.deposit_application' AND s.source_id = d.id), 0)::text AS posted,
			(SELECT count(*) FROM accounting.posted_sources s WHERE s.source_type = 'billing.deposit_application' AND s.source_id = d.id)::int AS parts
			FROM reporting.acc_deposits d WHERE d.property_id = $1 AND d.applied_amount > 0 AND ($2::uuid IS NULL OR d.folio_id = $2)
			AND d.applied_amount <> coalesce((SELECT sum(s.amount) FROM accounting.posted_sources s WHERE s.source_type = 'billing.deposit_application'
			  AND s.source_id = d.id), 0) AND d.created_at >= $3::date`, property, folio, cut))
		if err != nil {
			return nil, nil, err
		}
		for _, d := range deps {
			delta := dec(d.Applied).Sub(dec(d.Posted))
			bd := h.Date
			sources = append(sources, sourceMark{Type: "billing.deposit_application", ID: d.ID, Part: d.Parts, BusinessDate: &bd, Amount: delta,
				Items: []Item{{Source: "billing.deposit_application", Attrs: map[string]string{}, Amount: delta, Description: "Deposit " + d.Number + " applied",
					SourceType: "billing.deposit", SourceID: d.ID.String(), FallbackDebit: "customer_deposits", FallbackCredit: "guest_ledger"}}})
		}
	}
	// refunds
	if f.FolioID == nil {
		var rid any
		if f.RefundID != nil {
			rid = *f.RefundID
		}
		refs, err := handle.List[refundRow](tx.Query(ctx, `SELECT r.id, r.number, r.payment_id, r.amount::text AS amount, r.destination, r.business_date,
			r.method_type, r.purpose FROM reporting.acc_refunds r WHERE r.property_id = $1 AND r.status = 'completed' AND r.business_date >= $2::date
			AND r.business_date <= $3::date AND ($4::uuid IS NULL OR r.id = $4)`+notPosted("r", "billing.refund")+` ORDER BY r.business_date LIMIT 5000`,
			property, cut, upTo, rid))
		if err != nil {
			return nil, nil, err
		}
		for _, r := range refs {
			sources = append(sources, sourceMark{Type: "billing.refund", ID: r.ID, BusinessDate: r.BusinessDate, Key1: r.MethodType, Key2: r.Purpose,
				Amount: dec(r.Amount), Items: refundItems(r)})
		}
	}
	if len(sources) == 0 {
		return nil, nil, nil
	}
	return m.postSources(ctx, tx, posting{Entry: Entry{Property: property, Date: h.Date, SourceType: h.SourceType, SourceID: h.SourceID,
		SourceRef: h.SourceRef, EventID: h.EventID, Description: h.Description}, Sources: sources})
}

type deferredRow struct {
	ID               uuid.UUID `db:"id"`
	LiabilityType    string    `db:"liability_type"`
	RefType          string    `db:"ref_type"`
	RefID            uuid.UUID `db:"ref_id"`
	EntryType        string    `db:"entry_type"`
	Amount           string    `db:"amount"`
	RevenueComponent string    `db:"revenue_component"`
	Description      string    `db:"description"`
	OccurredAt       time.Time `db:"occurred_at"`
	ViaTender        bool      `db:"via_tender"`
}

func deferredItems(d deferredRow) []Item {
	amt := dec(d.Amount)
	attrs := map[string]string{"liabilityType": d.LiabilityType, "entryType": d.EntryType, "revenueComponent": d.RevenueComponent,
		"viaTender": boolStr(d.ViaTender), "refType": d.RefType}
	src := d.ID.String()
	dims := Dims{RevenueComponent: d.RevenueComponent, Component: d.RevenueComponent}
	switch d.EntryType {
	case "deferral", "adjustment":
		return []Item{{Source: "billing.deferred_revenue", Attrs: attrs, Amount: amt, Description: d.Description, SourceType: "billing.deferred_entry",
			SourceID: src, FallbackDebit: "deferred_clearing"}}
	case "recognition":
		a := amt.Abs()
		return []Item{
			{Source: "billing.deferred_revenue", Attrs: attrs, Amount: a, Description: d.Description, SourceType: "billing.deferred_entry", SourceID: src,
				FallbackCredit: "posting_clearing"},
			{Source: "billing.deferred_recognition", Attrs: attrs, Amount: a, Description: d.Description, SourceType: "billing.deferred_entry", SourceID: src,
				FallbackDebit: "posting_clearing", Dims: dims, DimSide: "credit"},
		}
	default: // breakage, reversal
		return []Item{{Source: "billing.deferred_revenue", Attrs: attrs, Amount: amt.Abs(), Description: d.Description, SourceType: "billing.deferred_entry",
			SourceID: src, Dims: dims, DimSide: "credit"}}
	}
}

func (m *Module) sweepDeferred(ctx context.Context, tx pgx.Tx, property uuid.UUID, f sweepFilter, h sweepHeader, cut, upTo time.Time) (*AccountingJournal, []Missing, error) {
	var voucher any
	if f.VoucherID != nil {
		voucher = *f.VoucherID
	}
	rows, err := handle.List[deferredRow](tx.Query(ctx, `SELECT e.id, e.liability_type, e.ref_type, e.ref_id, e.entry_type, e.amount::text AS amount,
		e.revenue_component, e.description, e.occurred_at, e.via_tender FROM reporting.acc_deferred_entries e WHERE e.property_id = $1
		AND e.occurred_at >= $2::date AND (e.occurred_at AT TIME ZONE coalesce((SELECT timezone FROM platform.properties WHERE id = $1 AND timezone <> ''),
		  (SELECT timezone FROM platform.instance)))::date <= $3::date AND ($4::uuid IS NULL OR e.ref_id = $4) AND ($5 = '' OR e.liability_type = $5)
		AND NOT EXISTS (SELECT 1 FROM accounting.posted_sources s WHERE s.source_type = 'billing.deferred_entry' AND s.source_id = e.id)
		ORDER BY e.occurred_at LIMIT 20000`, property, cut, upTo, voucher, f.Liability))
	if err != nil {
		return nil, nil, err
	}
	if len(rows) == 0 {
		return nil, nil, nil
	}
	var sources []sourceMark
	for _, d := range rows {
		bd := localDate(ctx, tx, property, d.OccurredAt)
		sources = append(sources, sourceMark{Type: "billing.deferred_entry", ID: d.ID, BusinessDate: &bd, Key1: d.LiabilityType, Key2: d.EntryType,
			Amount: dec(d.Amount), Items: deferredItems(d)})
	}
	desc := strings.Replace(h.Description, "Daily revenue", "Deferred revenue", 1)
	if desc == h.Description {
		desc = "Deferred revenue · " + h.Description
	}
	return m.postSources(ctx, tx, posting{Entry: Entry{Property: property, Date: h.Date, SourceType: h.SourceType, SourceID: h.SourceID,
		SourceRef: h.SourceRef, EventID: h.EventID, Description: desc}, Sources: sources})
}

type payoutRow struct {
	ID              uuid.UUID `db:"id"`
	Number          string    `db:"number"`
	PayoutType      string    `db:"payout_type"`
	BeneficiaryType string    `db:"beneficiary_type"`
	BeneficiaryID   uuid.UUID `db:"beneficiary_id"`
	BeneficiaryName string    `db:"beneficiary_name"`
	Amount          string    `db:"amount"`
	MethodType      string    `db:"method_type"`
	PaidAt          time.Time `db:"paid_at"`
	Deductions      *string   `db:"settlement_deductions"`
}

func (m *Module) sweepPayouts(ctx context.Context, tx pgx.Tx, property uuid.UUID, h sweepHeader, cut, upTo time.Time) (*AccountingJournal, []Missing, error) {
	rows, err := handle.List[payoutRow](tx.Query(ctx, `SELECT o.id, o.number, o.payout_type, o.beneficiary_type, o.beneficiary_id, o.beneficiary_name,
		o.amount::text AS amount, o.method_type, o.paid_at, o.settlement_deductions::text AS settlement_deductions FROM reporting.acc_payouts o
		WHERE o.property_id = $1 AND o.paid_at >= $2::date AND o.paid_at < $3::date + 2
		AND NOT EXISTS (SELECT 1 FROM accounting.posted_sources s WHERE s.source_type = 'billing.payout' AND s.source_id = o.id) LIMIT 5000`, property, cut, upTo))
	if err != nil {
		return nil, nil, err
	}
	var sources []sourceMark
	for _, o := range rows {
		bd := localDate(ctx, tx, property, o.PaidAt)
		if bd.After(upTo) {
			continue
		}
		amt := dec(o.Amount)
		ded := decp(o.Deductions)
		attrs := map[string]string{"payoutType": o.PayoutType, "methodType": o.MethodType}
		pid := o.BeneficiaryID
		dims := Dims{PartnerType: o.BeneficiaryType, PartnerID: &pid, PartnerName: o.BeneficiaryName}
		with := func(part string) map[string]string {
			c := map[string]string{"part": part}
			for k, v := range attrs {
				c[k] = v
			}
			return c
		}
		desc := "Payout " + o.Number + " · " + o.BeneficiaryName
		items := []Item{
			{Source: "billing.payout", Attrs: with("payable"), Amount: amt, Description: desc, Dims: dims, DimSide: "debit", SourceType: "billing.payout",
				SourceID: o.ID.String(), FallbackCredit: "posting_clearing"},
			{Source: "billing.payout_method", Attrs: attrs, Amount: amt, Description: desc, SourceType: "billing.payout", SourceID: o.ID.String(),
				FallbackDebit: "posting_clearing"},
		}
		if ded.IsPositive() {
			items = append(items, Item{Source: "billing.payout", Attrs: with("deduction"), Amount: ded, Description: desc + " (deductions)", Dims: dims,
				DimSide: "debit", SourceType: "billing.payout", SourceID: o.ID.String()})
		}
		sources = append(sources, sourceMark{Type: "billing.payout", ID: o.ID, BusinessDate: &bd, Key1: o.PayoutType, Key2: o.MethodType, Amount: amt, Items: items})
	}
	if len(sources) == 0 {
		return nil, nil, nil
	}
	return m.postSources(ctx, tx, posting{Entry: Entry{Property: property, Date: h.Date, SourceType: h.SourceType, SourceID: h.SourceID,
		SourceRef: h.SourceRef, EventID: h.EventID, Description: "Partner payouts · " + h.Description}, Sources: sources})
}

type allocationRow struct {
	ID            uuid.UUID  `db:"id"`
	InvoiceID     uuid.UUID  `db:"invoice_id"`
	Amount        string     `db:"amount"`
	CreatedAt     time.Time  `db:"created_at"`
	PaymentNumber string     `db:"payment_number"`
	InvoiceNumber *string    `db:"invoice_number"`
	AccountID     *uuid.UUID `db:"account_id"`
	BillToName    string     `db:"bill_to_name"`
	Customer      *uuid.UUID `db:"customer_id"`
	Corporate     *uuid.UUID `db:"corporate_account_id"`
}

// sweepAllocations posts the payments allocated to posted invoices: the
// invoiced receivable is settled from the city ledger (FR-AR-03).
func (m *Module) sweepAllocations(ctx context.Context, tx pgx.Tx, property uuid.UUID, f sweepFilter, h sweepHeader, upTo time.Time) (*AccountingJournal, []Missing, error) {
	var inv any
	if f.InvoiceID != nil {
		inv = *f.InvoiceID
	}
	rows, err := handle.List[allocationRow](tx.Query(ctx, `SELECT a.id, a.invoice_id, a.amount::text AS amount, a.created_at, a.payment_number,
		i.number AS invoice_number, i.account_id, i.bill_to_name, i.customer_id, i.corporate_account_id
		FROM reporting.acc_allocations a JOIN reporting.acc_invoices i ON i.id = a.invoice_id
		WHERE a.property_id = $1 AND a.invoice_id IS NOT NULL AND ($2::uuid IS NULL OR a.invoice_id = $2) AND a.created_at < $3::date + 2
		AND EXISTS (SELECT 1 FROM accounting.ar_entries e WHERE e.entry_type IN ('invoice', 'opening') AND e.invoice_id = a.invoice_id)
		AND NOT EXISTS (SELECT 1 FROM accounting.posted_sources s WHERE s.source_type = 'billing.allocation' AND s.source_id = a.id)
		ORDER BY a.created_at LIMIT 5000`, property, inv, upTo))
	if err != nil {
		return nil, nil, err
	}
	var sources []sourceMark
	var entries []arEntry
	cut := m.cutOver(ctx, tx, property)
	for _, a := range rows {
		bd := localDate(ctx, tx, property, a.CreatedAt)
		if bd.After(upTo) || bd.Before(cut) {
			continue // before the cut-over: in the opening balances
		}
		amt := dec(a.Amount)
		dims := arDims(a.AccountID, a.BillToName)
		sources = append(sources, sourceMark{Type: "billing.allocation", ID: a.ID, BusinessDate: &bd, Key1: deref(a.InvoiceNumber), Amount: amt,
			Items: []Item{{Source: "billing.allocation", Attrs: map[string]string{}, Amount: amt, Dims: dims,
				Description: "Payment " + a.PaymentNumber + " → " + deref(a.InvoiceNumber), SourceType: "billing.allocation", SourceID: a.ID.String(),
				FallbackDebit: "ar_unbilled", FallbackCredit: "ar_control"}}})
		entries = append(entries, arEntry{InvoiceID: a.InvoiceID, InvoiceNumber: deref(a.InvoiceNumber), AccountID: a.AccountID, CustomerID: a.Customer,
			CorporateID: a.Corporate, BillTo: a.BillToName, Type: "payment", Date: bd, Amount: amt.Neg(), SourceID: a.ID})
	}
	if len(sources) == 0 {
		return nil, nil, nil
	}
	j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: property, Date: h.Date, SourceType: h.SourceType, SourceID: h.SourceID,
		SourceRef: h.SourceRef, EventID: h.EventID, Description: "AR settlements · " + h.Description}, Sources: sources})
	if err != nil {
		return nil, missing, err
	}
	for _, e := range entries {
		if ok, err := isPosted(ctx, tx, "billing.allocation", e.SourceID); err != nil || !ok {
			if err != nil {
				return nil, nil, err
			}
			continue
		}
		e.Journal = journalID(j)
		if err := insertAREntry(ctx, tx, property, e); err != nil {
			return nil, nil, err
		}
	}
	return j, missing, nil
}

func journalID(j *AccountingJournal) *uuid.UUID {
	if j == nil {
		return nil
	}
	return &j.ID
}

func arDims(account *uuid.UUID, name string) Dims {
	if account == nil {
		return Dims{}
	}
	a := *account
	return Dims{PartnerType: "ar_account", PartnerID: &a, PartnerName: name}
}

// ── AR ledger entries ─────────────────────────────────────────────────────

type arEntry struct {
	InvoiceID     uuid.UUID
	InvoiceNumber string
	AccountID     *uuid.UUID
	CustomerID    *uuid.UUID
	CorporateID   *uuid.UUID
	BillTo        string
	Type          string
	Date          time.Time
	Due           *time.Time
	Amount        decimal.Decimal
	SourceID      uuid.UUID
	Journal       *uuid.UUID
}

func insertAREntry(ctx context.Context, tx pgx.Tx, property uuid.UUID, e arEntry) error {
	_, err := tx.Exec(ctx, `INSERT INTO accounting.ar_entries (id, property_id, invoice_id, invoice_number, account_id, customer_id, corporate_account_id,
		bill_to_name, entry_type, entry_date, due_date, amount, source_id, journal_id) VALUES (gen_random_uuid(),$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::numeric,$12,$13)
		ON CONFLICT (entry_type, source_id) DO UPDATE SET journal_id = EXCLUDED.journal_id, amount = EXCLUDED.amount`,
		property, e.InvoiceID, nz(e.InvoiceNumber), e.AccountID, e.CustomerID, e.CorporateID, nz(e.BillTo), e.Type, e.Date, e.Due, e.Amount.String(),
		e.SourceID, e.Journal)
	return err
}
