package sales

// HTTP API of CRM Sales (PRD P3 EP-01..EP-04, §11 API surface): leads and
// their capture paths, follow-ups, pipelines & opportunities with the
// kanban board and forecast, quotations with versions, discount approval,
// sending and the public acceptance link, sales targets and commission.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

const tagSales = "CRM Sales"

func (m *Module) add(reg *route.Registry, rt route.Route) {
	rt.Module, rt.Scope = "crm", route.ScopeProperty
	if rt.Tag == "" {
		rt.Tag = tagSales
	}
	reg.Add(rt)
}

// Register adds the CRM Sales routes and master data.
func (m *Module) Register(reg *route.Registry, eng *resource.Engine) {
	for _, d := range Defs() {
		eng.Register(reg, d)
	}
	m.registerLeads(reg)
	m.registerOpportunities(reg)
	m.registerQuotations(reg)
	m.registerCommission(reg)
	m.registerPublic(reg)
	m.registerAcceptance(reg)
}

// ── leads & follow-ups ────────────────────────────────────────────────────

// LeadList is a page of leads.
type leadPage = httpx.Page[Lead]

func (m *Module) registerLeads(reg *route.Registry) {
	db := m.DB
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/crm/leads", Summary: "Leads (filter by status, line, source, owner, SLA)",
		Permission: "crm.lead.view", Response: Lead{}, List: true,
		Query: []route.Param{{Name: "q"}, {Name: "filter[status]"}, {Name: "filter[line]"}, {Name: "filter[source]"}, {Name: "filter[ownerUserId]"},
			{Name: "filter[slaStatus]", Description: "pending, overdue, met, breached"}, {Name: "mine", Description: "true: only my leads"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (leadPage, error) {
			lp := httpx.ParseList(r)
			owner := lp.Filters["ownerUserId"]
			if r.URL.Query().Get("mine") == "true" {
				owner = handle.UserID(ctx).String()
			}
			items, err := handle.List[Lead](tx.Query(ctx, `SELECT * FROM (`+leadSelect+` WHERE l.property_id = $1 AND l.anonymized_at IS NULL) x
				WHERE ($2 = '' OR status = ANY(string_to_array($2, ','))) AND ($3 = '' OR line = $3) AND ($4 = '' OR source = $4)
				AND ($5 = '' OR owner_user_id::text = $5) AND ($6 = '' OR sla_status = $6)
				AND ($7 = '' OR number ILIKE '%' || $7 || '%' OR name ILIKE '%' || $7 || '%' OR company_name ILIKE '%' || $7 || '%'
				  OR phone ILIKE '%' || $7 || '%' OR email ILIKE '%' || $7 || '%')
				ORDER BY created_at DESC LIMIT $8`, handle.Property(ctx), lp.Filters["status"], lp.Filters["line"], lp.Filters["source"], owner,
				lp.Filters["slaStatus"], lp.Q, lp.Limit))
			return handle.Page(items, err)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/crm/leads", Summary: "Create Lead (manual, with source; duplicates are warned)",
		Permission: "crm.lead.create", Request: LeadInput{}, Response: LeadDetail{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in LeadInput) (LeadDetail, error) {
			return m.CreateLead(ctx, tx, handle.Property(ctx), in)
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/crm/leads/{id}", Summary: "Lead with duplicates, activities and assignments",
		Permission: "crm.lead.view", Response: LeadDetail{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (LeadDetail, error) {
			lid, err := handle.ID(r)
			if err != nil {
				return LeadDetail{}, err
			}
			if err := inProperty(ctx, tx, "crm.sales_leads", lid, "lead"); err != nil {
				return LeadDetail{}, err
			}
			return LeadDetailOf(ctx, tx, lid)
		})})
	m.add(reg, route.Route{Method: http.MethodPatch, Path: "/api/v1/crm/leads/{id}", Summary: "Update a lead", Permission: "crm.lead.update",
		Request: LeadUpdate{}, Response: LeadDetail{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in LeadUpdate) (LeadDetail, error) {
			lid, err := handle.ID(r)
			if err != nil {
				return LeadDetail{}, err
			}
			return m.UpdateLead(ctx, tx, handle.Property(ctx), lid, in)
		})})
	leadAction := func(path, summary, perm string, req any, fn func(ctx context.Context, tx pgx.Tx, lid uuid.UUID, r *http.Request) (any, error), res any) {
		m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/crm/leads/{id}" + path, Summary: summary, Permission: perm, Request: req,
			Response: res, Status: http.StatusOK, Handler: action(db, fn)})
	}
	leadAction(":assign", "Assign / reassign the lead", "crm.lead.assign", LeadAssignInput{},
		func(ctx context.Context, tx pgx.Tx, lid uuid.UUID, r *http.Request) (any, error) {
			var in LeadAssignInput
			if err := httpx.Decode(r, &in); err != nil {
				return nil, err
			}
			return m.AssignLead(ctx, tx, handle.Property(ctx), lid, in)
		}, LeadDetail{})
	leadAction(":qualify", "Mark the lead Qualified", "crm.lead.update", QualifyInput{},
		func(ctx context.Context, tx pgx.Tx, lid uuid.UUID, r *http.Request) (any, error) {
			var in QualifyInput
			if err := httpx.Decode(r, &in); err != nil {
				return nil, err
			}
			return m.QualifyLead(ctx, tx, handle.Property(ctx), lid, in)
		}, LeadDetail{})
	leadAction(":disqualify", "Mark the lead Unqualified with a reason", "crm.lead.update", DisqualifyInput{},
		func(ctx context.Context, tx pgx.Tx, lid uuid.UUID, r *http.Request) (any, error) {
			var in DisqualifyInput
			if err := httpx.Decode(r, &in); err != nil {
				return nil, err
			}
			return m.DisqualifyLead(ctx, tx, handle.Property(ctx), lid, in)
		}, LeadDetail{})
	leadAction(":convert", "Convert the lead into a customer (and corporate account) with an opportunity", "crm.lead.convert", ConvertInput{},
		func(ctx context.Context, tx pgx.Tx, lid uuid.UUID, r *http.Request) (any, error) {
			var in ConvertInput
			if err := httpx.Decode(r, &in); err != nil {
				return nil, err
			}
			return m.ConvertLead(ctx, tx, handle.Property(ctx), lid, in)
		}, ConvertResult{})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/crm/leads/{id}/activities", Summary: "Log an activity or schedule a follow-up on a lead",
		Permission: "crm.lead.update", Request: ActivityInput{}, Response: Activity{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ActivityInput) (Activity, error) {
			lid, err := handle.ID(r)
			if err != nil {
				return Activity{}, err
			}
			if err := inProperty(ctx, tx, "crm.sales_leads", lid, "lead"); err != nil {
				return Activity{}, err
			}
			return m.addActivity(ctx, tx, handle.Property(ctx), activityTarget{lead: &lid}, in, "manual", "")
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/crm/sales-users",
		Summary: "Sales staff of the property: owners of leads, opportunities and quotations, with their sales teams", Permission: "crm.lead.view",
		Response: SalesUser{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[SalesUser], error) {
			p := handle.Property(ctx)
			ids := holders(ctx, tx, p, "crm.opportunity.create")
			return handle.Page(handle.List[SalesUser](tx.Query(ctx, `SELECT u.id, u.full_name, u.email,
				coalesce(array_agg(DISTINCT t.name) FILTER (WHERE t.id IS NOT NULL), '{}') AS teams
				FROM platform.users u
				LEFT JOIN crm.sales_team_members tm ON tm.user_id = u.id AND tm.property_id = $2 AND tm.status = 'active'
				LEFT JOIN crm.sales_teams t ON t.id = tm.team_id AND t.archived_at IS NULL AND t.status = 'active'
				WHERE u.status = 'active' AND (u.id = ANY ($1) OR tm.id IS NOT NULL)
				GROUP BY u.id, u.full_name, u.email ORDER BY u.full_name`, ids, p)))
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/crm/leads:transfer", Summary: "Transfer leads, opportunities and follow-ups between sales",
		Permission: "crm.lead.assign", Request: TransferInput{}, Response: TransferResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TransferInput) (TransferResult, error) {
			return m.TransferLeads(ctx, tx, handle.Property(ctx), in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/crm/leads:import", Summary: "Import leads from a sales spreadsheet (CSV; preview or commit)",
		Permission: "crm.lead.import", Request: LeadImportInput{}, Response: LeadImportResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in LeadImportInput) (LeadImportResult, error) {
			return m.ImportLeads(ctx, tx, handle.Property(ctx), in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/crm/leads:capture-email", Summary: "Capture an inbound e-mail of a department mailbox as a lead",
		Permission: "crm.lead.capture", Request: EmailLeadInput{}, Response: CaptureResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in EmailLeadInput) (CaptureResult, error) {
			return m.CaptureEmail(ctx, tx, handle.Property(ctx), in)
		})})

	// Follow-ups (Naming Convention CRM → Follow-ups).
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/crm/follow-ups", Summary: "Follow-ups (mine by default; all with crm.lead.assign)",
		Permission: "crm.follow_up.view", Response: Activity{}, List: true,
		Query: []route.Param{{Name: "filter[status]", Description: "open (default), completed, cancelled, all"}, {Name: "filter[assignedTo]"},
			{Name: "overdue", Description: "true: only overdue"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Activity], error) {
			lp := httpx.ParseList(r)
			status := lp.Filters["status"]
			if status == "" {
				status = "open"
			}
			if status == "all" {
				status = ""
			}
			who := lp.Filters["assignedTo"]
			p := handle.Property(ctx)
			if !can(ctx, "crm.lead.assign", p) {
				who = handle.UserID(ctx).String()
			}
			items, err := handle.List[Activity](tx.Query(ctx, activitySelect+` WHERE a.property_id = $1 AND a.due_at IS NOT NULL
				AND ($2 = '' OR a.status = $2) AND ($3 = '' OR a.assigned_to::text = $3) AND (NOT $4 OR (a.status = 'open' AND a.due_at < now()))
				ORDER BY a.due_at, a.id LIMIT $5`, p, status, who, r.URL.Query().Get("overdue") == "true", lp.Limit))
			return handle.Page(items, err)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/crm/follow-ups", Summary: "Schedule a follow-up on a lead, an opportunity or a customer",
		Permission: "crm.follow_up.manage", Request: FollowUpInput{}, Response: Activity{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in FollowUpInput) (Activity, error) {
			if in.LeadID == nil && in.OpportunityID == nil && in.CustomerID == nil {
				return Activity{}, handle.Invalid("leadId", "required", "a lead, an opportunity or a customer")
			}
			if in.DueAt == nil {
				return Activity{}, handle.Invalid("dueAt", "required", "the due date of the follow-up")
			}
			return m.addActivity(ctx, tx, handle.Property(ctx), activityTarget{lead: in.LeadID, opportunity: in.OpportunityID, customer: in.CustomerID},
				in.ActivityInput, "manual", "")
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/crm/follow-ups/{id}:complete", Summary: "Complete a follow-up",
		Permission: "crm.follow_up.manage", Request: CompleteInput{}, Response: Activity{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CompleteInput) (Activity, error) {
			aid, err := handle.ID(r)
			if err != nil {
				return Activity{}, err
			}
			return m.CompleteFollowUp(ctx, tx, handle.Property(ctx), aid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/crm/follow-ups/{id}:cancel", Summary: "Cancel a follow-up",
		Permission: "crm.follow_up.manage", Request: FollowUpCancelInput{}, Response: Activity{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in FollowUpCancelInput) (Activity, error) {
			aid, err := handle.ID(r)
			if err != nil {
				return Activity{}, err
			}
			return m.CancelFollowUp(ctx, tx, handle.Property(ctx), aid, in)
		})})
}

// SalesUser is a user who can own leads, opportunities and quotations.
type SalesUser struct {
	ID       uuid.UUID `json:"id" db:"id"`
	FullName string    `json:"fullName" db:"full_name"`
	Email    string    `json:"email" db:"email"`
	Teams    []string  `json:"teams" db:"teams"`
}

// ── pipelines & opportunities ─────────────────────────────────────────────

func (m *Module) registerOpportunities(reg *route.Registry) {
	db := m.DB
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/crm/pipelines:seed-defaults",
		Summary: "Create the default pipelines and stages per business line (PRD P3 §16.1)", Permission: "crm.pipeline.create",
		Response: SeedResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (SeedResult, error) {
			p := handle.Property(ctx)
			res, err := SeedPipelines(ctx, tx, p)
			if err != nil {
				return res, err
			}
			return res, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "seed_defaults", EntityType: "crm.sales_pipeline",
				EntityID: p.String(), EntityLabel: "Default pipelines", PropertyID: &p, After: res})
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/crm/pipelines/{id}/board", Summary: "Sales Pipeline board (opportunities per stage)",
		Permission: "crm.opportunity.view", Response: PipelineBoard{},
		Query: []route.Param{{Name: "filter[ownerUserId]"}, {Name: "filter[line]"}, {Name: "mine"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (PipelineBoard, error) {
			pid, err := handle.ID(r)
			if err != nil {
				return PipelineBoard{}, err
			}
			var me *uuid.UUID
			if r.URL.Query().Get("mine") == "true" {
				u := handle.UserID(ctx)
				me = &u
			}
			return BoardOf(ctx, tx, handle.Property(ctx), pid, httpx.ParseList(r).Filters, me)
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/crm/sales-forecast", Summary: "Weighted pipeline forecast per month",
		Permission: "crm.opportunity.view", Response: Forecast{},
		Query: []route.Param{{Name: "from"}, {Name: "to"}, {Name: "line"}, {Name: "ownerUserId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Forecast, error) {
			p := handle.Property(ctx)
			today := localToday(ctx, tx, p)
			from, err := handle.QueryDate(r, "from", time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC))
			if err != nil {
				return Forecast{}, err
			}
			to, err := handle.QueryDate(r, "to", from.AddDate(0, 6, -1))
			if err != nil {
				return Forecast{}, err
			}
			return ForecastOf(ctx, tx, p, from, to, r.URL.Query().Get("line"), r.URL.Query().Get("ownerUserId"))
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/crm/opportunities", Summary: "Opportunities",
		Permission: "crm.opportunity.view", Response: Opportunity{}, List: true,
		Query: []route.Param{{Name: "q"}, {Name: "filter[status]"}, {Name: "filter[line]"}, {Name: "filter[pipelineId]"}, {Name: "filter[stageId]"},
			{Name: "filter[ownerUserId]"}, {Name: "filter[customerId]"}, {Name: "mine"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Opportunity], error) {
			lp := httpx.ParseList(r)
			owner := lp.Filters["ownerUserId"]
			if r.URL.Query().Get("mine") == "true" {
				owner = handle.UserID(ctx).String()
			}
			items, err := handle.List[Opportunity](tx.Query(ctx, `SELECT * FROM (`+opportunitySelect+` WHERE o.property_id = $1) x
				WHERE ($2 = '' OR status = ANY(string_to_array($2, ','))) AND ($3 = '' OR line = $3) AND ($4 = '' OR pipeline_id::text = $4)
				AND ($5 = '' OR stage_id::text = $5) AND ($6 = '' OR owner_user_id::text = $6) AND ($7 = '' OR customer_id::text = $7)
				AND ($8 = '' OR number ILIKE '%' || $8 || '%' OR title ILIKE '%' || $8 || '%')
				ORDER BY created_at DESC LIMIT $9`, handle.Property(ctx), lp.Filters["status"], lp.Filters["line"], lp.Filters["pipelineId"],
				lp.Filters["stageId"], owner, lp.Filters["customerId"], lp.Q, lp.Limit))
			return handle.Page(items, err)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/crm/opportunities", Summary: "Create an opportunity",
		Permission: "crm.opportunity.create", Request: OpportunityInput{}, Response: OpportunityDetail{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in OpportunityInput) (OpportunityDetail, error) {
			return m.CreateOpportunity(ctx, tx, handle.Property(ctx), in)
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/crm/opportunities/{id}", Summary: "Opportunity with stage history, quotations and activities",
		Permission: "crm.opportunity.view", Response: OpportunityDetail{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (OpportunityDetail, error) {
			oid, err := handle.ID(r)
			if err != nil {
				return OpportunityDetail{}, err
			}
			if err := inProperty(ctx, tx, "crm.sales_opportunities", oid, "opportunity"); err != nil {
				return OpportunityDetail{}, err
			}
			return OpportunityDetailOf(ctx, tx, oid)
		})})
	m.add(reg, route.Route{Method: http.MethodPatch, Path: "/api/v1/crm/opportunities/{id}", Summary: "Update an opportunity",
		Permission: "crm.opportunity.update", Request: OpportunityUpdate{}, Response: OpportunityDetail{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in OpportunityUpdate) (OpportunityDetail, error) {
			oid, err := handle.ID(r)
			if err != nil {
				return OpportunityDetail{}, err
			}
			return m.UpdateOpportunity(ctx, tx, handle.Property(ctx), oid, in)
		})})
	oppAction := func(path, summary, perm string, req any, fn func(ctx context.Context, tx pgx.Tx, oid uuid.UUID, r *http.Request) (any, error)) {
		m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/crm/opportunities/{id}" + path, Summary: summary, Permission: perm, Request: req,
			Response: OpportunityDetail{}, Status: http.StatusOK, Handler: action(db, fn)})
	}
	oppAction(":move-stage", "Move the opportunity to another stage", "crm.opportunity.update", MoveStageInput{},
		func(ctx context.Context, tx pgx.Tx, oid uuid.UUID, r *http.Request) (any, error) {
			var in MoveStageInput
			if err := httpx.Decode(r, &in); err != nil {
				return nil, err
			}
			return m.MoveStage(ctx, tx, handle.Property(ctx), oid, in)
		})
	oppAction(":win", "Mark the opportunity Won (requires an accepted quotation)", "crm.opportunity.close", WinInput{},
		func(ctx context.Context, tx pgx.Tx, oid uuid.UUID, r *http.Request) (any, error) {
			var in WinInput
			if err := httpx.Decode(r, &in); err != nil {
				return nil, err
			}
			return m.WinOpportunity(ctx, tx, handle.Property(ctx), oid, in)
		})
	oppAction(":lose", "Mark the opportunity Lost with a reason", "crm.opportunity.close", LoseInput{},
		func(ctx context.Context, tx pgx.Tx, oid uuid.UUID, r *http.Request) (any, error) {
			var in LoseInput
			if err := httpx.Decode(r, &in); err != nil {
				return nil, err
			}
			return m.LoseOpportunity(ctx, tx, handle.Property(ctx), oid, in)
		})
	oppAction(":reopen", "Reopen a lost opportunity", "crm.opportunity.close", ReopenInput{},
		func(ctx context.Context, tx pgx.Tx, oid uuid.UUID, r *http.Request) (any, error) {
			var in ReopenInput
			if err := httpx.Decode(r, &in); err != nil {
				return nil, err
			}
			return m.ReopenOpportunity(ctx, tx, handle.Property(ctx), oid, in)
		})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/crm/opportunities/{id}/activities", Summary: "Log an activity or schedule a follow-up on an opportunity",
		Permission: "crm.opportunity.update", Request: ActivityInput{}, Response: Activity{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ActivityInput) (Activity, error) {
			oid, err := handle.ID(r)
			if err != nil {
				return Activity{}, err
			}
			if err := inProperty(ctx, tx, "crm.sales_opportunities", oid, "opportunity"); err != nil {
				return Activity{}, err
			}
			return m.addActivity(ctx, tx, handle.Property(ctx), activityTarget{opportunity: &oid}, in, "manual", "")
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/crm/opportunities/{id}/venue-availability",
		Summary: "Venue availability around the event date of the opportunity", Permission: "crm.opportunity.view", Response: VenueAvailability{}, List: true,
		Query: []route.Param{{Name: "resourceType", Description: "default banquet_venue"}, {Name: "from"}, {Name: "to"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[VenueAvailability], error) {
			oid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[VenueAvailability]{}, err
			}
			o, err := GetOpportunity(ctx, tx, oid)
			if err != nil {
				return httpx.Page[VenueAvailability]{}, err
			}
			base := localToday(ctx, tx, handle.Property(ctx))
			if o.EventDate != nil {
				if d, err := time.Parse("2006-01-02", *o.EventDate); err == nil {
					base = d
				}
			}
			from, err := handle.QueryDate(r, "from", base)
			if err != nil {
				return httpx.Page[VenueAvailability]{}, err
			}
			to, err := handle.QueryDate(r, "to", from)
			if err != nil {
				return httpx.Page[VenueAvailability]{}, err
			}
			rt := r.URL.Query().Get("resourceType")
			if rt == "" && o.VenueResourceID == nil {
				rt = "banquet_venue"
			}
			return handle.Page(VenueAvailabilityOf(ctx, tx, o, rt, from, to))
		})})
}

// ── quotations ────────────────────────────────────────────────────────────

func (m *Module) registerQuotations(reg *route.Registry) {
	db := m.DB
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/crm/quotations", Summary: "Quotations (latest versions by default)",
		Permission: "crm.quotation.view", Response: QuotationBrief{}, List: true,
		Query: []route.Param{{Name: "q"}, {Name: "filter[status]"}, {Name: "filter[line]"}, {Name: "filter[opportunityId]"}, {Name: "filter[customerId]"},
			{Name: "filter[ownerUserId]"}, {Name: "allVersions", Description: "true: include revised versions"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[QuotationBrief], error) {
			lp := httpx.ParseList(r)
			all := r.URL.Query().Get("allVersions") == "true"
			items, err := handle.List[QuotationBrief](tx.Query(ctx, quotationBriefSelect+` WHERE q.property_id = $1
				AND ($2 = '' OR q.status = ANY(string_to_array($2, ','))) AND ($3 = '' OR q.line = $3) AND ($4 = '' OR q.opportunity_id::text = $4)
				AND ($5 = '' OR q.customer_id::text = $5) AND ($6 = '' OR q.owner_user_id::text = $6) AND ($7 OR q.status <> 'revised')
				AND ($8 = '' OR q.number ILIKE '%' || $8 || '%' OR q.title ILIKE '%' || $8 || '%' OR c.name ILIKE '%' || $8 || '%')
				ORDER BY q.created_at DESC LIMIT $9`, handle.Property(ctx), lp.Filters["status"], lp.Filters["line"], lp.Filters["opportunityId"],
				lp.Filters["customerId"], lp.Filters["ownerUserId"], all, lp.Q, lp.Limit))
			return handle.Page(items, err)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/crm/quotations", Summary: "Create a quotation (priced, tax & service, payment terms)",
		Permission: "crm.quotation.create", Request: QuotationInput{}, Response: QuotationDetail{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in QuotationInput) (QuotationDetail, error) {
			return m.CreateQuotation(ctx, tx, handle.Property(ctx), in)
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/crm/quotations/{id}", Summary: "Quotation with lines, versions, link and decision",
		Permission: "crm.quotation.view", Response: QuotationDetail{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (QuotationDetail, error) {
			qid, err := handle.ID(r)
			if err != nil {
				return QuotationDetail{}, err
			}
			if err := inProperty(ctx, tx, "crm.sales_quotations", qid, "quotation"); err != nil {
				return QuotationDetail{}, err
			}
			return m.QuotationDetailOf(ctx, tx, qid)
		})})
	m.add(reg, route.Route{Method: http.MethodPut, Path: "/api/v1/crm/quotations/{id}", Summary: "Edit a draft quotation (re-priced)",
		Permission: "crm.quotation.update", Request: QuotationInput{}, Response: QuotationDetail{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in QuotationInput) (QuotationDetail, error) {
			qid, err := handle.ID(r)
			if err != nil {
				return QuotationDetail{}, err
			}
			return m.UpdateQuotation(ctx, tx, handle.Property(ctx), qid, in)
		})})
	quoAction := func(path, summary, perm string, req any, fn func(ctx context.Context, tx pgx.Tx, qid uuid.UUID, r *http.Request) (any, error)) {
		m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/crm/quotations/{id}" + path, Summary: summary, Permission: perm, Request: req,
			Response: QuotationDetail{}, Status: http.StatusOK, Handler: action(db, fn)})
	}
	quoAction(":submit-approval", "Submit the discount above the Sales Policies limit for approval", "crm.quotation.update", nil,
		func(ctx context.Context, tx pgx.Tx, qid uuid.UUID, r *http.Request) (any, error) {
			return m.SubmitDiscountApproval(ctx, tx, handle.Property(ctx), qid)
		})
	quoAction(":send", "Send the quotation (PDF + secure acceptance link) by e-mail / WhatsApp", "crm.quotation.send", SendInput{},
		func(ctx context.Context, tx pgx.Tx, qid uuid.UUID, r *http.Request) (any, error) {
			var in SendInput
			if err := httpx.Decode(r, &in); err != nil {
				return nil, err
			}
			return m.SendQuotation(ctx, tx, handle.Property(ctx), qid, in)
		})
	quoAction(":revise", "Revise: the quotation becomes Revised and a new draft version is created", "crm.quotation.update", nil,
		func(ctx context.Context, tx pgx.Tx, qid uuid.UUID, r *http.Request) (any, error) {
			return m.ReviseQuotation(ctx, tx, handle.Property(ctx), qid)
		})
	quoAction(":accept", "Accept for the customer (signed copy / verbal confirmation)", "crm.quotation.accept", AcceptInput{},
		func(ctx context.Context, tx pgx.Tx, qid uuid.UUID, r *http.Request) (any, error) {
			var in AcceptInput
			if err := httpx.Decode(r, &in); err != nil {
				return nil, err
			}
			return m.AcceptQuotation(ctx, tx, handle.Property(ctx), qid, in)
		})
	quoAction(":reject", "Record the customer's rejection", "crm.quotation.update", RejectInput{},
		func(ctx context.Context, tx pgx.Tx, qid uuid.UUID, r *http.Request) (any, error) {
			var in RejectInput
			if err := httpx.Decode(r, &in); err != nil {
				return nil, err
			}
			return m.RejectQuotation(ctx, tx, handle.Property(ctx), qid, in)
		})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/crm/quotations/{id}/pdf", Summary: "Quotation PDF", Permission: "crm.quotation.view",
		RawContent: "application/pdf", Handler: func(w http.ResponseWriter, r *http.Request) {
			qid, err := handle.ID(r)
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			var b []byte
			err = db.WithReadTx(r.Context(), func(tx pgx.Tx) error {
				if err := inProperty(r.Context(), tx, "crm.sales_quotations", qid, "quotation"); err != nil {
					return err
				}
				d, err := m.QuotationDetailOf(r.Context(), tx, qid)
				if err != nil {
					return err
				}
				b, err = m.QuotationPDF(r.Context(), tx, d)
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write(b)
		}})
}

// ── commission & targets ──────────────────────────────────────────────────

func (m *Module) registerCommission(reg *route.Registry) {
	db := m.DB
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/crm/commission-lines", Summary: "Commission lines (earned, clawed back, adjusted)",
		Permission: "crm.commission.view", Response: CommissionLine{}, List: true,
		Query: []route.Param{{Name: "filter[userId]"}, {Name: "filter[statementId]"}, {Name: "filter[quotationId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CommissionLine], error) {
			lp := httpx.ParseList(r)
			items, err := handle.List[CommissionLine](tx.Query(ctx, commissionSelect+` WHERE c.property_id = $1 AND ($2 = '' OR c.user_id::text = $2)
				AND ($3 = '' OR c.statement_id::text = $3) AND ($4 = '' OR c.quotation_id::text = $4) ORDER BY c.created_at DESC LIMIT $5`,
				handle.Property(ctx), lp.Filters["userId"], lp.Filters["statementId"], lp.Filters["quotationId"], lp.Limit))
			return handle.Page(items, err)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/crm/commission-adjustments", Summary: "Book a manual commission adjustment",
		Permission: "crm.commission.manage", Request: AdjustInput{}, Response: CommissionLine{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in AdjustInput) (CommissionLine, error) {
			return m.AdjustCommission(ctx, tx, handle.Property(ctx), in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/crm/commission-statements:generate", Summary: "Generate the commission statements of a month",
		Permission: "crm.commission.manage", Request: GenerateInput{}, Response: CommissionStatement{}, List: true, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in GenerateInput) (httpx.Page[CommissionStatement], error) {
			return handle.Page(m.GenerateStatements(ctx, tx, handle.Property(ctx), in))
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/crm/commission-statements", Summary: "Commission statements",
		Permission: "crm.commission.view", Response: CommissionStatement{}, List: true,
		Query: []route.Param{{Name: "filter[period]"}, {Name: "filter[status]"}, {Name: "filter[userId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CommissionStatement], error) {
			lp := httpx.ParseList(r)
			items, err := handle.List[CommissionStatement](tx.Query(ctx, statementSelect+` WHERE s.property_id = $1 AND ($2 = '' OR s.period = $2)
				AND ($3 = '' OR s.status = $3) AND ($4 = '' OR s.user_id::text = $4) ORDER BY s.period DESC, u.full_name LIMIT $5`, handle.Property(ctx),
				lp.Filters["period"], lp.Filters["status"], lp.Filters["userId"], lp.Limit))
			return handle.Page(items, err)
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/crm/commission-statements:export", Summary: "Commission statements of a month (CSV for payroll)",
		Permission: "crm.commission.export", RawContent: "text/csv", Query: []route.Param{{Name: "period"}, {Name: "status"}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			var out string
			err := db.WithReadTx(r.Context(), func(tx pgx.Tx) error {
				var err error
				out, err = StatementsCSV(r.Context(), tx, handle.Property(r.Context()), r.URL.Query().Get("period"), r.URL.Query().Get("status"))
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="commission-`+strings.ReplaceAll(r.URL.Query().Get("period"), "/", "")+`.csv"`)
			_, _ = w.Write([]byte(out))
		}})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/crm/commission-statements/{id}", Summary: "Commission statement with its lines",
		Permission: "crm.commission.view", Response: StatementDetail{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (StatementDetail, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return StatementDetail{}, err
			}
			if err := inProperty(ctx, tx, "crm.sales_commission_statements", sid, "commission statement"); err != nil {
				return StatementDetail{}, err
			}
			return statementDetail(ctx, tx, sid)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/crm/commission-statements/{id}:submit", Summary: "Submit the statement for approval",
		Permission: "crm.commission.manage", Response: StatementDetail{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (StatementDetail, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return StatementDetail{}, err
			}
			return m.SubmitStatement(ctx, tx, handle.Property(ctx), sid)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/crm/commission-statements/{id}:mark-paid", Summary: "Mark an approved statement paid",
		Permission: "crm.commission.manage", Request: MarkPaidInput{}, Response: StatementDetail{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MarkPaidInput) (StatementDetail, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return StatementDetail{}, err
			}
			return m.MarkStatementPaid(ctx, tx, handle.Property(ctx), sid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/crm/sales-target-achievements", Summary: "Sales vs target per sales and period",
		Permission: "crm.sales_target.view", Response: TargetAchievement{}, List: true, Query: []route.Param{{Name: "from"}, {Name: "to"}, {Name: "userId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[TargetAchievement], error) {
			p := handle.Property(ctx)
			today := localToday(ctx, tx, p)
			from, err := handle.QueryDate(r, "from", time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC))
			if err != nil {
				return httpx.Page[TargetAchievement]{}, err
			}
			to, err := handle.QueryDate(r, "to", from.AddDate(0, 1, -1))
			if err != nil {
				return httpx.Page[TargetAchievement]{}, err
			}
			return handle.Page(TargetAchievements(ctx, tx, p, from, to, r.URL.Query().Get("userId")))
		})})
}

// ── public: inquiry form and quotation acceptance link ────────────────────

// PublicQuotationLine is a line shown on the acceptance page.
type PublicQuotationLine struct {
	Description string `json:"description"`
	Quantity    string `json:"quantity"`
	UnitPrice   string `json:"unitPrice"`
	Discount    string `json:"discount"`
	Total       string `json:"total"`
}

// PublicQuotation is the quotation behind its secure link (FR-QUO-05).
type PublicQuotation struct {
	Number         string                `json:"number"`
	Version        int                   `json:"version"`
	Title          string                `json:"title"`
	Status         string                `json:"status" enum:"sent,accepted,rejected,expired,revised"`
	CustomerName   *string               `json:"customerName"`
	CompanyName    *string               `json:"companyName"`
	EventType      *string               `json:"eventType"`
	EventDate      *string               `json:"eventDate"`
	EndDate        *string               `json:"endDate"`
	Pax            *int                  `json:"pax"`
	Currency       string                `json:"currency"`
	Lines          []PublicQuotationLine `json:"lines"`
	Subtotal       string                `json:"subtotal"`
	Discount       string                `json:"discount"`
	ServiceAmount  string                `json:"serviceAmount"`
	TaxAmount      string                `json:"taxAmount"`
	Total          string                `json:"total"`
	ValidUntil     string                `json:"validUntil"`
	PaymentTerms   []PaymentTerm         `json:"paymentTerms"`
	Terms          *string               `json:"terms"`
	AcceptedAt     *time.Time            `json:"acceptedAt"`
	AcceptedByName *string               `json:"acceptedByName"`
	// PRD P3 §16 #18 (p3_acceptance.go)
	OtpRequired      bool                     `json:"otpRequired" doc:"Acceptance needs the one-time code (:request-otp)"`
	EMeteraiRequired bool                     `json:"eMeteraiRequired" doc:"An e-Meterai is applied on acceptance"`
	EMeterai         *PublicQuotationEMeterai `json:"eMeterai"`
}

// PublicAcceptInput accepts a quotation from its link.
type PublicAcceptInput struct {
	Name          string `json:"name" doc:"Name of the person accepting"`
	TermsAccepted bool   `json:"termsAccepted" doc:"The terms & conditions are accepted"`
	Note          string `json:"note,omitempty"`
	OtpCode       string `json:"otpCode,omitempty" doc:"One-time code sent by :request-otp (required by the Sales Policies)"`
}

// PublicRejectInput rejects a quotation from its link.
type PublicRejectInput struct {
	Reason string `json:"reason"`
}

var publicLimiter = &handle.Limiter{N: 30, Period: time.Minute}

func (m *Module) byToken(ctx context.Context, tx pgx.Tx, token string, lock bool) (Quotation, uuid.UUID, error) {
	var qid, property uuid.UUID
	q := `SELECT id, property_id FROM crm.sales_quotations WHERE public_token = $1 AND status <> 'draft'
		AND (token_expires_at IS NULL OR token_expires_at > now())`
	if lock {
		q += ` FOR UPDATE`
	}
	if err := tx.QueryRow(ctx, q, token).Scan(&qid, &property); err != nil {
		if dbtx.IsNoRows(err) {
			return Quotation{}, property, errs.NotFound("quotation")
		}
		return Quotation{}, property, err
	}
	qt, err := GetQuotation(ctx, tx, qid)
	return qt, property, err
}

func (m *Module) publicView(ctx context.Context, tx pgx.Tx, qid uuid.UUID) (PublicQuotation, error) {
	d, err := m.QuotationDetailOf(ctx, tx, qid)
	if err != nil {
		return PublicQuotation{}, err
	}
	out := PublicQuotation{Number: d.Number, Version: d.Version, Title: d.Title, Status: d.Status, CustomerName: d.CustomerName,
		CompanyName: d.CorporateName, EventType: d.EventType, EventDate: d.EventDate, EndDate: d.EndDate, Pax: d.Pax, Currency: d.Currency,
		Subtotal: d.Subtotal, Discount: d.Discount, ServiceAmount: d.ServiceAmount, TaxAmount: d.TaxAmount, Total: d.Total, ValidUntil: d.ValidUntil,
		PaymentTerms: d.PaymentTerms, Terms: d.Terms, AcceptedAt: d.AcceptedAt, AcceptedByName: d.AcceptedByName, Lines: []PublicQuotationLine{},
		OtpRequired: d.otpRequired, EMeteraiRequired: d.EMeteraiRequired, EMeterai: publicEMeterai(d)}
	for _, l := range d.Lines {
		out.Lines = append(out.Lines, PublicQuotationLine{Description: l.Description, Quantity: l.Quantity, UnitPrice: l.UnitPrice, Discount: l.Discount,
			Total: l.Total})
	}
	return out, nil
}

func (m *Module) registerPublic(reg *route.Registry) {
	db := m.DB
	reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/public/inquiries", Module: "crm", Tag: "Public Website", Auth: route.AuthPublic,
		Summary: "Inquiry form (wedding, banquet, MICE, corporate golf, tournament, membership) → Lead", Request: InquiryInput{},
		Response: InquiryResult{}, Status: http.StatusAccepted, Handler: m.publicInquiry})
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/public/quotations/{token}", Module: "crm", Tag: "Public Website", Auth: route.AuthPublic,
		Summary: "Quotation behind its secure link", Response: PublicQuotation{}, Handler: publicLimiter.Wrap(func(w http.ResponseWriter, r *http.Request) {
			ctx := dbtx.System(r.Context())
			var out PublicQuotation
			err := db.WithReadTx(ctx, func(tx pgx.Tx) error {
				q, _, err := m.byToken(ctx, tx, chi.URLParam(r, "token"), false)
				if err != nil {
					return err
				}
				out, err = m.publicView(ctx, tx, q.ID)
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, out)
		})})
	decide := func(path, summary string, req any, fn func(ctx context.Context, tx pgx.Tx, property uuid.UUID, q Quotation, r *http.Request) error) {
		reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/public/quotations/{token}" + path, Module: "crm", Tag: "Public Website",
			Auth: route.AuthPublic, Summary: summary, Request: req, Response: PublicQuotation{},
			Handler: publicLimiter.Wrap(func(w http.ResponseWriter, r *http.Request) {
				ctx := dbtx.System(r.Context())
				var out PublicQuotation
				err := db.WithTx(ctx, func(tx pgx.Tx) error {
					q, property, err := m.byToken(ctx, tx, chi.URLParam(r, "token"), true)
					if err != nil {
						return err
					}
					pctx := crm.PublicCtx(ctx, property)
					if err := fn(pctx, tx, property, q, r); err != nil {
						return err
					}
					out, err = m.publicView(pctx, tx, q.ID)
					return err
				})
				if err != nil {
					httpx.WriteError(w, r, err)
					return
				}
				httpx.JSON(w, http.StatusOK, out)
			})})
	}
	reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/public/quotations/{token}:accept", Module: "crm", Tag: "Public Website",
		Auth: route.AuthPublic, Summary: "Accept the quotation (name, terms and the one-time code; recorded with IP, time and the verified code)",
		Request: PublicAcceptInput{}, Response: PublicQuotation{}, Handler: publicLimiter.Wrap(m.publicAccept)})
	decide(":reject", "Reject the quotation with a reason", PublicRejectInput{},
		func(ctx context.Context, tx pgx.Tx, property uuid.UUID, q Quotation, r *http.Request) error {
			var in PublicRejectInput
			if err := httpx.Decode(r, &in); err != nil {
				return err
			}
			if err := handle.Required("reason", in.Reason); err != nil {
				return err
			}
			_, err := m.reject(ctx, tx, property, q, decisionMeta{via: "public_link", ip: httpx.ClientIP(r), userAgent: r.UserAgent(), note: in.Reason})
			return err
		})
}

// ── helpers ───────────────────────────────────────────────────────────────

// action wraps a POST action whose body is decoded by fn.
func action(db *dbtx.DB, fn func(ctx context.Context, tx pgx.Tx, id uuid.UUID, r *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := handle.ID(r)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		ctx := r.Context()
		var out any
		err = db.WithTx(ctx, func(tx pgx.Tx) error {
			var err error
			out, err = fn(ctx, tx, id, r)
			return err
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

// inProperty checks that a row belongs to the property of the request.
func inProperty(ctx context.Context, q dbtx.Querier, table string, rid uuid.UUID, what string) error {
	var ok bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1 AND property_id = $2)`, rid, handle.Property(ctx)).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return errs.NotFound(what)
	}
	return nil
}

var _ = clock.Now
