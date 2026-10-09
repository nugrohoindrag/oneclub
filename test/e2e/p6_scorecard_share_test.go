package e2e

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// Scorecard to print and share (demo feedback 9 Oct 2026): after the round
// the front desk prints a player's card (PDF with tee, scores, course
// handicap, net and caddy) and shares a public link to it — by e-mail or
// WhatsApp — that opens without an account.
func TestScorecardPrintAndShare(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	fd := login(t, inst, "front.desk@demo.oneclub.id", demoPassword)
	c := setupGolfCourse(t, sa, "SS")
	sa.Must(204, "POST", "/api/v1/golf/players/"+f.CustomerA+"/official-handicap", map[string]any{"index": "14.2", "source": "PGI"})
	day := clubDay(inst, 28, isWeekday)
	bk, fid := golfBooking(t, sa, day, teeTimes(t, sa, c.Course, day)[0]["id"], []map[string]any{
		{"playerType": "non_member", "customerId": f.CustomerA, "name": "Hendra Wijaya"}})
	playDay, _ := time.ParseInLocation("2006-01-02", day, clubLoc(inst))
	caddy := idOf(sa.Must(201, "POST", "/api/v1/golf/caddies", map[string]any{"code": "C051", "name": "Caddy Card", "gender": "female"}))
	sa.Must(201, "POST", "/api/v1/golf/caddy-attendance:clock-in", map[string]any{"caddyId": caddy, "at": rfc(at(playDay, 5, 30))})
	sa.Must(201, "POST", "/api/v1/golf/caddy-assignments", map[string]any{"flightId": fid, "auto": true})
	sa.Must(201, "POST", "/api/v1/golf/golf-cart-assignments", map[string]any{"flightId": fid, "auto": true})
	checkIn(t, sa, day, bk)
	sa.Must(200, "POST", "/api/v1/golf/rounds/"+fid+":start", map[string]any{"at": rfc(time.Now().Add(-4 * time.Hour))})

	cards := fd.Must(200, "GET", "/api/v1/golf/bookings/"+str(bk["id"])+"/scorecards", nil).Items()
	if len(cards) != 1 {
		t.Fatalf("scorecards of the booking: %v", cards)
	}
	sid := str(cards[0]["id"])
	var entries []map[string]any
	for s := 1; s <= 18; s++ {
		entries = append(entries, map[string]any{"seq": s, "strokes": 5, "putts": 2})
	}
	sa.Must(200, "POST", "/api/v1/golf/scorecards/"+sid+"/scores", map[string]any{"source": "caddy", "entries": entries})
	card := fd.Must(200, "GET", "/api/v1/golf/bookings/"+str(bk["id"])+"/scorecards", nil).Items()[0]
	if card["gross"].(float64) != 90 || card["courseHandicap"] == nil || card["caddyName"] != "Caddy Card" || card["teeSetName"] == nil {
		t.Fatalf("printed card: %v", card)
	}
	if card["net"].(float64) != 90-card["courseHandicap"].(float64) {
		t.Fatalf("net = gross − course handicap: %v", card)
	}

	// the desk prints the PDF
	r := fd.Do("GET", "/api/v1/golf/scorecards/"+sid+"/pdf", nil)
	if r.Status != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/pdf") || !bytes.HasPrefix(r.Body, []byte("%PDF")) {
		t.Fatalf("scorecard PDF: %d %s", r.Status, r.Header.Get("Content-Type"))
	}

	// the link opens without an account and stays the same
	link := str(fd.Must(200, "POST", "/api/v1/golf/scorecards/"+sid+":share", map[string]any{}).JSON()["link"])
	i := strings.Index(link, "/api/v1/public/golf/scorecards/")
	if i < 0 {
		t.Fatalf("share link: %s", link)
	}
	if again := str(fd.Must(200, "POST", "/api/v1/golf/scorecards/"+sid+":share", map[string]any{}).JSON()["link"]); again != link {
		t.Fatalf("the link stays the same: %s / %s", link, again)
	}
	if pub := anon(t, inst).Do("GET", link[i:], nil); pub.Status != 200 || !bytes.HasPrefix(pub.Body, []byte("%PDF")) {
		t.Fatalf("public scorecard: %d", pub.Status)
	}
	anon(t, inst).Must(404, "GET", "/api/v1/public/golf/scorecards/not-a-token", nil)

	// sent by e-mail / WhatsApp
	sent := fd.Must(200, "POST", "/api/v1/golf/scorecards/"+sid+":share", map[string]any{"send": true, "email": "hendra.card@example.test", "phone": "+628129990091"}).JSON()
	if to := sent["sentTo"].([]any); len(to) != 2 {
		t.Fatalf("scorecard sent to: %v", to)
	}
}
