package rules

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
)

// SaveClubPolicy stores the next version of a club policy, in force now,
// for a module screen that edits its own policy (e.g. Sport Club › Booking
// Rules) without the generic Club Policies editor. The value is validated
// like a version added there.
func SaveClubPolicy(ctx context.Context, tx pgx.Tx, property *uuid.UUID, code, name string, value any) (int, error) {
	def, ok := policyDef(code)
	if !ok {
		return 0, errs.Validation("unknown_policy", "unknown club policy "+code)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	if fields := validatePolicy(code, def.Category, raw); len(fields) > 0 {
		return 0, errs.Validation("invalid_policy", "invalid policy", fields...)
	}
	var maxV *int
	if err := tx.QueryRow(ctx, `SELECT max(version) FROM platform.rules WHERE kind = 'club_policy' AND code = $1 AND property_id IS NOT DISTINCT FROM $2`,
		code, property).Scan(&maxV); err != nil {
		return 0, err
	}
	version := 1
	if maxV != nil {
		version = *maxV + 1
	}
	var uid *uuid.UUID
	if p := authz.From(ctx); p != nil {
		uid = id.Ptr(p.UserID)
	}
	rid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO platform.rules (id, kind, category, code, name, description, property_id, version, effective_from, value,
		created_by, updated_by) VALUES ($1,'club_policy',$2,$3,$4,$5,$6,$7,$8,$9,$10,$10)`,
		rid, def.Category, code, name, def.Description, property, version, clock.Now(), raw, uid); err != nil {
		return 0, err
	}
	return version, audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionCreate, EntityType: "platform.club_policy",
		EntityID: rid.String(), EntityLabel: code + " v" + itoa(version), PropertyID: property, After: value})
}
