package sportclub

import (
	"context"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/handle"
)

// SportActivity is the Sport Club section of the Customer 360.
type SportActivity struct {
	EntriesLast90Days int             `json:"entriesLast90Days"`
	LastVisit         *time.Time      `json:"lastVisit"`
	Enrollments       []EnrolledClass `json:"enrollments"`
	ClassAttendance   int             `json:"classesAttended"`
}

type EnrolledClass struct {
	Program    string     `json:"program" db:"program"`
	Status     string     `json:"status" db:"status"`
	ValidUntil *time.Time `json:"validUntil" db:"valid_until"`
}

// CustomerSection is the Sport Club part of the Customer 360.
func (m *Module) CustomerSection(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (any, error) {
	a := SportActivity{}
	if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE visit_date > current_date - 90)::int, max(used_at) FROM sportclub.entries
		WHERE (customer_id = $1 OR host_customer_id = $1) AND status <> 'cancelled'`, customer).Scan(&a.EntriesLast90Days, &a.LastVisit); err != nil {
		return a, err
	}
	if err := q.QueryRow(ctx, `SELECT count(*)::int FROM sportclub.session_bookings WHERE customer_id = $1 AND status = 'present'`, customer).Scan(&a.ClassAttendance); err != nil {
		return a, err
	}
	var err error
	a.Enrollments, err = handle.List[EnrolledClass](q.Query(ctx, `SELECT p.name AS program, e.status, e.valid_until FROM sportclub.enrollments e
		JOIN sportclub.class_programs p ON p.id = e.program_id WHERE e.customer_id = $1 ORDER BY e.created_at DESC`, customer))
	return a, err
}

// CustomerBehavior derives sport habits: visits and favourite facility.
func (m *Module) CustomerBehavior(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (crm.Behavior, error) {
	b := crm.Behavior{Facts: map[string]any{}, Highlights: []string{}}
	var visits int
	var fav *string
	if err := q.QueryRow(ctx, `SELECT count(*)::int, (SELECT f.name FROM sportclub.entries e2 JOIN sportclub.facilities f ON f.id = e2.facility_id
		WHERE e2.customer_id = $1 AND e2.status = 'used' GROUP BY f.name ORDER BY count(*) DESC LIMIT 1)
		FROM sportclub.entries WHERE customer_id = $1 AND status = 'used' AND visit_date > current_date - 90`, customer).Scan(&visits, &fav); err != nil {
		return b, err
	}
	b.Facts["visitsLast90Days"] = visits
	if fav != nil {
		b.Facts["favoriteFacility"] = *fav
		b.Highlights = append(b.Highlights, "Regular at "+*fav)
	}
	return b, nil
}
