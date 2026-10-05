package reporting

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

func TestReportPDF(t *testing.T) {
	rep := &Report{Code: "accounting.trial_balance", Name: "Trial Balance", Columns: []Column{{Key: "account", Label: "Account", Type: "string"},
		{Key: "closing", Label: "Closing", Type: "number"}, {Key: "date", Label: "Date", Type: "datetime"}},
		Params: []Param{{Key: "from", Label: "From", Type: "date"}}}
	rows := make([]map[string]any, 0, 150)
	for i := 0; i < 150; i++ {
		rows = append(rows, map[string]any{"account": fmt.Sprintf("11%02d (Kas & Bank) a very long account name that does not fit the column", i),
			"closing": "-1234567.5", "date": time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)})
	}
	b := reportPDF(rep, map[string]string{"from": "2026-10-01"}, rows, time.Now())
	for _, want := range []string{"%PDF-1.4", "(Trial Balance)", "(-1,234,567.5)", "(2026-10-01)", "From: 2026-10-01", "(Trial Balance \\(continued\\))", "%%EOF"} {
		if !bytes.Contains(b, []byte(want)) {
			t.Errorf("PDF misses %q", want)
		}
	}
	if empty := reportPDF(rep, nil, nil, time.Now()); !bytes.Contains(empty, []byte("No rows")) {
		t.Error("empty report")
	}
}

func TestGroupDigits(t *testing.T) {
	for in, want := range map[string]string{"0": "0", "999": "999", "1000": "1,000", "-1234567.50": "-1,234,567.50", "12345678": "12,345,678", "n/a": "n/a"} {
		if got := groupDigits(in); got != want {
			t.Errorf("groupDigits(%q) = %q, want %q", in, got, want)
		}
	}
	if got := clip("abcdefghij", 6); got != "abc..." {
		t.Errorf("clip: %q", got)
	}
}
