package sales

// Periodic jobs of CRM Sales: first-response SLA reminders and escalation
// and follow-up reminders every 15 minutes (FR-LEAD-06/08); quotation
// expiry (crm.quotation_expired), lead retention (UU PDP, PRD P3 §16 #19)
// and deal payment re-evaluation daily.

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/jobs"
)

// SLAArgs runs the SLA and follow-up reminders.
type SLAArgs struct{}

func (SLAArgs) Kind() string { return "crm_sales_sla" }
func (SLAArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

// SLAWorker sends the SLA reminders and escalations.
type SLAWorker struct {
	river.WorkerDefaults[SLAArgs]
	M *Module
}

func (w *SLAWorker) Work(ctx context.Context, _ *river.Job[SLAArgs]) error {
	_, err := w.M.RunSLA(ctx)
	return err
}

// DailyArgs runs quotation expiry, lead retention and deal evaluation.
type DailyArgs struct{}

func (DailyArgs) Kind() string { return "crm_sales_daily" }
func (DailyArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

// DailyWorker runs the daily housekeeping.
type DailyWorker struct {
	river.WorkerDefaults[DailyArgs]
	M *Module
}

func (w *DailyWorker) Work(ctx context.Context, _ *river.Job[DailyArgs]) error {
	_, err := w.M.RunDaily(ctx)
	return err
}

// RegisterJobs adds the CRM Sales workers and schedules.
func (m *Module) RegisterJobs(reg *jobs.Registrar, loc func() *time.Location) {
	river.AddWorker(reg.Workers, &SLAWorker{M: m})
	river.AddWorker(reg.Workers, &DailyWorker{M: m})
	reg.Periodic = append(reg.Periodic,
		river.NewPeriodicJob(river.PeriodicInterval(15*time.Minute), func() (river.JobArgs, *river.InsertOpts) { return SLAArgs{}, nil }, nil),
		river.NewPeriodicJob(jobs.DailyAt{Hour: 0, Minute: 20, Location: loc}, func() (river.JobArgs, *river.InsertOpts) { return DailyArgs{}, nil }, nil))
}

func (m *Module) properties(ctx context.Context) ([]uuid.UUID, error) {
	var out []uuid.UUID
	err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM platform.properties WHERE status = 'active' ORDER BY created_at`)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		return err
	})
	return out, err
}

// RunSLA sends the first-response reminders and escalations and the
// follow-up reminders of every property; it returns the notifications.
func (m *Module) RunSLA(ctx context.Context) (int, error) {
	ctx = dbtx.System(ctx)
	props, err := m.properties(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, p := range props {
		err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
			n, err := m.SLAReminders(ctx, tx, p)
			total += n
			return err
		})
		if err != nil {
			return total, fmt.Errorf("crm sales SLA for %s: %w", p, err)
		}
	}
	return total, nil
}

// SLAReminders processes one property.
func (m *Module) SLAReminders(ctx context.Context, tx pgx.Tx, property uuid.UUID) (int, error) {
	pol, _, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return 0, err
	}
	loc := location(ctx, tx, property)
	n := 0
	collect := func(sql string, args ...any) ([]uuid.UUID, error) {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	}
	// reminders before the SLA ends
	ids, err := collect(`SELECT id FROM crm.sales_leads WHERE property_id = $1 AND status = 'new' AND first_responded_at IS NULL
		AND sla_reminded_at IS NULL AND owner_user_id IS NOT NULL AND first_response_due_at > now()
		AND first_response_due_at <= now() + make_interval(mins => $2) FOR UPDATE SKIP LOCKED`, property, max(pol.SLAReminderMinutes, 1))
	if err != nil {
		return n, err
	}
	for _, lid := range ids {
		l, err := GetLead(ctx, tx, lid)
		if err != nil {
			return n, err
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.sales_leads SET sla_reminded_at = now() WHERE id = $1`, lid); err != nil {
			return n, err
		}
		if err := m.notifyUsers(ctx, tx, property, []uuid.UUID{*l.OwnerUserID}, "crm.sales_lead_sla_reminder", m.staffLink("/crm/leads/"+lid.String()),
			map[string]any{"number": l.Number, "name": l.Name, "dueAt": l.FirstResponseDueAt.In(loc).Format("02 Jan 15:04")}); err != nil {
			return n, err
		}
		n++
	}
	// escalation once the SLA is missed
	ids, err = collect(`SELECT id FROM crm.sales_leads WHERE property_id = $1 AND status = 'new' AND first_responded_at IS NULL
		AND sla_escalated_at IS NULL AND first_response_due_at <= now() FOR UPDATE SKIP LOCKED`, property)
	if err != nil {
		return n, err
	}
	managers := holders(ctx, tx, property, "crm.lead.assign")
	for _, lid := range ids {
		l, err := GetLead(ctx, tx, lid)
		if err != nil {
			return n, err
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.sales_leads SET sla_escalated_at = now() WHERE id = $1`, lid); err != nil {
			return n, err
		}
		to := append([]uuid.UUID{}, managers...)
		if l.OwnerUserID != nil {
			var mgr *uuid.UUID
			_ = tx.QueryRow(ctx, `SELECT t.manager_user_id FROM crm.sales_team_members m JOIN crm.sales_teams t ON t.id = m.team_id
				WHERE m.user_id = $1 AND m.property_id = $2 AND t.manager_user_id IS NOT NULL LIMIT 1`, *l.OwnerUserID, property).Scan(&mgr)
			if mgr != nil {
				to = append(to, *mgr)
			}
			to = append(to, *l.OwnerUserID)
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "sla_escalated", EntityType: "crm.lead", EntityID: lid.String(),
			EntityLabel: l.Number + " · " + l.Name, PropertyID: &property, ActorName: "system",
			After: map[string]any{"dueAt": l.FirstResponseDueAt, "ownerUserId": l.OwnerUserID}}); err != nil {
			return n, err
		}
		if _, err := m.Events.Publish(ctx, tx, EventLeadEscalated, "crm.lead", &lid, &property, map[string]any{"leadId": lid, "number": l.Number,
			"line": l.Line, "ownerUserId": l.OwnerUserID, "dueAt": l.FirstResponseDueAt}); err != nil {
			return n, err
		}
		owner := ""
		if l.OwnerName != nil {
			owner = *l.OwnerName
		}
		if err := m.notifyUsers(ctx, tx, property, uniq(to), "crm.sales_lead_sla_escalated", m.staffLink("/crm/leads/"+lid.String()),
			map[string]any{"number": l.Number, "name": l.Name, "line": l.Line, "owner": owner,
				"dueAt": l.FirstResponseDueAt.In(loc).Format("02 Jan 15:04")}); err != nil {
			return n, err
		}
		n++
	}
	// follow-ups due
	ids, err = collect(`SELECT id FROM crm.sales_activities WHERE property_id = $1 AND status = 'open' AND reminded_at IS NULL
		AND assigned_to IS NOT NULL AND due_at <= now() + interval '15 minutes' FOR UPDATE SKIP LOCKED`, property)
	if err != nil {
		return n, err
	}
	for _, aid := range ids {
		a, err := getActivity(ctx, tx, aid)
		if err != nil {
			return n, err
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.sales_activities SET reminded_at = now() WHERE id = $1`, aid); err != nil {
			return n, err
		}
		if err := m.notifyUsers(ctx, tx, property, []uuid.UUID{*a.AssignedTo}, "crm.sales_follow_up_due", m.staffLink("/crm/follow-ups"),
			map[string]any{"subject": a.Subject, "type": a.Type, "contact": deref(a.ContactName), "dueAt": a.DueAt.In(loc).Format("02 Jan 15:04")}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func uniq(ids []uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	out := []uuid.UUID{}
	for _, u := range ids {
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// RunDaily expires quotations, anonymises old leads and re-evaluates unpaid
// deals for every property; it returns the records changed.
func (m *Module) RunDaily(ctx context.Context) (int, error) {
	ctx = dbtx.System(ctx)
	props, err := m.properties(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, p := range props {
		err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
			n, err := m.ExpireQuotations(ctx, tx, p)
			if err != nil {
				return err
			}
			r, err := m.AnonymizeLeads(ctx, tx, p)
			if err != nil {
				return err
			}
			total += n + r
			rows, err := tx.Query(ctx, `SELECT id FROM crm.sales_quotations WHERE property_id = $1 AND status = 'accepted' AND paid_at IS NULL`, p)
			if err != nil {
				return err
			}
			ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
			if err != nil {
				return err
			}
			for _, qid := range ids {
				if err := m.evaluateDeal(ctx, tx, p, qid, clock.Now()); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return total, fmt.Errorf("crm sales daily for %s: %w", p, err)
		}
	}
	return total, nil
}

// AnonymizeLeads anonymises leads that never became customers after the
// retention period without activity.
func (m *Module) AnonymizeLeads(ctx context.Context, tx pgx.Tx, property uuid.UUID) (int, error) {
	pol, _, err := LoadPolicy(ctx, tx, property)
	if err != nil || pol.LeadRetentionDays <= 0 {
		return 0, err
	}
	rows, err := tx.Query(ctx, `UPDATE crm.sales_leads SET name = 'Anonymised lead', company_name = NULL, phone = NULL, email = NULL, message = NULL,
		notes = NULL, budget = NULL, anonymized_at = now() WHERE property_id = $1 AND anonymized_at IS NULL AND customer_id IS NULL
		AND status <> 'converted' AND coalesce(last_activity_at, updated_at) < now() - make_interval(days => $2) RETURNING id`, property, pol.LeadRetentionDays)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_activities SET notes = NULL WHERE lead_id = ANY ($1)`, ids); err != nil {
		return 0, err
	}
	return len(ids), audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "anonymize", EntityType: "crm.lead", EntityID: property.String(),
		EntityLabel: "Lead retention", PropertyID: &property, ActorName: "system", After: map[string]any{"leads": len(ids),
			"retentionDays": pol.LeadRetentionDays}})
}
