package cms

// Versioned website content (FR-CMS-01, -04, -05, -06, -08): pages, news
// articles, banners and gallery albums. Settings (template, parent, banner
// placement, schedule …) are edited in place; the content (translations per
// language, blocks, album images) is saved as immutable versions. The
// translation status per language is computed at every save: missing,
// incomplete (a translatable block text is missing), complete, or outdated
// (the default language changed after the translation).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// ── API types ─────────────────────────────────────────────────────────────

// CmsSEO are the search engine settings of a translation (FR-CMS-08).
type CmsSEO struct {
	MetaTitle       string     `json:"metaTitle,omitempty"`
	MetaDescription string     `json:"metaDescription,omitempty"`
	CanonicalURL    string     `json:"canonicalUrl,omitempty" doc:"Absolute URL or /path; default: the page URL"`
	OGImageID       *uuid.UUID `json:"ogImageId,omitempty" doc:"Open Graph image (media library)"`
	NoIndex         bool       `json:"noindex,omitempty"`
}

// CmsTranslation is the text of one language.
type CmsTranslation struct {
	Title       string     `json:"title"`
	Slug        string     `json:"slug,omitempty" doc:"URL slug per language (pages, news, albums); generated from the title when empty"`
	Summary     string     `json:"summary,omitempty" doc:"Page summary, news excerpt, banner subtitle or album description"`
	ButtonLabel string     `json:"buttonLabel,omitempty" doc:"Banner button label"`
	MediaID     *uuid.UUID `json:"mediaId,omitempty" doc:"Banner image, news featured image or album cover for this language"`
	Alt         string     `json:"alt,omitempty" doc:"Alternative text of the image (default: the media library alt text)"`
	SEO         *CmsSEO    `json:"seo,omitempty"`
}

// CmsBlock is one content block.
type CmsBlock struct {
	ID      string                    `json:"id,omitempty"`
	Type    string                    `json:"type" enum:"rich_text,image,gallery,video,cta,faq,contact_form,map,data,banner_slot"`
	Config  map[string]any            `json:"config,omitempty" doc:"Language-neutral settings (data block: source, filter, limit, layout)"`
	Content map[string]map[string]any `json:"content,omitempty" doc:"Texts per language"`
	Hidden  bool                      `json:"hidden,omitempty"`
}

// CmsGalleryItem is one image of a gallery album.
type CmsGalleryItem struct {
	MediaID uuid.UUID         `json:"mediaId"`
	Caption map[string]string `json:"caption,omitempty" doc:"Caption per language"`
}

// CmsDocument is the versioned content.
type CmsDocument struct {
	Translations map[string]CmsTranslation `json:"translations"`
	Blocks       []CmsBlock                `json:"blocks,omitempty"`
	Items        []CmsGalleryItem          `json:"items,omitempty" doc:"Gallery album images"`
}

// CmsSettings are the settings of a content item (not versioned).
type CmsSettings struct {
	Key           *string     `json:"key,omitempty" doc:"Page key (home, golf, …) or banner code; unique per type"`
	Template      *string     `json:"template,omitempty" doc:"Page template"`
	ParentID      *uuid.UUID  `json:"parentId,omitempty" doc:"Parent page (hierarchy, breadcrumbs)"`
	SortOrder     *int        `json:"sortOrder,omitempty"`
	ShowInSitemap *bool       `json:"showInSitemap,omitempty"`
	CategoryID    *uuid.UUID  `json:"categoryId,omitempty" doc:"News category"`
	Tags          []string    `json:"tags,omitempty" doc:"News tags"`
	AuthorName    *string     `json:"authorName,omitempty" doc:"News author"`
	Featured      *bool       `json:"featured,omitempty"`
	DisplayDate   *string     `json:"displayDate,omitempty" doc:"News publish date shown on the website (YYYY-MM-DD; default: first publication)"`
	Placement     *string     `json:"placement,omitempty" doc:"Banner placement"`
	PageIDs       []uuid.UUID `json:"pageIds,omitempty" doc:"Banner target pages (empty: every page showing the placement)"`
	Link          *CmsLink    `json:"link,omitempty" doc:"Banner link"`
	PublishAt     *time.Time  `json:"publishAt,omitempty" doc:"Go live at (after approval); banners: start of the display period"`
	UnpublishAt   *time.Time  `json:"unpublishAt,omitempty" doc:"Take down at; banners: end of the display period"`
	Clear         []string    `json:"clear,omitempty" doc:"Settings to clear: key, template, parentId, categoryId, authorName, displayDate, placement, pageIds, link, tags, publishAt, unpublishAt"`
}

// CmsContentInput creates a content item (settings + first version).
type CmsContentInput struct {
	CmsSettings
	CmsDocument
	Note string `json:"note,omitempty" doc:"Version note"`
}

// CmsContentSave saves a new version.
type CmsContentSave struct {
	CmsDocument
	Note        string `json:"note,omitempty"`
	BaseVersion *int   `json:"baseVersion,omitempty" doc:"Optimistic concurrency: the version the editor started from (or If-Match)"`
}

// CmsTranslationState is the translation status of one language.
type CmsTranslationState struct {
	Status string `json:"status" enum:"missing,incomplete,complete,outdated"`
	Title  string `json:"title,omitempty"`
	Slug   string `json:"slug,omitempty"`
	Path   string `json:"path,omitempty" doc:"Website path of this language"`
}

// CmsContent is a page, news article, banner or gallery album.
type CmsContent struct {
	ID                    uuid.UUID                      `json:"id" db:"id"`
	PropertyID            uuid.UUID                      `json:"propertyId" db:"property_id"`
	Kind                  string                         `json:"kind" db:"kind" enum:"page,article,banner,gallery"`
	Key                   *string                        `json:"key" db:"key"`
	Template              *string                        `json:"template" db:"template"`
	ParentID              *uuid.UUID                     `json:"parentId" db:"parent_id"`
	SortOrder             int                            `json:"sortOrder" db:"sort_order"`
	ShowInSitemap         bool                           `json:"showInSitemap" db:"show_in_sitemap"`
	CategoryID            *uuid.UUID                     `json:"categoryId" db:"category_id"`
	Tags                  []string                       `json:"tags" db:"tags"`
	AuthorName            *string                        `json:"authorName" db:"author_name"`
	Featured              bool                           `json:"featured" db:"featured"`
	DisplayDate           *string                        `json:"displayDate" db:"display_date"`
	Placement             *string                        `json:"placement" db:"placement"`
	PageIDs               []uuid.UUID                    `json:"pageIds" db:"page_ids"`
	Link                  *CmsLink                       `json:"link" db:"link"`
	Title                 string                         `json:"title" db:"title"`
	Translations          map[string]CmsTranslationState `json:"translations" db:"translation_status"`
	Status                string                         `json:"status" db:"status" enum:"draft,in_review,scheduled,published,unpublished"`
	StatusBeforeReview    *string                        `json:"statusBeforeReview" db:"status_before_review"`
	Live                  bool                           `json:"live" db:"live" doc:"The published version is on the website"`
	LatestVersion         int                            `json:"latestVersion" db:"latest_version"`
	ReviewVersion         *int                           `json:"reviewVersion" db:"review_version"`
	ApprovedVersion       *int                           `json:"approvedVersion" db:"approved_version"`
	PublishedVersion      *int                           `json:"publishedVersion" db:"published_version"`
	HasUnpublishedChanges bool                           `json:"hasUnpublishedChanges" db:"has_changes"`
	ApprovalRequestID     *uuid.UUID                     `json:"approvalRequestId" db:"approval_request_id"`
	PublishAt             *time.Time                     `json:"publishAt" db:"publish_at"`
	UnpublishAt           *time.Time                     `json:"unpublishAt" db:"unpublish_at"`
	PublishedAt           *time.Time                     `json:"publishedAt" db:"published_at"`
	FirstPublishedAt      *time.Time                     `json:"firstPublishedAt" db:"first_published_at"`
	UnpublishedAt         *time.Time                     `json:"unpublishedAt" db:"unpublished_at"`
	CreatedAt             time.Time                      `json:"createdAt" db:"created_at"`
	UpdatedAt             time.Time                      `json:"updatedAt" db:"updated_at"`
	ArchivedAt            *time.Time                     `json:"archivedAt" db:"archived_at"`
}

// CmsVersion is one saved version.
type CmsVersion struct {
	VersionNo     int            `json:"versionNo" db:"version_no"`
	Note          *string        `json:"note" db:"note"`
	Source        string         `json:"source" db:"source" enum:"create,edit,restore,rollback,import"`
	RestoredFrom  *int           `json:"restoredFrom" db:"restored_from"`
	ReviewStatus  *string        `json:"reviewStatus" db:"review_status" enum:"in_review,approved,rejected,withdrawn"`
	ApprovedAt    *time.Time     `json:"approvedAt" db:"approved_at"`
	PublishedAt   *time.Time     `json:"publishedAt" db:"published_at" doc:"First time this version went live"`
	CreatedAt     time.Time      `json:"createdAt" db:"created_at"`
	CreatedByName *string        `json:"createdByName" db:"created_by_name"`
	LangRev       map[string]int `json:"languageRevisions" db:"lang_rev" doc:"Version in which each language last changed"`
}

// CmsVersionDetail is a version with its content.
type CmsVersionDetail struct {
	CmsVersion
	Document CmsDocument `json:"document" db:"document"`
}

// CmsContentDetail is a content item with its latest version.
type CmsContentDetail struct {
	CmsContent
	Document CmsDocument `json:"document"`
	Version  CmsVersion  `json:"version"`
}

const contentSelect = `SELECT c.id, c.property_id, c.kind, c.key, c.template, c.parent_id, c.sort_order, c.show_in_sitemap, c.category_id, c.tags,
	c.author_name, c.featured, to_char(c.display_date, 'YYYY-MM-DD') AS display_date, c.placement, c.page_ids, c.link, c.title,
	c.translation_status, c.status, c.status_before_review, c.live, c.latest_version, c.review_version, c.approved_version, c.published_version,
	(c.latest_version > coalesce(c.published_version, 0)) AS has_changes, c.approval_request_id, c.publish_at, c.unpublish_at, c.published_at,
	c.first_published_at, c.unpublished_at, c.created_at, c.updated_at, c.archived_at
	FROM cms.contents c`

const versionSelect = `SELECT v.version_no, v.note, v.source, v.restored_from, v.review_status, v.approved_at, v.published_at, v.created_at,
	u.full_name AS created_by_name, v.lang_rev, v.document FROM cms.content_versions v LEFT JOIN platform.users u ON u.id = v.created_by`

const versionListSelect = `SELECT v.version_no, v.note, v.source, v.restored_from, v.review_status, v.approved_at, v.published_at, v.created_at,
	u.full_name AS created_by_name, v.lang_rev FROM cms.content_versions v LEFT JOIN platform.users u ON u.id = v.created_by`

// get loads a content item of a kind (optionally locked).
func get(ctx context.Context, q dbtx.Querier, kind string, cid uuid.UUID, lock bool) (CmsContent, error) {
	sql := contentSelect + ` WHERE c.id = $1 AND c.kind = $2 AND c.archived_at IS NULL`
	if lock {
		sql += ` FOR UPDATE OF c`
	}
	rows, err := q.Query(ctx, sql, cid, kind)
	return handle.One[CmsContent](rows, err, strings.ToLower(specOf(kind).Name))
}

func version(ctx context.Context, q dbtx.Querier, cid uuid.UUID, no int) (CmsVersionDetail, error) {
	rows, err := q.Query(ctx, versionSelect+` WHERE v.content_id = $1 AND v.version_no = $2`, cid, no)
	return handle.One[CmsVersionDetail](rows, err, "version")
}

func (m *Module) detail(ctx context.Context, q dbtx.Querier, kind string, cid uuid.UUID) (CmsContentDetail, error) {
	c, err := get(ctx, q, kind, cid, false)
	if err != nil {
		return CmsContentDetail{}, err
	}
	v, err := version(ctx, q, cid, c.LatestVersion)
	if err != nil {
		return CmsContentDetail{}, err
	}
	return CmsContentDetail{CmsContent: c, Document: v.Document, Version: v.CmsVersion}, nil
}

// ── settings ──────────────────────────────────────────────────────────────

type settingsRow struct {
	Key           *string
	Template      *string
	ParentID      *uuid.UUID
	SortOrder     int
	ShowInSitemap bool
	CategoryID    *uuid.UUID
	Tags          []string
	AuthorName    *string
	Featured      bool
	DisplayDate   *string
	Placement     *string
	PageIDs       []uuid.UUID
	Link          *CmsLink
	PublishAt     *time.Time
	UnpublishAt   *time.Time
}

func rowSettings(c CmsContent) settingsRow {
	return settingsRow{Key: c.Key, Template: c.Template, ParentID: c.ParentID, SortOrder: c.SortOrder, ShowInSitemap: c.ShowInSitemap,
		CategoryID: c.CategoryID, Tags: c.Tags, AuthorName: c.AuthorName, Featured: c.Featured, DisplayDate: c.DisplayDate, Placement: c.Placement,
		PageIDs: c.PageIDs, Link: c.Link, PublishAt: c.PublishAt, UnpublishAt: c.UnpublishAt}
}

var settingKinds = map[string][]string{
	"key": {KindPage, KindBanner, KindGallery}, "template": {KindPage}, "parentId": {KindPage},
	"showInSitemap": {KindPage, KindArticle, KindGallery}, "categoryId": {KindArticle}, "tags": {KindArticle}, "authorName": {KindArticle},
	"featured": {KindArticle, KindGallery}, "displayDate": {KindArticle}, "placement": {KindBanner}, "pageIds": {KindBanner}, "link": {KindBanner},
}

// applySettings merges a settings change and validates it for the kind.
func (m *Module) applySettings(ctx context.Context, tx pgx.Tx, spec kindSpec, cid *uuid.UUID, row *settingsRow, in CmsSettings, now time.Time) error {
	set := map[string]bool{}
	mark := func(name string, present bool) error {
		if !present {
			return nil
		}
		if kinds, ok := settingKinds[name]; ok && !slices.Contains(kinds, spec.Kind) {
			return handle.Invalid(name, "not_applicable", name+" does not apply to a "+strings.ToLower(spec.Name))
		}
		set[name] = true
		return nil
	}
	for _, c := range in.Clear {
		if err := mark(c, true); err != nil {
			return err
		}
		switch c {
		case "key":
			row.Key = nil
		case "template":
			row.Template = nil
		case "parentId":
			row.ParentID = nil
		case "categoryId":
			row.CategoryID = nil
		case "authorName":
			row.AuthorName = nil
		case "displayDate":
			row.DisplayDate = nil
		case "placement":
			return handle.Invalid("clear", "required", "a banner needs a placement")
		case "pageIds":
			row.PageIDs = nil
		case "link":
			row.Link = nil
		case "tags":
			row.Tags = nil
		case "publishAt":
			row.PublishAt = nil
		case "unpublishAt":
			row.UnpublishAt = nil
		default:
			return handle.Invalid("clear", "invalid_option", c+" cannot be cleared")
		}
	}
	for _, f := range []struct {
		name    string
		present bool
	}{{"key", in.Key != nil}, {"template", in.Template != nil}, {"parentId", in.ParentID != nil}, {"showInSitemap", in.ShowInSitemap != nil},
		{"categoryId", in.CategoryID != nil}, {"tags", in.Tags != nil}, {"authorName", in.AuthorName != nil}, {"featured", in.Featured != nil},
		{"displayDate", in.DisplayDate != nil}, {"placement", in.Placement != nil}, {"pageIds", in.PageIDs != nil}, {"link", in.Link != nil}} {
		if err := mark(f.name, f.present); err != nil {
			return err
		}
	}
	pid := handle.Property(ctx)
	if in.Key != nil {
		k := strings.ToLower(strings.TrimSpace(*in.Key))
		if !keyRe.MatchString(k) || len(k) > 60 {
			return handle.Invalid("key", "invalid", "key: lower-case letters, digits, - and _ (max 60)")
		}
		row.Key = &k
	}
	if in.Template != nil {
		if !slices.Contains(PageTemplates, *in.Template) {
			return handle.Invalid("template", "invalid_option", "must be one of: "+strings.Join(PageTemplates, ", "))
		}
		row.Template = in.Template
	}
	if in.ParentID != nil {
		if cid != nil && *in.ParentID == *cid {
			return handle.Invalid("parentId", "invalid", "a page cannot be its own parent")
		}
		if err := checkContents(ctx, tx, pid, KindPage, []uuid.UUID{*in.ParentID}, "parentId"); err != nil {
			return err
		}
		if cid != nil {
			var cycle bool
			if err := tx.QueryRow(ctx, `WITH RECURSIVE up AS (SELECT id, parent_id, 1 AS depth FROM cms.contents WHERE id = $1
				UNION ALL SELECT c.id, c.parent_id, up.depth + 1 FROM cms.contents c JOIN up ON c.id = up.parent_id WHERE up.depth < 20)
				SELECT EXISTS (SELECT 1 FROM up WHERE id = $2)`, *in.ParentID, *cid).Scan(&cycle); err != nil {
				return err
			}
			if cycle {
				return handle.Invalid("parentId", "cycle", "the parent page is below this page")
			}
		}
		row.ParentID = in.ParentID
	}
	if in.SortOrder != nil {
		row.SortOrder = *in.SortOrder
	}
	if in.ShowInSitemap != nil {
		row.ShowInSitemap = *in.ShowInSitemap
	}
	if in.CategoryID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cms.categories WHERE id = $1 AND property_id = $2 AND archived_at IS NULL)`,
			*in.CategoryID, pid).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return handle.Invalid("categoryId", "not_found", "news category not found in this property")
		}
		row.CategoryID = in.CategoryID
	}
	if in.Tags != nil {
		var tags []string
		for _, t := range in.Tags {
			t = strings.ToLower(plainText(t))
			if t == "" || slices.Contains(tags, t) {
				continue
			}
			if len([]rune(t)) > 40 {
				return handle.Invalid("tags", "too_long", "tags have at most 40 characters")
			}
			tags = append(tags, t)
		}
		if len(tags) > 20 {
			return handle.Invalid("tags", "too_many", "at most 20 tags")
		}
		row.Tags = tags
	}
	if in.AuthorName != nil {
		a := plainText(*in.AuthorName)
		if len([]rune(a)) > 120 {
			return handle.Invalid("authorName", "too_long", "at most 120 characters")
		}
		row.AuthorName = nz(a)
	}
	if in.Featured != nil {
		row.Featured = *in.Featured
	}
	if in.DisplayDate != nil {
		if _, err := time.Parse("2006-01-02", *in.DisplayDate); err != nil {
			return handle.Invalid("displayDate", "invalid_date", "must be a date (YYYY-MM-DD)")
		}
		row.DisplayDate = in.DisplayDate
	}
	if in.Placement != nil {
		if !slices.Contains(BannerPlacements, *in.Placement) {
			return handle.Invalid("placement", "invalid_option", "must be one of: "+strings.Join(BannerPlacements, ", "))
		}
		row.Placement = in.Placement
	}
	if in.PageIDs != nil {
		ids := uniqueIDs(in.PageIDs)
		if err := checkContents(ctx, tx, pid, KindPage, ids, "pageIds"); err != nil {
			return err
		}
		row.PageIDs = ids
	}
	if in.Link != nil {
		l := *in.Link
		if err := m.checkLink(ctx, tx, &l, "link"); err != nil {
			return err
		}
		row.Link = &l
	}
	if in.PublishAt != nil {
		t := in.PublishAt.UTC()
		row.PublishAt = &t
	}
	if in.UnpublishAt != nil {
		t := in.UnpublishAt.UTC()
		if !t.After(now) {
			return handle.Invalid("unpublishAt", "past", "the take-down time must be in the future")
		}
		row.UnpublishAt = &t
	}
	if row.PublishAt != nil && row.UnpublishAt != nil && !row.UnpublishAt.After(*row.PublishAt) {
		return handle.Invalid("unpublishAt", "before_publish", "the take-down time must be after the go-live time")
	}
	if spec.Kind == KindBanner && row.Placement == nil {
		return handle.Invalid("placement", "required", "a banner needs a placement")
	}
	if spec.Kind == KindPage && row.Template == nil {
		t := "standard"
		row.Template = &t
	}
	return nil
}

// ── documents ─────────────────────────────────────────────────────────────

// checkDocument validates and normalises the versioned content.
func (m *Module) checkDocument(ctx context.Context, tx pgx.Tx, spec kindSpec, property uuid.UUID, pol ContentPolicy, doc *CmsDocument) error {
	if len(doc.Translations) == 0 || strings.TrimSpace(doc.Translations[pol.DefaultLanguage].Title) == "" {
		return handle.Invalid("translations."+pol.DefaultLanguage+".title", "required", "a title in the default language ("+pol.DefaultLanguage+") is required")
	}
	rf := &refs{}
	for lang, t := range doc.Translations {
		p := "translations." + lang
		if !slices.Contains(pol.SupportedLanguages, lang) {
			return handle.Invalid(p, "unsupported_language", "language "+lang+" is not a website language (Content Policies)")
		}
		t.Title = plainText(t.Title)
		t.Summary = plainText(t.Summary)
		t.ButtonLabel = plainText(t.ButtonLabel)
		t.Alt = plainText(t.Alt)
		for _, c := range []struct {
			f   string
			v   string
			max int
		}{{"title", t.Title, 200}, {"summary", t.Summary, 1000}, {"buttonLabel", t.ButtonLabel, 80}, {"alt", t.Alt, 300}} {
			if len([]rune(c.v)) > c.max {
				return handle.Invalid(p+"."+c.f, "too_long", fmt.Sprintf("must be at most %d characters", c.max))
			}
		}
		if spec.SlugScope != "" {
			t.Slug = strings.ToLower(strings.TrimSpace(t.Slug))
			if t.Slug == "" && t.Title != "" {
				t.Slug = slugify(t.Title)
			}
			if t.Slug != "" && (!slugRe.MatchString(t.Slug) || len(t.Slug) > 120) {
				return handle.Invalid(p+".slug", "invalid", "slug: lower-case letters, digits and single dashes (max 120)")
			}
		} else {
			t.Slug = ""
		}
		if spec.Kind != KindBanner {
			t.ButtonLabel = ""
		}
		if t.MediaID != nil {
			if spec.Kind == KindPage {
				return handle.Invalid(p+".mediaId", "not_applicable", "pages show images through blocks")
			}
			rf.add(&rf.media, *t.MediaID)
		}
		if t.SEO != nil {
			s := t.SEO
			s.MetaTitle, s.MetaDescription = plainText(s.MetaTitle), plainText(s.MetaDescription)
			if len([]rune(s.MetaTitle)) > 120 || len([]rune(s.MetaDescription)) > 320 {
				return handle.Invalid(p+".seo", "too_long", "meta title ≤ 120 and meta description ≤ 320 characters")
			}
			s.CanonicalURL = strings.TrimSpace(s.CanonicalURL)
			if s.CanonicalURL != "" && !isHTTPURL(s.CanonicalURL) && !isSitePath(s.CanonicalURL) {
				return handle.Invalid(p+".seo.canonicalUrl", "invalid", "an http(s) address or a /path")
			}
			if s.OGImageID != nil {
				rf.add(&rf.media, *s.OGImageID)
			}
			if spec.Kind == KindBanner {
				t.SEO = nil
			}
		}
		doc.Translations[lang] = t
	}
	if spec.Blocks {
		if err := checkBlocks(doc.Blocks, pol, rf); err != nil {
			return err
		}
	} else if len(doc.Blocks) > 0 {
		return handle.Invalid("blocks", "not_applicable", "a "+strings.ToLower(spec.Name)+" has no blocks")
	}
	if spec.Items {
		if len(doc.Items) > 500 {
			return handle.Invalid("items", "too_many", "an album has at most 500 images")
		}
		for i := range doc.Items {
			it := &doc.Items[i]
			rf.add(&rf.media, it.MediaID)
			for lang, c := range it.Caption {
				if !slices.Contains(pol.SupportedLanguages, lang) {
					return handle.Invalid(fmt.Sprintf("items[%d].caption.%s", i, lang), "unsupported_language", "language "+lang+" is not a website language")
				}
				c = plainText(c)
				if len([]rune(c)) > 300 {
					return handle.Invalid(fmt.Sprintf("items[%d].caption.%s", i, lang), "too_long", "captions have at most 300 characters")
				}
				it.Caption[lang] = c
			}
		}
	} else if len(doc.Items) > 0 {
		return handle.Invalid("items", "not_applicable", "only gallery albums have images")
	}
	if err := m.checkMedia(ctx, tx, property, rf.media, "media"); err != nil {
		return err
	}
	if err := checkContents(ctx, tx, property, KindPage, rf.pages, "blocks.link.pageId"); err != nil {
		return err
	}
	if err := checkContents(ctx, tx, property, KindGallery, rf.galleries, "blocks.config.galleryId"); err != nil {
		return err
	}
	if len(rf.contacts) > 0 {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM cms.contacts WHERE property_id = $1 AND id = ANY($2) AND archived_at IS NULL`, property, rf.contacts).Scan(&n); err != nil {
			return err
		}
		if n != len(rf.contacts) {
			return handle.Invalid("blocks.config.contactId", "not_found", "contact information not found")
		}
	}
	return nil
}

// langHashes fingerprints the content of every language.
func langHashes(doc CmsDocument, langs []string) map[string]string {
	out := map[string]string{}
	for _, lang := range langs {
		part := map[string]any{"t": doc.Translations[lang]}
		var blocks []any
		for _, b := range doc.Blocks {
			blocks = append(blocks, []any{b.ID, b.Content[lang]})
		}
		part["b"] = blocks
		var items []any
		for _, it := range doc.Items {
			items = append(items, it.Caption[lang])
		}
		part["i"] = items
		raw, _ := json.Marshal(part)
		sum := sha256.Sum256(raw)
		out[lang] = hex.EncodeToString(sum[:12])
	}
	return out
}

// contentPath is the website path of a content item in a language.
func contentPath(kind, lang, slug string, key, template *string) string {
	switch kind {
	case KindPage:
		if deref(template) == "home" || deref(key) == "home" {
			return "/" + lang
		}
		if slug == "" {
			return ""
		}
		return "/" + lang + "/" + slug
	case KindArticle, KindGallery:
		if slug == "" {
			return ""
		}
		return "/" + lang + "/" + specOf(kind).URLPrefix + slug
	}
	return ""
}

// translationStates computes the translation status of every language.
func translationStates(c settingsRow, kind string, doc CmsDocument, rev map[string]int, pol ContentPolicy) map[string]CmsTranslationState {
	spec := specOf(kind)
	out := map[string]CmsTranslationState{}
	def := pol.DefaultLanguage
	for _, lang := range pol.SupportedLanguages {
		t, ok := doc.Translations[lang]
		st := CmsTranslationState{Status: "complete", Title: t.Title, Slug: t.Slug, Path: contentPath(kind, lang, t.Slug, c.Key, c.Template)}
		switch {
		case !ok || strings.TrimSpace(t.Title) == "" || (spec.SlugScope != "" && t.Slug == ""):
			st.Status = "missing"
		case lang != def && incomplete(doc, lang, def):
			st.Status = "incomplete"
		case lang != def && rev[lang] < rev[def]:
			st.Status = "outdated"
		}
		out[lang] = st
	}
	return out
}

// incomplete reports whether a translatable block text present in the
// default language is missing in lang.
func incomplete(doc CmsDocument, lang, def string) bool {
	for _, b := range doc.Blocks {
		if b.Hidden {
			continue
		}
		spec := blockSpecs[b.Type]
		for k, fs := range spec.Content {
			if !fs.Translate {
				continue
			}
			if b.Content[def][k] != nil && (b.Content[lang] == nil || b.Content[lang][k] == nil) {
				return true
			}
		}
	}
	return false
}

// saveVersion validates a document and stores it as the next version.
func (m *Module) saveVersion(ctx context.Context, tx pgx.Tx, c CmsContent, doc CmsDocument, note, source string, restoredFrom *int, pol ContentPolicy) (int, error) {
	spec := specOf(c.Kind)
	if err := m.checkDocument(ctx, tx, spec, c.PropertyID, pol, &doc); err != nil {
		return 0, err
	}
	no := c.LatestVersion + 1
	hashes := langHashes(doc, pol.SupportedLanguages)
	rev := map[string]int{}
	prevHash, prevRev := map[string]string{}, map[string]int{}
	if c.LatestVersion > 0 {
		var ph, pr []byte
		if err := tx.QueryRow(ctx, `SELECT lang_hash, lang_rev FROM cms.content_versions WHERE content_id = $1 AND version_no = $2`, c.ID, c.LatestVersion).
			Scan(&ph, &pr); err != nil {
			return 0, err
		}
		_ = json.Unmarshal(ph, &prevHash)
		_ = json.Unmarshal(pr, &prevRev)
	}
	for lang, h := range hashes {
		if r, ok := prevRev[lang]; ok && prevHash[lang] == h {
			rev[lang] = r
		} else {
			rev[lang] = no
		}
	}
	if note = plainText(note); len([]rune(note)) > 500 {
		return 0, handle.Invalid("note", "too_long", "at most 500 characters")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO cms.content_versions (id, property_id, content_id, version_no, document, lang_hash, lang_rev, note, source,
		restored_from, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		id.New(), c.PropertyID, c.ID, no, doc, hashes, rev, nz(note), source, restoredFrom, actorID(ctx)); err != nil {
		return 0, err
	}
	states := translationStates(rowSettings(c), c.Kind, doc, rev, pol)
	if _, err := tx.Exec(ctx, `UPDATE cms.contents SET latest_version = $2, title = $3, translation_status = $4, updated_by = $5 WHERE id = $1`,
		c.ID, no, doc.Translations[pol.DefaultLanguage].Title, states, actorID(ctx)); err != nil {
		return 0, err
	}
	return no, m.syncSlugs(ctx, tx, c.ID)
}

// refreshStates recomputes translation status and paths after settings changes.
func (m *Module) refreshStates(ctx context.Context, tx pgx.Tx, c CmsContent, pol ContentPolicy) error {
	v, err := version(ctx, tx, c.ID, c.LatestVersion)
	if err != nil {
		return err
	}
	states := translationStates(rowSettings(c), c.Kind, v.Document, v.LangRev, pol)
	_, err = tx.Exec(ctx, `UPDATE cms.contents SET translation_status = $2 WHERE id = $1`, c.ID, states)
	return err
}

// syncSlugs reserves the slugs of the latest and the published version.
func (m *Module) syncSlugs(ctx context.Context, tx pgx.Tx, cid uuid.UUID) error {
	var kind string
	var pid uuid.UUID
	var latest int
	var published *int
	if err := tx.QueryRow(ctx, `SELECT kind, property_id, latest_version, published_version FROM cms.contents WHERE id = $1`, cid).
		Scan(&kind, &pid, &latest, &published); err != nil {
		return err
	}
	scope := specOf(kind).SlugScope
	if scope == "" {
		return nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM cms.content_slugs WHERE content_id = $1`, cid); err != nil {
		return err
	}
	nos := []int{latest}
	if published != nil && *published != latest {
		nos = append(nos, *published)
	}
	type ls struct{ lang, slug string }
	var all []ls
	for _, no := range nos {
		v, err := version(ctx, tx, cid, no)
		if err != nil {
			return err
		}
		for lang, t := range v.Document.Translations {
			if t.Slug != "" && !slices.Contains(all, ls{lang, t.Slug}) {
				all = append(all, ls{lang, t.Slug})
			}
		}
	}
	for _, x := range all {
		var owner uuid.UUID
		err := tx.QueryRow(ctx, `INSERT INTO cms.content_slugs (property_id, scope, language, slug, content_id) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (property_id, scope, language, slug) DO UPDATE SET slug = EXCLUDED.slug RETURNING content_id`, pid, scope, x.lang, x.slug, cid).Scan(&owner)
		if err != nil {
			return err
		}
		if owner != cid {
			return errs.Validation("slug_taken", "slug already used", errs.Field("translations."+x.lang+".slug", "taken",
				fmt.Sprintf("the %s slug %q is already used by another %s", x.lang, x.slug, strings.ToLower(specOf(kind).Name))))
		}
	}
	return nil
}

// ── create / update / save ────────────────────────────────────────────────

// Create creates a content item and its first version.
func (m *Module) Create(ctx context.Context, tx pgx.Tx, kind string, in CmsContentInput, source string) (CmsContentDetail, error) {
	spec := specOf(kind)
	pid := handle.Property(ctx)
	pol, err := Policy(ctx, tx, pid)
	if err != nil {
		return CmsContentDetail{}, err
	}
	now := m.now()
	row := settingsRow{ShowInSitemap: true}
	if err := m.applySettings(ctx, tx, spec, nil, &row, in.CmsSettings, now); err != nil {
		return CmsContentDetail{}, err
	}
	if spec.Kind == KindPage && row.Key == nil {
		if t, ok := in.Translations[pol.DefaultLanguage]; ok {
			k := t.Slug
			if k == "" {
				k = slugify(t.Title)
			}
			var taken bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cms.contents WHERE property_id = $1 AND kind = 'page' AND key = $2
				AND archived_at IS NULL)`, pid, k).Scan(&taken); err != nil {
				return CmsContentDetail{}, err
			}
			if k != "" && keyRe.MatchString(k) && len(k) <= 60 && !taken {
				row.Key = &k
			}
		}
	}
	cid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO cms.contents (id, property_id, kind, key, template, parent_id, sort_order, show_in_sitemap, category_id, tags,
		author_name, featured, display_date, placement, page_ids, link, publish_at, unpublish_at, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,coalesce($10, '{}'::text[]),$11,$12,$13::date,$14,coalesce($15, '{}'::uuid[]),$16,$17,$18,$19,$19)`,
		cid, pid, kind, row.Key, row.Template, row.ParentID, row.SortOrder, row.ShowInSitemap, row.CategoryID, row.Tags, row.AuthorName, row.Featured,
		row.DisplayDate, row.Placement, row.PageIDs, row.Link, row.PublishAt, row.UnpublishAt, actorID(ctx)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return CmsContentDetail{}, handle.Invalid("key", "taken", "this key is already used")
		}
		return CmsContentDetail{}, err
	}
	c, err := get(ctx, tx, kind, cid, true)
	if err != nil {
		return CmsContentDetail{}, err
	}
	if source == "" {
		source = "create"
	}
	if _, err := m.saveVersion(ctx, tx, c, in.CmsDocument, in.Note, source, nil, pol); err != nil {
		return CmsContentDetail{}, err
	}
	action := "created"
	if source == "import" {
		action = "imported"
	}
	if err := m.logEvent(ctx, tx, c, action, 1, in.Note); err != nil {
		return CmsContentDetail{}, err
	}
	out, err := m.detail(ctx, tx, kind, cid)
	if err != nil {
		return out, err
	}
	return out, m.audit(ctx, tx, out.CmsContent, audit.ActionCreate, nil, out, "", nil)
}

// UpdateSettings changes the settings of a content item.
func (m *Module) UpdateSettings(ctx context.Context, tx pgx.Tx, kind string, cid uuid.UUID, in CmsSettings) (CmsContentDetail, error) {
	c, err := get(ctx, tx, kind, cid, true)
	if err != nil {
		return CmsContentDetail{}, err
	}
	before := c
	now := m.now()
	if in.PublishAt != nil && (c.Status == "published" || c.Status == "unpublished") {
		return CmsContentDetail{}, errs.Conflict("use_schedule", "the content was already published: schedule a new go-live with :schedule or :publish")
	}
	if in.PublishAt != nil && c.Status == "scheduled" && !in.PublishAt.After(now) {
		return CmsContentDetail{}, handle.Invalid("publishAt", "past", "a scheduled go-live must be in the future (publish now with :publish)")
	}
	row := rowSettings(c)
	if err := m.applySettings(ctx, tx, specOf(kind), &cid, &row, in, now); err != nil {
		return CmsContentDetail{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE cms.contents SET key = $2, template = $3, parent_id = $4, sort_order = $5, show_in_sitemap = $6, category_id = $7,
		tags = coalesce($8, '{}'::text[]), author_name = $9, featured = $10, display_date = $11::date, placement = $12, page_ids = coalesce($13, '{}'::uuid[]),
		link = $14, publish_at = $15, unpublish_at = $16, updated_by = $17 WHERE id = $1`,
		cid, row.Key, row.Template, row.ParentID, row.SortOrder, row.ShowInSitemap, row.CategoryID, row.Tags, row.AuthorName, row.Featured,
		row.DisplayDate, row.Placement, row.PageIDs, row.Link, row.PublishAt, row.UnpublishAt, actorID(ctx)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return CmsContentDetail{}, handle.Invalid("key", "taken", "this key is already used")
		}
		return CmsContentDetail{}, err
	}
	if c, err = get(ctx, tx, kind, cid, false); err != nil {
		return CmsContentDetail{}, err
	}
	pol, err := Policy(ctx, tx, c.PropertyID)
	if err != nil {
		return CmsContentDetail{}, err
	}
	if err := m.refreshStates(ctx, tx, c, pol); err != nil {
		return CmsContentDetail{}, err
	}
	if c.Live {
		if _, err := m.bumpRevision(ctx, tx, c.PropertyID); err != nil {
			return CmsContentDetail{}, err
		}
	}
	out, err := m.detail(ctx, tx, kind, cid)
	if err != nil {
		return out, err
	}
	return out, m.audit(ctx, tx, out.CmsContent, audit.ActionUpdate, before, out.CmsContent, "", map[string]any{"settings": true})
}

// SaveContent stores a new version of the content.
func (m *Module) SaveContent(ctx context.Context, tx pgx.Tx, kind string, cid uuid.UUID, in CmsContentSave) (CmsContentDetail, error) {
	c, err := get(ctx, tx, kind, cid, true)
	if err != nil {
		return CmsContentDetail{}, err
	}
	if c.Status == "in_review" {
		return CmsContentDetail{}, errs.Conflict("in_review", "the content is in review; withdraw the review to edit it")
	}
	if in.BaseVersion != nil && *in.BaseVersion != c.LatestVersion {
		return CmsContentDetail{}, errs.Precondition(fmt.Sprintf("version %d was saved in the meantime; reload before saving", c.LatestVersion))
	}
	pol, err := Policy(ctx, tx, c.PropertyID)
	if err != nil {
		return CmsContentDetail{}, err
	}
	no, err := m.saveVersion(ctx, tx, c, in.CmsDocument, in.Note, "edit", nil, pol)
	if err != nil {
		return CmsContentDetail{}, err
	}
	if err := m.logEvent(ctx, tx, c, "saved", no, in.Note); err != nil {
		return CmsContentDetail{}, err
	}
	out, err := m.detail(ctx, tx, kind, cid)
	if err != nil {
		return out, err
	}
	return out, m.audit(ctx, tx, out.CmsContent, audit.ActionUpdate, map[string]any{"version": c.LatestVersion}, map[string]any{"version": no, "document": out.Document},
		in.Note, map[string]any{"version": no})
}

// ── audit & log ───────────────────────────────────────────────────────────

func (m *Module) audit(ctx context.Context, tx pgx.Tx, c CmsContent, action string, before, after any, reason string, md map[string]any) error {
	label := c.Title
	if label == "" && c.Key != nil {
		label = *c.Key
	}
	if md == nil {
		md = map[string]any{}
	}
	md["kind"] = c.Kind
	actor := ""
	if authz.From(ctx) == nil {
		actor = "cms scheduler"
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "cms", Action: action, EntityType: "cms." + c.Kind, EntityID: c.ID.String(), EntityLabel: label,
		PropertyID: &c.PropertyID, Before: before, After: after, Reason: reason, Metadata: md, ActorName: actor})
}

func (m *Module) logEvent(ctx context.Context, tx pgx.Tx, c CmsContent, action string, no int, note string) error {
	var v *int
	if no > 0 {
		v = &no
	}
	_, err := tx.Exec(ctx, `INSERT INTO cms.publish_events (id, property_id, content_id, kind, action, version_no, actor_id, actor_name, note)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id.New(), c.PropertyID, c.ID, c.Kind, action, v, actorID(ctx), actorName(ctx), nz(plainText(note)))
	return err
}

// bumpRevision increments the website revision of a property.
func (m *Module) bumpRevision(ctx context.Context, tx pgx.Tx, property uuid.UUID) (int64, error) {
	var rev int64
	err := tx.QueryRow(ctx, `INSERT INTO cms.site_state (property_id, revision, changed_at) VALUES ($1, 1, now())
		ON CONFLICT (property_id) DO UPDATE SET revision = cms.site_state.revision + 1, changed_at = now() RETURNING revision`, property).Scan(&rev)
	return rev, err
}

// SiteChanged is the payload of cms.site_changed (navigation, contact,
// redirects, media … changed) and of the content publication events.
type SiteChanged struct {
	PropertyID uuid.UUID  `json:"propertyId"`
	Revision   int64      `json:"revision"`
	Paths      []string   `json:"paths" doc:"Affected website paths; empty = the whole site"`
	ContentID  *uuid.UUID `json:"contentId,omitempty"`
	Kind       string     `json:"kind,omitempty"`
	Key        *string    `json:"key,omitempty"`
	Title      string     `json:"title,omitempty"`
	VersionNo  int        `json:"versionNo,omitempty"`
	Action     string     `json:"action"`
}

// EventSiteChanged is published after changes of website master data.
const EventSiteChanged = "cms.site_changed"

// EventTypes are the events of the CMS (cms.<kind>_published / _unpublished, cms.site_changed).
func EventTypes() []string {
	out := []string{EventSiteChanged}
	for _, k := range kindSpecs {
		out = append(out, "cms."+k.Kind+"_published", "cms."+k.Kind+"_unpublished")
	}
	return out
}

// touchSite bumps the revision and announces a site-wide change.
func (m *Module) touchSite(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	if property == uuid.Nil {
		return nil
	}
	rev, err := m.bumpRevision(ctx, tx, property)
	if err != nil || m.Events == nil {
		return err
	}
	_, err = m.Events.Publish(ctx, tx, EventSiteChanged, "cms.site", &property, &property, SiteChanged{PropertyID: property, Revision: rev, Paths: []string{},
		Action: "changed"})
	return err
}
