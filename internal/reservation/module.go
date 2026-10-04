// Package reservation is the Reservation Engine. P0 provides the Resource
// foundation entity and the allocation component with the EXCLUDE
// constraint that prevents double booking at the database level (FR-TEC-06,
// Technical Doc §7.3); booking flows arrive in P1.
package reservation

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/resource"
)

var Resources = &resource.Def{
	Key: "reservation.resource", Module: "reservation", Perm: "reservation.resource", Path: "/api/v1/reservation/resources", Table: "reservation.resources",
	Name: "Resource", Plural: "Resources", Tag: "Foundation Data", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "resourceType", Column: "resource_type", Label: "Resource Type", Kind: resource.String, Max: 40, Default: "other", Filter: true},
		{Name: "venueId", Column: "venue_id", Label: "Venue", Kind: resource.UUID, Ref: &resource.Ref{Table: "platform.venues", SameProperty: true, Label: "venue"}},
		{Name: "capacity", Column: "capacity", Label: "Capacity", Kind: resource.Int, Min: resource.Min(1)},
		resource.Status("active", "inactive"), resource.Attributes()},
}

// Contribution returns catalogue entries.
func Contribution() catalog.Contribution {
	return catalog.Contribution{
		Permissions: resource.Permissions(Resources),
		RolePermissions: map[string][]string{
			"property_admin":    resource.AllActions(Resources),
			"reservation_staff": {"reservation.resource.view"},
		},
	}
}

// ErrSlotTaken is returned when an allocation overlaps another.
var ErrSlotTaken = errs.Conflict("slot_taken", "this resource is already booked for an overlapping period")

// Allocation is a lock on a resource for a period.
type Allocation struct {
	ID            uuid.UUID
	PropertyID    uuid.UUID
	ResourceID    uuid.UUID
	ReservationID uuid.UUID
	Start, End    time.Time
	Status        string
	ExpiresAt     *time.Time
}

// Hold places a temporary hold (e.g. online booking cart). The EXCLUDE
// constraint rejects overlaps with other held/confirmed allocations.
func Hold(ctx context.Context, tx pgx.Tx, property, resourceID, reservationID uuid.UUID, start, end time.Time, ttl time.Duration) (Allocation, error) {
	exp := clock.Now().Add(ttl)
	return insert(ctx, tx, Allocation{PropertyID: property, ResourceID: resourceID, ReservationID: reservationID, Start: start, End: end, Status: "held", ExpiresAt: &exp})
}

// Confirm allocates directly in status confirmed.
func Confirm(ctx context.Context, tx pgx.Tx, property, resourceID, reservationID uuid.UUID, start, end time.Time) (Allocation, error) {
	return insert(ctx, tx, Allocation{PropertyID: property, ResourceID: resourceID, ReservationID: reservationID, Start: start, End: end, Status: "confirmed"})
}

func insert(ctx context.Context, tx pgx.Tx, a Allocation) (Allocation, error) {
	if !a.End.After(a.Start) {
		return a, errs.Validation("invalid_period", "end must be after start")
	}
	a.ID = id.New()
	sp, err := tx.Begin(ctx)
	if err != nil {
		return a, err
	}
	_, err = sp.Exec(ctx, `INSERT INTO reservation.allocations (id, property_id, resource_id, reservation_id, period, status, expires_at)
		VALUES ($1, $2, $3, $4, tstzrange($5, $6, '[)'), $7, $8)`, a.ID, a.PropertyID, a.ResourceID, a.ReservationID, a.Start, a.End, a.Status, a.ExpiresAt)
	if err != nil {
		_ = sp.Rollback(ctx)
		if dbtx.IsExclusionViolation(err) {
			return a, ErrSlotTaken
		}
		return a, err
	}
	return a, sp.Commit(ctx)
}

// ConfirmHold turns a held allocation into confirmed.
func ConfirmHold(ctx context.Context, tx pgx.Tx, allocationID uuid.UUID) error {
	tag, err := tx.Exec(ctx, `UPDATE reservation.allocations SET status = 'confirmed', expires_at = NULL
		WHERE id = $1 AND status = 'held' AND expires_at > now()`, allocationID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errs.Conflict("hold_expired", "the hold has expired or was released")
	}
	return nil
}

// ReleaseExpired releases holds past their expiry; returns the count.
func ReleaseExpired(ctx context.Context, db *dbtx.DB) (int64, error) {
	var n int64
	err := db.WithTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE reservation.allocations SET status = 'released' WHERE status = 'held' AND expires_at <= now()`)
		n = tag.RowsAffected()
		return err
	})
	return n, err
}

// ReleaseHoldsArgs is the periodic hold release job.
type ReleaseHoldsArgs struct{}

func (ReleaseHoldsArgs) Kind() string { return "reservation_release_holds" }

func (ReleaseHoldsArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

// ReleaseHoldsWorker releases expired holds every minute.
type ReleaseHoldsWorker struct {
	river.WorkerDefaults[ReleaseHoldsArgs]
	DB *dbtx.DB
}

func (w *ReleaseHoldsWorker) Work(ctx context.Context, _ *river.Job[ReleaseHoldsArgs]) error {
	_, err := ReleaseExpired(ctx, w.DB)
	return err
}

// RegisterJobs adds the worker and its schedule.
func RegisterJobs(reg *jobs.Registrar, db *dbtx.DB) {
	river.AddWorker(reg.Workers, &ReleaseHoldsWorker{DB: db})
	reg.Periodic = append(reg.Periodic, river.NewPeriodicJob(river.PeriodicInterval(time.Minute),
		func() (river.JobArgs, *river.InsertOpts) { return ReleaseHoldsArgs{}, nil }, nil))
}

// EnsureResource returns the id of a resource by code, creating it when
// missing (tee time seats are created by golf on demand).
func EnsureResource(ctx context.Context, tx pgx.Tx, property uuid.UUID, code, name, resourceType string, venueID *uuid.UUID, attrs map[string]any) (uuid.UUID, error) {
	var rid uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM reservation.resources WHERE property_id = $1 AND code = $2`, property, code).Scan(&rid)
	if err == nil {
		return rid, nil
	}
	if !dbtx.IsNoRows(err) {
		return uuid.Nil, err
	}
	rid = id.New()
	raw, _ := json.Marshal(attrs)
	if attrs == nil {
		raw = []byte("{}")
	}
	err = tx.QueryRow(ctx, `INSERT INTO reservation.resources (id, property_id, code, name, resource_type, venue_id, capacity, attributes)
		VALUES ($1,$2,$3,$4,$5,$6,1,$7) ON CONFLICT (property_id, code) DO UPDATE SET name = EXCLUDED.name RETURNING id`,
		rid, property, code, name, resourceType, venueID, raw).Scan(&rid)
	return rid, err
}

// Busy returns the resources among ids that have a held or confirmed
// allocation overlapping [start, end).
func Busy(ctx context.Context, q dbtx.Querier, ids []uuid.UUID, start, end time.Time) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT DISTINCT resource_id FROM reservation.allocations WHERE resource_id = ANY($1) AND status IN ('held', 'confirmed')
		AND period && tstzrange($2, $3, '[)') AND (status = 'confirmed' OR expires_at > now())`, ids, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r uuid.UUID
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out[r] = true
	}
	return out, rows.Err()
}

// ReleaseExpiredOn releases expired holds on resources so the space can be
// re-used immediately (the periodic job does the same for everything).
func ReleaseExpiredOn(ctx context.Context, tx pgx.Tx, ids []uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE reservation.allocations SET status = 'released' WHERE resource_id = ANY($1) AND status = 'held' AND expires_at <= now()`, ids)
	return err
}

// ConfirmReservation confirms every held allocation of a reservation
// (booking). It fails with hold_expired when a hold has lapsed.
func ConfirmReservation(ctx context.Context, tx pgx.Tx, reservationID uuid.UUID) (int64, error) {
	var lapsed int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM reservation.allocations WHERE reservation_id = $1 AND status = 'released'
		AND NOT EXISTS (SELECT 1 FROM reservation.allocations x WHERE x.reservation_id = $1 AND x.status IN ('held','confirmed'))`, reservationID).Scan(&lapsed); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `UPDATE reservation.allocations SET status = 'confirmed', expires_at = NULL
		WHERE reservation_id = $1 AND status = 'held' AND expires_at > now()`, reservationID)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 0 && lapsed > 0 {
		return 0, errs.Conflict("hold_expired", "the hold has expired or was released")
	}
	return tag.RowsAffected(), nil
}

// ExtendHold moves the expiry of held allocations (payment pending).
func ExtendHold(ctx context.Context, tx pgx.Tx, reservationID uuid.UUID, until time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE reservation.allocations SET expires_at = $2 WHERE reservation_id = $1 AND status = 'held'`, reservationID, until)
	return err
}

// Release cancels every active allocation of a reservation.
func Release(ctx context.Context, tx pgx.Tx, reservationID uuid.UUID, status string) error {
	if status == "" {
		status = "cancelled"
	}
	_, err := tx.Exec(ctx, `UPDATE reservation.allocations SET status = $2, expires_at = NULL WHERE reservation_id = $1 AND status IN ('held', 'confirmed')`,
		reservationID, status)
	return err
}

// ReleaseAllocations cancels specific allocations (a removed player).
func ReleaseAllocations(ctx context.Context, tx pgx.Tx, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE reservation.allocations SET status = 'cancelled', expires_at = NULL WHERE id = ANY($1) AND status IN ('held', 'confirmed')`, ids)
	return err
}

// Status returns the status of an allocation.
func Status(ctx context.Context, q dbtx.Querier, allocationID uuid.UUID) (string, error) {
	var st string
	err := q.QueryRow(ctx, `SELECT CASE WHEN status = 'held' AND expires_at <= now() THEN 'expired' ELSE status END FROM reservation.allocations WHERE id = $1`,
		allocationID).Scan(&st)
	return st, err
}
