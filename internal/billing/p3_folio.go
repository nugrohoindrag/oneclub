package billing

// PRD P3 EP-17 FR-BIL-P3-01/02/05: the customer folio — one open folio per
// customer or corporate account — merges the folios of every business line
// (P1 golf, P2 sport / stay / POS, P3 banquet / package / tournament)
// without moving their lines; a split moves charges to another payer
// (another person or the company) across lines with transfer lines that keep
// the original charge as evidence; corporate accounts over their credit
// limit charge more only with an approved credit override.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/provision"
)

// CustomerFolio is the folio of a customer or corporate account across lines.
type CustomerFolio struct {
	ID                 uuid.UUID  `json:"id" db:"id"`
	Number             string     `json:"number" db:"number"`
	CustomerID         *uuid.UUID `json:"customerId" db:"customer_id"`
	CorporateAccountID *uuid.UUID `json:"corporateAccountId" db:"corporate_account_id"`
	HolderName         string     `json:"holderName" db:"holder_name"`
	Currency           string     `json:"currency" db:"currency"`
	Status             string     `json:"status" db:"status" enum:"open,closed"`
	Version            int        `json:"version" db:"version"`
	Folios             int        `json:"folios" db:"folios" doc:"Linked folios"`
	Charges            string     `json:"charges" db:"charges"`
	Paid               string     `json:"paid" db:"paid"`
	Balance            string     `json:"balance" db:"balance"`
	CreatedAt          time.Time  `json:"createdAt" db:"created_at"`
}

// LineCharges is the charge total of one business line.
type LineCharges struct {
	BusinessLine string `json:"businessLine" db:"business_line"`
	Charges      string `json:"charges" db:"charges"`
}

// CustomerFolioDetail is a customer folio with its folios and invoices.
type CustomerFolioDetail struct {
	CustomerFolio
	ByLine   []LineCharges `json:"byLine"`
	Folios   []Folio       `json:"folioList"`
	Invoices []Invoice     `json:"invoices"`
}

const customerFolioSelect = `SELECT c.id, c.number, c.customer_id, c.corporate_account_id, c.holder_name, c.currency, c.status, c.version, c.created_at,
	(SELECT count(*) FROM billing.folios f WHERE f.customer_folio_id = c.id)::int AS folios,
	coalesce((SELECT sum(l.total) FROM billing.folio_lines l JOIN billing.folios f ON f.id = l.folio_id
	  WHERE f.customer_folio_id = c.id AND l.voided_at IS NULL), 0)::text AS charges,
	(coalesce((SELECT sum(p.amount - p.refunded_amount) FROM billing.payments p JOIN billing.folios f ON f.id = p.folio_id
	  WHERE f.customer_folio_id = c.id AND p.status IN ('completed', 'refunded') AND p.purpose = 'settlement'), 0)
	 + coalesce((SELECT sum(d.applied_amount) FROM billing.deposits d JOIN billing.folios f ON f.id = d.folio_id WHERE f.customer_folio_id = c.id), 0))::text AS paid,
	'0' AS balance
	FROM billing.customer_folios c`

func fixCustomerFolio(c *CustomerFolio) {
	p := places(c.Currency)
	c.Balance = dec(c.Charges).Sub(dec(c.Paid)).StringFixed(p)
	c.Charges, c.Paid = dec(c.Charges).StringFixed(p), dec(c.Paid).StringFixed(p)
}

// GetCustomerFolio loads a customer folio with its folios and invoices.
func GetCustomerFolio(ctx context.Context, q dbtx.Querier, cid uuid.UUID) (CustomerFolioDetail, error) {
	c, err := oneOf[CustomerFolio]("customer folio")(q.Query(ctx, customerFolioSelect+` WHERE c.id = $1`, cid))
	if err != nil {
		return CustomerFolioDetail{}, errs.NotFound("customer folio")
	}
	fixCustomerFolio(&c)
	out := CustomerFolioDetail{CustomerFolio: c}
	if out.ByLine, err = handle.List[LineCharges](q.Query(ctx, `SELECT l.business_line, sum(l.total)::text AS charges FROM billing.folio_lines l
		JOIN billing.folios f ON f.id = l.folio_id WHERE f.customer_folio_id = $1 AND l.voided_at IS NULL GROUP BY 1 ORDER BY 1`, cid)); err != nil {
		return out, err
	}
	rows, err := q.Query(ctx, `SELECT `+folioCols+` FROM billing.folios f WHERE f.customer_folio_id = $1 ORDER BY f.created_at`, cid)
	if err != nil {
		return out, err
	}
	out.Folios = []Folio{}
	for rows.Next() {
		f, err := scanFolio(rows)
		if err != nil {
			rows.Close()
			return out, err
		}
		out.Folios = append(out.Folios, f)
	}
	rows.Close()
	out.Invoices, err = listInvoices(ctx, q, `i.customer_folio_id = $1 OR i.folio_id IN (SELECT id FROM billing.folios WHERE customer_folio_id = $1)`, cid)
	return out, err
}

// CustomerFolioInput opens (or returns) the customer folio of a payer.
type CustomerFolioInput struct {
	CustomerID         *uuid.UUID `json:"customerId,omitempty"`
	CorporateAccountID *uuid.UUID `json:"corporateAccountId,omitempty"`
}

// EnsureCustomerFolio returns the open customer folio of a customer or a
// corporate account, opening it on first use.
func (s *Service) EnsureCustomerFolio(ctx context.Context, tx pgx.Tx, property uuid.UUID, in CustomerFolioInput) (CustomerFolioDetail, error) {
	if (in.CustomerID == nil) == (in.CorporateAccountID == nil) {
		return CustomerFolioDetail{}, errs.Validation("payer_required", "choose either a customer or a corporate account",
			errs.Field("customerId", "required", "customer or corporate account"))
	}
	var existing uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM billing.customer_folios WHERE property_id = $1 AND status = 'open'
		AND (customer_id = $2 OR corporate_account_id = $3)`, property, in.CustomerID, in.CorporateAccountID).Scan(&existing)
	if err == nil {
		return GetCustomerFolio(ctx, tx, existing)
	}
	if !dbtx.IsNoRows(err) {
		return CustomerFolioDetail{}, err
	}
	var holder string
	if in.CustomerID != nil {
		c, err := crm.GetCustomer(ctx, tx, *in.CustomerID)
		if err != nil {
			return CustomerFolioDetail{}, errs.Validation("invalid_customer", "customer not found", errs.Field("customerId", "not_found", "customer not found"))
		}
		holder = c.Name
	} else {
		n, err := crm.CorporateName(ctx, tx, *in.CorporateAccountID)
		if err != nil {
			return CustomerFolioDetail{}, errs.Validation("invalid_corporate_account", "corporate account not found",
				errs.Field("corporateAccountId", "not_found", "corporate account not found"))
		}
		holder = n
	}
	cur, err := org.Currency(ctx, tx)
	if err != nil {
		return CustomerFolioDetail{}, err
	}
	num, err := number(ctx, tx, property, "CFL")
	if err != nil {
		return CustomerFolioDetail{}, err
	}
	cid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO billing.customer_folios (id, property_id, number, customer_id, corporate_account_id, holder_name, currency,
		created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)`, cid, property, num, in.CustomerID, in.CorporateAccountID, holder, cur,
		id.Ptr(actor(ctx))); err != nil {
		return CustomerFolioDetail{}, err
	}
	out, err := GetCustomerFolio(ctx, tx, cid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionCreate, EntityType: "billing.customer_folio", EntityID: cid.String(),
		EntityLabel: num + " · " + holder, PropertyID: &property, After: out.CustomerFolio})
}

func lockCustomerFolio(ctx context.Context, tx pgx.Tx, cid uuid.UUID) (CustomerFolio, error) {
	var x uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM billing.customer_folios WHERE id = $1 FOR UPDATE`, cid).Scan(&x); err != nil {
		if dbtx.IsNoRows(err) {
			return CustomerFolio{}, errs.NotFound("customer folio")
		}
		return CustomerFolio{}, err
	}
	c, err := oneOf[CustomerFolio]("customer folio")(tx.Query(ctx, customerFolioSelect+` WHERE c.id = $1`, cid))
	fixCustomerFolio(&c)
	return c, err
}

// MergeInput links folios of any business line to a customer folio.
type MergeInput struct {
	FolioIDs []uuid.UUID `json:"folioIds"`
}

// MergeFolios links folios to the customer folio (FR-BIL-P3-01): the folios
// keep their lines, payments and deposits; billing reads them together.
func (s *Service) MergeFolios(ctx context.Context, tx pgx.Tx, cid uuid.UUID, in MergeInput) (CustomerFolioDetail, error) {
	c, err := lockCustomerFolio(ctx, tx, cid)
	if err != nil {
		return CustomerFolioDetail{}, err
	}
	if c.Status != "open" {
		return CustomerFolioDetail{}, errs.Conflict("customer_folio_closed", "the customer folio is closed")
	}
	if len(in.FolioIDs) == 0 {
		return CustomerFolioDetail{}, handle.Invalid("folioIds", "required", "choose at least one folio")
	}
	var merged []string
	for _, fid := range in.FolioIDs {
		var status, num string
		var linked *uuid.UUID
		var property uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT property_id, status, number, customer_folio_id FROM billing.folios WHERE id = $1 FOR UPDATE`, fid).
			Scan(&property, &status, &num, &linked); err != nil {
			if dbtx.IsNoRows(err) {
				return CustomerFolioDetail{}, errs.NotFound("folio")
			}
			return CustomerFolioDetail{}, err
		}
		switch {
		case status == "cancelled":
			return CustomerFolioDetail{}, errs.Conflict("folio_cancelled", "folio "+num+" is cancelled")
		case linked != nil && *linked == cid:
			continue
		case linked != nil:
			return CustomerFolioDetail{}, errs.Conflict("folio_already_merged", "folio "+num+" belongs to another customer folio")
		}
		if _, err := tx.Exec(ctx, `UPDATE billing.folios SET customer_folio_id = $2, corporate_account_id = coalesce(corporate_account_id, $3),
			version = version + 1, updated_by = $4 WHERE id = $1`, fid, cid, c.CorporateAccountID, id.Ptr(actor(ctx))); err != nil {
			return CustomerFolioDetail{}, err
		}
		merged = append(merged, num)
	}
	if _, err := tx.Exec(ctx, `UPDATE billing.customer_folios SET version = version + 1, updated_by = $2 WHERE id = $1`, cid, id.Ptr(actor(ctx))); err != nil {
		return CustomerFolioDetail{}, err
	}
	out, err := GetCustomerFolio(ctx, tx, cid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "merge", EntityType: "billing.customer_folio", EntityID: cid.String(),
		EntityLabel: c.Number, Before: c, After: map[string]any{"merged": merged, "folios": out.Folios}})
}

// AttachFolio links a folio to the payer's customer folio (used by the P3
// business lines when they open a folio for a customer or a company).
func (s *Service) AttachFolio(ctx context.Context, tx pgx.Tx, property, folioID uuid.UUID, payer CustomerFolioInput) (uuid.UUID, error) {
	cf, err := s.EnsureCustomerFolio(ctx, tx, property, payer)
	if err != nil {
		return uuid.Nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE billing.folios SET customer_folio_id = $2, corporate_account_id = coalesce(corporate_account_id, $3) WHERE id = $1
		AND customer_folio_id IS NULL`, folioID, cf.ID, payer.CorporateAccountID)
	return cf.ID, err
}

// SplitInput moves charges to another payer (FR-BIL-P3-02).
type SplitInput struct {
	LineIDs            []uuid.UUID `json:"lineIds" doc:"Charge lines to split (per item / per person); any folio of the customer folio"`
	Percent            string      `json:"percent,omitempty" doc:"Share of every chosen line moved to the payer (default 100)"`
	CustomerID         *uuid.UUID  `json:"customerId,omitempty" doc:"Payer: another customer"`
	CorporateAccountID *uuid.UUID  `json:"corporateAccountId,omitempty" doc:"Payer: the company"`
	Reason             string      `json:"reason"`
}

// SplitResult is the folio that now carries the moved charges.
type SplitResult struct {
	TargetFolio         FolioDetail `json:"targetFolio"`
	TargetCustomerFolio uuid.UUID   `json:"targetCustomerFolioId"`
	Moved               string      `json:"moved"`
}

// SplitCharges moves (a share of) charge lines to another payer across
// business lines. The original line stays; the source folio gets a negative
// transfer line and the payer's split folio a positive one with the same
// business line, revenue component, tax and service, so revenue reports are
// unchanged and the evidence chain is kept.
func (s *Service) SplitCharges(ctx context.Context, tx pgx.Tx, property uuid.UUID, in SplitInput) (SplitResult, error) {
	if len(in.LineIDs) == 0 {
		return SplitResult{}, handle.Invalid("lineIds", "required", "choose the charges to split")
	}
	if err := handle.Required("reason", in.Reason); err != nil {
		return SplitResult{}, err
	}
	pct, err := handle.Decimal("percent", in.Percent, decimal.NewFromInt(100))
	if err != nil {
		return SplitResult{}, err
	}
	if !pct.IsPositive() || pct.GreaterThan(decimal.NewFromInt(100)) {
		return SplitResult{}, handle.Invalid("percent", "invalid", "between 0 and 100")
	}
	share := pct.Div(decimal.NewFromInt(100))
	payer := CustomerFolioInput{CustomerID: in.CustomerID, CorporateAccountID: in.CorporateAccountID}
	cf, err := s.EnsureCustomerFolio(ctx, tx, property, payer)
	if err != nil {
		return SplitResult{}, err
	}
	type srcLine struct {
		ID, FolioID                    uuid.UUID
		FolioNo, Desc, Line, Component string
		ChargeType                     string
		Net, Tax, Svc, Total           decimal.Decimal
		Comps, Taxes                   []byte
		Snapshot                       *uuid.UUID
		Liability                      bool
		Status                         string
		Invoice                        *uuid.UUID
	}
	var lines []srcLine
	var sourceFolio *uuid.UUID
	for _, lid := range in.LineIDs {
		var l srcLine
		var net, tax, svc, total string
		if err := tx.QueryRow(ctx, `SELECT l.id, l.folio_id, f.number, l.description, l.business_line, coalesce(l.revenue_component, l.charge_type),
			l.charge_type, l.net_amount::text, l.tax_amount::text, l.service_amount::text, l.total::text, l.components, l.tax_lines, l.pricing_snapshot_id,
			l.liability, f.status, l.invoice_id
			FROM billing.folio_lines l JOIN billing.folios f ON f.id = l.folio_id
			WHERE l.id = $1 AND l.property_id = $2 AND l.voided_at IS NULL`, lid, property).
			Scan(&l.ID, &l.FolioID, &l.FolioNo, &l.Desc, &l.Line, &l.Component, &l.ChargeType, &net, &tax, &svc, &total, &l.Comps, &l.Taxes,
				&l.Snapshot, &l.Liability, &l.Status, &l.Invoice); err != nil {
			if dbtx.IsNoRows(err) {
				return SplitResult{}, errs.Validation("invalid_line", "charge line not found or voided", errs.Field("lineIds", "not_found", lid.String()))
			}
			return SplitResult{}, err
		}
		if l.Status != "open" {
			return SplitResult{}, ErrFolioClosed
		}
		if l.Invoice != nil {
			return SplitResult{}, errs.Conflict("line_invoiced", "charge "+l.Desc+" is already invoiced; void or credit the invoice first")
		}
		l.Net, l.Tax, l.Svc, l.Total = dec(net), dec(tax), dec(svc), dec(total)
		lines = append(lines, l)
		if sourceFolio == nil {
			sourceFolio = &l.FolioID
		}
	}
	// one split folio per source folio and payer keeps the business line
	targets := map[uuid.UUID]uuid.UUID{}
	cur, err := org.Currency(ctx, tx)
	if err != nil {
		return SplitResult{}, err
	}
	moved := decimal.Zero
	var lastTarget uuid.UUID
	for _, l := range lines {
		target, ok := targets[l.FolioID]
		if !ok {
			ref, err := s.OpenLineFolio(ctx, tx, LineFolioInput{FolioInput: FolioInput{Property: property, CustomerID: payer.CustomerID, HolderName: cf.HolderName,
				SourceType: "split", SourceID: &l.FolioID, SourceRef: l.FolioNo}, BusinessLine: l.Line})
			if err != nil {
				return SplitResult{}, err
			}
			if _, err := tx.Exec(ctx, `UPDATE billing.folios SET customer_folio_id = $2, corporate_account_id = $3, split_from_folio_id = $4 WHERE id = $1`,
				ref.ID, cf.ID, payer.CorporateAccountID, l.FolioID); err != nil {
				return SplitResult{}, err
			}
			target, targets[l.FolioID] = ref.ID, ref.ID
		}
		lastTarget = target
		net, tax, svc := round(l.Net.Mul(share), cur), round(l.Tax.Mul(share), cur), round(l.Svc.Mul(share), cur)
		total := net.Add(tax).Add(svc)
		comps := scaleComponents(l.Comps, share, cur)
		var taxes any
		_ = json.Unmarshal(l.Taxes, &taxes)
		lid := l.ID
		desc := fmt.Sprintf("Split %s%% of %s", pct.String(), l.Desc)
		if _, err := s.AddLineCharge(ctx, tx, LineCharge{Charge: Charge{FolioID: l.FolioID, Description: desc + " to " + cf.HolderName,
			Net: net.Neg(), Tax: tax.Neg(), Service: svc.Neg(), Total: total.Neg(), Components: negate(comps), SnapshotID: l.Snapshot,
			ReferenceType: "billing.split", ReferenceID: &lid, Liability: l.Liability}, BusinessLine: l.Line, RevenueComponent: l.Component,
			TaxLines: taxes}); err != nil {
			return SplitResult{}, err
		}
		if _, err := s.AddLineCharge(ctx, tx, LineCharge{Charge: Charge{FolioID: target, Description: desc + " from " + l.FolioNo,
			Net: net, Tax: tax, Service: svc, Total: total, Components: comps, SnapshotID: l.Snapshot, ReferenceType: "billing.split", ReferenceID: &lid,
			Liability: l.Liability}, BusinessLine: l.Line, RevenueComponent: l.Component, TaxLines: taxes}); err != nil {
			return SplitResult{}, err
		}
		moved = moved.Add(total)
	}
	fd, err := GetFolio(ctx, tx, lastTarget)
	if err != nil {
		return SplitResult{}, err
	}
	res := SplitResult{TargetFolio: fd, TargetCustomerFolio: cf.ID, Moved: moved.StringFixed(places(cur))}
	return res, audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "split", EntityType: "billing.folio", EntityID: sourceFolio.String(),
		EntityLabel: "Split to " + cf.HolderName, PropertyID: &property, Reason: in.Reason,
		After: map[string]any{"lines": in.LineIDs, "percent": pct.String(), "targetFolio": fd.Number, "moved": res.Moved}})
}

// scaleComponents scales the all-in components of a line (the accounting
// export reads them).
func scaleComponents(raw []byte, share decimal.Decimal, cur string) []map[string]any {
	var cs []map[string]any
	_ = json.Unmarshal(raw, &cs)
	out := make([]map[string]any, 0, len(cs))
	for _, c := range cs {
		x := map[string]any{}
		for k, v := range c {
			x[k] = v
		}
		if a, ok := c["amount"].(string); ok {
			x["amount"] = round(dec(a).Mul(share), cur).String()
		}
		out = append(out, x)
	}
	return out
}

func negate(cs []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(cs))
	for _, c := range cs {
		x := map[string]any{}
		for k, v := range c {
			x[k] = v
		}
		if a, ok := c["amount"].(string); ok {
			x["amount"] = dec(a).Neg().String()
		}
		out = append(out, x)
	}
	return out
}

// ── corporate credit (FR-BIL-P3-05) ───────────────────────────────────────

// CreditOverrideDocumentType approves extra credit for a corporate account
// over its limit.
var CreditOverrideDocumentType = provision.DocumentType{Code: "credit_override", Module: "billing", Name: "Credit Override",
	Attributes: []provision.DocumentAttribute{{Key: "amount", Label: "Amount", Type: "number"}}}

// CreditOverrideInput requests extra credit for an account.
type CreditOverrideInput struct {
	Amount string `json:"amount"`
	Reason string `json:"reason"`
}

// CreditOverride is a credit override request.
type CreditOverride struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	AccountID         uuid.UUID  `json:"accountId" db:"account_id"`
	Amount            string     `json:"amount" db:"amount"`
	Reason            string     `json:"reason" db:"reason"`
	ExpiresAt         time.Time  `json:"expiresAt" db:"expires_at"`
	Status            string     `json:"status" db:"status" enum:"pending,approved,rejected,cancelled"`
	ApprovalRequestID *uuid.UUID `json:"approvalRequestId" db:"approval_request_id"`
	CreatedAt         time.Time  `json:"createdAt" db:"created_at"`
}

// RequestCreditOverride submits a credit override for approval.
func (h *HTTP) RequestCreditOverride(ctx context.Context, tx pgx.Tx, property, accountID uuid.UUID, in CreditOverrideInput) (CreditOverride, error) {
	a, err := GetAccount(ctx, tx, accountID)
	if err != nil {
		return CreditOverride{}, err
	}
	amt, err := handle.Decimal("amount", in.Amount, decimal.Zero)
	if err != nil {
		return CreditOverride{}, err
	}
	if !amt.IsPositive() {
		return CreditOverride{}, handle.Invalid("amount", "invalid", "positive amount")
	}
	if err := handle.Required("reason", in.Reason); err != nil {
		return CreditOverride{}, err
	}
	pol, _, err := LoadCreditPolicy(ctx, tx, property)
	if err != nil {
		return CreditOverride{}, err
	}
	oid := id.New()
	exp := clock.Now().AddDate(0, 0, max(pol.OverrideValidDays, 1))
	if _, err := tx.Exec(ctx, `INSERT INTO billing.credit_overrides (id, property_id, account_id, amount, reason, expires_at, created_by)
		VALUES ($1,$2,$3,$4::numeric,$5,$6,$7)`, oid, property, accountID, amt.String(), in.Reason, exp, id.Ptr(actor(ctx))); err != nil {
		return CreditOverride{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionCreate, EntityType: "billing.credit_override", EntityID: oid.String(),
		EntityLabel: a.Number, PropertyID: &property, Reason: in.Reason, After: map[string]any{"amount": amt.String(), "expiresAt": exp}}); err != nil {
		return CreditOverride{}, err
	}
	f, _ := amt.Float64()
	rid, _, err := h.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: CreditOverrideDocumentType.Code, DocumentID: oid, DocumentRef: a.Number,
		Title: "Credit override " + a.Number + " · " + amt.String(), PropertyID: property, Attributes: map[string]any{"amount": f}})
	if err != nil {
		return CreditOverride{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE billing.credit_overrides SET approval_request_id = $2 WHERE id = $1`, oid, rid); err != nil {
		return CreditOverride{}, err
	}
	return oneOf[CreditOverride]("credit override")(tx.Query(ctx, `SELECT id, account_id, amount::text AS amount, reason, expires_at, status, approval_request_id, created_at
		FROM billing.credit_overrides WHERE id = $1`, oid))
}

// CreditOverrideDecision applies the approval decision.
func (h *HTTP) CreditOverrideDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	st := map[string]string{approval.StatusApproved: "approved", approval.StatusRejected: "rejected", approval.StatusCancelled: "cancelled"}[d.Status]
	if st == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE billing.credit_overrides SET status = $2, decided_at = now() WHERE id = $1 AND status = 'pending'`, d.DocumentID, st)
	if err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionStatusChange, EntityType: "billing.credit_override",
		EntityID: d.DocumentID.String(), PropertyID: &d.PropertyID, Reason: d.Reason, After: map[string]any{"status": st}})
}

// ChargeToAccountInput settles a folio balance on a corporate (or member)
// account: the city ledger (FR-BIL-P3-05).
type ChargeToAccountInput struct {
	CorporateAccountID *uuid.UUID `json:"corporateAccountId,omitempty"`
	AccountID          *uuid.UUID `json:"accountId,omitempty"`
	Amount             string     `json:"amount,omitempty" doc:"Default: the folio balance"`
}

// ChargeToAccount moves a folio balance to a corporate account (credit
// limit and overrides apply).
func (s *Service) ChargeToAccount(ctx context.Context, tx pgx.Tx, property, folioID uuid.UUID, in ChargeToAccountInput) (Payment, error) {
	var aid uuid.UUID
	switch {
	case in.AccountID != nil:
		aid = *in.AccountID
	case in.CorporateAccountID != nil:
		a, err := s.EnsureCorporateAccount(ctx, tx, property, *in.CorporateAccountID)
		if err != nil {
			return Payment{}, err
		}
		aid = a.ID
	default:
		return Payment{}, errs.Validation("account_required", "choose the corporate account", errs.Field("corporateAccountId", "required", "required"))
	}
	f, err := GetFolio(ctx, tx, folioID)
	if err != nil {
		return Payment{}, err
	}
	amt, err := handle.Decimal("amount", in.Amount, dec(f.Summary.Balance))
	if err != nil {
		return Payment{}, err
	}
	if !amt.IsPositive() {
		return Payment{}, errs.Conflict("nothing_due", "the folio has no balance to charge")
	}
	return s.TakeTender(ctx, tx, TenderPaymentInput{PaymentInput: PaymentInput{FolioID: &folioID, AccountID: &aid, MethodType: "member_account",
		Amount: amt, Description: "Charge to account " + strings.TrimSpace(f.Number)}})
}
