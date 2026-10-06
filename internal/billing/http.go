package billing

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/pdf"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/storage"
)

// RefundDocumentType is the approval document type for refunds above the
// Refund Policy limit (FR-PAY-07).
var RefundDocumentType = provision.DocumentType{Code: "refund", Module: "billing", Name: "Refund",
	Attributes: []provision.DocumentAttribute{{Key: "amount", Label: "Amount", Type: "number"}}}

// HTTP serves the billing endpoints.
type HTTP struct {
	Svc       *Service
	Approvals *approval.Engine
	Notify    notify.Sender
	Files     *storage.Files
	PublicURL func() string // Member Portal base URL for links
	Holder    HolderResolver
	// WebsiteURL is the public website base URL (invoice payment links, P3).
	WebsiteURL func() string
}

// SubmitRefund implements Approver with the approval engine.
func (h *HTTP) SubmitRefund(ctx context.Context, tx pgx.Tx, refundID, property uuid.UUID, ref, title string, amount decimal.Decimal) (uuid.UUID, string, error) {
	f, _ := amount.Float64()
	return h.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: RefundDocumentType.Code, DocumentID: refundID, DocumentRef: ref,
		Title: title, PropertyID: property, Attributes: map[string]any{"amount": f}})
}

// RefundDecision is the approval hook of refunds.
func (h *HTTP) RefundDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	if d.Status == approval.StatusApproved {
		return h.Svc.CompleteRefund(ctx, tx, d.DocumentID)
	}
	st := "rejected"
	if d.Status == approval.StatusCancelled {
		st = "cancelled"
	}
	return h.Svc.RejectRefund(ctx, tx, d.DocumentID, st, d.Reason)
}

func pathID(r *http.Request) (uuid.UUID, error) { return httpx.PathUUID(r, "id") }

func prop(ctx context.Context) uuid.UUID {
	p, _ := reqctx.Property(ctx)
	return p
}

// ── folios ────────────────────────────────────────────────────────────────

// Folio is the folio list view.
type Folio struct {
	ID         uuid.UUID  `json:"id"`
	Number     string     `json:"number"`
	CustomerID *uuid.UUID `json:"customerId"`
	GuestID    *uuid.UUID `json:"guestId"`
	HolderName string     `json:"holderName"`
	SourceType string     `json:"sourceType"`
	SourceID   *uuid.UUID `json:"sourceId"`
	SourceRef  *string    `json:"sourceRef"`
	Status     string     `json:"status" enum:"open,closed,cancelled"`
	Currency   string     `json:"currency"`
	Version    int        `json:"version"`
	Charges    string     `json:"charges"`
	Payments   string     `json:"paid"`
	Balance    string     `json:"balance"`
	ClosedAt   *time.Time `json:"closedAt"`
	CreatedAt  time.Time  `json:"createdAt"`
}

// FolioLine is one charge.
type FolioLine struct {
	ID            uuid.UUID  `json:"id"`
	ChargeType    string     `json:"chargeType"`
	Description   string     `json:"description"`
	Quantity      string     `json:"quantity"`
	UnitPrice     string     `json:"unitPrice"`
	NetAmount     string     `json:"netAmount"`
	TaxAmount     string     `json:"taxAmount"`
	ServiceAmount string     `json:"serviceAmount"`
	Total         string     `json:"total"`
	Components    any        `json:"components"`
	SnapshotID    *uuid.UUID `json:"pricingSnapshotId"`
	ReferenceType *string    `json:"referenceType"`
	ReferenceID   *uuid.UUID `json:"referenceId"`
	Liability     bool       `json:"liability"`
	PostedAt      time.Time  `json:"postedAt"`
	VoidedAt      *time.Time `json:"voidedAt"`
	VoidReason    *string    `json:"voidReason"`
}

// Deposit is the API view of a deposit.
type Deposit struct {
	ID            uuid.UUID  `json:"id"`
	Number        string     `json:"number"`
	FolioID       *uuid.UUID `json:"folioId"`
	PaymentID     uuid.UUID  `json:"paymentId"`
	Amount        string     `json:"amount"`
	AppliedAmount string     `json:"appliedAmount"`
	Currency      string     `json:"currency"`
	Status        string     `json:"status" enum:"held,applied,refunded"`
	AppliedAt     *time.Time `json:"appliedAt"`
	CreatedAt     time.Time  `json:"createdAt"`
}

// FolioDetail is the full folio.
type FolioDetail struct {
	Folio
	Summary  Summary     `json:"summary"`
	Lines    []FolioLine `json:"lines"`
	Payments []Payment   `json:"payments"`
	Deposits []Deposit   `json:"deposits"`
	Refunds  []Refund    `json:"refunds"`
}

const folioCols = `f.id, f.number, f.customer_id, f.guest_id, f.holder_name, f.source_type, f.source_id, f.source_ref, f.status, f.currency, f.version,
	coalesce((SELECT sum(total) FROM billing.folio_lines l WHERE l.folio_id = f.id AND l.voided_at IS NULL), 0)::text,
	(coalesce((SELECT sum(amount - refunded_amount) FROM billing.payments p WHERE p.folio_id = f.id AND p.status IN ('completed','refunded') AND p.purpose = 'settlement'), 0)
	 + coalesce((SELECT sum(applied_amount) FROM billing.deposits d WHERE d.folio_id = f.id), 0))::text, f.closed_at, f.created_at`

func scanFolio(row pgx.Row) (Folio, error) {
	var f Folio
	err := row.Scan(&f.ID, &f.Number, &f.CustomerID, &f.GuestID, &f.HolderName, &f.SourceType, &f.SourceID, &f.SourceRef, &f.Status, &f.Currency,
		&f.Version, &f.Charges, &f.Payments, &f.ClosedAt, &f.CreatedAt)
	p := places(f.Currency)
	f.Balance = dec(f.Charges).Sub(dec(f.Payments)).StringFixed(p)
	f.Charges, f.Payments = dec(f.Charges).StringFixed(p), dec(f.Payments).StringFixed(p)
	return f, err
}

// GetFolio loads a folio with lines, payments, deposits and refunds.
func GetFolio(ctx context.Context, q dbtx.Querier, fid uuid.UUID) (FolioDetail, error) {
	var d FolioDetail
	f, err := scanFolio(q.QueryRow(ctx, `SELECT `+folioCols+` FROM billing.folios f WHERE f.id = $1`, fid))
	if dbtx.IsNoRows(err) {
		return d, errs.NotFound("folio")
	}
	if err != nil {
		return d, err
	}
	d.Folio = f
	if d.Summary, err = FolioSummary(ctx, q, fid); err != nil {
		return d, err
	}
	rows, err := q.Query(ctx, `SELECT id, charge_type, description, quantity::text, unit_price::text, net_amount::text, tax_amount::text, service_amount::text,
		total::text, components, pricing_snapshot_id, reference_type, reference_id, liability, posted_at, voided_at, void_reason
		FROM billing.folio_lines WHERE folio_id = $1 ORDER BY posted_at, id`, fid)
	if err != nil {
		return d, err
	}
	d.Lines = []FolioLine{}
	for rows.Next() {
		var l FolioLine
		if err := rows.Scan(&l.ID, &l.ChargeType, &l.Description, &l.Quantity, &l.UnitPrice, &l.NetAmount, &l.TaxAmount, &l.ServiceAmount, &l.Total,
			&l.Components, &l.SnapshotID, &l.ReferenceType, &l.ReferenceID, &l.Liability, &l.PostedAt, &l.VoidedAt, &l.VoidReason); err != nil {
			rows.Close()
			return d, err
		}
		d.Lines = append(d.Lines, l)
	}
	rows.Close()
	prs, err := q.Query(ctx, `SELECT `+paymentCols+paymentFrom+` WHERE p.folio_id = $1 ORDER BY p.created_at`, fid)
	if err != nil {
		return d, err
	}
	d.Payments = []Payment{}
	for prs.Next() {
		p, err := scanPayment(prs)
		if err != nil {
			prs.Close()
			return d, err
		}
		d.Payments = append(d.Payments, p)
	}
	prs.Close()
	drs, err := q.Query(ctx, `SELECT id, number, folio_id, payment_id, amount::text, applied_amount::text, currency, status, applied_at, created_at
		FROM billing.deposits WHERE folio_id = $1 ORDER BY created_at`, fid)
	if err != nil {
		return d, err
	}
	d.Deposits = []Deposit{}
	for drs.Next() {
		var x Deposit
		if err := drs.Scan(&x.ID, &x.Number, &x.FolioID, &x.PaymentID, &x.Amount, &x.AppliedAmount, &x.Currency, &x.Status, &x.AppliedAt, &x.CreatedAt); err != nil {
			drs.Close()
			return d, err
		}
		d.Deposits = append(d.Deposits, x)
	}
	drs.Close()
	rrs, err := q.Query(ctx, `SELECT `+refundCols+` WHERE r.folio_id = $1 ORDER BY r.created_at`, fid)
	if err != nil {
		return d, err
	}
	d.Refunds = []Refund{}
	for rrs.Next() {
		x, err := scanRefund(rrs)
		if err != nil {
			rrs.Close()
			return d, err
		}
		d.Refunds = append(d.Refunds, x)
	}
	rrs.Close()
	return d, rrs.Err()
}

func (h *HTTP) listFolios(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	lp := httpx.ParseList(r)
	where := []string{"f.property_id = $1"}
	args := []any{prop(ctx)}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	if v := lp.Filters["status"]; v != "" {
		add("f.status = ?", v)
	}
	if v := lp.Filters["sourceType"]; v != "" {
		add("f.source_type = ?", v)
	}
	if v := lp.Filters["customerId"]; v != "" {
		add("f.customer_id::text = ?", v)
	}
	if v := r.URL.Query().Get("date"); v != "" {
		add(clubDay("f.created_at"), v)
	}
	if lp.Q != "" {
		add("(f.number ILIKE ? OR f.holder_name ILIKE ? OR f.source_ref ILIKE ?)", "%"+lp.Q+"%")
	}
	offset := cursorOffset(lp.Cursor)
	out := []Folio{}
	err := h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+folioCols+` FROM billing.folios f WHERE `+strings.Join(where, " AND ")+
			fmt.Sprintf(` ORDER BY f.created_at DESC, f.id DESC LIMIT %d OFFSET %d`, lp.Limit+1, offset), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			f, err := scanFolio(rows)
			if err != nil {
				return err
			}
			out = append(out, f)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, page(out, lp.Limit, offset))
}

func cursorOffset(c string) int {
	s, err := httpx.DecodeCursor(c)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(s)
	return n
}

func page[T any](items []T, limit, offset int) httpx.Page[T] {
	p := httpx.Page[T]{Items: items}
	if len(items) > limit {
		p.Items = items[:limit]
		p.NextCursor = httpx.EncodeCursor(strconv.Itoa(offset + limit))
	}
	return p
}

func (h *HTTP) getFolio(w http.ResponseWriter, r *http.Request) {
	fid, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var out FolioDetail
	err = h.Svc.DB.WithReadTx(r.Context(), func(tx pgx.Tx) error {
		out, err = GetFolio(r.Context(), tx, fid)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

type CreateFolioRequest struct {
	CustomerID *uuid.UUID `json:"customerId,omitempty"`
	GuestID    *uuid.UUID `json:"guestId,omitempty"`
	HolderName string     `json:"holderName"`
	SourceRef  string     `json:"sourceRef,omitempty"`
}

func (h *HTTP) createFolio(w http.ResponseWriter, r *http.Request) {
	var req CreateFolioRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if strings.TrimSpace(req.HolderName) == "" {
		httpx.WriteError(w, r, errs.Validation("holder_required", "holder name is required", errs.Field("holderName", "required", "holder name is required")))
		return
	}
	ctx := r.Context()
	var out FolioDetail
	err := h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		ref, err := h.Svc.OpenFolio(ctx, tx, FolioInput{Property: prop(ctx), CustomerID: req.CustomerID, GuestID: req.GuestID, HolderName: req.HolderName,
			SourceType: "walk_in", SourceRef: req.SourceRef})
		if err != nil {
			return err
		}
		out, err = GetFolio(ctx, tx, ref.ID)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

type AddChargeRequest struct {
	ChargeType  string `json:"chargeType" enum:"other,locker,bag_storage,discount"`
	Description string `json:"description"`
	Quantity    string `json:"quantity,omitempty"`
	UnitPrice   string `json:"unitPrice"`
}

// addCharge posts a manual charge (misc. items); priced golf charges come
// from bookings with a pricing snapshot.
func (h *HTTP) addCharge(w http.ResponseWriter, r *http.Request) {
	fid, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req AddChargeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if req.ChargeType == "" {
		req.ChargeType = "other"
	}
	if req.ChargeType != "other" && req.ChargeType != "locker" && req.ChargeType != "bag_storage" && req.ChargeType != "discount" {
		httpx.WriteError(w, r, errs.Validation("invalid_charge_type", "invalid charge type", errs.Field("chargeType", "invalid", "other, locker, bag_storage or discount")))
		return
	}
	unit, err := decimal.NewFromString(req.UnitPrice)
	if err != nil || strings.TrimSpace(req.Description) == "" {
		httpx.WriteError(w, r, errs.Validation("invalid_charge", "description and unit price are required",
			errs.Field("unitPrice", "invalid", "decimal amount")))
		return
	}
	qty := decimal.NewFromInt(1)
	if req.Quantity != "" {
		if qty, err = decimal.NewFromString(req.Quantity); err != nil || !qty.IsPositive() {
			httpx.WriteError(w, r, errs.Validation("invalid_quantity", "invalid quantity", errs.Field("quantity", "invalid", "positive number")))
			return
		}
	}
	if req.ChargeType == "discount" && unit.IsPositive() {
		unit = unit.Neg()
	}
	ctx := r.Context()
	var out FolioDetail
	err = h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		total := unit.Mul(qty)
		if _, err := h.Svc.AddCharge(ctx, tx, Charge{FolioID: fid, ChargeType: req.ChargeType, Description: req.Description, Quantity: qty, UnitPrice: unit,
			Net: total, Total: total}); err != nil {
			return err
		}
		out, err = GetFolio(ctx, tx, fid)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

type ReasonRequest struct {
	Reason string `json:"reason"`
}

func (h *HTTP) voidLine(w http.ResponseWriter, r *http.Request) {
	fid, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	lid, err := httpx.PathUUID(r, "lineId")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req ReasonRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		httpx.WriteError(w, r, errs.Validation("reason_required", "a reason is required", errs.Field("reason", "required", "reason is required")))
		return
	}
	ctx := r.Context()
	var out FolioDetail
	err = h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var owner uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT folio_id FROM billing.folio_lines WHERE id = $1`, lid).Scan(&owner); err != nil || owner != fid {
			return errs.NotFound("folio line")
		}
		if err := h.Svc.VoidCharge(ctx, tx, lid, req.Reason); err != nil {
			return err
		}
		out, err = GetFolio(ctx, tx, fid)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// checkIfMatch enforces optimistic concurrency on folios (Technical Doc
// §8.1 ETag / If-Match).
func checkIfMatch(ctx context.Context, tx pgx.Tx, r *http.Request, fid uuid.UUID) error {
	im := strings.Trim(r.Header.Get("If-Match"), `"`)
	if im == "" {
		return nil
	}
	var v int
	if err := tx.QueryRow(ctx, `SELECT version FROM billing.folios WHERE id = $1`, fid).Scan(&v); err != nil {
		return errs.NotFound("folio")
	}
	if strconv.Itoa(v) != im {
		return errs.Precondition("the folio changed since you opened it; reload and try again")
	}
	return nil
}

func (h *HTTP) closeFolio(w http.ResponseWriter, r *http.Request) {
	fid, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	var out FolioDetail
	err = h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		if err := checkIfMatch(ctx, tx, r, fid); err != nil {
			return err
		}
		if err := h.Svc.CloseFolio(ctx, tx, fid); err != nil {
			return err
		}
		out, err = GetFolio(ctx, tx, fid)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *HTTP) reopenFolio(w http.ResponseWriter, r *http.Request) {
	fid, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req ReasonRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		httpx.WriteError(w, r, errs.Validation("reason_required", "a reason is required", errs.Field("reason", "required", "reason is required")))
		return
	}
	ctx := r.Context()
	var out FolioDetail
	err = h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		if err := checkIfMatch(ctx, tx, r, fid); err != nil {
			return err
		}
		var num string
		err := tx.QueryRow(ctx, `UPDATE billing.folios SET status = 'open', reopened_at = now(), reopened_by = $2, reopen_reason = $3, closed_at = NULL,
			version = version + 1 WHERE id = $1 AND status = 'closed' RETURNING number`, fid, id.Ptr(actor(ctx)), req.Reason).Scan(&num)
		if dbtx.IsNoRows(err) {
			return errs.Conflict("folio_not_closed", "only closed folios can be reopened")
		}
		if err != nil {
			return err
		}
		pid := prop(ctx)
		if err := audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "reopen", EntityType: "billing.folio", EntityID: fid.String(),
			EntityLabel: num, PropertyID: &pid, Reason: req.Reason, Before: map[string]any{"status": "closed"}, After: map[string]any{"status": "open"}}); err != nil {
			return err
		}
		out, err = GetFolio(ctx, tx, fid)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// ── payments ──────────────────────────────────────────────────────────────

type PaymentRequest struct {
	FolioID         *uuid.UUID `json:"folioId,omitempty"`
	AccountID       *uuid.UUID `json:"accountId,omitempty" doc:"Member account to charge (member_account) or to settle"`
	PaymentMethodID *uuid.UUID `json:"paymentMethodId,omitempty"`
	MethodType      string     `json:"methodType,omitempty" enum:"cash,bank_transfer,virtual_account,qris,card,payment_gateway,member_account"`
	Channel         string     `json:"channel,omitempty" enum:"online,venue,member_account"`
	Purpose         string     `json:"purpose,omitempty" enum:"settlement,deposit"`
	Amount          string     `json:"amount" doc:"Default: the folio balance"`
	Reference       string     `json:"reference,omitempty" doc:"EDC approval / transfer reference"`
	PayerName       string     `json:"payerName,omitempty"`
}

func (h *HTTP) createPayment(w http.ResponseWriter, r *http.Request) {
	var req PaymentRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	var out Payment
	err := h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		amount := decimal.Zero
		if req.Amount != "" {
			var err error
			if amount, err = decimal.NewFromString(req.Amount); err != nil {
				return errs.Validation("invalid_amount", "invalid amount", errs.Field("amount", "invalid", "decimal"))
			}
		} else if req.FolioID != nil {
			sum, err := FolioSummary(ctx, tx, *req.FolioID)
			if err != nil {
				return err
			}
			amount = dec(sum.Balance)
		}
		if req.FolioID != nil && req.MethodType == "member_account" && req.AccountID == nil {
			var cust *uuid.UUID
			_ = tx.QueryRow(ctx, `SELECT customer_id FROM billing.folios WHERE id = $1`, *req.FolioID).Scan(&cust)
			if cust != nil {
				if a, err := AccountFor(ctx, tx, prop(ctx), *cust, "member"); err == nil && a != nil {
					req.AccountID = &a.ID
				}
			}
		}
		var err error
		out, err = h.Svc.TakePayment(ctx, tx, PaymentInput{FolioID: req.FolioID, AccountID: req.AccountID, MethodType: req.MethodType,
			PaymentMethodID: req.PaymentMethodID, Channel: req.Channel, Purpose: req.Purpose, Amount: amount, Reference: req.Reference,
			PayerName: req.PayerName, Description: "OneClub payment"})
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *HTTP) listPayments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	lp := httpx.ParseList(r)
	where := []string{"p.property_id = $1"}
	args := []any{prop(ctx)}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	for k, col := range map[string]string{"status": "p.status", "methodType": "p.method_type", "channel": "p.channel", "folioId": "p.folio_id::text",
		"accountId": "p.account_id::text"} {
		if v := lp.Filters[k]; v != "" {
			add(col+" = ?", v)
		}
	}
	if v := r.URL.Query().Get("date"); v != "" {
		add(clubDay("coalesce(p.paid_at, p.created_at)"), v)
	}
	if lp.Q != "" {
		add("(p.number ILIKE ? OR f.number ILIKE ? OR p.reference ILIKE ?)", "%"+lp.Q+"%")
	}
	offset := cursorOffset(lp.Cursor)
	out := []Payment{}
	err := h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+paymentCols+paymentFrom+` WHERE `+strings.Join(where, " AND ")+
			fmt.Sprintf(` ORDER BY p.created_at DESC, p.id DESC LIMIT %d OFFSET %d`, lp.Limit+1, offset), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanPayment(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, page(out, lp.Limit, offset))
}

func (h *HTTP) getPayment(w http.ResponseWriter, r *http.Request) {
	pid, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var out Payment
	err = h.Svc.DB.WithReadTx(r.Context(), func(tx pgx.Tx) error {
		out, err = GetPayment(r.Context(), tx, pid)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *HTTP) cancelPayment(w http.ResponseWriter, r *http.Request) {
	pid, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req ReasonRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	var out Payment
	err = h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var num string
		err := tx.QueryRow(ctx, `UPDATE billing.payments SET status = 'cancelled', cancelled_at = now(), cancel_reason = $2, updated_by = $3
			WHERE id = $1 AND status = 'pending' RETURNING number`, pid, req.Reason, id.Ptr(actor(ctx))).Scan(&num)
		if dbtx.IsNoRows(err) {
			return errs.Conflict("payment_not_pending", "only pending payments can be cancelled; refund completed payments instead")
		}
		if err != nil {
			return err
		}
		p := prop(ctx)
		if err := audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionStatusChange, EntityType: "billing.payment", EntityID: pid.String(),
			EntityLabel: num, PropertyID: &p, Reason: req.Reason, After: map[string]any{"status": "cancelled"}}); err != nil {
			return err
		}
		out, err = GetPayment(ctx, tx, pid)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// receiptPDF renders a printable receipt (FR-PAY-11).
func (h *HTTP) receiptPDF(ctx context.Context, tx pgx.Tx, p Payment) ([]byte, error) {
	var clubName string
	_ = tx.QueryRow(ctx, `SELECT coalesce(branding->>'appName', name) FROM platform.instance`).Scan(&clubName)
	loc, _ := org.Location(ctx, tx, prop(ctx))
	if loc == nil {
		loc = time.UTC
	}
	d := pdf.New()
	d.Row(18, true, clubName)
	d.Row(12, false, "Receipt / Bukti Bayar")
	d.Space(8)
	d.Rule(d.Y + 10)
	d.Row(10, false, "Receipt No.", p.Number)
	if p.FolioNumber != nil {
		d.Row(10, false, "Folio", *p.FolioNumber)
	}
	paid := p.CreatedAt
	if p.PaidAt != nil {
		paid = *p.PaidAt
	}
	d.Row(10, false, "Date", paid.In(loc).Format("02 Jan 2006 15:04"))
	d.Row(10, false, "Method", strings.ReplaceAll(p.MethodType, "_", " "))
	if p.Reference != nil {
		d.Row(10, false, "Reference", *p.Reference)
	}
	if p.ReceivedByName != nil {
		d.Row(10, false, "Received by", *p.ReceivedByName)
	}
	d.Space(6)
	d.Rule(d.Y + 10)
	d.Row(14, true, "Amount", p.Currency+" "+formatAmount(dec(p.Amount), p.Currency))
	if dec(p.RefundedAmount).IsPositive() {
		d.Row(10, false, "Refunded", p.Currency+" "+formatAmount(dec(p.RefundedAmount), p.Currency))
	}
	d.Row(10, false, "Status", strings.ToUpper(p.Status[:1])+p.Status[1:])
	return d.Bytes(), nil
}

// formatAmount renders 1234567 as 1.234.567 (Indonesian grouping).
func formatAmount(d decimal.Decimal, cur string) string {
	s := d.StringFixed(places(cur))
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intPart, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, c := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	out := b.String()
	if frac != "" {
		out += "," + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}

func (h *HTTP) receipt(w http.ResponseWriter, r *http.Request) {
	pid, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	var body []byte
	var num string
	err = h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		p, err := GetPayment(ctx, tx, pid)
		if err != nil {
			return err
		}
		num = p.Number
		body, err = h.receiptPDF(ctx, tx, p)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="receipt-`+num+`.pdf"`)
	_, _ = w.Write(body)
}

type SendReceiptRequest struct {
	Email string `json:"email,omitempty" doc:"Default: the folio customer's e-mail"`
	Phone string `json:"phone,omitempty" doc:"WhatsApp number; default: the folio customer's phone"`
}

// SendReceipt e-mails / WhatsApps the receipt of a completed payment.
func (s *Service) SendReceipt(ctx context.Context, tx pgx.Tx, n notify.Sender, p Payment, email, phone, name, link string) error {
	loc, _ := org.Location(ctx, tx, prop(ctx))
	if loc == nil {
		loc = time.UTC
	}
	paid := p.CreatedAt
	if p.PaidAt != nil {
		paid = *p.PaidAt
	}
	ref := ""
	if p.Reference != nil {
		ref = *p.Reference
	}
	var ch []string
	if email != "" {
		ch = append(ch, notify.ChannelEmail)
	}
	if phone != "" {
		ch = append(ch, notify.ChannelWhatsApp)
	}
	if len(ch) == 0 {
		return errs.Validation("recipient_required", "no e-mail or phone to send the receipt to", errs.Field("email", "required", "e-mail or phone"))
	}
	if err := n.Send(ctx, tx, notify.Message{Event: "billing.receipt", Category: "general", Email: email, Phone: phone, Name: name, Channels: ch,
		Data: map[string]any{"name": name, "number": p.Number, "amount": p.Currency + " " + formatAmount(dec(p.Amount), p.Currency),
			"method": strings.ReplaceAll(p.MethodType, "_", " "), "paidAt": paid.In(loc).Format("02 Jan 2006 15:04"), "reference": ref, "link": link}}); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE billing.payments SET receipt_sent_at = now() WHERE id = $1`, p.ID)
	return err
}

func (h *HTTP) sendReceipt(w http.ResponseWriter, r *http.Request) {
	pid, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req SendReceiptRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	var out Payment
	err = h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		p, err := GetPayment(ctx, tx, pid)
		if err != nil {
			return err
		}
		if p.Status != "completed" && p.Status != "refunded" {
			return errs.Conflict("payment_not_completed", "a receipt is only available for completed payments")
		}
		email, phone, name := req.Email, req.Phone, ""
		if p.FolioID != nil {
			var cust, guest *uuid.UUID
			_ = tx.QueryRow(ctx, `SELECT holder_name, customer_id, guest_id FROM billing.folios WHERE id = $1`, *p.FolioID).Scan(&name, &cust, &guest)
			_, cEmail, cPhone, err := crm.Contact(ctx, tx, cust, guest)
			if err != nil {
				return err
			}
			if email == "" && phone == "" {
				email, phone = cEmail, cPhone
			}
		}
		if err := h.Svc.SendReceipt(ctx, tx, h.Notify, p, email, phone, name, ""); err != nil {
			return err
		}
		pp := prop(ctx)
		if err := audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "send_receipt", EntityType: "billing.payment", EntityID: pid.String(),
			EntityLabel: p.Number, PropertyID: &pp, Metadata: map[string]any{"email": email != "", "whatsapp": phone != ""}}); err != nil {
			return err
		}
		out, err = GetPayment(ctx, tx, pid)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// ── refunds & deposits ────────────────────────────────────────────────────

type RefundRequest struct {
	PaymentID   uuid.UUID `json:"paymentId"`
	Amount      string    `json:"amount"`
	Destination string    `json:"destination,omitempty" enum:"original_method,member_account"`
	Reason      string    `json:"reason"`
}

func (h *HTTP) createRefund(w http.ResponseWriter, r *http.Request) {
	var req RefundRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	amt, err := decimal.NewFromString(req.Amount)
	if err != nil {
		httpx.WriteError(w, r, errs.Validation("invalid_amount", "invalid amount", errs.Field("amount", "invalid", "decimal")))
		return
	}
	ctx := r.Context()
	var out Refund
	err = h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		out, err = h.Svc.RequestRefund(ctx, tx, req.PaymentID, amt, req.Destination, req.Reason, h)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *HTTP) listRefunds(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	lp := httpx.ParseList(r)
	where := "r.property_id = $1"
	args := []any{prop(ctx)}
	if v := lp.Filters["status"]; v != "" {
		args = append(args, v)
		where += " AND r.status = $2"
	}
	out := []Refund{}
	err := h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+refundCols+` WHERE `+where+` ORDER BY r.created_at DESC LIMIT 500`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			x, err := scanRefund(rows)
			if err != nil {
				return err
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Refund]{Items: out})
}

func (h *HTTP) listDeposits(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	lp := httpx.ParseList(r)
	where := "property_id = $1"
	args := []any{prop(ctx)}
	if v := lp.Filters["status"]; v != "" {
		args = append(args, v)
		where += " AND status = $2"
	}
	out := []Deposit{}
	err := h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, number, folio_id, payment_id, amount::text, applied_amount::text, currency, status, applied_at, created_at
			FROM billing.deposits WHERE `+where+` ORDER BY created_at DESC LIMIT 500`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x Deposit
			if err := rows.Scan(&x.ID, &x.Number, &x.FolioID, &x.PaymentID, &x.Amount, &x.AppliedAmount, &x.Currency, &x.Status, &x.AppliedAt, &x.CreatedAt); err != nil {
				return err
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Deposit]{Items: out})
}

func (h *HTTP) applyDeposit(w http.ResponseWriter, r *http.Request) {
	did, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	var out FolioDetail
	err = h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var fid *uuid.UUID
		var status, num string
		if err := tx.QueryRow(ctx, `SELECT folio_id, status, number FROM billing.deposits WHERE id = $1`, did).Scan(&fid, &status, &num); err != nil {
			if dbtx.IsNoRows(err) {
				return errs.NotFound("deposit")
			}
			return err
		}
		if status != "held" || fid == nil {
			return errs.Conflict("deposit_not_held", "only held deposits can be applied")
		}
		if err := h.Svc.ApplyDeposits(ctx, tx, *fid); err != nil {
			return err
		}
		p := prop(ctx)
		if err := audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "apply_deposit", EntityType: "billing.deposit", EntityID: did.String(),
			EntityLabel: num, PropertyID: &p}); err != nil {
			return err
		}
		out, err = GetFolio(ctx, tx, *fid)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// ── customer accounts & member charges ───────────────────────────────────

// AccountEntry is one ledger entry.
type AccountEntry struct {
	ID          uuid.UUID  `json:"id"`
	AccountID   uuid.UUID  `json:"accountId"`
	EntryType   string     `json:"entryType" enum:"charge,payment,refund,adjustment,opening_balance"`
	Amount      string     `json:"amount"`
	Currency    string     `json:"currency"`
	Description string     `json:"description"`
	FolioID     *uuid.UUID `json:"folioId"`
	PaymentID   *uuid.UUID `json:"paymentId"`
	OccurredAt  time.Time  `json:"occurredAt"`
	HolderName  string     `json:"holderName"`
	AccountNo   string     `json:"accountNumber"`
}

// AccountDetail is an account with its holder and recent entries.
type AccountDetail struct {
	Account
	HolderName string         `json:"holderName"`
	Entries    []AccountEntry `json:"entries"`
}

func (h *HTTP) listAccounts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	lp := httpx.ParseList(r)
	where := []string{"a.property_id = $1"}
	args := []any{prop(ctx)}
	if v := lp.Filters["accountType"]; v != "" {
		args = append(args, v)
		where = append(where, "a.account_type = $"+strconv.Itoa(len(args)))
	}
	if v := lp.Filters["customerId"]; v != "" {
		args = append(args, v)
		where = append(where, "a.customer_id::text = $"+strconv.Itoa(len(args)))
	}
	out := []AccountDetail{}
	err := h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		if lp.Q != "" {
			ids, err := crm.SearchCustomerIDs(ctx, tx, prop(ctx), lp.Q, 200)
			if err != nil {
				return err
			}
			args = append(args, "%"+lp.Q+"%", ids)
			where = append(where, fmt.Sprintf("(a.number ILIKE $%d OR a.customer_id = ANY($%d))", len(args)-1, len(args)))
		}
		rows, err := tx.Query(ctx, `SELECT `+accountCols+` FROM billing.customer_accounts a
			WHERE `+strings.Join(where, " AND ")+` ORDER BY a.number LIMIT 500`, args...)
		if err != nil {
			return err
		}
		var ids []uuid.UUID
		for rows.Next() {
			a, err := scanAccount(rows)
			if err != nil {
				rows.Close()
				return err
			}
			out = append(out, AccountDetail{Account: a, Entries: []AccountEntry{}})
			ids = append(ids, a.CustomerID)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		names, err := crm.Names(ctx, tx, ids)
		if err != nil {
			return err
		}
		for i := range out {
			out[i].HolderName = names[out[i].CustomerID]
		}
		return nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[AccountDetail]{Items: out})
}

// AccountEntries lists ledger entries of an account (newest first).
func AccountEntries(ctx context.Context, q dbtx.Querier, aid uuid.UUID, limit int) ([]AccountEntry, error) {
	return queryEntries(ctx, q, `e.account_id = $1`, []any{aid}, limit)
}

// queryEntries lists ledger entries with the account holder name.
func queryEntries(ctx context.Context, q dbtx.Querier, where string, args []any, limit int) ([]AccountEntry, error) {
	rows, err := q.Query(ctx, `SELECT e.id, e.account_id, e.entry_type, e.amount::text, e.currency, e.description, e.folio_id, e.payment_id, e.occurred_at,
		a.customer_id, a.number FROM billing.account_entries e JOIN billing.customer_accounts a ON a.id = e.account_id
		WHERE `+where+fmt.Sprintf(` ORDER BY e.occurred_at DESC, e.id DESC LIMIT %d`, limit), args...)
	if err != nil {
		return nil, err
	}
	out := []AccountEntry{}
	var cust []uuid.UUID
	for rows.Next() {
		var e AccountEntry
		var c uuid.UUID
		if err := rows.Scan(&e.ID, &e.AccountID, &e.EntryType, &e.Amount, &e.Currency, &e.Description, &e.FolioID, &e.PaymentID, &e.OccurredAt,
			&c, &e.AccountNo); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, e)
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

func (h *HTTP) getAccount(w http.ResponseWriter, r *http.Request) {
	aid, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	var out AccountDetail
	err = h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		a, err := GetAccount(ctx, tx, aid)
		if err != nil {
			return err
		}
		out.Account = a
		names, err := crm.Names(ctx, tx, []uuid.UUID{a.CustomerID})
		if err != nil {
			return err
		}
		out.HolderName = names[a.CustomerID]
		out.Entries, err = AccountEntries(ctx, tx, aid, 500)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type OpenAccountRequest struct {
	CustomerID  uuid.UUID `json:"customerId"`
	AccountType string    `json:"accountType" enum:"member,corporate,customer"`
	CreditLimit string    `json:"creditLimit,omitempty"`
}

func (h *HTTP) openAccount(w http.ResponseWriter, r *http.Request) {
	var req OpenAccountRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if req.AccountType != "member" && req.AccountType != "corporate" && req.AccountType != "customer" {
		httpx.WriteError(w, r, errs.Validation("invalid_account_type", "invalid account type", errs.Field("accountType", "invalid", "member, corporate or customer")))
		return
	}
	ctx := r.Context()
	var out Account
	err := h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		ok, err := crm.ExistsInProperty(ctx, tx, prop(ctx), req.CustomerID)
		if err != nil {
			return err
		}
		if !ok {
			return errs.Validation("invalid_customer", "customer not found", errs.Field("customerId", "not_found", "customer not found in this property"))
		}
		a, err := h.Svc.EnsureAccount(ctx, tx, prop(ctx), req.CustomerID, req.AccountType, nil)
		if err != nil {
			return err
		}
		if req.CreditLimit != "" {
			cl, err := decimal.NewFromString(req.CreditLimit)
			if err != nil || cl.IsNegative() {
				return errs.Validation("invalid_credit_limit", "invalid credit limit", errs.Field("creditLimit", "invalid", "non-negative decimal"))
			}
			if _, err := tx.Exec(ctx, `UPDATE billing.customer_accounts SET credit_limit = $2::numeric WHERE id = $1`, a.ID, cl.String()); err != nil {
				return err
			}
			p := prop(ctx)
			if err := audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionUpdate, EntityType: "billing.customer_account",
				EntityID: a.ID.String(), EntityLabel: a.Number, PropertyID: &p, After: map[string]any{"creditLimit": cl.String()}}); err != nil {
				return err
			}
		}
		out, err = GetAccount(ctx, tx, a.ID)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

type EntryRequest struct {
	EntryType   string `json:"entryType" enum:"adjustment,opening_balance"`
	Amount      string `json:"amount" doc:"Positive = the member owes more; negative = credit"`
	Description string `json:"description"`
}

// postEntry records an adjustment or the opening balance at cutover
// (FR-MIG-06); always audited with a reason.
func (h *HTTP) postEntry(w http.ResponseWriter, r *http.Request) {
	aid, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req EntryRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if req.EntryType != "adjustment" && req.EntryType != "opening_balance" {
		httpx.WriteError(w, r, errs.Validation("invalid_entry_type", "invalid entry type", errs.Field("entryType", "invalid", "adjustment or opening_balance")))
		return
	}
	amt, err := decimal.NewFromString(req.Amount)
	if err != nil || amt.IsZero() || strings.TrimSpace(req.Description) == "" {
		httpx.WriteError(w, r, errs.Validation("invalid_entry", "amount (non-zero) and description are required", errs.Field("amount", "invalid", "non-zero decimal")))
		return
	}
	ctx := r.Context()
	var out AccountDetail
	err = h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
		a, err := GetAccount(ctx, tx, aid)
		if err != nil {
			return err
		}
		p := prop(ctx)
		if req.EntryType == "opening_balance" {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM billing.account_entries WHERE account_id = $1 AND entry_type = 'opening_balance'`, aid).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return errs.Conflict("opening_balance_exists", "the opening balance is already recorded; use an adjustment")
			}
		}
		if err := PostEntry(ctx, tx, p, aid, req.EntryType, amt, a.Currency, req.Description, nil, nil); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: req.EntryType, EntityType: "billing.customer_account", EntityID: aid.String(),
			EntityLabel: a.Number, PropertyID: &p, Reason: req.Description, After: map[string]any{"amount": amt.String()}}); err != nil {
			return err
		}
		out.Account, err = GetAccount(ctx, tx, aid)
		if err != nil {
			return err
		}
		names, err := crm.Names(ctx, tx, []uuid.UUID{a.CustomerID})
		if err != nil {
			return err
		}
		out.HolderName = names[a.CustomerID]
		out.Entries, err = AccountEntries(ctx, tx, aid, 100)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

// propertyTZ is the timezone of the property $1 (else the instance's).
const propertyTZ = `coalesce((SELECT nullif(timezone, '') FROM platform.properties WHERE id = $1), (SELECT timezone FROM platform.instance))`

// clubDay is the condition that the instant col falls on the date ? at the
// property $1: from local midnight to local midnight, not the UTC date
// (col::date in the UTC session), which starts at 07:00 at the club (WIB).
func clubDay(col string) string {
	return col + ` >= (?::date::timestamp AT TIME ZONE ` + propertyTZ + `) AND ` + col + ` < ((?::date + 1)::timestamp AT TIME ZONE ` + propertyTZ + `)`
}

func (h *HTTP) listMemberCharges(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	lp := httpx.ParseList(r)
	args := []any{prop(ctx)}
	where := "e.property_id = $1 AND e.entry_type = 'charge'"
	if v := lp.Filters["accountId"]; v != "" {
		args = append(args, v)
		where += " AND e.account_id::text = $2"
	}
	if v := r.URL.Query().Get("date"); v != "" {
		args = append(args, v)
		where += " AND " + strings.ReplaceAll(clubDay("e.occurred_at"), "?", "$"+strconv.Itoa(len(args)))
	}
	var out []AccountEntry
	err := h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = queryEntries(ctx, tx, where, args, 500)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[AccountEntry]{Items: out})
}

// ── Daily Payment Summary (FR-PAY-12) ─────────────────────────────────────

type SummaryRow struct {
	MethodType string `json:"methodType"`
	Staff      string `json:"staff"`
	Count      int    `json:"count"`
	Amount     string `json:"amount"`
	Refunded   string `json:"refunded"`
	Net        string `json:"net"`
}

type DailySummary struct {
	Date     string       `json:"date"`
	Currency string       `json:"currency"`
	ByMethod []SummaryRow `json:"byMethod"`
	ByStaff  []SummaryRow `json:"byStaff"`
	Total    string       `json:"total"`
}

// DailyPaymentSummary totals completed payments of a business date per
// method and per staff (cash control until cashier shifts in P3).
func DailyPaymentSummary(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time) (DailySummary, error) {
	loc, err := org.Location(ctx, q, property)
	if err != nil {
		return DailySummary{}, err
	}
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	to := from.AddDate(0, 0, 1)
	cur, _ := org.Currency(ctx, q)
	out := DailySummary{Date: day.Format("2006-01-02"), Currency: cur, ByMethod: []SummaryRow{}, ByStaff: []SummaryRow{}}
	total := decimal.Zero
	run := func(group string, into *[]SummaryRow, staff bool) error {
		rows, err := q.Query(ctx, `SELECT `+group+`, count(*), sum(p.amount)::text, sum(p.refunded_amount)::text
			FROM billing.payments p LEFT JOIN platform.users u ON u.id = p.received_by
			WHERE p.property_id = $1 AND p.status IN ('completed', 'refunded') AND p.paid_at >= $2 AND p.paid_at < $3
			GROUP BY 1 ORDER BY 1`, property, from, to)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sr SummaryRow
			var key, amt, ref string
			if err := rows.Scan(&key, &sr.Count, &amt, &ref); err != nil {
				return err
			}
			if staff {
				sr.Staff = key
			} else {
				sr.MethodType = key
			}
			net := dec(amt).Sub(dec(ref))
			sr.Amount, sr.Refunded, sr.Net = dec(amt).StringFixed(places(cur)), dec(ref).StringFixed(places(cur)), net.StringFixed(places(cur))
			if !staff {
				total = total.Add(net)
			}
			*into = append(*into, sr)
		}
		return rows.Err()
	}
	if err := run("p.method_type", &out.ByMethod, false); err != nil {
		return out, err
	}
	if err := run("coalesce(u.full_name, CASE p.channel WHEN 'online' THEN 'Online (gateway)' ELSE 'Member charge' END)", &out.ByStaff, true); err != nil {
		return out, err
	}
	out.Total = total.StringFixed(places(cur))
	return out, nil
}

func (h *HTTP) dailySummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	day := clock.Now()
	if v := r.URL.Query().Get("date"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			httpx.WriteError(w, r, errs.BadRequest("invalid_date", "date must be YYYY-MM-DD"))
			return
		}
		day = t
	} else {
		err := h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
			day = businessDate(ctx, tx, prop(ctx), clock.Now())
			return nil
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	var out DailySummary
	err := h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = DailyPaymentSummary(ctx, tx, prop(ctx), day)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// Register adds the billing routes.
func (h *HTTP) Register(reg *route.Registry) {
	add := func(rt route.Route) {
		rt.Module = "billing"
		rt.Scope = route.ScopeProperty
		reg.Add(rt)
	}
	const tf, tp, ta = "Folios", "Payments", "Customer Accounts"
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/billing/folios", Tag: tf, Summary: "List folios", Permission: "billing.folio.view",
		Response: Folio{}, List: true, Query: []route.Param{{Name: "q"}, {Name: "filter[status]"}, {Name: "filter[sourceType]"}, {Name: "filter[customerId]"}, {Name: "date"}},
		Handler: h.listFolios})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/billing/folios", Tag: tf, Summary: "Create a folio (walk-in)", Permission: "billing.folio.create",
		Request: CreateFolioRequest{}, Response: FolioDetail{}, Idempotent: true, Handler: h.createFolio})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/billing/folios/{id}", Tag: tf, Summary: "View a folio (ETag)", Permission: "billing.folio.view",
		Response: FolioDetail{}, Handler: h.getFolio})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/billing/folios/{id}/lines", Tag: tf, Summary: "Add Charge", Permission: "billing.folio.add_charge",
		Request: AddChargeRequest{}, Response: FolioDetail{}, Handler: h.addCharge})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/billing/folios/{id}/lines/{lineId}:void", Tag: tf, Summary: "Void a charge (reason required)",
		Permission: "billing.folio.void", Request: ReasonRequest{}, Response: FolioDetail{}, Status: http.StatusOK, Handler: h.voidLine})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/billing/folios/{id}:close", Tag: tf, Summary: "Close Folio (final settlement, If-Match)",
		Permission: "billing.folio.close", Response: FolioDetail{}, Status: http.StatusOK, Handler: h.closeFolio})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/billing/folios/{id}:reopen", Tag: tf, Summary: "Reopen Folio (permission + reason, audited)",
		Permission: "billing.folio.reopen", Request: ReasonRequest{}, Response: FolioDetail{}, Status: http.StatusOK, Handler: h.reopenFolio})

	add(route.Route{Method: http.MethodGet, Path: "/api/v1/billing/payments", Tag: tp, Summary: "Payment History", Permission: "billing.payment.view",
		Response: Payment{}, List: true, Query: []route.Param{{Name: "q"}, {Name: "filter[status]"}, {Name: "filter[methodType]"}, {Name: "filter[channel]"},
			{Name: "filter[folioId]"}, {Name: "filter[accountId]"}, {Name: "date"}}, Handler: h.listPayments})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/billing/payments", Tag: tp, Summary: "Process Payment (venue, online or member charge)",
		Permission: "billing.payment.create", Request: PaymentRequest{}, Response: Payment{}, Idempotent: true, Handler: h.createPayment})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/billing/payments/{id}", Tag: tp, Summary: "View a payment", Permission: "billing.payment.view",
		Response: Payment{}, Handler: h.getPayment})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/billing/payments/{id}:cancel", Tag: tp, Summary: "Cancel a pending payment",
		Permission: "billing.payment.create", Request: ReasonRequest{}, Response: Payment{}, Status: http.StatusOK, Handler: h.cancelPayment})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/billing/payments/{id}/receipt", Tag: tp, Summary: "Printable receipt (PDF)",
		Permission: "billing.payment.view", RawContent: "application/pdf", Handler: h.receipt})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/billing/payments/{id}:send-receipt", Tag: tp, Summary: "Send the receipt by e-mail / WhatsApp",
		Permission: "billing.payment.view", Request: SendReceiptRequest{}, Response: Payment{}, Status: http.StatusOK, Handler: h.sendReceipt})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/billing/daily-payment-summary", Tag: tp, Summary: "Daily Payment Summary per method and staff",
		Permission: "billing.payment.view", Response: DailySummary{}, Query: []route.Param{{Name: "date"}}, Handler: h.dailySummary})

	add(route.Route{Method: http.MethodGet, Path: "/api/v1/billing/refunds", Tag: tp, Summary: "List refunds", Permission: "billing.refund.view",
		Response: Refund{}, List: true, Query: []route.Param{{Name: "filter[status]"}}, Handler: h.listRefunds})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/billing/refunds", Tag: tp, Summary: "Process Refund (approval above the Refund Policy limit)",
		Permission: "billing.refund.create", Request: RefundRequest{}, Response: Refund{}, Idempotent: true, Handler: h.createRefund})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/billing/deposits", Tag: tp, Summary: "List deposits", Permission: "billing.payment.view",
		Response: Deposit{}, List: true, Query: []route.Param{{Name: "filter[status]"}}, Handler: h.listDeposits})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/billing/deposits/{id}:apply", Tag: tp, Summary: "Apply Deposit to the folio",
		Permission: "billing.payment.create", Response: FolioDetail{}, Status: http.StatusOK, Handler: h.applyDeposit})

	add(route.Route{Method: http.MethodGet, Path: "/api/v1/billing/customer-accounts", Tag: ta, Summary: "List customer accounts with balance",
		Permission: "billing.customer_account.view", Response: AccountDetail{}, List: true,
		Query: []route.Param{{Name: "q"}, {Name: "filter[accountType]"}, {Name: "filter[customerId]"}}, Handler: h.listAccounts})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/billing/customer-accounts", Tag: ta, Summary: "Open a customer account",
		Permission: "billing.customer_account.manage", Request: OpenAccountRequest{}, Response: Account{}, Handler: h.openAccount})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/billing/customer-accounts/{id}", Tag: ta, Summary: "View an account with its ledger",
		Permission: "billing.customer_account.view", Response: AccountDetail{}, Handler: h.getAccount})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/billing/customer-accounts/{id}/entries", Tag: ta, Summary: "Post an adjustment or the opening balance",
		Permission: "billing.customer_account.adjust", Request: EntryRequest{}, Response: AccountDetail{}, Handler: h.postEntry})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/billing/member-charges", Tag: ta, Summary: "Member Charges", Permission: "billing.customer_account.view",
		Response: AccountEntry{}, List: true, Query: []route.Param{{Name: "filter[accountId]"}, {Name: "date"}}, Handler: h.listMemberCharges})
	h.registerFinance(reg)
}
