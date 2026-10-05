package app

// Trial dataset: customer engagement. At go-live most members join the
// loyalty programme (points are earned by the module on every settled
// payment and finished round) and the marketing segments are set up.
// Every day guests answer part of yesterday's round surveys (rating, NPS;
// a low score opens a complaint ticket), complaints come in at the front
// desk and the managers resolve and close them, members redeem rewards with
// their points; the member of the Member App answers the relationship NPS
// once a month; every month the marketing team sends a campaign to a
// segment (the Marketing Manager approves large ones) and the worker jobs
// of the month run at their simulated time: loyalty expiry / tier
// evaluation every night, the Top Spender snapshot on the 1st and the
// campaign dispatch.

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm/topspender"
	"oneclub/internal/kernel/dbtx"
)

func init() {
	registerTrialSeeder(trialSeeder{Name: "engagement", Order: 70, Setup: trialEngagementSetup, Day: trialEngagementDay})
}

const (
	trialCRMAdmin    = "crm.admin@demo.oneclub.id"
	trialClubManager = "club.manager@demo.oneclub.id"
	trialDemoMember  = "member@demo.oneclub.id"
)

func trialEngagementSetup(_ context.Context, t *Trial) error {
	crm := t.As(trialCRMAdmin)
	r := t.Rand("loyalty")
	for _, m := range t.Members() {
		if r.IntN(10) < 7 {
			crm.Post("/api/v1/crm/loyalty/accounts", J{"customerId": m.CustomerID.String()})
		}
	}
	for _, s := range []J{
		{"code": "TRL-GOLFERS", "name": "Active golfers (90 days)", "segmentType": "dynamic", "status": "active",
			"rules": J{"programs": []string{"golf"}, "minVisits": 1, "lookbackDays": 90}},
		{"code": "TRL-MEMBERS", "name": "All members", "segmentType": "dynamic", "status": "active", "rules": J{"customerTypes": []string{"member"}}},
		{"code": "TRL-VISITORS", "name": "Visitors (non-members)", "segmentType": "dynamic", "status": "active", "rules": J{"nonMembers": true, "lookbackDays": 90}},
	} {
		crm.Post("/api/v1/crm/segments", s)
	}
	return nil
}

// trialCampaigns are the monthly campaigns (segment, channel, subject).
var trialCampaigns = [][3]string{
	{"TRL-MEMBERS", "email", "Jadwal turnamen & promo member bulan ini"},
	{"TRL-GOLFERS", "email", "Weekday golf: green fee spesial"},
	{"TRL-VISITORS", "whatsapp", "Ajak keluarga: Family Day di club"},
}

func trialEngagementDay(ctx context.Context, t *Trial, day time.Time) error {
	r := t.Rand("engagement:" + day.Format(time.DateOnly))
	// the night jobs of the worker (loyalty expiry & tiers; the Top Spender
	// snapshot of the previous month on the 1st)
	t.At(day, "00:20")
	_, err := t.App.Engage.Loyalty.RunDaily(ctx)
	t.check(err)
	if day.Day() == 1 {
		t.At(day, "02:40")
		t.check((&topspender.SnapshotWorker{S: t.App.Engage.TopSpender}).Work(ctx, nil))
	}
	// yesterday's guests answer part of their surveys (e-mail link)
	t.At(day, "10:00")
	trialAnswerSurveys(ctx, t, day)
	// complaints at the front desk; the managers work the open tickets
	t.At(day, "11:00")
	trialTickets(t, r, day)
	// members redeem rewards with their points (Sunday at the clubhouse)
	if day.Weekday() == time.Sunday {
		t.At(day, "12:30")
		trialRedeemRewards(t, r)
	}
	// the Member App relationship NPS of the demo member (monthly)
	if day.Day() == 15 {
		t.As(trialDemoMember).Post("/api/v1/member/nps", J{"score": 7 + r.IntN(4), "businessLine": "golf", "comment": "Course in great shape"})
	}
	// the monthly campaign
	if day.Day() == 5 {
		t.At(day, "09:00")
		trialCampaign(ctx, t, day)
	}
	return nil
}

// trialAnswerSurveys answers ~40% of the surveys sent yesterday.
func trialAnswerSurveys(ctx context.Context, t *Trial, day time.Time) {
	type invite struct{ ID, Token, Context string }
	var invites []invite
	from := time.Date(day.Year(), day.Month(), day.Day()-1, 0, 0, 0, 0, t.Loc).AddDate(0, 0, 14)
	t.check(t.App.DB.WithReadTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		// trialRawSQL: the survey links of the e-mails (the guest clicks them);
		// a survey expires 14 days after it is sent (simulated clock)
		rows, err := tx.Query(ctx, `SELECT id::text, token, context_type FROM crm.feedback_requests WHERE property_id = $1 AND status = 'sent'
			AND expires_at >= $2 AND expires_at < $3 ORDER BY id`, t.Property, from, from.AddDate(0, 0, 1))
		if err != nil {
			return err
		}
		for rows.Next() {
			var v invite
			if err := rows.Scan(&v.ID, &v.Token, &v.Context); err != nil {
				rows.Close()
				return err
			}
			invites = append(invites, v)
		}
		rows.Close()
		return rows.Err()
	}))
	pub, mgr := t.Public(), t.As(trialClubManager)
	comments := map[int]string{1: "Slow play, waited at every tee", 2: "Caddy was late and the cart battery was weak", 3: "OK, greens a bit slow",
		4: "Great round, friendly caddy", 5: "Excellent course condition and service"}
	for _, v := range invites {
		r := t.Rand("survey:" + v.ID)
		if r.IntN(10) >= 4 {
			continue
		}
		rating := []int{2, 3, 4, 4, 4, 5, 5, 5, 5, 1}[r.IntN(10)]
		nps := []int{0, 3, 6, 7, 8, 9, 10}[min(6, rating+r.IntN(3))]
		body := J{"rating": rating, "nps": nps, "comment": comments[rating]}
		if v.Context == "round" {
			body["subjectRating"], body["subjectComment"] = min(5, rating+r.IntN(2)), "Caddy"
		}
		fb := pub.Post("/api/v1/public/feedback/"+v.Token, body)
		if rating <= 2 {
			mgr.Post("/api/v1/crm/feedback/"+fb.S("id")+":open-ticket", J{"priority": "high"})
		}
	}
}

// trialTickets files new complaints and works the open ones.
func trialTickets(t *Trial, r *rand.Rand, day time.Time) {
	desk, mgr := t.As(trialFrontDesk), t.As(trialClubManager)
	cats := t.ids("ticket-category", "/api/v1/crm/ticket-categories?limit=50", trialFrontDesk)
	if r.IntN(3) == 0 {
		subjects := [][3]string{{"FNB", "Cold food at the restaurant", "The soup and the main course came cold at lunch."},
			{"FACILITY", "Locker room cleanliness", "The shower in the men's locker room was not cleaned."},
			{"COURSE", "Bunker on hole 7 not raked", "Bunkers on the back nine were not raked this morning."},
			{"BILLING", "Double charge on my statement", "My green fee appears twice on the member statement."},
			{"SERVICE", "Long wait at the starter", "We waited 25 minutes at the first tee after our tee time."}}
		s := subjects[r.IntN(len(subjects))]
		body := J{"categoryId": cats(s[0]), "subject": s[1], "description": s[2], "channel": []string{"staff", "whatsapp", "email", "phone"}[r.IntN(4)]}
		if ms := t.MembersOn(day); len(ms) > 0 && r.IntN(3) > 0 {
			body["customerId"] = ms[r.IntN(len(ms))].CustomerID.String()
		} else {
			p := trialPersonOf(r, 40000+r.IntN(5000), "M")
			body["contactName"], body["contactPhone"] = p.Name, p.Phone
		}
		desk.Post("/api/v1/crm/tickets", body)
	}
	for _, tk := range mgr.Items("/api/v1/crm/tickets?limit=200&filter[status]=open,in_progress,escalated") {
		if t.Rand("ticket:"+tk.S("id")+day.Format(time.DateOnly)).IntN(3) > 0 { // else tomorrow
			mgr.Post("/api/v1/crm/tickets/"+tk.S("id")+":comment", J{"body": "Called the guest, apologised and followed up with the team"})
			mgr.Post("/api/v1/crm/tickets/"+tk.S("id")+":resolve", J{"resolution": "Followed up with the department; the guest accepted the apology"})
		}
	}
	for _, tk := range mgr.Items("/api/v1/crm/tickets?limit=200&filter[status]=resolved") {
		if t.Rand("close:"+tk.S("id")+day.Format(time.DateOnly)).IntN(2) == 0 {
			mgr.Post("/api/v1/crm/tickets/"+tk.S("id")+":close", J{"reason": "Guest confirmed"})
		}
	}
}

// trialRedeemRewards: members with enough points take a coffee or a range
// bucket at the clubhouse.
func trialRedeemRewards(t *Trial, r *rand.Rand) {
	cashier := t.As(trialCashier)
	rewards := t.ids("reward", "/api/v1/crm/loyalty/rewards?limit=50", trialCRMAdmin)
	for _, a := range t.As(trialCRMAdmin).Items("/api/v1/crm/loyalty/accounts?limit=500") {
		bal := int(a.N("balance"))
		if bal < 300 || r.IntN(3) != 0 {
			continue
		}
		code := "RW-COFFEE"
		if bal >= 500 && r.IntN(2) == 0 {
			code = "RW-BALLS"
		}
		red := cashier.Post("/api/v1/crm/loyalty/accounts/"+a.S("id")+":redeem-reward", J{"rewardId": rewards(code), "note": "At the clubhouse"})
		if id := red.S("id"); id != "" && red.S("status") != "completed" {
			cashier.Post("/api/v1/crm/loyalty/redemptions/"+id+":complete", J{"note": "Handed over"})
		}
	}
}

// trialCampaign sends the campaign of the month.
func trialCampaign(ctx context.Context, t *Trial, day time.Time) {
	crm := t.As(trialCRMAdmin)
	c := trialCampaigns[int(day.Month())%len(trialCampaigns)]
	seg := t.ids("segment", "/api/v1/crm/segments?limit=100", trialCRMAdmin)(c[0])
	crm.Post("/api/v1/crm/segments/"+seg+":compute", nil)
	code := "TRL-CMP-" + day.Format("200601")
	cmp := crm.Post("/api/v1/crm/campaigns", J{"code": code, "name": c[2], "segmentId": seg, "channel": c[1], "subject": c[2],
		"body": "Halo {{.name}}, " + c[2] + ". Info & booking: {{.link}}", "targetUrl": "https://club.example/promo"})
	crm.Post("/api/v1/crm/campaigns/"+cmp.S("id")+":schedule", J{})
	t.ApprovePending(ctx)
	_, err := t.App.Engage.Service.DispatchDue(ctx)
	t.check(err)
}
