// Package inventory is Inventory & BOM. P2 builds the BOM / Recipe
// foundation (PRD P2 EP-22): items, UOM conversions, recipes with
// sub-recipes, yield and waste, modifier impact, theoretical consumption per
// sale (from commercial.sale_completed) and theoretical food cost. Stock,
// actual consumption and production follow in P4.
package inventory

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/resource"
)

var UOMs = &resource.Def{
	Key: "inventory.uom", Module: "inventory", Perm: "inventory.uom", Path: "/api/v1/inventory/uoms", Table: "inventory.uoms",
	Name: "UOM", Plural: "UOM", Tag: "BOM & Recipes", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "kind, code",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "kind", Column: "kind", Label: "Kind", Kind: resource.Enum, Enum: []string{"mass", "volume", "count", "portion", "length"}, Default: "count", Filter: true},
		resource.Status("active", "inactive")},
}

var Items = &resource.Def{
	Key: "inventory.item", Module: "inventory", Perm: "inventory.item", Path: "/api/v1/inventory/items", Table: "inventory.items",
	Name: "Item", Plural: "Items", SchemaName: "InventoryItem", Tag: "BOM & Recipes", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "category", Column: "category", Label: "Category", Kind: resource.String, Max: 80, Filter: true},
		{Name: "itemType", Column: "item_type", Label: "Item Type", Kind: resource.Enum, Enum: []string{"raw", "semi_finished", "packaging", "consumable"}, Default: "raw", Filter: true},
		{Name: "baseUomId", Column: "base_uom_id", Label: "Stock UOM", Kind: resource.UUID, Required: true, Ref: &resource.Ref{Table: "inventory.uoms", SameProperty: true, Label: "UOM"}},
		{Name: "purchaseUomId", Column: "purchase_uom_id", Label: "Purchase UOM", Kind: resource.UUID, Ref: &resource.Ref{Table: "inventory.uoms", SameProperty: true, Label: "UOM"}},
		{Name: "standardCost", Column: "standard_cost", Label: "Standard Cost per Stock UOM", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		resource.Status("active", "inactive")},
}

var UOMConversions = &resource.Def{
	Key: "inventory.uom_conversion", Module: "inventory", Perm: "inventory.uom", Path: "/api/v1/inventory/uom-conversions", Table: "inventory.uom_conversions",
	Name: "UOM Conversion", Plural: "UOM Conversions", Tag: "BOM & Recipes", PropertyScoped: true, OrderBy: "created_at, id",
	Fields: []resource.Field{
		{Name: "itemId", Column: "item_id", Label: "Item (empty = all)", Kind: resource.UUID, Filter: true, Ref: &resource.Ref{Table: "inventory.items", SameProperty: true, Label: "item"}},
		{Name: "fromUomId", Column: "from_uom_id", Label: "From", Kind: resource.UUID, Required: true, Ref: &resource.Ref{Table: "inventory.uoms", SameProperty: true, Label: "UOM"}},
		{Name: "toUomId", Column: "to_uom_id", Label: "To", Kind: resource.UUID, Required: true, Ref: &resource.Ref{Table: "inventory.uoms", SameProperty: true, Label: "UOM"}},
		{Name: "factor", Column: "factor", Label: "1 From = Factor × To", Kind: resource.Decimal, Required: true, Min: resource.Min(0)}},
}

var Recipes = &resource.Def{
	Key: "inventory.recipe", Module: "inventory", Perm: "inventory.recipe", Path: "/api/v1/inventory/recipes", Table: "inventory.recipes",
	Name: "Recipe", Plural: "BOM & Recipes", Tag: "BOM & Recipes", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "recipeType", Column: "recipe_type", Label: "Recipe Type", Kind: resource.Enum, Enum: []string{"menu", "sub_recipe", "service"}, Default: "menu", Filter: true},
		{Name: "productId", Column: "product_id", Label: "Menu Item", Kind: resource.UUID, Filter: true, Ref: &resource.Ref{Table: "commercial.products", SameProperty: true, Label: "product"}},
		{Name: "outputItemId", Column: "output_item_id", Label: "Output Item (semi-finished)", Kind: resource.UUID, Ref: &resource.Ref{Table: "inventory.items", SameProperty: true, Label: "item"}},
		{Name: "serviceRef", Column: "service_ref", Label: "Service (rate / package code)", Kind: resource.String, Max: 80},
		{Name: "yieldQuantity", Column: "yield_quantity", Label: "Yield", Kind: resource.Decimal, Default: "1", Min: resource.Min(0)},
		{Name: "yieldUomId", Column: "yield_uom_id", Label: "Yield UOM", Kind: resource.UUID, Ref: &resource.Ref{Table: "inventory.uoms", SameProperty: true, Label: "UOM"}},
		{Name: "wastePercent", Column: "waste_percent", Label: "Waste (%)", Kind: resource.Decimal, Default: "0", Min: resource.Min(0), MaxN: resource.Max(99)},
		{Name: "notes", Column: "notes", Label: "Notes", Kind: resource.Text, Max: 2000},
		resource.Status("active", "inactive")},
}

var RecipeLines = &resource.Def{
	Key: "inventory.recipe_line", Module: "inventory", Perm: "inventory.recipe", Path: "/api/v1/inventory/recipe-lines", Table: "inventory.recipe_lines",
	Name: "Recipe Line", Plural: "Recipe Lines", Tag: "BOM & Recipes", PropertyScoped: true, OrderBy: "created_at, id",
	Fields: []resource.Field{
		{Name: "recipeId", Column: "recipe_id", Label: "Recipe", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "inventory.recipes", SameProperty: true, Label: "recipe"}},
		{Name: "itemId", Column: "item_id", Label: "Item", Kind: resource.UUID, Ref: &resource.Ref{Table: "inventory.items", SameProperty: true, Label: "item"}},
		{Name: "subRecipeId", Column: "sub_recipe_id", Label: "Sub-recipe", Kind: resource.UUID, Ref: &resource.Ref{Table: "inventory.recipes", SameProperty: true, Label: "recipe"}},
		{Name: "quantity", Column: "quantity", Label: "Quantity", Kind: resource.Decimal, Required: true, Min: resource.Min(0)},
		{Name: "uomId", Column: "uom_id", Label: "UOM", Kind: resource.UUID, Required: true, Ref: &resource.Ref{Table: "inventory.uoms", SameProperty: true, Label: "UOM"}},
		{Name: "wastePercent", Column: "waste_percent", Label: "Waste (%)", Kind: resource.Decimal, Default: "0", Min: resource.Min(0), MaxN: resource.Max(99)},
		{Name: "notes", Column: "notes", Label: "Notes", Kind: resource.Text, Max: 500}},
}

var ModifierImpacts = &resource.Def{
	Key: "inventory.modifier_impact", Module: "inventory", Perm: "inventory.recipe", Path: "/api/v1/inventory/modifier-impacts", Table: "inventory.modifier_impacts",
	Name: "Modifier Impact", Plural: "Modifier Impacts", Tag: "BOM & Recipes", PropertyScoped: true, OrderBy: "created_at, id",
	Fields: []resource.Field{
		{Name: "modifierId", Column: "modifier_id", Label: "Modifier", Kind: resource.UUID, Required: true, Filter: true, Ref: &resource.Ref{Table: "commercial.modifiers", SameProperty: true, Label: "modifier"}},
		{Name: "action", Column: "action", Label: "Action", Kind: resource.Enum, Enum: []string{"add", "remove", "replace"}, Required: true},
		{Name: "itemId", Column: "item_id", Label: "Item", Kind: resource.UUID, Required: true, Ref: &resource.Ref{Table: "inventory.items", SameProperty: true, Label: "item"}},
		{Name: "replaceItemId", Column: "replace_item_id", Label: "Replacement Item", Kind: resource.UUID, Ref: &resource.Ref{Table: "inventory.items", SameProperty: true, Label: "item"}},
		{Name: "quantity", Column: "quantity", Label: "Quantity per Unit Sold", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "uomId", Column: "uom_id", Label: "UOM", Kind: resource.UUID, Ref: &resource.Ref{Table: "inventory.uoms", SameProperty: true, Label: "UOM"}}},
}

// Module is the Inventory module.
// ProductCatalog is the product data of Commercial that the food cost
// needs; internal/app wires it (back office does not import Commercial,
// Tech Doc §4.2).
type ProductCatalog interface {
	ComboItems(ctx context.Context, q dbtx.Querier, productID uuid.UUID) (map[uuid.UUID]decimal.Decimal, error)
	SellingPrice(ctx context.Context, q dbtx.Querier, productID uuid.UUID) (decimal.Decimal, error)
	ProductNames(ctx context.Context, q dbtx.Querier, ids []uuid.UUID) (map[uuid.UUID]string, error)
	OutletNames(ctx context.Context, q dbtx.Querier, ids []uuid.UUID) (map[uuid.UUID]string, error)
}

type Module struct {
	DB       *dbtx.DB
	Products ProductCatalog
}

func dec(s string) decimal.Decimal { d, _ := decimal.NewFromString(s); return d }

// convert converts qty from one UOM to another (item-specific conversions
// win over general ones; the inverse direction is used when needed).
func convert(ctx context.Context, q dbtx.Querier, item *uuid.UUID, qty decimal.Decimal, from, to uuid.UUID) (decimal.Decimal, error) {
	if from == to {
		return qty, nil
	}
	var f string
	var inverse bool
	err := q.QueryRow(ctx, `SELECT factor::text, from_uom_id <> $2 FROM inventory.uom_conversions
		WHERE ((from_uom_id = $2 AND to_uom_id = $3) OR (from_uom_id = $3 AND to_uom_id = $2)) AND (item_id IS NULL OR item_id = $1)
		ORDER BY (item_id IS NOT NULL) DESC LIMIT 1`, item, from, to).Scan(&f, &inverse)
	if dbtx.IsNoRows(err) {
		var a, b string
		_ = q.QueryRow(ctx, `SELECT (SELECT code FROM inventory.uoms WHERE id = $1), (SELECT code FROM inventory.uoms WHERE id = $2)`, from, to).Scan(&a, &b)
		return qty, errs.Validation("no_uom_conversion", fmt.Sprintf("no conversion from %s to %s", a, b))
	}
	if err != nil {
		return qty, err
	}
	if inverse {
		return qty.Div(dec(f)), nil
	}
	return qty.Mul(dec(f)), nil
}

type recipe struct {
	ID         uuid.UUID  `db:"id"`
	Name       string     `db:"name"`
	ProductID  *uuid.UUID `db:"product_id"`
	OutputItem *uuid.UUID `db:"output_item_id"`
	Yield      string     `db:"yield_quantity"`
	YieldUOM   *uuid.UUID `db:"yield_uom_id"`
	Waste      string     `db:"waste_percent"`
}

type recipeLine struct {
	ItemID      *uuid.UUID `db:"item_id"`
	SubRecipeID *uuid.UUID `db:"sub_recipe_id"`
	Quantity    string     `db:"quantity"`
	UOM         uuid.UUID  `db:"uom_id"`
	Waste       string     `db:"waste_percent"`
	Name        string     `db:"name"`
	UOMCode     string     `db:"uom_code"`
}

func loadRecipe(ctx context.Context, q dbtx.Querier, where string, arg any) (recipe, []recipeLine, error) {
	rows, err := q.Query(ctx, `SELECT id, name, product_id, output_item_id, yield_quantity::text, yield_uom_id, waste_percent::text FROM inventory.recipes WHERE `+where, arg)
	r, err := handle.One[recipe](rows, err, "recipe")
	if err != nil {
		return r, nil, err
	}
	lines, err := handle.List[recipeLine](q.Query(ctx, `SELECT l.item_id, l.sub_recipe_id, l.quantity::text, l.uom_id, l.waste_percent::text,
		coalesce(i.name, s.name) AS name, u.code AS uom_code FROM inventory.recipe_lines l LEFT JOIN inventory.items i ON i.id = l.item_id
		LEFT JOIN inventory.recipes s ON s.id = l.sub_recipe_id JOIN inventory.uoms u ON u.id = l.uom_id WHERE l.recipe_id = $1 ORDER BY l.created_at, l.id`, r.ID))
	return r, lines, err
}

func grossUp(qty decimal.Decimal, wastePct string) decimal.Decimal {
	w := dec(wastePct)
	if !w.IsPositive() {
		return qty
	}
	return qty.Div(decimal.NewFromInt(1).Sub(w.Div(decimal.NewFromInt(100))))
}

// explode returns the base-UOM consumption of `units` yield units of a
// recipe, resolving sub-recipes (FR-BOM-02).
func explode(ctx context.Context, q dbtx.Querier, recipeID uuid.UUID, units decimal.Decimal, depth int, into map[uuid.UUID]decimal.Decimal) error {
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
		subUnits := qty
		if sub.YieldUOM != nil {
			if subUnits, err = convert(ctx, q, sub.OutputItem, qty, l.UOM, *sub.YieldUOM); err != nil {
				return err
			}
		}
		if err := explode(ctx, q, sub.ID, subUnits, depth+1, into); err != nil {
			return err
		}
	}
	return nil
}

func costOf(ctx context.Context, q dbtx.Querier, cons map[uuid.UUID]decimal.Decimal) (decimal.Decimal, map[uuid.UUID]decimal.Decimal, error) {
	total := decimal.Zero
	unit := map[uuid.UUID]decimal.Decimal{}
	for item, qty := range cons {
		var c string
		if err := q.QueryRow(ctx, `SELECT standard_cost::text FROM inventory.items WHERE id = $1`, item).Scan(&c); err != nil {
			return total, unit, err
		}
		unit[item] = dec(c)
		total = total.Add(qty.Mul(dec(c)))
	}
	return total, unit, nil
}

// productConsumption explodes one unit of a product: its recipe, or its
// combo components' recipes (FR-BOM-03), plus modifier impacts.
func (m *Module) productConsumption(ctx context.Context, q dbtx.Querier, productID uuid.UUID, modifiers []uuid.UUID, depth int) (map[uuid.UUID]decimal.Decimal, bool, error) {
	cons := map[uuid.UUID]decimal.Decimal{}
	has := false
	var rid uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM inventory.recipes WHERE product_id = $1 AND status = 'active' AND archived_at IS NULL`, productID).Scan(&rid)
	switch {
	case err == nil:
		has = true
		if err := explode(ctx, q, rid, decimal.NewFromInt(1), 0, cons); err != nil {
			return nil, true, err
		}
	case dbtx.IsNoRows(err):
		items := map[uuid.UUID]decimal.Decimal{}
		if m.Products != nil {
			if items, err = m.Products.ComboItems(ctx, q, productID); err != nil {
				return nil, false, err
			}
		}
		if depth < 3 {
			for pid, qty := range items {
				sub, ok, err := m.productConsumption(ctx, q, pid, nil, depth+1)
				if err != nil {
					return nil, false, err
				}
				has = has || ok
				if qty.IsZero() {
					qty = decimal.NewFromInt(1)
				}
				for k, v := range sub {
					cons[k] = cons[k].Add(v.Mul(qty))
				}
			}
		}
	default:
		return nil, false, err
	}
	for _, mid := range modifiers {
		rows, err := q.Query(ctx, `SELECT action, item_id, replace_item_id, quantity::text, uom_id FROM inventory.modifier_impacts WHERE modifier_id = $1`, mid)
		if err != nil {
			return nil, has, err
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
				return nil, has, err
			}
			imps = append(imps, x)
		}
		rows.Close()
		for _, x := range imps {
			has = true
			switch x.action {
			case "remove":
				delete(cons, x.item)
			case "replace":
				if q0, ok := cons[x.item]; ok {
					delete(cons, x.item)
					cons[*x.replace] = cons[*x.replace].Add(q0)
				}
			case "add":
				var base uuid.UUID
				if err := q.QueryRow(ctx, `SELECT base_uom_id FROM inventory.items WHERE id = $1`, x.item).Scan(&base); err != nil {
					return nil, has, err
				}
				qty := dec(x.qty)
				if x.uom != nil {
					if qty, err = convert(ctx, q, &x.item, qty, *x.uom, base); err != nil {
						return nil, has, err
					}
				}
				cons[x.item] = cons[x.item].Add(qty)
			}
		}
	}
	return cons, has, nil
}

// SaleCompleted records theoretical consumption for a completed sale
// (commercial.sale_completed subscriber; idempotent per order line).
func (m *Module) SaleCompleted(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		OrderID  uuid.UUID `json:"orderId"`
		OutletID uuid.UUID `json:"outletId"`
		At       time.Time `json:"at"`
		Lines    []struct {
			LineID      uuid.UUID `json:"lineId"`
			ProductID   uuid.UUID `json:"productId"`
			Quantity    string    `json:"quantity"`
			ModifierIDs []string  `json:"modifierIds"`
			NetAmount   string    `json:"netAmount"`
		} `json:"lines"`
	}
	if err := e.Decode(&p); err != nil {
		return err
	}
	if e.PropertyID == nil {
		return nil
	}
	if p.At.IsZero() {
		p.At = e.OccurredAt
	}
	for _, l := range p.Lines {
		var mods []uuid.UUID
		for _, s := range l.ModifierIDs {
			if u, err := uuid.Parse(s); err == nil {
				mods = append(mods, u)
			}
		}
		cons, has, err := m.productConsumption(ctx, tx, l.ProductID, mods, 0)
		if err != nil {
			if de, ok := errs.As(err); ok && de.Kind == errs.KindValidation {
				cons, has = map[uuid.UUID]decimal.Decimal{}, false // recipe data incomplete: record the sale without cost
			} else {
				return err
			}
		}
		qty := dec(l.Quantity)
		total, unit, err := costOf(ctx, tx, cons)
		if err != nil {
			return err
		}
		for item, per := range cons {
			q := per.Mul(qty)
			if _, err := tx.Exec(ctx, `INSERT INTO inventory.consumption_ledger (id, property_id, source_type, source_id, order_id, outlet_id, product_id, item_id,
				quantity, unit_cost, cost_amount, occurred_at) VALUES ($1,$2,'commercial.order_line',$3,$4,$5,$6,$7,$8::numeric,$9::numeric,$10::numeric,$11)
				ON CONFLICT DO NOTHING`, id.New(), *e.PropertyID, l.LineID, p.OrderID, p.OutletID, l.ProductID, item, q.String(), unit[item].String(),
				q.Mul(unit[item]).Round(4).String(), p.At); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO inventory.consumption_sales (source_id, property_id, order_id, outlet_id, product_id, quantity, net_amount, cost_amount,
			has_recipe, occurred_at) VALUES ($1,$2,$3,$4,$5,$6::numeric,$7::numeric,$8::numeric,$9,$10) ON CONFLICT DO NOTHING`,
			l.LineID, *e.PropertyID, p.OrderID, p.OutletID, l.ProductID, qty.String(), dec(l.NetAmount).String(), total.Mul(qty).Round(4).String(), has, p.At); err != nil {
			return err
		}
	}
	return nil
}

// ── HTTP ──────────────────────────────────────────────────────────────────

// CostLine is one ingredient of a recipe cost.
type CostLine struct {
	ItemID   uuid.UUID `json:"itemId"`
	Name     string    `json:"name"`
	Quantity string    `json:"quantity"`
	UOM      string    `json:"uom"`
	UnitCost string    `json:"unitCost"`
	Cost     string    `json:"cost"`
}

// RecipeCost is the theoretical cost of one portion (FR-BOM-05).
type RecipeCost struct {
	RecipeID        uuid.UUID  `json:"recipeId"`
	Name            string     `json:"name"`
	UnitCost        string     `json:"unitCost"`
	SellingPrice    *string    `json:"sellingPrice"`
	FoodCostPercent *string    `json:"foodCostPercent"`
	Lines           []CostLine `json:"lines"`
}

// FoodCostRow is theoretical food cost per product or outlet.
type FoodCostRow struct {
	Key             uuid.UUID `json:"key" db:"key"`
	Name            string    `json:"name" db:"name"`
	Quantity        string    `json:"quantity" db:"quantity"`
	NetSales        string    `json:"netSales" db:"net_sales"`
	TheoreticalCost string    `json:"theoreticalCost" db:"theoretical_cost"`
	FoodCostPercent *string   `json:"foodCostPercent" db:"food_cost_percent"`
	WithoutRecipe   int       `json:"linesWithoutRecipe" db:"without_recipe"`
}

// Consumption is a theoretical consumption row.
type Consumption struct {
	ItemID   uuid.UUID `json:"itemId" db:"item_id"`
	Name     string    `json:"name" db:"name"`
	UOM      string    `json:"uom" db:"uom"`
	Quantity string    `json:"quantity" db:"quantity"`
	Cost     string    `json:"cost" db:"cost"`
}

func (m *Module) RecipeCost(ctx context.Context, q dbtx.Querier, rid uuid.UUID) (RecipeCost, error) {
	r, _, err := loadRecipe(ctx, q, `id = $1`, rid)
	if err != nil {
		return RecipeCost{}, err
	}
	cons := map[uuid.UUID]decimal.Decimal{}
	if err := explode(ctx, q, rid, decimal.NewFromInt(1), 0, cons); err != nil {
		return RecipeCost{}, err
	}
	total, unit, err := costOf(ctx, q, cons)
	if err != nil {
		return RecipeCost{}, err
	}
	out := RecipeCost{RecipeID: rid, Name: r.Name, UnitCost: total.Round(2).String(), Lines: []CostLine{}}
	for item, qty := range cons {
		var name, uom string
		if err := q.QueryRow(ctx, `SELECT i.name, u.code FROM inventory.items i JOIN inventory.uoms u ON u.id = i.base_uom_id WHERE i.id = $1`, item).Scan(&name, &uom); err != nil {
			return out, err
		}
		out.Lines = append(out.Lines, CostLine{ItemID: item, Name: name, Quantity: qty.Round(4).String(), UOM: uom, UnitCost: unit[item].String(),
			Cost: qty.Mul(unit[item]).Round(2).String()})
	}
	if r.ProductID != nil && m.Products != nil {
		if p, err := m.Products.SellingPrice(ctx, q, *r.ProductID); err == nil && p.IsPositive() {
			price := p.String()
			out.SellingPrice = &price
			pct := total.Div(dec(price)).Mul(decimal.NewFromInt(100)).Round(2).String()
			out.FoodCostPercent = &pct
		}
	}
	return out, nil
}

func period(ctx context.Context, q dbtx.Querier, r *http.Request) (time.Time, time.Time, error) {
	loc := calendar.Location(ctx, q)
	n := time.Now().In(loc)
	f, err := handle.QueryDate(r, "from", time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		return f, f, err
	}
	t, err := handle.QueryDate(r, "to", time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC))
	if err != nil {
		return f, t, err
	}
	return time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, loc), time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc), nil
}

// Register adds the BOM routes.
func (m *Module) Register(reg *route.Registry, eng *resource.Engine) {
	for _, d := range []*resource.Def{UOMs, Items, UOMConversions, Recipes, RecipeLines, ModifierImpacts} {
		eng.Register(reg, d)
	}
	add := func(rt route.Route) {
		rt.Module, rt.Tag, rt.Scope = "inventory", "BOM & Recipes", route.ScopeProperty
		reg.Add(rt)
	}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/recipes/{id}/cost", Summary: "Theoretical cost per portion (sub-recipes, yield, waste)",
		Permission: "inventory.recipe.view", Response: RecipeCost{}, Handler: handle.Read(m.DB, func(ctx context.Context, tx pgx.Tx, r *http.Request) (RecipeCost, error) {
			rid, err := handle.ID(r)
			if err != nil {
				return RecipeCost{}, err
			}
			return m.RecipeCost(ctx, tx, rid)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/food-cost", Summary: "Theoretical Food Cost / COGS per menu item or outlet",
		Permission: "inventory.food_cost.view", Response: FoodCostRow{}, List: true,
		Query: []route.Param{{Name: "from"}, {Name: "to"}, {Name: "groupBy", Enum: []string{"product", "outlet"}}, {Name: "outletId"}},
		Handler: handle.Read(m.DB, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[FoodCostRow], error) {
			from, to, err := period(ctx, tx, r)
			if err != nil {
				return httpx.Page[FoodCostRow]{}, err
			}
			outlet, err := handle.QueryUUID(r, "outletId")
			if err != nil {
				return httpx.Page[FoodCostRow]{}, err
			}
			key, byOutlet := "s.product_id", r.URL.Query().Get("groupBy") == "outlet"
			if byOutlet {
				key = "s.outlet_id"
			}
			rows, err := handle.List[FoodCostRow](tx.Query(ctx, `SELECT `+key+` AS key, '' AS name, trim_scale(sum(s.quantity))::text AS quantity,
				trim_scale(sum(s.net_amount))::text AS net_sales, trim_scale(round(sum(s.cost_amount), 2))::text AS theoretical_cost,
				CASE WHEN sum(s.net_amount) > 0 THEN round(sum(s.cost_amount) / sum(s.net_amount) * 100, 2)::text END AS food_cost_percent,
				count(*) FILTER (WHERE NOT s.has_recipe)::int AS without_recipe
				FROM inventory.consumption_sales s WHERE s.property_id = $1 AND s.occurred_at >= $2 AND s.occurred_at < $3
				AND ($4::uuid IS NULL OR s.outlet_id = $4) AND `+key+` IS NOT NULL GROUP BY 1, 2 ORDER BY sum(s.net_amount) DESC`, handle.Property(ctx), from, to, outlet))
			if err != nil || m.Products == nil {
				return handle.Page(rows, err)
			}
			ids := make([]uuid.UUID, 0, len(rows))
			for _, x := range rows {
				ids = append(ids, x.Key)
			}
			names, err := m.Products.ProductNames(ctx, tx, ids)
			if byOutlet {
				names, err = m.Products.OutletNames(ctx, tx, ids)
			}
			for i := range rows {
				rows[i].Name = names[rows[i].Key]
			}
			return handle.Page(rows, err)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/inventory/consumption", Summary: "Theoretical consumption per item (ready for P4 stock)",
		Permission: "inventory.food_cost.view", Response: Consumption{}, List: true, Query: []route.Param{{Name: "from"}, {Name: "to"}, {Name: "outletId"}},
		Handler: handle.Read(m.DB, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Consumption], error) {
			from, to, err := period(ctx, tx, r)
			if err != nil {
				return httpx.Page[Consumption]{}, err
			}
			outlet, err := handle.QueryUUID(r, "outletId")
			if err != nil {
				return httpx.Page[Consumption]{}, err
			}
			return handle.Page(handle.List[Consumption](tx.Query(ctx, `SELECT c.item_id, i.name, u.code AS uom, trim_scale(round(sum(c.quantity), 4))::text AS quantity,
				trim_scale(round(sum(c.cost_amount), 2))::text AS cost FROM inventory.consumption_ledger c JOIN inventory.items i ON i.id = c.item_id
				JOIN inventory.uoms u ON u.id = i.base_uom_id WHERE c.property_id = $1 AND c.occurred_at >= $2 AND c.occurred_at < $3
				AND ($4::uuid IS NULL OR c.outlet_id = $4) GROUP BY c.item_id, i.name, u.code ORDER BY sum(c.cost_amount) DESC`, handle.Property(ctx), from, to, outlet)))
		})})
}

// Contribution returns catalogue entries.
func Contribution() catalog.Contribution {
	defs := []*resource.Def{UOMs, Items, Recipes}
	perms := append(resource.Permissions(defs...), catalog.P("inventory", "food_cost", "view")...)
	all := append(resource.AllActions(defs...), "inventory.food_cost.view")
	view := []string{"inventory.uom.view", "inventory.item.view", "inventory.recipe.view", "inventory.food_cost.view"}
	return catalog.Contribution{
		Permissions: perms,
		RolePermissions: map[string][]string{
			"property_admin":    all,
			"inventory_manager": all,
			"outlet_manager":    all,
			"finance_manager":   view,
			"accountant":        view,
			"general_manager":   view,
			"kitchen_staff":     {"inventory.recipe.view", "inventory.item.view", "inventory.uom.view"},
		},
	}
}
