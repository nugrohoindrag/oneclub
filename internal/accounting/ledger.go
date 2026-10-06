package accounting

// FR-ACC-02..05: posting a journal. Every journal is balanced (debit =
// credit, checked here and by the database at commit), append-only, numbered
// JV-YYYYMMDD-NNNN per property and dated in a financial period: automatic
// postings dated in a closed period move to the next open period (or are
// refused, per Accounting Configuration), manual journals are refused.

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
)

// Dims are the analysis dimensions of a journal line (FR-ACC-01).
type Dims struct {
	BusinessLine     string     `json:"businessLine,omitempty"`
	RevenueComponent string     `json:"revenueComponent,omitempty"`
	Component        string     `json:"component,omitempty"`
	CostCenter       string     `json:"costCenter,omitempty"`
	DepartmentID     *uuid.UUID `json:"departmentId,omitempty"`
	PartnerType      string     `json:"partnerType,omitempty"`
	PartnerID        *uuid.UUID `json:"partnerId,omitempty"`
	PartnerName      string     `json:"partnerName,omitempty"`
}

func (d Dims) key() string {
	p, dep := "", ""
	if d.PartnerID != nil {
		p = d.PartnerID.String()
	}
	if d.DepartmentID != nil {
		dep = d.DepartmentID.String()
	}
	return strings.Join([]string{d.BusinessLine, d.RevenueComponent, d.Component, d.CostCenter, dep, d.PartnerType, p}, "|")
}

// Line is a journal line to post.
type Line struct {
	AccountID   uuid.UUID
	Debit       decimal.Decimal
	Credit      decimal.Decimal
	Description string
	Dims
	RuleID     *uuid.UUID
	SourceType string
	SourceID   string
}

// Entry is a journal to post.
type Entry struct {
	Property    uuid.UUID
	Date        time.Time
	Type        string // automatic | manual | adjustment | recurring | reversal | opening | closing
	SourceType  string
	SourceID    string
	SourceRef   string
	EventID     *uuid.UUID
	Description string
	Lines       []Line
	Merge       bool // net lines of the same account and dimensions (automatic journals)
	Reverses    *uuid.UUID
	RequestID   *uuid.UUID
	ApprovedBy  *uuid.UUID
}

// ErrPeriodClosed is returned when a journal cannot be dated in its period.
var ErrPeriodClosed = errs.Conflict("period_closed", "the financial period is closed")

// mergeLines nets lines of the same account and dimensions.
func mergeLines(lines []Line) []Line {
	type acc struct {
		l   Line
		net decimal.Decimal
		src map[string]bool
	}
	idx := map[string]*acc{}
	var order []string
	for _, l := range lines {
		k := l.AccountID.String() + "#" + l.key()
		x, ok := idx[k]
		if !ok {
			x = &acc{l: l, src: map[string]bool{}}
			idx[k] = x
			order = append(order, k)
		}
		x.net = x.net.Add(l.Debit).Sub(l.Credit)
		x.src[l.SourceType+"|"+l.SourceID] = true
		if x.l.Description != l.Description {
			x.l.Description = ""
		}
		if x.l.RuleID == nil || l.RuleID == nil || *x.l.RuleID != *l.RuleID {
			x.l.RuleID = nil
		}
	}
	var out []Line
	for _, k := range order {
		x := idx[k]
		if x.net.IsZero() {
			continue
		}
		l := x.l
		if len(x.src) > 1 {
			l.SourceType, l.SourceID = "", ""
		}
		l.Debit, l.Credit = decimal.Zero, decimal.Zero
		if x.net.IsPositive() {
			l.Debit = x.net
		} else {
			l.Credit = x.net.Neg()
		}
		out = append(out, l)
	}
	// debits first, stable by account
	sort.SliceStable(out, func(i, j int) bool { return out[i].Debit.IsPositive() && !out[j].Debit.IsPositive() })
	return out
}

// period is a financial period row.
type period struct {
	ID     uuid.UUID
	Year   int
	Month  int
	Start  time.Time
	End    time.Time
	Status string
}

// ensurePeriod returns the period of date (created open when missing),
// locked for share so a concurrent close waits for the posting.
func ensurePeriod(ctx context.Context, tx pgx.Tx, property uuid.UUID, date time.Time) (period, error) {
	y, m := date.Year(), int(date.Month())
	start := time.Date(y, date.Month(), 1, 0, 0, 0, 0, time.UTC)
	if _, err := tx.Exec(ctx, `INSERT INTO accounting.periods (id, property_id, year, month, start_date, end_date) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (property_id, year, month) DO NOTHING`, id.New(), property, y, m, start, start.AddDate(0, 1, -1)); err != nil {
		return period{}, err
	}
	var p period
	err := tx.QueryRow(ctx, `SELECT id, year, month, start_date, end_date, status FROM accounting.periods WHERE property_id = $1 AND year = $2 AND month = $3
		FOR SHARE`, property, y, m).Scan(&p.ID, &p.Year, &p.Month, &p.Start, &p.End, &p.Status)
	return p, err
}

// allowedIn reports whether a journal type may be dated in a period status.
func allowedIn(journalType, status string) bool {
	switch status {
	case PeriodOpen:
		return true
	case PeriodSoftClosed:
		return journalType == "adjustment" || journalType == "closing"
	}
	return false
}

// datePeriod finds the period of an entry. Automatic postings (and their
// reversals) dated in a period that does not accept them move to the first
// day of the next open period when the configuration says so.
func datePeriod(ctx context.Context, tx pgx.Tx, e *Entry, redirect bool) (period, error) {
	p, err := ensurePeriod(ctx, tx, e.Property, e.Date)
	if err != nil {
		return p, err
	}
	if allowedIn(e.Type, p.Status) {
		return p, nil
	}
	if !redirect {
		return p, ErrPeriodClosed
	}
	d := p.End.AddDate(0, 0, 1)
	for i := 0; i < 240; i++ {
		np, err := ensurePeriod(ctx, tx, e.Property, d)
		if err != nil {
			return np, err
		}
		if allowedIn(e.Type, np.Status) {
			e.Date = np.Start
			return np, nil
		}
		d = np.End.AddDate(0, 0, 1)
	}
	return p, ErrPeriodClosed
}

// AccountingJournal is a posted journal.
type AccountingJournal struct {
	ID               uuid.UUID               `json:"id" db:"id"`
	Number           string                  `json:"number" db:"number"`
	JournalDate      string                  `json:"journalDate" db:"journal_date"`
	PeriodID         uuid.UUID               `json:"periodId" db:"period_id"`
	JournalType      string                  `json:"journalType" db:"journal_type" enum:"automatic,manual,adjustment,recurring,reversal,opening,closing"`
	Status           string                  `json:"status" db:"status" enum:"posted,reversed"`
	SourceType       string                  `json:"sourceType" db:"source_type"`
	SourceID         *string                 `json:"sourceId" db:"source_id"`
	SourceRef        *string                 `json:"sourceRef" db:"source_ref"`
	EventID          *uuid.UUID              `json:"eventId" db:"event_id"`
	Description      string                  `json:"description" db:"description"`
	Currency         string                  `json:"currency" db:"currency"`
	Total            string                  `json:"total" db:"total"`
	ReversesID       *uuid.UUID              `json:"reversesJournalId" db:"reverses_journal_id"`
	ReversedByID     *uuid.UUID              `json:"reversedByJournalId" db:"reversed_by"`
	RequestedDate    *string                 `json:"requestedDate" db:"requested_date" doc:"Date asked for when the period was closed (moved to the next open period)"`
	PostedAt         time.Time               `json:"postedAt" db:"posted_at"`
	PostedBy         *uuid.UUID              `json:"postedBy" db:"posted_by"`
	PostedByName     *string                 `json:"postedByName" db:"posted_by_name"`
	ApprovedBy       *uuid.UUID              `json:"approvedBy" db:"approved_by"`
	PropertyID       uuid.UUID               `json:"propertyId" db:"property_id"`
	Lines            []AccountingJournalLine `json:"lines" db:"-"`
	SourceReferences []JournalSourceRef      `json:"sourceReferences,omitempty" db:"-" doc:"Source documents posted by this journal (drill-down, FR-ACC-05)"`
}

// AccountingJournalLine is a posted journal line.
type AccountingJournalLine struct {
	ID               uuid.UUID  `json:"id" db:"id"`
	LineNo           int        `json:"lineNo" db:"line_no"`
	AccountID        uuid.UUID  `json:"accountId" db:"account_id"`
	AccountCode      string     `json:"accountCode" db:"account_code"`
	AccountName      string     `json:"accountName" db:"account_name"`
	Debit            string     `json:"debit" db:"debit"`
	Credit           string     `json:"credit" db:"credit"`
	Description      *string    `json:"description" db:"description"`
	BusinessLine     *string    `json:"businessLine" db:"business_line"`
	RevenueComponent *string    `json:"revenueComponent" db:"revenue_component"`
	Component        *string    `json:"component" db:"component"`
	CostCenter       *string    `json:"costCenter" db:"cost_center"`
	DepartmentID     *uuid.UUID `json:"departmentId" db:"department_id"`
	PartnerType      *string    `json:"partnerType" db:"partner_type"`
	PartnerID        *uuid.UUID `json:"partnerId" db:"partner_id"`
	PartnerName      *string    `json:"partnerName" db:"partner_name"`
	RuleID           *uuid.UUID `json:"ruleId" db:"rule_id"`
	SourceType       *string    `json:"sourceType" db:"source_type"`
	SourceID         *string    `json:"sourceId" db:"source_id"`
}

// JournalSourceRef is a source document posted by a journal.
type JournalSourceRef struct {
	SourceType   string  `json:"sourceType" db:"source_type"`
	SourceID     string  `json:"sourceId" db:"source_id"`
	BusinessDate *string `json:"businessDate" db:"business_date"`
	Amount       string  `json:"amount" db:"amount"`
}

const journalSelect = `SELECT j.id, j.number, to_char(j.journal_date, 'YYYY-MM-DD') AS journal_date, j.period_id, j.journal_type,
	CASE WHEN EXISTS (SELECT 1 FROM accounting.journals r WHERE r.reverses_journal_id = j.id) THEN 'reversed' ELSE 'posted' END AS status,
	j.source_type, j.source_id, j.source_ref, j.event_id, j.description, j.currency, trim_scale(j.total)::text AS total, j.reverses_journal_id,
	(SELECT r.id FROM accounting.journals r WHERE r.reverses_journal_id = j.id) AS reversed_by, to_char(j.requested_date, 'YYYY-MM-DD') AS requested_date,
	j.posted_at, j.posted_by, (SELECT u.full_name FROM platform.users u WHERE u.id = j.posted_by) AS posted_by_name, j.approved_by, j.property_id
	FROM accounting.journals j`

// GetJournal loads a journal with its lines.
func GetJournal(ctx context.Context, q dbtx.Querier, jid uuid.UUID) (AccountingJournal, error) {
	j, err := getOne[AccountingJournal]("journal")(q.Query(ctx, journalSelect+` WHERE j.id = $1`, jid))
	if err != nil {
		return j, err
	}
	if j.Lines, err = handle.List[AccountingJournalLine](q.Query(ctx, `SELECT l.id, l.line_no, l.account_id, a.code AS account_code, a.name AS account_name,
		trim_scale(l.debit)::text AS debit, trim_scale(l.credit)::text AS credit, l.description, l.business_line, l.revenue_component, l.component,
		l.cost_center, l.department_id, l.partner_type, l.partner_id, l.partner_name, l.rule_id, l.source_type, l.source_id
		FROM accounting.journal_lines l JOIN accounting.accounts a ON a.id = l.account_id WHERE l.journal_id = $1 ORDER BY l.line_no`, jid)); err != nil {
		return j, err
	}
	j.SourceReferences, err = handle.List[JournalSourceRef](q.Query(ctx, `SELECT source_type, source_id::text AS source_id, to_char(business_date, 'YYYY-MM-DD') AS business_date,
		trim_scale(amount)::text AS amount FROM accounting.posted_sources WHERE journal_id = $1 ORDER BY source_type, business_date LIMIT 500`, jid))
	return j, err
}

// post validates and inserts a journal. redirect moves an entry dated in
// a closed period to the next open period.
func (m *Module) post(ctx context.Context, tx pgx.Tx, e Entry, redirect bool) (AccountingJournal, error) {
	lines := e.Lines
	if e.Merge {
		lines = mergeLines(lines)
	}
	var kept []Line
	debit, credit := decimal.Zero, decimal.Zero
	for _, l := range lines {
		l.Debit, l.Credit = l.Debit.Round(4), l.Credit.Round(4)
		if l.Debit.IsNegative() || l.Credit.IsNegative() || (l.Debit.IsPositive() && l.Credit.IsPositive()) {
			return AccountingJournal{}, errs.Validation("invalid_line", "a line is either a debit or a credit of a positive amount")
		}
		if l.Debit.IsZero() && l.Credit.IsZero() {
			continue
		}
		debit, credit = debit.Add(l.Debit), credit.Add(l.Credit)
		kept = append(kept, l)
	}
	if len(kept) < 2 {
		return AccountingJournal{}, errs.Validation("journal_too_short", "a journal needs at least one debit and one credit line")
	}
	if !debit.Equal(credit) {
		return AccountingJournal{}, errs.Validation("unbalanced", "the journal is not balanced: debit "+debit.String()+" ≠ credit "+credit.String())
	}
	if strings.TrimSpace(e.Description) == "" {
		return AccountingJournal{}, handle.Invalid("description", "required", "description is required")
	}
	// accounts must be active posting accounts available at the property
	seen := map[uuid.UUID]bool{}
	for _, l := range kept {
		if seen[l.AccountID] {
			continue
		}
		seen[l.AccountID] = true
		var status, code string
		var posting bool
		var props []uuid.UUID
		err := tx.QueryRow(ctx, `SELECT code, status, is_posting, properties FROM accounting.accounts WHERE id = $1`, l.AccountID).Scan(&code, &status, &posting, &props)
		if dbtx.IsNoRows(err) {
			return AccountingJournal{}, errs.Validation("unknown_account", "account not found", errs.Field("lines", "not_found", "account "+l.AccountID.String()+" not found"))
		}
		if err != nil {
			return AccountingJournal{}, err
		}
		if a := checkAccount(acctInfo{}, false, status, posting, props, e.Property, code); a.Err != "" {
			return AccountingJournal{}, errs.Validation("invalid_account", a.Err, errs.Field("lines", "invalid_account", a.Err))
		}
	}
	requested := e.Date
	p, err := datePeriod(ctx, tx, &e, redirect)
	if err != nil {
		return AccountingJournal{}, err
	}
	var requestedDate *time.Time
	if !dateOnly(requested).Equal(dateOnly(e.Date)) {
		r := dateOnly(requested)
		requestedDate = &r
	}
	num, err := numbering.Next(ctx, tx, e.Property, "JV", e.Date)
	if err != nil {
		return AccountingJournal{}, err
	}
	jid := id.New()
	cur := currency(ctx, tx)
	var src *string
	if e.SourceID != "" {
		src = &e.SourceID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO accounting.journals (id, property_id, number, journal_date, period_id, journal_type, source_type, source_id, source_ref,
		event_id, description, currency, total, reverses_journal_id, requested_date, request_id, posted_by, approved_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::numeric,$14,$15,$16,$17,$18)`,
		jid, e.Property, num, dateOnly(e.Date), p.ID, e.Type, e.SourceType, src, nz(e.SourceRef), e.EventID, e.Description, cur, debit.String(), e.Reverses,
		requestedDate, e.RequestID, actorPtr(ctx), e.ApprovedBy); err != nil {
		if ok, c := dbtx.IsUniqueViolation(err); ok && strings.Contains(c, "reversal") {
			return AccountingJournal{}, errs.Conflict("already_reversed", "the journal is already reversed")
		}
		return AccountingJournal{}, err
	}
	batch := &pgx.Batch{}
	for i, l := range kept {
		batch.Queue(`INSERT INTO accounting.journal_lines (id, property_id, journal_id, line_no, journal_date, account_id, debit, credit, description,
			business_line, revenue_component, component, cost_center, department_id, partner_type, partner_id, partner_name, rule_id, source_type, source_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8::numeric,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
			id.New(), e.Property, jid, i+1, dateOnly(e.Date), l.AccountID, l.Debit.String(), l.Credit.String(), nz(l.Description), nz(l.BusinessLine),
			nz(l.RevenueComponent), nz(l.Component), nz(l.CostCenter), l.DepartmentID, nz(l.PartnerType), l.PartnerID, nz(l.PartnerName), l.RuleID,
			nz(l.SourceType), nz(l.SourceID))
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return AccountingJournal{}, err
	}
	// check the balance now instead of at commit (FR-ACC-04 is enforced by
	// the deferred constraint triggers; this surfaces errors here)
	if _, err := tx.Exec(ctx, `SET CONSTRAINTS accounting.journals_balanced, accounting.journal_lines_balanced IMMEDIATE`); err != nil {
		return AccountingJournal{}, err
	}
	if _, err := tx.Exec(ctx, `SET CONSTRAINTS accounting.journals_balanced, accounting.journal_lines_balanced DEFERRED`); err != nil {
		return AccountingJournal{}, err
	}
	if m.Events != nil {
		pid := e.Property
		if _, err := m.Events.Publish(ctx, tx, EventJournalPosted, "accounting.journal", &jid, &pid, map[string]any{"journalId": jid, "number": num,
			"journalDate": ymd(e.Date), "journalType": e.Type, "sourceType": e.SourceType, "sourceId": e.SourceID, "total": debit.String(), "currency": cur}); err != nil {
			return AccountingJournal{}, err
		}
	}
	return AccountingJournal{ID: jid, Number: num, JournalDate: ymd(e.Date), PeriodID: p.ID, JournalType: e.Type, Status: "posted", SourceType: e.SourceType,
		Description: e.Description, Currency: cur, Total: debit.String(), PropertyID: e.Property}, nil
}

// reverse posts the reversal of a journal (append-only correction,
// FR-ACC-03) dated date (default: the original date, moved to the next
// open period when that is closed).
func (m *Module) reverse(ctx context.Context, tx pgx.Tx, jid uuid.UUID, date *time.Time, reason string, redirect bool) (AccountingJournal, error) {
	orig, err := GetJournal(ctx, tx, jid)
	if err != nil {
		return orig, err
	}
	if orig.Status == "reversed" {
		return orig, errs.Conflict("already_reversed", "the journal is already reversed")
	}
	if orig.JournalType == "reversal" {
		return orig, errs.Conflict("reversal_of_reversal", "a reversal journal cannot be reversed; post a new journal")
	}
	d, _ := time.Parse("2006-01-02", orig.JournalDate)
	if date != nil {
		d = *date
	}
	var lines []Line
	for _, l := range orig.Lines {
		nl := Line{AccountID: l.AccountID, Debit: dec(l.Credit), Credit: dec(l.Debit), Description: deref(l.Description), RuleID: l.RuleID,
			SourceType: deref(l.SourceType), SourceID: deref(l.SourceID)}
		nl.Dims = Dims{BusinessLine: deref(l.BusinessLine), RevenueComponent: deref(l.RevenueComponent), Component: deref(l.Component),
			CostCenter: deref(l.CostCenter), DepartmentID: l.DepartmentID, PartnerType: deref(l.PartnerType), PartnerID: l.PartnerID, PartnerName: deref(l.PartnerName)}
		lines = append(lines, nl)
	}
	desc := "Reversal of " + orig.Number
	if reason != "" {
		desc += ": " + reason
	}
	return m.post(ctx, tx, Entry{Property: orig.PropertyID, Date: d, Type: "reversal", SourceType: "accounting.journal", SourceID: orig.ID.String(),
		SourceRef: orig.Number, Description: desc, Lines: lines, Reverses: &orig.ID}, redirect)
}
