package hrtime

// Attendance devices and profiles (FR-ATT-01/06, FR-INT-P5-01, §16 #7):
// face recognition + fingerprint terminals at the 6 device points reach
// OneClub only through the bridge agent (P0 FR-INT-07): the agent pushes
// attendance events (POST /api/v1/bridge/hris/attendance-events with its
// token) and receives the employee list / removals as queued commands of
// the attendance_terminal profile. Biometric templates stay on the device:
// OneClub keeps the device user number, the written consent and the
// events (§6 #9, FR-ATT-06). For the trial the vendor "mock" is a simulated
// adapter: HR fires device events from the Back Office and the employee
// sync completes at once. Leavers are removed from the devices (§16 #10).

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/kernel/secret"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/iam/password"
	"oneclub/internal/platform/integration"
	"oneclub/internal/platform/outbox"
)

// AttendanceProfile is the attendance identity of an employee.
type AttendanceProfile struct {
	EmployeeID         uuid.UUID  `json:"employeeId" db:"employee_id"`
	EmployeeNo         string     `json:"employeeNo" db:"employee_no"`
	EmployeeName       string     `json:"employeeName" db:"employee_name"`
	OrgUnitName        *string    `json:"orgUnitName" db:"org_unit_name"`
	DeviceUserNo       *string    `json:"deviceUserNo" db:"device_user_no"`
	PINSet             bool       `json:"pinSet" db:"pin_set"`
	PINLocked          bool       `json:"pinLocked" db:"pin_locked"`
	BiometricConsent   bool       `json:"biometricConsent" db:"biometric_consent"`
	ConsentSignedOn    *time.Time `json:"consentSignedOn" db:"consent_signed_on"`
	ConsentFileID      *uuid.UUID `json:"consentFileId" db:"consent_file_id"`
	ConsentWithdrawnAt *time.Time `json:"consentWithdrawnAt" db:"consent_withdrawn_at"`
	EnrolledAt         *time.Time `json:"enrolledAt" db:"enrolled_at"`
	RemovalRequestedAt *time.Time `json:"removalRequestedAt" db:"removal_requested_at"`
}

const profileSelect = `SELECT e.id AS employee_id, e.employee_no, e.full_name AS employee_name, ou.name AS org_unit_name, p.device_user_no,
	p.pin_hash IS NOT NULL AS pin_set, coalesce(p.locked_until > now(), false) AS pin_locked, coalesce(p.biometric_consent, false) AS biometric_consent,
	p.consent_signed_on, p.consent_file_id, p.consent_withdrawn_at, p.enrolled_at, p.removal_requested_at
	FROM hris.employees e LEFT JOIN hris.attendance_profiles p ON p.employee_id = e.id LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id`

// AttendancePINRequest sets an attendance PIN (6 digits).
type AttendancePINRequest struct {
	PIN        string `json:"pin"`
	CurrentPIN string `json:"currentPin,omitempty" doc:"Required when changing your own PIN in ESS"`
}

// AttendanceConsentRequest records (or withdraws) the written biometric consent.
type AttendanceConsentRequest struct {
	Consent  bool       `json:"consent" doc:"false = consent withdrawn: the employee is removed from the biometric devices"`
	SignedOn string     `json:"signedOn,omitempty" doc:"Date of the signed consent (required when consenting)"`
	FileID   *uuid.UUID `json:"fileId,omitempty" doc:"Scan of the signed consent (POST /api/v1/hris/document-files)"`
}

// AttendanceDeviceSyncResult is the outcome of an employee list push.
type AttendanceDeviceSyncResult struct {
	DeviceID  uuid.UUID  `json:"deviceId"`
	Vendor    string     `json:"vendor"`
	Users     int        `json:"users" doc:"Employees and partners with biometric consent sent to the device"`
	Partners  int        `json:"partners" doc:"Partner caddies and instructors among the users (FR-ATT-08)"`
	Removals  int        `json:"removals" doc:"Leavers / withdrawn consents removed from the device"`
	CommandID *uuid.UUID `json:"commandId" doc:"Bridge command (real devices)"`
	Status    string     `json:"status" enum:"synced,queued"`
}

// AttendanceDeviceEventInput is one event of a device.
type AttendanceDeviceEventInput struct {
	EventID      string `json:"eventId" doc:"Device event id (idempotency)"`
	DeviceUserNo string `json:"deviceUserNo"`
	OccurredAt   string `json:"occurredAt" doc:"RFC 3339"`
	Method       string `json:"method" enum:"face_recognition,fingerprint"`
	Direction    string `json:"direction,omitempty" enum:"in,out"`
}

// AttendanceDeviceEventsRequest is the push of the bridge agent.
type AttendanceDeviceEventsRequest struct {
	DeviceSerial string                       `json:"deviceSerial,omitempty"`
	DeviceCode   string                       `json:"deviceCode,omitempty"`
	Events       []AttendanceDeviceEventInput `json:"events"`
}

// AttendanceDeviceEventsResult answers each pushed event.
type AttendanceDeviceEventsResult struct {
	Results []AttendanceSyncItemResult `json:"results"`
}

// AttendanceSimulateRequest fires an event on a mock device (trial adapter).
type AttendanceSimulateRequest struct {
	EmployeeID uuid.UUID `json:"employeeId"`
	Method     string    `json:"method,omitempty" enum:"face_recognition,fingerprint"`
	Direction  string    `json:"direction,omitempty" enum:"in,out"`
	OccurredAt string    `json:"occurredAt,omitempty" doc:"RFC 3339 (default now)"`
	EventID    string    `json:"eventId,omitempty" doc:"Device event id; resending it is idempotent"`
}

func (m *Module) registerDevices(reg *route.Registry) {
	tag := "HRIS Attendance"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/attendance-profiles", Summary: "Attendance profiles of the employees",
		Permission: PermProfileView, Response: AttendanceProfile{}, List: true, Query: []route.Param{{Name: "q"}, {Name: "orgUnitId"}},
		Handler: listRead(m.DB, m.profilesHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/attendance-profiles/{id}:set-pin", Summary: "Reset the attendance PIN of an employee",
		Permission: PermProfileManage, Request: AttendancePINRequest{}, Response: AttendanceProfile{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.hrSetPINHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/attendance-profiles/{id}:consent",
		Summary: "Record or withdraw the written biometric consent", Permission: PermProfileManage, Request: AttendanceConsentRequest{}, Response: AttendanceProfile{},
		Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.consentHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/attendance-devices/{id}:sync-employees",
		Summary: "Send the employee list (no biometrics) to a device", Permission: PermProfileManage, Request: handle.Empty{}, Response: AttendanceDeviceSyncResult{},
		Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.syncEmployeesHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/attendance-devices/{id}:simulate",
		Summary: "Fire a face / fingerprint event on a mock device (trial adapter)", Permission: PermAttendanceRecord, Request: AttendanceSimulateRequest{},
		Response: AttendanceClockResult{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.simulateHTTP)})
	add(reg, "Integrations", route.Route{Method: http.MethodPost, Path: "/api/v1/bridge/hris/attendance-events",
		Summary: "Attendance events of a device (bridge agent bearer token)", Auth: route.AuthSignature, Request: AttendanceDeviceEventsRequest{},
		Response: AttendanceDeviceEventsResult{}, Status: http.StatusOK, Handler: m.bridgeEventsHTTP})
}

func (m *Module) profilesHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]AttendanceProfile, error) {
	unit, err := handle.QueryUUID(r, "orgUnitId")
	if err != nil {
		return nil, err
	}
	q := "%" + strings.TrimSpace(r.URL.Query().Get("q")) + "%"
	return handle.List[AttendanceProfile](tx.Query(ctx, profileSelect+` WHERE e.property_id = $1 AND e.archived_at IS NULL AND e.status = 'active'
		AND ($2::uuid IS NULL OR e.org_unit_id = $2) AND (e.full_name ILIKE $3 OR e.employee_no ILIKE $3) ORDER BY e.full_name LIMIT 1000`,
		handle.Property(ctx), unit, q))
}

func (m *Module) loadProfile(ctx context.Context, q dbtx.Querier, employeeID uuid.UUID) (AttendanceProfile, error) {
	return getOne[AttendanceProfile]("employee")(q.Query(ctx, profileSelect+` WHERE e.id = $1`, employeeID))
}

var digitsRe = regexp.MustCompile(`\d+`)

// ensureProfile creates the attendance profile of an employee with the next
// free device user number (the digits of the employee number when free).
func ensureProfile(ctx context.Context, tx pgx.Tx, emp hris.Employee) (string, error) {
	var no string
	err := tx.QueryRow(ctx, `SELECT device_user_no FROM hris.attendance_profiles WHERE employee_id = $1`, emp.ID).Scan(&no)
	if err == nil {
		return no, nil
	}
	if !dbtx.IsNoRows(err) {
		return "", err
	}
	cand := ""
	if d := digitsRe.FindAllString(emp.EmployeeNo, -1); len(d) > 0 {
		if n, err := strconv.Atoi(d[len(d)-1]); err == nil && n > 0 {
			cand = strconv.Itoa(n)
		}
	}
	// one namespace with the partners' numbers (p5_partners.go)
	var taken bool
	if cand != "" {
		if taken, err = deviceUserNoTaken(ctx, tx, emp.PropertyID, cand); err != nil {
			return "", err
		}
	}
	if cand == "" || taken {
		var n int64
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(device_user_no::bigint) FILTER (WHERE device_user_no ~ '^[0-9]{1,15}$'), 0) + 1
			FROM hris.attendance_profiles WHERE property_id = $1`, emp.PropertyID).Scan(&n); err != nil {
			return "", err
		}
		for {
			cand = strconv.FormatInt(n, 10)
			if taken, err = deviceUserNoTaken(ctx, tx, emp.PropertyID, cand); err != nil {
				return "", err
			}
			if !taken {
				break
			}
			n++
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO hris.attendance_profiles (employee_id, property_id, device_user_no) VALUES ($1,$2,$3)`, emp.ID, emp.PropertyID, cand)
	return cand, err
}

// setPIN stores a new attendance PIN.
func setPIN(ctx context.Context, tx pgx.Tx, emp hris.Employee, pin string) error {
	if !pinRe.MatchString(pin) {
		return handle.Invalid("pin", "invalid", "the PIN has 6 digits")
	}
	if strings.Count(pin, pin[:1]) == 6 || strings.Contains("0123456789", pin) || strings.Contains("9876543210", pin) {
		return handle.Invalid("pin", "weak", "choose a PIN that is not a repeated digit or a sequence")
	}
	if _, err := ensureProfile(ctx, tx, emp); err != nil {
		return err
	}
	hash, err := password.Hash(pin)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE hris.attendance_profiles SET pin_hash = $2, pin_set_at = now(), failed_pin_count = 0, locked_until = NULL WHERE employee_id = $1`,
		emp.ID, hash)
	return err
}

func (m *Module) hrSetPINHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req AttendancePINRequest) (AttendanceProfile, error) {
	property := handle.Property(ctx)
	eid, err := handle.ID(r)
	if err != nil {
		return AttendanceProfile{}, err
	}
	emp, err := employeeAt(ctx, tx, property, eid)
	if err != nil {
		return AttendanceProfile{}, err
	}
	if err := setPIN(ctx, tx, emp, req.PIN); err != nil {
		return AttendanceProfile{}, err
	}
	p, err := m.loadProfile(ctx, tx, eid)
	if err != nil {
		return p, err
	}
	return p, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "reset_pin", Category: audit.CategorySecurity, EntityType: "hris.attendance_profile",
		EntityID: eid.String(), EntityLabel: emp.EmployeeNo + " · " + emp.FullName, PropertyID: &property})
}

func (m *Module) consentHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req AttendanceConsentRequest) (AttendanceProfile, error) {
	property := handle.Property(ctx)
	eid, err := handle.ID(r)
	if err != nil {
		return AttendanceProfile{}, err
	}
	emp, err := employeeAt(ctx, tx, property, eid)
	if err != nil {
		return AttendanceProfile{}, err
	}
	before, err := m.loadProfile(ctx, tx, eid)
	if err != nil {
		return before, err
	}
	if _, err := ensureProfile(ctx, tx, emp); err != nil {
		return before, err
	}
	if req.Consent {
		signed, err := mustDate("signedOn", req.SignedOn)
		if err != nil {
			return before, err
		}
		if req.FileID != nil {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.files WHERE id = $1)`, *req.FileID).Scan(&ok); err != nil || !ok {
				return before, handle.Invalid("fileId", "not_found", "upload the signed consent first")
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.attendance_profiles SET biometric_consent = true, consent_signed_on = $2, consent_file_id = coalesce($3, consent_file_id),
			consent_withdrawn_at = NULL, removal_requested_at = NULL WHERE employee_id = $1`, eid, ymd(signed), req.FileID); err != nil {
			return before, err
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE hris.attendance_profiles SET biometric_consent = false, consent_withdrawn_at = now(), removal_requested_at = now()
			WHERE employee_id = $1`, eid); err != nil {
			return before, err
		}
		if err := m.queueRemoval(ctx, tx, emp); err != nil {
			return before, err
		}
	}
	after, err := m.loadProfile(ctx, tx, eid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: map[bool]string{true: "consent", false: "consent_withdrawn"}[req.Consent],
		EntityType: "hris.attendance_profile", EntityID: eid.String(), EntityLabel: emp.EmployeeNo + " · " + emp.FullName, PropertyID: &property,
		Before: map[string]any{"biometricConsent": before.BiometricConsent}, After: map[string]any{"biometricConsent": after.BiometricConsent,
			"signedOn": after.ConsentSignedOn}})
}

type deviceRow struct {
	ID         uuid.UUID
	PropertyID uuid.UUID
	Code, Name string
	Kind       string
	Vendor     string
	Methods    []string
	Agent      *uuid.UUID
	Status     string
}

func loadDevice(ctx context.Context, q dbtx.Querier, did uuid.UUID) (deviceRow, error) {
	var d deviceRow
	err := q.QueryRow(ctx, `SELECT id, property_id, code, name, device_kind, vendor, methods, bridge_agent_id, status FROM hris.attendance_devices
		WHERE id = $1 AND archived_at IS NULL`, did).Scan(&d.ID, &d.PropertyID, &d.Code, &d.Name, &d.Kind, &d.Vendor, &d.Methods, &d.Agent, &d.Status)
	if dbtx.IsNoRows(err) {
		return d, errs.NotFound("attendance device")
	}
	return d, err
}

func (m *Module) syncEmployeesHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (AttendanceDeviceSyncResult, error) {
	property := handle.Property(ctx)
	did, err := handle.ID(r)
	if err != nil {
		return AttendanceDeviceSyncResult{}, err
	}
	d, err := loadDevice(ctx, tx, did)
	if err != nil || d.PropertyID != property {
		return AttendanceDeviceSyncResult{}, errs.NotFound("attendance device")
	}
	if d.Kind != "biometric" || d.Status != "active" {
		return AttendanceDeviceSyncResult{}, errs.Conflict("not_biometric", "only an active biometric device keeps an employee list")
	}
	// every active employee gets a device number; consenting ones are sent
	emps, err := hris.Employees(ctx, tx, hris.EmployeeFilter{PropertyID: property, ActiveOn: ptrTime(today(ctx, tx, property))})
	if err != nil {
		return AttendanceDeviceSyncResult{}, err
	}
	for _, e := range emps {
		if _, err := ensureProfile(ctx, tx, e); err != nil {
			return AttendanceDeviceSyncResult{}, err
		}
	}
	type user struct {
		No   string `json:"deviceUserNo"`
		Name string `json:"name"`
	}
	var users []user
	rows, err := tx.Query(ctx, `SELECT p.device_user_no, e.full_name FROM hris.attendance_profiles p JOIN hris.employees e ON e.id = p.employee_id
		WHERE p.property_id = $1 AND p.biometric_consent AND e.status = 'active' AND e.archived_at IS NULL ORDER BY p.device_user_no`, property)
	if err != nil {
		return AttendanceDeviceSyncResult{}, err
	}
	for rows.Next() {
		var u user
		if err := rows.Scan(&u.No, &u.Name); err != nil {
			rows.Close()
			return AttendanceDeviceSyncResult{}, err
		}
		users = append(users, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return AttendanceDeviceSyncResult{}, err
	}
	var removals []string
	rrows, err := tx.Query(ctx, `SELECT device_user_no FROM hris.attendance_profiles WHERE property_id = $1 AND removal_requested_at IS NOT NULL`, property)
	if err != nil {
		return AttendanceDeviceSyncResult{}, err
	}
	if removals, err = pgx.CollectRows(rrows, pgx.RowTo[string]); err != nil {
		return AttendanceDeviceSyncResult{}, err
	}
	// partner caddies and instructors enrolled for device clock-in (FR-ATT-08)
	partners, partnerRemovals, err := partnerDeviceUsers(ctx, tx, property)
	if err != nil {
		return AttendanceDeviceSyncResult{}, err
	}
	for _, p := range partners {
		users = append(users, user{No: p[0], Name: p[1]})
	}
	removals = append(removals, partnerRemovals...)
	res := AttendanceDeviceSyncResult{DeviceID: d.ID, Vendor: d.Vendor, Users: len(users), Partners: len(partners), Removals: len(removals), Status: "synced"}
	if d.Vendor == "mock" {
		// the trial adapter enrols at once
		if _, err := tx.Exec(ctx, `UPDATE hris.attendance_profiles SET enrolled_at = coalesce(enrolled_at, now()) WHERE property_id = $1 AND biometric_consent`,
			property); err != nil {
			return res, err
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.partner_attendance_profiles SET enrolled_at = coalesce(enrolled_at, now())
			WHERE property_id = $1 AND biometric_consent AND status = 'active'`, property); err != nil {
			return res, err
		}
	} else {
		cmd, err := integration.Enqueue(ctx, tx, integration.CommandRequest{PropertyID: property, Device: d.Code, Command: "sync_users",
			Payload: map[string]any{"users": users, "remove": removals}, SourceType: "hris.attendance_device", SourceID: &d.ID, TTL: 10 * time.Minute})
		if err != nil {
			return res, err
		}
		if cmd == nil {
			return res, errs.Conflict("device_offline", "no online bridge agent serves the device "+d.Code)
		}
		res.CommandID, res.Status = &cmd.ID, "queued"
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.attendance_devices SET last_sync_at = now() WHERE id = $1`, d.ID); err != nil {
		return res, err
	}
	return res, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "sync_employees", EntityType: KeyAttendanceDevice, EntityID: d.ID.String(),
		EntityLabel: d.Code + " · " + d.Name, PropertyID: &property, After: res})
}

func ptrTime(t time.Time) *time.Time { return &t }

// queueRemoval removes an employee from the real biometric devices (leaver
// or withdrawn consent, §16 #10: templates deleted ≤ 30 days).
func (m *Module) queueRemoval(ctx context.Context, tx pgx.Tx, emp hris.Employee) error {
	var no string
	if err := tx.QueryRow(ctx, `SELECT device_user_no FROM hris.attendance_profiles WHERE employee_id = $1`, emp.ID).Scan(&no); err != nil {
		if dbtx.IsNoRows(err) {
			return nil
		}
		return err
	}
	return m.queueDeviceRemoval(ctx, tx, emp.PropertyID, no)
}

// queueDeviceRemoval removes a device user number from the real biometric
// devices of a property (employee or partner).
func (m *Module) queueDeviceRemoval(ctx context.Context, tx pgx.Tx, property uuid.UUID, no string) error {
	rows, err := tx.Query(ctx, `SELECT id, code FROM hris.attendance_devices WHERE property_id = $1 AND device_kind = 'biometric' AND vendor <> 'mock'
		AND status = 'active' AND archived_at IS NULL`, property)
	if err != nil {
		return err
	}
	type dev struct {
		id   uuid.UUID
		code string
	}
	var devs []dev
	for rows.Next() {
		var d dev
		if err := rows.Scan(&d.id, &d.code); err != nil {
			rows.Close()
			return err
		}
		devs = append(devs, d)
	}
	rows.Close()
	for _, d := range devs {
		if _, err := integration.Enqueue(ctx, tx, integration.CommandRequest{PropertyID: property, Device: d.code, Command: "delete_users",
			Payload: map[string]any{"deviceUserNos": []string{no}}, SourceType: "hris.attendance_device", SourceID: &d.id, TTL: 24 * time.Hour}); err != nil {
			return err
		}
	}
	return rows.Err()
}

// OnEmployeeTerminated removes a leaver from the biometric devices.
func (m *Module) OnEmployeeTerminated(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p hris.EmployeeTerminated
	if err := e.Decode(&p); err != nil {
		return nil //nolint:nilerr // foreign payload
	}
	ctx = reqctx.WithProperty(dbtx.System(ctx), p.PropertyID)
	if _, err := tx.Exec(ctx, `UPDATE hris.attendance_profiles SET removal_requested_at = coalesce(removal_requested_at, now()), pin_hash = NULL
		WHERE employee_id = $1`, p.EmployeeID); err != nil {
		return err
	}
	emp, err := hris.EmployeeByID(ctx, tx, p.EmployeeID)
	if err != nil {
		return nil //nolint:nilerr // archived since
	}
	return m.queueRemoval(ctx, tx, emp)
}

// ingestDeviceEvent records one device event.
func (m *Module) ingestDeviceEvent(ctx context.Context, tx pgx.Tx, d deviceRow, ev AttendanceDeviceEventInput) (clockOutcome, error) {
	if strings.TrimSpace(ev.EventID) == "" {
		return clockOutcome{}, handle.Invalid("eventId", "required", "the device event id is required")
	}
	at, err := parseInstant("occurredAt", ev.OccurredAt)
	if err != nil {
		return clockOutcome{}, err
	}
	if ev.Method != hris.MethodFaceRecognition && ev.Method != hris.MethodFingerprint {
		return clockOutcome{}, enumErr("method", []string{hris.MethodFaceRecognition, hris.MethodFingerprint})
	}
	if len(d.Methods) > 0 && !containsStr(d.Methods, ev.Method) {
		return clockOutcome{}, errs.Conflict("method_not_allowed", "the device "+d.Code+" is not set up for "+strings.ReplaceAll(ev.Method, "_", " "))
	}
	var eid uuid.UUID
	var consent bool
	err = tx.QueryRow(ctx, `SELECT employee_id, biometric_consent FROM hris.attendance_profiles WHERE property_id = $1 AND device_user_no = $2`,
		d.PropertyID, strings.TrimSpace(ev.DeviceUserNo)).Scan(&eid, &consent)
	if dbtx.IsNoRows(err) {
		return clockOutcome{}, errs.NotFound("device user " + ev.DeviceUserNo)
	}
	if err != nil {
		return clockOutcome{}, err
	}
	cfg, _, err := hris.LoadAttendanceConfiguration(ctx, tx, d.PropertyID, clock.Now())
	if err != nil {
		return clockOutcome{}, err
	}
	if cfg.BiometricConsentRequired && !consent {
		return clockOutcome{}, errs.Conflict("no_consent", "no written biometric consent on file for device user "+ev.DeviceUserNo)
	}
	emp, err := hris.EmployeeByID(ctx, tx, eid)
	if err != nil {
		return clockOutcome{}, err
	}
	offline := clock.Now().Sub(at) > 10*time.Minute
	return m.recordEvent(ctx, tx, clockInput{Emp: emp, Direction: ev.Direction, At: at, Method: ev.Method, Source: "device", DeviceID: &d.ID,
		ClientEventID: d.Code + ":" + strings.TrimSpace(ev.EventID), Offline: offline})
}

func containsStr(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func (m *Module) simulateHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req AttendanceSimulateRequest) (AttendanceClockResult, error) {
	property := handle.Property(ctx)
	did, err := handle.ID(r)
	if err != nil {
		return AttendanceClockResult{}, err
	}
	d, err := loadDevice(ctx, tx, did)
	if err != nil || d.PropertyID != property {
		return AttendanceClockResult{}, errs.NotFound("attendance device")
	}
	if d.Vendor != "mock" || d.Kind != "biometric" || d.Status != "active" {
		return AttendanceClockResult{}, errs.Conflict("not_mock", "only an active mock biometric device can be simulated")
	}
	emp, err := employeeAt(ctx, tx, property, req.EmployeeID)
	if err != nil {
		return AttendanceClockResult{}, err
	}
	no, err := ensureProfile(ctx, tx, emp)
	if err != nil {
		return AttendanceClockResult{}, err
	}
	at := clock.Now()
	if req.OccurredAt != "" {
		if at, err = parseInstant("occurredAt", req.OccurredAt); err != nil {
			return AttendanceClockResult{}, err
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
	out, err := m.ingestDeviceEvent(ctx, tx, d, AttendanceDeviceEventInput{EventID: evID, DeviceUserNo: no, OccurredAt: at.UTC().Format(time.RFC3339), Method: method,
		Direction: req.Direction})
	if err != nil {
		return AttendanceClockResult{}, err
	}
	return out.result(), audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "simulate", EntityType: KeyAttendanceDevice, EntityID: d.ID.String(),
		EntityLabel: d.Code, PropertyID: &property, After: map[string]any{"employee": emp.EmployeeNo, "method": method, "duplicate": out.Duplicate}})
}

// bridgeEventsHTTP receives the events a bridge agent read from a device.
func (m *Module) bridgeEventsHTTP(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if !strings.HasPrefix(token, "ocb_") {
		httpx.WriteError(w, r, errs.Unauthorized("agent token required"))
		return
	}
	var req AttendanceDeviceEventsRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := dbtx.System(r.Context())
	var agent, property uuid.UUID
	var agentName string
	if err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id, property_id, name FROM platform.bridge_agents WHERE token_hash = $1 AND status = 'active'`,
			secret.HashToken(token)).Scan(&agent, &property, &agentName)
	}); err != nil {
		httpx.WriteError(w, r, errs.Unauthorized("unknown or inactive agent"))
		return
	}
	ctx = reqctx.WithProperty(ctx, property)
	if len(req.Events) == 0 || len(req.Events) > 500 {
		httpx.WriteError(w, r, handle.Invalid("events", "invalid", "send 1 to 500 events"))
		return
	}
	out := AttendanceDeviceEventsResult{Results: []AttendanceSyncItemResult{}}
	err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var did uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM hris.attendance_devices WHERE property_id = $1 AND device_kind = 'biometric' AND status = 'active'
			AND archived_at IS NULL AND ((nullif($2, '') IS NOT NULL AND serial_no = $2) OR (nullif($3, '') IS NOT NULL AND code = upper($3)))
			AND (bridge_agent_id IS NULL OR bridge_agent_id = $4)`, property, req.DeviceSerial, req.DeviceCode, agent).Scan(&did)
		if dbtx.IsNoRows(err) {
			return errs.NotFound("attendance device served by this agent")
		}
		if err != nil {
			return err
		}
		d, err := loadDevice(ctx, tx, did)
		if err != nil {
			return err
		}
		accepted := 0
		for _, ev := range req.Events {
			res := AttendanceSyncItemResult{ClientEventID: ev.EventID}
			sp, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			if p, err := partnerByDeviceNo(ctx, sp, d.PropertyID, ev.DeviceUserNo); err != nil {
				_ = sp.Rollback(ctx)
				return err
			} else if p != nil {
				// a partner caddy / instructor (FR-ATT-08)
				pr, err := m.ingestPartnerEvent(ctx, sp, d, *p, ev)
				if err != nil {
					_ = sp.Rollback(ctx)
					if pe, ok := errs.As(err); ok && pe.Kind != errs.KindInternal {
						res.Status, res.Error = "rejected", pe.Message
						out.Results = append(out.Results, res)
						continue
					}
					return err
				}
				if err := sp.Commit(ctx); err != nil {
					return err
				}
				res.PartnerResult, res.Status = &pr, map[bool]string{true: "duplicate", false: "accepted"}[pr.Duplicate]
				if !pr.Duplicate {
					accepted++
				}
				out.Results = append(out.Results, res)
				continue
			}
			o, err := m.ingestDeviceEvent(ctx, sp, d, ev)
			if err != nil {
				_ = sp.Rollback(ctx)
				if pe, ok := errs.As(err); ok && pe.Kind != errs.KindInternal {
					res.Status, res.Error = "rejected", pe.Message
					out.Results = append(out.Results, res)
					continue
				}
				return err
			}
			if err := sp.Commit(ctx); err != nil {
				return err
			}
			cr := o.result()
			res.Result, res.Status = &cr, map[bool]string{true: "duplicate", false: "accepted"}[o.Duplicate]
			if !o.Duplicate {
				accepted++
			}
			out.Results = append(out.Results, res)
		}
		return audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "device_events", EntityType: KeyAttendanceDevice, EntityID: d.ID.String(),
			EntityLabel: d.Code, PropertyID: &property, ActorName: "bridge agent " + agentName,
			After: map[string]any{"events": len(req.Events), "accepted": accepted}})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
