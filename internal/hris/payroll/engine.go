package payroll

// Calculation of a payroll run (FR-PAY-02/03/04/07, FR-TAX-HR-01/03): load
// the policies, statutory rates, employees, contracts, structures, time &
// attendance, payroll inputs, adjustments, loans and the month / year to
// date of earlier runs, call the pure engine (hris.Calculate) per employee
// and store payslips and lines. A recalculation replaces them.

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/rules"
)

// runRow is the stored run the engine works on.
type runRow struct {
	ID             uuid.UUID
	PropertyID     uuid.UUID
	Number         string
	Name           string
	RunType        string
	PeriodCode     string
	PeriodStart    time.Time
	PeriodEnd      time.Time
	PaymentDate    time.Time
	CorrectsPeriod *string
	THRDate        *time.Time
	Religions      []string
	OrgUnitID      *uuid.UUID
	EmployeeIDs    []uuid.UUID
	Status         string
	Calculations   int
	TimeLockID     *uuid.UUID
	ApprovalID     *uuid.UUID
	Currency       string
}

func loadRun(ctx context.Context, tx pgx.Tx, rid uuid.UUID, forUpdate bool) (runRow, error) {
	q := `SELECT id, property_id, number, name, run_type, period_code, period_start, period_end, payment_date, corrects_period, thr_date, religions,
		org_unit_id, employee_ids, status, calculation_count, time_lock_id, approval_request_id, currency FROM hris.payroll_runs WHERE id = $1`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	var r runRow
	err := tx.QueryRow(ctx, q, rid).Scan(&r.ID, &r.PropertyID, &r.Number, &r.Name, &r.RunType, &r.PeriodCode, &r.PeriodStart, &r.PeriodEnd, &r.PaymentDate,
		&r.CorrectsPeriod, &r.THRDate, &r.Religions, &r.OrgUnitID, &r.EmployeeIDs, &r.Status, &r.Calculations, &r.TimeLockID, &r.ApprovalID, &r.Currency)
	if err != nil {
		if errs.Is(err, errs.KindNotFound) || strings.Contains(err.Error(), "no rows") {
			return r, errs.NotFound("payroll run")
		}
		return r, err
	}
	return r, nil
}

// priors are the earlier runs of the month and the year of an employee.
type priors struct {
	monthGross, monthTax, monthDeductible decimal.Decimal
	ytdGross, ytdDeductible, ytdTax       decimal.Decimal
	yearSeverance, yearSeveranceTax       decimal.Decimal
}

// loadPriors sums the approved / posted / paid runs of the tax year other
// than run, plus the imported opening YTD.
func loadPriors(ctx context.Context, tx pgx.Tx, run runRow, ids []uuid.UUID) (map[uuid.UUID]*priors, error) {
	out := map[uuid.UUID]*priors{}
	for _, e := range ids {
		out[e] = &priors{}
	}
	year := run.PeriodCode[:4]
	rows, err := tx.Query(ctx, `SELECT s.employee_id, r.period_code = $3 AS same_month, sum(s.taxable_gross), sum(s.pph21), sum(s.deductible), sum(s.severance),
		sum(s.pph21_final)
		FROM hris.payroll_slips s JOIN hris.payroll_runs r ON r.id = s.run_id
		WHERE r.property_id = $1 AND r.id <> $2 AND r.status IN ('approved', 'posted', 'paid') AND left(r.period_code, 4) = $4 AND r.period_code <= $3
		  AND s.employee_id = ANY ($5) GROUP BY 1, 2`, run.PropertyID, run.ID, run.PeriodCode, year, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var eid uuid.UUID
		var same bool
		var gross, tax, deductible, sev, sevTax decimal.Decimal
		if err := rows.Scan(&eid, &same, &gross, &tax, &deductible, &sev, &sevTax); err != nil {
			rows.Close()
			return nil, err
		}
		p := out[eid]
		if p == nil {
			continue
		}
		if same {
			p.monthGross, p.monthTax, p.monthDeductible = p.monthGross.Add(gross), p.monthTax.Add(tax), p.monthDeductible.Add(deductible)
		} else {
			p.ytdGross, p.ytdTax, p.ytdDeductible = p.ytdGross.Add(gross), p.ytdTax.Add(tax), p.ytdDeductible.Add(deductible)
		}
		p.yearSeverance, p.yearSeveranceTax = p.yearSeverance.Add(sev), p.yearSeveranceTax.Add(sevTax)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	month := run.PeriodCode[5:]
	yrows, err := tx.Query(ctx, `SELECT employee_id, gross, deductible, pph21 FROM hris.payroll_ytd WHERE property_id = $1 AND tax_year = $2::int
		AND through_month < $3::int AND employee_id = ANY ($4)`, run.PropertyID, year, month, ids)
	if err != nil {
		return nil, err
	}
	defer yrows.Close()
	for yrows.Next() {
		var eid uuid.UUID
		var gross, deductible, tax decimal.Decimal
		if err := yrows.Scan(&eid, &gross, &deductible, &tax); err != nil {
			return nil, err
		}
		if p := out[eid]; p != nil {
			p.ytdGross, p.ytdDeductible, p.ytdTax = p.ytdGross.Add(gross), p.ytdDeductible.Add(deductible), p.ytdTax.Add(tax)
		}
	}
	return out, yrows.Err()
}

// adjustment is an approved payroll adjustment of the run.
type adjustment struct {
	ID                         uuid.UUID
	EmployeeID                 uuid.UUID
	Code, Name, Kind, Category string
	Taxable, Irregular, PreTax bool
	Amount                     decimal.Decimal
	Number, Reason             string
}

// loanRow is an active loan with its next installment.
type loanRow struct {
	ID          uuid.UUID
	EmployeeID  uuid.UUID
	Type        string
	Installment decimal.Decimal
	Remaining   decimal.Decimal
}

// calcContext is everything shared by the employees of a run.
type calcContext struct {
	run        runRow
	cfg        hris.PayrollConfiguration
	overtime   hris.OvertimePolicy
	hrConfig   hris.HRConfiguration
	processing PayrollProcessing
	rates      hris.StatutoryRates
	rateSet    *uuid.UUID
	refs       []hris.PolicyUse
	cat        map[string]component
	today      time.Time
}

// slipResult is the calculated payslip of an employee.
type slipResult struct {
	emp      hris.Employee
	profile  hris.PayrollProfile
	contract *hris.Contract
	res      hris.PayResult
	time     *hris.TimeSummary
	messages []string
}

func (m *Module) calcContext(ctx context.Context, tx pgx.Tx, run runRow) (calcContext, error) {
	c := calcContext{run: run, today: today(ctx, tx, run.PropertyID)}
	at := hris.PolicyTime(run.PeriodEnd)
	cfg, r1, err := hris.LoadPayrollConfiguration(ctx, tx, run.PropertyID, at)
	if err != nil {
		return c, err
	}
	ot, r2, err := hris.LoadOvertimePolicy(ctx, tx, run.PropertyID, at)
	if err != nil {
		return c, err
	}
	hrc, r3, err := hris.LoadHRConfiguration(ctx, tx, run.PropertyID, at)
	if err != nil {
		return c, err
	}
	pp, r4, err := LoadProcessing(ctx, tx, run.PropertyID, at)
	if err != nil {
		return c, err
	}
	c.cfg, c.overtime, c.hrConfig, c.processing = cfg, ot, hrc, pp
	for _, r := range []rules.PolicyRef{r1, r2, r3, r4} {
		c.refs = append(c.refs, hris.PolicyUse{Code: r.Code, Version: r.Version})
	}
	if c.rates, c.rateSet, err = statutoryRatesAt(ctx, tx, run.PropertyID, run.PeriodEnd); err != nil {
		return c, err
	}
	if c.cat, err = loadComponents(ctx, tx, run.PropertyID, run.PeriodEnd); err != nil {
		return c, err
	}
	return c, nil
}

// employeesOf selects the employees of a run.
func (m *Module) employeesOf(ctx context.Context, tx pgx.Tx, c calcContext, adjs map[uuid.UUID][]adjustment) ([]hris.Employee, error) {
	run := c.run
	all, err := hris.Employees(ctx, tx, hris.EmployeeFilter{PropertyID: run.PropertyID, OrgUnitID: run.OrgUnitID, IDs: run.EmployeeIDs})
	if err != nil {
		return nil, err
	}
	var out []hris.Employee
	for _, e := range all {
		if e.JoinDate != nil && e.JoinDate.After(run.PeriodEnd) {
			continue
		}
		left := e.TerminationDate != nil && !e.TerminationDate.After(run.PeriodStart)
		switch run.RunType {
		case hris.RunRegular:
			if left || (e.Status != "active" && e.TerminationDate == nil) {
				continue
			}
		case hris.RunTHR:
			day := run.PaymentDate
			if run.THRDate != nil {
				day = *run.THRDate
			}
			if !e.EmployedOn(day) && (e.TerminationDate == nil || e.TerminationDate.Before(day.AddDate(0, 0, -30))) {
				continue
			}
			if len(run.Religions) > 0 {
				var rel *string
				if err := tx.QueryRow(ctx, `SELECT religion FROM hris.employees WHERE id = $1`, e.ID).Scan(&rel); err != nil {
					return nil, err
				}
				if rel == nil || !oneOf(run.Religions, *rel) {
					continue
				}
			}
		case hris.RunFinalSettlement:
			if e.TerminationDate == nil || e.TerminationDate.After(run.PeriodEnd.AddDate(0, 0, 1)) {
				continue
			}
		case hris.RunBonus, hris.RunAdjustment:
			if len(adjs[e.ID]) == 0 && run.RunType == hris.RunBonus {
				continue
			}
		}
		out = append(out, e)
	}
	if run.RunType == hris.RunAdjustment && run.CorrectsPeriod != nil {
		// employees paid in the corrected period
		rows, err := tx.Query(ctx, `SELECT DISTINCT s.employee_id FROM hris.payroll_slips s JOIN hris.payroll_runs r ON r.id = s.run_id
			WHERE r.property_id = $1 AND r.period_code = $2 AND r.status IN ('approved', 'posted', 'paid') AND r.run_type IN ('regular', 'adjustment')`,
			run.PropertyID, *run.CorrectsPeriod)
		if err != nil {
			return nil, err
		}
		paid := map[uuid.UUID]bool{}
		for rows.Next() {
			var eid uuid.UUID
			if err := rows.Scan(&eid); err != nil {
				rows.Close()
				return nil, err
			}
			paid[eid] = true
		}
		rows.Close()
		var kept []hris.Employee
		for _, e := range out {
			if paid[e.ID] || len(adjs[e.ID]) > 0 {
				kept = append(kept, e)
			}
		}
		out = kept
	} else if run.RunType == hris.RunAdjustment {
		var kept []hris.Employee
		for _, e := range out {
			if len(adjs[e.ID]) > 0 {
				kept = append(kept, e)
			}
		}
		out = kept
	}
	return out, nil
}

// loadAdjustments reserves the approved adjustments of the run.
func loadAdjustments(ctx context.Context, tx pgx.Tx, run runRow) (map[uuid.UUID][]adjustment, error) {
	if _, err := tx.Exec(ctx, `UPDATE hris.payroll_adjustments SET run_id = NULL WHERE run_id = $1 AND status = 'approved'`, run.ID); err != nil {
		return nil, err
	}
	target := run.RunType
	if target == hris.RunTHR {
		return map[uuid.UUID][]adjustment{}, nil
	}
	rows, err := tx.Query(ctx, `UPDATE hris.payroll_adjustments a SET run_id = $1 WHERE a.property_id = $2 AND a.status = 'approved' AND a.run_id IS NULL
		AND a.period_code = $3 AND a.target_run_type = $4 AND (cardinality($5::uuid[]) = 0 OR a.employee_id = ANY ($5))
		RETURNING a.id, a.employee_id, a.component_code, a.component_name, a.kind, a.category, a.taxable, a.irregular, a.pre_tax, a.amount, a.number, a.reason`,
		run.ID, run.PropertyID, run.PeriodCode, target, run.EmployeeIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID][]adjustment{}
	for rows.Next() {
		var a adjustment
		if err := rows.Scan(&a.ID, &a.EmployeeID, &a.Code, &a.Name, &a.Kind, &a.Category, &a.Taxable, &a.Irregular, &a.PreTax, &a.Amount, &a.Number,
			&a.Reason); err != nil {
			return nil, err
		}
		out[a.EmployeeID] = append(out[a.EmployeeID], a)
	}
	return out, rows.Err()
}

// loadLoans returns the installments due in the run (regular runs; the
// final settlement deducts the remaining balance).
func loadLoans(ctx context.Context, tx pgx.Tx, run runRow, ids []uuid.UUID) (map[uuid.UUID][]loanRow, error) {
	out := map[uuid.UUID][]loanRow{}
	if run.RunType != hris.RunRegular && run.RunType != hris.RunFinalSettlement {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT id, employee_id, loan_type, installment, principal - repaid FROM hris.employee_loans
		WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL AND start_period <= $2 AND principal > repaid AND employee_id = ANY ($3)
		ORDER BY created_at`, run.PropertyID, run.PeriodCode, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var l loanRow
		if err := rows.Scan(&l.ID, &l.EmployeeID, &l.Type, &l.Installment, &l.Remaining); err != nil {
			return nil, err
		}
		if run.RunType == hris.RunFinalSettlement {
			l.Installment = l.Remaining
		}
		out[l.EmployeeID] = append(out[l.EmployeeID], l)
	}
	return out, rows.Err()
}

// remainingLeave is the annual leave balance of an employee in a year.
func remainingLeave(ctx context.Context, tx pgx.Tx, employee uuid.UUID, year int) (decimal.Decimal, error) {
	var b decimal.Decimal
	err := tx.QueryRow(ctx, `SELECT coalesce(sum(entitled + carried_over - carried_expired + adjusted - used), 0) FROM hris.leave_balances
		WHERE employee_id = $1 AND leave_type = 'ANNUAL' AND year = $2`, employee, year).Scan(&b)
	return b, err
}

// buildItems turns the resolved pay of an employee into engine items.
func buildItems(c calcContext, lines []payLine, presentDays int) []hris.PayItem {
	basic, _ := fixedWage(lines)
	var items []hris.PayItem
	for _, l := range lines {
		it := l.comp.item(l.amount, l.source)
		it.SourceType, it.SourceID = "hris.salary_structure", l.ref
		if l.source == "contract" {
			it.SourceType = "hris.contract"
		}
		switch l.method {
		case "per_present_day":
			it.Quantity, it.Rate = decimal.NewFromInt(int64(presentDays)), dec(l.amount)
			it.Amount = it.Rate.Mul(it.Quantity)
			it.Prorate, it.Fixed = false, false
			it.Description = itoa(presentDays) + " present day(s)"
		case "percent_of_basic":
			it.Amount = dec(basic).Mul(dec(l.amount)).Div(decimal.NewFromInt(100))
			it.Description = l.amount + "% of basic salary"
		}
		items = append(items, it)
	}
	return items
}

// calculateRun computes every payslip of the run.
func (m *Module) calculateRun(ctx context.Context, tx pgx.Tx, run runRow) ([]slipResult, calcContext, error) {
	c, err := m.calcContext(ctx, tx, run)
	if err != nil {
		return nil, c, err
	}
	// close the attendance of the period up to yesterday
	if run.RunType == hris.RunRegular || run.RunType == hris.RunFinalSettlement {
		end := run.PeriodEnd
		if !end.Before(c.today) {
			end = c.today.AddDate(0, 0, -1)
		}
		if !end.Before(run.PeriodStart) {
			if err := hris.FinalizeAttendance(ctx, tx, run.PropertyID, run.PeriodStart, end); err != nil {
				return nil, c, err
			}
		}
	}
	adjs, err := loadAdjustments(ctx, tx, run)
	if err != nil {
		return nil, c, err
	}
	emps, err := m.employeesOf(ctx, tx, c, adjs)
	if err != nil {
		return nil, c, err
	}
	if len(emps) == 0 {
		return nil, c, errs.Conflict("no_employees", "no employee is paid by this run (check the period, filters and approved adjustments)")
	}
	ids := make([]uuid.UUID, len(emps))
	for i, e := range emps {
		ids[i] = e.ID
	}
	times := map[uuid.UUID]*hris.TimeSummary{}
	if run.RunType == hris.RunRegular {
		ts, err := hris.TimeSummaries(ctx, tx, run.PropertyID, run.PeriodStart, run.PeriodEnd, ids)
		if err != nil {
			return nil, c, err
		}
		for i := range ts {
			times[ts[i].EmployeeID] = &ts[i]
		}
	}
	inputs := map[uuid.UUID][]hris.PayrollInputLine{}
	if run.RunType == hris.RunRegular {
		lines, err := hris.CollectPayrollInputs(ctx, tx, run.PropertyID, run.PeriodStart, run.PeriodEnd, ids)
		if err != nil {
			return nil, c, err
		}
		for _, l := range lines {
			inputs[l.EmployeeID] = append(inputs[l.EmployeeID], l)
		}
	}
	loans, err := loadLoans(ctx, tx, run, ids)
	if err != nil {
		return nil, c, err
	}
	pri, err := loadPriors(ctx, tx, run, ids)
	if err != nil {
		return nil, c, err
	}
	paidRetro := map[uuid.UUID]periodPaid{}
	if run.RunType == hris.RunAdjustment && run.CorrectsPeriod != nil {
		if paidRetro, err = paidInPeriod(ctx, tx, run.PropertyID, *run.CorrectsPeriod); err != nil {
			return nil, c, err
		}
	}
	var out []slipResult
	for _, e := range emps {
		s, err := m.calculateEmployee(ctx, tx, c, e, times[e.ID], inputs[e.ID], adjs[e.ID], loans[e.ID], pri[e.ID], paidRetro[e.ID])
		if err != nil {
			return nil, c, err
		}
		out = append(out, s)
	}
	return out, c, nil
}

// periodPaid is what the approved runs paid an employee for a period.
type periodPaid struct {
	pay  map[string]decimal.Decimal // earnings (+) and deductions (−) by code
	bpjs map[string]decimal.Decimal // BPJS contributions by line code (BPJS_<PROGRAMME>_EE / _ER)
}

// paidInPeriod sums what the approved runs of a period paid per employee
// and code: structure, contract and time lines and the BPJS contributions
// of the regular run, plus the retro lines of earlier corrections.
func paidInPeriod(ctx context.Context, tx pgx.Tx, property uuid.UUID, period string) (map[uuid.UUID]periodPaid, error) {
	rows, err := tx.Query(ctx, `SELECT l.employee_id, l.code, l.kind IN ('bpjs_employee', 'bpjs_employer') AS bpjs,
		  sum(CASE WHEN l.kind = 'deduction' THEN -l.amount ELSE l.amount END)
		FROM hris.payroll_lines l JOIN hris.payroll_runs r ON r.id = l.run_id
		WHERE r.property_id = $1 AND r.status IN ('approved', 'posted', 'paid') AND r.run_type IN ('regular', 'adjustment')
		  AND ((r.run_type = 'regular' AND r.period_code = $2 AND (l.source IN ('structure', 'contract', 'time', 'grade', 'position', 'employee')
		        OR l.kind IN ('bpjs_employee', 'bpjs_employer')))
		    OR (r.run_type = 'adjustment' AND r.corrects_period = $2 AND l.source = 'retro'))
		  AND l.kind IN ('earning', 'deduction', 'bpjs_employee', 'bpjs_employer')
		GROUP BY 1, 2, 3`, property, period)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]periodPaid{}
	for rows.Next() {
		var eid uuid.UUID
		var code string
		var bpjs bool
		var amt decimal.Decimal
		if err := rows.Scan(&eid, &code, &bpjs, &amt); err != nil {
			return nil, err
		}
		p, ok := out[eid]
		if !ok {
			p = periodPaid{pay: map[string]decimal.Decimal{}, bpjs: map[string]decimal.Decimal{}}
			out[eid] = p
		}
		if bpjs {
			p.bpjs[code] = p.bpjs[code].Add(amt)
		} else {
			p.pay[code] = p.pay[code].Add(amt)
		}
	}
	return out, rows.Err()
}

func monthsWorked(join *time.Time, year, month int) int {
	if join == nil || join.Year() < year {
		return month
	}
	if join.Year() > year {
		return 0
	}
	return month - int(join.Month()) + 1
}

// regularInput builds the engine input of a regular period (also the
// recalculation of a corrected period).
func (m *Module) regularInput(ctx context.Context, tx pgx.Tx, c calcContext, e hris.Employee, from, to time.Time, ts *hris.TimeSummary,
	withInputs bool) (hris.PayInput, *hris.Contract, error) {
	day := to
	if e.TerminationDate != nil && e.TerminationDate.AddDate(0, 0, -1).Before(day) && !e.TerminationDate.AddDate(0, 0, -1).Before(from) {
		day = e.TerminationDate.AddDate(0, 0, -1)
	}
	contract, err := hris.ContractAt(ctx, tx, e.ID, day)
	if err != nil {
		return hris.PayInput{}, nil, err
	}
	lines, _, err := resolvePay(ctx, tx, c.cat, e, contract, day)
	if err != nil {
		return hris.PayInput{}, nil, err
	}
	present := 0
	if ts != nil {
		present = ts.PresentDays
	}
	week := 5
	if contract != nil && contract.WorkWeekDays > 0 {
		week = contract.WorkWeekDays
	}
	in := hris.PayInput{PeriodStart: from, PeriodEnd: to, JoinDate: e.JoinDate, LeaveDate: e.TerminationDate, WorkWeekDays: week,
		ProrationBasis: c.cfg.ProrationBasis, HourlyDivisor: dec(c.overtime.HourlyDivisor), RoundingUnit: dec(c.cfg.RoundingUnit), Rates: c.rates,
		Items: buildItems(c, lines, present), DeductAbsence: c.processing.DeductAbsence, BPJS: !oneOf(c.processing.BPJSExcludedCategories, e.WorkerCategory)}
	if ts != nil {
		in.UnpaidLeaveDays, in.AbsentDays, in.UnpaidPermissionHours = ts.UnpaidLeaveDays, decimal.NewFromInt(int64(ts.AbsentDays)), ts.UnpaidPermissionHours
		in.OvertimeHours, in.OvertimePayableHours = ts.Overtime.MultipliedHours, ts.Overtime.PayableHours
	}
	return in, contract, nil
}

func (m *Module) calculateEmployee(ctx context.Context, tx pgx.Tx, c calcContext, e hris.Employee, ts *hris.TimeSummary, inputs []hris.PayrollInputLine,
	adjs []adjustment, loans []loanRow, p *priors, paid periodPaid) (slipResult, error) {
	run := c.run
	s := slipResult{emp: e, time: ts}
	prof, err := hris.PayrollProfileOf(ctx, tx, e.ID)
	if err != nil {
		return s, err
	}
	s.profile = prof
	var in hris.PayInput
	switch run.RunType {
	case hris.RunRegular:
		if in, s.contract, err = m.regularInput(ctx, tx, c, e, run.PeriodStart, run.PeriodEnd, ts, true); err != nil {
			return s, err
		}
		if s.contract == nil {
			s.messages = append(s.messages, "No contract in force: no basic salary unless a salary structure defines it")
		}
	default:
		day := run.PeriodEnd
		if run.RunType == hris.RunTHR && run.THRDate != nil {
			day = *run.THRDate
		}
		if e.TerminationDate != nil && e.TerminationDate.AddDate(0, 0, -1).Before(day) {
			day = e.TerminationDate.AddDate(0, 0, -1)
		}
		if s.contract, err = hris.ContractAt(ctx, tx, e.ID, day); err != nil {
			return s, err
		}
		in = hris.PayInput{PeriodStart: run.PeriodStart, PeriodEnd: run.PeriodEnd, WorkWeekDays: 5, ProrationBasis: c.cfg.ProrationBasis,
			HourlyDivisor: dec(c.overtime.HourlyDivisor), RoundingUnit: dec(c.cfg.RoundingUnit), Rates: c.rates}
		if s.contract != nil && s.contract.WorkWeekDays > 0 {
			in.WorkWeekDays = s.contract.WorkWeekDays
		}
		lines, _, err := resolvePay(ctx, tx, c.cat, e, s.contract, day)
		if err != nil {
			return s, err
		}
		basic, wage := fixedWage(lines)
		switch run.RunType {
		case hris.RunTHR:
			basis := dec(wage)
			if c.cfg.THR.Basis == "base" {
				basis = dec(basic)
			}
			months := 0
			if e.JoinDate != nil {
				months = hris.ServiceMonths(*e.JoinDate, day)
			}
			amt := hris.Rp(c.cfg.THRAmount(basis, months))
			if amt.IsPositive() {
				it := componentOr(c.cat, "THR", "THR", "earning").item(amt.String(), "thr")
				it.Description = itoa(months) + " month(s) of service at " + ymd(day)
				if months < c.cfg.THR.FullAfterMonths {
					it.Description += " (" + itoa(months) + "/12)"
				}
				in.Items = append(in.Items, it)
			} else {
				s.messages = append(s.messages, "No THR: less than "+itoa(max(c.cfg.THR.MinServiceMonths, 1))+" month(s) of service")
			}
		case hris.RunFinalSettlement:
			items, msgs, err := m.settlementItems(ctx, tx, c, e, s.contract, basic, wage, in.WorkWeekDays)
			if err != nil {
				return s, err
			}
			in.Items = append(in.Items, items...)
			s.messages = append(s.messages, msgs...)
		case hris.RunAdjustment:
			if run.CorrectsPeriod != nil {
				items, bpjs, err := m.retroItems(ctx, tx, c, e, *run.CorrectsPeriod, paid)
				if err != nil {
					return s, err
				}
				in.Items = append(in.Items, items...)
				in.BPJSAdjustments = bpjs
			}
		}
	}
	// payroll inputs (service charge, commission, …)
	for _, l := range inputs {
		kind := "earning"
		if l.Kind == hris.PayrollInputDeduction {
			kind = "deduction"
		}
		comp := componentOr(c.cat, l.ComponentCode, l.Description, kind)
		it := comp.item(l.Amount.String(), "input:"+l.Source)
		it.Kind = kind
		it.Taxable = l.Kind == hris.PayrollInputEarning
		it.Irregular = l.Irregular || comp.Irregular
		it.PreTax, it.Fixed, it.Prorate = false, false, false
		it.SourceType, it.SourceID, it.Description = l.SourceType, l.SourceID.String(), l.Description
		in.Items = append(in.Items, it)
	}
	for _, a := range adjs {
		comp := componentOr(c.cat, a.Code, a.Name, a.Kind)
		it := comp.item(a.Amount.String(), "adjustment")
		it.Kind, it.Category, it.Taxable, it.Irregular, it.PreTax, it.Fixed, it.Prorate = a.Kind, a.Category, a.Taxable, a.Irregular, a.PreTax, false, false
		it.Name = a.Name
		it.SourceType, it.SourceID, it.Description = "hris.payroll_adjustment", a.ID.String(), a.Number+" · "+a.Reason
		in.Items = append(in.Items, it)
	}
	for _, l := range loans {
		amt := decimal.Min(l.Installment, l.Remaining)
		code := "LOAN"
		if l.Type == "cash_advance" {
			code = "CASH_ADVANCE"
		}
		it := componentOr(c.cat, code, "", "deduction").item(amt.String(), "loan")
		it.Kind, it.PreTax, it.Fixed, it.Prorate = "deduction", false, false, false
		it.SourceType, it.SourceID, it.Description = "hris.employee_loan", l.ID.String(), "Remaining before: "+l.Remaining.String()
		in.Items = append(in.Items, it)
	}
	// statutory
	in.PTKP = deref(prof.PTKPStatus)
	in.HasTaxID = (prof.NPWP != nil && strings.TrimSpace(*prof.NPWP) != "") || (prof.NIK != nil && strings.TrimSpace(*prof.NIK) != "")
	in.Tax = true
	year, month := run.PeriodEnd.Year(), int(run.PeriodEnd.Month())
	if pc, err := time.Parse("2006-01", run.PeriodCode); err == nil {
		year, month = pc.Year(), int(pc.Month())
	}
	leaves := e.TerminationDate != nil && !e.TerminationDate.After(run.PeriodEnd.AddDate(0, 0, 1))
	in.LastPeriod = month == 12 || (leaves && (run.RunType == hris.RunRegular || run.RunType == hris.RunFinalSettlement))
	in.MonthsWorked = monthsWorked(e.JoinDate, year, month)
	if p != nil {
		in.MonthPriorGross, in.MonthPriorTax, in.MonthPriorDeductible = p.monthGross, p.monthTax, p.monthDeductible
		in.YTDGross, in.YTDDeductible, in.YTDTax = p.ytdGross, p.ytdDeductible, p.ytdTax
		in.YearPriorSeverance, in.YearPriorSeveranceTax = p.yearSeverance, p.yearSeveranceTax
	}
	if in.PTKP == "" {
		in.PTKP = "TK/0"
		s.messages = append(s.messages, "No PTKP status on the employee: TK/0 applied")
	}
	if prof.Bank == nil {
		s.messages = append(s.messages, "No active bank account: excluded from the bank file")
	}
	if ts != nil && ts.Exceptions > 0 {
		s.messages = append(s.messages, itoa(ts.Exceptions)+" attendance exception(s) open in the period")
	}
	s.res = hris.Calculate(in)
	s.messages = append(s.messages, s.res.Messages...)
	return s, nil
}

// settlementItems are the final settlement lines of a leaver (FR-PAY-07).
func (m *Module) settlementItems(ctx context.Context, tx pgx.Tx, c calcContext, e hris.Employee, contract *hris.Contract, basic, wage string,
	week int) ([]hris.PayItem, []string, error) {
	var items []hris.PayItem
	var msgs []string
	var termType *string
	if err := tx.QueryRow(ctx, `SELECT termination_type FROM hris.employees WHERE id = $1`, e.ID).Scan(&termType); err != nil {
		return nil, nil, err
	}
	last := e.TerminationDate.AddDate(0, 0, -1)
	w := dec(wage)
	if c.processing.EncashLeaveOnTermination {
		bal, err := remainingLeave(ctx, tx, e.ID, last.Year())
		if err != nil {
			return nil, nil, err
		}
		divisor := dec(c.processing.DailyWageDivisorFiveDay)
		if week == 6 {
			divisor = dec(c.processing.DailyWageDivisorSixDay)
		}
		if bal.IsPositive() && divisor.IsPositive() {
			daily := w.Div(divisor)
			it := componentOr(c.cat, "LEAVE_ENCASHMENT", "Leave Encashment", "earning").item(hris.Rp(daily.Mul(bal)).String(), "settlement")
			it.Quantity, it.Rate = bal, daily.Round(2)
			it.Description = bal.String() + " day(s) of annual leave × wage ÷ " + divisor.String()
			items = append(items, it)
		}
	}
	months := 0
	if e.JoinDate != nil {
		months = hris.ServiceMonths(*e.JoinDate, e.TerminationDate.AddDate(0, 0, 0))
	}
	reason := ""
	if termType != nil {
		reason = c.processing.SeveranceReasons[*termType]
	}
	if reason != "" {
		sev, award := c.cfg.SeverancePay(w, months/12, reason)
		if sev.IsPositive() {
			it := componentOr(c.cat, "SEVERANCE", "Severance Pay", "earning").item(hris.Rp(sev).String(), "settlement")
			it.Description = itoa(months/12) + " year(s) of service, reason " + reason
			items = append(items, it)
		}
		if award.IsPositive() {
			it := componentOr(c.cat, "SERVICE_AWARD", "Service Award", "earning").item(hris.Rp(award).String(), "settlement")
			it.Description = itoa(months/12) + " year(s) of service"
			items = append(items, it)
		}
	}
	if c.processing.PKWTCompensation && termType != nil && *termType == "contract_ended" && contract != nil && contract.ContractType == "pkwt" {
		cm := hris.ServiceMonths(contract.StartDate, *e.TerminationDate)
		if comp := c.hrConfig.PKWTCompensation(w, cm); comp.IsPositive() {
			it := componentOr(c.cat, "PKWT_COMPENSATION", "PKWT Compensation", "earning").item(hris.Rp(comp).String(), "settlement")
			it.Description = itoa(cm) + " month(s) of PKWT " + contract.Number
			items = append(items, it)
		}
	}
	if len(items) == 0 {
		msgs = append(msgs, "No settlement amounts (leave balance, severance or PKWT compensation)")
	}
	_ = basic
	return items, msgs, nil
}

// retroItems recalculates the corrected period with today's data and pays
// the differences with what the approved runs paid (FR-PAY-06): pay lines
// as earnings, and the BPJS contributions of the recalculated fixed wage
// (statutory rates of the corrected period) as contribution differences.
func (m *Module) retroItems(ctx context.Context, tx pgx.Tx, c calcContext, e hris.Employee, period string, paid periodPaid) ([]hris.PayItem,
	[]hris.PayLine, error) {
	pc, err := time.Parse("2006-01", period)
	if err != nil {
		return nil, nil, err
	}
	from, to := periodBounds(pc, c.cfg.PeriodStartDay)
	ts, err := hris.TimeSummaries(ctx, tx, c.run.PropertyID, from, to, []uuid.UUID{e.ID})
	if err != nil {
		return nil, nil, err
	}
	var t *hris.TimeSummary
	if len(ts) > 0 {
		t = &ts[0]
	}
	in, _, err := m.regularInput(ctx, tx, c, e, from, to, t, false)
	if err != nil {
		return nil, nil, err
	}
	if in.Rates, _, err = statutoryRatesAt(ctx, tx, c.run.PropertyID, to); err != nil {
		return nil, nil, err
	}
	in.Tax, in.RoundingUnit = false, dec("1") // BPJS as in the regular run (worker categories excluded by the policy stay excluded)
	res := hris.Calculate(in)
	bpjs := hris.BPJSDifferences(res.Lines, paid.bpjs, period)
	now := map[string]hris.PayLine{}
	for _, l := range res.Lines {
		if l.Kind == hris.LineEarning || l.Kind == hris.LineDeduction {
			cur := now[l.Code]
			if cur.Code == "" {
				cur = l
				cur.Amount = decimal.Zero
			}
			if l.Kind == hris.LineDeduction {
				cur.Amount = cur.Amount.Sub(l.Amount)
			} else {
				cur.Amount = cur.Amount.Add(l.Amount)
			}
			now[l.Code] = cur
		}
	}
	codes := map[string]bool{}
	for k := range now {
		codes[k] = true
	}
	for k := range paid.pay {
		codes[k] = true
	}
	var keys []string
	for k := range codes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var items []hris.PayItem
	for _, code := range keys {
		diff := now[code].Amount.Sub(paid.pay[code])
		if diff.IsZero() {
			continue
		}
		l, ok := now[code]
		var comp component
		if ok {
			comp = component{Code: l.Code, Name: l.Name, Kind: "earning", Category: l.Category, Taxable: l.Taxable || l.PreTax, Irregular: l.Irregular}
		} else {
			comp = componentOr(c.cat, code, "", "")
			comp.Kind = "earning"
			if comp.PreTax {
				comp.Taxable = true
			}
		}
		// differences are earnings (a deduction difference is a negative earning)
		it := hris.PayItem{Code: comp.Code, Name: comp.Name, Kind: hris.LineEarning, Category: comp.Category, Taxable: comp.Taxable, Irregular: comp.Irregular,
			Amount: diff, Source: "retro", SourceType: "hris.payroll_period", SourceID: period, Description: "Correction of " + period}
		items = append(items, it)
	}
	return items, bpjs, nil
}

// periodBounds returns the dates of a payroll period: the calendar month,
// or from day N of the previous month to day N-1 (Payroll Configuration
// periodStartDay).
func periodBounds(month time.Time, startDay int) (time.Time, time.Time) {
	first := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	if startDay <= 1 || startDay > 28 {
		return first, first.AddDate(0, 1, -1)
	}
	from := time.Date(month.Year(), month.Month()-1, startDay, 0, 0, 0, 0, time.UTC)
	return from, time.Date(month.Year(), month.Month(), startDay-1, 0, 0, 0, 0, time.UTC)
}

// saveSlips replaces the payslips of the run and returns the totals.
func saveSlips(ctx context.Context, tx pgx.Tx, run runRow, slips []slipResult) (hris.PayrollTotals, map[string]decimal.Decimal, error) {
	if _, err := tx.Exec(ctx, `DELETE FROM hris.payroll_slips WHERE run_id = $1`, run.ID); err != nil {
		return hris.PayrollTotals{}, nil, err
	}
	sum := map[string]decimal.Decimal{}
	warnings := 0
	for _, s := range slips {
		r := s.res
		sid := id.New()
		status := "ok"
		if len(s.messages) > 0 {
			status = "warning"
			warnings++
		}
		var annual []byte
		if r.Annual != nil {
			annual, _ = json.Marshal(r.Annual)
		}
		var bankCode, bankName, accountNo, accountName *string
		if b := s.profile.Bank; b != nil {
			bankCode, bankName, accountNo, accountName = b.BankCode, &b.BankName, &b.AccountNo, &b.AccountName
		}
		var sched, present, absent int
		unpaid, ot := decimal.Zero, decimal.Zero
		if s.time != nil {
			sched, present, absent, unpaid, ot = s.time.ScheduledDays, s.time.PresentDays, s.time.AbsentDays, s.time.UnpaidLeaveDays, s.time.Overtime.PayableHours
		}
		e := s.emp
		msgs := s.messages
		if msgs == nil {
			msgs = []string{}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hris.payroll_slips (id, property_id, run_id, employee_id, employee_no, full_name, org_unit_id, org_unit_code,
			org_unit_name, position_name, grade_code, job_title, cost_center, employment_status, join_date, termination_date, ptkp_status, ter_category,
			tax_method, ter_rate, npwp, nik, bank_code, bank_name, account_no, account_name, proration_factor, fixed_wage, daily_wage, scheduled_days,
			present_days, absent_days, unpaid_leave_days, overtime_hours, gross, taxable_gross, deductible, severance, bpjs_employee, bpjs_employer, pph21,
			pph21_final, other_deductions, net, employer_cost, annual, messages, status)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,
			  $37,$38,$39,$40,$41,$42,$43,$44,$45,$46,$47,$48)`,
			sid, run.PropertyID, run.ID, e.ID, e.EmployeeNo, e.FullName, e.OrgUnitID, e.OrgUnitCode, e.OrgUnitName, e.PositionName, e.GradeCode, e.JobTitle,
			e.CostCenter, e.EmploymentStatus, e.JoinDate, e.TerminationDate, s.profile.PTKPStatus, r.TERCategory, r.TaxMethod, r.TERRate, s.profile.NPWP,
			s.profile.NIK, bankCode, bankName, accountNo, accountName, r.ProrationFactor.Round(6), hris.Rp(r.FixedWage), r.DailyWage.Round(2), sched, present,
			absent, unpaid, ot, r.Gross, r.TaxableGross, r.Deductible, r.Severance, r.BPJSEmployee, r.BPJSEmployer, r.PPh21, r.PPh21Final, r.OtherDeductions,
			r.Net, r.EmployerCost, annual, msgs, status); err != nil {
			return hris.PayrollTotals{}, nil, err
		}
		batch := &pgx.Batch{}
		for i, l := range r.Lines {
			var inputSource, inputKind *string
			if src, ok := strings.CutPrefix(l.Source, "input:"); ok {
				kind := string(inputKindOf(l))
				inputSource, inputKind = &src, &kind
			}
			batch.Queue(`INSERT INTO hris.payroll_lines (id, property_id, run_id, slip_id, employee_id, line_no, code, name, kind, category, programme, taxable,
				irregular, pre_tax, quantity, rate, amount, source, input_source, input_kind, source_type, source_id, description)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)`,
				id.New(), run.PropertyID, run.ID, sid, e.ID, i+1, l.Code, l.Name, l.Kind, l.Category, nullStr(l.Programme), l.Taxable, l.Irregular, l.PreTax,
				l.Quantity.Round(4), l.Rate.Round(4), l.Amount, l.Source, inputSource, inputKind, nullStr(l.SourceType), nullStr(l.SourceID),
				nullStr(l.Description))
		}
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return hris.PayrollTotals{}, nil, err
		}
		for k, v := range map[string]decimal.Decimal{"gross": r.Gross, "taxable": r.TaxableGross, "bpjsEE": r.BPJSEmployee, "bpjsER": r.BPJSEmployer,
			"pph21": r.PPh21.Add(r.PPh21Final), "other": r.OtherDeductions, "net": r.Net, "cost": r.EmployerCost} {
			sum[k] = sum[k].Add(v)
		}
	}
	t := hris.PayrollTotals{Headcount: len(slips), Gross: money(sum["gross"]), TaxableGross: money(sum["taxable"]), BPJSEmployee: money(sum["bpjsEE"]),
		BPJSEmployer: money(sum["bpjsER"]), PPh21: money(sum["pph21"]), OtherDeductions: money(sum["other"]), Net: money(sum["net"]),
		EmployerCost: money(sum["cost"]), EmployeesWarning: warnings}
	return t, sum, nil
}

// inputKindOf is the payroll input kind of a payslip line.
func inputKindOf(l hris.PayLine) hris.PayrollInputKind {
	switch {
	case l.Kind == hris.LineDeduction:
		return hris.PayrollInputDeduction
	case !l.Taxable:
		return hris.PayrollInputNonTax
	}
	return hris.PayrollInputEarning
}
