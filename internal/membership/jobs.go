package membership

// Daily P2 lifecycle job (PRD P2 EP-04) next to P1's expiry & renewal
// reminder job: annual fees (H-30 schedule, charge on the due date,
// reminders H-30 / H-7 / overdue), automatic suspension after the grace
// period, expiry of long arrears suspensions, scheduled pauses, monthly
// recognition of paid annual fees and child age-limit notices.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/provision"
)

// DailySummary reports what the P2 lifecycle job did.
type DailySummary struct {
	Scheduled, Charged, Reminders, Suspended, Expired, Paused, Resumed, Recognized, AgeNotices int
}

func collectIDs(rows pgx.Rows, err error) ([]uuid.UUID, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var x uuid.UUID
		if err := rows.Scan(&x); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// RunDaily runs the P2 lifecycle job for every property.
func (m *Module) RunDaily(ctx context.Context) (DailySummary, error) {
	sys := dbtx.System(ctx)
	var s DailySummary
	err := m.DB.WithTx(sys, func(tx pgx.Tx) error {
		props, err := collectIDs(tx.Query(sys, `SELECT id FROM platform.properties WHERE status = 'active'`))
		if err != nil {
			return err
		}
		for _, p := range props {
			if err := m.runDaily(withProperty(sys, p), tx, p, &s); err != nil {
				return err
			}
		}
		return nil
	})
	return s, err
}

func (m *Module) runDaily(ctx context.Context, tx pgx.Tx, property uuid.UUID, s *DailySummary) error {
	t := today(ctx, tx, property)
	// 1. schedule upcoming annual fees (H-30)
	ids, err := collectIDs(tx.Query(ctx, `SELECT ms.id FROM membership.memberships ms JOIN membership.types ty ON ty.id = ms.type_id
		WHERE ms.property_id = $1 AND ms.role = 'principal' AND ms.status IN ('active', 'paused') AND ms.next_fee_due IS NOT NULL
		AND ms.next_fee_due <= $2::date + 30 AND ty.annual_fee > 0
		AND NOT EXISTS (SELECT 1 FROM membership.fees f WHERE f.membership_id = ms.id AND f.fee_type = 'annual' AND f.period_start = ms.next_fee_due)`, property, t))
	if err != nil {
		return err
	}
	for _, mid := range ids {
		l, err := loadLC(ctx, tx, mid)
		if err != nil {
			return err
		}
		ty, err := loadLifecycleType(ctx, tx, l.TypeID)
		if err != nil {
			return err
		}
		ps := *l.NextFeeDue
		pe := ps.AddDate(1, 0, -1)
		if _, err := tx.Exec(ctx, `INSERT INTO membership.fees (id, property_id, membership_id, fee_type, period_start, period_end, due_date, amount, status)
			VALUES (gen_random_uuid(), $1, $2, 'annual', $3, $4, $3, $5::numeric, 'scheduled')`, property, l.ID, ps, pe, ty.AnnualFee.String()); err != nil {
			return err
		}
		s.Scheduled++
	}
	// 2. charge fees on their due date (folio + membership.annual_fee_due)
	ids, err = collectIDs(tx.Query(ctx, `SELECT id FROM membership.fees WHERE property_id = $1 AND status = 'scheduled' AND due_date <= $2`, property, t))
	if err != nil {
		return err
	}
	for _, fid := range ids {
		var mid uuid.UUID
		var feeType, amt string
		if err := tx.QueryRow(ctx, `SELECT membership_id, fee_type, amount::text FROM membership.fees WHERE id = $1`, fid).Scan(&mid, &feeType, &amt); err != nil {
			return err
		}
		l, err := loadLC(ctx, tx, mid)
		if err != nil {
			return err
		}
		if err := m.postFee(ctx, tx, l, fid, feeType, decimal.RequireFromString(amt)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE membership.fees SET status = 'due' WHERE id = $1`, fid); err != nil {
			return err
		}
		if err := m.publish(ctx, tx, EventAnnualFeeDue, l, map[string]any{"feeId": fid, "amount": amt}); err != nil {
			return err
		}
		s.Charged++
	}
	// 3. reminders H-30 / H-7 / overdue (once each)
	for _, rk := range []struct{ kind, cond, event string }{
		{"d30", "f.status = 'scheduled' AND f.due_date <= $2::date + 30", "membership.annual_fee_due"},
		{"d7", "f.status IN ('scheduled', 'due') AND f.due_date <= $2::date + 7 AND f.due_date >= $2::date", "membership.annual_fee_due"},
		{"overdue", "f.status = 'due' AND f.due_date < $2::date", "membership.annual_fee_overdue"},
	} {
		ids, err := collectIDs(tx.Query(ctx, `INSERT INTO membership.fee_reminders (fee_id, property_id, kind)
			SELECT f.id, f.property_id, $3 FROM membership.fees f WHERE f.property_id = $1 AND f.fee_type = 'annual' AND `+rk.cond+`
			ON CONFLICT DO NOTHING RETURNING fee_id`, property, t, rk.kind))
		if err != nil {
			return err
		}
		for _, fid := range ids {
			if err := m.notifyFee(ctx, tx, fid, rk.event, rk.kind); err != nil {
				return err
			}
			s.Reminders++
		}
	}
	pol, _, err := m.policy(ctx, tx, property)
	if err != nil {
		return err
	}
	// 4. automatic suspension after the grace period (arrears)
	if pol.SuspendAfterGrace {
		ids, err = collectIDs(tx.Query(ctx, `SELECT DISTINCT ms.id FROM membership.memberships ms JOIN membership.types ty ON ty.id = ms.type_id
			JOIN membership.fees f ON f.membership_id = ms.id
			WHERE ms.property_id = $1 AND ms.role = 'principal' AND ms.status IN ('active', 'paused') AND f.status = 'due'
			AND f.due_date + ty.grace_days < $2`, property, t))
		if err != nil {
			return err
		}
		for _, mid := range ids {
			l, err := loadLC(ctx, tx, mid)
			if err != nil {
				return err
			}
			if _, err := m.suspend(ctx, tx, l, "arrears", "annual fee unpaid after the grace period"); err != nil {
				return err
			}
			s.Suspended++
		}
	}
	// 5. long arrears suspensions expire (FR-MBL-09)
	ids, err = collectIDs(tx.Query(ctx, `SELECT id FROM membership.memberships WHERE property_id = $1 AND role = 'principal' AND status = 'suspended'
		AND suspension_kind = 'arrears' AND suspended_at < now() - make_interval(days => $2)`, property, pol.ExpireAfterSuspendedDays))
	if err != nil {
		return err
	}
	for _, mid := range ids {
		l, err := loadLC(ctx, tx, mid)
		if err != nil {
			return err
		}
		if _, err := m.transition(ctx, tx, l, "expired", "expired", "suspended for arrears too long", nil, ""); err != nil {
			return err
		}
		s.Expired++
	}
	// 6. scheduled pauses start; finished pauses end
	ids, err = collectIDs(tx.Query(ctx, `SELECT id FROM membership.memberships WHERE property_id = $1 AND role = 'principal' AND status = 'active'
		AND paused_from <= $2 AND paused_until >= $2`, property, t))
	if err != nil {
		return err
	}
	for _, mid := range ids {
		l, err := loadLC(ctx, tx, mid)
		if err != nil {
			return err
		}
		if _, err := m.transition(ctx, tx, l, "paused", "paused", "pause starts", nil, ""); err != nil {
			return err
		}
		s.Paused++
	}
	ids, err = collectIDs(tx.Query(ctx, `SELECT id FROM membership.memberships WHERE property_id = $1 AND role = 'principal' AND status = 'paused'
		AND paused_until < $2`, property, t))
	if err != nil {
		return err
	}
	for _, mid := range ids {
		l, err := loadLC(ctx, tx, mid)
		if err != nil {
			return err
		}
		if _, err := m.resume(ctx, tx, l, "pause ended"); err != nil {
			return err
		}
		s.Resumed++
	}
	// 7. recognise paid annual fees month by month (deferred revenue, FR-BIL-P2-06)
	type paidFee struct {
		ID     uuid.UUID `db:"id"`
		Amount string    `db:"amount"`
		Start  time.Time `db:"period_start"`
		End    time.Time `db:"period_end"`
	}
	fees, err := handle.List[paidFee](tx.Query(ctx, `SELECT id, amount::text AS amount, period_start, period_end FROM membership.fees
		WHERE property_id = $1 AND fee_type = 'annual' AND status = 'paid' AND period_start IS NOT NULL AND period_end IS NOT NULL AND period_start <= $2`, property, t))
	if err != nil {
		return err
	}
	for _, f := range fees {
		months := monthsBetween(f.Start, f.End)
		total := decimal.RequireFromString(f.Amount)
		per := total.Div(decimal.NewFromInt(int64(months))).Round(0)
		for i := 0; i < months; i++ {
			mstart := f.Start.AddDate(0, i, 0)
			if mstart.After(t) {
				break
			}
			amt := per
			if i == months-1 {
				amt = total.Sub(per.Mul(decimal.NewFromInt(int64(months - 1))))
			}
			occ := mstart
			if err := m.Billing.PostDeferred(ctx, tx, billing.DeferredEntry{PropertyID: property, LiabilityType: "annual_fee", RefType: "membership.fee",
				RefID: f.ID, EntryType: "recognition", Amount: amt, RevenueComponent: "membership_annual_fee", OccurredAt: &occ,
				Description: "Annual fee recognised " + mstart.Format("2006-01"), IdempotencyKey: fmt.Sprintf("annual-fee-%s-%s", f.ID, mstart.Format("200601"))}); err != nil {
				return err
			}
			s.Recognized++
		}
	}
	// 8. children who will pass the age limit within the notice window (FR-MBL-13)
	n, err := m.ageNotices(ctx, tx, property, t, pol.ChildAgeNoticeDays)
	s.AgeNotices += n
	return err
}

func monthsBetween(a, b time.Time) int {
	n := (b.Year()-a.Year())*12 + int(b.Month()) - int(a.Month())
	if b.Day() >= a.Day()-1 {
		n++
	}
	return max(n, 1)
}

func (m *Module) notifyFee(ctx context.Context, tx pgx.Tx, fid uuid.UUID, event, kind string) error {
	f, err := handle.Get[Fee](tx.Query(ctx, feeSelect+` WHERE f.id = $1`, fid))
	if err != nil {
		return err
	}
	var cust *uuid.UUID
	var typeName string
	if err := tx.QueryRow(ctx, `SELECT mb.customer_id, t.name FROM membership.memberships ms JOIN membership.members mb ON mb.id = ms.member_id
		JOIN membership.types t ON t.id = ms.type_id WHERE ms.id = $1`, f.MembershipID).Scan(&cust, &typeName); err != nil || cust == nil {
		return err
	}
	c, err := crm.GetCustomer(ctx, tx, *cust)
	if err != nil {
		return err
	}
	return m.notifyCustomer(ctx, tx, c, event, map[string]any{"name": c.Name, "memberNo": f.MemberNo, "typeName": typeName, "amount": f.Amount,
		"dueDate": f.DueDate.Format("2006-01-02"), "kind": kind})
}

func (m *Module) ageNotices(ctx context.Context, tx pgx.Tx, property uuid.UUID, t time.Time, noticeDays int) (int, error) {
	type row struct {
		PrincipalID uuid.UUID  `db:"principal_id"`
		ChildID     uuid.UUID  `db:"child_id"`
		ChildName   string     `db:"child_name"`
		BirthDate   time.Time  `db:"birth_date"`
		Eligibility []byte     `db:"eligibility"`
		Customer    *uuid.UUID `db:"principal_customer"`
	}
	list, err := handle.List[row](tx.Query(ctx, `SELECT ms.principal_id, c.id AS child_id, c.name AS child_name, c.birth_date, ty.eligibility,
		pmb.customer_id AS principal_customer
		FROM membership.memberships ms JOIN membership.members mb ON mb.id = ms.member_id JOIN crm.customers c ON c.id = mb.customer_id
		JOIN membership.types ty ON ty.id = ms.type_id JOIN membership.memberships pm ON pm.id = ms.principal_id
		JOIN membership.members pmb ON pmb.id = pm.member_id
		WHERE ms.property_id = $1 AND ms.role = 'family' AND ms.relationship = 'child' AND ms.status IN ('active', 'paused') AND c.birth_date IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM membership.history h WHERE h.membership_id = ms.principal_id AND h.event = 'age_limit_notice'
		  AND h.details->>'customerId' = c.id::text)`, property))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range list {
		var el Eligibility
		if err := json.Unmarshal(r.Eligibility, &el); err != nil || el.MaxChildAge == nil {
			continue
		}
		limit := r.BirthDate.AddDate(*el.MaxChildAge+1, 0, 0) // first day above the limit
		if limit.After(t.AddDate(0, 0, noticeDays)) {
			continue
		}
		l, err := loadLC(ctx, tx, r.PrincipalID)
		if err != nil {
			return n, err
		}
		if err := history(ctx, tx, property, l.MemberID, &l.ID, "age_limit_notice", l.Status, l.Status,
			map[string]any{"customerId": r.ChildID.String(), "childName": r.ChildName, "date": limit.Format("2006-01-02")}); err != nil {
			return n, err
		}
		if r.Customer != nil {
			if c, err := crm.GetCustomer(ctx, tx, *r.Customer); err == nil {
				if err := m.notifyCustomer(ctx, tx, c, "membership.age_limit", map[string]any{"name": c.Name, "childName": r.ChildName,
					"date": limit.Format("2006-01-02"), "memberNo": l.MemberNo}); err != nil {
					return n, err
				}
			}
		}
		n++
	}
	return n, nil
}

// DailyArgs runs the P2 lifecycle job.
type DailyArgs struct{}

func (DailyArgs) Kind() string { return "membership_daily_lifecycle" }

func (DailyArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 5}
}

// DailyWorker runs every hour (idempotent; reminders are sent once).
type DailyWorker struct {
	river.WorkerDefaults[DailyArgs]
	M *Module
}

func (w *DailyWorker) Work(ctx context.Context, _ *river.Job[DailyArgs]) error {
	_, err := w.M.RunDaily(ctx)
	return err
}

// registerDailyJob adds the P2 lifecycle job (called by RegisterJobs).
func (m *Module) registerDailyJob(reg *jobs.Registrar) {
	river.AddWorker(reg.Workers, &DailyWorker{M: m})
	reg.Periodic = append(reg.Periodic, river.NewPeriodicJob(river.PeriodicInterval(time.Hour),
		func() (river.JobArgs, *river.InsertOpts) { return DailyArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true}))
}

// Templates are the P2 membership notification templates (FR-INT-P2-04).
func Templates() []provision.Template {
	t := map[string]map[string][2]string{
		"membership.annual_fee_due": {
			"en": {"Annual fee due {{.dueDate}} — {{.memberNo}}", "Hello {{.name}},\n\nThe annual fee of your {{.typeName}} membership ({{.memberNo}}) of {{.amount}} is due on {{.dueDate}}."},
			"id": {"Iuran tahunan jatuh tempo {{.dueDate}} — {{.memberNo}}", "Halo {{.name}},\n\nIuran tahunan keanggotaan {{.typeName}} ({{.memberNo}}) sebesar {{.amount}} jatuh tempo pada {{.dueDate}}."},
		},
		"membership.annual_fee_overdue": {
			"en": {"Annual fee overdue — {{.memberNo}}", "Hello {{.name}},\n\nThe annual fee of {{.amount}} due on {{.dueDate}} has not been paid. The membership will be suspended after the grace period."},
			"id": {"Iuran tahunan terlambat — {{.memberNo}}", "Halo {{.name}},\n\nIuran tahunan {{.amount}} yang jatuh tempo {{.dueDate}} belum dibayar. Keanggotaan akan disuspensi setelah masa tenggang."},
		},
		"membership.suspended": {
			"en": {"Membership suspended — {{.memberNo}}", "Hello {{.name}},\n\nYour membership {{.memberNo}} has been suspended: {{.reason}}."},
			"id": {"Keanggotaan disuspensi — {{.memberNo}}", "Halo {{.name}},\n\nKeanggotaan {{.memberNo}} disuspensi: {{.reason}}."},
		},
		"membership.age_limit": {
			"en": {"{{.childName}} reaches the age limit", "Hello {{.name}},\n\n{{.childName}} will no longer be covered by membership {{.memberNo}} from {{.date}}."},
			"id": {"{{.childName}} mencapai batas umur", "Halo {{.name}},\n\n{{.childName}} tidak lagi tercakup keanggotaan {{.memberNo}} mulai {{.date}}."},
		},
	}
	var out []provision.Template
	for ev, locs := range t {
		for loc, c := range locs {
			for _, ch := range []string{"email", "in_app"} {
				out = append(out, provision.Template{Event: ev, Channel: ch, Locale: loc, Subject: c[0], Body: c[1]})
			}
		}
	}
	return out
}
