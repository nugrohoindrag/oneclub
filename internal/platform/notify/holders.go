package notify

import (
	"context"

	"github.com/google/uuid"

	"oneclub/internal/kernel/dbtx"
)

// Holders returns the active users holding a permission at a property
// (instance-wide assignments included) — the recipients of operational
// alerts such as a low feedback score or a golf cart service threshold.
func Holders(ctx context.Context, q dbtx.Querier, property uuid.UUID, permission string) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT u.id FROM platform.role_assignments ra
		JOIN platform.role_permissions rp ON rp.role_id = ra.role_id AND rp.permission_code = $2
		JOIN platform.users u ON u.id = ra.user_id AND u.status = 'active'
		WHERE ra.property_id IS NULL OR ra.property_id = $1`, property, permission)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var u uuid.UUID
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
