package iam

import (
	"context"
	"net/http"
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
	"oneclub/internal/platform/iam/password"
)

// User is the Users API representation (FR-IAM-01).
type User struct {
	ID          uuid.UUID        `json:"id"`
	Email       string           `json:"email" format:"email"`
	FullName    string           `json:"fullName"`
	Phone       *string          `json:"phone"`
	EmployeeID  *uuid.UUID       `json:"employeeId"`
	Status      string           `json:"status" enum:"active,inactive"`
	MFAEnabled  bool             `json:"mfaEnabled"`
	HasPIN      bool             `json:"hasPin"`
	Locked      bool             `json:"locked"`
	LastLoginAt *time.Time       `json:"lastLoginAt"`
	Locale      *string          `json:"locale"`
	CreatedAt   time.Time        `json:"createdAt"`
	UpdatedAt   time.Time        `json:"updatedAt"`
	Assignments []UserAssignment `json:"assignments"`
}

type UserAssignment struct {
	ID           uuid.UUID  `json:"id"`
	RoleID       uuid.UUID  `json:"roleId"`
	RoleCode     string     `json:"roleCode"`
	RoleName     string     `json:"roleName"`
	PropertyID   *uuid.UUID `json:"propertyId"`
	PropertyName *string    `json:"propertyName"`
	ValidUntil   *time.Time `json:"validUntil,omitempty" doc:"Expiry of a time-bound assignment (Auditor); no access after it"`
}

type CreateUserRequest struct {
	Email       string              `json:"email" format:"email"`
	FullName    string              `json:"fullName"`
	Phone       *string             `json:"phone,omitempty"`
	EmployeeID  *uuid.UUID          `json:"employeeId,omitempty"`
	Locale      *string             `json:"locale,omitempty" enum:"en,id"`
	Password    *string             `json:"password,omitempty" doc:"Optional initial password (must be changed at first login). Without it an invitation e-mail is sent."`
	Assignments []AssignmentRequest `json:"assignments"`
}

type AssignmentRequest struct {
	RoleID     uuid.UUID  `json:"roleId"`
	PropertyID *uuid.UUID `json:"propertyId"`
	ValidUntil *time.Time `json:"validUntil,omitempty" doc:"Expiry of the assignment (required for the Auditor role, PRD P4 §16 #18)"`
}

type UpdateUserRequest struct {
	FullName   *string    `json:"fullName,omitempty"`
	Phone      *string    `json:"phone,omitempty"`
	EmployeeID *uuid.UUID `json:"employeeId,omitempty"`
	Locale     *string    `json:"locale,omitempty" enum:"en,id"`
}

type ReasonRequest struct {
	Reason string `json:"reason"`
}

type SetPINRequest struct {
	PIN string `json:"pin"`
}

const userSelect = `
	SELECT u.id, u.email, u.full_name, u.phone, u.employee_id, u.status, u.mfa_enabled, u.pin_hash IS NOT NULL,
	       coalesce(u.locked_until > now(), false), u.last_login_at, u.locale, u.created_at, u.updated_at
	FROM platform.users u`

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.FullName, &u.Phone, &u.EmployeeID, &u.Status, &u.MFAEnabled, &u.HasPIN,
		&u.Locked, &u.LastLoginAt, &u.Locale, &u.CreatedAt, &u.UpdatedAt)
	return u, err
}

func (s *Service) loadUserAssignments(ctx context.Context, q dbtx.Querier, users []User) error {
	if len(users) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(users))
	idx := map[uuid.UUID]int{}
	for i, u := range users {
		ids[i] = u.ID
		idx[u.ID] = i
		users[i].Assignments = []UserAssignment{}
	}
	rows, err := q.Query(ctx, `
		SELECT ra.user_id, ra.id, r.id, r.code, r.name, ra.property_id, p.name, ra.valid_until
		FROM platform.role_assignments ra JOIN platform.roles r ON r.id = ra.role_id
		LEFT JOIN platform.properties p ON p.id = ra.property_id
		WHERE ra.user_id = ANY($1) ORDER BY r.name`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var uid uuid.UUID
		var a UserAssignment
		if err := rows.Scan(&uid, &a.ID, &a.RoleID, &a.RoleCode, &a.RoleName, &a.PropertyID, &a.PropertyName, &a.ValidUntil); err != nil {
			return err
		}
		users[idx[uid]].Assignments = append(users[idx[uid]].Assignments, a)
	}
	return rows.Err()
}

// visibilityClause restricts users to those with a role at a property in
// scope (Property Admin only manages users of their own properties).
func visibilityClause(ctx context.Context, argPos int) (string, []any) {
	sc := dbtx.ScopeFrom(ctx)
	if sc.AllProperties {
		return "true", nil
	}
	return `EXISTS (SELECT 1 FROM platform.role_assignments x WHERE x.user_id = u.id AND x.property_id = ANY($` +
		itoaN(argPos) + `))`, []any{sc.PropertyIDs}
}

func (s *Service) listUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	lp := httpx.ParseList(r)
	cursor, err := httpx.DecodeCursor(lp.Cursor)
	if err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	var out []User
	err = s.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		where := []string{}
		args := []any{}
		vis, vargs := visibilityClause(ctx, 1)
		where = append(where, vis)
		args = append(args, vargs...)
		add := func(cond string, v any) {
			args = append(args, v)
			where = append(where, strings.ReplaceAll(cond, "?", "$"+itoaN(len(args))))
		}
		if st := lp.Filters["status"]; st != "" {
			add("u.status = ?", st)
		}
		if lp.Q != "" {
			add("(u.full_name ILIKE ? OR u.email::text ILIKE ?)", "%"+lp.Q+"%")
		}
		if rc := lp.Filters["roleCode"]; rc != "" {
			add("EXISTS (SELECT 1 FROM platform.role_assignments y JOIN platform.roles rr ON rr.id = y.role_id WHERE y.user_id = u.id AND rr.code = ?)", rc)
		}
		if pid := lp.Filters["propertyId"]; pid != "" {
			add("EXISTS (SELECT 1 FROM platform.role_assignments z WHERE z.user_id = u.id AND z.property_id::text = ?)", pid)
		}
		if cursor != "" {
			add("u.id < ?::uuid", cursor)
		}
		args = append(args, lp.PageSize+1)
		rows, err := tx.Query(ctx, userSelect+" WHERE "+strings.Join(where, " AND ")+" ORDER BY u.id DESC LIMIT $"+itoaN(len(args)), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			u, err := scanUser(rows)
			if err != nil {
				return err
			}
			out = append(out, u)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return s.loadUserAssignments(ctx, tx, out)
	})
	if err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.BuildPage(out, lp.PageSize, func(u User) string { return u.ID.String() }))
}

func itoaN(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoaN(n/10) + string(rune('0'+n%10))
}

func (s *Service) getUserTx(ctx context.Context, tx pgx.Tx, uid uuid.UUID, forUpdate bool) (User, error) {
	vis, vargs := visibilityClause(ctx, 2)
	q := userSelect + " WHERE u.id = $1 AND " + vis
	if forUpdate {
		q += " FOR UPDATE OF u"
	}
	u, err := scanUser(tx.QueryRow(ctx, q, append([]any{uid}, vargs...)...))
	if dbtx.IsNoRows(err) {
		return u, errs.NotFound("user")
	}
	if err != nil {
		return u, err
	}
	us := []User{u}
	if err := s.loadUserAssignments(ctx, tx, us); err != nil {
		return u, err
	}
	return us[0], nil
}

// guardTarget prevents a property-scoped administrator from touching an
// instance-wide account (Super Admin / Platform Admin).
func guardTarget(ctx context.Context, u User) error {
	p := authz.From(ctx)
	if all, _ := p.PropertiesFor(""); all {
		return nil
	}
	for _, a := range u.Assignments {
		if a.PropertyID == nil {
			return errs.Forbidden("only an instance-wide administrator can change this account")
		}
	}
	return nil
}

func (s *Service) getUser(w http.ResponseWriter, r *http.Request) {
	uid, err := httpx.PathUUID(r, "id")
	if err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	var u User
	err = s.DB.WithReadTx(r.Context(), func(tx pgx.Tx) error {
		u, err = s.getUserTx(r.Context(), tx, uid, false)
		return err
	})
	respond(w, r, http.StatusOK, u, err)
}

func validLocale(l *string) bool { return l == nil || *l == "en" || *l == "id" }

func (s *Service) createUser(w http.ResponseWriter, r *http.Request) {
	var req CreateUserRequest
	if err := httpx.Decode(r, &req); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	ctx := r.Context()
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.FullName = strings.TrimSpace(req.FullName)
	var fields []errs.FieldError
	if !strings.Contains(req.Email, "@") || len(req.Email) > 254 {
		fields = append(fields, errs.Field("email", "invalid", "a valid e-mail is required"))
	}
	if req.FullName == "" {
		fields = append(fields, errs.Field("fullName", "required", "full name is required"))
	}
	if !validLocale(req.Locale) {
		fields = append(fields, errs.Field("locale", "invalid", "must be en or id"))
	}
	if len(req.Assignments) == 0 {
		fields = append(fields, errs.Field("assignments", "required", "assign at least one role"))
	}
	if len(fields) > 0 {
		respond(w, r, 0, nil, errs.Validation("invalid_user", "invalid user", fields...))
		return
	}
	if req.Password != nil {
		if err := password.CheckPolicy(*req.Password, req.Email); err != nil {
			respond(w, r, 0, nil, err)
			return
		}
	}
	var out User
	err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		uid := id.New()
		var hash *string
		mustChange := false
		if req.Password != nil {
			h, err := password.Hash(*req.Password)
			if err != nil {
				return err
			}
			hash = &h
			mustChange = true
		}
		p := authz.From(ctx)
		if _, err := tx.Exec(ctx, `
			INSERT INTO platform.users (id, email, full_name, phone, employee_id, locale, password_hash, password_changed_at,
			  must_change_password, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, CASE WHEN $7::text IS NULL THEN NULL ELSE now() END, $8, $9, $9)`,
			uid, req.Email, req.FullName, req.Phone, req.EmployeeID, req.Locale, hash, mustChange, id.Ptr(p.UserID)); err != nil {
			if ok, _ := dbtx.IsUniqueViolation(err); ok {
				return errs.Validation("email_taken", "e-mail already registered", errs.Field("email", "taken", "this e-mail is already registered"))
			}
			if dbtx.IsForeignKeyViolation(err) {
				return errs.Validation("employee_invalid", "employee not found", errs.Field("employeeId", "invalid", "employee not found"))
			}
			return err
		}
		for _, a := range req.Assignments {
			if _, err := s.assign(ctx, tx, uid, req.FullName, a); err != nil {
				return err
			}
		}
		var err error
		out, err = s.getUserTx(dbtx.System(ctx), tx, uid, false)
		if err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{
			Module: "platform", Action: audit.ActionCreate, EntityType: "platform.user", EntityID: uid.String(),
			EntityLabel: req.FullName, After: out,
		}); err != nil {
			return err
		}
		if req.Password == nil {
			return s.issueResetToken(ctx, tx, uid, req.FullName, "auth.account_invite", InviteTokenTTL)
		}
		return nil
	})
	respond(w, r, http.StatusCreated, out, err)
}

func (s *Service) updateUser(w http.ResponseWriter, r *http.Request) {
	uid, err := httpx.PathUUID(r, "id")
	if err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	var req UpdateUserRequest
	if err := httpx.Decode(r, &req); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	if !validLocale(req.Locale) {
		respond(w, r, 0, nil, errs.Validation("invalid_locale", "invalid locale", errs.Field("locale", "invalid", "must be en or id")))
		return
	}
	if req.FullName != nil && strings.TrimSpace(*req.FullName) == "" {
		respond(w, r, 0, nil, errs.Validation("invalid_user", "invalid user", errs.Field("fullName", "required", "full name is required")))
		return
	}
	ctx := r.Context()
	var out User
	err = s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		before, err := s.getUserTx(ctx, tx, uid, true)
		if err != nil {
			return err
		}
		if err := guardTarget(ctx, before); err != nil {
			return err
		}
		p := authz.From(ctx)
		if _, err := tx.Exec(ctx, `
			UPDATE platform.users SET full_name = coalesce($2, full_name), phone = coalesce($3, phone),
			  employee_id = coalesce($4, employee_id), locale = coalesce($5, locale), updated_by = $6 WHERE id = $1`,
			uid, req.FullName, req.Phone, req.EmployeeID, req.Locale, id.Ptr(p.UserID)); err != nil {
			if dbtx.IsForeignKeyViolation(err) {
				return errs.Validation("employee_invalid", "employee not found", errs.Field("employeeId", "invalid", "employee not found"))
			}
			if ok, _ := dbtx.IsUniqueViolation(err); ok {
				return errs.Validation("employee_taken", "employee already linked", errs.Field("employeeId", "taken", "this employee is linked to another user"))
			}
			return err
		}
		out, err = s.getUserTx(ctx, tx, uid, false)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionUpdate, EntityType: "platform.user",
			EntityID: uid.String(), EntityLabel: out.FullName, Before: before, After: out})
	})
	respond(w, r, http.StatusOK, out, err)
}

func (s *Service) setUserStatus(status string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, err := httpx.PathUUID(r, "id")
		if err != nil {
			respond(w, r, 0, nil, err)
			return
		}
		var req ReasonRequest
		if err := httpx.Decode(r, &req); err != nil {
			respond(w, r, 0, nil, err)
			return
		}
		ctx := r.Context()
		p := authz.From(ctx)
		if uid == p.UserID && status == "inactive" {
			respond(w, r, 0, nil, errs.Conflict("cannot_deactivate_self", "you cannot deactivate your own account"))
			return
		}
		var out User
		err = s.DB.WithTx(ctx, func(tx pgx.Tx) error {
			before, err := s.getUserTx(ctx, tx, uid, true)
			if err != nil {
				return err
			}
			if err := guardTarget(ctx, before); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE platform.users SET status = $2, updated_by = $3,
				locked_until = CASE WHEN $2 = 'active' THEN NULL ELSE locked_until END,
				failed_login_count = CASE WHEN $2 = 'active' THEN 0 ELSE failed_login_count END WHERE id = $1`,
				uid, status, id.Ptr(p.UserID)); err != nil {
				return err
			}
			if status == "inactive" {
				if _, err := tx.Exec(ctx, `UPDATE platform.sessions SET revoked_at = now(), revoked_reason = 'user_deactivated' WHERE user_id = $1 AND revoked_at IS NULL`, uid); err != nil {
					return err
				}
			}
			out, err = s.getUserTx(ctx, tx, uid, false)
			if err != nil {
				return err
			}
			return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionStatusChange, EntityType: "platform.user",
				EntityID: uid.String(), EntityLabel: out.FullName, Before: map[string]any{"status": before.Status},
				After: map[string]any{"status": status}, Reason: req.Reason})
		})
		respond(w, r, http.StatusOK, out, err)
	}
}

func (s *Service) resetMFA(w http.ResponseWriter, r *http.Request) {
	uid, err := httpx.PathUUID(r, "id")
	if err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	var req ReasonRequest
	if err := httpx.Decode(r, &req); err != nil {
		respond(w, r, 0, nil, err)
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		respond(w, r, 0, nil, errs.Validation("reason_required", "reason is required", errs.Field("reason", "required", "reason is required")))
		return
	}
	ctx := r.Context()
	err = s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		u, err := s.getUserTx(ctx, tx, uid, true)
		if err != nil {
			return err
		}
		if err := guardTarget(ctx, u); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.users SET mfa_enabled = false, mfa_secret_enc = NULL, mfa_pending_secret_enc = NULL WHERE id = $1`, uid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.sessions SET revoked_at = now(), revoked_reason = 'mfa_reset' WHERE user_id = $1 AND revoked_at IS NULL`, uid); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionMFAReset, Category: audit.CategorySecurity,
			EntityType: "platform.user", EntityID: uid.String(), EntityLabel: u.FullName, Reason: req.Reason})
	})
	respond(w, r, 0, nil, err)
}

func (s *Service) setPIN(self bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		p := authz.From(ctx)
		uid := p.UserID
		if !self {
			var err error
			if uid, err = httpx.PathUUID(r, "id"); err != nil {
				respond(w, r, 0, nil, err)
				return
			}
		}
		var req SetPINRequest
		if err := httpx.Decode(r, &req); err != nil {
			respond(w, r, 0, nil, err)
			return
		}
		if err := password.CheckPIN(req.PIN); err != nil {
			respond(w, r, 0, nil, err)
			return
		}
		h, err := password.Hash(req.PIN)
		if err != nil {
			respond(w, r, 0, nil, err)
			return
		}
		err = s.DB.WithTx(ctx, func(tx pgx.Tx) error {
			label := p.Name
			if !self {
				u, err := s.getUserTx(ctx, tx, uid, true)
				if err != nil {
					return err
				}
				if err := guardTarget(ctx, u); err != nil {
					return err
				}
				label = u.FullName
			}
			if _, err := tx.Exec(ctx, `UPDATE platform.users SET pin_hash = $2 WHERE id = $1`, uid, h); err != nil {
				return err
			}
			return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "pin_changed", Category: audit.CategorySecurity,
				EntityType: "platform.user", EntityID: uid.String(), EntityLabel: label})
		})
		respond(w, r, 0, nil, err)
	}
}
