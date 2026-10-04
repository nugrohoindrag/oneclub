package sportclub

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
	"oneclub/internal/reservation"
)

type ReasonInput struct {
	Reason string `json:"reason"`
}

type GenerateResult struct {
	Sessions int `json:"sessions"`
}

// MyClass is a session of the logged-in instructor with its roster.
type MyClass struct {
	Session Session          `json:"session"`
	Roster  []SessionBooking `json:"roster"`
}

// Register adds the Sport Club routes.
func (m *Module) Register(reg *route.Registry, eng *resource.Engine) {
	m.hooks()
	m.registerMe(reg)
	m.registerPublic(reg)
	for _, d := range []*resource.Def{Facilities, Courts, Lockers, Instructors, ClassPrograms, ClassSchedules} {
		eng.Register(reg, d)
	}
	db := m.DB
	add := func(rt route.Route) {
		rt.Module, rt.Scope = "sportclub", route.ScopeProperty
		if rt.Tag == "" {
			rt.Tag = "Sport Club"
		}
		reg.Add(rt)
	}
	// Bookings (court)
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/bookings", Summary: "Court Booking (slot & time band; package or payment)",
		Permission: "sportclub.booking.create", Request: CourtBookingInput{}, Response: CourtBookingResult{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CourtBookingInput) (CourtBookingResult, error) {
			return m.BookCourt(ctx, tx, handle.Property(ctx), in, r.Header.Get("Idempotency-Key"))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/bookings", Summary: "Sport Club bookings", Permission: "sportclub.booking.view",
		Response: reservation.Reservation{}, List: true, Query: []route.Param{{Name: "date", Description: "YYYY-MM-DD"}, {Name: "filter[status]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[reservation.Reservation], error) {
			lp := httpx.ParseList(r)
			now := localNow(ctx, tx)
			d, err := handle.QueryDate(r, "date", time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC))
			if err != nil {
				return httpx.Page[reservation.Reservation]{}, err
			}
			from := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, now.Location())
			rows, err := tx.Query(ctx, `SELECT r.id FROM reservation.reservations r WHERE r.property_id = $1 AND r.business_line = 'sportclub'
				AND r.kind = 'booking' AND r.source_type = 'sportclub.court_booking' AND ($2 = '' OR r.status = $2)
				AND EXISTS (SELECT 1 FROM reservation.reservation_lines l WHERE l.reservation_id = r.id AND l.period && tstzrange($3, $4, '[)'))
				ORDER BY r.created_at DESC LIMIT $5`, handle.Property(ctx), lp.Filters["status"], from, from.AddDate(0, 0, 1), lp.Limit)
			ids, err := collect(rows, err)
			if err != nil {
				return httpx.Page[reservation.Reservation]{}, err
			}
			out := []reservation.Reservation{}
			for _, id := range ids {
				res, err := m.Res.Get(ctx, tx, id, false)
				if err != nil {
					return httpx.Page[reservation.Reservation]{}, err
				}
				out = append(out, res)
			}
			return handle.Page(out, nil)
		})})
	// Entries
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/entries", Summary: "Sell / issue Entry Ticket (walk-in, guest of member, child, family, member, voucher, staying guest)",
		Permission: "sportclub.entry.create", Request: EntryInput{}, Response: EntryResult{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in EntryInput) (EntryResult, error) {
			return m.CreateEntry(ctx, tx, handle.Property(ctx), in, r.Header.Get("Idempotency-Key"))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/entries", Summary: "Entry tickets", Permission: "sportclub.entry.view",
		Response: Entry{}, List: true, Query: []route.Param{{Name: "date"}, {Name: "filter[status]"}, {Name: "filter[entryType]"}, {Name: "filter[facilityId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Entry], error) {
			lp := httpx.ParseList(r)
			now := localNow(ctx, tx)
			d, err := handle.QueryDate(r, "date", time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC))
			if err != nil {
				return httpx.Page[Entry]{}, err
			}
			return handle.Page(handle.List[Entry](tx.Query(ctx, entrySelect+` WHERE e.property_id = $1 AND e.visit_date = $2 AND ($3 = '' OR e.status = $3)
				AND ($4 = '' OR e.entry_type = $4) AND ($5 = '' OR e.facility_id::text = $5) ORDER BY e.created_at DESC LIMIT $6`,
				handle.Property(ctx), d.Format("2006-01-02"), lp.Filters["status"], lp.Filters["entryType"], lp.Filters["facilityId"], lp.Limit)))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/entries/{id}:cancel", Summary: "Cancel unused entry ticket (refund / voucher restored)",
		Permission: "sportclub.entry.cancel", Request: ReasonInput{}, Response: Entry{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReasonInput) (Entry, error) {
			eid, err := handle.ID(r)
			if err != nil {
				return Entry{}, err
			}
			if err := handle.Required("reason", in.Reason); err != nil {
				return Entry{}, err
			}
			return m.CancelEntry(ctx, tx, eid, in.Reason)
		})})
	// Access
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/access:validate", Summary: "Facility Access: validate a scanned member card, ticket or booking",
		Permission: "sportclub.access.validate", Request: AccessInput{}, Response: AccessResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in AccessInput) (AccessResult, error) {
			return m.ValidateAccess(ctx, tx, handle.Property(ctx), in, false)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/access-events", Summary: "Access History", Permission: "sportclub.access.view",
		Response: AccessEvent{}, List: true, Query: []route.Param{{Name: "date"}, {Name: "filter[facilityId]"}, {Name: "filter[result]"}, {Name: "filter[customerId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[AccessEvent], error) {
			lp := httpx.ParseList(r)
			now := localNow(ctx, tx)
			d, err := handle.QueryDate(r, "date", time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC))
			if err != nil {
				return httpx.Page[AccessEvent]{}, err
			}
			from := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, now.Location())
			return handle.Page(handle.List[AccessEvent](tx.Query(ctx, `SELECT a.id, a.facility_id, f.name AS facility_name, a.credential_type, a.code_hint,
				a.customer_id, c.name AS customer_name, a.direction, a.result, a.reason, a.terminal, a.offline, a.occurred_at
				FROM sportclub.access_events a JOIN sportclub.facilities f ON f.id = a.facility_id LEFT JOIN crm.customers c ON c.id = a.customer_id
				WHERE a.property_id = $1 AND a.occurred_at >= $2 AND a.occurred_at < $3 AND ($4 = '' OR a.facility_id::text = $4)
				AND ($5 = '' OR a.result = $5) AND ($6 = '' OR a.customer_id::text = $6) ORDER BY a.occurred_at DESC LIMIT $7`,
				handle.Property(ctx), from, from.AddDate(0, 0, 1), lp.Filters["facilityId"], lp.Filters["result"], lp.Filters["customerId"], lp.Limit)))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/occupancy", Summary: "Live occupancy of entry facilities (pool, gym)",
		Permission: "sportclub.access.view", Response: Occupancy{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Occupancy], error) {
			return handle.Page(m.occupancy(ctx, tx, handle.Property(ctx)))
		})})
	// Lockers
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/locker-assignments", Summary: "Assign locker (daily or rental)",
		Permission: "sportclub.locker_assignment.manage", Request: LockerAssignInput{}, Response: LockerAssignment{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in LockerAssignInput) (LockerAssignment, error) {
			return m.AssignLocker(ctx, tx, handle.Property(ctx), in, r.Header.Get("Idempotency-Key"))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/locker-assignments/{id}:return", Summary: "Return locker",
		Permission: "sportclub.locker_assignment.manage", Response: LockerAssignment{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (LockerAssignment, error) {
			aid, err := handle.ID(r)
			if err != nil {
				return LockerAssignment{}, err
			}
			return m.ReturnLocker(ctx, tx, aid)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/locker-assignments", Summary: "Locker History", Permission: "sportclub.locker_assignment.view",
		Response: LockerAssignment{}, List: true, Query: []route.Param{{Name: "filter[status]"}, {Name: "filter[lockerId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[LockerAssignment], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[LockerAssignment](tx.Query(ctx, lockerAssignSelect+` WHERE a.property_id = $1 AND ($2 = '' OR a.status = $2)
				AND ($3 = '' OR a.locker_id::text = $3) ORDER BY a.start_at DESC LIMIT $4`, handle.Property(ctx), lp.Filters["status"], lp.Filters["lockerId"], lp.Limit)))
		})})
	// Classes
	cls := func(rt route.Route) { rt.Tag = "Classes"; add(rt) }
	cls(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/class-schedules/{id}:generate", Summary: "Generate (more) sessions of a schedule",
		Permission: "sportclub.class.manage", Response: GenerateResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (GenerateResult, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return GenerateResult{}, err
			}
			// regenerate only sessions that do not exist yet
			if _, err := tx.Exec(ctx, `DELETE FROM sportclub.class_sessions s WHERE s.schedule_id = $1 AND s.status = 'scheduled' AND lower(s.period) > now()
				AND NOT EXISTS (SELECT 1 FROM sportclub.session_bookings b WHERE b.session_id = s.id)
				AND NOT EXISTS (SELECT 1 FROM reservation.capacity_slots c WHERE c.source_id = s.id AND c.booked > 0)`, sid); err != nil {
				return GenerateResult{}, err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM reservation.capacity_slots c WHERE c.source_type = 'sportclub.class_session' AND c.booked = 0
				AND NOT EXISTS (SELECT 1 FROM sportclub.class_sessions s WHERE s.id = c.source_id)`); err != nil {
				return GenerateResult{}, err
			}
			n, err := m.GenerateSessions(ctx, tx, sid)
			if err != nil {
				return GenerateResult{}, err
			}
			pid := handle.Property(ctx)
			return GenerateResult{Sessions: n}, auditOK(ctx, tx, "sportclub.class_schedule", sid, "generate", pid, map[string]any{"sessions": n})
		})})
	cls(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/class-sessions", Summary: "Class Schedule (sessions)", Permission: "sportclub.class.view",
		Response: Session{}, List: true, Query: []route.Param{{Name: "from"}, {Name: "days", Type: "integer"}, {Name: "filter[programId]"}, {Name: "filter[instructorId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Session], error) {
			lp := httpx.ParseList(r)
			now := localNow(ctx, tx)
			d, err := handle.QueryDate(r, "from", time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC))
			if err != nil {
				return httpx.Page[Session]{}, err
			}
			from := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, now.Location())
			days := min(max(handle.QueryInt(r, "days", 7), 1), 62)
			return handle.Page(handle.List[Session](tx.Query(ctx, sessionSelect+` WHERE s.property_id = $1 AND lower(s.period) >= $2 AND lower(s.period) < $3
				AND ($4 = '' OR s.program_id::text = $4) AND ($5 = '' OR s.instructor_id::text = $5) ORDER BY lower(s.period) LIMIT $6`,
				handle.Property(ctx), from, from.AddDate(0, 0, days), lp.Filters["programId"], lp.Filters["instructorId"], lp.Limit)))
		})})
	cls(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/class-sessions/{id}/roster", Summary: "Session roster", Permission: "sportclub.class.view",
		Response: SessionBooking{}, List: true, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[SessionBooking], error) {
			sid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[SessionBooking]{}, err
			}
			return handle.Page(handle.List[SessionBooking](tx.Query(ctx, bookingSelect+` WHERE b.session_id = $1 ORDER BY c.name`, sid)))
		})})
	cls(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/class-sessions/{id}:cancel", Summary: "Cancel session (quota returned, participants notified)",
		Permission: "sportclub.class.manage", Request: ReasonInput{}, Response: Session{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReasonInput) (Session, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return Session{}, err
			}
			if err := handle.Required("reason", in.Reason); err != nil {
				return Session{}, err
			}
			return m.CancelSession(ctx, tx, handle.Property(ctx), sid, in.Reason)
		})})
	cls(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/enrollments", Summary: "Class Registration (+ Registration Fee)",
		Permission: "sportclub.class.enroll", Request: EnrollInput{}, Response: Enrollment{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in EnrollInput) (Enrollment, error) {
			return m.Enroll(ctx, tx, handle.Property(ctx), in, r.Header.Get("Idempotency-Key"))
		})})
	cls(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/enrollments", Summary: "Class registrations", Permission: "sportclub.class.view",
		Response: Enrollment{}, List: true, Query: []route.Param{{Name: "filter[programId]"}, {Name: "filter[customerId]"}, {Name: "filter[status]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Enrollment], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[Enrollment](tx.Query(ctx, enrollmentSelect+` WHERE e.property_id = $1 AND ($2 = '' OR e.program_id::text = $2)
				AND ($3 = '' OR e.customer_id::text = $3) AND ($4 = '' OR e.status = $4) ORDER BY e.created_at DESC LIMIT $5`,
				handle.Property(ctx), lp.Filters["programId"], lp.Filters["customerId"], lp.Filters["status"], lp.Limit)))
		})})
	cls(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/session-bookings", Summary: "Book a seat in a class session (quota checked)",
		Permission: "sportclub.class.enroll", Request: SessionBookingInput{}, Response: SessionBooking{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in SessionBookingInput) (SessionBooking, error) {
			return m.BookSession(ctx, tx, handle.Property(ctx), in)
		})})
	cls(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/attendance", Summary: "Mark attendance (Hadir / Tidak Hadir / Izin) — quota per Class Policy",
		Permission: "sportclub.class.attendance", Request: AttendanceInput{}, Response: SessionBooking{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in AttendanceInput) (SessionBooking, error) {
			return m.MarkAttendance(ctx, tx, handle.Property(ctx), in)
		})})
	cls(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/my-classes", Summary: "My Classes (instructor): upcoming sessions with roster",
		Permission: "sportclub.class.view", Response: MyClass{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[MyClass], error) {
			p := authz.From(ctx)
			now := localNow(ctx, tx)
			from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
			ss, err := handle.List[Session](tx.Query(ctx, sessionSelect+` WHERE s.property_id = $1 AND i.user_id = $2 AND lower(s.period) >= $3
				AND lower(s.period) < $4 ORDER BY lower(s.period)`, handle.Property(ctx), p.UserID, from, from.AddDate(0, 0, 7)))
			if err != nil {
				return httpx.Page[MyClass]{}, err
			}
			out := []MyClass{}
			for _, s := range ss {
				roster, err := handle.List[SessionBooking](tx.Query(ctx, bookingSelect+` WHERE b.session_id = $1 AND b.status <> 'cancelled' ORDER BY c.name`, s.ID))
				if err != nil {
					return httpx.Page[MyClass]{}, err
				}
				out = append(out, MyClass{Session: s, Roster: roster})
			}
			return handle.Page(out, nil)
		})})
	cls(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/instructor-fees:calculate", Summary: "Calculate instructor fee for a period (approval)",
		Permission: "sportclub.instructor_fee.manage", Request: FeeCalcInput{}, Response: InstructorFee{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in FeeCalcInput) (InstructorFee, error) {
			return m.CalculateFee(ctx, tx, handle.Property(ctx), in)
		})})
	cls(route.Route{Method: http.MethodGet, Path: "/api/v1/sportclub/instructor-fees", Summary: "Instructor fees", Permission: "sportclub.instructor_fee.view",
		Response: InstructorFee{}, List: true, Query: []route.Param{{Name: "filter[status]"}, {Name: "filter[instructorId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[InstructorFee], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[InstructorFee](tx.Query(ctx, feeSelect+` WHERE f.property_id = $1 AND ($2 = '' OR f.status = $2)
				AND ($3 = '' OR f.instructor_id::text = $3) ORDER BY f.created_at DESC LIMIT $4`, handle.Property(ctx), lp.Filters["status"], lp.Filters["instructorId"], lp.Limit)))
		})})
	cls(route.Route{Method: http.MethodPost, Path: "/api/v1/sportclub/instructor-fees/{id}:pay", Summary: "Record payment of an approved instructor fee",
		Permission: "sportclub.instructor_fee.pay", Request: PayInput{}, Response: InstructorFee{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PayInput) (InstructorFee, error) {
			fid, err := handle.ID(r)
			if err != nil {
				return InstructorFee{}, err
			}
			return m.PayFee(ctx, tx, handle.Property(ctx), fid, in)
		})})
}

// AccessEvent is one Access History row.
type AccessEvent struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	FacilityID     uuid.UUID  `json:"facilityId" db:"facility_id"`
	FacilityName   string     `json:"facilityName" db:"facility_name"`
	CredentialType string     `json:"credentialType" db:"credential_type"`
	CodeHint       *string    `json:"codeHint" db:"code_hint"`
	CustomerID     *uuid.UUID `json:"customerId" db:"customer_id"`
	CustomerName   *string    `json:"customerName" db:"customer_name"`
	Direction      string     `json:"direction" db:"direction"`
	Result         string     `json:"result" db:"result"`
	Reason         *string    `json:"reason" db:"reason"`
	Terminal       *string    `json:"terminal" db:"terminal"`
	Offline        bool       `json:"offline" db:"offline"`
	OccurredAt     time.Time  `json:"occurredAt" db:"occurred_at"`
}

func collect(rows pgx.Rows, err error) ([]uuid.UUID, error) {
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

func auditOK(ctx context.Context, tx pgx.Tx, entity string, id uuid.UUID, action string, property uuid.UUID, after any) error {
	return auditRecord(ctx, tx, entity, id, action, property, after)
}

// SyncAccess is the offline Sport Reception action (FR-OPS-P2-02): the
// queued scan is validated on the server; a ticket already used elsewhere
// becomes a conflict for the client.
func (m *Module) SyncAccess(ctx context.Context, tx pgx.Tx, payload json.RawMessage) (any, error) {
	var in AccessInput
	if err := json.Unmarshal(payload, &in); err != nil {
		return nil, errs.Validation("invalid_payload", "invalid access payload")
	}
	return m.ValidateAccess(ctx, tx, handle.Property(ctx), in, true)
}
