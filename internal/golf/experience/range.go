package experience

// Driving Range (PRD P2 EP-12): bays (bookable through the Reservation
// Engine), walk-in queue and bay assignment, buckets sold at the "Driving
// Range Counter" POS outlet or redeemed from a prepaid ball balance (EP-19),
// ball dispenser through the bridge agent with a manual code fallback, and
// the range usage report.

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/commercial"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/integration"
	"oneclub/internal/platform/resource"
	"oneclub/internal/reservation"
)

// BallCategory is the voucher category of prepaid ball packages.
const BallCategory = "driving_range_balls"

func (m *Module) rangeHooks() {
	sync := func(ctx context.Context, tx pgx.Tx, row map[string]any) error {
		pid := handle.Property(ctx)
		var existing *uuid.UUID
		if s := str(row["resourceId"]); s != "" {
			u, _ := uuid.Parse(s)
			existing = &u
		}
		st := "active"
		if str(row["status"]) != "active" || str(row["readiness"]) == "maintenance" {
			st = "inactive"
		}
		one := 1
		rid, err := m.Reservations.EnsureResource(ctx, tx, existing, reservation.ResourceRequest{PropertyID: pid, Code: "BAY-" + str(row["code"]), Name: str(row["name"]),
			ResourceType: "driving_range_bay", Capacity: &one, Status: st, Attributes: map[string]any{"priceItem": "BAY-" + str(row["area"]), "bayId": str(row["id"])}})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE golf.range_bays SET resource_id = $2 WHERE id = $1`, row["id"], rid)
		return err
	}
	RangeBays.Hooks = resource.Hooks{
		AfterCreate: sync,
		AfterUpdate: func(ctx context.Context, tx pgx.Tx, _, after map[string]any) error { return sync(ctx, tx, after) },
	}
}

// RangeSession is a walk-in or booked range visit.
type RangeSession struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	Number       string     `json:"number" db:"number"`
	BayID        *uuid.UUID `json:"bayId" db:"bay_id"`
	BayCode      *string    `json:"bayCode" db:"bay_code"`
	Area         string     `json:"area" db:"area"`
	CustomerID   *uuid.UUID `json:"customerId" db:"customer_id"`
	CustomerName *string    `json:"customerName" db:"customer_name"`
	GuestName    *string    `json:"guestName" db:"guest_name"`
	Status       string     `json:"status" db:"status" enum:"waiting,active,finished,cancelled"`
	QueuePos     *int       `json:"queuePosition" db:"queue_pos"`
	QueuedAt     time.Time  `json:"queuedAt" db:"queued_at"`
	StartedAt    *time.Time `json:"startedAt" db:"started_at"`
	EndedAt      *time.Time `json:"endedAt" db:"ended_at"`
	Balls        int        `json:"balls" db:"balls"`
}

const sessionSelect = `SELECT s.id, s.number, s.bay_id, b.code AS bay_code, s.area, s.customer_id, s.guest_name AS customer_name, s.guest_name, s.status,
	CASE WHEN s.status = 'waiting' THEN (SELECT count(*) FROM golf.range_sessions w WHERE w.property_id = s.property_id AND w.status = 'waiting'
	  AND w.area = s.area AND w.queued_at <= s.queued_at)::int END AS queue_pos,
	s.queued_at, s.started_at, s.ended_at, coalesce((SELECT sum(balls) FROM golf.range_buckets k WHERE k.session_id = s.id), 0)::int AS balls
	FROM golf.range_sessions s LEFT JOIN golf.range_bays b ON b.id = s.bay_id`

func (m *Module) rangeSession(ctx context.Context, q dbtx.Querier, sid uuid.UUID) (RangeSession, error) {
	rows, err := q.Query(ctx, sessionSelect+` WHERE s.id = $1`, sid)
	return handle.One[RangeSession](rows, err, "range session")
}

type RangeSessionInput struct {
	CustomerID *uuid.UUID `json:"customerId,omitempty"`
	GuestName  string     `json:"guestName,omitempty"`
	Area       string     `json:"area,omitempty" enum:"indoor,outdoor"`
	BayID      *uuid.UUID `json:"bayId,omitempty" doc:"Assign this bay now; empty: first free bay or the queue"`
}

// StartSession checks a walk-in in: a free bay is assigned, otherwise the
// guest waits in the queue (FR-RNG-02).
func (m *Module) StartSession(ctx context.Context, tx pgx.Tx, property uuid.UUID, in RangeSessionInput) (RangeSession, error) {
	if in.CustomerID == nil && in.GuestName == "" {
		return RangeSession{}, handle.Invalid("guestName", "required", "customer or guest name is required")
	}
	if in.Area == "" {
		in.Area = "outdoor"
	}
	no, err := number(ctx, tx, property, "RNG")
	if err != nil {
		return RangeSession{}, err
	}
	if in.CustomerID != nil && in.GuestName == "" {
		c, err := crm.GetCustomer(ctx, tx, *in.CustomerID)
		if err != nil {
			return RangeSession{}, err
		}
		in.GuestName = c.Name // name snapshot for the queue screen
	}
	sid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO golf.range_sessions (id, property_id, number, area, customer_id, guest_name, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		sid, property, no, in.Area, in.CustomerID, nullStr(in.GuestName), actorPtr(ctx)); err != nil {
		return RangeSession{}, err
	}
	if err := m.tryAssignBay(ctx, tx, property, sid, in.Area, in.BayID); err != nil {
		return RangeSession{}, err
	}
	s, err := m.rangeSession(ctx, tx, sid)
	if err != nil {
		return s, err
	}
	return s, record(ctx, tx, "golf.range_session", sid, no, audit.ActionCreate, property, nil, s, "")
}

func (m *Module) tryAssignBay(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID, area string, bay *uuid.UUID) error {
	var bid uuid.UUID
	var readiness string
	var err error
	if bay != nil {
		err = tx.QueryRow(ctx, `SELECT id, readiness FROM golf.range_bays WHERE id = $1 AND property_id = $2 AND status = 'active' FOR UPDATE`, *bay, property).Scan(&bid, &readiness)
		if dbtx.IsNoRows(err) {
			return errs.NotFound("bay")
		}
		if err == nil && readiness != "available" {
			return errs.Conflict("bay_unavailable", "bay is "+readiness)
		}
	} else {
		// A bay booked through the Reservation Engine right now is not free.
		bid, readiness, err = m.freeBay(ctx, tx, property, area)
		if err == nil && bid == uuid.Nil {
			return nil // stays in the queue
		}
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.range_bays SET readiness = 'occupied' WHERE id = $1`, bid); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.range_sessions SET bay_id = $2, status = 'active', started_at = now() WHERE id = $1`, sid, bid); err != nil {
		return err
	}
	return m.live(ctx, tx, "golf.range", property, "bay", bid.String(), map[string]any{"readiness": "occupied", "sessionId": sid})
}

type AssignBayInput struct {
	BayID *uuid.UUID `json:"bayId,omitempty"`
}

// AssignBay moves a waiting guest to a bay.
func (m *Module) AssignBay(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID, in AssignBayInput) (RangeSession, error) {
	s, err := m.rangeSession(ctx, tx, sid)
	if err != nil {
		return s, err
	}
	if s.Status != "waiting" {
		return s, errs.Conflict("invalid_status", "session is "+s.Status)
	}
	if err := m.tryAssignBay(ctx, tx, property, sid, s.Area, in.BayID); err != nil {
		return s, err
	}
	after, err := m.rangeSession(ctx, tx, sid)
	if err != nil {
		return after, err
	}
	if after.Status != "active" {
		return after, errs.Conflict("no_bay_free", "no bay is free in the "+s.Area+" area")
	}
	return after, record(ctx, tx, "golf.range_session", sid, s.Number, "assign_bay", property, s, after, "")
}

// EndSession frees the bay and moves the next waiting guest in.
func (m *Module) EndSession(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID, cancel bool) (RangeSession, error) {
	s, err := m.rangeSession(ctx, tx, sid)
	if err != nil {
		return s, err
	}
	if s.Status == "finished" || s.Status == "cancelled" {
		return s, errs.Conflict("invalid_status", "session is "+s.Status)
	}
	st := "finished"
	if cancel {
		st = "cancelled"
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.range_sessions SET status = $2, ended_at = now() WHERE id = $1`, sid, st); err != nil {
		return s, err
	}
	if s.BayID != nil {
		if _, err := tx.Exec(ctx, `UPDATE golf.range_bays SET readiness = 'available' WHERE id = $1 AND readiness = 'occupied'`, *s.BayID); err != nil {
			return s, err
		}
		if err := m.live(ctx, tx, "golf.range", property, "bay", s.BayID.String(), map[string]any{"readiness": "available"}); err != nil {
			return s, err
		}
		var next uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM golf.range_sessions WHERE property_id = $1 AND status = 'waiting' AND area = $2 ORDER BY queued_at LIMIT 1 FOR UPDATE`,
			property, s.Area).Scan(&next)
		if err == nil {
			if err := m.tryAssignBay(ctx, tx, property, next, s.Area, s.BayID); err != nil {
				return s, err
			}
		} else if !dbtx.IsNoRows(err) {
			return s, err
		}
	}
	after, err := m.rangeSession(ctx, tx, sid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.range_session", sid, s.Number, audit.ActionStatusChange, property, s, after, "")
}

// ── buckets & dispenser (FR-RNG-03/04/05) ─────────────────────────────────

type BucketInput struct {
	SessionID  *uuid.UUID `json:"sessionId,omitempty"`
	CustomerID *uuid.UUID `json:"customerId,omitempty"`
	Balls      int        `json:"balls" doc:"Bucket size (configurable, e.g. 50)"`
	Source     string     `json:"source" enum:"sale,prepaid,complimentary"`
	OrderID    *uuid.UUID `json:"orderId,omitempty" doc:"Sale: the paid POS order of the Driving Range Counter"`
	Dispenser  string     `json:"dispenser,omitempty" doc:"Bridge device name; default any ball dispenser"`
	Reason     string     `json:"reason,omitempty" doc:"Complimentary: reason"`
}

// Bucket is an issued bucket of balls.
type Bucket struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	SessionID     *uuid.UUID `json:"sessionId" db:"session_id"`
	CustomerID    *uuid.UUID `json:"customerId" db:"customer_id"`
	Balls         int        `json:"balls" db:"balls"`
	Source        string     `json:"source" db:"source"`
	OrderID       *uuid.UUID `json:"orderId" db:"order_id"`
	VoucherID     *uuid.UUID `json:"voucherId" db:"voucher_id"`
	Amount        string     `json:"amount" db:"amount"`
	DispenserCode string     `json:"dispenserCode" db:"dispenser_code" doc:"Code to key in at the dispenser (manual fallback)"`
	DispenseMode  string     `json:"dispenseMode" db:"dispense_mode" enum:"bridge,manual"`
	CreatedAt     time.Time  `json:"createdAt" db:"created_at"`
	Remaining     *string    `json:"remainingBalance,omitempty" db:"-" doc:"Prepaid: remaining balls after the redemption"`
}

const bucketSelect = `SELECT id, session_id, customer_id, balls, source, order_id, voucher_id, trim_scale(amount)::text AS amount, dispenser_code, dispense_mode,
	created_at FROM golf.range_buckets`

func dispenserCode() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1000000))
	return fmt.Sprintf("%06d", n.Int64())
}

// IssueBucket issues balls: a POS sale, a prepaid balance redemption (the
// balance and its expiry live in Voucher & Prepaid) or complimentary.
func (m *Module) IssueBucket(ctx context.Context, tx pgx.Tx, property uuid.UUID, in BucketInput, key string) (Bucket, error) {
	if key != "" {
		b, err := handle.Get[Bucket](tx.Query(ctx, bucketSelect+` WHERE property_id = $1 AND idempotency_key = $2`, property, key))
		if err == nil {
			return b, nil
		}
		if !errs.Is(err, errs.KindNotFound) {
			return b, err
		}
	}
	if in.Balls <= 0 {
		return Bucket{}, handle.Invalid("balls", "invalid", "balls must be positive")
	}
	if in.SessionID != nil && in.CustomerID == nil {
		_ = tx.QueryRow(ctx, `SELECT customer_id FROM golf.range_sessions WHERE id = $1 AND property_id = $2`, *in.SessionID, property).Scan(&in.CustomerID)
	}
	bid := id.New()
	amount := decimal.Zero
	var voucherID *uuid.UUID
	var remaining *string
	switch in.Source {
	case "sale":
		if in.OrderID == nil {
			return Bucket{}, handle.Invalid("orderId", "required", "the paid POS order is required")
		}
		o, err := m.POS.Order(ctx, tx, *in.OrderID)
		if err != nil {
			return Bucket{}, err
		}
		if o.Status != "paid" && o.Status != "charged" {
			return Bucket{}, errs.Conflict("order_not_paid", "the POS order is not paid")
		}
		var used int
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(balls), 0)::int FROM golf.range_buckets WHERE order_id = $1`, *in.OrderID).Scan(&used); err != nil {
			return Bucket{}, err
		}
		if used > 0 {
			return Bucket{}, errs.Conflict("order_used", "balls of this order have already been issued")
		}
		amount, _ = decimal.NewFromString(o.Total)
	case "prepaid":
		res, err := m.Vouchers.RedeemBalance(ctx, tx, commercial.RedeemRequest{PropertyID: property, CustomerID: in.CustomerID,
			Quantity: decimal.NewFromInt(int64(in.Balls)), ServiceType: "driving_range", SourceType: "golf.range_bucket", SourceID: &bid,
			IdempotencyKey: key, Reference: "range bucket"}, BallCategory)
		if err != nil {
			return Bucket{}, err
		}
		for _, r := range res {
			v := r.Voucher.ID
			voucherID = &v
			rec, _ := decimal.NewFromString(r.Recognized)
			amount = amount.Add(rec)
			rem := r.Voucher.RemainingQuantity
			remaining = &rem
		}
	case "complimentary":
		if err := handle.Required("reason", in.Reason); err != nil {
			return Bucket{}, err
		}
	default:
		return Bucket{}, handle.Invalid("source", "invalid", "sale, prepaid or complimentary")
	}
	device := in.Dispenser
	if device == "" {
		device = "ball_dispenser"
	}
	code := dispenserCode()
	mode := "manual"
	cmd, err := integration.Enqueue(ctx, tx, integration.CommandRequest{PropertyID: property, Device: device, Command: "dispense",
		Payload: map[string]any{"balls": in.Balls, "code": code}, SourceType: "golf.range_bucket", SourceID: &bid})
	if err != nil {
		return Bucket{}, err
	}
	if cmd != nil {
		mode = "bridge"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.range_buckets (id, property_id, session_id, customer_id, balls, source, order_id, voucher_id, amount,
		dispenser_code, dispense_mode, idempotency_key, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10,$11,$12,$13)`, bid, property, in.SessionID,
		in.CustomerID, in.Balls, in.Source, in.OrderID, voucherID, amount.String(), code, mode, nullStr(key), actorPtr(ctx)); err != nil {
		return Bucket{}, err
	}
	b, err := handle.Get[Bucket](tx.Query(ctx, bucketSelect+` WHERE id = $1`, bid))
	if err != nil {
		return b, err
	}
	b.Remaining = remaining
	return b, record(ctx, tx, "golf.range_bucket", bid, fmt.Sprintf("%d balls", in.Balls), audit.ActionCreate, property, nil, b, in.Reason)
}

// RangeUsage is the Driving Range usage of a period (FR-RNG-06).
type RangeUsage struct {
	From           time.Time      `json:"from"`
	To             time.Time      `json:"to"`
	Sessions       int            `json:"sessions"`
	Buckets        int            `json:"buckets"`
	Balls          int            `json:"balls"`
	BallsBySource  map[string]int `json:"ballsBySource"`
	Revenue        string         `json:"revenue" doc:"Bucket sales + recognised prepaid revenue"`
	BayHoursUsed   string         `json:"bayHoursUsed"`
	BayHoursOpen   string         `json:"bayHoursAvailable"`
	BayUtilization string         `json:"bayUtilization"`
}

func (m *Module) RangeUsage(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time) (RangeUsage, error) {
	u := RangeUsage{From: from, To: to, BallsBySource: map[string]int{}}
	var used float64
	var bays int
	if err := q.QueryRow(ctx, `SELECT (SELECT count(*) FROM golf.range_sessions WHERE property_id = $1 AND queued_at >= $2 AND queued_at < $3)::int,
		(SELECT count(*) FROM golf.range_buckets WHERE property_id = $1 AND created_at >= $2 AND created_at < $3)::int,
		(SELECT coalesce(sum(balls), 0) FROM golf.range_buckets WHERE property_id = $1 AND created_at >= $2 AND created_at < $3)::int,
		(SELECT trim_scale(coalesce(sum(amount), 0))::text FROM golf.range_buckets WHERE property_id = $1 AND created_at >= $2 AND created_at < $3),
		(SELECT coalesce(sum(extract(epoch FROM least(coalesce(ended_at, now()), $3) - greatest(started_at, $2)) / 3600), 0)::float8 FROM golf.range_sessions
		  WHERE property_id = $1 AND started_at IS NOT NULL AND started_at < $3 AND coalesce(ended_at, now()) > $2),
		(SELECT count(*) FROM golf.range_bays WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL)::int`, property, from, to).
		Scan(&u.Sessions, &u.Buckets, &u.Balls, &u.Revenue, &used, &bays); err != nil {
		return u, err
	}
	rows, err := q.Query(ctx, `SELECT source, sum(balls)::int FROM golf.range_buckets WHERE property_id = $1 AND created_at >= $2 AND created_at < $3 GROUP BY source`, property, from, to)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return u, err
		}
		u.BallsBySource[s] = n
	}
	// Opening hours of the driving_range_bay resource type.
	var openH float64
	openH = 16
	if rt, err := m.Reservations.Type(ctx, q, "driving_range_bay"); err == nil {
		o, err1 := time.Parse("15:04", rt.OpenTime[:min(5, len(rt.OpenTime))])
		c, err2 := time.Parse("15:04", rt.CloseTime[:min(5, len(rt.CloseTime))])
		if err1 == nil && err2 == nil && c.After(o) {
			openH = c.Sub(o).Hours()
		}
	}
	days := to.Sub(from).Hours() / 24
	avail := openH * days * float64(bays)
	u.BayHoursUsed = decimal.NewFromFloat(used).Round(2).String()
	u.BayHoursOpen = decimal.NewFromFloat(avail).Round(2).String()
	if avail > 0 {
		u.BayUtilization = decimal.NewFromFloat(used / avail).Round(4).String()
	} else {
		u.BayUtilization = "0"
	}
	return u, rows.Err()
}

// freeBay picks the first available bay of an area that is not booked
// through the Reservation Engine right now (reservation.Busy).
func (m *Module) freeBay(ctx context.Context, tx pgx.Tx, property uuid.UUID, area string) (uuid.UUID, string, error) {
	type bay struct {
		ID         uuid.UUID  `db:"id"`
		ResourceID *uuid.UUID `db:"resource_id"`
	}
	bays, err := handle.List[bay](tx.Query(ctx, `SELECT id, resource_id FROM golf.range_bays WHERE property_id = $1 AND area = $2 AND status = 'active'
		AND archived_at IS NULL AND readiness = 'available' ORDER BY tier, code FOR UPDATE SKIP LOCKED`, property, area))
	if err != nil {
		return uuid.Nil, "", err
	}
	var res []uuid.UUID
	for _, b := range bays {
		if b.ResourceID != nil {
			res = append(res, *b.ResourceID)
		}
	}
	now := clock.Now()
	busy, err := reservation.Busy(ctx, tx, res, now, now.Add(time.Minute))
	if err != nil {
		return uuid.Nil, "", err
	}
	for _, b := range bays {
		if b.ResourceID == nil || !busy[*b.ResourceID] {
			return b.ID, "available", nil
		}
	}
	return uuid.Nil, "", nil
}
