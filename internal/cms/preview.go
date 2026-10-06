package cms

// Preview links (FR-CMS-06): a signed, expiring token lets reviewers and the
// marketing team open any version (draft, in review, scheduled) on the
// website before it is published. The token is an HMAC-SHA256 signature
// (key derived from the instance APP_SECRET) over the content id, version,
// language and expiry; it is not stored and cannot be forged or extended.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
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
	"oneclub/internal/platform/audit"
)

// CmsPreviewInput creates a preview link.
type CmsPreviewInput struct {
	VersionNo *int   `json:"versionNo,omitempty" doc:"Default: the latest version"`
	Language  string `json:"language,omitempty"`
	Hours     int    `json:"hours,omitempty" doc:"Validity (default and maximum: Content Policies)"`
}

// CmsPreviewLink is a shareable preview.
type CmsPreviewLink struct {
	Token     string    `json:"token"`
	URL       string    `json:"url" doc:"Website preview URL"`
	APIPath   string    `json:"apiPath" doc:"Public API returning the rendered version"`
	VersionNo int       `json:"versionNo"`
	Language  string    `json:"language"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (m *Module) previewKey() []byte {
	s := "dev-preview-secret"
	if m.Cfg != nil && m.Cfg.AppSecret != "" {
		s = m.Cfg.AppSecret
	}
	sum := sha256.Sum256([]byte("oneclub/cms/preview\x00" + s))
	return sum[:]
}

func (m *Module) sign(payload string) string {
	mac := hmac.New(sha256.New, m.previewKey())
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

type previewClaims struct {
	content uuid.UUID
	version int
	lang    string
	expires time.Time
}

func (m *Module) verify(token string) (previewClaims, error) {
	var c previewClaims
	p, sig, ok := strings.Cut(token, ".")
	if !ok {
		return c, errs.NotFound("preview")
	}
	raw, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return c, errs.NotFound("preview")
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return c, errs.NotFound("preview")
	}
	mac := hmac.New(sha256.New, m.previewKey())
	mac.Write(raw)
	if !hmac.Equal(mac.Sum(nil), got) {
		return c, errs.NotFound("preview")
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 5 || parts[0] != "v1" {
		return c, errs.NotFound("preview")
	}
	if c.content, err = uuid.Parse(parts[1]); err != nil {
		return c, errs.NotFound("preview")
	}
	if c.version, err = strconv.Atoi(parts[2]); err != nil {
		return c, errs.NotFound("preview")
	}
	c.lang = parts[3]
	exp, err := strconv.ParseInt(parts[4], 10, 64)
	if err != nil {
		return c, errs.NotFound("preview")
	}
	c.expires = time.Unix(exp, 0).UTC()
	if !m.now().Before(c.expires) {
		e := errs.NotFound("preview")
		e.Code, e.Message = "preview_expired", "this preview link has expired"
		return c, e
	}
	return c, nil
}

// PreviewLinkFor creates a signed preview link of a version.
func (m *Module) PreviewLinkFor(ctx context.Context, tx pgx.Tx, kind string, cid uuid.UUID, in CmsPreviewInput) (CmsPreviewLink, error) {
	c, err := get(ctx, tx, kind, cid, false)
	if err != nil {
		return CmsPreviewLink{}, err
	}
	pol, err := Policy(ctx, tx, c.PropertyID)
	if err != nil {
		return CmsPreviewLink{}, err
	}
	no := c.LatestVersion
	if in.VersionNo != nil {
		no = *in.VersionNo
	}
	v, err := version(ctx, tx, cid, no)
	if err != nil {
		return CmsPreviewLink{}, err
	}
	lang := in.Language
	if lang == "" {
		lang = pol.DefaultLanguage
	}
	if !contains(pol.SupportedLanguages, lang) {
		return CmsPreviewLink{}, errs.Validation("unsupported_language", "unsupported language", errs.Field("language", "unsupported_language", "not a website language"))
	}
	hours := in.Hours
	if hours <= 0 || hours > pol.PreviewLinkHours {
		hours = pol.PreviewLinkHours
	}
	exp := m.now().Add(time.Duration(hours) * time.Hour).Truncate(time.Second)
	token := m.sign(fmt.Sprintf("v1|%s|%d|%s|%d", cid, no, lang, exp.Unix()))
	slug := v.Document.Translations[lang].Slug
	out := CmsPreviewLink{Token: token, APIPath: "/api/v1/public/cms/preview/" + token, VersionNo: no, Language: lang, ExpiresAt: exp}
	base := ""
	if m.Cfg != nil {
		base = strings.TrimRight(m.Cfg.WebsiteURL, "/")
	}
	out.URL = base + "/" + lang + "/preview/" + token
	if slug != "" {
		out.URL += "?path=" + contentPath(kind, lang, slug, c.Key, c.Template)
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "cms", Action: "preview_link_created", Category: audit.CategorySecurity, EntityType: "cms." + kind,
		EntityID: cid.String(), EntityLabel: c.Title, PropertyID: &c.PropertyID,
		Metadata: map[string]any{"version": no, "language": lang, "expiresAt": exp}})
}

// publicPreview renders the version named by a preview token.
func (m *Module) publicPreview(w http.ResponseWriter, r *http.Request) {
	claims, err := m.verify(chi.URLParam(r, "token"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := dbtx.System(r.Context())
	var out CmsPublicContent
	err = m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var kind string
		var pid uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT kind, property_id FROM cms.contents WHERE id = $1 AND archived_at IS NULL`, claims.content).Scan(&kind, &pid); err != nil {
			if dbtx.IsNoRows(err) {
				return errs.NotFound("preview")
			}
			return err
		}
		q := r.URL.Query()
		q.Set("propertyId", pid.String())
		q.Set("lang", claims.lang)
		r.URL.RawQuery = q.Encode()
		s, err := m.resolveSite(ctx, tx, r)
		if err != nil {
			return err
		}
		sctx := reqctx.WithProperty(dbtx.WithScope(r.Context(), dbtx.Scope{PropertyIDs: []uuid.UUID{pid}}), pid)
		if err := dbtx.ApplyScope(sctx, tx); err != nil {
			return err
		}
		c, err := get(sctx, tx, kind, claims.content, false)
		if err != nil {
			return err
		}
		v, err := version(sctx, tx, claims.content, claims.version)
		if err != nil {
			return err
		}
		pol := s.pol
		pol.FallbackToDefault = true
		s.pol = pol
		out, err = m.render(sctx, tx, s, c, v.Document, claims.version, true)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	httpx.JSON(w, http.StatusOK, out)
}
