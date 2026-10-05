package tournament

// Tournament lists and the public views: the Tournament Schedule of the
// Back Office, the website and Member App listing (K5 public data:
// /public/tournaments, /public/tournaments/{id}), the public leaderboard
// (opt-in per tournament, names only with the player's consent,
// FR-WEB-P3-06) and the Leaderboard Screen feed (FR-OPS-P3-04).

import (
	"context"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/handle"
)

// ListFilter filters the Tournament Schedule.
type ListFilter struct {
	Status string // comma separated
	Type   string
	From   string // YYYY-MM-DD (start date on or after)
	To     string
	Search string
	Source string // oneclub | import
	Limit  int
}

// List lists the tournaments of a property (Tournament Schedule).
func List(ctx context.Context, q dbtx.Querier, property uuid.UUID, f ListFilter) ([]Tournament, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	return handle.List[Tournament](q.Query(ctx, tournamentSelect+` WHERE t.property_id = $1 AND ($2 = '' OR t.status = ANY(string_to_array($2, ',')))
		AND ($3 = '' OR t.tournament_type = $3) AND ($4 = '' OR t.end_date >= $4::date) AND ($5 = '' OR t.start_date <= $5::date)
		AND ($6 = '' OR t.name ILIKE '%' || $6 || '%' OR t.code ILIKE '%' || $6 || '%') AND ($7 = '' OR t.source = $7)
		ORDER BY t.start_date DESC, t.code LIMIT $8`, property, f.Status, f.Type, f.From, f.To, f.Search, f.Source, f.Limit))
}

// PublicPackage is a Tournament Package as players see it.
type TournamentPublicPackage struct {
	ID          uuid.UUID `json:"id"`
	Code        string    `json:"code"`
	Name        string    `json:"name"`
	Description *string   `json:"description"`
	PlayerType  string    `json:"playerType" enum:"member,guest,any"`
	IsDefault   bool      `json:"isDefault"`
	MemberTotal string    `json:"memberTotal"`
	GuestTotal  string    `json:"guestTotal"`
}

// PublicRound is the schedule of a round.
type TournamentPublicRound struct {
	RoundNo   int    `json:"roundNo"`
	PlayDate  string `json:"playDate"`
	StartTime string `json:"startTime"`
}

// PublicTournament is a tournament on the website and in the Member App.
type PublicTournament struct {
	ID                   uuid.UUID                 `json:"id"`
	Code                 string                    `json:"code"`
	Name                 string                    `json:"name"`
	Description          *string                   `json:"description"`
	TournamentType       string                    `json:"tournamentType"`
	CourseName           string                    `json:"courseName"`
	StartDate            string                    `json:"startDate"`
	EndDate              string                    `json:"endDate"`
	Format               string                    `json:"format" enum:"stroke_play,stableford"`
	ScoringBasis         string                    `json:"scoringBasis" enum:"gross,net,gross_and_net"`
	Eligibility          string                    `json:"eligibility" enum:"members,members_and_guests,invitation,open"`
	StartType            string                    `json:"startType" enum:"shotgun,tee_times"`
	FieldSize            int                       `json:"fieldSize"`
	PlacesLeft           int                       `json:"placesLeft"`
	WaitlistEnabled      bool                      `json:"waitlistEnabled"`
	Status               string                    `json:"status" enum:"draft,open,closed,in_progress,completed,cancelled"`
	RegistrationOpen     bool                      `json:"registrationOpen" doc:"Registration is open now"`
	RegistrationOpensAt  *time.Time                `json:"registrationOpensAt"`
	RegistrationClosesAt *time.Time                `json:"registrationClosesAt"`
	LeaderboardPublic    bool                      `json:"leaderboardPublic"`
	Rounds               []TournamentPublicRound   `json:"rounds"`
	Packages             []TournamentPublicPackage `json:"packages"`
	Sponsors             []TournamentSponsorLogo   `json:"sponsors"`
	MaxHandicap          string                    `json:"maxHandicap" doc:"Maximum handicap index (Tournament Policies or the tournament)"`
}

func (m *Module) publicView(ctx context.Context, q dbtx.Querier, property uuid.UUID, t Tournament) (PublicTournament, error) {
	out := PublicTournament{ID: t.ID, Code: t.Code, Name: t.Name, Description: t.Description, TournamentType: t.TournamentType, CourseName: t.CourseName,
		StartDate: t.StartDate, EndDate: t.EndDate, Format: t.Format, ScoringBasis: t.ScoringBasis, Eligibility: t.Eligibility, StartType: t.StartType,
		FieldSize: t.FieldSize, PlacesLeft: max(0, t.FieldSize-t.Registered), WaitlistEnabled: t.WaitlistEnabled, Status: t.Status,
		RegistrationOpensAt: t.RegistrationOpensAt, RegistrationClosesAt: t.RegistrationClosesAt, LeaderboardPublic: t.LeaderboardPublic,
		Rounds: []TournamentPublicRound{}, Packages: []TournamentPublicPackage{}}
	out.RegistrationOpen = t.Status == "open" && (t.RegistrationOpensAt == nil || !t.RegistrationOpensAt.After(now())) &&
		(t.RegistrationClosesAt == nil || t.RegistrationClosesAt.After(now()))
	rounds, err := listRounds(ctx, q, t.ID)
	if err != nil {
		return out, err
	}
	for _, r := range rounds {
		out.Rounds = append(out.Rounds, TournamentPublicRound{RoundNo: r.RoundNo, PlayDate: r.PlayDate, StartTime: r.StartTime})
	}
	fees, err := listFees(ctx, q, t.ID)
	if err != nil {
		return out, err
	}
	pkgs, err := listPackages(ctx, q, t.ID, fees)
	if err != nil {
		return out, err
	}
	for _, p := range pkgs {
		if p.Status != "active" {
			continue
		}
		out.Packages = append(out.Packages, TournamentPublicPackage{ID: p.ID, Code: p.Code, Name: p.Name, Description: p.Description, PlayerType: p.PlayerType,
			IsDefault: p.IsDefault, MemberTotal: p.MemberTotal, GuestTotal: p.GuestTotal})
	}
	if len(out.Packages) == 0 {
		// entry fees without a package
		out.Packages = append(out.Packages, TournamentPublicPackage{Code: "ENTRY", Name: "Tournament Fee", PlayerType: "any", IsDefault: true,
			MemberTotal: feeTotal(fees, nil, "member").String(), GuestTotal: feeTotal(fees, nil, "guest").String()})
	}
	ss, err := listSponsors(ctx, q, t.ID)
	if err != nil {
		return out, err
	}
	out.Sponsors = sponsorLogos(ss, true)
	pol, _, err := LoadPolicy(ctx, q, property)
	if err != nil {
		return out, err
	}
	out.MaxHandicap = pol.MaxHandicap
	if t.MaxHandicap != nil {
		out.MaxHandicap = *t.MaxHandicap
	}
	return out, nil
}

// PublicList lists the tournaments players can see: the website shows
// public tournaments only; the Member App also members' and invitation
// tournaments (registration through the golf office). Imported history is
// not listed.
func (m *Module) PublicList(ctx context.Context, q dbtx.Querier, property uuid.UUID, members bool, status string) ([]PublicTournament, error) {
	list, err := handle.List[Tournament](q.Query(ctx, tournamentSelect+` WHERE t.property_id = $1 AND t.source = 'oneclub' AND t.status <> 'draft'
		AND ($2 OR t.public) AND ($3 = '' OR t.status = ANY(string_to_array($3, ',')))
		AND (t.status NOT IN ('completed', 'cancelled') OR t.end_date >= current_date - 400)
		ORDER BY CASE WHEN t.status IN ('open', 'closed', 'in_progress') THEN 0 ELSE 1 END, t.start_date, t.code LIMIT 100`, property, members, status))
	if err != nil {
		return nil, err
	}
	out := make([]PublicTournament, 0, len(list))
	for _, t := range list {
		v, err := m.publicView(ctx, q, property, t)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// PublicGet loads one tournament for players (404 for drafts and, on the
// website, non-public tournaments).
func (m *Module) PublicGet(ctx context.Context, q dbtx.Querier, property, tid uuid.UUID, members bool) (PublicTournament, error) {
	t, err := tournamentAt(ctx, q, property, tid)
	if err != nil {
		return PublicTournament{}, err
	}
	if t.Status == "draft" || (!members && !t.Public) {
		return PublicTournament{}, errs.NotFound("tournament")
	}
	return m.publicView(ctx, q, property, t)
}

// PublicLeaderboard is the website leaderboard: only when the tournament
// opted in, players without consent are shown by initials.
func (m *Module) PublicLeaderboard(ctx context.Context, q dbtx.Querier, property, tid uuid.UUID, category, division string) (TournamentLeaderboard, error) {
	t, err := tournamentAt(ctx, q, property, tid)
	if err != nil {
		return TournamentLeaderboard{}, err
	}
	if !t.Public || !t.LeaderboardPublic || (t.Status != "in_progress" && t.Status != "completed") {
		return TournamentLeaderboard{}, errs.NotFound("leaderboard")
	}
	return m.compute(ctx, q, property, t, computeOptions{public: true, category: category, division: division})
}

// MemberLeaderboard is the leaderboard in the Member App (players of the
// club; once the tournament started).
func (m *Module) MemberLeaderboard(ctx context.Context, q dbtx.Querier, property, tid uuid.UUID, category, division string) (TournamentLeaderboard, error) {
	t, err := tournamentAt(ctx, q, property, tid)
	if err != nil {
		return TournamentLeaderboard{}, err
	}
	if t.Status != "in_progress" && t.Status != "completed" {
		return TournamentLeaderboard{}, errs.Conflict("not_started", "the leaderboard opens when the tournament starts")
	}
	return m.compute(ctx, q, property, t, computeOptions{category: category, division: division})
}

// PublicStartSheet is the published start sheet for players (no caddies).
func (m *Module) PublicStartSheet(ctx context.Context, q dbtx.Querier, property, tid uuid.UUID, roundNo int) (TournamentStartSheet, error) {
	s, err := m.GetStartSheet(ctx, q, property, tid, roundNo)
	if err != nil {
		return s, err
	}
	if s.Status == "scheduled" || s.Status == "drawn" {
		return TournamentStartSheet{}, errs.NotFound("start sheet")
	}
	for i := range s.Flights {
		for j := range s.Flights[i].Players {
			p := &s.Flights[i].Players[j]
			p.CaddyID, p.CaddyCode, p.CaddyName, p.ScoreID, p.ScoreStatus = nil, nil, nil, nil, nil
		}
	}
	return s, nil
}

// ScreenFeed is the Leaderboard Screen: the leaderboards of the
// tournaments in progress (or finalized today) with the rotation of
// Tournament Policies.
type TournamentScreenFeed struct {
	RotateSeconds int                     `json:"rotateSeconds"`
	Rows          int                     `json:"rows"`
	Leaderboards  []TournamentLeaderboard `json:"leaderboards"`
}

// Screen builds the Leaderboard Screen feed.
func (m *Module) Screen(ctx context.Context, q dbtx.Querier, property uuid.UUID, tid *uuid.UUID) (TournamentScreenFeed, error) {
	pol, _, err := LoadPolicy(ctx, q, property)
	if err != nil {
		return TournamentScreenFeed{}, err
	}
	out := TournamentScreenFeed{RotateSeconds: pol.LeaderboardRotateSeconds, Rows: pol.LeaderboardScreenRows, Leaderboards: []TournamentLeaderboard{}}
	list, err := handle.List[Tournament](q.Query(ctx, tournamentSelect+` WHERE t.property_id = $1 AND t.source = 'oneclub'
		AND ($2::uuid IS NULL OR t.id = $2) AND (t.status = 'in_progress' OR (t.status = 'completed' AND t.finalized_at >= now() - interval '24 hours'))
		ORDER BY t.start_date, t.code LIMIT 5`, property, tid))
	if err != nil {
		return out, err
	}
	for _, t := range list {
		lb, err := m.compute(ctx, q, property, t, computeOptions{limit: pol.LeaderboardScreenRows})
		if err != nil {
			return out, err
		}
		out.Leaderboards = append(out.Leaderboards, lb)
	}
	return out, nil
}
