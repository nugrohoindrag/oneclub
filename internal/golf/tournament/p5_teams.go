package tournament

// PRD P5 FR-TRN-P5-01 Team Formats: Scramble and Four-ball (Best Ball) from
// day one, Foursomes and Texas Scramble by configuration (§16 #11), with a
// configurable handicap allowance. A tournament takes a format (snapshot,
// so later edits of the format do not change it), its teams are formed by
// hand or automatically (balanced by handicap) and play together (the
// team code is the pairing group of the P3 draw). One-ball formats enter
// the team score once on the Tournament Desk (written on every member's P2
// scorecard, so validation and the round flow of P3 stay unchanged);
// Four-ball members enter their own scores. The team leaderboard ranks the
// teams per category with countback; the results are frozen when the
// tournament is finalized (golf.tournament_finalized).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/golf/experience"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

// TeamFormatTypes are the team formats of FR-TRN-P5-01.
var TeamFormatTypes = []string{"scramble", "four_ball", "foursomes", "texas_scramble"}

// TeamFormats (Golf → Tournaments → Team Formats).
var TeamFormats = &resource.Def{
	Key: "golf.team_format", Module: "golf", Perm: "golf.team_format", Path: "/api/v1/golf/team-formats", Table: "golf.team_formats",
	Name: "Team Format", Plural: "Team Formats", Tag: tagTournaments, PropertyScoped: true, CodeField: "code", OrderBy: "code", SchemaName: "TeamFormat",
	Fields: []resource.Field{
		{Name: "code", Column: "code", Label: "Code", Kind: resource.String, Required: true, Max: 20, Upper: true, CreateOnly: true, Pattern: code20,
			PatternMsg: "1–20 characters: A–Z, 0–9, - or _", Search: true},
		resource.Name(),
		{Name: "formatType", Column: "format_type", Label: "Format", Kind: resource.Enum, Enum: TeamFormatTypes, Required: true, Filter: true},
		{Name: "teamSize", Column: "team_size", Label: "Players per Team", Kind: resource.Int, Default: int64(4), Min: resource.Min(2), MaxN: resource.Max(4)},
		{Name: "scoresPerHole", Column: "scores_per_hole", Label: "Best Scores per Hole (Four-ball)", Kind: resource.Int, Default: int64(1),
			Min: resource.Min(1), MaxN: resource.Max(4)},
		{Name: "allowances", Column: "allowances", Label: "Handicap Allowance % (lowest handicap first; empty = WHS recommendation)",
			Kind: resource.IntList, Min: resource.Min(0), MaxN: resource.Max(100), Default: []int64{}},
		{Name: "minDrives", Column: "min_drives", Label: "Minimum Drives per Player (Texas Scramble)", Kind: resource.Int, Min: resource.Min(0),
			MaxN: resource.Max(18)},
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 2000},
		{Name: "status", Column: "status", Label: "Status", Kind: resource.Enum, Enum: []string{"active", "inactive"}, Default: "active", Filter: true},
	},
}

func init() {
	TeamFormats.Hooks = resource.Hooks{BeforeWrite: teamFormatBeforeWrite}
}

func intsOf(v any) []int {
	var out []int
	switch t := v.(type) {
	case []int64:
		for _, x := range t {
			out = append(out, int(x))
		}
	case []int32:
		for _, x := range t {
			out = append(out, int(x))
		}
	case []int:
		out = append(out, t...)
	case []any:
		for _, x := range t {
			switch n := x.(type) {
			case float64:
				out = append(out, int(n))
			case int64:
				out = append(out, int(n))
			case json.Number:
				i, _ := n.Int64()
				out = append(out, int(i))
			}
		}
	}
	return out
}

func numOf(v any, def int) int {
	switch t := v.(type) {
	case int64:
		return int(t)
	case int32:
		return int(t)
	case int:
		return t
	case float64:
		return int(t)
	case json.Number:
		i, _ := t.Int64()
		return int(i)
	}
	return def
}

func teamFormatBeforeWrite(_ context.Context, _ pgx.Tx, v map[string]any, before map[string]any) error {
	m := map[string]any{}
	for k, x := range before {
		m[k] = x
	}
	for k, x := range v {
		m[k] = x
	}
	ft, _ := m["formatType"].(string)
	size := numOf(m["teamSize"], 4)
	if ft == "foursomes" && size != 2 {
		return errs.Validation("invalid_team_size", "Foursomes is played by teams of two", errs.Field("teamSize", "invalid", "2"))
	}
	if ft != "four_ball" && numOf(m["scoresPerHole"], 1) != 1 {
		return errs.Validation("invalid_scores", "one-ball formats count one team score per hole", errs.Field("scoresPerHole", "invalid", "1"))
	}
	if ft == "four_ball" && numOf(m["scoresPerHole"], 1) > size {
		return errs.Validation("invalid_scores", "more best scores than players", errs.Field("scoresPerHole", "invalid", "≤ players per team"))
	}
	if al := intsOf(m["allowances"]); len(al) > 0 && ft != "four_ball" && len(al) > size {
		return errs.Validation("invalid_allowances", "one allowance per player at most", errs.Field("allowances", "invalid", "≤ players per team"))
	} else if len(al) == 0 && ft != "" {
		def := DefaultAllowances(ft, size)
		vals := make([]int64, len(def))
		for i, x := range def {
			vals[i] = int64(x)
		}
		v["allowances"] = vals
	}
	return nil
}

// ── team setup of a tournament ─────────────────────────────────────────────

// TournamentTeamMember is a member of a team.
type TournamentTeamMember struct {
	RegistrationID  uuid.UUID `json:"registrationId" db:"registration_id"`
	Number          string    `json:"number" db:"number"`
	PlayerName      string    `json:"playerName" db:"player_name"`
	CustomerID      uuid.UUID `json:"customerId" db:"customer_id"`
	HandicapIndex   *string   `json:"handicapIndex" db:"handicap_index"`
	Position        int       `json:"position" db:"position"`
	Status          string    `json:"status" db:"status"`
	Consent         bool      `json:"-" db:"public_consent"`
	CourseHandicap  *int      `json:"courseHandicap" db:"-" doc:"Of the current round (after the draw is published)"`
	PlayingHandicap *int      `json:"playingHandicap" db:"-" doc:"Best ball: the member's playing handicap"`
}

// TournamentTeam is a team with its members.
type TournamentTeam struct {
	ID                    uuid.UUID              `json:"id" db:"id"`
	Code                  string                 `json:"code" db:"code"`
	Name                  string                 `json:"name" db:"name"`
	CaptainRegistrationID *uuid.UUID             `json:"captainRegistrationId" db:"captain_registration_id"`
	Members               []TournamentTeamMember `json:"members" db:"-"`
	TeamHandicap          *int                   `json:"teamHandicap" db:"-" doc:"One ball: the team handicap of the current round"`
}

// TournamentTeamSetup is the team format of a tournament with its teams.
type TournamentTeamSetup struct {
	TournamentID uuid.UUID        `json:"tournamentId"`
	TeamFormatID *uuid.UUID       `json:"teamFormatId"`
	Format       *TeamFormatSpec  `json:"format" doc:"Snapshot taken when the format was set"`
	Teams        []TournamentTeam `json:"teams"`
	Unassigned   int              `json:"unassigned" doc:"Registered players without a team"`
}

// TeamSetupInput sets (or removes, null) the team format of a tournament.
type TournamentTeamSetupInput struct {
	TeamFormatID *uuid.UUID `json:"teamFormatId" doc:"Null removes the team format (teams are deleted)"`
}

// teamSettings returns the format snapshot of a team tournament (nil = individual).
func teamSettings(ctx context.Context, q dbtx.Querier, tid uuid.UUID) (*uuid.UUID, *TeamFormatSpec, error) {
	var fid uuid.UUID
	var raw []byte
	err := q.QueryRow(ctx, `SELECT team_format_id, format_snapshot FROM golf.tournament_team_settings WHERE tournament_id = $1`, tid).Scan(&fid, &raw)
	if dbtx.IsNoRows(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var f TeamFormatSpec
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, nil, err
	}
	return &fid, &f, nil
}

func loadTeams(ctx context.Context, q dbtx.Querier, tid uuid.UUID) ([]TournamentTeam, error) {
	teams, err := handle.List[TournamentTeam](q.Query(ctx, `SELECT id, code, name, captain_registration_id FROM golf.tournament_teams WHERE tournament_id = $1
		ORDER BY code`, tid))
	if err != nil {
		return nil, err
	}
	type row struct {
		TeamID uuid.UUID `db:"team_id"`
		TournamentTeamMember
	}
	members, err := handle.List[row](q.Query(ctx, `SELECT m.team_id, m.registration_id, r.number, r.player_name, r.customer_id,
		trim_scale(r.handicap_index)::text AS handicap_index, m.position, r.status, r.public_consent
		FROM golf.tournament_team_members m JOIN golf.tournament_teams t ON t.id = m.team_id JOIN golf.tournament_registrations r ON r.id = m.registration_id
		WHERE t.tournament_id = $1 ORDER BY m.team_id, m.position, r.player_name`, tid))
	if err != nil {
		return nil, err
	}
	by := map[uuid.UUID][]TournamentTeamMember{}
	for _, m := range members {
		by[m.TeamID] = append(by[m.TeamID], m.TournamentTeamMember)
	}
	for i := range teams {
		teams[i].Members = by[teams[i].ID]
		if teams[i].Members == nil {
			teams[i].Members = []TournamentTeamMember{}
		}
	}
	return teams, nil
}

// courseHandicaps of the registrations in a round (from the tournament scores).
func courseHandicaps(ctx context.Context, q dbtx.Querier, tid uuid.UUID, roundNo int) (map[uuid.UUID]*int, error) {
	out := map[uuid.UUID]*int{}
	rows, err := q.Query(ctx, `SELECT s.registration_id, s.course_handicap FROM golf.tournament_scores s JOIN golf.tournament_rounds x ON x.id = s.round_id
		WHERE s.tournament_id = $1 AND x.round_no = $2`, tid, roundNo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var rid uuid.UUID
		var ch *int
		if err := rows.Scan(&rid, &ch); err != nil {
			return nil, err
		}
		out[rid] = ch
	}
	return out, rows.Err()
}

// TeamSetup returns the team format and teams of a tournament.
func (m *Module) TeamSetup(ctx context.Context, q dbtx.Querier, property, tid uuid.UUID) (TournamentTeamSetup, error) {
	t, err := tournamentAt(ctx, q, property, tid)
	if err != nil {
		return TournamentTeamSetup{}, err
	}
	out := TournamentTeamSetup{TournamentID: tid, Teams: []TournamentTeam{}}
	if out.TeamFormatID, out.Format, err = teamSettings(ctx, q, tid); err != nil {
		return out, err
	}
	if out.Teams, err = loadTeams(ctx, q, tid); err != nil {
		return out, err
	}
	if out.Format != nil {
		chs, err := courseHandicaps(ctx, q, tid, t.CurrentRound)
		if err != nil {
			return out, err
		}
		for i := range out.Teams {
			tm := &out.Teams[i]
			var course []*int
			for k := range tm.Members {
				tm.Members[k].CourseHandicap = chs[tm.Members[k].RegistrationID]
				course = append(course, tm.Members[k].CourseHandicap)
			}
			per, team := TeamHandicaps(*out.Format, course)
			if out.Format.OneBall() {
				tm.TeamHandicap = &team
			} else {
				for k := range tm.Members {
					tm.Members[k].PlayingHandicap = ptr(per[k])
				}
			}
		}
	}
	err = q.QueryRow(ctx, `SELECT count(*)::int FROM golf.tournament_registrations r WHERE r.tournament_id = $1 AND r.status IN ('registered', 'checked_in')
		AND NOT EXISTS (SELECT 1 FROM golf.tournament_team_members m WHERE m.registration_id = r.id)`, tid).Scan(&out.Unassigned)
	return out, err
}

func editableTeams(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID) (Tournament, error) {
	t, err := lockTournament(ctx, tx, property, tid)
	if err != nil {
		return t, err
	}
	if t.Status != "draft" && t.Status != "open" && t.Status != "closed" {
		return t, errs.Conflict("invalid_status", "teams change before the tournament starts, not when "+t.Status)
	}
	return t, nil
}

// SetTeamFormat sets the team format of a tournament (snapshot of the format).
func (m *Module) SetTeamFormat(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, in TournamentTeamSetupInput) (TournamentTeamSetup, error) {
	t, err := editableTeams(ctx, tx, property, tid)
	if err != nil {
		return TournamentTeamSetup{}, err
	}
	before, err := m.TeamSetup(ctx, tx, property, tid)
	if err != nil {
		return before, err
	}
	if in.TeamFormatID == nil {
		for _, tm := range before.Teams {
			if err := deleteTeam(ctx, tx, tm.ID); err != nil {
				return before, err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM golf.tournament_team_settings WHERE tournament_id = $1`, tid); err != nil {
			return before, err
		}
	} else {
		var f TeamFormatSpec
		var status string
		var allow []int32
		err := tx.QueryRow(ctx, `SELECT code, name, format_type, team_size, scores_per_hole, allowances, min_drives, status FROM golf.team_formats
			WHERE id = $1 AND property_id = $2`, *in.TeamFormatID, property).Scan(&f.Code, &f.Name, &f.FormatType, &f.TeamSize, &f.ScoresPerHole, &allow,
			&f.MinDrives, &status)
		if dbtx.IsNoRows(err) {
			return before, handle.Invalid("teamFormatId", "not_found", "team format of this property")
		}
		if err != nil {
			return before, err
		}
		if status != "active" {
			return before, handle.Invalid("teamFormatId", "inactive", "an active team format (Settings: activate Foursomes / Texas Scramble first)")
		}
		for _, a := range allow {
			f.Allowances = append(f.Allowances, int(a))
		}
		if len(f.Allowances) == 0 {
			f.Allowances = DefaultAllowances(f.FormatType, f.TeamSize)
		}
		for _, tm := range before.Teams {
			if len(tm.Members) > f.TeamSize {
				return before, errs.Conflict("team_too_large", fmt.Sprintf("team %s has %d players; %s is played by teams of %d", tm.Name, len(tm.Members), f.Name,
					f.TeamSize))
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO golf.tournament_team_settings (tournament_id, property_id, team_format_id, format_snapshot, updated_by)
			VALUES ($1,$2,$3,$4,$5) ON CONFLICT (tournament_id) DO UPDATE SET team_format_id = EXCLUDED.team_format_id,
			format_snapshot = EXCLUDED.format_snapshot, updated_by = EXCLUDED.updated_by`, tid, property, *in.TeamFormatID, jsonOf(f), actor(ctx)); err != nil {
			return before, err
		}
	}
	after, err := m.TeamSetup(ctx, tx, property, tid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.tournament", tid, t.Code+" team format", "update", property, map[string]any{"format": before.Format},
		map[string]any{"format": after.Format}, "")
}

// TournamentTeamInput creates or changes a team.
type TournamentTeamInput struct {
	Name                  *string     `json:"name,omitempty" doc:"Default: Team <code>"`
	RegistrationIDs       []uuid.UUID `json:"registrationIds,omitempty" doc:"Members (replaces the members on update)"`
	CaptainRegistrationID *uuid.UUID  `json:"captainRegistrationId,omitempty" doc:"Default: the first member (one-ball team card)"`
}

func deleteTeam(ctx context.Context, tx pgx.Tx, teamID uuid.UUID) error {
	var code string
	if err := tx.QueryRow(ctx, `SELECT code FROM golf.tournament_teams WHERE id = $1`, teamID).Scan(&code); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_registrations r SET pairing_group = NULL FROM golf.tournament_team_members m
		WHERE m.team_id = $1 AND m.registration_id = r.id AND r.pairing_group = $2`, teamID, "TEAM-"+code); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM golf.tournament_team_members WHERE team_id = $1`, teamID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM golf.tournament_teams WHERE id = $1`, teamID)
	return err
}

// setMembers replaces the members of a team; members draw together (the
// team is the pairing group of the P3 draw).
func setMembers(ctx context.Context, tx pgx.Tx, property, tid, teamID uuid.UUID, code string, regs []uuid.UUID, size int) error {
	if len(regs) < 2 || len(regs) > size {
		return handle.Invalid("registrationIds", "invalid_team_size", fmt.Sprintf("2–%d players per team", size))
	}
	seen := map[uuid.UUID]bool{}
	for _, r := range regs {
		if seen[r] {
			return handle.Invalid("registrationIds", "duplicate", "a player once")
		}
		seen[r] = true
		var status string
		var other *uuid.UUID
		err := tx.QueryRow(ctx, `SELECT r.status, (SELECT m.team_id FROM golf.tournament_team_members m WHERE m.registration_id = r.id)
			FROM golf.tournament_registrations r WHERE r.id = $1 AND r.tournament_id = $2`, r, tid).Scan(&status, &other)
		if dbtx.IsNoRows(err) {
			return handle.Invalid("registrationIds", "not_found", "registration of this tournament")
		}
		if err != nil {
			return err
		}
		if status != "registered" && status != "checked_in" {
			return handle.Invalid("registrationIds", "not_registered", "registered players only (not "+status+")")
		}
		if other != nil && *other != teamID {
			return errs.Conflict("already_in_team", "a player is already in another team")
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_registrations r SET pairing_group = NULL FROM golf.tournament_team_members m
		WHERE m.team_id = $1 AND m.registration_id = r.id`, teamID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM golf.tournament_team_members WHERE team_id = $1`, teamID); err != nil {
		return err
	}
	for i, r := range regs {
		if _, err := tx.Exec(ctx, `INSERT INTO golf.tournament_team_members (team_id, registration_id, property_id, position) VALUES ($1,$2,$3,$4)`,
			teamID, r, property, i+1); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE golf.tournament_registrations SET pairing_group = $2 WHERE id = ANY($1)`, regs, "TEAM-"+code)
	return err
}

func nextTeamCode(ctx context.Context, tx pgx.Tx, tid uuid.UUID) (string, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT coalesce(max(substring(code FROM '^T([0-9]+)$')::int), 0) + 1 FROM golf.tournament_teams WHERE tournament_id = $1`, tid).Scan(&n)
	return fmt.Sprintf("T%02d", n), err
}

// SaveTeam creates (teamID nil) or changes a team.
func (m *Module) SaveTeam(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, teamID *uuid.UUID, in TournamentTeamInput) (TournamentTeam, error) {
	t, err := editableTeams(ctx, tx, property, tid)
	if err != nil {
		return TournamentTeam{}, err
	}
	_, f, err := teamSettings(ctx, tx, tid)
	if err != nil {
		return TournamentTeam{}, err
	}
	if f == nil {
		return TournamentTeam{}, errs.Conflict("no_team_format", "set the team format of "+t.Name+" first")
	}
	var before any
	var code string
	if teamID == nil {
		if len(in.RegistrationIDs) == 0 {
			return TournamentTeam{}, handle.Invalid("registrationIds", "required", "the players of the team")
		}
		if code, err = nextTeamCode(ctx, tx, tid); err != nil {
			return TournamentTeam{}, err
		}
		nid := id.New()
		teamID = &nid
		name := "Team " + code
		if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
			name = strings.TrimSpace(*in.Name)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO golf.tournament_teams (id, property_id, tournament_id, code, name, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$6)`,
			nid, property, tid, code, name, actor(ctx)); err != nil {
			return TournamentTeam{}, err
		}
	} else {
		teams, err := loadTeams(ctx, tx, tid)
		if err != nil {
			return TournamentTeam{}, err
		}
		for _, tm := range teams {
			if tm.ID == *teamID {
				before, code = tm, tm.Code
			}
		}
		if code == "" {
			return TournamentTeam{}, errs.NotFound("team")
		}
		if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
			if _, err := tx.Exec(ctx, `UPDATE golf.tournament_teams SET name = $2, updated_by = $3 WHERE id = $1`, *teamID, strings.TrimSpace(*in.Name),
				actor(ctx)); err != nil {
				return TournamentTeam{}, err
			}
		}
	}
	if len(in.RegistrationIDs) > 0 {
		if err := setMembers(ctx, tx, property, tid, *teamID, code, in.RegistrationIDs, f.TeamSize); err != nil {
			return TournamentTeam{}, err
		}
	}
	captain := in.CaptainRegistrationID
	if captain == nil {
		var first uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT t.captain_registration_id FROM golf.tournament_teams t JOIN golf.tournament_team_members m
			ON m.team_id = t.id AND m.registration_id = t.captain_registration_id WHERE t.id = $1),
			(SELECT registration_id FROM golf.tournament_team_members WHERE team_id = $1 ORDER BY position LIMIT 1))`, *teamID).Scan(&first); err != nil {
			return TournamentTeam{}, err
		}
		captain = &first
	} else {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.tournament_team_members WHERE team_id = $1 AND registration_id = $2)`, *teamID,
			*captain).Scan(&ok); err != nil {
			return TournamentTeam{}, err
		}
		if !ok {
			return TournamentTeam{}, handle.Invalid("captainRegistrationId", "not_member", "a member of the team")
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_teams SET captain_registration_id = $2 WHERE id = $1`, *teamID, *captain); err != nil {
		return TournamentTeam{}, err
	}
	setup, err := m.TeamSetup(ctx, tx, property, tid)
	if err != nil {
		return TournamentTeam{}, err
	}
	for _, tm := range setup.Teams {
		if tm.ID == *teamID {
			action := audit.ActionCreate
			if before != nil {
				action = audit.ActionUpdate
			}
			if err := record(ctx, tx, "golf.tournament_team", tm.ID, t.Code+" "+tm.Code+" · "+tm.Name, action, property, before, tm, ""); err != nil {
				return tm, err
			}
			return tm, live(ctx, tx, property, tid, "teams", nil)
		}
	}
	return TournamentTeam{}, errs.NotFound("team")
}

// DeleteTeam removes a team (the players stay registered).
func (m *Module) DeleteTeam(ctx context.Context, tx pgx.Tx, property, tid, teamID uuid.UUID) error {
	t, err := editableTeams(ctx, tx, property, tid)
	if err != nil {
		return err
	}
	teams, err := loadTeams(ctx, tx, tid)
	if err != nil {
		return err
	}
	for _, tm := range teams {
		if tm.ID == teamID {
			if err := deleteTeam(ctx, tx, teamID); err != nil {
				return err
			}
			return record(ctx, tx, "golf.tournament_team", teamID, t.Code+" "+tm.Code+" · "+tm.Name, audit.ActionDelete, property, tm, nil, "")
		}
	}
	return errs.NotFound("team")
}

// TournamentAutoTeamsInput forms teams from the players without a team.
type TournamentAutoTeamsInput struct {
	Method string `json:"method,omitempty" enum:"balanced,registration_order" doc:"balanced (default): handicap snake draft so team handicaps are even"`
}

// formTeams splits players (ordered by handicap) into teams of size: a
// snake draft for balanced teams, else consecutive groups (pure).
func formTeams(n, size int, balanced bool) [][]int {
	if n < 2 || size < 2 {
		return nil
	}
	teams := (n + size - 1) / size
	if n/teams < 2 {
		teams = n / 2
	}
	out := make([][]int, teams)
	if !balanced {
		for i := 0; i < n; i++ {
			out[min(i/size, teams-1)] = append(out[min(i/size, teams-1)], i)
		}
		return out
	}
	for i := 0; i < n; i++ {
		round, pos := i/teams, i%teams
		if round%2 == 1 {
			pos = teams - 1 - pos
		}
		out[pos] = append(out[pos], i)
	}
	return out
}

// AutoTeams forms teams of the format's size from the players without a team.
func (m *Module) AutoTeams(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, in TournamentAutoTeamsInput) (TournamentTeamSetup, error) {
	t, err := editableTeams(ctx, tx, property, tid)
	if err != nil {
		return TournamentTeamSetup{}, err
	}
	_, f, err := teamSettings(ctx, tx, tid)
	if err != nil {
		return TournamentTeamSetup{}, err
	}
	if f == nil {
		return TournamentTeamSetup{}, errs.Conflict("no_team_format", "set the team format of "+t.Name+" first")
	}
	method := in.Method
	if method == "" {
		method = "balanced"
	}
	if err := oneOf("method", method, "balanced", "registration_order"); err != nil {
		return TournamentTeamSetup{}, err
	}
	order := `r.handicap_index NULLS LAST, r.registered_at`
	if method == "registration_order" {
		order = `r.registered_at`
	}
	rows, err := tx.Query(ctx, `SELECT r.id FROM golf.tournament_registrations r WHERE r.tournament_id = $1 AND r.status IN ('registered', 'checked_in')
		AND NOT EXISTS (SELECT 1 FROM golf.tournament_team_members m WHERE m.registration_id = r.id) ORDER BY `+order, tid)
	if err != nil {
		return TournamentTeamSetup{}, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return TournamentTeamSetup{}, err
	}
	groups := formTeams(len(ids), f.TeamSize, method == "balanced")
	if len(groups) == 0 {
		return TournamentTeamSetup{}, errs.Conflict("no_players", "at least two players without a team are needed")
	}
	made := 0
	for _, g := range groups {
		if len(g) < 2 || len(g) > f.TeamSize {
			continue
		}
		regs := make([]uuid.UUID, 0, len(g))
		for _, i := range g {
			regs = append(regs, ids[i])
		}
		code, err := nextTeamCode(ctx, tx, tid)
		if err != nil {
			return TournamentTeamSetup{}, err
		}
		nid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO golf.tournament_teams (id, property_id, tournament_id, code, name, captain_registration_id, created_by, updated_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$7)`, nid, property, tid, code, "Team "+code, regs[0], actor(ctx)); err != nil {
			return TournamentTeamSetup{}, err
		}
		if err := setMembers(ctx, tx, property, tid, nid, code, regs, f.TeamSize); err != nil {
			return TournamentTeamSetup{}, err
		}
		made++
	}
	out, err := m.TeamSetup(ctx, tx, property, tid)
	if err != nil {
		return out, err
	}
	if err := record(ctx, tx, "golf.tournament", tid, t.Code+" teams", "auto_teams", property, nil, map[string]any{"method": method, "teams": made,
		"unassigned": out.Unassigned}, ""); err != nil {
		return out, err
	}
	return out, live(ctx, tx, property, tid, "teams", nil)
}

// TournamentTeamScoreInput enters the team strokes of a one-ball format.
type TournamentTeamScoreInput struct {
	TeamID   uuid.UUID               `json:"teamId"`
	Round    int                     `json:"round,omitempty" doc:"Default: the current round"`
	Entries  []experience.ScoreEntry `json:"entries"`
	DeviceID string                  `json:"deviceId,omitempty"`
}

// TournamentTeamScoreResult is the team card after an entry.
type TournamentTeamScoreResult struct {
	TeamID     uuid.UUID             `json:"teamId"`
	Scorecards []TournamentScorecard `json:"scorecards" doc:"The members' cards carrying the team strokes"`
}

// EnterTeamScores writes the strokes of a one-ball team on every member's
// card (Scramble, Texas Scramble, Foursomes); Four-ball members enter their
// own scores.
func (m *Module) EnterTeamScores(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, in TournamentTeamScoreInput) (TournamentTeamScoreResult, error) {
	t, err := tournamentAt(ctx, tx, property, tid)
	if err != nil {
		return TournamentTeamScoreResult{}, err
	}
	_, f, err := teamSettings(ctx, tx, tid)
	if err != nil {
		return TournamentTeamScoreResult{}, err
	}
	if f == nil {
		return TournamentTeamScoreResult{}, errs.Conflict("no_team_format", t.Name+" is not a team tournament")
	}
	if !f.OneBall() {
		return TournamentTeamScoreResult{}, errs.Conflict("best_ball_scores", f.Name+": every player enters their own score")
	}
	if len(in.Entries) == 0 {
		return TournamentTeamScoreResult{}, handle.Invalid("entries", "required", "strokes per hole")
	}
	teams, err := loadTeams(ctx, tx, tid)
	if err != nil {
		return TournamentTeamScoreResult{}, err
	}
	out := TournamentTeamScoreResult{TeamID: in.TeamID, Scorecards: []TournamentScorecard{}}
	var team *TournamentTeam
	for i := range teams {
		if teams[i].ID == in.TeamID {
			team = &teams[i]
		}
	}
	if team == nil {
		return out, handle.Invalid("teamId", "not_found", "team of this tournament")
	}
	for _, mb := range team.Members {
		if mb.Status != "registered" && mb.Status != "checked_in" {
			continue
		}
		sc, err := m.EnterScores(ctx, tx, property, tid, TournamentScoreInput{RegistrationID: mb.RegistrationID, Round: in.Round, Entries: in.Entries,
			DeviceID: in.DeviceID, Source: "staff"}, scorerDesk, nil)
		if err != nil {
			return out, err
		}
		out.Scorecards = append(out.Scorecards, sc)
	}
	holes := make([]int, 0, len(in.Entries))
	for _, e := range in.Entries {
		holes = append(holes, e.Seq)
	}
	return out, record(ctx, tx, "golf.tournament_team", team.ID, t.Code+" "+team.Code+" · "+team.Name, "enter_scores", property, nil,
		map[string]any{"round": in.Round, "holes": holes, "cards": len(out.Scorecards)}, "")
}

// ── team leaderboard ───────────────────────────────────────────────────────

// TournamentTeamRound is a team's round on the leaderboard.
type TournamentTeamRound struct {
	Round  int  `json:"round"`
	Thru   int  `json:"thru"`
	Gross  *int `json:"gross"`
	Net    *int `json:"net"`
	Points *int `json:"points"`
	ToPar  int  `json:"toPar"`
}

// TournamentTeamEntry is a line of a team leaderboard.
type TournamentTeamEntry struct {
	Position      *int                  `json:"position"`
	PositionLabel string                `json:"positionLabel"`
	Tied          bool                  `json:"tied"`
	TeamID        uuid.UUID             `json:"teamId"`
	TeamCode      string                `json:"teamCode"`
	TeamName      string                `json:"teamName"`
	Players       []string              `json:"players"`
	TeamHandicap  *int                  `json:"teamHandicap"`
	Thru          string                `json:"thru"`
	ToPar         *int                  `json:"toPar"`
	Points        *int                  `json:"points"`
	Score         *int                  `json:"score"`
	Rounds        []TournamentTeamRound `json:"rounds"`
	Status        string                `json:"status" enum:"not_started,playing,finished,out"`

	members []TournamentTeamMember
	last    teamRound
}

// TournamentTeamBoard is the team leaderboard of a category.
type TournamentTeamBoard struct {
	Category string                `json:"category" enum:"gross,net,stableford"`
	Entries  []TournamentTeamEntry `json:"entries"`
}

// TournamentTeamLeaderboard is the live (or final) team leaderboard.
type TournamentTeamLeaderboard struct {
	TournamentID uuid.UUID             `json:"tournamentId"`
	Code         string                `json:"code"`
	Name         string                `json:"name"`
	Status       string                `json:"status"`
	Format       TeamFormatSpec        `json:"format"`
	Boards       []TournamentTeamBoard `json:"boards"`
}

// TeamLeaderboard computes the team leaderboard from the members' cards.
func (m *Module) TeamLeaderboard(ctx context.Context, q dbtx.Querier, property, tid uuid.UUID) (TournamentTeamLeaderboard, error) {
	t, err := tournamentAt(ctx, q, property, tid)
	if err != nil {
		return TournamentTeamLeaderboard{}, err
	}
	_, f, err := teamSettings(ctx, q, tid)
	if err != nil {
		return TournamentTeamLeaderboard{}, err
	}
	if f == nil {
		return TournamentTeamLeaderboard{}, errs.Conflict("no_team_format", t.Name+" is not a team tournament")
	}
	lb := TournamentTeamLeaderboard{TournamentID: tid, Code: t.Code, Name: t.Name, Status: t.Status, Format: *f, Boards: []TournamentTeamBoard{}}
	teams, err := loadTeams(ctx, q, tid)
	if err != nil {
		return lb, err
	}
	type score struct {
		Registration uuid.UUID `db:"registration_id"`
		RoundNo      int       `db:"round_no"`
		Scorecard    uuid.UUID `db:"scorecard_id"`
		Status       string    `db:"status"`
		Course       *int      `db:"course_handicap"`
	}
	scores, err := handle.List[score](q.Query(ctx, `SELECT s.registration_id, x.round_no, s.scorecard_id, s.status, s.course_handicap
		FROM golf.tournament_scores s JOIN golf.tournament_rounds x ON x.id = s.round_id WHERE s.tournament_id = $1 ORDER BY x.round_no`, tid))
	if err != nil {
		return lb, err
	}
	ids := make([]uuid.UUID, 0, len(scores))
	byReg := map[uuid.UUID]map[int]score{}
	rounds := map[int]bool{}
	for _, s := range scores {
		ids = append(ids, s.Scorecard)
		if byReg[s.Registration] == nil {
			byReg[s.Registration] = map[int]score{}
		}
		byReg[s.Registration][s.RoundNo] = s
		rounds[s.RoundNo] = true
	}
	cards := map[uuid.UUID]experience.Scorecard{}
	if m.Experience != nil {
		if cards, err = m.Experience.TournamentScorecards(ctx, q, ids); err != nil {
			return lb, err
		}
	}
	var roundNos []int
	for r := range rounds {
		roundNos = append(roundNos, r)
	}
	sort.Ints(roundNos)
	type teamCalc struct {
		entry  TournamentTeamEntry
		rounds []teamRound
		out    bool
	}
	var calcs []*teamCalc
	for _, tm := range teams {
		c := &teamCalc{entry: TournamentTeamEntry{TeamID: tm.ID, TeamCode: tm.Code, TeamName: tm.Name, Players: []string{}, Rounds: []TournamentTeamRound{},
			members: tm.Members}}
		for _, mb := range tm.Members {
			c.entry.Players = append(c.entry.Players, mb.PlayerName)
		}
		outCount := 0
		for _, rn := range roundNos {
			var course []*int
			var memberCards []experience.Scorecard
			var captainCard *experience.Scorecard
			active := 0
			for _, mb := range tm.Members {
				s, ok := byReg[mb.RegistrationID][rn]
				if !ok {
					continue
				}
				if s.Status == "dq" || s.Status == "wd" || s.Status == "nr" {
					continue
				}
				active++
				course = append(course, s.Course)
				card := cards[s.Scorecard]
				memberCards = append(memberCards, card)
				if captainCard == nil || (tm.CaptainRegistrationID != nil && *tm.CaptainRegistrationID == mb.RegistrationID) {
					cc := card
					captainCard = &cc
				}
			}
			if active == 0 {
				if len(tm.Members) > 0 {
					outCount++
				}
				continue
			}
			per, teamPH := TeamHandicaps(*f, course)
			var r teamRound
			if f.OneBall() {
				r = oneBallRound(*captainCard, teamPH)
				c.entry.TeamHandicap = ptr(teamPH)
			} else {
				r = bestBallRound(memberCards, per, f.ScoresPerHole)
			}
			c.rounds = append(c.rounds, r)
			tr := TournamentTeamRound{Round: rn, Thru: r.thru, ToPar: r.toPar}
			if r.complete {
				tr.Gross, tr.Net = ptr(r.gross), ptr(r.net)
			}
			if r.thru > 0 {
				tr.Points = ptr(r.points)
			}
			c.entry.Rounds = append(c.entry.Rounds, tr)
			c.entry.last = r
		}
		c.out = outCount > 0 && len(c.rounds) == 0
		calcs = append(calcs, c)
	}
	for _, cat := range categories(t) {
		type row struct {
			c       *teamCalc
			e       TournamentTeamEntry
			value   int
			group   int
			started bool
		}
		rows := make([]row, 0, len(calcs))
		for _, c := range calcs {
			e := c.entry
			v, score, toPar, points, started := teamMetric(c.rounds, cat)
			e.Score, e.ToPar, e.Points = score, toPar, points
			g := 0
			switch {
			case c.out:
				g, e.Status, e.PositionLabel = 2, "out", "OUT"
			case !started:
				g, e.Status, e.PositionLabel = 1, "not_started", "-"
			case c.entry.last.complete:
				e.Status = "finished"
			default:
				e.Status = "playing"
			}
			switch {
			case c.entry.last.thru == 0:
				e.Thru = "-"
			case c.entry.last.complete:
				e.Thru = "F"
			default:
				e.Thru = itoa(c.entry.last.thru)
			}
			rows = append(rows, row{c: c, e: e, value: v, group: g, started: started})
		}
		cmp := func(a, b row) int {
			if a.group != b.group {
				return a.group - b.group
			}
			if a.group != 0 {
				return 0
			}
			if a.value != b.value {
				return a.value - b.value
			}
			return teamCountback(a.c.entry.last, b.c.entry.last, cat)
		}
		sort.SliceStable(rows, func(i, j int) bool {
			if c := cmp(rows[i], rows[j]); c != 0 {
				return c < 0
			}
			return rows[i].e.TeamCode < rows[j].e.TeamCode
		})
		entries := make([]TournamentTeamEntry, len(rows))
		for i := range rows {
			e := rows[i].e
			if rows[i].group == 0 {
				pos := i + 1
				if i > 0 && rows[i-1].group == 0 && cmp(rows[i-1], rows[i]) == 0 {
					pos = *entries[i-1].Position
					e.Tied, entries[i-1].Tied = true, true
				}
				e.Position, e.PositionLabel = &pos, itoa(pos)
			}
			entries[i] = e
		}
		for i := range entries {
			if entries[i].Tied && entries[i].Position != nil {
				entries[i].PositionLabel = "T" + itoa(*entries[i].Position)
			}
		}
		lb.Boards = append(lb.Boards, TournamentTeamBoard{Category: cat, Entries: entries})
	}
	return lb, nil
}

// FreezeTeamResults stores the final team results of a completed team
// tournament (idempotent; golf.tournament_finalized subscriber).
func (m *Module) FreezeTeamResults(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID) (int, error) {
	_, f, err := teamSettings(ctx, tx, tid)
	if err != nil || f == nil {
		return 0, err
	}
	lb, err := m.TeamLeaderboard(ctx, tx, property, tid)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM golf.tournament_team_results WHERE tournament_id = $1`, tid); err != nil {
		return 0, err
	}
	n := 0
	for _, b := range lb.Boards {
		for _, e := range b.Entries {
			members := make([]map[string]any, 0, len(e.members))
			for _, mb := range e.members {
				members = append(members, map[string]any{"registrationId": mb.RegistrationID, "customerId": mb.CustomerID, "playerName": mb.PlayerName,
					"consent": mb.Consent})
			}
			score := e.Score
			if b.Category == "stableford" {
				score = e.Points
			}
			var toPar *int
			if b.Category != "stableford" {
				toPar = e.ToPar
			}
			if _, err := tx.Exec(ctx, `INSERT INTO golf.tournament_team_results (id, property_id, tournament_id, team_id, team_name, members, category, position,
				position_label, tied, score, to_par, team_handicap) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, id.New(), property, tid, e.TeamID,
				e.TeamName, jsonOf(members), b.Category, e.Position, e.PositionLabel, e.Tied, score, toPar, e.TeamHandicap); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// TournamentTeamResult is a frozen team result.
type TournamentTeamResult struct {
	TeamID        uuid.UUID       `json:"teamId" db:"team_id"`
	TeamName      string          `json:"teamName" db:"team_name"`
	Members       json.RawMessage `json:"members" db:"members"`
	Category      string          `json:"category" db:"category" enum:"gross,net,stableford"`
	Position      *int            `json:"position" db:"position"`
	PositionLabel string          `json:"positionLabel" db:"position_label"`
	Tied          bool            `json:"tied" db:"tied"`
	Score         *int            `json:"score" db:"score"`
	ToPar         *int            `json:"toPar" db:"to_par"`
	TeamHandicap  *int            `json:"teamHandicap" db:"team_handicap"`
}

func (m *Module) registerTeams(reg *route.Registry) {
	db := m.DB
	tid := func(ctx context.Context, tx pgx.Tx, r *http.Request) (uuid.UUID, error) {
		return tournamentID(ctx, tx, r)
	}
	m.add(reg, route.Route{Method: http.MethodGet, Path: base + "/{id}/team-setup", Summary: "Team format and teams of a tournament (team handicaps)",
		Permission: "golf.tournament.view", Response: TournamentTeamSetup{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (TournamentTeamSetup, error) {
			t, err := handle.ID(r)
			if err != nil {
				return TournamentTeamSetup{}, err
			}
			return m.TeamSetup(ctx, tx, handle.Property(ctx), t)
		})})
	m.add(reg, route.Route{Method: http.MethodPut, Path: base + "/{id}/team-setup", Summary: "Set the team format (Scramble, Four-ball, Foursomes, Texas Scramble)",
		Permission: "golf.tournament_team.manage", Request: TournamentTeamSetupInput{}, Response: TournamentTeamSetup{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentTeamSetupInput) (TournamentTeamSetup, error) {
			t, err := handle.ID(r)
			if err != nil {
				return TournamentTeamSetup{}, err
			}
			return m.SetTeamFormat(ctx, tx, handle.Property(ctx), t, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: base + "/{id}/teams", Summary: "Add a team (players draw together)",
		Permission: "golf.tournament_team.manage", Request: TournamentTeamInput{}, Response: TournamentTeam{}, Status: http.StatusCreated,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentTeamInput) (TournamentTeam, error) {
			t, err := handle.ID(r)
			if err != nil {
				return TournamentTeam{}, err
			}
			return m.SaveTeam(ctx, tx, handle.Property(ctx), t, nil, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: base + "/{id}/teams:auto", Summary: "Form teams from the players without a team (balanced by handicap)",
		Permission: "golf.tournament_team.manage", Request: TournamentAutoTeamsInput{}, Response: TournamentTeamSetup{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentAutoTeamsInput) (TournamentTeamSetup, error) {
			t, err := handle.ID(r)
			if err != nil {
				return TournamentTeamSetup{}, err
			}
			return m.AutoTeams(ctx, tx, handle.Property(ctx), t, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPatch, Path: base + "/{id}/teams/{teamId}", Summary: "Change a team: name, members, captain",
		Permission: "golf.tournament_team.manage", Request: TournamentTeamInput{}, Response: TournamentTeam{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentTeamInput) (TournamentTeam, error) {
			t, err := handle.ID(r)
			if err != nil {
				return TournamentTeam{}, err
			}
			team, err := pathID(r, "teamId")
			if err != nil {
				return TournamentTeam{}, err
			}
			return m.SaveTeam(ctx, tx, handle.Property(ctx), t, &team, in)
		})})
	m.add(reg, route.Route{Method: http.MethodDelete, Path: base + "/{id}/teams/{teamId}", Summary: "Delete a team (players stay registered)",
		Permission: "golf.tournament_team.manage",
		Handler: m.write(http.StatusNoContent, func(ctx context.Context, tx pgx.Tx, r *http.Request) (any, error) {
			t, err := handle.ID(r)
			if err != nil {
				return nil, err
			}
			team, err := pathID(r, "teamId")
			if err != nil {
				return nil, err
			}
			return nil, m.DeleteTeam(ctx, tx, handle.Property(ctx), t, team)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: base + "/{id}/team-scores", Summary: "Enter the team strokes of a one-ball format (on every member's card)",
		Permission: "golf.tournament_score.enter", Request: TournamentTeamScoreInput{}, Response: TournamentTeamScoreResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentTeamScoreInput) (TournamentTeamScoreResult, error) {
			t, err := tid(ctx, tx, r)
			if err != nil {
				return TournamentTeamScoreResult{}, err
			}
			return m.EnterTeamScores(ctx, tx, handle.Property(ctx), t, in)
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: base + "/{id}/team-leaderboard", Summary: "Team leaderboard (team handicap, best ball, countback)",
		Permission: "golf.tournament_leaderboard.view", Response: TournamentTeamLeaderboard{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (TournamentTeamLeaderboard, error) {
			t, err := handle.ID(r)
			if err != nil {
				return TournamentTeamLeaderboard{}, err
			}
			return m.TeamLeaderboard(ctx, tx, handle.Property(ctx), t)
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: base + "/{id}/team-results", Summary: "Final team results", Permission: "golf.tournament.view",
		Response: TournamentTeamResult{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[TournamentTeamResult], error) {
			t, err := tid(ctx, tx, r)
			if err != nil {
				return httpx.Page[TournamentTeamResult]{}, err
			}
			return handle.Page(handle.List[TournamentTeamResult](tx.Query(ctx, `SELECT team_id, team_name, members, category, position, position_label, tied,
				score, to_par, team_handicap FROM golf.tournament_team_results WHERE tournament_id = $1 ORDER BY category, position NULLS LAST, team_name`, t)))
		})})
}
