package hris

// Calculation helpers on the HR policies, shared by the P5 areas (time &
// attendance, payroll, service charge): service length, overtime pay
// (FR-OVT-03), THR (FR-PAY-04), PKWT compensation (§16 #3), severance
// (FR-PAY-07) and the TER / Article 17 lookups. Money is decimal; rounding
// to the currency unit is left to the caller (Payroll Configuration
// roundingUnit).

import (
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Dec parses a decimal policy value ("" or invalid = 0).
func Dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(strings.TrimSpace(s))
	if err != nil {
		return decimal.Zero
	}
	return d
}

var (
	hundred = decimal.NewFromInt(100)
	twelve  = decimal.NewFromInt(12)
)

// ServiceMonths returns the completed months of service from join to at
// (0 when at is before join).
func ServiceMonths(join, at time.Time) int {
	if at.Before(join) {
		return 0
	}
	m := (at.Year()-join.Year())*12 + int(at.Month()) - int(join.Month())
	if at.Day() < join.Day() {
		m--
	}
	return max(m, 0)
}

// HourlyWage is monthly wage ÷ the Overtime Policy divisor (1/173).
func (p OvertimePolicy) HourlyWage(monthly decimal.Decimal) decimal.Decimal {
	div := Dec(p.HourlyDivisor)
	if !div.IsPositive() {
		return decimal.Zero
	}
	return monthly.Div(div)
}

// Overtime day kinds.
const (
	OvertimeWorkday     = "workday"
	OvertimeRestDay     = "rest_day"
	OvertimeShortestDay = "shortest_day"
)

// Steps returns the multiplier steps of a day kind for a work week of 5 or
// 6 days.
func (p OvertimePolicy) Steps(dayKind string, workWeekDays int) []OvertimeStep {
	switch {
	case dayKind == OvertimeWorkday:
		return p.Workday
	case dayKind == OvertimeShortestDay && workWeekDays == 6:
		return p.ShortestDaySixDayWeek
	case workWeekDays == 6:
		return p.RestDaySixDayWeek
	default:
		return p.RestDayFiveDayWeek
	}
}

// Multiplied returns the overtime hours of one day weighted by the
// multipliers (e.g. 3 hours on a workday = 1.5 + 2 + 2 = 5.5).
func (p OvertimePolicy) Multiplied(hours decimal.Decimal, dayKind string, workWeekDays int) decimal.Decimal {
	steps := p.Steps(dayKind, workWeekDays)
	total := decimal.Zero
	one := decimal.NewFromInt(1)
	remaining := hours
	for h := 1; remaining.IsPositive(); h++ {
		chunk := decimal.Min(one, remaining)
		factor := decimal.Zero
		for _, s := range steps {
			if h >= s.FromHour && (s.ToHour == 0 || h <= s.ToHour) {
				factor = Dec(s.Factor)
				break
			}
		}
		total = total.Add(chunk.Mul(factor))
		remaining = remaining.Sub(chunk)
	}
	return total
}

// Pay is the overtime pay of one day: hourly wage × multiplied hours
// (EP-08 AC: Rp5,190,000 / 173 = Rp30,000 per hour; 3 hours on a workday =
// Rp45,000 + Rp120,000 = Rp165,000).
func (p OvertimePolicy) Pay(monthly, hours decimal.Decimal, dayKind string, workWeekDays int) decimal.Decimal {
	return p.HourlyWage(monthly).Mul(p.Multiplied(hours, dayKind, workWeekDays))
}

// THRAmount is the THR of a wage after serviceMonths (EP-09 AC: Rp6,000,000
// after 6 months = 6/12 × Rp6,000,000 = Rp3,000,000).
func (c PayrollConfiguration) THRAmount(wage decimal.Decimal, serviceMonths int) decimal.Decimal {
	switch {
	case serviceMonths >= c.THR.FullAfterMonths:
		return wage
	case serviceMonths >= max(c.THR.MinServiceMonths, 1):
		return wage.Mul(decimal.NewFromInt(int64(serviceMonths))).Div(twelve)
	default:
		return decimal.Zero
	}
}

// PKWTCompensation is the compensation at the end of a PKWT: the configured
// months of wage per 12 months of service, proportional (PP 35/2021).
func (c HRConfiguration) PKWTCompensation(wage decimal.Decimal, serviceMonths int) decimal.Decimal {
	if serviceMonths < 1 {
		return decimal.Zero
	}
	return wage.Mul(Dec(c.PKWTCompensationPer12Months)).Mul(decimal.NewFromInt(int64(serviceMonths))).Div(twelve)
}

func stepMonths(steps []ServiceStep, years int) decimal.Decimal {
	out := decimal.Zero
	for _, s := range steps {
		if years >= s.MinYears {
			out = Dec(s.Months)
		}
	}
	return out
}

// SeverancePay returns the severance pay (pesangon × reason multiplier) and the
// service award of a wage after years of service (PP 35/2021).
func (c PayrollConfiguration) SeverancePay(wage decimal.Decimal, years int, reason string) (severance, award decimal.Decimal) {
	mult := decimal.NewFromInt(1)
	if m, ok := c.Severance.Multipliers[reason]; ok {
		mult = Dec(m)
	}
	severance = wage.Mul(stepMonths(c.Severance.SeveranceMonths, years)).Mul(mult)
	award = wage.Mul(stepMonths(c.Severance.ServiceAwardMonths, years))
	return severance, award
}

// Rate returns the percentage of an amount in a bracket table.
func Rate(table []TaxBracket, amount decimal.Decimal) decimal.Decimal {
	for _, b := range table {
		if b.UpTo == "" || amount.LessThanOrEqual(Dec(b.UpTo)) {
			return Dec(b.Rate)
		}
	}
	return decimal.Zero
}

// TERRate is the monthly effective PPh 21 rate (%) of a gross monthly
// income for a PTKP status (PP 58/2023).
func (c PayrollConfiguration) TERRate(ptkp string, gross decimal.Decimal) decimal.Decimal {
	cat := c.PPh21.TERCategories[ptkp]
	if cat == "" {
		cat = "A"
	}
	return Rate(c.PPh21.TERRates[cat], gross)
}

// ProgressiveTax is the Article 17 tax on an annual taxable income.
func ProgressiveTax(table []TaxBracket, taxable decimal.Decimal) decimal.Decimal {
	tax := decimal.Zero
	lower := decimal.Zero
	for _, b := range table {
		if !taxable.GreaterThan(lower) {
			break
		}
		upper := taxable
		if b.UpTo != "" && Dec(b.UpTo).LessThan(taxable) {
			upper = Dec(b.UpTo)
		}
		tax = tax.Add(upper.Sub(lower).Mul(Dec(b.Rate)).Div(hundred))
		if b.UpTo == "" {
			break
		}
		lower = Dec(b.UpTo)
	}
	return tax
}

// Contribution returns the employer and employee BPJS contributions of a
// wage (capped at the wage cap).
func Contribution(r ContributionRate, wage decimal.Decimal) (employer, employee decimal.Decimal) {
	base := wage
	if c := Dec(r.WageCap); c.IsPositive() && base.GreaterThan(c) {
		base = c
	}
	return base.Mul(Dec(r.EmployerPercent)).Div(hundred), base.Mul(Dec(r.EmployeePercent)).Div(hundred)
}
