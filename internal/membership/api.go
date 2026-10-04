package membership

// Public interface of Membership (Technical Doc §4.2) used by golf for
// Player Validation (FR-FLT-03), Member Rate and guest privileges
// (FR-MEM-10) and member card check-in (FR-CHK-01).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
)

// Privileges are the golf rights of a membership type (FR-MEM-02/10).
type Privileges struct {
	MemberRate        bool `json:"memberRate"`
	GolfAccess        bool `json:"golfAccess"`
	MaxGuests         int  `json:"maxGuests"`
	BookingWindowDays int  `json:"bookingWindowDays"`
}

// Info is the membership standing of a member on a date.
type Info struct {
	MemberID     uuid.UUID  `json:"memberId"`
	MemberNo     string     `json:"memberNo"`
	Name         string     `json:"name"`
	CustomerID   *uuid.UUID `json:"customerId"`
	MembershipID *uuid.UUID `json:"membershipId"`
	TypeCode     string     `json:"typeCode"`
	TypeName     string     `json:"typeName"`
	Role         string     `json:"role"`
	Status       string     `json:"status" enum:"pending,active,expired,inactive,none"`
	StartsOn     *time.Time `json:"startsOn"`
	EndsOn       *time.Time `json:"endsOn"`
	Privileges   Privileges `json:"privileges"`
}

// Active reports an Active membership valid on the date.
func (i Info) Active() bool { return i.Status == "active" }

// Standing returns the best membership of a member valid on day: an Active
// membership in period wins; otherwise the latest one with its status
// (Expired / Inactive) so callers can explain the refusal.
func Standing(ctx context.Context, q dbtx.Querier, memberID uuid.UUID, day time.Time) (Info, error) {
	var in Info
	err := q.QueryRow(ctx, `SELECT m.id, m.code, m.name, m.customer_id FROM membership.members m WHERE m.id = $1`, memberID).
		Scan(&in.MemberID, &in.MemberNo, &in.Name, &in.CustomerID)
	if dbtx.IsNoRows(err) {
		return in, errs.NotFound("member")
	}
	if err != nil {
		return in, err
	}
	d := day.Format("2006-01-02")
	var mid uuid.UUID
	var st string
	err = q.QueryRow(ctx, `SELECT ms.id, ms.role, ms.starts_on, ms.ends_on,
			CASE WHEN ms.status = 'active' AND (ms.ends_on IS NOT NULL AND ms.ends_on < $2::date) THEN 'expired'
			     WHEN ms.status = 'active' AND ms.starts_on > $2::date THEN 'pending' ELSE ms.status END,
			t.code, t.name, t.member_rate, t.golf_access, t.max_guests, t.booking_window_days
		FROM membership.memberships ms JOIN membership.types t ON t.id = ms.type_id
		WHERE ms.member_id = $1
		ORDER BY (ms.status = 'active' AND ms.starts_on <= $2::date AND (ms.ends_on IS NULL OR ms.ends_on >= $2::date)) DESC,
		         ms.starts_on DESC LIMIT 1`, memberID, d).
		Scan(&mid, &in.Role, &in.StartsOn, &in.EndsOn, &st, &in.TypeCode, &in.TypeName, &in.Privileges.MemberRate, &in.Privileges.GolfAccess,
			&in.Privileges.MaxGuests, &in.Privileges.BookingWindowDays)
	if dbtx.IsNoRows(err) {
		in.Status = "none"
		return in, nil
	}
	if err != nil {
		return in, err
	}
	in.MembershipID, in.Status = &mid, st
	return in, nil
}

// MemberByCustomer returns the member id of a customer, if any.
func MemberByCustomer(ctx context.Context, q dbtx.Querier, customerID uuid.UUID) (*uuid.UUID, error) {
	var mid uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM membership.members WHERE customer_id = $1 AND archived_at IS NULL ORDER BY created_at LIMIT 1`, customerID).Scan(&mid)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &mid, nil
}

// MemberByNumber resolves a member number (or a Rhapsody card number).
func MemberByNumber(ctx context.Context, q dbtx.Querier, property uuid.UUID, number string) (*uuid.UUID, error) {
	var mid uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM membership.members WHERE property_id = $1 AND upper(code) = upper($2)
		UNION ALL SELECT member_id FROM membership.cards WHERE property_id = $1 AND (upper(card_number) = upper($2) OR upper(legacy_number) = upper($2)) AND status = 'active'
		LIMIT 1`, property, strings.TrimSpace(number)).Scan(&mid)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &mid, nil
}

// CardLookup resolves a scanned member card (digital QR token, physical
// card number or legacy Rhapsody number) to the member (FR-CHK-01).
func CardLookup(ctx context.Context, q dbtx.Querier, property uuid.UUID, scanned string) (*uuid.UUID, error) {
	s := strings.TrimSpace(scanned)
	s = strings.TrimPrefix(s, "oneclub:card:")
	var mid uuid.UUID
	err := q.QueryRow(ctx, `SELECT member_id FROM membership.cards WHERE property_id = $1 AND status = 'active'
		AND (qr_token = $2 OR upper(card_number) = upper($2) OR upper(legacy_number) = upper($2)) LIMIT 1`, property, s).Scan(&mid)
	if dbtx.IsNoRows(err) {
		return MemberByNumber(ctx, q, property, s)
	}
	if err != nil {
		return nil, err
	}
	return &mid, nil
}

// MemberByUser returns the member linked to a portal user (through the
// customer profile, or the member record itself).
func MemberByUser(ctx context.Context, q dbtx.Querier, userID uuid.UUID) (*uuid.UUID, error) {
	var mid uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM membership.members WHERE user_id = $1 AND archived_at IS NULL LIMIT 1`, userID).Scan(&mid)
	if err == nil {
		return &mid, nil
	}
	if !dbtx.IsNoRows(err) {
		return nil, err
	}
	c, err := crm.CustomerByUser(ctx, q, userID)
	if err != nil {
		return nil, nil //nolint:nilerr // a portal user without customer profile has no membership
	}
	return MemberByCustomer(ctx, q, c.ID)
}

// FamilyMemberIDs returns members covered by the same principal membership.
func FamilyMemberIDs(ctx context.Context, q dbtx.Querier, memberID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `WITH mine AS (SELECT coalesce(principal_id, id) AS root FROM membership.memberships WHERE member_id = $1 AND status = 'active')
		SELECT DISTINCT ms.member_id FROM membership.memberships ms, mine
		WHERE (ms.id = mine.root OR ms.principal_id = mine.root) AND ms.status = 'active' AND ms.member_id <> $1`, memberID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var x uuid.UUID
		if err := rows.Scan(&x); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ── eligibility (FR-MEM-03) ───────────────────────────────────────────────

// Eligibility are the rules of a membership type.
type Eligibility struct {
	MinAge       *int   `json:"minAge,omitempty"`
	MaxAge       *int   `json:"maxAge,omitempty"`
	Gender       string `json:"gender,omitempty"`        // male | female
	ResidentOnly bool   `json:"residentOnly,omitempty"`  // Modernland residents
	StudentOnly  bool   `json:"studentOnly,omitempty"`   // student status
	MaxChildAge  *int   `json:"maxChildAge,omitempty"`   // children ≤ age
	MaxChildren  *int   `json:"maxChildren,omitempty"`   // family composition
	RequireSpous bool   `json:"requireSpouse,omitempty"` // family membership needs a spouse
}

// Dependent is a family member on an application.
type Dependent struct {
	CustomerID   uuid.UUID `json:"customerId"`
	Relationship string    `json:"relationship" enum:"spouse,child,parent,sibling,other"`
	Student      bool      `json:"student,omitempty"`
}

// Check is one eligibility check result.
type Check struct {
	Rule    string `json:"rule"`
	Subject string `json:"subject"`
	Passed  bool   `json:"passed"`
	Message string `json:"message"`
}

// EligibilityResult is stored on the application.
type EligibilityResult struct {
	Eligible  bool      `json:"eligible"`
	Checks    []Check   `json:"checks"`
	CheckedAt time.Time `json:"checkedAt"`
}

func typeBeforeWrite(_ context.Context, _ pgx.Tx, v map[string]any, _ map[string]any) error {
	raw, ok := v["eligibility"]
	if !ok || raw == nil {
		return nil
	}
	var e Eligibility
	s, _ := raw.(string)
	if err := json.Unmarshal([]byte(s), &e); err != nil {
		return errs.Validation("invalid_eligibility", "invalid eligibility rules", errs.Field("eligibility", "invalid",
			"object with minAge, maxAge, gender, residentOnly, studentOnly, maxChildAge, maxChildren, requireSpouse"))
	}
	if e.Gender != "" && e.Gender != "male" && e.Gender != "female" {
		return errs.Validation("invalid_eligibility", "gender must be male or female", errs.Field("eligibility", "invalid", "gender must be male or female"))
	}
	for _, n := range []*int{e.MinAge, e.MaxAge, e.MaxChildAge, e.MaxChildren} {
		if n != nil && (*n < 0 || *n > 120) {
			return errs.Validation("invalid_eligibility", "ages and counts must be between 0 and 120", errs.Field("eligibility", "invalid", "0 … 120"))
		}
	}
	if e.MinAge != nil && e.MaxAge != nil && *e.MinAge > *e.MaxAge {
		return errs.Validation("invalid_eligibility", "minimum age is above the maximum age", errs.Field("eligibility", "invalid", "minAge ≤ maxAge"))
	}
	return nil
}

// Evaluate runs the eligibility rules of a type for an applicant and
// dependents on day. residentVerified reports a verified resident proof.
func Evaluate(rules Eligibility, applicant crm.Customer, dependents []Dependent, depProfiles map[uuid.UUID]crm.Customer, studentApplicant, residentVerified bool, day time.Time) EligibilityResult {
	res := EligibilityResult{Eligible: true, CheckedAt: day}
	add := func(rule, subject string, ok bool, msg string) {
		res.Checks = append(res.Checks, Check{Rule: rule, Subject: subject, Passed: ok, Message: msg})
		if !ok {
			res.Eligible = false
		}
	}
	age := applicant.Age(day)
	if rules.MinAge != nil {
		add("min_age", applicant.Name, age >= *rules.MinAge, fmt.Sprintf("age %d, minimum %d", age, *rules.MinAge))
	}
	if rules.MaxAge != nil {
		add("max_age", applicant.Name, age >= 0 && age <= *rules.MaxAge, fmt.Sprintf("age %d, maximum %d", age, *rules.MaxAge))
	}
	if rules.Gender != "" {
		add("gender", applicant.Name, applicant.Gender == rules.Gender, "requires "+rules.Gender)
	}
	if rules.ResidentOnly {
		add("resident", applicant.Name, applicant.Resident || residentVerified, "Modernland resident proof required")
	}
	if rules.StudentOnly {
		add("student", applicant.Name, studentApplicant, "student status required")
	}
	children, spouse := 0, false
	for _, d := range dependents {
		p := depProfiles[d.CustomerID]
		switch d.Relationship {
		case "child":
			children++
			if rules.MaxChildAge != nil {
				a := p.Age(day)
				add("max_child_age", p.Name, a >= 0 && a <= *rules.MaxChildAge, fmt.Sprintf("child age %d, maximum %d", a, *rules.MaxChildAge))
			}
		case "spouse":
			spouse = true
		}
	}
	if rules.MaxChildren != nil {
		add("max_children", applicant.Name, children <= *rules.MaxChildren, fmt.Sprintf("%d children, maximum %d", children, *rules.MaxChildren))
	}
	if rules.RequireSpous {
		add("require_spouse", applicant.Name, spouse, "family membership requires a spouse")
	}
	if res.Checks == nil {
		res.Checks = []Check{}
	}
	return res
}

// PeriodEnd returns the last day of a package period starting on start.
func PeriodEnd(start time.Time, unit string, count int) time.Time {
	if count <= 0 {
		count = 1
	}
	if unit == "month" {
		return start.AddDate(0, count, -1)
	}
	return start.AddDate(count, 0, -1)
}
