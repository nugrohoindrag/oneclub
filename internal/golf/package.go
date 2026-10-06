package golf

// Fulfilment of commercial packages in golf (PRD P3 FR-PKG-04/06/08, §9.2).
// The tee time allocator of the package engine confirms golf seats of the
// Reservation Engine for the booking component (owner = component id);
// commercial.package_booked then becomes golf bookings here — one per tee
// time of a tee time component, with its flight and players, so package
// golfers are checked in, sent to the starter and given caddies like any
// other booking. The first player is the package customer, the others are
// nominees "to be announced" completed at the desk (FR-FLT-09). The package
// folio already holds the green fee, so the booking has no folio of its
// own (nothing to pay at check-in). commercial.package_cancelled cancels
// the bookings that were not played. Events are decoded by name.

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/secret"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/outbox"
)

// Package events consumed (docs/p3-p4-contracts.md, commercial).
const (
	PackageBooked    = "commercial.package_booked"
	PackageCancelled = "commercial.package_cancelled"
)

// golfPackageBooked is the part of commercial.package_booked golf needs.
type golfPackageBooked struct {
	BookingID          uuid.UUID  `json:"bookingId"`
	Number             string     `json:"number"`
	PackageCode        string     `json:"packageCode"`
	CustomerID         *uuid.UUID `json:"customerId"`
	CorporateAccountID *uuid.UUID `json:"corporateAccountId"`
	Channel            string     `json:"channel"`
	Components         []struct {
		BookingComponentID uuid.UUID `json:"bookingComponentId"`
		ComponentType      string    `json:"componentType"`
		Name               string    `json:"name"`
	} `json:"components"`
}

// packageSeat is a golf seat confirmed for a package component.
type packageSeat struct {
	allocation uuid.UUID
	teeTime    uuid.UUID
}

// OnPackageBooked creates the golf bookings of the tee time components of
// a package booking (idempotent per component and tee time).
func (m *Module) OnPackageBooked(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	var p golfPackageBooked
	if err := ev.Decode(&p); err != nil || ev.PropertyID == nil || p.BookingID == uuid.Nil {
		return nil //nolint:nilerr // foreign payload
	}
	property := *ev.PropertyID
	ctx = withProperty(dbtx.System(ctx), property)
	for _, c := range p.Components {
		if c.ComponentType != "tee_time" {
			continue
		}
		rows, err := tx.Query(ctx, `SELECT a.id, t.id FROM reservation.allocations a JOIN reservation.resources r ON r.id = a.resource_id
			JOIN golf.courses c ON c.property_id = a.property_id AND c.code = r.attributes->>'courseCode'
			JOIN golf.tee_times t ON t.course_id = c.id AND t.start_at = lower(a.period) AND t.start_tee = (r.attributes->>'tee')::int
			WHERE a.reservation_id = $1 AND a.status = 'confirmed' AND r.resource_type = 'golf_seat' ORDER BY t.start_at, t.start_tee, r.code`,
			c.BookingComponentID)
		if err != nil {
			return err
		}
		var seats []packageSeat
		for rows.Next() {
			var s packageSeat
			if err := rows.Scan(&s.allocation, &s.teeTime); err != nil {
				rows.Close()
				return err
			}
			seats = append(seats, s)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		var order []uuid.UUID
		byTee := map[uuid.UUID][]uuid.UUID{}
		for _, s := range seats {
			if _, ok := byTee[s.teeTime]; !ok {
				order = append(order, s.teeTime)
			}
			byTee[s.teeTime] = append(byTee[s.teeTime], s.allocation)
		}
		for _, tt := range order {
			if err := m.packageBooking(ctx, tx, property, p, c.BookingComponentID, c.Name, tt, byTee[tt]); err != nil {
				return err
			}
		}
	}
	return nil
}

// packageBooking books the players of one tee time of a package component.
func (m *Module) packageBooking(ctx context.Context, tx pgx.Tx, property uuid.UUID, p golfPackageBooked, component uuid.UUID, name string,
	teeTimeID uuid.UUID, allocations []uuid.UUID) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.bookings WHERE package_component_id = $1 AND tee_time_id = $2)`, component, teeTimeID).
		Scan(&exists); err != nil || exists {
		return err
	}
	s, err := lockSlot(ctx, tx, property, teeTimeID)
	if err != nil {
		return err
	}
	pol, err := LoadPolicies(ctx, tx, property, clock.Now())
	if err != nil {
		return err
	}
	contact, email, phone := "", "", ""
	if p.CustomerID != nil {
		if contact, email, phone, err = crm.Contact(ctx, tx, p.CustomerID, nil); err != nil {
			return err
		}
	}
	if contact == "" && p.CorporateAccountID != nil {
		if contact, err = crm.CorporateName(ctx, tx, *p.CorporateAccountID); err != nil {
			return err
		}
	}
	if contact == "" {
		contact = "Package " + p.Number
	}
	bookingType := "non_member"
	if p.CorporateAccountID != nil {
		bookingType = "corporate"
	}
	channel := p.Channel
	if channel != "website" && channel != "member_app" {
		channel = "back_office"
	}
	code, err := m.bookingCode(ctx, tx, property, s.PlayDate)
	if err != nil {
		return err
	}
	bid := id.New()
	note := "Package " + p.Number + " · " + name
	if _, err := tx.Exec(ctx, `INSERT INTO golf.bookings (id, property_id, code, booking_type, channel, status, course_id, tee_time_id, play_date, start_at,
		playing_route_id, player_count, customer_id, corporate_account_id, contact_name, contact_phone, contact_email, payment_mode, qr_token, notes,
		confirmed_at, package_booking_id, package_component_id)
		VALUES ($1,$2,$3,$4,$5,'confirmed',$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,'prepaid',$17,$18,now(),$19,$20)`,
		bid, property, code, bookingType, channel, s.CourseID, s.ID, s.PlayDate.Format("2006-01-02"), s.StartAt, s.RouteID, len(allocations), p.CustomerID,
		p.CorporateAccountID, contact, nullStr(phone), nullStr(email), secret.RandomToken(18), note, p.BookingID, component); err != nil {
		return err
	}
	maxP := max(min(pol.Golf.MaxPlayers, s.MaxP), 1)
	var flight uuid.UUID
	for i, alloc := range allocations {
		if i%maxP == 0 {
			flight = id.New()
			var fno int
			if err := tx.QueryRow(ctx, `SELECT coalesce(max(flight_no), 0) + 1 FROM golf.flights WHERE tee_time_id = $1`, s.ID).Scan(&fno); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO golf.flights (id, property_id, tee_time_id, booking_id, course_id, play_date, flight_no, status)
				VALUES ($1,$2,$3,$4,$5,$6,$7,'confirmed')`, flight, property, s.ID, bid, s.CourseID, s.PlayDate.Format("2006-01-02"), fno); err != nil {
				return err
			}
		}
		player, cust, tba := fmt.Sprintf("%s · player %d", contact, i+1), (*uuid.UUID)(nil), true
		if i == 0 && p.CustomerID != nil {
			player, cust, tba = contact, p.CustomerID, false
		}
		if _, err := tx.Exec(ctx, `INSERT INTO golf.booking_players (id, property_id, booking_id, flight_id, seq, player_type, segment, customer_id, name,
			tba, allocation_id, status) VALUES ($1,$2,$3,$4,$5,'non_member','package',$6,$7,$8,$9,'booked')`, id.New(), property, bid, flight, i+1, cust,
			player, tba, alloc); err != nil {
			return err
		}
	}
	if err := addHistory(ctx, tx, property, bid, "created", nil, map[string]any{"code": code, "package": p.Number, "players": len(allocations)},
		"package booking "+p.Number); err != nil {
		return err
	}
	if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "booking", s.CourseID, s.PlayDate, map[string]any{"bookingId": bid.String(),
		"teeTimeId": s.ID.String()}); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: audit.ActionCreate, EntityType: "golf.booking", EntityID: bid.String(),
		EntityLabel: code, PropertyID: &property, ActorName: "event:" + PackageBooked, After: map[string]any{"code": code, "status": "confirmed",
			"type": bookingType, "players": len(allocations), "startAt": s.StartAt, "packageBooking": p.Number, "packageComponentId": component}})
}

// OnPackageCancelled cancels the golf bookings of a cancelled package that
// were not played: booked players are cancelled (their seats were released
// with the component), checked-in players stay.
func (m *Module) OnPackageCancelled(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	var p struct {
		BookingID uuid.UUID `json:"bookingId"`
		Number    string    `json:"number"`
		Reason    string    `json:"reason"`
	}
	if err := ev.Decode(&p); err != nil || ev.PropertyID == nil || p.BookingID == uuid.Nil {
		return nil //nolint:nilerr // foreign payload
	}
	property := *ev.PropertyID
	ctx = withProperty(dbtx.System(ctx), property)
	rows, err := tx.Query(ctx, `SELECT id FROM golf.bookings WHERE package_booking_id = $1 AND status IN ('pending', 'confirmed', 'checked_in')
		ORDER BY start_at FOR UPDATE`, p.BookingID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	reason := "package " + p.Number + " cancelled"
	if p.Reason != "" {
		reason += ": " + p.Reason
	}
	for _, bid := range ids {
		b, err := GetBooking(ctx, tx, bid)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.booking_players SET status = 'cancelled' WHERE booking_id = $1 AND status = 'booked'`, bid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.flights f SET status = 'cancelled' WHERE f.booking_id = $1 AND f.status = 'confirmed'
			AND NOT EXISTS (SELECT 1 FROM golf.booking_players bp WHERE bp.flight_id = f.id AND bp.status = 'checked_in')`, bid); err != nil {
			return err
		}
		status := b.Status
		if b.Status != "checked_in" {
			status = "cancelled"
			if _, err := tx.Exec(ctx, `UPDATE golf.bookings SET status = 'cancelled', cancelled_at = now(), cancel_reason = $2 WHERE id = $1`, bid,
				reason); err != nil {
				return err
			}
		}
		if err := addHistory(ctx, tx, property, bid, "cancelled", map[string]any{"status": b.Status}, map[string]any{"status": status}, reason); err != nil {
			return err
		}
		if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "booking_cancelled", b.CourseID, mustDay(b.PlayDate),
			map[string]any{"bookingId": bid.String()}); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: audit.ActionStatusChange, EntityType: "golf.booking", EntityID: bid.String(),
			EntityLabel: b.Code, PropertyID: &property, ActorName: "event:" + PackageCancelled, Reason: reason,
			Before: map[string]any{"status": b.Status}, After: map[string]any{"status": status}}); err != nil {
			return err
		}
	}
	return nil
}

// errPackageBooking refuses golf-side changes of a package booking: the
// package policy applies (FR-PKG-08).
func errPackageBooking() error {
	return errs.Conflict("package_booking", "this booking belongs to a package; change or cancel it on the package booking")
}
