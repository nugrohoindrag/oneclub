package iam

// Employee logins of PRD P5 (FR-HR-04, contract H7), called by the HRIS
// module through internal/app inside its transaction: HR creates (or links)
// the personal login of an employee at onboarding with the self-service
// role, and the login is deactivated on the effective date of a
// resignation / termination.

import (
	"context"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
)

// EmployeeUserRequest creates or links the login of an employee.
type EmployeeUserRequest struct {
	EmployeeID uuid.UUID
	PropertyID uuid.UUID
	FullName   string
	Email      string
	Phone      *string
	Locale     *string
	// RoleCodes are assigned at PropertyID. Roles listed in
	// OnboardingRoles (the self-service role of the HR Configuration) may
	// be granted by HR without platform.role_assignment.manage; any other
	// role goes through the normal assignment checks.
	RoleCodes       []string
	OnboardingRoles []string
	ExistingUserID  *uuid.UUID
}

// ProvisionEmployeeUser creates the user (invitation e-mail to set the
// password) or links an existing one, and assigns the roles.
func (s *Service) ProvisionEmployeeUser(ctx context.Context, tx pgx.Tx, r EmployeeUserRequest) (uuid.UUID, error) {
	p := authz.From(ctx)
	if p == nil {
		return uuid.Nil, errs.Unauthorized("authentication required")
	}
	var uid uuid.UUID
	created := false
	if r.ExistingUserID != nil {
		var linked *uuid.UUID
		var name string
		if err := tx.QueryRow(ctx, `SELECT employee_id, full_name FROM platform.users WHERE id = $1 FOR UPDATE`, *r.ExistingUserID).Scan(&linked, &name); err != nil {
			return uuid.Nil, errs.Validation("user_invalid", "user not found", errs.Field("userId", "invalid", "user not found"))
		}
		if linked != nil && *linked != r.EmployeeID {
			return uuid.Nil, errs.Conflict("user_linked", name+" is linked to another employee")
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.users SET employee_id = $2, updated_by = $3 WHERE id = $1`, *r.ExistingUserID, r.EmployeeID,
			id.Ptr(p.UserID)); err != nil {
			return uuid.Nil, err
		}
		uid = *r.ExistingUserID
	} else {
		email := strings.ToLower(strings.TrimSpace(r.Email))
		if !strings.Contains(email, "@") || len(email) > 254 {
			return uuid.Nil, errs.Validation("invalid_user", "invalid user", errs.Field("email", "invalid", "a valid e-mail is required"))
		}
		if !validLocale(r.Locale) {
			return uuid.Nil, errs.Validation("invalid_user", "invalid user", errs.Field("locale", "invalid", "must be en or id"))
		}
		uid = id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO platform.users (id, email, full_name, phone, employee_id, locale, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`, uid, email, r.FullName, r.Phone, r.EmployeeID, r.Locale, id.Ptr(p.UserID)); err != nil {
			if ok, _ := dbtx.IsUniqueViolation(err); ok {
				return uuid.Nil, errs.Validation("email_taken", "e-mail already registered",
					errs.Field("email", "taken", "this e-mail is already registered; link the existing user instead"))
			}
			return uuid.Nil, err
		}
		created = true
	}
	for _, code := range r.RoleCodes {
		var rid uuid.UUID
		var name, scope string
		if err := tx.QueryRow(ctx, `SELECT id, name, scope FROM platform.roles WHERE code = $1 AND status = 'active'`, code).Scan(&rid, &name, &scope); err != nil {
			return uuid.Nil, errs.Validation("role_invalid", "role not found", errs.Field("roleCodes", "invalid", "unknown role "+code))
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.role_assignments WHERE user_id = $1 AND role_id = $2 AND property_id = $3)`,
			uid, rid, r.PropertyID).Scan(&exists); err != nil {
			return uuid.Nil, err
		}
		if exists {
			continue
		}
		pid := r.PropertyID
		if scope != "property" || !slices.Contains(r.OnboardingRoles, code) {
			if _, err := s.assign(ctx, tx, uid, r.FullName, AssignmentRequest{RoleID: rid, PropertyID: &pid}); err != nil {
				return uuid.Nil, err
			}
			continue
		}
		aid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO platform.role_assignments (id, user_id, role_id, property_id, created_by) VALUES ($1, $2, $3, $4, $5)`,
			aid, uid, rid, pid, id.Ptr(p.UserID)); err != nil {
			return uuid.Nil, err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionRoleAssigned, Category: audit.CategorySecurity,
			EntityType: "platform.role_assignment", EntityID: aid.String(), EntityLabel: r.FullName + " → " + name, PropertyID: &pid,
			After: map[string]any{"userId": uid, "roleId": rid, "roleCode": code, "propertyId": pid, "onboarding": true}}); err != nil {
			return uuid.Nil, err
		}
	}
	out, err := s.getUserTx(dbtx.System(ctx), tx, uid, false)
	if err != nil {
		return uuid.Nil, err
	}
	action := audit.ActionUpdate
	if created {
		action = audit.ActionCreate
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: action, EntityType: "platform.user", EntityID: uid.String(),
		EntityLabel: out.FullName, After: out, Metadata: map[string]any{"employeeId": r.EmployeeID, "source": "hris_onboarding"}}); err != nil {
		return uuid.Nil, err
	}
	if created {
		if err := s.issueResetToken(ctx, tx, uid, r.FullName, "auth.account_invite", InviteTokenTTL); err != nil {
			return uuid.Nil, err
		}
	}
	return uid, nil
}

// DeactivateEmployeeUser deactivates the login of an employee who left
// (H7): the user is set Inactive, sessions are revoked and approval
// delegations from or to the user end. ctx may be a system context.
func (s *Service) DeactivateEmployeeUser(ctx context.Context, tx pgx.Tx, uid uuid.UUID, reason string) error {
	var status, name string
	if err := tx.QueryRow(ctx, `SELECT status, full_name FROM platform.users WHERE id = $1 FOR UPDATE`, uid).Scan(&status, &name); err != nil {
		if dbtx.IsNoRows(err) {
			return nil
		}
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.sessions SET revoked_at = now(), revoked_reason = 'employee_terminated' WHERE user_id = $1 AND revoked_at IS NULL`,
		uid); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.approval_delegations SET revoked_at = now() WHERE (delegator_user_id = $1 OR delegate_user_id = $1)
		AND revoked_at IS NULL`, uid); err != nil {
		return err
	}
	if status == "inactive" {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.users SET status = 'inactive' WHERE id = $1`, uid); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionStatusChange, Category: audit.CategorySecurity,
		EntityType: "platform.user", EntityID: uid.String(), EntityLabel: name, Reason: reason,
		Before: map[string]any{"status": status}, After: map[string]any{"status": "inactive"}, ActorName: "HRIS offboarding"})
}
