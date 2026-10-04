package crm

import (
	"bytes"
	"context"
	"encoding/csv"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

var Segments = &resource.Def{
	Key: "crm.segment", Module: "crm", Perm: "crm.segment", Path: "/api/v1/crm/segments", Table: "crm.segments",
	Name: "Customer Segment", Plural: "Customer Segments", Tag: "CRM", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "rules", Column: "rules", Label: "Rules", Kind: resource.JSON, Default: "{}"},
		{Name: "memberCount", Column: "member_count", Label: "Members", Kind: resource.Int, ReadOnly: true},
		{Name: "computedAt", Column: "computed_at", Label: "Computed At", Kind: resource.Timestamp, ReadOnly: true},
		resource.Status("active", "inactive")},
}

var Campaigns = &resource.Def{
	Key: "crm.campaign", Module: "crm", Perm: "crm.campaign", Path: "/api/v1/crm/campaigns", Table: "crm.campaigns",
	Name: "Campaign", Plural: "Campaigns", Tag: "CRM", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "created_at DESC, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "segmentId", Column: "segment_id", Label: "Segment", Kind: resource.UUID, Required: true, Filter: true,
			Ref: &resource.Ref{Table: "crm.segments", SameProperty: true, Label: "segment"}},
		{Name: "templateEvent", Column: "template_event", Label: "Notification Template", Kind: resource.String, Required: true, Max: 80},
		{Name: "channel", Column: "channel", Label: "Channel", Kind: resource.Enum, Enum: []string{"email", "whatsapp", "in_app"}, Default: "email"},
		{Name: "data", Column: "data", Label: "Template Data", Kind: resource.JSON, Default: "{}"},
		{Name: "status", Column: "status", Label: "Status", Kind: resource.Enum, Enum: []string{"draft", "sent", "cancelled"}, Default: "draft", ReadOnly: true, Filter: true},
		{Name: "sentAt", Column: "sent_at", Label: "Sent At", Kind: resource.Timestamp, ReadOnly: true},
		{Name: "sentCount", Column: "sent_count", Label: "Sent", Kind: resource.Int, ReadOnly: true},
		{Name: "skippedCount", Column: "skipped_count", Label: "Skipped", Kind: resource.Int, ReadOnly: true}},
}

type ConsentInput struct {
	Profiling *bool `json:"profiling,omitempty"`
	Marketing *bool `json:"marketing,omitempty"`
}

type FeedbackRequestInput struct {
	CustomerID   uuid.UUID  `json:"customerId"`
	ContextType  string     `json:"contextType" enum:"round,stay,class,fnb,sport,meeting,other"`
	ContextID    *uuid.UUID `json:"contextId,omitempty"`
	ContextLabel string     `json:"contextLabel,omitempty"`
	Channel      string     `json:"channel,omitempty" enum:"email,whatsapp"`
}

type FollowUpInput struct {
	Note  string `json:"note"`
	Close bool   `json:"close,omitempty"`
}

// PublicSurvey is what the public feedback page shows.
type PublicSurvey struct {
	ContextType  string  `json:"contextType"`
	ContextLabel *string `json:"contextLabel"`
	Status       string  `json:"status"`
	HasSubject   bool    `json:"hasSubject" doc:"The survey also rates a person (e.g. the caddy)"`
}

var feedbackLimiter = &handle.Limiter{N: 30, Period: 60e9}

// sensitiveFor reports whether the caller may read health preferences.
func sensitiveFor(ctx context.Context) bool {
	p := authz.From(ctx)
	pid := handle.Property(ctx)
	return p != nil && p.Can("crm.preference.view_sensitive", &pid)
}

// registerEngagement adds the P2 CRM routes (EP-23/24): Customer 360,
// behaviour, staff context, consent, preferences, interactions, segments,
// feedback and campaigns.
func (m *Engagement) registerEngagement(reg *route.Registry, eng *resource.Engine) {
	for _, d := range []*resource.Def{Segments, Campaigns} {
		eng.Register(reg, d)
	}
	db := m.DB
	add := func(rt route.Route) {
		rt.Module, rt.Scope = "crm", route.ScopeProperty
		if rt.Tag == "" {
			rt.Tag = "CRM"
		}
		reg.Add(rt)
	}
	custID := func(ctx context.Context, q dbtx.Querier, r *http.Request) (uuid.UUID, error) {
		cid, err := handle.ID(r)
		if err != nil {
			return cid, err
		}
		var ok bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.customers WHERE id = $1 AND property_id = $2)`, cid, handle.Property(ctx)).Scan(&ok); err != nil {
			return cid, err
		}
		if !ok {
			return cid, errs.NotFound("customer")
		}
		return cid, nil
	}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/customers/{id}/360", Summary: "Customer 360 across business lines", Permission: "crm.customer.view",
		Response: Customer360{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Customer360, error) {
			cid, err := custID(ctx, tx, r)
			if err != nil {
				return Customer360{}, err
			}
			return m.View360(ctx, tx, handle.Property(ctx), cid, sensitiveFor(ctx))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/customers/{id}/behavior", Summary: "Behaviour profile (only with profiling consent)",
		Permission: "crm.customer.view", Response: BehaviorProfile{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (BehaviorProfile, error) {
			cid, err := custID(ctx, tx, r)
			if err != nil {
				return BehaviorProfile{}, err
			}
			return m.Profile(ctx, tx, handle.Property(ctx), cid)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/customers/{id}/context", Summary: "Personalised customer context for staff (check-in, POS)",
		Permission: "crm.customer.view", Response: CustomerContext{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CustomerContext, error) {
			cid, err := custID(ctx, tx, r)
			if err != nil {
				return CustomerContext{}, err
			}
			return m.Context(ctx, tx, handle.Property(ctx), cid, sensitiveFor(ctx))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/customers/{id}:consent", Summary: "Update profiling / marketing consent (UU PDP)",
		Permission: "crm.customer.update", Request: ConsentInput{}, Response: CustomerProfile{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ConsentInput) (CustomerProfile, error) {
			cid, err := custID(ctx, tx, r)
			if err != nil {
				return CustomerProfile{}, err
			}
			return SetConsent(ctx, tx, handle.Property(ctx), cid, in.Profiling, in.Marketing)
		})})
	// Preferences
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/customers/{id}/preferences", Summary: "Customer Preferences (diet / allergy need view_sensitive)",
		Permission: "crm.preference.view", Response: Preference{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Preference], error) {
			cid, err := custID(ctx, tx, r)
			if err != nil {
				return httpx.Page[Preference]{}, err
			}
			return handle.Page(ListPreferences(ctx, tx, cid, sensitiveFor(ctx)))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/customers/{id}/preferences", Summary: "Record Customer Preference",
		Permission: "crm.preference.manage", Request: PreferenceInput{}, Response: Preference{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PreferenceInput) (Preference, error) {
			cid, err := custID(ctx, tx, r)
			if err != nil {
				return Preference{}, err
			}
			return RecordPreference(ctx, tx, handle.Property(ctx), cid, in, "staff")
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/preferences/{id}:remove", Summary: "Remove a preference", Permission: "crm.preference.manage",
		Handler: handle.Write(db, http.StatusNoContent, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (handle.Empty, error) {
			pid, err := handle.ID(r)
			if err != nil {
				return handle.Empty{}, err
			}
			return handle.Empty{}, RemovePreference(ctx, tx, handle.Property(ctx), pid)
		})})
	// Interactions
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/customers/{id}/interactions", Summary: "Interaction History (manual + notifications sent)",
		Permission: "crm.interaction.view", Response: Interaction{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Interaction], error) {
			cid, err := custID(ctx, tx, r)
			if err != nil {
				return httpx.Page[Interaction]{}, err
			}
			return handle.Page(m.Interactions(ctx, tx, cid, httpx.ParseList(r).Limit))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/customers/{id}/interactions", Summary: "Log an interaction (call, WhatsApp, e-mail, visit)",
		Permission: "crm.interaction.create", Request: InteractionInput{}, Response: Interaction{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in InteractionInput) (Interaction, error) {
			cid, err := custID(ctx, tx, r)
			if err != nil {
				return Interaction{}, err
			}
			return LogInteraction(ctx, tx, handle.Property(ctx), cid, in, "manual", "", nil)
		})})
	// Segments
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/segments/{id}:compute", Summary: "Compute segment members from its rules",
		Permission: "crm.segment.update", Response: SegmentResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (SegmentResult, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return SegmentResult{}, err
			}
			return m.ComputeSegment(ctx, tx, handle.Property(ctx), sid)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/segments/{id}/members", Summary: "Segment members", Permission: "crm.segment.view",
		Response: SegmentMember{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[SegmentMember], error) {
			sid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[SegmentMember]{}, err
			}
			return handle.Page(SegmentMembers(ctx, tx, sid))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/segments/{id}/members.csv", Summary: "Export segment members (CSV)",
		Permission: "crm.segment.export", RawContent: "text/csv",
		Handler: func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			sid, err := handle.ID(r)
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			var buf bytes.Buffer
			cw := csv.NewWriter(&buf)
			_ = cw.Write([]string{"code", "name", "email", "phone"})
			err = db.WithReadTx(ctx, func(tx pgx.Tx) error {
				ms, err := SegmentMembers(ctx, tx, sid)
				for _, s := range ms {
					_ = cw.Write([]string{s.Code, s.Name, deref(s.Email), deref(s.Phone)})
				}
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			cw.Flush()
			w.Header().Set("Content-Type", "text/csv")
			w.Header().Set("Content-Disposition", `attachment; filename="segment-members.csv"`)
			_, _ = w.Write(buf.Bytes())
		}})
	// Feedback
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/feedback-requests", Summary: "Send a feedback survey link to a customer",
		Permission: "crm.feedback.request", Request: FeedbackRequestInput{}, Response: FeedbackInvite{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in FeedbackRequestInput) (FeedbackInvite, error) {
			if _, err := GetCustomer(ctx, tx, in.CustomerID); err != nil {
				return FeedbackInvite{}, err
			}
			pid := handle.Property(ctx)
			inv, err := m.RequestFeedback(ctx, tx, FeedbackRequest{PropertyID: pid, CustomerID: in.CustomerID, ContextType: in.ContextType,
				ContextID: in.ContextID, ContextLabel: in.ContextLabel, Channel: in.Channel})
			if err != nil {
				return FeedbackInvite{}, err
			}
			return *inv, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionCreate, EntityType: "crm.feedback_request",
				EntityID: inv.ID.String(), EntityLabel: in.ContextType, PropertyID: &pid, After: inv})
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/feedback", Summary: "Feedback received", Permission: "crm.feedback.view",
		Response: Feedback{}, List: true, Query: []route.Param{{Name: "filter[contextType]"}, {Name: "filter[lowScore]", Description: "true"}, {Name: "filter[followUp]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Feedback], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[Feedback](tx.Query(ctx, feedbackSelect+` WHERE f.property_id = $1 AND ($2 = '' OR f.context_type = $2)
				AND ($3 = '' OR f.low_score = ($3 = 'true')) AND ($4 = '' OR f.follow_up = $4) ORDER BY f.created_at DESC LIMIT $5`,
				handle.Property(ctx), lp.Filters["contextType"], lp.Filters["lowScore"], lp.Filters["followUp"], lp.Limit)))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/feedback/{id}:follow-up", Summary: "Follow up a (low-score) feedback",
		Permission: "crm.feedback.follow_up", Request: FollowUpInput{}, Response: Feedback{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in FollowUpInput) (Feedback, error) {
			fid, err := handle.ID(r)
			if err != nil {
				return Feedback{}, err
			}
			if err := handle.Required("note", in.Note); err != nil {
				return Feedback{}, err
			}
			status := "open"
			if in.Close {
				status = "closed"
			}
			pid := handle.Property(ctx)
			tag, err := tx.Exec(ctx, `UPDATE crm.feedback SET follow_up = $3, follow_up_note = $4 WHERE id = $1 AND property_id = $2`, fid, pid, status, in.Note)
			if err != nil {
				return Feedback{}, err
			}
			if tag.RowsAffected() == 0 {
				return Feedback{}, errs.NotFound("feedback")
			}
			f, err := handle.Get[Feedback](tx.Query(ctx, feedbackSelect+` WHERE f.id = $1`, fid))
			if err != nil {
				return f, err
			}
			return f, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionUpdate, EntityType: "crm.feedback", EntityID: fid.String(),
				EntityLabel: "follow-up", PropertyID: &pid, After: f})
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/campaigns/{id}:send", Summary: "Send campaign to its segment (opt-in only)",
		Permission: "crm.campaign.send", Response: CampaignResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (CampaignResult, error) {
			cid, err := handle.ID(r)
			if err != nil {
				return CampaignResult{}, err
			}
			return m.SendCampaign(ctx, tx, handle.Property(ctx), cid)
		})})
	// Public survey (link from WhatsApp / e-mail).
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/public/feedback/{token}", Summary: "Feedback survey (public link)", Tag: "Public",
		Module: "crm", Auth: route.AuthPublic, Response: PublicSurvey{},
		Handler: feedbackLimiter.Wrap(func(w http.ResponseWriter, r *http.Request) {
			ctx := dbtx.WithScope(r.Context(), dbtx.Scope{AllProperties: true})
			var out PublicSurvey
			err := db.WithReadTx(ctx, func(tx pgx.Tx) error {
				var subject *string
				err := tx.QueryRow(ctx, `SELECT context_type, context_label, CASE WHEN expires_at < now() AND status = 'sent' THEN 'expired' ELSE status END,
					subject_type FROM crm.feedback_requests WHERE token = $1`, chi.URLParam(r, "token")).Scan(&out.ContextType, &out.ContextLabel, &out.Status, &subject)
				if dbtx.IsNoRows(err) {
					return errs.NotFound("feedback survey")
				}
				out.HasSubject = subject != nil
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, out)
		})})
	reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/public/feedback/{token}", Summary: "Submit feedback (public link)", Tag: "Public",
		Module: "crm", Auth: route.AuthPublic, Request: FeedbackInput{}, Response: Feedback{},
		Handler: feedbackLimiter.Wrap(func(w http.ResponseWriter, r *http.Request) {
			var in FeedbackInput
			if err := httpx.Decode(r, &in); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			ctx := dbtx.WithScope(r.Context(), dbtx.Scope{AllProperties: true})
			var out Feedback
			err := db.WithTx(ctx, func(tx pgx.Tx) error {
				var err error
				out, err = m.SubmitFeedback(ctx, tx, chi.URLParam(r, "token"), in, "link")
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusCreated, out)
		})})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// engagementContribution is the P2 part of the CRM catalogue: preferences
// with health data, interactions, segments, feedback and campaigns.
func engagementContribution() catalog.Contribution {
	perms := resource.Permissions(Segments, Campaigns)
	perms = append(perms, catalog.P("crm", "preference", "view", "manage", "view_sensitive")...)
	perms = append(perms, catalog.P("crm", "interaction", "view", "create")...)
	perms = append(perms, catalog.P("crm", "feedback", "view", "request", "follow_up")...)
	perms = append(perms, catalog.P("crm", "campaign", "send")...)
	crmAll := append(resource.AllActions(Segments, Campaigns), "crm.preference.view", "crm.preference.manage",
		"crm.preference.view_sensitive", "crm.interaction.view", "crm.interaction.create", "crm.feedback.view", "crm.feedback.request",
		"crm.feedback.follow_up", "crm.campaign.send")
	frontline := []string{"crm.customer.view", "crm.preference.view", "crm.preference.manage"}
	manager := []string{"crm.customer.view", "crm.preference.view", "crm.interaction.view", "crm.feedback.view", "crm.feedback.follow_up", "crm.segment.view"}
	return catalog.Contribution{
		Permissions: perms,
		RolePermissions: map[string][]string{
			"property_admin":          crmAll,
			"crm_admin":               crmAll,
			"membership_admin":        {"crm.preference.view", "crm.preference.manage", "crm.interaction.view", "crm.interaction.create"},
			"membership_manager":      {"crm.preference.view", "crm.interaction.view", "crm.interaction.create", "crm.feedback.view", "crm.feedback.follow_up", "crm.segment.view", "crm.segment.export"},
			"sales_executive":         {"crm.interaction.view", "crm.interaction.create", "crm.preference.view"},
			"general_manager":         manager,
			"club_manager":            manager,
			"golf_manager":            manager,
			"sport_club_manager":      manager,
			"resort_manager":          manager,
			"outlet_manager":          manager,
			"marketing_staff":         {"crm.customer.view", "crm.segment.view", "crm.segment.create", "crm.segment.update", "crm.segment.export", "crm.campaign.view", "crm.campaign.create", "crm.campaign.update", "crm.campaign.send", "crm.feedback.view"},
			"caddy":                   frontline,
			"caddy_manager":           frontline,
			"starter_marshal":         {"crm.customer.view", "crm.preference.view"},
			"cashier":                 {"crm.customer.view", "crm.preference.view"},
			"pos_staff":               {"crm.customer.view", "crm.preference.view"},
			"front_desk":              frontline,
			"sport_club_receptionist": frontline,
		},
	}
}
