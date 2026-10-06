package tournament

// PRD P5 FR-TRN-P5-06 Advanced registration: every registration has a
// category — member (Active membership on the first day), guest, or
// sponsor invitation (a sponsor's guest) — with a quota per category (a
// full category waitlists, and the waitlist promotes only players whose
// category has room); fees can be limited to a category and carry an
// early-bird amount until a date (the registration date decides, so a
// waitlisted early registrant keeps the early-bird fee when promoted).

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

// RegistrationCategories of PRD P5 FR-TRN-P5-06.
var RegistrationCategories = []string{"member", "guest", "sponsor_invitation"}

func categoryLabel(c string) string {
	switch c {
	case "member":
		return "member"
	case "sponsor_invitation":
		return "sponsor invitation"
	}
	return "guest"
}

// registrationCategory returns the category of a new registration and
// whether its quota is used up.
func registrationCategory(ctx context.Context, tx pgx.Tx, tid uuid.UUID, playerType string, sponsor *uuid.UUID) (string, bool, error) {
	category := "guest"
	switch {
	case sponsor != nil:
		category = "sponsor_invitation"
	case playerType == "member":
		category = "member"
	}
	var full bool
	err := tx.QueryRow(ctx, `SELECT coalesce((SELECT c.quota IS NOT NULL AND (SELECT count(*) FROM golf.tournament_registrations r WHERE r.tournament_id = $1
		AND r.category = $2 AND r.status IN ('registered', 'checked_in')) >= c.quota FROM golf.tournament_registration_categories c
		WHERE c.tournament_id = $1 AND c.category = $2), false)`, tid, category).Scan(&full)
	return category, full, err
}

type feeExtra struct {
	ID              uuid.UUID `db:"id"`
	EarlyBirdAmount *string   `db:"early_bird_amount"`
	EarlyBirdUntil  *string   `db:"early_bird_until"`
	Category        *string   `db:"category"`
}

// advancedFees keeps the fees of the registration's category and applies
// the early-bird amounts (FR-TRN-P5-06).
func advancedFees(ctx context.Context, tx pgx.Tx, reg TournamentRegistration, list []TournamentFee) ([]TournamentFee, error) {
	if len(list) == 0 {
		return list, nil
	}
	ids := make([]uuid.UUID, 0, len(list))
	for _, f := range list {
		ids = append(ids, f.ID)
	}
	extras, err := handle.List[feeExtra](tx.Query(ctx, `SELECT id, trim_scale(early_bird_amount)::text AS early_bird_amount,
		to_char(early_bird_until, 'YYYY-MM-DD') AS early_bird_until, category FROM golf.tournament_fees WHERE id = ANY($1)`, ids))
	if err != nil {
		return nil, err
	}
	by := map[uuid.UUID]feeExtra{}
	for _, x := range extras {
		by[x.ID] = x
	}
	var category *string
	if err := tx.QueryRow(ctx, `SELECT category FROM golf.tournament_registrations WHERE id = $1`, reg.ID).Scan(&category); err != nil {
		return nil, err
	}
	regDate := reg.RegisteredAt.In(location(ctx, tx, reg.PropertyID)).Format("2006-01-02")
	early := false
	out := make([]TournamentFee, 0, len(list))
	for _, f := range list {
		x := by[f.ID]
		if x.Category != nil && (category == nil || *x.Category != *category) {
			continue
		}
		if x.EarlyBirdAmount != nil && x.EarlyBirdUntil != nil && regDate <= *x.EarlyBirdUntil {
			f.Amount = *x.EarlyBirdAmount
			early = true
		}
		out = append(out, f)
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_registrations SET early_bird = $2 WHERE id = $1`, reg.ID, early); err != nil {
		return nil, err
	}
	return out, nil
}

// ── API ───────────────────────────────────────────────────────────────────

// TournamentCategoryUse is a registration category with its quota and use.
type TournamentCategoryUse struct {
	Category   string `json:"category" enum:"member,guest,sponsor_invitation"`
	Quota      *int   `json:"quota" doc:"Null = no quota (field size only)"`
	Registered int    `json:"registered"`
	Waitlisted int    `json:"waitlisted"`
	Left       *int   `json:"left"`
}

// TournamentFeeEarlyBird is a fee with its category and early-bird amount.
type TournamentFeeEarlyBird struct {
	FeeID           uuid.UUID  `json:"feeId" db:"id"`
	Name            string     `json:"name" db:"name"`
	Component       string     `json:"component" db:"component"`
	PackageID       *uuid.UUID `json:"packageId" db:"package_id"`
	PlayerType      string     `json:"playerType" db:"player_type"`
	Amount          string     `json:"amount" db:"amount"`
	EarlyBirdAmount *string    `json:"earlyBirdAmount" db:"early_bird_amount"`
	EarlyBirdUntil  *string    `json:"earlyBirdUntil" db:"early_bird_until"`
	Category        *string    `json:"category" db:"category" enum:"member,guest,sponsor_invitation"`
	EarlyBirdOpen   bool       `json:"earlyBirdOpen" db:"-" doc:"Registrations today get the early-bird amount"`
}

// TournamentAdvancedRegistration is the advanced registration setup.
type TournamentAdvancedRegistration struct {
	TournamentID uuid.UUID                `json:"tournamentId"`
	FieldSize    int                      `json:"fieldSize"`
	Registered   int                      `json:"registered"`
	Categories   []TournamentCategoryUse  `json:"categories"`
	Fees         []TournamentFeeEarlyBird `json:"fees"`
	EarlyBirds   int                      `json:"earlyBirds" doc:"Registrations with the early-bird fee"`
}

// TournamentCategoryQuotaInput sets the quota of a category (null = none).
type TournamentCategoryQuotaInput struct {
	Category string `json:"category" enum:"member,guest,sponsor_invitation"`
	Quota    *int   `json:"quota"`
}

// TournamentFeeEarlyBirdInput sets the category and early-bird of a fee.
type TournamentFeeEarlyBirdInput struct {
	FeeID           uuid.UUID `json:"feeId"`
	Category        *string   `json:"category" enum:"member,guest,sponsor_invitation" doc:"Null = every category"`
	EarlyBirdAmount *string   `json:"earlyBirdAmount" doc:"Null = no early-bird"`
	EarlyBirdUntil  *string   `json:"earlyBirdUntil" doc:"Last registration date of the early-bird amount (YYYY-MM-DD)"`
}

// TournamentAdvancedRegistrationInput replaces the quotas and updates fees.
type TournamentAdvancedRegistrationInput struct {
	Quotas []TournamentCategoryQuotaInput `json:"quotas"`
	Fees   []TournamentFeeEarlyBirdInput  `json:"fees,omitempty"`
}

// AdvancedRegistration returns the categories, quotas and early-bird fees.
func (m *Module) AdvancedRegistration(ctx context.Context, q dbtx.Querier, property, tid uuid.UUID) (TournamentAdvancedRegistration, error) {
	t, err := tournamentAt(ctx, q, property, tid)
	if err != nil {
		return TournamentAdvancedRegistration{}, err
	}
	out := TournamentAdvancedRegistration{TournamentID: tid, FieldSize: t.FieldSize, Registered: t.Registered, Categories: []TournamentCategoryUse{}}
	for _, c := range RegistrationCategories {
		u := TournamentCategoryUse{Category: c}
		if err := q.QueryRow(ctx, `SELECT (SELECT quota FROM golf.tournament_registration_categories WHERE tournament_id = $1 AND category = $2),
			count(*) FILTER (WHERE status IN ('registered', 'checked_in'))::int, count(*) FILTER (WHERE status = 'waitlisted')::int
			FROM golf.tournament_registrations WHERE tournament_id = $1 AND category = $2`, tid, c).Scan(&u.Quota, &u.Registered, &u.Waitlisted); err != nil {
			return out, err
		}
		if u.Quota != nil {
			u.Left = ptr(max(*u.Quota-u.Registered, 0))
		}
		out.Categories = append(out.Categories, u)
	}
	if out.Fees, err = handle.List[TournamentFeeEarlyBird](q.Query(ctx, `SELECT id, name, component, package_id, player_type, trim_scale(amount)::text AS amount,
		trim_scale(early_bird_amount)::text AS early_bird_amount, to_char(early_bird_until, 'YYYY-MM-DD') AS early_bird_until, category
		FROM golf.tournament_fees WHERE tournament_id = $1 AND status = 'active' ORDER BY package_id NULLS FIRST, sequence, name`, tid)); err != nil {
		return out, err
	}
	today := now().In(location(ctx, q, property)).Format("2006-01-02")
	for i := range out.Fees {
		f := &out.Fees[i]
		f.EarlyBirdOpen = f.EarlyBirdAmount != nil && f.EarlyBirdUntil != nil && today <= *f.EarlyBirdUntil
	}
	err = q.QueryRow(ctx, `SELECT count(*)::int FROM golf.tournament_registrations WHERE tournament_id = $1 AND early_bird AND status <> 'withdrawn'`, tid).
		Scan(&out.EarlyBirds)
	return out, err
}

// SaveAdvancedRegistration sets the category quotas and the fee categories
// and early-bird amounts (before the tournament starts).
func (m *Module) SaveAdvancedRegistration(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, in TournamentAdvancedRegistrationInput) (TournamentAdvancedRegistration, error) {
	t, err := lockTournament(ctx, tx, property, tid)
	if err != nil {
		return TournamentAdvancedRegistration{}, err
	}
	if t.Status != "draft" && t.Status != "open" && t.Status != "closed" {
		return TournamentAdvancedRegistration{}, errs.Conflict("invalid_status", "registration categories change before the tournament starts, not when "+t.Status)
	}
	before, err := m.AdvancedRegistration(ctx, tx, property, tid)
	if err != nil {
		return before, err
	}
	seen := map[string]bool{}
	sum := 0
	for i, q := range in.Quotas {
		f := "quotas[" + itoa(i) + "]"
		if !slices.Contains(RegistrationCategories, q.Category) {
			return before, handle.Invalid(f+".category", "invalid", "member, guest or sponsor_invitation")
		}
		if seen[q.Category] {
			return before, handle.Invalid(f+".category", "duplicate", "once per category")
		}
		seen[q.Category] = true
		if q.Quota != nil {
			if *q.Quota < 0 || *q.Quota > t.FieldSize {
				return before, handle.Invalid(f+".quota", "invalid", "0 – the field size")
			}
			sum += *q.Quota
		}
	}
	_ = sum // quotas may overlap the field size (the field size stays the overall limit)
	if _, err := tx.Exec(ctx, `DELETE FROM golf.tournament_registration_categories WHERE tournament_id = $1`, tid); err != nil {
		return before, err
	}
	for _, q := range in.Quotas {
		if _, err := tx.Exec(ctx, `INSERT INTO golf.tournament_registration_categories (id, property_id, tournament_id, category, quota, updated_by)
			VALUES ($1,$2,$3,$4,$5,$6)`, id.New(), property, tid, q.Category, q.Quota, actor(ctx)); err != nil {
			return before, err
		}
	}
	for i, f := range in.Fees {
		fl := "fees[" + itoa(i) + "]"
		if f.Category != nil && !slices.Contains(RegistrationCategories, *f.Category) {
			return before, handle.Invalid(fl+".category", "invalid", "member, guest or sponsor_invitation")
		}
		var amount *string
		if f.EarlyBirdAmount != nil {
			if amount, err = decimalOrNil(fl+".earlyBirdAmount", *f.EarlyBirdAmount, 0, 1_000_000_000_000); err != nil {
				return before, err
			}
		}
		if (amount == nil) != (f.EarlyBirdUntil == nil || *f.EarlyBirdUntil == "") {
			return before, handle.Invalid(fl+".earlyBirdUntil", "required", "an early-bird needs its amount and its last date")
		}
		var until *string
		if f.EarlyBirdUntil != nil && *f.EarlyBirdUntil != "" {
			d, err := time.Parse("2006-01-02", *f.EarlyBirdUntil)
			if err != nil {
				return before, handle.Invalid(fl+".earlyBirdUntil", "invalid", "YYYY-MM-DD")
			}
			if d.Format("2006-01-02") > t.StartDate {
				return before, handle.Invalid(fl+".earlyBirdUntil", "invalid", "on or before the tournament start")
			}
			until = f.EarlyBirdUntil
		}
		tag, err := tx.Exec(ctx, `UPDATE golf.tournament_fees SET category = $3, early_bird_amount = $4::numeric, early_bird_until = $5::date, updated_by = $6
			WHERE id = $1 AND tournament_id = $2`, f.FeeID, tid, f.Category, amount, until, actor(ctx))
		if err != nil {
			return before, err
		}
		if tag.RowsAffected() == 0 {
			return before, handle.Invalid(fl+".feeId", "not_found", "fee of this tournament")
		}
	}
	after, err := m.AdvancedRegistration(ctx, tx, property, tid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.tournament", tid, t.Code+" registration categories", "update", property, before, after, "")
}

func (m *Module) registerAdvancedRegistration(reg *route.Registry) {
	db := m.DB
	m.add(reg, route.Route{Method: http.MethodGet, Path: base + "/{id}/registration-categories",
		Summary: "Registration categories (member, guest, sponsor invitation) with quotas and early-bird fees", Permission: "golf.tournament.view",
		Response: TournamentAdvancedRegistration{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (TournamentAdvancedRegistration, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentAdvancedRegistration{}, err
			}
			return m.AdvancedRegistration(ctx, tx, handle.Property(ctx), tid)
		})})
	m.add(reg, route.Route{Method: http.MethodPut, Path: base + "/{id}/registration-categories",
		Summary: "Set category quotas, fee categories and early-bird fees", Permission: "golf.tournament.manage", Request: TournamentAdvancedRegistrationInput{},
		Response: TournamentAdvancedRegistration{}, Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request,
			in TournamentAdvancedRegistrationInput) (TournamentAdvancedRegistration, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentAdvancedRegistration{}, err
			}
			return m.SaveAdvancedRegistration(ctx, tx, handle.Property(ctx), tid, in)
		})})
}
