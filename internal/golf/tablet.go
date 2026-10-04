package golf

// Pace of Play (PRD P2 FR-PLX-04) and the Caddy Tablet API (EP-06):
// assignments, round information with customer context, round progress and
// scores (online or through the offline sync queue), on-course orders,
// preferences, device handover and earnings.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/commercial"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/handle"
	syncsvc "oneclub/internal/platform/sync"
)

// PaceFlight is one flight on the Starter / Marshal pace screen.
type PaceFlight struct {
	FlightID       uuid.UUID  `json:"flightId"`
	FlightNo       string     `json:"flightNo"`
	RouteName      string     `json:"routeName"`
	TeeTime        time.Time  `json:"teeTime"`
	TeedOffAt      time.Time  `json:"teedOffAt"`
	CurrentSeq     int        `json:"currentSeq"`
	Holes          int        `json:"holes"`
	Hole           string     `json:"hole"`
	ElapsedMinutes int        `json:"elapsedMinutes"`
	TargetMinutes  int        `json:"targetMinutes" doc:"Cumulative target through the current hole"`
	BehindMinutes  int        `json:"behindMinutes" doc:"Positive: behind the target"`
	Slow           bool       `json:"slow" doc:"Behind more than the route tolerance"`
	AheadFlightNo  *string    `json:"aheadFlightNo"`
	GapHoles       *int       `json:"gapHoles" doc:"Holes between this flight and the flight ahead"`
	GapMinutes     *int       `json:"gapMinutes" doc:"Minutes since the flight ahead started this hole"`
	Players        []string   `json:"players"`
	Caddies        []string   `json:"caddies"`
	GolfCarts      []string   `json:"golfCarts"`
	HoleStartedAt  *time.Time `json:"holeStartedAt"`
}

// Pace computes pace of play for flights on course.
func (m *Module) Pace(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]PaceFlight, error) {
	type row struct {
		ID        uuid.UUID `db:"id"`
		No        string    `db:"flight_no"`
		RouteID   uuid.UUID `db:"route_id"`
		Route     string    `db:"route_name"`
		Tolerance int       `db:"tolerance_minutes"`
		TeeTime   time.Time `db:"tee_time"`
		TeedOff   time.Time `db:"teed_off_at"`
		Seq       int       `db:"current_seq"`
	}
	flights, err := handle.List[row](q.Query(ctx, `SELECT f.id, f.flight_no, f.route_id, r.name AS route_name, r.tolerance_minutes, f.tee_time, f.teed_off_at, f.current_seq
		FROM golf.flights f JOIN golf.playing_routes r ON r.id = f.route_id WHERE f.property_id = $1 AND f.status = 'on_course' ORDER BY f.teed_off_at`, property))
	if err != nil {
		return nil, err
	}
	now := clock.Now()
	routeHoles := map[uuid.UUID][]RouteHole{}
	out := make([]PaceFlight, 0, len(flights))
	starts := map[uuid.UUID]map[int]time.Time{}
	for _, f := range flights {
		holes, ok := routeHoles[f.RouteID]
		if !ok {
			if holes, err = RouteHoles(ctx, q, f.RouteID, nil); err != nil {
				return nil, err
			}
			routeHoles[f.RouteID] = holes
		}
		st := map[int]time.Time{}
		rows, err := q.Query(ctx, `SELECT seq, started_at FROM golf.hole_progress WHERE flight_id = $1`, f.ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var s int
			var t time.Time
			if err := rows.Scan(&s, &t); err != nil {
				rows.Close()
				return nil, err
			}
			st[s] = t
		}
		rows.Close()
		starts[f.ID] = st
		target := 0
		for i := 0; i < f.Seq && i < len(holes); i++ {
			target += holes[i].TargetMinutes
		}
		elapsed := int(now.Sub(f.TeedOff).Minutes())
		p := PaceFlight{FlightID: f.ID, FlightNo: f.No, RouteName: f.Route, TeeTime: f.TeeTime, TeedOffAt: f.TeedOff, CurrentSeq: f.Seq, Holes: len(holes),
			ElapsedMinutes: elapsed, TargetMinutes: target, BehindMinutes: elapsed - target, Players: []string{}, Caddies: []string{}, GolfCarts: []string{}}
		p.Slow = p.BehindMinutes > f.Tolerance
		if f.Seq >= 1 && f.Seq <= len(holes) {
			p.Hole = holes[f.Seq-1].SectionCode + "-" + itoa(holes[f.Seq-1].Number)
		}
		if t, ok := st[f.Seq]; ok {
			p.HoleStartedAt = &t
		}
		_ = q.QueryRow(ctx, `SELECT coalesce(array_agg(name ORDER BY created_at), '{}') FROM golf.flight_players WHERE flight_id = $1 AND status = 'playing'`, f.ID).Scan(&p.Players)
		_ = q.QueryRow(ctx, `SELECT coalesce(array_agg(c.code ORDER BY a.from_seq), '{}') FROM golf.caddy_assignments a JOIN golf.caddies c ON c.id = a.caddy_id
			WHERE a.flight_id = $1 AND a.status = 'active'`, f.ID).Scan(&p.Caddies)
		_ = q.QueryRow(ctx, `SELECT coalesce(array_agg(g.code), '{}') FROM golf.cart_assignments a JOIN golf.golf_carts g ON g.id = a.cart_id
			WHERE a.flight_id = $1 AND a.status = 'in_use'`, f.ID).Scan(&p.GolfCarts)
		out = append(out, p)
	}
	// Gap to the flight ahead on the same route (teed off earlier).
	for i := range out {
		for j := i - 1; j >= 0; j-- {
			if flights[j].RouteID != flights[i].RouteID {
				continue
			}
			no := out[j].FlightNo
			gh := out[j].CurrentSeq - out[i].CurrentSeq
			out[i].AheadFlightNo, out[i].GapHoles = &no, &gh
			if t, ok := starts[flights[j].ID][out[i].CurrentSeq]; ok && out[i].HoleStartedAt != nil {
				gm := int(out[i].HoleStartedAt.Sub(t).Minutes())
				out[i].GapMinutes = &gm
			}
			break
		}
	}
	return out, nil
}

// CheckPace flags flights that became slow (periodic job, ≤ 1 minute) and
// pushes an alert to the Starter / Marshal screens.
func (m *Module) CheckPace(ctx context.Context, tx pgx.Tx, property uuid.UUID) (int, error) {
	list, err := m.Pace(ctx, tx, property)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range list {
		st := "on_pace"
		if p.Slow {
			st = "slow"
		}
		tag, err := tx.Exec(ctx, `UPDATE golf.flights SET pace_status = $2 WHERE id = $1 AND pace_status <> $2`, p.FlightID, st)
		if err != nil {
			return n, err
		}
		if tag.RowsAffected() > 0 && p.Slow {
			n++
			if err := m.live(ctx, tx, "golf.pace", property, "slow", p.FlightID.String(), map[string]any{"flightNo": p.FlightNo, "behind": p.BehindMinutes,
				"hole": p.Hole}); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

// ── tablet ────────────────────────────────────────────────────────────────

type caddyRow struct {
	ID         uuid.UUID `db:"id"`
	PropertyID uuid.UUID `db:"property_id"`
	Code       string    `db:"code"`
	Name       string    `db:"name"`
	DutyStatus string    `db:"duty_status"`
}

// myCaddy resolves the caddy of the signed-in tablet user.
func (m *Module) myCaddy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (caddyRow, error) {
	rows, err := q.Query(ctx, `SELECT id, property_id, code, name, duty_status FROM golf.caddies WHERE property_id = $1 AND user_id = $2 AND archived_at IS NULL`,
		property, handle.UserID(ctx))
	c, err := handle.One[caddyRow](rows, err, "caddy profile of this user")
	return c, err
}

// MyAssignments is the caddy's My Assignments screen.
type MyAssignments struct {
	CaddyID    uuid.UUID         `json:"caddyId"`
	Code       string            `json:"code"`
	Name       string            `json:"name"`
	DutyStatus string            `json:"dutyStatus"`
	Current    *CaddyAssignment  `json:"current"`
	Next       []CaddyAssignment `json:"next"`
	QueueNo    *int              `json:"queuePosition" doc:"Position in today's rotation when no assignment is waiting"`
}

func (m *Module) MyAssignments(ctx context.Context, q dbtx.Querier, property uuid.UUID) (MyAssignments, error) {
	c, err := m.myCaddy(ctx, q, property)
	if err != nil {
		return MyAssignments{}, err
	}
	out := MyAssignments{CaddyID: c.ID, Code: c.Code, Name: c.Name, DutyStatus: c.DutyStatus, Next: []CaddyAssignment{}}
	list, err := handle.List[CaddyAssignment](q.Query(ctx, caddyAssignmentSelect+` WHERE a.caddy_id = $1 AND a.status IN ('assigned', 'accepted', 'active')
		ORDER BY (a.status = 'active') DESC, f.tee_time`, c.ID))
	if err != nil {
		return out, err
	}
	for i := range list {
		if list[i].Status == "active" && out.Current == nil {
			out.Current = &list[i]
		} else {
			out.Next = append(out.Next, list[i])
		}
	}
	if out.Current == nil && len(out.Next) == 0 {
		qe, err := m.Queue(ctx, q, property, nil)
		if err != nil {
			return out, err
		}
		for _, e := range qe {
			if e.CaddyID == c.ID {
				p := e.Position
				out.QueueNo = &p
			}
		}
	}
	return out, nil
}

// canSeeFlight: staff with golf.flight.view, or a caddy assigned to it.
func (m *Module) canSeeFlight(ctx context.Context, q dbtx.Querier, f Flight) error {
	if can(ctx, "golf.flight.view", f.PropertyID) {
		return nil
	}
	var ok bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.caddy_assignments a JOIN golf.caddies c ON c.id = a.caddy_id
		WHERE a.flight_id = $1 AND c.user_id = $2 AND a.status <> 'cancelled')`, f.ID, handle.UserID(ctx)).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return errs.Forbidden("you are not assigned to this flight")
	}
	return nil
}

// PlayerContext is the customer context shown on the tablet (FR-CTB-04):
// name, preferences, history with this caddy — nothing else personal.
type PlayerContext struct {
	PlayerID        uuid.UUID        `json:"playerId"`
	Name            string           `json:"name"`
	PlayerType      string           `json:"playerType"`
	TeeSet          *string          `json:"teeSet"`
	Handicap        *string          `json:"handicap"`
	Preferences     []crm.Preference `json:"preferences"`
	Highlights      []string         `json:"highlights"`
	RoundsWithMe    int              `json:"roundsWithMe"`
	LastRoundWithMe *time.Time       `json:"lastRoundWithMe"`
	FavoriteOfMine  bool             `json:"favoriteCaddyIsMe"`
	ScorecardID     *uuid.UUID       `json:"scorecardId"`
}

// RoundInfo is everything the tablet needs for a round (cached offline).
type RoundInfo struct {
	Flight     Flight          `json:"flight"`
	Holes      []RouteHole     `json:"holes"`
	Players    []PlayerContext `json:"players"`
	Scorecards []Scorecard     `json:"scorecards"`
	Times      RoundTimes      `json:"times"`
	ServerTime time.Time       `json:"serverTime"`
}

func (m *Module) RoundInfo(ctx context.Context, q dbtx.Querier, fid uuid.UUID) (RoundInfo, error) {
	f, err := m.GetFlight(ctx, q, fid)
	if err != nil {
		return RoundInfo{}, err
	}
	if err := m.canSeeFlight(ctx, q, f); err != nil {
		return RoundInfo{}, err
	}
	ri := RoundInfo{Flight: f, Players: []PlayerContext{}, Scorecards: []Scorecard{}, ServerTime: clock.Now()}
	if ri.Holes, err = RouteHoles(ctx, q, f.RouteID, f.TeeSetID); err != nil {
		return ri, err
	}
	var me *uuid.UUID
	if c, err := m.myCaddy(ctx, q, f.PropertyID); err == nil {
		me = &c.ID
	}
	for _, p := range f.Players {
		pc := PlayerContext{PlayerID: p.ID, Name: p.Name, PlayerType: p.PlayerType, Preferences: []crm.Preference{}, Highlights: []string{}, ScorecardID: p.ScorecardID}
		if p.TeeSetID != nil {
			var n string
			if err := q.QueryRow(ctx, `SELECT name FROM golf.tee_sets WHERE id = $1`, *p.TeeSetID).Scan(&n); err == nil {
				pc.TeeSet = &n
			}
		}
		if p.CustomerID != nil {
			_ = q.QueryRow(ctx, `SELECT coalesce(official_index, local_index)::text FROM golf.handicaps WHERE customer_id = $1`, *p.CustomerID).Scan(&pc.Handicap)
			if m.CRM != nil {
				cc, err := m.CRM.Context(ctx, q, f.PropertyID, *p.CustomerID, false)
				if err != nil {
					return ri, err
				}
				pc.Preferences, pc.Highlights = cc.Preferences, cc.Highlights
			}
			if me != nil {
				if err := q.QueryRow(ctx, `SELECT count(DISTINCT a.flight_id)::int, max(fl.tee_time),
					EXISTS (SELECT 1 FROM golf.caddy_favorites fv WHERE fv.caddy_id = $1 AND fv.customer_id = $2)
					FROM golf.caddy_assignments a JOIN golf.flights fl ON fl.id = a.flight_id
					JOIN golf.flight_players pl ON pl.flight_id = a.flight_id AND (a.player_id IS NULL OR a.player_id = pl.id)
					WHERE a.caddy_id = $1 AND pl.customer_id = $2 AND a.status IN ('completed', 'replaced') AND a.flight_id <> $3`,
					*me, *p.CustomerID, f.ID).Scan(&pc.RoundsWithMe, &pc.LastRoundWithMe, &pc.FavoriteOfMine); err != nil {
					return ri, err
				}
			}
		}
		ri.Players = append(ri.Players, pc)
		if p.ScorecardID != nil {
			sc, err := m.GetScorecard(ctx, q, *p.ScorecardID)
			if err != nil {
				return ri, err
			}
			ri.Scorecards = append(ri.Scorecards, sc)
		}
	}
	ri.Times, err = m.RoundTimes(ctx, q, fid)
	return ri, err
}

type HandoverInput struct {
	DeviceID string `json:"deviceId"`
}

// Handover moves the round to a replacement tablet (FR-CTB-09): every hole
// and score already synced stays on the server; the new device continues.
func (m *Module) Handover(ctx context.Context, tx pgx.Tx, fid uuid.UUID, in HandoverInput) (RoundInfo, error) {
	if err := handle.Required("deviceId", in.DeviceID); err != nil {
		return RoundInfo{}, err
	}
	f, err := m.lockFlight(ctx, tx, fid)
	if err != nil {
		return RoundInfo{}, err
	}
	if err := m.canSeeFlight(ctx, tx, f); err != nil {
		return RoundInfo{}, err
	}
	if f.Status != "on_course" && f.Status != "checked_in" {
		return RoundInfo{}, errs.Conflict("not_on_course", "flight is "+f.Status)
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.flights SET device_id = $2 WHERE id = $1`, fid, in.DeviceID); err != nil {
		return RoundInfo{}, err
	}
	if err := record(ctx, tx, "golf.flight", fid, f.FlightNo, "device_handover", f.PropertyID, map[string]any{"deviceId": f.DeviceID},
		map[string]any{"deviceId": in.DeviceID}, ""); err != nil {
		return RoundInfo{}, err
	}
	return m.RoundInfo(ctx, tx, fid)
}

// ── on-course order & preference (FR-CTB-07) ──────────────────────────────

type CourseOrderInput struct {
	ID       *uuid.UUID             `json:"id,omitempty" doc:"Client UUIDv7 (offline)"`
	FlightID uuid.UUID              `json:"flightId"`
	PlayerID uuid.UUID              `json:"playerId"`
	OutletID uuid.UUID              `json:"outletId" doc:"Halfway House / clubhouse outlet"`
	Lines    []commercial.LineInput `json:"lines"`
	Deliver  string                 `json:"deliver,omitempty" enum:"hole,halfway_house" doc:"Default: the next hole"`
	Notes    string                 `json:"notes,omitempty"`
}

// CourseOrder places an on-course F&B order charged to the player's folio.
func (m *Module) CourseOrder(ctx context.Context, tx pgx.Tx, in CourseOrderInput) (commercial.Order, error) {
	f, err := m.GetFlight(ctx, tx, in.FlightID)
	if err != nil {
		return commercial.Order{}, err
	}
	if err := m.canSeeFlight(ctx, tx, f); err != nil {
		return commercial.Order{}, err
	}
	var player *Player
	for i := range f.Players {
		if f.Players[i].ID == in.PlayerID {
			player = &f.Players[i]
		}
	}
	if player == nil {
		return commercial.Order{}, handle.Invalid("playerId", "not_in_flight", "player is not in this flight")
	}
	if player.FolioID == nil {
		return commercial.Order{}, errs.Conflict("no_folio", "the player is not checked in")
	}
	dest, ref := "hole", itoa(min(f.CurrentSeq+1, max(f.Holes, 1)))
	if in.Deliver == "halfway_house" {
		dest, ref = "halfway_house", "Halfway House"
	}
	return m.Commercial.CreateOrder(ctx, tx, f.PropertyID, commercial.OrderInput{ID: in.ID, OutletID: in.OutletID, OrderType: "on_course", Source: "caddy_tablet",
		CustomerID: player.CustomerID, ServingDestination: dest, DestinationRef: ref, ChargeFolioID: player.FolioID, Lines: in.Lines, Send: true, Notes: in.Notes})
}

// RecordPreference records a preference noticed by the caddy for a player of
// one of the caddy's flights.
func (m *Module) RecordPreference(ctx context.Context, tx pgx.Tx, property, customer uuid.UUID, in crm.PreferenceInput) (crm.Preference, error) {
	if !can(ctx, "crm.preference.manage", property) {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.caddy_assignments a JOIN golf.caddies c ON c.id = a.caddy_id
			JOIN golf.flight_players p ON p.flight_id = a.flight_id WHERE c.user_id = $1 AND p.customer_id = $2 AND a.status IN ('accepted', 'active', 'completed')
			AND a.assigned_at > now() - interval '1 day')`, handle.UserID(ctx), customer).Scan(&ok); err != nil {
			return crm.Preference{}, err
		}
		if !ok {
			return crm.Preference{}, errs.Forbidden("you can only record preferences of players in your rounds")
		}
	}
	return crm.RecordPreference(ctx, tx, property, customer, in, "caddy")
}

// ── earnings (FR-CTB-10) ──────────────────────────────────────────────────

type EarningLine struct {
	PostedAt  time.Time `json:"postedAt" db:"posted_at"`
	Component string    `json:"component" db:"revenue_component" enum:"caddy_fee,caddy_tip"`
	Amount    string    `json:"amount" db:"amount"`
	Desc      string    `json:"description" db:"description"`
}

type Earnings struct {
	CaddyID     uuid.UUID         `json:"caddyId"`
	From        time.Time         `json:"from"`
	To          time.Time         `json:"to"`
	CaddyFee    string            `json:"caddyFee"`
	Tips        string            `json:"tips"`
	Lines       []EarningLine     `json:"lines"`
	Settlements []Settlement      `json:"settlements"`
	Attendance  []Attendance      `json:"attendance"`
	Assignments []CaddyAssignment `json:"assignments"`
}

func (m *Module) Earnings(ctx context.Context, q dbtx.Querier, property, caddy uuid.UUID, from, to time.Time) (Earnings, error) {
	e := Earnings{CaddyID: caddy, From: from, To: to}
	var err error
	if e.Lines, err = handle.List[EarningLine](q.Query(ctx, `SELECT posted_at, revenue_component, trim_scale(total_amount)::text AS amount, description
		FROM billing.folio_lines WHERE property_id = $1 AND beneficiary_type = 'caddy' AND beneficiary_id = $2 AND status = 'posted'
		AND posted_at >= $3 AND posted_at < $4 ORDER BY posted_at DESC`, property, caddy, from, to)); err != nil {
		return e, err
	}
	fee, tip := decimal.Zero, decimal.Zero
	for _, l := range e.Lines {
		a, _ := decimal.NewFromString(l.Amount)
		if l.Component == "caddy_tip" {
			tip = tip.Add(a)
		} else {
			fee = fee.Add(a)
		}
	}
	e.CaddyFee, e.Tips = fee.String(), tip.String()
	if e.Settlements, err = handle.List[Settlement](q.Query(ctx, settlementSelect+` WHERE s.caddy_id = $1 ORDER BY s.period_start DESC LIMIT 12`, caddy)); err != nil {
		return e, err
	}
	if e.Attendance, err = handle.List[Attendance](q.Query(ctx, attendanceSelect+` WHERE a.caddy_id = $1 AND a.clock_in >= $2 AND a.clock_in < $3
		ORDER BY a.work_date DESC`, caddy, from, to)); err != nil {
		return e, err
	}
	e.Assignments, err = handle.List[CaddyAssignment](q.Query(ctx, caddyAssignmentSelect+` WHERE a.caddy_id = $1 AND f.tee_time >= $2 AND f.tee_time < $3
		ORDER BY f.tee_time DESC`, caddy, from, to))
	return e, err
}

// ── offline sync (FR-CTB-08) ──────────────────────────────────────────────

// RoundSync is one queued tablet action.
type RoundSync struct {
	Op           string       `json:"op" enum:"accept,tee_off,hole,finish,score"`
	FlightID     uuid.UUID    `json:"flightId"`
	AssignmentID *uuid.UUID   `json:"assignmentId,omitempty"`
	ScorecardID  *uuid.UUID   `json:"scorecardId,omitempty"`
	Seq          int          `json:"seq,omitempty"`
	At           *time.Time   `json:"at,omitempty"`
	DeviceID     string       `json:"deviceId,omitempty"`
	Entries      []ScoreEntry `json:"entries,omitempty"`
}

// SyncHandler replays the tablet's offline queue. Assignment changes made on
// the server win (the caddy no longer on the flight gets a conflict); scores
// are last-write-wins per hole; hole progress is idempotent per hole.
func (m *Module) SyncHandler(ctx context.Context, tx pgx.Tx, payload json.RawMessage) (any, error) {
	var in RoundSync
	if err := json.Unmarshal(payload, &in); err != nil {
		return nil, errs.Validation("invalid_payload", "invalid round payload")
	}
	f, err := m.GetFlight(ctx, tx, in.FlightID)
	if err != nil {
		return nil, err
	}
	if err := m.canSeeFlight(ctx, tx, f); err != nil {
		return nil, &syncsvc.ConflictError{Message: "assignment changed on the server: " + err.Error()}
	}
	need := "golf.round.operate"
	if in.Op == "score" {
		need = "golf.scorecard.enter"
	}
	if err := authz.RequireAt(ctx, need, f.PropertyID); err != nil {
		return nil, err
	}
	switch in.Op {
	case "accept":
		if in.AssignmentID == nil {
			return nil, errs.Validation("assignment_required", "assignmentId is required")
		}
		return m.AcceptAssignment(ctx, tx, *in.AssignmentID)
	case "tee_off":
		r, err := m.TeeOff(ctx, tx, in.FlightID, RoundEvent{At: in.At, DeviceID: in.DeviceID})
		return map[string]any{"status": r.Status, "currentSeq": r.CurrentSeq}, err
	case "hole":
		r, err := m.RecordHole(ctx, tx, in.FlightID, HoleInput{Seq: in.Seq, At: in.At, DeviceID: in.DeviceID})
		return map[string]any{"status": r.Status, "currentSeq": r.CurrentSeq}, err
	case "finish":
		r, err := m.FinishRound(ctx, tx, in.FlightID, RoundEvent{At: in.At, DeviceID: in.DeviceID})
		return map[string]any{"status": r.Status}, err
	case "score":
		if in.ScorecardID == nil {
			return nil, errs.Validation("scorecard_required", "scorecardId is required")
		}
		var fl *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT flight_id FROM golf.scorecards WHERE id = $1`, *in.ScorecardID).Scan(&fl); err != nil {
			if dbtx.IsNoRows(err) {
				return nil, errs.NotFound("scorecard")
			}
			return nil, err
		}
		if fl == nil || *fl != in.FlightID {
			return nil, errs.Validation("scorecard_mismatch", "scorecard is not part of this flight")
		}
		sc, err := m.EnterScores(ctx, tx, *in.ScorecardID, ScoreInput{Entries: in.Entries, Source: "caddy", DeviceID: in.DeviceID})
		return map[string]any{"scorecardId": sc.ID, "gross": sc.Gross, "status": sc.Status}, err
	}
	return nil, errs.Validation("invalid_op", "unknown round operation "+in.Op)
}
