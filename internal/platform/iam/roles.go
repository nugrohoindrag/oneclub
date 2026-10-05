package iam

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/catalog"
)

// Role is a role with its permissions (FR-IAM-05).
type Role struct {
	ID            uuid.UUID `json:"id"`
	Code          string    `json:"code"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	Category      string    `json:"category"`
	Scope         string    `json:"scope" enum:"platform,instance,property"`
	IsTemplate    bool      `json:"isTemplate"`
	MFARequired   bool      `json:"mfaRequired"`
	Status        string    `json:"status" enum:"active,inactive"`
	Permissions   []string  `json:"permissions"`
	AssignedUsers int       `json:"assignedUsers"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type RoleRequest struct {
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Scope       string   `json:"scope" enum:"instance,property"`
	MFARequired bool     `json:"mfaRequired"`
	Permissions []string `json:"permissions"`
}

type RoleUpdateRequest struct {
	Name        *string   `json:"name,omitempty"`
	Description *string   `json:"description,omitempty"`
	Category    *string   `json:"category,omitempty"`
	MFARequired *bool     `json:"mfaRequired,omitempty"`
	Status      *string   `json:"status,omitempty" enum:"active,inactive"`
	Permissions *[]string `json:"permissions,omitempty"`
}

type PermissionInfo struct {
	Code         string `json:"code"`
	Module       string `json:"module"`
	Object       string `json:"object"`
	Action       string `json:"action"`
	Description  string `json:"description"`
	PlatformOnly bool   `json:"platformOnly"`
}

type Assignment struct {
	ID           uuid.UUID  `json:"id"`
	UserID       uuid.UUID  `json:"userId"`
	UserName     string     `json:"userName"`
	RoleID       uuid.UUID  `json:"roleId"`
	RoleCode     string     `json:"roleCode"`
	RoleName     string     `json:"roleName"`
	PropertyID   *uuid.UUID `json:"propertyId"`
	PropertyName *string    `json:"propertyName"`
	CreatedAt    time.Time  `json:"createdAt"`
	ValidUntil   *time.Time `json:"validUntil,omitempty" doc:"Expiry of a time-bound assignment (Auditor); no access after it"`
}

type CreateAssignmentRequest struct {
	UserID     uuid.UUID  `json:"userId"`
	RoleID     uuid.UUID  `json:"roleId"`
	PropertyID *uuid.UUID `json:"propertyId"`
	ValidUntil *time.Time `json:"validUntil,omitempty" doc:"Expiry of the assignment (required for the Auditor role, PRD P4 §16 #18)"`
}

const roleSelect = `
	SELECT r.id, r.code, r.name, r.description, r.category, r.scope, r.is_template, r.mfa_required, r.status, r.updated_at,
	  coalesce((SELECT array_agg(permission_code ORDER BY permission_code) FROM platform.role_permissions WHERE role_id = r.id), '{}'),
	  (SELECT count(DISTINCT user_id) FROM platform.role_assignments WHERE role_id = r.id)
	FROM platform.roles r`

func scanRole(row pgx.Row) (Role, error) {
	var x Role
	err := row.Scan(&x.ID, &x.Code, &x.Name, &x.Description, &x.Category, &x.Scope, &x.IsTemplate, &x.MFARequired,
		&x.Status, &x.UpdatedAt, &x.Permissions, &x.AssignedUsers)
	return x, err
}

func (s *Service) listRoles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	lp := httpx.ParseList(r)
	var out []Role
	err := s.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		where := "true"
		args := []any{}
		if c := lp.Filters["category"]; c != "" {
			args = append(args, c)
			where += " AND r.category = $1"
		}
		if !authz.From(ctx).Can(catalog.ShellPlatformAdmin, nil) {
			where += " AND r.scope <> 'platform'"
		}
		rows, err := tx.Query(ctx, roleSelect+" WHERE "+where+" ORDER BY r.category, r.name", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			x, err := scanRole(rows)
			if err != nil {
				return err
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	if out == nil {
		out = []Role{}
	}
	respond(w, r, http.StatusOK, httpx.Page[Role]{Items: out}, err)
}

func (s *Service) getRole(w http.ResponseWriter, r *http.Request) {
	rid, err := httpx.PathUUID(r, "id")
	if err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	var out Role
	err = s.DB.WithReadTx(r.Context(), func(tx pgx.Tx) error {
		out, err = scanRole(tx.QueryRow(r.Context(), roleSelect+" WHERE r.id = $1", rid))
		if dbtx.IsNoRows(err) {
			return errs.NotFound("role")
		}
		return err
	})
	respond(w, r, http.StatusOK, out, err)
}

var roleCodeRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,60}$`)

// checkGrantable ensures perms exist, are not Platform Admin–only and are
// all held by the actor (no privilege escalation via custom roles).
func (s *Service) checkGrantable(ctx context.Context, perms []string) error {
	p := authz.From(ctx)
	known := map[string]catalog.Permission{}
	for _, cp := range s.Catalog.Permissions {
		known[cp.Code] = cp
	}
	var bad []errs.FieldError
	for _, code := range perms {
		cp, ok := known[code]
		switch {
		case !ok:
			bad = append(bad, errs.Field("permissions", "unknown", "unknown permission "+code))
		case cp.PlatformOnly:
			bad = append(bad, errs.Field("permissions", "platform_only", code+" is reserved for Platform Admin"))
		case !p.Can(code, nil):
			bad = append(bad, errs.Field("permissions", "not_held", "you cannot grant "+code+" because you do not hold it"))
		}
	}
	if len(bad) > 0 {
		return errs.Validation("invalid_permissions", "invalid permissions", bad...)
	}
	return nil
}

func (s *Service) createRole(w http.ResponseWriter, r *http.Request) {
	var req RoleRequest
	if err := httpx.Decode(r, &req); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	ctx := r.Context()
	var fields []errs.FieldError
	if !roleCodeRe.MatchString(req.Code) {
		fields = append(fields, errs.Field("code", "invalid", "lower-case letters, digits and underscore"))
	}
	if strings.TrimSpace(req.Name) == "" {
		fields = append(fields, errs.Field("name", "required", "name is required"))
	}
	if req.Scope != "instance" && req.Scope != "property" {
		fields = append(fields, errs.Field("scope", "invalid", "must be instance or property"))
	}
	if req.Category == "" {
		req.Category = "Custom"
	}
	if len(fields) > 0 {
		respond(w, r, 0, nil, errs.Validation("invalid_role", "invalid role", fields...))
		return
	}
	req.Permissions = dedupe(req.Permissions)
	if err := s.checkGrantable(ctx, req.Permissions); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	var out Role
	err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		rid := id.New()
		uid := id.Ptr(authz.From(ctx).UserID)
		if _, err := tx.Exec(ctx, `INSERT INTO platform.roles (id, code, name, description, category, scope, mfa_required, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)`, rid, req.Code, req.Name, req.Description, req.Category, req.Scope, req.MFARequired, uid); err != nil {
			if ok, _ := dbtx.IsUniqueViolation(err); ok {
				return errs.Validation("role_code_taken", "role code already exists", errs.Field("code", "taken", "role code already exists"))
			}
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.role_permissions (role_id, permission_code) SELECT $1, unnest($2::text[])`, rid, req.Permissions); err != nil {
			return err
		}
		var err error
		out, err = scanRole(tx.QueryRow(ctx, roleSelect+" WHERE r.id = $1", rid))
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionCreate, Category: audit.CategorySecurity,
			EntityType: "platform.role", EntityID: rid.String(), EntityLabel: out.Name, After: out})
	})
	respond(w, r, http.StatusCreated, out, err)
}

func (s *Service) updateRole(w http.ResponseWriter, r *http.Request) {
	rid, err := httpx.PathUUID(r, "id")
	if err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	var req RoleUpdateRequest
	if err := httpx.Decode(r, &req); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	ctx := r.Context()
	if req.Status != nil && *req.Status != "active" && *req.Status != "inactive" {
		respond(w, r, 0, nil, errs.Validation("invalid_status", "invalid status", errs.Field("status", "invalid", "must be active or inactive")))
		return
	}
	var out Role
	err = s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		before, err := scanRole(tx.QueryRow(ctx, roleSelect+" WHERE r.id = $1 FOR UPDATE OF r", rid))
		if dbtx.IsNoRows(err) {
			return errs.NotFound("role")
		}
		if err != nil {
			return err
		}
		if before.IsTemplate && (req.Permissions != nil || req.Name != nil || req.Category != nil) {
			return errs.Conflict("template_role_readonly", "template roles are maintained by OneClub; create a custom role instead")
		}
		if before.Scope == "platform" {
			return errs.Forbidden("the Platform Admin role cannot be changed")
		}
		if req.Permissions != nil {
			perms := dedupe(*req.Permissions)
			// Only newly added permissions must be grantable by the actor.
			var added []string
			for _, p := range perms {
				if !slices.Contains(before.Permissions, p) {
					added = append(added, p)
				}
			}
			if err := s.checkGrantable(ctx, added); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM platform.role_permissions WHERE role_id = $1`, rid); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO platform.role_permissions (role_id, permission_code) SELECT $1, unnest($2::text[])`, rid, perms); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.roles SET name = coalesce($2, name), description = coalesce($3, description),
			category = coalesce($4, category), mfa_required = coalesce($5, mfa_required), status = coalesce($6, status), updated_by = $7, updated_at = now()
			WHERE id = $1`, rid, req.Name, req.Description, req.Category, req.MFARequired, req.Status, id.Ptr(authz.From(ctx).UserID)); err != nil {
			return err
		}
		out, err = scanRole(tx.QueryRow(ctx, roleSelect+" WHERE r.id = $1", rid))
		if err != nil {
			return err
		}
		action := audit.ActionUpdate
		if req.Permissions != nil {
			action = audit.ActionPermissionChange
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: action, Category: audit.CategorySecurity,
			EntityType: "platform.role", EntityID: rid.String(), EntityLabel: out.Name, Before: before, After: out})
	})
	respond(w, r, http.StatusOK, out, err)
}

func (s *Service) deleteRole(w http.ResponseWriter, r *http.Request) {
	rid, err := httpx.PathUUID(r, "id")
	if err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	ctx := r.Context()
	err = s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		before, err := scanRole(tx.QueryRow(ctx, roleSelect+" WHERE r.id = $1 FOR UPDATE OF r", rid))
		if dbtx.IsNoRows(err) {
			return errs.NotFound("role")
		}
		if err != nil {
			return err
		}
		if before.IsTemplate {
			return errs.Conflict("template_role_readonly", "template roles cannot be deleted; set them Inactive instead")
		}
		if before.AssignedUsers > 0 {
			return errs.Conflict("role_in_use", "role is assigned to users; remove the assignments or set it Inactive")
		}
		if _, err := tx.Exec(ctx, `DELETE FROM platform.roles WHERE id = $1`, rid); err != nil {
			if dbtx.IsForeignKeyViolation(err) {
				return errs.Conflict("role_in_use", "role is referenced (e.g. by an approval workflow); set it Inactive instead")
			}
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionDelete, Category: audit.CategorySecurity,
			EntityType: "platform.role", EntityID: rid.String(), EntityLabel: before.Name, Before: before})
	})
	respond(w, r, 0, nil, err)
}

func (s *Service) listPermissions(w http.ResponseWriter, r *http.Request) {
	out := make([]PermissionInfo, 0, len(s.Catalog.Permissions))
	showPlatform := authz.From(r.Context()).Can(catalog.ShellPlatformAdmin, nil)
	for _, p := range s.Catalog.Permissions {
		if p.PlatformOnly && !showPlatform {
			continue
		}
		m, o, a := p.Parts()
		out = append(out, PermissionInfo{Code: p.Code, Module: m, Object: o, Action: a, Description: p.Description, PlatformOnly: p.PlatformOnly})
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[PermissionInfo]{Items: out})
}

// ── Role assignments (FR-IAM-07, FR-IAM-08) ────────────────────────────────

// assign validates and inserts one assignment. Rules:
//   - platform roles: only a Platform Admin
//   - instance roles (Super Admin): no property; actor must be instance-wide
//   - property roles: property required; actor needs role_assignment.manage at
//     that property and must hold every permission of the role there
func (s *Service) assign(ctx context.Context, tx pgx.Tx, userID uuid.UUID, userName string, a AssignmentRequest) (uuid.UUID, error) {
	p := authz.From(ctx)
	var code, name, scope, status string
	var perms []string
	err := tx.QueryRow(ctx, `SELECT code, name, scope, status,
		coalesce((SELECT array_agg(permission_code) FROM platform.role_permissions WHERE role_id = r.id), '{}')
		FROM platform.roles r WHERE id = $1`, a.RoleID).Scan(&code, &name, &scope, &status, &perms)
	if dbtx.IsNoRows(err) {
		return uuid.Nil, errs.Validation("role_invalid", "role not found", errs.Field("roleId", "invalid", "role not found"))
	}
	if err != nil {
		return uuid.Nil, err
	}
	if status != "active" {
		return uuid.Nil, errs.Validation("role_inactive", "role is inactive", errs.Field("roleId", "inactive", "role is inactive"))
	}
	instanceWide, _ := p.PropertiesFor("platform.role_assignment.manage")
	switch scope {
	case "platform":
		if !p.Can(catalog.ShellPlatformAdmin, nil) {
			return uuid.Nil, errs.Forbidden("only a Platform Admin can assign " + name)
		}
		if a.PropertyID != nil {
			return uuid.Nil, errs.Validation("property_not_allowed", name+" applies to all properties", errs.Field("propertyId", "not_allowed", "must be empty"))
		}
	case "instance":
		if !instanceWide {
			return uuid.Nil, errs.Forbidden("only an instance-wide administrator can assign " + name)
		}
		if a.PropertyID != nil {
			return uuid.Nil, errs.Validation("property_not_allowed", name+" applies to all properties", errs.Field("propertyId", "not_allowed", "must be empty"))
		}
	default:
		if a.PropertyID == nil {
			return uuid.Nil, errs.Validation("property_required", name+" is assigned per property", errs.Field("propertyId", "required", "select a property"))
		}
		if !p.Can("platform.role_assignment.manage", a.PropertyID) {
			return uuid.Nil, errs.Forbidden("you cannot manage roles at this property")
		}
		if !p.IsSystem() && !instanceWide {
			for _, perm := range perms {
				if !p.Can(perm, a.PropertyID) {
					return uuid.Nil, errs.Forbidden("you cannot assign " + name + " because it grants " + perm + " which you do not hold")
				}
			}
		}
	}
	if err := checkValidUntil(code, name, a.ValidUntil); err != nil {
		return uuid.Nil, err
	}
	// an expired time-bound assignment is renewed for the new period
	if rid, renewed, err := renewExpired(ctx, tx, userID, a); err != nil || renewed {
		if err != nil {
			return uuid.Nil, err
		}
		return rid, audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionRoleAssigned, Category: audit.CategorySecurity,
			EntityType: "platform.role_assignment", EntityID: rid.String(), EntityLabel: userName + " → " + name, PropertyID: a.PropertyID,
			After: map[string]any{"userId": userID, "roleId": a.RoleID, "roleCode": code, "propertyId": a.PropertyID, "validUntil": a.ValidUntil, "renewed": true}})
	}
	aid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO platform.role_assignments (id, user_id, role_id, property_id, created_by, valid_until) VALUES ($1, $2, $3, $4, $5, $6)`,
		aid, userID, a.RoleID, a.PropertyID, id.Ptr(p.UserID), a.ValidUntil); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return uuid.Nil, errs.Conflict("assignment_exists", "the user already has this role at this property")
		}
		if dbtx.IsForeignKeyViolation(err) {
			return uuid.Nil, errs.Validation("property_invalid", "property not found", errs.Field("propertyId", "invalid", "property not found"))
		}
		return uuid.Nil, err
	}
	return aid, audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionRoleAssigned, Category: audit.CategorySecurity,
		EntityType: "platform.role_assignment", EntityID: aid.String(), EntityLabel: userName + " → " + name, PropertyID: a.PropertyID,
		After: map[string]any{"userId": userID, "roleId": a.RoleID, "roleCode": code, "propertyId": a.PropertyID, "validUntil": a.ValidUntil}})
}

const assignmentSelect = `
	SELECT ra.id, ra.user_id, u.full_name, r.id, r.code, r.name, ra.property_id, p.name, ra.created_at, ra.valid_until
	FROM platform.role_assignments ra
	JOIN platform.users u ON u.id = ra.user_id
	JOIN platform.roles r ON r.id = ra.role_id
	LEFT JOIN platform.properties p ON p.id = ra.property_id`

func scanAssignment(row pgx.Row) (Assignment, error) {
	var a Assignment
	err := row.Scan(&a.ID, &a.UserID, &a.UserName, &a.RoleID, &a.RoleCode, &a.RoleName, &a.PropertyID, &a.PropertyName, &a.CreatedAt, &a.ValidUntil)
	return a, err
}

func (s *Service) listAssignments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	lp := httpx.ParseList(r)
	var out []Assignment
	err := s.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		sc := dbtx.ScopeFrom(ctx)
		where := []string{"true"}
		args := []any{}
		if !sc.AllProperties {
			args = append(args, sc.PropertyIDs)
			where = append(where, "ra.property_id = ANY($1)")
		}
		if v := lp.Filters["userId"]; v != "" {
			args = append(args, v)
			where = append(where, "ra.user_id::text = $"+itoaN(len(args)))
		}
		if v := lp.Filters["propertyId"]; v != "" {
			args = append(args, v)
			where = append(where, "ra.property_id::text = $"+itoaN(len(args)))
		}
		if v := lp.Filters["roleId"]; v != "" {
			args = append(args, v)
			where = append(where, "ra.role_id::text = $"+itoaN(len(args)))
		}
		rows, err := tx.Query(ctx, assignmentSelect+" WHERE "+strings.Join(where, " AND ")+" ORDER BY u.full_name, r.name LIMIT 1000", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAssignment(rows)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	if out == nil {
		out = []Assignment{}
	}
	respond(w, r, http.StatusOK, httpx.Page[Assignment]{Items: out}, err)
}

func (s *Service) createAssignment(w http.ResponseWriter, r *http.Request) {
	var req CreateAssignmentRequest
	if err := httpx.Decode(r, &req); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	ctx := r.Context()
	var out Assignment
	err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var name string
		if err := tx.QueryRow(ctx, `SELECT full_name FROM platform.users WHERE id = $1`, req.UserID).Scan(&name); err != nil {
			if dbtx.IsNoRows(err) {
				return errs.Validation("user_invalid", "user not found", errs.Field("userId", "invalid", "user not found"))
			}
			return err
		}
		aid, err := s.assign(ctx, tx, req.UserID, name, AssignmentRequest{RoleID: req.RoleID, PropertyID: req.PropertyID, ValidUntil: req.ValidUntil})
		if err != nil {
			return err
		}
		out, err = scanAssignment(tx.QueryRow(ctx, assignmentSelect+" WHERE ra.id = $1", aid))
		return err
	})
	respond(w, r, http.StatusCreated, out, err)
}

func (s *Service) deleteAssignment(w http.ResponseWriter, r *http.Request) {
	aid, err := httpx.PathUUID(r, "id")
	if err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	ctx := r.Context()
	p := authz.From(ctx)
	err = s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		a, err := scanAssignment(tx.QueryRow(ctx, assignmentSelect+" WHERE ra.id = $1 FOR UPDATE OF ra", aid))
		if dbtx.IsNoRows(err) {
			return errs.NotFound("role assignment")
		}
		if err != nil {
			return err
		}
		if a.PropertyID == nil {
			if all, _ := p.PropertiesFor("platform.role_assignment.manage"); !all {
				return errs.Forbidden("only an instance-wide administrator can remove this role")
			}
			if a.RoleCode == "platform_admin" && !p.Can(catalog.ShellPlatformAdmin, nil) {
				return errs.Forbidden("only a Platform Admin can remove this role")
			}
		} else if !p.Can("platform.role_assignment.manage", a.PropertyID) {
			return errs.Forbidden("you cannot manage roles at this property")
		}
		if a.UserID == p.UserID && a.PropertyID == nil {
			return errs.Conflict("cannot_remove_own_admin", "you cannot remove your own instance-wide role")
		}
		if _, err := tx.Exec(ctx, `DELETE FROM platform.role_assignments WHERE id = $1`, aid); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionRoleUnassigned, Category: audit.CategorySecurity,
			EntityType: "platform.role_assignment", EntityID: aid.String(), EntityLabel: a.UserName + " → " + a.RoleName,
			PropertyID: a.PropertyID, Before: a})
	})
	respond(w, r, 0, nil, err)
}

func dedupe(in []string) []string {
	out := []string{}
	for _, s := range in {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}
