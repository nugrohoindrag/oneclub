package payouts

// Sales Commission & Bonus Payout (PRD P5 EP-12): approved commission
// statements of P3 (H3) wait here until the payroll pays them — the earning
// COMMISSION and the clawback deduction (FR-CMS-HR-01/02) — and turn Paid
// in CRM when the payroll run is posted (FR-CMS-HR-03). Bonus programmes
// (performance / annual, FR-PAY-05) hold approved bonus lines (BONUS,
// irregular income), entered by HR or generated from the completed
// performance reviews (bonus months × fixed wage).

import (
	"context"
	"fmt"
	"net/http"
	"slices"
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

// CommissionPayout is an approved commission statement to pay with payroll.
type CommissionPayout struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	StatementID  uuid.UUID  `json:"statementId" db:"statement_id"`
	Number       string     `json:"number" db:"number"`
	UserID       *uuid.UUID `json:"userId" db:"user_id"`
	UserName     *string    `json:"userName" db:"user_name"`
	EmployeeID   *uuid.UUID `json:"employeeId" db:"employee_id"`
	EmployeeNo   *string    `json:"employeeNo" db:"employee_no"`
	EmployeeName *string    `json:"employeeName" db:"employee_name"`
	Period       string     `json:"period" db:"period"`
	Earned       string     `json:"earned" db:"earned"`
	Clawback     string     `json:"clawback" db:"clawback"`
	Adjustments  string     `json:"adjustments" db:"adjustments"`
	Total        string     `json:"total" db:"total"`
	Earning      string     `json:"earning" db:"earning" doc:"Payroll earning COMMISSION"`
	Deduction    string     `json:"deduction" db:"deduction" doc:"Payroll deduction COMMISSION_CLAWBACK"`
	Irregular    bool       `json:"irregular" db:"irregular" doc:"Irregular income for PPh 21"`
	Status       string     `json:"status" db:"status" enum:"unmatched,ready,paid,cancelled"`
	PayrollRunID *uuid.UUID `json:"payrollRunId" db:"payroll_run_id"`
	ConsumedAt   *time.Time `json:"consumedAt" db:"consumed_at"`
	ReceivedAt   time.Time  `json:"receivedAt" db:"received_at"`
}

const commissionSelect = `SELECT c.id, c.statement_id, c.number, c.user_id, c.user_name, c.employee_id, e.employee_no, e.full_name AS employee_name, c.period,
	trim_scale(c.earned)::text AS earned, trim_scale(c.clawback)::text AS clawback, trim_scale(c.adjustments)::text AS adjustments,
	trim_scale(c.total)::text AS total, trim_scale(c.earning)::text AS earning, trim_scale(c.deduction)::text AS deduction, c.irregular, c.status,
	c.payroll_run_id, c.consumed_at, c.received_at
	FROM hris.commission_payouts c LEFT JOIN hris.employees e ON e.id = c.employee_id`

// CommissionMatchInput links an unmatched statement to an employee.
type CommissionMatchInput struct {
	EmployeeID *uuid.UUID `json:"employeeId,omitempty" doc:"Default: the employee linked to the statement's user"`
}

func (m *Module) registerCommissions(reg *route.Registry) {
	tag := "HRIS Commissions"
	base := "/api/v1/hris/commission-payouts"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base, Summary: "Approved commission statements to pay with payroll (with clawbacks)",
		Permission: PermCommissionView, Response: CommissionPayout{}, List: true,
		Query:   []route.Param{{Name: "status", Enum: []string{"unmatched", "ready", "paid", "cancelled"}}, {Name: "period"}},
		Handler: listRead(m.DB, m.listCommissionsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:match", Summary: "Link an unmatched commission statement to its employee",
		Permission: PermCommissionManage, Request: CommissionMatchInput{}, Response: CommissionPayout{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.matchCommissionHTTP)})
}

func (m *Module) listCommissionsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]CommissionPayout, error) {
	return handle.List[CommissionPayout](tx.Query(ctx, commissionSelect+` WHERE c.property_id = $1 AND ($2 = '' OR c.status = $2)
		AND ($3 = '' OR c.period = $3) ORDER BY c.period DESC, c.received_at DESC LIMIT 500`, handle.Property(ctx), filterParam(r, "status"),
		filterParam(r, "period")))
}

func (m *Module) matchCommissionHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, in CommissionMatchInput) (CommissionPayout, error) {
	cid, err := handle.ID(r)
	if err != nil {
		return CommissionPayout{}, err
	}
	property := handle.Property(ctx)
	before, err := getOne[CommissionPayout]("commission payout")(tx.Query(ctx, commissionSelect+` WHERE c.id = $1 AND c.property_id = $2 FOR UPDATE OF c`,
		cid, property))
	if err != nil {
		return before, err
	}
	if before.Status != "unmatched" && before.Status != "ready" {
		return before, errs.Conflict("commission_paid", "the commission statement is "+before.Status)
	}
	var emp *hris.Employee
	if in.EmployeeID != nil {
		e, err := hris.EmployeeByID(ctx, tx, *in.EmployeeID)
		if err != nil {
			return before, err
		}
		emp = &e
	} else if before.UserID != nil {
		if emp, err = hris.EmployeeByUser(ctx, tx, *before.UserID); err != nil {
			return before, err
		}
	}
	if emp == nil || emp.PropertyID != property {
		return before, handle.Invalid("employeeId", "no_employee", "no employee of the property is linked to the statement's user: give employeeId")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.commission_payouts SET employee_id = $2, status = 'ready' WHERE id = $1`, cid, emp.ID); err != nil {
		return before, err
	}
	after, err := getOne[CommissionPayout]("commission payout")(tx.Query(ctx, commissionSelect+` WHERE c.id = $1`, cid))
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "match", EntityType: "hris.commission_payout", EntityID: cid.String(),
		EntityLabel: before.Number, PropertyID: &property, Before: map[string]any{"status": before.Status, "employeeId": before.EmployeeID},
		After: map[string]any{"status": after.Status, "employeeId": after.EmployeeID}})
}

// ── bonus programmes ──────────────────────────────────────────────────────

// Bonus types and statuses.
var (
	BonusTypes    = []string{"performance", "annual", "incentive", "other"}
	BonusStatuses = []string{"draft", "pending_approval", "approved", "paid", "cancelled"}
)

// BonusProgramme is a bonus programme with its totals.
type BonusProgramme struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	PropertyID        uuid.UUID  `json:"propertyId" db:"property_id"`
	Number            string     `json:"number" db:"number"`
	Name              string     `json:"name" db:"name"`
	BonusType         string     `json:"bonusType" db:"bonus_type" enum:"performance,annual,incentive,other"`
	PayPeriod         string     `json:"payPeriod" db:"pay_period" doc:"YYYY-MM of the payroll that pays it"`
	ReviewCycleID     *uuid.UUID `json:"reviewCycleId" db:"review_cycle_id"`
	Status            string     `json:"status" db:"status" enum:"draft,pending_approval,approved,paid,cancelled"`
	Employees         int        `json:"employees" db:"employees"`
	Total             string     `json:"total" db:"total"`
	ApprovalRequestID *uuid.UUID `json:"approvalRequestId" db:"approval_request_id"`
	ApprovedAt        *time.Time `json:"approvedAt" db:"approved_at"`
	DecisionNote      *string    `json:"decisionNote" db:"decision_note"`
	CancelReason      *string    `json:"cancelReason" db:"cancel_reason"`
	Notes             *string    `json:"notes" db:"notes"`
	CreatedAt         time.Time  `json:"createdAt" db:"created_at"`
}

const bonusSelect = `SELECT id, property_id, number, name, bonus_type, pay_period, review_cycle_id, status, employees, trim_scale(total)::text AS total,
	approval_request_id, approved_at, decision_note, cancel_reason, notes, created_at FROM hris.bonus_programmes`

// BonusProgrammeLine is the bonus of one employee.
type BonusProgrammeLine struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	EmployeeID   uuid.UUID  `json:"employeeId" db:"employee_id"`
	EmployeeNo   string     `json:"employeeNo" db:"employee_no"`
	FullName     string     `json:"fullName" db:"full_name"`
	Amount       string     `json:"amount" db:"amount"`
	Basis        *string    `json:"basis" db:"basis"`
	Note         *string    `json:"note" db:"note"`
	PayrollRunID *uuid.UUID `json:"payrollRunId" db:"payroll_run_id"`
	ConsumedAt   *time.Time `json:"consumedAt" db:"consumed_at"`
}

const bonusLineSelect = `SELECT id, employee_id, employee_no, full_name, trim_scale(amount)::text AS amount, basis, note, payroll_run_id, consumed_at
	FROM hris.bonus_lines`

// BonusProgrammeDetail is a programme with its lines.
type BonusProgrammeDetail struct {
	BonusProgramme
	Lines []BonusProgrammeLine `json:"lines"`
}

// BonusProgrammeInput creates a programme.
type BonusProgrammeInput struct {
	Name          string     `json:"name"`
	BonusType     string     `json:"bonusType,omitempty" enum:"performance,annual,incentive,other"`
	PayPeriod     string     `json:"payPeriod" doc:"YYYY-MM"`
	ReviewCycleID *uuid.UUID `json:"reviewCycleId,omitempty" doc:"Performance review cycle whose results are the basis"`
	Notes         string     `json:"notes,omitempty"`
}

// BonusLineInput is one employee's bonus.
type BonusLineInput struct {
	EmployeeID uuid.UUID `json:"employeeId"`
	Amount     string    `json:"amount"`
	Note       string    `json:"note,omitempty"`
}

// BonusLinesInput replaces the lines of a draft programme.
type BonusLinesInput struct {
	Lines []BonusLineInput `json:"lines"`
}

// BonusAction carries a reason / note.
type BonusAction struct {
	Reason string `json:"reason,omitempty"`
}

func (m *Module) registerBonuses(reg *route.Registry) {
	tag := "HRIS Commissions"
	base := "/api/v1/hris/bonus-programmes"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base, Summary: "Bonus programmes", Permission: PermBonusView, Response: BonusProgramme{},
		List: true, Query: []route.Param{{Name: "status", Enum: BonusStatuses}}, Handler: listRead(m.DB, m.listBonusesHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base, Summary: "Create a bonus programme (draft)", Permission: PermBonusManage,
		Request: BonusProgrammeInput{}, Response: BonusProgrammeDetail{}, Idempotent: true, Handler: handle.Write(m.DB, http.StatusCreated, m.createBonusHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/{id}", Summary: "Bonus programme with its lines", Permission: PermBonusView,
		Response: BonusProgrammeDetail{}, Handler: handle.Read(m.DB, func(ctx context.Context, tx pgx.Tx, r *http.Request) (BonusProgrammeDetail, error) {
			bid, err := handle.ID(r)
			if err != nil {
				return BonusProgrammeDetail{}, err
			}
			return m.Bonus(ctx, tx, bid)
		})})
	add(reg, tag, route.Route{Method: http.MethodPut, Path: base + "/{id}/lines", Summary: "Set the bonus lines of a draft programme",
		Permission: PermBonusManage, Request: BonusLinesInput{}, Response: BonusProgrammeDetail{}, Handler: handle.Write(m.DB, http.StatusOK, m.setBonusLinesHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:generate",
		Summary: "Generate the lines from the completed performance reviews (bonus months × fixed wage)", Permission: PermBonusManage,
		Request: handle.Empty{}, Response: BonusProgrammeDetail{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.generateBonusHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:approve", Summary: "Approve the bonus programme (approval engine)",
		Permission: PermBonusApprove, Request: BonusAction{}, Response: BonusProgrammeDetail{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.decideBonusHTTP(true))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:reject", Summary: "Reject the bonus programme at the current approval step",
		Permission: PermBonusApprove, Request: BonusAction{}, Response: BonusProgrammeDetail{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.decideBonusHTTP(false))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:cancel", Summary: "Cancel a bonus programme not yet approved",
		Permission: PermBonusManage, Request: BonusAction{}, Response: BonusProgrammeDetail{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.cancelBonusHTTP)})
}

func (m *Module) listBonusesHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]BonusProgramme, error) {
	return handle.List[BonusProgramme](tx.Query(ctx, bonusSelect+` WHERE property_id = $1 AND ($2 = '' OR status = $2) ORDER BY pay_period DESC, created_at DESC`,
		handle.Property(ctx), filterParam(r, "status")))
}

// Bonus loads a programme of the request's property with its lines.
func (m *Module) Bonus(ctx context.Context, tx pgx.Tx, bid uuid.UUID) (BonusProgrammeDetail, error) {
	b, err := getOne[BonusProgramme]("bonus programme")(tx.Query(ctx, bonusSelect+` WHERE id = $1 AND property_id = $2`, bid, handle.Property(ctx)))
	if err != nil {
		return BonusProgrammeDetail{}, err
	}
	lines, err := handle.List[BonusProgrammeLine](tx.Query(ctx, bonusLineSelect+` WHERE programme_id = $1 ORDER BY full_name, employee_id`, bid))
	return BonusProgrammeDetail{BonusProgramme: b, Lines: lines}, err
}

func lockBonus(ctx context.Context, tx pgx.Tx, bid uuid.UUID) (BonusProgramme, error) {
	return getOne[BonusProgramme]("bonus programme")(tx.Query(ctx, bonusSelect+` WHERE id = $1 AND property_id = $2 FOR UPDATE`, bid, handle.Property(ctx)))
}

func validPeriod(field, v string) (time.Time, error) {
	t, err := time.Parse("2006-01", strings.TrimSpace(v))
	if err != nil {
		return t, handle.Invalid(field, "invalid", "YYYY-MM")
	}
	return t, nil
}

func (m *Module) createBonusHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request, in BonusProgrammeInput) (BonusProgrammeDetail, error) {
	property := handle.Property(ctx)
	if err := handle.Required("name", in.Name); err != nil {
		return BonusProgrammeDetail{}, err
	}
	if in.BonusType == "" {
		in.BonusType = "performance"
	}
	if !oneOf(BonusTypes, in.BonusType) {
		return BonusProgrammeDetail{}, enumErr("bonusType", BonusTypes)
	}
	pp, err := validPeriod("payPeriod", in.PayPeriod)
	if err != nil {
		return BonusProgrammeDetail{}, err
	}
	if in.ReviewCycleID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.review_cycles WHERE id = $1 AND property_id = $2)`, *in.ReviewCycleID, property).
			Scan(&ok); err != nil {
			return BonusProgrammeDetail{}, err
		}
		if !ok {
			return BonusProgrammeDetail{}, handle.Invalid("reviewCycleId", "not_found", "no review cycle with this id")
		}
	}
	no, err := yearlyNumber(ctx, tx, property, "BNS", pp.Year())
	if err != nil {
		return BonusProgrammeDetail{}, err
	}
	bid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.bonus_programmes (id, property_id, number, name, bonus_type, pay_period, review_cycle_id, notes, created_by,
		updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)`, bid, property, no, strings.TrimSpace(in.Name), in.BonusType, pp.Format("2006-01"),
		in.ReviewCycleID, nullStr(in.Notes), actor(ctx)); err != nil {
		return BonusProgrammeDetail{}, err
	}
	out, err := m.Bonus(ctx, tx, bid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.bonus_programme", EntityID: bid.String(),
		EntityLabel: no + " · " + out.Name, PropertyID: &property, After: map[string]any{"name": out.Name, "bonusType": out.BonusType,
			"payPeriod": out.PayPeriod}})
}

// refreshBonus recomputes the programme totals.
func refreshBonus(ctx context.Context, tx pgx.Tx, bid uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE hris.bonus_programmes b SET employees = (SELECT count(*) FROM hris.bonus_lines WHERE programme_id = b.id),
		total = coalesce((SELECT sum(amount) FROM hris.bonus_lines WHERE programme_id = b.id), 0) WHERE id = $1`, bid)
	return err
}

func (m *Module) setBonusLinesHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, in BonusLinesInput) (BonusProgrammeDetail, error) {
	bid, err := handle.ID(r)
	if err != nil {
		return BonusProgrammeDetail{}, err
	}
	b, err := lockBonus(ctx, tx, bid)
	if err != nil {
		return BonusProgrammeDetail{}, err
	}
	if b.Status != "draft" {
		return BonusProgrammeDetail{}, errs.Conflict("bonus_not_draft", "only a draft programme can be edited (it is "+b.Status+")")
	}
	seen := map[uuid.UUID]bool{}
	type line struct {
		e    hris.Employee
		amt  decimal.Decimal
		note string
	}
	var lines []line
	for i, l := range in.Lines {
		field := fmt.Sprintf("lines[%d]", i)
		if seen[l.EmployeeID] {
			return BonusProgrammeDetail{}, handle.Invalid(field+".employeeId", "duplicate", "one line per employee")
		}
		seen[l.EmployeeID] = true
		e, err := hris.EmployeeByID(ctx, tx, l.EmployeeID)
		if err != nil || e.PropertyID != b.PropertyID {
			return BonusProgrammeDetail{}, handle.Invalid(field+".employeeId", "not_found", "no employee of the property with this id")
		}
		amt, err := handle.Decimal(field+".amount", l.Amount, decimal.Zero)
		if err != nil {
			return BonusProgrammeDetail{}, err
		}
		if !amt.IsPositive() {
			return BonusProgrammeDetail{}, handle.Invalid(field+".amount", "invalid", "a positive amount")
		}
		lines = append(lines, line{e, amt, l.Note})
	}
	before, err := m.Bonus(ctx, tx, bid)
	if err != nil {
		return before, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM hris.bonus_lines WHERE programme_id = $1`, bid); err != nil {
		return BonusProgrammeDetail{}, err
	}
	for _, l := range lines {
		if _, err := tx.Exec(ctx, `INSERT INTO hris.bonus_lines (id, programme_id, property_id, employee_id, employee_no, full_name, amount, basis, note)
			VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,'manual',$8)`, id.New(), bid, b.PropertyID, l.e.ID, l.e.EmployeeNo, l.e.FullName, l.amt.String(),
			nullStr(l.note)); err != nil {
			return BonusProgrammeDetail{}, err
		}
	}
	if err := refreshBonus(ctx, tx, bid); err != nil {
		return BonusProgrammeDetail{}, err
	}
	out, err := m.Bonus(ctx, tx, bid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionUpdate, EntityType: "hris.bonus_programme", EntityID: bid.String(),
		EntityLabel: b.Number, PropertyID: &b.PropertyID, Before: map[string]any{"employees": before.Employees, "total": before.Total},
		After: map[string]any{"employees": out.Employees, "total": out.Total}})
}

// generateBonusHTTP builds the lines from the completed reviews (of the
// programme's cycle, else each employee's latest result): bonus months
// recommended by the review × the fixed wage of the contract in force on the
// first day of the pay period (FR-PAY-05, FR-PRF-HR-04).
func (m *Module) generateBonusHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (BonusProgrammeDetail, error) {
	bid, err := handle.ID(r)
	if err != nil {
		return BonusProgrammeDetail{}, err
	}
	b, err := lockBonus(ctx, tx, bid)
	if err != nil {
		return BonusProgrammeDetail{}, err
	}
	if b.Status != "draft" {
		return BonusProgrammeDetail{}, errs.Conflict("bonus_not_draft", "only a draft programme can be generated (it is "+b.Status+")")
	}
	pp, _ := time.Parse("2006-01", b.PayPeriod)
	emps, err := hris.Employees(ctx, tx, hris.EmployeeFilter{PropertyID: b.PropertyID, ActiveOn: &pp})
	if err != nil {
		return BonusProgrammeDetail{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM hris.bonus_lines WHERE programme_id = $1`, bid); err != nil {
		return BonusProgrammeDetail{}, err
	}
	n := 0
	for _, e := range emps {
		var months decimal.Decimal
		var cycle string
		if b.ReviewCycleID != nil {
			var bm *string
			err := tx.QueryRow(ctx, `SELECT trim_scale(r.bonus_months)::text, c.code FROM hris.performance_reviews r JOIN hris.review_cycles c ON c.id = r.cycle_id
				WHERE r.employee_id = $1 AND r.cycle_id = $2 AND r.status = 'completed'`, e.ID, *b.ReviewCycleID).Scan(&bm, &cycle)
			if err != nil && !dbtx.IsNoRows(err) {
				return BonusProgrammeDetail{}, err
			}
			months = dec(deref(bm))
		} else {
			res, err := hris.LatestReviewResult(ctx, tx, e.ID)
			if err != nil {
				return BonusProgrammeDetail{}, err
			}
			if res != nil {
				months, cycle = dec(deref(res.BonusMonths)), res.CycleCode
			}
		}
		if !months.IsPositive() {
			continue
		}
		c, err := hris.ContractAt(ctx, tx, e.ID, pp)
		if err != nil {
			return BonusProgrammeDetail{}, err
		}
		if c == nil {
			continue
		}
		amt := months.Mul(c.FixedWage()).Round(0)
		if !amt.IsPositive() {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hris.bonus_lines (id, programme_id, property_id, employee_id, employee_no, full_name, amount, basis)
			VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8)`, id.New(), bid, b.PropertyID, e.ID, e.EmployeeNo, e.FullName, amt.String(),
			months.String()+" × fixed wage ("+cycle+")"); err != nil {
			return BonusProgrammeDetail{}, err
		}
		n++
	}
	if err := refreshBonus(ctx, tx, bid); err != nil {
		return BonusProgrammeDetail{}, err
	}
	out, err := m.Bonus(ctx, tx, bid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "generate", EntityType: "hris.bonus_programme", EntityID: bid.String(),
		EntityLabel: b.Number, PropertyID: &b.PropertyID, After: map[string]any{"employees": n, "total": out.Total}})
}

func (m *Module) decideBonusHTTP(approve bool) func(ctx context.Context, tx pgx.Tx, r *http.Request, in BonusAction) (BonusProgrammeDetail, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, in BonusAction) (BonusProgrammeDetail, error) {
		bid, err := handle.ID(r)
		if err != nil {
			return BonusProgrammeDetail{}, err
		}
		b, err := lockBonus(ctx, tx, bid)
		if err != nil {
			return BonusProgrammeDetail{}, err
		}
		switch {
		case b.Status == "pending_approval" && b.ApprovalRequestID != nil:
			if err := m.Approvals.Decide(ctx, tx, *b.ApprovalRequestID, approve, in.Reason); err != nil {
				return BonusProgrammeDetail{}, err
			}
		case b.Status == "draft" && approve:
			if b.Employees == 0 {
				return BonusProgrammeDetail{}, errs.Conflict("empty_programme", "add bonus lines first")
			}
			if _, err := tx.Exec(ctx, `UPDATE hris.bonus_programmes SET status = 'pending_approval', decision_note = NULL, updated_by = $2 WHERE id = $1`,
				bid, actor(ctx)); err != nil {
				return BonusProgrammeDetail{}, err
			}
			if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "submit_approval", EntityType: "hris.bonus_programme",
				EntityID: bid.String(), EntityLabel: b.Number, PropertyID: &b.PropertyID, Reason: in.Reason, Before: map[string]any{"status": b.Status},
				After: map[string]any{"status": "pending_approval"}}); err != nil {
				return BonusProgrammeDetail{}, err
			}
			aid, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: BonusDocumentType.Code, DocumentID: bid, DocumentRef: b.Number,
				Title: "Bonus " + b.Name + " · " + money(dec(b.Total)) + " to " + fmt.Sprint(b.Employees) + " employees", PropertyID: b.PropertyID,
				Attributes: map[string]any{"amount": dec(b.Total).InexactFloat64(), "employees": float64(b.Employees), "bonusType": b.BonusType}})
			if err != nil {
				return BonusProgrammeDetail{}, err
			}
			if _, err := tx.Exec(ctx, `UPDATE hris.bonus_programmes SET approval_request_id = $2 WHERE id = $1`, bid, aid); err != nil {
				return BonusProgrammeDetail{}, err
			}
		default:
			return BonusProgrammeDetail{}, errs.Conflict("bonus_not_submittable", "the programme is "+b.Status)
		}
		return m.Bonus(ctx, tx, bid)
	}
}

// BonusDecision applies the approval decision (approval engine hook).
func (m *Module) BonusDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	b, err := getOne[BonusProgramme]("bonus programme")(tx.Query(ctx, bonusSelect+` WHERE id = $1 FOR UPDATE`, d.DocumentID))
	if err != nil {
		if errs.Is(err, errs.KindNotFound) {
			return nil
		}
		return err
	}
	if b.Status != "pending_approval" {
		return nil
	}
	next := "draft"
	if d.Status == approval.StatusApproved {
		next = "approved"
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.bonus_programmes SET status = $2, decision_note = $3, approved_at = CASE WHEN $2 = 'approved' THEN now() END
		WHERE id = $1`, b.ID, next, nullStr(d.Reason)); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "approval_" + d.Status, EntityType: "hris.bonus_programme",
		EntityID: b.ID.String(), EntityLabel: b.Number, PropertyID: &b.PropertyID, Reason: d.Reason, Before: map[string]any{"status": b.Status},
		After: map[string]any{"status": next}}); err != nil {
		return err
	}
	var creator *uuid.UUID
	_ = tx.QueryRow(ctx, `SELECT created_by FROM hris.bonus_programmes WHERE id = $1`, b.ID).Scan(&creator)
	if creator == nil || d.Status == approval.StatusCancelled {
		return nil
	}
	decision := map[string]string{"approved": "approved", "draft": "rejected"}[next]
	return m.notifyUsers(ctx, tx, b.PropertyID, []uuid.UUID{*creator}, "hris.bonus_decided", "/hris/commissions/bonuses/"+b.ID.String(),
		map[string]any{"number": b.Number, "name": b.Name, "amount": money(dec(b.Total)), "employees": b.Employees, "payPeriod": b.PayPeriod,
			"decision": decision, "reason": d.Reason})
}

func (m *Module) cancelBonusHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, in BonusAction) (BonusProgrammeDetail, error) {
	bid, err := handle.ID(r)
	if err != nil {
		return BonusProgrammeDetail{}, err
	}
	b, err := lockBonus(ctx, tx, bid)
	if err != nil {
		return BonusProgrammeDetail{}, err
	}
	if !slices.Contains([]string{"draft", "pending_approval"}, b.Status) {
		return BonusProgrammeDetail{}, errs.Conflict("bonus_approved", "an approved programme cannot be cancelled")
	}
	if strings.TrimSpace(in.Reason) == "" {
		return BonusProgrammeDetail{}, handle.Invalid("reason", "required", "a reason is required")
	}
	if b.Status == "pending_approval" && b.ApprovalRequestID != nil {
		if _, err := tx.Exec(ctx, `UPDATE hris.bonus_programmes SET status = 'draft' WHERE id = $1`, bid); err != nil {
			return BonusProgrammeDetail{}, err
		}
		if err := m.Approvals.Cancel(ctx, tx, *b.ApprovalRequestID, "bonus programme cancelled"); err != nil {
			return BonusProgrammeDetail{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.bonus_programmes SET status = 'cancelled', cancelled_at = now(), cancel_reason = $2, updated_by = $3 WHERE id = $1`,
		bid, in.Reason, actor(ctx)); err != nil {
		return BonusProgrammeDetail{}, err
	}
	out, err := m.Bonus(ctx, tx, bid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "cancel", EntityType: "hris.bonus_programme", EntityID: bid.String(),
		EntityLabel: b.Number, PropertyID: &b.PropertyID, Reason: in.Reason, Before: map[string]any{"status": b.Status},
		After: map[string]any{"status": "cancelled"}})
}
