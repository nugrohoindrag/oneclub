package cms

// Content import (PRD P4 EP-29 wave-4 migration, CMS part): pages and news
// of the old website and its URLs (redirects) are imported idempotently —
// pages by key (or default slug), news by default-language slug, redirects
// by old path — as new draft versions that go through the normal review.
// dryRun validates everything without saving. Categories, menus, contact
// information and course guide texts use the generic CSV/XLSX import of
// their resources.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// CmsImportItem is one imported page or news article.
type CmsImportItem struct {
	CmsSettings
	CmsDocument
	CategoryCode string     `json:"categoryCode,omitempty" doc:"News category code (alternative to categoryId)"`
	PublishedAt  *time.Time `json:"publishedAt,omitempty" doc:"Original publication date of a news article (shown as its date)"`
}

// CmsImportRedirect is one old URL.
type CmsImportRedirect struct {
	From       string `json:"from"`
	To         string `json:"to"`
	StatusCode int    `json:"statusCode,omitempty"`
}

// CmsImportRequest is an import bundle.
type CmsImportRequest struct {
	DryRun    bool                `json:"dryRun,omitempty"`
	Pages     []CmsImportItem     `json:"pages,omitempty"`
	Articles  []CmsImportItem     `json:"articles,omitempty"`
	Redirects []CmsImportRedirect `json:"redirects,omitempty"`
}

// CmsImportError is a rejected item.
type CmsImportError struct {
	Kind    string `json:"kind"`
	Index   int    `json:"index"`
	Ref     string `json:"ref"`
	Message string `json:"message"`
}

// CmsImportResult summarises an import.
type CmsImportResult struct {
	DryRun    bool             `json:"dryRun"`
	Created   int              `json:"created"`
	Updated   int              `json:"updated"`
	Redirects int              `json:"redirects"`
	Errors    []CmsImportError `json:"errors"`
}

// Import imports a bundle.
func (m *Module) Import(ctx context.Context, tx pgx.Tx, in CmsImportRequest) (CmsImportResult, error) {
	out := CmsImportResult{DryRun: in.DryRun, Errors: []CmsImportError{}}
	if len(in.Pages)+len(in.Articles)+len(in.Redirects) == 0 {
		return out, handle.Invalid("pages", "required", "nothing to import")
	}
	if len(in.Pages)+len(in.Articles) > 500 || len(in.Redirects) > 5000 {
		return out, handle.Invalid("pages", "too_many", "import at most 500 pages and articles and 5000 redirects per bundle")
	}
	pid := handle.Property(ctx)
	pol, err := Policy(ctx, tx, pid)
	if err != nil {
		return out, err
	}
	// one savepoint per item: a rejected item does not stop the others
	try := func(kind string, i int, ref string, fn func(sp pgx.Tx) (bool, error)) error {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		created, ferr := fn(sp)
		if ferr != nil {
			_ = sp.Rollback(ctx)
			if _, ok := errs.As(ferr); !ok {
				return ferr
			}
			msg := ferr.Error()
			if e, ok := errs.As(ferr); ok && len(e.Fields) > 0 {
				msg = e.Fields[0].Field + ": " + e.Fields[0].Message
			}
			out.Errors = append(out.Errors, CmsImportError{Kind: kind, Index: i, Ref: ref, Message: msg})
			return nil
		}
		if in.DryRun {
			if err := sp.Rollback(ctx); err != nil {
				return err
			}
		} else if err := sp.Commit(ctx); err != nil {
			return err
		}
		switch {
		case kind == "redirect":
			out.Redirects++
		case created:
			out.Created++
		default:
			out.Updated++
		}
		return nil
	}
	for _, set := range []struct {
		kind  string
		items []CmsImportItem
	}{{KindPage, in.Pages}, {KindArticle, in.Articles}} {
		for i, it := range set.items {
			ref := it.Translations[pol.DefaultLanguage].Slug
			if it.Key != nil {
				ref = *it.Key
			}
			if ref == "" {
				ref = slugify(it.Translations[pol.DefaultLanguage].Title)
			}
			if err := try(set.kind, i, ref, func(sp pgx.Tx) (bool, error) {
				return m.importItem(ctx, sp, set.kind, pid, pol, it)
			}); err != nil {
				return out, err
			}
		}
	}
	for i, rd := range in.Redirects {
		if err := try("redirect", i, rd.From, func(sp pgx.Tx) (bool, error) {
			from, to := normPath(rd.From), normPath(rd.To)
			if !pathRe.MatchString(from) {
				return false, handle.Invalid("from", "invalid", "the old URL must be a path")
			}
			if (!pathRe.MatchString(to) && !isHTTPURL(to)) || strings.EqualFold(from, to) {
				return false, handle.Invalid("to", "invalid", "the new URL must be a different path or an http(s) address")
			}
			code := rd.StatusCode
			if code == 0 {
				code = 301
			}
			if code != 301 && code != 302 && code != 307 && code != 308 {
				return false, handle.Invalid("statusCode", "invalid", "301, 302, 307 or 308")
			}
			_, err := sp.Exec(ctx, `INSERT INTO cms.redirects (id, property_id, from_path, to_path, status_code, source, created_by, updated_by)
				VALUES ($1,$2,$3,$4,$5,'import',$6,$6) ON CONFLICT (property_id, from_path) WHERE archived_at IS NULL
				DO UPDATE SET to_path = EXCLUDED.to_path, status_code = EXCLUDED.status_code, status = 'active'`, id.New(), pid, from, to, code, actorID(ctx))
			return true, err
		}); err != nil {
			return out, err
		}
	}
	if !in.DryRun && out.Redirects > 0 {
		if err := m.touchSite(ctx, tx, pid); err != nil {
			return out, err
		}
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "cms", Action: audit.ActionImport, EntityType: "cms.import", EntityLabel: "Website content import",
		PropertyID: &pid, After: out, Metadata: map[string]any{"dryRun": in.DryRun, "pages": len(in.Pages), "articles": len(in.Articles),
			"redirects": len(in.Redirects)}})
}

func (m *Module) importItem(ctx context.Context, tx pgx.Tx, kind string, pid uuid.UUID, pol ContentPolicy, it CmsImportItem) (bool, error) {
	if it.CategoryCode != "" {
		if kind != KindArticle {
			return false, handle.Invalid("categoryCode", "not_applicable", "only news articles have a category")
		}
		var cid uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM cms.categories WHERE property_id = $1 AND code = upper($2) AND archived_at IS NULL`, pid, it.CategoryCode).
			Scan(&cid); err != nil {
			return false, handle.Invalid("categoryCode", "not_found", "category "+it.CategoryCode+" not found")
		}
		it.CategoryID = &cid
	}
	if it.PublishedAt != nil && it.DisplayDate == nil {
		d := it.PublishedAt.UTC().Format("2006-01-02")
		it.DisplayDate = &d
	}
	// existing content: by key (pages) or by default-language slug
	var existing *uuid.UUID
	def := it.Translations[pol.DefaultLanguage]
	slug := def.Slug
	if slug == "" {
		slug = slugify(def.Title)
	}
	if kind == KindPage && it.Key != nil {
		var cid uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM cms.contents WHERE property_id = $1 AND kind = 'page' AND key = $2 AND archived_at IS NULL`, pid,
			strings.ToLower(*it.Key)).Scan(&cid); err == nil {
			existing = &cid
		}
	}
	if existing == nil && slug != "" {
		var cid uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT content_id FROM cms.content_slugs WHERE property_id = $1 AND scope = $2 AND language = $3 AND slug = $4`,
			pid, specOf(kind).SlugScope, pol.DefaultLanguage, slug).Scan(&cid); err == nil {
			existing = &cid
		}
	}
	if existing == nil {
		_, err := m.Create(ctx, tx, kind, CmsContentInput{CmsSettings: it.CmsSettings, CmsDocument: it.CmsDocument, Note: "imported"}, "import")
		return true, err
	}
	c, err := get(ctx, tx, kind, *existing, true)
	if err != nil {
		return false, err
	}
	if c.Status == "in_review" {
		return false, errs.Conflict("in_review", fmt.Sprintf("%s is in review", c.Title))
	}
	if _, err := m.UpdateSettings(ctx, tx, kind, c.ID, it.CmsSettings); err != nil {
		return false, err
	}
	if c, err = get(ctx, tx, kind, *existing, true); err != nil {
		return false, err
	}
	no, err := m.saveVersion(ctx, tx, c, it.CmsDocument, "imported", "import", nil, pol)
	if err != nil {
		return false, err
	}
	return false, m.logEvent(ctx, tx, c, "imported", no, "")
}
