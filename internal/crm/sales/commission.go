package sales

// EP-04 Sales Target & Commission. A deal (accepted quotation) earns
// commission when it is paid in full (PRD P3 §16 #4): money is attributed
// to the deal from billing events decoded by name — billing.invoice_paid,
// billing.payment_settled, billing.refund_processed, billing.invoice_voided
// (docs/p3-p4-contracts.md) — and the reporting read models of payment
// schedules: a schedule with source quotation belongs to that quotation,
// money of other schedules (e.g. banquet events) goes to the oldest unpaid
// deal of the same customer / company. Refunds and voids within the
// clawback window claw the commission back (FR-COM-05). Commission
// statements per sales and month are approved by Finance (approval engine),
// marked paid and exported for payroll (FR-COM-04).

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/platform/outbox"
)

// billing payloads (contract K2, P1–P2 payment events) decoded by name.
type billingInvoiceEvent struct {
	InvoiceID          uuid.UUID  `json:"invoiceId"`
	Number             *string    `json:"number"`
	CustomerID         *uuid.UUID `json:"customerId"`
	CorporateAccountID *uuid.UUID `json:"corporateAccountId"`
	Total              string     `json:"total"`
	PaidAmount         string     `json:"paidAmount"`
	Status             string     `json:"status"`
	Reason             string     `json:"reason"`
}

type billingPaymentEvent struct {
	PaymentID uuid.UUID `json:"paymentId"`
	Amount    string    `json:"amount"`
	Purpose   string    `json:"purpose"`
}

type billingRefundEvent struct {
	RefundID  uuid.UUID `json:"refundId"`
	PaymentID uuid.UUID `json:"paymentId"`
	Amount    string    `json:"amount"`
}

// moneySource is where billing money belongs (reporting read models).
type moneySource struct {
	scheduleID  *uuid.UUID
	sourceType  *string
	sourceID    *uuid.UUID
	customerID  *uuid.UUID
	corporateID *uuid.UUID
	invoiceID   *uuid.UUID
	amount      decimal.Decimal
}

// OnInvoicePaid attributes a paid invoice of a payment schedule to its deal.
func (m *Module) OnInvoicePaid(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p billingInvoiceEvent
	if err := e.Decode(&p); err != nil {
		return err
	}
	if e.PropertyID == nil || p.InvoiceID == uuid.Nil {
		return nil
	}
	var src moneySource
	var schedType *string
	var schedSource *uuid.UUID
	err := tx.QueryRow(ctx, `SELECT schedule_id, schedule_source_type, schedule_source_id, coalesce(schedule_customer_id, customer_id),
		coalesce(schedule_corporate_account_id, corporate_account_id) FROM reporting.sales_invoice_sources WHERE invoice_id = $1`, p.InvoiceID).
		Scan(&src.scheduleID, &schedType, &schedSource, &src.customerID, &src.corporateID)
	if err != nil && !dbtx.IsNoRows(err) {
		return err
	}
	if dbtx.IsNoRows(err) || src.scheduleID == nil {
		return nil // folio / account invoices are not deal money
	}
	src.sourceType, src.sourceID = schedType, schedSource
	src.amount = dec(p.Total)
	if v := dec(p.PaidAmount); v.IsPositive() {
		src.amount = v
	}
	return m.attributeMoney(ctx, tx, *e.PropertyID, "invoice", p.InvoiceID, src, e.OccurredAt)
}

// OnPaymentSettled attributes a payment of a payment schedule line that is
// not paid through an invoice (invoices are attributed by OnInvoicePaid).
func (m *Module) OnPaymentSettled(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p billingPaymentEvent
	if err := e.Decode(&p); err != nil {
		return err
	}
	if e.PropertyID == nil || p.PaymentID == uuid.Nil {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT invoice_id, schedule_id, schedule_source_type, schedule_source_id,
		coalesce(schedule_customer_id, folio_customer_id), schedule_corporate_account_id, coalesce(allocated_amount, 0)::text
		FROM reporting.sales_payment_sources WHERE payment_id = $1`, p.PaymentID)
	if err != nil {
		return err
	}
	var srcs []moneySource
	for rows.Next() {
		var s moneySource
		var amt string
		if err := rows.Scan(&s.invoiceID, &s.scheduleID, &s.sourceType, &s.sourceID, &s.customerID, &s.corporateID, &amt); err != nil {
			rows.Close()
			return err
		}
		s.amount = dec(amt)
		srcs = append(srcs, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	total := decimal.Zero
	var target *moneySource
	for i := range srcs {
		if srcs[i].invoiceID != nil {
			return nil // paid through an invoice
		}
		if srcs[i].scheduleID != nil {
			total = total.Add(srcs[i].amount)
			if target == nil {
				target = &srcs[i]
			}
		}
	}
	if target == nil || !total.IsPositive() {
		return nil
	}
	target.amount = total
	return m.attributeMoney(ctx, tx, *e.PropertyID, "payment", p.PaymentID, *target, e.OccurredAt)
}

// attributeMoney resolves the deal of schedule money and records it once.
func (m *Module) attributeMoney(ctx context.Context, tx pgx.Tx, property uuid.UUID, sourceType string, sourceID uuid.UUID, src moneySource, at time.Time) error {
	qid, err := m.resolveDeal(ctx, tx, property, src, at)
	if err != nil || qid == nil {
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO crm.sales_deal_payments (id, property_id, quotation_id, source_type, source_id, schedule_id, amount, occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8) ON CONFLICT (source_type, source_id) DO NOTHING`, id.New(), property, *qid, sourceType, sourceID,
		src.scheduleID, src.amount.String(), at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_quotations SET paid_amount = paid_amount + $2::numeric WHERE id = $1`, *qid, src.amount.String()); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "deal_payment", EntityType: "crm.quotation", EntityID: qid.String(),
		EntityLabel: sourceType + " " + sourceID.String(), PropertyID: &property, ActorName: "billing",
		After: map[string]any{"sourceType": sourceType, "sourceId": sourceID, "amount": src.amount.String(), "scheduleId": src.scheduleID}}); err != nil {
		return err
	}
	return m.evaluateDeal(ctx, tx, property, *qid, at)
}

// resolveDeal finds the accepted quotation the money belongs to.
func (m *Module) resolveDeal(ctx context.Context, tx pgx.Tx, property uuid.UUID, src moneySource, at time.Time) (*uuid.UUID, error) {
	if src.sourceType != nil && *src.sourceType == "quotation" && src.sourceID != nil {
		var qid uuid.UUID
		err := tx.QueryRow(ctx, `SELECT a.id FROM crm.sales_quotations q JOIN crm.sales_quotations a ON a.property_id = q.property_id
			AND a.number = q.number AND a.status = 'accepted' WHERE q.id = $1 AND q.property_id = $2 LIMIT 1`, *src.sourceID, property).Scan(&qid)
		if err == nil {
			return &qid, nil
		}
		if !dbtx.IsNoRows(err) {
			return nil, err
		}
		return nil, nil
	}
	if src.scheduleID == nil {
		return nil, nil
	}
	var qid uuid.UUID
	err := tx.QueryRow(ctx, `SELECT quotation_id FROM crm.sales_deal_payments WHERE schedule_id = $1 ORDER BY created_at LIMIT 1`, *src.scheduleID).Scan(&qid)
	if err == nil {
		return &qid, nil
	}
	if !dbtx.IsNoRows(err) {
		return nil, err
	}
	if src.customerID == nil && src.corporateID == nil {
		return nil, nil
	}
	err = tx.QueryRow(ctx, `SELECT id FROM crm.sales_quotations WHERE property_id = $1 AND status = 'accepted' AND paid_at IS NULL
		AND accepted_at <= $4 AND (($3::uuid IS NOT NULL AND corporate_account_id = $3) OR ($3::uuid IS NULL AND customer_id = $2))
		AND NOT EXISTS (SELECT 1 FROM crm.sales_deal_payments d WHERE d.quotation_id = crm.sales_quotations.id AND d.schedule_id IS NOT NULL
		  AND d.schedule_id <> $5)
		ORDER BY accepted_at LIMIT 1`, property, src.customerID, src.corporateID, at.Add(time.Minute), *src.scheduleID).Scan(&qid)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &qid, nil
}

// evaluateDeal recognises the commission once the deal is paid in full (or
// its payment schedule is completed).
func (m *Module) evaluateDeal(ctx context.Context, tx pgx.Tx, property, qid uuid.UUID, at time.Time) error {
	q, err := lockQuotation(ctx, tx, property, qid)
	if err != nil {
		return err
	}
	if q.Status != "accepted" || q.PaidAt != nil {
		return nil
	}
	paid := dec(q.PaidAmount).GreaterThanOrEqual(dec(q.Total))
	if !paid {
		var done bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM reporting.sales_schedules s WHERE s.status = 'completed'
			AND ((s.source_type = 'quotation' AND s.source_id IN (SELECT id FROM crm.sales_quotations WHERE property_id = $2 AND number = $3))
			  OR s.schedule_id IN (SELECT schedule_id FROM crm.sales_deal_payments WHERE quotation_id = $1 AND schedule_id IS NOT NULL)))`,
			qid, property, q.Number).Scan(&done); err != nil {
			return err
		}
		paid = done
	}
	if !paid {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_quotations SET paid_at = $2 WHERE id = $1`, qid, at); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "deal_paid", EntityType: "crm.quotation", EntityID: qid.String(),
		EntityLabel: q.Number + " v" + itoa(q.Version), PropertyID: &property, ActorName: "billing",
		After: map[string]any{"paidAmount": q.PaidAmount, "total": q.Total}}); err != nil {
		return err
	}
	q.PaidAt = &at
	return m.recognize(ctx, tx, property, q, at)
}

type scheme struct {
	ID           uuid.UUID
	Type, Basis  string
	Rates        []SchemeRate
	Tiers        []SchemeTier
	ClawbackDays int
}

func schemeFor(ctx context.Context, q dbtx.Querier, property, user uuid.UUID, day time.Time) (*scheme, error) {
	var s scheme
	var rates, tiers []byte
	err := q.QueryRow(ctx, `SELECT id, scheme_type, basis, rates, tiers, clawback_days FROM crm.sales_commission_schemes
		WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL AND effective_from <= $3 AND (effective_to IS NULL OR effective_to >= $3)
		AND (user_id = $2 OR (user_id IS NULL AND (team_id IS NULL OR team_id IN (SELECT team_id FROM crm.sales_team_members WHERE user_id = $2
		  AND status = 'active'))))
		ORDER BY (user_id IS NOT NULL) DESC, (team_id IS NOT NULL) DESC, effective_from DESC, created_at DESC LIMIT 1`, property, user, day).
		Scan(&s.ID, &s.Type, &s.Basis, &rates, &tiers, &s.ClawbackDays)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(rates, &s.Rates)
	_ = json.Unmarshal(tiers, &s.Tiers)
	return &s, nil
}

func (s *scheme) rate(line string) (SchemeRate, bool) {
	var fallback *SchemeRate
	for i := range s.Rates {
		switch s.Rates[i].Line {
		case line:
			return s.Rates[i], true
		case "", "*":
			fallback = &s.Rates[i]
		}
	}
	if fallback != nil {
		return *fallback, true
	}
	return SchemeRate{}, false
}

// achievementPercent is the owner's revenue achievement of the target in
// force on day (paid deals, this deal included).
func achievementPercent(ctx context.Context, q dbtx.Querier, property, user uuid.UUID, day time.Time, basis, tz string) (decimal.Decimal, bool) {
	var target, achieved string
	err := q.QueryRow(ctx, `SELECT t.target_revenue::text, coalesce((SELECT sum(CASE WHEN $4 = 'total' THEN x.total ELSE x.net_amount END)
		FROM crm.sales_quotations x WHERE x.property_id = $1 AND x.status = 'accepted' AND x.owner_user_id = $2 AND x.paid_at IS NOT NULL
		AND (x.paid_at AT TIME ZONE $5)::date BETWEEN t.period_start AND t.period_end AND (t.line IS NULL OR x.line = t.line)), 0)::text
		FROM crm.sales_targets t WHERE t.property_id = $1 AND t.user_id = $2 AND t.status = 'active' AND t.archived_at IS NULL
		AND $3::date BETWEEN t.period_start AND t.period_end AND t.target_revenue > 0 ORDER BY t.period_end - t.period_start LIMIT 1`,
		property, user, day, basis, tz).Scan(&target, &achieved)
	if err != nil {
		return decimal.Zero, false
	}
	return dec(achieved).Mul(hundred).Div(dec(target)).Round(2), true
}

// recognize books the earned commission of a paid deal (once).
func (m *Module) recognize(ctx context.Context, tx pgx.Tx, property uuid.UUID, q Quotation, at time.Time) error {
	if q.OwnerUserID == nil {
		return nil
	}
	pol, _, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return err
	}
	loc := location(ctx, tx, property)
	l := at.In(loc)
	day := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
	sc, err := schemeFor(ctx, tx, property, *q.OwnerUserID, day)
	if err != nil {
		return err
	}
	basisKind := pol.CommissionBasis
	if sc != nil {
		basisKind = sc.Basis
	}
	basis := dec(q.NetAmount)
	if basisKind == "total" {
		basis = dec(q.Total)
	}
	rate, amount := decimal.Zero, decimal.Zero
	var schemeID *uuid.UUID
	switch {
	case sc != nil && sc.Type == "flat":
		schemeID = &sc.ID
		if r, ok := sc.rate(q.Line); ok {
			amount = dec(r.FlatAmount)
		}
	case sc != nil && sc.Type == "tiered" && len(sc.Tiers) > 0:
		schemeID = &sc.ID
		ach, ok := achievementPercent(ctx, tx, property, *q.OwnerUserID, day, basisKind, loc.String())
		rate = dec(sc.Tiers[0].Percent)
		if ok {
			best := decimal.NewFromInt(-1)
			for _, t := range sc.Tiers {
				minA := dec(t.MinAchievementPercent)
				if !ach.LessThan(minA) && minA.GreaterThan(best) {
					best, rate = minA, dec(t.Percent)
				}
			}
		}
		amount = round(basis.Mul(rate).Div(hundred), q.Currency)
	case sc != nil:
		if r, ok := sc.rate(q.Line); ok {
			schemeID = &sc.ID
			rate = dec(r.Percent)
		} else {
			rate = dec(pol.CommissionRates[q.Line])
		}
		amount = round(basis.Mul(rate).Div(hundred), q.Currency)
	default:
		rate = dec(pol.CommissionRates[q.Line])
		amount = round(basis.Mul(rate).Div(hundred), q.Currency)
	}
	if amount.IsZero() {
		return nil
	}
	cid := id.New()
	tag, err := tx.Exec(ctx, `INSERT INTO crm.sales_commissions (id, property_id, user_id, quotation_id, opportunity_id, scheme_id, line, kind, basis_amount,
		rate_percent, amount, currency, recognized_on, period, source_type, source_id, reason)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'earned',$8::numeric,$9::numeric,$10::numeric,$11,$12,$13,'deal',$4,$14)
		ON CONFLICT (quotation_id, kind, source_type, source_id) WHERE quotation_id IS NOT NULL AND source_id IS NOT NULL DO NOTHING`,
		cid, property, *q.OwnerUserID, q.ID, q.OpportunityID, schemeID, q.Line, basis.String(), rate.String(), amount.String(), q.Currency, day,
		day.Format("2006-01"), "deal "+q.Number+" paid")
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionCreate, EntityType: "crm.sales_commission", EntityID: cid.String(),
		EntityLabel: q.Number + " · earned", PropertyID: &property, ActorName: "system",
		After: map[string]any{"userId": q.OwnerUserID, "basis": basis.String(), "rate": rate.String(), "amount": amount.String(), "period": day.Format("2006-01")}}); err != nil {
		return err
	}
	return m.notifyUsers(ctx, tx, property, []uuid.UUID{*q.OwnerUserID}, "crm.sales_commission_earned", m.staffLink("/crm/commissions"),
		map[string]any{"number": q.Number, "amount": formatAmount(amount, q.Currency), "currency": q.Currency, "period": day.Format("2006-01")})
}

// OnRefund claws back the commission of a refunded deal payment.
func (m *Module) OnRefund(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p billingRefundEvent
	if err := e.Decode(&p); err != nil {
		return err
	}
	if e.PropertyID == nil || p.RefundID == uuid.Nil {
		return nil
	}
	var qid uuid.UUID
	err := tx.QueryRow(ctx, `SELECT d.quotation_id FROM crm.sales_deal_payments d WHERE d.amount > 0 AND d.reversed_at IS NULL AND (
		(d.source_type = 'payment' AND d.source_id = $1) OR
		(d.source_type = 'invoice' AND d.source_id IN (SELECT invoice_id FROM reporting.sales_payment_sources WHERE payment_id = $1 AND invoice_id IS NOT NULL)))
		ORDER BY d.created_at LIMIT 1`, p.PaymentID).Scan(&qid)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	amount := dec(p.Amount)
	tag, err := tx.Exec(ctx, `INSERT INTO crm.sales_deal_payments (id, property_id, quotation_id, source_type, source_id, amount, occurred_at)
		VALUES ($1,$2,$3,'refund',$4,$5::numeric,$6) ON CONFLICT (source_type, source_id) DO NOTHING`, id.New(), *e.PropertyID, qid, p.RefundID,
		amount.Neg().String(), e.OccurredAt)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return m.reverseMoney(ctx, tx, *e.PropertyID, qid, "refund", p.RefundID, amount, e.OccurredAt, "refund")
}

// OnInvoiceVoided reverses the money of a voided invoice of a deal.
func (m *Module) OnInvoiceVoided(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p billingInvoiceEvent
	if err := e.Decode(&p); err != nil {
		return err
	}
	if e.PropertyID == nil || p.InvoiceID == uuid.Nil {
		return nil
	}
	var qid uuid.UUID
	var amt string
	err := tx.QueryRow(ctx, `UPDATE crm.sales_deal_payments SET reversed_at = $2, reverse_reason = $3 WHERE source_type = 'invoice' AND source_id = $1
		AND reversed_at IS NULL RETURNING quotation_id, amount::text`, p.InvoiceID, e.OccurredAt, nullStr(p.Reason)).Scan(&qid, &amt)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return m.reverseMoney(ctx, tx, *e.PropertyID, qid, "invoice_void", p.InvoiceID, dec(amt), e.OccurredAt, "invoice voided "+p.Reason)
}

// reverseMoney lowers the paid amount of a deal and claws the commission
// back in proportion when within the clawback window.
func (m *Module) reverseMoney(ctx context.Context, tx pgx.Tx, property, qid uuid.UUID, sourceType string, sourceID uuid.UUID, amount decimal.Decimal,
	at time.Time, reason string) error {
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_quotations SET paid_amount = paid_amount - $2::numeric WHERE id = $1`, qid, amount.String()); err != nil {
		return err
	}
	q, err := GetQuotation(ctx, tx, qid)
	if err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "deal_payment_reversed", EntityType: "crm.quotation", EntityID: qid.String(),
		EntityLabel: q.Number, PropertyID: &property, ActorName: "billing", Reason: reason,
		After: map[string]any{"sourceType": sourceType, "sourceId": sourceID, "amount": amount.String()}}); err != nil {
		return err
	}
	var earnedID, user uuid.UUID
	var earned, cur string
	var recognized time.Time
	err = tx.QueryRow(ctx, `SELECT id, user_id, amount::text, currency, recognized_on FROM crm.sales_commissions WHERE quotation_id = $1 AND kind = 'earned'
		ORDER BY created_at LIMIT 1`, qid).Scan(&earnedID, &user, &earned, &cur, &recognized)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	pol, _, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return err
	}
	window := pol.ClawbackDays
	var schemeDays *int
	_ = tx.QueryRow(ctx, `SELECT s.clawback_days FROM crm.sales_commissions c JOIN crm.sales_commission_schemes s ON s.id = c.scheme_id WHERE c.id = $1`,
		earnedID).Scan(&schemeDays)
	if schemeDays != nil {
		window = *schemeDays
	}
	if q.PaidAt != nil && at.Sub(*q.PaidAt) > time.Duration(window)*24*time.Hour {
		return nil
	}
	total := dec(q.Total)
	share := decimal.NewFromInt(1)
	if total.IsPositive() && amount.LessThan(total) {
		share = amount.Div(total)
	}
	var clawed string
	if err := tx.QueryRow(ctx, `SELECT coalesce(sum(amount), 0)::text FROM crm.sales_commissions WHERE quotation_id = $1 AND kind = 'clawback'`, qid).
		Scan(&clawed); err != nil {
		return err
	}
	claw := round(dec(earned).Mul(share), cur)
	if rest := dec(earned).Add(dec(clawed)); claw.GreaterThan(rest) {
		claw = rest
	}
	if !claw.IsPositive() {
		return nil
	}
	loc := location(ctx, tx, property)
	l := at.In(loc)
	day := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
	cid := id.New()
	tag, err := tx.Exec(ctx, `INSERT INTO crm.sales_commissions (id, property_id, user_id, quotation_id, opportunity_id, line, kind, basis_amount, amount,
		currency, recognized_on, period, source_type, source_id, reason)
		VALUES ($1,$2,$3,$4,$5,$6,'clawback',$7::numeric,$8::numeric,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (quotation_id, kind, source_type, source_id) WHERE quotation_id IS NOT NULL AND source_id IS NOT NULL DO NOTHING`,
		cid, property, user, qid, q.OpportunityID, q.Line, amount.String(), claw.Neg().String(), cur, day, day.Format("2006-01"), sourceType, sourceID, reason)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "clawback", EntityType: "crm.sales_commission", EntityID: cid.String(),
		EntityLabel: q.Number + " · clawback", PropertyID: &property, ActorName: "system", Reason: reason,
		After: map[string]any{"userId": user, "amount": claw.Neg().String(), "period": day.Format("2006-01")}})
}

// ── commission lines & statements ─────────────────────────────────────────

// CommissionLine is one earned / clawed back / adjusted amount.
type CommissionLine struct {
	ID              uuid.UUID  `json:"id" db:"id"`
	UserID          uuid.UUID  `json:"userId" db:"user_id"`
	UserName        *string    `json:"userName" db:"user_name"`
	QuotationID     *uuid.UUID `json:"quotationId" db:"quotation_id"`
	QuotationNumber *string    `json:"quotationNumber" db:"quotation_number"`
	OpportunityID   *uuid.UUID `json:"opportunityId" db:"opportunity_id"`
	Line            *string    `json:"line" db:"line"`
	Kind            string     `json:"kind" db:"kind" enum:"earned,clawback,adjustment"`
	BasisAmount     string     `json:"basisAmount" db:"basis_amount"`
	RatePercent     *string    `json:"ratePercent" db:"rate_percent"`
	Amount          string     `json:"amount" db:"amount"`
	Currency        string     `json:"currency" db:"currency"`
	RecognizedOn    string     `json:"recognizedOn" db:"recognized_on"`
	Period          string     `json:"period" db:"period"`
	StatementID     *uuid.UUID `json:"statementId" db:"statement_id"`
	SourceType      string     `json:"sourceType" db:"source_type" enum:"deal,refund,invoice_void,manual"`
	Reason          *string    `json:"reason" db:"reason"`
	CreatedAt       time.Time  `json:"createdAt" db:"created_at"`
}

const commissionSelect = `SELECT c.id, c.user_id, u.full_name AS user_name, c.quotation_id, q.number AS quotation_number, c.opportunity_id, c.line, c.kind,
	trim_scale(c.basis_amount)::text AS basis_amount, trim_scale(c.rate_percent)::text AS rate_percent, trim_scale(c.amount)::text AS amount, c.currency,
	to_char(c.recognized_on, 'YYYY-MM-DD') AS recognized_on, c.period, c.statement_id, c.source_type, c.reason, c.created_at
	FROM crm.sales_commissions c LEFT JOIN platform.users u ON u.id = c.user_id LEFT JOIN crm.sales_quotations q ON q.id = c.quotation_id`

// CommissionStatement is the commission of a sales for a month.
type CommissionStatement struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	Number            string     `json:"number" db:"number"`
	UserID            uuid.UUID  `json:"userId" db:"user_id"`
	UserName          *string    `json:"userName" db:"user_name"`
	UserEmail         *string    `json:"userEmail" db:"user_email"`
	Period            string     `json:"period" db:"period"`
	PeriodStart       string     `json:"periodStart" db:"period_start"`
	PeriodEnd         string     `json:"periodEnd" db:"period_end"`
	Earned            string     `json:"earned" db:"earned"`
	Clawback          string     `json:"clawback" db:"clawback"`
	Adjustments       string     `json:"adjustments" db:"adjustments"`
	Total             string     `json:"total" db:"total"`
	Currency          string     `json:"currency" db:"currency"`
	Status            string     `json:"status" db:"status" enum:"draft,pending_approval,approved,rejected,paid"`
	ApprovalRequestID *uuid.UUID `json:"approvalRequestId" db:"approval_request_id"`
	SubmittedAt       *time.Time `json:"submittedAt" db:"submitted_at"`
	ApprovedAt        *time.Time `json:"approvedAt" db:"approved_at"`
	RejectedReason    *string    `json:"rejectedReason" db:"rejected_reason"`
	PaidAt            *time.Time `json:"paidAt" db:"paid_at"`
	PaidReference     *string    `json:"paidReference" db:"paid_reference"`
	CreatedAt         time.Time  `json:"createdAt" db:"created_at"`
}

const statementSelect = `SELECT s.id, s.number, s.user_id, u.full_name AS user_name, u.email::text AS user_email, s.period,
	to_char(s.period_start, 'YYYY-MM-DD') AS period_start, to_char(s.period_end, 'YYYY-MM-DD') AS period_end, trim_scale(s.earned)::text AS earned,
	trim_scale(s.clawback)::text AS clawback, trim_scale(s.adjustments)::text AS adjustments, trim_scale(s.total)::text AS total, s.currency, s.status,
	s.approval_request_id, s.submitted_at, s.approved_at, s.rejected_reason, s.paid_at, s.paid_reference, s.created_at
	FROM crm.sales_commission_statements s LEFT JOIN platform.users u ON u.id = s.user_id`

// StatementDetail is a statement with its lines.
type StatementDetail struct {
	CommissionStatement
	Lines []CommissionLine `json:"lines"`
}

func statementDetail(ctx context.Context, q dbtx.Querier, sid uuid.UUID) (StatementDetail, error) {
	s, err := getOne[CommissionStatement]("commission statement")(q.Query(ctx, statementSelect+` WHERE s.id = $1`, sid))
	if err != nil {
		return StatementDetail{}, err
	}
	lines, err := handle.List[CommissionLine](q.Query(ctx, commissionSelect+` WHERE c.statement_id = $1 ORDER BY c.recognized_on, c.created_at`, sid))
	return StatementDetail{CommissionStatement: s, Lines: lines}, err
}

var periodRe = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

// GenerateInput generates the statements of a month.
type GenerateInput struct {
	Period string `json:"period" doc:"YYYY-MM"`
}

// GenerateStatements builds (or refreshes the drafts of) the commission
// statements of a month: every unassigned commission line up to the month
// joins the statement of its sales; lines of an already submitted month
// carry over to the next statement.
func (m *Module) GenerateStatements(ctx context.Context, tx pgx.Tx, property uuid.UUID, in GenerateInput) ([]CommissionStatement, error) {
	if !periodRe.MatchString(in.Period) {
		return nil, handle.Invalid("period", "invalid", "YYYY-MM")
	}
	start, _ := time.Parse("2006-01", in.Period)
	end := start.AddDate(0, 1, -1)
	rows, err := tx.Query(ctx, `SELECT user_id FROM crm.sales_commissions WHERE property_id = $1 AND statement_id IS NULL AND period <= $2
		UNION SELECT user_id FROM crm.sales_commission_statements WHERE property_id = $1 AND period = $2 AND status IN ('draft', 'rejected')`, property, in.Period)
	if err != nil {
		return nil, err
	}
	users, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, err
	}
	loc := location(ctx, tx, property)
	cur := "IDR"
	_ = tx.QueryRow(ctx, `SELECT currency FROM platform.instance`).Scan(&cur)
	n := 0
	for _, u := range users {
		var sid uuid.UUID
		var status string
		err := tx.QueryRow(ctx, `SELECT id, status FROM crm.sales_commission_statements WHERE property_id = $1 AND user_id = $2 AND period = $3 FOR UPDATE`,
			property, u, in.Period).Scan(&sid, &status)
		switch {
		case dbtx.IsNoRows(err):
			number, err := numbering.Next(ctx, tx, property, "COM", clock.Now().In(loc))
			if err != nil {
				return nil, err
			}
			sid = id.New()
			if _, err := tx.Exec(ctx, `INSERT INTO crm.sales_commission_statements (id, property_id, number, user_id, period, period_start, period_end,
				currency, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)`, sid, property, number, u, in.Period, start, end, cur, actor(ctx)); err != nil {
				return nil, err
			}
		case err != nil:
			return nil, err
		case status != "draft" && status != "rejected":
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.sales_commissions SET statement_id = $3 WHERE property_id = $1 AND user_id = $2 AND statement_id IS NULL
			AND period <= $4`, property, u, sid, in.Period); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.sales_commission_statements s SET status = 'draft', rejected_reason = NULL,
			earned = coalesce((SELECT sum(amount) FROM crm.sales_commissions WHERE statement_id = s.id AND kind = 'earned'), 0),
			clawback = coalesce((SELECT sum(amount) FROM crm.sales_commissions WHERE statement_id = s.id AND kind = 'clawback'), 0),
			adjustments = coalesce((SELECT sum(amount) FROM crm.sales_commissions WHERE statement_id = s.id AND kind = 'adjustment'), 0),
			total = coalesce((SELECT sum(amount) FROM crm.sales_commissions WHERE statement_id = s.id), 0), updated_by = $2 WHERE s.id = $1`,
			sid, actor(ctx)); err != nil {
			return nil, err
		}
		n++
	}
	out, err := handle.List[CommissionStatement](tx.Query(ctx, statementSelect+` WHERE s.property_id = $1 AND s.period = $2 ORDER BY u.full_name`,
		property, in.Period))
	if err != nil {
		return nil, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "generate", EntityType: "crm.commission_statement", EntityID: in.Period,
		EntityLabel: "Commission statements " + in.Period, PropertyID: &property, After: map[string]any{"period": in.Period, "statements": n}})
}

func lockStatement(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID) (StatementDetail, error) {
	var x uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM crm.sales_commission_statements WHERE id = $1 AND property_id = $2 FOR UPDATE`, sid, property).Scan(&x); err != nil {
		if dbtx.IsNoRows(err) {
			return StatementDetail{}, errs.NotFound("commission statement")
		}
		return StatementDetail{}, err
	}
	return statementDetail(ctx, tx, sid)
}

// SubmitStatement sends a statement to Finance approval.
func (m *Module) SubmitStatement(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID) (StatementDetail, error) {
	s, err := lockStatement(ctx, tx, property, sid)
	if err != nil {
		return s, err
	}
	if s.Status != "draft" && s.Status != "rejected" {
		return s, conflict("statement_not_draft", "the statement is "+s.Status)
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_commission_statements SET status = 'pending_approval', submitted_at = now(), updated_by = $2 WHERE id = $1`,
		sid, actor(ctx)); err != nil {
		return s, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "submit_approval", EntityType: "crm.commission_statement", EntityID: sid.String(),
		EntityLabel: s.Number, PropertyID: &property, Before: map[string]any{"status": s.Status}, After: map[string]any{"status": "pending_approval"}}); err != nil {
		return s, err
	}
	total, _ := dec(s.Total).Float64()
	rid, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: StatementDocumentType.Code, DocumentID: sid, DocumentRef: s.Number,
		Title: "Commission " + s.Period + " · " + deref(s.UserName) + " · " + s.Total, PropertyID: property, Attributes: map[string]any{"total": total}})
	if err != nil {
		return s, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_commission_statements SET approval_request_id = $2 WHERE id = $1`, sid, rid); err != nil {
		return s, err
	}
	return statementDetail(ctx, tx, sid)
}

// StatementDecision applies the approval decision (approval engine hook).
func (m *Module) StatementDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	var status, number, period, total, cur string
	var user uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT status, number, period, total::text, currency, user_id FROM crm.sales_commission_statements WHERE id = $1 FOR UPDATE`,
		d.DocumentID).Scan(&status, &number, &period, &total, &cur, &user); err != nil {
		if dbtx.IsNoRows(err) {
			return nil
		}
		return err
	}
	if status != "pending_approval" {
		return nil
	}
	next := map[string]string{approval.StatusApproved: "approved", approval.StatusRejected: "rejected", approval.StatusCancelled: "draft"}[d.Status]
	if next == "" {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_commission_statements SET status = $2, approved_at = CASE WHEN $2 = 'approved' THEN now() END,
		rejected_reason = CASE WHEN $2 = 'rejected' THEN $3 END WHERE id = $1`, d.DocumentID, next, nullStr(d.Reason)); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: next, EntityType: "crm.commission_statement", EntityID: d.DocumentID.String(),
		EntityLabel: number, PropertyID: &d.PropertyID, Reason: d.Reason, Before: map[string]any{"status": status}, After: map[string]any{"status": next}}); err != nil {
		return err
	}
	if next != "approved" {
		return nil
	}
	if _, err := m.Events.Publish(ctx, tx, EventCommissionApproved, "crm.commission_statement", &d.DocumentID, &d.PropertyID, map[string]any{
		"statementId": d.DocumentID, "number": number, "userId": user, "period": period, "total": dec(total).String(), "currency": cur}); err != nil {
		return err
	}
	return m.notifyUsers(ctx, tx, d.PropertyID, []uuid.UUID{user}, "crm.sales_commission_approved", m.staffLink("/crm/commission-statements/"+d.DocumentID.String()),
		map[string]any{"number": number, "period": period, "total": formatAmount(dec(total), cur), "currency": cur})
}

// MarkPaidInput marks a statement paid (payroll reference).
type MarkPaidInput struct {
	Reference string `json:"reference,omitempty" doc:"Payroll batch / transfer reference"`
}

// MarkStatementPaid marks an approved statement paid.
func (m *Module) MarkStatementPaid(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID, in MarkPaidInput) (StatementDetail, error) {
	s, err := lockStatement(ctx, tx, property, sid)
	if err != nil {
		return s, err
	}
	if s.Status != "approved" {
		return s, conflict("statement_not_approved", "only an approved statement can be marked paid")
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_commission_statements SET status = 'paid', paid_at = now(), paid_reference = $2, updated_by = $3 WHERE id = $1`,
		sid, nullStr(in.Reference), actor(ctx)); err != nil {
		return s, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "mark_paid", EntityType: "crm.commission_statement", EntityID: sid.String(),
		EntityLabel: s.Number, PropertyID: &property, Before: map[string]any{"status": s.Status},
		After: map[string]any{"status": "paid", "reference": in.Reference}}); err != nil {
		return s, err
	}
	return statementDetail(ctx, tx, sid)
}

// AdjustInput books a manual commission adjustment (audited).
type AdjustInput struct {
	UserID      uuid.UUID  `json:"userId"`
	Amount      string     `json:"amount" doc:"Signed amount (negative = deduction / clawback)"`
	Reason      string     `json:"reason"`
	QuotationID *uuid.UUID `json:"quotationId,omitempty"`
	Period      string     `json:"period,omitempty" doc:"YYYY-MM; default the current month"`
}

// AdjustCommission books a manual adjustment line.
func (m *Module) AdjustCommission(ctx context.Context, tx pgx.Tx, property uuid.UUID, in AdjustInput) (CommissionLine, error) {
	if err := handle.Required("reason", in.Reason); err != nil {
		return CommissionLine{}, err
	}
	amt, err := handle.Decimal("amount", in.Amount, decimal.Zero)
	if err != nil {
		return CommissionLine{}, err
	}
	if amt.IsZero() {
		return CommissionLine{}, handle.Invalid("amount", "invalid", "a non-zero amount")
	}
	if err := ensureUser(ctx, tx, "userId", in.UserID); err != nil {
		return CommissionLine{}, err
	}
	today := localToday(ctx, tx, property)
	period := today.Format("2006-01")
	if in.Period != "" {
		if !periodRe.MatchString(in.Period) {
			return CommissionLine{}, handle.Invalid("period", "invalid", "YYYY-MM")
		}
		period = in.Period
	}
	var opp *uuid.UUID
	var line *string
	if in.QuotationID != nil {
		q, err := GetQuotation(ctx, tx, *in.QuotationID)
		if err != nil {
			return CommissionLine{}, handle.Invalid("quotationId", "not_found", "quotation not found")
		}
		opp, line = q.OpportunityID, &q.Line
	}
	cur := "IDR"
	_ = tx.QueryRow(ctx, `SELECT currency FROM platform.instance`).Scan(&cur)
	cid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO crm.sales_commissions (id, property_id, user_id, quotation_id, opportunity_id, line, kind, amount, currency,
		recognized_on, period, source_type, reason, created_by) VALUES ($1,$2,$3,$4,$5,$6,'adjustment',$7::numeric,$8,$9,$10,'manual',$11,$12)`,
		cid, property, in.UserID, in.QuotationID, opp, line, amt.String(), cur, today, period, in.Reason, actor(ctx)); err != nil {
		return CommissionLine{}, err
	}
	c, err := getOne[CommissionLine]("commission")(tx.Query(ctx, commissionSelect+` WHERE c.id = $1`, cid))
	if err != nil {
		return c, err
	}
	return c, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "adjust", EntityType: "crm.sales_commission", EntityID: cid.String(),
		EntityLabel: "Commission adjustment " + period, PropertyID: &property, Reason: in.Reason, After: c})
}

// StatementsCSV exports the statements of a month for payroll (P5).
func StatementsCSV(ctx context.Context, q dbtx.Querier, property uuid.UUID, period, status string) (string, error) {
	rows, err := handle.List[CommissionStatement](q.Query(ctx, statementSelect+` WHERE s.property_id = $1 AND s.period = $2
		AND ($3 = '' OR s.status = $3) ORDER BY u.full_name`, property, period, status))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("number,period,user_email,user_name,earned,clawback,adjustments,total,currency,status,approved_at,paid_at,paid_reference\n")
	esc := func(s string) string {
		if strings.ContainsAny(s, ",\"\n") {
			return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
		}
		return s
	}
	ts := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.UTC().Format(time.RFC3339)
	}
	for _, s := range rows {
		fmt.Fprintf(&b, "%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n", esc(s.Number), s.Period, esc(deref(s.UserEmail)), esc(deref(s.UserName)), s.Earned,
			s.Clawback, s.Adjustments, s.Total, s.Currency, s.Status, ts(s.ApprovedAt), ts(s.PaidAt), esc(deref(s.PaidReference)))
	}
	return b.String(), nil
}

// ── targets (FR-COM-01/02) ────────────────────────────────────────────────

// TargetAchievement is the achievement of a sales target.
type TargetAchievement struct {
	TargetID           uuid.UUID  `json:"targetId" db:"target_id"`
	UserID             *uuid.UUID `json:"userId" db:"user_id"`
	UserName           *string    `json:"userName" db:"user_name"`
	TeamID             *uuid.UUID `json:"teamId" db:"team_id"`
	TeamName           *string    `json:"teamName" db:"team_name"`
	Line               *string    `json:"line" db:"line"`
	PeriodType         string     `json:"periodType" db:"period_type"`
	PeriodStart        string     `json:"periodStart" db:"period_start"`
	PeriodEnd          string     `json:"periodEnd" db:"period_end"`
	Currency           string     `json:"currency" db:"currency"`
	TargetRevenue      string     `json:"targetRevenue" db:"target_revenue"`
	AchievedRevenue    string     `json:"achievedRevenue" db:"achieved_revenue" doc:"Deals won and paid in the period (commission basis)"`
	AchievementPercent string     `json:"achievementPercent" db:"achievement_percent"`
	TargetDeals        int        `json:"targetDeals" db:"target_deals"`
	AchievedDeals      int        `json:"achievedDeals" db:"achieved_deals"`
	BookedRevenue      string     `json:"bookedRevenue" db:"booked_revenue" doc:"Deals won (accepted) in the period, paid or not"`
	BookedDeals        int        `json:"bookedDeals" db:"booked_deals"`
}

// TargetAchievements computes the achievement of the targets overlapping
// [from, to].
func TargetAchievements(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time, user string) ([]TargetAchievement, error) {
	pol, _, err := LoadPolicy(ctx, q, property)
	if err != nil {
		return nil, err
	}
	tz := location(ctx, q, property).String()
	return handle.List[TargetAchievement](q.Query(ctx, `WITH t AS (
		SELECT t.*, u.full_name AS user_name, tm.name AS team_name FROM crm.sales_targets t LEFT JOIN platform.users u ON u.id = t.user_id
		LEFT JOIN crm.sales_teams tm ON tm.id = t.team_id
		WHERE t.property_id = $1 AND t.status = 'active' AND t.archived_at IS NULL AND t.period_start <= $3 AND t.period_end >= $2
		  AND ($4 = '' OR t.user_id::text = $4)),
	d AS (SELECT t.id AS target_id, x.id, CASE WHEN $5 = 'total' THEN x.total ELSE x.net_amount END AS value, x.paid_at, x.accepted_at
		FROM t JOIN crm.sales_quotations x ON x.property_id = t.property_id AND x.status = 'accepted'
		  AND (t.line IS NULL OR x.line = t.line) AND (t.user_id IS NULL OR x.owner_user_id = t.user_id)
		  AND (t.team_id IS NULL OR x.owner_user_id IN (SELECT m.user_id FROM crm.sales_team_members m WHERE m.team_id = t.team_id)))
	SELECT t.id AS target_id, t.user_id, t.user_name, t.team_id, t.team_name, t.line, t.period_type, to_char(t.period_start, 'YYYY-MM-DD') AS period_start,
		to_char(t.period_end, 'YYYY-MM-DD') AS period_end, t.currency, trim_scale(t.target_revenue)::text AS target_revenue,
		trim_scale(coalesce((SELECT sum(value) FROM d WHERE d.target_id = t.id AND (d.paid_at AT TIME ZONE $6)::date BETWEEN t.period_start AND t.period_end), 0))::text AS achieved_revenue,
		trim_scale(round(CASE WHEN t.target_revenue > 0 THEN coalesce((SELECT sum(value) FROM d WHERE d.target_id = t.id
		  AND (d.paid_at AT TIME ZONE $6)::date BETWEEN t.period_start AND t.period_end), 0) * 100 / t.target_revenue ELSE 0 END, 2))::text AS achievement_percent,
		t.target_deals, (SELECT count(*) FROM d WHERE d.target_id = t.id AND (d.paid_at AT TIME ZONE $6)::date BETWEEN t.period_start AND t.period_end)::int AS achieved_deals,
		trim_scale(coalesce((SELECT sum(value) FROM d WHERE d.target_id = t.id AND (d.accepted_at AT TIME ZONE $6)::date BETWEEN t.period_start AND t.period_end), 0))::text AS booked_revenue,
		(SELECT count(*) FROM d WHERE d.target_id = t.id AND (d.accepted_at AT TIME ZONE $6)::date BETWEEN t.period_start AND t.period_end)::int AS booked_deals
	FROM t ORDER BY t.period_start, t.user_name NULLS LAST, t.team_name NULLS LAST`, property, from, to, user, pol.CommissionBasis, tz))
}

var _ = errs.NotFound
