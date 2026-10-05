package accounting

// EP-27 FR-POL-P4-03/04: Accounting Configuration (default accounts,
// posting modes, unmapped / late posting handling, manual journal approval
// threshold, loyalty point value) and the Accounting Policies (closing
// checklist, allowance for doubtful accounts, service charge pool). Club
// policies are versioned with an effective date (P0 framework) and may be
// property specific.

import (
	"context"
	"maps"
	"slices"

	"github.com/google/uuid"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/rules"
)

// Policy codes.
const (
	ConfigCode        = "accounting.configuration"
	ClosingPolicyCode = "accounting.closing"
	AllowanceCode     = "accounting.allowance"
	ServiceChargeCode = "accounting.service_charge"
)

// AccountingConfiguration is the Accounting Configuration document.
type AccountingConfiguration struct {
	// Accounts maps default-account roles (used by posting rules as @role)
	// to account codes of the chart of accounts.
	Accounts map[string]string `json:"accounts" doc:"Default accounts: role → account code"`
	// PostingModes per business line: daily_summary (journal at the
	// business day close, contract K4) or per_transaction (journal when a
	// payment settles); invoices, AP and inventory always post per document.
	PostingModes map[string]string `json:"postingModes" doc:"Business line → daily_summary | per_transaction (FR-PST-03)"`
	// UnmappedPosting: suspense posts what has no rule to the suspense
	// account and queues an exception; hold keeps the item unposted.
	UnmappedPosting string `json:"unmappedPosting" enum:"suspense,hold"`
	// ClosedPeriodPosting: next_open_period moves automatic postings dated in
	// a closed period to the first day of the next open period; exception
	// queues them.
	ClosedPeriodPosting string `json:"closedPeriodPosting" enum:"next_open_period,exception"`
	// ManualJournalApprovalThreshold: manual journals of this total or more
	// need approval (FR-ACC-02).
	ManualJournalApprovalThreshold string `json:"manualJournalApprovalThreshold"`
	// LoyaltyPointValue is the money value of one loyalty point (liability).
	LoyaltyPointValue string `json:"loyaltyPointValue"`
	// Dimensions offered on journal lines (FR-ACC-01).
	Dimensions []string `json:"dimensions"`
	// AutoMatchDays is the date tolerance of bank auto-matching.
	AutoMatchDays int `json:"autoMatchDays"`
}

// DefaultAccounts maps the roles to the OneClub club & hospitality template.
var DefaultAccounts = map[string]string{
	"cash": "1111", "petty_cash": "1112", "bank": "1121", "card_clearing": "1131", "gateway_clearing": "1132", "cash_in_transit": "1133",
	"guest_ledger": "1141", "ar_control": "1142", "ar_unbilled": "1143", "ar_other": "1144", "allowance_doubtful": "1149",
	"inventory": "1151", "wip": "1159", "ppn_input": "1171", "pph_prepaid": "1172", "prepaid_expense": "1181", "posting_clearing": "1199",
	"fixed_asset": "1240", "accumulated_depreciation": "1290",
	"ap_control": "2111", "consignment_payable": "2112", "grni": "2113", "service_charge_payable": "2121", "ppn_output": "2131",
	"pb1_payable": "2132", "pph_payable": "2133", "customer_deposits": "2141", "deferred_voucher": "2151", "deferred_prepaid": "2152",
	"deferred_annual_fee": "2153", "deferred_package": "2154", "deferred_clearing": "2159", "loyalty_liability": "2161",
	"caddy_fee_payable": "2171", "instructor_fee_payable": "2172", "commission_payable": "2173", "accrued_expenses": "2181", "suspense": "2199",
	"share_capital": "3100", "retained_earnings": "3200", "opening_balance_equity": "3900",
	"revenue_other": "4890", "breakage_income": "4810", "cancellation_income": "4820", "caddy_deduction_income": "4830", "discounts": "4910",
	"sales_allowance": "4920", "interest_income": "7110",
	"cogs": "5110", "cogs_consignment": "5130", "waste_expense": "5180", "inventory_variance": "5190",
	"instructor_fee_expense": "6120", "commission_expense": "6130", "loyalty_expense": "6410", "depreciation_expense": "6510",
	"bad_debt_expense": "6610", "bank_charges": "6620", "general_expense": "6640", "cash_over_short": "6910", "rounding": "6990",
}

// DefaultConfiguration follows PRD P4 FR-PST-03 (daily summary for charges
// and POS; per document for invoices and AP) and §16 #7.
var DefaultConfiguration = AccountingConfiguration{Accounts: DefaultAccounts,
	PostingModes: map[string]string{"golf": "daily_summary", "sportclub": "daily_summary", "stay": "daily_summary", "pos": "daily_summary",
		"membership": "daily_summary", "voucher": "daily_summary", "banquet": "daily_summary", "package": "daily_summary", "other": "daily_summary"},
	UnmappedPosting: "suspense", ClosedPeriodPosting: "next_open_period", ManualJournalApprovalThreshold: "10000000", LoyaltyPointValue: "1",
	Dimensions: []string{"property", "business_line", "department", "cost_center"}, AutoMatchDays: 3}

// ClosingPolicy is the period closing checklist (FR-ACC-06).
type ClosingPolicy struct {
	RequireBusinessDaysClosed  bool `json:"requireBusinessDaysClosed" doc:"Every business day of the period is closed by the night audit"`
	RequireNoPostingExceptions bool `json:"requireNoPostingExceptions" doc:"No open posting exception dated in the period"`
	RequireBankReconciled      bool `json:"requireBankReconciled" doc:"No unmatched bank transaction dated in the period"`
	RequireApMatched           bool `json:"requireApMatched" doc:"No unposted goods receipt / vendor invoice exception in the period"`
	RequireOpnamePosted        bool `json:"requireOpnamePosted" doc:"Stock opname of the period posted (inventory movement received)"`
}

// DefaultClosingPolicy: the checklist is shown with warnings; only posting
// exceptions block the close by default (PRD P4 §9.4).
var DefaultClosingPolicy = ClosingPolicy{RequireNoPostingExceptions: true}

// AllowancePolicy holds the provision rates per ageing bucket (FR-AR-05).
type AllowancePolicy struct {
	Rate0to30       string `json:"rate0to30"`
	Rate31to60      string `json:"rate31to60"`
	Rate61to90      string `json:"rate61to90"`
	RateOver90      string `json:"rateOver90"`
	RequireApproval bool   `json:"requireApproval"`
}

// DefaultAllowancePolicy: 0 / 5 / 25 / 50 percent.
var DefaultAllowancePolicy = AllowancePolicy{Rate0to30: "0", Rate31to60: "5", Rate61to90: "25", RateOver90: "50", RequireApproval: true}

// ServiceChargePolicy is the service charge pool basis (FR-REV-06, PRD P4
// §16 #16: 95% distributed, 5% reserve for breakage & loss).
type ServiceChargePolicy struct {
	ReservePercent string `json:"reservePercent"`
	// DepartmentWeights by department code; departments without a weight
	// share equally (weight 1).
	DepartmentWeights map[string]string `json:"departmentWeights"`
}

// DefaultServiceChargePolicy follows PRD P4 §16 #16.
var DefaultServiceChargePolicy = ServiceChargePolicy{ReservePercent: "5", DepartmentWeights: map[string]string{}}

func init() {
	// PRD P4 §7 labels: the club policy screen offers them as categories.
	for _, c := range []string{"Accounting Configuration", "Accounting Policies"} {
		if !slices.Contains(rules.PolicyCategories, c) {
			rules.PolicyCategories = append(rules.PolicyCategories, c)
		}
	}
	rules.RegisterPolicy(rules.PolicyDef{Code: ConfigCode, Category: "Accounting Configuration", Name: "Accounting Configuration",
		Description: "Default accounts, posting mode per business line, suspense / hold of unmapped postings, late postings into closed periods, " +
			"manual journal approval threshold, loyalty point value and dimensions", Default: DefaultConfiguration})
	rules.RegisterPolicy(rules.PolicyDef{Code: ClosingPolicyCode, Category: "Accounting Policies", Name: "Period closing checklist",
		Description: "Which checks block a period close: business days closed, posting exceptions, bank reconciliation, 3-way matching, stock opname",
		Default:     DefaultClosingPolicy})
	rules.RegisterPolicy(rules.PolicyDef{Code: AllowanceCode, Category: "Accounting Policies", Name: "Allowance for doubtful accounts",
		Description: "Provision rate per AR ageing bucket", Default: DefaultAllowancePolicy})
	rules.RegisterPolicy(rules.PolicyDef{Code: ServiceChargeCode, Category: "Accounting Policies", Name: "Service charge pool",
		Description: "Reserve percentage and department weights of the service charge pool", Default: DefaultServiceChargePolicy})
}

// LoadConfiguration returns the Accounting Configuration in force; roles
// missing from a configured map keep the template default.
func LoadConfiguration(ctx context.Context, q dbtx.Querier, property uuid.UUID) (AccountingConfiguration, error) {
	// the stored value is decoded over the default: fresh maps keep the
	// package defaults from being written into
	def := DefaultConfiguration
	def.Accounts, def.PostingModes = maps.Clone(DefaultAccounts), maps.Clone(DefaultConfiguration.PostingModes)
	def.Dimensions = slices.Clone(DefaultConfiguration.Dimensions)
	c, _, err := rules.PolicyAt(ctx, q, ConfigCode, property, def)
	if err != nil {
		return c, err
	}
	merged := make(map[string]string, len(DefaultAccounts))
	for k, v := range DefaultAccounts {
		merged[k] = v
	}
	for k, v := range c.Accounts {
		if v != "" {
			merged[k] = v
		}
	}
	c.Accounts = merged
	modes := map[string]string{}
	for k, v := range DefaultConfiguration.PostingModes {
		modes[k] = v
	}
	for k, v := range c.PostingModes {
		modes[k] = v
	}
	c.PostingModes = modes
	if c.UnmappedPosting == "" {
		c.UnmappedPosting = "suspense"
	}
	if c.ClosedPeriodPosting == "" {
		c.ClosedPeriodPosting = "next_open_period"
	}
	if c.AutoMatchDays <= 0 {
		c.AutoMatchDays = DefaultConfiguration.AutoMatchDays
	}
	if c.ManualJournalApprovalThreshold == "" {
		c.ManualJournalApprovalThreshold = DefaultConfiguration.ManualJournalApprovalThreshold
	}
	if c.LoyaltyPointValue == "" {
		c.LoyaltyPointValue = DefaultConfiguration.LoyaltyPointValue
	}
	return c, nil
}

func loadClosingPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (ClosingPolicy, error) {
	p, _, err := rules.PolicyAt(ctx, q, ClosingPolicyCode, property, DefaultClosingPolicy)
	return p, err
}

func loadAllowancePolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (AllowancePolicy, error) {
	p, _, err := rules.PolicyAt(ctx, q, AllowanceCode, property, DefaultAllowancePolicy)
	return p, err
}

func loadServiceChargePolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (ServiceChargePolicy, error) {
	def := DefaultServiceChargePolicy
	def.DepartmentWeights = maps.Clone(DefaultServiceChargePolicy.DepartmentWeights)
	p, _, err := rules.PolicyAt(ctx, q, ServiceChargeCode, property, def)
	return p, err
}
