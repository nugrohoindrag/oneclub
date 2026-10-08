package experience

// Advanced Caddy Lifecycle (PRD P2 EP-05) on P1's caddy core: clock-in /
// clock-out on the daily attendance, rotation, accept on the tablet, level
// promotion through approval, incidents, rating, favourites, history,
// utilisation and caddy fee settlement of P1's assignments and tips.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/crm"
	"oneclub/internal/golf"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// ── attendance & rotation (FR-CDL-03/04) ──────────────────────────────────

type ClockInput struct {
	CaddyID uuid.UUID  `json:"caddyId"`
	Shift   string     `json:"shift,omitempty" enum:"morning,afternoon,full_day"`
	At      *time.Time `json:"at,omitempty"`
}

// Attendance is a caddy's day: P1 attendance with the P2 shift and clock
// times.
type Attendance struct {
	CaddyID      uuid.UUID  `json:"caddyId" db:"caddy_id"`
	CaddyCode    string     `json:"caddyCode" db:"caddy_code"`
	CaddyName    string     `json:"caddyName" db:"caddy_name"`
	WorkDate     time.Time  `json:"workDate" db:"work_date"`
	Status       *string    `json:"status" db:"status" enum:"present,absent,leave"`
	QueueNo      *float64   `json:"queueNo" db:"queue_no"`
	Shift        *string    `json:"shift" db:"shift" enum:"morning,afternoon,full_day"`
	ClockedInAt  *time.Time `json:"clockedInAt" db:"clocked_in_at"`
	ClockedOutAt *time.Time `json:"clockedOutAt" db:"clocked_out_at"`
	Rounds       int        `json:"roundsToday" db:"rounds"`
}

// attendanceSelect: $1 = property, $2 = work date; caddies present in P1 or
// clocked in through P2.
const attendanceSelect = `SELECT c.id AS caddy_id, c.code AS caddy_code, c.name AS caddy_name, $2::date AS work_date, a.status, a.queue_no::float8 AS queue_no,
	s.shift, s.clocked_in_at, s.clocked_out_at,
	(SELECT count(*) FROM golf.caddy_assignments x WHERE x.caddy_id = c.id AND x.play_date = $2::date AND x.status IN ('assigned', 'in_play', 'completed'))::int AS rounds
	FROM golf.caddies c LEFT JOIN golf.caddy_attendance a ON a.caddy_id = c.id AND a.work_date = $2::date
	LEFT JOIN golf.caddy_shifts s ON s.caddy_id = c.id AND s.work_date = $2::date
	WHERE c.property_id = $1 AND (a.caddy_id IS NOT NULL OR s.caddy_id IS NOT NULL)`

func (m *Module) attendance(ctx context.Context, q dbtx.Querier, property, caddy uuid.UUID, day time.Time) (Attendance, error) {
	rows, err := q.Query(ctx, attendanceSelect+` AND c.id = $3`, property, day.Format("2006-01-02"), caddy)
	return handle.One[Attendance](rows, err, "attendance")
}

// ClockIn records arrival: P1 attendance Present (queue in arrival order,
// through P1's RecordAttendance) and the P2 clock-in with the shift.
func (m *Module) ClockIn(ctx context.Context, tx pgx.Tx, property uuid.UUID, in ClockInput) (Attendance, error) {
	at := eventTime(in.At)
	loc := location(ctx, tx, property)
	local := at.In(loc)
	day := localDay(at, loc)
	if in.Shift == "" {
		in.Shift = "morning"
		if local.Hour() >= 12 {
			in.Shift = "afternoon"
		}
	}
	var out *time.Time
	err := tx.QueryRow(ctx, `SELECT clocked_out_at FROM golf.caddy_shifts WHERE caddy_id = $1 AND work_date = $2::date`, in.CaddyID, day.Format("2006-01-02")).Scan(&out)
	switch {
	case err == nil && out == nil:
		return Attendance{}, errs.Conflict("already_clocked_in", "caddy has already clocked in today")
	case err == nil:
		return Attendance{}, errs.Conflict("already_clocked_out", "caddy has already clocked out today")
	case !dbtx.IsNoRows(err):
		return Attendance{}, err
	}
	if _, err := m.Golf.RecordAttendance(ctx, tx, property, golf.AttendanceRequest{Date: day.Format("2006-01-02"),
		Entries: []golf.AttendanceEntry{{CaddyID: in.CaddyID, Status: "present"}}}); err != nil {
		return Attendance{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.caddy_shifts (caddy_id, work_date, property_id, shift, clocked_in_at, created_by) VALUES ($1,$2::date,$3,$4,$5,$6)`,
		in.CaddyID, day.Format("2006-01-02"), property, in.Shift, at, actorPtr(ctx)); err != nil {
		return Attendance{}, err
	}
	a, err := m.attendance(ctx, tx, property, in.CaddyID, day)
	if err != nil {
		return a, err
	}
	return a, record(ctx, tx, "golf.caddy_shift", in.CaddyID, a.CaddyName+" "+day.Format("2006-01-02"), "clock_in", property, nil, a, "")
}

// ClockOut ends the day; a caddy with an open assignment cannot clock out.
// The caddy leaves the P2 rotation (P1 attendance stays Present).
func (m *Module) ClockOut(ctx context.Context, tx pgx.Tx, property uuid.UUID, in ClockInput) (Attendance, error) {
	var busy bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.caddy_assignments WHERE caddy_id = $1 AND property_id = $2 AND status IN ('assigned', 'in_play'))`,
		in.CaddyID, property).Scan(&busy); err != nil {
		return Attendance{}, err
	}
	if busy {
		return Attendance{}, errs.Conflict("caddy_on_duty", "caddy still has an assignment")
	}
	var day time.Time
	if err := tx.QueryRow(ctx, `UPDATE golf.caddy_shifts SET clocked_out_at = $3 WHERE (caddy_id, work_date) = (SELECT caddy_id, work_date FROM golf.caddy_shifts
		WHERE caddy_id = $1 AND property_id = $2 AND clocked_out_at IS NULL ORDER BY work_date DESC LIMIT 1) RETURNING work_date`,
		in.CaddyID, property, eventTime(in.At)).Scan(&day); err != nil {
		if dbtx.IsNoRows(err) {
			return Attendance{}, errs.Conflict("not_clocked_in", "caddy has not clocked in")
		}
		return Attendance{}, err
	}
	a, err := m.attendance(ctx, tx, property, in.CaddyID, day)
	if err != nil {
		return a, err
	}
	if err := realtimeBoards(ctx, tx, property, day); err != nil {
		return a, err
	}
	return a, record(ctx, tx, "golf.caddy_shift", in.CaddyID, a.CaddyName, "clock_out", property, nil, a, "")
}

// RotationEntry is one available caddy in the rotation (Caddy Master board).
type RotationEntry struct {
	Position       int        `json:"position"`
	CaddyID        uuid.UUID  `json:"caddyId" db:"caddy_id"`
	Code           string     `json:"code" db:"code"`
	Name           string     `json:"name" db:"name"`
	Level          *string    `json:"level" db:"level"`
	LevelRank      *int       `json:"levelRank" db:"level_rank"`
	QueueNo        float64    `json:"queueNo" db:"queue_no"`
	Rounds         int        `json:"roundsToday" db:"rounds"`
	LastAssignedAt *time.Time `json:"lastAssignedAt" db:"last_assigned_at"`
}

// Rotation returns the present, free caddies of a day (P1 attendance and
// queue) in the order of the Caddy Policies: arrival (fewest rounds today,
// then queue number), round_robin (longest since the last assignment) or
// level (the requested level first). Clocked-out caddies and caddies at the
// daily round limit are left out. Staff assign the caddy through P1.
func (m *Module) Rotation(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time, level *uuid.UUID) ([]RotationEntry, error) {
	pol, err := m.caddyPolicy(ctx, q, property)
	if err != nil {
		return nil, err
	}
	order := "rounds, queue_no"
	switch pol.Rotation {
	case "round_robin":
		order = "last_assigned_at NULLS FIRST, queue_no"
	case "level":
		order = "(level_id IS DISTINCT FROM $4::uuid), rounds, queue_no"
	}
	limit := pol.MaxRoundsPerDay
	if limit <= 0 {
		limit = 99
	}
	list, err := handle.List[RotationEntry](q.Query(ctx, `SELECT caddy_id, code, name, level, level_rank, queue_no, rounds, last_assigned_at FROM (
		SELECT a.caddy_id, c.code, c.name, p.level_id, l.name AS level, l.rank AS level_rank, coalesce(a.queue_no, 0)::float8 AS queue_no,
		  (SELECT count(*) FROM golf.caddy_assignments x WHERE x.caddy_id = c.id AND x.play_date = $2::date AND x.status IN ('assigned', 'in_play', 'completed'))::int AS rounds,
		  (SELECT max(x.assigned_at) FROM golf.caddy_assignments x WHERE x.caddy_id = c.id AND x.play_date = $2::date) AS last_assigned_at
		FROM golf.caddy_attendance a JOIN golf.caddies c ON c.id = a.caddy_id
		LEFT JOIN golf.caddy_profiles p ON p.caddy_id = c.id LEFT JOIN golf.caddy_levels l ON l.id = p.level_id
		LEFT JOIN golf.caddy_shifts s ON s.caddy_id = c.id AND s.work_date = a.work_date
		WHERE a.property_id = $1 AND a.work_date = $2::date AND a.status = 'present' AND s.clocked_out_at IS NULL AND c.status = 'active' AND c.archived_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM golf.caddy_assignments x WHERE x.caddy_id = c.id AND x.status IN ('assigned', 'in_play'))) r
		WHERE rounds < $3 AND ($4::uuid IS NULL OR true) ORDER BY `+order, property, day.Format("2006-01-02"), limit, level))
	for i := range list {
		list[i].Position = i + 1
	}
	return list, err
}

// caddyAssignment loads one P1 caddy assignment.
func (m *Module) caddyAssignment(ctx context.Context, q dbtx.Querier, aid uuid.UUID) (CaddyAssignment, uuid.UUID, error) {
	var property uuid.UUID
	if err := q.QueryRow(ctx, `SELECT property_id FROM golf.caddy_assignments WHERE id = $1`, aid).Scan(&property); err != nil {
		if dbtx.IsNoRows(err) {
			return CaddyAssignment{}, property, errs.NotFound("caddy assignment")
		}
		return CaddyAssignment{}, property, err
	}
	list, err := golf.ListCaddyAssignments(ctx, q, location(ctx, q, property), "a.id = $1", aid)
	if err != nil {
		return CaddyAssignment{}, property, err
	}
	if len(list) == 0 {
		return CaddyAssignment{}, property, errs.NotFound("caddy assignment")
	}
	return list[0], property, nil
}

// AcceptAssignment is the caddy's acknowledgement on the tablet (FR-TAB-01).
func (m *Module) AcceptAssignment(ctx context.Context, tx pgx.Tx, aid uuid.UUID) (CaddyAssignment, error) {
	a, property, err := m.caddyAssignment(ctx, tx, aid)
	if err != nil {
		return a, err
	}
	if err := m.ownAssignment(ctx, tx, a, property); err != nil {
		return a, err
	}
	if a.Status != "assigned" && a.Status != "in_play" {
		return a, errs.Conflict("invalid_status", "assignment is "+a.Status)
	}
	tag, err := tx.Exec(ctx, `INSERT INTO golf.caddy_assignment_acceptances (assignment_id, property_id, accepted_by) VALUES ($1,$2,$3)
		ON CONFLICT (assignment_id) DO NOTHING`, aid, property, actorPtr(ctx))
	if err != nil || tag.RowsAffected() == 0 {
		return a, err
	}
	return a, record(ctx, tx, "golf.caddy_assignment", aid, deref(a.BookingCode)+" · "+a.CaddyName, "accept", property, nil, map[string]any{"accepted": true}, "")
}

// ownAssignment: a caddy (tablet user) may only act on their own assignment;
// staff with golf.caddy_assignment.manage may act on any.
func (m *Module) ownAssignment(ctx context.Context, q dbtx.Querier, a CaddyAssignment, property uuid.UUID) error {
	var owner *uuid.UUID
	if err := q.QueryRow(ctx, `SELECT user_id FROM golf.caddy_profiles WHERE caddy_id = $1`, a.CaddyID).Scan(&owner); err != nil && !dbtx.IsNoRows(err) {
		return err
	}
	if owner != nil && *owner == handle.UserID(ctx) {
		return nil
	}
	if can(ctx, "golf.caddy_assignment.manage", property) {
		return nil
	}
	return errs.Forbidden("this is not your assignment")
}

// CaddyProfile is the P2 profile of a P1 caddy.
type CaddyProfile struct {
	CaddyID  uuid.UUID  `json:"caddyId" db:"caddy_id"`
	LevelID  *uuid.UUID `json:"levelId" db:"level_id"`
	Level    *string    `json:"level" db:"level"`
	UserID   *uuid.UUID `json:"userId" db:"user_id" doc:"Caddy Tablet login"`
	JoinedOn *time.Time `json:"joinedOn" db:"joined_on"`
	// BaseSalary is the monthly base salary (gaji pokok); the caddy fee per
	// assignment comes on top.
	BaseSalary *string `json:"baseSalary" db:"base_salary"`
}

// CaddyProfileInput sets the tablet login and joined date (the level
// changes only through promotion approval).
type CaddyProfileInput struct {
	UserID   *uuid.UUID `json:"userId,omitempty"`
	JoinedOn string     `json:"joinedOn,omitempty" doc:"YYYY-MM-DD"`
	LevelID  *uuid.UUID `json:"levelId,omitempty" doc:"Initial level only; later changes go through promotion"`
	// BaseSalary sets the monthly base salary ("" keeps it).
	BaseSalary string `json:"baseSalary,omitempty" doc:"Monthly base salary (gaji pokok)"`
}

const profileSelect = `SELECT c.id AS caddy_id, p.level_id, l.name AS level, p.user_id, p.joined_on, trim_scale(p.base_salary)::text AS base_salary FROM golf.caddies c
	LEFT JOIN golf.caddy_profiles p ON p.caddy_id = c.id LEFT JOIN golf.caddy_levels l ON l.id = p.level_id`

func (m *Module) CaddyProfile(ctx context.Context, q dbtx.Querier, property, caddy uuid.UUID) (CaddyProfile, error) {
	rows, err := q.Query(ctx, profileSelect+` WHERE c.id = $1 AND c.property_id = $2`, caddy, property)
	return handle.One[CaddyProfile](rows, err, "caddy")
}

func (m *Module) SetCaddyProfile(ctx context.Context, tx pgx.Tx, property, caddy uuid.UUID, in CaddyProfileInput) (CaddyProfile, error) {
	before, err := m.CaddyProfile(ctx, tx, property, caddy)
	if err != nil {
		return before, err
	}
	var joined *string
	if in.JoinedOn != "" {
		if _, err := time.Parse("2006-01-02", in.JoinedOn); err != nil {
			return before, handle.Invalid("joinedOn", "invalid_date", "joinedOn must be YYYY-MM-DD")
		}
		joined = &in.JoinedOn
	}
	var salary *string
	if in.BaseSalary != "" {
		v, err := decimal.NewFromString(in.BaseSalary)
		if err != nil || v.IsNegative() {
			return before, handle.Invalid("baseSalary", "invalid", "a non-negative amount")
		}
		s := v.String()
		salary = &s
	}
	if in.LevelID != nil && before.LevelID != nil && *before.LevelID != *in.LevelID {
		return before, errs.Conflict("level_change_needs_promotion", "the level changes through a promotion request")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.caddy_profiles (caddy_id, property_id, level_id, user_id, joined_on, base_salary, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5::date,$7::numeric,$6,$6) ON CONFLICT (caddy_id) DO UPDATE SET level_id = coalesce(golf.caddy_profiles.level_id, EXCLUDED.level_id),
		user_id = coalesce(EXCLUDED.user_id, golf.caddy_profiles.user_id), joined_on = coalesce(EXCLUDED.joined_on, golf.caddy_profiles.joined_on),
		base_salary = coalesce(EXCLUDED.base_salary, golf.caddy_profiles.base_salary), updated_by = EXCLUDED.updated_by`,
		caddy, property, in.LevelID, in.UserID, joined, actorPtr(ctx), salary); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return before, handle.Invalid("userId", "user_taken", "this user is already the tablet login of another caddy")
		}
		return before, err
	}
	after, err := m.CaddyProfile(ctx, tx, property, caddy)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.caddy_profile", caddy, "caddy profile", audit.ActionUpdate, property, before, after, "")
}

// ── promotion (FR-CDL-01/02) ──────────────────────────────────────────────

// Indicators are the eligibility indicators shown for a promotion.
type Indicators struct {
	Rounds        int      `json:"rounds"`
	AverageRating *string  `json:"averageRating"`
	Ratings       int      `json:"ratings"`
	Incidents12m  int      `json:"incidentsLast12Months"`
	MonthsActive  int      `json:"monthsActive"`
	Eligible      bool     `json:"eligible" doc:"Meets the target level thresholds (promotion still needs approval)"`
	Reasons       []string `json:"reasons"`
}

func (m *Module) indicators(ctx context.Context, q dbtx.Querier, caddy uuid.UUID, target *uuid.UUID) (Indicators, error) {
	var ind Indicators
	var joined *time.Time
	if err := q.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM golf.caddy_assignments WHERE caddy_id = $1 AND status IN ('completed', 'replaced'))::int,
		(SELECT trim_scale(round(avg(rating), 2))::text FROM golf.caddy_ratings WHERE caddy_id = $1),
		(SELECT count(*) FROM golf.caddy_ratings WHERE caddy_id = $1)::int,
		(SELECT count(*) FROM golf.incidents WHERE caddy_id = $1 AND subject_type = 'caddy' AND occurred_at > now() - interval '12 months')::int,
		(SELECT coalesce(p.joined_on, (c.created_at AT TIME ZONE coalesce((SELECT nullif(x.timezone, '') FROM platform.properties x WHERE x.id = c.property_id),
		  (SELECT timezone FROM platform.instance)))::date) FROM golf.caddies c LEFT JOIN golf.caddy_profiles p ON p.caddy_id = c.id WHERE c.id = $1)`,
		caddy).Scan(&ind.Rounds, &ind.AverageRating, &ind.Ratings, &ind.Incidents12m, &joined); err != nil {
		return ind, err
	}
	if joined != nil {
		n := localNow(ctx, q) // the club's month, as joined_on
		ind.MonthsActive = (n.Year()-joined.Year())*12 + int(n.Month()-joined.Month())
	}
	ind.Reasons = []string{}
	ind.Eligible = true
	if target != nil {
		var minRounds int
		var minRating string
		var maxInc *int
		if err := q.QueryRow(ctx, `SELECT min_rounds, min_rating::text, max_incidents FROM golf.caddy_levels WHERE id = $1`, *target).Scan(&minRounds, &minRating, &maxInc); err != nil {
			if dbtx.IsNoRows(err) {
				return ind, errs.NotFound("caddy level")
			}
			return ind, err
		}
		if ind.Rounds < minRounds {
			ind.Eligible = false
			ind.Reasons = append(ind.Reasons, "fewer rounds than required")
		}
		mr, _ := decimal.NewFromString(minRating)
		avg := decimal.Zero
		if ind.AverageRating != nil {
			avg, _ = decimal.NewFromString(*ind.AverageRating)
		}
		if mr.IsPositive() && avg.LessThan(mr) {
			ind.Eligible = false
			ind.Reasons = append(ind.Reasons, "average rating below the level minimum")
		}
		if maxInc != nil && ind.Incidents12m > *maxInc {
			ind.Eligible = false
			ind.Reasons = append(ind.Reasons, "too many incidents in the last 12 months")
		}
	}
	return ind, nil
}

type PromotionInput struct {
	ToLevelID uuid.UUID `json:"toLevelId"`
	Reason    string    `json:"reason"`
}

// LevelChange is a Level History entry.
type LevelChange struct {
	ID          uuid.UUID      `json:"id" db:"id"`
	CaddyID     uuid.UUID      `json:"caddyId" db:"caddy_id"`
	FromLevel   *string        `json:"fromLevel" db:"from_level"`
	ToLevel     string         `json:"toLevel" db:"to_level"`
	Status      string         `json:"status" db:"status" enum:"pending,approved,rejected"`
	Reason      string         `json:"reason" db:"reason"`
	Indicators  map[string]any `json:"indicators" db:"indicators"`
	ApprovalID  *uuid.UUID     `json:"approvalRequestId" db:"approval_request_id"`
	EffectiveAt *time.Time     `json:"effectiveAt" db:"effective_at"`
	CreatedAt   time.Time      `json:"createdAt" db:"created_at"`
}

const levelSelect = `SELECT h.id, h.caddy_id, fl.name AS from_level, tl.name AS to_level, h.status, h.reason, h.indicators, h.approval_request_id,
	h.effective_at, h.created_at FROM golf.caddy_level_history h LEFT JOIN golf.caddy_levels fl ON fl.id = h.from_level_id
	JOIN golf.caddy_levels tl ON tl.id = h.to_level_id`

// RequestPromotion submits a level change for approval; it is never
// automatic (PO §8). The level changes only when approved.
func (m *Module) RequestPromotion(ctx context.Context, tx pgx.Tx, property, caddy uuid.UUID, in PromotionInput) (LevelChange, error) {
	if err := handle.Required("reason", in.Reason); err != nil {
		return LevelChange{}, err
	}
	var name string
	var from *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT c.name, p.level_id FROM golf.caddies c LEFT JOIN golf.caddy_profiles p ON p.caddy_id = c.id WHERE c.id = $1 AND c.property_id = $2`, caddy, property).Scan(&name, &from); err != nil {
		if dbtx.IsNoRows(err) {
			return LevelChange{}, errs.NotFound("caddy")
		}
		return LevelChange{}, err
	}
	if from != nil && *from == in.ToLevelID {
		return LevelChange{}, handle.Invalid("toLevelId", "same_level", "caddy already has this level")
	}
	var pending bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.caddy_level_history WHERE caddy_id = $1 AND status = 'pending')`, caddy).Scan(&pending); err != nil {
		return LevelChange{}, err
	}
	if pending {
		return LevelChange{}, errs.Conflict("promotion_pending", "a level change is already waiting for approval")
	}
	ind, err := m.indicators(ctx, tx, caddy, &in.ToLevelID)
	if err != nil {
		return LevelChange{}, err
	}
	hid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO golf.caddy_level_history (id, property_id, caddy_id, from_level_id, to_level_id, reason, indicators, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, hid, property, caddy, from, in.ToLevelID, in.Reason, jsonOf(ind), actorPtr(ctx)); err != nil {
		return LevelChange{}, err
	}
	rid, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: PromotionType.Code, DocumentID: hid, DocumentRef: name,
		Title: "Caddy promotion · " + name, PropertyID: property, Attributes: map[string]any{"eligible": ind.Eligible, "rounds": ind.Rounds}})
	if err != nil {
		return LevelChange{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.caddy_level_history SET approval_request_id = $2 WHERE id = $1`, hid, rid); err != nil {
		return LevelChange{}, err
	}
	lc, err := handle.Get[LevelChange](tx.Query(ctx, levelSelect+` WHERE h.id = $1`, hid))
	if err != nil {
		return lc, err
	}
	return lc, record(ctx, tx, "golf.caddy_level_history", hid, name, audit.ActionCreate, property, nil, lc, in.Reason)
}

func (m *Module) promotionDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	st := map[string]string{approval.StatusApproved: "approved", approval.StatusRejected: "rejected", approval.StatusCancelled: "rejected"}[d.Status]
	var caddy, to uuid.UUID
	err := tx.QueryRow(ctx, `UPDATE golf.caddy_level_history SET status = $2, effective_at = CASE WHEN $2 = 'approved' THEN now() END
		WHERE id = $1 AND status = 'pending' RETURNING caddy_id, to_level_id`, d.DocumentID, st).Scan(&caddy, &to)
	if dbtx.IsNoRows(err) || st != "approved" {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.caddy_profiles (caddy_id, property_id, level_id) VALUES ($1,$3,$2)
		ON CONFLICT (caddy_id) DO UPDATE SET level_id = EXCLUDED.level_id`, caddy, to, d.PropertyID); err != nil {
		return err
	}
	return m.publish(ctx, tx, "golf.caddy_promoted", "golf.caddy", caddy, d.PropertyID, map[string]any{"caddyId": caddy, "levelId": to})
}

// ── incidents (FR-CDL-07, FR-CTL-06) ──────────────────────────────────────

type IncidentInput struct {
	SubjectType  string     `json:"subjectType" enum:"caddy,golf_cart"`
	CaddyID      *uuid.UUID `json:"caddyId,omitempty"`
	GolfCartID   *uuid.UUID `json:"golfCartId,omitempty"`
	FlightID     *uuid.UUID `json:"flightId,omitempty"`
	PlayerID     *uuid.UUID `json:"playerId,omitempty" doc:"Booking player"`
	CustomerID   *uuid.UUID `json:"customerId,omitempty"`
	Category     string     `json:"category" doc:"e.g. misconduct, late, damage, accident, lost_item"`
	Severity     string     `json:"severity,omitempty" enum:"low,medium,high,critical"`
	Description  string     `json:"description"`
	ActionTaken  string     `json:"actionTaken,omitempty"`
	Attachments  []string   `json:"attachments,omitempty" doc:"File URLs (uploaded through /api/v1/files)"`
	DamageAmount string     `json:"damageAmount,omitempty" doc:"Golf cart damage to charge to the player's folio (through approval)"`
	OccurredAt   *time.Time `json:"occurredAt,omitempty"`
}

// Incident is a caddy or golf cart incident.
type Incident struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	Number       string     `json:"number" db:"number"`
	SubjectType  string     `json:"subjectType" db:"subject_type"`
	CaddyID      *uuid.UUID `json:"caddyId" db:"caddy_id"`
	GolfCartID   *uuid.UUID `json:"golfCartId" db:"golf_cart_id"`
	FlightID     *uuid.UUID `json:"flightId" db:"flight_id"`
	PlayerID     *uuid.UUID `json:"playerId" db:"booking_player_id"`
	CustomerID   *uuid.UUID `json:"customerId" db:"customer_id"`
	Category     string     `json:"category" db:"category"`
	Severity     string     `json:"severity" db:"severity"`
	Description  string     `json:"description" db:"description"`
	ActionTaken  *string    `json:"actionTaken" db:"action_taken"`
	Attachments  []string   `json:"attachments" db:"attachments"`
	DamageAmount *string    `json:"damageAmount" db:"damage_amount"`
	DamageStatus *string    `json:"damageStatus" db:"damage_status" enum:"pending,approved,rejected,charged"`
	Status       string     `json:"status" db:"status" enum:"open,closed"`
	OccurredAt   time.Time  `json:"occurredAt" db:"occurred_at"`
}

const incidentSelect = `SELECT id, number, subject_type, caddy_id, golf_cart_id, flight_id, booking_player_id, customer_id, category, severity, description,
	action_taken, attachments, trim_scale(damage_amount)::text AS damage_amount, damage_status, status, occurred_at FROM golf.incidents`

// ReportIncident records an incident; golf cart damage goes through approval
// (Golf Cart Policies) before it is charged to the player's folio.
func (m *Module) ReportIncident(ctx context.Context, tx pgx.Tx, property uuid.UUID, in IncidentInput) (Incident, error) {
	if err := handle.Required("description", in.Description); err != nil {
		return Incident{}, err
	}
	if err := handle.Required("category", in.Category); err != nil {
		return Incident{}, err
	}
	switch in.SubjectType {
	case "caddy":
		if in.CaddyID == nil {
			return Incident{}, handle.Invalid("caddyId", "required", "caddy is required")
		}
	case "golf_cart":
		if in.GolfCartID == nil {
			return Incident{}, handle.Invalid("golfCartId", "required", "golf cart is required")
		}
	default:
		return Incident{}, handle.Invalid("subjectType", "invalid", "caddy or golf_cart")
	}
	if in.Severity == "" {
		in.Severity = "low"
	}
	dmg, err := handle.Decimal("damageAmount", in.DamageAmount, decimal.Zero)
	if err != nil {
		return Incident{}, err
	}
	if dmg.IsPositive() && in.PlayerID == nil {
		return Incident{}, handle.Invalid("playerId", "required", "a damage charge needs the player of the flight")
	}
	if in.PlayerID != nil {
		var flight uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT flight_id, customer_id FROM golf.booking_players WHERE id = $1 AND property_id = $2`, *in.PlayerID, property).
			Scan(&flight, &in.CustomerID); err != nil {
			if dbtx.IsNoRows(err) {
				return Incident{}, handle.Invalid("playerId", "not_found", "player not found")
			}
			return Incident{}, err
		}
		if in.FlightID == nil {
			in.FlightID = &flight
		}
	}
	no, err := number(ctx, tx, property, "INC")
	if err != nil {
		return Incident{}, err
	}
	if in.Attachments == nil {
		in.Attachments = []string{}
	}
	iid := id.New()
	var damage, dstatus *string
	if dmg.IsPositive() {
		s, p := dmg.String(), "pending"
		damage, dstatus = &s, &p
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.incidents (id, property_id, number, subject_type, caddy_id, golf_cart_id, flight_id, booking_player_id, customer_id,
		category, severity, description, action_taken, attachments, damage_amount, damage_status, occurred_at, reported_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::numeric,$16,$17,$18)`, iid, property, no, in.SubjectType, in.CaddyID, in.GolfCartID,
		in.FlightID, in.PlayerID, in.CustomerID, in.Category, in.Severity, in.Description, nullStr(in.ActionTaken), in.Attachments, damage, dstatus,
		eventTime(in.OccurredAt), actorPtr(ctx)); err != nil {
		return Incident{}, err
	}
	if dmg.IsPositive() {
		pol, err := m.cartPolicy(ctx, tx, property)
		if err != nil {
			return Incident{}, err
		}
		if pol.DamageChargeApproval {
			rid, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: DamageChargeType.Code, DocumentID: iid, DocumentRef: no,
				Title: "Golf cart damage charge · " + no, PropertyID: property, Attributes: map[string]any{"amount": dmg.InexactFloat64()}})
			if err != nil {
				return Incident{}, err
			}
			if _, err := tx.Exec(ctx, `UPDATE golf.incidents SET approval_request_id = $2 WHERE id = $1`, iid, rid); err != nil {
				return Incident{}, err
			}
		} else if err := m.chargeDamage(ctx, tx, iid); err != nil {
			return Incident{}, err
		}
	}
	inc, err := handle.Get[Incident](tx.Query(ctx, incidentSelect+` WHERE id = $1`, iid))
	if err != nil {
		return inc, err
	}
	return inc, record(ctx, tx, "golf.incident", iid, no, audit.ActionCreate, property, nil, inc, "")
}

func (m *Module) damageDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	st := map[string]string{approval.StatusApproved: "approved", approval.StatusRejected: "rejected", approval.StatusCancelled: "rejected"}[d.Status]
	tag, err := tx.Exec(ctx, `UPDATE golf.incidents SET damage_status = $2 WHERE id = $1 AND damage_status = 'pending'`, d.DocumentID, st)
	if err != nil || tag.RowsAffected() == 0 || st != "approved" {
		return err
	}
	return m.chargeDamage(ctx, tx, d.DocumentID)
}

// chargeDamage posts the golf cart damage to the booking folio of the player.
func (m *Module) chargeDamage(ctx context.Context, tx pgx.Tx, iid uuid.UUID) error {
	var amount, no string
	var folio *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT i.damage_amount::text, i.number, b.folio_id FROM golf.incidents i JOIN golf.booking_players bp ON bp.id = i.booking_player_id
		JOIN golf.bookings b ON b.id = bp.booking_id WHERE i.id = $1`, iid).Scan(&amount, &no, &folio); err != nil || folio == nil {
		if dbtx.IsNoRows(err) {
			return nil
		}
		return err
	}
	amt, _ := decimal.NewFromString(amount)
	lid, err := m.Billing.AddCharge(ctx, tx, billing.Charge{FolioID: *folio, ChargeType: "other",
		ReferenceType: "golf.incident", ReferenceID: &iid, Description: "Golf cart damage · " + no, Quantity: decimal.NewFromInt(1), UnitPrice: amt, Net: amt, Total: amt})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE golf.incidents SET damage_status = 'charged', folio_line_id = $2 WHERE id = $1`, iid, lid)
	return err
}

// ── rating & favourites (FR-CDL-06/09) ────────────────────────────────────

type RatingInput struct {
	Rating     int        `json:"rating" doc:"1–5"`
	Comment    string     `json:"comment,omitempty"`
	CustomerID *uuid.UUID `json:"customerId,omitempty" doc:"Staff entry on behalf of a player; the Member Portal uses the signed-in member"`
}

// RateCaddy stores the player's rating of the caddy after the round.
func (m *Module) RateCaddy(ctx context.Context, tx pgx.Tx, property, aid uuid.UUID, customer *uuid.UUID, rating int, comment, channel string) error {
	if rating < 1 || rating > 5 {
		return handle.Invalid("rating", "invalid_rating", "rating must be between 1 and 5")
	}
	a, _, err := m.caddyAssignment(ctx, tx, aid)
	if err != nil {
		return err
	}
	if a.Status != "completed" && a.Status != "replaced" {
		return errs.Conflict("round_not_finished", "the caddy can be rated after the round")
	}
	if customer != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.booking_players WHERE flight_id = $1 AND customer_id = $2)`, a.FlightID, *customer).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errs.Forbidden("only players of the flight can rate the caddy")
		}
	}
	tag, err := tx.Exec(ctx, `INSERT INTO golf.caddy_ratings (id, property_id, assignment_id, caddy_id, customer_id, rating, comment, channel)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (assignment_id, customer_id) DO NOTHING`, id.New(), property, aid, a.CaddyID, customer, rating, nullStr(comment), channel)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errs.Conflict("already_rated", "this caddy has already been rated for the round")
	}
	return record(ctx, tx, "golf.caddy_rating", aid, a.CaddyName, audit.ActionCreate, property, nil, map[string]any{"rating": rating, "channel": channel}, "")
}

// RegisterFeedbackSubject lets the round feedback survey rate the caddy.
func (m *Module) RegisterFeedbackSubject() {
	crm.RegisterSubject("golf.caddy_assignment", func(ctx context.Context, tx pgx.Tx, property uuid.UUID, customer *uuid.UUID, subject uuid.UUID, rating int, comment, channel string) error {
		return m.RateCaddy(ctx, tx, property, subject, customer, rating, comment, channel)
	})
}

// Favorite marks a caddy as a customer's favourite (Favorite Count) and
// records the preference in CRM.
func (m *Module) Favorite(ctx context.Context, tx pgx.Tx, property, caddy, customer uuid.UUID, on bool) error {
	var name, code string
	if err := tx.QueryRow(ctx, `SELECT name, code FROM golf.caddies WHERE id = $1 AND property_id = $2`, caddy, property).Scan(&name, &code); err != nil {
		if dbtx.IsNoRows(err) {
			return errs.NotFound("caddy")
		}
		return err
	}
	if !on {
		if _, err := tx.Exec(ctx, `DELETE FROM golf.caddy_favorites WHERE caddy_id = $1 AND customer_id = $2`, caddy, customer); err != nil {
			return err
		}
		prefs, err := crm.ListPreferences(ctx, tx, customer, false)
		if err != nil {
			return err
		}
		for _, p := range prefs {
			if p.Category == "favorite_caddy" && p.RefID != nil && *p.RefID == caddy {
				if err := crm.RemovePreference(ctx, tx, property, p.ID); err != nil {
					return err
				}
			}
		}
		return record(ctx, tx, "golf.caddy_favorite", caddy, name, audit.ActionDelete, property, map[string]any{"customerId": customer}, nil, "")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.caddy_favorites (property_id, caddy_id, customer_id) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`,
		property, caddy, customer); err != nil {
		return err
	}
	if _, err := crm.RecordPreference(ctx, tx, property, customer, crm.PreferenceInput{Category: "favorite_caddy", Value: "#" + code + " " + name,
		RefType: "golf.caddy", RefID: &caddy}, "member"); err != nil {
		return err
	}
	return record(ctx, tx, "golf.caddy_favorite", caddy, name, audit.ActionCreate, property, nil, map[string]any{"customerId": customer}, "")
}

// ── history & utilisation (FR-CDL-06/08) ──────────────────────────────────

// CaddyHistory is the round, assignment and customer history of a caddy.
type CaddyHistory struct {
	CaddyID         uuid.UUID         `json:"caddyId"`
	Rounds          int               `json:"rounds"`
	FavoriteCount   int               `json:"favoriteCount"`
	AverageRating   *string           `json:"averageRating"`
	Assignments     []CaddyAssignment `json:"assignments"`
	Customers       []CustomerServed  `json:"customers"`
	RepeatCustomers int               `json:"repeatCustomers"`
	Levels          []LevelChange     `json:"levelHistory"`
}

type CustomerServed struct {
	CustomerID uuid.UUID `json:"customerId" db:"customer_id"`
	Name       string    `json:"name" db:"name"`
	Rounds     int       `json:"rounds" db:"rounds"`
	LastRound  time.Time `json:"lastRound" db:"last_round"`
	Favorite   bool      `json:"favorite" db:"favorite"`
}

func (m *Module) CaddyHistory(ctx context.Context, q dbtx.Querier, property, caddy uuid.UUID, limit int) (CaddyHistory, error) {
	h := CaddyHistory{CaddyID: caddy}
	if err := q.QueryRow(ctx, `SELECT (SELECT count(*) FROM golf.caddy_assignments WHERE caddy_id = $1 AND status IN ('completed', 'replaced'))::int,
		(SELECT count(*) FROM golf.caddy_favorites WHERE caddy_id = $1)::int,
		(SELECT trim_scale(round(avg(rating), 2))::text FROM golf.caddy_ratings WHERE caddy_id = $1)`, caddy).Scan(&h.Rounds, &h.FavoriteCount, &h.AverageRating); err != nil {
		return h, err
	}
	var err error
	if h.Assignments, err = golf.ListCaddyAssignments(ctx, q, location(ctx, q, property), "a.caddy_id = $1", caddy); err != nil {
		return h, err
	}
	if limit > 0 && len(h.Assignments) > limit {
		h.Assignments = h.Assignments[:limit]
	}
	if h.Customers, err = handle.List[CustomerServed](q.Query(ctx, `SELECT bp.customer_id, max(bp.name) AS name, count(DISTINCT a.flight_id)::int AS rounds,
		max(lower(a.period)) AS last_round, EXISTS (SELECT 1 FROM golf.caddy_favorites fv WHERE fv.caddy_id = $1 AND fv.customer_id = bp.customer_id) AS favorite
		FROM golf.caddy_assignments a JOIN golf.booking_players bp ON bp.id = ANY(a.player_ids) AND bp.customer_id IS NOT NULL
		WHERE a.caddy_id = $1 AND a.status IN ('completed', 'replaced') GROUP BY bp.customer_id ORDER BY rounds DESC, name`, caddy)); err != nil {
		return h, err
	}
	for _, c := range h.Customers {
		if c.Rounds > 1 {
			h.RepeatCustomers++
		}
	}
	h.Levels, err = handle.List[LevelChange](q.Query(ctx, levelSelect+` WHERE h.caddy_id = $1 ORDER BY h.created_at DESC`, caddy))
	return h, err
}

// Utilization is round & duty statistics per caddy for a period.
type Utilization struct {
	CaddyID         uuid.UUID `json:"caddyId" db:"caddy_id"`
	Code            string    `json:"code" db:"code"`
	Name            string    `json:"name" db:"name"`
	DaysPresent     int       `json:"daysPresent" db:"days_present"`
	Rounds          int       `json:"rounds" db:"rounds"`
	RoundsPerDay    *string   `json:"roundsPerDay" db:"rounds_per_day"`
	DutyHours       string    `json:"dutyHours" db:"duty_hours"`
	OnCourseHours   string    `json:"onCourseHours" db:"on_course_hours"`
	UtilizationRate *string   `json:"utilizationRate" db:"utilization_rate" doc:"On-course hours ÷ duty hours"`
}

func (m *Module) Utilization(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time) ([]Utilization, error) {
	return handle.List[Utilization](q.Query(ctx, `WITH att AS (
		  SELECT caddy_id, count(*)::int AS days, sum(duty_hours) AS hours FROM reporting.golf_caddy_duty
		  WHERE property_id = $1 AND status = 'present' AND duty_hours IS NOT NULL AND work_date >= $2::date AND work_date <= $3::date
		  GROUP BY caddy_id),
		rnd AS (
		  SELECT caddy_id, count(*)::int AS rounds, sum(extract(epoch FROM coalesce(finished_at, now()) - started_at) / 3600) AS hours
		  FROM golf.caddy_assignments WHERE property_id = $1 AND status IN ('completed', 'replaced') AND started_at IS NOT NULL
		  AND play_date >= $2::date AND play_date <= $3::date GROUP BY caddy_id)
		SELECT c.id AS caddy_id, c.code, c.name, coalesce(att.days, 0) AS days_present, coalesce(rnd.rounds, 0) AS rounds,
		  trim_scale(round(coalesce(rnd.rounds, 0)::numeric / nullif(att.days, 0), 2))::text AS rounds_per_day,
		  trim_scale(round(coalesce(att.hours, 0)::numeric, 2))::text AS duty_hours,
		  trim_scale(round(coalesce(rnd.hours, 0)::numeric, 2))::text AS on_course_hours,
		  trim_scale(round(coalesce(rnd.hours, 0)::numeric / nullif(att.hours, 0)::numeric, 4))::text AS utilization_rate
		FROM golf.caddies c LEFT JOIN att ON att.caddy_id = c.id LEFT JOIN rnd ON rnd.caddy_id = c.id
		WHERE c.property_id = $1 AND c.archived_at IS NULL ORDER BY c.code`, property, from.Format("2006-01-02"), to.Format("2006-01-02")))
}

// ── caddy fee settlement (FR-CDL-10) ──────────────────────────────────────

type SettlementInput struct {
	CaddyID     uuid.UUID `json:"caddyId"`
	PeriodStart string    `json:"periodStart" doc:"YYYY-MM-DD"`
	PeriodEnd   string    `json:"periodEnd" doc:"YYYY-MM-DD"`
}

// SettlementLine is a caddy fee (assignment) or non-cash tip in a settlement.
type SettlementLine struct {
	Kind        string    `json:"kind" db:"kind" enum:"caddy_fee,caddy_tip"`
	SourceID    uuid.UUID `json:"sourceId" db:"source_id" doc:"Caddy assignment or caddy tip"`
	Amount      string    `json:"amount" db:"amount"`
	Date        time.Time `json:"date" db:"day"`
	Description string    `json:"description" db:"description"`
}

// Settlement is a caddy fee statement.
type Settlement struct {
	ID          uuid.UUID        `json:"id" db:"id"`
	Number      string           `json:"number" db:"number"`
	CaddyID     uuid.UUID        `json:"caddyId" db:"caddy_id"`
	CaddyName   string           `json:"caddyName" db:"caddy_name"`
	PeriodStart time.Time        `json:"periodStart" db:"period_start"`
	PeriodEnd   time.Time        `json:"periodEnd" db:"period_end"`
	Rounds      int              `json:"rounds" db:"rounds"`
	CaddyFee    string           `json:"caddyFee" db:"caddy_fee"`
	Tips        string           `json:"tips" db:"tips"`
	Deductions  string           `json:"deductions" db:"deductions"`
	Total       string           `json:"total" db:"total"`
	Lines       []SettlementLine `json:"lines" db:"lines"`
	Status      string           `json:"status" db:"status" enum:"pending,approved,rejected,paid"`
	ApprovalID  *uuid.UUID       `json:"approvalRequestId" db:"approval_request_id"`
	PayoutID    *uuid.UUID       `json:"payoutId" db:"payout_id"`
	CreatedAt   time.Time        `json:"createdAt" db:"created_at"`
}

const settlementSelect = `SELECT s.id, s.number, s.caddy_id, c.name AS caddy_name, s.period_start, s.period_end, s.rounds,
	trim_scale(s.caddy_fee)::text AS caddy_fee, trim_scale(s.tips)::text AS tips, trim_scale(s.deductions)::text AS deductions,
	trim_scale(s.total)::text AS total, s.lines, s.status, s.approval_request_id, s.payout_id, s.created_at
	FROM golf.caddy_settlements s JOIN golf.caddies c ON c.id = s.caddy_id`

func (m *Module) settlement(ctx context.Context, q dbtx.Querier, sid uuid.UUID) (Settlement, error) {
	rows, err := q.Query(ctx, settlementSelect+` WHERE s.id = $1`, sid)
	return handle.One[Settlement](rows, err, "caddy settlement")
}

// CreateSettlement builds the statement from P1's records held for the
// caddy in the period and not yet settled: the caddy fee of finished
// assignments plus non-cash tips (cash tips went to the caddy directly),
// minus the deductions of the Caddy Policies; it then goes to approval.
func (m *Module) CreateSettlement(ctx context.Context, tx pgx.Tx, property uuid.UUID, in SettlementInput) (Settlement, error) {
	from, err := time.Parse("2006-01-02", in.PeriodStart)
	if err != nil {
		return Settlement{}, handle.Invalid("periodStart", "invalid_date", "periodStart must be YYYY-MM-DD")
	}
	to, err := time.Parse("2006-01-02", in.PeriodEnd)
	if err != nil || to.Before(from) {
		return Settlement{}, handle.Invalid("periodEnd", "invalid_date", "periodEnd must be a date on or after periodStart")
	}
	var name string
	if err := tx.QueryRow(ctx, `SELECT name FROM golf.caddies WHERE id = $1 AND property_id = $2 FOR UPDATE`, in.CaddyID, property).Scan(&name); err != nil {
		if dbtx.IsNoRows(err) {
			return Settlement{}, errs.NotFound("caddy")
		}
		return Settlement{}, err
	}
	lines, err := handle.List[SettlementLine](tx.Query(ctx, `SELECT 'caddy_fee' AS kind, a.id AS source_id, trim_scale(coalesce(fs.share_amount, a.fee_amount))::text AS amount, a.play_date AS day,
		  coalesce(b.code, 'Flight') || ' · ' || a.status AS description
		FROM golf.caddy_assignments a JOIN golf.flights f ON f.id = a.flight_id LEFT JOIN golf.bookings b ON b.id = f.booking_id
		LEFT JOIN golf.caddy_fee_shares fs ON fs.assignment_id = a.id
		WHERE a.property_id = $1 AND a.caddy_id = $2 AND a.status IN ('completed', 'replaced') AND coalesce(fs.share_amount, a.fee_amount) > 0
		  AND NOT EXISTS (SELECT 1 FROM golf.caddy_settlement_items i WHERE i.item_type = 'caddy_fee' AND i.item_id = a.id)
		  AND a.play_date >= $3::date AND a.play_date <= $4::date
		UNION ALL
		SELECT 'caddy_tip', t.id, trim_scale(t.amount)::text, t.tip_date, 'Non-cash tip'
		FROM golf.caddy_tips t WHERE t.property_id = $1 AND t.caddy_id = $2 AND t.method = 'non_cash'
		  AND NOT EXISTS (SELECT 1 FROM golf.caddy_settlement_items i WHERE i.item_type = 'caddy_tip' AND i.item_id = t.id)
		  AND t.tip_date >= $3::date AND t.tip_date <= $4::date
		ORDER BY day, kind`, property, in.CaddyID, in.PeriodStart, in.PeriodEnd))
	if err != nil {
		return Settlement{}, err
	}
	if len(lines) == 0 {
		return Settlement{}, errs.Conflict("nothing_to_settle", "no unsettled caddy fee or tip in the period")
	}
	var fee, tips decimal.Decimal
	var rounds int
	for _, l := range lines {
		a, _ := decimal.NewFromString(l.Amount)
		if l.Kind == "caddy_tip" {
			tips = tips.Add(a)
		} else {
			fee = fee.Add(a)
			rounds++
		}
	}
	pol, err := m.caddyPolicy(ctx, tx, property)
	if err != nil {
		return Settlement{}, err
	}
	pct, _ := decimal.NewFromString(pol.DeductionPercent)
	per, _ := decimal.NewFromString(pol.DeductionPerRound)
	ded := fee.Mul(pct).Div(decimal.NewFromInt(100)).Add(per.Mul(decimal.NewFromInt(int64(rounds)))).Round(2)
	total := fee.Add(tips).Sub(ded)
	no, err := number(ctx, tx, property, "CST")
	if err != nil {
		return Settlement{}, err
	}
	sid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO golf.caddy_settlements (id, property_id, number, caddy_id, period_start, period_end, rounds, caddy_fee, tips,
		deductions, total, lines, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9::numeric,$10::numeric,$11::numeric,$12,$13)`, sid, property, no,
		in.CaddyID, in.PeriodStart, in.PeriodEnd, rounds, fee.String(), tips.String(), ded.String(), total.String(), jsonOf(lines), actorPtr(ctx)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return Settlement{}, errs.Conflict("period_settled", "this period already has a settlement for the caddy")
		}
		return Settlement{}, err
	}
	for _, l := range lines {
		if _, err := tx.Exec(ctx, `INSERT INTO golf.caddy_settlement_items (item_type, item_id, settlement_id, property_id, amount) VALUES ($1,$2,$3,$4,$5::numeric)`,
			l.Kind, l.SourceID, sid, property, l.Amount); err != nil {
			return Settlement{}, err
		}
	}
	rid, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: SettlementType.Code, DocumentID: sid, DocumentRef: no,
		Title: "Caddy fee settlement · " + name, PropertyID: property, Attributes: map[string]any{"amount": total.InexactFloat64()}})
	if err != nil {
		return Settlement{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.caddy_settlements SET approval_request_id = $2 WHERE id = $1`, sid, rid); err != nil {
		return Settlement{}, err
	}
	s, err := m.settlement(ctx, tx, sid)
	if err != nil {
		return s, err
	}
	return s, record(ctx, tx, "golf.caddy_settlement", sid, no+" · "+name, audit.ActionCreate, property, nil, s, "")
}

// settlementDecision applies the approval; a rejected statement releases its
// assignments and tips for a new settlement.
func (m *Module) settlementDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	st := map[string]string{approval.StatusApproved: "approved", approval.StatusRejected: "rejected", approval.StatusCancelled: "rejected"}[d.Status]
	tag, err := tx.Exec(ctx, `UPDATE golf.caddy_settlements SET status = $2 WHERE id = $1 AND status = 'pending'`, d.DocumentID, st)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	if st == "approved" {
		return m.publishSettlementApproved(ctx, tx, d.PropertyID, d.DocumentID)
	}
	_, err = tx.Exec(ctx, `DELETE FROM golf.caddy_settlement_items WHERE settlement_id = $1`, d.DocumentID)
	return err
}

// EventCaddySettlementApproved (contract K8) announces an approved caddy fee
// settlement; accounting releases the caddy fee liability with its payout.
const EventCaddySettlementApproved = "golf.caddy_settlement_approved"

// publishSettlementApproved publishes golf.caddy_settlement_approved once,
// when the statement is approved (by the workflow or at once without one).
func (m *Module) publishSettlementApproved(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID) error {
	s, err := m.settlement(ctx, tx, sid)
	if err != nil {
		return err
	}
	return m.publish(ctx, tx, EventCaddySettlementApproved, "golf.caddy_settlement", sid, property, map[string]any{"settlementId": s.ID,
		"number": s.Number, "caddyId": s.CaddyID, "caddyName": s.CaddyName, "periodStart": s.PeriodStart.Format("2006-01-02"),
		"periodEnd": s.PeriodEnd.Format("2006-01-02"), "rounds": s.Rounds, "caddyFee": s.CaddyFee, "tips": s.Tips, "deductions": s.Deductions,
		"total": s.Total})
}

type PayInput struct {
	MethodType string `json:"methodType" enum:"cash,bank_transfer"`
	Reference  string `json:"reference,omitempty"`
}

// PaySettlement records the payout; the caddy liability decreases.
func (m *Module) PaySettlement(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID, in PayInput) (Settlement, error) {
	s, err := m.settlement(ctx, tx, sid)
	if err != nil {
		return s, err
	}
	if s.Status != "approved" {
		return s, errs.Conflict("not_approved", "only approved settlements can be paid")
	}
	amt, _ := decimal.NewFromString(s.Total)
	p, err := m.Billing.RecordPayout(ctx, tx, billing.PayoutRequest{PropertyID: property, PayoutType: "caddy_fee_settlement", BeneficiaryType: "caddy",
		BeneficiaryID: s.CaddyID, BeneficiaryName: s.CaddyName, SourceType: "golf.caddy_settlement", SourceID: sid, Amount: amt, MethodType: in.MethodType,
		Reference: in.Reference})
	if err != nil {
		return s, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.caddy_settlements SET status = 'paid', payout_id = $2 WHERE id = $1`, sid, p.ID); err != nil {
		return s, err
	}
	after, err := m.settlement(ctx, tx, sid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.caddy_settlement", sid, s.Number, audit.ActionStatusChange, property, s, after, "")
}

// CaddyLiability is the caddy fee & non-cash tip liability not yet paid out.
type CaddyLiability struct {
	CaddyID   uuid.UUID `json:"caddyId" db:"caddy_id"`
	Code      string    `json:"code" db:"code"`
	Name      string    `json:"name" db:"name"`
	Recorded  string    `json:"recorded" db:"recorded" doc:"Caddy fee of finished rounds + non-cash tips"`
	Paid      string    `json:"paid" db:"paid" doc:"Settlement payouts (net of deductions)"`
	Deducted  string    `json:"deducted" db:"deducted"`
	Liability string    `json:"liability" db:"liability"`
}

func (m *Module) Liabilities(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]CaddyLiability, error) {
	return handle.List[CaddyLiability](q.Query(ctx, `SELECT c.id AS caddy_id, c.code, c.name,
		trim_scale(coalesce(f.amt, 0) + coalesce(t.amt, 0))::text AS recorded, trim_scale(coalesce(s.paid, 0))::text AS paid,
		trim_scale(coalesce(s.ded, 0))::text AS deducted,
		trim_scale(coalesce(f.amt, 0) + coalesce(t.amt, 0) - coalesce(s.paid, 0) - coalesce(s.ded, 0))::text AS liability
		FROM golf.caddies c
		LEFT JOIN (SELECT a.caddy_id, sum(coalesce(fs.share_amount, a.fee_amount)) AS amt FROM golf.caddy_assignments a
		  LEFT JOIN golf.caddy_fee_shares fs ON fs.assignment_id = a.id WHERE a.property_id = $1 AND a.status IN ('completed', 'replaced')
		  GROUP BY a.caddy_id) f ON f.caddy_id = c.id
		LEFT JOIN (SELECT caddy_id, sum(amount) AS amt FROM golf.caddy_tips WHERE property_id = $1 AND method = 'non_cash' GROUP BY caddy_id) t ON t.caddy_id = c.id
		LEFT JOIN (SELECT caddy_id, sum(total) AS paid, sum(deductions) AS ded FROM golf.caddy_settlements WHERE property_id = $1 AND status = 'paid'
		  GROUP BY caddy_id) s ON s.caddy_id = c.id
		WHERE c.property_id = $1 AND c.archived_at IS NULL ORDER BY c.code`, property))
}
