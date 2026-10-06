package e2e

import (
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// Invoice Management (Revenue & Billing): a manual invoice for an exception
// is previewed without saving anything, needs billing.invoice.manual, prices
// its lines with discount and the Tax & Service rules, carries the customer
// references and supporting documents (internal notes and documents for
// staff only), is billed through its own folio and voiding it voids the
// charges; the invoice list filters by source, payer, due date and amount.
func TestInvoiceManual(t *testing.T) {
	sa := superAdmin(t, inst)
	fin := login(t, inst, "finance@demo.oneclub.id", demoPassword)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	corp := idOf(sa.Must(201, "POST", "/api/v1/crm/corporate-accounts", map[string]any{"code": "MAN" + sfx, "name": "PT Manual " + sfx,
		"email": "ar" + sfx + "@manual.test", "npwp": "02.222.333.4-555.000", "address": "Jl. Thamrin 2, Jakarta"}))

	// A tax rule of the property (the demo seeds PPN on MAIN).
	var tax map[string]any
	for _, c := range fin.Must(200, "GET", "/api/v1/billing/invoice-tax-codes", nil).Items() {
		if c["kind"] == "tax" && tax == nil {
			tax = c
		}
	}
	if tax == nil {
		t.Fatal("no tax rule in force on the main property")
	}
	rate := bilDec(tax["ratePercent"])
	lines := []map[string]any{
		{"description": "Golf Package", "businessLine": "golf", "revenueComponent": "green_fee", "quantity": "2", "unit": "pax", "unitPrice": "750000",
			"discountPercent": "10", "taxCodes": []string{str(tax["code"])}},
		{"description": "Golf Cart", "businessLine": "golf", "revenueComponent": "buggy_fee", "unitPrice": "250000", "discount": "50000"},
	}
	// 2 × 750,000 − 10% = 1,350,000 (+ tax) and 250,000 − 50,000 = 200,000.
	wantTax := decimal.NewFromInt(1_350_000).Mul(rate).Div(decimal.NewFromInt(100)).Round(0)
	wantTotal := decimal.NewFromInt(1_550_000).Add(wantTax)
	body := map[string]any{"corporateAccountId": corp, "lines": lines, "termsDays": 14, "customerPo": "PO-" + sfx, "contractRef": "CTR-" + sfx,
		"notes": "Thank you for playing", "internalNotes": "Agreed by phone with the GM"}

	// Preview: the numbers, nothing saved.
	pv := fin.Must(200, "POST", "/api/v1/billing/invoices:preview", body).JSON()
	if !bilDec(pv["total"]).Equal(wantTotal) || !bilDec(pv["taxAmount"]).Equal(wantTax) || pv["source"] != "manual" {
		t.Fatalf("preview total %v tax %v (want %s / %s): %v", pv["total"], pv["taxAmount"], wantTotal, wantTax, pv)
	}
	if got := fin.Must(200, "GET", "/api/v1/billing/invoices?filter[corporateAccountId]="+corp, nil).Items(); len(got) != 0 {
		t.Fatalf("preview saved an invoice: %v", got)
	}
	// Manual invoices have their own permission.
	roleUser(t, inst, "banquet_sales").Must(403, "POST", "/api/v1/billing/invoices", body, "Idempotency-Key", newKey())
	fin.Must(422, "POST", "/api/v1/billing/invoices", map[string]any{"lines": lines}, "Idempotency-Key", newKey())
	fin.Must(422, "POST", "/api/v1/billing/invoices", map[string]any{"corporateAccountId": corp, "lines": []map[string]any{{"description": "x",
		"unitPrice": "100", "discount": "200"}}}, "Idempotency-Key", newKey())

	// A supporting document, then the invoice issued.
	up, ctype := multipartBody(t, nil, "file", "po.pdf", "%PDF-1.4\n% purchase order\n")
	r := fin.Do("POST", "/api/v1/billing/invoice-files", up, "Content-Type", ctype)
	if r.Status != 201 {
		t.Fatalf("upload: %s", r)
	}
	file := str(r.JSON()["id"])
	body["attachmentFileIds"], body["issue"] = []string{file}, true
	inv := fin.Must(201, "POST", "/api/v1/billing/invoices", body, "Idempotency-Key", newKey()).JSON()
	iid := str(inv["id"])
	if inv["status"] != "issued" || !bilDec(inv["total"]).Equal(wantTotal) || inv["customerPo"] != "PO-"+sfx || inv["billToName"] != "PT Manual "+sfx {
		t.Fatalf("manual invoice: %v", inv)
	}
	ls := inv["lines"].([]any)
	if l := ls[0].(map[string]any); len(ls) != 2 || l["unit"] != "pax" || !bilDec(l["discountAmount"]).Equal(decimal.NewFromInt(150_000)) {
		t.Fatalf("lines: %v", ls)
	}
	det := fin.Must(200, "GET", "/api/v1/billing/invoices/"+iid, nil).JSON()
	in, _ := det["internal"].(map[string]any)
	if in == nil || in["notes"] != "Agreed by phone with the GM" || len(in["attachments"].([]any)) != 1 {
		t.Fatalf("internal part: %v", det["internal"])
	}
	fin.Must(200, "GET", "/api/v1/billing/invoices/"+iid+"/attachments/"+file, nil)
	fin.Must(200, "GET", "/api/v1/billing/invoices/"+iid+"/pdf", nil)
	if p := fin.Must(200, "PATCH", "/api/v1/billing/invoices/"+iid+"/internal", map[string]any{"notes": "", "attachmentFileIds": []string{}}).JSON(); len(p["internal"].(map[string]any)["attachments"].([]any)) != 0 {
		t.Fatalf("internal update: %v", p["internal"])
	}

	// The list: source, payer, due date and amount filters.
	q := "/api/v1/billing/invoices?filter[corporateAccountId]=" + corp
	if got := fin.Must(200, "GET", q+"&filter[source]=manual&filter[payer]=corporate&minTotal="+wantTotal.String(), nil).Items(); len(got) != 1 || got[0]["sourceRef"] == nil {
		t.Fatalf("filtered list: %v", got)
	}
	for _, f := range []string{"&filter[source]=folio", "&filter[payer]=individual", "&maxTotal=1", "&dueTo=2000-01-01"} {
		if got := fin.Must(200, "GET", q+f, nil).Items(); len(got) != 0 {
			t.Fatalf("filter %s: %v", f, got)
		}
	}
	fin.Must(422, "GET", q+"&minTotal=abc", nil)

	// Void: the folio charges are voided too.
	fin.Must(200, "POST", "/api/v1/billing/invoices/"+iid+":void", map[string]any{"reason": "Billed twice"})
	var open int
	sysQueryRow(t, inst, `SELECT count(*) FROM billing.folio_lines WHERE folio_id = $1 AND voided_at IS NULL`, []any{mustUUID(str(inv["folioId"]))}, &open)
	if open != 0 {
		t.Fatalf("%d manual charges still open after void", open)
	}
}
