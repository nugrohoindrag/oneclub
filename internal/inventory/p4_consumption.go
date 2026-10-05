package inventory

// PRD P4 EP-06 Consumption Otomatis and the procurement contract: stock is
// deducted from events, never through calls of other modules (PRD P4
// §5.4.1):
//
//	commercial.sale_completed (K7)   → consumption of the outlet's warehouse
//	commercial.package_consumed (K6) → consumption of the component
//	banquet.event_completed (K6)     → consumption of the banquet kitchen
//	golf.round_finished              → service BOM per player (golf.round)
//	procurement.goods_received       → receipt at the base unit cost
//	procurement.purchase_returned    → return_out at the base unit cost
//	banquet.beo_issued / beo_revised (K1)  → scheduled production (FR-PRD-05)
//
// Every subscriber is idempotent per source (Stock.Post) and tolerant: an
// unknown item or warehouse becomes a posting exception instead of failing
// the event (no event is lost, none blocks the outbox).

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/outbox"
)

// stockExplode returns the base-UOM stock consumption of `units` yield units
// of a recipe. Sub-recipes with an output item are consumed from stock
// (produced semi-finished items) when the configuration says so; other
// sub-recipes are exploded into their ingredients (FR-CNS-01).
func (s *Stock) stockExplode(ctx context.Context, q dbtx.Querier, recipeID uuid.UUID, units decimal.Decimal, depth int, into map[uuid.UUID]decimal.Decimal,
	cfg Configuration) error {
	if depth > 6 {
		return errs.Validation("recipe_cycle", "sub-recipes are nested too deeply (cycle?)")
	}
	r, lines, err := loadRecipe(ctx, q, `id = $1`, recipeID)
	if err != nil {
		return err
	}
	factor := grossUp(units, r.Waste).Div(dec(r.Yield))
	for _, l := range lines {
		qty := grossUp(dec(l.Quantity), l.Waste).Mul(factor)
		if l.ItemID != nil {
			var base uuid.UUID
			if err := q.QueryRow(ctx, `SELECT base_uom_id FROM inventory.items WHERE id = $1`, *l.ItemID).Scan(&base); err != nil {
				return err
			}
			b, err := convert(ctx, q, l.ItemID, qty, l.UOM, base)
			if err != nil {
				return err
			}
			into[*l.ItemID] = into[*l.ItemID].Add(b)
			continue
		}
		sub, _, err := loadRecipe(ctx, q, `id = $1`, *l.SubRecipeID)
		if err != nil {
			return err
		}
		if cfg.ConsumeSemiFinishedStock && sub.OutputItem != nil {
			var base uuid.UUID
			if err := q.QueryRow(ctx, `SELECT base_uom_id FROM inventory.items WHERE id = $1`, *sub.OutputItem).Scan(&base); err != nil {
				return err
			}
			b, err := convert(ctx, q, sub.OutputItem, qty, l.UOM, base)
			if err != nil {
				return err
			}
			into[*sub.OutputItem] = into[*sub.OutputItem].Add(b)
			continue
		}
		subUnits := qty
		if sub.YieldUOM != nil {
			if subUnits, err = convert(ctx, q, sub.OutputItem, qty, l.UOM, *sub.YieldUOM); err != nil {
				return err
			}
		}
		if err := s.stockExplode(ctx, q, sub.ID, subUnits, depth+1, into, cfg); err != nil {
			return err
		}
	}
	return nil
}

// stockConsumption is the stock consumed by one unit of a product: its
// recipe, the retail item sold 1:1, or its combo components, plus modifier
// impacts (FR-CNS-01, FR-INV-05).
func (s *Stock) stockConsumption(ctx context.Context, q dbtx.Querier, property, productID uuid.UUID, modifiers []uuid.UUID, cfg Configuration,
	depth int) (map[uuid.UUID]decimal.Decimal, error) {
	cons := map[uuid.UUID]decimal.Decimal{}
	var rid, item uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM inventory.recipes WHERE product_id = $1 AND status = 'active' AND archived_at IS NULL`, productID).Scan(&rid)
	switch {
	case err == nil:
		if err := s.stockExplode(ctx, q, rid, decimal.NewFromInt(1), 0, cons, cfg); err != nil {
			return nil, err
		}
	case !dbtx.IsNoRows(err):
		return nil, err
	default:
		err := q.QueryRow(ctx, `SELECT id FROM inventory.items WHERE property_id = $1 AND product_id = $2 AND archived_at IS NULL`, property, productID).Scan(&item)
		switch {
		case err == nil:
			cons[item] = decimal.NewFromInt(1)
		case !dbtx.IsNoRows(err):
			return nil, err
		case s.Products != nil && depth < 3:
			combo, err := s.Products.ComboItems(ctx, q, productID)
			if err != nil {
				return nil, err
			}
			for pid, n := range combo {
				sub, err := s.stockConsumption(ctx, q, property, pid, nil, cfg, depth+1)
				if err != nil {
					return nil, err
				}
				if n.IsZero() {
					n = decimal.NewFromInt(1)
				}
				for k, v := range sub {
					cons[k] = cons[k].Add(v.Mul(n))
				}
			}
		}
	}
	for _, mid := range modifiers {
		rows, err := q.Query(ctx, `SELECT action, item_id, replace_item_id, quantity::text, uom_id FROM inventory.modifier_impacts WHERE modifier_id = $1`, mid)
		if err != nil {
			return nil, err
		}
		type imp struct {
			action  string
			item    uuid.UUID
			replace *uuid.UUID
			qty     string
			uom     *uuid.UUID
		}
		var imps []imp
		for rows.Next() {
			var x imp
			if err := rows.Scan(&x.action, &x.item, &x.replace, &x.qty, &x.uom); err != nil {
				rows.Close()
				return nil, err
			}
			imps = append(imps, x)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		for _, x := range imps {
			switch x.action {
			case "remove":
				delete(cons, x.item)
			case "replace":
				if q0, ok := cons[x.item]; ok && x.replace != nil {
					delete(cons, x.item)
					cons[*x.replace] = cons[*x.replace].Add(q0)
				}
			case "add":
				var base uuid.UUID
				if err := q.QueryRow(ctx, `SELECT base_uom_id FROM inventory.items WHERE id = $1`, x.item).Scan(&base); err != nil {
					return nil, err
				}
				qty := dec(x.qty)
				if x.uom != nil {
					if qty, err = convert(ctx, q, &x.item, qty, *x.uom, base); err != nil {
						return nil, err
					}
				}
				cons[x.item] = cons[x.item].Add(qty)
			}
		}
	}
	return cons, nil
}

// consumptionWarehouse picks the warehouse an outlet consumes an item from:
// among the warehouses mapped to the outlet, the one that stocks the item
// (balance or par stock), else the first; without a mapped warehouse, the
// configured fallback.
func consumptionWarehouse(ctx context.Context, q dbtx.Querier, property uuid.UUID, outlet *uuid.UUID, item uuid.UUID, fallback ...string) (*uuid.UUID, error) {
	if outlet != nil {
		var wid uuid.UUID
		err := q.QueryRow(ctx, `SELECT w.id FROM inventory.warehouses w WHERE w.property_id = $1 AND w.outlet_id = $2 AND w.status = 'active' AND w.archived_at IS NULL
			ORDER BY (EXISTS (SELECT 1 FROM inventory.stock_balances b WHERE b.warehouse_id = w.id AND b.item_id = $3)
			  OR EXISTS (SELECT 1 FROM inventory.par_stocks p WHERE p.warehouse_id = w.id AND p.item_id = $3)) DESC, w.code LIMIT 1`,
			property, *outlet, item).Scan(&wid)
		if err == nil {
			return &wid, nil
		}
		if !dbtx.IsNoRows(err) {
			return nil, err
		}
	}
	for _, code := range fallback {
		if wid, err := warehouseByCode(ctx, q, property, code); err != nil || wid != nil {
			return wid, err
		}
	}
	return nil, nil
}

// exception records an event line that could not be posted.
func exception(ctx context.Context, tx pgx.Tx, property uuid.UUID, e outbox.Event, sourceType string, sourceID *uuid.UUID, item, wh *uuid.UUID,
	q *decimal.Decimal, reason string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM inventory.posting_exceptions WHERE property_id = $1 AND source_type = $2
		AND source_id IS NOT DISTINCT FROM $3 AND item_id IS NOT DISTINCT FROM $4 AND status = 'open' AND reason = $5)`, property, sourceType, sourceID, item,
		reason).Scan(&exists); err != nil || exists {
		return err
	}
	var qs any
	if q != nil {
		qs = q.String()
	}
	slog.WarnContext(ctx, "inventory posting exception", "event", e.Type, "source", sourceType, "reason", reason)
	_, err := tx.Exec(ctx, `INSERT INTO inventory.posting_exceptions (id, property_id, event_type, event_id, source_type, source_id, item_id, warehouse_id, quantity, reason)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10)`, id.New(), property, e.Type, e.ID, sourceType, sourceID, item, wh, qs, reason)
	return err
}

// knownItem reports whether an item exists in the property.
func knownItem(ctx context.Context, q dbtx.Querier, property, item uuid.UUID) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM inventory.items WHERE id = $1 AND property_id = $2)`, item, property).Scan(&ok)
	return ok, err
}

func knownWarehouse(ctx context.Context, q dbtx.Querier, property, wh uuid.UUID) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM inventory.warehouses WHERE id = $1 AND property_id = $2)`, wh, property).Scan(&ok)
	return ok, err
}

// consumed are the lines of one automatic consumption grouped by warehouse.
type consumed map[uuid.UUID][]PostLine

func (c consumed) add(wh uuid.UUID, l PostLine) { c[wh] = append(c[wh], l) }

func (s *Stock) postConsumption(ctx context.Context, tx pgx.Tx, property uuid.UUID, c consumed, base PostInput) error {
	for wh, lines := range c {
		in := base
		in.WarehouseID, in.Lines, in.Type, in.Auto = wh, lines, MoveConsumption, true
		if _, _, err := s.Post(ctx, tx, property, in); err != nil {
			if de, ok := errs.As(err); ok && de.Code == "no_stock_lines" {
				continue
			}
			return err
		}
	}
	return nil
}

func eventCtx(ctx context.Context, property uuid.UUID) context.Context {
	return reqctx.WithProperty(dbtx.System(ctx), property)
}

func localDay(ctx context.Context, q dbtx.Querier, t time.Time) time.Time {
	return dateOnly(t.In(localNow(ctx, q).Location()))
}

// OnSaleCompleted deducts the stock of a completed POS sale (K7, FR-CNS-01).
func (s *Stock) OnSaleCompleted(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		OrderID  uuid.UUID  `json:"orderId"`
		OrderNo  string     `json:"orderNo"`
		OutletID *uuid.UUID `json:"outletId"`
		At       time.Time  `json:"at"`
		Lines    []struct {
			LineID      uuid.UUID `json:"lineId"`
			ProductID   uuid.UUID `json:"productId"`
			Quantity    string    `json:"quantity"`
			ModifierIDs []string  `json:"modifierIds"`
			NetAmount   string    `json:"netAmount"`
		} `json:"lines"`
	}
	if err := e.Decode(&p); err != nil || e.PropertyID == nil || p.OrderID == uuid.Nil {
		return nil //nolint:nilerr // malformed or property-less events are not stock events
	}
	property := *e.PropertyID
	ctx = eventCtx(ctx, property)
	if p.At.IsZero() {
		p.At = e.OccurredAt
	}
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return err
	}
	c := consumed{}
	for _, l := range p.Lines {
		var mods []uuid.UUID
		for _, m := range l.ModifierIDs {
			if u, err := uuid.Parse(m); err == nil {
				mods = append(mods, u)
			}
		}
		per, err := s.stockConsumption(ctx, tx, property, l.ProductID, mods, cfg, 0)
		if err != nil {
			if de, ok := errs.As(err); ok && de.Kind == errs.KindValidation {
				lid := l.LineID
				if err := exception(ctx, tx, property, e, "sale", &lid, nil, nil, nil, "recipe data incomplete: "+de.Message); err != nil {
					return err
				}
				continue
			}
			return err
		}
		units := dec(l.Quantity)
		single := len(per) == 1
		for item, q := range per {
			total := q.Mul(units)
			if !total.IsPositive() {
				continue
			}
			wh, err := consumptionWarehouse(ctx, tx, property, p.OutletID, item, cfg.DefaultConsumptionWarehouse)
			if err != nil {
				return err
			}
			if wh == nil {
				it := item
				if err := exception(ctx, tx, property, e, "sale", &p.OrderID, &it, nil, &total, "no warehouse mapped to the outlet"); err != nil {
					return err
				}
				continue
			}
			pl := PostLine{ItemID: item, Quantity: total.Neg()}
			if single {
				net := dec(l.NetAmount)
				pl.SalesAmount = &net
			}
			c.add(*wh, pl)
		}
	}
	return s.postConsumption(ctx, tx, property, c, PostInput{SourceType: "sale", SourceID: &p.OrderID, OutletID: p.OutletID,
		BusinessDate: localDay(ctx, tx, p.At), Reason: "POS sale " + p.OrderNo})
}

type consumptionLine struct {
	ItemID   uuid.UUID  `json:"itemId"`
	Quantity string     `json:"quantity"`
	UOMID    *uuid.UUID `json:"uomId"`
}

// eventLines converts contract consumption lines (base UOM) into warehouse
// groups; unknown items become exceptions.
func (s *Stock) eventLines(ctx context.Context, tx pgx.Tx, property uuid.UUID, e outbox.Event, sourceType string, sourceID uuid.UUID, outlet *uuid.UUID,
	lines []consumptionLine, cfg Configuration, fallback ...string) (consumed, error) {
	c := consumed{}
	for _, l := range lines {
		q := dec(l.Quantity)
		if !q.IsPositive() {
			continue
		}
		ok, err := knownItem(ctx, tx, property, l.ItemID)
		if err != nil {
			return nil, err
		}
		item := l.ItemID
		if !ok {
			if err := exception(ctx, tx, property, e, sourceType, &sourceID, &item, nil, &q, "unknown item"); err != nil {
				return nil, err
			}
			continue
		}
		if l.UOMID != nil {
			it, err := loadItem(ctx, tx, property, item, cfg.ValuationMethod)
			if err != nil {
				return nil, err
			}
			if q, err = toBase(ctx, tx, it, q, l.UOMID); err != nil {
				if err := exception(ctx, tx, property, e, sourceType, &sourceID, &item, nil, &q, "no UOM conversion"); err != nil {
					return nil, err
				}
				continue
			}
		}
		wh, err := consumptionWarehouse(ctx, tx, property, outlet, item, append(fallback, cfg.DefaultConsumptionWarehouse)...)
		if err != nil {
			return nil, err
		}
		if wh == nil {
			if err := exception(ctx, tx, property, e, sourceType, &sourceID, &item, nil, &q, "no consumption warehouse configured"); err != nil {
				return nil, err
			}
			continue
		}
		c.add(*wh, PostLine{ItemID: item, Quantity: q.Neg()})
	}
	return c, nil
}

// OnPackageConsumed deducts the stock of a consumed package component (K6).
func (s *Stock) OnPackageConsumed(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		BookingID          uuid.UUID         `json:"bookingId"`
		BookingComponentID uuid.UUID         `json:"bookingComponentId"`
		ConsumedAt         time.Time         `json:"consumedAt"`
		BusinessDate       string            `json:"businessDate"`
		Quantity           string            `json:"quantity"`
		OutletID           *uuid.UUID        `json:"outletId"`
		RecipeID           *uuid.UUID        `json:"recipeId"`
		ProductID          *uuid.UUID        `json:"productId"`
		Consumption        []consumptionLine `json:"consumption"`
	}
	if err := e.Decode(&p); err != nil || e.PropertyID == nil {
		return nil //nolint:nilerr // not a stock event
	}
	property := *e.PropertyID
	ctx = eventCtx(ctx, property)
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return err
	}
	// One consumption of a component: idempotent per component and time.
	src := e.ID
	if p.BookingComponentID != uuid.Nil {
		src = uuid.NewSHA1(p.BookingComponentID, []byte(p.ConsumedAt.UTC().Format(time.RFC3339Nano)+"/"+p.BusinessDate))
	}
	lines := p.Consumption
	if len(lines) == 0 {
		units := dec(p.Quantity)
		if !units.IsPositive() {
			units = decimal.NewFromInt(1)
		}
		per := map[uuid.UUID]decimal.Decimal{}
		switch {
		case p.RecipeID != nil:
			err = s.stockExplode(ctx, tx, *p.RecipeID, units, 0, per, cfg)
		case p.ProductID != nil:
			per, err = s.stockConsumption(ctx, tx, property, *p.ProductID, nil, cfg, 0)
			for k, v := range per {
				per[k] = v.Mul(units)
			}
		}
		if err != nil {
			if de, ok := errs.As(err); ok && (de.Kind == errs.KindValidation || de.Kind == errs.KindNotFound) {
				return exception(ctx, tx, property, e, "package", &src, nil, nil, nil, "recipe data incomplete: "+de.Message)
			}
			return err
		}
		for k, v := range per {
			lines = append(lines, consumptionLine{ItemID: k, Quantity: v.String()})
		}
	}
	c, err := s.eventLines(ctx, tx, property, e, "package", src, p.OutletID, lines, cfg, cfg.PackageWarehouse)
	if err != nil {
		return err
	}
	bd, _ := parseDate(p.BusinessDate)
	if bd.IsZero() && !p.ConsumedAt.IsZero() {
		bd = localDay(ctx, tx, p.ConsumedAt)
	}
	return s.postConsumption(ctx, tx, property, c, PostInput{SourceType: "package", SourceID: &src, OutletID: p.OutletID, BusinessDate: bd,
		Reason: "Package consumption"})
}

// OnBanquetCompleted deducts the banquet BOM per final pax (K6, FR-CNS-03).
func (s *Stock) OnBanquetCompleted(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		EventID      uuid.UUID         `json:"eventId"`
		Number       string            `json:"number"`
		CompletedAt  time.Time         `json:"completedAt"`
		BusinessDate string            `json:"businessDate"`
		OutletID     *uuid.UUID        `json:"outletId"`
		Consumption  []consumptionLine `json:"consumption"`
	}
	if err := e.Decode(&p); err != nil || e.PropertyID == nil || p.EventID == uuid.Nil {
		return nil //nolint:nilerr // not a stock event
	}
	property := *e.PropertyID
	ctx = eventCtx(ctx, property)
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return err
	}
	c, err := s.eventLines(ctx, tx, property, e, "banquet", p.EventID, p.OutletID, p.Consumption, cfg, cfg.BanquetWarehouse)
	if err != nil {
		return err
	}
	bd, _ := parseDate(p.BusinessDate)
	if bd.IsZero() && !p.CompletedAt.IsZero() {
		bd = localDay(ctx, tx, p.CompletedAt)
	}
	return s.postConsumption(ctx, tx, property, c, PostInput{SourceType: "banquet", SourceID: &p.EventID, OutletID: p.OutletID, BusinessDate: bd,
		Reason: "Banquet event " + p.Number})
}

// OnRoundFinished deducts the service BOM of a golf round per player
// (FR-CNS-04: e.g. one bottle of mineral water per round, at Golf Ops).
func (s *Stock) OnRoundFinished(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		FlightID    uuid.UUID `json:"flightId"`
		HolesPlayed *int      `json:"holesPlayed"`
		Players     int       `json:"players"`
	}
	if err := e.Decode(&p); err != nil || e.PropertyID == nil || p.FlightID == uuid.Nil {
		return nil //nolint:nilerr // not a stock event
	}
	property := *e.PropertyID
	ctx = eventCtx(ctx, property)
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return err
	}
	refs := []string{cfg.GolfRoundServiceRef}
	if p.HolesPlayed != nil && *p.HolesPlayed > 0 {
		refs = []string{cfg.GolfRoundServiceRef + "." + itoa(*p.HolesPlayed), cfg.GolfRoundServiceRef}
	}
	var rid uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM inventory.recipes WHERE property_id = $1 AND recipe_type = 'service' AND service_ref = ANY($2)
		AND status = 'active' AND archived_at IS NULL ORDER BY array_position($2, service_ref), code LIMIT 1`, property, refs).Scan(&rid)
	if dbtx.IsNoRows(err) {
		return nil // no service BOM for golf rounds
	}
	if err != nil {
		return err
	}
	// Players of the flight from the golf read model (checked-in players).
	players := 0
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM reporting.golf_rounds WHERE flight_id = $1`, p.FlightID).Scan(&players); err != nil {
		return err
	}
	if players == 0 {
		players = p.Players
	}
	src := p.FlightID
	if players <= 0 {
		return exception(ctx, tx, property, e, "golf_round", &src, nil, nil, nil, "no players found for the flight")
	}
	per := map[uuid.UUID]decimal.Decimal{}
	if err := s.stockExplode(ctx, tx, rid, decimal.NewFromInt(int64(players)), 0, per, cfg); err != nil {
		if de, ok := errs.As(err); ok && de.Kind == errs.KindValidation {
			return exception(ctx, tx, property, e, "golf_round", &src, nil, nil, nil, "service BOM incomplete: "+de.Message)
		}
		return err
	}
	var lines []consumptionLine
	for k, v := range per {
		lines = append(lines, consumptionLine{ItemID: k, Quantity: v.String()})
	}
	c, err := s.eventLines(ctx, tx, property, e, "golf_round", src, nil, lines, cfg, cfg.GolfRoundWarehouse)
	if err != nil {
		return err
	}
	return s.postConsumption(ctx, tx, property, c, PostInput{SourceType: "golf_round", SourceID: &src, CostCenter: "golf_ops",
		BusinessDate: localDay(ctx, tx, e.OccurredAt), Reason: "Golf round service BOM"})
}

// OnGoodsReceived posts a procurement goods receipt into the warehouse at
// the base unit cost of the PO (procurement.goods_received).
func (s *Stock) OnGoodsReceived(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		GoodsReceiptID uuid.UUID `json:"goodsReceiptId"`
		Number         string    `json:"number"`
		PONumber       string    `json:"poNumber"`
		SupplierID     uuid.UUID `json:"supplierId"`
		WarehouseID    uuid.UUID `json:"warehouseId"`
		ReceivedDate   string    `json:"receivedDate"`
		Lines          []struct {
			ItemID       uuid.UUID `json:"itemId"`
			BaseQuantity string    `json:"baseQuantity"`
			BaseUnitCost string    `json:"baseUnitCost"`
			BatchNo      string    `json:"batchNo"`
			ExpiryDate   string    `json:"expiryDate"`
			SerialNos    []string  `json:"serialNos"`
		} `json:"lines"`
	}
	if err := e.Decode(&p); err != nil || e.PropertyID == nil || p.GoodsReceiptID == uuid.Nil {
		return nil //nolint:nilerr // not a stock event
	}
	property := *e.PropertyID
	ctx = eventCtx(ctx, property)
	src := p.GoodsReceiptID
	ok, err := knownWarehouse(ctx, tx, property, p.WarehouseID)
	if err != nil {
		return err
	}
	if !ok {
		wh := p.WarehouseID
		return exception(ctx, tx, property, e, "goods_receipt", &src, nil, &wh, nil, "unknown warehouse")
	}
	var lines []PostLine
	for _, l := range p.Lines {
		q := dec(l.BaseQuantity)
		if !q.IsPositive() {
			continue
		}
		item := l.ItemID
		known, err := knownItem(ctx, tx, property, item)
		if err != nil {
			return err
		}
		if !known {
			if err := exception(ctx, tx, property, e, "goods_receipt", &src, &item, &p.WarehouseID, &q, "unknown item"); err != nil {
				return err
			}
			continue
		}
		c := dec(l.BaseUnitCost)
		pl := PostLine{ItemID: item, Quantity: q, UnitCost: &c, BatchNo: l.BatchNo, SerialNos: l.SerialNos}
		if d, err := parseDate(l.ExpiryDate); err == nil {
			pl.ExpiryDate = &d
		}
		lines = append(lines, pl)
	}
	if len(lines) == 0 {
		return nil
	}
	bd, _ := parseDate(p.ReceivedDate)
	supplier := p.SupplierID
	var sup *uuid.UUID
	if supplier != uuid.Nil {
		sup = &supplier
	}
	m, created, err := s.Post(ctx, tx, property, PostInput{Type: MoveReceipt, SourceType: "goods_receipt", SourceID: &src, WarehouseID: p.WarehouseID,
		SupplierID: sup, BusinessDate: bd, Auto: true, Reason: "Goods Receipt " + p.Number, Notes: strings.TrimSpace("PO " + p.PONumber), Lines: lines})
	if err != nil || !created {
		return err
	}
	// Receipts close the open purchase requests ("on order") and refresh the
	// supplier's last price.
	for _, l := range m.Lines {
		q := dec(l.Quantity)
		if err := closeReorderRequests(ctx, tx, p.WarehouseID, l.ItemID, q); err != nil {
			return err
		}
		if sup != nil {
			if _, err := tx.Exec(ctx, `INSERT INTO inventory.supplier_items (id, property_id, item_id, supplier_id, last_price) VALUES ($1,$2,$3,$4,$5::numeric)
				ON CONFLICT (item_id, supplier_id) DO UPDATE SET last_price = EXCLUDED.last_price, updated_at = now()`,
				id.New(), property, l.ItemID, *sup, l.UnitCost); err != nil {
				return err
			}
		}
	}
	return nil
}

// closeReorderRequests applies a receipt to the open purchase requests of
// the warehouse × item, oldest first.
func closeReorderRequests(ctx context.Context, tx pgx.Tx, wh, item uuid.UUID, received decimal.Decimal) error {
	rows, err := tx.Query(ctx, `SELECT id, (suggested_quantity - received_quantity)::text FROM inventory.reorder_requests
		WHERE warehouse_id = $1 AND item_id = $2 AND status = 'open' ORDER BY business_date, created_at FOR UPDATE`, wh, item)
	if err != nil {
		return err
	}
	type open struct {
		id   uuid.UUID
		left decimal.Decimal
	}
	var list []open
	for rows.Next() {
		var o open
		var l string
		if err := rows.Scan(&o.id, &l); err != nil {
			rows.Close()
			return err
		}
		o.left = dec(l)
		list = append(list, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, o := range list {
		if !received.IsPositive() {
			break
		}
		take := decimal.Min(o.left, received)
		received = received.Sub(take)
		if _, err := tx.Exec(ctx, `UPDATE inventory.reorder_requests SET received_quantity = received_quantity + $2::numeric,
			status = CASE WHEN received_quantity + $2::numeric >= suggested_quantity THEN 'received' ELSE status END WHERE id = $1`, o.id, take.String()); err != nil {
			return err
		}
	}
	return nil
}

// OnPurchaseReturned takes returned goods out of the warehouse at the base
// unit cost (procurement.purchase_returned).
func (s *Stock) OnPurchaseReturned(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		PurchaseReturnID uuid.UUID `json:"purchaseReturnId"`
		Number           string    `json:"number"`
		SupplierID       uuid.UUID `json:"supplierId"`
		WarehouseID      uuid.UUID `json:"warehouseId"`
		Lines            []struct {
			ItemID       uuid.UUID `json:"itemId"`
			BaseQuantity string    `json:"baseQuantity"`
			BaseUnitCost string    `json:"baseUnitCost"`
			BatchNo      string    `json:"batchNo"`
		} `json:"lines"`
	}
	if err := e.Decode(&p); err != nil || e.PropertyID == nil || p.PurchaseReturnID == uuid.Nil {
		return nil //nolint:nilerr // not a stock event
	}
	property := *e.PropertyID
	ctx = eventCtx(ctx, property)
	src := p.PurchaseReturnID
	ok, err := knownWarehouse(ctx, tx, property, p.WarehouseID)
	if err != nil {
		return err
	}
	if !ok {
		wh := p.WarehouseID
		return exception(ctx, tx, property, e, "purchase_return", &src, nil, &wh, nil, "unknown warehouse")
	}
	var lines []PostLine
	for _, l := range p.Lines {
		q := dec(l.BaseQuantity)
		if !q.IsPositive() {
			continue
		}
		item := l.ItemID
		known, err := knownItem(ctx, tx, property, item)
		if err != nil {
			return err
		}
		if !known {
			if err := exception(ctx, tx, property, e, "purchase_return", &src, &item, &p.WarehouseID, &q, "unknown item"); err != nil {
				return err
			}
			continue
		}
		c := dec(l.BaseUnitCost)
		pl := PostLine{ItemID: item, Quantity: q.Neg(), UnitCost: &c, BatchNo: l.BatchNo}
		if pl.BatchNo != "" {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM inventory.batches WHERE property_id = $1 AND item_id = $2 AND batch_no = $3)`,
				property, item, pl.BatchNo).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				pl.BatchNo = ""
			}
		}
		lines = append(lines, pl)
	}
	if len(lines) == 0 {
		return nil
	}
	var sup *uuid.UUID
	if p.SupplierID != uuid.Nil {
		sup = &p.SupplierID
	}
	_, _, err = s.Post(ctx, tx, property, PostInput{Type: MoveReturnOut, SourceType: "purchase_return", SourceID: &src, WarehouseID: p.WarehouseID,
		SupplierID: sup, Auto: true, ExplicitCost: true, Reason: "Purchase Return " + p.Number, Lines: lines})
	return err
}

// OnBEOIssued schedules the production of the semi-finished items of a BEO
// (banquet.beo_issued / banquet.beo_revised, FR-PRD-05): every menu recipe of
// the requirements is exploded per final pax with produced sub-recipes kept
// as their output items; one draft production order per semi-finished item
// in the banquet kitchen, scheduled for the day before the event. A revision
// replaces the drafts (completed orders stay).
func (s *Stock) OnBEOIssued(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		BEONumber    string `json:"beoNumber"`
		Version      int    `json:"version"`
		EventID      uuid.UUID
		EventNumber  string `json:"eventNumber"`
		EventDate    string `json:"eventDate"`
		Pax          int    `json:"pax"`
		OutletID     *uuid.UUID
		Requirements []struct {
			Source   string     `json:"source"`
			RecipeID *uuid.UUID `json:"recipeId"`
			NeededBy string     `json:"neededBy"`
		} `json:"requirements"`
	}
	var raw struct {
		EventID  uuid.UUID  `json:"eventId"`
		OutletID *uuid.UUID `json:"outletId"`
	}
	if err := e.Decode(&p); err != nil || e.Decode(&raw) != nil || e.PropertyID == nil || raw.EventID == uuid.Nil {
		return nil //nolint:nilerr // not a BEO event
	}
	p.EventID, p.OutletID = raw.EventID, raw.OutletID
	property := *e.PropertyID
	ctx = eventCtx(ctx, property)
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return err
	}
	cfg.ConsumeSemiFinishedStock = true
	// Semi-finished quantities of the menu recipes per final pax.
	need := map[uuid.UUID]decimal.Decimal{}
	due := map[uuid.UUID]string{}
	seen := map[uuid.UUID]bool{}
	for _, r := range p.Requirements {
		if r.RecipeID == nil || seen[*r.RecipeID] || (r.Source != "" && r.Source != "menu") || p.Pax <= 0 {
			continue
		}
		seen[*r.RecipeID] = true
		per := map[uuid.UUID]decimal.Decimal{}
		if err := s.stockExplode(ctx, tx, *r.RecipeID, decimal.NewFromInt(int64(p.Pax)), 0, per, cfg); err != nil {
			if de, ok := errs.As(err); ok && (de.Kind == errs.KindValidation || de.Kind == errs.KindNotFound) {
				src := p.EventID
				if err := exception(ctx, tx, property, e, "banquet", &src, nil, nil, nil, "BEO recipe incomplete: "+de.Message); err != nil {
					return err
				}
				continue
			}
			return err
		}
		for item, q := range per {
			need[item] = need[item].Add(q)
			if r.NeededBy != "" && (due[item] == "" || r.NeededBy < due[item]) {
				due[item] = r.NeededBy
			}
		}
	}
	// Keep the items produced by an active sub-recipe.
	type plan struct {
		recipe uuid.UUID
		qty    decimal.Decimal
		on     *time.Time
	}
	plans := map[uuid.UUID]plan{}
	for item, q := range need {
		var rid uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM inventory.recipes WHERE property_id = $1 AND output_item_id = $2 AND recipe_type = 'sub_recipe'
			AND status = 'active' AND archived_at IS NULL ORDER BY code LIMIT 1`, property, item).Scan(&rid)
		if dbtx.IsNoRows(err) {
			continue
		}
		if err != nil {
			return err
		}
		var on *time.Time
		if d, err := parseDate(due[item]); err == nil {
			on = &d
		} else if d, err := parseDate(p.EventDate); err == nil {
			d = d.AddDate(0, 0, -1)
			on = &d
		}
		plans[item] = plan{rid, q.Round(6), on}
	}
	wh, err := consumptionWarehouse(ctx, tx, property, p.OutletID, uuid.Nil, cfg.BanquetWarehouse, cfg.DefaultConsumptionWarehouse)
	if err != nil {
		return err
	}
	if wh == nil {
		if len(plans) == 0 {
			return nil
		}
		src := p.EventID
		return exception(ctx, tx, property, e, "banquet", &src, nil, nil, nil, "no banquet kitchen warehouse configured for the production schedule")
	}
	ref := strings.TrimSpace(p.BEONumber + " v" + itoa(p.Version) + " " + p.EventNumber)
	// Drafts of items no longer required are cancelled; the others follow the BEO.
	if _, err := tx.Exec(ctx, `UPDATE inventory.production_orders SET status = 'cancelled', notes = concat_ws(E'\n', notes, 'Cancelled by ' || $3::text)
		WHERE source_type = 'banquet_event' AND source_id = $1 AND status = 'draft' AND NOT (output_item_id = ANY($2))`, p.EventID, keys(plans), ref); err != nil {
		return err
	}
	for item, pl := range plans {
		var pid uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM inventory.production_orders WHERE source_type = 'banquet_event' AND source_id = $1 AND output_item_id = $2
			AND status <> 'cancelled'`, p.EventID, item).Scan(&pid)
		switch {
		case err == nil:
			var st string
			if err := tx.QueryRow(ctx, `SELECT status FROM inventory.production_orders WHERE id = $1`, pid).Scan(&st); err != nil || st != "draft" {
				if err != nil {
					return err
				}
				continue // already produced
			}
			r, _, err := loadRecipe(ctx, tx, `id = $1`, pl.recipe)
			if err != nil {
				return err
			}
			inputs, err := s.recipeInputs(ctx, tx, property, r, pl.qty)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE inventory.production_orders SET planned_quantity = $2::numeric, scheduled_for = $3, source_ref = $4 WHERE id = $1`,
				pid, pl.qty.String(), pl.on, ref); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM inventory.production_inputs WHERE production_order_id = $1`, pid); err != nil {
				return err
			}
			for in, q := range inputs {
				if _, err := tx.Exec(ctx, `INSERT INTO inventory.production_inputs (id, property_id, production_order_id, item_id, planned_quantity)
					VALUES ($1,$2,$3,$4,$5::numeric)`, id.New(), property, pid, in, q.Round(6).String()); err != nil {
					return err
				}
			}
		case dbtx.IsNoRows(err):
			sched := ""
			if pl.on != nil {
				sched = pl.on.Format("2006-01-02")
			}
			po, err := s.CreateProduction(ctx, tx, property, ProductionOrderInput{RecipeID: pl.recipe, WarehouseID: *wh, PlannedQuantity: pl.qty.String(),
				ScheduledFor: sched, Notes: "Banquet production schedule " + ref})
			if err != nil {
				if de, ok := errs.As(err); ok && de.Kind == errs.KindValidation {
					src := p.EventID
					it := item
					if err := exception(ctx, tx, property, e, "banquet", &src, &it, wh, &pl.qty, "production order: "+de.Message); err != nil {
						return err
					}
					continue
				}
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE inventory.production_orders SET source_type = 'banquet_event', source_id = $2, source_ref = $3 WHERE id = $1`,
				po.ID, p.EventID, ref); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return nil
}

func keys[V any](m map[uuid.UUID]V) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func itoa(n int) string {
	return decimal.NewFromInt(int64(n)).String()
}
