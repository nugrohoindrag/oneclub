package billing

// Online payment of payment schedule lines (PRD P3 FR-APP-P3-07,
// FR-WEB-P3-05, FR-INT-P3-04): every schedule has a secure payment link
// (website /payment/{token}); a DP or termin is paid through the P1 payment
// gateway from the Member App, from the link in the schedule reminder and
// right after the online acceptance of a quotation ("Pay down payment") —
// without staff issuing an invoice first. A line with an issued invoice is
// paid through that invoice.

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

// PublicPaymentSchedule is a payment schedule behind its payment link.
type PublicPaymentSchedule struct {
	Token       string                      `json:"token" doc:"Payment link token (website /payment/{token})"`
	Number      string                      `json:"number"`
	Title       string                      `json:"title"`
	Currency    string                      `json:"currency"`
	TotalAmount string                      `json:"totalAmount"`
	PaidAmount  string                      `json:"paidAmount"`
	Status      string                      `json:"status" enum:"active,completed,cancelled"`
	NextLineID  *uuid.UUID                  `json:"nextLineId" doc:"First line still to pay (the DP first)"`
	Lines       []PublicPaymentScheduleLine `json:"lines"`
}

// PublicPaymentScheduleLine is one due amount of a schedule behind its link.
type PublicPaymentScheduleLine struct {
	ID         uuid.UUID `json:"id"`
	Seq        int       `json:"seq"`
	Label      string    `json:"label"`
	Kind       string    `json:"kind" enum:"down_payment,installment,final"`
	DueDate    string    `json:"dueDate"`
	Amount     string    `json:"amount"`
	PaidAmount string    `json:"paidAmount"`
	Status     string    `json:"status" enum:"pending,partially_paid,paid,overdue,cancelled"`
	Payable    bool      `json:"payable" doc:"Can be paid online now"`
}

// scheduleLink is the website payment link of a schedule.
func (h *HTTP) scheduleLink(token string) string {
	base := h.publicBase()
	if base == "" || token == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/payment/" + token
}

// ScheduleToken is the payment link token of a schedule.
func ScheduleToken(ctx context.Context, q dbtx.Querier, sid uuid.UUID) (string, error) {
	var token string
	err := q.QueryRow(ctx, `SELECT public_token FROM billing.payment_schedules WHERE id = $1`, sid).Scan(&token)
	if dbtx.IsNoRows(err) {
		return "", errs.NotFound("payment schedule")
	}
	return token, err
}

// publicSchedule is the link view of a schedule.
func publicSchedule(ctx context.Context, q dbtx.Querier, sid uuid.UUID) (PublicPaymentSchedule, error) {
	sc, err := GetSchedule(ctx, q, sid)
	if err != nil {
		return PublicPaymentSchedule{}, err
	}
	token, err := ScheduleToken(ctx, q, sid)
	if err != nil {
		return PublicPaymentSchedule{}, err
	}
	out := PublicPaymentSchedule{Token: token, Number: sc.Number, Title: sc.Title, Currency: sc.Currency, TotalAmount: sc.TotalAmount,
		PaidAmount: sc.PaidAmount, Status: sc.Status, Lines: make([]PublicPaymentScheduleLine, 0, len(sc.Lines))}
	for _, l := range sc.Lines {
		payable := sc.Status == "active" && sc.FolioID != nil && contains([]string{"pending", "partially_paid", "overdue"}, l.Status)
		if payable && l.InvoiceID != nil {
			var st string
			if err := q.QueryRow(ctx, `SELECT status FROM billing.invoices WHERE id = $1`, *l.InvoiceID).Scan(&st); err != nil {
				return out, err
			}
			payable = st != "paid"
		}
		if payable && out.NextLineID == nil {
			lid := l.ID
			out.NextLineID = &lid
		}
		out.Lines = append(out.Lines, PublicPaymentScheduleLine{ID: l.ID, Seq: l.Seq, Label: l.Label, Kind: l.Kind, DueDate: l.DueDate, Amount: l.Amount,
			PaidAmount: l.PaidAmount, Status: l.Status, Payable: payable})
	}
	return out, nil
}

// PayScheduleLineOnline opens a gateway payment (QRIS, VA, card) for a
// schedule line; the payment is allocated when the webhook settles it.
func (h *HTTP) PayScheduleLineOnline(ctx context.Context, tx pgx.Tx, sid, lid uuid.UUID, method string) (Payment, error) {
	var owner uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT schedule_id FROM billing.payment_schedule_lines WHERE id = $1`, lid).Scan(&owner); err != nil || owner != sid {
		if err != nil && !dbtx.IsNoRows(err) {
			return Payment{}, err
		}
		return Payment{}, errs.NotFound("payment schedule line")
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM billing.payment_schedules WHERE id = $1`, sid).Scan(&status); err != nil {
		return Payment{}, err
	}
	if status != "active" {
		return Payment{}, errs.Conflict("schedule_not_active", "the payment schedule is "+status)
	}
	if method == "" {
		method = "qris"
	}
	if !contains([]string{"qris", "virtual_account", "card"}, method) {
		return Payment{}, handle.Invalid("method", "invalid", "qris, virtual_account or card")
	}
	return h.PayScheduleLine(ctx, tx, lid, PayScheduleLineInput{MethodType: method, Online: true})
}

// registerP3ScheduleLinks adds the payment link and Member App routes.
func (h *HTTP) registerP3ScheduleLinks(reg *route.Registry) {
	db := h.Svc.DB
	byToken := func(ctx context.Context, q dbtx.Querier, token string) (uuid.UUID, error) {
		var sid uuid.UUID
		if err := q.QueryRow(ctx, `SELECT id FROM billing.payment_schedules WHERE public_token = $1 AND status <> 'cancelled'`, token).Scan(&sid); err != nil {
			if dbtx.IsNoRows(err) {
				return sid, errs.NotFound("payment schedule")
			}
			return sid, err
		}
		return sid, nil
	}
	publicRead := func(fn func(ctx context.Context, tx pgx.Tx, r *http.Request) (PublicPaymentSchedule, error)) http.HandlerFunc {
		return publicLimiter.Wrap(func(w http.ResponseWriter, r *http.Request) {
			var out PublicPaymentSchedule
			ctx := dbtx.System(r.Context())
			err := db.WithReadTx(ctx, func(tx pgx.Tx) error {
				var err error
				out, err = fn(ctx, tx, r)
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, out)
		})
	}
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/public/payment-schedules/{token}", Module: "billing", Tag: "Public Website",
		Auth: route.AuthPublic, Summary: "Payment schedule behind its payment link (DP, termin)", Response: PublicPaymentSchedule{},
		Handler: publicRead(func(ctx context.Context, tx pgx.Tx, r *http.Request) (PublicPaymentSchedule, error) {
			sid, err := byToken(ctx, tx, chi.URLParam(r, "token"))
			if err != nil {
				return PublicPaymentSchedule{}, err
			}
			return publicSchedule(ctx, tx, sid)
		})})
	reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/public/payment-schedules/{token}/lines/{lineId}:pay", Module: "billing",
		Tag: "Public Website", Auth: route.AuthPublic, Summary: "Pay a DP / termin from the payment link (gateway checkout)", Request: PublicPayInput{},
		Response: Payment{}, Handler: publicLimiter.Wrap(func(w http.ResponseWriter, r *http.Request) {
			var in PublicPayInput
			if err := httpx.Decode(r, &in); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			lid, err := uuid.Parse(chi.URLParam(r, "lineId"))
			if err != nil {
				httpx.WriteError(w, r, errs.NotFound("payment schedule line"))
				return
			}
			var out Payment
			ctx := dbtx.System(r.Context())
			err = db.WithTx(ctx, func(tx pgx.Tx) error {
				sid, err := byToken(ctx, tx, chi.URLParam(r, "token"))
				if err != nil {
					return err
				}
				out, err = h.PayScheduleLineOnline(ctx, tx, sid, lid, in.Method)
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusCreated, out)
		})})
	// "Pay down payment" right after the online acceptance of a quotation:
	// the schedule its conversion produced (asynchronously; 404 until then).
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/public/quotations/{token}/payment-schedule", Module: "billing", Tag: "Public Website",
		Auth: route.AuthPublic, Summary: "Payment schedule of an accepted quotation (DP payment step)", Response: PublicPaymentSchedule{},
		Handler: publicRead(func(ctx context.Context, tx pgx.Tx, r *http.Request) (PublicPaymentSchedule, error) {
			var sid uuid.UUID
			err := tx.QueryRow(ctx, `SELECT v.schedule_id FROM crm.sales_quotations t
				JOIN reporting.quotation_payment_schedules v ON v.property_id = t.property_id AND v.quotation_number = t.number AND v.status <> 'cancelled'
				JOIN crm.sales_quotations a ON a.id = v.quotation_id AND a.status = 'accepted'
				WHERE t.public_token = $1 ORDER BY v.created_at DESC LIMIT 1`, chi.URLParam(r, "token")).Scan(&sid)
			if dbtx.IsNoRows(err) {
				return PublicPaymentSchedule{}, errs.NotFound("payment schedule")
			}
			if err != nil {
				return PublicPaymentSchedule{}, err
			}
			return publicSchedule(ctx, tx, sid)
		})})
	// Member App: pay a schedule line of my own schedules online.
	crm.MeRoute(reg, "billing", "Member Portal", route.Route{Method: http.MethodPost, Path: "/api/v1/member/payment-schedules/{id}/lines/{lineId}:pay-online",
		Summary: "Pay a DP / termin of my payment schedule online (QRIS, VA, card)", Request: PublicPayInput{}, Response: Payment{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PublicPayInput) (Payment, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return Payment{}, err
			}
			sid, err := handle.ID(r)
			if err != nil {
				return Payment{}, err
			}
			lid, err := handle.ID(r, "lineId")
			if err != nil {
				return Payment{}, err
			}
			var cust *uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT customer_id FROM billing.payment_schedules WHERE id = $1`, sid).Scan(&cust); err != nil || cust == nil || *cust != c.ID {
				if err != nil && !dbtx.IsNoRows(err) {
					return Payment{}, err
				}
				return Payment{}, errs.NotFound("payment schedule")
			}
			return h.PayScheduleLineOnline(ctx, tx, sid, lid, in.Method)
		})})
}
