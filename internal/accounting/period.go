package accounting

// FR-ACC-06/07 Financial Period & Period Closing: monthly periods per
// property (Open → Soft Closed: only finance adjustments → Closed), the
// closing checklist (business days closed, posting exceptions, bank
// reconciliation, AP posting, stock opname) with the blocking items of the
// closing policy, reopening through approval, accounting.period_closed /
// accounting.period_reopened (contract K10) and the year-end closing of the
// revenue and expense accounts to retained earnings.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/provision"
)

// ReopenDocumentType approves reopening a financial period.
var ReopenDocumentType = provision.DocumentType{Code: "accounting_period_reopen", Module: "accounting", Name: "Reopen Financial Period",
	Attributes: []provision.DocumentAttribute{{Key: "status", Label: "Current Status", Type: "string"}}}

// PeriodChecklistItem is one check of the period closing.
type PeriodChecklistItem struct {
	Code     string `json:"code" enum:"business_days_closed,no_posting_exceptions,bank_reconciled,ap_matched,opname_posted"`
	Label    string `json:"label"`
	OK       bool   `json:"ok"`
	Blocking bool   `json:"blocking" doc:"Blocks the close (Accounting Policies → period closing checklist)"`
	Detail   string `json:"detail"`
}

// FinancialPeriod is a financial period.
type FinancialPeriod struct {
	ID           uuid.UUID             `json:"id" db:"id"`
	PropertyID   uuid.UUID             `json:"propertyId" db:"property_id"`
	Year         int                   `json:"year" db:"year"`
	Month        int                   `json:"month" db:"month"`
	StartDate    string                `json:"startDate" db:"start_date"`
	EndDate      string                `json:"endDate" db:"end_date"`
	Status       string                `json:"status" db:"status" enum:"open,soft_closed,closed"`
	SoftClosedAt *time.Time            `json:"softClosedAt" db:"soft_closed_at"`
	ClosedAt     *time.Time            `json:"closedAt" db:"closed_at"`
	ClosedByName *string               `json:"closedByName" db:"closed_by_name"`
	ReopenedAt   *time.Time            `json:"reopenedAt" db:"reopened_at"`
	ReopenReason *string               `json:"reopenReason" db:"reopen_reason"`
	ReopenStatus *string               `json:"reopenStatus" db:"reopen_status" enum:"pending,approved,rejected"`
	Journals     int                   `json:"journals" db:"journals"`
	StoredChecks json.RawMessage       `json:"-" db:"checklist"`
	Checklist    []PeriodChecklistItem `json:"checklist" db:"-"`
	CanClose     bool                  `json:"canClose" db:"-"`
}

const periodSelect = `SELECT p.id, p.property_id, p.year, p.month, to_char(p.start_date, 'YYYY-MM-DD') AS start_date, to_char(p.end_date, 'YYYY-MM-DD') AS end_date,
	p.status, p.soft_closed_at, p.closed_at, (SELECT u.full_name FROM platform.users u WHERE u.id = p.closed_by) AS closed_by_name, p.reopened_at,
	p.reopen_reason, p.reopen_status, (SELECT count(*) FROM accounting.journals j WHERE j.period_id = p.id)::int AS journals, p.checklist
	FROM accounting.periods p`

// ListPeriods lists the periods of a property (a year or all).
func ListPeriods(ctx context.Context, q dbtx.Querier, property uuid.UUID, year int) ([]FinancialPeriod, error) {
	return handle.List[FinancialPeriod](q.Query(ctx, periodSelect+` WHERE p.property_id = $1 AND ($2 = 0 OR p.year = $2) ORDER BY p.year DESC, p.month DESC`,
		property, year))
}

// GetPeriod loads a period with its live closing checklist.
func GetPeriod(ctx context.Context, q dbtx.Querier, property, pid uuid.UUID) (FinancialPeriod, error) {
	p, err := getOne[FinancialPeriod]("financial period")(q.Query(ctx, periodSelect+` WHERE p.id = $1 AND p.property_id = $2`, pid, property))
	if err != nil {
		return p, err
	}
	if p.Status == PeriodClosed && len(p.StoredChecks) > 2 {
		_ = json.Unmarshal(p.StoredChecks, &p.Checklist)
		return p, nil
	}
	p.Checklist, err = checklist(ctx, q, property, p)
	if err != nil {
		return p, err
	}
	p.CanClose = p.Status != PeriodClosed
	for _, c := range p.Checklist {
		if c.Blocking && !c.OK {
			p.CanClose = false
		}
	}
	return p, nil
}

func checklist(ctx context.Context, q dbtx.Querier, property uuid.UUID, p FinancialPeriod) ([]PeriodChecklistItem, error) {
	pol, err := loadClosingPolicy(ctx, q, property)
	if err != nil {
		return nil, err
	}
	start, _ := time.Parse("2006-01-02", p.StartDate)
	end, _ := time.Parse("2006-01-02", p.EndDate)
	var openDays int
	if err := q.QueryRow(ctx, `SELECT count(DISTINCT l.business_date)::int FROM reporting.acc_folio_lines l WHERE l.property_id = $1
		AND l.business_date BETWEEN $2 AND $3 AND NOT EXISTS (SELECT 1 FROM reporting.acc_billing_days d WHERE d.property_id = l.property_id
		AND d.business_date = l.business_date AND d.status = 'closed')`, property, start, end).Scan(&openDays); err != nil {
		return nil, err
	}
	var exceptions int
	if err := q.QueryRow(ctx, `SELECT count(*)::int FROM accounting.posting_exceptions e LEFT JOIN accounting.processed_events pe ON pe.event_id = e.event_id
		WHERE e.property_id = $1 AND e.status = 'open' AND coalesce(pe.occurred_at, e.created_at) < ($2::date + 1)`, property, end).Scan(&exceptions); err != nil {
		return nil, err
	}
	var unmatched int
	if err := q.QueryRow(ctx, `SELECT count(*)::int FROM accounting.bank_transactions WHERE property_id = $1 AND status = 'unmatched' AND tx_date <= $2`,
		property, end).Scan(&unmatched); err != nil {
		return nil, err
	}
	var apOpen int
	if err := q.QueryRow(ctx, `SELECT count(*)::int FROM accounting.posting_exceptions WHERE property_id = $1 AND status = 'open' AND event_type LIKE 'procurement.%'`,
		property).Scan(&apOpen); err != nil {
		return nil, err
	}
	var opnames int
	if err := q.QueryRow(ctx, `SELECT count(*)::int FROM accounting.posted_sources WHERE property_id = $1 AND source_type = 'inventory.movement' AND key1 = 'opname'
		AND business_date BETWEEN $2 AND $3`, property, start, end).Scan(&opnames); err != nil {
		return nil, err
	}
	return []PeriodChecklistItem{
		{Code: "business_days_closed", Label: "Business days closed (night audit)", OK: openDays == 0, Blocking: pol.RequireBusinessDaysClosed,
			Detail: fmt.Sprintf("%d business days with charges not closed", openDays)},
		{Code: "no_posting_exceptions", Label: "Posting exceptions resolved", OK: exceptions == 0, Blocking: pol.RequireNoPostingExceptions,
			Detail: fmt.Sprintf("%d open posting exceptions", exceptions)},
		{Code: "bank_reconciled", Label: "Bank reconciliation completed", OK: unmatched == 0, Blocking: pol.RequireBankReconciled,
			Detail: fmt.Sprintf("%d unmatched bank statement lines", unmatched)},
		{Code: "ap_matched", Label: "Goods receipts and vendor invoices posted (3-way matching)", OK: apOpen == 0, Blocking: pol.RequireApMatched,
			Detail: fmt.Sprintf("%d open procurement posting exceptions", apOpen)},
		{Code: "opname_posted", Label: "Stock opname posted", OK: opnames > 0, Blocking: pol.RequireOpnamePosted,
			Detail: fmt.Sprintf("%d stock opname movements posted in the period", opnames)},
	}, nil
}

// GeneratePeriodsInput creates the periods of a year.
type GeneratePeriodsInput struct {
	Year int `json:"year"`
}

// GeneratePeriods creates the twelve monthly periods of a year.
func (m *Module) GeneratePeriods(ctx context.Context, tx pgx.Tx, property uuid.UUID, in GeneratePeriodsInput) ([]FinancialPeriod, error) {
	if in.Year < 2000 || in.Year > 2200 {
		return nil, handle.Invalid("year", "invalid", "a year between 2000 and 2200")
	}
	for mth := 1; mth <= 12; mth++ {
		if _, err := ensurePeriod(ctx, tx, property, time.Date(in.Year, time.Month(mth), 1, 0, 0, 0, 0, time.UTC)); err != nil {
			return nil, err
		}
	}
	out, err := ListPeriods(ctx, tx, property, in.Year)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: audit.ActionCreate, EntityType: "accounting.period",
		EntityID: fmt.Sprint(in.Year), EntityLabel: fmt.Sprintf("Financial periods %d", in.Year), PropertyID: &property, After: map[string]any{"year": in.Year}})
}

func lockPeriod(ctx context.Context, tx pgx.Tx, property, pid uuid.UUID) (FinancialPeriod, error) {
	var st string
	if err := tx.QueryRow(ctx, `SELECT status FROM accounting.periods WHERE id = $1 AND property_id = $2 FOR UPDATE`, pid, property).Scan(&st); err != nil {
		if dbtx.IsNoRows(err) {
			return FinancialPeriod{}, errs.NotFound("financial period")
		}
		return FinancialPeriod{}, err
	}
	return GetPeriod(ctx, tx, property, pid)
}

func (m *Module) publishPeriod(ctx context.Context, tx pgx.Tx, ev string, p FinancialPeriod, status string) error {
	if m.Events == nil {
		return nil
	}
	pid, prop := p.ID, p.PropertyID
	_, err := m.Events.Publish(ctx, tx, ev, "accounting.period", &pid, &prop, map[string]any{"periodId": p.ID, "year": p.Year, "month": p.Month,
		"status": status, "startDate": p.StartDate, "endDate": p.EndDate, "closedAt": time.Now().UTC()})
	return err
}

// SoftClosePeriod soft-closes an open period: from now on only finance
// adjustment journals may be dated in it.
func (m *Module) SoftClosePeriod(ctx context.Context, tx pgx.Tx, property, pid uuid.UUID) (FinancialPeriod, error) {
	p, err := lockPeriod(ctx, tx, property, pid)
	if err != nil {
		return p, err
	}
	if p.Status != PeriodOpen {
		return p, errs.Conflict("not_open", "the period is "+p.Status)
	}
	raw, _ := json.Marshal(p.Checklist)
	if _, err := tx.Exec(ctx, `UPDATE accounting.periods SET status = 'soft_closed', checklist = $2, soft_closed_at = now(), soft_closed_by = $3 WHERE id = $1`,
		pid, raw, actorPtr(ctx)); err != nil {
		return p, err
	}
	if err := m.publishPeriod(ctx, tx, EventPeriodClosed, p, PeriodSoftClosed); err != nil {
		return p, err
	}
	after, err := GetPeriod(ctx, tx, property, pid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "soft_close", EntityType: "accounting.period", EntityID: pid.String(),
		EntityLabel: fmt.Sprintf("%04d-%02d", p.Year, p.Month), PropertyID: &property, Before: map[string]any{"status": p.Status},
		After: map[string]any{"status": after.Status, "checklist": p.Checklist}})
}

// ClosePeriod closes a period when the blocking checklist items pass; no
// journal of any source can be dated in it afterwards.
func (m *Module) ClosePeriod(ctx context.Context, tx pgx.Tx, property, pid uuid.UUID) (FinancialPeriod, error) {
	p, err := lockPeriod(ctx, tx, property, pid)
	if err != nil {
		return p, err
	}
	if p.Status == PeriodClosed {
		return p, errs.Conflict("closed", "the period is already closed")
	}
	for _, c := range p.Checklist {
		if c.Blocking && !c.OK {
			return p, errs.Conflict("checklist_open", "the period cannot be closed: "+c.Label+" ("+c.Detail+")")
		}
	}
	raw, _ := json.Marshal(p.Checklist)
	if _, err := tx.Exec(ctx, `UPDATE accounting.periods SET status = 'closed', checklist = $2, closed_at = now(), closed_by = $3, reopen_status = NULL
		WHERE id = $1`, pid, raw, actorPtr(ctx)); err != nil {
		return p, err
	}
	if err := m.publishPeriod(ctx, tx, EventPeriodClosed, p, PeriodClosed); err != nil {
		return p, err
	}
	after, err := GetPeriod(ctx, tx, property, pid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "close", EntityType: "accounting.period", EntityID: pid.String(),
		EntityLabel: fmt.Sprintf("%04d-%02d", p.Year, p.Month), PropertyID: &property, Before: map[string]any{"status": p.Status},
		After: map[string]any{"status": PeriodClosed, "checklist": p.Checklist}})
}

// PeriodReopenInput asks to reopen a period.
type PeriodReopenInput struct {
	Reason string `json:"reason"`
}

// ReopenPeriod requests reopening a soft-closed or closed period
// (approval; reopened at once without a workflow).
func (m *Module) ReopenPeriod(ctx context.Context, tx pgx.Tx, property, pid uuid.UUID, in PeriodReopenInput) (FinancialPeriod, error) {
	p, err := lockPeriod(ctx, tx, property, pid)
	if err != nil {
		return p, err
	}
	if p.Status == PeriodOpen {
		return p, errs.Conflict("open", "the period is open")
	}
	if p.ReopenStatus != nil && *p.ReopenStatus == "pending" {
		return p, errs.Conflict("reopen_pending", "a reopening request is already pending")
	}
	if err := handle.Required("reason", in.Reason); err != nil {
		return p, err
	}
	if m.Approvals == nil {
		return p, errs.Conflict("no_approval", "approvals are not available")
	}
	var fy bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounting.fiscal_years WHERE property_id = $1 AND year = $2 AND status = 'closed')`,
		property, p.Year).Scan(&fy); err != nil {
		return p, err
	}
	if fy {
		return p, errs.Conflict("year_closed", "the fiscal year is closed; reverse the year-end closing first")
	}
	if _, err := tx.Exec(ctx, `UPDATE accounting.periods SET reopen_status = 'pending', reopen_reason = $2 WHERE id = $1`, pid, in.Reason); err != nil {
		return p, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "reopen_request", EntityType: "accounting.period", EntityID: pid.String(),
		EntityLabel: fmt.Sprintf("%04d-%02d", p.Year, p.Month), PropertyID: &property, Reason: in.Reason, Before: map[string]any{"status": p.Status},
		After: map[string]any{"reopenStatus": "pending"}}); err != nil {
		return p, err
	}
	req, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: ReopenDocumentType.Code, DocumentID: pid,
		DocumentRef: fmt.Sprintf("%04d-%02d", p.Year, p.Month), Title: fmt.Sprintf("Reopen financial period %04d-%02d: %s", p.Year, p.Month, in.Reason),
		PropertyID: property, Attributes: map[string]any{"status": p.Status}})
	if err != nil {
		return p, err
	}
	if _, err := tx.Exec(ctx, `UPDATE accounting.periods SET reopen_request_id = $2 WHERE id = $1`, pid, req); err != nil {
		return p, err
	}
	return GetPeriod(ctx, tx, property, pid)
}

// ReopenDecision applies the approval decision of a reopening.
func (m *Module) ReopenDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	p, err := GetPeriod(ctx, tx, d.PropertyID, d.DocumentID)
	if err != nil {
		return err
	}
	switch d.Status {
	case approval.StatusApproved:
		if _, err := tx.Exec(ctx, `UPDATE accounting.periods SET status = 'open', reopen_status = 'approved', reopened_at = now(), reopened_by = $2,
			checklist = '[]'::jsonb WHERE id = $1`, p.ID, uidPtr(d.DecidedBy)); err != nil {
			return err
		}
		if err := m.publishPeriod(ctx, tx, EventPeriodReopened, p, PeriodOpen); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "reopen", EntityType: "accounting.period", EntityID: p.ID.String(),
			EntityLabel: fmt.Sprintf("%04d-%02d", p.Year, p.Month), PropertyID: &d.PropertyID, Reason: deref(p.ReopenReason),
			Before: map[string]any{"status": p.Status}, After: map[string]any{"status": PeriodOpen}})
	case approval.StatusRejected, approval.StatusCancelled:
		_, err := tx.Exec(ctx, `UPDATE accounting.periods SET reopen_status = $2 WHERE id = $1`, p.ID, map[string]string{approval.StatusRejected: "rejected",
			approval.StatusCancelled: "rejected"}[d.Status])
		return err
	}
	return nil
}

func uidPtr(u uuid.UUID) *uuid.UUID {
	if u == uuid.Nil {
		return nil
	}
	return &u
}

// ── year-end closing (FR-ACC-07) ──────────────────────────────────────────

// YearEndInput closes a fiscal year.
type YearEndInput struct {
	Year int `json:"year"`
}

// FiscalYear is a closed fiscal year.
type FiscalYear struct {
	Year             int        `json:"year" db:"year"`
	Status           string     `json:"status" db:"status"`
	NetIncome        string     `json:"netIncome" db:"net_income"`
	ClosingJournalID *uuid.UUID `json:"closingJournalId" db:"closing_journal_id"`
	ClosedAt         time.Time  `json:"closedAt" db:"closed_at"`
}

// CloseYear posts the year-end closing journal: every revenue, cost of
// sales and expense account of the year is closed to retained earnings
// (dated 31 December, allowed while December is open or soft closed).
func (m *Module) CloseYear(ctx context.Context, tx pgx.Tx, property uuid.UUID, in YearEndInput) (FiscalYear, error) {
	if in.Year < 2000 || in.Year > 2200 {
		return FiscalYear{}, handle.Invalid("year", "invalid", "a year")
	}
	var done bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounting.fiscal_years WHERE property_id = $1 AND year = $2)`, property, in.Year).Scan(&done); err != nil {
		return FiscalYear{}, err
	}
	if done {
		return FiscalYear{}, errs.Conflict("year_closed", "the fiscal year is already closed")
	}
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return FiscalYear{}, err
	}
	reCode := cfg.Accounts["retained_earnings"]
	re, err := accountIDByCode(ctx, tx, reCode)
	if err != nil {
		return FiscalYear{}, errs.Conflict("missing_account", "retained earnings account "+reCode+" not found")
	}
	start := time.Date(in.Year, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(in.Year, 12, 31, 0, 0, 0, 0, time.UTC)
	rows, err := tx.Query(ctx, `SELECT l.account_id, sum(l.debit - l.credit)::text FROM accounting.journal_lines l JOIN accounting.accounts a ON a.id = l.account_id
		JOIN accounting.journals j ON j.id = l.journal_id WHERE l.property_id = $1 AND l.journal_date BETWEEN $2 AND $3
		AND a.account_type IN ('revenue', 'expense', 'cogs') AND j.journal_type <> 'closing' GROUP BY l.account_id HAVING sum(l.debit - l.credit) <> 0`,
		property, start, end)
	if err != nil {
		return FiscalYear{}, err
	}
	var lines []Line
	net := decimal.Zero // debit balance of P&L = loss
	for rows.Next() {
		var aid uuid.UUID
		var bal string
		if err := rows.Scan(&aid, &bal); err != nil {
			rows.Close()
			return FiscalYear{}, err
		}
		b := dec(bal)
		net = net.Add(b)
		if b.IsPositive() {
			lines = append(lines, Line{AccountID: aid, Credit: b, Description: fmt.Sprintf("Closing %d", in.Year)})
		} else {
			lines = append(lines, Line{AccountID: aid, Debit: b.Neg(), Description: fmt.Sprintf("Closing %d", in.Year)})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return FiscalYear{}, err
	}
	income := net.Neg()
	var jid *uuid.UUID
	if len(lines) > 0 {
		if net.IsPositive() {
			lines = append(lines, Line{AccountID: re, Debit: net, Description: fmt.Sprintf("Net loss %d", in.Year)})
		} else if net.IsNegative() {
			lines = append(lines, Line{AccountID: re, Credit: net.Neg(), Description: fmt.Sprintf("Net income %d", in.Year)})
		}
		if err := lockProperty(ctx, tx, property); err != nil {
			return FiscalYear{}, err
		}
		j, err := m.post(ctx, tx, Entry{Property: property, Date: end, Type: "closing", SourceType: "accounting.fiscal_year", SourceID: fmt.Sprint(in.Year),
			SourceRef: fmt.Sprintf("FY%d", in.Year), Description: fmt.Sprintf("Year-end closing %d to retained earnings", in.Year), Lines: lines}, false)
		if err != nil {
			return FiscalYear{}, err
		}
		jid = &j.ID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO accounting.fiscal_years (property_id, year, status, net_income, closing_journal_id, closed_by)
		VALUES ($1,$2,'closed',$3::numeric,$4,$5)`, property, in.Year, income.String(), jid, actorPtr(ctx)); err != nil {
		return FiscalYear{}, err
	}
	fy := FiscalYear{Year: in.Year, Status: "closed", NetIncome: income.String(), ClosingJournalID: jid, ClosedAt: time.Now().UTC()}
	return fy, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "year_end_close", EntityType: "accounting.fiscal_year",
		EntityID: fmt.Sprint(in.Year), EntityLabel: fmt.Sprintf("Fiscal year %d", in.Year), PropertyID: &property, After: fy})
}

// ListFiscalYears lists the closed fiscal years.
func ListFiscalYears(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]FiscalYear, error) {
	return handle.List[FiscalYear](q.Query(ctx, `SELECT year, status, trim_scale(net_income)::text AS net_income, closing_journal_id, closed_at
		FROM accounting.fiscal_years WHERE property_id = $1 ORDER BY year DESC`, property))
}
