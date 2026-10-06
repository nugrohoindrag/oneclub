package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// Billing workspace (Revenue & Billing → Billing): a folio without a payer is
// an exception that blocks Mark Ready and Generate; Prepare Billing fixes it;
// an accountant's adjustment waits for the Finance Manager's approval; the
// invoice is generated from the billing record and the folio moves to
// Invoiced with what the customer really paid; consolidation needs the
// Finance Manager and one payer; a medium exception is acknowledged.
func TestBillingWorkspace(t *testing.T) {
	sa := superAdmin(t, inst)
	fin := login(t, inst, "finance@demo.oneclub.id", demoPassword)
	acc := roleUser(t, inst, "accountant")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	cust := customer(t, sa, "BW"+sfx, "Billing Customer "+sfx, nil)
	folio := func(customer string) string {
		body := map[string]any{"holderName": "Billing " + sfx}
		if customer != "" {
			body["customerId"] = customer
		}
		f := idOf(sa.Must(201, "POST", "/api/v1/billing/folios", body))
		sa.Must(201, "POST", "/api/v1/billing/folios/"+f+"/lines", map[string]any{"chargeType": "other", "description": "Meeting room", "unitPrice": "2000000"})
		return f
	}
	item := func(f string) map[string]any {
		return acc.Must(200, "GET", "/api/v1/billing/billing-records/"+f, nil).JSON()["item"].(map[string]any)
	}
	codes := func(it map[string]any) map[string]bool {
		out := map[string]bool{}
		for _, e := range it["exceptions"].([]any) {
			if e.(map[string]any)["resolved"] == nil {
				out[str(e.(map[string]any)["code"])] = true
			}
		}
		return out
	}

	// No payer: pending with Missing Customer; Mark Ready and Generate refuse it.
	f := folio("")
	if it := item(f); it["status"] != "pending_billing" || !codes(it)["missing_customer"] || it["canInvoice"] != false {
		t.Fatalf("folio without payer: %v", it)
	}
	acc.Must(409, "POST", "/api/v1/billing/billing-records/"+f+":mark-ready", map[string]any{})
	gen := acc.Must(200, "POST", "/api/v1/billing/billing-records:generate", map[string]any{"folioIds": []string{f}}).JSON()
	if gen["generated"] != float64(0) || gen["skipped"] != float64(1) {
		t.Fatalf("generate with a blocking exception: %v", gen)
	}
	acc.Must(409, "POST", "/api/v1/billing/billing-records/"+f+"/exceptions/missing_customer:resolve", map[string]any{"reason": "later"})

	// Prepare Billing: bill the customer; ready once every validation passed.
	acc.Must(200, "PUT", "/api/v1/billing/billing-records/"+f, map[string]any{"customerId": cust, "customerPo": "PO-" + sfx})
	d := acc.Must(200, "POST", "/api/v1/billing/billing-records/"+f+":mark-ready", map[string]any{}).JSON()
	if it := d["item"].(map[string]any); it["status"] != "ready_to_invoice" || it["billTo"] != "Billing Customer "+sfx {
		t.Fatalf("marked ready: %v", it)
	}

	// An accountant's discount waits for the Finance Manager.
	line := str(d["lines"].([]any)[0].(map[string]any)["id"])
	d = acc.Must(200, "POST", "/api/v1/billing/billing-records/"+f+"/adjustments", map[string]any{"lineId": line, "kind": "discount", "amount": "200000",
		"reason": "Loyal customer"}).JSON()
	if it := d["item"].(map[string]any); it["status"] != "exception" || !codes(it)["approval_required"] || !bilDec(it["toInvoice"]).Equal(decimal.NewFromInt(1_800_000)) {
		t.Fatalf("after the discount: %v", it)
	}
	acc.Must(403, "POST", "/api/v1/billing/billing-records/"+f+":approve", map[string]any{})
	fin.Must(200, "POST", "/api/v1/billing/billing-records/"+f+":approve", map[string]any{"reason": "OK"})
	if it := item(f); it["status"] != "ready_to_invoice" {
		t.Fatalf("approved: %v", it)
	}

	// Generate: issued from the billing record, linked back to the folio.
	gen = acc.Must(200, "POST", "/api/v1/billing/billing-records:generate", map[string]any{"folioIds": []string{f}}).JSON()
	res := gen["results"].([]any)[0].(map[string]any)
	if gen["generated"] != float64(1) || !bilDec(res["total"]).Equal(decimal.NewFromInt(1_800_000)) {
		t.Fatalf("generate: %v", gen)
	}
	inv := fin.Must(200, "GET", "/api/v1/billing/invoices/"+str(res["invoiceId"]), nil).JSON()
	if inv["status"] != "issued" || inv["customerPo"] != "PO-"+sfx || inv["billToName"] != "Billing Customer "+sfx {
		t.Fatalf("invoice: %v", inv)
	}
	it := item(f)
	if it["status"] != "invoiced" || it["invoiceId"] != res["invoiceId"] || !bilDec(it["paid"]).IsZero() || !bilDec(it["outstanding"]).Equal(bilDec(inv["total"])) {
		t.Fatalf("invoiced folio: %v", it)
	}
	// What the customer paid on the invoice, not the transfer to AR.
	fin.Must(200, "POST", "/api/v1/billing/invoices/"+str(res["invoiceId"])+":pay", map[string]any{"methodType": "bank_transfer", "amount": "500000"})
	if it = item(f); it["invoiceStatus"] != "partially_paid" || !bilDec(it["paid"]).Equal(decimal.NewFromInt(500_000)) {
		t.Fatalf("partially paid invoice: %v", it)
	}
	hist := acc.Must(200, "GET", "/api/v1/billing/billing-records/"+f, nil).JSON()["history"].([]any)
	kinds := map[string]bool{}
	for _, h := range hist {
		kinds[str(h.(map[string]any)["kind"])] = true
	}
	for _, k := range []string{"charge_created", "billing_edited", "discount_changed", "billing_approved", "billing_marked_ready", "invoice_generated", "invoice_posted"} {
		if !kinds[k] {
			t.Fatalf("history has no %s: %v", k, hist)
		}
	}

	// Consolidation: the Finance Manager, one payer, one invoice for both folios.
	a, b, other := folio(cust), folio(cust), folio(customer(t, sa, "BX"+sfx, "Other Customer "+sfx, nil))
	acc.Must(403, "POST", "/api/v1/billing/billing-records:consolidate", map[string]any{"folioIds": []string{a, b}})
	fin.Must(409, "POST", "/api/v1/billing/billing-records:consolidate", map[string]any{"folioIds": []string{a, other}})
	con := fin.Must(200, "POST", "/api/v1/billing/billing-records:consolidate", map[string]any{"folioIds": []string{a, b}}).JSON()
	if !bilDec(con["total"]).Equal(decimal.NewFromInt(4_000_000)) || item(a)["invoiceId"] != con["invoiceId"] || item(b)["invoiceId"] != con["invoiceId"] {
		t.Fatalf("consolidated: %v", con)
	}
	// a folio invoice whose source lists both folios
	if ci := fin.Must(200, "GET", "/api/v1/billing/invoices/"+str(con["invoiceId"]), nil).JSON(); ci["source"] != "folio" ||
		!strings.Contains(str(ci["sourceRef"]), ", ") {
		t.Fatalf("consolidated invoice source: %v / %v", ci["source"], ci["sourceRef"])
	}

	// Owner, note and split (the original folio keeps its charges).
	owners := acc.Must(200, "GET", "/api/v1/billing/billing-owners", nil).Items()
	if len(owners) == 0 {
		t.Fatal("no billing owners")
	}
	acc.Must(200, "POST", "/api/v1/billing/billing-records:assign", map[string]any{"folioIds": []string{other}, "ownerId": owners[0]["id"]})
	acc.Must(200, "POST", "/api/v1/billing/billing-records/"+other+"/notes", map[string]any{"body": "Call the guest"})
	if it := item(other); it["ownerId"] != owners[0]["id"] {
		t.Fatalf("owner: %v", it)
	}
	sp := acc.Must(200, "POST", "/api/v1/billing/billing-records/"+other+":split", map[string]any{"customerId": cust, "amount": "500000", "reason": "Host pays part"}).JSON()
	if !bilDec(sp["moved"]).Equal(decimal.NewFromInt(500_000)) || !bilDec(item(other)["toInvoice"]).Equal(decimal.NewFromInt(1_500_000)) {
		t.Fatalf("split: %v / %v", sp, item(other))
	}
	q := acc.Must(200, "GET", "/api/v1/billing/billing-queue?filter[status]=pending_billing&filter[owner]="+str(owners[0]["id"])+"&q=Other+Customer+"+sfx, nil).Items()
	if len(q) != 1 || q[0]["folioId"] != other {
		t.Fatalf("queue by owner: %v", q)
	}

	// A credit term for an individual is a medium exception the accountant acknowledges.
	acc.Must(200, "PUT", "/api/v1/billing/billing-records/"+other, map[string]any{"termsDays": 14})
	if !codes(item(other))["invalid_payment_term"] {
		t.Fatalf("no payment term exception: %v", item(other))
	}
	acc.Must(200, "POST", "/api/v1/billing/billing-records/"+other+"/exceptions/invalid_payment_term:resolve", map[string]any{"reason": "Member with terms"})
	if it := item(other); codes(it)["invalid_payment_term"] || it["canInvoice"] != true {
		t.Fatalf("acknowledged: %v", it)
	}
}
