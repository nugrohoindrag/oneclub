package journey

// Routes of CRM → Journeys (PRD P5 §11: /crm/journeys (+ :activate,
// :pause), /crm/journeys/{id}/enrollments, /crm/experiments), the Member
// App Offers and the tracked journey link; jobs and catalogue.

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/provision"
)

// Permissions of journeys.
func Permissions() []catalog.Permission {
	return catalog.P("crm", "journey", "view", "create", "update", "delete", "activate", "run", "enroll")
}

// RolePermissions grant journeys to CRM, marketing and management.
func RolePermissions() map[string][]string {
	all := []string{"crm.journey.view", "crm.journey.create", "crm.journey.update", "crm.journey.delete", "crm.journey.activate", "crm.journey.run",
		"crm.journey.enroll"}
	view := []string{"crm.journey.view"}
	return map[string][]string{
		"property_admin": all, "crm_admin": all, "marketing_staff": all,
		"general_manager": view, "club_manager": view, "sales_executive": view, "finance_manager": view,
		"membership_manager": {"crm.journey.view", "crm.journey.enroll"},
	}
}

// Templates are the notification templates of journeys.
func Templates() []provision.Template {
	t := map[string][2]string{
		"en": {"{{.subject}}", "{{.message}}{{if .link}}\n\n{{.link}}{{end}}{{if .preferencesLink}}\n\nCommunication preferences: {{.preferencesLink}}{{end}}"},
		"id": {"{{.subject}}", "{{.message}}{{if .link}}\n\n{{.link}}{{end}}{{if .preferencesLink}}\n\nPreferensi komunikasi: {{.preferencesLink}}{{end}}"},
	}
	var out []provision.Template
	for loc, c := range t {
		for _, ch := range []string{"in_app", "email", "whatsapp"} {
			out = append(out, provision.Template{Event: MessageTemplate, Channel: ch, Locale: loc, Subject: c[0], Body: c[1]})
		}
	}
	return out
}

// JourneyEnrollInput enrolls customers manually.
type JourneyEnrollInput struct {
	CustomerIDs []uuid.UUID `json:"customerIds"`
	Occurrence  string      `json:"occurrence,omitempty" doc:"Default: manual:<today>"`
}

// JourneyEnrollResult reports a manual enrollment.
type JourneyEnrollResult struct {
	Enrolled int `json:"enrolled"`
	Skipped  int `json:"skipped" doc:"Already enrolled, re-entry window or inactive customer"`
}

// JourneyCompleteInput completes a journey.
type JourneyCompleteInput struct {
	Reason string `json:"reason"`
}

// JourneyExitInput takes a customer out of a journey.
type JourneyExitInput struct {
	Reason string `json:"reason"`
}

func (s *Service) param(ctx context.Context, q dbtx.Querier, r *http.Request) (Journey, error) {
	jid, err := handle.ID(r)
	if err != nil {
		return Journey{}, err
	}
	return Get(ctx, q, handle.Property(ctx), jid, false)
}

func (s *Service) audit(ctx context.Context, tx pgx.Tx, action string, before, after Journey, reason string) error {
	pid := handle.Property(ctx)
	var b any
	if before.ID != uuid.Nil {
		b = map[string]any{"status": before.Status, "steps": len(before.Steps)}
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: action, EntityType: "crm.journey", EntityID: after.ID.String(),
		EntityLabel: after.Code + " · " + after.Name, PropertyID: &pid, Reason: reason, Before: b, After: after})
}

// Register adds the journey routes (wired by internal/app).
func (s *Service) Register(reg *route.Registry) {
	db := s.DB
	add := func(rt route.Route) {
		rt.Module, rt.Scope, rt.Tag = "crm", route.ScopeProperty, "Journeys"
		reg.Add(rt)
	}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/journeys", Summary: "Journeys", Permission: "crm.journey.view", Response: Journey{}, List: true,
		Query: []route.Param{{Name: "q"}, {Name: "filter[status]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Journey], error) {
			lp := httpx.ParseList(r)
			return handle.Page(List(ctx, tx, handle.Property(ctx), lp.Filters["status"], lp.Q, lp.Limit))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/journey-templates", Summary: "Journey templates (priority journeys of the club)",
		Permission: "crm.journey.view", Response: TemplateInfo{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[TemplateInfo], error) {
			return handle.Page(TemplateInfos, nil)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/journeys", Summary: "Create a journey (draft)", Permission: "crm.journey.create",
		Request: JourneyInput{}, Response: Journey{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in JourneyInput) (Journey, error) {
			j, err := s.Create(ctx, tx, handle.Property(ctx), in, "custom")
			if err != nil {
				return j, err
			}
			return j, s.audit(ctx, tx, audit.ActionCreate, Journey{}, j, "")
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/journeys:from-template", Summary: "Create a journey from a template (draft)",
		Permission: "crm.journey.create", Request: TemplateRequest{}, Response: Journey{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TemplateRequest) (Journey, error) {
			j, err := s.FromTemplate(ctx, tx, handle.Property(ctx), in)
			if err != nil {
				return j, err
			}
			return j, s.audit(ctx, tx, audit.ActionCreate, Journey{}, j, "template "+in.Template)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/journeys/{id}", Summary: "Journey with its steps", Permission: "crm.journey.view", Response: Journey{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Journey, error) { return s.param(ctx, tx, r) })})
	add(route.Route{Method: http.MethodPut, Path: "/api/v1/crm/journeys/{id}", Summary: "Change a draft or paused journey (steps replaced)",
		Permission: "crm.journey.update", Request: JourneyInput{}, Response: Journey{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in JourneyInput) (Journey, error) {
			jid, err := handle.ID(r)
			if err != nil {
				return Journey{}, err
			}
			before, after, err := s.Update(ctx, tx, handle.Property(ctx), jid, in)
			if err != nil {
				return after, err
			}
			return after, s.audit(ctx, tx, audit.ActionUpdate, before, after, "")
		})})
	type transition func(ctx context.Context, tx pgx.Tx, property, jid uuid.UUID) (Journey, Journey, error)
	for _, t := range []struct {
		verb, summary string
		fn            transition
	}{{"activate", "Activate (approval) or resume a journey", s.Activate}, {"pause", "Pause a journey", s.Pause}} {
		t := t
		add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/journeys/{id}:" + t.verb, Summary: t.summary, Permission: "crm.journey.activate",
			Response: Journey{}, Status: http.StatusOK,
			Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (Journey, error) {
				jid, err := handle.ID(r)
				if err != nil {
					return Journey{}, err
				}
				before, after, err := t.fn(ctx, tx, handle.Property(ctx), jid)
				if err != nil {
					return after, err
				}
				return after, s.audit(ctx, tx, t.verb, before, after, "")
			})})
	}
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/journeys/{id}:complete", Summary: "Complete a journey (open enrollments exit)",
		Permission: "crm.journey.activate", Request: JourneyCompleteInput{}, Response: Journey{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in JourneyCompleteInput) (Journey, error) {
			jid, err := handle.ID(r)
			if err != nil {
				return Journey{}, err
			}
			before, after, err := s.Complete(ctx, tx, handle.Property(ctx), jid, in.Reason)
			if err != nil {
				return after, err
			}
			return after, s.audit(ctx, tx, "complete", before, after, in.Reason)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/journeys/{id}:archive", Summary: "Archive a draft or completed journey",
		Permission: "crm.journey.delete", Response: Journey{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (Journey, error) {
			jid, err := handle.ID(r)
			if err != nil {
				return Journey{}, err
			}
			j, err := s.Archive(ctx, tx, handle.Property(ctx), jid)
			if err != nil {
				return j, err
			}
			return j, s.audit(ctx, tx, audit.ActionArchive, j, j, "")
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/journeys/{id}:run", Summary: "Run a journey now (triggers and due steps)",
		Permission: "crm.journey.run", Response: RunResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (RunResult, error) {
			jid, err := handle.ID(r)
			if err != nil {
				return RunResult{}, err
			}
			pid := handle.Property(ctx)
			if _, err := Get(ctx, tx, pid, jid, false); err != nil {
				return RunResult{}, err
			}
			res, err := s.RunJourney(ctx, tx, pid, jid)
			if err != nil {
				return res, err
			}
			return res, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "run", EntityType: "crm.journey", EntityID: jid.String(),
				EntityLabel: "journey run", PropertyID: &pid, After: res})
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/journeys:run", Summary: "Run every active journey of the property now",
		Permission: "crm.journey.run", Response: RunResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (RunResult, error) {
			pid := handle.Property(ctx)
			res, err := s.RunProperty(ctx, tx, pid)
			if err != nil {
				return res, err
			}
			return res, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "run", EntityType: "crm.journey", EntityID: pid.String(),
				EntityLabel: "journeys run", PropertyID: &pid, After: res})
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/journeys/{id}/enrollments", Summary: "Enrollments of a journey", Permission: "crm.journey.view",
		Response: JourneyEnrollment{}, List: true, Query: []route.Param{{Name: "filter[status]"}, {Name: "filter[customerId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[JourneyEnrollment], error) {
			j, err := s.param(ctx, tx, r)
			if err != nil {
				return httpx.Page[JourneyEnrollment]{}, err
			}
			lp := httpx.ParseList(r)
			return handle.Page(Enrollments(ctx, tx, j.ID, lp.Filters["status"], lp.Filters["customerId"], lp.Limit))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/journeys/{id}/enrollments", Summary: "Enroll customers in an active journey",
		Permission: "crm.journey.enroll", Request: JourneyEnrollInput{}, Response: JourneyEnrollResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in JourneyEnrollInput) (JourneyEnrollResult, error) {
			j, err := s.param(ctx, tx, r)
			if err != nil {
				return JourneyEnrollResult{}, err
			}
			if len(in.CustomerIDs) == 0 {
				return JourneyEnrollResult{}, handle.Invalid("customerIds", "required", "choose at least one customer")
			}
			if j.Status != "active" {
				return JourneyEnrollResult{}, handle.Invalid("journey", "journey_not_active", "the journey is "+j.Status)
			}
			occ := in.Occurrence
			if occ == "" {
				occ = "manual:" + localDay(now(), location(ctx, tx, j.PropertyID)).Format("2006-01-02")
			}
			var out JourneyEnrollResult
			for _, c := range in.CustomerIDs {
				_, created, err := s.enroll(ctx, tx, j, EnrollRequest{Customer: c, Occurrence: occ, Context: map[string]any{"source": "manual"}})
				if err != nil {
					return out, err
				}
				if created {
					out.Enrolled++
				} else {
					out.Skipped++
				}
			}
			pid := handle.Property(ctx)
			return out, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "enroll", EntityType: "crm.journey", EntityID: j.ID.String(),
				EntityLabel: j.Code, PropertyID: &pid, After: out, Metadata: map[string]any{"customerIds": in.CustomerIDs}})
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/journeys/{id}/enrollments/{enrollmentId}:exit", Summary: "Take a customer out of a journey",
		Permission: "crm.journey.enroll", Request: JourneyExitInput{}, Response: JourneyEnrollment{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in JourneyExitInput) (JourneyEnrollment, error) {
			j, err := s.param(ctx, tx, r)
			if err != nil {
				return JourneyEnrollment{}, err
			}
			if err := handle.Required("reason", in.Reason); err != nil {
				return JourneyEnrollment{}, err
			}
			eid, err := handle.ID(r, "enrollmentId")
			if err != nil {
				return JourneyEnrollment{}, err
			}
			tag, err := tx.Exec(ctx, `UPDATE crm.journey_enrollments SET status = 'exited', exited_at = now(), exit_reason = $3, current_key = NULL,
				next_run_at = NULL WHERE id = $1 AND journey_id = $2 AND status = 'active'`, eid, j.ID, "manual: "+in.Reason)
			if err != nil {
				return JourneyEnrollment{}, err
			}
			if tag.RowsAffected() == 0 {
				return JourneyEnrollment{}, handle.Invalid("enrollmentId", "not_active", "no active enrollment of this journey")
			}
			rows, err := tx.Query(ctx, enrollmentSelect+` WHERE e.id = $1`, eid)
			out, err := handle.One[JourneyEnrollment](rows, err, "enrollment")
			if err != nil {
				return out, err
			}
			pid := handle.Property(ctx)
			return out, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "exit", EntityType: "crm.journey_enrollment", EntityID: eid.String(),
				EntityLabel: j.Code + " · " + out.CustomerName, PropertyID: &pid, Reason: in.Reason, After: map[string]any{"status": "exited"}})
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/journeys/{id}/events", Summary: "Executed steps of a journey (journey events)",
		Permission: "crm.journey.view", Response: JourneyEvent{}, List: true, Query: []route.Param{{Name: "filter[enrollmentId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[JourneyEvent], error) {
			j, err := s.param(ctx, tx, r)
			if err != nil {
				return httpx.Page[JourneyEvent]{}, err
			}
			lp := httpx.ParseList(r)
			return handle.Page(Events(ctx, tx, j.ID, lp.Filters["enrollmentId"], lp.Limit))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/journeys/{id}/performance", Summary: "Performance per journey and step",
		Permission: "crm.journey.view", Response: JourneyPerformance{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (JourneyPerformance, error) {
			jid, err := handle.ID(r)
			if err != nil {
				return JourneyPerformance{}, err
			}
			return Performance(ctx, tx, handle.Property(ctx), jid)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/experiments", Summary: "A/B tests and control groups of the journeys",
		Permission: "crm.journey.view", Response: JourneyExperiment{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[JourneyExperiment], error) {
			return handle.Page(Experiments(ctx, tx, handle.Property(ctx)))
		})})
	// Member App Offers (EP-26, §7.4).
	me := func(rt route.Route) { crm.MeRoute(reg, "crm", "Member Portal", rt) }
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/journey-offers", Summary: "My personal offers (journeys)", Response: MemberOffer{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[MemberOffer], error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[MemberOffer]{}, err
			}
			return handle.Page(Offers(ctx, tx, c.ID, localDay(now(), location(ctx, tx, c.PropertyID))))
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/journey-offers/{id}:read", Summary: "Mark an offer as read", Response: MemberOffer{},
		Status: http.StatusOK, Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (MemberOffer, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return MemberOffer{}, err
			}
			oid, err := handle.ID(r)
			if err != nil {
				return MemberOffer{}, err
			}
			o, err := MarkOfferRead(ctx, tx, c.ID, oid)
			if err != nil {
				return o, err
			}
			pid := c.PropertyID
			return o, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "read", EntityType: "crm.journey_event", EntityID: oid.String(),
				EntityLabel: o.Title, PropertyID: &pid})
		})})
	// Tracked link of journey messages (click → Member App Offers).
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/public/journey-links/{token}", Summary: "Tracked journey link (redirects to the offers)",
		Tag: "Public", Module: "crm", Auth: route.AuthPublic, RawContent: "text/html",
		Handler: linkLimiter.Wrap(func(w http.ResponseWriter, r *http.Request) {
			ctx := dbtx.WithScope(r.Context(), dbtx.Scope{AllProperties: true})
			if err := db.WithTx(ctx, func(tx pgx.Tx) error { return TrackClick(ctx, tx, chi.URLParam(r, "token")) }); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			target := s.portal() + "/loyalty/offers"
			if s.portal() == "" {
				target = s.website() + "/"
			}
			http.Redirect(w, r, target, http.StatusFound)
		})})
}

var linkLimiter = &handle.Limiter{N: 60, Period: time.Minute}

// ── jobs ──────────────────────────────────────────────────────────────────

// RunArgs runs the active journeys.
type RunArgs struct{}

func (RunArgs) Kind() string { return "crm_journeys_run" }

func (RunArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

type runWorker struct {
	river.WorkerDefaults[RunArgs]
	S *Service
}

func (w *runWorker) Work(ctx context.Context, _ *river.Job[RunArgs]) error {
	_, err := w.S.RunDue(ctx)
	return err
}

// RegisterJobs runs the journeys every 5 minutes.
func (s *Service) RegisterJobs(reg *jobs.Registrar) {
	river.AddWorker(reg.Workers, &runWorker{S: s})
	reg.Periodic = append(reg.Periodic, river.NewPeriodicJob(river.PeriodicInterval(5*time.Minute),
		func() (river.JobArgs, *river.InsertOpts) { return RunArgs{}, nil }, nil))
}
