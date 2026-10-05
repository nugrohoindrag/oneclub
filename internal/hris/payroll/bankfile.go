package payroll

// Bank transfer file (FR-PPY-02, FR-INT-P5-04), simulation comparison with
// the previous period (FR-PAY-08) and the parallel run against the legacy
// payroll with its sign-off (EP-28 FR-MIG-P5-05/06, EP-29).

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// BankTransfer is one transfer of a bank file.
type BankTransfer struct {
	Sequence    int
	EmployeeNo  string
	FullName    string
	BankCode    string
	BankName    string
	AccountNo   string
	AccountName string
	Amount      decimal.Decimal
}

// BankFileMeta is the header data of a bank file.
type BankFileMeta struct {
	Currency, Reference, Remark, CompanyCode, DebitAccount string
	PaymentDate                                            time.Time
}

func pad(s string, c BankColumn) string {
	if c.Width <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) > c.Width {
		return string(r[:c.Width])
	}
	p := c.Pad
	if p == "" {
		p = " "
	}
	fill := strings.Repeat(p, c.Width-len(r))
	if c.Align == "right" {
		return fill + s
	}
	return s + fill
}

func field(c BankColumn, l BankLayout, meta BankFileMeta, t *BankTransfer, count int, total decimal.Decimal) string {
	if lit, ok := strings.CutPrefix(c.Field, "literal:"); ok {
		return lit
	}
	df := l.DateFormat
	if df == "" {
		df = "2006-01-02"
	}
	switch c.Field {
	case "currency":
		return meta.Currency
	case "paymentDate":
		return meta.PaymentDate.Format(df)
	case "reference":
		return meta.Reference
	case "remark":
		return meta.Remark
	case "companyCode":
		return meta.CompanyCode
	case "debitAccount":
		return meta.DebitAccount
	case "count":
		return strconv.Itoa(count)
	case "total":
		return total.Round(0).String()
	case "totalCents":
		return total.Mul(decimal.NewFromInt(100)).Round(0).String()
	}
	if t == nil {
		return ""
	}
	switch c.Field {
	case "sequence":
		return strconv.Itoa(t.Sequence)
	case "employeeNo":
		return t.EmployeeNo
	case "fullName":
		return t.FullName
	case "bankCode":
		return t.BankCode
	case "bankName":
		return t.BankName
	case "accountNo":
		return t.AccountNo
	case "accountName":
		return t.AccountName
	case "amount":
		return t.Amount.Round(0).String()
	case "amountCents":
		return t.Amount.Mul(decimal.NewFromInt(100)).Round(0).String()
	}
	return ""
}

// RenderBankFile renders transfers with a layout (pure; unit tested).
func RenderBankFile(l BankLayout, meta BankFileMeta, transfers []BankTransfer) ([]byte, error) {
	total := decimal.Zero
	for _, t := range transfers {
		total = total.Add(t.Amount)
	}
	n := len(transfers)
	var buf bytes.Buffer
	if l.Format == "fixed" {
		record := func(cols []BankColumn, t *BankTransfer) {
			for _, c := range cols {
				buf.WriteString(pad(field(c, l, meta, t, n, total), c))
			}
			buf.WriteString("\r\n")
		}
		if len(l.Header) > 0 {
			record(l.Header, nil)
		}
		for i := range transfers {
			record(l.Columns, &transfers[i])
		}
		if len(l.Trailer) > 0 {
			record(l.Trailer, nil)
		}
		return buf.Bytes(), nil
	}
	w := csv.NewWriter(&buf)
	if l.Delimiter != "" {
		w.Comma = []rune(l.Delimiter)[0]
	}
	if l.HeaderRow {
		var h []string
		for _, c := range l.Columns {
			h = append(h, c.Label)
		}
		if err := w.Write(h); err != nil {
			return nil, err
		}
	}
	for i := range transfers {
		var row []string
		for _, c := range l.Columns {
			row = append(row, field(c, l, meta, &transfers[i], n, total))
		}
		if err := w.Write(row); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

// bankFileHTTP serves the bank file of a posted / paid run (approved runs
// as a preview) and records the download.
func (m *Module) bankFileHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rid, err := handle.ID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var out []byte
	var name string
	err = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		property := handle.Property(ctx)
		run, err := m.inProperty(ctx, tx, rid, property)
		if err != nil {
			return err
		}
		if !oneOf([]string{hris.RunApproved, hris.RunPosted, hris.RunPaid}, run.Status) {
			return errs.Conflict("run_not_approved", "the bank file is available once the run is approved")
		}
		pp, _, err := LoadProcessing(ctx, tx, property, hris.PolicyTime(run.PeriodEnd))
		if err != nil {
			return err
		}
		layout, ok := pp.layout(r.URL.Query().Get("layout"))
		if !ok {
			return handle.Invalid("layout", "unknown_layout", "unknown bank file layout")
		}
		rows, err := tx.Query(ctx, `SELECT employee_no, full_name, coalesce(bank_code, ''), coalesce(bank_name, ''), coalesce(account_no, ''),
			coalesce(account_name, full_name), net FROM hris.payroll_slips WHERE run_id = $1 AND net > 0 AND account_no IS NOT NULL
			AND ($2 = '' OR upper(coalesce(bank_code, bank_name, '')) = upper($2)) ORDER BY employee_no`, rid, layout.Bank)
		if err != nil {
			return err
		}
		var ts []BankTransfer
		for rows.Next() {
			var t BankTransfer
			if err := rows.Scan(&t.EmployeeNo, &t.FullName, &t.BankCode, &t.BankName, &t.AccountNo, &t.AccountName, &t.Amount); err != nil {
				rows.Close()
				return err
			}
			t.Sequence = len(ts) + 1
			ts = append(ts, t)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		out, err = RenderBankFile(layout, BankFileMeta{Currency: run.Currency, Reference: run.Number, Remark: "GAJI " + run.PeriodCode,
			CompanyCode: pp.CompanyCode, DebitAccount: pp.DebitAccount, PaymentDate: run.PaymentDate}, ts)
		if err != nil {
			return err
		}
		ext := ".csv"
		if layout.Format == "fixed" {
			ext = ".txt"
		}
		name = run.Number + "-" + layout.Code + ext
		if _, err := tx.Exec(ctx, `UPDATE hris.payroll_runs SET bank_file_count = bank_file_count + 1, bank_file_at = now() WHERE id = $1`, rid); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "bank_file", EntityType: "hris.payroll_run", EntityID: rid.String(),
			EntityLabel: run.Number, PropertyID: &property, Metadata: map[string]any{"layout": layout.Code, "transfers": len(ts)}})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = w.Write(out)
}

// ── comparison with the previous period (FR-PAY-08) ──────────────────────

// PayrollComparisonRow compares an employee with the previous period.
type PayrollComparisonRow struct {
	EmployeeID    uuid.UUID `json:"employeeId"`
	EmployeeNo    string    `json:"employeeNo"`
	FullName      string    `json:"fullName"`
	OrgUnitName   string    `json:"orgUnitName"`
	PreviousGross string    `json:"previousGross"`
	Gross         string    `json:"gross"`
	GrossDiff     string    `json:"grossDiff"`
	PreviousNet   string    `json:"previousNet"`
	Net           string    `json:"net"`
	NetDiff       string    `json:"netDiff"`
	ChangePercent string    `json:"changePercent"`
	Flag          string    `json:"flag" enum:"new,left,changed,same" doc:"changed: net differs by more than 10%"`
}

// PayrollComparison is the simulation check of a run.
type PayrollComparison struct {
	RunID          uuid.UUID              `json:"runId"`
	PreviousRunID  *uuid.UUID             `json:"previousRunId"`
	PreviousNumber string                 `json:"previousNumber"`
	PreviousPeriod string                 `json:"previousPeriod"`
	Gross          string                 `json:"gross"`
	PreviousGross  string                 `json:"previousGross"`
	Net            string                 `json:"net"`
	PreviousNet    string                 `json:"previousNet"`
	Headcount      int                    `json:"headcount"`
	PreviousCount  int                    `json:"previousHeadcount"`
	Rows           []PayrollComparisonRow `json:"rows"`
}

type slipSum struct {
	no, name, unit string
	gross, net     decimal.Decimal
}

func slipSums(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (map[uuid.UUID]slipSum, error) {
	rows, err := tx.Query(ctx, `SELECT employee_id, employee_no, full_name, coalesce(org_unit_name, ''), gross, net FROM hris.payroll_slips WHERE run_id = $1`, rid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]slipSum{}
	for rows.Next() {
		var eid uuid.UUID
		var s slipSum
		if err := rows.Scan(&eid, &s.no, &s.name, &s.unit, &s.gross, &s.net); err != nil {
			return nil, err
		}
		out[eid] = s
	}
	return out, rows.Err()
}

func (m *Module) comparisonHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (PayrollComparison, error) {
	property := handle.Property(ctx)
	rid, err := handle.ID(r)
	if err != nil {
		return PayrollComparison{}, err
	}
	run, err := m.inProperty(ctx, tx, rid, property)
	if err != nil {
		return PayrollComparison{}, err
	}
	out := PayrollComparison{RunID: rid, Rows: []PayrollComparisonRow{}}
	var prev uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id, number, period_code FROM hris.payroll_runs WHERE property_id = $1 AND run_type = $2 AND period_code < $3
		AND status IN ('approved', 'posted', 'paid') ORDER BY period_code DESC, created_at DESC LIMIT 1`, property, run.RunType, run.PeriodCode).
		Scan(&prev, &out.PreviousNumber, &out.PreviousPeriod)
	if err != nil && !strings.Contains(err.Error(), "no rows") {
		return out, err
	}
	cur, err := slipSums(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	old := map[uuid.UUID]slipSum{}
	if prev != uuid.Nil {
		out.PreviousRunID = &prev
		if old, err = slipSums(ctx, tx, prev); err != nil {
			return out, err
		}
	}
	tg, tn, pg, pn := decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero
	ids := map[uuid.UUID]bool{}
	for k := range cur {
		ids[k] = true
	}
	for k := range old {
		ids[k] = true
	}
	for eid := range ids {
		c, inCur := cur[eid]
		o, inOld := old[eid]
		row := PayrollComparisonRow{EmployeeID: eid, PreviousGross: money(o.gross), Gross: money(c.gross), PreviousNet: money(o.net), Net: money(c.net)}
		row.EmployeeNo, row.FullName, row.OrgUnitName = c.no, c.name, c.unit
		if !inCur {
			row.EmployeeNo, row.FullName, row.OrgUnitName = o.no, o.name, o.unit
		}
		row.GrossDiff, row.NetDiff = money(c.gross.Sub(o.gross)), money(c.net.Sub(o.net))
		switch {
		case !inOld:
			row.Flag = "new"
		case !inCur:
			row.Flag = "left"
		default:
			row.Flag = "same"
			if o.net.IsPositive() {
				pct := c.net.Sub(o.net).Mul(decimal.NewFromInt(100)).Div(o.net).Round(2)
				row.ChangePercent = pct.String()
				if pct.Abs().GreaterThan(decimal.NewFromInt(10)) {
					row.Flag = "changed"
				}
			}
		}
		tg, tn, pg, pn = tg.Add(c.gross), tn.Add(c.net), pg.Add(o.gross), pn.Add(o.net)
		out.Rows = append(out.Rows, row)
	}
	sort.Slice(out.Rows, func(i, j int) bool { return out.Rows[i].FullName < out.Rows[j].FullName })
	out.Gross, out.Net, out.PreviousGross, out.PreviousNet = money(tg), money(tn), money(pg), money(pn)
	out.Headcount, out.PreviousCount = len(cur), len(old)
	return out, nil
}

// ── parallel run (EP-28 FR-MIG-P5-05/06, EP-29) ──────────────────────────

// ParallelRunRow is one employee and component compared.
type ParallelRunRow struct {
	EmployeeID    uuid.UUID `json:"employeeId"`
	EmployeeNo    string    `json:"employeeNo"`
	FullName      string    `json:"fullName"`
	ComponentCode string    `json:"componentCode" doc:"A pay component, or the totals GROSS, BPJS_EE, PPH21, NET"`
	OneClub       string    `json:"oneClub"`
	Legacy        string    `json:"legacy"`
	Difference    string    `json:"difference"`
}

// ParallelRun compares a run with the imported legacy payroll of its period.
type ParallelRun struct {
	RunID            uuid.UUID        `json:"runId"`
	PeriodCode       string           `json:"periodCode"`
	Employees        int              `json:"employees"`
	Matched          int              `json:"matched" doc:"Employees without any difference"`
	WithDifferences  int              `json:"withDifferences"`
	MissingInLegacy  int              `json:"missingInLegacy"`
	MissingInOneClub int              `json:"missingInOneClub"`
	NetOneClub       string           `json:"netOneClub"`
	NetLegacy        string           `json:"netLegacy"`
	Rows             []ParallelRunRow `json:"rows" doc:"Rows with a difference (all rows with ?all=true)"`
	SignOffs         []PayrollSignOff `json:"signOffs"`
}

// totalCodes are the summary codes compared besides the components.
var totalCodes = []string{"GROSS", "BPJS_EE", "PPH21", "NET"}

// ParallelRunOf compares the run with the legacy lines (also used by the
// Parallel Run Report).
func ParallelRunOf(ctx context.Context, tx pgx.Tx, rid uuid.UUID, all bool) (ParallelRun, error) {
	var out ParallelRun
	var property uuid.UUID
	var signoffs []byte
	if err := tx.QueryRow(ctx, `SELECT property_id, period_code, parallel_signoffs FROM hris.payroll_runs WHERE id = $1`, rid).
		Scan(&property, &out.PeriodCode, &signoffs); err != nil {
		return out, errs.NotFound("payroll run")
	}
	out.RunID, out.Rows, out.SignOffs = rid, []ParallelRunRow{}, []PayrollSignOff{}
	_ = json.Unmarshal(signoffs, &out.SignOffs)
	type key struct {
		e    uuid.UUID
		code string
	}
	one, leg := map[key]decimal.Decimal{}, map[key]decimal.Decimal{}
	names := map[uuid.UUID][2]string{}
	inOne, inLeg := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
	rows, err := tx.Query(ctx, `SELECT s.employee_id, s.employee_no, s.full_name, l.code, sum(l.amount) FROM hris.payroll_lines l
		JOIN hris.payroll_slips s ON s.id = l.slip_id WHERE l.run_id = $1 AND l.kind IN ('earning', 'deduction') GROUP BY 1, 2, 3, 4
		UNION ALL SELECT employee_id, employee_no, full_name, x.code, x.amount FROM hris.payroll_slips,
		  LATERAL (VALUES ('GROSS', gross), ('BPJS_EE', bpjs_employee), ('PPH21', pph21 + pph21_final), ('NET', net)) x(code, amount) WHERE run_id = $1`, rid)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var e uuid.UUID
		var no, name, code string
		var amt decimal.Decimal
		if err := rows.Scan(&e, &no, &name, &code, &amt); err != nil {
			rows.Close()
			return out, err
		}
		one[key{e, code}] = one[key{e, code}].Add(amt)
		names[e], inOne[e] = [2]string{no, name}, true
	}
	rows.Close()
	lrows, err := tx.Query(ctx, `SELECT l.employee_id, e.employee_no, e.full_name, l.component_code, l.amount FROM hris.legacy_payroll_lines l
		JOIN hris.employees e ON e.id = l.employee_id WHERE l.property_id = $1 AND l.period_code = $2`, property, out.PeriodCode)
	if err != nil {
		return out, err
	}
	for lrows.Next() {
		var e uuid.UUID
		var no, name, code string
		var amt decimal.Decimal
		if err := lrows.Scan(&e, &no, &name, &code, &amt); err != nil {
			lrows.Close()
			return out, err
		}
		leg[key{e, code}] = leg[key{e, code}].Add(amt)
		names[e], inLeg[e] = [2]string{no, name}, true
	}
	lrows.Close()
	keys := map[key]bool{}
	for k := range one {
		keys[k] = true
	}
	for k := range leg {
		keys[k] = true
	}
	diffEmp := map[uuid.UUID]bool{}
	var ks []key
	for k := range keys {
		// components missing on one side are compared only when the legacy
		// file has lines of that employee
		if _, ok := leg[k]; !ok && !oneOf(totalCodes, k.code) && !inLeg[k.e] {
			continue
		}
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool {
		if names[ks[i].e][1] != names[ks[j].e][1] {
			return names[ks[i].e][1] < names[ks[j].e][1]
		}
		return ks[i].code < ks[j].code
	})
	netOne, netLeg := decimal.Zero, decimal.Zero
	for _, k := range ks {
		if !inOne[k.e] || !inLeg[k.e] {
			continue
		}
		d := one[k].Sub(leg[k])
		if k.code == "NET" {
			netOne, netLeg = netOne.Add(one[k]), netLeg.Add(leg[k])
		}
		if !d.IsZero() {
			diffEmp[k.e] = true
		}
		if all || !d.IsZero() {
			out.Rows = append(out.Rows, ParallelRunRow{EmployeeID: k.e, EmployeeNo: names[k.e][0], FullName: names[k.e][1], ComponentCode: k.code,
				OneClub: money(one[k]), Legacy: money(leg[k]), Difference: money(d)})
		}
	}
	for e := range inOne {
		if !inLeg[e] {
			out.MissingInLegacy++
			continue
		}
		out.Employees++
		if diffEmp[e] {
			out.WithDifferences++
		} else {
			out.Matched++
		}
	}
	for e := range inLeg {
		if !inOne[e] {
			out.MissingInOneClub++
		}
	}
	out.NetOneClub, out.NetLegacy = money(netOne), money(netLeg)
	return out, nil
}

func (m *Module) parallelRunHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (ParallelRun, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return ParallelRun{}, err
	}
	if _, err := m.inProperty(ctx, tx, rid, handle.Property(ctx)); err != nil {
		return ParallelRun{}, err
	}
	return ParallelRunOf(ctx, tx, rid, r.URL.Query().Get("all") == "true")
}

func (m *Module) signOffHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req PayrollSignOffInput) (PayrollRun, error) {
	property := handle.Property(ctx)
	rid, err := handle.ID(r)
	if err != nil {
		return PayrollRun{}, err
	}
	run, err := loadRun(ctx, tx, rid, true)
	if err != nil || run.PropertyID != property {
		return PayrollRun{}, errs.NotFound("payroll run")
	}
	if !oneOf([]string{"hr_manager", "finance_manager"}, req.Capacity) {
		return PayrollRun{}, enumErr("capacity", []string{"hr_manager", "finance_manager"})
	}
	if run.Status == hris.RunDraft || run.Status == hris.RunCancelled {
		return PayrollRun{}, errs.Conflict("run_not_calculated", "sign off a calculated run")
	}
	pr, err := ParallelRunOf(ctx, tx, rid, false)
	if err != nil {
		return PayrollRun{}, err
	}
	if pr.Employees == 0 {
		return PayrollRun{}, errs.Conflict("no_legacy_payroll", "import the legacy payroll of "+run.PeriodCode+" first")
	}
	if pr.WithDifferences > 0 && strings.TrimSpace(req.Note) == "" {
		return PayrollRun{}, handle.Invalid("note", "required", "explain the "+itoa(pr.WithDifferences)+" employee(s) with differences")
	}
	uid := handle.UserID(ctx)
	var name string
	_ = tx.QueryRow(ctx, `SELECT coalesce(full_name, email) FROM platform.users WHERE id = $1`, uid).Scan(&name)
	var list []PayrollSignOff
	for _, s := range pr.SignOffs {
		if s.Capacity == req.Capacity {
			continue
		}
		if s.UserID == uid {
			return PayrollRun{}, errs.Conflict("same_person", "HR Manager and Finance Manager sign off as two different people")
		}
		list = append(list, s)
	}
	list = append(list, PayrollSignOff{UserID: uid, UserName: name, Capacity: req.Capacity, Note: strings.TrimSpace(req.Note), SignedAt: clock.Now().UTC()})
	raw, _ := json.Marshal(list)
	if _, err := tx.Exec(ctx, `UPDATE hris.payroll_runs SET parallel_signoffs = $2 WHERE id = $1`, rid, raw); err != nil {
		return PayrollRun{}, err
	}
	out, err := m.run(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "parallel_run_sign_off", EntityType: "hris.payroll_run", EntityID: rid.String(),
		EntityLabel: out.Number, PropertyID: &property, Reason: req.Note, After: map[string]any{"capacity": req.Capacity, "matched": pr.Matched,
			"withDifferences": pr.WithDifferences}})
}

func jsonOf(v any) ([]byte, error) { return json.Marshal(v) }
