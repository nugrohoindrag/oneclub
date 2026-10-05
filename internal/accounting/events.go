package accounting

// Event subscribers of accounting (docs/p3-p4-contracts.md): payloads are
// decoded into accounting's own structs (no import of the publishers).

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/outbox"
)

// ConsumedEvents lists the event types accounting subscribes to.
var ConsumedEvents = []string{
	"billing.business_day_closed", "billing.payment_settled", "billing.folio_closed", "billing.refund_processed",
	"billing.invoice_issued", "billing.invoice_paid", "billing.invoice_voided", "billing.credit_note_issued", "billing.invoice_written_off",
	"commercial.voucher_sold", "commercial.voucher_redeemed", "commercial.voucher_expired", "commercial.promotion_applied",
	"commercial.package_booked", "commercial.package_consumed", "membership.annual_fee_due", "membership.fee_paid",
	"sportclub.instructor_fee_approved", "golf.caddy_settlement_approved", "crm.loyalty_points_changed",
	"inventory.movement_posted", "inventory.asset_depreciated", "inventory.consignment_sold",
	"procurement.goods_received", "procurement.purchase_returned", "procurement.vendor_invoice_approved", "procurement.debit_note_issued",
	"commercial.sale_completed", "commercial.shift_closed", "golf.round_finished", "inventory.revaluation_posted",
}

func (m *Module) handlers() map[string]evHandler {
	return map[string]evHandler{
		"billing.business_day_closed":         m.onBusinessDayClosed,
		"billing.payment_settled":             m.onPaymentSettled,
		"billing.folio_closed":                m.onFolioClosed,
		"billing.refund_processed":            m.onRefundProcessed,
		"billing.invoice_issued":              m.onInvoiceIssued,
		"billing.invoice_paid":                m.onInvoicePaid,
		"billing.invoice_voided":              m.onInvoiceVoided,
		"billing.credit_note_issued":          m.onCreditNote,
		"billing.invoice_written_off":         m.onWrittenOff,
		"commercial.voucher_sold":             m.onVoucher,
		"commercial.voucher_redeemed":         m.onVoucher,
		"commercial.voucher_expired":          m.onVoucher,
		"commercial.promotion_applied":        m.onPromotionApplied,
		"commercial.package_booked":           m.onPackageBooked,
		"commercial.package_consumed":         m.onPackageConsumed,
		"membership.annual_fee_due":           m.onAnnualFee,
		"membership.fee_paid":                 m.onAnnualFee,
		"sportclub.instructor_fee_approved":   m.onInstructorFee,
		"golf.caddy_settlement_approved":      m.onCaddySettlement,
		"crm.loyalty_points_changed":          m.onLoyalty,
		"inventory.movement_posted":           m.onMovement,
		"inventory.asset_depreciated":         m.onDepreciation,
		"inventory.consignment_sold":          m.onConsignment,
		"procurement.goods_received":          m.onGoodsReceived,
		"procurement.purchase_returned":       m.onPurchaseReturned,
		"procurement.vendor_invoice_approved": m.onVendorInvoice,
		"procurement.debit_note_issued":       m.onDebitNote,
		"commercial.sale_completed":           m.onSaleCompleted,
		"commercial.shift_closed":             m.onShiftClosed,
		"golf.round_finished":                 m.onRoundFinished,
		"inventory.revaluation_posted":        m.onRevaluation,
	}
}

// Subscriptions returns one outbox subscriber per consumed event type.
func (m *Module) Subscriptions() map[string]outbox.Handler {
	out := map[string]outbox.Handler{}
	for t, h := range m.handlers() {
		out[t] = m.handler(h)
	}
	return out
}

func eventDate(ctx context.Context, tx pgx.Tx, ev Ev) time.Time {
	if ev.OccurredAt.IsZero() {
		return localToday(ctx, tx, ev.Property)
	}
	return localDate(ctx, tx, ev.Property, ev.OccurredAt)
}

// ── billing: K4 business day, payments, refunds ───────────────────────────

// onBusinessDayClosed posts the daily journal of a closed business day
// (contract K4): everything not yet posted up to that day.
func (m *Module) onBusinessDayClosed(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		BusinessDate string `json:"businessDate"`
		Charges      string `json:"charges"`
		PaymentTotal string `json:"paymentTotal"`
	}
	if err := ev.decode(&p); err != nil {
		return outcome{}, badPayload("business_day_closed payload: %v", err)
	}
	d, err := time.Parse("2006-01-02", p.BusinessDate)
	if err != nil {
		return outcome{}, badPayload("business_day_closed without a business date")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO accounting.business_days (property_id, business_date, summary) VALUES ($1,$2,$3)
		ON CONFLICT (property_id, business_date) DO UPDATE SET summary = EXCLUDED.summary, received_at = now()`, ev.Property, d, ev.Payload); err != nil {
		return outcome{}, err
	}
	out, err := m.sweepBilling(ctx, tx, ev.Property, sweepFilter{UpTo: &d, Revenue: true, Deferred: true, Payouts: true, AR: true},
		sweepHeader{Date: d, SourceType: ev.Type, SourceID: p.BusinessDate, SourceRef: "Business day " + p.BusinessDate, EventID: &ev.ID,
			Description: "Daily revenue " + p.BusinessDate})
	if err != nil {
		return out, err
	}
	if len(out.Journals) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE accounting.business_days SET journal_ids = journal_ids || $3 WHERE property_id = $1 AND business_date = $2`,
			ev.Property, d, out.Journals); err != nil {
			return out, err
		}
	}
	return out, nil
}

// postingMode returns the posting mode of a business line.
func postingMode(cfg AccountingConfiguration, line string) string {
	if v := cfg.PostingModes[line]; v != "" {
		return v
	}
	return "daily_summary"
}

// onPaymentSettled posts a settled payment with its folio in
// per-transaction mode (FR-PST-03); in daily summary mode the business day
// close posts it.
func (m *Module) onPaymentSettled(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		PaymentID uuid.UUID  `json:"paymentId"`
		Number    string     `json:"number"`
		FolioID   *uuid.UUID `json:"folioId"`
	}
	if err := ev.decode(&p); err != nil || p.PaymentID == uuid.Nil {
		return outcome{}, badPayload("payment_settled payload")
	}
	var line *string
	var bd *time.Time
	if err := tx.QueryRow(ctx, `SELECT folio_business_line, business_date FROM reporting.acc_payments WHERE id = $1`, p.PaymentID).Scan(&line, &bd); err != nil {
		if err == pgx.ErrNoRows {
			return outcome{Status: "no_posting", Note: "payment not found"}, nil
		}
		return outcome{}, err
	}
	cfg, err := LoadConfiguration(ctx, tx, ev.Property)
	if err != nil {
		return outcome{}, err
	}
	if p.FolioID == nil || postingMode(cfg, deref(line)) != "per_transaction" {
		return outcome{Status: "no_posting", Note: "posted with the daily summary at the business day close"}, nil
	}
	d := eventDate(ctx, tx, ev)
	if bd != nil {
		d = *bd
	}
	return m.sweepBilling(ctx, tx, ev.Property, sweepFilter{FolioID: p.FolioID, Revenue: true},
		sweepHeader{Date: d, SourceType: ev.Type, SourceID: p.PaymentID.String(), SourceRef: p.Number, EventID: &ev.ID, Description: "Payment " + p.Number})
}

// onFolioClosed posts what remains of a closed folio in per-transaction mode.
func (m *Module) onFolioClosed(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		FolioID uuid.UUID `json:"folioId"`
		Number  string    `json:"number"`
	}
	if err := ev.decode(&p); err != nil || p.FolioID == uuid.Nil {
		return outcome{}, badPayload("folio_closed payload")
	}
	var line string
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT business_line FROM reporting.acc_folio_lines WHERE folio_id = $1 LIMIT 1), '')`, p.FolioID).Scan(&line); err != nil {
		return outcome{}, err
	}
	cfg, err := LoadConfiguration(ctx, tx, ev.Property)
	if err != nil {
		return outcome{}, err
	}
	if postingMode(cfg, line) != "per_transaction" {
		return outcome{Status: "no_posting", Note: "posted with the daily summary at the business day close"}, nil
	}
	return m.sweepBilling(ctx, tx, ev.Property, sweepFilter{FolioID: &p.FolioID, Revenue: true},
		sweepHeader{Date: eventDate(ctx, tx, ev), SourceType: ev.Type, SourceID: p.FolioID.String(), SourceRef: p.Number, EventID: &ev.ID,
			Description: "Folio " + p.Number + " closed"})
}

// onRefundProcessed posts a refund per document.
func (m *Module) onRefundProcessed(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		RefundID uuid.UUID `json:"refundId"`
		Number   string    `json:"number"`
	}
	if err := ev.decode(&p); err != nil || p.RefundID == uuid.Nil {
		return outcome{}, badPayload("refund_processed payload")
	}
	d := eventDate(ctx, tx, ev)
	var bd *time.Time
	if err := tx.QueryRow(ctx, `SELECT business_date FROM reporting.acc_refunds WHERE id = $1`, p.RefundID).Scan(&bd); err == nil && bd != nil {
		d = *bd
	}
	return m.sweepBilling(ctx, tx, ev.Property, sweepFilter{RefundID: &p.RefundID, Revenue: true},
		sweepHeader{Date: d, SourceType: ev.Type, SourceID: p.RefundID.String(), SourceRef: p.Number, EventID: &ev.ID, Description: "Refund " + p.Number})
}

// ── commercial / membership: deferred revenue (K3, K8) ────────────────────

// onVoucher posts the deferred revenue entries of the voucher (sale →
// liability, redemption → revenue, expiry → breakage, FR-REV-02/03).
func (m *Module) onVoucher(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		VoucherID uuid.UUID `json:"voucherId"`
		Code      string    `json:"code"`
	}
	if err := ev.decode(&p); err != nil || p.VoucherID == uuid.Nil {
		return outcome{}, badPayload("%s payload", ev.Type)
	}
	d := eventDate(ctx, tx, ev)
	return m.sweepBilling(ctx, tx, ev.Property, sweepFilter{UpTo: &d, VoucherID: &p.VoucherID, Deferred: true},
		sweepHeader{Date: d, SourceType: ev.Type, SourceID: p.VoucherID.String(), SourceRef: p.Code, EventID: &ev.ID, Description: "Voucher " + p.Code})
}

// onAnnualFee posts the annual fee deferrals and monthly recognitions.
func (m *Module) onAnnualFee(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	d := eventDate(ctx, tx, ev)
	return m.sweepBilling(ctx, tx, ev.Property, sweepFilter{UpTo: &d, Liability: "annual_fee", Deferred: true},
		sweepHeader{Date: d, SourceType: ev.Type, SourceID: ev.ID.String(), EventID: &ev.ID, Description: "Annual membership fees " + ymd(d)})
}

func (m *Module) recordAllocation(ctx context.Context, tx pgx.Tx, ev Ev, sourceType string, sourceID uuid.UUID, ref, line string, total, discount decimal.Decimal,
	components any) error {
	raw, _ := json.Marshal(components)
	if components == nil {
		raw = []byte("[]")
	}
	_, err := tx.Exec(ctx, `INSERT INTO accounting.revenue_allocations (id, property_id, source_type, source_id, reference, business_line, total, discount, components, event_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8::numeric,$9,$10) ON CONFLICT (source_type, source_id, event_id) DO NOTHING`,
		id.New(), ev.Property, sourceType, sourceID, nz(ref), nz(line), total.String(), discount.String(), raw, ev.ID)
	return err
}

// onPromotionApplied records the discount of a promotion per business line
// (K3); the discount itself reaches the ledger with the folio lines.
func (m *Module) onPromotionApplied(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		PromotionID  uuid.UUID `json:"promotionId"`
		Code         string    `json:"code"`
		SourceType   string    `json:"sourceType"`
		SourceID     uuid.UUID `json:"sourceId"`
		BusinessLine string    `json:"businessLine"`
		Discount     string    `json:"discount"`
	}
	if err := ev.decode(&p); err != nil {
		return outcome{}, badPayload("promotion_applied payload")
	}
	src := p.SourceID
	if src == uuid.Nil {
		src = ev.ID
	}
	if err := m.recordAllocation(ctx, tx, ev, "promotion:"+p.SourceType, src, p.Code, p.BusinessLine, decimal.Zero, dec(p.Discount),
		[]map[string]any{{"promotionId": p.PromotionID, "discount": p.Discount}}); err != nil {
		return outcome{}, err
	}
	return outcome{Status: "no_posting", Note: "promotion discount recorded (posted with the folio lines)"}, nil
}

// onPackageBooked records the revenue allocation of a package (K3).
func (m *Module) onPackageBooked(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		BookingID  uuid.UUID        `json:"bookingId"`
		Number     string           `json:"number"`
		Total      string           `json:"total"`
		Components []map[string]any `json:"components"`
	}
	if err := ev.decode(&p); err != nil || p.BookingID == uuid.Nil {
		return outcome{}, badPayload("package_booked payload")
	}
	if err := m.recordAllocation(ctx, tx, ev, "package_booking", p.BookingID, p.Number, "package", dec(p.Total), decimal.Zero, p.Components); err != nil {
		return outcome{}, err
	}
	return outcome{Status: "no_posting", Note: "package allocation recorded; the package is posted with the folio lines and deferred revenue"}, nil
}

// onPackageConsumed records the consumption and posts the package deferred
// revenue recognised so far.
func (m *Module) onPackageConsumed(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		BookingID          uuid.UUID      `json:"bookingId"`
		BookingComponentID uuid.UUID      `json:"bookingComponentId"`
		Revenue            map[string]any `json:"revenue"`
	}
	if err := ev.decode(&p); err != nil || p.BookingID == uuid.Nil {
		return outcome{}, badPayload("package_consumed payload")
	}
	total := decimal.Zero
	if v, ok := p.Revenue["total"].(string); ok {
		total = dec(v)
	}
	if err := m.recordAllocation(ctx, tx, ev, "package_consumption", p.BookingComponentID, p.BookingID.String(), "package", total, decimal.Zero,
		[]map[string]any{p.Revenue}); err != nil {
		return outcome{}, err
	}
	d := eventDate(ctx, tx, ev)
	return m.sweepBilling(ctx, tx, ev.Property, sweepFilter{UpTo: &d, Liability: "package", Deferred: true},
		sweepHeader{Date: d, SourceType: ev.Type, SourceID: p.BookingID.String(), EventID: &ev.ID, Description: "Package revenue " + ymd(d)})
}

// ── partner fees, loyalty ─────────────────────────────────────────────────

// onInstructorFee accrues an approved instructor fee (paid later as a payout).
func (m *Module) onInstructorFee(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		FeeID        uuid.UUID `json:"feeId"`
		FeeNo        string    `json:"feeNo"`
		InstructorID uuid.UUID `json:"instructorId"`
		Amount       string    `json:"amount"`
	}
	if err := ev.decode(&p); err != nil || p.FeeID == uuid.Nil {
		return outcome{}, badPayload("instructor_fee_approved payload")
	}
	amt := dec(p.Amount)
	d := eventDate(ctx, tx, ev)
	instr := p.InstructorID
	var out outcome
	j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: ev.Property, Date: d, SourceType: ev.Type, SourceID: p.FeeID.String(),
		SourceRef: p.FeeNo, EventID: &ev.ID, Description: "Instructor fee " + p.FeeNo},
		Sources: []sourceMark{{Type: "sportclub.instructor_fee", ID: p.FeeID, BusinessDate: &d, Amount: amt, Items: []Item{{Source: ev.Type,
			Attrs: map[string]string{}, Amount: amt, Description: "Instructor fee " + p.FeeNo, Dims: Dims{PartnerType: "instructor", PartnerID: &instr},
			SourceType: "sportclub.instructor_fee", SourceID: p.FeeID.String()}}}}})
	out.add(j, missing)
	return out, err
}

// onCaddySettlement posts the caddy fee payouts recorded so far (the caddy
// fee liability is released when the settlement is paid).
func (m *Module) onCaddySettlement(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	d := eventDate(ctx, tx, ev)
	return m.sweepBilling(ctx, tx, ev.Property, sweepFilter{UpTo: &d, Payouts: true},
		sweepHeader{Date: d, SourceType: ev.Type, SourceID: ev.ID.String(), EventID: &ev.ID, Description: "Caddy settlement " + ymd(d)})
}

// onLoyalty posts the loyalty points liability (FR-REV-02/03): earned
// points are a liability, expired points breakage; redeemed points reach
// the ledger as the loyalty tender of the folio payment.
func (m *Module) onLoyalty(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		AccountID  uuid.UUID   `json:"accountId"`
		CustomerID *uuid.UUID  `json:"customerId"`
		Kind       string      `json:"kind"`
		Points     json.Number `json:"points"`
		SourceType string      `json:"sourceType"`
	}
	if err := ev.decode(&p); err != nil {
		return outcome{}, badPayload("loyalty_points_changed payload")
	}
	if p.Kind == "redeemed" {
		return outcome{Status: "no_posting", Note: "redeemed points are posted with the loyalty tender of the payment"}, nil
	}
	cfg, err := LoadConfiguration(ctx, tx, ev.Property)
	if err != nil {
		return outcome{}, err
	}
	pts := dec(p.Points.String())
	amt := pts.Mul(dec(cfg.LoyaltyPointValue)).Round(places(currency(ctx, tx)))
	if p.Kind != "adjusted" {
		amt = amt.Abs()
	}
	if amt.IsZero() {
		return outcome{Status: "no_posting", Note: "no value"}, nil
	}
	d := eventDate(ctx, tx, ev)
	var dims Dims
	if p.CustomerID != nil {
		c := *p.CustomerID
		dims = Dims{PartnerType: "customer", PartnerID: &c}
	}
	var out outcome
	j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: ev.Property, Date: d, SourceType: ev.Type, SourceID: p.AccountID.String(),
		EventID: &ev.ID, Description: "Loyalty points " + p.Kind},
		Sources: []sourceMark{{Type: "crm.loyalty_points", ID: ev.ID, BusinessDate: &d, Key1: p.Kind, Amount: amt, Items: []Item{{Source: ev.Type,
			Attrs: map[string]string{"kind": p.Kind, "sourceType": p.SourceType}, Amount: amt, Dims: dims, DimSide: "credit",
			Description: "Loyalty points " + p.Kind + " (" + pts.String() + ")", SourceType: "crm.loyalty_account", SourceID: p.AccountID.String()}}}}})
	out.add(j, missing)
	return out, err
}

// ── inventory (P4 EP-05, EP-09) ───────────────────────────────────────────

type movementPayload struct {
	MovementID   uuid.UUID `json:"movementId"`
	Number       string    `json:"number"`
	MovementType string    `json:"movementType"`
	BusinessDate string    `json:"businessDate"`
	CostCenter   *string   `json:"costCenter"`
	SourceType   string    `json:"sourceType"`
	TotalCost    string    `json:"totalCost"`
	Lines        []struct {
		Quantity    string `json:"quantity"`
		TotalCost   string `json:"totalCost"`
		Consignment bool   `json:"consignment"`
	} `json:"lines"`
}

var inbound = map[string]bool{"receipt": true, "transfer_in": true, "production_in": true}

// onMovement posts the inventory journal of a stock movement: COGS,
// waste, adjustment / opname variance, production (FR-INV EP-05). Goods
// receipts and purchase returns are posted from procurement (GRNI).
func (m *Module) onMovement(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p movementPayload
	if err := ev.decode(&p); err != nil || p.MovementID == uuid.Nil {
		return outcome{}, badPayload("movement_posted payload")
	}
	if (p.MovementType == "receipt" && p.SourceType == "goods_receipt") || (p.MovementType == "return_out" && p.SourceType == "purchase_return") ||
		p.SourceType == "consignment" {
		return outcome{Status: "no_posting", Note: "posted from the procurement document"}, nil
	}
	if p.SourceType == "revaluation" {
		return outcome{Status: "no_posting", Note: "posted from inventory.revaluation_posted"}, nil
	}
	// signed value: stock in > 0, stock out < 0
	amt := decimal.Zero
	for _, l := range p.Lines {
		if l.Consignment {
			continue
		}
		c := dec(l.TotalCost).Abs()
		if dec(l.Quantity).IsNegative() {
			c = c.Neg()
		}
		amt = amt.Add(c)
	}
	if len(p.Lines) == 0 {
		amt = dec(p.TotalCost)
		if !inbound[p.MovementType] && amt.IsPositive() && p.MovementType != "adjustment" && p.MovementType != "opname" {
			amt = amt.Neg()
		}
	}
	if amt.IsZero() {
		return outcome{Status: "no_posting", Note: "no value"}, nil
	}
	d := eventDate(ctx, tx, ev)
	if t, err := time.Parse("2006-01-02", p.BusinessDate); err == nil {
		d = t
	}
	cc := deref(p.CostCenter)
	var out outcome
	j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: ev.Property, Date: d, SourceType: ev.Type, SourceID: p.MovementID.String(),
		SourceRef: p.Number, EventID: &ev.ID, Description: "Stock movement " + p.Number + " (" + p.MovementType + ")"},
		Sources: []sourceMark{{Type: "inventory.movement", ID: p.MovementID, BusinessDate: &d, Key1: p.MovementType, Key2: cc, Amount: amt,
			Items: []Item{{Source: ev.Type, Attrs: map[string]string{"movementType": p.MovementType, "sourceType": p.SourceType, "costCenter": cc},
				Amount: amt, Dims: Dims{CostCenter: cc}, DimSide: "credit", Description: p.Number, SourceType: "inventory.movement",
				SourceID: p.MovementID.String(), FallbackDebit: "inventory"}}}}})
	out.add(j, missing)
	return out, err
}

// onRevaluation posts the invoice price variance of a goods receipt
// (procurement.invoice_price_variance → inventory.revaluation_posted): the
// part still in stock revalues the inventory, the consumed part goes to
// cost of sales or price variance (Inventory Configuration); both clear the
// GRNI left by the vendor invoice. Negative amounts reverse the sides.
func (m *Module) onRevaluation(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		VendorInvoiceID uuid.UUID `json:"vendorInvoiceId"`
		Number          string    `json:"number"`
		GoodsReceiptID  uuid.UUID `json:"goodsReceiptId"`
		ConsumedTo      string    `json:"consumedTo"`
		Lines           []struct {
			StockAmount    string `json:"stockAmount"`
			ConsumedAmount string `json:"consumedAmount"`
		} `json:"lines"`
	}
	if err := ev.decode(&p); err != nil || p.VendorInvoiceID == uuid.Nil || p.GoodsReceiptID == uuid.Nil {
		return outcome{}, badPayload("revaluation_posted payload")
	}
	stock, consumed := decimal.Zero, decimal.Zero
	for _, l := range p.Lines {
		stock, consumed = stock.Add(dec(l.StockAmount)), consumed.Add(dec(l.ConsumedAmount))
	}
	if stock.IsZero() && consumed.IsZero() {
		return outcome{Status: "no_posting", Note: "no value"}, nil
	}
	to := p.ConsumedTo
	if to == "" {
		to = "cogs"
	}
	src := uuid.NewSHA1(p.VendorInvoiceID, []byte(p.GoodsReceiptID.String()))
	var items []Item
	for _, x := range []struct {
		part string
		amt  decimal.Decimal
	}{{"stock", stock}, {"consumed", consumed}} {
		if x.amt.IsZero() {
			continue
		}
		items = append(items, Item{Source: ev.Type, Attrs: map[string]string{"part": x.part, "consumedTo": to}, Amount: x.amt,
			Description: "Price variance " + p.Number, SourceType: "inventory.revaluation", SourceID: src.String(), FallbackCredit: "grni"})
	}
	d := eventDate(ctx, tx, ev)
	var out outcome
	j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: ev.Property, Date: d, SourceType: ev.Type, SourceID: src.String(),
		SourceRef: p.Number, EventID: &ev.ID, Description: "Invoice price variance " + p.Number},
		Sources: []sourceMark{{Type: "inventory.revaluation", ID: src, BusinessDate: &d, Key1: p.Number, Amount: stock.Add(consumed), Items: items}}})
	out.add(j, missing)
	return out, err
}

// onDepreciation posts the monthly depreciation run (PRD P4 §16 #5).
func (m *Module) onDepreciation(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		RunID  uuid.UUID `json:"runId"`
		Period string    `json:"period"`
		Lines  []struct {
			AssetCode string `json:"assetCode"`
			Category  string `json:"category"`
			Amount    string `json:"amount"`
		} `json:"lines"`
		Total string `json:"total"`
	}
	if err := ev.decode(&p); err != nil || p.RunID == uuid.Nil {
		return outcome{}, badPayload("asset_depreciated payload")
	}
	d := eventDate(ctx, tx, ev)
	if t, err := time.Parse("2006-01", p.Period); err == nil {
		d = monthEnd(t)
	}
	var items []Item
	sum := decimal.Zero
	for _, l := range p.Lines {
		a := dec(l.Amount)
		sum = sum.Add(a)
		items = append(items, Item{Source: ev.Type, Attrs: map[string]string{"category": l.Category}, Amount: a, Description: "Depreciation " + l.AssetCode,
			Dims: Dims{CostCenter: l.Category}, DimSide: "debit", SourceType: "inventory.asset", SourceID: l.AssetCode})
	}
	if len(items) == 0 {
		sum = dec(p.Total)
		items = append(items, Item{Source: ev.Type, Attrs: map[string]string{}, Amount: sum, Description: "Depreciation " + p.Period})
	}
	var out outcome
	j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: ev.Property, Date: d, SourceType: ev.Type, SourceID: p.RunID.String(),
		SourceRef: p.Period, EventID: &ev.ID, Description: "Asset depreciation " + p.Period},
		Sources: []sourceMark{{Type: "inventory.depreciation_run", ID: p.RunID, BusinessDate: &d, Amount: sum, Items: items}}})
	out.add(j, missing)
	return out, err
}

// onConsignment books the cost and the consignment payable of a sold
// consignment item (AP to the supplier when sold, PRD P4 §16 #4).
func (m *Module) onConsignment(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		SupplierID uuid.UUID `json:"supplierId"`
		ItemID     uuid.UUID `json:"itemId"`
		Quantity   string    `json:"quantity"`
		TotalCost  string    `json:"totalCost"`
		SourceType string    `json:"sourceType"`
		SourceID   uuid.UUID `json:"sourceId"`
	}
	if err := ev.decode(&p); err != nil || p.SupplierID == uuid.Nil {
		return outcome{}, badPayload("consignment_sold payload")
	}
	amt := dec(p.TotalCost)
	d := eventDate(ctx, tx, ev)
	name, _, err := supplierInfo(ctx, tx, p.SupplierID)
	if err != nil {
		return outcome{}, err
	}
	sup := p.SupplierID
	var out outcome
	j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: ev.Property, Date: d, SourceType: ev.Type, SourceID: ev.ID.String(),
		EventID: &ev.ID, Description: "Consignment sold · " + name},
		Sources: []sourceMark{{Type: "inventory.consignment_sale", ID: ev.ID, BusinessDate: &d, Amount: amt, Items: []Item{{Source: ev.Type,
			Attrs: map[string]string{}, Amount: amt, Dims: Dims{PartnerType: "supplier", PartnerID: &sup, PartnerName: name}, DimSide: "credit",
			Description: "Consignment " + p.Quantity + " × item", SourceType: "inventory.item", SourceID: p.ItemID.String()}}}}})
	if err != nil {
		return out, err
	}
	out.add(j, missing)
	if ok, _ := isPosted(ctx, tx, "inventory.consignment_sale", ev.ID); ok {
		if err := upsertAPItem(ctx, tx, ev.Property, apItem{SupplierID: p.SupplierID, SupplierName: name, Type: "consignment", SourceID: ev.ID,
			Number: "CONS-" + ymd(d), InvoiceDate: d, DueDate: monthEnd(d).AddDate(0, 0, 10), Subtotal: amt, Amount: amt, Journal: journalID(j),
			Description: "Consignment sale " + p.SourceType}); err != nil {
			return out, err
		}
	}
	return out, nil
}

// ── procurement (P4 EP-14, EP-15, EP-19) ──────────────────────────────────

func (m *Module) onGoodsReceived(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		GoodsReceiptID uuid.UUID `json:"goodsReceiptId"`
		Number         string    `json:"number"`
		PONumber       string    `json:"poNumber"`
		SupplierID     uuid.UUID `json:"supplierId"`
		ReceivedDate   string    `json:"receivedDate"`
		Total          string    `json:"total"`
	}
	if err := ev.decode(&p); err != nil || p.GoodsReceiptID == uuid.Nil {
		return outcome{}, badPayload("goods_received payload")
	}
	d := eventDate(ctx, tx, ev)
	if t, err := time.Parse("2006-01-02", p.ReceivedDate); err == nil {
		d = t
	}
	name, _, err := supplierInfo(ctx, tx, p.SupplierID)
	if err != nil {
		return outcome{}, err
	}
	sup := p.SupplierID
	amt := dec(p.Total)
	var out outcome
	j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: ev.Property, Date: d, SourceType: ev.Type, SourceID: p.GoodsReceiptID.String(),
		SourceRef: p.Number, EventID: &ev.ID, Description: "Goods receipt " + p.Number + " (" + p.PONumber + ") · " + name},
		Sources: []sourceMark{{Type: "procurement.goods_receipt", ID: p.GoodsReceiptID, BusinessDate: &d, Key1: p.PONumber, Amount: amt,
			Items: []Item{{Source: ev.Type, Attrs: map[string]string{}, Amount: amt, Dims: Dims{PartnerType: "supplier", PartnerID: &sup, PartnerName: name},
				DimSide: "credit", Description: "GR " + p.Number, SourceType: "procurement.goods_receipt", SourceID: p.GoodsReceiptID.String(),
				FallbackDebit: "inventory"}}}}})
	out.add(j, missing)
	return out, err
}

func (m *Module) onPurchaseReturned(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		PurchaseReturnID uuid.UUID `json:"purchaseReturnId"`
		Number           string    `json:"number"`
		SupplierID       uuid.UUID `json:"supplierId"`
		Total            string    `json:"total"`
	}
	if err := ev.decode(&p); err != nil || p.PurchaseReturnID == uuid.Nil {
		return outcome{}, badPayload("purchase_returned payload")
	}
	d := eventDate(ctx, tx, ev)
	name, _, err := supplierInfo(ctx, tx, p.SupplierID)
	if err != nil {
		return outcome{}, err
	}
	sup := p.SupplierID
	amt := dec(p.Total)
	var out outcome
	j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: ev.Property, Date: d, SourceType: ev.Type, SourceID: p.PurchaseReturnID.String(),
		SourceRef: p.Number, EventID: &ev.ID, Description: "Purchase return " + p.Number + " · " + name},
		Sources: []sourceMark{{Type: "procurement.purchase_return", ID: p.PurchaseReturnID, BusinessDate: &d, Amount: amt,
			Items: []Item{{Source: ev.Type, Attrs: map[string]string{}, Amount: amt, Dims: Dims{PartnerType: "supplier", PartnerID: &sup, PartnerName: name},
				DimSide: "debit", Description: "Return " + p.Number, SourceType: "procurement.purchase_return", SourceID: p.PurchaseReturnID.String()}}}}})
	out.add(j, missing)
	return out, err
}

type vendorInvoicePayload struct {
	VendorInvoiceID   uuid.UUID `json:"vendorInvoiceId"`
	Number            string    `json:"number"`
	SupplierInvoiceNo string    `json:"supplierInvoiceNo"`
	SupplierID        uuid.UUID `json:"supplierId"`
	InvoiceDate       string    `json:"invoiceDate"`
	DueDate           string    `json:"dueDate"`
	Currency          string    `json:"currency"`
	Subtotal          string    `json:"subtotal"`
	TaxAmount         string    `json:"taxAmount"`
	Total             string    `json:"total"`
	Withholding       string    `json:"withholding"`
	TaxInvoiceNo      *string   `json:"taxInvoiceNo"`
	Lines             []struct {
		ItemID      *uuid.UUID `json:"itemId"`
		Description string     `json:"description"`
		Quantity    string     `json:"quantity"`
		UnitPrice   string     `json:"unitPrice"`
		Total       string     `json:"total"`
		AccountHint string     `json:"accountHint"`
	} `json:"lines"`
}

// onVendorInvoiceapproved opens the AP item of a matched vendor invoice:
// Dr GRNI / expense / asset, Dr VAT input, Cr AP (and withholding tax
// payable), FR-AP-01/05.
func (m *Module) onVendorInvoice(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p vendorInvoicePayload
	if err := ev.decode(&p); err != nil || p.VendorInvoiceID == uuid.Nil {
		return outcome{}, badPayload("vendor_invoice_approved payload")
	}
	name, npwp, err := supplierInfo(ctx, tx, p.SupplierID)
	if err != nil {
		return outcome{}, err
	}
	d := eventDate(ctx, tx, ev)
	if t, err := time.Parse("2006-01-02", p.InvoiceDate); err == nil {
		d = t
	}
	due := d.AddDate(0, 0, 30)
	if t, err := time.Parse("2006-01-02", p.DueDate); err == nil {
		due = t
	}
	sup := p.SupplierID
	dims := Dims{PartnerType: "supplier", PartnerID: &sup, PartnerName: name}
	src := p.VendorInvoiceID.String()
	var items []Item
	sum := decimal.Zero
	for _, l := range p.Lines {
		a := dec(l.Total)
		sum = sum.Add(a)
		hint := l.AccountHint
		if hint == "" {
			hint = "inventory"
		}
		items = append(items, Item{Source: "procurement.vendor_invoice_line", Attrs: map[string]string{"accountHint": hint}, Amount: a,
			Description: l.Description, Dims: dims, DimSide: "debit", SourceType: "procurement.vendor_invoice", SourceID: src, FallbackCredit: "posting_clearing"})
	}
	subtotal := dec(p.Subtotal)
	if len(p.Lines) == 0 {
		sum = subtotal
		items = append(items, Item{Source: "procurement.vendor_invoice_line", Attrs: map[string]string{"accountHint": "inventory"}, Amount: subtotal,
			Description: p.Number, Dims: dims, DimSide: "debit", SourceType: "procurement.vendor_invoice", SourceID: src, FallbackCredit: "posting_clearing"})
	}
	tax, wht, total := dec(p.TaxAmount), dec(p.Withholding), dec(p.Total)
	payable := total.Sub(wht)
	if tax.IsPositive() {
		items = append(items, Item{Source: "procurement.vendor_invoice_tax", Attrs: map[string]string{}, Amount: tax, Description: "PPN " + p.Number,
			SourceType: "procurement.vendor_invoice", SourceID: src, FallbackCredit: "posting_clearing"})
	}
	if wht.IsPositive() {
		items = append(items, Item{Source: "procurement.vendor_invoice_withholding", Attrs: map[string]string{}, Amount: wht, Description: "PPh " + p.Number,
			SourceType: "procurement.vendor_invoice", SourceID: src, FallbackDebit: "posting_clearing"})
	}
	items = append(items, Item{Source: "procurement.vendor_invoice_payable", Attrs: map[string]string{}, Amount: payable, Description: "AP " + p.Number,
		Dims: dims, DimSide: "credit", SourceType: "procurement.vendor_invoice", SourceID: src, FallbackDebit: "posting_clearing", FallbackCredit: "ap_control"})
	// rounding between lines, tax and total
	if diff := sum.Add(tax).Sub(total); !diff.IsZero() {
		cfg, err := LoadConfiguration(ctx, tx, ev.Property)
		if err != nil {
			return outcome{}, err
		}
		r := &resolver{tx: tx, ctx: ctx, property: ev.Property, cfg: cfg, byCode: map[string]acctInfo{}, byID: map[uuid.UUID]acctInfo{}}
		rnd, err := r.role("rounding")
		if err != nil {
			return outcome{}, err
		}
		clr, err := r.role("posting_clearing")
		if err != nil {
			return outcome{}, err
		}
		if rnd.Err == "" && clr.Err == "" {
			items = append(items, Item{Source: "accounting.rounding", Amount: diff, Description: "Rounding " + p.Number, DebitAccount: &clr.ID, CreditAccount: &rnd.ID})
		}
	}
	var out outcome
	j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: ev.Property, Date: d, SourceType: ev.Type, SourceID: src, SourceRef: p.Number,
		EventID: &ev.ID, Description: "Vendor invoice " + p.Number + " (" + p.SupplierInvoiceNo + ") · " + name},
		Sources: []sourceMark{{Type: "procurement.vendor_invoice", ID: p.VendorInvoiceID, BusinessDate: &d, Key1: p.SupplierInvoiceNo, Amount: payable, Items: items}}})
	if err != nil {
		return out, err
	}
	out.add(j, missing)
	if ok, _ := isPosted(ctx, tx, "procurement.vendor_invoice", p.VendorInvoiceID); ok {
		if err := upsertAPItem(ctx, tx, ev.Property, apItem{SupplierID: p.SupplierID, SupplierName: name, Type: "vendor_invoice", SourceID: p.VendorInvoiceID,
			Number: p.Number, SupplierInvoiceNo: p.SupplierInvoiceNo, InvoiceDate: d, DueDate: due, Subtotal: subtotal, Tax: tax, Withholding: wht,
			Amount: payable, TaxInvoiceNo: deref(p.TaxInvoiceNo), Journal: journalID(j), Description: "Vendor invoice " + p.Number}); err != nil {
			return out, err
		}
		if tax.IsPositive() {
			if err := m.createInputTaxInvoice(ctx, tx, ev.Property, "procurement.vendor_invoice", p.VendorInvoiceID, p.Number, p.SupplierID, name, npwp,
				d, subtotal, tax, deref(p.TaxInvoiceNo)); err != nil {
				return out, err
			}
		}
	}
	return out, nil
}

// onDebitNote reduces the payable to the supplier (FR-AP-04).
func (m *Module) onDebitNote(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		DebitNoteID     uuid.UUID  `json:"debitNoteId"`
		Number          string     `json:"number"`
		SupplierID      uuid.UUID  `json:"supplierId"`
		VendorInvoiceID *uuid.UUID `json:"vendorInvoiceId"`
		Amount          string     `json:"amount"`
		Reason          string     `json:"reason"`
	}
	if err := ev.decode(&p); err != nil || p.DebitNoteID == uuid.Nil {
		return outcome{}, badPayload("debit_note_issued payload")
	}
	name, _, err := supplierInfo(ctx, tx, p.SupplierID)
	if err != nil {
		return outcome{}, err
	}
	d := eventDate(ctx, tx, ev)
	amt := dec(p.Amount)
	sup := p.SupplierID
	var out outcome
	j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: ev.Property, Date: d, SourceType: ev.Type, SourceID: p.DebitNoteID.String(),
		SourceRef: p.Number, EventID: &ev.ID, Description: "Debit note " + p.Number + " · " + name + ": " + p.Reason},
		Sources: []sourceMark{{Type: "procurement.debit_note", ID: p.DebitNoteID, BusinessDate: &d, Amount: amt, Items: []Item{{Source: ev.Type,
			Attrs: map[string]string{}, Amount: amt, Dims: Dims{PartnerType: "supplier", PartnerID: &sup, PartnerName: name}, DimSide: "debit",
			Description: "DN " + p.Number, SourceType: "procurement.debit_note", SourceID: p.DebitNoteID.String()}}}}})
	if err != nil {
		return out, err
	}
	out.add(j, missing)
	if ok, _ := isPosted(ctx, tx, "procurement.debit_note", p.DebitNoteID); ok {
		var related *uuid.UUID
		if p.VendorInvoiceID != nil {
			var rid uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT id FROM accounting.ap_items WHERE item_type = 'vendor_invoice' AND source_id = $1`, *p.VendorInvoiceID).Scan(&rid); err == nil {
				related = &rid
			}
		}
		if err := upsertAPItem(ctx, tx, ev.Property, apItem{SupplierID: p.SupplierID, SupplierName: name, Type: "debit_note", SourceID: p.DebitNoteID,
			Number: p.Number, InvoiceDate: d, DueDate: d, Amount: amt.Neg(), Subtotal: amt.Neg(), Related: related, Journal: journalID(j),
			Description: "Debit note: " + p.Reason}); err != nil {
			return out, err
		}
	}
	return out, nil
}

// supplierInfo returns the name and NPWP of a supplier.
func supplierInfo(ctx context.Context, tx pgx.Tx, sid uuid.UUID) (string, string, error) {
	var name string
	var npwp *string
	err := tx.QueryRow(ctx, `SELECT name, npwp FROM reporting.acc_suppliers WHERE id = $1`, sid).Scan(&name, &npwp)
	if err == pgx.ErrNoRows {
		return "Supplier " + sid.String()[:8], "", nil
	}
	return name, deref(npwp), err
}

// ── K7 / K8 operational events without their own ledger effect ───────────

// onSaleCompleted traces a POS sale (K7): its revenue, tax and service
// reach the ledger with the folio lines of the order (daily summary or per
// transaction), its cost with the consumption movement of inventory.
func (m *Module) onSaleCompleted(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		OrderID uuid.UUID `json:"orderId"`
		OrderNo string    `json:"orderNo"`
		Total   string    `json:"total"`
	}
	if err := ev.decode(&p); err != nil || p.OrderID == uuid.Nil {
		return outcome{}, badPayload("sale_completed payload")
	}
	return outcome{Status: "no_posting", Note: "order " + p.OrderNo + " (" + p.Total + ") is posted with its folio lines; cost of sales with the inventory consumption"}, nil
}

// onShiftClosed posts the cash over / short of a closed POS shift.
func (m *Module) onShiftClosed(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		ShiftID  uuid.UUID `json:"shiftId"`
		ShiftNo  string    `json:"shiftNo"`
		Variance string    `json:"variance"`
	}
	if err := ev.decode(&p); err != nil || p.ShiftID == uuid.Nil {
		return outcome{}, badPayload("shift_closed payload")
	}
	if dec(p.Variance).IsZero() {
		return outcome{Status: "no_posting", Note: "shift " + p.ShiftNo + " without cash variance"}, nil
	}
	d := eventDate(ctx, tx, ev)
	var out outcome
	j, missing, err := m.sweepCash(ctx, tx, ev.Property, sweepHeader{Date: d, SourceType: ev.Type, SourceID: p.ShiftID.String(), SourceRef: p.ShiftNo,
		EventID: &ev.ID, Description: "Shift " + p.ShiftNo + " closed"}, m.cutOver(ctx, tx, ev.Property), d)
	out.add(j, missing)
	return out, err
}

// onRoundFinished traces a finished round: the caddy fee held for the
// caddies is already a liability of the folio line (FR-REV-05).
func (m *Module) onRoundFinished(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	return outcome{Status: "no_posting", Note: "caddy fee liability posted with the folio lines; released by the caddy settlement payout"}, nil
}
