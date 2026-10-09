package experience

// Golf Cart Full Lifecycle (PRD P2 EP-07) on P1's readiness states (no new
// state, PRD P2 §6 #11):
//
//	Release/Pre-op Inspection (pass) → Ready → Assignment → In Use → Return
//	(P1: Not Ready / Charging) → Post-Operation Inspection → pass: stays
//	Not Ready / Charging → (pre-op) Ready; fail: Maintenance → Release → Ready
//
// Readiness changes through P1's SetReadiness (contract C8); service hours,
// battery and GPS are kept in the P2 cart profile.

import (
	"context"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/golf"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// setReadiness changes the readiness through P1 (golf cart board, events
// and audit stay P1's) and notifies the P2 readiness screen (FR-CTL-07).
func (m *Module) setReadiness(ctx context.Context, tx pgx.Tx, property, cart uuid.UUID, readiness, reason string) error {
	var before string
	if err := tx.QueryRow(ctx, `SELECT readiness FROM golf.golf_carts WHERE id = $1`, cart).Scan(&before); err != nil {
		return err
	}
	if before == readiness {
		return nil
	}
	if _, err := m.Golf.SetReadiness(ctx, tx, property, cart, golf.ReadinessRequest{Readiness: readiness, Reason: reason}); err != nil {
		return err
	}
	return m.live(ctx, tx, "golf.cart", property, "readiness", cart.String(), map[string]any{"from": before, "to": readiness})
}

type CheckResult struct {
	Item string `json:"item"`
	Pass bool   `json:"pass"`
	Note string `json:"note,omitempty"`
}

type InspectionInput struct {
	Kind           string        `json:"kind" enum:"pre_op,post_op,release"`
	ChecklistID    *uuid.UUID    `json:"checklistId,omitempty" doc:"Default: the active checklist of the cart type and kind"`
	Results        []CheckResult `json:"results"`
	Photos         []string      `json:"photos,omitempty"`
	HourMeter      string        `json:"hourMeter,omitempty"`
	BatteryPercent *int          `json:"batteryPercent,omitempty"`
	Notes          string        `json:"notes,omitempty" doc:"Maintenance description when the inspection fails"`
}

// Inspection is a recorded golf cart inspection.
type Inspection struct {
	ID             uuid.UUID     `json:"id" db:"id"`
	GolfCartID     uuid.UUID     `json:"golfCartId" db:"golf_cart_id"`
	Kind           string        `json:"kind" db:"inspection_kind"`
	ChecklistID    *uuid.UUID    `json:"checklistId" db:"checklist_id"`
	Results        []CheckResult `json:"results" db:"results"`
	Photos         []string      `json:"photos" db:"photos"`
	Passed         bool          `json:"passed" db:"passed"`
	HourMeter      *string       `json:"hourMeter" db:"hour_meter"`
	BatteryPercent *int          `json:"batteryPercent" db:"battery_percent"`
	ReadinessAfter string        `json:"readinessAfter" db:"readiness_after"`
	MaintenanceID  *uuid.UUID    `json:"maintenanceId" db:"maintenance_id"`
	InspectedAt    time.Time     `json:"inspectedAt" db:"inspected_at"`
}

const inspectionSelect = `SELECT id, golf_cart_id, inspection_kind, checklist_id, results, photos, passed, trim_scale(hour_meter)::text AS hour_meter,
	battery_percent, readiness_after, maintenance_id, inspected_at FROM golf.cart_inspections`

// Inspect records an inspection and moves the readiness (FR-CTL-01/02).
func (m *Module) Inspect(ctx context.Context, tx pgx.Tx, property, cart uuid.UUID, in InspectionInput) (Inspection, error) {
	var code, readiness, cartType string
	if err := tx.QueryRow(ctx, `SELECT code, readiness, cart_type FROM golf.golf_carts WHERE id = $1 AND property_id = $2 FOR UPDATE`, cart, property).
		Scan(&code, &readiness, &cartType); err != nil {
		if dbtx.IsNoRows(err) {
			return Inspection{}, errs.NotFound("golf cart")
		}
		return Inspection{}, err
	}
	allowed := map[string][]string{
		"pre_op":  {"not_ready", "charging", "ready"},
		"post_op": {"not_ready", "charging"},
		"release": {"maintenance"},
	}
	from, ok := allowed[in.Kind]
	if !ok {
		return Inspection{}, handle.Invalid("kind", "invalid_kind", "pre_op, post_op or release")
	}
	if !slices.Contains(from, readiness) {
		return Inspection{}, errs.Conflict("invalid_readiness", "a "+in.Kind+" inspection is not possible while the cart is "+readiness)
	}
	if in.Kind == "pre_op" && readiness != "ready" {
		// A returned cart needs its post-operation inspection first.
		var returned bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.golf_cart_assignments a WHERE a.golf_cart_id = $1 AND a.returned_at IS NOT NULL
			AND a.returned_at > coalesce((SELECT max(inspected_at) FROM golf.cart_inspections i WHERE i.golf_cart_id = $1), '-infinity'))`, cart).Scan(&returned); err != nil {
			return Inspection{}, err
		}
		if returned {
			return Inspection{}, errs.Conflict("post_op_required", "the cart returned from a round: do the post-operation inspection first")
		}
	}
	// Checklist: every item must be answered.
	var items []string
	if in.ChecklistID != nil {
		if err := tx.QueryRow(ctx, `SELECT items FROM golf.cart_checklists WHERE id = $1 AND property_id = $2`, *in.ChecklistID, property).Scan(&items); err != nil {
			if dbtx.IsNoRows(err) {
				return Inspection{}, errs.NotFound("checklist")
			}
			return Inspection{}, err
		}
	} else {
		err := tx.QueryRow(ctx, `SELECT id, items FROM golf.cart_checklists WHERE property_id = $1 AND inspection_kind = $2 AND cart_type = $3
			AND status = 'active' AND archived_at IS NULL ORDER BY created_at DESC LIMIT 1`, property, in.Kind, cartType).Scan(&in.ChecklistID, &items)
		if err != nil && !dbtx.IsNoRows(err) {
			return Inspection{}, err
		}
	}
	if len(in.Results) == 0 {
		return Inspection{}, handle.Invalid("results", "required", "inspection results are required")
	}
	answered := map[string]bool{}
	passed := true
	for _, r := range in.Results {
		answered[r.Item] = true
		passed = passed && r.Pass
	}
	for _, it := range items {
		if !answered[it] {
			return Inspection{}, handle.Invalid("results", "incomplete", "checklist item not answered: "+it)
		}
	}
	hm, err := handle.Decimal("hourMeter", in.HourMeter, decimal.Zero)
	if err != nil {
		return Inspection{}, err
	}
	pol, err := m.cartPolicy(ctx, tx, property)
	if err != nil {
		return Inspection{}, err
	}
	if passed && in.Kind != "post_op" && in.BatteryPercent != nil && *in.BatteryPercent < pol.MinBatteryForReady {
		passed = false
		in.Results = append(in.Results, CheckResult{Item: "Battery level", Pass: false, Note: "below the Golf Cart Policies minimum"})
	}
	var after string
	var maint *uuid.UUID
	switch {
	case in.Kind == "post_op" && passed:
		after = readiness // stays Not Ready / Charging until the pre-op inspection
	case passed: // pre_op or release
		after = "ready"
	case in.Kind == "release":
		after = "maintenance" // still failing: stays in maintenance
	default:
		after = "maintenance"
	}
	iid := id.New()
	if !passed && in.Kind != "release" {
		desc := in.Notes
		if desc == "" {
			desc = "Failed " + in.Kind + " inspection"
			for _, r := range in.Results {
				if !r.Pass {
					desc += " · " + r.Item
				}
			}
		}
		mid, err := m.openMaintenance(ctx, tx, property, cart, "repair", desc, decimal.Zero)
		if err != nil {
			return Inspection{}, err
		}
		maint = &mid
	}
	if in.Photos == nil {
		in.Photos = []string{}
	}
	var hmp *string
	if !hm.IsZero() {
		s := hm.String()
		hmp = &s
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.cart_inspections (id, property_id, golf_cart_id, inspection_kind, checklist_id, results, photos, passed, hour_meter,
		battery_percent, readiness_after, maintenance_id, inspected_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10,$11,$12,$13)`, iid, property, cart,
		in.Kind, in.ChecklistID, jsonOf(in.Results), in.Photos, passed, hmp, in.BatteryPercent, after, maint, actorPtr(ctx)); err != nil {
		return Inspection{}, err
	}
	if in.BatteryPercent != nil {
		if _, err := tx.Exec(ctx, `INSERT INTO golf.cart_profiles (golf_cart_id, property_id, battery_percent) VALUES ($1,$3,$2)
			ON CONFLICT (golf_cart_id) DO UPDATE SET battery_percent = EXCLUDED.battery_percent`, cart, *in.BatteryPercent, property); err != nil {
			return Inspection{}, err
		}
	}
	if in.Kind == "release" && passed {
		// Maintenance release closes the open maintenance record(s).
		var scheduled bool
		if err := tx.QueryRow(ctx, `WITH c AS (UPDATE golf.cart_maintenance SET status = 'closed', closed_at = now(), closed_by = $3, release_inspection_id = $2
			WHERE golf_cart_id = $1 AND status = 'open' RETURNING category) SELECT EXISTS (SELECT 1 FROM c WHERE category = 'scheduled_service')`,
			cart, iid, actorPtr(ctx)).Scan(&scheduled); err != nil {
			return Inspection{}, err
		}
		if scheduled {
			if _, err := tx.Exec(ctx, `INSERT INTO golf.cart_profiles (golf_cart_id, property_id, last_service_at) VALUES ($1,$2,now())
				ON CONFLICT (golf_cart_id) DO UPDATE SET last_service_at = now(), service_alerted_at = NULL`, cart, property); err != nil {
				return Inspection{}, err
			}
		}
	}
	if err := m.setReadiness(ctx, tx, property, cart, after, in.Kind+" inspection"); err != nil {
		return Inspection{}, err
	}
	insp, err := handle.Get[Inspection](tx.Query(ctx, inspectionSelect+` WHERE id = $1`, iid))
	if err != nil {
		return insp, err
	}
	return insp, record(ctx, tx, "golf.cart_inspection", iid, code+" "+in.Kind, audit.ActionCreate, property, map[string]any{"readiness": readiness}, insp, "")
}

// ── maintenance (FR-CTL-04) ───────────────────────────────────────────────

type MaintenanceInput struct {
	GolfCartID  uuid.UUID `json:"golfCartId"`
	Category    string    `json:"category" enum:"repair,scheduled_service,battery,tyre,body,other"`
	Description string    `json:"description"`
	Cost        string    `json:"cost,omitempty"`
}

type MaintenanceUpdate struct {
	Cost  string `json:"cost,omitempty"`
	Notes string `json:"notes,omitempty"`
}

// Maintenance is a golf cart maintenance record.
type Maintenance struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	Number      string     `json:"number" db:"number"`
	GolfCartID  uuid.UUID  `json:"golfCartId" db:"golf_cart_id"`
	CartCode    string     `json:"golfCartCode" db:"cart_code"`
	Category    string     `json:"category" db:"category"`
	Description string     `json:"description" db:"description"`
	Cost        string     `json:"cost" db:"cost"`
	Status      string     `json:"status" db:"status" enum:"open,closed"`
	OpenedAt    time.Time  `json:"openedAt" db:"opened_at"`
	ClosedAt    *time.Time `json:"closedAt" db:"closed_at"`
	ReleaseID   *uuid.UUID `json:"releaseInspectionId" db:"release_inspection_id"`
	Notes       *string    `json:"notes" db:"notes"`
}

const maintenanceSelect = `SELECT m.id, m.number, m.golf_cart_id, g.code AS cart_code, m.category, m.description, trim_scale(m.cost)::text AS cost,
	m.status, m.opened_at, m.closed_at, m.release_inspection_id, m.notes FROM golf.cart_maintenance m JOIN golf.golf_carts g ON g.id = m.golf_cart_id`

func (m *Module) openMaintenance(ctx context.Context, tx pgx.Tx, property, cart uuid.UUID, category, desc string, cost decimal.Decimal) (uuid.UUID, error) {
	no, err := number(ctx, tx, property, "MNT")
	if err != nil {
		return uuid.Nil, err
	}
	mid := id.New()
	_, err = tx.Exec(ctx, `INSERT INTO golf.cart_maintenance (id, property_id, golf_cart_id, number, category, description, cost, opened_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8)`, mid, property, cart, no, category, desc, cost.String(), actorPtr(ctx))
	return mid, err
}

// OpenMaintenance puts a cart in Maintenance (e.g. scheduled service).
func (m *Module) OpenMaintenance(ctx context.Context, tx pgx.Tx, property uuid.UUID, in MaintenanceInput) (Maintenance, error) {
	if err := handle.Required("description", in.Description); err != nil {
		return Maintenance{}, err
	}
	cost, err := handle.Decimal("cost", in.Cost, decimal.Zero)
	if err != nil {
		return Maintenance{}, err
	}
	var readiness string
	if err := tx.QueryRow(ctx, `SELECT readiness FROM golf.golf_carts WHERE id = $1 AND property_id = $2 FOR UPDATE`, in.GolfCartID, property).Scan(&readiness); err != nil {
		if dbtx.IsNoRows(err) {
			return Maintenance{}, errs.NotFound("golf cart")
		}
		return Maintenance{}, err
	}
	if readiness == "in_use" {
		return Maintenance{}, errs.Conflict("cart_in_use", "the cart is in use; replace it first")
	}
	if in.Category == "" {
		in.Category = "repair"
	}
	mid, err := m.openMaintenance(ctx, tx, property, in.GolfCartID, in.Category, in.Description, cost)
	if err != nil {
		return Maintenance{}, err
	}
	if err := m.setReadiness(ctx, tx, property, in.GolfCartID, "maintenance", in.Description); err != nil {
		return Maintenance{}, err
	}
	mt, err := handle.Get[Maintenance](tx.Query(ctx, maintenanceSelect+` WHERE m.id = $1`, mid))
	if err != nil {
		return mt, err
	}
	return mt, record(ctx, tx, "golf.cart_maintenance", mid, mt.Number, audit.ActionCreate, property, nil, mt, "")
}

// UpdateMaintenance records cost and notes (closing happens by release inspection).
func (m *Module) UpdateMaintenance(ctx context.Context, tx pgx.Tx, property, mid uuid.UUID, in MaintenanceUpdate) (Maintenance, error) {
	before, err := handle.Get[Maintenance](tx.Query(ctx, maintenanceSelect+` WHERE m.id = $1 AND m.property_id = $2`, mid, property))
	if err != nil {
		return before, err
	}
	cost, err := handle.Decimal("cost", in.Cost, decimal.RequireFromString(before.Cost))
	if err != nil {
		return before, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.cart_maintenance SET cost = $2::numeric, notes = coalesce($3, notes) WHERE id = $1`, mid, cost.String(), nullStr(in.Notes)); err != nil {
		return before, err
	}
	after, err := handle.Get[Maintenance](tx.Query(ctx, maintenanceSelect+` WHERE m.id = $1`, mid))
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.cart_maintenance", mid, after.Number, audit.ActionUpdate, property, before, after, "")
}

// ── readiness board (FR-CTL-07) ───────────────────────────────────────────

type BoardCart struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	Code              string     `json:"code" db:"code"`
	Name              string     `json:"name" db:"name"`
	Readiness         string     `json:"readiness" db:"readiness"`
	BatteryPercent    *int       `json:"batteryPercent" db:"battery_percent"`
	HoursSinceService string     `json:"hoursSinceService" db:"hours_since_service"`
	ServiceDue        bool       `json:"serviceDue" db:"service_due"`
	BookingCode       *string    `json:"bookingCode" db:"booking_code" doc:"Round the cart is assigned to"`
	Lat               *float64   `json:"lat" db:"last_lat"`
	Lng               *float64   `json:"lng" db:"last_lng"`
	PositionAt        *time.Time `json:"positionAt" db:"position_at"`
}

type Board struct {
	Counts map[string]int `json:"counts"`
	Carts  []BoardCart    `json:"golfCarts"`
}

func (m *Module) ReadinessBoard(ctx context.Context, q dbtx.Querier, property uuid.UUID) (Board, error) {
	pol, err := m.cartPolicy(ctx, q, property)
	if err != nil {
		return Board{}, err
	}
	carts, err := handle.List[BoardCart](q.Query(ctx, `SELECT g.id, g.code, g.name, g.readiness, cp.battery_percent,
		trim_scale(round(`+cartServiceHours+`::numeric, 2))::text AS hours_since_service,
		`+cartServiceHours+` >= coalesce(cp.service_threshold_hours, nullif($2, '')::numeric, 'Infinity'::numeric) AS service_due,
		(SELECT coalesce(b.code, 'Flight ' || f.flight_no) FROM golf.golf_cart_assignments a JOIN golf.flights f ON f.id = a.flight_id
		  LEFT JOIN golf.bookings b ON b.id = f.booking_id WHERE a.golf_cart_id = g.id AND a.status IN ('assigned', 'in_use') LIMIT 1) AS booking_code,
		cp.last_lat, cp.last_lng, cp.position_at FROM golf.golf_carts g LEFT JOIN golf.cart_profiles cp ON cp.golf_cart_id = g.id WHERE g.property_id = $1 AND g.status = 'active' AND g.archived_at IS NULL ORDER BY g.code`,
		property, pol.DefaultServiceHours))
	b := Board{Counts: map[string]int{}, Carts: carts}
	for _, c := range carts {
		b.Counts[c.Readiness]++
	}
	return b, err
}

// ── GPS (FR-PLX-02, FR-PLX-05, FR-INT-P2-02) ──────────────────────────────

// Position is a GPS fix of a golf cart.
type Position struct {
	GolfCartID uuid.UUID  `json:"golfCartId"`
	DeviceID   string     `json:"deviceId,omitempty"`
	FlightID   *uuid.UUID `json:"flightId,omitempty" doc:"The flight using the cart"`
	Lat        float64    `json:"lat"`
	Lng        float64    `json:"lng"`
	Battery    *int       `json:"batteryPercent,omitempty"`
	At         time.Time  `json:"at"`
	Source     string     `json:"source,omitempty" enum:"tablet,estimated" doc:"tablet: the caddy tablet's GPS; estimated: the green of the hole being played (no fix in the last 10 minutes)"`
}

// GPSAdapter is the golf cart GPS vendor interface (vendor: OQ #8).
type GPSAdapter interface {
	Positions(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]Position, error)
}

// TabletGPS (demo feedback 9 Oct 2026, OneClub replaces Smartscore): the
// caddy tablet in the cart's bracket is the cart's GPS — no GPS vendor and
// no device registered per cart. A cart out on the course shows the last
// fix its caddy's tablet sent within FreshFix; without one, the green of
// the hole its flight is playing (estimated).
type TabletGPS struct{}

// FreshFix is how long a tablet fix places the cart.
const FreshFix = 10 * time.Minute

func (TabletGPS) Positions(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]Position, error) {
	type row struct {
		CartID   uuid.UUID      `db:"golf_cart_id"`
		FlightID uuid.UUID      `db:"flight_id"`
		Device   *string        `db:"gps_device_id"`
		Lat      *float64       `db:"last_lat"`
		Lng      *float64       `db:"last_lng"`
		At       *time.Time     `db:"position_at"`
		Battery  *int           `db:"battery_percent"`
		Geometry map[string]any `db:"geometry"`
	}
	rows, err := handle.List[row](q.Query(ctx, `SELECT a.golf_cart_id, a.flight_id, cp.gps_device_id, cp.last_lat::float8 AS last_lat, cp.last_lng::float8 AS last_lng,
		cp.position_at, cp.battery_percent, coalesce(ca.geometry, '{}'::jsonb) AS geometry
		FROM golf.golf_cart_assignments a LEFT JOIN golf.cart_profiles cp ON cp.golf_cart_id = a.golf_cart_id JOIN golf.flights f ON f.id = a.flight_id
		LEFT JOIN golf.round_progress rp ON rp.flight_id = f.id LEFT JOIN golf.hole_progress p ON p.flight_id = f.id AND p.seq = rp.current_seq
		LEFT JOIN golf.course_assets ca ON ca.hole_id = p.hole_id AND ca.asset_type = 'green_center' AND ca.status = 'active'
		WHERE a.property_id = $1 AND (a.status = 'in_use' OR (a.status = 'assigned' AND f.tee_off_at IS NOT NULL AND f.round_finish_at IS NULL))`, property))
	if err != nil {
		return nil, err
	}
	now := clock.Now()
	out := []Position{}
	for _, r := range rows {
		fid := r.FlightID
		if r.Lat != nil && r.Lng != nil && r.At != nil && now.Sub(*r.At) <= FreshFix {
			out = append(out, Position{GolfCartID: r.CartID, DeviceID: deref(r.Device), FlightID: &fid, Lat: *r.Lat, Lng: *r.Lng, Battery: r.Battery, At: *r.At,
				Source: "tablet"})
			continue
		}
		lat, lng, ok := geoPoint(r.Geometry)
		if !ok {
			continue
		}
		out = append(out, Position{GolfCartID: r.CartID, DeviceID: deref(r.Device), FlightID: &fid, Lat: lat, Lng: lng, Battery: r.Battery, At: now.UTC(),
			Source: "estimated"})
	}
	return out, nil
}

// geoPoint reads a GeoJSON Point ([lng, lat]) or the first vertex of a
// Polygon of a P1 course asset.
func geoPoint(g map[string]any) (float64, float64, bool) {
	c := g["coordinates"]
	if g["type"] == "Polygon" {
		rings, _ := c.([]any)
		if len(rings) == 0 {
			return 0, 0, false
		}
		ring, _ := rings[0].([]any)
		if len(ring) == 0 {
			return 0, 0, false
		}
		c = ring[0]
	}
	a, ok := c.([]any)
	if !ok || len(a) < 2 {
		return 0, 0, false
	}
	lng, ok1 := a[0].(float64)
	lat, ok2 := a[1].(float64)
	return lat, lng, ok1 && ok2
}

// IngestPositions stores fixes pushed by the GPS vendor / bridge agent.
func (m *Module) IngestPositions(ctx context.Context, tx pgx.Tx, property uuid.UUID, ps []Position) (int, error) {
	n := 0
	for _, p := range ps {
		at := p.At
		if at.IsZero() {
			at = time.Now().UTC()
		}
		tag, err := tx.Exec(ctx, `INSERT INTO golf.cart_profiles (golf_cart_id, property_id, last_lat, last_lng, position_at, battery_percent)
			SELECT g.id, g.property_id, $3, $4, $5, $6 FROM golf.golf_carts g LEFT JOIN golf.cart_profiles cp ON cp.golf_cart_id = g.id
			WHERE g.property_id = $1 AND (g.id = $2 OR ($7 <> '' AND cp.gps_device_id = $7))
			ON CONFLICT (golf_cart_id) DO UPDATE SET last_lat = EXCLUDED.last_lat, last_lng = EXCLUDED.last_lng, position_at = EXCLUDED.position_at,
			battery_percent = coalesce(EXCLUDED.battery_percent, golf.cart_profiles.battery_percent)`, property, p.GolfCartID, p.Lat, p.Lng, at, p.Battery, p.DeviceID)
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	if n > 0 {
		if err := m.live(ctx, tx, "golf.cart", property, "positions", "", map[string]any{"count": n}); err != nil {
			return n, err
		}
	}
	return n, nil
}

// Distance is the distance from a position to a target on the hole.
type Distance struct {
	Target string `json:"target" enum:"green_front,green_center,green_back,hazard,point_of_interest,distance_marker"`
	Name   string `json:"name,omitempty"`
	Meters int    `json:"meters"`
}

// HoleDistances computes GPS distances to the green front/center/back,
// hazards and POIs of the hole's P1 course assets (FR-PLX-02).
func (m *Module) HoleDistances(ctx context.Context, q dbtx.Querier, hole uuid.UUID, lat, lng float64) ([]Distance, error) {
	var ok bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.holes WHERE id = $1)`, hole).Scan(&ok); err != nil {
		return nil, err
	}
	if !ok {
		return nil, errs.NotFound("hole")
	}
	type asset struct {
		Type     string         `db:"asset_type"`
		Name     string         `db:"name"`
		Geometry map[string]any `db:"geometry"`
	}
	assets, err := handle.List[asset](q.Query(ctx, `SELECT asset_type, name, geometry FROM golf.course_assets WHERE hole_id = $1 AND status = 'active'
		AND archived_at IS NULL AND asset_type IN ('green_front', 'green_center', 'green_back', 'hazard', 'point_of_interest', 'distance_marker')
		ORDER BY array_position(ARRAY['green_front','green_center','green_back','hazard','point_of_interest','distance_marker'], asset_type), code`, hole))
	if err != nil {
		return nil, err
	}
	out := []Distance{}
	for _, a := range assets {
		if la, lo, ok := geoPoint(a.Geometry); ok {
			out = append(out, Distance{Target: a.Type, Name: a.Name, Meters: haversine(lat, lng, la, lo)})
		}
	}
	return out, nil
}

// courseOverview puts the course map and the green of the hole on it
// (Cart View, FR-PLX-01/02); the map projects the device position too.
func courseOverview(ctx context.Context, q dbtx.Querier, hole uuid.UUID, out *CourseMap) (courseMap, error) {
	var course uuid.UUID
	if err := q.QueryRow(ctx, `SELECT course_id FROM golf.holes WHERE id = $1`, hole).Scan(&course); err != nil {
		return courseMap{}, err
	}
	cm, err := loadCourseMap(ctx, q, course)
	if err != nil || !cm.ok {
		return cm, err
	}
	out.OverviewURL = cm.url
	var geom map[string]any
	err = q.QueryRow(ctx, `SELECT geometry FROM golf.course_assets WHERE hole_id = $1 AND asset_type = 'green_center' AND status = 'active'
		AND archived_at IS NULL ORDER BY code LIMIT 1`, hole).Scan(&geom)
	if dbtx.IsNoRows(err) {
		return cm, nil
	}
	if err != nil {
		return cm, err
	}
	if la, lo, ok := geoPoint(geom); ok {
		out.GreenX, out.GreenY = cm.project(la, lo)
	}
	return cm, nil
}

func haversine(lat1, lng1, lat2, lng2 float64) int {
	const r = 6371000.0
	rad := math.Pi / 180
	dlat, dlng := (lat2-lat1)*rad, (lng2-lng1)*rad
	a := math.Sin(dlat/2)*math.Sin(dlat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dlng/2)*math.Sin(dlng/2)
	return int(math.Round(2 * r * math.Asin(math.Sqrt(a))))
}

// CartProfile is the P2 service & GPS profile of a P1 golf cart.
type CartProfile struct {
	GolfCartID            uuid.UUID  `json:"golfCartId" db:"golf_cart_id"`
	ServiceThresholdHours *string    `json:"serviceThresholdHours" db:"service_threshold_hours" doc:"Default: Golf Cart Policies"`
	HoursSinceService     string     `json:"hoursSinceService" db:"hours_since_service"`
	LastServiceAt         *time.Time `json:"lastServiceAt" db:"last_service_at"`
	BatteryPercent        *int       `json:"batteryPercent" db:"battery_percent"`
	GPSDeviceID           *string    `json:"gpsDeviceId" db:"gps_device_id"`
}

// CartProfileInput sets the service threshold and the GPS device.
type CartProfileInput struct {
	ServiceThresholdHours string `json:"serviceThresholdHours,omitempty"`
	GPSDeviceID           string `json:"gpsDeviceId,omitempty"`
}

func (m *Module) CartProfile(ctx context.Context, q dbtx.Querier, property, cart uuid.UUID) (CartProfile, error) {
	rows, err := q.Query(ctx, `SELECT g.id AS golf_cart_id, trim_scale(cp.service_threshold_hours)::text AS service_threshold_hours,
		trim_scale(round(`+cartServiceHours+`::numeric, 2))::text AS hours_since_service, cp.last_service_at, cp.battery_percent, cp.gps_device_id
		FROM golf.golf_carts g LEFT JOIN golf.cart_profiles cp ON cp.golf_cart_id = g.id WHERE g.id = $1 AND g.property_id = $2`, cart, property)
	return handle.One[CartProfile](rows, err, "golf cart")
}

func (m *Module) SetCartProfile(ctx context.Context, tx pgx.Tx, property, cart uuid.UUID, in CartProfileInput) (CartProfile, error) {
	before, err := m.CartProfile(ctx, tx, property, cart)
	if err != nil {
		return before, err
	}
	var threshold *string
	if in.ServiceThresholdHours != "" {
		v, err := handle.Decimal("serviceThresholdHours", in.ServiceThresholdHours, decimal.Zero)
		if err != nil {
			return before, err
		}
		s := v.String()
		threshold = &s
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.cart_profiles (golf_cart_id, property_id, service_threshold_hours, gps_device_id, updated_by)
		VALUES ($1,$2,$3::numeric,$4,$5) ON CONFLICT (golf_cart_id) DO UPDATE SET
		service_threshold_hours = coalesce(EXCLUDED.service_threshold_hours, golf.cart_profiles.service_threshold_hours),
		gps_device_id = coalesce(EXCLUDED.gps_device_id, golf.cart_profiles.gps_device_id), updated_by = EXCLUDED.updated_by`,
		cart, property, threshold, nullStr(in.GPSDeviceID), actorPtr(ctx)); err != nil {
		return before, err
	}
	after, err := m.CartProfile(ctx, tx, property, cart)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.cart_profile", cart, "golf cart profile", audit.ActionUpdate, property, before, after, "")
}

// ReadyGuard allows a manual Ready only when the cart's latest inspection
// passed and it has not been out or into maintenance since (FR-CTL-02);
// otherwise the cart becomes Ready through a pre-op or release inspection.
func (m *Module) ReadyGuard(ctx context.Context, q dbtx.Querier, property, cart uuid.UUID) error {
	var ok bool
	if err := q.QueryRow(ctx, `WITH last AS (SELECT passed, inspected_at FROM golf.cart_inspections WHERE golf_cart_id = $1 AND property_id = $2
		ORDER BY inspected_at DESC LIMIT 1)
		SELECT coalesce((SELECT passed FROM last), false)
		  AND NOT EXISTS (SELECT 1 FROM golf.golf_cart_assignments a, last WHERE a.golf_cart_id = $1 AND a.returned_at > last.inspected_at)
		  AND NOT EXISTS (SELECT 1 FROM golf.cart_maintenance mt WHERE mt.golf_cart_id = $1 AND mt.status = 'open')`, cart, property).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return errs.Conflict("inspection_required", "a golf cart becomes Ready after a passed pre-op or release inspection")
	}
	return nil
}
