package payroll

// Payroll Processing (Settings → Payroll Configuration, FR-POL-P5-04,
// FR-PPY-02, FR-INT-P5-04): how runs treat absence, BPJS exclusions, the
// final settlement, the period lock and the bank file layouts. Versioned
// with an effective date like every HR policy; each run stores the version
// it used (FR-POL-P5-07). The statutory part (PTKP, PPh 21, BPJS) is the
// statutory rate set; components, THR, severance and rounding are the
// Payroll Configuration of Core HR.

import (
	"context"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/rules"
)

// ProcessingCode is the policy code.
const ProcessingCode = "hris.payroll_processing"

// BankColumn is one field of a bank file record.
type BankColumn struct {
	Label string `json:"label" doc:"Header label (CSV)"`
	Field string `json:"field" doc:"sequence, employeeNo, fullName, accountNo, accountName, bankCode, bankName, amount, amountCents, currency, paymentDate, reference, remark, companyCode, debitAccount, count, total, totalCents or literal:<text>"`
	Width int    `json:"width,omitempty" doc:"Fixed width (fixed format)"`
	Align string `json:"align,omitempty" enum:"left,right"`
	Pad   string `json:"pad,omitempty" doc:"Pad character (default space; 0 for numbers)"`
}

// BankLayout is a bank payroll transfer file layout.
type BankLayout struct {
	Code       string       `json:"code"`
	Name       string       `json:"name"`
	Bank       string       `json:"bank" doc:"Bank code the layout is for (empty = any bank)"`
	Format     string       `json:"format" enum:"csv,fixed"`
	Delimiter  string       `json:"delimiter,omitempty"`
	HeaderRow  bool         `json:"headerRow" doc:"CSV: first row with the column labels"`
	DateFormat string       `json:"dateFormat" doc:"Go layout of dates, e.g. 2006-01-02 or 20060102"`
	Header     []BankColumn `json:"header" doc:"Header record (fixed format)"`
	Columns    []BankColumn `json:"columns"`
	Trailer    []BankColumn `json:"trailer" doc:"Trailer record (fixed format)"`
	Note       string       `json:"note,omitempty"`
}

// PayrollProcessing is the policy value.
type PayrollProcessing struct {
	DeductAbsence            bool              `json:"deductAbsence" doc:"Unexcused absence is deducted at the daily wage (no work, no pay)"`
	BPJSExcludedCategories   []string          `json:"bpjsExcludedCategories" doc:"Worker categories without BPJS contributions, e.g. intern"`
	DailyWageDivisorFiveDay  string            `json:"dailyWageDivisorFiveDay" doc:"Daily wage = monthly wage ÷ this for a 5-day week (leave encashment)"`
	DailyWageDivisorSixDay   string            `json:"dailyWageDivisorSixDay"`
	EncashLeaveOnTermination bool              `json:"encashLeaveOnTermination" doc:"Unused annual leave is paid in the final settlement"`
	SeveranceReasons         map[string]string `json:"severanceReasons" doc:"Termination type → severance reason of the Payroll Configuration ('' = no severance)"`
	PKWTCompensation         bool              `json:"pkwtCompensation" doc:"Pay the PKWT compensation when a PKWT ends (PP 35/2021)"`
	LockAtCalculation        bool              `json:"lockAtCalculation" doc:"Lock the attendance of a closed period when the run is calculated (else at approval)"`
	NotifyPayslips           bool              `json:"notifyPayslips" doc:"Notify employees when their payslips are released"`
	CompanyCode              string            `json:"companyCode" doc:"Company / corporate ID printed in the bank file"`
	DebitAccount             string            `json:"debitAccount" doc:"Debited company account number printed in the bank file"`
	DefaultBankLayout        string            `json:"defaultBankLayout"`
	BankLayouts              []BankLayout      `json:"bankLayouts"`
}

// NewPayrollProcessing returns the defaults (generic CSV and the BCA
// layout; verify the bank layout against the bank's current specification
// before go-live).
func NewPayrollProcessing() PayrollProcessing {
	return PayrollProcessing{
		DeductAbsence: true, BPJSExcludedCategories: []string{"intern"}, DailyWageDivisorFiveDay: "21", DailyWageDivisorSixDay: "25",
		EncashLeaveOnTermination: true, PKWTCompensation: true, LockAtCalculation: true, NotifyPayslips: true,
		SeveranceReasons:  map[string]string{"terminated": "terminated", "retired": "retired", "deceased": "deceased", "resigned": "resigned", "contract_ended": ""},
		CompanyCode:       "ONECLUB",
		DefaultBankLayout: "generic_csv",
		BankLayouts: []BankLayout{
			{Code: "generic_csv", Name: "Generic CSV", Format: "csv", Delimiter: ",", HeaderRow: true, DateFormat: "2006-01-02",
				Columns: []BankColumn{{Label: "No", Field: "sequence"}, {Label: "Employee No", Field: "employeeNo"}, {Label: "Name", Field: "fullName"},
					{Label: "Bank", Field: "bankCode"}, {Label: "Account No", Field: "accountNo"}, {Label: "Account Name", Field: "accountName"},
					{Label: "Amount", Field: "amount"}, {Label: "Currency", Field: "currency"}, {Label: "Payment Date", Field: "paymentDate"},
					{Label: "Reference", Field: "reference"}}},
			{Code: "bca_payroll", Name: "BCA – Payroll Transfer (fixed width)", Bank: "BCA", Format: "fixed", DateFormat: "20060102",
				Header: []BankColumn{{Field: "literal:0"}, {Field: "companyCode", Width: 10}, {Field: "debitAccount", Width: 10, Align: "right", Pad: "0"},
					{Field: "paymentDate", Width: 8}, {Field: "count", Width: 5, Align: "right", Pad: "0"}, {Field: "totalCents", Width: 17, Align: "right", Pad: "0"},
					{Field: "reference", Width: 20}},
				Columns: []BankColumn{{Field: "literal:1"}, {Field: "accountNo", Width: 10, Align: "right", Pad: "0"},
					{Field: "amountCents", Width: 15, Align: "right", Pad: "0"}, {Field: "employeeNo", Width: 10}, {Field: "accountName", Width: 30},
					{Field: "remark", Width: 18}},
				Trailer: []BankColumn{{Field: "literal:9"}, {Field: "count", Width: 5, Align: "right", Pad: "0"}, {Field: "totalCents", Width: 17, Align: "right",
					Pad: "0"}},
				Note: "BCA employees only; verify against the bank's current payroll file specification"},
		},
	}
}

func init() {
	rules.RegisterPolicy(rules.PolicyDef{Code: ProcessingCode, Category: hris.CategoryPayrollConfiguration, Name: "Payroll Processing",
		Description: "Absence deduction, BPJS exclusions, final settlement (leave encashment, severance reasons, PKWT compensation), period lock, " +
			"payslip notification and the bank file layouts",
		Default: NewPayrollProcessing()})
	hris.PolicyCodes = appendOnce(hris.PolicyCodes, ProcessingCode)
}

func appendOnce(list []string, v string) []string {
	for _, s := range list {
		if s == v {
			return list
		}
	}
	return append(list, v)
}

// LoadProcessing returns the Payroll Processing policy in force.
func LoadProcessing(ctx context.Context, q dbtx.Querier, property uuid.UUID, at time.Time) (PayrollProcessing, rules.PolicyRef, error) {
	return rules.Policy(ctx, q, ProcessingCode, &property, at, NewPayrollProcessing())
}

// layout returns a bank layout by code (the default when empty).
func (p PayrollProcessing) layout(code string) (BankLayout, bool) {
	if code == "" {
		code = p.DefaultBankLayout
	}
	for _, l := range p.BankLayouts {
		if l.Code == code {
			return l, true
		}
	}
	return BankLayout{}, false
}
