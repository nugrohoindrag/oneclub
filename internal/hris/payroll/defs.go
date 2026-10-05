package payroll

// Master data of payroll through the generic resource engine: pay
// components (FR-PAY-01) and employee loans / cash advances (FR-PAY-02
// deductions).

import (
	"context"
	"fmt"
	"net/http"
	"regexp"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

// Resource keys.
const (
	KeyPayComponent = "hris.pay_component"
	KeyEmployeeLoan = "hris.employee_loan"
)

var (
	componentCodeRe = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,29}$`)
	periodRe        = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)
)

// Defs returns the resource definitions of payroll.
func (m *Module) Defs() []*resource.Def {
	components := &resource.Def{
		Key: KeyPayComponent, Module: hris.Module, Perm: KeyPayComponent, Path: "/api/v1/hris/pay-components", Table: "hris.pay_components",
		Name: "Pay Component", Plural: "Pay Components", SchemaName: "PayrollPayComponent", Tag: "HRIS Payroll", PropertyScoped: true, Archive: true,
		CodeField: "code", OrderBy: "sort_order, code, id",
		Fields: []resource.Field{
			{Name: "code", Column: "code", Label: "Code", Kind: resource.String, Required: true, Max: 30, Upper: true, Pattern: componentCodeRe,
				PatternMsg: "1–30 characters: A–Z, 0–9, - or _", Search: true, CreateOnly: true},
			resource.Name(),
			{Name: "nameId", Column: "name_id", Label: "Name (Bahasa Indonesia)", Kind: resource.String, Max: 120},
			{Name: "kind", Column: "kind", Label: "Kind", Kind: resource.Enum, Enum: []string{"earning", "deduction"}, Required: true, Filter: true},
			{Name: "category", Column: "category", Label: "Category", Kind: resource.Enum, Enum: hris.ComponentCategories, Default: hris.CatAllowance,
				Filter: true},
			{Name: "taxable", Column: "taxable", Label: "Taxable (PPh 21 gross)", Kind: resource.Bool, Default: true},
			{Name: "irregular", Column: "irregular", Label: "Irregular income (bonus, THR)", Kind: resource.Bool, Default: false},
			{Name: "fixed", Column: "fixed", Label: "Fixed (BPJS, overtime and THR wage basis)", Kind: resource.Bool, Default: false},
			{Name: "preTax", Column: "pre_tax", Label: "Deduction before tax (reduces the taxable gross)", Kind: resource.Bool, Default: false},
			{Name: "prorate", Column: "prorate", Label: "Prorated for joiners and leavers", Kind: resource.Bool, Default: false},
			{Name: "calcMethod", Column: "calc_method", Label: "Calculation", Kind: resource.Enum, Enum: []string{"fixed", "per_present_day", "percent_of_basic"},
				Default: "fixed"},
			{Name: "defaultAmount", Column: "default_amount", Label: "Default Amount / Rate", Kind: resource.Decimal, Min: resource.Min(0)},
			{Name: "sortOrder", Column: "sort_order", Label: "Payslip Order", Kind: resource.Int, Default: int64(100), Min: resource.Min(0),
				MaxN: resource.Max(9999)},
			{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 2000},
			resource.Status("active", "inactive")},
		Hooks: resource.Hooks{BeforeWrite: componentBeforeWrite},
	}
	loans := &resource.Def{
		Key: KeyEmployeeLoan, Module: hris.Module, Perm: KeyEmployeeLoan, Path: "/api/v1/hris/employee-loans", Table: "hris.employee_loans",
		Name: "Employee Loan", Plural: "Employee Loans", SchemaName: "PayrollEmployeeLoan", Tag: "HRIS Payroll", PropertyScoped: true, Archive: true,
		OrderBy: "created_at DESC, id DESC",
		Fields: []resource.Field{
			{Name: "employeeId", Column: "employee_id", Label: "Employee", Kind: resource.UUID, Required: true, Filter: true, CreateOnly: true,
				Ref: &resource.Ref{Table: "hris.employees", SameProperty: true, Label: "employee"}},
			{Name: "loanType", Column: "loan_type", Label: "Type", Kind: resource.Enum, Enum: []string{"loan", "cash_advance"}, Default: "loan", Filter: true},
			{Name: "principal", Column: "principal", Label: "Principal", Kind: resource.Decimal, Required: true, Min: resource.Min(1)},
			{Name: "installment", Column: "installment", Label: "Installment per Period", Kind: resource.Decimal, Required: true, Min: resource.Min(1)},
			{Name: "startPeriod", Column: "start_period", Label: "First Deduction Period (YYYY-MM)", Kind: resource.String, Required: true, Max: 7,
				Pattern: periodRe, PatternMsg: "a period YYYY-MM"},
			{Name: "repaid", Column: "repaid", Label: "Repaid", Kind: resource.Decimal, ReadOnly: true},
			{Name: "reference", Column: "reference", Label: "Reference", Kind: resource.String, Max: 60, Search: true},
			{Name: "notes", Column: "notes", Label: "Notes", Kind: resource.Text, Max: 2000},
			resource.Status("active", "settled", "cancelled")},
		Hooks: resource.Hooks{BeforeWrite: loanBeforeWrite},
	}
	return []*resource.Def{components, loans}
}

func value(values, before map[string]any, k string) any {
	if v, ok := values[k]; ok {
		return v
	}
	if before != nil {
		return before[k]
	}
	return nil
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func boolOf(v any) bool { b, _ := v.(bool); return b }

func componentBeforeWrite(_ context.Context, _ pgx.Tx, values, before map[string]any) error {
	kind := str(value(values, before, "kind"))
	if kind == "earning" && boolOf(value(values, before, "preTax")) {
		return handle.Invalid("preTax", "invalid", "only a deduction is taken before tax")
	}
	if kind == "deduction" && boolOf(value(values, before, "fixed")) {
		return handle.Invalid("fixed", "invalid", "only an earning counts in the fixed wage")
	}
	return nil
}

func loanBeforeWrite(_ context.Context, _ pgx.Tx, values, before map[string]any) error {
	principal, installment := dec(decString(value(values, before, "principal"))), dec(decString(value(values, before, "installment")))
	if installment.GreaterThan(principal) {
		return handle.Invalid("installment", "invalid", "the installment cannot exceed the principal")
	}
	if before != nil && principal.LessThan(dec(decString(before["repaid"]))) {
		return handle.Invalid("principal", "invalid", "the principal cannot be less than the amount already repaid")
	}
	return nil
}

func decString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	}
	return fmt.Sprint(v)
}

// ── default components (FR-PAY-01, Payroll Configuration) ─────────────────

// builtinComponents complete the Payroll Configuration components with the
// codes the engine itself produces.
var builtinComponents = []componentDef{
	{Code: "BASIC", Name: "Basic Salary", NameID: "Gaji Pokok", Kind: "earning", Category: hris.CatBasic, Taxable: true, Fixed: true, Prorate: true, Sort: 10},
	{Code: "POSITION", Name: "Position Allowance", NameID: "Tunjangan Jabatan", Kind: "earning", Category: hris.CatAllowance, Taxable: true, Fixed: true,
		Prorate: true, Sort: 20},
	{Code: "MEAL", Name: "Meal Allowance", NameID: "Uang Makan", Kind: "earning", Category: hris.CatAllowance, Taxable: true, Method: "per_present_day",
		Sort: 30},
	{Code: "TRANSPORT", Name: "Transport Allowance", NameID: "Uang Transport", Kind: "earning", Category: hris.CatAllowance, Taxable: true,
		Method: "per_present_day", Sort: 40},
	{Code: "OVERTIME", Name: "Overtime", NameID: "Lembur", Kind: "earning", Category: hris.CatOvertime, Taxable: true, Sort: 50},
	{Code: "SERVICE_CHARGE", Name: "Service Charge", NameID: "Service Charge", Kind: "earning", Category: hris.CatServiceCharge, Taxable: true, Sort: 60},
	{Code: "COMMISSION", Name: "Sales Commission", NameID: "Komisi Penjualan", Kind: "earning", Category: hris.CatCommission, Taxable: true, Sort: 70},
	{Code: "BONUS", Name: "Bonus", NameID: "Bonus", Kind: "earning", Category: hris.CatBonus, Taxable: true, Irregular: true, Sort: 80},
	{Code: "THR", Name: "THR", NameID: "Tunjangan Hari Raya", Kind: "earning", Category: hris.CatTHR, Taxable: true, Irregular: true, Sort: 90},
	{Code: "LEAVE_ENCASHMENT", Name: "Leave Encashment", NameID: "Uang Pengganti Cuti", Kind: "earning", Category: hris.CatLeaveEncashment, Taxable: true,
		Irregular: true, Sort: 95},
	{Code: "SEVERANCE", Name: "Severance Pay", NameID: "Uang Pesangon", Kind: "earning", Category: hris.CatSeverance, Taxable: true, Sort: 96},
	{Code: "SERVICE_AWARD", Name: "Service Award", NameID: "Uang Penghargaan Masa Kerja", Kind: "earning", Category: hris.CatSeverance, Taxable: true,
		Sort: 97},
	{Code: "PKWT_COMPENSATION", Name: "PKWT Compensation", NameID: "Uang Kompensasi PKWT", Kind: "earning", Category: hris.CatSeverance, Taxable: true,
		Sort: 98},
	{Code: "UNPAID_LEAVE", Name: "Unpaid Leave", NameID: "Potongan Cuti Tidak Dibayar", Kind: "deduction", Category: hris.CatAbsence, PreTax: true, Sort: 110},
	{Code: "ABSENCE", Name: "Absence", NameID: "Potongan Mangkir", Kind: "deduction", Category: hris.CatAbsence, PreTax: true, Sort: 111},
	{Code: "UNPAID_PERMISSION", Name: "Unpaid Permission", NameID: "Potongan Izin", Kind: "deduction", Category: hris.CatAbsence, PreTax: true, Sort: 112},
	{Code: "LOAN", Name: "Loan Installment", NameID: "Cicilan Pinjaman", Kind: "deduction", Category: hris.CatLoan, Sort: 120},
	{Code: "CASH_ADVANCE", Name: "Cash Advance", NameID: "Kasbon", Kind: "deduction", Category: hris.CatLoan, Sort: 121},
}

type componentDef struct {
	Code, Name, NameID, Kind, Category, Method string
	Taxable, Irregular, Fixed, PreTax, Prorate bool
	Default                                    string
	Sort                                       int
}

// LoadDefaultsResult reports the default components created.
type LoadDefaultsResult struct {
	Created int `json:"created"`
	Total   int `json:"total"`
}

func (m *Module) registerComponents(reg *route.Registry) {
	add(reg, "HRIS Payroll", route.Route{Method: http.MethodPost, Path: "/api/v1/hris/pay-components:load-defaults",
		Summary: "Create the default pay components (Payroll Configuration + engine components) that do not exist yet", Permission: KeyPayComponent + ".create",
		Request: handle.Empty{}, Response: LoadDefaultsResult{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.loadDefaultsHTTP)})
}

func (m *Module) loadDefaultsHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request, _ handle.Empty) (LoadDefaultsResult, error) {
	property := handle.Property(ctx)
	n, total, err := ensureComponents(ctx, tx, property)
	if err != nil {
		return LoadDefaultsResult{}, err
	}
	return LoadDefaultsResult{Created: n, Total: total}, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "load_defaults",
		EntityType: KeyPayComponent, EntityLabel: "Default pay components", PropertyID: &property, After: map[string]any{"created": n}})
}
