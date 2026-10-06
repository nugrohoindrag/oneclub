package cms

// Publication workflow (FR-CMS-06, PRD P4 §7.6 statuses, §9.5 Website
// Update): Draft → In Review (approval, optional by Content Policies) →
// Scheduled → Published → Unpublished, with versions, rollback and
// automatic go-live / take-down by the scheduler. "live" tells whether the
// published version is on the website; a new version can be in review or
// scheduled while the previous one stays live.

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/notify"
)

// CmsSubmitInput submits the latest version for review.
type CmsSubmitInput struct {
	Note string `json:"note,omitempty"`
}

// CmsPublishInput publishes now or at publishAt.
type CmsPublishInput struct {
	PublishAt   *time.Time `json:"publishAt,omitempty" doc:"Go live later (status Scheduled)"`
	UnpublishAt *time.Time `json:"unpublishAt,omitempty" doc:"Take down automatically"`
	Note        string     `json:"note,omitempty"`
}

// CmsScheduleInput sets the go-live and/or take-down time.
type CmsScheduleInput struct {
	PublishAt   *time.Time `json:"publishAt,omitempty"`
	UnpublishAt *time.Time `json:"unpublishAt,omitempty"`
}

// CmsReasonInput carries a reason (unpublish, withdraw).
type CmsReasonInput struct {
	Reason string `json:"reason,omitempty"`
}

// CmsVersionInput names a version (restore, rollback).
type CmsVersionInput struct {
	VersionNo int    `json:"versionNo"`
	Reason    string `json:"reason,omitempty"`
}

// requiredComplete checks the languages that must be complete before review.
func requiredComplete(c CmsContent, pol ContentPolicy) error {
	var fields []errs.FieldError
	for _, lang := range pol.RequiredLanguages {
		st := c.Translations[lang]
		if st.Status == "missing" || st.Status == "incomplete" {
			fields = append(fields, errs.Field("translations."+lang, "translation_"+st.Status,
				fmt.Sprintf("the %s translation is %s (required by Content Policies)", lang, st.Status)))
		}
	}
	if len(fields) > 0 {
		return errs.Validation("translation_incomplete", "required translations are not complete", fields...)
	}
	return nil
}

// Submit sends the latest version to review (approval engine); without a
// configured workflow the request is approved at once.
func (m *Module) Submit(ctx context.Context, tx pgx.Tx, kind string, cid uuid.UUID, in CmsSubmitInput) (CmsContentDetail, error) {
	c, err := get(ctx, tx, kind, cid, true)
	if err != nil {
		return CmsContentDetail{}, err
	}
	if c.Status == "in_review" {
		return CmsContentDetail{}, errs.Conflict("in_review", "the content is already in review")
	}
	if c.ApprovedVersion != nil && *c.ApprovedVersion == c.LatestVersion {
		return CmsContentDetail{}, errs.Conflict("already_approved", "the latest version is already approved; publish or schedule it")
	}
	pol, err := Policy(ctx, tx, c.PropertyID)
	if err != nil {
		return CmsContentDetail{}, err
	}
	if err := requiredComplete(c, pol); err != nil {
		return CmsContentDetail{}, err
	}
	langs := 0
	for _, st := range c.Translations {
		if st.Status == "complete" || st.Status == "outdated" {
			langs++
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE cms.contents SET status_before_review = status, status = 'in_review', review_version = latest_version,
		updated_by = $2 WHERE id = $1`, cid, actorID(ctx)); err != nil {
		return CmsContentDetail{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE cms.content_versions SET review_status = 'in_review' WHERE content_id = $1 AND version_no = $2`, cid, c.LatestVersion); err != nil {
		return CmsContentDetail{}, err
	}
	if err := m.logEvent(ctx, tx, c, "submitted", c.LatestVersion, in.Note); err != nil {
		return CmsContentDetail{}, err
	}
	first := 0
	if c.PublishedVersion == nil {
		first = 1
	}
	ref := deref(c.Key)
	if ref == "" {
		ref = c.Title
	}
	rid, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: ContentDocumentType.Code, DocumentID: cid,
		DocumentRef: fmt.Sprintf("%s v%d", ref, c.LatestVersion), Title: specOf(kind).Name + " · " + c.Title, PropertyID: c.PropertyID,
		Attributes: map[string]any{"kind": kind, "template": deref(c.Template), "languages": langs, "firstPublication": first}})
	if err != nil {
		return CmsContentDetail{}, err
	}
	// The decision hook may already have run (no workflow): link the
	// request only while the version is still in review.
	if _, err := tx.Exec(ctx, `UPDATE cms.contents SET approval_request_id = $2 WHERE id = $1 AND review_version = $3`, cid, rid, c.LatestVersion); err != nil {
		return CmsContentDetail{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE cms.content_versions SET approval_request_id = $3 WHERE content_id = $1 AND version_no = $2`, cid, c.LatestVersion, rid); err != nil {
		return CmsContentDetail{}, err
	}
	out, err := m.detail(ctx, tx, kind, cid)
	if err != nil {
		return out, err
	}
	return out, m.audit(ctx, tx, out.CmsContent, "submit", map[string]any{"status": c.Status}, map[string]any{"status": out.Status, "approvalRequestId": rid,
		"version": c.LatestVersion}, in.Note, nil)
}

// Withdraw cancels the pending review (the requester only).
func (m *Module) Withdraw(ctx context.Context, tx pgx.Tx, kind string, cid uuid.UUID, in CmsReasonInput) (CmsContentDetail, error) {
	c, err := get(ctx, tx, kind, cid, false)
	if err != nil {
		return CmsContentDetail{}, err
	}
	if c.Status != "in_review" || c.ApprovalRequestID == nil {
		return CmsContentDetail{}, errs.Conflict("not_in_review", "the content is not in review")
	}
	reason := in.Reason
	if strings.TrimSpace(reason) == "" {
		reason = "withdrawn by the editor"
	}
	if err := m.Approvals.Cancel(ctx, tx, *c.ApprovalRequestID, reason); err != nil {
		return CmsContentDetail{}, err
	}
	out, err := m.detail(ctx, tx, kind, cid)
	if err != nil {
		return out, err
	}
	return out, m.audit(ctx, tx, out.CmsContent, "withdraw", map[string]any{"status": c.Status}, map[string]any{"status": out.Status}, reason, nil)
}

// Decision is the approval hook of website content.
func (m *Module) Decision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	var kind string
	if err := tx.QueryRow(ctx, `SELECT kind FROM cms.contents WHERE id = $1`, d.DocumentID).Scan(&kind); err != nil {
		if dbtx.IsNoRows(err) {
			return nil
		}
		return err
	}
	c, err := get(ctx, tx, kind, d.DocumentID, true)
	if err != nil {
		if errs.Is(err, errs.KindNotFound) {
			return nil
		}
		return err
	}
	if c.ReviewVersion == nil {
		return nil
	}
	v := *c.ReviewVersion
	if d.Status != approval.StatusApproved {
		st := map[string]string{approval.StatusRejected: "rejected", approval.StatusCancelled: "withdrawn"}[d.Status]
		if st == "" {
			st = "rejected"
		}
		if _, err := tx.Exec(ctx, `UPDATE cms.content_versions SET review_status = $3 WHERE content_id = $1 AND version_no = $2`, c.ID, v, st); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE cms.contents SET status = coalesce(status_before_review, 'draft'), status_before_review = NULL,
			review_version = NULL, approval_request_id = NULL WHERE id = $1`, c.ID); err != nil {
			return err
		}
		return m.logEvent(ctx, tx, c, st, v, d.Reason)
	}
	now := m.now()
	if _, err := tx.Exec(ctx, `UPDATE cms.content_versions SET review_status = 'approved', approved_at = $3 WHERE content_id = $1 AND version_no = $2`,
		c.ID, v, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE cms.contents SET approved_version = $2, review_version = NULL, approval_request_id = NULL,
		status = coalesce(status_before_review, 'draft'), status_before_review = NULL WHERE id = $1`, c.ID, v); err != nil {
		return err
	}
	if err := m.logEvent(ctx, tx, c, "approved", v, d.Reason); err != nil {
		return err
	}
	if c.PublishAt != nil && c.PublishAt.After(now) {
		return m.schedule(ctx, tx, c, v)
	}
	return m.publishNow(ctx, tx, c.ID, v, now)
}

// schedule marks an approved version to go live at publish_at.
func (m *Module) schedule(ctx context.Context, tx pgx.Tx, c CmsContent, v int) error {
	if _, err := tx.Exec(ctx, `UPDATE cms.contents SET status = CASE WHEN status = 'in_review' THEN status ELSE 'scheduled' END,
		status_before_review = CASE WHEN status = 'in_review' THEN 'scheduled' ELSE status_before_review END, approved_version = $2 WHERE id = $1`, c.ID, v); err != nil {
		return err
	}
	return m.logEvent(ctx, tx, c, "scheduled", v, "go-live "+c.PublishAt.UTC().Format(time.RFC3339))
}

// publishNow puts a version on the website.
func (m *Module) publishNow(ctx context.Context, tx pgx.Tx, cid uuid.UUID, v int, now time.Time) error {
	var kind string
	if err := tx.QueryRow(ctx, `SELECT kind FROM cms.contents WHERE id = $1`, cid).Scan(&kind); err != nil {
		return err
	}
	c, err := get(ctx, tx, kind, cid, true)
	if err != nil {
		return err
	}
	pol, err := Policy(ctx, tx, c.PropertyID)
	if err != nil {
		return err
	}
	var prev *CmsVersionDetail
	if c.PublishedVersion != nil {
		pv, err := version(ctx, tx, cid, *c.PublishedVersion)
		if err != nil {
			return err
		}
		prev = &pv
	}
	next, err := version(ctx, tx, cid, v)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE cms.contents SET live = true, published_version = $2, approved_version = $2, published_at = $3,
		first_published_at = coalesce(first_published_at, $3), unpublished_at = NULL, publish_at = NULL,
		status = CASE WHEN status = 'in_review' THEN status ELSE 'published' END,
		status_before_review = CASE WHEN status = 'in_review' THEN 'published' ELSE NULL END WHERE id = $1`, cid, v, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE cms.content_versions SET published_at = coalesce(published_at, $3),
		review_status = coalesce(review_status, 'approved'), approved_at = coalesce(approved_at, $3) WHERE content_id = $1 AND version_no = $2`, cid, v, now); err != nil {
		return err
	}
	if err := m.syncSlugs(ctx, tx, cid); err != nil {
		return err
	}
	paths := m.paths(c, next.Document)
	if prev != nil {
		old := m.paths(c, prev.Document)
		if pol.AutoRedirectOnSlugChange {
			if err := m.autoRedirects(ctx, tx, c, prev.Document, next.Document); err != nil {
				return err
			}
		}
		for _, p := range old {
			if !slices.Contains(paths, p) {
				paths = append(paths, p)
			}
		}
	}
	// A live page path must never be redirected away.
	if live := m.paths(c, next.Document); len(live) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE cms.redirects SET status = 'inactive', note = coalesce(note, 'deactivated: the path is a live page')
			WHERE property_id = $1 AND from_path = ANY($2) AND status = 'active' AND archived_at IS NULL`, c.PropertyID, live); err != nil {
			return err
		}
	}
	if err := m.logEvent(ctx, tx, c, "published", v, ""); err != nil {
		return err
	}
	if err := m.announce(ctx, tx, c, "published", v, paths); err != nil {
		return err
	}
	return m.audit(ctx, tx, c, "publish", map[string]any{"publishedVersion": c.PublishedVersion, "live": c.Live},
		map[string]any{"publishedVersion": v, "live": true, "paths": paths}, "", nil)
}

// paths are the website paths of a document in every language.
func (m *Module) paths(c CmsContent, doc CmsDocument) []string {
	var out []string
	for lang, t := range doc.Translations {
		if p := contentPath(c.Kind, lang, t.Slug, c.Key, c.Template); p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

// autoRedirects creates 301 redirects for slugs that changed on publication.
func (m *Module) autoRedirects(ctx context.Context, tx pgx.Tx, c CmsContent, prev, next CmsDocument) error {
	for lang, t := range prev.Translations {
		from := contentPath(c.Kind, lang, t.Slug, c.Key, c.Template)
		nt, ok := next.Translations[lang]
		if !ok || from == "" {
			continue
		}
		to := contentPath(c.Kind, lang, nt.Slug, c.Key, c.Template)
		if to == "" || to == from {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO cms.redirects (id, property_id, from_path, to_path, status_code, source, note, created_by, updated_by)
			VALUES ($1,$2,$3,$4,301,'auto',$5,$6,$6) ON CONFLICT (property_id, from_path) WHERE archived_at IS NULL
			DO UPDATE SET to_path = EXCLUDED.to_path, status = 'active', source = 'auto'`,
			id.New(), c.PropertyID, from, to, "slug changed: "+c.Title, actorID(ctx)); err != nil {
			return err
		}
		// Chains A → B → C collapse to A → C.
		if _, err := tx.Exec(ctx, `UPDATE cms.redirects SET to_path = $3 WHERE property_id = $1 AND to_path = $2 AND archived_at IS NULL`,
			c.PropertyID, from, to); err != nil {
			return err
		}
	}
	return nil
}

// announce bumps the website revision and publishes cms.<kind>_<action>.
func (m *Module) announce(ctx context.Context, tx pgx.Tx, c CmsContent, action string, v int, paths []string) error {
	rev, err := m.bumpRevision(ctx, tx, c.PropertyID)
	if err != nil || m.Events == nil {
		return err
	}
	if paths == nil {
		paths = []string{}
	}
	cid := c.ID
	_, err = m.Events.Publish(ctx, tx, "cms."+c.Kind+"_"+action, "cms."+c.Kind, &cid, &c.PropertyID, SiteChanged{PropertyID: c.PropertyID,
		Revision: rev, Paths: paths, ContentID: &cid, Kind: c.Kind, Key: c.Key, Title: c.Title, VersionNo: v, Action: action})
	return err
}

// takeDown removes the live version from the website.
func (m *Module) takeDown(ctx context.Context, tx pgx.Tx, c CmsContent, reason string, now time.Time) error {
	if _, err := tx.Exec(ctx, `UPDATE cms.contents SET live = false, unpublished_at = $2, unpublish_at = NULL, publish_at = NULL,
		status = CASE WHEN status = 'in_review' THEN status ELSE 'unpublished' END,
		status_before_review = CASE WHEN status = 'in_review' THEN 'unpublished' ELSE NULL END WHERE id = $1`, c.ID, now); err != nil {
		return err
	}
	v := deref(c.PublishedVersion)
	if err := m.logEvent(ctx, tx, c, "unpublished", v, reason); err != nil {
		return err
	}
	var paths []string
	if c.PublishedVersion != nil {
		pv, err := version(ctx, tx, c.ID, *c.PublishedVersion)
		if err != nil {
			return err
		}
		paths = m.paths(c, pv.Document)
	}
	if err := m.announce(ctx, tx, c, "unpublished", v, paths); err != nil {
		return err
	}
	return m.audit(ctx, tx, c, "unpublish", map[string]any{"live": c.Live, "status": c.Status}, map[string]any{"live": false}, reason, nil)
}

// Publish publishes the approved version now (or schedules it). When the
// Content Policies do not require approval, the latest version is approved
// by the publisher.
func (m *Module) Publish(ctx context.Context, tx pgx.Tx, kind string, cid uuid.UUID, in CmsPublishInput) (CmsContentDetail, error) {
	c, err := get(ctx, tx, kind, cid, true)
	if err != nil {
		return CmsContentDetail{}, err
	}
	if c.Status == "in_review" {
		return CmsContentDetail{}, errs.Conflict("in_review", "the content is in review; wait for the decision or withdraw it")
	}
	pol, err := Policy(ctx, tx, c.PropertyID)
	if err != nil {
		return CmsContentDetail{}, err
	}
	now := m.now()
	target := 0
	switch {
	case !pol.RequireApproval && (c.ApprovedVersion == nil || *c.ApprovedVersion < c.LatestVersion):
		if err := requiredComplete(c, pol); err != nil {
			return CmsContentDetail{}, err
		}
		target = c.LatestVersion
		if _, err := tx.Exec(ctx, `UPDATE cms.content_versions SET review_status = 'approved', approved_at = $3 WHERE content_id = $1 AND version_no = $2`,
			cid, target, now); err != nil {
			return CmsContentDetail{}, err
		}
		if err := m.logEvent(ctx, tx, c, "approved", target, "published without review (Content Policies)"); err != nil {
			return CmsContentDetail{}, err
		}
	case c.ApprovedVersion != nil:
		target = *c.ApprovedVersion
	default:
		return CmsContentDetail{}, errs.Conflict("approval_required", "submit the content for review first (Content Policies require approval)")
	}
	if c.Live && c.PublishedVersion != nil && *c.PublishedVersion == target {
		return CmsContentDetail{}, errs.Conflict("already_published", "this version is already live")
	}
	if in.UnpublishAt != nil {
		if !in.UnpublishAt.After(now) || (in.PublishAt != nil && !in.UnpublishAt.After(*in.PublishAt)) {
			return CmsContentDetail{}, handle.Invalid("unpublishAt", "invalid", "the take-down time must be in the future and after the go-live")
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE cms.contents SET approved_version = $2, publish_at = $3, unpublish_at = coalesce($4, unpublish_at), updated_by = $5
		WHERE id = $1`, cid, target, in.PublishAt, in.UnpublishAt, actorID(ctx)); err != nil {
		return CmsContentDetail{}, err
	}
	if in.PublishAt != nil && in.PublishAt.After(now) {
		c.PublishAt = in.PublishAt
		if err := m.schedule(ctx, tx, c, target); err != nil {
			return CmsContentDetail{}, err
		}
		out, err := m.detail(ctx, tx, kind, cid)
		if err != nil {
			return out, err
		}
		return out, m.audit(ctx, tx, out.CmsContent, "schedule", map[string]any{"status": c.Status}, map[string]any{"status": out.Status,
			"publishAt": in.PublishAt, "version": target}, in.Note, nil)
	}
	if err := m.publishNow(ctx, tx, cid, target, now); err != nil {
		return CmsContentDetail{}, err
	}
	return m.detail(ctx, tx, kind, cid)
}

// Schedule sets the go-live of an approved version and/or the take-down.
func (m *Module) Schedule(ctx context.Context, tx pgx.Tx, kind string, cid uuid.UUID, in CmsScheduleInput) (CmsContentDetail, error) {
	c, err := get(ctx, tx, kind, cid, true)
	if err != nil {
		return CmsContentDetail{}, err
	}
	if in.PublishAt == nil && in.UnpublishAt == nil {
		return CmsContentDetail{}, handle.Invalid("publishAt", "required", "set publishAt and / or unpublishAt")
	}
	if c.Status == "in_review" {
		return CmsContentDetail{}, errs.Conflict("in_review", "the content is in review")
	}
	now := m.now()
	before := map[string]any{"status": c.Status, "publishAt": c.PublishAt, "unpublishAt": c.UnpublishAt}
	if in.PublishAt != nil {
		if !in.PublishAt.After(now) {
			return CmsContentDetail{}, handle.Invalid("publishAt", "past", "the go-live must be in the future (or publish now)")
		}
		pol, err := Policy(ctx, tx, c.PropertyID)
		if err != nil {
			return CmsContentDetail{}, err
		}
		if c.ApprovedVersion == nil || (!pol.RequireApproval && *c.ApprovedVersion < c.LatestVersion) {
			if pol.RequireApproval {
				return CmsContentDetail{}, errs.Conflict("approval_required", "submit the content for review first (Content Policies require approval)")
			}
			return m.Publish(ctx, tx, kind, cid, CmsPublishInput{PublishAt: in.PublishAt, UnpublishAt: in.UnpublishAt})
		}
		if c.Live && c.PublishedVersion != nil && *c.PublishedVersion == *c.ApprovedVersion {
			return CmsContentDetail{}, handle.Invalid("publishAt", "already_live", "the approved version is already live; set only unpublishAt")
		}
		at := in.PublishAt.UTC()
		c.PublishAt = &at
		if _, err := tx.Exec(ctx, `UPDATE cms.contents SET publish_at = $2 WHERE id = $1`, cid, at); err != nil {
			return CmsContentDetail{}, err
		}
		if err := m.schedule(ctx, tx, c, *c.ApprovedVersion); err != nil {
			return CmsContentDetail{}, err
		}
	}
	if in.UnpublishAt != nil {
		at := in.UnpublishAt.UTC()
		if !at.After(now) || (c.PublishAt != nil && !at.After(*c.PublishAt)) {
			return CmsContentDetail{}, handle.Invalid("unpublishAt", "invalid", "the take-down must be in the future and after the go-live")
		}
		if !c.Live && c.Status != "scheduled" {
			return CmsContentDetail{}, errs.Conflict("not_published", "only live or scheduled content can be taken down later")
		}
		if _, err := tx.Exec(ctx, `UPDATE cms.contents SET unpublish_at = $2 WHERE id = $1`, cid, at); err != nil {
			return CmsContentDetail{}, err
		}
	}
	out, err := m.detail(ctx, tx, kind, cid)
	if err != nil {
		return out, err
	}
	return out, m.audit(ctx, tx, out.CmsContent, "schedule", before, map[string]any{"status": out.Status, "publishAt": out.PublishAt,
		"unpublishAt": out.UnpublishAt}, "", nil)
}

// Unpublish takes the content off the website (or cancels its go-live).
func (m *Module) Unpublish(ctx context.Context, tx pgx.Tx, kind string, cid uuid.UUID, in CmsReasonInput) (CmsContentDetail, error) {
	c, err := get(ctx, tx, kind, cid, true)
	if err != nil {
		return CmsContentDetail{}, err
	}
	if err := handle.Required("reason", in.Reason); err != nil {
		return CmsContentDetail{}, err
	}
	switch {
	case c.Live:
		if err := m.takeDown(ctx, tx, c, in.Reason, m.now()); err != nil {
			return CmsContentDetail{}, err
		}
	case c.Status == "scheduled":
		if _, err := tx.Exec(ctx, `UPDATE cms.contents SET publish_at = NULL, unpublish_at = NULL,
			status = CASE WHEN published_version IS NULL THEN 'draft' ELSE 'unpublished' END WHERE id = $1`, cid); err != nil {
			return CmsContentDetail{}, err
		}
		if err := m.logEvent(ctx, tx, c, "unpublished", deref(c.ApprovedVersion), "schedule cancelled: "+in.Reason); err != nil {
			return CmsContentDetail{}, err
		}
		if err := m.audit(ctx, tx, c, "unpublish", map[string]any{"status": c.Status}, map[string]any{"scheduleCancelled": true}, in.Reason, nil); err != nil {
			return CmsContentDetail{}, err
		}
	default:
		return CmsContentDetail{}, errs.Conflict("not_published", "the content is neither live nor scheduled")
	}
	return m.detail(ctx, tx, kind, cid)
}

// Restore copies an earlier version into a new version (draft changes).
func (m *Module) Restore(ctx context.Context, tx pgx.Tx, kind string, cid uuid.UUID, in CmsVersionInput) (CmsContentDetail, error) {
	c, err := get(ctx, tx, kind, cid, true)
	if err != nil {
		return CmsContentDetail{}, err
	}
	if c.Status == "in_review" {
		return CmsContentDetail{}, errs.Conflict("in_review", "the content is in review; withdraw the review to edit it")
	}
	old, err := version(ctx, tx, cid, in.VersionNo)
	if err != nil {
		return CmsContentDetail{}, err
	}
	pol, err := Policy(ctx, tx, c.PropertyID)
	if err != nil {
		return CmsContentDetail{}, err
	}
	no, err := m.saveVersion(ctx, tx, c, old.Document, fmt.Sprintf("restored from version %d", in.VersionNo), "restore", &in.VersionNo, pol)
	if err != nil {
		return CmsContentDetail{}, err
	}
	if err := m.logEvent(ctx, tx, c, "restored", no, fmt.Sprintf("from version %d", in.VersionNo)); err != nil {
		return CmsContentDetail{}, err
	}
	out, err := m.detail(ctx, tx, kind, cid)
	if err != nil {
		return out, err
	}
	return out, m.audit(ctx, tx, out.CmsContent, "restore_version", map[string]any{"version": c.LatestVersion}, map[string]any{"version": no,
		"restoredFrom": in.VersionNo}, in.Reason, nil)
}

// Rollback republishes a version that was live before (already approved
// then), as a new version.
func (m *Module) Rollback(ctx context.Context, tx pgx.Tx, kind string, cid uuid.UUID, in CmsVersionInput) (CmsContentDetail, error) {
	c, err := get(ctx, tx, kind, cid, true)
	if err != nil {
		return CmsContentDetail{}, err
	}
	if c.Status == "in_review" {
		return CmsContentDetail{}, errs.Conflict("in_review", "the content is in review; withdraw the review first")
	}
	if err := handle.Required("reason", in.Reason); err != nil {
		return CmsContentDetail{}, err
	}
	old, err := version(ctx, tx, cid, in.VersionNo)
	if err != nil {
		return CmsContentDetail{}, err
	}
	if old.PublishedAt == nil {
		return CmsContentDetail{}, errs.Conflict("never_published", fmt.Sprintf("version %d was never live; restore it and submit it for review", in.VersionNo))
	}
	pol, err := Policy(ctx, tx, c.PropertyID)
	if err != nil {
		return CmsContentDetail{}, err
	}
	no, err := m.saveVersion(ctx, tx, c, old.Document, fmt.Sprintf("rollback to version %d: %s", in.VersionNo, in.Reason), "rollback", &in.VersionNo, pol)
	if err != nil {
		return CmsContentDetail{}, err
	}
	if err := m.logEvent(ctx, tx, c, "rolled_back", no, fmt.Sprintf("to version %d: %s", in.VersionNo, in.Reason)); err != nil {
		return CmsContentDetail{}, err
	}
	if err := m.publishNow(ctx, tx, cid, no, m.now()); err != nil {
		return CmsContentDetail{}, err
	}
	out, err := m.detail(ctx, tx, kind, cid)
	if err != nil {
		return out, err
	}
	return out, m.audit(ctx, tx, out.CmsContent, "rollback", map[string]any{"publishedVersion": c.PublishedVersion},
		map[string]any{"publishedVersion": no, "rolledBackTo": in.VersionNo}, in.Reason, nil)
}

// Delete removes a never-published item, or archives a published one.
func (m *Module) Delete(ctx context.Context, tx pgx.Tx, kind string, cid uuid.UUID) error {
	c, err := get(ctx, tx, kind, cid, true)
	if err != nil {
		return err
	}
	if c.Status == "in_review" {
		return errs.Conflict("in_review", "withdraw the review before deleting")
	}
	var children bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cms.contents WHERE parent_id = $1 AND archived_at IS NULL)`, cid).Scan(&children); err != nil {
		return err
	}
	if children {
		return errs.Conflict("in_use", "the page has sub-pages; move or delete them first")
	}
	if c.PublishedVersion == nil {
		if _, err := tx.Exec(ctx, `UPDATE cms.contents SET parent_id = NULL WHERE parent_id = $1`, cid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM cms.contents WHERE id = $1`, cid); err != nil {
			if dbtx.IsForeignKeyViolation(err) {
				return errs.Conflict("in_use", "the content is referenced; unpublish it instead")
			}
			return err
		}
		return m.audit(ctx, tx, c, audit.ActionDelete, c, nil, "", nil)
	}
	if c.Live {
		if err := m.takeDown(ctx, tx, c, "archived", m.now()); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE cms.contents SET archived_at = now(), status = 'unpublished', live = false, publish_at = NULL, unpublish_at = NULL,
		updated_by = $2 WHERE id = $1`, cid, actorID(ctx)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM cms.content_slugs WHERE content_id = $1`, cid); err != nil {
		return err
	}
	if err := m.logEvent(ctx, tx, c, "archived", c.LatestVersion, ""); err != nil {
		return err
	}
	return m.audit(ctx, tx, c, audit.ActionArchive, c, nil, "", nil)
}

// ── scheduler (FR-CMS-06 Scheduled, §9.5 automatic take-down) ─────────────

// ScheduleResult reports a scheduler run.
type ScheduleResult struct {
	Published   int `json:"published"`
	Unpublished int `json:"unpublished"`
}

// RunSchedule publishes scheduled content whose go-live has come and takes
// down live content whose take-down time has passed (all properties).
func (m *Module) RunSchedule(ctx context.Context, now time.Time) (ScheduleResult, error) {
	ctx = dbtx.System(ctx)
	var res ScheduleResult
	type due struct {
		id      uuid.UUID
		kind    string
		publish bool
	}
	var list []due
	err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, kind, true FROM cms.contents WHERE archived_at IS NULL AND approved_version IS NOT NULL AND publish_at <= $1
			AND (status = 'scheduled' OR (status = 'in_review' AND status_before_review = 'scheduled'))
			UNION ALL
			SELECT id, kind, false FROM cms.contents WHERE archived_at IS NULL AND live AND unpublish_at <= $1 ORDER BY 3 DESC`, now)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d due
			if err := rows.Scan(&d.id, &d.kind, &d.publish); err != nil {
				return err
			}
			list = append(list, d)
		}
		return rows.Err()
	})
	if err != nil {
		return res, err
	}
	for _, d := range list {
		err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
			c, err := get(ctx, tx, d.kind, d.id, true)
			if err != nil {
				return err
			}
			if d.publish {
				if c.ApprovedVersion == nil || c.PublishAt == nil || c.PublishAt.After(now) {
					return nil
				}
				if err := m.publishNow(ctx, tx, c.ID, *c.ApprovedVersion, now); err != nil {
					return err
				}
				res.Published++
				return m.notifyLive(ctx, tx, c, *c.ApprovedVersion)
			}
			if !c.Live || c.UnpublishAt == nil || c.UnpublishAt.After(now) {
				return nil
			}
			res.Unpublished++
			return m.takeDown(ctx, tx, c, "take-down time reached", now)
		})
		if err != nil {
			return res, fmt.Errorf("cms schedule %s: %w", d.id, err)
		}
	}
	return res, nil
}

// notifyLive tells the author that scheduled content went live.
func (m *Module) notifyLive(ctx context.Context, tx pgx.Tx, c CmsContent, v int) error {
	if m.Notify == nil {
		return nil
	}
	var author *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT created_by FROM cms.content_versions WHERE content_id = $1 AND version_no = $2`, c.ID, v).Scan(&author); err != nil {
		return err
	}
	if author == nil {
		return nil
	}
	path := ""
	if paths := m.pathsOf(ctx, tx, c, v); len(paths) > 0 {
		path = paths[0]
	}
	return m.Notify.Send(ctx, tx, notify.Message{Event: "cms.content_published", Category: "marketing", UserIDs: []uuid.UUID{*author},
		PropertyID: &c.PropertyID, Data: map[string]any{"title": c.Title, "kindLabel": specOf(c.Kind).Name, "version": v, "path": path}})
}

func (m *Module) pathsOf(ctx context.Context, tx pgx.Tx, c CmsContent, v int) []string {
	ver, err := version(ctx, tx, c.ID, v)
	if err != nil {
		return nil
	}
	return m.paths(c, ver.Document)
}

// ScheduleArgs is the River job of the CMS scheduler.
type ScheduleArgs struct{}

func (ScheduleArgs) Kind() string { return "cms_publish_schedule" }

func (ScheduleArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 1}
}

// ScheduleWorker runs the scheduler.
type ScheduleWorker struct {
	river.WorkerDefaults[ScheduleArgs]
	M *Module
}

func (w *ScheduleWorker) Work(ctx context.Context, _ *river.Job[ScheduleArgs]) error {
	res, err := w.M.RunSchedule(ctx, time.Now().UTC())
	if err == nil && res.Published+res.Unpublished > 0 {
		slog.InfoContext(ctx, "cms schedule", "published", res.Published, "unpublished", res.Unpublished)
	}
	return err
}

// RegisterJobs adds the scheduler (every minute).
func (m *Module) RegisterJobs(reg *jobs.Registrar) {
	river.AddWorker(reg.Workers, &ScheduleWorker{M: m})
	reg.Periodic = append(reg.Periodic, river.NewPeriodicJob(river.PeriodicInterval(time.Minute),
		func() (river.JobArgs, *river.InsertOpts) { return ScheduleArgs{}, nil }, nil))
}
