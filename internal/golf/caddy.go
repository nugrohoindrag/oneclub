package golf

// Advanced Caddy Lifecycle (PRD P2 EP-05): attendance & rotation, next
// assignment, accept, level promotion through approval, incidents, rating,
// favourites, history, utilisation and caddy fee settlement.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/platform/provision"
)

// Document types of the golf module.
var (
	PromotionType          = provision.DocumentType{Code: "golf_caddy_promotion", Module: "golf", Name: "Caddy Promotion"}
	SettlementType         = provision.DocumentType{Code: "golf_caddy_settlement", Module: "golf", Name: "Caddy Fee Settlement", Attributes: []provision.DocumentAttribute{{Key: "amount", Label: "Amount", Type: "number"}}}
	HIOType                = provision.DocumentType{Code: "golf_hio_verification", Module: "golf", Name: "Hole-in-One Verification"}
	DamageChargeType       = provision.DocumentType{Code: "golf_damage_charge", Module: "golf", Name: "Golf Cart Damage Charge", Attributes: []provision.DocumentAttribute{{Key: "amount", Label: "Amount", Type: "number"}}}
	IntroductionLetterType = provision.DocumentType{Code: "golf_introduction_letter", Module: "golf", Name: "Introduction Letter"}
)

// DocumentTypes lists the golf approval document types.
func DocumentTypes() []provision.DocumentType {
	return []provision.DocumentType{PromotionType, SettlementType, HIOType, DamageChargeType, IntroductionLetterType}
}

// Decision routes approval decisions of golf documents.
func (m *Module) Decision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	switch d.DocumentType {
	case PromotionType.Code:
		return m.promotionDecision(ctx, tx, d)
	case SettlementType.Code:
		st := map[string]string{approval.StatusApproved: "approved", approval.StatusRejected: "rejected", approval.StatusCancelled: "rejected"}[d.Status]
		_, err := tx.Exec(ctx, `UPDATE golf.caddy_settlements SET status = $2 WHERE id = $1 AND status = 'pending'`, d.DocumentID, st)
		return err
	case HIOType.Code:
		return m.hioDecision(ctx, tx, d)
	case DamageChargeType.Code:
		return m.damageDecision(ctx, tx, d)
	case IntroductionLetterType.Code:
		return m.letterDecision(ctx, tx, d)
	}
	return nil
}

// ── attendance & rotation (FR-CDL-03/04) ──────────────────────────────────

type ClockInput struct {
	CaddyID uuid.UUID  `json:"caddyId"`
	Shift   string     `json:"shift,omitempty" enum:"morning,afternoon,full_day"`
	At      *time.Time `json:"at,omitempty"`
}

// Attendance is a caddy's attendance day.
type Attendance struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	CaddyID        uuid.UUID  `json:"caddyId" db:"caddy_id"`
	CaddyCode      string     `json:"caddyCode" db:"caddy_code"`
	CaddyName      string     `json:"caddyName" db:"caddy_name"`
	WorkDate       time.Time  `json:"workDate" db:"work_date"`
	Shift          string     `json:"shift" db:"shift"`
	ClockIn        time.Time  `json:"clockIn" db:"clock_in"`
	ClockOut       *time.Time `json:"clockOut" db:"clock_out"`
	QueueNo        int        `json:"queueNo" db:"queue_no"`
	Rounds         int        `json:"rounds" db:"rounds"`
	LastAssignedAt *time.Time `json:"lastAssignedAt" db:"last_assigned_at"`
}

const attendanceSelect = `SELECT a.id, a.caddy_id, c.code AS caddy_code, c.name AS caddy_name, a.work_date, a.shift, a.clock_in, a.clock_out, a.queue_no,
	a.rounds, a.last_assigned_at FROM golf.caddy_attendance a JOIN golf.caddies c ON c.id = a.caddy_id`

// ClockIn records arrival; the arrival order is the rotation queue number.
func (m *Module) ClockIn(ctx context.Context, tx pgx.Tx, property uuid.UUID, in ClockInput) (Attendance, error) {
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM golf.caddies WHERE id = $1 AND property_id = $2 FOR UPDATE`, in.CaddyID, property).Scan(&status); err != nil {
		if dbtx.IsNoRows(err) {
			return Attendance{}, errs.NotFound("caddy")
		}
		return Attendance{}, err
	}
	if status != "active" {
		return Attendance{}, errs.Conflict("caddy_inactive", "caddy is "+status)
	}
	at := eventTime(in.At)
	local := at.In(localNow(ctx, tx).Location())
	day := local.Format("2006-01-02")
	if in.Shift == "" {
		in.Shift = "morning"
		if local.Hour() >= 12 {
			in.Shift = "afternoon"
		}
	}
	aid := id.New()
	tag, err := tx.Exec(ctx, `INSERT INTO golf.caddy_attendance (id, property_id, caddy_id, work_date, shift, clock_in, queue_no, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,(SELECT coalesce(max(queue_no), 0) + 1 FROM golf.caddy_attendance WHERE property_id = $2 AND work_date = $4),$7)
		ON CONFLICT (caddy_id, work_date) DO NOTHING`, aid, property, in.CaddyID, day, in.Shift, at, actor(ctx))
	if err != nil {
		return Attendance{}, err
	}
	if tag.RowsAffected() == 0 {
		return Attendance{}, errs.Conflict("already_clocked_in", "caddy has already clocked in today")
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.caddies SET duty_status = 'available' WHERE id = $1 AND duty_status = 'off_duty'`, in.CaddyID); err != nil {
		return Attendance{}, err
	}
	a, err := handle.Get[Attendance](tx.Query(ctx, attendanceSelect+` WHERE a.id = $1`, aid))
	if err != nil {
		return a, err
	}
	if err := m.live(ctx, tx, "golf.caddy", property, "clock_in", in.CaddyID.String(), nil); err != nil {
		return a, err
	}
	return a, record(ctx, tx, "golf.caddy_attendance", aid, a.CaddyName+" "+day, "clock_in", property, nil, a, "")
}

// ClockOut ends the day; a caddy on course cannot clock out.
func (m *Module) ClockOut(ctx context.Context, tx pgx.Tx, property uuid.UUID, in ClockInput) (Attendance, error) {
	var duty string
	if err := tx.QueryRow(ctx, `SELECT duty_status FROM golf.caddies WHERE id = $1 AND property_id = $2 FOR UPDATE`, in.CaddyID, property).Scan(&duty); err != nil {
		if dbtx.IsNoRows(err) {
			return Attendance{}, errs.NotFound("caddy")
		}
		return Attendance{}, err
	}
	if duty == "on_course" || duty == "assigned" {
		return Attendance{}, errs.Conflict("caddy_on_duty", "caddy still has an assignment")
	}
	var aid uuid.UUID
	if err := tx.QueryRow(ctx, `UPDATE golf.caddy_attendance SET clock_out = $2 WHERE caddy_id = $1 AND clock_out IS NULL RETURNING id`,
		in.CaddyID, eventTime(in.At)).Scan(&aid); err != nil {
		if dbtx.IsNoRows(err) {
			return Attendance{}, errs.Conflict("not_clocked_in", "caddy has not clocked in")
		}
		return Attendance{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.caddies SET duty_status = 'off_duty' WHERE id = $1`, in.CaddyID); err != nil {
		return Attendance{}, err
	}
	a, err := handle.Get[Attendance](tx.Query(ctx, attendanceSelect+` WHERE a.id = $1`, aid))
	if err != nil {
		return a, err
	}
	return a, record(ctx, tx, "golf.caddy_attendance", aid, a.CaddyName, "clock_out", property, nil, a, "")
}

// QueueEntry is one caddy in the rotation (Caddy Master board).
type QueueEntry struct {
	Position   int        `json:"position"`
	CaddyID    uuid.UUID  `json:"caddyId" db:"caddy_id"`
	Code       string     `json:"code" db:"code"`
	Name       string     `json:"name" db:"name"`
	Level      *string    `json:"level" db:"level"`
	LevelRank  *int       `json:"levelRank" db:"level_rank"`
	QueueNo    int        `json:"queueNo" db:"queue_no"`
	Rounds     int        `json:"roundsToday" db:"rounds"`
	LastOut    *time.Time `json:"lastAssignedAt" db:"last_assigned_at"`
	DutyStatus string     `json:"dutyStatus" db:"duty_status"`
}

// Queue returns available caddies in rotation order (Caddy Policies):
// arrival (fewest rounds today, then arrival), round_robin (longest since
// last assignment) or level (requested level first, then arrival).
func (m *Module) Queue(ctx context.Context, q dbtx.Querier, property uuid.UUID, level *uuid.UUID) ([]QueueEntry, error) {
	pol, err := m.caddyPolicy(ctx, q, property)
	if err != nil {
		return nil, err
	}
	order := "a.rounds, a.queue_no"
	switch pol.Rotation {
	case "round_robin":
		order = "a.last_assigned_at NULLS FIRST, a.queue_no"
	case "level":
		order = "(c.level_id IS DISTINCT FROM $3) , a.rounds, a.queue_no"
	}
	rows, err := q.Query(ctx, `SELECT a.caddy_id, c.code, c.name, l.name AS level, l.rank AS level_rank, a.queue_no, a.rounds, a.last_assigned_at, c.duty_status
		FROM golf.caddy_attendance a JOIN golf.caddies c ON c.id = a.caddy_id LEFT JOIN golf.caddy_levels l ON l.id = c.level_id
		WHERE a.property_id = $1 AND a.clock_out IS NULL AND c.status = 'active' AND c.duty_status = 'available' AND a.rounds < $2
		AND ($3::uuid IS NULL OR $3::uuid IS NOT NULL) ORDER BY `+order, property, max(pol.MaxRoundsPerDay, 1), level)
	list, err := handle.List[QueueEntry](rows, err)
	for i := range list {
		list[i].Position = i + 1
	}
	return list, err
}

// AcceptAssignment is the caddy's acknowledgement on the tablet (FR-CTB-02).
func (m *Module) AcceptAssignment(ctx context.Context, tx pgx.Tx, aid uuid.UUID) (CaddyAssignment, error) {
	a, err := m.caddyAssignment(ctx, tx, aid)
	if err != nil {
		return a, err
	}
	if err := m.ownAssignment(ctx, tx, a); err != nil {
		return a, err
	}
	if a.Status != "assigned" {
		if a.Status == "accepted" || a.Status == "active" {
			return a, nil
		}
		return a, errs.Conflict("invalid_status", "assignment is "+a.Status)
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.caddy_assignments SET status = 'accepted', accepted_at = now() WHERE id = $1`, aid); err != nil {
		return a, err
	}
	after, err := m.caddyAssignment(ctx, tx, aid)
	if err != nil {
		return after, err
	}
	var pid uuid.UUID
	_ = tx.QueryRow(ctx, `SELECT property_id FROM golf.caddy_assignments WHERE id = $1`, aid).Scan(&pid)
	return after, record(ctx, tx, "golf.caddy_assignment", aid, a.FlightNo+" · "+a.CaddyName, "accept", pid, a, after, "")
}

// ownAssignment: a caddy (tablet user) may only act on their own assignment;
// staff with golf.caddy_assignment.manage may act on any.
func (m *Module) ownAssignment(ctx context.Context, q dbtx.Querier, a CaddyAssignment) error {
	uid := handle.UserID(ctx)
	var owner *uuid.UUID
	if err := q.QueryRow(ctx, `SELECT user_id FROM golf.caddies WHERE id = $1`, a.CaddyID).Scan(&owner); err != nil {
		return err
	}
	if owner != nil && *owner == uid {
		return nil
	}
	var pid uuid.UUID
	if err := q.QueryRow(ctx, `SELECT property_id FROM golf.caddy_assignments WHERE id = $1`, a.ID).Scan(&pid); err != nil {
		return err
	}
	if can(ctx, "golf.caddy_assignment.manage", pid) {
		return nil
	}
	return errs.Forbidden("this is not your assignment")
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
		(SELECT joined_on FROM golf.caddies WHERE id = $1)`, caddy).Scan(&ind.Rounds, &ind.AverageRating, &ind.Ratings, &ind.Incidents12m, &joined); err != nil {
		return ind, err
	}
	if joined != nil {
		n := clock.Now()
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
	if err := tx.QueryRow(ctx, `SELECT name, level_id FROM golf.caddies WHERE id = $1 AND property_id = $2 FOR UPDATE`, caddy, property).Scan(&name, &from); err != nil {
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
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, hid, property, caddy, from, in.ToLevelID, in.Reason, jsonOf(ind), actor(ctx)); err != nil {
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
	if _, err := tx.Exec(ctx, `UPDATE golf.caddies SET level_id = $2 WHERE id = $1`, caddy, to); err != nil {
		return err
	}
	return m.publish(ctx, tx, "golf.caddy_promoted", "golf.caddy", caddy, d.PropertyID, map[string]any{"caddyId": caddy, "levelId": to})
}

// ── incidents (FR-CDL-07, FR-CTL-06) ──────────────────────────────────────

type IncidentInput struct {
	SubjectType  string     `json:"subjectType" enum:"caddy,golf_cart"`
	CaddyID      *uuid.UUID `json:"caddyId,omitempty"`
	CartID       *uuid.UUID `json:"cartId,omitempty"`
	FlightID     *uuid.UUID `json:"flightId,omitempty"`
	PlayerID     *uuid.UUID `json:"playerId,omitempty"`
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
	IncidentNo   string     `json:"incidentNo" db:"incident_no"`
	SubjectType  string     `json:"subjectType" db:"subject_type"`
	CaddyID      *uuid.UUID `json:"caddyId" db:"caddy_id"`
	CartID       *uuid.UUID `json:"cartId" db:"cart_id"`
	FlightID     *uuid.UUID `json:"flightId" db:"flight_id"`
	PlayerID     *uuid.UUID `json:"playerId" db:"player_id"`
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

const incidentSelect = `SELECT id, incident_no, subject_type, caddy_id, cart_id, flight_id, player_id, customer_id, category, severity, description, action_taken,
	attachments, trim_scale(damage_amount)::text AS damage_amount, damage_status, status, occurred_at FROM golf.incidents`

// ReportIncident records an incident; golf cart damage goes through approval
// before it is charged to the player's folio.
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
		if in.CartID == nil {
			return Incident{}, handle.Invalid("cartId", "required", "golf cart is required")
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
	if in.PlayerID != nil && in.CustomerID == nil {
		_ = tx.QueryRow(ctx, `SELECT customer_id FROM golf.flight_players WHERE id = $1`, *in.PlayerID).Scan(&in.CustomerID)
	}
	no, err := numbering.Next(ctx, tx, property, "INC", localNow(ctx, tx))
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
	if _, err := tx.Exec(ctx, `INSERT INTO golf.incidents (id, property_id, incident_no, subject_type, caddy_id, cart_id, flight_id, player_id, customer_id,
		category, severity, description, action_taken, attachments, damage_amount, damage_status, occurred_at, reported_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::numeric,$16,$17,$18)`, iid, property, no, in.SubjectType, in.CaddyID, in.CartID,
		in.FlightID, in.PlayerID, in.CustomerID, in.Category, in.Severity, in.Description, nzs(in.ActionTaken), in.Attachments, damage, dstatus,
		eventTime(in.OccurredAt), actor(ctx)); err != nil {
		return Incident{}, err
	}
	if dmg.IsPositive() {
		if in.PlayerID == nil {
			return Incident{}, handle.Invalid("playerId", "required", "a damage charge needs the player of the flight")
		}
		rid, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: DamageChargeType.Code, DocumentID: iid, DocumentRef: no,
			Title: "Golf cart damage charge · " + no, PropertyID: property, Attributes: map[string]any{"amount": dmg.InexactFloat64()}})
		if err != nil {
			return Incident{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.incidents SET approval_request_id = $2 WHERE id = $1`, iid, rid); err != nil {
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
	var player *uuid.UUID
	var amount, no string
	err := tx.QueryRow(ctx, `UPDATE golf.incidents SET damage_status = $2 WHERE id = $1 AND damage_status = 'pending'
		RETURNING player_id, damage_amount::text, incident_no`, d.DocumentID, st).Scan(&player, &amount, &no)
	if dbtx.IsNoRows(err) || st != "approved" || player == nil {
		return nil
	}
	if err != nil {
		return err
	}
	var folio *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT folio_id FROM golf.flight_players WHERE id = $1`, *player).Scan(&folio); err != nil || folio == nil {
		return err
	}
	amt, _ := decimal.NewFromString(amount)
	iid := d.DocumentID
	l, err := m.Billing.AddCharge(ctx, tx, billing.Charge{FolioID: *folio, BusinessLine: billing.LineGolf, ReferenceType: "golf.incident", ReferenceID: &iid,
		RevenueComponent: "damage_charge", Description: "Golf cart damage · " + no, Net: amt})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE golf.incidents SET damage_status = 'charged', folio_line_id = $2 WHERE id = $1`, iid, l.ID)
	return err
}

// ── rating & favourites (FR-CDL-06/09) ────────────────────────────────────

type RatingInput struct {
	Rating     int        `json:"rating" doc:"1–5"`
	Comment    string     `json:"comment,omitempty"`
	CustomerID *uuid.UUID `json:"customerId,omitempty" doc:"Staff entry on behalf of a player; the Member App uses the signed-in member"`
}

// RateCaddy stores the player's rating of the caddy after the round.
func (m *Module) RateCaddy(ctx context.Context, tx pgx.Tx, property, aid uuid.UUID, customer *uuid.UUID, rating int, comment, channel string) error {
	if rating < 1 || rating > 5 {
		return handle.Invalid("rating", "invalid_rating", "rating must be between 1 and 5")
	}
	a, err := m.caddyAssignment(ctx, tx, aid)
	if err != nil {
		return err
	}
	if a.Status != "completed" && a.Status != "replaced" {
		return errs.Conflict("round_not_finished", "the caddy can be rated after the round")
	}
	if customer != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.flight_players WHERE flight_id = $1 AND customer_id = $2)`, a.FlightID, *customer).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errs.Forbidden("only players of the flight can rate the caddy")
		}
	}
	tag, err := tx.Exec(ctx, `INSERT INTO golf.caddy_ratings (id, property_id, assignment_id, caddy_id, customer_id, rating, comment, channel)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (assignment_id, customer_id) DO NOTHING`, id.New(), property, aid, a.CaddyID, customer, rating, nzs(comment), channel)
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
		if _, err := tx.Exec(ctx, `UPDATE crm.preferences SET archived_at = now() WHERE customer_id = $1 AND category = 'favorite_caddy' AND ref_id = $2
			AND archived_at IS NULL`, customer, caddy); err != nil {
			return err
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

func (m *Module) CaddyHistory(ctx context.Context, q dbtx.Querier, caddy uuid.UUID, limit int) (CaddyHistory, error) {
	h := CaddyHistory{CaddyID: caddy}
	if err := q.QueryRow(ctx, `SELECT (SELECT count(*) FROM golf.caddy_assignments WHERE caddy_id = $1 AND status IN ('completed', 'replaced'))::int,
		(SELECT count(*) FROM golf.caddy_favorites WHERE caddy_id = $1)::int,
		(SELECT trim_scale(round(avg(rating), 2))::text FROM golf.caddy_ratings WHERE caddy_id = $1)`, caddy).Scan(&h.Rounds, &h.FavoriteCount, &h.AverageRating); err != nil {
		return h, err
	}
	var err error
	if h.Assignments, err = handle.List[CaddyAssignment](q.Query(ctx, caddyAssignmentSelect+` WHERE a.caddy_id = $1 ORDER BY f.tee_time DESC LIMIT $2`, caddy, limit)); err != nil {
		return h, err
	}
	if h.Customers, err = handle.List[CustomerServed](q.Query(ctx, `SELECT p.customer_id, c.name, count(DISTINCT a.flight_id)::int AS rounds, max(f.tee_time) AS last_round,
		EXISTS (SELECT 1 FROM golf.caddy_favorites fv WHERE fv.caddy_id = $1 AND fv.customer_id = p.customer_id) AS favorite
		FROM golf.caddy_assignments a JOIN golf.flights f ON f.id = a.flight_id
		JOIN golf.flight_players p ON p.flight_id = a.flight_id AND (a.player_id IS NULL OR a.player_id = p.id) AND p.customer_id IS NOT NULL
		JOIN crm.customers c ON c.id = p.customer_id
		WHERE a.caddy_id = $1 AND a.status IN ('completed', 'replaced') GROUP BY p.customer_id, c.name ORDER BY rounds DESC, c.name`, caddy)); err != nil {
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
	RoundsPerDay    string    `json:"roundsPerDay" db:"rounds_per_day"`
	DutyHours       string    `json:"dutyHours" db:"duty_hours"`
	OnCourseHours   string    `json:"onCourseHours" db:"on_course_hours"`
	UtilizationRate string    `json:"utilizationRate" db:"utilization_rate" doc:"On-course hours ÷ duty hours"`
}

func (m *Module) Utilization(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time) ([]Utilization, error) {
	return handle.List[Utilization](q.Query(ctx, `WITH att AS (
		  SELECT caddy_id, count(*)::int AS days, sum(extract(epoch FROM coalesce(clock_out, least(now(), clock_in + interval '12 hours')) - clock_in) / 3600) AS hours
		  FROM golf.caddy_attendance WHERE property_id = $1 AND work_date >= $2 AND work_date < $3 GROUP BY caddy_id),
		rnd AS (
		  SELECT a.caddy_id, count(*)::int AS rounds, sum(extract(epoch FROM coalesce(a.ended_at, now()) - a.started_at) / 3600) AS hours
		  FROM golf.caddy_assignments a JOIN golf.flights f ON f.id = a.flight_id
		  WHERE a.property_id = $1 AND a.status IN ('completed', 'replaced') AND a.started_at IS NOT NULL AND f.tee_time >= $4 AND f.tee_time < $5
		  GROUP BY a.caddy_id)
		SELECT c.id AS caddy_id, c.code, c.name, coalesce(att.days, 0) AS days_present, coalesce(rnd.rounds, 0) AS rounds,
		  trim_scale(round(coalesce(rnd.rounds, 0)::numeric / nullif(att.days, 0), 2))::text AS rounds_per_day,
		  trim_scale(round(coalesce(att.hours, 0)::numeric, 2))::text AS duty_hours,
		  trim_scale(round(coalesce(rnd.hours, 0)::numeric, 2))::text AS on_course_hours,
		  trim_scale(round(coalesce(rnd.hours, 0)::numeric / nullif(att.hours, 0)::numeric, 4))::text AS utilization_rate
		FROM golf.caddies c LEFT JOIN att ON att.caddy_id = c.id LEFT JOIN rnd ON rnd.caddy_id = c.id
		WHERE c.property_id = $1 AND c.archived_at IS NULL ORDER BY c.code`, property, from.Format("2006-01-02"), to.Format("2006-01-02"), from, to))
}

// ── caddy fee settlement (FR-CDL-10) ──────────────────────────────────────

type SettlementInput struct {
	CaddyID     uuid.UUID `json:"caddyId"`
	PeriodStart string    `json:"periodStart" doc:"YYYY-MM-DD"`
	PeriodEnd   string    `json:"periodEnd" doc:"YYYY-MM-DD"`
}

// SettlementLine is a caddy fee or tip folio line in a settlement.
type SettlementLine struct {
	LineID    uuid.UUID  `json:"lineId" db:"id"`
	Component string     `json:"component" db:"revenue_component"`
	Amount    string     `json:"amount" db:"amount"`
	FolioCode string     `json:"folioCode" db:"folio_code"`
	SourceID  *uuid.UUID `json:"sourceId" db:"source_id"`
	PostedAt  time.Time  `json:"postedAt" db:"posted_at"`
	Desc      string     `json:"description" db:"description"`
}

// Settlement is a caddy fee statement.
type Settlement struct {
	ID           uuid.UUID        `json:"id" db:"id"`
	SettlementNo string           `json:"settlementNo" db:"settlement_no"`
	CaddyID      uuid.UUID        `json:"caddyId" db:"caddy_id"`
	CaddyName    string           `json:"caddyName" db:"caddy_name"`
	PeriodStart  time.Time        `json:"periodStart" db:"period_start"`
	PeriodEnd    time.Time        `json:"periodEnd" db:"period_end"`
	Rounds       int              `json:"rounds" db:"rounds"`
	CaddyFee     string           `json:"caddyFee" db:"caddy_fee"`
	Tips         string           `json:"tips" db:"tips"`
	Deductions   string           `json:"deductions" db:"deductions"`
	Total        string           `json:"total" db:"total"`
	Lines        []SettlementLine `json:"lines" db:"lines"`
	Status       string           `json:"status" db:"status" enum:"pending,approved,rejected,paid"`
	ApprovalID   *uuid.UUID       `json:"approvalRequestId" db:"approval_request_id"`
	PayoutID     *uuid.UUID       `json:"payoutId" db:"payout_id"`
	CreatedAt    time.Time        `json:"createdAt" db:"created_at"`
}

const settlementSelect = `SELECT s.id, s.settlement_no, s.caddy_id, c.name AS caddy_name, s.period_start, s.period_end, s.rounds,
	trim_scale(s.caddy_fee)::text AS caddy_fee, trim_scale(s.tips)::text AS tips, trim_scale(s.deductions)::text AS deductions,
	trim_scale(s.total)::text AS total, s.lines, s.status, s.approval_request_id, s.payout_id, s.created_at
	FROM golf.caddy_settlements s JOIN golf.caddies c ON c.id = s.caddy_id`

func (m *Module) settlement(ctx context.Context, q dbtx.Querier, sid uuid.UUID) (Settlement, error) {
	rows, err := q.Query(ctx, settlementSelect+` WHERE s.id = $1`, sid)
	return handle.One[Settlement](rows, err, "caddy settlement")
}

// CreateSettlement builds the statement: caddy fee + non-cash tips recorded
// on folios for the caddy in the period (not yet settled), minus the
// deductions of the Caddy Policies; it then goes to approval.
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
	pf, _ := dayBounds(ctx, tx, from)
	_, pt := dayBounds(ctx, tx, to)
	lines, err := handle.List[SettlementLine](tx.Query(ctx, `SELECT l.id, l.revenue_component, trim_scale(l.total_amount)::text AS amount, f.code AS folio_code, l.source_id,
		l.posted_at, l.description FROM billing.folio_lines l JOIN billing.folios f ON f.id = l.folio_id
		WHERE l.property_id = $1 AND l.beneficiary_type = 'caddy' AND l.beneficiary_id = $2 AND l.status = 'posted' AND l.liability
		AND l.posted_at >= $3 AND l.posted_at < $4
		AND NOT EXISTS (SELECT 1 FROM golf.caddy_settlements s, jsonb_array_elements(s.lines) e
		  WHERE s.caddy_id = $2 AND s.status <> 'rejected' AND (e->>'lineId')::uuid = l.id)
		ORDER BY l.posted_at`, property, in.CaddyID, pf, pt))
	if err != nil {
		return Settlement{}, err
	}
	if len(lines) == 0 {
		return Settlement{}, errs.Conflict("nothing_to_settle", "no unsettled caddy fee or tip in the period")
	}
	var fee, tips decimal.Decimal
	rounds := map[string]bool{}
	for _, l := range lines {
		a, _ := decimal.NewFromString(l.Amount)
		if l.Component == "caddy_tip" {
			tips = tips.Add(a)
		} else {
			fee = fee.Add(a)
			if l.SourceID != nil {
				rounds[l.SourceID.String()] = true
			}
		}
	}
	pol, err := m.caddyPolicy(ctx, tx, property)
	if err != nil {
		return Settlement{}, err
	}
	pct, _ := decimal.NewFromString(pol.DeductionPercent)
	per, _ := decimal.NewFromString(pol.DeductionPerRound)
	ded := fee.Mul(pct).Div(decimal.NewFromInt(100)).Add(per.Mul(decimal.NewFromInt(int64(len(rounds))))).Round(2)
	total := fee.Add(tips).Sub(ded)
	no, err := numbering.Next(ctx, tx, property, "CST", localNow(ctx, tx))
	if err != nil {
		return Settlement{}, err
	}
	sid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO golf.caddy_settlements (id, property_id, settlement_no, caddy_id, period_start, period_end, rounds, caddy_fee, tips,
		deductions, total, lines, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9::numeric,$10::numeric,$11::numeric,$12,$13)`, sid, property, no,
		in.CaddyID, in.PeriodStart, in.PeriodEnd, len(rounds), fee.String(), tips.String(), ded.String(), total.String(), jsonOf(lines), actor(ctx)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return Settlement{}, errs.Conflict("period_settled", "this period already has a settlement for the caddy")
		}
		return Settlement{}, err
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

type PayInput struct {
	MethodType string `json:"methodType" enum:"cash,bank_transfer"`
	Reference  string `json:"reference,omitempty"`
}

// PaySettlement records the payment; the caddy liability decreases.
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
	return after, record(ctx, tx, "golf.caddy_settlement", sid, s.SettlementNo, audit.ActionStatusChange, property, s, after, "")
}

// CaddyLiability is the caddy fee & tip liability not yet paid out.
type CaddyLiability struct {
	CaddyID   uuid.UUID `json:"caddyId" db:"caddy_id"`
	Code      string    `json:"code" db:"code"`
	Name      string    `json:"name" db:"name"`
	Recorded  string    `json:"recorded" db:"recorded" doc:"Caddy fee + tips on folios"`
	Paid      string    `json:"paid" db:"paid" doc:"Settlement payouts (net of deductions)"`
	Deducted  string    `json:"deducted" db:"deducted"`
	Liability string    `json:"liability" db:"liability"`
}

func (m *Module) Liabilities(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]CaddyLiability, error) {
	return handle.List[CaddyLiability](q.Query(ctx, `SELECT c.id AS caddy_id, c.code, c.name,
		trim_scale(coalesce(r.amt, 0))::text AS recorded, trim_scale(coalesce(s.paid, 0))::text AS paid, trim_scale(coalesce(s.ded, 0))::text AS deducted,
		trim_scale(coalesce(r.amt, 0) - coalesce(s.paid, 0) - coalesce(s.ded, 0))::text AS liability
		FROM golf.caddies c
		LEFT JOIN (SELECT beneficiary_id, sum(total_amount) AS amt FROM billing.folio_lines WHERE property_id = $1 AND beneficiary_type = 'caddy'
		  AND liability AND status = 'posted' GROUP BY beneficiary_id) r ON r.beneficiary_id = c.id
		LEFT JOIN (SELECT caddy_id, sum(total) AS paid, sum(deductions) AS ded FROM golf.caddy_settlements WHERE property_id = $1 AND status = 'paid'
		  GROUP BY caddy_id) s ON s.caddy_id = c.id
		WHERE c.property_id = $1 AND c.archived_at IS NULL ORDER BY c.code`, property))
}
