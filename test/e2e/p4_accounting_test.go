package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/accounting"
	"oneclub/internal/billing"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/outbox"
)

func init() {
	resourceCRUDModules["accounting"] = true
	resourceCRUDSkip["accounting.posting_rule"] = "needs a debit / credit account or role: covered by TestP4AccountingLedgerCore"
	resourceCRUDSkip["accounting.recurring_journal"] = "balanced lines: covered by TestP4AccountingLedgerCore"
	resourceCRUDSkip["accounting.revenue_allocation_rule"] = "shares of 100%: covered by TestP4AccountingAutomaticPosting"
	resourceCRUDSkip["accounting.bank_account"] = "asset GL account: covered by TestP4AccountingLedgerCore"
	resourceCRUDSkip["accounting.tax_code"] = "covered by TestP4AccountingLedgerCore"
	resourceCRUDSkip["accounting.account_mapping"] = "covered by TestP4AccountingLedgerCore"
}

const accBase = "/api/v1/accounting"

// accMDR is a Super Admin client on the MDR property (the books of these
// tests start on 1 December 2025).
func accMDR(t *testing.T) *Client {
	c := *superAdmin(t, inst)
	c.Property = inst.MDR
	return &c
}

// accDispatch delivers the pending outbox events and waits until
// accounting has processed every event it consumes published so far (the
// background dispatcher may hold some of them at the same moment).
func accDispatch(t *testing.T) {
	t.Helper()
	upTo := time.Now()
	waitFor(t, 60*time.Second, "accounting to process the published events", func() bool {
		if _, err := inst.App.Dispatcher.DispatchPending(t.Context()); err != nil {
			t.Logf("dispatch: %v", err)
		}
		var pending int
		sysQueryRow(t, inst, `SELECT count(*) FROM platform.outbox o WHERE o.event_type = ANY ($1) AND o.occurred_at <= $2
			AND NOT EXISTS (SELECT 1 FROM platform.outbox_processed p WHERE p.event_id = o.id AND p.subscriber = 'accounting.post:' || o.event_type)`,
			[]any{accounting.ConsumedEvents, upTo}, &pending)
		return pending == 0
	})
}

// accPublish publishes an event of another module (inventory, procurement,
// commercial …) as its owner would, and delivers it.
func accPublish(t *testing.T, property uuid.UUID, eventType string, payload any) uuid.UUID {
	t.Helper()
	ctx := dbtx.System(context.Background())
	var eid uuid.UUID
	agg := uuid.New()
	if err := inst.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		eid, err = inst.App.Bus.Publish(ctx, tx, eventType, "test.event", &agg, &property, payload)
		return err
	}); err != nil {
		t.Fatalf("publish %s: %v", eventType, err)
	}
	accDispatch(t)
	return eid
}

// accProcessed returns how accounting processed an event.
func accProcessed(t *testing.T, eid uuid.UUID) (string, int) {
	t.Helper()
	var st string
	var n int
	sysQueryRow(t, inst, `SELECT status, cardinality(journal_ids) FROM accounting.processed_events WHERE event_id = $1`, []any{eid}, &st, &n)
	return st, n
}

// accBal is the balance (debit − credit) of an account at a property up to
// a date ("" = all).
func accBal(t *testing.T, property uuid.UUID, code, asOf string) decimal.Decimal {
	t.Helper()
	var s string
	sysQueryRow(t, inst, `SELECT coalesce(sum(l.debit - l.credit), 0)::text FROM accounting.journal_lines l JOIN accounting.accounts a ON a.id = l.account_id
		WHERE a.code = $1 AND l.property_id = $2 AND ($3 = '' OR l.journal_date <= $3::date)`, []any{code, property, asOf}, &s)
	return dec(s)
}

// accAccount returns the id of an account code.
func accAccount(t *testing.T, code string) string {
	t.Helper()
	var v string
	sysQueryRow(t, inst, `SELECT id::text FROM accounting.accounts WHERE code = $1`, []any{code}, &v)
	return v
}

// accJournalOf returns the journal posted for an event (first one).
func accJournalOf(t *testing.T, c *Client, eid uuid.UUID) map[string]any {
	t.Helper()
	var jid string
	sysQueryRow(t, inst, `SELECT journal_ids[1]::text FROM accounting.processed_events WHERE event_id = $1`, []any{eid}, &jid)
	return c.Must(200, "GET", accBase+"/journals/"+jid, nil).JSON()
}

// accLines sums the journal lines of a journal per account code (debit −
// credit).
func accLines(j map[string]any) map[string]decimal.Decimal {
	out := map[string]decimal.Decimal{}
	for _, l := range j["lines"].([]any) {
		m := l.(map[string]any)
		out[str(m["accountCode"])] = out[str(m["accountCode"])].Add(dec(m["debit"])).Sub(dec(m["credit"]))
	}
	return out
}

func accEq(t *testing.T, what string, got decimal.Decimal, want int64) {
	t.Helper()
	if !got.Equal(decimal.NewFromInt(want)) {
		t.Fatalf("%s: want %d, got %s", what, want, got)
	}
}

// accSysErr runs SQL as the application owner and returns its error.
func accSysErr(sql string, args ...any) error {
	ctx := dbtx.System(context.Background())
	return inst.DB.WithTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, sql, args...)
		return err
	})
}

// accBusinessDay is the current business date of a property.
func accBusinessDay(t *testing.T, c *Client) string {
	t.Helper()
	days := c.Must(200, "GET", "/api/v1/billing/business-days", nil).Items()
	for _, d := range days {
		if d["current"] == true {
			return str(d["businessDate"])
		}
	}
	t.Fatalf("no current business day: %v", days)
	return ""
}

// accSetupMDR opens the MDR book from 1 December 2025 (idempotent across
// the tests of this file).
func accSetupMDR(t *testing.T) *Client {
	t.Helper()
	c := accMDR(t)
	c.Must(200, "POST", accBase+"/book:load-template", map[string]any{"cutOverDate": "2025-12-01"})
	return c
}

// EP-16 / EP-23 / EP-27 / EP-29: book & chart of accounts template, master
// data (accounts, tax codes, cash & bank accounts, mappings, posting rules
// with versions), opening balances imported from Excel Finance with AP open
// items and their reconciliation, manual journals (below the threshold),
// reversal, accrual auto-reversal, recurring journals, the database
// enforcement of balance and append-only, soft-close / close / reopen of
// periods with late postings, year-end closing to retained earnings and
// the financial statements (P&L = change of earnings in the Balance Sheet,
// balanced Balance Sheet, Cash Flow, TB, GL, consolidated).
func TestP4AccountingLedgerCore(t *testing.T) {
	c := accMDR(t)
	st := c.Must(200, "GET", accBase+"/book", nil).JSON()
	if st["roles"] == nil || st["configuration"] == nil {
		t.Fatalf("setup status: %v", st)
	}
	lr := c.Must(200, "POST", accBase+"/book:load-template", map[string]any{"cutOverDate": "2025-12-01"}).JSON()
	if lr["book"].(map[string]any)["cutOverDate"] != "2025-12-01" || lr["book"].(map[string]any)["status"] != "setup" {
		t.Fatalf("book: %v", lr)
	}
	c.Must(409, "POST", accBase+"/book:load-template", map[string]any{"cutOverDate": "2026-01-01"})
	if n := len(c.Must(200, "GET", accBase+"/accounts?limit=500", nil).Items()); n < 100 {
		t.Fatalf("chart of accounts template: %d accounts", n)
	}
	if p := c.Must(200, "POST", accBase+"/periods:generate", map[string]any{"year": 2025}).Items(); len(p) != 12 {
		t.Fatalf("periods of 2025: %d", len(p))
	}

	// Master data: accounts, tax codes (Tax Configuration), mappings, cash & bank accounts.
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	acc := c.Must(201, "POST", accBase+"/accounts", map[string]any{"code": "4895" + sfx[:2], "name": "Test Income", "nameId": "Pendapatan Uji",
		"accountType": "revenue", "subtype": "revenue", "parentId": accAccount(t, "4800")}, "Idempotency-Key", newKey()).JSON()
	if acc["normalBalance"] != "credit" {
		t.Fatalf("normal balance derived from the type: %v", acc)
	}
	c.Must(200, "PATCH", accBase+"/accounts/"+str(acc["id"]), map[string]any{"description": "Edited"})
	c.Must(204, "DELETE", accBase+"/accounts/"+str(acc["id"]), nil)
	tc := c.Must(201, "POST", accBase+"/tax-codes", map[string]any{"code": "TST" + sfx[:3], "name": "Test tax", "kind": "local_tax", "ratePercent": "5",
		"accountId": accAccount(t, "2132")}, "Idempotency-Key", newKey()).JSON()
	c.Must(200, "PATCH", accBase+"/tax-codes/"+str(tc["id"]), map[string]any{"ratePercent": "6"})
	c.Must(204, "DELETE", accBase+"/tax-codes/"+str(tc["id"]), nil)
	mp := c.Must(201, "POST", accBase+"/account-mappings", map[string]any{"sourceCode": "XL-BANK-" + sfx, "sourceName": "Kas Bank (Excel)",
		"accountId": accAccount(t, "1121")}, "Idempotency-Key", newKey()).JSON()
	c.Must(200, "PATCH", accBase+"/account-mappings/"+str(mp["id"]), map[string]any{"notes": "Excel Finance row 12"})
	tmpMap := c.Must(201, "POST", accBase+"/account-mappings", map[string]any{"sourceCode": "XL-TMP-" + sfx, "accountId": accAccount(t, "1111")},
		"Idempotency-Key", newKey()).JSON()
	c.Must(204, "DELETE", accBase+"/account-mappings/"+str(tmpMap["id"]), nil)
	c.Must(422, "POST", accBase+"/bank-accounts", map[string]any{"code": "BAD" + sfx, "name": "Bad", "glAccountId": accAccount(t, "4110")},
		"Idempotency-Key", newKey())
	bank := c.Must(201, "POST", accBase+"/bank-accounts", map[string]any{"code": "BCA-" + sfx, "name": "BCA Operasional MDR", "kind": "bank",
		"bankName": "BCA", "accountNo": "1234567890", "glAccountId": accAccount(t, "1121"), "statementFormat": "generic_csv"}, "Idempotency-Key", newKey()).JSON()
	c.Must(200, "PATCH", accBase+"/bank-accounts/"+str(bank["id"]), map[string]any{"accountHolder": "PT Modern Golf"})
	tmpBank := c.Must(201, "POST", accBase+"/bank-accounts", map[string]any{"code": "TMP-" + sfx, "name": "Temp", "kind": "cash",
		"glAccountId": accAccount(t, "1111")}, "Idempotency-Key", newKey()).JSON()
	c.Must(204, "DELETE", accBase+"/bank-accounts/"+str(tmpBank["id"]), nil)

	// Posting rules: a custom rule, its new version from an effective date, defaults.
	c.Must(422, "POST", accBase+"/posting-rules", map[string]any{"code": "TST-NO-SIDE", "name": "No side", "source": "test.source"}, "Idempotency-Key", newKey())
	pr := c.Must(201, "POST", accBase+"/posting-rules", map[string]any{"code": "TST-RULE-" + sfx, "name": "Test rule", "source": "test.source",
		"conditions": map[string]any{"kind": "x"}, "debitRole": "cash", "creditAccountId": accAccount(t, "4890")}, "Idempotency-Key", newKey()).JSON()
	c.Must(200, "PATCH", accBase+"/posting-rules/"+str(pr["id"]), map[string]any{"name": "Test rule (edited)"})
	nv := c.Must(200, "POST", accBase+"/posting-rules/"+str(pr["id"])+":new-version", map[string]any{"effectiveFrom": "2030-01-01", "creditRole": "revenue_other"}).JSON()
	if nv["code"] != pr["code"] || nv["effectiveFrom"] != "2030-01-01" {
		t.Fatalf("new version: %v", nv)
	}
	if g := c.Must(200, "POST", accBase+"/posting-rules:generate-defaults", map[string]any{}).JSON(); g["created"].(float64) != 0 {
		t.Fatalf("defaults already generated by the template: %v", g)
	}

	// Opening balances from Excel Finance (preview with an issue, then commit).
	sup := idOf(c.Must(201, "POST", "/api/v1/procurement/suppliers", map[string]any{"code": "ACCSUP" + sfx, "name": "PT Pemasok Akuntansi " + sfx,
		"npwp": "01.234.567.8-901.000"}, "Idempotency-Key", newKey()))
	var supCode string
	sysQueryRow(t, inst, `SELECT code FROM procurement.suppliers WHERE id = $1`, []any{mustUUID(sup)}, &supCode)
	csv := "account,debit,credit,partner_type,partner,document_no,document_date,due_date,description\n" +
		"XL-BANK-" + sfx + ",500000000,,,,,,,Bank BCA\n" +
		"1111,10000000,,,,,,,Cash on hand\n" +
		"1181,5000000,,,,,,,Prepaid insurance\n" +
		"2111,,10000000,supplier," + supCode + ",VI-OLD-" + sfx + ",2025-11-20,2025-12-20,Open vendor invoice\n" +
		"3100,,400000000,,,,,,Share capital\n" +
		"3900,,105000000,,,,,,Opening balance equity\n"
	bad := strings.Replace(csv, "1181,", "99999,", 1)
	pv := c.Must(200, "POST", accBase+"/opening-balances:import", map[string]any{"content": bad, "description": "Excel Finance 30 Nov 2025", "preview": true}).JSON()
	if len(pv["issues"].([]any)) != 1 || pv["balanced"] == true {
		t.Fatalf("preview with an unknown account: %v", pv)
	}
	c.Must(422, "POST", accBase+"/opening-balances:import", map[string]any{"content": bad, "description": "Excel Finance 30 Nov 2025"})
	im := c.Must(200, "POST", accBase+"/opening-balances:import", map[string]any{"content": csv, "description": "Excel Finance 30 Nov 2025"}).JSON()
	batch := im["batch"].(map[string]any)
	if batch["balanceDate"] != "2025-11-30" || !dec(batch["totalDebit"]).Equal(decimal.NewFromInt(515_000_000)) || len(batch["lines"].([]any)) != 6 {
		t.Fatalf("imported batch: %v", batch)
	}
	draft := c.Must(201, "POST", accBase+"/opening-balances", map[string]any{"description": "Draft to drop", "lines": []map[string]any{
		{"accountCode": "1111", "debit": "1"}, {"accountCode": "3900", "credit": "1"}}}).JSON()
	c.Must(204, "DELETE", accBase+"/opening-balances/"+str(draft["id"]), nil)
	// posting the opening balances is the Finance Manager's sign-off (not the Accountant's)
	roleUser(t, inst, "accountant").Must(403, "POST", accBase+"/opening-balances/"+str(batch["id"])+":post", map[string]any{})
	posted := c.Must(200, "POST", accBase+"/opening-balances/"+str(batch["id"])+":post", map[string]any{}).JSON()
	if posted["status"] != "posted" || posted["journalId"] == nil {
		t.Fatalf("posted batch: %v", posted)
	}
	rec := c.Must(200, "GET", accBase+"/opening-balances/"+str(batch["id"])+"/reconciliation", nil).JSON()
	if rec["ok"] != true {
		t.Fatalf("opening reconciliation: %v", rec)
	}
	tb := c.Must(200, "GET", accBase+"/trial-balance?from=2025-11-01&to=2025-11-30&propertyId="+inst.MDR.String(), nil).JSON()
	if tb["balanced"] != true || !dec(tb["totalDebit"]).Equal(decimal.NewFromInt(515_000_000)) {
		t.Fatalf("opening trial balance: %v", tb)
	}
	if items := c.Must(200, "GET", accBase+"/payables?filter[supplierId]="+sup, nil).Items(); len(items) != 1 || items[0]["itemType"] != "opening" {
		t.Fatalf("opening AP item: %v", items)
	}
	accLedgerJournals(t, c, sfx)
}

// accLedgerJournals: manual journals, reversal, DB enforcement, accruals,
// recurring journals, periods, year-end and the statements (MDR).
func accLedgerJournals(t *testing.T, c *Client, sfx string) {
	line := func(code, debit, credit string) map[string]any {
		return map[string]any{"accountId": accAccount(t, code), "debit": debit, "credit": credit}
	}
	mj := func(date, typ, desc string, submit bool, lines ...map[string]any) map[string]any {
		return c.Must(201, "POST", accBase+"/manual-journals", map[string]any{"journalDate": date, "journalType": typ, "description": desc, "lines": lines,
			"submit": submit}).JSON()
	}
	// FR-ACC-02: below the approval threshold a manual journal posts at once.
	mj1 := mj("2025-12-10", "manual", "Sponsorship December", true, line("1122", "2000000", ""), line("4890", "", "2000000"))
	if mj1["status"] != "posted" || mj1["journalId"] == nil {
		t.Fatalf("posted manual journal: %v", mj1)
	}
	mj2 := mj("2025-12-10", "manual", "Repairs", false, line("6640", "500000", ""), line("1122", "", "500000"))
	if mj2["status"] != "draft" {
		t.Fatalf("draft: %v", mj2)
	}
	c.Must(200, "PATCH", accBase+"/manual-journals/"+str(mj2["id"]), map[string]any{"journalDate": "2025-12-10", "description": "Repairs (clubhouse)",
		"lines": []map[string]any{line("6640", "500000", ""), line("1122", "", "500000")}})
	mj2 = c.Must(200, "POST", accBase+"/manual-journals/"+str(mj2["id"])+":submit", map[string]any{}).JSON()
	if mj2["status"] != "posted" {
		t.Fatalf("submitted: %v", mj2)
	}
	c.Must(422, "POST", accBase+"/manual-journals", map[string]any{"journalDate": "2025-12-10", "description": "Unbalanced",
		"lines": []map[string]any{line("6640", "500000", ""), line("1122", "", "400000")}})
	mj3 := mj("2025-12-11", "manual", "Draft to cancel", false, line("6640", "1", ""), line("1122", "", "1"))
	if x := c.Must(200, "POST", accBase+"/manual-journals/"+str(mj3["id"])+":cancel", map[string]any{"reason": "not needed"}).JSON(); x["status"] != "cancelled" {
		t.Fatalf("cancelled: %v", x)
	}
	if l := c.Must(200, "GET", accBase+"/manual-journals?filter[status]=posted", nil).Items(); len(l) < 2 {
		t.Fatalf("posted manual journals: %v", l)
	}
	c.Must(200, "GET", accBase+"/manual-journals/"+str(mj2["id"]), nil)

	// FR-ACC-03: posted journals are corrected by a reversal only.
	j2 := str(mj2["journalId"])
	rev := c.Must(200, "POST", accBase+"/journals/"+j2+":reverse", map[string]any{"reason": "booked twice"}).JSON()
	if rev["journalType"] != "reversal" || str(rev["reversesJournalId"]) != j2 || rev["journalDate"] != "2025-12-10" {
		t.Fatalf("reversal: %v", rev)
	}
	if o := c.Must(200, "GET", accBase+"/journals/"+j2, nil).JSON(); o["status"] != "reversed" || str(o["reversedByJournalId"]) != str(rev["id"]) {
		t.Fatalf("reversed journal: %v", o)
	}
	c.Must(409, "POST", accBase+"/journals/"+j2+":reverse", map[string]any{"reason": "again"})
	c.Must(409, "POST", accBase+"/journals/"+str(rev["id"])+":reverse", map[string]any{"reason": "reverse the reversal"})
	if err := accSysErr(`UPDATE accounting.journals SET description = 'changed' WHERE id = $1`, mustUUID(j2)); err == nil {
		t.Fatal("a posted journal must not be updatable")
	}
	if err := accSysErr(`DELETE FROM accounting.journal_lines WHERE journal_id = $1`, mustUUID(j2)); err == nil {
		t.Fatal("journal lines must not be deletable")
	}
	if err := accSysErr(`WITH j AS (INSERT INTO accounting.journals (id, property_id, number, journal_date, period_id, journal_type, source_type, description,
		currency, total) SELECT gen_random_uuid(), $1, 'JV-UNBAL-' || $2, DATE '2025-12-11', p.id, 'manual', 'test', 'Unbalanced', 'IDR', 100
		FROM accounting.periods p WHERE p.property_id = $1 AND p.year = 2025 AND p.month = 12 RETURNING id)
		INSERT INTO accounting.journal_lines (id, property_id, journal_id, line_no, journal_date, account_id, debit, credit)
		SELECT gen_random_uuid(), $1, j.id, 1, DATE '2025-12-11', a.id, 100, 0 FROM j, accounting.accounts a WHERE a.code = '1111'`, inst.MDR, sfx); err == nil ||
		!strings.Contains(err.Error(), "not balanced") {
		t.Fatalf("the database must refuse an unbalanced journal: %v", err)
	}
	if jl := c.Must(200, "GET", accBase+"/journals?from=2025-12-01&to=2025-12-31&filter[journalType]=manual", nil).Items(); len(jl) < 2 {
		t.Fatalf("journals: %v", jl)
	}

	// FR-ACC-08: accruals reversed on the first day of the next period, recurring journals.
	mj("2025-12-31", "adjustment", "Electricity accrual December", true, line("6310", "300000", ""), line("2181", "", "300000"))
	acr := c.Must(201, "POST", accBase+"/manual-journals", map[string]any{"journalDate": "2025-12-31", "journalType": "adjustment",
		"description": "Water accrual December", "autoReverse": true, "submit": true, "lines": []map[string]any{line("6310", "200000", ""), line("2181", "", "200000")}}).JSON()
	if acr["reversalJournalId"] == nil {
		t.Fatalf("auto-reversed accrual: %v", acr)
	}
	if r := c.Must(200, "GET", accBase+"/journals/"+str(acr["reversalJournalId"]), nil).JSON(); r["journalDate"] != "2026-01-01" {
		t.Fatalf("accrual reversal date: %v", r)
	}
	rj := c.Must(201, "POST", accBase+"/recurring-journals", map[string]any{"code": "RENT-" + sfx, "name": "Equipment rental", "frequency": "monthly",
		"dayOfMonth": 28, "nextRunDate": "2025-12-28", "lines": []map[string]any{line("6640", "1000000", ""), line("2181", "", "1000000")}},
		"Idempotency-Key", newKey()).JSON()
	c.Must(422, "POST", accBase+"/recurring-journals", map[string]any{"code": "BAD-" + sfx, "name": "Unbalanced", "nextRunDate": "2025-12-28",
		"lines": []map[string]any{line("6640", "1000000", ""), line("2181", "", "1")}}, "Idempotency-Key", newKey())
	c.Must(200, "PATCH", accBase+"/recurring-journals/"+str(rj["id"]), map[string]any{"description": "Buggy rental contract"})
	tmp := c.Must(201, "POST", accBase+"/recurring-journals", map[string]any{"code": "TMP-" + sfx, "name": "Temp", "nextRunDate": "2030-01-28",
		"lines": []map[string]any{line("6640", "1", ""), line("2181", "", "1")}}, "Idempotency-Key", newKey()).JSON()
	c.Must(204, "DELETE", accBase+"/recurring-journals/"+str(tmp["id"]), nil)
	run := c.Must(200, "POST", accBase+"/recurring-journals:run", map[string]any{"asOf": "2026-01-31"}).JSON()
	if run["posted"].(float64) != 2 {
		t.Fatalf("recurring run: %v", run)
	}
	if again := c.Must(200, "POST", accBase+"/recurring-journals:run", map[string]any{"asOf": "2026-01-31"}).JSON(); again["posted"].(float64) != 0 {
		t.Fatalf("each due date posts once: %v", again)
	}
	if r := c.Must(200, "GET", accBase+"/recurring-journals/"+str(rj["id"]), nil).JSON(); r["nextRunDate"] != "2026-02-28" || r["runs"].(float64) != 2 {
		t.Fatalf("recurring journal after the run: %v", r)
	}
	c.Must(200, "PATCH", accBase+"/recurring-journals/"+str(rj["id"]), map[string]any{"status": "inactive"})

	// FR-ACC-06: Soft Closed accepts finance adjustments only.
	var dec2025 string
	for _, p := range c.Must(200, "GET", accBase+"/periods?year=2025", nil).Items() {
		if p["month"].(float64) == 12 {
			dec2025 = str(p["id"])
		}
	}
	if p := c.Must(200, "GET", accBase+"/periods/"+dec2025, nil).JSON(); len(p["checklist"].([]any)) != 5 || p["canClose"] != true {
		t.Fatalf("closing checklist: %v", p)
	}
	if p := c.Must(200, "POST", accBase+"/periods/"+dec2025+":soft-close", map[string]any{}).JSON(); p["status"] != "soft_closed" {
		t.Fatalf("soft close: %v", p)
	}
	c.Must(409, "POST", accBase+"/manual-journals", map[string]any{"journalDate": "2025-12-20", "description": "Manual in a soft-closed period", "submit": true,
		"lines": []map[string]any{line("1122", "1", ""), line("4890", "", "1")}})
	mj("2025-12-20", "adjustment", "Late sponsorship adjustment", true, line("1122", "100000", ""), line("4890", "", "100000"))

	// FR-ACC-07: year-end closing (revenue 2,100,000 − rent 1,000,000 − accruals 500,000).
	fy := c.Must(200, "POST", accBase+"/fiscal-years:close", map[string]any{"year": 2025}).JSON()
	accEq(t, "net income 2025", dec(fy["netIncome"]), 600_000)
	c.Must(409, "POST", accBase+"/fiscal-years:close", map[string]any{"year": 2025})
	if l := c.Must(200, "GET", accBase+"/fiscal-years", nil).Items(); len(l) != 1 {
		t.Fatalf("fiscal years: %v", l)
	}
	accEq(t, "retained earnings", accBal(t, inst.MDR, "3200", "2025-12-31"), -600_000)
	pl := c.Must(200, "GET", accBase+"/reports/profit-loss?from=2025-12-01&to=2025-12-31&compare=previous_period&propertyId="+inst.MDR.String(), nil).JSON()
	accEq(t, "P&L December (closing excluded)", dec(pl["totals"].(map[string]any)["netIncome"]), 600_000)
	if pl["compareFrom"] != "2025-11-01" {
		t.Fatalf("comparison period: %v", pl["compareFrom"])
	}
	bsDec := c.Must(200, "GET", accBase+"/reports/balance-sheet?to=2025-12-31&propertyId="+inst.MDR.String(), nil).JSON()
	if bsDec["balanced"] != true || !dec(bsDec["totals"].(map[string]any)["currentEarnings"]).IsZero() {
		t.Fatalf("balance sheet after the year-end closing: %v", bsDec["totals"])
	}

	// Closed: no journal of any source; the closed year cannot be reopened.
	if p := c.Must(200, "POST", accBase+"/periods/"+dec2025+":close", map[string]any{}).JSON(); p["status"] != "closed" {
		t.Fatalf("close: %v", p)
	}
	c.Must(409, "POST", accBase+"/periods/"+dec2025+":reopen", map[string]any{"reason": "audit correction"})
	c.Must(409, "POST", accBase+"/manual-journals", map[string]any{"journalDate": "2025-12-20", "journalType": "adjustment", "description": "Too late",
		"submit": true, "lines": []map[string]any{line("1122", "1", ""), line("4890", "", "1")}})
	var closedEvents int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.outbox WHERE event_type = 'accounting.period_closed' AND aggregate_id = $1`, []any{mustUUID(dec2025)},
		&closedEvents)
	if closedEvents != 2 {
		t.Fatalf("accounting.period_closed (soft closed + closed): %d", closedEvents)
	}
	ctx := dbtx.System(context.Background())
	var status string
	_ = inst.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		status, err = accounting.PeriodStatus(ctx, tx, inst.MDR, time.Date(2025, 12, 15, 0, 0, 0, 0, time.UTC))
		return err
	})
	if status != accounting.PeriodClosed {
		t.Fatalf("K10 period status: %s", status)
	}
	// A late operational event dated in the closed period posts on the first day of the next open period.
	late := accPublish(t, inst.MDR, "inventory.movement_posted", map[string]any{"movementId": uuid.New(), "number": "SMV-LATE-" + sfx,
		"movementType": "waste", "businessDate": "2025-12-20", "warehouseId": uuid.New(), "costCenter": "kitchen", "sourceType": "waste", "currency": "IDR",
		"totalCost": "75000", "lines": []map[string]any{{"itemId": uuid.New(), "quantity": "-3", "unitCost": "25000", "totalCost": "75000"}}})
	if st, n := accProcessed(t, late); st != "posted" || n != 1 {
		t.Fatalf("late waste movement: %s %d", st, n)
	}
	lj := accJournalOf(t, c, late)
	if lj["journalDate"] != "2026-01-01" || lj["requestedDate"] != "2025-12-20" {
		t.Fatalf("late posting date: %v", lj)
	}
	if ls := accLines(lj); !ls["5180"].Equal(decimal.NewFromInt(75000)) || !ls["1151"].Equal(decimal.NewFromInt(-75000)) {
		t.Fatalf("waste journal: %v", ls)
	}

	// January 2026: closed and reopened through approval (none configured: at once).
	var jan string
	for _, p := range c.Must(200, "POST", accBase+"/periods:generate", map[string]any{"year": 2026}).Items() {
		if p["month"].(float64) == 1 {
			jan = str(p["id"])
		}
	}
	c.Must(200, "POST", accBase+"/periods/"+jan+":close", map[string]any{})
	if p := c.Must(200, "POST", accBase+"/periods/"+jan+":reopen", map[string]any{"reason": "late supplier invoice"}).JSON(); p["status"] != "open" ||
		p["reopenStatus"] != "approved" {
		t.Fatalf("reopened: %v", p)
	}
	var reopened int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.outbox WHERE event_type = 'accounting.period_reopened' AND aggregate_id = $1`, []any{mustUUID(jan)}, &reopened)
	if reopened != 1 {
		t.Fatalf("accounting.period_reopened: %d", reopened)
	}

	// EP-22: P&L of January = change of the current earnings in the Balance Sheet; balanced statements.
	prop := "&propertyId=" + inst.MDR.String()
	plJan := c.Must(200, "GET", accBase+"/reports/profit-loss?from=2026-01-01&to=2026-01-31"+prop, nil).JSON()
	ni := dec(plJan["totals"].(map[string]any)["netIncome"])
	accEq(t, "net income January", ni, 200_000-1_000_000-75_000)
	bsJan := c.Must(200, "GET", accBase+"/reports/balance-sheet?to=2026-01-31&compare=previous_period"+prop, nil).JSON()
	if bsJan["balanced"] != true || !dec(bsJan["totals"].(map[string]any)["currentEarnings"]).Equal(ni) {
		t.Fatalf("balance sheet January: %v (net income %s)", bsJan["totals"], ni)
	}
	cf := c.Must(200, "GET", accBase+"/reports/cash-flow?from=2025-12-01&to=2026-01-31&compare=last_year"+prop, nil).JSON()
	if cf["balanced"] != true {
		t.Fatalf("cash flow: %v", cf["totals"])
	}
	tb := c.Must(200, "GET", accBase+"/trial-balance?from=2025-12-01&to=2026-01-31"+prop, nil).JSON()
	if tb["balanced"] != true {
		t.Fatalf("trial balance: %v", tb)
	}
	gl := c.Must(200, "GET", accBase+"/general-ledger?accountId="+accAccount(t, "1122")+"&from=2025-12-01&to=2025-12-31"+prop, nil).JSON()
	accEq(t, "GL 1122 December", dec(gl["closingBalance"]), 2_100_000)
	if len(gl["lines"].([]any)) != 4 {
		t.Fatalf("GL lines: %v", gl["lines"])
	}
	rbl := c.Must(200, "GET", accBase+"/reports/revenue-by-business-line?from=2025-12-01&to=2025-12-31"+prop, nil).JSON()
	accEq(t, "revenue by business line", dec(rbl["totals"].(map[string]any)["total"]), 2_100_000)
	if cons := c.Must(200, "GET", accBase+"/reports/profit-loss?from=2025-12-01&to=2025-12-31", nil).JSON(); cons["consolidated"] != true {
		t.Fatalf("consolidated P&L: %v", cons)
	}
	c.Must(404, "GET", accBase+"/reports/unknown", nil)
	for _, code := range []string{"accounting.general_ledger", "accounting.trial_balance", "accounting.profit_loss", "accounting.balance_sheet",
		"accounting.cash_flow", "accounting.ar_aging", "accounting.ap_aging", "accounting.bank_reconciliation", "accounting.deferred_revenue",
		"accounting.tax_ppn", "accounting.revenue_by_business_line", "accounting.posting_exception"} {
		if r := c.Must(200, "GET", "/api/v1/reporting/reports/"+code+"?params[from]=2025-12-01&params[to]=2026-01-31", nil).JSON(); r["rows"] == nil {
			t.Fatalf("%s: %v", code, r)
		}
	}
	fp := c.Must(200, "GET", "/api/v1/reporting/dashboards/financial-performance?from=2025-12-01&to=2026-01-31", nil).JSON()
	kpis := map[string]string{}
	for _, k := range fp["kpis"].([]any) {
		kpis[str(k.(map[string]any)["key"])] = str(k.(map[string]any)["value"])
	}
	if len(kpis) != 8 || kpis["revenue"] != "2100000" {
		t.Fatalf("financial performance: %v", kpis)
	}
}

// accSupplier creates a supplier of a property.
func accSupplier(t *testing.T, c *Client, sfx string) string {
	t.Helper()
	return idOf(c.Must(201, "POST", "/api/v1/procurement/suppliers", map[string]any{"code": "AS" + sfx, "name": "CV Sumber Makmur " + sfx,
		"npwp": "02.345.678.9-012.000"}, "Idempotency-Key", newKey()))
}

// EP-17 / EP-19 / EP-21 posting of the back office events (contracts:
// procurement.*, inventory.*, K3, K8 by name): goods receipts (GRNI), vendor
// invoices (AP, VAT input, input tax invoice), purchase returns, debit
// notes, service bills with withholding tax, payment runs with approval,
// execution and accounting.vendor_payment_made, inventory movements (COGS,
// waste, opname, production, transfers), depreciation, consignment,
// loyalty, instructor fees, revenue allocations; idempotency of a delivered
// event (three deliveries → one journal), suspense + exception for an
// unmapped event and its repost after the rule is added, invalid payloads
// in the exception queue.
func TestP4AccountingAutomaticPosting(t *testing.T) {
	c := accSetupMDR(t)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	today := time.Now().In(clubLoc(inst)).Format("2006-01-02")
	sup := accSupplier(t, c, sfx)
	bank := idOf(c.Must(201, "POST", accBase+"/bank-accounts", map[string]any{"code": "PAY-" + sfx, "name": "BCA Payments " + sfx, "kind": "bank",
		"glAccountId": accAccount(t, "1121")}, "Idempotency-Key", newKey()))

	// Goods receipt → Dr Inventory / Cr GRNI.
	gr := uuid.New()
	grPayload := map[string]any{"goodsReceiptId": gr, "number": "GR-" + sfx, "purchaseOrderId": uuid.New(), "poNumber": "PO-" + sfx, "supplierId": sup,
		"warehouseId": uuid.New(), "receivedDate": today, "currency": "IDR", "total": "1100000",
		"lines": []map[string]any{{"itemId": uuid.New(), "quantity": "110", "uomId": uuid.New(), "baseQuantity": "110", "unitCost": "10000",
			"baseUnitCost": "10000", "totalCost": "1100000"}}}
	e1 := accPublish(t, inst.MDR, "procurement.goods_received", grPayload)
	if st, n := accProcessed(t, e1); st != "posted" || n != 1 {
		t.Fatalf("goods receipt: %s %d", st, n)
	}
	if ls := accLines(accJournalOf(t, c, e1)); !ls["1151"].Equal(decimal.NewFromInt(1_100_000)) || !ls["2113"].Equal(decimal.NewFromInt(-1_100_000)) {
		t.Fatalf("GRNI journal: %v", ls)
	}
	// FR-PST-05: the same event delivered three times posts once; a second
	// event for the same document posts nothing.
	raw, _ := json.Marshal(grPayload)
	h := inst.App.Ledger.Module.Subscriptions()["procurement.goods_received"]
	ctx := dbtx.System(context.Background())
	for i := 0; i < 3; i++ {
		ev := outbox.Event{ID: e1, Type: "procurement.goods_received", PropertyID: &inst.MDR, Payload: raw, OccurredAt: time.Now()}
		if err := inst.DB.WithTx(ctx, func(tx pgx.Tx) error { return h(ctx, tx, ev) }); err != nil {
			t.Fatalf("redelivery %d: %v", i, err)
		}
	}
	e1b := accPublish(t, inst.MDR, "procurement.goods_received", grPayload)
	var journals int
	sysQueryRow(t, inst, `SELECT count(DISTINCT journal_id) FROM accounting.journal_lines WHERE source_id = $1`, []any{gr.String()}, &journals)
	if st, n := accProcessed(t, e1b); journals != 1 || st != "no_posting" || n != 0 {
		t.Fatalf("one journal per goods receipt: %d journals, second event %s %d", journals, st, n)
	}

	// Vendor invoice (3-way matched) → AP with VAT input and the input tax invoice.
	vi := uuid.New()
	e2 := accPublish(t, inst.MDR, "procurement.vendor_invoice_approved", map[string]any{"vendorInvoiceId": vi, "number": "VI-" + sfx,
		"supplierInvoiceNo": "INV/" + sfx, "supplierId": sup, "invoiceDate": today, "dueDate": today, "currency": "IDR", "subtotal": "1100000",
		"taxAmount": "121000", "total": "1221000", "withholding": "0", "taxInvoiceNo": "010.000-26." + sfx, "goodsReceiptIds": []string{gr.String()},
		"lines": []map[string]any{{"itemId": uuid.New(), "description": "Sabun bungalow", "quantity": "100", "unitPrice": "10000", "total": "1000000",
			"accountHint": "inventory"}, {"description": "Ongkos kirim", "quantity": "1", "unitPrice": "100000", "total": "100000", "accountHint": "expense"}}})
	vl := accLines(accJournalOf(t, c, e2))
	if !vl["2113"].Equal(decimal.NewFromInt(1_000_000)) || !vl["6640"].Equal(decimal.NewFromInt(100_000)) || !vl["1171"].Equal(decimal.NewFromInt(121_000)) ||
		!vl["2111"].Equal(decimal.NewFromInt(-1_221_000)) || !vl["1199"].IsZero() {
		t.Fatalf("vendor invoice journal: %v", vl)
	}
	if ti := c.Must(200, "GET", accBase+"/tax-invoices?filter[direction]=input&filter[sourceId]="+vi.String(), nil).Items(); len(ti) != 1 || ti[0]["ppn"] != "121000" {
		t.Fatalf("input tax invoice: %v", ti)
	}
	// Purchase return and debit note reduce GRNI and the payable (FR-AP-04).
	e3 := accPublish(t, inst.MDR, "procurement.purchase_returned", map[string]any{"purchaseReturnId": uuid.New(), "number": "PRT-" + sfx,
		"goodsReceiptId": gr, "supplierId": sup, "warehouseId": uuid.New(), "currency": "IDR", "total": "100000",
		"lines": []map[string]any{{"itemId": uuid.New(), "baseQuantity": "10", "baseUnitCost": "10000", "totalCost": "100000"}}})
	if ls := accLines(accJournalOf(t, c, e3)); !ls["2113"].Equal(decimal.NewFromInt(100_000)) || !ls["1151"].Equal(decimal.NewFromInt(-100_000)) {
		t.Fatalf("purchase return journal: %v", ls)
	}
	e4 := accPublish(t, inst.MDR, "procurement.debit_note_issued", map[string]any{"debitNoteId": uuid.New(), "number": "DN-" + sfx, "supplierId": sup,
		"vendorInvoiceId": vi, "amount": "100000", "currency": "IDR", "reason": "10 pcs returned"})
	if ls := accLines(accJournalOf(t, c, e4)); !ls["2111"].Equal(decimal.NewFromInt(100_000)) || !ls["2113"].Equal(decimal.NewFromInt(-100_000)) {
		t.Fatalf("debit note journal: %v", ls)
	}
	// A service bill without PO with PPh 23 withheld (FR-AP-01, FR-AP-05).
	vb := c.Must(201, "POST", accBase+"/vendor-bills", map[string]any{"supplierId": sup, "supplierInvoiceNo": "SVC/" + sfx, "invoiceDate": today,
		"taxAmount": "110000", "withholdingTaxCode": "PPH23", "lines": []map[string]any{{"accountId": accAccount(t, "6210"), "description": "Lawn mowing",
			"amount": "1000000", "costCenter": "course"}}}).JSON()
	if vb["withholding"] != "20000" || vb["amount"] != "1090000" || vb["itemType"] != "vendor_bill" {
		t.Fatalf("vendor bill: %v", vb)
	}
	c.Must(422, "POST", accBase+"/vendor-bills", map[string]any{"supplierId": sup, "supplierInvoiceNo": "X", "invoiceDate": today, "lines": []map[string]any{}})
	ag := c.Must(200, "GET", accBase+"/ap-aging?asOf="+today, nil).JSON()
	var mine map[string]any
	for _, r := range ag["rows"].([]any) {
		if m := r.(map[string]any); str(m["supplierId"]) == sup {
			mine = m
		}
	}
	if mine == nil || mine["total"] != "2211000" {
		t.Fatalf("AP aging of the supplier: %v", mine)
	}
	if p := c.Must(200, "GET", accBase+"/payables?filter[supplierId]="+sup+"&filter[status]=unpaid", nil).Items(); len(p) != 3 {
		t.Fatalf("payables: %v", p)
	}

	// Payment run (FR-AP-02): due payables of the supplier, debit note offset, approval, execution.
	run := c.Must(201, "POST", accBase+"/payment-runs", map[string]any{"paymentDate": today, "bankAccountId": bank, "supplierId": sup,
		"dueBy": "2099-12-31", "notes": "Weekly run"}).JSON()
	if run["total"] != "2211000" || len(run["lines"].([]any)) != 3 || run["status"] != "draft" {
		t.Fatalf("payment run: %v", run)
	}
	rid := str(run["id"])
	c.Must(409, "POST", accBase+"/payment-runs/"+rid+":execute", map[string]any{})
	if s := c.Must(200, "POST", accBase+"/payment-runs/"+rid+":submit", map[string]any{}).JSON(); s["status"] != "approved" {
		t.Fatalf("submitted run (no workflow: approved): %v", s)
	}
	ex := c.Must(200, "POST", accBase+"/payment-runs/"+rid+":execute", map[string]any{}).JSON()
	if ex["status"] != "executed" || ex["journalId"] == nil {
		t.Fatalf("executed run: %v", ex)
	}
	pj := accLines(c.Must(200, "GET", accBase+"/journals/"+str(ex["journalId"]), nil).JSON())
	if !pj["2111"].Equal(decimal.NewFromInt(2_211_000)) || !pj["1121"].Equal(decimal.NewFromInt(-2_211_000)) {
		t.Fatalf("payment journal: %v", pj)
	}
	var payload []byte
	sysQueryRow(t, inst, `SELECT payload FROM platform.outbox WHERE event_type = 'accounting.vendor_payment_made' AND payload->>'supplierId' = $1`, []any{sup}, &payload)
	var vp struct {
		Amount      string `json:"amount"`
		Allocations []struct {
			VendorInvoiceID string `json:"vendorInvoiceId"`
			Amount          string `json:"amount"`
		} `json:"allocations"`
	}
	_ = json.Unmarshal(payload, &vp)
	if vp.Amount != "2211000" || len(vp.Allocations) != 1 || vp.Allocations[0].VendorInvoiceID != vi.String() || vp.Allocations[0].Amount != "1221000" {
		t.Fatalf("accounting.vendor_payment_made: %s", payload)
	}
	if p := c.Must(200, "GET", accBase+"/payables?filter[supplierId]="+sup+"&filter[status]=unpaid", nil).Items(); len(p) != 0 {
		t.Fatalf("payables after the run: %v", p)
	}
	if vps := c.Must(200, "GET", accBase+"/vendor-payments?supplierId="+sup, nil).Items(); len(vps) != 1 {
		t.Fatalf("vendor payments: %v", vps)
	}
	if f := c.Must(200, "GET", accBase+"/payment-runs/"+rid+"/bank-file", nil); !strings.Contains(string(f.Body), "2211000") {
		t.Fatalf("bank file: %s", f.Body)
	}
	c.Must(200, "GET", accBase+"/payment-runs/"+rid, nil)
	if l := c.Must(200, "GET", accBase+"/payment-runs?filter[status]=executed", nil).Items(); len(l) == 0 {
		t.Fatal("executed runs")
	}
	vb2 := c.Must(201, "POST", accBase+"/vendor-bills", map[string]any{"supplierId": sup, "supplierInvoiceNo": "SVC2/" + sfx, "invoiceDate": today,
		"lines": []map[string]any{{"accountId": accAccount(t, "6630"), "description": "Stationery", "amount": "250000"}}}).JSON()
	run2 := c.Must(201, "POST", accBase+"/payment-runs", map[string]any{"paymentDate": today, "bankAccountId": bank,
		"items": []map[string]any{{"apItemId": vb2["id"], "amount": "100000"}}}).JSON()
	c.Must(409, "POST", accBase+"/payment-runs", map[string]any{"paymentDate": today, "bankAccountId": bank, "items": []map[string]any{{"apItemId": vb2["id"]}}})
	if x := c.Must(200, "POST", accBase+"/payment-runs/"+str(run2["id"])+":cancel", map[string]any{"reason": "wrong amount"}).JSON(); x["status"] != "cancelled" {
		t.Fatalf("cancelled run: %v", x)
	}

	// Inventory movements (EP-05): COGS per cost center, opname variance, production, transfers, receipts of GR.
	mv := func(typ, cc, src, qty, cost string) uuid.UUID {
		return accPublish(t, inst.MDR, "inventory.movement_posted", map[string]any{"movementId": uuid.New(), "number": "SMV-" + typ + "-" + sfx,
			"movementType": typ, "businessDate": today, "warehouseId": uuid.New(), "costCenter": cc, "sourceType": src, "currency": "IDR", "totalCost": cost,
			"lines": []map[string]any{{"itemId": uuid.New(), "quantity": qty, "unitCost": "1", "totalCost": cost}}})
	}
	if ls := accLines(accJournalOf(t, c, mv("consumption", "kitchen", "sale", "-2", "50000"))); !ls["5110"].Equal(decimal.NewFromInt(50_000)) ||
		!ls["1151"].Equal(decimal.NewFromInt(-50_000)) {
		t.Fatalf("COGS journal: %v", ls)
	}
	if ls := accLines(accJournalOf(t, c, mv("issue", "pro_shop", "sale", "-1", "40000"))); !ls["5120"].Equal(decimal.NewFromInt(40_000)) {
		t.Fatalf("pro shop COGS: %v", ls)
	}
	if ls := accLines(accJournalOf(t, c, mv("opname", "bar", "opname", "1", "10000"))); !ls["1151"].Equal(decimal.NewFromInt(10_000)) ||
		!ls["5190"].Equal(decimal.NewFromInt(-10_000)) {
		t.Fatalf("opname variance: %v", ls)
	}
	if ls := accLines(accJournalOf(t, c, mv("production_in", "kitchen", "production", "5", "30000"))); !ls["1151"].Equal(decimal.NewFromInt(30_000)) ||
		!ls["1159"].Equal(decimal.NewFromInt(-30_000)) {
		t.Fatalf("production: %v", ls)
	}
	if st, _ := accProcessed(t, mv("transfer_out", "kitchen", "transfer", "-1", "5000")); st != "no_posting" {
		t.Fatalf("transfer between locations has no ledger effect: %s", st)
	}
	if st, _ := accProcessed(t, mv("receipt", "", "goods_receipt", "10", "100000")); st != "no_posting" {
		t.Fatalf("stock receipt of a goods receipt is posted from procurement: %s", st)
	}
	// FR-PST-04: an unmapped movement goes to suspense and the exception queue; reposted once the rule exists.
	odd := mv("relabel", "bar", "relabel", "-1", "20000")
	if st, n := accProcessed(t, odd); st != "posted" || n != 1 {
		t.Fatalf("unmapped movement (suspense): %s %d", st, n)
	}
	if ls := accLines(accJournalOf(t, c, odd)); !ls["2199"].Equal(decimal.NewFromInt(20_000)) || !ls["1151"].Equal(decimal.NewFromInt(-20_000)) {
		t.Fatalf("suspense journal: %v", ls)
	}
	var exID string
	for _, x := range c.Must(200, "GET", accBase+"/posting-exceptions?filter[status]=open&filter[reason]=missing_rule", nil).Items() {
		if str(x["eventId"]) == odd.String() {
			exID = str(x["id"])
		}
	}
	if exID == "" {
		t.Fatal("posting exception of the unmapped movement")
	}
	c.Must(200, "GET", accBase+"/posting-exceptions/"+exID, nil)
	c.Must(201, "POST", accBase+"/posting-rules", map[string]any{"code": "INV-RELABEL-" + sfx, "name": "Relabel loss", "source": "inventory.movement_posted",
		"conditions": map[string]any{"movementType": "relabel"}, "debitRole": "inventory", "creditRole": "inventory_variance"}, "Idempotency-Key", newKey())
	rp := c.Must(200, "POST", accBase+"/posting-exceptions/"+exID+":repost", map[string]any{}).JSON()
	if rp["status"] != "resolved" || len(rp["resolutionJournalIds"].([]any)) != 1 {
		t.Fatalf("reposted exception: %v", rp)
	}
	fixed := accLines(c.Must(200, "GET", accBase+"/journals/"+str(rp["resolutionJournalIds"].([]any)[0]), nil).JSON())
	if !fixed["5190"].Equal(decimal.NewFromInt(20_000)) || !fixed["2199"].IsZero() {
		t.Fatalf("reposted journal: %v", fixed)
	}
	accEq(t, "suspense after the repost", accBal(t, inst.MDR, "2199", ""), 0)
	c.Must(409, "POST", accBase+"/posting-exceptions/"+exID+":repost", map[string]any{})
	bad := accPublish(t, inst.MDR, "procurement.goods_received", map[string]any{"number": "GR-BROKEN"})
	var badEx string
	sysQueryRow(t, inst, `SELECT id::text FROM accounting.posting_exceptions WHERE event_id = $1 AND reason = 'invalid_payload'`, []any{bad}, &badEx)
	c.Must(422, "POST", accBase+"/posting-exceptions/"+badEx+":ignore", map[string]any{})
	if x := c.Must(200, "POST", accBase+"/posting-exceptions/"+badEx+":ignore", map[string]any{"reason": "test event"}).JSON(); x["status"] != "ignored" {
		t.Fatalf("ignored exception: %v", x)
	}

	// Depreciation, consignment, loyalty, instructor fees.
	dep := accPublish(t, inst.MDR, "inventory.asset_depreciated", map[string]any{"runId": uuid.New(), "period": "2026-09", "total": "1500000", "currency": "IDR",
		"lines": []map[string]any{{"assetId": uuid.New(), "assetCode": "BUG-01", "category": "golf_cart", "amount": "1500000"}}})
	if j := accJournalOf(t, c, dep); j["journalDate"] != "2026-09-30" || !accLines(j)["6510"].Equal(decimal.NewFromInt(1_500_000)) {
		t.Fatalf("depreciation: %v", j)
	}
	con := accPublish(t, inst.MDR, "inventory.consignment_sold", map[string]any{"supplierId": sup, "itemId": uuid.New(), "quantity": "2", "unitCost": "50000",
		"totalCost": "100000", "currency": "IDR", "sourceType": "pos_order", "sourceId": uuid.New()})
	if ls := accLines(accJournalOf(t, c, con)); !ls["5130"].Equal(decimal.NewFromInt(100_000)) || !ls["2112"].Equal(decimal.NewFromInt(-100_000)) {
		t.Fatalf("consignment: %v", ls)
	}
	cust := uuid.New()
	earn := accPublish(t, inst.MDR, "crm.loyalty_points_changed", map[string]any{"accountId": uuid.New(), "customerId": cust, "kind": "earned", "points": 1000,
		"balance": 1000, "sourceType": "billing.payment", "sourceId": uuid.New()})
	if ls := accLines(accJournalOf(t, c, earn)); !ls["2161"].Equal(decimal.NewFromInt(-1000)) || !ls["6410"].Equal(decimal.NewFromInt(1000)) {
		t.Fatalf("loyalty earned: %v", ls)
	}
	exp := accPublish(t, inst.MDR, "crm.loyalty_points_changed", map[string]any{"accountId": uuid.New(), "customerId": cust, "kind": "expired", "points": -200,
		"balance": 800, "sourceType": "crm.loyalty_expiry", "sourceId": uuid.New()})
	if ls := accLines(accJournalOf(t, c, exp)); !ls["4810"].Equal(decimal.NewFromInt(-200)) {
		t.Fatalf("loyalty breakage: %v", ls)
	}
	if st, _ := accProcessed(t, accPublish(t, inst.MDR, "crm.loyalty_points_changed", map[string]any{"accountId": uuid.New(), "kind": "redeemed",
		"points": -100})); st != "no_posting" {
		t.Fatalf("redeemed points are posted with the loyalty tender: %s", st)
	}
	fee := accPublish(t, inst.MDR, "sportclub.instructor_fee_approved", map[string]any{"feeId": uuid.New(), "feeNo": "IF-" + sfx, "instructorId": uuid.New(),
		"amount": "750000", "periodStart": today, "periodEnd": today})
	if ls := accLines(accJournalOf(t, c, fee)); !ls["6120"].Equal(decimal.NewFromInt(750_000)) || !ls["2172"].Equal(decimal.NewFromInt(-750_000)) {
		t.Fatalf("instructor fee: %v", ls)
	}

	// K3 revenue allocations and K7/K8 events without own ledger effect are traced.
	accPublish(t, inst.MDR, "commercial.package_booked", map[string]any{"bookingId": uuid.New(), "number": "PKB-" + sfx, "packageId": uuid.New(),
		"packageCode": "WED-GOLD", "total": "5000000", "currency": "IDR", "components": []map[string]any{{"componentType": "fnb", "allocatedTotal": "3000000"},
			{"componentType": "reservation", "allocatedTotal": "2000000"}}})
	accPublish(t, inst.MDR, "commercial.package_consumed", map[string]any{"bookingId": uuid.New(), "bookingComponentId": uuid.New(), "packageId": uuid.New(),
		"componentType": "fnb", "businessDate": today, "quantity": "2", "revenue": map[string]any{"revenueComponent": "package", "total": "3000000"}})
	accPublish(t, inst.MDR, "commercial.promotion_applied", map[string]any{"promotionId": uuid.New(), "code": "RAMADAN", "sourceType": "pos_order",
		"sourceId": uuid.New(), "businessLine": "pos", "discount": "50000", "currency": "IDR"})
	if a := c.Must(200, "GET", accBase+"/revenue-allocations", nil).Items(); len(a) < 3 {
		t.Fatalf("revenue allocations: %v", a)
	}
	for _, ev := range []struct {
		typ     string
		payload map[string]any
	}{{"commercial.sale_completed", map[string]any{"orderId": uuid.New(), "orderNo": "ORD-" + sfx, "total": "100000"}},
		{"golf.round_finished", map[string]any{"flightId": uuid.New()}},
		{"commercial.shift_closed", map[string]any{"shiftId": uuid.New(), "shiftNo": "SHF-" + sfx, "variance": "0"}},
		{"membership.annual_fee_due", map[string]any{"feeId": uuid.New()}}} {
		if st, _ := accProcessed(t, accPublish(t, inst.MDR, ev.typ, ev.payload)); st != "no_posting" {
			t.Fatalf("%s: %s", ev.typ, st)
		}
	}
	if pe := c.Must(200, "GET", accBase+"/processed-events?filter[eventType]=procurement.goods_received", nil).Items(); len(pe) < 3 {
		t.Fatalf("processed events: %v", pe)
	}

	// Revenue allocation rules (FR-REV-01): the shares add up to 100 %.
	c.Must(422, "POST", accBase+"/revenue-allocation-rules", map[string]any{"code": "BAD-" + sfx, "name": "Bad", "matchComponent": "x",
		"effectiveFrom": "2025-01-01", "components": []map[string]any{{"component": "green_fee", "percent": "60"}}}, "Idempotency-Key", newKey())
	ra := c.Must(201, "POST", accBase+"/revenue-allocation-rules", map[string]any{"code": "GOLFPKG-" + sfx, "name": "Golf package split",
		"matchComponent": "golf_package_" + sfx, "effectiveFrom": "2025-01-01", "components": []map[string]any{{"component": "green_fee", "percent": "70"},
			{"component": "buggy_fee", "percent": "30"}}}, "Idempotency-Key", newKey()).JSON()
	c.Must(200, "PATCH", accBase+"/revenue-allocation-rules/"+str(ra["id"]), map[string]any{"name": "Golf package split (2025)"})
	tmpRule := c.Must(201, "POST", accBase+"/revenue-allocation-rules", map[string]any{"code": "TMPRA-" + sfx, "name": "Temp", "matchComponent": "tmp",
		"effectiveFrom": "2025-01-01", "components": []map[string]any{{"component": "other", "percent": "100"}}}, "Idempotency-Key", newKey()).JSON()
	c.Must(204, "DELETE", accBase+"/revenue-allocation-rules/"+str(tmpRule["id"]), nil)

	// Invoice price variance (procurement.invoice_price_variance → inventory.revaluation_posted):
	// stock part revalues the inventory, consumed part to price variance, both clear GRNI.
	rv := accPublish(t, inst.MDR, "inventory.revaluation_posted", map[string]any{"vendorInvoiceId": uuid.New(), "number": "VI-PPV-" + sfx,
		"goodsReceiptId": uuid.New(), "warehouseId": uuid.New(), "consumedTo": "price_variance", "currency": "IDR",
		"lines": []map[string]any{{"itemId": uuid.New(), "receivedQuantity": "10", "inStockQuantity": "6", "consumedQuantity": "4",
			"receivedUnitCost": "10000", "invoicedUnitCost": "15000", "stockAmount": "30000", "consumedAmount": "20000"}}})
	if ls := accLines(accJournalOf(t, c, rv)); !ls["1151"].Equal(decimal.NewFromInt(30_000)) || !ls["5190"].Equal(decimal.NewFromInt(20_000)) ||
		!ls["2113"].Equal(decimal.NewFromInt(-50_000)) {
		t.Fatalf("invoice price variance journal: %v", ls)
	}
}

// accEfaktur makes sure an e-Faktur (Coretax) sandbox integration is enabled.
func accEfaktur(t *testing.T) {
	t.Helper()
	var n int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.integrations WHERE capability = 'tax_invoice' AND enabled`, nil, &n)
	if n == 0 {
		platformAdmin(t, inst).Must(201, "POST", "/api/v1/platform/integrations", map[string]any{"code": "acct-efaktur", "adapter": "mock-efaktur",
			"name": "e-Faktur Sandbox (Accounting)", "enabled": true})
	}
}

// accTaxedCharge posts a charge with PPN 11 % (tax detail of the pricing
// snapshot) or a bundled charge without components.
func accTaxedCharge(t *testing.T, folio, component, desc string, net, tax int64, components any) {
	t.Helper()
	ctx := dbtx.System(context.Background())
	lc := billing.LineCharge{Charge: billing.Charge{FolioID: mustUUID(folio), Description: desc, Net: decimal.NewFromInt(net), Tax: decimal.NewFromInt(tax),
		Components: components}, BusinessLine: "other", RevenueComponent: component}
	if tax > 0 {
		lc.TaxLines = []map[string]any{{"code": "PPN", "name": "PPN 11%", "kind": "tax", "amount": fmt.Sprint(tax)}}
	}
	if err := inst.DB.WithTx(ctx, func(tx pgx.Tx) error {
		_, err := inst.App.Billing.AddLineCharge(ctx, tx, lc)
		return err
	}); err != nil {
		t.Fatalf("charge %s: %v", desc, err)
	}
}

// EP-18 / EP-21 / EP-17 on the billing documents of MDR: corporate invoice
// (AR ledger, output tax invoice uploaded to Coretax, cancelled and
// replaced, e-Faktur export, PPN report), partial payment, credit note,
// write-off and allowance, AR control = open invoices = AR Aging; the
// wedding down payment held as a deposit liability and released to revenue;
// the voucher 5x Rp635,000 (127,000 per redemption, breakage 254,000);
// revenue allocation of a bundled charge; the night audit (K4) posting the
// daily journal equal to the Daily Revenue Report; service charge pool; the
// control reconciliation.
func TestP4AccountingBillingAR(t *testing.T) {
	c := accSetupMDR(t)
	accEfaktur(t)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	day := accBusinessDay(t, c)
	m := inst.MDR
	bal := func(code string) decimal.Decimal { return accBal(t, m, code, "") }

	// Corporate invoice with a taxed line (K2).
	corp := idOf(c.Must(201, "POST", "/api/v1/crm/corporate-accounts", map[string]any{"code": "ACC" + sfx, "name": "PT Akuntansi " + sfx,
		"email": "ar" + sfx + "@acc.test", "npwp": "01.111.222.3-444.000", "address": "Jl. Thamrin 1, Jakarta"}, "Idempotency-Key", newKey()))
	cf := str(c.Must(200, "POST", "/api/v1/billing/customer-folios", map[string]any{"corporateAccountId": corp}).JSON()["id"])
	f1 := idOf(c.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"holderName": "PT Akuntansi seminar", "sourceRef": "Seminar"}))
	c.Must(201, "POST", "/api/v1/billing/folios/"+f1+"/lines", map[string]any{"chargeType": "other", "description": "Meeting room", "unitPrice": "4000000"})
	accTaxedCharge(t, f1, "other", "Seminar kit", 1_000_000, 110_000, nil)
	c.Must(200, "POST", "/api/v1/billing/customer-folios/"+cf+":merge", map[string]any{"folioIds": []string{f1}})
	inv := c.Must(201, "POST", "/api/v1/billing/invoices", map[string]any{"customerFolioId": cf, "corporateAccountId": corp, "termsDays": 30},
		"Idempotency-Key", newKey()).JSON()
	iid := str(inv["id"])
	c.Must(200, "POST", "/api/v1/billing/invoices/"+iid+":issue", nil)
	accDispatch(t)
	js := c.Must(200, "GET", accBase+"/journals?filter[sourceId]="+iid, nil).Items()
	if len(js) != 1 {
		t.Fatalf("invoice journal: %v", js)
	}
	if ls := accLines(c.Must(200, "GET", accBase+"/journals/"+str(js[0]["id"]), nil).JSON()); !ls["1142"].Equal(decimal.NewFromInt(5_110_000)) ||
		!ls["1143"].Equal(decimal.NewFromInt(-5_110_000)) {
		t.Fatalf("invoice issue journal: %v", ls)
	}

	// e-Faktur (FR-REV-07): draft from the invoice, upload, cancel with a replacement, export, PPN report.
	tis := c.Must(200, "GET", accBase+"/tax-invoices?filter[direction]=output&filter[sourceId]="+iid, nil).Items()
	if len(tis) != 1 || tis[0]["ppn"] != "110000" || tis[0]["dpp"] != "1000000" || tis[0]["status"] != "draft" || tis[0]["partnerNpwp"] == nil {
		t.Fatalf("output tax invoice: %v", tis)
	}
	tid := str(tis[0]["id"])
	up := c.Must(200, "POST", accBase+"/tax-invoices/"+tid+":upload", map[string]any{}).JSON()
	if up["status"] != "uploaded" || str(up["fakturNumber"]) == "" {
		t.Fatalf("uploaded tax invoice: %v", up)
	}
	c.Must(409, "POST", accBase+"/tax-invoices/"+tid+":upload", map[string]any{})
	var uploaded int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.outbox WHERE event_type = 'accounting.tax_invoice_uploaded' AND aggregate_id = $1`, []any{mustUUID(tid)}, &uploaded)
	if uploaded != 1 {
		t.Fatalf("accounting.tax_invoice_uploaded: %d", uploaded)
	}
	repl := c.Must(200, "POST", accBase+"/tax-invoices/"+tid+":cancel", map[string]any{"reason": "NPWP corrected", "replace": true}).JSON()
	if str(repl["replacesId"]) != tid || repl["status"] != "draft" {
		t.Fatalf("replacement tax invoice: %v", repl)
	}
	c.Must(200, "GET", accBase+"/tax-invoices/"+str(repl["id"]), nil)
	exp := c.Must(200, "POST", accBase+"/tax-invoices:export", map[string]any{"taxPeriod": day[:7]}).JSON()
	if !strings.Contains(str(exp["content"]), "FK") || exp["invoices"].(float64) < 1 {
		t.Fatalf("e-Faktur export: %v", exp)
	}
	if ppn := c.Must(200, "GET", accBase+"/ppn-report?period="+day[:7], nil).JSON(); dec(ppn["outputPpn"]).LessThan(decimal.NewFromInt(110_000)) {
		t.Fatalf("PPN report: %v", ppn)
	}

	// Partial payment, credit note (service and tax reversed proportionally), write-off.
	c.Must(200, "POST", "/api/v1/billing/invoices/"+iid+":pay", map[string]any{"methodType": "bank_transfer", "amount": "2000000", "reference": "TRF-" + sfx})
	cn := c.Must(201, "POST", "/api/v1/billing/credit-notes", map[string]any{"invoiceId": iid, "amount": "555000", "reason": "Projector failure"},
		"Idempotency-Key", newKey()).JSON()
	c.Must(200, "POST", "/api/v1/billing/invoices/"+iid+":write-off", map[string]any{"amount": "55000", "reason": "Bank charges absorbed"})
	accDispatch(t)
	cnj := c.Must(200, "GET", accBase+"/journals?filter[sourceId]="+str(cn["id"]), nil).Items()
	if len(cnj) != 1 {
		t.Fatalf("credit note journal: %v", cnj)
	}
	if ls := accLines(c.Must(200, "GET", accBase+"/journals/"+str(cnj[0]["id"]), nil).JSON()); !ls["1142"].Equal(decimal.NewFromInt(-555_000)) ||
		!ls["2131"].Equal(decimal.NewFromInt(11_947)) || !ls["4920"].Equal(decimal.NewFromInt(543_053)) {
		t.Fatalf("credit note journal: %v", ls)
	}
	c.Must(200, "POST", accBase+"/postings:run", map[string]any{"upTo": day})
	ents := c.Must(200, "GET", accBase+"/ar-entries?invoiceId="+iid, nil).Items()
	kinds := map[string]bool{}
	for _, e := range ents {
		kinds[str(e["entryType"])] = true
	}
	if !kinds["invoice"] || !kinds["payment"] || !kinds["credit_note"] || !kinds["write_off"] {
		t.Fatalf("AR ledger of the invoice: %v", ents)
	}
	if rs := c.Must(200, "GET", accBase+"/receivables", nil).Items(); len(rs) == 0 {
		t.Fatal("receivables")
	}
	// FR-AR-04/06 AC: AR control = open invoices of billing = AR Aging.
	ag := c.Must(200, "GET", accBase+"/ar-aging?asOf="+day, nil).JSON()
	if ag["difference"] != "0" || dec(ag["totals"].(map[string]any)["total"]).LessThan(decimal.NewFromInt(2_500_000)) {
		t.Fatalf("AR aging vs AR control: %v / %v", ag["totals"], ag["arControl"])
	}
	bag := c.Must(200, "GET", "/api/v1/billing/aging?asOf="+day, nil).JSON()
	if !dec(bag["totals"].(map[string]any)["total"]).Equal(dec(ag["totals"].(map[string]any)["total"])) {
		t.Fatalf("AR aging = operational ageing of billing: %v vs %v", ag["totals"], bag["totals"])
	}
	// FR-AR-05 allowance: 45 days later the open invoice is 31–60 days old (5 %).
	later := mustDate(day).AddDate(0, 0, 45).Format("2006-01-02")
	alw := c.Must(201, "POST", accBase+"/allowances", map[string]any{"asOf": later, "notes": "Quarter end"}).JSON()
	if alw["status"] != "posted" || dec(alw["adjustment"]).IsZero() {
		t.Fatalf("allowance run: %v", alw)
	}
	if l := c.Must(200, "GET", accBase+"/allowances", nil).Items(); len(l) == 0 {
		t.Fatal("allowance runs")
	}

	// Wedding down payment (FR-REV-02 AC): a deposit liability, released to revenue with the event charge.
	dep0, rev0 := bal("2141"), bal("4890")
	wf := idOf(c.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"holderName": "Wedding Andi & Sari " + sfx, "sourceRef": "Wedding"}))
	c.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": wf, "amount": "26400000", "methodType": "bank_transfer", "channel": "venue",
		"purpose": "deposit", "reference": "DP-" + sfx})
	c.Must(200, "POST", accBase+"/postings:run", map[string]any{"upTo": day})
	accEq(t, "deposit liability after the DP", bal("2141").Sub(dep0), -26_400_000)
	c.Must(201, "POST", "/api/v1/billing/folios/"+wf+"/lines", map[string]any{"chargeType": "other", "description": "Wedding package 300 pax", "unitPrice": "26400000"})
	deps := c.Must(200, "GET", "/api/v1/billing/deposits?filter[folioId]="+wf, nil).Items()
	if len(deps) != 1 {
		t.Fatalf("deposits: %v", deps)
	}
	c.Must(200, "POST", "/api/v1/billing/deposits/"+str(deps[0]["id"])+":apply", nil)
	c.Must(200, "POST", accBase+"/postings:run", map[string]any{"upTo": day})
	accEq(t, "deposit released", bal("2141").Sub(dep0), 0)
	accEq(t, "wedding revenue", bal("4890").Sub(rev0), -26_400_000)

	// Voucher 5x entry Rp635,000 (FR-REV-02/03 AC).
	cust := idOf(c.Must(201, "POST", "/api/v1/crm/customers", map[string]any{"code": "ACCV" + sfx, "name": "Voucher Buyer " + sfx,
		"email": "v" + sfx + "@acc.test"}, "Idempotency-Key", newKey()))
	vt := idOf(c.Must(201, "POST", "/api/v1/commercial/voucher-types", map[string]any{"code": "ACC5X" + sfx, "name": "Voucher Masuk 5x " + sfx,
		"kind": "quota", "category": "sport_entry", "unit": "entry", "faceValue": "5", "price": "635000", "validityMonths": 6,
		"applicableServices": []string{"facility_entry"}, "revenueComponent": "sport_entry"}, "Idempotency-Key", newKey()))
	liab0, sport0, brk0, clr0 := bal("2151"), bal("4210"), bal("4810"), bal("2159")
	sell := func() map[string]any {
		s := c.Must(201, "POST", "/api/v1/commercial/vouchers:sell", map[string]any{"voucherTypeId": vt, "customerId": cust,
			"payment": map[string]any{"methodType": "cash"}}, "Idempotency-Key", newKey()).JSON()
		return s["vouchers"].([]any)[0].(map[string]any)
	}
	redeem := func(code any) {
		c.Must(200, "POST", "/api/v1/commercial/vouchers:redeem", map[string]any{"code": code, "serviceType": "facility_entry"}, "Idempotency-Key", newKey())
	}
	v1 := sell()
	accDispatch(t)
	c.Must(200, "POST", accBase+"/postings:run", map[string]any{"upTo": day})
	accEq(t, "voucher sold: liability", bal("2151").Sub(liab0), -635_000)
	redeem(v1["code"])
	accDispatch(t)
	accEq(t, "first redemption recognises 127,000", bal("4210").Sub(sport0), -127_000)
	v2 := sell()
	for i := 0; i < 3; i++ {
		redeem(v2["code"])
	}
	sysExec(t, inst, `UPDATE commercial.vouchers SET expires_at = now() - interval '1 second' WHERE id = $1`, mustUUID(str(v2["id"])))
	if n, err := inst.App.Vouchers.RunVoucherExpiry(context.Background()); err != nil || n < 1 {
		t.Fatalf("voucher expiry: %d %v", n, err)
	}
	accDispatch(t)
	c.Must(200, "POST", accBase+"/postings:run", map[string]any{"upTo": day})
	accEq(t, "breakage of the 2 entries left", bal("4810").Sub(brk0), -254_000)
	accEq(t, "voucher revenue (4 redemptions)", bal("4210").Sub(sport0), -508_000)
	accEq(t, "voucher liability (4 entries of the first voucher)", bal("2151").Sub(liab0), -508_000)
	accEq(t, "deferred clearing nets out", bal("2159").Sub(clr0), 0)

	// Revenue allocation of a bundled charge without components (FR-REV-01).
	c.Must(201, "POST", accBase+"/revenue-allocation-rules", map[string]any{"code": "PKG-" + sfx, "name": "Golf package", "matchComponent": "golf_pkg_" + sfx,
		"effectiveFrom": "2025-01-01", "components": []map[string]any{{"component": "green_fee", "percent": "70"}, {"component": "buggy_fee", "percent": "30"}}},
		"Idempotency-Key", newKey())
	gf0, bg0 := bal("4110"), bal("4120")

	// Cashier shift, the bundled charge, the night audit (K4) and the posting reconciliation (FR-PST-07 AC).
	sh := c.Must(201, "POST", "/api/v1/billing/cashier-shifts", map[string]any{"station": "front_desk", "openingFloat": "500000"}).JSON()
	walk := idOf(c.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"holderName": "MDR walk-in " + sfx}))
	c.Must(201, "POST", "/api/v1/billing/folios/"+walk+"/lines", map[string]any{"chargeType": "other", "description": "Court rental", "unitPrice": "300000"})
	accTaxedCharge(t, walk, "golf_pkg_"+sfx, "Golf package", 1_000_000, 0, []map[string]any{})
	c.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": walk, "amount": "1300000", "methodType": "cash", "channel": "venue"})
	closed := c.Must(200, "POST", "/api/v1/billing/cashier-shifts/"+str(sh["id"])+":close", map[string]any{"countedCash": "1790000", "note": "Rp10.000 short"}).JSON()
	if !dec(closed["variance"]).Equal(decimal.NewFromInt(-10_000)) {
		t.Fatalf("closed shift: %v", closed)
	}
	over0 := bal("6910")
	if run := c.Must(200, "POST", "/api/v1/billing/business-days:night-audit", nil).JSON(); run["status"] != "completed" || run["businessDate"] != day {
		t.Fatalf("night audit: %v", run)
	}
	accDispatch(t)
	accEq(t, "allocated green fee (70 %)", bal("4110").Sub(gf0), -700_000)
	accEq(t, "allocated buggy fee (30 %)", bal("4120").Sub(bg0), -300_000)
	accEq(t, "cash short of the shift", bal("6910").Sub(over0), 10_000)
	pr := c.Must(200, "GET", accBase+"/posting-reconciliation?date="+day, nil).JSON()
	if pr["dayClosed"] != true || pr["ok"] != true || dec(pr["journal"]).IsZero() || !dec(pr["journal"]).Equal(dec(pr["dailyRevenue"])) {
		t.Fatalf("posting reconciliation of the business day: %v", pr)
	}
	dr := c.Must(200, "GET", "/api/v1/billing/daily-revenue?date="+day, nil).JSON()
	if !dec(dr["charges"]).Equal(dec(pr["dailyRevenue"])) {
		t.Fatalf("Daily Revenue Report %v vs posting reconciliation %v", dr["charges"], pr["dailyRevenue"])
	}

	// Service charge pool (FR-REV-06): 5 % reserve, distribution basis per line.
	c.Must(201, "POST", accBase+"/manual-journals", map[string]any{"journalDate": day, "journalType": "adjustment", "description": "Service charge collected",
		"submit": true, "lines": []map[string]any{{"accountId": accAccount(t, "1122"), "debit": "1000000"},
			{"accountId": accAccount(t, "2121"), "credit": "1000000", "businessLine": "pos"}}})
	pool := c.Must(200, "POST", accBase+"/service-charge-pools", map[string]any{"year": mustDate(day).Year(), "month": int(mustDate(day).Month())}).JSON()
	if !dec(pool["collected"]).Equal(decimal.NewFromInt(1_000_000)) || pool["reserve"] != "50000" || pool["distributable"] != "950000" {
		t.Fatalf("service charge pool: %v", pool)
	}
	if a := c.Must(200, "POST", accBase+"/service-charge-pools/"+str(pool["id"])+":approve", map[string]any{}).JSON(); a["status"] != "approved" {
		t.Fatalf("approved pool: %v", a)
	}
	c.Must(409, "POST", accBase+"/service-charge-pools", map[string]any{"year": mustDate(day).Year(), "month": int(mustDate(day).Month())})
	if l := c.Must(200, "GET", accBase+"/service-charge-pools", nil).Items(); len(l) == 0 {
		t.Fatal("service charge pools")
	}

	// G6 / FR-AR-06: control accounts = sub-ledgers.
	dfr := c.Must(200, "GET", accBase+"/deferred-revenue?to="+day, nil).JSON()
	for _, r := range dfr["rows"].([]any) {
		if m := r.(map[string]any); m["difference"] != "0" {
			t.Fatalf("deferred revenue %v: GL %v ≠ sub-ledger %v", m["type"], m["glBalance"], m["subledger"])
		}
	}
	if rec := c.Must(200, "GET", accBase+"/reconciliation?asOf="+day, nil).JSON(); rec["ok"] != true {
		t.Fatalf("control reconciliation: %v", rec["checks"])
	}
}

func mustDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

// accMain is a Super Admin client on MAIN.
func accMain(t *testing.T) *Client {
	c := *superAdmin(t, inst)
	c.Property = inst.Main
	return &c
}

// accPolicy stores a version of an accounting club policy for a property.
func accPolicy(t *testing.T, c *Client, property uuid.UUID, category, code string, value any) {
	t.Helper()
	c.Must(201, "POST", "/api/v1/platform/club-policies", map[string]any{"category": category, "code": code, "name": code, "propertyId": property,
		"value": value})
}

// EP-17 acceptance criteria on MAIN (golf posted per transaction by the
// Accounting Configuration): the member rate golf all-in Rp640,000 paid in
// cash posts Dr cash 640,000 / Cr PPN output, green fee, buggy, HIO and
// the caddy fee liability of the snapshot components; the
// billing.payment_settled event delivered three times posts one journal.
// EP-23 on MAIN: the book opens at the business date with the opening
// balances pulled from the operational sub-ledgers. FR-ACC-02: a manual
// journal above the threshold waits for the Finance Manager's approval.
func TestP4AccountingGolfPerTransaction(t *testing.T) {
	c := accMain(t)
	day := accBusinessDay(t, c)
	lr := c.Must(200, "POST", accBase+"/book:load-template", map[string]any{"cutOverDate": day}).JSON()
	if lr["book"].(map[string]any)["cutOverDate"] != day {
		t.Fatalf("MAIN book: %v", lr)
	}
	ob := c.Must(201, "POST", accBase+"/opening-balances", map[string]any{"description": "MAIN opening (Excel Finance + sub-ledgers)", "includeSubledgers": true,
		"lines": []map[string]any{{"accountCode": "1111", "debit": "25000000"}, {"accountCode": "1121", "debit": "475000000"},
			{"accountCode": "3100", "credit": "500000000"}}}).JSON()
	if ob["balanceDate"] != mustDate(day).AddDate(0, 0, -1).Format("2006-01-02") || !dec(ob["totalDebit"]).Equal(dec(ob["totalCredit"])) {
		t.Fatalf("MAIN opening batch: %v", ob)
	}
	c.Must(200, "POST", accBase+"/opening-balances/"+str(ob["id"])+":post", map[string]any{})
	if rec := c.Must(200, "GET", accBase+"/opening-balances/"+str(ob["id"])+"/reconciliation", nil).JSON(); rec["ok"] != true {
		t.Fatalf("MAIN opening reconciliation: %v", rec)
	}

	// FR-POL-P4-03: golf posts per transaction on MAIN.
	sa := superAdmin(t, inst)
	accPolicy(t, sa, inst.Main, "Accounting Configuration", accounting.ConfigCode, map[string]any{"postingModes": map[string]string{"golf": "per_transaction"}})
	t.Cleanup(func() {
		accPolicy(t, sa, inst.Main, "Accounting Configuration", accounting.ConfigCode, map[string]any{"postingModes": map[string]string{"golf": "daily_summary"}})
	})
	if st := c.Must(200, "GET", accBase+"/book", nil).JSON(); st["configuration"].(map[string]any)["postingModes"].(map[string]any)["golf"] != "per_transaction" {
		t.Fatalf("configuration in force: %v", st["configuration"])
	}

	gm := login(t, inst, "golf.manager@demo.oneclub.id", demoPassword)
	cashier := login(t, inst, "cashier@demo.oneclub.id", demoPassword)
	course := demoCourse(t, inst)
	playDay := clubDay(inst, 11, isWeekday)
	slot := slotsOf(teeTimes(t, gm, course, playDay), "afternoon", 10)[18]
	// a single player: this tee time accepts one player (template minimum 1)
	sysExec(t, inst, `UPDATE golf.tee_times SET min_players = 1 WHERE id = $1`, mustUUID(str(slot["id"])))
	bk := gm.Must(201, "POST", "/api/v1/golf/bookings", map[string]any{"bookingType": "member", "channel": "back_office", "teeTimeId": slot["id"],
		"players": []map[string]any{{"playerType": "member", "memberNo": "D0001"}}}).JSON()
	eqAmount(t, "member rate all-in", bk["folio"].(map[string]any)["charges"], 640000)
	pay := payFolio(t, cashier, bk)
	accDispatch(t)
	pid := str(pay["id"])
	js := c.Must(200, "GET", accBase+"/journals?filter[sourceId]="+pid, nil).Items()
	if len(js) != 1 {
		t.Fatalf("journal of the payment: %v", js)
	}
	j := c.Must(200, "GET", accBase+"/journals/"+str(js[0]["id"]), nil).JSON()
	ls := accLines(j)
	credit := decimal.Zero
	for code, v := range ls {
		if code != "1111" {
			credit = credit.Add(v)
		}
	}
	if !ls["1111"].Equal(decimal.NewFromInt(640_000)) || !credit.Equal(decimal.NewFromInt(-640_000)) || !ls["2199"].IsZero() {
		t.Fatalf("golf all-in journal (cash 640,000 = credits): %v", ls)
	}
	for _, code := range []string{"2131", "4110", "4120", "4140", "2171"} {
		if !ls[code].IsNegative() {
			t.Fatalf("golf all-in journal misses a credit on %s (PPN, green fee, buggy, HIO, caddy fee liability): %v", code, ls)
		}
	}

	// billing.payment_settled delivered three times: one journal.
	var eid uuid.UUID
	var raw string
	sysQueryRow(t, inst, `SELECT id, payload::text FROM platform.outbox WHERE event_type = 'billing.payment_settled' AND aggregate_id = $1`,
		[]any{mustUUID(pid)}, &eid, &raw)
	h := inst.App.Ledger.Module.Subscriptions()["billing.payment_settled"]
	ctx := dbtx.System(context.Background())
	for i := 0; i < 3; i++ {
		ev := outbox.Event{ID: eid, Type: "billing.payment_settled", PropertyID: &inst.Main, Payload: []byte(raw), OccurredAt: time.Now()}
		if err := inst.DB.WithTx(ctx, func(tx pgx.Tx) error { return h(ctx, tx, ev) }); err != nil {
			t.Fatalf("redelivery %d: %v", i, err)
		}
	}
	var journals int
	sysQueryRow(t, inst, `SELECT count(*) FROM accounting.journals WHERE source_id = $1`, []any{pid}, &journals)
	if journals != 1 {
		t.Fatalf("payment_settled ×3: %d journals", journals)
	}

	// FR-ACC-02: a manual journal of the threshold (Rp10,000,000) or more waits for the Finance Manager.
	wf := sa.Must(201, "POST", "/api/v1/platform/approval-workflows", map[string]any{"documentType": accounting.ManualJournalDocumentType.Code,
		"name": "Manual journals MAIN", "propertyId": inst.Main, "steps": []map[string]any{{"stepNo": 1, "name": "Finance Manager", "approverType": "role",
			"approverRoleId": roleID(t, sa, "finance_manager")}}}).JSON()
	t.Cleanup(func() {
		sa.Must(200, "PATCH", "/api/v1/platform/approval-workflows/"+str(wf["id"]), map[string]any{"status": "inactive"})
	})
	acct := roleUser(t, inst, "accountant")
	acct.Property = inst.Main
	fm := roleUser(t, inst, "finance_manager")
	fm.Property = inst.Main
	big := acct.Must(201, "POST", accBase+"/manual-journals", map[string]any{"journalDate": day, "journalType": "adjustment",
		"description": "Reclass of the opening cash to the bank", "submit": true, "lines": []map[string]any{
			{"accountId": accAccount(t, "1121"), "debit": "12000000"}, {"accountId": accAccount(t, "1111"), "credit": "12000000"}}}).JSON()
	if big["status"] != "pending_approval" || big["approvalRequestId"] == nil || big["journalId"] != nil {
		t.Fatalf("manual journal above the threshold: %v", big)
	}
	fm.Must(200, "POST", "/api/v1/platform/approvals/"+str(big["approvalRequestId"])+":approve", map[string]any{})
	got := c.Must(200, "GET", accBase+"/manual-journals/"+str(big["id"]), nil).JSON()
	if got["status"] != "posted" || got["journalId"] == nil {
		t.Fatalf("approved manual journal: %v", got)
	}
	if pj := c.Must(200, "GET", accBase+"/journals/"+str(got["journalId"]), nil).JSON(); pj["approvedBy"] == nil || pj["status"] != "posted" {
		t.Fatalf("journal of the approved request: %v", pj)
	}
}

// EP-20 Cash & Bank (MDR): cash, petty cash and bank accounts, the deposit
// of a closed cashier shift, petty cash expense / replenishment, transfer
// and count with variance, bank statements (CSV and MT940, duplicates
// skipped) and the reconciliation: gateway settlement and shift deposits
// auto-matched on the same date (AC), manual match / unmatch, bank charges
// settled with a journal, ignored lines, completion without difference.
func TestP4AccountingCashBank(t *testing.T) {
	c := accSetupMDR(t)
	day := accBusinessDay(t, c)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	bankGL := c.Must(201, "POST", accBase+"/accounts", map[string]any{"code": "1129" + sfx[:3], "name": "Bank BNI " + sfx, "accountType": "asset",
		"subtype": "bank", "cashFlow": "cash", "parentId": accAccount(t, "1100")}, "Idempotency-Key", newKey()).JSON()
	pettyGL := c.Must(201, "POST", accBase+"/accounts", map[string]any{"code": "1119" + sfx[:3], "name": "Petty cash ProShop " + sfx, "accountType": "asset",
		"subtype": "cash", "cashFlow": "cash", "parentId": accAccount(t, "1100")}, "Idempotency-Key", newKey()).JSON()
	newAcct := func(code, kind string, gl any) string {
		return idOf(c.Must(201, "POST", accBase+"/bank-accounts", map[string]any{"code": code + sfx, "name": code + " " + sfx, "kind": kind, "glAccountId": gl,
			"statementFormat": "generic_csv"}, "Idempotency-Key", newKey()))
	}
	bank := newAcct("BNI", "bank", bankGL["id"])
	petty := newAcct("PETTY", "petty_cash", pettyGL["id"])
	cashier := newAcct("CASHIER", "cash", accAccount(t, "1111"))
	bal := func(code string) decimal.Decimal { return accBal(t, inst.MDR, code, "") }

	// A cashier shift: float 200,000, cash takings 300,000, counted 500,000.
	sh := c.Must(201, "POST", "/api/v1/billing/cashier-shifts", map[string]any{"station": "golf", "openingFloat": "200000"}).JSON()
	f := idOf(c.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"holderName": "Cash walk-in " + sfx}))
	c.Must(201, "POST", "/api/v1/billing/folios/"+f+"/lines", map[string]any{"chargeType": "other", "description": "Court rental", "unitPrice": "300000"})
	c.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": f, "amount": "300000", "methodType": "cash", "channel": "venue"})
	c.Must(200, "POST", "/api/v1/billing/cashier-shifts/"+str(sh["id"])+":close", map[string]any{"countedCash": "500000"})
	var shift map[string]any
	for _, s := range c.Must(200, "GET", accBase+"/cash-shifts?from="+day+"&to="+day, nil).Items() {
		if s["number"] == sh["number"] {
			shift = s
		}
	}
	if shift == nil || shift["toDeposit"] != "300000" || shift["deposited"] != false {
		t.Fatalf("closed shift to deposit: %v", shift)
	}

	// FR-BNK-04/05: deposits, transfer, replenishment, petty cash expense and count.
	cash := func(body map[string]any) map[string]any {
		body["txDate"] = day
		return c.Must(201, "POST", accBase+"/cash-transactions", body).JSON()
	}
	cash1111 := bal("1111")
	dep := cash(map[string]any{"kind": "deposit", "fromAccountId": cashier, "toAccountId": bank, "amount": "300000", "shiftRefs": []any{sh["number"]},
		"reference": "SETOR-" + sfx})
	if dep["journalId"] == nil || !dec(dep["variance"]).IsZero() {
		t.Fatalf("shift deposit: %v", dep)
	}
	c.Must(422, "POST", accBase+"/cash-transactions", map[string]any{"kind": "deposit", "txDate": day, "fromAccountId": cashier, "toAccountId": bank,
		"amount": "300000", "shiftRefs": []any{sh["number"]}})
	cash(map[string]any{"kind": "deposit", "fromAccountId": cashier, "toAccountId": bank, "amount": "50000", "expected": "50000", "description": "Pro shop takings"})
	cash(map[string]any{"kind": "transfer", "fromAccountId": cashier, "toAccountId": bank, "amount": "5000000", "description": "Excess cash to the bank"})
	accEq(t, "cash on hand deposited / transferred", bal("1111").Sub(cash1111), -5_350_000)
	cash(map[string]any{"kind": "replenishment", "fromAccountId": bank, "toAccountId": petty, "amount": "1000000"})
	cash(map[string]any{"kind": "expense", "fromAccountId": petty, "amount": "150000", "expenseAccountId": accAccount(t, "6640"), "costCenter": "PROSHOP",
		"description": "Office supplies"})
	cash(map[string]any{"kind": "transfer", "fromAccountId": petty, "toAccountId": cashier, "amount": "100000"})
	over0 := bal("6910")
	cnt := cash(map[string]any{"kind": "count", "fromAccountId": petty, "amount": "740000"})
	if !dec(cnt["variance"]).Equal(decimal.NewFromInt(-10_000)) {
		t.Fatalf("petty cash count: %v", cnt)
	}
	accEq(t, "petty cash after the count", bal(str(pettyGL["code"])), 740_000)
	accEq(t, "cash short of the count", bal("6910").Sub(over0), 10_000)
	if l := c.Must(200, "GET", accBase+"/cash-transactions?filter[kind]=deposit", nil).Items(); len(l) < 2 {
		t.Fatalf("deposits: %v", l)
	}

	// The gateway settlement of the day (P1 payment reconciliation) goes to this bank by a posting rule.
	gw := "accgw" + sfx
	c.Must(201, "POST", accBase+"/posting-rules", map[string]any{"code": "GW-BNI-" + sfx, "name": "Gateway " + gw + " to BNI", "source": "billing.gateway_settlement",
		"conditions": map[string]any{"integration": gw}, "debitAccountId": bankGL["id"], "creditRole": "gateway_clearing", "priority": 10},
		"Idempotency-Key", newKey())
	sysExec(t, inst, `INSERT INTO billing.reconciliations (id, property_id, business_date, integration_code, status, oneclub_count, oneclub_total, settlement_count,
		settlement_total, exception_count) VALUES ($1, $2, $3::date, $4, 'matched', 3, 1010000, 3, 1000000, 0)`, uuid.New(), inst.MDR, day, gw)
	fee0 := bal("6620")
	c.Must(200, "POST", accBase+"/postings:run", map[string]any{"upTo": day})
	accEq(t, "gateway fee", bal("6620").Sub(fee0), 10_000)
	accEq(t, "bank book balance", bal(str(bankGL["code"])), 5_350_000-1_000_000+1_000_000)

	// FR-BNK-02: statements (CSV; MT940 on another account), re-import skips duplicates.
	csv := "Account statement BNI " + sfx + "\n" +
		"date,description,reference,amount,balance\n" +
		day + ",SETORAN TUNAI SHIFT,SETOR-" + sfx + ",350000,\n" +
		day + ",TRANSFER DARI KAS,,5000000,\n" +
		day + ",PENGISIAN KAS KECIL,,-1000000,\n" +
		day + ",SETTLEMENT " + gw + ",,1000000,\n" +
		day + ",BIAYA ADMINISTRASI,,-25000,\n" +
		day + ",KOREKSI BANK,,777,\n" +
		day + ",KOREKSI BANK BATAL,,-777,5325000\n"
	if pv := c.Must(200, "POST", accBase+"/bank-transactions:import", map[string]any{"bankAccountId": bank, "content": csv, "preview": true}).JSON(); pv["lines"].(float64) != 7 ||
		pv["statementId"] != nil {
		t.Fatalf("statement preview: %v", pv)
	}
	im := c.Must(200, "POST", accBase+"/bank-transactions:import", map[string]any{"bankAccountId": bank, "content": csv, "filename": "bni.csv"}).JSON()
	if im["imported"].(float64) != 7 || im["duplicates"].(float64) != 0 {
		t.Fatalf("statement import: %v", im)
	}
	if again := c.Must(200, "POST", accBase+"/bank-transactions:import", map[string]any{"bankAccountId": bank, "content": csv}).JSON(); again["imported"].(float64) != 0 ||
		again["duplicates"].(float64) != 7 {
		t.Fatalf("re-import: %v", again)
	}
	yymmdd := mustDate(day).Format("060102")
	mt := ":20:STMT" + sfx + "\n:25:9876543210\n:28C:1/1\n:60F:C" + yymmdd + "IDR0,00\n" +
		":61:" + yymmdd + yymmdd[2:] + "C1500000,00NTRFNONREF\n:86:TRANSFER MASUK PT ABC\n" +
		":61:" + yymmdd + yymmdd[2:] + "D25000,00NCHGNONREF\n:86:BIAYA ADMIN\n:62F:C" + yymmdd + "IDR1475000,00\n-\n"
	mandiri := c.Must(201, "POST", accBase+"/bank-accounts", map[string]any{"code": "MDRI" + sfx, "name": "Mandiri " + sfx, "kind": "bank",
		"glAccountId": accAccount(t, "1122"), "statementFormat": "mt940"}, "Idempotency-Key", newKey()).JSON()
	if m := c.Must(200, "POST", accBase+"/bank-transactions:import", map[string]any{"bankAccountId": mandiri["id"], "content": mt}).JSON(); m["imported"].(float64) != 2 ||
		m["closingBalance"] != "1475000" || m["format"] != "mt940" {
		t.Fatalf("MT940 import: %v", m)
	}
	if l := c.Must(200, "GET", accBase+"/bank-transactions?filter[bankAccountId]="+bank+"&filter[status]=unmatched", nil).Items(); len(l) != 7 {
		t.Fatalf("unmatched statement lines: %d", len(l))
	}

	// FR-BNK-03 reconciliation.
	rec := c.Must(201, "POST", accBase+"/bank-reconciliations", map[string]any{"bankAccountId": bank, "statementDate": day, "statementBalance": "5325000"}).JSON()
	rid := str(rec["id"])
	c.Must(409, "POST", accBase+"/bank-reconciliations", map[string]any{"bankAccountId": bank, "statementDate": day, "statementBalance": "5325000"})
	am := c.Must(200, "POST", accBase+"/bank-reconciliations/"+rid+":auto-match", map[string]any{}).JSON()
	if am["matched"].(float64) != 4 || am["unmatched"].(float64) != 3 {
		t.Fatalf("auto-match: %v", am)
	}
	txOf := func(desc string) map[string]any {
		for _, x := range c.Must(200, "GET", accBase+"/bank-transactions?filter[bankAccountId]="+bank, nil).Items() {
			if x["description"] == desc {
				return x
			}
		}
		t.Fatalf("statement line %q not found", desc)
		return nil
	}
	if sd := txOf("SETORAN TUNAI SHIFT"); sd["status"] != "matched" || len(sd["matchedJournals"].([]any)) != 2 {
		t.Fatalf("shift deposits aggregated: %v", sd)
	}
	if g := txOf("SETTLEMENT " + gw); g["status"] != "matched" {
		t.Fatalf("gateway settlement: %v", g)
	}
	// manual: release the transfer and match it again
	tr := txOf("TRANSFER DARI KAS")
	r := c.Must(200, "POST", accBase+"/bank-reconciliations/"+rid+":unmatch", map[string]any{"bankTransactionId": tr["id"]}).JSON()
	var line string
	for _, l := range r["outstandingLines"].([]any) {
		if m := l.(map[string]any); m["amount"] == "5000000" {
			line = str(m["lineId"])
		}
	}
	c.Must(422, "POST", accBase+"/bank-reconciliations/"+rid+":match", map[string]any{"bankTransactionId": txOf("BIAYA ADMINISTRASI")["id"],
		"journalLineIds": []string{line}})
	c.Must(200, "POST", accBase+"/bank-reconciliations/"+rid+":match", map[string]any{"bankTransactionId": tr["id"], "journalLineIds": []string{line}})
	c.Must(409, "POST", accBase+"/bank-reconciliations/"+rid+":complete", map[string]any{})
	charges0 := bal("6620")
	c.Must(200, "POST", accBase+"/bank-reconciliations/"+rid+":resolve", map[string]any{"bankTransactionId": txOf("BIAYA ADMINISTRASI")["id"],
		"accountId": accAccount(t, "6620"), "description": "Bank administration fee"})
	accEq(t, "bank charges from the statement", bal("6620").Sub(charges0), 25_000)
	for _, d := range []string{"KOREKSI BANK", "KOREKSI BANK BATAL"} {
		c.Must(200, "POST", accBase+"/bank-reconciliations/"+rid+":ignore", map[string]any{"bankTransactionId": txOf(d)["id"], "reason": "Bank correction pair"})
	}
	if g := c.Must(200, "GET", accBase+"/bank-reconciliations/"+rid, nil).JSON(); g["difference"] != "0" || len(g["unmatchedTransactions"].([]any)) != 0 {
		t.Fatalf("reconciliation before completion: %v", g)
	}
	done := c.Must(200, "POST", accBase+"/bank-reconciliations/"+rid+":complete", map[string]any{}).JSON()
	if done["status"] != "completed" || done["bookBalance"] != "5325000" {
		t.Fatalf("completed reconciliation: %v", done)
	}
	c.Must(409, "POST", accBase+"/bank-reconciliations/"+rid+":auto-match", map[string]any{})
	if l := c.Must(200, "GET", accBase+"/bank-reconciliations?bankAccountId="+bank, nil).Items(); len(l) != 1 {
		t.Fatalf("reconciliations: %v", l)
	}
	if rep := c.Must(200, "GET", "/api/v1/reporting/reports/accounting.bank_reconciliation?params[from]="+day+"&params[to]="+day, nil).JSON(); len(rep["rows"].([]any)) < 7 {
		t.Fatalf("bank reconciliation report: %v", rep)
	}
}

// EP-23 FR-TRS-04 and contract K10: the transition sign-off of a new
// property stops its Accounting Export (earlier exports stay
// downloadable); billing refuses corrections dated in a closed financial
// period and accepts them in an open one.
func TestP4AccountingTransitionAndPeriodGuard(t *testing.T) {
	sa := superAdmin(t, inst)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	p := sa.Must(201, "POST", "/api/v1/platform/properties", map[string]any{"code": "ACT" + sfx[:3], "name": "Accounting Transition " + sfx,
		"timezone": "Asia/Jakarta"}).JSON()
	c := *sa
	c.Property = mustUUID(str(p["id"]))
	today := time.Now().In(clubLoc(inst)).Format("2006-01-02")
	c.Must(200, "POST", accBase+"/book:load-template", map[string]any{"cutOverDate": mustDate(today).AddDate(0, 0, 1-mustDate(today).Day()).Format("2006-01-02")})
	old := c.Must(201, "POST", "/api/v1/billing/accounting-exports", map[string]any{"date": today}).JSON()
	acct := roleUser(t, inst, "accountant")
	acct.Property = c.Property
	acct.Must(403, "POST", accBase+"/book:sign-off", map[string]any{"note": "Reconciliation month agreed"})
	c.Must(422, "POST", accBase+"/book:sign-off", map[string]any{"note": ""})
	b := c.Must(200, "POST", accBase+"/book:sign-off", map[string]any{"note": "Reconciliation month agreed with Excel Finance"}).JSON()
	if b["status"] != "live" || b["exportStoppedAt"] == nil {
		t.Fatalf("signed-off book: %v", b)
	}
	c.Must(409, "POST", accBase+"/book:sign-off", map[string]any{"note": "again"})
	r := c.Do("POST", "/api/v1/billing/accounting-exports", map[string]any{"date": today})
	if r.Status != 409 || !strings.Contains(string(r.Body), "export_stopped") {
		t.Fatalf("export after the sign-off: %s", r)
	}
	found := false
	for _, e := range c.Must(200, "GET", "/api/v1/billing/accounting-exports", nil).Items() {
		found = found || e["id"] == old["id"]
	}
	if !found {
		t.Fatal("the export made before the sign-off stays downloadable")
	}

	// K10 on MDR: December 2025 is closed, January 2026 open (reopened).
	m := accSetupMDR(t)
	f := idOf(m.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"holderName": "Period guard " + sfx}))
	charge := func(desc, price string) string {
		ls := m.Must(201, "POST", "/api/v1/billing/folios/"+f+"/lines", map[string]any{"chargeType": "other", "description": desc, "unitPrice": price}).JSON()["lines"].([]any)
		return str(ls[len(ls)-1].(map[string]any)["id"])
	}
	l1, l2 := charge("Late correction", "1000"), charge("Open period", "2000")
	sysExec(t, inst, `UPDATE billing.folio_lines SET business_date = '2025-12-15' WHERE id = $1`, mustUUID(l1))
	sysExec(t, inst, `UPDATE billing.folio_lines SET business_date = '2026-01-15' WHERE id = $1`, mustUUID(l2))
	m.Must(200, "POST", accBase+"/periods:generate", map[string]any{"year": 2025})
	var dec2025, st string
	sysQueryRow(t, inst, `SELECT id::text, status FROM accounting.periods WHERE property_id = $1 AND year = 2025 AND month = 12`, []any{inst.MDR}, &dec2025, &st)
	if st != accounting.PeriodClosed { // closed by TestP4AccountingLedgerCore when it ran before
		m.Must(200, "POST", accBase+"/periods/"+dec2025+":close", map[string]any{})
	}
	r = m.Do("POST", "/api/v1/billing/folios/"+f+"/lines/"+l1+":void", map[string]any{"reason": "Wrong price"})
	if r.Status != 409 || !strings.Contains(string(r.Body), "period_closed") {
		t.Fatalf("void in a closed period: %s", r)
	}
	m.Must(200, "POST", "/api/v1/billing/folios/"+f+"/lines/"+l2+":void", map[string]any{"reason": "Wrong price"})
}
