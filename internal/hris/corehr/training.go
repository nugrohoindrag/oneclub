package corehr

// Training (FR-TRC-04/05): participants of a session with attendance,
// result and score; completing a session records the cost and issues the
// certificate of the program to the participants who passed; the mandatory
// training matrix per position shows compliance per department.

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// Participant is a participant of a training session.
type Participant struct {
	ID              uuid.UUID  `json:"id" db:"id"`
	SessionID       uuid.UUID  `json:"sessionId" db:"session_id"`
	EmployeeID      uuid.UUID  `json:"employeeId" db:"employee_id"`
	EmployeeNo      string     `json:"employeeNo" db:"employee_no"`
	EmployeeName    string     `json:"employeeName" db:"employee_name"`
	OrgUnit         *string    `json:"orgUnit" db:"org_unit"`
	Attendance      string     `json:"attendance" db:"attendance" enum:"registered,attended,absent,excused"`
	Result          *string    `json:"result" db:"result" enum:"passed,failed"`
	Score           *string    `json:"score" db:"score"`
	CertificationID *uuid.UUID `json:"certificationId" db:"certification_id"`
	Notes           *string    `json:"notes" db:"notes"`
}

const participantSelect = `SELECT p.id, p.session_id, p.employee_id, e.employee_no, e.full_name AS employee_name, ou.name AS org_unit, p.attendance, p.result,
	trim_scale(p.score)::text AS score, p.certification_id, p.notes
	FROM hris.training_participants p JOIN hris.employees e ON e.id = p.employee_id LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id`

// ParticipantsRequest registers employees to a session.
type ParticipantsRequest struct {
	EmployeeIDs []uuid.UUID `json:"employeeIds"`
}

// ParticipantUpdate records attendance and result.
type ParticipantUpdate struct {
	Attendance *string `json:"attendance,omitempty" enum:"registered,attended,absent,excused"`
	Result     *string `json:"result,omitempty" enum:"passed,failed,"`
	Score      *string `json:"score,omitempty"`
	Notes      *string `json:"notes,omitempty"`
}

// CompleteSessionRequest completes a session.
type CompleteSessionRequest struct {
	CertificateIssuedOn string `json:"certificateIssuedOn,omitempty" doc:"Default: the session end date"`
}

// SessionResult is a completed session.
type SessionResult struct {
	SessionID      uuid.UUID     `json:"sessionId"`
	Status         string        `json:"status"`
	Attended       int           `json:"attended"`
	Passed         int           `json:"passed"`
	CostTotal      string        `json:"costTotal"`
	Certifications []uuid.UUID   `json:"certifications"`
	Participants   []Participant `json:"participants"`
}

// MatrixRow is the mandatory training status of one employee and program.
type MatrixRow struct {
	EmployeeID    uuid.UUID  `json:"employeeId" db:"employee_id"`
	EmployeeName  string     `json:"employeeName" db:"employee_name"`
	OrgUnit       *string    `json:"orgUnit" db:"org_unit"`
	Position      string     `json:"position" db:"position"`
	ProgramID     uuid.UUID  `json:"programId" db:"program_id"`
	ProgramCode   string     `json:"programCode" db:"program_code"`
	ProgramName   string     `json:"programName" db:"program_name"`
	LastCompleted *time.Time `json:"lastCompleted" db:"last_completed"`
	DueDate       *time.Time `json:"dueDate" db:"due_date"`
	Status        string     `json:"status" db:"status" enum:"compliant,due,missing"`
}

func (m *Module) registerTraining(reg *route.Registry) {
	tag := "HRIS Training & Certification"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/training-sessions/{id}/participants", Summary: "Participants of a training session",
		Permission: "hris.training_session.view", Response: Participant{}, List: true, Handler: listRead(m.DB, m.participantsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/training-sessions/{id}/participants", Summary: "Register employees to a training session",
		Permission: "hris.training_session.update", Request: ParticipantsRequest{}, Response: Participant{}, List: true, Status: http.StatusOK,
		Handler: listWrite(m.DB, http.StatusOK, m.addParticipantsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPatch, Path: "/api/v1/hris/training-participants/{id}", Summary: "Record attendance and result",
		Permission: "hris.training_session.update", Request: ParticipantUpdate{}, Response: Participant{}, Handler: handle.Write(m.DB, http.StatusOK, m.updateParticipantHTTP)})
	add(reg, tag, route.Route{Method: http.MethodDelete, Path: "/api/v1/hris/training-participants/{id}", Summary: "Remove a participant of a planned session",
		Permission: "hris.training_session.update", Handler: handle.Write(m.DB, http.StatusNoContent, m.removeParticipantHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/training-sessions/{id}:complete", Summary: "Complete a training session (cost, certificates)",
		Permission: "hris.training_session.update", Request: CompleteSessionRequest{}, Response: SessionResult{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.completeSessionHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/training-matrix", Summary: "Mandatory training per position and its compliance",
		Permission: "hris.training_program.view", Response: MatrixRow{}, List: true,
		Query:   []route.Param{{Name: "orgUnitId"}, {Name: "status", Enum: []string{"compliant", "due", "missing"}}},
		Handler: listRead(m.DB, m.matrixHTTP)})
}

type sessionInfo struct {
	property, program uuid.UUID
	status, title     string
	start, end        time.Time
	capacity          *int
	location          *string
}

func (m *Module) session(ctx context.Context, tx pgx.Tx, sid uuid.UUID, lock bool) (sessionInfo, error) {
	var s sessionInfo
	sql := `SELECT property_id, program_id, status, title, starts_at, ends_at, capacity, location FROM hris.training_sessions
		WHERE id = $1 AND property_id = $2 AND archived_at IS NULL`
	if lock {
		sql += " FOR UPDATE"
	}
	if err := tx.QueryRow(ctx, sql, sid, handle.Property(ctx)).Scan(&s.property, &s.program, &s.status, &s.title, &s.start, &s.end, &s.capacity,
		&s.location); err != nil {
		return s, errs.NotFound("training session")
	}
	return s, nil
}

func (m *Module) participantsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]Participant, error) {
	sid, err := handle.ID(r)
	if err != nil {
		return nil, err
	}
	if _, err := m.session(ctx, tx, sid, false); err != nil {
		return nil, err
	}
	return handle.List[Participant](tx.Query(ctx, participantSelect+` WHERE p.session_id = $1 ORDER BY e.full_name`, sid))
}

func (m *Module) addParticipantsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ParticipantsRequest) ([]Participant, error) {
	sid, err := handle.ID(r)
	if err != nil {
		return nil, err
	}
	s, err := m.session(ctx, tx, sid, true)
	if err != nil {
		return nil, err
	}
	if s.status != "planned" {
		return nil, errs.Conflict("session_closed", "participants join a planned session only")
	}
	if len(req.EmployeeIDs) == 0 {
		return nil, handle.Invalid("employeeIds", "required", "choose one or more employees")
	}
	var users []uuid.UUID
	var added []string
	for _, eid := range req.EmployeeIDs {
		var name string
		var user *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT e.full_name, u.id FROM hris.employees e LEFT JOIN platform.users u ON u.employee_id = e.id AND u.status = 'active'
			WHERE e.id = $1 AND e.property_id = $2 AND e.status = 'active'`, eid, s.property).Scan(&name, &user); err != nil {
			return nil, handle.Invalid("employeeIds", "not_found", "employee "+eid.String()+" not found or inactive")
		}
		tag, err := tx.Exec(ctx, `INSERT INTO hris.training_participants (id, property_id, session_id, employee_id, created_by, updated_by)
			VALUES ($1,$2,$3,$4,$5,$5) ON CONFLICT (session_id, employee_id) DO NOTHING`, id.New(), s.property, sid, eid, actor(ctx))
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() > 0 {
			added = append(added, name)
			if user != nil {
				users = append(users, *user)
			}
		}
	}
	if s.capacity != nil {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM hris.training_participants WHERE session_id = $1`, sid).Scan(&n); err != nil {
			return nil, err
		}
		if n > *s.capacity {
			return nil, errs.Conflict("session_full", "the session has "+itoa(*s.capacity)+" places")
		}
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "add_participants", EntityType: KeyTrainingSession, EntityID: sid.String(),
		EntityLabel: s.title, PropertyID: &s.property, After: map[string]any{"added": added}}); err != nil {
		return nil, err
	}
	loc := location(ctx, tx, s.property)
	if err := m.notifyUsers(ctx, tx, s.property, users, "hris.training_scheduled", "/ops/ess/training",
		map[string]any{"title": s.title, "date": s.start.In(loc).Format("2006-01-02 15:04"), "location": deref(s.location)}); err != nil {
		return nil, err
	}
	return handle.List[Participant](tx.Query(ctx, participantSelect+` WHERE p.session_id = $1 ORDER BY e.full_name`, sid))
}

func (m *Module) participant(ctx context.Context, tx pgx.Tx, pid uuid.UUID) (Participant, sessionInfo, error) {
	p, err := getOne[Participant]("participant")(tx.Query(ctx, participantSelect+` WHERE p.id = $1 AND p.property_id = $2`, pid, handle.Property(ctx)))
	if err != nil {
		return p, sessionInfo{}, err
	}
	s, err := m.session(ctx, tx, p.SessionID, true)
	return p, s, err
}

func (m *Module) updateParticipantHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ParticipantUpdate) (Participant, error) {
	pid, err := handle.ID(r)
	if err != nil {
		return Participant{}, err
	}
	before, s, err := m.participant(ctx, tx, pid)
	if err != nil {
		return before, err
	}
	if s.status == "cancelled" {
		return before, errs.Conflict("session_cancelled", "the session was cancelled")
	}
	if req.Attendance != nil && !oneOf([]string{"registered", "attended", "absent", "excused"}, *req.Attendance) {
		return before, enumErr("attendance", []string{"registered", "attended", "absent", "excused"})
	}
	clearResult := false
	if req.Result != nil {
		switch *req.Result {
		case "":
			clearResult, req.Result = true, nil
		case "passed", "failed":
		default:
			return before, enumErr("result", []string{"passed", "failed"})
		}
	}
	if req.Score != nil {
		if _, err := handle.Decimal("score", *req.Score, decimalZero); err != nil {
			return before, err
		}
	}
	att := before.Attendance
	if req.Attendance != nil {
		att = *req.Attendance
	}
	if req.Result != nil && att != "attended" {
		return before, handle.Invalid("result", "invalid", "only participants who attended pass or fail")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.training_participants SET attendance = coalesce($2, attendance),
		result = CASE WHEN $3 OR coalesce($2, attendance) <> 'attended' THEN NULL ELSE coalesce($4, result) END,
		score = coalesce(nullif($5, '')::numeric, score), notes = coalesce($6, notes), updated_by = $7 WHERE id = $1`,
		pid, req.Attendance, clearResult, req.Result, req.Score, req.Notes, actor(ctx)); err != nil {
		return before, err
	}
	after, err := getOne[Participant]("participant")(tx.Query(ctx, participantSelect+` WHERE p.id = $1`, pid))
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionUpdate, EntityType: "hris.training_participant",
		EntityID: pid.String(), EntityLabel: s.title + " · " + after.EmployeeName, PropertyID: &s.property, Before: before, After: after})
}

func (m *Module) removeParticipantHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (handle.Empty, error) {
	pid, err := handle.ID(r)
	if err != nil {
		return handle.Empty{}, err
	}
	before, s, err := m.participant(ctx, tx, pid)
	if err != nil {
		return handle.Empty{}, err
	}
	if s.status != "planned" {
		return handle.Empty{}, errs.Conflict("session_closed", "participants leave a planned session only")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM hris.training_participants WHERE id = $1`, pid); err != nil {
		return handle.Empty{}, err
	}
	return handle.Empty{}, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionDelete, EntityType: "hris.training_participant",
		EntityID: pid.String(), EntityLabel: s.title + " · " + before.EmployeeName, PropertyID: &s.property, Before: before})
}

func (m *Module) completeSessionHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req CompleteSessionRequest) (SessionResult, error) {
	sid, err := handle.ID(r)
	if err != nil {
		return SessionResult{}, err
	}
	s, err := m.session(ctx, tx, sid, true)
	if err != nil {
		return SessionResult{}, err
	}
	if s.status != "planned" {
		return SessionResult{}, errs.Conflict("session_closed", "the session is already "+s.status)
	}
	loc := location(ctx, tx, s.property)
	issued := time.Date(s.end.In(loc).Year(), s.end.In(loc).Month(), s.end.In(loc).Day(), 0, 0, 0, 0, time.UTC)
	if req.CertificateIssuedOn != "" {
		if issued, err = mustDate("certificateIssuedOn", req.CertificateIssuedOn); err != nil {
			return SessionResult{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.training_participants SET attendance = 'absent' WHERE session_id = $1 AND attendance = 'registered'`, sid); err != nil {
		return SessionResult{}, err
	}
	var certType *uuid.UUID
	var cost string
	if err := tx.QueryRow(ctx, `SELECT certification_type_id, trim_scale(cost_per_participant)::text FROM hris.training_programs WHERE id = $1`, s.program).
		Scan(&certType, &cost); err != nil {
		return SessionResult{}, err
	}
	res := SessionResult{SessionID: sid, Status: "completed", Certifications: []uuid.UUID{}}
	if certType != nil {
		var issuer *string
		var validity *int
		if err := tx.QueryRow(ctx, `SELECT issuer, validity_months FROM hris.certification_types WHERE id = $1`, *certType).Scan(&issuer, &validity); err != nil {
			return res, err
		}
		var expires *string
		if validity != nil {
			e := ymd(issued.AddDate(0, *validity, -1))
			expires = &e
		}
		rows, err := tx.Query(ctx, `SELECT p.id, p.employee_id, e.full_name FROM hris.training_participants p JOIN hris.employees e ON e.id = p.employee_id
			WHERE p.session_id = $1 AND p.attendance = 'attended' AND p.result = 'passed' AND p.certification_id IS NULL`, sid)
		if err != nil {
			return res, err
		}
		type passed struct {
			pid, eid uuid.UUID
			name     string
		}
		var ps []passed
		for rows.Next() {
			var p passed
			if err := rows.Scan(&p.pid, &p.eid, &p.name); err != nil {
				rows.Close()
				return res, err
			}
			ps = append(ps, p)
		}
		rows.Close()
		for _, p := range ps {
			cid := id.New()
			if _, err := tx.Exec(ctx, `INSERT INTO hris.certifications (id, property_id, certification_type_id, holder_kind, employee_id, holder_name, issuer,
				issued_on, expires_on, notes, created_by, updated_by) VALUES ($1,$2,$3,'employee',$4,$5,$6,$7::date,$8::date,$9,$10,$10)`,
				cid, s.property, *certType, p.eid, p.name, issuer, ymd(issued), expires, "Training: "+s.title, actor(ctx)); err != nil {
				return res, err
			}
			if err := m.certificationAfterCreate(ctx, tx, map[string]any{"id": cid.String()}); err != nil {
				return res, err
			}
			if _, err := tx.Exec(ctx, `UPDATE hris.training_participants SET certification_id = $2 WHERE id = $1`, p.pid, cid); err != nil {
				return res, err
			}
			res.Certifications = append(res.Certifications, cid)
		}
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE attendance = 'attended'), count(*) FILTER (WHERE result = 'passed')
		FROM hris.training_participants WHERE session_id = $1`, sid).Scan(&res.Attended, &res.Passed); err != nil {
		return res, err
	}
	if err := tx.QueryRow(ctx, `UPDATE hris.training_sessions SET status = 'completed', completed_at = now(),
		cost_total = coalesce(cost_total, $2::numeric * $3), updated_by = $4 WHERE id = $1 RETURNING trim_scale(cost_total)::text`,
		sid, cost, res.Attended, actor(ctx)).Scan(&res.CostTotal); err != nil {
		return res, err
	}
	if res.Participants, err = handle.List[Participant](tx.Query(ctx, participantSelect+` WHERE p.session_id = $1 ORDER BY e.full_name`, sid)); err != nil {
		return res, err
	}
	return res, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "complete", EntityType: KeyTrainingSession, EntityID: sid.String(),
		EntityLabel: s.title, PropertyID: &s.property, Before: map[string]any{"status": s.status},
		After: map[string]any{"status": "completed", "attended": res.Attended, "passed": res.Passed, "costTotal": res.CostTotal,
			"certifications": len(res.Certifications)}})
}

func (m *Module) matrixHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]MatrixRow, error) {
	unit, err := handle.QueryUUID(r, "orgUnitId")
	if err != nil {
		return nil, err
	}
	property := handle.Property(ctx)
	return handle.List[MatrixRow](tx.Query(ctx, `SELECT * FROM (
		SELECT e.id AS employee_id, e.full_name AS employee_name, ou.name AS org_unit, p.name AS position, tp.id AS program_id, tp.code AS program_code,
		  tp.name AS program_name, last.completed AS last_completed,
		  CASE WHEN last.completed IS NULL THEN NULL WHEN tp.refresher_months IS NULL THEN NULL
		    ELSE (last.completed + make_interval(months => tp.refresher_months))::date END AS due_date,
		  CASE WHEN last.completed IS NULL THEN 'missing'
		    WHEN tp.refresher_months IS NOT NULL AND (last.completed + make_interval(months => tp.refresher_months))::date < $2::date THEN 'due'
		    ELSE 'compliant' END AS status
		FROM hris.employees e JOIN hris.positions p ON p.id = e.position_id
		JOIN hris.training_programs tp ON tp.property_id = e.property_id AND p.code = ANY (tp.required_positions) AND tp.archived_at IS NULL
		  AND tp.status = 'active'
		LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id
		LEFT JOIN LATERAL (SELECT (max(s.ends_at) AT TIME ZONE coalesce((SELECT nullif(x.timezone, '') FROM platform.properties x WHERE x.id = e.property_id),
		  (SELECT timezone FROM platform.instance)))::date AS completed FROM hris.training_participants pa
		  JOIN hris.training_sessions s ON s.id = pa.session_id
		  WHERE pa.employee_id = e.id AND s.program_id = tp.id AND s.status = 'completed' AND pa.attendance = 'attended'
		    AND coalesce(pa.result, 'passed') = 'passed') last ON true
		WHERE e.property_id = $1 AND e.status = 'active' AND e.archived_at IS NULL
		  AND ($3::uuid IS NULL OR e.org_unit_id IN (WITH RECURSIVE d AS (SELECT id FROM hris.org_units WHERE id = $3
		    UNION ALL SELECT c.id FROM hris.org_units c JOIN d ON c.parent_id = d.id) SELECT id FROM d))) x
		WHERE ($4 = '' OR status = $4) ORDER BY org_unit NULLS LAST, employee_name, program_name`,
		property, ymd(today(ctx, tx, property)), unit, r.URL.Query().Get("status")))
}
