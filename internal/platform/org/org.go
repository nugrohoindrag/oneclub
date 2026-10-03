// Package org owns Organization, Property, Venue, Department and Employee
// (EP-02, EP-04 FR-MD-01/02) and the Venue Activation reference flow
// (PRD §8 vertical slice).
package org

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/resource"
)

// CodeRe is the master data code pattern (upper case, digits, - and _).
var CodeRe = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)

const codeMsg = "1–20 characters: A–Z, 0–9, - or _"

func codeField() resource.Field {
	return resource.Field{Name: "code", Column: "code", Label: "Code", Kind: resource.String, Required: true, Max: 20,
		Upper: true, Pattern: CodeRe, PatternMsg: codeMsg, Search: true}
}

func statusField(values ...string) resource.Field {
	return resource.Field{Name: "status", Column: "status", Label: "Status", Kind: resource.Enum, Enum: values, Default: "active", Filter: true}
}

// Publisher publishes outbox events.
type Publisher interface {
	Publish(ctx context.Context, tx pgx.Tx, eventType, aggregateType string, aggregateID, propertyID *uuid.UUID, payload any) (uuid.UUID, error)
}

// FlagReader reads feature flags.
type FlagReader interface {
	FlagBool(ctx context.Context, q dbtx.Querier, key string) bool
}

// Module wires the org resources.
type Module struct {
	DB        *dbtx.DB
	Engine    *resource.Engine
	Approvals *approval.Engine
	Events    Publisher
	Flags     FlagReader
}

// VenueActivationFlag turns on the Venue Activation approval (PRD §8).
const VenueActivationFlag = "platform.venue_activation_approval"

// VenueActivationType is the approval document type of the reference flow.
var VenueActivationType = provision.DocumentType{Code: "venue_activation", Module: "platform", Name: "Venue Activation",
	Attributes: []provision.DocumentAttribute{{Key: "venueType", Label: "Venue Type", Type: "string"}}}

// Defs returns the resource definitions.
func (m *Module) Defs() []*resource.Def {
	properties := &resource.Def{
		Key: "platform.property", Module: "platform", Perm: "platform.property", Path: "/api/v1/platform/properties", Table: "platform.properties",
		Name: "Property", Plural: "Properties", Tag: "Organization", Archive: true, CodeField: "code", OrderBy: "name, id",
		Fields: []resource.Field{
			codeField(),
			{Name: "name", Column: "name", Label: "Name", Kind: resource.String, Required: true, Max: 120, Search: true},
			{Name: "address", Column: "address", Label: "Address", Kind: resource.Text, Max: 500},
			{Name: "city", Column: "city", Label: "City", Kind: resource.String, Max: 80, Filter: true},
			{Name: "phone", Column: "phone", Label: "Phone", Kind: resource.String, Max: 40},
			{Name: "email", Column: "email", Label: "E-mail", Kind: resource.Email, Max: 254},
			{Name: "timezone", Column: "timezone", Label: "Timezone (empty = instance timezone)", Kind: resource.String, Max: 64},
			statusField("active", "inactive"),
		},
		Hooks: resource.Hooks{BeforeWrite: func(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
			if tz, ok := v["timezone"].(string); ok && tz != "" {
				if _, err := time.LoadLocation(tz); err != nil {
					return errs.Validation("invalid_timezone", "invalid timezone", errs.Field("timezone", "invalid", "IANA timezone, e.g. Asia/Jakarta"))
				}
			}
			return nil
		}},
	}
	venues := &resource.Def{
		Key: "platform.venue", Module: "platform", Perm: "platform.venue", Path: "/api/v1/platform/venues", Table: "platform.venues",
		Name: "Venue", Plural: "Venues", Tag: "Organization", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
		Fields: []resource.Field{
			codeField(),
			{Name: "name", Column: "name", Label: "Name", Kind: resource.String, Required: true, Max: 120, Search: true},
			{Name: "venueType", Column: "venue_type", Label: "Venue Type", Kind: resource.Enum,
				Enum: []string{"golf", "sport", "stay", "banquet", "dining", "other"}, Default: "golf", Filter: true},
			{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 2000},
			statusField("pending", "active", "inactive", "rejected"),
		},
		Hooks: resource.Hooks{
			BeforeWrite: m.venueBeforeWrite,
			AfterCreate: m.venueAfterCreate,
		},
	}
	departments := &resource.Def{
		Key: "platform.department", Module: "platform", Perm: "platform.department", Path: "/api/v1/platform/departments", Table: "platform.departments",
		Name: "Department", Plural: "Departments", Tag: "Organization", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
		Fields: []resource.Field{
			codeField(),
			{Name: "name", Column: "name", Label: "Name", Kind: resource.String, Required: true, Max: 120, Search: true},
			{Name: "parentId", Column: "parent_id", Label: "Parent Department", Kind: resource.UUID, Filter: true,
				Ref: &resource.Ref{Table: "platform.departments", SameProperty: true, Label: "parent department"}},
			statusField("active", "inactive"),
		},
		Hooks: resource.Hooks{BeforeWrite: departmentNoCycle},
	}
	employees := &resource.Def{
		Key: "platform.employee", Module: "platform", Perm: "platform.employee", Path: "/api/v1/platform/employees", Table: "platform.employees",
		Name: "Employee", Plural: "Employees", Tag: "Organization", PropertyScoped: true, Archive: true, CodeField: "employeeNo", OrderBy: "full_name, id",
		Fields: []resource.Field{
			{Name: "employeeNo", Column: "employee_no", Label: "Employee No.", Kind: resource.String, Required: true, Max: 30, Upper: true, Search: true},
			{Name: "fullName", Column: "full_name", Label: "Full Name", Kind: resource.String, Required: true, Max: 120, Search: true},
			{Name: "departmentId", Column: "department_id", Label: "Department", Kind: resource.UUID, Filter: true,
				Ref: &resource.Ref{Table: "platform.departments", SameProperty: true, Label: "department"}},
			{Name: "jobTitle", Column: "job_title", Label: "Job Title", Kind: resource.String, Max: 120, Search: true},
			{Name: "email", Column: "email", Label: "E-mail", Kind: resource.Email, Max: 254},
			{Name: "phone", Column: "phone", Label: "Phone", Kind: resource.String, Max: 40},
			{Name: "joinDate", Column: "join_date", Label: "Join Date", Kind: resource.Date},
			statusField("active", "inactive"),
		},
	}
	return []*resource.Def{properties, venues, departments, employees}
}

// departmentNoCycle rejects a parent that is the department itself or one of
// its descendants.
func departmentNoCycle(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	parent, ok := v["parentId"].(string)
	if !ok || parent == "" || before == nil {
		return nil
	}
	self, _ := before["id"].(string)
	var cycle bool
	err := tx.QueryRow(ctx, `WITH RECURSIVE up AS (
		SELECT id, parent_id FROM platform.departments WHERE id = $1::uuid
		UNION ALL SELECT d.id, d.parent_id FROM platform.departments d JOIN up ON d.id = up.parent_id)
		SELECT EXISTS (SELECT 1 FROM up WHERE id = $2::uuid)`, parent, self).Scan(&cycle)
	if err != nil {
		return err
	}
	if cycle {
		return errs.Validation("department_cycle", "a department cannot be its own ancestor",
			errs.Field("parentId", "cycle", "choose a department that is not below this one"))
	}
	return nil
}

func (m *Module) activationRequired(ctx context.Context, tx pgx.Tx) bool {
	return m.Flags != nil && m.Flags.FlagBool(ctx, tx, VenueActivationFlag)
}

// venueBeforeWrite: with Venue Activation on, new venues start Pending and
// users cannot set a venue Active themselves while it is Pending.
func (m *Module) venueBeforeWrite(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	if !m.activationRequired(ctx, tx) {
		return nil
	}
	if before == nil {
		v["status"] = "pending"
		return nil
	}
	if st, ok := v["status"]; ok && before["status"] == "pending" && st != "pending" {
		return errs.Conflict("venue_pending_approval", "this venue is waiting for Venue Activation approval")
	}
	return nil
}

// venueAfterCreate submits the Venue Activation approval (PRD §8).
func (m *Module) venueAfterCreate(ctx context.Context, tx pgx.Tx, row map[string]any) error {
	if row["status"] != "pending" {
		return nil
	}
	vid, _ := uuid.Parse(row["id"].(string))
	pid, _ := uuid.Parse(row["propertyId"].(string))
	_, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{
		DocumentType: VenueActivationType.Code, DocumentID: vid, DocumentRef: fmt.Sprint(row["code"]),
		Title: "Activate venue " + fmt.Sprint(row["name"]), PropertyID: pid,
		Attributes: map[string]any{"venueType": row["venueType"]},
	})
	return err
}

// VenueDecision applies the approval outcome: Approve → Active and publish
// platform.venue_activated; Reject → Rejected; Cancel → stays Pending… then
// Inactive (the requester withdrew it).
func (m *Module) VenueDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	status := map[string]string{approval.StatusApproved: "active", approval.StatusRejected: "rejected", approval.StatusCancelled: "inactive"}[d.Status]
	if status == "" {
		return nil
	}
	var code, name, before string
	if err := tx.QueryRow(ctx, `SELECT code, name, status FROM platform.venues WHERE id = $1 FOR UPDATE`, d.DocumentID).Scan(&code, &name, &before); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.venues SET status = $2 WHERE id = $1`, d.DocumentID, status); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionStatusChange, EntityType: "platform.venue",
		EntityID: d.DocumentID.String(), EntityLabel: code + " · " + name, PropertyID: &d.PropertyID, Reason: d.Reason,
		Before: map[string]any{"status": before}, After: map[string]any{"status": status},
		Metadata: map[string]any{"approvalRequestId": d.RequestID}}); err != nil {
		return err
	}
	if status != "active" {
		return nil
	}
	_, err := m.Events.Publish(ctx, tx, "platform.venue_activated", "platform.venue", &d.DocumentID, &d.PropertyID, map[string]any{
		"venueId": d.DocumentID, "venueCode": code, "venueName": name, "propertyId": d.PropertyID, "approvalRequestId": d.RequestID,
	})
	return err
}

// ── Organization (FR-ORG-01) ──────────────────────────────────────────────

type Organization struct {
	ID          uuid.UUID  `json:"id"`
	LegalName   string     `json:"legalName"`
	DisplayName string     `json:"displayName"`
	NPWP        *string    `json:"npwp"`
	Address     *string    `json:"address"`
	City        *string    `json:"city"`
	Province    *string    `json:"province"`
	PostalCode  *string    `json:"postalCode"`
	Phone       *string    `json:"phone"`
	Email       *string    `json:"email" format:"email"`
	Website     *string    `json:"website"`
	LogoFileID  *uuid.UUID `json:"logoFileId"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

type OrganizationUpdate struct {
	LegalName   *string    `json:"legalName,omitempty"`
	DisplayName *string    `json:"displayName,omitempty"`
	NPWP        *string    `json:"npwp,omitempty" doc:"15 or 16 digits; punctuation allowed"`
	Address     *string    `json:"address,omitempty"`
	City        *string    `json:"city,omitempty"`
	Province    *string    `json:"province,omitempty"`
	PostalCode  *string    `json:"postalCode,omitempty"`
	Phone       *string    `json:"phone,omitempty"`
	Email       *string    `json:"email,omitempty"`
	Website     *string    `json:"website,omitempty"`
	LogoFileID  *uuid.UUID `json:"logoFileId,omitempty"`
}

const orgSelect = `SELECT id, legal_name, display_name, npwp, address, city, province, postal_code, phone, email, website, logo_file_id, updated_at FROM platform.organization`

func scanOrg(row pgx.Row) (Organization, error) {
	var o Organization
	err := row.Scan(&o.ID, &o.LegalName, &o.DisplayName, &o.NPWP, &o.Address, &o.City, &o.Province, &o.PostalCode, &o.Phone, &o.Email,
		&o.Website, &o.LogoFileID, &o.UpdatedAt)
	return o, err
}

func (m *Module) getOrg(w http.ResponseWriter, r *http.Request) {
	o, err := scanOrg(m.DB.Primary.QueryRow(r.Context(), orgSelect))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, o)
}

var npwpRe = regexp.MustCompile(`^\d{15,16}$`)

func (m *Module) updateOrg(w http.ResponseWriter, r *http.Request) {
	var req OrganizationUpdate
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var fields []errs.FieldError
	if req.NPWP != nil && *req.NPWP != "" {
		digits := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, *req.NPWP)
		if !npwpRe.MatchString(digits) {
			fields = append(fields, errs.Field("npwp", "invalid", "NPWP must have 15 or 16 digits"))
		}
	}
	if req.LegalName != nil && strings.TrimSpace(*req.LegalName) == "" {
		fields = append(fields, errs.Field("legalName", "required", "legal name is required"))
	}
	if req.DisplayName != nil && strings.TrimSpace(*req.DisplayName) == "" {
		fields = append(fields, errs.Field("displayName", "required", "display name is required"))
	}
	if req.Email != nil && *req.Email != "" && !strings.Contains(*req.Email, "@") {
		fields = append(fields, errs.Field("email", "invalid", "must be a valid e-mail address"))
	}
	if len(fields) > 0 {
		httpx.WriteError(w, r, errs.Validation("invalid_organization", "invalid organization", fields...))
		return
	}
	ctx := r.Context()
	var out Organization
	err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		before, err := scanOrg(tx.QueryRow(ctx, orgSelect+" FOR UPDATE"))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.organization SET legal_name = coalesce($1, legal_name), display_name = coalesce($2, display_name),
			npwp = coalesce($3, npwp), address = coalesce($4, address), city = coalesce($5, city), province = coalesce($6, province),
			postal_code = coalesce($7, postal_code), phone = coalesce($8, phone), email = coalesce($9, email), website = coalesce($10, website),
			logo_file_id = coalesce($11, logo_file_id), updated_at = now(), updated_by = $12`,
			req.LegalName, req.DisplayName, req.NPWP, req.Address, req.City, req.Province, req.PostalCode, req.Phone, req.Email, req.Website,
			req.LogoFileID, id.Ptr(authz.From(ctx).UserID)); err != nil {
			if dbtx.IsForeignKeyViolation(err) {
				return errs.Validation("invalid_logo", "logo file not found", errs.Field("logoFileId", "invalid", "file not found"))
			}
			return err
		}
		if out, err = scanOrg(tx.QueryRow(ctx, orgSelect)); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionUpdate, EntityType: "platform.organization",
			EntityID: out.ID.String(), EntityLabel: out.DisplayName, Before: before, After: out})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// Register adds organization routes and resource definitions.
func (m *Module) Register(reg *route.Registry) {
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/organization", Module: "platform", Tag: "Organization",
		Summary: "Organization profile", Permission: "platform.organization.view", Response: Organization{}, Handler: m.getOrg})
	reg.Add(route.Route{Method: http.MethodPatch, Path: "/api/v1/platform/organization", Module: "platform", Tag: "Organization",
		Summary: "Edit organization profile", Permission: "platform.organization.update", Request: OrganizationUpdate{}, Response: Organization{}, Handler: m.updateOrg})
	for _, d := range m.Defs() {
		m.Engine.Register(reg, d)
	}
}
