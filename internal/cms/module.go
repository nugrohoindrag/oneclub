// Package cms is Landing Page & CMS (PRD P4 EP-24, layer "Public Channel",
// Technical Doc §4.1): pages, news articles, banners and gallery albums with
// versions, bilingual content, approval workflow and scheduling; the images
// library; navigation menus, redirects, contact information and course
// guide texts; and the public website API.
//
// The CMS never copies or writes the data of other domains (PRD P4 §5.4.1):
// rates, packages, promotions, events, tournaments, hall of fame and the
// course data are referenced by data blocks (source + filter, contract K5)
// and fetched by the website from the public API of the owning module.
package cms

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/resource"
	"oneclub/internal/platform/rules"
	"oneclub/internal/platform/storage"
)

// Publisher publishes domain events (outbox.Bus).
type Publisher interface {
	Publish(ctx context.Context, tx pgx.Tx, eventType, aggregateType string, aggregateID, propertyID *uuid.UUID, payload any) (uuid.UUID, error)
}

var _ Publisher = (*outbox.Bus)(nil)

// Module is the CMS module.
type Module struct {
	DB        *dbtx.DB
	Events    Publisher
	Approvals *approval.Engine
	Files     *storage.Files
	Notify    notify.Sender
	Cfg       *config.Config
	// Enabled reports whether a module is enabled (public endpoints answer
	// 404 while the CMS module is disabled).
	Enabled func(ctx context.Context, module string) (bool, error)
	// Now is the clock (tests use the real clock; jobs pass explicit times).
	Now func() time.Time
}

func (m *Module) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now().UTC()
}

// ── content kinds ─────────────────────────────────────────────────────────

// Content kinds.
const (
	KindPage    = "page"
	KindArticle = "article"
	KindBanner  = "banner"
	KindGallery = "gallery"
)

type kindSpec struct {
	Kind      string
	Plural    string
	Name      string // UI label
	Path      string // Back Office API base path
	Perm      string // permission prefix
	SlugScope string // URL space of the slug ("" = no slug)
	URLPrefix string // website path below /{lang}/
	Blocks    bool
	Items     bool
}

var kindSpecs = []kindSpec{
	{Kind: KindPage, Plural: "pages", Name: "Page", Path: "/api/v1/cms/pages", Perm: "cms.page", SlugScope: "page", Blocks: true},
	{Kind: KindArticle, Plural: "articles", Name: "News Article", Path: "/api/v1/cms/articles", Perm: "cms.article", SlugScope: "article", URLPrefix: "news/", Blocks: true},
	{Kind: KindBanner, Plural: "banners", Name: "Banner", Path: "/api/v1/cms/banners", Perm: "cms.banner"},
	{Kind: KindGallery, Plural: "galleries", Name: "Gallery Album", Path: "/api/v1/cms/galleries", Perm: "cms.gallery", SlugScope: "gallery", URLPrefix: "gallery/", Items: true},
}

func specOf(kind string) kindSpec {
	for _, s := range kindSpecs {
		if s.Kind == kind {
			return s
		}
	}
	return kindSpec{Kind: kind}
}

// PageTemplates are the page templates of the public website (Naming
// Convention §26 public navigation and Product Overview §38 public pages).
var PageTemplates = []string{"home", "standard", "landing", "golf", "course_guide", "sport_club", "bungalow", "vip_suite", "meeting_mice",
	"wedding_banquet", "events", "membership", "packages", "promotions", "hall_of_fame", "news", "gallery", "contact", "location"}

// BannerPlacements are the banner slots of the website.
var BannerPlacements = []string{"home_hero", "home_highlight", "page_header", "page_inline", "sidebar", "popup", "announcement_bar", "footer"}

// RouteKeys are the structured website routes a navigation item, CTA or
// banner may link to (path below /{lang}/; Naming Convention §26 incl. the
// booking entry points).
var RouteKeys = map[string]string{
	"home": "", "golf": "golf", "course_guide": "golf/course-guide", "hole_by_hole": "golf/hole-by-hole", "handicap": "golf/handicap",
	"facilities": "golf/facilities", "reciprocal_clubs": "golf/reciprocal-clubs", "sport_club": "sport-club", "bungalow": "bungalow",
	"vip_suite": "vip-suite", "meeting_mice": "meeting", "wedding_banquet": "wedding-banquet", "events": "events", "membership": "membership",
	"packages": "packages", "promotions": "promotions", "hall_of_fame": "hall-of-fame", "tournaments": "tournaments", "news": "news", "gallery": "gallery",
	"contact": "contact", "location": "location", "book_golf": "book-golf", "book_sport_club": "sport-club/book", "book_bungalow": "bungalow/book",
	"book_meeting_room": "meeting/book", "book_event": "events/book", "member_portal": "member",
}

func routeKeyNames() []string {
	out := make([]string, 0, len(RouteKeys))
	for k := range RouteKeys {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// ── Content Policies (PRD P4 EP-27, open question #15) ──────────────────────

// ContentPolicy is the Content Policies document of the website.
type ContentPolicy struct {
	RequireApproval          bool     `json:"requireApproval" doc:"Publishing needs an approved review (Approval Workflows → Website Content)"`
	DefaultLanguage          string   `json:"defaultLanguage" doc:"Language of the main content and the fallback"`
	SupportedLanguages       []string `json:"supportedLanguages" doc:"Languages of the website (Languages menu)"`
	FallbackToDefault        bool     `json:"fallbackToDefault" doc:"Serve the default language when a translation is missing"`
	RequiredLanguages        []string `json:"requiredLanguages" doc:"Languages that must be complete before a review can be submitted"`
	PreviewLinkHours         int      `json:"previewLinkHours" doc:"Validity of shared preview links"`
	AutoRedirectOnSlugChange bool     `json:"autoRedirectOnSlugChange" doc:"Create a 301 redirect when a published slug changes"`
	AllowIndexing            bool     `json:"allowIndexing" doc:"robots: allow search engines (switch off on staging)"`
	RobotsDisallow           []string `json:"robotsDisallow" doc:"Paths search engines must not crawl"`
	MaxUploadMB              int      `json:"maxUploadMb" doc:"Maximum image upload size"`
}

// DefaultContentPolicy follows PRD P4 open question #15 (Marketing Staff
// manage the website, publishing needs approval) and FR-CMS-05 (ID/EN).
var DefaultContentPolicy = ContentPolicy{RequireApproval: true, DefaultLanguage: "id", SupportedLanguages: []string{"id", "en"},
	FallbackToDefault: true, RequiredLanguages: []string{"id"}, PreviewLinkHours: 72, AutoRedirectOnSlugChange: true, AllowIndexing: true,
	RobotsDisallow: []string{"/booking/", "/member/"}, MaxUploadMB: 10}

// PolicyCode is the Content Policies code (Settings → Club Policies).
const PolicyCode = "cms.content"

// PolicyCategory is the Club Policies category (Naming Convention §33 pattern).
const PolicyCategory = "Content Policies"

func init() {
	rules.RegisterPolicy(rules.PolicyDef{Code: PolicyCode, Category: PolicyCategory, Name: "Website content publishing",
		Description: "Approval before publishing, website languages (default, supported, required), preview links, redirects, indexing and uploads",
		Default:     DefaultContentPolicy})
	if !slices.Contains(rules.PolicyCategories, PolicyCategory) {
		rules.PolicyCategories = append(rules.PolicyCategories, PolicyCategory)
	}
}

// LanguageNames labels the common website languages.
var LanguageNames = map[string]string{"id": "Bahasa Indonesia", "en": "English", "zh": "中文", "ja": "日本語", "ko": "한국어"}

// Policy returns the Content Policies in force for a property, normalised.
func Policy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (ContentPolicy, error) {
	p, _, err := rules.PolicyAt(ctx, q, PolicyCode, property, DefaultContentPolicy)
	if err != nil {
		return p, err
	}
	return p.normalize(), nil
}

func (p ContentPolicy) normalize() ContentPolicy {
	clean := func(in []string) []string {
		var out []string
		for _, l := range in {
			l = strings.ToLower(strings.TrimSpace(l))
			if langRe.MatchString(l) && !slices.Contains(out, l) {
				out = append(out, l)
			}
		}
		return out
	}
	p.DefaultLanguage = strings.ToLower(strings.TrimSpace(p.DefaultLanguage))
	if !langRe.MatchString(p.DefaultLanguage) {
		p.DefaultLanguage = "id"
	}
	p.SupportedLanguages = clean(p.SupportedLanguages)
	if !slices.Contains(p.SupportedLanguages, p.DefaultLanguage) {
		p.SupportedLanguages = append([]string{p.DefaultLanguage}, p.SupportedLanguages...)
	}
	var req []string
	for _, l := range clean(p.RequiredLanguages) {
		if slices.Contains(p.SupportedLanguages, l) {
			req = append(req, l)
		}
	}
	if !slices.Contains(req, p.DefaultLanguage) {
		req = append([]string{p.DefaultLanguage}, req...)
	}
	p.RequiredLanguages = req
	if p.PreviewLinkHours <= 0 || p.PreviewLinkHours > 24*30 {
		p.PreviewLinkHours = 72
	}
	if p.MaxUploadMB <= 0 || p.MaxUploadMB > 25 {
		p.MaxUploadMB = 10
	}
	if p.RobotsDisallow == nil {
		p.RobotsDisallow = []string{}
	}
	return p
}

// ── approval document type ────────────────────────────────────────────────

// ContentDocumentType is the approval document type of website content
// (FR-CMS-06): without a configured workflow a review is approved at once.
var ContentDocumentType = provision.DocumentType{Code: "cms_content", Module: "cms", Name: "Website Content Publication",
	Attributes: []provision.DocumentAttribute{
		{Key: "kind", Label: "Content type (page, article, banner, gallery)", Type: "string"},
		{Key: "template", Label: "Page template", Type: "string"},
		{Key: "languages", Label: "Number of languages", Type: "number"},
		{Key: "firstPublication", Label: "First publication (1 = yes)", Type: "number"},
	}}

// Templates are the notification templates of the CMS.
func Templates() []provision.Template {
	t := map[string][2]string{
		"en": {"Published on the website: {{.title}}", "{{.kindLabel}} \"{{.title}}\" (version {{.version}}) is now live on the website ({{.path}})."},
		"id": {"Tayang di website: {{.title}}", "{{.kindLabel}} \"{{.title}}\" (versi {{.version}}) sekarang tayang di website ({{.path}})."},
	}
	var out []provision.Template
	for loc, c := range t {
		for _, ch := range []string{"email", "in_app"} {
			out = append(out, provision.Template{Event: "cms.content_published", Channel: ch, Locale: loc, Subject: c[0], Body: c[1]})
		}
	}
	return out
}

// ── catalogue ─────────────────────────────────────────────────────────────

// Contribution returns the CMS permissions and their role templates.
func Contribution() catalog.Contribution {
	defs := resourceDefs()
	perms := resource.Permissions(defs...)
	var all []string
	for _, k := range kindSpecs {
		_, obj, _ := strings.Cut(k.Perm, ".")
		perms = append(perms, catalog.P("cms", obj, "view", "create", "update", "delete", "publish")...)
		for _, a := range []string{"view", "create", "update", "delete", "publish"} {
			all = append(all, k.Perm+"."+a)
		}
	}
	perms = append(perms, catalog.P("cms", "media", "view", "create", "update", "delete")...)
	perms = append(perms, catalog.P("cms", "language", "view")...)
	perms = append(perms, catalog.P("cms", "import", "create")...)
	all = append(all, "cms.media.view", "cms.media.create", "cms.media.update", "cms.media.delete", "cms.language.view", "cms.import.create")
	all = append(all, resource.AllActions(defs...)...)
	view := []string{catalog.ModuleAccess("cms"), "cms.page.view", "cms.article.view", "cms.banner.view", "cms.gallery.view", "cms.media.view",
		"cms.language.view", "cms.category.view", "cms.menu.view", "cms.redirect.view", "cms.contact.view", "cms.course_guide.view"}
	return catalog.Contribution{
		Permissions: perms,
		RolePermissions: map[string][]string{
			"marketing_staff": all,
			"property_admin":  append(append([]string{}, all...), catalog.ModuleAccess("cms")),
			"general_manager": view, // approver of website content in the demo workflow (open question #15)
			"club_manager":    view,
			"crm_admin":       {catalog.ModuleAccess("cms"), "cms.page.view", "cms.article.view", "cms.banner.view", "cms.contact.view"},
			"sales_executive": {catalog.ModuleAccess("cms"), "cms.page.view", "cms.contact.view"},
		},
	}
}

// ── helpers ───────────────────────────────────────────────────────────────

func actorID(ctx context.Context) *uuid.UUID {
	if p := authz.From(ctx); p != nil && p.UserID != uuid.Nil {
		return id.Ptr(p.UserID)
	}
	return nil
}

func actorName(ctx context.Context) string {
	if p := authz.From(ctx); p != nil && p.Name != "" {
		return p.Name
	}
	return "system"
}

func nz(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
