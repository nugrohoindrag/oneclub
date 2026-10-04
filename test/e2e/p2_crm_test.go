package e2e

import (
	"strings"
	"testing"
)

// EP-23/24: preferences (health data restricted), profiling consent,
// personalised context, Customer 360, interactions, segmentation & export,
// feedback with low-score follow-up, campaign honouring opt-in.
func TestP2CRM(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	c := customer(t, sa, "CRM-1", "Dewi Pelanggan", map[string]any{"email": "dewi@crm.test", "birthDate": dateAgo(40, 0, 0), "gender": "female"})
	sa.Must(201, "POST", "/api/v1/crm/customers/"+c+"/preferences", map[string]any{"category": "beverage", "value": "Es Teh Tawar"})
	sa.Must(201, "POST", "/api/v1/crm/customers/"+c+"/preferences", map[string]any{"category": "allergy", "value": "Peanuts"})
	again := sa.Must(201, "POST", "/api/v1/crm/customers/"+c+"/preferences", map[string]any{"category": "beverage", "value": "es teh tawar"}).JSON()
	if n := len(sa.Must(200, "GET", "/api/v1/crm/customers/"+c+"/preferences", nil).Items()); n != 2 {
		t.Fatalf("preferences (no duplicate) = %d (%v)", n, again)
	}
	fd := roleUser(t, inst, "front_desk")
	if ps := fd.Must(200, "GET", "/api/v1/crm/customers/"+c+"/preferences", nil).Items(); len(ps) != 1 || ps[0]["category"] != "beverage" {
		t.Fatalf("health preferences need view_sensitive: %v", ps)
	}
	// P1's Customer 360 (Basic) hides them too; staff with the permission see both.
	if ps := fd.Must(200, "GET", "/api/v1/crm/customers/"+c+"/overview", nil).JSON()["preferences"].([]any); len(ps) != 1 {
		t.Fatalf("Customer 360 shows health preferences without view_sensitive: %v", ps)
	}
	if ps := sa.Must(200, "GET", "/api/v1/crm/customers/"+c+"/overview", nil).JSON()["preferences"].([]any); len(ps) != 2 {
		t.Fatalf("Customer 360 preferences for view_sensitive: %v", ps)
	}
	// Profiling needs consent (UU PDP).
	if b := sa.Must(200, "GET", "/api/v1/crm/customers/"+c+"/behavior", nil).JSON(); b["consent"] != false || len(b["lines"].(map[string]any)) != 0 {
		t.Fatalf("no profiling without consent: %v", b)
	}
	sa.Must(200, "POST", "/api/v1/crm/customers/"+c+":consent", map[string]any{"profiling": true, "marketing": true})
	b := sa.Must(200, "GET", "/api/v1/crm/customers/"+c+"/behavior", nil).JSON()
	for _, k := range []string{"golf", "sportclub", "booking", "pos"} {
		if _, ok := b["lines"].(map[string]any)[k]; !ok {
			t.Fatalf("behaviour line %s missing: %v", k, b)
		}
	}
	ctx := sa.Must(200, "GET", "/api/v1/crm/customers/"+c+"/context", nil).JSON()
	if !strings.Contains(strings.Join(toStrings(ctx["highlights"]), "|"), "Likes Es Teh Tawar") {
		t.Fatalf("personalised context: %v", ctx)
	}
	v := sa.Must(200, "GET", "/api/v1/crm/customers/"+c+"/360", nil).JSON()
	for _, k := range []string{"membership", "golf", "sportclub", "bookings", "pos", "payments"} {
		if _, ok := v["sections"].(map[string]any)[k]; !ok {
			t.Fatalf("360 section %s missing: %v", k, v["sections"])
		}
	}
	if v["placeholders"].(map[string]any)["loyalty"] == nil {
		t.Fatalf("P3 placeholders: %v", v["placeholders"])
	}
	sa.Must(201, "POST", "/api/v1/crm/customers/"+c+"/interactions", map[string]any{"channel": "phone", "subject": "Asked about bungalow rates"})
	if r := sa.Do("POST", "/api/v1/crm/customers/"+c+"/interactions", map[string]any{"channel": "phone"}); r.Status != 422 {
		t.Fatalf("interaction subject required: %s", r)
	}

	// Segmentation: female, 30–50.
	seg := idOf(sa.Must(201, "POST", "/api/v1/crm/segments", map[string]any{"code": "LADIES-30-50", "name": "Ladies 30–50",
		"rules": map[string]any{"gender": "female", "minAge": 30, "maxAge": 50}}))
	res := sa.Must(200, "POST", "/api/v1/crm/segments/"+seg+":compute", nil).JSON()
	if res["memberCount"].(float64) < 1 {
		t.Fatalf("segment compute: %v", res)
	}
	found := false
	for _, m := range sa.Must(200, "GET", "/api/v1/crm/segments/"+seg+"/members", nil).Items() {
		found = found || m["customerId"] == c
	}
	csv := sa.Must(200, "GET", "/api/v1/crm/segments/"+seg+"/members.csv", nil)
	if !found || !strings.Contains(string(csv.Body), "Dewi Pelanggan") {
		t.Fatalf("segment members / export: %v %s", found, csv.Body)
	}

	// Feedback survey → low score alert → follow-up.
	inv := sa.Must(201, "POST", "/api/v1/crm/feedback-requests", map[string]any{"customerId": c, "contextType": "stay", "contextLabel": "Bungalow Eagle"}).JSON()
	pub := anon(t, inst)
	if s := pub.Must(200, "GET", "/api/v1/public/feedback/"+str(inv["token"]), nil).JSON(); s["status"] != "sent" {
		t.Fatalf("public survey: %v", s)
	}
	nps := 3
	fb := pub.Must(201, "POST", "/api/v1/public/feedback/"+str(inv["token"]), map[string]any{"rating": 4, "nps": nps, "comment": "AC noisy"}).JSON()
	if fb["lowScore"] != true || fb["followUp"] != "open" {
		t.Fatalf("NPS 3 is a low score: %v", fb)
	}
	sa.Must(200, "POST", "/api/v1/crm/feedback/"+str(fb["id"])+":follow-up", map[string]any{"note": "Called guest, AC fixed", "close": true})
	if l := sa.Must(200, "GET", "/api/v1/crm/feedback?filter[lowScore]=true&filter[followUp]=closed", nil).Items(); len(l) < 1 {
		t.Fatalf("closed follow-ups: %v", l)
	}

	// Campaign: only customers with marketing consent receive it.
	c2 := customer(t, sa, "CRM-2", "Ibu Tanpa Izin", map[string]any{"email": "noconsent@crm.test", "birthDate": dateAgo(35, 0, 0), "gender": "female"})
	sa.Must(200, "POST", "/api/v1/crm/segments/"+seg+":compute", nil)
	camp := idOf(sa.Must(201, "POST", "/api/v1/crm/campaigns", map[string]any{"code": "LADIES-DAY", "name": "Ladies Day", "segmentId": seg,
		"templateEvent": "crm.feedback_survey", "channel": "email", "data": map[string]any{"context": "Ladies Day", "link": "https://club.test/ladies"}}))
	sent := sa.Must(200, "POST", "/api/v1/crm/campaigns/"+camp+":send", nil).JSON()
	if sent["sent"].(float64) < 1 || sent["skipped"].(float64) < 1 {
		t.Fatalf("campaign honours opt-in: %v (no-consent customer %s)", sent, c2)
	}
	if r := sa.Do("POST", "/api/v1/crm/campaigns/"+camp+":send", nil); r.Status != 409 {
		t.Fatalf("campaign sent once: %s", r)
	}
	its := sa.Must(200, "GET", "/api/v1/crm/customers/"+c+"/interactions", nil).Items()
	sources := map[string]bool{}
	for _, it := range its {
		sources[str(it["source"])] = true
	}
	if !sources["manual"] || !sources["campaign"] || !sources["feedback"] {
		t.Fatalf("interaction history: %v", its)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, str(x))
	}
	return out
}
