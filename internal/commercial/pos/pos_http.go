package pos

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/resource"
)

type ReasonInput struct {
	Reason string `json:"reason"`
}

type LinesInput struct {
	Lines []LineInput `json:"lines"`
}

type ChargeInput struct {
	FolioID uuid.UUID `json:"folioId" doc:"Open folio of a stay, VIP suite, meeting room or reservation"`
}

type TicketStateInput struct {
	State string `json:"state" enum:"preparing,ready,out_for_delivery,served,cancelled"`
}

type ReceiptInput struct {
	Email string `json:"email"`
}

// ReceiptView is the printable receipt.
type ReceiptView struct {
	OrderID uuid.UUID `json:"orderId"`
	Text    string    `json:"text"`
}

// ProductLookup is a product found by barcode (Pro Shop retail).
type ProductLookup struct {
	ProductID   uuid.UUID  `json:"productId" db:"product_id"`
	VariantID   *uuid.UUID `json:"variantId" db:"variant_id"`
	Name        string     `json:"name" db:"name"`
	Price       string     `json:"price" db:"price"`
	ProductType string     `json:"productType" db:"product_type"`
}

// MenuItem is an item of the menu of an outlet now (POS, member app).
type MenuItem struct {
	ProductID      uuid.UUID `json:"productId" db:"id"`
	Code           string    `json:"code" db:"code"`
	Name           string    `json:"name" db:"name"`
	Category       *string   `json:"category" db:"category"`
	ProductType    string    `json:"productType" db:"product_type"`
	Price          string    `json:"price" db:"price"`
	MemberPrice    *string   `json:"memberPrice" db:"member_price"`
	KitchenStation *string   `json:"kitchenStation" db:"kitchen_station"`
	ImageURL       *string   `json:"imageUrl" db:"image_url" doc:"Product photo (POS menu)"`
	// PRD P4 FR-CNS-07 (additive): stock of retail items sold 1:1 in the
	// warehouses of the outlet (K9 reporting.stock_availability).
	StockTracked bool    `json:"stockTracked" db:"-" doc:"The item is stocked in a warehouse of the outlet (inventory)"`
	Available    *string `json:"available" db:"-" doc:"Available quantity at the outlet (stock-tracked items)"`
	SoldOut      bool    `json:"soldOut" db:"-" doc:"No available stock at the outlet (POS Policies markSoldOut)"`
}

func (m *Module) registerPOS(reg *route.Registry, eng *resource.Engine) {
	for _, d := range []*resource.Def{ProductVariants, ModifierGroups, Modifiers, Menus} {
		eng.Register(reg, d)
	}
	db := m.DB
	add := func(rt route.Route) {
		rt.Module, rt.Tag, rt.Scope = "commercial", "POS", route.ScopeProperty
		reg.Add(rt)
	}
	oid := func(r *http.Request) (uuid.UUID, error) { return handle.ID(r) }
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/commercial/outlets/{id}/menu", Summary: "Items on the menu of an outlet now (cached by offline POS)",
		Permission: "commercial.order.view", Response: MenuItem{}, List: true, Query: []route.Param{{Name: "channel", Enum: []string{"pos", "member_app", "caddy_tablet", "website"}}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[MenuItem], error) {
			outletID, err := oid(r)
			if err != nil {
				return httpx.Page[MenuItem]{}, err
			}
			ch := r.URL.Query().Get("channel")
			if ch == "" {
				ch = "pos"
			}
			return handle.Page(m.MenuNow(ctx, tx, outletID, ch))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/commercial/products:lookup", Summary: "Find a product by barcode (retail)", Permission: "commercial.order.view",
		Response: ProductLookup{}, Query: []route.Param{{Name: "barcode", Required: true}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (ProductLookup, error) {
			return handle.Get[ProductLookup](tx.Query(ctx, `SELECT p.id AS product_id, v.id AS variant_id, coalesce(p.name || ' (' || v.name || ')', p.name) AS name,
				trim_scale(p.price + coalesce(v.price_delta, 0))::text AS price, p.product_type FROM commercial.products p
				LEFT JOIN commercial.product_variants v ON v.product_id = p.id AND v.barcode = $2
				WHERE p.property_id = $1 AND (p.barcode = $2 OR v.barcode = $2) AND p.status = 'active' LIMIT 1`, handle.Property(ctx), r.URL.Query().Get("barcode")))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/orders", Summary: "Create Order (POS, pre-order, on-course, add-on, catering)",
		Permission: "commercial.order.create", Request: OrderInput{}, Response: Order{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in OrderInput) (Order, error) {
			return m.CreateOrder(ctx, tx, handle.Property(ctx), in)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/commercial/orders", Summary: "Orders", Permission: "commercial.order.view", Response: Order{}, List: true,
		Query: []route.Param{{Name: "filter[outletId]"}, {Name: "filter[status]"}, {Name: "filter[customerId]"}, {Name: "filter[shiftId]"}, {Name: "date"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Order], error) {
			lp := httpx.ParseList(r)
			d, err := handle.QueryDate(r, "date", time.Time{})
			if err != nil {
				return httpx.Page[Order]{}, err
			}
			from := time.Time{}
			if !d.IsZero() {
				from = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.Local)
			}
			list, err := handle.List[Order](tx.Query(ctx, orderSelect+` WHERE o.property_id = $1 AND ($2 = '' OR o.outlet_id::text = $2)
				AND ($3 = '' OR o.status = ANY(string_to_array($3, ','))) AND ($4 = '' OR o.customer_id::text = $4) AND ($5 = '' OR o.shift_id::text = $5)
				AND ($6::timestamptz = '0001-01-01T00:00:00Z' OR (o.created_at >= $6 AND o.created_at < $6::timestamptz + interval '1 day'))
				ORDER BY o.created_at DESC LIMIT $7`, handle.Property(ctx), lp.Filters["outletId"], lp.Filters["status"], lp.Filters["customerId"],
				lp.Filters["shiftId"], from, lp.Limit))
			return handle.Page(list, err)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/commercial/orders/{id}", Summary: "View order", Permission: "commercial.order.view", Response: Order{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Order, error) {
			id, err := oid(r)
			if err != nil {
				return Order{}, err
			}
			return m.Order(ctx, tx, id)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/orders/{id}/lines", Summary: "Add items", Permission: "commercial.order.create",
		Request: LinesInput{}, Response: Order{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in LinesInput) (Order, error) {
			id, err := oid(r)
			if err != nil {
				return Order{}, err
			}
			return m.AddLines(ctx, tx, id, in.Lines)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/orders/{id}/lines/{lineId}:discount", Summary: "Apply Discount (above POS Policies limit: supervisor)",
		Permission: "commercial.pos.discount", Request: DiscountInput{}, Response: Order{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in DiscountInput) (Order, error) {
			id, err := oid(r)
			if err != nil {
				return Order{}, err
			}
			lid, err := handle.ID(r, "lineId")
			if err != nil {
				return Order{}, err
			}
			return m.Discount(ctx, tx, id, lid, in)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/orders/{id}/lines/{lineId}:void", Summary: "Void an unpaid item", Permission: "commercial.order.void",
		Request: ReasonInput{}, Response: Order{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReasonInput) (Order, error) {
			id, err := oid(r)
			if err != nil {
				return Order{}, err
			}
			lid, err := handle.ID(r, "lineId")
			if err != nil {
				return Order{}, err
			}
			if err := handle.Required("reason", in.Reason); err != nil {
				return Order{}, err
			}
			return m.VoidLine(ctx, tx, id, lid, in.Reason)
		})})
	simple := func(path, summary, perm string, fn func(ctx context.Context, tx pgx.Tx, id uuid.UUID) (Order, error)) {
		add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/orders/{id}:" + path, Summary: summary, Permission: perm, Response: Order{}, Status: http.StatusOK,
			Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (Order, error) {
				id, err := oid(r)
				if err != nil {
					return Order{}, err
				}
				return fn(ctx, tx, id)
			})})
	}
	simple("send", "Send to Kitchen (KDS per station)", "commercial.order.create", m.SendToKitchen)
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/orders/{id}:void", Summary: "Void an unpaid order", Permission: "commercial.order.void",
		Request: ReasonInput{}, Response: Order{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReasonInput) (Order, error) {
			id, err := oid(r)
			if err != nil {
				return Order{}, err
			}
			if err := handle.Required("reason", in.Reason); err != nil {
				return Order{}, err
			}
			return m.VoidOrder(ctx, tx, id, in.Reason)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/orders/{id}:split", Summary: "Split Bill (per item / per person, or equally)",
		Permission: "commercial.order.pay", Request: SplitInput{}, Response: Order{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in SplitInput) (Order, error) {
			id, err := oid(r)
			if err != nil {
				return Order{}, err
			}
			return m.Split(ctx, tx, id, in)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/orders/{id}:pay", Summary: "Process Payment (multi-tender: cash, card, QRIS, transfer, voucher, member charge)",
		Permission: "commercial.order.pay", Request: PayInput{}, Response: Order{}, Status: http.StatusOK, Idempotent: true,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PayInput) (Order, error) {
			id, err := oid(r)
			if err != nil {
				return Order{}, err
			}
			return m.Pay(ctx, tx, id, in, r.Header.Get("Idempotency-Key"))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/orders/{id}:charge", Summary: "Charge to Stay / Reservation folio",
		Permission: "commercial.order.pay", Request: ChargeInput{}, Response: Order{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ChargeInput) (Order, error) {
			id, err := oid(r)
			if err != nil {
				return Order{}, err
			}
			return m.ChargeToFolio(ctx, tx, id, in.FolioID)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/orders/{id}:refund", Summary: "Refund Transaction (approval)", Permission: "commercial.order.refund",
		Request: ReasonInput{}, Response: OrderRequest{}, Status: http.StatusAccepted,
		Handler: handle.Write(db, http.StatusAccepted, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReasonInput) (OrderRequest, error) {
			id, err := oid(r)
			if err != nil {
				return OrderRequest{}, err
			}
			if err := handle.Required("reason", in.Reason); err != nil {
				return OrderRequest{}, err
			}
			return m.RequestRefund(ctx, tx, id, in.Reason)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/orders/{id}:repeat", Summary: "Repeat Order (one-tap reorder)", Permission: "commercial.order.create",
		Request: OrderInput{}, Response: Order{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in OrderInput) (Order, error) {
			id, err := oid(r)
			if err != nil {
				return Order{}, err
			}
			return m.Repeat(ctx, tx, id, in)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/commercial/orders/{id}/receipt", Summary: "Receipt (thermal text)", Permission: "commercial.order.view",
		Response: ReceiptView{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (ReceiptView, error) {
			id, err := oid(r)
			if err != nil {
				return ReceiptView{}, err
			}
			text, _, err := m.Receipt(ctx, tx, id)
			return ReceiptView{OrderID: id, Text: text}, err
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/orders/{id}:send-receipt", Summary: "Send digital receipt by e-mail", Permission: "commercial.order.view",
		Request: ReceiptInput{}, Status: http.StatusAccepted,
		Handler: handle.Write(db, http.StatusAccepted, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReceiptInput) (any, error) {
			id, err := oid(r)
			if err != nil {
				return nil, err
			}
			if err := handle.Required("email", in.Email); err != nil {
				return nil, err
			}
			text, o, err := m.Receipt(ctx, tx, id)
			if err != nil {
				return nil, err
			}
			if err := m.Notify.Send(ctx, tx, notify.Message{Event: "commercial.receipt", Category: "receipt", Email: in.Email, Channels: []string{notify.ChannelEmail},
				PropertyID: &o.PropertyID, Data: map[string]any{"orderNo": o.OrderNo, "outlet": o.OutletName, "lines": text, "total": o.Total, "payments": ""}}); err != nil {
				return nil, err
			}
			return map[string]any{"sent": true}, audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "send_receipt", EntityType: "commercial.order",
				EntityID: id.String(), EntityLabel: o.OrderNo, PropertyID: &o.PropertyID})
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/commercial/customers/{id}/favorites", Summary: "Favorite Food & Drinks / Most Ordered", Permission: "commercial.order.view",
		Response: FavoriteItem{}, List: true, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[FavoriteItem], error) {
			cid, err := oid(r)
			if err != nil {
				return httpx.Page[FavoriteItem]{}, err
			}
			return handle.Page(m.Favorites(ctx, tx, handle.Property(ctx), cid))
		})})
	// Kitchen Display
	kds := func(rt route.Route) { rt.Tag = "Kitchen Display"; add(rt) }
	kds(route.Route{Method: http.MethodGet, Path: "/api/v1/commercial/kitchen-orders", Summary: "Kitchen Display tickets (scheduled items appear at their time)",
		Permission: "commercial.kitchen.view", Response: Ticket{}, List: true,
		Query: []route.Param{{Name: "station"}, {Name: "status", Description: "active (default) | all | <status>"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Ticket], error) {
			pol, err := m.posPolicy(ctx, tx, handle.Property(ctx))
			if err != nil {
				return httpx.Page[Ticket]{}, err
			}
			st := r.URL.Query().Get("status")
			where := `t.property_id = $1 AND ($2 = '' OR t.station = $2)`
			args := []any{handle.Property(ctx), r.URL.Query().Get("station")}
			switch st {
			case "", "active":
				where += ` AND t.status IN ('received', 'preparing', 'ready', 'out_for_delivery') AND coalesce(t.due_at, t.received_at) <= now() + make_interval(mins => $3)`
				args = append(args, pol.ScheduledLeadMinutes)
			case "all":
				where += ` AND t.received_at > now() - interval '1 day'`
			default:
				where += ` AND t.status = $3`
				args = append(args, st)
			}
			return handle.Page(m.tickets(ctx, tx, where, args...))
		})})
	kds(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/kitchen-orders/{id}:state", Summary: "Kitchen State: Preparing, Ready, Out for Delivery, Served",
		Permission: "commercial.kitchen.update", Request: TicketStateInput{}, Response: Ticket{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TicketStateInput) (Ticket, error) {
			tid, err := oid(r)
			if err != nil {
				return Ticket{}, err
			}
			return m.SetTicketState(ctx, tx, tid, in.State)
		})})
	if m.Realtime != nil {
		kds(route.Route{Method: http.MethodGet, Path: "/api/v1/commercial/kds/stream", Summary: "Kitchen Display live updates (Server-Sent Events)",
			Permission: "commercial.kitchen.view", RawContent: "text/event-stream", Handler: m.Realtime.Stream([]string{"commercial.kds"})})
		add(route.Route{Method: http.MethodGet, Path: "/api/v1/commercial/orders/stream", Summary: "Order status live updates (Server-Sent Events)",
			Permission: "commercial.order.view", RawContent: "text/event-stream", Handler: m.Realtime.Stream([]string{"commercial.orders"})})
	}
	// Shifts
	sh := func(rt route.Route) { rt.Tag = "POS Shift"; add(rt) }
	sh(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/shifts:open", Summary: "Open Shift (opening cash)", Permission: "commercial.shift.manage",
		Request: OpenShiftInput{}, Response: Shift{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in OpenShiftInput) (Shift, error) {
			return m.OpenShift(ctx, tx, handle.Property(ctx), in)
		})})
	sh(route.Route{Method: http.MethodGet, Path: "/api/v1/commercial/shifts", Summary: "Shifts", Permission: "commercial.shift.view", Response: Shift{}, List: true,
		Query: []route.Param{{Name: "filter[status]"}, {Name: "filter[outletId]"}, {Name: "mine", Type: "boolean"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Shift], error) {
			lp := httpx.ParseList(r)
			mine := ""
			if r.URL.Query().Get("mine") == "true" {
				if a := actorID(ctx); a != nil {
					mine = a.String()
				}
			}
			return handle.Page(handle.List[Shift](tx.Query(ctx, shiftSelect+` WHERE s.property_id = $1 AND ($2 = '' OR s.status = $2) AND ($3 = '' OR s.outlet_id::text = $3)
				AND ($4 = '' OR s.cashier_id::text = $4) ORDER BY s.opened_at DESC LIMIT $5`, handle.Property(ctx), lp.Filters["status"], lp.Filters["outletId"], mine, lp.Limit)))
		})})
	sh(route.Route{Method: http.MethodGet, Path: "/api/v1/commercial/shifts/{id}/report", Summary: "X report (running) / Z report (closed)", Permission: "commercial.shift.view",
		Response: ShiftReport{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (ShiftReport, error) {
			sid, err := oid(r)
			if err != nil {
				return ShiftReport{}, err
			}
			rep, err := m.ShiftReport(ctx, tx, sid, "x")
			if err == nil && rep.Shift.Status == "closed" {
				rep.Kind = "z"
			}
			return rep, err
		})})
	sh(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/shifts/{id}/cash-movements", Summary: "Cash in / cash out", Permission: "commercial.shift.manage",
		Request: CashMovementInput{}, Response: ShiftReport{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CashMovementInput) (ShiftReport, error) {
			sid, err := oid(r)
			if err != nil {
				return ShiftReport{}, err
			}
			return m.CashMovement(ctx, tx, sid, in)
		})})
	sh(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/shifts/{id}:close", Summary: "Close Shift (count cash, variance, Z report)", Permission: "commercial.shift.manage",
		Request: CloseShiftInput{}, Response: ShiftReport{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CloseShiftInput) (ShiftReport, error) {
			sid, err := oid(r)
			if err != nil {
				return ShiftReport{}, err
			}
			return m.CloseShift(ctx, tx, sid, in)
		})})
}

// MenuNow returns the products on the active menus of an outlet for a channel.
func (m *Module) MenuNow(ctx context.Context, tx pgx.Tx, outletID uuid.UUID, channel string) ([]MenuItem, error) {
	ou, err := loadOutlet(ctx, tx, outletID)
	if err != nil {
		return nil, err
	}
	items, err := m.menuNow(ctx, tx, ou, outletID, channel)
	if err != nil || len(items) == 0 {
		return items, err
	}
	return items, m.markSoldOut(ctx, tx, ou.PropertyID, outletID, items)
}

// markSoldOut flags stock-tracked items of the menu with their available
// quantity at the outlet from the K9 stock availability read model
// (reporting.stock_availability: retail items sold 1:1 in the warehouses
// mapped to the outlet) and marks those without stock Sold Out (PRD P4
// FR-CNS-07, POS Policies markSoldOut).
func (m *Module) markSoldOut(ctx context.Context, tx pgx.Tx, property, outletID uuid.UUID, items []MenuItem) error {
	pol, err := m.posPolicy(ctx, tx, property)
	if err != nil {
		return err
	}
	ids := make([]uuid.UUID, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ProductID)
	}
	rows, err := tx.Query(ctx, `SELECT product_id, trim_scale(sum(available))::text FROM reporting.stock_availability
		WHERE property_id = $1 AND outlet_id = $2 AND product_id = ANY($3) GROUP BY product_id`, property, outletID, ids)
	if err != nil {
		return err
	}
	avail := map[uuid.UUID]string{}
	for rows.Next() {
		var pid uuid.UUID
		var q string
		if err := rows.Scan(&pid, &q); err != nil {
			rows.Close()
			return err
		}
		avail[pid] = q
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range items {
		q, ok := avail[items[i].ProductID]
		if !ok {
			continue
		}
		items[i].StockTracked, items[i].Available = true, &q
		d, _ := decimal.NewFromString(q)
		items[i].SoldOut = pol.MarkSoldOut && !d.IsPositive()
	}
	return nil
}

func (m *Module) menuNow(ctx context.Context, tx pgx.Tx, ou outlet, outletID uuid.UUID, channel string) ([]MenuItem, error) {
	now := time.Now().In(locOf(ctx, tx))
	wd := int(now.Weekday())
	if wd == 0 {
		wd = 7
	}
	hm := now.Format("15:04")
	var menus int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM commercial.menus WHERE outlet_id = $1 AND status = 'active' AND archived_at IS NULL`, outletID).Scan(&menus); err != nil {
		return nil, err
	}
	if menus == 0 {
		// no menus configured: every active product sold at the outlet
		return handle.List[MenuItem](tx.Query(ctx, `SELECT id, code, name, category, product_type, trim_scale(price)::text AS price,
			trim_scale(member_price)::text AS member_price, kitchen_station, image_url FROM commercial.products WHERE property_id = $1 AND status = 'active'
			AND archived_at IS NULL AND (cardinality(outlet_ids) = 0 OR $2 = ANY(outlet_ids)) ORDER BY category NULLS LAST, name`, ou.PropertyID, outletID.String()))
	}
	return handle.List[MenuItem](tx.Query(ctx, `SELECT DISTINCT p.id, p.code, p.name, p.category, p.product_type, trim_scale(p.price)::text AS price,
		trim_scale(p.member_price)::text AS member_price, p.kitchen_station, p.image_url FROM commercial.menus mn JOIN commercial.products p ON p.id::text = ANY(mn.product_ids)
		WHERE mn.outlet_id = $1 AND mn.status = 'active' AND mn.archived_at IS NULL AND $2 = ANY(mn.channels) AND $3 = ANY(mn.weekdays)
		AND (mn.available_from IS NULL OR mn.available_from <= $4::time) AND (mn.available_to IS NULL OR mn.available_to > $4::time)
		AND p.status = 'active' ORDER BY p.category NULLS LAST, p.name`, outletID, channel, wd, hm))
}

func locOf(ctx context.Context, tx pgx.Tx) *time.Location {
	var name string
	if err := tx.QueryRow(ctx, `SELECT timezone FROM platform.instance`).Scan(&name); err != nil {
		return time.UTC
	}
	l, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return l
}
