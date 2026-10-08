// Package golf is the Golf Operations module (GolfOne baseline). P1 (Golf
// Core MVP) covers the course structure, tee sheet, booking / flight /
// player, caddy, golf cart, check-in, bag drop, locker, starter, golf
// policies, the staff operational interface, the Member Portal and the
// public website booking flow.
package golf

import (
	"context"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/resource"
)

var codeRe = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)
var code40Re = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,39}$`)
var numRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,19}$`)
var hhmmRe = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

func code20(label string) resource.Field {
	return resource.Field{Name: "code", Column: "code", Label: label, Kind: resource.String, Required: true, Max: 20, Upper: true,
		Pattern: codeRe, PatternMsg: "1–20 characters: A–Z, 0–9, - or _", Search: true}
}

func courseRef() resource.Field {
	return resource.Field{Name: "courseId", Column: "course_id", Label: "Course", Kind: resource.UUID, Required: true, Filter: true, CreateOnly: true,
		Ref: &resource.Ref{Table: "golf.courses", SameProperty: true, Label: "course"}}
}

// Courses is the Course resource (P0 header + P1 details).
var Courses = &resource.Def{
	Key: "golf.course", Module: "golf", Perm: "golf.course", Path: "/api/v1/golf/courses", Table: "golf.courses",
	Name: "Course", Plural: "Courses", Tag: "Course", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{
		{Name: "code", Column: "code", Label: "Code", Kind: resource.String, Required: true, Max: 20, Upper: true,
			Pattern: codeRe, PatternMsg: "1–20 characters: A–Z, 0–9, - or _", Search: true},
		{Name: "name", Column: "name", Label: "Name", Kind: resource.String, Required: true, Max: 120, Search: true},
		{Name: "venueId", Column: "venue_id", Label: "Venue", Kind: resource.UUID, Required: true, Filter: true,
			Ref: &resource.Ref{Table: "platform.venues", SameProperty: true, Label: "venue"}},
		{Name: "holes", Column: "holes", Label: "Holes", Kind: resource.Int, Required: true, Enum: []string{"9", "18", "27", "36"}},
		{Name: "lengthMeters", Column: "length_meters", Label: "Length (m)", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "par", Column: "par", Label: "Par", Kind: resource.Int, Min: resource.Min(27)},
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 4000},
		{Name: "guide", Column: "guide", Label: "Course Guide", Kind: resource.Text, Max: 20000},
		resource.Status("active", "inactive"),
	},
}

var CourseSections = &resource.Def{
	Key: "golf.course_section", Module: "golf", Perm: "golf.course", Path: "/api/v1/golf/course-sections", Table: "golf.course_sections",
	Name: "Course Section", Plural: "Course Sections", Tag: "Course", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "course_id, sequence, code",
	Fields: []resource.Field{courseRef(), code20("Code"), resource.Name(),
		{Name: "sequence", Column: "sequence", Label: "Sequence", Kind: resource.Int, Default: int64(1), Min: resource.Min(1)},
		resource.Status("active", "inactive")},
}

var TeeSets = &resource.Def{
	Key: "golf.tee_set", Module: "golf", Perm: "golf.course", Path: "/api/v1/golf/tee-sets", Table: "golf.tee_sets",
	Name: "Tee Set", Plural: "Tee Sets", Tag: "Course", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "course_id, sequence, code",
	Fields: []resource.Field{courseRef(), code20("Code"), resource.Name(),
		{Name: "color", Column: "color", Label: "Color", Kind: resource.String, Max: 30},
		{Name: "courseRating", Column: "course_rating", Label: "Course Rating", Kind: resource.Decimal, Min: resource.Min(1), MaxN: resource.Max(99.9)},
		{Name: "slope", Column: "slope", Label: "Slope", Kind: resource.Int, Min: resource.Min(55), MaxN: resource.Max(155)},
		{Name: "gender", Column: "gender", Label: "Gender", Kind: resource.Enum, Enum: []string{"male", "female", "any"}},
		{Name: "sequence", Column: "sequence", Label: "Sequence", Kind: resource.Int, Default: int64(1)},
		resource.Status("active", "inactive")},
}

var Holes = &resource.Def{
	Key: "golf.hole", Module: "golf", Perm: "golf.course", Path: "/api/v1/golf/holes", Table: "golf.holes",
	Name: "Hole", Plural: "Holes", Tag: "Course", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "course_id, number",
	Fields: []resource.Field{courseRef(),
		{Name: "sectionId", Column: "section_id", Label: "Course Section", Kind: resource.UUID, Required: true, Filter: true,
			Ref: &resource.Ref{Table: "golf.course_sections", SameProperty: true, Label: "course section"}},
		code20("Code"),
		{Name: "number", Column: "number", Label: "Hole No.", Kind: resource.Int, Required: true, Min: resource.Min(1), MaxN: resource.Max(36)},
		{Name: "par", Column: "par", Label: "Par", Kind: resource.Int, Required: true, Min: resource.Min(3), MaxN: resource.Max(6)},
		{Name: "strokeIndex", Column: "stroke_index", Label: "Stroke Index", Kind: resource.Int, Min: resource.Min(1), MaxN: resource.Max(36)},
		{Name: "distances", Column: "distances", Label: "Distance per tee set (m)", Kind: resource.JSON},
		{Name: "description", Column: "description", Label: "Hole-by-Hole description", Kind: resource.Text, Max: 4000},
		resource.Status("active", "inactive")},
}

var PlayingRoutes = &resource.Def{
	Key: "golf.playing_route", Module: "golf", Perm: "golf.course", Path: "/api/v1/golf/playing-routes", Table: "golf.playing_routes",
	Name: "Playing Route", Plural: "Playing Routes", Tag: "Course", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "course_id, code",
	Fields: []resource.Field{courseRef(), code20("Code"), resource.Name(),
		{Name: "sectionCodes", Column: "section_codes", Label: "Sections in order (e.g. FRONT,BACK)", Kind: resource.String, Required: true, Max: 200, Upper: true,
			Pattern: regexp.MustCompile(`^[A-Z0-9_-]+(,[A-Z0-9_-]+)*$`), PatternMsg: "comma separated section codes"},
		{Name: "holeCount", Column: "hole_count", Label: "Holes", Kind: resource.Int, ReadOnly: true},
		{Name: "isDefault", Column: "is_default", Label: "Default route", Kind: resource.Bool, Default: false},
		resource.Status("active", "inactive")},
}

var CourseAssets = &resource.Def{
	Key: "golf.course_asset", Module: "golf", Perm: "golf.course", Path: "/api/v1/golf/course-assets", Table: "golf.course_assets",
	Name: "Course Asset", Plural: "Course Assets", Tag: "Course", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "course_id, asset_type, code",
	Fields: []resource.Field{courseRef(),
		{Name: "holeId", Column: "hole_id", Label: "Hole", Kind: resource.UUID, Filter: true, Ref: &resource.Ref{Table: "golf.holes", SameProperty: true, Label: "hole"}},
		{Name: "code", Column: "code", Label: "Code", Kind: resource.String, Required: true, Max: 40, Upper: true, Pattern: code40Re, PatternMsg: "1–40 characters: A–Z, 0–9, - or _", Search: true},
		{Name: "assetType", Column: "asset_type", Label: "Asset Type", Kind: resource.Enum, Required: true, Filter: true,
			Enum: []string{"course_map", "panorama", "hazard", "point_of_interest", "green_front", "green_center", "green_back", "distance_marker"}},
		resource.Name(),
		{Name: "fileId", Column: "file_id", Label: "File", Kind: resource.UUID, Ref: &resource.Ref{Table: "platform.files", Label: "file"}},
		{Name: "geometry", Column: "geometry", Label: "Geometry (GeoJSON)", Kind: resource.JSON},
		resource.Status("active", "inactive")},
}

var TeeSheetTemplates = &resource.Def{
	Key: "golf.tee_sheet_template", Module: "golf", Perm: "golf.tee_sheet", Path: "/api/v1/golf/tee-sheet-templates", Table: "golf.tee_sheet_templates",
	Name: "Tee Sheet Template", Plural: "Tee Sheet Templates", Tag: "Tee Sheet", PropertyScoped: true, Archive: true, CodeField: "code",
	OrderBy: "course_id, day_type_code, start_time, effective_from DESC",
	Fields: []resource.Field{courseRef(),
		{Name: "code", Column: "code", Label: "Code", Kind: resource.String, Required: true, Max: 40, Upper: true, Pattern: code40Re, PatternMsg: "1–40 characters", Search: true, CreateOnly: true},
		resource.Name(),
		{Name: "dayTypeCode", Column: "day_type_code", Label: "Day Type", Kind: resource.String, Required: true, Max: 20, Upper: true, Filter: true},
		{Name: "session", Column: "session", Label: "Session", Kind: resource.Enum, Enum: []string{"morning", "afternoon", "night"}, Required: true, Filter: true},
		{Name: "startTime", Column: "start_time", Label: "First tee time (HH:MM)", Kind: resource.String, Required: true, Pattern: hhmmRe, PatternMsg: "HH:MM"},
		{Name: "endTime", Column: "end_time", Label: "Last tee time (HH:MM)", Kind: resource.String, Required: true, Pattern: hhmmRe, PatternMsg: "HH:MM"},
		{Name: "intervalMinutes", Column: "interval_minutes", Label: "Interval (minutes)", Kind: resource.Int, Required: true, Min: resource.Min(4), MaxN: resource.Max(30)},
		{Name: "startTees", Column: "start_tees", Label: "Start tee", Kind: resource.Enum, Enum: []string{"1", "1,10"}, Default: "1"},
		{Name: "flightsPerSlot", Column: "flights_per_slot", Label: "Flights per slot", Kind: resource.Int, Default: int64(1), Min: resource.Min(1), MaxN: resource.Max(4)},
		{Name: "minPlayers", Column: "min_players", Label: "Min players", Kind: resource.Int, Default: int64(1), Min: resource.Min(1), MaxN: resource.Max(6)},
		{Name: "maxPlayers", Column: "max_players", Label: "Max players", Kind: resource.Int, Default: int64(4), Min: resource.Min(1), MaxN: resource.Max(6)},
		{Name: "playingRouteId", Column: "playing_route_id", Label: "Playing Route", Kind: resource.UUID, Ref: &resource.Ref{Table: "golf.playing_routes", SameProperty: true, Label: "playing route"}},
		{Name: "peak", Column: "peak", Label: "Peak", Kind: resource.Bool, Default: false},
		{Name: "memberOnly", Column: "member_only", Label: "Member only", Kind: resource.Bool, Default: false},
		{Name: "lighting", Column: "lighting", Label: "Night lights", Kind: resource.Bool, Default: false},
		{Name: "effectiveFrom", Column: "effective_from", Label: "Effective From", Kind: resource.Date, Required: true},
		{Name: "effectiveTo", Column: "effective_to", Label: "Effective To", Kind: resource.Date},
		resource.Status("active", "inactive")},
}

var Caddies = &resource.Def{
	Key: "golf.caddy", Module: "golf", Perm: "golf.caddy", Path: "/api/v1/golf/caddies", Table: "golf.caddies",
	Name: "Caddy", Plural: "Caddies", Tag: "Caddies", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "code, id",
	Fields: []resource.Field{
		{Name: "code", Column: "code", Label: "Caddy No.", Kind: resource.String, Required: true, Max: 20, Pattern: numRe, PatternMsg: "1–20 characters", Search: true},
		resource.Name(),
		{Name: "gender", Column: "gender", Label: "Gender", Kind: resource.Enum, Enum: []string{"male", "female"}, Filter: true},
		{Name: "phone", Column: "phone", Label: "Phone", Kind: resource.String, Max: 40},
		{Name: "photoFileId", Column: "photo_file_id", Label: "Photo", Kind: resource.UUID, Ref: &resource.Ref{Table: "platform.files", Label: "photo"}},
		{Name: "partnershipStatus", Column: "partnership_status", Label: "Partnership", Kind: resource.Enum, Enum: []string{"partner", "trainee", "employee"}, Default: "partner", Filter: true},
		{Name: "legacyRef", Column: "legacy_ref", Label: "Rhapsody Reference", Kind: resource.String, Max: 60},
		resource.Status("active", "inactive")},
}

var GolfCarts = &resource.Def{
	Key: "golf.golf_cart", Module: "golf", Perm: "golf.golf_cart", Path: "/api/v1/golf/golf-carts", Table: "golf.golf_carts",
	Name: "Golf Cart", Plural: "Golf Carts", Tag: "Golf Carts", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "code, id",
	Fields: []resource.Field{
		{Name: "code", Column: "code", Label: "Golf Cart No.", Kind: resource.String, Required: true, Max: 20, Pattern: numRe, PatternMsg: "1–20 characters", Search: true},
		resource.Name(),
		{Name: "cartType", Column: "cart_type", Label: "Type", Kind: resource.Enum, Enum: []string{"electric", "gasoline", "other"}, Default: "electric", Filter: true},
		{Name: "capacity", Column: "capacity", Label: "Capacity", Kind: resource.Int, Default: int64(2), Min: resource.Min(1), MaxN: resource.Max(6)},
		{Name: "readiness", Column: "readiness", Label: "Readiness", Kind: resource.Enum, ReadOnly: true, Filter: true,
			Enum: []string{"ready", "not_ready", "in_use", "charging", "maintenance", "out_of_service"}},
		{Name: "readinessReason", Column: "readiness_reason", Label: "Readiness reason", Kind: resource.String, ReadOnly: true},
		{Name: "legacyRef", Column: "legacy_ref", Label: "Rhapsody Reference", Kind: resource.String, Max: 60},
		resource.Status("active", "inactive")},
}

var Lockers = &resource.Def{
	Key: "golf.locker", Module: "golf", Perm: "golf.locker", Path: "/api/v1/golf/lockers", Table: "golf.lockers",
	Name: "Locker", Plural: "Lockers", Tag: "Lockers", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "area, code",
	Fields: []resource.Field{
		{Name: "code", Column: "code", Label: "Locker No.", Kind: resource.String, Required: true, Max: 20, Pattern: numRe, PatternMsg: "1–20 characters", Search: true},
		resource.Name(),
		{Name: "area", Column: "area", Label: "Area", Kind: resource.Enum, Enum: []string{"male", "female"}, Required: true, Filter: true},
		{Name: "zone", Column: "zone", Label: "Zone", Kind: resource.String, Max: 40, Filter: true},
		{Name: "lockerStatus", Column: "locker_status", Label: "Locker Status", Kind: resource.Enum, Enum: []string{"available", "occupied", "maintenance"}, Default: "available", Filter: true},
		{Name: "legacyRef", Column: "legacy_ref", Label: "Rhapsody Reference", Kind: resource.String, Max: 60},
		resource.Status("active", "inactive")},
}

func init() {
	PlayingRoutes.Hooks = resource.Hooks{BeforeWrite: routeBeforeWrite}
	TeeSheetTemplates.Hooks = resource.Hooks{BeforeWrite: templateBeforeWrite}
	Holes.Hooks = resource.Hooks{BeforeWrite: holeBeforeWrite}
}

// routeBeforeWrite derives the hole count from the sections (FR-CRS-03).
func routeBeforeWrite(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	codes, ok := v["sectionCodes"].(string)
	if !ok {
		return nil
	}
	course, _ := v["courseId"].(string)
	if course == "" && before != nil {
		course, _ = before["courseId"].(string)
	}
	total := 0
	for _, c := range strings.Split(codes, ",") {
		var n int
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.course_sections WHERE course_id = $1::uuid AND code = $2),
			(SELECT count(*) FROM golf.holes h JOIN golf.course_sections s ON s.id = h.section_id WHERE s.course_id = $1::uuid AND s.code = $2 AND h.status = 'active')`,
			course, c).Scan(&exists, &n); err != nil {
			return err
		}
		if !exists {
			return errs.Validation("unknown_section", "unknown course section "+c, errs.Field("sectionCodes", "not_found", "section "+c+" does not exist on this course"))
		}
		total += n
	}
	v["holeCount"] = int64(total)
	return nil
}

func holeBeforeWrite(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	sec, _ := v["sectionId"].(string)
	course, _ := v["courseId"].(string)
	if before != nil && course == "" {
		course, _ = before["courseId"].(string)
	}
	if sec != "" {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.course_sections WHERE id = $1::uuid AND course_id = $2::uuid)`, sec, course).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errs.Validation("section_mismatch", "the section belongs to another course", errs.Field("sectionId", "invalid", "section of this course"))
		}
	}
	return nil
}

// templateBeforeWrite keeps templates consistent; an effective template is
// changed by adding a new template with a later effective date
// (FR-TEE-11), never by editing it.
func templateBeforeWrite(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	if before != nil {
		// the club's date, not the UTC date (still yesterday before 07:00 WIB)
		pid, _ := reqctx.Property(ctx)
		today := localDay(clock.Now(), location(ctx, tx, pid)).Format("2006-01-02")
		if eff, _ := before["effectiveFrom"].(string); eff != "" && eff <= today {
			for k := range v {
				if k != "status" && k != "effectiveTo" && k != "name" {
					return errs.Conflict("template_already_effective", "this template is already effective; add a new template with a later effective date (slots with bookings never change)")
				}
			}
		}
	}
	m := map[string]any{}
	for k, x := range before {
		m[k] = x
	}
	for k, x := range v {
		m[k] = x
	}
	st, _ := m["startTime"].(string)
	en, _ := m["endTime"].(string)
	if st != "" && en != "" && en < st {
		return errs.Validation("invalid_template", "last tee time must not be before the first", errs.Field("endTime", "invalid", "after the first tee time"))
	}
	mn, _ := m["minPlayers"].(int64)
	mx, _ := m["maxPlayers"].(int64)
	if mn > 0 && mx > 0 && mx < mn {
		return errs.Validation("invalid_template", "max players must be at least min players", errs.Field("maxPlayers", "invalid", "≥ min players"))
	}
	return nil
}

func p(obj string, actions ...string) []catalog.Permission { return catalog.P("golf", obj, actions...) }

// Contribution returns catalogue entries.
func Contribution() catalog.Contribution {
	perms := resource.Permissions(Courses, Caddies, GolfCarts, Lockers)
	for _, x := range [][]catalog.Permission{
		p("tee_sheet", "view", "create", "update", "delete", "export", "import", "manage"),
		p("booking", "view", "create", "update", "cancel", "no_show", "waive_fee", "export"),
		p("flight", "view", "manage"),
		p("caddy_assignment", "manage"),
		p("golf_cart_assignment", "manage"),
		p("locker_assignment", "manage"),
		p("check_in", "perform"),
		p("bag", "manage"),
		p("starter", "view", "control"),
		p("course_status", "update"),
		p("rain_check", "view", "issue"),
		p("handicap", "view", "manage"),
		p("policy", "view"),
	} {
		perms = append(perms, x...)
	}
	all := []string{}
	for _, x := range perms {
		all = append(all, x.Code)
	}
	view := []string{"golf.course.view", "golf.tee_sheet.view", "golf.booking.view", "golf.flight.view", "golf.caddy.view", "golf.golf_cart.view",
		"golf.locker.view", "golf.starter.view", "golf.rain_check.view", "golf.handicap.view", "golf.policy.view"}
	desk := append(append([]string{}, view...), "golf.booking.create", "golf.booking.update", "golf.booking.cancel", "golf.booking.no_show",
		"golf.flight.manage", "golf.check_in.perform", "golf.bag.manage", "golf.locker_assignment.manage", "golf.handicap.manage", "golf.booking.export")
	starter := append(append([]string{}, view...), "golf.starter.control", "golf.check_in.perform", "golf.flight.manage", "golf.course_status.update",
		"golf.rain_check.issue", "golf.caddy_assignment.manage", "golf.golf_cart_assignment.manage")
	caddyMgr := append(append([]string{}, view...), "golf.caddy.create", "golf.caddy.update", "golf.caddy.export", "golf.caddy_assignment.manage")
	staff := append(append([]string{}, view...), "golf.bag.manage", "golf.locker_assignment.manage", "golf.golf_cart.update",
		"golf.golf_cart_assignment.manage", "golf.check_in.perform")
	admin := append(append([]string{}, desk...), resource.AllActions(Courses, Caddies, GolfCarts, Lockers)...)
	admin = append(admin, "golf.tee_sheet.create", "golf.tee_sheet.update", "golf.tee_sheet.delete", "golf.tee_sheet.export", "golf.tee_sheet.import",
		"golf.tee_sheet.manage", "golf.caddy_assignment.manage", "golf.golf_cart_assignment.manage", "golf.starter.control", "golf.course_status.update",
		"golf.rain_check.issue", "golf.booking.waive_fee", "platform.club_policy.view", "platform.club_policy.manage", "platform.calendar_day.view",
		"platform.calendar_day.create", "platform.calendar_day.update", "platform.calendar_day.delete", "platform.calendar_day.export", "platform.calendar_day.import")
	return catalog.Contribution{
		Permissions: perms,
		RolePermissions: map[string][]string{
			"property_admin":    all,
			"golf_manager":      admin,
			"golf_admin":        append([]string{}, admin...),
			"starter_marshal":   starter,
			"caddy_manager":     caddyMgr,
			"golf_staff":        staff,
			"reservation_staff": desk,
			// the front desk assigns the caddies and golf carts (Caddy Master role merged in, demo feedback 9 Oct 2026)
			"front_desk": append(append([]string{}, desk...), "golf.caddy_assignment.manage", "golf.golf_cart_assignment.manage", "golf.golf_cart.update",
				"golf.rain_check.issue"),
			"general_manager": append(append([]string{}, view...), "golf.booking.export"),
			"club_manager":    append(append([]string{}, view...), "golf.booking.export"),
			"finance_manager": {"golf.booking.view", "golf.course.view", "golf.tee_sheet.view"},
		},
	}
}
