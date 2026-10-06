package payroll

// Loans and cash advances as an employee service (HRIS improvement phase B,
// spec §24, §27, §29, §30): requested by HR for an employee or by the
// employee in Employee Self Service, approved through the approval engine
// (document type hris.employee_loan; no workflow = approved at once), paid
// to the employee by Finance (hris.loan_disbursed: the loan becomes active
// and regular payroll runs deduct the installments) and repaid by payroll or
// outside it (hris.loan_repaid). The statement lists the payment, the
// payroll installments and the repayments with the outstanding balance.
// Loans entered directly through the resource (legacy, already paid) start
// active as before.

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

// Loan events (consumed by accounting: Dr / Cr employee receivables).
const (
	EventLoanDisbursed = "hris.loan_disbursed"
	EventLoanRepaid    = "hris.loan_repaid"
)

// EmployeeLoan is a loan or cash advance with its balance.
type EmployeeLoan struct {
	ID                 uuid.UUID  `json:"id" db:"id"`
	Number             *string    `json:"number" db:"number"`
	EmployeeID         uuid.UUID  `json:"employeeId" db:"employee_id"`
	EmployeeNo         string     `json:"employeeNo" db:"employee_no"`
	EmployeeName       string     `json:"employeeName" db:"employee_name"`
	OrgUnitName        *string    `json:"orgUnitName" db:"org_unit_name"`
	LoanType           string     `json:"loanType" db:"loan_type" enum:"loan,cash_advance"`
	Principal          string     `json:"principal" db:"principal"`
	Installment        string     `json:"installment" db:"installment"`
	StartPeriod        string     `json:"startPeriod" db:"start_period"`
	Repaid             string     `json:"repaid" db:"repaid"`
	Outstanding        string     `json:"outstanding" db:"outstanding"`
	Installments       int        `json:"installments" db:"installments" doc:"Remaining installments (tenor left)"`
	Purpose            *string    `json:"purpose" db:"purpose"`
	Reference          *string    `json:"reference" db:"reference"`
	RequestSource      string     `json:"requestSource" db:"request_source" enum:"hr,ess"`
	Status             string     `json:"status" db:"status" enum:"submitted,approved,rejected,active,settled,cancelled"`
	ApprovalRequestID  *uuid.UUID `json:"approvalRequestId" db:"approval_request_id"`
	DecidedAt          *time.Time `json:"decidedAt" db:"decided_at"`
	DecisionNote       *string    `json:"decisionNote" db:"decision_note"`
	DisbursedOn        *time.Time `json:"disbursedOn" db:"disbursed_on"`
	DisbursementMethod *string    `json:"disbursementMethod" db:"disbursement_method"`
	DisbursementRef    *string    `json:"disbursementRef" db:"disbursement_ref"`
	CreatedAt          time.Time  `json:"createdAt" db:"created_at"`
}

// LoanMovement is one line of a loan statement.
type LoanMovement struct {
	Date      time.Time `json:"date"`
	Kind      string    `json:"kind" enum:"disbursement,payroll,repayment"`
	Amount    string    `json:"amount"`
	Reference *string   `json:"reference"`
	Balance   string    `json:"balance" doc:"Outstanding after the movement"`
}

// LoanStatement is a loan with its movements.
type LoanStatement struct {
	Loan      EmployeeLoan   `json:"loan"`
	Movements []LoanMovement `json:"movements"`
}

// LoanRequestInput requests a loan or cash advance.
type LoanRequestInput struct {
	EmployeeID  uuid.UUID `json:"employeeId,omitempty" doc:"HR request: the employee (ESS: yourself)"`
	LoanType    string    `json:"loanType" enum:"loan,cash_advance"`
	Principal   string    `json:"principal"`
	Installment string    `json:"installment,omitempty" doc:"Per payroll period; default the principal (one deduction)"`
	StartPeriod string    `json:"startPeriod,omitempty" doc:"First deduction period YYYY-MM; default next month"`
	Purpose     string    `json:"purpose"`
}

// LoanDisburseInput records the payment of an approved loan by Finance.
type LoanDisburseInput struct {
	DisbursedOn     string `json:"disbursedOn"`
	Method          string `json:"method" enum:"cash,bank_transfer"`
	Reference       string `json:"reference,omitempty"`
	BankAccountCode string `json:"bankAccountCode,omitempty" doc:"GL cash / bank account code (empty: the default account of the method)"`
}

// LoanRepayInput records a repayment outside payroll.
type LoanRepayInput struct {
	PaidOn          string `json:"paidOn"`
	Amount          string `json:"amount"`
	Method          string `json:"method" enum:"cash,bank_transfer"`
	Reference       string `json:"reference,omitempty"`
	Notes           string `json:"notes,omitempty"`
	BankAccountCode string `json:"bankAccountCode,omitempty"`
}

// LoanCancelInput cancels a request not yet paid.
type LoanCancelInput struct {
	Note string `json:"note"`
}

// LoanMoney is the payload of hris.loan_disbursed / hris.loan_repaid.
type LoanMoney struct {
	LoanID          uuid.UUID `json:"loanId"`
	MovementID      uuid.UUID `json:"movementId"`
	Number          string    `json:"number"`
	LoanType        string    `json:"loanType"`
	EmployeeID      uuid.UUID `json:"employeeId"`
	EmployeeName    string    `json:"employeeName"`
	Date            string    `json:"date"`
	Amount          string    `json:"amount"`
	Method          string    `json:"method"`
	Reference       string    `json:"reference"`
	BankAccountCode string    `json:"bankAccountCode"`
}

const loanSelect = `SELECT l.id, l.number, l.employee_id, e.employee_no, e.full_name AS employee_name, ou.name AS org_unit_name, l.loan_type,
	trim_scale(l.principal)::text AS principal, trim_scale(l.installment)::text AS installment, l.start_period, trim_scale(l.repaid)::text AS repaid,
	trim_scale(l.principal - l.repaid)::text AS outstanding, ceil((l.principal - l.repaid) / l.installment)::int AS installments, l.purpose, l.reference,
	l.request_source, l.status, l.approval_request_id, l.decided_at, l.decision_note, l.disbursed_on, l.disbursement_method, l.disbursement_ref, l.created_at
	FROM hris.employee_loans l JOIN hris.employees e ON e.id = l.employee_id LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id`

var loanStatuses = []string{"submitted", "approved", "rejected", "active", "settled", "cancelled"}

func (m *Module) registerLoans(reg *route.Registry) {
	tag := "HRIS Payroll"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/loans", Summary: "Loans and cash advances with the outstanding balance",
		Permission: KeyEmployeeLoan + ".view", Response: EmployeeLoan{}, List: true, Query: []route.Param{{Name: "status", Enum: loanStatuses},
			{Name: "loanType", Enum: []string{"loan", "cash_advance"}}, {Name: "employeeId"}, {Name: "q"}}, Handler: listRead(m.DB, m.loansHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/employee-loans:request",
		Summary:    "Request a loan or cash advance for an employee (approval engine; approved at once without a workflow)",
		Permission: KeyEmployeeLoan + ".create", Request: LoanRequestInput{}, Response: EmployeeLoan{}, Idempotent: true,
		Handler: handle.Write(m.DB, http.StatusCreated, m.requestLoanHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/employee-loans/{id}/statement", Summary: "Loan statement: payment, installments, repayments",
		Permission: KeyEmployeeLoan + ".view", Response: LoanStatement{}, Handler: handle.Read(m.DB, m.loanStatementHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/employee-loans/{id}:disburse",
		Summary: "Record the payment of an approved loan / cash advance by Finance (active; payroll deducts the installments)", Permission: PermLoanPay,
		Request: LoanDisburseInput{}, Response: EmployeeLoan{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.disburseLoanHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/employee-loans/{id}:repay",
		Summary: "Record a repayment outside payroll (cash returned, transfer)", Permission: PermLoanPay, Request: LoanRepayInput{},
		Response: EmployeeLoan{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.repayLoanHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/employee-loans/{id}:cancel",
		Summary: "Cancel a loan request not yet paid (a pending approval is withdrawn by its requester)", Permission: KeyEmployeeLoan + ".update",
		Request: LoanCancelInput{}, Response: EmployeeLoan{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.cancelLoanHTTP(false))})
	ess := "Employee Self Service"
	add(reg, ess, route.Route{Method: http.MethodGet, Path: "/api/v1/ess/loans", Summary: "My loans and cash advances", Permission: hris.PermissionESS,
		Response: EmployeeLoan{}, List: true, Handler: listRead(m.DB, m.myLoansHTTP)})
	add(reg, ess, route.Route{Method: http.MethodGet, Path: "/api/v1/ess/loans/{id}/statement", Summary: "My loan statement", Permission: hris.PermissionESS,
		Response: LoanStatement{}, Handler: handle.Read(m.DB, m.myLoanStatementHTTP)})
	add(reg, ess, route.Route{Method: http.MethodPost, Path: "/api/v1/ess/loans", Summary: "Request a loan or cash advance", Permission: hris.PermissionESS,
		Request: LoanRequestInput{}, Response: EmployeeLoan{}, Idempotent: true, Handler: handle.Write(m.DB, http.StatusCreated, m.myLoanRequestHTTP)})
	add(reg, ess, route.Route{Method: http.MethodPost, Path: "/api/v1/ess/loans/{id}:cancel", Summary: "Withdraw my loan request",
		Permission: hris.PermissionESS, Request: LoanCancelInput{}, Response: EmployeeLoan{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.cancelLoanHTTP(true))})
}

func (m *Module) loansHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]EmployeeLoan, error) {
	emp, err := uuidParam(r, "employeeId")
	if err != nil {
		return nil, err
	}
	q := filterParam(r, "q")
	return handle.List[EmployeeLoan](tx.Query(ctx, loanSelect+` WHERE l.property_id = $1 AND l.archived_at IS NULL AND ($2 = '' OR l.status = $2)
		AND ($3 = '' OR l.loan_type = $3) AND ($4::uuid IS NULL OR l.employee_id = $4)
		AND ($5 = '' OR e.full_name ILIKE '%' || $5 || '%' OR e.employee_no ILIKE '%' || $5 || '%' OR l.number ILIKE '%' || $5 || '%')
		ORDER BY CASE l.status WHEN 'submitted' THEN 0 WHEN 'approved' THEN 1 WHEN 'active' THEN 2 ELSE 3 END, l.created_at DESC LIMIT 500`,
		handle.Property(ctx), filterParam(r, "status"), filterParam(r, "loanType"), emp, q))
}

func (m *Module) loan(ctx context.Context, tx pgx.Tx, lid uuid.UUID) (EmployeeLoan, error) {
	rows, err := tx.Query(ctx, loanSelect+` WHERE l.id = $1`, lid)
	return handle.One[EmployeeLoan](rows, err, "loan")
}

// requestLoan stores and submits a request.
func (m *Module) requestLoan(ctx context.Context, tx pgx.Tx, property uuid.UUID, e hris.Employee, req LoanRequestInput, source string) (uuid.UUID, error) {
	typ := req.LoanType
	if typ == "" {
		typ = "loan"
	}
	if !oneOf([]string{"loan", "cash_advance"}, typ) {
		return uuid.Nil, enumErr("loanType", []string{"loan", "cash_advance"})
	}
	principal, err := handle.Decimal("principal", req.Principal, dec("0"))
	if err != nil {
		return uuid.Nil, err
	}
	if !principal.IsPositive() {
		return uuid.Nil, handle.Invalid("principal", "invalid", "a positive amount")
	}
	installment := principal
	if strings.TrimSpace(req.Installment) != "" {
		if installment, err = handle.Decimal("installment", req.Installment, dec("0")); err != nil {
			return uuid.Nil, err
		}
	}
	if !installment.IsPositive() || installment.GreaterThan(principal) {
		return uuid.Nil, handle.Invalid("installment", "invalid", "a positive amount up to the principal")
	}
	day := today(ctx, tx, property)
	start := time.Date(day.Year(), day.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	if strings.TrimSpace(req.StartPeriod) != "" {
		if start, err = parsePeriod("startPeriod", req.StartPeriod); err != nil {
			return uuid.Nil, err
		}
	}
	if strings.TrimSpace(req.Purpose) == "" {
		return uuid.Nil, handle.Invalid("purpose", "required", "is required")
	}
	if e.Status != "active" || (e.TerminationDate != nil && !e.TerminationDate.After(day)) {
		return uuid.Nil, handle.Invalid("employeeId", "invalid", "only an active employee can receive a loan")
	}
	prefix := "LOAN"
	if typ == "cash_advance" {
		prefix = "CADV"
	}
	number, err := yearlyNumber(ctx, tx, property, prefix, day.Year())
	if err != nil {
		return uuid.Nil, err
	}
	lid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.employee_loans (id, property_id, employee_id, loan_type, principal, installment, start_period, number,
		purpose, request_source, status, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'submitted',$11,$11)`,
		lid, property, e.ID, typ, principal, installment, periodCode(start), number, strings.TrimSpace(req.Purpose), source, actor(ctx)); err != nil {
		return uuid.Nil, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.employee_loan", EntityID: lid.String(),
		EntityLabel: number + " · " + e.FullName, PropertyID: &property, After: map[string]any{"loanType": typ, "principal": principal.String(),
			"installment": installment.String(), "startPeriod": periodCode(start), "source": source, "status": "submitted"}}); err != nil {
		return uuid.Nil, err
	}
	what := map[string]string{"loan": "Loan", "cash_advance": "Cash advance"}[typ]
	rid, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: LoanDocumentType.Code, DocumentID: lid, DocumentRef: number,
		Title: what + " " + number + " · " + e.FullName, PropertyID: property,
		Attributes: map[string]any{"amount": floatOf(principal.String()), "loanType": typ, "source": source}})
	if err != nil {
		return uuid.Nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE hris.employee_loans SET approval_request_id = $2 WHERE id = $1 AND approval_request_id IS NULL`, lid, rid)
	return lid, err
}

func (m *Module) requestLoanHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request, req LoanRequestInput) (EmployeeLoan, error) {
	property := handle.Property(ctx)
	e, err := hris.EmployeeByID(ctx, tx, req.EmployeeID)
	if err != nil || e.PropertyID != property {
		return EmployeeLoan{}, handle.Invalid("employeeId", "not_found", "employee not found")
	}
	lid, err := m.requestLoan(ctx, tx, property, e, req, "hr")
	if err != nil {
		return EmployeeLoan{}, err
	}
	return m.loan(ctx, tx, lid)
}

func (m *Module) myLoanRequestHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request, req LoanRequestInput) (EmployeeLoan, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return EmployeeLoan{}, err
	}
	lid, err := m.requestLoan(ctx, tx, e.PropertyID, e, req, "ess")
	if err != nil {
		return EmployeeLoan{}, err
	}
	return m.loan(ctx, tx, lid)
}

// LoanDecision applies the approval decision (approval engine hook).
func (m *Module) LoanDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	var status, number, typ, principal string
	var creator *uuid.UUID
	err := tx.QueryRow(ctx, `SELECT status, coalesce(number, ''), loan_type, trim_scale(principal)::text, created_by FROM hris.employee_loans
		WHERE id = $1 FOR UPDATE`, d.DocumentID).Scan(&status, &number, &typ, &principal, &creator)
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
	if _, err := tx.Exec(ctx, `UPDATE hris.employee_loans SET status = $2, decided_at = now(), decision_note = $3 WHERE id = $1`, d.DocumentID, next,
		nullStr(d.Reason)); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "approval_" + d.Status, EntityType: "hris.employee_loan",
		EntityID: d.DocumentID.String(), EntityLabel: number, PropertyID: &d.PropertyID, Reason: d.Reason, Before: map[string]any{"status": status},
		After: map[string]any{"status": next}}); err != nil {
		return err
	}
	if next == "cancelled" {
		return nil
	}
	data := map[string]any{"number": number, "loanType": map[string]string{"loan": "loan", "cash_advance": "cash advance"}[typ], "amount": principal,
		"decision": next, "reason": d.Reason}
	if creator != nil {
		if err := m.notifyUsers(ctx, tx, d.PropertyID, []uuid.UUID{*creator}, NotifyLoanDecided, "/hris/loans", data); err != nil {
			return err
		}
	}
	if next == "approved" {
		return m.notifyUsers(ctx, tx, d.PropertyID, holders(ctx, tx, d.PropertyID, PermLoanPay), NotifyLoanToPay, "/hris/loans", data)
	}
	return nil
}

// lockedLoan reads the state of a loan of the property for an action.
type lockedLoan struct {
	status, number, typ string
	employee            uuid.UUID
	principal, repaid   decimal.Decimal
	approval            *uuid.UUID
	createdBy           *uuid.UUID
}

func lockLoan(ctx context.Context, tx pgx.Tx, lid, property uuid.UUID) (lockedLoan, error) {
	var l lockedLoan
	err := tx.QueryRow(ctx, `SELECT status, coalesce(number, ''), loan_type, employee_id, principal, repaid, approval_request_id, created_by
		FROM hris.employee_loans WHERE id = $1 AND property_id = $2 AND archived_at IS NULL FOR UPDATE`, lid, property).
		Scan(&l.status, &l.number, &l.typ, &l.employee, &l.principal, &l.repaid, &l.approval, &l.createdBy)
	if dbtx.IsNoRows(err) {
		return l, errs.NotFound("loan")
	}
	return l, err
}

func methodOK(field, v string) error {
	if !oneOf([]string{"cash", "bank_transfer"}, v) {
		return enumErr(field, []string{"cash", "bank_transfer"})
	}
	return nil
}

func (m *Module) disburseLoanHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req LoanDisburseInput) (EmployeeLoan, error) {
	property := handle.Property(ctx)
	lid, err := handle.ID(r)
	if err != nil {
		return EmployeeLoan{}, err
	}
	l, err := lockLoan(ctx, tx, lid, property)
	if err != nil {
		return EmployeeLoan{}, err
	}
	if l.status != "approved" {
		return EmployeeLoan{}, errs.Conflict("loan_not_approved", "only an approved loan request can be paid")
	}
	on, err := parseDate("disbursedOn", req.DisbursedOn)
	if err != nil {
		return EmployeeLoan{}, err
	}
	if on == nil {
		return EmployeeLoan{}, handle.Invalid("disbursedOn", "required", "is required")
	}
	if err := methodOK("method", req.Method); err != nil {
		return EmployeeLoan{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.employee_loans SET status = 'active', disbursed_on = $2, disbursement_method = $3, disbursement_ref = $4,
		disbursed_by = $5, updated_by = $5 WHERE id = $1`, lid, ymd(*on), req.Method, nullStr(req.Reference), actor(ctx)); err != nil {
		return EmployeeLoan{}, err
	}
	out, err := m.loan(ctx, tx, lid)
	if err != nil {
		return out, err
	}
	if err := m.publishLoanMoney(ctx, tx, EventLoanDisbursed, property, out, lid, *on, l.principal, req.Method, req.Reference, req.BankAccountCode); err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "disburse", EntityType: "hris.employee_loan", EntityID: lid.String(),
		EntityLabel: l.number + " · " + out.EmployeeName, PropertyID: &property, Before: map[string]any{"status": l.status},
		After: map[string]any{"status": "active", "disbursedOn": ymd(*on), "method": req.Method, "reference": req.Reference}})
}

func (m *Module) repayLoanHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req LoanRepayInput) (EmployeeLoan, error) {
	property := handle.Property(ctx)
	lid, err := handle.ID(r)
	if err != nil {
		return EmployeeLoan{}, err
	}
	l, err := lockLoan(ctx, tx, lid, property)
	if err != nil {
		return EmployeeLoan{}, err
	}
	if l.status != "active" {
		return EmployeeLoan{}, errs.Conflict("loan_not_active", "only an active loan can be repaid")
	}
	on, err := parseDate("paidOn", req.PaidOn)
	if err != nil {
		return EmployeeLoan{}, err
	}
	if on == nil {
		return EmployeeLoan{}, handle.Invalid("paidOn", "required", "is required")
	}
	if err := methodOK("method", req.Method); err != nil {
		return EmployeeLoan{}, err
	}
	amount, err := handle.Decimal("amount", req.Amount, dec("0"))
	if err != nil {
		return EmployeeLoan{}, err
	}
	outstanding := l.principal.Sub(l.repaid)
	if !amount.IsPositive() || amount.GreaterThan(outstanding) {
		return EmployeeLoan{}, handle.Invalid("amount", "invalid", "a positive amount up to the outstanding "+outstanding.StringFixed(2))
	}
	rid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.loan_repayments (id, property_id, loan_id, paid_on, amount, method, reference, notes, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, rid, property, lid, ymd(*on), amount, req.Method, nullStr(req.Reference), nullStr(req.Notes), actor(ctx)); err != nil {
		return EmployeeLoan{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.employee_loans SET repaid = repaid + $2, status = CASE WHEN repaid + $2 >= principal THEN 'settled' ELSE status END,
		updated_by = $3 WHERE id = $1`, lid, amount, actor(ctx)); err != nil {
		return EmployeeLoan{}, err
	}
	out, err := m.loan(ctx, tx, lid)
	if err != nil {
		return out, err
	}
	if err := m.publishLoanMoney(ctx, tx, EventLoanRepaid, property, out, rid, *on, amount, req.Method, req.Reference, req.BankAccountCode); err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "repay", EntityType: "hris.employee_loan", EntityID: lid.String(),
		EntityLabel: l.number + " · " + out.EmployeeName, PropertyID: &property, Before: map[string]any{"repaid": l.repaid.String(), "status": l.status},
		After: map[string]any{"repaid": out.Repaid, "status": out.Status, "amount": amount.String(), "method": req.Method, "paidOn": ymd(*on)}})
}

func (m *Module) publishLoanMoney(ctx context.Context, tx pgx.Tx, event string, property uuid.UUID, l EmployeeLoan, movement uuid.UUID, on time.Time,
	amount decimal.Decimal, method, reference, bank string) error {
	if m.Events == nil {
		return nil
	}
	_, err := m.Events.Publish(ctx, tx, event, "hris.employee_loan", &l.ID, &property, LoanMoney{LoanID: l.ID, MovementID: movement, Number: deref(l.Number),
		LoanType: l.LoanType, EmployeeID: l.EmployeeID, EmployeeName: l.EmployeeName, Date: ymd(on), Amount: money(amount), Method: method,
		Reference: strings.TrimSpace(reference), BankAccountCode: strings.TrimSpace(bank)})
	return err
}

// cancelLoanHTTP cancels a request not yet paid: HR (approved, or
// submitted when HR requested it) or the employee (own submitted request).
func (m *Module) cancelLoanHTTP(self bool) func(ctx context.Context, tx pgx.Tx, r *http.Request, req LoanCancelInput) (EmployeeLoan, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, req LoanCancelInput) (EmployeeLoan, error) {
		lid, err := handle.ID(r)
		if err != nil {
			return EmployeeLoan{}, err
		}
		property := handle.Property(ctx)
		if self {
			e, err := me(ctx, tx)
			if err != nil {
				return EmployeeLoan{}, err
			}
			property = e.PropertyID
			var owner uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT employee_id FROM hris.employee_loans WHERE id = $1`, lid).Scan(&owner); err != nil || owner != e.ID {
				return EmployeeLoan{}, errs.NotFound("loan")
			}
		}
		l, err := lockLoan(ctx, tx, lid, property)
		if err != nil {
			return EmployeeLoan{}, err
		}
		if strings.TrimSpace(req.Note) == "" {
			return EmployeeLoan{}, handle.Invalid("note", "required", "explain why the request is cancelled")
		}
		switch {
		case l.status == "submitted" && l.approval != nil:
			// the approval engine withdraws the request (requester only) and the hook cancels the loan
			if err := m.Approvals.Cancel(ctx, tx, *l.approval, req.Note); err != nil {
				return EmployeeLoan{}, err
			}
		case l.status == "approved" && !self:
			if _, err := tx.Exec(ctx, `UPDATE hris.employee_loans SET status = 'cancelled', decision_note = $2, updated_by = $3 WHERE id = $1`, lid,
				strings.TrimSpace(req.Note), actor(ctx)); err != nil {
				return EmployeeLoan{}, err
			}
		default:
			return EmployeeLoan{}, errs.Conflict("loan_not_cancellable", "only a request not yet paid can be cancelled")
		}
		out, err := m.loan(ctx, tx, lid)
		if err != nil {
			return out, err
		}
		return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "cancel", EntityType: "hris.employee_loan", EntityID: lid.String(),
			EntityLabel: l.number + " · " + out.EmployeeName, PropertyID: &property, Reason: req.Note, Before: map[string]any{"status": l.status},
			After: map[string]any{"status": out.Status}})
	}
}

func (m *Module) myLoansHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request) ([]EmployeeLoan, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return nil, err
	}
	return handle.List[EmployeeLoan](tx.Query(ctx, loanSelect+` WHERE l.employee_id = $1 AND l.archived_at IS NULL ORDER BY l.created_at DESC LIMIT 100`, e.ID))
}

func (m *Module) loanStatementHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (LoanStatement, error) {
	lid, err := handle.ID(r)
	if err != nil {
		return LoanStatement{}, err
	}
	l, err := m.loan(ctx, tx, lid)
	if err != nil {
		return LoanStatement{}, err
	}
	var prop uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id FROM hris.employee_loans WHERE id = $1`, lid).Scan(&prop); err != nil || prop != handle.Property(ctx) {
		return LoanStatement{}, errs.NotFound("loan")
	}
	return m.statement(ctx, tx, l)
}

func (m *Module) myLoanStatementHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (LoanStatement, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return LoanStatement{}, err
	}
	lid, err := handle.ID(r)
	if err != nil {
		return LoanStatement{}, err
	}
	l, err := m.loan(ctx, tx, lid)
	if err != nil || l.EmployeeID != e.ID {
		return LoanStatement{}, errs.NotFound("loan")
	}
	return m.statement(ctx, tx, l)
}

// statement lists the payment, the installments of posted payroll runs and
// the repayments, oldest first, with the running balance.
func (m *Module) statement(ctx context.Context, tx pgx.Tx, l EmployeeLoan) (LoanStatement, error) {
	out := LoanStatement{Loan: l, Movements: []LoanMovement{}}
	rows, err := tx.Query(ctx, `SELECT d, kind, amount, ref FROM (
		SELECT disbursed_on AS d, 'disbursement' AS kind, principal AS amount, disbursement_ref AS ref, 0 AS o FROM hris.employee_loans
		  WHERE id = $1 AND disbursed_on IS NOT NULL
		UNION ALL
		SELECT r.period_end, 'payroll', pl.amount, r.number, 1 FROM hris.payroll_lines pl JOIN hris.payroll_runs r ON r.id = pl.run_id
		  WHERE pl.source = 'loan' AND pl.source_id = $1::text AND r.status IN ('posted', 'paid')
		UNION ALL
		SELECT paid_on, 'repayment', amount, reference, 2 FROM hris.loan_repayments WHERE loan_id = $1) x ORDER BY d, o`, l.ID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	bal := decimal.Zero
	if l.DisbursedOn == nil { // entered directly (already paid): the balance starts at the principal
		bal = dec(l.Principal)
	}
	for rows.Next() {
		var mv LoanMovement
		var amt decimal.Decimal
		if err := rows.Scan(&mv.Date, &mv.Kind, &amt, &mv.Reference); err != nil {
			return out, err
		}
		if mv.Kind == "disbursement" {
			bal = bal.Add(amt)
		} else {
			bal = bal.Sub(amt)
		}
		mv.Amount, mv.Balance = money(amt), money(bal)
		out.Movements = append(out.Movements, mv)
	}
	return out, rows.Err()
}
