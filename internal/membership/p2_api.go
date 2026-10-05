package membership

// Contract C5 of PRD P2 §5.4.2 — membership status & entitlements read by
// Sport Club, Stay, POS and Golf — on P1's membership model: a customer's
// member record holds memberships of one or more programs (FR-MBL-01).

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/handle"
)

// Entitlements a membership type grants per business line (FR-MBL-05). The
// golf privileges of P1 (member rate, golf access, booking window, guests)
// are their defaults.
type Entitlements struct {
	MemberRate           bool     `json:"memberRate" doc:"Pricing segment member applies"`
	Segment              string   `json:"segment,omitempty" doc:"Pricing segment, default member"`
	BookingWindowDays    int      `json:"bookingWindowDays,omitempty"`
	GuestQuotaPerMonth   int      `json:"guestQuotaPerMonth,omitempty"`
	FacilityAccess       []string `json:"facilityAccess,omitempty" doc:"Facility types the member may enter (* = all)"`
	FreeEntry            bool     `json:"freeEntry,omitempty"`
	ClassDiscountPercent string   `json:"classDiscountPercent,omitempty"`
	Golf                 bool     `json:"golf,omitempty"`
	MemberCharge         bool     `json:"memberCharge,omitempty" doc:"May sign bills to the Member Account"`
}

// Active is one membership covering a customer.
type Active struct {
	MembershipID uuid.UUID    `json:"membershipId" db:"membership_id"`
	MemberNo     string       `json:"memberNo" db:"member_no"`
	ProgramCode  string       `json:"programCode" db:"program_code"`
	ProgramName  string       `json:"programName" db:"program_name"`
	ProgramKind  string       `json:"programKind" db:"program_kind" enum:"golf,sport_club,corporate,residence"`
	TypeCode     string       `json:"typeCode" db:"type_code"`
	TypeName     string       `json:"typeName" db:"type_name"`
	Role         string       `json:"role" db:"role" enum:"principal,family,nominee"`
	Status       string       `json:"status" db:"status"`
	StartsOn     time.Time    `json:"startsOn" db:"starts_on"`
	EndsOn       *time.Time   `json:"endsOn" db:"ends_on"`
	Raw          []byte       `json:"-" db:"entitlements"`
	MemberRate   bool         `json:"-" db:"member_rate"`
	GolfAccess   bool         `json:"-" db:"golf_access"`
	Window       int          `json:"-" db:"booking_window_days"`
	Entitlements Entitlements `json:"entitlements" db:"-"`
}

// programKind accepts P2's "sportclub" spelling of P1's sport_club.
func programKind(k string) string {
	if k == "sportclub" {
		return "sport_club"
	}
	return k
}

// Memberships returns every membership covering the customer (any status;
// an Active membership past its end date reads as expired).
func Memberships(ctx context.Context, q dbtx.Querier, property, customerID uuid.UUID) ([]Active, error) {
	list, err := handle.List[Active](q.Query(ctx, `SELECT ms.id AS membership_id, mb.code AS member_no, p.code AS program_code, p.name AS program_name,
		p.program_kind, t.code AS type_code, t.name AS type_name, ms.role,
		CASE WHEN ms.status = 'active' AND ms.ends_on < billing.local_date(ms.property_id) THEN 'expired' ELSE ms.status END AS status,
		ms.starts_on, ms.ends_on, t.entitlements, t.member_rate, t.golf_access, t.booking_window_days
		FROM membership.memberships ms JOIN membership.members mb ON mb.id = ms.member_id
		JOIN membership.types t ON t.id = ms.type_id JOIN membership.programs p ON p.id = t.program_id
		WHERE mb.customer_id = $1 AND ms.property_id = $2 ORDER BY p.name, ms.starts_on DESC`, customerID, property))
	for i := range list {
		a := &list[i]
		e := Entitlements{MemberRate: a.MemberRate, Golf: a.GolfAccess, BookingWindowDays: a.Window}
		_ = json.Unmarshal(a.Raw, &e)
		if e.Segment == "" {
			e.Segment = "member"
		}
		a.Entitlements = e
	}
	return list, err
}

// ActiveFor returns only the Active memberships: Paused, Suspended, Expired
// and Cancelled memberships grant no entitlement (FR-MBL-07, FR-MBL-10).
func ActiveFor(ctx context.Context, q dbtx.Querier, property, customerID uuid.UUID) ([]Active, error) {
	all, err := Memberships(ctx, q, property, customerID)
	out := []Active{}
	for _, a := range all {
		if a.Status == "active" {
			out = append(out, a)
		}
	}
	return out, err
}

// IsActive reports whether the customer holds an Active membership of a
// program kind ("" = any program).
func IsActive(ctx context.Context, q dbtx.Querier, property, customerID uuid.UUID, kind string) (bool, error) {
	act, err := ActiveFor(ctx, q, property, customerID)
	for _, a := range act {
		if kind == "" || a.ProgramKind == programKind(kind) {
			return true, err
		}
	}
	return false, err
}

// FacilityAccess reports whether an Active membership grants entry to a
// facility type, and whether the entry is free.
func FacilityAccess(ctx context.Context, q dbtx.Querier, property, customerID uuid.UUID, facilityType string) (allowed, free bool, err error) {
	act, err := ActiveFor(ctx, q, property, customerID)
	for _, a := range act {
		fa := a.Entitlements.FacilityAccess
		ok := slices.Contains(fa, facilityType) || slices.Contains(fa, "*") ||
			(len(fa) == 0 && (a.ProgramKind == "sport_club" || a.ProgramKind == "residence"))
		if ok {
			allowed = true
			free = free || a.Entitlements.FreeEntry
		}
	}
	return allowed, free, err
}

// Segment returns the pricing segment of the customer for a program kind
// ("member" when an Active membership grants Member Rate, else "").
func Segment(ctx context.Context, q dbtx.Querier, property, customerID uuid.UUID, kind string) (string, error) {
	act, err := ActiveFor(ctx, q, property, customerID)
	k := programKind(kind)
	for _, a := range act {
		if (k == "" || a.ProgramKind == k || a.ProgramKind == "residence" || a.ProgramKind == "corporate") && a.Entitlements.MemberRate {
			return a.Entitlements.Segment, err
		}
	}
	return "", err
}

// CardInfo is a member card resolved from a scan (QR token or card number).
type CardInfo struct {
	CardID       uuid.UUID  `json:"cardId" db:"card_id"`
	CardNumber   string     `json:"cardNumber" db:"card_number"`
	Status       string     `json:"status" db:"status"`
	MemberID     uuid.UUID  `json:"memberId" db:"member_id"`
	MemberNo     string     `json:"memberNo" db:"member_no"`
	CustomerID   *uuid.UUID `json:"customerId" db:"customer_id"`
	CustomerName string     `json:"customerName" db:"customer_name"`
	Memberships  []Active   `json:"memberships" db:"-"`
}

// LookupCard resolves a scanned card for facility access and check-in
// (digital QR token, physical or legacy Rhapsody number).
func LookupCard(ctx context.Context, q dbtx.Querier, property uuid.UUID, scanned string) (*CardInfo, error) {
	rows, err := q.Query(ctx, `SELECT c.id AS card_id, c.card_number, c.status, c.member_id, mb.code AS member_no, mb.customer_id, mb.name AS customer_name
		FROM membership.cards c JOIN membership.members mb ON mb.id = c.member_id
		WHERE c.property_id = $1 AND (c.qr_token = $2 OR upper(c.card_number) = upper($2) OR upper(c.legacy_number) = upper($2))
		ORDER BY (c.status = 'active') DESC LIMIT 1`, property, trimCardPrefix(scanned))
	ci, err := handle.One[CardInfo](rows, err, "member card")
	if err != nil {
		return nil, err
	}
	ci.Memberships = []Active{}
	if ci.CustomerID != nil {
		ci.Memberships, err = Memberships(ctx, q, property, *ci.CustomerID)
	}
	return &ci, err
}

func trimCardPrefix(s string) string {
	if len(s) > 13 && s[:13] == "oneclub:card:" {
		return s[13:]
	}
	return s
}

// CustomerSection is the membership part of the Customer 360: every program
// the customer belongs to, any status (FR-CRM-01).
func CustomerSection(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (any, error) {
	return Memberships(ctx, q, property, customer)
}

// SegmentFacts returns the Active membership types and program kinds of
// every member at a property (segmentation input, FR-CRM-03).
func SegmentFacts(ctx context.Context, q dbtx.Querier, property uuid.UUID) (types, programs map[uuid.UUID][]string, err error) {
	types, programs = map[uuid.UUID][]string{}, map[uuid.UUID][]string{}
	rows, err := q.Query(ctx, `SELECT mb.customer_id, t.code, p.program_kind FROM membership.memberships ms
		JOIN membership.members mb ON mb.id = ms.member_id JOIN membership.types t ON t.id = ms.type_id
		JOIN membership.programs p ON p.id = t.program_id
		WHERE ms.property_id = $1 AND ms.status = 'active' AND mb.customer_id IS NOT NULL`, property)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c uuid.UUID
		var t, p string
		if err := rows.Scan(&c, &t, &p); err != nil {
			return nil, nil, err
		}
		types[c] = append(types[c], t)
		programs[c] = append(programs[c], p)
	}
	return types, programs, rows.Err()
}

// HasMemberRate tells whether the customer has an active membership that
// grants the Member Rate (POS member prices, FR-POS-03).
func HasMemberRate(ctx context.Context, q dbtx.Querier, property, customerID uuid.UUID) (bool, error) {
	act, err := ActiveFor(ctx, q, property, customerID)
	for _, a := range act {
		if a.Entitlements.MemberRate {
			return true, err
		}
	}
	return false, err
}

// MemberNo returns the member number of a member ("" when unknown).
func MemberNo(ctx context.Context, q dbtx.Querier, memberID uuid.UUID) (string, error) {
	var no string
	err := q.QueryRow(ctx, `SELECT code FROM membership.members WHERE id = $1`, memberID).Scan(&no)
	if dbtx.IsNoRows(err) {
		return "", nil
	}
	return no, err
}

// MemberRef is an active member found by FindMembers.
type MemberRef struct {
	ID   uuid.UUID
	No   string
	Name string
}

// FindMembers returns up to 20 active members other than self: the given
// ids (e.g. family) and the members matching query by exact member number
// or by name (3 letters or more).
func FindMembers(ctx context.Context, q dbtx.Querier, property, self uuid.UUID, ids []uuid.UUID, query string) ([]MemberRef, error) {
	rows, err := q.Query(ctx, `SELECT id, code, name FROM membership.members WHERE property_id = $1 AND status = 'active' AND id <> $2
		AND (id = ANY($3) OR ($4 <> '' AND (upper(code) = upper($4) OR (length($4) >= 3 AND name ILIKE '%' || $4 || '%')))) ORDER BY name LIMIT 20`,
		property, self, ids, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemberRef
	for rows.Next() {
		var m MemberRef
		if err := rows.Scan(&m.ID, &m.No, &m.Name); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MaxBookingWindowDays is the longest golf booking window of the active
// membership types (nil without one).
func MaxBookingWindowDays(ctx context.Context, q dbtx.Querier, property uuid.UUID) (*int, error) {
	var mx *int
	err := q.QueryRow(ctx, `SELECT max(booking_window_days) FROM membership.types WHERE property_id = $1 AND status = 'active'`, property).Scan(&mx)
	return mx, err
}
