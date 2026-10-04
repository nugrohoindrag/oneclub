package sales

// Lead & opportunity import (PRD P3 EP-25 FR-MIG-P3-04): active leads and
// opportunities from the sales spreadsheets, CSV with a header row.
// Re-running a file is idempotent by externalRef; preview validates every
// row without saving.

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// ImportColumns are the accepted CSV columns.
var ImportColumns = []string{"externalRef", "name", "companyName", "phone", "email", "source", "line", "eventType", "eventDate", "pax", "budget",
	"notes", "ownerEmail", "status", "createdAt", "opportunityTitle", "expectedValue", "stage", "expectedCloseDate"}

// LeadImportInput is a CSV import of leads.
type LeadImportInput struct {
	Mode string `json:"mode" enum:"preview,commit" doc:"preview validates without saving"`
	CSV  string `json:"csv" doc:"Header row with: externalRef (required), name, companyName, phone, email, source, line, eventType, eventDate, pax, budget, notes, ownerEmail, status (new|contacted|qualified|unqualified), createdAt (YYYY-MM-DD), opportunityTitle, expectedValue, stage (code or name), expectedCloseDate"`
}

// ImportRowError is a rejected row.
type ImportRowError struct {
	Row     int    `json:"row" doc:"1-based line (header = 1)"`
	Field   string `json:"field,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// LeadImportResult reports an import.
type LeadImportResult struct {
	Mode          string           `json:"mode" enum:"preview,commit"`
	TotalRows     int              `json:"totalRows"`
	Created       int              `json:"created"`
	Existing      int              `json:"existing" doc:"Rows already imported (same externalRef)"`
	Opportunities int              `json:"opportunities"`
	Failed        int              `json:"failed"`
	Errors        []ImportRowError `json:"errors"`
}

var errPreview = errors.New("preview")

// ImportLeads imports leads (and their opportunities) from CSV.
func (m *Module) ImportLeads(ctx context.Context, tx pgx.Tx, property uuid.UUID, in LeadImportInput) (LeadImportResult, error) {
	res := LeadImportResult{Mode: in.Mode, Errors: []ImportRowError{}}
	if in.Mode != "preview" && in.Mode != "commit" {
		return res, enumErr("mode", []string{"preview", "commit"})
	}
	cr := csv.NewReader(strings.NewReader(strings.TrimPrefix(in.CSV, "\uFEFF")))
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return res, handle.Invalid("csv", "csv_invalid", "the file is empty or not a CSV")
	}
	cols := map[string]int{}
	for i, h := range header {
		h = strings.TrimSpace(h)
		matched := false
		for _, c := range ImportColumns {
			if strings.EqualFold(h, c) {
				cols[c], matched = i, true
			}
		}
		if !matched && h != "" {
			res.Errors = append(res.Errors, ImportRowError{Row: 1, Field: h, Code: "unknown_column", Message: "unknown column " + h})
		}
	}
	if _, ok := cols["externalRef"]; !ok {
		res.Errors = append(res.Errors, ImportRowError{Row: 1, Field: "externalRef", Code: "missing_column", Message: "column externalRef is required"})
	}
	if len(res.Errors) > 0 {
		return res, nil
	}
	outer, err := tx.Begin(ctx) // preview rolls everything back
	if err != nil {
		return res, err
	}
	defer func() { _ = outer.Rollback(ctx) }()
	line := 1
	for {
		rec, err := cr.Read()
		line++
		if errors.Is(err, io.EOF) {
			break
		}
		get := func(c string) string {
			if i, ok := cols[c]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		if err != nil {
			res.TotalRows++
			res.Failed++
			res.Errors = append(res.Errors, ImportRowError{Row: line, Code: "csv_invalid", Message: err.Error()})
			continue
		}
		empty := true
		for _, c := range rec {
			if strings.TrimSpace(c) != "" {
				empty = false
			}
		}
		if empty {
			continue
		}
		res.TotalRows++
		if res.TotalRows > 20000 {
			return res, handle.Invalid("csv", "too_many_rows", "imports are limited to 20,000 rows per file")
		}
		sp, err := outer.Begin(ctx)
		if err != nil {
			return res, err
		}
		created, opp, rowErr := m.importRow(ctx, sp, property, get)
		if rowErr != nil {
			_ = sp.Rollback(ctx)
			res.Failed++
			if de, ok := errs.As(rowErr); ok && de.Kind != errs.KindInternal {
				if len(de.Fields) == 0 {
					res.Errors = append(res.Errors, ImportRowError{Row: line, Code: de.Code, Message: de.Message})
				}
				for _, f := range de.Fields {
					res.Errors = append(res.Errors, ImportRowError{Row: line, Field: f.Field, Code: f.Code, Message: f.Message})
				}
				continue
			}
			return res, rowErr
		}
		if err := sp.Commit(ctx); err != nil {
			return res, err
		}
		if created {
			res.Created++
		} else {
			res.Existing++
		}
		if opp {
			res.Opportunities++
		}
	}
	if in.Mode == "commit" {
		if err := outer.Commit(ctx); err != nil {
			return res, err
		}
	}
	return res, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionImport, EntityType: "crm.lead", EntityID: property.String(),
		EntityLabel: "Lead import (" + in.Mode + ")", PropertyID: &property, After: map[string]any{"mode": in.Mode, "rows": res.TotalRows,
			"created": res.Created, "existing": res.Existing, "opportunities": res.Opportunities, "failed": res.Failed}})
}

// importRow imports one row; it reports whether a lead and an opportunity
// were created.
func (m *Module) importRow(ctx context.Context, tx pgx.Tx, property uuid.UUID, get func(string) string) (bool, bool, error) {
	ref := get("externalRef")
	if ref == "" {
		return false, false, handle.Invalid("externalRef", "required", "externalRef is required")
	}
	var existing uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM crm.sales_leads WHERE property_id = $1 AND external_ref = $2`, property, ref).Scan(&existing)
	if err == nil {
		return false, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, false, err
	}
	status := strings.ToLower(get("status"))
	if status == "" {
		status = "new"
	}
	if !oneOf([]string{"new", "contacted", "qualified", "unqualified"}, status) {
		return false, false, handle.Invalid("status", "invalid", "new, contacted, qualified or unqualified")
	}
	source := strings.ToLower(get("source"))
	if source == "" {
		source = "import"
	}
	spec := leadSpec{LeadInput: LeadInput{Name: get("name"), CompanyName: get("companyName"), Phone: get("phone"), Email: get("email"), Source: source,
		Line: strings.ToLower(get("line")), EventType: strings.ToLower(get("eventType")), Budget: get("budget"), Notes: get("notes")},
		externalRef: ref, status: status, noSLA: true, assignMethod: "import"}
	if v := get("eventDate"); v != "" {
		d := route.Date(v)
		spec.EventDate = &d
	}
	if v := get("pax"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return false, false, handle.Invalid("pax", "invalid", "a whole number")
		}
		spec.Pax = &n
	}
	if v := get("createdAt"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return false, false, handle.Invalid("createdAt", "invalid_date", "YYYY-MM-DD")
		}
		spec.createdAt = &t
	}
	if v := strings.ToLower(get("ownerEmail")); v != "" {
		var uid uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM platform.users WHERE lower(email) = $1 AND status = 'active'`, v).Scan(&uid); err != nil {
			return false, false, handle.Invalid("ownerEmail", "not_found", "no active user with this e-mail")
		}
		spec.OwnerUserID = &uid
	}
	if err := spec.validate(); err != nil {
		return false, false, err
	}
	l, err := m.createLead(ctx, tx, property, spec)
	if err != nil {
		return false, false, err
	}
	if status == "unqualified" {
		if _, err := tx.Exec(ctx, `UPDATE crm.sales_leads SET unqualified_at = created_at, unqualified_reason = 'other', unqualified_note = 'imported'
			WHERE id = $1`, l.ID); err != nil {
			return false, false, err
		}
	}
	title := get("opportunityTitle")
	if title == "" || status == "unqualified" {
		return true, false, nil
	}
	in := ConvertInput{Title: title, ExpectedValue: get("expectedValue"), OwnerUserID: spec.OwnerUserID}
	if v := get("expectedCloseDate"); v != "" {
		d := route.Date(v)
		in.ExpectedCloseDate = &d
	}
	res, err := m.ConvertLead(ctx, tx, property, l.ID, in)
	if err != nil {
		return false, false, err
	}
	if stage := get("stage"); stage != "" && res.Opportunity != nil {
		var sid uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM crm.sales_pipeline_stages WHERE pipeline_id = $1 AND kind = 'open' AND archived_at IS NULL
			AND (lower(code) = lower($2) OR lower(name) = lower($2)) LIMIT 1`, res.Opportunity.PipelineID, stage).Scan(&sid)
		if err != nil {
			return false, false, handle.Invalid("stage", "not_found", "no open stage "+stage+" in pipeline "+res.Opportunity.PipelineName)
		}
		if sid != res.Opportunity.StageID {
			st, err := stageOf(ctx, tx, sid)
			if err != nil {
				return false, false, err
			}
			if err := m.changeStage(ctx, tx, property, *res.Opportunity, st, "imported"); err != nil {
				return false, false, err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.sales_opportunities SET external_ref = $2 WHERE id = $1`, res.Opportunity.ID, ref); err != nil {
		return false, false, err
	}
	return true, true, nil
}

var _ = errPreview
