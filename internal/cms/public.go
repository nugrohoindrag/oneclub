package cms

// Public website API (web): published content only, per language with
// fallback to the default language (FR-CMS-05), SEO data, sitemap and
// robots (FR-CMS-08), navigation (FR-CMS-09), banners, news, gallery,
// contact information and course guide. Responses are cache-friendly:
// strong ETag (If-None-Match → 304), Last-Modified = the last change of the
// website, Cache-Control public with stale-while-revalidate; every publish
// bumps the website revision (FR-CMS-10).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/handle"
)

// ── public types ──────────────────────────────────────────────────────────

// CmsPublicMedia is an image with its web variants.
type CmsPublicMedia struct {
	ID       uuid.UUID         `json:"id"`
	URL      string            `json:"url"`
	Width    *int              `json:"width"`
	Height   *int              `json:"height"`
	Alt      string            `json:"alt"`
	Caption  string            `json:"caption,omitempty"`
	Variants []CmsMediaVariant `json:"variants"`
}

// CmsPublicLink is a resolved link.
type CmsPublicLink struct {
	Href     string `json:"href"`
	External bool   `json:"external"`
	NewTab   bool   `json:"newTab"`
}

// CmsPublicForm is the target of a contact form block (the existing public
// contact / inquiry endpoints of CRM).
type CmsPublicForm struct {
	Action string   `json:"action"`
	Method string   `json:"method"`
	Fields []string `json:"fields"`
	Topic  string   `json:"topic,omitempty"`
	Line   string   `json:"line,omitempty"`
}

// CmsPublicBlock is a rendered block.
type CmsPublicBlock struct {
	ID      string           `json:"id"`
	Type    string           `json:"type"`
	Config  map[string]any   `json:"config"`
	Content map[string]any   `json:"content" doc:"Texts of the served language (default language for missing texts)"`
	Media   []CmsPublicMedia `json:"media,omitempty"`
	Link    *CmsPublicLink   `json:"link,omitempty"`
	Data    *CmsDataRef      `json:"data,omitempty" doc:"Data block: the public API of the owning module the website reads (K5)"`
	Form    *CmsPublicForm   `json:"form,omitempty"`
	Embed   string           `json:"embedUrl,omitempty"`
	Map     map[string]any   `json:"map,omitempty"`
}

// CmsAlternate is a language version of a page (hreflang).
type CmsAlternate struct {
	Language string `json:"language"`
	Href     string `json:"href"`
}

// CmsPublicSEO are the SEO tags of a page.
type CmsPublicSEO struct {
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Canonical   string         `json:"canonical"`
	OGImage     string         `json:"ogImage,omitempty"`
	NoIndex     bool           `json:"noindex"`
	Alternates  []CmsAlternate `json:"alternates"`
}

// CmsCrumb is one breadcrumb.
type CmsCrumb struct {
	Title string `json:"title"`
	Path  string `json:"path"`
}

// CmsPublicCategory is a news category.
type CmsPublicCategory struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

// CmsPublicItem is one image of an album.
type CmsPublicItem struct {
	Media   CmsPublicMedia `json:"media"`
	Caption string         `json:"caption,omitempty"`
}

// CmsPublicContent is a rendered page, news article or album.
type CmsPublicContent struct {
	ID                uuid.UUID          `json:"id"`
	Kind              string             `json:"kind"`
	Key               *string            `json:"key"`
	Template          *string            `json:"template"`
	Language          string             `json:"language" doc:"Language served"`
	RequestedLanguage string             `json:"requestedLanguage"`
	Fallback          bool               `json:"fallback" doc:"The requested translation is missing; the default language is served"`
	Title             string             `json:"title"`
	Slug              string             `json:"slug"`
	Path              string             `json:"path"`
	Summary           string             `json:"summary,omitempty"`
	Image             *CmsPublicMedia    `json:"image,omitempty"`
	SEO               CmsPublicSEO       `json:"seo"`
	Blocks            []CmsPublicBlock   `json:"blocks"`
	Items             []CmsPublicItem    `json:"items,omitempty"`
	Breadcrumbs       []CmsCrumb         `json:"breadcrumbs"`
	Category          *CmsPublicCategory `json:"category,omitempty"`
	Tags              []string           `json:"tags,omitempty"`
	AuthorName        *string            `json:"authorName,omitempty"`
	Date              *string            `json:"date,omitempty" doc:"News publish date"`
	StructuredData    map[string]any     `json:"structuredData" doc:"Basic schema.org JSON-LD"`
	VersionNo         int                `json:"versionNo"`
	PublishedAt       *time.Time         `json:"publishedAt"`
	Revision          int64              `json:"revision"`
	Preview           bool               `json:"preview"`
}

// CmsPublicSummary is a list entry (news, albums, pages).
type CmsPublicSummary struct {
	ID          uuid.UUID          `json:"id"`
	Kind        string             `json:"kind"`
	Key         *string            `json:"key,omitempty"`
	Template    *string            `json:"template,omitempty"`
	ParentID    *uuid.UUID         `json:"parentId,omitempty"`
	Language    string             `json:"language"`
	Title       string             `json:"title"`
	Slug        string             `json:"slug"`
	Path        string             `json:"path"`
	Paths       map[string]string  `json:"paths" doc:"Path per language"`
	Summary     string             `json:"summary,omitempty"`
	Image       *CmsPublicMedia    `json:"image,omitempty"`
	Category    *CmsPublicCategory `json:"category,omitempty"`
	Tags        []string           `json:"tags,omitempty"`
	Featured    bool               `json:"featured"`
	Date        *string            `json:"date,omitempty"`
	PublishedAt *time.Time         `json:"publishedAt"`
	ImageCount  int                `json:"imageCount,omitempty"`
}

// CmsPublicList is a list with the next cursor.
type CmsPublicList struct {
	Language   string              `json:"language"`
	Items      []CmsPublicSummary  `json:"items"`
	Categories []CmsPublicCategory `json:"categories,omitempty"`
	NextCursor string              `json:"nextCursor,omitempty"`
	Revision   int64               `json:"revision"`
}

// CmsPublicBanner is a banner of a placement.
type CmsPublicBanner struct {
	ID          uuid.UUID       `json:"id"`
	Key         *string         `json:"key"`
	Placement   string          `json:"placement"`
	Language    string          `json:"language"`
	Title       string          `json:"title"`
	Subtitle    string          `json:"subtitle,omitempty"`
	ButtonLabel string          `json:"buttonLabel,omitempty"`
	Image       *CmsPublicMedia `json:"image,omitempty"`
	Link        *CmsPublicLink  `json:"link,omitempty"`
	SortOrder   int             `json:"sortOrder"`
	StartsAt    *time.Time      `json:"startsAt"`
	EndsAt      *time.Time      `json:"endsAt"`
}

// CmsPublicBanners are the banners of a placement in one language.
type CmsPublicBanners struct {
	Language string            `json:"language"`
	Items    []CmsPublicBanner `json:"items"`
	Revision int64             `json:"revision"`
}

// CmsPublicNavItem is a resolved navigation item.
type CmsPublicNavItem struct {
	ID       string             `json:"id"`
	Label    string             `json:"label"`
	Href     string             `json:"href"`
	External bool               `json:"external"`
	NewTab   bool               `json:"newTab"`
	Type     string             `json:"type"`
	RouteKey string             `json:"routeKey,omitempty"`
	Children []CmsPublicNavItem `json:"children"`
}

// CmsPublicMenu is a navigation menu in one language.
type CmsPublicMenu struct {
	Code     string             `json:"code"`
	Name     string             `json:"name"`
	Location string             `json:"location"`
	Items    []CmsPublicNavItem `json:"items"`
}

// CmsPublicNavigation are the menus of the website.
type CmsPublicNavigation struct {
	Language string          `json:"language"`
	Menus    []CmsPublicMenu `json:"menus"`
	Revision int64           `json:"revision"`
}

// CmsOpeningHours is one opening-hours row.
type CmsOpeningHours struct {
	Day    string `json:"day"`
	Open   string `json:"open,omitempty"`
	Close  string `json:"close,omitempty"`
	Closed bool   `json:"closed,omitempty"`
	Note   string `json:"note,omitempty"`
}

// CmsPublicContact is a point of contact.
type CmsPublicContact struct {
	Code         string            `json:"code"`
	Name         string            `json:"name"`
	Primary      bool              `json:"primary"`
	Address      string            `json:"address,omitempty"`
	Phones       []string          `json:"phones"`
	WhatsApp     string            `json:"whatsapp,omitempty"`
	WhatsAppURL  string            `json:"whatsappUrl,omitempty"`
	Email        string            `json:"email,omitempty"`
	Latitude     *string           `json:"latitude"`
	Longitude    *string           `json:"longitude"`
	MapURL       string            `json:"mapUrl,omitempty"`
	OpeningHours []CmsOpeningHours `json:"openingHours"`
	SocialLinks  map[string]string `json:"socialLinks"`
	Note         string            `json:"note,omitempty"`
}

// CmsPublicContacts is the Contact page data.
type CmsPublicContacts struct {
	Language string             `json:"language"`
	Contacts []CmsPublicContact `json:"contacts"`
	Revision int64              `json:"revision"`
}

// CmsPublicHole is the course guide text of one hole.
type CmsPublicHole struct {
	CourseCode  string           `json:"courseCode"`
	HoleNumber  int              `json:"holeNumber"`
	Title       string           `json:"title"`
	Description string           `json:"description,omitempty" doc:"Sanitised HTML"`
	Tips        string           `json:"tips,omitempty" doc:"Sanitised HTML"`
	Media       []CmsPublicMedia `json:"media"`
}

// CmsPublicCourseGuide completes /api/v1/public/golf/info (par, distances).
type CmsPublicCourseGuide struct {
	Language string          `json:"language"`
	GolfInfo string          `json:"golfInfo" doc:"Golf course data endpoint (owner: golf)"`
	Holes    []CmsPublicHole `json:"holes"`
	Revision int64           `json:"revision"`
}

// CmsSitemapEntry is one URL of the sitemap.
type CmsSitemapEntry struct {
	Loc        string         `json:"loc"`
	Path       string         `json:"path"`
	Language   string         `json:"language"`
	LastMod    time.Time      `json:"lastmod"`
	ChangeFreq string         `json:"changefreq"`
	Priority   string         `json:"priority"`
	Alternates []CmsAlternate `json:"alternates"`
}

// CmsSitemap is the sitemap data (also served as sitemap.xml).
type CmsSitemap struct {
	Entries  []CmsSitemapEntry `json:"entries"`
	Revision int64             `json:"revision"`
}

// CmsRobots is the robots.txt data.
type CmsRobots struct {
	AllowIndexing bool     `json:"allowIndexing"`
	Disallow      []string `json:"disallow"`
	Sitemap       string   `json:"sitemap"`
	Text          string   `json:"text" doc:"robots.txt content"`
}

// CmsPublicRedirect is an active redirect.
type CmsPublicRedirect struct {
	From       string `json:"from" db:"from_path"`
	To         string `json:"to" db:"to_path"`
	StatusCode int    `json:"statusCode" db:"status_code"`
}

// CmsPublicRedirects are the redirects of the website.
type CmsPublicRedirects struct {
	Items    []CmsPublicRedirect `json:"items"`
	Revision int64               `json:"revision"`
}

// CmsLanguage is a website language.
type CmsLanguage struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

// CmsPublicSite is the website bootstrap of the CMS.
type CmsPublicSite struct {
	PropertyID      uuid.UUID     `json:"propertyId"`
	PropertyCode    string        `json:"propertyCode"`
	Name            string        `json:"name"`
	DefaultLanguage string        `json:"defaultLanguage"`
	Languages       []CmsLanguage `json:"languages"`
	Fallback        bool          `json:"fallbackToDefault"`
	Revision        int64         `json:"revision" doc:"Changes with every publication; revalidate cached pages when it changes"`
	ChangedAt       time.Time     `json:"changedAt"`
	WebsiteURL      string        `json:"websiteUrl"`
}

// ── plumbing ──────────────────────────────────────────────────────────────

type site struct {
	pid       uuid.UUID
	code      string
	name      string
	pol       ContentPolicy
	lang      string // served language
	reqLang   string
	revision  int64
	changedAt time.Time
	base      string // website base URL
	now       time.Time
}

func (s *site) url(path string) string { return s.base + path }

// resolveSite finds the property (propertyId, property code or the first
// property) and the language.
func (m *Module) resolveSite(ctx context.Context, tx pgx.Tx, r *http.Request) (*site, error) {
	q := r.URL.Query()
	s := &site{now: m.now()}
	if m.Cfg != nil {
		s.base = strings.TrimRight(m.Cfg.WebsiteURL, "/")
	}
	var err error
	switch {
	case q.Get("propertyId") != "":
		pid, perr := uuid.Parse(q.Get("propertyId"))
		if perr != nil {
			return nil, errs.BadRequest("invalid_property", "propertyId must be an id")
		}
		err = tx.QueryRow(ctx, `SELECT id, code, name FROM platform.properties WHERE id = $1 AND status = 'active' AND archived_at IS NULL`, pid).
			Scan(&s.pid, &s.code, &s.name)
	default:
		err = tx.QueryRow(ctx, `SELECT id, code, name FROM platform.properties WHERE status = 'active' AND archived_at IS NULL AND ($1 = '' OR code = upper($1))
			ORDER BY created_at LIMIT 1`, q.Get("property")).Scan(&s.pid, &s.code, &s.name)
	}
	if dbtx.IsNoRows(err) {
		return nil, errs.NotFound("website")
	}
	if err != nil {
		return nil, err
	}
	if s.pol, err = Policy(ctx, tx, s.pid); err != nil {
		return nil, err
	}
	s.reqLang = strings.ToLower(q.Get("lang"))
	s.lang = s.reqLang
	if !slices.Contains(s.pol.SupportedLanguages, s.lang) {
		s.lang = s.pol.DefaultLanguage
	}
	if s.reqLang == "" {
		s.reqLang = s.lang
	}
	err = tx.QueryRow(ctx, `SELECT revision, changed_at FROM cms.site_state WHERE property_id = $1`, s.pid).Scan(&s.revision, &s.changedAt)
	if dbtx.IsNoRows(err) {
		err = nil
		s.changedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return s, err
}

type publicFn func(ctx context.Context, tx pgx.Tx, s *site, r *http.Request) (any, error)

// PublicLimiter limits the public website API per client IP and path
// (the website server caches pages, browsers call the API rarely).
var PublicLimiter = &handle.Limiter{N: 300, Period: time.Minute}

// limited rejects requests over the public rate limit with 429.
func limited(h http.HandlerFunc) http.HandlerFunc { return PublicLimiter.Wrap(h) }

// public runs a public read and writes it with cache validators.
func (m *Module) public(fn publicFn) http.HandlerFunc {
	return limited(func(w http.ResponseWriter, r *http.Request) {
		ctx := dbtx.System(r.Context())
		if m.Enabled != nil {
			if on, err := m.Enabled(ctx, "cms"); err == nil && !on {
				httpx.WriteError(w, r, errs.NotFound("website content"))
				return
			}
		}
		var out any
		var s *site
		err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
			var err error
			if s, err = m.resolveSite(ctx, tx, r); err != nil {
				return err
			}
			sctx := reqctx.WithProperty(dbtx.WithScope(r.Context(), dbtx.Scope{PropertyIDs: []uuid.UUID{s.pid}}), s.pid)
			if err := dbtx.ApplyScope(sctx, tx); err != nil {
				return err
			}
			out, err = fn(sctx, tx, s, r)
			return err
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		writeCached(w, r, out, s.changedAt, 60)
	})
}

// writeCached writes JSON with a strong ETag; a matching If-None-Match
// answers 304 Not Modified.
func writeCached(w http.ResponseWriter, r *http.Request, v any, lastMod time.Time, maxAge int) {
	body, err := json.Marshal(v)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Cache-Control", fmt.Sprintf("public, max-age=%d, stale-while-revalidate=600", maxAge))
	if !lastMod.IsZero() {
		h.Set("Last-Modified", lastMod.UTC().Format(http.TimeFormat))
	}
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		for _, t := range strings.Split(inm, ",") {
			if t = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(t), "W/")); t == etag || t == "*" {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
	}
	h.Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(body, '\n'))
}

// visible is the SQL condition of live public content.
const visible = `c.live AND c.archived_at IS NULL AND (c.unpublish_at IS NULL OR c.unpublish_at > now())`

type publishedRow struct {
	CmsContent
	Document CmsDocument `db:"document"`
}

const publishedSelect = `SELECT c.id, c.property_id, c.kind, c.key, c.template, c.parent_id, c.sort_order, c.show_in_sitemap, c.category_id, c.tags,
	c.author_name, c.featured, to_char(c.display_date, 'YYYY-MM-DD') AS display_date, c.placement, c.page_ids, c.link, c.title, c.translation_status,
	c.status, c.live, c.latest_version, c.published_version, c.publish_at, c.unpublish_at, c.published_at, c.first_published_at, c.created_at,
	c.updated_at, v.document
	FROM cms.contents c JOIN cms.content_versions v ON v.content_id = c.id AND v.version_no = c.published_version`

// translationFor picks the served translation (requested or default).
func translationFor(doc CmsDocument, s *site, slugged bool) (string, CmsTranslation, bool) {
	ok := func(t CmsTranslation, found bool) bool {
		return found && strings.TrimSpace(t.Title) != "" && (!slugged || t.Slug != "")
	}
	if t, found := doc.Translations[s.lang]; ok(t, found) {
		return s.lang, t, true
	}
	if s.pol.FallbackToDefault || s.lang == s.pol.DefaultLanguage {
		if t, found := doc.Translations[s.pol.DefaultLanguage]; ok(t, found) {
			return s.pol.DefaultLanguage, t, true
		}
	}
	return "", CmsTranslation{}, false
}

// renderer resolves media, pages and galleries of rendered content.
type renderer struct {
	s         *site
	tx        pgx.Tx
	ctx       context.Context
	media     map[uuid.UUID]CmsMedia
	pages     map[uuid.UUID]pageRef
	galleries map[uuid.UUID][]CmsGalleryItem
	contacts  map[uuid.UUID]map[string]any
}

type pageRef struct {
	key, template *string
	paths         map[string]string
	titles        map[string]string
	parent        *uuid.UUID
}

func newRenderer(ctx context.Context, tx pgx.Tx, s *site) *renderer {
	return &renderer{s: s, tx: tx, ctx: ctx, media: map[uuid.UUID]CmsMedia{}, pages: map[uuid.UUID]pageRef{},
		galleries: map[uuid.UUID][]CmsGalleryItem{}, contacts: map[uuid.UUID]map[string]any{}}
}

// load fetches every referenced object of the documents.
func (rd *renderer) load(docs ...CmsDocument) error {
	rf := &refs{}
	collect := func(doc CmsDocument) {
		for _, t := range doc.Translations {
			if t.MediaID != nil {
				rf.add(&rf.media, *t.MediaID)
			}
			if t.SEO != nil && t.SEO.OGImageID != nil {
				rf.add(&rf.media, *t.SEO.OGImageID)
			}
		}
		for _, it := range doc.Items {
			rf.add(&rf.media, it.MediaID)
		}
		for _, b := range doc.Blocks {
			for k, v := range b.Config {
				switch k {
				case "mediaId":
					if u, err := uuid.Parse(fmt.Sprint(v)); err == nil {
						rf.add(&rf.media, u)
					}
				case "mediaIds":
					if list, ok := v.([]any); ok {
						for _, x := range list {
							if u, err := uuid.Parse(fmt.Sprint(x)); err == nil {
								rf.add(&rf.media, u)
							}
						}
					}
				case "galleryId":
					if u, err := uuid.Parse(fmt.Sprint(v)); err == nil {
						rf.add(&rf.galleries, u)
					}
				case "contactId":
					if u, err := uuid.Parse(fmt.Sprint(v)); err == nil {
						rf.add(&rf.contacts, u)
					}
				case "link":
					if l, ok := v.(map[string]any); ok {
						if u, err := uuid.Parse(fmt.Sprint(l["pageId"])); err == nil {
							rf.add(&rf.pages, u)
						}
					}
				}
			}
		}
	}
	for _, d := range docs {
		collect(d)
	}
	if len(rf.galleries) > 0 {
		rows, err := handle.List[publishedRow](rd.tx.Query(rd.ctx, publishedSelect+` WHERE c.property_id = $1 AND c.id = ANY($2) AND c.kind = 'gallery' AND `+visible,
			rd.s.pid, rf.galleries))
		if err != nil {
			return err
		}
		for _, g := range rows {
			rd.galleries[g.ID] = g.Document.Items
			for _, it := range g.Document.Items {
				rf.add(&rf.media, it.MediaID)
			}
		}
	}
	mm, err := mediaMap(rd.ctx, rd.tx, rd.s.pid, rf.media)
	if err != nil {
		return err
	}
	for k, v := range mm {
		rd.media[k] = v
	}
	if err := rd.loadPages(rf.pages); err != nil {
		return err
	}
	if len(rf.contacts) > 0 {
		rows, err := rd.tx.Query(rd.ctx, `SELECT id, latitude::text, longitude::text, name, coalesce(map_url, '') FROM cms.contacts
			WHERE property_id = $1 AND id = ANY($2) AND archived_at IS NULL`, rd.s.pid, rf.contacts)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var cid uuid.UUID
			var lat, lng *string
			var name, mapURL string
			if err := rows.Scan(&cid, &lat, &lng, &name, &mapURL); err != nil {
				return err
			}
			rd.contacts[cid] = map[string]any{"latitude": lat, "longitude": lng, "name": name, "mapUrl": mapURL}
		}
		return rows.Err()
	}
	return nil
}

// loadPages loads live pages (paths per language) by id.
func (rd *renderer) loadPages(ids []uuid.UUID) error {
	var need []uuid.UUID
	for _, u := range ids {
		if _, ok := rd.pages[u]; !ok {
			need = append(need, u)
		}
	}
	if len(need) == 0 {
		return nil
	}
	rows, err := handle.List[publishedRow](rd.tx.Query(rd.ctx, publishedSelect+` WHERE c.property_id = $1 AND c.id = ANY($2) AND c.kind = 'page' AND `+visible,
		rd.s.pid, need))
	if err != nil {
		return err
	}
	for _, p := range rows {
		ref := pageRef{key: p.Key, template: p.Template, paths: map[string]string{}, titles: map[string]string{}, parent: p.ParentID}
		for lang, t := range p.Document.Translations {
			if t.Title != "" {
				ref.paths[lang] = contentPath(KindPage, lang, t.Slug, p.Key, p.Template)
				ref.titles[lang] = t.Title
			}
		}
		rd.pages[p.ID] = ref
	}
	return nil
}

func (rd *renderer) pathOf(ref pageRef) (string, string) {
	if p, ok := ref.paths[rd.s.lang]; ok && p != "" {
		return p, ref.titles[rd.s.lang]
	}
	return ref.paths[rd.s.pol.DefaultLanguage], ref.titles[rd.s.pol.DefaultLanguage]
}

func (rd *renderer) mediaOf(id uuid.UUID, alt, caption string) *CmsPublicMedia {
	md, ok := rd.media[id]
	if !ok {
		return nil
	}
	if alt == "" {
		alt = pick(md.Alt, rd.s.lang, rd.s.pol.DefaultLanguage)
	}
	if caption == "" {
		caption = pick(md.Caption, rd.s.lang, rd.s.pol.DefaultLanguage)
	}
	return &CmsPublicMedia{ID: md.ID, URL: md.URL, Width: md.Width, Height: md.Height, Alt: alt, Caption: caption, Variants: md.Variants}
}

func pick(m map[string]string, lang, def string) string {
	if v := m[lang]; v != "" {
		return v
	}
	return m[def]
}

// link resolves a link for the served language (nil when its page is not live).
func (rd *renderer) link(raw any) *CmsPublicLink {
	var l CmsLink
	b, _ := json.Marshal(raw)
	if json.Unmarshal(b, &l) != nil {
		return nil
	}
	switch l.Type {
	case "page":
		if l.PageID == nil {
			return nil
		}
		ref, ok := rd.pages[*l.PageID]
		if !ok {
			return nil
		}
		p, _ := rd.pathOf(ref)
		return &CmsPublicLink{Href: p, NewTab: l.NewTab}
	case "route":
		return &CmsPublicLink{Href: routePath(rd.s.lang, l.RouteKey), NewTab: l.NewTab}
	case "url":
		return &CmsPublicLink{Href: l.URL, External: isAbsoluteURL(l.URL), NewTab: l.NewTab}
	}
	return nil
}

func routePath(lang, key string) string {
	p, ok := RouteKeys[key]
	if !ok {
		return "/" + lang
	}
	if p == "" {
		return "/" + lang
	}
	return "/" + lang + "/" + p
}

// blocks renders the visible blocks in the served language.
func (rd *renderer) blocks(doc CmsDocument) []CmsPublicBlock {
	out := []CmsPublicBlock{}
	for _, b := range doc.Blocks {
		if b.Hidden {
			continue
		}
		content := map[string]any{}
		for k, v := range b.Content[rd.s.pol.DefaultLanguage] {
			content[k] = v
		}
		for k, v := range b.Content[rd.s.lang] {
			content[k] = v
		}
		pb := CmsPublicBlock{ID: b.ID, Type: b.Type, Config: map[string]any{}, Content: content}
		for k, v := range b.Config {
			pb.Config[k] = v
		}
		switch b.Type {
		case "image":
			alt, _ := content["alt"].(string)
			caption, _ := content["caption"].(string)
			u, _ := uuid.Parse(fmt.Sprint(b.Config["mediaId"]))
			if md := rd.mediaOf(u, alt, caption); md != nil {
				pb.Media = []CmsPublicMedia{*md}
			} else {
				continue // image removed from the library
			}
			if l, ok := b.Config["link"]; ok {
				pb.Link = rd.link(l)
			}
		case "gallery":
			var items []CmsGalleryItem
			if gid, err := uuid.Parse(fmt.Sprint(b.Config["galleryId"])); err == nil {
				items = rd.galleries[gid]
			}
			if list, ok := b.Config["mediaIds"].([]any); ok {
				for _, x := range list {
					if u, err := uuid.Parse(fmt.Sprint(x)); err == nil {
						items = append(items, CmsGalleryItem{MediaID: u})
					}
				}
			}
			limit, _ := toInt(b.Config["limit"])
			for _, it := range items {
				if limit > 0 && len(pb.Media) >= limit {
					break
				}
				if md := rd.mediaOf(it.MediaID, "", pick(it.Caption, rd.s.lang, rd.s.pol.DefaultLanguage)); md != nil {
					pb.Media = append(pb.Media, *md)
				}
			}
		case "video":
			pb.Embed = videoEmbed(fmt.Sprint(b.Config["url"]))
		case "cta":
			pb.Link = rd.link(b.Config["link"])
			if pb.Link == nil {
				continue // linked page no longer live
			}
			if u, err := uuid.Parse(fmt.Sprint(b.Config["mediaId"])); err == nil {
				if md := rd.mediaOf(u, "", ""); md != nil {
					pb.Media = []CmsPublicMedia{*md}
				}
			}
		case "contact_form":
			ep, _ := b.Config["endpoint"].(string)
			f := &CmsPublicForm{Method: http.MethodPost, Fields: []string{"guest.name", "guest.phone", "guest.email", "message", "topic"}}
			f.Topic, _ = b.Config["topic"].(string)
			f.Line, _ = b.Config["line"].(string)
			if ep == "inquiry" {
				f.Action = "/api/v1/public/inquiries"
				f.Fields = []string{"guest.name", "guest.phone", "guest.email", "line", "eventDate", "pax", "message", "consent"}
			} else {
				f.Action = "/api/v1/public/contact"
			}
			pb.Form = f
		case "map":
			mp := map[string]any{"latitude": b.Config["latitude"], "longitude": b.Config["longitude"], "zoom": b.Config["zoom"]}
			if cid, err := uuid.Parse(fmt.Sprint(b.Config["contactId"])); err == nil {
				if c, ok := rd.contacts[cid]; ok {
					for k, v := range c {
						mp[k] = v
					}
				}
			}
			pb.Map = mp
		case "data":
			pb.Data = dataRef(b.Config, rd.s.pid, rd.s.code, rd.s.lang)
		case "banner_slot":
			pb.Data = &CmsDataRef{Source: "banners", Owner: "cms", Endpoint: "/api/v1/public/cms/banners",
				Query: map[string]string{"placement": fmt.Sprint(b.Config["placement"]), "propertyId": rd.s.pid.String(), "lang": rd.s.lang}}
			pb.Data.URL = pb.Data.Endpoint + "?lang=" + rd.s.lang + "&placement=" + fmt.Sprint(b.Config["placement"]) + "&propertyId=" + rd.s.pid.String()
		}
		out = append(out, pb)
	}
	return out
}

// render builds the public view of a content item.
func (m *Module) render(ctx context.Context, tx pgx.Tx, s *site, c CmsContent, doc CmsDocument, versionNo int, preview bool) (CmsPublicContent, error) {
	spec := specOf(c.Kind)
	lang, t, ok := translationFor(doc, s, spec.SlugScope != "")
	if !ok {
		return CmsPublicContent{}, errs.NotFound(strings.ToLower(spec.Name))
	}
	rd := newRenderer(ctx, tx, s)
	if err := rd.load(doc); err != nil {
		return CmsPublicContent{}, err
	}
	served := *s
	served.lang = lang
	rd.s = &served
	path := contentPath(c.Kind, lang, t.Slug, c.Key, c.Template)
	out := CmsPublicContent{ID: c.ID, Kind: c.Kind, Key: c.Key, Template: c.Template, Language: lang, RequestedLanguage: s.reqLang,
		Fallback: lang != s.reqLang, Title: t.Title, Slug: t.Slug, Path: path, Summary: t.Summary, Blocks: rd.blocks(doc), Breadcrumbs: []CmsCrumb{},
		VersionNo: versionNo, PublishedAt: c.PublishedAt, Revision: s.revision, Preview: preview, Tags: c.Tags, AuthorName: c.AuthorName}
	if t.MediaID != nil {
		out.Image = rd.mediaOf(*t.MediaID, t.Alt, "")
	} else if def := doc.Translations[s.pol.DefaultLanguage]; def.MediaID != nil {
		out.Image = rd.mediaOf(*def.MediaID, def.Alt, "")
	}
	for _, it := range doc.Items {
		if md := rd.mediaOf(it.MediaID, "", pick(it.Caption, lang, s.pol.DefaultLanguage)); md != nil {
			out.Items = append(out.Items, CmsPublicItem{Media: *md, Caption: md.Caption})
		}
	}
	if c.Kind == KindArticle {
		out.Date = articleDate(c)
		if c.CategoryID != nil {
			cat, err := category(ctx, tx, *c.CategoryID, lang, s.pol.DefaultLanguage)
			if err != nil {
				return out, err
			}
			out.Category = cat
		}
	}
	// SEO (FR-CMS-08)
	seo := CmsPublicSEO{Title: t.Title, Description: t.Summary, Canonical: s.url(path), Alternates: []CmsAlternate{}}
	if t.SEO != nil {
		if t.SEO.MetaTitle != "" {
			seo.Title = t.SEO.MetaTitle
		}
		if t.SEO.MetaDescription != "" {
			seo.Description = t.SEO.MetaDescription
		}
		if t.SEO.CanonicalURL != "" {
			seo.Canonical = t.SEO.CanonicalURL
			if strings.HasPrefix(seo.Canonical, "/") {
				seo.Canonical = s.url(seo.Canonical)
			}
		}
		if t.SEO.OGImageID != nil {
			if md := rd.mediaOf(*t.SEO.OGImageID, "", ""); md != nil {
				seo.OGImage = md.URL
			}
		}
		seo.NoIndex = t.SEO.NoIndex
	}
	if seo.OGImage == "" && out.Image != nil {
		seo.OGImage = out.Image.URL
	}
	if preview {
		seo.NoIndex = true
	}
	for _, l := range s.pol.SupportedLanguages {
		if tr, ok := doc.Translations[l]; ok && tr.Title != "" && (spec.SlugScope == "" || tr.Slug != "") {
			seo.Alternates = append(seo.Alternates, CmsAlternate{Language: l, Href: s.url(contentPath(c.Kind, l, tr.Slug, c.Key, c.Template))})
		}
	}
	if len(seo.Alternates) > 1 {
		seo.Alternates = append(seo.Alternates, CmsAlternate{Language: "x-default",
			Href: s.url(contentPath(c.Kind, s.pol.DefaultLanguage, doc.Translations[s.pol.DefaultLanguage].Slug, c.Key, c.Template))})
	}
	out.SEO = seo
	// breadcrumbs: ancestors of a page, the news / gallery index otherwise
	switch c.Kind {
	case KindPage:
		var chain []CmsCrumb
		parent := c.ParentID
		for i := 0; parent != nil && i < 10; i++ {
			if err := rd.loadPages([]uuid.UUID{*parent}); err != nil {
				return out, err
			}
			ref, ok := rd.pages[*parent]
			if !ok {
				break
			}
			p, title := rd.pathOf(ref)
			chain = append([]CmsCrumb{{Title: title, Path: p}}, chain...)
			parent = ref.parent
		}
		out.Breadcrumbs = append(chain, CmsCrumb{Title: t.Title, Path: path})
	case KindArticle:
		out.Breadcrumbs = []CmsCrumb{{Title: map[string]string{"id": "Berita"}[lang] + map[string]string{"en": "News"}[lang], Path: routePath(lang, "news")},
			{Title: t.Title, Path: path}}
	case KindGallery:
		out.Breadcrumbs = []CmsCrumb{{Title: map[string]string{"id": "Galeri"}[lang] + map[string]string{"en": "Gallery"}[lang], Path: routePath(lang, "gallery")},
			{Title: t.Title, Path: path}}
	}
	// basic schema.org structured data
	sd := map[string]any{"@context": "https://schema.org", "@type": "WebPage", "name": seo.Title, "url": seo.Canonical, "inLanguage": lang}
	if seo.Description != "" {
		sd["description"] = seo.Description
	}
	switch c.Kind {
	case KindArticle:
		sd["@type"] = "NewsArticle"
		sd["headline"] = t.Title
		if out.Date != nil {
			sd["datePublished"] = *out.Date
		}
		if c.AuthorName != nil {
			sd["author"] = map[string]any{"@type": "Person", "name": *c.AuthorName}
		}
		if out.Image != nil {
			sd["image"] = s.url(out.Image.URL)
		}
		sd["publisher"] = map[string]any{"@type": "Organization", "name": s.name}
	case KindGallery:
		sd["@type"] = "ImageGallery"
	}
	out.StructuredData = sd
	return out, nil
}

func articleDate(c CmsContent) *string {
	if c.DisplayDate != nil {
		return c.DisplayDate
	}
	if c.FirstPublishedAt != nil {
		d := c.FirstPublishedAt.UTC().Format("2006-01-02")
		return &d
	}
	return nil
}

func category(ctx context.Context, tx pgx.Tx, cid uuid.UUID, lang, def string) (*CmsPublicCategory, error) {
	var code, name string
	var labels map[string]string
	err := tx.QueryRow(ctx, `SELECT code, name, labels FROM cms.categories WHERE id = $1 AND status = 'active' AND archived_at IS NULL`, cid).Scan(&code, &name, &labels)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	label := labels[lang]
	if label == "" {
		label = name
		if lang != def && labels[def] != "" {
			label = labels[def]
		}
	}
	return &CmsPublicCategory{Code: code, Label: label}, nil
}

// findBySlug finds live content of a URL space by slug (any language).
func findBySlug(ctx context.Context, tx pgx.Tx, s *site, scope, slug string) (publishedRow, error) {
	rows, err := handle.List[publishedRow](tx.Query(ctx, publishedSelect+` JOIN cms.content_slugs sl ON sl.content_id = c.id
		WHERE sl.property_id = $1 AND sl.scope = $2 AND sl.slug = $3 AND `+visible+` ORDER BY (sl.language = $4) DESC LIMIT 5`, s.pid, scope, slug, s.lang))
	if err != nil {
		return publishedRow{}, err
	}
	for _, r := range rows {
		for _, t := range r.Document.Translations {
			if t.Slug == slug {
				return r, nil
			}
		}
	}
	return publishedRow{}, errs.NotFound(scope)
}

// ── handlers ──────────────────────────────────────────────────────────────

func (m *Module) publicSite(ctx context.Context, tx pgx.Tx, s *site, _ *http.Request) (any, error) {
	out := CmsPublicSite{PropertyID: s.pid, PropertyCode: s.code, Name: s.name, DefaultLanguage: s.pol.DefaultLanguage, Fallback: s.pol.FallbackToDefault,
		Revision: s.revision, ChangedAt: s.changedAt, WebsiteURL: s.base}
	for _, l := range s.pol.SupportedLanguages {
		name := LanguageNames[l]
		if name == "" {
			name = l
		}
		out.Languages = append(out.Languages, CmsLanguage{Code: l, Name: name, Default: l == s.pol.DefaultLanguage})
	}
	return out, nil
}

func (m *Module) publicPage(ctx context.Context, tx pgx.Tx, s *site, r *http.Request) (any, error) {
	slug := strings.ToLower(chi.URLParam(r, "slug"))
	row, err := findBySlug(ctx, tx, s, "page", slug)
	if errs.Is(err, errs.KindNotFound) {
		// home and other keyed pages are also found by key
		rows, lerr := handle.List[publishedRow](tx.Query(ctx, publishedSelect+` WHERE c.property_id = $1 AND c.kind = 'page' AND c.key = $2 AND `+visible, s.pid, slug))
		if lerr != nil {
			return nil, lerr
		}
		if len(rows) == 0 {
			return nil, errs.NotFound("page")
		}
		row, err = rows[0], nil
	}
	if err != nil {
		return nil, err
	}
	return m.render(ctx, tx, s, row.CmsContent, row.Document, deref(row.PublishedVersion), false)
}

func (m *Module) publicPages(ctx context.Context, tx pgx.Tx, s *site, _ *http.Request) (any, error) {
	rows, err := handle.List[publishedRow](tx.Query(ctx, publishedSelect+` WHERE c.property_id = $1 AND c.kind = 'page' AND `+visible+`
		ORDER BY c.sort_order, c.title`, s.pid))
	if err != nil {
		return nil, err
	}
	out := CmsPublicList{Language: s.lang, Items: []CmsPublicSummary{}, Revision: s.revision}
	for _, p := range rows {
		if sum, ok := m.summary(p, s, nil); ok {
			out.Items = append(out.Items, sum)
		}
	}
	return out, nil
}

// summary builds a list entry for the served language.
func (m *Module) summary(p publishedRow, s *site, rd *renderer) (CmsPublicSummary, bool) {
	spec := specOf(p.Kind)
	lang, t, ok := translationFor(p.Document, s, spec.SlugScope != "")
	if !ok {
		return CmsPublicSummary{}, false
	}
	sum := CmsPublicSummary{ID: p.ID, Kind: p.Kind, Key: p.Key, Template: p.Template, ParentID: p.ParentID, Language: lang, Title: t.Title, Slug: t.Slug,
		Path: contentPath(p.Kind, lang, t.Slug, p.Key, p.Template), Paths: map[string]string{}, Summary: t.Summary, Tags: p.Tags, Featured: p.Featured,
		PublishedAt: p.PublishedAt, ImageCount: len(p.Document.Items)}
	for l, tr := range p.Document.Translations {
		if tr.Title != "" && (spec.SlugScope == "" || tr.Slug != "") {
			sum.Paths[l] = contentPath(p.Kind, l, tr.Slug, p.Key, p.Template)
		}
	}
	if p.Kind == KindArticle {
		sum.Date = articleDate(p.CmsContent)
	}
	if rd != nil {
		mid, alt := t.MediaID, t.Alt
		if mid == nil {
			def := p.Document.Translations[s.pol.DefaultLanguage]
			mid, alt = def.MediaID, def.Alt
		}
		if mid != nil {
			sum.Image = rd.mediaOf(*mid, alt, "")
		} else if p.Kind == KindGallery && len(p.Document.Items) > 0 {
			sum.Image = rd.mediaOf(p.Document.Items[0].MediaID, "", "")
		}
	}
	return sum, true
}

func (m *Module) publicNews(ctx context.Context, tx pgx.Tx, s *site, r *http.Request) (any, error) {
	q := r.URL.Query()
	limit := min(max(handle.QueryInt(r, "limit", 12), 1), 50)
	offset := 0
	if c, err := httpx.DecodeCursor(q.Get("cursor")); err == nil && c != "" {
		offset, _ = strconv.Atoi(c)
	}
	featured := q.Get("featured") == "true"
	rows, err := handle.List[publishedRow](tx.Query(ctx, publishedSelect+` LEFT JOIN cms.categories cat ON cat.id = c.category_id
		WHERE c.property_id = $1 AND c.kind = 'article' AND `+visible+` AND ($2 = '' OR cat.code = upper($2)) AND ($3 = '' OR $3 = ANY(c.tags))
		AND (NOT $4 OR c.featured)
		ORDER BY coalesce(c.display_date, c.first_published_at::date) DESC, c.first_published_at DESC, c.id LIMIT $5 OFFSET $6`,
		s.pid, q.Get("category"), strings.ToLower(q.Get("tag")), featured, limit+1, offset))
	if err != nil {
		return nil, err
	}
	out, err := m.list(ctx, tx, s, rows, limit, offset)
	if err != nil {
		return nil, err
	}
	cats, err := handle.List[struct {
		Code   string            `db:"code"`
		Name   string            `db:"name"`
		Labels map[string]string `db:"labels"`
	}](tx.Query(ctx, `SELECT code, name, labels FROM cms.categories WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL ORDER BY sort_order, name`, s.pid))
	if err != nil {
		return nil, err
	}
	out.Categories = []CmsPublicCategory{}
	for _, c := range cats {
		label := c.Labels[s.lang]
		if label == "" {
			label = c.Name
		}
		out.Categories = append(out.Categories, CmsPublicCategory{Code: c.Code, Label: label})
	}
	return out, nil
}

func (m *Module) list(ctx context.Context, tx pgx.Tx, s *site, rows []publishedRow, limit, offset int) (CmsPublicList, error) {
	out := CmsPublicList{Language: s.lang, Items: []CmsPublicSummary{}, Revision: s.revision}
	if len(rows) > limit {
		rows = rows[:limit]
		out.NextCursor = httpx.EncodeCursor(strconv.Itoa(offset + limit))
	}
	rd := newRenderer(ctx, tx, s)
	var docs []CmsDocument
	for _, p := range rows {
		docs = append(docs, CmsDocument{Translations: p.Document.Translations, Items: firstItem(p.Document.Items)})
	}
	if err := rd.load(docs...); err != nil {
		return out, err
	}
	cats := map[uuid.UUID]*CmsPublicCategory{}
	for _, p := range rows {
		sum, ok := m.summary(p, s, rd)
		if !ok {
			continue
		}
		if p.CategoryID != nil {
			if _, done := cats[*p.CategoryID]; !done {
				c, err := category(ctx, tx, *p.CategoryID, s.lang, s.pol.DefaultLanguage)
				if err != nil {
					return out, err
				}
				cats[*p.CategoryID] = c
			}
			sum.Category = cats[*p.CategoryID]
		}
		out.Items = append(out.Items, sum)
	}
	return out, nil
}

func firstItem(items []CmsGalleryItem) []CmsGalleryItem {
	if len(items) == 0 {
		return nil
	}
	return items[:1]
}

func (m *Module) publicArticle(ctx context.Context, tx pgx.Tx, s *site, r *http.Request) (any, error) {
	row, err := findBySlug(ctx, tx, s, "article", strings.ToLower(chi.URLParam(r, "slug")))
	if err != nil {
		return nil, err
	}
	return m.render(ctx, tx, s, row.CmsContent, row.Document, deref(row.PublishedVersion), false)
}

func (m *Module) publicGalleries(ctx context.Context, tx pgx.Tx, s *site, r *http.Request) (any, error) {
	limit := min(max(handle.QueryInt(r, "limit", 24), 1), 50)
	offset := 0
	if c, err := httpx.DecodeCursor(r.URL.Query().Get("cursor")); err == nil && c != "" {
		offset, _ = strconv.Atoi(c)
	}
	rows, err := handle.List[publishedRow](tx.Query(ctx, publishedSelect+` WHERE c.property_id = $1 AND c.kind = 'gallery' AND `+visible+`
		ORDER BY c.featured DESC, c.sort_order, c.first_published_at DESC, c.id LIMIT $2 OFFSET $3`, s.pid, limit+1, offset))
	if err != nil {
		return nil, err
	}
	return m.list(ctx, tx, s, rows, limit, offset)
}

func (m *Module) publicGallery(ctx context.Context, tx pgx.Tx, s *site, r *http.Request) (any, error) {
	row, err := findBySlug(ctx, tx, s, "gallery", strings.ToLower(chi.URLParam(r, "slug")))
	if err != nil {
		return nil, err
	}
	return m.render(ctx, tx, s, row.CmsContent, row.Document, deref(row.PublishedVersion), false)
}

func (m *Module) publicBanners(ctx context.Context, tx pgx.Tx, s *site, r *http.Request) (any, error) {
	q := r.URL.Query()
	placement := q.Get("placement")
	if placement != "" && !slices.Contains(BannerPlacements, placement) {
		return nil, errs.BadRequest("invalid_placement", "placement must be one of: "+strings.Join(BannerPlacements, ", "))
	}
	var page *uuid.UUID
	if p := strings.ToLower(q.Get("page")); p != "" {
		var pid uuid.UUID
		err := tx.QueryRow(ctx, `SELECT c.id FROM cms.contents c LEFT JOIN cms.content_slugs sl ON sl.content_id = c.id AND sl.scope = 'page'
			WHERE c.property_id = $1 AND c.kind = 'page' AND c.archived_at IS NULL AND (c.key = $2 OR sl.slug = $2 OR c.id::text = $2) LIMIT 1`, s.pid, p).Scan(&pid)
		if err != nil && !dbtx.IsNoRows(err) {
			return nil, err
		}
		if err == nil {
			page = &pid
		}
	}
	rows, err := handle.List[publishedRow](tx.Query(ctx, publishedSelect+` WHERE c.property_id = $1 AND c.kind = 'banner' AND `+visible+`
		AND ($2 = '' OR c.placement = $2) AND (cardinality(c.page_ids) = 0 OR $3::uuid = ANY(c.page_ids))
		ORDER BY c.placement, c.sort_order, c.published_at DESC`, s.pid, placement, page))
	if err != nil {
		return nil, err
	}
	rd := newRenderer(ctx, tx, s)
	var docs []CmsDocument
	for _, b := range rows {
		docs = append(docs, b.Document)
	}
	if err := rd.load(docs...); err != nil {
		return nil, err
	}
	var linkPages []uuid.UUID
	for _, b := range rows {
		if b.Link != nil && b.Link.PageID != nil {
			linkPages = append(linkPages, *b.Link.PageID)
		}
	}
	if err := rd.loadPages(linkPages); err != nil {
		return nil, err
	}
	out := CmsPublicBanners{Language: s.lang, Items: []CmsPublicBanner{}, Revision: s.revision}
	for _, b := range rows {
		lang, t, ok := translationFor(b.Document, s, false)
		if !ok {
			continue
		}
		pb := CmsPublicBanner{ID: b.ID, Key: b.Key, Placement: deref(b.Placement), Language: lang, Title: t.Title, Subtitle: t.Summary,
			ButtonLabel: t.ButtonLabel, SortOrder: b.SortOrder, StartsAt: b.PublishedAt, EndsAt: b.UnpublishAt}
		mid := t.MediaID
		if mid == nil {
			mid = b.Document.Translations[s.pol.DefaultLanguage].MediaID
		}
		if mid != nil {
			pb.Image = rd.mediaOf(*mid, t.Alt, "")
		}
		if b.Link != nil {
			pb.Link = rd.link(b.Link)
		}
		out.Items = append(out.Items, pb)
	}
	return out, nil
}

func (m *Module) publicNavigation(ctx context.Context, tx pgx.Tx, s *site, r *http.Request) (any, error) {
	loc := r.URL.Query().Get("location")
	rows, err := handle.List[struct {
		Code     string        `db:"code"`
		Name     string        `db:"name"`
		Location string        `db:"location"`
		Items    []CmsMenuItem `db:"items"`
	}](tx.Query(ctx, `SELECT code, name, location, items FROM cms.menus WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL
		AND ($2 = '' OR location = $2) ORDER BY location, code`, s.pid, loc))
	if err != nil {
		return nil, err
	}
	rd := newRenderer(ctx, tx, s)
	var pages []uuid.UUID
	var walk func(items []CmsMenuItem)
	walk = func(items []CmsMenuItem) {
		for _, it := range items {
			if u, err := uuid.Parse(it.PageID); err == nil {
				pages = append(pages, u)
			}
			walk(it.Children)
		}
	}
	for _, mn := range rows {
		walk(mn.Items)
	}
	if err := rd.loadPages(uniqueIDs(pages)); err != nil {
		return nil, err
	}
	out := CmsPublicNavigation{Language: s.lang, Menus: []CmsPublicMenu{}, Revision: s.revision}
	var resolve func(items []CmsMenuItem) []CmsPublicNavItem
	resolve = func(items []CmsMenuItem) []CmsPublicNavItem {
		res := []CmsPublicNavItem{}
		for _, it := range items {
			if len(it.Languages) > 0 && !slices.Contains(it.Languages, s.lang) {
				continue
			}
			label := pick(it.Label, s.lang, s.pol.DefaultLanguage)
			ni := CmsPublicNavItem{ID: it.ID, Label: label, Type: it.Type, RouteKey: it.RouteKey, NewTab: it.NewTab}
			switch it.Type {
			case "page":
				u, err := uuid.Parse(it.PageID)
				ref, ok := rd.pages[u]
				if err != nil || !ok {
					continue // page not live: hidden from the navigation
				}
				ni.Href, _ = rd.pathOf(ref)
			case "route":
				ni.Href = routePath(s.lang, it.RouteKey)
			default:
				ni.Href, ni.External = it.URL, isAbsoluteURL(it.URL)
			}
			ni.Children = resolve(it.Children)
			res = append(res, ni)
		}
		return res
	}
	for _, mn := range rows {
		out.Menus = append(out.Menus, CmsPublicMenu{Code: mn.Code, Name: mn.Name, Location: mn.Location, Items: resolve(mn.Items)})
	}
	return out, nil
}

func (m *Module) publicContact(ctx context.Context, tx pgx.Tx, s *site, _ *http.Request) (any, error) {
	rows, err := handle.List[struct {
		Code         string                       `db:"code"`
		Name         string                       `db:"name"`
		IsPrimary    bool                         `db:"is_primary"`
		Address      *string                      `db:"address"`
		Phones       []string                     `db:"phones"`
		WhatsApp     *string                      `db:"whatsapp"`
		Email        *string                      `db:"email"`
		Latitude     *string                      `db:"latitude"`
		Longitude    *string                      `db:"longitude"`
		MapURL       *string                      `db:"map_url"`
		OpeningHours []CmsOpeningHours            `db:"opening_hours"`
		SocialLinks  map[string]string            `db:"social_links"`
		Translations map[string]map[string]string `db:"translations"`
	}](tx.Query(ctx, `SELECT code, name, is_primary, address, phones, whatsapp, email, trim_scale(latitude)::text AS latitude,
		trim_scale(longitude)::text AS longitude, map_url, opening_hours, social_links, translations FROM cms.contacts
		WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL ORDER BY is_primary DESC, sort_order, name`, s.pid))
	if err != nil {
		return nil, err
	}
	out := CmsPublicContacts{Language: s.lang, Contacts: []CmsPublicContact{}, Revision: s.revision}
	for _, c := range rows {
		pc := CmsPublicContact{Code: c.Code, Name: c.Name, Primary: c.IsPrimary, Address: deref(c.Address), Phones: c.Phones, WhatsApp: deref(c.WhatsApp),
			Email: deref(c.Email), Latitude: c.Latitude, Longitude: c.Longitude, MapURL: deref(c.MapURL), OpeningHours: c.OpeningHours,
			SocialLinks: c.SocialLinks}
		if pc.Phones == nil {
			pc.Phones = []string{}
		}
		if pc.OpeningHours == nil {
			pc.OpeningHours = []CmsOpeningHours{}
		}
		if pc.SocialLinks == nil {
			pc.SocialLinks = map[string]string{}
		}
		if tr, ok := c.Translations[s.lang]; ok {
			if tr["name"] != "" {
				pc.Name = tr["name"]
			}
			if tr["address"] != "" {
				pc.Address = tr["address"]
			}
			pc.Note = tr["note"]
		}
		if pc.WhatsApp != "" {
			digits := strings.Map(func(r rune) rune {
				if r >= '0' && r <= '9' {
					return r
				}
				return -1
			}, pc.WhatsApp)
			if strings.HasPrefix(digits, "0") {
				digits = "62" + digits[1:]
			}
			pc.WhatsAppURL = "https://wa.me/" + digits
		}
		out.Contacts = append(out.Contacts, pc)
	}
	return out, nil
}

func (m *Module) publicCourseGuide(ctx context.Context, tx pgx.Tx, s *site, r *http.Request) (any, error) {
	rows, err := handle.List[struct {
		CourseCode   string                       `db:"course_code"`
		HoleNumber   int                          `db:"hole_number"`
		Title        *string                      `db:"title"`
		Translations map[string]map[string]string `db:"translations"`
		MediaIDs     []string                     `db:"media_ids"`
	}](tx.Query(ctx, `SELECT course_code, hole_number, title, translations, media_ids FROM cms.course_guides
		WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL AND ($2 = '' OR course_code = upper($2))
		ORDER BY course_code, sort_order, hole_number`, s.pid, r.URL.Query().Get("course")))
	if err != nil {
		return nil, err
	}
	rd := newRenderer(ctx, tx, s)
	var ids []uuid.UUID
	for _, h := range rows {
		for _, x := range h.MediaIDs {
			if u, err := uuid.Parse(x); err == nil {
				ids = append(ids, u)
			}
		}
	}
	mm, err := mediaMap(ctx, tx, s.pid, uniqueIDs(ids))
	if err != nil {
		return nil, err
	}
	rd.media = mm
	out := CmsPublicCourseGuide{Language: s.lang, GolfInfo: "/api/v1/public/golf/info?property=" + s.code, Holes: []CmsPublicHole{}, Revision: s.revision}
	for _, h := range rows {
		tr := h.Translations[s.lang]
		def := h.Translations[s.pol.DefaultLanguage]
		get := func(k string) string {
			if tr[k] != "" {
				return tr[k]
			}
			return def[k]
		}
		ph := CmsPublicHole{CourseCode: h.CourseCode, HoleNumber: h.HoleNumber, Title: get("title"), Description: get("description"), Tips: get("tips"),
			Media: []CmsPublicMedia{}}
		if ph.Title == "" {
			ph.Title = deref(h.Title)
		}
		for _, x := range h.MediaIDs {
			if u, err := uuid.Parse(x); err == nil {
				if md := rd.mediaOf(u, "", ""); md != nil {
					ph.Media = append(ph.Media, *md)
				}
			}
		}
		out.Holes = append(out.Holes, ph)
	}
	return out, nil
}

func (m *Module) sitemap(ctx context.Context, tx pgx.Tx, s *site) (CmsSitemap, error) {
	rows, err := handle.List[publishedRow](tx.Query(ctx, publishedSelect+` WHERE c.property_id = $1 AND c.kind IN ('page', 'article', 'gallery')
		AND c.show_in_sitemap AND `+visible+` ORDER BY c.kind, c.sort_order, c.title`, s.pid))
	if err != nil {
		return CmsSitemap{}, err
	}
	out := CmsSitemap{Entries: []CmsSitemapEntry{}, Revision: s.revision}
	seen := map[string]bool{}
	for _, p := range rows {
		spec := specOf(p.Kind)
		var alts []CmsAlternate
		for _, l := range s.pol.SupportedLanguages {
			t, ok := p.Document.Translations[l]
			if !ok || t.Title == "" || (spec.SlugScope != "" && t.Slug == "") || (t.SEO != nil && t.SEO.NoIndex) {
				continue
			}
			alts = append(alts, CmsAlternate{Language: l, Href: s.url(contentPath(p.Kind, l, t.Slug, p.Key, p.Template))})
		}
		for _, a := range alts {
			if seen[a.Href] {
				continue
			}
			seen[a.Href] = true
			freq, prio := "weekly", "0.6"
			switch {
			case p.Kind == KindArticle:
				freq, prio = "monthly", "0.5"
			case deref(p.Template) == "home" || deref(p.Key) == "home":
				freq, prio = "daily", "1.0"
			}
			lm := s.changedAt
			if p.PublishedAt != nil {
				lm = *p.PublishedAt
			}
			out.Entries = append(out.Entries, CmsSitemapEntry{Loc: a.Href, Path: strings.TrimPrefix(a.Href, s.base), Language: a.Language, LastMod: lm.UTC(),
				ChangeFreq: freq, Priority: prio, Alternates: alts})
		}
	}
	// Structured website pages (rates, booking …) not managed as CMS pages.
	keys := routeKeyNames()
	for _, k := range keys {
		if strings.HasPrefix(k, "book_") || k == "member_portal" {
			continue
		}
		var alts []CmsAlternate
		for _, l := range s.pol.SupportedLanguages {
			alts = append(alts, CmsAlternate{Language: l, Href: s.url(routePath(l, k))})
		}
		for _, a := range alts {
			if seen[a.Href] {
				continue
			}
			seen[a.Href] = true
			out.Entries = append(out.Entries, CmsSitemapEntry{Loc: a.Href, Path: strings.TrimPrefix(a.Href, s.base), Language: a.Language,
				LastMod: s.changedAt.UTC(), ChangeFreq: "weekly", Priority: "0.5", Alternates: alts})
		}
	}
	sort.SliceStable(out.Entries, func(i, j int) bool { return out.Entries[i].Loc < out.Entries[j].Loc })
	return out, nil
}

func (m *Module) publicSitemap(ctx context.Context, tx pgx.Tx, s *site, _ *http.Request) (any, error) {
	return m.sitemap(ctx, tx, s)
}

type xmlURLSet struct {
	XMLName xml.Name `xml:"urlset"`
	NS      string   `xml:"xmlns,attr"`
	XHTML   string   `xml:"xmlns:xhtml,attr"`
	URLs    []xmlURL `xml:"url"`
}

type xmlURL struct {
	Loc        string    `xml:"loc"`
	LastMod    string    `xml:"lastmod"`
	ChangeFreq string    `xml:"changefreq"`
	Priority   string    `xml:"priority"`
	Links      []xmlLink `xml:"xhtml:link"`
}

type xmlLink struct {
	Rel      string `xml:"rel,attr"`
	HrefLang string `xml:"hreflang,attr"`
	Href     string `xml:"href,attr"`
}

// sitemapXML serves sitemap.xml with hreflang alternates.
func (m *Module) sitemapXML(w http.ResponseWriter, r *http.Request) {
	ctx := dbtx.System(r.Context())
	var sm CmsSitemap
	var s *site
	err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		if s, err = m.resolveSite(ctx, tx, r); err != nil {
			return err
		}
		sctx := dbtx.WithScope(r.Context(), dbtx.Scope{PropertyIDs: []uuid.UUID{s.pid}})
		if err := dbtx.ApplyScope(sctx, tx); err != nil {
			return err
		}
		sm, err = m.sitemap(sctx, tx, s)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	set := xmlURLSet{NS: "http://www.sitemaps.org/schemas/sitemap/0.9", XHTML: "http://www.w3.org/1999/xhtml"}
	for _, e := range sm.Entries {
		u := xmlURL{Loc: e.Loc, LastMod: e.LastMod.Format("2006-01-02"), ChangeFreq: e.ChangeFreq, Priority: e.Priority}
		for _, a := range e.Alternates {
			u.Links = append(u.Links, xmlLink{Rel: "alternate", HrefLang: a.Language, Href: a.Href})
		}
		set.URLs = append(set.URLs, u)
	}
	body, err := xml.MarshalIndent(set, "", "  ")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Last-Modified", s.changedAt.UTC().Format(http.TimeFormat))
	_, _ = w.Write(append([]byte(xml.Header), body...))
}

func (m *Module) publicRobots(_ context.Context, _ pgx.Tx, s *site, _ *http.Request) (any, error) {
	out := CmsRobots{AllowIndexing: s.pol.AllowIndexing, Disallow: append([]string{"/api/"}, s.pol.RobotsDisallow...), Sitemap: s.url("/sitemap.xml")}
	var b strings.Builder
	b.WriteString("User-agent: *\n")
	if !out.AllowIndexing {
		b.WriteString("Disallow: /\n")
	} else {
		for _, d := range out.Disallow {
			b.WriteString("Disallow: " + d + "\n")
		}
	}
	b.WriteString("Sitemap: " + out.Sitemap + "\n")
	out.Text = b.String()
	return out, nil
}

func (m *Module) publicRedirects(ctx context.Context, tx pgx.Tx, s *site, _ *http.Request) (any, error) {
	items, err := handle.List[CmsPublicRedirect](tx.Query(ctx, `SELECT from_path, to_path, status_code FROM cms.redirects
		WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL ORDER BY from_path`, s.pid))
	return CmsPublicRedirects{Items: items, Revision: s.revision}, err
}
