package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

func toInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case int32:
		return int(t)
	case float64:
		return int(t)
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(t))
		return n
	}
	return 0
}

func parseDate(s string) (time.Time, error) { return time.Parse("2006-01-02", strings.TrimSpace(s)) }

// optDate parses an optional YYYY-MM-DD input field.
func optDate(field, s string) (*time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	d, err := parseDate(s)
	if err != nil {
		return nil, handle.Invalid(field, "invalid_date", field+" must be a date (YYYY-MM-DD)")
	}
	return &d, nil
}

// qty parses a required decimal quantity input.
func qty(field, v string, positive bool) (decimal.Decimal, error) {
	d, err := handle.Decimal(field, v, decimal.Zero)
	if err != nil {
		return d, err
	}
	if d.IsZero() || (positive && d.IsNegative()) {
		msg := "must not be zero"
		if positive {
			msg = "must be greater than zero"
		}
		return d, handle.Invalid(field, "invalid_quantity", field+" "+msg)
	}
	return d, nil
}

// optCost parses an optional unit cost (≥ 0).
func optCost(field, v string) (*decimal.Decimal, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	d, err := handle.Decimal(field, v, decimal.Zero)
	if err != nil {
		return nil, err
	}
	if d.IsNegative() {
		return nil, handle.Invalid(field, "invalid_cost", field+" must not be negative")
	}
	return &d, nil
}

func record(ctx context.Context, tx pgx.Tx, property uuid.UUID, entity string, eid uuid.UUID, label, action string, before, after any, reason string) error {
	return audit.Record(ctx, tx, audit.Entry{Module: "inventory", Action: action, EntityType: entity, EntityID: eid.String(), EntityLabel: label,
		PropertyID: &property, Before: before, After: after, Reason: reason})
}

func lineField(i int, f string) string { return fmt.Sprintf("lines[%d].%s", i, f) }

// conflictStatus is the standard error of an action in the wrong status.
func conflictStatus(doc, number, status, action string) error {
	return errs.Conflict("invalid_status", fmt.Sprintf("%s %s is %s and cannot be %s", doc, number, strings.ReplaceAll(status, "_", " "), action))
}
