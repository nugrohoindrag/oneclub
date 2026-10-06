package hris

// Pure payroll calculation engine (PRD P5 EP-09, EP-10): one employee in
// one payroll run, from inputs already loaded by hris/payroll (salary
// structure, contract, time & attendance, payroll inputs, adjustments,
// loans, statutory rates and the month / year-to-date figures of earlier
// runs). No database access, so every rule is covered by worked examples
// in unit tests (FR-TAX-HR-05):
//
//   - proration of fixed pay for joiners and leavers (calendar or working
//     days, FR-PAY-03), unpaid leave, unexcused absence and unpaid
//     permission deducted at the daily / hourly wage
//   - overtime pay = monthly fixed wage ÷ 173 × multiplied hours (§16 #3)
//   - BPJS Kesehatan, JHT, JP, JKK, JKM with wage caps (FR-TAX-HR-03)
//   - PPh 21 with the monthly effective rate (TER) on the gross of the
//     month, and the annual calculation (Article 17) in the last tax period
//     of the year or of the employment; final tax on severance
//   - net pay rounded to the Payroll Configuration rounding unit
//
// Money is rounded to whole rupiah (half up); taxes are rounded down.

import (
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Payroll line kinds.
const (
	LineEarning      = "earning"
	LineDeduction    = "deduction"
	LineBPJSEmployee = "bpjs_employee"
	LineBPJSEmployer = "bpjs_employer"
	LineTax          = "tax"
)

// Component categories.
const (
	CatBasic           = "basic"
	CatAllowance       = "allowance"
	CatOvertime        = "overtime"
	CatServiceCharge   = "service_charge"
	CatCommission      = "commission"
	CatBonus           = "bonus"
	CatTHR             = "thr"
	CatSeverance       = "severance" // final tax (PP 68/2009)
	CatLeaveEncashment = "leave_encashment"
	CatAbsence         = "absence" // pre-tax deduction: unpaid leave, absence, unpaid permission
	CatLoan            = "loan"
	CatDeduction       = "deduction"
	CatStatutory       = "statutory"
	CatRounding        = "rounding"
	CatOther           = "other"
)

// ComponentCategories lists the categories of pay components.
var ComponentCategories = []string{CatBasic, CatAllowance, CatOvertime, CatServiceCharge, CatCommission, CatBonus, CatTHR, CatSeverance,
	CatLeaveEncashment, CatAbsence, CatLoan, CatDeduction, CatOther}

// PayItem is one amount entering the calculation.
type PayItem struct {
	Code        string
	Name        string
	Kind        string // LineEarning | LineDeduction
	Category    string
	Taxable     bool
	Irregular   bool
	PreTax      bool // deduction reducing the taxable gross
	Fixed       bool // counts in the monthly fixed wage (BPJS, overtime, THR, daily wage basis)
	Prorate     bool // prorated for joiners / leavers
	Quantity    decimal.Decimal
	Rate        decimal.Decimal
	Amount      decimal.Decimal
	Source      string // structure | contract | time | input:<code> | adjustment | loan | retro | thr | settlement
	SourceType  string
	SourceID    string
	Description string
}

// PayLine is one line of a payslip.
type PayLine struct {
	Code        string          `json:"code"`
	Name        string          `json:"name"`
	Kind        string          `json:"kind" enum:"earning,deduction,bpjs_employee,bpjs_employer,tax"`
	Category    string          `json:"category"`
	Taxable     bool            `json:"taxable"`
	Irregular   bool            `json:"irregular"`
	PreTax      bool            `json:"preTax"`
	Quantity    decimal.Decimal `json:"quantity"`
	Rate        decimal.Decimal `json:"rate"`
	Amount      decimal.Decimal `json:"amount"`
	Programme   string          `json:"programme,omitempty"`
	Source      string          `json:"source"`
	SourceType  string          `json:"sourceType,omitempty"`
	SourceID    string          `json:"sourceId,omitempty"`
	Description string          `json:"description"`
}

// PayInput is everything the engine needs for one employee in one run.
type PayInput struct {
	PeriodStart, PeriodEnd time.Time
	// JoinDate / LeaveDate bound the employment (LeaveDate = first day no
	// longer employed); nil = before / after the period.
	JoinDate, LeaveDate *time.Time
	WorkWeekDays        int
	ProrationBasis      string // calendar_days | working_days
	HourlyDivisor       decimal.Decimal
	RoundingUnit        decimal.Decimal
	// Items: fixed pay (prorated), variable pay, inputs, adjustments, loans.
	Items []PayItem
	// Time & attendance of the period.
	UnpaidLeaveDays       decimal.Decimal
	AbsentDays            decimal.Decimal // unexcused; deducted only when DeductAbsence
	DeductAbsence         bool
	UnpaidPermissionHours decimal.Decimal
	OvertimeHours         decimal.Decimal // multiplied hours (Σ payable hours × factor)
	OvertimePayableHours  decimal.Decimal
	// Statutory.
	Rates    StatutoryRates
	BPJS     bool // contributions are calculated (regular runs)
	BPJSWage *decimal.Decimal
	// BPJSAdjustments are contribution differences of a corrected period
	// (adjustment runs, BPJSDifferences); negative = refund.
	BPJSAdjustments []PayLine
	PTKP            string
	HasTaxID        bool // NPWP or NIK registered
	Tax             bool // PPh 21 is calculated
	LastPeriod      bool // December or the last period of the employment: annual calculation
	// Earlier runs of the same tax month and of the year before this month.
	MonthPriorGross       decimal.Decimal
	MonthPriorTax         decimal.Decimal
	MonthPriorDeductible  decimal.Decimal
	YTDGross              decimal.Decimal // Jan … previous month (incl. imported opening YTD)
	YTDDeductible         decimal.Decimal
	YTDTax                decimal.Decimal
	MonthsWorked          int // months of the year with employment up to this month (annual occupational cost limit)
	YearPriorSeverance    decimal.Decimal
	YearPriorSeveranceTax decimal.Decimal
}

// PayrollAnnualTax is the annual PPh 21 calculation of the last tax period.
type PayrollAnnualTax struct {
	Gross            decimal.Decimal `json:"gross"`
	OccupationalCost decimal.Decimal `json:"occupationalCost"`
	Contributions    decimal.Decimal `json:"contributions"`
	Net              decimal.Decimal `json:"net"`
	PTKP             decimal.Decimal `json:"ptkp"`
	TaxableIncome    decimal.Decimal `json:"taxableIncome" doc:"PKP, rounded down to Rp1,000"`
	AnnualTax        decimal.Decimal `json:"annualTax"`
	WithheldBefore   decimal.Decimal `json:"withheldBefore"`
	TaxThisPeriod    decimal.Decimal `json:"taxThisPeriod"`
	MonthsWorked     int             `json:"monthsWorked"`
}

// PayResult is the calculated pay of one employee in one run.
type PayResult struct {
	Lines           []PayLine
	ProrationFactor decimal.Decimal
	FixedWage       decimal.Decimal // full monthly fixed wage (BPJS / overtime basis)
	DailyWage       decimal.Decimal
	Gross           decimal.Decimal // earnings − pre-tax deductions
	TaxableGross    decimal.Decimal // TER base of this run (taxable earnings − pre-tax + taxable employer BPJS)
	Deductible      decimal.Decimal // employee JHT + JP of this run
	Severance       decimal.Decimal
	BPJSEmployee    decimal.Decimal
	BPJSEmployer    decimal.Decimal
	PPh21           decimal.Decimal // TER / annual
	PPh21Final      decimal.Decimal // final tax on severance
	OtherDeductions decimal.Decimal
	Net             decimal.Decimal
	EmployerCost    decimal.Decimal
	TaxMethod       string // ter | annual | none
	TERCategory     string
	TERRate         decimal.Decimal
	Annual          *PayrollAnnualTax
	Messages        []string
}

var (
	one      = decimal.NewFromInt(1)
	thousand = decimal.NewFromInt(1000)
)

// Rp rounds to whole rupiah (half up).
func Rp(d decimal.Decimal) decimal.Decimal { return d.Round(0) }

// RoundTo rounds to a unit (nearest; unit ≤ 1 = whole rupiah).
func RoundTo(d, unit decimal.Decimal) decimal.Decimal {
	if !unit.GreaterThan(one) {
		return Rp(d)
	}
	return d.Div(unit).Round(0).Mul(unit)
}

func dayOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// workDay reports whether a weekday is a working day of a 5 or 6 day week.
func workDay(d time.Time, workWeekDays int) bool {
	switch d.Weekday() {
	case time.Sunday:
		return false
	case time.Saturday:
		return workWeekDays == 6
	}
	return true
}

// PeriodDays counts the days of [from, to] (calendar_days) or its working
// days (working_days, Monday–Friday or Monday–Saturday).
func PeriodDays(basis string, from, to time.Time, workWeekDays int) int {
	n := 0
	for d := dayOnly(from); !d.After(dayOnly(to)); d = d.AddDate(0, 0, 1) {
		if basis != "working_days" || workDay(d, workWeekDays) {
			n++
		}
	}
	return n
}

// ProrationFactor is the employed share of a period (FR-PAY-03): employed
// days ÷ period days on the proration basis; 1 for a full period.
func ProrationFactor(basis string, from, to time.Time, join, leave *time.Time, workWeekDays int) decimal.Decimal {
	start, end := dayOnly(from), dayOnly(to)
	if join != nil && dayOnly(*join).After(start) {
		start = dayOnly(*join)
	}
	if leave != nil && dayOnly(*leave).AddDate(0, 0, -1).Before(end) {
		end = dayOnly(*leave).AddDate(0, 0, -1)
	}
	total := PeriodDays(basis, from, to, workWeekDays)
	if total == 0 || end.Before(start) {
		return decimal.Zero
	}
	worked := PeriodDays(basis, start, end, workWeekDays)
	if worked >= total {
		return one
	}
	return decimal.NewFromInt(int64(worked)).Div(decimal.NewFromInt(int64(total)))
}

// TERCategory returns the TER category of a PTKP status (A when unknown).
func (r StatutoryRates) TERCategory(ptkp string) string {
	if c := r.TERCategories[ptkp]; c != "" {
		return c
	}
	return "A"
}

// MonthlyTER is the PPh 21 of a month's gross with the effective rate:
// gross × TER rate, rounded down, + the surcharge without NPWP / NIK.
func (r StatutoryRates) MonthlyTER(ptkp string, gross decimal.Decimal, hasTaxID bool) (tax, rate decimal.Decimal) {
	if !gross.IsPositive() {
		return decimal.Zero, decimal.Zero
	}
	rate = Rate(r.TERRates[r.TERCategory(ptkp)], gross)
	tax = gross.Mul(rate).Div(hundred).Floor()
	if !hasTaxID {
		tax = tax.Mul(one.Add(Dec(r.NonNPWPSurchargePercent).Div(hundred))).Floor()
	}
	return tax, rate
}

// Annual is the annual PPh 21 (PMK 168/2023 last tax period): gross of the
// year − occupational cost (5%, at most the annual maximum pro rata to the
// months worked) − employee pension / JHT contributions − PTKP, rounded
// down to Rp1,000, taxed with the Article 17 rates.
func (r StatutoryRates) Annual(ptkp string, gross, contributions decimal.Decimal, monthsWorked int, hasTaxID bool) PayrollAnnualTax {
	months := min(max(monthsWorked, 1), 12)
	maxCost := Dec(r.OccupationalCostMaxAnnual).Mul(decimal.NewFromInt(int64(months))).Div(twelve)
	cost := decimal.Min(gross.Mul(Dec(r.OccupationalCostPercent)).Div(hundred), maxCost)
	if cost.IsNegative() {
		cost = decimal.Zero
	}
	cost = cost.Floor()
	a := PayrollAnnualTax{Gross: gross, OccupationalCost: cost, Contributions: contributions, MonthsWorked: months}
	a.Net = gross.Sub(cost).Sub(contributions)
	ptkpStatus := ptkp
	if r.PTKP[ptkpStatus] == "" {
		ptkpStatus = "TK/0"
	}
	a.PTKP = Dec(r.PTKP[ptkpStatus])
	pkp := a.Net.Sub(a.PTKP)
	if pkp.IsNegative() {
		pkp = decimal.Zero
	}
	a.TaxableIncome = pkp.Div(thousand).Floor().Mul(thousand)
	a.AnnualTax = ProgressiveTax(r.ProgressiveRates, a.TaxableIncome).Floor()
	if !hasTaxID {
		a.AnnualTax = a.AnnualTax.Mul(one.Add(Dec(r.NonNPWPSurchargePercent).Div(hundred))).Floor()
	}
	return a
}

// SeveranceTax is the final tax on severance paid in a year: progressive on
// the cumulative severance of the year, less the tax already withheld.
func (r StatutoryRates) SeveranceTax(amount, priorThisYear, priorTax decimal.Decimal) decimal.Decimal {
	if !amount.IsPositive() {
		return decimal.Zero
	}
	total := ProgressiveTax(r.SeveranceTaxBrackets, priorThisYear.Add(amount)).Floor()
	return decimal.Max(total.Sub(priorTax), decimal.Zero)
}

// Contributions are the BPJS shares of a monthly wage, rounded to rupiah.
type Contributions struct {
	Programme string
	Base      decimal.Decimal
	Employer  decimal.Decimal
	Employee  decimal.Decimal
}

// BPJSContributions returns the five programmes of a wage (FR-TAX-HR-03).
func (r StatutoryRates) BPJSContributions(wage decimal.Decimal) []Contributions {
	rates := map[string]ContributionRate{ProgrammeKesehatan: r.BPJS.Kesehatan, ProgrammeJHT: r.BPJS.JHT, ProgrammeJP: r.BPJS.JP, ProgrammeJKK: r.BPJS.JKK,
		ProgrammeJKM: r.BPJS.JKM}
	out := make([]Contributions, 0, len(BPJSProgrammes))
	for _, p := range BPJSProgrammes {
		rt := rates[p]
		base := wage
		if c := Dec(rt.WageCap); c.IsPositive() && base.GreaterThan(c) {
			base = c
		}
		er, ee := Contribution(rt, wage)
		out = append(out, Contributions{Programme: p, Base: base, Employer: Rp(er), Employee: Rp(ee)})
	}
	return out
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// bpjsNames are the payslip names of the contribution lines.
var bpjsNames = map[string]string{ProgrammeKesehatan: "BPJS Kesehatan", ProgrammeJHT: "BPJS JHT", ProgrammeJP: "BPJS JP", ProgrammeJKK: "BPJS JKK",
	ProgrammeJKM: "BPJS JKM"}

// Calculate computes the pay of one employee in one run.
func Calculate(in PayInput) PayResult {
	res := PayResult{TaxMethod: "none", TERCategory: in.Rates.TERCategory(in.PTKP)}
	divisor := in.HourlyDivisor
	if !divisor.IsPositive() {
		divisor = decimal.NewFromInt(173)
	}
	res.ProrationFactor = ProrationFactor(in.ProrationBasis, in.PeriodStart, in.PeriodEnd, in.JoinDate, in.LeaveDate, in.WorkWeekDays)
	for _, it := range in.Items {
		if it.Fixed && it.Kind == LineEarning {
			res.FixedWage = res.FixedWage.Add(it.Amount)
		}
	}
	days := PeriodDays(in.ProrationBasis, in.PeriodStart, in.PeriodEnd, in.WorkWeekDays)
	if days > 0 {
		res.DailyWage = res.FixedWage.Div(decimal.NewFromInt(int64(days)))
	}
	add := func(l PayLine) {
		if l.Quantity.IsZero() && l.Rate.IsZero() {
			l.Quantity = one
			l.Rate = l.Amount
		}
		res.Lines = append(res.Lines, l)
	}
	// earnings and deductions of the run
	for _, it := range in.Items {
		amt := it.Amount
		qty, rate := it.Quantity, it.Rate
		desc := it.Description
		if it.Prorate && res.ProrationFactor.LessThan(one) {
			amt = amt.Mul(res.ProrationFactor)
			qty, rate = res.ProrationFactor.Round(4), it.Amount
			if desc == "" {
				desc = "Prorated " + res.ProrationFactor.Mul(hundred).Round(2).String() + "%"
			}
		}
		amt = Rp(amt)
		if amt.IsZero() && !it.Fixed {
			continue
		}
		add(PayLine{Code: it.Code, Name: it.Name, Kind: it.Kind, Category: it.Category, Taxable: it.Taxable, Irregular: it.Irregular, PreTax: it.PreTax,
			Quantity: qty, Rate: rate, Amount: amt, Source: it.Source, SourceType: it.SourceType, SourceID: it.SourceID, Description: desc})
	}
	// overtime (FR-OVT-03): hourly wage = fixed wage ÷ 173
	if in.OvertimeHours.IsPositive() && res.FixedWage.IsPositive() {
		hourly := res.FixedWage.Div(divisor)
		add(PayLine{Code: "OVERTIME", Name: "Overtime", Kind: LineEarning, Category: CatOvertime, Taxable: true, Quantity: in.OvertimeHours,
			Rate: hourly.Round(2), Amount: Rp(res.FixedWage.Mul(in.OvertimeHours).Div(divisor)), Source: "time",
			Description: in.OvertimePayableHours.String() + " h payable = " + in.OvertimeHours.String() + " h multiplied × 1/" + divisor.String()})
	}
	// unpaid leave, absence, unpaid permission (pre-tax)
	if in.UnpaidLeaveDays.IsPositive() && res.DailyWage.IsPositive() {
		add(PayLine{Code: "UNPAID_LEAVE", Name: "Unpaid Leave", Kind: LineDeduction, Category: CatAbsence, PreTax: true, Quantity: in.UnpaidLeaveDays,
			Rate: res.DailyWage.Round(2), Amount: Rp(res.DailyWage.Mul(in.UnpaidLeaveDays)), Source: "time", Description: in.UnpaidLeaveDays.String() + " day(s)"})
	}
	if in.DeductAbsence && in.AbsentDays.IsPositive() && res.DailyWage.IsPositive() {
		add(PayLine{Code: "ABSENCE", Name: "Absence", Kind: LineDeduction, Category: CatAbsence, PreTax: true, Quantity: in.AbsentDays,
			Rate: res.DailyWage.Round(2), Amount: Rp(res.DailyWage.Mul(in.AbsentDays)), Source: "time", Description: in.AbsentDays.String() + " day(s) absent"})
	}
	if in.UnpaidPermissionHours.IsPositive() && res.FixedWage.IsPositive() {
		hourly := res.FixedWage.Div(divisor)
		add(PayLine{Code: "UNPAID_PERMISSION", Name: "Unpaid Permission", Kind: LineDeduction, Category: CatAbsence, PreTax: true,
			Quantity: in.UnpaidPermissionHours, Rate: hourly.Round(2), Amount: Rp(hourly.Mul(in.UnpaidPermissionHours)), Source: "time",
			Description: in.UnpaidPermissionHours.String() + " h"})
	}
	// BPJS (FR-TAX-HR-03) on the full monthly fixed wage
	taxableER := decimal.Zero
	if in.BPJS {
		wage := res.FixedWage
		if in.BPJSWage != nil {
			wage = *in.BPJSWage
		}
		for _, c := range in.Rates.BPJSContributions(wage) {
			up := strings.ToUpper(c.Programme)
			if c.Employee.IsPositive() {
				add(PayLine{Code: "BPJS_" + up + "_EE", Name: bpjsNames[c.Programme], Kind: LineBPJSEmployee, Category: CatStatutory, Programme: c.Programme,
					Quantity: c.Base, Rate: rateOf(c.Employee, c.Base), Amount: c.Employee, Source: "statutory"})
				if contains(in.Rates.DeductibleEmployeeContributions, c.Programme) {
					res.Deductible = res.Deductible.Add(c.Employee)
				}
			}
			if c.Employer.IsPositive() {
				add(PayLine{Code: "BPJS_" + up + "_ER", Name: bpjsNames[c.Programme] + " (employer)", Kind: LineBPJSEmployer, Category: CatStatutory,
					Programme: c.Programme, Quantity: c.Base, Rate: rateOf(c.Employer, c.Base), Amount: c.Employer, Source: "statutory"})
				if contains(in.Rates.TaxableEmployerContributions, c.Programme) {
					taxableER = taxableER.Add(c.Employer)
				}
			}
		}
	}
	// retro differences of the contributions of a corrected period: taxed
	// and deductible like the contributions they correct
	for _, l := range in.BPJSAdjustments {
		if l.Amount.IsZero() || (l.Kind != LineBPJSEmployee && l.Kind != LineBPJSEmployer) {
			continue
		}
		add(l)
		if l.Kind == LineBPJSEmployee && contains(in.Rates.DeductibleEmployeeContributions, l.Programme) {
			res.Deductible = res.Deductible.Add(l.Amount)
		}
		if l.Kind == LineBPJSEmployer && contains(in.Rates.TaxableEmployerContributions, l.Programme) {
			taxableER = taxableER.Add(l.Amount)
		}
	}
	// totals before tax
	taxable := decimal.Zero
	for _, l := range res.Lines {
		switch l.Kind {
		case LineEarning:
			res.Gross = res.Gross.Add(l.Amount)
			if l.Category == CatSeverance {
				res.Severance = res.Severance.Add(l.Amount)
			} else if l.Taxable {
				taxable = taxable.Add(l.Amount)
			}
		case LineDeduction:
			if l.PreTax {
				res.Gross = res.Gross.Sub(l.Amount)
				taxable = taxable.Sub(l.Amount)
			} else {
				res.OtherDeductions = res.OtherDeductions.Add(l.Amount)
			}
		case LineBPJSEmployee:
			res.BPJSEmployee = res.BPJSEmployee.Add(l.Amount)
		case LineBPJSEmployer:
			res.BPJSEmployer = res.BPJSEmployer.Add(l.Amount)
		}
	}
	res.TaxableGross = taxable.Add(taxableER)
	// PPh 21
	if in.Tax {
		monthGross := in.MonthPriorGross.Add(res.TaxableGross)
		if in.LastPeriod {
			res.TaxMethod = "annual"
			gross := in.YTDGross.Add(monthGross)
			contrib := in.YTDDeductible.Add(in.MonthPriorDeductible).Add(res.Deductible)
			a := in.Rates.Annual(in.PTKP, gross, contrib, in.MonthsWorked, in.HasTaxID)
			a.WithheldBefore = in.YTDTax.Add(in.MonthPriorTax)
			a.TaxThisPeriod = a.AnnualTax.Sub(a.WithheldBefore)
			res.Annual = &a
			res.PPh21 = a.TaxThisPeriod
		} else if !res.TaxableGross.IsZero() || in.MonthPriorGross.IsPositive() {
			res.TaxMethod = "ter"
			tax, rate := in.Rates.MonthlyTER(in.PTKP, monthGross, in.HasTaxID)
			res.TERRate = rate
			res.PPh21 = decimal.Max(tax.Sub(in.MonthPriorTax), decimal.Zero)
		}
		if !res.PPh21.IsZero() || res.TaxMethod != "none" {
			desc := "TER " + res.TERCategory + " " + res.TERRate.String() + "% × " + monthGross.String()
			if res.Annual != nil {
				desc = "Annual: PKP " + res.Annual.TaxableIncome.String() + ", tax " + res.Annual.AnnualTax.String() + " − withheld " + res.Annual.WithheldBefore.String()
			}
			if !in.HasTaxID {
				desc += " (+" + in.Rates.NonNPWPSurchargePercent + "% without NPWP)"
			}
			add(PayLine{Code: "PPH21", Name: "PPh 21", Kind: LineTax, Category: CatStatutory, Quantity: one, Rate: res.PPh21, Amount: res.PPh21,
				Source: "statutory", Description: desc})
		}
		if res.Severance.IsPositive() {
			res.PPh21Final = in.Rates.SeveranceTax(res.Severance, in.YearPriorSeverance, in.YearPriorSeveranceTax)
			add(PayLine{Code: "PPH21_FINAL", Name: "PPh 21 Final (severance)", Kind: LineTax, Category: CatStatutory, Quantity: one, Rate: res.PPh21Final,
				Amount: res.PPh21Final, Source: "statutory", Description: "PP 68/2009 on " + res.Severance.String()})
		}
	}
	res.Net = res.Gross.Sub(res.BPJSEmployee).Sub(res.PPh21).Sub(res.PPh21Final).Sub(res.OtherDeductions)
	if rounded := RoundTo(res.Net, in.RoundingUnit); !rounded.Equal(res.Net) {
		diff := rounded.Sub(res.Net)
		add(PayLine{Code: "ROUNDING", Name: "Rounding", Kind: LineEarning, Category: CatRounding, Quantity: one, Rate: diff, Amount: diff, Source: "rounding"})
		res.Gross = res.Gross.Add(diff)
		res.Net = rounded
	}
	res.EmployerCost = res.Gross.Add(res.BPJSEmployer)
	if in.Tax && in.PTKP == "" {
		res.Messages = append(res.Messages, "No PTKP status: TK/0 applied")
	}
	if res.Net.IsNegative() {
		res.Messages = append(res.Messages, "Negative net pay")
	}
	sortLines(res.Lines)
	return res
}

// BPJSDifferences are the contribution lines of a retro correction (FR-PAY
// retro, FR-TAX-HR-03): the BPJS lines of the corrected period recalculated
// with today's data (now, from Calculate with BPJS) less what the approved
// runs paid for that period (paid, by line code BPJS_<PROGRAMME>_EE / _ER,
// earlier corrections included). One line per programme and side with a
// difference, sorted by code; negative = refund (e.g. a cap reached, or a
// worker category excluded from BPJS since).
func BPJSDifferences(now []PayLine, paid map[string]decimal.Decimal, period string) []PayLine {
	cur := map[string]PayLine{}
	for _, l := range now {
		if l.Kind == LineBPJSEmployee || l.Kind == LineBPJSEmployer {
			x := cur[l.Code]
			if x.Code == "" {
				x = l
				x.Amount = decimal.Zero
			}
			x.Amount = x.Amount.Add(l.Amount)
			cur[l.Code] = x
		}
	}
	codes := make([]string, 0, len(cur)+len(paid))
	for c := range cur {
		codes = append(codes, c)
	}
	for c := range paid {
		if _, ok := cur[c]; !ok {
			codes = append(codes, c)
		}
	}
	sort.Strings(codes)
	var out []PayLine
	for _, code := range codes {
		diff := cur[code].Amount.Sub(paid[code])
		if diff.IsZero() {
			continue
		}
		l, ok := cur[code]
		if !ok {
			l = bpjsLineOf(code)
			if l.Code == "" {
				continue // not a contribution line
			}
		}
		out = append(out, PayLine{Code: code, Name: l.Name, Kind: l.Kind, Category: CatStatutory, Programme: l.Programme, Quantity: one, Rate: diff,
			Amount: diff, Source: "retro", SourceType: "hris.payroll_period", SourceID: period,
			Description: "Correction of " + period + ": " + cur[code].Amount.String() + " − paid " + paid[code].String()})
	}
	return out
}

// bpjsLineOf describes a contribution line code (BPJS_JHT_EE …).
func bpjsLineOf(code string) PayLine {
	rest, ok := strings.CutPrefix(code, "BPJS_")
	if !ok {
		return PayLine{}
	}
	kind, name := "", ""
	switch {
	case strings.HasSuffix(rest, "_EE"):
		kind, rest = LineBPJSEmployee, strings.TrimSuffix(rest, "_EE")
	case strings.HasSuffix(rest, "_ER"):
		kind, rest = LineBPJSEmployer, strings.TrimSuffix(rest, "_ER")
	default:
		return PayLine{}
	}
	p := strings.ToLower(rest)
	if name = bpjsNames[p]; name == "" {
		return PayLine{}
	}
	if kind == LineBPJSEmployer {
		name += " (employer)"
	}
	return PayLine{Code: code, Name: name, Kind: kind, Programme: p}
}

func rateOf(amount, base decimal.Decimal) decimal.Decimal {
	if !base.IsPositive() {
		return decimal.Zero
	}
	return amount.Mul(hundred).Div(base).Round(2)
}

var kindOrder = map[string]int{LineEarning: 0, LineDeduction: 1, LineBPJSEmployee: 2, LineTax: 3, LineBPJSEmployer: 4}

func sortLines(lines []PayLine) {
	sort.SliceStable(lines, func(i, j int) bool { return kindOrder[lines[i].Kind] < kindOrder[lines[j].Kind] })
}
