package resource

import (
	"regexp"

	"oneclub/internal/platform/catalog"
)

// Permissions returns the standard permissions of a definition.
func Permissions(defs ...*Def) []catalog.Permission {
	var out []catalog.Permission
	for _, d := range defs {
		mod, obj := splitPerm(d.Perm)
		out = append(out, catalog.P(mod, obj, catalog.CRUD...)...)
	}
	return out
}

func splitPerm(p string) (string, string) {
	for i := range p {
		if p[i] == '.' {
			return p[:i], p[i+1:]
		}
	}
	return p, ""
}

// AllActions lists <perm>.<action> for every CRUD action of defs.
func AllActions(defs ...*Def) []string {
	var out []string
	for _, d := range defs {
		for _, a := range catalog.CRUD {
			out = append(out, d.Perm+"."+a)
		}
	}
	return out
}

// FoundationCode is the code pattern for foundation entities: letters,
// digits, dot, dash, underscore and slash (member numbers vary by club).
var FoundationCode = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,39}$`)

// Code returns the standard code field for foundation entities.
func Code(label string) Field {
	return Field{Name: "code", Column: "code", Label: label, Kind: String, Required: true, Max: 40, Upper: true,
		Pattern: FoundationCode, PatternMsg: "1–40 characters: letters, digits, . _ / -", Search: true}
}

// Status returns the standard status field.
func Status(values ...string) Field {
	return Field{Name: "status", Column: "status", Label: "Status", Kind: Enum, Enum: values, Default: "active", Filter: true}
}

// Name returns the standard name field.
func Name() Field {
	return Field{Name: "name", Column: "name", Label: "Name", Kind: String, Required: true, Max: 160, Search: true}
}

// Attributes returns the free-form attributes field.
func Attributes() Field {
	return Field{Name: "attributes", Column: "attributes", Label: "Attributes", Kind: JSON}
}

func f64(v float64) *float64 { return &v }

// Min returns a pointer for Field.Min.
func Min(v float64) *float64 { return f64(v) }

// Max returns a pointer for Field.MaxN.
func Max(v float64) *float64 { return f64(v) }
