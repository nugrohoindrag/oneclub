package dataimport

import (
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	if s, e, err := Resolve("inventory", ""); err != nil || s.Name != "inventory" || e != "items" {
		t.Fatalf("default entity: %v %v %v", s, e, err)
	}
	if _, e, err := Resolve("procurement", "open-purchase-orders"); err != nil || e != "open-purchase-orders" {
		t.Fatalf("named entity: %v %v", e, err)
	}
	if _, _, err := Resolve("inventory", "suppliers"); err == nil || !strings.Contains(err.Error(), "items, categories") {
		t.Fatalf("wrong entity: %v", err)
	}
	if _, _, err := Resolve("payroll", ""); err == nil || !strings.Contains(err.Error(), "sales, banquet") {
		t.Fatalf("unknown scope: %v", err)
	}
}

func TestBanquetRows(t *testing.T) {
	csv := "\uFEFFlegacy_ref,Title,customerName,date,pax,contractTotal,downPayment\n" +
		"BB-1,Wedding,Lina,2027-01-10,250,75000000,22500000\n" +
		"BB-2,Arisan,Sri,2027-01-20,forty,8000000,\n" +
		"BB-3,Meeting,Budi,2027-02-01,,1000000,\n"
	rows, issues, err := banquetRows(csv)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].LegacyRef != "BB-1" || rows[0].Pax != 250 || rows[0].DownPayment != "22500000" || rows[1].Pax != 0 {
		t.Fatalf("rows: %+v", rows)
	}
	if len(issues) != 1 || issues[0].Row != 3 || !strings.Contains(issues[0].Message, "whole number") {
		t.Fatalf("issues: %+v", issues)
	}
	if _, _, err := banquetRows("legacyRef,colour\nX,red\n"); err == nil || !strings.Contains(err.Error(), "colour") {
		t.Fatalf("unknown column: %v", err)
	}
}

func TestDropRowsAndFailedRows(t *testing.T) {
	out, err := dropRows("h1,h2\na,1\nb,2\nc,3\n", map[int]bool{2: true, 4: true})
	if err != nil || out != "h1,h2\nb,2\n" {
		t.Fatalf("dropRows: %q %v", out, err)
	}
	if n := failedRows([]Issue{{Row: 2}, {Row: 2}, {Row: 5}}); n != 2 {
		t.Fatalf("failed rows: %d", n)
	}
	r := Report{Scope: "sales", Entity: "leads", Mode: "preview", Rows: 2, Created: 1, Failed: 1, Issues: []Issue{{Row: 3, Field: "name", Message: "required"}},
		Reconciliation: map[string]any{"opportunities": 1, "nested": map[string]any{"a": 1}}}
	sum := r.Summary()
	for _, want := range []string{"sales leads (preview): 2 rows", "row 3 [name]: required", "opportunities", `{"a":1}`, "dry run"} {
		if !strings.Contains(sum, want) {
			t.Fatalf("summary misses %q:\n%s", want, sum)
		}
	}
}
