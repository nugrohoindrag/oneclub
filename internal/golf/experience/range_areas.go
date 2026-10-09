package experience

// Driving range areas (demo feedback 9 Oct 2026): not every club has indoor
// bays, so the front desk switches the Indoor and Outdoor areas on or off.
// An area is offered for booking — Member App, website and front desk —
// only while it is on and has an active bay; the availability and the
// booking refuse any other area, so hiding it in a screen is not enough.

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

// RangeArea is a driving range area and whether it can be booked.
type RangeArea struct {
	Area    string `json:"area" db:"area" enum:"outdoor,indoor"`
	Enabled bool   `json:"enabled" db:"enabled" doc:"Switched on by the front desk (default on)"`
	Bays    int    `json:"bays" db:"bays" doc:"Active bays of the area"`
	Offered bool   `json:"offered" db:"-" doc:"Bookable: switched on and has a bay"`
}

// RangeAreaInput switches an area on or off.
type RangeAreaInput struct {
	Enabled bool `json:"enabled"`
}

// rangeAreaNames in the order they are offered (outdoor first).
var rangeAreaNames = []string{"outdoor", "indoor"}

// RangeAreas lists both areas with their switch and bays.
func (m *Module) RangeAreas(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]RangeArea, error) {
	rows, err := q.Query(ctx, `SELECT a.area, coalesce(s.enabled, true) AS enabled,
		(SELECT count(*) FROM golf.range_bays b WHERE b.property_id = $1 AND b.area = a.area AND b.status = 'active'
			AND b.archived_at IS NULL AND b.resource_id IS NOT NULL)::int AS bays
		FROM unnest($2::text[]) WITH ORDINALITY AS a(area, ord)
		LEFT JOIN golf.range_areas s ON s.property_id = $1 AND s.area = a.area ORDER BY a.ord`, property, rangeAreaNames)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[RangeArea])
	for i := range out {
		out[i].Offered = out[i].Enabled && out[i].Bays > 0
	}
	return out, err
}

func (m *Module) offeredRangeAreas(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]RangeArea, error) {
	all, err := m.RangeAreas(ctx, q, property)
	out := []RangeArea{}
	for _, a := range all {
		if a.Offered {
			out = append(out, a)
		}
	}
	return out, err
}

// bookableArea resolves the area of an availability or a booking: the
// requested one when it is offered, the first offered one when empty.
func (m *Module) bookableArea(ctx context.Context, q dbtx.Querier, property uuid.UUID, area string) (string, error) {
	if area != "" && area != "indoor" && area != "outdoor" {
		return "", handle.Invalid("area", "invalid", "indoor or outdoor")
	}
	offered, err := m.offeredRangeAreas(ctx, q, property)
	if err != nil {
		return "", err
	}
	for _, a := range offered {
		if area == "" || a.Area == area {
			return a.Area, nil
		}
	}
	if area == "" {
		return "", errs.Conflict("range_closed", "no driving range area is open for booking")
	}
	return "", handle.Invalid("area", "closed", "the "+area+" area is not open for booking")
}

// SetRangeArea switches an area on or off.
func (m *Module) SetRangeArea(ctx context.Context, tx pgx.Tx, property uuid.UUID, area string, in RangeAreaInput) ([]RangeArea, error) {
	if area != "indoor" && area != "outdoor" {
		return nil, errs.NotFound("range area")
	}
	before, err := m.RangeAreas(ctx, tx, property)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.range_areas (property_id, area, enabled, updated_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT (property_id, area) DO UPDATE SET enabled = excluded.enabled, updated_by = excluded.updated_by`,
		property, area, in.Enabled, actorPtr(ctx)); err != nil {
		return nil, err
	}
	after, err := m.RangeAreas(ctx, tx, property)
	if err != nil {
		return nil, err
	}
	action := "disable"
	if in.Enabled {
		action = "enable"
	}
	return after, record(ctx, tx, "golf.range_area", property, "Driving range "+area, action, property, before, after, "")
}

func (m *Module) registerRangeAreas(reg *route.Registry, add func(tag string, rt route.Route)) {
	db := m.DB
	add("Driving Range", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/range-areas", Summary: "Driving range areas (Indoor / Outdoor) and whether they can be booked",
		Permission: "golf.range.view", Response: RangeArea{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[RangeArea], error) {
			return handle.Page(m.RangeAreas(ctx, tx, handle.Property(ctx)))
		})})
	add("Driving Range", route.Route{Method: http.MethodPut, Path: "/api/v1/golf/range-areas/{area}", Summary: "Front desk: switch a driving range area on or off",
		Permission: "golf.range.operate", Request: RangeAreaInput{}, Response: RangeArea{}, List: true, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RangeAreaInput) (httpx.Page[RangeArea], error) {
			return handle.Page(m.SetRangeArea(ctx, tx, handle.Property(ctx), chi.URLParam(r, "area"), in))
		})})
	crm.MeRoute(reg, "golf", "Member Portal", route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/range-areas", Summary: "Driving range areas open for booking",
		Response: RangeArea{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[RangeArea], error) {
			return handle.Page(m.offeredRangeAreas(ctx, tx, handle.Property(ctx)))
		})})
	crm.PublicRoute(reg, "golf", route.Route{Method: http.MethodGet, Path: "/api/v1/public/golf/range-areas", Summary: "Driving range areas open for booking",
		Response: RangeArea{}, List: true, Query: []route.Param{{Name: "propertyId", Required: true}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			pid, err := uuid.Parse(r.URL.Query().Get("propertyId"))
			if err != nil {
				httpx.WriteError(w, r, errs.BadRequest("property_required", "propertyId is required"))
				return
			}
			ctx := crm.PublicCtx(r.Context(), pid)
			var out []RangeArea
			err = db.WithReadTx(ctx, func(tx pgx.Tx) error {
				out, err = m.offeredRangeAreas(ctx, tx, pid)
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, httpx.Page[RangeArea]{Items: out})
		}})
}
