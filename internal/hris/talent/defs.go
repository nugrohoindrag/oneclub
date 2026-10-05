package talent

// Master data through the generic resource engine (list, view, add, edit,
// delete-or-archive, CSV / XLSX export and import, audit): candidates
// (FR-RCT-02) and review templates per position with competencies and KPIs
// (FR-PRF-HR-01).

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/mask"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

// Resource keys.
const (
	KeyCandidate      = "hris.candidate"
	KeyReviewTemplate = "hris.review_template"
)

var (
	codeRe30   = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,29}$`)
	itemCodeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,39}$`)
)

// Candidate sources and education levels.
var (
	Sources   = []string{"website", "referral", "walk_in", "job_portal", "agency", "internal", "social_media", "other"}
	Education = []string{"sd", "smp", "sma", "d1", "d3", "s1", "s2", "s3", "other"}
)

// Defs returns the resource definitions.
func (m *Module) Defs() []*resource.Def {
	candidates := &resource.Def{
		Key: KeyCandidate, Module: hris.Module, Perm: KeyCandidate, Path: "/api/v1/hris/candidates", Table: "hris.candidates", Name: "Candidate",
		Plural: "Candidates", SchemaName: "RecruitmentCandidate", Tag: "HRIS Recruitment", PropertyScoped: true, Archive: true,
		OrderBy: "created_at DESC, id DESC",
		Fields: []resource.Field{
			{Name: "fullName", Column: "full_name", Label: "Full Name", Kind: resource.String, Required: true, Max: 120, Search: true},
			{Name: "email", Column: "email", Label: "E-mail", Kind: resource.Email, Max: 254, Search: true},
			{Name: "phone", Column: "phone", Label: "Phone", Kind: resource.String, Max: 40, Search: true},
			{Name: "gender", Column: "gender", Label: "Gender", Kind: resource.Enum, Enum: []string{"male", "female"}},
			{Name: "birthDate", Column: "birth_date", Label: "Date of Birth", Kind: resource.Date},
			{Name: "city", Column: "city", Label: "City", Kind: resource.String, Max: 80, Filter: true},
			{Name: "address", Column: "address", Label: "Address", Kind: resource.Text, Max: 500},
			{Name: "education", Column: "education", Label: "Education", Kind: resource.Enum, Enum: Education, Filter: true},
			{Name: "currentEmployer", Column: "current_employer", Label: "Current Employer", Kind: resource.String, Max: 120},
			{Name: "currentTitle", Column: "current_title", Label: "Current Job Title", Kind: resource.String, Max: 120},
			{Name: "experienceYears", Column: "experience_years", Label: "Experience (years)", Kind: resource.Decimal, Min: resource.Min(0),
				MaxN: resource.Max(60)},
			{Name: "expectedSalary", Column: "expected_salary", Label: "Expected Salary", Kind: resource.Decimal, Min: resource.Min(0)},
			{Name: "source", Column: "source", Label: "Source", Kind: resource.Enum, Enum: Sources, Default: "other", Filter: true},
			{Name: "referredById", Column: "referred_by_id", Label: "Referred By", Kind: resource.UUID, Filter: true,
				Ref: &resource.Ref{Table: "hris.employees", SameProperty: true, Label: "employee"}},
			{Name: "employeeId", Column: "employee_id", Label: "Employee (internal candidate)", Kind: resource.UUID, Filter: true,
				Ref: &resource.Ref{Table: "hris.employees", SameProperty: true, Label: "employee"}},
			{Name: "cvFileId", Column: "cv_file_id", Label: "CV (uploaded with POST /api/v1/hris/recruitment-files)", Kind: resource.UUID},
			{Name: "profileUrl", Column: "profile_url", Label: "Profile / Portfolio URL", Kind: resource.String, Max: 300},
			{Name: "notes", Column: "notes", Label: "Notes", Kind: resource.Text, Max: 4000},
			{Name: "consentAt", Column: "consent_at", Label: "Consent to Process the Application (UU PDP)", Kind: resource.Timestamp},
			{Name: "talentPoolConsent", Column: "talent_pool_consent", Label: "Talent Pool Consent", Kind: resource.Bool, Default: false, Filter: true},
			{Name: "retentionUntil", Column: "retention_until", Label: "Erased On", Kind: resource.Date, ReadOnly: true},
			{Name: "erasedAt", Column: "erased_at", Label: "Erased At", Kind: resource.Timestamp, ReadOnly: true},
			{Name: "status", Column: "status", Label: "Status", Kind: resource.Enum, Enum: []string{"active", "hired", "erased"}, Default: "active",
				ReadOnly: true, Filter: true},
		},
		Hooks: resource.Hooks{BeforeWrite: m.candidateBeforeWrite, AfterRead: maskCandidate},
	}
	templates := &resource.Def{
		Key: KeyReviewTemplate, Module: hris.Module, Perm: KeyReviewTemplate, Path: "/api/v1/hris/review-templates", Table: "hris.review_templates",
		Name: "Review Template", Plural: "Review Templates", SchemaName: "PerformanceReviewTemplate", Tag: "HRIS Performance Review", PropertyScoped: true,
		Archive: true, CodeField: "code", OrderBy: "name, id",
		Fields: []resource.Field{
			{Name: "code", Column: "code", Label: "Code", Kind: resource.String, Required: true, Max: 30, Upper: true, Pattern: codeRe30,
				PatternMsg: "1–30 characters: A–Z, 0–9, - or _", Search: true},
			resource.Name(),
			{Name: "reviewType", Column: "review_type", Label: "Review Type", Kind: resource.Enum, Enum: []string{"annual", "semester", "probation", "any"},
				Default: "any", Filter: true},
			{Name: "positionCodes", Column: "position_codes", Label: "Positions (codes; empty = every position)", Kind: resource.StringList, Upper: true,
				Default: []string{}},
			{Name: "competencies", Column: "competencies", Label: "Competencies ([{code, label, description, weight}])", Kind: resource.JSONList,
				Default: "[]"},
			{Name: "kpis", Column: "kpis", Label: "KPIs ([{code, label, description, weight, target}])", Kind: resource.JSONList, Default: "[]"},
			{Name: "competencyWeight", Column: "competency_weight", Label: "Competency Weight (%; KPIs get the rest)", Kind: resource.Decimal,
				Default: "50", Min: resource.Min(0), MaxN: resource.Max(100)},
			{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 2000},
			resource.Status("active", "inactive"),
		},
		Hooks: resource.Hooks{BeforeWrite: templateBeforeWrite},
	}
	return []*resource.Def{candidates, templates}
}

// propertyOf is the property of a row being written.
func propertyOf(ctx context.Context, before map[string]any) uuid.UUID {
	if before != nil {
		if s, ok := before["propertyId"].(string); ok {
			if u, err := uuid.Parse(s); err == nil {
				return u
			}
		}
	}
	p, _ := reqctx.Property(ctx)
	return p
}

func (m *Module) candidateBeforeWrite(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	if before != nil && before["status"] == "erased" {
		return errs.Conflict("candidate_erased", "the candidate's personal data was erased")
	}
	// Masked values sent back unchanged are ignored.
	for _, f := range []string{"expectedSalary"} {
		if s, ok := v[f].(string); ok && (strings.Contains(s, "*") || s == mask.Redacted) {
			delete(v, f)
		}
	}
	if fid, ok := v["cvFileId"].(string); ok && fid != "" {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.files WHERE id = $1::uuid AND purpose = 'attachment')`, fid).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return handle.Invalid("cvFileId", "not_found", "file not found: upload the CV first")
		}
	}
	if before == nil {
		email, _ := v["email"].(string)
		phone, _ := v["phone"].(string)
		if email == "" && phone == "" {
			return handle.Invalid("email", "required", "an e-mail or phone is required to contact the candidate")
		}
		if dup, err := findCandidate(ctx, tx, propertyOf(ctx, nil), email, phone); err != nil {
			return err
		} else if dup != nil {
			return errs.Conflict("candidate_exists", "a candidate with this e-mail or phone exists: add an application to "+dup.String())
		}
	}
	return nil
}

// findCandidate returns the live candidate with the e-mail or phone.
func findCandidate(ctx context.Context, q pgx.Tx, property uuid.UUID, email, phone string) (*uuid.UUID, error) {
	email, phone = strings.ToLower(strings.TrimSpace(email)), normPhone(phone)
	if email == "" && phone == "" {
		return nil, nil
	}
	var id *uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM hris.candidates WHERE property_id = $1 AND erased_at IS NULL AND archived_at IS NULL
		AND (($2 <> '' AND lower(email) = $2) OR ($3 <> '' AND regexp_replace(coalesce(phone, ''), '[^0-9]', '', 'g') = $3))
		ORDER BY created_at LIMIT 1`, property, email, phone).Scan(&id)
	if err != nil && !dbtx.IsNoRows(err) {
		return nil, err
	}
	return id, nil
}

// normPhone keeps the digits of a phone, 0… becomes 62… (Indonesia).
func normPhone(s string) string {
	d := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	if strings.HasPrefix(d, "0") {
		d = "62" + d[1:]
	}
	return d
}

// maskCandidate hides the expected salary and CV from users without
// hris.candidate.view_sensitive (UU PDP).
func maskCandidate(ctx context.Context, row map[string]any) {
	pid, _ := uuid.Parse(str(row["propertyId"]))
	if can(ctx, "hris.candidate.view_sensitive", pid) {
		return
	}
	if row["expectedSalary"] != nil {
		row["expectedSalary"] = mask.Redacted
	}
	if row["cvFileId"] != nil {
		row["cvFileId"] = nil
	}
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// ReviewTemplateItem is a competency or KPI of a review template.
type ReviewTemplateItem struct {
	Code        string `json:"code"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Weight      string `json:"weight,omitempty" doc:"Relative weight (default 1)"`
	Target      string `json:"target,omitempty" doc:"KPI target (e.g. 95% attendance)"`
}

// parseItems validates the competencies / KPIs of a template.
func parseItems(field, raw string) ([]ReviewTemplateItem, error) {
	var items []ReviewTemplateItem
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&items); err != nil {
		return nil, handle.Invalid(field, "invalid", "a list of {code, label, description, weight, target}")
	}
	seen := map[string]bool{}
	for i := range items {
		it := &items[i]
		it.Code = strings.ToLower(strings.TrimSpace(it.Code))
		it.Label = strings.TrimSpace(it.Label)
		if !itemCodeRe.MatchString(it.Code) {
			return nil, handle.Invalid(field, "invalid_code", "item codes are 1–40 characters a–z, 0–9 or _")
		}
		if it.Label == "" {
			return nil, handle.Invalid(field, "required", "every item has a label")
		}
		if seen[it.Code] {
			return nil, handle.Invalid(field, "duplicate", "duplicate item code "+it.Code)
		}
		seen[it.Code] = true
		if it.Weight == "" {
			it.Weight = "1"
		}
		if w, err := decimal.NewFromString(it.Weight); err != nil || !w.IsPositive() || w.GreaterThan(decimal.NewFromInt(100)) {
			return nil, handle.Invalid(field, "invalid_weight", "weights are numbers above 0 and up to 100")
		}
	}
	return items, nil
}

func templateBeforeWrite(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	for _, f := range []string{"competencies", "kpis"} {
		raw, ok := v[f].(string)
		if !ok {
			continue
		}
		items, err := parseItems(f, raw)
		if err != nil {
			return err
		}
		norm, _ := json.Marshal(items)
		v[f] = string(norm)
	}
	if list, ok := v["positionCodes"].([]string); ok && len(list) > 0 {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(DISTINCT code) FROM hris.positions WHERE property_id = $1 AND code = ANY ($2) AND archived_at IS NULL`,
			propertyOf(ctx, before), list).Scan(&n); err != nil {
			return err
		}
		if n != len(uniq(list)) {
			return handle.Invalid("positionCodes", "not_found", "unknown position code")
		}
	}
	return nil
}

func uniq(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
