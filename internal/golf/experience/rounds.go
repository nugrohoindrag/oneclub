package experience

// Round tracking of PRD P2 (EP-06 Caddy Tablet, EP-09 Pace of Play) on P1's
// flights. Tee-off and Round Finish stay P1's starter actions; P2 reacts to
// P1's golf.flight_teed_off / golf.round_finished events (contract C7) and
// keeps its round data (progress, pace, fee shares) in P2 tables.

import (
	"context"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/crm"
	"oneclub/internal/golf"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/outbox"
)

const (
	defaultHoleTarget = 15 // minutes per hole without a pace target
	defaultTolerance  = 10 // minutes behind before a flight is slow
)

// RoundPlayer is a booking player of the round.
type RoundPlayer struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	CustomerID    *uuid.UUID `json:"customerId" db:"customer_id"`
	Name          string     `json:"name" db:"name"`
	PlayerType    string     `json:"playerType" db:"player_type" enum:"member,guest_of_member,reciprocal,non_member"`
	Status        string     `json:"status" db:"status" enum:"booked,checked_in,no_show,cancelled,removed"`
	HandicapIndex *string    `json:"handicapIndex" db:"handicap_index"`
	ScorecardID   *uuid.UUID `json:"scorecardId" db:"scorecard_id"`
}

// Round is a P1 flight seen as a round: players, caddies, golf carts and
// the P2 hole progress.
type Round struct {
	FlightID      uuid.UUID         `json:"flightId" db:"id"`
	PropertyID    uuid.UUID         `json:"propertyId" db:"property_id"`
	FlightNo      int               `json:"flightNo" db:"flight_no"`
	BookingID     *uuid.UUID        `json:"bookingId" db:"booking_id"`
	BookingCode   *string           `json:"bookingCode" db:"booking_code"`
	FolioID       *uuid.UUID        `json:"folioId" db:"folio_id"`
	CourseID      uuid.UUID         `json:"courseId" db:"course_id"`
	RouteID       *uuid.UUID        `json:"playingRouteId" db:"route_id"`
	RouteName     *string           `json:"playingRouteName" db:"route_name"`
	Tolerance     int               `json:"toleranceMinutes" db:"tolerance_minutes"`
	PlayDate      time.Time         `json:"playDate" db:"play_date"`
	TeeTime       time.Time         `json:"teeTime" db:"tee_time"`
	Status        string            `json:"status" db:"status" enum:"confirmed,checked_in,ready,on_hold,in_play,completed,cancelled"`
	CurrentSeq    int               `json:"currentSeq" db:"current_seq"`
	Holes         int               `json:"holes" db:"holes"`
	TeeOffAt      *time.Time        `json:"teeOffAt" db:"tee_off_at"`
	FinishedAt    *time.Time        `json:"roundFinishAt" db:"round_finish_at"`
	PaceStatus    string            `json:"paceStatus" db:"pace_status" enum:"on_pace,slow,fast"`
	BehindMinutes int               `json:"behindMinutes" db:"behind_minutes"`
	DeviceID      *string           `json:"deviceId" db:"tablet_device"`
	Players       []RoundPlayer     `json:"players" db:"-"`
	Caddies       []CaddyAssignment `json:"caddies" db:"-"`
	GolfCarts     []CartAssignment  `json:"golfCarts" db:"-"`
}

// Label is the booking code, or the flight number of a walk-in flight.
func (r Round) Label() string {
	if r.BookingCode != nil {
		return *r.BookingCode
	}
	return "Flight " + strconv.Itoa(r.FlightNo)
}

// roundSelect reads the P1 flight with the P2 round progress. The playing
// route is the booking's, else the tee time's, else the course default.
const roundSelect = `SELECT f.id, f.property_id, f.flight_no, f.booking_id, b.code AS booking_code, b.folio_id, f.course_id, r.id AS route_id,
	r.name AS route_name, coalesce(pt.tolerance_minutes, 10) AS tolerance_minutes, f.play_date, tt.start_at AS tee_time, f.status,
	coalesce(rp.current_seq, 0) AS current_seq, coalesce(r.hole_count, 0) AS holes, f.tee_off_at, f.round_finish_at,
	coalesce(rp.pace_status, 'on_pace') AS pace_status, coalesce(rp.behind_minutes, 0) AS behind_minutes, rp.tablet_device
	FROM golf.flights f JOIN golf.tee_times tt ON tt.id = f.tee_time_id LEFT JOIN golf.bookings b ON b.id = f.booking_id
	LEFT JOIN golf.playing_routes r ON r.id = coalesce(b.playing_route_id, tt.playing_route_id,
	  (SELECT d.id FROM golf.playing_routes d WHERE d.course_id = f.course_id AND d.is_default AND d.status = 'active' AND d.archived_at IS NULL LIMIT 1))
	LEFT JOIN golf.route_pace_tolerances pt ON pt.playing_route_id = r.id
	LEFT JOIN golf.round_progress rp ON rp.flight_id = f.id`

// GetRound loads the round of a flight.
func (m *Module) GetRound(ctx context.Context, q dbtx.Querier, fid uuid.UUID) (Round, error) {
	rows, err := q.Query(ctx, roundSelect+` WHERE f.id = $1`, fid)
	r, err := handle.One[Round](rows, err, "flight")
	if err != nil {
		return r, err
	}
	if r.Players, err = handle.List[RoundPlayer](q.Query(ctx, `SELECT p.id, p.customer_id, p.name, p.player_type, p.status,
		trim_scale(p.handicap_index)::text AS handicap_index, s.id AS scorecard_id FROM golf.booking_players p
		LEFT JOIN golf.scorecards s ON s.booking_player_id = p.id WHERE p.flight_id = $1 AND p.status NOT IN ('cancelled', 'removed') ORDER BY p.seq`, fid)); err != nil {
		return r, err
	}
	if r.Caddies, err = golf.ListCaddyAssignments(ctx, q, location(ctx, q, r.PropertyID), "a.flight_id = $1", fid); err != nil {
		return r, err
	}
	r.GolfCarts, err = golf.ListCartAssignments(ctx, q, "a.flight_id = $1", fid)
	return r, err
}

func (m *Module) lockRound(ctx context.Context, tx pgx.Tx, fid uuid.UUID) (Round, error) {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM golf.flights WHERE id = $1 FOR UPDATE`, fid); err != nil {
		return Round{}, err
	}
	return m.GetRound(ctx, tx, fid)
}

func (m *Module) roundHoles(ctx context.Context, q dbtx.Querier, r Round) ([]RouteHole, error) {
	if r.RouteID == nil {
		return []RouteHole{}, nil
	}
	rh, err := golf.LoadRouteHoles(ctx, q, *r.RouteID)
	return rh.Holes, err
}

// holeTargets returns the pace target per hole (P2 hole_pace_targets).
func holeTargets(ctx context.Context, q dbtx.Querier, holes []RouteHole) (map[uuid.UUID]int, error) {
	out := map[uuid.UUID]int{}
	ids := make([]uuid.UUID, 0, len(holes))
	for _, h := range holes {
		out[h.HoleID] = defaultHoleTarget
		ids = append(ids, h.HoleID)
	}
	rows, err := q.Query(ctx, `SELECT hole_id, target_minutes FROM golf.hole_pace_targets WHERE hole_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var h uuid.UUID
		var t int
		if err := rows.Scan(&h, &t); err != nil {
			return nil, err
		}
		out[h] = t
	}
	return out, rows.Err()
}

// RoundEvent is a tablet round event (the device time comes with offline sync).
type RoundEvent struct {
	At          *time.Time `json:"at,omitempty" doc:"Device time of the event (offline queue); default now"`
	DeviceID    string     `json:"deviceId,omitempty"`
	HolesPlayed *int       `json:"holesPlayed,omitempty" doc:"Complete: holes actually played (default the last hole reached)"`
}

// StartRound tees the flight off through P1's starter (FR-PLX-01); replays
// of an offline tee-off are idempotent.
func (m *Module) StartRound(ctx context.Context, tx pgx.Tx, fid uuid.UUID, in RoundEvent) (Round, error) {
	r, err := m.lockRound(ctx, tx, fid)
	if err != nil {
		return r, err
	}
	if r.Status != "in_play" {
		if r.Status == "completed" || r.Status == "cancelled" {
			return r, errs.Conflict("round_closed", "the flight is "+r.Status)
		}
		if _, err := m.Golf.Control(ctx, tx, r.PropertyID, fid, "tee-off", golf.StarterAction{}); err != nil {
			return r, err
		}
	}
	if err := m.roundStarted(ctx, tx, r.PropertyID, fid, eventTime(in.At), in.DeviceID); err != nil {
		return r, err
	}
	return m.GetRound(ctx, tx, fid)
}

// OnFlightTeedOff starts the P2 round of a flight teed off by the starter.
func (m *Module) OnFlightTeedOff(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	fid, ok := flightOf(e)
	if !ok || e.PropertyID == nil {
		return nil
	}
	ctx = reqctx.WithProperty(dbtx.System(ctx), *e.PropertyID)
	return m.roundStarted(ctx, tx, *e.PropertyID, fid, e.OccurredAt, "")
}

func flightOf(e outbox.Event) (uuid.UUID, bool) {
	var p struct {
		FlightID uuid.UUID `json:"flightId"`
	}
	if err := e.Decode(&p); err != nil || p.FlightID == uuid.Nil {
		return uuid.Nil, false
	}
	return p.FlightID, true
}

// roundStarted opens the first hole and the digital scorecards of the
// checked-in players. Idempotent (the starter event and the tablet both
// call it).
func (m *Module) roundStarted(ctx context.Context, tx pgx.Tx, property, fid uuid.UUID, at time.Time, device string) error {
	r, err := m.GetRound(ctx, tx, fid)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.round_progress (flight_id, property_id, current_seq, last_hole_at, tablet_device) VALUES ($1,$2,1,$3,$4)
		ON CONFLICT (flight_id) DO UPDATE SET tablet_device = coalesce(EXCLUDED.tablet_device, golf.round_progress.tablet_device)`,
		fid, property, at, nullStr(device)); err != nil {
		return err
	}
	holes, err := m.roundHoles(ctx, tx, r)
	if err != nil || len(holes) == 0 {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.hole_progress (id, property_id, flight_id, seq, hole_id, started_at, device_id, source)
		VALUES ($1,$2,$3,1,$4,$5,$6,'staff') ON CONFLICT (flight_id, seq) DO NOTHING`, id.New(), property, fid, holes[0].HoleID, at, nullStr(device)); err != nil {
		return err
	}
	for _, p := range r.Players {
		if p.Status == "checked_in" && p.ScorecardID == nil {
			if err := m.openScorecard(ctx, tx, r, p, holes, at); err != nil {
				return err
			}
		}
	}
	return m.live(ctx, tx, "golf.pace", property, "teed_off", fid.String(), map[string]any{"seq": 1})
}

type HoleInput struct {
	Seq      int        `json:"seq" doc:"Hole sequence on the playing route (1 … 18) now being played"`
	At       *time.Time `json:"at,omitempty"`
	DeviceID string     `json:"deviceId,omitempty"`
}

// RecordHole records hole progress and the pace of play (FR-PLX-03/04);
// replays (offline sync) are idempotent per hole.
func (m *Module) RecordHole(ctx context.Context, tx pgx.Tx, fid uuid.UUID, in HoleInput) (Round, error) {
	r, err := m.lockRound(ctx, tx, fid)
	if err != nil {
		return r, err
	}
	if r.Status != "in_play" {
		return r, errs.Conflict("not_in_play", "flight is "+r.Status)
	}
	holes, err := m.roundHoles(ctx, tx, r)
	if err != nil {
		return r, err
	}
	if in.Seq < 1 || in.Seq > len(holes) {
		return r, handle.Invalid("seq", "invalid_seq", "hole sequence out of range")
	}
	at := eventTime(in.At)
	tag, err := tx.Exec(ctx, `INSERT INTO golf.hole_progress (id, property_id, flight_id, seq, hole_id, started_at, device_id, source)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'tablet') ON CONFLICT (flight_id, seq) DO NOTHING`, id.New(), r.PropertyID, fid, in.Seq, holes[in.Seq-1].HoleID, at, nullStr(in.DeviceID))
	if err != nil {
		return r, err
	}
	if tag.RowsAffected() == 0 {
		return r, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.hole_progress SET finished_at = $3 WHERE flight_id = $1 AND seq < $2 AND finished_at IS NULL`, fid, in.Seq, at); err != nil {
		return r, err
	}
	behind, pace := 0, "on_pace"
	if r.TeeOffAt != nil {
		targets, err := holeTargets(ctx, tx, holes)
		if err != nil {
			return r, err
		}
		target := 0
		for i := 0; i < in.Seq-1; i++ {
			target += targets[holes[i].HoleID]
		}
		behind = int(at.Sub(*r.TeeOffAt).Minutes()) - target
		switch {
		case behind > r.Tolerance:
			pace = "slow"
		case behind < -r.Tolerance:
			pace = "fast"
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.round_progress (flight_id, property_id, current_seq, last_hole_at, behind_minutes, pace_status, tablet_device)
		VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (flight_id) DO UPDATE SET current_seq = greatest(golf.round_progress.current_seq, EXCLUDED.current_seq),
		last_hole_at = EXCLUDED.last_hole_at, behind_minutes = EXCLUDED.behind_minutes, pace_status = EXCLUDED.pace_status,
		tablet_device = coalesce(EXCLUDED.tablet_device, golf.round_progress.tablet_device)`,
		fid, r.PropertyID, in.Seq, at, behind, pace, nullStr(in.DeviceID)); err != nil {
		return r, err
	}
	if err := m.live(ctx, tx, "golf.pace", r.PropertyID, "hole", fid.String(), map[string]any{"seq": in.Seq, "paceStatus": pace, "behindMinutes": behind}); err != nil {
		return r, err
	}
	after, err := m.GetRound(ctx, tx, fid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.flight", fid, r.Label(), "hole_progress", r.PropertyID, map[string]any{"seq": r.CurrentSeq},
		map[string]any{"seq": in.Seq, "at": at}, "")
}

// FinishRound records Round Finish through P1's starter; the P2 part runs
// on P1's golf.round_finished event. Replays are idempotent.
func (m *Module) FinishRound(ctx context.Context, tx pgx.Tx, fid uuid.UUID, in RoundEvent) (Round, error) {
	r, err := m.lockRound(ctx, tx, fid)
	if err != nil {
		return r, err
	}
	if r.Status == "completed" {
		return r, nil
	}
	if r.Status != "in_play" {
		return r, errs.Conflict("not_in_play", "flight is "+r.Status)
	}
	played := in.HolesPlayed
	if played == nil {
		n := r.CurrentSeq
		if n == 0 {
			n = r.Holes
		}
		played = &n
	}
	if _, err := m.Golf.Control(ctx, tx, r.PropertyID, fid, "finish", golf.StarterAction{HolesPlayed: played}); err != nil {
		return r, err
	}
	return m.GetRound(ctx, tx, fid)
}

// OnRoundFinished closes the hole progress, splits the caddy fee of
// replaced caddies, checks golf cart service hours and sends the post-round
// survey. Every step is idempotent (at-least-once delivery).
func (m *Module) OnRoundFinished(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	fid, ok := flightOf(e)
	if !ok || e.PropertyID == nil {
		return nil
	}
	property := *e.PropertyID
	ctx = reqctx.WithProperty(dbtx.System(ctx), property)
	if _, err := tx.Exec(ctx, `UPDATE golf.hole_progress SET finished_at = $2 WHERE flight_id = $1 AND finished_at IS NULL`, fid, e.OccurredAt); err != nil {
		return err
	}
	if err := m.splitCaddyFees(ctx, tx, property, fid); err != nil {
		return err
	}
	carts, err := collectIDs(tx.Query(ctx, `SELECT DISTINCT golf_cart_id FROM golf.golf_cart_assignments WHERE flight_id = $1`, fid))
	if err != nil {
		return err
	}
	for _, c := range carts {
		if err := m.checkService(ctx, tx, property, c); err != nil {
			return err
		}
	}
	if err := m.requestRoundFeedback(ctx, tx, property, fid); err != nil {
		return err
	}
	return m.live(ctx, tx, "golf.pace", property, "finished", fid.String(), nil)
}

// splitCaddyFees shares the caddy fee of a replacement chain by the Caddy
// Policies (FR-CDL-05): by holes served, all to the first or the last
// caddy. P1 holds the full fee on each assignment; the shares are P2's.
func (m *Module) splitCaddyFees(ctx context.Context, tx pgx.Tx, property, fid uuid.UUID) error {
	type asg struct {
		ID         uuid.UUID  `db:"id"`
		Fee        string     `db:"fee_amount"`
		ReplacedBy *uuid.UUID `db:"replaced_by_id"`
		Holes      int        `db:"holes"`
	}
	list, err := handle.List[asg](tx.Query(ctx, `SELECT a.id, a.fee_amount::text AS fee_amount, a.replaced_by_id,
		(SELECT count(*) FROM golf.hole_progress h WHERE h.flight_id = a.flight_id AND h.started_at >= coalesce(a.started_at, a.assigned_at)
		  AND h.started_at < coalesce(a.finished_at, 'infinity'))::int AS holes
		FROM golf.caddy_assignments a WHERE a.flight_id = $1 AND a.status IN ('completed', 'replaced') ORDER BY a.assigned_at`, fid))
	if err != nil {
		return err
	}
	byID := map[uuid.UUID]asg{}
	replacement := map[uuid.UUID]bool{}
	for _, a := range list {
		byID[a.ID] = a
		if a.ReplacedBy != nil {
			replacement[*a.ReplacedBy] = true
		}
	}
	pol, err := m.caddyPolicy(ctx, tx, property)
	if err != nil {
		return err
	}
	for _, head := range list {
		if replacement[head.ID] || head.ReplacedBy == nil {
			continue
		}
		chain := []asg{head}
		for cur := head; cur.ReplacedBy != nil; {
			next, ok := byID[*cur.ReplacedBy]
			if !ok {
				break
			}
			chain = append(chain, next)
			cur = next
		}
		if len(chain) < 2 {
			continue
		}
		base := dec(head.Fee)
		shares := make([]decimal.Decimal, len(chain))
		total := 0
		for _, a := range chain {
			total += a.Holes
		}
		switch {
		case pol.ReplacementSplit == "first_caddy":
			shares[0] = base
		case pol.ReplacementSplit == "last_caddy" || total == 0:
			shares[len(chain)-1] = base
		default:
			used := decimal.Zero
			for i, a := range chain {
				shares[i] = base.Mul(decimal.NewFromInt(int64(a.Holes))).Div(decimal.NewFromInt(int64(total))).Round(2)
				if i == len(chain)-1 {
					shares[i] = base.Sub(used)
				}
				used = used.Add(shares[i])
			}
		}
		for i, a := range chain {
			if _, err := tx.Exec(ctx, `INSERT INTO golf.caddy_fee_shares (assignment_id, property_id, holes, share_amount) VALUES ($1,$2,$3,$4::numeric)
				ON CONFLICT (assignment_id) DO UPDATE SET holes = EXCLUDED.holes, share_amount = EXCLUDED.share_amount`,
				a.ID, property, a.Holes, shares[i].String()); err != nil {
				return err
			}
		}
	}
	return nil
}

// cartServiceHours is the usage of a golf cart since its last service: the
// out → return time of P1's golf cart assignments.
const cartServiceHours = `coalesce((SELECT sum(extract(epoch FROM coalesce(a.returned_at, now()) - a.out_at)) / 3600 FROM golf.golf_cart_assignments a
	WHERE a.golf_cart_id = g.id AND a.out_at IS NOT NULL AND a.out_at > coalesce(cp.last_service_at, '-infinity')), 0)`

// checkService alerts the Golf Manager once when a golf cart passes its
// service threshold (FR-CTL-04).
func (m *Module) checkService(ctx context.Context, tx pgx.Tx, property, cart uuid.UUID) error {
	pol, err := m.cartPolicy(ctx, tx, property)
	if err != nil {
		return err
	}
	var code string
	var hours float64
	var threshold *string
	var alerted *time.Time
	if err := tx.QueryRow(ctx, `SELECT g.code, `+cartServiceHours+`::float8, cp.service_threshold_hours::text, cp.service_alerted_at
		FROM golf.golf_carts g LEFT JOIN golf.cart_profiles cp ON cp.golf_cart_id = g.id WHERE g.id = $1`, cart).Scan(&code, &hours, &threshold, &alerted); err != nil {
		return err
	}
	limit := dec(pol.DefaultServiceHours)
	if threshold != nil {
		limit = dec(*threshold)
	}
	since := decimal.NewFromFloat(hours).Round(2)
	if alerted != nil || !limit.IsPositive() || since.LessThan(limit) {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.cart_profiles (golf_cart_id, property_id, service_alerted_at) VALUES ($1,$2,now())
		ON CONFLICT (golf_cart_id) DO UPDATE SET service_alerted_at = now()`, cart, property); err != nil {
		return err
	}
	if m.Notify != nil {
		users, err := notify.Holders(ctx, tx, property, "golf.cart_maintenance.manage")
		if err != nil {
			return err
		}
		if len(users) > 0 {
			if err := m.Notify.Send(ctx, tx, notify.Message{Event: "golf.cart_service_due", Category: "golf", UserIDs: users, PropertyID: &property,
				Link: "/golf/golf-carts/" + cart.String(), Data: map[string]any{"cart": code, "hours": since.String(), "threshold": limit.String()}}); err != nil {
				return err
			}
		}
	}
	return m.publish(ctx, tx, "golf.cart_service_due", "golf.golf_cart", cart, property, map[string]any{"golfCartId": cart, "hoursSinceService": since.String()})
}

// requestRoundFeedback sends the post-round survey; the caddy of the player
// is rated in the same survey (FR-CDL-06). CRM dedupes per player.
func (m *Module) requestRoundFeedback(ctx context.Context, tx pgx.Tx, property, fid uuid.UUID) error {
	if m.CRM == nil {
		return nil
	}
	r, err := m.GetRound(ctx, tx, fid)
	if err != nil {
		return err
	}
	for _, p := range r.Players {
		if p.CustomerID == nil || p.Status != "checked_in" {
			continue
		}
		req := crm.FeedbackRequest{PropertyID: property, CustomerID: *p.CustomerID, ContextType: "round", ContextID: &p.ID,
			ContextLabel: "Round " + r.Label() + " · " + deref(r.RouteName)}
		for _, a := range r.Caddies {
			if a.Status != "completed" {
				continue
			}
			for _, pid := range a.PlayerIDs {
				if pid == p.ID {
					aid := a.ID
					req.SubjectType, req.SubjectID = "golf.caddy_assignment", &aid
				}
			}
		}
		if _, err := m.CRM.RequestFeedback(ctx, tx, req); err != nil {
			return err
		}
	}
	return nil
}

// CartReplaceInput replaces the golf cart of a flight mid-round.
type CartReplaceInput struct {
	GolfCartID *uuid.UUID `json:"golfCartId,omitempty" doc:"Replacement golf cart; empty: the first Ready cart"`
	Reason     string     `json:"reason"`
}

// ReplaceCart swaps the golf cart mid-round (FR-CTL-05) through P1's
// return and assign; the replacement goes out at once when the flight is in
// play (contract golf.StartCartAssignment).
func (m *Module) ReplaceCart(ctx context.Context, tx pgx.Tx, property, aid uuid.UUID, in CartReplaceInput) (Round, error) {
	if err := handle.Required("reason", in.Reason); err != nil {
		return Round{}, err
	}
	var flight, cart uuid.UUID
	var status string
	if err := tx.QueryRow(ctx, `SELECT flight_id, golf_cart_id, status FROM golf.golf_cart_assignments WHERE id = $1 AND property_id = $2`, aid, property).
		Scan(&flight, &cart, &status); err != nil {
		if dbtx.IsNoRows(err) {
			return Round{}, errs.NotFound("golf cart assignment")
		}
		return Round{}, err
	}
	if _, err := m.Golf.ReturnCart(ctx, tx, property, aid); err != nil {
		return Round{}, err
	}
	next := in.GolfCartID
	if next == nil {
		var c uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM golf.golf_carts WHERE property_id = $1 AND status = 'active' AND readiness = 'ready' AND archived_at IS NULL
			AND id <> $2 ORDER BY readiness_changed_at, code LIMIT 1`, property, cart).Scan(&c); err != nil {
			if dbtx.IsNoRows(err) {
				return Round{}, errs.Conflict("no_cart_ready", "no golf cart is Ready")
			}
			return Round{}, err
		}
		next = &c
	}
	out, err := m.Golf.AssignCarts(ctx, tx, property, golf.CartAssignRequest{FlightID: flight, CartIDs: []uuid.UUID{*next}})
	if err != nil {
		return Round{}, err
	}
	if status == "in_use" {
		if err := m.Golf.StartCartAssignment(ctx, tx, property, out[0].ID); err != nil {
			return Round{}, err
		}
	}
	after, err := m.GetRound(ctx, tx, flight)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.golf_cart_assignment", aid, after.Label(), "replace", property, map[string]any{"golfCartId": cart},
		map[string]any{"golfCartId": next, "assignmentId": out[0].ID}, in.Reason)
}
