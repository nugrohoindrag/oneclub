package membership

// Join Membership in the Member App (member journey: Register / Apply →
// Approval → Payment → Membership Activated). The applicant has no
// account yet, so the application is followed with its number and the
// e-mail it was filed with. Once approved, the joining fee folio is paid
// online; the payment subscriber activates the membership and sends the
// Member Portal activation e-mail (OnPaymentSettled, provisionPortal).

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/billing"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
)

// ApplicationTrack is the applicant's view of an application.
type ApplicationTrack struct {
	ApplicationNo  string           `json:"applicationNo"`
	Status         string           `json:"status" enum:"draft,pending,approved,rejected,cancelled,completed"`
	TypeName       string           `json:"typeName"`
	PackageName    string           `json:"packageName"`
	Fee            string           `json:"fee" doc:"Joining fee + first period fee"`
	Outstanding    *string          `json:"outstanding" doc:"Fee still to pay (approved applications)"`
	DecisionReason *string          `json:"decisionReason"`
	Payment        *billing.Payment `json:"payment" doc:"Pending online payment of the fee"`
	MemberNo       *string          `json:"memberNo" doc:"Set once the membership is active"`
}

// TrackInput identifies the applicant (no account yet).
type TrackInput struct {
	Email  string `json:"email"`
	Method string `json:"method,omitempty" enum:"qris,virtual_account,card"`
}

// trackedApplication finds an application by number and applicant e-mail.
func trackedApplication(ctx context.Context, tx pgx.Tx, number, email string) (Application, crm.Customer, error) {
	var aid uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT a.id FROM membership.applications a JOIN crm.customers c ON c.id = a.customer_id
		WHERE upper(a.number) = upper($1) AND lower(c.email) = lower($2) AND $2 <> '' LIMIT 1`, strings.TrimSpace(number), strings.TrimSpace(email)).Scan(&aid); err != nil {
		return Application{}, crm.Customer{}, errs.NotFound("membership application")
	}
	a, err := GetApplication(ctx, tx, aid)
	if err != nil {
		return a, crm.Customer{}, err
	}
	c, err := crm.GetCustomer(ctx, tx, a.CustomerID)
	return a, c, err
}

func (m *Module) track(ctx context.Context, tx pgx.Tx, a Application) (ApplicationTrack, error) {
	out := ApplicationTrack{ApplicationNo: a.Number, Status: a.Status, TypeName: a.TypeName, PackageName: a.PackageName, Fee: a.Fee,
		DecisionReason: a.DecisionReason}
	if a.FeeFolioID != nil && a.Status == "approved" {
		sum, err := billing.FolioSummary(ctx, tx, *a.FeeFolioID)
		if err != nil {
			return out, err
		}
		out.Outstanding = &sum.Balance
		if p, err := billing.PendingOnline(ctx, tx, *a.FeeFolioID); err == nil && p != nil {
			out.Payment = p
		}
	}
	if a.MembershipID != nil {
		var no string
		if err := tx.QueryRow(ctx, `SELECT mb.code FROM membership.memberships ms JOIN membership.members mb ON mb.id = ms.member_id WHERE ms.id = $1`,
			*a.MembershipID).Scan(&no); err == nil {
			out.MemberNo = &no
		}
	}
	return out, nil
}

func (m *Module) registerJoinJourney(reg *route.Registry) {
	limited := func(w http.ResponseWriter, r *http.Request) bool {
		if !crm.PublicLimiter.Allow(reqctx.GetMeta(r.Context()).IP + r.URL.Path) {
			httpx.WriteError(w, r, errs.RateLimited())
			return true
		}
		return false
	}
	crm.PublicRoute(reg, "membership", route.Route{Method: http.MethodPost, Path: "/api/v1/public/membership-applications/{number}:track",
		Summary: "Follow my membership application (number + e-mail)", Request: TrackInput{}, Response: ApplicationTrack{}, NoAudit: "read-only lookup",
		Handler: func(w http.ResponseWriter, r *http.Request) {
			if limited(w, r) {
				return
			}
			var in TrackInput
			if err := httpx.Decode(r, &in); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			ctx := dbtx.System(r.Context())
			var out ApplicationTrack
			err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
				a, _, err := trackedApplication(ctx, tx, chi.URLParam(r, "number"), in.Email)
				if err != nil {
					return err
				}
				out, err = m.track(ctx, tx, a)
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, out)
		}})
	crm.PublicRoute(reg, "membership", route.Route{Method: http.MethodPost, Path: "/api/v1/public/membership-applications/{number}:pay-online",
		Summary: "Pay the joining fee of my approved application online", Request: TrackInput{}, Response: ApplicationTrack{},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			if limited(w, r) {
				return
			}
			var in TrackInput
			if err := httpx.Decode(r, &in); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			ctx := dbtx.System(r.Context())
			var out ApplicationTrack
			err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
				a, c, err := trackedApplication(ctx, tx, chi.URLParam(r, "number"), in.Email)
				if err != nil {
					return err
				}
				if a.Status != "approved" || a.FeeFolioID == nil {
					return errs.Conflict("application_not_payable", "the fee can be paid once the application is approved")
				}
				sum, err := billing.FolioSummary(ctx, tx, *a.FeeFolioID)
				if err != nil {
					return err
				}
				if !decimalOf(sum.Balance).IsPositive() {
					return errs.Conflict("nothing_due", "the fee is already paid")
				}
				// a new checkout replaces the previous one (another method)
				if err := m.Billing.CancelPending(ctx, tx, *a.FeeFolioID, "new checkout"); err != nil {
					return err
				}
				method := in.Method
				if method == "" {
					method = "qris"
				}
				var property uuid.UUID
				if err := tx.QueryRow(ctx, `SELECT property_id FROM membership.applications WHERE id = $1`, a.ID).Scan(&property); err != nil {
					return err
				}
				if _, err := m.Billing.TakePayment(withProperty(ctx, property), tx, billing.PaymentInput{FolioID: a.FeeFolioID, MethodType: method,
					Channel: "online", Amount: decimalOf(sum.Balance), Description: "Membership " + a.Number, PayerName: c.Name}); err != nil {
					return err
				}
				out, err = m.track(ctx, tx, a)
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusCreated, out)
		}})
}
