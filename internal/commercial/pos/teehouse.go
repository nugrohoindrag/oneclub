package pos

// Tee houses (demo feedback 9 Oct 2026): six on-course F&B outlets in two
// groups of three (one group per nine). Each tee house is a POS outlet
// (outlet type tee_house, attributes group / area) with the menu of the
// Halfway House, and its own inventory warehouse — POS sales consume there,
// replenished from the store with the Halfway House par stock. The tee
// house board shows per tee house the open orders by service status
// (order tracking) and the stock against par (stock monitoring).

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/inventory"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
)

// TeeHouseGroup is one area of tee houses.
type TeeHouseGroup struct {
	Code  string `json:"code" doc:"e.g. A"`
	Area  string `json:"area" doc:"e.g. Front Nine"`
	Count int    `json:"count"`
}

// TeeHouseSetupInput sets up the tee houses (default 3 + 3).
type TeeHouseSetupInput struct {
	Groups   []TeeHouseGroup `json:"groups,omitempty" doc:"Default: A Front Nine × 3, B Back Nine × 3"`
	Template string          `json:"template,omitempty" doc:"Outlet / warehouse code whose menu and par stock are copied (default HALFWAY)"`
}

// TeeHouse is a tee house with its live board.
type TeeHouse struct {
	ID          uuid.UUID      `json:"id"`
	Code        string         `json:"code"`
	Name        string         `json:"name"`
	Group       string         `json:"group"`
	Area        string         `json:"area"`
	Status      string         `json:"status"`
	WarehouseID *uuid.UUID     `json:"warehouseId"`
	OrdersToday int            `json:"ordersToday"`
	SalesToday  string         `json:"salesToday"`
	Open        map[string]int `json:"open" doc:"Open orders by service status: new, sent, preparing, ready, out_for_delivery"`
	LowStock    int            `json:"lowStock" doc:"Items at or under the minimum"`
	OutOfStock  int            `json:"outOfStock"`
}

// teeHouseHoles is where the default tee houses stand (MGCC: Tee House 2 by
// hole 8; the Halfway House after hole 9).
var teeHouseHoles = map[int]int{1: 2, 2: 8, 3: 5, 4: 12, 5: 14, 6: 17}

// SetupTeeHouses creates the tee house outlets, menus and warehouses once.
func (m *Module) SetupTeeHouses(ctx context.Context, tx pgx.Tx, property uuid.UUID, in TeeHouseSetupInput) ([]TeeHouse, error) {
	if len(in.Groups) == 0 {
		in.Groups = []TeeHouseGroup{{Code: "A", Area: "Front Nine", Count: 3}, {Code: "B", Area: "Back Nine", Count: 3}}
	}
	if in.Template == "" {
		in.Template = "HALFWAY"
	}
	var tmpl *uuid.UUID
	_ = tx.QueryRow(ctx, `SELECT id FROM commercial.outlets WHERE property_id = $1 AND code = $2`, property, in.Template).Scan(&tmpl)
	n := 0
	for _, g := range in.Groups {
		if g.Count < 1 || g.Count > 9 {
			return nil, handle.Invalid("groups", "invalid", "1 – 9 tee houses per group")
		}
		for i := 1; i <= g.Count; i++ {
			n++
			code, name := "TH"+strconv.Itoa(n), fmt.Sprintf("Tee House %d", n)
			var oid uuid.UUID
			if err := tx.QueryRow(ctx, `INSERT INTO commercial.outlets (id, property_id, code, name, outlet_type, status, attributes, created_by)
				VALUES ($1,$2,$3,$4,'tee_house','active',jsonb_build_object('group', $5::text, 'area', $6::text),$7)
				ON CONFLICT (property_id, code) DO UPDATE SET outlet_type = 'tee_house',
				  attributes = commercial.outlets.attributes || jsonb_build_object('group', $5::text, 'area', $6::text)
				RETURNING id`, id.New(), property, code, name, g.Code, g.Area, handle.UserID(ctx)).Scan(&oid); err != nil {
				return nil, err
			}
			// the hole it stands by, for "nearest tee house" on the caddy tablet (kept when set)
			if h, ok := teeHouseHoles[n]; ok {
				if _, err := tx.Exec(ctx, `UPDATE commercial.outlets SET attributes = attributes || jsonb_build_object('hole', $2::int) WHERE id = $1 AND NOT attributes ? 'hole'`,
					oid, h); err != nil {
					return nil, err
				}
			}
			if tmpl != nil {
				if _, err := tx.Exec(ctx, `INSERT INTO commercial.menus (id, property_id, outlet_id, code, name, available_from, available_to, weekdays, product_ids, channels, created_by)
					SELECT gen_random_uuid(), property_id, $2, $3 || '-' || code, $4 || ' · ' || name, available_from, available_to, weekdays, product_ids, channels, $5
					FROM commercial.menus WHERE outlet_id = $1 AND status = 'active' AND archived_at IS NULL
					ON CONFLICT (property_id, code) DO NOTHING`, *tmpl, oid, code, name, handle.UserID(ctx)); err != nil {
					return nil, err
				}
			}
			if _, err := inventory.EnsureOutletWarehouse(ctx, tx, property, oid, code, name, "tee_house", in.Template); err != nil {
				return nil, err
			}
		}
	}
	out, err := m.TeeHouses(ctx, tx, property)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "setup", EntityType: "commercial.tee_house", EntityID: property.String(),
		EntityLabel: fmt.Sprintf("%d tee houses", n), PropertyID: &property, After: out})
}

// TeeHouses lists the tee houses with today's orders and stock alerts.
func (m *Module) TeeHouses(ctx context.Context, tx pgx.Tx, property uuid.UUID) ([]TeeHouse, error) {
	// today is the club's day (WIB), not the database's UTC day
	now := clock.Now().In(calendar.Location(ctx, tx))
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	rows, err := tx.Query(ctx, `SELECT o.id, o.code, o.name, coalesce(o.attributes->>'group', ''), coalesce(o.attributes->>'area', ''), o.status,
		(SELECT w.id FROM inventory.warehouses w WHERE w.outlet_id = o.id AND w.archived_at IS NULL ORDER BY w.code LIMIT 1),
		(SELECT count(*) FROM commercial.orders x WHERE x.outlet_id = o.id AND x.created_at >= $2 AND x.status <> 'voided')::int,
		(SELECT trim_scale(coalesce(sum(l.total_amount), 0))::text FROM commercial.orders x JOIN commercial.order_lines l ON l.order_id = x.id AND l.status = 'active'
		  WHERE x.outlet_id = o.id AND x.created_at >= $2 AND x.status IN ('paid', 'charged'))
		FROM commercial.outlets o WHERE o.property_id = $1 AND o.outlet_type = 'tee_house' AND o.archived_at IS NULL
		ORDER BY o.attributes->>'group', length(o.code), o.code`, property, today)
	if err != nil {
		return nil, err
	}
	out := []TeeHouse{}
	var ids []uuid.UUID
	for rows.Next() {
		t := TeeHouse{Open: map[string]int{}}
		if err := rows.Scan(&t.ID, &t.Code, &t.Name, &t.Group, &t.Area, &t.Status, &t.WarehouseID, &t.OrdersToday, &t.SalesToday); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
		ids = append(ids, t.ID)
	}
	rows.Close()
	if len(ids) == 0 {
		return out, nil
	}
	open, err := tx.Query(ctx, `SELECT outlet_id, service_status, count(*)::int FROM commercial.orders WHERE outlet_id = ANY($1) AND status IN ('open', 'paid', 'charged')
		AND service_status <> 'served' AND created_at >= $2 GROUP BY 1, 2`, ids, today)
	if err != nil {
		return nil, err
	}
	at := map[uuid.UUID]int{}
	for i, t := range out {
		at[t.ID] = i
	}
	for open.Next() {
		var oid uuid.UUID
		var st string
		var c int
		if err := open.Scan(&oid, &st, &c); err != nil {
			open.Close()
			return nil, err
		}
		out[at[oid]].Open[st] = c
	}
	open.Close()
	stock, err := inventory.OutletStock(ctx, tx, property, ids)
	if err != nil {
		return nil, err
	}
	for _, l := range stock {
		switch l.Status {
		case "low":
			out[at[l.OutletID]].LowStock++
		case "out":
			out[at[l.OutletID]].OutOfStock++
		}
	}
	return out, nil
}

func (m *Module) registerTeeHouses(reg *route.Registry) {
	db := m.DB
	add := func(rt route.Route) {
		rt.Module, rt.Tag, rt.Scope = "commercial", "Tee Houses", route.ScopeProperty
		reg.Add(rt)
	}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/commercial/tee-houses", Summary: "Tee houses by group with today's orders, open orders by service status and stock alerts",
		Permission: "commercial.order.view", Response: TeeHouse{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[TeeHouse], error) {
			out, err := m.TeeHouses(ctx, tx, handle.Property(ctx))
			return httpx.Page[TeeHouse]{Items: out}, err
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/commercial/tee-houses/stock", Summary: "Tee house stock monitoring: balance against par per item",
		Permission: "commercial.order.view", Response: inventory.OutletStockLine{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[inventory.OutletStockLine], error) {
			rows, err := tx.Query(ctx, `SELECT id FROM commercial.outlets WHERE property_id = $1 AND outlet_type = 'tee_house' AND archived_at IS NULL`, handle.Property(ctx))
			if err != nil {
				return httpx.Page[inventory.OutletStockLine]{}, err
			}
			ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
			if err != nil {
				return httpx.Page[inventory.OutletStockLine]{}, err
			}
			out, err := inventory.OutletStock(ctx, tx, handle.Property(ctx), ids)
			return httpx.Page[inventory.OutletStockLine]{Items: out}, err
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/tee-houses:setup", Summary: "Set up the tee houses: outlets by group, Halfway House menu, own warehouse with par stock",
		Permission: "commercial.outlet.create", Request: TeeHouseSetupInput{}, Response: TeeHouse{}, List: true, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TeeHouseSetupInput) (httpx.Page[TeeHouse], error) {
			out, err := m.SetupTeeHouses(ctx, tx, handle.Property(ctx), in)
			return httpx.Page[TeeHouse]{Items: out}, err
		})})
}
