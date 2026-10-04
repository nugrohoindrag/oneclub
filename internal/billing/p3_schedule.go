package billing

// PRD P3 EP-17 FR-BIL-P3-03 / FR-BIL-P3-07: payment schedules — down
// payment, installments and final payment with due dates, reminders and
// overdue status — for banquet & events, packages, tournaments and
// membership fee installments. One schedule per source document (a
// quotation converted twice never creates a second schedule, FR-QUO-07).

import (
	"context"
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
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/org"
)

// EventScheduleDue is published when a schedule line becomes due or overdue.
const EventScheduleDue = "billing.payment_schedule_due"

// Schedule is a payment schedule.
type Schedule struct {
	ID                 uuid.UUID      `json:"id" db:"id"`
	Number             string         `json:"number" db:"number"`
	Title              string         `json:"title" db:"title"`
	FolioID            *uuid.UUID     `json:"folioId" db:"folio_id"`
	CustomerID         *uuid.UUID     `json:"customerId" db:"customer_id"`
	CorporateAccountID *uuid.UUID     `json:"corporateAccountId" db:"corporate_account_id"`
	SourceType         string         `json:"sourceType" db:"source_type" enum:"banquet_event,package_booking,membership,quotation,tournament,other"`
	SourceID           *uuid.UUID     `json:"sourceId" db:"source_id"`
	SourceRef          *string        `json:"sourceRef" db:"source_ref"`
	TotalAmount        string         `json:"totalAmount" db:"total_amount"`
	PaidAmount         string         `json:"paidAmount" db:"paid_amount"`
	Currency           string         `json:"currency" db:"currency"`
	Status             string         `json:"status" db:"status" enum:"active,completed,cancelled"`
	CreatedAt          time.Time      `json:"createdAt" db:"created_at"`
	Lines              []ScheduleLine `json:"lines" db:"-"`
}

// ScheduleLine is one due amount of a schedule.
type ScheduleLine struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	Seq        int        `json:"seq" db:"seq"`
	Label      string     `json:"label" db:"label"`
	Kind       string     `json:"kind" db:"kind" enum:"down_payment,installment,final"`
	DueDate    string     `json:"dueDate" db:"due_date"`
	Amount     string     `json:"amount" db:"amount"`
	PaidAmount string     `json:"paidAmount" db:"paid_amount"`
	Status     string     `json:"status" db:"status" enum:"pending,partially_paid,paid,overdue,cancelled"`
	InvoiceID  *uuid.UUID `json:"invoiceId" db:"invoice_id"`
	PaidAt     *time.Time `json:"paidAt" db:"paid_at"`
}

const scheduleSelect = `SELECT s.id, s.number, s.title, s.folio_id, s.customer_id, s.corporate_account_id, s.source_type, s.source_id, s.source_ref,
	trim_scale(s.total_amount)::text AS total_amount,
	trim_scale(coalesce((SELECT sum(l.paid_amount) FROM billing.payment_schedule_lines l WHERE l.schedule_id = s.id), 0))::text AS paid_amount,
	s.currency, s.status, s.created_at FROM billing.payment_schedules s`

// GetSchedule loads a schedule with its lines.
func GetSchedule(ctx context.Context, q dbtx.Querier, sid uuid.UUID) (Schedule, error) {
	sc, err := oneOf[Schedule]("payment schedule")(q.Query(ctx, scheduleSelect+` WHERE s.id = $1`, sid))
	if err != nil {
		return sc, err
	}
	sc.Lines, err = handle.List[ScheduleLine](q.Query(ctx, `SELECT id, seq, label, kind, to_char(due_date, 'YYYY-MM-DD') AS due_date,
		trim_scale(amount)::text AS amount, trim_scale(paid_amount)::text AS paid_amount, status, invoice_id, paid_at
		FROM billing.payment_schedule_lines WHERE schedule_id = $1 ORDER BY seq`, sid))
	return sc, err
}

// ScheduleBySource returns the active schedule of a source document, if any.
func ScheduleBySource(ctx context.Context, q dbtx.Querier, sourceType string, sourceID uuid.UUID) (*Schedule, error) {
	var sid uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM billing.payment_schedules WHERE source_type = $1 AND source_id = $2 AND status <> 'cancelled'`,
		sourceType, sourceID).Scan(&sid)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sc, err := GetSchedule(ctx, q, sid)
	return &sc, err
}

// ScheduleLineInput is one due amount: a percent of the total or an amount.
type ScheduleLineInput struct {
	Label   string `json:"label"`
	Kind    string `json:"kind,omitempty" enum:"down_payment,installment,final"`
	Percent string `json:"percent,omitempty"`
	Amount  string `json:"amount,omitempty"`
	DueDate string `json:"dueDate" doc:"YYYY-MM-DD"`
}

// InstallmentPlan builds a down payment plus equal monthly installments.
type InstallmentPlan struct {
	DownPaymentPercent string `json:"downPaymentPercent"`
	Installments       int    `json:"installments" doc:"Number of installments after the down payment"`
	FirstDueDate       string `json:"firstDueDate,omitempty" doc:"Due date of the down payment (default today)"`
}

// ScheduleInput creates a payment schedule.
type ScheduleInput struct {
	Title              string              `json:"title"`
	FolioID            *uuid.UUID          `json:"folioId,omitempty" doc:"Folio the payments are taken on (deposits until the event)"`
	CustomerID         *uuid.UUID          `json:"customerId,omitempty"`
	CorporateAccountID *uuid.UUID          `json:"corporateAccountId,omitempty"`
	SourceType         string              `json:"sourceType,omitempty" enum:"banquet_event,package_booking,membership,quotation,tournament,other"`
	SourceID           *uuid.UUID          `json:"sourceId,omitempty"`
	SourceRef          string              `json:"sourceRef,omitempty"`
	TotalAmount        string              `json:"totalAmount"`
	Lines              []ScheduleLineInput `json:"lines,omitempty"`
	Installments       *InstallmentPlan    `json:"installments,omitempty" doc:"Alternative to lines: down payment + monthly installments"`
}

// CreateSchedule creates a schedule; an existing active schedule of the same
// source is returned unchanged (idempotent conversion).
func (s *Service) CreateSchedule(ctx context.Context, tx pgx.Tx, property uuid.UUID, in ScheduleInput) (Schedule, error) {
	if in.SourceType == "" {
		in.SourceType = "other"
	}
	if in.SourceID != nil {
		if sc, err := ScheduleBySource(ctx, tx, in.SourceType, *in.SourceID); err != nil || sc != nil {
			if sc != nil {
				return *sc, nil
			}
			return Schedule{}, err
		}
	}
	if err := handle.Required("title", in.Title); err != nil {
		return Schedule{}, err
	}
	total, err := handle.Decimal("totalAmount", in.TotalAmount, decimal.Zero)
	if err != nil {
		return Schedule{}, err
	}
	if !total.IsPositive() {
		return Schedule{}, handle.Invalid("totalAmount", "invalid", "positive amount")
	}
	cur, err := org.Currency(ctx, tx)
	if err != nil {
		return Schedule{}, err
	}
	if in.FolioID != nil {
		var cust *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT customer_id FROM billing.folios WHERE id = $1 AND property_id = $2`, *in.FolioID, property).Scan(&cust); err != nil {
			if dbtx.IsNoRows(err) {
				return Schedule{}, errs.Validation("invalid_folio", "folio not found", errs.Field("folioId", "not_found", "folio not found"))
			}
			return Schedule{}, err
		}
		if in.CustomerID == nil {
			in.CustomerID = cust
		}
	}
	today := localToday(ctx, tx, property, clock.Now())
	lines := in.Lines
	if in.Installments != nil {
		cfg, _, err := LoadPaymentConfiguration(ctx, tx, property)
		if err != nil {
			return Schedule{}, err
		}
		pct, err := handle.Decimal("installments.downPaymentPercent", in.Installments.DownPaymentPercent, dec(cfg.MinDownPaymentPercent))
		if err != nil {
			return Schedule{}, err
		}
		if pct.LessThan(dec(cfg.MinDownPaymentPercent)) || pct.GreaterThanOrEqual(decimal.NewFromInt(100)) {
			return Schedule{}, handle.Invalid("installments.downPaymentPercent", "below_minimum",
				fmt.Sprintf("down payment between %s%% and 100%%", cfg.MinDownPaymentPercent))
		}
		n := in.Installments.Installments
		if n < 1 || n > cfg.MaxInstallments {
			return Schedule{}, handle.Invalid("installments.installments", "invalid", fmt.Sprintf("1 to %d installments", cfg.MaxInstallments))
		}
		first := today
		if in.Installments.FirstDueDate != "" {
			if first, err = time.Parse("2006-01-02", in.Installments.FirstDueDate); err != nil {
				return Schedule{}, handle.Invalid("installments.firstDueDate", "invalid", "YYYY-MM-DD")
			}
		}
		lines = []ScheduleLineInput{{Label: "Down Payment " + pct.String() + "%", Kind: "down_payment", Percent: pct.String(), DueDate: first.Format("2006-01-02")}}
		each := decimal.NewFromInt(100).Sub(pct).Div(decimal.NewFromInt(int64(n)))
		for i := 1; i <= n; i++ {
			k := "installment"
			if i == n {
				k = "final"
			}
			lines = append(lines, ScheduleLineInput{Label: fmt.Sprintf("Installment %d/%d", i, n), Kind: k, Percent: each.String(),
				DueDate: first.AddDate(0, i, 0).Format("2006-01-02")})
		}
	}
	if len(lines) == 0 {
		return Schedule{}, handle.Invalid("lines", "required", "at least one due amount")
	}
	type ln struct {
		label, kind string
		due         time.Time
		amount      decimal.Decimal
	}
	var built []ln
	sum := decimal.Zero
	for i, l := range lines {
		f := fmt.Sprintf("lines[%d]", i)
		due, err := time.Parse("2006-01-02", l.DueDate)
		if err != nil {
			return Schedule{}, handle.Invalid(f+".dueDate", "invalid", "YYYY-MM-DD")
		}
		amt := decimal.Zero
		switch {
		case l.Amount != "":
			if amt, err = handle.Decimal(f+".amount", l.Amount, decimal.Zero); err != nil {
				return Schedule{}, err
			}
		case l.Percent != "":
			p, err := handle.Decimal(f+".percent", l.Percent, decimal.Zero)
			if err != nil {
				return Schedule{}, err
			}
			amt = round(total.Mul(p).Div(decimal.NewFromInt(100)), cur)
		}
		if i == len(lines)-1 && l.Amount == "" {
			amt = total.Sub(sum) // the last line takes the rounding remainder
		}
		if !amt.IsPositive() {
			return Schedule{}, handle.Invalid(f+".amount", "invalid", "positive amount or percent")
		}
		kind := l.Kind
		if kind == "" {
			switch {
			case i == 0 && len(lines) > 1:
				kind = "down_payment"
			case i == len(lines)-1:
				kind = "final"
			default:
				kind = "installment"
			}
		}
		label := strings.TrimSpace(l.Label)
		if label == "" {
			label = strings.ReplaceAll(kind, "_", " ")
		}
		built = append(built, ln{label, kind, due, amt})
		sum = sum.Add(amt)
	}
	if !sum.Equal(total) {
		return Schedule{}, errs.Validation("schedule_total_mismatch", fmt.Sprintf("the due amounts add up to %s, not %s", sum.String(), total.String()))
	}
	num, err := number(ctx, tx, property, "PSC")
	if err != nil {
		return Schedule{}, err
	}
	sid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO billing.payment_schedules (id, property_id, number, title, folio_id, customer_id, corporate_account_id, source_type,
		source_id, source_ref, total_amount, currency, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::numeric,$12,$13,$13)`,
		sid, property, num, in.Title, in.FolioID, in.CustomerID, in.CorporateAccountID, in.SourceType, in.SourceID, nullStr(in.SourceRef), total.String(), cur,
		id.Ptr(actor(ctx))); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok && in.SourceID != nil {
			if sc, err := ScheduleBySource(ctx, tx, in.SourceType, *in.SourceID); err == nil && sc != nil {
				return *sc, nil
			}
		}
		return Schedule{}, err
	}
	for i, l := range built {
		if _, err := tx.Exec(ctx, `INSERT INTO billing.payment_schedule_lines (id, property_id, schedule_id, seq, label, kind, due_date, amount)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8::numeric)`, id.New(), property, sid, i+1, l.label, l.kind, l.due, l.amount.String()); err != nil {
			return Schedule{}, err
		}
	}
	sc, err := GetSchedule(ctx, tx, sid)
	if err != nil {
		return sc, err
	}
	return sc, audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionCreate, EntityType: "billing.payment_schedule", EntityID: sid.String(),
		EntityLabel: num + " · " + in.Title, PropertyID: &property, After: sc})
}

// refreshScheduleLine recomputes the paid amount and status of a line and
// completes the schedule when every line is paid.
func (s *Service) refreshScheduleLine(ctx context.Context, tx pgx.Tx, lineID uuid.UUID) error {
	var sid uuid.UUID
	var amount, paid, status string
	var due time.Time
	var property uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT l.schedule_id, l.property_id, l.amount::text, l.status, l.due_date,
		coalesce((SELECT sum(a.amount) FROM billing.payment_allocations a WHERE a.schedule_line_id = l.id), 0)::text
		FROM billing.payment_schedule_lines l WHERE l.id = $1 FOR UPDATE`, lineID).Scan(&sid, &property, &amount, &status, &due, &paid); err != nil {
		return err
	}
	if status == "cancelled" {
		return nil
	}
	next := "pending"
	switch {
	case dec(paid).GreaterThanOrEqual(dec(amount)):
		next = "paid"
	case dec(paid).IsPositive():
		next = "partially_paid"
	}
	if next != "paid" && due.Before(localToday(ctx, tx, property, clock.Now())) {
		next = "overdue"
	}
	if _, err := tx.Exec(ctx, `UPDATE billing.payment_schedule_lines SET paid_amount = $2::numeric, status = $3,
		paid_at = CASE WHEN $3 = 'paid' THEN coalesce(paid_at, now()) ELSE NULL END WHERE id = $1`, lineID, dec(paid).String(), next); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE billing.payment_schedules SET status = 'completed' WHERE id = $1 AND status = 'active'
		AND NOT EXISTS (SELECT 1 FROM billing.payment_schedule_lines WHERE schedule_id = $1 AND status NOT IN ('paid', 'cancelled'))`, sid)
	return err
}

// PayScheduleLineInput pays a schedule line at the venue or online.
type PayScheduleLineInput struct {
	MethodType string `json:"methodType" enum:"cash,bank_transfer,virtual_account,qris,card,payment_gateway"`
	Amount     string `json:"amount,omitempty" doc:"Default: the unpaid amount of the line"`
	Reference  string `json:"reference,omitempty"`
	Online     bool   `json:"online,omitempty"`
}

// PayScheduleLine takes a payment for a line on the schedule's folio (a
// deposit until the event for banquet, package and tournament schedules).
// Lines with an invoice are paid through the invoice.
func (h *HTTP) PayScheduleLine(ctx context.Context, tx pgx.Tx, lineID uuid.UUID, in PayScheduleLineInput) (Payment, error) {
	s := h.Svc
	var property, sid uuid.UUID
	var inv *uuid.UUID
	var amount, paid, status, sourceType string
	var folio *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT l.property_id, l.schedule_id, l.invoice_id, l.amount::text, l.paid_amount::text, l.status, s.source_type, s.folio_id
		FROM billing.payment_schedule_lines l JOIN billing.payment_schedules s ON s.id = l.schedule_id WHERE l.id = $1 FOR UPDATE OF l`, lineID).
		Scan(&property, &sid, &inv, &amount, &paid, &status, &sourceType, &folio); err != nil {
		if dbtx.IsNoRows(err) {
			return Payment{}, errs.NotFound("payment schedule line")
		}
		return Payment{}, err
	}
	if inv != nil {
		var st string
		_ = tx.QueryRow(ctx, `SELECT status FROM billing.invoices WHERE id = $1`, *inv).Scan(&st)
		if st != "void" && st != "draft" {
			return h.PayInvoice(ctx, tx, *inv, PayInvoiceInput{MethodType: in.MethodType, Amount: in.Amount, Reference: in.Reference, Online: in.Online})
		}
	}
	if status == "paid" || status == "cancelled" {
		return Payment{}, errs.Conflict("nothing_due", "the line is paid or cancelled")
	}
	if folio == nil {
		return Payment{}, errs.Conflict("no_folio", "the payment schedule has no folio")
	}
	due := dec(amount).Sub(dec(paid))
	amt, err := handle.Decimal("amount", in.Amount, due)
	if err != nil {
		return Payment{}, err
	}
	if !amt.IsPositive() || amt.GreaterThan(due) {
		return Payment{}, handle.Invalid("amount", "invalid", "between 0 and the unpaid amount")
	}
	method := in.MethodType
	if method == "" {
		method = "cash"
	}
	pin := PaymentInput{FolioID: folio, MethodType: method, Purpose: schedulePurpose(sourceType), Amount: amt, Reference: in.Reference}
	if in.Online {
		pin.Channel = "online"
	}
	p, err := s.TakePayment(ctx, tx, pin)
	if err != nil {
		return p, err
	}
	if err := TagPayment(ctx, tx, p.ID, "scheduleLineId", lineID.String()); err != nil {
		return p, err
	}
	if p.Status == "completed" {
		if err := s.allocate(ctx, tx, property, p.ID, nil, &lineID, amt); err != nil {
			return p, err
		}
	}
	return GetPayment(ctx, tx, p.ID)
}

// OnSchedulePaymentSettled allocates settled gateway payments of schedule
// lines (idempotent).
func (s *Service) OnSchedulePaymentSettled(ctx context.Context, tx pgx.Tx, paymentID uuid.UUID) error {
	var property uuid.UUID
	var ref map[string]any
	var amount string
	if err := tx.QueryRow(ctx, `SELECT property_id, tender_ref, (amount - refunded_amount)::text FROM billing.payments WHERE id = $1`, paymentID).
		Scan(&property, &ref, &amount); err != nil {
		if dbtx.IsNoRows(err) {
			return nil
		}
		return err
	}
	raw, _ := ref["scheduleLineId"].(string)
	lid, err := uuid.Parse(raw)
	if err != nil {
		return nil
	}
	var done bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM billing.payment_allocations WHERE payment_id = $1)`, paymentID).Scan(&done); err != nil || done {
		return err
	}
	var due string
	if err := tx.QueryRow(ctx, `SELECT (amount - paid_amount)::text FROM billing.payment_schedule_lines WHERE id = $1`, lid).Scan(&due); err != nil {
		return err
	}
	amt := decimal.Min(dec(amount), dec(due))
	if !amt.IsPositive() {
		return nil
	}
	return s.allocate(ctx, tx, property, paymentID, nil, &lid, amt)
}

// CancelSchedule cancels the unpaid lines of a schedule (event cancelled).
func (s *Service) CancelSchedule(ctx context.Context, tx pgx.Tx, sid uuid.UUID, reason string) (Schedule, error) {
	before, err := GetSchedule(ctx, tx, sid)
	if err != nil {
		return before, err
	}
	if err := handle.Required("reason", reason); err != nil {
		return before, err
	}
	if before.Status == "cancelled" {
		return before, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE billing.payment_schedule_lines SET status = 'cancelled' WHERE schedule_id = $1 AND status IN ('pending', 'overdue')`,
		sid); err != nil {
		return before, err
	}
	if _, err := tx.Exec(ctx, `UPDATE billing.payment_schedules SET status = 'cancelled', cancelled_at = now(), cancel_reason = $2, updated_by = $3
		WHERE id = $1`, sid, reason, id.Ptr(actor(ctx))); err != nil {
		return before, err
	}
	after, err := GetSchedule(ctx, tx, sid)
	if err != nil {
		return after, err
	}
	var property uuid.UUID
	_ = tx.QueryRow(ctx, `SELECT property_id FROM billing.payment_schedules WHERE id = $1`, sid).Scan(&property)
	return after, audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "cancel", EntityType: "billing.payment_schedule", EntityID: sid.String(),
		EntityLabel: before.Number, PropertyID: &property, Reason: reason, Before: before, After: after})
}

// ScheduleReminders sends reminders N days before each due date (Payment
// Configuration, e.g. D-14 and D-8 for the final payment of a wedding),
// marks lines overdue and publishes billing.payment_schedule_due.
func (h *HTTP) ScheduleReminders(ctx context.Context, tx pgx.Tx, property uuid.UUID) (int, error) {
	s := h.Svc
	cfg, _, err := LoadPaymentConfiguration(ctx, tx, property)
	if err != nil {
		return 0, err
	}
	today := localToday(ctx, tx, property, clock.Now())
	type row struct {
		id, schedule uuid.UUID
		label, title string
		number       string
		due          time.Time
		outstanding  string
		status       string
		reminders    []string
		customer     *uuid.UUID
		corporate    *uuid.UUID
	}
	rows, err := tx.Query(ctx, `SELECT l.id, l.schedule_id, l.label, s.title, s.number, l.due_date, (l.amount - l.paid_amount)::text, l.status, l.reminders,
		s.customer_id, s.corporate_account_id FROM billing.payment_schedule_lines l JOIN billing.payment_schedules s ON s.id = l.schedule_id
		WHERE l.property_id = $1 AND s.status = 'active' AND l.status IN ('pending', 'partially_paid', 'overdue') FOR UPDATE OF l`, property)
	if err != nil {
		return 0, err
	}
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.schedule, &r.label, &r.title, &r.number, &r.due, &r.outstanding, &r.status, &r.reminders, &r.customer, &r.corporate); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, r)
	}
	rows.Close()
	n := 0
	for _, r := range list {
		days := int(r.due.Sub(today).Hours() / 24)
		tag := ""
		if days < 0 {
			tag = "overdue"
			if r.status != "overdue" {
				if _, err := tx.Exec(ctx, `UPDATE billing.payment_schedule_lines SET status = 'overdue' WHERE id = $1`, r.id); err != nil {
					return n, err
				}
			}
		} else {
			for _, d := range cfg.ScheduleReminderDaysBefore {
				if d == days {
					tag = fmt.Sprintf("D-%d", d)
				}
			}
		}
		if tag == "" || contains(r.reminders, tag) {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE billing.payment_schedule_lines SET reminders = array_append(reminders, $2) WHERE id = $1`, r.id, tag); err != nil {
			return n, err
		}
		lid := r.id
		if _, err := s.Events.Publish(ctx, tx, EventScheduleDue, "billing.payment_schedule", &r.schedule, &property, map[string]any{"scheduleId": r.schedule,
			"lineId": lid, "label": r.label, "dueDate": r.due.Format("2006-01-02"), "outstanding": r.outstanding, "tag": tag}); err != nil {
			return n, err
		}
		msg := notify.Message{Event: "billing.payment_schedule_reminder", Category: "billing", PropertyID: &property,
			Data: map[string]any{"title": r.title, "label": r.label, "dueDate": r.due.Format("02 Jan 2006"), "amount": r.outstanding, "schedule": r.number,
				"overdue": tag == "overdue"}}
		ok := false
		if r.customer != nil {
			if ok, err = crm.Recipient(ctx, tx, *r.customer, &msg); err != nil {
				return n, err
			}
		}
		if !ok && r.corporate != nil {
			var email *string
			_ = tx.QueryRow(ctx, `SELECT email FROM crm.corporate_accounts WHERE id = $1`, *r.corporate).Scan(&email)
			if email != nil && *email != "" {
				msg.Email, msg.Channels, ok = *email, []string{notify.ChannelEmail}, true
			}
		}
		if ok && h.Notify != nil {
			if err := h.Notify.Send(ctx, tx, msg); err != nil {
				return n, err
			}
		}
		n++
	}
	return n, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
