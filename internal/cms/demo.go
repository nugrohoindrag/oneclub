package cms

// Demo website content of the main property (Naming Convention §26 public
// navigation in Bahasa Indonesia and English): published pages with data
// blocks, header and footer menus, a home banner, news, contact information,
// course guide texts and the Website Content Publication approval workflow
// (Marketing Staff submit, the General Manager approves — PRD P4 open
// question #15 names a Marketing Manager, a role not among the templates).

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/id"
)

type demoPage struct {
	key, template, slug string
	title               [2]string // id, en
	summary             [2]string
	blocks              []CmsBlock
}

func tx2(idText, enText string) map[string]map[string]any {
	return map[string]map[string]any{"id": {"html": "<p>" + idText + "</p>"}, "en": {"html": "<p>" + enText + "</p>"}}
}

func heading(idText, enText string) map[string]map[string]any {
	return map[string]map[string]any{"id": {"heading": idText}, "en": {"heading": enText}}
}

func dataBlock(source string, filter map[string]any, limit int, h [2]string) CmsBlock {
	cfg := map[string]any{"source": source, "layout": "grid"}
	if filter != nil {
		cfg["filter"] = filter
	}
	if limit > 0 {
		cfg["limit"] = limit
	}
	return CmsBlock{ID: uuid.NewString(), Type: "data", Config: cfg, Content: heading(h[0], h[1])}
}

func text(idText, enText string) CmsBlock {
	return CmsBlock{ID: uuid.NewString(), Type: "rich_text", Content: tx2(idText, enText)}
}

func cta(route string, label [2]string) CmsBlock {
	return CmsBlock{ID: uuid.NewString(), Type: "cta", Config: map[string]any{"style": "primary", "link": map[string]any{"type": "route", "routeKey": route}},
		Content: map[string]map[string]any{"id": {"label": label[0]}, "en": {"label": label[1]}}}
}

var demoPages = []demoPage{
	{"home", "home", "home", [2]string{"Beranda", "Home"}, [2]string{"Modern Golf & Country Club, Tangerang", "Modern Golf & Country Club, Tangerang"}, []CmsBlock{
		{ID: uuid.NewString(), Type: "banner_slot", Config: map[string]any{"placement": "home_hero"}},
		text("Selamat datang di Modern Golf & Country Club: 18 hole, sport club, bungalow dan venue acara.",
			"Welcome to Modern Golf & Country Club: 18 holes, a sport club, bungalows and event venues."),
		dataBlock("promotions", nil, 3, [2]string{"Promosi", "What's On"}),
		dataBlock("events", nil, 3, [2]string{"Acara", "Events"}),
		dataBlock("news", nil, 3, [2]string{"Berita", "News"}),
		cta("book_golf", [2]string{"Pesan Tee Time", "Book a Tee Time"}),
	}},
	{"golf", "golf", "golf", [2]string{"Golf", "Golf"}, [2]string{"Lapangan 18 hole", "An 18-hole championship course"}, []CmsBlock{
		text("Dua lapangan, East dan West, dengan driving range dan pro shop.", "Two courses, East and West, with a driving range and pro shop."),
		dataBlock("rates", map[string]any{"line": "golf"}, 0, [2]string{"Tarif Green Fee", "Green Fee Rates"}),
		dataBlock("course_guide", nil, 18, [2]string{"Panduan Lapangan", "Course Guide"}),
		cta("book_golf", [2]string{"Pesan Golf", "Book Golf"}),
	}},
	{"sport_club", "sport_club", "sport-club", [2]string{"Sport Club", "Sport Club"}, [2]string{"Kolam renang, tenis, gym dan lainnya", "Pool, tennis, gym and more"}, []CmsBlock{
		dataBlock("sport_club", nil, 0, [2]string{"Fasilitas", "Facilities"}),
		dataBlock("rates", map[string]any{"line": "sportclub"}, 0, [2]string{"Tarif", "Rates"}),
		cta("book_sport_club", [2]string{"Pesan Lapangan", "Book a Court"}),
	}},
	{"bungalow", "bungalow", "bungalow", [2]string{"Bungalow", "Bungalow"}, [2]string{"Menginap di tepi lapangan", "Stay by the fairway"}, []CmsBlock{
		dataBlock("stay_units", nil, 0, [2]string{"Tipe Bungalow", "Bungalow Types"}),
		dataBlock("rates", map[string]any{"line": "stay"}, 0, [2]string{"Tarif", "Rates"}),
		dataBlock("availability", map[string]any{"resourceType": "bungalow"}, 0, [2]string{"Ketersediaan", "Availability"}),
	}},
	{"vip_suite", "vip_suite", "vip-suite", [2]string{"VIP Suite", "VIP Suite"}, [2]string{"Ruang privat untuk tamu istimewa", "Private suites for special guests"}, []CmsBlock{
		text("VIP Suite dapat dipesan per blok waktu.", "VIP Suites are booked per time block."),
		cta("book_meeting_room", [2]string{"Ajukan Pemesanan", "Request a Booking"}),
	}},
	{"meeting_mice", "meeting_mice", "meeting", [2]string{"Meeting & MICE", "Meeting & MICE"}, [2]string{"Ruang rapat dan paket MICE", "Meeting rooms and MICE packages"}, []CmsBlock{
		dataBlock("rates", map[string]any{"line": "meeting"}, 0, [2]string{"Paket Meeting", "Meeting Packages"}),
		{ID: uuid.NewString(), Type: "contact_form", Config: map[string]any{"endpoint": "inquiry", "line": "mice"},
			Content: map[string]map[string]any{"id": {"heading": "Minta Penawaran"}, "en": {"heading": "Request a Quotation"}}},
	}},
	{"wedding_banquet", "wedding_banquet", "wedding-banquet", [2]string{"Pernikahan & Banquet", "Wedding & Banquet"}, [2]string{"", ""}, []CmsBlock{
		dataBlock("packages", map[string]any{"line": "wedding"}, 6, [2]string{"Paket Pernikahan", "Wedding Packages"}),
		{ID: uuid.NewString(), Type: "contact_form", Config: map[string]any{"endpoint": "inquiry", "line": "wedding"},
			Content: map[string]map[string]any{"id": {"heading": "Konsultasi Pernikahan"}, "en": {"heading": "Plan Your Wedding"}}},
	}},
	{"events", "events", "events", [2]string{"Acara", "Events"}, [2]string{"", ""}, []CmsBlock{
		dataBlock("events", nil, 12, [2]string{"Acara Mendatang", "Upcoming Events"}),
		dataBlock("tournaments", nil, 6, [2]string{"Turnamen", "Tournaments"}),
	}},
	{"membership", "membership", "membership", [2]string{"Keanggotaan", "Membership"}, [2]string{"", ""}, []CmsBlock{
		dataBlock("membership_types", nil, 0, [2]string{"Jenis Keanggotaan", "Membership Types"}),
	}},
	{"packages", "packages", "packages", [2]string{"Paket", "Packages"}, [2]string{"", ""}, []CmsBlock{
		dataBlock("packages", nil, 12, [2]string{"Paket", "Packages"}),
	}},
	{"promotions", "promotions", "promotions", [2]string{"Promosi", "Promotions"}, [2]string{"", ""}, []CmsBlock{
		dataBlock("promotions", nil, 12, [2]string{"Promosi Berlaku", "Current Promotions"}),
	}},
	{"hall_of_fame", "hall_of_fame", "hall-of-fame", [2]string{"Hall of Fame", "Hall of Fame"}, [2]string{"", ""}, []CmsBlock{
		dataBlock("hall_of_fame", nil, 0, [2]string{"Hall of Fame", "Hall of Fame"}),
	}},
	{"news", "news", "news", [2]string{"Berita", "News"}, [2]string{"", ""}, []CmsBlock{
		dataBlock("news", nil, 12, [2]string{"Berita Terbaru", "Latest News"}),
	}},
	{"gallery", "gallery", "gallery", [2]string{"Galeri", "Gallery"}, [2]string{"", ""}, []CmsBlock{
		dataBlock("gallery", nil, 12, [2]string{"Album", "Albums"}),
	}},
	{"contact", "contact", "contact", [2]string{"Kontak", "Contact"}, [2]string{"", ""}, []CmsBlock{
		{ID: uuid.NewString(), Type: "contact_form", Config: map[string]any{"endpoint": "contact", "topic": "general"},
			Content: map[string]map[string]any{"id": {"heading": "Hubungi Kami", "submitLabel": "Kirim"}, "en": {"heading": "Contact Us", "submitLabel": "Send"}}},
	}},
	{"location", "location", "location", [2]string{"Lokasi", "Location"}, [2]string{"", ""}, []CmsBlock{
		{ID: uuid.NewString(), Type: "map", Config: map[string]any{"latitude": -6.1886, "longitude": 106.638, "zoom": 15},
			Content: heading("Lokasi Kami", "Find Us")},
	}},
}

// demoContent inserts a published content item (version 1).
func demoContent(ctx context.Context, tx pgx.Tx, property uuid.UUID, pol ContentPolicy, kind string, row settingsRow, doc CmsDocument, now time.Time) (uuid.UUID, error) {
	cid := id.New()
	hashes := langHashes(doc, pol.SupportedLanguages)
	rev := map[string]int{}
	for l := range hashes {
		rev[l] = 1
	}
	states := translationStates(row, kind, doc, rev, pol)
	if _, err := tx.Exec(ctx, `INSERT INTO cms.contents (id, property_id, kind, key, template, sort_order, show_in_sitemap, category_id, tags, author_name,
		featured, placement, link, title, translation_status, status, live, latest_version, approved_version, published_version, published_at,
		first_published_at) VALUES ($1,$2,$3,$4,$5,$6,true,$7,coalesce($8, '{}'::text[]),$9,$10,$11,$12,$13,$14,'published',true,1,1,1,$15,$15)`,
		cid, property, kind, row.Key, row.Template, row.SortOrder, row.CategoryID, row.Tags, row.AuthorName, row.Featured, row.Placement, row.Link,
		doc.Translations[pol.DefaultLanguage].Title, states, now); err != nil {
		return cid, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO cms.content_versions (id, property_id, content_id, version_no, document, lang_hash, lang_rev, note, source,
		review_status, approved_at, published_at) VALUES ($1,$2,$3,1,$4,$5,$6,'demo content','create','approved',$7,$7)`,
		id.New(), property, cid, doc, hashes, rev, now); err != nil {
		return cid, err
	}
	if scope := specOf(kind).SlugScope; scope != "" {
		for lang, t := range doc.Translations {
			if t.Slug == "" {
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO cms.content_slugs (property_id, scope, language, slug, content_id) VALUES ($1,$2,$3,$4,$5)`,
				property, scope, lang, t.Slug, cid); err != nil {
				return cid, err
			}
		}
	}
	_, err := tx.Exec(ctx, `INSERT INTO cms.publish_events (id, property_id, content_id, kind, action, version_no, actor_name, note) VALUES ($1,$2,$3,$4,'published',1,'oneclub seed-demo','demo content')`,
		id.New(), property, cid, kind)
	return cid, err
}

// SeedDemo seeds the demo website of a property (idempotent).
func SeedDemo(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cms.contents WHERE property_id = $1 AND kind = 'page' AND key = 'home')`, property).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	pol, err := Policy(ctx, tx, property)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	pages := map[string]uuid.UUID{}
	for i, p := range demoPages {
		key, tpl := p.key, p.template
		doc := CmsDocument{Translations: map[string]CmsTranslation{
			"id": {Title: p.title[0], Slug: p.slug, Summary: p.summary[0], SEO: &CmsSEO{MetaTitle: p.title[0] + " · Modern Golf & Country Club"}},
			"en": {Title: p.title[1], Slug: p.slug, Summary: p.summary[1], SEO: &CmsSEO{MetaTitle: p.title[1] + " · Modern Golf & Country Club"}},
		}, Blocks: p.blocks}
		cid, err := demoContent(ctx, tx, property, pol, KindPage, settingsRow{Key: &key, Template: &tpl, SortOrder: i * 10, ShowInSitemap: true}, doc, now)
		if err != nil {
			return fmt.Errorf("demo page %s: %w", p.key, err)
		}
		pages[p.key] = cid
	}
	// news categories and articles
	cats := map[string]uuid.UUID{}
	for i, c := range [][3]string{{"CLUB", "Berita Klub", "Club News"}, {"TOURNAMENT", "Turnamen", "Tournaments"}} {
		cid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO cms.categories (id, property_id, code, name, labels, sort_order) VALUES ($1,$2,$3,$4,$5,$6)`,
			cid, property, c[0], c[1], map[string]string{"en": c[2]}, i); err != nil {
			return err
		}
		cats[c[0]] = cid
	}
	author := "Marketing Modern Golf"
	for i, a := range []struct {
		cat       string
		slug      [2]string
		title     [2]string
		excerpt   [2]string
		body      [2]string
		featured  bool
		daysAgoTs int
	}{
		{"CLUB", [2]string{"renovasi-clubhouse-selesai", "clubhouse-renovation-completed"}, [2]string{"Renovasi Clubhouse Selesai", "Clubhouse Renovation Completed"},
			[2]string{"Clubhouse baru siap menyambut member.", "The new clubhouse welcomes members."},
			[2]string{"Restoran dan locker room kini lebih nyaman.", "The restaurant and locker rooms are now more comfortable."}, true, 3},
		{"TOURNAMENT", [2]string{"juara-club-championship-2026", "club-championship-2026-winners"}, [2]string{"Juara Club Championship 2026", "Club Championship 2026 Winners"},
			[2]string{"Selamat kepada para juara.", "Congratulations to the champions."},
			[2]string{"Hasil lengkap ada di Hall of Fame.", "Full results are in the Hall of Fame."}, false, 10},
	} {
		cat := cats[a.cat]
		doc := CmsDocument{Translations: map[string]CmsTranslation{
			"id": {Title: a.title[0], Slug: a.slug[0], Summary: a.excerpt[0]},
			"en": {Title: a.title[1], Slug: a.slug[1], Summary: a.excerpt[1]},
		}, Blocks: []CmsBlock{text(a.body[0], a.body[1])}}
		at := now.AddDate(0, 0, -a.daysAgoTs)
		cid, err := demoContent(ctx, tx, property, pol, KindArticle, settingsRow{CategoryID: &cat, Tags: []string{"club"}, AuthorName: &author,
			Featured: a.featured, ShowInSitemap: true, SortOrder: i}, doc, at)
		if err != nil {
			return fmt.Errorf("demo article: %w", err)
		}
		_ = cid
	}
	// home hero banner
	bkey, placement := "home-hero-booking", "home_hero"
	if _, err := demoContent(ctx, tx, property, pol, KindBanner, settingsRow{Key: &bkey, Placement: &placement,
		Link: &CmsLink{Type: "route", RouteKey: "book_golf"}}, CmsDocument{Translations: map[string]CmsTranslation{
		"id": {Title: "Main Golf di Tangerang", Summary: "Pesan tee time online, bayar dengan QRIS.", ButtonLabel: "Pesan Sekarang"},
		"en": {Title: "Play Golf in Tangerang", Summary: "Book your tee time online and pay with QRIS.", ButtonLabel: "Book Now"},
	}}, now); err != nil {
		return fmt.Errorf("demo banner: %w", err)
	}
	// navigation (Naming Convention §26)
	nav := func(page, routeKey, idLabel, enLabel string) CmsMenuItem {
		it := CmsMenuItem{ID: uuid.NewString(), Label: map[string]string{"id": idLabel, "en": enLabel}}
		if pid, ok := pages[page]; ok && page != "" {
			it.Type, it.PageID = "page", pid.String()
		} else {
			it.Type, it.RouteKey = "route", routeKey
		}
		return it
	}
	header := []CmsMenuItem{nav("home", "home", "Beranda", "Home"), nav("golf", "golf", "Golf", "Golf"), nav("sport_club", "sport_club", "Sport Club", "Sport Club"),
		nav("bungalow", "bungalow", "Bungalow", "Bungalow"), nav("vip_suite", "vip_suite", "VIP Suite", "VIP Suite"),
		nav("meeting_mice", "meeting_mice", "Meeting & MICE", "Meeting & MICE"), nav("wedding_banquet", "wedding_banquet", "Pernikahan & Banquet", "Wedding & Banquet"),
		nav("events", "events", "Acara", "Events"), nav("membership", "membership", "Keanggotaan", "Membership"), nav("packages", "packages", "Paket", "Packages"),
		nav("promotions", "promotions", "Promosi", "Promotions"), nav("hall_of_fame", "hall_of_fame", "Hall of Fame", "Hall of Fame"),
		nav("news", "news", "Berita", "News"), nav("gallery", "gallery", "Galeri", "Gallery"), nav("contact", "contact", "Kontak", "Contact"),
		nav("location", "location", "Lokasi", "Location")}
	footer := []CmsMenuItem{nav("", "book_golf", "Pesan Golf", "Book Golf"), nav("", "book_sport_club", "Pesan Sport Club", "Book Sport Club"),
		nav("", "book_bungalow", "Pesan Bungalow", "Book Bungalow"), nav("", "book_meeting_room", "Pesan Ruang Meeting", "Book Meeting Room"),
		nav("", "book_event", "Pesan Acara", "Book Event"), nav("contact", "contact", "Kontak", "Contact"), nav("location", "location", "Lokasi", "Location")}
	for _, mn := range []struct {
		code, name, location string
		items                []CmsMenuItem
	}{{"HEADER", "Main navigation", "header", header}, {"FOOTER", "Footer", "footer", footer}} {
		if _, err := tx.Exec(ctx, `INSERT INTO cms.menus (id, property_id, code, name, location, items) VALUES ($1,$2,$3,$4,$5,$6)`,
			id.New(), property, mn.code, mn.name, mn.location, mn.items); err != nil {
			return err
		}
	}
	// contact information
	if _, err := tx.Exec(ctx, `INSERT INTO cms.contacts (id, property_id, code, name, is_primary, address, phones, whatsapp, email, latitude, longitude,
		opening_hours, social_links, translations) VALUES ($1,$2,'CLUB','Modern Golf & Country Club',true,
		'Jl. Modern Golf Raya, Modernland, Kota Tangerang, Banten',$3,'+62 811 0000 2026','info@demo.oneclub.id',-6.1886,106.638,$4,$5,$6)`,
		id.New(), property, []string{"+62 21 0000 2026"},
		[]map[string]any{{"day": "mon", "open": "06:00", "close": "21:00"}, {"day": "sat", "open": "05:30", "close": "21:00"},
			{"day": "sun", "open": "05:30", "close": "21:00"}},
		map[string]string{"instagram": "https://instagram.com/moderngolf.demo"},
		map[string]map[string]string{"en": {"note": "Reservations by WhatsApp are answered 07.00–20.00."},
			"id": {"note": "Reservasi WhatsApp dijawab pukul 07.00–20.00."}}); err != nil {
		return err
	}
	// course guide texts (EAST holes 1–3)
	for h := 1; h <= 3; h++ {
		if _, err := tx.Exec(ctx, `INSERT INTO cms.course_guides (id, property_id, course_code, hole_number, title, translations, sort_order) VALUES ($1,$2,'EAST',$3,$4,$5,$3)`,
			id.New(), property, h, fmt.Sprintf("Hole %d", h), map[string]map[string]string{
				"id": {"title": fmt.Sprintf("Hole %d", h), "description": "<p>Pukulan pertama ke tengah fairway.</p>"},
				"en": {"title": fmt.Sprintf("Hole %d", h), "description": "<p>Aim the tee shot at the centre of the fairway.</p>"}}); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO cms.site_state (property_id, revision, changed_at) VALUES ($1, 1, now()) ON CONFLICT (property_id) DO NOTHING`, property); err != nil {
		return err
	}
	// approval workflow: Website Content Publication approved by the General Manager at this property
	var wf bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.approval_workflows WHERE document_type = $1)`, ContentDocumentType.Code).Scan(&wf); err != nil {
		return err
	}
	if !wf {
		wid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO platform.approval_workflows (id, document_type, name, property_id) VALUES ($1,$2,$3,$4)`,
			wid, ContentDocumentType.Code, "Website Content Publication", property); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.approval_workflow_steps (id, workflow_id, step_no, name, approver_type, approver_role_id, conditions, sla_hours)
			SELECT $1, $2, 1, 'Marketing approval (General Manager)', 'role', r.id, '[]'::jsonb, 24 FROM platform.roles r WHERE r.code = 'general_manager'`,
			id.New(), wid); err != nil {
			return err
		}
	}
	return nil
}
