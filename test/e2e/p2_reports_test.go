package e2e

import (
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"oneclub/internal/reporting"
)

// EP-29: every P2 report and KPI dashboard runs with a date range and
// exports; the Voucher Liability Report equals the deferred revenue
// sub-ledger of vouchers and prepaid balances.
func TestP2ReportsAndDashboards(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	from := time.Now().AddDate(0, 0, -400).Format("2006-01-02")
	to := time.Now().AddDate(0, 0, 30).Format("2006-01-02")
	for _, r := range reporting.P2Reports {
		res := sa.Must(200, "GET", "/api/v1/reporting/reports/"+r.Code+"?params[from]="+from+"&params[to]="+to, nil).JSON()
		if res["rows"] == nil {
			t.Fatalf("%s: %v", r.Code, res)
		}
	}
	for _, d := range reporting.DashboardCodes {
		res := sa.Must(200, "GET", "/api/v1/reporting/dashboards/"+d+"?from="+from+"&to="+to, nil).JSON()
		if len(res["kpis"].([]any)) < 5 {
			t.Fatalf("dashboard %s: %v", d, res)
		}
	}
	sa.Must(404, "GET", "/api/v1/reporting/dashboards/unknown", nil)

	// Voucher Liability Report = deferred revenue sub-ledger (voucher + prepaid).
	vt := idOf(sa.Must(201, "POST", "/api/v1/commercial/voucher-types", map[string]any{"code": "RPT-GIFT", "name": "Report gift", "kind": "value",
		"category": "gift", "unit": "rupiah", "faceValue": "250000", "price": "250000", "validityMonths": 12}))
	sa.Must(201, "POST", "/api/v1/commercial/vouchers:sell", map[string]any{"voucherTypeId": vt, "guestName": "Report Buyer",
		"payment": map[string]any{"methodType": "cash"}}, "Idempotency-Key", newKey())
	today := time.Now().In(f.Loc).Format("2006-01-02") // the club's date: the vouchers above were issued on it
	rep := sa.Must(200, "GET", "/api/v1/reporting/reports/commercial.voucher_liability?params[to]="+today, nil).JSON()
	sumRep := decimal.Zero
	for _, r := range rep["rows"].([]any) {
		v, _ := decimal.NewFromString(str(r.(map[string]any)["liability"]))
		sumRep = sumRep.Add(v)
	}
	led := sa.Must(200, "GET", "/api/v1/billing/deferred-revenue", nil).JSON()
	sumLed := decimal.Zero
	for _, r := range led["byType"].([]any) {
		m := r.(map[string]any)
		if m["liabilityType"] == "voucher" || m["liabilityType"] == "prepaid" {
			v, _ := decimal.NewFromString(str(m["amount"]))
			sumLed = sumLed.Add(v)
		}
	}
	if !sumRep.Equal(sumLed) || sumLed.IsZero() {
		t.Fatalf("voucher liability report %s ≠ sub-ledger %s", sumRep, sumLed)
	}
	// Export (CSV) of a P2 report is accepted.
	exp := sa.Must(202, "POST", "/api/v1/reporting/exports", map[string]any{"reportCode": "commercial.pos_sales", "format": "csv",
		"params": map[string]any{"from": from, "to": to}}, "Idempotency-Key", newKey()).JSON()
	if exp["status"] == nil {
		t.Fatalf("export: %v", exp)
	}
	_ = fmt.Sprint
}
