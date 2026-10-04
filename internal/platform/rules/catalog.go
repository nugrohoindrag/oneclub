package rules

// Club Policies catalogue (PRD P2 EP-28): every module registers the club
// policies it reads, with the Naming Convention §25 category and its typed
// code default. Settings → Club Policies lists the catalogue with the
// version in force, and new versions are validated against the type.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
)

// PolicyDef describes one club policy code.
type PolicyDef struct {
	Code        string `json:"code"`
	Category    string `json:"category"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Default     any    `json:"default"`
}

var (
	defsMu sync.RWMutex
	defs   = map[string]PolicyDef{}
)

// RegisterPolicy adds a policy to the catalogue (module init).
func RegisterPolicy(d PolicyDef) {
	defsMu.Lock()
	defer defsMu.Unlock()
	defs[d.Code] = d
}

// PolicyDefs lists the catalogue sorted by category and code.
func PolicyDefs() []PolicyDef {
	defsMu.RLock()
	defer defsMu.RUnlock()
	out := make([]PolicyDef, 0, len(defs))
	for _, d := range defs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Code < out[j].Code
	})
	return out
}

func policyDef(code string) (PolicyDef, bool) {
	defsMu.RLock()
	defer defsMu.RUnlock()
	d, ok := defs[code]
	return d, ok
}

// validatePolicy checks a value against the registered type: unknown fields
// and wrong types are rejected.
func validatePolicy(code, category string, raw json.RawMessage) []errs.FieldError {
	d, ok := policyDef(code)
	if !ok {
		return nil
	}
	var out []errs.FieldError
	if category != d.Category {
		out = append(out, errs.Field("category", "invalid", "policy "+code+" belongs to "+d.Category))
	}
	v := reflect.New(reflect.TypeOf(d.Default)).Interface()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		out = append(out, errs.Field("value", "invalid", strings.TrimPrefix(err.Error(), "json: ")))
	}
	return out
}

// CatalogEntry is a catalogue policy with the version in force.
type CatalogEntry struct {
	PolicyDef
	InForce        json.RawMessage `json:"inForce" doc:"Configured value in force (null: the default applies)"`
	Version        int             `json:"version" doc:"0 = code default"`
	PropertyScoped bool            `json:"propertyScoped" doc:"The version in force is specific to the active property"`
}

func (s *Service) catalog(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var pid *uuid.UUID
	if p, ok := reqctx.Property(ctx); ok {
		pid = &p
	}
	out := []CatalogEntry{}
	err := s.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		for _, d := range PolicyDefs() {
			e := CatalogEntry{PolicyDef: d}
			raw, version, ok, err := Resolve(ctx, tx, "club_policy", d.Code, pid, time.Now())
			if err != nil {
				return err
			}
			if ok {
				e.InForce, e.Version = raw, version
				var scoped bool
				if pid != nil {
					_ = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.rules WHERE kind = 'club_policy' AND code = $1 AND property_id = $2)`, d.Code, *pid).Scan(&scoped)
				}
				e.PropertyScoped = scoped
			}
			out = append(out, e)
		}
		return nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[CatalogEntry]{Items: out})
}

func (s *Service) registerCatalog(reg *route.Registry) {
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/club-policies/catalog", Module: "platform", Tag: "Club Policies",
		Summary: "Club Policies catalogue: every policy with its category, default and version in force", Permission: "platform.club_policy.view",
		Response: CatalogEntry{}, List: true, Handler: s.catalog})
}

var _ = context.Background
