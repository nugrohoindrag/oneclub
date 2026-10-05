package banquet

// Integration of Banquet & Event: crm.quotation_accepted becomes an event
// (PRD P3 §5.4.1: CRM never calls banquet), billing.payment_settled makes
// the event Definite once the DP is received and marks paid registrations,
// billing.invoice_paid settles the final invoice; the option expiry and
// daily reminders run as jobs and the night audit gets a banquet check.

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/billing"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/outbox"
)

// OnPaymentSettled reacts to payments on banquet folios: the event becomes
// Definite once the DP is received; a registration becomes paid.
func (m *Module) OnPaymentSettled(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	var p billing.SettledPayload
	if err := ev.Decode(&p); err != nil {
		return err
	}
	if p.FolioID == nil || p.SourceType == nil || *p.SourceType != "banquet_event" {
		return nil
	}
	var eid uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM banquet.events WHERE folio_id = $1`, *p.FolioID).Scan(&eid)
	switch {
	case err == nil:
		return m.onDepositPaid(ctx, tx, eid)
	case !dbtx.IsNoRows(err):
		return err
	}
	var pid uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM banquet.participants WHERE folio_id = $1`, *p.FolioID).Scan(&pid)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return m.refreshPayment(ctx, tx, pid)
}

// OnScheduleDue tells the sales owner of an event that a DP or settlement
// term is due soon or overdue (billing.payment_schedule_due; the customer
// gets Billing's payment reminder).
func (m *Module) OnScheduleDue(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	var p struct {
		ScheduleID  uuid.UUID `json:"scheduleId"`
		Label       string    `json:"label"`
		DueDate     string    `json:"dueDate"`
		Outstanding string    `json:"outstanding"`
		Tag         string    `json:"tag"`
	}
	if err := ev.Decode(&p); err != nil {
		return nil //nolint:nilerr // foreign payload
	}
	var eid uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM banquet.events WHERE schedule_id = $1 AND status IN ('inquiry', 'tentative', 'definite')`, p.ScheduleID).Scan(&eid)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	e, err := m.Get(ctx, tx, eid)
	if err != nil {
		return err
	}
	return m.notifyOwner(ctx, tx, e, "banquet.payment_due", map[string]any{"label": p.Label, "dueDate": p.DueDate, "amount": p.Outstanding,
		"tag": p.Tag, "overdue": p.Tag == "overdue"})
}

// OnInvoicePaid settles an event whose final invoice is paid.
func (m *Module) OnInvoicePaid(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	var p struct {
		InvoiceID uuid.UUID `json:"invoiceId"`
	}
	if err := ev.Decode(&p); err != nil {
		return err
	}
	var eid, property uuid.UUID
	var number string
	err := tx.QueryRow(ctx, `UPDATE banquet.events SET settled_at = coalesce(settled_at, now()), version = version + 1 WHERE final_invoice_id = $1
		RETURNING id, property_id, number`, p.InvoiceID).Scan(&eid, &property, &number)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: "settled", EntityType: "banquet.event", EntityID: eid.String(), EntityLabel: number,
		PropertyID: &property, ActorName: "event:" + billing.EventInvoicePaid, Metadata: map[string]any{"invoiceId": p.InvoiceID}})
}

// ── night audit (EP-18) ───────────────────────────────────────────────────

// NightAuditCheck reports definite events that ended but are not completed
// and completed events without final billing.
func (m *Module) NightAuditCheck(ctx context.Context, tx pgx.Tx, property uuid.UUID, day time.Time) ([]billing.AuditFinding, error) {
	tz := calendar.Location(ctx, tx).String()
	list := func(sql string) ([]string, error) {
		rows, err := tx.Query(ctx, sql, property, day.Format("2006-01-02"), tz)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowTo[string])
	}
	open, err := list(`SELECT number || ' · ' || title FROM banquet.events WHERE property_id = $1 AND status = 'definite'
		AND (end_at AT TIME ZONE $3)::date <= $2::date ORDER BY number LIMIT 50`)
	if err != nil {
		return nil, err
	}
	unbilled, err := list(`SELECT number || ' · ' || title FROM banquet.events WHERE property_id = $1 AND status = 'completed' AND final_billed_at IS NULL
		AND (end_at AT TIME ZONE $3)::date <= $2::date ORDER BY number LIMIT 50`)
	if err != nil {
		return nil, err
	}
	sev := func(n int) string {
		if n > 0 {
			return "warning"
		}
		return "info"
	}
	return []billing.AuditFinding{
		{Check: "banquet_events_not_completed", Severity: sev(len(open)), Count: len(open), Message: "Events that ended but are not completed (final pax)", Items: open},
		{Check: "banquet_final_billing_pending", Severity: sev(len(unbilled)), Count: len(unbilled), Message: "Completed events without final billing", Items: unbilled},
	}, nil
}

// ── jobs ──────────────────────────────────────────────────────────────────

// HoldsArgs expires tentative holds past their option date.
type HoldsArgs struct{}

func (HoldsArgs) Kind() string { return "banquet_holds" }
func (HoldsArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

// HoldsWorker runs ExpireOptions.
type HoldsWorker struct {
	river.WorkerDefaults[HoldsArgs]
	M *Module
}

func (w *HoldsWorker) Work(ctx context.Context, _ *river.Job[HoldsArgs]) error {
	_, err := w.M.ExpireOptions(ctx)
	return err
}

// DailyArgs sends the overdue checklist and final pax reminders.
type DailyArgs struct{}

func (DailyArgs) Kind() string { return "banquet_daily" }
func (DailyArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

// DailyWorker runs DailyReminders.
type DailyWorker struct {
	river.WorkerDefaults[DailyArgs]
	M *Module
}

func (w *DailyWorker) Work(ctx context.Context, _ *river.Job[DailyArgs]) error {
	_, err := w.M.DailyReminders(ctx)
	return err
}

// RegisterJobs adds the workers and schedules of the module.
func (m *Module) RegisterJobs(reg *jobs.Registrar, loc func() *time.Location) {
	river.AddWorker(reg.Workers, &HoldsWorker{M: m})
	river.AddWorker(reg.Workers, &DailyWorker{M: m})
	reg.Periodic = append(reg.Periodic,
		river.NewPeriodicJob(river.PeriodicInterval(15*time.Minute), func() (river.JobArgs, *river.InsertOpts) { return HoldsArgs{}, nil }, nil),
		river.NewPeriodicJob(jobs.DailyAt{Hour: 7, Minute: 20, Location: loc}, func() (river.JobArgs, *river.InsertOpts) { return DailyArgs{}, nil }, nil))
}

// DailyReminders reminds the PIC of overdue checklist tasks (once a day)
// and the sales owner two days before the final pax cut-off. Returns the
// number of reminders.
func (m *Module) DailyReminders(ctx context.Context) (int, error) {
	ctx = dbtx.System(ctx)
	n := 0
	err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		loc := calendar.Location(ctx, tx)
		todayDay := localDay(clock.Now(), loc)
		rows, err := tx.Query(ctx, `SELECT c.id, c.event_id, c.task, to_char(c.due_date, 'YYYY-MM-DD'), coalesce(c.owner_user_id, e.sales_owner_id)
			FROM banquet.event_checklist_items c JOIN banquet.events e ON e.id = c.event_id WHERE c.status = 'open' AND c.due_date < $1::date
			AND e.status IN ('inquiry', 'tentative', 'definite') AND (c.reminded_at IS NULL OR c.reminded_at < $2) ORDER BY c.due_date LIMIT 500`,
			todayDay.Format("2006-01-02"), todayDay)
		if err != nil {
			return err
		}
		type task struct {
			id, event uuid.UUID
			name, due string
			owner     *uuid.UUID
		}
		var tasks []task
		for rows.Next() {
			var t task
			if err := rows.Scan(&t.id, &t.event, &t.name, &t.due, &t.owner); err != nil {
				rows.Close()
				return err
			}
			tasks = append(tasks, t)
		}
		rows.Close()
		for _, t := range tasks {
			if _, err := tx.Exec(ctx, `UPDATE banquet.event_checklist_items SET reminded_at = now() WHERE id = $1`, t.id); err != nil {
				return err
			}
			if t.owner == nil || m.Notify == nil {
				continue
			}
			e, err := m.Get(ctx, tx, t.event)
			if err != nil {
				return err
			}
			if err := m.Notify.Send(ctx, tx, notify.Message{Event: "banquet.checklist_overdue", Category: "general", UserIDs: []uuid.UUID{*t.owner},
				PropertyID: &e.PropertyID, Link: "/banquet-event/events/" + e.ID.String(), Data: eventData(ctx, tx, e, map[string]any{"task": t.name,
					"dueDate": t.due})}); err != nil {
				return err
			}
			n++
		}
		deadline := todayDay.AddDate(0, 0, 2).Format("2006-01-02")
		rows, err = tx.Query(ctx, `SELECT id FROM banquet.events WHERE status IN ('tentative', 'definite') AND pax_deadline = $1::date`, deadline)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return err
		}
		for _, eid := range ids {
			e, err := m.Get(ctx, tx, eid)
			if err != nil {
				return err
			}
			if err := m.notifyOwner(ctx, tx, e, "banquet.guaranteed_pax_due", map[string]any{"deadline": deadline}); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	if err != nil {
		return n, fmt.Errorf("banquet daily reminders: %w", err)
	}
	return n, nil
}
