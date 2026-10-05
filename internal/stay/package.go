package stay

// Fulfilment of commercial packages in Stay & Venue (PRD P3 FR-PKG-04/06/08,
// §9.2). The reservation allocator of the package engine books the units
// in the Reservation Engine; commercial.package_booked then becomes stays
// here — one per bungalow / VIP suite / meeting room of the reservation —
// so package guests appear on the Stay Front Desk, are checked in with
// their identity and get an incidentals folio (the room is paid in the
// package folio). Checking the stay in checks the reservation in, which
// consumes the package component (reservation.checked_in, wired by
// internal/app). commercial.package_cancelled cancels the stays not yet
// checked in. Events are decoded by name.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/billing"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/platform/outbox"
)

// Package events consumed (docs/p3-p4-contracts.md, commercial).
const (
	PackageBooked    = "commercial.package_booked"
	PackageCancelled = "commercial.package_cancelled"
)

// stayPackageBooked is the part of commercial.package_booked stay needs.
type stayPackageBooked struct {
	BookingID          uuid.UUID  `json:"bookingId"`
	Number             string     `json:"number"`
	PackageCode        string     `json:"packageCode"`
	CustomerID         *uuid.UUID `json:"customerId"`
	CorporateAccountID *uuid.UUID `json:"corporateAccountId"`
	Pax                int        `json:"pax"`
	Channel            string     `json:"channel"`
	Components         []struct {
		BookingComponentID uuid.UUID  `json:"bookingComponentId"`
		ComponentType      string     `json:"componentType"`
		Name               string     `json:"name"`
		AllocationID       *uuid.UUID `json:"allocationId"`
	} `json:"components"`
}

// OnPackageBooked creates the stays of the reservation components of a
// package booking whose resources are stay units (idempotent per
// component and unit).
func (m *Module) OnPackageBooked(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	var p stayPackageBooked
	if err := ev.Decode(&p); err != nil || ev.PropertyID == nil || p.BookingID == uuid.Nil {
		return nil //nolint:nilerr // foreign payload
	}
	property := *ev.PropertyID
	ctx = reqctx.WithProperty(dbtx.System(ctx), property)
	for _, c := range p.Components {
		if c.ComponentType != "reservation" || c.AllocationID == nil {
			continue
		}
		r, err := m.Res.Get(ctx, tx, *c.AllocationID, false)
		if errs.Is(err, errs.KindNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		for _, l := range r.Lines {
			var kind string
			var uid uuid.UUID
			var typeID *uuid.UUID
			err := tx.QueryRow(ctx, `SELECT kind, id, type_id FROM (
				SELECT 'bungalow' AS kind, id, type_id, resource_id FROM stay.bungalows
				UNION ALL SELECT 'vip_suite', id, NULL::uuid, resource_id FROM stay.vip_suites
				UNION ALL SELECT 'meeting_room', id, NULL::uuid, resource_id FROM stay.meeting_rooms) u WHERE resource_id = $1 LIMIT 1`, l.ResourceID).
				Scan(&kind, &uid, &typeID)
			if dbtx.IsNoRows(err) {
				continue // not a stay unit (venue, golf cart, other resource type)
			}
			if err != nil {
				return err
			}
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM stay.stays WHERE package_component_id = $1 AND unit_id = $2)`, c.BookingComponentID, uid).
				Scan(&exists); err != nil {
				return err
			}
			if exists {
				continue
			}
			if err := m.packageStay(ctx, tx, property, p, c.BookingComponentID, c.Name, r.ID, kind, uid, typeID, l.Start, l.End); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Module) packageStay(ctx context.Context, tx pgx.Tx, property uuid.UUID, p stayPackageBooked, component uuid.UUID, name string, rid uuid.UUID,
	kind string, uid uuid.UUID, typeID *uuid.UUID, start, end time.Time) error {
	guest, email, phone := "", "", ""
	var err error
	if p.CustomerID != nil {
		if guest, email, phone, err = crm.Contact(ctx, tx, p.CustomerID, nil); err != nil {
			return err
		}
	}
	corporate := ""
	if p.CorporateAccountID != nil {
		if corporate, err = crm.CorporateName(ctx, tx, *p.CorporateAccountID); err != nil {
			return err
		}
	}
	if guest == "" {
		guest = nonEmpty(corporate, "Package "+p.Number)
	}
	// incidentals folio (restaurant, minibar …); the room is in the package folio
	f, err := m.Billing.OpenLineFolio(ctx, tx, billing.LineFolioInput{FolioInput: billing.FolioInput{Property: property, CustomerID: p.CustomerID,
		HolderName: guest, SourceType: "reservation", SourceID: &rid}, BusinessLine: billing.LineStay, ReservationID: &rid, CorporateName: corporate})
	if err != nil {
		return err
	}
	no, err := numbering.Next(ctx, tx, property, "STY", clock.Now().In(calendar.Location(ctx, tx)))
	if err != nil {
		return err
	}
	sid := id.New()
	channel := p.Channel
	if channel != "website" && channel != "member_app" && channel != "ops" {
		channel = "back_office"
	}
	adults := 1
	if kind == "bungalow" && p.Pax > 1 {
		adults = p.Pax
	}
	var pax *int
	if kind == "meeting_room" && p.Pax > 0 {
		pax = &p.Pax
	}
	if _, err := tx.Exec(ctx, `INSERT INTO stay.stays (id, property_id, stay_no, kind, reservation_id, customer_id, guest_name, guest_phone, guest_email,
		corporate_name, unit_id, unit_type_id, unit_assigned, start_at, end_at, adults, pax, special_requests, status, folio_id, channel,
		package_booking_id, package_component_id, room_posting)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,true,$13,$14,$15,$16,$17,'reserved',$18,$19,$20,$21,'package')`,
		sid, property, no, kind, rid, p.CustomerID, guest, nzs(phone), nzs(email), nzs(corporate), uid, typeID, start, end, adults, pax,
		"Package "+p.Number+" · "+name, f.ID, channel, p.BookingID, component); err != nil {
		return err
	}
	s, err := m.Get(ctx, tx, sid)
	if err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: audit.ActionCreate, EntityType: "stay.stay", EntityID: sid.String(),
		EntityLabel: no + " · " + s.UnitName, PropertyID: &property, ActorName: "event:" + PackageBooked, After: s,
		Metadata: map[string]any{"packageBooking": p.Number, "packageComponentId": component}})
}

// OnPackageCancelled cancels the stays of a cancelled package that were
// not checked in (the reservation was released with the component).
func (m *Module) OnPackageCancelled(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	var p struct {
		BookingID uuid.UUID `json:"bookingId"`
		Number    string    `json:"number"`
		Reason    string    `json:"reason"`
	}
	if err := ev.Decode(&p); err != nil || ev.PropertyID == nil || p.BookingID == uuid.Nil {
		return nil //nolint:nilerr // foreign payload
	}
	ctx = reqctx.WithProperty(dbtx.System(ctx), *ev.PropertyID)
	rows, err := tx.Query(ctx, `SELECT id FROM stay.stays WHERE package_booking_id = $1 AND status IN ('requested', 'reserved') ORDER BY start_at`, p.BookingID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	for _, sid := range ids {
		s, err := m.lock(ctx, tx, sid)
		if err != nil {
			return err
		}
		if _, err := m.setStatus(ctx, tx, s, "cancelled", ""); err != nil {
			return err
		}
		if s.FolioID != nil {
			if fo, err := billing.GetFolio(ctx, tx, *s.FolioID); err != nil {
				return err
			} else if fo.Status == "open" && len(fo.Lines) == 0 && len(fo.Payments) == 0 {
				if err := m.Billing.CloseFolio(ctx, tx, fo.ID); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// errPackageStay refuses stay-side cancellation of a package stay: the
// package policy applies (FR-PKG-08).
func errPackageStay() error {
	return errs.Conflict("package_booking", "this stay belongs to a package; change or cancel it on the package booking")
}
