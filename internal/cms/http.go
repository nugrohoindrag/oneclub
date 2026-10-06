package cms

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

// Register adds the CMS routes (Back Office, staff app paths /cms/...) and
// the public website API.
func (m *Module) Register(reg *route.Registry, eng *resource.Engine) {
	m.resourceHooks()
	for _, d := range resourceDefs() {
		eng.Register(reg, d)
	}
	add := func(rt route.Route) {
		rt.Module, rt.Scope = "cms", route.ScopeProperty
		if rt.Tag == "" {
			rt.Tag = "CMS"
		}
		reg.Add(rt)
	}
	for _, k := range kindSpecs {
		m.registerKind(add, k)
	}
	m.registerMedia(add)
	m.registerTools(add)
	m.registerPublic(reg)
}

func versionParam(r *http.Request) (int, error) {
	n, err := strconv.Atoi(chi.URLParam(r, "no"))
	if err != nil || n <= 0 {
		return 0, errs.NotFound("version")
	}
	return n, nil
}

func (m *Module) registerKind(add func(route.Route), k kindSpec) {
	db := m.DB
	kind := k.Kind
	tag := "CMS " + strings.ToUpper(k.Plural[:1]) + k.Plural[1:]
	q := []route.Param{{Name: "q", Description: "Search title / key"}, {Name: "filter[status]", Description: "draft,in_review,scheduled,published,unpublished"},
		{Name: "filter[live]", Type: "boolean"}, {Name: "includeArchived", Type: "boolean"}}
	switch kind {
	case KindPage:
		q = append(q, route.Param{Name: "filter[template]"}, route.Param{Name: "filter[parentId]"})
	case KindArticle:
		q = append(q, route.Param{Name: "filter[categoryId]"}, route.Param{Name: "filter[tag]"})
	case KindBanner:
		q = append(q, route.Param{Name: "filter[placement]"})
	}
	add(route.Route{Method: http.MethodGet, Path: k.Path, Tag: tag, Summary: k.Name + "s with status, live flag and translation status per language",
		Permission: k.Perm + ".view", Response: CmsContent{}, List: true, Query: q,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CmsContent], error) {
			lp := httpx.ParseList(r)
			offset := 0
			if c, err := httpx.DecodeCursor(lp.Cursor); err == nil && c != "" {
				offset, _ = strconv.Atoi(c)
			}
			f := lp.Filters
			list, err := handle.List[CmsContent](tx.Query(ctx, contentSelect+` WHERE c.property_id = $1 AND c.kind = $2 AND ($3 OR c.archived_at IS NULL)
				AND ($4 = '' OR c.status = ANY(string_to_array($4, ','))) AND ($5 = '' OR c.template = $5) AND ($6 = '' OR c.placement = $6)
				AND ($7 = '' OR c.category_id::text = $7) AND ($8 = '' OR c.live = ($8 = 'true')) AND ($9 = '' OR c.parent_id::text = $9)
				AND ($10 = '' OR $10 = ANY(c.tags)) AND ($11 = '' OR c.title ILIKE '%' || $11 || '%' OR c.key ILIKE '%' || $11 || '%')
				ORDER BY CASE WHEN c.kind IN ('page', 'banner') THEN c.sort_order END, c.updated_at DESC, c.id LIMIT $12 OFFSET $13`,
				handle.Property(ctx), kind, r.URL.Query().Get("includeArchived") == "true", f["status"], f["template"], f["placement"], f["categoryId"],
				f["live"], f["parentId"], strings.ToLower(f["tag"]), lp.Q, lp.PageSize+1, offset))
			page := httpx.Page[CmsContent]{Items: list}
			if len(list) > lp.PageSize {
				page.Items = list[:lp.PageSize]
				page.NextCursor = httpx.EncodeCursor(strconv.Itoa(offset + lp.PageSize))
			}
			return page, err
		})})
	add(route.Route{Method: http.MethodGet, Path: k.Path + "/{id}", Tag: tag, Summary: "View a " + strings.ToLower(k.Name) + " with its latest version (ETag = version)",
		Permission: k.Perm + ".view", Response: CmsContentDetail{},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			cid, err := handle.ID(r)
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			var out CmsContentDetail
			if err := db.WithReadTx(r.Context(), func(tx pgx.Tx) error {
				out, err = m.detail(r.Context(), tx, kind, cid)
				return err
			}); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			w.Header().Set("ETag", `"`+strconv.Itoa(out.LatestVersion)+`"`)
			httpx.JSON(w, http.StatusOK, out)
		}})
	add(route.Route{Method: http.MethodPost, Path: k.Path, Tag: tag, Summary: "Add a " + strings.ToLower(k.Name) + " (settings + first version, Draft)",
		Permission: k.Perm + ".create", Request: CmsContentInput{}, Response: CmsContentDetail{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CmsContentInput) (CmsContentDetail, error) {
			return m.Create(ctx, tx, kind, in, "create")
		})})
	add(route.Route{Method: http.MethodPatch, Path: k.Path + "/{id}", Tag: tag, Summary: "Edit the settings (template, parent, placement, period …)",
		Permission: k.Perm + ".update", Request: CmsSettings{}, Response: CmsContentDetail{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CmsSettings) (CmsContentDetail, error) {
			cid, err := handle.ID(r)
			if err != nil {
				return CmsContentDetail{}, err
			}
			return m.UpdateSettings(ctx, tx, kind, cid, in)
		})})
	add(route.Route{Method: http.MethodPut, Path: k.Path + "/{id}/content", Tag: tag,
		Summary:    "Save the content as a new version (translations, blocks; If-Match or baseVersion for concurrency)",
		Permission: k.Perm + ".update", Request: CmsContentSave{}, Response: CmsContentDetail{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CmsContentSave) (CmsContentDetail, error) {
			cid, err := handle.ID(r)
			if err != nil {
				return CmsContentDetail{}, err
			}
			if im := strings.Trim(strings.TrimPrefix(r.Header.Get("If-Match"), "W/"), `" `); im != "" && in.BaseVersion == nil {
				n, err := strconv.Atoi(im)
				if err != nil {
					return CmsContentDetail{}, errs.Precondition("If-Match must be the version (ETag) of the content")
				}
				in.BaseVersion = &n
			}
			return m.SaveContent(ctx, tx, kind, cid, in)
		})})
	add(route.Route{Method: http.MethodDelete, Path: k.Path + "/{id}", Tag: tag,
		Summary: "Delete (never published) or archive (published: taken off the website)", Permission: k.Perm + ".delete",
		Handler: handle.Write(db, http.StatusNoContent, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (any, error) {
			cid, err := handle.ID(r)
			if err != nil {
				return nil, err
			}
			return nil, m.Delete(ctx, tx, kind, cid)
		})})
	add(route.Route{Method: http.MethodGet, Path: k.Path + "/{id}/versions", Tag: tag, Summary: "Version history (every save is a version)",
		Permission: k.Perm + ".view", Response: CmsVersion{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CmsVersion], error) {
			cid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[CmsVersion]{}, err
			}
			if _, err := get(ctx, tx, kind, cid, false); err != nil {
				return httpx.Page[CmsVersion]{}, err
			}
			return handle.Page(handle.List[CmsVersion](tx.Query(ctx, versionListSelect+` WHERE v.content_id = $1 ORDER BY v.version_no DESC LIMIT 200`, cid)))
		})})
	add(route.Route{Method: http.MethodGet, Path: k.Path + "/{id}/versions/{no}", Tag: tag, Summary: "View a version",
		Permission: k.Perm + ".view", Response: CmsVersionDetail{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CmsVersionDetail, error) {
			cid, err := handle.ID(r)
			if err != nil {
				return CmsVersionDetail{}, err
			}
			no, err := versionParam(r)
			if err != nil {
				return CmsVersionDetail{}, err
			}
			if _, err := get(ctx, tx, kind, cid, false); err != nil {
				return CmsVersionDetail{}, err
			}
			return version(ctx, tx, cid, no)
		})})
	action := func(verb, perm, summary string, req any, fn func(ctx context.Context, tx pgx.Tx, cid uuid.UUID, r *http.Request) (CmsContentDetail, error)) {
		add(route.Route{Method: http.MethodPost, Path: k.Path + "/{id}:" + verb, Tag: tag, Summary: summary, Permission: k.Perm + "." + perm,
			Request: req, Response: CmsContentDetail{}, Status: http.StatusOK,
			Handler: func(w http.ResponseWriter, r *http.Request) {
				cid, err := handle.ID(r)
				if err != nil {
					httpx.WriteError(w, r, err)
					return
				}
				var out CmsContentDetail
				err = db.WithTx(r.Context(), func(tx pgx.Tx) error {
					var err error
					out, err = fn(r.Context(), tx, cid, r)
					return err
				})
				if err != nil {
					httpx.WriteError(w, r, err)
					return
				}
				httpx.JSON(w, http.StatusOK, out)
			}})
	}
	decode := func(r *http.Request, v any) error { return httpx.Decode(r, v) }
	action("submit", "update", "Submit the latest version for review (approval: Website Content Publication)", CmsSubmitInput{},
		func(ctx context.Context, tx pgx.Tx, cid uuid.UUID, r *http.Request) (CmsContentDetail, error) {
			var in CmsSubmitInput
			if err := decode(r, &in); err != nil {
				return CmsContentDetail{}, err
			}
			return m.Submit(ctx, tx, kind, cid, in)
		})
	action("withdraw", "update", "Withdraw the review (requester)", CmsReasonInput{},
		func(ctx context.Context, tx pgx.Tx, cid uuid.UUID, r *http.Request) (CmsContentDetail, error) {
			var in CmsReasonInput
			if err := decode(r, &in); err != nil {
				return CmsContentDetail{}, err
			}
			return m.Withdraw(ctx, tx, kind, cid, in)
		})
	action("publish", "publish", "Publish the approved version now, or at publishAt (Scheduled)", CmsPublishInput{},
		func(ctx context.Context, tx pgx.Tx, cid uuid.UUID, r *http.Request) (CmsContentDetail, error) {
			var in CmsPublishInput
			if err := decode(r, &in); err != nil {
				return CmsContentDetail{}, err
			}
			return m.Publish(ctx, tx, kind, cid, in)
		})
	action("schedule", "publish", "Schedule the go-live of the approved version and / or the take-down", CmsScheduleInput{},
		func(ctx context.Context, tx pgx.Tx, cid uuid.UUID, r *http.Request) (CmsContentDetail, error) {
			var in CmsScheduleInput
			if err := decode(r, &in); err != nil {
				return CmsContentDetail{}, err
			}
			return m.Schedule(ctx, tx, kind, cid, in)
		})
	action("unpublish", "publish", "Unpublish (take off the website) or cancel a scheduled go-live", CmsReasonInput{},
		func(ctx context.Context, tx pgx.Tx, cid uuid.UUID, r *http.Request) (CmsContentDetail, error) {
			var in CmsReasonInput
			if err := decode(r, &in); err != nil {
				return CmsContentDetail{}, err
			}
			return m.Unpublish(ctx, tx, kind, cid, in)
		})
	action("restore", "update", "Restore an earlier version as a new draft version", CmsVersionInput{},
		func(ctx context.Context, tx pgx.Tx, cid uuid.UUID, r *http.Request) (CmsContentDetail, error) {
			var in CmsVersionInput
			if err := decode(r, &in); err != nil {
				return CmsContentDetail{}, err
			}
			return m.Restore(ctx, tx, kind, cid, in)
		})
	action("rollback", "publish", "Roll back: republish a version that was live before", CmsVersionInput{},
		func(ctx context.Context, tx pgx.Tx, cid uuid.UUID, r *http.Request) (CmsContentDetail, error) {
			var in CmsVersionInput
			if err := decode(r, &in); err != nil {
				return CmsContentDetail{}, err
			}
			return m.Rollback(ctx, tx, kind, cid, in)
		})
	add(route.Route{Method: http.MethodPost, Path: k.Path + "/{id}:preview-link", Tag: tag, Summary: "Create a signed, expiring preview link of a version",
		Permission: k.Perm + ".view", Request: CmsPreviewInput{}, Response: CmsPreviewLink{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CmsPreviewInput) (CmsPreviewLink, error) {
			cid, err := handle.ID(r)
			if err != nil {
				return CmsPreviewLink{}, err
			}
			return m.PreviewLinkFor(ctx, tx, kind, cid, in)
		})})
}

func (m *Module) registerMedia(add func(route.Route)) {
	db := m.DB
	const tag = "CMS Images"
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/cms/media", Tag: tag, Summary: "Images library", Permission: "cms.media.view",
		Response: CmsMedia{}, List: true, Query: []route.Param{{Name: "q"}, {Name: "filter[folder]"}, {Name: "filter[tag]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CmsMedia], error) {
			lp := httpx.ParseList(r)
			offset := 0
			if c, err := httpx.DecodeCursor(lp.Cursor); err == nil && c != "" {
				offset, _ = strconv.Atoi(c)
			}
			list, err := handle.List[CmsMedia](tx.Query(ctx, mediaSelect+` WHERE property_id = $1 AND archived_at IS NULL AND ($2 = '' OR folder = $2)
				AND ($3 = '' OR $3 = ANY(tags)) AND ($4 = '' OR filename ILIKE '%' || $4 || '%' OR alt::text ILIKE '%' || $4 || '%')
				ORDER BY created_at DESC, id LIMIT $5 OFFSET $6`, handle.Property(ctx), lp.Filters["folder"], strings.ToLower(lp.Filters["tag"]), lp.Q,
				lp.PageSize+1, offset))
			page := httpx.Page[CmsMedia]{Items: withURL(list)}
			if len(list) > lp.PageSize {
				page.Items = page.Items[:lp.PageSize]
				page.NextCursor = httpx.EncodeCursor(strconv.Itoa(offset + lp.PageSize))
			}
			return page, err
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/cms/media/{id}", Tag: tag, Summary: "View an image", Permission: "cms.media.view",
		Response: CmsMedia{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CmsMedia, error) {
			mid, err := handle.ID(r)
			if err != nil {
				return CmsMedia{}, err
			}
			return getMedia(ctx, tx, mid)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/cms/media", Tag: tag,
		Summary:    "Upload an image (multipart: file, alt / alt.<lang>, caption / caption.<lang>, tags, folder); web sizes are generated",
		Permission: "cms.media.create", Response: CmsMedia{}, Handler: m.upload})
	add(route.Route{Method: http.MethodPatch, Path: "/api/v1/cms/media/{id}", Tag: tag, Summary: "Edit alt text, caption, tags and folder",
		Permission: "cms.media.update", Request: CmsMediaUpdate{}, Response: CmsMedia{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CmsMediaUpdate) (CmsMedia, error) {
			mid, err := handle.ID(r)
			if err != nil {
				return CmsMedia{}, err
			}
			return m.UpdateMedia(ctx, tx, mid, in)
		})})
	add(route.Route{Method: http.MethodDelete, Path: "/api/v1/cms/media/{id}", Tag: tag, Summary: "Remove an unused image", Permission: "cms.media.delete",
		Handler: handle.Write(db, http.StatusNoContent, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (any, error) {
			mid, err := handle.ID(r)
			if err != nil {
				return nil, err
			}
			return nil, m.DeleteMedia(ctx, tx, mid)
		})})
}

// CmsLanguageState summarises the translations of one language.
type CmsLanguageState struct {
	CmsLanguage
	Required   bool `json:"required" doc:"Must be complete before a review"`
	Complete   int  `json:"complete"`
	Incomplete int  `json:"incomplete"`
	Missing    int  `json:"missing"`
	Outdated   int  `json:"outdated"`
}

// CmsLanguages is the Languages menu.
type CmsLanguages struct {
	DefaultLanguage   string             `json:"defaultLanguage"`
	FallbackToDefault bool               `json:"fallbackToDefault"`
	RequireApproval   bool               `json:"requireApproval"`
	Languages         []CmsLanguageState `json:"languages"`
	PolicyCode        string             `json:"policyCode" doc:"Edited in Settings → Club Policies → Content Policies"`
}

// CmsTranslationRow is the translation status of one content item.
type CmsTranslationRow struct {
	ID           uuid.UUID                      `json:"id" db:"id"`
	Kind         string                         `json:"kind" db:"kind"`
	Key          *string                        `json:"key" db:"key"`
	Title        string                         `json:"title" db:"title"`
	Status       string                         `json:"status" db:"status"`
	Live         bool                           `json:"live" db:"live"`
	Translations map[string]CmsTranslationState `json:"translations" db:"translation_status"`
	UpdatedAt    time.Time                      `json:"updatedAt" db:"updated_at"`
}

// CmsDataSourceUsage is a data source with the content showing it.
type CmsDataSourceUsage struct {
	CmsDataSource
	UsedBy []CmsUsage `json:"usedBy"`
}

// CmsUsage is a content item using a data source.
type CmsUsage struct {
	ID     uuid.UUID `json:"id" db:"id"`
	Kind   string    `json:"kind" db:"kind"`
	Title  string    `json:"title" db:"title"`
	Status string    `json:"status" db:"status"`
	Live   bool      `json:"live" db:"live"`
}

// CmsDataBlockCheck validates a data block configuration.
type CmsDataBlockCheck struct {
	Source string         `json:"source"`
	Filter map[string]any `json:"filter,omitempty"`
	Limit  *int           `json:"limit,omitempty"`
	Layout string         `json:"layout,omitempty"`
}

// CmsScheduleEntry is an upcoming go-live or take-down.
type CmsScheduleEntry struct {
	ID      uuid.UUID `json:"id" db:"id"`
	Kind    string    `json:"kind" db:"kind"`
	Title   string    `json:"title" db:"title"`
	Action  string    `json:"action" db:"action" enum:"publish,unpublish"`
	At      time.Time `json:"at" db:"at"`
	Version *int      `json:"version" db:"version"`
	Overdue bool      `json:"overdue" db:"overdue"`
}

// CmsPublishEvent is one entry of the publishing log.
type CmsPublishEvent struct {
	ID        uuid.UUID `json:"id" db:"id"`
	ContentID uuid.UUID `json:"contentId" db:"content_id"`
	Kind      string    `json:"kind" db:"kind"`
	Title     string    `json:"title" db:"title"`
	Action    string    `json:"action" db:"action"`
	VersionNo *int      `json:"versionNo" db:"version_no"`
	ActorName *string   `json:"actorName" db:"actor_name"`
	Note      *string   `json:"note" db:"note"`
	At        time.Time `json:"at" db:"at"`
}

// CmsRevision is the website revision after a purge.
type CmsRevision struct {
	Revision int64 `json:"revision"`
}

func (m *Module) registerTools(add func(route.Route)) {
	db := m.DB
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/cms/languages", Tag: "CMS Languages",
		Summary: "Languages: website languages (Content Policies) and translation status counts", Permission: "cms.language.view", Response: CmsLanguages{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CmsLanguages, error) {
			pid := handle.Property(ctx)
			pol, err := Policy(ctx, tx, pid)
			if err != nil {
				return CmsLanguages{}, err
			}
			out := CmsLanguages{DefaultLanguage: pol.DefaultLanguage, FallbackToDefault: pol.FallbackToDefault, RequireApproval: pol.RequireApproval,
				Languages: []CmsLanguageState{}, PolicyCode: PolicyCode}
			counts := map[string]map[string]int{}
			rows, err := tx.Query(ctx, `SELECT t.key, t.value->>'status', count(*) FROM cms.contents c, jsonb_each(c.translation_status) t
				WHERE c.property_id = $1 AND c.archived_at IS NULL GROUP BY 1, 2`, pid)
			if err != nil {
				return out, err
			}
			for rows.Next() {
				var lang, st string
				var n int
				if err := rows.Scan(&lang, &st, &n); err != nil {
					rows.Close()
					return out, err
				}
				if counts[lang] == nil {
					counts[lang] = map[string]int{}
				}
				counts[lang][st] = n
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return out, err
			}
			for _, l := range pol.SupportedLanguages {
				name := LanguageNames[l]
				if name == "" {
					name = l
				}
				c := counts[l]
				out.Languages = append(out.Languages, CmsLanguageState{CmsLanguage: CmsLanguage{Code: l, Name: name, Default: l == pol.DefaultLanguage},
					Required: contains(pol.RequiredLanguages, l), Complete: c["complete"], Incomplete: c["incomplete"], Missing: c["missing"], Outdated: c["outdated"]})
			}
			return out, nil
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/cms/translation-status", Tag: "CMS Languages", Summary: "Translation status per content item and language",
		Permission: "cms.language.view", Response: CmsTranslationRow{}, List: true,
		Query: []route.Param{{Name: "filter[kind]"}, {Name: "filter[language]"}, {Name: "filter[translation]", Description: "missing,incomplete,complete,outdated"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CmsTranslationRow], error) {
			f := httpx.ParseList(r).Filters
			return handle.Page(handle.List[CmsTranslationRow](tx.Query(ctx, `SELECT id, kind, key, title, status, live, translation_status, updated_at
				FROM cms.contents c WHERE property_id = $1 AND archived_at IS NULL AND ($2 = '' OR kind = $2)
				AND ($3 = '' OR $4 = '' OR translation_status->$3->>'status' = ANY(string_to_array($4, ',')))
				AND ($3 <> '' OR $4 = '' OR EXISTS (SELECT 1 FROM jsonb_each(c.translation_status) t WHERE t.value->>'status' = ANY(string_to_array($4, ','))))
				ORDER BY kind, title LIMIT 1000`, handle.Property(ctx), f["kind"], f["language"], f["translation"])))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/cms/data-sources", Tag: "CMS Data Blocks",
		Summary:    "Structured data a data block can show (Pricing, Packages, Promotions, Events …, contract K5) and the content using each",
		Permission: "cms.page.view", Response: CmsDataSourceUsage{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CmsDataSourceUsage], error) {
			out := []CmsDataSourceUsage{}
			for _, s := range DataSources {
				used, err := handle.List[CmsUsage](tx.Query(ctx, `SELECT c.id, c.kind, c.title, c.status, c.live FROM cms.contents c
					JOIN cms.content_versions v ON v.content_id = c.id AND v.version_no = c.latest_version
					WHERE c.property_id = $1 AND c.archived_at IS NULL
					AND v.document->'blocks' @> jsonb_build_array(jsonb_build_object('type', 'data', 'config', jsonb_build_object('source', $2::text)))
					ORDER BY c.kind, c.title`, handle.Property(ctx), s.Key))
				if err != nil {
					return httpx.Page[CmsDataSourceUsage]{}, err
				}
				ds := s
				if ds.Filters == nil {
					ds.Filters = []CmsDataFilter{}
				}
				out = append(out, CmsDataSourceUsage{CmsDataSource: ds, UsedBy: used})
			}
			return httpx.Page[CmsDataSourceUsage]{Items: out}, nil
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/cms/data-sources:validate", Tag: "CMS Data Blocks",
		Summary:    "Check a data block (source, filter, limit) and show the public API the website will read",
		Permission: "cms.page.view", Request: CmsDataBlockCheck{}, Response: CmsDataRef{}, Status: http.StatusOK, NoAudit: "read-only validation",
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CmsDataBlockCheck) (CmsDataRef, error) {
			pid := handle.Property(ctx)
			pol, err := Policy(ctx, tx, pid)
			if err != nil {
				return CmsDataRef{}, err
			}
			cfg := map[string]any{"source": in.Source}
			if in.Filter != nil {
				cfg["filter"] = in.Filter
			}
			if in.Limit != nil {
				cfg["limit"] = float64(*in.Limit)
			}
			if in.Layout != "" {
				cfg["layout"] = in.Layout
			}
			blocks := []CmsBlock{{Type: "data", Config: cfg}}
			if err := checkBlocks(blocks, pol, &refs{}); err != nil {
				return CmsDataRef{}, err
			}
			var code string
			if err := tx.QueryRow(ctx, `SELECT code FROM platform.properties WHERE id = $1`, pid).Scan(&code); err != nil {
				return CmsDataRef{}, err
			}
			ref := dataRef(blocks[0].Config, pid, code, pol.DefaultLanguage)
			if ref == nil {
				return CmsDataRef{}, handle.Invalid("source", "invalid_option", "unknown data source")
			}
			return *ref, nil
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/cms/schedule", Tag: "CMS", Summary: "Scheduled go-lives and take-downs (next 60 days and overdue)",
		Permission: "cms.page.view", Response: CmsScheduleEntry{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CmsScheduleEntry], error) {
			return handle.Page(handle.List[CmsScheduleEntry](tx.Query(ctx, `SELECT id, kind, title, 'publish' AS action, publish_at AS at, approved_version AS version,
				publish_at < now() AS overdue FROM cms.contents WHERE property_id = $1 AND archived_at IS NULL AND status = 'scheduled'
				AND publish_at < now() + interval '60 days'
				UNION ALL SELECT id, kind, title, 'unpublish', unpublish_at, published_version, unpublish_at < now() FROM cms.contents
				WHERE property_id = $1 AND archived_at IS NULL AND live AND unpublish_at < now() + interval '60 days' ORDER BY at`, handle.Property(ctx))))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/cms/publishing-log", Tag: "CMS", Summary: "Publishing log (saved, submitted, approved, published …)",
		Permission: "cms.page.view", Response: CmsPublishEvent{}, List: true, Query: []route.Param{{Name: "filter[contentId]"}, {Name: "filter[action]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CmsPublishEvent], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[CmsPublishEvent](tx.Query(ctx, `SELECT e.id, e.content_id, e.kind, c.title, e.action, e.version_no, e.actor_name, e.note, e.at
				FROM cms.publish_events e JOIN cms.contents c ON c.id = e.content_id WHERE e.property_id = $1 AND ($2 = '' OR e.content_id::text = $2)
				AND ($3 = '' OR e.action = $3) ORDER BY e.at DESC, e.id LIMIT $4`, handle.Property(ctx), lp.Filters["contentId"], lp.Filters["action"], lp.Limit)))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/cms/site:revalidate", Tag: "CMS",
		Summary: "Purge the website cache: bump the revision and ask the website to revalidate every page", Permission: "cms.page.publish",
		Response: CmsRevision{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (CmsRevision, error) {
			pid := handle.Property(ctx)
			if err := m.touchSite(ctx, tx, pid); err != nil {
				return CmsRevision{}, err
			}
			var rev int64
			if err := tx.QueryRow(ctx, `SELECT revision FROM cms.site_state WHERE property_id = $1`, pid).Scan(&rev); err != nil {
				return CmsRevision{}, err
			}
			return CmsRevision{Revision: rev}, audit.Record(ctx, tx, audit.Entry{Module: "cms", Action: "cache_purged", EntityType: "cms.site",
				EntityID: pid.String(), EntityLabel: "Website cache", PropertyID: &pid, After: map[string]any{"revision": rev}})
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/cms/imports", Tag: "CMS",
		Summary:    "Import website content (EP-29: pages, news, redirects of the old website) as draft versions; dryRun validates only",
		Permission: "cms.import.create", Request: CmsImportRequest{}, Response: CmsImportResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CmsImportRequest) (CmsImportResult, error) {
			return m.Import(ctx, tx, in)
		})})
}

func (m *Module) registerPublic(reg *route.Registry) {
	pub := func(rt route.Route) {
		rt.Module, rt.Tag, rt.Auth = "cms", "Public", route.AuthPublic
		rt.Query = append([]route.Param{{Name: "propertyId", Description: "Property id (default: the main property)"},
			{Name: "property", Description: "Property code (alternative to propertyId)"}}, rt.Query...)
		reg.Add(rt)
	}
	lang := route.Param{Name: "lang", Description: "Language (default: the default language; missing translations fall back to it)"}
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/cms/site", Summary: "Website languages and revision (cache key)",
		Response: CmsPublicSite{}, Handler: m.public(m.publicSite)})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/cms/pages", Summary: "Published pages with their path per language (routing, static generation)",
		Response: CmsPublicList{}, Query: []route.Param{lang}, Handler: m.public(m.publicPages)})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/cms/pages/{slug}", Summary: "Published page by slug or key (blocks, SEO, data block endpoints; ETag)",
		Response: CmsPublicContent{}, Query: []route.Param{lang}, Handler: m.public(m.publicPage)})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/cms/navigation", Summary: "Navigation menus (header, footer …) in a language",
		Response: CmsPublicNavigation{}, Query: []route.Param{lang, {Name: "location"}}, Handler: m.public(m.publicNavigation)})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/cms/banners", Summary: "Banners of a placement (and page) in their display period",
		Response: CmsPublicBanners{}, Query: []route.Param{lang, {Name: "placement"}, {Name: "page", Description: "Page key, slug or id"}},
		Handler: m.public(m.publicBanners)})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/cms/news", Summary: "News list (category, tag, featured)", Response: CmsPublicList{},
		Query:   []route.Param{lang, {Name: "category"}, {Name: "tag"}, {Name: "featured", Type: "boolean"}, {Name: "limit", Type: "integer"}, {Name: "cursor"}},
		Handler: m.public(m.publicNews)})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/cms/news/{slug}", Summary: "News article", Response: CmsPublicContent{},
		Query: []route.Param{lang}, Handler: m.public(m.publicArticle)})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/cms/gallery", Summary: "Gallery albums", Response: CmsPublicList{},
		Query: []route.Param{lang, {Name: "limit", Type: "integer"}, {Name: "cursor"}}, Handler: m.public(m.publicGalleries)})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/cms/gallery/{slug}", Summary: "Gallery album with its images", Response: CmsPublicContent{},
		Query: []route.Param{lang}, Handler: m.public(m.publicGallery)})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/cms/contact", Summary: "Contact Information (address, phones, WhatsApp, e-mail, map, opening hours)",
		Response: CmsPublicContacts{}, Query: []route.Param{lang}, Handler: m.public(m.publicContact)})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/cms/course-guide", Summary: "Course Guide texts per hole (complete /public/golf/info)",
		Response: CmsPublicCourseGuide{}, Query: []route.Param{lang, {Name: "course", Description: "Course code"}}, Handler: m.public(m.publicCourseGuide)})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/cms/sitemap", Summary: "Sitemap data (pages, news, albums, structured pages; hreflang)",
		Response: CmsSitemap{}, Handler: m.public(m.publicSitemap)})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/cms/sitemap.xml", Summary: "sitemap.xml", RawContent: "application/xml",
		Handler: limited(m.sitemapXML)})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/cms/robots", Summary: "robots.txt data (indexing, disallowed paths, sitemap)",
		Response: CmsRobots{}, Handler: m.public(m.publicRobots)})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/cms/redirects", Summary: "Active redirects (old URL → new URL)",
		Response: CmsPublicRedirects{}, Handler: m.public(m.publicRedirects)})
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/public/cms/preview/{token}", Module: "cms", Tag: "Public", Auth: route.AuthPublic,
		Summary: "Preview a version through a signed, expiring link (never cached, noindex)", Response: CmsPublicContent{}, Handler: limited(m.publicPreview)})
}
