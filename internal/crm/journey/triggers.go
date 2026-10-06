package journey

// Domain events into journeys: exit rules (e.g. the renewal journey stops
// once the membership is renewed), goals (conversion with the attributed
// revenue) and event triggers (welcome on membership.activated, post-event
// follow-up on banquet.event_completed …). The customer is resolved from
// the payload (customerId, membershipId, eventId, paymentId, bookingId).

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/outbox"
)

type eventFacts struct {
	Customer *uuid.UUID
	Amount   decimal.Decimal
	Ref      string
	Context  map[string]any
}

// resolve finds the customer of an event payload.
func resolve(ctx context.Context, tx pgx.Tx, property uuid.UUID, e outbox.Event) (eventFacts, error) {
	var p struct {
		CustomerID   *uuid.UUID `json:"customerId"`
		MembershipID *uuid.UUID `json:"membershipId"`
		EventID      *uuid.UUID `json:"eventId"`
		PaymentID    *uuid.UUID `json:"paymentId"`
		BookingID    *uuid.UUID `json:"bookingId"`
		Number       string     `json:"number"`
		Code         string     `json:"code"`
		Amount       string     `json:"amount"`
		EndsOn       string     `json:"endsOn"`
	}
	f := eventFacts{Context: map[string]any{"event": e.Type}}
	if err := e.Decode(&p); err != nil {
		return f, nil //nolint:nilerr // foreign payload: no customer
	}
	f.Ref = p.Number
	if f.Ref == "" {
		f.Ref = p.Code
	}
	if d, err := decimal.NewFromString(p.Amount); err == nil {
		f.Amount = d
	}
	one := func(sql string, arg uuid.UUID) error {
		var c *uuid.UUID
		err := tx.QueryRow(ctx, sql, arg, property).Scan(&c)
		if dbtx.IsNoRows(err) {
			return nil
		}
		f.Customer = c
		return err
	}
	var err error
	switch {
	case p.CustomerID != nil:
		f.Customer = p.CustomerID
	case p.MembershipID != nil:
		err = one(`SELECT customer_id FROM reporting.membership_lifecycle WHERE membership_id = $1 AND property_id = $2`, *p.MembershipID)
	case p.PaymentID != nil:
		err = one(`SELECT customer_id FROM reporting.eng_payments WHERE payment_id = $1 AND property_id = $2`, *p.PaymentID)
	case p.EventID != nil:
		err = one(`SELECT customer_id FROM reporting.crm_banquet_event_customers WHERE event_id = $1 AND property_id = $2`, *p.EventID)
	case p.BookingID != nil:
		err = one(`SELECT customer_id FROM reporting.golf_bookings WHERE booking_id = $1 AND property_id = $2`, *p.BookingID)
	}
	if p.MembershipID != nil {
		f.Context["membershipId"] = p.MembershipID.String()
	}
	if p.EventID != nil {
		f.Context["eventId"] = p.EventID.String()
	}
	if p.BookingID != nil {
		f.Context["bookingId"] = p.BookingID.String()
	}
	if p.EndsOn != "" {
		f.Context["endsOn"] = p.EndsOn
	}
	if f.Ref != "" {
		f.Context["reference"] = f.Ref
	}
	return f, err
}

// OnEvent applies the exit rules, goals and event triggers of the journeys
// of the event's property (outbox subscriber for TriggerEvents).
func (s *Service) OnEvent(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	if e.PropertyID == nil {
		return nil
	}
	property := *e.PropertyID
	var used bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.journeys WHERE property_id = $1 AND archived_at IS NULL
		AND (trigger_event = $2 OR $2 = ANY(exit_events) OR goal_event = $2))`, property, e.Type).Scan(&used); err != nil || !used {
		return err
	}
	f, err := resolve(ctx, tx, property, e)
	if err != nil || f.Customer == nil {
		return err
	}
	customer := *f.Customer
	at := e.OccurredAt
	if at.IsZero() {
		at = now()
	}
	// Goals first: the event that ends a journey is also its conversion.
	if _, err := tx.Exec(ctx, `UPDATE crm.journey_enrollments en SET converted_at = $4, conversion_ref = $5, revenue = $6::numeric
		FROM crm.journeys j WHERE j.id = en.journey_id AND j.property_id = $1 AND j.goal_event = $2 AND en.customer_id = $3 AND en.converted_at IS NULL
		AND en.entered_at <= $4 AND en.entered_at >= $4::timestamptz - make_interval(days => j.goal_days)`, property, e.Type, customer, at,
		e.Type+" "+f.Ref, f.Amount.String()); err != nil {
		return err
	}
	// Exit rules.
	type open struct {
		ID         uuid.UUID  `db:"id"`
		JourneyID  uuid.UUID  `db:"journey_id"`
		CurrentKey *string    `db:"current_key"`
		Cohort     string     `db:"cohort"`
		Anchor     *time.Time `db:"anchor_date"`
		EnteredAt  time.Time  `db:"entered_at"`
	}
	exits, err := handle.List[open](tx.Query(ctx, `SELECT en.id, en.journey_id, en.current_key, en.cohort, en.anchor_date, en.entered_at
		FROM crm.journey_enrollments en JOIN crm.journeys j ON j.id = en.journey_id WHERE j.property_id = $1 AND $2 = ANY(j.exit_events)
		AND en.customer_id = $3 AND en.status = 'active' ORDER BY en.id FOR UPDATE OF en`, property, e.Type, customer))
	if err != nil {
		return err
	}
	for _, x := range exits {
		j, err := Get(ctx, tx, property, x.JourneyID, false)
		if err != nil {
			return err
		}
		en := enrollment{ID: x.ID, PropertyID: property, JourneyID: x.JourneyID, CustomerID: customer, Cohort: x.Cohort, CurrentKey: x.CurrentKey,
			AnchorDate: x.Anchor, EnteredAt: x.EnteredAt}
		if _, err := s.logEvent(ctx, tx, j, en, JourneyStep{Key: TargetExit, StepType: "exit"}, "exited", eventExtra{Details: map[string]any{"event": e.Type,
			"reference": f.Ref}}); err != nil {
			return err
		}
		if err := s.finish(ctx, tx, en, "exited", "event "+e.Type); err != nil {
			return err
		}
	}
	// Event triggers.
	rows, err := tx.Query(ctx, `SELECT id FROM crm.journeys WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL
		AND trigger_type = 'event' AND trigger_event = $2 ORDER BY code`, property, e.Type)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	occurrence := "ev:" + e.ID.String()
	if e.AggregateID != nil {
		occurrence = "ev:" + e.Type + ":" + e.AggregateID.String()
	}
	for _, jid := range ids {
		j, err := Get(ctx, tx, property, jid, false)
		if err != nil {
			return err
		}
		if _, _, err := s.enroll(ctx, tx, j, EnrollRequest{Customer: customer, Occurrence: occurrence, Context: f.Context}); err != nil {
			return err
		}
	}
	return nil
}
