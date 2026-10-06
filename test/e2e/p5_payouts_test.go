package e2e

// PRD P5 payouts & distributions: EP-11 Service Charge Distribution, EP-12
// Sales Commission & Bonus Payout, EP-13 caddy and EP-14 instructor payout
// runs, their EP-24 policy, EP-26 statements (Caddy App, instructor, ESS)
// and EP-27 reports, with the accounting journals (H5) and the bank file.
// Exact amounts follow the PRD acceptance criteria; isolated amounts run
// on a property of their own (book opened by the test).

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/reqctx"
)

func init() {
	resourceCRUDSkip["hris.partner_profile"] = "the partner must be a caddy or a partner instructor of the property: covered by TestP5PayoutsCaddyAcceptance"
}

// poEvents are the events the payouts consume (H1–H3).
var poEvents = []string{"golf.caddy_settlement_approved", "sportclub.instructor_fee_approved", "crm.commission_approved"}

// poDispatch delivers the pending outbox events and waits until hris
// processed every consumed event published so far.
func poDispatch(t *testing.T) {
	t.Helper()
	upTo := time.Now()
	waitFor(t, 60*time.Second, "hris payouts to process the published events", func() bool {
		if _, err := inst.App.Dispatcher.DispatchPending(t.Context()); err != nil {
			t.Logf("dispatch: %v", err)
		}
		var pending int
		sysQueryRow(t, inst, `SELECT count(*) FROM platform.outbox o WHERE o.event_type = ANY ($1) AND o.occurred_at <= $2
			AND NOT EXISTS (SELECT 1 FROM platform.outbox_processed p WHERE p.event_id = o.id AND p.subscriber = 'hris.payouts:' || o.event_type)`,
			[]any{poEvents, upTo}, &pending)
		return pending == 0
	})
	accDispatch(t)
}

// poPublish publishes an event as its owner module does (contract tests
// H1–H3) and delivers it.
func poPublish(t *testing.T, property uuid.UUID, eventType, aggregate string, agg uuid.UUID, payload any) uuid.UUID {
	t.Helper()
	ctx := dbtx.System(context.Background())
	var eid uuid.UUID
	if err := inst.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		eid, err = inst.App.Bus.Publish(ctx, tx, eventType, aggregate, &agg, &property, payload)
		return err
	}); err != nil {
		t.Fatalf("publish %s: %v", eventType, err)
	}
	poDispatch(t)
	return eid
}

// poAccounts sums journal lines (debit − credit) per account code of a
// property for a source document, optionally of one partner.
func poAccounts(t *testing.T, property uuid.UUID, sourceID string, partner string) map[string]decimal.Decimal {
	t.Helper()
	out := map[string]decimal.Decimal{}
	ctx := dbtx.System(context.Background())
	if err := inst.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT a.code, sum(l.debit - l.credit)::text FROM accounting.journal_lines l JOIN accounting.accounts a ON a.id = l.account_id
			WHERE l.property_id = $1 AND l.source_id = $2 AND ($3 = '' OR l.partner_id::text = $3) GROUP BY a.code`, property, sourceID, partner)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var code, v string
			if err := rows.Scan(&code, &v); err != nil {
				return err
			}
			out[code] = dec(v)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func poEq(t *testing.T, what string, got any, want string) {
	t.Helper()
	if !dec(got).Equal(decimal.RequireFromString(want)) {
		t.Fatalf("%s: want %s, got %v", what, want, got)
	}
}

// poUser creates a login with a role at a property and signs it in there.
func poUser(t *testing.T, sa *Client, role, name string, property uuid.UUID) (*Client, string) {
	t.Helper()
	email := fmt.Sprintf("po.%s.%s@demo.test", strings.ReplaceAll(role, "_", "-"), hrSuffix())
	u := sa.Must(201, "POST", "/api/v1/platform/users", map[string]any{"email": email, "fullName": name, "password": "Payout#2026xx",
		"assignments": []map[string]any{{"roleId": roleID(t, sa, role), "propertyId": property}}}).JSON()
	c := login(t, inst, email, "Payout#2026xx")
	c.Must(204, "POST", "/api/v1/auth/password/change", map[string]any{"currentPassword": "Payout#2026xx", "newPassword": "Payout#2026yy"})
	c.Property = property
	return c, str(u["id"])
}

func poLine(t *testing.T, run map[string]any, partner string) map[string]any {
	t.Helper()
	for _, l := range run["lines"].([]any) {
		if m := l.(map[string]any); m["partnerId"] == partner {
			return m
		}
	}
	t.Fatalf("no line of partner %s in run %v", partner, run["number"])
	return nil
}

func poPeriod(schedule, day string) (string, string) {
	f, to := hris.PartnerPeriod(schedule, mustDate(day))
	return f.Format("2006-01-02"), to.Format("2006-01-02")
}

func poInputs(t *testing.T, property uuid.UUID, from, to string, employees ...string) []hris.PayrollInputLine {
	t.Helper()
	var ids []uuid.UUID
	for _, e := range employees {
		ids = append(ids, mustUUID(e))
	}
	var out []hris.PayrollInputLine
	ctx := reqctx.WithProperty(dbtx.System(context.Background()), property)
	if err := inst.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = hris.CollectPayrollInputs(ctx, tx, property, mustDate(from), mustDate(to), ids)
		return err
	}); err != nil {
		t.Fatalf("collect payroll inputs: %v", err)
	}
	return out
}

func poConsume(t *testing.T, property uuid.UUID, lines []hris.PayrollInputLine) uuid.UUID {
	t.Helper()
	run := uuid.New()
	ctx := reqctx.WithProperty(dbtx.System(context.Background()), property)
	if err := inst.DB.WithTx(ctx, func(tx pgx.Tx) error {
		return hris.MarkPayrollInputsConsumed(ctx, tx, property, run, lines)
	}); err != nil {
		t.Fatalf("mark payroll inputs consumed: %v", err)
	}
	return run
}

func poOf(lines []hris.PayrollInputLine, source, component string) []hris.PayrollInputLine {
	var out []hris.PayrollInputLine
	for _, l := range lines {
		if l.Source == source && l.ComponentCode == component {
			out = append(out, l)
		}
	}
	return out
}

// ── EP-13 caddy payout run on MAIN, end to end ────────────────────────────

// FR-CDY-01–05, FR-OPS-P5-03, §9.3: a round with a caddy (caddy fee) and a
// non-cash tip → approved settlement (H1) → caddy payout run (semi-monthly
// period): PPh 21 non-employee 5% of 50%, BPJS BPU (JKK 1% + JKM) and the
// settlement deductions → Finance approval posts the journal (caddy fee
// liability against net payable, PPh 21, BPU, deductions) → bank file →
// statements (Caddy App, PDF) → paid: bank journal, the settlement turns
// Paid in golf and the caddy's liability is 0.
func TestP5PayoutsCaddyRun(t *testing.T) {
	c := fixMainBook(t)
	sa := superAdmin(t, inst)
	gm := login(t, inst, "golf.manager@demo.oneclub.id", demoPassword)
	cashier := login(t, inst, "cashier@demo.oneclub.id", demoPassword)
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	fin := login(t, inst, "finance@demo.oneclub.id", demoPassword)
	today := invToday()
	sfx := hrSuffix()
	day := clubDay(inst, 15, isWeekday)
	playDay, _ := time.ParseInLocation("2006-01-02", day, clubLoc(inst))
	slots := slotsOf(teeTimes(t, gm, demoCourse(t, inst), day), "afternoon", 10)
	slot := slots[len(slots)-2]
	sysExec(t, inst, `UPDATE golf.tee_times SET min_players = 1 WHERE id = $1`, mustUUID(str(slot["id"])))
	caddy := idOf(sa.Must(201, "POST", "/api/v1/golf/caddies", map[string]any{"code": "PO" + sfx, "name": "Caddy Payout " + sfx, "gender": "female"}))
	sa.Must(201, "POST", "/api/v1/golf/caddy-attendance:clock-in", map[string]any{"caddyId": caddy, "at": rfc(at(playDay, 5, 30))})
	c.Must(200, "POST", accBase+"/postings:run", map[string]any{"upTo": today})
	liab0 := accBal(t, inst.Main, "2171", "")

	// the round, its caddy fee and a non-cash tip (caddy fee liability)
	bk := gm.Must(201, "POST", "/api/v1/golf/bookings", map[string]any{"bookingType": "member", "channel": "back_office", "teeTimeId": slot["id"],
		"players": []map[string]any{{"playerType": "member", "memberNo": "D0001"}}}).JSON()
	payFolio(t, cashier, bk)
	fid := firstFlight(bk)
	ca := sa.Must(201, "POST", "/api/v1/golf/caddy-assignments", map[string]any{"flightId": fid, "assignments": []map[string]any{
		{"caddyId": caddy, "playerIds": []any{playerIDs(bk)[0]}}}}).Items()[0]
	poEq(t, "caddy fee", ca["feeAmount"], "150000")
	sa.Must(201, "POST", "/api/v1/golf/golf-cart-assignments", map[string]any{"flightId": fid, "auto": true})
	checkIn(t, sa, day, bk)
	sa.Must(200, "POST", "/api/v1/golf/rounds/"+fid+":start", map[string]any{"at": rfc(time.Now().Add(-4 * time.Hour))})
	sa.Must(200, "POST", "/api/v1/golf/rounds/"+fid+":complete", map[string]any{})
	sa.Must(201, "POST", "/api/v1/golf/caddy-tips", map[string]any{"assignmentId": ca["id"], "amount": "50000", "method": "non_cash"})
	accDispatch(t)
	c.Must(200, "POST", accBase+"/postings:run", map[string]any{"upTo": today})
	accEq(t, "caddy fee + non-cash tip liability", accBal(t, inst.Main, "2171", "").Sub(liab0), -200_000)

	// FR-CDY-01: partner profile (NPWP, bank account, BPU) — masked for the caddy master
	prof := hr.Must(201, "POST", hrBase+"/partner-profiles", map[string]any{"partnerKind": "caddy", "partnerId": caddy, "npwp": "09.254.294.3-407.000",
		"nik": "3271046504930002", "bankCode": "014", "bankName": "BCA", "bankAccountNo": "8800123456", "bankAccountName": "Caddy Payout " + sfx,
		"bpuEnrolled": true, "bpuNo": "BPU" + sfx}, "Idempotency-Key", newKey()).JSON()
	if prof["partnerName"] != "Caddy Payout "+sfx || prof["bankAccountNo"] != "8800123456" {
		t.Fatalf("partner profile: %v", prof)
	}
	if v := roleUser(t, inst, "caddy_manager").Must(200, "GET", hrBase+"/partner-profiles/"+str(prof["id"]), nil).JSON(); v["bankAccountNo"] == "8800123456" ||
		v["npwp"] == prof["npwp"] {
		t.Fatalf("bank account and NPWP must be masked for the caddy master: %v", v)
	}
	hr.Must(409, "POST", hrBase+"/partner-profiles", map[string]any{"partnerKind": "caddy", "partnerId": caddy}, "Idempotency-Key", newKey())

	// H1: the approved settlement reaches hris
	st := sa.Must(201, "POST", "/api/v1/golf/caddy-settlements", map[string]any{"caddyId": caddy, "periodStart": day, "periodEnd": day}).JSON()
	if st["status"] != "approved" || !dec(st["caddyFee"]).Add(dec(st["tips"])).Equal(decimal.NewFromInt(200_000)) {
		t.Fatalf("settlement (no workflow: approved at once): %v", st)
	}
	ded := dec(st["deductions"])
	poDispatch(t)
	var src map[string]any
	for _, s := range hr.Must(200, "GET", hrBase+"/payout-sources?kind=caddy&status=open", nil).Items() {
		if s["sourceId"] == st["id"] {
			src = s
		}
	}
	if src == nil || !dec(src["gross"]).Equal(decimal.NewFromInt(200_000)) || !dec(src["deductions"]).Equal(ded) || src["partnerId"] != caddy ||
		src["number"] != st["number"] || dec(src["units"]).IntPart() != 1 {
		t.Fatalf("payout source of the settlement: %v", src)
	}

	// the run of the semi-monthly period (15th / month end)
	from, to := poPeriod("semi_monthly", day)
	hr.Must(422, "POST", hrBase+"/payout-runs", map[string]any{"kind": "caddy", "periodStart": from, "periodEnd": mustDate(to).AddDate(0, 0, 1).Format("2006-01-02")},
		"Idempotency-Key", newKey())
	run := hr.Must(201, "POST", hrBase+"/payout-runs", map[string]any{"kind": "caddy", "periodStart": from, "periodEnd": to}, "Idempotency-Key", newKey()).JSON()
	rid := str(run["id"])
	if run["status"] != "draft" || run["payDate"] == nil || !strings.HasPrefix(str(run["number"]), "PYO-") {
		t.Fatalf("draft run: %v", run)
	}
	hr.Must(409, "POST", hrBase+"/payout-runs/"+rid+":mark-paid", map[string]any{})
	run = hr.Must(200, "POST", hrBase+"/payout-runs/"+rid+":calculate", map[string]any{}).JSON()
	if run["status"] != "calculated" || len(run["policyVersions"].([]any)) != 2 || !strings.Contains(str(run["taxNote"]), "tax consultant") {
		t.Fatalf("calculated run: %v", run)
	}
	l := poLine(t, run, caddy)
	poEq(t, "gross = caddy fee + non-cash tip", l["gross"], "200000")
	poEq(t, "taxable base 50%", l["taxBase"], "100000")
	poEq(t, "PPh 21 (5% bracket, NPWP)", l["pph21"], "5000")
	poEq(t, "BPU JKK 1%", l["bpuJkk"], "2000")
	poEq(t, "BPU JKM", l["bpuJkm"], "6800")
	net := decimal.NewFromInt(200_000 - 5_000 - 8_800).Sub(ded)
	poEq(t, "net", l["net"], net.String())
	if l["paymentMethod"] != "bank_transfer" || l["hasTaxId"] != true || l["monthlyDue"] != true || len(l["sources"].([]any)) != 1 {
		t.Fatalf("line: %v", l)
	}
	if s := hr.Must(200, "GET", hrBase+"/payout-sources?kind=caddy&status=claimed", nil).Items(); len(s) == 0 {
		t.Fatal("the run claims its sources")
	}
	// recalculation is stable
	if again := hr.Must(200, "POST", hrBase+"/payout-runs/"+rid+":calculate", map[string]any{}).JSON(); again["net"] != run["net"] || again["partners"] != run["partners"] {
		t.Fatalf("recalculation: %v vs %v", again["net"], run["net"])
	}
	roleUser(t, inst, "hr_admin").Must(403, "POST", hrBase+"/payout-runs/"+rid+":approve", map[string]any{})
	hr.Must(409, "GET", hrBase+"/payout-runs/"+rid+"/bank-file", nil)

	// Finance approval posts the run (H5)
	run = fin.Must(200, "POST", hrBase+"/payout-runs/"+rid+":approve", map[string]any{"reason": "checked"}).JSON()
	if run["status"] != "approved" || run["postedAt"] == nil {
		t.Fatalf("approved run: %v", run)
	}
	l = poLine(t, run, caddy) // lines of the last calculation
	if n := hrEvents(t, "hris.payout_posted", rid); n != 1 {
		t.Fatalf("hris.payout_posted events: %d", n)
	}
	accDispatch(t)
	j := poAccounts(t, inst.Main, rid, caddy)
	if !j["2171"].Equal(decimal.NewFromInt(200_000)) || !j["2174"].Equal(net.Neg()) || !j["2135"].Equal(decimal.NewFromInt(-5_000)) ||
		!j["2136"].Equal(decimal.NewFromInt(-8_800)) || !j["4830"].Equal(ded.Neg()) {
		t.Fatalf("payout journal of the caddy (Dr liability 200,000, Cr net %s, PPh 21 5,000, BPU 8,800, deductions %s): %v", net, ded, j)
	}
	hr.Must(409, "POST", hrBase+"/payout-runs/"+rid+":cancel", map[string]any{"reason": "too late"})

	// bank file (FR-CDY-04) and statements (FR-CDY-03)
	bf := fin.Must(200, "GET", hrBase+"/payout-runs/"+rid+"/bank-file", nil)
	if !strings.HasPrefix(bf.Header.Get("Content-Type"), "text/csv") || !strings.Contains(string(bf.Body), "8800123456") ||
		!strings.Contains(string(bf.Body), net.StringFixed(2)) || !strings.HasPrefix(string(bf.Body), "pay_date,run,") {
		t.Fatalf("bank file: %s", bf.Body)
	}
	if bca := fin.Must(200, "GET", hrBase+"/payout-runs/"+rid+"/bank-file?format=bca_csv", nil); !strings.HasPrefix(string(bca.Body), "H,") {
		t.Fatalf("BCA layout: %s", bca.Body)
	}
	if pdf := hr.Must(200, "GET", hrBase+"/payout-runs/"+rid+"/statements/"+str(l["id"]), nil); !strings.HasPrefix(string(pdf.Body), "%PDF") {
		t.Fatal("statement PDF")
	}
	cu, uid := poUser(t, sa, "caddy", "Caddy Login "+sfx, inst.Main)
	sa.Must(200, "PUT", "/api/v1/golf/caddies/"+caddy+"/profile", map[string]any{"userId": uid})
	t.Cleanup(func() {
		sysExec(t, inst, `UPDATE golf.caddy_profiles SET user_id = NULL WHERE caddy_id = $1`, mustUUID(caddy))
	})
	hist := cu.Must(200, "GET", hrBase+"/my-payouts", nil).Items()
	if len(hist) != 1 || hist[0]["lineId"] != l["id"] || !dec(hist[0]["net"]).Equal(net) || hist[0]["runStatus"] != "approved" {
		t.Fatalf("Caddy App payout history: %v", hist)
	}
	stmt := cu.Must(200, "GET", hrBase+"/my-payouts/"+str(l["id"]), nil).JSON()
	if stmt["title"] != "Payout Statement" || stmt["line"].(map[string]any)["bankAccountNo"] == "8800123456" {
		t.Fatalf("caddy statement (bank account masked): %v", stmt)
	}
	if pdf := cu.Must(200, "GET", hrBase+"/my-payouts/"+str(l["id"])+"/pdf", nil); !strings.HasPrefix(string(pdf.Body), "%PDF") {
		t.Fatal("caddy statement PDF")
	}
	cu.Must(403, "GET", hrBase+"/payout-runs", nil)
	roleUser(t, inst, "cashier").Must(403, "GET", hrBase+"/my-payouts", nil)

	// paid: bank journal, golf settlement Paid, caddy sub-ledger 0 (EP-13 AC)
	paid := fin.Must(200, "POST", hrBase+"/payout-runs/"+rid+":mark-paid", map[string]any{"reference": "TRF-" + sfx}).JSON()
	if paid["status"] != "paid" || paid["paidReference"] != "TRF-"+sfx || paid["paidOn"] == nil {
		t.Fatalf("paid run: %v", paid)
	}
	accDispatch(t)
	all := poAccounts(t, inst.Main, rid, "")
	bank, cash := dec(paid["bankAmount"]), dec(paid["cashAmount"])
	if !all["1121"].Equal(bank.Neg()) || !all["1111"].Equal(cash.Neg()) || !all["2174"].IsZero() || !bank.Add(cash).Equal(dec(paid["net"])) {
		t.Fatalf("payment journal (Cr bank %s, Cr cash %s, payable cleared): %v", bank, cash, all)
	}
	if s := sa.Must(200, "GET", "/api/v1/golf/caddy-settlements/"+str(st["id"]), nil).JSON(); s["status"] != "paid" {
		t.Fatalf("golf settlement after the payout: %v", s)
	}
	sa.Must(409, "POST", "/api/v1/golf/caddy-settlements/"+str(st["id"])+":pay", map[string]any{"methodType": "cash"})
	for _, x := range sa.Must(200, "GET", "/api/v1/golf/caddy-liabilities", nil).Items() {
		if x["caddyId"] == caddy && !dec(x["liability"]).IsZero() {
			t.Fatalf("caddy sub-ledger after the payout: %v", x)
		}
	}
	// GL: the caddy's fee and tip liability is cleared; what remains of the
	// run's debit belongs to the other caddies of the period
	others := decimal.Zero
	for _, x := range paid["lines"].([]any) {
		if m := x.(map[string]any); m["partnerId"] != caddy {
			others = others.Add(dec(m["gross"]))
		}
	}
	if d := accBal(t, inst.Main, "2171", "").Sub(liab0); !d.Equal(others) {
		t.Fatalf("caddy fee liability after the payout: delta %s, other caddies of the run %s", d, others)
	}
	if h := cu.Must(200, "GET", hrBase+"/my-payouts", nil).Items(); h[0]["runStatus"] != "paid" {
		t.Fatalf("history after payment: %v", h)
	}
	var notes int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.notifications WHERE user_id = $1 AND event_code = 'hris.payout_statement_ready'`, []any{mustUUID(uid)}, &notes)
	if notes != 1 { // the login was linked after the approval: only the payment notice
		t.Fatalf("statement notifications of the caddy: %d", notes)
	}
	if r := hr.Must(200, "GET", "/api/v1/reporting/reports/hris.caddy_payout?params[from]="+from+"&params[to]="+to, nil).JSON(); len(r["rows"].([]any)) == 0 {
		t.Fatal("Caddy Payout Report")
	}
	if l := hr.Must(200, "GET", hrBase+"/payout-runs?kind=caddy&status=paid", nil).Items(); len(l) == 0 {
		t.Fatal("run list")
	}
}

// ── EP-13 acceptance on a property of its own ────────────────────────────

// EP-13 AC: settlement Rp6,000,000 + non-cash tips Rp400,000 → statement
// Rp6,400,000 before tax / deductions; PPh 21 non-employee Rp160,000, BPU
// Rp70,800, Caddy Policies deduction of the Partner Payout Policy version;
// the approval workflow (reject → recalculate → approve); the liability of
// the caddy is cleared, PPh 21 and BPJS payables recorded; a redelivered
// event is ignored; a second run of the period finds nothing to pay.
func TestP5PayoutsCaddyAcceptance(t *testing.T) {
	c, prop := fixProperty(t, "PO", "")
	sa := superAdmin(t, inst)
	sfx := hrSuffix()
	today := invToday()
	caddy := idOf(c.Must(201, "POST", "/api/v1/golf/caddies", map[string]any{"code": "AC" + sfx, "name": "Caddy AC " + sfx, "gender": "male"}))
	spare := idOf(c.Must(201, "POST", "/api/v1/golf/caddies", map[string]any{"code": "SP" + sfx, "name": "Caddy Spare " + sfx, "gender": "male"}))
	prof := c.Must(201, "POST", hrBase+"/partner-profiles", map[string]any{"partnerKind": "caddy", "partnerId": caddy, "nik": "3271046504930011",
		"bankName": "BCA", "bankAccountNo": "8800654321", "bankAccountName": "Caddy AC", "bpuEnrolled": true}, "Idempotency-Key", newKey()).JSON()
	c.Must(200, "PATCH", hrBase+"/partner-profiles/"+str(prof["id"]), map[string]any{"bankCode": "014", "notes": "verified"})
	sp := c.Must(201, "POST", hrBase+"/partner-profiles", map[string]any{"partnerKind": "caddy", "partnerId": spare}, "Idempotency-Key", newKey()).JSON()
	c.Must(204, "DELETE", hrBase+"/partner-profiles/"+str(sp["id"]), nil)
	c.Must(422, "POST", hrBase+"/partner-profiles", map[string]any{"partnerKind": "caddy", "partnerId": uuid.NewString()}, "Idempotency-Key", newKey())

	// Partner Payout Policy version of the property: a monthly uniform deduction
	pol := sa.Must(201, "POST", "/api/v1/platform/club-policies", map[string]any{"category": hris.CategoryHRPolicies, "code": hris.PartnerPayoutPolicyCode,
		"name": "Partner Payout Policy AC", "propertyId": prop, "value": map[string]any{"caddy": map[string]any{"withholdPph21": true, "taxMethod": "per_payment",
			"deductBpu": true, "paymentMethod": "bank_transfer", "minimumNet": "0",
			"deductions": []map[string]any{{"code": "UNIFORM", "label": "Uniform", "kind": "fixed", "amount": "25000", "perMonth": true}}}}})
	t.Cleanup(func() {
		sa.Must(200, "POST", "/api/v1/platform/club-policies/"+idOf(pol)+":set-status", map[string]any{"status": "inactive"})
	})

	from, to := poPeriod("semi_monthly", today)
	stID := uuid.New()
	h1 := map[string]any{"settlementId": stID, "number": "CST-AC-" + sfx, "caddyId": caddy, "caddyName": "Caddy AC " + sfx, "periodStart": from,
		"periodEnd": today, "rounds": 40, "caddyFee": "6000000", "tips": "400000", "deductions": "0", "total": "6400000"}
	poPublish(t, prop, "golf.caddy_settlement_approved", "golf.caddy_settlement", stID, h1)
	poPublish(t, prop, "golf.caddy_settlement_approved", "golf.caddy_settlement", stID, h1) // redelivery
	var n int
	sysQueryRow(t, inst, `SELECT count(*) FROM hris.payout_sources WHERE source_id = $1`, []any{stID}, &n)
	if n != 1 {
		t.Fatalf("a redelivered settlement is kept once: %d", n)
	}

	run := c.Must(201, "POST", hrBase+"/payout-runs", map[string]any{"kind": "caddy", "periodStart": from, "periodEnd": to, "notes": "AC"},
		"Idempotency-Key", newKey()).JSON()
	rid := str(run["id"])
	run = c.Must(200, "POST", hrBase+"/payout-runs/"+rid+":calculate", map[string]any{}).JSON()
	l := poLine(t, run, caddy)
	poEq(t, "statement before tax / deductions (EP-13 AC)", l["gross"], "6400000")
	poEq(t, "PPh 21 = Article 17 × 50% of gross", l["pph21"], "160000")
	poEq(t, "BPU JKK", l["bpuJkk"], "64000")
	poEq(t, "BPU JKM", l["bpuJkm"], "6800")
	poEq(t, "uniform (policy version)", l["otherDeductions"], "25000")
	poEq(t, "net", l["net"], "6144200")
	if ds := l["deductions"].([]any); len(ds) != 1 || ds[0].(map[string]any)["code"] != "UNIFORM" || l["units"] != float64(40) {
		t.Fatalf("line deductions / rounds: %v", l)
	}
	if run["partners"] != float64(1) || run["net"] != "6144200" || run["pph21"] != "160000" || run["bpu"] != "70800" {
		t.Fatalf("run totals: %v", run)
	}
	var pv bool
	for _, p := range run["policyVersions"].([]any) {
		if m := p.(map[string]any); m["code"] == hris.PartnerPayoutPolicyCode && dec(m["version"]).IntPart() > 0 {
			pv = true
		}
	}
	if !pv {
		t.Fatalf("the run keeps the Partner Payout Policy version it used: %v", run["policyVersions"])
	}

	// approval workflow: the General Manager of the property rejects, then approves
	talentWorkflow(t, sa, "hris.payout_run", "amount", 5_000_000)
	gmc, _ := poUser(t, sa, "general_manager", "GM "+sfx, prop)
	run = c.Must(200, "POST", hrBase+"/payout-runs/"+rid+":approve", map[string]any{}).JSON()
	if run["status"] != "pending_approval" || run["approvalRequestId"] == nil {
		t.Fatalf("run waiting for the GM: %v", run)
	}
	c.Must(403, "POST", hrBase+"/payout-runs/"+rid+":approve", map[string]any{})  // not one's own request
	gmc.Must(422, "POST", hrBase+"/payout-runs/"+rid+":reject", map[string]any{}) // a reason is required
	gmc.Must(200, "POST", hrBase+"/payout-runs/"+rid+":reject", map[string]any{"reason": "check the uniform"})
	run = c.Must(200, "GET", hrBase+"/payout-runs/"+rid, nil).JSON()
	if run["status"] != "calculated" || run["decisionNote"] != "check the uniform" {
		t.Fatalf("rejected run back to Calculated: %v", run)
	}
	c.Must(200, "POST", hrBase+"/payout-runs/"+rid+":calculate", map[string]any{})
	run = c.Must(200, "POST", hrBase+"/payout-runs/"+rid+":approve", map[string]any{}).JSON()
	if run["status"] != "pending_approval" {
		t.Fatalf("resubmitted: %v", run)
	}
	run = gmc.Must(200, "POST", hrBase+"/payout-runs/"+rid+":approve", map[string]any{"reason": "ok"}).JSON()
	if run["status"] != "approved" {
		t.Fatalf("approved by the step approver: %v", run)
	}
	accDispatch(t)
	j := poAccounts(t, prop, rid, caddy)
	if !j["2171"].Equal(decimal.NewFromInt(6_400_000)) || !j["2174"].Equal(decimal.NewFromInt(-6_144_200)) || !j["2135"].Equal(decimal.NewFromInt(-160_000)) ||
		!j["2136"].Equal(decimal.NewFromInt(-70_800)) || !j["4830"].Equal(decimal.NewFromInt(-25_000)) {
		t.Fatalf("payout journal: %v", j)
	}
	c.Must(200, "POST", hrBase+"/payout-runs/"+rid+":mark-paid", map[string]any{"paidOn": today, "reference": "AC-" + sfx})
	accDispatch(t)
	accEq(t, "partner payout payable cleared", accBal(t, prop, "2174", ""), 0)
	accEq(t, "PPh 21 non-employee payable", accBal(t, prop, "2135", ""), -160_000)
	accEq(t, "BPJS BPU payable", accBal(t, prop, "2136", ""), -70_800)
	accEq(t, "bank", accBal(t, prop, "1121", ""), -6_144_200)

	// a second run of the period finds nothing; it is cancelled
	r2 := c.Must(201, "POST", hrBase+"/payout-runs", map[string]any{"kind": "caddy", "periodStart": from, "periodEnd": to}, "Idempotency-Key", newKey()).JSON()
	r2 = c.Must(200, "POST", hrBase+"/payout-runs/"+str(r2["id"])+":calculate", map[string]any{}).JSON()
	if r2["partners"] != float64(0) {
		t.Fatalf("paid sources are not paid twice: %v", r2)
	}
	c.Must(409, "POST", hrBase+"/payout-runs/"+str(r2["id"])+":approve", map[string]any{})
	c.Must(422, "POST", hrBase+"/payout-runs/"+str(r2["id"])+":cancel", map[string]any{})
	if r := c.Must(200, "POST", hrBase+"/payout-runs/"+str(r2["id"])+":cancel", map[string]any{"reason": "nothing to pay"}).JSON(); r["status"] != "cancelled" {
		t.Fatalf("cancelled: %v", r)
	}
}

// ── EP-14 instructor payout ───────────────────────────────────────────────

// FR-INS-HR-03/04, EP-14 AC: a partner instructor with 8 sessions ×
// Rp150,000 (H2) → statement Rp1,200,000 before tax, PPh 21 Rp30,000 →
// monthly instructor run → the instructor fee payable of the instructor is
// cleared; Honor Statement in the instructor area; an employee instructor's
// fee goes to payroll (INSTRUCTOR_FEE) and is paid with it.
func TestP5PayoutsInstructorRun(t *testing.T) {
	fixMainBook(t)
	sa := superAdmin(t, inst)
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	sfx := hrSuffix()
	iu, uid := poUser(t, sa, "instructor_coach", "Coach Login "+sfx, inst.Main)
	partner := idOf(sa.Must(201, "POST", "/api/v1/sportclub/instructors", map[string]any{"code": "PI" + sfx, "name": "Coach Partner " + sfx,
		"partnership": "partner", "userId": uid, "disciplines": []string{"tennis"}, "feeScheme": "per_session", "feeRate": "150000"}))
	emp := hrNewEmployee(t, hr, "Coach Employee "+sfx, "GYM-INSTR", nil)
	employed := idOf(sa.Must(201, "POST", "/api/v1/sportclub/instructors", map[string]any{"code": "EI" + sfx, "name": "Coach Employee " + sfx,
		"partnership": "employee", "employeeId": emp["id"], "disciplines": []string{"swimming"}}))
	hr.Must(422, "POST", hrBase+"/partner-profiles", map[string]any{"partnerKind": "instructor", "partnerId": employed}, "Idempotency-Key", newKey())

	last := mustDate(invToday()).AddDate(0, -1, 0)
	from, to := poPeriod("monthly", last.Format("2006-01-02"))
	fee := uuid.New()
	poPublish(t, inst.Main, "sportclub.instructor_fee_approved", "sportclub.instructor_fee", fee, map[string]any{"feeId": fee, "feeNo": "IF-" + sfx,
		"instructorId": partner, "amount": "1200000", "periodStart": from + "T00:00:00Z", "periodEnd": to + "T00:00:00Z"})
	efee := uuid.New()
	poPublish(t, inst.Main, "sportclub.instructor_fee_approved", "sportclub.instructor_fee", efee, map[string]any{"feeId": efee, "feeNo": "IFE-" + sfx,
		"instructorId": employed, "amount": "900000", "periodStart": from, "periodEnd": to})
	var ch, ech string
	sysQueryRow(t, inst, `SELECT channel FROM hris.payout_sources WHERE source_id = $1`, []any{fee}, &ch)
	sysQueryRow(t, inst, `SELECT channel FROM hris.payout_sources WHERE source_id = $1`, []any{efee}, &ech)
	if ch != "payout" || ech != "payroll" {
		t.Fatalf("partner instructor → payout run, employee instructor → payroll: %s / %s", ch, ech)
	}
	accEq(t, "instructor fee payable of the partner (accrued on approval)", poPartnerBal(t, "2172", partner), -1_200_000)

	// the monthly run (default period: last month)
	run := hr.Must(201, "POST", hrBase+"/payout-runs", map[string]any{"kind": "instructor"}, "Idempotency-Key", newKey()).JSON()
	if str(run["periodStart"])[:10] != from || str(run["periodEnd"])[:10] != to {
		t.Fatalf("default instructor period (last month): %v – %v", run["periodStart"], run["periodEnd"])
	}
	rid := str(run["id"])
	run = hr.Must(200, "POST", hrBase+"/payout-runs/"+rid+":calculate", map[string]any{}).JSON()
	l := poLine(t, run, partner)
	poEq(t, "honorarium before tax (EP-14 AC)", l["gross"], "1200000")
	poEq(t, "PPh 21 (5% of 600,000; NPWP unknown → +20%)", l["pph21"], "36000")
	poEq(t, "net", l["net"], "1164000")
	if l["paymentMethod"] != "cash" || l["bpuEnrolled"] != false {
		t.Fatalf("no profile: cash, no BPU: %v", l)
	}
	for _, x := range run["lines"].([]any) {
		if x.(map[string]any)["partnerId"] == employed {
			t.Fatal("the employee instructor is paid with payroll, not by the payout run")
		}
	}
	login(t, inst, "finance@demo.oneclub.id", demoPassword).Must(200, "POST", hrBase+"/payout-runs/"+rid+":approve", map[string]any{})
	accDispatch(t)
	j := poAccounts(t, inst.Main, rid, partner)
	if !j["2172"].Equal(decimal.NewFromInt(1_200_000)) || !j["2174"].Equal(decimal.NewFromInt(-1_164_000)) || !j["2135"].Equal(decimal.NewFromInt(-36_000)) {
		t.Fatalf("instructor payout journal: %v", j)
	}
	accEq(t, "instructor fee payable of the partner cleared", poPartnerBal(t, "2172", partner), 0)

	// Honor Statement (FR-INS-HR-04)
	h := iu.Must(200, "GET", hrBase+"/my-payouts?kind=instructor", nil).Items()
	if len(h) != 1 || h[0]["number"] != run["number"] || !dec(h[0]["net"]).Equal(decimal.NewFromInt(1_164_000)) {
		t.Fatalf("honor statements: %v", h)
	}
	st := iu.Must(200, "GET", hrBase+"/my-payouts/"+str(h[0]["lineId"]), nil).JSON()
	if st["title"] != "Honor Statement" || st["kind"] != "instructor" {
		t.Fatalf("honor statement: %v", st)
	}
	iu.Must(404, "GET", hrBase+"/my-payouts/"+uuid.NewString(), nil)

	// the employee instructor's fee through payroll (FR-INS-HR-03)
	in := poOf(poInputs(t, inst.Main, from, to, str(emp["id"])), "instructor_fee", "INSTRUCTOR_FEE")
	if len(in) != 1 || !in[0].Amount.Equal(decimal.NewFromInt(900_000)) || in[0].Kind != hris.PayrollInputEarning {
		t.Fatalf("instructor fee payroll input: %+v", in)
	}
	poConsume(t, inst.Main, in)
	if again := poOf(poInputs(t, inst.Main, from, to, str(emp["id"])), "instructor_fee", "INSTRUCTOR_FEE"); len(again) != 0 {
		t.Fatalf("a consumed input is not returned again: %+v", again)
	}
	if r := hr.Must(200, "GET", "/api/v1/reporting/reports/hris.instructor_payout?params[from]="+from+"&params[to]="+to, nil).JSON(); len(r["rows"].([]any)) == 0 {
		t.Fatal("Instructor Payout Report")
	}
}

// poPartnerBal is the GL balance of an account for one partner at MAIN.
func poPartnerBal(t *testing.T, code, partner string) decimal.Decimal {
	t.Helper()
	var s string
	sysQueryRow(t, inst, `SELECT coalesce(sum(l.debit - l.credit), 0)::text FROM accounting.journal_lines l JOIN accounting.accounts a ON a.id = l.account_id
		WHERE a.code = $1 AND l.property_id = $2 AND l.partner_id = $3`, []any{code, inst.Main, mustUUID(partner)}, &s)
	return dec(s)
}

// ── EP-11 service charge distribution ─────────────────────────────────────

// FR-SVC-01–04, §16 #5, §9.2: last month's pool (Rp9,900,000, approved in
// Accounting) → 5% reserve, 95% split equally among eligible employees ×
// attendance factor (20 of 25 days = 0.8), the forfeited part
// redistributed; probation, daily worker, SP2 and unpaid leave the whole
// month excluded, SP1 eligible → approval (workflow: reject, approve) →
// journal: the service charge liability of the month is 0 → ESS statement →
// payroll input SERVICE_CHARGE, Paid once the payroll is posted.
func TestP5PayoutsServiceCharge(t *testing.T) {
	first := mustDate(invToday())
	first = first.AddDate(0, 0, 1-first.Day())
	lastFirst := first.AddDate(0, -1, 0)
	c, prop := fixProperty(t, "SC", lastFirst.Format("2006-01-02"))
	sa := superAdmin(t, inst)
	sfx := hrSuffix()
	unit := idOf(c.Must(201, "POST", hrBase+"/org-units", map[string]any{"code": "SC" + sfx, "name": "F&B SC " + sfx, "unitType": "department"},
		"Idempotency-Key", newKey()))
	pos := idOf(c.Must(201, "POST", hrBase+"/positions", map[string]any{"code": "SCW" + sfx, "name": "Waiter", "orgUnitId": unit}, "Idempotency-Key", newKey()))
	join := lastFirst.AddDate(-1, 0, -10).Format("2006-01-02")
	newEmp := func(name, status, category string) string {
		return idOf(c.Must(201, "POST", hrBase+"/employees", map[string]any{"fullName": name + " " + sfx, "positionId": pos, "gender": "male", "joinDate": join,
			"employmentStatus": status, "workerCategory": category}, "Idempotency-Key", newKey()))
	}
	a := newEmp("Ayu", "permanent", "regular")
	b := newEmp("Bima", "contract", "regular")
	sp2 := newEmp("Candra", "permanent", "regular")
	prob := newEmp("Dewi", "probation", "regular")
	daily := newEmp("Eko", "contract", "daily")
	unpaid := newEmp("Fajar", "permanent", "regular")
	sp1 := newEmp("Gita", "permanent", "regular")
	for _, x := range []struct {
		emp   string
		level int
	}{{sp2, 2}, {sp1, 1}} {
		c.Must(201, "POST", hrBase+"/employees/"+x.emp+"/documents", map[string]any{"documentType": "warning_letter", "warningLevel": x.level,
			"issuedOn": lastFirst.Format("2006-01-02"), "expiresOn": first.AddDate(0, 5, 0).Format("2006-01-02")}, "Idempotency-Key", newKey())
	}
	// last month's closed attendance: 25 working days
	for i := 0; i < 25; i++ {
		d := lastFirst.AddDate(0, 0, i)
		for _, e := range []string{a, b, sp2, prob, daily, unpaid, sp1} {
			status, paid, frac := "present", any(nil), "0"
			switch {
			case e == b && i >= 20:
				status = "absent"
			case e == unpaid:
				status, paid, frac = "on_leave", false, "1"
			}
			sysExec(t, inst, `INSERT INTO hris.attendance_days (id, property_id, employee_id, work_date, scheduled_start, scheduled_end, scheduled_minutes,
				status, leave_paid, leave_fraction, finalized, locked) VALUES (gen_random_uuid(), $1, $2, $3::date, $3::date + time '08:00', $3::date + time '17:00',
				480, $4, $5, $6::numeric, true, true)`, prop, mustUUID(e), d.Format("2006-01-02"), status, paid, frac)
		}
	}
	// the pool of last month in Accounting (H4)
	c.Must(201, "POST", accBase+"/manual-journals", map[string]any{"journalDate": lastFirst.AddDate(0, 0, 9).Format("2006-01-02"), "journalType": "adjustment",
		"description": "Service charge collected", "submit": true, "lines": []map[string]any{{"accountId": accAccount(t, "1121"), "debit": "9900000"},
			{"accountId": accAccount(t, "2121"), "credit": "9900000", "businessLine": "pos"}}})
	year, month := lastFirst.Year(), int(lastFirst.Month())
	c.Must(409, "POST", hrBase+"/service-charge-distributions", map[string]any{"year": year, "month": month}, "Idempotency-Key", newKey()) // no pool yet
	c.Must(422, "POST", hrBase+"/service-charge-distributions", map[string]any{"year": first.Year(), "month": int(first.Month())}, "Idempotency-Key", newKey())
	pool := c.Must(200, "POST", accBase+"/service-charge-pools", map[string]any{"year": year, "month": month}).JSON()

	d := c.Must(201, "POST", hrBase+"/service-charge-distributions", map[string]any{"year": year, "month": month}, "Idempotency-Key", newKey()).JSON()
	did := str(d["id"])
	if d["status"] != "draft" || d["payPeriod"] != first.Format("2006-01") || d["collected"] != "9900000" {
		t.Fatalf("draft distribution: %v", d)
	}
	c.Must(409, "POST", hrBase+"/service-charge-distributions", map[string]any{"year": year, "month": month}, "Idempotency-Key", newKey())
	d = c.Must(200, "POST", hrBase+"/service-charge-distributions/"+did+":simulate", map[string]any{}).JSON()
	if d["status"] != "simulated" || d["reserve"] != "495000" || d["distributable"] != "9405000" || d["distributed"] != "9404998" ||
		d["roundingDifference"] != "2" || d["undistributed"] != "0" || d["eligible"] != float64(3) || d["excluded"] != float64(4) {
		t.Fatalf("simulation (5%% reserve, 95%% split, rounding): %v", d)
	}
	lines := map[string]map[string]any{}
	for _, x := range d["lines"].([]any) {
		m := x.(map[string]any)
		lines[str(m["employeeId"])] = m
	}
	poEq(t, "full attendance: 9,405,000 × 1 / 2.8", lines[a]["amount"], "3358928")
	poEq(t, "SP1 stays eligible", lines[sp1]["amount"], "3358928")
	poEq(t, "attendance factor 20 / 25", lines[b]["attendanceFactor"], "0.8")
	poEq(t, "base share 9,405,000 / 3", lines[b]["baseShare"], "3135000")
	poEq(t, "80% of the share", lines[b]["attendanceAmount"], "2508000")
	poEq(t, "with the redistributed part", lines[b]["amount"], "2687142")
	for emp, reason := range map[string]string{sp2: "warning_letter", prob: "probation", daily: "worker_category", unpaid: "unpaid_leave_full_period"} {
		if lines[emp]["eligible"] != false || lines[emp]["exclusionReason"] != reason || !dec(lines[emp]["amount"]).IsZero() {
			t.Errorf("excluded %s: %v", reason, lines[emp])
		}
	}
	c.Must(409, "POST", hrBase+"/service-charge-distributions/"+did+":approve", map[string]any{}) // the pool is not approved yet
	c.Must(200, "POST", accBase+"/service-charge-pools/"+str(pool["id"])+":approve", map[string]any{})

	// ESS: the employee B signs in (linked before the approval notifies the employees)
	var prev *string
	sysQueryRow(t, inst, `SELECT employee_id::text FROM platform.users WHERE email = 'role.super_admin@matrix.test'`, nil, &prev)
	sysExec(t, inst, `UPDATE platform.users SET employee_id = $1 WHERE email = 'role.super_admin@matrix.test'`, mustUUID(b))
	t.Cleanup(func() {
		sysExec(t, inst, `UPDATE platform.users SET employee_id = $1::uuid WHERE email = 'role.super_admin@matrix.test'`, prev)
	})

	// approval workflow: the General Manager of the property rejects, then approves
	talentWorkflow(t, sa, "hris.service_charge_distribution", "employees", 1)
	gmc, _ := poUser(t, sa, "general_manager", "GM "+sfx, prop)
	d = c.Must(200, "POST", hrBase+"/service-charge-distributions/"+did+":approve", map[string]any{}).JSON()
	if d["status"] != "pending_approval" {
		t.Fatalf("waiting for the approver: %v", d)
	}
	gmc.Must(200, "POST", hrBase+"/service-charge-distributions/"+did+":reject", map[string]any{"reason": "check Bima's attendance"})
	if d = c.Must(200, "GET", hrBase+"/service-charge-distributions/"+did, nil).JSON(); d["status"] != "simulated" {
		t.Fatalf("rejected → simulated: %v", d)
	}
	c.Must(200, "POST", hrBase+"/service-charge-distributions/"+did+":approve", map[string]any{})
	d = gmc.Must(200, "POST", hrBase+"/service-charge-distributions/"+did+":approve", map[string]any{"reason": "ok"}).JSON()
	if d["status"] != "approved" {
		t.Fatalf("approved: %v", d)
	}
	c.Must(409, "POST", hrBase+"/service-charge-distributions/"+did+":cancel", map[string]any{"reason": "late"})
	if n := hrEvents(t, "hris.service_charge_distributed", did); n != 1 {
		t.Fatalf("hris.service_charge_distributed: %d", n)
	}
	accDispatch(t)
	accEq(t, "service charge liability of the month (FR-SVC-04)", accBal(t, prop, "2121", ""), 0)
	accEq(t, "distribution payable (payroll)", accBal(t, prop, "2126", ""), -9_404_998)
	accEq(t, "reserve 5%", accBal(t, prop, "2127", ""), -495_000)
	accEq(t, "rounding", accBal(t, prop, "6990", ""), -2)
	c.Must(409, "POST", accBase+"/service-charge-pools", map[string]any{"year": year, "month": month}) // the distributed pool stays as approved

	// ESS statement (the signed-in employee's own lines only)
	ess := sa.Must(200, "GET", "/api/v1/ess/service-charge", nil).Items()
	if len(ess) != 1 || ess[0]["amount"] != "2687142" || ess[0]["attendanceFactor"] != "0.8" || ess[0]["period"] != lastFirst.Format("2006-01") {
		t.Fatalf("ESS service charge: %v", ess)
	}
	var ns int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.notifications n JOIN platform.users u ON u.id = n.user_id
		WHERE u.email = 'role.super_admin@matrix.test' AND n.event_code = 'hris.service_charge_statement'`, nil, &ns)
	if ns == 0 {
		t.Error("service charge statement notification")
	}

	// payroll input (SERVICE_CHARGE) and Paid when the payroll is posted
	in := poOf(poInputs(t, prop, first.Format("2006-01-02"), first.AddDate(0, 1, -1).Format("2006-01-02")), "service_charge", "SERVICE_CHARGE")
	if len(in) != 3 || in[0].Kind != hris.PayrollInputEarning || in[0].Irregular {
		t.Fatalf("service charge payroll inputs: %+v", in)
	}
	sum := decimal.Zero
	for _, x := range in {
		sum = sum.Add(x.Amount)
	}
	if !sum.Equal(decimal.NewFromInt(9_404_998)) {
		t.Fatalf("payroll inputs = distributed: %s", sum)
	}
	if early := poOf(poInputs(t, prop, lastFirst.Format("2006-01-02"), first.AddDate(0, 0, -1).Format("2006-01-02")), "service_charge", "SERVICE_CHARGE"); len(early) != 0 {
		t.Fatalf("paid with the payroll of the pay period, not before: %+v", early)
	}
	poConsume(t, prop, in)
	if d = c.Must(200, "GET", hrBase+"/service-charge-distributions/"+did, nil).JSON(); d["status"] != "paid" {
		t.Fatalf("paid with payroll: %v", d)
	}
	rep := c.Must(200, "GET", "/api/v1/reporting/reports/hris.service_charge_distribution?params[from]="+lastFirst.Format("2006-01-02")+
		"&params[to]="+invToday(), nil).JSON()
	mine, paidRows := 0, 0
	for _, x := range rep["rows"].([]any) {
		if m := x.(map[string]any); strings.HasSuffix(str(m["employee"]), " "+sfx) {
			mine++
			if m["paid"] == "yes" {
				paidRows++
			}
		}
	}
	if mine != 7 || paidRows != 3 {
		t.Fatalf("Service Charge Distribution Report: %d lines of the test, %d paid", mine, paidRows)
	}

	// a distribution can be cancelled before approval
	prev2 := lastFirst.AddDate(0, -1, 0)
	c.Must(200, "POST", accBase+"/service-charge-pools", map[string]any{"year": prev2.Year(), "month": int(prev2.Month())})
	d2 := c.Must(201, "POST", hrBase+"/service-charge-distributions", map[string]any{"year": prev2.Year(), "month": int(prev2.Month()), "notes": "test"},
		"Idempotency-Key", newKey()).JSON()
	if x := c.Must(200, "POST", hrBase+"/service-charge-distributions/"+str(d2["id"])+":cancel", map[string]any{"reason": "duplicate"}).JSON(); x["status"] != "cancelled" {
		t.Fatalf("cancelled: %v", x)
	}
}

// ── EP-12 commission & bonus payout ───────────────────────────────────────

// FR-CMS-HR-01–03, FR-PAY-05: an approved commission statement (H3) with
// a clawback → COMMISSION earning (irregular) + COMMISSION_CLAWBACK
// deduction; the statement turns Paid in CRM when the payroll is posted;
// a statement of a user without employee profile waits Unmatched until HR
// matches it; bonus programmes (manual lines, review-based generation)
// with approval → BONUS (irregular); ESS Commission & Bonus.
func TestP5PayoutsCommissionBonus(t *testing.T) {
	sa := superAdmin(t, inst)
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	sfx := hrSuffix()
	emp := hrNewEmployee(t, hr, "Sales Payout "+sfx, "SALES-EXEC", nil)
	eid := str(emp["id"])
	sales := ttLink(t, "sales_executive", eid)
	suser := userID(t, "sales_executive")
	month := mustDate(invToday()).AddDate(0, -1, 0).Format("2006-01")

	// a CRM statement with clawback (earned 1,500,000, clawback −250,000)
	stmt := uuid.New()
	sysExec(t, inst, `INSERT INTO crm.sales_commission_statements (id, property_id, number, user_id, period, period_start, period_end, earned, clawback,
		adjustments, total, currency, status, approved_at) VALUES ($1,$2,$3,$4,$5,to_date($5 || '-01','YYYY-MM-DD'),
		(to_date($5 || '-01','YYYY-MM-DD') + interval '1 month - 1 day')::date, 1500000, -250000, 0, 1250000, 'IDR', 'approved', now())`,
		stmt, inst.Main, "COM-PO-"+sfx, mustUUID(suser), month)
	poPublish(t, inst.Main, "crm.commission_approved", "crm.commission_statement", stmt, map[string]any{"statementId": stmt, "number": "COM-PO-" + sfx,
		"userId": suser, "period": month, "total": "1250000", "currency": "IDR"})
	var cp map[string]any
	for _, x := range hr.Must(200, "GET", hrBase+"/commission-payouts?status=ready", nil).Items() {
		if x["statementId"] == stmt.String() {
			cp = x
		}
	}
	if cp == nil || cp["employeeId"] != eid || cp["earning"] != "1500000" || cp["deduction"] != "250000" || cp["total"] != "1250000" {
		t.Fatalf("commission payout: %v", cp)
	}

	// an unmatched statement (the user has no employee profile) and HR's match
	_, lid := poUser(t, sa, "sales_executive", "Sales Lone "+sfx, inst.Main)
	stmt2 := uuid.New()
	poPublish(t, inst.Main, "crm.commission_approved", "crm.commission_statement", stmt2, map[string]any{"statementId": stmt2, "number": "COM-UM-" + sfx,
		"userId": lid, "period": month, "total": "400000", "currency": "IDR"})
	var um map[string]any
	for _, x := range hr.Must(200, "GET", hrBase+"/commission-payouts?status=unmatched", nil).Items() {
		if x["statementId"] == stmt2.String() {
			um = x
		}
	}
	if um == nil || um["earning"] != "400000" {
		t.Fatalf("unmatched statement: %v", um)
	}
	hr.Must(422, "POST", hrBase+"/commission-payouts/"+str(um["id"])+":match", map[string]any{})
	other := hrNewEmployee(t, hr, "Sales Other "+sfx, "SALES-EXEC", nil)
	if m := hr.Must(200, "POST", hrBase+"/commission-payouts/"+str(um["id"])+":match", map[string]any{"employeeId": other["id"]}).JSON(); m["status"] != "ready" ||
		m["employeeId"] != other["id"] {
		t.Fatalf("matched: %v", m)
	}

	// bonus programme: manual lines, workflow approval by the GM
	bp := hr.Must(201, "POST", hrBase+"/bonus-programmes", map[string]any{"name": "Annual bonus " + sfx, "bonusType": "annual", "payPeriod": invToday()[:7]},
		"Idempotency-Key", newKey()).JSON()
	bid := str(bp["id"])
	hr.Must(409, "POST", hrBase+"/bonus-programmes/"+bid+":approve", map[string]any{}) // no lines
	bp = hr.Must(200, "PUT", hrBase+"/bonus-programmes/"+bid+"/lines", map[string]any{"lines": []map[string]any{{"employeeId": eid, "amount": "2000000",
		"note": "target exceeded"}}}).JSON()
	if bp["employees"] != float64(1) || bp["total"] != "2000000" {
		t.Fatalf("bonus lines: %v", bp)
	}
	talentWorkflow(t, sa, "hris.bonus_programme", "amount", 1_000_000)
	if bp = hr.Must(200, "POST", hrBase+"/bonus-programmes/"+bid+":approve", map[string]any{}).JSON(); bp["status"] != "pending_approval" {
		t.Fatalf("bonus waiting for the GM: %v", bp)
	}
	gm := login(t, inst, "gm@demo.oneclub.id", demoPassword)
	if bp = gm.Must(200, "POST", hrBase+"/bonus-programmes/"+bid+":approve", map[string]any{"reason": "ok"}).JSON(); bp["status"] != "approved" {
		t.Fatalf("bonus approved by the GM: %v", bp)
	}
	// a second programme: generated from completed reviews, rejected, cancelled
	b2 := hr.Must(201, "POST", hrBase+"/bonus-programmes", map[string]any{"name": "Performance " + sfx, "payPeriod": invToday()[:7]},
		"Idempotency-Key", newKey()).JSON()
	hr.Must(200, "POST", hrBase+"/bonus-programmes/"+str(b2["id"])+":generate", map[string]any{})
	hr.Must(200, "PUT", hrBase+"/bonus-programmes/"+str(b2["id"])+"/lines", map[string]any{"lines": []map[string]any{{"employeeId": other["id"], "amount": "3000000"}}})
	hr.Must(200, "POST", hrBase+"/bonus-programmes/"+str(b2["id"])+":approve", map[string]any{})
	gm.Must(200, "POST", hrBase+"/bonus-programmes/"+str(b2["id"])+":reject", map[string]any{"reason": "budget"})
	if x := hr.Must(200, "GET", hrBase+"/bonus-programmes/"+str(b2["id"]), nil).JSON(); x["status"] != "draft" || x["decisionNote"] != "budget" {
		t.Fatalf("rejected bonus back to draft: %v", x)
	}
	hr.Must(200, "POST", hrBase+"/bonus-programmes/"+str(b2["id"])+":cancel", map[string]any{"reason": "budget"})

	// payroll inputs of the employee: commission, clawback, bonus
	pf, pt := poPeriod("monthly", invToday())
	in := poInputs(t, inst.Main, pf, pt, eid)
	com, claw, bonus := poOf(in, "commission", "COMMISSION"), poOf(in, "commission", "COMMISSION_CLAWBACK"), poOf(in, "bonus", "BONUS")
	if len(com) != 1 || !com[0].Amount.Equal(decimal.NewFromInt(1_500_000)) || !com[0].Irregular || com[0].Kind != hris.PayrollInputEarning ||
		len(claw) != 1 || !claw[0].Amount.Equal(decimal.NewFromInt(250_000)) || claw[0].Kind != hris.PayrollInputDeduction ||
		len(bonus) != 1 || !bonus[0].Amount.Equal(decimal.NewFromInt(2_000_000)) || !bonus[0].Irregular {
		t.Fatalf("payroll inputs: %+v", in)
	}
	// ESS before payment
	items := sales.Must(200, "GET", "/api/v1/ess/commissions", nil).Items()
	if len(items) != 2 {
		t.Fatalf("ESS commission & bonus: %v", items)
	}
	poConsume(t, inst.Main, in)
	var status, ref string
	sysQueryRow(t, inst, `SELECT status, coalesce(paid_reference, '') FROM crm.sales_commission_statements WHERE id = $1`, []any{stmt}, &status, &ref)
	if status != "paid" || !strings.HasPrefix(ref, "Payroll run ") {
		t.Fatalf("CRM statement Paid once the payroll is posted (FR-CMS-HR-03): %s %s", status, ref)
	}
	if x := hr.Must(200, "GET", hrBase+"/bonus-programmes/"+bid, nil).JSON(); x["status"] != "paid" {
		t.Fatalf("bonus programme paid: %v", x)
	}
	for _, x := range sales.Must(200, "GET", "/api/v1/ess/commissions", nil).Items() {
		if x["status"] != "paid" {
			t.Fatalf("ESS after payroll: %v", x)
		}
	}
	if r := hr.Must(200, "GET", "/api/v1/reporting/reports/hris.commission_payout?params[from]="+month+"-01&params[to]="+invToday(), nil).JSON(); len(r["rows"].([]any)) < 2 {
		t.Fatalf("Commission Payout Report: %v", r["rows"])
	}
	roleUser(t, inst, "cashier").Must(403, "GET", "/api/v1/reporting/reports/hris.commission_payout", nil)
	roleUser(t, inst, "employee_self_service").Must(403, "GET", hrBase+"/commission-payouts", nil)
}
