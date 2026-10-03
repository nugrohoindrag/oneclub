package e2e

// P1 website booking (EP-14) with online payment (EP-12 / FR-INT-P1-01) and
// the Modern Golf rate card (EP-08).

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/platform/integration"
)

// FR-PRC-03/04 AC: the rate card effective 1 April 2026 resolves to the
// published all-in prices.
func TestP1RateCard(t *testing.T) {
	gm := login(t, inst, "golf.manager@demo.oneclub.id", demoPassword)
	wd := clubDay(inst, 2, isWeekday)
	sat := clubDay(inst, 1, isSaturday)
	cases := []struct {
		date, tm string
		segments []string
		want     int64
		rule     string
	}{
		{wd, "06:00", []string{"member"}, 640000, "MEMBER"},
		{sat, "06:00", []string{"member"}, 640000, "MEMBER"},
		{wd, "06:00", []string{"guest"}, 995000, "GUEST-WD"},
		{wd, "12:00", []string{"guest"}, 995000, "GUEST-WD"},
		{sat, "06:00", []string{"guest"}, 2960000, "GUEST-WE-AM"},
		{sat, "12:30", []string{"guest"}, 1960000, "GUEST-WE-PM"},
		{wd, "17:00", []string{"guest"}, 1050000, "GUEST-NIGHT"},
		{sat, "17:00", []string{"guest"}, 1050000, "GUEST-NIGHT"},
		{wd, "06:00", []string{"senior", "guest"}, 740000, "SENIOR-WD"},
		{wd, "06:00", []string{"ladies", "guest"}, 740000, "LADIES-WD"},
		{wd, "06:00", []string{"junior", "guest"}, 740000, "JUNIOR-WD"},
		{sat, "06:00", []string{"senior", "guest"}, 2960000, "GUEST-WE-AM"}, // no weekend senior rate
	}
	for _, c := range cases {
		r := gm.Must(200, "POST", "/api/v1/commercial/pricing:resolve", map[string]any{"date": c.date, "time": c.tm, "segments": c.segments,
			"chargeType": "golf_round", "channel": "back_office"}).JSON()
		if !dec(r["total"]).Equal(dec(c.want)) || r["ruleCode"] != c.rule {
			t.Fatalf("%s %s %v: want %d (%s), got %v (%v)", c.date, c.tm, c.segments, c.want, c.rule, r["total"], r["ruleCode"])
		}
		// all-in: PPN 11% included, caddy fee held as liability
		if !dec(r["taxAmount"]).IsPositive() {
			t.Fatalf("PPN 11%% must be included: %v", r)
		}
	}
	r := gm.Must(200, "POST", "/api/v1/commercial/pricing:resolve", map[string]any{"date": wd, "time": "06:00", "segments": []string{"guest"},
		"chargeType": "extra_cart"}).JSON()
	eqAmount(t, "extra buggy surcharge (non-member)", r["total"], 560000)
}

// FR-WEB-04..08 AC: a website guest books, pays online, the gateway
// webhook arrives three times and exactly one payment settles the booking;
// the guest then cancels from the manage link and is refunded.
func TestP1WebsiteBooking(t *testing.T) {
	gm := login(t, inst, "golf.manager@demo.oneclub.id", demoPassword)
	course := demoCourse(t, inst)
	day := clubDay(inst, 3, isWeekday)
	pm := slotsOf(teeTimes(t, gm, course, day), "afternoon", 10)

	web := anon(t, inst)
	web.Must(200, "GET", "/api/v1/public/golf/info", nil)
	web.Must(200, "GET", "/api/v1/public/golf/rates?date="+day, nil)
	if n := len(web.Must(200, "GET", "/api/v1/public/golf/availability?date="+day+"&courseId="+course+"&players=2", nil).Items()); n == 0 {
		t.Fatal("public availability empty")
	}
	h := web.Must(201, "POST", "/api/v1/public/golf/holds", map[string]any{"teeTimeId": pm[2]["id"], "players": 2}).JSON()
	b := web.Must(201, "POST", "/api/v1/public/golf/bookings", map[string]any{"holdId": h["id"], "holdToken": h["holdToken"], "consent": true,
		"paymentMethod": "qris", "contact": map[string]any{"name": "Xena Website", "phone": "+628129990050", "email": "xena@example.test"},
		"players": []map[string]any{{"name": "Yosef Website", "phone": "+628129990051"}}}).JSON()
	if b["status"] != "pending" || b["payment"] == nil || b["manageToken"] == "" {
		t.Fatalf("website booking waits for online payment: %v", b)
	}
	pay := b["payment"].(map[string]any)
	eqAmount(t, "website total (2 × guest weekday)", pay["amount"], 2*995000)

	// gateway webhook delivered three times (distinct delivery ids)
	pa := platformAdmin(t, inst)
	sec := str(pa.Must(200, "POST", "/api/v1/platform/integrations/"+integrationID(t, inst, "mock-payment")+":rotate-webhook-secret", nil).JSON()["webhookSecret"])
	hook := anon(t, inst)
	hook.Property = uuid.Nil
	for i := 0; i < 3; i++ {
		raw, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("evt_web_%s_%d", str(b["code"]), i), "type": "payment.paid",
			"data": map[string]any{"reference": pay["number"], "status": "paid", "amount": str(pay["amount"])}})
		hook.Must(200, "POST", "/api/v1/webhooks/mock-payment", raw, integration.SignatureHeader, integration.Sign(sec, raw, time.Now()))
	}
	token := str(b["manageToken"])
	waitFor(t, 15*time.Second, "booking confirmed by payment", func() bool {
		return web.Must(200, "GET", "/api/v1/public/bookings/"+token, nil).JSON()["status"] == "confirmed"
	})
	var payments int
	sysQueryRow(t, inst, `SELECT count(*) FROM billing.payments p JOIN golf.bookings b ON b.folio_id = p.folio_id
		WHERE b.code = $1 AND p.status = 'completed'`, []any{b["code"]}, &payments)
	if payments != 1 {
		t.Fatalf("exactly one completed payment expected, got %d", payments)
	}
	c := web.Must(200, "POST", "/api/v1/public/bookings/"+token+":cancel", map[string]any{"reason": "Change of plans"}).JSON()
	if c["status"] != "cancelled" {
		t.Fatalf("public cancel: %v", c)
	}
}
