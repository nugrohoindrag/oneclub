package hris

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func eqDec(t *testing.T, what string, got decimal.Decimal, want string) {
	t.Helper()
	if !got.Equal(d(want)) {
		t.Errorf("%s = %s, want %s", what, got, want)
	}
}

func scCand(status string, present, scheduled int) ServiceChargeCandidate {
	return ServiceChargeCandidate{EmployeeID: uuid.New(), OrgUnitCodes: []string{"FNB-REST", "FNB"}, GradeCode: "G1", EmploymentStatus: status,
		WorkerCategory: "regular", EmployedInPeriod: true, ScheduledDays: scheduled, PresentDays: decimal.NewFromInt(int64(present)),
		UnpaidLeaveDays: decimal.Zero}
}

// checkBalanced: collected = reserve + distributed + undistributed + rounding,
// and the distributed total is the sum of the lines.
func checkBalanced(t *testing.T, r ServiceChargeResult) {
	t.Helper()
	sum := decimal.Zero
	for _, s := range r.Shares {
		sum = sum.Add(s.Amount)
	}
	if !sum.Equal(r.Distributed) {
		t.Errorf("lines %s ≠ distributed %s", sum, r.Distributed)
	}
	if tot := r.Reserve.Add(r.Distributed).Add(r.Undistributed).Add(r.RoundingDifference); !tot.Equal(r.Collected) {
		t.Errorf("reserve %s + distributed %s + undistributed %s + rounding %s = %s ≠ pool %s", r.Reserve, r.Distributed, r.Undistributed,
			r.RoundingDifference, tot, r.Collected)
	}
	if r.RoundingDifference.IsNegative() || r.Undistributed.IsNegative() {
		t.Errorf("negative rounding / undistributed: %s / %s", r.RoundingDifference, r.Undistributed)
	}
}

// PRD P5 EP-11 AC: pool Rp100,000,000 shared equally by 50 eligible
// employees with equal points = Rp2,000,000 each; one present 20 of 25
// working days receives 80% of the share and the rest is redistributed by
// the rule.
func TestServiceChargeAcceptance(t *testing.T) {
	p := NewServiceChargePolicy()
	p.DistributedPercent, p.ReservePercent = "100", "0" // the AC pool is the distributable amount
	var cands []ServiceChargeCandidate
	for range 50 {
		cands = append(cands, scCand(StatusPermanent, 25, 25))
	}
	r := DistributeServiceCharge(p, d("100000000"), cands)
	checkBalanced(t, r)
	if r.Eligible != 50 {
		t.Fatalf("eligible %d", r.Eligible)
	}
	for _, s := range r.Shares {
		eqDec(t, "equal share", s.Amount, "2000000")
	}
	eqDec(t, "distributed", r.Distributed, "100000000")

	cands[7].PresentDays = decimal.NewFromInt(20)
	r = DistributeServiceCharge(p, d("100000000"), cands)
	checkBalanced(t, r)
	low := r.Shares[7]
	eqDec(t, "attendance factor", low.Factor, "0.8")
	eqDec(t, "base share", low.BaseShare, "2000000")
	eqDec(t, "80% of the share", low.AttendanceAmount, "1600000")
	// forfeited 400,000 redistributed ∝ factor: 400,000 × 0.8 / 49.8 and 400,000 / 49.8
	eqDec(t, "redistributed (80%)", low.Redistributed, "6425.70")
	eqDec(t, "paid (80%)", low.Amount, "1606425")
	eqDec(t, "paid (full)", r.Shares[0].Amount, "2008032")
	eqDec(t, "redistributed (full)", r.Shares[0].Redistributed, "8032.13")
	eqDec(t, "distributed", r.Distributed, "99999993")
	eqDec(t, "rounding", r.RoundingDifference, "7")

	p.RedistributeForfeited = false
	r = DistributeServiceCharge(p, d("100000000"), cands)
	checkBalanced(t, r)
	eqDec(t, "paid without redistribution", r.Shares[7].Amount, "1600000")
	eqDec(t, "full share", r.Shares[0].Amount, "2000000")
	eqDec(t, "forfeited kept undistributed", r.Undistributed, "400000")
}

// §16 #5: 95% distributed, 5% reserve; permanent & contract employees past
// probation are eligible; probation, daily workers, SP2 or worse and unpaid
// leave the whole period are not.
func TestServiceChargeEligibilityAndReserve(t *testing.T) {
	p := NewServiceChargePolicy()
	perm := scCand(StatusPermanent, 22, 22)
	contract := scCand(StatusContract, 22, 22)
	sp1 := scCand(StatusPermanent, 22, 22)
	sp1.WarningLevel = 1
	probation := scCand(StatusProbation, 22, 22)
	probationEnd := scCand(StatusPermanent, 22, 22) // PKWTT still within its probation months
	probationEnd.InProbation = true
	daily := scCand(StatusContract, 22, 22)
	daily.WorkerCategory = "daily"
	sp2 := scCand(StatusPermanent, 22, 22)
	sp2.WarningLevel = 2
	sp3 := scCand(StatusContract, 22, 22)
	sp3.WarningLevel = 3
	unpaid := scCand(StatusPermanent, 0, 22)
	unpaid.UnpaidLeaveDays = decimal.NewFromInt(22)
	partUnpaid := scCand(StatusPermanent, 11, 22)
	partUnpaid.UnpaidLeaveDays = decimal.NewFromInt(11)
	resigned := scCand(StatusResigned, 5, 22)
	gone := scCand(StatusPermanent, 0, 0)
	gone.EmployedInPeriod = false
	cands := []ServiceChargeCandidate{perm, contract, sp1, probation, probationEnd, daily, sp2, sp3, unpaid, partUnpaid, resigned, gone}
	r := DistributeServiceCharge(p, d("10000000"), cands)
	checkBalanced(t, r)
	eqDec(t, "reserve 5%", r.Reserve, "500000")
	eqDec(t, "distributable 95%", r.Distributable, "9500000")
	want := []string{"", "", "", SCReasonProbation, SCReasonProbation, SCReasonCategory, SCReasonWarning, SCReasonWarning, SCReasonUnpaidLeave, "",
		SCReasonStatus, SCReasonNotInPeriod}
	for i, s := range r.Shares {
		if s.Reason != want[i] || s.Eligible != (want[i] == "") {
			t.Errorf("candidate %d: reason %q eligible %v, want %q", i, s.Reason, s.Eligible, want[i])
		}
		if !s.Eligible && !s.Amount.IsZero() {
			t.Errorf("candidate %d excluded but paid %s", i, s.Amount)
		}
	}
	// 4 eligible, weights × factors: 1, 1, 1, 0.5 → 9,500,000 × f / 3.5
	eqDec(t, "full attendance", r.Shares[0].Amount, "2714285")
	eqDec(t, "half the period on unpaid leave", r.Shares[9].Amount, "1357142")
	eqDec(t, "distributed", r.Distributed, "9499997")
	eqDec(t, "rounding", r.RoundingDifference, "3")
	if r.Eligible != 4 {
		t.Errorf("eligible %d", r.Eligible)
	}
}

// FR-SVC-02: department shares and grade points; employees outside the
// shared departments and grades without points.
func TestServiceChargeDepartmentsAndPoints(t *testing.T) {
	p := NewServiceChargePolicy()
	p.DistributedPercent, p.ReservePercent = "100", "0"
	p.Method = "points"
	p.DepartmentShares = map[string]string{"FNB": "60", "GOLF": "30"} // 10% stays undistributed
	p.GradePoints = map[string]string{"G1": "1", "G3": "2", "G9": "0"}
	a := scCand(StatusPermanent, 20, 20) // FNB G1
	b := scCand(StatusPermanent, 20, 20) // FNB G3
	b.GradeCode = "G3"
	c := scCand(StatusPermanent, 20, 20) // GOLF G1
	c.OrgUnitCodes = []string{"GOLF"}
	x := scCand(StatusPermanent, 20, 20) // engineering: no share
	x.OrgUnitCodes = []string{"ENG"}
	z := scCand(StatusPermanent, 20, 20) // grade without points
	z.GradeCode = "G9"
	r := DistributeServiceCharge(p, d("1000000"), []ServiceChargeCandidate{a, b, c, x, z})
	checkBalanced(t, r)
	eqDec(t, "FNB G1 (1 of 3 points of 600,000)", r.Shares[0].Amount, "200000")
	eqDec(t, "FNB G3 (2 of 3 points)", r.Shares[1].Amount, "400000")
	eqDec(t, "GOLF alone", r.Shares[2].Amount, "300000")
	if r.Shares[3].Reason != SCReasonNoDepartment || r.Shares[4].Reason != SCReasonNoPoints {
		t.Errorf("reasons %q %q", r.Shares[3].Reason, r.Shares[4].Reason)
	}
	if r.Shares[0].Group != "FNB" || r.Shares[2].Group != "GOLF" {
		t.Errorf("groups %q %q", r.Shares[0].Group, r.Shares[2].Group)
	}
	eqDec(t, "10% of the pool without a department share", r.Undistributed, "100000")

	// a department share without any eligible employee stays undistributed
	p.DepartmentShares["SPORT"] = "10"
	r = DistributeServiceCharge(p, d("1000000"), []ServiceChargeCandidate{a, b, c})
	checkBalanced(t, r)
	eqDec(t, "SPORT share undistributed", r.Undistributed, "100000")
}

// PRD P5 §16 #4 / FR-TAX-HR-02: PPh 21 non-employee = Article 17 rates ×
// 50% of the gross; non-NPWP surcharge 20%; cumulative variant.
func TestPartnerPPh21(t *testing.T) {
	c := NewPayrollConfiguration()
	base, tax, rate := c.PartnerPPh21("per_payment", d("6400000"), decimal.Zero, true)
	eqDec(t, "taxable base 50%", base, "3200000")
	eqDec(t, "5% bracket", tax, "160000")
	eqDec(t, "effective rate", rate, "5")
	_, tax, _ = c.PartnerPPh21("per_payment", d("6400000"), decimal.Zero, false)
	eqDec(t, "without NPWP +20%", tax, "192000")
	// 200,000,000 → base 100,000,000: 60M × 5% + 40M × 15%
	_, tax, rate = c.PartnerPPh21("per_payment", d("200000000"), decimal.Zero, true)
	eqDec(t, "two brackets", tax, "9000000")
	eqDec(t, "effective rate", rate, "9")
	// cumulative: 58,000,000 before + 3,200,000 → 2,000,000 × 5% + 1,200,000 × 15%
	_, tax, _ = c.PartnerPPh21("cumulative_annual", d("6400000"), d("58000000"), true)
	eqDec(t, "crossing a bracket in the year", tax, "280000")
	_, tax, _ = c.PartnerPPh21("cumulative_annual", d("6400000"), decimal.Zero, true)
	eqDec(t, "first payment of the year", tax, "160000")
	// odd amounts: base rounded down to the rupiah, tax rounded down
	base, tax, _ = c.PartnerPPh21("per_payment", d("1234567"), decimal.Zero, true)
	eqDec(t, "base", base, "617283")
	eqDec(t, "tax 5% of 617,283 = 30,864.15", tax, "30864")
	base, tax, _ = c.PartnerPPh21("per_payment", decimal.Zero, decimal.Zero, true)
	if !base.IsZero() || !tax.IsZero() {
		t.Errorf("zero gross: %s %s", base, tax)
	}
	c.PPh21.NonEmployeeTaxableSharePercent = "100" // a changed regulation version
	_, tax, _ = c.PartnerPPh21("per_payment", d("6400000"), decimal.Zero, true)
	eqDec(t, "configured taxable share", tax, "320000")
}

// EP-13 AC: settlement Rp6,000,000 + non-cash tips Rp400,000 = statement
// Rp6,400,000 before tax and deductions; PPh 21, BPJS BPU (JKK 1% + JKM
// Rp6,800) and the deductions of the Caddy Policies make the net.
func TestPartnerPayoutCaddy(t *testing.T) {
	c := NewPayrollConfiguration()
	r := NewPartnerPayoutPolicy().Caddy
	in := PartnerPayoutInput{Gross: d("6000000").Add(d("400000")), SourceDeductions: decimal.Zero, HasTaxID: true, BPUEnrolled: true, MonthlyDue: true}
	a := CalculatePartnerPayout(c, r, in)
	eqDec(t, "gross", a.Gross, "6400000")
	eqDec(t, "PPh 21", a.PPh21, "160000")
	eqDec(t, "JKK 1%", a.BPUJKK, "64000")
	eqDec(t, "JKM", a.BPUJKM, "6800")
	eqDec(t, "BPU", a.BPU(), "70800")
	eqDec(t, "net", a.Net, "6169200")

	// second run of the month: BPU and monthly deductions already taken
	r.Deductions = []PartnerDeduction{{Code: "UNIFORM", Label: "Uniform", Kind: "fixed", Amount: "50000", PerMonth: true},
		{Code: "COOP", Label: "Cooperative", Kind: "percent", Amount: "1"}}
	in.MonthlyDue = false
	in.SourceDeductions = d("300000")
	a = CalculatePartnerPayout(c, r, in)
	eqDec(t, "no BPU", a.BPU(), "0")
	eqDec(t, "cooperative 1%", a.OtherDeductions, "64000")
	if len(a.Deductions) != 1 || a.Deductions[0].Code != "COOP" {
		t.Errorf("deductions %v", a.Deductions)
	}
	eqDec(t, "net", a.Net, "5876000") // 6,400,000 − 300,000 − 160,000 − 64,000
	in.MonthlyDue = true
	a = CalculatePartnerPayout(c, r, in)
	eqDec(t, "monthly: uniform + cooperative", a.OtherDeductions, "114000")
	eqDec(t, "net", a.Net, "5755200")

	// not enrolled in BPU, no tax withheld
	r.WithholdPPh21, r.Deductions = false, nil
	in.BPUEnrolled = false
	a = CalculatePartnerPayout(c, r, in)
	eqDec(t, "net = gross − settlement deductions", a.Net, "6100000")

	// deductions never take the net below zero
	r = NewPartnerPayoutPolicy().Caddy
	r.Deductions = []PartnerDeduction{{Code: "LOAN", Label: "Loan", Kind: "fixed", Amount: "500000"}}
	a = CalculatePartnerPayout(c, r, PartnerPayoutInput{Gross: d("200000"), HasTaxID: true, BPUEnrolled: true, MonthlyDue: true})
	eqDec(t, "PPh 21 of 100,000", a.PPh21, "5000")
	eqDec(t, "JKK", a.BPUJKK, "2000")
	eqDec(t, "loan capped", a.OtherDeductions, "186200")
	eqDec(t, "net", a.Net, "0")
	// declared BPU income
	a = CalculatePartnerPayout(c, NewPartnerPayoutPolicy().Caddy, PartnerPayoutInput{Gross: d("6400000"), HasTaxID: true, BPUEnrolled: true,
		MonthlyDue: true, DeclaredIncome: d("5000000")})
	eqDec(t, "JKK of the declared income", a.BPUJKK, "50000")
}

// EP-14 AC: 8 sessions × Rp150,000 = statement Rp1,200,000 before tax.
func TestPartnerPayoutInstructor(t *testing.T) {
	c := NewPayrollConfiguration()
	a := CalculatePartnerPayout(c, NewPartnerPayoutPolicy().Instructor, PartnerPayoutInput{Gross: d("150000").Mul(decimal.NewFromInt(8)), HasTaxID: true})
	eqDec(t, "gross", a.Gross, "1200000")
	eqDec(t, "taxable base", a.TaxBase, "600000")
	eqDec(t, "PPh 21", a.PPh21, "30000")
	eqDec(t, "net (no BPU enrolment)", a.Net, "1170000")
}

func TestCommissionSplit(t *testing.T) {
	e, dd := CommissionSplit(d("1500000"), d("-250000"), d("100000"))
	eqDec(t, "earning", e, "1600000")
	eqDec(t, "clawback deduction", dd, "250000")
	e, dd = CommissionSplit(d("0"), d("-300000"), d("-20000"))
	eqDec(t, "earning", e, "0")
	eqDec(t, "deduction", dd, "320000")
	if !e.Sub(dd).Equal(d("-320000")) {
		t.Errorf("net = statement total")
	}
}

func TestPartnerPeriods(t *testing.T) {
	day := func(s string) time.Time { v, _ := time.Parse("2006-01-02", s); return v }
	for _, c := range []struct{ schedule, on, from, to, prevFrom, prevTo string }{
		{"semi_monthly", "2026-10-05", "2026-10-01", "2026-10-15", "2026-09-16", "2026-09-30"},
		{"semi_monthly", "2026-10-15", "2026-10-01", "2026-10-15", "2026-09-16", "2026-09-30"},
		{"semi_monthly", "2026-10-16", "2026-10-16", "2026-10-31", "2026-10-01", "2026-10-15"},
		{"semi_monthly", "2028-02-29", "2028-02-16", "2028-02-29", "2028-02-01", "2028-02-15"},
		{"monthly", "2026-10-05", "2026-10-01", "2026-10-31", "2026-09-01", "2026-09-30"},
		{"monthly", "2026-01-31", "2026-01-01", "2026-01-31", "2025-12-01", "2025-12-31"},
	} {
		f, to := PartnerPeriod(c.schedule, day(c.on))
		pf, pt := PreviousPartnerPeriod(c.schedule, day(c.on))
		if f.Format(time.DateOnly) != c.from || to.Format(time.DateOnly) != c.to || pf.Format(time.DateOnly) != c.prevFrom ||
			pt.Format(time.DateOnly) != c.prevTo {
			t.Errorf("%s %s: %s–%s prev %s–%s", c.schedule, c.on, f, to, pf, pt)
		}
		if !ValidPartnerPeriod(c.schedule, f, to) {
			t.Errorf("%s %s–%s not valid", c.schedule, f, to)
		}
	}
	if ValidPartnerPeriod("semi_monthly", day("2026-10-01"), day("2026-10-31")) || ValidPartnerPeriod("monthly", day("2026-10-02"), day("2026-10-31")) {
		t.Error("misaligned periods accepted")
	}
}

func TestRounding(t *testing.T) {
	eqDec(t, "down to 100", RoundDown(d("12345.67"), d("100")), "12300")
	eqDec(t, "down to 1", RoundDown(d("12345.67"), d("1")), "12345")
	eqDec(t, "nearest 100", RoundNearest(d("12350"), d("100")), "12400")
	eqDec(t, "no unit", RoundDown(d("1.239"), decimal.Zero), "1.23")
}
