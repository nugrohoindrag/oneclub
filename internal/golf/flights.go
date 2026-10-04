package golf

// Flights and rounds — the interim P1 contract (PRD P2 §5.4.2 C7/C8) that
// the P2 caddy, golf cart, tablet, scoring and pace-of-play features use:
// flight with players, check-in with all-in pricing, caddy and golf cart
// assignment, tee off, hole progress, round completion and the golf events
// golf.player_checked_in, golf.caddy_assigned, golf.golf_cart_assigned,
// golf.flight_teed_off and golf.round_finished.

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/membership"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/numbering"
)

// RouteHole is one hole of a playing route in playing order.
type RouteHole struct {
	Seq           int            `json:"seq" db:"seq"`
	HoleID        uuid.UUID      `json:"holeId" db:"hole_id"`
	Number        int            `json:"number" db:"number"`
	SectionCode   string         `json:"sectionCode" db:"section_code"`
	Par           int            `json:"par" db:"par"`
	StrokeIndex   int            `json:"strokeIndex" db:"stroke_index"`
	TargetMinutes int            `json:"targetMinutes" db:"target_minutes"`
	MapURL        *string        `json:"mapUrl" db:"map_url"`
	PanoramaURL   *string        `json:"panoramaUrl" db:"panorama_url"`
	Geo           map[string]any `json:"geo" db:"geo"`
	DistanceM     *int           `json:"distanceMeters" db:"distance_m"`
}

// RouteHoles lists the holes of a route; distances come from the tee set.
func RouteHoles(ctx context.Context, q dbtx.Querier, routeID uuid.UUID, teeSetID *uuid.UUID) ([]RouteHole, error) {
	return handle.List[RouteHole](q.Query(ctx, `SELECT (row_number() OVER (ORDER BY u.ord, h.number))::int AS seq, h.id AS hole_id, h.number,
		s.code AS section_code, h.par, h.stroke_index, h.target_minutes, h.map_url, h.panorama_url, h.geo,
		(t.distances->>h.id::text)::int AS distance_m
		FROM golf.playing_routes r CROSS JOIN LATERAL unnest(r.section_ids) WITH ORDINALITY AS u(sid, ord)
		JOIN golf.course_sections s ON s.id = u.sid::uuid JOIN golf.holes h ON h.section_id = s.id
		LEFT JOIN golf.tee_sets t ON t.id = $2
		WHERE r.id = $1 ORDER BY u.ord, h.number`, routeID, teeSetID))
}

type playRoute struct {
	ID         uuid.UUID `db:"id"`
	PropertyID uuid.UUID `db:"property_id"`
	CourseID   uuid.UUID `db:"course_id"`
	Code       string    `db:"code"`
	Name       string    `db:"name"`
	Target     *int      `db:"target_minutes"`
	Tolerance  int       `db:"tolerance_minutes"`
	Status     string    `db:"status"`
}

func loadRoute(ctx context.Context, q dbtx.Querier, rid uuid.UUID) (playRoute, error) {
	rows, err := q.Query(ctx, `SELECT id, property_id, course_id, code, name, target_minutes, tolerance_minutes, status FROM golf.playing_routes WHERE id = $1`, rid)
	return handle.One[playRoute](rows, err, "playing route")
}

// ── types ─────────────────────────────────────────────────────────────────

type Player struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	CustomerID   *uuid.UUID `json:"customerId" db:"customer_id"`
	Name         string     `json:"name" db:"name"`
	PlayerType   string     `json:"playerType" db:"player_type" enum:"member,guest_of_member,visitor,reciprocal"`
	TeeSetID     *uuid.UUID `json:"teeSetId" db:"tee_set_id"`
	FolioID      *uuid.UUID `json:"folioId" db:"folio_id"`
	HIOInsured   bool       `json:"hioInsured" db:"hio_insured"`
	HIOPolicyRef *string    `json:"hioPolicyRef" db:"hio_policy_ref"`
	Segment      *string    `json:"segment" db:"segment"`
	Status       string     `json:"status" db:"status" enum:"booked,checked_in,playing,finished,cancelled,no_show"`
	CheckedInAt  *time.Time `json:"checkedInAt" db:"checked_in_at"`
	ScorecardID  *uuid.UUID `json:"scorecardId" db:"scorecard_id"`
}

type CaddyAssignment struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	FlightID      uuid.UUID  `json:"flightId" db:"flight_id"`
	FlightNo      string     `json:"flightNo" db:"flight_no"`
	PlayerID      *uuid.UUID `json:"playerId" db:"player_id"`
	PlayerName    *string    `json:"playerName" db:"player_name"`
	CaddyID       uuid.UUID  `json:"caddyId" db:"caddy_id"`
	CaddyCode     string     `json:"caddyCode" db:"caddy_code"`
	CaddyName     string     `json:"caddyName" db:"caddy_name"`
	Status        string     `json:"status" db:"status" enum:"assigned,accepted,active,replaced,completed,cancelled"`
	FromSeq       int        `json:"fromSeq" db:"from_seq"`
	ToSeq         *int       `json:"toSeq" db:"to_seq"`
	FeeAmount     string     `json:"feeAmount" db:"fee_amount"`
	ReplaceReason *string    `json:"replaceReason" db:"replace_reason"`
	TeeTime       time.Time  `json:"teeTime" db:"tee_time"`
	AssignedAt    time.Time  `json:"assignedAt" db:"assigned_at"`
	AcceptedAt    *time.Time `json:"acceptedAt" db:"accepted_at"`
	StartedAt     *time.Time `json:"startedAt" db:"started_at"`
	EndedAt       *time.Time `json:"endedAt" db:"ended_at"`
}

const caddyAssignmentSelect = `SELECT a.id, a.flight_id, f.flight_no, a.player_id, p.name AS player_name, a.caddy_id, c.code AS caddy_code, c.name AS caddy_name,
	a.status, a.from_seq, a.to_seq, trim_scale(a.fee_amount)::text AS fee_amount, a.replace_reason, f.tee_time, a.assigned_at, a.accepted_at, a.started_at, a.ended_at
	FROM golf.caddy_assignments a JOIN golf.flights f ON f.id = a.flight_id JOIN golf.caddies c ON c.id = a.caddy_id
	LEFT JOIN golf.flight_players p ON p.id = a.player_id`

type CartAssignment struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	FlightID      uuid.UUID  `json:"flightId" db:"flight_id"`
	CartID        uuid.UUID  `json:"cartId" db:"cart_id"`
	CartCode      string     `json:"cartCode" db:"cart_code"`
	Status        string     `json:"status" db:"status" enum:"assigned,in_use,returned,replaced,cancelled"`
	FromSeq       int        `json:"fromSeq" db:"from_seq"`
	ToSeq         *int       `json:"toSeq" db:"to_seq"`
	OutAt         *time.Time `json:"outAt" db:"out_at"`
	ReturnedAt    *time.Time `json:"returnedAt" db:"returned_at"`
	UsageHours    *string    `json:"usageHours" db:"usage_hours"`
	ReplaceReason *string    `json:"replaceReason" db:"replace_reason"`
	AssignedAt    time.Time  `json:"assignedAt" db:"assigned_at"`
}

const cartAssignmentSelect = `SELECT a.id, a.flight_id, a.cart_id, g.code AS cart_code, a.status, a.from_seq, a.to_seq, a.out_at, a.returned_at,
	trim_scale(a.usage_hours)::text AS usage_hours, a.replace_reason, a.assigned_at
	FROM golf.cart_assignments a JOIN golf.golf_carts g ON g.id = a.cart_id`

// Flight is a tee-time flight with its players, caddies and golf carts.
type Flight struct {
	ID         uuid.UUID         `json:"id" db:"id"`
	PropertyID uuid.UUID         `json:"propertyId" db:"property_id"`
	FlightNo   string            `json:"flightNo" db:"flight_no"`
	RouteID    uuid.UUID         `json:"routeId" db:"route_id"`
	RouteName  string            `json:"routeName" db:"route_name"`
	TeeSetID   *uuid.UUID        `json:"teeSetId" db:"tee_set_id"`
	TeeTime    time.Time         `json:"teeTime" db:"tee_time"`
	Status     string            `json:"status" db:"status" enum:"booked,checked_in,on_course,finished,cancelled,no_show"`
	CurrentSeq int               `json:"currentSeq" db:"current_seq"`
	Holes      int               `json:"holes" db:"holes"`
	TeedOffAt  *time.Time        `json:"teedOffAt" db:"teed_off_at"`
	FinishedAt *time.Time        `json:"finishedAt" db:"finished_at"`
	DeviceID   *string           `json:"deviceId" db:"device_id"`
	Notes      *string           `json:"notes" db:"notes"`
	Players    []Player          `json:"players" db:"-"`
	Caddies    []CaddyAssignment `json:"caddies" db:"-"`
	Carts      []CartAssignment  `json:"golfCarts" db:"-"`
}

const flightSelect = `SELECT f.id, f.property_id, f.flight_no, f.route_id, r.name AS route_name, f.tee_set_id, f.tee_time, f.status, f.current_seq,
	(SELECT count(*) FROM unnest(r.section_ids) s JOIN golf.holes h ON h.section_id = s::uuid)::int AS holes,
	f.teed_off_at, f.finished_at, f.device_id, f.notes FROM golf.flights f JOIN golf.playing_routes r ON r.id = f.route_id`

// GetFlight loads a flight with players and assignments.
func (m *Module) GetFlight(ctx context.Context, q dbtx.Querier, fid uuid.UUID) (Flight, error) {
	rows, err := q.Query(ctx, flightSelect+` WHERE f.id = $1`, fid)
	f, err := handle.One[Flight](rows, err, "flight")
	if err != nil {
		return f, err
	}
	if f.Players, err = handle.List[Player](q.Query(ctx, `SELECT p.id, p.customer_id, p.name, p.player_type, p.tee_set_id, p.folio_id, p.hio_insured,
		p.hio_policy_ref, p.segment, p.status, p.checked_in_at, s.id AS scorecard_id FROM golf.flight_players p
		LEFT JOIN golf.scorecards s ON s.player_id = p.id WHERE p.flight_id = $1 ORDER BY p.created_at, p.id`, fid)); err != nil {
		return f, err
	}
	if f.Caddies, err = handle.List[CaddyAssignment](q.Query(ctx, caddyAssignmentSelect+` WHERE a.flight_id = $1 ORDER BY a.from_seq, a.assigned_at`, fid)); err != nil {
		return f, err
	}
	f.Carts, err = handle.List[CartAssignment](q.Query(ctx, cartAssignmentSelect+` WHERE a.flight_id = $1 ORDER BY a.from_seq, a.assigned_at`, fid))
	return f, err
}

func (m *Module) lockFlight(ctx context.Context, tx pgx.Tx, fid uuid.UUID) (Flight, error) {
	var pid uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id FROM golf.flights WHERE id = $1 FOR UPDATE`, fid).Scan(&pid); err != nil {
		if dbtx.IsNoRows(err) {
			return Flight{}, errs.NotFound("flight")
		}
		return Flight{}, err
	}
	return m.GetFlight(ctx, tx, fid)
}

// ── create & check-in ─────────────────────────────────────────────────────

type PlayerInput struct {
	CustomerID        *uuid.UUID `json:"customerId,omitempty"`
	Name              string     `json:"name,omitempty"`
	Phone             string     `json:"phone,omitempty"`
	Email             string     `json:"email,omitempty"`
	PlayerType        string     `json:"playerType,omitempty" enum:"member,guest_of_member,visitor,reciprocal"`
	TeeSetID          *uuid.UUID `json:"teeSetId,omitempty"`
	ReciprocalVisitID *uuid.UUID `json:"reciprocalVisitId,omitempty"`
}

type FlightInput struct {
	RouteID  uuid.UUID     `json:"routeId"`
	TeeSetID *uuid.UUID    `json:"teeSetId,omitempty"`
	TeeTime  time.Time     `json:"teeTime"`
	Players  []PlayerInput `json:"players"`
	Notes    string        `json:"notes,omitempty"`
}

var playerTypes = []string{"member", "guest_of_member", "visitor", "reciprocal"}

// CreateFlight books a flight (interim tee time booking, P1 contract).
func (m *Module) CreateFlight(ctx context.Context, tx pgx.Tx, property uuid.UUID, in FlightInput) (Flight, error) {
	rt, err := loadRoute(ctx, tx, in.RouteID)
	if err != nil {
		return Flight{}, err
	}
	if rt.PropertyID != property || rt.Status != "active" {
		return Flight{}, handle.Invalid("routeId", "unavailable", "playing route is not available")
	}
	if len(in.Players) == 0 || len(in.Players) > 4 {
		return Flight{}, handle.Invalid("players", "invalid_players", "a flight has 1 to 4 players")
	}
	if in.TeeTime.IsZero() {
		return Flight{}, handle.Invalid("teeTime", "required", "tee time is required")
	}
	no, err := numbering.Next(ctx, tx, property, "FLT", in.TeeTime.In(localNow(ctx, tx).Location()))
	if err != nil {
		return Flight{}, err
	}
	fid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO golf.flights (id, property_id, flight_no, route_id, tee_set_id, tee_time, notes, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, fid, property, no, in.RouteID, in.TeeSetID, in.TeeTime, nzs(in.Notes), actor(ctx)); err != nil {
		return Flight{}, err
	}
	for i, p := range in.Players {
		if err := m.addPlayer(ctx, tx, property, fid, i, p); err != nil {
			return Flight{}, err
		}
	}
	f, err := m.GetFlight(ctx, tx, fid)
	if err != nil {
		return f, err
	}
	return f, record(ctx, tx, "golf.flight", fid, no, audit.ActionCreate, property, nil, f, "")
}

func (m *Module) addPlayer(ctx context.Context, tx pgx.Tx, property, fid uuid.UUID, i int, p PlayerInput) error {
	if p.PlayerType == "" {
		p.PlayerType = "member"
	}
	if !slices.Contains(playerTypes, p.PlayerType) {
		return handle.Invalid("players", "invalid_player_type", "unknown player type "+p.PlayerType)
	}
	name := p.Name
	switch {
	case p.CustomerID != nil:
		c, err := crm.GetCustomer(ctx, tx, *p.CustomerID)
		if err != nil {
			return err
		}
		name = c.Name
	case p.Name != "" && (p.Phone != "" || p.Email != ""):
		// Guest rounds are kept on the customer profile (FR-SCR-10).
		c, _, err := crm.FindOrCreate(ctx, tx, property, crm.Identity{Name: p.Name, Phone: p.Phone, Email: p.Email})
		if err != nil {
			return err
		}
		p.CustomerID, name = &c.ID, c.Name
	case p.Name == "":
		return handle.Invalid("players", "required", "player name or customer is required")
	}
	if p.PlayerType == "member" && p.CustomerID == nil {
		return handle.Invalid("players", "member_required", "a member player needs a customer")
	}
	if p.ReciprocalVisitID != nil {
		var verified bool
		if err := tx.QueryRow(ctx, `SELECT verified FROM golf.reciprocal_visits WHERE id = $1 AND property_id = $2 AND direction = 'inbound'`,
			*p.ReciprocalVisitID, property).Scan(&verified); err != nil {
			if dbtx.IsNoRows(err) {
				return errs.NotFound("reciprocal visit")
			}
			return err
		}
		if !verified {
			return errs.Conflict("not_verified", "the reciprocal visitor has not been verified")
		}
		p.PlayerType = "reciprocal"
	}
	_, err := tx.Exec(ctx, `INSERT INTO golf.flight_players (id, property_id, flight_id, customer_id, name, player_type, tee_set_id, reciprocal_visit_id,
		created_by, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9, now() + make_interval(secs => $10))`, id.New(), property, fid, p.CustomerID, name,
		p.PlayerType, p.TeeSetID, p.ReciprocalVisitID, actor(ctx), float64(i)/1000)
	if p.ReciprocalVisitID != nil && err == nil {
		_, err = tx.Exec(ctx, `UPDATE golf.reciprocal_visits SET flight_id = $2 WHERE id = $1`, *p.ReciprocalVisitID, fid)
	}
	return err
}

type CheckInInput struct {
	PlayerIDs []uuid.UUID `json:"playerIds,omitempty" doc:"Default: every booked player"`
	NoCharge  bool        `json:"noCharge,omitempty" doc:"Skip the green fee charge (complimentary)"`
}

// segmentFor maps a player to the pricing segment.
func (m *Module) segmentFor(ctx context.Context, q dbtx.Querier, property uuid.UUID, p Player) (string, string, error) {
	switch p.PlayerType {
	case "member":
		if p.CustomerID != nil {
			seg, err := membership.Segment(ctx, q, property, *p.CustomerID, "golf")
			if err != nil {
				return "", "", err
			}
			if seg != "" {
				return seg, "", nil
			}
		}
		return "member", "", nil
	case "reciprocal":
		var item *string
		_ = q.QueryRow(ctx, `SELECT c.rate_item FROM golf.flight_players p JOIN golf.reciprocal_visits v ON v.id = p.reciprocal_visit_id
			JOIN golf.reciprocal_clubs c ON c.id = v.club_id WHERE p.id = $1`, p.ID).Scan(&item)
		return "reciprocal", deref(item), nil
	case "guest_of_member":
		return "guest_of_member", "", nil
	}
	return "non_member", "", nil
}

// CheckIn checks players in: opens the golf folio and posts the all-in green
// fee (components green fee, buggy, HIO insurance …). The caddy fee component
// is held and posted per caddy when the round ends (EP-05 settlement).
func (m *Module) CheckIn(ctx context.Context, tx pgx.Tx, fid uuid.UUID, in CheckInInput) (Flight, error) {
	f, err := m.lockFlight(ctx, tx, fid)
	if err != nil {
		return f, err
	}
	if f.Status != "booked" && f.Status != "checked_in" {
		return f, errs.Conflict("invalid_status", "flight is "+f.Status)
	}
	rt, err := loadRoute(ctx, tx, f.RouteID)
	if err != nil {
		return f, err
	}
	n := 0
	for _, p := range f.Players {
		if p.Status != "booked" || (len(in.PlayerIDs) > 0 && !slices.Contains(in.PlayerIDs, p.ID)) {
			continue
		}
		seg, item, err := m.segmentFor(ctx, tx, f.PropertyID, p)
		if err != nil {
			return f, err
		}
		folio, err := m.Billing.OpenFolio(ctx, tx, billing.FolioInput{Property: f.PropertyID, BusinessLine: billing.LineGolf,
			CustomerID: p.CustomerID, HolderName: p.Name, SourceType: "golf_round", SourceID: &p.ID})
		if err != nil {
			return f, err
		}
		upd := map[string]any{}
		if !in.NoCharge {
			if item == "" {
				item = rt.Code
			}
			if err := m.chargeGreenFee(ctx, tx, f, p, folio.ID, seg, item, upd); err != nil {
				return f, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.flight_players SET status = 'checked_in', checked_in_at = now(), folio_id = $2, segment = $3,
			hio_insured = coalesce(($4::jsonb->>'hio')::boolean, hio_insured), hio_policy_ref = coalesce($4::jsonb->>'policy', hio_policy_ref),
			caddy_fee_amount = ($4::jsonb->>'caddyFee')::numeric, pricing_snapshot_id = ($4::jsonb->>'snapshot')::uuid WHERE id = $1`,
			p.ID, folio.ID, seg, upd); err != nil {
			return f, err
		}
		if err := m.publish(ctx, tx, "golf.player_checked_in", "golf.flight", f.ID, f.PropertyID, map[string]any{"flightId": f.ID,
			"playerId": p.ID, "customerId": p.CustomerID, "playerType": p.PlayerType, "folioId": folio.ID, "teeTime": f.TeeTime}); err != nil {
			return f, err
		}
		n++
	}
	if n == 0 {
		return f, errs.Conflict("nothing_to_check_in", "no booked player to check in")
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.flights SET status = 'checked_in', updated_by = $2 WHERE id = $1 AND status = 'booked'`, fid, actor(ctx)); err != nil {
		return f, err
	}
	after, err := m.GetFlight(ctx, tx, fid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.flight", fid, f.FlightNo, "check_in", f.PropertyID, f, after, "")
}

func (m *Module) chargeGreenFee(ctx context.Context, tx pgx.Tx, f Flight, p Player, folioID uuid.UUID, seg, item string, upd map[string]any) error {
	pr, err := commercial.Pricer{}.Price(ctx, tx, f.PropertyID, commercial.PriceRequest{ServiceType: "golf", ItemRef: item, Segment: seg,
		Start: f.TeeTime, Channel: "ops"})
	if errs.Is(err, errs.KindNotFound) || errs.Is(err, errs.KindValidation) {
		return nil // no golf rate configured (interim): the folio stays open for manual charges
	}
	if err != nil {
		return err
	}
	if pr.SnapshotID != nil {
		upd["snapshot"] = pr.SnapshotID.String()
	}
	charge := func(comp, desc string, net, svc, tax decimal.Decimal) error {
		if net.Add(svc).Add(tax).IsZero() {
			return nil
		}
		_, err := m.Billing.AddCharge(ctx, tx, billing.Charge{FolioID: folioID, BusinessLine: billing.LineGolf, ReferenceType: "golf.flight_player", ReferenceID: &p.ID,
			RevenueComponent: comp, Description: desc, Net: net, Service: svc, Tax: tax, TaxLines: pr.Tax.Lines, SnapshotID: pr.SnapshotID})
		return err
	}
	if len(pr.Components) == 0 {
		comp := pr.RevenueComponent
		if comp == "" || comp == "other" {
			comp = "green_fee"
		}
		return charge(comp, pr.RuleName, pr.Net(), pr.ServiceAmount(), pr.TaxAmount())
	}
	// All-in price: the caddy fee is a liability held for the caddy (no tax);
	// the rest of the price, with its tax and service, is split pro rata.
	total := pr.Total()
	caddy := decimal.Zero
	others := []commercial.Component{}
	for _, c := range pr.Components {
		amt, _ := decimal.NewFromString(c.Amount)
		if c.RevenueComponent == "caddy_fee" || c.Code == "caddy_fee" {
			caddy = caddy.Add(amt)
			continue
		}
		if c.RevenueComponent == "hio_insurance" || c.Code == "hio_insurance" {
			upd["hio"] = true
			upd["policy"] = "HIO-" + pr.RuleCode + "-" + p.ID.String()[:8]
		}
		others = append(others, c)
	}
	if caddy.IsPositive() {
		upd["caddyFee"] = caddy.String()
	}
	rest := total.Sub(caddy)
	if !total.IsPositive() || len(others) == 0 {
		return nil
	}
	scale := rest.Div(total)
	net, svc, tax := pr.Net().Mul(scale), pr.ServiceAmount().Mul(scale), pr.TaxAmount().Mul(scale)
	var restSum decimal.Decimal
	for _, c := range others {
		amt, _ := decimal.NewFromString(c.Amount)
		restSum = restSum.Add(amt)
	}
	var usedN, usedS, usedT decimal.Decimal
	for i, c := range others {
		amt, _ := decimal.NewFromString(c.Amount)
		share := decimal.Zero
		if restSum.IsPositive() {
			share = amt.Div(restSum)
		}
		cn, cs, ct := net.Mul(share).Round(2), svc.Mul(share).Round(2), tax.Mul(share).Round(2)
		if i == len(others)-1 {
			cn, cs, ct = net.Round(2).Sub(usedN), svc.Round(2).Sub(usedS), tax.Round(2).Sub(usedT)
		}
		usedN, usedS, usedT = usedN.Add(cn), usedS.Add(cs), usedT.Add(ct)
		comp := c.RevenueComponent
		if comp == "" {
			comp = "golf_other"
		}
		if err := charge(comp, c.Name, cn, cs, ct); err != nil {
			return err
		}
	}
	return nil
}

// ── caddy & golf cart assignment ──────────────────────────────────────────

type AssignCaddyInput struct {
	PlayerID *uuid.UUID `json:"playerId,omitempty" doc:"Empty: the caddy serves the whole flight"`
	CaddyID  *uuid.UUID `json:"caddyId,omitempty" doc:"Empty: next caddy by the rotation policy"`
	LevelID  *uuid.UUID `json:"levelId,omitempty" doc:"Rotation per level: requested caddy level"`
}

// AssignCaddy assigns a caddy (C8: golf.caddy_assigned).
func (m *Module) AssignCaddy(ctx context.Context, tx pgx.Tx, fid uuid.UUID, in AssignCaddyInput) (CaddyAssignment, error) {
	f, err := m.lockFlight(ctx, tx, fid)
	if err != nil {
		return CaddyAssignment{}, err
	}
	if f.Status == "finished" || f.Status == "cancelled" || f.Status == "no_show" {
		return CaddyAssignment{}, errs.Conflict("invalid_status", "flight is "+f.Status)
	}
	if in.PlayerID != nil && !slices.ContainsFunc(f.Players, func(p Player) bool { return p.ID == *in.PlayerID }) {
		return CaddyAssignment{}, handle.Invalid("playerId", "not_in_flight", "player is not in this flight")
	}
	for _, a := range f.Caddies {
		if (a.Status == "assigned" || a.Status == "accepted" || a.Status == "active") && ((a.PlayerID == nil && in.PlayerID == nil) ||
			(a.PlayerID != nil && in.PlayerID != nil && *a.PlayerID == *in.PlayerID)) {
			return CaddyAssignment{}, errs.Conflict("already_assigned", "a caddy is already assigned; use Caddy Replacement")
		}
	}
	cid := uuid.Nil
	if in.CaddyID != nil {
		cid = *in.CaddyID
	} else {
		q, err := m.Queue(ctx, tx, f.PropertyID, in.LevelID)
		if err != nil {
			return CaddyAssignment{}, err
		}
		if len(q) == 0 {
			return CaddyAssignment{}, errs.Conflict("no_caddy_available", "no caddy is available in the rotation")
		}
		cid = q[0].CaddyID
	}
	var duty, status, name string
	var user *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT duty_status, status, name, user_id FROM golf.caddies WHERE id = $1 AND property_id = $2 FOR UPDATE`, cid, f.PropertyID).
		Scan(&duty, &status, &name, &user); err != nil {
		if dbtx.IsNoRows(err) {
			return CaddyAssignment{}, errs.NotFound("caddy")
		}
		return CaddyAssignment{}, err
	}
	if status != "active" {
		return CaddyAssignment{}, errs.Conflict("caddy_inactive", "caddy is "+status)
	}
	if duty != "available" {
		return CaddyAssignment{}, errs.Conflict("caddy_unavailable", name+" is not available ("+duty+")")
	}
	aid := id.New()
	st := "assigned"
	if f.Status == "on_course" {
		st = "active"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.caddy_assignments (id, property_id, flight_id, player_id, caddy_id, status, from_seq, started_at, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,greatest($7, 1), CASE WHEN $6 = 'active' THEN now() END, $8)`, aid, f.PropertyID, fid, in.PlayerID, cid, st, f.CurrentSeq, actor(ctx)); err != nil {
		return CaddyAssignment{}, err
	}
	duty = "assigned"
	if st == "active" {
		duty = "on_course"
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.caddies SET duty_status = $2 WHERE id = $1`, cid, duty); err != nil {
		return CaddyAssignment{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.caddy_attendance SET last_assigned_at = now() WHERE caddy_id = $1 AND clock_out IS NULL`, cid); err != nil {
		return CaddyAssignment{}, err
	}
	a, err := m.caddyAssignment(ctx, tx, aid)
	if err != nil {
		return a, err
	}
	if user != nil && m.Notify != nil {
		if err := m.Notify.Send(ctx, tx, notify.Message{Event: "golf.caddy_next_assignment", Category: "golf", UserIDs: []uuid.UUID{*user},
			PropertyID: &f.PropertyID, Channels: []string{notify.ChannelInApp}, Link: "/caddy/assignments",
			Data: map[string]any{"flight": f.FlightNo, "teeTime": f.TeeTime.In(localNow(ctx, tx).Location()).Format("15:04"), "route": f.RouteName}}); err != nil {
			return a, err
		}
	}
	if err := m.publish(ctx, tx, "golf.caddy_assigned", "golf.flight", fid, f.PropertyID, map[string]any{"flightId": fid, "assignmentId": aid,
		"caddyId": cid, "playerId": in.PlayerID}); err != nil {
		return a, err
	}
	if err := m.live(ctx, tx, "golf.caddy", f.PropertyID, "assigned", cid.String(), map[string]any{"flightId": fid}); err != nil {
		return a, err
	}
	return a, record(ctx, tx, "golf.caddy_assignment", aid, f.FlightNo+" · "+name, audit.ActionCreate, f.PropertyID, nil, a, "")
}

func (m *Module) caddyAssignment(ctx context.Context, q dbtx.Querier, aid uuid.UUID) (CaddyAssignment, error) {
	rows, err := q.Query(ctx, caddyAssignmentSelect+` WHERE a.id = $1`, aid)
	return handle.One[CaddyAssignment](rows, err, "caddy assignment")
}

type AssignCartInput struct {
	CartID *uuid.UUID `json:"cartId,omitempty" doc:"Empty: first Ready golf cart"`
}

// AssignCart assigns a Ready golf cart (C8: golf.golf_cart_assigned). Only
// carts that passed their inspection are Ready (FR-CTL-02).
func (m *Module) AssignCart(ctx context.Context, tx pgx.Tx, fid uuid.UUID, in AssignCartInput) (CartAssignment, error) {
	f, err := m.lockFlight(ctx, tx, fid)
	if err != nil {
		return CartAssignment{}, err
	}
	if f.Status == "finished" || f.Status == "cancelled" || f.Status == "no_show" {
		return CartAssignment{}, errs.Conflict("invalid_status", "flight is "+f.Status)
	}
	return m.assignCart(ctx, tx, f, in.CartID, max(f.CurrentSeq, 1), "")
}

func (m *Module) assignCart(ctx context.Context, tx pgx.Tx, f Flight, cartID *uuid.UUID, fromSeq int, reason string) (CartAssignment, error) {
	var cid uuid.UUID
	var code, readiness string
	var err error
	if cartID != nil {
		err = tx.QueryRow(ctx, `SELECT id, code, readiness FROM golf.golf_carts WHERE id = $1 AND property_id = $2 AND status = 'active' FOR UPDATE`,
			*cartID, f.PropertyID).Scan(&cid, &code, &readiness)
	} else {
		err = tx.QueryRow(ctx, `SELECT id, code, readiness FROM golf.golf_carts WHERE property_id = $1 AND status = 'active' AND readiness = 'ready'
			AND archived_at IS NULL ORDER BY usage_hours, code LIMIT 1 FOR UPDATE SKIP LOCKED`, f.PropertyID).Scan(&cid, &code, &readiness)
	}
	if dbtx.IsNoRows(err) {
		if cartID == nil {
			return CartAssignment{}, errs.Conflict("no_cart_ready", "no golf cart is Ready")
		}
		return CartAssignment{}, errs.NotFound("golf cart")
	}
	if err != nil {
		return CartAssignment{}, err
	}
	if readiness != "ready" {
		return CartAssignment{}, errs.Conflict("cart_not_ready", "golf cart "+code+" is not Ready ("+readiness+")")
	}
	aid := id.New()
	st, out := "assigned", (*time.Time)(nil)
	if f.Status == "on_course" {
		now := clock.Now()
		st, out = "in_use", &now
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.cart_assignments (id, property_id, flight_id, cart_id, status, from_seq, out_at, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, aid, f.PropertyID, f.ID, cid, st, fromSeq, out, actor(ctx)); err != nil {
		return CartAssignment{}, err
	}
	if err := m.setReadiness(ctx, tx, f.PropertyID, cid, "in_use", "assigned to "+f.FlightNo); err != nil {
		return CartAssignment{}, err
	}
	rows, err := tx.Query(ctx, cartAssignmentSelect+` WHERE a.id = $1`, aid)
	a, err := handle.One[CartAssignment](rows, err, "golf cart assignment")
	if err != nil {
		return a, err
	}
	if err := m.publish(ctx, tx, "golf.golf_cart_assigned", "golf.flight", f.ID, f.PropertyID, map[string]any{"flightId": f.ID, "assignmentId": aid,
		"cartId": cid}); err != nil {
		return a, err
	}
	return a, record(ctx, tx, "golf.cart_assignment", aid, f.FlightNo+" · "+code, audit.ActionCreate, f.PropertyID, nil, a, reason)
}

// ── round progress (FR-CTB-05) ────────────────────────────────────────────

type RoundEvent struct {
	At       *time.Time `json:"at,omitempty" doc:"Device time of the event (offline queue); default now"`
	DeviceID string     `json:"deviceId,omitempty"`
}

func eventTime(at *time.Time) time.Time {
	if at != nil && !at.IsZero() {
		return at.UTC()
	}
	return clock.Now()
}

// TeeOff starts the round: caddies and golf carts go out, scorecards open.
func (m *Module) TeeOff(ctx context.Context, tx pgx.Tx, fid uuid.UUID, in RoundEvent) (Flight, error) {
	f, err := m.lockFlight(ctx, tx, fid)
	if err != nil {
		return f, err
	}
	if f.Status == "on_course" {
		return f, nil // replayed offline event
	}
	if f.Status != "checked_in" {
		return f, errs.Conflict("not_checked_in", "check the flight in before tee off")
	}
	at := eventTime(in.At)
	holes, err := RouteHoles(ctx, tx, f.RouteID, f.TeeSetID)
	if err != nil {
		return f, err
	}
	if len(holes) == 0 {
		return f, errs.Conflict("route_without_holes", "the playing route has no holes")
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.flights SET status = 'on_course', teed_off_at = $2, current_seq = 1, device_id = coalesce($3, device_id),
		updated_by = $4 WHERE id = $1`, fid, at, nzs(in.DeviceID), actor(ctx)); err != nil {
		return f, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.hole_progress (id, property_id, flight_id, seq, hole_id, started_at, device_id) VALUES ($1,$2,$3,1,$4,$5,$6)
		ON CONFLICT (flight_id, seq) DO NOTHING`, id.New(), f.PropertyID, fid, holes[0].HoleID, at, nzs(in.DeviceID)); err != nil {
		return f, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.caddy_assignments SET status = 'active', started_at = $2 WHERE flight_id = $1 AND status IN ('assigned', 'accepted')`, fid, at); err != nil {
		return f, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.caddies SET duty_status = 'on_course' WHERE id IN (SELECT caddy_id FROM golf.caddy_assignments WHERE flight_id = $1 AND status = 'active')`, fid); err != nil {
		return f, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.cart_assignments SET status = 'in_use', out_at = $2 WHERE flight_id = $1 AND status = 'assigned'`, fid, at); err != nil {
		return f, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.flight_players SET status = 'playing' WHERE flight_id = $1 AND status = 'checked_in'`, fid); err != nil {
		return f, err
	}
	for _, p := range f.Players {
		if p.Status == "checked_in" {
			if err := m.openScorecard(ctx, tx, f, p, holes, at); err != nil {
				return f, err
			}
		}
	}
	if err := m.publish(ctx, tx, "golf.flight_teed_off", "golf.flight", fid, f.PropertyID, map[string]any{"flightId": fid, "teedOffAt": at}); err != nil {
		return f, err
	}
	if err := m.live(ctx, tx, "golf.pace", f.PropertyID, "teed_off", fid.String(), map[string]any{"seq": 1}); err != nil {
		return f, err
	}
	after, err := m.GetFlight(ctx, tx, fid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.flight", fid, f.FlightNo, "tee_off", f.PropertyID, f, after, "")
}

type HoleInput struct {
	Seq      int        `json:"seq" doc:"Hole sequence on the route (1 … 18) now being played"`
	At       *time.Time `json:"at,omitempty"`
	DeviceID string     `json:"deviceId,omitempty"`
}

// RecordHole records hole progress; replays (offline sync) are idempotent.
func (m *Module) RecordHole(ctx context.Context, tx pgx.Tx, fid uuid.UUID, in HoleInput) (Flight, error) {
	f, err := m.lockFlight(ctx, tx, fid)
	if err != nil {
		return f, err
	}
	if f.Status != "on_course" {
		return f, errs.Conflict("not_on_course", "flight is "+f.Status)
	}
	holes, err := RouteHoles(ctx, tx, f.RouteID, f.TeeSetID)
	if err != nil {
		return f, err
	}
	if in.Seq < 1 || in.Seq > len(holes) {
		return f, handle.Invalid("seq", "invalid_seq", "hole sequence out of range")
	}
	at := eventTime(in.At)
	tag, err := tx.Exec(ctx, `INSERT INTO golf.hole_progress (id, property_id, flight_id, seq, hole_id, started_at, device_id, source)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'tablet') ON CONFLICT (flight_id, seq) DO NOTHING`, id.New(), f.PropertyID, fid, in.Seq, holes[in.Seq-1].HoleID, at, nzs(in.DeviceID))
	if err != nil {
		return f, err
	}
	if tag.RowsAffected() == 0 {
		return f, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.hole_progress SET finished_at = $3 WHERE flight_id = $1 AND seq < $2 AND finished_at IS NULL`, fid, in.Seq, at); err != nil {
		return f, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.flights SET current_seq = greatest(current_seq, $2), device_id = coalesce($3, device_id) WHERE id = $1`,
		fid, in.Seq, nzs(in.DeviceID)); err != nil {
		return f, err
	}
	if err := m.live(ctx, tx, "golf.pace", f.PropertyID, "hole", fid.String(), map[string]any{"seq": in.Seq}); err != nil {
		return f, err
	}
	after, err := m.GetFlight(ctx, tx, fid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.flight", fid, f.FlightNo, "hole_progress", f.PropertyID, map[string]any{"seq": f.CurrentSeq},
		map[string]any{"seq": in.Seq, "at": at}, "")
}

// FinishRound completes the round: caddy fees are posted as liability per
// caddy (split on replacement), golf carts return for post-op inspection.
func (m *Module) FinishRound(ctx context.Context, tx pgx.Tx, fid uuid.UUID, in RoundEvent) (Flight, error) {
	f, err := m.lockFlight(ctx, tx, fid)
	if err != nil {
		return f, err
	}
	if f.Status == "finished" {
		return f, nil
	}
	if f.Status != "on_course" {
		return f, errs.Conflict("not_on_course", "flight is "+f.Status)
	}
	at := eventTime(in.At)
	if _, err := tx.Exec(ctx, `UPDATE golf.hole_progress SET finished_at = $2 WHERE flight_id = $1 AND finished_at IS NULL`, fid, at); err != nil {
		return f, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.flights SET status = 'finished', finished_at = $2, updated_by = $3 WHERE id = $1`, fid, at, actor(ctx)); err != nil {
		return f, err
	}
	played := max(f.CurrentSeq, 1)
	if err := m.settleCaddies(ctx, tx, f, played, at); err != nil {
		return f, err
	}
	for _, c := range f.Carts {
		if c.Status == "in_use" || c.Status == "assigned" {
			if err := m.returnCart(ctx, tx, f, c, played, at, "returned"); err != nil {
				return f, err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.flight_players SET status = 'finished' WHERE flight_id = $1 AND status = 'playing'`, fid); err != nil {
		return f, err
	}
	if err := m.publish(ctx, tx, "golf.round_finished", "golf.flight", fid, f.PropertyID, map[string]any{"flightId": fid, "finishedAt": at,
		"holes": played}); err != nil {
		return f, err
	}
	if err := m.live(ctx, tx, "golf.pace", f.PropertyID, "finished", fid.String(), nil); err != nil {
		return f, err
	}
	after, err := m.GetFlight(ctx, tx, fid)
	if err != nil {
		return after, err
	}
	if m.CRM != nil {
		for _, p := range after.Players {
			if p.CustomerID == nil || p.Status != "finished" {
				continue
			}
			var subject *uuid.UUID
			for _, a := range after.Caddies {
				if a.Status == "completed" && (a.PlayerID == nil || *a.PlayerID == p.ID) {
					subject = &a.ID
				}
			}
			req := crm.FeedbackRequest{PropertyID: f.PropertyID, CustomerID: *p.CustomerID, ContextType: "round", ContextID: &p.ID,
				ContextLabel: "Round " + f.FlightNo + " · " + f.RouteName}
			if subject != nil {
				req.SubjectType, req.SubjectID = "golf.caddy_assignment", subject
			}
			if _, err := m.CRM.RequestFeedback(ctx, tx, req); err != nil {
				return after, err
			}
		}
	}
	return after, record(ctx, tx, "golf.flight", fid, f.FlightNo, "finish", f.PropertyID, f, after, "")
}

// settleCaddies posts the caddy fee of every player to the player's folio as
// a liability for the caddy, split by the Caddy Policies on replacement.
func (m *Module) settleCaddies(ctx context.Context, tx pgx.Tx, f Flight, played int, at time.Time) error {
	pol, err := m.caddyPolicy(ctx, tx, f.PropertyID)
	if err != nil {
		return err
	}
	for _, a := range f.Caddies {
		if a.Status == "assigned" || a.Status == "accepted" || a.Status == "active" {
			if _, err := tx.Exec(ctx, `UPDATE golf.caddy_assignments SET status = 'completed', to_seq = $2, ended_at = $3 WHERE id = $1`, a.ID, played, at); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE golf.caddies SET duty_status = 'available' WHERE id = $1 AND duty_status IN ('assigned', 'on_course')`, a.CaddyID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE golf.caddy_attendance SET rounds = rounds + 1 WHERE caddy_id = $1 AND clock_out IS NULL`, a.CaddyID); err != nil {
				return err
			}
		}
	}
	type served struct {
		ID      uuid.UUID  `db:"id"`
		Player  *uuid.UUID `db:"player_id"`
		CaddyID uuid.UUID  `db:"caddy_id"`
		Name    string     `db:"name"`
		From    int        `db:"from_seq"`
		To      int        `db:"to_seq"`
		Level   string     `db:"level_fee"`
	}
	list, err := handle.List[served](tx.Query(ctx, `SELECT a.id, a.player_id, a.caddy_id, c.name, a.from_seq, coalesce(a.to_seq, $2) AS to_seq,
		coalesce(trim_scale(l.fee_amount), 0)::text AS level_fee FROM golf.caddy_assignments a JOIN golf.caddies c ON c.id = a.caddy_id
		LEFT JOIN golf.caddy_levels l ON l.id = c.level_id WHERE a.flight_id = $1 AND a.status IN ('completed', 'replaced') ORDER BY a.from_seq, a.assigned_at`,
		f.ID, played))
	if err != nil || len(list) == 0 {
		return err
	}
	for _, p := range f.Players {
		if p.Status != "playing" && p.Status != "checked_in" {
			continue
		}
		var mine []served
		for _, s := range list {
			if s.Player == nil || *s.Player == p.ID {
				mine = append(mine, s)
			}
		}
		if len(mine) == 0 {
			continue
		}
		var held *string
		if err := tx.QueryRow(ctx, `SELECT trim_scale(caddy_fee_amount)::text FROM golf.flight_players WHERE id = $1`, p.ID).Scan(&held); err != nil {
			return err
		}
		base, _ := decimal.NewFromString(mine[0].Level)
		if held != nil {
			base, _ = decimal.NewFromString(*held)
		}
		if !base.IsPositive() {
			continue
		}
		shares := make([]decimal.Decimal, len(mine))
		switch pol.ReplacementSplit {
		case "first_caddy":
			shares[0] = base
		case "last_caddy":
			shares[len(mine)-1] = base
		default:
			var used decimal.Decimal
			for i, s := range mine {
				holes := decimal.NewFromInt(int64(max(s.To-s.From+1, 0)))
				shares[i] = base.Mul(holes).Div(decimal.NewFromInt(int64(played))).Round(2)
				if i == len(mine)-1 {
					shares[i] = base.Sub(used)
				}
				used = used.Add(shares[i])
			}
		}
		folio := p.FolioID
		if folio == nil {
			fo, err := m.Billing.OpenFolio(ctx, tx, billing.FolioInput{Property: f.PropertyID, BusinessLine: billing.LineGolf,
				CustomerID: p.CustomerID, HolderName: p.Name, SourceType: "golf_round", SourceID: &p.ID})
			if err != nil {
				return err
			}
			folio = &fo.ID
			if _, err := tx.Exec(ctx, `UPDATE golf.flight_players SET folio_id = $2 WHERE id = $1`, p.ID, fo.ID); err != nil {
				return err
			}
		}
		for i, s := range mine {
			if !shares[i].IsPositive() {
				continue
			}
			sid := s.ID
			if _, err := m.Billing.AddCharge(ctx, tx, billing.Charge{FolioID: *folio, BusinessLine: billing.LineGolf, ReferenceType: "golf.caddy_assignment",
				ReferenceID: &sid, RevenueComponent: "caddy_fee", Description: "Caddy fee · " + s.Name, Net: shares[i], Liability: true,
				BeneficiaryType: "caddy", BeneficiaryID: &s.CaddyID}); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE golf.caddy_assignments SET fee_amount = fee_amount + $2::numeric WHERE id = $1`, s.ID, shares[i].String()); err != nil {
				return err
			}
		}
	}
	return nil
}

type ReplaceInput struct {
	NewID  *uuid.UUID `json:"newId,omitempty" doc:"Replacement caddy / golf cart; empty: next in rotation / first Ready cart"`
	Reason string     `json:"reason"`
	AtSeq  int        `json:"atSeq,omitempty" doc:"First hole served by the replacement; default the current hole"`
}

// ReplaceCaddy swaps the caddy mid-round; both caddies keep the holes they
// served (FR-CDL-05).
func (m *Module) ReplaceCaddy(ctx context.Context, tx pgx.Tx, aid uuid.UUID, in ReplaceInput) (Flight, error) {
	if err := handle.Required("reason", in.Reason); err != nil {
		return Flight{}, err
	}
	a, err := m.caddyAssignment(ctx, tx, aid)
	if err != nil {
		return Flight{}, err
	}
	f, err := m.lockFlight(ctx, tx, a.FlightID)
	if err != nil {
		return f, err
	}
	if a.Status != "assigned" && a.Status != "accepted" && a.Status != "active" {
		return f, errs.Conflict("not_current", "only the current caddy can be replaced")
	}
	at := in.AtSeq
	if at == 0 {
		at = max(f.CurrentSeq, 1)
	}
	if at < a.FromSeq {
		return f, handle.Invalid("atSeq", "invalid_seq", "replacement hole is before the caddy started")
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.caddy_assignments SET status = 'replaced', to_seq = $2, ended_at = now(), replace_reason = $3 WHERE id = $1`,
		aid, at-1, in.Reason); err != nil {
		return f, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.caddies SET duty_status = 'available' WHERE id = $1`, a.CaddyID); err != nil {
		return f, err
	}
	if at-1 < a.FromSeq {
		// replaced before serving a hole: no share
		if _, err := tx.Exec(ctx, `UPDATE golf.caddy_assignments SET status = 'cancelled' WHERE id = $1`, aid); err != nil {
			return f, err
		}
	}
	saved := f.CurrentSeq
	f.CurrentSeq = at
	na, err := m.assignCaddyAt(ctx, tx, f, a.PlayerID, in.NewID)
	f.CurrentSeq = saved
	if err != nil {
		return f, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.caddy_assignments SET replaced_by = $2 WHERE id = $1`, aid, na); err != nil {
		return f, err
	}
	after, err := m.GetFlight(ctx, tx, f.ID)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.caddy_assignment", aid, f.FlightNo+" · "+a.CaddyName, "replace", f.PropertyID, a,
		map[string]any{"replacementAssignmentId": na, "atSeq": at}, in.Reason)
}

func (m *Module) assignCaddyAt(ctx context.Context, tx pgx.Tx, f Flight, player *uuid.UUID, caddy *uuid.UUID) (uuid.UUID, error) {
	cid := uuid.Nil
	if caddy != nil {
		cid = *caddy
	} else {
		q, err := m.Queue(ctx, tx, f.PropertyID, nil)
		if err != nil {
			return cid, err
		}
		if len(q) == 0 {
			return cid, errs.Conflict("no_caddy_available", "no caddy is available in the rotation")
		}
		cid = q[0].CaddyID
	}
	var duty, name string
	if err := tx.QueryRow(ctx, `SELECT duty_status, name FROM golf.caddies WHERE id = $1 AND property_id = $2 AND status = 'active' FOR UPDATE`, cid, f.PropertyID).
		Scan(&duty, &name); err != nil {
		if dbtx.IsNoRows(err) {
			return cid, errs.NotFound("caddy")
		}
		return cid, err
	}
	if duty != "available" {
		return cid, errs.Conflict("caddy_unavailable", name+" is not available ("+duty+")")
	}
	st, d := "assigned", "assigned"
	if f.Status == "on_course" {
		st, d = "active", "on_course"
	}
	aid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO golf.caddy_assignments (id, property_id, flight_id, player_id, caddy_id, status, from_seq, started_at, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7, CASE WHEN $6 = 'active' THEN now() END, $8)`, aid, f.PropertyID, f.ID, player, cid, st, max(f.CurrentSeq, 1), actor(ctx)); err != nil {
		return aid, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.caddies SET duty_status = $2 WHERE id = $1`, cid, d); err != nil {
		return aid, err
	}
	return aid, m.publish(ctx, tx, "golf.caddy_assigned", "golf.flight", f.ID, f.PropertyID, map[string]any{"flightId": f.ID, "assignmentId": aid,
		"caddyId": cid, "playerId": player, "replacement": true})
}

// ReplaceCart swaps the golf cart mid-round (FR-CTL-05). The replaced cart
// returns for inspection.
func (m *Module) ReplaceCart(ctx context.Context, tx pgx.Tx, aid uuid.UUID, in ReplaceInput) (Flight, error) {
	if err := handle.Required("reason", in.Reason); err != nil {
		return Flight{}, err
	}
	rows, err := tx.Query(ctx, cartAssignmentSelect+` WHERE a.id = $1`, aid)
	a, err := handle.One[CartAssignment](rows, err, "golf cart assignment")
	if err != nil {
		return Flight{}, err
	}
	f, err := m.lockFlight(ctx, tx, a.FlightID)
	if err != nil {
		return f, err
	}
	if a.Status != "assigned" && a.Status != "in_use" {
		return f, errs.Conflict("not_current", "only the current golf cart can be replaced")
	}
	at := in.AtSeq
	if at == 0 {
		at = max(f.CurrentSeq, 1)
	}
	if err := m.returnCart(ctx, tx, f, a, at-1, clock.Now(), "replaced"); err != nil {
		return f, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.cart_assignments SET replace_reason = $2 WHERE id = $1`, aid, in.Reason); err != nil {
		return f, err
	}
	na, err := m.assignCart(ctx, tx, f, in.NewID, at, in.Reason)
	if err != nil {
		return f, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.cart_assignments SET replaced_by = $2 WHERE id = $1`, aid, na.ID); err != nil {
		return f, err
	}
	after, err := m.GetFlight(ctx, tx, f.ID)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.cart_assignment", aid, f.FlightNo+" · "+a.CartCode, "replace", f.PropertyID, a,
		map[string]any{"replacementAssignmentId": na.ID, "atSeq": at}, in.Reason)
}

// returnCart ends a cart assignment, accumulates usage hours and sends the
// cart to post-operation inspection.
func (m *Module) returnCart(ctx context.Context, tx pgx.Tx, f Flight, a CartAssignment, toSeq int, at time.Time, status string) error {
	hours := decimal.Zero
	if a.OutAt != nil && at.After(*a.OutAt) {
		hours = decimal.NewFromFloat(at.Sub(*a.OutAt).Hours()).Round(2)
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.cart_assignments SET status = $2, to_seq = $3, returned_at = $4, usage_hours = $5::numeric WHERE id = $1`,
		a.ID, status, max(toSeq, 0), at, hours.String()); err != nil {
		return err
	}
	var code string
	var since, threshold decimal.Decimal
	var alerted *time.Time
	var sinceS, thrS string
	if err := tx.QueryRow(ctx, `UPDATE golf.golf_carts SET usage_hours = usage_hours + $2::numeric, hours_since_service = hours_since_service + $2::numeric
		WHERE id = $1 RETURNING code, hours_since_service::text, service_threshold_hours::text, service_alerted_at`, a.CartID, hours.String()).
		Scan(&code, &sinceS, &thrS, &alerted); err != nil {
		return err
	}
	since, _ = decimal.NewFromString(sinceS)
	threshold, _ = decimal.NewFromString(thrS)
	if err := m.setReadiness(ctx, tx, f.PropertyID, a.CartID, "under_inspection", "returned from "+f.FlightNo); err != nil {
		return err
	}
	// Usage hours past the service threshold notify the Golf Manager (FR-CTL-04).
	if since.GreaterThanOrEqual(threshold) && alerted == nil {
		if _, err := tx.Exec(ctx, `UPDATE golf.golf_carts SET service_alerted_at = now() WHERE id = $1`, a.CartID); err != nil {
			return err
		}
		if m.Notify != nil {
			users, err := notify.Holders(ctx, tx, f.PropertyID, "golf.cart_maintenance.manage")
			if err != nil {
				return err
			}
			if len(users) > 0 {
				if err := m.Notify.Send(ctx, tx, notify.Message{Event: "golf.cart_service_due", Category: "golf", UserIDs: users, PropertyID: &f.PropertyID,
					Link: "/golf/golf-carts/" + a.CartID.String(), Data: map[string]any{"cart": code, "hours": since.String(), "threshold": threshold.String()}}); err != nil {
					return err
				}
			}
		}
		if err := m.publish(ctx, tx, "golf.cart_service_due", "golf.golf_cart", a.CartID, f.PropertyID, map[string]any{"cartId": a.CartID,
			"hoursSinceService": since.String()}); err != nil {
			return err
		}
	}
	return nil
}

type TipInput struct {
	Amount   string     `json:"amount"`
	PlayerID *uuid.UUID `json:"playerId,omitempty" doc:"Folio of this player; default the assignment's player"`
}

// Tip records a non-cash tip for the caddy on the player's folio (FR-CDL-10).
func (m *Module) Tip(ctx context.Context, tx pgx.Tx, aid uuid.UUID, in TipInput) (billing.FolioLine, error) {
	amt, err := handle.Decimal("amount", in.Amount, decimal.Zero)
	if err != nil {
		return billing.FolioLine{}, err
	}
	if !amt.IsPositive() {
		return billing.FolioLine{}, handle.Invalid("amount", "invalid_amount", "tip must be positive")
	}
	a, err := m.caddyAssignment(ctx, tx, aid)
	if err != nil {
		return billing.FolioLine{}, err
	}
	player := in.PlayerID
	if player == nil {
		player = a.PlayerID
	}
	var folio *uuid.UUID
	q := `SELECT folio_id FROM golf.flight_players WHERE flight_id = $1 AND ($2::uuid IS NULL OR id = $2) AND folio_id IS NOT NULL ORDER BY created_at LIMIT 1`
	if err := tx.QueryRow(ctx, q, a.FlightID, player).Scan(&folio); err != nil {
		if dbtx.IsNoRows(err) {
			return billing.FolioLine{}, errs.Conflict("no_folio", "the player has no folio; check the player in first")
		}
		return billing.FolioLine{}, err
	}
	return m.Billing.AddCharge(ctx, tx, billing.Charge{FolioID: *folio, BusinessLine: billing.LineGolf, ReferenceType: "golf.caddy_assignment", ReferenceID: &aid,
		RevenueComponent: "caddy_tip", Description: "Caddy tip · " + a.CaddyName, Net: amt, Liability: true, BeneficiaryType: "caddy", BeneficiaryID: &a.CaddyID})
}

// CancelFlight cancels a booked flight and releases its caddies and carts.
func (m *Module) CancelFlight(ctx context.Context, tx pgx.Tx, fid uuid.UUID, reason string, noShow bool) (Flight, error) {
	f, err := m.lockFlight(ctx, tx, fid)
	if err != nil {
		return f, err
	}
	if f.Status != "booked" && f.Status != "checked_in" {
		return f, errs.Conflict("invalid_status", "flight is "+f.Status)
	}
	st := "cancelled"
	if noShow {
		st = "no_show"
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.flights SET status = $2 WHERE id = $1`, fid, st); err != nil {
		return f, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.flight_players SET status = $2 WHERE flight_id = $1 AND status IN ('booked', 'checked_in')`, fid, st); err != nil {
		return f, err
	}
	for _, a := range f.Caddies {
		if a.Status == "assigned" || a.Status == "accepted" {
			if _, err := tx.Exec(ctx, `UPDATE golf.caddy_assignments SET status = 'cancelled' WHERE id = $1`, a.ID); err != nil {
				return f, err
			}
			if _, err := tx.Exec(ctx, `UPDATE golf.caddies SET duty_status = 'available' WHERE id = $1 AND duty_status = 'assigned'`, a.CaddyID); err != nil {
				return f, err
			}
		}
	}
	for _, c := range f.Carts {
		if c.Status == "assigned" {
			if _, err := tx.Exec(ctx, `UPDATE golf.cart_assignments SET status = 'cancelled' WHERE id = $1`, c.ID); err != nil {
				return f, err
			}
			if err := m.setReadiness(ctx, tx, f.PropertyID, c.CartID, "ready", "assignment cancelled"); err != nil {
				return f, err
			}
		}
	}
	after, err := m.GetFlight(ctx, tx, fid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.flight", fid, f.FlightNo, audit.ActionStatusChange, f.PropertyID, f, after, reason)
}

func jsonOf(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
