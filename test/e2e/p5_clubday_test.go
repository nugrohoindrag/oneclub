package e2e

import (
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// As-of cut-offs and date filters follow the club's calendar (instance
// timezone, Asia/Jakarta = UTC+7): an invoice issued yesterday and paid at
// 00:30 today at the club (17:30 UTC yesterday) is still open as of
// yesterday in every ageing (billing, AR Aging of accounting, both AR Aging
// reports), and its payment and folio are listed under today. A cut-off at
// midnight UTC (date + 1 compared with the timestamp) counted the payment
// as of yesterday and dated it yesterday, every day, not only from 17:00 to
// 24:00 UTC.
func TestClubDayAsOfCutOffsAndFilters(t *testing.T) {
	c := accMDR(t) // the property with books (accounting AR Aging)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	loc := clubLoc(inst)
	now := time.Now().In(loc)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	today, yday := todayStart.Format("2006-01-02"), todayStart.AddDate(0, 0, -1).Format("2006-01-02")
	at := todayStart.Add(30 * time.Minute) // 00:30 at the club
	if at.After(time.Now()) {
		at = time.Now()
	}

	corp := idOf(c.Must(201, "POST", "/api/v1/crm/corporate-accounts", map[string]any{"code": "CLD" + sfx, "name": "PT Hari Klub " + sfx,
		"email": "ar" + sfx + "@clubday.test"}, "Idempotency-Key", newKey()))
	cf := str(c.Must(200, "POST", "/api/v1/billing/customer-folios", map[string]any{"corporateAccountId": corp}).JSON()["id"])
	holder := "Hari Klub " + sfx
	f := idOf(c.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"holderName": holder, "sourceRef": "Club day"}))
	c.Must(201, "POST", "/api/v1/billing/folios/"+f+"/lines", map[string]any{"chargeType": "other", "description": "Meeting room", "unitPrice": "2000000"})
	c.Must(200, "POST", "/api/v1/billing/customer-folios/"+cf+":merge", map[string]any{"folioIds": []string{f}})
	inv := c.Must(201, "POST", "/api/v1/billing/invoices", map[string]any{"customerFolioId": cf, "corporateAccountId": corp, "termsDays": 30},
		"Idempotency-Key", newKey()).JSON()
	iid := str(inv["id"])
	issued := c.Must(200, "POST", "/api/v1/billing/invoices/"+iid+":issue", nil).JSON()
	total := bilDec(issued["total"])
	c.Must(200, "POST", "/api/v1/billing/invoices/"+iid+":pay", map[string]any{"methodType": "bank_transfer", "amount": total.String(), "reference": "TRF-" + sfx})
	if st := c.Must(200, "GET", "/api/v1/billing/invoices/"+iid, nil).JSON(); st["status"] != "paid" {
		t.Fatalf("invoice paid: %v", st)
	}

	// Issued yesterday, paid (allocated) at 00:30 today at the club; the
	// folio was opened then too. Restored afterwards.
	var issue0 time.Time
	var paid0, folio0 time.Time
	var pid string
	sysQueryRow(t, inst, `SELECT i.issue_date, a.created_at, a.payment_id::text, f.created_at FROM billing.invoices i
		JOIN billing.payment_allocations a ON a.invoice_id = i.id JOIN billing.folios f ON f.id = $2 WHERE i.id = $1`,
		[]any{mustUUID(iid), mustUUID(f)}, &issue0, &paid0, &pid, &folio0)
	t.Cleanup(func() {
		sysExec(t, inst, `UPDATE billing.invoices SET issue_date = $2 WHERE id = $1`, mustUUID(iid), issue0)
		sysExec(t, inst, `UPDATE billing.payment_allocations SET created_at = $2 WHERE invoice_id = $1`, mustUUID(iid), paid0)
		sysExec(t, inst, `UPDATE billing.payments SET created_at = $2, paid_at = $2 WHERE id = $1`, mustUUID(pid), paid0)
		sysExec(t, inst, `UPDATE billing.folios SET created_at = $2 WHERE id = $1`, mustUUID(f), folio0)
	})
	sysExec(t, inst, `UPDATE billing.invoices SET issue_date = $2::date WHERE id = $1`, mustUUID(iid), yday)
	sysExec(t, inst, `UPDATE billing.payment_allocations SET created_at = $2 WHERE invoice_id = $1`, mustUUID(iid), at)
	sysExec(t, inst, `UPDATE billing.payments SET created_at = $2, paid_at = $2 WHERE id = $1`, mustUUID(pid), at)
	sysExec(t, inst, `UPDATE billing.folios SET created_at = $2 WHERE id = $1`, mustUUID(f), at)

	// Operational ageing of billing.
	billingOpen := func(asOf string) decimal.Decimal {
		ag := c.Must(200, "GET", "/api/v1/billing/aging?asOf="+asOf, nil).JSON()
		for _, r := range ag["rows"].([]any) {
			if m := r.(map[string]any); str(m["corporateAccountId"]) == corp {
				return bilDec(m["total"])
			}
		}
		return decimal.Zero
	}
	if o := billingOpen(yday); !o.Equal(total) {
		t.Fatalf("billing ageing as of yesterday: open %s, want %s (paid at 00:30 today at the club)", o, total)
	}
	if o := billingOpen(today); !o.IsZero() {
		t.Fatalf("billing ageing as of today: open %s, want 0", o)
	}
	// AR Aging of accounting (FR-AR-04).
	accOpen := func(asOf string) decimal.Decimal {
		ag := c.Must(200, "GET", accBase+"/ar-aging?asOf="+asOf, nil).JSON()
		for _, r := range ag["rows"].([]any) {
			if m := r.(map[string]any); str(m["corporateAccountId"]) == corp {
				return bilDec(m["total"])
			}
		}
		return decimal.Zero
	}
	if o := accOpen(yday); !o.Equal(total) {
		t.Fatalf("accounting AR aging as of yesterday: open %s, want %s", o, total)
	}
	if o := accOpen(today); !o.IsZero() {
		t.Fatalf("accounting AR aging as of today: open %s, want 0", o)
	}
	// Both AR Aging reports.
	reportOpen := func(code, key, val, to string) decimal.Decimal {
		rep := c.Must(200, "GET", "/api/v1/reporting/reports/"+code+"?params[from]="+to+"&params[to]="+to, nil).JSON()
		for _, r := range rep["rows"].([]any) {
			if m := r.(map[string]any); str(m[key]) == val {
				return bilDec(m["total"])
			}
		}
		return decimal.Zero
	}
	for _, x := range []struct{ code, key, val string }{{"billing.ar_aging", "billTo", "PT Hari Klub " + sfx}, {"accounting.ar_aging", "invoice", str(issued["number"])}} {
		if o := reportOpen(x.code, x.key, x.val, yday); !o.Equal(total) {
			t.Fatalf("%s as of yesterday: open %s, want %s", x.code, o, total)
		}
		if o := reportOpen(x.code, x.key, x.val, today); !o.IsZero() {
			t.Fatalf("%s as of today: open %s, want 0", x.code, o)
		}
	}

	// List filters by date: the payment and the folio of 00:30 are today's.
	for _, x := range []struct{ path, id string }{
		{"/api/v1/billing/payments?q=TRF-" + sfx + "&date=", pid},
		{"/api/v1/billing/folios?q=" + url.QueryEscape(holder) + "&date=", f},
	} {
		listed := func(day string) bool {
			for _, it := range c.Must(200, "GET", x.path+day, nil).Items() {
				if str(it["id"]) == x.id {
					return true
				}
			}
			return false
		}
		if !listed(today) || listed(yday) {
			t.Fatalf("%s: listed under today %v, yesterday %v — want today only", x.path, listed(today), listed(yday))
		}
	}
	c.Must(200, "GET", "/api/v1/billing/member-charges?date="+today, nil)
}
