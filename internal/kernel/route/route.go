// Package route is the declarative API route registry. One Route declaration
// drives: HTTP mounting, authentication mode, module gating (FR-INS-04),
// permission + property scope (FR-IAM-11), idempotency (FR-JOB-06), audit
// enforcement (FR-AUD-01) and the published OpenAPI document (FR-TEC-02).
package route

import (
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Scope decides the Row Level Security scope of a request.
type Scope int

const (
	// ScopeGlobal: instance-level resource; RLS allows every property where
	// the principal holds Permission (or any role when Permission is empty).
	ScopeGlobal Scope = iota
	// ScopeProperty: requires the active property (header X-Property-Id,
	// chosen in the property switcher); permission is checked at that
	// property and RLS is narrowed to it.
	ScopeProperty
)

// Auth is the authentication mode.
type Auth int

const (
	AuthRequired   Auth = iota // session or API key; MFA must be complete
	AuthPublic                 // anonymous allowed
	AuthMFAPending             // allowed while the session still awaits MFA
	AuthSignature              // anonymous; handler verifies a signature (webhooks, bridge agents)
)

// Param documents a query or header parameter.
type Param struct {
	Name        string
	In          string // query | header
	Description string
	Required    bool
	Type        string // string | integer | boolean
	Enum        []string
}

// Route declares one API operation.
type Route struct {
	Method      string
	Path        string // chi pattern, e.g. /api/v1/platform/venues/{id}
	OperationID string
	Summary     string
	Description string
	Tag         string
	Module      string // module code used for Enabled Modules gating
	Permission  string // required permission; "" = any authenticated principal
	Scope       Scope
	Auth        Auth

	Request    any // zero value of the request body type (nil = no body)
	Response   any // zero value of the response type (nil = 204)
	List       bool
	Status     int    // success status; default 200, 201 for POST with body response
	RawContent string // non-JSON response content type, e.g. text/csv

	// RequestSchema/ResponseSchema override reflection (generic resources).
	RequestSchema  *openapi3.Schema
	ResponseSchema *openapi3.Schema
	SchemaName     string // component name for ResponseSchema/RequestSchema base

	Query      []Param
	Idempotent bool   // honours Idempotency-Key
	NoAudit    string // reason a mutating route writes no audit entry

	Handler http.HandlerFunc
}

// Mutating reports whether the route changes data.
func (r *Route) Mutating() bool {
	return r.Method != http.MethodGet && r.Method != http.MethodHead
}

// SuccessStatus returns the documented success status code.
func (r *Route) SuccessStatus() int {
	if r.Status != 0 {
		return r.Status
	}
	if r.Response == nil && r.ResponseSchema == nil && r.RawContent == "" {
		return http.StatusNoContent
	}
	if r.Method == http.MethodPost && !r.List {
		return http.StatusCreated
	}
	return http.StatusOK
}

// ID is "METHOD path".
func (r *Route) ID() string { return r.Method + " " + r.Path }

// Registry collects routes from all modules.
type Registry struct {
	routes []*Route
	seen   map[string]bool
}

func NewRegistry() *Registry { return &Registry{seen: map[string]bool{}} }

// Add registers a route; duplicate METHOD+path panics at startup.
func (g *Registry) Add(r Route) {
	if r.Handler == nil {
		panic("route: nil handler for " + r.ID())
	}
	if g.seen[r.ID()] {
		panic("route: duplicate " + r.ID())
	}
	if r.OperationID == "" {
		r.OperationID = defaultOperationID(r.Method, r.Path)
	}
	if r.Module == "" {
		panic("route: module required for " + r.ID())
	}
	g.seen[r.ID()] = true
	g.routes = append(g.routes, &r)
}

// Routes returns all routes sorted by path then method.
func (g *Registry) Routes() []*Route {
	out := append([]*Route(nil), g.routes...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path == out[j].Path {
			return out[i].Method < out[j].Method
		}
		return out[i].Path < out[j].Path
	})
	return out
}

var nonWord = regexp.MustCompile(`[^A-Za-z0-9]+`)

func defaultOperationID(method, path string) string {
	p := strings.TrimPrefix(path, "/api/v1/")
	p = strings.NewReplacer("{", "by-", "}", "", ":", "-").Replace(p)
	parts := nonWord.Split(p, -1)
	var b strings.Builder
	b.WriteString(strings.ToLower(method))
	for _, s := range parts {
		if s == "" {
			continue
		}
		b.WriteString(strings.ToUpper(s[:1]) + s[1:])
	}
	return b.String()
}

// PathParams returns {name} parameters in a chi path.
func PathParams(path string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`\{([a-zA-Z0-9_]+)(:[^}]*)?\}`).FindAllStringSubmatch(path, -1) {
		out = append(out, m[1])
	}
	return out
}

// OpenAPIPath converts a chi path to OpenAPI form (drops regexps).
func OpenAPIPath(path string) string {
	return regexp.MustCompile(`\{([a-zA-Z0-9_]+):[^}]*\}`).ReplaceAllString(path, "{$1}")
}
