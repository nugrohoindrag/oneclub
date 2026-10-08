package approval

// Comments and attachments on approval requests (HRIS improvement phase C,
// docs/HRIS_Product_Requirements_UI_Backend_Audit.md §27): the requester,
// the approvers and Approvals view-all users comment on a request — with an
// optional photo or PDF (supporting document, quotation, receipt) — at any
// status. The other party is notified: the requester when an approver
// comments, the approvers of the current step when the requester does.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/notify"
)

// RequestComment is a comment on an approval request.
type RequestComment struct {
	ID         uuid.UUID  `json:"id"`
	AuthorName string     `json:"authorName"`
	Body       string     `json:"body"`
	FileID     *uuid.UUID `json:"fileId"`
	FileName   *string    `json:"fileName"`
	CreatedAt  time.Time  `json:"createdAt"`
}

// CommentRequest adds a comment (JSON, or multipart with body and file).
type CommentRequest struct {
	Body string `json:"body"`
}

const (
	commentMaxBytes = 10 << 20
	commentMaxBody  = 4000
)

var commentFileTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true, "application/pdf": true}

func (h *HTTP) registerComments(add func(route.Route)) {
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/approvals/{id}/comments",
		Summary: "Comment on a request, optionally with an attachment (JSON {body}, or multipart: body, file — JPEG, PNG, WebP or PDF up to 10 MB)",
		Request: CommentRequest{}, Response: RequestComment{}, Handler: h.addComment})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/approvals/{id}/comments/{commentId}/file", Summary: "Download the attachment of a comment",
		RawContent: "application/octet-stream", Handler: h.commentFile})
}

// comments lists the comments of a request, oldest first.
func comments(ctx context.Context, tx pgx.Tx, rid uuid.UUID) ([]RequestComment, error) {
	rows, err := tx.Query(ctx, `SELECT c.id, u.full_name, c.body, c.file_id, f.filename, c.created_at FROM platform.approval_request_comments c
		JOIN platform.users u ON u.id = c.author_id LEFT JOIN platform.files f ON f.id = c.file_id WHERE c.request_id = $1 ORDER BY c.created_at, c.id`, rid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RequestComment{}
	for rows.Next() {
		var c RequestComment
		if err := rows.Scan(&c.ID, &c.AuthorName, &c.Body, &c.FileID, &c.FileName, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// readComment reads the comment text and the optional file of the request.
func readComment(w http.ResponseWriter, r *http.Request) (string, []byte, string, string, error) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		var req CommentRequest
		if err := httpx.Decode(r, &req); err != nil {
			return "", nil, "", "", err
		}
		return strings.TrimSpace(req.Body), nil, "", "", nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, commentMaxBytes+256<<10)
	if err := r.ParseMultipartForm(commentMaxBytes); err != nil { //nolint:gosec // G120: body capped by MaxBytesReader above
		return "", nil, "", "", errs.BadRequest("invalid_upload", "upload must be multipart/form-data up to 10 MB")
	}
	body := strings.TrimSpace(r.FormValue("body"))
	file, hdr, err := r.FormFile("file")
	if err != nil {
		return body, nil, "", "", nil //nolint:nilerr // the attachment is optional
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, commentMaxBytes+1))
	if err != nil || len(data) == 0 || len(data) > commentMaxBytes {
		return "", nil, "", "", errs.Validation("file_size", "file too large or empty", errs.Field("file", "size", "up to 10 MB"))
	}
	ctype := http.DetectContentType(data)
	if i := strings.IndexByte(ctype, ';'); i >= 0 {
		ctype = ctype[:i]
	}
	if !commentFileTypes[ctype] {
		return "", nil, "", "", errs.Validation("file_type", "unsupported file type", errs.Field("file", "type", "JPEG, PNG, WebP or PDF"))
	}
	name := strings.TrimSpace(hdr.Filename)
	if name == "" {
		name = "attachment"
	}
	return body, data, name, ctype, nil
}

func (h *HTTP) addComment(w http.ResponseWriter, r *http.Request) {
	rid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, data, name, ctype, err := readComment(w, r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if body == "" {
		httpx.WriteError(w, r, errs.Validation("body_required", "write a comment", errs.Field("body", "required", "is required")))
		return
	}
	if len([]rune(body)) > commentMaxBody {
		httpx.WriteError(w, r, errs.Validation("body_too_long", "comment too long", errs.Field("body", "max", "at most 4000 characters")))
		return
	}
	if data != nil && h.Files == nil {
		httpx.WriteError(w, r, errs.BadRequest("attachments_unavailable", "attachments are not available"))
		return
	}
	p := authz.From(r.Context())
	ctx := dbtx.WithScope(r.Context(), dbtx.Scope{AllProperties: true, UserID: p.UserID})
	var out RequestComment
	err = h.E.DB.WithTx(ctx, func(tx pgx.Tx) error {
		x, err := h.loadDetail(r.WithContext(ctx), tx, rid) // visible to the requester, approvers, involved and view-all only
		if err != nil {
			return err
		}
		var fid *uuid.UUID
		if data != nil {
			f, err := h.Files.Save(ctx, tx, name, ctype, "attachment", false, bytes.NewReader(data), int64(len(data)))
			if err != nil {
				return err
			}
			fid = &f.ID
		}
		cid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO platform.approval_request_comments (id, request_id, property_id, author_id, body, file_id)
			VALUES ($1,$2,$3,$4,$5,$6)`, cid, rid, x.PropertyID, p.UserID, body, fid); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "approval_commented", EntityType: "platform.approval_request",
			EntityID: rid.String(), EntityLabel: x.Title, PropertyID: &x.PropertyID, Reason: body,
			Metadata: map[string]any{"commentId": cid.String(), "attachment": fid != nil}}); err != nil {
			return err
		}
		cs, err := comments(ctx, tx, rid)
		if err != nil {
			return err
		}
		for _, c := range cs {
			if c.ID == cid {
				out = c
			}
		}
		return h.notifyComment(ctx, tx, x, p.UserID, body)
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

// notifyComment tells the other party: the requester, or the approvers of
// the current step when the requester comments.
func (h *HTTP) notifyComment(ctx context.Context, tx pgx.Tx, x Request, author uuid.UUID, body string) error {
	var users []uuid.UUID
	if author != x.RequestedBy {
		users = []uuid.UUID{x.RequestedBy}
	} else if x.Status == StatusPending && x.CurrentStepNo != nil {
		us, err := approversFor(ctx, tx, x.ID, *x.CurrentStepNo)
		if err != nil {
			return err
		}
		users = us
	}
	if len(users) == 0 || h.E.Notify == nil {
		return nil
	}
	var name string
	_ = tx.QueryRow(ctx, `SELECT full_name FROM platform.users WHERE id = $1`, author).Scan(&name)
	if len([]rune(body)) > 300 {
		body = string([]rune(body)[:300]) + "…"
	}
	return h.E.Notify.Send(ctx, tx, notify.Message{Event: "approval.comment_added", Category: "approval", UserIDs: users, Link: h.E.link(x.ID),
		PropertyID: &x.PropertyID, Data: map[string]any{"title": x.Title, "documentType": x.DocumentName, "documentRef": x.DocumentRef,
			"authorName": name, "comment": body}})
}

func (h *HTTP) commentFile(w http.ResponseWriter, r *http.Request) {
	rid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	cid, err := httpx.PathUUID(r, "commentId")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := dbtx.WithScope(r.Context(), dbtx.Scope{AllProperties: true, UserID: authz.From(r.Context()).UserID})
	var fid *uuid.UUID
	err = h.E.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		if _, err := h.loadDetail(r.WithContext(ctx), tx, rid); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `SELECT file_id FROM platform.approval_request_comments WHERE id = $1 AND request_id = $2`, cid, rid).Scan(&fid)
		if dbtx.IsNoRows(err) || (err == nil && fid == nil) {
			return errs.NotFound("attachment")
		}
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rc, name, err := h.Files.Open(r.Context(), *fid)
	if err != nil {
		httpx.WriteError(w, r, errs.NotFound("attachment"))
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(name, `"`, "")+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, rc)
}
