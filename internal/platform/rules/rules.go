// Package rules is the Business Rules / Club Policies framework (PRD §6.1):
// versioned values with effective dates, optionally overridden per
// property. Domain-specific rules (Golf Policies, Cancellation Policies, …)
// are added by modules from P1 using Resolve.
package rules

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
)

// PolicyCategories follow Naming Convention §25.
var PolicyCategories = []string{"Golf Policies", "Sport Club Policies", "Banquet Policies", "Pricing Policies", "Cancellation Policies",
	"Refund Policies", "Guest Policies", "Member Policies", "Caddy Policies", "Golf Cart Policies", "Weather Policies"}

// Rule is one version.
type Rule struct {
	ID            uuid.UUID       `json:"id"`
	Kind          string          `json:"kind" enum:"business_rule,club_policy"`
	Category      string          `json:"category"`
	Code          string          `json:"code"`
	Name          string          `json:"name"`
	Description   *string         `json:"description"`
	PropertyID    *uuid.UUID      `json:"propertyId"`
	Version       int             `json:"version"`
	EffectiveFrom time.Time       `json:"effectiveFrom"`
	Value         json.RawMessage `json:"value"`
	Status        string          `json:"status" enum:"draft,active,inactive"`
	InEffect      bool            `json:"inEffect" doc:"This version is the one currently in force"`
	CreatedAt     time.Time       `json:"createdAt"`
}

type RuleRequest struct {
	Category      string          `json:"category"`
	Code          string          `json:"code"`
	Name          string          `json:"name"`
	Description   string          `json:"description,omitempty"`
	PropertyID    *uuid.UUID      `json:"propertyId,omitempty"`
	Value         json.RawMessage `json:"value"`
	EffectiveFrom *time.Time      `json:"effectiveFrom,omitempty" doc:"Default now; cannot be in the past once a version exists"`
}

type StatusRequest struct {
	Status string `json:"status" enum:"active,inactive"`
	Reason string `json:"reason,omitempty"`
}

// Service serves both kinds.
type Service struct{ DB *dbtx.DB }

// Resolve returns the value in force for code at time at: a property
// override wins over the instance-wide rule.
func Resolve(ctx context.Context, q dbtx.Querier, kind, code string, property *uuid.UUID, at time.Time) (json.RawMessage, int, bool, error) {
	var raw []byte
	var version int
	err := q.QueryRow(ctx, `SELECT value, version FROM platform.rules WHERE kind = $1 AND code = $2 AND status = 'active'
		AND effective_from <= $3 AND (property_id IS NULL OR property_id = $4)
		ORDER BY (property_id IS NOT NULL) DESC, effective_from DESC, version DESC LIMIT 1`, kind, code, at, property).Scan(&raw, &version)
	if dbtx.IsNoRows(err) {
		return nil, 0, false, nil
	}
	return raw, version, err == nil, err
}

const ruleSelect = `SELECT id, kind, category, code, name, description, property_id, version, effective_from, value, status, created_at FROM platform.rules`

func scanRule(row pgx.Row) (Rule, error) {
	var r Rule
	var raw []byte
	err := row.Scan(&r.ID, &r.Kind, &r.Category, &r.Code, &r.Name, &r.Description, &r.PropertyID, &r.Version, &r.EffectiveFrom, &raw, &r.Status, &r.CreatedAt)
	r.Value = raw
	return r, err
}

var ruleCodeRe = regexp.MustCompile(`^[a-z][a-z0-9_.]{1,80}$`)

func (s *Service) list(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		lp := httpx.ParseList(r)
		history := r.URL.Query().Get("history") == "true"
		out := []Rule{}
		err := s.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
			args := []any{kind}
			where := "kind = $1"
			if c := lp.Filters["category"]; c != "" {
				args = append(args, c)
				where += " AND category = $2"
			}
			if c := lp.Filters["code"]; c != "" {
				args = append(args, c)
				where += " AND code = $" + string(rune('0'+len(args)))
			}
			sql := ruleSelect + " WHERE " + where + " ORDER BY category, code, property_id NULLS FIRST, version DESC"
			rows, err := tx.Query(ctx, sql, args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			now := clock.Now()
			seen := map[string]bool{}
			for rows.Next() {
				x, err := scanRule(rows)
				if err != nil {
					return err
				}
				key := x.Code + "|" + uuidStr(x.PropertyID)
				if !seen[key] && x.Status == "active" && !x.EffectiveFrom.After(now) {
					x.InEffect = true
					seen[key] = true
				}
				if history || x.InEffect || x.EffectiveFrom.After(now) {
					out = append(out, x)
				}
			}
			return rows.Err()
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, httpx.Page[Rule]{Items: out})
	}
}

func uuidStr(u *uuid.UUID) string {
	if u == nil {
		return ""
	}
	return u.String()
}

func (s *Service) create(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req RuleRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		var fields []errs.FieldError
		if !ruleCodeRe.MatchString(req.Code) {
			fields = append(fields, errs.Field("code", "invalid", "lower-case letters, digits, dot and underscore"))
		}
		if strings.TrimSpace(req.Name) == "" {
			fields = append(fields, errs.Field("name", "required", "name is required"))
		}
		if strings.TrimSpace(req.Category) == "" {
			fields = append(fields, errs.Field("category", "required", "category is required"))
		} else if kind == "club_policy" && !slices.Contains(PolicyCategories, req.Category) {
			fields = append(fields, errs.Field("category", "invalid", "one of: "+strings.Join(PolicyCategories, ", ")))
		}
		var v any
		if len(req.Value) == 0 || json.Unmarshal(req.Value, &v) != nil {
			fields = append(fields, errs.Field("value", "invalid", "value must be valid JSON"))
		}
		if len(fields) > 0 {
			httpx.WriteError(w, r, errs.Validation("invalid_rule", "invalid rule", fields...))
			return
		}
		ctx := r.Context()
		eff := clock.Now()
		if req.EffectiveFrom != nil {
			eff = req.EffectiveFrom.UTC()
		}
		var out Rule
		err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
			var maxV *int
			if err := tx.QueryRow(ctx, `SELECT max(version) FROM platform.rules WHERE kind = $1 AND code = $2 AND property_id IS NOT DISTINCT FROM $3`,
				kind, req.Code, req.PropertyID).Scan(&maxV); err != nil {
				return err
			}
			version := 1
			if maxV != nil {
				version = *maxV + 1
				if eff.Before(clock.Now().Add(-time.Minute)) {
					return errs.Validation("effective_date_past", "a new version cannot take effect in the past",
						errs.Field("effectiveFrom", "past", "choose now or a future date"))
				}
			}
			rid := id.New()
			if _, err := tx.Exec(ctx, `INSERT INTO platform.rules (id, kind, category, code, name, description, property_id, version,
				effective_from, value, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)`,
				rid, kind, req.Category, req.Code, req.Name, nullable(req.Description), req.PropertyID, version, eff, []byte(req.Value),
				id.Ptr(authz.From(ctx).UserID)); err != nil {
				if dbtx.IsForeignKeyViolation(err) {
					return errs.Validation("invalid_property", "property not found", errs.Field("propertyId", "invalid", "property not found"))
				}
				return err
			}
			var err error
			if out, err = scanRule(tx.QueryRow(ctx, ruleSelect+" WHERE id = $1", rid)); err != nil {
				return err
			}
			return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionCreate, EntityType: "platform." + kind,
				EntityID: rid.String(), EntityLabel: req.Code + " v" + itoa(version), PropertyID: req.PropertyID, After: out})
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, out)
	}
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *Service) setStatus(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rid, err := httpx.PathUUID(r, "id")
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		var req StatusRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if req.Status != "active" && req.Status != "inactive" {
			httpx.WriteError(w, r, errs.Validation("invalid_status", "invalid status", errs.Field("status", "invalid", "active or inactive")))
			return
		}
		ctx := r.Context()
		var out Rule
		err = s.DB.WithTx(ctx, func(tx pgx.Tx) error {
			before, err := scanRule(tx.QueryRow(ctx, ruleSelect+" WHERE id = $1 AND kind = $2 FOR UPDATE", rid, kind))
			if dbtx.IsNoRows(err) {
				return errs.NotFound("rule")
			}
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE platform.rules SET status = $2, updated_by = $3 WHERE id = $1`, rid, req.Status, id.Ptr(authz.From(ctx).UserID)); err != nil {
				return err
			}
			if out, err = scanRule(tx.QueryRow(ctx, ruleSelect+" WHERE id = $1", rid)); err != nil {
				return err
			}
			return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionStatusChange, EntityType: "platform." + kind,
				EntityID: rid.String(), EntityLabel: out.Code + " v" + itoa(out.Version), PropertyID: out.PropertyID, Reason: req.Reason,
				Before: map[string]any{"status": before.Status}, After: map[string]any{"status": out.Status}})
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

// Register adds Business Rules and Club Policies routes.
func (s *Service) Register(reg *route.Registry) {
	for _, k := range []struct{ kind, path, perm, tag string }{
		{"business_rule", "/api/v1/platform/business-rules", "platform.business_rule", "Business Rules"},
		{"club_policy", "/api/v1/platform/club-policies", "platform.club_policy", "Club Policies"},
	} {
		q := []route.Param{{Name: "filter[category]"}, {Name: "filter[code]"}, {Name: "history", Type: "boolean"}}
		reg.Add(route.Route{Method: http.MethodGet, Path: k.path, Module: "platform", Tag: k.tag, Summary: "List " + k.tag + " (in force and scheduled)",
			Permission: k.perm + ".view", Response: Rule{}, List: true, Query: q, Handler: s.list(k.kind),
			OperationID: "list" + strings.ReplaceAll(k.tag, " ", "")})
		reg.Add(route.Route{Method: http.MethodPost, Path: k.path, Module: "platform", Tag: k.tag, Summary: "Add a new version",
			Permission: k.perm + ".manage", Request: RuleRequest{}, Response: Rule{}, Handler: s.create(k.kind),
			OperationID: "create" + strings.ReplaceAll(k.tag, " ", "")})
		reg.Add(route.Route{Method: http.MethodPost, Path: k.path + "/{id}:set-status", Module: "platform", Tag: k.tag, Summary: "Activate or deactivate a version",
			Permission: k.perm + ".manage", Request: StatusRequest{}, Response: Rule{}, Status: http.StatusOK, Handler: s.setStatus(k.kind),
			OperationID: "setStatus" + strings.ReplaceAll(k.tag, " ", "")})
	}
}
