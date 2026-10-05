package cms

// Master data of the website managed through the generic resource engine
// (list / view / add / edit / delete-or-archive, CSV/XLSX export and import
// — the import also serves the EP-29 migration of the old website's URLs):
// News Categories, Navigation Menus (FR-CMS-09), Redirects, Contact
// Information and Course Guide texts (FR-CMS-04).

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

// Categories are the news categories.
var Categories = &resource.Def{
	Key: "cms.category", Module: "cms", Perm: "cms.category", Path: "/api/v1/cms/categories", Table: "cms.categories",
	Name: "News Category", Plural: "News Categories", Tag: "CMS", PropertyScoped: true, Archive: true, CodeField: "code",
	OrderBy: "sort_order, name, id", SchemaName: "CmsCategory",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "labels", Column: "labels", Label: "Name per language ({\"en\": \"Tournament\"})", Kind: resource.JSON, Default: "{}"},
		{Name: "sortOrder", Column: "sort_order", Label: "Sort Order", Kind: resource.Int, Default: int64(0)},
		resource.Status("active", "inactive")},
}

// Menus are the public navigation menus (header, footer …).
var Menus = &resource.Def{
	Key: "cms.menu", Module: "cms", Perm: "cms.menu", Path: "/api/v1/cms/menus", Table: "cms.menus",
	Name: "Navigation Menu", Plural: "Navigation Menus", Tag: "CMS", PropertyScoped: true, Archive: true, CodeField: "code",
	OrderBy: "location, code, id", SchemaName: "CmsMenu",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "location", Column: "location", Label: "Location", Kind: resource.Enum, Enum: []string{"header", "footer", "mobile", "sidebar", "legal"},
			Default: "header", Filter: true},
		{Name: "items", Column: "items", Label: "Items (tree: label per language, page / url / route link, children)", Kind: resource.JSONList, Default: "[]"},
		resource.Status("active", "inactive")},
}

// Redirects map old URLs to new ones (301 by default).
var Redirects = &resource.Def{
	Key: "cms.redirect", Module: "cms", Perm: "cms.redirect", Path: "/api/v1/cms/redirects", Table: "cms.redirects",
	Name: "Redirect", Plural: "Redirects", Tag: "CMS", PropertyScoped: true, Archive: true, CodeField: "fromPath",
	OrderBy: "from_path, id", SchemaName: "CmsRedirect",
	Fields: []resource.Field{
		{Name: "fromPath", Column: "from_path", Label: "Old URL (path)", Kind: resource.String, Required: true, Max: 500, Search: true},
		{Name: "toPath", Column: "to_path", Label: "New URL (path or https:// address)", Kind: resource.String, Required: true, Max: 1000, Search: true},
		{Name: "statusCode", Column: "status_code", Label: "Redirect Type", Kind: resource.Int, Enum: []string{"301", "302", "307", "308"}, Default: int64(301)},
		{Name: "source", Column: "source", Label: "Source", Kind: resource.Enum, Enum: []string{"manual", "auto", "import"}, ReadOnly: true, Filter: true},
		{Name: "note", Column: "note", Label: "Note", Kind: resource.Text, Max: 500},
		resource.Status("active", "inactive")},
}

// Contacts is the Contact Information of the club (one or more points of contact).
var Contacts = &resource.Def{
	Key: "cms.contact", Module: "cms", Perm: "cms.contact", Path: "/api/v1/cms/contacts", Table: "cms.contacts",
	Name: "Contact Information", Plural: "Contact Information", Tag: "CMS", PropertyScoped: true, Archive: true, CodeField: "code",
	OrderBy: "is_primary DESC, sort_order, name, id", SchemaName: "CmsContact",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "isPrimary", Column: "is_primary", Label: "Primary contact", Kind: resource.Bool, Default: false},
		{Name: "address", Column: "address", Label: "Address", Kind: resource.Text, Max: 1000},
		{Name: "phones", Column: "phones", Label: "Phone numbers", Kind: resource.StringList, Default: []string{}},
		{Name: "whatsapp", Column: "whatsapp", Label: "WhatsApp number", Kind: resource.String, Max: 30},
		{Name: "email", Column: "email", Label: "E-mail", Kind: resource.Email, Max: 200},
		{Name: "latitude", Column: "latitude", Label: "Latitude", Kind: resource.Decimal, Min: resource.Min(-90), MaxN: resource.Max(90)},
		{Name: "longitude", Column: "longitude", Label: "Longitude", Kind: resource.Decimal, Min: resource.Min(-180), MaxN: resource.Max(180)},
		{Name: "mapUrl", Column: "map_url", Label: "Map link", Kind: resource.String, Max: 1000},
		{Name: "openingHours", Column: "opening_hours", Label: "Opening hours ([{day, open, close, closed}])", Kind: resource.JSONList, Default: "[]"},
		{Name: "socialLinks", Column: "social_links", Label: "Social media links ({instagram: url, …})", Kind: resource.JSON, Default: "{}"},
		{Name: "translations", Column: "translations", Label: "Translations ({en: {name, address, note}})", Kind: resource.JSON, Default: "{}"},
		{Name: "sortOrder", Column: "sort_order", Label: "Sort Order", Kind: resource.Int, Default: int64(0)},
		resource.Status("active", "inactive")},
}

// CourseGuides are the Course Guide texts per hole.
var CourseGuides = &resource.Def{
	Key: "cms.course_guide", Module: "cms", Perm: "cms.course_guide", Path: "/api/v1/cms/course-guides", Table: "cms.course_guides",
	Name: "Course Guide Hole", Plural: "Course Guide", Tag: "CMS", PropertyScoped: true, Archive: true,
	OrderBy: "course_code, hole_number, id", SchemaName: "CmsCourseGuideHole",
	Fields: []resource.Field{
		{Name: "courseCode", Column: "course_code", Label: "Course (code)", Kind: resource.String, Required: true, Max: 40, Upper: true, CreateOnly: true,
			Filter: true, Search: true},
		{Name: "holeNumber", Column: "hole_number", Label: "Hole", Kind: resource.Int, Required: true, Min: resource.Min(1), MaxN: resource.Max(36), CreateOnly: true},
		{Name: "title", Column: "title", Label: "Title (default language)", Kind: resource.String, Max: 160, Search: true},
		{Name: "translations", Column: "translations", Label: "Text per language ({id: {title, description, tips}})", Kind: resource.JSON, Default: "{}"},
		{Name: "mediaIds", Column: "media_ids", Label: "Images (media library ids)", Kind: resource.StringList, Default: []string{}},
		{Name: "sortOrder", Column: "sort_order", Label: "Sort Order", Kind: resource.Int, Default: int64(0)},
		resource.Status("active", "inactive")},
}

func resourceDefs() []*resource.Def {
	return []*resource.Def{Categories, Menus, Redirects, Contacts, CourseGuides}
}

// resourceHooks validates the JSON parts of the website master data and
// bumps the website revision after every change.
func (m *Module) resourceHooks() {
	after := resource.Hooks{}
	touch := func(ctx context.Context, tx pgx.Tx) error {
		pid, _ := reqctx.Property(ctx)
		return m.touchSite(ctx, tx, pid)
	}
	after.AfterCreate = func(ctx context.Context, tx pgx.Tx, _ map[string]any) error { return touch(ctx, tx) }
	after.AfterUpdate = func(ctx context.Context, tx pgx.Tx, _, _ map[string]any) error { return touch(ctx, tx) }
	with := func(before func(ctx context.Context, tx pgx.Tx, v map[string]any, b map[string]any) error) resource.Hooks {
		h := after
		h.BeforeWrite = before
		return h
	}
	Categories.Hooks = with(func(ctx context.Context, tx pgx.Tx, v map[string]any, _ map[string]any) error {
		return m.checkLabels(ctx, tx, v, "labels", 120)
	})
	Menus.Hooks = with(func(ctx context.Context, tx pgx.Tx, v map[string]any, _ map[string]any) error {
		raw, ok := v["items"].(string)
		if !ok {
			return nil
		}
		pol, err := Policy(ctx, tx, handle.Property(ctx))
		if err != nil {
			return err
		}
		var items []CmsMenuItem
		if err := json.Unmarshal([]byte(raw), &items); err != nil {
			return handle.Invalid("items", "invalid", "items must be a list of menu items")
		}
		count := 0
		if err := m.checkMenuItems(ctx, tx, pol, items, 1, &count, "items"); err != nil {
			return err
		}
		out, _ := json.Marshal(items)
		v["items"] = string(out)
		return nil
	})
	Redirects.Hooks = with(func(ctx context.Context, tx pgx.Tx, v map[string]any, b map[string]any) error {
		return checkRedirect(ctx, tx, v, b)
	})
	Contacts.Hooks = with(func(ctx context.Context, tx pgx.Tx, v map[string]any, _ map[string]any) error {
		return m.checkContact(ctx, tx, v)
	})
	CourseGuides.Hooks = with(func(ctx context.Context, tx pgx.Tx, v map[string]any, _ map[string]any) error {
		return m.checkCourseGuide(ctx, tx, v)
	})
}

// checkLabels validates a {lang: text} JSON field.
func (m *Module) checkLabels(ctx context.Context, tx pgx.Tx, v map[string]any, field string, max int) error {
	raw, ok := v[field].(string)
	if !ok {
		return nil
	}
	pol, err := Policy(ctx, tx, handle.Property(ctx))
	if err != nil {
		return err
	}
	var labels map[string]string
	if err := json.Unmarshal([]byte(raw), &labels); err != nil {
		return handle.Invalid(field, "invalid", field+" must be an object of texts per language")
	}
	for lang, s := range labels {
		if !slices.Contains(pol.SupportedLanguages, lang) {
			return handle.Invalid(field+"."+lang, "unsupported_language", "language "+lang+" is not a website language (Content Policies)")
		}
		if len([]rune(s)) > max {
			return handle.Invalid(field+"."+lang, "too_long", fmt.Sprintf("must be at most %d characters", max))
		}
		labels[lang] = strings.TrimSpace(plainText(s))
	}
	out, _ := json.Marshal(labels)
	v[field] = string(out)
	return nil
}

// CmsMenuItem is one navigation entry.
type CmsMenuItem struct {
	ID        string            `json:"id"`
	Label     map[string]string `json:"label" doc:"Label per language"`
	Type      string            `json:"type" enum:"page,url,route"`
	PageID    string            `json:"pageId,omitempty"`
	URL       string            `json:"url,omitempty"`
	RouteKey  string            `json:"routeKey,omitempty"`
	NewTab    bool              `json:"newTab,omitempty"`
	Languages []string          `json:"languages,omitempty" doc:"Show only for these languages (empty: all)"`
	Children  []CmsMenuItem     `json:"children,omitempty"`
}

func (m *Module) checkMenuItems(ctx context.Context, tx pgx.Tx, pol ContentPolicy, items []CmsMenuItem, depth int, count *int, path string) error {
	if depth > 3 {
		return handle.Invalid(path, "too_deep", "menus have at most 3 levels")
	}
	for i := range items {
		it := &items[i]
		p := fmt.Sprintf("%s[%d]", path, i)
		*count++
		if *count > 200 {
			return handle.Invalid(path, "too_many", "a menu has at most 200 items")
		}
		if it.ID == "" {
			it.ID = uuid.NewString()
		}
		if len(it.Label) == 0 || strings.TrimSpace(it.Label[pol.DefaultLanguage]) == "" {
			return handle.Invalid(p+".label."+pol.DefaultLanguage, "required", "a label in the default language is required")
		}
		for lang, s := range it.Label {
			if !slices.Contains(pol.SupportedLanguages, lang) {
				return handle.Invalid(p+".label."+lang, "unsupported_language", "language "+lang+" is not a website language")
			}
			if len([]rune(s)) > 80 {
				return handle.Invalid(p+".label."+lang, "too_long", "labels have at most 80 characters")
			}
			it.Label[lang] = strings.TrimSpace(plainText(s))
		}
		for _, l := range it.Languages {
			if !slices.Contains(pol.SupportedLanguages, l) {
				return handle.Invalid(p+".languages", "unsupported_language", "language "+l+" is not a website language")
			}
		}
		link := CmsLink{Type: it.Type, URL: it.URL, RouteKey: it.RouteKey}
		if it.PageID != "" {
			u, err := uuid.Parse(it.PageID)
			if err != nil {
				return handle.Invalid(p+".pageId", "invalid", "pageId must be a page id")
			}
			link.PageID = &u
		}
		if err := m.checkLink(ctx, tx, &link, p); err != nil {
			return err
		}
		it.URL, it.RouteKey = link.URL, link.RouteKey
		if len(it.Children) > 0 {
			if err := m.checkMenuItems(ctx, tx, pol, it.Children, depth+1, count, p+".children"); err != nil {
				return err
			}
		}
	}
	return nil
}

var pathRe = regexp.MustCompile(`^/[A-Za-z0-9._~!$&'()*+,;=:@%/-]*(\?[^#\s]*)?$`)

// normPath normalises a website path ("old-page" → "/old-page").
func normPath(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || isAbsoluteURL(s) {
		return s
	}
	if !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	if len(s) > 1 {
		s = strings.TrimRight(s, "/")
	}
	return s
}

func checkRedirect(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	get := func(k string) string {
		if s, ok := v[k].(string); ok {
			return s
		}
		if before != nil {
			if s, ok := before[k].(string); ok {
				return s
			}
		}
		return ""
	}
	from, to := normPath(get("fromPath")), normPath(get("toPath"))
	if _, ok := v["fromPath"]; ok {
		if !pathRe.MatchString(from) {
			return handle.Invalid("fromPath", "invalid", "the old URL must be a path on this website (e.g. /promo-2022)")
		}
		v["fromPath"] = from
	}
	if _, ok := v["toPath"]; ok {
		if !pathRe.MatchString(to) && !isHTTPURL(to) {
			return handle.Invalid("toPath", "invalid", "the new URL must be a path (/…) or an http(s) address")
		}
		v["toPath"] = to
	}
	if strings.EqualFold(from, to) {
		return handle.Invalid("toPath", "loop", "the new URL must differ from the old URL")
	}
	// A → B while B → A exists would loop forever.
	var loop bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cms.redirects WHERE property_id = $1 AND lower(from_path) = lower($2)
		AND lower(to_path) = lower($3) AND archived_at IS NULL AND status = 'active')`, handle.Property(ctx), to, from).Scan(&loop); err != nil {
		return err
	}
	if loop {
		return handle.Invalid("toPath", "loop", "a redirect from the new URL back to the old URL exists")
	}
	return nil
}

var phoneRe = regexp.MustCompile(`^\+?[0-9 ()-]{6,24}$`)

var socialKeys = []string{"instagram", "facebook", "youtube", "tiktok", "x", "linkedin", "website", "tripadvisor", "google_maps"}

var weekDays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun", "public_holiday"}

func (m *Module) checkContact(ctx context.Context, tx pgx.Tx, v map[string]any) error {
	if s, ok := v["whatsapp"].(string); ok && s != "" && !phoneRe.MatchString(s) {
		return handle.Invalid("whatsapp", "invalid", "WhatsApp must be a phone number (+62…)")
	}
	if list, ok := v["phones"].([]string); ok {
		for _, p := range list {
			if !phoneRe.MatchString(p) {
				return handle.Invalid("phones", "invalid", p+" is not a phone number")
			}
		}
	}
	if s, ok := v["mapUrl"].(string); ok && s != "" && !isHTTPURL(s) {
		return handle.Invalid("mapUrl", "invalid", "the map link must be an http(s) address")
	}
	if s, ok := v["address"].(string); ok {
		v["address"] = plainText(s)
	}
	if raw, ok := v["openingHours"].(string); ok {
		var hours []struct {
			Day    string `json:"day"`
			Open   string `json:"open,omitempty"`
			Close  string `json:"close,omitempty"`
			Closed bool   `json:"closed,omitempty"`
			Note   string `json:"note,omitempty"`
		}
		if err := strictJSON(raw, &hours); err != nil {
			return handle.Invalid("openingHours", "invalid", "opening hours are [{day, open, close, closed, note}]")
		}
		for i, h := range hours {
			if !slices.Contains(weekDays, h.Day) {
				return handle.Invalid(fmt.Sprintf("openingHours[%d].day", i), "invalid", "day is one of "+strings.Join(weekDays, ", "))
			}
			if !h.Closed {
				o, err1 := time.Parse("15:04", h.Open)
				c, err2 := time.Parse("15:04", h.Close)
				if err1 != nil || err2 != nil || !c.After(o) {
					return handle.Invalid(fmt.Sprintf("openingHours[%d]", i), "invalid", "open and close are HH:MM with close after open")
				}
			}
			hours[i].Note = plainText(h.Note)
		}
		out, _ := json.Marshal(hours)
		v["openingHours"] = string(out)
	}
	if raw, ok := v["socialLinks"].(string); ok {
		var links map[string]string
		if err := json.Unmarshal([]byte(raw), &links); err != nil {
			return handle.Invalid("socialLinks", "invalid", "social links are an object {instagram: url, …}")
		}
		for k, u := range links {
			if !slices.Contains(socialKeys, k) {
				return handle.Invalid("socialLinks."+k, "invalid", "network is one of "+strings.Join(socialKeys, ", "))
			}
			if !isHTTPURL(u) {
				return handle.Invalid("socialLinks."+k, "invalid", "must be an http(s) address")
			}
		}
	}
	if raw, ok := v["translations"].(string); ok {
		pol, err := Policy(ctx, tx, handle.Property(ctx))
		if err != nil {
			return err
		}
		var tr map[string]struct {
			Name    string `json:"name,omitempty"`
			Address string `json:"address,omitempty"`
			Note    string `json:"note,omitempty"`
		}
		if err := strictJSON(raw, &tr); err != nil {
			return handle.Invalid("translations", "invalid", "translations are {lang: {name, address, note}}")
		}
		for lang, t := range tr {
			if !slices.Contains(pol.SupportedLanguages, lang) {
				return handle.Invalid("translations."+lang, "unsupported_language", "language "+lang+" is not a website language")
			}
			t.Name, t.Address, t.Note = plainText(t.Name), plainText(t.Address), plainText(t.Note)
			tr[lang] = t
		}
		out, _ := json.Marshal(tr)
		v["translations"] = string(out)
	}
	return nil
}

func (m *Module) checkCourseGuide(ctx context.Context, tx pgx.Tx, v map[string]any) error {
	if s, ok := v["title"].(string); ok {
		v["title"] = plainText(s)
	}
	if raw, ok := v["translations"].(string); ok {
		pol, err := Policy(ctx, tx, handle.Property(ctx))
		if err != nil {
			return err
		}
		var tr map[string]struct {
			Title       string `json:"title,omitempty"`
			Description string `json:"description,omitempty"`
			Tips        string `json:"tips,omitempty"`
		}
		if err := strictJSON(raw, &tr); err != nil {
			return handle.Invalid("translations", "invalid", "translations are {lang: {title, description, tips}}")
		}
		for lang, t := range tr {
			if !slices.Contains(pol.SupportedLanguages, lang) {
				return handle.Invalid("translations."+lang, "unsupported_language", "language "+lang+" is not a website language")
			}
			t.Title, t.Tips = plainText(t.Title), sanitizeHTML(t.Tips)
			t.Description = sanitizeHTML(t.Description)
			tr[lang] = t
		}
		out, _ := json.Marshal(tr)
		v["translations"] = string(out)
	}
	if list, ok := v["mediaIds"].([]string); ok && len(list) > 0 {
		ids := make([]uuid.UUID, 0, len(list))
		for _, s := range list {
			u, err := uuid.Parse(s)
			if err != nil {
				return handle.Invalid("mediaIds", "invalid", s+" is not a media id")
			}
			ids = append(ids, u)
		}
		if err := m.checkMedia(ctx, tx, handle.Property(ctx), ids, "mediaIds"); err != nil {
			return err
		}
	}
	return nil
}

func strictJSON(raw string, v any) error {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// checkMedia verifies that media library items exist in the property.
func (m *Module) checkMedia(ctx context.Context, q pgx.Tx, property uuid.UUID, ids []uuid.UUID, field string) error {
	if len(ids) == 0 {
		return nil
	}
	var n int
	if err := q.QueryRow(ctx, `SELECT count(DISTINCT id) FROM cms.media WHERE property_id = $1 AND id = ANY($2) AND archived_at IS NULL`,
		property, ids).Scan(&n); err != nil {
		return err
	}
	if n != len(uniqueIDs(ids)) {
		return errs.Validation("media_not_found", "image not found in the media library", errs.Field(field, "not_found", "image not found in the media library"))
	}
	return nil
}

func uniqueIDs(ids []uuid.UUID) []uuid.UUID {
	var out []uuid.UUID
	for _, u := range ids {
		if !slices.Contains(out, u) {
			out = append(out, u)
		}
	}
	return out
}
