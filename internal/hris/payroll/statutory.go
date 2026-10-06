package payroll

// Statutory rate sets (PRD P5 EP-10 FR-TAX-HR-01/03/05, §6 #18,
// /hris/statutory-rates): PTKP, PPh 21 TER and Article 17 rates, the final
// tax on severance and the BPJS rates with wage caps, versioned by
// effective date. A draft is activated by a second person (FR-PPY-05:
// review by two people for payroll rule changes); the tax consultant's
// verification is recorded on the set (initial values are marked "to be
// verified by the tax consultant").

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// StatutoryRateSet is a versioned statutory rate set.
type StatutoryRateSet struct {
	ID                 uuid.UUID           `json:"id" db:"id"`
	Code               string              `json:"code" db:"code"`
	Name               string              `json:"name" db:"name"`
	EffectiveFrom      time.Time           `json:"effectiveFrom" db:"effective_from"`
	Status             string              `json:"status" db:"status" enum:"draft,active,inactive"`
	Rates              hris.StatutoryRates `json:"rates" db:"rates"`
	Regulation         *string             `json:"regulation" db:"regulation"`
	VerificationStatus string              `json:"verificationStatus" db:"verification_status" enum:"unverified,verified" doc:"unverified: to be verified by the tax consultant"`
	VerifiedBy         *uuid.UUID          `json:"verifiedBy" db:"verified_by"`
	VerifiedAt         *time.Time          `json:"verifiedAt" db:"verified_at"`
	VerificationNote   *string             `json:"verificationNote" db:"verification_note"`
	ActivatedBy        *uuid.UUID          `json:"activatedBy" db:"activated_by"`
	ActivatedAt        *time.Time          `json:"activatedAt" db:"activated_at"`
	Notes              *string             `json:"notes" db:"notes"`
	InForce            bool                `json:"inForce" db:"in_force" doc:"The active set applied today"`
	CreatedBy          *uuid.UUID          `json:"createdBy" db:"created_by"`
	UpdatedBy          *uuid.UUID          `json:"updatedBy" db:"updated_by"`
	CreatedAt          time.Time           `json:"createdAt" db:"created_at"`
	UpdatedAt          time.Time           `json:"updatedAt" db:"updated_at"`
}

// StatutoryRateInput creates or edits a draft rate set (rates omitted on
// create = a copy of the set in force).
type StatutoryRateInput struct {
	Code          string               `json:"code,omitempty"`
	Name          string               `json:"name,omitempty"`
	EffectiveFrom string               `json:"effectiveFrom,omitempty"`
	Rates         *hris.StatutoryRates `json:"rates,omitempty"`
	Regulation    *string              `json:"regulation,omitempty"`
	Notes         *string              `json:"notes,omitempty"`
}

// StatutoryRateDecision is the note of an activation, verification or
// deactivation.
type StatutoryRateDecision struct {
	Note string `json:"note,omitempty"`
}

var rateCodeRe = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,29}$`)

const rateSelect = `SELECT s.id, s.code, s.name, s.effective_from, s.status, s.rates, s.regulation, s.verification_status, s.verified_by, s.verified_at,
	s.verification_note, s.activated_by, s.activated_at, s.notes, s.created_by, s.updated_by, s.created_at, s.updated_at,
	(s.status = 'active' AND s.id = (SELECT x.id FROM hris.statutory_rate_sets x WHERE x.status = 'active' AND x.effective_from <= $1::date
	  ORDER BY x.effective_from DESC LIMIT 1)) AS in_force
	FROM hris.statutory_rate_sets s`

func (m *Module) registerStatutory(reg *route.Registry) {
	tag := "HRIS Payroll"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/statutory-rates", Summary: "Statutory rate sets (PTKP, PPh 21, BPJS)",
		Permission: PermRateView, Response: StatutoryRateSet{}, List: true, Handler: listRead(m.DB, m.ratesHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/statutory-rates", Summary: "Create a draft statutory rate set",
		Permission: PermRateManage, Request: StatutoryRateInput{}, Response: StatutoryRateSet{}, Idempotent: true,
		Handler: handle.Write(m.DB, http.StatusCreated, m.createRateHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/statutory-rates/{id}", Summary: "Statutory rate set", Permission: PermRateView,
		Response: StatutoryRateSet{}, Handler: handle.Read(m.DB, m.getRateHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPatch, Path: "/api/v1/hris/statutory-rates/{id}", Summary: "Edit a draft statutory rate set",
		Permission: PermRateManage, Request: StatutoryRateInput{}, Response: StatutoryRateSet{}, Handler: handle.Write(m.DB, http.StatusOK, m.patchRateHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/statutory-rates/{id}:activate",
		Summary: "Activate a draft statutory rate set (by another person than its last editor)", Permission: PermRateActivate,
		Request: StatutoryRateDecision{}, Response: StatutoryRateSet{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.activateRateHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/statutory-rates/{id}:verify",
		Summary: "Record the tax consultant's verification of a rate set", Permission: PermRateVerify, Request: StatutoryRateDecision{},
		Response: StatutoryRateSet{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.verifyRateHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/statutory-rates/{id}:deactivate", Summary: "Deactivate a statutory rate set",
		Permission: PermRateActivate, Request: StatutoryRateDecision{}, Response: StatutoryRateSet{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.deactivateRateHTTP)})
}

func (m *Module) ratesHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request) ([]StatutoryRateSet, error) {
	return handle.List[StatutoryRateSet](tx.Query(ctx, rateSelect+` ORDER BY s.effective_from DESC, s.created_at DESC`, ymd(today(ctx, tx, handle.Property(ctx)))))
}

func (m *Module) rateSet(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (StatutoryRateSet, error) {
	rows, err := tx.Query(ctx, rateSelect+` WHERE s.id = $2`, ymd(today(ctx, tx, handle.Property(ctx))), rid)
	return handle.One[StatutoryRateSet](rows, err, "statutory rate set")
}

func (m *Module) getRateHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (StatutoryRateSet, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return StatutoryRateSet{}, err
	}
	return m.rateSet(ctx, tx, rid)
}

// validateRates checks a rate set (FR-TAX-HR-01/03).
func validateRates(r hris.StatutoryRates) error {
	if len(r.PTKP) == 0 || len(r.TERCategories) == 0 {
		return handle.Invalid("rates.ptkp", "required", "PTKP amounts and TER categories are required")
	}
	for status, cat := range r.TERCategories {
		if _, ok := r.TERRates[cat]; !ok {
			return handle.Invalid("rates.terCategories", "invalid", "TER category "+cat+" of "+status+" has no rate table")
		}
		if hris.Dec(r.PTKP[status]).IsZero() {
			return handle.Invalid("rates.ptkp", "invalid", "PTKP of "+status+" is missing")
		}
	}
	check := func(field string, table []hris.TaxBracket) error {
		if len(table) == 0 {
			return handle.Invalid(field, "required", "at least one bracket")
		}
		prev := decimal.Zero
		for i, b := range table {
			if _, err := decimal.NewFromString(b.Rate); err != nil {
				return handle.Invalid(field, "invalid", "bracket rate must be a number")
			}
			if b.UpTo == "" {
				if i != len(table)-1 {
					return handle.Invalid(field, "invalid", "only the last bracket is open-ended")
				}
				continue
			}
			up, err := decimal.NewFromString(b.UpTo)
			if err != nil || !up.GreaterThan(prev) {
				return handle.Invalid(field, "invalid", "bracket limits must increase")
			}
			prev = up
		}
		if table[len(table)-1].UpTo != "" {
			return handle.Invalid(field, "invalid", "the last bracket must be open-ended")
		}
		return nil
	}
	for cat, t := range r.TERRates {
		if err := check("rates.terRates."+cat, t); err != nil {
			return err
		}
	}
	if err := check("rates.progressiveRates", r.ProgressiveRates); err != nil {
		return err
	}
	if err := check("rates.severanceTaxBrackets", r.SeveranceTaxBrackets); err != nil {
		return err
	}
	for name, c := range map[string]hris.ContributionRate{"kesehatan": r.BPJS.Kesehatan, "jht": r.BPJS.JHT, "jp": r.BPJS.JP, "jkk": r.BPJS.JKK,
		"jkm": r.BPJS.JKM} {
		for _, v := range []string{c.EmployerPercent, c.EmployeePercent} {
			d, err := decimal.NewFromString(v)
			if err != nil || d.IsNegative() || d.GreaterThan(decimal.NewFromInt(100)) {
				return handle.Invalid("rates.bpjs."+name, "invalid", "contribution percentages between 0 and 100")
			}
		}
		if c.WageCap != "" {
			if d, err := decimal.NewFromString(c.WageCap); err != nil || !d.IsPositive() {
				return handle.Invalid("rates.bpjs."+name+".wageCap", "invalid", "a positive wage cap (empty = no cap)")
			}
		}
	}
	return nil
}

func (m *Module) createRateHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request, req StatutoryRateInput) (StatutoryRateSet, error) {
	code := strings.ToUpper(strings.TrimSpace(req.Code))
	if !rateCodeRe.MatchString(code) {
		return StatutoryRateSet{}, handle.Invalid("code", "invalid", "1–30 characters: A–Z, 0–9, - or _")
	}
	if strings.TrimSpace(req.Name) == "" {
		return StatutoryRateSet{}, handle.Invalid("name", "required", "is required")
	}
	eff, err := mustDate("effectiveFrom", req.EffectiveFrom)
	if err != nil {
		return StatutoryRateSet{}, err
	}
	rates := req.Rates
	if rates == nil {
		cur, _, err := statutoryRatesAt(ctx, tx, handle.Property(ctx), eff)
		if err != nil {
			return StatutoryRateSet{}, err
		}
		rates = &cur
	}
	if err := validateRates(*rates); err != nil {
		return StatutoryRateSet{}, err
	}
	raw, _ := json.Marshal(rates)
	rid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.statutory_rate_sets (id, code, name, effective_from, rates, regulation, notes, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)`, rid, code, strings.TrimSpace(req.Name), ymd(eff), raw, req.Regulation, req.Notes, actor(ctx)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return StatutoryRateSet{}, errs.Conflict("code_taken", "a rate set with this code exists")
		}
		return StatutoryRateSet{}, err
	}
	property := handle.Property(ctx)
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.statutory_rate_set", EntityID: rid.String(),
		EntityLabel: code, PropertyID: &property, After: map[string]any{"code": code, "effectiveFrom": ymd(eff), "rates": rates}}); err != nil {
		return StatutoryRateSet{}, err
	}
	return m.rateSet(ctx, tx, rid)
}

func lockRate(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (string, *uuid.UUID, error) {
	var status string
	var editor *uuid.UUID
	err := tx.QueryRow(ctx, `SELECT status, coalesce(updated_by, created_by) FROM hris.statutory_rate_sets WHERE id = $1 FOR UPDATE`, rid).Scan(&status, &editor)
	if dbtx.IsNoRows(err) {
		return "", nil, errs.NotFound("statutory rate set")
	}
	return status, editor, err
}

func (m *Module) patchRateHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req StatutoryRateInput) (StatutoryRateSet, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return StatutoryRateSet{}, err
	}
	status, _, err := lockRate(ctx, tx, rid)
	if err != nil {
		return StatutoryRateSet{}, err
	}
	if status != "draft" {
		return StatutoryRateSet{}, errs.Conflict("rate_set_not_draft", "only a draft rate set can be edited; create a new version instead")
	}
	if req.Code != "" {
		return StatutoryRateSet{}, handle.Invalid("code", "read_only", "the code cannot change")
	}
	before, err := m.rateSet(ctx, tx, rid)
	if err != nil {
		return before, err
	}
	name, eff, rates := before.Name, before.EffectiveFrom, before.Rates
	if strings.TrimSpace(req.Name) != "" {
		name = strings.TrimSpace(req.Name)
	}
	if req.EffectiveFrom != "" {
		if eff, err = mustDate("effectiveFrom", req.EffectiveFrom); err != nil {
			return before, err
		}
	}
	if req.Rates != nil {
		if err := validateRates(*req.Rates); err != nil {
			return before, err
		}
		rates = *req.Rates
	}
	raw, _ := json.Marshal(rates)
	if _, err := tx.Exec(ctx, `UPDATE hris.statutory_rate_sets SET name = $2, effective_from = $3, rates = $4, regulation = coalesce($5, regulation),
		notes = coalesce($6, notes), updated_by = $7, verification_status = 'unverified', verified_by = NULL, verified_at = NULL WHERE id = $1`,
		rid, name, ymd(eff), raw, req.Regulation, req.Notes, actor(ctx)); err != nil {
		return before, err
	}
	property := handle.Property(ctx)
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionUpdate, EntityType: "hris.statutory_rate_set", EntityID: rid.String(),
		EntityLabel: before.Code, PropertyID: &property, Before: map[string]any{"effectiveFrom": ymd(before.EffectiveFrom), "rates": before.Rates},
		After: map[string]any{"effectiveFrom": ymd(eff), "rates": rates}}); err != nil {
		return before, err
	}
	return m.rateSet(ctx, tx, rid)
}

func (m *Module) activateRateHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req StatutoryRateDecision) (StatutoryRateSet, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return StatutoryRateSet{}, err
	}
	status, editor, err := lockRate(ctx, tx, rid)
	if err != nil {
		return StatutoryRateSet{}, err
	}
	if status != "draft" {
		return StatutoryRateSet{}, errs.Conflict("rate_set_not_draft", "only a draft rate set can be activated")
	}
	if me := actor(ctx); me != nil && editor != nil && *me == *editor {
		return StatutoryRateSet{}, errs.Forbidden("a second person activates a rate set (FR-PPY-05): you edited it last")
	}
	var clash bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.statutory_rate_sets a JOIN hris.statutory_rate_sets d ON d.id = $1
		WHERE a.status = 'active' AND a.effective_from = d.effective_from)`, rid).Scan(&clash); err != nil {
		return StatutoryRateSet{}, err
	}
	if clash {
		return StatutoryRateSet{}, errs.Conflict("effective_date_taken", "another active rate set starts on the same date; deactivate it first")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.statutory_rate_sets SET status = 'active', activated_by = $2, activated_at = now() WHERE id = $1`, rid,
		actor(ctx)); err != nil {
		return StatutoryRateSet{}, err
	}
	out, err := m.rateSet(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	property := handle.Property(ctx)
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "activate", EntityType: "hris.statutory_rate_set", EntityID: rid.String(),
		EntityLabel: out.Code, PropertyID: &property, Reason: req.Note, Before: map[string]any{"status": status}, After: map[string]any{"status": "active",
			"effectiveFrom": ymd(out.EffectiveFrom), "verification": out.VerificationStatus}})
}

func (m *Module) verifyRateHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req StatutoryRateDecision) (StatutoryRateSet, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return StatutoryRateSet{}, err
	}
	if _, _, err := lockRate(ctx, tx, rid); err != nil {
		return StatutoryRateSet{}, err
	}
	if strings.TrimSpace(req.Note) == "" {
		return StatutoryRateSet{}, handle.Invalid("note", "required", "name the consultant and the test cases reviewed")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.statutory_rate_sets SET verification_status = 'verified', verified_by = $2, verified_at = now(),
		verification_note = $3 WHERE id = $1`, rid, actor(ctx), strings.TrimSpace(req.Note)); err != nil {
		return StatutoryRateSet{}, err
	}
	out, err := m.rateSet(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	property := handle.Property(ctx)
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "verify", EntityType: "hris.statutory_rate_set", EntityID: rid.String(),
		EntityLabel: out.Code, PropertyID: &property, Reason: req.Note, After: map[string]any{"verificationStatus": "verified"}})
}

func (m *Module) deactivateRateHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req StatutoryRateDecision) (StatutoryRateSet, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return StatutoryRateSet{}, err
	}
	status, _, err := lockRate(ctx, tx, rid)
	if err != nil {
		return StatutoryRateSet{}, err
	}
	if status == "inactive" {
		return StatutoryRateSet{}, errs.Conflict("rate_set_inactive", "the rate set is already inactive")
	}
	if strings.TrimSpace(req.Note) == "" {
		return StatutoryRateSet{}, handle.Invalid("note", "required", "explain why the rate set is withdrawn")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.statutory_rate_sets SET status = 'inactive', updated_by = $2 WHERE id = $1`, rid, actor(ctx)); err != nil {
		return StatutoryRateSet{}, err
	}
	out, err := m.rateSet(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	property := handle.Property(ctx)
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "deactivate", EntityType: "hris.statutory_rate_set", EntityID: rid.String(),
		EntityLabel: out.Code, PropertyID: &property, Reason: req.Note, Before: map[string]any{"status": status}, After: map[string]any{"status": "inactive"}})
}

// statutoryRatesAt returns the rates in force on a day: the latest active
// rate set from that date, else the Payroll Configuration (ref nil).
func statutoryRatesAt(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time) (hris.StatutoryRates, *uuid.UUID, error) {
	var rid uuid.UUID
	var raw []byte
	err := q.QueryRow(ctx, `SELECT id, rates FROM hris.statutory_rate_sets WHERE status = 'active' AND effective_from <= $1::date
		ORDER BY effective_from DESC LIMIT 1`, ymd(day)).Scan(&rid, &raw)
	if err != nil && !dbtx.IsNoRows(err) {
		return hris.StatutoryRates{}, nil, err
	}
	if err == nil {
		r := hris.DefaultStatutoryRates() // missing keys keep the defaults
		if err := json.Unmarshal(raw, &r); err != nil {
			return r, nil, err
		}
		return r, &rid, nil
	}
	cfg, _, err := hris.LoadPayrollConfiguration(ctx, q, property, hris.PolicyTime(day))
	if err != nil {
		return hris.StatutoryRates{}, nil, err
	}
	return hris.StatutoryRatesFrom(cfg), nil, nil
}
