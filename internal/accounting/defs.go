package accounting

// Master data of Accounting on the generic resource engine: Chart of
// Accounts (FR-ACC-01, CSV import = migration of the CoA, FR-MIG-P4-01),
// Posting Rules (FR-PST-01/06), Tax Configuration → accounts (FR-REV-04 /
// FR-POL-P4-04), Cash & Bank Accounts (FR-BNK-01), Excel Finance account
// mappings (FR-TRS-05), Revenue Allocation Rules (FR-REV-01) and Recurring
// Journals (FR-ACC-08).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

var accountFK = &resource.Ref{Table: "accounting.accounts", Label: "account"}

// AccountTypes are the types of the chart of accounts.
var AccountTypes = []string{"asset", "liability", "equity", "revenue", "cogs", "expense"}

// Accounts is the Chart of Accounts (instance level: one legal entity).
var Accounts = &resource.Def{
	Key: "accounting.account", Module: "accounting", Perm: "accounting.account", Path: "/api/v1/accounting/accounts", Table: "accounting.accounts",
	Name: "Account", Plural: "Chart of Accounts", Tag: "Chart of Accounts", Archive: true, CodeField: "code", OrderBy: "code, id",
	SchemaName: "AccountingAccount",
	Fields: []resource.Field{
		{Name: "code", Column: "code", Label: "Account Code", Kind: resource.String, Required: true, Max: 20, Search: true, CreateOnly: true,
			Pattern: resource.FoundationCode, PatternMsg: "1–20 characters: letters, digits, . _ / -"},
		resource.Name(),
		{Name: "nameId", Column: "name_id", Label: "Name (Bahasa Indonesia)", Kind: resource.String, Max: 160, Search: true},
		{Name: "accountType", Column: "account_type", Label: "Account Type", Kind: resource.Enum, Enum: AccountTypes, Required: true, Filter: true},
		{Name: "subtype", Column: "subtype", Label: "Subtype", Kind: resource.String, Max: 40, Default: "other", Filter: true},
		{Name: "parentId", Column: "parent_id", Label: "Parent Account", Kind: resource.UUID, Filter: true, Ref: accountFK},
		{Name: "isPosting", Column: "is_posting", Label: "Posting Account (not a header)", Kind: resource.Bool, Default: true, Filter: true},
		{Name: "normalBalance", Column: "normal_balance", Label: "Normal Balance", Kind: resource.Enum, Enum: []string{"debit", "credit"}},
		{Name: "cashFlow", Column: "cash_flow", Label: "Cash Flow Group", Kind: resource.Enum,
			Enum: []string{"cash", "operating", "investing", "financing", "non_cash"}, Default: "operating"},
		{Name: "businessLine", Column: "business_line", Label: "Business Line (dimension default)", Kind: resource.String, Max: 40},
		{Name: "costCenter", Column: "cost_center", Label: "Cost Center (dimension default)", Kind: resource.String, Max: 40},
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 1000},
		resource.Status("active", "inactive")},
}

// PostingRules are the versioned posting rules.
var PostingRules = &resource.Def{
	Key: "accounting.posting_rule", Module: "accounting", Perm: "accounting.posting_rule", Path: "/api/v1/accounting/posting-rules",
	Table: "accounting.posting_rules", Name: "Posting Rule", Plural: "Posting Rules", Tag: "Posting Rules", CodeField: "code", NoDelete: true,
	OrderBy: "source, priority, code, effective_from DESC, id", SchemaName: "AccountingPostingRule",
	Fields: []resource.Field{
		{Name: "code", Column: "code", Label: "Rule Code", Kind: resource.String, Required: true, Max: 60, Upper: true, Search: true, CreateOnly: true},
		resource.Name(),
		{Name: "source", Column: "source", Label: "Posting Source (event / document)", Kind: resource.String, Required: true, Max: 80, Filter: true, Search: true},
		{Name: "conditions", Column: "conditions", Label: "Conditions ({attribute: value | [values] | \"*\"})", Kind: resource.JSON, Default: map[string]any{}},
		{Name: "debitAccountId", Column: "debit_account_id", Label: "Debit Account", Kind: resource.UUID, Ref: accountFK},
		{Name: "debitRole", Column: "debit_role", Label: "Debit Default Account (role)", Kind: resource.String, Max: 60},
		{Name: "creditAccountId", Column: "credit_account_id", Label: "Credit Account", Kind: resource.UUID, Ref: accountFK},
		{Name: "creditRole", Column: "credit_role", Label: "Credit Default Account (role)", Kind: resource.String, Max: 60},
		{Name: "priority", Column: "priority", Label: "Priority (lower wins)", Kind: resource.Int, Default: int64(100)},
		{Name: "effectiveFrom", Column: "effective_from", Label: "Effective From", Kind: resource.Date, Default: "2000-01-01", CreateOnly: true},
		{Name: "effectiveTo", Column: "effective_to", Label: "Effective To", Kind: resource.Date},
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 1000},
		resource.Status("active", "inactive")},
}

// TaxCodes is the Tax Configuration mapped to accounts.
var TaxCodes = &resource.Def{
	Key: "accounting.tax_code", Module: "accounting", Perm: "accounting.tax_code", Path: "/api/v1/accounting/tax-codes", Table: "accounting.tax_codes",
	Name: "Tax Code", Plural: "Tax Configuration", Tag: "Revenue & Tax", CodeField: "code", OrderBy: "code, id", SchemaName: "AccountingTaxCode",
	Fields: []resource.Field{resource.Code("Tax Code"), resource.Name(),
		{Name: "kind", Column: "kind", Label: "Kind", Kind: resource.Enum, Enum: []string{"output_vat", "input_vat", "local_tax", "service_charge", "withholding"},
			Required: true, Filter: true},
		{Name: "ratePercent", Column: "rate_percent", Label: "Rate (%)", Kind: resource.Decimal, Default: "0", Min: resource.Min(0), MaxN: resource.Max(100)},
		{Name: "accountId", Column: "account_id", Label: "Account", Kind: resource.UUID, Required: true, Ref: accountFK},
		{Name: "ruleCodes", Column: "rule_codes", Label: "Tax & Service Rule Codes", Kind: resource.StringList, Default: []string{}, Upper: true},
		{Name: "transactionCode", Column: "transaction_code", Label: "e-Faktur Transaction Code", Kind: resource.String, Max: 4},
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 1000},
		resource.Status("active", "inactive")},
}

// BankAccounts are the cash, petty cash and bank accounts of a property.
var BankAccounts = &resource.Def{
	Key: "accounting.bank_account", Module: "accounting", Perm: "accounting.bank_account", Path: "/api/v1/accounting/bank-accounts",
	Table: "accounting.bank_accounts", Name: "Cash / Bank Account", Plural: "Cash & Bank Accounts", Tag: "Cash & Bank", PropertyScoped: true, Archive: true,
	CodeField: "code", OrderBy: "kind, code, id", SchemaName: "AccountingBankAccount",
	Fields: []resource.Field{resource.Code("Account Code"), resource.Name(),
		{Name: "kind", Column: "kind", Label: "Kind", Kind: resource.Enum, Enum: []string{"bank", "cash", "petty_cash"}, Default: "bank", Filter: true},
		{Name: "bankName", Column: "bank_name", Label: "Bank", Kind: resource.String, Max: 80},
		{Name: "accountNo", Column: "account_no", Label: "Account Number", Kind: resource.String, Max: 40},
		{Name: "accountHolder", Column: "account_holder", Label: "Account Holder", Kind: resource.String, Max: 160},
		{Name: "currency", Column: "currency", Label: "Currency", Kind: resource.String, Max: 3, Default: "IDR", Upper: true},
		{Name: "glAccountId", Column: "gl_account_id", Label: "GL Account", Kind: resource.UUID, Required: true, Ref: accountFK},
		{Name: "statementFormat", Column: "statement_format", Label: "Statement Format", Kind: resource.Enum,
			Enum: []string{"generic_csv", "bca_csv", "mandiri_csv", "mt940"}, Default: "generic_csv"},
		{Name: "floatAmount", Column: "float_amount", Label: "Float / Imprest (petty cash)", Kind: resource.Decimal, Min: resource.Min(0)},
		{Name: "custodian", Column: "custodian", Label: "Custodian", Kind: resource.String, Max: 160},
		resource.Status("active", "inactive")},
}

// AccountMappings map Excel Finance items to the chart of accounts.
var AccountMappings = &resource.Def{
	Key: "accounting.account_mapping", Module: "accounting", Perm: "accounting.account_mapping", Path: "/api/v1/accounting/account-mappings",
	Table: "accounting.account_mappings", Name: "Account Mapping", Plural: "Account Mappings", Tag: "Accounting Transition", CodeField: "sourceCode",
	OrderBy: "source_system, source_code, id", SchemaName: "AccountingAccountMapping",
	Fields: []resource.Field{
		{Name: "sourceSystem", Column: "source_system", Label: "Source", Kind: resource.String, Max: 40, Default: "excel_finance", Filter: true},
		{Name: "sourceCode", Column: "source_code", Label: "Source Code (Excel Finance item)", Kind: resource.String, Required: true, Max: 60, Search: true},
		{Name: "sourceName", Column: "source_name", Label: "Source Name", Kind: resource.String, Max: 160, Search: true},
		{Name: "accountId", Column: "account_id", Label: "OneClub Account", Kind: resource.UUID, Required: true, Ref: accountFK, Filter: true},
		{Name: "notes", Column: "notes", Label: "Notes", Kind: resource.Text, Max: 1000}},
}

// RevenueAllocationRules split bundled charges into components.
var RevenueAllocationRules = &resource.Def{
	Key: "accounting.revenue_allocation_rule", Module: "accounting", Perm: "accounting.revenue_allocation_rule",
	Path: "/api/v1/accounting/revenue-allocation-rules", Table: "accounting.revenue_allocation_rules", Name: "Revenue Allocation Rule",
	Plural: "Revenue Allocation Rules", Tag: "Revenue & Tax", Archive: true, CodeField: "code", OrderBy: "code, effective_from DESC, id",
	SchemaName: "AccountingRevenueAllocationRule",
	Fields: []resource.Field{resource.Code("Rule Code"), resource.Name(),
		{Name: "businessLine", Column: "business_line", Label: "Business Line (empty: all)", Kind: resource.String, Max: 40, Filter: true},
		{Name: "matchComponent", Column: "match_component", Label: "Charge / Revenue Component", Kind: resource.String, Required: true, Max: 60, Filter: true},
		{Name: "components", Column: "components", Label: "Allocation ([{component, percent, liability}], 100%)", Kind: resource.JSONList, Required: true},
		{Name: "effectiveFrom", Column: "effective_from", Label: "Effective From", Kind: resource.Date, Required: true},
		{Name: "effectiveTo", Column: "effective_to", Label: "Effective To", Kind: resource.Date},
		resource.Status("active", "inactive")},
}

// RecurringJournals are the recurring journals (monthly accruals).
var RecurringJournals = &resource.Def{
	Key: "accounting.recurring_journal", Module: "accounting", Perm: "accounting.recurring_journal", Path: "/api/v1/accounting/recurring-journals",
	Table: "accounting.recurring_journals", Name: "Recurring Journal", Plural: "Recurring Journals", Tag: "General Ledger", PropertyScoped: true,
	CodeField: "code", OrderBy: "code, id", SchemaName: "AccountingRecurringJournal",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 1000},
		{Name: "lines", Column: "lines", Label: "Lines ([{accountId, debit | credit, description}])", Kind: resource.JSONList, Required: true},
		{Name: "frequency", Column: "frequency", Label: "Frequency", Kind: resource.Enum, Enum: []string{"monthly", "quarterly", "yearly"}, Default: "monthly"},
		{Name: "dayOfMonth", Column: "day_of_month", Label: "Day of Month", Kind: resource.Int, Default: int64(28), Min: resource.Min(1), MaxN: resource.Max(31)},
		{Name: "nextRunDate", Column: "next_run_date", Label: "Next Run", Kind: resource.Date, Required: true},
		{Name: "endDate", Column: "end_date", Label: "End Date", Kind: resource.Date},
		{Name: "autoReverse", Column: "auto_reverse", Label: "Reverse on the 1st of the next month", Kind: resource.Bool, Default: false},
		{Name: "lastRunAt", Column: "last_run_at", Label: "Last Run", Kind: resource.Timestamp, ReadOnly: true},
		{Name: "runs", Column: "runs", Label: "Runs", Kind: resource.Int, ReadOnly: true},
		resource.Status("active", "inactive")},
}

// Defs are the resource definitions of Accounting.
func Defs() []*resource.Def {
	return []*resource.Def{Accounts, PostingRules, TaxCodes, BankAccounts, AccountMappings, RevenueAllocationRules, RecurringJournals}
}

func str(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case *string:
		if t == nil {
			return ""
		}
		return *t
	}
	return fmt.Sprint(v)
}

// rawJSON returns the JSON of a resource value (stored as text, bytes or a
// decoded value).
func rawJSON(v any) []byte {
	switch x := v.(type) {
	case string:
		return []byte(x)
	case []byte:
		return x
	case json.RawMessage:
		return x
	}
	b, _ := json.Marshal(v)
	return b
}

func val(values, before map[string]any, k string) any {
	if v, ok := values[k]; ok {
		return v
	}
	if before != nil {
		return before[k]
	}
	return nil
}

func init() {
	Accounts.Hooks = resource.Hooks{BeforeWrite: accountBeforeWrite}
	PostingRules.Hooks = resource.Hooks{BeforeWrite: ruleBeforeWrite}
	BankAccounts.Hooks = resource.Hooks{BeforeWrite: bankBeforeWrite}
	RevenueAllocationRules.Hooks = resource.Hooks{BeforeWrite: func(ctx context.Context, tx pgx.Tx, v, before map[string]any) error {
		if c, ok := v["components"]; ok {
			return validateAllocation(c)
		}
		return nil
	}}
	RecurringJournals.Hooks = resource.Hooks{BeforeWrite: func(ctx context.Context, tx pgx.Tx, v, before map[string]any) error {
		if l, ok := v["lines"]; ok {
			return validateRecurringLines(ctx, tx, handle.Property(ctx), l)
		}
		return nil
	}}
}

// accountBeforeWrite derives the normal balance and protects used accounts.
func accountBeforeWrite(ctx context.Context, tx pgx.Tx, v, before map[string]any) error {
	typ := str(val(v, before, "accountType"))
	if before == nil || v["accountType"] != nil {
		if _, ok := v["normalBalance"]; !ok || v["normalBalance"] == nil {
			v["normalBalance"] = normalBalance(typ, strings.HasPrefix(str(val(v, before, "subtype")), "contra") ||
				str(val(v, before, "subtype")) == "accumulated_depreciation")
		}
	}
	if p := str(v["parentId"]); p != "" && before != nil && p == str(before["id"]) {
		return handle.Invalid("parentId", "cycle", "an account cannot be its own parent")
	}
	if before == nil {
		return nil
	}
	var used bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounting.journal_lines WHERE account_id = $1::uuid)`, str(before["id"])).Scan(&used); err != nil {
		return err
	}
	if !used {
		return nil
	}
	if t, ok := v["accountType"]; ok && str(t) != str(before["accountType"]) {
		return errs.Conflict("account_used", "the account has journal lines: its type cannot change")
	}
	if p, ok := v["isPosting"]; ok && p == false {
		return errs.Conflict("account_used", "the account has journal lines: it cannot become a header")
	}
	return nil
}

// ruleBeforeWrite checks the accounts or roles of a rule; a rule already
// used by posted journals keeps its mapping (create a new version).
func ruleBeforeWrite(ctx context.Context, tx pgx.Tx, v, before map[string]any) error {
	for _, side := range []string{"debit", "credit"} {
		acct, role := str(val(v, before, side+"AccountId")), strings.TrimPrefix(str(val(v, before, side+"Role")), "@")
		if acct == "" && role == "" {
			return handle.Invalid(side+"AccountId", "required", "choose the "+side+" account or default account role")
		}
		if role != "" {
			if _, ok := DefaultAccounts[role]; !ok {
				return handle.Invalid(side+"Role", "invalid", "unknown default account role "+role)
			}
			if _, ok := v[side+"Role"]; ok {
				v[side+"Role"] = role
			}
		}
	}
	if c, ok := v["conditions"]; ok && c != nil {
		if _, isMap := c.(map[string]any); !isMap {
			raw := rawJSON(c)
			var m map[string]any
			if json.Unmarshal(raw, &m) != nil {
				return handle.Invalid("conditions", "invalid", "conditions are an object {attribute: value}")
			}
		}
	}
	if before == nil {
		return nil
	}
	var used bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounting.journal_lines WHERE rule_id = $1::uuid)`, str(before["id"])).Scan(&used); err != nil {
		return err
	}
	if used {
		for _, k := range []string{"source", "conditions", "debitAccountId", "debitRole", "creditAccountId", "creditRole"} {
			if nv, ok := v[k]; ok {
				a, _ := json.Marshal(nv)
				b, _ := json.Marshal(before[k])
				if string(a) != string(b) {
					return errs.Conflict("rule_used", "the rule has posted journals: create a new version with an effective date instead (FR-PST-06)")
				}
			}
		}
	}
	return nil
}

// bankBeforeWrite checks the GL account of a cash or bank account.
func bankBeforeWrite(ctx context.Context, tx pgx.Tx, v, before map[string]any) error {
	gl := str(val(v, before, "glAccountId"))
	if gl == "" {
		return nil
	}
	var typ string
	var posting bool
	if err := tx.QueryRow(ctx, `SELECT account_type, is_posting FROM accounting.accounts WHERE id = $1::uuid`, gl).Scan(&typ, &posting); err != nil {
		return handle.Invalid("glAccountId", "not_found", "account not found")
	}
	if typ != "asset" || !posting {
		return handle.Invalid("glAccountId", "invalid", "choose an asset posting account (cash, bank, clearing)")
	}
	return nil
}

// PostingRuleVersionInput versions a posting rule (FR-PST-06).
type PostingRuleVersionInput struct {
	EffectiveFrom   string         `json:"effectiveFrom"`
	Conditions      map[string]any `json:"conditions,omitempty"`
	DebitAccountID  *uuid.UUID     `json:"debitAccountId,omitempty"`
	DebitRole       string         `json:"debitRole,omitempty"`
	CreditAccountID *uuid.UUID     `json:"creditAccountId,omitempty"`
	CreditRole      string         `json:"creditRole,omitempty"`
	Priority        *int           `json:"priority,omitempty"`
}
