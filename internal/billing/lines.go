package billing

// P2 extension of the billing public interface (PRD P2 EP-03, contracts
// C1–C3 of §5.4.2): every business line posts to P1's folios, member
// charge works across lines (and offline at the POS), tenders such as
// Voucher & Prepaid plug into TakePayment, the deferred revenue sub-ledger
// keeps the voucher / prepaid / annual fee liability, and partner payouts
// settle what the club holds for caddies and instructors.

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
)

// Business lines of folios, lines and member account entries (statement
// sections, FR-BIL-P2-02).
const (
	LineGolf       = "golf"
	LineSport      = "sportclub"
	LineStay       = "stay"
	LinePOS        = "pos"
	LineMembership = "membership"
	LineVoucher    = "voucher"
	LineOther      = "other"
)

// BusinessLines lists the valid business lines.
var BusinessLines = []string{LineGolf, LineSport, LineStay, LinePOS, LineMembership, LineVoucher, LineOther}

// RevenueComponents of the accounting export (FR-BIL-P2-05) on top of P1's
// charge types.
var RevenueComponents = []string{
	"green_fee", "caddy_fee", "caddy_tip", "buggy_fee", "hio_insurance", "golf_other",
	"sport_entry", "court", "class", "registration_fee", "locker",
	"bungalow", "vip_suite", "meeting", "equipment",
	"fnb", "pro_shop", "driving_range",
	"voucher_deferred", "breakage",
	"membership_fee", "membership_annual_fee", "card_replacement_fee", "reactivation_fee", "nominee_fee",
	"caddy_fee_settlement", "instructor_fee",
	"cancellation_fee", "damage_charge", "late_checkout_fee", "deposit", "other",
}

func componentOf(line string) string {
	switch line {
	case LinePOS:
		return "fnb"
	case LineSport:
		return "sport_entry"
	case LineStay:
		return "bungalow"
	case LineGolf:
		return "golf_other"
	}
	return "other"
}

// ── C3: tenders ───────────────────────────────────────────────────────────

// TenderRequest is passed to a tender handler inside TakePayment.
type TenderRequest struct {
	PropertyID     uuid.UUID
	FolioID        uuid.UUID
	CustomerID     *uuid.UUID
	BusinessLine   string
	Amount         decimal.Decimal
	Data           map[string]any
	IdempotencyKey string
	Offline        bool
}

// TenderResult tells billing how much the tender covered and what to store.
type TenderResult struct {
	Amount decimal.Decimal
	Ref    map[string]any
}

// TenderHandler redeems a tender inside the payment transaction.
type TenderHandler func(ctx context.Context, tx pgx.Tx, r TenderRequest) (TenderResult, error)

var tenderMu sync.RWMutex
var tenders = map[string]TenderHandler{}

// RegisterTender plugs a tender (C3), e.g. voucher_prepaid from Commercial.
func (s *Service) RegisterTender(method string, h TenderHandler) {
	tenderMu.Lock()
	defer tenderMu.Unlock()
	tenders[method] = h
}

// applyTender redeems a voucher / prepaid balance or moves the amount to
// another open folio (Charge to Stay / Reservation, FR-BIL-P2-04).
func (s *Service) applyTender(ctx context.Context, tx pgx.Tx, property, folioID uuid.UUID, in TenderPaymentInput) (decimal.Decimal, map[string]any, error) {
	var number, line string
	var cust *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT number, business_line, customer_id FROM billing.folios WHERE id = $1`, folioID).Scan(&number, &line, &cust); err != nil {
		return decimal.Zero, nil, err
	}
	switch in.MethodType {
	case "voucher_prepaid":
		tenderMu.RLock()
		h, ok := tenders["voucher_prepaid"]
		tenderMu.RUnlock()
		if !ok {
			return decimal.Zero, nil, errs.Unavailable("voucher tender is not available")
		}
		key := in.IdempotencyKey
		if key == "" {
			key = id.New().String()
		}
		res, err := h(ctx, tx, TenderRequest{PropertyID: property, FolioID: folioID, CustomerID: cust, BusinessLine: line, Amount: in.Amount,
			Data: in.Tender, IdempotencyKey: key, Offline: in.Offline})
		return res.Amount, res.Ref, err
	default: // folio_transfer
		raw, _ := in.Tender["targetFolioId"].(string)
		target, err := uuid.Parse(raw)
		if err != nil || target == folioID {
			return decimal.Zero, nil, errs.Validation("invalid_target", "a different open target folio is required for a folio transfer",
				errs.Field("tender.targetFolioId", "invalid", "choose the stay / reservation folio"))
		}
		var targetNo string
		if err := tx.QueryRow(ctx, `SELECT number FROM billing.folios WHERE id = $1`, target).Scan(&targetNo); err != nil {
			if dbtx.IsNoRows(err) {
				return decimal.Zero, nil, errs.NotFound("target folio")
			}
			return decimal.Zero, nil, err
		}
		if _, err := s.AddLineCharge(ctx, tx, LineCharge{Charge: Charge{FolioID: target, ReferenceType: "billing.folio_transfer", ReferenceID: &folioID,
			Description: "Charged from " + number, Net: in.Amount}, BusinessLine: line, RevenueComponent: componentOf(line)}); err != nil {
			return decimal.Zero, nil, err
		}
		return in.Amount, map[string]any{"targetFolioId": target.String(), "targetFolioNumber": targetNo}, nil
	}
}

// ── C1: folio lookups and cancellation ────────────────────────────────────

// FolioBySource returns the open folio of a source (e.g. a stay), if any.
func FolioBySource(ctx context.Context, q dbtx.Querier, sourceType string, sourceID uuid.UUID) (*uuid.UUID, error) {
	var fid uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM billing.folios WHERE source_type = $1 AND source_id = $2 AND status = 'open'
		ORDER BY created_at DESC LIMIT 1`, sourceType, sourceID).Scan(&fid)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	return &fid, err
}

// FolioByReservation returns the open folio of a reservation, if any.
func FolioByReservation(ctx context.Context, q dbtx.Querier, reservationID uuid.UUID) (*uuid.UUID, error) {
	var fid uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM billing.folios WHERE reservation_id = $1 AND status = 'open' ORDER BY created_at DESC LIMIT 1`,
		reservationID).Scan(&fid)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	return &fid, err
}

// VoidSourceCharges voids the open charges of a source document and
// returns their total.
func (s *Service) VoidSourceCharges(ctx context.Context, tx pgx.Tx, referenceType string, referenceID uuid.UUID, reason string) (decimal.Decimal, error) {
	rows, err := tx.Query(ctx, `SELECT l.id, l.total::text FROM billing.folio_lines l JOIN billing.folios f ON f.id = l.folio_id
		WHERE l.reference_type = $1 AND l.reference_id = $2 AND l.voided_at IS NULL AND f.status = 'open'`, referenceType, referenceID)
	if err != nil {
		return decimal.Zero, err
	}
	type v struct {
		id  uuid.UUID
		amt string
	}
	var ls []v
	for rows.Next() {
		var x v
		if err := rows.Scan(&x.id, &x.amt); err != nil {
			rows.Close()
			return decimal.Zero, err
		}
		ls = append(ls, x)
	}
	rows.Close()
	total := decimal.Zero
	for _, l := range ls {
		if err := s.VoidCharge(ctx, tx, l.id, reason); err != nil {
			return total, err
		}
		total = total.Add(dec(l.amt))
	}
	return total, nil
}

// SettleCancellation voids the folio charges, posts the cancellation fee of
// the Cancellation Policy and refunds what was paid above it.
func (s *Service) SettleCancellation(ctx context.Context, tx pgx.Tx, folioID uuid.UUID, fee decimal.Decimal, businessLine, reason string) (decimal.Decimal, error) {
	status, err := FolioStatus(ctx, tx, folioID)
	if err != nil || status != "open" {
		return decimal.Zero, err
	}
	lines, err := LinesOf(ctx, tx, folioID)
	if err != nil {
		return decimal.Zero, err
	}
	for _, l := range lines {
		if err := s.VoidCharge(ctx, tx, l.ID, "cancelled: "+reason); err != nil {
			return decimal.Zero, err
		}
	}
	if fee.IsPositive() {
		if _, err := s.AddLineCharge(ctx, tx, LineCharge{Charge: Charge{FolioID: folioID, ChargeType: "cancellation_fee", ReferenceType: "billing.cancellation",
			ReferenceID: &folioID, Description: "Cancellation fee", Net: fee}, BusinessLine: businessLine, RevenueComponent: "cancellation_fee"}); err != nil {
			return decimal.Zero, err
		}
	}
	refunds, err := s.RefundFolio(ctx, tx, folioID, reason, nil)
	refunded := decimal.Zero
	for _, r := range refunds {
		refunded = refunded.Add(dec(r.Amount))
	}
	return refunded, err
}

// ReopenFolio reopens a closed folio (permission + reason + audit, FR-PAY-09).
func (s *Service) ReopenFolio(ctx context.Context, tx pgx.Tx, fid uuid.UUID, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return errs.Validation("reason_required", "a reason is required", errs.Field("reason", "required", "reason is required"))
	}
	var num string
	var pid uuid.UUID
	err := tx.QueryRow(ctx, `UPDATE billing.folios SET status = 'open', reopened_at = now(), reopened_by = $2, reopen_reason = $3, closed_at = NULL,
		version = version + 1 WHERE id = $1 AND status = 'closed' RETURNING number, property_id`, fid, id.Ptr(actor(ctx)), reason).Scan(&num, &pid)
	if dbtx.IsNoRows(err) {
		return errs.Conflict("folio_not_closed", "only closed folios can be reopened")
	}
	if err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "reopen", EntityType: "billing.folio", EntityID: fid.String(),
		EntityLabel: num, PropertyID: &pid, Reason: reason, Before: map[string]any{"status": "closed"}, After: map[string]any{"status": "open"}})
}

// ── C2: member account across business lines ──────────────────────────────

// SetAccountStatus suspends or reactivates the member account of a customer
// (Membership suspension blocks member charge, FR-MBL-10).
func (s *Service) SetAccountStatus(ctx context.Context, tx pgx.Tx, property, customerID uuid.UUID, status, reason string) error {
	a, err := AccountFor(ctx, tx, property, customerID, "member")
	if err != nil || a == nil || a.Status == status {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE billing.customer_accounts SET status = $2, status_reason = $3, updated_by = $4 WHERE id = $1`,
		a.ID, status, nullStr(reason), id.Ptr(actor(ctx))); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionStatusChange, EntityType: "billing.customer_account",
		EntityID: a.ID.String(), EntityLabel: a.Number, PropertyID: &property, Reason: reason,
		Before: map[string]any{"status": a.Status}, After: map[string]any{"status": status}})
}

// lineLabels name the business lines on the Member Statement.
var lineLabels = map[string]string{LineGolf: "Golf", LineSport: "Sport Club", LineStay: "Stay & Venue", LinePOS: "F&B / POS",
	LineMembership: "Membership", LineVoucher: "Voucher", LineOther: "Other"}

// LineTotal is the charges of one business line in a statement period.
type LineTotal struct {
	BusinessLine string          `json:"businessLine"`
	Label        string          `json:"label"`
	Charges      decimal.Decimal `json:"charges"`
}

// chargesByLine totals the charges of statement lines per business line
// (FR-BIL-P2-02), in the order of BusinessLines; charges without a line
// count as other.
func chargesByLine(lines []stmtLine) []LineTotal {
	sum := map[string]decimal.Decimal{}
	for _, l := range lines {
		if amt := dec(l.Amount); amt.IsPositive() {
			bl := l.Line
			if !slices.Contains(BusinessLines, bl) {
				bl = LineOther
			}
			sum[bl] = sum[bl].Add(amt)
		}
	}
	out := []LineTotal{}
	for _, bl := range BusinessLines {
		if v, ok := sum[bl]; ok {
			out = append(out, LineTotal{BusinessLine: bl, Label: lineLabels[bl], Charges: v})
		}
	}
	return out
}

// tagEntries records the business line and source of the member account
// entries of a payment (combined Member Statement, FR-BIL-P2-02).
func tagEntries(ctx context.Context, tx pgx.Tx, paymentID uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE billing.account_entries e SET business_line = f.business_line, source_type = 'billing.payment', source_id = $1,
		offline = p.offline FROM billing.payments p LEFT JOIN billing.folios f ON f.id = p.folio_id WHERE p.id = $1 AND e.payment_id = $1`, paymentID)
	return err
}

// ── deferred revenue sub-ledger (FR-BIL-P2-06) ────────────────────────────

// DeferredEntry is one movement of a liability.
type DeferredEntry struct {
	PropertyID       uuid.UUID
	LiabilityType    string // voucher | prepaid | annual_fee | package
	RefType          string
	RefID            uuid.UUID
	EntryType        string // deferral | recognition | breakage | adjustment | reversal
	Amount           decimal.Decimal
	RevenueComponent string
	Description      string
	IdempotencyKey   string
	OccurredAt       *time.Time
}

// PostDeferred records a liability movement. The sign follows the entry
// type: deferral raises the liability, the others release it (adjustment
// keeps the given sign). Idempotent per key.
func (s *Service) PostDeferred(ctx context.Context, tx pgx.Tx, e DeferredEntry) error {
	amt := e.Amount
	switch e.EntryType {
	case "deferral":
		amt = amt.Abs()
	case "recognition", "breakage", "reversal":
		amt = amt.Abs().Neg()
	}
	if amt.IsZero() {
		return nil
	}
	at := clock.Now()
	if e.OccurredAt != nil {
		at = *e.OccurredAt
	}
	_, err := tx.Exec(ctx, `INSERT INTO billing.deferred_revenue_entries (id, property_id, liability_type, ref_type, ref_id, entry_type, amount,
		revenue_component, description, idempotency_key, occurred_at, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9,$10,$11,$12) ON CONFLICT DO NOTHING`,
		id.New(), e.PropertyID, e.LiabilityType, e.RefType, e.RefID, e.EntryType, amt.String(), e.RevenueComponent, e.Description,
		nullStr(e.IdempotencyKey), at, id.Ptr(actor(ctx)))
	return err
}

// Liability returns the deferred balance of a type at a time (all types when "").
func Liability(ctx context.Context, q dbtx.Querier, property uuid.UUID, liabilityType string, at time.Time) (decimal.Decimal, error) {
	var raw string
	err := q.QueryRow(ctx, `SELECT coalesce(sum(amount), 0)::text FROM billing.deferred_revenue_entries WHERE property_id = $1
		AND ($2 = '' OR liability_type = $2) AND occurred_at <= $3`, property, liabilityType, at).Scan(&raw)
	return dec(raw), err
}

// ── payouts ───────────────────────────────────────────────────────────────

// PayoutRequest records money paid to a partner (caddy, instructor).
type PayoutRequest struct {
	PropertyID      uuid.UUID
	PayoutType      string // caddy_fee_settlement | instructor_fee
	BeneficiaryType string
	BeneficiaryID   uuid.UUID
	BeneficiaryName string
	SourceType      string
	SourceID        uuid.UUID
	Amount          decimal.Decimal
	MethodType      string // cash | bank_transfer
	Reference       string
}

// Payout is a recorded payout.
type Payout struct {
	ID              uuid.UUID `json:"id"`
	Number          string    `json:"number"`
	PayoutType      string    `json:"payoutType"`
	BeneficiaryType string    `json:"beneficiaryType"`
	BeneficiaryID   uuid.UUID `json:"beneficiaryId"`
	BeneficiaryName string    `json:"beneficiaryName"`
	Amount          string    `json:"amount"`
	MethodType      string    `json:"methodType"`
	Reference       *string   `json:"reference"`
	PaidAt          time.Time `json:"paidAt"`
}

// RecordPayout stores a payout (one per source document).
func (s *Service) RecordPayout(ctx context.Context, tx pgx.Tx, r PayoutRequest) (Payout, error) {
	if r.MethodType != "cash" && r.MethodType != "bank_transfer" {
		return Payout{}, errs.Validation("invalid_method", "payout method must be cash or bank_transfer",
			errs.Field("methodType", "invalid", "cash or bank_transfer"))
	}
	var cur string
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT currency FROM billing.customer_accounts WHERE property_id = $1 LIMIT 1), 'IDR')`,
		r.PropertyID).Scan(&cur); err != nil {
		return Payout{}, err
	}
	num, err := number(ctx, tx, r.PropertyID, "PO")
	if err != nil {
		return Payout{}, err
	}
	p := Payout{ID: id.New(), Number: num, PayoutType: r.PayoutType, BeneficiaryType: r.BeneficiaryType, BeneficiaryID: r.BeneficiaryID,
		BeneficiaryName: r.BeneficiaryName, Amount: r.Amount.String(), MethodType: r.MethodType, Reference: nullStr(r.Reference), PaidAt: clock.Now()}
	if _, err := tx.Exec(ctx, `INSERT INTO billing.payouts (id, property_id, number, payout_type, beneficiary_type, beneficiary_id, beneficiary_name,
		source_type, source_id, amount, currency, method_type, reference, paid_at, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::numeric,$11,$12,$13,$14,$15)`,
		p.ID, r.PropertyID, num, r.PayoutType, r.BeneficiaryType, r.BeneficiaryID, r.BeneficiaryName, r.SourceType, r.SourceID, p.Amount, cur,
		r.MethodType, p.Reference, p.PaidAt, id.Ptr(actor(ctx))); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return Payout{}, errs.Conflict("already_paid", "this document has already been paid out")
		}
		return Payout{}, err
	}
	return p, audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionCreate, EntityType: "billing.payout",
		EntityID: p.ID.String(), EntityLabel: num + " · " + r.BeneficiaryName, PropertyID: &r.PropertyID, After: p})
}

// ── website / app checkout (FR-WEB-P2-04, FR-APP-P2-07) ───────────────────

// Checkout is the payment step of a website / app booking.
type Checkout struct {
	FolioID     uuid.UUID `json:"folioId"`
	Total       string    `json:"total"`
	VoucherPaid string    `json:"voucherPaid" doc:"Covered by the voucher code"`
	AmountDue   string    `json:"amountDue"`
	Online      *Payment  `json:"online" doc:"Pending gateway payment for the amount due (or the deposit)"`
}

// CheckoutRequest pays a folio from a website / app booking.
type CheckoutRequest struct {
	FolioID     uuid.UUID
	VoucherCode string
	Method      string          // qris | virtual_account | card; "" = pay later
	Amount      decimal.Decimal // e.g. the deposit; zero = the balance
	Description string
	PayerName   string
}

// Checkout redeems an optional voucher code and opens the online payment
// (P1 gateway payment, settled by the signed webhook).
func (s *Service) Checkout(ctx context.Context, tx pgx.Tx, r CheckoutRequest) (Checkout, error) {
	out := Checkout{FolioID: r.FolioID, VoucherPaid: "0"}
	sum, err := FolioSummary(ctx, tx, r.FolioID)
	if err != nil {
		return out, err
	}
	out.Total = sum.Charges
	if due := dec(sum.Balance); r.VoucherCode != "" && due.IsPositive() {
		p, err := s.TakeTender(ctx, tx, TenderPaymentInput{PaymentInput: PaymentInput{FolioID: &r.FolioID, MethodType: "voucher_prepaid", Amount: due,
			Description: r.Description}, Tender: map[string]any{"code": r.VoucherCode}})
		if err != nil {
			return out, err
		}
		out.VoucherPaid = p.Amount
	}
	if sum, err = FolioSummary(ctx, tx, r.FolioID); err != nil {
		return out, err
	}
	due := dec(sum.Balance)
	if r.Method != "" && due.IsPositive() {
		amt := r.Amount
		if amt.IsZero() || amt.GreaterThan(due) {
			amt = due
		}
		p, err := s.TakePayment(ctx, tx, PaymentInput{FolioID: &r.FolioID, MethodType: r.Method, Channel: "online", Amount: amt,
			Description: r.Description, PayerName: r.PayerName})
		if err != nil {
			return out, err
		}
		out.Online = &p
		if sum, err = FolioSummary(ctx, tx, r.FolioID); err != nil {
			return out, err
		}
	}
	out.AmountDue = sum.Balance
	return out, nil
}
