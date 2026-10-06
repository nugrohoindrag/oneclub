package hris

// Certification enforcement (FR-TRC-03, contract H7): a holder working in a
// workforce role (lifeguard, caddy, food handler …) or a position that
// requires certifications must hold a valid certificate of every mandatory
// type on the working day. Scheduling (EP-06) calls CheckEmployee; golf
// caddy assignment and sport club instructor schedules reach CheckPartner
// through hooks wired by internal/app (business lines never import hris).
//
// The HR Configuration certificationEnforcement decides what fails:
// "expired" (default) refuses holders whose certificate of a mandatory type
// expired or was revoked without a valid one; "required" also refuses
// holders without any certificate of the type; "off" checks nothing.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
)

// Holder kinds.
const (
	HolderEmployee   = "employee"
	HolderCaddy      = "caddy"
	HolderInstructor = "instructor"
)

// CertificationGap is a mandatory certification a holder lacks on a day.
type CertificationGap struct {
	TypeCode  string  `json:"typeCode"`
	TypeName  string  `json:"typeName"`
	Reason    string  `json:"reason" enum:"missing,expired"`
	ExpiredOn *string `json:"expiredOn,omitempty"`
}

// CertificationCheck is the result of a check.
type CertificationCheck struct {
	Required []string           `json:"required" doc:"Mandatory certification type codes"`
	Gaps     []CertificationGap `json:"gaps"`
}

// OK reports whether the holder may work.
func (c CertificationCheck) OK() bool { return len(c.Gaps) == 0 }

// Err returns the conflict refusing the assignment (nil when OK).
func (c CertificationCheck) Err(holderName string) error {
	if c.OK() {
		return nil
	}
	var parts []string
	for _, g := range c.Gaps {
		if g.Reason == "expired" && g.ExpiredOn != nil {
			parts = append(parts, fmt.Sprintf("%s expired on %s", g.TypeName, *g.ExpiredOn))
		} else {
			parts = append(parts, g.TypeName+" missing")
		}
	}
	return errs.Conflict("certification_invalid", holderName+" has no valid mandatory certification: "+strings.Join(parts, ", "))
}

type requiredType struct {
	id         uuid.UUID
	code, name string
}

// requiredTypes returns the active certification types mandatory for a
// workforce role plus the extra type codes (position requirements).
func requiredTypes(ctx context.Context, q dbtx.Querier, role string, extra []string) ([]requiredType, error) {
	rows, err := q.Query(ctx, `SELECT id, code, name FROM hris.certification_types WHERE archived_at IS NULL AND status = 'active'
		AND (($1 <> '' AND $1 = ANY (mandatory_for)) OR code = ANY ($2)) ORDER BY code`, role, nonNil(extra))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []requiredType
	for rows.Next() {
		var t requiredType
		if err := rows.Scan(&t.id, &t.code, &t.name); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// check evaluates one holder against the required types.
func check(ctx context.Context, q dbtx.Querier, mode, kind string, holderID uuid.UUID, types []requiredType, day time.Time) (CertificationCheck, error) {
	out := CertificationCheck{Required: []string{}, Gaps: []CertificationGap{}}
	for _, t := range types {
		out.Required = append(out.Required, t.code)
	}
	if mode == "off" || len(types) == 0 {
		return out, nil
	}
	d := day.Format("2006-01-02")
	for _, t := range types {
		var valid, has bool
		var lastExpiry *string
		err := q.QueryRow(ctx, `SELECT
			  coalesce(bool_or(status <> 'revoked' AND (issued_on IS NULL OR issued_on <= $4::date) AND (expires_on IS NULL OR expires_on >= $4::date)), false),
			  count(*) > 0, to_char(max(expires_on) FILTER (WHERE expires_on < $4::date), 'YYYY-MM-DD')
			FROM hris.certifications WHERE certification_type_id = $1 AND archived_at IS NULL AND holder_kind = $2
			  AND (CASE WHEN $2 = 'employee' THEN employee_id ELSE partner_id END) = $3`, t.id, kind, holderID, d).Scan(&valid, &has, &lastExpiry)
		if err != nil {
			return out, err
		}
		switch {
		case valid:
		case has:
			out.Gaps = append(out.Gaps, CertificationGap{TypeCode: t.code, TypeName: t.name, Reason: "expired", ExpiredOn: lastExpiry})
		case mode == "required":
			out.Gaps = append(out.Gaps, CertificationGap{TypeCode: t.code, TypeName: t.name, Reason: "missing"})
		}
	}
	return out, nil
}

func enforcement(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time) (string, error) {
	cfg, _, err := LoadHRConfiguration(ctx, q, property, PolicyTime(day))
	if err != nil {
		return "", err
	}
	if !slices.Contains([]string{"expired", "required", "off"}, cfg.CertificationEnforcement) {
		return "expired", nil
	}
	return cfg.CertificationEnforcement, nil
}

// CheckEmployee checks an employee for a working day. role overrides the
// workforce role of the employee's position ("" = the position's role); the
// certifications the position requires always apply.
func CheckEmployee(ctx context.Context, q dbtx.Querier, employeeID uuid.UUID, role string, day time.Time) (CertificationCheck, error) {
	var property uuid.UUID
	var posRole *string
	var extra []string
	if err := q.QueryRow(ctx, `SELECT e.property_id, p.workforce_role, coalesce(p.required_certifications, '{}') FROM hris.employees e
		LEFT JOIN hris.positions p ON p.id = e.position_id WHERE e.id = $1`, employeeID).Scan(&property, &posRole, &extra); err != nil {
		if dbtx.IsNoRows(err) {
			return CertificationCheck{}, errs.NotFound("employee")
		}
		return CertificationCheck{}, err
	}
	if role == "" && posRole != nil {
		role = *posRole
	}
	mode, err := enforcement(ctx, q, property, day)
	if err != nil {
		return CertificationCheck{}, err
	}
	types, err := requiredTypes(ctx, q, role, extra)
	if err != nil {
		return CertificationCheck{}, err
	}
	return check(ctx, q, mode, HolderEmployee, employeeID, types, day)
}

// CheckPartner checks a caddy (golf caddy id) or an instructor (sport club
// instructor id) for a working day; role defaults to the kind. An
// instructor who is an employee is checked through employeeID when given.
func CheckPartner(ctx context.Context, q dbtx.Querier, property uuid.UUID, kind string, partnerID uuid.UUID, role string, day time.Time) (CertificationCheck, error) {
	if role == "" {
		role = kind
	}
	mode, err := enforcement(ctx, q, property, day)
	if err != nil {
		return CertificationCheck{}, err
	}
	types, err := requiredTypes(ctx, q, role, nil)
	if err != nil {
		return CertificationCheck{}, err
	}
	return check(ctx, q, mode, kind, partnerID, types, day)
}

// PartnersWithGaps returns the partners (of ids) that may not work on a
// day, e.g. caddies to hide from the Caddy Queue.
func PartnersWithGaps(ctx context.Context, q dbtx.Querier, property uuid.UUID, kind string, ids []uuid.UUID, day time.Time) (map[uuid.UUID]CertificationCheck, error) {
	out := map[uuid.UUID]CertificationCheck{}
	if len(ids) == 0 {
		return out, nil
	}
	mode, err := enforcement(ctx, q, property, day)
	if err != nil {
		return nil, err
	}
	types, err := requiredTypes(ctx, q, kind, nil)
	if err != nil || mode == "off" || len(types) == 0 {
		return out, err
	}
	for _, id := range ids {
		c, err := check(ctx, q, mode, kind, id, types, day)
		if err != nil {
			return nil, err
		}
		if !c.OK() {
			out[id] = c
		}
	}
	return out, nil
}
