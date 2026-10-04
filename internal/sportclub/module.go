// Package sportclub is Sport Club & Facility (PRD P2 EP-14) and Classes &
// Training (EP-15): facilities and courts, court booking, entry tickets,
// facility access, lockers, class programs, schedules, sessions,
// registration, session quota, attendance and instructor fees.
package sportclub

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/resource"
	"oneclub/internal/reservation"
)

// FacilityTypes of Modern Golf (PRD P2 EP-14).
var FacilityTypes = []string{"tennis", "squash", "table_tennis", "badminton", "futsal", "basketball", "volleyball", "swimming_pool", "gym",
	"aerobic_studio", "spa", "sauna", "steam_room", "jacuzzi", "massage", "other"}

var Facilities = &resource.Def{
	Key: "sportclub.facility", Module: "sportclub", Perm: "sportclub.facility", Path: "/api/v1/sportclub/facilities", Table: "sportclub.facilities",
	Name: "Facility", Plural: "Facilities", Tag: "Sport Club", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "facilityType", Column: "facility_type", Label: "Facility Type", Kind: resource.String, Max: 60, Filter: true},
		{Name: "venueId", Column: "venue_id", Label: "Venue", Kind: resource.UUID, Ref: &resource.Ref{Table: "platform.venues", SameProperty: true, Label: "venue"}},
		{Name: "capacity", Column: "capacity", Label: "Capacity", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "usageMode", Column: "usage_mode", Label: "Usage Mode", Kind: resource.Enum, Enum: []string{"slot_booking", "entry", "class"}, Default: "entry", Filter: true},
		{Name: "minAge", Column: "min_age", Label: "Minimum Age", Kind: resource.Int, Min: resource.Min(0)},
		{Name: "maxAge", Column: "max_age", Label: "Maximum Age", Kind: resource.Int, Min: resource.Min(0)},
		{Name: "openingHours", Column: "opening_hours", Label: "Opening Hours per Day Type", Kind: resource.JSON, Default: "{}"},
		{Name: "priceItem", Column: "price_item", Label: "Pricing Item", Kind: resource.String, Max: 60, Upper: true},
		{Name: "resourceId", Column: "resource_id", Label: "Bookable Resource", Kind: resource.UUID, ReadOnly: true},
		resource.Status("active", "inactive"), resource.Attributes()},
}

var Courts = &resource.Def{
	Key: "sportclub.court", Module: "sportclub", Perm: "sportclub.court", Path: "/api/v1/sportclub/courts", Table: "sportclub.courts",
	Name: "Court", Plural: "Courts", Tag: "Sport Club", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "facilityId", Column: "facility_id", Label: "Facility", Kind: resource.UUID, Required: true, Filter: true,
			Ref: &resource.Ref{Table: "sportclub.facilities", SameProperty: true, Label: "facility"}},
		{Name: "surface", Column: "surface", Label: "Surface", Kind: resource.String, Max: 60},
		{Name: "indoor", Column: "indoor", Label: "Indoor", Kind: resource.Bool, Default: false},
		{Name: "priceItem", Column: "price_item", Label: "Pricing Item (default: facility)", Kind: resource.String, Max: 60, Upper: true},
		{Name: "resourceId", Column: "resource_id", Label: "Bookable Resource", Kind: resource.UUID, ReadOnly: true},
		resource.Status("active", "inactive")},
}

var Lockers = &resource.Def{
	Key: "sportclub.locker", Module: "sportclub", Perm: "sportclub.locker", Path: "/api/v1/sportclub/lockers", Table: "sportclub.lockers",
	Name: "Locker", Plural: "Lockers", Tag: "Sport Club", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "code, id",
	Fields: []resource.Field{resource.Code("Locker No."), resource.Name(),
		{Name: "area", Column: "area", Label: "Area", Kind: resource.Enum, Enum: []string{"male", "female", "unisex"}, Default: "unisex", Filter: true},
		{Name: "facilityId", Column: "facility_id", Label: "Facility", Kind: resource.UUID, Ref: &resource.Ref{Table: "sportclub.facilities", SameProperty: true, Label: "facility"}},
		{Name: "size", Column: "size", Label: "Size", Kind: resource.String, Max: 20},
		{Name: "status", Column: "status", Label: "Status", Kind: resource.Enum, Enum: []string{"available", "occupied", "maintenance", "out_of_service"}, Default: "available", Filter: true}},
}

var Instructors = &resource.Def{
	Key: "sportclub.instructor", Module: "sportclub", Perm: "sportclub.instructor", Path: "/api/v1/sportclub/instructors", Table: "sportclub.instructors",
	Name: "Instructor", Plural: "Instructors", Tag: "Classes", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "partnership", Column: "partnership", Label: "Partnership", Kind: resource.Enum, Enum: []string{"employee", "partner"}, Default: "partner", Filter: true},
		{Name: "employeeId", Column: "employee_id", Label: "Employee", Kind: resource.UUID, Ref: &resource.Ref{Table: "platform.employees", SameProperty: true, Label: "employee"}},
		{Name: "userId", Column: "user_id", Label: "Login User", Kind: resource.UUID, Ref: &resource.Ref{Table: "platform.users", Label: "user"}},
		{Name: "phone", Column: "phone", Label: "Phone", Kind: resource.String, Max: 40},
		{Name: "email", Column: "email", Label: "E-mail", Kind: resource.Email, Max: 254},
		{Name: "disciplines", Column: "disciplines", Label: "Disciplines", Kind: resource.StringList, Default: []string{}},
		{Name: "certifications", Column: "certifications", Label: "Certifications", Kind: resource.JSONList, Default: "[]"},
		{Name: "feeScheme", Column: "fee_scheme", Label: "Fee Scheme", Kind: resource.Enum, Enum: []string{"per_session", "per_student"}, Default: "per_session"},
		{Name: "feeRate", Column: "fee_rate", Label: "Fee Rate", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		resource.Status("active", "inactive")},
}

var ClassPrograms = &resource.Def{
	Key: "sportclub.class_program", Module: "sportclub", Perm: "sportclub.class_program", Path: "/api/v1/sportclub/class-programs",
	Table: "sportclub.class_programs", Name: "Class Program", Plural: "Class Programs", Tag: "Classes", PropertyScoped: true, Archive: true,
	CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "discipline", Column: "discipline", Label: "Discipline", Kind: resource.Enum, Required: true, Filter: true,
			Enum: []string{"swimming", "tennis", "aikido", "aerobic", "gym", "badminton", "squash", "other"}},
		{Name: "level", Column: "level", Label: "Level", Kind: resource.String, Max: 40},
		{Name: "minAge", Column: "min_age", Label: "Minimum Age", Kind: resource.Int, Min: resource.Min(0)},
		{Name: "maxAge", Column: "max_age", Label: "Maximum Age", Kind: resource.Int, Min: resource.Min(0)},
		{Name: "capacity", Column: "capacity", Label: "Capacity per Session", Kind: resource.Int, Required: true, Min: resource.Min(1)},
		{Name: "durationMinutes", Column: "duration_minutes", Label: "Session Length (minutes)", Kind: resource.Int, Default: int64(60), Min: resource.Min(15)},
		{Name: "facilityId", Column: "facility_id", Label: "Facility", Kind: resource.UUID, Ref: &resource.Ref{Table: "sportclub.facilities", SameProperty: true, Label: "facility"}},
		{Name: "registrationValidMonths", Column: "registration_valid_months", Label: "Registration Valid (months)", Kind: resource.Int, Default: int64(12), Min: resource.Min(1)},
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 2000},
		{Name: "resourceId", Column: "resource_id", Label: "Bookable Resource", Kind: resource.UUID, ReadOnly: true},
		resource.Status("active", "inactive")},
}

var ClassSchedules = &resource.Def{
	Key: "sportclub.class_schedule", Module: "sportclub", Perm: "sportclub.class_schedule", Path: "/api/v1/sportclub/class-schedules",
	Table: "sportclub.class_schedules", Name: "Class Schedule", Plural: "Class Schedules", Tag: "Classes", PropertyScoped: true,
	OrderBy: "start_date DESC, start_time",
	Fields: []resource.Field{
		{Name: "programId", Column: "program_id", Label: "Class Program", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "sportclub.class_programs", SameProperty: true, Label: "class program"}},
		{Name: "instructorId", Column: "instructor_id", Label: "Instructor", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "sportclub.instructors", SameProperty: true, Label: "instructor"}},
		{Name: "facilityId", Column: "facility_id", Label: "Facility", Kind: resource.UUID, CreateOnly: true, Ref: &resource.Ref{Table: "sportclub.facilities", SameProperty: true, Label: "facility"}},
		{Name: "weekdays", Column: "weekdays", Label: "Weekdays (1 = Mon … 7 = Sun)", Kind: resource.IntList, Required: true, CreateOnly: true, Enum: []string{"1", "2", "3", "4", "5", "6", "7"}},
		{Name: "startTime", Column: "start_time", Label: "Start Time", Kind: resource.Time, Required: true, CreateOnly: true},
		{Name: "startDate", Column: "start_date", Label: "From", Kind: resource.Date, Required: true, CreateOnly: true},
		{Name: "endDate", Column: "end_date", Label: "Until", Kind: resource.Date, Required: true, CreateOnly: true},
		{Name: "capacity", Column: "capacity", Label: "Capacity (default: program)", Kind: resource.Int, CreateOnly: true, Min: resource.Min(1)},
		resource.Status("active", "inactive")},
}

// Module is the Sport Club module.
type Module struct {
	DB         *dbtx.DB
	Res        *reservation.Engine
	Billing    *billing.Service
	Commercial *commercial.Module
	Events     *outbox.Bus
	Approvals  *approval.Engine
	Notify     notify.Sender
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// hooks create the bookable resources behind courts, entry facilities and
// class programs (one Reservation Engine, FR-RSV-01).
func (m *Module) hooks() {
	Facilities.Hooks = resource.Hooks{
		AfterCreate: func(ctx context.Context, tx pgx.Tx, row map[string]any) error {
			return m.syncFacilityResource(ctx, tx, row)
		},
		AfterUpdate: func(ctx context.Context, tx pgx.Tx, _, after map[string]any) error {
			return m.syncFacilityResource(ctx, tx, after)
		},
	}
	Courts.Hooks = resource.Hooks{
		AfterCreate: func(ctx context.Context, tx pgx.Tx, row map[string]any) error {
			return m.syncCourtResource(ctx, tx, row)
		},
		AfterUpdate: func(ctx context.Context, tx pgx.Tx, _, after map[string]any) error {
			return m.syncCourtResource(ctx, tx, after)
		},
	}
	ClassPrograms.Hooks = resource.Hooks{
		AfterCreate: func(ctx context.Context, tx pgx.Tx, row map[string]any) error {
			return m.syncProgramResource(ctx, tx, row)
		},
		AfterUpdate: func(ctx context.Context, tx pgx.Tx, _, after map[string]any) error {
			return m.syncProgramResource(ctx, tx, after)
		},
	}
	ClassSchedules.Hooks = resource.Hooks{
		AfterCreate: func(ctx context.Context, tx pgx.Tx, row map[string]any) error {
			_, err := m.GenerateSessions(ctx, tx, mustUUID(row["id"]))
			return err
		},
	}
}

func intPtr(v any) *int {
	switch t := v.(type) {
	case int64:
		n := int(t)
		return &n
	case int32:
		n := int(t)
		return &n
	case int:
		return &t
	}
	return nil
}

func uuidPtr(v any) *uuidT {
	if s := str(v); s != "" {
		u, err := parseUUID(s)
		if err == nil {
			return &u
		}
	}
	return nil
}

func (m *Module) syncFacilityResource(ctx context.Context, tx pgx.Tx, row map[string]any) error {
	if str(row["usageMode"]) != "entry" {
		return nil
	}
	pid, _ := reqctx.Property(ctx)
	capacity := intPtr(row["capacity"])
	if capacity == nil {
		return nil // unlimited entry: no capacity resource
	}
	rid, err := m.Res.EnsureResource(ctx, tx, uuidPtr(row["resourceId"]), reservation.ResourceRequest{PropertyID: pid, Code: "FAC-" + str(row["code"]),
		Name: str(row["name"]), ResourceType: "facility_entry", VenueID: uuidPtr(row["venueId"]), Capacity: capacity,
		Status: activeOr(str(row["status"])), Attributes: map[string]any{"priceItem": str(row["priceItem"]), "facilityId": str(row["id"])}})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE sportclub.facilities SET resource_id = $2 WHERE id = $1`, row["id"], rid)
	return err
}

func activeOr(s string) string {
	if s == "active" {
		return "active"
	}
	return "inactive"
}

func (m *Module) syncCourtResource(ctx context.Context, tx pgx.Tx, row map[string]any) error {
	pid, _ := reqctx.Property(ctx)
	var facCode, facPrice, facType *string
	var venue *uuidT
	if err := tx.QueryRow(ctx, `SELECT code, price_item, facility_type, venue_id FROM sportclub.facilities WHERE id = $1`, row["facilityId"]).
		Scan(&facCode, &facPrice, &facType, &venue); err != nil {
		return err
	}
	item := str(row["priceItem"])
	if item == "" && facPrice != nil {
		item = *facPrice
	}
	if item == "" && facCode != nil {
		item = *facCode
	}
	rid, err := m.Res.EnsureResource(ctx, tx, uuidPtr(row["resourceId"]), reservation.ResourceRequest{PropertyID: pid, Code: "CRT-" + str(row["code"]),
		Name: str(row["name"]), ResourceType: "sport_court", VenueID: venue, Status: activeOr(str(row["status"])),
		Attributes: map[string]any{"priceItem": item, "courtId": str(row["id"]), "facilityId": str(row["facilityId"]), "facilityType": deref(facType)}})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE sportclub.courts SET resource_id = $2 WHERE id = $1`, row["id"], rid)
	return err
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (m *Module) syncProgramResource(ctx context.Context, tx pgx.Tx, row map[string]any) error {
	pid, _ := reqctx.Property(ctx)
	rid, err := m.Res.EnsureResource(ctx, tx, uuidPtr(row["resourceId"]), reservation.ResourceRequest{PropertyID: pid, Code: "CLS-" + str(row["code"]),
		Name: str(row["name"]), ResourceType: "class_session", Capacity: intPtr(row["capacity"]), Status: activeOr(str(row["status"])),
		Attributes: map[string]any{"priceItem": str(row["code"]), "programId": str(row["id"])}})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE sportclub.class_programs SET resource_id = $2 WHERE id = $1`, row["id"], rid)
	return err
}

// Policy is the Sport Club Policies document (FR-POL-P2-01).
type Policy struct {
	GuestOfMemberMustBePresent bool                 `json:"guestOfMemberMustBePresent"`
	MaxGuestsPerMember         int                  `json:"maxGuestsPerMember"`
	ChildMaxAge                int                  `json:"childMaxAge"`
	OpeningHours               map[string][2]string `json:"openingHours"`
	DeductQuotaOnAbsent        bool                 `json:"deductQuotaOnAbsent"`
	ClassBookingCutoffMinutes  int                  `json:"classBookingCutoffMinutes"`
}

var defaultPolicy = Policy{GuestOfMemberMustBePresent: true, MaxGuestsPerMember: 4, ChildMaxAge: 11,
	OpeningHours:        map[string][2]string{"weekday": {"06:00", "21:00"}, "weekend": {"06:00", "21:00"}, "holiday": {"06:00", "21:00"}},
	DeductQuotaOnAbsent: true, ClassBookingCutoffMinutes: 0}

// Contribution returns catalogue entries.
func Contribution() catalog.Contribution {
	defs := []*resource.Def{Facilities, Courts, Lockers, Instructors, ClassPrograms, ClassSchedules}
	perms := resource.Permissions(defs...)
	perms = append(perms, catalog.P("sportclub", "entry", "view", "create", "cancel")...)
	perms = append(perms, catalog.P("sportclub", "access", "view", "validate")...)
	perms = append(perms, catalog.P("sportclub", "booking", "view", "create")...)
	perms = append(perms, catalog.P("sportclub", "locker_assignment", "view", "manage")...)
	perms = append(perms, catalog.P("sportclub", "class", "view", "enroll", "attendance", "manage")...)
	perms = append(perms, catalog.P("sportclub", "instructor_fee", "view", "manage", "pay")...)
	ops := []string{"sportclub.facility.view", "sportclub.court.view", "sportclub.locker.view", "sportclub.instructor.view", "sportclub.class_program.view",
		"sportclub.class_schedule.view", "sportclub.entry.view", "sportclub.entry.create", "sportclub.access.view", "sportclub.access.validate",
		"sportclub.booking.view", "sportclub.booking.create", "sportclub.locker_assignment.view", "sportclub.locker_assignment.manage",
		"sportclub.class.view", "sportclub.class.enroll"}
	manager := append(append(resource.AllActions(defs...), ops...), "sportclub.entry.cancel", "sportclub.class.attendance", "sportclub.class.manage",
		"sportclub.instructor_fee.view", "sportclub.instructor_fee.manage")
	return catalog.Contribution{
		Permissions: perms,
		RolePermissions: map[string][]string{
			"property_admin":          manager,
			"sport_club_manager":      manager,
			"sport_club_receptionist": ops,
			"instructor_coach":        {"sportclub.class.view", "sportclub.class.attendance", "sportclub.class_program.view", "sportclub.facility.view"},
			"lifeguard":               {"sportclub.facility.view", "sportclub.access.view"},
			"general_manager":         {"sportclub.facility.view", "sportclub.court.view", "sportclub.entry.view", "sportclub.access.view", "sportclub.booking.view", "sportclub.class.view", "sportclub.instructor_fee.view"},
			"club_manager":            {"sportclub.facility.view", "sportclub.court.view", "sportclub.entry.view", "sportclub.access.view", "sportclub.booking.view", "sportclub.class.view"},
			"finance_manager":         {"sportclub.instructor_fee.view", "sportclub.instructor_fee.manage", "sportclub.instructor_fee.pay", "sportclub.entry.view"},
			"accountant":              {"sportclub.instructor_fee.view", "sportclub.instructor_fee.pay"},
			"front_desk":              {"sportclub.facility.view", "sportclub.access.validate", "sportclub.access.view", "sportclub.entry.create", "sportclub.entry.view"},
			"reservation_staff":       {"sportclub.facility.view", "sportclub.court.view", "sportclub.booking.view", "sportclub.booking.create", "sportclub.class.view", "sportclub.class.enroll"},
		},
	}
}

func jsonMap(v any) map[string]any {
	switch t := v.(type) {
	case map[string]any:
		return t
	case []byte:
		var m map[string]any
		_ = json.Unmarshal(t, &m)
		return m
	case string:
		var m map[string]any
		_ = json.Unmarshal([]byte(t), &m)
		return m
	}
	return nil
}

var errNotFound = errs.NotFound
