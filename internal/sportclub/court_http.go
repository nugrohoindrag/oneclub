package sportclub

// HTTP of the court booking (docs/requirement-booking-sportclub-mgcc.md):
// the front desk and admin routes (/api/v1/sportclub/…), the website routes
// (/api/v1/public/…: sports, slot grid, quote, checkout, confirmation,
// Cek Booking, QR, e-ticket, calendar file) and the Member App routes
// (/api/v1/member/sport-club/…).

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"net/http"
	"strings"
	"time"

	"github.com/boombuler/barcode"
	qrcode "github.com/boombuler/barcode/qr"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/pdf"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/rules"
	"oneclub/internal/reservation"
)

// QuoteInput asks the price of a cart.
type QuoteInput struct {
	Lines      []CourtLine `json:"lines"`
	PromoCode  string      `json:"promoCode,omitempty"`
	Method     string      `json:"method,omitempty" doc:"Online method code (website / Member App): adds its service fee"`
	CustomerID *uuid.UUID  `json:"customerId,omitempty"`
}

type idsInput struct {
	Reason string `json:"reason"`
}

func requirePerm(ctx context.Context, perm string) error {
	return authz.RequireAt(ctx, perm, handle.Property(ctx))
}

func (m *Module) registerCourts(reg *route.Registry) {
	db := m.DB
	add := func(rt route.Route) {
		rt.Module, rt.Scope = "sportclub", route.ScopeProperty
		if rt.Tag == "" {
			rt.Tag = "Sport Club Courts"
		}
		reg.Add(rt)
	}
	// ── board, grid, quote ──
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/board", Summary: "Court board of a day: courts, bookings per line, blocks, summary, staff on duty, alerts",
		Permission: "sportclub.booking.view", Response: Board{}, Query: []route.Param{{Name: "date"}, {Name: "facilityId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Board, error) {
			d, err := localDate(ctx, tx, r, "date")
			if err != nil {
				return Board{}, err
			}
			f, err := handle.QueryUUID(r, "facilityId")
			if err != nil {
				return Board{}, err
			}
			return m.board(ctx, tx, handle.Property(ctx), d, f)
		})})
	if m.Hub != nil {
		add(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/board/stream", Summary: "Real-time court board events (SSE)", Permission: "sportclub.booking.view",
			RawContent: "text/event-stream", Query: []route.Param{{Name: "propertyId", Description: "Active property (EventSource cannot send X-Property-Id)"}},
			Handler: m.Hub.Stream([]string{"sportclub"})})
	}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/grid", Summary: "Slot grid of a sport (status and price per hour)", Permission: "sportclub.booking.view",
		Response: Grid{}, Query: []route.Param{{Name: "facilityId", Required: true}, {Name: "date"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Grid, error) {
			d, err := localDate(ctx, tx, r, "date")
			if err != nil {
				return Grid{}, err
			}
			f, err := handle.QueryUUID(r, "facilityId")
			if err != nil || f == nil {
				return Grid{}, handle.Invalid("facilityId", "required", "facilityId is required")
			}
			return m.grid(ctx, tx, handle.Property(ctx), *f, d, false, nil)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/quote", Summary: "Price of courts and hours (the quote of every screen)", Permission: "sportclub.booking.view",
		Request: QuoteInput{}, Response: Quote{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in QuoteInput) (Quote, error) {
			pid := handle.Property(ctx)
			pol, err := m.courtPolicy(ctx, tx, pid)
			if err != nil {
				return Quote{}, err
			}
			slots, err := m.normalizeLines(ctx, tx, pid, in.Lines, pol, slotOptions{SkipHours: true})
			if err != nil {
				return Quote{}, err
			}
			return m.quote(ctx, tx, pid, quoteRequest{Slots: slots, Promo: in.PromoCode, Customer: in.CustomerID, Channel: "ops"}, pol)
		})})
	// ── bookings ──
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/bookings", Summary: "Court Booking at the desk: several courts and hours, package, desk payment",
		Permission: "sportclub.booking.create", Request: CourtBookingInput{}, Response: CourtBookingResult{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CourtBookingInput) (CourtBookingResult, error) {
			if in.Discount != nil && in.Discount.Amount != "" {
				if err := requirePerm(ctx, "sportclub.booking.override"); err != nil {
					return CourtBookingResult{}, err
				}
			}
			if in.Channel == "website" || in.Channel == "member_app" {
				in.Channel = "ops"
			}
			if in.Channel == "" {
				in.Channel = "ops"
			}
			return m.BookCourt(ctx, tx, handle.Property(ctx), in, r.Header.Get("Idempotency-Key"))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/bookings", Summary: "Court bookings (status per line, payment status, channel)",
		Permission: "sportclub.booking.view", Response: CourtBooking{}, List: true,
		Query: []route.Param{{Name: "date", Description: "One day (YYYY-MM-DD)"}, {Name: "from"}, {Name: "to"}, {Name: "q", Description: "Code, name or phone"},
			{Name: "filter[facilityId]"}, {Name: "filter[courtId]"}, {Name: "filter[state]"}, {Name: "filter[channel]"}, {Name: "filter[payStatus]"},
			{Name: "filter[customerId]"}, {Name: "filter[recurringGroupId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CourtBooking], error) {
			return handle.Page(m.listBookings(ctx, tx, handle.Property(ctx), r))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/bookings/{id}", Summary: "Court booking with bill and history", Permission: "sportclub.booking.view",
		Response: CourtBookingDetail{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CourtBookingDetail, error) {
			rid, err := handle.ID(r)
			if err != nil {
				return CourtBookingDetail{}, err
			}
			if _, err := m.lockBookingRead(ctx, tx, handle.Property(ctx), rid); err != nil {
				return CourtBookingDetail{}, err
			}
			return m.detail(ctx, tx, rid)
		})})
	op := func(path, summary, perm string, req any, fn func(ctx context.Context, tx pgx.Tx, pid, rid uuid.UUID, r *http.Request) (CourtBookingDetail, error)) {
		add(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/bookings/{id}:" + path, Summary: summary, Permission: perm, Request: req,
			Response: CourtBookingDetail{}, Status: http.StatusOK, Idempotent: path == "pay",
			Handler: func(w http.ResponseWriter, r *http.Request) {
				var out CourtBookingDetail
				ctx := r.Context()
				err := db.WithTx(ctx, func(tx pgx.Tx) error {
					rid, err := handle.ID(r)
					if err != nil {
						return err
					}
					out, err = fn(ctx, tx, handle.Property(ctx), rid, r)
					return err
				})
				if err != nil {
					httpx.WriteError(w, r, err)
					return
				}
				httpx.JSON(w, http.StatusOK, out)
			}})
	}
	op("pay", "Take a payment at the desk (split per player: several payments)", "sportclub.booking.operate", CourtPayInput{},
		func(ctx context.Context, tx pgx.Tx, pid, rid uuid.UUID, r *http.Request) (CourtBookingDetail, error) {
			var in CourtPayInput
			if err := httpx.Decode(r, &in); err != nil {
				return CourtBookingDetail{}, err
			}
			return m.PayBooking(ctx, tx, pid, rid, in, r.Header.Get("Idempotency-Key"))
		})
	op("move", "Move a line to another court / hour (supervisor; a higher price is charged)", "sportclub.booking.override", MoveInput{},
		func(ctx context.Context, tx pgx.Tx, pid, rid uuid.UUID, r *http.Request) (CourtBookingDetail, error) {
			var in MoveInput
			if err := httpx.Decode(r, &in); err != nil {
				return CourtBookingDetail{}, err
			}
			return m.MoveLine(ctx, tx, pid, rid, in)
		})
	op("extend", "Overtime: add the next hour(s) of the court", "sportclub.booking.operate", ExtendInput{},
		func(ctx context.Context, tx pgx.Tx, pid, rid uuid.UUID, r *http.Request) (CourtBookingDetail, error) {
			var in ExtendInput
			if err := httpx.Decode(r, &in); err != nil {
				return CourtBookingDetail{}, err
			}
			return m.ExtendLine(ctx, tx, pid, rid, in)
		})
	op("no-show", "Mark lines started without check-in as No-show (no refund)", "sportclub.booking.operate", LinesInput{},
		func(ctx context.Context, tx pgx.Tx, pid, rid uuid.UUID, r *http.Request) (CourtBookingDetail, error) {
			var in LinesInput
			if err := httpx.Decode(r, &in); err != nil {
				return CourtBookingDetail{}, err
			}
			return m.NoShow(ctx, tx, pid, rid, in)
		})
	op("extras", "Charge rentals and drinks to the bill", "sportclub.booking.operate", ExtrasInput{},
		func(ctx context.Context, tx pgx.Tx, pid, rid uuid.UUID, r *http.Request) (CourtBookingDetail, error) {
			var in ExtrasInput
			if err := httpx.Decode(r, &in); err != nil {
				return CourtBookingDetail{}, err
			}
			return m.AddExtras(ctx, tx, pid, rid, in)
		})
	op("complete", "End of play (Selesai Main): the bill must be settled", "sportclub.booking.operate", CompleteInput{},
		func(ctx context.Context, tx pgx.Tx, pid, rid uuid.UUID, r *http.Request) (CourtBookingDetail, error) {
			var in CompleteInput
			if err := httpx.Decode(r, &in); err != nil {
				return CourtBookingDetail{}, err
			}
			return m.Complete(ctx, tx, pid, rid, in)
		})
	op("void", "Void a booking keyed in by mistake (supervisor, no refund)", "sportclub.booking.override", idsInput{},
		func(ctx context.Context, tx pgx.Tx, pid, rid uuid.UUID, r *http.Request) (CourtBookingDetail, error) {
			var in idsInput
			if err := httpx.Decode(r, &in); err != nil {
				return CourtBookingDetail{}, err
			}
			return m.VoidBooking(ctx, tx, pid, rid, in.Reason)
		})
	op("discount", "Manual discount (supervisor)", "sportclub.booking.override", DiscountInput{},
		func(ctx context.Context, tx pgx.Tx, pid, rid uuid.UUID, r *http.Request) (CourtBookingDetail, error) {
			var in DiscountInput
			if err := httpx.Decode(r, &in); err != nil {
				return CourtBookingDetail{}, err
			}
			return m.DiscountBooking(ctx, tx, pid, rid, in)
		})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/bookings/{id}:check-in", Summary: "Check in the lines of a booking (check-in open, paid)",
		Permission: "sportclub.booking.operate", Request: ScanInput{}, Response: ScanResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ScanInput) (ScanResult, error) {
			rid, err := handle.ID(r)
			if err != nil {
				return ScanResult{}, err
			}
			b, err := m.lockBooking(ctx, tx, handle.Property(ctx), rid)
			if err != nil {
				return ScanResult{}, err
			}
			return m.ScanCheckIn(ctx, tx, handle.Property(ctx), &b, in.LineIDs)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/check-in:scan", Summary: "Scan a booking QR or type its code: check-in or the reason of the refusal",
		Permission: "sportclub.booking.operate", Request: ScanInput{}, Response: ScanResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ScanInput) (ScanResult, error) {
			b, err := m.findByCode(ctx, tx, handle.Property(ctx), in.Code)
			if err != nil {
				return ScanResult{}, err
			}
			if b != nil {
				if _, err := m.lockBooking(ctx, tx, handle.Property(ctx), b.ID); err != nil {
					return ScanResult{}, err
				}
			}
			return m.ScanCheckIn(ctx, tx, handle.Property(ctx), b, in.LineIDs)
		})})
	// ── court staff, blocks ──
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/court-staff", Summary: "Court staff screen: the next two hours per court and open problems",
		Permission: "sportclub.court_report.view", Response: CourtStaffView{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CourtStaffView, error) {
			return m.courtStaff(ctx, tx, handle.Property(ctx))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/court-reports", Summary: "Court ready, or a problem (temporary block, front desk alerted)",
		Permission: "sportclub.court_report.create", Request: CourtReportInput{}, Response: CourtReportResult{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CourtReportInput) (CourtReportResult, error) {
			return m.ReportCourt(ctx, tx, handle.Property(ctx), in)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/court-reports/{id}:resolve", Summary: "Close a court problem (its block is removed)",
		Permission: "sportclub.court_report.create", Response: CourtReport{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (CourtReport, error) {
			id, err := handle.ID(r)
			if err != nil {
				return CourtReport{}, err
			}
			return m.ResolveReport(ctx, tx, handle.Property(ctx), id)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/blocks", Summary: "Blocked court slots of a period", Permission: "sportclub.booking.view",
		Response: BoardBlock{}, List: true, Query: []route.Param{{Name: "from"}, {Name: "to"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[BoardBlock], error) {
			from, to, err := localRange(ctx, tx, r, 30)
			if err != nil {
				return httpx.Page[BoardBlock]{}, err
			}
			return handle.Page(m.blocks(ctx, tx, handle.Property(ctx), from, to.AddDate(0, 0, 1)))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/blocks", Summary: "Block courts (maintenance, tournament, event); bookings in the way are listed",
		Permission: "sportclub.block.manage", Request: BlockInput{}, Response: BlockResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in BlockInput) (BlockResult, error) {
			return m.Block(ctx, tx, handle.Property(ctx), in)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/blocks/{id}:remove", Summary: "Remove a block (supervisor)", Permission: "sportclub.booking.override",
		Request: idsInput{}, Response: handle.Empty{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in idsInput) (handle.Empty, error) {
			id, err := handle.ID(r)
			if err != nil {
				return handle.Empty{}, err
			}
			return handle.Empty{}, m.Unblock(ctx, tx, handle.Property(ctx), id, in.Reason)
		})})
	// ── recurring bookings ──
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/recurring", Summary: "Recurring bookings (communities, schools, companies)",
		Permission: "sportclub.booking.view", Response: Recurring{}, List: true, Query: []route.Param{{Name: "filter[status]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Recurring], error) {
			lp := httpx.ParseList(r)
			return handle.Page(m.recurring(ctx, tx, ` WHERE g.property_id = $1 AND ($2 = '' OR g.status = $2) ORDER BY g.created_at DESC`,
				handle.Property(ctx), lp.Filters["status"]))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/recurring/{id}", Summary: "Recurring booking with its meetings", Permission: "sportclub.booking.view",
		Response: Recurring{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Recurring, error) {
			id, err := handle.ID(r)
			if err != nil {
				return Recurring{}, err
			}
			list, err := m.recurring(ctx, tx, ` WHERE g.id = $1 AND g.property_id = $2`, id, handle.Property(ctx))
			if err != nil || len(list) == 0 {
				if err == nil {
					err = errNotFound("recurring booking")
				}
				return Recurring{}, err
			}
			g := list[0]
			g.Bookings, err = m.loadBookings(ctx, tx, handle.Property(ctx), `AND r.recurring_group_id = $2 ORDER BY (SELECT min(lower(l.period))
				FROM reservation.reservation_lines l WHERE l.reservation_id = r.id)`, g.GroupID)
			return g, err
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/recurring:preview", Summary: "Meetings of a recurring booking with the clashes, before saving",
		Permission: "sportclub.recurring.manage", Request: RecurringInput{}, Response: RecurringPreview{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RecurringInput) (RecurringPreview, error) {
			return m.PreviewRecurring(ctx, tx, handle.Property(ctx), in)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/recurring", Summary: "Create a recurring booking (one booking per meeting)",
		Permission: "sportclub.recurring.manage", Request: RecurringInput{}, Response: Recurring{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RecurringInput) (Recurring, error) {
			return m.CreateRecurring(ctx, tx, handle.Property(ctx), in, r.Header.Get("Idempotency-Key"))
		})})
	for _, action := range []string{"pause", "resume", "stop", "extend"} {
		add(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/recurring/{id}:" + action, Summary: "Recurring booking: " + action,
			Permission: "sportclub.recurring.manage", Request: RecurringAction{}, Response: Recurring{}, Status: http.StatusOK,
			Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RecurringAction) (Recurring, error) {
				id, err := handle.ID(r)
				if err != nil {
					return Recurring{}, err
				}
				return m.ActRecurring(ctx, tx, handle.Property(ctx), id, action, in, r.Header.Get("Idempotency-Key"))
			})})
	}
	// ── admin: overview, report, incidents, settings, packages ──
	report := func(ctx context.Context, tx pgx.Tx, r *http.Request, days int) (CourtReportData, error) {
		from, to, err := localRange(ctx, tx, r, days)
		if err != nil {
			return CourtReportData{}, err
		}
		f, err := handle.QueryUUID(r, "facilityId")
		if err != nil {
			return CourtReportData{}, err
		}
		return m.CourtReport(ctx, tx, handle.Property(ctx), from, to, f)
	}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/overview", Summary: "Sport Club Overview: KPI of the period, next bookings, alerts",
		Permission: "sportclub.dashboard.view", Response: CourtReportData{}, Query: []route.Param{{Name: "from"}, {Name: "to"}, {Name: "facilityId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CourtReportData, error) {
			return report(ctx, tx, r, 0)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/court-report", Summary: "Utilisation, heatmap, revenue, channel, no-show, top customers",
		Permission: "sportclub.dashboard.view", Response: CourtReportData{}, Query: []route.Param{{Name: "from"}, {Name: "to"}, {Name: "facilityId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CourtReportData, error) {
			return report(ctx, tx, r, 29)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/incidents", Summary: "Sport Club incidents", Permission: "sportclub.incident.view",
		Response: Incident{}, List: true, Query: []route.Param{{Name: "filter[status]"}, {Name: "from"}, {Name: "to"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Incident], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[Incident](tx.Query(ctx, incidentSelect+` WHERE i.property_id = $1 AND ($2 = '' OR i.status = $2)
				ORDER BY i.occurred_at DESC LIMIT $3`, handle.Property(ctx), lp.Filters["status"], lp.Limit)))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/incidents", Summary: "Record an incident (injury, damage, complaint)",
		Permission: "sportclub.incident.create", Request: IncidentInput{}, Response: Incident{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in IncidentInput) (Incident, error) {
			return m.CreateIncident(ctx, tx, handle.Property(ctx), in)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/incidents/{id}:close", Summary: "Close an incident with the action taken",
		Permission: "sportclub.incident.manage", Request: idsInput{}, Response: Incident{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in idsInput) (Incident, error) {
			id, err := handle.ID(r)
			if err != nil {
				return Incident{}, err
			}
			return m.CloseIncident(ctx, tx, handle.Property(ctx), id, in.Reason)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/court-policy", Summary: "Court Booking policy in force", Permission: "sportclub.booking.view",
		Response: CourtPolicy{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (CourtPolicy, error) {
			return m.courtPolicy(ctx, tx, handle.Property(ctx))
		})})
	add(route.Route{Method: http.MethodPut, Path: "/api/v1/sportclub/court-policy", Summary: "Save the Court Booking policy (new version in force now)",
		Permission: "sportclub.setting.manage", Request: CourtPolicy{}, Response: CourtPolicy{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CourtPolicy) (CourtPolicy, error) {
			if err := validPolicy(in); err != nil {
				return in, err
			}
			pid := handle.Property(ctx)
			if _, err := rules.SaveClubPolicy(ctx, tx, &pid, "sportclub.court_booking", "Court booking", in); err != nil {
				return in, err
			}
			return m.courtPolicy(ctx, tx, pid)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/rate-card", Summary: "Court rates and packages (like the brochure)", Permission: "sportclub.booking.view",
		Response: RateCard{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (RateCard, error) {
			return m.rateCard(ctx, tx, handle.Property(ctx))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/packages", Summary: "Active court and class packages per customer with the quota left",
		Permission: "sportclub.booking.view", Response: ActivePackage{}, List: true, Query: []route.Param{{Name: "customerId"}, {Name: "q"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[ActivePackage], error) {
			c, err := handle.QueryUUID(r, "customerId")
			if err != nil {
				return httpx.Page[ActivePackage]{}, err
			}
			return handle.Page(m.activePackages(ctx, tx, handle.Property(ctx), c, httpx.ParseList(r).Q))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/charge-targets", Summary: "Court bookings in play whose bill a café order can be charged to",
		Permission: "commercial.order.pay", Response: ChargeTarget{}, List: true, Query: []route.Param{{Name: "q"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[ChargeTarget], error) {
			return handle.Page(m.chargeTargets(ctx, tx, handle.Property(ctx), r.URL.Query().Get("q")))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/staff-on-duty", Summary: "Sport Club staff on duty (HRIS roster)", Permission: "sportclub.booking.view",
		Response: DutyStaff{}, List: true, Query: []route.Param{{Name: "date"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[DutyStaff], error) {
			d, err := localDate(ctx, tx, r, "date")
			if err != nil {
				return httpx.Page[DutyStaff]{}, err
			}
			return handle.Page(m.onDuty(ctx, tx, handle.Property(ctx), d, d.AddDate(0, 0, 1)))
		})})
	m.registerCourtPublic(reg)
	m.registerCourtMember(reg)
}

func validPolicy(p CourtPolicy) error {
	switch {
	case p.WindowDays < 1 || p.WindowDays > 365:
		return handle.Invalid("windowDays", "invalid", "booking window between 1 and 365 days")
	case p.HoldMinutes < 5 || p.HoldMinutes > 120:
		return handle.Invalid("holdMinutes", "invalid", "hold between 5 and 120 minutes")
	case p.MinHours < 1 || p.MaxHours < p.MinHours || p.MaxHours > 12:
		return handle.Invalid("maxHours", "invalid", "minimum 1 hour, maximum between the minimum and 12 hours")
	}
	for i, o := range p.Methods {
		if _, err := decimal.NewFromString(nonEmpty(o.Fee, "0")); err != nil {
			return handle.Invalid(fmt.Sprintf("methods.%d.fee", i), "invalid", "fee must be a number")
		}
		if !strings.Contains("qris virtual_account card payment_gateway", o.MethodType) || o.MethodType == "" {
			return handle.Invalid(fmt.Sprintf("methods.%d.methodType", i), "invalid", "qris, virtual_account, card or payment_gateway")
		}
	}
	return nil
}

func localDate(ctx context.Context, q dbtx.Querier, r *http.Request, key string) (time.Time, error) {
	loc := calendar.Location(ctx, q)
	n := clock.Now().In(loc)
	d, err := handle.QueryDate(r, key, time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC))
	if err != nil {
		return d, err
	}
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc), nil
}

// localRange is [from, to] in local dates (default: the last days + today).
func localRange(ctx context.Context, q dbtx.Querier, r *http.Request, days int) (time.Time, time.Time, error) {
	loc := calendar.Location(ctx, q)
	n := clock.Now().In(loc)
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
	from, err := handle.QueryDate(r, "from", today.AddDate(0, 0, -days))
	if err != nil {
		return from, from, err
	}
	to, err := handle.QueryDate(r, "to", today)
	if err != nil {
		return from, to, err
	}
	from = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	to = time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, loc)
	if to.Before(from) {
		return from, to, handle.Invalid("to", "invalid_range", "to must be on or after from")
	}
	if to.Sub(from) > 400*24*time.Hour {
		return from, to, handle.Invalid("to", "too_long", "at most 400 days")
	}
	return from, to, nil
}

func (m *Module) lockBookingRead(ctx context.Context, q pgx.Tx, property, rid uuid.UUID) (uuid.UUID, error) {
	var x uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM reservation.reservations WHERE id = $1 AND property_id = $2 AND source_type = 'sportclub.court_booking'`, rid, property).Scan(&x)
	if err != nil {
		return x, errNotFound("court booking")
	}
	return x, nil
}

// listBookings filters the court bookings (Riwayat, Reservasi, Pembayaran).
func (m *Module) listBookings(ctx context.Context, tx pgx.Tx, property uuid.UUID, r *http.Request) ([]CourtBooking, error) {
	lp := httpx.ParseList(r)
	args := []any{}
	where := ""
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args)+1)
	}
	if r.URL.Query().Get("date") != "" || (r.URL.Query().Get("from") == "" && r.URL.Query().Get("to") == "" && lp.Q == "") {
		d, err := localDate(ctx, tx, r, "date")
		if err != nil {
			return nil, err
		}
		where += ` AND EXISTS (SELECT 1 FROM reservation.reservation_lines l WHERE l.reservation_id = r.id AND l.period && tstzrange(` + arg(d) + `, ` +
			arg(d.AddDate(0, 0, 1)) + `, '[)'))`
	} else if r.URL.Query().Get("from") != "" || r.URL.Query().Get("to") != "" {
		from, to, err := localRange(ctx, tx, r, 30)
		if err != nil {
			return nil, err
		}
		where += ` AND EXISTS (SELECT 1 FROM reservation.reservation_lines l WHERE l.reservation_id = r.id AND l.period && tstzrange(` + arg(from) + `, ` +
			arg(to.AddDate(0, 0, 1)) + `, '[)'))`
	}
	if lp.Q != "" {
		p := arg(strings.TrimSpace(lp.Q))
		where += ` AND (r.code ILIKE '%' || ` + p + ` || '%' OR c.name ILIKE '%' || ` + p + ` || '%' OR r.guest_name ILIKE '%' || ` + p + ` || '%'
			OR coalesce(c.phone, r.guest_phone, '') ILIKE '%' || ` + p + ` || '%' OR r.corporate_name ILIKE '%' || ` + p + ` || '%')`
	}
	if v := lp.Filters["customerId"]; v != "" {
		where += ` AND r.customer_id::text = ` + arg(v)
	}
	if v := lp.Filters["recurringGroupId"]; v != "" {
		where += ` AND r.recurring_group_id::text = ` + arg(v)
	}
	if v := lp.Filters["facilityId"]; v != "" {
		where += ` AND EXISTS (SELECT 1 FROM reservation.reservation_lines l JOIN sportclub.courts cc ON cc.resource_id = l.resource_id
			WHERE l.reservation_id = r.id AND cc.facility_id::text = ` + arg(v) + `)`
	}
	if v := lp.Filters["courtId"]; v != "" {
		where += ` AND EXISTS (SELECT 1 FROM reservation.reservation_lines l JOIN sportclub.courts cc ON cc.resource_id = l.resource_id
			WHERE l.reservation_id = r.id AND cc.id::text = ` + arg(v) + `)`
	}
	limit := lp.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	list, err := m.loadBookings(ctx, tx, property, where+` ORDER BY (SELECT min(lower(l.period)) FROM reservation.reservation_lines l WHERE l.reservation_id = r.id) DESC
		LIMIT `+arg(limit), args...)
	if err != nil {
		return nil, err
	}
	out := []CourtBooking{}
	for _, b := range list {
		if v := lp.Filters["state"]; v != "" && !strings.Contains(","+v+",", ","+b.State+",") {
			continue
		}
		if v := lp.Filters["channel"]; v != "" && !strings.Contains(","+v+",", ","+b.Channel+",") {
			continue
		}
		if v := lp.Filters["payStatus"]; v != "" && !strings.Contains(","+v+",", ","+b.PayStatus+",") {
			continue
		}
		out = append(out, b)
	}
	return out, nil
}

// ActivePackage is a court or class package with its quota left (FR-76).
type ActivePackage struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	Code         string     `json:"code" db:"code"`
	TypeName     string     `json:"typeName" db:"type_name"`
	Category     string     `json:"category" db:"category"`
	CustomerID   *uuid.UUID `json:"customerId" db:"customer_id"`
	CustomerName *string    `json:"customerName" db:"customer_name"`
	Original     string     `json:"original" db:"original"`
	Remaining    string     `json:"remaining" db:"remaining"`
	ExpiresAt    *time.Time `json:"expiresAt" db:"expires_at"`
	Status       string     `json:"status" db:"status"`
	PricePaid    string     `json:"pricePaid" db:"price_paid"`
}

func (m *Module) activePackages(ctx context.Context, q dbtx.Querier, property uuid.UUID, customer *uuid.UUID, search string) ([]ActivePackage, error) {
	return handle.List[ActivePackage](q.Query(ctx, `SELECT v.id, v.code, t.name AS type_name, t.category, v.customer_id, c.name AS customer_name,
		trim_scale(v.original_quantity)::text AS original, trim_scale(v.remaining_quantity)::text AS remaining, v.expires_at, v.status,
		trim_scale(v.price_paid)::text AS price_paid
		FROM commercial.vouchers v JOIN commercial.voucher_types t ON t.id = v.voucher_type_id LEFT JOIN crm.customers c ON c.id = v.customer_id
		WHERE v.property_id = $1 AND t.category IN ('court_package', 'class_package') AND v.status IN ('active', 'partially_redeemed')
		AND v.remaining_quantity > 0 AND (v.expires_at IS NULL OR v.expires_at > now()) AND ($2::uuid IS NULL OR v.customer_id = $2)
		AND ($3 = '' OR v.code ILIKE '%' || $3 || '%' OR c.name ILIKE '%' || $3 || '%' OR c.phone ILIKE '%' || $3 || '%')
		ORDER BY v.expires_at NULLS LAST, v.code LIMIT 300`, property, customer, search))
}

// ── website (public) ────────────────────────────────────────────────────────

// PublicGridQuery is answered by GET /api/v1/public/sport-club/grid.
type PublicQuoteInput struct {
	PropertyID uuid.UUID   `json:"propertyId"`
	Lines      []CourtLine `json:"lines"`
	PromoCode  string      `json:"promoCode,omitempty"`
	Method     string      `json:"method,omitempty"`
}

// PublicCourtBookingView is the confirmation / Cek Booking page.
type PublicCourtBookingView struct {
	Booking PublicBookingInfo     `json:"booking"`
	Lines   []PublicBillLine      `json:"bill"`
	Payment *PublicPendingPayment `json:"payment"`
	Club    ClubInfo              `json:"club"`
	Terms   map[string]string     `json:"terms"`
	Methods []OnlineMethod        `json:"methods"`
}

// PublicBookingInfo is the part of a booking the guest sees.
type PublicBookingInfo struct {
	Code        string             `json:"code"`
	Token       string             `json:"token"`
	State       string             `json:"state"`
	PayStatus   string             `json:"payStatus"`
	Name        string             `json:"name"`
	Channel     string             `json:"channel"`
	Start       *time.Time         `json:"start"`
	End         *time.Time         `json:"end"`
	HoldSeconds int                `json:"holdSeconds"`
	Charges     string             `json:"charges"`
	Paid        string             `json:"paid"`
	Balance     string             `json:"balance"`
	Tax         string             `json:"tax"`
	ServiceFee  string             `json:"serviceFee"`
	PackageCode string             `json:"packageCode,omitempty"`
	Lines       []CourtBookingLine `json:"lines"`
}

// PublicBillLine is one line of the bill.
type PublicBillLine struct {
	Description string `json:"description"`
	Net         string `json:"net"`
	Tax         string `json:"tax"`
	Total       string `json:"total"`
	Kind        string `json:"kind" enum:"rent,service_fee,discount,extra"`
}

// PublicPendingPayment is the online payment waiting on the mock gateway.
type PublicPendingPayment struct {
	Number      string     `json:"number"`
	Method      string     `json:"method"`
	Amount      string     `json:"amount"`
	Status      string     `json:"status"`
	QRString    *string    `json:"qrString"`
	VANumber    *string    `json:"vaNumber"`
	ExpiresAt   *time.Time `json:"expiresAt"`
	CheckoutURL *string    `json:"checkoutUrl"`
	Sandbox     bool       `json:"sandbox" doc:"Mock gateway: pay on the website payment page"`
}

// ClubInfo is the address of the club on the confirmation.
type ClubInfo struct {
	Name    string  `json:"name" db:"name"`
	Address *string `json:"address" db:"address"`
	City    *string `json:"city" db:"city"`
	Phone   *string `json:"phone" db:"phone"`
}

// LookupInput finds a booking from its code and the phone or e-mail.
type LookupInput struct {
	PropertyID uuid.UUID `json:"propertyId"`
	Code       string    `json:"code"`
	Contact    string    `json:"contact" doc:"Phone or e-mail used for the booking"`
}

// LookupResult is the token of the Cek Booking page.
type LookupResult struct {
	Token string `json:"token"`
}

func (m *Module) publicView(ctx context.Context, tx pgx.Tx, property uuid.UUID, b CourtBooking) (PublicCourtBookingView, error) {
	pol, err := m.courtPolicy(ctx, tx, property)
	if err != nil {
		return PublicCourtBookingView{}, err
	}
	out := PublicCourtBookingView{Lines: []PublicBillLine{}, Terms: pol.Terms, Methods: []OnlineMethod{},
		Booking: PublicBookingInfo{Code: b.Code, Token: b.Token, State: b.State, PayStatus: b.PayStatus, Name: maskName(b.Name), Channel: b.Channel,
			Start: b.Start, End: b.End, HoldSeconds: b.HoldSeconds, Charges: trimDec(b.Charges), Paid: trimDec(b.Paid), Balance: b.Balance,
			Tax: trimDec(b.Tax), ServiceFee: trimDec(b.ServiceFee), PackageCode: b.PackageCode, Lines: b.Lines}}
	for _, o := range pol.Methods {
		if o.Active {
			out.Methods = append(out.Methods, o)
		}
	}
	if err := tx.QueryRow(ctx, `SELECT name, address, city, phone FROM platform.properties WHERE id = $1`, property).Scan(&out.Club.Name, &out.Club.Address,
		&out.Club.City, &out.Club.Phone); err != nil {
		return out, err
	}
	if b.FolioID != nil {
		f, err := billing.GetFolio(ctx, tx, *b.FolioID)
		if err != nil {
			return out, err
		}
		for _, l := range f.Lines {
			if l.VoidedAt != nil {
				continue
			}
			kind := "rent"
			switch {
			case l.ReferenceType != nil && *l.ReferenceType == "sportclub.service_fee":
				kind = "service_fee"
			case l.ReferenceType != nil && *l.ReferenceType == "sportclub.discount":
				kind = "discount"
			case l.ReferenceType != nil && *l.ReferenceType == "sportclub.extra":
				kind = "extra"
			}
			out.Lines = append(out.Lines, PublicBillLine{Description: l.Description, Net: trimDec(l.NetAmount), Tax: trimDec(decimal.RequireFromString(l.TaxAmount).
				Add(decimal.RequireFromString(l.ServiceAmount)).String()), Total: trimDec(l.Total), Kind: kind})
		}
		if p, err := billing.PendingOnline(ctx, tx, *b.FolioID); err != nil {
			return out, err
		} else if p != nil {
			out.Payment = &PublicPendingPayment{Number: p.Number, Method: p.MethodType, Amount: p.Amount, Status: p.Status, QRString: p.QRString, VANumber: p.VANumber,
				ExpiresAt: p.ExpiresAt, CheckoutURL: p.CheckoutURL, Sandbox: p.CheckoutURL != nil && strings.Contains(*p.CheckoutURL, "sandbox.pay.local")}
			if b.HoldExpiresAt != nil && (p.ExpiresAt == nil || b.HoldExpiresAt.Before(*p.ExpiresAt)) {
				out.Payment.ExpiresAt = b.HoldExpiresAt
			}
		}
	}
	return out, nil
}

// maskName keeps the first name and the initials (the page is shareable).
func maskName(n string) string {
	parts := strings.Fields(n)
	if len(parts) <= 1 {
		return n
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += " " + string([]rune(p)[0]) + "."
	}
	return out
}

func (m *Module) publicRead(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, tx pgx.Tx, pid uuid.UUID) (any, error)) {
	pid, err := uuid.Parse(r.URL.Query().Get("propertyId"))
	if err != nil {
		httpx.WriteError(w, r, errs.BadRequest("property_required", "propertyId is required"))
		return
	}
	ctx := crm.PublicCtx(r.Context(), pid)
	var out any
	err = m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = fn(ctx, tx, pid)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (m *Module) publicWrite(w http.ResponseWriter, r *http.Request, pid uuid.UUID, fn func(ctx context.Context, tx pgx.Tx) (any, error)) {
	if !crm.PublicLimiter.Allow(r.RemoteAddr + r.URL.Path) {
		httpx.WriteError(w, r, errs.RateLimited())
		return
	}
	ctx := crm.PublicCtx(r.Context(), pid)
	var out any
	err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = fn(ctx, tx)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// facilityByKey finds a sport by id or website slug.
func (m *Module) facilityByKey(ctx context.Context, q dbtx.Querier, property uuid.UUID, key string) (uuid.UUID, error) {
	if id, err := uuid.Parse(key); err == nil {
		return id, nil
	}
	var fid uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM sportclub.facilities WHERE property_id = $1 AND usage_mode = 'slot_booking' AND archived_at IS NULL
		AND (lower(content ->> 'slug') = lower($2) OR lower(code) = lower($2)) ORDER BY sort_order LIMIT 1`, property, key).Scan(&fid)
	if dbtx.IsNoRows(err) {
		return fid, errNotFound("sport")
	}
	return fid, err
}

func (m *Module) registerCourtPublic(reg *route.Registry) {
	pub := func(rt route.Route) { crm.PublicRoute(reg, "sportclub", rt) }
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/sport-club/grid", Summary: "Website slot grid of a sport (status and price per hour, before tax)",
		Response: Grid{}, Query: []route.Param{{Name: "propertyId", Required: true}, {Name: "facility", Required: true, Description: "Sport id or slug"}, {Name: "date"}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			m.publicRead(w, r, func(ctx context.Context, tx pgx.Tx, pid uuid.UUID) (any, error) {
				fid, err := m.facilityByKey(ctx, tx, pid, r.URL.Query().Get("facility"))
				if err != nil {
					return nil, err
				}
				d, err := localDate(ctx, tx, r, "date")
				if err != nil {
					return nil, err
				}
				pol, err := m.courtPolicy(ctx, tx, pid)
				if err != nil {
					return nil, err
				}
				n := clock.Now().In(d.Location())
				today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, d.Location())
				if d.Before(today) || d.After(today.AddDate(0, 0, pol.WindowDays)) {
					return nil, errs.Validation("outside_booking_window", fmt.Sprintf("courts can be booked up to %d days ahead", pol.WindowDays))
				}
				return m.grid(ctx, tx, pid, fid, d, true, nil)
			})
		}})
	pub(route.Route{Method: http.MethodPost, Path: "/api/v1/public/sport-club/quote", Summary: "Website quote of a cart: rent, promotion, tax, service fee per method",
		Request: PublicQuoteInput{}, Response: Quote{},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			var in PublicQuoteInput
			if err := httpx.Decode(r, &in); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			ctx := crm.PublicCtx(r.Context(), in.PropertyID)
			var out Quote
			err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
				pol, err := m.courtPolicy(ctx, tx, in.PropertyID)
				if err != nil {
					return err
				}
				slots, err := m.normalizeLines(ctx, tx, in.PropertyID, in.Lines, pol, slotOptions{Online: true, SkipHours: true})
				if err != nil {
					return err
				}
				out, err = m.quote(ctx, tx, in.PropertyID, quoteRequest{Slots: slots, Promo: in.PromoCode, Channel: "website", Method: in.Method, Online: true}, pol)
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, out)
		}})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/sport-club/rate-card", Summary: "Court rates and packages (like the brochure)", Response: RateCard{},
		Query: []route.Param{{Name: "propertyId", Required: true}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			m.publicRead(w, r, func(ctx context.Context, tx pgx.Tx, pid uuid.UUID) (any, error) { return m.rateCard(ctx, tx, pid) })
		}})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/court-bookings/{token}", Summary: "Booking confirmation / Cek Booking (public token)",
		Response: PublicCourtBookingView{}, Query: []route.Param{{Name: "propertyId", Required: true}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			m.publicRead(w, r, func(ctx context.Context, tx pgx.Tx, pid uuid.UUID) (any, error) {
				b, err := m.bookingByToken(ctx, tx, pid, chi.URLParam(r, "token"))
				if err != nil {
					return nil, err
				}
				return m.publicView(ctx, tx, pid, b)
			})
		}})
	pub(route.Route{Method: http.MethodPost, Path: "/api/v1/public/court-bookings/{token}:abandon", Summary: "Release a held booking after a failed payment (slots free again)",
		Response: PublicCourtBookingView{}, Query: []route.Param{{Name: "propertyId", Required: true}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			pid, err := uuid.Parse(r.URL.Query().Get("propertyId"))
			if err != nil {
				httpx.WriteError(w, r, errs.BadRequest("property_required", "propertyId is required"))
				return
			}
			m.publicWrite(w, r, pid, func(ctx context.Context, tx pgx.Tx) (any, error) {
				b, err := m.bookingByToken(ctx, tx, pid, chi.URLParam(r, "token"))
				if err != nil {
					return nil, err
				}
				if b.Status == reservation.StatusDraft && b.PayStatus != "paid" {
					if b.FolioID != nil {
						if err := m.Billing.CancelPending(ctx, tx, *b.FolioID, "payment failed on the website"); err != nil {
							return nil, err
						}
					}
					if _, err := m.Res.ExpireNow(ctx, tx, b.ID, "payment failed"); err != nil {
						return nil, err
					}
					if err := m.live(ctx, tx, pid, "expired", b.ID); err != nil {
						return nil, err
					}
				}
				nb, err := m.bookingByToken(ctx, tx, pid, b.Token)
				if err != nil {
					return nil, err
				}
				return m.publicView(ctx, tx, pid, nb)
			})
		}})
	pub(route.Route{Method: http.MethodPost, Path: "/api/v1/public/court-bookings:lookup", Summary: "Cek Booking: code + phone or e-mail → the booking page",
		Request: LookupInput{}, Response: LookupResult{},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			var in LookupInput
			if err := httpx.Decode(r, &in); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			m.publicWrite(w, r, in.PropertyID, func(ctx context.Context, tx pgx.Tx) (any, error) {
				contact := strings.ToLower(strings.TrimSpace(in.Contact))
				digits := strings.Map(func(c rune) rune {
					if c >= '0' && c <= '9' {
						return c
					}
					return -1
				}, contact)
				if len(digits) > 8 {
					digits = digits[len(digits)-8:]
				}
				var token string
				err := tx.QueryRow(ctx, `SELECT r.attributes ->> 'publicToken' FROM reservation.reservations r LEFT JOIN crm.customers c ON c.id = r.customer_id
					WHERE r.property_id = $1 AND r.code = upper($2) AND r.source_type = 'sportclub.court_booking'
					AND (lower(coalesce(c.email, r.guest_email, '')) = $3 OR ($4 <> '' AND right(regexp_replace(coalesce(c.phone, r.guest_phone, ''), '[^0-9]', '', 'g'), 8) = $4))`,
					in.PropertyID, strings.TrimSpace(in.Code), contact, digits).Scan(&token)
				if dbtx.IsNoRows(err) || token == "" {
					return nil, errs.NotFound("booking with this code and phone / e-mail")
				}
				return LookupResult{Token: token}, err
			})
		}})
	file := func(path, summary, contentType string, fn func(ctx context.Context, tx pgx.Tx, pid uuid.UUID, b CourtBooking) ([]byte, string, error)) {
		pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/court-bookings/{token}/" + path, Summary: summary, RawContent: contentType,
			Query: []route.Param{{Name: "propertyId", Required: true}},
			Handler: func(w http.ResponseWriter, r *http.Request) {
				pid, err := uuid.Parse(r.URL.Query().Get("propertyId"))
				if err != nil {
					httpx.WriteError(w, r, errs.BadRequest("property_required", "propertyId is required"))
					return
				}
				ctx := crm.PublicCtx(r.Context(), pid)
				var body []byte
				var name string
				err = m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
					b, err := m.bookingByToken(ctx, tx, pid, chi.URLParam(r, "token"))
					if err != nil {
						return err
					}
					body, name, err = fn(ctx, tx, pid, b)
					return err
				})
				if err != nil {
					httpx.WriteError(w, r, err)
					return
				}
				w.Header().Set("Content-Type", contentType)
				if name != "" {
					w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
				}
				w.Header().Set("Cache-Control", "no-store")
				_, _ = w.Write(body)
			}})
	}
	file("qr.png", "QR check-in of the booking (PNG)", "image/png", func(ctx context.Context, tx pgx.Tx, pid uuid.UUID, b CourtBooking) ([]byte, string, error) {
		img, err := qrPNG(b.Token, 320)
		return img, "", err
	})
	file("e-ticket.pdf", "E-ticket (PDF)", "application/pdf", func(ctx context.Context, tx pgx.Tx, pid uuid.UUID, b CourtBooking) ([]byte, string, error) {
		v, err := m.publicView(ctx, tx, pid, b)
		if err != nil {
			return nil, "", err
		}
		body, err := eTicketPDF(v, calendar.Location(ctx, tx))
		return body, "e-ticket-" + b.Code + ".pdf", err
	})
	file("calendar.ics", "Add to calendar (.ics)", "text/calendar", func(ctx context.Context, tx pgx.Tx, pid uuid.UUID, b CourtBooking) ([]byte, string, error) {
		var club ClubInfo
		_ = tx.QueryRow(ctx, `SELECT name, address, city, phone FROM platform.properties WHERE id = $1`, pid).Scan(&club.Name, &club.Address, &club.City, &club.Phone)
		return icsOf(b, club), "booking-" + b.Code + ".ics", nil
	})
}

func qrPNG(content string, size int) ([]byte, error) {
	code, err := qrcode.Encode(content, qrcode.M, qrcode.Auto)
	if err != nil {
		return nil, err
	}
	code, err = barcode.Scale(code, size, size)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, code); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// eTicketPDF renders the e-ticket: code, QR, courts and hours, payer, the
// bill and the rules (FR-41).
func eTicketPDF(v PublicCourtBookingView, loc *time.Location) ([]byte, error) {
	d := pdf.New()
	d.Row(16, true, "E-Ticket Sport Club", v.Club.Name)
	d.Row(9, false, "Kode booking: "+v.Booking.Code, "Status: "+stateLabelID(v.Booking.State)+" · "+payLabelID(v.Booking.PayStatus))
	d.Space(4)
	img, err := qrPNG(v.Booking.Token, 300)
	if err != nil {
		return nil, err
	}
	y := d.Y - 150
	if err := d.Image(img, pdf.Margin, y, 150, 150); err != nil {
		return nil, err
	}
	d.Text(pdf.Margin+170, d.Y-14, 11, true, "Atas nama: "+v.Booking.Name)
	d.Text(pdf.Margin+170, d.Y-30, 9, false, "Scan QR ini di front desk Sport Club.")
	d.Text(pdf.Margin+170, d.Y-44, 9, false, "Datang 15 menit sebelum jam main.")
	d.Text(pdf.Margin+170, d.Y-58, 9, false, "Check-in dibuka 30 menit sebelum jam main.")
	d.Y = y - 10
	d.Row(11, true, "Jadwal")
	for _, l := range v.Booking.Lines {
		if l.State == StateVoid || l.State == StateExpired {
			continue
		}
		d.Row(9, false, fmt.Sprintf("%s · %s", deref(l.FacilityName), l.CourtName),
			fmt.Sprintf("%s %s–%s", l.Start.In(loc).Format("Mon 2 Jan 2006"), l.Start.In(loc).Format("15:04"), l.End.In(loc).Format("15:04")))
	}
	d.Space(6)
	d.Row(11, true, "Rincian biaya")
	for _, l := range v.Lines {
		d.Row(9, false, l.Description, "Rp "+fmtMoney(l.Total))
	}
	if v.Booking.PackageCode != "" {
		d.Row(9, false, "Dibayar dengan paket "+v.Booking.PackageCode, "")
	}
	d.Row(9, true, "Total", "Rp "+fmtMoney(v.Booking.Charges))
	d.Row(9, false, "Dibayar", "Rp "+fmtMoney(v.Booking.Paid))
	d.Space(6)
	addr := deref(v.Club.Address)
	if c := deref(v.Club.City); c != "" {
		addr += ", " + c
	}
	d.Row(9, false, v.Club.Name+" — "+addr)
	if p := deref(v.Club.Phone); p != "" {
		d.Row(9, false, "Telepon: "+p)
	}
	d.Space(4)
	for _, line := range wrap(v.Terms["id"], 110) {
		d.Row(8, false, line)
	}
	return d.Bytes(), nil
}

func wrap(s string, n int) []string {
	var out []string
	cur := ""
	for _, w := range strings.Fields(s) {
		if len(cur)+len(w)+1 > n {
			out = append(out, cur)
			cur = w
			continue
		}
		if cur != "" {
			cur += " "
		}
		cur += w
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func stateLabelID(s string) string {
	return map[string]string{StateAwaitingPayment: "Menunggu Pembayaran", StateExpired: "Kedaluwarsa", StateScheduled: "Terjadwal", StateLate: "Belum Datang",
		StatePlaying: "Sedang Main", StateFinished: "Selesai", StateNoShow: "No-show", StateVoid: "Void"}[s]
}

func payLabelID(s string) string {
	return map[string]string{"paid": "Lunas", "unpaid": "Belum bayar", "partially_paid": "Sebagian", "overpaid": "Lebih bayar"}[s]
}

func icsOf(b CourtBooking, club ClubInfo) []byte {
	f := func(t time.Time) string { return t.UTC().Format("20060102T150405Z") }
	esc := func(s string) string {
		return strings.NewReplacer(`\`, `\\`, ",", `\,`, ";", `\;`, "\n", `\n`).Replace(s)
	}
	var sb strings.Builder
	sb.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//OneClub//Sport Club//ID\r\n")
	for _, l := range b.Lines {
		if l.State == StateVoid || l.State == StateExpired {
			continue
		}
		sb.WriteString("BEGIN:VEVENT\r\n")
		sb.WriteString("UID:" + l.ID.String() + "@oneclub\r\n")
		sb.WriteString("DTSTAMP:" + f(clock.Now()) + "\r\n")
		sb.WriteString("DTSTART:" + f(l.Start) + "\r\nDTEND:" + f(l.End) + "\r\n")
		sb.WriteString("SUMMARY:" + esc(deref(l.FacilityName)+" · "+l.CourtName) + "\r\n")
		loc := club.Name
		if a := strings.TrimSpace(deref(club.Address)); a != "" {
			loc += ", " + a
		}
		sb.WriteString("LOCATION:" + esc(loc) + "\r\n")
		sb.WriteString("DESCRIPTION:" + esc("Booking "+b.Code+". Scan QR di front desk Sport Club, datang 15 menit sebelumnya.") + "\r\n")
		sb.WriteString("END:VEVENT\r\n")
	}
	sb.WriteString("END:VCALENDAR\r\n")
	return []byte(sb.String())
}

// ── Member App ──────────────────────────────────────────────────────────────

// MySportSummary is the Sport Club part of the Member App home (§8.4).
type MySportSummary struct {
	NextBooking *CourtBooking   `json:"nextBooking"`
	NextClass   *SessionBooking `json:"nextClass"`
	Packages    []ActivePackage `json:"packages"`
	Membership  *string         `json:"membership" doc:"Sport Club membership type in force"`
	Status      *string         `json:"status"`
}

// PackageType is a package a member can buy.
type PackageType struct {
	ID       uuid.UUID `json:"id" db:"id"`
	Code     string    `json:"code" db:"code"`
	Name     string    `json:"name" db:"name"`
	Category string    `json:"category" db:"category"`
	Uses     string    `json:"uses" db:"uses"`
	Price    string    `json:"price" db:"price"`
	Items    []string  `json:"items" db:"items"`
	Months   *int      `json:"validityMonths" db:"validity_months"`
}

// GuestTicketInput buys a Guest With Member ticket for a guest (FR-117).
type GuestTicketInput struct {
	GuestName    string `json:"guestName"`
	GuestPhone   string `json:"guestPhone,omitempty"`
	VisitDate    string `json:"visitDate,omitempty" doc:"YYYY-MM-DD; default today"`
	MemberCharge bool   `json:"memberCharge,omitempty"`
	PayMethod    string `json:"payMethod,omitempty" doc:"Online method code when not charged to the member account"`
}

// GuestTicketResult is the ticket with the share link of the guest.
type GuestTicketResult struct {
	Entry    Entry             `json:"entry"`
	Checkout *billing.Checkout `json:"checkout"`
	Token    string            `json:"token" doc:"QR of the ticket for the share link"`
}

func (m *Module) registerCourtMember(reg *route.Registry) {
	db := m.DB
	me := func(rt route.Route) { crm.MeRoute(reg, "sportclub", "Member Portal", rt) }
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/sport-club/court-bookings", Summary: "My court bookings (upcoming and history, QR, e-ticket)",
		Response: CourtBooking{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CourtBooking], error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[CourtBooking]{}, err
			}
			list, err := m.loadBookings(ctx, tx, p.PropertyID, `AND r.customer_id = $2 ORDER BY (SELECT min(lower(l.period)) FROM reservation.reservation_lines l
				WHERE l.reservation_id = r.id) DESC LIMIT 100`, p.ID)
			for i := range list {
				list[i].Token, _ = list[i].Attributes["publicToken"].(string)
			}
			return handle.Page(list, err)
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/sport-club/summary", Summary: "Sport Club home: next court booking, next class, packages, membership",
		Response: MySportSummary{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (MySportSummary, error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return MySportSummary{}, err
			}
			out := MySportSummary{Packages: []ActivePackage{}}
			list, err := m.loadBookings(ctx, tx, p.PropertyID, `AND r.customer_id = $2 AND r.status IN ('draft', 'confirmed', 'checked_in')
				AND EXISTS (SELECT 1 FROM reservation.reservation_lines l WHERE l.reservation_id = r.id AND upper(l.period) > now())
				ORDER BY (SELECT min(lower(l.period)) FROM reservation.reservation_lines l WHERE l.reservation_id = r.id) LIMIT 5`, p.ID)
			if err != nil {
				return out, err
			}
			for i := range list {
				if list[i].State != StateExpired && !list[i].Void {
					list[i].Token, _ = list[i].Attributes["publicToken"].(string)
					out.NextBooking = &list[i]
					break
				}
			}
			sb, err := handle.List[SessionBooking](tx.Query(ctx, bookingSelect+` WHERE b.customer_id = $1 AND b.status = 'booked'
				AND EXISTS (SELECT 1 FROM sportclub.class_sessions s WHERE s.id = b.session_id AND lower(s.period) > now()) ORDER BY b.created_at LIMIT 1`, p.ID))
			if err != nil {
				return out, err
			}
			if len(sb) > 0 {
				out.NextClass = &sb[0]
			}
			if out.Packages, err = m.activePackages(ctx, tx, p.PropertyID, &p.ID, ""); err != nil {
				return out, err
			}
			_ = tx.QueryRow(ctx, `SELECT t.name, ms.status FROM membership.memberships ms JOIN membership.members mb ON mb.id = ms.member_id
				JOIN membership.types t ON t.id = ms.type_id JOIN membership.programs pg ON pg.id = t.program_id
				WHERE mb.customer_id = $1 AND pg.program_kind = 'sport_club' ORDER BY (ms.status = 'active') DESC, ms.starts_on DESC LIMIT 1`, p.ID).
				Scan(&out.Membership, &out.Status)
			return out, nil
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/sport-club/packages", Summary: "My court and class packages with the quota left",
		Response: ActivePackage{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[ActivePackage], error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[ActivePackage]{}, err
			}
			return handle.Page(m.activePackages(ctx, tx, p.PropertyID, &p.ID, ""))
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/sport-club/package-types", Summary: "Court and class packages I can buy (member rates)",
		Response: PackageType{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[PackageType], error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[PackageType]{}, err
			}
			// a class package of the guest rate (code -G-) is not offered to members (member rate, brochure)
			return handle.Page(handle.List[PackageType](tx.Query(ctx, `SELECT id, code, name, category, trim_scale(face_value)::text AS uses,
				trim_scale(coalesce(member_price, price))::text AS price, applicable_items AS items, validity_months
				FROM commercial.voucher_types WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL
				AND category IN ('court_package', 'class_package', 'sport_entry') AND code NOT LIKE '%-G-%' ORDER BY category, code`, p.PropertyID)))
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/sport-club/quote", Summary: "Price of courts and hours for the Member App (service fee of the method)",
		Request: QuoteInput{}, Response: Quote{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in QuoteInput) (Quote, error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return Quote{}, err
			}
			pol, err := m.courtPolicy(ctx, tx, p.PropertyID)
			if err != nil {
				return Quote{}, err
			}
			slots, err := m.normalizeLines(ctx, tx, p.PropertyID, in.Lines, pol, slotOptions{Online: true, SkipHours: true})
			if err != nil {
				return Quote{}, err
			}
			return m.quote(ctx, tx, p.PropertyID, quoteRequest{Slots: slots, Promo: in.PromoCode, Customer: &p.ID, Channel: "member_app", Method: in.Method,
				Online: in.Method != ""}, pol)
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/sport-club/guest-tickets", Summary: "Guest With Member ticket for my guest (share link)",
		Request: GuestTicketInput{}, Response: GuestTicketResult{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in GuestTicketInput) (GuestTicketResult, error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return GuestTicketResult{}, err
			}
			if strings.TrimSpace(in.GuestName) == "" {
				return GuestTicketResult{}, handle.Invalid("guestName", "required", "the guest's name is required")
			}
			var fid uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT id FROM sportclub.facilities WHERE property_id = $1 AND usage_mode = 'entry' AND status = 'active'
				AND archived_at IS NULL ORDER BY (facility_type = 'swimming_pool') DESC, name LIMIT 1`, p.PropertyID).Scan(&fid); err != nil {
				return GuestTicketResult{}, errs.Conflict("no_entry_facility", "no pool or gym to enter")
			}
			ei := EntryInput{FacilityID: fid, EntryType: "guest_of_member", HostCustomerID: &p.ID, Guest: &GuestInput{Name: in.GuestName, Phone: in.GuestPhone},
				VisitDate: in.VisitDate, Channel: "member_app", SkipHostPresence: true, BillToHost: in.MemberCharge}
			if in.MemberCharge {
				ei.Payment = &PaymentInput{MethodType: "member_account"}
			}
			res, err := m.CreateEntry(ctx, tx, p.PropertyID, ei, r.Header.Get("Idempotency-Key"))
			if err != nil {
				return GuestTicketResult{}, err
			}
			out := GuestTicketResult{Entry: res.Entry, Token: res.Entry.QRToken}
			if !in.MemberCharge && res.Folio != nil && in.PayMethod != "" {
				pol, err := m.courtPolicy(ctx, tx, p.PropertyID)
				if err != nil {
					return out, err
				}
				o, ok := pol.method(in.PayMethod)
				if !ok {
					return out, handle.Invalid("payMethod", "invalid_method", "this payment method is not available")
				}
				amt, _ := decimal.NewFromString(res.Folio.Balance)
				if _, err := m.serviceFee(ctx, tx, res.Folio.ID, o, amt); err != nil {
					return out, err
				}
				co, err := m.Billing.Checkout(ctx, tx, billing.CheckoutRequest{FolioID: res.Folio.ID, Method: o.MethodType, Description: "Guest ticket " + res.Entry.TicketNo})
				if err != nil {
					return out, err
				}
				out.Checkout = &co
			}
			return out, nil
		})})
	crm.PublicRoute(reg, "sportclub", route.Route{Method: http.MethodGet, Path: "/api/v1/public/sport-club/tickets/{token}", Summary: "Entry ticket shared with a guest (QR)",
		Response: Entry{}, Query: []route.Param{{Name: "propertyId", Required: true}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			m.publicRead(w, r, func(ctx context.Context, tx pgx.Tx, pid uuid.UUID) (any, error) {
				token := chi.URLParam(r, "token")
				if len(token) < 16 {
					return nil, errNotFound("ticket")
				}
				rows, err := tx.Query(ctx, entrySelect+` WHERE e.property_id = $1 AND e.qr_token = $2`, pid, token)
				e, err := handle.One[Entry](rows, err, "ticket")
				if err == nil {
					e.CustomerName = nil // the host stays private
				}
				return e, err
			})
		}})
	crm.PublicRoute(reg, "sportclub", route.Route{Method: http.MethodGet, Path: "/api/v1/public/sport-club/tickets/{token}/qr.png", Summary: "QR of an entry ticket shared with a guest (PNG)",
		RawContent: "image/png", Query: []route.Param{{Name: "propertyId", Required: true}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			pid, err := uuid.Parse(r.URL.Query().Get("propertyId"))
			if err != nil {
				httpx.WriteError(w, r, errs.BadRequest("property_required", "propertyId is required"))
				return
			}
			token := chi.URLParam(r, "token")
			ctx := crm.PublicCtx(r.Context(), pid)
			err = m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
				var n int
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM sportclub.entries WHERE property_id = $1 AND qr_token = $2`, pid, token).Scan(&n); err != nil {
					return err
				}
				if len(token) < 16 || n == 0 {
					return errNotFound("ticket")
				}
				return nil
			})
			var img []byte
			if err == nil {
				img, err = qrPNG(token, 320)
			}
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			_, _ = w.Write(img) //nolint:gosec // G705: a PNG generated by the server (QR of the token), never HTML
		}})
}
