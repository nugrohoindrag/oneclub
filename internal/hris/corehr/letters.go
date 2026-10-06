package corehr

// HR letters (FR-CTR-05): employment agreement, employment certificate
// (surat keterangan kerja), warning letter … rendered as PDF from a letter
// template and the employee's data. Identity numbers and salaries are only
// filled in for users who may see them (FR-HR-03).

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"text/template"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/pdf"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/org"
)

// LetterData are the fields a letter template may use.
type LetterData struct {
	Company  struct{ LegalName, Address, City string }
	Property string
	Today    string
	Employee struct {
		EmployeeNo, FullName, NIK, JobTitle, OrgUnit, Position, Grade, JoinDate, BirthPlace, BirthDate, Address, EmploymentStatus string
	}
	Contract struct {
		Number, Type, StartDate, EndDate, BaseSalary string
		ProbationMonths                              int
	}
}

var letterFuncs = template.FuncMap{"upper": strings.ToUpper}

func parseLetter(body string) (*template.Template, error) {
	return template.New("letter").Funcs(letterFuncs).Option("missingkey=error").Parse(body)
}

func (m *Module) registerLetters(reg *route.Registry) {
	add(reg, "HRIS Employees", route.Route{Method: http.MethodGet, Path: "/api/v1/hris/employees/{id}/letters/{code}",
		Summary: "Generate an HR letter (PDF) from a letter template", Permission: "hris.letter.generate", RawContent: "application/pdf",
		Handler: m.letterHTTP})
}

func (m *Module) letterHTTP(w http.ResponseWriter, r *http.Request) {
	eid, err := handle.ID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	code := strings.ToUpper(chi.URLParam(r, "code"))
	ctx := r.Context()
	var out []byte
	var name string
	err = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		out, name, err = m.Letter(ctx, tx, eid, code)
		if err != nil {
			return err
		}
		pid := handle.Property(ctx)
		return audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "generate_letter", EntityType: KeyEmployee, EntityID: eid.String(),
			EntityLabel: name, PropertyID: &pid, Metadata: map[string]any{"template": code}})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Disposition", `inline; filename="`+strings.ReplaceAll(name, `"`, "")+`.pdf"`)
	_, _ = w.Write(out)
}

// Letter renders a letter template for an employee.
func (m *Module) Letter(ctx context.Context, tx pgx.Tx, eid uuid.UUID, code string) ([]byte, string, error) {
	if err := m.inProperty(ctx, tx, eid); err != nil {
		return nil, "", err
	}
	var title, body string
	if err := tx.QueryRow(ctx, `SELECT title, body FROM hris.letter_templates WHERE code = $1 AND archived_at IS NULL AND status = 'active'`, code).
		Scan(&title, &body); err != nil {
		return nil, "", errs.NotFound("letter template")
	}
	tpl, err := parseLetter(body)
	if err != nil {
		return nil, "", errs.Conflict("invalid_template", "the letter template is invalid: "+err.Error())
	}
	e, err := hris.EmployeeByID(ctx, tx, eid)
	if err != nil {
		return nil, "", err
	}
	day := today(ctx, tx, e.PropertyID)
	var d LetterData
	d.Today = day.Format("2 January 2006")
	var legal, addr, city *string
	_ = tx.QueryRow(ctx, `SELECT legal_name, address, city FROM platform.organization LIMIT 1`).Scan(&legal, &addr, &city)
	d.Company.LegalName, d.Company.Address, d.Company.City = deref(legal), deref(addr), deref(city)
	d.Property, _ = org.PropertyName(ctx, tx, e.PropertyID)
	var nik, birthPlace, address *string
	var birth *time.Time
	if err := tx.QueryRow(ctx, `SELECT nik, birth_place, birth_date, address FROM hris.employees WHERE id = $1`, eid).Scan(&nik, &birthPlace, &birth, &address); err != nil {
		return nil, "", err
	}
	d.Employee.EmployeeNo, d.Employee.FullName, d.Employee.JobTitle = e.EmployeeNo, e.FullName, deref(e.JobTitle)
	d.Employee.OrgUnit, d.Employee.Position, d.Employee.Grade = deref(e.OrgUnitName), deref(e.PositionName), deref(e.GradeCode)
	d.Employee.EmploymentStatus, d.Employee.BirthPlace, d.Employee.Address = e.EmploymentStatus, deref(birthPlace), deref(address)
	if e.JoinDate != nil {
		d.Employee.JoinDate = e.JoinDate.Format("2 January 2006")
	}
	if birth != nil {
		d.Employee.BirthDate = birth.Format("2 January 2006")
	}
	if nik != nil {
		d.Employee.NIK = *nik
		if !can(ctx, "hris.employee.view_sensitive", e.PropertyID) {
			d.Employee.NIK = MaskTail(*nik)
		}
	}
	if c, err := hris.ContractAt(ctx, tx, eid, day); err != nil {
		return nil, "", err
	} else if c != nil {
		d.Contract.Number, d.Contract.Type, d.Contract.StartDate = c.Number, strings.ToUpper(c.ContractType), c.StartDate.Format("2 January 2006")
		if c.EndDate != nil {
			d.Contract.EndDate = c.EndDate.Format("2 January 2006")
		}
		if can(ctx, "hris.contract.view_salary", e.PropertyID) {
			d.Contract.BaseSalary = c.BaseSalary
		}
		var months int
		_ = tx.QueryRow(ctx, `SELECT probation_months FROM hris.contracts WHERE id = $1`, c.ID).Scan(&months)
		d.Contract.ProbationMonths = months
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, d); err != nil {
		return nil, "", errs.Conflict("invalid_template", "the letter template cannot be filled: "+err.Error())
	}
	doc := pdf.New()
	if m.Logo != nil {
		if logo := m.Logo(ctx, tx); len(logo) > 0 {
			_ = doc.Logo(logo, 140, 48)
		}
	}
	doc.Row(9, false, d.Company.LegalName)
	doc.Space(12)
	doc.Row(14, true, title)
	doc.Rule(doc.Y + 8)
	doc.Space(8)
	for _, line := range wrapText(buf.String(), 95) {
		doc.Row(10, false, line)
	}
	name := code + " " + e.EmployeeNo
	return doc.Bytes(), name, nil
}

// wrapText splits text into lines of at most n characters (words kept).
func wrapText(text string, n int) []string {
	var out []string
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		line := ""
		for _, w := range strings.Fields(para) {
			if line != "" && len(line)+1+len(w) > n {
				out = append(out, line)
				line = ""
			}
			if line != "" {
				line += " "
			}
			line += w
		}
		out = append(out, line)
	}
	return out
}
