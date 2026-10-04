package sportclub

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/membership"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/platform/provision"
	"oneclub/internal/reservation"
)

// InstructorFeeType is the approval document type of instructor honoraria.
var InstructorFeeType = provision.DocumentType{Code: "instructor_fee", Module: "sportclub", Name: "Instructor Fee",
	Attributes: []provision.DocumentAttribute{{Key: "amount", Label: "Amount", Type: "number"}}}

type program struct {
	ID          uuid.UUID  `db:"id"`
	PropertyID  uuid.UUID  `db:"property_id"`
	Code        string     `db:"code"`
	Name        string     `db:"name"`
	MinAge      *int       `db:"min_age"`
	MaxAge      *int       `db:"max_age"`
	Capacity    int        `db:"capacity"`
	Duration    int        `db:"duration_minutes"`
	FacilityID  *uuid.UUID `db:"facility_id"`
	ResourceID  *uuid.UUID `db:"resource_id"`
	ValidMonths int        `db:"registration_valid_months"`
	Status      string     `db:"status"`
}

func (m *Module) program(ctx context.Context, q dbtx.Querier, pid uuid.UUID) (program, error) {
	rows, err := q.Query(ctx, `SELECT id, property_id, code, name, min_age, max_age, capacity, duration_minutes, facility_id, resource_id,
		registration_valid_months, status FROM sportclub.class_programs WHERE id = $1`, pid)
	return handle.One[program](rows, err, "class program")
}

// GenerateSessions creates the sessions of a schedule; instructor or
// facility conflicts reject the whole schedule (FR-CLS-03).
func (m *Module) GenerateSessions(ctx context.Context, tx pgx.Tx, scheduleID uuid.UUID) (int, error) {
	var s struct {
		Property   uuid.UUID
		Program    uuid.UUID
		Instructor uuid.UUID
		Facility   *uuid.UUID
		Weekdays   []int32
		Start      string
		From, To   time.Time
		Capacity   *int
	}
	if err := tx.QueryRow(ctx, `SELECT property_id, program_id, instructor_id, facility_id, weekdays, to_char(start_time, 'HH24:MI'), start_date, end_date, capacity
		FROM sportclub.class_schedules WHERE id = $1`, scheduleID).Scan(&s.Property, &s.Program, &s.Instructor, &s.Facility, &s.Weekdays, &s.Start, &s.From, &s.To, &s.Capacity); err != nil {
		return 0, err
	}
	p, err := m.program(ctx, tx, s.Program)
	if err != nil {
		return 0, err
	}
	if p.ResourceID == nil {
		return 0, errs.Conflict("program_not_bookable", p.Name+" has no bookable resource")
	}
	if s.Facility == nil {
		s.Facility = p.FacilityID
	}
	capacity := p.Capacity
	if s.Capacity != nil {
		capacity = *s.Capacity
	}
	loc := calendar.Location(ctx, tx)
	st, _ := time.Parse("15:04", s.Start)
	n := 0
	for d := s.From; !d.After(s.To); d = d.AddDate(0, 0, 1) {
		wd := int32(d.Weekday())
		if wd == 0 {
			wd = 7
		}
		if !slices.Contains(s.Weekdays, wd) {
			continue
		}
		start := time.Date(d.Year(), d.Month(), d.Day(), st.Hour(), st.Minute(), 0, 0, loc)
		end := start.Add(time.Duration(p.Duration) * time.Minute)
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sportclub.class_sessions WHERE schedule_id = $1 AND lower(period) = $2)`, scheduleID, start).Scan(&exists); err != nil {
			return n, err
		}
		if exists {
			continue
		}
		sid := id.New()
		sp, err := tx.Begin(ctx)
		if err != nil {
			return n, err
		}
		_, err = sp.Exec(ctx, `INSERT INTO sportclub.class_sessions (id, property_id, schedule_id, program_id, instructor_id, facility_id, period, capacity, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,tstzrange($7,$8,'[)'),$9,$10)`, sid, s.Property, scheduleID, p.ID, s.Instructor, s.Facility, start, end, capacity, actor(ctx))
		if err != nil {
			_ = sp.Rollback(ctx)
			if dbtx.IsExclusionViolation(err) {
				return n, errs.Conflict("schedule_conflict", "the instructor or the facility already has a class at "+start.Format("Mon 2 Jan 15:04"))
			}
			return n, err
		}
		if err := sp.Commit(ctx); err != nil {
			return n, err
		}
		if _, err := m.Res.CreateCapacitySlot(ctx, tx, s.Property, *p.ResourceID, start, end, capacity, "sportclub.class_session", sid); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Session is a class session with its roster counts.
type Session struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	ProgramID      uuid.UUID  `json:"programId" db:"program_id"`
	ProgramName    string     `json:"programName" db:"program_name"`
	InstructorID   uuid.UUID  `json:"instructorId" db:"instructor_id"`
	InstructorName string     `json:"instructorName" db:"instructor_name"`
	FacilityID     *uuid.UUID `json:"facilityId" db:"facility_id"`
	FacilityName   *string    `json:"facilityName" db:"facility_name"`
	Start          time.Time  `json:"start" db:"start_at"`
	End            time.Time  `json:"end" db:"end_at"`
	Capacity       int        `json:"capacity" db:"capacity"`
	Booked         int        `json:"booked" db:"booked"`
	Present        int        `json:"present" db:"present"`
	Status         string     `json:"status" db:"status" enum:"scheduled,completed,cancelled"`
	CancelReason   *string    `json:"cancelReason" db:"cancel_reason"`
}

const sessionSelect = `SELECT s.id, s.program_id, p.name AS program_name, s.instructor_id, i.name AS instructor_name, s.facility_id, f.name AS facility_name,
	lower(s.period) AS start_at, upper(s.period) AS end_at, s.capacity,
	(SELECT count(*) FROM sportclub.session_bookings b WHERE b.session_id = s.id AND b.status <> 'cancelled')::int AS booked,
	(SELECT count(*) FROM sportclub.session_bookings b WHERE b.session_id = s.id AND b.status = 'present')::int AS present,
	s.status, s.cancel_reason
	FROM sportclub.class_sessions s JOIN sportclub.class_programs p ON p.id = s.program_id JOIN sportclub.instructors i ON i.id = s.instructor_id
	LEFT JOIN sportclub.facilities f ON f.id = s.facility_id`

func (m *Module) session(ctx context.Context, q dbtx.Querier, sid uuid.UUID) (Session, error) {
	rows, err := q.Query(ctx, sessionSelect+` WHERE s.id = $1`, sid)
	return handle.One[Session](rows, err, "class session")
}

// ── registration (FR-CLS-04) ──────────────────────────────────────────────

type EnrollInput struct {
	ProgramID  uuid.UUID     `json:"programId"`
	CustomerID uuid.UUID     `json:"customerId"`
	Segment    string        `json:"segment,omitempty" doc:"member | guest; default from membership"`
	Channel    string        `json:"channel,omitempty"`
	Payment    *PaymentInput `json:"payment,omitempty"`
}

// Enrollment is a Class Registration.
type Enrollment struct {
	ID              uuid.UUID  `json:"id" db:"id"`
	ProgramID       uuid.UUID  `json:"programId" db:"program_id"`
	ProgramName     string     `json:"programName" db:"program_name"`
	CustomerID      uuid.UUID  `json:"customerId" db:"customer_id"`
	CustomerName    string     `json:"customerName" db:"customer_name"`
	Segment         string     `json:"segment" db:"segment"`
	ValidUntil      time.Time  `json:"validUntil" db:"valid_until"`
	RegistrationFee string     `json:"registrationFee" db:"registration_fee"`
	FolioID         *uuid.UUID `json:"folioId" db:"folio_id"`
	Status          string     `json:"status" db:"status"`
	QuotaRemaining  string     `json:"quotaRemaining" db:"-"`
}

const enrollmentSelect = `SELECT e.id, e.program_id, p.name AS program_name, e.customer_id, c.name AS customer_name, e.segment, e.valid_until,
	trim_scale(e.registration_fee)::text AS registration_fee, e.folio_id, e.status
	FROM sportclub.enrollments e JOIN sportclub.class_programs p ON p.id = e.program_id JOIN crm.customers c ON c.id = e.customer_id`

// Enroll registers a customer to a program and charges the Registration Fee
// (member vs guest price via pricing service class_registration).
func (m *Module) Enroll(ctx context.Context, tx pgx.Tx, property uuid.UUID, in EnrollInput, key string) (Enrollment, error) {
	p, err := m.program(ctx, tx, in.ProgramID)
	if err != nil {
		return Enrollment{}, err
	}
	if p.PropertyID != property || p.Status != "active" {
		return Enrollment{}, errs.Validation("program_unavailable", "class program not available")
	}
	segment := in.Segment
	if segment == "" {
		segment = "guest"
		if s, err := membership.Segment(ctx, tx, property, in.CustomerID, "sportclub"); err != nil {
			return Enrollment{}, err
		} else if s != "" {
			segment = s
		}
	}
	now := localNow(ctx, tx)
	pr, err := commercial.Pricer{}.Price(ctx, tx, property, commercial.PriceRequest{ServiceType: "class_registration", ItemRef: p.Code, Segment: segment, Start: now})
	if err != nil && !errs.Is(err, errs.KindValidation) {
		return Enrollment{}, err
	}
	fee := decimal.Zero
	var folio *uuid.UUID
	eid := id.New()
	if err == nil && pr.Total().IsPositive() {
		fee = pr.Total()
		f, err := m.Billing.OpenFolio(ctx, tx, billing.FolioInput{Property: property, BusinessLine: billing.LineSport, CustomerID: &in.CustomerID,
			SourceType: "class_enrollment", SourceID: &eid})
		if err != nil {
			return Enrollment{}, err
		}
		folio = &f.ID
		if _, err := m.Billing.AddCharge(ctx, tx, billing.Charge{FolioID: f.ID, BusinessLine: billing.LineSport, ReferenceType: "sportclub.enrollment", ReferenceID: &eid,
			RevenueComponent: "registration_fee", Description: "Registration Fee " + p.Name, Net: pr.Net(), Service: pr.ServiceAmount(), Tax: pr.TaxAmount(),
			TaxLines: pr.Tax.Lines, SnapshotID: pr.SnapshotID}); err != nil {
			return Enrollment{}, err
		}
	}
	valid := time.Date(now.Year(), now.Month()+time.Month(p.ValidMonths), now.Day(), 0, 0, 0, 0, time.UTC)
	if _, err := tx.Exec(ctx, `INSERT INTO sportclub.enrollments (id, property_id, program_id, customer_id, segment, valid_until, registration_fee, folio_id, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9)`, eid, property, p.ID, in.CustomerID, segment, valid, fee.String(), folio, actor(ctx)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return Enrollment{}, errs.Conflict("already_registered", "the customer is already registered to "+p.Name)
		}
		if dbtx.IsForeignKeyViolation(err) {
			return Enrollment{}, errs.Validation("invalid_customer", "customer not found")
		}
		return Enrollment{}, err
	}
	if folio != nil {
		if err := m.settle(ctx, tx, *folio, in.Payment, nonEmpty(in.Channel, "ops"), key); err != nil {
			return Enrollment{}, err
		}
	}
	rows, err := tx.Query(ctx, enrollmentSelect+` WHERE e.id = $1`, eid)
	e, err := handle.One[Enrollment](rows, err, "enrollment")
	if err != nil {
		return e, err
	}
	q, _ := commercial.Quota(ctx, tx, property, in.CustomerID, "class_package", p.Code)
	e.QuotaRemaining = q.String()
	return e, audit.Record(ctx, tx, audit.Entry{Module: "sportclub", Action: audit.ActionCreate, EntityType: "sportclub.enrollment",
		EntityID: eid.String(), EntityLabel: p.Name + " · " + e.CustomerName, PropertyID: &property, After: e})
}

func nonEmpty(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// ── sessions: booking, attendance, cancellation ───────────────────────────

type SessionBookingInput struct {
	SessionID  uuid.UUID `json:"sessionId"`
	CustomerID uuid.UUID `json:"customerId"`
	Channel    string    `json:"channel,omitempty"`
}

// SessionBooking is one roster entry.
type SessionBooking struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	SessionID    uuid.UUID  `json:"sessionId" db:"session_id"`
	CustomerID   uuid.UUID  `json:"customerId" db:"customer_id"`
	CustomerName string     `json:"customerName" db:"customer_name"`
	Status       string     `json:"status" db:"status" enum:"booked,present,absent,excused,cancelled"`
	QuotaUsed    bool       `json:"quotaUsed" db:"quota_used"`
	VoucherID    *uuid.UUID `json:"voucherId" db:"voucher_id"`
	MarkedAt     *time.Time `json:"markedAt" db:"marked_at"`
}

const bookingSelect = `SELECT b.id, b.session_id, b.customer_id, c.name AS customer_name, b.status, b.quota_used, b.voucher_id, b.marked_at
	FROM sportclub.session_bookings b JOIN crm.customers c ON c.id = b.customer_id`

// BookSession books a seat in a session: registration and quota are checked
// (the 5th attendance on a 4x package is refused, FR-CLS-05 AC).
func (m *Module) BookSession(ctx context.Context, tx pgx.Tx, property uuid.UUID, in SessionBookingInput) (SessionBooking, error) {
	s, err := m.session(ctx, tx, in.SessionID)
	if err != nil {
		return SessionBooking{}, err
	}
	if s.Status != "scheduled" {
		return SessionBooking{}, errs.Conflict("session_"+s.Status, "the session is "+s.Status)
	}
	p, err := m.program(ctx, tx, s.ProgramID)
	if err != nil {
		return SessionBooking{}, err
	}
	var enrollID uuid.UUID
	var valid time.Time
	if err := tx.QueryRow(ctx, `SELECT id, valid_until FROM sportclub.enrollments WHERE program_id = $1 AND customer_id = $2 AND status = 'active'`,
		p.ID, in.CustomerID).Scan(&enrollID, &valid); err != nil {
		if dbtx.IsNoRows(err) {
			return SessionBooking{}, errs.Conflict("not_registered", "register the customer to "+p.Name+" first (registration fee)")
		}
		return SessionBooking{}, err
	}
	if valid.Before(s.Start) {
		return SessionBooking{}, errs.Conflict("registration_expired", "the registration expired on "+valid.Format("2006-01-02"))
	}
	quota, err := commercial.Quota(ctx, tx, property, in.CustomerID, "class_package", p.Code)
	if err != nil {
		return SessionBooking{}, err
	}
	var open int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM sportclub.session_bookings b JOIN sportclub.class_sessions s ON s.id = b.session_id
		WHERE b.customer_id = $1 AND s.program_id = $2 AND b.status = 'booked' AND NOT b.quota_used`, in.CustomerID, p.ID).Scan(&open); err != nil {
		return SessionBooking{}, err
	}
	if quota.LessThanOrEqual(decimal.NewFromInt(int64(open))) {
		return SessionBooking{}, errs.Conflict("quota_exhausted", fmt.Sprintf("the session quota for %s is used up (%s left, %d booked); buy a new package", p.Name, quota.String(), open))
	}
	r, err := m.Res.Book(ctx, tx, property, reservation.BookRequest{Kind: "class", BusinessLine: "sportclub", Channel: nonEmpty(in.Channel, "back_office"),
		CustomerID: &in.CustomerID, Confirm: true, SourceType: "sportclub.class_session", SourceID: &s.ID,
		Lines: []reservation.LineRequest{{ResourceID: *p.ResourceID, Start: s.Start, End: s.End, Description: p.Name}}})
	if err != nil {
		return SessionBooking{}, err
	}
	bid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO sportclub.session_bookings (id, property_id, session_id, enrollment_id, customer_id, reservation_id, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, bid, property, s.ID, enrollID, in.CustomerID, r.ID, actor(ctx)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return SessionBooking{}, errs.Conflict("already_booked", "the customer is already booked in this session")
		}
		return SessionBooking{}, err
	}
	rows, err := tx.Query(ctx, bookingSelect+` WHERE b.id = $1`, bid)
	b, err := handle.One[SessionBooking](rows, err, "session booking")
	if err != nil {
		return b, err
	}
	return b, audit.Record(ctx, tx, audit.Entry{Module: "sportclub", Action: audit.ActionCreate, EntityType: "sportclub.session_booking",
		EntityID: bid.String(), EntityLabel: p.Name + " " + s.Start.Format("2006-01-02 15:04"), PropertyID: &property, After: b})
}

type AttendanceInput struct {
	SessionBookingID uuid.UUID `json:"sessionBookingId"`
	Status           string    `json:"status" enum:"present,absent,excused"`
}

// MarkAttendance records attendance and deducts the quota per Class Policy
// (present always; absent when the policy says so; excused never).
func (m *Module) MarkAttendance(ctx context.Context, tx pgx.Tx, property uuid.UUID, in AttendanceInput) (SessionBooking, error) {
	if !slices.Contains([]string{"present", "absent", "excused"}, in.Status) {
		return SessionBooking{}, handle.Invalid("status", "invalid_status", "status must be present, absent or excused")
	}
	rows, err := tx.Query(ctx, bookingSelect+` WHERE b.id = $1 FOR UPDATE OF b`, in.SessionBookingID)
	b, err := handle.One[SessionBooking](rows, err, "session booking")
	if err != nil {
		return b, err
	}
	if b.Status == "cancelled" {
		return b, errs.Conflict("booking_cancelled", "the booking is cancelled")
	}
	s, err := m.session(ctx, tx, b.SessionID)
	if err != nil {
		return b, err
	}
	if s.Status == "cancelled" {
		return b, errs.Conflict("session_cancelled", "the session is cancelled")
	}
	// only the session's instructor or a manager marks attendance
	if p := authz.From(ctx); p != nil && !p.Can("sportclub.class.manage", &property) {
		var uid *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT user_id FROM sportclub.instructors WHERE id = $1`, s.InstructorID).Scan(&uid); err != nil {
			return b, err
		}
		if uid == nil || *uid != p.UserID {
			return b, errs.Forbidden("only the instructor of this class can mark attendance")
		}
	}
	pol, _, err := m.policy(ctx, tx, property)
	if err != nil {
		return b, err
	}
	p, err := m.program(ctx, tx, s.ProgramID)
	if err != nil {
		return b, err
	}
	deduct := in.Status == "present" || (in.Status == "absent" && pol.DeductQuotaOnAbsent)
	voucher := b.VoucherID
	if deduct && !b.QuotaUsed {
		v, err := m.Commercial.UseQuota(ctx, tx, property, b.CustomerID, "class_package", p.Code, decimal.NewFromInt(1), "class_session",
			"sportclub.session_booking", &b.ID, "class-"+b.ID.String())
		if err != nil {
			return b, err
		}
		voucher = v
	}
	if !deduct && b.QuotaUsed && b.VoucherID != nil {
		if err := m.Commercial.Restore(ctx, tx, *b.VoucherID, decimal.NewFromInt(1), "attendance changed to "+in.Status, "class-restore-"+b.ID.String()+"-"+in.Status); err != nil {
			return b, err
		}
		voucher = nil
	}
	if _, err := tx.Exec(ctx, `UPDATE sportclub.session_bookings SET status = $2, quota_used = $3, voucher_id = $4, marked_by = $5, marked_at = now() WHERE id = $1`,
		b.ID, in.Status, deduct, voucher, actor(ctx)); err != nil {
		return b, err
	}
	rows, err = tx.Query(ctx, bookingSelect+` WHERE b.id = $1`, b.ID)
	after, err := handle.One[SessionBooking](rows, err, "session booking")
	if err != nil {
		return after, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "sportclub", Action: "attendance", EntityType: "sportclub.session_booking",
		EntityID: b.ID.String(), EntityLabel: p.Name + " · " + b.CustomerName, PropertyID: &property, Before: b, After: after}); err != nil {
		return after, err
	}
	if in.Status == "present" {
		_, err = m.Events.Publish(ctx, tx, "sportclub.class_attended", "sportclub.class_session", &s.ID, &property, map[string]any{
			"sessionId": s.ID, "programId": p.ID, "program": p.Name, "customerId": b.CustomerID, "instructorId": s.InstructorID})
	}
	return after, err
}

// CancelSession cancels a session by the club: seats released, used quota
// returned, participants notified (FR-CLS-07).
func (m *Module) CancelSession(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID, reason string) (Session, error) {
	s, err := m.session(ctx, tx, sid)
	if err != nil {
		return s, err
	}
	if s.Status != "scheduled" {
		return s, errs.Conflict("session_"+s.Status, "the session is "+s.Status)
	}
	if _, err := tx.Exec(ctx, `UPDATE sportclub.class_sessions SET status = 'cancelled', cancel_reason = $2, updated_by = $3 WHERE id = $1`, sid, reason, actor(ctx)); err != nil {
		return s, err
	}
	rows, err := tx.Query(ctx, `SELECT b.id, b.customer_id, b.reservation_id, b.voucher_id, b.quota_used FROM sportclub.session_bookings b
		WHERE b.session_id = $1 AND b.status <> 'cancelled'`, sid)
	if err != nil {
		return s, err
	}
	type bk struct {
		id, cust uuid.UUID
		res      *uuid.UUID
		voucher  *uuid.UUID
		used     bool
	}
	var list []bk
	for rows.Next() {
		var x bk
		if err := rows.Scan(&x.id, &x.cust, &x.res, &x.voucher, &x.used); err != nil {
			rows.Close()
			return s, err
		}
		list = append(list, x)
	}
	rows.Close()
	for _, x := range list {
		if x.res != nil {
			if _, err := m.Res.Cancel(ctx, tx, *x.res, "class cancelled by the club: "+reason, true); err != nil {
				return s, err
			}
		}
		if x.used && x.voucher != nil {
			if err := m.Commercial.Restore(ctx, tx, *x.voucher, decimal.NewFromInt(1), "class cancelled by the club", "class-cancel-"+x.id.String()); err != nil {
				return s, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE sportclub.session_bookings SET status = 'cancelled', quota_used = false WHERE id = $1`, x.id); err != nil {
			return s, err
		}
		msg := notify.Message{Event: "sportclub.class_cancelled", Category: "booking", PropertyID: &property,
			Data: map[string]any{"program": s.ProgramName, "start": s.Start.In(calendar.Location(ctx, tx)).Format("Mon 2 Jan 15:04"), "reason": reason}}
		if ok, err := customerRecipient(ctx, tx, x.cust, &msg); err != nil {
			return s, err
		} else if ok {
			if err := m.Notify.Send(ctx, tx, msg); err != nil {
				return s, err
			}
		}
	}
	if err := m.Res.CloseCapacitySlot(ctx, tx, "sportclub.class_session", sid); err != nil {
		return s, err
	}
	after, err := m.session(ctx, tx, sid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "sportclub", Action: audit.ActionStatusChange, EntityType: "sportclub.class_session",
		EntityID: sid.String(), EntityLabel: s.ProgramName + " " + s.Start.Format("2006-01-02 15:04"), PropertyID: &property, Before: s, After: after, Reason: reason})
}

func customerRecipient(ctx context.Context, tx pgx.Tx, customer uuid.UUID, msg *notify.Message) (bool, error) {
	var email, locale *string
	var user *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT email, user_id, locale FROM crm.customers WHERE id = $1`, customer).Scan(&email, &user, &locale); err != nil {
		return false, err
	}
	switch {
	case user != nil:
		msg.UserIDs = []uuid.UUID{*user}
	case email != nil && *email != "":
		msg.Email, msg.Channels = *email, []string{notify.ChannelEmail}
		if locale != nil {
			msg.Locale = *locale
		}
	default:
		return false, nil
	}
	return true, nil
}

// ── instructor fees (FR-CLS-08) ───────────────────────────────────────────

type FeeCalcInput struct {
	InstructorID uuid.UUID `json:"instructorId"`
	PeriodStart  string    `json:"periodStart" doc:"YYYY-MM-DD"`
	PeriodEnd    string    `json:"periodEnd" doc:"YYYY-MM-DD"`
}

// InstructorFee is an honorarium statement.
type InstructorFee struct {
	ID             uuid.UUID        `json:"id" db:"id"`
	FeeNo          string           `json:"feeNo" db:"fee_no"`
	InstructorID   uuid.UUID        `json:"instructorId" db:"instructor_id"`
	InstructorName string           `json:"instructorName" db:"instructor_name"`
	PeriodStart    time.Time        `json:"periodStart" db:"period_start"`
	PeriodEnd      time.Time        `json:"periodEnd" db:"period_end"`
	Sessions       int              `json:"sessions" db:"sessions"`
	Students       int              `json:"students" db:"students"`
	Rate           string           `json:"rate" db:"rate"`
	Amount         string           `json:"amount" db:"amount"`
	Lines          []map[string]any `json:"lines" db:"lines"`
	Status         string           `json:"status" db:"status" enum:"pending,approved,rejected,paid"`
	ApprovalID     *uuid.UUID       `json:"approvalId" db:"approval_id"`
	PayoutID       *uuid.UUID       `json:"payoutId" db:"payout_id"`
}

const feeSelect = `SELECT f.id, f.fee_no, f.instructor_id, i.name AS instructor_name, f.period_start, f.period_end, f.sessions, f.students,
	trim_scale(f.rate)::text AS rate, trim_scale(f.amount)::text AS amount, f.lines, f.status, f.approval_id, f.payout_id
	FROM sportclub.instructor_fees f JOIN sportclub.instructors i ON i.id = f.instructor_id`

func (m *Module) instructorFee(ctx context.Context, q dbtx.Querier, fid uuid.UUID) (InstructorFee, error) {
	rows, err := q.Query(ctx, feeSelect+` WHERE f.id = $1`, fid)
	return handle.One[InstructorFee](rows, err, "instructor fee")
}

// CalculateFee computes the honorarium of a period and submits it for
// approval: per session held, or per student present.
func (m *Module) CalculateFee(ctx context.Context, tx pgx.Tx, property uuid.UUID, in FeeCalcInput) (InstructorFee, error) {
	from, err := time.Parse("2006-01-02", in.PeriodStart)
	if err != nil {
		return InstructorFee{}, handle.Invalid("periodStart", "invalid_date", "periodStart must be YYYY-MM-DD")
	}
	to, err := time.Parse("2006-01-02", in.PeriodEnd)
	if err != nil || to.Before(from) {
		return InstructorFee{}, handle.Invalid("periodEnd", "invalid_date", "periodEnd must be a date on or after periodStart")
	}
	var name, scheme, rate string
	if err := tx.QueryRow(ctx, `SELECT name, fee_scheme, fee_rate::text FROM sportclub.instructors WHERE id = $1 AND property_id = $2`, in.InstructorID, property).
		Scan(&name, &scheme, &rate); err != nil {
		if dbtx.IsNoRows(err) {
			return InstructorFee{}, errNotFound("instructor")
		}
		return InstructorFee{}, err
	}
	loc := calendar.Location(ctx, tx)
	pf, pt := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc), time.Date(to.Year(), to.Month(), to.Day()+1, 0, 0, 0, 0, loc)
	type line struct {
		SessionID uuid.UUID `db:"id" json:"sessionId"`
		Program   string    `db:"program_name" json:"program"`
		Start     time.Time `db:"start_at" json:"start"`
		Present   int       `db:"present" json:"present"`
	}
	lines, err := handle.List[line](tx.Query(ctx, `SELECT s.id, p.name AS program_name, lower(s.period) AS start_at,
		(SELECT count(*) FROM sportclub.session_bookings b WHERE b.session_id = s.id AND b.status = 'present')::int AS present
		FROM sportclub.class_sessions s JOIN sportclub.class_programs p ON p.id = s.program_id
		WHERE s.instructor_id = $1 AND s.status <> 'cancelled' AND lower(s.period) >= $2 AND lower(s.period) < $3 AND upper(s.period) <= now()
		ORDER BY lower(s.period)`, in.InstructorID, pf, pt))
	if err != nil {
		return InstructorFee{}, err
	}
	students := 0
	for _, l := range lines {
		students += l.Present
	}
	r, _ := decimal.NewFromString(rate)
	units := len(lines)
	if scheme == "per_student" {
		units = students
	}
	amount := r.Mul(decimal.NewFromInt(int64(units)))
	no, err := numbering.Next(ctx, tx, property, "IFE", clock.Now().In(loc))
	if err != nil {
		return InstructorFee{}, err
	}
	fid := id.New()
	raw, _ := json.Marshal(lines)
	if _, err := tx.Exec(ctx, `INSERT INTO sportclub.instructor_fees (id, property_id, fee_no, instructor_id, period_start, period_end, sessions, students, rate,
		amount, lines, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10::numeric,$11,$12)`,
		fid, property, no, in.InstructorID, from, to, len(lines), students, r.String(), amount.String(), raw, actor(ctx)); err != nil {
		return InstructorFee{}, err
	}
	ids := make([]uuid.UUID, 0, len(lines))
	for _, l := range lines {
		ids = append(ids, l.SessionID)
	}
	if _, err := tx.Exec(ctx, `UPDATE sportclub.class_sessions SET status = 'completed' WHERE id = ANY($1) AND status = 'scheduled'`, ids); err != nil {
		return InstructorFee{}, err
	}
	f, err := m.instructorFee(ctx, tx, fid)
	if err != nil {
		return f, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "sportclub", Action: audit.ActionCreate, EntityType: "sportclub.instructor_fee",
		EntityID: fid.String(), EntityLabel: no + " · " + name, PropertyID: &property, After: f}); err != nil {
		return f, err
	}
	aid, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: InstructorFeeType.Code, DocumentID: fid, DocumentRef: no,
		Title: fmt.Sprintf("Instructor fee %s %s – %s", name, in.PeriodStart, in.PeriodEnd), PropertyID: property, Attributes: map[string]any{"amount": amount.String()}})
	if err != nil {
		return f, err
	}
	if _, err := tx.Exec(ctx, `UPDATE sportclub.instructor_fees SET approval_id = $2 WHERE id = $1`, fid, aid); err != nil {
		return f, err
	}
	return m.instructorFee(ctx, tx, fid)
}

// FeeDecision applies the approval of an instructor fee.
func (m *Module) FeeDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	status := map[string]string{approval.StatusApproved: "approved", approval.StatusRejected: "rejected", approval.StatusCancelled: "rejected"}[d.Status]
	tag, err := tx.Exec(ctx, `UPDATE sportclub.instructor_fees SET status = $2 WHERE id = $1 AND status = 'pending'`, d.DocumentID, status)
	if err != nil || tag.RowsAffected() == 0 || status != "approved" {
		return err
	}
	f, err := m.instructorFee(ctx, tx, d.DocumentID)
	if err != nil {
		return err
	}
	pid := d.PropertyID
	_, err = m.Events.Publish(ctx, tx, "sportclub.instructor_fee_approved", "sportclub.instructor_fee", &f.ID, &pid, map[string]any{
		"feeId": f.ID, "feeNo": f.FeeNo, "instructorId": f.InstructorID, "amount": f.Amount, "periodStart": f.PeriodStart, "periodEnd": f.PeriodEnd})
	return err
}

type PayInput struct {
	MethodType string `json:"methodType" enum:"cash,bank_transfer"`
	Reference  string `json:"reference,omitempty"`
}

// PayFee records the payment of an approved honorarium.
func (m *Module) PayFee(ctx context.Context, tx pgx.Tx, property, fid uuid.UUID, in PayInput) (InstructorFee, error) {
	f, err := m.instructorFee(ctx, tx, fid)
	if err != nil {
		return f, err
	}
	if f.Status != "approved" {
		return f, errs.Conflict("not_approved", "only approved instructor fees can be paid")
	}
	amt, _ := decimal.NewFromString(f.Amount)
	p, err := m.Billing.RecordPayout(ctx, tx, billing.PayoutRequest{PropertyID: property, PayoutType: "instructor_fee", BeneficiaryType: "instructor",
		BeneficiaryID: f.InstructorID, BeneficiaryName: f.InstructorName, SourceType: "sportclub.instructor_fee", SourceID: fid, Amount: amt,
		MethodType: in.MethodType, Reference: in.Reference})
	if err != nil {
		return f, err
	}
	if _, err := tx.Exec(ctx, `UPDATE sportclub.instructor_fees SET status = 'paid', payout_id = $2 WHERE id = $1`, fid, p.ID); err != nil {
		return f, err
	}
	after, err := m.instructorFee(ctx, tx, fid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "sportclub", Action: audit.ActionStatusChange, EntityType: "sportclub.instructor_fee",
		EntityID: fid.String(), EntityLabel: f.FeeNo, PropertyID: &property, Before: f, After: after})
}

// Templates are the Sport Club notification templates (FR-INT-P2-04).
func Templates() []provision.Template {
	t := map[string][2]string{
		"en": {"Class cancelled: {{.program}} {{.start}}", "The {{.program}} class on {{.start}} has been cancelled by the club ({{.reason}}). Your session quota has been returned."},
		"id": {"Kelas dibatalkan: {{.program}} {{.start}}", "Kelas {{.program}} pada {{.start}} dibatalkan oleh club ({{.reason}}). Kuota sesi Anda telah dikembalikan."},
	}
	var out []provision.Template
	for loc, c := range t {
		for _, ch := range []string{"email", "in_app"} {
			out = append(out, provision.Template{Event: "sportclub.class_cancelled", Channel: ch, Locale: loc, Subject: c[0], Body: c[1]})
		}
	}
	return out
}
