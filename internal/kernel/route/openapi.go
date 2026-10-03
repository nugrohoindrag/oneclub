package route

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Info describes the published API.
type Info struct {
	Title       string
	Version     string
	Description string
}

// BuildOpenAPI produces the OpenAPI 3.0 document for every registered route.
// The document is generated per build and checked for drift / breaking
// changes in CI (FR-TEC-02).
func (g *Registry) BuildOpenAPI(info Info) (*openapi3.T, error) {
	rf := newReflector()
	doc := &openapi3.T{
		OpenAPI: "3.0.3",
		Info: &openapi3.Info{
			Title:       info.Title,
			Version:     info.Version,
			Description: info.Description,
		},
		Servers: openapi3.Servers{{URL: "/"}},
		Paths:   openapi3.NewPaths(),
		Components: &openapi3.Components{
			Schemas: rf.components,
			SecuritySchemes: openapi3.SecuritySchemes{
				"sessionCookie": &openapi3.SecuritySchemeRef{Value: &openapi3.SecurityScheme{
					Type: "apiKey", In: "cookie", Name: "oneclub_session",
					Description: "Opaque, revocable session token (HttpOnly cookie).",
				}},
				"apiKey": &openapi3.SecuritySchemeRef{Value: &openapi3.SecurityScheme{
					Type: "http", Scheme: "bearer",
					Description: "Server-to-server API key with limited scopes (FR-INT-06).",
				}},
			},
		},
	}
	problem := rf.ref(reflect.TypeOf(Problem{}), "Problem")
	tags := map[string]bool{}

	for _, r := range g.Routes() {
		op := &openapi3.Operation{
			OperationID: r.OperationID,
			Summary:     r.Summary,
			Description: r.Description,
			Responses:   openapi3.NewResponses(),
			Extensions:  map[string]any{"x-module": r.Module},
		}
		if r.Tag != "" {
			op.Tags = []string{r.Tag}
			tags[r.Tag] = true
		}
		if r.Permission != "" {
			op.Extensions["x-permission"] = r.Permission
		}
		if r.Mutating() {
			op.Extensions["x-audited"] = r.NoAudit == ""
		}
		switch r.Auth {
		case AuthPublic, AuthSignature:
			op.Security = &openapi3.SecurityRequirements{}
		default:
			op.Security = &openapi3.SecurityRequirements{{"sessionCookie": []string{}}, {"apiKey": []string{}}}
		}

		for _, p := range PathParams(r.Path) {
			op.Parameters = append(op.Parameters, &openapi3.ParameterRef{Value: &openapi3.Parameter{
				Name: p, In: "path", Required: true, Schema: openapi3.NewStringSchema().NewRef(),
			}})
		}
		if r.Scope == ScopeProperty {
			op.Parameters = append(op.Parameters, &openapi3.ParameterRef{Value: &openapi3.Parameter{
				Name: "X-Property-Id", In: "header", Required: true,
				Description: "Active property chosen in the property switcher.",
				Schema:      openapi3.NewUUIDSchema().NewRef(),
			}})
		}
		if r.Idempotent {
			op.Parameters = append(op.Parameters, &openapi3.ParameterRef{Value: &openapi3.Parameter{
				Name: "Idempotency-Key", In: "header",
				Description: "Repeating a request with the same key returns the original result (FR-JOB-06).",
				Schema:      openapi3.NewStringSchema().NewRef(),
			}})
		}
		if r.List {
			for _, p := range []Param{
				{Name: "cursor", In: "query", Type: "string", Description: "Opaque cursor from nextCursor."},
				{Name: "limit", In: "query", Type: "integer", Description: "Page size (max 500)."},
			} {
				op.Parameters = append(op.Parameters, paramRef(p))
			}
		}
		for _, p := range r.Query {
			op.Parameters = append(op.Parameters, paramRef(p))
		}

		if r.RequestSchema != nil || r.Request != nil {
			var s *openapi3.SchemaRef
			if r.RequestSchema != nil {
				name := r.SchemaName + "Input"
				rf.components[name] = r.RequestSchema.NewRef()
				s = openapi3.NewSchemaRef("#/components/schemas/"+name, r.RequestSchema)
			} else {
				s = rf.schemaFor(reflect.TypeOf(r.Request))
			}
			op.RequestBody = &openapi3.RequestBodyRef{Value: openapi3.NewRequestBody().WithRequired(true).WithJSONSchemaRef(s)}
		}

		status := r.SuccessStatus()
		resp := openapi3.NewResponse().WithDescription(http.StatusText(status))
		switch {
		case r.RawContent != "":
			resp.WithContent(openapi3.Content{r.RawContent: &openapi3.MediaType{Schema: openapi3.NewStringSchema().WithFormat("binary").NewRef()}})
		case r.ResponseSchema != nil || r.Response != nil:
			var item *openapi3.SchemaRef
			if r.ResponseSchema != nil {
				rf.components[r.SchemaName] = r.ResponseSchema.NewRef()
				item = openapi3.NewSchemaRef("#/components/schemas/"+r.SchemaName, r.ResponseSchema)
			} else {
				item = rf.schemaFor(reflect.TypeOf(r.Response))
			}
			if r.List {
				page := openapi3.NewObjectSchema()
				page.Properties = openapi3.Schemas{
					"items":      openapi3.NewArraySchema().WithItems(item.Value).NewRef(),
					"nextCursor": openapi3.NewStringSchema().NewRef(),
				}
				page.Properties["items"].Value.Items = item
				page.Required = []string{"items"}
				resp.WithJSONSchema(page)
			} else {
				resp.WithJSONSchemaRef(item)
			}
		}
		op.Responses.Set(fmt.Sprint(status), &openapi3.ResponseRef{Value: resp})
		problemResp := func(desc string) *openapi3.ResponseRef {
			return &openapi3.ResponseRef{Value: openapi3.NewResponse().WithDescription(desc).WithContent(openapi3.Content{
				"application/problem+json": &openapi3.MediaType{Schema: problem},
			})}
		}
		op.Responses.Set("default", problemResp("Problem Details (RFC 9457)"))
		if r.Auth == AuthRequired {
			op.Responses.Set("401", problemResp("Not authenticated"))
			op.Responses.Set("403", problemResp("Missing permission, module disabled or MFA required"))
		}

		path := OpenAPIPath(r.Path)
		item := doc.Paths.Value(path)
		if item == nil {
			item = &openapi3.PathItem{}
			doc.Paths.Set(path, item)
		}
		item.SetOperation(r.Method, op)
	}
	names := make([]string, 0, len(tags))
	for t := range tags {
		names = append(names, t)
	}
	slices.Sort(names)
	for _, t := range names {
		doc.Tags = append(doc.Tags, &openapi3.Tag{Name: t})
	}
	return doc, nil
}

func paramRef(p Param) *openapi3.ParameterRef {
	var s *openapi3.Schema
	switch p.Type {
	case "integer":
		s = openapi3.NewIntegerSchema()
	case "boolean":
		s = openapi3.NewBoolSchema()
	default:
		s = openapi3.NewStringSchema()
	}
	for _, e := range p.Enum {
		s.Enum = append(s.Enum, e)
	}
	in := p.In
	if in == "" {
		in = "query"
	}
	return &openapi3.ParameterRef{Value: &openapi3.Parameter{Name: p.Name, In: in, Required: p.Required, Description: p.Description, Schema: s.NewRef()}}
}

// Problem mirrors httpx.Problem for documentation (kernel/route must not
// import httpx to avoid a cycle).
type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Instance  string `json:"instance,omitempty"`
	Code      string `json:"code,omitempty"`
	RequestID string `json:"requestId,omitempty"`
	Errors    []struct {
		Field   string `json:"field"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"errors,omitempty"`
}

// reflector converts Go types to OpenAPI schemas. Named struct types become
// components; field rules:
//   - json tag name is the property name; "-" skips the field
//   - non-pointer without omitempty => required; pointer => nullable
//   - `enum:"a,b,c"` and `format:"email"` tags refine strings
type reflector struct {
	components openapi3.Schemas
	names      map[reflect.Type]string
}

func newReflector() *reflector {
	return &reflector{components: openapi3.Schemas{}, names: map[reflect.Type]string{}}
}

var (
	tUUID    = reflect.TypeOf(uuid.UUID{})
	tTime    = reflect.TypeOf(time.Time{})
	tDecimal = reflect.TypeOf(decimal.Decimal{})
	tRaw     = reflect.TypeOf(json.RawMessage{})
	tDate    = reflect.TypeOf(Date(""))
)

// Date documents a YYYY-MM-DD string.
type Date string

func (rf *reflector) ref(t reflect.Type, name string) *openapi3.SchemaRef {
	rf.names[t] = name
	return rf.schemaFor(t)
}

func (rf *reflector) schemaFor(t reflect.Type) *openapi3.SchemaRef {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t {
	case tUUID:
		return openapi3.NewUUIDSchema().NewRef()
	case tTime:
		return openapi3.NewDateTimeSchema().NewRef()
	case tDecimal:
		s := openapi3.NewStringSchema()
		s.Pattern = `^-?\d+(\.\d+)?$`
		s.Description = "Decimal amount as string (never float)."
		return s.NewRef()
	case tRaw:
		return openapi3.NewObjectSchema().NewRef()
	case tDate:
		return openapi3.NewStringSchema().WithFormat("date").NewRef()
	}
	switch t.Kind() {
	case reflect.String:
		return openapi3.NewStringSchema().NewRef()
	case reflect.Bool:
		return openapi3.NewBoolSchema().NewRef()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return openapi3.NewIntegerSchema().NewRef()
	case reflect.Float32, reflect.Float64:
		return openapi3.NewFloat64Schema().NewRef()
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return openapi3.NewBytesSchema().NewRef()
		}
		a := openapi3.NewArraySchema()
		a.Items = rf.schemaFor(t.Elem())
		return a.NewRef()
	case reflect.Map:
		o := openapi3.NewObjectSchema()
		if t.Elem().Kind() != reflect.Interface {
			o.AdditionalProperties = openapi3.AdditionalProperties{Schema: rf.schemaFor(t.Elem())}
		}
		return o.NewRef()
	case reflect.Interface:
		return openapi3.NewSchema().NewRef()
	case reflect.Struct:
		return rf.structSchema(t)
	}
	return openapi3.NewSchema().NewRef()
}

func (rf *reflector) componentName(t reflect.Type) string {
	if n, ok := rf.names[t]; ok {
		return n
	}
	name := t.Name()
	if name == "" {
		return ""
	}
	if i := strings.Index(name, "["); i >= 0 { // generic instantiation
		name = name[:i]
	}
	pkg := t.PkgPath()
	pkg = pkg[strings.LastIndex(pkg, "/")+1:]
	candidate := name
	for other, n := range rf.names {
		if n == candidate && other != t {
			candidate = strings.ToUpper(pkg[:1]) + pkg[1:] + name
			break
		}
	}
	rf.names[t] = candidate
	return candidate
}

func (rf *reflector) structSchema(t reflect.Type) *openapi3.SchemaRef {
	name := rf.componentName(t)
	if name != "" {
		if _, ok := rf.components[name]; ok {
			return openapi3.NewSchemaRef("#/components/schemas/"+name, rf.components[name].Value)
		}
		// Reserve before recursing to support cycles.
		rf.components[name] = openapi3.NewObjectSchema().NewRef()
	}
	s := openapi3.NewObjectSchema()
	s.Properties = openapi3.Schemas{}
	rf.addFields(t, s)
	if name == "" {
		return s.NewRef()
	}
	rf.components[name] = s.NewRef()
	return openapi3.NewSchemaRef("#/components/schemas/"+name, s)
}

func (rf *reflector) addFields(t reflect.Type, s *openapi3.Schema) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		parts := strings.Split(tag, ",")
		jname := parts[0]
		omitempty := slices.Contains(parts[1:], "omitempty")
		if f.Anonymous && jname == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				rf.addFields(ft, s)
				continue
			}
		}
		if jname == "" {
			jname = f.Name
		}
		ref := rf.schemaFor(f.Type)
		if f.Type.Kind() == reflect.Pointer || f.Tag.Get("enum") != "" || f.Tag.Get("format") != "" || f.Tag.Get("doc") != "" {
			// copy so we do not mutate shared component schemas
			if ref.Ref != "" && f.Type.Kind() == reflect.Pointer {
				wrapper := openapi3.NewSchema()
				wrapper.AllOf = openapi3.SchemaRefs{ref}
				wrapper.Nullable = true
				ref = wrapper.NewRef()
			} else if ref.Ref == "" {
				cp := *ref.Value
				if f.Type.Kind() == reflect.Pointer {
					cp.Nullable = true
				}
				if e := f.Tag.Get("enum"); e != "" {
					cp.Enum = nil
					for _, v := range strings.Split(e, ",") {
						cp.Enum = append(cp.Enum, v)
					}
				}
				if fm := f.Tag.Get("format"); fm != "" {
					cp.Format = fm
				}
				if d := f.Tag.Get("doc"); d != "" {
					cp.Description = d
				}
				ref = cp.NewRef()
			}
		}
		s.Properties[jname] = ref
		if !omitempty && f.Type.Kind() != reflect.Pointer {
			s.Required = append(s.Required, jname)
		}
	}
}
