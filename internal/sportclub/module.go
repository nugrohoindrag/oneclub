// Package sportclub is Sport Club & Facility. P0 provides the Facility
// foundation entity (FR-MD-05).
package sportclub

import (
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/resource"
)

var Facilities = &resource.Def{
	Key: "sportclub.facility", Module: "sportclub", Perm: "sportclub.facility", Path: "/api/v1/sportclub/facilities", Table: "sportclub.facilities",
	Name: "Facility", Plural: "Facilities", Tag: "Foundation Data", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "facilityType", Column: "facility_type", Label: "Facility Type", Kind: resource.String, Max: 60, Filter: true},
		{Name: "venueId", Column: "venue_id", Label: "Venue", Kind: resource.UUID, Ref: &resource.Ref{Table: "platform.venues", SameProperty: true, Label: "venue"}},
		{Name: "capacity", Column: "capacity", Label: "Capacity", Kind: resource.Int, Min: resource.Min(1)},
		resource.Status("active", "inactive"), resource.Attributes()},
}

// Contribution returns catalogue entries.
func Contribution() catalog.Contribution {
	return catalog.Contribution{
		Permissions: resource.Permissions(Facilities),
		RolePermissions: map[string][]string{
			"property_admin":     resource.AllActions(Facilities),
			"sport_club_manager": {"sportclub.facility.view", "sportclub.facility.create", "sportclub.facility.update", "sportclub.facility.export"},
		},
	}
}
