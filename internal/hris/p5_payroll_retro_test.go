package hris

// Worked example of a retro correction with BPJS differences (PRD P5 EP-09
// FR-PAY-06 retro, EP-10 FR-TAX-HR-03): March 2026 was paid on a basic
// salary of Rp11,000,000 (TK/0); in April a raise to Rp13,000,000 is
// approved with effect from 1 March. The April adjustment run pays the
// difference of the basic salary and of every BPJS contribution of March,
// with the caps of March (Kesehatan Rp12,000,000, JP Rp10,547,400):
//
//	programme   March paid (11 M)    March now (13 M)     difference
//	Kesehatan   110,000 / 440,000    120,000 / 480,000    10,000 / 40,000  (cap 12 M)
//	JHT         220,000 / 407,000    260,000 / 481,000    40,000 / 74,000
//	JP          105,474 / 210,948    105,474 / 210,948    —                (cap 10,547,400 both times)
//	JKK               — / 26,400           — / 31,200      — / 4,800
//	JKM               — / 33,000           — / 39,000      — / 6,000
//	                                                      (employee / employer)
//
// April's regular run (13 M) has a TER base of 13,550,200 (incl. the taxable
// employer Kesehatan 480,000, JKK 31,200, JKM 39,000) at TER A 5% = 677,510.
// The adjustment adds 2,000,000 + the taxable employer differences 50,800 =
// 2,050,800, so April's base becomes 15,601,000 at TER A 7% = 1,092,070:
// PPh 21 of the adjustment 414,560. Net = 2,000,000 − 50,000 − 414,560 =
// 1,535,440; employer cost = 2,000,000 + 124,800.

import (
	"testing"

	"github.com/shopspring/decimal"
)

func monthOf(start, end string, items ...PayItem) PayInput {
	return PayInput{PeriodStart: ymdT(start), PeriodEnd: ymdT(end), WorkWeekDays: 5, ProrationBasis: "calendar_days", HourlyDivisor: d("173"),
		RoundingUnit: d("1"), Items: items, Rates: DefaultStatutoryRates(), PTKP: "TK/0", HasTaxID: true, Tax: true, BPJS: true, MonthsWorked: 3}
}

// paidBPJS keeps the contribution lines of a result by code (what
// hris/payroll reads from the approved runs of the corrected period).
func paidBPJS(r PayResult) map[string]decimal.Decimal {
	out := map[string]decimal.Decimal{}
	for _, l := range r.Lines {
		if l.Kind == LineBPJSEmployee || l.Kind == LineBPJSEmployer {
			out[l.Code] = out[l.Code].Add(l.Amount)
		}
	}
	return out
}

func TestPayrollRetroBPJSDifferences(t *testing.T) {
	march := Calculate(monthOf("2026-03-01", "2026-03-31", basicItem("11000000")))
	eq(t, "March JHT ee", line(t, march, "BPJS_JHT_EE").Amount, "220000")
	eq(t, "March JP ee (cap)", line(t, march, "BPJS_JP_EE").Amount, "105474")

	// the corrected period recalculated with today's data (no tax, as hris/payroll does)
	redo := monthOf("2026-03-01", "2026-03-31", basicItem("13000000"))
	redo.Tax = false
	diffs := BPJSDifferences(Calculate(redo).Lines, paidBPJS(march), "2026-03")
	want := map[string]string{"BPJS_KESEHATAN_EE": "10000", "BPJS_KESEHATAN_ER": "40000", "BPJS_JHT_EE": "40000", "BPJS_JHT_ER": "74000",
		"BPJS_JKK_ER": "4800", "BPJS_JKM_ER": "6000"}
	if len(diffs) != len(want) {
		t.Fatalf("differences: %+v", diffs)
	}
	for _, l := range diffs {
		eq(t, l.Code, l.Amount, want[l.Code])
		if l.Source != "retro" || l.SourceID != "2026-03" || l.Programme == "" || l.Category != CatStatutory {
			t.Errorf("line %+v", l)
		}
	}
	if diffs[0].Code != "BPJS_JHT_EE" || diffs[0].Kind != LineBPJSEmployee || diffs[1].Kind != LineBPJSEmployer {
		t.Errorf("sorted by code with their kind: %+v", diffs[:2])
	}

	// April: the regular run on the new salary, then the adjustment run
	april := Calculate(monthOf("2026-04-01", "2026-04-30", basicItem("13000000")))
	eq(t, "April TER base", april.TaxableGross, "13550200")
	eq(t, "April PPh 21 (TER A 5%)", april.PPh21, "677510")
	adj := monthOf("2026-04-01", "2026-04-30", PayItem{Code: "BASIC", Name: "Basic Salary", Kind: LineEarning, Category: CatBasic, Taxable: true,
		Amount: d("2000000"), Source: "retro", SourceType: "hris.payroll_period", SourceID: "2026-03", Description: "Correction of 2026-03"})
	adj.BPJS = false // a correction run calculates no contributions of its own
	adj.BPJSAdjustments = diffs
	adj.MonthPriorGross, adj.MonthPriorTax, adj.MonthPriorDeductible = april.TaxableGross, april.PPh21, april.Deductible
	r := Calculate(adj)
	eq(t, "gross", r.Gross, "2000000")
	eq(t, "BPJS employee", r.BPJSEmployee, "50000")
	eq(t, "BPJS employer", r.BPJSEmployer, "124800")
	eq(t, "deductible (JHT + JP employee)", r.Deductible, "40000")
	eq(t, "TER base of the adjustment", r.TaxableGross, "2050800")
	eq(t, "TER rate on the month", r.TERRate, "7")
	eq(t, "PPh 21", r.PPh21, "414560")
	eq(t, "net", r.Net, "1535440")
	eq(t, "employer cost", r.EmployerCost, "2124800")
	eq(t, "JHT employer line", line(t, r, "BPJS_JHT_ER").Amount, "74000")

	// a cut in pay refunds contributions; a programme no longer due is refunded in full
	down := monthOf("2026-03-01", "2026-03-31", basicItem("10000000"))
	down.Tax = false
	refund := BPJSDifferences(Calculate(down).Lines, paidBPJS(march), "2026-03")
	for _, l := range refund {
		if !l.Amount.IsNegative() {
			t.Errorf("refund %s = %s", l.Code, l.Amount)
		}
	}
	gone := BPJSDifferences(nil, map[string]decimal.Decimal{"BPJS_JHT_EE": d("220000"), "OVERTIME": d("1")}, "2026-03")
	if len(gone) != 1 || gone[0].Code != "BPJS_JHT_EE" || gone[0].Kind != LineBPJSEmployee || gone[0].Programme != ProgrammeJHT ||
		!gone[0].Amount.Equal(d("-220000")) || gone[0].Name != "BPJS JHT" {
		t.Fatalf("excluded since: %+v", gone)
	}
	// nothing changed: no differences
	if same := BPJSDifferences(march.Lines, paidBPJS(march), "2026-03"); len(same) != 0 {
		t.Fatalf("no change: %+v", same)
	}
}
