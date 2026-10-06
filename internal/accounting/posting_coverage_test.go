package accounting

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// postingCoverage maps every event accounting consumes to the e2e tests
// (test/e2e) that assert its ledger effect with exact amounts or its
// deliberate absence (no_posting) — PRD P4 FR-REL-P4-04: an integration test
// of every posting rule and of the balance reconciliations.
var postingCoverage = map[string][]string{
	"billing.business_day_closed":         {"TestP4AccountingBillingAR"},          // night audit → daily journal = Daily Revenue Report
	"billing.payment_settled":             {"TestP4AccountingGolfPerTransaction"}, // per transaction, ×3 delivery
	"billing.folio_closed":                {"TestP4FixLedgerBillingPostings"},
	"billing.refund_processed":            {"TestP4FixLedgerBillingPostings"},
	"billing.invoice_issued":              {"TestP4AccountingBillingAR", "TestP4FixLedgerBillingPostings"},
	"billing.invoice_paid":                {"TestP4AccountingBillingAR"}, // AR ledger payment, AR control = AR Aging
	"billing.invoice_voided":              {"TestP4FixLedgerBillingPostings"},
	"billing.credit_note_issued":          {"TestP4AccountingBillingAR"},
	"billing.invoice_written_off":         {"TestP4AccountingBillingAR"},
	"commercial.voucher_sold":             {"TestP4AccountingBillingAR"},
	"commercial.voucher_redeemed":         {"TestP4AccountingBillingAR"},
	"commercial.voucher_expired":          {"TestP4AccountingBillingAR"},
	"commercial.promotion_applied":        {"TestP4AccountingAutomaticPosting"},
	"commercial.package_booked":           {"TestP4AccountingAutomaticPosting"},
	"commercial.package_consumed":         {"TestP4AccountingAutomaticPosting"},
	"commercial.sale_completed":           {"TestP4AccountingAutomaticPosting"},
	"commercial.shift_closed":             {"TestP4AccountingAutomaticPosting"}, // shift without variance; POS cash over / short amount: not asserted
	"membership.annual_fee_due":           {"TestP4AccountingAutomaticPosting", "TestP4FixLedgerBillingPostings"},
	"membership.fee_paid":                 {"TestP4FixLedgerBillingPostings"},
	"sportclub.instructor_fee_approved":   {"TestP4AccountingAutomaticPosting"},
	"golf.caddy_settlement_approved":      {"TestP4FixLedgerCaddySettlement"},
	"golf.round_finished":                 {"TestP4AccountingAutomaticPosting"},
	"crm.loyalty_points_changed":          {"TestP4AccountingAutomaticPosting"},
	"crm.commission_approved":             {"TestP4FixLedgerCommission"},
	"inventory.movement_posted":           {"TestP4AccountingAutomaticPosting", "TestP4FixLedgerInventoryValuation", "TestP4FixLedgerOpeningStockCarried"},
	"inventory.asset_depreciated":         {"TestP4AccountingAutomaticPosting", "TestP4FixLedgerAssetDisposal"},
	"inventory.asset_disposed":            {"TestP4FixLedgerAssetDisposal"},
	"inventory.consignment_sold":          {"TestP4AccountingAutomaticPosting"},
	"inventory.revaluation_posted":        {"TestP4AccountingAutomaticPosting"},
	"procurement.goods_received":          {"TestP4AccountingAutomaticPosting"},
	"procurement.purchase_returned":       {"TestP4AccountingAutomaticPosting"},
	"procurement.vendor_invoice_approved": {"TestP4AccountingAutomaticPosting"},
	"procurement.debit_note_issued":       {"TestP4AccountingAutomaticPosting"},
	"hris.payroll_posted":                 {"TestP5PayrollFullRun"}, // PRD P5 EP-15: journal = run totals
	"hris.payroll_paid":                   {"TestP5PayrollFullRun"},
	"hris.loan_disbursed":                 {"TestP5LoanRequestsWithRevision"}, // HRIS phase B: Dr employee receivables / Cr bank
	"hris.loan_repaid":                    {"TestP5LoanRequestsWithRevision"}, // cash returned: Dr cash / Cr receivables
}

// TestPostingCoverage: every consumed event has a handler and at least one
// e2e test asserting its posting; the tests named exist.
func TestPostingCoverage(t *testing.T) {
	h := (&Module{}).handlers()
	seen := map[string]bool{}
	for _, ev := range ConsumedEvents {
		if seen[ev] {
			t.Errorf("%s consumed twice", ev)
		}
		seen[ev] = true
		if h[ev] == nil {
			t.Errorf("%s has no handler", ev)
		}
		if len(postingCoverage[ev]) == 0 {
			t.Errorf("%s has no e2e posting assertion (FR-REL-P4-04)", ev)
		}
	}
	for ev := range h {
		if !seen[ev] {
			t.Errorf("handler of %s is not subscribed (ConsumedEvents)", ev)
		}
	}
	for ev := range postingCoverage {
		if !seen[ev] {
			t.Errorf("coverage entry %s is not a consumed event", ev)
		}
	}
	files, err := filepath.Glob(filepath.Join("..", "..", "test", "e2e", "*_test.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("e2e tests not found: %v", err)
	}
	defined := map[string]bool{}
	re := regexp.MustCompile(`(?m)^func (Test\w+)\(t \*testing\.T\)`)
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
			defined[m[1]] = true
		}
	}
	for ev, tests := range postingCoverage {
		for _, name := range tests {
			if !defined[name] {
				t.Errorf("%s: e2e test %s does not exist", ev, name)
			}
		}
	}
}

// TestDefaultRuleSources: every default posting rule belongs to a consumed
// event or to an internal posting source of accounting, and the sources of
// the ledger fixes have their rules.
func TestDefaultRuleSources(t *testing.T) {
	consumed := map[string]bool{}
	for _, ev := range ConsumedEvents {
		consumed[ev] = true
	}
	internal := map[string]bool{}
	codes := map[string]ruleSpec{}
	for _, s := range defaultRuleSpecs() {
		if _, dup := codes[s.Code]; dup {
			t.Errorf("duplicate default rule %s", s.Code)
		}
		codes[s.Code] = s
		if !consumed[s.Source] {
			internal[s.Source] = true
		}
		for _, side := range []string{s.Debit, s.Credit} {
			if strings.HasPrefix(side, "@") && DefaultAccounts[strings.TrimPrefix(side, "@")] == "" {
				t.Errorf("%s uses role %s without a default account", s.Code, side)
			}
		}
	}
	var srcs []string
	for s := range internal {
		srcs = append(srcs, s)
	}
	sort.Strings(srcs)
	for _, s := range srcs {
		if !strings.HasPrefix(s, "billing.") && !strings.HasPrefix(s, "procurement.vendor_invoice_") && s != "accounting.vendor_payment" {
			t.Errorf("default rule source %s is neither consumed nor an internal source", s)
		}
	}
	for code, want := range map[string][2]string{
		"DEF-INV-OPENING":         {"@inventory", "@opening_balance_equity"},
		"DEF-COMMISSION":          {"@commission_expense", "@commission_payable"},
		"DEF-ASSET-DISP-COST":     {"@posting_clearing", "@fixed_asset"},
		"DEF-ASSET-DISP-ACC":      {"@accumulated_depreciation", "@posting_clearing"},
		"DEF-ASSET-DISP-PROCEEDS": {"@ar_other", "@posting_clearing"},
		"DEF-ASSET-DISP-GAIN":     {"@posting_clearing", "@asset_disposal_gain"},
		"DEF-ASSET-DISP-LOSS":     {"@asset_disposal_loss", "@posting_clearing"},
	} {
		s, ok := codes[code]
		if !ok || s.Debit != want[0] || s.Credit != want[1] {
			t.Errorf("%s: %+v, want Dr %s / Cr %s", code, s, want[0], want[1])
		}
	}
	// the opening stock rule wins over the adjustment rule (lower priority)
	if codes["DEF-INV-OPENING"].Priority >= codes["DEF-INV-ADJ"].Priority {
		t.Error("DEF-INV-OPENING must take precedence over DEF-INV-ADJ")
	}
	open := postingRule{Conditions: codes["DEF-INV-OPENING"].Cond}
	if ok, _ := open.matches(map[string]string{"movementType": "adjustment", "sourceType": "opening_stock"}); !ok {
		t.Error("DEF-INV-OPENING must match the opening stock movement")
	}
	if ok, _ := open.matches(map[string]string{"movementType": "adjustment", "sourceType": "adjustment"}); ok {
		t.Error("DEF-INV-OPENING must not match an ordinary adjustment")
	}
}

// TestCategoryCounterAccounts: which category account is the other side of
// a stock movement (FR-INV-01/02).
func TestCategoryCounterAccounts(t *testing.T) {
	a := itemAccounts{Inventory: "1152", COGS: "5120", Expense: "6640", Waste: "5180", Variance: "5190"}
	for _, c := range []struct{ movement, source, want string }{
		{"consumption", "sale", "5120"},
		{"issue", "sale", "5120"},
		{"issue", "banquet", "5120"},
		{"issue", "requisition", "6640"},
		{"waste", "waste", "5180"},
		{"adjustment", "adjustment", "5190"},
		{"opname", "opname", "5190"},
		{"adjustment", "opening_stock", ""},
		{"transfer_out", "transfer", "1152"},
		{"transfer_in", "transfer", "1152"},
		{"receipt", "goods_receipt", ""},
		{"production_in", "production", ""},
	} {
		if got := a.counter(c.movement, c.source); got != c.want {
			t.Errorf("%s/%s: got %q, want %q", c.movement, c.source, got, c.want)
		}
	}
	if got := (itemAccounts{}).counter("waste", "waste"); got != "" {
		t.Errorf("no category account: the rule decides, got %q", got)
	}
}
