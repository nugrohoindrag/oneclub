package corehr

// Certifications of employees and partners (FR-TRC-02): holder, issuer and
// expiry from the certification type, renewal supersedes the previous
// certificate, revocation, and the compliance view of the holders of a
// workforce role (FR-TRC-03, FR-TRC-05).

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// certificationBeforeWrite resolves the holder, the issuer and the expiry.
func (m *Module) certificationBeforeWrite(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	if before != nil {
		if before["status"] == "revoked" || before["status"] == "renewed" {
			return errs.Conflict("certification_closed", "a revoked or renewed certificate cannot be changed")
		}
		if exp, ok := v["expiresOn"]; ok {
			if s, _ := exp.(string); s != "" {
				t, _ := time.Parse("2006-01-02", s)
				if !t.Before(today(ctx, tx, propertyOf(ctx, before))) && before["status"] == "expired" {
					v["status"] = "active"
				}
			}
		}
		return nil
	}
	kind, _ := v["holderKind"].(string)
	typeID, _ := v["certificationTypeId"].(string)
	var issuer *string
	var validity *int
	if err := tx.QueryRow(ctx, `SELECT issuer, validity_months FROM hris.certification_types WHERE id = $1::uuid AND archived_at IS NULL`, typeID).
		Scan(&issuer, &validity); err != nil {
		return handle.Invalid("certificationTypeId", "not_found", "certification type not found")
	}
	property := handle.Property(ctx)
	switch kind {
	case hris.HolderEmployee:
		emp, _ := v["employeeId"].(string)
		if emp == "" {
			return handle.Invalid("employeeId", "required", "choose the employee")
		}
		delete(v, "partnerId")
		var n string
		if err := tx.QueryRow(ctx, `SELECT full_name FROM hris.employees WHERE id = $1::uuid AND property_id = $2`, emp, property).Scan(&n); err != nil {
			return handle.Invalid("employeeId", "not_found", "employee not found")
		}
		v["holderName"] = n
	default:
		pid, _ := v["partnerId"].(string)
		if pid == "" {
			return handle.Invalid("partnerId", "required", "choose the "+kind)
		}
		delete(v, "employeeId")
		partner, _ := uuid.Parse(pid)
		n, err := m.partnerName(ctx, tx, property, kind, partner)
		if err != nil {
			return err
		}
		v["holderName"] = n
	}
	if s, _ := v["issuer"].(string); s == "" && issuer != nil {
		v["issuer"] = *issuer
	}
	if exp, _ := v["expiresOn"].(string); exp == "" && validity != nil {
		if iss, _ := v["issuedOn"].(string); iss != "" {
			t, _ := time.Parse("2006-01-02", iss)
			v["expiresOn"] = ymd(t.AddDate(0, *validity, -1))
		}
	}
	iss, _ := v["issuedOn"].(string)
	exp, _ := v["expiresOn"].(string)
	if iss != "" && exp != "" && exp < iss {
		return handle.Invalid("expiresOn", "invalid", "must be on or after the issue date")
	}
	return nil
}

// partnerName resolves a caddy / instructor through the directory wired by
// internal/app.
func (m *Module) partnerName(ctx context.Context, q pgx.Tx, property uuid.UUID, kind string, id uuid.UUID) (string, error) {
	if m.Partners == nil {
		return kind + " " + id.String()[:8], nil
	}
	n, err := m.Partners.PartnerName(ctx, q, property, kind, id)
	if err != nil {
		return "", err
	}
	if n == "" {
		return "", handle.Invalid("partnerId", "not_found", kind+" not found in this property")
	}
	return n, nil
}

// certificationAfterCreate marks the previous certificate of the same type
// and holder Renewed.
func (m *Module) certificationAfterCreate(ctx context.Context, tx pgx.Tx, row map[string]any) error {
	_, err := tx.Exec(ctx, `UPDATE hris.certifications o SET status = 'renewed', renewed_by_id = n.id
		FROM hris.certifications n WHERE n.id = $1::uuid AND o.id <> n.id AND o.certification_type_id = n.certification_type_id
		AND o.holder_kind = n.holder_kind AND o.employee_id IS NOT DISTINCT FROM n.employee_id AND o.partner_id IS NOT DISTINCT FROM n.partner_id
		AND o.status IN ('active', 'expired') AND o.archived_at IS NULL AND coalesce(o.expires_on, 'infinity') <= coalesce(n.expires_on, 'infinity')`,
		row["id"])
	return err
}

// CertificationView is a certification with its type and validity.
type CertificationView struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	TypeID       uuid.UUID  `json:"certificationTypeId" db:"certification_type_id"`
	TypeCode     string     `json:"typeCode" db:"type_code"`
	TypeName     string     `json:"typeName" db:"type_name"`
	HolderKind   string     `json:"holderKind" db:"holder_kind"`
	EmployeeID   *uuid.UUID `json:"employeeId" db:"employee_id"`
	PartnerID    *uuid.UUID `json:"partnerId" db:"partner_id"`
	HolderName   string     `json:"holderName" db:"holder_name"`
	CertNo       *string    `json:"certificateNo" db:"certificate_no"`
	Issuer       *string    `json:"issuer" db:"issuer"`
	IssuedOn     *time.Time `json:"issuedOn" db:"issued_on"`
	ExpiresOn    *time.Time `json:"expiresOn" db:"expires_on"`
	FileID       *uuid.UUID `json:"fileId" db:"file_id"`
	Status       string     `json:"status" db:"status" enum:"active,expired,renewed,revoked"`
	Validity     string     `json:"validity" db:"validity" enum:"valid,expiring,expired,revoked,renewed,no_expiry"`
	DaysLeft     *int       `json:"daysLeft" db:"days_left"`
	MandatoryFor []string   `json:"mandatoryFor" db:"mandatory_for"`
}

const certificationSelect = `SELECT c.id, c.certification_type_id, t.code AS type_code, t.name AS type_name, c.holder_kind, c.employee_id, c.partner_id,
	c.holder_name, c.certificate_no, c.issuer, c.issued_on, c.expires_on, c.file_id, c.status,
	CASE WHEN c.status IN ('revoked', 'renewed') THEN c.status WHEN c.expires_on IS NULL THEN 'no_expiry' WHEN c.expires_on < $1::date THEN 'expired'
	  WHEN c.expires_on <= $1::date + coalesce((SELECT max(d) FROM unnest(t.reminder_days) d), 60) THEN 'expiring' ELSE 'valid' END AS validity,
	(c.expires_on - $1::date) AS days_left, t.mandatory_for
	FROM hris.certifications c JOIN hris.certification_types t ON t.id = c.certification_type_id`

// ComplianceRow is one holder of a workforce role with the result of the
// mandatory certification check.
type ComplianceRow struct {
	HolderKind string                  `json:"holderKind"`
	HolderID   uuid.UUID               `json:"holderId"`
	HolderName string                  `json:"holderName"`
	OrgUnit    *string                 `json:"orgUnit"`
	Position   *string                 `json:"position"`
	Required   []string                `json:"required"`
	Gaps       []hris.CertificationGap `json:"gaps"`
	Compliant  bool                    `json:"compliant"`
}

func (m *Module) registerCertifications(reg *route.Registry) {
	tag := "HRIS Training & Certification"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/certification-status", Summary: "Certificates with validity (expiry follow-up)",
		Permission: "hris.certification.view", Response: CertificationView{}, List: true,
		Query: []route.Param{{Name: "holderKind", Enum: []string{"employee", "caddy", "instructor"}}, {Name: "employeeId"}, {Name: "partnerId"},
			{Name: "validity", Enum: []string{"valid", "expiring", "expired", "revoked", "renewed", "no_expiry"}}},
		Handler: listRead(m.DB, m.certificationStatusHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/certifications/{id}:revoke", Summary: "Revoke a certificate",
		Permission: "hris.certification.update", Request: ReasonRequest{}, Response: CertificationView{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.revokeHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/certification-compliance", Summary: "Mandatory certification compliance per workforce role",
		Permission: "hris.certification.view", Response: ComplianceRow{}, List: true,
		Query:   []route.Param{{Name: "role", Enum: hris.WorkforceRoles}, {Name: "date", Description: "YYYY-MM-DD (default today)"}},
		Handler: listRead(m.DB, m.complianceHTTP)})
}

func (m *Module) certificationStatusHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]CertificationView, error) {
	q := r.URL.Query()
	property := handle.Property(ctx)
	emp, err := handle.QueryUUID(r, "employeeId")
	if err != nil {
		return nil, err
	}
	partner, err := handle.QueryUUID(r, "partnerId")
	if err != nil {
		return nil, err
	}
	return handle.List[CertificationView](tx.Query(ctx, `SELECT * FROM (`+certificationSelect+` WHERE c.property_id = $2 AND c.archived_at IS NULL
		AND ($3 = '' OR c.holder_kind = $3) AND ($4::uuid IS NULL OR c.employee_id = $4) AND ($5::uuid IS NULL OR c.partner_id = $5)) x
		WHERE ($6 = '' OR validity = $6) ORDER BY expires_on NULLS LAST, holder_name LIMIT 1000`,
		ymd(today(ctx, tx, property)), property, q.Get("holderKind"), emp, partner, q.Get("validity")))
}

func (m *Module) loadCertification(ctx context.Context, tx pgx.Tx, cid uuid.UUID) (CertificationView, error) {
	property := handle.Property(ctx)
	return getOne[CertificationView]("certification")(tx.Query(ctx, certificationSelect+` WHERE c.id = $2 AND c.property_id = $3 AND c.archived_at IS NULL`,
		ymd(today(ctx, tx, property)), cid, property))
}

func (m *Module) revokeHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ReasonRequest) (CertificationView, error) {
	cid, err := handle.ID(r)
	if err != nil {
		return CertificationView{}, err
	}
	before, err := m.loadCertification(ctx, tx, cid)
	if err != nil {
		return before, err
	}
	if before.Status == "revoked" || before.Status == "renewed" {
		return before, errs.Conflict("certification_closed", "the certificate is already "+before.Status)
	}
	if strings.TrimSpace(req.Reason) == "" {
		return before, handle.Invalid("reason", "required", "a reason is required")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.certifications SET status = 'revoked', revoked_reason = $2, updated_by = $3 WHERE id = $1`, cid, req.Reason,
		actor(ctx)); err != nil {
		return before, err
	}
	after, err := m.loadCertification(ctx, tx, cid)
	if err != nil {
		return after, err
	}
	property := handle.Property(ctx)
	return after, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionStatusChange, EntityType: KeyCertification, EntityID: cid.String(),
		EntityLabel: after.TypeName + " · " + after.HolderName, PropertyID: &property, Reason: req.Reason,
		Before: map[string]any{"status": before.Status}, After: map[string]any{"status": after.Status}})
}

// complianceHTTP checks the employees of positions with the workforce role
// (or requiring certifications) and the partner holders of a role.
func (m *Module) complianceHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]ComplianceRow, error) {
	property := handle.Property(ctx)
	day, err := handle.QueryDate(r, "date", today(ctx, tx, property))
	if err != nil {
		return nil, err
	}
	role := r.URL.Query().Get("role")
	if role != "" && !oneOf(hris.WorkforceRoles, role) {
		return nil, errs.BadRequest("invalid_role", "unknown workforce role")
	}
	rows, err := tx.Query(ctx, `SELECT e.id, e.full_name, ou.name, p.name FROM hris.employees e LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id
		JOIN hris.positions p ON p.id = e.position_id
		WHERE e.property_id = $1 AND e.status = 'active' AND e.archived_at IS NULL
		  AND (($2 <> '' AND p.workforce_role = $2) OR ($2 = '' AND (p.workforce_role IS NOT NULL OR cardinality(p.required_certifications) > 0)))
		ORDER BY e.full_name`, property, role)
	if err != nil {
		return nil, err
	}
	type person struct {
		id             uuid.UUID
		name           string
		unit, position *string
	}
	var people []person
	for rows.Next() {
		var p person
		if err := rows.Scan(&p.id, &p.name, &p.unit, &p.position); err != nil {
			rows.Close()
			return nil, err
		}
		people = append(people, p)
	}
	rows.Close()
	out := []ComplianceRow{}
	for _, p := range people {
		chk, err := hris.CheckEmployee(ctx, tx, p.id, role, day)
		if err != nil {
			return nil, err
		}
		if len(chk.Required) == 0 {
			continue
		}
		out = append(out, ComplianceRow{HolderKind: hris.HolderEmployee, HolderID: p.id, HolderName: p.name, OrgUnit: p.unit, Position: p.position,
			Required: chk.Required, Gaps: chk.Gaps, Compliant: chk.OK()})
	}
	// partner holders with certificates on file (caddies, instructors)
	if role == "" || role == hris.HolderCaddy || role == hris.HolderInstructor {
		prow, err := tx.Query(ctx, `SELECT DISTINCT ON (holder_kind, partner_id) holder_kind, partner_id, holder_name FROM hris.certifications
			WHERE property_id = $1 AND holder_kind <> 'employee' AND archived_at IS NULL AND ($2 = '' OR holder_kind = $2)
			ORDER BY holder_kind, partner_id, created_at DESC`, property, role)
		if err != nil {
			return nil, err
		}
		type partner struct {
			kind string
			id   uuid.UUID
			name string
		}
		var ps []partner
		for prow.Next() {
			var p partner
			if err := prow.Scan(&p.kind, &p.id, &p.name); err != nil {
				prow.Close()
				return nil, err
			}
			ps = append(ps, p)
		}
		prow.Close()
		for _, p := range ps {
			chk, err := hris.CheckPartner(ctx, tx, property, p.kind, p.id, "", day)
			if err != nil {
				return nil, err
			}
			if len(chk.Required) == 0 {
				continue
			}
			out = append(out, ComplianceRow{HolderKind: p.kind, HolderID: p.id, HolderName: p.name, Required: chk.Required, Gaps: chk.Gaps,
				Compliant: chk.OK()})
		}
	}
	return out, nil
}
