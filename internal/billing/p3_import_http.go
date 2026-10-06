package billing

// Corporate AR migration (PRD P3 EP-25 FR-MIG-P3-02, FR-MIG-P3-05, §16 #17):
// the open corporate invoices of the legacy system arrive from the Excel
// workbook (XLSX, first sheet) or its CSV export. Preview validates every
// row and reconciles the totals without saving; commit imports the rows
// (idempotent per legacy number, so a delta file can be re-run) and
// reconciles the corporate AR total of the migrated invoices against the
// file and the control total signed off by the club.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/csv"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
	"github.com/xuri/excelize/v2"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// ARImportColumns are the accepted columns (header row, any order).
var ARImportColumns = []string{"legacyNumber", "corporateCode", "customerCode", "issueDate", "dueDate", "outstanding", "originalTotal", "description"}

// CorporateARImportInput is an open corporate invoice file.
type CorporateARImportInput struct {
	Mode          string `json:"mode" enum:"preview,commit" doc:"preview validates and reconciles without saving"`
	CSV           string `json:"csv,omitempty" doc:"CSV text with a header row: legacyNumber (required), corporateCode or customerCode, issueDate, dueDate (YYYY-MM-DD or DD/MM/YYYY), outstanding (required), originalTotal, description"`
	XLSX          string `json:"xlsx,omitempty" doc:"Base64 of the Excel workbook (first sheet, same columns); instead of csv"`
	ExpectedTotal string `json:"expectedTotal,omitempty" doc:"Control total of the open corporate AR in the legacy system, signed off by the club"`
}

// CorporateARImportError is a rejected row.
type CorporateARImportError struct {
	Row     int    `json:"row" doc:"1-based line (header = 1)"`
	Field   string `json:"field,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// CorporateARCompanyTotal is the migrated AR of one company (or customer).
type CorporateARCompanyTotal struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Invoices    int    `json:"invoices"`
	Outstanding string `json:"outstanding"`
}

// CorporateARImportResult reports an import and its reconciliation.
type CorporateARImportResult struct {
	Mode          string                    `json:"mode" enum:"preview,commit"`
	TotalRows     int                       `json:"totalRows"`
	Imported      int                       `json:"imported" doc:"New invoices (preview: would be created)"`
	Existing      int                       `json:"existing" doc:"Rows already imported (same legacy number and amount)"`
	Failed        int                       `json:"failed"`
	Errors        []CorporateARImportError  `json:"errors"`
	FileTotal     string                    `json:"fileTotal" doc:"Outstanding of the valid rows of the file"`
	ExpectedTotal *string                   `json:"expectedTotal"`
	MigratedOpen  string                    `json:"migratedOpen" doc:"Open balance of all migrated invoices (RH-…) in OneClub after this run"`
	Difference    string                    `json:"difference" doc:"Control total (else file total) − migrated open AR"`
	Reconciled    bool                      `json:"reconciled" doc:"Every row valid, the file matches the control total and the migrated AR matches the file"`
	Companies     []CorporateARCompanyTotal `json:"companies"`
}

// arRows reads the rows of the file (header first).
func arRows(in CorporateARImportInput) ([][]string, error) {
	if strings.TrimSpace(in.XLSX) != "" {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(in.XLSX))
		if err != nil {
			return nil, handle.Invalid("xlsx", "invalid", "the workbook must be base64 encoded")
		}
		f, err := excelize.OpenReader(bytes.NewReader(raw))
		if err != nil {
			return nil, handle.Invalid("xlsx", "invalid", "not an Excel workbook (.xlsx)")
		}
		defer f.Close()
		sheets := f.GetSheetList()
		if len(sheets) == 0 {
			return nil, handle.Invalid("xlsx", "empty", "the workbook has no sheet")
		}
		rows, err := f.GetRows(sheets[0])
		if err != nil {
			return nil, handle.Invalid("xlsx", "invalid", err.Error())
		}
		return rows, nil
	}
	if strings.TrimSpace(in.CSV) == "" {
		return nil, handle.Invalid("csv", "required", "upload the CSV or the Excel workbook")
	}
	cr := csv.NewReader(strings.NewReader(strings.TrimPrefix(in.CSV, "\uFEFF")))
	cr.FieldsPerRecord, cr.TrimLeadingSpace = -1, true
	var out [][]string
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, handle.Invalid("csv", "csv_invalid", err.Error())
		}
		out = append(out, rec)
	}
}

func arDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, true
	}
	for _, layout := range []string{"2006-01-02", "02/01/2006", "2/1/2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func arAmount(s string) (decimal.Decimal, bool) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "Rp"))
	s = strings.ReplaceAll(s, " ", "")
	if s == "" {
		return decimal.Zero, true
	}
	d, err := decimal.NewFromString(s)
	return d, err == nil && !d.IsNegative()
}

// ImportCorporateAR previews or imports the open corporate invoices of a
// legacy file and reconciles the AR total.
func (s *Service) ImportCorporateAR(ctx context.Context, tx pgx.Tx, property uuid.UUID, in CorporateARImportInput) (CorporateARImportResult, error) {
	res := CorporateARImportResult{Mode: in.Mode, Errors: []CorporateARImportError{}, Companies: []CorporateARCompanyTotal{}}
	if in.Mode != "preview" && in.Mode != "commit" {
		return res, handle.Invalid("mode", "invalid", "mode is preview or commit")
	}
	var expected *decimal.Decimal
	if strings.TrimSpace(in.ExpectedTotal) != "" {
		d, ok := arAmount(in.ExpectedTotal)
		if !ok {
			return res, handle.Invalid("expectedTotal", "invalid", "a positive amount")
		}
		expected = &d
		str := d.String()
		res.ExpectedTotal = &str
	}
	rows, err := arRows(in)
	if err != nil {
		return res, err
	}
	if len(rows) == 0 {
		return res, handle.Invalid("csv", "empty", "the file is empty")
	}
	cols := map[string]int{}
	for i, h := range rows[0] {
		h = strings.TrimSpace(strings.TrimPrefix(h, "\uFEFF"))
		matched := false
		for _, c := range ARImportColumns {
			if strings.EqualFold(h, c) {
				cols[c], matched = i, true
			}
		}
		if !matched && h != "" {
			res.Errors = append(res.Errors, CorporateARImportError{Row: 1, Field: h, Code: "unknown_column", Message: "unknown column " + h})
		}
	}
	for _, c := range []string{"legacyNumber", "outstanding"} {
		if _, ok := cols[c]; !ok {
			res.Errors = append(res.Errors, CorporateARImportError{Row: 1, Field: c, Code: "missing_column", Message: "column " + c + " is required"})
		}
	}
	_, hasCorp := cols["corporateCode"]
	_, hasCust := cols["customerCode"]
	if !hasCorp && !hasCust {
		res.Errors = append(res.Errors, CorporateARImportError{Row: 1, Field: "corporateCode", Code: "missing_column",
			Message: "column corporateCode (or customerCode) is required"})
	}
	if len(res.Errors) > 0 {
		return res, recordARImport(ctx, tx, property, res)
	}
	outer, err := tx.Begin(ctx) // preview rolls everything back
	if err != nil {
		return res, err
	}
	defer func() { _ = outer.Rollback(ctx) }()
	fileTotal := decimal.Zero
	seen := map[string]int{}
	for i, rec := range rows[1:] {
		line := i + 2
		get := func(c string) string {
			if j, ok := cols[c]; ok && j < len(rec) {
				return strings.TrimSpace(rec[j])
			}
			return ""
		}
		if strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		res.TotalRows++
		fail := func(field, code, msg string) {
			res.Failed++
			res.Errors = append(res.Errors, CorporateARImportError{Row: line, Field: field, Code: code, Message: msg})
		}
		legacy := strings.ToUpper(get("legacyNumber"))
		if legacy == "" {
			fail("legacyNumber", "required", "legacy invoice number is required")
			continue
		}
		if prev, dup := seen[legacy]; dup {
			fail("legacyNumber", "duplicate", "legacy number repeated (row "+itoa(prev)+")")
			continue
		}
		seen[legacy] = line
		out, ok := arAmount(get("outstanding"))
		if !ok || !out.IsPositive() {
			fail("outstanding", "invalid", "a positive outstanding amount")
			continue
		}
		orig, ok := arAmount(get("originalTotal"))
		if !ok || (orig.IsPositive() && orig.LessThan(out)) {
			fail("originalTotal", "invalid", "the original total cannot be below the outstanding amount")
			continue
		}
		issue, ok1 := arDate(get("issueDate"))
		due, ok2 := arDate(get("dueDate"))
		if !ok1 || !ok2 {
			fail("dueDate", "invalid_date", "dates as YYYY-MM-DD or DD/MM/YYYY")
			continue
		}
		li := LegacyInvoice{LegacyNumber: legacy, IssueDate: issue, DueDate: due, Outstanding: out, OriginalTotal: orig, Description: get("description")}
		code := get("corporateCode")
		if code != "" {
			var cid uuid.UUID
			if err := outer.QueryRow(ctx, `SELECT id FROM crm.corporate_accounts WHERE property_id = $1 AND upper(code) = upper($2) AND archived_at IS NULL`,
				property, code).Scan(&cid); err != nil {
				if dbtx.IsNoRows(err) {
					fail("corporateCode", "not_found", "corporate account "+code+" not found")
					continue
				}
				return res, err
			}
			li.CorporateAccountID = &cid
		} else if cc := get("customerCode"); cc != "" {
			var cid uuid.UUID
			if err := outer.QueryRow(ctx, `SELECT id FROM crm.customers WHERE property_id = $1 AND upper(code) = upper($2) AND erased_at IS NULL`,
				property, cc).Scan(&cid); err != nil {
				if dbtx.IsNoRows(err) {
					fail("customerCode", "not_found", "customer "+cc+" not found")
					continue
				}
				return res, err
			}
			li.CustomerID = &cid
		} else {
			fail("corporateCode", "required", "corporate code or customer code is required")
			continue
		}
		sp, err := outer.Begin(ctx)
		if err != nil {
			return res, err
		}
		inv, inserted, err := s.ImportOpenInvoice(ctx, sp, property, li)
		if err != nil {
			_ = sp.Rollback(ctx)
			fail("", "invalid", err.Error())
			continue
		}
		if !inserted && !dec(inv.Total).Equal(round(out, inv.Currency)) {
			_ = sp.Rollback(ctx)
			fail("outstanding", "changed", "invoice RH-"+legacy+" was imported with "+inv.Total+"; correct it in OneClub, not by re-import")
			continue
		}
		if err := sp.Commit(ctx); err != nil {
			return res, err
		}
		if inserted {
			res.Imported++
		} else {
			res.Existing++
		}
		fileTotal = fileTotal.Add(out)
	}
	res.FileTotal = fileTotal.String()
	migrated := decimal.Zero
	comp, err := handle.List[struct {
		Code        string `db:"code"`
		Name        string `db:"name"`
		Invoices    int    `db:"invoices"`
		Outstanding string `db:"outstanding"`
	}](outer.Query(ctx, `SELECT coalesce(ca.code, c.code, '') AS code, coalesce(ca.name, c.name, i.bill_to_name) AS name, count(*)::int AS invoices,
		trim_scale(sum(i.total - i.paid_amount - i.credited_amount - i.written_off_amount))::text AS outstanding
		FROM billing.invoices i LEFT JOIN crm.corporate_accounts ca ON ca.id = i.corporate_account_id LEFT JOIN crm.customers c ON c.id = i.customer_id
		AND i.corporate_account_id IS NULL
		WHERE i.property_id = $1 AND i.number LIKE 'RH-%' AND i.status NOT IN ('void', 'paid', 'draft')
		GROUP BY 1, 2`, property))
	if err != nil {
		return res, err
	}
	for _, c := range comp {
		res.Companies = append(res.Companies, CorporateARCompanyTotal{Code: c.Code, Name: c.Name, Invoices: c.Invoices, Outstanding: c.Outstanding})
		migrated = migrated.Add(dec(c.Outstanding))
	}
	sort.Slice(res.Companies, func(i, j int) bool { return res.Companies[i].Code < res.Companies[j].Code })
	res.MigratedOpen = migrated.String()
	control := fileTotal
	if expected != nil {
		control = *expected
	}
	res.Difference = control.Sub(migrated).String()
	res.Reconciled = res.Failed == 0 && (expected == nil || expected.Equal(fileTotal)) && migrated.Equal(control)
	if in.Mode == "commit" {
		if err := outer.Commit(ctx); err != nil {
			return res, err
		}
	}
	return res, recordARImport(ctx, tx, property, res)
}

// recordARImport audits an import run (preview runs included: the file was
// checked by this user).
func recordARImport(ctx context.Context, tx pgx.Tx, property uuid.UUID, res CorporateARImportResult) error {
	return audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionImport, EntityType: "billing.invoice", EntityID: property.String(),
		EntityLabel: "Corporate AR migration (" + res.Mode + ")", PropertyID: &property, After: map[string]any{"mode": res.Mode, "imported": res.Imported,
			"existing": res.Existing, "failed": res.Failed, "fileTotal": res.FileTotal, "expectedTotal": res.ExpectedTotal, "migratedOpen": res.MigratedOpen,
			"reconciled": res.Reconciled, "errors": len(res.Errors)}})
}

func itoa(n int) string {
	return decimal.NewFromInt(int64(n)).String()
}

// registerARImport adds the corporate AR import route.
func (h *HTTP) registerARImport(reg *route.Registry) {
	reg.Add(route.Route{Module: "billing", Tag: "Invoices", Scope: route.ScopeProperty, Method: http.MethodPost, Path: "/api/v1/billing/invoices:import",
		Summary:    "Import the open corporate invoices of the legacy system (Excel / CSV; preview or commit) with the AR reconciliation",
		Permission: "billing.invoice.import", Request: CorporateARImportInput{}, Response: CorporateARImportResult{}, Status: http.StatusOK,
		Handler: handle.Write(h.Svc.DB, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CorporateARImportInput) (CorporateARImportResult, error) {
			return h.Svc.ImportCorporateAR(ctx, tx, handle.Property(ctx), in)
		})})
}
