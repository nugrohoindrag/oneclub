package app

// PRD P3 EP-10–11 Pricing & Promotion Engine and Package Management (owner:
// commercial). Commercial sits below the business lines (Technical Doc
// §4.2), so the composition root plugs in the component allocators of the
// package engine (Reservation Engine resources, golf tee time seats,
// vouchers), the events that consume package components in their line
// (check-in, voucher redemption), the quotation conversion of package
// quotations (FR-QUO-06), the personal promo codes of CRM campaigns
// (FR-CMP-05) and the package / promotion rows of the accounting export
// (FR-INT-P3-05).

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/commercial/voucher"
	"oneclub/internal/golf"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/storage"
	"oneclub/internal/reservation"
)

// p3Commercial holds the services of the area.
type p3Commercial struct {
	Module *commercial.P3Module
}

// p3CommercialContributions are the catalogue contributions (permissions, role mappings).
func p3CommercialContributions() []catalog.Contribution {
	return []catalog.Contribution{commercial.P3Contribution()}
}

// p3CommercialDocumentTypes are the approval document types.
func p3CommercialDocumentTypes() []provision.DocumentType { return commercial.P3DocumentTypes }

// p3CommercialTemplates are the notification templates (FR-INT-P3-06).
func p3CommercialTemplates() []provision.Template { return commercial.P3Templates() }

// buildP3Commercial wires routes, hooks, approval decisions and jobs.
func (a *App) buildP3Commercial(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
	m := &commercial.P3Module{DB: db, Billing: a.Billing, Events: a.Bus, Approvals: a.Approvals, Notify: a.Notification,
		WebsiteURL: func() string { return cfg.WebsiteURL }}
	m.Register(reg, a.Engine)
	a.Approvals.RegisterDocumentType(commercial.PromotionActivationType, m.PromotionDecision)
	m.RegisterJobs(a.Registrar)
	commercial.RegisterComponentAllocator("reservation", commercial.ComponentAllocator{Allocate: a.allocateResource, Release: a.releaseResource})
	commercial.RegisterComponentAllocator("tee_time", commercial.ComponentAllocator{Allocate: allocateTeeTime, Release: releaseTeeTime})
	commercial.RegisterComponentAllocator("voucher", commercial.ComponentAllocator{Allocate: a.allocateVouchers, Release: a.releaseVouchers})
	a.Billing.RegisterExportSection("commercial.package_promotion", commercial.ExportRows)
	a.Promo.Module = m
	a.convertsQuotations("commercial", "package")
}

// subscribeP3Commercial registers the event subscribers: online payments
// confirm website / app package bookings, closed folios redeem booking
// promotions (K3), accepted package quotations book the package, CRM
// campaigns register personal promo codes, and the business lines consume
// package components (reservation check-in, voucher redemption).
func (a *App) subscribeP3Commercial() {
	m := a.Promo.Module
	a.Bus.Subscribe(billing.EventPaymentSettled, "commercial.package_payment", m.OnPaymentSettled)
	a.Bus.Subscribe(billing.EventFolioClosed, "commercial.promotion_folio_closed", commercial.OnFolioClosed(a.Bus))
	a.Bus.Subscribe("crm.quotation_accepted", "commercial.package_quotation", m.OnQuotationAccepted)
	a.Bus.Subscribe("crm.campaign_sent", "commercial.campaign_promo_codes", m.OnCampaignSent)
	a.Bus.Subscribe("reservation.checked_in", "commercial.package_reservation_checked_in", func(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
		var p struct {
			ReservationID uuid.UUID `json:"reservationId"`
			Code          string    `json:"code"`
		}
		if err := e.Decode(&p); err != nil || p.ReservationID == uuid.Nil || e.PropertyID == nil {
			return nil //nolint:nilerr // foreign payload
		}
		return m.ConsumeAllocation(reqctx.WithProperty(dbtx.System(ctx), *e.PropertyID), tx, p.ReservationID, "check-in "+p.Code)
	})
	a.Bus.Subscribe("commercial.voucher_redeemed", "commercial.package_voucher_redeemed", func(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
		var p struct {
			VoucherID uuid.UUID `json:"voucherId"`
			Code      string    `json:"code"`
		}
		if err := e.Decode(&p); err != nil || p.VoucherID == uuid.Nil || e.PropertyID == nil {
			return nil //nolint:nilerr // foreign payload
		}
		return m.ConsumeAllocation(reqctx.WithProperty(dbtx.System(ctx), *e.PropertyID), tx, p.VoucherID, "voucher "+p.Code)
	})
	// Golf and Stay & Venue fulfil the tee time and stay components (golf
	// bookings with players, stays on the Stay Front Desk) and cancel them
	// with the package (FR-PKG-04/08, §9.2); each golfer checked in uses one
	// place of the tee time component (FR-PKG-06). The stay check-in checks
	// the reservation in, consumed above.
	a.Bus.Subscribe(commercial.EventPackageBooked, "golf.package_bookings", a.Golf.OnPackageBooked)
	a.Bus.Subscribe(commercial.EventPackageCancelled, "golf.package_cancelled", a.Golf.OnPackageCancelled)
	a.Bus.Subscribe(commercial.EventPackageBooked, "stay.package_stays", a.Stay.OnPackageBooked)
	a.Bus.Subscribe(commercial.EventPackageCancelled, "stay.package_cancelled", a.Stay.OnPackageCancelled)
	a.Bus.Subscribe(golf.EventPlayerCheckedIn, "commercial.package_golfer_checked_in", a.consumePackageGolfer)
}

// consumePackageGolfer uses one place of the tee time component of a
// package golf booking when a player checks in (idempotent per player).
func (a *App) consumePackageGolfer(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		PlayerID           uuid.UUID  `json:"playerId"`
		Code               string     `json:"code"`
		PackageComponentID *uuid.UUID `json:"packageComponentId"`
	}
	if err := e.Decode(&p); err != nil || p.PackageComponentID == nil || e.PropertyID == nil {
		return nil //nolint:nilerr // foreign payload or not a package booking
	}
	ctx = reqctx.WithProperty(dbtx.System(ctx), *e.PropertyID)
	var bid uuid.UUID
	var status, bstatus, left string
	err := tx.QueryRow(ctx, `SELECT c.booking_id, c.status, b.status, (c.quantity - c.consumed_quantity)::text FROM commercial.package_booking_components c
		JOIN commercial.package_bookings b ON b.id = c.booking_id WHERE c.id = $1`, *p.PackageComponentID).Scan(&bid, &status, &bstatus, &left)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	rest, _ := decimal.NewFromString(left)
	if status != "unused" || bstatus != "confirmed" || !rest.IsPositive() {
		return nil
	}
	_, err = a.Promo.Module.Consume(ctx, tx, bid, commercial.ConsumeInput{BookingComponentID: *p.PackageComponentID,
		Quantity: decimal.Min(rest, decimal.NewFromInt(1)).String(), Reference: "golf check-in " + p.Code}, "golf-player-"+p.PlayerID.String())
	return err
}

// demoP3Commercial seeds demo data for the area on the MAIN property
// (idempotent): a draft happy hour, a promo code promotion and a draft Golf
// & Lunch package with its components.
func demoP3Commercial(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	return commercial.SeedDemo(ctx, tx, property)
}

// ── component allocators (FR-PKG-04) ──────────────────────────────────────

// resourceChannel maps a package channel to a Reservation Engine channel.
func resourceChannel(c string) string {
	if slices.Contains(reservation.Channels, c) {
		return c
	}
	return "back_office"
}

// allocateResource books Reservation Engine resources for a component:
// the specific resource, else free resources of the resource type (one per
// unit for exclusive types, the places of a capacity type), all in one
// confirmed reservation sourced from the package booking. Night resources
// (bungalow, VIP suite) use their check-in and check-out times.
func (a *App) allocateResource(ctx context.Context, tx pgx.Tx, r commercial.AllocationRequest) (commercial.Allocation, error) {
	eng := a.Reservations
	typeCode := r.ResourceTypeCode
	if r.ResourceID != nil {
		res, err := eng.Resource(ctx, tx, *r.ResourceID)
		if err != nil {
			return commercial.Allocation{}, err
		}
		typeCode = res.ResourceType
	}
	rt, err := eng.Type(ctx, tx, typeCode)
	if err != nil {
		return commercial.Allocation{}, errs.Conflict("resource_type_unknown", "resource type "+typeCode+" is not configured")
	}
	loc := r.Start.Location()
	start, end := r.Start, r.End
	if rt.ReservationModel == "night" {
		ci, co := "14:00", "12:00"
		if rt.CheckInTime != nil {
			ci = *rt.CheckInTime
		}
		if rt.CheckOutTime != nil {
			co = *rt.CheckOutTime
		}
		nights := max(r.Nights, 1)
		start = clockOnDay(r.ServiceDate, ci, loc)
		end = clockOnDay(r.ServiceDate.AddDate(0, 0, nights), co, loc)
	}
	units := int(r.Quantity.Ceil().IntPart())
	if units < 1 {
		units = 1
	}
	var lines []reservation.LineRequest
	desc := r.PackageCode + " · " + r.ComponentName
	switch {
	case r.ResourceID != nil:
		q := 1
		if rt.AllocationMode != "exclusive" {
			q = units
		}
		lines = append(lines, reservation.LineRequest{ResourceID: *r.ResourceID, Start: start, End: end, Quantity: q, Description: desc})
	case rt.AllocationMode != "exclusive":
		var rid uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM reservation.resources WHERE property_id = $1 AND resource_type = $2 AND status = 'active'
			AND archived_at IS NULL ORDER BY name, id LIMIT 1`, r.Property, rt.Code).Scan(&rid); err != nil {
			if dbtx.IsNoRows(err) {
				return commercial.Allocation{}, errs.Conflict("no_resource", "no "+rt.Name+" is configured")
			}
			return commercial.Allocation{}, err
		}
		lines = append(lines, reservation.LineRequest{ResourceID: rid, Start: start, End: end, Quantity: units, Description: desc})
	default:
		rows, err := tx.Query(ctx, `SELECT id FROM reservation.resources WHERE property_id = $1 AND resource_type = $2 AND status = 'active'
			AND archived_at IS NULL ORDER BY name, id`, r.Property, rt.Code)
		if err != nil {
			return commercial.Allocation{}, err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return commercial.Allocation{}, err
		}
		bStart := start.Add(-time.Duration(rt.BufferBefore) * time.Minute)
		bEnd := end.Add(time.Duration(rt.BufferAfter) * time.Minute)
		busy, err := reservation.Busy(ctx, tx, ids, bStart, bEnd)
		if err != nil {
			return commercial.Allocation{}, err
		}
		for _, id := range ids {
			if len(lines) == units {
				break
			}
			if !busy[id] {
				lines = append(lines, reservation.LineRequest{ResourceID: id, Start: start, End: end, Quantity: 1, Description: desc})
			}
		}
		if len(lines) < units {
			return commercial.Allocation{}, errs.Conflict("resource_unavailable",
				fmt.Sprintf("only %d of %d %s free from %s", len(lines), units, rt.Name, start.In(loc).Format("2 Jan 15:04")))
		}
	}
	src := r.BookingID
	res, err := eng.Book(ctx, tx, r.Property, reservation.BookRequest{Lines: lines, CustomerID: r.CustomerID, GuestName: r.GuestName,
		GuestPhone: r.GuestPhone, GuestEmail: r.GuestEmail, Channel: resourceChannel(r.Channel), SourceType: "package_booking", SourceID: &src,
		Confirm: true, Notes: "Package " + r.BookingNumber + " · " + r.ComponentName,
		Attributes: map[string]any{"packageBookingComponentId": r.BookingComponentID.String()}})
	if err != nil {
		return commercial.Allocation{}, err
	}
	out := commercial.Allocation{Ref: res.Code, ID: &res.ID, Start: res.Start, End: res.End,
		Details: map[string]any{"reservationId": res.ID.String(), "lines": len(res.Lines)}}
	if len(res.Lines) > 0 {
		out.ResourceID = &res.Lines[0].ResourceID
		names := []string{}
		for _, l := range res.Lines {
			names = append(names, l.ResourceName)
		}
		out.Details["resources"] = names
	}
	return out, nil
}

func clockOnDay(day time.Time, hhmm string, loc *time.Location) time.Time {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		t, _ = time.Parse("15:04", "12:00")
	}
	return time.Date(day.Year(), day.Month(), day.Day(), t.Hour(), t.Minute(), 0, 0, loc)
}

// releaseResource cancels the reservation of a component (fee waived: the
// package cancellation policy applies).
func (a *App) releaseResource(ctx context.Context, tx pgx.Tx, r commercial.ReleaseRequest) error {
	if r.AllocationID == nil {
		return nil
	}
	res, err := a.Reservations.Get(ctx, tx, *r.AllocationID, false)
	if err != nil {
		if errs.Is(err, errs.KindNotFound) {
			return nil
		}
		return err
	}
	if !slices.Contains([]string{reservation.StatusDraft, reservation.StatusPending, reservation.StatusConfirmed}, res.Status) {
		return nil
	}
	_, err = a.Reservations.Cancel(ctx, tx, res.ID, "package: "+r.Reason, true)
	return err
}

// teeSlot is a tee time candidate of a tee time component.
type teeSlot struct {
	ID         uuid.UUID
	CourseCode string
	VenueID    uuid.UUID
	StartAt    time.Time
	Tee        int
	Flights    int
	MaxPlayers int
	Interval   int
}

// allocateTeeTime seats the players of a tee time component on the first
// open tee times of the service day from the component start (within its
// search window), on the component's course when one is given: the golf
// seats of the Reservation Engine are confirmed for the booking component,
// so the tee time can no longer be oversold; golf books the players from
// commercial.package_booked (contract).
func allocateTeeTime(ctx context.Context, tx pgx.Tx, r commercial.AllocationRequest) (commercial.Allocation, error) {
	players := int(r.Quantity.Ceil().IntPart())
	if players < 1 {
		players = 1
	}
	windowEnd := r.End
	if !windowEnd.After(r.Start) {
		windowEnd = r.Start.Add(2 * time.Hour)
	}
	rows, err := tx.Query(ctx, `SELECT t.id, c.code, c.venue_id, t.start_at, t.start_tee, t.flights, t.max_players, t.interval_minutes
		FROM golf.tee_times t JOIN golf.courses c ON c.id = t.course_id LEFT JOIN golf.course_status cs ON cs.course_id = t.course_id
		WHERE t.property_id = $1 AND t.play_date = $2::date AND t.status = 'open' AND NOT t.member_only AND coalesce(cs.course_state, 'open') <> 'closed'
		AND t.start_at >= $3 AND t.start_at < $4 AND ($5 = '' OR c.code = $5) ORDER BY t.start_at, t.start_tee`,
		r.Property, r.ServiceDate.Format("2006-01-02"), r.Start, windowEnd, strings.ToUpper(r.RefCode))
	if err != nil {
		return commercial.Allocation{}, err
	}
	var slots []teeSlot
	for rows.Next() {
		var s teeSlot
		if err := rows.Scan(&s.ID, &s.CourseCode, &s.VenueID, &s.StartAt, &s.Tee, &s.Flights, &s.MaxPlayers, &s.Interval); err != nil {
			rows.Close()
			return commercial.Allocation{}, err
		}
		slots = append(slots, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return commercial.Allocation{}, err
	}
	loc := r.Start.Location()
	if len(slots) == 0 {
		return commercial.Allocation{}, errs.Conflict("tee_time_unavailable", "no tee time is offered on "+r.ServiceDate.Format("2006-01-02")+
			" from "+r.Start.In(loc).Format("15:04"))
	}
	seated := 0
	var refs, teeTimes []string
	var first *uuid.UUID
	var firstStart *time.Time
	for _, s := range slots {
		if seated == players {
			break
		}
		var seats []uuid.UUID
		for f := 1; f <= s.Flights; f++ {
			for seat := 1; seat <= s.MaxPlayers; seat++ {
				vid := s.VenueID
				rid, err := reservation.EnsureResource(ctx, tx, r.Property, golf.SeatCode(s.CourseCode, s.Tee, f, seat),
					fmt.Sprintf("%s tee %d flight %d seat %d", s.CourseCode, s.Tee, f, seat), "golf_seat", &vid,
					map[string]any{"courseCode": s.CourseCode, "tee": s.Tee, "flight": f, "seat": seat})
				if err != nil {
					return commercial.Allocation{}, err
				}
				seats = append(seats, rid)
			}
		}
		if err := reservation.ReleaseExpiredOn(ctx, tx, seats); err != nil {
			return commercial.Allocation{}, err
		}
		end := s.StartAt.Add(time.Duration(max(s.Interval, 1)) * time.Minute)
		busy, err := reservation.Busy(ctx, tx, seats, s.StartAt, end)
		if err != nil {
			return commercial.Allocation{}, err
		}
		here := 0
		for _, seat := range seats {
			if seated == players {
				break
			}
			if busy[seat] {
				continue
			}
			if _, err := reservation.Confirm(ctx, tx, r.Property, seat, r.BookingComponentID, s.StartAt, end); err != nil {
				if err == reservation.ErrSlotTaken {
					continue
				}
				return commercial.Allocation{}, err
			}
			seated++
			here++
		}
		if here > 0 {
			refs = append(refs, fmt.Sprintf("%s %s ×%d", s.CourseCode, s.StartAt.In(loc).Format("15:04"), here))
			teeTimes = append(teeTimes, s.ID.String())
			if first == nil {
				sid, st := s.ID, s.StartAt
				first, firstStart = &sid, &st
			}
		}
	}
	if seated < players {
		return commercial.Allocation{}, errs.Conflict("tee_time_full", fmt.Sprintf("the tee times from %s have %d free places, %d needed",
			r.Start.In(loc).Format("2 Jan 15:04"), seated, players))
	}
	return commercial.Allocation{Ref: strings.Join(refs, ", "), ID: &r.BookingComponentID, Start: firstStart,
		Details: map[string]any{"teeTimeIds": teeTimes, "players": players, "firstTeeTimeId": first}}, nil
}

// releaseTeeTime frees the seats of a tee time component.
func releaseTeeTime(ctx context.Context, tx pgx.Tx, r commercial.ReleaseRequest) error {
	return reservation.Release(ctx, tx, r.BookingComponentID, "cancelled")
}

// allocateVouchers issues the vouchers of a voucher component to the
// customer (one per unit); their value stays in the package folio as the
// deferred voucher share.
func (a *App) allocateVouchers(ctx context.Context, tx pgx.Tx, r commercial.AllocationRequest) (commercial.Allocation, error) {
	n := int(r.Quantity.Ceil().IntPart())
	if n < 1 {
		n = 1
	}
	if r.DryRun {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM commercial.voucher_types WHERE property_id = $1 AND code = $2 AND status = 'active')`,
			r.Property, r.RefCode).Scan(&ok); err != nil {
			return commercial.Allocation{}, err
		}
		if !ok {
			return commercial.Allocation{}, errs.Conflict("voucher_type_inactive", "voucher type "+r.RefCode+" is not active")
		}
		return commercial.Allocation{Ref: fmt.Sprintf("%d × %s", n, r.RefCode)}, nil
	}
	src := r.BookingID
	var codes, ids []string
	var first *uuid.UUID
	for i := 0; i < n; i++ {
		v, err := a.Vouchers.Issue(ctx, tx, voucher.IssueRequest{PropertyID: r.Property, TypeCode: r.RefCode, CustomerID: r.CustomerID, Via: "issue",
			SourceType: "package_booking", SourceID: &src, ExpiresAt: r.ExpiresAt, Notes: "Package " + r.BookingNumber + " · " + r.ComponentName})
		if err != nil {
			return commercial.Allocation{}, err
		}
		codes, ids = append(codes, v.Code), append(ids, v.ID.String())
		if first == nil {
			vid := v.ID
			first = &vid
		}
	}
	return commercial.Allocation{Ref: strings.Join(codes, ", "), ID: first, Details: map[string]any{"voucherIds": ids, "codes": codes}}, nil
}

// releaseVouchers voids the unused vouchers of a component.
func (a *App) releaseVouchers(ctx context.Context, tx pgx.Tx, r commercial.ReleaseRequest) error {
	raw, _ := r.Details["voucherIds"].([]any)
	for _, x := range raw {
		vid, err := uuid.Parse(fmt.Sprint(x))
		if err != nil {
			continue
		}
		v, err := a.Vouchers.Voucher(ctx, tx, vid)
		if err != nil {
			return err
		}
		if v.Status != "active" {
			continue
		}
		if _, err := a.Vouchers.Void(ctx, tx, vid, "package: "+r.Reason); err != nil {
			return err
		}
	}
	return nil
}
