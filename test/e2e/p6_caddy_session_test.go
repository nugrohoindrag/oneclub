package e2e

import (
	"testing"
	"time"
)

// Demo feedback, second wave (9 Oct 2026): rain pauses the round on the
// caddy tablet and the play time stops; after the round the front desk takes
// the player's 1–5 caddy rating; the tee houses are set up in one go.
func TestRainPauseCaddyRatingAndTeeHouses(t *testing.T) {
	f := setupP2(t)
	sa := f.SA

	c := setupGolfCourse(t, sa, "RP")
	day := clubDay(inst, 29, isWeekday)
	bk, fid := golfBooking(t, sa, day, teeTimes(t, sa, c.Course, day)[0]["id"], []map[string]any{
		{"playerType": "non_member", "customerId": f.CustomerA, "name": "Hendra Wijaya"}})
	playDay, _ := time.ParseInLocation("2006-01-02", day, clubLoc(inst))
	caddy := idOf(sa.Must(201, "POST", "/api/v1/golf/caddies", map[string]any{"code": "C042", "name": "Caddy Rain", "gender": "female"}))
	sa.Must(201, "POST", "/api/v1/golf/caddy-attendance:clock-in", map[string]any{"caddyId": caddy, "at": rfc(at(playDay, 5, 30))})
	aid := str(sa.Must(201, "POST", "/api/v1/golf/caddy-assignments", map[string]any{"flightId": fid, "auto": true}).Items()[0]["id"])
	sa.Must(201, "POST", "/api/v1/golf/golf-cart-assignments", map[string]any{"flightId": fid, "auto": true})
	checkIn(t, sa, day, bk)

	// Only a flight on the course pauses.
	sa.Must(409, "POST", "/api/v1/golf/flights/"+fid+":pause", nil)
	sa.Must(200, "POST", "/api/v1/golf/rounds/"+fid+":start", map[string]any{"at": rfc(time.Now().Add(-60 * time.Minute))})
	p := sa.Must(200, "POST", "/api/v1/golf/flights/"+fid+":pause", map[string]any{"reason": "rain"}).JSON()
	if p["pausedAt"] == nil || p["pauseReason"] != "rain" {
		t.Fatalf("paused round: %v", p)
	}
	r := sa.Must(200, "POST", "/api/v1/golf/flights/"+fid+":resume", nil).JSON()
	if r["pausedAt"] != nil {
		t.Fatalf("resumed round: %v", r)
	}

	// The front desk takes the rating once the round is over.
	player := str(asMaps(bk["players"])[0]["id"])
	rate := map[string]any{"playerId": player, "rating": 5, "comment": "Reads the greens well"}
	sa.Must(409, "POST", "/api/v1/golf/caddy-assignments/"+aid+":rate", rate)
	sa.Must(200, "POST", "/api/v1/golf/starter-queue/"+fid+":finish", map[string]any{"holesPlayed": 18})
	sa.Must(422, "POST", "/api/v1/golf/caddy-assignments/"+aid+":rate", map[string]any{"playerId": player, "rating": 6})
	sa.Must(204, "POST", "/api/v1/golf/caddy-assignments/"+aid+":rate", rate)
	sa.Must(409, "POST", "/api/v1/golf/caddy-assignments/"+aid+":rate", rate)

	// Tee houses: two groups of three by default.
	th := sa.Must(200, "POST", "/api/v1/commercial/tee-houses:setup", map[string]any{}).Items()
	if len(th) != 6 || th[0]["code"] != "TH1" {
		t.Fatalf("tee houses: %v", th)
	}
}
