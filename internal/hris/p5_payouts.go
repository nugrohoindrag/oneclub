package hris

// Payouts & distributions (PRD P5 EP-11 Service Charge Distribution, EP-12
// Sales Commission & Bonus Payout, EP-13 caddy and EP-14 instructor payout
// runs): the public part other packages build on — the domain events and
// their payloads (accounting posts them, docs/p5-contracts.md), the partner
// kinds, the Partner Payout Policy (Caddy Policies continued, FR-POL-P5-06)
// and, in p5_payouts_calc.go, the pure calculation engines (95/5
// distribution with attendance factor, PPh 21 non-employee, BPJS BPU).
// The use cases live in hris/payouts.

import (
	"context"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/rules"
)

// Domain events of the payouts area (outbox; aggregates
// hris.service_charge_distribution and hris.payout_run).
const (
	// EventServiceChargeDistributed: a distribution was approved; accounting
	// moves the month's pool from the service charge liability to the
	// distribution payable (paid by payroll), the reserve and the rounding.
	EventServiceChargeDistributed = "hris.service_charge_distributed"
	// EventPayoutPosted: a partner payout run was approved; accounting clears
	// the caddy fee / instructor fee liability against PPh 21, BPJS BPU,
	// deductions and the partner payout payable (net).
	EventPayoutPosted = "hris.payout_posted"
	// EventPayoutPaid: the run was paid (bank transfer / cash); accounting
	// clears the partner payout payable against the bank or cash.
	EventPayoutPaid = "hris.payout_paid"
)

// Partner kinds of payout runs (non-employee workforce, PRD P5 §16 #4).
const (
	PayoutKindCaddy      = "caddy"
	PayoutKindInstructor = "instructor"
)

// PayoutKinds lists the payout run kinds.
var PayoutKinds = []string{PayoutKindCaddy, PayoutKindInstructor}

// ServiceChargeDistributed is the payload of hris.service_charge_distributed.
// collected = distributed + reserve + undistributed + roundingDifference.
type ServiceChargeDistributed struct {
	DistributionID      uuid.UUID `json:"distributionId"`
	Number              string    `json:"number"`
	PropertyID          uuid.UUID `json:"propertyId"`
	Year                int       `json:"year"`
	Month               int       `json:"month"`
	PoolID              uuid.UUID `json:"poolId" doc:"accounting.service_charge_pools id (contract H4)"`
	PayPeriod           string    `json:"payPeriod" doc:"YYYY-MM of the payroll that pays the lines"`
	Collected           string    `json:"collected"`
	Reserve             string    `json:"reserve" doc:"Breakage & loss reserve"`
	Distributed         string    `json:"distributed" doc:"Σ employee lines (paid with payroll)"`
	Undistributed       string    `json:"undistributed" doc:"Forfeited by attendance without redistribution, department shares below 100%, no eligible employee"`
	RoundingDifference  string    `json:"roundingDifference"`
	RoundingAccountCode string    `json:"roundingAccountCode" doc:"GL account of the rounding difference (Service Charge Policy; empty = rounding role)"`
	Employees           int       `json:"employees"`
	PolicyVersion       int       `json:"policyVersion"`
}

// PayoutSourceRef is one source document paid by a payout line.
type PayoutSourceRef struct {
	SourceType string    `json:"sourceType" enum:"golf.caddy_settlement,sportclub.instructor_fee"`
	SourceID   uuid.UUID `json:"sourceId"`
	Number     string    `json:"number"`
	Gross      string    `json:"gross"`
	Deductions string    `json:"deductions"`
}

// PayoutPostedLine is one partner of a posted payout run.
type PayoutPostedLine struct {
	LineID           uuid.UUID         `json:"lineId"`
	PartnerID        uuid.UUID         `json:"partnerId"`
	PartnerName      string            `json:"partnerName"`
	Gross            string            `json:"gross"`
	SourceDeductions string            `json:"sourceDeductions" doc:"Deductions of the settlements (golf Caddy Policies)"`
	PPh21            string            `json:"pph21"`
	BPU              string            `json:"bpu" doc:"BPJS Ketenagakerjaan BPU (JKK + JKM)"`
	OtherDeductions  string            `json:"otherDeductions"`
	Net              string            `json:"net"`
	PaymentMethod    string            `json:"paymentMethod" enum:"bank_transfer,cash"`
	Sources          []PayoutSourceRef `json:"sources"`
}

// PayoutPosted is the payload of hris.payout_posted.
type PayoutPosted struct {
	RunID            uuid.UUID          `json:"runId"`
	Number           string             `json:"number"`
	PropertyID       uuid.UUID          `json:"propertyId"`
	Kind             string             `json:"kind" enum:"caddy,instructor"`
	PeriodStart      string             `json:"periodStart"`
	PeriodEnd        string             `json:"periodEnd"`
	PayDate          string             `json:"payDate"`
	Gross            string             `json:"gross"`
	SourceDeductions string             `json:"sourceDeductions"`
	PPh21            string             `json:"pph21"`
	BPU              string             `json:"bpu"`
	OtherDeductions  string             `json:"otherDeductions"`
	Net              string             `json:"net"`
	Lines            []PayoutPostedLine `json:"lines"`
}

// PayoutPaid is the payload of hris.payout_paid.
type PayoutPaid struct {
	RunID      uuid.UUID `json:"runId"`
	Number     string    `json:"number"`
	PropertyID uuid.UUID `json:"propertyId"`
	Kind       string    `json:"kind" enum:"caddy,instructor"`
	PaidOn     string    `json:"paidOn"`
	Reference  string    `json:"reference"`
	Net        string    `json:"net"`
	BankAmount string    `json:"bankAmount" doc:"Paid by bank transfer (bank file)"`
	CashAmount string    `json:"cashAmount" doc:"Paid in cash"`
}

// ── Partner Payout Policy (Caddy Policies continued, FR-POL-P5-06) ───────

// PartnerPayoutPolicyCode is the policy of caddy and instructor payouts.
const PartnerPayoutPolicyCode = "hris.partner_payout_policy"

// PartnerDeduction is a deduction from partner payouts (Caddy Policies):
// fixed per run, percent of the gross, or once a month.
type PartnerDeduction struct {
	Code     string `json:"code"`
	Label    string `json:"label"`
	Kind     string `json:"kind" enum:"fixed,percent"`
	Amount   string `json:"amount" doc:"Amount (fixed) or percent of the gross"`
	PerMonth bool   `json:"perMonth" doc:"Deducted once a month (first run of the month that pays the partner)"`
}

// PartnerPayoutRule is the payout rule of one partner kind.
type PartnerPayoutRule struct {
	WithholdPPh21 bool   `json:"withholdPph21" doc:"Withhold PPh 21 non-employee (Article 17 rates × the taxable share of the Payroll Configuration)"`
	TaxMethod     string `json:"taxMethod" enum:"per_payment,cumulative_annual" doc:"per_payment: Article 17 on each payment's taxable base (PMK 168/2023); cumulative_annual: brackets on the calendar-year cumulative base"`
	DeductBPU     bool   `json:"deductBpu" doc:"Deduct BPJS Ketenagakerjaan BPU (JKK & JKM) of enrolled partners (rates: Payroll Configuration)"`
	// BPU contributions are monthly: semi-monthly runs deduct them once
	// a month, in the first run of the month that pays the partner.
	Deductions    []PartnerDeduction `json:"deductions"`
	MinimumNet    string             `json:"minimumNet" doc:"Lines below this net are held for the next run (0 = pay any amount)"`
	PaymentMethod string             `json:"paymentMethod" enum:"bank_transfer,cash" doc:"Default payment method of partners without a profile"`
}

// PartnerPayoutPolicy is the payout policy of the non-employee workforce
// (PRD P5 §16 #4): caddies semi-monthly (15th & month end) including
// non-cash tips, partner instructors monthly (schedules: Payroll
// Configuration partnerPayout).
type PartnerPayoutPolicy struct {
	Caddy        PartnerPayoutRule `json:"caddy"`
	Instructor   PartnerPayoutRule `json:"instructor"`
	BankFile     string            `json:"bankFile" enum:"generic_csv,bca_csv" doc:"Layout of the bulk transfer file"`
	DebitAccount string            `json:"debitAccount" doc:"Club account debited by the bulk transfer (bank file header)"`
	TaxNote      string            `json:"taxNote" doc:"Shown on runs and statements"`
}

// PartnerTaxNote marks the initial partner tax and BPJS values.
const PartnerTaxNote = "PPh 21 non-employee (Article 17 rates × 50% of gross) and BPJS Ketenagakerjaan BPU are initial values, " +
	"to be verified by the tax consultant before go-live (FR-TAX-HR-05)."

// Rule returns the rule of a partner kind.
func (p PartnerPayoutPolicy) Rule(kind string) PartnerPayoutRule {
	if kind == PayoutKindInstructor {
		return p.Instructor
	}
	return p.Caddy
}

// NewPartnerPayoutPolicy returns the PRD P5 §16 #4 defaults.
func NewPartnerPayoutPolicy() PartnerPayoutPolicy {
	rule := func() PartnerPayoutRule {
		return PartnerPayoutRule{WithholdPPh21: true, TaxMethod: "per_payment", DeductBPU: true, Deductions: []PartnerDeduction{}, MinimumNet: "0",
			PaymentMethod: "bank_transfer"}
	}
	return PartnerPayoutPolicy{Caddy: rule(), Instructor: rule(), BankFile: "generic_csv", TaxNote: PartnerTaxNote}
}

func init() {
	rules.RegisterPolicy(rules.PolicyDef{Code: PartnerPayoutPolicyCode, Category: CategoryHRPolicies, Name: "Partner Payout Policy",
		Description: "Caddy and partner instructor payouts: PPh 21 non-employee method, BPJS BPU deduction, deductions (Caddy Policies), " +
			"minimum net, payment method and bank file. " + PartnerTaxNote, Default: NewPartnerPayoutPolicy()})
	PolicyCodes = append(PolicyCodes, PartnerPayoutPolicyCode)
}

// LoadPartnerPayoutPolicy returns the Partner Payout Policy in force.
func LoadPartnerPayoutPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID, at time.Time) (PartnerPayoutPolicy, rules.PolicyRef, error) {
	return rules.Policy(ctx, q, PartnerPayoutPolicyCode, &property, at, NewPartnerPayoutPolicy())
}
