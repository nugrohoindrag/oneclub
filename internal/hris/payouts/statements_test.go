package payouts

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"oneclub/internal/hris"
)

func TestWrap(t *testing.T) {
	lines := wrap(hris.PartnerTaxNote, 40)
	if len(lines) < 3 || strings.Join(lines, " ") != hris.PartnerTaxNote {
		t.Fatalf("wrap: %q", lines)
	}
	for _, l := range lines {
		if len(l) > 40 {
			t.Errorf("line too long: %q", l)
		}
	}
}

func TestStatementPDF(t *testing.T) {
	note := hris.PartnerTaxNote
	code := "C001"
	s := PayoutStatement{Number: "PYO-2026-00001", Kind: hris.PayoutKindCaddy, Title: "Payout Statement", PeriodStart: time.Now(), PeriodEnd: time.Now(),
		PayDate: time.Now(), RunStatus: "approved", TaxNote: &note, Line: PayoutRunLine{PartnerName: "Caddy", PartnerCode: &code, Units: 40, Fee: "6000000",
			Tips: "400000", Gross: "6400000", TaxBase: "3200000", PPh21Rate: "5", PPh21: "160000", BPUJKK: "64000", BPUJKM: "6800", Net: "6169200",
			Sources:    []hris.PayoutSourceRef{{Number: "CST-1", Gross: "6400000", Deductions: "0"}},
			Deductions: []hris.PartnerDeductionAmount{{Code: "UNIFORM", Label: "Uniform", Amount: "25000"}}}}
	out := StatementPDF(s)
	if !bytes.HasPrefix(out, []byte("%PDF")) || !bytes.Contains(out, []byte("Rp 6.169.200")) || !bytes.Contains(out, []byte("Uniform")) {
		t.Fatalf("statement PDF: %d bytes", len(out))
	}
	if money(dec("-1234567.6")) != "-Rp 1.234.568" {
		t.Errorf("money: %s", money(dec("-1234567.6")))
	}
}
