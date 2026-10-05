package procurement

// PRD P4 FR-GR-01: the delivery note (surat jalan) photo or document of a
// goods receipt. The warehouse uploads the file first (Back Office form or
// the ops Goods Receipt screen, camera capture), then sends its id in
// attachmentFileIds of the receipt; the files are private and served to the
// holders of procurement.goods_receipt.view through the receipt only.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/storage"
)

// deliveryNoteMaxBytes caps a delivery note photo / document.
const deliveryNoteMaxBytes = 10 << 20

// deliveryNoteTypes are the accepted delivery note files (camera photos, scans, PDF).
var deliveryNoteTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true, "application/pdf": true}

// RegisterGoodsReceiptAttachments adds the delivery note upload and the
// download of a receipt's attachments (wired by internal/app with the
// platform file store).
func (m *Module) RegisterGoodsReceiptAttachments(reg *route.Registry, files *storage.Files) {
	add := func(rt route.Route) {
		rt.Module, rt.Tag, rt.Scope = "procurement", "Goods Receipts", route.ScopeProperty
		reg.Add(rt)
	}
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/procurement/delivery-note-files",
		Summary:    "Upload a delivery note photo or document (multipart: file; JPEG, PNG, WebP or PDF up to 10 MB) for attachmentFileIds of a goods receipt",
		Permission: "procurement.goods_receipt.create", Response: storage.File{}, Handler: m.uploadDeliveryNote(files)})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/procurement/goods-receipts/{id}/attachments/{fileId}",
		Summary: "Download a delivery note photo / document of a goods receipt", Permission: "procurement.goods_receipt.view",
		RawContent: "application/octet-stream", Handler: m.downloadDeliveryNote(files)})
}

func (m *Module) uploadDeliveryNote(files *storage.Files) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, deliveryNoteMaxBytes+256<<10)
		if err := r.ParseMultipartForm(deliveryNoteMaxBytes); err != nil { //nolint:gosec // G120: body capped by MaxBytesReader above
			httpx.WriteError(w, r, errs.BadRequest("invalid_upload", "upload must be multipart/form-data up to 10 MB"))
			return
		}
		file, hdr, err := r.FormFile("file")
		if err != nil {
			httpx.WriteError(w, r, errs.Validation("file_required", "file is required", errs.Field("file", "required", "choose a photo or document")))
			return
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, deliveryNoteMaxBytes+1))
		if err != nil || len(data) == 0 || len(data) > deliveryNoteMaxBytes {
			httpx.WriteError(w, r, errs.Validation("file_size", "file too large or empty", errs.Field("file", "size", "up to 10 MB")))
			return
		}
		ctype := http.DetectContentType(data)
		if i := strings.IndexByte(ctype, ';'); i >= 0 {
			ctype = ctype[:i]
		}
		if !deliveryNoteTypes[ctype] {
			httpx.WriteError(w, r, errs.Validation("file_type", "unsupported file type", errs.Field("file", "type", "JPEG, PNG, WebP photo or PDF")))
			return
		}
		name := strings.TrimSpace(hdr.Filename)
		if name == "" {
			name = "delivery-note"
		}
		ctx := r.Context()
		pid := handle.Property(ctx)
		var out storage.File
		err = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
			out, err = files.Save(ctx, tx, name, ctype, "attachment", false, bytes.NewReader(data), int64(len(data)))
			if err != nil {
				return err
			}
			return audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: audit.ActionCreate, EntityType: "platform.file",
				EntityID: out.ID.String(), EntityLabel: "Delivery note " + name, PropertyID: &pid, After: out})
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, out)
	}
}

func (m *Module) downloadDeliveryNote(files *storage.Files) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		gid, err := httpx.PathUUID(r, "id")
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		fid, err := httpx.PathUUID(r, "fileId")
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		ctx := r.Context()
		err = m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
			var ids []uuid.UUID
			var ctype string
			if err := tx.QueryRow(ctx, `SELECT g.attachment_file_ids, coalesce(f.content_type, '') FROM procurement.goods_receipts g
				LEFT JOIN platform.files f ON f.id = $3 WHERE g.id = $1 AND g.property_id = $2`, gid, handle.Property(ctx), fid).Scan(&ids, &ctype); err != nil {
				return errs.NotFound("goods receipt")
			}
			if !slices.Contains(ids, fid) || ctype == "" {
				return errs.NotFound("delivery note file")
			}
			return streamFile(ctx, w, files, fid, ctype)
		})
		if err != nil {
			httpx.WriteError(w, r, err)
		}
	}
}

func streamFile(ctx context.Context, w http.ResponseWriter, files *storage.Files, fid uuid.UUID, ctype string) error {
	rc, name, err := files.Open(ctx, fid)
	if err != nil {
		return errs.NotFound("delivery note file")
	}
	defer rc.Close()
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", `inline; filename="`+strings.ReplaceAll(name, `"`, "")+`"`)
	_, err = io.Copy(w, rc)
	return err
}
