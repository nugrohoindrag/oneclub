// Package golf is the Golf Operations module (GolfOne baseline). In P0 it
// only owns the Course header (FR-ORG-04); sections, playing routes, holes
// and tee sets arrive in P1.
package golf

import (
	"regexp"

	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/resource"
)

var codeRe = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)

// Courses is the Course header resource.
var Courses = &resource.Def{
	Key: "golf.course", Module: "golf", Perm: "golf.course", Path: "/api/v1/golf/courses", Table: "golf.courses",
	Name: "Course", Plural: "Courses", Tag: "Golf", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{
		{Name: "code", Column: "code", Label: "Code", Kind: resource.String, Required: true, Max: 20, Upper: true,
			Pattern: codeRe, PatternMsg: "1–20 characters: A–Z, 0–9, - or _", Search: true},
		{Name: "name", Column: "name", Label: "Name", Kind: resource.String, Required: true, Max: 120, Search: true},
		{Name: "venueId", Column: "venue_id", Label: "Venue", Kind: resource.UUID, Required: true, Filter: true,
			Ref: &resource.Ref{Table: "platform.venues", SameProperty: true, Label: "venue"}},
		{Name: "holes", Column: "holes", Label: "Holes", Kind: resource.Int, Required: true, Enum: []string{"9", "18", "27", "36"}},
		resource.Status("active", "inactive"),
	},
}

// Contribution returns the catalogue entries of the golf module.
func Contribution() catalog.Contribution {
	return catalog.Contribution{
		Permissions: resource.Permissions(Courses),
		RolePermissions: map[string][]string{
			"property_admin": resource.AllActions(Courses),
			"golf_manager":   {"golf.course.view", "golf.course.create", "golf.course.update", "golf.course.export"},
			"golf_admin":     {"golf.course.view", "golf.course.update"},
		},
	}
}
