package accounting

// EP-21 Revenue & Tax and the control reconciliations: revenue allocation
// rules for bundled charges without a component snapshot (FR-REV-01, total
// allocated = price), the deferred revenue report (voucher, prepaid, annual
// fee, package, deposits, caddy fee liability; GL = sub-ledger, FR-REV-02/05,
// G6), the service charge pool per month with the department basis
// (FR-REV-06), the posting reconciliation of a business day against the
// Daily Revenue Report and the Accounting Export (FR-PST-07) and the control
// account reconciliation (FR-AR-06, NFR financial integrity).

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// AllocationShare is one component of a revenue allocation rule.
type AllocationShare struct {
	Component string `json:"component"`
	Percent   string `json:"percent"`
	Liability bool   `json:"liability,omitempty"`
}

type allocRule struct {
	BusinessLine  *string    `db:"business_line"`
	MatchComp     string     `db:"match_component"`
	Components    []byte     `db:"components"`
	EffectiveFrom time.Time  `db:"effective_from"`
	EffectiveTo   *time.Time `db:"effective_to"`
	shares        []AllocationShare
}

// allocator splits the net of bundled charges without components.
type allocator struct{ rules []allocRule }

func loadAllocator(ctx context.Context, q dbtx.Querier) (*allocator, error) {
	rules, err := handle.List[allocRule](q.Query(ctx, `SELECT business_line, match_component, components, effective_from, effective_to
		FROM accounting.revenue_allocation_rules WHERE status = 'active' AND archived_at IS NULL ORDER BY business_line NULLS LAST, effective_from DESC`))
	if err != nil {
		return nil, err
	}
	for i := range rules {
		_ = json.Unmarshal(rules[i].Components, &rules[i].shares)
	}
	return &allocator{rules: rules}, nil
}

// split returns the components of a line net per the first matching rule.
func (a *allocator) split(line, component string, day *time.Time, net decimal.Decimal, cur string) []comp {
	if a == nil || net.IsZero() {
		return nil
	}
	for _, r := range a.rules {
		if !strings.EqualFold(r.MatchComp, component) || (r.BusinessLine != nil && *r.BusinessLine != "" && *r.BusinessLine != line) {
			continue
		}
		if day != nil && (day.Before(r.EffectiveFrom) || (r.EffectiveTo != nil && day.After(*r.EffectiveTo))) {
			continue
		}
		if len(r.shares) == 0 {
			continue
		}
		var out []comp
		acc := decimal.Zero
		for i, s := range r.shares {
			amt := net.Mul(dec(s.Percent)).Div(decimal.NewFromInt(100)).Round(places(cur))
			if i == len(r.shares)-1 {
				amt = net.Sub(acc) // total allocated = price
			}
			acc = acc.Add(amt)
			out = append(out, comp{Code: s.Component, Amount: amt.String(), Liability: s.Liability})
		}
		return out
	}
	return nil
}

// validateAllocation checks the shares of a rule (sum 100 %).
func validateAllocation(raw any) error {
	b := rawJSON(raw)
	var shares []AllocationShare
	if err := json.Unmarshal(b, &shares); err != nil || len(shares) == 0 {
		return handle.Invalid("components", "invalid", "components: [{component, percent}]")
	}
	sum := decimal.Zero
	for _, s := range shares {
		if strings.TrimSpace(s.Component) == "" || !dec(s.Percent).IsPositive() {
			return handle.Invalid("components", "invalid", "every share needs a component and a positive percent")
		}
		sum = sum.Add(dec(s.Percent))
	}
	if !sum.Equal(decimal.NewFromInt(100)) {
		return handle.Invalid("components", "not_100", "the shares must add up to 100% (now "+sum.String()+"%)")
	}
	return nil
}

// ── deferred revenue & liabilities (FR-REV-02/05) ─────────────────────────

// DeferredLiabilityRow is one liability: GL balance against its sub-ledger and the
// movements of the period.
type DeferredLiabilityRow struct {
	Type        string `json:"type" enum:"voucher,prepaid,annual_fee,package,deposits,caddy_fee,loyalty"`
	Label       string `json:"label"`
	AccountCode string `json:"accountCode"`
	GLBalance   string `json:"glBalance" doc:"Credit balance of the liability account at the date"`
	Subledger   string `json:"subledger" doc:"Operational sub-ledger at the date"`
	Difference  string `json:"difference"`
	Deferred    string `json:"deferred" doc:"Deferrals in the period"`
	Recognised  string `json:"recognised" doc:"Revenue recognised in the period"`
	Breakage    string `json:"breakage" doc:"Breakage in the period"`
}

// DeferredRevenueReport is the deferred revenue / liability report.
type DeferredRevenueReport struct {
	From string                 `json:"from"`
	To   string                 `json:"to"`
	Rows []DeferredLiabilityRow `json:"rows"`
}

// DeferredRevenue reports the liabilities of a property at a date.
func DeferredRevenue(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time) (DeferredRevenueReport, error) {
	out := DeferredRevenueReport{From: ymd(from), To: ymd(to), Rows: []DeferredLiabilityRow{}}
	cfg, err := LoadConfiguration(ctx, q, property)
	if err != nil {
		return out, err
	}
	end := dateOnly(to).AddDate(0, 0, 1)
	gl := func(role string) (decimal.Decimal, error) {
		b, err := roleBalance(ctx, q, property, cfg, role, &to)
		return b.Neg(), err
	}
	types := []string{"voucher", "prepaid", "annual_fee", "package"}
	labels := map[string]string{"voucher": "Vouchers", "prepaid": "Prepaid balances", "annual_fee": "Annual membership fees", "package": "Packages"}
	for _, lt := range types {
		g, err := gl(liabilityRoles[lt])
		if err != nil {
			return out, err
		}
		var sub, deferred, recog, brk *string
		if err := q.QueryRow(ctx, `SELECT sum(amount) FILTER (WHERE occurred_at < $3)::text,
			sum(amount) FILTER (WHERE entry_type IN ('deferral', 'adjustment') AND occurred_at >= $4 AND occurred_at < $3)::text,
			sum(-amount) FILTER (WHERE entry_type = 'recognition' AND occurred_at >= $4 AND occurred_at < $3)::text,
			sum(-amount) FILTER (WHERE entry_type = 'breakage' AND occurred_at >= $4 AND occurred_at < $3)::text
			FROM reporting.acc_deferred_entries WHERE property_id = $1 AND liability_type = $2`, property, lt, end, dateOnly(from)).
			Scan(&sub, &deferred, &recog, &brk); err != nil {
			return out, err
		}
		s := decp(sub)
		out.Rows = append(out.Rows, DeferredLiabilityRow{Type: lt, Label: labels[lt], AccountCode: cfg.Accounts[liabilityRoles[lt]], GLBalance: g.String(),
			Subledger: s.String(), Difference: g.Sub(s).String(), Deferred: decp(deferred).String(), Recognised: decp(recog).String(), Breakage: decp(brk).String()})
	}
	g, err := gl("customer_deposits")
	if err != nil {
		return out, err
	}
	var held *string
	if err := q.QueryRow(ctx, `SELECT sum(amount - applied_amount)::text FROM reporting.acc_deposits WHERE property_id = $1 AND status = 'held' AND created_at < $2`,
		property, end).Scan(&held); err != nil {
		return out, err
	}
	out.Rows = append(out.Rows, DeferredLiabilityRow{Type: "deposits", Label: "Customer deposits / DP", AccountCode: cfg.Accounts["customer_deposits"],
		GLBalance: g.String(), Subledger: decp(held).String(), Difference: g.Sub(decp(held)).String(), Deferred: "0", Recognised: "0", Breakage: "0"})
	if g, err = gl("caddy_fee_payable"); err != nil {
		return out, err
	}
	c, err := caddySubledger(ctx, q, property, to)
	if err != nil {
		return out, err
	}
	out.Rows = append(out.Rows, DeferredLiabilityRow{Type: "caddy_fee", Label: "Caddy fee liability", AccountCode: cfg.Accounts["caddy_fee_payable"],
		GLBalance: g.String(), Subledger: c.String(), Difference: g.Sub(c).String(), Deferred: "0", Recognised: "0", Breakage: "0"})
	if g, err = gl("loyalty_liability"); err != nil {
		return out, err
	}
	out.Rows = append(out.Rows, DeferredLiabilityRow{Type: "loyalty", Label: "Loyalty points", AccountCode: cfg.Accounts["loyalty_liability"],
		GLBalance: g.String(), Subledger: g.String(), Difference: "0", Deferred: "0", Recognised: "0", Breakage: "0"})
	return out, nil
}

// ── control account reconciliation (FR-AR-06, G6) ────────────────────────

// ControlReconciliation compares the control accounts with their
// sub-ledgers at a date.
type ControlReconciliation struct {
	AsOf   string              `json:"asOf"`
	Checks []ControlReconCheck `json:"checks"`
	OK     bool                `json:"ok"`
}

// ReconcileControls reconciles AR, AP and the liabilities at a date.
func ReconcileControls(ctx context.Context, q dbtx.Querier, property uuid.UUID, asOf time.Time) (ControlReconciliation, error) {
	out := ControlReconciliation{AsOf: ymd(asOf), Checks: []ControlReconCheck{}, OK: true}
	cfg, err := LoadConfiguration(ctx, q, property)
	if err != nil {
		return out, err
	}
	ar, err := roleBalance(ctx, q, property, cfg, "ar_control", &asOf)
	if err != nil {
		return out, err
	}
	var open, ledger *string
	if err := q.QueryRow(ctx, `WITH open AS (`+arOpenAsOf+`) SELECT sum(open_amount)::text FROM open WHERE open_amount > 0`, property, ymd(asOf)).Scan(&open); err != nil {
		return out, err
	}
	if err := q.QueryRow(ctx, `SELECT sum(amount)::text FROM accounting.ar_entries WHERE property_id = $1 AND entry_date <= $2`, property, dateOnly(asOf)).
		Scan(&ledger); err != nil {
		return out, err
	}
	ap, err := roleBalance(ctx, q, property, cfg, "ap_control", &asOf)
	if err != nil {
		return out, err
	}
	consignment, err := roleBalance(ctx, q, property, cfg, "consignment_payable", &asOf)
	if err != nil {
		return out, err
	}
	ap = ap.Add(consignment) // consignment payables are AP items too
	aging, err := APAgingAsOf(ctx, q, &property, asOf)
	if err != nil {
		return out, err
	}
	out.Checks = append(out.Checks,
		check("ar_control_vs_open_invoices", "AR control = open invoices of billing (AR Aging)", ar, decp(open)),
		check("ar_control_vs_ar_ledger", "AR control = AR ledger", ar, decp(ledger)),
		check("ap_control_vs_payables", "AP control = open payables (AP Aging)", ap.Neg(), dec(aging.Totals.Total)))
	dr, err := DeferredRevenue(ctx, q, property, asOf, asOf)
	if err != nil {
		return out, err
	}
	for _, r := range dr.Rows {
		if r.Type == "loyalty" {
			continue
		}
		out.Checks = append(out.Checks, check(r.Type, r.Label+" ("+r.AccountCode+") = sub-ledger", dec(r.GLBalance), dec(r.Subledger)))
	}
	for _, c := range out.Checks {
		out.OK = out.OK && c.OK
	}
	return out, nil
}

// ── posting reconciliation per business day (FR-PST-07) ───────────────────

// PostingReconRow compares one business line & revenue component.
type PostingReconRow struct {
	BusinessLine     string `json:"businessLine"`
	RevenueComponent string `json:"revenueComponent"`
	DailyRevenue     string `json:"dailyRevenue" doc:"Total of the Daily Revenue Report (net + service + tax)"`
	Journal          string `json:"journal" doc:"Total credited by the journals of the day (revenue, liabilities, tax, service)"`
	Difference       string `json:"difference"`
	OK               bool   `json:"ok"`
}

// PostingReconciliation is the posting reconciliation of a business day.
type PostingReconciliation struct {
	BusinessDate     string                      `json:"businessDate"`
	DayClosed        bool                        `json:"dayClosed" doc:"The business day was closed by the night audit (K4 received)"`
	Rows             []PostingReconRow           `json:"rows"`
	DailyRevenue     string                      `json:"dailyRevenue"`
	Journal          string                      `json:"journal"`
	ExportComponents []AccountingExportComponent `json:"exportComponents" doc:"Accounting Export (P1–P3) per component for the parallel run"`
	OK               bool                        `json:"ok"`
}

// AccountingExportComponent is one component of the Accounting Export of a day.
type AccountingExportComponent struct {
	Component string `json:"component" db:"component"`
	Liability bool   `json:"liability" db:"liability"`
	Amount    string `json:"amount" db:"amount"`
}

// ReconcilePosting compares the journals of a business day with the Daily
// Revenue Report (frozen with the night audit, or live) per business line
// and revenue component.
func ReconcilePosting(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time) (PostingReconciliation, error) {
	out := PostingReconciliation{BusinessDate: ymd(day), Rows: []PostingReconRow{}, OK: true}
	var summary []byte
	err := q.QueryRow(ctx, `SELECT summary FROM accounting.business_days WHERE property_id = $1 AND business_date = $2`, property, dateOnly(day)).Scan(&summary)
	if err != nil && !dbtx.IsNoRows(err) {
		return out, err
	}
	out.DayClosed = err == nil
	type key struct{ line, comp string }
	daily := map[key]decimal.Decimal{}
	if out.DayClosed {
		var s struct {
			Revenue []struct {
				BusinessLine     string `json:"businessLine"`
				RevenueComponent string `json:"revenueComponent"`
				Total            string `json:"total"`
			} `json:"revenue"`
		}
		_ = json.Unmarshal(summary, &s)
		for _, r := range s.Revenue {
			k := key{r.BusinessLine, r.RevenueComponent}
			daily[k] = daily[k].Add(dec(r.Total))
		}
	} else {
		rows, err := q.Query(ctx, `SELECT business_line, revenue_component, sum(total)::text FROM reporting.acc_folio_lines WHERE property_id = $1
			AND business_date = $2 AND voided_at IS NULL GROUP BY 1, 2`, property, dateOnly(day))
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var l, c, t string
			if err := rows.Scan(&l, &c, &t); err != nil {
				rows.Close()
				return out, err
			}
			daily[key{l, c}] = daily[key{l, c}].Add(dec(t))
		}
		rows.Close()
	}
	journal := map[key]decimal.Decimal{}
	rows, err := q.Query(ctx, `SELECT l.business_line, l.revenue_component, sum(l.credit - l.debit)::text FROM accounting.journal_lines l
		WHERE l.property_id = $1 AND l.business_line IS NOT NULL AND l.revenue_component IS NOT NULL AND l.journal_id IN (
		  SELECT DISTINCT s.journal_id FROM accounting.posted_sources s WHERE s.property_id = $1 AND s.source_type = 'billing.folio_line'
		  AND s.business_date = $2 AND s.journal_id IS NOT NULL)
		AND (l.source_type IS NULL OR l.source_type = 'billing.folio_line') GROUP BY 1, 2`, property, dateOnly(day))
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var l, c, t string
		if err := rows.Scan(&l, &c, &t); err != nil {
			rows.Close()
			return out, err
		}
		journal[key{l, c}] = journal[key{l, c}].Add(dec(t))
	}
	rows.Close()
	// lines of other business dates posted by the same journals
	rows, err = q.Query(ctx, `SELECT s.key1, s.key2, sum(s.amount)::text FROM accounting.posted_sources s WHERE s.property_id = $1
		AND s.source_type = 'billing.folio_line' AND s.business_date <> $2 AND s.journal_id IN (SELECT DISTINCT s2.journal_id FROM accounting.posted_sources s2
		WHERE s2.property_id = $1 AND s2.source_type = 'billing.folio_line' AND s2.business_date = $2) GROUP BY 1, 2`, property, dateOnly(day))
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var l, c, t string
		if err := rows.Scan(&l, &c, &t); err != nil {
			rows.Close()
			return out, err
		}
		journal[key{l, c}] = journal[key{l, c}].Sub(dec(t))
	}
	rows.Close()
	keys := map[key]bool{}
	for k := range daily {
		keys[k] = true
	}
	for k := range journal {
		keys[k] = true
	}
	var list []key
	for k := range keys {
		list = append(list, k)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].line == list[j].line {
			return list[i].comp < list[j].comp
		}
		return list[i].line < list[j].line
	})
	td, tj := decimal.Zero, decimal.Zero
	for _, k := range list {
		d, j := daily[k], journal[k]
		if d.IsZero() && j.IsZero() {
			continue
		}
		td, tj = td.Add(d), tj.Add(j)
		r := PostingReconRow{BusinessLine: k.line, RevenueComponent: k.comp, DailyRevenue: d.String(), Journal: j.String(), Difference: j.Sub(d).String(),
			OK: j.Equal(d)}
		out.OK = out.OK && r.OK
		out.Rows = append(out.Rows, r)
	}
	out.DailyRevenue, out.Journal = td.String(), tj.String()
	out.ExportComponents, err = handle.List[AccountingExportComponent](q.Query(ctx, `SELECT component, liability, trim_scale(amount)::text AS amount
		FROM reporting.acc_export_components WHERE property_id = $1 AND business_date = $2 ORDER BY component`, property, dateOnly(day)))
	return out, err
}

// ── service charge pool (FR-REV-06) ───────────────────────────────────────

// ServiceChargePoolLine is the basis of one department in a service charge pool.
type ServiceChargePoolLine struct {
	Department string `json:"department"`
	Weight     string `json:"weight"`
	Amount     string `json:"amount"`
}

// ServiceChargePool is the service charge collected in a month and its
// distribution basis (payment to employees in P5).
type ServiceChargePool struct {
	ID             uuid.UUID               `json:"id" db:"id"`
	Year           int                     `json:"year" db:"year"`
	Month          int                     `json:"month" db:"month"`
	Collected      string                  `json:"collected" db:"collected"`
	ReservePercent string                  `json:"reservePercent" db:"reserve_percent"`
	Reserve        string                  `json:"reserve" db:"reserve"`
	Distributable  string                  `json:"distributable" db:"distributable"`
	Basis          string                  `json:"basis" db:"basis"`
	Lines          []ServiceChargePoolLine `json:"lines" db:"lines"`
	Status         string                  `json:"status" db:"status" enum:"draft,approved"`
	ApprovedAt     *time.Time              `json:"approvedAt" db:"approved_at"`
	CreatedAt      time.Time               `json:"createdAt" db:"created_at"`
}

const poolSelect = `SELECT id, year, month, trim_scale(collected)::text AS collected, trim_scale(reserve_percent)::text AS reserve_percent,
	trim_scale(reserve)::text AS reserve, trim_scale(distributable)::text AS distributable, basis, lines, status, approved_at, created_at
	FROM accounting.service_charge_pools`

// ListServiceChargePools lists the pools of a property.
func ListServiceChargePools(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]ServiceChargePool, error) {
	return handle.List[ServiceChargePool](q.Query(ctx, poolSelect+` WHERE property_id = $1 ORDER BY year DESC, month DESC`, property))
}

// ServiceChargePoolInput computes the pool of a month.
type ServiceChargePoolInput struct {
	Year  int `json:"year"`
	Month int `json:"month"`
}

// ComputeServiceChargePool computes (or recomputes a draft) pool: service
// charge credited to the service charge payable account in the month, the
// reserve of the policy and the distribution per department (weights of
// the policy, else by the business line of the charges).
func (m *Module) ComputeServiceChargePool(ctx context.Context, tx pgx.Tx, property uuid.UUID, in ServiceChargePoolInput) (ServiceChargePool, error) {
	if in.Year < 2000 || in.Month < 1 || in.Month > 12 {
		return ServiceChargePool{}, handle.Invalid("month", "invalid", "a year and a month")
	}
	var st string
	err := tx.QueryRow(ctx, `SELECT status FROM accounting.service_charge_pools WHERE property_id = $1 AND year = $2 AND month = $3 FOR UPDATE`,
		property, in.Year, in.Month).Scan(&st)
	if err == nil && st == "approved" {
		return ServiceChargePool{}, errs.Conflict("approved", "the pool of the month is approved")
	}
	if err != nil && !dbtx.IsNoRows(err) {
		return ServiceChargePool{}, err
	}
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return ServiceChargePool{}, err
	}
	pol, err := loadServiceChargePolicy(ctx, tx, property)
	if err != nil {
		return ServiceChargePool{}, err
	}
	start := time.Date(in.Year, time.Month(in.Month), 1, 0, 0, 0, 0, time.UTC)
	rows, err := tx.Query(ctx, `SELECT coalesce(nullif(l.business_line, ''), 'other'), sum(l.credit - l.debit)::text FROM accounting.journal_lines l
		JOIN accounting.accounts a ON a.id = l.account_id WHERE a.code = $1 AND l.property_id = $2 AND l.journal_date BETWEEN $3 AND $4
		AND (l.partner_type IS NULL OR l.partner_type <> 'service_charge_payout') GROUP BY 1 ORDER BY 1`,
		cfg.Accounts["service_charge_payable"], property, start, monthEnd(start))
	if err != nil {
		return ServiceChargePool{}, err
	}
	perLine := map[string]decimal.Decimal{}
	collected := decimal.Zero
	for rows.Next() {
		var l, v string
		if err := rows.Scan(&l, &v); err != nil {
			rows.Close()
			return ServiceChargePool{}, err
		}
		perLine[l] = dec(v)
		collected = collected.Add(dec(v))
	}
	rows.Close()
	cur := currency(ctx, tx)
	rp := dec(pol.ReservePercent)
	reserve := collected.Mul(rp).Div(decimal.NewFromInt(100)).Round(places(cur))
	dist := collected.Sub(reserve)
	var lines []ServiceChargePoolLine
	basis := "business_line"
	if len(pol.DepartmentWeights) > 0 {
		basis = "department_weights"
		var deps []string
		total := decimal.Zero
		for d, w := range pol.DepartmentWeights {
			deps = append(deps, d)
			total = total.Add(dec(w))
		}
		sort.Strings(deps)
		acc := decimal.Zero
		for i, d := range deps {
			amt := decimal.Zero
			if total.IsPositive() {
				amt = dist.Mul(dec(pol.DepartmentWeights[d])).Div(total).Round(places(cur))
			}
			if i == len(deps)-1 {
				amt = dist.Sub(acc)
			}
			acc = acc.Add(amt)
			lines = append(lines, ServiceChargePoolLine{Department: d, Weight: pol.DepartmentWeights[d], Amount: amt.String()})
		}
	} else {
		var keys []string
		for k := range perLine {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		acc := decimal.Zero
		for i, k := range keys {
			amt := decimal.Zero
			if collected.IsPositive() {
				amt = dist.Mul(perLine[k]).Div(collected).Round(places(cur))
			}
			if i == len(keys)-1 {
				amt = dist.Sub(acc)
			}
			acc = acc.Add(amt)
			lines = append(lines, ServiceChargePoolLine{Department: k, Weight: perLine[k].String(), Amount: amt.String()})
		}
	}
	if lines == nil {
		lines = []ServiceChargePoolLine{}
	}
	raw, _ := json.Marshal(lines)
	if _, err := tx.Exec(ctx, `INSERT INTO accounting.service_charge_pools (id, property_id, year, month, collected, reserve_percent, reserve, distributable, basis,
		lines, created_by) VALUES ($1,$2,$3,$4,$5::numeric,$6::numeric,$7::numeric,$8::numeric,$9,$10,$11)
		ON CONFLICT (property_id, year, month) DO UPDATE SET collected = EXCLUDED.collected, reserve_percent = EXCLUDED.reserve_percent, reserve = EXCLUDED.reserve,
		distributable = EXCLUDED.distributable, basis = EXCLUDED.basis, lines = EXCLUDED.lines`, id.New(), property, in.Year, in.Month, collected.String(),
		rp.String(), reserve.String(), dist.String(), basis, raw, actorPtr(ctx)); err != nil {
		return ServiceChargePool{}, err
	}
	p, err := getOne[ServiceChargePool]("service charge pool")(tx.Query(ctx, poolSelect+` WHERE property_id = $1 AND year = $2 AND month = $3`, property, in.Year, in.Month))
	if err != nil {
		return p, err
	}
	return p, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "compute", EntityType: "accounting.service_charge_pool", EntityID: p.ID.String(),
		EntityLabel: start.Format("2006-01"), PropertyID: &property, After: p})
}

// ApproveServiceChargePool approves the pool basis of a month (handed to
// payroll in P5).
func (m *Module) ApproveServiceChargePool(ctx context.Context, tx pgx.Tx, property, pid uuid.UUID) (ServiceChargePool, error) {
	p, err := getOne[ServiceChargePool]("service charge pool")(tx.Query(ctx, poolSelect+` WHERE id = $1 AND property_id = $2 FOR UPDATE`, pid, property))
	if err != nil {
		return p, err
	}
	if p.Status == "approved" {
		return p, errs.Conflict("approved", "the pool is already approved")
	}
	if _, err := tx.Exec(ctx, `UPDATE accounting.service_charge_pools SET status = 'approved', approved_at = now(), approved_by = $2 WHERE id = $1`,
		pid, actorPtr(ctx)); err != nil {
		return p, err
	}
	after, err := getOne[ServiceChargePool]("service charge pool")(tx.Query(ctx, poolSelect+` WHERE id = $1`, pid))
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "approve", EntityType: "accounting.service_charge_pool", EntityID: pid.String(),
		EntityLabel: time.Date(p.Year, time.Month(p.Month), 1, 0, 0, 0, 0, time.UTC).Format("2006-01"), PropertyID: &property,
		Before: map[string]any{"status": p.Status}, After: map[string]any{"status": "approved"}})
}

// AccountingRevenueAllocation is a received K3 allocation (package, promotion).
type AccountingRevenueAllocation struct {
	ID           uuid.UUID       `json:"id" db:"id"`
	SourceType   string          `json:"sourceType" db:"source_type"`
	SourceID     uuid.UUID       `json:"sourceId" db:"source_id"`
	Reference    *string         `json:"reference" db:"reference"`
	BusinessLine *string         `json:"businessLine" db:"business_line"`
	Total        string          `json:"total" db:"total"`
	Discount     string          `json:"discount" db:"discount"`
	Components   json.RawMessage `json:"components" db:"components"`
	CreatedAt    time.Time       `json:"createdAt" db:"created_at"`
}

// ListRevenueAllocations lists the received revenue allocations.
func ListRevenueAllocations(ctx context.Context, q dbtx.Querier, property uuid.UUID, sourceType string) ([]AccountingRevenueAllocation, error) {
	return handle.List[AccountingRevenueAllocation](q.Query(ctx, `SELECT id, source_type, source_id, reference, business_line, trim_scale(total)::text AS total,
		trim_scale(discount)::text AS discount, components, created_at FROM accounting.revenue_allocations WHERE property_id = $1
		AND ($2 = '' OR source_type LIKE $2 || '%') ORDER BY created_at DESC LIMIT 500`, property, sourceType))
}
