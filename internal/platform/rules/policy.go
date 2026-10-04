package rules

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/kernel/dbtx"
)

// PolicyRef identifies the policy version a booking or transaction was
// decided under (PRD P1 FR-POL-09): version 0 means the code default.
type PolicyRef struct {
	Code    string `json:"code"`
	Version int    `json:"version"`
}

// Policy resolves a club policy into T. Fields missing from the configured
// value keep the defaults in def, so modules always work before a club has
// configured anything and new fields can be added without migrations.
func Policy[T any](ctx context.Context, q dbtx.Querier, code string, property *uuid.UUID, at time.Time, def T) (T, PolicyRef, error) {
	out := def
	raw, version, ok, err := Resolve(ctx, q, "club_policy", code, property, at)
	if err != nil || !ok {
		return out, PolicyRef{Code: code}, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return def, PolicyRef{Code: code}, err
	}
	return out, PolicyRef{Code: code, Version: version}, nil
}

// PolicyAt is Policy for a property id value.
func PolicyAt[T any](ctx context.Context, q dbtx.Querier, code string, property uuid.UUID, def T) (T, PolicyRef, error) {
	p := property
	return Policy(ctx, q, code, &p, time.Now(), def)
}
