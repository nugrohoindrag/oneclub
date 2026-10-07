package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/secret"
	"oneclub/internal/platform/iam/password"
)

// DemoPassword is the password of every demo user (dev/staging only).
const DemoPassword = "Demo#Club2026"

// DemoPIN is the staff PIN of operational demo users.
const DemoPIN = "246810"

// DemoUser is a seeded demo account.
type DemoUser struct {
	Email    string
	Name     string
	Role     string
	Property string // property code; "" = instance-wide
}

// DemoUsers cover each shell and the reference flows.
var DemoUsers = []DemoUser{
	{"property.admin@demo.oneclub.id", "Rina Property Admin", "property_admin", "MAIN"},
	{"property.admin2@demo.oneclub.id", "Budi Property Admin", "property_admin", "MDR"},
	{"gm@demo.oneclub.id", "Agus General Manager", "general_manager", "MAIN"},
	{"finance@demo.oneclub.id", "Sari Finance Manager", "finance_manager", "MAIN"},
	{"golf.manager@demo.oneclub.id", "Dewi Golf Manager", "golf_manager", "MAIN"},
	{"starter@demo.oneclub.id", "Joko Starter", "starter_marshal", "MAIN"},
	{"cashier@demo.oneclub.id", "Wati Cashier", "cashier", "MAIN"},
	{"member@demo.oneclub.id", "Hendra Member", "member", "MAIN"},
}

// DemoResult reports seeded identifiers.
type DemoResult struct {
	Properties  map[string]uuid.UUID
	DeviceToken string
	Users       []DemoUser
}

// SeedDemo creates demo data in an existing instance (idempotent).
func SeedDemo(ctx context.Context, db *dbtx.DB) (*DemoResult, error) {
	ctx = dbtx.System(ctx)
	users := append(append([]DemoUser{}, DemoUsers...), P1DemoUsers...)
	res := &DemoResult{Properties: map[string]uuid.UUID{}, Users: users}
	hash, err := password.Hash(DemoPassword)
	if err != nil {
		return nil, err
	}
	pinHash, err := password.Hash(DemoPIN)
	if err != nil {
		return nil, err
	}
	err = db.WithTx(ctx, func(tx pgx.Tx) error {
		// properties
		var main uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM platform.properties ORDER BY created_at LIMIT 1`).Scan(&main); err != nil {
			return fmt.Errorf("no property found; run instance create first: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.properties SET code = 'MAIN' WHERE id = $1 AND NOT EXISTS (SELECT 1 FROM platform.properties WHERE code = 'MAIN')`, main); err != nil {
			return err
		}
		res.Properties["MAIN"] = main
		mdr := id.New()
		if err := tx.QueryRow(ctx, `INSERT INTO platform.properties (id, code, name, city) VALUES ($1, 'MDR', 'Modern Driving Range', 'Tangerang')
			ON CONFLICT (code) DO UPDATE SET name = EXCLUDED.name RETURNING id`, mdr).Scan(&mdr); err != nil {
			return err
		}
		res.Properties["MDR"] = mdr

		// venues, courses, departments per property
		for code, pid := range res.Properties {
			for _, v := range [][3]string{{"GOLF", "Golf Course", "golf"}, {"SPORT", "Sport Center", "sport"}} {
				vid := id.New()
				if err := tx.QueryRow(ctx, `INSERT INTO platform.venues (id, property_id, code, name, venue_type, status) VALUES ($1,$2,$3,$4,$5,'active')
					ON CONFLICT (property_id, code) DO UPDATE SET name = EXCLUDED.name RETURNING id`, vid, pid, v[0], v[1]+" "+code, v[2]).Scan(&vid); err != nil {
					return err
				}
			}
			// Earlier seeds added empty EAST/WEST placeholder courses (no holes, no
			// templates); archive them so the tee sheet opens on the real course.
			if _, err := tx.Exec(ctx, `UPDATE golf.courses c SET status = 'inactive', archived_at = now()
				WHERE c.property_id = $1 AND c.code IN ('EAST', 'WEST') AND c.archived_at IS NULL
				  AND NOT EXISTS (SELECT 1 FROM golf.tee_sheet_templates t WHERE t.course_id = c.id)
				  AND NOT EXISTS (SELECT 1 FROM golf.tee_times t WHERE t.course_id = c.id)`, pid); err != nil {
				return err
			}
			for _, d := range [][2]string{{"GOLF-OPS", "Golf Operations"}, {"FIN", "Finance"}, {"FNB", "Food & Beverage"}} {
				if _, err := tx.Exec(ctx, `INSERT INTO platform.departments (id, property_id, code, name) VALUES ($1,$2,$3,$4)
					ON CONFLICT (property_id, code) DO NOTHING`, id.New(), pid, d[0], d[1]); err != nil {
					return err
				}
			}
		}

		// users
		for _, u := range users {
			uid := id.New()
			if err := tx.QueryRow(ctx, `INSERT INTO platform.users (id, email, full_name, password_hash, password_changed_at, pin_hash, locale)
				VALUES ($1,$2,$3,$4,now(),$5,'id') ON CONFLICT (email) DO UPDATE SET full_name = EXCLUDED.full_name RETURNING id`,
				uid, u.Email, u.Name, hash, pinHash).Scan(&uid); err != nil {
				return err
			}
			var pid *uuid.UUID
			if u.Property != "" {
				p := res.Properties[u.Property]
				pid = &p
			}
			if _, err := tx.Exec(ctx, `INSERT INTO platform.role_assignments (id, user_id, role_id, property_id)
				SELECT $1, $2, r.id, $3 FROM platform.roles r WHERE r.code = $4
				ON CONFLICT DO NOTHING`, id.New(), uid, pid, u.Role); err != nil {
				return err
			}
		}

		if err := seedGolfDemo(ctx, tx, main); err != nil {
			return fmt.Errorf("golf demo: %w", err)
		}
		if err := seedP3P4Demo(ctx, tx, main); err != nil {
			return fmt.Errorf("P3/P4 demo: %w", err)
		}

		// approval workflows: Test Approval (2 steps, step 2 only above
		// IDR 10,000,000) and Venue Activation (1 step).
		type step struct {
			No         int
			Name, Role string
			Conds      []map[string]any
			SLA        *int
		}
		sla := 24
		for _, wf := range []struct {
			doc, name string
			steps     []step
		}{
			{"test_approval", "Test Approval — 2 steps", []step{
				{1, "General Manager review", "general_manager", nil, &sla},
				{2, "Finance approval above IDR 10,000,000", "finance_manager", []map[string]any{{"attribute": "amount", "operator": "gt", "value": 10000000}}, &sla},
			}},
			{"venue_activation", "Venue Activation", []step{{1, "General Manager approval", "general_manager", nil, &sla}}},
			{"membership_application", "Membership Application", []step{{1, "Membership Manager approval", "membership_manager", nil, &sla}}},
			{"refund", "Refund", []step{{1, "Finance Manager approval", "finance_manager", nil, &sla}}},
			{"price_override", "Golf Price Override", []step{{1, "Golf Manager approval", "golf_manager", nil, &sla}}},
			{"cancellation_waiver", "Cancellation / No-show Fee Waiver", []step{{1, "Golf Manager approval", "golf_manager", nil, &sla}}},
		} {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.approval_workflows WHERE document_type = $1)`, wf.doc).Scan(&exists); err != nil {
				return err
			}
			if exists {
				continue
			}
			wid := id.New()
			if _, err := tx.Exec(ctx, `INSERT INTO platform.approval_workflows (id, document_type, name) VALUES ($1,$2,$3)`, wid, wf.doc, wf.name); err != nil {
				return err
			}
			for _, s := range wf.steps {
				conds := s.Conds
				if conds == nil {
					conds = []map[string]any{}
				}
				raw, _ := json.Marshal(conds)
				if _, err := tx.Exec(ctx, `INSERT INTO platform.approval_workflow_steps (id, workflow_id, step_no, name, approver_type, approver_role_id, conditions, sla_hours)
					SELECT $1, $2, $3, $4, 'role', r.id, $5, $6 FROM platform.roles r WHERE r.code = $7`,
					id.New(), wid, s.No, s.Name, raw, s.SLA, s.Role); err != nil {
					return err
				}
			}
		}

		// one POS device at MAIN
		var hasDevice bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.devices WHERE name = 'Demo POS 1')`).Scan(&hasDevice); err != nil {
			return err
		}
		if !hasDevice {
			res.DeviceToken = "ocd_" + secret.RandomToken(32)
			if _, err := tx.Exec(ctx, `INSERT INTO platform.devices (id, property_id, name, device_type, token_hash) VALUES ($1,$2,'Demo POS 1','pos',$3)`,
				id.New(), main, secret.HashToken(res.DeviceToken)); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO audit.audit_log (id, actor_type, actor_name, module, action, category, entity_type, entity_label)
			VALUES ($1, 'system', 'oneclub seed-demo', 'platform', 'demo_seeded', 'system', 'platform.instance', 'Demo data')`, id.New())
		return err
	})
	return res, err
}
