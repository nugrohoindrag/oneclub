package app

// Trial dataset: CRM sales. At go-live the sales team, the commission
// scheme and the monthly sales targets are set up. Leads come in every day
// (WhatsApp, Instagram, the website form, walk-ins, member referrals);
// sales call them, qualify or disqualify, convert the good ones into
// opportunities, send quotations (corporate golf days, meetings,
// tournaments) and win or lose them; accepted quotations get their payment
// schedule, the customers pay the down payment and the balance, which
// accrues the sales commission; every month the commission statements are
// generated, approved and paid.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func init() {
	registerTrialSeeder(trialSeeder{Name: "sales", Order: 60, Setup: trialSalesSetup, Day: trialSalesDay})
}

const (
	trialSales     = "sales@demo.oneclub.id"
	trialMarketing = "marketing@demo.oneclub.id"
)

func trialSalesSetup(ctx context.Context, t *Trial) error {
	admin := t.Admin()
	var sales, banquetSales string
	for _, u := range []struct {
		email string
		dst   *string
	}{{trialSales, &sales}, {trialBanquetSales, &banquetSales}} {
		*u.dst = t.userID(ctx, u.email)
	}
	team := admin.Post("/api/v1/crm/sales-teams", J{"code": "SALES", "name": "Sales & Events Team"}).S("id")
	admin.Post("/api/v1/crm/sales-team-members", J{"teamId": team, "userId": sales})
	admin.Post("/api/v1/crm/sales-team-members", J{"teamId": team, "userId": banquetSales})
	admin.Post("/api/v1/crm/commission-schemes", J{"code": "COMM-2026", "name": "Sales commission 2026", "schemeType": "percent", "basis": "net",
		"rates":         []J{{"line": "golf", "percent": "5"}, {"line": "mice", "percent": "3"}, {"line": "tournament", "percent": "4"}, {"line": "wedding", "percent": "2"}},
		"effectiveFrom": t.Start.AddDate(0, 0, -30).Format(time.DateOnly), "clawbackDays": 90})
	for m := time.Date(t.Start.Year(), t.Start.Month(), 1, 0, 0, 0, 0, time.UTC); !m.After(t.Today.AddDate(0, 1, 0)); m = m.AddDate(0, 1, 0) {
		for _, x := range []struct {
			user, line, revenue string
			deals               int
		}{{sales, "golf", "250000000", 4}, {sales, "mice", "150000000", 3}, {banquetSales, "wedding", "400000000", 3}} {
			admin.Post("/api/v1/crm/sales-targets", J{"userId": x.user, "line": x.line, "periodType": "month", "periodStart": m.Format(time.DateOnly),
				"periodEnd": m.AddDate(0, 1, -1).Format(time.DateOnly), "targetRevenue": x.revenue, "targetDeals": x.deals})
		}
	}
	return nil
}

// userID returns the id of a user (by e-mail).
func (t *Trial) userID(ctx context.Context, email string) string {
	if v := t.Ref("user:" + email); v != "" {
		return v
	}
	var id string
	t.check(t.App.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text FROM platform.users WHERE email = $1`, email).Scan(&id)
	}))
	t.SetRef("user:"+email, id)
	return id
}

// trialDayTag marks a record with its simulated day (in its notes).
func trialDayTag(day time.Time) string { return "[d:" + day.Format(time.DateOnly) + "]" }

// trialTaggedDay reads the simulated day of a record ("" when untagged).
func trialTaggedDay(notes string) string {
	if i := strings.Index(notes, "[d:"); i >= 0 && len(notes) >= i+13 {
		return notes[i+3 : i+13]
	}
	return ""
}

func trialSalesDay(ctx context.Context, t *Trial, day time.Time) error {
	r := t.Rand("sales:" + day.Format(time.DateOnly))
	ds := day.Format(time.DateOnly)
	ago := func(tag string, n int) bool { return tag != "" && tag <= day.AddDate(0, 0, -n).Format(time.DateOnly) }
	mk, sx, cashier := t.As("crm.admin@demo.oneclub.id"), t.As(trialSales), t.As(trialCashier)
	t.At(day, "09:30")
	// new leads
	for range r.IntN(3) {
		p := trialPersonOf(r, 30000+r.IntN(5000), []string{"M", "F"}[r.IntN(2)])
		line := []string{"golf", "golf", "mice", "tournament", "wedding"}[r.IntN(5)]
		owner := t.userID(ctx, trialSales)
		if line == "wedding" {
			owner = t.userID(ctx, trialBanquetSales)
		}
		body := J{"name": p.Name, "phone": p.Phone, "email": p.Email, "source": []string{"whatsapp", "instagram", "website_form", "walk_in", "member_referral",
			"phone"}[r.IntN(6)], "line": line, "pax": 20 + 10*r.IntN(8), "eventDate": day.AddDate(0, 0, 20+r.IntN(40)).Format(time.DateOnly),
			"budget": fmt.Sprint(25000000 + 5000000*r.IntN(12)), "marketingConsent": r.IntN(3) > 0, "ownerUserId": owner, "duplicateAcknowledged": true,
			"notes": "Trial lead " + trialDayTag(day)}
		if line != "wedding" {
			body["companyName"] = trialCorporates[r.IntN(len(trialCorporates))][0]
		}
		if body["source"] == "member_referral" {
			if ms := t.MembersOn(day); len(ms) > 0 {
				body["referrerCustomerId"] = ms[r.IntN(len(ms))].CustomerID
			}
		}
		mk.Post("/api/v1/crm/leads", body)
	}
	// follow up the leads of the previous days
	for _, l := range sx.Items("/api/v1/crm/leads?limit=200&filter[status]=new") {
		if tag := trialTaggedDay(l.S("notes")); ago(tag, 1) {
			trialOwner(t, l).Post("/api/v1/crm/leads/"+l.S("id")+"/activities", J{"type": "call", "direction": "outbound", "subject": "First call",
				"notes": "Explained the packages"})
		}
	}
	for _, l := range sx.Items("/api/v1/crm/leads?limit=200&filter[status]=contacted") {
		tag := trialTaggedDay(l.S("notes"))
		if !ago(tag, 3) {
			continue
		}
		c := trialOwner(t, l)
		if t.Rand("lead:"+l.S("id")).IntN(5) == 0 {
			c.Post("/api/v1/crm/leads/"+l.S("id")+":disqualify", J{"reason": []string{"budget", "no_response", "date_unavailable", "competitor"}[r.IntN(4)]})
			continue
		}
		c.Post("/api/v1/crm/leads/"+l.S("id")+":qualify", J{"note": "Budget and date confirmed"})
		c.Post("/api/v1/crm/leads/"+l.S("id")+":convert", J{"title": strings.ToUpper(l.S("line")[:1]) + l.S("line")[1:] + " · " + l.S("name"),
			"expectedCloseDate": day.AddDate(0, 0, 14).Format(time.DateOnly)})
	}
	// proposals: quotation for the new opportunities
	stages := map[string]map[string]string{}
	for _, o := range sx.Items("/api/v1/crm/opportunities?limit=200&filter[status]=open") {
		if o.S("line") == "wedding" || strings.Contains(o.S("notes"), "[quoted]") {
			continue
		}
		pid := o.S("pipelineId")
		if stages[pid] == nil {
			stages[pid] = map[string]string{}
			for _, s := range sx.Items("/api/v1/crm/pipeline-stages?filter[pipelineId]=" + pid + "&limit=50") {
				stages[pid][s.S("code")] = s.S("id")
			}
		}
		c := trialOwner(t, o)
		if st := stages[pid]["PROPOSAL"]; st != "" {
			c.Post("/api/v1/crm/opportunities/"+o.S("id")+":move-stage", J{"stageId": st, "note": "Quotation prepared"})
		}
		pax := int(o.N("pax"))
		if pax <= 0 {
			pax = 30
		}
		lines := []J{{"itemType": "service", "description": "Green fee weekday (corporate)", "quantity": fmt.Sprint(pax), "unitPrice": "1250000"},
			{"itemType": "service", "description": "Buffet lunch", "quantity": fmt.Sprint(pax), "unitPrice": "185000"}}
		if o.S("line") == "mice" {
			lines = []J{{"itemType": "service", "description": "Full day meeting package", "quantity": fmt.Sprint(pax), "unitPrice": "385000"},
				{"itemType": "service", "description": "Meeting room rental", "quantity": "1", "unitPrice": "5000000"}}
		}
		q := c.Post("/api/v1/crm/quotations", J{"opportunityId": o.S("id"), "pricingMode": "nett", "notes": "Trial quotation " + trialDayTag(day),
			"paymentTerms": []J{{"label": "Down Payment 50%", "percent": "50", "dueDays": 3}, {"label": "Final Payment", "percent": "50", "dueDays": 14}}, "lines": lines})
		c.Post("/api/v1/crm/quotations/"+q.S("id")+":send", J{"channels": []string{"email"}, "message": "Terlampir penawaran kami"})
		c.Patch("/api/v1/crm/opportunities/"+o.S("id"), J{"notes": o.S("notes") + " [quoted]"})
	}
	// decisions on the quotations sent a few days ago
	for _, q := range sx.Items("/api/v1/crm/quotations?limit=200&filter[status]=sent") {
		tag := ""
		if at, err := time.Parse(time.RFC3339Nano, q.S("createdAt")); err == nil { // re-dated to the simulated day
			tag = at.In(t.Loc).Format(time.DateOnly)
		}
		if !ago(tag, 4) {
			continue
		}
		c := trialOwner(t, q)
		switch x := t.Rand("quote:" + q.S("id")).IntN(10); {
		case x < 7:
			c.Post("/api/v1/crm/quotations/"+q.S("id")+":accept", J{"acceptedByName": q.S("customerName"), "note": "Signed quotation received by e-mail"})
		case x < 9:
			c.Post("/api/v1/crm/quotations/"+q.S("id")+":reject", J{"reason": "Chose another venue"})
			if oid := q.S("opportunityId"); oid != "" {
				c.Post("/api/v1/crm/opportunities/"+oid+":lose", J{"reason": []string{"price", "competitor", "budget"}[x%3]})
			}
		}
	}
	// the customers pay the quotation schedules when due
	t.At(day, "14:00")
	for _, s := range cashier.Items("/api/v1/billing/payment-schedules?filter[sourceType]=quotation&limit=200") {
		if s.S("status") == "paid" || s.S("status") == "cancelled" {
			continue
		}
		sd := cashier.Get("/api/v1/billing/payment-schedules/" + s.S("id"))
		for _, ln := range sd.A("lines") {
			if ln.S("status") == "paid" || ln.S("dueDate") > ds {
				continue
			}
			cashier.Post("/api/v1/billing/payment-schedules/"+s.S("id")+"/lines/"+ln.S("id")+":pay", J{"methodType": "bank_transfer",
				"reference": "TRF-" + s.S("number")})
		}
	}
	// commission statements of the previous month
	if day.Day() == 2 {
		trialCommissionStatements(ctx, t, day.AddDate(0, -1, 0).Format("2006-01"))
	}
	return nil
}

// trialOwner is a client of the record's owner (the sales executive or
// the banquet sales).
func trialOwner(t *Trial, rec J) *TrialClient {
	if owner := rec.S("ownerUserId"); owner != "" && owner == t.userID(context.Background(), trialBanquetSales) {
		return t.As(trialBanquetSales)
	}
	return t.As(trialSales)
}

func trialCommissionStatements(ctx context.Context, t *Trial, period string) {
	fin := t.As(trialFinance)
	for _, st := range fin.Post("/api/v1/crm/commission-statements:generate", J{"period": period}).Items() {
		fin.Post("/api/v1/crm/commission-statements/"+st.S("id")+":submit", nil)
		t.ApprovePending(ctx)
		if s := fin.Get("/api/v1/crm/commission-statements/" + st.S("id")); s.S("status") == "approved" {
			fin.Post("/api/v1/crm/commission-statements/"+st.S("id")+":mark-paid", J{"reference": "PAYROLL-" + period})
		}
	}
}
