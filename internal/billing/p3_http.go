package billing

// Routes, permissions, templates and jobs of PRD P3 EP-17 / EP-18 (API
// surface PRD P3 §11: customer folios, payment schedules, invoices, credit
// notes, allocations, ageing, corporate statements, cashier shifts,
// business days, night audit and the Daily Revenue Report).

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/provision"
)

// P3DocumentTypes are the approval document types of P3 billing.
var P3DocumentTypes = []provision.DocumentType{WriteOffDocumentType, CreditOverrideDocumentType, ReopenDocumentType}

func (h *HTTP) publicBase() string {
	if h.WebsiteURL != nil {
		return h.WebsiteURL() + "/id"
	}
	return ""
}

// sendInvoice e-mails / WhatsApps an invoice with its payment link.
func (h *HTTP) sendInvoice(ctx context.Context, tx pgx.Tx, property uuid.UUID, inv Invoice, event string, extra map[string]any) error {
	if h.Notify == nil {
		return nil
	}
	d, err := GetInvoice(ctx, tx, inv.ID, h.publicBase())
	if err != nil {
		return err
	}
	data := map[string]any{"number": deref(d.Number), "billTo": d.BillToName, "total": formatAmount(dec(d.Total), d.Currency),
		"outstanding": formatAmount(dec(d.Outstanding), d.Currency), "currency": d.Currency, "dueDate": deref(d.DueDate), "link": deref(d.PayLink)}
	for k, v := range extra {
		data[k] = v
	}
	msg := notify.Message{Event: event, Category: "billing", PropertyID: &property, Data: data}
	ok := false
	if d.CustomerID != nil && d.CorporateAccountID == nil {
		if ok, err = crm.Recipient(ctx, tx, *d.CustomerID, &msg); err != nil {
			return err
		}
	}
	if !ok && d.BillToEmail != nil && *d.BillToEmail != "" {
		msg.Email, msg.Name, msg.Channels, ok = *d.BillToEmail, d.BillToName, []string{notify.ChannelEmail}, true
	}
	if !ok {
		return nil
	}
	return h.Notify.Send(ctx, tx, msg)
}

// SendInput chooses the channels of an invoice / statement.
type SendInput struct {
	Email string `json:"email,omitempty" doc:"Override the bill-to e-mail"`
}

// RegisterP3 adds the PRD P3 billing routes.
func (h *HTTP) RegisterP3(reg *route.Registry) {
	s, db := h.Svc, h.Svc.DB
	add := func(tag string, rt route.Route) {
		rt.Module, rt.Tag, rt.Scope = "billing", tag, route.ScopeProperty
		reg.Add(rt)
	}
	const tcf, tps, tin, tsh, tnd = "Customer Folios", "Payment Schedules", "Invoices", "Cashier Shifts", "Night Audit"

	// ── customer folios ─────────────────────────────────────────────────
	add(tcf, route.Route{Method: http.MethodGet, Path: "/api/v1/billing/customer-folios", Summary: "Customer folios (one per customer or company, across lines)",
		Permission: "billing.customer_folio.view", Response: CustomerFolio{}, List: true,
		Query: []route.Param{{Name: "q"}, {Name: "filter[status]"}, {Name: "filter[customerId]"}, {Name: "filter[corporateAccountId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CustomerFolio], error) {
			lp := httpx.ParseList(r)
			items, err := handle.List[CustomerFolio](tx.Query(ctx, customerFolioSelect+` WHERE c.property_id = $1 AND ($2 = '' OR c.status = $2)
				AND ($3 = '' OR c.customer_id::text = $3) AND ($4 = '' OR c.corporate_account_id::text = $4)
				AND ($5 = '' OR c.number ILIKE '%' || $5 || '%' OR c.holder_name ILIKE '%' || $5 || '%') ORDER BY c.created_at DESC LIMIT $6`,
				handle.Property(ctx), lp.Filters["status"], lp.Filters["customerId"], lp.Filters["corporateAccountId"], lp.Q, lp.Limit))
			for i := range items {
				fixCustomerFolio(&items[i])
			}
			return handle.Page(items, err)
		})})
	add(tcf, route.Route{Method: http.MethodPost, Path: "/api/v1/billing/customer-folios", Summary: "Open (or return) the customer folio of a customer or company",
		Permission: "billing.customer_folio.manage", Request: CustomerFolioInput{}, Response: CustomerFolioDetail{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CustomerFolioInput) (CustomerFolioDetail, error) {
			return s.EnsureCustomerFolio(ctx, tx, handle.Property(ctx), in)
		})})
	add(tcf, route.Route{Method: http.MethodGet, Path: "/api/v1/billing/customer-folios/{id}", Summary: "Customer folio with its folios per business line",
		Permission: "billing.customer_folio.view", Response: CustomerFolioDetail{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CustomerFolioDetail, error) {
			cid, err := handle.ID(r)
			if err != nil {
				return CustomerFolioDetail{}, err
			}
			return GetCustomerFolio(ctx, tx, cid)
		})})
	add(tcf, route.Route{Method: http.MethodPost, Path: "/api/v1/billing/customer-folios/{id}:merge", Summary: "Merge folios of any line into the customer folio",
		Permission: "billing.customer_folio.manage", Request: MergeInput{}, Response: CustomerFolioDetail{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MergeInput) (CustomerFolioDetail, error) {
			cid, err := handle.ID(r)
			if err != nil {
				return CustomerFolioDetail{}, err
			}
			return s.MergeFolios(ctx, tx, cid, in)
		})})
	add(tcf, route.Route{Method: http.MethodPost, Path: "/api/v1/billing/customer-folios:split", Summary: "Split Bill across lines: move charges to another payer",
		Permission: "billing.customer_folio.split", Request: SplitInput{}, Response: SplitResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in SplitInput) (SplitResult, error) {
			return s.SplitCharges(ctx, tx, handle.Property(ctx), in)
		})})
	add(tcf, route.Route{Method: http.MethodPost, Path: "/api/v1/billing/folios/{id}:charge-to-account", Summary: "Charge the folio balance to a corporate account (city ledger)",
		Permission: "billing.customer_account.charge", Request: ChargeToAccountInput{}, Response: Payment{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ChargeToAccountInput) (Payment, error) {
			fid, err := handle.ID(r)
			if err != nil {
				return Payment{}, err
			}
			return s.ChargeToAccount(ctx, tx, handle.Property(ctx), fid, in)
		})})
	add(tcf, route.Route{Method: http.MethodPost, Path: "/api/v1/billing/customer-accounts/{id}:credit-override", Summary: "Request a credit override (approval)",
		Permission: "billing.credit_override.request", Request: CreditOverrideInput{}, Response: CreditOverride{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CreditOverrideInput) (CreditOverride, error) {
			aid, err := handle.ID(r)
			if err != nil {
				return CreditOverride{}, err
			}
			return h.RequestCreditOverride(ctx, tx, handle.Property(ctx), aid, in)
		})})
	add(tcf, route.Route{Method: http.MethodGet, Path: "/api/v1/billing/credit-overrides", Summary: "Credit overrides", Permission: "billing.customer_account.view",
		Response: CreditOverride{}, List: true, Query: []route.Param{{Name: "filter[accountId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CreditOverride], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[CreditOverride](tx.Query(ctx, `SELECT id, account_id, trim_scale(amount)::text AS amount, reason, expires_at, status,
				approval_request_id, created_at FROM billing.credit_overrides WHERE property_id = $1 AND ($2 = '' OR account_id::text = $2)
				ORDER BY created_at DESC LIMIT $3`, handle.Property(ctx), lp.Filters["accountId"], lp.Limit)))
		})})

	// ── payment schedules ──────────────────────────────────────────────
	add(tps, route.Route{Method: http.MethodGet, Path: "/api/v1/billing/payment-schedules", Summary: "Payment schedules (DP, installments, final payment)",
		Permission: "billing.payment_schedule.view", Response: Schedule{}, List: true,
		Query: []route.Param{{Name: "filter[status]"}, {Name: "filter[sourceType]"}, {Name: "filter[customerId]"}, {Name: "q"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Schedule], error) {
			lp := httpx.ParseList(r)
			items, err := handle.List[Schedule](tx.Query(ctx, scheduleSelect+` WHERE s.property_id = $1 AND ($2 = '' OR s.status = $2)
				AND ($3 = '' OR s.source_type = $3) AND ($4 = '' OR s.customer_id::text = $4)
				AND ($5 = '' OR s.number ILIKE '%' || $5 || '%' OR s.title ILIKE '%' || $5 || '%') ORDER BY s.created_at DESC LIMIT $6`,
				handle.Property(ctx), lp.Filters["status"], lp.Filters["sourceType"], lp.Filters["customerId"], lp.Q, lp.Limit))
			if err != nil {
				return httpx.Page[Schedule]{}, err
			}
			for i := range items {
				full, err := GetSchedule(ctx, tx, items[i].ID)
				if err != nil {
					return httpx.Page[Schedule]{}, err
				}
				items[i].Lines = full.Lines
			}
			return handle.Page(items, nil)
		})})
	add(tps, route.Route{Method: http.MethodPost, Path: "/api/v1/billing/payment-schedules", Summary: "Create a payment schedule or an installment plan",
		Permission: "billing.payment_schedule.manage", Request: ScheduleInput{}, Response: Schedule{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ScheduleInput) (Schedule, error) {
			return s.CreateSchedule(ctx, tx, handle.Property(ctx), in)
		})})
	add(tps, route.Route{Method: http.MethodGet, Path: "/api/v1/billing/payment-schedules/{id}", Summary: "Payment schedule with its lines",
		Permission: "billing.payment_schedule.view", Response: Schedule{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Schedule, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return Schedule{}, err
			}
			return GetSchedule(ctx, tx, sid)
		})})
	add(tps, route.Route{Method: http.MethodPost, Path: "/api/v1/billing/payment-schedules/{id}:cancel", Summary: "Cancel the unpaid lines of a schedule",
		Permission: "billing.payment_schedule.manage", Request: ReasonRequest{}, Response: Schedule{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReasonRequest) (Schedule, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return Schedule{}, err
			}
			return s.CancelSchedule(ctx, tx, sid, in.Reason)
		})})
	add(tps, route.Route{Method: http.MethodPost, Path: "/api/v1/billing/payment-schedules/{id}/lines/{lineId}:pay", Summary: "Pay a schedule line (venue or online)",
		Permission: "billing.payment.create", Request: PayScheduleLineInput{}, Response: Payment{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PayScheduleLineInput) (Payment, error) {
			lid, err := handle.ID(r, "lineId")
			if err != nil {
				return Payment{}, err
			}
			return h.PayScheduleLine(ctx, tx, lid, in)
		})})
	add(tps, route.Route{Method: http.MethodPost, Path: "/api/v1/billing/payment-schedules/{id}/lines/{lineId}:invoice", Summary: "Issue the invoice of a schedule line (DP invoice with payment link)",
		Permission: "billing.invoice.create", Response: InvoiceDetail{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (InvoiceDetail, error) {
			lid, err := handle.ID(r, "lineId")
			if err != nil {
				return InvoiceDetail{}, err
			}
			return h.CreateInvoice(ctx, tx, handle.Property(ctx), InvoiceInput{ScheduleLineID: &lid, Issue: true})
		})})

	// ── invoices ───────────────────────────────────────────────────────
	add(tin, route.Route{Method: http.MethodGet, Path: "/api/v1/billing/invoices", Summary: "Invoices", Permission: "billing.invoice.view", Response: Invoice{},
		List: true, Query: []route.Param{{Name: "q"}, {Name: "filter[status]"}, {Name: "filter[kind]"}, {Name: "filter[accountId]"},
			{Name: "filter[corporateAccountId]"}, {Name: "filter[customerId]"}, {Name: "from"}, {Name: "to"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Invoice], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[Invoice](tx.Query(ctx, invoiceSelect+` WHERE i.property_id = $1
				AND ($2 = '' OR i.status = ANY(string_to_array($2, ','))) AND ($3 = '' OR i.kind = $3) AND ($4 = '' OR i.account_id::text = $4)
				AND ($5 = '' OR i.corporate_account_id::text = $5) AND ($6 = '' OR i.customer_id::text = $6)
				AND ($7 = '' OR i.number ILIKE '%' || $7 || '%' OR i.bill_to_name ILIKE '%' || $7 || '%')
				AND ($8 = '' OR i.issue_date >= $8::date) AND ($9 = '' OR i.issue_date <= $9::date)
				ORDER BY i.created_at DESC LIMIT $10`, handle.Property(ctx), lp.Filters["status"], lp.Filters["kind"], lp.Filters["accountId"],
				lp.Filters["corporateAccountId"], lp.Filters["customerId"], lp.Q, r.URL.Query().Get("from"), r.URL.Query().Get("to"), lp.Limit)))
		})})
	add(tin, route.Route{Method: http.MethodPost, Path: "/api/v1/billing/invoices", Summary: "Generate Invoice from a folio, customer folio, account or schedule line",
		Permission: "billing.invoice.create", Request: InvoiceInput{}, Response: InvoiceDetail{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in InvoiceInput) (InvoiceDetail, error) {
			return h.CreateInvoice(ctx, tx, handle.Property(ctx), in)
		})})
	add(tin, route.Route{Method: http.MethodGet, Path: "/api/v1/billing/invoices/{id}", Summary: "Invoice with lines, allocations, credit notes and write-offs",
		Permission: "billing.invoice.view", Response: InvoiceDetail{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (InvoiceDetail, error) {
			iid, err := handle.ID(r)
			if err != nil {
				return InvoiceDetail{}, err
			}
			return GetInvoice(ctx, tx, iid, h.publicBase())
		})})
	invAction := func(path, summary, perm string, req any, fn func(ctx context.Context, tx pgx.Tx, iid uuid.UUID, r *http.Request) (any, error), res any) {
		add(tin, route.Route{Method: http.MethodPost, Path: "/api/v1/billing/invoices/{id}" + path, Summary: summary, Permission: perm, Request: req,
			Response: res, Status: http.StatusOK, Handler: func(w http.ResponseWriter, r *http.Request) {
				iid, err := handle.ID(r)
				if err != nil {
					httpx.WriteError(w, r, err)
					return
				}
				ctx := r.Context()
				var out any
				err = db.WithTx(ctx, func(tx pgx.Tx) error {
					var err error
					out, err = fn(ctx, tx, iid, r)
					return err
				})
				if err != nil {
					httpx.WriteError(w, r, err)
					return
				}
				httpx.JSON(w, http.StatusOK, out)
			}})
	}
	invAction(":issue", "Issue the invoice (number, due date, AR transfer)", "billing.invoice.issue", nil,
		func(ctx context.Context, tx pgx.Tx, iid uuid.UUID, r *http.Request) (any, error) {
			return h.IssueInvoice(ctx, tx, iid)
		}, InvoiceDetail{})
	invAction(":send", "Send the invoice with its payment link", "billing.invoice.issue", SendInput{},
		func(ctx context.Context, tx pgx.Tx, iid uuid.UUID, r *http.Request) (any, error) {
			var in SendInput
			if err := httpx.Decode(r, &in); err != nil {
				return nil, err
			}
			inv, err := lockInvoice(ctx, tx, iid)
			if err != nil {
				return nil, err
			}
			if inv.Status == "draft" || inv.Status == "void" {
				return nil, errs.Conflict("invoice_not_open", "issue the invoice before sending it")
			}
			if in.Email != "" {
				inv.BillToEmail = &in.Email
				if _, err := tx.Exec(ctx, `UPDATE billing.invoices SET bill_to_email = $2 WHERE id = $1`, iid, in.Email); err != nil {
					return nil, err
				}
			}
			p := handle.Property(ctx)
			if err := h.sendInvoice(ctx, tx, p, inv, "billing.invoice", nil); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE billing.invoices SET sent_at = now() WHERE id = $1`, iid); err != nil {
				return nil, err
			}
			if err := audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "send", EntityType: "billing.invoice", EntityID: iid.String(),
				EntityLabel: deref(inv.Number), PropertyID: &p}); err != nil {
				return nil, err
			}
			return GetInvoice(ctx, tx, iid, h.publicBase())
		}, InvoiceDetail{})
	invAction(":void", "Void an invoice without payments", "billing.invoice.void", ReasonRequest{},
		func(ctx context.Context, tx pgx.Tx, iid uuid.UUID, r *http.Request) (any, error) {
			var in ReasonRequest
			if err := httpx.Decode(r, &in); err != nil {
				return nil, err
			}
			return s.VoidInvoice(ctx, tx, iid, in.Reason)
		}, Invoice{})
	invAction(":write-off", "Write off (part of) an invoice (approval)", "billing.invoice.write_off", WriteOffInput{},
		func(ctx context.Context, tx pgx.Tx, iid uuid.UUID, r *http.Request) (any, error) {
			var in WriteOffInput
			if err := httpx.Decode(r, &in); err != nil {
				return nil, err
			}
			return h.RequestWriteOff(ctx, tx, handle.Property(ctx), iid, in)
		}, WriteOff{})
	invAction(":pay", "Take a payment for the invoice (venue or payment link)", "billing.payment.create", PayInvoiceInput{},
		func(ctx context.Context, tx pgx.Tx, iid uuid.UUID, r *http.Request) (any, error) {
			var in PayInvoiceInput
			if err := httpx.Decode(r, &in); err != nil {
				return nil, err
			}
			return h.PayInvoice(ctx, tx, iid, in)
		}, Payment{})
	add(tin, route.Route{Method: http.MethodGet, Path: "/api/v1/billing/invoices/{id}/pdf", Summary: "Invoice PDF", Permission: "billing.invoice.view",
		RawContent: "application/pdf", Handler: func(w http.ResponseWriter, r *http.Request) {
			iid, err := handle.ID(r)
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			var b []byte
			err = db.WithReadTx(r.Context(), func(tx pgx.Tx) error {
				d, err := GetInvoice(r.Context(), tx, iid, "")
				if err != nil {
					return err
				}
				b, err = InvoicePDF(r.Context(), tx, d)
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write(b)
		}})
	add(tin, route.Route{Method: http.MethodGet, Path: "/api/v1/billing/credit-notes", Summary: "Credit notes", Permission: "billing.invoice.view",
		Response: CreditNote{}, List: true, Query: []route.Param{{Name: "filter[invoiceId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CreditNote], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[CreditNote](tx.Query(ctx, `SELECT id, number, invoice_id, trim_scale(amount)::text AS amount, currency, reason, status,
				created_at FROM billing.credit_notes WHERE property_id = $1 AND ($2 = '' OR invoice_id::text = $2) ORDER BY created_at DESC LIMIT $3`,
				handle.Property(ctx), lp.Filters["invoiceId"], lp.Limit)))
		})})
	add(tin, route.Route{Method: http.MethodPost, Path: "/api/v1/billing/credit-notes", Summary: "Issue a credit note", Permission: "billing.invoice.credit",
		Request: CreditNoteInput{}, Response: CreditNote{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CreditNoteInput) (CreditNote, error) {
			return s.IssueCreditNote(ctx, tx, handle.Property(ctx), in)
		})})
	add(tin, route.Route{Method: http.MethodGet, Path: "/api/v1/billing/payment-allocations", Summary: "Payment allocations", Permission: "billing.invoice.view",
		Response: Allocation{}, List: true, Query: []route.Param{{Name: "filter[paymentId]"}, {Name: "filter[invoiceId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Allocation], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[Allocation](tx.Query(ctx, `SELECT a.id, a.payment_id, p.number AS payment_number, a.invoice_id, a.schedule_line_id,
				trim_scale(a.amount)::text AS amount, a.created_at FROM billing.payment_allocations a JOIN billing.payments p ON p.id = a.payment_id
				WHERE a.property_id = $1 AND ($2 = '' OR a.payment_id::text = $2) AND ($3 = '' OR a.invoice_id::text = $3) ORDER BY a.created_at DESC LIMIT $4`,
				handle.Property(ctx), lp.Filters["paymentId"], lp.Filters["invoiceId"], lp.Limit)))
		})})
	add(tin, route.Route{Method: http.MethodPost, Path: "/api/v1/billing/payment-allocations", Summary: "Allocate a payment to invoices",
		Permission: "billing.invoice.allocate", Request: AllocationInput{}, Response: Allocation{}, List: true, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in AllocationInput) (httpx.Page[Allocation], error) {
			return handle.Page(s.AllocatePayment(ctx, tx, handle.Property(ctx), in))
		})})
	add(tin, route.Route{Method: http.MethodGet, Path: "/api/v1/billing/aging", Summary: "Receivable ageing (0–30, 31–60, 61–90, > 90 days)",
		Permission: "billing.invoice.view", Response: Aging{}, Query: []route.Param{{Name: "asOf", Description: "YYYY-MM-DD"}, {Name: "accountId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Aging, error) {
			p := handle.Property(ctx)
			asOf, err := handle.QueryDate(r, "asOf", localToday(ctx, tx, p, clock.Now()))
			if err != nil {
				return Aging{}, err
			}
			acct, err := handle.QueryUUID(r, "accountId")
			if err != nil {
				return Aging{}, err
			}
			return AgingAsOf(ctx, tx, p, asOf, acct)
		})})
	statement := func(ctx context.Context, tx pgx.Tx, r *http.Request) (AccountStatement, error) {
		cid, err := handle.ID(r)
		if err != nil {
			return AccountStatement{}, err
		}
		p := handle.Property(ctx)
		var aid uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM billing.customer_accounts WHERE property_id = $1 AND corporate_account_id = $2 AND account_type = 'corporate'`,
			p, cid).Scan(&aid); err != nil {
			if dbtx.IsNoRows(err) {
				return AccountStatement{}, errs.NotFound("corporate billing account")
			}
			return AccountStatement{}, err
		}
		today := localToday(ctx, tx, p, clock.Now())
		from, err := handle.QueryDate(r, "from", time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC))
		if err != nil {
			return AccountStatement{}, err
		}
		to, err := handle.QueryDate(r, "to", today)
		if err != nil {
			return AccountStatement{}, err
		}
		return AccountStatementOf(ctx, tx, p, aid, from, to)
	}
	add(tin, route.Route{Method: http.MethodGet, Path: "/api/v1/billing/corporate-accounts/{id}/statement", Summary: "Corporate statement (charges, payments, open invoices, ageing)",
		Permission: "billing.invoice.view", Response: AccountStatement{}, Query: []route.Param{{Name: "from"}, {Name: "to"}},
		Handler: handle.Read(db, statement)})
	add(tin, route.Route{Method: http.MethodPost, Path: "/api/v1/billing/corporate-accounts/{id}/statement:send", Summary: "Send the corporate statement by e-mail",
		Permission: "billing.invoice.issue", Request: SendInput{}, Response: AccountStatement{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in SendInput) (AccountStatement, error) {
			st, err := statement(ctx, tx, r)
			if err != nil {
				return st, err
			}
			cid, _ := handle.ID(r)
			return st, h.sendStatement(ctx, tx, handle.Property(ctx), cid, st, in.Email)
		})})

	// ── cashier shifts ─────────────────────────────────────────────────
	add(tsh, route.Route{Method: http.MethodGet, Path: "/api/v1/billing/cashier-shifts", Summary: "Cashier shifts (mine; all with permission)",
		Permission: "billing.cashier_shift.view", Response: CashierShift{}, List: true, Query: []route.Param{{Name: "filter[status]"}, {Name: "date"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CashierShift], error) {
			lp := httpx.ParseList(r)
			all := hasPerm(ctx, "billing.cashier_shift.view_all")
			return handle.Page(handle.List[CashierShift](tx.Query(ctx, shiftSelect+` WHERE property_id = $1 AND ($2 OR cashier_id = $3)
				AND ($4 = '' OR status = $4) AND ($5 = '' OR business_date = $5::date) ORDER BY opened_at DESC LIMIT $6`, handle.Property(ctx), all,
				handle.UserID(ctx), lp.Filters["status"], r.URL.Query().Get("date"), lp.Limit)))
		})})
	add(tsh, route.Route{Method: http.MethodPost, Path: "/api/v1/billing/cashier-shifts", Summary: "Open Shift (opening float)", Permission: "billing.cashier_shift.operate",
		Request: OpenShiftInput{}, Response: CashierShift{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in OpenShiftInput) (CashierShift, error) {
			return s.OpenCashierShift(ctx, tx, handle.Property(ctx), in)
		})})
	add(tsh, route.Route{Method: http.MethodGet, Path: "/api/v1/billing/cashier-shifts/current", Summary: "My open cashier shift",
		Permission: "billing.cashier_shift.operate", Response: CashierShift{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CashierShift, error) {
			var sid uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT id FROM billing.cashier_shifts WHERE property_id = $1 AND cashier_id = $2 AND status = 'open'`,
				handle.Property(ctx), handle.UserID(ctx)).Scan(&sid); err != nil {
				if dbtx.IsNoRows(err) {
					return CashierShift{}, errs.NotFound("open cashier shift")
				}
				return CashierShift{}, err
			}
			return GetCashierShift(ctx, tx, sid)
		})})
	add(tsh, route.Route{Method: http.MethodGet, Path: "/api/v1/billing/cashier-shifts/{id}", Summary: "Cashier shift with payments per method and expected cash",
		Permission: "billing.cashier_shift.view", Response: CashierShift{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CashierShift, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return CashierShift{}, err
			}
			sh, err := GetCashierShift(ctx, tx, sid)
			if err == nil && sh.CashierID != handle.UserID(ctx) && !hasPerm(ctx, "billing.cashier_shift.view_all") {
				return CashierShift{}, errs.NotFound("cashier shift")
			}
			return sh, err
		})})
	add(tsh, route.Route{Method: http.MethodPost, Path: "/api/v1/billing/cashier-shifts/{id}/cash-movements", Summary: "Cash in / cash out",
		Permission: "billing.cashier_shift.operate", Request: CashMovementInput{}, Response: CashierShift{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CashMovementInput) (CashierShift, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return CashierShift{}, err
			}
			return s.AddCashMovement(ctx, tx, handle.Property(ctx), sid, in)
		})})
	add(tsh, route.Route{Method: http.MethodPost, Path: "/api/v1/billing/cashier-shifts/{id}:close", Summary: "Close Shift (cash count, variance)",
		Permission: "billing.cashier_shift.operate", Request: CloseShiftInput{}, Response: CashierShift{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CloseShiftInput) (CashierShift, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return CashierShift{}, err
			}
			return s.CloseCashierShift(ctx, tx, handle.Property(ctx), sid, in)
		})})

	// ── business day & night audit ─────────────────────────────────────
	add(tnd, route.Route{Method: http.MethodGet, Path: "/api/v1/billing/business-days", Summary: "Business days (current open day first)",
		Permission: "billing.night_audit.view", Response: BusinessDay{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[BusinessDay], error) {
			p := handle.Property(ctx)
			cur, err := CurrentBusinessDate(ctx, tx, p)
			if err != nil {
				return httpx.Page[BusinessDay]{}, err
			}
			days, err := handle.List[BusinessDay](tx.Query(ctx, `SELECT to_char(business_date, 'YYYY-MM-DD') AS business_date, status, closed_at, reopened_at,
				reopen_reason, '{}'::jsonb AS summary FROM billing.business_days WHERE property_id = $1 ORDER BY business_date DESC LIMIT $2`, p, httpx.ParseList(r).Limit))
			if err != nil {
				return httpx.Page[BusinessDay]{}, err
			}
			cs := cur.Format("2006-01-02")
			found := false
			for i := range days {
				if days[i].BusinessDate == cs {
					days[i].Current, found = true, true
				}
			}
			if !found {
				days = append([]BusinessDay{{BusinessDate: cs, Status: "open", Current: true, Summary: []byte("{}")}}, days...)
			}
			return handle.Page(days, nil)
		})})
	add(tnd, route.Route{Method: http.MethodPost, Path: "/api/v1/billing/business-days:night-audit", Summary: "Run the Night Audit (close the current business day)",
		Permission: "billing.night_audit.run", Response: NightAuditRun{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (NightAuditRun, error) {
			return h.RunNightAudit(ctx, tx, handle.Property(ctx), "manual")
		})})
	add(tnd, route.Route{Method: http.MethodPost, Path: "/api/v1/billing/business-days/{date}:reopen", Summary: "Reopen a closed business day (approval)",
		Permission: "billing.night_audit.reopen", Request: ReopenInput{}, Response: BusinessDay{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReopenInput) (BusinessDay, error) {
			day, err := time.Parse("2006-01-02", chi.URLParam(r, "date"))
			if err != nil {
				return BusinessDay{}, errs.BadRequest("invalid_date", "date must be YYYY-MM-DD")
			}
			return h.RequestReopen(ctx, tx, handle.Property(ctx), day, in)
		})})
	add(tnd, route.Route{Method: http.MethodGet, Path: "/api/v1/billing/night-audit-runs", Summary: "Night Audit runs with their checks",
		Permission: "billing.night_audit.view", Response: NightAuditRun{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[NightAuditRun], error) {
			return handle.Page(handle.List[NightAuditRun](tx.Query(ctx, `SELECT id, to_char(business_date, 'YYYY-MM-DD') AS business_date, mode, status, checks,
				exceptions, warnings, started_at, finished_at FROM billing.night_audit_runs WHERE property_id = $1 ORDER BY started_at DESC LIMIT $2`,
				handle.Property(ctx), httpx.ParseList(r).Limit)))
		})})
	add(tnd, route.Route{Method: http.MethodGet, Path: "/api/v1/billing/daily-revenue", Summary: "Daily Revenue Report of a business day (frozen once closed)",
		Permission: "billing.night_audit.view", Response: DailyRevenue{}, Query: []route.Param{{Name: "date", Description: "YYYY-MM-DD; default the current business day"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (DailyRevenue, error) {
			p := handle.Property(ctx)
			cur, err := CurrentBusinessDate(ctx, tx, p)
			if err != nil {
				return DailyRevenue{}, err
			}
			day, err := handle.QueryDate(r, "date", cur)
			if err != nil {
				return DailyRevenue{}, err
			}
			return DailyRevenueOf(ctx, tx, p, day)
		})})
	h.registerP3Member(reg)
	h.registerP3Public(reg)
	h.registerP3ScheduleLinks(reg)
	h.registerARImport(reg)
}

func hasPerm(ctx context.Context, perm string) bool {
	p := authz.From(ctx)
	if p == nil {
		return false
	}
	pid := handle.Property(ctx)
	return p.Can(perm, &pid)
}

// sendStatement e-mails the corporate statement.
func (h *HTTP) sendStatement(ctx context.Context, tx pgx.Tx, property, corporateID uuid.UUID, st AccountStatement, email string) error {
	if email == "" {
		var e *string
		_ = tx.QueryRow(ctx, `SELECT email FROM crm.corporate_accounts WHERE id = $1`, corporateID).Scan(&e)
		email = deref(e)
	}
	if email == "" {
		return errs.Conflict("no_email", "the corporate account has no e-mail address")
	}
	if h.Notify != nil {
		if err := h.Notify.Send(ctx, tx, notify.Message{Event: "billing.corporate_statement", Category: "billing", Email: email, Name: st.BillToName,
			Channels: []string{notify.ChannelEmail}, PropertyID: &property, Data: map[string]any{"company": st.BillToName, "from": st.From, "to": st.To,
				"opening": st.OpeningBalance, "charges": st.Charges, "payments": st.Payments, "closing": st.ClosingBalance, "overdue": st.Aging.Totals.Overdue,
				"currency": st.Account.Currency}}); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE billing.customer_accounts SET last_statement_month = $2 WHERE id = $1`, st.Account.ID, st.To[:7]); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "send_statement", EntityType: "billing.customer_account", EntityID: st.Account.ID.String(),
		EntityLabel: st.Account.Number, PropertyID: &property, After: map[string]any{"from": st.From, "to": st.To, "closing": st.ClosingBalance}})
}

// ── member & public ───────────────────────────────────────────────────────

// PublicInvoice is the invoice shown on the payment link page.
type PublicInvoice struct {
	Number      string        `json:"number"`
	Kind        string        `json:"kind"`
	BillToName  string        `json:"billToName"`
	IssueDate   string        `json:"issueDate"`
	DueDate     string        `json:"dueDate"`
	Currency    string        `json:"currency"`
	Total       string        `json:"total"`
	Outstanding string        `json:"outstanding"`
	Status      string        `json:"status"`
	Lines       []InvoiceLine `json:"lines"`
}

// PublicPayInput opens a gateway payment from the payment link.
type PublicPayInput struct {
	Method string `json:"method,omitempty" enum:"qris,virtual_account,card"`
}

func (h *HTTP) registerP3Member(reg *route.Registry) {
	db := h.Svc.DB
	me := func(rt route.Route) { crm.MeRoute(reg, "billing", "Member Portal", rt) }
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/invoices", Summary: "My invoices", Response: Invoice{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Invoice], error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[Invoice]{}, err
			}
			return handle.Page(listInvoices(ctx, tx, `i.customer_id = $1 AND i.corporate_account_id IS NULL AND i.status <> 'draft'`, c.ID))
		})})
	myInvoice := func(ctx context.Context, tx pgx.Tx, r *http.Request) (InvoiceDetail, error) {
		c, err := crm.Me(ctx, tx)
		if err != nil {
			return InvoiceDetail{}, err
		}
		iid, err := handle.ID(r)
		if err != nil {
			return InvoiceDetail{}, err
		}
		d, err := GetInvoice(ctx, tx, iid, h.publicBase())
		if err != nil || d.CustomerID == nil || *d.CustomerID != c.ID || d.Status == "draft" {
			return InvoiceDetail{}, errs.NotFound("invoice")
		}
		return d, nil
	}
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/invoices/{id}", Summary: "My invoice", Response: InvoiceDetail{},
		Handler: handle.Read(db, myInvoice)})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/invoices/{id}:pay-online", Summary: "Pay my invoice online (QRIS, VA, card)",
		Request: PublicPayInput{}, Response: Payment{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PublicPayInput) (Payment, error) {
			d, err := myInvoice(ctx, tx, r)
			if err != nil {
				return Payment{}, err
			}
			return h.PayInvoice(ctx, tx, d.ID, PayInvoiceInput{MethodType: in.Method, Online: true, PayerName: d.BillToName})
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/payment-schedules", Summary: "My payment schedules (e.g. banquet terms)",
		Response: Schedule{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Schedule], error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[Schedule]{}, err
			}
			items, err := handle.List[Schedule](tx.Query(ctx, scheduleSelect+` WHERE s.customer_id = $1 AND s.status <> 'cancelled' ORDER BY s.created_at DESC`, c.ID))
			if err != nil {
				return httpx.Page[Schedule]{}, err
			}
			for i := range items {
				full, err := GetSchedule(ctx, tx, items[i].ID)
				if err != nil {
					return httpx.Page[Schedule]{}, err
				}
				items[i].Lines = full.Lines
			}
			return handle.Page(items, nil)
		})})
}

func (h *HTTP) registerP3Public(reg *route.Registry) {
	db := h.Svc.DB
	byToken := func(ctx context.Context, tx pgx.Tx, token string) (InvoiceDetail, uuid.UUID, error) {
		var iid, property uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id, property_id FROM billing.invoices WHERE public_token = $1 AND status NOT IN ('draft', 'void')`, token).
			Scan(&iid, &property); err != nil {
			return InvoiceDetail{}, property, errs.NotFound("invoice")
		}
		d, err := GetInvoice(ctx, tx, iid, "")
		return d, property, err
	}
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/public/invoices/{token}", Module: "billing", Tag: "Public Website", Auth: route.AuthPublic,
		Summary: "Invoice behind a payment link", Response: PublicInvoice{}, Handler: publicLimiter.Wrap(func(w http.ResponseWriter, r *http.Request) {
			var out PublicInvoice
			err := db.WithReadTx(dbtx.System(r.Context()), func(tx pgx.Tx) error {
				d, _, err := byToken(dbtx.System(r.Context()), tx, chi.URLParam(r, "token"))
				if err != nil {
					return err
				}
				out = PublicInvoice{Number: deref(d.Number), Kind: d.Kind, BillToName: d.BillToName, IssueDate: deref(d.IssueDate), DueDate: deref(d.DueDate),
					Currency: d.Currency, Total: d.Total, Outstanding: d.Outstanding, Status: d.Status, Lines: d.Lines}
				return nil
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, out)
		})})
	reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/public/invoices/{token}:pay", Module: "billing", Tag: "Public Website", Auth: route.AuthPublic,
		Summary: "Pay an invoice from its payment link (gateway checkout)", Request: PublicPayInput{}, Response: Payment{},
		Handler: publicLimiter.Wrap(func(w http.ResponseWriter, r *http.Request) {
			var in PublicPayInput
			if err := httpx.Decode(r, &in); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			var out Payment
			ctx := dbtx.System(r.Context())
			err := db.WithTx(ctx, func(tx pgx.Tx) error {
				d, _, err := byToken(ctx, tx, chi.URLParam(r, "token"))
				if err != nil {
					return err
				}
				out, err = h.PayInvoice(ctx, tx, d.ID, PayInvoiceInput{MethodType: in.Method, Online: true, PayerName: d.BillToName})
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusCreated, out)
		})})
}

var publicLimiter = &handle.Limiter{N: 30, Period: time.Minute}

// ── catalogue, templates, jobs ────────────────────────────────────────────

// P3Contribution is the PRD P3 part of the billing catalogue.
func P3Contribution() catalog.Contribution {
	perms := catalog.P("billing", "customer_folio", "view", "manage", "split")
	perms = append(perms, catalog.P("billing", "payment_schedule", "view", "manage")...)
	perms = append(perms, catalog.P("billing", "invoice", "view", "create", "issue", "void", "credit", "write_off", "allocate", "import")...)
	perms = append(perms, catalog.P("billing", "credit_override", "request")...)
	perms = append(perms, catalog.P("billing", "cashier_shift", "view", "operate", "view_all")...)
	perms = append(perms, catalog.P("billing", "night_audit", "view", "run", "reopen")...)
	all := make([]string, 0, len(perms))
	for _, p := range perms {
		all = append(all, p.Code)
	}
	desk := []string{"billing.customer_folio.view", "billing.customer_folio.manage", "billing.customer_folio.split", "billing.payment_schedule.view",
		"billing.invoice.view", "billing.cashier_shift.view", "billing.cashier_shift.operate"}
	banquet := []string{"billing.customer_folio.view", "billing.customer_folio.manage", "billing.payment_schedule.view", "billing.payment_schedule.manage",
		"billing.invoice.view", "billing.invoice.create", "billing.invoice.issue", "billing.cashier_shift.view", "billing.cashier_shift.operate"}
	view := []string{"billing.customer_folio.view", "billing.payment_schedule.view", "billing.invoice.view", "billing.cashier_shift.view",
		"billing.cashier_shift.view_all", "billing.night_audit.view"}
	accountant := append(append([]string{}, view...), "billing.invoice.create", "billing.invoice.issue", "billing.invoice.credit", "billing.invoice.allocate",
		"billing.payment_schedule.manage", "billing.customer_folio.manage", "billing.credit_override.request")
	return catalog.Contribution{
		Permissions: perms,
		RolePermissions: map[string][]string{
			"property_admin":          all,
			"finance_manager":         append(all, "billing.customer_account.charge"),
			"accountant":              append(accountant, "billing.customer_account.charge", "billing.payment.create"),
			"general_manager":         view,
			"resort_manager":          append(append([]string{}, view...), "billing.night_audit.run"),
			"club_manager":            append(append([]string{}, view...), "billing.night_audit.run"),
			"front_desk":              append(append([]string{}, desk...), "billing.night_audit.view", "billing.night_audit.run"),
			"reservation_staff":       desk,
			"cashier":                 desk,
			"sport_club_receptionist": {"billing.cashier_shift.view", "billing.cashier_shift.operate"},
			"outlet_manager":          {"billing.cashier_shift.view", "billing.cashier_shift.view_all", "billing.night_audit.view"},
			"banquet_manager":         append(append([]string{}, banquet...), "billing.payment.create", "billing.payment.view", "billing.folio.view"),
			"banquet_sales":           append(append([]string{}, banquet...), "billing.payment.view", "billing.folio.view"),
			"event_manager":           {"billing.payment_schedule.view", "billing.invoice.view", "billing.customer_folio.view"},
			"sales_executive":         {"billing.payment_schedule.view", "billing.invoice.view"},
		},
	}
}

// P3Templates are the notification templates of P3 billing.
func P3Templates() []provision.Template {
	t := map[string]map[string][2]string{
		"billing.invoice": {
			"en": {"Invoice {{.number}}", "Dear {{.billTo}},\n\nPlease find invoice {{.number}} of {{.currency}} {{.total}}, due {{.dueDate}}.\nPay online: {{.link}}"},
			"id": {"Tagihan {{.number}}", "Yth. {{.billTo}},\n\nBerikut tagihan {{.number}} sebesar {{.currency}} {{.total}}, jatuh tempo {{.dueDate}}.\nBayar online: {{.link}}"},
		},
		"billing.invoice_reminder": {
			"en": {"Reminder: invoice {{.number}}", "Dear {{.billTo}},\n\nInvoice {{.number}} has {{.currency}} {{.outstanding}} outstanding, due {{.dueDate}}.\nPay online: {{.link}}"},
			"id": {"Pengingat tagihan {{.number}}", "Yth. {{.billTo}},\n\nTagihan {{.number}} masih tersisa {{.currency}} {{.outstanding}}, jatuh tempo {{.dueDate}}.\nBayar online: {{.link}}"},
		},
		"billing.payment_schedule_reminder": {
			"en": {"Payment due: {{.label}}", "Hello,\n\n{{.label}} for {{.title}} ({{.amount}}) is due on {{.dueDate}}.{{if .overdue}} It is now overdue.{{end}}{{if .link}}\nPay online: {{.link}}{{end}}"},
			"id": {"Jatuh tempo pembayaran: {{.label}}", "Halo,\n\n{{.label}} untuk {{.title}} ({{.amount}}) jatuh tempo pada {{.dueDate}}.{{if .overdue}} Pembayaran sudah lewat jatuh tempo.{{end}}{{if .link}}\nBayar online: {{.link}}{{end}}"},
		},
		"billing.night_audit_blocked": {
			"en": {"Night audit blocked for {{.businessDate}}", "The night audit of {{.businessDate}} found {{.exceptions}} blocking exception(s). Close the open shifts and run it again."},
			"id": {"Night audit tertahan untuk {{.businessDate}}", "Night audit {{.businessDate}} menemukan {{.exceptions}} pengecualian. Tutup shift yang masih terbuka lalu jalankan lagi."},
		},
		"billing.corporate_statement": {
			"en": {"Statement {{.company}} {{.from}} – {{.to}}", "Dear {{.company}},\n\nOpening {{.opening}}, charges {{.charges}}, payments {{.payments}}, closing balance {{.currency}} {{.closing}} (overdue {{.overdue}})."},
			"id": {"Statement {{.company}} {{.from}} – {{.to}}", "Yth. {{.company}},\n\nSaldo awal {{.opening}}, tagihan {{.charges}}, pembayaran {{.payments}}, saldo akhir {{.currency}} {{.closing}} (lewat jatuh tempo {{.overdue}})."},
		},
	}
	var out []provision.Template
	for ev, locs := range t {
		for loc, c := range locs {
			for _, ch := range []string{"email", "in_app", "whatsapp"} {
				out = append(out, provision.Template{Event: ev, Channel: ch, Locale: loc, Subject: c[0], Body: c[1]})
			}
		}
	}
	return out
}

// P3JobsArgs runs the P3 billing housekeeping (reminders, overdue, corporate
// statements on the statement day).
type P3JobsArgs struct{}

func (P3JobsArgs) Kind() string { return "billing_p3_daily" }
func (P3JobsArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

// P3JobsWorker sends invoice and schedule reminders and corporate statements.
type P3JobsWorker struct {
	river.WorkerDefaults[P3JobsArgs]
	H *HTTP
}

func (w *P3JobsWorker) Work(ctx context.Context, _ *river.Job[P3JobsArgs]) error {
	_, err := w.H.RunDaily(ctx)
	return err
}

// RunDaily runs reminders, overdue marking and statements for every
// property; it returns the number of reminders and statements sent.
func (h *HTTP) RunDaily(ctx context.Context) (int, error) {
	ctx = dbtx.System(ctx)
	props, err := properties(ctx, h.Svc.DB)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, p := range props {
		err := h.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
			n, err := h.InvoiceReminders(ctx, tx, p)
			if err != nil {
				return err
			}
			m, err := h.ScheduleReminders(ctx, tx, p)
			if err != nil {
				return err
			}
			total += n + m
			pol, _, err := LoadCreditPolicy(ctx, tx, p)
			if err != nil {
				return err
			}
			today := localToday(ctx, tx, p, clock.Now())
			if today.Day() != pol.StatementDay {
				return nil
			}
			end := today.AddDate(0, 0, -1)
			start := time.Date(end.Year(), end.Month(), 1, 0, 0, 0, 0, time.UTC)
			rows, err := tx.Query(ctx, `SELECT a.id, a.corporate_account_id FROM billing.customer_accounts a WHERE a.property_id = $1 AND a.account_type = 'corporate'
				AND a.corporate_account_id IS NOT NULL AND coalesce(a.last_statement_month, '') <> $2`, p, end.Format("2006-01"))
			if err != nil {
				return err
			}
			type acc struct{ id, corp uuid.UUID }
			var list []acc
			for rows.Next() {
				var x acc
				if err := rows.Scan(&x.id, &x.corp); err != nil {
					rows.Close()
					return err
				}
				list = append(list, x)
			}
			rows.Close()
			for _, a := range list {
				st, err := AccountStatementOf(ctx, tx, p, a.id, start, end)
				if err != nil {
					return err
				}
				if err := h.sendStatement(ctx, tx, p, a.corp, st, ""); err != nil {
					if strings.Contains(err.Error(), "no e-mail") {
						continue
					}
					return err
				}
				total++
			}
			return nil
		})
		if err != nil {
			return total, fmt.Errorf("billing daily for %s: %w", p, err)
		}
	}
	return total, nil
}

// NightAuditArgs runs the automatic night audit.
type NightAuditArgs struct{}

func (NightAuditArgs) Kind() string { return "billing_night_audit" }
func (NightAuditArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 1}
}

// NightAuditWorker closes the business day after the cut-off.
type NightAuditWorker struct {
	river.WorkerDefaults[NightAuditArgs]
	H *HTTP
}

func (w *NightAuditWorker) Work(ctx context.Context, _ *river.Job[NightAuditArgs]) error {
	ctx = dbtx.System(ctx)
	props, err := properties(ctx, w.H.Svc.DB)
	if err != nil {
		return err
	}
	for _, p := range props {
		if err := w.H.Svc.DB.WithTx(ctx, func(tx pgx.Tx) error {
			_, err := w.H.AutoNightAudit(ctx, tx, p, clock.Now())
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// RegisterP3Jobs adds the P3 billing jobs.
func (h *HTTP) RegisterP3Jobs(reg *jobs.Registrar, loc func() *time.Location) {
	river.AddWorker(reg.Workers, &P3JobsWorker{H: h})
	river.AddWorker(reg.Workers, &NightAuditWorker{H: h})
	reg.Periodic = append(reg.Periodic,
		river.NewPeriodicJob(jobs.DailyAt{Hour: 7, Minute: 10, Location: loc}, func() (river.JobArgs, *river.InsertOpts) { return P3JobsArgs{}, nil }, nil),
		river.NewPeriodicJob(river.PeriodicInterval(15*time.Minute), func() (river.JobArgs, *river.InsertOpts) { return NightAuditArgs{}, nil }, nil))
}
