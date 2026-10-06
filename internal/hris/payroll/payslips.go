package payroll

// Payslips (PRD P5 FR-PPY-03, FR-ESS-04, FR-OPS-P5-05): the payslip lines
// of a run for payroll roles, the PDF (`GET /hris/payslips/{id}/pdf`) and
// Employee Self Service → Payslip, where an employee sees only their own
// payslips of posted runs (identity numbers and the account number masked;
// responses are never cached: Cache-Control no-store).

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/pdf"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/org"
)

// PayrollSlipLine is a line of a payslip.
type PayrollSlipLine struct {
	LineNo      int     `json:"lineNo" db:"line_no"`
	Code        string  `json:"code" db:"code"`
	Name        string  `json:"name" db:"name"`
	Kind        string  `json:"kind" db:"kind" enum:"earning,deduction,bpjs_employee,bpjs_employer,tax"`
	Category    string  `json:"category" db:"category"`
	Programme   *string `json:"programme" db:"programme"`
	Taxable     bool    `json:"taxable" db:"taxable"`
	Irregular   bool    `json:"irregular" db:"irregular"`
	PreTax      bool    `json:"preTax" db:"pre_tax"`
	Quantity    string  `json:"quantity" db:"quantity"`
	Rate        string  `json:"rate" db:"rate"`
	Amount      string  `json:"amount" db:"amount"`
	Source      string  `json:"source" db:"source"`
	SourceType  *string `json:"sourceType" db:"source_type"`
	SourceID    *string `json:"sourceId" db:"source_id"`
	Description *string `json:"description" db:"description"`
}

// PayrollSlip is the payslip of an employee in a run.
type PayrollSlip struct {
	ID              uuid.UUID              `json:"id" db:"id"`
	RunID           uuid.UUID              `json:"runId" db:"run_id"`
	RunNumber       string                 `json:"runNumber" db:"run_number"`
	RunName         string                 `json:"runName" db:"run_name"`
	RunType         string                 `json:"runType" db:"run_type"`
	RunStatus       string                 `json:"runStatus" db:"run_status"`
	PeriodCode      string                 `json:"periodCode" db:"period_code"`
	PeriodStart     time.Time              `json:"periodStart" db:"period_start"`
	PeriodEnd       time.Time              `json:"periodEnd" db:"period_end"`
	PaymentDate     time.Time              `json:"paymentDate" db:"payment_date"`
	EmployeeID      uuid.UUID              `json:"employeeId" db:"employee_id"`
	EmployeeNo      string                 `json:"employeeNo" db:"employee_no"`
	FullName        string                 `json:"fullName" db:"full_name"`
	OrgUnitName     *string                `json:"orgUnitName" db:"org_unit_name"`
	PositionName    *string                `json:"positionName" db:"position_name"`
	GradeCode       *string                `json:"gradeCode" db:"grade_code"`
	CostCenter      *string                `json:"costCenter" db:"cost_center"`
	PTKPStatus      *string                `json:"ptkpStatus" db:"ptkp_status"`
	TERCategory     *string                `json:"terCategory" db:"ter_category"`
	TaxMethod       string                 `json:"taxMethod" db:"tax_method" enum:"ter,annual,none"`
	TERRate         string                 `json:"terRate" db:"ter_rate"`
	NPWP            *string                `json:"npwp" db:"npwp" doc:"Masked unless the viewer may see sensitive data"`
	BankName        *string                `json:"bankName" db:"bank_name"`
	AccountNo       *string                `json:"accountNo" db:"account_no" doc:"Masked unless the viewer may see sensitive data"`
	AccountName     *string                `json:"accountName" db:"account_name"`
	ProrationFactor string                 `json:"prorationFactor" db:"proration_factor"`
	FixedWage       string                 `json:"fixedWage" db:"fixed_wage"`
	ScheduledDays   int                    `json:"scheduledDays" db:"scheduled_days"`
	PresentDays     int                    `json:"presentDays" db:"present_days"`
	AbsentDays      int                    `json:"absentDays" db:"absent_days"`
	UnpaidLeaveDays string                 `json:"unpaidLeaveDays" db:"unpaid_leave_days"`
	OvertimeHours   string                 `json:"overtimeHours" db:"overtime_hours"`
	Gross           string                 `json:"gross" db:"gross"`
	TaxableGross    string                 `json:"taxableGross" db:"taxable_gross"`
	BPJSEmployee    string                 `json:"bpjsEmployee" db:"bpjs_employee"`
	BPJSEmployer    string                 `json:"bpjsEmployer" db:"bpjs_employer"`
	PPh21           string                 `json:"pph21" db:"pph21"`
	PPh21Final      string                 `json:"pph21Final" db:"pph21_final"`
	OtherDeductions string                 `json:"otherDeductions" db:"other_deductions"`
	Net             string                 `json:"net" db:"net"`
	EmployerCost    string                 `json:"employerCost" db:"employer_cost"`
	Annual          *hris.PayrollAnnualTax `json:"annual" db:"annual"`
	Messages        []string               `json:"messages" db:"messages"`
	Status          string                 `json:"status" db:"status" enum:"ok,warning"`
	PublishedAt     *time.Time             `json:"publishedAt" db:"published_at"`
	ViewedAt        *time.Time             `json:"viewedAt" db:"viewed_at"`
	Lines           []PayrollSlipLine      `json:"lines" db:"-"`
}

const slipSelect = `SELECT s.id, s.run_id, r.number AS run_number, r.name AS run_name, r.run_type, r.status AS run_status, r.period_code, r.period_start,
	r.period_end, r.payment_date, s.employee_id, s.employee_no, s.full_name, s.org_unit_name, s.position_name, s.grade_code, s.cost_center, s.ptkp_status,
	s.ter_category, s.tax_method, trim_scale(s.ter_rate)::text AS ter_rate, s.npwp, s.bank_name, s.account_no, s.account_name,
	trim_scale(s.proration_factor)::text AS proration_factor, s.fixed_wage::text AS fixed_wage, s.scheduled_days, s.present_days, s.absent_days,
	trim_scale(s.unpaid_leave_days)::text AS unpaid_leave_days, trim_scale(s.overtime_hours)::text AS overtime_hours, s.gross::text AS gross,
	s.taxable_gross::text AS taxable_gross, s.bpjs_employee::text AS bpjs_employee, s.bpjs_employer::text AS bpjs_employer, s.pph21::text AS pph21,
	s.pph21_final::text AS pph21_final, s.other_deductions::text AS other_deductions, s.net::text AS net, s.employer_cost::text AS employer_cost, s.annual,
	s.messages, s.status, s.published_at, s.viewed_at
	FROM hris.payroll_slips s JOIN hris.payroll_runs r ON r.id = s.run_id`

const lineSelect = `SELECT line_no, code, name, kind, category, programme, taxable, irregular, pre_tax, trim_scale(quantity)::text AS quantity,
	trim_scale(rate)::text AS rate, amount::text AS amount, source, source_type, source_id, description FROM hris.payroll_lines`

func (m *Module) registerPayslips(reg *route.Registry) {
	tag := "HRIS Payroll"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/payslips", Summary: "Payslips of an employee across the runs (employee profile)",
		Permission: PermRunView, Response: PayrollSlip{}, List: true, Query: []route.Param{{Name: "employeeId", Required: true}, {Name: "year"}},
		Handler: listRead(m.DB, m.employeeSlipsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/payslips/{id}", Summary: "Payslip with its lines", Permission: PermRunView,
		Response: PayrollSlip{}, Handler: handle.Read(m.DB, m.slipHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/payslips/{id}/pdf", Summary: "Payslip PDF", Permission: PermRunView,
		RawContent: "application/pdf", Handler: m.slipPDFHTTP(false)})
	ess := "Employee Self Service"
	add(reg, ess, route.Route{Method: http.MethodGet, Path: "/api/v1/ess/payslips", Summary: "My payslips (posted runs)", Permission: hris.PermissionESS,
		Response: PayrollSlip{}, List: true, Query: []route.Param{{Name: "year"}}, Handler: listRead(m.DB, m.mySlipsHTTP)})
	add(reg, ess, route.Route{Method: http.MethodGet, Path: "/api/v1/ess/payslips/{id}", Summary: "My payslip", Permission: hris.PermissionESS,
		Response: PayrollSlip{}, Handler: handle.Read(m.DB, m.mySlipHTTP)})
	add(reg, ess, route.Route{Method: http.MethodGet, Path: "/api/v1/ess/payslips/{id}/pdf", Summary: "My payslip as PDF", Permission: hris.PermissionESS,
		RawContent: "application/pdf", Handler: m.slipPDFHTTP(true)})
}

// masked hides identity and account numbers from viewers without the
// sensitive permission (the employee sees the last 4 digits only).
func masked(s *PayrollSlip, show bool) {
	s.NPWP, s.AccountNo = maskPtr(s.NPWP, show), maskPtr(s.AccountNo, show)
}

func (m *Module) slip(ctx context.Context, tx pgx.Tx, sid uuid.UUID, where string, args ...any) (PayrollSlip, error) {
	rows, err := tx.Query(ctx, slipSelect+` WHERE s.id = $1`+where, append([]any{sid}, args...)...)
	s, err := handle.One[PayrollSlip](rows, err, "payslip")
	if err != nil {
		return s, err
	}
	if s.Messages == nil {
		s.Messages = []string{}
	}
	s.Lines, err = handle.List[PayrollSlipLine](tx.Query(ctx, lineSelect+` WHERE slip_id = $1 ORDER BY line_no`, sid))
	return s, err
}

func (m *Module) runSlipsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]PayrollSlip, error) {
	property := handle.Property(ctx)
	rid, err := handle.ID(r)
	if err != nil {
		return nil, err
	}
	if _, err := m.inProperty(ctx, tx, rid, property); err != nil {
		return nil, err
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	out, err := handle.List[PayrollSlip](tx.Query(ctx, slipSelect+` WHERE s.run_id = $1 AND ($2 = '' OR s.full_name ILIKE '%' || $2 || '%' OR s.employee_no ILIKE $2 || '%')
		AND ($3 = '' OR s.status = $3) ORDER BY s.org_unit_name NULLS LAST, s.full_name`, rid, q, filterParam(r, "status")))
	show := can(ctx, "hris.employee.view_sensitive", property)
	for i := range out {
		masked(&out[i], show)
		if out[i].Messages == nil {
			out[i].Messages = []string{}
		}
		out[i].Lines = []PayrollSlipLine{}
	}
	return out, err
}

// employeeSlipsHTTP lists the payslips of one employee, newest first (lines
// are read per payslip).
func (m *Module) employeeSlipsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]PayrollSlip, error) {
	property := handle.Property(ctx)
	eid, err := handle.QueryUUID(r, "employeeId")
	if err != nil {
		return nil, err
	}
	if eid == nil {
		return nil, errs.Validation("employee_required", "employeeId is required", errs.Field("employeeId", "required", "required"))
	}
	out, err := handle.List[PayrollSlip](tx.Query(ctx, slipSelect+` WHERE s.employee_id = $1 AND s.property_id = $2 AND r.status <> 'cancelled'
		AND ($3 = '' OR left(r.period_code, 4) = $3) ORDER BY r.period_code DESC, r.payment_date DESC LIMIT 60`, *eid, property, filterParam(r, "year")))
	show := can(ctx, "hris.employee.view_sensitive", property)
	for i := range out {
		masked(&out[i], show)
		if out[i].Messages == nil {
			out[i].Messages = []string{}
		}
		out[i].Lines = []PayrollSlipLine{}
	}
	return out, err
}

func (m *Module) slipHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (PayrollSlip, error) {
	property := handle.Property(ctx)
	sid, err := handle.ID(r)
	if err != nil {
		return PayrollSlip{}, err
	}
	s, err := m.slip(ctx, tx, sid, ` AND s.property_id = $2`, property)
	if err != nil {
		return s, err
	}
	masked(&s, can(ctx, "hris.employee.view_sensitive", property))
	return s, nil
}

// me is the employee of the signed-in user.
func me(ctx context.Context, q pgx.Tx) (hris.Employee, error) {
	uid := handle.UserID(ctx)
	if uid == uuid.Nil {
		return hris.Employee{}, errs.Unauthorized("sign in with your personal account")
	}
	e, err := hris.EmployeeByUser(ctx, q, uid)
	if err != nil {
		return hris.Employee{}, err
	}
	if e == nil {
		return hris.Employee{}, errs.NotFound("employee profile linked to your account")
	}
	return *e, nil
}

func (m *Module) mySlipsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]PayrollSlip, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return nil, err
	}
	out, err := handle.List[PayrollSlip](tx.Query(ctx, slipSelect+` WHERE s.employee_id = $1 AND r.status IN ('posted', 'paid') AND s.published_at IS NOT NULL
		AND ($2 = '' OR left(r.period_code, 4) = $2) ORDER BY r.period_code DESC, r.payment_date DESC LIMIT 60`, e.ID, filterParam(r, "year")))
	for i := range out {
		masked(&out[i], false)
		out[i].Messages, out[i].Lines = []string{}, []PayrollSlipLine{}
		out[i].EmployerCost, out[i].BPJSEmployer = "", ""
	}
	return out, err
}

func (m *Module) mySlipHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (PayrollSlip, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return PayrollSlip{}, err
	}
	sid, err := handle.ID(r)
	if err != nil {
		return PayrollSlip{}, err
	}
	s, err := m.slip(ctx, tx, sid, ` AND s.employee_id = $2 AND r.status IN ('posted', 'paid') AND s.published_at IS NOT NULL`, e.ID)
	if err != nil {
		return s, err
	}
	masked(&s, false)
	s.Messages = []string{}
	return s, nil
}

// slipPDFHTTP renders a payslip PDF (own payslip in ESS) and records the
// access in the audit log; the first ESS download marks it viewed.
func (m *Module) slipPDFHTTP(self bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		sid, err := handle.ID(r)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		var out []byte
		var name string
		err = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
			var s PayrollSlip
			var err error
			if self {
				e, err := me(ctx, tx)
				if err != nil {
					return err
				}
				if s, err = m.slip(ctx, tx, sid, ` AND s.employee_id = $2 AND r.status IN ('posted', 'paid') AND s.published_at IS NOT NULL`, e.ID); err != nil {
					return err
				}
				masked(&s, false)
				if _, err := tx.Exec(ctx, `UPDATE hris.payroll_slips SET viewed_at = coalesce(viewed_at, now()) WHERE id = $1`, sid); err != nil {
					return err
				}
			} else {
				property := handle.Property(ctx)
				if s, err = m.slip(ctx, tx, sid, ` AND s.property_id = $2`, property); err != nil {
					return err
				}
				masked(&s, can(ctx, "hris.employee.view_sensitive", property))
			}
			out, err = m.renderSlip(ctx, tx, s)
			if err != nil {
				return err
			}
			name = "Payslip " + s.PeriodCode + " " + s.EmployeeNo
			pid := propertyOf(ctx, tx, s.RunID)
			return audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "download_payslip", EntityType: "hris.payslip", EntityID: sid.String(),
				EntityLabel: name, PropertyID: &pid, Metadata: map[string]any{"self": self}})
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
}

func propertyOf(ctx context.Context, tx pgx.Tx, rid uuid.UUID) uuid.UUID {
	var p uuid.UUID
	_ = tx.QueryRow(ctx, `SELECT property_id FROM hris.payroll_runs WHERE id = $1`, rid).Scan(&p)
	return p
}

// rupiah formats an amount with thousands separators (Indonesian style).
func rupiah(s string) string {
	d := dec(s).Round(0)
	neg := d.IsNegative()
	digits := d.Abs().String()
	var b strings.Builder
	for i, c := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// renderSlip draws the payslip (Bahasa Indonesia headings, FR-PPY-03).
func (m *Module) renderSlip(ctx context.Context, tx pgx.Tx, s PayrollSlip) ([]byte, error) {
	doc := pdf.New()
	if m.Logo != nil {
		if logo := m.Logo(ctx, tx); len(logo) > 0 {
			_ = doc.Logo(logo, 120, 40)
		}
	}
	var legal *string
	_ = tx.QueryRow(ctx, `SELECT legal_name FROM platform.organization LIMIT 1`).Scan(&legal)
	prop, _ := org.PropertyName(ctx, tx, propertyOf(ctx, tx, s.RunID))
	doc.Row(9, false, deref(legal), prop)
	doc.Space(6)
	doc.Row(14, true, "SLIP GAJI / PAYSLIP", s.PeriodCode)
	doc.Rule(doc.Y + 8)
	doc.Space(6)
	doc.Row(9, false, "Nama / Name: "+s.FullName, "No. Karyawan: "+s.EmployeeNo)
	doc.Row(9, false, "Departemen: "+deref(s.OrgUnitName), "Jabatan: "+deref(s.PositionName))
	doc.Row(9, false, "Periode: "+ymd(s.PeriodStart)+" s/d "+ymd(s.PeriodEnd), "Tanggal bayar: "+ymd(s.PaymentDate))
	doc.Row(9, false, "PTKP: "+deref(s.PTKPStatus)+"  ·  TER: "+deref(s.TERCategory)+" "+s.TERRate+"%", "NPWP: "+deref(s.NPWP))
	doc.Row(9, false, "Rekening: "+deref(s.BankName)+" "+deref(s.AccountNo), "Run: "+s.RunNumber)
	doc.Space(6)
	section := func(title string, kinds ...string) decimal.Decimal {
		total := decimal.Zero
		doc.Row(10, true, title)
		for _, l := range s.Lines {
			if !oneOf(kinds, l.Kind) {
				continue
			}
			label := l.Name
			if l.Description != nil && *l.Description != "" && len(*l.Description) < 60 {
				label += " (" + *l.Description + ")"
			}
			doc.Row(9, false, "  "+label, rupiah(l.Amount))
			total = total.Add(dec(l.Amount))
		}
		return total
	}
	earn := section("Pendapatan / Earnings", hris.LineEarning)
	doc.Row(9, true, "  Total Pendapatan", rupiah(earn.String()))
	doc.Space(4)
	ded := section("Potongan / Deductions", hris.LineDeduction, hris.LineBPJSEmployee, hris.LineTax)
	doc.Row(9, true, "  Total Potongan", rupiah(ded.String()))
	doc.Rule(doc.Y + 6)
	doc.Space(4)
	doc.Row(12, true, "Gaji Bersih / Net Pay", "Rp "+rupiah(s.Net))
	doc.Space(8)
	er := section("Ditanggung perusahaan / Employer contributions (info)", hris.LineBPJSEmployer)
	if er.IsPositive() {
		doc.Row(9, false, "  Total", rupiah(er.String()))
	}
	if s.Annual != nil {
		doc.Space(4)
		doc.Row(9, true, "Perhitungan PPh 21 tahunan / Annual PPh 21")
		doc.Row(9, false, "  Penghasilan bruto setahun", rupiah(s.Annual.Gross.String()))
		doc.Row(9, false, "  Biaya jabatan", rupiah(s.Annual.OccupationalCost.String()))
		doc.Row(9, false, "  Iuran JHT / JP", rupiah(s.Annual.Contributions.String()))
		doc.Row(9, false, "  PTKP", rupiah(s.Annual.PTKP.String()))
		doc.Row(9, false, "  PKP", rupiah(s.Annual.TaxableIncome.String()))
		doc.Row(9, false, "  PPh 21 setahun", rupiah(s.Annual.AnnualTax.String()))
		doc.Row(9, false, "  Telah dipotong", rupiah(s.Annual.WithheldBefore.String()))
	}
	doc.Space(10)
	doc.Row(7, false, "Dokumen ini rahasia dan dibuat secara elektronik oleh OneClub; tidak memerlukan tanda tangan.")
	return doc.Bytes(), nil
}
