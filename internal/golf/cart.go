package golf

// Golf Cart Full Lifecycle (PRD P2 EP-07), mapped to the P1 readiness states:
//
//	Release/Pre-op Inspection (pass) → Ready → Assignment → In Use → Return
//	→ Post-Operation Inspection → pass: Not Ready / Charging → (pre-op) Ready
//	                            → fail: Maintenance → Release Inspection → Ready
//
// "Under Inspection" is the proposed state between return and post-op
// inspection. Readiness changes only through this file (contract C8).

import (
	"context"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
)

// setReadiness changes the readiness of a golf cart and notifies the
// readiness board (FR-CTL-07).
func (m *Module) setReadiness(ctx context.Context, tx pgx.Tx, property, cart uuid.UUID, readiness, reason string) error {
	var before string
	if err := tx.QueryRow(ctx, `SELECT readiness FROM golf.golf_carts WHERE id = $1`, cart).Scan(&before); err != nil {
		return err
	}
	if before == readiness {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.golf_carts SET readiness = $2, updated_by = $3 WHERE id = $1`, cart, readiness, actor(ctx)); err != nil {
		return err
	}
	if err := m.live(ctx, tx, "golf.cart", property, "readiness", cart.String(), map[string]any{"from": before, "to": readiness}); err != nil {
		return err
	}
	return m.publish(ctx, tx, "golf.golf_cart_readiness_changed", "golf.golf_cart", cart, property, map[string]any{"cartId": cart, "from": before,
		"to": readiness, "reason": reason})
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
	CartID         uuid.UUID     `json:"cartId" db:"cart_id"`
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

const inspectionSelect = `SELECT id, cart_id, inspection_kind, checklist_id, results, photos, passed, trim_scale(hour_meter)::text AS hour_meter,
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
		"pre_op":  {"not_ready", "charging", "under_inspection", "ready"},
		"post_op": {"under_inspection"},
		"release": {"maintenance"},
	}
	from, ok := allowed[in.Kind]
	if !ok {
		return Inspection{}, handle.Invalid("kind", "invalid_kind", "pre_op, post_op or release")
	}
	if !slices.Contains(from, readiness) {
		return Inspection{}, errs.Conflict("invalid_readiness", "a "+in.Kind+" inspection is not possible while the cart is "+readiness)
	}
	if in.Kind == "pre_op" && readiness == "under_inspection" {
		// A returned cart needs its post-operation inspection first.
		var returned bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.cart_assignments a WHERE a.cart_id = $1 AND a.returned_at IS NOT NULL
			AND a.assigned_at > coalesce((SELECT max(inspected_at) FROM golf.cart_inspections i WHERE i.cart_id = $1), '-infinity'))`, cart).Scan(&returned); err != nil {
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
	after := readiness
	var maint *uuid.UUID
	switch {
	case in.Kind == "post_op" && passed:
		after = "not_ready"
		if pol.ChargingAfterUse {
			after = "charging"
		}
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
	if _, err := tx.Exec(ctx, `INSERT INTO golf.cart_inspections (id, property_id, cart_id, inspection_kind, checklist_id, results, photos, passed, hour_meter,
		battery_percent, readiness_after, maintenance_id, inspected_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10,$11,$12,$13)`, iid, property, cart,
		in.Kind, in.ChecklistID, jsonOf(in.Results), in.Photos, passed, hmp, in.BatteryPercent, after, maint, actor(ctx)); err != nil {
		return Inspection{}, err
	}
	if in.BatteryPercent != nil {
		if _, err := tx.Exec(ctx, `UPDATE golf.golf_carts SET battery_percent = $2 WHERE id = $1`, cart, *in.BatteryPercent); err != nil {
			return Inspection{}, err
		}
	}
	if in.Kind == "release" && passed {
		// Maintenance release closes the open maintenance record(s).
		var scheduled bool
		if err := tx.QueryRow(ctx, `WITH c AS (UPDATE golf.cart_maintenance SET status = 'closed', closed_at = now(), closed_by = $3, release_inspection_id = $2
			WHERE cart_id = $1 AND status = 'open' RETURNING category) SELECT EXISTS (SELECT 1 FROM c WHERE category = 'scheduled_service')`,
			cart, iid, actor(ctx)).Scan(&scheduled); err != nil {
			return Inspection{}, err
		}
		if scheduled {
			if _, err := tx.Exec(ctx, `UPDATE golf.golf_carts SET hours_since_service = 0, service_alerted_at = NULL WHERE id = $1`, cart); err != nil {
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
	CartID      uuid.UUID `json:"cartId"`
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
	ID            uuid.UUID  `json:"id" db:"id"`
	MaintenanceNo string     `json:"maintenanceNo" db:"maintenance_no"`
	CartID        uuid.UUID  `json:"cartId" db:"cart_id"`
	CartCode      string     `json:"cartCode" db:"cart_code"`
	Category      string     `json:"category" db:"category"`
	Description   string     `json:"description" db:"description"`
	Cost          string     `json:"cost" db:"cost"`
	Status        string     `json:"status" db:"status" enum:"open,closed"`
	OpenedAt      time.Time  `json:"openedAt" db:"opened_at"`
	ClosedAt      *time.Time `json:"closedAt" db:"closed_at"`
	ReleaseID     *uuid.UUID `json:"releaseInspectionId" db:"release_inspection_id"`
	Notes         *string    `json:"notes" db:"notes"`
}

const maintenanceSelect = `SELECT m.id, m.maintenance_no, m.cart_id, g.code AS cart_code, m.category, m.description, trim_scale(m.cost)::text AS cost,
	m.status, m.opened_at, m.closed_at, m.release_inspection_id, m.notes FROM golf.cart_maintenance m JOIN golf.golf_carts g ON g.id = m.cart_id`

func (m *Module) openMaintenance(ctx context.Context, tx pgx.Tx, property, cart uuid.UUID, category, desc string, cost decimal.Decimal) (uuid.UUID, error) {
	no, err := numbering.Next(ctx, tx, property, "MNT", localNow(ctx, tx))
	if err != nil {
		return uuid.Nil, err
	}
	mid := id.New()
	_, err = tx.Exec(ctx, `INSERT INTO golf.cart_maintenance (id, property_id, cart_id, maintenance_no, category, description, cost, opened_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8)`, mid, property, cart, no, category, desc, cost.String(), actor(ctx))
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
	if err := tx.QueryRow(ctx, `SELECT readiness FROM golf.golf_carts WHERE id = $1 AND property_id = $2 FOR UPDATE`, in.CartID, property).Scan(&readiness); err != nil {
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
	mid, err := m.openMaintenance(ctx, tx, property, in.CartID, in.Category, in.Description, cost)
	if err != nil {
		return Maintenance{}, err
	}
	if err := m.setReadiness(ctx, tx, property, in.CartID, "maintenance", in.Description); err != nil {
		return Maintenance{}, err
	}
	mt, err := handle.Get[Maintenance](tx.Query(ctx, maintenanceSelect+` WHERE m.id = $1`, mid))
	if err != nil {
		return mt, err
	}
	return mt, record(ctx, tx, "golf.cart_maintenance", mid, mt.MaintenanceNo, audit.ActionCreate, property, nil, mt, "")
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
	if _, err := tx.Exec(ctx, `UPDATE golf.cart_maintenance SET cost = $2::numeric, notes = coalesce($3, notes) WHERE id = $1`, mid, cost.String(), nzs(in.Notes)); err != nil {
		return before, err
	}
	after, err := handle.Get[Maintenance](tx.Query(ctx, maintenanceSelect+` WHERE m.id = $1`, mid))
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.cart_maintenance", mid, after.MaintenanceNo, audit.ActionUpdate, property, before, after, "")
}

type ReadinessInput struct {
	Readiness string `json:"readiness" enum:"out_of_service,under_inspection"`
	Reason    string `json:"reason"`
}

// SetCartStatus takes a cart out of service or back to inspection.
func (m *Module) SetCartStatus(ctx context.Context, tx pgx.Tx, property, cart uuid.UUID, in ReadinessInput) error {
	if err := handle.Required("reason", in.Reason); err != nil {
		return err
	}
	if in.Readiness != "out_of_service" && in.Readiness != "under_inspection" {
		return handle.Invalid("readiness", "invalid", "out_of_service or under_inspection (Ready only through an inspection)")
	}
	var before string
	if err := tx.QueryRow(ctx, `SELECT readiness FROM golf.golf_carts WHERE id = $1 AND property_id = $2 FOR UPDATE`, cart, property).Scan(&before); err != nil {
		if dbtx.IsNoRows(err) {
			return errs.NotFound("golf cart")
		}
		return err
	}
	if before == "in_use" {
		return errs.Conflict("cart_in_use", "the cart is in use")
	}
	if err := m.setReadiness(ctx, tx, property, cart, in.Readiness, in.Reason); err != nil {
		return err
	}
	return record(ctx, tx, "golf.golf_cart", cart, in.Readiness, audit.ActionStatusChange, property, map[string]any{"readiness": before},
		map[string]any{"readiness": in.Readiness}, in.Reason)
}

// ── readiness board (FR-CTL-07) ───────────────────────────────────────────

type BoardCart struct {
	ID                uuid.UUID      `json:"id" db:"id"`
	Code              string         `json:"code" db:"code"`
	Name              string         `json:"name" db:"name"`
	Readiness         string         `json:"readiness" db:"readiness"`
	BatteryPercent    *int           `json:"batteryPercent" db:"battery_percent"`
	UsageHours        string         `json:"usageHours" db:"usage_hours"`
	HoursSinceService string         `json:"hoursSinceService" db:"hours_since_service"`
	ServiceDue        bool           `json:"serviceDue" db:"service_due"`
	FlightNo          *string        `json:"flightNo" db:"flight_no"`
	Position          map[string]any `json:"position" db:"last_position"`
}

type Board struct {
	Counts map[string]int `json:"counts"`
	Carts  []BoardCart    `json:"carts"`
}

func (m *Module) ReadinessBoard(ctx context.Context, q dbtx.Querier, property uuid.UUID) (Board, error) {
	carts, err := handle.List[BoardCart](q.Query(ctx, `SELECT g.id, g.code, g.name, g.readiness, g.battery_percent, trim_scale(g.usage_hours)::text AS usage_hours,
		trim_scale(g.hours_since_service)::text AS hours_since_service, g.hours_since_service >= g.service_threshold_hours AS service_due,
		(SELECT f.flight_no FROM golf.cart_assignments a JOIN golf.flights f ON f.id = a.flight_id WHERE a.cart_id = g.id AND a.status IN ('assigned', 'in_use') LIMIT 1) AS flight_no,
		g.last_position FROM golf.golf_carts g WHERE g.property_id = $1 AND g.status = 'active' AND g.archived_at IS NULL ORDER BY g.code`, property))
	b := Board{Counts: map[string]int{}, Carts: carts}
	for _, c := range carts {
		b.Counts[c.Readiness]++
	}
	return b, err
}

// ── GPS (FR-PLX-02, FR-PLX-05, FR-INT-P2-02) ──────────────────────────────

// Position is a GPS fix of a golf cart.
type Position struct {
	CartID   uuid.UUID `json:"cartId"`
	DeviceID string    `json:"deviceId,omitempty"`
	Lat      float64   `json:"lat"`
	Lng      float64   `json:"lng"`
	Battery  *int      `json:"batteryPercent,omitempty"`
	At       time.Time `json:"at"`
}

// GPSAdapter is the golf cart GPS vendor interface (vendor: OQ #8).
type GPSAdapter interface {
	Positions(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]Position, error)
}

// MockGPS places in-use carts on the tee of the hole their flight is
// playing — enough for the map and pace screens until a vendor is chosen.
type MockGPS struct{}

func (MockGPS) Positions(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]Position, error) {
	type row struct {
		CartID uuid.UUID      `db:"cart_id"`
		Device *string        `db:"gps_device_id"`
		Geo    map[string]any `db:"geo"`
	}
	rows, err := handle.List[row](q.Query(ctx, `SELECT a.cart_id, g.gps_device_id, coalesce(h.geo, '{}'::jsonb) AS geo
		FROM golf.cart_assignments a JOIN golf.golf_carts g ON g.id = a.cart_id JOIN golf.flights f ON f.id = a.flight_id
		LEFT JOIN golf.hole_progress p ON p.flight_id = f.id AND p.seq = f.current_seq LEFT JOIN golf.holes h ON h.id = p.hole_id
		WHERE a.property_id = $1 AND a.status = 'in_use'`, property))
	if err != nil {
		return nil, err
	}
	out := []Position{}
	for _, r := range rows {
		lat, lng, ok := point(r.Geo["tee"])
		if !ok {
			continue
		}
		out = append(out, Position{CartID: r.CartID, DeviceID: deref(r.Device), Lat: lat, Lng: lng, At: time.Now().UTC()})
	}
	return out, nil
}

func point(v any) (float64, float64, bool) {
	a, ok := v.([]any)
	if !ok || len(a) != 2 {
		return 0, 0, false
	}
	lat, ok1 := a[0].(float64)
	lng, ok2 := a[1].(float64)
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
		pos := map[string]any{"lat": p.Lat, "lng": p.Lng, "at": at}
		tag, err := tx.Exec(ctx, `UPDATE golf.golf_carts SET last_position = $3, battery_percent = coalesce($4, battery_percent)
			WHERE property_id = $1 AND (id = $2 OR ($5 <> '' AND gps_device_id = $5))`, property, p.CartID, pos, p.Battery, p.DeviceID)
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
	Target string `json:"target"`
	Name   string `json:"name,omitempty"`
	Meters int    `json:"meters"`
}

// HoleDistances computes GPS distances to green front/center/back, hazards
// and POIs (FR-PLX-02).
func (m *Module) HoleDistances(ctx context.Context, q dbtx.Querier, hole uuid.UUID, lat, lng float64) ([]Distance, error) {
	var geo map[string]any
	if err := q.QueryRow(ctx, `SELECT geo FROM golf.holes WHERE id = $1`, hole).Scan(&geo); err != nil {
		if dbtx.IsNoRows(err) {
			return nil, errs.NotFound("hole")
		}
		return nil, err
	}
	out := []Distance{}
	for _, k := range []string{"greenFront", "greenCenter", "greenBack"} {
		if la, lo, ok := point(geo[k]); ok {
			out = append(out, Distance{Target: k, Meters: haversine(lat, lng, la, lo)})
		}
	}
	for _, kind := range []string{"hazards", "pois"} {
		list, _ := geo[kind].([]any)
		for _, it := range list {
			mp, _ := it.(map[string]any)
			la, ok1 := mp["lat"].(float64)
			lo, ok2 := mp["lng"].(float64)
			if ok1 && ok2 {
				out = append(out, Distance{Target: kind[:len(kind)-1], Name: str(mp["name"]), Meters: haversine(lat, lng, la, lo)})
			}
		}
	}
	return out, nil
}

func haversine(lat1, lng1, lat2, lng2 float64) int {
	const r = 6371000.0
	rad := math.Pi / 180
	dlat, dlng := (lat2-lat1)*rad, (lng2-lng1)*rad
	a := math.Sin(dlat/2)*math.Sin(dlat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dlng/2)*math.Sin(dlng/2)
	return int(math.Round(2 * r * math.Asin(math.Sqrt(a))))
}
