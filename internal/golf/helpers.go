package golf

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/realtime"
	"oneclub/internal/platform/rules"
)

func actor(ctx context.Context) *uuid.UUID {
	if p := authz.From(ctx); p != nil && p.UserID != uuid.Nil {
		return id.Ptr(p.UserID)
	}
	return nil
}

func nzs(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func localNow(ctx context.Context, q dbtx.Querier) time.Time {
	return clock.Now().In(calendar.Location(ctx, q))
}

func dayBounds(ctx context.Context, q dbtx.Querier, d time.Time) (time.Time, time.Time) {
	loc := calendar.Location(ctx, q)
	from := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
	return from, from.AddDate(0, 0, 1)
}

func record(ctx context.Context, tx pgx.Tx, entity string, eid uuid.UUID, label, action string, property uuid.UUID, before, after any, reason string) error {
	return audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: action, EntityType: entity, EntityID: eid.String(), EntityLabel: label,
		PropertyID: &property, Before: before, After: after, Reason: reason})
}

func (m *Module) publish(ctx context.Context, tx pgx.Tx, event, aggregate string, aid, property uuid.UUID, payload any) error {
	if m.Events == nil {
		return nil
	}
	_, err := m.Events.Publish(ctx, tx, event, aggregate, &aid, &property, payload)
	return err
}

// live notifies real-time screens (pace of play, golf cart readiness, range).
func (m *Module) live(ctx context.Context, tx pgx.Tx, topic string, property uuid.UUID, kind, eid string, data map[string]any) error {
	if m.Realtime == nil {
		return nil
	}
	return realtime.Notify(ctx, tx, topic, property, kind, eid, data)
}

func (m *Module) caddyPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (CaddyPolicy, error) {
	p, _, err := rules.PolicyAt(ctx, q, "golf.caddy", property, defaultCaddyPolicy)
	return p, err
}

func (m *Module) cartPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (CartPolicy, error) {
	p, _, err := rules.PolicyAt(ctx, q, "golf.cart", property, defaultCartPolicy)
	return p, err
}

func (m *Module) reciprocalPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (ReciprocalPolicy, error) {
	p, _, err := rules.PolicyAt(ctx, q, "golf.reciprocal", property, defaultReciprocalPolicy)
	return p, err
}

func (m *Module) hofPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (HallOfFamePolicy, error) {
	p, _, err := rules.PolicyAt(ctx, q, "golf.hall_of_fame", property, defaultHOFPolicy)
	return p, err
}

func collectIDs(rows pgx.Rows, err error) ([]uuid.UUID, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}

func can(ctx context.Context, perm string, property uuid.UUID) bool {
	p := authz.From(ctx)
	return p != nil && p.Can(perm, &property)
}

func fmtSscan(s string, v *float64) (int, error) { return fmt.Sscan(s, v) }
