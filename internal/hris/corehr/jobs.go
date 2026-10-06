package corehr

// Daily Core HR job (00:05 instance time, per property in its timezone):
// apply due employment changes and terminations (the H7 event leaves on the
// effective date), align employment statuses with the contract in force
// (probation end, renewal start), end lapsed contracts and send the
// contract (H-30 / H-7), document and certification (H-60 / H-30)
// reminders; expired certificates publish hris.certification_expired (H7).

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/jobs"
)

// DailyArgs runs the daily Core HR job.
type DailyArgs struct{}

func (DailyArgs) Kind() string { return "hris_core_daily" }
func (DailyArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

// DailyWorker runs RunDaily.
type DailyWorker struct {
	river.WorkerDefaults[DailyArgs]
	M *Module
}

func (w *DailyWorker) Work(ctx context.Context, _ *river.Job[DailyArgs]) error {
	_, err := w.M.RunDaily(ctx)
	return err
}

// RegisterJobs adds the worker and its daily schedule.
func (m *Module) RegisterJobs(reg *jobs.Registrar, loc func() *time.Location) {
	river.AddWorker(reg.Workers, &DailyWorker{M: m})
	reg.Periodic = append(reg.Periodic,
		river.NewPeriodicJob(jobs.DailyAt{Hour: 0, Minute: 5, Location: loc}, func() (river.JobArgs, *river.InsertOpts) { return DailyArgs{}, nil }, nil))
}

// DailyReport counts what the daily job did.
type DailyReport struct {
	Changes        int `json:"changes"`
	Terminations   int `json:"terminations"`
	StatusChanges  int `json:"statusChanges"`
	ContractsEnded int `json:"contractsEnded"`
	ContractAlerts int `json:"contractAlerts"`
	DocumentAlerts int `json:"documentAlerts"`
	CertAlerts     int `json:"certificationAlerts"`
	CertsExpired   int `json:"certificationsExpired"`
}

// RunDaily runs the daily job for every active property.
func (m *Module) RunDaily(ctx context.Context) (DailyReport, error) {
	ctx = dbtx.System(ctx)
	var rep DailyReport
	var props []uuid.UUID
	if err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM platform.properties WHERE status = 'active' ORDER BY created_at`)
		if err != nil {
			return err
		}
		props, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		return err
	}); err != nil {
		return rep, err
	}
	for _, p := range props {
		if err := m.DB.WithTx(ctx, func(tx pgx.Tx) error { return m.dailyProperty(ctx, tx, p, &rep) }); err != nil {
			return rep, fmt.Errorf("hris daily for %s: %w", p, err)
		}
	}
	return rep, nil
}

func ids(rows pgx.Rows, err error) ([]uuid.UUID, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}

func (m *Module) dailyProperty(ctx context.Context, tx pgx.Tx, property uuid.UUID, rep *DailyReport) error {
	day := today(ctx, tx, property)
	d := ymd(day)
	cfg, _, err := hris.LoadHRConfiguration(ctx, tx, property, clock.Now())
	if err != nil {
		return err
	}
	// 1. due transfers / promotions
	changes, err := ids(tx.Query(ctx, `SELECT id FROM hris.employment_history WHERE property_id = $1 AND status = 'scheduled' AND kind <> 'termination'
		AND effective_date <= $2::date ORDER BY effective_date, created_at`, property, d))
	if err != nil {
		return err
	}
	for _, h := range changes {
		if err := m.applyChange(ctx, tx, h); err != nil {
			return err
		}
		rep.Changes++
	}
	// 2. due terminations (H7)
	leavers, err := ids(tx.Query(ctx, `SELECT id FROM hris.employees WHERE property_id = $1 AND termination_status = 'scheduled'
		AND termination_date <= $2::date`, property, d))
	if err != nil {
		return err
	}
	for _, e := range leavers {
		if err := m.completeTermination(ctx, tx, e); err != nil {
			return err
		}
		rep.Terminations++
	}
	// 3. employment status from the contract in force
	sync, err := ids(tx.Query(ctx, `SELECT DISTINCT e.id FROM hris.employees e JOIN hris.contracts c ON c.employee_id = e.id
		WHERE e.property_id = $1 AND e.status = 'active' AND e.employment_status IN ('probation', 'contract', 'permanent')
		  AND c.status IN ('active', 'expiring') AND c.start_date <= $2::date
		  AND ((e.employment_status = 'probation' AND (c.probation_end_date IS NULL OR c.probation_end_date < $2::date))
		    OR (e.employment_status = 'contract' AND c.contract_type = 'pkwtt')
		    OR (e.employment_status <> 'contract' AND c.contract_type = 'pkwt'))`, property, d))
	if err != nil {
		return err
	}
	for _, e := range sync {
		if err := m.syncEmploymentStatus(ctx, tx, e, day); err != nil {
			return err
		}
		rep.StatusChanges++
	}
	// 4. lapsed contracts end; the renewal they were superseded by is in force
	tag, err := tx.Exec(ctx, `UPDATE hris.contracts SET status = 'ended', end_reason = coalesce(end_reason, 'expired')
		WHERE property_id = $1 AND status IN ('active', 'expiring') AND coalesce(ended_on, end_date) < $2::date`, property, d)
	if err != nil {
		return err
	}
	rep.ContractsEnded += int(tag.RowsAffected())
	if n, err := m.contractReminders(ctx, tx, property, day, cfg.ContractReminderDays); err != nil {
		return err
	} else {
		rep.ContractAlerts += n
	}
	if n, err := m.documentReminders(ctx, tx, property, day, cfg.DocumentReminderDays); err != nil {
		return err
	} else {
		rep.DocumentAlerts += n
	}
	alerts, expired, err := m.certificationJob(ctx, tx, property, day, cfg.CertificationReminderDays)
	if err != nil {
		return err
	}
	rep.CertAlerts += alerts
	rep.CertsExpired += expired
	return nil
}

// due returns the reminder thresholds (days) reached by daysLeft and
// whether one of them was not sent yet.
func due(thresholds, sent []int, daysLeft int) ([]int, bool) {
	var reached []int
	fresh := false
	for _, t := range thresholds {
		if daysLeft <= t {
			reached = append(reached, t)
			if !slices.Contains(sent, t) {
				fresh = true
			}
		}
	}
	return reached, fresh
}

func (m *Module) contractReminders(ctx context.Context, tx pgx.Tx, property uuid.UUID, day time.Time, thresholds []int) (int, error) {
	if len(thresholds) == 0 {
		return 0, nil
	}
	horizon := slices.Max(thresholds)
	rows, err := tx.Query(ctx, `SELECT c.id, c.number, c.employee_id, e.employee_no, e.full_name, c.contract_type, c.end_date, c.reminders_sent,
		(c.end_date - $2::date) FROM hris.contracts c JOIN hris.employees e ON e.id = c.employee_id
		WHERE c.property_id = $1 AND c.status IN ('active', 'expiring') AND c.end_date IS NOT NULL AND c.ended_on IS NULL
		  AND c.end_date >= $2::date AND c.end_date <= $2::date + $3::int AND e.status = 'active' AND e.termination_status IS NULL`, property, ymd(day), horizon)
	if err != nil {
		return 0, err
	}
	type item struct {
		p    hris.ContractExpiring
		end  time.Time
		sent []int
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.p.ContractID, &it.p.Number, &it.p.EmployeeID, &it.p.EmployeeNo, &it.p.FullName, &it.p.ContractType, &it.end, &it.sent,
			&it.p.DaysRemaining); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	n := 0
	for _, it := range items {
		reached, fresh := due(thresholds, it.sent, it.p.DaysRemaining)
		if !fresh {
			continue
		}
		it.p.PropertyID, it.p.EndDate = property, ymd(it.end)
		if _, err := tx.Exec(ctx, `UPDATE hris.contracts SET status = 'expiring', reminders_sent = ARRAY(SELECT DISTINCT unnest(reminders_sent || $2::int[]))
			WHERE id = $1`, it.p.ContractID, reached); err != nil {
			return n, err
		}
		cid := it.p.ContractID
		if _, err := m.Events.Publish(ctx, tx, hris.EventContractExpiring, "hris.contract", &cid, &property, it.p); err != nil {
			return n, err
		}
		users := hrUsers(ctx, tx, property, "hris.contract.renew")
		if sup, err := hris.Supervisor(ctx, tx, it.p.EmployeeID); err == nil && sup != nil && sup.UserID != nil {
			users = append(users, *sup.UserID)
		}
		if err := m.notifyUsers(ctx, tx, property, users, "hris.contract_expiring", "/hris/contracts?expiring=1",
			map[string]any{"number": it.p.Number, "employeeName": it.p.FullName, "employeeNo": it.p.EmployeeNo, "contractType": strings.ToUpper(it.p.ContractType),
				"endDate": it.p.EndDate, "daysRemaining": it.p.DaysRemaining}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (m *Module) documentReminders(ctx context.Context, tx pgx.Tx, property uuid.UUID, day time.Time, thresholds []int) (int, error) {
	if len(thresholds) == 0 {
		return 0, nil
	}
	rows, err := tx.Query(ctx, `SELECT d.id, d.title, d.document_type, d.expires_on, d.reminders_sent, (d.expires_on - $2::date), e.employee_no, e.full_name,
		d.confidential FROM hris.employee_documents d JOIN hris.employees e ON e.id = d.employee_id
		WHERE d.property_id = $1 AND d.archived_at IS NULL AND d.status = 'active' AND d.expires_on IS NOT NULL AND d.expires_on >= $2::date
		  AND d.expires_on <= $2::date + $3::int AND e.status = 'active'`, property, ymd(day), slices.Max(thresholds))
	if err != nil {
		return 0, err
	}
	type item struct {
		id           uuid.UUID
		title, dtype string
		expires      time.Time
		sent         []int
		left         int
		no, name     string
		confidential bool
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.title, &it.dtype, &it.expires, &it.sent, &it.left, &it.no, &it.name, &it.confidential); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	n := 0
	for _, it := range items {
		reached, fresh := due(thresholds, it.sent, it.left)
		if !fresh {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.employee_documents SET reminders_sent = ARRAY(SELECT DISTINCT unnest(reminders_sent || $2::int[])) WHERE id = $1`,
			it.id, reached); err != nil {
			return n, err
		}
		perm := "hris.employee_document.manage"
		if it.confidential {
			perm = "hris.employee_document.view_confidential"
		}
		if err := m.notifyUsers(ctx, tx, property, hrUsers(ctx, tx, property, perm), "hris.document_expiring", "/hris/documents",
			map[string]any{"title": it.title, "documentType": documentTitle(it.dtype, nil), "employeeName": it.name, "employeeNo": it.no,
				"expiresOn": ymd(it.expires)}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (m *Module) certificationJob(ctx context.Context, tx pgx.Tx, property uuid.UUID, day time.Time, defaults []int) (alerts, expired int, err error) {
	d := ymd(day)
	type cert struct {
		id, typeID         uuid.UUID
		code, name, kind   string
		emp, partner       *uuid.UUID
		holder             string
		expires            time.Time
		sent, reminderDays []int
		left               int
		mandatory          []string
	}
	scan := func(rows pgx.Rows, err error) ([]cert, error) {
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []cert
		for rows.Next() {
			var c cert
			if err := rows.Scan(&c.id, &c.typeID, &c.code, &c.name, &c.kind, &c.emp, &c.partner, &c.holder, &c.expires, &c.sent, &c.reminderDays, &c.left,
				&c.mandatory); err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		return out, rows.Err()
	}
	const sel = `SELECT c.id, t.id, t.code, t.name, c.holder_kind, c.employee_id, c.partner_id, c.holder_name, c.expires_on, c.reminders_sent, t.reminder_days,
		(c.expires_on - $2::date), t.mandatory_for FROM hris.certifications c JOIN hris.certification_types t ON t.id = c.certification_type_id
		LEFT JOIN hris.employees e ON e.id = c.employee_id
		WHERE c.property_id = $1 AND c.archived_at IS NULL AND c.expires_on IS NOT NULL AND (c.employee_id IS NULL OR e.status = 'active')`
	// expired: past the expiry date and not renewed
	gone, err := scan(tx.Query(ctx, sel+` AND c.status = 'active' AND c.expires_on < $2::date`, property, d))
	if err != nil {
		return 0, 0, err
	}
	for _, c := range gone {
		if _, err := tx.Exec(ctx, `UPDATE hris.certifications SET status = 'expired', expired_notified_at = now() WHERE id = $1`, c.id); err != nil {
			return alerts, expired, err
		}
		cid := c.id
		if _, err := m.Events.Publish(ctx, tx, hris.EventCertificationExpired, "hris.certification", &cid, &property, hris.CertificationExpired{
			CertificationID: c.id, CertificationTypeID: c.typeID, CertificationTypeCode: c.code, CertificationTypeName: c.name, PropertyID: property,
			HolderKind: c.kind, EmployeeID: c.emp, PartnerID: c.partner, HolderName: c.holder, ExpiresOn: ymd(c.expires), MandatoryFor: c.mandatory}); err != nil {
			return alerts, expired, err
		}
		if err := m.notifyCertification(ctx, tx, property, c.emp, "hris.certification_expired", map[string]any{"typeName": c.name, "holderName": c.holder,
			"expiresOn": ymd(c.expires), "roles": strings.Join(c.mandatory, ", ")}); err != nil {
			return alerts, expired, err
		}
		expired++
	}
	// reminders H-60 / H-30 (type reminder days, else the HR Configuration)
	soon, err := scan(tx.Query(ctx, sel+` AND c.status = 'active' AND c.expires_on >= $2::date AND c.expires_on <= $2::date + 366`, property, d))
	if err != nil {
		return alerts, expired, err
	}
	for _, c := range soon {
		thresholds := c.reminderDays
		if len(thresholds) == 0 {
			thresholds = defaults
		}
		reached, fresh := due(thresholds, c.sent, c.left)
		if !fresh {
			continue
		}
		// a renewal on file already covers it
		var renewed bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.certifications n WHERE n.certification_type_id = $1 AND n.id <> $2 AND n.archived_at IS NULL
			AND n.status = 'active' AND n.holder_kind = $3 AND n.employee_id IS NOT DISTINCT FROM $4 AND n.partner_id IS NOT DISTINCT FROM $5
			AND coalesce(n.expires_on, 'infinity') > $6::date)`, c.typeID, c.id, c.kind, c.emp, c.partner, ymd(c.expires)).Scan(&renewed); err != nil {
			return alerts, expired, err
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.certifications SET reminders_sent = ARRAY(SELECT DISTINCT unnest(reminders_sent || $2::int[])) WHERE id = $1`,
			c.id, reached); err != nil {
			return alerts, expired, err
		}
		if renewed {
			continue
		}
		if err := m.notifyCertification(ctx, tx, property, c.emp, "hris.certification_expiring", map[string]any{"typeName": c.name, "holderName": c.holder,
			"expiresOn": ymd(c.expires), "daysRemaining": c.left}); err != nil {
			return alerts, expired, err
		}
		alerts++
	}
	return alerts, expired, nil
}

// notifyCertification informs HR, the holder (when an employee with a
// login) and the holder's supervisor.
func (m *Module) notifyCertification(ctx context.Context, tx pgx.Tx, property uuid.UUID, emp *uuid.UUID, event string, data map[string]any) error {
	users := hrUsers(ctx, tx, property, "hris.certification.update")
	if emp != nil {
		var user *uuid.UUID
		_ = tx.QueryRow(ctx, `SELECT id FROM platform.users WHERE employee_id = $1 AND status = 'active'`, *emp).Scan(&user)
		if user != nil {
			users = append(users, *user)
		}
		if sup, err := hris.Supervisor(ctx, tx, *emp); err == nil && sup != nil && sup.UserID != nil {
			users = append(users, *sup.UserID)
		}
	}
	return m.notifyUsers(ctx, tx, property, users, event, "/hris/certifications", data)
}
