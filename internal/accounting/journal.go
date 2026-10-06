package accounting

// FR-ACC-02/03/08: manual and adjustment journals (draft → submit →
// approval above the threshold of the Accounting Configuration → posted),
// reversal of posted journals (append-only correction), recurring journals
// (monthly accruals) and automatic reversal at the start of the next
// period.

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/platform/provision"
)

// ManualJournalDocumentType approves manual journals above the threshold.
var ManualJournalDocumentType = provision.DocumentType{Code: "accounting_manual_journal", Module: "accounting", Name: "Manual Journal",
	Attributes: []provision.DocumentAttribute{{Key: "total", Label: "Total", Type: "number"}, {Key: "journalType", Label: "Journal Type", Type: "string"}}}

// ManualJournalLineInput is a line of a manual or recurring journal.
type ManualJournalLineInput struct {
	AccountID    uuid.UUID  `json:"accountId"`
	Debit        string     `json:"debit,omitempty"`
	Credit       string     `json:"credit,omitempty"`
	Description  string     `json:"description,omitempty"`
	BusinessLine string     `json:"businessLine,omitempty"`
	CostCenter   string     `json:"costCenter,omitempty"`
	DepartmentID *uuid.UUID `json:"departmentId,omitempty"`
	PartnerType  string     `json:"partnerType,omitempty" enum:"supplier,customer,ar_account,employee,other"`
	PartnerID    *uuid.UUID `json:"partnerId,omitempty"`
	PartnerName  string     `json:"partnerName,omitempty"`
}

// toLines validates journal line inputs (amounts, accounts, balance).
func toLines(ctx context.Context, q dbtx.Querier, property uuid.UUID, in []ManualJournalLineInput) ([]Line, decimal.Decimal, error) {
	if len(in) < 2 {
		return nil, decimal.Zero, handle.Invalid("lines", "journal_too_short", "a journal needs at least one debit and one credit line")
	}
	var out []Line
	debit, credit := decimal.Zero, decimal.Zero
	for _, l := range in {
		d, err := handle.Decimal("lines.debit", l.Debit, decimal.Zero)
		if err != nil {
			return nil, decimal.Zero, err
		}
		c, err := handle.Decimal("lines.credit", l.Credit, decimal.Zero)
		if err != nil {
			return nil, decimal.Zero, err
		}
		if d.IsNegative() || c.IsNegative() || (d.IsPositive() && c.IsPositive()) || (d.IsZero() && c.IsZero()) {
			return nil, decimal.Zero, handle.Invalid("lines", "invalid_line", "each line is either a debit or a credit of a positive amount")
		}
		var status, code string
		var posting bool
		var props []uuid.UUID
		err = q.QueryRow(ctx, `SELECT code, status, is_posting, properties FROM accounting.accounts WHERE id = $1`, l.AccountID).Scan(&code, &status, &posting, &props)
		if dbtx.IsNoRows(err) {
			return nil, decimal.Zero, handle.Invalid("lines", "unknown_account", "account "+l.AccountID.String()+" not found")
		}
		if err != nil {
			return nil, decimal.Zero, err
		}
		if a := checkAccount(acctInfo{}, false, status, posting, props, property, code); a.Err != "" {
			return nil, decimal.Zero, handle.Invalid("lines", "invalid_account", a.Err)
		}
		debit, credit = debit.Add(d), credit.Add(c)
		out = append(out, Line{AccountID: l.AccountID, Debit: d, Credit: c, Description: l.Description, Dims: Dims{BusinessLine: l.BusinessLine,
			CostCenter: l.CostCenter, DepartmentID: l.DepartmentID, PartnerType: l.PartnerType, PartnerID: l.PartnerID, PartnerName: l.PartnerName}})
	}
	if !debit.Equal(credit) {
		return nil, decimal.Zero, handle.Invalid("lines", "unbalanced", "the journal is not balanced: debit "+debit.String()+" ≠ credit "+credit.String())
	}
	return out, debit, nil
}

// ManualJournalInput drafts a manual or adjustment journal.
type ManualJournalInput struct {
	JournalDate string                   `json:"journalDate"`
	JournalType string                   `json:"journalType,omitempty" enum:"manual,adjustment" doc:"adjustment: finance adjustments allowed in a Soft Closed period"`
	Description string                   `json:"description"`
	AutoReverse bool                     `json:"autoReverse,omitempty" doc:"Reverse automatically on the first day of the next period (accruals, FR-ACC-08)"`
	Lines       []ManualJournalLineInput `json:"lines"`
	Submit      bool                     `json:"submit,omitempty" doc:"Submit at once (post, or request approval above the threshold)"`
}

// ManualJournal is a manual journal request.
type ManualJournal struct {
	ID                uuid.UUID                `json:"id" db:"id"`
	Number            string                   `json:"number" db:"number"`
	JournalDate       string                   `json:"journalDate" db:"journal_date"`
	JournalType       string                   `json:"journalType" db:"journal_type" enum:"manual,adjustment,recurring"`
	Description       string                   `json:"description" db:"description"`
	Currency          string                   `json:"currency" db:"currency"`
	Total             string                   `json:"total" db:"total"`
	Lines             []ManualJournalLineInput `json:"lines" db:"lines"`
	AutoReverse       bool                     `json:"autoReverse" db:"auto_reverse"`
	Status            string                   `json:"status" db:"status" enum:"draft,pending_approval,posted,rejected,cancelled"`
	ApprovalRequestID *uuid.UUID               `json:"approvalRequestId" db:"approval_request_id"`
	JournalID         *uuid.UUID               `json:"journalId" db:"journal_id"`
	JournalNumber     *string                  `json:"journalNumber" db:"journal_number"`
	ReversalJournalID *uuid.UUID               `json:"reversalJournalId" db:"reversal_journal_id"`
	DecisionReason    *string                  `json:"decisionReason" db:"decision_reason"`
	CreatedBy         *uuid.UUID               `json:"createdBy" db:"created_by"`
	CreatedByName     *string                  `json:"createdByName" db:"created_by_name"`
	CreatedAt         time.Time                `json:"createdAt" db:"created_at"`
}

const mjSelect = `SELECT m.id, m.number, to_char(m.journal_date, 'YYYY-MM-DD') AS journal_date, m.journal_type, m.description, m.currency,
	trim_scale(m.total)::text AS total, m.lines, m.auto_reverse, m.status, m.approval_request_id, m.journal_id,
	(SELECT j.number FROM accounting.journals j WHERE j.id = m.journal_id) AS journal_number, m.reversal_journal_id, m.decision_reason, m.created_by,
	(SELECT u.full_name FROM platform.users u WHERE u.id = m.created_by) AS created_by_name, m.created_at FROM accounting.manual_journals m`

// GetManualJournal loads a manual journal of a property.
func GetManualJournal(ctx context.Context, q dbtx.Querier, property, mid uuid.UUID) (ManualJournal, error) {
	return getOne[ManualJournal]("manual journal")(q.Query(ctx, mjSelect+` WHERE m.id = $1 AND m.property_id = $2`, mid, property))
}

// ListManualJournals lists the manual journals of a property.
func ListManualJournals(ctx context.Context, q dbtx.Querier, property uuid.UUID, status string) ([]ManualJournal, error) {
	return handle.List[ManualJournal](q.Query(ctx, mjSelect+` WHERE m.property_id = $1 AND ($2 = '' OR m.status = $2) ORDER BY m.created_at DESC LIMIT 500`,
		property, status))
}

func (in ManualJournalInput) validate(ctx context.Context, q dbtx.Querier, property uuid.UUID) (time.Time, string, decimal.Decimal, error) {
	d, err := parseDate("journalDate", in.JournalDate)
	if err != nil {
		return d, "", decimal.Zero, err
	}
	typ := in.JournalType
	if typ == "" {
		typ = "manual"
	}
	if typ != "manual" && typ != "adjustment" {
		return d, "", decimal.Zero, handle.Invalid("journalType", "invalid", "manual or adjustment")
	}
	if err := handle.Required("description", in.Description); err != nil {
		return d, "", decimal.Zero, err
	}
	_, total, err := toLines(ctx, q, property, in.Lines)
	return d, typ, total, err
}

// CreateManualJournal drafts (and optionally submits) a manual journal.
func (m *Module) CreateManualJournal(ctx context.Context, tx pgx.Tx, property uuid.UUID, in ManualJournalInput) (ManualJournal, error) {
	d, typ, total, err := in.validate(ctx, tx, property)
	if err != nil {
		return ManualJournal{}, err
	}
	if _, err := GetBook(ctx, tx, property); err != nil {
		return ManualJournal{}, err
	}
	mid := id.New()
	num, err := numbering.Next(ctx, tx, property, "MJ", d)
	if err != nil {
		return ManualJournal{}, err
	}
	raw, _ := json.Marshal(in.Lines)
	if _, err := tx.Exec(ctx, `INSERT INTO accounting.manual_journals (id, property_id, number, journal_date, journal_type, description, currency, total, lines,
		auto_reverse, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9,$10,$11,$11)`,
		mid, property, num, d, typ, in.Description, currency(ctx, tx), total.String(), raw, in.AutoReverse, actorPtr(ctx)); err != nil {
		return ManualJournal{}, err
	}
	mj, err := GetManualJournal(ctx, tx, property, mid)
	if err != nil {
		return mj, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: audit.ActionCreate, EntityType: "accounting.manual_journal", EntityID: mid.String(),
		EntityLabel: num + " · " + in.Description, PropertyID: &property, After: mj}); err != nil {
		return mj, err
	}
	if in.Submit {
		return m.SubmitManualJournal(ctx, tx, property, mid)
	}
	return mj, nil
}

func lockManual(ctx context.Context, tx pgx.Tx, property, mid uuid.UUID) (ManualJournal, error) {
	var st string
	if err := tx.QueryRow(ctx, `SELECT status FROM accounting.manual_journals WHERE id = $1 AND property_id = $2 FOR UPDATE`, mid, property).Scan(&st); err != nil {
		if dbtx.IsNoRows(err) {
			return ManualJournal{}, errs.NotFound("manual journal")
		}
		return ManualJournal{}, err
	}
	return GetManualJournal(ctx, tx, property, mid)
}

// UpdateManualJournal edits a draft (or rejected) manual journal.
func (m *Module) UpdateManualJournal(ctx context.Context, tx pgx.Tx, property, mid uuid.UUID, in ManualJournalInput) (ManualJournal, error) {
	before, err := lockManual(ctx, tx, property, mid)
	if err != nil {
		return before, err
	}
	if before.Status != "draft" && before.Status != "rejected" {
		return before, errs.Conflict("not_draft", "only a draft or rejected journal can be edited")
	}
	d, typ, total, err := in.validate(ctx, tx, property)
	if err != nil {
		return before, err
	}
	raw, _ := json.Marshal(in.Lines)
	if _, err := tx.Exec(ctx, `UPDATE accounting.manual_journals SET journal_date = $2, journal_type = $3, description = $4, total = $5::numeric, lines = $6,
		auto_reverse = $7, status = 'draft', updated_by = $8 WHERE id = $1`, mid, d, typ, in.Description, total.String(), raw, in.AutoReverse, actorPtr(ctx)); err != nil {
		return before, err
	}
	after, err := GetManualJournal(ctx, tx, property, mid)
	if err != nil {
		return after, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: audit.ActionUpdate, EntityType: "accounting.manual_journal", EntityID: mid.String(),
		EntityLabel: after.Number, PropertyID: &property, Before: before, After: after}); err != nil {
		return after, err
	}
	if in.Submit {
		return m.SubmitManualJournal(ctx, tx, property, mid)
	}
	return after, nil
}

// SubmitManualJournal posts a draft below the approval threshold, or
// requests approval (FR-ACC-02).
func (m *Module) SubmitManualJournal(ctx context.Context, tx pgx.Tx, property, mid uuid.UUID) (ManualJournal, error) {
	mj, err := lockManual(ctx, tx, property, mid)
	if err != nil {
		return mj, err
	}
	if mj.Status != "draft" && mj.Status != "rejected" {
		return mj, errs.Conflict("not_draft", "the journal is "+mj.Status)
	}
	// the period must accept the journal before it goes for approval
	d, _ := time.Parse("2006-01-02", mj.JournalDate)
	p, err := ensurePeriod(ctx, tx, property, d)
	if err != nil {
		return mj, err
	}
	if !allowedIn(mj.JournalType, p.Status) {
		return mj, ErrPeriodClosed
	}
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return mj, err
	}
	threshold := dec(cfg.ManualJournalApprovalThreshold)
	if threshold.IsPositive() && dec(mj.Total).GreaterThanOrEqual(threshold) {
		if m.Approvals == nil {
			return mj, errs.Conflict("no_approval", "approvals are not available")
		}
		if _, err := tx.Exec(ctx, `UPDATE accounting.manual_journals SET status = 'pending_approval', updated_by = $2 WHERE id = $1`, mid, actorPtr(ctx)); err != nil {
			return mj, err
		}
		req, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: ManualJournalDocumentType.Code, DocumentID: mid, DocumentRef: mj.Number,
			Title: "Manual journal " + mj.Number + " · " + mj.Currency + " " + mj.Total, PropertyID: property,
			Attributes: map[string]any{"total": dec(mj.Total).InexactFloat64(), "journalType": mj.JournalType}})
		if err != nil {
			return mj, err
		}
		if _, err := tx.Exec(ctx, `UPDATE accounting.manual_journals SET approval_request_id = $2 WHERE id = $1`, mid, req); err != nil {
			return mj, err
		}
	} else if err := m.postManual(ctx, tx, property, mid, nil); err != nil {
		return mj, err
	}
	after, err := GetManualJournal(ctx, tx, property, mid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "submit", EntityType: "accounting.manual_journal", EntityID: mid.String(),
		EntityLabel: mj.Number, PropertyID: &property, Before: map[string]any{"status": mj.Status},
		After: map[string]any{"status": after.Status, "journalNumber": after.JournalNumber}})
}

// postManual posts an approved (or below-threshold) manual journal.
func (m *Module) postManual(ctx context.Context, tx pgx.Tx, property, mid uuid.UUID, approvedBy *uuid.UUID) error {
	mj, err := GetManualJournal(ctx, tx, property, mid)
	if err != nil {
		return err
	}
	lines, _, err := toLines(ctx, tx, property, mj.Lines)
	if err != nil {
		return err
	}
	for i := range lines {
		lines[i].SourceType, lines[i].SourceID = "accounting.manual_journal", mid.String()
	}
	d, _ := time.Parse("2006-01-02", mj.JournalDate)
	if err := lockProperty(ctx, tx, property); err != nil {
		return err
	}
	j, err := m.post(ctx, tx, Entry{Property: property, Date: d, Type: mj.JournalType, SourceType: "accounting.manual_journal", SourceID: mid.String(),
		SourceRef: mj.Number, Description: mj.Description, Lines: lines, ApprovedBy: approvedBy}, false)
	if err != nil {
		return err
	}
	var rev *uuid.UUID
	if mj.AutoReverse {
		next := monthEnd(d).AddDate(0, 0, 1)
		r, err := m.reverse(ctx, tx, j.ID, &next, "automatic reversal of accrual "+mj.Number, true)
		if err != nil {
			return err
		}
		rev = &r.ID
	}
	_, err = tx.Exec(ctx, `UPDATE accounting.manual_journals SET status = 'posted', journal_id = $2, reversal_journal_id = $3, decided_at = now() WHERE id = $1`,
		mid, j.ID, rev)
	return err
}

// ManualJournalDecision applies the approval decision of a manual journal.
func (m *Module) ManualJournalDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	var st string
	if err := tx.QueryRow(ctx, `SELECT status FROM accounting.manual_journals WHERE id = $1 FOR UPDATE`, d.DocumentID).Scan(&st); err != nil || st != "pending_approval" {
		return err
	}
	switch d.Status {
	case approval.StatusApproved:
		by := d.DecidedBy
		var approver *uuid.UUID
		if by != uuid.Nil {
			approver = &by
		}
		return m.postManual(ctx, tx, d.PropertyID, d.DocumentID, approver)
	case approval.StatusRejected:
		_, err := tx.Exec(ctx, `UPDATE accounting.manual_journals SET status = 'rejected', decision_reason = $2, decided_at = now() WHERE id = $1`,
			d.DocumentID, nz(d.Reason))
		return err
	case approval.StatusCancelled:
		_, err := tx.Exec(ctx, `UPDATE accounting.manual_journals SET status = 'draft' WHERE id = $1`, d.DocumentID)
		return err
	}
	return nil
}

// CancelManualJournal cancels a draft or pending manual journal.
func (m *Module) CancelManualJournal(ctx context.Context, tx pgx.Tx, property, mid uuid.UUID, in ExceptionResolveInput) (ManualJournal, error) {
	mj, err := lockManual(ctx, tx, property, mid)
	if err != nil {
		return mj, err
	}
	if mj.Status == "posted" || mj.Status == "cancelled" {
		return mj, errs.Conflict("not_cancellable", "the journal is "+mj.Status+"; reverse a posted journal instead")
	}
	if mj.Status == "pending_approval" && mj.ApprovalRequestID != nil && m.Approvals != nil {
		if err := m.Approvals.Cancel(ctx, tx, *mj.ApprovalRequestID, "manual journal cancelled"); err != nil {
			return mj, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE accounting.manual_journals SET status = 'cancelled', updated_by = $2 WHERE id = $1`, mid, actorPtr(ctx)); err != nil {
		return mj, err
	}
	after, err := GetManualJournal(ctx, tx, property, mid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "cancel", EntityType: "accounting.manual_journal", EntityID: mid.String(),
		EntityLabel: mj.Number, PropertyID: &property, Reason: in.Reason, Before: map[string]any{"status": mj.Status}, After: map[string]any{"status": "cancelled"}})
}

// JournalReverseInput reverses a posted journal.
type JournalReverseInput struct {
	Date   string `json:"date,omitempty" doc:"Reversal date (default: the journal date; moved to the next open period when closed)"`
	Reason string `json:"reason"`
}

// ReverseJournal posts the reversal of a posted journal (FR-ACC-03).
func (m *Module) ReverseJournal(ctx context.Context, tx pgx.Tx, property, jid uuid.UUID, in JournalReverseInput) (AccountingJournal, error) {
	orig, err := GetJournal(ctx, tx, jid)
	if err != nil {
		return orig, err
	}
	if orig.PropertyID != property {
		return orig, errs.NotFound("journal")
	}
	if err := handle.Required("reason", in.Reason); err != nil {
		return orig, err
	}
	var d *time.Time
	if strings.TrimSpace(in.Date) != "" {
		t, err := parseDate("date", in.Date)
		if err != nil {
			return orig, err
		}
		d = &t
	}
	if err := lockProperty(ctx, tx, property); err != nil {
		return orig, err
	}
	r, err := m.reverse(ctx, tx, jid, d, in.Reason, d == nil)
	if err != nil {
		return r, err
	}
	out, err := GetJournal(ctx, tx, r.ID)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: audit.ActionVoid, EntityType: "accounting.journal", EntityID: jid.String(),
		EntityLabel: orig.Number, PropertyID: &property, Reason: in.Reason, Before: map[string]any{"status": orig.Status},
		After: map[string]any{"status": "reversed", "reversalJournalId": r.ID, "reversalNumber": r.Number}})
}

// ── recurring journals (FR-ACC-08) ────────────────────────────────────────

// RecurringRunResult reports a run of the recurring journals.
type RecurringRunResult struct {
	Posted   int      `json:"posted"`
	Journals []string `json:"journals"`
	Errors   []string `json:"errors"`
}

// RunRecurringInput runs the recurring journals due by a date.
type RunRecurringInput struct {
	AsOf string `json:"asOf,omitempty" doc:"Default: today"`
}

// RunRecurring posts the recurring journals of a property due by asOf
// (each due date once; automatic reversal on the first day of the next
// month when asked).
func (m *Module) RunRecurring(ctx context.Context, tx pgx.Tx, property uuid.UUID, asOf time.Time) (RecurringRunResult, error) {
	res := RecurringRunResult{Journals: []string{}, Errors: []string{}}
	type rec struct {
		ID          uuid.UUID  `db:"id"`
		Code        string     `db:"code"`
		Name        string     `db:"name"`
		Lines       []byte     `db:"lines"`
		Frequency   string     `db:"frequency"`
		DayOfMonth  int        `db:"day_of_month"`
		NextRunDate time.Time  `db:"next_run_date"`
		EndDate     *time.Time `db:"end_date"`
		AutoReverse bool       `db:"auto_reverse"`
	}
	list, err := handle.List[rec](tx.Query(ctx, `SELECT id, code, name, lines, frequency, day_of_month, next_run_date, end_date, auto_reverse
		FROM accounting.recurring_journals WHERE property_id = $1 AND status = 'active' AND next_run_date <= $2 AND (end_date IS NULL OR next_run_date <= end_date)
		ORDER BY next_run_date, code FOR UPDATE`, property, dateOnly(asOf)))
	if err != nil {
		return res, err
	}
	if len(list) == 0 {
		return res, nil
	}
	if err := lockProperty(ctx, tx, property); err != nil {
		return res, err
	}
	for _, r := range list {
		n := 0
		next := r.NextRunDate
		for !next.After(dateOnly(asOf)) && (r.EndDate == nil || !next.After(*r.EndDate)) {
			var in []ManualJournalLineInput
			_ = json.Unmarshal(r.Lines, &in)
			lines, _, err := toLines(ctx, tx, property, in)
			if err != nil {
				res.Errors = append(res.Errors, r.Code+": "+err.Error())
				break
			}
			for i := range lines {
				lines[i].SourceType, lines[i].SourceID = "accounting.recurring_journal", r.ID.String()
			}
			sp, err := tx.Begin(ctx)
			if err != nil {
				return res, err
			}
			j, err := m.post(ctx, sp, Entry{Property: property, Date: next, Type: "recurring", SourceType: "accounting.recurring_journal", SourceID: r.ID.String(),
				SourceRef: r.Code + " " + ymd(next), Description: r.Name + " " + next.Format("2006-01"), Lines: lines}, true)
			if err == nil && r.AutoReverse {
				rd := monthEnd(next).AddDate(0, 0, 1)
				_, err = m.reverse(ctx, sp, j.ID, &rd, "automatic reversal of "+r.Code, true)
			}
			if err != nil {
				_ = sp.Rollback(ctx)
				res.Errors = append(res.Errors, r.Code+" "+ymd(next)+": "+err.Error())
				break
			}
			if err := sp.Commit(ctx); err != nil {
				return res, err
			}
			res.Posted++
			n++
			res.Journals = append(res.Journals, j.Number)
			next = advance(next, r.Frequency, r.DayOfMonth)
		}
		if _, err := tx.Exec(ctx, `UPDATE accounting.recurring_journals SET next_run_date = $2, last_run_at = now(), runs = runs + $3 WHERE id = $1`,
			r.ID, next, n); err != nil {
			return res, err
		}
	}
	return res, nil
}

// advance returns the next run date of a schedule.
func advance(d time.Time, freq string, day int) time.Time {
	months := map[string]int{"monthly": 1, "quarterly": 3, "yearly": 12}[freq]
	if months == 0 {
		months = 1
	}
	first := time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, months, 0)
	last := monthEnd(first).Day()
	if day > last {
		day = last
	}
	if day < 1 {
		day = 1
	}
	return time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, time.UTC)
}

// validateRecurringLines checks the lines of a recurring journal resource.
func validateRecurringLines(ctx context.Context, q dbtx.Querier, property uuid.UUID, raw any) error {
	b := rawJSON(raw)
	var in []ManualJournalLineInput
	if err := json.Unmarshal(b, &in); err != nil {
		return handle.Invalid("lines", "invalid", "lines must be [{accountId, debit|credit, description}]")
	}
	_, _, err := toLines(ctx, q, property, in)
	return err
}
