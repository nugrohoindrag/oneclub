package reporting

// Scheduled reports (FR-BI-05): a registered report delivered as CSV, XLSX
// or PDF daily, weekly or monthly to the users of roles (and named users)
// of the property, by e-mail, WhatsApp and in-app notification. Each
// recipient gets the export under their own report permission — a user
// without the report permission is skipped (salary reports reach payroll
// roles only, FR-RPT-P5-04) — and finds it under Reports → Exports.

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/xuri/excelize/v2"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/provision"
)

// EventScheduledReport is the notification of a delivered scheduled report.
const EventScheduledReport = "reporting.scheduled_report_ready"

// ScheduledReport is a report delivered on a schedule.
type ScheduledReport struct {
	ID               uuid.UUID         `json:"id" db:"id"`
	PropertyID       uuid.UUID         `json:"propertyId" db:"property_id"`
	Name             string            `json:"name" db:"name"`
	ReportCode       string            `json:"reportCode" db:"report_code"`
	ReportName       string            `json:"reportName" db:"-"`
	Format           string            `json:"format" db:"format" enum:"csv,xlsx,pdf"`
	Params           map[string]string `json:"params" db:"parameters"`
	Period           string            `json:"period" db:"period" enum:"previous_day,previous_week,previous_month,month_to_date,year_to_date"`
	Frequency        string            `json:"frequency" db:"frequency" enum:"daily,weekly,monthly"`
	Weekday          *int              `json:"weekday" db:"weekday" doc:"Weekly: 0 = Sunday … 6 = Saturday"`
	MonthDay         *int              `json:"monthDay" db:"month_day" doc:"Monthly: day 1–28"`
	SendTime         string            `json:"sendTime" db:"send_time" doc:"HH:MM in the club time zone"`
	Channels         []string          `json:"channels" db:"channels"`
	RecipientRoles   []string          `json:"recipientRoles" db:"recipient_roles"`
	RecipientUserIDs []uuid.UUID       `json:"recipientUserIds" db:"recipient_user_ids"`
	Status           string            `json:"status" db:"status" enum:"active,paused"`
	NextRunAt        *time.Time        `json:"nextRunAt" db:"next_run_at"`
	LastRunAt        *time.Time        `json:"lastRunAt" db:"last_run_at"`
	LastStatus       *string           `json:"lastStatus" db:"last_status"`
	CreatedAt        time.Time         `json:"createdAt" db:"created_at"`
	UpdatedAt        time.Time         `json:"updatedAt" db:"updated_at"`
}

// ScheduledReportRequest creates a schedule.
type ScheduledReportRequest struct {
	Name             string            `json:"name"`
	ReportCode       string            `json:"reportCode"`
	Format           string            `json:"format" enum:"csv,xlsx,pdf"`
	Params           map[string]string `json:"params,omitempty" doc:"Report parameters besides the period (from / to are set by the period)"`
	Period           string            `json:"period" enum:"previous_day,previous_week,previous_month,month_to_date,year_to_date"`
	Frequency        string            `json:"frequency" enum:"daily,weekly,monthly"`
	Weekday          *int              `json:"weekday,omitempty"`
	MonthDay         *int              `json:"monthDay,omitempty"`
	SendTime         string            `json:"sendTime,omitempty" doc:"HH:MM (default 07:00)"`
	Channels         []string          `json:"channels,omitempty" doc:"email, whatsapp, in_app (default email + in_app)"`
	RecipientRoles   []string          `json:"recipientRoles,omitempty" doc:"Role codes, e.g. general_manager"`
	RecipientUserIDs []uuid.UUID       `json:"recipientUserIds,omitempty"`
}

// ScheduledReportUpdate changes a schedule (fields given replace).
type ScheduledReportUpdate struct {
	Name             *string            `json:"name,omitempty"`
	Format           *string            `json:"format,omitempty" enum:"csv,xlsx,pdf"`
	Params           *map[string]string `json:"params,omitempty"`
	Period           *string            `json:"period,omitempty" enum:"previous_day,previous_week,previous_month,month_to_date,year_to_date"`
	Frequency        *string            `json:"frequency,omitempty" enum:"daily,weekly,monthly"`
	Weekday          *int               `json:"weekday,omitempty"`
	MonthDay         *int               `json:"monthDay,omitempty"`
	SendTime         *string            `json:"sendTime,omitempty"`
	Channels         *[]string          `json:"channels,omitempty"`
	RecipientRoles   *[]string          `json:"recipientRoles,omitempty"`
	RecipientUserIDs *[]uuid.UUID       `json:"recipientUserIds,omitempty"`
}

// ScheduledReportRun is one delivery of a schedule.
type ScheduledReportRun struct {
	ID         uuid.UUID `json:"id" db:"id"`
	ScheduleID uuid.UUID `json:"scheduleId" db:"schedule_id"`
	Trigger    string    `json:"trigger" db:"trigger" enum:"schedule,manual"`
	PeriodFrom string    `json:"periodFrom" db:"period_from"`
	PeriodTo   string    `json:"periodTo" db:"period_to"`
	Status     string    `json:"status" db:"status" enum:"completed,partial,failed,skipped"`
	Recipients int       `json:"recipients" db:"recipients"`
	Delivered  int       `json:"delivered" db:"delivered"`
	Skipped    int       `json:"skipped" db:"skipped"`
	RowCount   *int      `json:"rowCount" db:"row_count"`
	Error      *string   `json:"error" db:"error"`
	CreatedAt  time.Time `json:"createdAt" db:"created_at"`
}

var (
	schedulePeriods = []string{"previous_day", "previous_week", "previous_month", "month_to_date", "year_to_date"}
	scheduleFreqs   = []string{"daily", "weekly", "monthly"}
	scheduleFormats = []string{"csv", "xlsx", "pdf"}
	scheduleChans   = []string{notify.ChannelEmail, notify.ChannelWhatsApp, notify.ChannelInApp}
)

// SchedulePeriod resolves the report period of a run on a local date.
func SchedulePeriod(period string, on time.Time) (time.Time, time.Time) {
	d := day(on)
	switch period {
	case "previous_day":
		y := d.AddDate(0, 0, -1)
		return y, y
	case "previous_week":
		wd := (int(d.Weekday()) + 6) % 7 // Monday = 0
		mon := d.AddDate(0, 0, -wd-7)
		return mon, mon.AddDate(0, 0, 6)
	case "previous_month":
		first := monthStart(d).AddDate(0, -1, 0)
		return first, monthEnd(first)
	case "year_to_date":
		return time.Date(d.Year(), 1, 1, 0, 0, 0, 0, time.UTC), d
	default: // month_to_date
		return monthStart(d), d
	}
}

// NextScheduledRun is the first send time strictly after `after` (in loc).
func NextScheduledRun(frequency string, weekday, monthDay *int, sendTime string, after time.Time, loc *time.Location) time.Time {
	hh, mm := 7, 0
	if t, err := time.Parse("15:04", sendTime); err == nil {
		hh, mm = t.Hour(), t.Minute()
	} else if t, err := time.Parse("15:04:05", sendTime); err == nil {
		hh, mm = t.Hour(), t.Minute()
	}
	a := after.In(loc)
	at := func(y int, mo time.Month, dd int) time.Time { return time.Date(y, mo, dd, hh, mm, 0, 0, loc) }
	switch frequency {
	case "weekly":
		w := 1
		if weekday != nil {
			w = *weekday
		}
		for i := 0; i < 8; i++ {
			c := at(a.Year(), a.Month(), a.Day()+i)
			if int(c.Weekday()) == w && c.After(a) {
				return c.UTC()
			}
		}
	case "monthly":
		md := 1
		if monthDay != nil {
			md = *monthDay
		}
		c := at(a.Year(), a.Month(), md)
		if !c.After(a) {
			c = at(a.Year(), a.Month()+1, md)
		}
		return c.UTC()
	}
	c := at(a.Year(), a.Month(), a.Day())
	if !c.After(a) {
		c = at(a.Year(), a.Month(), a.Day()+1)
	}
	return c.UTC()
}

const scheduleSelect = `SELECT id, property_id, name, report_code, format, parameters, period, frequency, weekday, month_day, to_char(send_time, 'HH24:MI') AS send_time,
	channels, recipient_roles, recipient_user_ids, status, next_run_at, last_run_at, last_status, created_at, updated_at FROM reporting.scheduled_reports`

func (b *BI) loadSchedule(ctx context.Context, q dbtx.Querier, sid uuid.UUID, lock bool) (ScheduledReport, error) {
	sql := scheduleSelect + ` WHERE id = $1 AND archived_at IS NULL`
	if lock {
		sql += ` FOR UPDATE`
	}
	rows, err := q.Query(ctx, sql, sid)
	s, err := handle.One[ScheduledReport](rows, err, "scheduled report")
	if err != nil {
		return s, err
	}
	b.decorate(&s)
	return s, nil
}

func (b *BI) decorate(s *ScheduledReport) {
	if rep, ok := b.S.find(s.ReportCode); ok {
		s.ReportName = rep.Name
	}
	if s.Params == nil {
		s.Params = map[string]string{}
	}
}

// validateSchedule checks a schedule and that its author may run the report.
func (b *BI) validateSchedule(ctx context.Context, q dbtx.Querier, property uuid.UUID, s *ScheduledReport) error {
	rep, ok := b.S.find(s.ReportCode)
	if !ok {
		return handle.Invalid("reportCode", "invalid", "unknown report")
	}
	if !authz.From(ctx).Can(rep.Permission, &property) {
		return handle.Invalid("reportCode", "forbidden", "you cannot run this report yourself")
	}
	if strings.TrimSpace(s.Name) == "" {
		s.Name = rep.Name
	}
	if !slices.Contains(scheduleFormats, s.Format) {
		return handle.Invalid("format", "invalid", "format must be csv, xlsx or pdf")
	}
	if !slices.Contains(schedulePeriods, s.Period) {
		return handle.Invalid("period", "invalid", "period must be one of "+strings.Join(schedulePeriods, ", "))
	}
	if !slices.Contains(scheduleFreqs, s.Frequency) {
		return handle.Invalid("frequency", "invalid", "frequency must be daily, weekly or monthly")
	}
	switch s.Frequency {
	case "weekly":
		if s.Weekday == nil || *s.Weekday < 0 || *s.Weekday > 6 {
			return handle.Invalid("weekday", "required", "choose the weekday (0 = Sunday … 6 = Saturday)")
		}
		s.MonthDay = nil
	case "monthly":
		if s.MonthDay == nil || *s.MonthDay < 1 || *s.MonthDay > 28 {
			return handle.Invalid("monthDay", "required", "choose the day of the month (1–28)")
		}
		s.Weekday = nil
	default:
		s.Weekday, s.MonthDay = nil, nil
	}
	if s.SendTime == "" {
		s.SendTime = "07:00"
	}
	if _, err := time.Parse("15:04", s.SendTime); err != nil {
		return handle.Invalid("sendTime", "invalid", "send time must be HH:MM")
	}
	if len(s.Channels) == 0 {
		s.Channels = []string{notify.ChannelEmail, notify.ChannelInApp}
	}
	for _, c := range s.Channels {
		if !slices.Contains(scheduleChans, c) {
			return handle.Invalid("channels", "invalid", "channels are email, whatsapp and in_app")
		}
	}
	s.Channels = dedupe(s.Channels)
	if len(s.RecipientRoles) == 0 && len(s.RecipientUserIDs) == 0 {
		return handle.Invalid("recipientRoles", "required", "choose the roles or users who receive the report")
	}
	if s.RecipientRoles == nil {
		s.RecipientRoles = []string{}
	}
	if s.RecipientUserIDs == nil {
		s.RecipientUserIDs = []uuid.UUID{}
	}
	if len(s.RecipientRoles) > 0 {
		var n int
		if err := q.QueryRow(ctx, `SELECT count(DISTINCT code) FROM platform.roles WHERE code = ANY($1)`, s.RecipientRoles).Scan(&n); err != nil {
			return err
		}
		if n != len(dedupe(s.RecipientRoles)) {
			return handle.Invalid("recipientRoles", "invalid", "unknown role")
		}
	}
	if len(s.RecipientUserIDs) > 0 {
		var n int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM platform.users WHERE id = ANY($1)`, s.RecipientUserIDs).Scan(&n); err != nil {
			return err
		}
		if n != len(s.RecipientUserIDs) {
			return handle.Invalid("recipientUserIds", "invalid", "unknown user")
		}
	}
	for k := range s.Params {
		if k == "from" || k == "to" || k == "date" {
			delete(s.Params, k)
		}
	}
	for _, p := range rep.Params {
		if v, ok := s.Params[p.Key]; ok && p.Type == "enum" && v != "" && !slices.Contains(p.Enum, v) {
			return handle.Invalid("params."+p.Key, "invalid", p.Label+" must be one of "+strings.Join(p.Enum, ", "))
		}
	}
	return nil
}

func (b *BI) createSchedule(ctx context.Context, tx pgx.Tx, r *http.Request, req ScheduledReportRequest) (ScheduledReport, error) {
	property := handle.Property(ctx)
	s := ScheduledReport{Name: req.Name, ReportCode: req.ReportCode, Format: req.Format, Params: req.Params, Period: req.Period, Frequency: req.Frequency,
		Weekday: req.Weekday, MonthDay: req.MonthDay, SendTime: req.SendTime, Channels: req.Channels, RecipientRoles: req.RecipientRoles,
		RecipientUserIDs: req.RecipientUserIDs}
	if s.Params == nil {
		s.Params = map[string]string{}
	}
	if s.Format == "" {
		s.Format = "pdf"
	}
	if err := b.validateSchedule(ctx, tx, property, &s); err != nil {
		return s, err
	}
	next := NextScheduledRun(s.Frequency, s.Weekday, s.MonthDay, s.SendTime, b.now(), calendar.Location(ctx, tx))
	sid := id.New()
	uid := handle.UserID(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO reporting.scheduled_reports (id, property_id, name, report_code, format, parameters, period, frequency, weekday,
		month_day, send_time, channels, recipient_roles, recipient_user_ids, status, next_run_at, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::time,$12,$13,$14,'active',$15,$16,$16)`, sid, property, s.Name, s.ReportCode, s.Format, s.Params, s.Period,
		s.Frequency, s.Weekday, s.MonthDay, s.SendTime, s.Channels, s.RecipientRoles, s.RecipientUserIDs, next, uid); err != nil {
		return s, err
	}
	out, err := b.loadSchedule(ctx, tx, sid, false)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "reporting", Action: audit.ActionCreate, EntityType: "reporting.scheduled_report",
		EntityID: sid.String(), EntityLabel: out.Name, PropertyID: &property, After: out})
}

func (b *BI) updateSchedule(ctx context.Context, tx pgx.Tx, r *http.Request, req ScheduledReportUpdate) (ScheduledReport, error) {
	sid, err := handle.ID(r)
	if err != nil {
		return ScheduledReport{}, err
	}
	before, err := b.loadSchedule(ctx, tx, sid, true)
	if err != nil {
		return before, err
	}
	s := before
	s.Params = map[string]string{}
	for k, v := range before.Params {
		s.Params[k] = v
	}
	if req.Name != nil {
		s.Name = *req.Name
	}
	if req.Format != nil {
		s.Format = *req.Format
	}
	if req.Params != nil {
		s.Params = *req.Params
	}
	if req.Period != nil {
		s.Period = *req.Period
	}
	if req.Frequency != nil {
		s.Frequency = *req.Frequency
	}
	if req.Weekday != nil {
		s.Weekday = req.Weekday
	}
	if req.MonthDay != nil {
		s.MonthDay = req.MonthDay
	}
	if req.SendTime != nil {
		s.SendTime = *req.SendTime
	}
	if req.Channels != nil {
		s.Channels = *req.Channels
	}
	if req.RecipientRoles != nil {
		s.RecipientRoles = *req.RecipientRoles
	}
	if req.RecipientUserIDs != nil {
		s.RecipientUserIDs = *req.RecipientUserIDs
	}
	if s.Params == nil {
		s.Params = map[string]string{}
	}
	if err := b.validateSchedule(ctx, tx, s.PropertyID, &s); err != nil {
		return before, err
	}
	next := NextScheduledRun(s.Frequency, s.Weekday, s.MonthDay, s.SendTime, b.now(), calendar.Location(ctx, tx))
	if _, err := tx.Exec(ctx, `UPDATE reporting.scheduled_reports SET name = $2, format = $3, parameters = $4, period = $5, frequency = $6, weekday = $7,
		month_day = $8, send_time = $9::time, channels = $10, recipient_roles = $11, recipient_user_ids = $12,
		next_run_at = CASE WHEN status = 'active' THEN $13 ELSE next_run_at END, updated_by = $14 WHERE id = $1`, sid, s.Name, s.Format, s.Params, s.Period,
		s.Frequency, s.Weekday, s.MonthDay, s.SendTime, s.Channels, s.RecipientRoles, s.RecipientUserIDs, next, handle.UserID(ctx)); err != nil {
		return before, err
	}
	out, err := b.loadSchedule(ctx, tx, sid, false)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "reporting", Action: audit.ActionUpdate, EntityType: "reporting.scheduled_report",
		EntityID: sid.String(), EntityLabel: out.Name, PropertyID: &out.PropertyID, Before: before, After: out})
}

func (b *BI) setScheduleStatus(status string) func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (ScheduledReport, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (ScheduledReport, error) {
		sid, err := handle.ID(r)
		if err != nil {
			return ScheduledReport{}, err
		}
		before, err := b.loadSchedule(ctx, tx, sid, true)
		if err != nil {
			return before, err
		}
		if before.Status == status {
			return before, errs.Conflict("schedule_status", "the schedule is already "+status)
		}
		var next *time.Time
		if status == "active" {
			n := NextScheduledRun(before.Frequency, before.Weekday, before.MonthDay, before.SendTime, b.now(), calendar.Location(ctx, tx))
			next = &n
		}
		if _, err := tx.Exec(ctx, `UPDATE reporting.scheduled_reports SET status = $2, next_run_at = $3, updated_by = $4 WHERE id = $1`,
			sid, status, next, handle.UserID(ctx)); err != nil {
			return before, err
		}
		out, err := b.loadSchedule(ctx, tx, sid, false)
		if err != nil {
			return out, err
		}
		return out, audit.Record(ctx, tx, audit.Entry{Module: "reporting", Action: audit.ActionStatusChange, EntityType: "reporting.scheduled_report",
			EntityID: sid.String(), EntityLabel: out.Name, PropertyID: &out.PropertyID, Before: map[string]any{"status": before.Status},
			After: map[string]any{"status": status}})
	}
}

func (b *BI) deleteSchedule(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (struct{}, error) {
	sid, err := handle.ID(r)
	if err != nil {
		return struct{}{}, err
	}
	before, err := b.loadSchedule(ctx, tx, sid, true)
	if err != nil {
		return struct{}{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE reporting.scheduled_reports SET archived_at = now(), status = 'paused', next_run_at = NULL, updated_by = $2 WHERE id = $1`,
		sid, handle.UserID(ctx)); err != nil {
		return struct{}{}, err
	}
	return struct{}{}, audit.Record(ctx, tx, audit.Entry{Module: "reporting", Action: audit.ActionArchive, EntityType: "reporting.scheduled_report",
		EntityID: sid.String(), EntityLabel: before.Name, PropertyID: &before.PropertyID, Before: before})
}

// runNow delivers a schedule immediately (manual run).
func (b *BI) runNow(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (ScheduledReportRun, error) {
	sid, err := handle.ID(r)
	if err != nil {
		return ScheduledReportRun{}, err
	}
	s, err := b.loadSchedule(ctx, tx, sid, true)
	if err != nil {
		return ScheduledReportRun{}, err
	}
	run, err := b.deliver(ctx, tx, s, "manual", handle.UserID(ctx))
	if err != nil {
		return run, err
	}
	return run, audit.Record(ctx, tx, audit.Entry{Module: "reporting", Action: audit.ActionExport, EntityType: "reporting.scheduled_report",
		EntityID: sid.String(), EntityLabel: s.Name, PropertyID: &s.PropertyID,
		After: map[string]any{"run": run.ID, "period": run.PeriodFrom + " – " + run.PeriodTo, "delivered": run.Delivered, "skipped": run.Skipped}})
}

// recipients resolves the users of a schedule at its property.
func recipients(ctx context.Context, q dbtx.Querier, s ScheduledReport) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT u.id FROM platform.users u JOIN platform.role_assignments ra ON ra.user_id = u.id
		JOIN platform.roles r ON r.id = ra.role_id
		WHERE u.status = 'active' AND r.code = ANY($1) AND (ra.property_id IS NULL OR ra.property_id = $2) AND (ra.valid_until IS NULL OR ra.valid_until > now())
		UNION SELECT id FROM platform.users WHERE id = ANY($3) AND status = 'active'`, s.RecipientRoles, s.PropertyID, s.RecipientUserIDs)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	slices.SortFunc(out, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	return out, err
}

// renderReport builds the export file of a report (CSV, XLSX or PDF).
func renderReport(rep *Report, format string, prm map[string]string, rows []map[string]any, at time.Time) ([]byte, string, error) {
	var buf bytes.Buffer
	switch format {
	case "pdf":
		buf.Write(reportPDF(rep, prm, rows, at))
		return buf.Bytes(), "application/pdf", nil
	case "xlsx":
		f := excelize.NewFile()
		for i, c := range rep.Columns {
			ref, _ := excelize.CoordinatesToCellName(i+1, 1)
			_ = f.SetCellValue("Sheet1", ref, c.Label)
		}
		for ri, row := range rows {
			for ci, c := range rep.Columns {
				ref, _ := excelize.CoordinatesToCellName(ci+1, ri+2)
				_ = f.SetCellValue("Sheet1", ref, cellValue(row[c.Key]))
			}
		}
		if err := f.Write(&buf); err != nil {
			return nil, "", err
		}
		_ = f.Close()
		return buf.Bytes(), "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", nil
	}
	cw := csv.NewWriter(&buf)
	hdr := make([]string, len(rep.Columns))
	for i, c := range rep.Columns {
		hdr[i] = c.Label
	}
	_ = cw.Write(hdr)
	for _, row := range rows {
		rec := make([]string, len(rep.Columns))
		for i, c := range rep.Columns {
			rec[i] = fmt.Sprint(cellValue(row[c.Key]))
		}
		_ = cw.Write(rec)
	}
	cw.Flush()
	return buf.Bytes(), "text/csv", nil
}

// deliver runs a schedule in tx: one export per recipient holding the
// report permission, notified on the schedule's channels.
func (b *BI) deliver(ctx context.Context, tx pgx.Tx, s ScheduledReport, trigger string, by uuid.UUID) (ScheduledReportRun, error) {
	loc := calendar.Location(ctx, tx)
	now := b.now()
	from, to := SchedulePeriod(s.Period, now.In(loc))
	run := ScheduledReportRun{ID: id.New(), ScheduleID: s.ID, Trigger: trigger, PeriodFrom: from.Format(dateFmt), PeriodTo: to.Format(dateFmt),
		CreatedAt: now.UTC()}
	var creator *uuid.UUID
	if by != uuid.Nil {
		creator = &by
	}
	finish := func(status string, msg *string) (ScheduledReportRun, error) {
		run.Status, run.Error = status, msg
		if _, err := tx.Exec(ctx, `INSERT INTO reporting.scheduled_report_runs (id, schedule_id, property_id, trigger, period_from, period_to, status, recipients,
			delivered, skipped, row_count, error, created_by) VALUES ($1,$2,$3,$4,$5::date,$6::date,$7,$8,$9,$10,$11,$12,$13)`,
			run.ID, s.ID, s.PropertyID, trigger, run.PeriodFrom, run.PeriodTo, status, run.Recipients, run.Delivered, run.Skipped, run.RowCount, msg,
			creator); err != nil {
			return run, err
		}
		next := NextScheduledRun(s.Frequency, s.Weekday, s.MonthDay, s.SendTime, now, loc)
		_, err := tx.Exec(ctx, `UPDATE reporting.scheduled_reports SET last_run_at = now(), last_status = $2,
			next_run_at = CASE WHEN status = 'active' AND $3 = 'schedule' THEN $4 ELSE next_run_at END WHERE id = $1`, s.ID, status, trigger, next)
		return run, err
	}
	rep, ok := b.S.find(s.ReportCode)
	if !ok {
		msg := "the report no longer exists"
		return finish("failed", &msg)
	}
	users, err := recipients(ctx, tx, s)
	if err != nil {
		return run, err
	}
	pol := LoadBIPolicy(ctx, tx, s.PropertyID)
	if len(users) > pol.MaxScheduledRecipients {
		users = users[:pol.MaxScheduledRecipients]
	}
	run.Recipients = len(users)
	var allowed []*authz.Principal
	for _, u := range users {
		p, err := b.LoadAuthz(ctx, u)
		if err != nil || p == nil || !p.Can(rep.Permission, &s.PropertyID) {
			run.Skipped++
			continue
		}
		allowed = append(allowed, p)
	}
	if len(allowed) == 0 {
		msg := "no recipient holds the report permission at the property"
		return finish("skipped", &msg)
	}
	prm := map[string]string{}
	for k, v := range s.Params {
		prm[k] = v
	}
	prm["from"], prm["to"], prm["date"] = run.PeriodFrom, run.PeriodTo, run.PeriodTo
	prm["propertyId"] = s.PropertyID.String()
	// the rows of the property (RLS narrowed to it) on the read replica
	qctx := dbtx.WithScope(ctx, dbtx.Scope{PropertyIDs: []uuid.UUID{s.PropertyID}})
	var rows []map[string]any
	if err := b.S.DB.WithReportTx(qctx, func(rtx pgx.Tx) error {
		if err := queryBudget(qctx, rtx, max(pol.QueryTimeoutSeconds, 120)); err != nil {
			return err
		}
		var err error
		rows, err = rep.Query(qctx, rtx, prm, 100000)
		return err
	}); err != nil {
		msg := "the report failed: " + err.Error()
		slog.WarnContext(ctx, "scheduled report failed", "schedule", s.ID, "err", err)
		return finish("failed", &msg)
	}
	n := len(rows)
	run.RowCount = &n
	body, ctype, err := renderReport(rep, s.Format, prm, rows, now)
	if err != nil {
		return run, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO reporting.scheduled_report_runs (id, schedule_id, property_id, trigger, period_from, period_to, status, created_by)
		VALUES ($1,$2,$3,$4,$5::date,$6::date,'completed',$7)`, run.ID, s.ID, s.PropertyID, trigger, run.PeriodFrom, run.PeriodTo, creator); err != nil {
		return run, err
	}
	name := strings.ReplaceAll(strings.ToLower(s.Name), " ", "-") + "-" + run.PeriodFrom + "-" + run.PeriodTo + "." + s.Format
	link := ""
	if b.S.Cfg != nil {
		link = b.S.Cfg.PublicBaseURL + "/reports/exports"
	}
	for _, p := range allowed {
		pctx := authz.WithPrincipal(ctx, p)
		f, err := b.S.Files.Save(pctx, tx, name, ctype, "export", false, bytes.NewReader(body), int64(len(body)))
		if err != nil {
			return run, err
		}
		eid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO reporting.exports (id, report_code, format, parameters, property_id, status, file_id, row_count, requested_by,
			completed_at, schedule_run_id) VALUES ($1,$2,$3,$4,$5,'completed',$6,$7,$8,now(),$9)`, eid, rep.Code, s.Format, prm, s.PropertyID, f.ID, n,
			p.UserID, run.ID); err != nil {
			return run, err
		}
		if err := b.S.Notify.Send(ctx, tx, notify.Message{Event: EventScheduledReport, Category: "report", UserIDs: []uuid.UUID{p.UserID},
			Channels: s.Channels, Link: link, PropertyID: &s.PropertyID,
			Data: map[string]any{"name": s.Name, "reportName": rep.Name, "format": strings.ToUpper(s.Format), "rowCount": n, "from": run.PeriodFrom,
				"to": run.PeriodTo, "frequency": s.Frequency, "link": link}}); err != nil {
			return run, err
		}
		run.Delivered++
	}
	status := "completed"
	if run.Skipped > 0 {
		status = "partial"
	}
	run.Status = status
	if _, err := tx.Exec(ctx, `UPDATE reporting.scheduled_report_runs SET status = $2, recipients = $3, delivered = $4, skipped = $5, row_count = $6
		WHERE id = $1`, run.ID, status, run.Recipients, run.Delivered, run.Skipped, n); err != nil {
		return run, err
	}
	next := NextScheduledRun(s.Frequency, s.Weekday, s.MonthDay, s.SendTime, now, loc)
	_, err = tx.Exec(ctx, `UPDATE reporting.scheduled_reports SET last_run_at = now(), last_status = $2,
		next_run_at = CASE WHEN status = 'active' AND $3 = 'schedule' THEN $4 ELSE next_run_at END WHERE id = $1`, s.ID, status, trigger, next)
	return run, err
}

// RunDue delivers the schedules whose send time has come (job).
func (b *BI) RunDue(ctx context.Context) (int, error) {
	sys := dbtx.System(ctx)
	var due []struct {
		ID         uuid.UUID `db:"id"`
		PropertyID uuid.UUID `db:"property_id"`
	}
	if err := b.S.DB.WithReadTx(sys, func(tx pgx.Tx) error {
		rows, err := tx.Query(sys, `SELECT id, property_id FROM reporting.scheduled_reports WHERE status = 'active' AND archived_at IS NULL
			AND next_run_at <= $1 ORDER BY next_run_at LIMIT 100`, b.now())
		if err != nil {
			return err
		}
		due, err = pgx.CollectRows(rows, pgx.RowToStructByName[struct {
			ID         uuid.UUID `db:"id"`
			PropertyID uuid.UUID `db:"property_id"`
		}])
		return err
	}); err != nil {
		return 0, err
	}
	n := 0
	for _, d := range due {
		pctx := reqctx.WithProperty(authz.WithPrincipal(dbtx.WithScope(ctx, dbtx.Scope{PropertyIDs: []uuid.UUID{d.PropertyID}}), authz.System()), d.PropertyID)
		err := b.S.DB.WithTx(pctx, func(tx pgx.Tx) error {
			var s ScheduledReport
			s, err := b.loadSchedule(pctx, tx, d.ID, true)
			if err != nil {
				return err
			}
			if s.Status != "active" || s.NextRunAt == nil || s.NextRunAt.After(b.now()) {
				return nil // delivered meanwhile
			}
			_, err = b.deliver(pctx, tx, s, "schedule", uuid.Nil)
			return err
		})
		if err != nil {
			slog.ErrorContext(ctx, "scheduled report delivery failed", "schedule", d.ID, "err", err)
			continue
		}
		n++
	}
	return n, nil
}

// ScheduledReportArgs triggers the delivery of due schedules.
type ScheduledReportArgs struct{}

func (ScheduledReportArgs) Kind() string { return "scheduled_reports" }

func (ScheduledReportArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

// ScheduledReportWorker delivers the due schedules.
type ScheduledReportWorker struct {
	river.WorkerDefaults[ScheduledReportArgs]
	BI *BI
}

func (w *ScheduledReportWorker) Timeout(*river.Job[ScheduledReportArgs]) time.Duration {
	return 15 * time.Minute
}

func (w *ScheduledReportWorker) Work(ctx context.Context, _ *river.Job[ScheduledReportArgs]) error {
	_, err := w.BI.RunDue(ctx)
	return err
}

// ScheduledReportTemplates are the notification templates (ID / EN).
func ScheduledReportTemplates() []provision.Template {
	t := map[string][2]string{
		"en": {"Scheduled report: {{.name}} ({{.from}} – {{.to}})",
			"Your {{.frequency}} report {{.name}} ({{.reportName}}, {{.format}}, {{.rowCount}} rows) for {{.from}} – {{.to}} is ready under Reports → Exports.\n\n{{.link}}"},
		"id": {"Laporan terjadwal: {{.name}} ({{.from}} – {{.to}})",
			"Laporan {{.name}} ({{.reportName}}, {{.format}}, {{.rowCount}} baris) periode {{.from}} – {{.to}} sudah tersedia di Reports → Exports.\n\n{{.link}}"},
	}
	var out []provision.Template
	for _, ch := range []string{notify.ChannelInApp, notify.ChannelEmail, notify.ChannelWhatsApp} {
		for loc, v := range t {
			out = append(out, provision.Template{Event: EventScheduledReport, Channel: ch, Locale: loc, Subject: v[0], Body: v[1]})
		}
	}
	slices.SortFunc(out, func(a, b provision.Template) int {
		return strings.Compare(a.Channel+a.Locale, b.Channel+b.Locale)
	})
	return out
}

func (b *BI) listSchedules(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[ScheduledReport], error) {
	list, err := handle.List[ScheduledReport](tx.Query(ctx, scheduleSelect+` WHERE property_id = $1 AND archived_at IS NULL ORDER BY name`, handle.Property(ctx)))
	for i := range list {
		b.decorate(&list[i])
	}
	return httpx.Page[ScheduledReport]{Items: list}, err
}

func (b *BI) getSchedule(ctx context.Context, tx pgx.Tx, r *http.Request) (ScheduledReport, error) {
	sid, err := handle.ID(r)
	if err != nil {
		return ScheduledReport{}, err
	}
	return b.loadSchedule(ctx, tx, sid, false)
}

func (b *BI) listRuns(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[ScheduledReportRun], error) {
	sid, err := handle.ID(r)
	if err != nil {
		return httpx.Page[ScheduledReportRun]{}, err
	}
	if _, err := b.loadSchedule(ctx, tx, sid, false); err != nil {
		return httpx.Page[ScheduledReportRun]{}, err
	}
	return handle.Page(handle.List[ScheduledReportRun](tx.Query(ctx, `SELECT id, schedule_id, trigger, period_from::text AS period_from,
		period_to::text AS period_to, status, recipients, delivered, skipped, row_count, error, created_at FROM reporting.scheduled_report_runs
		WHERE schedule_id = $1 ORDER BY created_at DESC LIMIT 50`, sid)))
}

func (b *BI) registerSchedules(add func(route.Route)) {
	db := b.S.DB
	const base = "/api/v1/reporting/scheduled-reports"
	add(route.Route{Method: http.MethodGet, Path: base, Scope: route.ScopeProperty, Permission: PermScheduledReportView, Summary: "Scheduled reports",
		Response: ScheduledReport{}, List: true, Handler: handle.Read(db, b.listSchedules)})
	add(route.Route{Method: http.MethodPost, Path: base, Scope: route.ScopeProperty, Permission: PermScheduledReportManage,
		Summary: "Schedule a report (daily / weekly / monthly, e-mail / WhatsApp, per role)", Request: ScheduledReportRequest{}, Response: ScheduledReport{},
		Handler: handle.Write(db, http.StatusCreated, b.createSchedule)})
	add(route.Route{Method: http.MethodGet, Path: base + "/{id}", Scope: route.ScopeProperty, Permission: PermScheduledReportView, Summary: "Scheduled report",
		Response: ScheduledReport{}, Handler: handle.Read(db, b.getSchedule)})
	add(route.Route{Method: http.MethodPatch, Path: base + "/{id}", Scope: route.ScopeProperty, Permission: PermScheduledReportManage,
		Summary: "Change a scheduled report", Request: ScheduledReportUpdate{}, Response: ScheduledReport{}, Handler: handle.Write(db, http.StatusOK, b.updateSchedule)})
	add(route.Route{Method: http.MethodDelete, Path: base + "/{id}", Scope: route.ScopeProperty, Permission: PermScheduledReportManage,
		Summary: "Remove a scheduled report", Handler: handle.Write(db, http.StatusNoContent, b.deleteSchedule)})
	add(route.Route{Method: http.MethodPost, Path: base + "/{id}:pause", Scope: route.ScopeProperty, Permission: PermScheduledReportManage,
		Summary: "Pause a scheduled report", Response: ScheduledReport{}, Status: http.StatusOK, Handler: handle.Write(db, http.StatusOK, b.setScheduleStatus("paused"))})
	add(route.Route{Method: http.MethodPost, Path: base + "/{id}:resume", Scope: route.ScopeProperty, Permission: PermScheduledReportManage,
		Summary: "Resume a scheduled report", Response: ScheduledReport{}, Status: http.StatusOK, Handler: handle.Write(db, http.StatusOK, b.setScheduleStatus("active"))})
	add(route.Route{Method: http.MethodPost, Path: base + "/{id}:run", Scope: route.ScopeProperty, Permission: PermScheduledReportManage,
		Summary: "Deliver a scheduled report now", Response: ScheduledReportRun{}, Handler: handle.Write(db, http.StatusCreated, b.runNow)})
	add(route.Route{Method: http.MethodGet, Path: base + "/{id}/runs", Scope: route.ScopeProperty, Permission: PermScheduledReportView,
		Summary: "Deliveries of a scheduled report", Response: ScheduledReportRun{}, List: true, Handler: handle.Read(db, b.listRuns)})
}
