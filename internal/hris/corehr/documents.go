package corehr

// Employee documents (FR-CTR-04): KTP, NPWP, diplomas, BPJS cards,
// certificates, warning letters (SP) … with validity, expiry reminders and
// restricted access: files are private and only streamed through HRIS to
// holders of hris.employee_document.view; confidential documents (warning
// letters, medical) also need hris.employee_document.view_confidential, and
// document numbers are masked without hris.employee.view_sensitive.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/storage"
)

// DocumentTypes of employee documents.
var DocumentTypes = []string{"ktp", "kk", "npwp", "passport", "ijazah", "cv", "bpjs_kesehatan", "bpjs_ketenagakerjaan", "certificate", "contract",
	"warning_letter", "medical", "reference_letter", "photo", "other"}

// EmployeeDocument is a document of an employee.
type EmployeeDocument struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	PropertyID   uuid.UUID  `json:"propertyId" db:"property_id"`
	EmployeeID   uuid.UUID  `json:"employeeId" db:"employee_id"`
	EmployeeNo   string     `json:"employeeNo" db:"employee_no"`
	EmployeeName string     `json:"employeeName" db:"employee_name"`
	DocumentType string     `json:"documentType" db:"document_type"`
	Title        string     `json:"title" db:"title"`
	DocumentNo   *string    `json:"documentNo" db:"document_no"`
	IssuedOn     *time.Time `json:"issuedOn" db:"issued_on"`
	ExpiresOn    *time.Time `json:"expiresOn" db:"expires_on"`
	WarningLevel *int       `json:"warningLevel" db:"warning_level"`
	FileID       *uuid.UUID `json:"fileId" db:"file_id"`
	FileName     *string    `json:"fileName" db:"file_name"`
	Confidential bool       `json:"confidential" db:"confidential"`
	Status       string     `json:"status" db:"status" enum:"active,superseded"`
	Validity     string     `json:"validity" db:"validity" enum:"valid,expiring,expired,no_expiry"`
	Notes        *string    `json:"notes" db:"notes"`
	CreatedAt    time.Time  `json:"createdAt" db:"created_at"`
	UpdatedAt    time.Time  `json:"updatedAt" db:"updated_at"`
}

const documentSelect = `SELECT d.id, d.property_id, d.employee_id, e.employee_no, e.full_name AS employee_name, d.document_type, d.title, d.document_no,
	d.issued_on, d.expires_on, d.warning_level, d.file_id, f.filename AS file_name, d.confidential, d.status,
	CASE WHEN d.expires_on IS NULL THEN 'no_expiry' WHEN d.expires_on < $1::date THEN 'expired' WHEN d.expires_on <= $1::date + 30 THEN 'expiring'
	  ELSE 'valid' END AS validity, d.notes, d.created_at, d.updated_at
	FROM hris.employee_documents d JOIN hris.employees e ON e.id = d.employee_id LEFT JOIN platform.files f ON f.id = d.file_id`

// DocumentRequest adds a document to an employee.
type DocumentRequest struct {
	DocumentType string     `json:"documentType" enum:"ktp,kk,npwp,passport,ijazah,cv,bpjs_kesehatan,bpjs_ketenagakerjaan,certificate,contract,warning_letter,medical,reference_letter,photo,other"`
	Title        string     `json:"title,omitempty" doc:"Default: the document type"`
	DocumentNo   string     `json:"documentNo,omitempty"`
	IssuedOn     string     `json:"issuedOn,omitempty"`
	ExpiresOn    string     `json:"expiresOn,omitempty"`
	WarningLevel *int       `json:"warningLevel,omitempty" doc:"1–3 for a warning letter (SP1–SP3)"`
	FileID       *uuid.UUID `json:"fileId,omitempty" doc:"Uploaded with POST /api/v1/hris/document-files"`
	Confidential *bool      `json:"confidential,omitempty" doc:"Default: true for warning letters and medical documents"`
	Notes        string     `json:"notes,omitempty"`
}

// DocumentUpdate edits a document.
type DocumentUpdate struct {
	Title        *string    `json:"title,omitempty"`
	DocumentNo   *string    `json:"documentNo,omitempty"`
	IssuedOn     *string    `json:"issuedOn,omitempty"`
	ExpiresOn    *string    `json:"expiresOn,omitempty"`
	FileID       *uuid.UUID `json:"fileId,omitempty"`
	Confidential *bool      `json:"confidential,omitempty"`
	Status       *string    `json:"status,omitempty" enum:"active,superseded"`
	Notes        *string    `json:"notes,omitempty"`
}

const documentMaxBytes = 10 << 20

var documentFileTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true, "application/pdf": true}

func (m *Module) registerDocuments(reg *route.Registry) {
	tag := "HRIS Documents"
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/document-files",
		Summary:    "Upload an employee document, certificate or signed contract file (multipart: file; JPEG, PNG, WebP or PDF up to 10 MB)",
		Permission: "hris.employee_document.manage", Response: storage.File{}, Handler: m.uploadHTTP})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/employee-documents", Summary: "Employee documents (expiry follow-up)",
		Permission: "hris.employee_document.view", Response: EmployeeDocument{}, List: true,
		Query:   []route.Param{{Name: "employeeId"}, {Name: "documentType"}, {Name: "expiringWithin", Type: "integer", Description: "Expired or expiring within N days"}},
		Handler: listRead(m.DB, m.listDocumentsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/employees/{id}/documents", Summary: "Add an employee document",
		Permission: "hris.employee_document.manage", Request: DocumentRequest{}, Response: EmployeeDocument{}, Idempotent: true,
		Handler: handle.Write(m.DB, http.StatusCreated, m.createDocumentHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPatch, Path: "/api/v1/hris/employee-documents/{id}", Summary: "Edit an employee document",
		Permission: "hris.employee_document.manage", Request: DocumentUpdate{}, Response: EmployeeDocument{}, Handler: handle.Write(m.DB, http.StatusOK, m.updateDocumentHTTP)})
	add(reg, tag, route.Route{Method: http.MethodDelete, Path: "/api/v1/hris/employee-documents/{id}", Summary: "Archive an employee document",
		Permission: "hris.employee_document.manage", Handler: handle.Write(m.DB, http.StatusNoContent, m.archiveDocumentHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/employee-documents/{id}/file", Summary: "Download an employee document file",
		Permission: "hris.employee_document.view", RawContent: "application/octet-stream", Handler: m.documentFileHTTP})
	add(reg, "HRIS Training & Certification", route.Route{Method: http.MethodGet, Path: "/api/v1/hris/certifications/{id}/file",
		Summary: "Download a certificate file", Permission: "hris.certification.view", RawContent: "application/octet-stream", Handler: m.certificationFileHTTP})
	add(reg, "HRIS Contracts", route.Route{Method: http.MethodGet, Path: "/api/v1/hris/contracts/{id}/file", Summary: "Download the signed contract",
		Permission: "hris.contract.view", RawContent: "application/octet-stream", Handler: m.contractFileHTTP})
}

func (m *Module) uploadHTTP(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, documentMaxBytes+256<<10)
	if err := r.ParseMultipartForm(documentMaxBytes); err != nil { //nolint:gosec // G120: body capped by MaxBytesReader above
		httpx.WriteError(w, r, errs.BadRequest("invalid_upload", "upload must be multipart/form-data up to 10 MB"))
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		httpx.WriteError(w, r, errs.Validation("file_required", "file is required", errs.Field("file", "required", "choose a photo or PDF")))
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, documentMaxBytes+1))
	if err != nil || len(data) == 0 || len(data) > documentMaxBytes {
		httpx.WriteError(w, r, errs.Validation("file_size", "file too large or empty", errs.Field("file", "size", "up to 10 MB")))
		return
	}
	ctype := http.DetectContentType(data)
	if i := strings.IndexByte(ctype, ';'); i >= 0 {
		ctype = ctype[:i]
	}
	if !documentFileTypes[ctype] {
		httpx.WriteError(w, r, errs.Validation("file_type", "unsupported file type", errs.Field("file", "type", "JPEG, PNG, WebP or PDF")))
		return
	}
	name := strings.TrimSpace(hdr.Filename)
	if name == "" {
		name = "document"
	}
	ctx := r.Context()
	pid := handle.Property(ctx)
	var out storage.File
	err = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		out, err = m.Files.Save(ctx, tx, name, ctype, "attachment", false, bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "platform.file",
			EntityID: out.ID.String(), EntityLabel: "HR document " + name, PropertyID: &pid, After: out})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

// maskDocuments hides confidential documents and masks numbers.
func maskDocuments(ctx context.Context, list []EmployeeDocument) []EmployeeDocument {
	out := []EmployeeDocument{}
	for _, d := range list {
		if d.Confidential && !can(ctx, "hris.employee_document.view_confidential", d.PropertyID) {
			continue
		}
		if d.DocumentNo != nil && !can(ctx, "hris.employee.view_sensitive", d.PropertyID) {
			s := MaskTail(*d.DocumentNo)
			d.DocumentNo = &s
		}
		out = append(out, d)
	}
	return out
}

func (m *Module) listDocumentsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]EmployeeDocument, error) {
	emp, err := handle.QueryUUID(r, "employeeId")
	if err != nil {
		return nil, err
	}
	property := handle.Property(ctx)
	within := handle.QueryInt(r, "expiringWithin", -1)
	list, err := handle.List[EmployeeDocument](tx.Query(ctx, documentSelect+` WHERE d.property_id = $2 AND d.archived_at IS NULL
		AND ($3::uuid IS NULL OR d.employee_id = $3) AND ($4 = '' OR d.document_type = $4)
		AND ($5::int < 0 OR (d.expires_on IS NOT NULL AND d.status = 'active' AND d.expires_on <= $1::date + $5::int AND e.status = 'active'))
		ORDER BY CASE WHEN $5::int >= 0 THEN d.expires_on END, e.full_name, d.document_type, d.created_at DESC LIMIT 1000`,
		ymd(today(ctx, tx, property)), property, emp, r.URL.Query().Get("documentType"), within))
	return maskDocuments(ctx, list), err
}

func (m *Module) loadDocument(ctx context.Context, tx pgx.Tx, did uuid.UUID, property uuid.UUID) (EmployeeDocument, error) {
	return getOne[EmployeeDocument]("document")(tx.Query(ctx, documentSelect+` WHERE d.id = $2 AND d.property_id = $3 AND d.archived_at IS NULL`,
		ymd(today(ctx, tx, property)), did, property))
}

func (m *Module) viewDocument(ctx context.Context, tx pgx.Tx, did, property uuid.UUID) (EmployeeDocument, error) {
	d, err := m.loadDocument(ctx, tx, did, property)
	if err != nil {
		return d, err
	}
	list := maskDocuments(ctx, []EmployeeDocument{d})
	if len(list) == 0 {
		return d, errs.NotFound("document")
	}
	return list[0], nil
}

func documentAudit(d EmployeeDocument) map[string]any {
	return map[string]any{"employeeId": d.EmployeeID, "documentType": d.DocumentType, "title": d.Title, "issuedOn": datePtr(d.IssuedOn),
		"expiresOn": datePtr(d.ExpiresOn), "warningLevel": d.WarningLevel, "fileId": d.FileID, "confidential": d.Confidential, "status": d.Status}
}

// checkFile checks that an uploaded file exists.
func checkFile(ctx context.Context, tx pgx.Tx, field string, fid *uuid.UUID) error {
	if fid == nil {
		return nil
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.files WHERE id = $1)`, *fid).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return handle.Invalid(field, "not_found", "file not found")
	}
	return nil
}

func (m *Module) createDocumentHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req DocumentRequest) (EmployeeDocument, error) {
	eid, err := handle.ID(r)
	if err != nil {
		return EmployeeDocument{}, err
	}
	if err := m.inProperty(ctx, tx, eid); err != nil {
		return EmployeeDocument{}, err
	}
	did, err := m.AddDocument(ctx, tx, eid, req)
	if err != nil {
		return EmployeeDocument{}, err
	}
	property := handle.Property(ctx)
	d, err := m.loadDocument(ctx, tx, did, property)
	if err != nil {
		return d, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.employee_document", EntityID: did.String(),
		EntityLabel: d.EmployeeNo + " · " + d.Title, PropertyID: &property, After: documentAudit(d)}); err != nil {
		return d, err
	}
	return m.viewDocument(ctx, tx, did, property)
}

// AddDocument stores a document of an employee (also used by imports).
func (m *Module) AddDocument(ctx context.Context, tx pgx.Tx, eid uuid.UUID, req DocumentRequest) (uuid.UUID, error) {
	if !oneOf(DocumentTypes, req.DocumentType) {
		return uuid.Nil, enumErr("documentType", DocumentTypes)
	}
	issued, err := parseDate("issuedOn", req.IssuedOn)
	if err != nil {
		return uuid.Nil, err
	}
	expires, err := parseDate("expiresOn", req.ExpiresOn)
	if err != nil {
		return uuid.Nil, err
	}
	if issued != nil && expires != nil && expires.Before(*issued) {
		return uuid.Nil, handle.Invalid("expiresOn", "invalid", "must be on or after the issue date")
	}
	level := req.WarningLevel
	if req.DocumentType == "warning_letter" {
		if level == nil || *level < 1 || *level > 3 {
			return uuid.Nil, handle.Invalid("warningLevel", "required", "a warning letter is SP1, SP2 or SP3 (1–3)")
		}
	} else {
		level = nil
	}
	if err := checkFile(ctx, tx, "fileId", req.FileID); err != nil {
		return uuid.Nil, err
	}
	confidential := req.DocumentType == "warning_letter" || req.DocumentType == "medical"
	if req.Confidential != nil {
		confidential = *req.Confidential
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = documentTitle(req.DocumentType, level)
	}
	var property uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id FROM hris.employees WHERE id = $1`, eid).Scan(&property); err != nil {
		return uuid.Nil, errs.NotFound("employee")
	}
	did := id.New()
	var is, ex *string
	if issued != nil {
		s := ymd(*issued)
		is = &s
	}
	if expires != nil {
		s := ymd(*expires)
		ex = &s
	}
	if _, err := tx.Exec(ctx, `INSERT INTO hris.employee_documents (id, property_id, employee_id, document_type, title, document_no, issued_on, expires_on,
		warning_level, file_id, confidential, notes, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7::date,$8::date,$9,$10,$11,$12,$13,$13)`,
		did, property, eid, req.DocumentType, title, nullStr(req.DocumentNo), is, ex, level, req.FileID, confidential, nullStr(req.Notes), actor(ctx)); err != nil {
		return uuid.Nil, err
	}
	// a newer document of the same identity type supersedes the older one
	if !oneOf([]string{"warning_letter", "certificate", "other", "reference_letter", "medical", "contract"}, req.DocumentType) {
		if _, err := tx.Exec(ctx, `UPDATE hris.employee_documents SET status = 'superseded' WHERE employee_id = $1 AND document_type = $2 AND id <> $3
			AND status = 'active' AND archived_at IS NULL`, eid, req.DocumentType, did); err != nil {
			return uuid.Nil, err
		}
	}
	return did, nil
}

func documentTitle(t string, level *int) string {
	names := map[string]string{"ktp": "KTP", "kk": "Kartu Keluarga", "npwp": "NPWP", "passport": "Passport", "ijazah": "Ijazah", "cv": "CV",
		"bpjs_kesehatan": "BPJS Kesehatan", "bpjs_ketenagakerjaan": "BPJS Ketenagakerjaan", "certificate": "Certificate", "contract": "Contract",
		"medical": "Medical Certificate", "reference_letter": "Reference Letter", "photo": "Photo", "other": "Document"}
	if t == "warning_letter" && level != nil {
		return "SP" + itoa(*level)
	}
	return names[t]
}

func (m *Module) updateDocumentHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req DocumentUpdate) (EmployeeDocument, error) {
	did, err := handle.ID(r)
	if err != nil {
		return EmployeeDocument{}, err
	}
	property := handle.Property(ctx)
	before, err := m.viewDocument(ctx, tx, did, property)
	if err != nil {
		return before, err
	}
	var is, ex *string
	clearIssued, clearExpiry := false, false
	if req.IssuedOn != nil {
		t, err := parseDate("issuedOn", *req.IssuedOn)
		if err != nil {
			return before, err
		}
		if t == nil {
			clearIssued = true
		} else {
			s := ymd(*t)
			is = &s
		}
	}
	if req.ExpiresOn != nil {
		t, err := parseDate("expiresOn", *req.ExpiresOn)
		if err != nil {
			return before, err
		}
		if t == nil {
			clearExpiry = true
		} else {
			s := ymd(*t)
			ex = &s
		}
	}
	if req.Status != nil && !oneOf([]string{"active", "superseded"}, *req.Status) {
		return before, enumErr("status", []string{"active", "superseded"})
	}
	if req.DocumentNo != nil && strings.Contains(*req.DocumentNo, "*") {
		req.DocumentNo = nil // masked value sent back unchanged
	}
	if err := checkFile(ctx, tx, "fileId", req.FileID); err != nil {
		return before, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.employee_documents SET title = coalesce($2, title), document_no = coalesce($3, document_no),
		issued_on = CASE WHEN $4 THEN NULL ELSE coalesce($5::date, issued_on) END, expires_on = CASE WHEN $6 THEN NULL ELSE coalesce($7::date, expires_on) END,
		file_id = coalesce($8, file_id), confidential = coalesce($9, confidential), status = coalesce($10, status), notes = coalesce($11, notes),
		reminders_sent = CASE WHEN $7::date IS NOT NULL THEN '{}' ELSE reminders_sent END, updated_by = $12 WHERE id = $1`,
		did, req.Title, req.DocumentNo, clearIssued, is, clearExpiry, ex, req.FileID, req.Confidential, req.Status, req.Notes, actor(ctx)); err != nil {
		if strings.Contains(err.Error(), "check constraint") {
			return before, handle.Invalid("expiresOn", "invalid", "must be on or after the issue date")
		}
		return before, err
	}
	after, err := m.loadDocument(ctx, tx, did, property)
	if err != nil {
		return after, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionUpdate, EntityType: "hris.employee_document", EntityID: did.String(),
		EntityLabel: after.EmployeeNo + " · " + after.Title, PropertyID: &property, Before: documentAudit(before), After: documentAudit(after)}); err != nil {
		return after, err
	}
	return m.viewDocument(ctx, tx, did, property)
}

func (m *Module) archiveDocumentHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (handle.Empty, error) {
	did, err := handle.ID(r)
	if err != nil {
		return handle.Empty{}, err
	}
	property := handle.Property(ctx)
	before, err := m.viewDocument(ctx, tx, did, property)
	if err != nil {
		return handle.Empty{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.employee_documents SET archived_at = now(), updated_by = $2 WHERE id = $1`, did, actor(ctx)); err != nil {
		return handle.Empty{}, err
	}
	return handle.Empty{}, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionArchive, EntityType: "hris.employee_document",
		EntityID: did.String(), EntityLabel: before.EmployeeNo + " · " + before.Title, PropertyID: &property, Before: documentAudit(before)})
}

// streamFile writes a stored file inline.
func (m *Module) streamFile(ctx context.Context, w http.ResponseWriter, fid uuid.UUID) error {
	var ctype string
	if err := m.DB.Primary.QueryRow(ctx, `SELECT content_type FROM platform.files WHERE id = $1`, fid).Scan(&ctype); err != nil {
		return errs.NotFound("file")
	}
	rc, name, err := m.Files.Open(ctx, fid)
	if err != nil {
		return errs.NotFound("file")
	}
	defer rc.Close()
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Disposition", `inline; filename="`+strings.ReplaceAll(name, `"`, "")+`"`)
	_, err = io.Copy(w, rc)
	return err
}

// fileOf runs a lookup of the file id of a record and streams it.
func (m *Module) fileOf(w http.ResponseWriter, r *http.Request, lookup func(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (*uuid.UUID, error)) {
	rid, err := handle.ID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	var fid *uuid.UUID
	err = m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		fid, err = lookup(ctx, tx, rid)
		return err
	})
	if err == nil && fid == nil {
		err = errs.NotFound("file")
	}
	if err == nil {
		err = m.streamFile(ctx, w, *fid)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
	}
}

func (m *Module) documentFileHTTP(w http.ResponseWriter, r *http.Request) {
	m.fileOf(w, r, func(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (*uuid.UUID, error) {
		d, err := m.viewDocument(ctx, tx, rid, handle.Property(ctx))
		return d.FileID, err
	})
}

func (m *Module) certificationFileHTTP(w http.ResponseWriter, r *http.Request) {
	m.fileOf(w, r, func(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (*uuid.UUID, error) {
		var fid *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT file_id FROM hris.certifications WHERE id = $1 AND property_id = $2 AND archived_at IS NULL`, rid,
			handle.Property(ctx)).Scan(&fid); err != nil {
			return nil, errs.NotFound("certification")
		}
		return fid, nil
	})
}

func (m *Module) contractFileHTTP(w http.ResponseWriter, r *http.Request) {
	m.fileOf(w, r, func(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (*uuid.UUID, error) {
		var fid *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT signed_file_id FROM hris.contracts WHERE id = $1 AND property_id = $2`, rid, handle.Property(ctx)).Scan(&fid); err != nil {
			return nil, errs.NotFound("contract")
		}
		return fid, nil
	})
}
