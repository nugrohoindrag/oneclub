package crm

// Member & Guest App (PRD P2 EP-25): the signed-in portal user's own
// customer profile, preferences and consents (FR-APP-P2-09).

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/handle"
)

// Me returns the customer profile linked to the signed-in portal user;
// every Member Portal route works on its data only.
func Me(ctx context.Context, q dbtx.Querier) (Customer, error) {
	c, err := CustomerByUser(ctx, q, handle.UserID(ctx))
	if err != nil {
		return c, err
	}
	return c, nil
}

// MeRoute registers a Member Portal route like P1's /api/v1/member routes:
// portal access permission, the property is the signed-in customer's.
func MeRoute(reg *route.Registry, module, tag string, rt route.Route) {
	rt.Module, rt.Tag, rt.Permission = module, "Member Portal", catalog.ShellMemberPortal
	reg.Add(rt)
}

// MyPreferences is the Profile screen of the Member App.
type MyPreferences struct {
	Profile     CustomerProfile `json:"profile"`
	Preferences []Preference    `json:"preferences" doc:"Including the member's own health preferences"`
}

func (m *Engagement) registerMe(reg *route.Registry) {
	db := m.DB
	MeRoute(reg, "crm", "Member Portal", route.Route{Method: http.MethodGet, Path: "/api/v1/member/preferences", Summary: "My preferences and consents",
		Response: MyPreferences{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (MyPreferences, error) {
			p, err := Me(ctx, tx)
			if err != nil {
				return MyPreferences{}, err
			}
			prof, err := GetCustomerProfile(ctx, tx, p.ID)
			if err != nil {
				return MyPreferences{}, err
			}
			prefs, err := ListPreferences(ctx, tx, p.ID, true)
			return MyPreferences{Profile: prof, Preferences: prefs}, err
		})})
	MeRoute(reg, "crm", "Member Portal", route.Route{Method: http.MethodPost, Path: "/api/v1/member/preferences", Summary: "Add a preference",
		Request: PreferenceInput{}, Response: Preference{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PreferenceInput) (Preference, error) {
			p, err := Me(ctx, tx)
			if err != nil {
				return Preference{}, err
			}
			return RecordPreference(ctx, tx, p.PropertyID, p.ID, in, "member")
		})})
	MeRoute(reg, "crm", "Member Portal", route.Route{Method: http.MethodPost, Path: "/api/v1/member/preferences/{id}:remove", Summary: "Remove one of my preferences",
		Handler: handle.Write(db, http.StatusNoContent, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (handle.Empty, error) {
			p, err := Me(ctx, tx)
			if err != nil {
				return handle.Empty{}, err
			}
			pid, err := handle.ID(r)
			if err != nil {
				return handle.Empty{}, err
			}
			pref, err := preference(ctx, tx, pid)
			if err != nil {
				return handle.Empty{}, err
			}
			if pref.CustomerID != p.ID {
				return handle.Empty{}, errs.NotFound("preference")
			}
			return handle.Empty{}, RemovePreference(ctx, tx, p.PropertyID, pid)
		})})
	MeRoute(reg, "crm", "Member Portal", route.Route{Method: http.MethodPost, Path: "/api/v1/member/consent", Summary: "My profiling / marketing consent",
		Request: ConsentInput{}, Response: CustomerProfile{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ConsentInput) (CustomerProfile, error) {
			p, err := Me(ctx, tx)
			if err != nil {
				return CustomerProfile{}, err
			}
			return SetConsent(ctx, tx, p.PropertyID, p.ID, in.Profiling, in.Marketing)
		})})
	MeRoute(reg, "crm", "Member Portal", route.Route{Method: http.MethodPost, Path: "/api/v1/member/feedback", Summary: "Give feedback after a visit (in-app)",
		Request: MyFeedbackInput{}, Response: Feedback{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MyFeedbackInput) (Feedback, error) {
			p, err := Me(ctx, tx)
			if err != nil {
				return Feedback{}, err
			}
			var token string
			if err := tx.QueryRow(ctx, `SELECT token FROM crm.feedback_requests WHERE id = $1 AND customer_id = $2`, in.RequestID, p.ID).Scan(&token); err != nil {
				if dbtx.IsNoRows(err) {
					return Feedback{}, errs.NotFound("feedback survey")
				}
				return Feedback{}, err
			}
			return m.SubmitFeedback(ctx, tx, token, in.FeedbackInput, "member_app")
		})})
	MeRoute(reg, "crm", "Member Portal", route.Route{Method: http.MethodGet, Path: "/api/v1/member/feedback-requests", Summary: "Surveys waiting for my answer",
		Response: FeedbackInvite{}, List: true, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[FeedbackInvite], error) {
			p, err := Me(ctx, tx)
			if err != nil {
				return httpx.Page[FeedbackInvite]{}, err
			}
			return handle.Page(handle.List[FeedbackInvite](tx.Query(ctx, `SELECT id, token, context_type, context_label, status, expires_at FROM crm.feedback_requests
				WHERE customer_id = $1 AND status = 'sent' AND expires_at > now() ORDER BY sent_at DESC`, p.ID)))
		})})
}

type MyFeedbackInput struct {
	RequestID uuid.UUID `json:"requestId"`
	FeedbackInput
}
