package e2e

// PRD P5 payroll (EP-09 Payroll Engine, EP-10 PPh 21 & BPJS, EP-15 Payroll
// Accounting, Payment & Payslip, the payroll parts of EP-16/24/26/27/28/29).
// The tests run on the MDR property (its book starts on 1 December 2025):
// each builds its own department, position and employees with contracts,
// bank accounts and tax profiles through the HRIS API, and periods relative
// to the club date (never December, whose annual PPh 21 calculation is
// covered by the unit tests of internal/hris). Time & attendance of past
// periods (unpaid leave, absence, approved overtime) is a SQL fixture of the
// read model payroll consumes (hris.attendance_days, hris.overtime_requests).

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/dbtx"
)

func init() {
	resourceCRUDSkip["hris.employee_loan"] = "installments and repayment by payroll: covered by TestP5PayrollFullRun"
}

// prTeam is the department of a payroll test on MDR.
type prTeam struct {
	sfx, unitID, unitCode, posID string
	sa                           *Client
}

var prSeq int

func prDigits(n int) string {
	prSeq++
	return fmt.Sprintf("%0*d", n, (time.Now().UnixNano()/1000+int64(prSeq)*7919)%int64(pow10(n)))
}

func pow10(n int) int64 {
	p := int64(1)
	for i := 0; i < n; i++ {
		p *= 10
	}
	return p
}

// prSetup creates a department and a position (no grade: no grade
// structure applies) on MDR.
func prSetup(t *testing.T) prTeam {
	t.Helper()
	sa := accSetupMDR(t)
	tm := prTeam{sfx: hrSuffix(), sa: sa}
	tm.unitCode = "PR" + tm.sfx
	tm.unitID = idOf(sa.Must(201, "POST", hrBase+"/org-units", map[string]any{"code": tm.unitCode, "name": "Payroll Test " + tm.sfx, "unitType": "department",
		"costCenter": "CC-" + tm.sfx}, "Idempotency-Key", newKey()))
	tm.posID = idOf(sa.Must(201, "POST", hrBase+"/positions", map[string]any{"code": "PRP" + tm.sfx, "name": "Payroll Tester", "orgUnitId": tm.unitID},
		"Idempotency-Key", newKey()))
	return tm
}

// prEmployee creates an employee with tax profile, bank account and an
// active contract.
func prEmployee(t *testing.T, tm prTeam, name, ptkp string, join time.Time, base string, allowances []map[string]any, bank string, npwp bool,
	contract map[string]any) string {
	t.Helper()
	body := map[string]any{"fullName": name + " " + tm.sfx, "positionId": tm.posID, "gender": "female", "joinDate": join.Format("2006-01-02"),
		"nik": "3603" + prDigits(12), "ptkpStatus": ptkp, "bpjsKesehatanNo": "0001" + prDigits(9), "bpjsKetenagakerjaanNo": "2" + prDigits(10),
		"employmentStatus": "permanent"}
	if npwp {
		body["npwp"] = "09" + prDigits(13)
	}
	if contract != nil && contract["contractType"] == "pkwt" {
		body["employmentStatus"] = "contract"
	}
	eid := idOf(tm.sa.Must(201, "POST", hrBase+"/employees", body, "Idempotency-Key", newKey()))
	if bank != "" {
		tm.sa.Must(201, "POST", hrBase+"/bank-accounts", map[string]any{"employeeId": eid, "bankCode": bank, "bankName": bank, "accountNo": "52" + prDigits(8),
			"accountName": name}, "Idempotency-Key", newKey())
	}
	if allowances == nil {
		allowances = []map[string]any{}
	}
	c := map[string]any{"employeeId": eid, "contractType": "pkwtt", "startDate": join.Format("2006-01-02"), "baseSalary": base, "allowances": allowances}
	for k, v := range contract {
		c[k] = v
	}
	cid := idOf(tm.sa.Must(201, "POST", hrBase+"/contracts", c, "Idempotency-Key", newKey()))
	tm.sa.Must(200, "POST", hrBase+"/contracts/"+cid+":activate", map[string]any{})
	return eid
}

// prMonth is the first day of the month k months before the club's month,
// moved further back while skip says so.
func prMonth(k int, skip func(time.Time) bool) time.Time {
	now := time.Now().In(clubLoc(inst))
	for {
		m := time.Date(now.Year(), now.Month()-time.Month(k), 1, 0, 0, 0, 0, time.UTC)
		if skip == nil || !skip(m) {
			return m
		}
		k++
	}
}

func notDecember(m time.Time) bool { return m.Month() == time.December }

func ymdOf(t time.Time) string { return t.Format("2006-01-02") }

func monthEnd(m time.Time) time.Time { return m.AddDate(0, 1, -1) }

// prRole gives a matrix user a role at MDR for the test.
func prRole(t *testing.T, role string) {
	t.Helper()
	email := "role." + role + "@matrix.test"
	sysExec(t, inst, `INSERT INTO platform.role_assignments (id, user_id, role_id, property_id)
		SELECT gen_random_uuid(), u.id, r.id, $1 FROM platform.users u, platform.roles r WHERE u.email = $2 AND r.code = $3 AND r.is_template
		ON CONFLICT DO NOTHING`, inst.MDR, email, role)
	t.Cleanup(func() {
		sysExec(t, inst, `DELETE FROM platform.role_assignments WHERE property_id = $1 AND user_id = (SELECT id FROM platform.users WHERE email = $2)
			AND role_id = (SELECT id FROM platform.roles WHERE code = $3 AND is_template)`, inst.MDR, email, role)
	})
}

// prUser logs a matrix user in on MDR.
func prUser(t *testing.T, role string) *Client {
	t.Helper()
	prRole(t, role)
	c := roleUser(t, inst, role)
	c.Property = inst.MDR
	return c
}

// prAbsence stores a closed attendance day of a past period (fixture).
func prDay(t *testing.T, emp string, day time.Time, status string, unpaidLeave bool) {
	t.Helper()
	sched := time.Date(day.Year(), day.Month(), day.Day(), 8, 0, 0, 0, clubLoc(inst))
	var paid any
	frac := "0"
	if unpaidLeave {
		paid, frac = false, "1"
	}
	sysExec(t, inst, `INSERT INTO hris.attendance_days (id, property_id, employee_id, work_date, scheduled_start, scheduled_end, scheduled_minutes, status,
		leave_type, leave_paid, leave_fraction, finalized, locked) VALUES ($1,$2,$3,$4,$5,$6,480,$7,$8,$9,$10::numeric,true,true)`,
		uuid.New(), inst.MDR, mustUUID(emp), ymdOf(day), sched, sched.Add(9*time.Hour), status, map[bool]any{true: "UNPAID", false: nil}[unpaidLeave], paid, frac)
}

// prOvertime stores approved overtime of a past day (fixture): 3 hours on
// a workday = 1.5 + 2 + 2 = 5.5 multiplied hours.
func prOvertime(t *testing.T, emp string, day time.Time) {
	t.Helper()
	start := time.Date(day.Year(), day.Month(), day.Day(), 17, 0, 0, 0, clubLoc(inst))
	sysExec(t, inst, `INSERT INTO hris.overtime_requests (id, property_id, number, employee_id, work_date, starts_at, ends_at, hours, timing, reason,
		day_kind, actual_hours, payable_hours, multiplied_hours, tiers, status, decided_at) VALUES ($1,$2,$3,$4,$5,$6,$7,3,'before','Month-end stock take',
		'workday',3,3,5.5,'[{"factor":"1.5","hours":"1"},{"factor":"2","hours":"2"}]'::jsonb,'approved',now())`,
		uuid.New(), inst.MDR, "OT-PR-"+prDigits(8), mustUUID(emp), ymdOf(day), start, start.Add(3*time.Hour))
}

func prSlip(t *testing.T, c *Client, run, emp string) map[string]any {
	t.Helper()
	for _, s := range c.Must(200, "GET", hrBase+"/payroll-runs/"+run+"/payslips", nil).Items() {
		if s["employeeId"] == emp {
			return c.Must(200, "GET", hrBase+"/payslips/"+str(s["id"]), nil).JSON()
		}
	}
	t.Fatalf("no payslip of %s in run %s", emp, run)
	return nil
}

// prLine returns the amount of a payslip line (sum of the lines of the code).
func prLine(slip map[string]any, code string) decimal.Decimal {
	out := decimal.Zero
	for _, l := range slip["lines"].([]any) {
		m := l.(map[string]any)
		if m["code"] == code {
			out = out.Add(dec(m["amount"]))
		}
	}
	return out
}

func prEq(t *testing.T, what string, got any, want decimal.Decimal) {
	t.Helper()
	if !dec(got).Equal(want) {
		t.Fatalf("%s: want %s, got %v", what, want, got)
	}
}

func di(n int64) decimal.Decimal { return decimal.NewFromInt(n) }

// prEvent returns the outbox event of a run.
func prEvent(t *testing.T, eventType, run string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	sysQueryRow(t, inst, `SELECT id FROM platform.outbox WHERE event_type = $1 AND aggregate_id = $2::uuid ORDER BY occurred_at DESC LIMIT 1`,
		[]any{eventType, run}, &id)
	return id
}

// prInputs is the payroll input source of the tests (service charge of the
// tested employees, EP-11 contract): lines for the employees named and the
// runs that consumed them.
type prInputs struct {
	mu       sync.Mutex
	lines    map[uuid.UUID]decimal.Decimal
	consumed map[uuid.UUID]uuid.UUID // employee → run
}

func (p *prInputs) register() {
	hris.RegisterPayrollInputSource(hris.PayrollInputSource{Code: "e2e_service_charge",
		Lines: func(_ context.Context, _ dbtx.Querier, _ uuid.UUID, _, _ time.Time, employees []uuid.UUID) ([]hris.PayrollInputLine, error) {
			p.mu.Lock()
			defer p.mu.Unlock()
			var out []hris.PayrollInputLine
			for _, e := range employees {
				if amt, ok := p.lines[e]; ok {
					if _, done := p.consumed[e]; done {
						continue
					}
					out = append(out, hris.PayrollInputLine{EmployeeID: e, ComponentCode: "SERVICE_CHARGE", Description: "Service charge distribution (e2e)",
						Kind: hris.PayrollInputEarning, Amount: amt, SourceType: "e2e.distribution", SourceID: e})
				}
			}
			return out, nil
		},
		Consumed: func(_ context.Context, _ dbtx.Querier, _, run uuid.UUID, lines []hris.PayrollInputLine) error {
			p.mu.Lock()
			defer p.mu.Unlock()
			for _, l := range lines {
				p.consumed[l.EmployeeID] = run
			}
			return nil
		}})
}

// EP-09/10/15 full run (§9.1 "payroll run → payslip in ESS → bank file →
// payroll journal") with exact amounts: proration of a joiner, overtime
// (1/173 × 5.5 multiplied hours), unpaid leave and absence, a payroll input
// (service charge), an approved bonus, a loan installment, BPJS with caps,
// PPh 21 TER, the approval workflow (Finance), the period lock, posting
// (journal = run totals, inputs consumed, loan repaid, payslips released
// and notified), the bank files, payment (salaries payable cleared), ESS
// payslip (own only, PDF), statutory exports, reports, KPIs, the comparison
// with the previous period and the parallel run with its sign-off.
func TestP5PayrollFullRun(t *testing.T) {
	tm := prSetup(t)
	sa := tm.sa
	p := prMonth(3, notDecember)
	pEnd := monthEnd(p)
	days := int64(pEnd.Day())
	period := p.Format("2006-01")
	a := prEmployee(t, tm, "Ayu", "TK/0", p.AddDate(0, 0, -400), "10000000", nil, "BCA", true, nil)
	b := prEmployee(t, tm, "Bima", "K/1", p.AddDate(0, 0, -500), "5190000", nil, "MANDIRI", true, nil)
	cJoin := p.AddDate(0, 0, 15)
	c := prEmployee(t, tm, "Citra", "TK/1", cJoin, "6200000", nil, "BCA", true, nil)
	d := prEmployee(t, tm, "Dewi", "TK/0", p.AddDate(0, 0, -700), "4650000", nil, "BRI", false, nil)
	// time & attendance of the period (fixture): B 3 h overtime, D 2 days unpaid leave + 1 day absent
	prOvertime(t, b, p.AddDate(0, 0, 4))
	prDay(t, d, p.AddDate(0, 0, 5), "on_leave", true)
	prDay(t, d, p.AddDate(0, 0, 6), "on_leave", true)
	prDay(t, d, p.AddDate(0, 0, 7), "absent", false)
	// payroll input: service charge distribution of B (EP-11 contract)
	inputs := &prInputs{lines: map[uuid.UUID]decimal.Decimal{mustUUID(b): di(300_000)}, consumed: map[uuid.UUID]uuid.UUID{}}
	inputs.register()
	t.Cleanup(func() {
		hris.RegisterPayrollInputSource(hris.PayrollInputSource{Code: "e2e_service_charge",
			Lines: func(context.Context, dbtx.Querier, uuid.UUID, time.Time, time.Time, []uuid.UUID) ([]hris.PayrollInputLine, error) {
				return nil, nil
			}})
	})
	// loan (FR-PAY-02 potongan pinjaman): 400,000 per period
	loan := sa.Must(201, "POST", hrBase+"/employee-loans", map[string]any{"employeeId": d, "loanType": "loan", "principal": "1000000", "installment": "400000",
		"startPeriod": period, "reference": "PINJ-" + tm.sfx}, "Idempotency-Key", newKey()).JSON()
	sa.Must(422, "POST", hrBase+"/employee-loans", map[string]any{"employeeId": d, "principal": "100", "installment": "200", "startPeriod": period})
	sa.Must(200, "PATCH", hrBase+"/employee-loans/"+str(loan["id"]), map[string]any{"notes": "Motorbike"})
	spare := idOf(sa.Must(201, "POST", hrBase+"/employee-loans", map[string]any{"employeeId": d, "loanType": "cash_advance", "principal": "50000",
		"installment": "50000", "startPeriod": "2099-01"}, "Idempotency-Key", newKey()))
	sa.Must(204, "DELETE", hrBase+"/employee-loans/"+spare, nil)
	// FR-PAY-05: a bonus through the approval engine (no workflow → approved at once)
	bonus := sa.Must(201, "POST", hrBase+"/payroll-adjustments", map[string]any{"employeeId": a, "componentCode": "BONUS", "amount": "1000000",
		"periodCode": period, "reason": "Q2 target bonus"}, "Idempotency-Key", newKey()).JSON()
	if bonus["status"] != "approved" || bonus["category"] != "bonus" || bonus["irregular"] != true {
		t.Fatalf("bonus: %v", bonus)
	}
	gone := sa.Must(201, "POST", hrBase+"/payroll-adjustments", map[string]any{"employeeId": b, "componentCode": "BONUS", "amount": "500000",
		"periodCode": period, "reason": "duplicate"}, "Idempotency-Key", newKey()).JSON()
	sa.Must(422, "POST", hrBase+"/payroll-adjustments/"+str(gone["id"])+":cancel", map[string]any{})
	sa.Must(200, "POST", hrBase+"/payroll-adjustments/"+str(gone["id"])+":cancel", map[string]any{"note": "entered twice"})
	sa.Must(422, "POST", hrBase+"/payroll-adjustments", map[string]any{"employeeId": a, "componentCode": "UNPAID_LEAVE", "amount": "1", "periodCode": period,
		"reason": "x"})
	// FR-PAY-06: the run must be approved by Finance (workflow on this period)
	fin := prUser(t, "finance_manager")
	wf := sa.Must(201, "POST", "/api/v1/platform/approval-workflows", map[string]any{"documentType": "hris.payroll_run", "name": "Payroll " + tm.sfx,
		"steps": []map[string]any{{"stepNo": 1, "name": "Finance Manager", "approverType": "role", "approverRoleId": roleID(t, sa, "finance_manager"),
			"conditions": []map[string]any{{"attribute": "periodCode", "operator": "eq", "value": period}}}}}).JSON()
	t.Cleanup(func() {
		sa.Must(200, "PATCH", "/api/v1/platform/approval-workflows/"+str(wf["id"]), map[string]any{"status": "inactive"})
	})

	run := sa.Must(201, "POST", hrBase+"/payroll-runs", map[string]any{"runType": "regular", "periodCode": period, "orgUnitId": tm.unitID},
		"Idempotency-Key", newKey()).JSON()
	rid := str(run["id"])
	if run["status"] != "draft" || !strings.HasPrefix(str(run["number"]), "PAY-") || str(run["periodStart"])[:10] != ymdOf(p) ||
		str(run["periodEnd"])[:10] != ymdOf(pEnd) {
		t.Fatalf("draft run: %v", run)
	}
	sa.Must(409, "POST", hrBase+"/payroll-runs", map[string]any{"runType": "regular", "periodCode": period})
	sa.Must(200, "PATCH", hrBase+"/payroll-runs/"+rid, map[string]any{"name": "Payroll " + period + " (test)"})
	sa.Must(409, "POST", hrBase+"/payroll-runs/"+rid+":approve", map[string]any{})
	run = sa.Must(200, "POST", hrBase+"/payroll-runs/"+rid+":calculate", map[string]any{}).JSON()
	if run["status"] != "calculated" || run["headcount"] != float64(4) || run["timeLockStatus"] != "locked" || run["statutoryRateCode"] != "ID-2024" ||
		run["statutoryVerified"] != false || len(run["policyRefs"].([]any)) != 4 {
		t.Fatalf("calculated: %v", run)
	}
	if hrEvents(t, hris.EventPayrollCalculated, rid) != 1 {
		t.Fatal("hris.payroll_calculated")
	}

	// A: 10,000,000 + bonus 1,000,000; BPJS on 10,000,000; TER A on
	// 11,454,000 (incl. Kesehatan 400,000 + JKK 24,000 + JKM 30,000) = 3.5%
	sA := prSlip(t, sa, rid, a)
	prEq(t, "A basic", prLine(sA, "BASIC"), di(10_000_000))
	prEq(t, "A bonus", prLine(sA, "BONUS"), di(1_000_000))
	prEq(t, "A Kesehatan employee", prLine(sA, "BPJS_KESEHATAN_EE"), di(100_000))
	prEq(t, "A JHT employer", prLine(sA, "BPJS_JHT_ER"), di(370_000))
	prEq(t, "A JP employee", prLine(sA, "BPJS_JP_EE"), di(100_000))
	prEq(t, "A taxable gross", sA["taxableGross"], di(11_454_000))
	prEq(t, "A TER rate", sA["terRate"], decimal.RequireFromString("3.5"))
	prEq(t, "A PPh 21", sA["pph21"], di(400_890))
	prEq(t, "A net", sA["net"], di(10_199_110))
	if sA["taxMethod"] != "ter" || sA["terCategory"] != "A" || sA["npwp"] == nil {
		t.Fatalf("A tax: %v", sA)
	}
	// B: overtime 5,190,000 ÷ 173 × 5.5 = 165,000; service charge 300,000; K/1 TER B 0%
	sB := prSlip(t, sa, rid, b)
	prEq(t, "B overtime", prLine(sB, "OVERTIME"), di(165_000))
	prEq(t, "B service charge", prLine(sB, "SERVICE_CHARGE"), di(300_000))
	prEq(t, "B Kesehatan employer", prLine(sB, "BPJS_KESEHATAN_ER"), di(207_600))
	prEq(t, "B JKK", prLine(sB, "BPJS_JKK_ER"), di(12_456))
	prEq(t, "B taxable gross", sB["taxableGross"], di(5_890_626))
	prEq(t, "B PPh 21", sB["pph21"], di(0))
	prEq(t, "B net", sB["net"], di(5_447_400))
	// C joins on day 16: prorated basic (calendar days), BPJS on the full wage
	sC := prSlip(t, sa, rid, c)
	factor := di(days - 15).Div(di(days))
	cBasic := di(6_200_000).Mul(factor).Round(0)
	prEq(t, "C prorated basic", prLine(sC, "BASIC"), cBasic)
	prEq(t, "C BPJS employee", sC["bpjsEmployee"], di(248_000))
	prEq(t, "C net", sC["net"], cBasic.Sub(di(248_000)))
	// D: 2 days unpaid leave + 1 day absent at 4,650,000 ÷ days; loan 400,000
	sD := prSlip(t, sa, rid, d)
	daily := di(4_650_000).Div(di(days))
	unpaid, absent := daily.Mul(di(2)).Round(0), daily.Round(0)
	prEq(t, "D unpaid leave", prLine(sD, "UNPAID_LEAVE"), unpaid)
	prEq(t, "D absence", prLine(sD, "ABSENCE"), absent)
	prEq(t, "D loan", prLine(sD, "LOAN"), di(400_000))
	dGross := di(4_650_000).Sub(unpaid).Sub(absent)
	prEq(t, "D gross", sD["gross"], dGross)
	prEq(t, "D net", sD["net"], dGross.Sub(di(186_000)).Sub(di(400_000)))
	// run totals = Σ payslips
	gross := di(11_000_000).Add(di(5_655_000)).Add(cBasic).Add(dGross)
	er := di(1_024_000 + 531_456 + 634_880 + 476_160)
	ee := di(400_000 + 207_600 + 248_000 + 186_000)
	net := di(10_199_110 + 5_447_400).Add(cBasic.Sub(di(248_000))).Add(dGross.Sub(di(586_000)))
	run = sa.Must(200, "GET", hrBase+"/payroll-runs/"+rid, nil).JSON()
	prEq(t, "run gross", run["gross"], gross)
	prEq(t, "run BPJS employer", run["bpjsEmployer"], er)
	prEq(t, "run BPJS employee", run["bpjsEmployee"], ee)
	prEq(t, "run PPh 21", run["pph21"], di(400_890))
	prEq(t, "run other deductions", run["otherDeductions"], di(400_000))
	prEq(t, "run net", run["net"], net)
	prEq(t, "run employer cost", run["employerCost"], gross.Add(er))
	// FR-PAY-08 simulation: comparison with the previous period
	cmp := sa.Must(200, "GET", hrBase+"/payroll-runs/"+rid+"/comparison", nil).JSON()
	if cmp["headcount"] != float64(4) || len(cmp["rows"].([]any)) < 4 {
		t.Fatalf("comparison: %v", cmp)
	}
	// FR-POL-P5-07 / attendance locked by payroll
	locked := false
	for _, l := range sa.Must(200, "GET", hrBase+"/time-locks", nil).Items() {
		if l["reference"] == run["number"] && l["status"] == "locked" {
			locked = true
		}
	}
	if !locked {
		t.Fatal("the calculated period locks attendance")
	}
	// HR payslips are masked for viewers without sensitive data; the PDF
	if r := sa.Do("GET", hrBase+"/payslips/"+str(sA["id"])+"/pdf", nil); r.Status != 200 || !strings.HasPrefix(string(r.Body), "%PDF") ||
		r.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("payslip pdf: %d %s", r.Status, r.Header)
	}

	// approval: Finance through the workflow
	run = sa.Must(200, "POST", hrBase+"/payroll-runs/"+rid+":approve", map[string]any{"note": "please approve"}).JSON()
	if run["status"] != "submitted" || run["approvalRequestId"] == nil {
		t.Fatalf("submitted: %v", run)
	}
	sa.Must(409, "PATCH", hrBase+"/payroll-runs/"+rid, map[string]any{"name": "x"})
	sa.Must(409, "POST", hrBase+"/payroll-runs/"+rid+":post", map[string]any{})
	fin.Must(200, "POST", "/api/v1/platform/approvals/"+str(run["approvalRequestId"])+":approve", map[string]any{"reason": "checked"})
	run = sa.Must(200, "GET", hrBase+"/payroll-runs/"+rid, nil).JSON()
	if run["status"] != "approved" {
		t.Fatalf("approved: %v", run)
	}
	// AC: an approved run cannot change
	sa.Must(409, "POST", hrBase+"/payroll-runs/"+rid+":calculate", map[string]any{})
	sa.Must(409, "PATCH", hrBase+"/payroll-runs/"+rid, map[string]any{"name": "x"})
	sa.Must(409, "POST", hrBase+"/payroll-runs/"+rid+":cancel", map[string]any{"note": "x"})
	// the Back Office HR role without payroll rights sees no salaries
	roleUser(t, inst, "outlet_manager").Must(403, "GET", hrBase+"/payroll-runs/"+rid, nil)

	// parallel run (EP-28 FR-MIG-P5-05/06): legacy payroll of the period
	hr := prUser(t, "hr_manager")
	legacy := "employeeNo,componentCode,amount\n"
	var aNo, bNo string
	sysQueryRow(t, inst, `SELECT employee_no FROM hris.employees WHERE id = $1`, []any{mustUUID(a)}, &aNo)
	sysQueryRow(t, inst, `SELECT employee_no FROM hris.employees WHERE id = $1`, []any{mustUUID(b)}, &bNo)
	legacy += aNo + ",GROSS,11000000\n" + aNo + ",PPH21,400890\n" + aNo + ",BPJS_EE,400000\n" + aNo + ",NET,10199110\n" + aNo + ",BASIC,10000000\n" +
		aNo + ",BONUS,1000000\n"
	legacy += bNo + ",BASIC,5190000\n" + bNo + ",OVERTIME,165000\n" + bNo + ",SERVICE_CHARGE,300000\n" + bNo + ",GROSS,5655000\n" + bNo +
		",BPJS_EE,207600\n" + bNo + ",PPH21,10000\n" + bNo + ",NET,5437400\n"
	dry := hr.Must(200, "POST", hrBase+"/payroll-imports", map[string]any{"kind": "legacy", "periodCode": period, "csv": legacy, "dryRun": true}).JSON()
	if dry["inserted"] != float64(13) || dry["dryRun"] != true {
		t.Fatalf("legacy dry run: %v", dry)
	}
	imp := hr.Must(200, "POST", hrBase+"/payroll-imports", map[string]any{"kind": "legacy", "periodCode": period,
		"csv": legacy + "EMP-NOPE,NET,1\n"}).JSON()
	if imp["inserted"] != float64(13) || imp["failed"] != float64(1) {
		t.Fatalf("legacy import: %v", imp)
	}
	pr := hr.Must(200, "GET", hrBase+"/payroll-runs/"+rid+"/parallel-run", nil).JSON()
	if pr["employees"] != float64(2) || pr["matched"] != float64(1) || pr["withDifferences"] != float64(1) || pr["missingInLegacy"] != float64(2) {
		t.Fatalf("parallel run: %v", pr)
	}
	diffs := map[string]string{}
	for _, r := range pr["rows"].([]any) {
		m := r.(map[string]any)
		diffs[str(m["componentCode"])] = str(m["difference"])
	}
	if !dec(diffs["PPH21"]).Equal(di(-10_000)) || !dec(diffs["NET"]).Equal(di(10_000)) || len(diffs) != 2 {
		t.Fatalf("parallel run differences: %v", diffs)
	}
	hr.Must(422, "POST", hrBase+"/payroll-runs/"+rid+":sign-off-parallel-run", map[string]any{"capacity": "hr_manager"})
	hr.Must(200, "POST", hrBase+"/payroll-runs/"+rid+":sign-off-parallel-run", map[string]any{"capacity": "hr_manager",
		"note": "B: legacy taxed the service charge month; explained"})
	hr.Must(409, "POST", hrBase+"/payroll-runs/"+rid+":sign-off-parallel-run", map[string]any{"capacity": "finance_manager", "note": "x"})
	run = fin.Must(200, "POST", hrBase+"/payroll-runs/"+rid+":sign-off-parallel-run", map[string]any{"capacity": "finance_manager",
		"note": "agreed"}).JSON()
	if len(run["parallelSignOffs"].([]any)) != 2 {
		t.Fatalf("sign-offs: %v", run["parallelSignOffs"])
	}
	rep := hr.Must(200, "GET", "/api/v1/reporting/reports/hris.payroll_parallel_run?params[periodCode]="+period+"&params[from]="+ymdOf(p)+
		"&params[to]="+ymdOf(pEnd), nil).JSON()
	if len(rep["rows"].([]any)) != 2 {
		t.Fatalf("parallel run report: %v", rep["rows"])
	}

	// ESS logins of A and B (payslip notification at posting)
	prRole(t, "lifeguard")
	prRole(t, "golf_staff")
	essA := ttLink(t, "lifeguard", a)
	essB := ttLink(t, "golf_staff", b)

	// post: journal, inputs consumed, loan repaid, payslips released
	if r := fin.Do("GET", hrBase+"/payroll-runs/"+rid+"/bank-file", nil); r.Status != 200 {
		t.Fatalf("bank file of an approved run (preview): %s", r.String())
	}
	run = fin.Must(200, "POST", hrBase+"/payroll-runs/"+rid+":post", map[string]any{}).JSON()
	if run["status"] != "posted" {
		t.Fatalf("posted: %v", run)
	}
	inputs.mu.Lock()
	consumedBy := inputs.consumed[mustUUID(b)]
	inputs.mu.Unlock()
	if consumedBy.String() != rid {
		t.Fatalf("payroll input consumed by the run: %v", consumedBy)
	}
	if v := sa.Must(200, "GET", hrBase+"/employee-loans/"+str(loan["id"]), nil).JSON(); !dec(v["repaid"]).Equal(di(400_000)) || v["status"] != "active" {
		t.Fatalf("loan repaid: %v", v)
	}
	if v := sa.Must(200, "GET", hrBase+"/payroll-adjustments?periodCode="+period+"&employeeId="+a, nil).Items(); len(v) != 1 || v[0]["status"] != "paid" ||
		v[0]["runNumber"] != run["number"] {
		t.Fatalf("bonus paid by the run: %v", v)
	}
	accDispatch(t)
	ev := prEvent(t, hris.EventPayrollPosted, rid)
	if s, n := accProcessed(t, ev); s != "posted" || n != 1 {
		t.Fatalf("payroll journal: %s %d", s, n)
	}
	j := accLines(accJournalOf(t, fin, ev))
	prEq(t, "salary expense 6110", j["6110"], gross.Sub(di(300_000)))
	prEq(t, "service charge distribution payable cleared 2126", j["2126"], di(300_000))
	prEq(t, "BPJS employer expense 6111", j["6111"], er)
	prEq(t, "BPJS payable 2175", j["2175"], er.Add(ee).Neg())
	prEq(t, "PPh 21 payable 2134", j["2134"], di(-400_890))
	prEq(t, "employee receivable 1145", j["1145"], di(-400_000))
	prEq(t, "salaries payable 2176 = net", j["2176"], net.Neg())
	var exceptions int
	sysQueryRow(t, inst, `SELECT count(*) FROM accounting.posting_exceptions WHERE event_id = $1`, []any{ev}, &exceptions)
	if exceptions != 0 {
		t.Fatal("the DEF-PAYROLL rules map every line")
	}

	// bank files (generic CSV, BCA fixed width)
	csv := fin.Must(200, "GET", hrBase+"/payroll-runs/"+rid+"/bank-file?layout=generic_csv", nil)
	rows := strings.Split(strings.TrimSpace(string(csv.Body)), "\n")
	if len(rows) != 5 || !strings.HasPrefix(rows[0], "No,Employee No") || !strings.Contains(string(csv.Body), ",10199110,IDR,") {
		t.Fatalf("generic bank file: %s", csv.Body)
	}
	fixed := strings.Split(strings.TrimSpace(string(fin.Must(200, "GET", hrBase+"/payroll-runs/"+rid+"/bank-file?layout=bca_payroll", nil).Body)), "\r\n")
	if len(fixed) != 4 || fixed[0][0] != '0' || fixed[3][0] != '9' || len(fixed[1]) != 84 || !strings.Contains(fixed[1]+fixed[2], "000001019911000") {
		t.Fatalf("BCA bank file (A and C): %q", fixed)
	}
	fin.Must(422, "GET", hrBase+"/payroll-runs/"+rid+"/bank-file?layout=nope", nil)

	// ESS payslip (FR-ESS-04, FR-PPY-03): own payslips only, masked, PDF
	slips := essA.Must(200, "GET", "/api/v1/ess/payslips", nil).Items()
	if len(slips) != 1 || slips[0]["periodCode"] != period || !dec(slips[0]["net"]).Equal(di(10_199_110)) || slips[0]["employerCost"] != "" {
		t.Fatalf("my payslips: %v", slips)
	}
	mine := essA.Must(200, "GET", "/api/v1/ess/payslips/"+str(slips[0]["id"]), nil).JSON()
	if !prLine(mine, "BONUS").Equal(di(1_000_000)) || !strings.HasPrefix(str(mine["accountNo"]), "*") {
		t.Fatalf("my payslip: %v", mine)
	}
	essB.Must(404, "GET", "/api/v1/ess/payslips/"+str(slips[0]["id"]), nil)
	essB.Must(404, "GET", "/api/v1/ess/payslips/"+str(slips[0]["id"])+"/pdf", nil)
	if r := essA.Do("GET", "/api/v1/ess/payslips/"+str(slips[0]["id"])+"/pdf", nil); r.Status != 200 || !strings.HasPrefix(string(r.Body), "%PDF") {
		t.Fatalf("my payslip pdf: %d", r.Status)
	}
	essA.Must(403, "GET", hrBase+"/payroll-runs/"+rid, nil)
	waitFor(t, 20*time.Second, "payslip notification (FR-ESS-07)", func() bool {
		return ttCount(t, `SELECT count(*) FROM platform.notifications n JOIN platform.users u ON u.id = n.user_id
			WHERE u.email = 'role.lifeguard@matrix.test' AND n.event_code = 'hris.payslip_published'`) > 0
	})
	me := essA.Must(200, "GET", "/api/v1/ess/me", nil).JSON()
	found := false
	for _, s := range me["sections"].([]any) {
		if m := s.(map[string]any); m["key"] == "payslip" && m["offline"] == false {
			found = true
		}
	}
	if !found {
		t.Fatalf("ESS Payslip section: %v", me["sections"])
	}

	// payment (FR-PPY-02): Paid, salaries payable cleared against the bank
	fin.Must(422, "POST", hrBase+"/payroll-runs/"+rid+":mark-paid", map[string]any{"paidOn": ymdOf(pEnd)})
	run = fin.Must(200, "POST", hrBase+"/payroll-runs/"+rid+":mark-paid", map[string]any{"paidOn": ymdOf(pEnd), "reference": "TRF-" + tm.sfx}).JSON()
	if run["status"] != "paid" || run["bankFileCount"] != float64(3) {
		t.Fatalf("paid: %v", run)
	}
	fin.Must(409, "POST", hrBase+"/payroll-runs/"+rid+":mark-paid", map[string]any{"paidOn": ymdOf(pEnd), "reference": "again"})
	accDispatch(t)
	paidEv := prEvent(t, hris.EventPayrollPaid, rid)
	pj := accLines(accJournalOf(t, fin, paidEv))
	prEq(t, "salaries payable cleared", pj["2176"], net)
	prEq(t, "bank", pj["1121"], net.Neg())

	// statutory exports (FR-TAX-HR-04): e-Bupot, BPJS, 1721-A1
	eb := string(fin.Must(200, "GET", hrBase+"/payroll-exports/e-bupot?periodCode="+period, nil).Body)
	if !strings.Contains(eb, "21-100-01") || !strings.Contains(eb, ",400890,") || !strings.Contains(eb, aNo) {
		t.Fatalf("e-Bupot: %s", eb)
	}
	kes := string(fin.Must(200, "GET", hrBase+"/payroll-exports/bpjs-kesehatan?periodCode="+period, nil).Body)
	if strings.Count(kes, "\n") < 5 || !strings.Contains(kes, ",400000,100000,500000,") {
		t.Fatalf("BPJS Kesehatan: %s", kes)
	}
	tk := string(fin.Must(200, "GET", hrBase+"/payroll-exports/bpjs-ketenagakerjaan?periodCode="+period, nil).Body)
	if !strings.Contains(tk, "JHT Pemberi Kerja") || !strings.Contains(tk, ",370000,200000,200000,100000,24000,30000,") {
		t.Fatalf("BPJS Ketenagakerjaan: %s", tk)
	}
	a1 := string(fin.Must(200, "GET", hrBase+"/payroll-exports/1721-a1?year="+p.Format("2006")+"&employeeId="+a, nil).Body)
	if !strings.Contains(a1, "8 Jumlah Penghasilan Bruto") || !strings.Contains(a1, aNo) || !strings.Contains(a1, ",11454000,500000,300000,800000,10654000,") || !strings.HasSuffix(strings.TrimSpace(a1), ",400890") {
		t.Fatalf("1721-A1: %s", a1)
	}
	roleUser(t, inst, "golf_admin").Must(403, "GET", hrBase+"/payroll-exports/e-bupot?periodCode="+period, nil)

	// reports and KPIs (EP-27): payroll roles only
	from, to := ymdOf(p), ymdOf(pEnd)
	for _, code := range []string{"hris.payroll_summary", "hris.payroll_cost", "hris.payroll_headcount_cost", "hris.overtime_cost", "hris.withholding_tax", "hris.bpjs",
		"hris.payroll_annual"} {
		res := hr.Must(200, "GET", "/api/v1/reporting/reports/"+code+"?params[from]="+from+"&params[to]="+to, nil).JSON()
		if len(res["rows"].([]any)) == 0 {
			t.Fatalf("%s: %v", code, res)
		}
	}
	summary := hr.Must(200, "GET", "/api/v1/reporting/reports/hris.payroll_summary?params[from]="+from+"&params[to]="+to, nil).JSON()
	foundRun := false
	for _, r := range summary["rows"].([]any) {
		if m := r.(map[string]any); m["number"] == run["number"] && dec(m["net"]).Equal(net) && dec(m["employerCost"]).Equal(gross.Add(er)) {
			foundRun = true
		}
	}
	if !foundRun {
		t.Fatalf("Payroll Summary Report: %v", summary["rows"])
	}
	ot := hr.Must(200, "GET", "/api/v1/reporting/reports/hris.overtime_cost?params[from]="+from+"&params[to]="+to, nil).JSON()
	otB := false
	for _, r := range ot["rows"].([]any) {
		if m := r.(map[string]any); m["employeeNo"] == bNo && dec(m["amount"]).Equal(di(165_000)) && dec(m["multipliedHours"]).Equal(decimal.RequireFromString("5.5")) {
			otB = true
		}
	}
	if !otB {
		t.Fatalf("Overtime Cost Report: %v", ot["rows"])
	}
	roleUser(t, inst, "golf_manager").Must(403, "GET", "/api/v1/reporting/reports/hris.payroll_summary", nil)
	kpi := hr.Must(200, "GET", "/api/v1/reporting/hr-performance?from="+from+"&to="+to, nil).JSON()
	var kpiCost map[string]any
	for _, k := range kpi["kpis"].([]any) {
		if m := k.(map[string]any); m["key"] == "payroll_cost" {
			kpiCost = m
		}
	}
	if kpiCost == nil || kpiCost["status"] != "available" || dec(kpiCost["value"]).LessThan(gross.Add(er)) {
		t.Fatalf("Payroll Cost KPI: %v", kpi["kpis"])
	}
	// FR-MIG-P5-03 opening YTD import and the year to date view
	ytd := hr.Must(200, "POST", hrBase+"/payroll-imports", map[string]any{"kind": "ytd", "csv": "employeeNo,taxYear,throughMonth,gross,deductible,pph21\n" +
		aNo + "," + p.Format("2006") + ",1,10454000,300000,261350\n"}).JSON()
	if ytd["inserted"] != float64(1) {
		t.Fatalf("YTD import: %v", ytd)
	}
	for _, y := range hr.Must(200, "GET", hrBase+"/payroll-ytd?year="+p.Format("2006"), nil).Items() {
		if y["employeeId"] == a && (!dec(y["openingGross"]).Equal(di(10_454_000)) || !dec(y["totalPph21"]).Equal(di(261_350+400_890))) {
			t.Fatalf("YTD: %v", y)
		}
	}
	prof := hr.Must(200, "GET", hrBase+"/payroll-profiles?issues=true", nil).Items()
	for _, x := range prof {
		if x["employeeId"] == a {
			t.Fatalf("A is complete: %v", x)
		}
	}
}

// AC EP-09: an approved run is immutable; attendance changed afterwards
// (lock released by HR) is paid by the adjustment run of the next period
// (retro difference). A cancelled run releases its period lock.
func TestP5PayrollRetroAdjustment(t *testing.T) {
	tm := prSetup(t)
	sa := tm.sa
	q := prMonth(5, func(m time.Time) bool { return m.Month() == time.December || m.Month() == time.November })
	next := q.AddDate(0, 1, 0)
	r := prEmployee(t, tm, "Rudi", "K/0", q.AddDate(0, 0, -900), "5190000", nil, "BCA", true, nil)
	run := sa.Must(201, "POST", hrBase+"/payroll-runs", map[string]any{"runType": "regular", "periodCode": q.Format("2006-01"), "orgUnitId": tm.unitID},
		"Idempotency-Key", newKey()).JSON()
	rid := str(run["id"])
	sa.Must(200, "POST", hrBase+"/payroll-runs/"+rid+":calculate", map[string]any{})
	run = sa.Must(200, "POST", hrBase+"/payroll-runs/"+rid+":approve", map[string]any{}).JSON()
	if run["status"] != "approved" || run["timeLockStatus"] != "locked" {
		t.Fatalf("approved without workflow: %v", run)
	}
	netBefore := str(run["net"])
	// HR opens the period again and the overtime of a day is approved late
	sa.Must(200, "POST", hrBase+"/time-locks/"+str(run["timeLockId"])+":release", map[string]any{"note": "late overtime approval"})
	prOvertime(t, r, q.AddDate(0, 0, 9))
	sa.Must(409, "POST", hrBase+"/payroll-runs/"+rid+":calculate", map[string]any{})
	sa.Must(422, "POST", hrBase+"/payroll-runs", map[string]any{"runType": "adjustment", "periodCode": q.Format("2006-01"),
		"correctsPeriod": next.Format("2006-01")})
	adj := sa.Must(201, "POST", hrBase+"/payroll-runs", map[string]any{"runType": "adjustment", "periodCode": next.Format("2006-01"),
		"correctsPeriod": q.Format("2006-01"), "orgUnitId": tm.unitID}, "Idempotency-Key", newKey()).JSON()
	adj = sa.Must(200, "POST", hrBase+"/payroll-runs/"+str(adj["id"])+":calculate", map[string]any{}).JSON()
	if adj["headcount"] != float64(1) {
		t.Fatalf("adjustment run: %v", adj)
	}
	s := prSlip(t, sa, str(adj["id"]), r)
	prEq(t, "retro overtime", prLine(s, "OVERTIME"), di(165_000))
	prEq(t, "no BPJS on a correction", s["bpjsEmployee"], di(0))
	prEq(t, "retro net", s["net"], di(165_000))
	for _, l := range s["lines"].([]any) {
		if m := l.(map[string]any); m["code"] == "OVERTIME" && (m["source"] != "retro" || !strings.Contains(str(m["description"]), q.Format("2006-01"))) {
			t.Fatalf("retro line: %v", m)
		}
	}
	if v := sa.Must(200, "GET", hrBase+"/payroll-runs/"+rid, nil).JSON(); str(v["net"]) != netBefore || v["status"] != "approved" {
		t.Fatalf("the approved run is unchanged: %v", v)
	}
	// a run cancelled before approval releases its lock
	k := prMonth(9, notDecember)
	cr := sa.Must(201, "POST", hrBase+"/payroll-runs", map[string]any{"runType": "regular", "periodCode": k.Format("2006-01"), "orgUnitId": tm.unitID},
		"Idempotency-Key", newKey()).JSON()
	cr = sa.Must(200, "POST", hrBase+"/payroll-runs/"+str(cr["id"])+":calculate", map[string]any{}).JSON()
	if cr["timeLockStatus"] != "locked" {
		t.Fatalf("lock: %v", cr)
	}
	sa.Must(422, "POST", hrBase+"/payroll-runs/"+str(cr["id"])+":cancel", map[string]any{})
	cr = sa.Must(200, "POST", hrBase+"/payroll-runs/"+str(cr["id"])+":cancel", map[string]any{"note": "wrong period"}).JSON()
	if cr["status"] != "cancelled" || cr["timeLockStatus"] != "released" {
		t.Fatalf("cancelled: %v", cr)
	}
	// the period can have a regular run again
	sa.Must(201, "POST", hrBase+"/payroll-runs", map[string]any{"runType": "regular", "periodCode": k.Format("2006-01"), "orgUnitId": tm.unitID},
		"Idempotency-Key", newKey())
}

// FR-PAY-04 (EP-09 AC: THR of Rp6,000,000 after 6 months = Rp3,000,000)
// and FR-PAY-07 final settlement: leave encashment, severance and service
// award with the final tax of PP 68/2009, PKWT compensation (§16 #3).
func TestP5PayrollTHRAndFinalSettlement(t *testing.T) {
	tm := prSetup(t)
	sa := tm.sa
	m := prMonth(4, notDecember)
	thrDate := m.AddDate(0, 0, 9)
	t1 := prEmployee(t, tm, "Tia", "TK/0", thrDate.AddDate(0, -6, 0), "6000000", nil, "BCA", true, nil)
	t2 := prEmployee(t, tm, "Tono", "K/0", thrDate.AddDate(-2, 0, 0), "5000000", []map[string]any{{"code": "POSITION", "name": "Position", "amount": "500000"}},
		"BCA", true, nil)
	t3 := prEmployee(t, tm, "Tari", "TK/0", thrDate.AddDate(0, 0, -10), "5000000", nil, "BCA", true, nil)
	thr := sa.Must(201, "POST", hrBase+"/payroll-runs", map[string]any{"runType": "thr", "periodCode": m.Format("2006-01"), "thrDate": ymdOf(thrDate),
		"orgUnitId": tm.unitID}, "Idempotency-Key", newKey()).JSON()
	thr = sa.Must(200, "POST", hrBase+"/payroll-runs/"+str(thr["id"])+":calculate", map[string]any{}).JSON()
	if thr["headcount"] != float64(3) || thr["timeLockId"] != nil {
		t.Fatalf("THR run: %v", thr)
	}
	prEq(t, "THR 6 months of 6,000,000", prLine(prSlip(t, sa, str(thr["id"]), t1), "THR"), di(3_000_000))
	s2 := prSlip(t, sa, str(thr["id"]), t2)
	prEq(t, "THR base + fixed allowance", prLine(s2, "THR"), di(5_500_000))
	prEq(t, "THR PPh 21 TER A 0.25%", s2["pph21"], di(13_750))
	s3 := prSlip(t, sa, str(thr["id"]), t3)
	if !prLine(s3, "THR").IsZero() || s3["status"] != "warning" {
		t.Fatalf("no THR under one month: %v", s3)
	}

	// final settlement of two leavers of last month
	f := prMonth(2, nil)
	leave := f.AddDate(0, 0, 14)
	f1 := prEmployee(t, tm, "Fajar", "TK/0", leave.AddDate(-5, -2, 0), "10000000", nil, "BCA", true, nil)
	f2 := prEmployee(t, tm, "Fina", "TK/0", leave.AddDate(0, -18, 0), "6000000", nil, "BCA", true,
		map[string]any{"contractType": "pkwt", "endDate": hrDay(30)})
	sysExec(t, inst, `INSERT INTO hris.leave_balances (id, property_id, employee_id, leave_type, year, entitled) VALUES ($1,$2,$3,'ANNUAL',$4,6)
		ON CONFLICT (employee_id, leave_type, year) DO UPDATE SET entitled = 6, used = 0, adjusted = 0, carried_over = 0, carried_expired = 0`,
		uuid.New(), inst.MDR, mustUUID(f1), leave.Year())
	sa.Must(200, "POST", hrBase+"/employees/"+f1+":terminate", map[string]any{"terminationType": "terminated", "effectiveDate": ymdOf(leave),
		"reason": "Efficiency"})
	sa.Must(200, "POST", hrBase+"/employees/"+f2+":terminate", map[string]any{"terminationType": "contract_ended", "effectiveDate": ymdOf(leave),
		"reason": "PKWT not renewed"})
	sa.Must(422, "POST", hrBase+"/payroll-runs", map[string]any{"runType": "final_settlement", "periodCode": f.Format("2006-01")})
	fs := sa.Must(201, "POST", hrBase+"/payroll-runs", map[string]any{"runType": "final_settlement", "periodCode": f.Format("2006-01"),
		"employeeIds": []string{f1, f2}}, "Idempotency-Key", newKey()).JSON()
	fs = sa.Must(200, "POST", hrBase+"/payroll-runs/"+str(fs["id"])+":calculate", map[string]any{}).JSON()
	if fs["headcount"] != float64(2) {
		t.Fatalf("final settlement: %v", fs)
	}
	s1 := prSlip(t, sa, str(fs["id"]), f1)
	prEq(t, "severance 6 months (5 years)", prLine(s1, "SEVERANCE"), di(60_000_000))
	prEq(t, "service award 2 months", prLine(s1, "SERVICE_AWARD"), di(20_000_000))
	prEq(t, "leave encashment 6 days × 10,000,000 ÷ 21", prLine(s1, "LEAVE_ENCASHMENT"), di(2_857_143))
	prEq(t, "final tax PP 68/2009 on 80,000,000", prLine(s1, "PPH21_FINAL"), di(1_500_000))
	prEq(t, "annual PPh 21 of the leaver", s1["pph21"], di(0))
	prEq(t, "net", s1["net"], di(81_357_143))
	if s1["taxMethod"] != "annual" {
		t.Fatalf("the last period of the employment takes the annual calculation: %v", s1["taxMethod"])
	}
	s2f := prSlip(t, sa, str(fs["id"]), f2)
	prEq(t, "PKWT compensation 18 months", prLine(s2f, "PKWT_COMPENSATION"), di(9_000_000))
	prEq(t, "PKWT net", s2f["net"], di(9_000_000))
}

// FR-PAY-01 salary structures (effective-dated, versioned, two-person
// activation, resolution with the contract), pay components and EP-10
// statutory rate sets (versioned; verification; two-person activation).
func TestP5PayrollStructuresAndRates(t *testing.T) {
	tm := prSetup(t)
	sa := tm.sa
	fin := prUser(t, "finance_manager")
	res := sa.Must(200, "POST", hrBase+"/pay-components:load-defaults", map[string]any{}).JSON()
	if dec(res["total"]).IntPart() < 18 {
		t.Fatalf("default components: %v", res)
	}
	if again := sa.Must(200, "POST", hrBase+"/pay-components:load-defaults", map[string]any{}).JSON(); again["created"] != float64(0) {
		t.Fatalf("load defaults is idempotent: %v", again)
	}
	sa.Must(422, "POST", hrBase+"/pay-components", map[string]any{"code": "BADX" + tm.sfx, "name": "Bad", "kind": "earning", "preTax": true})
	e := prEmployee(t, tm, "Sari", "TK/0", time.Now().AddDate(-2, 0, 0), "7000000", nil, "BCA", true, nil)
	code := "EMP-" + tm.sfx
	body := map[string]any{"code": code, "name": "Sari allowances", "scope": "employee", "employeeId": e, "effectiveFrom": hrDay(-30),
		"lines": []map[string]any{{"componentCode": "MEAL", "amount": "40000"}, {"componentCode": "POSITION", "amount": "750000"}}}
	sa.Must(422, "POST", hrBase+"/salary-structures", map[string]any{"code": code, "name": "x", "scope": "employee", "employeeId": e, "effectiveFrom": hrDay(-30),
		"lines": []map[string]any{{"componentCode": "NOPE", "amount": "1"}}})
	sa.Must(422, "POST", hrBase+"/salary-structures", map[string]any{"code": code, "name": "x", "scope": "grade", "employeeId": e, "effectiveFrom": hrDay(-30)})
	st := sa.Must(201, "POST", hrBase+"/salary-structures", body, "Idempotency-Key", newKey()).JSON()
	sid := str(st["id"])
	if st["status"] != "draft" || st["version"] != float64(1) {
		t.Fatalf("structure: %v", st)
	}
	sa.Must(200, "PATCH", hrBase+"/salary-structures/"+sid, map[string]any{"lines": []map[string]any{{"componentCode": "MEAL", "amount": "45000"},
		{"componentCode": "POSITION", "amount": "750000"}}})
	sa.Must(403, "POST", hrBase+"/salary-structures/"+sid+":activate", map[string]any{})
	st = fin.Must(200, "POST", hrBase+"/salary-structures/"+sid+":activate", map[string]any{"note": "reviewed"}).JSON()
	if st["status"] != "active" || st["inForce"] != true {
		t.Fatalf("activated: %v", st)
	}
	sa.Must(409, "PATCH", hrBase+"/salary-structures/"+sid, map[string]any{"name": "x"})
	resolved := sa.Must(200, "GET", hrBase+"/salary-structures/resolved?employeeId="+e, nil).JSON()
	lines := map[string]map[string]any{}
	for _, l := range resolved["lines"].([]any) {
		m := l.(map[string]any)
		lines[str(m["componentCode"])] = m
	}
	if lines["BASIC"]["source"] != "contract" || lines["MEAL"]["method"] != "per_present_day" || !dec(lines["MEAL"]["amount"]).Equal(di(45_000)) ||
		!dec(resolved["fixedWage"]).Equal(di(7_750_000)) {
		t.Fatalf("resolved pay: %v", resolved)
	}
	sa.Must(422, "POST", hrBase+"/salary-structures/"+sid+":revise", map[string]any{"effectiveFrom": hrDay(-40)})
	v2 := sa.Must(201, "POST", hrBase+"/salary-structures/"+sid+":revise", map[string]any{"effectiveFrom": hrDay(30), "note": "2027 increase"}).JSON()
	if v2["version"] != float64(2) || v2["status"] != "draft" || v2["previousId"] != sid {
		t.Fatalf("revision: %v", v2)
	}
	if list := sa.Must(200, "GET", hrBase+"/salary-structures?code="+code, nil).Items(); len(list) != 2 {
		t.Fatalf("versions: %v", list)
	}
	sa.Must(422, "POST", hrBase+"/salary-structures/"+sid+":deactivate", map[string]any{})
	fin.Must(200, "POST", hrBase+"/salary-structures/"+sid+":deactivate", map[string]any{"note": "replaced"})
	if v := sa.Must(200, "GET", hrBase+"/salary-structures/resolved?employeeId="+e, nil).JSON(); !dec(v["fixedWage"]).Equal(di(7_000_000)) {
		t.Fatalf("without the structure: %v", v)
	}

	// statutory rate sets: the initial set is in force and to be verified
	var cur map[string]any
	for _, r := range sa.Must(200, "GET", hrBase+"/statutory-rates", nil).Items() {
		if r["inForce"] == true {
			cur = r
		}
	}
	if cur == nil || cur["code"] != "ID-2024" || cur["verificationStatus"] != "unverified" {
		t.Fatalf("rates in force: %v", cur)
	}
	rc := "TST-" + tm.sfx
	sa.Must(422, "POST", hrBase+"/statutory-rates", map[string]any{"code": rc, "name": "Bad", "effectiveFrom": "2099-01-01",
		"rates": map[string]any{"ptkp": map[string]any{"TK/0": "54000000"}, "terCategories": map[string]any{"TK/0": "Z"}}})
	draft := sa.Must(201, "POST", hrBase+"/statutory-rates", map[string]any{"code": rc, "name": "Test 2099", "effectiveFrom": "2099-01-01",
		"regulation": "test"}, "Idempotency-Key", newKey()).JSON()
	did := str(draft["id"])
	rates := draft["rates"].(map[string]any)
	rates["bpjs"].(map[string]any)["kesehatan"].(map[string]any)["employeePercent"] = "2"
	upd := sa.Must(200, "PATCH", hrBase+"/statutory-rates/"+did, map[string]any{"rates": rates}).JSON()
	if upd["rates"].(map[string]any)["bpjs"].(map[string]any)["kesehatan"].(map[string]any)["employeePercent"] != "2" {
		t.Fatalf("rate edit: %v", upd)
	}
	sa.Must(403, "POST", hrBase+"/statutory-rates/"+did+":activate", map[string]any{})
	finMain := roleUser(t, inst, "finance_manager")
	act := finMain.Must(200, "POST", hrBase+"/statutory-rates/"+did+":activate", map[string]any{"note": "second person"}).JSON()
	t.Cleanup(func() {
		sa.Must(200, "POST", hrBase+"/statutory-rates/"+did+":deactivate", map[string]any{"note": "test rates removed"})
	})
	if act["status"] != "active" || act["inForce"] != false {
		t.Fatalf("activated future set: %v", act)
	}
	sa.Must(409, "PATCH", hrBase+"/statutory-rates/"+did, map[string]any{"name": "x"})
	sa.Must(422, "POST", hrBase+"/statutory-rates/"+did+":verify", map[string]any{})
	ver := finMain.Must(200, "POST", hrBase+"/statutory-rates/"+did+":verify", map[string]any{"note": "KAP test cases 1–12 verified"}).JSON()
	if ver["verificationStatus"] != "verified" {
		t.Fatalf("verified: %v", ver)
	}
	// bonus from a review cycle (FR-PAY-05): the demo cycle of MAIN is not closed yet
	var cycle string
	sysQueryRow(t, inst, `SELECT id::text FROM hris.review_cycles WHERE property_id = $1 ORDER BY created_at LIMIT 1`, []any{inst.Main}, &cycle)
	hrMain := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	br := hrMain.Must(200, "POST", hrBase+"/payroll-adjustments:from-reviews", map[string]any{"cycleId": cycle, "periodCode": prMonth(0, nil).Format("2006-01")}).JSON()
	if br["created"] == nil {
		t.Fatalf("bonuses from reviews: %v", br)
	}
}
