package payroll

// Payroll migration (PRD P5 EP-28 FR-MIG-P5-03/05/06): the opening
// year-to-date payroll per employee (taxable gross, deductible JHT / JP,
// PPh 21 withheld before the cut-over, for the annual calculation and the
// 1721-A1) and the legacy payroll of a parallel-run period (compared with
// the OneClub run). CSV through `POST /hris/payroll-imports` or
// `oneclub import hris --payroll-ytd F | --legacy-payroll F --period
// YYYY-MM`; repeatable (upsert), dry run, one savepoint per row. Also the
// year-to-date view and the payroll profiles of employees (Benefits).

import (
	"context"
	"encoding/csv"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// Import kinds and their CSV columns.
const (
	ImportYTD    = "ytd"
	ImportLegacy = "legacy"
)

// ImportColumns are the CSV headers per kind.
var ImportColumns = map[string][]string{
	ImportYTD:    {"employeeNo", "taxYear", "throughMonth", "gross", "deductible", "pph21", "monthsWorked", "legacyRef"},
	ImportLegacy: {"employeeNo", "componentCode", "amount", "periodCode", "batchRef"},
}

// PayrollImportRequest imports a CSV.
type PayrollImportRequest struct {
	Kind       string `json:"kind" enum:"ytd,legacy"`
	PeriodCode string `json:"periodCode,omitempty" doc:"Legacy payroll: the period (YYYY-MM) when the CSV has no periodCode column"`
	CSV        string `json:"csv" doc:"ytd: employeeNo, taxYear, throughMonth, gross, deductible, pph21, monthsWorked, legacyRef; legacy: employeeNo, componentCode (a component or GROSS, BPJS_EE, PPH21, NET), amount, periodCode, batchRef"`
	DryRun     bool   `json:"dryRun,omitempty"`
}

// PayrollImportIssue is a rejected row.
type PayrollImportIssue struct {
	Row     int    `json:"row"`
	Key     string `json:"key"`
	Message string `json:"message"`
}

// PayrollImportReport reports an import.
type PayrollImportReport struct {
	Kind     string               `json:"kind"`
	Rows     int                  `json:"rows"`
	Inserted int                  `json:"inserted"`
	Updated  int                  `json:"updated"`
	Failed   int                  `json:"failed"`
	DryRun   bool                 `json:"dryRun"`
	Issues   []PayrollImportIssue `json:"issues"`
}

// PayrollYTD is the year to date of an employee.
type PayrollYTD struct {
	EmployeeID     uuid.UUID `json:"employeeId" db:"employee_id"`
	EmployeeNo     string    `json:"employeeNo" db:"employee_no"`
	FullName       string    `json:"fullName" db:"full_name"`
	TaxYear        int       `json:"taxYear" db:"tax_year"`
	OpeningThrough *int      `json:"openingThroughMonth" db:"through_month"`
	OpeningGross   string    `json:"openingGross" db:"opening_gross"`
	OpeningPPh21   string    `json:"openingPph21" db:"opening_pph21"`
	Gross          string    `json:"gross" db:"gross" doc:"Taxable gross of the OneClub runs of the year"`
	PPh21          string    `json:"pph21" db:"pph21"`
	BPJSEmployee   string    `json:"bpjsEmployee" db:"bpjs_employee"`
	Net            string    `json:"net" db:"net"`
	TotalGross     string    `json:"totalGross" db:"total_gross"`
	TotalPPh21     string    `json:"totalPph21" db:"total_pph21"`
}

// PayrollProfileView is the payroll readiness of an employee (Benefits).
type PayrollProfileView struct {
	EmployeeID            uuid.UUID `json:"employeeId" db:"id"`
	EmployeeNo            string    `json:"employeeNo" db:"employee_no"`
	FullName              string    `json:"fullName" db:"full_name"`
	OrgUnitName           *string   `json:"orgUnitName" db:"org_unit_name"`
	EmploymentStatus      string    `json:"employmentStatus" db:"employment_status"`
	WorkerCategory        string    `json:"workerCategory" db:"worker_category"`
	PTKPStatus            *string   `json:"ptkpStatus" db:"ptkp_status"`
	TERCategory           string    `json:"terCategory" db:"-"`
	NPWP                  *string   `json:"npwp" db:"npwp" doc:"Masked unless the viewer may see sensitive data"`
	NIK                   *string   `json:"nik" db:"nik" doc:"Masked unless the viewer may see sensitive data"`
	BPJSKesehatanNo       *string   `json:"bpjsKesehatanNo" db:"bpjs_kesehatan_no"`
	BPJSKetenagakerjaanNo *string   `json:"bpjsKetenagakerjaanNo" db:"bpjs_ketenagakerjaan_no"`
	BankName              *string   `json:"bankName" db:"bank_name"`
	AccountNo             *string   `json:"accountNo" db:"account_no"`
	BPJSEnrolled          bool      `json:"bpjsEnrolled" db:"-" doc:"Contributions are calculated (worker category not excluded)"`
	Issues                []string  `json:"issues" db:"-" doc:"Missing data that blocks or affects payroll"`
}

func (m *Module) registerImports(reg *route.Registry) {
	tag := "HRIS Payroll"
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/payroll-imports",
		Summary: "Import the opening year-to-date payroll or the legacy payroll of a parallel run (CSV)", Permission: PermImport,
		Request: PayrollImportRequest{}, Response: PayrollImportReport{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.importHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/payroll-ytd", Summary: "Year-to-date payroll per employee (opening + OneClub runs)",
		Permission: PermRunView, Response: PayrollYTD{}, List: true, Query: []route.Param{{Name: "year", Required: true}}, Handler: listRead(m.DB, m.ytdHTTP)})
}

func (m *Module) registerProfiles(reg *route.Registry) {
	add(reg, "HRIS Payroll", route.Route{Method: http.MethodGet, Path: "/api/v1/hris/payroll-profiles",
		Summary: "Payroll profiles: PTKP, TER category, NPWP, BPJS and bank readiness (Benefits)", Permission: PermProfileView, Response: PayrollProfileView{},
		List: true, Query: []route.Param{{Name: "issues"}}, Handler: listRead(m.DB, m.profilesHTTP)})
}

func (m *Module) importHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request, req PayrollImportRequest) (PayrollImportReport, error) {
	property := handle.Property(ctx)
	rep, err := m.importCSV(ctx, tx, property, req.Kind, req.PeriodCode, strings.NewReader(req.CSV), req.DryRun)
	if err != nil {
		return rep, err
	}
	return rep, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionImport, EntityType: "hris.payroll_import",
		EntityLabel: "Payroll import " + req.Kind, PropertyID: &property, After: map[string]any{"kind": req.Kind, "rows": rep.Rows, "inserted": rep.Inserted,
			"updated": rep.Updated, "failed": rep.Failed, "dryRun": rep.DryRun}})
}

// ImportCSV runs an import outside a request (CLI).
func (m *Module) ImportCSV(ctx context.Context, property uuid.UUID, kind, period string, src io.Reader, dryRun bool) (PayrollImportReport, error) {
	var rep PayrollImportReport
	err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		if rep, err = m.importCSV(ctx, tx, property, kind, period, src, dryRun); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionImport, EntityType: "hris.payroll_import",
			EntityLabel: "Payroll import " + kind + " (CLI)", PropertyID: &property, After: map[string]any{"kind": kind, "rows": rep.Rows,
				"inserted": rep.Inserted, "updated": rep.Updated, "failed": rep.Failed, "dryRun": rep.DryRun}})
	})
	return rep, err
}

func (m *Module) importCSV(ctx context.Context, tx pgx.Tx, property uuid.UUID, kind, period string, src io.Reader, dryRun bool) (PayrollImportReport, error) {
	rep := PayrollImportReport{Kind: kind, DryRun: dryRun, Issues: []PayrollImportIssue{}}
	cols, ok := ImportColumns[kind]
	if !ok {
		return rep, enumErr("kind", []string{ImportYTD, ImportLegacy})
	}
	rd := csv.NewReader(src)
	rd.FieldsPerRecord, rd.TrimLeadingSpace = -1, true
	records, err := rd.ReadAll()
	if err != nil {
		return rep, handle.Invalid("csv", "invalid", "malformed CSV: "+err.Error())
	}
	if len(records) < 2 {
		return rep, handle.Invalid("csv", "empty", "a header row and at least one data row are required")
	}
	col := map[string]int{}
	for i, h := range records[0] {
		h = strings.TrimSpace(strings.TrimPrefix(h, "\ufeff"))
		if !oneOf(cols, h) {
			return rep, handle.Invalid("csv", "unknown_column", "unknown column "+h+"; columns: "+strings.Join(cols, ", "))
		}
		col[h] = i
	}
	required := []string{"employeeNo", "taxYear", "throughMonth"}
	if kind == ImportLegacy {
		required = []string{"employeeNo", "componentCode", "amount"}
		if _, ok := col["periodCode"]; !ok {
			if _, err := parsePeriod("periodCode", period); err != nil {
				return rep, handle.Invalid("periodCode", "required", "name the period (YYYY-MM) or add a periodCode column")
			}
		}
	}
	for _, c := range required {
		if _, ok := col[c]; !ok {
			return rep, handle.Invalid("csv", "missing_column", "column "+c+" is required")
		}
	}
	outer, err := tx.Begin(ctx)
	if err != nil {
		return rep, err
	}
	uid := actor(ctx)
	for i, rec := range records[1:] {
		get := func(c string) string {
			if j, ok := col[c]; ok && j < len(rec) {
				return strings.TrimSpace(rec[j])
			}
			return ""
		}
		rep.Rows++
		row := i + 2
		fail := func(msg string) {
			rep.Failed++
			rep.Issues = append(rep.Issues, PayrollImportIssue{Row: row, Key: get("employeeNo"), Message: msg})
		}
		e, err := hris.EmployeeByNo(ctx, outer, property, get("employeeNo"))
		if err != nil {
			_ = outer.Rollback(ctx)
			return rep, err
		}
		if e == nil {
			fail("unknown employee number")
			continue
		}
		amount := func(c string) (decimal.Decimal, bool) {
			v := get(c)
			if v == "" {
				return decimal.Zero, true
			}
			d, err := decimal.NewFromString(strings.ReplaceAll(v, ",", ""))
			return d, err == nil
		}
		sp, err := outer.Begin(ctx)
		if err != nil {
			_ = outer.Rollback(ctx)
			return rep, err
		}
		var inserted bool
		switch kind {
		case ImportYTD:
			year, err1 := strconv.Atoi(get("taxYear"))
			month, err2 := strconv.Atoi(get("throughMonth"))
			gross, ok1 := amount("gross")
			ded, ok2 := amount("deductible")
			tax, ok3 := amount("pph21")
			months := month
			if v := get("monthsWorked"); v != "" {
				months, err = strconv.Atoi(v)
			}
			if err1 != nil || err2 != nil || err != nil || month < 1 || month > 12 || year < 2000 || !ok1 || !ok2 || !ok3 || months < 0 || months > 12 {
				_ = sp.Rollback(ctx)
				fail("taxYear, throughMonth (1–12), monthsWorked and the amounts must be numbers")
				continue
			}
			err = sp.QueryRow(ctx, `INSERT INTO hris.payroll_ytd (id, property_id, employee_id, tax_year, through_month, gross, deductible, pph21, months_worked,
				legacy_ref, imported_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
				ON CONFLICT (employee_id, tax_year) DO UPDATE SET through_month = EXCLUDED.through_month, gross = EXCLUDED.gross,
				  deductible = EXCLUDED.deductible, pph21 = EXCLUDED.pph21, months_worked = EXCLUDED.months_worked, legacy_ref = EXCLUDED.legacy_ref,
				  imported_at = now(), imported_by = EXCLUDED.imported_by
				RETURNING (xmax = 0)`, id.New(), property, e.ID, year, month, gross, ded, tax, months, nullStr(get("legacyRef")), uid).Scan(&inserted)
		case ImportLegacy:
			p := get("periodCode")
			if p == "" {
				p = period
			}
			pc, perr := parsePeriod("periodCode", p)
			amt, ok := amount("amount")
			code := strings.ToUpper(get("componentCode"))
			if perr != nil || !ok || !componentCodeRe.MatchString(code) {
				_ = sp.Rollback(ctx)
				fail("periodCode (YYYY-MM), componentCode and amount are required")
				continue
			}
			err = sp.QueryRow(ctx, `INSERT INTO hris.legacy_payroll_lines (id, property_id, period_code, employee_id, component_code, amount, batch_ref,
				imported_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
				ON CONFLICT (property_id, period_code, employee_id, component_code) DO UPDATE SET amount = EXCLUDED.amount, batch_ref = EXCLUDED.batch_ref,
				  imported_at = now(), imported_by = EXCLUDED.imported_by
				RETURNING (xmax = 0)`, id.New(), property, periodCode(pc), e.ID, code, amt, nullStr(get("batchRef")), uid).Scan(&inserted)
		}
		if err != nil {
			_ = sp.Rollback(ctx)
			fail(err.Error())
			continue
		}
		if err := sp.Commit(ctx); err != nil {
			_ = outer.Rollback(ctx)
			return rep, err
		}
		if inserted {
			rep.Inserted++
		} else {
			rep.Updated++
		}
	}
	if dryRun {
		return rep, outer.Rollback(ctx)
	}
	return rep, outer.Commit(ctx)
}

func (m *Module) ytdHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]PayrollYTD, error) {
	year, err := strconv.Atoi(r.URL.Query().Get("year"))
	if err != nil || year < 2000 || year > 2100 {
		return nil, handle.Invalid("year", "invalid", "a tax year, e.g. 2026")
	}
	return handle.List[PayrollYTD](tx.Query(ctx, `WITH runs AS (
		  SELECT s.employee_id, sum(s.taxable_gross) AS gross, sum(s.pph21 + s.pph21_final) AS pph21, sum(s.bpjs_employee) AS bpjs_employee, sum(s.net) AS net
		  FROM hris.payroll_slips s JOIN hris.payroll_runs r ON r.id = s.run_id
		  WHERE r.property_id = $1 AND left(r.period_code, 4) = $2::text AND r.status IN ('approved', 'posted', 'paid') GROUP BY 1)
		SELECT e.id AS employee_id, e.employee_no, e.full_name, $2::int AS tax_year, y.through_month, coalesce(y.gross, 0)::text AS opening_gross,
		  coalesce(y.pph21, 0)::text AS opening_pph21, coalesce(x.gross, 0)::text AS gross, coalesce(x.pph21, 0)::text AS pph21,
		  coalesce(x.bpjs_employee, 0)::text AS bpjs_employee, coalesce(x.net, 0)::text AS net,
		  (coalesce(y.gross, 0) + coalesce(x.gross, 0))::text AS total_gross, (coalesce(y.pph21, 0) + coalesce(x.pph21, 0))::text AS total_pph21
		FROM hris.employees e LEFT JOIN runs x ON x.employee_id = e.id
		LEFT JOIN hris.payroll_ytd y ON y.employee_id = e.id AND y.tax_year = $2::int
		WHERE e.property_id = $1 AND (x.employee_id IS NOT NULL OR y.employee_id IS NOT NULL) ORDER BY e.full_name`, handle.Property(ctx), strconv.Itoa(year)))
}

func (m *Module) profilesHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]PayrollProfileView, error) {
	property := handle.Property(ctx)
	day := today(ctx, tx, property)
	rates, _, err := statutoryRatesAt(ctx, tx, property, day)
	if err != nil {
		return nil, err
	}
	pp, _, err := LoadProcessing(ctx, tx, property, hris.PolicyTime(day))
	if err != nil {
		return nil, err
	}
	out, err := handle.List[PayrollProfileView](tx.Query(ctx, `SELECT e.id, e.employee_no, e.full_name, ou.name AS org_unit_name, e.employment_status,
		e.worker_category, e.ptkp_status, e.npwp, e.nik, e.bpjs_kesehatan_no, e.bpjs_ketenagakerjaan_no, b.bank_name, b.account_no
		FROM hris.employees e LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id
		LEFT JOIN LATERAL (SELECT bank_name, account_no FROM hris.employee_bank_accounts WHERE employee_id = e.id AND status = 'active' AND archived_at IS NULL
		  ORDER BY is_primary DESC, created_at LIMIT 1) b ON true
		WHERE e.property_id = $1 AND e.archived_at IS NULL AND e.status = 'active' AND (e.termination_date IS NULL OR e.termination_date > $2::date)
		ORDER BY e.full_name`, property, ymd(day)))
	if err != nil {
		return nil, err
	}
	show := can(ctx, "hris.employee.view_sensitive", property)
	onlyIssues := filterParam(r, "issues") == "true"
	kept := out[:0]
	for _, p := range out {
		p.Issues = []string{}
		p.TERCategory = rates.TERCategory(deref(p.PTKPStatus))
		p.BPJSEnrolled = !oneOf(pp.BPJSExcludedCategories, p.WorkerCategory)
		if p.PTKPStatus == nil {
			p.Issues = append(p.Issues, "PTKP status missing (TK/0 applied)")
		}
		if strings.TrimSpace(deref(p.NPWP)) == "" && strings.TrimSpace(deref(p.NIK)) == "" {
			p.Issues = append(p.Issues, "No NPWP / NIK: PPh 21 +"+rates.NonNPWPSurchargePercent+"%")
		}
		if p.BPJSEnrolled && (p.BPJSKesehatanNo == nil || p.BPJSKetenagakerjaanNo == nil) {
			p.Issues = append(p.Issues, "BPJS number missing")
		}
		if p.AccountNo == nil {
			p.Issues = append(p.Issues, "No bank account: excluded from the bank file")
		}
		p.NPWP, p.NIK, p.AccountNo = maskPtr(p.NPWP, show), maskPtr(p.NIK, show), maskPtr(p.AccountNo, show)
		p.BPJSKesehatanNo, p.BPJSKetenagakerjaanNo = maskPtr(p.BPJSKesehatanNo, show), maskPtr(p.BPJSKetenagakerjaanNo, show)
		if onlyIssues && len(p.Issues) == 0 {
			continue
		}
		kept = append(kept, p)
	}
	return kept, nil
}
