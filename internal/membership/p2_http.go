package membership

// Back Office routes of the P2 membership lifecycle (PRD P2 §11 API surface).

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/handle"
)

// PostponeInput moves the next annual fee due date.
type PostponeInput struct {
	DueDate string `json:"dueDate" doc:"YYYY-MM-DD"`
	Reason  string `json:"reason"`
}

// ChangeInput asks for an upgrade or downgrade.
type ChangeInput struct {
	TypeID uuid.UUID `json:"typeId"`
	Reason string    `json:"reason,omitempty"`
}

// ReplaceCardInput replaces a card.
type ReplaceCardInput struct {
	Reason   string `json:"reason"`
	WaiveFee bool   `json:"waiveFee,omitempty"`
}

// EntitlementView is the C5 answer for a customer (FR-MBL-05).
type EntitlementView struct {
	CustomerID  uuid.UUID `json:"customerId"`
	Memberships []Active  `json:"memberships" doc:"Active memberships with their entitlements per line"`
}

func (m *Module) registerLifecycle(reg *route.Registry) {
	db := m.DB
	add := func(rt route.Route) {
		rt.Module, rt.Scope = "membership", route.ScopeProperty
		if rt.Tag == "" {
			rt.Tag = "Membership Lifecycle"
		}
		reg.Add(rt)
	}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/membership/memberships/{id}", Summary: "Membership detail", Permission: "membership.membership.view",
		Response: MembershipDetail{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (MembershipDetail, error) {
			mid, err := handle.ID(r)
			if err != nil {
				return MembershipDetail{}, err
			}
			return GetMembershipDetail(ctx, tx, mid)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/membership/memberships/{id}:pause", Summary: "Pause a membership (approval; validity extended)",
		Permission: "membership.membership.pause", Request: PauseInput{}, Response: LifecycleRequest{}, Status: http.StatusAccepted,
		Handler: handle.Write(db, http.StatusAccepted, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PauseInput) (LifecycleRequest, error) {
			mid, err := handle.ID(r)
			if err != nil {
				return LifecycleRequest{}, err
			}
			from, err := parseDay("from", in.From)
			if err != nil {
				return LifecycleRequest{}, err
			}
			until, err := parseDay("until", in.Until)
			if err != nil {
				return LifecycleRequest{}, err
			}
			return m.RequestPause(ctx, tx, mid, from, until, in.Reason, "back_office")
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/membership/memberships/{id}:resume", Summary: "Resume a paused membership",
		Permission: "membership.membership.pause", Request: ReasonInput{}, Response: Membership{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReasonInput) (Membership, error) {
			mid, err := handle.ID(r)
			if err != nil {
				return Membership{}, err
			}
			return m.Resume(ctx, tx, mid, in.Reason)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/membership/memberships/{id}:postpone", Summary: "Postpone the annual fee due date (approval)",
		Permission: "membership.membership.postpone", Request: PostponeInput{}, Response: LifecycleRequest{}, Status: http.StatusAccepted,
		Handler: handle.Write(db, http.StatusAccepted, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PostponeInput) (LifecycleRequest, error) {
			mid, err := handle.ID(r)
			if err != nil {
				return LifecycleRequest{}, err
			}
			d, err := parseDay("dueDate", in.DueDate)
			if err != nil {
				return LifecycleRequest{}, err
			}
			return m.RequestPostpone(ctx, tx, mid, d, in.Reason)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/membership/memberships/{id}:suspend", Summary: "Suspend a membership (discipline)",
		Permission: "membership.membership.suspend", Request: ReasonInput{}, Response: Membership{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReasonInput) (Membership, error) {
			mid, err := handle.ID(r)
			if err != nil {
				return Membership{}, err
			}
			return m.Suspend(ctx, tx, mid, "discipline", in.Reason)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/membership/memberships/{id}:lift-suspension", Summary: "Lift a suspension",
		Permission: "membership.membership.suspend", Request: ReasonInput{}, Response: Membership{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReasonInput) (Membership, error) {
			mid, err := handle.ID(r)
			if err != nil {
				return Membership{}, err
			}
			return m.LiftSuspension(ctx, tx, mid, in.Reason)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/membership/memberships/{id}:reactivate", Summary: "Reactivate an expired or suspended membership (approval, fee)",
		Permission: "membership.membership.reactivate", Request: ReasonInput{}, Response: LifecycleRequest{}, Status: http.StatusAccepted,
		Handler: handle.Write(db, http.StatusAccepted, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReasonInput) (LifecycleRequest, error) {
			mid, err := handle.ID(r)
			if err != nil {
				return LifecycleRequest{}, err
			}
			return m.RequestReactivation(ctx, tx, mid, in.Reason)
		})})
	for _, dir := range []string{"upgrade", "downgrade"} {
		direction := dir
		add(route.Route{Method: http.MethodPost, Path: "/api/v1/membership/memberships/{id}:" + direction, Summary: strings.ToUpper(direction[:1]) + direction[1:] +
			" a membership type (approval, prorated fee)", Permission: "membership.membership.change", Request: ChangeInput{}, Response: LifecycleRequest{},
			Status: http.StatusAccepted,
			Handler: handle.Write(db, http.StatusAccepted, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ChangeInput) (LifecycleRequest, error) {
				mid, err := handle.ID(r)
				if err != nil {
					return LifecycleRequest{}, err
				}
				return m.RequestChange(ctx, tx, mid, in.TypeID, direction, in.Reason)
			})})
	}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/membership/memberships/{id}/change-preview", Summary: "Prorated fee difference of an upgrade / downgrade",
		Permission: "membership.membership.view", Response: ChangePreview{}, Query: []route.Param{{Name: "typeId", Required: true}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (ChangePreview, error) {
			mid, err := handle.ID(r)
			if err != nil {
				return ChangePreview{}, err
			}
			to, err := uuid.Parse(r.URL.Query().Get("typeId"))
			if err != nil {
				return ChangePreview{}, handle.Invalid("typeId", "invalid", "typeId is required")
			}
			return m.PreviewChange(ctx, tx, mid, to)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/membership/memberships/{id}:cancel", Summary: "Cancel a membership (cards blocked)",
		Permission: "membership.membership.cancel", Request: ReasonInput{}, Response: Membership{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReasonInput) (Membership, error) {
			mid, err := handle.ID(r)
			if err != nil {
				return Membership{}, err
			}
			return m.Cancel(ctx, tx, mid, in.Reason)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/membership/memberships/{id}/members", Summary: "Add a family member or corporate nominee",
		Permission: "membership.membership.members", Request: MemberInput{}, Response: Membership{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MemberInput) (Membership, error) {
			mid, err := handle.ID(r)
			if err != nil {
				return Membership{}, err
			}
			return m.AddMember(ctx, tx, mid, in)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/membership/memberships/{id}/members/{memberId}:remove", Summary: "Remove a family member or nominee",
		Permission: "membership.membership.members", Request: ReasonInput{}, Response: Membership{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReasonInput) (Membership, error) {
			mid, err := handle.ID(r)
			if err != nil {
				return Membership{}, err
			}
			covered, err := uuid.Parse(chi.URLParam(r, "memberId"))
			if err != nil {
				return Membership{}, errs.BadRequest("invalid_id", "memberId must be a UUID")
			}
			return m.RemoveMember(ctx, tx, mid, covered, in.Reason)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/membership/memberships/{id}:replace-nominee", Summary: "Replace a corporate nominee (approval, fee)",
		Permission: "membership.membership.members", Request: NomineeChangeInput{}, Response: LifecycleRequest{}, Status: http.StatusAccepted,
		Handler: handle.Write(db, http.StatusAccepted, func(ctx context.Context, tx pgx.Tx, r *http.Request, in NomineeChangeInput) (LifecycleRequest, error) {
			mid, err := handle.ID(r)
			if err != nil {
				return LifecycleRequest{}, err
			}
			return m.RequestNomineeChange(ctx, tx, mid, in)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/membership/requests", Summary: "Lifecycle requests (pause, postpone, reactivation, upgrade …)",
		Permission: "membership.membership.view", Response: LifecycleRequest{}, List: true, Query: []route.Param{{Name: "filter[status]"}, {Name: "filter[requestType]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[LifecycleRequest], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[LifecycleRequest](tx.Query(ctx, requestSelect+` WHERE r.property_id = $1 AND ($2 = '' OR r.status = $2)
				AND ($3 = '' OR r.request_type = $3) ORDER BY r.created_at DESC LIMIT $4`, handle.Property(ctx), lp.Filters["status"], lp.Filters["requestType"], lp.Limit)))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/membership/entitlements", Summary: "Membership status & entitlements of a customer (C5)",
		Permission: "membership.membership.view", Response: EntitlementView{}, Query: []route.Param{{Name: "customerId", Required: true}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (EntitlementView, error) {
			cid, err := uuid.Parse(r.URL.Query().Get("customerId"))
			if err != nil {
				return EntitlementView{}, handle.Invalid("customerId", "invalid", "customerId is required")
			}
			act, err := ActiveFor(ctx, tx, handle.Property(ctx), cid)
			return EntitlementView{CustomerID: cid, Memberships: act}, err
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/membership/annual-fees", Summary: "Membership Fee History & annual fee due dates", Tag: "Membership Fees",
		Permission: "membership.membership.view", Response: Fee{}, List: true,
		Query: []route.Param{{Name: "filter[status]"}, {Name: "filter[membershipId]"}, {Name: "dueWithinDays", Type: "integer"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Fee], error) {
			lp := httpx.ParseList(r)
			days := -1
			if v := r.URL.Query().Get("dueWithinDays"); v != "" {
				n, err := strconv.Atoi(v)
				if err != nil {
					return httpx.Page[Fee]{}, handle.Invalid("dueWithinDays", "invalid", "a number of days")
				}
				days = n
			}
			return handle.Page(handle.List[Fee](tx.Query(ctx, feeSelect+` WHERE f.property_id = $1 AND ($2 = '' OR f.status = $2)
				AND ($3 = '' OR f.membership_id::text = $3) AND ($4 < 0 OR f.due_date <= current_date + $4) ORDER BY f.due_date LIMIT $5`,
				handle.Property(ctx), lp.Filters["status"], lp.Filters["membershipId"], days, lp.Limit)))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/membership/annual-fees/{id}:waive", Summary: "Waive a membership fee (reason)", Tag: "Membership Fees",
		Permission: "membership.fee.waive", Request: ReasonInput{}, Response: Fee{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReasonInput) (Fee, error) {
			fid, err := handle.ID(r)
			if err != nil {
				return Fee{}, err
			}
			return m.WaiveFee(ctx, tx, fid, in.Reason)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/membership/cards/{id}:replace", Summary: "Card Replacement (old card blocked, fee per type)", Tag: "Membership Cards",
		Permission: "membership.card.issue", Request: ReplaceCardInput{}, Response: Card{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReplaceCardInput) (Card, error) {
			cid, err := handle.ID(r)
			if err != nil {
				return Card{}, err
			}
			return m.ReplaceCard(ctx, tx, cid, in.Reason, in.WaiveFee)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/membership/cards:lookup", Summary: "Resolve a scanned member card (QR / number) with memberships", Tag: "Membership Cards",
		Permission: "membership.card.view", Response: CardInfo{}, Query: []route.Param{{Name: "code", Required: true}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CardInfo, error) {
			ci, err := LookupCard(ctx, tx, handle.Property(ctx), r.URL.Query().Get("code"))
			if err != nil {
				return CardInfo{}, err
			}
			return *ci, nil
		})})
}

// p2Contribution is the P2 part of the membership catalogue.
func p2Contribution() catalog.Contribution {
	perms := catalog.P("membership", "membership", "pause", "postpone", "suspend", "reactivate", "change", "cancel", "members")
	perms = append(perms, catalog.P("membership", "fee", "waive")...)
	lifecycle := []string{"membership.membership.pause", "membership.membership.postpone", "membership.membership.suspend", "membership.membership.reactivate",
		"membership.membership.change", "membership.membership.cancel", "membership.membership.members", "membership.fee.waive"}
	desk := []string{"membership.member.view", "membership.membership.view", "membership.card.view"}
	return catalog.Contribution{
		Permissions: perms,
		RolePermissions: map[string][]string{
			"property_admin":          lifecycle,
			"membership_admin":        lifecycle,
			"membership_manager":      lifecycle,
			"sport_club_manager":      append(append([]string{}, desk...), "membership.application.view"),
			"sport_club_receptionist": desk,
			"cashier":                 desk,
			"pos_staff":               {"membership.card.view"},
		},
	}
}
