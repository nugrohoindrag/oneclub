package billing

// Member Portal transactions (FR-APP-08): My Transactions, Payments, Member
// Charges and Membership Statement.

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
)

// HolderResolver returns the customer whose member account pays for a
// portal user (the principal of a family membership); nil = own profile.
type HolderResolver func(ctx context.Context, tx pgx.Tx, userID uuid.UUID) (*uuid.UUID, error)

type portalCtx struct {
	customer uuid.UUID
	holder   uuid.UUID
	property uuid.UUID
	account  *Account
}

func (h *HTTP) portalOf(ctx context.Context, tx pgx.Tx) (context.Context, portalCtx, error) {
	var pc portalCtx
	p := authz.From(ctx)
	if p == nil {
		return ctx, pc, errs.Unauthorized("authentication required")
	}
	c, err := crm.CustomerByUser(ctx, tx, p.UserID)
	if err != nil {
		return ctx, pc, err
	}
	pc.customer, pc.holder, pc.property = c.ID, c.ID, c.PropertyID
	if h.Holder != nil {
		if hc, err := h.Holder(ctx, tx, p.UserID); err == nil && hc != nil {
			pc.holder = *hc
		}
	}
	ctx = reqctx.WithProperty(ctx, c.PropertyID)
	pc.account, err = AccountFor(ctx, tx, c.PropertyID, pc.holder, "member")
	return ctx, pc, err
}

func (h *HTTP) portalRead(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, tx pgx.Tx, pc portalCtx) (any, error)) {
	ctx := r.Context()
	var out any
	err := h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		ctx, pc, err := h.portalOf(ctx, tx)
		if err != nil {
			return err
		}
		out, err = fn(ctx, tx, pc)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// MyTransactions lists the member's folios with their lines and payments.
type MyTransactions struct {
	Account *Account      `json:"account"`
	Folios  []FolioDetail `json:"folios"`
}

func (h *HTTP) myTransactions(w http.ResponseWriter, r *http.Request) {
	h.portalRead(w, r, func(ctx context.Context, tx pgx.Tx, pc portalCtx) (any, error) {
		out := MyTransactions{Account: pc.account, Folios: []FolioDetail{}}
		rows, err := tx.Query(ctx, `SELECT id FROM billing.folios WHERE customer_id = ANY($1) ORDER BY created_at DESC LIMIT 100`, []uuid.UUID{pc.customer, pc.holder})
		if err != nil {
			return nil, err
		}
		var ids []uuid.UUID
		for rows.Next() {
			var x uuid.UUID
			if err := rows.Scan(&x); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, x)
		}
		rows.Close()
		for _, x := range ids {
			f, err := GetFolio(ctx, tx, x)
			if err != nil {
				return nil, err
			}
			out.Folios = append(out.Folios, f)
		}
		return out, nil
	})
}

func (h *HTTP) myPayments(w http.ResponseWriter, r *http.Request) {
	h.portalRead(w, r, func(ctx context.Context, tx pgx.Tx, pc portalCtx) (any, error) {
		var acct *uuid.UUID
		if pc.account != nil {
			acct = &pc.account.ID
		}
		rows, err := tx.Query(ctx, `SELECT `+paymentCols+paymentFrom+` WHERE (f.customer_id = ANY($1) OR p.account_id = $2)
			AND p.status <> 'cancelled' ORDER BY p.created_at DESC LIMIT 200`, []uuid.UUID{pc.customer, pc.holder}, acct)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []Payment{}
		for rows.Next() {
			p, err := scanPayment(rows)
			if err != nil {
				return nil, err
			}
			out = append(out, p)
		}
		return httpx.Page[Payment]{Items: out}, rows.Err()
	})
}

func (h *HTTP) myCharges(w http.ResponseWriter, r *http.Request) {
	h.portalRead(w, r, func(ctx context.Context, tx pgx.Tx, pc portalCtx) (any, error) {
		if pc.account == nil {
			return httpx.Page[AccountEntry]{Items: []AccountEntry{}}, nil
		}
		out, err := AccountEntries(ctx, tx, pc.account.ID, 300)
		return httpx.Page[AccountEntry]{Items: out}, err
	})
}

func (h *HTTP) myStatements(w http.ResponseWriter, r *http.Request) {
	h.portalRead(w, r, func(ctx context.Context, tx pgx.Tx, pc portalCtx) (any, error) {
		if pc.account == nil {
			return httpx.Page[Statement]{Items: []Statement{}}, nil
		}
		out, err := ListStatements(ctx, tx, `s.account_id = $1`, pc.account.ID)
		return httpx.Page[Statement]{Items: out}, err
	})
}

func (h *HTTP) myStatementPDF(w http.ResponseWriter, r *http.Request) {
	sid, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	err = h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		ctx, pc, err := h.portalOf(ctx, tx)
		if err != nil {
			return err
		}
		if pc.account == nil {
			return errs.NotFound("statement")
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM billing.member_statements WHERE id = $1 AND account_id = $2)`, sid, pc.account.ID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errs.NotFound("statement")
		}
		return StatementPDF(ctx, tx, h.Files, sid, w)
	})
	if err != nil {
		httpx.WriteError(w, r, err)
	}
}

func (h *HTTP) myReceipt(w http.ResponseWriter, r *http.Request) {
	pid, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	var body []byte
	var num string
	err = h.Svc.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		ctx, pc, err := h.portalOf(ctx, tx)
		if err != nil {
			return err
		}
		p, err := GetPayment(ctx, tx, pid)
		if err != nil {
			return err
		}
		owned := p.AccountID != nil && pc.account != nil && *p.AccountID == pc.account.ID
		if p.FolioID != nil && !owned {
			var cust *uuid.UUID
			_ = tx.QueryRow(ctx, `SELECT customer_id FROM billing.folios WHERE id = $1`, *p.FolioID).Scan(&cust)
			owned = cust != nil && (*cust == pc.customer || *cust == pc.holder)
		}
		if !owned {
			return errs.NotFound("payment")
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

// RegisterMember adds the Member Portal billing routes.
func (h *HTTP) RegisterMember(reg *route.Registry) {
	add := func(rt route.Route) {
		rt.Module, rt.Tag, rt.Permission = "billing", "Member Portal", catalog.ShellMemberPortal
		reg.Add(rt)
	}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/member/transactions", Summary: "My Transactions (folios, charges, payments)",
		Response: MyTransactions{}, Handler: h.myTransactions})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/member/payments", Summary: "My Payments", Response: Payment{}, List: true, Handler: h.myPayments})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/member/payments/{id}/receipt", Summary: "My receipt (PDF)", RawContent: "application/pdf", Handler: h.myReceipt})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/member/member-charges", Summary: "My Member Charges (member account ledger)",
		Response: AccountEntry{}, List: true, Handler: h.myCharges})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/member/statements", Summary: "My Membership Statements", Response: Statement{}, List: true, Handler: h.myStatements})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/member/statements/{id}/pdf", Summary: "Membership Statement PDF", RawContent: "application/pdf",
		Handler: h.myStatementPDF})
}
