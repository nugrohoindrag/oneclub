package pos

// POS Table View (FR-POS-01 dine-in): the dining tables of an outlet on its
// floor plan, table reservations, and the live state of every table —
// Available, Booked (a reservation is due), Occupied (an open order seats
// it) or Billed (the bill was presented and waits for payment).

import (
	"context"
	"fmt"
	"net/http"

	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/commercial"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

// DiningTables are the tables of an outlet; pos_x / pos_y place the table on
// the floor plan (percent of its width and height).
var DiningTables = &resource.Def{
	Key: "commercial.dining_table", Module: "commercial", Perm: "commercial.dining_table", Path: "/api/v1/commercial/dining-tables",
	Table: "commercial.dining_tables", Name: "Dining Table", Plural: "Dining Tables", Tag: "POS", PropertyScoped: true, Archive: true,
	OrderBy: "outlet_id, length(code), code, id",
	Fields: []resource.Field{resource.Code("Table"),
		{Name: "outletId", Column: "outlet_id", Label: "Outlet", Kind: resource.UUID, Required: true, Filter: true, CreateOnly: true,
			Ref: &resource.Ref{Table: "commercial.outlets", SameProperty: true, Label: "outlet"}},
		{Name: "area", Column: "area", Label: "Area", Kind: resource.String, Max: 40, Filter: true},
		{Name: "seats", Column: "seats", Label: "Seats", Kind: resource.Int, Default: int64(4), Min: resource.Min(1), MaxN: resource.Max(30)},
		{Name: "shape", Column: "shape", Label: "Shape", Kind: resource.Enum, Enum: []string{"square", "round", "rect", "seat"}, Default: "square"},
		{Name: "posX", Column: "pos_x", Label: "Position X (%)", Kind: resource.Decimal, Default: "50", Min: resource.Min(0), MaxN: resource.Max(100)},
		{Name: "posY", Column: "pos_y", Label: "Position Y (%)", Kind: resource.Decimal, Default: "50", Min: resource.Min(0), MaxN: resource.Max(100)},
		resource.Status("active", "inactive")},
}

// TableReservations book tables of an outlet for a time.
var TableReservations = &resource.Def{
	Key: "commercial.table_reservation", Module: "commercial", Perm: "commercial.table_reservation", Path: "/api/v1/commercial/table-reservations",
	Table: "commercial.table_reservations", Name: "Table Reservation", Plural: "Table Reservations", Tag: "POS", PropertyScoped: true,
	OrderBy: "reserved_for DESC, id DESC",
	Fields: []resource.Field{
		{Name: "outletId", Column: "outlet_id", Label: "Outlet", Kind: resource.UUID, Required: true, Filter: true,
			Ref: &resource.Ref{Table: "commercial.outlets", SameProperty: true, Label: "outlet"}},
		{Name: "reservedFor", Column: "reserved_for", Label: "Reserved For", Kind: resource.Timestamp, Required: true},
		{Name: "durationMinutes", Column: "duration_minutes", Label: "Duration (minutes)", Kind: resource.Int, Default: int64(90), Min: resource.Min(15), MaxN: resource.Max(720)},
		{Name: "guestName", Column: "guest_name", Label: "Guest Name", Kind: resource.String, Max: 160, Search: true},
		{Name: "customerId", Column: "customer_id", Label: "Customer", Kind: resource.UUID, Ref: &resource.Ref{Table: "crm.customers", SameProperty: true, Label: "customer"}},
		{Name: "phone", Column: "phone", Label: "Phone", Kind: resource.String, Max: 40, Search: true},
		{Name: "guestCount", Column: "guest_count", Label: "Guests", Kind: resource.Int, Default: int64(2), Min: resource.Min(1), MaxN: resource.Max(200)},
		{Name: "tableIds", Column: "table_ids", Label: "Tables", Kind: resource.StringList, Default: []string{}},
		{Name: "status", Column: "status", Label: "Status", Kind: resource.Enum, Enum: []string{"booked", "seated", "cancelled", "no_show"}, Default: "booked", Filter: true},
		{Name: "orderId", Column: "order_id", Label: "Order", Kind: resource.UUID, ReadOnly: true},
		{Name: "notes", Column: "notes", Label: "Notes", Kind: resource.Text, Max: 500}},
	Hooks: resource.Hooks{BeforeWrite: reservationBeforeWrite},
}

func valOf(v, before map[string]any, k string) any {
	if x, ok := v[k]; ok {
		return x
	}
	return before[k]
}

func strOf(x any) string {
	if x == nil {
		return ""
	}
	return fmt.Sprint(x)
}

// reservationBeforeWrite takes the guest name from the customer and checks
// that the tables belong to the outlet.
func reservationBeforeWrite(ctx context.Context, tx pgx.Tx, v, before map[string]any) error {
	if strOf(valOf(v, before, "guestName")) == "" {
		c := strOf(valOf(v, before, "customerId"))
		if c == "" {
			return handle.Invalid("guestName", "required", "enter the guest name or choose a customer")
		}
		var name string
		if err := tx.QueryRow(ctx, `SELECT name FROM crm.customers WHERE id = $1`, c).Scan(&name); err != nil {
			return handle.Invalid("customerId", "not_found", "customer not found")
		}
		v["guestName"] = name
	}
	ids, _ := valOf(v, before, "tableIds").([]string)
	if len(ids) == 0 {
		return nil
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM commercial.dining_tables WHERE outlet_id = $1 AND id::text = ANY($2) AND archived_at IS NULL`,
		strOf(valOf(v, before, "outletId")), ids).Scan(&n); err != nil {
		return err
	}
	if n != len(ids) {
		return handle.Invalid("tableIds", "invalid", "choose tables of this outlet")
	}
	return nil
}

// TableState is a table on the floor plan with its live state.
type TableState struct {
	ID          uuid.UUID     `json:"id" db:"id"`
	Code        string        `json:"code" db:"code"`
	Area        *string       `json:"area" db:"area"`
	Seats       int           `json:"seats" db:"seats"`
	Shape       string        `json:"shape" db:"shape" enum:"square,round,rect,seat"`
	PosX        float64       `json:"posX" db:"pos_x"`
	PosY        float64       `json:"posY" db:"pos_y"`
	State       string        `json:"state" db:"-" enum:"available,booked,occupied,billed"`
	Order       *TableOrder   `json:"order" db:"-"`
	Reservation *TableBooking `json:"reservation" db:"-"`
}

// TableOrder is the open order seating a table.
type TableOrder struct {
	ID           uuid.UUID `json:"id"`
	OrderNo      string    `json:"orderNo"`
	TableNo      *string   `json:"tableNo"`
	GuestCount   *int      `json:"guestCount"`
	CustomerName *string   `json:"customerName"`
	Total        string    `json:"total"`
	// Kitchen state of the order (Table View: preparing, ready to serve …)
	ServiceStatus string     `json:"serviceStatus" enum:"new,sent,preparing,ready,out_for_delivery,served"`
	SeatedAt      time.Time  `json:"seatedAt"`
	BilledAt      *time.Time `json:"billedAt"`
}

// TableBooking is the reservation due at a table.
type TableBooking struct {
	ID          uuid.UUID  `json:"id"`
	GuestName   string     `json:"guestName"`
	CustomerID  *uuid.UUID `json:"customerId"`
	GuestCount  int        `json:"guestCount"`
	ReservedFor time.Time  `json:"reservedFor"`
	TableIDs    []string   `json:"tableIds"`
}

// bookedWindow: a reservation shows its tables Booked from this long before
// its time until it is seated, cancelled or this long past its time.
const bookedBefore, bookedGrace = 3 * time.Hour, 30 * time.Minute

// Tables returns the tables of an outlet with their state now.
func (m *Module) Tables(ctx context.Context, tx pgx.Tx, outletID uuid.UUID) ([]TableState, error) {
	rows, err := tx.Query(ctx, `SELECT id, code, area, seats, shape, pos_x::float8 AS pos_x, pos_y::float8 AS pos_y FROM commercial.dining_tables
		WHERE outlet_id = $1 AND status = 'active' AND archived_at IS NULL ORDER BY length(code), code`, outletID)
	if err != nil {
		return nil, err
	}
	tables := []TableState{}
	for rows.Next() {
		var t TableState
		if err := rows.Scan(&t.ID, &t.Code, &t.Area, &t.Seats, &t.Shape, &t.PosX, &t.PosY); err != nil {
			rows.Close()
			return nil, err
		}
		t.State = "available"
		tables = append(tables, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	byID := map[uuid.UUID]*TableState{}
	for i := range tables {
		byID[tables[i].ID] = &tables[i]
	}
	// open orders seating tables
	rows, err = tx.Query(ctx, `SELECT o.id, o.order_no, o.table_no, o.guest_count, c.name, o.service_status, o.created_at, o.billed_at, o.table_ids,
		trim_scale(coalesce((SELECT sum(total_amount) FROM commercial.order_lines l WHERE l.order_id = o.id AND l.status = 'active'), 0))::text
		FROM commercial.orders o LEFT JOIN reporting.customer_directory c ON c.id = o.customer_id
		WHERE o.outlet_id = $1 AND o.status = 'open' AND cardinality(o.table_ids) > 0`, outletID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var o TableOrder
		var ids []uuid.UUID
		if err := rows.Scan(&o.ID, &o.OrderNo, &o.TableNo, &o.GuestCount, &o.CustomerName, &o.ServiceStatus, &o.SeatedAt, &o.BilledAt, &ids, &o.Total); err != nil {
			rows.Close()
			return nil, err
		}
		for _, tid := range ids {
			if t := byID[tid]; t != nil {
				oc := o
				t.Order = &oc
				t.State = "occupied"
				if o.BilledAt != nil {
					t.State = "billed"
				}
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// reservations due
	now := clock.Now()
	rows, err = tx.Query(ctx, `SELECT id, guest_name, customer_id, guest_count, reserved_for, table_ids FROM commercial.table_reservations
		WHERE outlet_id = $1 AND status = 'booked' AND reserved_for BETWEEN $2 AND $3 ORDER BY reserved_for`,
		outletID, now.Add(-bookedGrace), now.Add(bookedBefore))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var b TableBooking
		if err := rows.Scan(&b.ID, &b.GuestName, &b.CustomerID, &b.GuestCount, &b.ReservedFor, &b.TableIDs); err != nil {
			return nil, err
		}
		for _, s := range b.TableIDs {
			tid, err := uuid.Parse(s)
			if err != nil {
				continue
			}
			if t := byID[tid]; t != nil && t.Reservation == nil {
				bc := b
				t.Reservation = &bc
				if t.State == "available" {
					t.State = "booked"
				}
			}
		}
	}
	return tables, rows.Err()
}

// seatTables checks the tables an order seats: active tables of the outlet,
// not seated by another open order. It returns their codes.
func seatTables(ctx context.Context, tx pgx.Tx, outletID uuid.UUID, ids []uuid.UUID, except *uuid.UUID) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT id, code FROM commercial.dining_tables WHERE outlet_id = $1 AND id = ANY($2) AND status = 'active'
		AND archived_at IS NULL ORDER BY length(code), code FOR UPDATE`, outletID, ids)
	if err != nil {
		return nil, err
	}
	var codes []string
	found := 0
	for rows.Next() {
		var tid uuid.UUID
		var code string
		if err := rows.Scan(&tid, &code); err != nil {
			rows.Close()
			return nil, err
		}
		codes = append(codes, code)
		found++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	uniq := map[uuid.UUID]bool{}
	for _, tid := range ids {
		uniq[tid] = true
	}
	if found != len(uniq) {
		return nil, handle.Invalid("tableIds", "invalid", "choose active tables of this outlet")
	}
	var busy []string
	if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(DISTINCT t.code), '{}') FROM commercial.orders o
		JOIN commercial.dining_tables t ON t.id = ANY(o.table_ids)
		WHERE o.outlet_id = $1 AND o.status = 'open' AND o.table_ids && $2 AND t.id = ANY($2) AND ($3::uuid IS NULL OR o.id <> $3)`,
		outletID, ids, except).Scan(&busy); err != nil {
		return nil, err
	}
	if len(busy) > 0 {
		return nil, errs.Conflict("table_occupied", "table "+strings.Join(busy, ", ")+" already has an open order")
	}
	return codes, nil
}

// seatReservation marks the reservation seated by the order.
func seatReservation(ctx context.Context, tx pgx.Tx, outletID, resID, orderID uuid.UUID) error {
	tag, err := tx.Exec(ctx, `UPDATE commercial.table_reservations SET status = 'seated', order_id = $3, updated_at = now()
		WHERE id = $1 AND outlet_id = $2 AND status = 'booked'`, resID, outletID, orderID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return handle.Invalid("tableReservationId", "invalid", "the reservation is not booked at this outlet")
	}
	return nil
}

// TablesInput moves an order to other tables, or joins tables to it.
type TablesInput struct {
	TableIDs []uuid.UUID `json:"tableIds" doc:"All the tables the order seats from now on"`
}

// MoveTables seats an open order at other tables (move, join or release).
func (m *Module) MoveTables(ctx context.Context, tx pgx.Tx, oid uuid.UUID, ids []uuid.UUID) (Order, error) {
	o, err := m.lockOrder(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	if o.Status != "open" {
		return o, errs.Conflict("order_closed", "the order is "+o.Status)
	}
	if len(ids) == 0 {
		return o, handle.Invalid("tableIds", "required", "choose at least one table")
	}
	codes, err := seatTables(ctx, tx, o.OutletID, ids, &oid)
	if err != nil {
		return o, err
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.orders SET table_ids = $2, table_no = $3, serving_destination = 'table', updated_at = now() WHERE id = $1`,
		oid, ids, strings.Join(codes, ", ")); err != nil {
		return o, err
	}
	out, err := m.Order(ctx, tx, oid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: audit.ActionUpdate, EntityType: "commercial.order",
		EntityID: oid.String(), EntityLabel: o.OrderNo + " · tables", PropertyID: &o.PropertyID,
		Before: map[string]any{"tableNo": o.TableNo}, After: map[string]any{"tableNo": out.TableNo}})
}

// syncOrderActions replays offline actions on an open order: items added
// and sent to the kitchen, then the tables (the payment follows in the caller).
func (m *Module) syncOrderActions(ctx context.Context, tx pgx.Tx, oid uuid.UUID, in SyncOrder) (Order, error) {
	o, err := m.Order(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	if len(in.Lines) > 0 {
		if o, err = m.AddLines(ctx, tx, oid, in.Lines); err != nil {
			return o, err
		}
		if o, err = m.SendToKitchen(ctx, tx, oid); err != nil {
			return o, err
		}
	}
	if len(in.TableIDs) > 0 {
		if o, err = m.MoveTables(ctx, tx, oid, in.TableIDs); err != nil {
			return o, err
		}
	}
	return o, nil
}

// PresentBill marks the bill of an open order presented to the table
// (Table View: Billed).
func (m *Module) PresentBill(ctx context.Context, tx pgx.Tx, oid uuid.UUID) (Order, error) {
	o, err := m.lockOrder(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	if o.Status != "open" {
		return o, errs.Conflict("order_closed", "the order is "+o.Status)
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.orders SET billed_at = now(), updated_at = now() WHERE id = $1`, oid); err != nil {
		return o, err
	}
	out, err := m.Order(ctx, tx, oid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: audit.ActionUpdate, EntityType: "commercial.order",
		EntityID: oid.String(), EntityLabel: o.OrderNo + " · bill presented", PropertyID: &o.PropertyID, After: map[string]any{"billedAt": out.BilledAt}})
}

func (m *Module) registerTables(reg *route.Registry, eng *resource.Engine) {
	for _, d := range []*resource.Def{DiningTables, TableReservations} {
		eng.Register(reg, d)
	}
	db := m.DB
	add := func(rt route.Route) {
		rt.Module, rt.Tag, rt.Scope = "commercial", "POS", route.ScopeProperty
		reg.Add(rt)
	}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/commercial/outlets/{id}/tables", Summary: "Table View: the tables of an outlet with their state now",
		Permission: "commercial.order.view", Response: TableState{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[TableState], error) {
			id, err := handle.ID(r)
			if err != nil {
				return httpx.Page[TableState]{}, err
			}
			return handle.Page(m.Tables(ctx, tx, id))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/outlets/{id}:quote", Summary: "Tax & service of an amount at the outlet now (cart estimate; nothing is stored)",
		Permission: "commercial.order.view", Request: commercial.CalculateRequest{}, Response: commercial.Breakdown{}, Status: http.StatusOK, NoAudit: "read-only calculation",
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in commercial.CalculateRequest) (commercial.Breakdown, error) {
			id, err := handle.ID(r)
			if err != nil {
				return commercial.Breakdown{}, err
			}
			ou, err := loadOutlet(ctx, tx, id)
			if err != nil {
				return commercial.Breakdown{}, err
			}
			amount, err := handle.Decimal("amount", in.Amount, decimal.Zero)
			if err != nil {
				return commercial.Breakdown{}, err
			}
			now := clock.Now()
			rules, err := commercial.RulesAt(ctx, tx, ou.PropertyID, now)
			if err != nil {
				return commercial.Breakdown{}, err
			}
			var cur string
			if err := tx.QueryRow(ctx, `SELECT currency FROM platform.instance`).Scan(&cur); err != nil {
				return commercial.Breakdown{}, err
			}
			return commercial.CalculateMode(commercial.WithCodes(rules, ou.TaxCodes), amount, cur, now, ou.PricingMode), nil
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/orders/{id}:tables", Summary: "Move Table: seat the order at other tables (move or join)",
		Permission: "commercial.order.create", Request: TablesInput{}, Response: Order{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TablesInput) (Order, error) {
			id, err := handle.ID(r)
			if err != nil {
				return Order{}, err
			}
			return m.MoveTables(ctx, tx, id, in.TableIDs)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/orders/{id}:bill", Summary: "Print Bill: present the bill to the table (Table View: Billed)",
		Permission: "commercial.order.create", Response: Order{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (Order, error) {
			id, err := handle.ID(r)
			if err != nil {
				return Order{}, err
			}
			return m.PresentBill(ctx, tx, id)
		})})
}
