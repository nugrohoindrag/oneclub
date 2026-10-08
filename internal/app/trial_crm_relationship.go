package app

// Trial dataset: the relationship side of CRM for the CRM Dashboard Overview
// and Customer 360. At the current date the CRM Admin records what the
// relationship team learnt about the members (tee time, caddy, cart, food,
// drink, locker) and schedules their follow-ups: renewal calls for the
// memberships that end soon, welcome calls for new members, enquiries and
// event follow-ups — some due today, some overdue, the rest in the coming days.

import (
	"context"
	"fmt"
	"sort"
	"time"

	"oneclub/internal/kernel/clock"
)

func init() {
	registerTrialSeeder(trialSeeder{Name: "crm-relationship", Order: 76, Final: trialCRMRelationshipFinal})
}

// trialPreferences are the member preferences per category (one is picked per member).
var trialPreferences = []struct {
	Category string
	Values   []string
}{
	{"tee_time", []string{"Weekend 06:00–07:00, front nine", "Weekday 06:30, back nine start", "Saturday early flight with the regular four-ball", "Afternoon 14:00 after office"}},
	{"golf_cart", []string{"Single rider", "Shared cart with the spouse", "Walks the course, push cart", "Cart with GPS"}},
	{"favorite_caddy", []string{"Caddy C001 (Siti)", "Caddy C004", "Senior caddy who reads the greens", "Same caddy as the last round"}},
	{"food", []string{"Nasi goreng kampung after the round", "Soto Betawi", "Light breakfast at the halfway house", "Grilled fish, no fried food"}},
	{"beverage", []string{"Iced lemon tea", "Hot black coffee, no sugar", "Coconut water at the turn", "Isotonic drink on the course"}},
	{"facility", []string{"Locker near the entrance", "Spa after the round", "Driving range bucket before tee off", "Private dining room for guests"}},
}

func trialCRMRelationshipFinal(_ context.Context, t *Trial) error {
	crm := t.As(trialCRMAdmin)
	members := append([]trialMember(nil), t.Members()...)
	r := t.Rand("crm-relationship")

	// Preferences for 60 members, three or four categories each.
	n := 0
	for _, m := range members {
		if n == 60 {
			break
		}
		n++
		for _, i := range r.Perm(len(trialPreferences))[:3+r.IntN(2)] {
			p := trialPreferences[i]
			crm.Post(fmt.Sprintf("/api/v1/crm/customers/%s/preferences", m.CustomerID), J{"category": p.Category, "value": p.Values[r.IntN(len(p.Values))]})
		}
	}

	// Follow-ups of the CRM Admin: renewal calls for the memberships ending first.
	sort.SliceStable(members, func(i, j int) bool { return members[i].EndsOn.Before(members[j].EndsOn) })
	// Due dates count from the real date (the final step runs at the real time).
	now := clock.Now().In(t.Loc)
	due := func(days int, hhmm string) string {
		d := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, t.Loc).AddDate(0, 0, days)
		hh, mm := 9, 0
		_, _ = fmt.Sscanf(hhmm, "%d:%d", &hh, &mm)
		return time.Date(d.Year(), d.Month(), d.Day(), hh, mm, 0, 0, t.Loc).Format(time.RFC3339)
	}
	plan := []struct {
		Days       int
		At, Type   string
		Subject    string
		FromExpiry bool
	}{
		{0, "09:30", "call", "Renewal call — membership ends soon", true},
		{0, "11:00", "call", "Renewal call — membership ends soon", true},
		{0, "15:00", "whatsapp", "Send the renewal offer", true},
		{-1, "10:00", "call", "Renewal reminder — no answer yesterday", true},
		{-3, "14:00", "email", "Membership inquiry — family upgrade", false},
		{1, "10:00", "call", "Welcome call — first month as a member", false},
		{2, "09:00", "meeting", "Event follow-up — club championship registration", false},
		{3, "16:00", "call", "Renewal call — membership ends soon", true},
		{5, "10:30", "whatsapp", "Guest invitation — member referral", false},
		{7, "09:00", "call", "Win-back — no visit for a while", false},
		{9, "13:00", "email", "Corporate membership proposal", false},
		{12, "10:00", "call", "Renewal call — membership ends soon", true},
	}
	expiring, other := 0, len(members)-1
	for _, p := range plan {
		var m trialMember
		if p.FromExpiry {
			m, expiring = members[expiring], expiring+1
		} else {
			m, other = members[other], other-1
		}
		crm.Post("/api/v1/crm/follow-ups", J{"type": p.Type, "subject": p.Subject, "customerId": m.CustomerID, "direction": "outbound",
			"dueAt": due(p.Days, p.At), "notes": "Member " + m.No + " · " + m.Type})
	}
	return nil
}
