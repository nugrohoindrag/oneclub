package billing

// Sandbox checkout (FR-INT-05): the hosted payment page of the mock
// payment gateway. Payments created through a sandbox integration stay
// Pending until the gateway calls the webhook; this page lets the payer
// finish them by hand — paid or failed — through the same settlement path
// as a signed webhook. Production integrations are never touched: the
// payment must be pending, online and created by an integration that is
// both a sandbox adapter and in sandbox mode.

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/integration"
)

// SandboxCheckout is the mock gateway's hosted payment page.
type SandboxCheckout struct {
	Number      string     `json:"number"`
	Description *string    `json:"description" doc:"Folio number"`
	MethodType  string     `json:"methodType"`
	Amount      string     `json:"amount"`
	Currency    string     `json:"currency"`
	Status      string     `json:"status" enum:"pending,completed,cancelled,refunded"`
	QRString    *string    `json:"qrString"`
	VANumber    *string    `json:"vaNumber"`
	ExpiresAt   *time.Time `json:"expiresAt"`
	PaidAt      *time.Time `json:"paidAt"`
}

// SandboxOutcome is what the payer does on the sandbox page.
type SandboxOutcome struct {
	Outcome string `json:"outcome" enum:"paid,failed" doc:"Default: paid"`
}

func sandboxAdapter(key string) bool {
	for _, a := range integration.Adapters() {
		if a.Key == key {
			return a.Sandbox
		}
	}
	return false
}

// sandboxPayment loads an online payment created by a sandbox integration.
func sandboxPayment(ctx context.Context, tx pgx.Tx, number string) (Payment, error) {
	p, err := scanPayment(tx.QueryRow(ctx, `SELECT `+paymentCols+paymentFrom+` WHERE p.number = $1 AND p.channel = 'online'`, number))
	if dbtx.IsNoRows(err) || (err == nil && p.IntegrationCode == nil) {
		return p, errs.NotFound("payment")
	}
	if err != nil {
		return p, err
	}
	var adapter, mode string
	if err := tx.QueryRow(ctx, `SELECT adapter, mode FROM platform.integrations WHERE code = $1`, *p.IntegrationCode).Scan(&adapter, &mode); err != nil ||
		mode != "sandbox" || !sandboxAdapter(adapter) {
		return p, errs.NotFound("payment")
	}
	return p, nil
}

func (s *Service) registerSandboxCheckout(reg *route.Registry) {
	view := func(p Payment) SandboxCheckout {
		return SandboxCheckout{Number: p.Number, Description: p.FolioNumber, MethodType: p.MethodType, Amount: p.Amount, Currency: p.Currency,
			Status: p.Status, QRString: p.QRString, VANumber: p.VANumber, ExpiresAt: p.ExpiresAt, PaidAt: p.PaidAt}
	}
	crm.PublicRoute(reg, "billing", route.Route{Method: http.MethodGet, Path: "/api/v1/public/sandbox-checkout/{number}",
		Summary: "Sandbox gateway: the hosted payment page of a mock payment", Response: SandboxCheckout{},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			ctx := dbtx.System(r.Context())
			var out SandboxCheckout
			err := s.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
				p, err := sandboxPayment(ctx, tx, chi.URLParam(r, "number"))
				out = view(p)
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, out)
		}})
	crm.PublicRoute(reg, "billing", route.Route{Method: http.MethodPost, Path: "/api/v1/public/sandbox-checkout/{number}:complete",
		Summary: "Sandbox gateway: pay (or fail) a mock payment, settled like a gateway webhook", Request: SandboxOutcome{}, Response: SandboxCheckout{},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			if !crm.PublicLimiter.Allow(reqctx.GetMeta(r.Context()).IP + r.URL.Path) {
				httpx.WriteError(w, r, errs.RateLimited())
				return
			}
			var in SandboxOutcome
			if err := httpx.Decode(r, &in); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			if in.Outcome == "" {
				in.Outcome = "paid"
			}
			if in.Outcome != "paid" && in.Outcome != "failed" {
				httpx.WriteError(w, r, errs.Validation("invalid_outcome", "outcome is paid or failed", errs.Field("outcome", "invalid", "paid or failed")))
				return
			}
			ctx := dbtx.System(r.Context())
			var out SandboxCheckout
			err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
				p, err := sandboxPayment(ctx, tx, chi.URLParam(r, "number"))
				if err != nil {
					return err
				}
				if p.Status != "pending" {
					return errs.Conflict("payment_not_pending", "the payment is "+p.Status)
				}
				if err := s.SettleGateway(ctx, tx, *p.IntegrationCode, map[string]any{"externalId": deref(p.ExternalID), "reference": p.Number,
					"status": in.Outcome, "amount": p.Amount}); err != nil {
					return err
				}
				p, err = GetPayment(ctx, tx, p.ID)
				out = view(p)
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, out)
		}})
}
