package tournament

// FR-TRN-11 Finalize: results are locked (positions per category and
// division with countback), ranking prizes and the Hole-in-One award are
// given, the Hall of Fame receives the Tournament / Club Champions (public
// only with the player's consent, P2), participants are notified and
// golf.tournament_results_published is published (docs/p3-p4-contracts.md).

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/golf/experience"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/handle"
)

// TournamentResult is a frozen result line.
type TournamentResult struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	RegistrationID *uuid.UUID `json:"registrationId" db:"registration_id" doc:"Null for imported history"`
	CustomerID     *uuid.UUID `json:"customerId" db:"customer_id"`
	PlayerName     string     `json:"playerName" db:"player_name"`
	DivisionID     *uuid.UUID `json:"divisionId" db:"division_id" doc:"Null: overall"`
	DivisionName   *string    `json:"divisionName" db:"division_name"`
	Category       string     `json:"category" db:"category" enum:"gross,net,stableford"`
	Position       *int       `json:"position" db:"position"`
	PositionLabel  string     `json:"positionLabel" db:"position_label"`
	Tied           bool       `json:"tied" db:"tied"`
	Score          *int       `json:"score" db:"score"`
	ToPar          *int       `json:"toPar" db:"to_par"`
	TieBreak       *string    `json:"tieBreak" db:"tie_break"`
}

// Champion is a winner in the results-published event.
type TournamentChampion struct {
	Division   string     `json:"division"`
	Category   string     `json:"category" enum:"gross,net,stableford"`
	CustomerID *uuid.UUID `json:"customerId"`
	PlayerName string     `json:"playerName"`
	Score      *int       `json:"score"`
}

// TournamentResults are the final results with awards.
type TournamentResults struct {
	TournamentID uuid.UUID            `json:"tournamentId"`
	Code         string               `json:"code"`
	Name         string               `json:"name"`
	Status       string               `json:"status"`
	FinalizedAt  *time.Time           `json:"finalizedAt"`
	Champions    []TournamentChampion `json:"champions"`
	Results      []TournamentResult   `json:"results"`
	Awards       []TournamentPrize    `json:"awards" doc:"Prizes with their recipients"`
}

const resultSelect = `SELECT x.id, x.registration_id, x.customer_id, x.player_name, x.division_id, coalesce(d.name, x.division_label) AS division_name,
	x.category, x.position, x.position_label, x.tied, x.score, x.to_par, x.tie_break FROM golf.tournament_results x
	LEFT JOIN golf.tournament_divisions d ON d.id = x.division_id`

// Results returns the frozen results of a tournament (empty before Finalize).
func (m *Module) Results(ctx context.Context, q dbtx.Querier, property, tid uuid.UUID) (TournamentResults, error) {
	t, err := tournamentAt(ctx, q, property, tid)
	if err != nil {
		return TournamentResults{}, err
	}
	out := TournamentResults{TournamentID: tid, Code: t.Code, Name: t.Name, Status: t.Status, FinalizedAt: t.FinalizedAt, Champions: []TournamentChampion{}}
	if out.Results, err = handle.List[TournamentResult](q.Query(ctx, resultSelect+` WHERE x.tournament_id = $1
		ORDER BY x.category, (x.division_id IS NOT NULL OR x.division_label IS NOT NULL), d.sequence, x.division_label, x.position NULLS LAST, x.player_name`, tid)); err != nil {
		return out, err
	}
	prizes, err := listPrizes(ctx, q, tid)
	if err != nil {
		return out, err
	}
	out.Awards = []TournamentPrize{}
	for _, p := range prizes {
		if p.RegistrationID != nil {
			out.Awards = append(out.Awards, p)
		}
	}
	out.Champions, err = champions(ctx, q, tid)
	return out, err
}

func champions(ctx context.Context, q dbtx.Querier, tid uuid.UUID) ([]TournamentChampion, error) {
	type row struct {
		Division   *string    `db:"division_name"`
		Category   string     `db:"category"`
		CustomerID *uuid.UUID `db:"customer_id"`
		PlayerName string     `db:"player_name"`
		Score      *int       `db:"score"`
	}
	rows, err := handle.List[row](q.Query(ctx, `SELECT coalesce(d.name, x.division_label) AS division_name, x.category, x.customer_id, x.player_name, x.score
		FROM golf.tournament_results x LEFT JOIN golf.tournament_divisions d ON d.id = x.division_id
		WHERE x.tournament_id = $1 AND x.position = 1 ORDER BY x.category, (x.division_id IS NOT NULL OR x.division_label IS NOT NULL), d.sequence,
		x.division_label, x.player_name`, tid))
	out := []TournamentChampion{}
	for _, r := range rows {
		div := "Overall"
		if r.Division != nil {
			div = *r.Division
		}
		out = append(out, TournamentChampion{Division: div, Category: r.Category, CustomerID: r.CustomerID, PlayerName: r.PlayerName, Score: r.Score})
	}
	return out, err
}

// Finalize locks the results of a tournament whose last round is complete.
func (m *Module) Finalize(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID) (TournamentResults, error) {
	t, err := lockTournament(ctx, tx, property, tid)
	if err != nil {
		return TournamentResults{}, err
	}
	if t.Status != "in_progress" {
		return TournamentResults{}, errs.Conflict("invalid_status", "only a tournament in progress is finalized, not "+t.Status)
	}
	rounds, err := listRounds(ctx, tx, tid)
	if err != nil {
		return TournamentResults{}, err
	}
	last := rounds[len(rounds)-1]
	if last.Status != "completed" {
		var pending []string
		rows, err := tx.Query(ctx, `SELECT r.player_name FROM golf.tournament_flight_players fp JOIN golf.tournament_registrations r ON r.id = fp.registration_id
			LEFT JOIN golf.tournament_scores s ON s.round_id = fp.round_id AND s.registration_id = fp.registration_id
			WHERE fp.round_id = $1 AND (s.id IS NULL OR s.status NOT IN ('finalized', 'dq', 'wd', 'nr')) ORDER BY r.player_name LIMIT 10`, last.ID)
		if err != nil {
			return TournamentResults{}, err
		}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				rows.Close()
				return TournamentResults{}, err
			}
			pending = append(pending, n)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return TournamentResults{}, err
		}
		msg := fmt.Sprintf("round %d is not complete", last.RoundNo)
		if len(pending) > 0 {
			msg += ": cards to validate — " + strings.Join(pending, ", ")
		}
		return TournamentResults{}, errs.Conflict("round_not_complete", msg)
	}
	lb, err := m.compute(ctx, tx, property, t, computeOptions{})
	if err != nil {
		return TournamentResults{}, err
	}
	pol, _, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return TournamentResults{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM golf.tournament_results WHERE tournament_id = $1`, tid); err != nil {
		return TournamentResults{}, err
	}
	resultID := map[string]uuid.UUID{} // category|division|registration → result
	for _, b := range lb.Boards {
		for _, e := range b.Entries {
			if e.RegistrationID == nil {
				continue
			}
			var rounds []map[string]any
			for _, r := range e.Rounds {
				rounds = append(rounds, map[string]any{"round": r.Round, "gross": r.Gross, "net": r.Net, "points": r.Points, "status": r.Status})
			}
			score := e.Score
			var toPar *int
			if b.Category != "stableford" {
				toPar = e.ToPar
			} else if e.Points != nil {
				score = e.Points
			}
			rid := id.New()
			if _, err := tx.Exec(ctx, `INSERT INTO golf.tournament_results (id, property_id, tournament_id, registration_id, customer_id, player_name, division_id,
				category, position, position_label, tied, score, to_par, tie_break, rounds) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, rid, property,
				tid, *e.RegistrationID, e.customerID, e.PlayerName, b.DivisionID, b.Category, e.Position, e.PositionLabel, e.Tied, score, toPar, e.TieBreak,
				jsonOf(rounds)); err != nil {
				return TournamentResults{}, err
			}
			resultID[resultKey(b.Category, b.DivisionID, *e.RegistrationID)] = rid
		}
	}
	if err := m.awardPrizes(ctx, tx, property, t, lb, pol); err != nil {
		return TournamentResults{}, err
	}
	if err := m.hallOfFame(ctx, tx, property, t, lb, resultID); err != nil {
		return TournamentResults{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournaments SET status = 'completed', finalized_at = now(), finalized_by = $2, updated_by = $2 WHERE id = $1`,
		tid, actor(ctx)); err != nil {
		return TournamentResults{}, err
	}
	// the course blocks of played rounds stay (history); none is open any more
	out, err := m.Results(ctx, tx, property, tid)
	if err != nil {
		return out, err
	}
	champs := make([]map[string]any, 0, len(out.Champions))
	for _, c := range out.Champions {
		champs = append(champs, map[string]any{"division": c.Division, "category": c.Category, "customerId": c.CustomerID, "playerName": c.PlayerName,
			"score": c.Score})
	}
	if err := m.publish(ctx, tx, EventFinalized, "golf.tournament", tid, property, map[string]any{"tournamentId": tid, "code": t.Code,
		"finalizedAt": out.FinalizedAt}); err != nil {
		return out, err
	}
	if err := m.publish(ctx, tx, EventResultsPublished, "golf.tournament", tid, property, map[string]any{"tournamentId": tid, "code": t.Code, "name": t.Name,
		"endDate": t.EndDate, "champions": champs}); err != nil {
		return out, err
	}
	if err := m.notifyResults(ctx, tx, property, t, lb); err != nil {
		return out, err
	}
	if err := record(ctx, tx, "golf.tournament", tid, t.Code, "finalize", property, map[string]any{"status": t.Status},
		map[string]any{"status": "completed", "results": len(out.Results), "awards": len(out.Awards), "champions": len(out.Champions)}, ""); err != nil {
		return out, err
	}
	return out, live(ctx, tx, property, tid, "finalized", nil)
}

func resultKey(cat string, div *uuid.UUID, reg uuid.UUID) string {
	d := "overall"
	if div != nil {
		d = div.String()
	}
	return cat + "|" + d + "|" + reg.String()
}

// awardPrizes gives the ranking prizes (gross before net / stableford,
// overall before divisions; with One Prize per Player a winner is skipped
// by later prizes) and the Hole-in-One prize of a hole aced in the event.
func (m *Module) awardPrizes(ctx context.Context, tx pgx.Tx, property uuid.UUID, t Tournament, lb TournamentLeaderboard, pol TournamentPolicy) error {
	prizes, err := listPrizes(ctx, tx, t.ID)
	if err != nil {
		return err
	}
	sort.SliceStable(prizes, func(i, j int) bool {
		pi, pj := categoryOrder(prizes[i].Category), categoryOrder(prizes[j].Category)
		if pi != pj {
			return pi < pj
		}
		if (prizes[i].DivisionID == nil) != (prizes[j].DivisionID == nil) {
			return prizes[i].DivisionID == nil
		}
		return derefInt(prizes[i].Position) < derefInt(prizes[j].Position)
	})
	boards := map[string][]TournamentLeaderboardEntry{}
	for _, b := range lb.Boards {
		d := "overall"
		if b.DivisionID != nil {
			d = b.DivisionID.String()
		}
		boards[b.Category+"|"+d] = b.Entries
	}
	won := map[uuid.UUID]bool{}
	next := map[string]int{}
	for _, p := range prizes {
		if p.Status != "open" || p.RegistrationID != nil {
			continue
		}
		var winner *uuid.UUID
		var result string
		switch {
		case isRanking(p.Category):
			d := "overall"
			if p.DivisionID != nil {
				d = p.DivisionID.String()
			}
			key := p.Category + "|" + d
			entries := boards[key]
			for i := next[key]; i < len(entries); i++ {
				e := entries[i]
				next[key] = i + 1
				if e.Position == nil || e.RegistrationID == nil || (pol.OnePrizePerPlayer && won[*e.RegistrationID]) {
					continue
				}
				winner = e.RegistrationID
				switch {
				case p.Category == "stableford" && e.Points != nil:
					result = fmt.Sprintf("%d pts", *e.Points)
				case e.Score != nil:
					result = fmt.Sprintf("%d", *e.Score)
				}
				if e.TieBreak != nil {
					result += " (" + *e.TieBreak + ")"
				}
				break
			}
		case p.Category == "hole_in_one" && p.HoleNumber != nil:
			var rid uuid.UUID
			err := tx.QueryRow(ctx, `SELECT s.registration_id FROM golf.tournament_scores s JOIN golf.scorecard_holes h ON h.scorecard_id = s.scorecard_id
				WHERE s.tournament_id = $1 AND h.hole_number = $2 AND h.strokes = 1 AND s.status = 'finalized' ORDER BY h.entered_at LIMIT 1`, t.ID, *p.HoleNumber).Scan(&rid)
			if err == nil {
				winner, result = &rid, fmt.Sprintf("Hole-in-One on hole %d", *p.HoleNumber)
			} else if !dbtx.IsNoRows(err) {
				return err
			}
		}
		if winner == nil {
			continue
		}
		won[*winner] = true
		if _, err := tx.Exec(ctx, `UPDATE golf.tournament_prizes SET registration_id = $2, result_text = $3, status = 'awarded', awarded_at = now(), awarded_by = $4
			WHERE id = $1`, p.ID, *winner, nullStr(result), actor(ctx)); err != nil {
			return err
		}
		if err := record(ctx, tx, "golf.tournament_prize", p.ID, t.Code+" · "+p.Name, "award", property, nil,
			map[string]any{"registrationId": winner, "result": result}, "finalize"); err != nil {
			return err
		}
	}
	return nil
}

func categoryOrder(c string) int {
	switch c {
	case "gross":
		return 0
	case "net", "stableford":
		return 1
	}
	return 2
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// hallOfFame creates the champion entries: the winners (position 1) of the
// overall boards (Tournament Champion; Club Champion for a club
// championship) and of divisions mapped to a Hall of Fame division.
func (m *Module) hallOfFame(ctx context.Context, tx pgx.Tx, property uuid.UUID, t Tournament, lb TournamentLeaderboard, results map[string]uuid.UUID) error {
	if m.Experience == nil {
		return nil
	}
	divs, err := listDivisions(ctx, tx, t.ID)
	if err != nil {
		return err
	}
	hof := map[uuid.UUID]*string{}
	for _, d := range divs {
		hof[d.ID] = d.HallOfFameDivision
	}
	end, _ := time.Parse("2006-01-02", t.EndDate)
	category := "tournament_champion"
	if t.TournamentType == "club_championship" {
		category = "club_champion"
	}
	consent := map[uuid.UUID]bool{}
	for _, b := range lb.Boards {
		for _, e := range b.Entries {
			if e.RegistrationID != nil {
				consent[*e.RegistrationID] = e.consent
			}
		}
	}
	for _, b := range lb.Boards {
		var division *string
		if b.DivisionID != nil {
			division = hof[*b.DivisionID]
			if division == nil {
				continue // a division not kept in the Hall of Fame
			}
		} else if t.TournamentType == "club_championship" {
			division = ptr("open")
		}
		for _, e := range b.Entries {
			if e.Position == nil || *e.Position != 1 || e.RegistrationID == nil {
				continue
			}
			label := map[string]string{"gross": "Gross Champion", "net": "Net Champion", "stableford": "Stableford Champion"}[b.Category]
			title := fmt.Sprintf("%s %d — %s", t.Name, end.Year(), label)
			if b.DivisionID != nil {
				title = fmt.Sprintf("%s %d — %s %s", t.Name, end.Year(), b.Division, label)
			}
			score := e.Score
			if b.Category == "stableford" {
				score = e.Points
			}
			cust := e.customerID
			if _, err := m.Experience.TournamentHallOfFameEntry(ctx, tx, property, experience.TournamentHallOfFameInput{Category: category, Title: title,
				Year: end.Year(), Division: division, CustomerID: &cust, PlayerName: e.PlayerName, Score: score, AchievedOn: end,
				SourceID: results[resultKey(b.Category, b.DivisionID, *e.RegistrationID)], Consent: consent[*e.RegistrationID]}); err != nil {
				return err
			}
		}
	}
	return nil
}

// notifyResults tells every finisher the final position (primary category).
func (m *Module) notifyResults(ctx context.Context, tx pgx.Tx, property uuid.UUID, t Tournament, lb TournamentLeaderboard) error {
	primary := primaryCategory(t)
	for _, b := range lb.Boards {
		if b.Category != primary || b.DivisionID != nil {
			continue
		}
		for _, e := range b.Entries {
			if e.RegistrationID == nil || e.Status == "not_started" {
				continue
			}
			var email *string
			if err := tx.QueryRow(ctx, `SELECT email FROM golf.tournament_registrations WHERE id = $1`, *e.RegistrationID).Scan(&email); err != nil {
				return err
			}
			user, err := customerUser(ctx, tx, e.customerID)
			if err != nil {
				return err
			}
			score := "-"
			switch {
			case b.Category == "stableford" && e.Points != nil:
				score = fmt.Sprintf("%d points", *e.Points)
			case e.Score != nil:
				score = fmt.Sprintf("%d", *e.Score)
			}
			if err := m.notifyCustomer(ctx, tx, property, deref(email), e.PlayerName, user, "golf.tournament_results_published", map[string]any{
				"name": e.PlayerName, "tournament": t.Name, "position": e.PositionLabel, "category": b.Category, "score": score}); err != nil {
				return err
			}
		}
	}
	return nil
}
