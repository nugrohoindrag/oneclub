// Package authz is the single place where domain authorization is decided
// (FR-IAM-11). Handlers call Require/RequireAt; the UI only hides menus.
//
// Model: a user holds role assignments. An assignment with PropertyID == nil
// is instance-wide (Platform Admin, Super Admin); otherwise it applies to one
// property only (FR-IAM-07, FR-IAM-08). Permissions are granular strings of
// the form <module>.<object>.<action> (FR-IAM-05).
package authz

import (
	"context"
	"slices"

	"github.com/google/uuid"

	"oneclub/internal/kernel/errs"
)

type ActorKind string

const (
	ActorUser   ActorKind = "user"
	ActorAPIKey ActorKind = "api_key"
	ActorDevice ActorKind = "device"
	ActorSystem ActorKind = "system"
)

// Assignment is one role granted to a principal, optionally at one property.
type Assignment struct {
	RoleID      uuid.UUID
	RoleCode    string
	RoleName    string
	PropertyID  *uuid.UUID
	Permissions map[string]struct{}
}

// Principal is the authenticated actor of a request or job.
type Principal struct {
	Kind       ActorKind
	UserID     uuid.UUID // zero for system / api key
	APIKeyID   uuid.UUID
	SessionID  uuid.UUID
	DeviceID   *uuid.UUID
	Name       string
	Email      string
	Locale     string
	MFAPending bool // session must finish MFA before anything else
	// PasswordChangeRequired: temporary password; only the password change
	// endpoint (and other AuthMFAPending routes) are reachable.
	PasswordChangeRequired bool
	MFARequired            bool
	MFAEnabled             bool
	Assignments            []Assignment
	system                 bool
}

// System is the principal used by background jobs and provisioning.
func System() *Principal {
	return &Principal{Kind: ActorSystem, Name: "system", system: true}
}

func (p *Principal) IsSystem() bool { return p != nil && p.system }

// HasRole reports whether any assignment carries role code.
func (p *Principal) HasRole(code string) bool {
	for _, a := range p.Assignments {
		if a.RoleCode == code {
			return true
		}
	}
	return false
}

// RoleCodes returns distinct role codes (for audit "role" column).
func (p *Principal) RoleCodes() []string {
	var out []string
	for _, a := range p.Assignments {
		if !slices.Contains(out, a.RoleCode) {
			out = append(out, a.RoleCode)
		}
	}
	return out
}

// Can reports whether the principal holds perm. With propertyID == nil it is
// true when any assignment grants perm; otherwise the assignment must be
// instance-wide or bound to that property.
func (p *Principal) Can(perm string, propertyID *uuid.UUID) bool {
	if p == nil {
		return false
	}
	if p.system {
		return true
	}
	for _, a := range p.Assignments {
		if _, ok := a.Permissions[perm]; !ok {
			continue
		}
		if propertyID == nil || a.PropertyID == nil || *a.PropertyID == *propertyID {
			return true
		}
	}
	return false
}

// PropertiesFor returns the properties where perm is held. all=true means
// every property (instance-wide assignment). perm == "" means "any role".
func (p *Principal) PropertiesFor(perm string) (all bool, ids []uuid.UUID) {
	if p == nil {
		return false, nil
	}
	if p.system {
		return true, nil
	}
	for _, a := range p.Assignments {
		if perm != "" {
			if _, ok := a.Permissions[perm]; !ok {
				continue
			}
		}
		if a.PropertyID == nil {
			return true, nil
		}
		if !slices.Contains(ids, *a.PropertyID) {
			ids = append(ids, *a.PropertyID)
		}
	}
	return false, ids
}

// CanAccessProperty reports whether the principal has any role at property.
func (p *Principal) CanAccessProperty(property uuid.UUID) bool {
	all, ids := p.PropertiesFor("")
	return all || slices.Contains(ids, property)
}

// Permissions returns the union of permission codes (optionally at property).
func (p *Principal) Permissions(propertyID *uuid.UUID) []string {
	set := map[string]struct{}{}
	for _, a := range p.Assignments {
		if propertyID != nil && a.PropertyID != nil && *a.PropertyID != *propertyID {
			continue
		}
		for k := range a.Permissions {
			set[k] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

type ctxKey struct{}

func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// From returns the principal or nil.
func From(ctx context.Context) *Principal {
	p, _ := ctx.Value(ctxKey{}).(*Principal)
	return p
}

// Require fails with 403 unless the principal holds perm somewhere.
func Require(ctx context.Context, perm string) error {
	p := From(ctx)
	if p == nil {
		return errs.Unauthorized("authentication required")
	}
	if !p.Can(perm, nil) {
		return errs.Forbidden("missing permission " + perm)
	}
	return nil
}

// RequireAt fails with 403 unless the principal holds perm at property.
func RequireAt(ctx context.Context, perm string, property uuid.UUID) error {
	p := From(ctx)
	if p == nil {
		return errs.Unauthorized("authentication required")
	}
	if !p.Can(perm, &property) {
		return errs.Forbidden("missing permission " + perm + " at this property")
	}
	return nil
}
