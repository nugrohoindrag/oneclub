package loyalty

// Activity points (FR-LOY-02: "poin aktivitas — round selesai, event,
// referral") next to the round_finished points of earn.go: the member
// referral reward when a referred lead's opportunity is won (FR-LEAD-04,
// crm.opportunity_won with referrerCustomerId, idempotent per opportunity)
// and event attendance. Points come from the active activity earning rules.

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/outbox"
)

// Activities that give points (earning rules of type activity).
var Activities = []string{"round_finished", "referral", "event_attended"}

// activityPoints sums the points of the active rules of an activity.
func activityPoints(ctx context.Context, q dbtx.Querier, property uuid.UUID, activity string) (int64, error) {
	rules, err := activeRules(ctx, q, property, "activity", localToday(ctx, q, property))
	if err != nil {
		return 0, err
	}
	var points int64
	for _, r := range rules {
		if r.Activity != nil && *r.Activity == activity {
			points += r.Points
		}
	}
	return points, nil
}

// AwardActivity gives the points of an activity to the active account of a
// customer, once per key; no account or no rule gives nothing.
func (m *Module) AwardActivity(ctx context.Context, tx pgx.Tx, property, customer uuid.UUID, activity, sourceType string, sourceID *uuid.UUID,
	ref, key, description string) (*LoyaltyEntry, error) {
	points, err := activityPoints(ctx, tx, property, activity)
	if err != nil || points <= 0 {
		return nil, err
	}
	var aid uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM crm.loyalty_accounts WHERE customer_id = $1 AND property_id = $2 AND status = 'active'`, customer, property).Scan(&aid)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a, err := lockAccount(ctx, tx, aid)
	if err != nil {
		return nil, err
	}
	e, created, err := m.post(ctx, tx, &a, posting{Kind: KindEarned, Points: points, SourceType: sourceType, SourceID: sourceID, SourceRef: ref,
		Key: key, Description: description})
	if err != nil || !created {
		return &e, err
	}
	if err := m.notifyCustomer(ctx, tx, property, customer, "crm.loyalty_points_earned", map[string]any{"points": points, "balance": a.Balance,
		"reference": ref}, "in_app"); err != nil {
		return &e, err
	}
	return &e, nil
}

// OpportunityWon is the part of crm.opportunity_won loyalty uses.
type OpportunityWon struct {
	OpportunityID      uuid.UUID  `json:"opportunityId"`
	Number             string     `json:"number"`
	CustomerID         uuid.UUID  `json:"customerId"`
	ReferrerCustomerID *uuid.UUID `json:"referrerCustomerId"`
}

// OnOpportunityWon rewards the member who referred a won deal (once per
// opportunity; the referred customer earns nothing for it).
func (m *Module) OnOpportunityWon(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p OpportunityWon
	if err := e.Decode(&p); err != nil || p.ReferrerCustomerID == nil || p.OpportunityID == uuid.Nil || e.PropertyID == nil {
		return nil //nolint:nilerr // no referral to reward
	}
	if *p.ReferrerCustomerID == p.CustomerID {
		return nil
	}
	oid := p.OpportunityID
	_, err := m.AwardActivity(ctx, tx, *e.PropertyID, *p.ReferrerCustomerID, "referral", "crm.opportunity", &oid, p.Number,
		"referral:"+oid.String(), "Referral reward: deal "+p.Number+" won")
	return err
}

// EventAttended is an attendance of a club event (Event Operations check-in).
type EventAttended struct {
	EventID        uuid.UUID  `json:"eventId"`
	RegistrationID *uuid.UUID `json:"registrationId"`
	CustomerID     *uuid.UUID `json:"customerId"`
	Number         string     `json:"number"`
}

// OnEventAttended gives the event_attended activity points to the guest.
func (m *Module) OnEventAttended(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p EventAttended
	if err := e.Decode(&p); err != nil || p.CustomerID == nil || p.EventID == uuid.Nil || e.PropertyID == nil {
		return nil //nolint:nilerr // not an attendance of a known customer
	}
	src := p.EventID
	if p.RegistrationID != nil {
		src = *p.RegistrationID
	}
	_, err := m.AwardActivity(ctx, tx, *e.PropertyID, *p.CustomerID, "event_attended", "banquet.event", &src, p.Number,
		"event:"+p.EventID.String()+":"+p.CustomerID.String(), "Activity points: event attended")
	return err
}
