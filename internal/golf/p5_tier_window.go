package golf

// PRD P5 FR-LOY-P5-02: the loyalty tier benefit "booking window + days"
// (Gold +2, Platinum +4; PRD P5 §16 #14) extends the member booking window.
// CRM sits above golf (Technical Doc §4.2), so internal/app wires the
// reader of the tier benefits; without it the windows are unchanged.

import (
	"context"

	"github.com/google/uuid"

	"oneclub/internal/kernel/dbtx"
)

var (
	tierWindowBonus func(ctx context.Context, q dbtx.Querier, customer uuid.UUID) (int, error)
	tierWindowMax   func(ctx context.Context, q dbtx.Querier, property uuid.UUID) (int, error)
)

// SetTierBookingWindow wires the tier benefit readers (internal/app): the
// extra days of a customer and the largest extra days of a property (tee
// sheet generation horizon).
func SetTierBookingWindow(bonus func(ctx context.Context, q dbtx.Querier, customer uuid.UUID) (int, error),
	maxBonus func(ctx context.Context, q dbtx.Querier, property uuid.UUID) (int, error)) {
	tierWindowBonus, tierWindowMax = bonus, maxBonus
}

type tierCustomerKey struct{}

// withTierCustomer marks the customer a Member App tee hold is placed for.
func withTierCustomer(ctx context.Context, customer uuid.UUID) context.Context {
	return context.WithValue(ctx, tierCustomerKey{}, customer)
}

func tierCustomer(ctx context.Context) *uuid.UUID {
	if c, ok := ctx.Value(tierCustomerKey{}).(uuid.UUID); ok && c != uuid.Nil {
		return &c
	}
	return nil
}

// tierBonus is the extra booking window days of a customer (0 without a tier).
func tierBonus(ctx context.Context, q dbtx.Querier, customer *uuid.UUID) int {
	if customer == nil || tierWindowBonus == nil {
		return 0
	}
	n, err := tierWindowBonus(ctx, q, *customer)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// requestTierBonus is the largest bonus of the customers of a booking request.
func requestTierBonus(ctx context.Context, q dbtx.Querier, req BookingRequest) int {
	best := tierBonus(ctx, q, req.CustomerID)
	for _, p := range req.Players {
		best = max(best, tierBonus(ctx, q, p.CustomerID))
	}
	return max(best, tierBonus(ctx, q, tierCustomer(ctx)))
}

// withTierWindow extends the member and guest-of-member windows of the
// policies by the bonus (on a copy).
func withTierWindow(pol Policies, bonus int) Policies {
	if bonus <= 0 {
		return pol
	}
	w := make(map[string]int, len(pol.Golf.BookingWindowDays))
	for k, v := range pol.Golf.BookingWindowDays {
		w[k] = v
		if (k == "member" || k == "guest_of_member") && v > 0 {
			w[k] = v + bonus
		}
	}
	pol.Golf.BookingWindowDays = w
	return pol
}

// tierHorizon is the largest tier bonus of a property.
func tierHorizon(ctx context.Context, q dbtx.Querier, property uuid.UUID) int {
	if tierWindowMax == nil {
		return 0
	}
	n, err := tierWindowMax(ctx, q, property)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
