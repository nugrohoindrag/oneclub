package commercial

// PRD P5 EP-22 routes (PRD P5 §11): Commercial → Packages → Capacity
// (/commercial/packages/{id}/capacity, the inventory requirement of large
// quotas) and Profitability (/commercial/package-profitability), with the
// P5 master data (component rules, capacity, cost rules, payment templates)
// through the resource engine.

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

// PackageComponentRuleView is a component rule with its component.
type PackageComponentRuleView struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	ComponentID       uuid.UUID  `json:"componentId" db:"component_id"`
	ComponentName     string     `json:"componentName" db:"component_name"`
	ComponentType     string     `json:"componentType" db:"component_type"`
	Seq               int        `json:"seq" db:"seq"`
	ChoiceGroup       *string    `json:"choiceGroup" db:"choice_group"`
	ChoicePick        int        `json:"choicePick" db:"choice_pick"`
	AfterComponentID  *uuid.UUID `json:"afterComponentId" db:"after_component_id"`
	AfterComponent    *string    `json:"afterComponent" db:"after_component"`
	MinGapMinutes     int        `json:"minGapMinutes" db:"min_gap_minutes"`
	MaxGapMinutes     *int       `json:"maxGapMinutes" db:"max_gap_minutes"`
	WindowStart       *string    `json:"windowStart" db:"window_start"`
	WindowEnd         *string    `json:"windowEnd" db:"window_end"`
	Status            string     `json:"status" db:"status" enum:"active,inactive"`
	ComponentOptional bool       `json:"componentOptional" db:"optional"`
}

// RegisterP5 adds the PRD P5 package resources and routes.
func (m *P3Module) RegisterP5(reg *route.Registry, eng *resource.Engine) {
	for _, d := range []*resource.Def{PackageComponentRules, PackageCapacities, PackageCostRules, PackagePaymentTemplates} {
		eng.Register(reg, d)
	}
	a := routeAdder{reg: reg}
	db := m.DB
	add := func(rt routeSpec) { a.add(rt, "Packages") }
	add(routeSpec{method: http.MethodGet, path: "/api/v1/commercial/packages/{id}/capacity",
		summary: "Package Capacity: allotments, blackouts, daily quota and component time blocks per date", perm: "commercial.package_capacity.view",
		res: PackageCapacityCalendar{}, query: []queryParam{{name: "date", doc: "First date (default today)"}, {name: "days", typ: "integer", doc: "1–62"}},
		h: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (PackageCapacityCalendar, error) {
			pid, err := handle.ID(r)
			if err != nil {
				return PackageCapacityCalendar{}, err
			}
			d, err := handle.QueryDate(r, "date", clock.Now().In(calendar.Location(ctx, tx)))
			if err != nil {
				return PackageCapacityCalendar{}, err
			}
			return CapacityCalendar(ctx, tx, handle.Property(ctx), pid, d, min(max(handle.QueryInt(r, "days", 14), 1), 62))
		})})
	add(routeSpec{method: http.MethodGet, path: "/api/v1/commercial/packages/{id}/inventory-requirement",
		summary: "Inventory requirement (BOM, P4) of a package quota before it is sold", perm: "commercial.package.view", res: PackageInventoryRequirement{},
		query: []queryParam{{name: "date", doc: "Start date: the allotment of the date is the default number of bookings"}, {name: "pax", typ: "integer"},
			{name: "nights", typ: "integer"}, {name: "bookings", typ: "integer"}},
		h: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (PackageInventoryRequirement, error) {
			pid, err := handle.ID(r)
			if err != nil {
				return PackageInventoryRequirement{}, err
			}
			var date *time.Time
			if r.URL.Query().Get("date") != "" {
				d, err := handle.QueryDate(r, "date", time.Time{})
				if err != nil {
					return PackageInventoryRequirement{}, err
				}
				date = &d
			}
			return InventoryRequirement(ctx, tx, handle.Property(ctx), pid, date, handle.QueryInt(r, "pax", 0), handle.QueryInt(r, "nights", 0),
				min(handle.QueryInt(r, "bookings", 0), 10000))
		})})
	add(routeSpec{method: http.MethodGet, path: "/api/v1/commercial/packages/{id}/component-rules",
		summary: "Component rules of a package: choice groups, sequence & gap, service window", perm: "commercial.package_component_rule.view",
		res: PackageComponentRuleView{}, list: true,
		h: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[PackageComponentRuleView], error) {
			pid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[PackageComponentRuleView]{}, err
			}
			return handle.Page(handle.List[PackageComponentRuleView](tx.Query(ctx, `SELECT r.id, r.component_id, c.name AS component_name,
				c.component_type, c.seq, r.choice_group, r.choice_pick, r.after_component_id, a.name AS after_component, r.min_gap_minutes,
				r.max_gap_minutes, to_char(r.window_start, 'HH24:MI') AS window_start, to_char(r.window_end, 'HH24:MI') AS window_end, r.status, c.optional
				FROM commercial.package_component_rules r JOIN commercial.package_components c ON c.id = r.component_id
				LEFT JOIN commercial.package_components a ON a.id = r.after_component_id WHERE c.package_id = $1 ORDER BY c.seq, r.created_at`, pid)))
		})})
	add(routeSpec{method: http.MethodGet, path: "/api/v1/commercial/package-profitability",
		summary: "Package Profitability: allocated revenue vs COGS (BOM), caddy fee, room cost, commission per package and period",
		perm:    "commercial.package_profitability.view", res: PackageProfitability{},
		query: []queryParam{{name: "from", doc: "Start dates from (default: first day of the month)"}, {name: "to", doc: "Start dates until (default: today)"},
			{name: "packageId"}, {name: "groupBy", enum: []string{"package", "month", "component"}}},
		h: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (PackageProfitability, error) {
			now := clock.Now().In(calendar.Location(ctx, tx))
			from, err := handle.QueryDate(r, "from", time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC))
			if err != nil {
				return PackageProfitability{}, err
			}
			to, err := handle.QueryDate(r, "to", now)
			if err != nil {
				return PackageProfitability{}, err
			}
			pkg, err := handle.QueryUUID(r, "packageId")
			if err != nil {
				return PackageProfitability{}, err
			}
			return Profitability(ctx, tx, handle.Property(ctx), from, to, pkg, r.URL.Query().Get("groupBy"))
		})})
	add(routeSpec{method: http.MethodPost, path: "/api/v1/commercial/package-profitability:recalculate",
		summary: "Recalculate package costs of a period (after cost rules or item costs changed)", perm: "commercial.package_profitability.recalculate",
		req: ProfitabilityRecalcInput{}, res: ProfitabilityRecalcResult{}, status: http.StatusOK,
		h: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, _ *http.Request, in ProfitabilityRecalcInput) (ProfitabilityRecalcResult, error) {
			return RecalculateProfitability(ctx, tx, handle.Property(ctx), in)
		})})
}
