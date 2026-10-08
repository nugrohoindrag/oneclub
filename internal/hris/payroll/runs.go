package payroll

// Payroll runs (PRD P5 FR-PAY-02/06/08, FR-PPY-01/02): create → :calculate
// (recalculation while not approved; the attendance of a closed period is
// locked through hris.LockTimePeriod) → :approve (approval engine; without
// a workflow approved at once; the period is locked) → :post (payroll
// inputs consumed, loans repaid, payslips released, hris.payroll_posted
// with the journal lines) → :mark-paid (hris.payroll_paid). An approved run
// is immutable; corrections go through an adjustment run of a later period.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// PayrollSignOff is one sign-off of the parallel run (FR-MIG-P5-05).
type PayrollSignOff struct {
	UserID   uuid.UUID `json:"userId"`
	UserName string    `json:"userName"`
	Capacity string    `json:"capacity" enum:"hr_manager,finance_manager"`
	Note     string    `json:"note"`
	SignedAt time.Time `json:"signedAt"`
}

// PayrollRun is a payroll run.
type PayrollRun struct {
	ID                 uuid.UUID        `json:"id" db:"id"`
	Number             string           `json:"number" db:"number"`
	Name               string           `json:"name" db:"name"`
	RunType            string           `json:"runType" db:"run_type" enum:"regular,thr,bonus,adjustment,final_settlement"`
	PeriodCode         string           `json:"periodCode" db:"period_code"`
	PeriodStart        time.Time        `json:"periodStart" db:"period_start"`
	PeriodEnd          time.Time        `json:"periodEnd" db:"period_end"`
	PaymentDate        time.Time        `json:"paymentDate" db:"payment_date"`
	CorrectsPeriod     *string          `json:"correctsPeriod" db:"corrects_period"`
	THRDate            *time.Time       `json:"thrDate" db:"thr_date"`
	Religions          []string         `json:"religions" db:"religions"`
	OrgUnitID          *uuid.UUID       `json:"orgUnitId" db:"org_unit_id"`
	OrgUnitName        *string          `json:"orgUnitName" db:"org_unit_name"`
	EmployeeIDs        []uuid.UUID      `json:"employeeIds" db:"employee_ids"`
	Status             string           `json:"status" db:"status" enum:"draft,calculated,submitted,approved,posted,paid,cancelled"`
	CalculationCount   int              `json:"calculationCount" db:"calculation_count"`
	Headcount          int              `json:"headcount" db:"headcount"`
	Gross              string           `json:"gross" db:"gross"`
	TaxableGross       string           `json:"taxableGross" db:"taxable_gross"`
	BPJSEmployee       string           `json:"bpjsEmployee" db:"bpjs_employee"`
	BPJSEmployer       string           `json:"bpjsEmployer" db:"bpjs_employer"`
	PPh21              string           `json:"pph21" db:"pph21"`
	OtherDeductions    string           `json:"otherDeductions" db:"other_deductions"`
	Net                string           `json:"net" db:"net"`
	EmployerCost       string           `json:"employerCost" db:"employer_cost"`
	Warnings           int              `json:"warnings" db:"warnings"`
	Currency           string           `json:"currency" db:"currency"`
	PolicyRefs         []hris.PolicyUse `json:"policyRefs" db:"policy_refs"`
	StatutoryRateSetID *uuid.UUID       `json:"statutoryRateSetId" db:"statutory_rate_set_id"`
	StatutoryRateCode  *string          `json:"statutoryRateCode" db:"statutory_rate_code"`
	StatutoryVerified  *bool            `json:"statutoryVerified" db:"statutory_verified" doc:"false: the rates are still to be verified by the tax consultant"`
	TimeLockID         *uuid.UUID       `json:"timeLockId" db:"time_lock_id"`
	TimeLockStatus     *string          `json:"timeLockStatus" db:"time_lock_status"`
	ApprovalRequestID  *uuid.UUID       `json:"approvalRequestId" db:"approval_request_id"`
	CalculatedAt       *time.Time       `json:"calculatedAt" db:"calculated_at"`
	SubmittedAt        *time.Time       `json:"submittedAt" db:"submitted_at"`
	ApprovedAt         *time.Time       `json:"approvedAt" db:"approved_at"`
	DecisionNote       *string          `json:"decisionNote" db:"decision_note"`
	PostedAt           *time.Time       `json:"postedAt" db:"posted_at"`
	PaidOn             *time.Time       `json:"paidOn" db:"paid_on"`
	PaidAt             *time.Time       `json:"paidAt" db:"paid_at"`
	PaymentReference   *string          `json:"paymentReference" db:"payment_reference"`
	BankAccountCode    *string          `json:"bankAccountCode" db:"bank_account_code"`
	BankFileCount      int              `json:"bankFileCount" db:"bank_file_count"`
	BankFileAt         *time.Time       `json:"bankFileAt" db:"bank_file_at"`
	CancelledAt        *time.Time       `json:"cancelledAt" db:"cancelled_at"`
	CancelReason       *string          `json:"cancelReason" db:"cancel_reason"`
	ParallelSignOffs   []PayrollSignOff `json:"parallelSignOffs" db:"parallel_signoffs"`
	FinanceStatus      string           `json:"financeStatus" db:"finance_status" enum:"not_posted,pending,posted,failed" doc:"Outcome of the posting in Finance & Accounting"`
	FinanceMessage     *string          `json:"financeMessage" db:"finance_message" doc:"Why the posting failed"`
	FinanceJournals    []string         `json:"financeJournals" db:"finance_journals" doc:"Journal numbers booked by Accounting"`
	FinanceUpdatedAt   *time.Time       `json:"financeUpdatedAt" db:"finance_updated_at"`
	Notes              *string          `json:"notes" db:"notes"`
	CreatedAt          time.Time        `json:"createdAt" db:"created_at"`
	UpdatedAt          time.Time        `json:"updatedAt" db:"updated_at"`
}

// PayrollRunInput creates a run (or edits a draft / calculated one).
type PayrollRunInput struct {
	RunType        string      `json:"runType,omitempty" enum:"regular,thr,bonus,adjustment,final_settlement"`
	PeriodCode     string      `json:"periodCode,omitempty" doc:"YYYY-MM; the period dates follow the Payroll Configuration periodStartDay"`
	Name           string      `json:"name,omitempty"`
	PaymentDate    string      `json:"paymentDate,omitempty" doc:"Default: the Payroll Configuration pay day of the period month"`
	CorrectsPeriod string      `json:"correctsPeriod,omitempty" doc:"Adjustment run: the approved period recalculated (retro differences)"`
	THRDate        string      `json:"thrDate,omitempty" doc:"THR run: the religious holiday (service is counted to this date)"`
	Religions      []string    `json:"religions,omitempty" doc:"THR run: only employees of these religions"`
	OrgUnitID      *uuid.UUID  `json:"orgUnitId,omitempty"`
	EmployeeIDs    []uuid.UUID `json:"employeeIds,omitempty" doc:"Limit the run to these employees (required for a final settlement)"`
	Notes          *string     `json:"notes,omitempty"`
}

// PayrollRunAction carries an optional note.
type PayrollRunAction struct {
	Note string `json:"note,omitempty"`
}

// PayrollPaymentInput confirms the payment (FR-PPY-02).
type PayrollPaymentInput struct {
	PaidOn          string `json:"paidOn"`
	Reference       string `json:"reference"`
	BankAccountCode string `json:"bankAccountCode,omitempty" doc:"GL bank account code (empty: the default bank account)"`
}

// PayrollSignOffInput signs off the parallel run.
type PayrollSignOffInput struct {
	Capacity string `json:"capacity" enum:"hr_manager,finance_manager"`
	Note     string `json:"note,omitempty"`
}

const runSelect = `SELECT r.id, r.number, r.name, r.run_type, r.period_code, r.period_start, r.period_end, r.payment_date, r.corrects_period, r.thr_date,
	r.religions, r.org_unit_id, ou.name AS org_unit_name, r.employee_ids, r.status, r.calculation_count, r.headcount, r.gross::text AS gross,
	r.taxable_gross::text AS taxable_gross, r.bpjs_employee::text AS bpjs_employee, r.bpjs_employer::text AS bpjs_employer, r.pph21::text AS pph21,
	r.other_deductions::text AS other_deductions, r.net::text AS net, r.employer_cost::text AS employer_cost, r.warnings, r.currency, r.policy_refs,
	r.statutory_rate_set_id, srs.code AS statutory_rate_code, (srs.verification_status = 'verified') AS statutory_verified, r.time_lock_id,
	tl.status AS time_lock_status, r.approval_request_id, r.calculated_at, r.submitted_at, r.approved_at, r.decision_note, r.posted_at, r.paid_on, r.paid_at,
	r.payment_reference, r.bank_account_code, r.bank_file_count, r.bank_file_at, r.cancelled_at, r.cancel_reason, r.parallel_signoffs, r.finance_status,
	r.finance_message, r.finance_journals, r.finance_updated_at, r.notes, r.created_at, r.updated_at
	FROM hris.payroll_runs r
	LEFT JOIN hris.org_units ou ON ou.id = r.org_unit_id
	LEFT JOIN hris.statutory_rate_sets srs ON srs.id = r.statutory_rate_set_id
	LEFT JOIN hris.time_locks tl ON tl.id = r.time_lock_id`

func (m *Module) registerRuns(reg *route.Registry) {
	tag := "HRIS Payroll"
	write := func(path, summary, perm string, req any, h http.HandlerFunc) {
		add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/payroll-runs/{id}" + path, Summary: summary, Permission: perm, Request: req,
			Response: PayrollRun{}, Status: http.StatusOK, Handler: h})
	}
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/payroll-runs", Summary: "Payroll runs", Permission: PermRunView,
		Response: PayrollRun{}, List: true, Query: []route.Param{{Name: "status", Enum: hris.RunStatuses}, {Name: "runType", Enum: hris.RunTypes},
			{Name: "periodCode"}, {Name: "year"}}, Handler: listRead(m.DB, m.runsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/payroll-runs", Summary: "Create a payroll run (draft)", Permission: PermRunManage,
		Request: PayrollRunInput{}, Response: PayrollRun{}, Idempotent: true, Handler: handle.Write(m.DB, http.StatusCreated, m.createRunHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/payroll-runs/{id}", Summary: "Payroll run", Permission: PermRunView,
		Response: PayrollRun{}, Handler: handle.Read(m.DB, m.getRunHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPatch, Path: "/api/v1/hris/payroll-runs/{id}", Summary: "Edit a payroll run before approval",
		Permission: PermRunManage, Request: PayrollRunInput{}, Response: PayrollRun{}, Handler: handle.Write(m.DB, http.StatusOK, m.patchRunHTTP)})
	write(":calculate", "Calculate (or recalculate) the payroll run", PermRunManage, PayrollRunAction{}, handle.Write(m.DB, http.StatusOK, m.calculateHTTP))
	write(":approve", "Submit the calculated run for approval (approved at once without a workflow)", PermRunApprove, PayrollRunAction{},
		handle.Write(m.DB, http.StatusOK, m.approveHTTP))
	write(":post", "Post the approved run: payroll journal, payslips released, inputs consumed", PermRunPost, PayrollRunAction{},
		handle.Write(m.DB, http.StatusOK, m.postHTTP))
	write(":mark-paid", "Confirm the bank payment of the posted run", PermRunPay, PayrollPaymentInput{}, handle.Write(m.DB, http.StatusOK, m.markPaidHTTP))
	write(":cancel", "Cancel a run before approval (releases the period lock)", PermRunManage, PayrollRunAction{},
		handle.Write(m.DB, http.StatusOK, m.cancelHTTP))
	write(":sign-off-parallel-run", "Sign off the parallel run of the period (HR Manager and Finance Manager)", PermRunSignOff, PayrollSignOffInput{},
		handle.Write(m.DB, http.StatusOK, m.signOffHTTP))
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/payroll-runs/{id}/payslips", Summary: "Payslips of a run", Permission: PermRunView,
		Response: PayrollSlip{}, List: true, Query: []route.Param{{Name: "q"}, {Name: "status", Enum: []string{"ok", "warning"}}},
		Handler: listRead(m.DB, m.runSlipsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/payroll-runs/{id}/comparison",
		Summary: "Simulation check: comparison with the previous period per employee (FR-PAY-08)", Permission: PermRunView, Response: PayrollComparison{},
		Handler: handle.Read(m.DB, m.comparisonHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/payroll-runs/{id}/journal", Summary: "Payroll journal lines of the run",
		Permission: PermRunView, Response: hris.PayrollJournalLine{}, List: true, Handler: listRead(m.DB, m.journalHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/payroll-runs/{id}/bank-file", Summary: "Bank transfer file of the run (CSV / fixed width)",
		Permission: PermRunPay, RawContent: "text/csv", Query: []route.Param{{Name: "layout"}}, Handler: m.bankFileHTTP})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/payroll-runs/{id}/parallel-run",
		Summary: "Parallel run: OneClub vs legacy payroll per employee and component (EP-28/29)", Permission: PermRunView, Response: ParallelRun{},
		Handler: handle.Read(m.DB, m.parallelRunHTTP)})
}

func (m *Module) runsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]PayrollRun, error) {
	return handle.List[PayrollRun](tx.Query(ctx, runSelect+` WHERE r.property_id = $1 AND ($2 = '' OR r.status = $2) AND ($3 = '' OR r.run_type = $3)
		AND ($4 = '' OR r.period_code = $4) AND ($5 = '' OR left(r.period_code, 4) = $5)
		ORDER BY r.period_code DESC, r.created_at DESC LIMIT 300`, handle.Property(ctx), filterParam(r, "status"), filterParam(r, "runType"),
		filterParam(r, "periodCode"), filterParam(r, "year")))
}

func (m *Module) run(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (PayrollRun, error) {
	rows, err := tx.Query(ctx, runSelect+` WHERE r.id = $1`, rid)
	return handle.One[PayrollRun](rows, err, "payroll run")
}

func (m *Module) getRunHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (PayrollRun, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return PayrollRun{}, err
	}
	return m.run(ctx, tx, rid)
}

func runAudit(r PayrollRun) map[string]any {
	return map[string]any{"status": r.Status, "runType": r.RunType, "periodCode": r.PeriodCode, "headcount": r.Headcount}
}

func (m *Module) createRunHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request, req PayrollRunInput) (PayrollRun, error) {
	property := handle.Property(ctx)
	if !oneOf(hris.RunTypes, req.RunType) {
		return PayrollRun{}, enumErr("runType", hris.RunTypes)
	}
	month, err := parsePeriod("periodCode", req.PeriodCode)
	if err != nil {
		return PayrollRun{}, err
	}
	rid, err := m.CreateRun(ctx, tx, property, req, month)
	if err != nil {
		return PayrollRun{}, err
	}
	out, err := m.run(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.payroll_run", EntityID: rid.String(),
		EntityLabel: out.Number + " · " + out.Name, PropertyID: &property, After: runAudit(out)})
}

// CreateRun stores a draft run (HTTP and demo).
func (m *Module) CreateRun(ctx context.Context, tx pgx.Tx, property uuid.UUID, req PayrollRunInput, month time.Time) (uuid.UUID, error) {
	cfg, _, err := hris.LoadPayrollConfiguration(ctx, tx, property, hris.PolicyTime(month))
	if err != nil {
		return uuid.Nil, err
	}
	from, to := periodBounds(month, cfg.PeriodStartDay)
	pay := time.Date(month.Year(), month.Month(), min(max(cfg.PayDay, 1), 28), 0, 0, 0, 0, time.UTC)
	if pay.Before(from) {
		pay = to
	}
	if req.PaymentDate != "" {
		if pay, err = mustDate("paymentDate", req.PaymentDate); err != nil {
			return uuid.Nil, err
		}
	}
	var corrects *string
	if req.RunType == hris.RunAdjustment && req.CorrectsPeriod != "" {
		cp, err := parsePeriod("correctsPeriod", req.CorrectsPeriod)
		if err != nil {
			return uuid.Nil, err
		}
		if !cp.Before(month) {
			return uuid.Nil, handle.Invalid("correctsPeriod", "invalid", "an adjustment corrects an earlier period")
		}
		c := periodCode(cp)
		corrects = &c
	} else if req.CorrectsPeriod != "" {
		return uuid.Nil, handle.Invalid("correctsPeriod", "invalid", "only an adjustment run corrects a period")
	}
	var thr *time.Time
	if req.THRDate != "" {
		if req.RunType != hris.RunTHR {
			return uuid.Nil, handle.Invalid("thrDate", "invalid", "only a THR run has a holiday date")
		}
		if thr, err = parseDate("thrDate", req.THRDate); err != nil {
			return uuid.Nil, err
		}
	}
	if req.RunType == hris.RunFinalSettlement && len(req.EmployeeIDs) == 0 {
		return uuid.Nil, handle.Invalid("employeeIds", "required", "name the leavers of the final settlement")
	}
	if req.RunType == hris.RunTHR && thr == nil {
		thr = &pay
	}
	religions := req.Religions
	if religions == nil {
		religions = []string{}
	}
	ids := req.EmployeeIDs
	if ids == nil {
		ids = []uuid.UUID{}
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = map[string]string{hris.RunRegular: "Payroll", hris.RunTHR: "THR", hris.RunBonus: "Bonus", hris.RunAdjustment: "Payroll Adjustment",
			hris.RunFinalSettlement: "Final Settlement"}[req.RunType] + " " + month.Format("January 2006")
	}
	number, err := yearlyNumber(ctx, tx, property, "PAY", month.Year())
	if err != nil {
		return uuid.Nil, err
	}
	rid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.payroll_runs (id, property_id, number, name, run_type, period_code, period_start, period_end, payment_date,
		corrects_period, thr_date, religions, org_unit_id, employee_ids, currency, notes, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$17)`,
		rid, property, number, name, req.RunType, periodCode(month), ymd(from), ymd(to), ymd(pay), corrects, thr, religions, req.OrgUnitID, ids,
		orDefault(cfg.Currency, "IDR"), req.Notes, actor(ctx)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return uuid.Nil, errs.Conflict("period_has_run", "the period "+periodCode(month)+" already has a regular payroll run")
		}
		return uuid.Nil, err
	}
	return rid, nil
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func (m *Module) patchRunHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req PayrollRunInput) (PayrollRun, error) {
	property := handle.Property(ctx)
	rid, err := handle.ID(r)
	if err != nil {
		return PayrollRun{}, err
	}
	run, err := loadRun(ctx, tx, rid, true)
	if err != nil {
		return PayrollRun{}, err
	}
	if run.Status != hris.RunDraft && run.Status != hris.RunCalculated {
		return PayrollRun{}, errs.Conflict("run_locked", "an approved run cannot change (FR-PAY-06); correct it with an adjustment run")
	}
	if req.RunType != "" || req.PeriodCode != "" || req.CorrectsPeriod != "" {
		return PayrollRun{}, handle.Invalid("runType", "read_only", "run type and period cannot change; cancel the run and create another")
	}
	before, err := m.run(ctx, tx, rid)
	if err != nil {
		return before, err
	}
	pay := run.PaymentDate
	if req.PaymentDate != "" {
		if pay, err = mustDate("paymentDate", req.PaymentDate); err != nil {
			return before, err
		}
	}
	thr := run.THRDate
	if req.THRDate != "" {
		if run.RunType != hris.RunTHR {
			return before, handle.Invalid("thrDate", "invalid", "only a THR run has a holiday date")
		}
		if thr, err = parseDate("thrDate", req.THRDate); err != nil {
			return before, err
		}
	}
	name := run.Name
	if strings.TrimSpace(req.Name) != "" {
		name = strings.TrimSpace(req.Name)
	}
	religions, ids, unit := run.Religions, run.EmployeeIDs, run.OrgUnitID
	if req.Religions != nil {
		religions = req.Religions
	}
	if req.EmployeeIDs != nil {
		ids = req.EmployeeIDs
	}
	if req.OrgUnitID != nil {
		unit = req.OrgUnitID
	}
	// a changed run must be calculated again
	if _, err := tx.Exec(ctx, `UPDATE hris.payroll_runs SET name = $2, payment_date = $3, thr_date = $4, religions = $5, employee_ids = $6, org_unit_id = $7,
		notes = coalesce($8, notes), status = 'draft', updated_by = $9 WHERE id = $1`, rid, name, ymd(pay), thr, religions, ids, unit, req.Notes,
		actor(ctx)); err != nil {
		return before, err
	}
	out, err := m.run(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionUpdate, EntityType: "hris.payroll_run", EntityID: rid.String(),
		EntityLabel: out.Number, PropertyID: &property, Before: runAudit(before), After: runAudit(out)})
}

// lockPeriod locks the attendance of the run period up to yesterday (the
// days of a closed period; a run of a running period locks at approval).
func (m *Module) lockPeriod(ctx context.Context, tx pgx.Tx, run runRow, day time.Time) (*uuid.UUID, error) {
	if run.RunType != hris.RunRegular && run.RunType != hris.RunFinalSettlement {
		return run.TimeLockID, nil
	}
	if run.TimeLockID != nil {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM hris.time_locks WHERE id = $1`, *run.TimeLockID).Scan(&status); err != nil {
			return nil, err
		}
		if status == "locked" {
			return run.TimeLockID, nil
		}
	}
	end := run.PeriodEnd
	if !end.Before(day) {
		end = day.AddDate(0, 0, -1)
	}
	if end.Before(run.PeriodStart) {
		return nil, nil
	}
	lid, err := hris.LockTimePeriod(ctx, tx, run.PropertyID, run.PeriodStart, end, run.Number, actor(ctx))
	if err != nil {
		return nil, err
	}
	return &lid, nil
}

// Calculate (re)calculates a run (HTTP and demo).
func (m *Module) Calculate(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (hris.PayrollTotals, error) {
	run, err := loadRun(ctx, tx, rid, true)
	if err != nil {
		return hris.PayrollTotals{}, err
	}
	if run.Status != hris.RunDraft && run.Status != hris.RunCalculated {
		return hris.PayrollTotals{}, errs.Conflict("run_locked", "only a draft or calculated run can be calculated; an approved run is corrected by an adjustment run")
	}
	if run.RunType == hris.RunRegular {
		if _, _, err := ensureComponents(ctx, tx, run.PropertyID); err != nil {
			return hris.PayrollTotals{}, err
		}
	}
	slips, c, err := m.calculateRun(ctx, tx, run)
	if err != nil {
		return hris.PayrollTotals{}, err
	}
	totals, sum, err := saveSlips(ctx, tx, run, slips)
	if err != nil {
		return totals, err
	}
	lock := run.TimeLockID
	if c.processing.LockAtCalculation && !run.PeriodEnd.After(c.today.AddDate(0, 0, -1)) {
		if lock, err = m.lockPeriod(ctx, tx, run, c.today); err != nil {
			return totals, err
		}
	}
	refs, _ := jsonOf(c.refs)
	if _, err := tx.Exec(ctx, `UPDATE hris.payroll_runs SET status = 'calculated', calculation_count = calculation_count + 1, headcount = $2, gross = $3,
		taxable_gross = $4, bpjs_employee = $5, bpjs_employer = $6, pph21 = $7, other_deductions = $8, net = $9, employer_cost = $10, warnings = $11,
		policy_refs = $12, statutory_rate_set_id = $13, time_lock_id = $14, calculated_at = now(), calculated_by = $15, decision_note = NULL WHERE id = $1`,
		rid, totals.Headcount, sum["gross"], sum["taxable"], sum["bpjsEE"], sum["bpjsER"], sum["pph21"], sum["other"], sum["net"], sum["cost"],
		totals.EmployeesWarning, refs, c.rateSet, lock, actor(ctx)); err != nil {
		return totals, err
	}
	if m.Events != nil {
		p := run.PropertyID
		if _, err := m.Events.Publish(ctx, tx, hris.EventPayrollCalculated, "hris.payroll_run", &rid, &p, hris.PayrollCalculated{RunID: rid, Number: run.Number,
			PropertyID: run.PropertyID, RunType: run.RunType, PeriodCode: run.PeriodCode, PeriodStart: ymd(run.PeriodStart), PeriodEnd: ymd(run.PeriodEnd),
			PaymentDate: ymd(run.PaymentDate), Totals: totals, Calculation: run.Calculations + 1, PolicyRefs: c.refs}); err != nil {
			return totals, err
		}
	}
	return totals, nil
}

func (m *Module) calculateHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req PayrollRunAction) (PayrollRun, error) {
	property := handle.Property(ctx)
	rid, err := handle.ID(r)
	if err != nil {
		return PayrollRun{}, err
	}
	if _, err := m.inProperty(ctx, tx, rid, property); err != nil {
		return PayrollRun{}, err
	}
	if _, err := m.Calculate(ctx, tx, rid); err != nil {
		return PayrollRun{}, err
	}
	out, err := m.run(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "calculate", EntityType: "hris.payroll_run", EntityID: rid.String(),
		EntityLabel: out.Number, PropertyID: &property, Reason: req.Note, After: runAudit(out),
		Metadata: map[string]any{"calculation": out.CalculationCount, "warnings": out.Warnings}})
}

func (m *Module) inProperty(ctx context.Context, tx pgx.Tx, rid, property uuid.UUID) (runRow, error) {
	run, err := loadRun(ctx, tx, rid, false)
	if err != nil {
		return run, err
	}
	if run.PropertyID != property {
		return run, errs.NotFound("payroll run")
	}
	return run, nil
}

func (m *Module) approveHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req PayrollRunAction) (PayrollRun, error) {
	property := handle.Property(ctx)
	rid, err := handle.ID(r)
	if err != nil {
		return PayrollRun{}, err
	}
	run, err := loadRun(ctx, tx, rid, true)
	if err != nil {
		return PayrollRun{}, err
	}
	if run.PropertyID != property {
		return PayrollRun{}, errs.NotFound("payroll run")
	}
	if run.Status != hris.RunCalculated {
		return PayrollRun{}, errs.Conflict("run_not_calculated", "only a calculated run can be submitted for approval")
	}
	if err := m.blockingExceptions(ctx, tx, rid); err != nil {
		return PayrollRun{}, err
	}
	cur, err := m.run(ctx, tx, rid)
	if err != nil {
		return cur, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.payroll_runs SET status = 'submitted', submitted_at = now(), submitted_by = $2 WHERE id = $1`, rid,
		actor(ctx)); err != nil {
		return cur, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "submit", EntityType: "hris.payroll_run", EntityID: rid.String(),
		EntityLabel: cur.Number, PropertyID: &property, Reason: req.Note, Before: map[string]any{"status": cur.Status},
		After: map[string]any{"status": hris.RunSubmitted}}); err != nil {
		return cur, err
	}
	aid, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: RunDocumentType.Code, DocumentID: rid, DocumentRef: cur.Number,
		Title: "Payroll run " + cur.Number + " · " + cur.Name + " · " + itoa(cur.Headcount) + " employees", PropertyID: property,
		Attributes: map[string]any{"net": floatOf(cur.Net), "gross": floatOf(cur.Gross), "headcount": float64(cur.Headcount), "runType": cur.RunType,
			"periodCode": cur.PeriodCode}})
	if err != nil {
		return cur, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.payroll_runs SET approval_request_id = $2 WHERE id = $1`, rid, aid); err != nil {
		return cur, err
	}
	return m.run(ctx, tx, rid)
}

func floatOf(s string) float64 {
	f, _ := dec(s).Float64()
	return f
}

// RunDecision applies the approval decision (approval engine hook):
// approved runs are locked (period lock of the attendance); a rejection
// returns the run to Calculated.
func (m *Module) RunDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	run, err := loadRun(ctx, tx, d.DocumentID, true)
	if err != nil {
		if errs.Is(err, errs.KindNotFound) {
			return nil
		}
		return err
	}
	if run.Status != hris.RunSubmitted {
		return nil
	}
	next := hris.RunCalculated
	if d.Status == approval.StatusApproved {
		next = hris.RunApproved
	}
	var approver *uuid.UUID
	if d.DecidedBy != uuid.Nil {
		u := d.DecidedBy
		approver = &u
	}
	lock := run.TimeLockID
	if next == hris.RunApproved {
		if lock, err = m.lockPeriod(ctx, tx, run, today(ctx, tx, run.PropertyID)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.payroll_runs SET status = $2, approved_at = CASE WHEN $2 = 'approved' THEN now() END,
		approved_by = CASE WHEN $2 = 'approved' THEN $3::uuid END, decision_note = $4, time_lock_id = $5 WHERE id = $1`,
		run.ID, next, approver, nullStr(d.Reason), lock); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "approval_" + d.Status, EntityType: "hris.payroll_run", EntityID: run.ID.String(),
		EntityLabel: run.Number, PropertyID: &run.PropertyID, Reason: d.Reason, Before: map[string]any{"status": run.Status},
		After: map[string]any{"status": next}}); err != nil {
		return err
	}
	if d.Status == approval.StatusCancelled {
		return nil
	}
	var headcount int
	_ = tx.QueryRow(ctx, `SELECT headcount FROM hris.payroll_runs WHERE id = $1`, run.ID).Scan(&headcount)
	decision := map[string]string{hris.RunApproved: "approved", hris.RunCalculated: "rejected"}[next]
	users := holders(ctx, tx, run.PropertyID, PermRunManage)
	if err := m.notifyUsers(ctx, tx, run.PropertyID, users, NotifyRunDecided, "/hris/payroll/runs/"+run.ID.String(),
		map[string]any{"number": run.Number, "name": run.Name, "headcount": headcount, "decision": decision, "reason": d.Reason}); err != nil {
		return err
	}
	if next == hris.RunApproved {
		return m.notifyUsers(ctx, tx, run.PropertyID, holders(ctx, tx, run.PropertyID, PermRunPost), NotifyRunApproved, "/hris/payroll/runs/"+run.ID.String(),
			map[string]any{"number": run.Number, "name": run.Name})
	}
	return nil
}

// Post books the approved run (HTTP and demo): hris.payroll_posted with the
// journal lines, payroll inputs consumed, loans repaid, adjustments paid,
// payslips released and employees notified.
func (m *Module) Post(ctx context.Context, tx pgx.Tx, rid uuid.UUID) error {
	run, err := loadRun(ctx, tx, rid, true)
	if err != nil {
		return err
	}
	if run.Status != hris.RunApproved {
		return errs.Conflict("run_not_approved", "only an approved run can be posted")
	}
	// payroll inputs consumed (each source keeps its own "paid in run" marker)
	rows, err := tx.Query(ctx, `SELECT employee_id, input_source, code, input_kind, amount, coalesce(source_type, ''), coalesce(source_id, ''), irregular,
		coalesce(description, '') FROM hris.payroll_lines WHERE run_id = $1 AND input_source IS NOT NULL ORDER BY line_no`, rid)
	if err != nil {
		return err
	}
	var lines []hris.PayrollInputLine
	for rows.Next() {
		var l hris.PayrollInputLine
		var kind, sid string
		if err := rows.Scan(&l.EmployeeID, &l.Source, &l.ComponentCode, &kind, &l.Amount, &l.SourceType, &sid, &l.Irregular, &l.Description); err != nil {
			rows.Close()
			return err
		}
		l.Kind = hris.PayrollInputKind(kind)
		l.SourceID, _ = uuid.Parse(sid)
		lines = append(lines, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if err := hris.MarkPayrollInputsConsumed(ctx, tx, run.PropertyID, rid, lines); err != nil {
		return err
	}
	// loans repaid, adjustments paid
	if _, err := tx.Exec(ctx, `UPDATE hris.employee_loans l SET repaid = l.repaid + x.amount,
		status = CASE WHEN l.repaid + x.amount >= l.principal THEN 'settled' ELSE l.status END
		FROM (SELECT source_id::uuid AS id, sum(amount) AS amount FROM hris.payroll_lines WHERE run_id = $1 AND source = 'loan' GROUP BY 1) x
		WHERE l.id = x.id`, rid); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.payroll_adjustments SET status = 'paid' WHERE run_id = $1 AND status = 'approved'`, rid); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.payroll_runs SET status = 'posted', posted_at = now(), posted_by = $2 WHERE id = $1`, rid, actor(ctx)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.payroll_slips SET published_at = now() WHERE run_id = $1`, rid); err != nil {
		return err
	}
	cur, err := m.run(ctx, tx, rid)
	if err != nil {
		return err
	}
	jl, err := journalLines(ctx, tx, rid)
	if err != nil {
		return err
	}
	if m.Events != nil {
		p := run.PropertyID
		if err := m.financePending(ctx, tx, rid, "posting_event_id", func() (uuid.UUID, error) {
			return m.Events.Publish(ctx, tx, hris.EventPayrollPosted, "hris.payroll_run", &rid, &p, hris.PayrollPosted{RunID: rid, Number: run.Number,
				PropertyID: run.PropertyID, RunType: run.RunType, PeriodCode: run.PeriodCode, PeriodStart: ymd(run.PeriodStart), PeriodEnd: ymd(run.PeriodEnd),
				PaymentDate: ymd(run.PaymentDate), PostingDate: ymd(run.PeriodEnd), Currency: run.Currency, Totals: totalsOf(cur), JournalLines: jl,
				PolicyRefs: cur.PolicyRefs})
		}); err != nil {
			return err
		}
	}
	pp, _, err := LoadProcessing(ctx, tx, run.PropertyID, hris.PolicyTime(run.PeriodEnd))
	if err != nil {
		return err
	}
	if !pp.NotifyPayslips || m.Notify == nil {
		return nil
	}
	urows, err := tx.Query(ctx, `SELECT u.id FROM hris.payroll_slips s JOIN platform.users u ON u.employee_id = s.employee_id AND u.status = 'active'
		WHERE s.run_id = $1`, rid)
	if err != nil {
		return err
	}
	var users []uuid.UUID
	for urows.Next() {
		var u uuid.UUID
		if err := urows.Scan(&u); err != nil {
			urows.Close()
			return err
		}
		users = append(users, u)
	}
	urows.Close()
	return m.notifyUsers(ctx, tx, run.PropertyID, users, NotifyPayslip, "/ops/ess/payslip", map[string]any{"period": run.PeriodCode, "runName": run.Name,
		"paymentDate": ymd(run.PaymentDate)}, "in_app", "email", "whatsapp")
}

func totalsOf(r PayrollRun) hris.PayrollTotals {
	return hris.PayrollTotals{Headcount: r.Headcount, Gross: r.Gross, TaxableGross: r.TaxableGross, BPJSEmployee: r.BPJSEmployee, BPJSEmployer: r.BPJSEmployer,
		PPh21: r.PPh21, OtherDeductions: r.OtherDeductions, Net: r.Net, EmployerCost: r.EmployerCost, EmployeesWarning: r.Warnings}
}

func (m *Module) postHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req PayrollRunAction) (PayrollRun, error) {
	property := handle.Property(ctx)
	rid, err := handle.ID(r)
	if err != nil {
		return PayrollRun{}, err
	}
	if _, err := m.inProperty(ctx, tx, rid, property); err != nil {
		return PayrollRun{}, err
	}
	if err := m.Post(ctx, tx, rid); err != nil {
		return PayrollRun{}, err
	}
	out, err := m.run(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "post", EntityType: "hris.payroll_run", EntityID: rid.String(),
		EntityLabel: out.Number, PropertyID: &property, Reason: req.Note, Before: map[string]any{"status": hris.RunApproved}, After: runAudit(out)})
}

// MarkPaid confirms the payment of a posted run (HTTP and demo).
func (m *Module) MarkPaid(ctx context.Context, tx pgx.Tx, rid uuid.UUID, req PayrollPaymentInput) error {
	run, err := loadRun(ctx, tx, rid, true)
	if err != nil {
		return err
	}
	if run.Status != hris.RunPosted {
		return errs.Conflict("run_not_posted", "only a posted run can be marked paid")
	}
	paid, err := mustDate("paidOn", req.PaidOn)
	if err != nil {
		return err
	}
	if strings.TrimSpace(req.Reference) == "" {
		return handle.Invalid("reference", "required", "the bank transfer reference is required")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.payroll_runs SET status = 'paid', paid_on = $2, paid_at = now(), paid_by = $3, payment_reference = $4,
		bank_account_code = $5 WHERE id = $1`, rid, ymd(paid), actor(ctx), strings.TrimSpace(req.Reference), nullStr(req.BankAccountCode)); err != nil {
		return err
	}
	cur, err := m.run(ctx, tx, rid)
	if err != nil {
		return err
	}
	if m.Events == nil {
		return nil
	}
	p := run.PropertyID
	return m.financePending(ctx, tx, rid, "payment_event_id", func() (uuid.UUID, error) {
		return m.Events.Publish(ctx, tx, hris.EventPayrollPaid, "hris.payroll_run", &rid, &p, hris.PayrollPaid{RunID: rid, Number: run.Number,
			PropertyID: run.PropertyID, RunType: run.RunType, PeriodCode: run.PeriodCode, PaidOn: ymd(paid), Reference: strings.TrimSpace(req.Reference),
			BankAccountCode: strings.TrimSpace(req.BankAccountCode), Currency: run.Currency, Net: cur.Net, Employees: cur.Headcount,
			JournalLines: []hris.PayrollJournalLine{{Part: hris.PartNetPay, ComponentCode: "NET", ComponentName: "Net pay", Category: "net_pay", Amount: cur.Net,
				DebitRole: "salaries_payable", CreditRole: "bank", Description: "Net pay " + run.Number + " (" + strings.TrimSpace(req.Reference) + ")"}}})
	})
}

func (m *Module) markPaidHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req PayrollPaymentInput) (PayrollRun, error) {
	property := handle.Property(ctx)
	rid, err := handle.ID(r)
	if err != nil {
		return PayrollRun{}, err
	}
	if _, err := m.inProperty(ctx, tx, rid, property); err != nil {
		return PayrollRun{}, err
	}
	if err := m.MarkPaid(ctx, tx, rid, req); err != nil {
		return PayrollRun{}, err
	}
	out, err := m.run(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "mark_paid", EntityType: "hris.payroll_run", EntityID: rid.String(),
		EntityLabel: out.Number, PropertyID: &property, Before: map[string]any{"status": hris.RunPosted},
		After: map[string]any{"status": out.Status, "paidOn": req.PaidOn, "reference": req.Reference}})
}

func (m *Module) cancelHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req PayrollRunAction) (PayrollRun, error) {
	property := handle.Property(ctx)
	rid, err := handle.ID(r)
	if err != nil {
		return PayrollRun{}, err
	}
	run, err := loadRun(ctx, tx, rid, true)
	if err != nil {
		return PayrollRun{}, err
	}
	if run.PropertyID != property {
		return PayrollRun{}, errs.NotFound("payroll run")
	}
	if !oneOf([]string{hris.RunDraft, hris.RunCalculated, hris.RunSubmitted}, run.Status) {
		return PayrollRun{}, errs.Conflict("run_locked", "an approved run cannot be cancelled; correct it with an adjustment run")
	}
	if strings.TrimSpace(req.Note) == "" {
		return PayrollRun{}, handle.Invalid("note", "required", "explain why the run is cancelled")
	}
	if run.Status == hris.RunSubmitted && run.ApprovalID != nil {
		if err := m.Approvals.Cancel(ctx, tx, *run.ApprovalID, req.Note); err != nil {
			return PayrollRun{}, err
		}
	}
	if run.TimeLockID != nil {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM hris.time_locks WHERE id = $1`, *run.TimeLockID).Scan(&status); err != nil {
			return PayrollRun{}, err
		}
		if status == "locked" {
			if err := hris.ReleaseTimeLock(ctx, tx, property, *run.TimeLockID, actor(ctx), "Payroll run "+run.Number+" cancelled"); err != nil {
				return PayrollRun{}, err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.payroll_adjustments SET run_id = NULL WHERE run_id = $1 AND status = 'approved'`, rid); err != nil {
		return PayrollRun{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.payroll_runs SET status = 'cancelled', cancelled_at = now(), cancelled_by = $2, cancel_reason = $3 WHERE id = $1`,
		rid, actor(ctx), strings.TrimSpace(req.Note)); err != nil {
		return PayrollRun{}, err
	}
	out, err := m.run(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "cancel", EntityType: "hris.payroll_run", EntityID: rid.String(),
		EntityLabel: out.Number, PropertyID: &property, Reason: req.Note, Before: map[string]any{"status": run.Status}, After: runAudit(out)})
}

// ── journal (EP-15 FR-PPY-01, contract H5) ────────────────────────────────

// debitRole is the default expense / liability of an earning category.
func debitRole(category string) string {
	switch category {
	case hris.CatServiceCharge:
		return "service_charge_distribution_payable" // approved distributions (payouts area) credit it
	case hris.CatCommission:
		return "commission_payable"
	case hris.CatSeverance:
		return "severance_expense"
	}
	return "salary_expense"
}

// journalLines aggregates the payslip lines of a run per part, component and
// department (earnings and employer contributions carry the department and
// cost center; liabilities do not).
func journalLines(ctx context.Context, tx pgx.Tx, rid uuid.UUID) ([]hris.PayrollJournalLine, error) {
	rows, err := tx.Query(ctx, `SELECT l.kind, l.code, min(l.name), l.category, coalesce(l.programme, ''), l.pre_tax,
		CASE WHEN l.kind IN ('earning', 'bpjs_employer') OR l.pre_tax THEN s.org_unit_id END,
		CASE WHEN l.kind IN ('earning', 'bpjs_employer') OR l.pre_tax THEN coalesce(s.org_unit_code, '') ELSE '' END,
		CASE WHEN l.kind IN ('earning', 'bpjs_employer') OR l.pre_tax THEN coalesce(s.cost_center, '') ELSE '' END, sum(l.amount)
		FROM hris.payroll_lines l JOIN hris.payroll_slips s ON s.id = l.slip_id WHERE l.run_id = $1
		GROUP BY 1, 2, 4, 5, 6, 7, 8, 9 ORDER BY 1, 2, 8`, rid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []hris.PayrollJournalLine{}
	for rows.Next() {
		var kind, code, name, category, programme, unitCode, cc string
		var preTax bool
		var unit *uuid.UUID
		var amount decimal.Decimal
		if err := rows.Scan(&kind, &code, &name, &category, &programme, &preTax, &unit, &unitCode, &cc, &amount); err != nil {
			return nil, err
		}
		if amount.IsZero() {
			continue
		}
		l := hris.PayrollJournalLine{ComponentCode: code, ComponentName: name, Category: category, Programme: programme, OrgUnitID: unit, OrgUnitCode: unitCode,
			CostCenter: cc, Amount: money(amount), Description: name}
		switch {
		case kind == hris.LineEarning:
			l.Part, l.DebitRole, l.CreditRole = hris.PartEarning, debitRole(category), "salaries_payable"
		case kind == hris.LineDeduction && preTax:
			// unpaid leave / absence reduce the salary expense
			l.Part, l.DebitRole, l.CreditRole, l.Amount = hris.PartEarning, "salary_expense", "salaries_payable", money(amount.Neg())
		case kind == hris.LineDeduction:
			l.Part, l.DebitRole, l.CreditRole = hris.PartDeduction, "salaries_payable", "employee_receivable"
		case kind == hris.LineBPJSEmployer:
			l.Part, l.DebitRole, l.CreditRole = hris.PartEmployerContribution, "employee_benefit_expense", "bpjs_payable"
		case kind == hris.LineBPJSEmployee:
			l.Part, l.DebitRole, l.CreditRole = hris.PartBPJSEmployee, "salaries_payable", "bpjs_payable"
		case kind == hris.LineTax:
			l.Part, l.DebitRole, l.CreditRole = hris.PartPPh21, "salaries_payable", "pph21_payable"
		default:
			continue
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (m *Module) journalHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]hris.PayrollJournalLine, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return nil, err
	}
	if _, err := m.inProperty(ctx, tx, rid, handle.Property(ctx)); err != nil {
		return nil, err
	}
	return journalLines(ctx, tx, rid)
}
