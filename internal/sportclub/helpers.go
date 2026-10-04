package sportclub

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/rules"
)

type uuidT = uuid.UUID

func parseUUID(s string) (uuid.UUID, error) { return uuid.Parse(s) }

func mustUUID(v any) uuid.UUID {
	u, _ := uuid.Parse(str(v))
	return u
}

func actor(ctx context.Context) *uuid.UUID {
	if p := authz.From(ctx); p != nil {
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

// localNow returns now in the instance timezone.
func localNow(ctx context.Context, q dbtx.Querier) time.Time {
	return clock.Now().In(calendar.Location(ctx, q))
}

// dayKind returns weekday | weekend | holiday for a local date.
func dayKind(ctx context.Context, q dbtx.Querier, property uuid.UUID, d time.Time) (string, error) {
	h, err := calendar.IsHoliday(ctx, q, property, d)
	if err != nil {
		return "", err
	}
	if h {
		return "holiday", nil
	}
	if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		return "weekend", nil
	}
	return "weekday", nil
}

func (m *Module) policy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (Policy, rules.PolicyRef, error) {
	return rules.PolicyAt(ctx, q, "sportclub.policy", property, defaultPolicy)
}

func auditRecord(ctx context.Context, tx pgx.Tx, entity string, eid uuid.UUID, action string, property uuid.UUID, after any) error {
	return audit.Record(ctx, tx, audit.Entry{Module: "sportclub", Action: action, EntityType: entity, EntityID: eid.String(), PropertyID: &property, After: after})
}
