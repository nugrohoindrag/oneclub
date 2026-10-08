package experience

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
	"oneclub/internal/golf"
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
	CourseID       uuid.UUID  `json:"courseId"`
	HoleID         *uuid.UUID `json:"holeId" doc:"Hole being played"`
	Label          string     `json:"label" doc:"Booking code or flight number"`
	RouteName      *string    `json:"playingRouteName"`
	TeeTime        time.Time  `json:"teeTime"`
	TeeOffAt       time.Time  `json:"teeOffAt"`
	CurrentSeq     int        `json:"currentSeq"`
	Holes          int        `json:"holes"`
	Hole           string     `json:"hole"`
	ElapsedMinutes int        `json:"elapsedMinutes"`
	TargetMinutes  int        `json:"targetMinutes" doc:"Cumulative target through the current hole"`
	BehindMinutes  int        `json:"behindMinutes" doc:"Positive: behind the target"`
	Slow           bool       `json:"slow" doc:"Behind more than the route tolerance"`
	AheadLabel     *string    `json:"aheadLabel"`
	GapHoles       *int       `json:"gapHoles" doc:"Holes between this flight and the flight ahead"`
	GapMinutes     *int       `json:"gapMinutes" doc:"Minutes since the flight ahead started this hole"`
	Players        []string   `json:"players"`
	Caddies        []string   `json:"caddies"`
	GolfCarts      []string   `json:"golfCarts"`
	HoleStartedAt  *time.Time `json:"holeStartedAt"`
}

// Pace computes pace of play for flights in play (FR-PLX-04).
func (m *Module) Pace(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]PaceFlight, error) {
	ids, err := collectIDs(q.Query(ctx, `SELECT id FROM golf.flights WHERE property_id = $1 AND status = 'in_play' AND tee_off_at IS NOT NULL ORDER BY tee_off_at`, property))
	if err != nil {
		return nil, err
	}
	now := clock.Now()
	routeHoles := map[uuid.UUID][]RouteHole{}
	rounds := make([]Round, 0, len(ids))
	out := make([]PaceFlight, 0, len(ids))
	starts := map[uuid.UUID]map[int]time.Time{}
	for _, fid := range ids {
		r, err := m.GetRound(ctx, q, fid)
		if err != nil {
			return nil, err
		}
		var holes []RouteHole
		if r.RouteID != nil {
			var ok bool
			if holes, ok = routeHoles[*r.RouteID]; !ok {
				if holes, err = m.roundHoles(ctx, q, r); err != nil {
					return nil, err
				}
				routeHoles[*r.RouteID] = holes
			}
		}
		st := map[int]time.Time{}
		rows, err := q.Query(ctx, `SELECT seq, started_at FROM golf.hole_progress WHERE flight_id = $1`, fid)
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
		starts[fid] = st
		targets, err := holeTargets(ctx, q, holes)
		if err != nil {
			return nil, err
		}
		target := 0
		for i := 0; i < r.CurrentSeq && i < len(holes); i++ {
			target += targets[holes[i].HoleID]
		}
		elapsed := int(now.Sub(*r.TeeOffAt).Minutes())
		p := PaceFlight{FlightID: fid, CourseID: r.CourseID, Label: r.Label(), RouteName: r.RouteName, TeeTime: r.TeeTime, TeeOffAt: *r.TeeOffAt, CurrentSeq: r.CurrentSeq,
			Holes: len(holes), ElapsedMinutes: elapsed, TargetMinutes: target, BehindMinutes: elapsed - target, Players: []string{}, Caddies: []string{},
			GolfCarts: []string{}}
		p.Slow = p.BehindMinutes > r.Tolerance
		if r.CurrentSeq >= 1 && r.CurrentSeq <= len(holes) {
			p.Hole = holes[r.CurrentSeq-1].SectionCode + "-" + itoa(holes[r.CurrentSeq-1].Number)
			hid := holes[r.CurrentSeq-1].HoleID
			p.HoleID = &hid
		}
		if t, ok := st[r.CurrentSeq]; ok {
			p.HoleStartedAt = &t
		}
		for _, pl := range r.Players {
			if pl.Status == "checked_in" {
				p.Players = append(p.Players, pl.Name)
			}
		}
		for _, c := range r.Caddies {
			if c.Status == "in_play" {
				p.Caddies = append(p.Caddies, c.CaddyCode)
			}
		}
		for _, c := range r.GolfCarts {
			if c.Status == "in_use" {
				p.GolfCarts = append(p.GolfCarts, c.CartCode)
			}
		}
		rounds = append(rounds, r)
		out = append(out, p)
	}
	// Gap to the flight ahead on the same route (teed off earlier).
	for i := range out {
		for j := i - 1; j >= 0; j-- {
			if rounds[j].RouteID == nil || rounds[i].RouteID == nil || *rounds[j].RouteID != *rounds[i].RouteID {
				continue
			}
			label := out[j].Label
			gh := out[j].CurrentSeq - out[i].CurrentSeq
			out[i].AheadLabel, out[i].GapHoles = &label, &gh
			if t, ok := starts[rounds[j].FlightID][out[i].CurrentSeq]; ok && out[i].HoleStartedAt != nil {
				gm := int(out[i].HoleStartedAt.Sub(t).Minutes())
				out[i].GapMinutes = &gm
			}
			break
		}
	}
	return out, nil
}

// CheckPace flags flights that became slow (periodic job) and pushes an alert
// to the Starter / Marshal screens.
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
		tag, err := tx.Exec(ctx, `INSERT INTO golf.round_progress (flight_id, property_id, pace_status, behind_minutes) VALUES ($1,$4,$2,$3)
			ON CONFLICT (flight_id) DO UPDATE SET pace_status = EXCLUDED.pace_status, behind_minutes = EXCLUDED.behind_minutes
			WHERE golf.round_progress.pace_status <> EXCLUDED.pace_status`, p.FlightID, st, p.BehindMinutes, property)
		if err != nil {
			return n, err
		}
		if tag.RowsAffected() > 0 && p.Slow {
			n++
			if err := m.live(ctx, tx, "golf.pace", property, "slow", p.FlightID.String(), map[string]any{"label": p.Label, "behind": p.BehindMinutes,
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
}

// myCaddy resolves the caddy of the signed-in tablet user.
func (m *Module) myCaddy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (caddyRow, error) {
	rows, err := q.Query(ctx, `SELECT c.id, c.property_id, c.code, c.name FROM golf.caddies c JOIN golf.caddy_profiles p ON p.caddy_id = c.id
		WHERE c.property_id = $1 AND p.user_id = $2 AND c.archived_at IS NULL`,
		property, handle.UserID(ctx))
	return handle.One[caddyRow](rows, err, "caddy profile of this user")
}

// MyAssignments is the caddy's My Assignments screen.
type MyAssignments struct {
	CaddyID    uuid.UUID         `json:"caddyId"`
	Code       string            `json:"code"`
	Name       string            `json:"name"`
	DutyStatus string            `json:"dutyStatus" enum:"off_duty,available,assigned,in_play"`
	Current    *CaddyAssignment  `json:"current"`
	Next       []CaddyAssignment `json:"next"`
	QueueNo    *int              `json:"queuePosition" doc:"Position in today's rotation when no assignment is waiting"`
}

func (m *Module) MyAssignments(ctx context.Context, q dbtx.Querier, property uuid.UUID) (MyAssignments, error) {
	c, err := m.myCaddy(ctx, q, property)
	if err != nil {
		return MyAssignments{}, err
	}
	out := MyAssignments{CaddyID: c.ID, Code: c.Code, Name: c.Name, DutyStatus: "off_duty", Next: []CaddyAssignment{}}
	loc := location(ctx, q, property)
	list, err := golf.ListCaddyAssignments(ctx, q, loc, "a.caddy_id = $1 AND a.status IN ('assigned', 'in_play')", c.ID)
	if err != nil {
		return out, err
	}
	for i := len(list) - 1; i >= 0; i-- { // listed latest first: next assignments in tee time order
		if list[i].Status == "in_play" && out.Current == nil {
			out.Current = &list[i]
			out.DutyStatus = "in_play"
		} else {
			out.Next = append(out.Next, list[i])
		}
	}
	if out.Current == nil && len(out.Next) > 0 {
		out.DutyStatus = "assigned"
	}
	today := localDay(clock.Now(), loc)
	if out.Current == nil && len(out.Next) == 0 {
		var present bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.caddy_attendance a LEFT JOIN golf.caddy_shifts s ON s.caddy_id = a.caddy_id
			AND s.work_date = a.work_date WHERE a.caddy_id = $1 AND a.work_date = $2::date AND a.status = 'present' AND s.clocked_out_at IS NULL)`,
			c.ID, today.Format("2006-01-02")).Scan(&present); err != nil {
			return out, err
		}
		if present {
			out.DutyStatus = "available"
		}
		qe, err := m.Rotation(ctx, q, property, today, nil)
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
func (m *Module) canSeeFlight(ctx context.Context, q dbtx.Querier, r Round) error {
	if can(ctx, "golf.flight.view", r.PropertyID) {
		return nil
	}
	var ok bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.caddy_assignments a JOIN golf.caddy_profiles c ON c.caddy_id = a.caddy_id
		WHERE a.flight_id = $1 AND c.user_id = $2 AND a.status <> 'cancelled')`, r.FlightID, handle.UserID(ctx)).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return errs.Forbidden("you are not assigned to this flight")
	}
	return nil
}

// PlayerContext is the customer context shown on the tablet (FR-TAB-03):
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
	CourseHandicap  *int             `json:"courseHandicap" doc:"From the scorecard's tee set (WHS)"`
	HoleStrokes     []int            `json:"holeStrokes" doc:"Handicap strokes received per hole, in route sequence"`
}

// RoundInfo is everything the tablet needs for a round (cached offline).
type RoundInfo struct {
	Round         Round              `json:"round"`
	Holes         []RouteHole        `json:"holes"`
	Players       []PlayerContext    `json:"players"`
	Scorecards    []Scorecard        `json:"scorecards"`
	Times         RoundTimes         `json:"times"`
	Interventions []PaceIntervention `json:"interventions" doc:"Marshal messages not yet acknowledged"`
	ServerTime    time.Time          `json:"serverTime"`
}

func (m *Module) RoundInfo(ctx context.Context, q dbtx.Querier, fid uuid.UUID) (RoundInfo, error) {
	r, err := m.GetRound(ctx, q, fid)
	if err != nil {
		return RoundInfo{}, err
	}
	if err := m.canSeeFlight(ctx, q, r); err != nil {
		return RoundInfo{}, err
	}
	ri := RoundInfo{Round: r, Players: []PlayerContext{}, Scorecards: []Scorecard{}, ServerTime: clock.Now()}
	if ri.Holes, err = m.roundHoles(ctx, q, r); err != nil {
		return ri, err
	}
	var me *uuid.UUID
	if c, err := m.myCaddy(ctx, q, r.PropertyID); err == nil {
		me = &c.ID
	}
	for _, p := range r.Players {
		pc := PlayerContext{PlayerID: p.ID, Name: p.Name, PlayerType: p.PlayerType, Handicap: p.HandicapIndex, Preferences: []crm.Preference{},
			Highlights: []string{}, ScorecardID: p.ScorecardID}
		if p.CustomerID != nil {
			if pc.Handicap == nil {
				pc.Handicap = currentHandicap(ctx, q, *p.CustomerID)
			}
			if m.CRM != nil {
				cc, err := m.CRM.Context(ctx, q, r.PropertyID, *p.CustomerID, false)
				if err != nil {
					return ri, err
				}
				pc.Preferences, pc.Highlights = cc.Preferences, cc.Highlights
			}
			if me != nil {
				if err := q.QueryRow(ctx, `SELECT count(DISTINCT a.flight_id)::int, max(lower(a.period)),
					EXISTS (SELECT 1 FROM golf.caddy_favorites fv WHERE fv.caddy_id = $1 AND fv.customer_id = $2)
					FROM golf.caddy_assignments a JOIN golf.booking_players pl ON pl.id = ANY(a.player_ids)
					WHERE a.caddy_id = $1 AND pl.customer_id = $2 AND a.status IN ('completed', 'replaced') AND a.flight_id <> $3`,
					*me, *p.CustomerID, r.FlightID).Scan(&pc.RoundsWithMe, &pc.LastRoundWithMe, &pc.FavoriteOfMine); err != nil {
					return ri, err
				}
			}
		}
		if p.ScorecardID != nil {
			sc, err := m.GetScorecard(ctx, q, *p.ScorecardID)
			if err != nil {
				return ri, err
			}
			pc.TeeSet = sc.TeeSetName
			if pc.Handicap != nil {
				ch := CourseHandicap(dec(*pc.Handicap), sc.CourseRating, sc.SlopeRating, sc.Par, sc.Holes)
				si := make([]*int, len(sc.Scores))
				for i, h := range sc.Scores {
					si[i] = h.StrokeIndex
				}
				pc.CourseHandicap, pc.HoleStrokes = &ch, HoleStrokes(ch, si)
			}
			ri.Scorecards = append(ri.Scorecards, sc)
		}
		ri.Players = append(ri.Players, pc)
	}
	if ri.Interventions, err = openInterventions(ctx, q, fid); err != nil {
		return ri, err
	}
	ri.Times, err = m.RoundTimes(ctx, q, fid)
	return ri, err
}

type HandoverInput struct {
	DeviceID string `json:"deviceId"`
}

// Handover moves the round to a replacement tablet (FR-TAB-08): every hole
// and score already synced stays on the server; the new device continues.
func (m *Module) Handover(ctx context.Context, tx pgx.Tx, fid uuid.UUID, in HandoverInput) (RoundInfo, error) {
	if err := handle.Required("deviceId", in.DeviceID); err != nil {
		return RoundInfo{}, err
	}
	r, err := m.lockRound(ctx, tx, fid)
	if err != nil {
		return RoundInfo{}, err
	}
	if err := m.canSeeFlight(ctx, tx, r); err != nil {
		return RoundInfo{}, err
	}
	if r.Status == "completed" || r.Status == "cancelled" {
		return RoundInfo{}, errs.Conflict("round_closed", "flight is "+r.Status)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.round_progress (flight_id, property_id, tablet_device) VALUES ($1,$3,$2)
		ON CONFLICT (flight_id) DO UPDATE SET tablet_device = EXCLUDED.tablet_device`, fid, in.DeviceID, r.PropertyID); err != nil {
		return RoundInfo{}, err
	}
	if err := record(ctx, tx, "golf.flight", fid, r.Label(), "device_handover", r.PropertyID, map[string]any{"deviceId": r.DeviceID},
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
	OutletID uuid.UUID              `json:"outletId" doc:"Tee house, Halfway House or clubhouse outlet"`
	Lines    []commercial.LineInput `json:"lines"`
	Deliver  string                 `json:"deliver,omitempty" enum:"hole,halfway_house" doc:"Default: the next hole; halfway_house = pick up at the outlet (tee house)"`
	Notes    string                 `json:"notes,omitempty"`
}

// CourseOrder places an on-course F&B order charged to the player's folio.
func (m *Module) CourseOrder(ctx context.Context, tx pgx.Tx, in CourseOrderInput) (commercial.Order, error) {
	f, err := m.GetRound(ctx, tx, in.FlightID)
	if err != nil {
		return commercial.Order{}, err
	}
	if err := m.canSeeFlight(ctx, tx, f); err != nil {
		return commercial.Order{}, err
	}
	var player *RoundPlayer
	for i := range f.Players {
		if f.Players[i].ID == in.PlayerID {
			player = &f.Players[i]
		}
	}
	if player == nil {
		return commercial.Order{}, handle.Invalid("playerId", "not_in_flight", "player is not in this flight")
	}
	if player.Status != "checked_in" || f.FolioID == nil {
		return commercial.Order{}, errs.Conflict("no_folio", "the player is not checked in")
	}
	dest, ref := "hole", itoa(min(f.CurrentSeq+1, max(f.Holes, 1)))
	if in.Deliver == "halfway_house" {
		// pick-up at the outlet itself: the Halfway House or a tee house
		ref = "Halfway House"
		if name, err := commercial.OutletName(ctx, tx, in.OutletID); err == nil {
			ref = name
		}
		dest = "halfway_house"
	}
	return m.POS.CreateOrder(ctx, tx, f.PropertyID, commercial.OrderInput{ID: in.ID, OutletID: in.OutletID, OrderType: "on_course", Source: "caddy_tablet",
		CustomerID: player.CustomerID, ServingDestination: dest, DestinationRef: ref, ChargeFolioID: f.FolioID, Lines: in.Lines, Send: true, Notes: in.Notes})
}

// RecordPreference records a preference noticed by the caddy for a player of
// one of the caddy's flights.
func (m *Module) RecordPreference(ctx context.Context, tx pgx.Tx, property, customer uuid.UUID, in crm.PreferenceInput) (crm.Preference, error) {
	if !can(ctx, "crm.preference.manage", property) {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.caddy_assignments a JOIN golf.caddy_profiles c ON c.caddy_id = a.caddy_id
			JOIN golf.booking_players p ON p.id = ANY(a.player_ids) WHERE c.user_id = $1 AND p.customer_id = $2 AND a.status IN ('assigned', 'in_play', 'completed')
			AND a.assigned_at > now() - interval '1 day')`, handle.UserID(ctx), customer).Scan(&ok); err != nil {
			return crm.Preference{}, err
		}
		if !ok {
			return crm.Preference{}, errs.Forbidden("you can only record preferences of players in your rounds")
		}
	}
	return crm.RecordPreference(ctx, tx, property, customer, in, "caddy")
}

// ── earnings (FR-TAB-09) ──────────────────────────────────────────────────

// EarningLine is a caddy fee of a finished round or a tip.
type EarningLine struct {
	Date        time.Time `json:"date" db:"day"`
	Component   string    `json:"component" db:"component" enum:"caddy_fee,caddy_tip"`
	Method      *string   `json:"method" db:"method" enum:"cash,non_cash"`
	Amount      string    `json:"amount" db:"amount"`
	Description string    `json:"description" db:"description"`
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
	loc := location(ctx, q, property)
	df, dt := from.In(loc).Format("2006-01-02"), to.In(loc).Format("2006-01-02")
	var err error
	if e.Lines, err = handle.List[EarningLine](q.Query(ctx, `SELECT a.play_date AS day, 'caddy_fee' AS component, NULL::text AS method,
		  trim_scale(coalesce(fs.share_amount, a.fee_amount))::text AS amount, coalesce(b.code, 'Flight') AS description
		FROM golf.caddy_assignments a JOIN golf.flights f ON f.id = a.flight_id LEFT JOIN golf.bookings b ON b.id = f.booking_id
		LEFT JOIN golf.caddy_fee_shares fs ON fs.assignment_id = a.id
		WHERE a.property_id = $1 AND a.caddy_id = $2 AND a.status IN ('completed', 'replaced') AND a.play_date >= $3::date AND a.play_date < $4::date
		UNION ALL
		SELECT t.tip_date, 'caddy_tip', t.method, trim_scale(t.amount)::text, 'Tip'
		FROM golf.caddy_tips t WHERE t.property_id = $1 AND t.caddy_id = $2 AND t.tip_date >= $3::date AND t.tip_date < $4::date
		ORDER BY day DESC`, property, caddy, df, dt)); err != nil {
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
	if e.Attendance, err = handle.List[Attendance](q.Query(ctx, `SELECT c.id AS caddy_id, c.code AS caddy_code, c.name AS caddy_name, a.work_date, a.status,
		a.queue_no::float8 AS queue_no, s.shift, s.clocked_in_at, s.clocked_out_at,
		(SELECT count(*) FROM golf.caddy_assignments x WHERE x.caddy_id = c.id AND x.play_date = a.work_date AND x.status IN ('assigned', 'in_play', 'completed'))::int AS rounds
		FROM golf.caddy_attendance a JOIN golf.caddies c ON c.id = a.caddy_id LEFT JOIN golf.caddy_shifts s ON s.caddy_id = a.caddy_id AND s.work_date = a.work_date
		WHERE a.property_id = $1 AND a.caddy_id = $2 AND a.work_date >= $3::date AND a.work_date < $4::date ORDER BY a.work_date DESC`, property, caddy, df, dt)); err != nil {
		return e, err
	}
	e.Assignments, err = golf.ListCaddyAssignments(ctx, q, loc, "a.caddy_id = $1 AND a.play_date >= $2::date AND a.play_date < $3::date", caddy, df, dt)
	return e, err
}

// ── offline sync (FR-TAB-07) ──────────────────────────────────────────────

// RoundSync is one queued tablet action.
type RoundSync struct {
	Op           string       `json:"op" enum:"accept,tee_off,hole,finish,score,pause,resume"`
	Reason       string       `json:"reason,omitempty" doc:"pause: rain, lightning, other"`
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
	r, err := m.GetRound(ctx, tx, in.FlightID)
	if err != nil {
		return nil, err
	}
	if err := m.canSeeFlight(ctx, tx, r); err != nil {
		return nil, &syncsvc.ConflictError{Message: "assignment changed on the server: " + err.Error()}
	}
	need := "golf.round.operate"
	if in.Op == "score" {
		need = "golf.scorecard.enter"
	}
	if err := authz.RequireAt(ctx, need, r.PropertyID); err != nil {
		return nil, err
	}
	switch in.Op {
	case "accept":
		if in.AssignmentID == nil {
			return nil, errs.Validation("assignment_required", "assignmentId is required")
		}
		return m.AcceptAssignment(ctx, tx, *in.AssignmentID)
	case "tee_off":
		out, err := m.StartRound(ctx, tx, in.FlightID, RoundEvent{At: in.At, DeviceID: in.DeviceID})
		return map[string]any{"status": out.Status, "currentSeq": out.CurrentSeq}, err
	case "hole":
		out, err := m.RecordHole(ctx, tx, in.FlightID, HoleInput{Seq: in.Seq, At: in.At, DeviceID: in.DeviceID})
		return map[string]any{"status": out.Status, "currentSeq": out.CurrentSeq}, err
	case "finish":
		out, err := m.FinishRound(ctx, tx, in.FlightID, RoundEvent{At: in.At, DeviceID: in.DeviceID})
		return map[string]any{"status": out.Status}, err
	case "pause":
		return m.Golf.PauseRound(ctx, tx, in.FlightID, golf.PauseInput{Reason: in.Reason, At: in.At})
	case "resume":
		return m.Golf.ResumeRound(ctx, tx, in.FlightID, golf.PauseInput{At: in.At})
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
