package hrtime

// Master data of time & attendance through the generic resource engine:
// shift templates (FR-SCH-01), staffing requirements (FR-SCH-03), the HR
// holiday calendar (FR-OVT-03), geofences (FR-ATT-05) and attendance
// devices (FR-ATT-01/02, FR-INT-P5-01).

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

var codeRe = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)

func code() resource.Field {
	return resource.Field{Name: "code", Column: "code", Label: "Code", Kind: resource.String, Required: true, Max: 20, Upper: true,
		Pattern: codeRe, PatternMsg: "1–20 characters: A–Z, 0–9, - or _", Search: true}
}

func ref(n, c, label, table string, required bool) resource.Field {
	return resource.Field{Name: n, Column: c, Label: label, Kind: resource.UUID, Required: required, Filter: true,
		Ref: &resource.Ref{Table: table, SameProperty: strings.HasPrefix(table, "hris."), Label: strings.ToLower(label)}}
}

func status() resource.Field { return resource.Status("active", "inactive") }

func notes(n string) resource.Field {
	return resource.Field{Name: n, Column: map[string]string{"notes": "notes", "description": "description"}[n], Label: strings.ToUpper(n[:1]) + n[1:],
		Kind: resource.Text, Max: 2000}
}

// Resource keys.
const (
	KeyShiftTemplate       = "hris.shift_template"
	KeyStaffingRequirement = "hris.staffing_requirement"
	KeyHoliday             = "hris.holiday"
	KeyGeofence            = "hris.geofence"
	KeyAttendanceDevice    = "hris.attendance_device"
)

// DeviceMethods are the clock-in methods of attendance devices.
var DeviceMethods = []string{hris.MethodFingerprint, hris.MethodFaceRecognition, hris.MethodKioskQR, hris.MethodKioskPIN}

// Defs returns the resource definitions of time & attendance.
func (m *Module) Defs() []*resource.Def {
	templates := &resource.Def{
		Key: KeyShiftTemplate, Module: hris.Module, Perm: KeyShiftTemplate, Path: "/api/v1/hris/shift-templates", Table: "hris.shift_templates",
		Name: "Shift Template", Plural: "Shift Templates", SchemaName: "ShiftTemplate", Tag: "HRIS Schedules", PropertyScoped: true, Archive: true,
		CodeField: "code", OrderBy: "start_time, code, id",
		Fields: []resource.Field{code(), resource.Name(),
			ref("orgUnitId", "org_unit_id", "Department", "hris.org_units", false),
			{Name: "startTime", Column: "start_time", Label: "Start", Kind: resource.Time, Required: true},
			{Name: "endTime", Column: "end_time", Label: "End (before the start = next day)", Kind: resource.Time, Required: true},
			{Name: "breakMinutes", Column: "break_minutes", Label: "Break (minutes)", Kind: resource.Int, Default: int64(60), Min: resource.Min(0),
				MaxN: resource.Max(240)},
			{Name: "crossesMidnight", Column: "crosses_midnight", Label: "Crosses Midnight", Kind: resource.Bool, ReadOnly: true},
			{Name: "workMinutes", Column: "work_minutes", Label: "Working Minutes", Kind: resource.Int, ReadOnly: true},
			{Name: "workforceRole", Column: "workforce_role", Label: "Workforce Role (certification check)", Kind: resource.Enum, Enum: hris.WorkforceRoles,
				Filter: true},
			{Name: "color", Column: "color", Label: "Color", Kind: resource.String, Max: 20},
			notes("description"), status()},
		Hooks: resource.Hooks{BeforeWrite: templateBeforeWrite},
	}
	staffing := &resource.Def{
		Key: KeyStaffingRequirement, Module: hris.Module, Perm: KeyStaffingRequirement, Path: "/api/v1/hris/staffing-requirements",
		Table: "hris.staffing_requirements", Name: "Staffing Requirement", Plural: "Staffing Requirements", SchemaName: "ShiftStaffingRequirement",
		Tag: "HRIS Schedules", PropertyScoped: true, Archive: true, OrderBy: "created_at, id",
		Fields: []resource.Field{
			ref("orgUnitId", "org_unit_id", "Department", "hris.org_units", true),
			ref("positionId", "position_id", "Position (empty = any)", "hris.positions", false),
			ref("shiftTemplateId", "shift_template_id", "Shift (empty = any)", "hris.shift_templates", false),
			{Name: "weekdays", Column: "weekdays", Label: "Weekdays (1 = Monday … 7 = Sunday)", Kind: resource.IntList, Default: []int{1, 2, 3, 4, 5, 6, 7},
				Min: resource.Min(1), MaxN: resource.Max(7)},
			{Name: "minStaff", Column: "min_staff", Label: "Minimum Staff", Kind: resource.Int, Required: true, Min: resource.Min(1), MaxN: resource.Max(500)},
			notes("notes"), status()},
	}
	holidays := &resource.Def{
		Key: KeyHoliday, Module: hris.Module, Perm: KeyHoliday, Path: "/api/v1/hris/holidays", Table: "hris.holidays", Name: "Holiday",
		Plural: "Holidays", SchemaName: "HRHoliday", Tag: "HRIS Leave", PropertyScoped: true, Archive: true, OrderBy: "holiday_date, id",
		Fields: []resource.Field{
			{Name: "holidayDate", Column: "holiday_date", Label: "Date", Kind: resource.Date, Required: true, Filter: true},
			resource.Name(),
			{Name: "kind", Column: "kind", Label: "Kind", Kind: resource.Enum, Enum: []string{"public_holiday", "collective_leave", "company_holiday"},
				Default: "public_holiday", Filter: true},
			{Name: "deductsAnnualLeave", Column: "deducts_annual_leave", Label: "Deducts Annual Leave (collective leave)", Kind: resource.Bool, Default: false},
			notes("notes"), status()},
		Hooks: resource.Hooks{BeforeWrite: holidayBeforeWrite},
	}
	geofences := &resource.Def{
		Key: KeyGeofence, Module: hris.Module, Perm: KeyGeofence, Path: "/api/v1/hris/geofences", Table: "hris.geofences", Name: "Geofence",
		Plural: "Geofences", SchemaName: "AttendanceGeofence", Tag: "HRIS Attendance", PropertyScoped: true, Archive: true, CodeField: "code",
		OrderBy: "code, id",
		Fields: []resource.Field{code(), resource.Name(),
			{Name: "latitude", Column: "latitude", Label: "Latitude", Kind: resource.Decimal, Required: true, Min: resource.Min(-90), MaxN: resource.Max(90)},
			{Name: "longitude", Column: "longitude", Label: "Longitude", Kind: resource.Decimal, Required: true, Min: resource.Min(-180),
				MaxN: resource.Max(180)},
			{Name: "radiusMeters", Column: "radius_meters", Label: "Radius (m)", Kind: resource.Int, Default: int64(300), Min: resource.Min(20),
				MaxN: resource.Max(20000)},
			{Name: "orgUnitCodes", Column: "org_unit_codes", Label: "Org Units (codes; empty = all)", Kind: resource.StringList, Upper: true,
				Default: []string{}},
			notes("notes"), status()},
	}
	devices := &resource.Def{
		Key: KeyAttendanceDevice, Module: hris.Module, Perm: KeyAttendanceDevice, Path: "/api/v1/hris/attendance-devices", Table: "hris.attendance_devices",
		Name: "Attendance Device", Plural: "Attendance Devices", SchemaName: "AttendanceDevice", Tag: "HRIS Attendance", PropertyScoped: true,
		Archive: true, CodeField: "code", OrderBy: "code, id",
		Fields: []resource.Field{code(), resource.Name(),
			{Name: "deviceKind", Column: "device_kind", Label: "Kind", Kind: resource.Enum, Enum: []string{"biometric", "kiosk"}, Required: true, Filter: true,
				CreateOnly: true},
			{Name: "locationPoint", Column: "location_point", Label: "Location", Kind: resource.String, Max: 120},
			{Name: "methods", Column: "methods", Label: "Methods", Kind: resource.StringList, Enum: DeviceMethods, Default: []string{}},
			{Name: "vendor", Column: "vendor", Label: "Vendor (mock = trial adapter)", Kind: resource.Enum, Enum: []string{"mock", "zkteco", "other"},
				Default: "mock"},
			{Name: "serialNo", Column: "serial_no", Label: "Serial No.", Kind: resource.String, Max: 60},
			ref("platformDeviceId", "platform_device_id", "Registered Device (kiosk)", "platform.devices", false),
			ref("bridgeAgentId", "bridge_agent_id", "Bridge Agent", "platform.bridge_agents", false),
			ref("geofenceId", "geofence_id", "Geofence", "hris.geofences", false),
			{Name: "lastEventAt", Column: "last_event_at", Label: "Last Event", Kind: resource.Timestamp, ReadOnly: true},
			{Name: "lastSyncAt", Column: "last_sync_at", Label: "Last Employee Sync", Kind: resource.Timestamp, ReadOnly: true},
			notes("notes"), status()},
		Hooks: resource.Hooks{BeforeWrite: deviceBeforeWrite},
	}
	return []*resource.Def{templates, staffing, holidays, geofences, devices}
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// value returns the new value of a field, else the stored one.
func value(values, before map[string]any, k string) any {
	if v, ok := values[k]; ok {
		return v
	}
	if before != nil {
		return before[k]
	}
	return nil
}

func templateBeforeWrite(_ context.Context, _ pgx.Tx, values, before map[string]any) error {
	s, e := str(value(values, before, "startTime")), str(value(values, before, "endTime"))
	if s != "" && s == e {
		return handle.Invalid("endTime", "invalid", "the shift must end at another time than it starts")
	}
	return nil
}

func holidayBeforeWrite(_ context.Context, _ pgx.Tx, values, before map[string]any) error {
	if b, _ := value(values, before, "deductsAnnualLeave").(bool); b && str(value(values, before, "kind")) != "collective_leave" {
		return handle.Invalid("deductsAnnualLeave", "invalid", "only collective leave (cuti bersama) deducts annual leave")
	}
	return nil
}

func deviceBeforeWrite(ctx context.Context, tx pgx.Tx, values, before map[string]any) error {
	kind := str(value(values, before, "deviceKind"))
	var methods []string
	switch v := value(values, before, "methods").(type) {
	case []string:
		methods = v
	case []any:
		for _, x := range v {
			methods = append(methods, str(x))
		}
	}
	for _, mth := range methods {
		biometric := mth == hris.MethodFingerprint || mth == hris.MethodFaceRecognition
		if kind == "kiosk" && biometric || kind == "biometric" && !biometric {
			return handle.Invalid("methods", "invalid", "a kiosk clocks with QR / PIN, a biometric device with fingerprint / face recognition")
		}
	}
	if len(methods) == 0 {
		if kind == "kiosk" {
			values["methods"] = []string{hris.MethodKioskQR, hris.MethodKioskPIN}
		} else {
			values["methods"] = []string{hris.MethodFaceRecognition, hris.MethodFingerprint}
		}
	}
	if pd, ok := values["platformDeviceId"]; ok && pd != nil && kind != "kiosk" {
		return handle.Invalid("platformDeviceId", "invalid", "only a kiosk runs on a registered ops device")
	}
	if pd := str(value(values, before, "platformDeviceId")); pd != "" {
		pid, _ := reqctx.Property(ctx)
		var dt string
		if err := tx.QueryRow(ctx, `SELECT device_type FROM platform.devices WHERE id = $1 AND property_id = $2`, pd, pid).Scan(&dt); err != nil {
			return handle.Invalid("platformDeviceId", "not_found", "registered device not found at this property")
		}
		if !slices.Contains([]string{"kiosk", "tablet", "other"}, dt) {
			return handle.Invalid("platformDeviceId", "invalid", "the registered device must be a kiosk or tablet")
		}
	}
	return nil
}
