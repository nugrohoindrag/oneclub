package hris

// Payroll of PRD P5 (EP-09 Payroll Engine, EP-10 PPh 21 & BPJS, EP-15
// Payroll Accounting, Payment & Payslip): the public part — the domain
// events and their payloads (docs/p5-contracts.md), the run statuses and
// types, the statutory rate set the engine works with and the Employee Self
// Service section Payslip. The pure calculation engine is in
// p5_payroll_calc.go; the implementation (HTTP, runs, payslips, exports,
// imports, demo) lives in hris/payroll.

import (
	"maps"
	"slices"

	"github.com/google/uuid"
)

// Domain events of payroll (PRD P5 §11).
const (
	EventPayrollCalculated = "hris.payroll_calculated"
	EventPayrollPosted     = "hris.payroll_posted"
	EventPayrollPaid       = "hris.payroll_paid"
)

// Payroll run statuses (PRD P5 §7.6: Calculated, Approved, Posted, Paid).
const (
	RunDraft      = "draft"
	RunCalculated = "calculated"
	RunSubmitted  = "submitted" // waiting for the approval workflow
	RunApproved   = "approved"
	RunPosted     = "posted"
	RunPaid       = "paid"
	RunCancelled  = "cancelled"
)

// RunStatuses lists the payroll run statuses.
var RunStatuses = []string{RunDraft, RunCalculated, RunSubmitted, RunApproved, RunPosted, RunPaid, RunCancelled}

// Payroll run types (FR-PAY-02/04/05/06/07).
const (
	RunRegular         = "regular"          // monthly payroll of the period
	RunTHR             = "thr"              // religious holiday allowance
	RunBonus           = "bonus"            // approved bonuses (performance, annual)
	RunAdjustment      = "adjustment"       // corrections and retro differences of an approved period
	RunFinalSettlement = "final_settlement" // leavers: leave encashment, severance, PKWT compensation
)

// RunTypes lists the payroll run types.
var RunTypes = []string{RunRegular, RunTHR, RunBonus, RunAdjustment, RunFinalSettlement}

// Journal line parts of hris.payroll_posted / hris.payroll_paid (the
// accounting posting rules match on them, contract H5).
const (
	PartEarning              = "earning"               // gross pay per component (negative: pre-tax deduction)
	PartEmployerContribution = "employer_contribution" // BPJS employer share (expense)
	PartBPJSEmployee         = "bpjs_employee"         // BPJS employee share withheld
	PartPPh21                = "pph21"                 // PPh 21 withheld (negative: December refund)
	PartDeduction            = "deduction"             // after-tax deductions (loans, cash advances, others)
	PartNetPay               = "net_pay"               // net pay transferred (hris.payroll_paid)
)

// PayrollJournalLine is one line of the payroll journal handed to
// accounting: an amount per part, component (or BPJS programme) and cost
// center / department. Accounting maps it with the DEF-PAYROLL-* posting
// rules (conditions on part, component, category, programme, costCenter);
// DebitRole / CreditRole are the default account roles when no rule
// matches.
type PayrollJournalLine struct {
	Part          string     `json:"part" enum:"earning,employer_contribution,bpjs_employee,pph21,deduction,net_pay"`
	ComponentCode string     `json:"componentCode"`
	ComponentName string     `json:"componentName"`
	Category      string     `json:"category" doc:"Component category, e.g. basic, allowance, overtime, service_charge, commission, bonus, thr, severance"`
	Programme     string     `json:"programme,omitempty" enum:"kesehatan,jht,jp,jkk,jkm" doc:"BPJS programme"`
	OrgUnitID     *uuid.UUID `json:"orgUnitId" doc:"Department (platform department id = org unit id)"`
	OrgUnitCode   string     `json:"orgUnitCode"`
	CostCenter    string     `json:"costCenter"`
	Amount        string     `json:"amount"`
	DebitRole     string     `json:"debitRole"`
	CreditRole    string     `json:"creditRole"`
	Description   string     `json:"description"`
}

// PayrollTotals are the totals of a payroll run.
type PayrollTotals struct {
	Headcount        int    `json:"headcount"`
	Gross            string `json:"gross" doc:"Earnings less pre-tax deductions (unpaid leave, absence)"`
	BPJSEmployee     string `json:"bpjsEmployee"`
	BPJSEmployer     string `json:"bpjsEmployer"`
	PPh21            string `json:"pph21" doc:"PPh 21 withheld (TER / annual and final tax on severance)"`
	OtherDeductions  string `json:"otherDeductions" doc:"After-tax deductions (loans, cash advances, others)"`
	Net              string `json:"net"`
	EmployerCost     string `json:"employerCost" doc:"Gross + BPJS employer contributions"`
	TaxableGross     string `json:"taxableGross"`
	EmployeesWarning int    `json:"employeesWarning"`
}

// PayrollCalculated is the payload of hris.payroll_calculated (each
// calculation and recalculation of a run).
type PayrollCalculated struct {
	RunID       uuid.UUID     `json:"runId"`
	Number      string        `json:"number"`
	PropertyID  uuid.UUID     `json:"propertyId"`
	RunType     string        `json:"runType"`
	PeriodCode  string        `json:"periodCode" doc:"YYYY-MM"`
	PeriodStart string        `json:"periodStart"`
	PeriodEnd   string        `json:"periodEnd"`
	PaymentDate string        `json:"paymentDate"`
	Totals      PayrollTotals `json:"totals"`
	Calculation int           `json:"calculation" doc:"1 for the first calculation, then 2, 3 … for recalculations"`
	PolicyRefs  []PolicyUse   `json:"policyRefs"`
}

// PolicyUse records the policy version a run used (FR-POL-P5-07).
type PolicyUse struct {
	Code    string `json:"code"`
	Version int    `json:"version"`
}

// PayrollPosted is the payload of hris.payroll_posted (contract H5):
// accounting books JournalLines dated PostingDate (Dr salary expense by
// component and department, Dr BPJS employer expense, Cr salaries / PPh 21 /
// BPJS payables; service charge and commission clear their payables).
type PayrollPosted struct {
	RunID        uuid.UUID            `json:"runId"`
	Number       string               `json:"number"`
	PropertyID   uuid.UUID            `json:"propertyId"`
	RunType      string               `json:"runType"`
	PeriodCode   string               `json:"periodCode"`
	PeriodStart  string               `json:"periodStart"`
	PeriodEnd    string               `json:"periodEnd"`
	PaymentDate  string               `json:"paymentDate"`
	PostingDate  string               `json:"postingDate" doc:"Journal date: the end of the payroll period"`
	Currency     string               `json:"currency"`
	Totals       PayrollTotals        `json:"totals"`
	JournalLines []PayrollJournalLine `json:"journalLines"`
	PolicyRefs   []PolicyUse          `json:"policyRefs"`
}

// PayrollPaid is the payload of hris.payroll_paid: the net pay transferred
// (accounting: Dr salaries payable, Cr bank).
type PayrollPaid struct {
	RunID           uuid.UUID            `json:"runId"`
	Number          string               `json:"number"`
	PropertyID      uuid.UUID            `json:"propertyId"`
	RunType         string               `json:"runType"`
	PeriodCode      string               `json:"periodCode"`
	PaidOn          string               `json:"paidOn"`
	Reference       string               `json:"reference"`
	BankAccountCode string               `json:"bankAccountCode,omitempty" doc:"GL bank account code (empty: the default bank account)"`
	Currency        string               `json:"currency"`
	Net             string               `json:"net"`
	Employees       int                  `json:"employees"`
	JournalLines    []PayrollJournalLine `json:"journalLines"`
}

// ── statutory rates (EP-10) ──────────────────────────────────────────────

// BPJS programmes.
const (
	ProgrammeKesehatan = "kesehatan"
	ProgrammeJHT       = "jht"
	ProgrammeJP        = "jp"
	ProgrammeJKK       = "jkk"
	ProgrammeJKM       = "jkm"
)

// BPJSProgrammes lists the programmes in payslip order.
var BPJSProgrammes = []string{ProgrammeKesehatan, ProgrammeJHT, ProgrammeJP, ProgrammeJKK, ProgrammeJKM}

// StatutoryRates is one versioned statutory rate set (HRIS → Payroll →
// Statutory Rates, /hris/statutory-rates): PTKP, PPh 21 TER categories and
// monthly rates (PP 58/2023), Article 17 rates (UU HPP), occupational cost,
// the final tax on severance (PP 68/2009) and the BPJS Kesehatan &
// Ketenagakerjaan rates with their wage caps. The set in force on the last
// day of a payroll period applies; without one the Payroll Configuration
// values apply. Every value must be verified by the tax consultant before
// go-live and at every regulation change (FR-TAX-HR-05).
type StatutoryRates struct {
	PTKP                            map[string]string       `json:"ptkp" doc:"Annual PTKP per status (TK/0 … K/3)"`
	TERCategories                   map[string]string       `json:"terCategories" doc:"PTKP status → TER category A / B / C"`
	TERRates                        map[string][]TaxBracket `json:"terRates" doc:"Monthly gross brackets per TER category (rate %)"`
	ProgressiveRates                []TaxBracket            `json:"progressiveRates" doc:"Article 17 rates on annual taxable income"`
	OccupationalCostPercent         string                  `json:"occupationalCostPercent" doc:"Biaya jabatan %"`
	OccupationalCostMaxAnnual       string                  `json:"occupationalCostMaxAnnual" doc:"Biaya jabatan maximum per year (pro rata per month worked)"`
	NonNPWPSurchargePercent         string                  `json:"nonNpwpSurchargePercent" doc:"Surcharge without NPWP / NIK"`
	SeveranceTaxBrackets            []TaxBracket            `json:"severanceTaxBrackets" doc:"Final PPh 21 on severance, service award and PKWT compensation"`
	TaxableEmployerContributions    []string                `json:"taxableEmployerContributions" doc:"BPJS employer shares that are taxable income (gross)"`
	DeductibleEmployeeContributions []string                `json:"deductibleEmployeeContributions" doc:"BPJS employee shares deducted in the annual calculation"`
	BPJS                            BPJSConfig              `json:"bpjs"`
}

// StatutoryRatesFrom returns the statutory part of a Payroll Configuration
// (used when no statutory rate set is in force).
func StatutoryRatesFrom(c PayrollConfiguration) StatutoryRates {
	terRates := map[string][]TaxBracket{}
	for k, v := range c.PPh21.TERRates {
		terRates[k] = slices.Clone(v)
	}
	return StatutoryRates{
		PTKP: maps.Clone(c.PTKP), TERCategories: maps.Clone(c.PPh21.TERCategories), TERRates: terRates,
		ProgressiveRates: slices.Clone(c.PPh21.ProgressiveRates), OccupationalCostPercent: c.PPh21.OccupationalCostPercent,
		OccupationalCostMaxAnnual: c.PPh21.OccupationalCostMaxAnnual, NonNPWPSurchargePercent: c.PPh21.NonNPWPSurchargePercent,
		SeveranceTaxBrackets:            DefaultSeveranceTaxBrackets(),
		TaxableEmployerContributions:    []string{ProgrammeKesehatan, ProgrammeJKK, ProgrammeJKM},
		DeductibleEmployeeContributions: []string{ProgrammeJHT, ProgrammeJP},
		BPJS:                            c.BPJS,
	}
}

// DefaultStatutoryRates are the initial rates (PMK 101/2016 PTKP, PP 58/2023
// TER, UU HPP Article 17, PP 68/2009 severance, BPJS rates of the PRD P5
// EP-10 acceptance criteria) — to be verified by the tax consultant.
func DefaultStatutoryRates() StatutoryRates { return StatutoryRatesFrom(NewPayrollConfiguration()) }

// DefaultSeveranceTaxBrackets is the final tax on severance pay (PP 68/2009):
// 0% up to Rp50 million, 5% to 100 million, 15% to 500 million, 25% above.
func DefaultSeveranceTaxBrackets() []TaxBracket {
	return brackets("50000000", "0", "100000000", "5", "500000000", "15", "", "25")
}

// ── Employee Self Service ────────────────────────────────────────────────

func init() {
	// FR-ESS-04 / FR-PPY-03: payslips are never cached offline (FR-OPS-P5-05).
	RegisterESSSection(ESSSection{Key: "payslip", Label: "Payslip", LabelID: "Slip Gaji", Icon: "receipt_long", Path: "/ops/ess/payslip", Order: 60})
}
