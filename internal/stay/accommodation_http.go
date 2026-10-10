package stay

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

// DateInput names a local day.
type DateInput struct {
	Date string `json:"date,omitempty" doc:"YYYY-MM-DD; default today"`
}

// reservationTabs filter the accommodation reservations (§4 menu).
var reservationTabs = map[string]string{
	"all":              `true`,
	"confirmed":        `x.booking_status = 'confirmed'`,
	"awaiting_payment": `x.booking_status = 'awaiting_payment'`,
	"pending_payment":  `x.booking_status IN ('awaiting_payment', 'confirmed', 'requested') AND x.payment_status IN ('unpaid', 'pending', 'partially_paid', 'failed')`,
	"expired":          `x.booking_status = 'expired'`,
	"void":             `x.status = 'void'`,
	"balance":          `x.status NOT IN ('expired', 'void') AND x.total_due::numeric - x.paid::numeric <> 0`,
	"checked_in":       `x.status = 'checked_in'`,
	"checked_out":      `x.status = 'checked_out'`,
	"cancelled":        `x.status = 'cancelled'`,
	"no_show":          `x.status = 'no_show'`,
	"requested":        `x.status = 'requested'`,
}

// registerAccommodation adds the Accommodation routes (bungalow management
// requirements): master data, reservations, front office, room status and
// rack, housekeeping, maintenance, guest requests, waitlist, guests,
// dashboard and reports.
func (m *Module) registerAccommodation(reg *route.Registry, eng *resource.Engine) {
	for _, d := range append(append([]*resource.Def{}, accommodationDefs...), accommodationShared...) {
		eng.Register(reg, d)
	}
	m.registerExperience(reg)
	db := m.DB
	add := func(rt route.Route) {
		rt.Module, rt.Tag, rt.Scope = "stay", tagAccommodation, route.ScopeProperty
		reg.Add(rt)
	}
	day := func(r *http.Request, ctx context.Context, tx pgx.Tx) (time.Time, error) {
		loc := calendar.Location(ctx, tx)
		n := clock.Now().In(loc)
		d, err := handle.QueryDate(r, "date", time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC))
		return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc), err
	}
	id := func(r *http.Request) (uuid.UUID, error) { return handle.ID(r) }

	// ── dashboard and reports ─────────────────────────────────────────────
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/dashboard", Summary: "Accommodation dashboard: KPI and today's operation",
		Permission: "stay.dashboard.view", Response: Dashboard{}, Query: []route.Param{{Name: "date", Description: "YYYY-MM-DD"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Dashboard, error) {
			d, err := day(r, ctx, tx)
			if err != nil {
				return Dashboard{}, err
			}
			return m.DashboardFor(ctx, tx, handle.Property(ctx), d)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/accommodation-report", Summary: "Accommodation report: occupancy, ADR, RevPAR, revenue, operations",
		Permission: "stay.dashboard.view", Response: Report{}, Query: []route.Param{{Name: "from", Description: "YYYY-MM-DD"}, {Name: "to", Description: "YYYY-MM-DD"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Report, error) {
			t := today(ctx, tx)
			from, err := handle.QueryDate(r, "from", t.AddDate(0, 0, -29))
			if err != nil {
				return Report{}, err
			}
			to, err := handle.QueryDate(r, "to", t)
			if err != nil {
				return Report{}, err
			}
			return m.ReportFor(ctx, tx, handle.Property(ctx), from, to)
		})})

	// ── reservations ──────────────────────────────────────────────────────
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/reservations", Summary: "Bungalow reservations (all, confirmed, pending payment, checked-in …)",
		Permission: "stay.stay.view", Response: Stay{}, List: true, Query: []route.Param{{Name: "tab", Description: "all, confirmed, pending_payment, checked_in, checked_out, cancelled, no_show, requested"},
			{Name: "from", Description: "YYYY-MM-DD (stays overlapping)"}, {Name: "to"}, {Name: "q"}, {Name: "typeId"}, {Name: "source"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Stay], error) {
			lp := httpx.ParseList(r)
			tab := r.URL.Query().Get("tab")
			cond, ok := reservationTabs[tab]
			if !ok {
				cond = reservationTabs["all"]
			}
			loc := calendar.Location(ctx, tx)
			from, err := handle.QueryDate(r, "from", time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC))
			if err != nil {
				return httpx.Page[Stay]{}, err
			}
			to, err := handle.QueryDate(r, "to", time.Date(9998, 1, 1, 0, 0, 0, 0, time.UTC))
			if err != nil {
				return httpx.Page[Stay]{}, err
			}
			typeID, err := handle.QueryUUID(r, "typeId")
			if err != nil {
				return httpx.Page[Stay]{}, err
			}
			f := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
			t := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
			order := `x.start_at DESC`
			if tab == "confirmed" || tab == "pending_payment" || tab == "requested" {
				order = `x.start_at`
			}
			return handle.Page(handle.List[Stay](tx.Query(ctx, `SELECT x.* FROM (`+staySelect+` WHERE s.property_id = $1 AND s.kind = 'bungalow') x
				WHERE `+cond+` AND x.end_at > $2 AND x.start_at < $3 AND ($4::uuid IS NULL OR x.unit_type_id = $4) AND ($5 = '' OR x.booking_source = $5)
				AND ($6 = '' OR x.stay_no ILIKE '%' || $6 || '%' OR x.reservation_code ILIKE '%' || $6 || '%' OR x.customer_name ILIKE '%' || $6 || '%'
				  OR x.guest_name ILIKE '%' || $6 || '%' OR x.corporate_name ILIKE '%' || $6 || '%' OR x.unit_name ILIKE '%' || $6 || '%')
				ORDER BY `+order+` LIMIT $7`, handle.Property(ctx), f, t, typeID, r.URL.Query().Get("source"), lp.Q, lp.Limit)))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/search", Summary: "Search availability: room types with free bungalows and prices per rate plan",
		Permission: "stay.stay.view", Response: SearchType{}, List: true, Query: []route.Param{{Name: "arrival", Required: true, Description: "YYYY-MM-DD"},
			{Name: "departure", Required: true}, {Name: "adults", Type: "integer"}, {Name: "children", Type: "integer"}, {Name: "promoCode"},
			{Name: "corporateAccountId"}, {Name: "customerId"}, {Name: "source"}, {Name: "typeId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[SearchType], error) {
			in, err := searchInput(ctx, tx, r)
			if err != nil {
				return httpx.Page[SearchType]{}, err
			}
			return handle.Page(m.Search(ctx, tx, handle.Property(ctx), in))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/stays:quote", Summary: "Price summary of a booking (nothing is kept)", Permission: "stay.stay.create",
		Request: StayInput{}, Response: StayResult{}, NoAudit: "read-only preview, rolled back",
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in StayInput) (StayResult, error) {
			sp, err := tx.Begin(ctx)
			if err != nil {
				return StayResult{}, err
			}
			defer func() { _ = sp.Rollback(ctx) }()
			in.Payment = nil
			return m.Book(ctx, sp, handle.Property(ctx), in, "")
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/stays/{id}:reschedule", Summary: "Reschedule: dates, room type, bungalow, guests, rate plan (price recalculated)",
		Permission: "stay.stay.reschedule", Request: RescheduleInput{}, Response: StayResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RescheduleInput) (StayResult, error) {
			sid, err := id(r)
			if err != nil {
				return StayResult{}, err
			}
			return m.Reschedule(ctx, tx, sid, in)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/stays/{id}:late-checkout", Summary: "Approve a late check-out (bungalow kept; fee at check-out)",
		Permission: "stay.stay.update", Request: LateCheckoutInput{}, Response: StayResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in LateCheckoutInput) (StayResult, error) {
			sid, err := id(r)
			if err != nil {
				return StayResult{}, err
			}
			return m.LateCheckout(ctx, tx, sid, in)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/stays/{id}:charge", Summary: "Charge to Room: an additional charge on the stay folio",
		Permission: "stay.stay.charge", Request: ChargeInput{}, Response: StayResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ChargeInput) (StayResult, error) {
			sid, err := id(r)
			if err != nil {
				return StayResult{}, err
			}
			return m.ChargeToRoom(ctx, tx, sid, in)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/stays/{id}/addons", Summary: "Add an add-on to a stay (charged to the folio)",
		Permission: "stay.stay.charge", Request: StayAddonInput{}, Response: StayResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in StayAddonInput) (StayResult, error) {
			sid, err := id(r)
			if err != nil {
				return StayResult{}, err
			}
			return m.AddAddon(ctx, tx, sid, in)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/stays/{id}/addons/{addonId}:void", Summary: "Remove an add-on of a stay",
		Permission: "stay.stay.charge", Request: ReasonInput{}, Response: StayResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReasonInput) (StayResult, error) {
			sid, err := id(r)
			if err != nil {
				return StayResult{}, err
			}
			aid, err := handle.ID(r, "addonId")
			if err != nil {
				return StayResult{}, err
			}
			return m.VoidAddon(ctx, tx, sid, aid, in.Reason)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/stays/{id}:update", Summary: "Change the guest contact, requests, notes, VIP and source",
		Permission: "stay.stay.update", Request: StayUpdateInput{}, Response: StayResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in StayUpdateInput) (StayResult, error) {
			sid, err := id(r)
			if err != nil {
				return StayResult{}, err
			}
			return m.UpdateDetails(ctx, tx, sid, in)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/stays/{id}/history", Summary: "Audit trail of a stay (reservation, folio, payments, requests)",
		Permission: "stay.stay.view", Response: HistoryEntry{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[HistoryEntry], error) {
			sid, err := id(r)
			if err != nil {
				return httpx.Page[HistoryEntry]{}, err
			}
			return handle.Page(m.History(ctx, tx, sid))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/stays/{id}/unit-options", Summary: "Bungalows for a room assignment with their warnings",
		Permission: "stay.stay.view", Response: UnitOption{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[UnitOption], error) {
			sid, err := id(r)
			if err != nil {
				return httpx.Page[UnitOption]{}, err
			}
			return handle.Page(m.UnitOptions(ctx, tx, sid))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/front-office", Summary: "Front office day: arrivals, departures, in-house guests",
		Permission: "stay.stay.view", Response: FrontOffice{}, Query: []route.Param{{Name: "date"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (FrontOffice, error) {
			d, err := day(r, ctx, tx)
			if err != nil {
				return FrontOffice{}, err
			}
			return m.FrontOfficeDay(ctx, tx, handle.Property(ctx), d)
		})})

	// ── rooms ─────────────────────────────────────────────────────────────
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/room-status", Summary: "Room status of every bungalow: housekeeping, operational and reservation status",
		Permission: "stay.room_status.view", Response: RoomState{}, List: true, Query: []route.Param{{Name: "date"}, {Name: "typeId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[RoomState], error) {
			d, err := day(r, ctx, tx)
			if err != nil {
				return httpx.Page[RoomState]{}, err
			}
			t, err := handle.QueryUUID(r, "typeId")
			if err != nil {
				return httpx.Page[RoomState]{}, err
			}
			return handle.Page(m.RoomStates(ctx, tx, handle.Property(ctx), d, t))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/bungalows/{id}:room-status", Summary: "Set the housekeeping status (Dirty, Cleaning, Cleaned, Inspected, Ready)",
		Permission: "stay.room_status.update", Request: RoomStatusInput{}, Status: http.StatusNoContent,
		Handler: handle.Write(db, http.StatusNoContent, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RoomStatusInput) (any, error) {
			bid, err := id(r)
			if err != nil {
				return nil, err
			}
			return nil, m.changeRoomStatus(ctx, tx, bid, in)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/room-rack", Summary: "Room rack: stays and blocks per bungalow and day",
		Permission: "stay.room_status.view", Response: RoomRack{}, Query: []route.Param{{Name: "from"}, {Name: "days", Type: "integer"}, {Name: "typeId"},
			{Name: "status", Description: "Stay statuses, comma separated"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (RoomRack, error) {
			loc := calendar.Location(ctx, tx)
			n := clock.Now().In(loc)
			from, err := handle.QueryDate(r, "from", time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC))
			if err != nil {
				return RoomRack{}, err
			}
			t, err := handle.QueryUUID(r, "typeId")
			if err != nil {
				return RoomRack{}, err
			}
			return m.Rack(ctx, tx, handle.Property(ctx), from, min(max(handle.QueryInt(r, "days", 14), 1), 62), t, r.URL.Query().Get("status"))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/room-blocks", Summary: "Room blocks (Blocked, Maintenance, Out of Order)", Permission: "stay.room_block.view",
		Response: RoomBlock{}, List: true, Query: []route.Param{{Name: "bungalowId"}, {Name: "status"}, {Name: "from"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[RoomBlock], error) {
			b, err := handle.QueryUUID(r, "bungalowId")
			if err != nil {
				return httpx.Page[RoomBlock]{}, err
			}
			from, err := handle.QueryDate(r, "from", time.Now().AddDate(0, 0, -30))
			if err != nil {
				return httpx.Page[RoomBlock]{}, err
			}
			return handle.Page(handle.List[RoomBlock](tx.Query(ctx, blockSelect+` WHERE k.property_id = $1 AND ($2::uuid IS NULL OR k.bungalow_id = $2)
				AND ($3 = '' OR k.status = $3) AND k.end_at >= $4 ORDER BY k.start_at DESC LIMIT 300`, handle.Property(ctx), b, r.URL.Query().Get("status"), from)))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/room-blocks", Summary: "Block a bungalow (Blocked / Maintenance / Out of Order): it is not offered",
		Permission: "stay.room_block.manage", Request: BlockInput{}, Response: RoomBlock{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in BlockInput) (RoomBlock, error) {
			return m.createBlock(ctx, tx, handle.Property(ctx), in, nil)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/room-blocks/{id}:release", Summary: "Release a room block", Permission: "stay.room_block.manage",
		Request: ReasonInput{}, Response: RoomBlock{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReasonInput) (RoomBlock, error) {
			bid, err := id(r)
			if err != nil {
				return RoomBlock{}, err
			}
			return m.releaseBlock(ctx, tx, bid, in.Reason)
		})})

	// ── housekeeping ──────────────────────────────────────────────────────
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/housekeeping", Summary: "Housekeeping board of a day: tasks and room status",
		Permission: "stay.housekeeping.view", Response: HKBoard{}, Query: []route.Param{{Name: "date"}, {Name: "assignee"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (HKBoard, error) {
			d, err := day(r, ctx, tx)
			if err != nil {
				return HKBoard{}, err
			}
			return m.HKBoardFor(ctx, tx, handle.Property(ctx), d, r.URL.Query().Get("assignee"))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/housekeeping-tasks", Summary: "Create a housekeeping task", Permission: "stay.housekeeping.manage",
		Request: HKTaskInput{}, Response: HKTask{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in HKTaskInput) (HKTask, error) {
			in.Source, in.GuestRequestID = "manual", nil
			return m.createHKTask(ctx, tx, handle.Property(ctx), in)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/housekeeping:plan-day", Summary: "Plan the stayover cleaning of the occupied bungalows",
		Permission: "stay.housekeeping.manage", Request: DateInput{}, Response: HKTask{}, List: true, Status: http.StatusOK,
		NoAudit: "batch trigger: every task it creates is audited",
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in DateInput) (httpx.Page[HKTask], error) {
			d := today(ctx, tx)
			if in.Date != "" {
				var err error
				if d, err = time.ParseInLocation(time.DateOnly, in.Date, calendar.Location(ctx, tx)); err != nil {
					return httpx.Page[HKTask]{}, handle.Invalid("date", "invalid_date", "date must be YYYY-MM-DD")
				}
			}
			return handle.Page(m.PlanDay(ctx, tx, handle.Property(ctx), d))
		})})
	hkOp := func(op, summary, perm string, req, res any) {
		add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/housekeeping-tasks/{id}:" + op, Summary: summary, Permission: perm, Request: req, Response: res,
			Status: http.StatusOK, Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in hkBody) (any, error) {
				tid, err := id(r)
				if err != nil {
					return nil, err
				}
				switch op {
				case "assign":
					return m.assignHK(ctx, tx, tid, HKAssignInput{AssignedTo: in.AssignedTo, AssigneeUserID: in.AssigneeUserID, Priority: in.Priority})
				case "start":
					return m.startHK(ctx, tx, tid)
				case "complete":
					return m.completeHK(ctx, tx, tid, HKCompleteInput{Checklist: in.Checklist, Notes: in.Notes})
				case "inspect":
					return m.inspectTask(ctx, tx, tid, InspectInput{Result: in.Result, Checklist: in.Checklist, Notes: in.Notes, WorkOrderTitle: in.WorkOrderTitle,
						WorkOrderCategory: in.WorkOrderCategory})
				}
				return m.cancelHK(ctx, tx, tid, in.Reason)
			})})
	}
	hkOp("assign", "Assign a housekeeping task", "stay.housekeeping.manage", HKAssignInput{}, HKTask{})
	hkOp("start", "Start cleaning (bungalow Cleaning)", "stay.housekeeping.work", nil, HKTask{})
	hkOp("complete", "Complete the task with its checklist (bungalow Cleaned)", "stay.housekeeping.work", HKCompleteInput{}, HKTask{})
	hkOp("inspect", "Inspect the cleaned bungalow: passed → Inspected / Ready, failed → Cleaning", "stay.housekeeping.inspect", InspectInput{}, RoomInspection{})
	hkOp("cancel", "Cancel a housekeeping task", "stay.housekeeping.manage", ReasonInput{}, HKTask{})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/bungalows/{id}:inspect", Summary: "Inspect a bungalow (without a task)", Permission: "stay.housekeeping.inspect",
		Request: InspectInput{}, Response: RoomInspection{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in InspectInput) (RoomInspection, error) {
			bid, err := id(r)
			if err != nil {
				return RoomInspection{}, err
			}
			return m.inspect(ctx, tx, handle.Property(ctx), bid, nil, in)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/inspections", Summary: "Room inspections", Permission: "stay.housekeeping.view",
		Response: RoomInspection{}, List: true, Query: []route.Param{{Name: "bungalowId"}, {Name: "from"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[RoomInspection], error) {
			b, err := handle.QueryUUID(r, "bungalowId")
			if err != nil {
				return httpx.Page[RoomInspection]{}, err
			}
			from, err := handle.QueryDate(r, "from", time.Now().AddDate(0, 0, -30))
			if err != nil {
				return httpx.Page[RoomInspection]{}, err
			}
			return handle.Page(handle.List[RoomInspection](tx.Query(ctx, inspectionSelect+` WHERE i.property_id = $1 AND ($2::uuid IS NULL OR i.bungalow_id = $2)
				AND i.inspected_at >= $3 ORDER BY i.inspected_at DESC LIMIT 300`, handle.Property(ctx), b, from)))
		})})

	// ── maintenance ───────────────────────────────────────────────────────
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/work-orders", Summary: "Maintenance work orders and history", Permission: "stay.work_order.view",
		Response: WorkOrder{}, List: true, Query: []route.Param{{Name: "status", Description: "Comma separated; open = open, assigned, in progress"},
			{Name: "bungalowId"}, {Name: "source"}, {Name: "q"}, {Name: "from"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[WorkOrder], error) {
			lp := httpx.ParseList(r)
			b, err := handle.QueryUUID(r, "bungalowId")
			if err != nil {
				return httpx.Page[WorkOrder]{}, err
			}
			from, err := handle.QueryDate(r, "from", time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC))
			if err != nil {
				return httpx.Page[WorkOrder]{}, err
			}
			status := r.URL.Query().Get("status")
			if status == "open" {
				status = "open,assigned,in_progress"
			}
			return handle.Page(handle.List[WorkOrder](tx.Query(ctx, woSelect+` WHERE w.property_id = $1 AND ($2 = '' OR w.status = ANY(string_to_array($2, ',')))
				AND ($3::uuid IS NULL OR w.bungalow_id = $3) AND ($4 = '' OR w.source = $4) AND w.created_at >= $5
				AND ($6 = '' OR w.wo_no ILIKE '%' || $6 || '%' OR w.title ILIKE '%' || $6 || '%' OR b.code ILIKE '%' || $6 || '%')
				ORDER BY CASE w.status WHEN 'in_progress' THEN 0 WHEN 'assigned' THEN 1 WHEN 'open' THEN 2 ELSE 3 END,
				CASE w.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END, w.created_at DESC LIMIT $7`,
				handle.Property(ctx), status, b, r.URL.Query().Get("source"), from, lp.Q, lp.Limit)))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/work-orders/{id}", Summary: "Work order", Permission: "stay.work_order.view", Response: WorkOrder{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (WorkOrder, error) {
			wid, err := id(r)
			if err != nil {
				return WorkOrder{}, err
			}
			return m.workOrder(ctx, tx, wid)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/work-orders", Summary: "Report a maintenance issue (optionally Out of Order)", Permission: "stay.work_order.create",
		Request: WorkOrderInput{}, Response: WorkOrder{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in WorkOrderInput) (WorkOrder, error) {
			return m.createWorkOrder(ctx, tx, handle.Property(ctx), in, "manual", nil)
		})})
	woOp := func(op, summary, perm string, req any) {
		add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/work-orders/{id}:" + op, Summary: summary, Permission: perm, Request: req, Response: WorkOrder{},
			Status: http.StatusOK, Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in woBody) (WorkOrder, error) {
				wid, err := id(r)
				if err != nil {
					return WorkOrder{}, err
				}
				return m.moveWorkOrder(ctx, tx, wid, op, WOAssignInput{AssignedTo: in.AssignedTo, Priority: in.Priority},
					WOResolveInput{Resolution: in.Resolution, Cost: in.Cost}, in.Reason)
			})})
	}
	woOp("assign", "Assign the work order to a technician", "stay.work_order.manage", WOAssignInput{})
	woOp("start", "Start the work", "stay.work_order.work", nil)
	woOp("resolve", "Resolve: the bungalow is reopened and inspected", "stay.work_order.work", WOResolveInput{})
	woOp("close", "Close a resolved work order", "stay.work_order.manage", nil)
	woOp("cancel", "Cancel the work order", "stay.work_order.manage", ReasonInput{})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/preventive-schedules:generate", Summary: "Raise the preventive work orders that are due",
		Permission: "stay.work_order.manage", Request: DateInput{}, Response: WorkOrder{}, List: true, Status: http.StatusOK,
		NoAudit: "batch trigger: every work order it raises is audited",
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in DateInput) (httpx.Page[WorkOrder], error) {
			d := today(ctx, tx)
			if in.Date != "" {
				var err error
				if d, err = time.ParseInLocation(time.DateOnly, in.Date, calendar.Location(ctx, tx)); err != nil {
					return httpx.Page[WorkOrder]{}, handle.Invalid("date", "invalid_date", "date must be YYYY-MM-DD")
				}
			}
			return handle.Page(m.GeneratePreventive(ctx, tx, handle.Property(ctx), d))
		})})

	// ── guest requests ────────────────────────────────────────────────────
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/guest-requests", Summary: "Guest requests", Permission: "stay.guest_request.view",
		Response: GuestRequest{}, List: true, Query: []route.Param{{Name: "status", Description: "Comma separated; open = requested, assigned, in progress"},
			{Name: "stayId"}, {Name: "from"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[GuestRequest], error) {
			lp := httpx.ParseList(r)
			sid, err := handle.QueryUUID(r, "stayId")
			if err != nil {
				return httpx.Page[GuestRequest]{}, err
			}
			from, err := handle.QueryDate(r, "from", time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC))
			if err != nil {
				return httpx.Page[GuestRequest]{}, err
			}
			status := r.URL.Query().Get("status")
			if status == "open" {
				status = "requested,assigned,in_progress"
			}
			return handle.Page(handle.List[GuestRequest](tx.Query(ctx, requestSelect+` WHERE g.property_id = $1 AND ($2 = '' OR g.status = ANY(string_to_array($2, ',')))
				AND ($3::uuid IS NULL OR g.stay_id = $3) AND g.created_at >= $4
				ORDER BY CASE g.status WHEN 'requested' THEN 0 WHEN 'assigned' THEN 1 WHEN 'in_progress' THEN 2 ELSE 3 END, g.created_at DESC LIMIT $5`,
				handle.Property(ctx), status, sid, from, lp.Limit)))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/guest-requests", Summary: "Take a guest request", Permission: "stay.guest_request.create",
		Request: GuestRequestInput{}, Response: GuestRequest{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in GuestRequestInput) (GuestRequest, error) {
			if in.Source == "member_app" || in.Source == "guest_app" {
				in.Source = "front_desk"
			}
			return m.createRequest(ctx, tx, in)
		})})
	for _, op := range []string{"assign", "start", "complete", "cancel"} {
		op := op
		add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/guest-requests/{id}:" + op, Summary: label(op) + " a guest request", Permission: "stay.guest_request.work",
			Request: RequestMoveInput{}, Response: GuestRequest{}, Status: http.StatusOK,
			Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RequestMoveInput) (GuestRequest, error) {
				rid, err := id(r)
				if err != nil {
					return GuestRequest{}, err
				}
				return m.moveRequest(ctx, tx, rid, op, in)
			})})
	}

	// ── waitlist ──────────────────────────────────────────────────────────
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/waitlist", Summary: "Waitlist with the bungalows free now", Permission: "stay.waitlist.view",
		Response: WaitlistEntry{}, List: true, Query: []route.Param{{Name: "status", Description: "Comma separated; default waiting,offered"}, {Name: "typeId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[WaitlistEntry], error) {
			t, err := handle.QueryUUID(r, "typeId")
			if err != nil {
				return httpx.Page[WaitlistEntry]{}, err
			}
			status := r.URL.Query().Get("status")
			if status == "" {
				status = "waiting,offered"
			}
			list, err := handle.List[WaitlistEntry](tx.Query(ctx, waitlistSelect+` WHERE w.property_id = $1 AND w.status = ANY(string_to_array($2, ','))
				AND ($3::uuid IS NULL OR w.bungalow_type_id = $3)`+waitlistOrder+` LIMIT 300`, handle.Property(ctx), status, t))
			if err != nil {
				return httpx.Page[WaitlistEntry]{}, err
			}
			for i := range list {
				if list[i].Status == "waiting" || list[i].Status == "offered" {
					n, err := m.freeUnits(ctx, tx, handle.Property(ctx), list[i].BungalowTypeID, list[i].ArrivalDate, list[i].Nights)
					if err != nil {
						return httpx.Page[WaitlistEntry]{}, err
					}
					list[i].Available = &n
				}
			}
			return httpx.Page[WaitlistEntry]{Items: list}, nil
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/waitlist", Summary: "Join the waitlist of a full room type", Permission: "stay.waitlist.manage",
		Request: WaitlistInput{}, Response: WaitlistEntry{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in WaitlistInput) (WaitlistEntry, error) {
			return m.joinWaitlist(ctx, tx, handle.Property(ctx), in)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/waitlist/{id}:convert", Summary: "Convert a waitlist entry into a reservation", Permission: "stay.waitlist.manage",
		Request: WaitlistConvertInput{}, Response: StayResult{}, Status: http.StatusCreated, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in WaitlistConvertInput) (StayResult, error) {
			wid, err := id(r)
			if err != nil {
				return StayResult{}, err
			}
			return m.convertWaitlist(ctx, tx, handle.Property(ctx), wid, in, r.Header.Get("Idempotency-Key"))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/waitlist/{id}:cancel", Summary: "Remove a waitlist entry", Permission: "stay.waitlist.manage",
		Request: ReasonInput{}, Response: WaitlistEntry{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReasonInput) (WaitlistEntry, error) {
			wid, err := id(r)
			if err != nil {
				return WaitlistEntry{}, err
			}
			return m.cancelWaitlist(ctx, tx, wid, in.Reason)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/waitlist:offer", Summary: "Offer the free bungalows to the waiting guests now", Permission: "stay.waitlist.manage",
		Status: http.StatusNoContent, NoAudit: "batch trigger: every offer is audited",
		Handler: handle.Write(db, http.StatusNoContent, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (any, error) {
			rows, err := tx.Query(ctx, `SELECT DISTINCT bungalow_type_id FROM stay.waitlist WHERE property_id = $1 AND status = 'waiting'`, handle.Property(ctx))
			if err != nil {
				return nil, err
			}
			types, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
			if err != nil {
				return nil, err
			}
			for _, t := range types {
				if err := m.offerWaitlist(ctx, tx, handle.Property(ctx), t); err != nil {
					return nil, err
				}
			}
			return nil, nil
		})})

	// ── guests ────────────────────────────────────────────────────────────
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/guests", Summary: "Accommodation guests with their stay history", Permission: "stay.guest.view",
		Response: GuestSummary{}, List: true, Query: []route.Param{{Name: "q"}, {Name: "vip", Type: "boolean"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[GuestSummary], error) {
			lp := httpx.ParseList(r)
			return handle.Page(m.Guests(ctx, tx, handle.Property(ctx), lp.Q, r.URL.Query().Get("vip") == "true", lp.Limit))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/guests/{id}", Summary: "Guest profile with booking, stay and payment history", Permission: "stay.guest.view",
		Response: GuestDetail{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (GuestDetail, error) {
			cid, err := id(r)
			if err != nil {
				return GuestDetail{}, err
			}
			return m.Guest(ctx, tx, handle.Property(ctx), cid)
		})})
	add(route.Route{Method: http.MethodPut, Path: "/api/v1/stay/guests/{id}/profile", Summary: "Update the guest profile (address, identity, nationality, preferences, VIP)",
		Permission: "stay.guest.update", Request: GuestProfileInput{}, Response: GuestProfile{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in GuestProfileInput) (GuestProfile, error) {
			cid, err := id(r)
			if err != nil {
				return GuestProfile{}, err
			}
			return m.SaveGuestProfile(ctx, tx, handle.Property(ctx), cid, in)
		})})
}

// hkBody is the body of a housekeeping task move.
type hkBody struct {
	AssignedTo        string          `json:"assignedTo,omitempty"`
	AssigneeUserID    *uuid.UUID      `json:"assigneeUserId,omitempty"`
	Priority          string          `json:"priority,omitempty"`
	Checklist         []ChecklistItem `json:"checklist,omitempty"`
	Notes             string          `json:"notes,omitempty"`
	Result            string          `json:"result,omitempty"`
	WorkOrderTitle    string          `json:"workOrderTitle,omitempty"`
	WorkOrderCategory string          `json:"workOrderCategory,omitempty"`
	Reason            string          `json:"reason,omitempty"`
}

// woBody is the body of a work order move.
type woBody struct {
	AssignedTo string `json:"assignedTo,omitempty"`
	Priority   string `json:"priority,omitempty"`
	Resolution string `json:"resolution,omitempty"`
	Cost       string `json:"cost,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

func searchInput(ctx context.Context, tx pgx.Tx, r *http.Request) (SearchInput, error) {
	loc := calendar.Location(ctx, tx)
	q := r.URL.Query()
	a, err := time.ParseInLocation(time.DateOnly, q.Get("arrival"), loc)
	if err != nil {
		return SearchInput{}, handle.Invalid("arrival", "invalid_date", "arrival must be YYYY-MM-DD")
	}
	d, err := time.ParseInLocation(time.DateOnly, q.Get("departure"), loc)
	if err != nil || !d.After(a) {
		return SearchInput{}, handle.Invalid("departure", "invalid_date", "departure must be after arrival")
	}
	if d.Sub(a) > 60*24*time.Hour {
		return SearchInput{}, errs.Validation("too_long", "search at most 60 nights")
	}
	in := SearchInput{Arrival: a, Departure: d, Adults: handle.QueryInt(r, "adults", 1), Children: handle.QueryInt(r, "children", 0),
		PromoCode: q.Get("promoCode"), Source: q.Get("source")}
	if in.CorporateID, err = handle.QueryUUID(r, "corporateAccountId"); err != nil {
		return in, err
	}
	if in.CustomerID, err = handle.QueryUUID(r, "customerId"); err != nil {
		return in, err
	}
	in.TypeID, err = handle.QueryUUID(r, "typeId")
	return in, err
}
