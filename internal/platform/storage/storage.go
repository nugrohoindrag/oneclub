// Package storage stores files (branding images, exports, imports) in
// S3-compatible object storage, or on the local filesystem in development
// (Technical Doc §2; Open Question #3 decides managed vs self-hosted S3).
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
)

// Blob is the object storage driver.
type Blob interface {
	Put(ctx context.Context, key, contentType string, r io.Reader, size int64) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// New returns the configured driver.
func New(cfg config.Storage, instance string) (Blob, error) {
	switch cfg.Driver {
	case "s3":
		cl, err := minio.New(cfg.Endpoint, &minio.Options{
			Creds: credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""), Secure: cfg.UseSSL, Region: cfg.Region,
		})
		if err != nil {
			return nil, err
		}
		return &s3Blob{cl: cl, bucket: cfg.Bucket, prefix: instance + "/"}, nil
	default:
		dir := filepath.Join(cfg.Dir, instance)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, err
		}
		return &fsBlob{dir: dir}, nil
	}
}

type fsBlob struct{ dir string }

func (b *fsBlob) path(key string) (string, error) {
	clean := filepath.Clean("/" + key)
	if strings.Contains(clean, "..") {
		return "", errors.New("storage: invalid key")
	}
	return filepath.Join(b.dir, filepath.FromSlash(clean)), nil
}

func (b *fsBlob) Put(_ context.Context, key, _ string, r io.Reader, _ int64) error {
	p, err := b.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (b *fsBlob) Get(_ context.Context, key string) (io.ReadCloser, error) {
	p, err := b.path(key)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

type s3Blob struct {
	cl     *minio.Client
	bucket string
	prefix string
}

func (b *s3Blob) Put(ctx context.Context, key, contentType string, r io.Reader, size int64) error {
	_, err := b.cl.PutObject(ctx, b.bucket, b.prefix+key, r, size, minio.PutObjectOptions{ContentType: contentType})
	return err
}

func (b *s3Blob) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return b.cl.GetObject(ctx, b.bucket, b.prefix+key, minio.GetObjectOptions{})
}

// Files manages platform.files records and the HTTP endpoints.
type Files struct {
	DB   *dbtx.DB
	Blob Blob
}

// File is a stored file record.
type File struct {
	ID          uuid.UUID `json:"id"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"contentType"`
	SizeBytes   int64     `json:"sizeBytes"`
	Purpose     string    `json:"purpose" enum:"branding,export,import,attachment"`
	Public      bool      `json:"public"`
	URL         string    `json:"url"`
	CreatedAt   time.Time `json:"createdAt"`
}

// URLFor is the download URL of a file.
func URLFor(fid uuid.UUID) string { return "/api/v1/files/" + fid.String() }

// Save stores content and inserts the file record within tx.
func (f *Files) Save(ctx context.Context, tx pgx.Tx, filename, contentType, purpose string, public bool, r io.Reader, size int64) (File, error) {
	fid := id.New()
	ext := strings.ToLower(filepath.Ext(filename))
	key := fmt.Sprintf("%s/%s/%s%s", purpose, time.Now().UTC().Format("2006/01"), fid, ext)
	if err := f.Blob.Put(ctx, key, contentType, r, size); err != nil {
		return File{}, fmt.Errorf("storage put: %w", err)
	}
	var uid *uuid.UUID
	if p := authz.From(ctx); p != nil {
		uid = id.Ptr(p.UserID)
	}
	out := File{ID: fid, Filename: filename, ContentType: contentType, SizeBytes: size, Purpose: purpose, Public: public, URL: URLFor(fid), CreatedAt: time.Now().UTC()}
	_, err := tx.Exec(ctx, `INSERT INTO platform.files (id, storage_key, filename, content_type, size_bytes, purpose, public, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, fid, key, filename, contentType, size, purpose, public, uid)
	return out, err
}

var allowedBranding = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true, "image/svg+xml": true, "image/x-icon": true, "image/vnd.microsoft.icon": true}

// upload accepts branding images (logo, favicon, login photo).
func (f *Files) upload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(5 << 20); err != nil {
		httpx.WriteError(w, r, errs.BadRequest("invalid_upload", "upload must be multipart/form-data up to 5 MB"))
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		httpx.WriteError(w, r, errs.Validation("file_required", "file is required", errs.Field("file", "required", "choose a file")))
		return
	}
	defer file.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(file, head)
	ctype := http.DetectContentType(head[:n])
	if strings.HasSuffix(strings.ToLower(hdr.Filename), ".svg") {
		ctype = "image/svg+xml"
	}
	if !allowedBranding[ctype] {
		httpx.WriteError(w, r, errs.Validation("file_type", "unsupported file type", errs.Field("file", "type", "PNG, JPEG, WebP, SVG or ICO only")))
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	var out File
	err = f.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = f.Save(ctx, tx, hdr.Filename, ctype, "branding", true, file, hdr.Size)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionCreate, EntityType: "platform.file",
			EntityID: out.ID.String(), EntityLabel: hdr.Filename, After: out})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

// download serves a file. Public files (branding) need no session; private
// files (exports) only to their creator.
func (f *Files) download(w http.ResponseWriter, r *http.Request) {
	fid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	var key, name, ctype string
	var public bool
	var createdBy *uuid.UUID
	err = f.DB.Primary.QueryRow(ctx, `SELECT storage_key, filename, content_type, public, created_by FROM platform.files WHERE id = $1`, fid).
		Scan(&key, &name, &ctype, &public, &createdBy)
	if dbtx.IsNoRows(err) {
		httpx.WriteError(w, r, errs.NotFound("file"))
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !public {
		p := authz.From(ctx)
		if p == nil {
			httpx.WriteError(w, r, errs.Unauthorized("authentication required"))
			return
		}
		if createdBy == nil || *createdBy != p.UserID {
			httpx.WriteError(w, r, errs.NotFound("file"))
			return
		}
	}
	rc, err := f.Blob.Get(ctx, key)
	if err != nil {
		httpx.WriteError(w, r, errs.NotFound("file"))
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", ctype)
	if public {
		w.Header().Set("Cache-Control", "public, max-age=300")
	} else {
		w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(name, `"`, "")+`"`)
	}
	_, _ = io.Copy(w, rc)
}

// Register adds file routes.
func (f *Files) Register(reg *route.Registry) {
	reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/files", Module: "platform", Tag: "Branding",
		Summary: "Upload a branding image (multipart: file)", Permission: "platform.branding.update", Response: File{}, Handler: f.upload})
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/files/{id}", Module: "platform", Tag: "Branding",
		Summary: "Download a file (public branding files need no session)", Auth: route.AuthPublic, RawContent: "application/octet-stream", Handler: f.download})
}
