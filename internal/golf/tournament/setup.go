package tournament

// FR-TRN-01 Tournament Creation, FR-TRN-02 Tournament Schedule (course
// blocked on the P1 tee sheet), FR-TRN-03 Tournament Packages & Tournament
// Fees, divisions, registration window and the tournament status
// (Draft → Open → Closed → In Progress → Completed, or Cancelled).

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/golf"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/platform/org"
)

var (
	codeRe = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,39}$`)
	code20 = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)
	hhmmRe = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
)

// ── views ─────────────────────────────────────────────────────────────────

// Tournament is the tournament header.
type Tournament struct {
	ID                   uuid.UUID  `json:"id" db:"id"`
	PropertyID           uuid.UUID  `json:"propertyId" db:"property_id"`
	Code                 string     `json:"code" db:"code"`
	Name                 string     `json:"name" db:"name"`
	Description          *string    `json:"description" db:"description"`
	TournamentType       string     `json:"tournamentType" db:"tournament_type" enum:"club,club_championship,corporate,invitational,sponsor,charity"`
	CourseID             uuid.UUID  `json:"courseId" db:"course_id"`
	CourseName           string     `json:"courseName" db:"course_name"`
	PlayingRouteID       uuid.UUID  `json:"playingRouteId" db:"playing_route_id"`
	PlayingRouteName     string     `json:"playingRouteName" db:"route_name"`
	StartDate            string     `json:"startDate" db:"start_date"`
	EndDate              string     `json:"endDate" db:"end_date"`
	Format               string     `json:"format" db:"format" enum:"stroke_play,stableford"`
	ScoringBasis         string     `json:"scoringBasis" db:"scoring_basis" enum:"gross,net,gross_and_net"`
	HandicapAllowance    *string    `json:"handicapAllowance" db:"handicap_allowance" doc:"Percent; null = Tournament Policies"`
	MaxHandicap          *string    `json:"maxHandicap" db:"max_handicap" doc:"Null = Tournament Policies"`
	Eligibility          string     `json:"eligibility" db:"eligibility" enum:"members,members_and_guests,invitation,open"`
	FieldSize            int        `json:"fieldSize" db:"field_size"`
	WaitlistEnabled      bool       `json:"waitlistEnabled" db:"waitlist_enabled"`
	PlayersPerFlight     int        `json:"playersPerFlight" db:"players_per_flight"`
	StartType            string     `json:"startType" db:"start_type" enum:"shotgun,tee_times"`
	RegistrationOpensAt  *time.Time `json:"registrationOpensAt" db:"registration_opens_at"`
	RegistrationClosesAt *time.Time `json:"registrationClosesAt" db:"registration_closes_at"`
	TieBreak             *string    `json:"tieBreak" db:"tie_break" enum:"countback,shared" doc:"Null = Tournament Policies"`
	CutAfterRound        *int       `json:"cutAfterRound" db:"cut_after_round"`
	CutTop               *int       `json:"cutTop" db:"cut_top" doc:"Top N and ties make the cut"`
	Public               bool       `json:"public" db:"public" doc:"Listed and open for registration on the website"`
	LeaderboardPublic    bool       `json:"leaderboardPublic" db:"leaderboard_public"`
	EventID              *uuid.UUID `json:"eventId" db:"event_id" doc:"Banquet & Event event (venue, catering)"`
	CustomerID           *uuid.UUID `json:"customerId" db:"customer_id"`
	CorporateAccountID   *uuid.UUID `json:"corporateAccountId" db:"corporate_account_id"`
	QuotationID          *uuid.UUID `json:"quotationId" db:"quotation_id"`
	QuotationNumber      *string    `json:"quotationNumber" db:"quotation_number"`
	Currency             string     `json:"currency" db:"currency"`
	Status               string     `json:"status" db:"status" enum:"draft,open,closed,in_progress,completed,cancelled"`
	CurrentRound         int        `json:"currentRound" db:"current_round"`
	RoundCount           int        `json:"roundCount" db:"round_count"`
	Registered           int        `json:"registered" db:"registered" doc:"Registered and checked-in players"`
	Waitlisted           int        `json:"waitlisted" db:"waitlisted"`
	CheckedIn            int        `json:"checkedIn" db:"checked_in"`
	FinalizedAt          *time.Time `json:"finalizedAt" db:"finalized_at"`
	CancelledAt          *time.Time `json:"cancelledAt" db:"cancelled_at"`
	CancelReason         *string    `json:"cancelReason" db:"cancel_reason"`
	Notes                *string    `json:"notes" db:"notes"`
	CreatedAt            time.Time  `json:"createdAt" db:"created_at"`
}

const tournamentSelect = `SELECT t.id, t.property_id, t.code, t.name, t.description, t.tournament_type, t.course_id, c.name AS course_name, t.playing_route_id,
	r.name AS route_name, t.start_date::text AS start_date, t.end_date::text AS end_date, t.format, t.scoring_basis,
	trim_scale(t.handicap_allowance)::text AS handicap_allowance, trim_scale(t.max_handicap)::text AS max_handicap, t.eligibility, t.field_size,
	t.waitlist_enabled, t.players_per_flight, t.start_type, t.registration_opens_at, t.registration_closes_at, t.tie_break, t.cut_after_round, t.cut_top,
	t.public, t.leaderboard_public, t.event_id, t.customer_id, t.corporate_account_id, t.quotation_id, t.quotation_number, t.currency, t.status,
	t.current_round, t.finalized_at, t.cancelled_at, t.cancel_reason, t.notes, t.created_at,
	(SELECT count(*) FROM golf.tournament_rounds x WHERE x.tournament_id = t.id)::int AS round_count,
	(SELECT count(*) FROM golf.tournament_registrations x WHERE x.tournament_id = t.id AND x.status IN ('registered', 'checked_in'))::int AS registered,
	(SELECT count(*) FROM golf.tournament_registrations x WHERE x.tournament_id = t.id AND x.status = 'waitlisted')::int AS waitlisted,
	(SELECT count(*) FROM golf.tournament_registrations x WHERE x.tournament_id = t.id AND x.status = 'checked_in')::int AS checked_in
	FROM golf.tournaments t JOIN golf.courses c ON c.id = t.course_id JOIN golf.playing_routes r ON r.id = t.playing_route_id`

// TournamentRound is one round (day) of a tournament.
type TournamentRound struct {
	ID                 uuid.UUID  `json:"id" db:"id"`
	RoundNo            int        `json:"roundNo" db:"round_no"`
	PlayDate           string     `json:"playDate" db:"play_date"`
	StartTime          string     `json:"startTime" db:"start_time" doc:"Shotgun time / first tee time (local HH:MM)"`
	TeeIntervalMinutes int        `json:"teeIntervalMinutes" db:"tee_interval_minutes"`
	StartTees          string     `json:"startTees" db:"start_tees" doc:"1, or 1,10 for a two-tee start"`
	PlayingRouteID     *uuid.UUID `json:"playingRouteId" db:"playing_route_id" doc:"Null = the tournament's route"`
	CourseBlockID      *uuid.UUID `json:"courseBlockId" db:"course_block_id" doc:"Tee sheet block of the round"`
	Status             string     `json:"status" db:"status" enum:"scheduled,drawn,published,in_progress,completed"`
	DrawMethod         *string    `json:"drawMethod" db:"draw_method"`
	DrawnAt            *time.Time `json:"drawnAt" db:"drawn_at"`
	PublishedAt        *time.Time `json:"publishedAt" db:"published_at"`
	StartedAt          *time.Time `json:"startedAt" db:"started_at"`
	CompletedAt        *time.Time `json:"completedAt" db:"completed_at"`
	Flights            int        `json:"flights" db:"flights"`
}

const roundSelect = `SELECT x.id, x.round_no, x.play_date::text AS play_date, x.start_time, x.tee_interval_minutes, x.start_tees, x.playing_route_id,
	x.course_block_id, x.status, x.draw_method, x.drawn_at, x.published_at, x.started_at, x.completed_at,
	(SELECT count(*) FROM golf.tournament_flights f WHERE f.round_id = x.id)::int AS flights FROM golf.tournament_rounds x`

// TournamentDivision is a division (flight A/B/C, ladies, senior …).
type TournamentDivision struct {
	ID                 uuid.UUID  `json:"id" db:"id"`
	Code               string     `json:"code" db:"code"`
	Name               string     `json:"name" db:"name"`
	Sequence           int        `json:"sequence" db:"sequence"`
	Gender             string     `json:"gender" db:"gender" enum:"male,female,any"`
	PlayerType         string     `json:"playerType" db:"player_type" enum:"member,guest,any"`
	HandicapMin        *string    `json:"handicapMin" db:"handicap_min"`
	HandicapMax        *string    `json:"handicapMax" db:"handicap_max"`
	AgeMin             *int       `json:"ageMin" db:"age_min"`
	TeeSetID           *uuid.UUID `json:"teeSetId" db:"tee_set_id"`
	HallOfFameDivision *string    `json:"hallOfFameDivision" db:"hall_of_fame_division" enum:"men,ladies,senior,junior,open"`
	Status             string     `json:"status" db:"status" enum:"active,inactive"`
	Players            int        `json:"players" db:"players"`
}

const divisionSelect = `SELECT d.id, d.code, d.name, d.sequence, d.gender, d.player_type, trim_scale(d.handicap_min)::text AS handicap_min,
	trim_scale(d.handicap_max)::text AS handicap_max, d.age_min, d.tee_set_id, d.hall_of_fame_division, d.status,
	(SELECT count(*) FROM golf.tournament_registrations x WHERE x.division_id = d.id AND x.status IN ('registered', 'checked_in'))::int AS players
	FROM golf.tournament_divisions d`

// TournamentPackage is a Tournament Package with its total per player type.
type TournamentPackage struct {
	ID          uuid.UUID `json:"id" db:"id"`
	Code        string    `json:"code" db:"code"`
	Name        string    `json:"name" db:"name"`
	Description *string   `json:"description" db:"description"`
	PlayerType  string    `json:"playerType" db:"player_type" enum:"member,guest,any"`
	IsDefault   bool      `json:"isDefault" db:"is_default"`
	Sequence    int       `json:"sequence" db:"sequence"`
	Status      string    `json:"status" db:"status" enum:"active,inactive"`
	MemberTotal string    `json:"memberTotal" db:"-" doc:"Entry fees + package fees for a member"`
	GuestTotal  string    `json:"guestTotal" db:"-" doc:"Entry fees + package fees for a guest"`
}

const packageSelect = `SELECT p.id, p.code, p.name, p.description, p.player_type, p.is_default, p.sequence, p.status FROM golf.tournament_packages p`

// TournamentFee is a fee component (Tournament Fees).
type TournamentFee struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	PackageID  *uuid.UUID `json:"packageId" db:"package_id" doc:"Null: charged on every registration of the player type"`
	Component  string     `json:"component" db:"component" enum:"entry_fee,green_fee,caddy_fee,cart_fee,dinner,goodie_bag,insurance,other"`
	Name       string     `json:"name" db:"name"`
	PlayerType string     `json:"playerType" db:"player_type" enum:"member,guest,any"`
	Amount     string     `json:"amount" db:"amount" doc:"Nett (tax & service inclusive)"`
	Currency   string     `json:"currency" db:"currency"`
	TaxCodes   []string   `json:"taxCodes" db:"tax_codes" doc:"Tax & Service rules included in the amount"`
	Liability  bool       `json:"liability" db:"liability" doc:"Held for a partner (caddy fee), not club revenue"`
	Sequence   int        `json:"sequence" db:"sequence"`
	Status     string     `json:"status" db:"status" enum:"active,inactive"`
}

const feeSelect = `SELECT f.id, f.package_id, f.component, f.name, f.player_type, trim_scale(f.amount)::text AS amount, f.currency, f.tax_codes, f.liability,
	f.sequence, f.status FROM golf.tournament_fees f`

// TournamentDetail is a tournament with its schedule and setup.
type TournamentDetail struct {
	Tournament
	Rounds        []TournamentRound    `json:"rounds"`
	Divisions     []TournamentDivision `json:"divisions"`
	Packages      []TournamentPackage  `json:"packages"`
	Fees          []TournamentFee      `json:"fees"`
	Sponsors      []TournamentSponsor  `json:"sponsors"`
	Prizes        []TournamentPrize    `json:"prizes"`
	Policy        TournamentPolicy     `json:"policy" doc:"Tournament Policies in force"`
	PolicyVersion int                  `json:"policyVersion"`
}

// GetTournament loads the header.
func GetTournament(ctx context.Context, q dbtx.Querier, tid uuid.UUID) (Tournament, error) {
	rows, err := q.Query(ctx, tournamentSelect+` WHERE t.id = $1`, tid)
	return handle.One[Tournament](rows, err, "tournament")
}

// tournamentAt loads a tournament of the property (404 for other properties).
func tournamentAt(ctx context.Context, q dbtx.Querier, property, tid uuid.UUID) (Tournament, error) {
	t, err := GetTournament(ctx, q, tid)
	if err == nil && t.PropertyID != property {
		return Tournament{}, errs.NotFound("tournament")
	}
	return t, err
}

// lockTournament locks the tournament row (registrations, draws and status
// changes are serialised per tournament).
func lockTournament(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID) (Tournament, error) {
	var p uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id FROM golf.tournaments WHERE id = $1 FOR UPDATE`, tid).Scan(&p); err != nil || p != property {
		if err == nil || dbtx.IsNoRows(err) {
			return Tournament{}, errs.NotFound("tournament")
		}
		return Tournament{}, err
	}
	return GetTournament(ctx, tx, tid)
}

func listRounds(ctx context.Context, q dbtx.Querier, tid uuid.UUID) ([]TournamentRound, error) {
	return handle.List[TournamentRound](q.Query(ctx, roundSelect+` WHERE x.tournament_id = $1 ORDER BY x.round_no`, tid))
}

func listDivisions(ctx context.Context, q dbtx.Querier, tid uuid.UUID) ([]TournamentDivision, error) {
	return handle.List[TournamentDivision](q.Query(ctx, divisionSelect+` WHERE d.tournament_id = $1 ORDER BY d.sequence, d.code`, tid))
}

func listFees(ctx context.Context, q dbtx.Querier, tid uuid.UUID) ([]TournamentFee, error) {
	return handle.List[TournamentFee](q.Query(ctx, feeSelect+` WHERE f.tournament_id = $1 ORDER BY f.package_id NULLS FIRST, f.sequence, f.name`, tid))
}

func listPackages(ctx context.Context, q dbtx.Querier, tid uuid.UUID, fees []TournamentFee) ([]TournamentPackage, error) {
	ps, err := handle.List[TournamentPackage](q.Query(ctx, packageSelect+` WHERE p.tournament_id = $1 ORDER BY p.sequence, p.code`, tid))
	if err != nil {
		return nil, err
	}
	for i := range ps {
		ps[i].MemberTotal = feeTotal(fees, &ps[i].ID, "member").String()
		ps[i].GuestTotal = feeTotal(fees, &ps[i].ID, "guest").String()
	}
	return ps, nil
}

// applicableFees are the active fees of a registration: fees without a
// package plus the fees of the chosen package, for the player type.
func applicableFees(fees []TournamentFee, pkg *uuid.UUID, playerType string) []TournamentFee {
	var out []TournamentFee
	for _, f := range fees {
		if f.Status != "active" || (f.PlayerType != "any" && f.PlayerType != playerType) {
			continue
		}
		if f.PackageID != nil && (pkg == nil || *f.PackageID != *pkg) {
			continue
		}
		out = append(out, f)
	}
	return out
}

func feeTotal(fees []TournamentFee, pkg *uuid.UUID, playerType string) decimal.Decimal {
	t := decimal.Zero
	for _, f := range applicableFees(fees, pkg, playerType) {
		t = t.Add(dec(f.Amount))
	}
	return t
}

// Detail loads a tournament with its setup.
func (m *Module) Detail(ctx context.Context, q dbtx.Querier, property, tid uuid.UUID) (TournamentDetail, error) {
	t, err := tournamentAt(ctx, q, property, tid)
	if err != nil {
		return TournamentDetail{}, err
	}
	d := TournamentDetail{Tournament: t}
	if d.Rounds, err = listRounds(ctx, q, tid); err != nil {
		return d, err
	}
	if d.Divisions, err = listDivisions(ctx, q, tid); err != nil {
		return d, err
	}
	if d.Fees, err = listFees(ctx, q, tid); err != nil {
		return d, err
	}
	if d.Packages, err = listPackages(ctx, q, tid, d.Fees); err != nil {
		return d, err
	}
	if d.Sponsors, err = listSponsors(ctx, q, tid); err != nil {
		return d, err
	}
	if d.Prizes, err = listPrizes(ctx, q, tid); err != nil {
		return d, err
	}
	pol, ref, err := LoadPolicy(ctx, q, property)
	d.Policy, d.PolicyVersion = pol, ref.Version
	return d, err
}

// ── setup inputs ──────────────────────────────────────────────────────────

// RoundInput schedules one round.
type TournamentRoundInput struct {
	PlayDate           string     `json:"playDate" doc:"YYYY-MM-DD"`
	StartTime          string     `json:"startTime" doc:"HH:MM shotgun time or first tee time (local)"`
	TeeIntervalMinutes int        `json:"teeIntervalMinutes,omitempty" doc:"Tee times: minutes between flights (default 10)"`
	StartTees          string     `json:"startTees,omitempty" doc:"Tee times: 1 (default) or 1,10 (two-tee start)"`
	PlayingRouteID     *uuid.UUID `json:"playingRouteId,omitempty" doc:"Default: the tournament's playing route"`
}

// TournamentInput creates a tournament (FR-TRN-01).
type TournamentInput struct {
	Code                 string                 `json:"code,omitempty" doc:"Default: generated (TRN-YYYYMMDD-NNNN)"`
	Name                 string                 `json:"name"`
	Description          string                 `json:"description,omitempty"`
	TournamentType       string                 `json:"tournamentType,omitempty" enum:"club,club_championship,corporate,invitational,sponsor,charity"`
	CourseID             uuid.UUID              `json:"courseId"`
	PlayingRouteID       *uuid.UUID             `json:"playingRouteId,omitempty" doc:"Default: the default playing route of the course"`
	Format               string                 `json:"format" enum:"stroke_play,stableford"`
	ScoringBasis         string                 `json:"scoringBasis,omitempty" enum:"gross,net,gross_and_net"`
	HandicapAllowance    string                 `json:"handicapAllowance,omitempty" doc:"Percent; default Tournament Policies"`
	MaxHandicap          string                 `json:"maxHandicap,omitempty" doc:"Default Tournament Policies"`
	Eligibility          string                 `json:"eligibility,omitempty" enum:"members,members_and_guests,invitation,open"`
	FieldSize            int                    `json:"fieldSize"`
	WaitlistEnabled      *bool                  `json:"waitlistEnabled,omitempty"`
	PlayersPerFlight     int                    `json:"playersPerFlight,omitempty" doc:"Default 4"`
	StartType            string                 `json:"startType,omitempty" enum:"shotgun,tee_times"`
	RegistrationOpensAt  *time.Time             `json:"registrationOpensAt,omitempty"`
	RegistrationClosesAt *time.Time             `json:"registrationClosesAt,omitempty" doc:"Default: Tournament Policies registration deadline"`
	TieBreak             string                 `json:"tieBreak,omitempty" enum:"countback,shared"`
	CutAfterRound        *int                   `json:"cutAfterRound,omitempty"`
	CutTop               *int                   `json:"cutTop,omitempty"`
	Public               bool                   `json:"public,omitempty"`
	LeaderboardPublic    *bool                  `json:"leaderboardPublic,omitempty"`
	EventID              *uuid.UUID             `json:"eventId,omitempty" doc:"Banquet & Event event (venue, catering)"`
	CustomerID           *uuid.UUID             `json:"customerId,omitempty"`
	CorporateAccountID   *uuid.UUID             `json:"corporateAccountId,omitempty"`
	Rounds               []TournamentRoundInput `json:"rounds"`
	Notes                string                 `json:"notes,omitempty"`
}

// TournamentPatch changes a tournament; omitted fields stay.
type TournamentPatch struct {
	Name                 *string                `json:"name,omitempty"`
	Description          *string                `json:"description,omitempty"`
	TournamentType       *string                `json:"tournamentType,omitempty" enum:"club,club_championship,corporate,invitational,sponsor,charity"`
	PlayingRouteID       *uuid.UUID             `json:"playingRouteId,omitempty"`
	Format               *string                `json:"format,omitempty" enum:"stroke_play,stableford"`
	ScoringBasis         *string                `json:"scoringBasis,omitempty" enum:"gross,net,gross_and_net"`
	HandicapAllowance    *string                `json:"handicapAllowance,omitempty"`
	MaxHandicap          *string                `json:"maxHandicap,omitempty"`
	Eligibility          *string                `json:"eligibility,omitempty" enum:"members,members_and_guests,invitation,open"`
	FieldSize            *int                   `json:"fieldSize,omitempty"`
	WaitlistEnabled      *bool                  `json:"waitlistEnabled,omitempty"`
	PlayersPerFlight     *int                   `json:"playersPerFlight,omitempty"`
	StartType            *string                `json:"startType,omitempty" enum:"shotgun,tee_times"`
	RegistrationOpensAt  *time.Time             `json:"registrationOpensAt,omitempty"`
	RegistrationClosesAt *time.Time             `json:"registrationClosesAt,omitempty"`
	TieBreak             *string                `json:"tieBreak,omitempty" enum:"countback,shared"`
	CutAfterRound        *int                   `json:"cutAfterRound,omitempty"`
	CutTop               *int                   `json:"cutTop,omitempty"`
	Public               *bool                  `json:"public,omitempty"`
	LeaderboardPublic    *bool                  `json:"leaderboardPublic,omitempty"`
	EventID              *uuid.UUID             `json:"eventId,omitempty"`
	CustomerID           *uuid.UUID             `json:"customerId,omitempty"`
	CorporateAccountID   *uuid.UUID             `json:"corporateAccountId,omitempty"`
	Rounds               []TournamentRoundInput `json:"rounds,omitempty" doc:"Replaces the schedule (draft / open tournaments without a draw)"`
	Notes                *string                `json:"notes,omitempty"`
}

// setup is the editable state of a tournament.
type setup struct {
	Name, Type, Format, Basis, Eligibility, StartType string
	Description, Allowance, MaxHandicap, TieBreak     *string
	Notes                                             *string
	CourseID, RouteID                                 uuid.UUID
	FieldSize, PPF                                    int
	Waitlist, Public, LeaderboardPublic               bool
	OpensAt, ClosesAt                                 *time.Time
	CutAfter, CutTop                                  *int
	EventID, CustomerID, CorporateID                  *uuid.UUID
}

func oneOf(field, v string, allowed ...string) error {
	if !slices.Contains(allowed, v) {
		return handle.Invalid(field, "invalid", field+" must be one of "+strings.Join(allowed, ", "))
	}
	return nil
}

// percentOrNil validates an optional decimal within [lo, hi].
func decimalOrNil(field, v string, lo, hi int64) (*string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	d, err := decimal.NewFromString(v)
	if err != nil || d.LessThan(decimal.NewFromInt(lo)) || d.GreaterThan(decimal.NewFromInt(hi)) {
		return nil, handle.Invalid(field, "invalid", fmt.Sprintf("%s must be a number between %d and %d", field, lo, hi))
	}
	s := d.String()
	return &s, nil
}

func (s *setup) validate(ctx context.Context, q dbtx.Querier, property uuid.UUID, rounds int) error {
	if strings.TrimSpace(s.Name) == "" {
		return handle.Invalid("name", "required", "name is required")
	}
	for _, c := range []struct {
		f, v string
		ok   []string
	}{{"tournamentType", s.Type, []string{"club", "club_championship", "corporate", "invitational", "sponsor", "charity"}},
		{"format", s.Format, []string{"stroke_play", "stableford"}}, {"scoringBasis", s.Basis, []string{"gross", "net", "gross_and_net"}},
		{"eligibility", s.Eligibility, []string{"members", "members_and_guests", "invitation", "open"}}, {"startType", s.StartType, []string{"shotgun", "tee_times"}}} {
		if err := oneOf(c.f, c.v, c.ok...); err != nil {
			return err
		}
	}
	if s.TieBreak != nil {
		if err := oneOf("tieBreak", *s.TieBreak, "countback", "shared"); err != nil {
			return err
		}
	}
	if s.FieldSize < 1 || s.FieldSize > 288 {
		return handle.Invalid("fieldSize", "invalid", "field size must be between 1 and 288 players")
	}
	if s.PPF < 1 || s.PPF > 5 {
		return handle.Invalid("playersPerFlight", "invalid", "1–5 players per flight")
	}
	if s.CutAfter != nil || s.CutTop != nil {
		if s.CutAfter == nil || s.CutTop == nil || *s.CutAfter < 1 || *s.CutAfter >= rounds || *s.CutTop < 1 {
			return handle.Invalid("cutAfterRound", "invalid", "a cut needs cutAfterRound (before the last round) and cutTop")
		}
	}
	if s.OpensAt != nil && s.ClosesAt != nil && !s.ClosesAt.After(*s.OpensAt) {
		return handle.Invalid("registrationClosesAt", "invalid", "registration must close after it opens")
	}
	var ok bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.courses WHERE id = $1 AND property_id = $2 AND status = 'active' AND archived_at IS NULL)`,
		s.CourseID, property).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return handle.Invalid("courseId", "not_found", "active course of this property")
	}
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.playing_routes WHERE id = $1 AND course_id = $2 AND status = 'active')`,
		s.RouteID, s.CourseID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return handle.Invalid("playingRouteId", "not_found", "active playing route of the course")
	}
	return nil
}

// defaultRoute is the default (else first) active playing route of a course.
func defaultRoute(ctx context.Context, q dbtx.Querier, course uuid.UUID) (uuid.UUID, error) {
	var rid uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM golf.playing_routes WHERE course_id = $1 AND status = 'active' AND archived_at IS NULL
		ORDER BY is_default DESC, hole_count DESC, code LIMIT 1`, course).Scan(&rid)
	if dbtx.IsNoRows(err) {
		return rid, handle.Invalid("playingRouteId", "required", "the course has no active playing route")
	}
	return rid, err
}

type roundRow struct {
	No       int
	Date     time.Time
	Start    string
	Interval int
	Tees     string
	Route    *uuid.UUID
}

func validateRounds(ctx context.Context, q dbtx.Querier, course uuid.UUID, startType string, in []TournamentRoundInput) ([]roundRow, error) {
	if len(in) == 0 {
		return nil, handle.Invalid("rounds", "required", "at least one round is required")
	}
	if len(in) > 8 {
		return nil, handle.Invalid("rounds", "too_many", "at most 8 rounds")
	}
	out := make([]roundRow, 0, len(in))
	for i, r := range in {
		f := fmt.Sprintf("rounds[%d]", i)
		d, err := time.Parse("2006-01-02", r.PlayDate)
		if err != nil {
			return nil, handle.Invalid(f+".playDate", "invalid", "playDate must be YYYY-MM-DD")
		}
		if !hhmmRe.MatchString(r.StartTime) {
			return nil, handle.Invalid(f+".startTime", "invalid", "startTime must be HH:MM")
		}
		iv := r.TeeIntervalMinutes
		if iv == 0 {
			iv = 10
		}
		if iv < 4 || iv > 30 {
			return nil, handle.Invalid(f+".teeIntervalMinutes", "invalid", "4–30 minutes")
		}
		tees := r.StartTees
		if tees == "" {
			tees = "1"
		}
		if tees != "1" && tees != "1,10" {
			return nil, handle.Invalid(f+".startTees", "invalid", "1 or 1,10")
		}
		if tees == "1,10" && startType != "tee_times" {
			return nil, handle.Invalid(f+".startTees", "invalid", "a two-tee start applies to tee times, not a shotgun")
		}
		if r.PlayingRouteID != nil {
			var ok bool
			if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.playing_routes WHERE id = $1 AND course_id = $2 AND status = 'active')`,
				*r.PlayingRouteID, course).Scan(&ok); err != nil {
				return nil, err
			}
			if !ok {
				return nil, handle.Invalid(f+".playingRouteId", "not_found", "active playing route of the course")
			}
		}
		if i > 0 && !d.After(out[i-1].Date) && (!d.Equal(out[i-1].Date) || r.StartTime <= out[i-1].Start) {
			return nil, handle.Invalid(f+".playDate", "invalid", "rounds must be in chronological order")
		}
		out = append(out, roundRow{No: i + 1, Date: d, Start: r.StartTime, Interval: iv, Tees: tees, Route: r.PlayingRouteID})
	}
	return out, nil
}

func insertRounds(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, rounds []roundRow) error {
	for _, r := range rounds {
		if _, err := tx.Exec(ctx, `INSERT INTO golf.tournament_rounds (id, property_id, tournament_id, round_no, play_date, start_time, tee_interval_minutes,
			start_tees, playing_route_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id.New(), property, tid, r.No, r.Date.Format("2006-01-02"), r.Start, r.Interval,
			r.Tees, r.Route); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE golf.tournaments SET start_date = $2, end_date = $3 WHERE id = $1`, tid, rounds[0].Date.Format("2006-01-02"),
		rounds[len(rounds)-1].Date.Format("2006-01-02"))
	return err
}

// Create creates a draft tournament.
func (m *Module) Create(ctx context.Context, tx pgx.Tx, property uuid.UUID, in TournamentInput) (TournamentDetail, error) {
	pol, ref, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return TournamentDetail{}, err
	}
	s := setup{Name: strings.TrimSpace(in.Name), Description: nullStr(in.Description), Type: in.TournamentType, Format: in.Format, Basis: in.ScoringBasis,
		Eligibility: in.Eligibility, StartType: in.StartType, CourseID: in.CourseID, FieldSize: in.FieldSize, PPF: in.PlayersPerFlight, Waitlist: true,
		Public: in.Public, LeaderboardPublic: true, OpensAt: in.RegistrationOpensAt, ClosesAt: in.RegistrationClosesAt, CutAfter: in.CutAfterRound,
		CutTop: in.CutTop, EventID: in.EventID, CustomerID: in.CustomerID, CorporateID: in.CorporateAccountID, Notes: nullStr(in.Notes), TieBreak: nullStr(in.TieBreak)}
	if s.Type == "" {
		s.Type = "club"
	}
	if s.Basis == "" {
		s.Basis = "gross_and_net"
	}
	if s.Eligibility == "" {
		s.Eligibility = "members_and_guests"
	}
	if s.StartType == "" {
		s.StartType = "shotgun"
	}
	if s.PPF == 0 {
		s.PPF = 4
	}
	if in.WaitlistEnabled != nil {
		s.Waitlist = *in.WaitlistEnabled
	}
	if in.LeaderboardPublic != nil {
		s.LeaderboardPublic = *in.LeaderboardPublic
	}
	if s.Allowance, err = decimalOrNil("handicapAllowance", in.HandicapAllowance, 0, 100); err != nil {
		return TournamentDetail{}, err
	}
	if s.MaxHandicap, err = decimalOrNil("maxHandicap", in.MaxHandicap, 0, 54); err != nil {
		return TournamentDetail{}, err
	}
	if in.PlayingRouteID != nil {
		s.RouteID = *in.PlayingRouteID
	} else if in.CourseID != uuid.Nil {
		if s.RouteID, err = defaultRoute(ctx, tx, in.CourseID); err != nil {
			return TournamentDetail{}, err
		}
	}
	rounds, err := validateRounds(ctx, tx, in.CourseID, s.StartType, in.Rounds)
	if err != nil {
		return TournamentDetail{}, err
	}
	if err := s.validate(ctx, tx, property, len(rounds)); err != nil {
		return TournamentDetail{}, err
	}
	loc := location(ctx, tx, property)
	code := strings.ToUpper(strings.TrimSpace(in.Code))
	if code == "" {
		if code, err = numbering.Next(ctx, tx, property, "TRN", now().In(loc)); err != nil {
			return TournamentDetail{}, err
		}
	}
	if !codeRe.MatchString(code) {
		return TournamentDetail{}, handle.Invalid("code", "invalid", "1–40 characters: A–Z, 0–9, - or _")
	}
	cur, err := org.Currency(ctx, tx)
	if err != nil {
		return TournamentDetail{}, err
	}
	_ = pol
	tid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO golf.tournaments (id, property_id, code, name, description, tournament_type, course_id, playing_route_id, start_date,
		end_date, format, scoring_basis, handicap_allowance, max_handicap, eligibility, field_size, waitlist_enabled, players_per_flight, start_type,
		registration_opens_at, registration_closes_at, tie_break, cut_after_round, cut_top, public, leaderboard_public, event_id, customer_id,
		corporate_account_id, currency, policy_versions, notes, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9,$10,$11,$12::numeric,$13::numeric,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$32)`,
		tid, property, code, s.Name, s.Description, s.Type, s.CourseID, s.RouteID, rounds[0].Date.Format("2006-01-02"), s.Format, s.Basis, s.Allowance,
		s.MaxHandicap, s.Eligibility, s.FieldSize, s.Waitlist, s.PPF, s.StartType, s.OpensAt, s.ClosesAt, s.TieBreak, s.CutAfter, s.CutTop, s.Public,
		s.LeaderboardPublic, s.EventID, s.CustomerID, s.CorporateID, cur, jsonOf(map[string]int{PolicyCode: ref.Version}), s.Notes, actor(ctx)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return TournamentDetail{}, errs.Conflict("code_taken", "a tournament with this code exists")
		}
		if dbtx.IsForeignKeyViolation(err) {
			return TournamentDetail{}, handle.Invalid("customerId", "not_found", "customer or corporate account not found")
		}
		return TournamentDetail{}, err
	}
	if err := insertRounds(ctx, tx, property, tid, rounds); err != nil {
		return TournamentDetail{}, err
	}
	d, err := m.Detail(ctx, tx, property, tid)
	if err != nil {
		return d, err
	}
	if err := record(ctx, tx, "golf.tournament", tid, code+" · "+s.Name, audit.ActionCreate, property, nil, d.Tournament, ""); err != nil {
		return d, err
	}
	return d, live(ctx, tx, property, tid, "tournament", nil)
}

// Update applies a patch. Scoring rules are frozen once the tournament has
// started; the schedule changes only before the first draw.
func (m *Module) Update(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, in TournamentPatch) (TournamentDetail, error) {
	t, err := lockTournament(ctx, tx, property, tid)
	if err != nil {
		return TournamentDetail{}, err
	}
	if t.Status == "cancelled" || t.Status == "completed" {
		return TournamentDetail{}, errs.Conflict("tournament_closed", "a "+t.Status+" tournament cannot be changed")
	}
	started := t.Status == "in_progress"
	if started && (in.Format != nil || in.ScoringBasis != nil || in.HandicapAllowance != nil || in.MaxHandicap != nil || in.PlayingRouteID != nil ||
		in.StartType != nil || in.Rounds != nil || in.CutAfterRound != nil || in.CutTop != nil || in.TieBreak != nil || in.PlayersPerFlight != nil) {
		return TournamentDetail{}, errs.Conflict("tournament_started", "format, handicap, schedule and draw settings are frozen once the tournament started")
	}
	s := setup{Name: t.Name, Description: t.Description, Type: t.TournamentType, Format: t.Format, Basis: t.ScoringBasis, Eligibility: t.Eligibility,
		StartType: t.StartType, CourseID: t.CourseID, RouteID: t.PlayingRouteID, FieldSize: t.FieldSize, PPF: t.PlayersPerFlight, Waitlist: t.WaitlistEnabled,
		Public: t.Public, LeaderboardPublic: t.LeaderboardPublic, OpensAt: t.RegistrationOpensAt, ClosesAt: t.RegistrationClosesAt, CutAfter: t.CutAfterRound,
		CutTop: t.CutTop, EventID: t.EventID, CustomerID: t.CustomerID, CorporateID: t.CorporateAccountID, Notes: t.Notes, TieBreak: t.TieBreak,
		Allowance: t.HandicapAllowance, MaxHandicap: t.MaxHandicap}
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	set(&s.Name, in.Name)
	set(&s.Type, in.TournamentType)
	set(&s.Format, in.Format)
	set(&s.Basis, in.ScoringBasis)
	set(&s.Eligibility, in.Eligibility)
	set(&s.StartType, in.StartType)
	if in.Description != nil {
		s.Description = nullStr(*in.Description)
	}
	if in.Notes != nil {
		s.Notes = nullStr(*in.Notes)
	}
	if in.TieBreak != nil {
		s.TieBreak = nullStr(*in.TieBreak)
	}
	if in.HandicapAllowance != nil {
		if s.Allowance, err = decimalOrNil("handicapAllowance", *in.HandicapAllowance, 0, 100); err != nil {
			return TournamentDetail{}, err
		}
	}
	if in.MaxHandicap != nil {
		if s.MaxHandicap, err = decimalOrNil("maxHandicap", *in.MaxHandicap, 0, 54); err != nil {
			return TournamentDetail{}, err
		}
	}
	if in.PlayingRouteID != nil {
		s.RouteID = *in.PlayingRouteID
	}
	if in.FieldSize != nil {
		s.FieldSize = *in.FieldSize
	}
	if in.PlayersPerFlight != nil {
		s.PPF = *in.PlayersPerFlight
	}
	for _, b := range []struct {
		dst *bool
		v   *bool
	}{{&s.Waitlist, in.WaitlistEnabled}, {&s.Public, in.Public}, {&s.LeaderboardPublic, in.LeaderboardPublic}} {
		if b.v != nil {
			*b.dst = *b.v
		}
	}
	if in.RegistrationOpensAt != nil {
		s.OpensAt = in.RegistrationOpensAt
	}
	if in.RegistrationClosesAt != nil {
		s.ClosesAt = in.RegistrationClosesAt
	}
	if in.CutAfterRound != nil {
		s.CutAfter = in.CutAfterRound
	}
	if in.CutTop != nil {
		s.CutTop = in.CutTop
	}
	if in.EventID != nil {
		s.EventID = in.EventID
	}
	if in.CustomerID != nil {
		s.CustomerID = in.CustomerID
	}
	if in.CorporateAccountID != nil {
		s.CorporateID = in.CorporateAccountID
	}
	roundCount := t.RoundCount
	var rounds []roundRow
	if in.Rounds != nil {
		var drawn bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.tournament_rounds WHERE tournament_id = $1 AND status <> 'scheduled')`, tid).Scan(&drawn); err != nil {
			return TournamentDetail{}, err
		}
		if drawn || (t.Status != "draft" && t.Status != "open" && t.Status != "closed") {
			return TournamentDetail{}, errs.Conflict("schedule_frozen", "the schedule changes only before the first draw")
		}
		if rounds, err = validateRounds(ctx, tx, s.CourseID, s.StartType, in.Rounds); err != nil {
			return TournamentDetail{}, err
		}
		roundCount = len(rounds)
	}
	if err := s.validate(ctx, tx, property, roundCount); err != nil {
		return TournamentDetail{}, err
	}
	if s.FieldSize < t.Registered {
		return TournamentDetail{}, handle.Invalid("fieldSize", "too_small", fmt.Sprintf("%d players are already registered", t.Registered))
	}
	before := t
	if _, err := tx.Exec(ctx, `UPDATE golf.tournaments SET name = $2, description = $3, tournament_type = $4, playing_route_id = $5, format = $6,
		scoring_basis = $7, handicap_allowance = $8::numeric, max_handicap = $9::numeric, eligibility = $10, field_size = $11, waitlist_enabled = $12,
		players_per_flight = $13, start_type = $14, registration_opens_at = $15, registration_closes_at = $16, tie_break = $17, cut_after_round = $18,
		cut_top = $19, public = $20, leaderboard_public = $21, event_id = $22, customer_id = $23, corporate_account_id = $24, notes = $25, updated_by = $26
		WHERE id = $1`, tid, s.Name, s.Description, s.Type, s.RouteID, s.Format, s.Basis, s.Allowance, s.MaxHandicap, s.Eligibility, s.FieldSize, s.Waitlist,
		s.PPF, s.StartType, s.OpensAt, s.ClosesAt, s.TieBreak, s.CutAfter, s.CutTop, s.Public, s.LeaderboardPublic, s.EventID, s.CustomerID, s.CorporateID,
		s.Notes, actor(ctx)); err != nil {
		if dbtx.IsForeignKeyViolation(err) {
			return TournamentDetail{}, handle.Invalid("customerId", "not_found", "customer or corporate account not found")
		}
		return TournamentDetail{}, err
	}
	if rounds != nil {
		old, err := listRounds(ctx, tx, tid)
		if err != nil {
			return TournamentDetail{}, err
		}
		for _, r := range old {
			if r.CourseBlockID != nil && m.Golf != nil {
				if err := m.Golf.TournamentUnblock(ctx, tx, property, *r.CourseBlockID); err != nil {
					return TournamentDetail{}, err
				}
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM golf.tournament_rounds WHERE tournament_id = $1`, tid); err != nil {
			return TournamentDetail{}, err
		}
		if err := insertRounds(ctx, tx, property, tid, rounds); err != nil {
			return TournamentDetail{}, err
		}
		if t.Status == "open" || t.Status == "closed" {
			if err := m.blockRounds(ctx, tx, property, tid); err != nil {
				return TournamentDetail{}, err
			}
		}
	}
	d, err := m.Detail(ctx, tx, property, tid)
	if err != nil {
		return d, err
	}
	if err := record(ctx, tx, "golf.tournament", tid, d.Code+" · "+d.Name, audit.ActionUpdate, property, before, d.Tournament, ""); err != nil {
		return d, err
	}
	return d, live(ctx, tx, property, tid, "tournament", nil)
}

// roundWindow is the course block of a round: from the lead time before
// the first start to the round duration after the last start.
func roundWindow(t Tournament, r TournamentRound, pol TournamentPolicy, loc *time.Location, flights int) (time.Time, time.Time) {
	day, _ := time.Parse("2006-01-02", r.PlayDate)
	start := atLocal(day, r.StartTime, loc)
	last := start
	if t.StartType == "tee_times" {
		if flights == 0 {
			flights = (t.FieldSize + t.PlayersPerFlight - 1) / t.PlayersPerFlight
		}
		slots := flights
		if r.StartTees == "1,10" {
			slots = (flights + 1) / 2
		}
		if slots > 1 {
			last = start.Add(time.Duration((slots-1)*r.TeeIntervalMinutes) * time.Minute)
		}
	}
	return start.Add(-time.Duration(pol.BlockLeadMinutes) * time.Minute), last.Add(time.Duration(pol.RoundDurationMinutes) * time.Minute)
}

// blockRounds closes the course on the tee sheet for every round without a
// block (FR-TRN-02).
func (m *Module) blockRounds(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID) error {
	if m.Golf == nil {
		return nil
	}
	t, err := GetTournament(ctx, tx, tid)
	if err != nil {
		return err
	}
	pol, _, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return err
	}
	rounds, err := listRounds(ctx, tx, tid)
	if err != nil {
		return err
	}
	loc := location(ctx, tx, property)
	for _, r := range rounds {
		if r.CourseBlockID != nil {
			continue
		}
		if err := m.blockRound(ctx, tx, property, t, r, pol, loc); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) blockRound(ctx context.Context, tx pgx.Tx, property uuid.UUID, t Tournament, r TournamentRound, pol TournamentPolicy, loc *time.Location) error {
	from, to := roundWindow(t, r, pol, loc, r.Flights)
	b, err := m.Golf.TournamentBlock(ctx, tx, property, golf.TournamentBlockInput{CourseID: t.CourseID, StartsAt: from, EndsAt: to,
		Notes: fmt.Sprintf("Tournament %s · %s · round %d (%s %s)", t.Code, t.Name, r.RoundNo, strings.ReplaceAll(t.StartType, "_", " "), r.StartTime)})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE golf.tournament_rounds SET course_block_id = $2 WHERE id = $1`, r.ID, b.ID)
	return err
}

// releaseBlocks re-opens the tee sheet of rounds not played (cancellation).
func (m *Module) releaseBlocks(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID) error {
	rounds, err := listRounds(ctx, tx, tid)
	if err != nil {
		return err
	}
	for _, r := range rounds {
		if r.CourseBlockID == nil || m.Golf == nil || r.Status == "completed" {
			continue
		}
		if err := m.Golf.TournamentUnblock(ctx, tx, property, *r.CourseBlockID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.tournament_rounds SET course_block_id = NULL WHERE id = $1`, r.ID); err != nil {
			return err
		}
	}
	return nil
}

// OpenRegistrationInput opens registration.
type TournamentOpenRegistrationInput struct {
	ClosesAt *time.Time `json:"closesAt,omitempty" doc:"Default: the tournament's close, else the Tournament Policies deadline"`
}

// OpenRegistration opens the registration window (Draft / Closed → Open)
// and closes the course on the tee sheet for the rounds.
func (m *Module) OpenRegistration(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, in TournamentOpenRegistrationInput) (TournamentDetail, error) {
	t, err := lockTournament(ctx, tx, property, tid)
	if err != nil {
		return TournamentDetail{}, err
	}
	if t.Status != "draft" && t.Status != "closed" {
		return TournamentDetail{}, errs.Conflict("invalid_status", "registration opens from Draft or Closed, not "+t.Status)
	}
	pol, _, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return TournamentDetail{}, err
	}
	rounds, err := listRounds(ctx, tx, tid)
	if err != nil {
		return TournamentDetail{}, err
	}
	loc := location(ctx, tx, property)
	day, _ := time.Parse("2006-01-02", rounds[0].PlayDate)
	firstStart := atLocal(day, rounds[0].StartTime, loc)
	if !firstStart.After(now()) {
		return TournamentDetail{}, errs.Conflict("tournament_past", "the first round has already started")
	}
	opens := now()
	if t.RegistrationOpensAt != nil && t.Status == "draft" {
		opens = *t.RegistrationOpensAt
	}
	closes := in.ClosesAt
	if closes == nil {
		closes = t.RegistrationClosesAt
	}
	if closes == nil || (t.Status == "closed" && !closes.After(now())) {
		c := firstStart.AddDate(0, 0, -pol.RegistrationDeadlineDays)
		if !c.After(now()) {
			c = firstStart.Add(-time.Hour)
		}
		closes = &c
	}
	if !closes.After(now()) || closes.After(firstStart) {
		return TournamentDetail{}, handle.Invalid("closesAt", "invalid", "registration must close in the future and before the first start")
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournaments SET status = 'open', registration_opens_at = $2, registration_closes_at = $3, updated_by = $4 WHERE id = $1`,
		tid, opens, *closes, actor(ctx)); err != nil {
		return TournamentDetail{}, err
	}
	if err := m.blockRounds(ctx, tx, property, tid); err != nil {
		return TournamentDetail{}, err
	}
	d, err := m.Detail(ctx, tx, property, tid)
	if err != nil {
		return d, err
	}
	if err := record(ctx, tx, "golf.tournament", tid, d.Code, "open_registration", property, map[string]any{"status": t.Status},
		map[string]any{"status": "open", "opensAt": opens, "closesAt": closes}, ""); err != nil {
		return d, err
	}
	return d, live(ctx, tx, property, tid, "status", nil)
}

// CloseRegistration closes the registration (Open → Closed).
func (m *Module) CloseRegistration(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, reason string) (TournamentDetail, error) {
	t, err := lockTournament(ctx, tx, property, tid)
	if err != nil {
		return TournamentDetail{}, err
	}
	if t.Status != "open" {
		return TournamentDetail{}, errs.Conflict("invalid_status", "only an open registration can be closed")
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournaments SET status = 'closed', registration_closes_at = least(coalesce(registration_closes_at, now()), now()),
		updated_by = $2 WHERE id = $1`, tid, actor(ctx)); err != nil {
		return TournamentDetail{}, err
	}
	d, err := m.Detail(ctx, tx, property, tid)
	if err != nil {
		return d, err
	}
	if err := record(ctx, tx, "golf.tournament", tid, d.Code, "close_registration", property, map[string]any{"status": "open"},
		map[string]any{"status": "closed"}, reason); err != nil {
		return d, err
	}
	return d, live(ctx, tx, property, tid, "status", nil)
}

// ReasonInput carries a mandatory reason.
type TournamentReasonInput struct {
	Reason string `json:"reason"`
}

// Cancel cancels a tournament: registrations are withdrawn with a full
// refund and the tee sheet re-opens.
func (m *Module) Cancel(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, reason string) (TournamentDetail, error) {
	if err := handle.Required("reason", reason); err != nil {
		return TournamentDetail{}, err
	}
	t, err := lockTournament(ctx, tx, property, tid)
	if err != nil {
		return TournamentDetail{}, err
	}
	if t.Status == "completed" || t.Status == "cancelled" {
		return TournamentDetail{}, errs.Conflict("invalid_status", "a "+t.Status+" tournament cannot be cancelled")
	}
	regs, err := collectIDs(tx.Query(ctx, `SELECT id FROM golf.tournament_registrations WHERE tournament_id = $1 AND status <> 'withdrawn'
		ORDER BY status = 'waitlisted', registered_at`, tid))
	if err != nil {
		return TournamentDetail{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournaments SET status = 'cancelled', cancelled_at = now(), cancel_reason = $2, updated_by = $3 WHERE id = $1`,
		tid, reason, actor(ctx)); err != nil {
		return TournamentDetail{}, err
	}
	for _, rid := range regs {
		if _, err := m.withdraw(ctx, tx, property, rid, withdrawal{reason: "Tournament cancelled: " + reason, fullRefund: true, cancelled: true}); err != nil {
			return TournamentDetail{}, err
		}
	}
	if err := m.releaseBlocks(ctx, tx, property, tid); err != nil {
		return TournamentDetail{}, err
	}
	d, err := m.Detail(ctx, tx, property, tid)
	if err != nil {
		return d, err
	}
	if err := record(ctx, tx, "golf.tournament", tid, d.Code, "cancel", property, map[string]any{"status": t.Status},
		map[string]any{"status": "cancelled", "withdrawn": len(regs)}, reason); err != nil {
		return d, err
	}
	return d, live(ctx, tx, property, tid, "status", nil)
}

func collectIDs(rows pgx.Rows, err error) ([]uuid.UUID, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}

// ── divisions ─────────────────────────────────────────────────────────────

// DivisionInput creates a division; on update omitted fields stay.
type TournamentDivisionInput struct {
	Code               *string    `json:"code,omitempty" doc:"Required on create"`
	Name               *string    `json:"name,omitempty" doc:"Required on create"`
	Sequence           *int       `json:"sequence,omitempty"`
	Gender             *string    `json:"gender,omitempty" enum:"male,female,any"`
	PlayerType         *string    `json:"playerType,omitempty" enum:"member,guest,any"`
	HandicapMin        *string    `json:"handicapMin,omitempty"`
	HandicapMax        *string    `json:"handicapMax,omitempty"`
	AgeMin             *int       `json:"ageMin,omitempty"`
	TeeSetID           *uuid.UUID `json:"teeSetId,omitempty" doc:"Tee set played by the division (default: by gender)"`
	HallOfFameDivision *string    `json:"hallOfFameDivision,omitempty" enum:"men,ladies,senior,junior,open"`
	Status             *string    `json:"status,omitempty" enum:"active,inactive"`
}

func (m *Module) editable(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID) (Tournament, error) {
	t, err := lockTournament(ctx, tx, property, tid)
	if err != nil {
		return t, err
	}
	if t.Status == "completed" || t.Status == "cancelled" {
		return t, errs.Conflict("tournament_closed", "a "+t.Status+" tournament cannot be changed")
	}
	return t, nil
}

// SaveDivision creates (did nil) or updates a division.
func (m *Module) SaveDivision(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, did *uuid.UUID, in TournamentDivisionInput) (TournamentDivision, error) {
	t, err := m.editable(ctx, tx, property, tid)
	if err != nil {
		return TournamentDivision{}, err
	}
	cur := TournamentDivision{Sequence: 1, Gender: "any", PlayerType: "any", Status: "active"}
	if did != nil {
		rows, err := tx.Query(ctx, divisionSelect+` WHERE d.id = $1 AND d.tournament_id = $2`, *did, tid)
		if cur, err = handle.One[TournamentDivision](rows, err, "division"); err != nil {
			return cur, err
		}
	} else if in.Code == nil || in.Name == nil {
		return cur, handle.Invalid("code", "required", "code and name are required")
	}
	before := cur
	if in.Code != nil {
		cur.Code = strings.ToUpper(strings.TrimSpace(*in.Code))
	}
	if in.Name != nil {
		cur.Name = strings.TrimSpace(*in.Name)
	}
	if in.Sequence != nil {
		cur.Sequence = *in.Sequence
	}
	if in.Gender != nil {
		cur.Gender = *in.Gender
	}
	if in.PlayerType != nil {
		cur.PlayerType = *in.PlayerType
	}
	if in.HandicapMin != nil {
		if cur.HandicapMin, err = decimalOrNil("handicapMin", *in.HandicapMin, -10, 54); err != nil {
			return cur, err
		}
	}
	if in.HandicapMax != nil {
		if cur.HandicapMax, err = decimalOrNil("handicapMax", *in.HandicapMax, -10, 54); err != nil {
			return cur, err
		}
	}
	if in.AgeMin != nil {
		cur.AgeMin = in.AgeMin
		if *in.AgeMin <= 0 {
			cur.AgeMin = nil
		}
	}
	if in.TeeSetID != nil {
		cur.TeeSetID = in.TeeSetID
		if *in.TeeSetID == uuid.Nil {
			cur.TeeSetID = nil
		}
	}
	if in.HallOfFameDivision != nil {
		cur.HallOfFameDivision = nullStr(*in.HallOfFameDivision)
	}
	if in.Status != nil {
		cur.Status = *in.Status
	}
	if !code20.MatchString(cur.Code) {
		return cur, handle.Invalid("code", "invalid", "1–20 characters: A–Z, 0–9, - or _")
	}
	if cur.Name == "" {
		return cur, handle.Invalid("name", "required", "name is required")
	}
	if err := oneOf("gender", cur.Gender, "male", "female", "any"); err != nil {
		return cur, err
	}
	if err := oneOf("playerType", cur.PlayerType, "member", "guest", "any"); err != nil {
		return cur, err
	}
	if err := oneOf("status", cur.Status, "active", "inactive"); err != nil {
		return cur, err
	}
	if cur.HallOfFameDivision != nil {
		if err := oneOf("hallOfFameDivision", *cur.HallOfFameDivision, "men", "ladies", "senior", "junior", "open"); err != nil {
			return cur, err
		}
	}
	if cur.HandicapMin != nil && cur.HandicapMax != nil && dec(*cur.HandicapMax).LessThan(dec(*cur.HandicapMin)) {
		return cur, handle.Invalid("handicapMax", "invalid", "handicapMax must not be below handicapMin")
	}
	if cur.TeeSetID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.tee_sets WHERE id = $1 AND course_id = $2)`, *cur.TeeSetID, t.CourseID).Scan(&ok); err != nil {
			return cur, err
		}
		if !ok {
			return cur, handle.Invalid("teeSetId", "not_found", "tee set of the tournament course")
		}
	}
	action := audit.ActionUpdate
	if did == nil {
		action = audit.ActionCreate
		cur.ID = id.New()
		_, err = tx.Exec(ctx, `INSERT INTO golf.tournament_divisions (id, property_id, tournament_id, code, name, sequence, gender, player_type, handicap_min,
			handicap_max, age_min, tee_set_id, hall_of_fame_division, status, created_by, updated_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10::numeric,$11,$12,$13,$14,$15,$15)`, cur.ID, property, tid, cur.Code, cur.Name, cur.Sequence,
			cur.Gender, cur.PlayerType, cur.HandicapMin, cur.HandicapMax, cur.AgeMin, cur.TeeSetID, cur.HallOfFameDivision, cur.Status, actor(ctx))
	} else {
		_, err = tx.Exec(ctx, `UPDATE golf.tournament_divisions SET code = $2, name = $3, sequence = $4, gender = $5, player_type = $6,
			handicap_min = $7::numeric, handicap_max = $8::numeric, age_min = $9, tee_set_id = $10, hall_of_fame_division = $11, status = $12, updated_by = $13
			WHERE id = $1`, cur.ID, cur.Code, cur.Name, cur.Sequence, cur.Gender, cur.PlayerType, cur.HandicapMin, cur.HandicapMax, cur.AgeMin, cur.TeeSetID,
			cur.HallOfFameDivision, cur.Status, actor(ctx))
	}
	if err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return cur, errs.Conflict("code_taken", "a division with this code exists")
		}
		return cur, err
	}
	rows, err := tx.Query(ctx, divisionSelect+` WHERE d.id = $1`, cur.ID)
	out, err := handle.One[TournamentDivision](rows, err, "division")
	if err != nil {
		return out, err
	}
	return out, record(ctx, tx, "golf.tournament_division", out.ID, t.Code+" · "+out.Name, action, property, before, out, "")
}

// DeleteDivision removes a division that no player, prize or result uses.
func (m *Module) DeleteDivision(ctx context.Context, tx pgx.Tx, property, tid, did uuid.UUID) error {
	t, err := m.editable(ctx, tx, property, tid)
	if err != nil {
		return err
	}
	var used bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.tournament_registrations WHERE division_id = $1)
		OR EXISTS (SELECT 1 FROM golf.tournament_prizes WHERE division_id = $1) OR EXISTS (SELECT 1 FROM golf.tournament_results WHERE division_id = $1)`, did).
		Scan(&used); err != nil {
		return err
	}
	if used {
		return errs.Conflict("division_in_use", "players or prizes use this division; set it inactive instead")
	}
	tag, err := tx.Exec(ctx, `DELETE FROM golf.tournament_divisions WHERE id = $1 AND tournament_id = $2`, did, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errs.NotFound("division")
	}
	return record(ctx, tx, "golf.tournament_division", did, t.Code, audit.ActionDelete, property, nil, nil, "")
}

// ── packages & fees ───────────────────────────────────────────────────────

// PackageInput creates a Tournament Package; on update omitted fields stay.
type TournamentPackageInput struct {
	Code        *string `json:"code,omitempty" doc:"Required on create"`
	Name        *string `json:"name,omitempty" doc:"Required on create"`
	Description *string `json:"description,omitempty"`
	PlayerType  *string `json:"playerType,omitempty" enum:"member,guest,any"`
	IsDefault   *bool   `json:"isDefault,omitempty" doc:"Chosen when a registration names no package"`
	Sequence    *int    `json:"sequence,omitempty"`
	Status      *string `json:"status,omitempty" enum:"active,inactive"`
}

// SavePackage creates (pid nil) or updates a package.
func (m *Module) SavePackage(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, pid *uuid.UUID, in TournamentPackageInput) (TournamentPackage, error) {
	t, err := m.editable(ctx, tx, property, tid)
	if err != nil {
		return TournamentPackage{}, err
	}
	cur := TournamentPackage{PlayerType: "any", Sequence: 1, Status: "active"}
	if pid != nil {
		rows, err := tx.Query(ctx, packageSelect+` WHERE p.id = $1 AND p.tournament_id = $2`, *pid, tid)
		if cur, err = handle.One[TournamentPackage](rows, err, "package"); err != nil {
			return cur, err
		}
	} else if in.Code == nil || in.Name == nil {
		return cur, handle.Invalid("code", "required", "code and name are required")
	}
	before := cur
	if in.Code != nil {
		cur.Code = strings.ToUpper(strings.TrimSpace(*in.Code))
	}
	if in.Name != nil {
		cur.Name = strings.TrimSpace(*in.Name)
	}
	if in.Description != nil {
		cur.Description = nullStr(*in.Description)
	}
	if in.PlayerType != nil {
		cur.PlayerType = *in.PlayerType
	}
	if in.IsDefault != nil {
		cur.IsDefault = *in.IsDefault
	}
	if in.Sequence != nil {
		cur.Sequence = *in.Sequence
	}
	if in.Status != nil {
		cur.Status = *in.Status
	}
	if !code20.MatchString(cur.Code) {
		return cur, handle.Invalid("code", "invalid", "1–20 characters: A–Z, 0–9, - or _")
	}
	if cur.Name == "" {
		return cur, handle.Invalid("name", "required", "name is required")
	}
	if err := oneOf("playerType", cur.PlayerType, "member", "guest", "any"); err != nil {
		return cur, err
	}
	if err := oneOf("status", cur.Status, "active", "inactive"); err != nil {
		return cur, err
	}
	action := audit.ActionUpdate
	if pid == nil {
		action = audit.ActionCreate
		cur.ID = id.New()
		_, err = tx.Exec(ctx, `INSERT INTO golf.tournament_packages (id, property_id, tournament_id, code, name, description, player_type, is_default, sequence,
			status, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)`, cur.ID, property, tid, cur.Code, cur.Name, cur.Description,
			cur.PlayerType, cur.IsDefault, cur.Sequence, cur.Status, actor(ctx))
	} else {
		_, err = tx.Exec(ctx, `UPDATE golf.tournament_packages SET code = $2, name = $3, description = $4, player_type = $5, is_default = $6, sequence = $7,
			status = $8, updated_by = $9 WHERE id = $1`, cur.ID, cur.Code, cur.Name, cur.Description, cur.PlayerType, cur.IsDefault, cur.Sequence, cur.Status,
			actor(ctx))
	}
	if err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return cur, errs.Conflict("code_taken", "a package with this code exists")
		}
		return cur, err
	}
	if cur.IsDefault {
		// one default package per player type
		if _, err := tx.Exec(ctx, `UPDATE golf.tournament_packages SET is_default = false WHERE tournament_id = $1 AND id <> $2 AND is_default
			AND (player_type = $3 OR $3 = 'any' OR player_type = 'any')`, tid, cur.ID, cur.PlayerType); err != nil {
			return cur, err
		}
	}
	fees, err := listFees(ctx, tx, tid)
	if err != nil {
		return cur, err
	}
	cur.MemberTotal, cur.GuestTotal = feeTotal(fees, &cur.ID, "member").String(), feeTotal(fees, &cur.ID, "guest").String()
	return cur, record(ctx, tx, "golf.tournament_package", cur.ID, t.Code+" · "+cur.Name, action, property, before, cur, "")
}

// DeletePackage removes a package no registration uses (its fees go too).
func (m *Module) DeletePackage(ctx context.Context, tx pgx.Tx, property, tid, pid uuid.UUID) error {
	t, err := m.editable(ctx, tx, property, tid)
	if err != nil {
		return err
	}
	var used bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.tournament_registrations WHERE package_id = $1)`, pid).Scan(&used); err != nil {
		return err
	}
	if used {
		return errs.Conflict("package_in_use", "registrations use this package; set it inactive instead")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM golf.tournament_fees WHERE package_id = $1 AND tournament_id = $2`, pid, tid); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM golf.tournament_packages WHERE id = $1 AND tournament_id = $2`, pid, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errs.NotFound("package")
	}
	return record(ctx, tx, "golf.tournament_package", pid, t.Code, audit.ActionDelete, property, nil, nil, "")
}

// FeeInput creates a fee component; on update omitted fields stay.
type TournamentFeeInput struct {
	PackageID  *uuid.UUID `json:"packageId,omitempty" doc:"Empty: charged on every registration of the player type (entry fee)"`
	Component  *string    `json:"component,omitempty" enum:"entry_fee,green_fee,caddy_fee,cart_fee,dinner,goodie_bag,insurance,other" doc:"Required on create"`
	Name       *string    `json:"name,omitempty" doc:"Required on create"`
	PlayerType *string    `json:"playerType,omitempty" enum:"member,guest,any"`
	Amount     *string    `json:"amount,omitempty" doc:"Nett amount (tax & service inclusive); required on create"`
	TaxCodes   []string   `json:"taxCodes,omitempty" doc:"Tax & Service rule codes included in the amount"`
	Liability  *bool      `json:"liability,omitempty" doc:"Default: true for the caddy fee"`
	Sequence   *int       `json:"sequence,omitempty"`
	Status     *string    `json:"status,omitempty" enum:"active,inactive"`
}

// SaveFee creates (fid nil) or updates a fee component.
func (m *Module) SaveFee(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, fid *uuid.UUID, in TournamentFeeInput) (TournamentFee, error) {
	t, err := m.editable(ctx, tx, property, tid)
	if err != nil {
		return TournamentFee{}, err
	}
	cur := TournamentFee{PlayerType: "any", Sequence: 1, Status: "active", Currency: t.Currency, TaxCodes: []string{}}
	if fid != nil {
		rows, err := tx.Query(ctx, feeSelect+` WHERE f.id = $1 AND f.tournament_id = $2`, *fid, tid)
		if cur, err = handle.One[TournamentFee](rows, err, "fee"); err != nil {
			return cur, err
		}
	} else if in.Component == nil || in.Name == nil || in.Amount == nil {
		return cur, handle.Invalid("component", "required", "component, name and amount are required")
	}
	before := cur
	if in.PackageID != nil {
		cur.PackageID = in.PackageID
		if *in.PackageID == uuid.Nil {
			cur.PackageID = nil
		}
	}
	if in.Component != nil {
		cur.Component = *in.Component
		if fid == nil && in.Liability == nil {
			cur.Liability = cur.Component == "caddy_fee"
		}
	}
	if in.Name != nil {
		cur.Name = strings.TrimSpace(*in.Name)
	}
	if in.PlayerType != nil {
		cur.PlayerType = *in.PlayerType
	}
	if in.Amount != nil {
		a, err := handle.Decimal("amount", *in.Amount, decimal.Zero)
		if err != nil {
			return cur, err
		}
		if a.IsNegative() {
			return cur, handle.Invalid("amount", "invalid", "amount must not be negative")
		}
		cur.Amount = a.String()
	}
	if in.TaxCodes != nil {
		cur.TaxCodes = in.TaxCodes
	}
	if in.Liability != nil {
		cur.Liability = *in.Liability
	}
	if in.Sequence != nil {
		cur.Sequence = *in.Sequence
	}
	if in.Status != nil {
		cur.Status = *in.Status
	}
	if err := oneOf("component", cur.Component, "entry_fee", "green_fee", "caddy_fee", "cart_fee", "dinner", "goodie_bag", "insurance", "other"); err != nil {
		return cur, err
	}
	if cur.Name == "" {
		return cur, handle.Invalid("name", "required", "name is required")
	}
	if err := oneOf("playerType", cur.PlayerType, "member", "guest", "any"); err != nil {
		return cur, err
	}
	if err := oneOf("status", cur.Status, "active", "inactive"); err != nil {
		return cur, err
	}
	if cur.PackageID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.tournament_packages WHERE id = $1 AND tournament_id = $2)`, *cur.PackageID, tid).Scan(&ok); err != nil {
			return cur, err
		}
		if !ok {
			return cur, handle.Invalid("packageId", "not_found", "package of this tournament")
		}
	}
	for _, c := range cur.TaxCodes {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM commercial.tax_service_rules WHERE property_id = $1 AND code = $2)`, property, c).Scan(&ok); err != nil {
			return cur, err
		}
		if !ok {
			return cur, handle.Invalid("taxCodes", "not_found", "unknown Tax & Service rule "+c)
		}
	}
	action := audit.ActionUpdate
	if fid == nil {
		action = audit.ActionCreate
		cur.ID = id.New()
		_, err = tx.Exec(ctx, `INSERT INTO golf.tournament_fees (id, property_id, tournament_id, package_id, component, name, player_type, amount, currency,
			tax_codes, liability, sequence, status, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9,$10,$11,$12,$13,$14,$14)`,
			cur.ID, property, tid, cur.PackageID, cur.Component, cur.Name, cur.PlayerType, cur.Amount, cur.Currency, cur.TaxCodes, cur.Liability, cur.Sequence,
			cur.Status, actor(ctx))
	} else {
		_, err = tx.Exec(ctx, `UPDATE golf.tournament_fees SET package_id = $2, component = $3, name = $4, player_type = $5, amount = $6::numeric, tax_codes = $7,
			liability = $8, sequence = $9, status = $10, updated_by = $11 WHERE id = $1`, cur.ID, cur.PackageID, cur.Component, cur.Name, cur.PlayerType,
			cur.Amount, cur.TaxCodes, cur.Liability, cur.Sequence, cur.Status, actor(ctx))
	}
	if err != nil {
		return cur, err
	}
	rows, err := tx.Query(ctx, feeSelect+` WHERE f.id = $1`, cur.ID)
	out, err := handle.One[TournamentFee](rows, err, "fee")
	if err != nil {
		return out, err
	}
	return out, record(ctx, tx, "golf.tournament_fee", out.ID, t.Code+" · "+out.Name, action, property, before, out, "")
}

// DeleteFee removes a fee component (fees already posted stay on the folios).
func (m *Module) DeleteFee(ctx context.Context, tx pgx.Tx, property, tid, fid uuid.UUID) error {
	t, err := m.editable(ctx, tx, property, tid)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM golf.tournament_fees WHERE id = $1 AND tournament_id = $2`, fid, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errs.NotFound("fee")
	}
	return record(ctx, tx, "golf.tournament_fee", fid, t.Code, audit.ActionDelete, property, nil, nil, "")
}
