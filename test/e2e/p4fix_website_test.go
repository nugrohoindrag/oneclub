package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// p4fixNavMenu returns the public menu with the code of a location in a language.
func p4fixNavMenu(t *testing.T, c *Client, q, lang, location, code string) map[string]any {
	t.Helper()
	for _, mn := range c.Must(200, "GET", "/api/v1/public/cms/navigation"+q+"&lang="+lang+"&location="+location, nil).JSON()["menus"].([]any) {
		if m := mn.(map[string]any); m["code"] == code {
			return m
		}
	}
	t.Fatalf("menu %s (%s) not served in %s", code, location, lang)
	return nil
}

// p4fixNavShape renders a menu as "Label=href[Child=href|…]" entries.
func p4fixNavShape(items []any) []string {
	var out []string
	for _, x := range items {
		it := x.(map[string]any)
		s := str(it["label"]) + "=" + str(it["href"])
		if ch, _ := it["children"].([]any); len(ch) > 0 {
			s += "[" + strings.Join(p4fixNavShape(ch), "|") + "]"
		}
		out = append(out, s)
	}
	return out
}

// FR-CMS-09 + FR-WEB-P3-01 (P4 fix package H): the website header is the
// CMS header menu. The demo seeds the grouped PROPOSAL (one dropdown level,
// ID/EN labels, VIP Suite and Meeting & MICE linked to their pages, the
// Tournaments website route); the club edits menus in the CMS, and the
// sitemap lists the Tournaments route in both languages (FR-CMS-08).
func TestP4FixWebsiteNavigation(t *testing.T) {
	pub := anon(t, inst)
	mk := roleUser(t, inst, "marketing_staff")
	q := "?propertyId=" + inst.Main.String()
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)

	en := p4fixNavShape(p4fixNavMenu(t, pub, q, "en", "header", "HEADER")["items"].([]any))
	want := []string{
		"Golf=/en/golf[Golf=/en/golf|Tournaments=/en/tournaments|Hall of Fame=/en/hall-of-fame]",
		"Membership=/en/membership",
		"Facilities=/en/sport-club[Sport Club=/en/sport-club|Stay & Venue=/en/bungalow|VIP Suite=/en/vip-suite|Meeting & MICE=/en/meeting]",
		"Events & Wedding=/en/wedding-banquet[Wedding & Banquet=/en/wedding-banquet|Events=/en/events]",
		"Offers=/en/packages[Packages=/en/packages|Promotions=/en/promotions]",
		"News & Gallery=/en/news[News=/en/news|Gallery=/en/gallery]",
		"Contact=/en/contact[Contact=/en/contact|Location=/en/location]",
	}
	if strings.Join(en, "\n") != strings.Join(want, "\n") {
		t.Fatalf("English header menu:\n%s\nwant:\n%s", strings.Join(en, "\n"), strings.Join(want, "\n"))
	}
	id := p4fixNavShape(p4fixNavMenu(t, pub, q, "id", "header", "HEADER")["items"].([]any))
	if len(id) != len(want) || id[2] != "Fasilitas=/id/sport-club[Sport Club=/id/sport-club|Menginap & Venue=/id/bungalow|VIP Suite=/id/vip-suite|Meeting & MICE=/id/meeting]" ||
		!strings.HasPrefix(id[0], "Golf=/id/golf[Golf=/id/golf|Turnamen=/id/tournaments|") || !strings.HasPrefix(id[3], "Acara & Pernikahan=") {
		t.Fatalf("Indonesian header menu: %v", id)
	}
	// Every item carries the website shape: id, external / new-tab flags, children list.
	for _, x := range p4fixNavMenu(t, pub, q, "en", "header", "HEADER")["items"].([]any) {
		it := x.(map[string]any)
		if str(it["id"]) == "" || it["external"] != false || it["newTab"] != false || it["children"] == nil {
			t.Fatalf("navigation item shape: %v", it)
		}
	}

	// The club re-groups its navigation in the CMS: a dropdown with the new
	// Tournaments route, an external link only in English; unknown routes rejected.
	if r := mk.Do("POST", "/api/v1/cms/menus", map[string]any{"code": "NAVX" + sfx, "name": "Bad", "location": "sidebar", "items": []map[string]any{
		{"label": map[string]any{"id": "x"}, "type": "route", "routeKey": "tournament"}}}); r.Status != 422 || !strings.Contains(string(r.Body), "tournaments") {
		t.Fatalf("unknown route key (the message lists tournaments): %s", r)
	}
	menu := idOf(mk.Must(201, "POST", "/api/v1/cms/menus", map[string]any{"code": "NAV" + sfx, "name": "Sidebar", "location": "sidebar", "items": []map[string]any{
		{"label": map[string]any{"id": "Kompetisi", "en": "Competitions"}, "type": "route", "routeKey": "tournaments", "children": []map[string]any{
			{"label": map[string]any{"id": "Turnamen", "en": "Tournaments"}, "type": "route", "routeKey": "tournaments"},
			{"label": map[string]any{"id": "Hall of Fame"}, "type": "route", "routeKey": "hall_of_fame"},
			{"label": map[string]any{"id": "Federasi"}, "type": "url", "url": "https://pgi.or.id", "newTab": true, "languages": []string{"en"}}}}}}))
	mk.Must(200, "PATCH", "/api/v1/cms/menus/"+menu, map[string]any{"name": "Sidebar (competitions)"})
	if got := strings.Join(p4fixNavShape(p4fixNavMenu(t, pub, q, "en", "sidebar", "NAV"+sfx)["items"].([]any)), ";"); got !=
		"Competitions=/en/tournaments[Tournaments=/en/tournaments|Hall of Fame=/en/hall-of-fame|Federasi=https://pgi.or.id]" {
		t.Fatalf("edited menu in English: %s", got)
	}
	sb := p4fixNavMenu(t, pub, q, "id", "sidebar", "NAV"+sfx)["items"].([]any)[0].(map[string]any)
	if ch := sb["children"].([]any); len(ch) != 2 || sb["href"] != "/id/tournaments" {
		t.Fatalf("edited menu in Indonesian (external link English only): %v", sb)
	}
	en2 := p4fixNavMenu(t, pub, q, "en", "sidebar", "NAV"+sfx)["items"].([]any)[0].(map[string]any)["children"].([]any)[2].(map[string]any)
	if en2["external"] != true || en2["newTab"] != true {
		t.Fatalf("external link flags: %v", en2)
	}

	// Sitemap: the Tournaments route in both languages with hreflang alternates.
	sm := pub.Must(200, "GET", "/api/v1/public/cms/sitemap"+q, nil).JSON()["entries"].([]any)
	found := 0
	for _, e := range sm {
		m := e.(map[string]any)
		if p := str(m["path"]); p == "/en/tournaments" || p == "/id/tournaments" {
			if len(m["alternates"].([]any)) != 2 {
				t.Fatalf("sitemap alternates: %v", m)
			}
			found++
		}
	}
	if found != 2 {
		t.Fatalf("sitemap lacks the Tournaments route: %v", sm)
	}
}
