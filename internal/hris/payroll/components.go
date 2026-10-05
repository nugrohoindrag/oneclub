package payroll

// The component catalogue the engine works with: the pay components of the
// property (master data), completed by the components of the Payroll
// Configuration in force and the engine's own codes, so a run works before
// the club has loaded its components.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
)

// component is a resolved pay component.
type component struct {
	Code, Name, Kind, Category, Method string
	Taxable, Irregular, Fixed, PreTax  bool
	Prorate                            bool
	Default                            string
	Sort                               int
}

func fromDef(d componentDef) component {
	method := d.Method
	if method == "" {
		method = "fixed"
	}
	return component{Code: d.Code, Name: d.Name, Kind: d.Kind, Category: d.Category, Method: method, Taxable: d.Taxable, Irregular: d.Irregular,
		Fixed: d.Fixed, PreTax: d.PreTax, Prorate: d.Prorate, Default: d.Default, Sort: d.Sort}
}

// configComponent maps a Payroll Configuration component (the builtin of
// the same code gives category and method).
func configComponent(p hris.PayComponent) component {
	for _, b := range builtinComponents {
		if b.Code == p.Code {
			c := fromDef(b)
			c.Name, c.Kind, c.Fixed, c.Taxable = p.Name, p.Kind, p.Fixed && p.Kind == "earning", p.Taxable
			if c.Fixed {
				c.Prorate = true
			}
			return c
		}
	}
	cat := hris.CatAllowance
	if p.Kind == "deduction" {
		cat = hris.CatDeduction
	}
	return component{Code: p.Code, Name: p.Name, Kind: p.Kind, Category: cat, Method: "fixed", Taxable: p.Taxable, Fixed: p.Fixed && p.Kind == "earning",
		Prorate: p.Fixed && p.Kind == "earning", Sort: 100}
}

// loadComponents returns the component catalogue of a property on a date.
func loadComponents(ctx context.Context, q dbtx.Querier, property uuid.UUID, at time.Time) (map[string]component, error) {
	out := map[string]component{}
	for _, b := range builtinComponents {
		out[b.Code] = fromDef(b)
	}
	cfg, _, err := hris.LoadPayrollConfiguration(ctx, q, property, hris.PolicyTime(at))
	if err != nil {
		return nil, err
	}
	for _, p := range cfg.Components {
		out[p.Code] = configComponent(p)
	}
	rows, err := q.Query(ctx, `SELECT code, name, kind, category, taxable, irregular, fixed, pre_tax, prorate, calc_method,
		coalesce(trim_scale(default_amount)::text, ''), sort_order FROM hris.pay_components
		WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL`, property)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c component
		if err := rows.Scan(&c.Code, &c.Name, &c.Kind, &c.Category, &c.Taxable, &c.Irregular, &c.Fixed, &c.PreTax, &c.Prorate, &c.Method, &c.Default,
			&c.Sort); err != nil {
			return nil, err
		}
		out[c.Code] = c
	}
	return out, rows.Err()
}

// ensureComponents creates the default components missing at a property;
// returns the number created and the number of components.
func ensureComponents(ctx context.Context, tx pgx.Tx, property uuid.UUID) (int, int, error) {
	cfg, _, err := hris.LoadPayrollConfiguration(ctx, tx, property, clock.Now())
	if err != nil {
		return 0, 0, err
	}
	defs := map[string]component{}
	var order []string
	for _, b := range builtinComponents {
		defs[b.Code] = fromDef(b)
		order = append(order, b.Code)
	}
	for _, p := range cfg.Components {
		if _, ok := defs[p.Code]; !ok {
			order = append(order, p.Code)
		}
		defs[p.Code] = configComponent(p)
	}
	names := map[string]string{}
	for _, b := range builtinComponents {
		names[b.Code] = b.NameID
	}
	n := 0
	for _, code := range order {
		c := defs[code]
		tag, err := tx.Exec(ctx, `INSERT INTO hris.pay_components (id, property_id, code, name, name_id, kind, category, taxable, irregular, fixed, pre_tax,
			prorate, calc_method, sort_order) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT (property_id, code) DO NOTHING`,
			id.New(), property, c.Code, c.Name, nullStr(names[c.Code]), c.Kind, c.Category, c.Taxable, c.Irregular, c.Fixed, c.PreTax, c.Prorate, c.Method,
			c.Sort)
		if err != nil {
			return n, 0, err
		}
		n += int(tag.RowsAffected())
	}
	var total int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM hris.pay_components WHERE property_id = $1 AND archived_at IS NULL`, property).Scan(&total)
	return n, total, err
}

// item turns a component and an amount into an engine item.
func (c component) item(amount string, source string) hris.PayItem {
	return hris.PayItem{Code: c.Code, Name: c.Name, Kind: c.Kind, Category: c.Category, Taxable: c.Taxable, Irregular: c.Irregular, PreTax: c.PreTax,
		Fixed: c.Fixed, Prorate: c.Prorate, Amount: dec(amount), Source: source}
}

// componentOr returns the component of a code, or an ad-hoc taxable
// earning / after-tax deduction named after it.
func componentOr(cat map[string]component, code, name, kind string) component {
	if c, ok := cat[code]; ok {
		return c
	}
	if kind == "" {
		kind = "earning"
	}
	if name == "" {
		name = code
	}
	c := component{Code: code, Name: name, Kind: kind, Category: hris.CatOther, Method: "fixed", Taxable: kind == "earning", Sort: 100}
	if kind == "deduction" {
		c.Category = hris.CatDeduction
	}
	return c
}
