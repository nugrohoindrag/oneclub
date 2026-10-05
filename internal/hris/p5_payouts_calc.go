package hris

// Pure calculation engines of the payouts area (no database): the service
// charge distribution of the Service Charge Policy (PRD P5 §16 #5, EP-11
// AC), the partner payout line with PPh 21 non-employee (Article 17 rates ×
// 50% of gross, §16 #4, FR-TAX-HR-02) and BPJS Ketenagakerjaan BPU, the
// commission payout split (EP-12) and the partner payout periods
// (caddies on the 15th and at month end, instructors monthly). Every rate
// comes from a versioned policy; unit tests in p5_payouts_calc_test.go.

import (
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// RoundDown rounds an amount down to a unit (e.g. 1 = whole rupiah); a
// non-positive unit keeps 2 decimals.
func RoundDown(v, unit decimal.Decimal) decimal.Decimal {
	if !unit.IsPositive() {
		return v.RoundFloor(2)
	}
	return v.Div(unit).Floor().Mul(unit)
}

// RoundNearest rounds an amount half up to a unit.
func RoundNearest(v, unit decimal.Decimal) decimal.Decimal {
	if !unit.IsPositive() {
		return v.Round(2)
	}
	return v.Div(unit).Round(0).Mul(unit)
}

// ── service charge distribution (EP-11) ──────────────────────────────────

// Exclusion reasons of the service charge distribution.
const (
	SCReasonStatus             = "status_not_eligible"
	SCReasonProbation          = "probation"
	SCReasonCategory           = "worker_category"
	SCReasonWarning            = "warning_letter"
	SCReasonUnpaidLeave        = "unpaid_leave_full_period"
	SCReasonNoDepartment       = "no_department_share"
	SCReasonNoPoints           = "no_points"
	SCReasonNotInPeriod        = "not_employed_in_period"
	scGroupAll                 = "*"
	scDisplayPlaces      int32 = 2
)

// ServiceChargeCandidate is an employee of the period.
type ServiceChargeCandidate struct {
	EmployeeID       uuid.UUID
	OrgUnitCodes     []string // own org unit first, then its parents (department shares)
	GradeCode        string
	EmploymentStatus string
	WorkerCategory   string
	InProbation      bool // probation not yet completed at the period end
	WarningLevel     int  // highest active warning letter (SP) in the period
	EmployedInPeriod bool
	ScheduledDays    int
	PresentDays      decimal.Decimal // present days (paid leave included when it counts)
	UnpaidLeaveDays  decimal.Decimal
}

// ServiceChargeShare is the result of one employee.
type ServiceChargeShare struct {
	EmployeeID       uuid.UUID
	Eligible         bool
	Reason           string
	Group            string // department share (org unit code) or "*"
	Points           decimal.Decimal
	Factor           decimal.Decimal // attendance factor
	BaseShare        decimal.Decimal // share before the attendance factor
	AttendanceAmount decimal.Decimal // base share × factor
	Redistributed    decimal.Decimal // part of the forfeited amount redistributed to the employee
	Amount           decimal.Decimal // paid (rounded down to the rounding unit)
}

// ServiceChargeResult is a distribution: Collected = Reserve + Distributed +
// Undistributed + RoundingDifference (to the cent, by construction).
type ServiceChargeResult struct {
	Collected          decimal.Decimal
	Reserve            decimal.Decimal
	Distributable      decimal.Decimal
	Distributed        decimal.Decimal
	Undistributed      decimal.Decimal
	RoundingDifference decimal.Decimal
	Eligible           int
	Shares             []ServiceChargeShare
}

func pct(v decimal.Decimal, p string) decimal.Decimal { return v.Mul(Dec(p)).Div(hundred) }

// eligibility returns the exclusion reason of a candidate ("" = eligible).
func (p ServiceChargePolicy) eligibility(c ServiceChargeCandidate) string {
	switch {
	case !c.EmployedInPeriod:
		return SCReasonNotInPeriod
	case p.ExcludeProbation && (c.InProbation || c.EmploymentStatus == StatusProbation):
		return SCReasonProbation
	case len(p.EligibleStatuses) > 0 && !slices.Contains(p.EligibleStatuses, c.EmploymentStatus):
		return SCReasonStatus
	case slices.Contains(p.ExcludeWorkerCategories, c.WorkerCategory):
		return SCReasonCategory
	case p.ExcludeWarningLevelFrom > 0 && c.WarningLevel >= p.ExcludeWarningLevelFrom:
		return SCReasonWarning
	case p.ExcludeFullPeriodUnpaid && c.UnpaidLeaveDays.IsPositive() && c.PresentDays.IsZero() &&
		c.UnpaidLeaveDays.GreaterThanOrEqual(decimal.NewFromInt(int64(max(c.ScheduledDays, 1)))):
		return SCReasonUnpaidLeave
	}
	return ""
}

// attendance is days present ÷ working days (1 without a schedule or when
// the policy has no attendance factor), capped at 1.
func (p ServiceChargePolicy) attendance(c ServiceChargeCandidate) decimal.Decimal {
	one := decimal.NewFromInt(1)
	if !p.AttendanceFactor || c.ScheduledDays <= 0 {
		return one
	}
	f := c.PresentDays.Div(decimal.NewFromInt(int64(c.ScheduledDays)))
	if f.GreaterThan(one) {
		return one
	}
	if f.IsNegative() {
		return decimal.Zero
	}
	return f.Round(4)
}

// DistributeServiceCharge distributes a month's pool (PRD P5 §16 #5): the
// reserve (5%) stays for breakage & loss; the distributed share (95%) is
// split per department share (when configured) equally per eligible
// employee — or by grade points — multiplied by the attendance factor;
// what the factor forfeits is redistributed by the same rule (weights ×
// factor) or left undistributed. Lines are rounded down to the rounding
// unit; the difference goes to the rounding account (FR-SVC-04).
func DistributeServiceCharge(p ServiceChargePolicy, collected decimal.Decimal, cands []ServiceChargeCandidate) ServiceChargeResult {
	unit := Dec(p.RoundingUnit)
	res := ServiceChargeResult{Collected: collected}
	res.Reserve = pct(collected, p.ReservePercent).Round(scDisplayPlaces)
	res.Distributable = pct(collected, p.DistributedPercent).Round(scDisplayPlaces)
	if res.Reserve.Add(res.Distributable).GreaterThan(collected) {
		res.Distributable = collected.Sub(res.Reserve)
	}
	res.Undistributed = collected.Sub(res.Reserve).Sub(res.Distributable)

	// groups: department shares (org unit code → %) or one pool
	groups := map[string]decimal.Decimal{}
	if len(p.DepartmentShares) == 0 {
		groups[scGroupAll] = res.Distributable
	} else {
		var codes []string
		for c := range p.DepartmentShares {
			codes = append(codes, c)
		}
		sort.Strings(codes)
		acc := decimal.Zero
		for _, c := range codes {
			amt := pct(res.Distributable, p.DepartmentShares[c]).Round(scDisplayPlaces)
			if acc.Add(amt).GreaterThan(res.Distributable) {
				amt = res.Distributable.Sub(acc)
			}
			groups[c] = amt
			acc = acc.Add(amt)
		}
		res.Undistributed = res.Undistributed.Add(res.Distributable.Sub(acc))
	}

	shares := make([]ServiceChargeShare, len(cands))
	members := map[string][]int{}
	for i, c := range cands {
		s := ServiceChargeShare{EmployeeID: c.EmployeeID, Points: decimal.Zero, Factor: decimal.Zero, BaseShare: decimal.Zero,
			AttendanceAmount: decimal.Zero, Redistributed: decimal.Zero, Amount: decimal.Zero}
		s.Reason = p.eligibility(c)
		if s.Reason == "" {
			s.Group = scGroupAll
			if len(p.DepartmentShares) > 0 {
				s.Group = ""
				for _, code := range c.OrgUnitCodes {
					if _, ok := p.DepartmentShares[code]; ok {
						s.Group = code
						break
					}
				}
				if s.Group == "" {
					s.Reason = SCReasonNoDepartment
				}
			}
		}
		if s.Reason == "" {
			s.Points = decimal.NewFromInt(1)
			if p.Method == "points" {
				s.Points = decimal.NewFromInt(1)
				if v, ok := p.GradePoints[c.GradeCode]; ok {
					s.Points = Dec(v)
				}
				if !s.Points.IsPositive() {
					s.Reason, s.Points = SCReasonNoPoints, decimal.Zero
				}
			}
		}
		if s.Reason == "" {
			s.Eligible = true
			s.Factor = p.attendance(c)
			members[s.Group] = append(members[s.Group], i)
		}
		shares[i] = s
	}

	var keys []string
	for g := range groups {
		keys = append(keys, g)
	}
	sort.Strings(keys)
	for _, g := range keys {
		pool := groups[g]
		idx := members[g]
		weights, weighted := decimal.Zero, decimal.Zero
		for _, i := range idx {
			weights = weights.Add(shares[i].Points)
			weighted = weighted.Add(shares[i].Points.Mul(shares[i].Factor))
		}
		if !weights.IsPositive() {
			res.Undistributed = res.Undistributed.Add(pool)
			continue
		}
		forfeited := decimal.Zero
		exact := make(map[int]decimal.Decimal, len(idx))
		for _, i := range idx {
			base := pool.Mul(shares[i].Points).Div(weights)
			att := base.Mul(shares[i].Factor)
			exact[i] = att
			forfeited = forfeited.Add(base.Sub(att))
			shares[i].BaseShare, shares[i].AttendanceAmount = base, att
		}
		undist := decimal.Zero
		if forfeited.IsPositive() {
			if p.RedistributeForfeited && weighted.IsPositive() {
				for _, i := range idx {
					r := forfeited.Mul(shares[i].Points).Mul(shares[i].Factor).Div(weighted)
					shares[i].Redistributed = r
					exact[i] = exact[i].Add(r)
				}
			} else {
				undist = forfeited.Round(scDisplayPlaces)
			}
		}
		paid := decimal.Zero
		for _, i := range idx {
			shares[i].Amount = RoundDown(exact[i], unit)
			paid = paid.Add(shares[i].Amount)
			shares[i].BaseShare = shares[i].BaseShare.Round(scDisplayPlaces)
			shares[i].AttendanceAmount = shares[i].AttendanceAmount.Round(scDisplayPlaces)
			shares[i].Redistributed = shares[i].Redistributed.Round(scDisplayPlaces)
			res.Eligible++
		}
		if paid.Add(undist).GreaterThan(pool) { // a group without redistribution: rounding of the forfeited part
			undist = pool.Sub(paid)
		}
		res.Distributed = res.Distributed.Add(paid)
		res.Undistributed = res.Undistributed.Add(undist)
		res.RoundingDifference = res.RoundingDifference.Add(pool.Sub(paid).Sub(undist))
	}
	res.Shares = shares
	return res
}

// ── partner payouts (EP-13, EP-14) ───────────────────────────────────────

// PartnerPayoutInput is one partner of a payout run.
type PartnerPayoutInput struct {
	Gross            decimal.Decimal // settlements (caddy fee + non-cash tips) / approved instructor fees
	SourceDeductions decimal.Decimal // deductions already applied by the settlements (golf Caddy Policies)
	YTDTaxBase       decimal.Decimal // taxable base of earlier payouts in the calendar year (cumulative method)
	HasTaxID         bool            // NPWP or NIK registered (else the non-NPWP surcharge)
	BPUEnrolled      bool            // registered for BPJS Ketenagakerjaan BPU
	DeclaredIncome   decimal.Decimal // BPU declared income (0 = the gross of the run)
	MonthlyDue       bool            // first run of the month paying the partner: BPU and monthly deductions are due
}

// PartnerDeductionAmount is a deduction applied to a line.
type PartnerDeductionAmount struct {
	Code   string `json:"code"`
	Label  string `json:"label"`
	Amount string `json:"amount"`
}

// PartnerPayoutAmounts is the calculated payout line.
type PartnerPayoutAmounts struct {
	Gross            decimal.Decimal
	SourceDeductions decimal.Decimal
	TaxBase          decimal.Decimal // DPP: taxable share × gross
	PPh21Rate        decimal.Decimal // effective rate on the taxable base, %
	PPh21            decimal.Decimal
	BPUJKK           decimal.Decimal
	BPUJKM           decimal.Decimal
	OtherDeductions  decimal.Decimal
	Deductions       []PartnerDeductionAmount
	Net              decimal.Decimal
}

// PartnerPPh21 is PPh 21 non-employee (bukan pegawai, PMK 168/2023): the
// Article 17 rates on the taxable base (taxable share, 50%, of the gross);
// cumulative_annual applies the brackets to the year's cumulative base;
// without NPWP / NIK the tax is raised by the non-NPWP surcharge. Rounded
// down to the rounding unit.
func (c PayrollConfiguration) PartnerPPh21(method string, gross, ytdBase decimal.Decimal, hasTaxID bool) (base, tax, rate decimal.Decimal) {
	unit := Dec(c.RoundingUnit)
	base = RoundDown(pct(gross, c.PPh21.NonEmployeeTaxableSharePercent), decimal.NewFromInt(1))
	if !base.IsPositive() {
		return decimal.Zero, decimal.Zero, decimal.Zero
	}
	if method == "cumulative_annual" {
		tax = ProgressiveTax(c.PPh21.ProgressiveRates, ytdBase.Add(base)).Sub(ProgressiveTax(c.PPh21.ProgressiveRates, ytdBase))
	} else {
		tax = ProgressiveTax(c.PPh21.ProgressiveRates, base)
	}
	if !hasTaxID {
		tax = tax.Add(pct(tax, c.PPh21.NonNPWPSurchargePercent))
	}
	tax = RoundDown(tax, unit)
	rate = tax.Mul(hundred).Div(base).Round(4)
	return base, tax, rate
}

// PartnerBPU is the monthly BPJS Ketenagakerjaan BPU contribution: JKK %
// of the declared income and the fixed JKM.
func (c PayrollConfiguration) PartnerBPU(declared decimal.Decimal) (jkk, jkm decimal.Decimal) {
	unit := Dec(c.RoundingUnit)
	return RoundNearest(pct(declared, c.BPJS.Partner.JKKPercent), unit), RoundNearest(Dec(c.BPJS.Partner.JKMAmount), unit)
}

// CalculatePartnerPayout calculates a payout line: gross − settlement
// deductions − PPh 21 − BPU − deductions of the policy = net. Deductions
// never take the net below zero (BPU, then the other deductions are
// capped).
func CalculatePartnerPayout(c PayrollConfiguration, r PartnerPayoutRule, in PartnerPayoutInput) PartnerPayoutAmounts {
	unit := Dec(c.RoundingUnit)
	out := PartnerPayoutAmounts{Gross: in.Gross, SourceDeductions: in.SourceDeductions, TaxBase: decimal.Zero, PPh21Rate: decimal.Zero,
		PPh21: decimal.Zero, BPUJKK: decimal.Zero, BPUJKM: decimal.Zero, OtherDeductions: decimal.Zero, Deductions: []PartnerDeductionAmount{}}
	if r.WithholdPPh21 {
		out.TaxBase, out.PPh21, out.PPh21Rate = c.PartnerPPh21(r.TaxMethod, in.Gross, in.YTDTaxBase, in.HasTaxID)
	}
	left := in.Gross.Sub(in.SourceDeductions).Sub(out.PPh21)
	if r.DeductBPU && in.BPUEnrolled && in.MonthlyDue && left.IsPositive() {
		declared := in.DeclaredIncome
		if !declared.IsPositive() {
			declared = in.Gross
		}
		out.BPUJKK, out.BPUJKM = c.PartnerBPU(declared)
		if out.BPUJKK.GreaterThan(left) {
			out.BPUJKK = left
		}
		if out.BPUJKM.GreaterThan(left.Sub(out.BPUJKK)) {
			out.BPUJKM = left.Sub(out.BPUJKK)
		}
		left = left.Sub(out.BPUJKK).Sub(out.BPUJKM)
	}
	for _, d := range r.Deductions {
		if d.PerMonth && !in.MonthlyDue {
			continue
		}
		amt := Dec(d.Amount)
		if d.Kind == "percent" {
			amt = RoundNearest(pct(in.Gross, d.Amount), unit)
		}
		if amt.GreaterThan(left) {
			amt = decimal.Max(left, decimal.Zero)
		}
		if !amt.IsPositive() {
			continue
		}
		out.Deductions = append(out.Deductions, PartnerDeductionAmount{Code: d.Code, Label: d.Label, Amount: amt.String()})
		out.OtherDeductions = out.OtherDeductions.Add(amt)
		left = left.Sub(amt)
	}
	out.Net = decimal.Max(left, decimal.Zero)
	return out
}

// BPU is the total BPJS BPU of a line.
func (a PartnerPayoutAmounts) BPU() decimal.Decimal { return a.BPUJKK.Add(a.BPUJKM) }

// ── commission payout (EP-12) ────────────────────────────────────────────

// CommissionSplit splits an approved commission statement into the payroll
// earning (earned + positive adjustments) and the deduction (clawbacks of
// earlier deals and negative adjustments, FR-CMS-HR-02); earning −
// deduction = the statement total.
func CommissionSplit(earned, clawback, adjustments decimal.Decimal) (earning, deduction decimal.Decimal) {
	earning = earned.Abs()
	deduction = clawback.Abs()
	if adjustments.IsNegative() {
		deduction = deduction.Add(adjustments.Neg())
	} else {
		earning = earning.Add(adjustments)
	}
	return earning, deduction
}

// ── partner payout periods (§16 #4) ──────────────────────────────────────

func monthEnd(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, time.UTC)
}

// PartnerPeriod returns the payout period containing a date: semi_monthly
// 1–15 and 16–month end, monthly the calendar month.
func PartnerPeriod(schedule string, day time.Time) (from, to time.Time) {
	d := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	first := time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC)
	if schedule != "semi_monthly" {
		return first, monthEnd(d)
	}
	if d.Day() <= 15 {
		return first, first.AddDate(0, 0, 14)
	}
	return first.AddDate(0, 0, 15), monthEnd(d)
}

// PreviousPartnerPeriod is the last period that ended before a date.
func PreviousPartnerPeriod(schedule string, day time.Time) (from, to time.Time) {
	f, _ := PartnerPeriod(schedule, day)
	return PartnerPeriod(schedule, f.AddDate(0, 0, -1))
}

// ValidPartnerPeriod reports whether from–to is one period of the schedule.
func ValidPartnerPeriod(schedule string, from, to time.Time) bool {
	f, t := PartnerPeriod(schedule, from)
	return f.Equal(from) && t.Equal(to)
}
