package reservation

// Booking confirmation per business line (PRD P2 FR-INT-P2-04): when a
// booking is confirmed the customer receives the confirmation (in-app for
// portal users, else e-mail; WhatsApp when the messaging integration is on).

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/provision"
)

var lineNames = map[string][2]string{
	"sportclub": {"Sport Club", "Sport Club"}, "stay": {"Stay & Venue", "Stay & Venue"}, "golf": {"Golf", "Golf"}, "other": {"Booking", "Booking"},
}

// NotifyConfirmed is the reservation.confirmed subscriber.
func (e *Engine) NotifyConfirmed(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	if e.Notify == nil {
		return nil
	}
	var p struct {
		ReservationID uuid.UUID `json:"reservationId"`
	}
	if err := ev.Decode(&p); err != nil {
		return err
	}
	r, err := e.Get(ctx, tx, p.ReservationID, false)
	if err != nil || r.Kind != "booking" || r.CustomerID == nil {
		return err
	}
	var name string
	var email, phone, locale *string
	var user *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT name, email, phone, locale, user_id FROM crm.customers WHERE id = $1`, *r.CustomerID).Scan(&name, &email, &phone, &locale, &user); err != nil {
		return err
	}
	loc := calendar.Location(ctx, tx)
	start, resource := "", ""
	if r.Start != nil {
		start = r.Start.In(loc).Format("2006-01-02 15:04")
	}
	if len(r.Lines) > 0 {
		resource = r.Lines[0].ResourceName
	}
	ln := lineNames[r.BusinessLine]
	if ln[0] == "" {
		ln = lineNames["other"]
	}
	msg := notify.Message{Event: "reservation.booking_confirmed", Category: "booking", PropertyID: &r.PropertyID, Link: "/bookings",
		Data: map[string]any{"name": name, "code": r.Code, "line": ln[0], "resource": resource, "start": start}}
	switch {
	case user != nil:
		msg.UserIDs = []uuid.UUID{*user}
	case email != nil && *email != "":
		msg.Email, msg.Channels = *email, []string{notify.ChannelEmail}
		if locale != nil {
			msg.Locale = *locale
		}
	default:
		return nil
	}
	return e.Notify.Send(ctx, tx, msg)
}

// Templates are the reservation notification templates.
func Templates() []provision.Template {
	t := map[string][2]string{
		"en": {"Booking confirmed: {{.code}}", "Hello {{.name}},\n\nYour {{.line}} booking {{.code}} ({{.resource}}, {{.start}}) is confirmed. See you soon."},
		"id": {"Booking dikonfirmasi: {{.code}}", "Halo {{.name}},\n\nBooking {{.line}} {{.code}} ({{.resource}}, {{.start}}) telah dikonfirmasi. Sampai jumpa."},
	}
	var out []provision.Template
	for loc, c := range t {
		for _, ch := range []string{"email", "in_app", "whatsapp"} {
			out = append(out, provision.Template{Event: "reservation.booking_confirmed", Channel: ch, Locale: loc, Subject: c[0], Body: c[1]})
		}
	}
	return out
}
