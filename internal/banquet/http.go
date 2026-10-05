package banquet

// HTTP API of Banquet & Event (PRD P3 §11: /api/v1/banquet/events with
// :make-definite, :complete, :cancel, schedule, participants, checklist,
// vendors, packages, menus, menu-selection, venues, venue-calendar; BEOs with
// :issue, :revise, :acknowledge, pdf and the procurement requirement).

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

func (m *Module) add(reg *route.Registry, rt route.Route) {
	rt.Module, rt.Scope = "banquet", route.ScopeProperty
	if rt.Tag == "" {
		rt.Tag = tag
	}
	reg.Add(rt)
}

// Register adds the routes and master data of the module.
func (m *Module) Register(reg *route.Registry, eng *resource.Engine) {
	m.hooks()
	for _, d := range Defs() {
		eng.Register(reg, d)
	}
	m.registerEvents(reg)
	m.registerBilling(reg)
	m.registerOperations(reg)
	m.registerBEO(reg)
	m.registerImport(reg)
	m.registerPublic(reg)
	m.registerMember(reg)
}

// owned checks that a row of a banquet table belongs to the property of
// the request.
func owned(ctx context.Context, q dbtx.Querier, table string, rid uuid.UUID, what string) error {
	var ok bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM banquet.`+table+` WHERE id = $1 AND property_id = $2)`, rid, handle.Property(ctx)).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return errs.NotFound(what)
	}
	return nil
}

// write is handle.Write for a /{id} route of a banquet table.
func write[Req any, Res any](db *dbtx.DB, status int, table, what string, fn func(ctx context.Context, tx pgx.Tx, rid uuid.UUID, r *http.Request, in Req) (Res, error)) http.HandlerFunc {
	return handle.Write(db, status, func(ctx context.Context, tx pgx.Tx, r *http.Request, in Req) (Res, error) {
		var zero Res
		rid, err := handle.ID(r)
		if err != nil {
			return zero, err
		}
		if err := owned(ctx, tx, table, rid, what); err != nil {
			return zero, err
		}
		return fn(ctx, tx, rid, r, in)
	})
}

// read is handle.Read for a /{id} route of a banquet table.
func read[Res any](db *dbtx.DB, table, what string, fn func(ctx context.Context, tx pgx.Tx, rid uuid.UUID, r *http.Request) (Res, error)) http.HandlerFunc {
	return handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Res, error) {
		var zero Res
		rid, err := handle.ID(r)
		if err != nil {
			return zero, err
		}
		if err := owned(ctx, tx, table, rid, what); err != nil {
			return zero, err
		}
		return fn(ctx, tx, rid, r)
	})
}

// readTagged reads a /{id} resource and sets its ETag (Technical Doc §8.1).
func readTagged[Res any](db *dbtx.DB, table, what string, fn func(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (Res, int, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rid, err := handle.ID(r)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		var out Res
		var version int
		err = db.WithReadTx(r.Context(), func(tx pgx.Tx) error {
			if err := owned(r.Context(), tx, table, rid, what); err != nil {
				return err
			}
			out, version, err = fn(r.Context(), tx, rid)
			return err
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		w.Header().Set("ETag", fmt.Sprintf(`"%d"`, version))
		httpx.JSON(w, http.StatusOK, out)
	}
}

// period reads from / to (YYYY-MM-DD, local days, to inclusive).
func period(ctx context.Context, q dbtx.Querier, r *http.Request, defDays int) (time.Time, time.Time, error) {
	loc := calendar.Location(ctx, q)
	today := localDay(clock.Now(), loc)
	from, to := today, today.AddDate(0, 0, defDays)
	if v := r.URL.Query().Get("from"); v != "" {
		d, err := time.ParseInLocation("2006-01-02", v, loc)
		if err != nil {
			return from, to, handle.Invalid("from", "invalid_date", "YYYY-MM-DD")
		}
		from = d
	}
	if v := r.URL.Query().Get("to"); v != "" {
		d, err := time.ParseInLocation("2006-01-02", v, loc)
		if err != nil {
			return from, to, handle.Invalid("to", "invalid_date", "YYYY-MM-DD")
		}
		to = d
	}
	if to.Before(from) {
		return from, to, handle.Invalid("to", "invalid_period", "to must not be before from")
	}
	return from, to.AddDate(0, 0, 1), nil
}

// ReasonInput carries a reason.
type BanquetReasonInput struct {
	Reason string `json:"reason,omitempty"`
}

// IssueBEOInput issues a draft BEO.
type IssueBEOInput struct {
	Reason string `json:"reason,omitempty" doc:"Recorded with the version (required for a revision)"`
}

// ── events ────────────────────────────────────────────────────────────────

func (m *Module) registerEvents(reg *route.Registry) {
	db := m.DB
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/banquet/events", Summary: "Events (Events, Banquet, MICE, Weddings; event calendar by period)",
		Permission: "banquet.event.view", Response: BanquetEvent{}, List: true,
		Query: []route.Param{{Name: "q"}, {Name: "filter[status]", Description: "Comma separated: inquiry, tentative, definite, completed, cancelled"},
			{Name: "filter[category]", Description: "wedding, banquet, mice, social, sport, tournament, other (comma separated)"},
			{Name: "filter[eventTypeId]"}, {Name: "filter[ownerUserId]"}, {Name: "from", Description: "YYYY-MM-DD (default: a year back)"},
			{Name: "to", Description: "YYYY-MM-DD (default: two years ahead)"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[BanquetEvent], error) {
			lp := httpx.ParseList(r)
			loc := calendar.Location(ctx, tx)
			today := localDay(clock.Now(), loc)
			from, to := today.AddDate(-1, 0, 0), today.AddDate(2, 0, 1)
			if r.URL.Query().Get("from") != "" || r.URL.Query().Get("to") != "" {
				var err error
				if from, to, err = period(ctx, tx, r, 730); err != nil {
					return httpx.Page[BanquetEvent]{}, err
				}
			}
			f := EventFilter{Status: lp.Filters["status"], Category: lp.Filters["category"], Text: lp.Q, From: from, To: to, Limit: lp.Limit}
			if v := lp.Filters["eventTypeId"]; v != "" {
				u, err := uuid.Parse(v)
				if err != nil {
					return httpx.Page[BanquetEvent]{}, handle.Invalid("filter[eventTypeId]", "invalid", "uuid")
				}
				f.EventTypeID = &u
			}
			if v := lp.Filters["ownerUserId"]; v != "" {
				u, err := uuid.Parse(v)
				if err != nil {
					return httpx.Page[BanquetEvent]{}, handle.Invalid("filter[ownerUserId]", "invalid", "uuid")
				}
				f.OwnerID = &u
			}
			return handle.Page(m.List(ctx, tx, handle.Property(ctx), f))
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/events", Summary: "Create Event (inquiry; venue holds and package optional)",
		Permission: "banquet.event.create", Request: EventInput{}, Response: EventDetail{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in EventInput) (EventDetail, error) {
			return m.CreateEvent(ctx, tx, handle.Property(ctx), in)
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/banquet/events/{id}", Summary: "Event with venues, schedule, menu, resources, vendors, billing and BEO (ETag)",
		Permission: "banquet.event.view", Response: EventDetail{},
		Handler: readTagged(db, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID) (EventDetail, int, error) {
			d, err := m.Detail(ctx, tx, eid)
			return d, d.Version, err
		})})
	m.add(reg, route.Route{Method: http.MethodPatch, Path: "/api/v1/banquet/events/{id}", Summary: "Update an event (If-Match: version)",
		Permission: "banquet.event.update", Request: EventPatch{}, Response: EventDetail{},
		Handler: write(db, http.StatusOK, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request, in EventPatch) (EventDetail, error) {
			return m.UpdateEvent(ctx, tx, eid, in, r.Header.Get("If-Match"))
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/events/{id}/venues", Summary: "Hold a venue (Tentative until the option date; waitlist when taken)",
		Permission: "banquet.event.hold", Request: VenueHoldInput{}, Response: VenueHold{},
		Handler: write(db, http.StatusCreated, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request, in VenueHoldInput) (VenueHold, error) {
			return m.HoldVenue(ctx, tx, eid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/events/{id}/venues/{holdId}:release", Summary: "Release a venue hold (waitlist moves up)",
		Permission: "banquet.event.hold", Request: BanquetReasonInput{}, Response: EventDetail{},
		Handler: write(db, http.StatusOK, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request, in BanquetReasonInput) (EventDetail, error) {
			hid, err := handle.ID(r, "holdId")
			if err != nil {
				return EventDetail{}, err
			}
			return m.ReleaseHold(ctx, tx, eid, hid, in.Reason)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/events/{id}:extend-option", Summary: "Move the option date of the tentative holds",
		Permission: "banquet.event.hold", Request: ExtendOptionInput{}, Response: EventDetail{},
		Handler: write(db, http.StatusOK, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request, in ExtendOptionInput) (EventDetail, error) {
			return m.ExtendOption(ctx, tx, eid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/events/{id}:make-definite",
		Summary: "Make the event Definite (down payment received, or override with approval)", Permission: "banquet.event.confirm",
		Request: DefiniteInput{}, Response: DefiniteResult{},
		Handler: write(db, http.StatusOK, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request, in DefiniteInput) (DefiniteResult, error) {
			return m.MakeDefinite(ctx, tx, eid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/events/{id}:cancel",
		Summary: "Cancel the event (Banquet Policies cancellation tiers: forfeited DP, refund)", Permission: "banquet.event.cancel",
		Request: EventCancelInput{}, Response: EventCancelResult{},
		Handler: write(db, http.StatusOK, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request, in EventCancelInput) (EventCancelResult, error) {
			if in.WaiveFee && !canWaive(ctx, handle.Property(ctx)) {
				return EventCancelResult{}, errs.Forbidden("waiving the cancellation fee needs banquet.billing.manage")
			}
			return m.CancelEvent(ctx, tx, eid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/events/{id}:complete",
		Summary: "Complete the event with the final pax (BEO locked, consumption to P4)", Permission: "banquet.event.complete",
		Request: EventCompleteInput{}, Response: EventDetail{},
		Handler: write(db, http.StatusOK, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request, in EventCompleteInput) (EventDetail, error) {
			return m.CompleteEvent(ctx, tx, eid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/events/{id}/package", Summary: "Price the event with a banquet package (minimum pax, inclusions)",
		Permission: "banquet.event.update", Request: EventPackageInput{}, Response: EventDetail{},
		Handler: write(db, http.StatusOK, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request, in EventPackageInput) (EventDetail, error) {
			return m.SetPackage(ctx, tx, eid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/events/{id}:guarantee-pax",
		Summary: "Set the guaranteed (final) pax: locked at the cut-off, later increases charged", Permission: "banquet.event.update",
		Request: GuaranteedPaxInput{}, Response: EventDetail{},
		Handler: write(db, http.StatusOK, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request, in GuaranteedPaxInput) (EventDetail, error) {
			return m.GuaranteePax(ctx, tx, eid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPut, Path: "/api/v1/banquet/events/{id}/menu-selection",
		Summary: "Menu Selection of one menu (category quotas; extra choices charged or refused)", Permission: "banquet.event.update",
		Request: MenuSelectionInput{}, Response: EventDetail{},
		Handler: write(db, http.StatusOK, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request, in MenuSelectionInput) (EventDetail, error) {
			return m.SelectMenu(ctx, tx, eid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/banquet/events/{id}/schedule", Summary: "Event Schedule (run-of-show)",
		Permission: "banquet.event.view", Response: RundownItem{}, List: true,
		Handler: read(db, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request) (httpx.Page[RundownItem], error) {
			return handle.Page(m.schedule(ctx, tx, eid))
		})})
	m.add(reg, route.Route{Method: http.MethodPut, Path: "/api/v1/banquet/events/{id}/schedule", Summary: "Replace the run-of-show (sessions with venue and person in charge)",
		Permission: "banquet.event.update", Request: RundownInput{}, Response: RundownItem{}, List: true,
		Handler: write(db, http.StatusOK, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request, in RundownInput) (httpx.Page[RundownItem], error) {
			return handle.Page(m.SetSchedule(ctx, tx, eid, in))
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/events/{id}/resources",
		Summary: "Book another resource for the event (bungalow, meeting room, equipment …)", Permission: "banquet.event.hold",
		Request: EventResourceInput{}, Response: EventResource{},
		Handler: write(db, http.StatusCreated, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request, in EventResourceInput) (EventResource, error) {
			return m.AddResource(ctx, tx, eid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/event-resources/{id}:release", Summary: "Release a resource of an event",
		Permission: "banquet.event.hold", Request: BanquetReasonInput{}, Response: EventResource{},
		Handler: write(db, http.StatusOK, "event_resources", "event resource", func(ctx context.Context, tx pgx.Tx, xid uuid.UUID, r *http.Request, in BanquetReasonInput) (EventResource, error) {
			return m.ReleaseResource(ctx, tx, xid, in.Reason)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/events/{id}/vendors", Summary: "Book a vendor (decoration, MC, band, photographer …)",
		Permission: "banquet.event.update", Request: EventVendorInput{}, Response: EventVendor{},
		Handler: write(db, http.StatusCreated, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request, in EventVendorInput) (EventVendor, error) {
			return m.AddVendor(ctx, tx, eid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/event-vendors/{id}:cancel", Summary: "Cancel a vendor booking (fee charge voided)",
		Permission: "banquet.event.update", Request: BanquetReasonInput{}, Response: EventVendor{},
		Handler: write(db, http.StatusOK, "event_vendors", "event vendor", func(ctx context.Context, tx pgx.Tx, xid uuid.UUID, r *http.Request, in BanquetReasonInput) (EventVendor, error) {
			return m.CancelVendor(ctx, tx, xid, in.Reason)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/events/{id}/meetings", Summary: "Schedule a Food Tasting, Technical Meeting or site visit",
		Permission: "banquet.event.update", Request: EventMeetingInput{}, Response: EventMeeting{},
		Handler: write(db, http.StatusCreated, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request, in EventMeetingInput) (EventMeeting, error) {
			return m.ScheduleMeeting(ctx, tx, eid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/event-meetings/{id}:record",
		Summary: "Record the outcome of a meeting (changes forwarded to the next BEO)", Permission: "banquet.event.update",
		Request: EventMeetingRecord{}, Response: EventMeeting{},
		Handler: write(db, http.StatusOK, "event_meetings", "meeting", func(ctx context.Context, tx pgx.Tx, mid uuid.UUID, r *http.Request, in EventMeetingRecord) (EventMeeting, error) {
			return m.RecordMeeting(ctx, tx, mid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/banquet/venue-calendar",
		Summary: "Venue Calendar: holds (tentative, definite, waitlist) and bookings per venue", Permission: "banquet.event.view",
		Response: VenueCalendarRow{}, List: true, Query: []route.Param{{Name: "from"}, {Name: "to"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[VenueCalendarRow], error) {
			from, to, err := period(ctx, tx, r, 13)
			if err != nil {
				return httpx.Page[VenueCalendarRow]{}, err
			}
			return handle.Page(m.VenueCalendar(ctx, tx, handle.Property(ctx), from, to))
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/banquet/venue-availability",
		Summary: "Venues free for a period, pax and layout (setup / teardown buffers)", Permission: "banquet.event.view",
		Response: BanquetVenueAvailability{}, List: true,
		Query: []route.Param{{Name: "start", Required: true, Description: "RFC 3339"}, {Name: "end", Required: true}, {Name: "pax", Type: "integer"}, {Name: "layout"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[BanquetVenueAvailability], error) {
			start, err := handle.QueryTime(r, "start", time.Time{})
			if err != nil {
				return httpx.Page[BanquetVenueAvailability]{}, err
			}
			end, err := handle.QueryTime(r, "end", time.Time{})
			if err != nil {
				return httpx.Page[BanquetVenueAvailability]{}, err
			}
			if start.IsZero() || !end.After(start) {
				return httpx.Page[BanquetVenueAvailability]{}, handle.Invalid("end", "invalid_period", "start and end are required; end after start")
			}
			layout := r.URL.Query().Get("layout")
			if !validLayout(layout) {
				return httpx.Page[BanquetVenueAvailability]{}, handle.Invalid("layout", "invalid", "unknown layout")
			}
			return handle.Page(m.Availability(ctx, tx, handle.Property(ctx), start, end, handle.QueryInt(r, "pax", 0), layout))
		})})
}

// ── billing ───────────────────────────────────────────────────────────────

func (m *Module) registerBilling(reg *route.Registry) {
	db := m.DB
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/banquet/events/{id}/billing",
		Summary: "Event Billing: charges per component, folio, payment schedule, deposits and the final invoice", Permission: "banquet.billing.view",
		Response: EventBilling{},
		Handler: read(db, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request) (EventBilling, error) {
			return m.Billing2(ctx, tx, eid)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/events/{id}/charges",
		Summary: "Post an extra charge (corkage, outdoor add-on, electricity, overtime, F&B, damage …)", Permission: "banquet.billing.manage",
		Request: EventChargeInput{}, Response: EventCharge{},
		Handler: write(db, http.StatusCreated, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request, in EventChargeInput) (EventCharge, error) {
			return m.AddCharge(ctx, tx, eid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/event-charges/{id}:void", Summary: "Void a charge of an event (reason)",
		Permission: "banquet.billing.manage", Request: EventChargeVoid{}, Response: EventCharge{},
		Handler: write(db, http.StatusOK, "event_charges", "charge", func(ctx context.Context, tx pgx.Tx, cid uuid.UUID, r *http.Request, in EventChargeVoid) (EventCharge, error) {
			return m.VoidEventCharge(ctx, tx, cid, in.Reason)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/events/{id}/payment-schedule",
		Summary: "Payment Schedule of the event (DP, terms, final payment; default Banquet Policies)", Permission: "banquet.billing.manage",
		Request: EventScheduleRequest{}, Response: EventBilling{},
		Handler: write(db, http.StatusCreated, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request, in EventScheduleRequest) (EventBilling, error) {
			return m.BuildSchedule(ctx, tx, eid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/events/{id}:final-billing",
		Summary: "Final Billing: deposits applied, overpayment refunded, balance paid or invoiced", Permission: "banquet.billing.manage",
		Request: EventFinalBillInput{}, Response: EventFinalBill{},
		Handler: write(db, http.StatusOK, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request, in EventFinalBillInput) (EventFinalBill, error) {
			return m.FinalBilling(ctx, tx, eid, in)
		})})
}

// ── operations: checklist, participants, incidents, today ────────────────

// ChecklistRow is a checklist task with its event (Event Checklist).
type EventChecklistRow struct {
	EventChecklistItem
	EventNumber string    `json:"eventNumber" db:"event_number"`
	EventTitle  string    `json:"eventTitle" db:"event_title"`
	EventStart  time.Time `json:"eventStart" db:"event_start"`
}

func (m *Module) registerOperations(reg *route.Registry) {
	db := m.DB
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/banquet/checklist-items",
		Summary: "Event Checklist across events (overdue tasks first)", Permission: "banquet.checklist.view", Response: EventChecklistRow{}, List: true,
		Query: []route.Param{{Name: "filter[status]", Description: "open, done"}, {Name: "filter[department]"}, {Name: "filter[eventId]"},
			{Name: "overdue", Description: "true: only overdue tasks"}, {Name: "mine", Description: "true: only my tasks"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[EventChecklistRow], error) {
			lp := httpx.ParseList(r)
			mine := ""
			if r.URL.Query().Get("mine") == "true" {
				mine = handle.UserID(ctx).String()
			}
			items, err := handle.List[EventChecklistRow](tx.Query(ctx, `SELECT x.*, e.number AS event_number, e.title AS event_title, e.start_at AS event_start
				FROM (`+checklistSelect+`) x JOIN banquet.events e ON e.id = x.event_id
				WHERE e.property_id = $1 AND e.status IN ('inquiry', 'tentative', 'definite') AND ($3 = '' OR x.status = $3) AND ($4 = '' OR x.department = $4)
				AND ($5 = '' OR x.event_id::text = $5) AND ($6 = '' OR x.overdue) AND ($7 = '' OR x.owner_user_id::text = $7)
				ORDER BY x.overdue DESC, x.due_date NULLS LAST, e.start_at LIMIT $8`, handle.Property(ctx), today(ctx, tx), lp.Filters["status"],
				lp.Filters["department"], lp.Filters["eventId"], strings.TrimSuffix(r.URL.Query().Get("overdue"), "false"), mine, lp.Limit))
			return handle.Page(items, err)
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/banquet/events/{id}/checklist", Summary: "Checklist of an event",
		Permission: "banquet.checklist.view", Response: EventChecklistItem{}, List: true,
		Handler: read(db, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request) (httpx.Page[EventChecklistItem], error) {
			return handle.Page(m.Checklist(ctx, tx, eid))
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/events/{id}/checklist", Summary: "Add a checklist task (PIC, due date)",
		Permission: "banquet.checklist.update", Request: EventChecklistInput{}, Response: EventChecklistItem{},
		Handler: write(db, http.StatusCreated, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request, in EventChecklistInput) (EventChecklistItem, error) {
			return m.AddChecklistItem(ctx, tx, eid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/events/{id}/checklist:apply-template", Summary: "Add the tasks of a checklist template",
		Permission: "banquet.checklist.update", Request: ChecklistTemplateApply{}, Response: EventChecklistItem{}, List: true,
		Handler: write(db, http.StatusOK, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request, in ChecklistTemplateApply) (httpx.Page[EventChecklistItem], error) {
			return handle.Page(m.ApplyTemplate(ctx, tx, eid, in))
		})})
	m.add(reg, route.Route{Method: http.MethodPatch, Path: "/api/v1/banquet/checklist-items/{id}", Summary: "Change a checklist task (task, PIC, due date)",
		Permission: "banquet.checklist.update", Request: EventChecklistInput{}, Response: EventChecklistItem{},
		Handler: write(db, http.StatusOK, "event_checklist_items", "checklist item", func(ctx context.Context, tx pgx.Tx, cid uuid.UUID, r *http.Request, in EventChecklistInput) (EventChecklistItem, error) {
			return m.UpdateChecklistItem(ctx, tx, cid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/checklist-items/{id}:toggle", Summary: "Mark a checklist task done or open",
		Permission: "banquet.checklist.update", Request: ChecklistToggleInput{}, Response: EventChecklistItem{},
		Handler: write(db, http.StatusOK, "event_checklist_items", "checklist item", func(ctx context.Context, tx pgx.Tx, cid uuid.UUID, r *http.Request, in ChecklistToggleInput) (EventChecklistItem, error) {
			return m.ToggleChecklistItem(ctx, tx, cid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/banquet/events/{id}/participants", Summary: "Participants / Guest Registration of an event",
		Permission: "banquet.participant.view", Response: EventParticipant{}, List: true,
		Query: []route.Param{{Name: "q", Description: "Name, company or ticket code"}, {Name: "filter[status]"}},
		Handler: read(db, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request) (httpx.Page[EventParticipant], error) {
			lp := httpx.ParseList(r)
			return handle.Page(m.Participants(ctx, tx, eid, lp.Filters["status"], lp.Q))
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/events/{id}/participants",
		Summary: "Register a guest (capacity and waitlist of Event Policies; QR ticket)", Permission: "banquet.participant.manage",
		Request: EventParticipantInput{}, Response: EventParticipant{},
		Handler: write(db, http.StatusCreated, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request, in EventParticipantInput) (EventParticipant, error) {
			return m.RegisterParticipant(ctx, tx, eid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/events/{id}/participants:import", Summary: "Import a guest list (duplicates skipped)",
		Permission: "banquet.participant.manage", Request: GuestListImport{}, Response: GuestListImportResult{},
		Handler: write(db, http.StatusOK, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request, in GuestListImport) (GuestListImportResult, error) {
			return m.ImportParticipants(ctx, tx, eid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/participants/{id}:withdraw", Summary: "Withdraw a registration (refund per Event Policies)",
		Permission: "banquet.participant.manage", Request: RegistrationWithdrawInput{}, Response: EventParticipant{},
		Handler: write(db, http.StatusOK, "participants", "participant", func(ctx context.Context, tx pgx.Tx, pid uuid.UUID, r *http.Request, in RegistrationWithdrawInput) (EventParticipant, error) {
			return m.Withdraw(ctx, tx, pid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/participants/{id}:check-in", Summary: "Check a guest in (from the list)",
		Permission: "banquet.participant.check_in", Request: EventCheckInInput{}, Response: EventCheckInResult{},
		Handler: write(db, http.StatusOK, "participants", "participant", func(ctx context.Context, tx pgx.Tx, pid uuid.UUID, r *http.Request, in EventCheckInInput) (EventCheckInResult, error) {
			return m.CheckInParticipant(ctx, tx, pid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/events/{id}:check-in", Summary: "Event Check-in by QR ticket code",
		Permission: "banquet.participant.check_in", Request: EventCheckInInput{}, Response: EventCheckInResult{},
		Handler: write(db, http.StatusOK, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request, in EventCheckInInput) (EventCheckInResult, error) {
			return m.CheckInByCode(ctx, tx, eid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/events/{id}/incidents", Summary: "Record an incident note (Event Operations)",
		Permission: "banquet.event.operate", Request: EventIncidentInput{}, Response: EventIncident{},
		Handler: write(db, http.StatusCreated, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request, in EventIncidentInput) (EventIncident, error) {
			return m.AddIncident(ctx, tx, eid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/banquet/today", Summary: "Today's Events (Event Operations): venues, run-of-show, latest BEO, checklist",
		Permission: "banquet.event.view", Response: TodayEvent{}, List: true, Query: []route.Param{{Name: "date", Description: "YYYY-MM-DD (default today)"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[TodayEvent], error) {
			loc := calendar.Location(ctx, tx)
			day := localDay(clock.Now(), loc)
			if v := r.URL.Query().Get("date"); v != "" {
				d, err := time.ParseInLocation("2006-01-02", v, loc)
				if err != nil {
					return httpx.Page[TodayEvent]{}, handle.Invalid("date", "invalid_date", "YYYY-MM-DD")
				}
				day = d
			}
			return handle.Page(m.Today(ctx, tx, handle.Property(ctx), day))
		})})
}

// ── BEO & Banquet Production ─────────────────────────────────────────────

func (m *Module) registerBEO(reg *route.Registry) {
	db := m.DB
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/banquet/beos", Summary: "Banquet Event Orders (current versions; history with filter[status]=superseded)",
		Permission: "banquet.beo.view", Response: BEO{}, List: true,
		Query: []route.Param{{Name: "q"}, {Name: "filter[status]", Description: "draft, issued, superseded"}, {Name: "filter[eventId]"},
			{Name: "from"}, {Name: "to"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[BEO], error) {
			lp := httpx.ParseList(r)
			loc := calendar.Location(ctx, tx)
			today := localDay(clock.Now(), loc)
			from, to := today.AddDate(-1, 0, 0), today.AddDate(2, 0, 1)
			if r.URL.Query().Get("from") != "" || r.URL.Query().Get("to") != "" {
				var err error
				if from, to, err = period(ctx, tx, r, 730); err != nil {
					return httpx.Page[BEO]{}, err
				}
			}
			status := lp.Filters["status"]
			list, err := handle.List[BEO](tx.Query(ctx, beoSelect+` WHERE b.property_id = $1 AND (($2 = '' AND b.status <> 'superseded') OR b.status = ANY(string_to_array($2, ',')))
				AND ($3 = '' OR b.event_id::text = $3) AND b.event_date >= $4::date AND b.event_date < $5::date
				AND ($6 = '' OR b.number ILIKE '%' || $6 || '%' OR e.number ILIKE '%' || $6 || '%' OR e.title ILIKE '%' || $6 || '%')
				ORDER BY b.event_date, b.number, b.version DESC LIMIT $7`, handle.Property(ctx), status, lp.Filters["eventId"], from.Format("2006-01-02"),
				to.Format("2006-01-02"), lp.Q, lp.Limit))
			if err != nil {
				return httpx.Page[BEO]{}, err
			}
			for i := range list {
				if list[i].Departments, err = handle.List[BEODepartment](tx.Query(ctx, `SELECT d.department, d.instructions, d.notified_at, d.acknowledged_at,
					u.full_name AS acknowledged_by FROM banquet.beo_departments d LEFT JOIN platform.users u ON u.id = d.acknowledged_by
					WHERE d.beo_id = $1 ORDER BY d.department`, list[i].ID)); err != nil {
					return httpx.Page[BEO]{}, err
				}
				if list[i].Instructions == nil {
					list[i].Instructions = map[string]string{}
				}
			}
			return handle.Page(list, nil)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/beos", Summary: "Create the draft BEO of an event (function sheet generated from the event)",
		Permission: "banquet.beo.manage", Request: BEOInput{}, Response: BEO{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in BEOInput) (BEO, error) {
			if err := owned(ctx, tx, "events", in.EventID, "event"); err != nil {
				return BEO{}, handle.Invalid("eventId", "not_found", "event not found")
			}
			return m.CreateBEO(ctx, tx, in)
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/banquet/beos/{id}", Summary: "BEO version with changes and distribution status (ETag)",
		Permission: "banquet.beo.view", Response: BEO{},
		Handler: readTagged(db, "beos", "BEO", func(ctx context.Context, tx pgx.Tx, bid uuid.UUID) (BEO, int, error) {
			b, err := m.GetBEO(ctx, tx, bid)
			return b, b.Rev, err
		})})
	m.add(reg, route.Route{Method: http.MethodPatch, Path: "/api/v1/banquet/beos/{id}", Summary: "Edit a draft BEO: instructions per department, notes (If-Match)",
		Permission: "banquet.beo.manage", Request: BEOPatch{}, Response: BEO{},
		Handler: write(db, http.StatusOK, "beos", "BEO", func(ctx context.Context, tx pgx.Tx, bid uuid.UUID, r *http.Request, in BEOPatch) (BEO, error) {
			return m.UpdateBEO(ctx, tx, bid, in, r.Header.Get("If-Match"))
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/beos/{id}:issue",
		Summary: "Issue the BEO: departments notified, Banquet Production built, requirement published (If-Match)", Permission: "banquet.beo.issue",
		Request: IssueBEOInput{}, Response: BEO{},
		Handler: write(db, http.StatusOK, "beos", "BEO", func(ctx context.Context, tx pgx.Tx, bid uuid.UUID, r *http.Request, in IssueBEOInput) (BEO, error) {
			return m.IssueBEO(ctx, tx, bid, r.Header.Get("If-Match"), in.Reason)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/beos/{id}:revise",
		Summary: "Revise the issued BEO: next version, changes marked, departments confirm again (If-Match)", Permission: "banquet.beo.issue",
		Request: BEORevise{}, Response: BEO{},
		Handler: write(db, http.StatusOK, "beos", "BEO", func(ctx context.Context, tx pgx.Tx, bid uuid.UUID, r *http.Request, in BEORevise) (BEO, error) {
			return m.ReviseBEO(ctx, tx, bid, in, r.Header.Get("If-Match"))
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/beos/{id}:acknowledge", Summary: "Confirm reading the current BEO version for a department",
		Permission: "banquet.beo.acknowledge", Request: BEOAckInput{}, Response: BEO{},
		Handler: write(db, http.StatusOK, "beos", "BEO", func(ctx context.Context, tx pgx.Tx, bid uuid.UUID, r *http.Request, in BEOAckInput) (BEO, error) {
			return m.AcknowledgeBEO(ctx, tx, bid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/banquet/beos/{id}/pdf", Summary: "BEO function sheet (PDF, standard banquet format)",
		Permission: "banquet.beo.view", RawContent: "application/pdf", Handler: func(w http.ResponseWriter, r *http.Request) {
			bid, err := handle.ID(r)
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			var b []byte
			var number string
			err = db.WithReadTx(r.Context(), func(tx pgx.Tx) error {
				if err := owned(r.Context(), tx, "beos", bid, "BEO"); err != nil {
					return err
				}
				x, err := m.GetBEO(r.Context(), tx, bid)
				if err != nil {
					return err
				}
				number = fmt.Sprintf("%s-v%d", x.Number, x.Version)
				b = BEOPDF(r.Context(), tx, x)
				return nil
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			w.Header().Set("Content-Type", "application/pdf")
			w.Header().Set("Content-Disposition", `inline; filename="`+number+`.pdf"`)
			_, _ = w.Write(b)
		}})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/banquet/events/{id}/procurement-requirement",
		Summary: "Ingredient requirement of the event from the menu BOM per pax (K1)", Permission: "banquet.beo.view", Response: EventProcurementRequirement{},
		Handler: read(db, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request) (EventProcurementRequirement, error) {
			return m.Requirement(ctx, tx, eid)
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/banquet/production", Summary: "Banquet Production (Kitchen): dishes of the issued BEOs per serving time",
		Permission: "banquet.production.view", Response: BanquetProductionItem{}, List: true,
		Query: []route.Param{{Name: "date", Description: "YYYY-MM-DD (default today)"}, {Name: "filter[outletId]"}, {Name: "filter[status]"},
			{Name: "filter[station]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[BanquetProductionItem], error) {
			lp := httpx.ParseList(r)
			loc := calendar.Location(ctx, tx)
			day := localDay(clock.Now(), loc)
			if v := r.URL.Query().Get("date"); v != "" {
				d, err := time.ParseInLocation("2006-01-02", v, loc)
				if err != nil {
					return httpx.Page[BanquetProductionItem]{}, handle.Invalid("date", "invalid_date", "YYYY-MM-DD")
				}
				day = d
			}
			var outlet *uuid.UUID
			if v := lp.Filters["outletId"]; v != "" {
				u, err := uuid.Parse(v)
				if err != nil {
					return httpx.Page[BanquetProductionItem]{}, handle.Invalid("filter[outletId]", "invalid", "uuid")
				}
				outlet = &u
			}
			return handle.Page(m.Production(ctx, tx, handle.Property(ctx), day, outlet, lp.Filters["status"], lp.Filters["station"]))
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/production-items/{id}:status", Summary: "Move a dish through production",
		Permission: "banquet.production.update", Request: BanquetProductionStatus{}, Response: BanquetProductionItem{},
		Handler: write(db, http.StatusOK, "production_items", "production item", func(ctx context.Context, tx pgx.Tx, pid uuid.UUID, r *http.Request, in BanquetProductionStatus) (BanquetProductionItem, error) {
			return m.SetProductionStatus(ctx, tx, pid, in)
		})})
}
