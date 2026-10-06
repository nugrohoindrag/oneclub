package hrtime

// Device clock-in of partner caddies and instructors (PRD P5 FR-ATT-08,
// Should; FR-INT-P5-01): a partner gets a device user number (from 90001,
// one namespace with the employees) and a written biometric consent; the
// employee list pushed to the devices then carries the partners too, and
// the events the bridge agent (or the mock adapter) reads for a partner are
// kept here and published as hris.partner_attendance_recorded. internal/app
// marks a caddy present in Caddy Master on the first clock-in of the day
// (the caddy joins the queue). No biometric template is stored (§6 #9).

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// Permissions of partner attendance.
const (
	PermPartnerView   = "hris.partner_attendance.view"
	PermPartnerManage = "hris.partner_attendance.manage"
)

// PartnerDirectory resolves golf caddies and sport club instructors
// (wired by internal/app: hris does not import the business lines).
type PartnerDirectory interface {
	PartnerName(ctx context.Context, q dbtx.Querier, property uuid.UUID, kind string, id uuid.UUID) (string, error)
}

// PartnerAttendanceProfile is the attendance identity of a partner.
type PartnerAttendanceProfile struct {
	ID                 uuid.UUID  `json:"id" db:"id"`
	HolderKind         string     `json:"holderKind" db:"holder_kind" enum:"caddy,instructor"`
	PartnerID          uuid.UUID  `json:"partnerId" db:"partner_id"`
	PartnerName        string     `json:"partnerName" db:"partner_name"`
	DeviceUserNo       string     `json:"deviceUserNo" db:"device_user_no"`
	BiometricConsent   bool       `json:"biometricConsent" db:"biometric_consent"`
	ConsentSignedOn    *time.Time `json:"consentSignedOn" db:"consent_signed_on"`
	ConsentWithdrawnAt *time.Time `json:"consentWithdrawnAt" db:"consent_withdrawn_at"`
	EnrolledAt         *time.Time `json:"enrolledAt" db:"enrolled_at"`
	Status             string     `json:"status" db:"status" enum:"active,inactive"`
	LastEventAt        *time.Time `json:"lastEventAt" db:"last_event_at"`
	CreatedAt          time.Time  `json:"createdAt" db:"created_at"`
}

const partnerProfileSelect = `SELECT p.id, p.holder_kind, p.partner_id, p.partner_name, p.device_user_no, p.biometric_consent, p.consent_signed_on,
	p.consent_withdrawn_at, p.enrolled_at, p.status, (SELECT max(occurred_at) FROM hris.partner_attendance_events x WHERE x.profile_id = p.id) AS last_event_at,
	p.created_at FROM hris.partner_attendance_profiles p`

// PartnerAttendanceProfileRequest enrols a partner for device clock-in.
type PartnerAttendanceProfileRequest struct {
	HolderKind string `json:"holderKind" enum:"caddy,instructor"`
	PartnerID  string `json:"partnerId" doc:"Caddy (golf) or instructor (sport club) id"`
	Consent    bool   `json:"consent,omitempty" doc:"Written biometric consent signed (required before the partner is sent to a device)"`
	SignedOn   string `json:"signedOn,omitempty" doc:"Date of the signed consent (required with consent)"`
}

// PartnerAttendanceConsentRequest records or withdraws the consent of a partner.
type PartnerAttendanceConsentRequest struct {
	Consent  bool   `json:"consent" doc:"false = consent withdrawn: the partner is removed from the biometric devices"`
	SignedOn string `json:"signedOn,omitempty" doc:"Date of the signed consent (required when consenting)"`
}

// PartnerAttendanceEvent is a clock event of a partner.
type PartnerAttendanceEvent struct {
	ID          uuid.UUID `json:"id" db:"id"`
	ProfileID   uuid.UUID `json:"profileId" db:"profile_id"`
	HolderKind  string    `json:"holderKind" db:"holder_kind" enum:"caddy,instructor"`
	PartnerID   uuid.UUID `json:"partnerId" db:"partner_id"`
	PartnerName string    `json:"partnerName" db:"partner_name"`
	WorkDate    time.Time `json:"workDate" db:"work_date"`
	Direction   string    `json:"direction" db:"direction" enum:"in,out"`
	OccurredAt  time.Time `json:"occurredAt" db:"occurred_at"`
	Method      string    `json:"method" db:"method" enum:"face_recognition,fingerprint"`
	DeviceCode  string    `json:"deviceCode" db:"device_code"`
	Offline     bool      `json:"offline" db:"offline"`
}

const partnerEventSelect = `SELECT e.id, e.profile_id, e.holder_kind, e.partner_id, p.partner_name, e.work_date, e.direction, e.occurred_at, e.method,
	d.code AS device_code, e.offline FROM hris.partner_attendance_events e JOIN hris.partner_attendance_profiles p ON p.id = e.profile_id
	JOIN hris.attendance_devices d ON d.id = e.device_id`

// PartnerSimulateRequest fires a partner event on a mock device (trial adapter).
type PartnerSimulateRequest struct {
	ProfileID  uuid.UUID `json:"profileId"`
	Method     string    `json:"method,omitempty" enum:"face_recognition,fingerprint"`
	Direction  string    `json:"direction,omitempty" enum:"in,out"`
	OccurredAt string    `json:"occurredAt,omitempty" doc:"RFC 3339 (default now)"`
	EventID    string    `json:"eventId,omitempty" doc:"Device event id; resending it is idempotent"`
}

// PartnerClockResult is the outcome of a partner device event.
type PartnerClockResult struct {
	Event     PartnerAttendanceEvent `json:"event"`
	Duplicate bool                   `json:"duplicate"`
	Message   string                 `json:"message"`
}

func (m *Module) registerPartners(reg *route.Registry) {
	tag := "HRIS Attendance"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/partner-attendance-profiles",
		Summary: "Device clock-in profiles of partner caddies and instructors", Permission: PermPartnerView, Response: PartnerAttendanceProfile{}, List: true,
		Query: []route.Param{{Name: "holderKind", Enum: []string{hris.HolderCaddy, hris.HolderInstructor}}, {Name: "q"}}, Handler: listRead(m.DB, m.partnerProfilesHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/partner-attendance-profiles",
		Summary: "Enrol a caddy or instructor for device clock-in", Permission: PermPartnerManage, Request: PartnerAttendanceProfileRequest{},
		Response: PartnerAttendanceProfile{}, Idempotent: true, Handler: handle.Write(m.DB, http.StatusCreated, m.createPartnerProfileHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/partner-attendance-profiles/{id}:consent",
		Summary: "Record or withdraw the written biometric consent of a partner", Permission: PermPartnerManage, Request: PartnerAttendanceConsentRequest{},
		Response: PartnerAttendanceProfile{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.partnerConsentHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/partner-attendance-events", Summary: "Clock events of partner caddies and instructors",
		Permission: PermPartnerView, Response: PartnerAttendanceEvent{}, List: true,
		Query:   []route.Param{{Name: "from"}, {Name: "to"}, {Name: "holderKind", Enum: []string{hris.HolderCaddy, hris.HolderInstructor}}, {Name: "profileId"}},
		Handler: listRead(m.DB, m.partnerEventsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/attendance-devices/{id}:simulate-partner",
		Summary: "Fire a face / fingerprint event of a partner on a mock device (trial adapter)", Permission: PermPartnerManage,
		Request: PartnerSimulateRequest{}, Response: PartnerClockResult{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.simulatePartnerHTTP)})
}

func (m *Module) partnerProfilesHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]PartnerAttendanceProfile, error) {
	kind := r.URL.Query().Get("holderKind")
	if kind != "" && kind != hris.HolderCaddy && kind != hris.HolderInstructor {
		return nil, enumErr("holderKind", []string{hris.HolderCaddy, hris.HolderInstructor})
	}
	q := "%" + strings.TrimSpace(r.URL.Query().Get("q")) + "%"
	return handle.List[PartnerAttendanceProfile](tx.Query(ctx, partnerProfileSelect+` WHERE p.property_id = $1 AND ($2 = '' OR p.holder_kind = $2)
		AND (p.partner_name ILIKE $3 OR p.device_user_no ILIKE $3) ORDER BY p.holder_kind, p.partner_name LIMIT 1000`, handle.Property(ctx), kind, q))
}

func (m *Module) loadPartnerProfile(ctx context.Context, q dbtx.Querier, property, pid uuid.UUID) (PartnerAttendanceProfile, error) {
	return getOne[PartnerAttendanceProfile]("partner attendance profile")(q.Query(ctx, partnerProfileSelect+` WHERE p.id = $1 AND p.property_id = $2`,
		pid, property))
}

// nextPartnerDeviceNo is the next free partner device user number.
func nextPartnerDeviceNo(ctx context.Context, tx pgx.Tx, property uuid.UUID) (string, error) {
	var n int64
	if err := tx.QueryRow(ctx, `SELECT greatest($2::bigint, coalesce(max(device_user_no::bigint) FILTER (WHERE device_user_no ~ '^[0-9]{1,15}$'), 0) + 1)
		FROM hris.partner_attendance_profiles WHERE property_id = $1`, property, hris.PartnerDeviceUserBase).Scan(&n); err != nil {
		return "", err
	}
	for {
		no := strconv.FormatInt(n, 10)
		taken, err := deviceUserNoTaken(ctx, tx, property, no)
		if err != nil {
			return "", err
		}
		if !taken {
			return no, nil
		}
		n++
	}
}

// deviceUserNoTaken reports whether an employee or a partner holds a
// device user number.
func deviceUserNoTaken(ctx context.Context, q dbtx.Querier, property uuid.UUID, no string) (bool, error) {
	var taken bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.attendance_profiles WHERE property_id = $1 AND device_user_no = $2)
		OR EXISTS (SELECT 1 FROM hris.partner_attendance_profiles WHERE property_id = $1 AND device_user_no = $2)`, property, no).Scan(&taken)
	return taken, err
}

func (m *Module) createPartnerProfileHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request, req PartnerAttendanceProfileRequest) (PartnerAttendanceProfile, error) {
	property := handle.Property(ctx)
	if req.HolderKind != hris.HolderCaddy && req.HolderKind != hris.HolderInstructor {
		return PartnerAttendanceProfile{}, enumErr("holderKind", []string{hris.HolderCaddy, hris.HolderInstructor})
	}
	pid, err := uuid.Parse(strings.TrimSpace(req.PartnerID))
	if err != nil {
		return PartnerAttendanceProfile{}, handle.Invalid("partnerId", "invalid", "must be a caddy or instructor id")
	}
	if m.Partners == nil {
		return PartnerAttendanceProfile{}, errs.Conflict("partners_unavailable", "the caddy and instructor directory is not available")
	}
	name, err := m.Partners.PartnerName(ctx, tx, property, req.HolderKind, pid)
	if err != nil {
		return PartnerAttendanceProfile{}, err
	}
	if name == "" {
		return PartnerAttendanceProfile{}, handle.Invalid("partnerId", "not_found", req.HolderKind+" not found at this property")
	}
	var signed *string
	if req.Consent {
		d, err := mustDate("signedOn", req.SignedOn)
		if err != nil {
			return PartnerAttendanceProfile{}, err
		}
		s := ymd(d)
		signed = &s
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.partner_attendance_profiles WHERE property_id = $1 AND holder_kind = $2 AND partner_id = $3)`,
		property, req.HolderKind, pid).Scan(&exists); err != nil {
		return PartnerAttendanceProfile{}, err
	}
	if exists {
		return PartnerAttendanceProfile{}, errs.Conflict("already_enrolled", name+" already has a device clock-in profile")
	}
	no, err := nextPartnerDeviceNo(ctx, tx, property)
	if err != nil {
		return PartnerAttendanceProfile{}, err
	}
	pfid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.partner_attendance_profiles (id, property_id, holder_kind, partner_id, partner_name, device_user_no,
		biometric_consent, consent_signed_on, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::date,$9,$9)`,
		pfid, property, req.HolderKind, pid, name, no, req.Consent, signed, actor(ctx)); err != nil {
		return PartnerAttendanceProfile{}, err
	}
	p, err := m.loadPartnerProfile(ctx, tx, property, pfid)
	if err != nil {
		return p, err
	}
	return p, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.partner_attendance_profile",
		EntityID: pfid.String(), EntityLabel: req.HolderKind + " · " + name, PropertyID: &property,
		After: map[string]any{"holderKind": req.HolderKind, "partnerId": pid, "deviceUserNo": no, "biometricConsent": req.Consent}})
}

func (m *Module) partnerConsentHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req PartnerAttendanceConsentRequest) (PartnerAttendanceProfile, error) {
	property := handle.Property(ctx)
	pid, err := handle.ID(r)
	if err != nil {
		return PartnerAttendanceProfile{}, err
	}
	before, err := m.loadPartnerProfile(ctx, tx, property, pid)
	if err != nil {
		return before, err
	}
	if req.Consent {
		signed, err := mustDate("signedOn", req.SignedOn)
		if err != nil {
			return before, err
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.partner_attendance_profiles SET biometric_consent = true, consent_signed_on = $2::date, consent_withdrawn_at = NULL,
			removal_requested_at = NULL, status = 'active', updated_by = $3 WHERE id = $1`, pid, ymd(signed), actor(ctx)); err != nil {
			return before, err
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE hris.partner_attendance_profiles SET biometric_consent = false, consent_withdrawn_at = now(), removal_requested_at = now(),
			updated_by = $2 WHERE id = $1`, pid, actor(ctx)); err != nil {
			return before, err
		}
		if err := m.queueDeviceRemoval(ctx, tx, property, before.DeviceUserNo); err != nil {
			return before, err
		}
	}
	after, err := m.loadPartnerProfile(ctx, tx, property, pid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: map[bool]string{true: "consent", false: "consent_withdrawn"}[req.Consent],
		EntityType: "hris.partner_attendance_profile", EntityID: pid.String(), EntityLabel: after.HolderKind + " · " + after.PartnerName, PropertyID: &property,
		Before: map[string]any{"biometricConsent": before.BiometricConsent}, After: map[string]any{"biometricConsent": after.BiometricConsent,
			"signedOn": after.ConsentSignedOn}})
}

func (m *Module) partnerEventsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]PartnerAttendanceEvent, error) {
	property := handle.Property(ctx)
	to := today(ctx, tx, property)
	from := to.AddDate(0, 0, -6)
	if f, err := parseDate("from", r.URL.Query().Get("from")); err != nil {
		return nil, err
	} else if f != nil {
		from = *f
	}
	if t, err := parseDate("to", r.URL.Query().Get("to")); err != nil {
		return nil, err
	} else if t != nil {
		to = *t
	}
	profile, err := handle.QueryUUID(r, "profileId")
	if err != nil {
		return nil, err
	}
	kind := r.URL.Query().Get("holderKind")
	return handle.List[PartnerAttendanceEvent](tx.Query(ctx, partnerEventSelect+` WHERE e.property_id = $1 AND e.work_date BETWEEN $2::date AND $3::date
		AND ($4 = '' OR e.holder_kind = $4) AND ($5::uuid IS NULL OR e.profile_id = $5) ORDER BY e.occurred_at DESC LIMIT 2000`,
		property, ymd(from), ymd(to), kind, profile))
}

// partnerByDeviceNo finds the active partner profile of a device user number.
func partnerByDeviceNo(ctx context.Context, q dbtx.Querier, property uuid.UUID, no string) (*PartnerAttendanceProfile, error) {
	p, err := getOne[PartnerAttendanceProfile]("partner")(q.Query(ctx, partnerProfileSelect+` WHERE p.property_id = $1 AND p.device_user_no = $2`,
		property, strings.TrimSpace(no)))
	if err != nil {
		if e, ok := errs.As(err); ok && e.Kind == errs.KindNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// ingestPartnerEvent records one device event of a partner (idempotent per
// device event id) and publishes hris.partner_attendance_recorded.
func (m *Module) ingestPartnerEvent(ctx context.Context, tx pgx.Tx, d deviceRow, p PartnerAttendanceProfile, ev AttendanceDeviceEventInput) (PartnerClockResult, error) {
	if strings.TrimSpace(ev.EventID) == "" {
		return PartnerClockResult{}, handle.Invalid("eventId", "required", "the device event id is required")
	}
	at, err := parseInstant("occurredAt", ev.OccurredAt)
	if err != nil {
		return PartnerClockResult{}, err
	}
	if ev.Method != hris.MethodFaceRecognition && ev.Method != hris.MethodFingerprint {
		return PartnerClockResult{}, enumErr("method", []string{hris.MethodFaceRecognition, hris.MethodFingerprint})
	}
	if len(d.Methods) > 0 && !containsStr(d.Methods, ev.Method) {
		return PartnerClockResult{}, errs.Conflict("method_not_allowed", "the device "+d.Code+" is not set up for "+strings.ReplaceAll(ev.Method, "_", " "))
	}
	if p.Status != "active" {
		return PartnerClockResult{}, errs.Conflict("inactive", p.PartnerName+" is not enrolled for device clock-in")
	}
	cfg, _, err := hris.LoadAttendanceConfiguration(ctx, tx, d.PropertyID, clock.Now())
	if err != nil {
		return PartnerClockResult{}, err
	}
	if cfg.BiometricConsentRequired && !p.BiometricConsent {
		return PartnerClockResult{}, errs.Conflict("no_consent", "no written biometric consent on file for device user "+p.DeviceUserNo)
	}
	clientID := d.Code + ":" + strings.TrimSpace(ev.EventID)
	var existing uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM hris.partner_attendance_events WHERE profile_id = $1 AND client_event_id = $2`, p.ID, clientID).Scan(&existing)
	if err == nil {
		e, err := getOne[PartnerAttendanceEvent]("event")(tx.Query(ctx, partnerEventSelect+` WHERE e.id = $1`, existing))
		return PartnerClockResult{Event: e, Duplicate: true, Message: "Already recorded"}, err
	}
	if !dbtx.IsNoRows(err) {
		return PartnerClockResult{}, err
	}
	loc := location(ctx, tx, d.PropertyID)
	day := dateOf(at, loc)
	var last string
	var ins int
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT direction FROM hris.partner_attendance_events WHERE profile_id = $1 AND work_date = $2::date
		AND occurred_at <= $3 ORDER BY occurred_at DESC LIMIT 1), ''), (SELECT count(*) FROM hris.partner_attendance_events WHERE profile_id = $1
		AND work_date = $2::date AND direction = 'in')`, p.ID, ymd(day), at).Scan(&last, &ins); err != nil {
		return PartnerClockResult{}, err
	}
	dir := ev.Direction
	if dir == "" {
		dir = "in"
		if last == "in" {
			dir = "out"
		}
	}
	if dir != "in" && dir != "out" {
		return PartnerClockResult{}, enumErr("direction", []string{"in", "out"})
	}
	offline := clock.Now().Sub(at) > 10*time.Minute
	eid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.partner_attendance_events (id, property_id, profile_id, holder_kind, partner_id, work_date, direction,
		occurred_at, method, device_id, client_event_id, offline) VALUES ($1,$2,$3,$4,$5,$6::date,$7,$8,$9,$10,$11,$12)`,
		eid, d.PropertyID, p.ID, p.HolderKind, p.PartnerID, ymd(day), dir, at, ev.Method, d.ID, clientID, offline); err != nil {
		return PartnerClockResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.attendance_devices SET last_event_at = greatest(coalesce(last_event_at, $2), $2) WHERE id = $1`, d.ID, at); err != nil {
		return PartnerClockResult{}, err
	}
	if _, err := m.Events.Publish(ctx, tx, hris.EventPartnerAttendanceRecorded, "hris.partner_attendance_event", &eid, &d.PropertyID,
		hris.PartnerAttendanceRecorded{EventID: eid, PropertyID: d.PropertyID, ProfileID: p.ID, HolderKind: p.HolderKind, PartnerID: p.PartnerID,
			PartnerName: p.PartnerName, WorkDate: ymd(day), Direction: dir, OccurredAt: at, Method: ev.Method, DeviceID: d.ID, DeviceCode: d.Code,
			Offline: offline, FirstInOfDay: dir == "in" && ins == 0}); err != nil {
		return PartnerClockResult{}, err
	}
	e, err := getOne[PartnerAttendanceEvent]("event")(tx.Query(ctx, partnerEventSelect+` WHERE e.id = $1`, eid))
	msg := fmt.Sprintf("%s clocked %s at %s", p.PartnerName, dir, at.In(loc).Format("15:04"))
	return PartnerClockResult{Event: e, Message: msg}, err
}

func (m *Module) simulatePartnerHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req PartnerSimulateRequest) (PartnerClockResult, error) {
	property := handle.Property(ctx)
	did, err := handle.ID(r)
	if err != nil {
		return PartnerClockResult{}, err
	}
	d, err := loadDevice(ctx, tx, did)
	if err != nil || d.PropertyID != property {
		return PartnerClockResult{}, errs.NotFound("attendance device")
	}
	if d.Vendor != "mock" || d.Kind != "biometric" || d.Status != "active" {
		return PartnerClockResult{}, errs.Conflict("not_mock", "only an active mock biometric device can be simulated")
	}
	p, err := m.loadPartnerProfile(ctx, tx, property, req.ProfileID)
	if err != nil {
		return PartnerClockResult{}, err
	}
	at := clock.Now()
	if req.OccurredAt != "" {
		if at, err = parseInstant("occurredAt", req.OccurredAt); err != nil {
			return PartnerClockResult{}, err
		}
	}
	method := req.Method
	if method == "" {
		method = hris.MethodFaceRecognition
	}
	evID := req.EventID
	if evID == "" {
		evID = fmt.Sprintf("SIM-%d", at.UnixNano())
	}
	out, err := m.ingestPartnerEvent(ctx, tx, d, p, AttendanceDeviceEventInput{EventID: evID, DeviceUserNo: p.DeviceUserNo,
		OccurredAt: at.UTC().Format(time.RFC3339), Method: method, Direction: req.Direction})
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "simulate", EntityType: KeyAttendanceDevice, EntityID: d.ID.String(),
		EntityLabel: d.Code, PropertyID: &property, After: map[string]any{"partner": p.PartnerName, "holderKind": p.HolderKind, "method": method,
			"duplicate": out.Duplicate}})
}

// partnerDeviceUsers are the consenting partners sent to the devices and
// the partners to remove from them.
func partnerDeviceUsers(ctx context.Context, tx pgx.Tx, property uuid.UUID) (users [][2]string, removals []string, err error) {
	rows, err := tx.Query(ctx, `SELECT device_user_no, partner_name, biometric_consent AND status = 'active', removal_requested_at IS NOT NULL
		FROM hris.partner_attendance_profiles WHERE property_id = $1 ORDER BY device_user_no`, property)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var no, name string
		var send, remove bool
		if err := rows.Scan(&no, &name, &send, &remove); err != nil {
			return nil, nil, err
		}
		switch {
		case send:
			users = append(users, [2]string{no, name})
		case remove:
			removals = append(removals, no)
		}
	}
	return users, removals, rows.Err()
}
