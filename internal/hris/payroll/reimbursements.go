package payroll

// Reimbursement (HRIS improvement phase C, spec §24 and §30): the employee
// (Employee Self Service) or HR on their behalf claims an expense of a
// category with its receipt; the approval engine decides (document type
// hris.reimbursement; no workflow = approved at once); HR sends approved
// claims to Finance, which pays them (hris.reimbursement_paid: Dr the
// expense account of the category / Cr cash or bank). HRIS keeps the claim
// and its status, Finance the settlement.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/storage"
)

// EventReimbursementPaid is consumed by accounting.
const EventReimbursementPaid = "hris.reimbursement_paid"

// Reimbursement is a claim.
type Reimbursement struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	Number            string     `json:"number" db:"number"`
	EmployeeID        uuid.UUID  `json:"employeeId" db:"employee_id"`
	EmployeeNo        string     `json:"employeeNo" db:"employee_no"`
	EmployeeName      string     `json:"employeeName" db:"employee_name"`
	OrgUnitName       *string    `json:"orgUnitName" db:"org_unit_name"`
	CategoryID        uuid.UUID  `json:"categoryId" db:"category_id"`
	CategoryCode      string     `json:"categoryCode" db:"category_code"`
	CategoryName      string     `json:"categoryName" db:"category_name"`
	ExpenseDate       time.Time  `json:"expenseDate" db:"expense_date"`
	Amount            string     `json:"amount" db:"amount"`
	Description       string     `json:"description" db:"description"`
	FileID            *uuid.UUID `json:"fileId" db:"file_id"`
	FileName          *string    `json:"fileName" db:"file_name"`
	RequestSource     string     `json:"requestSource" db:"request_source" enum:"hr,ess"`
	Status            string     `json:"status" db:"status" enum:"submitted,approved,rejected,sent_to_finance,paid,cancelled"`
	ApprovalRequestID *uuid.UUID `json:"approvalRequestId" db:"approval_request_id"`
	DecidedAt         *time.Time `json:"decidedAt" db:"decided_at"`
	DecisionNote      *string    `json:"decisionNote" db:"decision_note"`
	SentAt            *time.Time `json:"sentAt" db:"sent_at"`
	PaidOn            *time.Time `json:"paidOn" db:"paid_on"`
	PaymentMethod     *string    `json:"paymentMethod" db:"payment_method"`
	PaymentRef        *string    `json:"paymentRef" db:"payment_ref"`
	CreatedAt         time.Time  `json:"createdAt" db:"created_at"`
}

// ReimbursementInput claims an expense.
type ReimbursementInput struct {
	EmployeeID  uuid.UUID  `json:"employeeId,omitempty" doc:"HR claim: the employee (ESS: yourself)"`
	CategoryID  uuid.UUID  `json:"categoryId"`
	ExpenseDate string     `json:"expenseDate"`
	Amount      string     `json:"amount"`
	Description string     `json:"description"`
	FileID      *uuid.UUID `json:"fileId,omitempty" doc:"Receipt uploaded with POST …/reimbursement-files"`
}

// ReimbursementPayInput records the payment by Finance.
type ReimbursementPayInput struct {
	PaidOn          string `json:"paidOn"`
	Method          string `json:"method" enum:"cash,bank_transfer"`
	Reference       string `json:"reference,omitempty"`
	BankAccountCode string `json:"bankAccountCode,omitempty" doc:"GL cash / bank account code (empty: the default account of the method)"`
}

// ReimbursementNote carries a note (send to Finance, cancel).
type ReimbursementNote struct {
	Note string `json:"note,omitempty"`
}

// ReimbursementPaid is the payload of hris.reimbursement_paid.
type ReimbursementPaid struct {
	ReimbursementID    uuid.UUID  `json:"reimbursementId"`
	Number             string     `json:"number"`
	EmployeeID         uuid.UUID  `json:"employeeId"`
	EmployeeName       string     `json:"employeeName"`
	CategoryCode       string     `json:"categoryCode"`
	ExpenseAccountCode string     `json:"expenseAccountCode"`
	CostCenter         string     `json:"costCenter"`
	OrgUnitID          *uuid.UUID `json:"orgUnitId"`
	PaidOn             string     `json:"paidOn"`
	Amount             string     `json:"amount"`
	Method             string     `json:"method"`
	Reference          string     `json:"reference"`
	BankAccountCode    string     `json:"bankAccountCode"`
}

const reimbSelect = `SELECT r.id, r.number, r.employee_id, e.employee_no, e.full_name AS employee_name, ou.name AS org_unit_name, r.category_id,
	c.code AS category_code, c.name AS category_name, r.expense_date, trim_scale(r.amount)::text AS amount, r.description, r.file_id, f.filename AS file_name,
	r.request_source, r.status, r.approval_request_id, r.decided_at, r.decision_note, r.sent_at, r.paid_on, r.payment_method, r.payment_ref, r.created_at
	FROM hris.reimbursements r JOIN hris.employees e ON e.id = r.employee_id JOIN hris.reimbursement_categories c ON c.id = r.category_id
	LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id LEFT JOIN platform.files f ON f.id = r.file_id`

var reimbStatuses = []string{"submitted", "approved", "rejected", "sent_to_finance", "paid", "cancelled"}

const (
	receiptMaxBytes = 10 << 20
)

var receiptTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true, "application/pdf": true}

func (m *Module) registerReimbursements(reg *route.Registry) {
	tag := "HRIS Reimbursement"
	base := "/api/v1/hris/reimbursements"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base, Summary: "Reimbursement claims", Permission: PermReimbView, Response: Reimbursement{},
		List: true, Query: []route.Param{{Name: "status", Enum: reimbStatuses}, {Name: "employeeId"}, {Name: "categoryId"}, {Name: "q"}},
		Handler: listRead(m.DB, m.reimbursementsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/reimbursement-files",
		Summary: "Upload a receipt (multipart: file; JPEG, PNG, WebP or PDF up to 10 MB)", Permission: PermReimbManage, Response: storage.File{},
		Handler: m.receiptUploadHTTP})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base, Summary: "Claim an expense for an employee (approval engine)", Permission: PermReimbManage,
		Request: ReimbursementInput{}, Response: Reimbursement{}, Idempotent: true, Handler: handle.Write(m.DB, http.StatusCreated, m.createReimbHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/{id}/receipt", Summary: "Download the receipt of a claim", Permission: PermReimbView,
		RawContent: "application/octet-stream", Handler: m.receiptHTTP(false)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:send-to-finance", Summary: "Send an approved claim to Finance for payment",
		Permission: PermReimbManage, Request: ReimbursementNote{}, Response: Reimbursement{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.sendReimbHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:mark-paid", Summary: "Record the payment of a claim (Finance; journal)",
		Permission: PermReimbPay, Request: ReimbursementPayInput{}, Response: Reimbursement{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.payReimbHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:cancel", Summary: "Cancel a claim not yet sent to Finance",
		Permission: PermReimbManage, Request: ReimbursementNote{}, Response: Reimbursement{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.cancelReimbHTTP(false))})
	ess := "Employee Self Service"
	add(reg, ess, route.Route{Method: http.MethodGet, Path: "/api/v1/ess/reimbursements", Summary: "My reimbursement claims", Permission: hris.PermissionESS,
		Response: Reimbursement{}, List: true, Handler: listRead(m.DB, m.myReimbHTTP)})
	add(reg, ess, route.Route{Method: http.MethodPost, Path: "/api/v1/ess/reimbursement-files", Summary: "Upload my receipt (multipart: file)",
		Permission: hris.PermissionESS, Response: storage.File{}, Handler: m.receiptUploadHTTP})
	add(reg, ess, route.Route{Method: http.MethodPost, Path: "/api/v1/ess/reimbursements", Summary: "Claim an expense", Permission: hris.PermissionESS,
		Request: ReimbursementInput{}, Response: Reimbursement{}, Idempotent: true, Handler: handle.Write(m.DB, http.StatusCreated, m.myReimbCreateHTTP)})
	add(reg, ess, route.Route{Method: http.MethodGet, Path: "/api/v1/ess/reimbursements/{id}/receipt", Summary: "My receipt", Permission: hris.PermissionESS,
		RawContent: "application/octet-stream", Handler: m.receiptHTTP(true)})
	add(reg, ess, route.Route{Method: http.MethodPost, Path: "/api/v1/ess/reimbursements/{id}:cancel", Summary: "Withdraw my claim",
		Permission: hris.PermissionESS, Request: ReimbursementNote{}, Response: Reimbursement{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.cancelReimbHTTP(true))})
}

func (m *Module) reimbursementsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]Reimbursement, error) {
	emp, err := uuidParam(r, "employeeId")
	if err != nil {
		return nil, err
	}
	cat, err := uuidParam(r, "categoryId")
	if err != nil {
		return nil, err
	}
	return handle.List[Reimbursement](tx.Query(ctx, reimbSelect+` WHERE r.property_id = $1 AND ($2 = '' OR r.status = $2)
		AND ($3::uuid IS NULL OR r.employee_id = $3) AND ($4::uuid IS NULL OR r.category_id = $4)
		AND ($5 = '' OR e.full_name ILIKE '%' || $5 || '%' OR r.number ILIKE '%' || $5 || '%')
		ORDER BY CASE r.status WHEN 'submitted' THEN 0 WHEN 'approved' THEN 1 WHEN 'sent_to_finance' THEN 2 ELSE 3 END, r.created_at DESC LIMIT 500`,
		handle.Property(ctx), filterParam(r, "status"), emp, cat, filterParam(r, "q")))
}

func (m *Module) reimbursement(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (Reimbursement, error) {
	rows, err := tx.Query(ctx, reimbSelect+` WHERE r.id = $1`, rid)
	return handle.One[Reimbursement](rows, err, "reimbursement")
}

// receiptUploadHTTP stores a receipt (HR and ESS).
func (m *Module) receiptUploadHTTP(w http.ResponseWriter, r *http.Request) {
	if m.Files == nil {
		httpx.WriteError(w, r, errs.BadRequest("uploads_unavailable", "file storage is not configured"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, receiptMaxBytes+256<<10)
	if err := r.ParseMultipartForm(receiptMaxBytes); err != nil { //nolint:gosec // G120: body capped by MaxBytesReader above
		httpx.WriteError(w, r, errs.BadRequest("invalid_upload", "upload must be multipart/form-data up to 10 MB"))
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		httpx.WriteError(w, r, errs.Validation("file_required", "file is required", errs.Field("file", "required", "choose a photo or PDF")))
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, receiptMaxBytes+1))
	if err != nil || len(data) == 0 || len(data) > receiptMaxBytes {
		httpx.WriteError(w, r, errs.Validation("file_size", "file too large or empty", errs.Field("file", "size", "up to 10 MB")))
		return
	}
	ctype := http.DetectContentType(data)
	if i := strings.IndexByte(ctype, ';'); i >= 0 {
		ctype = ctype[:i]
	}
	if !receiptTypes[ctype] {
		httpx.WriteError(w, r, errs.Validation("file_type", "unsupported file type", errs.Field("file", "type", "JPEG, PNG, WebP or PDF")))
		return
	}
	name := strings.TrimSpace(hdr.Filename)
	if name == "" {
		name = "receipt"
	}
	ctx := r.Context()
	var out storage.File
	err = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		if out, err = m.Files.Save(ctx, tx, name, ctype, "attachment", false, bytes.NewReader(data), int64(len(data))); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "platform.file", EntityID: out.ID.String(),
			EntityLabel: "Receipt " + name, After: out})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

// createReimb validates, stores and submits a claim.
func (m *Module) createReimb(ctx context.Context, tx pgx.Tx, property uuid.UUID, e hris.Employee, req ReimbursementInput, source string) (uuid.UUID, error) {
	var code, name string
	var maxAmount *decimal.Decimal
	var receipt bool
	err := tx.QueryRow(ctx, `SELECT code, name, max_amount, receipt_required FROM hris.reimbursement_categories WHERE id = $1 AND property_id = $2
		AND status = 'active' AND archived_at IS NULL`, req.CategoryID, property).Scan(&code, &name, &maxAmount, &receipt)
	if dbtx.IsNoRows(err) {
		return uuid.Nil, handle.Invalid("categoryId", "not_found", "reimbursement category not found")
	}
	if err != nil {
		return uuid.Nil, err
	}
	day := today(ctx, tx, property)
	on, err := parseDate("expenseDate", req.ExpenseDate)
	if err != nil {
		return uuid.Nil, err
	}
	if on == nil || on.After(day) {
		return uuid.Nil, handle.Invalid("expenseDate", "invalid", "the date of the expense, not in the future")
	}
	amount, err := handle.Decimal("amount", req.Amount, dec("0"))
	if err != nil {
		return uuid.Nil, err
	}
	if !amount.IsPositive() {
		return uuid.Nil, handle.Invalid("amount", "invalid", "a positive amount")
	}
	if maxAmount != nil && amount.GreaterThan(*maxAmount) {
		return uuid.Nil, handle.Invalid("amount", "over_limit", "at most "+maxAmount.StringFixed(0)+" per claim for "+name)
	}
	if strings.TrimSpace(req.Description) == "" {
		return uuid.Nil, handle.Invalid("description", "required", "is required")
	}
	if receipt && req.FileID == nil {
		return uuid.Nil, handle.Invalid("fileId", "required", "attach the receipt")
	}
	if req.FileID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.files WHERE id = $1)`, *req.FileID).Scan(&ok); err != nil {
			return uuid.Nil, err
		}
		if !ok {
			return uuid.Nil, handle.Invalid("fileId", "not_found", "file not found")
		}
	}
	if e.Status != "active" {
		return uuid.Nil, handle.Invalid("employeeId", "invalid", "only an active employee can claim")
	}
	number, err := yearlyNumber(ctx, tx, property, "RMB", day.Year())
	if err != nil {
		return uuid.Nil, err
	}
	rid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.reimbursements (id, property_id, number, employee_id, category_id, expense_date, amount, description, file_id,
		request_source, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)`, rid, property, number, e.ID, req.CategoryID, ymd(*on), amount,
		strings.TrimSpace(req.Description), req.FileID, source, actor(ctx)); err != nil {
		return uuid.Nil, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.reimbursement", EntityID: rid.String(),
		EntityLabel: number + " · " + e.FullName, PropertyID: &property, After: map[string]any{"category": code, "amount": amount.String(),
			"expenseDate": ymd(*on), "source": source}}); err != nil {
		return uuid.Nil, err
	}
	aid, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: ReimbursementDocumentType.Code, DocumentID: rid, DocumentRef: number,
		Title: "Reimbursement " + number + " · " + name + " · " + e.FullName, PropertyID: property,
		Attributes: map[string]any{"amount": floatOf(amount.String()), "category": code, "source": source}})
	if err != nil {
		return uuid.Nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE hris.reimbursements SET approval_request_id = $2 WHERE id = $1 AND approval_request_id IS NULL`, rid, aid)
	return rid, err
}

func (m *Module) createReimbHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request, req ReimbursementInput) (Reimbursement, error) {
	property := handle.Property(ctx)
	e, err := hris.EmployeeByID(ctx, tx, req.EmployeeID)
	if err != nil || e.PropertyID != property {
		return Reimbursement{}, handle.Invalid("employeeId", "not_found", "employee not found")
	}
	rid, err := m.createReimb(ctx, tx, property, e, req, "hr")
	if err != nil {
		return Reimbursement{}, err
	}
	return m.reimbursement(ctx, tx, rid)
}

func (m *Module) myReimbCreateHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request, req ReimbursementInput) (Reimbursement, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return Reimbursement{}, err
	}
	rid, err := m.createReimb(ctx, tx, e.PropertyID, e, req, "ess")
	if err != nil {
		return Reimbursement{}, err
	}
	return m.reimbursement(ctx, tx, rid)
}

func (m *Module) myReimbHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request) ([]Reimbursement, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return nil, err
	}
	return handle.List[Reimbursement](tx.Query(ctx, reimbSelect+` WHERE r.employee_id = $1 ORDER BY r.created_at DESC LIMIT 200`, e.ID))
}

// ReimbursementDecision applies the approval decision (approval engine hook).
func (m *Module) ReimbursementDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	var status, number string
	var amount decimal.Decimal
	var creator *uuid.UUID
	err := tx.QueryRow(ctx, `SELECT status, number, amount, created_by FROM hris.reimbursements WHERE id = $1 FOR UPDATE`, d.DocumentID).
		Scan(&status, &number, &amount, &creator)
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
	if _, err := tx.Exec(ctx, `UPDATE hris.reimbursements SET status = $2, decided_at = now(), decision_note = $3 WHERE id = $1`, d.DocumentID, next,
		nullStr(d.Reason)); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "approval_" + d.Status, EntityType: "hris.reimbursement",
		EntityID: d.DocumentID.String(), EntityLabel: number, PropertyID: &d.PropertyID, Reason: d.Reason, Before: map[string]any{"status": status},
		After: map[string]any{"status": next}}); err != nil {
		return err
	}
	if next == "cancelled" || creator == nil {
		return nil
	}
	return m.notifyUsers(ctx, tx, d.PropertyID, []uuid.UUID{*creator}, NotifyReimbDecided, "/ops/ess/reimbursements", map[string]any{"number": number,
		"amount": money(amount), "decision": next, "reason": d.Reason})
}

func lockReimb(ctx context.Context, tx pgx.Tx, rid, property uuid.UUID) (string, string, *uuid.UUID, error) {
	var status, number string
	var approvalID *uuid.UUID
	err := tx.QueryRow(ctx, `SELECT status, number, approval_request_id FROM hris.reimbursements WHERE id = $1 AND property_id = $2 FOR UPDATE`, rid, property).
		Scan(&status, &number, &approvalID)
	if dbtx.IsNoRows(err) {
		return "", "", nil, errs.NotFound("reimbursement")
	}
	return status, number, approvalID, err
}

func (m *Module) sendReimbHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ReimbursementNote) (Reimbursement, error) {
	property := handle.Property(ctx)
	rid, err := handle.ID(r)
	if err != nil {
		return Reimbursement{}, err
	}
	status, number, _, err := lockReimb(ctx, tx, rid, property)
	if err != nil {
		return Reimbursement{}, err
	}
	if status != "approved" {
		return Reimbursement{}, errs.Conflict("reimbursement_not_approved", "only an approved claim is sent to Finance")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.reimbursements SET status = 'sent_to_finance', sent_at = now(), sent_by = $2, updated_by = $2 WHERE id = $1`, rid,
		actor(ctx)); err != nil {
		return Reimbursement{}, err
	}
	out, err := m.reimbursement(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "send_to_finance", EntityType: "hris.reimbursement", EntityID: rid.String(),
		EntityLabel: number + " · " + out.EmployeeName, PropertyID: &property, Reason: req.Note, Before: map[string]any{"status": status},
		After: map[string]any{"status": "sent_to_finance"}}); err != nil {
		return out, err
	}
	return out, m.notifyUsers(ctx, tx, property, holders(ctx, tx, property, PermReimbPay), NotifyReimbToPay, "/hris/payroll?tab=reimbursements",
		map[string]any{"number": number, "employeeName": out.EmployeeName, "amount": money(dec(out.Amount)), "category": out.CategoryName})
}

func (m *Module) payReimbHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ReimbursementPayInput) (Reimbursement, error) {
	property := handle.Property(ctx)
	rid, err := handle.ID(r)
	if err != nil {
		return Reimbursement{}, err
	}
	status, number, _, err := lockReimb(ctx, tx, rid, property)
	if err != nil {
		return Reimbursement{}, err
	}
	if status != "sent_to_finance" {
		return Reimbursement{}, errs.Conflict("reimbursement_not_sent", "only a claim sent to Finance is paid")
	}
	on, err := parseDate("paidOn", req.PaidOn)
	if err != nil {
		return Reimbursement{}, err
	}
	if on == nil {
		return Reimbursement{}, handle.Invalid("paidOn", "required", "is required")
	}
	if err := methodOK("method", req.Method); err != nil {
		return Reimbursement{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.reimbursements SET status = 'paid', paid_on = $2, payment_method = $3, payment_ref = $4, paid_by = $5,
		updated_by = $5 WHERE id = $1`, rid, ymd(*on), req.Method, nullStr(req.Reference), actor(ctx)); err != nil {
		return Reimbursement{}, err
	}
	out, err := m.reimbursement(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	if m.Events != nil {
		var account, costCenter string
		var unit *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT coalesce(c.expense_account_code, ''), coalesce(e.cost_center, ou.cost_center, ''), e.org_unit_id
			FROM hris.reimbursements r JOIN hris.reimbursement_categories c ON c.id = r.category_id JOIN hris.employees e ON e.id = r.employee_id
			LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id WHERE r.id = $1`, rid).Scan(&account, &costCenter, &unit); err != nil {
			return out, err
		}
		if _, err := m.Events.Publish(ctx, tx, EventReimbursementPaid, "hris.reimbursement", &rid, &property, ReimbursementPaid{ReimbursementID: rid,
			Number: number, EmployeeID: out.EmployeeID, EmployeeName: out.EmployeeName, CategoryCode: out.CategoryCode, ExpenseAccountCode: account,
			CostCenter: costCenter, OrgUnitID: unit, PaidOn: ymd(*on), Amount: out.Amount, Method: req.Method, Reference: strings.TrimSpace(req.Reference),
			BankAccountCode: strings.TrimSpace(req.BankAccountCode)}); err != nil {
			return out, err
		}
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "pay", EntityType: "hris.reimbursement", EntityID: rid.String(),
		EntityLabel: number + " · " + out.EmployeeName, PropertyID: &property, Before: map[string]any{"status": status},
		After: map[string]any{"status": "paid", "paidOn": ymd(*on), "method": req.Method, "reference": req.Reference}})
}

// cancelReimbHTTP cancels a claim: HR before it goes to Finance, the
// employee while it waits for approval.
func (m *Module) cancelReimbHTTP(self bool) func(ctx context.Context, tx pgx.Tx, r *http.Request, req ReimbursementNote) (Reimbursement, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, req ReimbursementNote) (Reimbursement, error) {
		rid, err := handle.ID(r)
		if err != nil {
			return Reimbursement{}, err
		}
		property := handle.Property(ctx)
		if self {
			e, err := me(ctx, tx)
			if err != nil {
				return Reimbursement{}, err
			}
			property = e.PropertyID
			var owner uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT employee_id FROM hris.reimbursements WHERE id = $1`, rid).Scan(&owner); err != nil || owner != e.ID {
				return Reimbursement{}, errs.NotFound("reimbursement")
			}
		}
		status, number, approvalID, err := lockReimb(ctx, tx, rid, property)
		if err != nil {
			return Reimbursement{}, err
		}
		if strings.TrimSpace(req.Note) == "" {
			return Reimbursement{}, handle.Invalid("note", "required", "explain why the claim is cancelled")
		}
		switch {
		case status == "submitted" && approvalID != nil:
			if err := m.Approvals.Cancel(ctx, tx, *approvalID, req.Note); err != nil {
				return Reimbursement{}, err
			}
		case status == "approved" && !self:
			if _, err := tx.Exec(ctx, `UPDATE hris.reimbursements SET status = 'cancelled', decision_note = $2, updated_by = $3 WHERE id = $1`, rid,
				strings.TrimSpace(req.Note), actor(ctx)); err != nil {
				return Reimbursement{}, err
			}
		default:
			return Reimbursement{}, errs.Conflict("reimbursement_not_cancellable", "only a claim not yet sent to Finance can be cancelled")
		}
		out, err := m.reimbursement(ctx, tx, rid)
		if err != nil {
			return out, err
		}
		return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "cancel", EntityType: "hris.reimbursement", EntityID: rid.String(),
			EntityLabel: number + " · " + out.EmployeeName, PropertyID: &property, Reason: req.Note, Before: map[string]any{"status": status},
			After: map[string]any{"status": out.Status}})
	}
}

// receiptHTTP streams the receipt of a claim (ESS: own claims only).
func (m *Module) receiptHTTP(self bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		rid, err := handle.ID(r)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		var fid *uuid.UUID
		err = m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
			x, err := m.reimbursement(ctx, tx, rid)
			if err != nil {
				return err
			}
			if self {
				e, err := me(ctx, tx)
				if err != nil {
					return err
				}
				if x.EmployeeID != e.ID {
					return errs.NotFound("reimbursement")
				}
			} else {
				var prop uuid.UUID
				if err := tx.QueryRow(ctx, `SELECT property_id FROM hris.reimbursements WHERE id = $1`, rid).Scan(&prop); err != nil || prop != handle.Property(ctx) {
					return errs.NotFound("reimbursement")
				}
			}
			if x.FileID == nil {
				return errs.NotFound("receipt")
			}
			fid = x.FileID
			return nil
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if m.Files == nil {
			httpx.WriteError(w, r, errs.NotFound("receipt"))
			return
		}
		rc, name, err := m.Files.Open(ctx, *fid)
		if err != nil {
			httpx.WriteError(w, r, errs.NotFound("receipt"))
			return
		}
		defer rc.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(name, `"`, "")+`"`)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.Copy(w, rc)
	}
}
