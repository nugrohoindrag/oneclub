package hris

// Worked examples of the payroll engine (PRD P5 EP-09 / EP-10, DoD §13.2:
// table of test cases incl. limits and rounding). Every amount is exact to
// the rupiah; the cases are the basis of the tax consultant's review
// (FR-TAX-HR-05).

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func ymdT(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func basicItem(amount string) PayItem {
	return PayItem{Code: "BASIC", Name: "Basic Salary", Kind: LineEarning, Category: CatBasic, Taxable: true, Fixed: true, Prorate: true, Amount: d(amount),
		Source: "contract"}
}

func line(t *testing.T, r PayResult, code string) PayLine {
	t.Helper()
	for _, l := range r.Lines {
		if l.Code == code {
			return l
		}
	}
	t.Fatalf("no line %s in %+v", code, r.Lines)
	return PayLine{}
}

func eq(t *testing.T, what string, got decimal.Decimal, want string) {
	t.Helper()
	if !got.Equal(d(want)) {
		t.Errorf("%s = %s, want %s", what, got, want)
	}
}

func october(items ...PayItem) PayInput {
	return PayInput{PeriodStart: ymdT("2026-10-01"), PeriodEnd: ymdT("2026-10-31"), WorkWeekDays: 5, ProrationBasis: "calendar_days",
		HourlyDivisor: d("173"), RoundingUnit: d("1"), Items: items, Rates: DefaultStatutoryRates(), PTKP: "TK/0", HasTaxID: true, Tax: true,
		MonthsWorked: 10}
}

// Rp10,000,000 TK/0 in October: BPJS on the full wage (JP below its cap),
// taxable gross incl. the employer Kesehatan / JKK / JKM shares, TER A 2.5%.
func TestPayrollMonthlyTER(t *testing.T) {
	in := october(basicItem("10000000"))
	in.BPJS = true
	r := Calculate(in)
	eq(t, "kesehatan ee", line(t, r, "BPJS_KESEHATAN_EE").Amount, "100000")
	eq(t, "kesehatan er", line(t, r, "BPJS_KESEHATAN_ER").Amount, "400000")
	eq(t, "jht ee", line(t, r, "BPJS_JHT_EE").Amount, "200000")
	eq(t, "jht er", line(t, r, "BPJS_JHT_ER").Amount, "370000")
	eq(t, "jp ee", line(t, r, "BPJS_JP_EE").Amount, "100000")
	eq(t, "jp er", line(t, r, "BPJS_JP_ER").Amount, "200000")
	eq(t, "jkk er", line(t, r, "BPJS_JKK_ER").Amount, "24000")
	eq(t, "jkm er", line(t, r, "BPJS_JKM_ER").Amount, "30000")
	eq(t, "taxable gross", r.TaxableGross, "10454000")
	eq(t, "TER rate", r.TERRate, "2.5")
	eq(t, "PPh 21", r.PPh21, "261350")
	eq(t, "net", r.Net, "9338650")
	eq(t, "employer cost", r.EmployerCost, "11024000")
	eq(t, "deductible", r.Deductible, "300000")
	if r.TaxMethod != "ter" || r.TERCategory != "A" {
		t.Fatalf("method %s / %s", r.TaxMethod, r.TERCategory)
	}
}

// EP-10 AC: Rp6,000,000 → Kesehatan 240,000 / 60,000, JHT 222,000 /
// 120,000, JP 120,000 / 60,000; above the caps (Kesehatan 12 million, JP
// 10,547,400) the capped base applies.
func TestPayrollBPJSCaps(t *testing.T) {
	r := DefaultStatutoryRates()
	got := map[string]Contributions{}
	for _, c := range r.BPJSContributions(d("6000000")) {
		got[c.Programme] = c
	}
	eq(t, "kes er", got[ProgrammeKesehatan].Employer, "240000")
	eq(t, "kes ee", got[ProgrammeKesehatan].Employee, "60000")
	eq(t, "jht er", got[ProgrammeJHT].Employer, "222000")
	eq(t, "jht ee", got[ProgrammeJHT].Employee, "120000")
	eq(t, "jp er", got[ProgrammeJP].Employer, "120000")
	eq(t, "jp ee", got[ProgrammeJP].Employee, "60000")
	for _, c := range r.BPJSContributions(d("15000000")) {
		got[c.Programme] = c
	}
	eq(t, "kes er capped", got[ProgrammeKesehatan].Employer, "480000")
	eq(t, "kes ee capped", got[ProgrammeKesehatan].Employee, "120000")
	eq(t, "kes base", got[ProgrammeKesehatan].Base, "12000000")
	eq(t, "jp er capped", got[ProgrammeJP].Employer, "210948")
	eq(t, "jp ee capped", got[ProgrammeJP].Employee, "105474")
	eq(t, "jht uncapped", got[ProgrammeJHT].Employee, "300000")
	eq(t, "jkk", got[ProgrammeJKK].Employer, "36000")
	eq(t, "jkm", got[ProgrammeJKM].Employer, "45000")
}

// December: annual calculation on the year (Jan–Nov as in
// TestPayrollMonthlyTER): gross 125,448,000 − biaya jabatan 6,000,000 (max)
// − JHT/JP 3,600,000 − PTKP 54,000,000 = PKP 61,848,000 → 3,277,200;
// withheld 11 × 261,350 = 2,874,850 → December 402,350.
func TestPayrollDecemberAnnual(t *testing.T) {
	in := october(basicItem("10000000"))
	in.PeriodStart, in.PeriodEnd = ymdT("2026-12-01"), ymdT("2026-12-31")
	in.BPJS, in.LastPeriod, in.MonthsWorked = true, true, 12
	in.YTDGross, in.YTDDeductible, in.YTDTax = d("114994000"), d("3300000"), d("2874850")
	r := Calculate(in)
	if r.Annual == nil || r.TaxMethod != "annual" {
		t.Fatalf("annual: %+v", r)
	}
	eq(t, "gross year", r.Annual.Gross, "125448000")
	eq(t, "occupational", r.Annual.OccupationalCost, "6000000")
	eq(t, "contributions", r.Annual.Contributions, "3600000")
	eq(t, "net", r.Annual.Net, "115848000")
	eq(t, "PKP", r.Annual.TaxableIncome, "61848000")
	eq(t, "annual tax", r.Annual.AnnualTax, "3277200")
	eq(t, "December PPh 21", r.PPh21, "402350")
	eq(t, "net pay", r.Net, "9197650")
}

// A July joiner (6 months): biaya jabatan max pro rata 3,000,000; the TER
// withheld exceeds the annual tax → December refund (negative PPh 21).
func TestPayrollAnnualRefundPartialYear(t *testing.T) {
	in := october(basicItem("10000000"))
	in.PeriodStart, in.PeriodEnd = ymdT("2026-12-01"), ymdT("2026-12-31")
	in.LastPeriod, in.MonthsWorked = true, 6
	in.YTDGross, in.YTDTax = d("50000000"), d("1000000")
	r := Calculate(in)
	eq(t, "occupational", r.Annual.OccupationalCost, "3000000")
	eq(t, "PKP", r.Annual.TaxableIncome, "3000000")
	eq(t, "annual tax", r.Annual.AnnualTax, "150000")
	eq(t, "refund", r.PPh21, "-850000")
	eq(t, "net", r.Net, "10850000")
}

// THR in its own run of the month: TER on the month's total (regular
// 10,454,000 + THR 10,000,000 = 20,454,000, 9%) less the tax of the
// regular run.
func TestPayrollTERCombinedMonth(t *testing.T) {
	in := october(PayItem{Code: "THR", Name: "THR", Kind: LineEarning, Category: CatTHR, Taxable: true, Irregular: true, Amount: d("10000000"),
		Source: "thr"})
	in.MonthPriorGross, in.MonthPriorTax = d("10454000"), d("261350")
	r := Calculate(in)
	eq(t, "TER rate", r.TERRate, "9")
	eq(t, "PPh 21 of the THR run", r.PPh21, "1579510")
	eq(t, "net", r.Net, "8420490")
}

// EP-09 AC: THR Rp6,000,000 after 6 months = 3,000,000.
func TestPayrollTHRAmount(t *testing.T) {
	eq(t, "THR 6 months", NewPayrollConfiguration().THRAmount(d("6000000"), ServiceMonths(ymdT("2026-03-20"), ymdT("2026-09-20"))), "3000000")
	eq(t, "THR 11 months", NewPayrollConfiguration().THRAmount(d("6000000"), 11), "5500000")
}

// FR-PAY-03: joiner on 16 October = 16/31 (calendar) or 11/22 working days.
func TestPayrollProration(t *testing.T) {
	join := ymdT("2026-10-16")
	in := october(basicItem("6200000"))
	in.Tax = false
	in.JoinDate = &join
	r := Calculate(in)
	eq(t, "calendar factor", r.ProrationFactor.Mul(d("31")).Round(6), "16")
	eq(t, "calendar basic", line(t, r, "BASIC").Amount, "3200000")
	in.ProrationBasis = "working_days"
	r = Calculate(in)
	eq(t, "working days basic", line(t, r, "BASIC").Amount, "3100000")
	// leaver: last day 10 October (leave date = 11 October) → 10/31
	leave := ymdT("2026-10-11")
	in = october(basicItem("6200000"))
	in.Tax, in.LeaveDate = false, &leave
	r = Calculate(in)
	eq(t, "leaver basic", line(t, r, "BASIC").Amount, "2000000")
	if got := PeriodDays("working_days", ymdT("2026-10-01"), ymdT("2026-10-31"), 6); got != 27 {
		t.Fatalf("6-day week working days = %d", got)
	}
}

// Overtime (EP-08 AC): Rp5,190,000 ÷ 173 = 30,000 per hour; 3 hours on a
// workday = 5.5 multiplied hours = Rp165,000. Unpaid leave: 2 days of
// 6,200,000 ÷ 31 = 400,000 (pre-tax).
func TestPayrollOvertimeAndUnpaidLeave(t *testing.T) {
	in := october(basicItem("5190000"))
	in.Tax = false
	in.OvertimeHours, in.OvertimePayableHours = d("5.5"), d("3")
	r := Calculate(in)
	eq(t, "overtime", line(t, r, "OVERTIME").Amount, "165000")
	eq(t, "overtime rate", line(t, r, "OVERTIME").Rate, "30000")
	eq(t, "gross", r.Gross, "5355000")
	in = october(basicItem("6200000"))
	in.Tax = false
	in.UnpaidLeaveDays = d("2")
	in.AbsentDays, in.DeductAbsence = d("1"), true
	r = Calculate(in)
	eq(t, "unpaid leave", line(t, r, "UNPAID_LEAVE").Amount, "400000")
	eq(t, "absence", line(t, r, "ABSENCE").Amount, "200000")
	eq(t, "gross", r.Gross, "5600000")
	eq(t, "net", r.Net, "5600000")
}

// No NPWP / NIK: +20% (Rp10,000,000 TER A 2% = 200,000 → 240,000).
func TestPayrollNonNPWPSurcharge(t *testing.T) {
	in := october(basicItem("10000000"))
	in.HasTaxID = false
	r := Calculate(in)
	eq(t, "PPh 21", r.PPh21, "240000")
}

// PP 68/2009: severance Rp120,000,000 → 0% × 50 M + 5% × 50 M + 15% × 20 M
// = 5,500,000 final tax; not part of the TER base.
func TestPayrollSeveranceFinalTax(t *testing.T) {
	in := october(PayItem{Code: "SEVERANCE", Name: "Severance", Kind: LineEarning, Category: CatSeverance, Taxable: true, Amount: d("120000000"),
		Source: "settlement"})
	r := Calculate(in)
	eq(t, "final tax", r.PPh21Final, "5500000")
	eq(t, "TER base", r.TaxableGross, "0")
	eq(t, "net", r.Net, "114500000")
	eq(t, "second payment", DefaultStatutoryRates().SeveranceTax(d("30000000"), d("120000000"), d("5500000")), "4500000")
}

// Net pay rounding (Payroll Configuration rounding unit 100) and after-tax
// deductions (loan installment).
func TestPayrollRoundingAndDeductions(t *testing.T) {
	in := october(basicItem("10000000"), PayItem{Code: "LOAN", Name: "Loan Installment", Kind: LineDeduction, Category: CatLoan, Amount: d("500000"),
		Source: "loan"})
	in.BPJS, in.RoundingUnit = true, d("100")
	r := Calculate(in)
	eq(t, "rounding", line(t, r, "ROUNDING").Amount, "50")
	eq(t, "net", r.Net, "8838700")
	eq(t, "other deductions", r.OtherDeductions, "500000")
	eq(t, "PPh 21 unchanged by the loan", r.PPh21, "261350")
}
