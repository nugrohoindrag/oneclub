package engagement

// Routes of Customer Engagement (PRD P3 §11): advanced segmentation, P3
// campaigns (preview, schedule, cancel, recipients, statistics), reminder
// rules, the suppression list, communication preferences, complaint
// tickets with SLA and escalation, NPS and Corporate 360; the Member App
// Support and Communication Preferences (EP-19) and the public unsubscribe
// / tracked-link pages of campaign messages.

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

var publicLimiter = &handle.Limiter{N: 30, Period: 60e9}

// TicketCreated is the result of opening a ticket from a feedback.
type TicketCreated struct {
	Ticket  ComplaintTicket `json:"ticket"`
	Created bool            `json:"created" doc:"false: the feedback already had a ticket"`
}

// ConsentEvent is one entry of the consent history.
type ConsentEvent struct {
	ID        uuid.UUID `json:"id" db:"id"`
	Channel   string    `json:"channel" db:"channel"`
	OptedIn   bool      `json:"optedIn" db:"opted_in"`
	Source    string    `json:"source" db:"source"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
}

// MemberTicketInput is a complaint from the Member App (Support).
type MemberTicketInput struct {
	CategoryID        *uuid.UUID  `json:"categoryId,omitempty"`
	BusinessLine      string      `json:"businessLine,omitempty" enum:"golf,sportclub,stay,pos,membership,banquet,other"`
	Subject           string      `json:"subject"`
	Description       string      `json:"description"`
	AttachmentFileIDs []uuid.UUID `json:"attachmentFileIds,omitempty"`
}

// PublicComplaintInput is a complaint from the website.
type PublicComplaintInput struct {
	PropertyID   uuid.UUID       `json:"propertyId"`
	Guest        crm.PublicGuest `json:"guest"`
	BusinessLine string          `json:"businessLine,omitempty" enum:"golf,sportclub,stay,pos,membership,banquet,other"`
	Subject      string          `json:"subject"`
	Description  string          `json:"description"`
}

func (p PublicComplaintInput) Property() uuid.UUID      { return p.PropertyID }
func (p PublicComplaintInput) Visitor() crm.PublicGuest { return p.Guest }

// PublicComplaintReceipt confirms a website complaint.
type PublicComplaintReceipt struct {
	Number string `json:"number"`
	Status string `json:"status"`
}

// TicketReplyInput is a reply of the customer.
type TicketReplyInput struct {
	Body string `json:"body"`
}

// MemberFeedbackInput is general feedback from the Member App (Support).
type MemberFeedbackInput struct {
	ContextType string `json:"contextType,omitempty" enum:"round,stay,class,fnb,sport,meeting,other"`
	Rating      int    `json:"rating" doc:"1–5"`
	NPS         *int   `json:"nps,omitempty" doc:"0–10"`
	Comment     string `json:"comment,omitempty"`
}

// TicketCategoryOption is a complaint category the member can choose.
type TicketCategoryOption struct {
	ID           uuid.UUID `json:"id" db:"id"`
	Name         string    `json:"name" db:"name"`
	BusinessLine *string   `json:"businessLine" db:"business_line"`
}

func (s *Service) customerParam(ctx context.Context, q dbtx.Querier, r *http.Request) (uuid.UUID, error) {
	cid, err := handle.ID(r)
	if err != nil {
		return cid, err
	}
	ok, err := crm.ExistsInProperty(ctx, q, handle.Property(ctx), cid)
	if err != nil {
		return cid, err
	}
	if !ok {
		return cid, errs.NotFound("customer")
	}
	return cid, nil
}

func ticketParam(ctx context.Context, q dbtx.Querier, r *http.Request) (uuid.UUID, error) {
	tid, err := handle.ID(r)
	if err != nil {
		return tid, err
	}
	var ok bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.tickets WHERE id = $1 AND property_id = $2)`, tid, handle.Property(ctx)).Scan(&ok); err != nil {
		return tid, err
	}
	if !ok {
		return tid, errs.NotFound("ticket")
	}
	return tid, nil
}

func userID(ctx context.Context) uuid.UUID {
	if p := authz.From(ctx); p != nil {
		return p.UserID
	}
	return uuid.Nil
}

// Register adds the engagement routes (wired by internal/app).
func (s *Service) Register(reg *route.Registry, eng *resource.Engine) {
	for _, d := range []*resource.Def{ReminderRules, Suppressions, TicketCategories} {
		eng.Register(reg, d)
	}
	db := s.DB
	add := func(tag string, rt route.Route) {
		rt.Module, rt.Scope, rt.Tag = "crm", route.ScopeProperty, tag
		reg.Add(rt)
	}
	// ── segmentation (FR-C360-02/03) ──
	add("CRM", route.Route{Method: http.MethodPost, Path: "/api/v1/crm/segments/{id}:refresh", Summary: "Refresh a segment with the P3 dimensions",
		Permission: "crm.segment.update", Response: SegmentRefresh{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (SegmentRefresh, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return SegmentRefresh{}, err
			}
			return s.Refresh(ctx, tx, handle.Property(ctx), sid)
		})})
	for _, op := range []struct {
		action, summary string
		remove          bool
	}{{"add", "Add customers to a static segment", false}, {"remove", "Remove customers from a static segment", true}} {
		remove := op.remove
		add("CRM", route.Route{Method: http.MethodPost, Path: "/api/v1/crm/segments/{id}/members:" + op.action, Summary: op.summary,
			Permission: "crm.segment.update", Request: SegmentMembersInput{}, Response: SegmentMembersResult{}, Status: http.StatusOK,
			Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in SegmentMembersInput) (SegmentMembersResult, error) {
				sid, err := handle.ID(r)
				if err != nil {
					return SegmentMembersResult{}, err
				}
				return ChangeMembers(ctx, tx, handle.Property(ctx), sid, in, remove)
			})})
	}
	// ── campaigns (FR-CMP-01..08) ──
	add("Campaigns", route.Route{Method: http.MethodGet, Path: "/api/v1/crm/campaigns/{id}/preview", Summary: "Preview the personalised message and the audience",
		Permission: "crm.campaign.view", Response: CampaignPreview{}, Query: []route.Param{{Name: "customerId", Description: "Sample recipient"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CampaignPreview, error) {
			cid, err := handle.ID(r)
			if err != nil {
				return CampaignPreview{}, err
			}
			sample, err := handle.QueryUUID(r, "customerId")
			if err != nil {
				return CampaignPreview{}, err
			}
			return s.PreviewCampaign(ctx, tx, handle.Property(ctx), cid, sample)
		})})
	add("Campaigns", route.Route{Method: http.MethodPost, Path: "/api/v1/crm/campaigns/{id}:schedule", Summary: "Schedule a campaign (approval above the Campaign Policies threshold)",
		Permission: "crm.campaign.send", Request: CampaignScheduleInput{}, Response: CampaignDetail{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CampaignScheduleInput) (CampaignDetail, error) {
			cid, err := handle.ID(r)
			if err != nil {
				return CampaignDetail{}, err
			}
			return s.Schedule(ctx, tx, handle.Property(ctx), cid, in)
		})})
	add("Campaigns", route.Route{Method: http.MethodPost, Path: "/api/v1/crm/campaigns/{id}:cancel", Summary: "Cancel a campaign not sent yet",
		Permission: "crm.campaign.update", Request: CampaignCancelInput{}, Response: CampaignDetail{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CampaignCancelInput) (CampaignDetail, error) {
			cid, err := handle.ID(r)
			if err != nil {
				return CampaignDetail{}, err
			}
			return s.Cancel(ctx, tx, handle.Property(ctx), cid, in)
		})})
	add("Campaigns", route.Route{Method: http.MethodGet, Path: "/api/v1/crm/campaigns/{id}/detail", Summary: "Campaign with its P3 fields",
		Permission: "crm.campaign.view", Response: CampaignDetail{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CampaignDetail, error) {
			cid, err := handle.ID(r)
			if err != nil {
				return CampaignDetail{}, err
			}
			return getCampaign(ctx, tx, handle.Property(ctx), cid, false)
		})})
	add("Campaigns", route.Route{Method: http.MethodGet, Path: "/api/v1/crm/campaigns/{id}/recipients", Summary: "Recipients with delivery, read, click and conversion",
		Permission: "crm.campaign.view", Response: CampaignRecipient{}, List: true, Query: []route.Param{{Name: "filter[status]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CampaignRecipient], error) {
			cid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[CampaignRecipient]{}, err
			}
			if _, err := getCampaign(ctx, tx, handle.Property(ctx), cid, false); err != nil {
				return httpx.Page[CampaignRecipient]{}, err
			}
			lp := httpx.ParseList(r)
			return handle.Page(recipients(ctx, tx, cid, lp.Filters["status"], lp.Limit))
		})})
	add("Campaigns", route.Route{Method: http.MethodGet, Path: "/api/v1/crm/campaigns/{id}/stats", Summary: "Campaign performance (sent, delivered, read, clicked, converted)",
		Permission: "crm.campaign.view", Response: CampaignStats{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CampaignStats, error) {
			cid, err := handle.ID(r)
			if err != nil {
				return CampaignStats{}, err
			}
			return StatsOf(ctx, tx, handle.Property(ctx), cid)
		})})
	// ── reminders (FR-CMP-06) ──
	add("Campaigns", route.Route{Method: http.MethodGet, Path: "/api/v1/crm/reminder-rules/{id}/preview", Summary: "Customers a reminder rule reaches today",
		Permission: "crm.reminder.view", Response: ReminderPreview{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (ReminderPreview, error) {
			rid, err := handle.ID(r)
			if err != nil {
				return ReminderPreview{}, err
			}
			return s.PreviewReminder(ctx, tx, handle.Property(ctx), rid)
		})})
	add("Campaigns", route.Route{Method: http.MethodGet, Path: "/api/v1/crm/reminder-log", Summary: "Reminders sent (or skipped)", Permission: "crm.reminder.view",
		Response: ReminderLog{}, List: true, Query: []route.Param{{Name: "filter[ruleId]"}, {Name: "filter[customerId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[ReminderLog], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[ReminderLog](tx.Query(ctx, `SELECT l.id, l.rule_id, x.code AS rule_code, x.kind, l.customer_id, c.name AS customer_name,
				l.occurrence, l.channel, l.status, l.created_at FROM crm.reminder_log l JOIN crm.reminder_rules x ON x.id = l.rule_id
				JOIN crm.customers c ON c.id = l.customer_id WHERE l.property_id = $1 AND ($2 = '' OR l.rule_id::text = $2)
				AND ($3 = '' OR l.customer_id::text = $3) ORDER BY l.created_at DESC LIMIT $4`, handle.Property(ctx), lp.Filters["ruleId"],
				lp.Filters["customerId"], lp.Limit)))
		})})
	// ── communication preferences (FR-CMP-02) ──
	add("CRM", route.Route{Method: http.MethodGet, Path: "/api/v1/crm/customers/{id}/communication-preferences", Summary: "Opt-in per channel and suppression",
		Permission: "crm.customer.view", Response: CommunicationPreferences{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CommunicationPreferences, error) {
			cid, err := s.customerParam(ctx, tx, r)
			if err != nil {
				return CommunicationPreferences{}, err
			}
			return Preferences(ctx, tx, cid)
		})})
	add("CRM", route.Route{Method: http.MethodPost, Path: "/api/v1/crm/customers/{id}/communication-preferences", Summary: "Record the customer's opt-in / opt-out per channel",
		Permission: "crm.customer.update", Request: CommunicationPreferencesInput{}, Response: CommunicationPreferences{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CommunicationPreferencesInput) (CommunicationPreferences, error) {
			cid, err := s.customerParam(ctx, tx, r)
			if err != nil {
				return CommunicationPreferences{}, err
			}
			return SetPreferences(ctx, tx, handle.Property(ctx), cid, in, "staff", "", nil)
		})})
	add("CRM", route.Route{Method: http.MethodGet, Path: "/api/v1/crm/customers/{id}/consent-history", Summary: "Consent history (UU PDP evidence)",
		Permission: "crm.customer.view", Response: ConsentEvent{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[ConsentEvent], error) {
			cid, err := s.customerParam(ctx, tx, r)
			if err != nil {
				return httpx.Page[ConsentEvent]{}, err
			}
			return handle.Page(handle.List[ConsentEvent](tx.Query(ctx, `SELECT id, channel, opted_in, source, created_at FROM crm.consent_events
				WHERE customer_id = $1 ORDER BY created_at DESC, id LIMIT $2`, cid, httpx.ParseList(r).Limit)))
		})})
	// ── Corporate 360 (FR-C360-04) ──
	add("Corporate Accounts", route.Route{Method: http.MethodGet, Path: "/api/v1/crm/corporate-accounts/{id}/360", Summary: "Corporate 360",
		Permission: "crm.corporate_account.view", Response: Corporate360{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Corporate360, error) {
			cid, err := handle.ID(r)
			if err != nil {
				return Corporate360{}, err
			}
			return s.CorporateView(ctx, tx, handle.Property(ctx), cid)
		})})
	// ── NPS (FR-TKT-06) ──
	add("Complaints", route.Route{Method: http.MethodGet, Path: "/api/v1/crm/nps", Summary: "NPS per business line and month with the latest comments",
		Permission: "crm.feedback.view", Response: NPSReport{},
		Query: []route.Param{{Name: "from", Description: "YYYY-MM-DD (default: 90 days ago)"}, {Name: "to"}, {Name: "businessLine"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (NPSReport, error) {
			today := localToday(ctx, tx, handle.Property(ctx))
			from, err := handle.QueryDate(r, "from", today.AddDate(0, 0, -90))
			if err != nil {
				return NPSReport{}, err
			}
			to, err := handle.QueryDate(r, "to", today)
			if err != nil {
				return NPSReport{}, err
			}
			return NPS(ctx, tx, handle.Property(ctx), from, to, r.URL.Query().Get("businessLine"))
		})})
	add("Complaints", route.Route{Method: http.MethodPost, Path: "/api/v1/crm/nps-responses", Summary: "Record an NPS answer given to staff",
		Permission: "crm.ticket.create", Request: StaffNPSInput{}, Response: NPSResponse{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in StaffNPSInput) (NPSResponse, error) {
			pid := handle.Property(ctx)
			if in.CustomerID != nil {
				ok, err := crm.ExistsInProperty(ctx, tx, pid, *in.CustomerID)
				if err != nil {
					return NPSResponse{}, err
				}
				if !ok {
					return NPSResponse{}, handle.Invalid("customerId", "not_found", "customer not found in this property")
				}
			}
			return RecordNPS(ctx, tx, pid, in.CustomerID, in.NPSInput, "staff")
		})})
	s.registerTickets(add)
	s.registerMember(reg)
	s.registerPublic(reg)
}

// StaffNPSInput is an NPS answer recorded by staff.
type StaffNPSInput struct {
	CustomerID *uuid.UUID `json:"customerId,omitempty"`
	NPSInput
}

func (s *Service) registerTickets(add func(string, route.Route)) {
	db := s.DB
	add("Complaints", route.Route{Method: http.MethodGet, Path: "/api/v1/crm/tickets", Summary: "Complaint tickets", Permission: "crm.ticket.view",
		Response: ComplaintTicket{}, List: true, Query: []route.Param{{Name: "filter[status]"}, {Name: "filter[priority]"}, {Name: "filter[businessLine]"},
			{Name: "filter[customerId]"}, {Name: "mine", Description: "true: assigned to me"}, {Name: "open", Description: "true: not resolved / closed"},
			{Name: "overdue", Description: "true: an SLA timer has passed"}, {Name: "q"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[ComplaintTicket], error) {
			lp := httpx.ParseList(r)
			v := r.URL.Query()
			mine := uuid.Nil
			if v.Get("mine") == "true" {
				mine = userID(ctx)
			}
			items, err := handle.List[ComplaintTicket](tx.Query(ctx, ticketSelect+` WHERE t.property_id = $1 AND ($2 = '' OR t.status = $2)
				AND ($3 = '' OR t.priority = $3) AND ($4 = '' OR t.business_line = $4) AND ($5 = '' OR t.customer_id::text = $5)
				AND ($6 = '00000000-0000-0000-0000-000000000000'::uuid OR t.assigned_to = $6)
				AND (NOT $7 OR t.status IN ('open', 'in_progress', 'escalated'))
				AND (NOT $8 OR (t.status IN ('open', 'in_progress', 'escalated') AND (t.resolution_due_at < now()
				  OR (t.first_responded_at IS NULL AND t.first_response_due_at < now()))))
				AND ($9 = '' OR t.number ILIKE '%' || $9 || '%' OR t.subject ILIKE '%' || $9 || '%' OR coalesce(c.name, t.contact_name) ILIKE '%' || $9 || '%')
				ORDER BY CASE t.status WHEN 'escalated' THEN 0 WHEN 'open' THEN 1 WHEN 'in_progress' THEN 2 ELSE 3 END, t.resolution_due_at, t.id LIMIT $10`,
				handle.Property(ctx), lp.Filters["status"], lp.Filters["priority"], lp.Filters["businessLine"], lp.Filters["customerId"], mine,
				v.Get("open") == "true", v.Get("overdue") == "true", lp.Q, lp.Limit))
			now := clock.Now()
			for i := range items {
				overdue(&items[i], now)
			}
			return handle.Page(items, err)
		})})
	add("Complaints", route.Route{Method: http.MethodPost, Path: "/api/v1/crm/tickets", Summary: "Open a complaint ticket", Permission: "crm.ticket.create",
		Request: TicketInput{}, Response: ComplaintTicket{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TicketInput) (ComplaintTicket, error) {
			ch := in.Channel
			if ch == "" {
				ch = "staff"
			}
			if ch != "staff" && ch != "whatsapp" && ch != "email" && ch != "phone" {
				return ComplaintTicket{}, handle.Invalid("channel", "invalid_channel", "channel must be staff, whatsapp, email or phone")
			}
			return s.CreateTicket(ctx, tx, handle.Property(ctx), in, ch, "staff", nil)
		})})
	add("Complaints", route.Route{Method: http.MethodGet, Path: "/api/v1/crm/tickets/{id}", Summary: "Ticket with its history and compensations",
		Permission: "crm.ticket.view", Response: TicketDetail{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (TicketDetail, error) {
			tid, err := ticketParam(ctx, tx, r)
			if err != nil {
				return TicketDetail{}, err
			}
			return ticketDetail(ctx, tx, tid, false)
		})})
	add("Complaints", route.Route{Method: http.MethodPost, Path: "/api/v1/crm/tickets/{id}:assign", Summary: "Route a ticket to a department / user",
		Permission: "crm.ticket.manage", Request: TicketAssignInput{}, Response: ComplaintTicket{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TicketAssignInput) (ComplaintTicket, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return ComplaintTicket{}, err
			}
			return s.Assign(ctx, tx, handle.Property(ctx), tid, in)
		})})
	add("Complaints", route.Route{Method: http.MethodPost, Path: "/api/v1/crm/tickets/{id}:comment", Summary: "Reply to the customer or add an internal note",
		Permission: "crm.ticket.manage", Request: TicketCommentInput{}, Response: TicketDetail{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TicketCommentInput) (TicketDetail, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TicketDetail{}, err
			}
			return s.Comment(ctx, tx, handle.Property(ctx), tid, in)
		})})
	add("Complaints", route.Route{Method: http.MethodPost, Path: "/api/v1/crm/tickets/{id}:escalate", Summary: "Escalate a ticket (line manager → General Manager)",
		Permission: "crm.ticket.escalate", Request: TicketReasonInput{}, Response: ComplaintTicket{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TicketReasonInput) (ComplaintTicket, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return ComplaintTicket{}, err
			}
			return s.Escalate(ctx, tx, handle.Property(ctx), tid, in)
		})})
	add("Complaints", route.Route{Method: http.MethodPost, Path: "/api/v1/crm/tickets/{id}:resolve", Summary: "Resolve a ticket (the customer is informed)",
		Permission: "crm.ticket.manage", Request: TicketResolveInput{}, Response: ComplaintTicket{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TicketResolveInput) (ComplaintTicket, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return ComplaintTicket{}, err
			}
			return s.Resolve(ctx, tx, handle.Property(ctx), tid, in)
		})})
	add("Complaints", route.Route{Method: http.MethodPost, Path: "/api/v1/crm/tickets/{id}:close", Summary: "Close a resolved ticket",
		Permission: "crm.ticket.manage", Request: TicketReasonInput{}, Response: ComplaintTicket{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TicketReasonInput) (ComplaintTicket, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return ComplaintTicket{}, err
			}
			return s.Close(ctx, tx, handle.Property(ctx), tid, in, "staff")
		})})
	add("Complaints", route.Route{Method: http.MethodPost, Path: "/api/v1/crm/tickets/{id}:reopen", Summary: "Reopen a resolved / closed ticket (new SLA timers)",
		Permission: "crm.ticket.manage", Request: TicketReasonInput{}, Response: ComplaintTicket{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TicketReasonInput) (ComplaintTicket, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return ComplaintTicket{}, err
			}
			return s.Reopen(ctx, tx, handle.Property(ctx), tid, in, "staff")
		})})
	add("Complaints", route.Route{Method: http.MethodPost, Path: "/api/v1/crm/tickets/{id}:compensate", Summary: "Propose a compensation (approval)",
		Permission: "crm.ticket.compensate", Request: TicketCompensationInput{}, Response: TicketCompensation{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TicketCompensationInput) (TicketCompensation, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TicketCompensation{}, err
			}
			return s.Compensate(ctx, tx, handle.Property(ctx), tid, in)
		})})
	add("Complaints", route.Route{Method: http.MethodPost, Path: "/api/v1/crm/feedback/{id}:open-ticket", Summary: "Open a complaint ticket from a feedback",
		Permission: "crm.ticket.create", Request: FeedbackTicketInput{}, Response: TicketCreated{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in FeedbackTicketInput) (TicketCreated, error) {
			fid, err := handle.ID(r)
			if err != nil {
				return TicketCreated{}, err
			}
			t, created, err := s.TicketFromFeedback(ctx, tx, handle.Property(ctx), fid, in, "staff")
			if err != nil || created {
				return TicketCreated{Ticket: t, Created: created}, err
			}
			pid := handle.Property(ctx)
			return TicketCreated{Ticket: t}, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "view_existing", EntityType: "crm.ticket",
				EntityID: t.ID.String(), EntityLabel: t.Number, PropertyID: &pid, Metadata: map[string]any{"feedbackId": fid}})
		})})
}

// ── Member App: Support & Communication Preferences (EP-19) ───────────────

func (s *Service) registerMember(reg *route.Registry) {
	db := s.DB
	me := func(rt route.Route) { crm.MeRoute(reg, "crm", "Member Portal", rt) }
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/communication-preferences", Summary: "My communication preferences (opt-in per channel)",
		Response: CommunicationPreferences{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CommunicationPreferences, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return CommunicationPreferences{}, err
			}
			return Preferences(ctx, tx, c.ID)
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/communication-preferences", Summary: "Change my communication preferences",
		Request: CommunicationPreferencesInput{}, Response: CommunicationPreferences{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CommunicationPreferencesInput) (CommunicationPreferences, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return CommunicationPreferences{}, err
			}
			return SetPreferences(ctx, tx, c.PropertyID, c.ID, in, "member", "", nil)
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/ticket-categories", Summary: "Complaint categories", Response: TicketCategoryOption{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[TicketCategoryOption], error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[TicketCategoryOption]{}, err
			}
			return handle.Page(handle.List[TicketCategoryOption](tx.Query(ctx, `SELECT id, name, business_line FROM crm.ticket_categories
				WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL ORDER BY name`, c.PropertyID)))
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/tickets", Summary: "My complaints and their status", Response: ComplaintTicket{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[ComplaintTicket], error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[ComplaintTicket]{}, err
			}
			items, err := handle.List[ComplaintTicket](tx.Query(ctx, ticketSelect+` WHERE t.customer_id = $1 ORDER BY t.created_at DESC LIMIT 100`, c.ID))
			for i := range items {
				memberView(&items[i])
			}
			return handle.Page(items, err)
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/tickets", Summary: "Send a complaint", Request: MemberTicketInput{}, Response: ComplaintTicket{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MemberTicketInput) (ComplaintTicket, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return ComplaintTicket{}, err
			}
			t, err := s.CreateTicket(ctx, tx, c.PropertyID, TicketInput{CustomerID: &c.ID, CategoryID: in.CategoryID, BusinessLine: in.BusinessLine,
				Subject: in.Subject, Description: in.Description, AttachmentFileIDs: in.AttachmentFileIDs}, "member_app", "customer", nil)
			memberView(&t)
			return t, err
		})})
	mine := func(ctx context.Context, tx pgx.Tx, r *http.Request) (crm.Customer, uuid.UUID, error) {
		c, err := crm.Me(ctx, tx)
		if err != nil {
			return c, uuid.Nil, err
		}
		tid, err := handle.ID(r)
		if err != nil {
			return c, tid, err
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.tickets WHERE id = $1 AND customer_id = $2)`, tid, c.ID).Scan(&ok); err != nil {
			return c, tid, err
		}
		if !ok {
			return c, tid, errs.NotFound("ticket")
		}
		return c, tid, nil
	}
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/tickets/{id}", Summary: "My complaint with the conversation", Response: TicketDetail{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (TicketDetail, error) {
			_, tid, err := mine(ctx, tx, r)
			if err != nil {
				return TicketDetail{}, err
			}
			d, err := ticketDetail(ctx, tx, tid, true)
			memberView(&d.ComplaintTicket)
			return d, err
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/tickets/{id}:reply", Summary: "Reply on my complaint", Request: TicketReplyInput{},
		Response: TicketDetail{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TicketReplyInput) (TicketDetail, error) {
			c, tid, err := mine(ctx, tx, r)
			if err != nil {
				return TicketDetail{}, err
			}
			d, err := s.CustomerReply(ctx, tx, c.PropertyID, tid, in.Body)
			memberView(&d.ComplaintTicket)
			return d, err
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/tickets/{id}:reopen", Summary: "Reopen my resolved complaint (within the reopen window)",
		Request: TicketReasonInput{}, Response: ComplaintTicket{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TicketReasonInput) (ComplaintTicket, error) {
			c, tid, err := mine(ctx, tx, r)
			if err != nil {
				return ComplaintTicket{}, err
			}
			t, err := s.Reopen(ctx, tx, c.PropertyID, tid, in, "customer")
			memberView(&t)
			return t, err
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/support/feedback", Summary: "Send feedback (Support)", Request: MemberFeedbackInput{},
		Response: crm.Feedback{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MemberFeedbackInput) (crm.Feedback, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return crm.Feedback{}, err
			}
			return s.MemberFeedback(ctx, tx, c, in)
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/nps", Summary: "How likely am I to recommend the club (NPS)", Request: NPSInput{},
		Response: NPSResponse{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in NPSInput) (NPSResponse, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return NPSResponse{}, err
			}
			return RecordNPS(ctx, tx, c.PropertyID, &c.ID, in, "member_app")
		})})
}

// memberView hides the internal routing of a ticket from the customer.
func memberView(t *ComplaintTicket) {
	t.AssignedTo, t.AssigneeName, t.DepartmentID, t.DepartmentName = nil, nil, nil, nil
	t.EscalationLevel, t.EscalatedAt = 0, nil
	if t.Status == "escalated" {
		t.Status = "in_progress"
	}
}

// ── public pages of campaign messages and the website complaint form ─────

func (s *Service) registerPublic(reg *route.Registry) {
	db := s.DB
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/public/unsubscribe/{token}", Summary: "Unsubscribe page of a campaign message",
		Tag: "Public", Module: "crm", Auth: route.AuthPublic, Response: UnsubscribeInfo{},
		Handler: publicLimiter.Wrap(func(w http.ResponseWriter, r *http.Request) {
			ctx := dbtx.WithScope(r.Context(), dbtx.Scope{AllProperties: true})
			var out UnsubscribeInfo
			err := db.WithReadTx(ctx, func(tx pgx.Tx) error {
				var err error
				out, err = unsubscribeInfo(ctx, tx, chi.URLParam(r, "token"))
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, out)
		})})
	reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/public/unsubscribe/{token}", Summary: "Stop marketing messages on this channel (or all channels)",
		Tag: "Public", Module: "crm", Auth: route.AuthPublic, Request: UnsubscribeInput{}, Response: UnsubscribeInfo{},
		Handler: publicLimiter.Wrap(func(w http.ResponseWriter, r *http.Request) {
			var in UnsubscribeInput
			if err := httpx.Decode(r, &in); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			ctx := dbtx.WithScope(r.Context(), dbtx.Scope{AllProperties: true})
			var out UnsubscribeInfo
			err := db.WithTx(ctx, func(tx pgx.Tx) error {
				var err error
				out, err = Unsubscribe(ctx, tx, chi.URLParam(r, "token"), in)
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, out)
		})})
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/public/campaign-links/{token}", Summary: "Tracked campaign link (redirects to the target)",
		Tag: "Public", Module: "crm", Auth: route.AuthPublic, RawContent: "text/html",
		Handler: publicLimiter.Wrap(func(w http.ResponseWriter, r *http.Request) {
			ctx := dbtx.WithScope(r.Context(), dbtx.Scope{AllProperties: true})
			var target string
			err := db.WithTx(ctx, func(tx pgx.Tx) error {
				var err error
				target, err = trackClick(ctx, tx, chi.URLParam(r, "token"))
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			if target == "" {
				target = s.website() + "/"
			}
			http.Redirect(w, r, target, http.StatusFound)
		})})
	crm.PublicRoute(reg, "crm", route.Route{Method: http.MethodPost, Path: "/api/v1/public/complaints", Summary: "Send a complaint from the website",
		Request: PublicComplaintInput{}, Response: PublicComplaintReceipt{},
		Handler: crm.PublicWrite(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, property uuid.UUID, c crm.Customer,
			in PublicComplaintInput) (PublicComplaintReceipt, error) {
			t, err := s.CreateTicket(ctx, tx, property, TicketInput{CustomerID: &c.ID, ContactName: in.Guest.Name, ContactEmail: in.Guest.Email,
				ContactPhone: in.Guest.Phone, BusinessLine: in.BusinessLine, Subject: in.Subject, Description: in.Description}, "website", "customer", nil)
			return PublicComplaintReceipt{Number: t.Number, Status: t.Status}, err
		})})
}
