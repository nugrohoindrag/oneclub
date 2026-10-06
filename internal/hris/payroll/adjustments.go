package payroll

// Payroll adjustments (PRD P5 FR-PAY-05 bonus from the performance review
// with approval, corrections, one-off allowances or deductions): requested
// for an employee, period and run type, approved through the approval
// engine (document type hris.payroll_adjustment; no workflow = approved at
// once) and paid by the run of that period and type.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// PayrollAdjustment is a payroll adjustment.
type PayrollAdjustment struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	Number        string     `json:"number" db:"number"`
	EmployeeID    uuid.UUID  `json:"employeeId" db:"employee_id"`
	EmployeeNo    string     `json:"employeeNo" db:"employee_no"`
	EmployeeName  string     `json:"employeeName" db:"employee_name"`
	ComponentCode string     `json:"componentCode" db:"component_code"`
	ComponentName string     `json:"componentName" db:"component_name"`
	Kind          string     `json:"kind" db:"kind" enum:"earning,deduction"`
	Category      string     `json:"category" db:"category"`
	Taxable       bool       `json:"taxable" db:"taxable"`
	Irregular     bool       `json:"irregular" db:"irregular"`
	Amount        string     `json:"amount" db:"amount"`
	PeriodCode    string     `json:"periodCode" db:"period_code"`
	TargetRunType string     `json:"targetRunType" db:"target_run_type" enum:"regular,bonus,adjustment,final_settlement"`
	Reason        string     `json:"reason" db:"reason"`
	Source        string     `json:"source" db:"source" enum:"manual,performance_review,import"`
	ReviewID      *uuid.UUID `json:"reviewId" db:"review_id"`
	Status        string     `json:"status" db:"status" enum:"submitted,approved,rejected,cancelled,paid"`
	DecisionNote  *string    `json:"decisionNote" db:"decision_note"`
	DecidedAt     *time.Time `json:"decidedAt" db:"decided_at"`
	RunID         *uuid.UUID `json:"runId" db:"run_id"`
	RunNumber     *string    `json:"runNumber" db:"run_number"`
	CreatedAt     time.Time  `json:"createdAt" db:"created_at"`
}

// PayrollAdjustmentInput requests an adjustment.
type PayrollAdjustmentInput struct {
	EmployeeID    uuid.UUID `json:"employeeId"`
	ComponentCode string    `json:"componentCode"`
	Amount        string    `json:"amount" doc:"Positive amount (an earning pays it, a deduction withholds it)"`
	PeriodCode    string    `json:"periodCode"`
	TargetRunType string    `json:"targetRunType,omitempty" enum:"regular,bonus,adjustment,final_settlement" doc:"Default regular"`
	Reason        string    `json:"reason"`
}

// PayrollAdjustmentCancel cancels a request.
type PayrollAdjustmentCancel struct {
	Note string `json:"note"`
}

// BonusFromReviewsInput creates the bonuses of a closed review cycle.
type BonusFromReviewsInput struct {
	CycleID       uuid.UUID `json:"cycleId"`
	PeriodCode    string    `json:"periodCode"`
	ComponentCode string    `json:"componentCode,omitempty" doc:"Default BONUS"`
	TargetRunType string    `json:"targetRunType,omitempty" enum:"regular,bonus" doc:"Default bonus"`
}

// BonusFromReviewsResult reports the bonuses created.
type BonusFromReviewsResult struct {
	Created     int                 `json:"created"`
	Skipped     int                 `json:"skipped" doc:"Reviews without bonus months or already requested"`
	Adjustments []PayrollAdjustment `json:"adjustments"`
}

const adjustmentSelect = `SELECT a.id, a.number, a.employee_id, e.employee_no, e.full_name AS employee_name, a.component_code, a.component_name, a.kind, a.category,
	a.taxable, a.irregular, a.amount::text AS amount, a.period_code, a.target_run_type, a.reason, a.source, a.review_id, a.status, a.decision_note, a.decided_at,
	a.run_id, r.number AS run_number, a.created_at
	FROM hris.payroll_adjustments a JOIN hris.employees e ON e.id = a.employee_id LEFT JOIN hris.payroll_runs r ON r.id = a.run_id`

var adjStatuses = []string{"submitted", "approved", "rejected", "cancelled", "paid"}

func (m *Module) registerAdjustments(reg *route.Registry) {
	tag := "HRIS Payroll"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/payroll-adjustments", Summary: "Payroll adjustments and bonuses",
		Permission: PermAdjView, Response: PayrollAdjustment{}, List: true, Query: []route.Param{{Name: "status", Enum: adjStatuses}, {Name: "periodCode"},
			{Name: "employeeId"}}, Handler: listRead(m.DB, m.adjustmentsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/payroll-adjustments",
		Summary: "Request a payroll adjustment (approval engine; approved at once without a workflow)", Permission: PermAdjManage,
		Request: PayrollAdjustmentInput{}, Response: PayrollAdjustment{}, Idempotent: true, Handler: handle.Write(m.DB, http.StatusCreated, m.createAdjustmentHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/payroll-adjustments:from-reviews",
		Summary: "Request the bonuses of a closed performance review cycle (bonus months × fixed wage)", Permission: PermAdjManage,
		Request: BonusFromReviewsInput{}, Response: BonusFromReviewsResult{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.fromReviewsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/payroll-adjustments/{id}:cancel", Summary: "Cancel a payroll adjustment not yet paid",
		Permission: PermAdjManage, Request: PayrollAdjustmentCancel{}, Response: PayrollAdjustment{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.cancelAdjustmentHTTP)})
}

func (m *Module) adjustmentsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]PayrollAdjustment, error) {
	emp, err := uuidParam(r, "employeeId")
	if err != nil {
		return nil, err
	}
	return handle.List[PayrollAdjustment](tx.Query(ctx, adjustmentSelect+` WHERE a.property_id = $1 AND ($2 = '' OR a.status = $2)
		AND ($3 = '' OR a.period_code = $3) AND ($4::uuid IS NULL OR a.employee_id = $4) ORDER BY a.created_at DESC LIMIT 500`,
		handle.Property(ctx), filterParam(r, "status"), filterParam(r, "periodCode"), emp))
}

func (m *Module) adjustment(ctx context.Context, tx pgx.Tx, aid uuid.UUID) (PayrollAdjustment, error) {
	rows, err := tx.Query(ctx, adjustmentSelect+` WHERE a.id = $1`, aid)
	return handle.One[PayrollAdjustment](rows, err, "payroll adjustment")
}

// requestAdjustment stores and submits an adjustment.
func (m *Module) requestAdjustment(ctx context.Context, tx pgx.Tx, property uuid.UUID, req PayrollAdjustmentInput, source string,
	review *uuid.UUID) (uuid.UUID, error) {
	month, err := parsePeriod("periodCode", req.PeriodCode)
	if err != nil {
		return uuid.Nil, err
	}
	target := req.TargetRunType
	if target == "" {
		target = hris.RunRegular
	}
	if !oneOf([]string{hris.RunRegular, hris.RunBonus, hris.RunAdjustment, hris.RunFinalSettlement}, target) {
		return uuid.Nil, enumErr("targetRunType", []string{hris.RunRegular, hris.RunBonus, hris.RunAdjustment, hris.RunFinalSettlement})
	}
	amount, err := handle.Decimal("amount", req.Amount, dec("0"))
	if err != nil {
		return uuid.Nil, err
	}
	if !amount.IsPositive() {
		return uuid.Nil, handle.Invalid("amount", "invalid", "a positive amount")
	}
	if strings.TrimSpace(req.Reason) == "" {
		return uuid.Nil, handle.Invalid("reason", "required", "is required")
	}
	e, err := hris.EmployeeByID(ctx, tx, req.EmployeeID)
	if err != nil || e.PropertyID != property {
		return uuid.Nil, handle.Invalid("employeeId", "not_found", "employee not found")
	}
	cat, err := loadComponents(ctx, tx, property, month)
	if err != nil {
		return uuid.Nil, err
	}
	code := strings.ToUpper(strings.TrimSpace(req.ComponentCode))
	c, ok := cat[code]
	if !ok {
		return uuid.Nil, handle.Invalid("componentCode", "unknown_component", "unknown pay component "+code)
	}
	if c.Category == hris.CatAbsence || c.Category == hris.CatLoan {
		return uuid.Nil, handle.Invalid("componentCode", "invalid", c.Code+" comes from attendance or loans")
	}
	number, err := yearlyNumber(ctx, tx, property, "PADJ", month.Year())
	if err != nil {
		return uuid.Nil, err
	}
	aid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.payroll_adjustments (id, property_id, number, employee_id, component_code, component_name, kind, category,
		taxable, irregular, pre_tax, amount, period_code, target_run_type, reason, source, review_id, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$18)`,
		aid, property, number, e.ID, c.Code, c.Name, c.Kind, c.Category, c.Taxable, c.Irregular, c.PreTax, amount, periodCode(month), target,
		strings.TrimSpace(req.Reason), source, review, actor(ctx)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return uuid.Nil, errs.Conflict("bonus_requested", "the bonus of this review was already requested")
		}
		return uuid.Nil, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.payroll_adjustment", EntityID: aid.String(),
		EntityLabel: number + " · " + e.FullName, PropertyID: &property, After: map[string]any{"componentCode": c.Code, "periodCode": periodCode(month),
			"targetRunType": target, "source": source}}); err != nil {
		return uuid.Nil, err
	}
	rid, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: AdjustmentDocumentType.Code, DocumentID: aid, DocumentRef: number,
		Title: "Payroll adjustment " + number + " · " + c.Name + " · " + e.FullName, PropertyID: property,
		Attributes: map[string]any{"amount": floatOf(amount.String()), "componentCode": c.Code, "source": source}})
	if err != nil {
		return uuid.Nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE hris.payroll_adjustments SET approval_request_id = $2 WHERE id = $1`, aid, rid)
	return aid, err
}

func (m *Module) createAdjustmentHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request, req PayrollAdjustmentInput) (PayrollAdjustment, error) {
	aid, err := m.requestAdjustment(ctx, tx, handle.Property(ctx), req, "manual", nil)
	if err != nil {
		return PayrollAdjustment{}, err
	}
	return m.adjustment(ctx, tx, aid)
}

// AdjustmentDecision applies the approval decision (approval engine hook).
func (m *Module) AdjustmentDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	var status, number, component, period string
	var emp uuid.UUID
	var creator *uuid.UUID
	err := tx.QueryRow(ctx, `SELECT status, number, component_name, period_code, employee_id, created_by FROM hris.payroll_adjustments WHERE id = $1 FOR UPDATE`,
		d.DocumentID).Scan(&status, &number, &component, &period, &emp, &creator)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil || status != "submitted" {
		return err
	}
	next := map[string]string{approval.StatusApproved: "approved", approval.StatusRejected: "rejected", approval.StatusCancelled: "cancelled"}[d.Status]
	if next == "" {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.payroll_adjustments SET status = $2, decided_at = now(), decision_note = $3 WHERE id = $1`, d.DocumentID, next,
		nullStr(d.Reason)); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "approval_" + d.Status, EntityType: "hris.payroll_adjustment",
		EntityID: d.DocumentID.String(), EntityLabel: number, PropertyID: &d.PropertyID, Reason: d.Reason, Before: map[string]any{"status": status},
		After: map[string]any{"status": next}}); err != nil {
		return err
	}
	if creator == nil || next == "cancelled" {
		return nil
	}
	var name string
	_ = tx.QueryRow(ctx, `SELECT full_name FROM hris.employees WHERE id = $1`, emp).Scan(&name)
	return m.notifyUsers(ctx, tx, d.PropertyID, []uuid.UUID{*creator}, NotifyAdjDecided, "/hris/payroll/adjustments", map[string]any{"number": number,
		"component": component, "employeeName": name, "period": period, "decision": next, "reason": d.Reason})
}

func (m *Module) cancelAdjustmentHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req PayrollAdjustmentCancel) (PayrollAdjustment, error) {
	property := handle.Property(ctx)
	aid, err := handle.ID(r)
	if err != nil {
		return PayrollAdjustment{}, err
	}
	var status string
	var approvalID, runID *uuid.UUID
	err = tx.QueryRow(ctx, `SELECT status, approval_request_id, run_id FROM hris.payroll_adjustments WHERE id = $1 AND property_id = $2 FOR UPDATE`, aid, property).
		Scan(&status, &approvalID, &runID)
	if dbtx.IsNoRows(err) {
		return PayrollAdjustment{}, errs.NotFound("payroll adjustment")
	}
	if err != nil {
		return PayrollAdjustment{}, err
	}
	if status != "submitted" && status != "approved" {
		return PayrollAdjustment{}, errs.Conflict("adjustment_closed", "only a submitted or approved adjustment can be cancelled")
	}
	if strings.TrimSpace(req.Note) == "" {
		return PayrollAdjustment{}, handle.Invalid("note", "required", "explain why the adjustment is cancelled")
	}
	if runID != nil {
		var rs string
		if err := tx.QueryRow(ctx, `SELECT status FROM hris.payroll_runs WHERE id = $1`, *runID).Scan(&rs); err != nil {
			return PayrollAdjustment{}, err
		}
		if oneOf([]string{hris.RunSubmitted, hris.RunApproved, hris.RunPosted, hris.RunPaid}, rs) {
			return PayrollAdjustment{}, errs.Conflict("adjustment_in_run", "the adjustment is in an approved payroll run")
		}
	}
	if status == "submitted" && approvalID != nil {
		if err := m.Approvals.Cancel(ctx, tx, *approvalID, req.Note); err != nil {
			return PayrollAdjustment{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.payroll_adjustments SET status = 'cancelled', run_id = NULL, decision_note = $2, updated_by = $3 WHERE id = $1`,
		aid, strings.TrimSpace(req.Note), actor(ctx)); err != nil {
		return PayrollAdjustment{}, err
	}
	out, err := m.adjustment(ctx, tx, aid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "cancel", EntityType: "hris.payroll_adjustment", EntityID: aid.String(),
		EntityLabel: out.Number, PropertyID: &property, Reason: req.Note, Before: map[string]any{"status": status}, After: map[string]any{"status": "cancelled"}})
}

// fromReviewsHTTP creates the bonuses of the completed reviews of a cycle
// (bonus months × the fixed wage at the end of the payment period).
func (m *Module) fromReviewsHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request, req BonusFromReviewsInput) (BonusFromReviewsResult, error) {
	property := handle.Property(ctx)
	out := BonusFromReviewsResult{Adjustments: []PayrollAdjustment{}}
	month, err := parsePeriod("periodCode", req.PeriodCode)
	if err != nil {
		return out, err
	}
	code := req.ComponentCode
	if code == "" {
		code = "BONUS"
	}
	target := req.TargetRunType
	if target == "" {
		target = hris.RunBonus
	}
	cfg, _, err := hris.LoadPayrollConfiguration(ctx, tx, property, hris.PolicyTime(month))
	if err != nil {
		return out, err
	}
	_, end := periodBounds(month, cfg.PeriodStartDay)
	var cycle string
	if err := tx.QueryRow(ctx, `SELECT code FROM hris.review_cycles WHERE id = $1 AND property_id = $2`, req.CycleID, property).Scan(&cycle); err != nil {
		return out, handle.Invalid("cycleId", "not_found", "review cycle not found")
	}
	rows, err := tx.Query(ctx, `SELECT r.id, r.employee_id, trim_scale(r.bonus_months)::text, coalesce(r.final_rating, '') FROM hris.performance_reviews r
		WHERE r.cycle_id = $1 AND r.status = 'completed' ORDER BY r.id`, req.CycleID)
	if err != nil {
		return out, err
	}
	type rv struct {
		id, emp uuid.UUID
		months  *string
		rating  string
	}
	var reviews []rv
	for rows.Next() {
		var x rv
		if err := rows.Scan(&x.id, &x.emp, &x.months, &x.rating); err != nil {
			rows.Close()
			return out, err
		}
		reviews = append(reviews, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	cat, err := loadComponents(ctx, tx, property, end)
	if err != nil {
		return out, err
	}
	for _, x := range reviews {
		if x.months == nil || !dec(*x.months).IsPositive() {
			out.Skipped++
			continue
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.payroll_adjustments WHERE review_id = $1 AND component_code = $2
			AND status NOT IN ('rejected', 'cancelled'))`, x.id, code).Scan(&exists); err != nil {
			return out, err
		}
		if exists {
			out.Skipped++
			continue
		}
		e, err := hris.EmployeeByID(ctx, tx, x.emp)
		if err != nil {
			return out, err
		}
		c, err := hris.ContractAt(ctx, tx, e.ID, end)
		if err != nil {
			return out, err
		}
		lines, _, err := resolvePay(ctx, tx, cat, e, c, end)
		if err != nil {
			return out, err
		}
		_, wage := fixedWage(lines)
		amount := hris.Rp(dec(wage).Mul(dec(*x.months)))
		if !amount.IsPositive() {
			out.Skipped++
			continue
		}
		rid := x.id
		aid, err := m.requestAdjustment(ctx, tx, property, PayrollAdjustmentInput{EmployeeID: e.ID, ComponentCode: code, Amount: amount.String(),
			PeriodCode: periodCode(month), TargetRunType: target, Reason: "Performance bonus " + cycle + " (" + x.rating + ", " + *x.months + " month(s))"},
			"performance_review", &rid)
		if err != nil {
			return out, err
		}
		a, err := m.adjustment(ctx, tx, aid)
		if err != nil {
			return out, err
		}
		out.Adjustments = append(out.Adjustments, a)
		out.Created++
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "bonus_from_reviews", EntityType: "hris.payroll_adjustment",
		EntityLabel: "Bonuses " + cycle, PropertyID: &property, After: map[string]any{"cycle": cycle, "created": out.Created, "skipped": out.Skipped}})
}
