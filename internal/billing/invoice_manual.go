package billing

// Invoice Management (Revenue & Billing, docs/Revenue_Billing_Invoice_Management.md):
// the Create Invoice workspace of the Accountant.
//
//   - Manual invoice: the exceptions of automatic billing (adjustment,
//     special billing, non-system transaction, one-off charge), under its
//     own permission billing.invoice.manual. The lines are posted to a folio
//     of their own (source manual_invoice) with business line and revenue
//     component, so revenue posting, the Tax & Service rules and the AR
//     transfer at issue are those of every folio invoice; voiding the
//     invoice voids the charges.
//   - Preview: the invoice a request would generate, computed in a
//     transaction that is rolled back (lines, deposits, tax, due terms).
//   - Supporting documents and internal notes, never shown to the customer.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/storage"
)

// TaxCode is a Tax & Service rule a manual invoice line can carry.
type TaxCode struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Kind        string `json:"kind" enum:"tax,service"`
	RatePercent string `json:"ratePercent"`
}

// TaxEngine is the Tax & Service engine of Commercial, wired by
// internal/app (billing does not import commercial).
type TaxEngine interface {
	// Codes lists the rules in force.
	Codes(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]TaxCode, error)
	// OnNet computes service and tax of the rules named by codes on a net
	// amount (prices before tax & service) with the tax lines of the folio.
	OnNet(ctx context.Context, q dbtx.Querier, property uuid.UUID, codes []string, net decimal.Decimal, currency string) (service, tax decimal.Decimal, lines any, err error)
}

// ManualLine is a line of a manual invoice.
type ManualLine struct {
	Description      string   `json:"description"`
	BusinessLine     string   `json:"businessLine,omitempty" doc:"Default other; decides the revenue account with the revenue component"`
	RevenueComponent string   `json:"revenueComponent,omitempty" doc:"Default: the business line's (green_fee, fnb, venue_rental …)"`
	Quantity         string   `json:"quantity,omitempty" doc:"Default 1"`
	Unit             string   `json:"unit,omitempty" doc:"Unit of measure (pax, night, hour …)"`
	UnitPrice        string   `json:"unitPrice" doc:"Price before discount, tax and service"`
	Discount         string   `json:"discount,omitempty" doc:"Discount amount of the line"`
	DiscountPercent  string   `json:"discountPercent,omitempty" doc:"Or a discount percentage of the line"`
	TaxCodes         []string `json:"taxCodes,omitempty" doc:"Tax & Service rules added on top of the net (Settings → Tax & Service)"`
}

// maxManualLines caps the lines of a manual invoice.
const maxManualLines = 100

type manualInvoice struct {
	folioID uuid.UUID
	lines   []draftLine
}

// manualLines validates and prices the lines of a manual invoice and posts
// them to a new manual_invoice folio of the payer.
func (h *HTTP) manualLines(ctx context.Context, tx pgx.Tx, property uuid.UUID, in InvoiceInput) (manualInvoice, error) {
	if err := authz.RequireAt(ctx, "billing.invoice.manual", property); err != nil {
		return manualInvoice{}, err
	}
	if in.CustomerID == nil && in.CorporateAccountID == nil {
		return manualInvoice{}, errs.Validation("payer_required", "choose the customer or the company to invoice",
			errs.Field("customerId", "required", "customer or company"))
	}
	if len(in.Lines) > maxManualLines {
		return manualInvoice{}, handle.Invalid("lines", "too_many", fmt.Sprintf("at most %d lines", maxManualLines))
	}
	cur, err := org.Currency(ctx, tx)
	if err != nil {
		return manualInvoice{}, err
	}
	holder := ""
	if in.CorporateAccountID != nil {
		if err := tx.QueryRow(ctx, `SELECT name FROM crm.corporate_accounts WHERE id = $1`, *in.CorporateAccountID).Scan(&holder); err != nil {
			if dbtx.IsNoRows(err) {
				return manualInvoice{}, errs.NotFound("corporate account")
			}
			return manualInvoice{}, err
		}
	}
	if in.CustomerID != nil {
		var name string
		if err := tx.QueryRow(ctx, `SELECT name FROM crm.customers WHERE id = $1`, *in.CustomerID).Scan(&name); err != nil {
			if dbtx.IsNoRows(err) {
				return manualInvoice{}, errs.NotFound("customer")
			}
			return manualInvoice{}, err
		}
		if holder == "" {
			holder = name
		}
	}
	var known []TaxCode
	if h.Tax != nil {
		if known, err = h.Tax.Codes(ctx, tx, property); err != nil {
			return manualInvoice{}, err
		}
	}
	hundred := decimal.NewFromInt(100)
	type priced struct {
		draftLine
		taxLines any
	}
	out := make([]priced, 0, len(in.Lines))
	for i, l := range in.Lines {
		f := func(name string) string { return fmt.Sprintf("lines[%d].%s", i, name) }
		desc := strings.TrimSpace(l.Description)
		if desc == "" {
			return manualInvoice{}, handle.Invalid(f("description"), "required", "describe the product or service")
		}
		qty := decimal.NewFromInt(1)
		if strings.TrimSpace(l.Quantity) != "" {
			if qty, err = decimal.NewFromString(l.Quantity); err != nil || !qty.IsPositive() {
				return manualInvoice{}, handle.Invalid(f("quantity"), "invalid", "a quantity above zero")
			}
		}
		unit, err := decimal.NewFromString(strings.TrimSpace(l.UnitPrice))
		if err != nil || unit.IsNegative() {
			return manualInvoice{}, handle.Invalid(f("unitPrice"), "invalid", "zero or more")
		}
		gross := qty.Mul(unit).Round(places(cur))
		disc := decimal.Zero
		switch {
		case strings.TrimSpace(l.DiscountPercent) != "":
			pct, err := decimal.NewFromString(l.DiscountPercent)
			if err != nil || pct.IsNegative() || pct.GreaterThan(hundred) {
				return manualInvoice{}, handle.Invalid(f("discountPercent"), "invalid", "0 to 100")
			}
			disc = gross.Mul(pct).Div(hundred).Round(places(cur))
		case strings.TrimSpace(l.Discount) != "":
			if disc, err = decimal.NewFromString(l.Discount); err != nil || disc.IsNegative() || disc.GreaterThan(gross) {
				return manualInvoice{}, handle.Invalid(f("discount"), "invalid", "zero up to the line amount")
			}
		}
		bl := l.BusinessLine
		if bl == "" {
			bl = LineOther
		}
		if !slices.Contains(BusinessLines, bl) {
			return manualInvoice{}, handle.Invalid(f("businessLine"), "invalid", "unknown business line")
		}
		comp := l.RevenueComponent
		if comp == "" {
			comp = componentOf(bl)
		}
		if !slices.Contains(RevenueComponents, comp) {
			return manualInvoice{}, handle.Invalid(f("revenueComponent"), "invalid", "unknown revenue component")
		}
		net := gross.Sub(disc)
		svc, tax := decimal.Zero, decimal.Zero
		var taxLines any
		if len(l.TaxCodes) > 0 {
			for _, c := range l.TaxCodes {
				if !slices.ContainsFunc(known, func(k TaxCode) bool { return k.Code == c }) {
					return manualInvoice{}, handle.Invalid(f("taxCodes"), "not_found", "unknown Tax & Service rule "+c)
				}
			}
			if svc, tax, taxLines, err = h.Tax.OnNet(ctx, tx, property, l.TaxCodes, net, cur); err != nil {
				return manualInvoice{}, err
			}
		}
		out = append(out, priced{draftLine: draftLine{desc: desc, qty: qty, unit: unit, discount: disc, net: net, svc: svc, tax: tax,
			total: net.Add(svc).Add(tax), line: ptr(bl), component: ptr(comp), uom: nullStr(strings.TrimSpace(l.Unit))}, taxLines: taxLines})
	}
	ref := in.BillingRef
	for _, r := range []string{in.CustomerPO, in.ContractRef} {
		if ref == "" {
			ref = r
		}
	}
	folio, err := h.Svc.OpenLineFolio(ctx, tx, LineFolioInput{FolioInput: FolioInput{Property: property, CustomerID: in.CustomerID, HolderName: holder,
		SourceType: "manual_invoice", SourceRef: ref}, BusinessLine: *out[0].line})
	if err != nil {
		return manualInvoice{}, err
	}
	if in.CorporateAccountID != nil {
		if _, err := tx.Exec(ctx, `UPDATE billing.folios SET corporate_account_id = $2 WHERE id = $1`, folio.ID, *in.CorporateAccountID); err != nil {
			return manualInvoice{}, err
		}
	}
	m := manualInvoice{folioID: folio.ID}
	for _, p := range out {
		lid, err := h.Svc.AddLineCharge(ctx, tx, LineCharge{Charge: Charge{FolioID: folio.ID, Description: p.desc, Quantity: p.qty, UnitPrice: p.unit,
			Net: p.net, Tax: p.tax, Service: p.svc, Total: p.total, ReferenceType: "manual_invoice"}, BusinessLine: *p.line, RevenueComponent: *p.component,
			TaxLines: p.taxLines})
		if err != nil {
			return manualInvoice{}, err
		}
		l, fid := p.draftLine, folio.ID
		l.folioLine, l.folioOf = &lid, &fid
		m.lines = append(m.lines, l)
	}
	return m, nil
}

// voidManualCharges voids the charges of a voided manual invoice: they were
// posted for this invoice only.
func (s *Service) voidManualCharges(ctx context.Context, tx pgx.Tx, inv Invoice, reason string) error {
	if inv.FolioID == nil || inv.Source != "manual" {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id FROM billing.folio_lines WHERE folio_id = $1 AND voided_at IS NULL`, *inv.FolioID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	for _, lid := range ids {
		if err := s.VoidCharge(ctx, tx, lid, "Invoice void: "+reason); err != nil {
			return err
		}
	}
	return nil
}

// ── supporting documents ──────────────────────────────────────────────────

const (
	invoiceFilePurpose  = "attachment" // platform.files purpose of private documents
	invoiceFileMaxBytes = 10 << 20
	maxInvoiceFiles     = 20
)

var invoiceFileTypes = map[string]bool{"application/pdf": true, "image/jpeg": true, "image/png": true, "image/webp": true}

// invoiceFiles returns the distinct attachments, accepting only uploaded
// invoice documents.
func invoiceFiles(ctx context.Context, q dbtx.Querier, ids []uuid.UUID) ([]uuid.UUID, error) {
	out := []uuid.UUID{}
	for _, x := range ids {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	if len(out) == 0 {
		return out, nil
	}
	if len(out) > maxInvoiceFiles {
		return nil, handle.Invalid("attachmentFileIds", "too_many", fmt.Sprintf("at most %d documents", maxInvoiceFiles))
	}
	var n int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM platform.files WHERE id = ANY($1) AND purpose = $2`, out, invoiceFilePurpose).Scan(&n); err != nil {
		return nil, err
	}
	if n != len(out) {
		return nil, handle.Invalid("attachmentFileIds", "not_found", "upload the documents through /api/v1/billing/invoice-files")
	}
	return out, nil
}

// InvoiceInternal is what staff see of an invoice and the customer does not.
type InvoiceInternal struct {
	Notes       *string             `json:"notes" doc:"Internal notes, never printed"`
	Attachments []InvoiceAttachment `json:"attachments"`
}

// InvoiceAttachment is a supporting document of an invoice (PO, contract,
// booking / event confirmation, service evidence).
type InvoiceAttachment struct {
	FileID      uuid.UUID `json:"fileId" db:"id"`
	Filename    string    `json:"filename" db:"filename"`
	ContentType string    `json:"contentType" db:"content_type"`
	SizeBytes   int64     `json:"sizeBytes" db:"size_bytes"`
}

// WithInternal adds the staff-only part of an invoice.
func WithInternal(ctx context.Context, q dbtx.Querier, d InvoiceDetail) (InvoiceDetail, error) {
	in := InvoiceInternal{}
	if err := q.QueryRow(ctx, `SELECT internal_notes FROM billing.invoices WHERE id = $1`, d.ID).Scan(&in.Notes); err != nil {
		return d, err
	}
	var err error
	if in.Attachments, err = handle.List[InvoiceAttachment](q.Query(ctx, `SELECT f.id, f.filename, f.content_type, f.size_bytes
		FROM billing.invoices i JOIN platform.files f ON f.id = ANY(i.attachment_file_ids) WHERE i.id = $1 ORDER BY f.created_at`, d.ID)); err != nil {
		return d, err
	}
	if in.Attachments == nil {
		in.Attachments = []InvoiceAttachment{}
	}
	d.Internal = &in
	return d, nil
}

// InvoiceInternalInput changes the internal part of an invoice.
type InvoiceInternalInput struct {
	Notes             *string     `json:"notes,omitempty" doc:"Internal notes; empty clears"`
	AttachmentFileIDs []uuid.UUID `json:"attachmentFileIds,omitempty" doc:"The full list of supporting documents (replaces it)"`
}

var errPreview = errors.New("invoice preview")

// RegisterInvoiceWorkspace adds the routes of the Create Invoice workspace.
func (h *HTTP) RegisterInvoiceWorkspace(reg *route.Registry) {
	db := h.Svc.DB
	add := func(rt route.Route) {
		rt.Module, rt.Tag, rt.Scope = "billing", "Invoices", route.ScopeProperty
		reg.Add(rt)
	}
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/billing/invoices:preview", Summary: "Preview the invoice a Generate Invoice request would create (nothing is saved)",
		Permission: "billing.invoice.create", Request: InvoiceInput{}, Response: InvoiceDetail{}, Status: http.StatusOK,
		NoAudit: "read-only preview, rolled back", Handler: func(w http.ResponseWriter, r *http.Request) {
			var in InvoiceInput
			if err := httpx.Decode(r, &in); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			in.Issue = false
			ctx := r.Context()
			var out InvoiceDetail
			err := db.WithTx(ctx, func(tx pgx.Tx) error {
				d, err := h.CreateInvoice(ctx, tx, handle.Property(ctx), in)
				if err != nil {
					return err
				}
				out = d
				return errPreview // roll back
			})
			if !errors.Is(err, errPreview) {
				httpx.WriteError(w, r, err)
				return
			}
			out.ID = uuid.Nil
			httpx.JSON(w, http.StatusOK, out)
		}})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/billing/invoice-tax-codes", Summary: "Tax & Service rules a manual invoice line can carry",
		Permission: "billing.invoice.create", Response: TaxCode{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[TaxCode], error) {
			if h.Tax == nil {
				return handle.Page([]TaxCode{}, nil)
			}
			return handle.Page(h.Tax.Codes(ctx, tx, handle.Property(ctx)))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/billing/invoice-files",
		Summary:    "Upload a supporting document of an invoice (multipart: file; PDF, JPEG, PNG or WebP up to 10 MB) for attachmentFileIds",
		Permission: "billing.invoice.create", Response: storage.File{}, Handler: h.uploadInvoiceFile})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/billing/invoices/{id}/attachments/{fileId}", Summary: "Download a supporting document of an invoice",
		Permission: "billing.invoice.view", RawContent: "application/octet-stream", Handler: h.downloadInvoiceFile})
	add(route.Route{Method: http.MethodPatch, Path: "/api/v1/billing/invoices/{id}/internal", Summary: "Change the internal notes and supporting documents of an invoice",
		Permission: "billing.invoice.create", Request: InvoiceInternalInput{}, Response: InvoiceDetail{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in InvoiceInternalInput) (InvoiceDetail, error) {
			iid, err := handle.ID(r)
			if err != nil {
				return InvoiceDetail{}, err
			}
			before, err := lockInvoice(ctx, tx, iid)
			if err != nil {
				return InvoiceDetail{}, err
			}
			if in.AttachmentFileIDs != nil {
				files, err := invoiceFiles(ctx, tx, in.AttachmentFileIDs)
				if err != nil {
					return InvoiceDetail{}, err
				}
				if _, err := tx.Exec(ctx, `UPDATE billing.invoices SET attachment_file_ids = $2 WHERE id = $1`, iid, files); err != nil {
					return InvoiceDetail{}, err
				}
			}
			if in.Notes != nil {
				if _, err := tx.Exec(ctx, `UPDATE billing.invoices SET internal_notes = $2 WHERE id = $1`, iid, nullStr(*in.Notes)); err != nil {
					return InvoiceDetail{}, err
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE billing.invoices SET version = version + 1, updated_by = $2 WHERE id = $1`, iid, id.Ptr(actor(ctx))); err != nil {
				return InvoiceDetail{}, err
			}
			p := handle.Property(ctx)
			if err := audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionUpdate, EntityType: "billing.invoice", EntityID: iid.String(),
				EntityLabel: deref(before.Number), PropertyID: &p, After: in}); err != nil {
				return InvoiceDetail{}, err
			}
			d, err := GetInvoice(ctx, tx, iid, h.publicBase())
			if err != nil {
				return d, err
			}
			return WithInternal(ctx, tx, d)
		})})
}

func (h *HTTP) uploadInvoiceFile(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, invoiceFileMaxBytes+256<<10)
	if err := r.ParseMultipartForm(invoiceFileMaxBytes); err != nil { //nolint:gosec // G120: body capped by MaxBytesReader above
		httpx.WriteError(w, r, errs.BadRequest("invalid_upload", "upload must be multipart/form-data up to 10 MB"))
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		httpx.WriteError(w, r, errs.Validation("file_required", "file is required", errs.Field("file", "required", "choose a document")))
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, invoiceFileMaxBytes+1))
	if err != nil || len(data) == 0 || len(data) > invoiceFileMaxBytes {
		httpx.WriteError(w, r, errs.Validation("file_size", "file too large or empty", errs.Field("file", "size", "up to 10 MB")))
		return
	}
	ctype := http.DetectContentType(data)
	if i := strings.IndexByte(ctype, ';'); i >= 0 {
		ctype = ctype[:i]
	}
	if !invoiceFileTypes[ctype] {
		httpx.WriteError(w, r, errs.Validation("file_type", "unsupported file type", errs.Field("file", "type", "PDF, JPEG, PNG or WebP")))
		return
	}
	name := strings.TrimSpace(hdr.Filename)
	if name == "" {
		name = "document"
	}
	ctx := r.Context()
	pid := handle.Property(ctx)
	var out storage.File
	err = h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		if out, err = h.Files.Save(ctx, tx, name, ctype, invoiceFilePurpose, false, bytes.NewReader(data), int64(len(data))); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionCreate, EntityType: "platform.file",
			EntityID: out.ID.String(), EntityLabel: "Invoice document " + name, PropertyID: &pid, After: out})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *HTTP) downloadInvoiceFile(w http.ResponseWriter, r *http.Request) {
	iid, err := httpx.PathUUID(r, "id")
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
	err = h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var ctype string
		if err := tx.QueryRow(ctx, `SELECT f.content_type FROM billing.invoices i JOIN platform.files f ON f.id = $3 AND f.id = ANY(i.attachment_file_ids)
			WHERE i.id = $1 AND i.property_id = $2`, iid, handle.Property(ctx), fid).Scan(&ctype); err != nil {
			return errs.NotFound("invoice document")
		}
		rc, name, err := h.Files.Open(ctx, fid)
		if err != nil {
			return errs.NotFound("invoice document")
		}
		defer rc.Close()
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Content-Disposition", `inline; filename="`+strings.ReplaceAll(name, `"`, "")+`"`)
		_, err = io.Copy(w, rc)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
	}
}
