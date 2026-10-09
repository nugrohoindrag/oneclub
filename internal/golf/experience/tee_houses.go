package experience

import (
	"context"
	"net/http"
	"sort"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

/*
 * Nearest tee house (demo feedback 10 Oct 2026 #27): the Cart View shows the
 * tee house / Halfway House closest to the flight and On-Course Order picks
 * it by default. A tee house outlet carries the hole it stands by (outlet
 * attribute "hole", e.g. Halfway House = 9); its position is that hole's
 * green unless the outlet has its own "lat"/"lng". With the tablet's GPS the
 * nearest one wins, without it the next one on the flight's route.
 */

// RouteTeeHouse is a tee house seen from a flight.
type RouteTeeHouse struct {
	OutletID   uuid.UUID `json:"outletId"`
	Code       string    `json:"code"`
	Name       string    `json:"name"`
	Hole       int       `json:"hole" doc:"Hole the tee house stands by (between this hole and the next)"`
	Between    string    `json:"between" doc:"e.g. 9–10"`
	Open       bool      `json:"open"`
	Meters     *int      `json:"meters" doc:"Distance from the tablet (GPS)"`
	HolesAhead *int      `json:"holesAhead" doc:"Holes until the flight reaches it on its route (0 = this hole)"`
	Nearest    bool      `json:"nearest" doc:"The one to suggest: nearest by GPS, else the next on the route"`
}

type teeHouseRow struct {
	ID     uuid.UUID `db:"id"`
	Code   string    `db:"code"`
	Name   string    `db:"name"`
	Status string    `db:"status"`
	Hole   *string   `db:"hole"`
	Lat    *string   `db:"lat"`
	Lng    *string   `db:"lng"`
}

// RouteTeeHouses lists the tee houses of the course for a flight.
func (m *Module) RouteTeeHouses(ctx context.Context, q dbtx.Querier, fid uuid.UUID, lat, lng *float64) ([]RouteTeeHouse, error) {
	r, err := m.GetRound(ctx, q, fid)
	if err != nil {
		return nil, err
	}
	if err := m.canSeeFlight(ctx, q, r); err != nil {
		return nil, err
	}
	rows, err := handle.List[teeHouseRow](q.Query(ctx, `SELECT id, code, name, status, attributes->>'hole' AS hole, attributes->>'lat' AS lat, attributes->>'lng' AS lng
		FROM commercial.outlets WHERE property_id = $1 AND archived_at IS NULL AND (outlet_type = 'tee_house' OR code = 'HALFWAY')
		ORDER BY length(code), code`, r.PropertyID))
	if err != nil {
		return nil, err
	}
	holes, err := m.roundHoles(ctx, q, r)
	if err != nil {
		return nil, err
	}
	// route sequence of each hole number (an 18-hole route plays 1..18)
	seqOf := map[int]int{}
	greenOf := map[int]uuid.UUID{}
	for _, h := range holes {
		if _, ok := seqOf[h.Number]; !ok {
			seqOf[h.Number] = h.Sequence
			greenOf[h.Number] = h.HoleID
		}
	}
	out := []RouteTeeHouse{}
	for _, t := range rows {
		hole := 0
		if t.Hole != nil {
			hole, _ = strconv.Atoi(*t.Hole)
		}
		th := RouteTeeHouse{OutletID: t.ID, Code: t.Code, Name: t.Name, Hole: hole, Open: t.Status == "active"}
		if hole > 0 {
			th.Between = strconv.Itoa(hole) + "–" + strconv.Itoa(hole%max(len(holes), 18)+1)
			if s, ok := seqOf[hole]; ok && r.CurrentSeq > 0 {
				ahead := s - r.CurrentSeq
				if ahead >= 0 {
					th.HolesAhead = &ahead
				}
			}
		}
		if lat != nil && lng != nil {
			var plat, plng float64
			ok := false
			if t.Lat != nil && t.Lng != nil {
				plat, _ = strconv.ParseFloat(*t.Lat, 64)
				plng, _ = strconv.ParseFloat(*t.Lng, 64)
				ok = plat != 0 && plng != 0
			} else if hid, has := greenOf[hole]; has {
				var g map[string]any
				if err := q.QueryRow(ctx, `SELECT geometry FROM golf.course_assets WHERE hole_id = $1 AND asset_type = 'green_center' AND status = 'active' LIMIT 1`,
					hid).Scan(&g); err == nil {
					plat, plng, ok = geoPoint(g)
				}
			}
			if ok {
				d := haversine(*lat, *lng, plat, plng)
				th.Meters = &d
			}
		}
		out = append(out, th)
	}
	// nearest by GPS when known, else the next on the route; closed last
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Open != b.Open {
			return a.Open
		}
		if a.Meters != nil && b.Meters != nil {
			return *a.Meters < *b.Meters
		}
		if (a.Meters != nil) != (b.Meters != nil) {
			return a.Meters != nil
		}
		if a.HolesAhead != nil && b.HolesAhead != nil {
			return *a.HolesAhead < *b.HolesAhead
		}
		return a.HolesAhead != nil
	})
	if len(out) > 0 && out[0].Open {
		out[0].Nearest = true
	}
	return out, nil
}

func (m *Module) registerRouteTeeHouses(add func(tag string, rt route.Route)) {
	add("Caddy Tablet", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/rounds/{id}/tee-houses", Summary: "Tee houses for the flight: nearest by GPS, else the next on the route",
		Permission: "golf.tablet.use", Response: RouteTeeHouse{}, List: true, Query: []route.Param{{Name: "lat"}, {Name: "lng"}},
		Handler: handle.Read(m.DB, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[RouteTeeHouse], error) {
			fid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[RouteTeeHouse]{}, err
			}
			var lat, lng *float64
			if a, err := strconv.ParseFloat(r.URL.Query().Get("lat"), 64); err == nil {
				if b, err := strconv.ParseFloat(r.URL.Query().Get("lng"), 64); err == nil {
					lat, lng = &a, &b
				}
			}
			return handle.Page(m.RouteTeeHouses(ctx, tx, fid, lat, lng))
		})})
}
