package commercial

// PRD P5 FR-PKG-P5-04 Package Profitability: allocated revenue (P3 revenue
// allocation, net of tax & service) against the cost of every package
// booking — the BOM cost of F&B components at the P4 inventory cost
// (estimated at booking, the actual consumption lines once used), caddy
// fee, room cost, commission and other direct costs of the package cost
// rules — per package and period. Cost lines are refreshed from the
// booking, consumption and cancellation events (idempotent per booking);
// the API and the Package Profitability Report read the same reporting
// view (reporting.commercial_package_profit_lines).

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/inventory"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/outbox"
)

type costComponent struct {
	ID               uuid.UUID  `db:"id"`
	ComponentID      uuid.UUID  `db:"component_id"`
	Name             string     `db:"name"`
	Status           string     `db:"status"`
	Quantity         string     `db:"quantity"`
	ConsumedQuantity string     `db:"consumed_quantity"`
	RecipeID         *uuid.UUID `db:"recipe_id"`
	ProductID        *uuid.UUID `db:"product_id"`
	AllocatedNet     string     `db:"allocated_net"`
	Liability        bool       `db:"liability"`
}

type costRule struct {
	ID          uuid.UUID  `db:"id"`
	ComponentID *uuid.UUID `db:"component_id"`
	CostType    string     `db:"cost_type"`
	Basis       string     `db:"basis"`
	Amount      string     `db:"amount"`
}

type costLine struct {
	component *uuid.UUID
	costType  string
	basis     string
	amount    decimal.Decimal
	rule      *uuid.UUID
	detail    map[string]any
}

// ruleCost is the cost of one rule for a booking (pure, unit tested).
// qty, revenue: of the component (or of the booking for package rules).
func ruleCost(basis string, amount, qty, revenue decimal.Decimal, pax int) decimal.Decimal {
	switch basis {
	case "per_booking":
		return amount
	case "per_pax":
		return amount.Mul(decimal.NewFromInt(int64(pax)))
	case "percent_of_revenue":
		return revenue.Mul(amount).Div(hundred)
	}
	return amount.Mul(qty)
}

// RefreshBookingCosts recomputes the cost lines of a package booking.
func RefreshBookingCosts(ctx context.Context, tx pgx.Tx, bid uuid.UUID) error {
	var property, pkg uuid.UUID
	var status, cur string
	var start time.Time
	var pax int
	err := tx.QueryRow(ctx, `SELECT property_id, package_id, status, start_date, pax, currency FROM commercial.package_bookings WHERE id = $1 FOR UPDATE`, bid).
		Scan(&property, &pkg, &status, &start, &pax, &cur)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM commercial.package_cost_lines WHERE booking_id = $1`, bid); err != nil {
		return err
	}
	if status == "pending" || status == "expired" {
		return nil
	}
	pol, err := LoadPackagePolicy(ctx, tx, property)
	if err != nil {
		return err
	}
	comps, err := handle.List[costComponent](tx.Query(ctx, `SELECT id, component_id, name, status, trim_scale(quantity)::text AS quantity,
		trim_scale(consumed_quantity)::text AS consumed_quantity, recipe_id, product_id, trim_scale(allocated_net)::text AS allocated_net, liability
		FROM commercial.package_booking_components WHERE booking_id = $1 ORDER BY seq`, bid))
	if err != nil {
		return err
	}
	pl := places(cur)
	costs := map[uuid.UUID]decimal.Decimal{}
	unitCost := func(item uuid.UUID) (decimal.Decimal, error) {
		if c, ok := costs[item]; ok {
			return c, nil
		}
		c, err := itemCost(ctx, tx, item, pol.CostBasis)
		costs[item] = c
		return c, err
	}
	var lines []costLine
	revenue := decimal.Zero
	active := map[uuid.UUID]costComponent{}
	for _, c := range comps {
		cid := c.ID
		consumed := dec(c.ConsumedQuantity)
		if c.Status == "cancelled" && !consumed.IsPositive() {
			continue
		}
		active[c.ComponentID] = c
		if !c.Liability {
			revenue = revenue.Add(dec(c.AllocatedNet))
		}
		// actual BOM cost of what was consumed (the consumption lines of K6)
		actual := decimal.Zero
		rows, err := tx.Query(ctx, `SELECT consumption FROM commercial.package_consumptions WHERE booking_component_id = $1`, c.ID)
		if err != nil {
			return err
		}
		var raws [][]byte
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			raws = append(raws, raw)
		}
		rows.Close()
		for _, raw := range raws {
			var cl []ConsumptionLine
			if err := json.Unmarshal(raw, &cl); err != nil {
				continue
			}
			for _, l := range cl {
				uc, err := unitCost(l.ItemID)
				if err != nil {
					return err
				}
				actual = actual.Add(uc.Mul(dec(l.Quantity)))
			}
		}
		if actual.IsPositive() {
			lines = append(lines, costLine{component: &cid, costType: "cogs", basis: "actual", amount: actual.Round(pl),
				detail: map[string]any{"consumed": consumed.String()}})
		}
		// estimate of what is still to be served
		rest := dec(c.Quantity).Sub(consumed)
		if c.Status == "unused" && rest.IsPositive() {
			rid, err := componentRecipe(ctx, tx, c.RecipeID, c.ProductID)
			if err != nil {
				return err
			}
			if rid != nil {
				reqs, err := inventory.ExplodeRecipe(ctx, tx, *rid, rest)
				if err != nil {
					if de, ok := errs.As(err); !ok || de.Kind == errs.KindInternal {
						return err
					}
					reqs = nil // incomplete recipe: no estimate
				}
				est := decimal.Zero
				for _, r := range reqs {
					uc, err := unitCost(r.ItemID)
					if err != nil {
						return err
					}
					est = est.Add(uc.Mul(r.Quantity))
				}
				if est.IsPositive() {
					lines = append(lines, costLine{component: &cid, costType: "cogs", basis: "estimate", amount: est.Round(pl),
						detail: map[string]any{"quantity": rest.String()}})
				}
			}
		}
	}
	// direct costs of the cost rules in force on the start date
	rules, err := handle.List[costRule](tx.Query(ctx, `SELECT id, component_id, cost_type, basis, trim_scale(amount)::text AS amount
		FROM commercial.package_cost_rules WHERE package_id = $1 AND status = 'active' AND (effective_from IS NULL OR effective_from <= $2::date)
		AND (effective_to IS NULL OR effective_to >= $2::date) ORDER BY created_at`, pkg, start.Format("2006-01-02")))
	if err != nil {
		return err
	}
	for _, r := range rules {
		rid := r.ID
		if r.ComponentID != nil {
			c, ok := active[*r.ComponentID]
			if !ok {
				continue
			}
			qty := dec(c.Quantity)
			if c.Status == "cancelled" || c.Status == "expired" {
				qty = dec(c.ConsumedQuantity)
			}
			cid := c.ID
			amt := ruleCost(r.Basis, dec(r.Amount), qty, dec(c.AllocatedNet), pax).Round(pl)
			if amt.IsPositive() {
				lines = append(lines, costLine{component: &cid, costType: r.CostType, basis: "rule", amount: amt, rule: &rid,
					detail: map[string]any{"basis": r.Basis, "amount": r.Amount, "quantity": qty.String()}})
			}
			continue
		}
		if status == "cancelled" {
			continue // booking-level costs only for bookings that took place
		}
		amt := ruleCost(r.Basis, dec(r.Amount), decimal.NewFromInt(1), revenue, pax).Round(pl)
		if amt.IsPositive() {
			lines = append(lines, costLine{costType: r.CostType, basis: "rule", amount: amt, rule: &rid, detail: map[string]any{"basis": r.Basis, "amount": r.Amount}})
		}
	}
	for _, l := range lines {
		raw, _ := json.Marshal(l.detail)
		if _, err := tx.Exec(ctx, `INSERT INTO commercial.package_cost_lines (id, property_id, booking_id, booking_component_id, cost_type, basis, amount, rule_id, detail)
			VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9)`, id.New(), property, bid, l.component, l.costType, l.basis, l.amount.String(), l.rule, raw); err != nil {
			return err
		}
	}
	return nil
}

// OnPackageCostEvent refreshes the costs of the booking of a package event
// (commercial.package_booked / package_consumed / package_cancelled).
func OnPackageCostEvent(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		BookingID uuid.UUID `json:"bookingId"`
	}
	if err := e.Decode(&p); err != nil || p.BookingID == uuid.Nil || e.PropertyID == nil {
		return nil //nolint:nilerr // foreign payload
	}
	return RefreshBookingCosts(reqctx.WithProperty(dbtx.System(ctx), *e.PropertyID), tx, p.BookingID)
}

// ── Package Profitability (GET /commercial/package-profitability) ─────────

// PackageProfitabilityRow is the profitability of a package (per period
// or component).
type PackageProfitabilityRow struct {
	PackageID     uuid.UUID `json:"packageId" db:"package_id"`
	PackageCode   string    `json:"packageCode" db:"package_code"`
	PackageName   string    `json:"packageName" db:"package_name"`
	PackageType   string    `json:"packageType" db:"package_type"`
	Period        *string   `json:"period" db:"period" doc:"YYYY-MM when grouped by month"`
	Component     *string   `json:"component" db:"component" doc:"When grouped by component"`
	Bookings      int       `json:"bookings" db:"bookings"`
	Pax           int       `json:"pax" db:"pax"`
	Revenue       string    `json:"revenue" db:"revenue" doc:"Allocated net revenue (liabilities apart)"`
	PassThrough   string    `json:"passThrough" db:"pass_through" doc:"Held for third parties (liability), not club revenue"`
	COGS          string    `json:"cogs" db:"cogs" doc:"BOM cost (P4): actual when consumed, estimated otherwise"`
	CaddyFee      string    `json:"caddyFee" db:"caddy_fee"`
	RoomCost      string    `json:"roomCost" db:"room_cost"`
	Commission    string    `json:"commission" db:"commission"`
	OtherCost     string    `json:"otherCost" db:"other_cost" doc:"Labour and other direct costs"`
	TotalCost     string    `json:"totalCost" db:"total_cost"`
	Margin        string    `json:"margin" db:"margin"`
	MarginPercent *string   `json:"marginPercent" db:"margin_percent"`
}

// PackageProfitability is the response of the profitability endpoint.
type PackageProfitability struct {
	From    string                    `json:"from"`
	To      string                    `json:"to"`
	GroupBy string                    `json:"groupBy" enum:"package,month,component"`
	Rows    []PackageProfitabilityRow `json:"rows"`
	Total   PackageProfitabilityRow   `json:"total"`
}

const profitAgg = `count(DISTINCT booking_id)::int AS bookings,
	coalesce(sum(pax) FILTER (WHERE first_line), 0)::int AS pax,
	trim_scale(coalesce(sum(amount) FILTER (WHERE line_kind = 'revenue'), 0))::text AS revenue,
	trim_scale(coalesce(sum(amount) FILTER (WHERE line_kind = 'pass_through'), 0))::text AS pass_through,
	trim_scale(coalesce(sum(amount) FILTER (WHERE cost_type = 'cogs'), 0))::text AS cogs,
	trim_scale(coalesce(sum(amount) FILTER (WHERE cost_type = 'caddy_fee'), 0))::text AS caddy_fee,
	trim_scale(coalesce(sum(amount) FILTER (WHERE cost_type = 'room_cost'), 0))::text AS room_cost,
	trim_scale(coalesce(sum(amount) FILTER (WHERE cost_type = 'commission'), 0))::text AS commission,
	trim_scale(coalesce(sum(amount) FILTER (WHERE cost_type IN ('labour', 'other')), 0))::text AS other_cost,
	trim_scale(coalesce(sum(amount) FILTER (WHERE line_kind = 'cost'), 0))::text AS total_cost,
	trim_scale(coalesce(sum(amount) FILTER (WHERE line_kind = 'revenue'), 0) - coalesce(sum(amount) FILTER (WHERE line_kind = 'cost'), 0))::text AS margin,
	CASE WHEN coalesce(sum(amount) FILTER (WHERE line_kind = 'revenue'), 0) > 0 THEN trim_scale(round((coalesce(sum(amount) FILTER (WHERE line_kind = 'revenue'), 0)
	  - coalesce(sum(amount) FILTER (WHERE line_kind = 'cost'), 0)) * 100 / sum(amount) FILTER (WHERE line_kind = 'revenue'), 2))::text END AS margin_percent`

// Profitability reads the profit lines of the bookings starting in [from, to].
func Profitability(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time, pkg *uuid.UUID, groupBy string) (PackageProfitability, error) {
	out := PackageProfitability{From: from.Format("2006-01-02"), To: to.Format("2006-01-02"), GroupBy: groupBy, Rows: []PackageProfitabilityRow{}}
	if to.Before(from) {
		return out, handle.Invalid("to", "invalid_period", "on or after from")
	}
	period, component := "NULL::text", "NULL::text"
	switch groupBy {
	case "", "package":
		out.GroupBy = "package"
	case "month":
		period = "to_char(start_date, 'YYYY-MM')"
	case "component":
		component = "coalesce(component_name, '(package)')"
	default:
		return out, handle.Invalid("groupBy", "invalid", "package, month or component")
	}
	base := `FROM reporting.commercial_package_profit_lines WHERE property_id = $1 AND start_date BETWEEN $2::date AND $3::date
		AND ($4::uuid IS NULL OR package_id = $4)`
	rows, err := handle.List[PackageProfitabilityRow](q.Query(ctx, `SELECT package_id, package_code, min(package_name) AS package_name,
		min(package_type) AS package_type, `+period+` AS period, `+component+` AS component, `+profitAgg+` `+base+`
		GROUP BY package_id, package_code, 5, 6 ORDER BY package_code, 5 NULLS FIRST, 6 NULLS FIRST`,
		property, out.From, out.To, pkg))
	if err != nil {
		return out, err
	}
	out.Rows = rows
	tot, err := handle.List[PackageProfitabilityRow](q.Query(ctx, `SELECT '00000000-0000-0000-0000-000000000000'::uuid AS package_id, 'TOTAL' AS package_code,
		'Total' AS package_name, '' AS package_type, NULL::text AS period, NULL::text AS component, `+profitAgg+` `+base, property, out.From, out.To, pkg))
	if err != nil {
		return out, err
	}
	if len(tot) == 1 {
		out.Total = tot[0]
	}
	return out, nil
}

// ProfitabilityRecalcInput recalculates the costs of a period.
type ProfitabilityRecalcInput struct {
	From      string     `json:"from" doc:"Start dates from (YYYY-MM-DD)"`
	To        string     `json:"to" doc:"Start dates until (YYYY-MM-DD)"`
	PackageID *uuid.UUID `json:"packageId,omitempty"`
}

// ProfitabilityRecalcResult reports a recalculation.
type ProfitabilityRecalcResult struct {
	Bookings int `json:"bookings"`
}

// RecalculateProfitability refreshes the cost lines of the bookings of a
// period (after cost rules or item costs changed).
func RecalculateProfitability(ctx context.Context, tx pgx.Tx, property uuid.UUID, in ProfitabilityRecalcInput) (ProfitabilityRecalcResult, error) {
	from, err := time.Parse("2006-01-02", in.From)
	if err != nil {
		return ProfitabilityRecalcResult{}, handle.Invalid("from", "invalid", "YYYY-MM-DD")
	}
	to, err := time.Parse("2006-01-02", in.To)
	if err != nil || to.Before(from) {
		return ProfitabilityRecalcResult{}, handle.Invalid("to", "invalid", "YYYY-MM-DD on or after from")
	}
	if to.Sub(from) > 400*24*time.Hour {
		return ProfitabilityRecalcResult{}, handle.Invalid("to", "too_long", "at most 400 days")
	}
	rows, err := tx.Query(ctx, `SELECT id FROM commercial.package_bookings WHERE property_id = $1 AND start_date BETWEEN $2::date AND $3::date
		AND ($4::uuid IS NULL OR package_id = $4) AND status IN ('confirmed', 'completed', 'cancelled') ORDER BY start_date`, property, in.From, in.To, in.PackageID)
	if err != nil {
		return ProfitabilityRecalcResult{}, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return ProfitabilityRecalcResult{}, err
	}
	for _, b := range ids {
		if err := RefreshBookingCosts(ctx, tx, b); err != nil {
			return ProfitabilityRecalcResult{}, err
		}
	}
	out := ProfitabilityRecalcResult{Bookings: len(ids)}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "recalculate", EntityType: "commercial.package_profitability",
		EntityID: property.String(), EntityLabel: "Package Profitability " + in.From + " – " + in.To, PropertyID: &property,
		After: map[string]any{"from": in.From, "to": in.To, "packageId": in.PackageID, "bookings": len(ids)}})
}
