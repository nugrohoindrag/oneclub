package provision

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/catalog"
)

// DocumentType is an approval document type registered by a module (FR-APR-09).
type DocumentType struct {
	Code       string
	Module     string
	Name       string
	Attributes []DocumentAttribute
}

// DocumentAttribute is a condition attribute exposed by a document type.
type DocumentAttribute struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Type  string `json:"type"` // number | string | uuid
}

// ReportDef is a report registry entry (FR-REP-02).
type ReportDef struct {
	Code        string
	Name        string
	Module      string
	Permission  string
	Description string
	Parameters  []map[string]any
	Columns     []map[string]any
}

// Template is a default notification template (FR-NOT-02).
type Template struct {
	Event   string
	Channel string
	Locale  string
	Subject string
	Body    string
}

// Flag is a default feature flag (FR-INS-05).
type Flag struct {
	Key           string
	Description   string
	Type          string
	Default       any
	ClientVisible bool
}

// Seeds is everything synchronised from code into an instance database.
type Seeds struct {
	Catalog       *catalog.Catalog
	DocumentTypes []DocumentType
	Reports       []ReportDef
	Templates     []Template
	Flags         []Flag
}

// Sync upserts the catalogue. Idempotent; runs after every migrate.
//   - modules: inserted with their default enablement, never re-enabled
//   - permissions: upserted; removed codes are deleted
//   - role templates: upserted, permissions replaced to match the template
//   - document types, reports: upserted
//   - templates, flags: inserted only when missing (club may edit them)
func Sync(ctx context.Context, tx pgx.Tx, s Seeds) error {
	c := s.Catalog
	for _, m := range c.Modules {
		if _, err := tx.Exec(ctx, `
			INSERT INTO platform.modules (code, name, layer, sort_order, enabled, always_on)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (code) DO UPDATE SET name = EXCLUDED.name, layer = EXCLUDED.layer,
			  sort_order = EXCLUDED.sort_order, always_on = EXCLUDED.always_on,
			  enabled = platform.modules.enabled OR EXCLUDED.always_on`,
			m.Code, m.Name, m.Layer, m.SortOrder, m.Default || m.AlwaysOn, m.AlwaysOn); err != nil {
			return fmt.Errorf("sync module %s: %w", m.Code, err)
		}
	}

	codes := make([]string, 0, len(c.Permissions))
	for _, p := range c.Permissions {
		mod, obj, act := p.Parts()
		codes = append(codes, p.Code)
		if _, err := tx.Exec(ctx, `
			INSERT INTO platform.permissions (code, module, object, action, description, platform_only)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (code) DO UPDATE SET description = EXCLUDED.description, platform_only = EXCLUDED.platform_only`,
			p.Code, mod, obj, act, p.Description, p.PlatformOnly); err != nil {
			return fmt.Errorf("sync permission %s: %w", p.Code, err)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM platform.permissions WHERE NOT (code = ANY($1))`, codes); err != nil {
		return err
	}

	for _, r := range c.Roles {
		var roleID [16]byte
		err := tx.QueryRow(ctx, `
			INSERT INTO platform.roles (id, code, name, category, scope, is_template, mfa_required, description)
			VALUES ($1, $2, $3, $4, $5, true, $6, $7)
			ON CONFLICT (code) DO UPDATE SET name = EXCLUDED.name, category = EXCLUDED.category,
			  scope = EXCLUDED.scope, is_template = true, mfa_required = EXCLUDED.mfa_required
			RETURNING id`,
			id.New(), r.Code, r.Name, r.Category, r.Scope, r.MFARequired, "Template role ("+r.Category+")").Scan(&roleID)
		if err != nil {
			return fmt.Errorf("sync role %s: %w", r.Code, err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM platform.role_permissions WHERE role_id = $1`, roleID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO platform.role_permissions (role_id, permission_code)
			SELECT $1, unnest($2::text[])`, roleID, r.Permissions); err != nil {
			return fmt.Errorf("sync role permissions %s: %w", r.Code, err)
		}
	}
	// Custom (non-template) roles may not keep permissions that became
	// Platform Admin–only.
	if _, err := tx.Exec(ctx, `
		DELETE FROM platform.role_permissions rp USING platform.roles r, platform.permissions p
		WHERE rp.role_id = r.id AND rp.permission_code = p.code AND p.platform_only AND r.scope <> 'platform'`); err != nil {
		return err
	}

	for _, d := range s.DocumentTypes {
		attrs, _ := json.Marshal(d.Attributes)
		if _, err := tx.Exec(ctx, `
			INSERT INTO platform.approval_document_types (code, module, name, attributes) VALUES ($1, $2, $3, $4)
			ON CONFLICT (code) DO UPDATE SET module = EXCLUDED.module, name = EXCLUDED.name, attributes = EXCLUDED.attributes`,
			d.Code, d.Module, d.Name, attrs); err != nil {
			return fmt.Errorf("sync document type %s: %w", d.Code, err)
		}
	}

	for _, r := range s.Reports {
		params, _ := json.Marshal(nonNil(r.Parameters))
		cols, _ := json.Marshal(nonNil(r.Columns))
		if _, err := tx.Exec(ctx, `
			INSERT INTO reporting.report_definitions (code, name, module, permission, description, parameters, columns)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (code) DO UPDATE SET name = EXCLUDED.name, module = EXCLUDED.module, permission = EXCLUDED.permission,
			  description = EXCLUDED.description, parameters = EXCLUDED.parameters, columns = EXCLUDED.columns`,
			r.Code, r.Name, r.Module, r.Permission, r.Description, params, cols); err != nil {
			return fmt.Errorf("sync report %s: %w", r.Code, err)
		}
	}

	for _, t := range s.Templates {
		if _, err := tx.Exec(ctx, `
			INSERT INTO platform.notification_templates (id, event_code, channel, locale, subject, body)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (event_code, channel, locale) DO NOTHING`,
			id.New(), t.Event, t.Channel, t.Locale, t.Subject, t.Body); err != nil {
			return fmt.Errorf("sync template %s: %w", t.Event, err)
		}
	}

	for _, f := range s.Flags {
		v, _ := json.Marshal(f.Default)
		if _, err := tx.Exec(ctx, `
			INSERT INTO platform.feature_flags (key, description, value, value_type, client_visible)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (key) DO UPDATE SET description = EXCLUDED.description, value_type = EXCLUDED.value_type,
			  client_visible = EXCLUDED.client_visible`,
			f.Key, f.Description, v, f.Type, f.ClientVisible); err != nil {
			return fmt.Errorf("sync flag %s: %w", f.Key, err)
		}
	}
	return nil
}

func nonNil(v []map[string]any) []map[string]any {
	if v == nil {
		return []map[string]any{}
	}
	return v
}
