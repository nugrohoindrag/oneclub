// Package resource is the generic master data engine. A Def (table + typed
// fields) yields list / view / add / edit / delete-or-archive endpoints,
// CSV/XLSX export (FR-MD-07), idempotent CSV import (FR-MD-06), audit
// before/after (FR-AUD-01) and the OpenAPI schema — so every master data
// entity behaves the same way.
package resource

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/errs"
)

// Kind is a field type.
type Kind int

const (
	String Kind = iota
	Text
	Int
	Decimal
	Bool
	Date
	Timestamp
	UUID
	Enum
	Email
	JSON
	Time       // time of day "HH:MM" (SQL time)
	IntList    // SQL int[]; JSON array of integers (Enum/Min/MaxN apply per element)
	StringList // SQL text[]; JSON array of strings (Enum/Upper apply per element)
	JSONList   // SQL jsonb holding an array of objects
	Image      // SQL text: URL of an uploaded image (/api/v1/files/…) or an https:// image
)

// Ref is a reference to another table.
type Ref struct {
	Table        string // e.g. "platform.venues"
	SameProperty bool   // referenced row must belong to the same property
	Label        string // e.g. "venue"
}

// Field is one column exposed through the API.
type Field struct {
	Name       string // JSON name (camelCase)
	Column     string // SQL column
	Label      string // English UI label
	Kind       Kind
	Required   bool
	Enum       []string
	Max        int
	Pattern    *regexp.Regexp
	PatternMsg string
	Upper      bool // normalise to upper case (codes)
	Ref        *Ref
	ReadOnly   bool // server-managed; never accepted from clients
	CreateOnly bool // settable on create only
	Default    any
	Search     bool // included in ?q= search
	Filter     bool // filter[<name>]= supported
	Min, MaxN  *float64
}

// Hooks customise behaviour inside the write transaction.
type Hooks struct {
	// BeforeWrite validates/adjusts values (before == nil on create).
	BeforeWrite func(ctx context.Context, tx pgx.Tx, values map[string]any, before map[string]any) error
	AfterCreate func(ctx context.Context, tx pgx.Tx, row map[string]any) error
	AfterUpdate func(ctx context.Context, tx pgx.Tx, before, after map[string]any) error
	// AfterRead adjusts a row before it leaves the API (list, view, export),
	// e.g. masking personal data the caller may not see (UU PDP).
	AfterRead func(ctx context.Context, row map[string]any)
}

// Def defines one resource.
type Def struct {
	Key            string // import/export key and audit entity type, e.g. "crm.customer"
	Module         string // gating module code
	Perm           string // permission prefix, e.g. "crm.customer"
	Path           string // e.g. "/api/v1/crm/customers"
	Table          string // e.g. "crm.customers"
	Name           string // "Customer"
	Plural         string // "Customers"
	Tag            string
	PropertyScoped bool
	Archive        bool   // has archived_at (soft delete after usage check)
	CodeField      string // natural key JSON name (import upsert key)
	NoDelete       bool
	OrderBy        string // default ORDER BY (SQL), default "created_at DESC, id DESC"
	Fields         []Field
	Hooks          Hooks
	SchemaName     string
}

func (d *Def) field(name string) (*Field, bool) {
	for i := range d.Fields {
		if d.Fields[i].Name == name {
			return &d.Fields[i], true
		}
	}
	return nil, false
}

func (d *Def) entityLabel(row map[string]any) string {
	var parts []string
	for _, k := range []string{d.CodeField, "name", "fullName"} {
		if k == "" {
			continue
		}
		if v, ok := row[k].(string); ok && v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, " · ")
}

// selectList builds the SELECT column list; values come back as Go types
// that marshal cleanly to JSON.
func (d *Def) selectList() string {
	cols := []string{"id::text AS id"}
	if d.PropertyScoped {
		cols = append(cols, "property_id::text AS property_id")
	}
	for _, f := range d.Fields {
		switch f.Kind {
		case UUID, Decimal:
			cols = append(cols, fmt.Sprintf("%s::text AS %s", f.Column, f.Column))
		case Date:
			cols = append(cols, fmt.Sprintf("to_char(%s, 'YYYY-MM-DD') AS %s", f.Column, f.Column))
		case Int:
			cols = append(cols, fmt.Sprintf("%s::int8 AS %s", f.Column, f.Column))
		case Time:
			cols = append(cols, fmt.Sprintf("to_char(%s, 'HH24:MI') AS %s", f.Column, f.Column))
		case IntList, StringList:
			cols = append(cols, fmt.Sprintf("to_jsonb(%s) AS %s", f.Column, f.Column))
		default:
			cols = append(cols, f.Column)
		}
	}
	cols = append(cols, "created_at", "updated_at")
	if d.Archive {
		cols = append(cols, "archived_at")
	}
	return strings.Join(cols, ", ")
}

func (d *Def) scanRows(rows pgx.Rows) ([]map[string]any, error) {
	defer rows.Close()
	fds := rows.FieldDescriptions()
	byCol := map[string]string{"id": "id", "property_id": "propertyId", "created_at": "createdAt", "updated_at": "updatedAt", "archived_at": "archivedAt"}
	for _, f := range d.Fields {
		byCol[f.Column] = f.Name
	}
	var out []map[string]any
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		m := make(map[string]any, len(vals))
		for i, fd := range fds {
			name := byCol[fd.Name]
			if name == "" {
				name = fd.Name
			}
			m[name] = vals[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// coerce validates and converts one input value to its SQL parameter form.
func coerce(f *Field, v any) (any, *errs.FieldError) {
	bad := func(code, msg string) (any, *errs.FieldError) {
		fe := errs.Field(f.Name, code, msg)
		return nil, &fe
	}
	if v == nil {
		return nil, nil
	}
	str := func() (string, bool) {
		switch t := v.(type) {
		case string:
			return t, true
		case json.Number:
			return t.String(), true
		}
		return "", false
	}
	switch f.Kind {
	case String, Text, Email, Enum, Image:
		s, ok := str()
		if !ok {
			return bad("invalid_type", "must be text")
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, nil
		}
		if f.Upper {
			s = strings.ToUpper(s)
		}
		if f.Max > 0 && len([]rune(s)) > f.Max {
			return bad("too_long", fmt.Sprintf("must be at most %d characters", f.Max))
		}
		if f.Kind == Email && (!strings.Contains(s, "@") || strings.ContainsAny(s, " \t")) {
			return bad("invalid_email", "must be a valid e-mail address")
		}
		if f.Kind == Image && !strings.HasPrefix(s, "/api/v1/files/") && !strings.HasPrefix(s, "https://") {
			return bad("invalid_image", "must be an uploaded image or an https:// address")
		}
		if f.Kind == Enum {
			found := false
			for _, e := range f.Enum {
				if e == s {
					found = true
				}
			}
			if !found {
				return bad("invalid_option", "must be one of: "+strings.Join(f.Enum, ", "))
			}
		}
		if f.Pattern != nil && !f.Pattern.MatchString(s) {
			msg := f.PatternMsg
			if msg == "" {
				msg = "invalid format"
			}
			return bad("invalid_format", msg)
		}
		return s, nil
	case Int:
		var n int64
		switch t := v.(type) {
		case json.Number:
			i, err := t.Int64()
			if err != nil {
				return bad("invalid_type", "must be a whole number")
			}
			n = i
		case string:
			if strings.TrimSpace(t) == "" {
				return nil, nil
			}
			i, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
			if err != nil {
				return bad("invalid_type", "must be a whole number")
			}
			n = i
		case float64:
			n = int64(t)
		case int64:
			n = t
		default:
			return bad("invalid_type", "must be a whole number")
		}
		if f.Min != nil && float64(n) < *f.Min {
			return bad("too_small", fmt.Sprintf("must be at least %v", *f.Min))
		}
		if f.MaxN != nil && float64(n) > *f.MaxN {
			return bad("too_large", fmt.Sprintf("must be at most %v", *f.MaxN))
		}
		if len(f.Enum) > 0 {
			ok := false
			for _, e := range f.Enum {
				ok = ok || e == strconv.FormatInt(n, 10)
			}
			if !ok {
				return bad("invalid_option", "must be one of: "+strings.Join(f.Enum, ", "))
			}
		}
		return n, nil
	case Decimal:
		s, ok := str()
		if !ok {
			if fl, isF := v.(float64); isF {
				s = strconv.FormatFloat(fl, 'f', -1, 64)
			} else {
				return bad("invalid_type", "must be a decimal number")
			}
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, nil
		}
		fl, err := strconv.ParseFloat(s, 64)
		if err != nil || !regexp.MustCompile(`^-?\d+(\.\d+)?$`).MatchString(s) {
			return bad("invalid_type", "must be a decimal number")
		}
		if f.Min != nil && fl < *f.Min {
			return bad("too_small", fmt.Sprintf("must be at least %v", *f.Min))
		}
		if f.MaxN != nil && fl > *f.MaxN {
			return bad("too_large", fmt.Sprintf("must be at most %v", *f.MaxN))
		}
		return s, nil
	case Bool:
		switch t := v.(type) {
		case bool:
			return t, nil
		case string:
			b, err := strconv.ParseBool(strings.TrimSpace(strings.ToLower(t)))
			if err != nil {
				return bad("invalid_type", "must be true or false")
			}
			return b, nil
		}
		return bad("invalid_type", "must be true or false")
	case Date:
		s, ok := str()
		if !ok || (s != "" && !dateRe.MatchString(s)) {
			return bad("invalid_date", "must be a date (YYYY-MM-DD)")
		}
		if s == "" {
			return nil, nil
		}
		if _, err := time.Parse("2006-01-02", s); err != nil {
			return bad("invalid_date", "must be a valid date")
		}
		return s, nil
	case Timestamp:
		s, ok := str()
		if !ok {
			return bad("invalid_datetime", "must be an ISO 8601 date-time with offset")
		}
		if s == "" {
			return nil, nil
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			if d, derr := time.Parse("2006-01-02", s); derr == nil {
				return d.UTC(), nil
			}
			return bad("invalid_datetime", "must be an ISO 8601 date-time with offset")
		}
		return t.UTC(), nil
	case UUID:
		s, ok := str()
		if !ok {
			return bad("invalid_id", "must be an id")
		}
		if strings.TrimSpace(s) == "" {
			return nil, nil
		}
		u, err := uuid.Parse(strings.TrimSpace(s))
		if err != nil {
			return bad("invalid_id", "must be an id")
		}
		return u.String(), nil
	case JSON:
		raw, err := json.Marshal(v)
		if err != nil {
			return bad("invalid_type", "must be JSON")
		}
		return string(raw), nil
	case JSONList:
		if s, ok := v.(string); ok {
			var arr []any
			if err := json.Unmarshal([]byte(s), &arr); err != nil {
				return bad("invalid_type", "must be a JSON array")
			}
			v = arr
		}
		if _, ok := v.([]any); !ok {
			return bad("invalid_type", "must be a list")
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return bad("invalid_type", "must be a JSON array")
		}
		return string(raw), nil
	case Time:
		s, ok := str()
		if !ok {
			return bad("invalid_time", "must be a time (HH:MM)")
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, nil
		}
		if _, err := time.Parse("15:04", s); err != nil {
			return bad("invalid_time", "must be a time (HH:MM)")
		}
		return s, nil
	case IntList, StringList:
		var items []any
		switch t := v.(type) {
		case []any:
			items = t
		case string:
			for _, p := range strings.Split(t, ",") {
				if p = strings.TrimSpace(p); p != "" {
					items = append(items, p)
				}
			}
		default:
			return bad("invalid_type", "must be a list")
		}
		el := *f
		el.Kind = String
		if f.Kind == IntList {
			el.Kind = Int
		}
		if f.Kind == IntList {
			out := []int64{}
			for _, it := range items {
				x, fe := coerce(&el, it)
				if fe != nil {
					return nil, fe
				}
				if x != nil {
					out = append(out, x.(int64))
				}
			}
			return out, nil
		}
		out := []string{}
		for _, it := range items {
			x, fe := coerce(&el, it)
			if fe != nil {
				return nil, fe
			}
			if x != nil {
				out = append(out, x.(string))
			}
		}
		return out, nil
	}
	return v, nil
}

func cast(f *Field) string {
	switch f.Kind {
	case UUID:
		return "::uuid"
	case Decimal:
		return "::numeric"
	case Date:
		return "::date"
	case Timestamp:
		return "::timestamptz"
	case JSON, JSONList:
		return "::jsonb"
	case Time:
		return "::time"
	case IntList:
		return "::int[]"
	case StringList:
		return "::text[]"
	case Int:
		return "::int8"
	case Bool:
		return "::bool"
	}
	return ""
}

// Schema builds the OpenAPI schema of the resource representation.
func (d *Def) Schema(input bool) *openapi3.Schema {
	s := openapi3.NewObjectSchema()
	s.Properties = openapi3.Schemas{}
	if !input {
		s.Properties["id"] = openapi3.NewUUIDSchema().NewRef()
		if d.PropertyScoped {
			s.Properties["propertyId"] = openapi3.NewUUIDSchema().NewRef()
		}
		s.Properties["createdAt"] = openapi3.NewDateTimeSchema().NewRef()
		s.Properties["updatedAt"] = openapi3.NewDateTimeSchema().NewRef()
		if d.Archive {
			ad := openapi3.NewDateTimeSchema()
			ad.Nullable = true
			s.Properties["archivedAt"] = ad.NewRef()
		}
		s.Required = []string{"id", "createdAt", "updatedAt"}
	}
	for i := range d.Fields {
		f := &d.Fields[i]
		if input && f.ReadOnly {
			continue
		}
		var fs *openapi3.Schema
		switch f.Kind {
		case Int:
			fs = openapi3.NewInt64Schema()
		case Decimal:
			fs = openapi3.NewStringSchema()
			fs.Pattern = `^-?\d+(\.\d+)?$`
		case Bool:
			fs = openapi3.NewBoolSchema()
		case Date:
			fs = openapi3.NewStringSchema().WithFormat("date")
		case Timestamp:
			fs = openapi3.NewDateTimeSchema()
		case UUID:
			fs = openapi3.NewUUIDSchema()
		case Email:
			fs = openapi3.NewStringSchema().WithFormat("email")
		case JSON:
			fs = openapi3.NewObjectSchema()
		case JSONList:
			fs = openapi3.NewArraySchema().WithItems(openapi3.NewObjectSchema())
		case Time:
			fs = openapi3.NewStringSchema()
			fs.Pattern = `^\d{2}:\d{2}$`
		case IntList:
			items := openapi3.NewInt64Schema()
			for _, e := range f.Enum {
				n, _ := strconv.Atoi(e)
				items.Enum = append(items.Enum, n)
			}
			fs = openapi3.NewArraySchema().WithItems(items)
		case StringList:
			items := openapi3.NewStringSchema()
			for _, e := range f.Enum {
				items.Enum = append(items.Enum, e)
			}
			fs = openapi3.NewArraySchema().WithItems(items)
		default:
			fs = openapi3.NewStringSchema()
		}
		for _, e := range f.Enum {
			if f.Kind == IntList || f.Kind == StringList {
				break
			}
			if f.Kind == Int {
				n, _ := strconv.Atoi(e)
				fs.Enum = append(fs.Enum, n)
			} else {
				fs.Enum = append(fs.Enum, e)
			}
		}
		if f.Max > 0 && f.Kind != IntList && f.Kind != StringList && f.Kind != JSONList {
			m := uint64(f.Max)
			fs.MaxLength = &m
		}
		fs.Description = f.Label
		if !f.Required {
			fs.Nullable = true
		} else if input && f.Default == nil {
			s.Required = append(s.Required, f.Name)
		}
		if !input && f.Required {
			s.Required = append(s.Required, f.Name)
		}
		s.Properties[f.Name] = fs.NewRef()
	}
	return s
}
