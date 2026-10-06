package payroll

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

// FR-PPY-02 / FR-INT-P5-04: the generic CSV and the BCA fixed-width layout.
func TestRenderBankFile(t *testing.T) {
	p := NewPayrollProcessing()
	meta := BankFileMeta{Currency: "IDR", Reference: "PAY-2026-00012", Remark: "GAJI 2026-07", CompanyCode: "ONECLUB", DebitAccount: "1234567890",
		PaymentDate: day("2026-07-25")}
	ts := []BankTransfer{{Sequence: 1, EmployeeNo: "EMP-00001", FullName: "Ayu", BankCode: "BCA", AccountNo: "5270012345", AccountName: "Ayu Lestari",
		Amount: decimal.RequireFromString("10199110")}, {Sequence: 2, EmployeeNo: "EMP-00002", FullName: "Citra", BankCode: "BCA", AccountNo: "123",
		AccountName: "Citra", Amount: decimal.RequireFromString("2952000.50")}}
	gen, _ := p.layout("")
	out, err := RenderBankFile(gen, meta, ts)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 3 || lines[1] != "1,EMP-00001,Ayu,BCA,5270012345,Ayu Lestari,10199110,IDR,2026-07-25,PAY-2026-00012" {
		t.Fatalf("generic CSV: %q", lines)
	}
	bca, ok := p.layout("bca_payroll")
	if !ok {
		t.Fatal("bca layout")
	}
	out, err = RenderBankFile(bca, meta, ts)
	if err != nil {
		t.Fatal(err)
	}
	rec := strings.Split(strings.TrimSuffix(string(out), "\r\n"), "\r\n")
	if len(rec) != 4 {
		t.Fatalf("records: %q", rec)
	}
	if want := "0ONECLUB   1234567890202607250000200000001315111050PAY-2026-00012      "; rec[0] != want {
		t.Fatalf("header\n%q\n%q", rec[0], want)
	}
	if !strings.HasPrefix(rec[1], "15270012345000001019911000EMP-00001 Ayu Lestari") || len(rec[1]) != 84 {
		t.Fatalf("detail %q (%d)", rec[1], len(rec[1]))
	}
	if !strings.HasPrefix(rec[2], "10000000123000000295200050") {
		t.Fatalf("zero-padded account and cents: %q", rec[2])
	}
	if rec[3] != "90000200000001315111050" {
		t.Fatalf("trailer %q", rec[3])
	}
}

func TestPeriodBounds(t *testing.T) {
	from, to := periodBounds(day("2026-02-01"), 1)
	if from != day("2026-02-01") || to != day("2026-02-28") {
		t.Fatalf("calendar month: %v %v", from, to)
	}
	from, to = periodBounds(day("2026-01-01"), 21)
	if from != day("2025-12-21") || to != day("2026-01-20") {
		t.Fatalf("21st to 20th: %v %v", from, to)
	}
	if monthsWorked(nil, 2026, 7) != 7 {
		t.Fatal("full year")
	}
	j := day("2026-04-16")
	if monthsWorked(&j, 2026, 12) != 9 {
		t.Fatal("joiner of April")
	}
}
