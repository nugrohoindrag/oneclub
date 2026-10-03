package billing

// Finance controls of P1: daily gateway reconciliation (FR-PAY-10), monthly
// member statements (FR-MEM-12), the daily accounting export
// (FR-INT-P1-04) and the payment webhook subscriber.

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/shopspring/decimal"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/pdf"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/integration"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/storage"
)

// OnWebhook settles payments from verified payment gateway webhooks
// (subscriber of integration.webhook_received).
func (s *Service) OnWebhook(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		IntegrationCode string         `json:"integrationCode"`
		Capability      string         `json:"capability"`
		Data            map[string]any `json:"data"`
	}
	if err := e.Decode(&p); err != nil {
		return err
	}
	if p.Capability != integration.CapPayment {
		return nil
	}
	return s.SettleGateway(dbtx.System(ctx), tx, p.IntegrationCode, p.Data)
}

// ── reconciliation ────────────────────────────────────────────────────────

// SettlementLine is one row of a vendor settlement report.
type SettlementLine struct {
	ExternalID string `json:"externalId"`
	Reference  string `json:"reference,omitempty"`
	Amount     string `json:"amount"`
	Status     string `json:"status,omitempty" enum:"settled,pending,failed"`
}

type ReconcileRequest struct {
	Date            route.Date       `json:"date"`
	IntegrationCode string           `json:"integrationCode"`
	Settlement      []SettlementLine `json:"settlement,omitempty" doc:"Vendor settlement rows; omitted = pulled from the gateway API"`
}

type Reconciliation struct {
	ID              uuid.UUID            `json:"id"`
	BusinessDate    string               `json:"businessDate"`
	IntegrationCode string               `json:"integrationCode"`
	Status          string               `json:"status" enum:"matched,exceptions"`
	OneClubCount    int                  `json:"oneclubCount"`
	OneClubTotal    string               `json:"oneclubTotal"`
	SettlementCount int                  `json:"settlementCount"`
	SettlementTotal string               `json:"settlementTotal"`
	ExceptionCount  int                  `json:"exceptionCount"`
	CreatedAt       time.Time            `json:"createdAt"`
	Items           []ReconciliationItem `json:"items"`
}

type ReconciliationItem struct {
	ID               uuid.UUID  `json:"id"`
	ExternalID       string     `json:"externalId"`
	PaymentID        *uuid.UUID `json:"paymentId"`
	OneClubAmount    *string    `json:"oneclubAmount"`
	SettlementAmount *string    `json:"settlementAmount"`
	Result           string     `json:"result" enum:"matched,missing_in_settlement,missing_in_oneclub,amount_mismatch"`
	ResolvedAt       *time.Time `json:"resolvedAt"`
	ResolutionNote   *string    `json:"resolutionNote"`
}

// Reconcile compares completed gateway payments of a business date with the
// vendor settlement report and stores the exceptions.
func (s *Service) Reconcile(ctx context.Context, tx pgx.Tx, property uuid.UUID, day time.Time, code string, lines []SettlementLine) (uuid.UUID, error) {
	loc, err := org.Location(ctx, tx, property)
	if err != nil {
		return uuid.Nil, err
	}
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	to := from.AddDate(0, 0, 1)
	if lines == nil {
		rep, err := s.settlementReporter(ctx, code)
		if err != nil {
			return uuid.Nil, err
		}
		items, err := rep.Settlement(ctx, from, to)
		if err != nil {
			return uuid.Nil, errs.Unavailable("settlement report: " + err.Error())
		}
		for _, it := range items {
			lines = append(lines, SettlementLine{ExternalID: it.ExternalID, Reference: it.Reference, Amount: it.Amount, Status: it.Status})
		}
	}
	type ours struct {
		id     uuid.UUID
		ext    string
		ref    string
		amount decimal.Decimal
	}
	rows, err := tx.Query(ctx, `SELECT id, coalesce(external_id, ''), number, amount::text FROM billing.payments
		WHERE property_id = $1 AND integration_code = $2 AND status IN ('completed', 'refunded') AND paid_at >= $3 AND paid_at < $4`, property, code, from, to)
	if err != nil {
		return uuid.Nil, err
	}
	var list []ours
	for rows.Next() {
		var o ours
		var a string
		if err := rows.Scan(&o.id, &o.ext, &o.ref, &a); err != nil {
			rows.Close()
			return uuid.Nil, err
		}
		o.amount = dec(a)
		list = append(list, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return uuid.Nil, err
	}
	rid := id.New()
	type item struct {
		ext      string
		payment  *uuid.UUID
		our, sat *decimal.Decimal
		result   string
	}
	var items []item
	used := map[int]bool{}
	ourTotal, satTotal := decimal.Zero, decimal.Zero
	satCount := 0
	for _, o := range list {
		ourTotal = ourTotal.Add(o.amount)
	}
	for i, l := range lines {
		if l.Status == "failed" {
			continue
		}
		satCount++
		satTotal = satTotal.Add(dec(l.Amount))
		_ = i
	}
	for _, o := range list {
		o := o
		match := -1
		for i, l := range lines {
			if used[i] || l.Status == "failed" {
				continue
			}
			if (l.ExternalID != "" && l.ExternalID == o.ext) || (l.Reference != "" && l.Reference == o.ref) {
				match = i
				break
			}
		}
		amt := o.amount
		if match < 0 {
			items = append(items, item{ext: o.ext, payment: &o.id, our: &amt, result: "missing_in_settlement"})
			continue
		}
		used[match] = true
		sat := dec(lines[match].Amount)
		res := "matched"
		if !sat.Equal(amt) {
			res = "amount_mismatch"
		}
		items = append(items, item{ext: o.ext, payment: &o.id, our: &amt, sat: &sat, result: res})
	}
	for i, l := range lines {
		if used[i] || l.Status == "failed" {
			continue
		}
		sat := dec(l.Amount)
		ext := l.ExternalID
		if ext == "" {
			ext = l.Reference
		}
		items = append(items, item{ext: ext, sat: &sat, result: "missing_in_oneclub"})
	}
	exceptions := 0
	for _, it := range items {
		if it.result != "matched" {
			exceptions++
		}
	}
	status := "matched"
	if exceptions > 0 {
		status = "exceptions"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO billing.reconciliations (id, property_id, business_date, integration_code, status, oneclub_count, oneclub_total,
		settlement_count, settlement_total, exception_count, created_by) VALUES ($1,$2,$3::date,$4,$5,$6,$7::numeric,$8,$9::numeric,$10,$11)`,
		rid, property, day.Format("2006-01-02"), code, status, len(list), ourTotal.String(), satCount, satTotal.String(), exceptions, id.Ptr(actor(ctx))); err != nil {
		return uuid.Nil, err
	}
	for _, it := range items {
		var our, sat *string
		if it.our != nil {
			v := it.our.String()
			our = &v
		}
		if it.sat != nil {
			v := it.sat.String()
			sat = &v
		}
		if _, err := tx.Exec(ctx, `INSERT INTO billing.reconciliation_items (id, property_id, reconciliation_id, external_id, payment_id, oneclub_amount,
			settlement_amount, result) VALUES ($1,$2,$3,$4,$5,$6::numeric,$7::numeric,$8)`, id.New(), property, rid, it.ext, it.payment, our, sat, it.result); err != nil {
			return uuid.Nil, err
		}
	}
	return rid, audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "reconcile", EntityType: "billing.reconciliation", EntityID: rid.String(),
		EntityLabel: code + " " + day.Format("2006-01-02"), PropertyID: &property,
		After: map[string]any{"status": status, "exceptions": exceptions, "oneclubTotal": ourTotal.String(), "settlementTotal": satTotal.String()}})
}

func (s *Service) settlementReporter(ctx context.Context, code string) (integration.SettlementReporter, error) {
	if s.Gateways == nil {
		return nil, errs.Unavailable("no payment integration")
	}
	a, err := s.Gateways.ByCode(ctx, code)
	if err != nil {
		return nil, errs.Validation("invalid_integration", "unknown payment integration", errs.Field("integrationCode", "not_found", "unknown integration"))
	}
	rep, ok := a.(integration.SettlementReporter)
	if !ok {
		return nil, errs.Validation("settlement_required", "this gateway has no settlement API; upload the settlement rows",
			errs.Field("settlement", "required", "settlement rows are required"))
	}
	return rep, nil
}

// GetReconciliation loads a reconciliation with its items.
func GetReconciliation(ctx context.Context, q dbtx.Querier, rid uuid.UUID) (Reconciliation, error) {
	var r Reconciliation
	var day time.Time
	err := q.QueryRow(ctx, `SELECT id, business_date, integration_code, status, oneclub_count, oneclub_total::text, settlement_count, settlement_total::text,
		exception_count, created_at FROM billing.reconciliations WHERE id = $1`, rid).Scan(&r.ID, &day, &r.IntegrationCode, &r.Status, &r.OneClubCount,
		&r.OneClubTotal, &r.SettlementCount, &r.SettlementTotal, &r.ExceptionCount, &r.CreatedAt)
	if dbtx.IsNoRows(err) {
		return r, errs.NotFound("reconciliation")
	}
	if err != nil {
		return r, err
	}
	r.BusinessDate = day.Format("2006-01-02")
	rows, err := q.Query(ctx, `SELECT id, external_id, payment_id, oneclub_amount::text, settlement_amount::text, result, resolved_at, resolution_note
		FROM billing.reconciliation_items WHERE reconciliation_id = $1 ORDER BY (result = 'matched'), external_id`, rid)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	r.Items = []ReconciliationItem{}
	for rows.Next() {
		var it ReconciliationItem
		if err := rows.Scan(&it.ID, &it.ExternalID, &it.PaymentID, &it.OneClubAmount, &it.SettlementAmount, &it.Result, &it.ResolvedAt, &it.ResolutionNote); err != nil {
			return r, err
		}
		r.Items = append(r.Items, it)
	}
	return r, rows.Err()
}

func (h *HTTP) reconcile(w http.ResponseWriter, r *http.Request) {
	var req ReconcileRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	day, err := time.Parse("2006-01-02", string(req.Date))
	if err != nil {
		httpx.WriteError(w, r, errs.Validation("invalid_date", "invalid date", errs.Field("date", "invalid", "YYYY-MM-DD")))
		return
	}
	if req.IntegrationCode == "" {
		httpx.WriteError(w, r, errs.Validation("integration_required", "integration code is required", errs.Field("integrationCode", "required", "payment integration")))
		return
	}
	ctx := r.Context()
	var out Reconciliation
	err = h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		lines := req.Settlement
		if lines != nil && len(lines) == 0 {
			lines = []SettlementLine{}
		}
		rid, err := h.Svc.Reconcile(ctx, tx, prop(ctx), day, req.IntegrationCode, lines)
		if err != nil {
			return err
		}
		out, err = GetReconciliation(ctx, tx, rid)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *HTTP) listReconciliations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := []Reconciliation{}
	err := h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM billing.reconciliations WHERE property_id = $1 ORDER BY business_date DESC, created_at DESC LIMIT 100`, prop(ctx))
		if err != nil {
			return err
		}
		var ids []uuid.UUID
		for rows.Next() {
			var x uuid.UUID
			if err := rows.Scan(&x); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, x)
		}
		rows.Close()
		for _, x := range ids {
			rec, err := GetReconciliation(ctx, tx, x)
			if err != nil {
				return err
			}
			out = append(out, rec)
		}
		return nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Reconciliation]{Items: out})
}

type ResolveItemRequest struct {
	Note string `json:"note"`
}

func (h *HTTP) resolveItem(w http.ResponseWriter, r *http.Request) {
	iid, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req ResolveItemRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if strings.TrimSpace(req.Note) == "" {
		httpx.WriteError(w, r, errs.Validation("note_required", "an explanation is required", errs.Field("note", "required", "explain the difference")))
		return
	}
	ctx := r.Context()
	var out Reconciliation
	err = h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var rid uuid.UUID
		err := tx.QueryRow(ctx, `UPDATE billing.reconciliation_items SET resolved_at = now(), resolved_by = $2, resolution_note = $3
			WHERE id = $1 AND result <> 'matched' RETURNING reconciliation_id`, iid, id.Ptr(actor(ctx)), req.Note).Scan(&rid)
		if dbtx.IsNoRows(err) {
			return errs.NotFound("reconciliation exception")
		}
		if err != nil {
			return err
		}
		p := prop(ctx)
		if err := audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "resolve_exception", EntityType: "billing.reconciliation", EntityID: rid.String(),
			PropertyID: &p, Reason: req.Note, After: map[string]any{"itemId": iid}}); err != nil {
			return err
		}
		out, err = GetReconciliation(ctx, tx, rid)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// ── member statements (FR-MEM-12) ─────────────────────────────────────────

type Statement struct {
	ID             uuid.UUID       `json:"id"`
	AccountID      uuid.UUID       `json:"accountId"`
	AccountNumber  string          `json:"accountNumber"`
	HolderName     string          `json:"holderName"`
	PeriodStart    string          `json:"periodStart"`
	PeriodEnd      string          `json:"periodEnd"`
	OpeningBalance string          `json:"openingBalance"`
	TotalCharges   string          `json:"totalCharges"`
	TotalPayments  string          `json:"totalPayments"`
	ClosingBalance string          `json:"closingBalance"`
	Currency       string          `json:"currency"`
	Lines          json.RawMessage `json:"lines"`
	HasPDF         bool            `json:"hasPdf"`
	SentAt         *time.Time      `json:"sentAt"`
	CreatedAt      time.Time       `json:"createdAt"`
}

const statementCols = `s.id, s.account_id, a.number, a.customer_id, s.period_start, s.period_end, s.opening_balance::text, s.total_charges::text,
	s.total_payments::text, s.closing_balance::text, s.currency, s.lines, s.file_id IS NOT NULL, s.sent_at, s.created_at
	FROM billing.member_statements s JOIN billing.customer_accounts a ON a.id = s.account_id`

// ListStatements lists statements (optionally of one account).
func ListStatements(ctx context.Context, q dbtx.Querier, where string, args ...any) ([]Statement, error) {
	rows, err := q.Query(ctx, `SELECT `+statementCols+` WHERE `+where+` ORDER BY s.period_start DESC, a.number LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	out := []Statement{}
	var cust []uuid.UUID
	for rows.Next() {
		var st Statement
		var ps, pe time.Time
		var c uuid.UUID
		if err := rows.Scan(&st.ID, &st.AccountID, &st.AccountNumber, &c, &ps, &pe, &st.OpeningBalance, &st.TotalCharges, &st.TotalPayments,
			&st.ClosingBalance, &st.Currency, &st.Lines, &st.HasPDF, &st.SentAt, &st.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		st.PeriodStart, st.PeriodEnd = ps.Format("2006-01-02"), pe.Format("2006-01-02")
		out = append(out, st)
		cust = append(cust, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	names, err := crm.Names(ctx, q, cust)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].HolderName = names[cust[i]]
	}
	return out, nil
}

type stmtLine struct {
	Date        string `json:"date"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Amount      string `json:"amount"`
}

// StatementDeps are what statement generation needs besides the DB.
type StatementDeps struct {
	Files     *storage.Files
	Notify    notify.Sender
	PortalURL string
}

// GenerateStatements builds statements of every member account for the
// period [start, end] (inclusive dates, local) and notifies the members.
// It is idempotent per account and period.
func (s *Service) GenerateStatements(ctx context.Context, tx pgx.Tx, deps StatementDeps, property uuid.UUID, start, end time.Time) (int, error) {
	loc, err := org.Location(ctx, tx, property)
	if err != nil {
		return 0, err
	}
	from := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
	to := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
	rows, err := tx.Query(ctx, `SELECT a.id, a.number, a.customer_id, a.currency FROM billing.customer_accounts a
		WHERE a.property_id = $1 AND a.account_type = 'member'
		  AND NOT EXISTS (SELECT 1 FROM billing.member_statements s WHERE s.account_id = a.id AND s.period_start = $2::date)
		  AND EXISTS (SELECT 1 FROM billing.account_entries e WHERE e.account_id = a.id AND e.occurred_at < $3)`, property, start.Format("2006-01-02"), to)
	if err != nil {
		return 0, err
	}
	type acc struct {
		id   uuid.UUID
		num  string
		cust uuid.UUID
		cur  string
	}
	var list []acc
	for rows.Next() {
		var a acc
		if err := rows.Scan(&a.id, &a.num, &a.cust, &a.cur); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, a)
	}
	rows.Close()
	var clubName string
	_ = tx.QueryRow(ctx, `SELECT coalesce(branding->>'appName', name) FROM platform.instance`).Scan(&clubName)
	n := 0
	for _, a := range list {
		var opening string
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(amount), 0)::text FROM billing.account_entries WHERE account_id = $1 AND occurred_at < $2`, a.id, from).Scan(&opening); err != nil {
			return n, err
		}
		er, err := tx.Query(ctx, `SELECT occurred_at, entry_type, description, amount::text FROM billing.account_entries
			WHERE account_id = $1 AND occurred_at >= $2 AND occurred_at < $3 ORDER BY occurred_at, id`, a.id, from, to)
		if err != nil {
			return n, err
		}
		var lines []stmtLine
		charges, payments := decimal.Zero, decimal.Zero
		for er.Next() {
			var at time.Time
			var l stmtLine
			if err := er.Scan(&at, &l.Type, &l.Description, &l.Amount); err != nil {
				er.Close()
				return n, err
			}
			l.Date = at.In(loc).Format("2006-01-02")
			amt := dec(l.Amount)
			if amt.IsPositive() {
				charges = charges.Add(amt)
			} else {
				payments = payments.Add(amt.Neg())
			}
			l.Amount = amt.StringFixed(places(a.cur))
			lines = append(lines, l)
		}
		er.Close()
		if lines == nil {
			lines = []stmtLine{}
		}
		closing := dec(opening).Add(charges).Sub(payments)
		raw, _ := json.Marshal(lines)
		names, err := crm.Names(ctx, tx, []uuid.UUID{a.cust})
		if err != nil {
			return n, err
		}
		holder := names[a.cust]
		// PDF
		d := pdf.New()
		d.Row(18, true, clubName)
		d.Row(12, false, "Member Statement / Laporan Tagihan Member")
		d.Row(10, false, holder+" · "+a.num, start.Format("02 Jan 2006")+" – "+end.Format("02 Jan 2006"))
		d.Space(4)
		d.Rule(d.Y + 10)
		d.Columns(9, true, []float64{pdf.Margin, pdf.Margin + 80, -0}, []string{"Date", "Description", "Amount (" + a.cur + ")"})
		d.Columns(9, false, []float64{pdf.Margin, pdf.Margin + 80, -0}, []string{start.Format("2006-01-02"), "Opening balance", formatAmount(dec(opening), a.cur)})
		for _, l := range lines {
			d.Columns(9, false, []float64{pdf.Margin, pdf.Margin + 80, -0}, []string{l.Date, l.Description, formatAmount(dec(l.Amount), a.cur)})
		}
		d.Rule(d.Y + 10)
		d.Row(10, false, "Total charges", formatAmount(charges, a.cur))
		d.Row(10, false, "Total payments", formatAmount(payments, a.cur))
		d.Row(12, true, "Closing balance", formatAmount(closing, a.cur))
		body := d.Bytes()
		var fileID *uuid.UUID
		if deps.Files != nil && deps.Files.Blob != nil {
			f, err := deps.Files.Save(ctx, tx, fmt.Sprintf("member-statement-%s-%s.pdf", a.num, start.Format("2006-01")), "application/pdf", "export", false,
				bytes.NewReader(body), int64(len(body)))
			if err != nil {
				return n, err
			}
			fileID = &f.ID
		}
		sid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO billing.member_statements (id, property_id, account_id, period_start, period_end, opening_balance, total_charges,
			total_payments, closing_balance, currency, lines, file_id) VALUES ($1,$2,$3,$4::date,$5::date,$6::numeric,$7::numeric,$8::numeric,$9::numeric,$10,$11,$12)
			ON CONFLICT (account_id, period_start) DO NOTHING`, sid, property, a.id, start.Format("2006-01-02"), end.Format("2006-01-02"),
			dec(opening).String(), charges.String(), payments.String(), closing.String(), a.cur, raw, fileID); err != nil {
			return n, err
		}
		// notify the member (e-mail + in-app) with a link to the Member Portal
		c, err := crm.GetCustomer(ctx, tx, a.cust)
		if err == nil && deps.Notify != nil {
			msg := notify.Message{Event: "membership.statement_ready", Category: "general", Link: deps.PortalURL + "/membership/statement",
				Data: map[string]any{"name": c.Name, "period": start.Format("January 2006"), "closingBalance": a.cur + " " + formatAmount(closing, a.cur),
					"link": deps.PortalURL + "/membership/statement"}}
			if c.UserID != nil {
				msg.UserIDs = []uuid.UUID{*c.UserID}
			} else if c.Email != "" {
				msg.Email, msg.Name, msg.Channels = c.Email, c.Name, []string{notify.ChannelEmail}
			}
			if len(msg.UserIDs) > 0 || msg.Email != "" {
				if err := deps.Notify.Send(ctx, tx, msg); err != nil {
					return n, err
				}
				if _, err := tx.Exec(ctx, `UPDATE billing.member_statements SET sent_at = now() WHERE id = $1`, sid); err != nil {
					return n, err
				}
			}
		}
		n++
	}
	if n > 0 {
		if err := audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "generate_statements", Category: audit.CategorySystem,
			EntityType: "billing.member_statement", EntityLabel: start.Format("2006-01"), PropertyID: &property, Metadata: map[string]any{"count": n}}); err != nil {
			return n, err
		}
	}
	return n, nil
}

type GenerateStatementsRequest struct {
	Period string `json:"period" doc:"YYYY-MM"`
}

type GenerateResult struct {
	Generated int `json:"generated"`
}

func (h *HTTP) generateStatements(w http.ResponseWriter, r *http.Request) {
	var req GenerateStatementsRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	start, err := time.Parse("2006-01", req.Period)
	if err != nil {
		httpx.WriteError(w, r, errs.Validation("invalid_period", "period must be YYYY-MM", errs.Field("period", "invalid", "YYYY-MM")))
		return
	}
	end := start.AddDate(0, 1, -1)
	ctx := r.Context()
	var out GenerateResult
	err = h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		n, err := h.Svc.GenerateStatements(ctx, tx, StatementDeps{Files: h.Files, Notify: h.Notify, PortalURL: h.portal()}, prop(ctx), start, end)
		out.Generated = n
		if err != nil {
			return err
		}
		p := prop(ctx)
		return audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "generate_statements", EntityType: "billing.member_statement",
			EntityLabel: req.Period, PropertyID: &p, Metadata: map[string]any{"generated": n}})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *HTTP) portal() string {
	if h.PublicURL != nil {
		return h.PublicURL()
	}
	return ""
}

func (h *HTTP) listStatements(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	lp := httpx.ParseList(r)
	var out []Statement
	err := h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		if v := lp.Filters["accountId"]; v != "" {
			out, err = ListStatements(ctx, tx, `s.property_id = $1 AND s.account_id::text = $2`, prop(ctx), v)
		} else {
			out, err = ListStatements(ctx, tx, `s.property_id = $1`, prop(ctx))
		}
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Statement]{Items: out})
}

// StatementPDF streams the stored statement PDF.
func StatementPDF(ctx context.Context, q dbtx.Querier, files *storage.Files, sid uuid.UUID, w http.ResponseWriter) error {
	var fid *uuid.UUID
	if err := q.QueryRow(ctx, `SELECT file_id FROM billing.member_statements WHERE id = $1`, sid).Scan(&fid); err != nil {
		if dbtx.IsNoRows(err) {
			return errs.NotFound("statement")
		}
		return err
	}
	if fid == nil || files == nil {
		return errs.NotFound("statement file")
	}
	rc, name, err := files.Open(ctx, *fid)
	if err != nil {
		return errs.NotFound("statement file")
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="`+name+`"`)
	_, err = io.Copy(w, rc)
	return err
}

func (h *HTTP) statementPDF(w http.ResponseWriter, r *http.Request) {
	sid, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	if err := h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error { return StatementPDF(ctx, tx, h.Files, sid, w) }); err != nil {
		httpx.WriteError(w, r, err)
	}
}

// ── accounting export (FR-INT-P1-04) ──────────────────────────────────────

type ExportRow struct {
	Section     string `json:"section"`
	Code        string `json:"code"`
	Description string `json:"description"`
	Amount      string `json:"amount"`
}

type AccountingExport struct {
	ID           uuid.UUID   `json:"id"`
	BusinessDate string      `json:"businessDate"`
	FileID       *uuid.UUID  `json:"fileId"`
	Rows         []ExportRow `json:"rows"`
	CreatedAt    time.Time   `json:"createdAt"`
}

// BuildAccountingExport summarises one business date: revenue per all-in
// component (caddy fee held as liability), tax and service, payments per
// method, refunds, member charges and deposits.
func BuildAccountingExport(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time) ([]ExportRow, error) {
	loc, err := org.Location(ctx, q, property)
	if err != nil {
		return nil, err
	}
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	to := from.AddDate(0, 0, 1)
	cur, _ := org.Currency(ctx, q)
	pl := places(cur)
	var out []ExportRow
	sums := map[string]decimal.Decimal{}
	names := map[string]string{}
	liab := map[string]bool{}
	rows, err := q.Query(ctx, `SELECT charge_type, description, components, tax_amount::text, service_amount::text, net_amount::text, liability
		FROM billing.folio_lines WHERE property_id = $1 AND posted_at >= $2 AND posted_at < $3 AND voided_at IS NULL`, property, from, to)
	if err != nil {
		return nil, err
	}
	tax, svc := decimal.Zero, decimal.Zero
	for rows.Next() {
		var ct, desc string
		var comps []byte
		var t, s, net string
		var isLiab bool
		if err := rows.Scan(&ct, &desc, &comps, &t, &s, &net, &isLiab); err != nil {
			rows.Close()
			return nil, err
		}
		tax, svc = tax.Add(dec(t)), svc.Add(dec(s))
		var cs []struct {
			Code      string `json:"code"`
			Name      string `json:"name"`
			Amount    string `json:"amount"`
			Liability bool   `json:"liability"`
		}
		_ = json.Unmarshal(comps, &cs)
		if len(cs) == 0 {
			key := ct
			sums[key] = sums[key].Add(dec(net))
			names[key] = strings.ReplaceAll(ct, "_", " ")
			liab[key] = liab[key] || isLiab || ct == "caddy_tip"
			continue
		}
		for _, c := range cs {
			sums[c.Code] = sums[c.Code].Add(dec(c.Amount))
			names[c.Code] = c.Name
			liab[c.Code] = liab[c.Code] || c.Liability
		}
	}
	rows.Close()
	keys := make([]string, 0, len(sums))
	for k := range sums {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sec := "revenue"
		if liab[k] {
			sec = "liability"
		}
		out = append(out, ExportRow{Section: sec, Code: k, Description: names[k], Amount: sums[k].StringFixed(pl)})
	}
	out = append(out, ExportRow{Section: "tax", Code: "tax", Description: "Tax (PPN / PB1)", Amount: tax.StringFixed(pl)},
		ExportRow{Section: "tax", Code: "service", Description: "Service charge", Amount: svc.StringFixed(pl)})
	pr, err := q.Query(ctx, `SELECT method_type, purpose, sum(amount)::text FROM billing.payments WHERE property_id = $1 AND status IN ('completed','refunded')
		AND paid_at >= $2 AND paid_at < $3 GROUP BY 1, 2 ORDER BY 1, 2`, property, from, to)
	if err != nil {
		return nil, err
	}
	for pr.Next() {
		var m, purpose, a string
		if err := pr.Scan(&m, &purpose, &a); err != nil {
			pr.Close()
			return nil, err
		}
		sec := "payment"
		if purpose == "deposit" {
			sec = "deposit"
		}
		if m == "member_account" {
			sec = "member_charge"
		}
		out = append(out, ExportRow{Section: sec, Code: m, Description: strings.ReplaceAll(m, "_", " ") + " (" + purpose + ")", Amount: dec(a).StringFixed(pl)})
	}
	pr.Close()
	var refunds string
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(amount), 0)::text FROM billing.refunds WHERE property_id = $1 AND status = 'completed'
		AND processed_at >= $2 AND processed_at < $3`, property, from, to).Scan(&refunds); err != nil {
		return nil, err
	}
	out = append(out, ExportRow{Section: "refund", Code: "refund", Description: "Refunds", Amount: dec(refunds).StringFixed(pl)})
	return out, nil
}

// SaveAccountingExport builds and stores the export file of a day.
func (s *Service) SaveAccountingExport(ctx context.Context, tx pgx.Tx, files *storage.Files, property uuid.UUID, day time.Time) (AccountingExport, error) {
	rows, err := BuildAccountingExport(ctx, tx, property, day)
	if err != nil {
		return AccountingExport{}, err
	}
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	_ = cw.Write([]string{"business_date", "section", "code", "description", "amount"})
	totals := map[string]string{}
	agg := map[string]decimal.Decimal{}
	for _, r := range rows {
		_ = cw.Write([]string{day.Format("2006-01-02"), r.Section, r.Code, r.Description, r.Amount})
		agg[r.Section] = agg[r.Section].Add(dec(r.Amount))
	}
	cw.Flush()
	for k, v := range agg {
		totals[k] = v.String()
	}
	var fileID *uuid.UUID
	if files != nil && files.Blob != nil {
		f, err := files.Save(ctx, tx, "accounting-export-"+day.Format("2006-01-02")+".csv", "text/csv", "export", false, bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		if err != nil {
			return AccountingExport{}, err
		}
		fileID = &f.ID
	}
	eid := id.New()
	raw, _ := json.Marshal(totals)
	if _, err := tx.Exec(ctx, `INSERT INTO billing.accounting_exports (id, property_id, business_date, file_id, totals, created_by) VALUES ($1,$2,$3::date,$4,$5,$6)`,
		eid, property, day.Format("2006-01-02"), fileID, raw, id.Ptr(actor(ctx))); err != nil {
		return AccountingExport{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionExport, EntityType: "billing.accounting_export", EntityID: eid.String(),
		EntityLabel: day.Format("2006-01-02"), PropertyID: &property, After: totals}); err != nil {
		return AccountingExport{}, err
	}
	return AccountingExport{ID: eid, BusinessDate: day.Format("2006-01-02"), FileID: fileID, Rows: rows, CreatedAt: clock.Now()}, nil
}

type AccountingExportRequest struct {
	Date route.Date `json:"date"`
}

func (h *HTTP) createAccountingExport(w http.ResponseWriter, r *http.Request) {
	var req AccountingExportRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	day, err := time.Parse("2006-01-02", string(req.Date))
	if err != nil {
		httpx.WriteError(w, r, errs.Validation("invalid_date", "invalid date", errs.Field("date", "invalid", "YYYY-MM-DD")))
		return
	}
	ctx := r.Context()
	var out AccountingExport
	err = h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		out, err = h.Svc.SaveAccountingExport(ctx, tx, h.Files, prop(ctx), day)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *HTTP) listAccountingExports(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := []AccountingExport{}
	err := h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, business_date, file_id, created_at FROM billing.accounting_exports WHERE property_id = $1
			ORDER BY business_date DESC, created_at DESC LIMIT 200`, prop(ctx))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e AccountingExport
			var d time.Time
			if err := rows.Scan(&e.ID, &d, &e.FileID, &e.CreatedAt); err != nil {
				return err
			}
			e.BusinessDate = d.Format("2006-01-02")
			e.Rows = []ExportRow{}
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[AccountingExport]{Items: out})
}

func (h *HTTP) downloadAccountingExport(w http.ResponseWriter, r *http.Request) {
	eid, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	err = h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var fid *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT file_id FROM billing.accounting_exports WHERE id = $1`, eid).Scan(&fid); err != nil {
			return errs.NotFound("accounting export")
		}
		if fid == nil {
			return errs.NotFound("accounting export file")
		}
		rc, name, err := h.Files.Open(ctx, *fid)
		if err != nil {
			return errs.NotFound("accounting export file")
		}
		defer rc.Close()
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		_, err = io.Copy(w, rc)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
	}
}

func (h *HTTP) registerFinance(reg *route.Registry) {
	add := func(rt route.Route) {
		rt.Module = "billing"
		rt.Scope = route.ScopeProperty
		reg.Add(rt)
	}
	const tr, ts, te = "Payment Reconciliation", "Member Statements", "Accounting Export"
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/billing/reconciliations", Tag: tr, Summary: "Reconcile gateway payments of a day with the settlement report",
		Permission: "billing.reconciliation.manage", Request: ReconcileRequest{}, Response: Reconciliation{}, Handler: h.reconcile})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/billing/reconciliations", Tag: tr, Summary: "Reconciliations with exceptions",
		Permission: "billing.reconciliation.view", Response: Reconciliation{}, List: true, Handler: h.listReconciliations})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/billing/reconciliation-items/{id}:resolve", Tag: tr, Summary: "Explain a reconciliation exception",
		Permission: "billing.reconciliation.manage", Request: ResolveItemRequest{}, Response: Reconciliation{}, Status: http.StatusOK, Handler: h.resolveItem})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/billing/member-statements", Tag: ts, Summary: "Member statements",
		Permission: "billing.customer_account.view", Response: Statement{}, List: true, Query: []route.Param{{Name: "filter[accountId]"}}, Handler: h.listStatements})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/billing/member-statements:generate", Tag: ts, Summary: "Generate member statements of a month",
		Permission: "billing.statement.generate", Request: GenerateStatementsRequest{}, Response: GenerateResult{}, Status: http.StatusOK, Handler: h.generateStatements})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/billing/member-statements/{id}/pdf", Tag: ts, Summary: "Member statement PDF",
		Permission: "billing.customer_account.view", RawContent: "application/pdf", Handler: h.statementPDF})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/billing/accounting-exports", Tag: te, Summary: "Export one business day for the accounting system",
		Permission: "billing.accounting_export.create", Request: AccountingExportRequest{}, Response: AccountingExport{}, Handler: h.createAccountingExport})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/billing/accounting-exports", Tag: te, Summary: "Accounting exports",
		Permission: "billing.accounting_export.create", Response: AccountingExport{}, List: true, Handler: h.listAccountingExports})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/billing/accounting-exports/{id}/file", Tag: te, Summary: "Download an accounting export (CSV)",
		Permission: "billing.accounting_export.create", RawContent: "text/csv", Handler: h.downloadAccountingExport})
}

// ── scheduled jobs ────────────────────────────────────────────────────────

// MonthlyAt fires on a day of the month at hh:mm in a timezone.
type MonthlyAt struct {
	Day, Hour, Minute int
	Location          func() *time.Location
}

// Next implements river.PeriodicSchedule.
func (m MonthlyAt) Next(now time.Time) time.Time {
	loc := time.UTC
	if m.Location != nil {
		if l := m.Location(); l != nil {
			loc = l
		}
	}
	t := now.In(loc)
	next := time.Date(t.Year(), t.Month(), m.Day, m.Hour, m.Minute, 0, 0, loc)
	if !next.After(t) {
		next = time.Date(t.Year(), t.Month()+1, m.Day, m.Hour, m.Minute, 0, 0, loc)
	}
	return next.UTC()
}

type StatementsArgs struct{}

func (StatementsArgs) Kind() string { return "billing_member_statements" }
func (StatementsArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

type StatementsWorker struct {
	river.WorkerDefaults[StatementsArgs]
	Svc  *Service
	Deps func() StatementDeps
}

// Work generates last month's statements for every property.
func (w *StatementsWorker) Work(ctx context.Context, _ *river.Job[StatementsArgs]) error {
	ctx = dbtx.System(ctx)
	props, err := properties(ctx, w.Svc.DB)
	if err != nil {
		return err
	}
	for _, p := range props {
		err := w.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
			loc, err := org.Location(ctx, tx, p)
			if err != nil {
				return err
			}
			now := clock.Now().In(loc)
			start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
			end := start.AddDate(0, 1, -1)
			n, err := w.Svc.GenerateStatements(ctx, tx, w.Deps(), p, start, end)
			if n > 0 {
				slog.InfoContext(ctx, "member statements generated", "property", p, "count", n, "period", start.Format("2006-01"))
			}
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

type DailyFinanceArgs struct{}

func (DailyFinanceArgs) Kind() string { return "billing_daily_finance" }
func (DailyFinanceArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

// DailyFinanceWorker exports yesterday for accounting and reconciles every
// payment gateway with a settlement API.
type DailyFinanceWorker struct {
	river.WorkerDefaults[DailyFinanceArgs]
	Svc   *Service
	Files *storage.Files
}

func (w *DailyFinanceWorker) Work(ctx context.Context, _ *river.Job[DailyFinanceArgs]) error {
	ctx = dbtx.System(ctx)
	props, err := properties(ctx, w.Svc.DB)
	if err != nil {
		return err
	}
	var codes []string
	rows, err := w.Svc.DB.Primary.Query(ctx, `SELECT code FROM platform.integrations WHERE capability = 'payment' AND enabled`)
	if err == nil {
		for rows.Next() {
			var c string
			if rows.Scan(&c) == nil {
				codes = append(codes, c)
			}
		}
		rows.Close()
	}
	for _, p := range props {
		err := w.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
			day := businessDate(ctx, tx, p, clock.Now()).AddDate(0, 0, -1)
			var done bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM billing.accounting_exports WHERE property_id = $1 AND business_date = $2::date)`,
				p, day.Format("2006-01-02")).Scan(&done); err != nil {
				return err
			}
			if !done {
				if _, err := w.Svc.SaveAccountingExport(ctx, tx, w.Files, p, day); err != nil {
					return err
				}
			}
			for _, c := range codes {
				var rec bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM billing.reconciliations WHERE property_id = $1 AND business_date = $2::date AND integration_code = $3)`,
					p, day.Format("2006-01-02"), c).Scan(&rec); err != nil {
					return err
				}
				if rec {
					continue
				}
				if _, err := w.Svc.settlementReporter(ctx, c); err != nil {
					continue // file upload needed for this gateway
				}
				sp, err := tx.Begin(ctx)
				if err != nil {
					return err
				}
				if _, err := w.Svc.Reconcile(ctx, sp, p, day, c, nil); err != nil {
					_ = sp.Rollback(ctx)
					slog.WarnContext(ctx, "automatic reconciliation failed", "integration", c, "err", err)
					continue
				}
				if err := sp.Commit(ctx); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func properties(ctx context.Context, db *dbtx.DB) ([]uuid.UUID, error) {
	rows, err := db.Primary.Query(ctx, `SELECT id FROM platform.properties WHERE status = 'active' AND archived_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var p uuid.UUID
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RegisterJobs adds the billing workers and schedules.
func RegisterJobs(reg *jobs.Registrar, svc *Service, files *storage.Files, deps func() StatementDeps, loc func() *time.Location) {
	river.AddWorker(reg.Workers, &StatementsWorker{Svc: svc, Deps: deps})
	river.AddWorker(reg.Workers, &DailyFinanceWorker{Svc: svc, Files: files})
	reg.Periodic = append(reg.Periodic,
		river.NewPeriodicJob(MonthlyAt{Day: 1, Hour: 6, Location: loc}, func() (river.JobArgs, *river.InsertOpts) { return StatementsArgs{}, nil }, nil),
		river.NewPeriodicJob(jobs.DailyAt{Hour: 1, Minute: 30, Location: loc}, func() (river.JobArgs, *river.InsertOpts) { return DailyFinanceArgs{}, nil }, nil))
}
