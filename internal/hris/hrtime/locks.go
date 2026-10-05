package hrtime

// Payroll period locks and the time summary of a period (the EP-09
// contract, hris.TimeSummaries): HR / Finance close the attendance of a
// period before payroll; corrections, leave, permission and overtime of a
// locked period are refused until the lock is released.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// TimeLockRequest locks a payroll period.
type TimeLockRequest struct {
	PeriodStart string `json:"periodStart"`
	PeriodEnd   string `json:"periodEnd"`
	Reference   string `json:"reference" doc:"e.g. the payroll run, PAY-2026-10"`
}

// TimeLockRelease releases a lock.
type TimeLockRelease struct {
	Note string `json:"note"`
}

func (m *Module) registerLocks(reg *route.Registry) {
	tag := "HRIS Attendance"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/time-locks", Summary: "Payroll period locks of attendance",
		Permission: PermLockView, Response: hris.TimeLock{}, List: true, Handler: listRead(m.DB, m.locksHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/time-locks", Summary: "Close and lock the attendance of a payroll period",
		Permission: PermLockManage, Request: TimeLockRequest{}, Response: hris.TimeLock{}, Idempotent: true,
		Handler: handle.Write(m.DB, http.StatusCreated, m.lockHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/time-locks/{id}:release", Summary: "Release a payroll period lock",
		Permission: PermLockManage, Request: TimeLockRelease{}, Response: hris.TimeLock{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.releaseHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/time-summary",
		Summary: "Time & attendance per employee of a payroll period (payroll input)", Permission: PermLockView, Response: hris.TimeSummary{}, List: true,
		Query: []route.Param{{Name: "from", Required: true}, {Name: "to", Required: true}, {Name: "employeeId"}}, Handler: listRead(m.DB, m.summaryHTTP)})
}

const lockSelect = `SELECT id, property_id, period_start, period_end, reference, status, locked_at, released_at, release_note FROM hris.time_locks`

func (m *Module) locksHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request) ([]hris.TimeLock, error) {
	return handle.List[hris.TimeLock](tx.Query(ctx, lockSelect+` WHERE property_id = $1 ORDER BY period_start DESC, locked_at DESC LIMIT 200`,
		handle.Property(ctx)))
}

func (m *Module) lockHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req TimeLockRequest) (hris.TimeLock, error) {
	property := handle.Property(ctx)
	from, err := mustDate("periodStart", req.PeriodStart)
	if err != nil {
		return hris.TimeLock{}, err
	}
	to, err := mustDate("periodEnd", req.PeriodEnd)
	if err != nil {
		return hris.TimeLock{}, err
	}
	if to.Before(from) || to.Sub(from) > 62*24*time.Hour {
		return hris.TimeLock{}, handle.Invalid("periodEnd", "invalid", "a payroll period of at most 63 days")
	}
	if !to.Before(today(ctx, tx, property)) {
		return hris.TimeLock{}, handle.Invalid("periodEnd", "not_closed", "a period is locked after its last day")
	}
	if strings.TrimSpace(req.Reference) == "" {
		return hris.TimeLock{}, handle.Invalid("reference", "required", "name the payroll period")
	}
	if err := hris.FinalizeAttendance(ctx, tx, property, from, to); err != nil {
		return hris.TimeLock{}, err
	}
	lid, err := hris.LockTimePeriod(ctx, tx, property, from, to, strings.TrimSpace(req.Reference), actor(ctx))
	if err != nil {
		return hris.TimeLock{}, err
	}
	l, err := getOne[hris.TimeLock]("lock")(tx.Query(ctx, lockSelect+` WHERE id = $1`, lid))
	if err != nil {
		return l, err
	}
	return l, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "lock", EntityType: "hris.time_lock", EntityID: lid.String(),
		EntityLabel: l.Reference, PropertyID: &property, After: map[string]any{"periodStart": ymd(from), "periodEnd": ymd(to)}})
}

func (m *Module) releaseHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req TimeLockRelease) (hris.TimeLock, error) {
	property := handle.Property(ctx)
	lid, err := handle.ID(r)
	if err != nil {
		return hris.TimeLock{}, err
	}
	if strings.TrimSpace(req.Note) == "" {
		return hris.TimeLock{}, handle.Invalid("note", "required", "explain why the period opens again")
	}
	if err := hris.ReleaseTimeLock(ctx, tx, property, lid, actor(ctx), req.Note); err != nil {
		return hris.TimeLock{}, err
	}
	l, err := getOne[hris.TimeLock]("lock")(tx.Query(ctx, lockSelect+` WHERE id = $1`, lid))
	if err != nil {
		return l, err
	}
	return l, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "release", EntityType: "hris.time_lock", EntityID: lid.String(),
		EntityLabel: l.Reference, PropertyID: &property, Reason: req.Note, After: map[string]any{"status": l.Status}})
}

func (m *Module) summaryHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]hris.TimeSummary, error) {
	property := handle.Property(ctx)
	from, err := handle.QueryDate(r, "from", time.Time{})
	if err != nil {
		return nil, err
	}
	to, err := handle.QueryDate(r, "to", time.Time{})
	if err != nil {
		return nil, err
	}
	if from.IsZero() || to.IsZero() || to.Before(from) || to.Sub(from) > 62*24*time.Hour {
		return nil, errs.BadRequest("invalid_period", "from and to (YYYY-MM-DD) of at most 63 days")
	}
	var ids []uuid.UUID
	if emp, err := handle.QueryUUID(r, "employeeId"); err != nil {
		return nil, err
	} else if emp != nil {
		ids = []uuid.UUID{*emp}
	}
	return hris.TimeSummaries(ctx, tx, property, from, to, ids)
}
