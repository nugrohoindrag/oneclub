package tournament

// PRD P5 FR-TRN-P5-04 Tournament History: the archive of completed and
// imported tournaments (P3 FR-MIG-P3-03 import) with their champions,
// player statistics across tournaments (events, wins, top 3 / 10, best and
// average scores) and the champion history per tournament and Order of
// Merit season (completing the P2 Hall of Fame). Reads the view
// reporting.golf_tournament_history.

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

// TournamentHistoryChampion is a winner of an archived tournament.
type TournamentHistoryChampion struct {
	Category   string     `json:"category" enum:"gross,net,stableford"`
	PlayerName string     `json:"playerName"`
	CustomerID *uuid.UUID `json:"customerId"`
	Score      *int       `json:"score"`
}

// TournamentHistoryItem is a tournament of the archive.
type TournamentHistoryItem struct {
	TournamentID   uuid.UUID                   `json:"tournamentId" db:"tournament_id"`
	Code           string                      `json:"code" db:"code"`
	Name           string                      `json:"name" db:"tournament_name"`
	TournamentType string                      `json:"tournamentType" db:"tournament_type"`
	Format         string                      `json:"format" db:"format"`
	StartDate      string                      `json:"startDate" db:"start_date"`
	EndDate        string                      `json:"endDate" db:"end_date"`
	Source         string                      `json:"source" db:"source" enum:"oneclub,import"`
	Players        int                         `json:"players" db:"players"`
	TeamFormat     *string                     `json:"teamFormat" db:"team_format"`
	Series         []string                    `json:"series" db:"series"`
	Champions      []TournamentHistoryChampion `json:"champions" db:"-"`
}

// TournamentPlayerStats are a player's statistics across tournaments.
type TournamentPlayerStats struct {
	PlayerKey      string     `json:"playerKey" db:"player_key" doc:"Customer id, else n:<name> (imported history)"`
	CustomerID     *uuid.UUID `json:"customerId" db:"customer_id"`
	PlayerName     string     `json:"playerName" db:"player_name"`
	Events         int        `json:"events" db:"events"`
	Wins           int        `json:"wins" db:"wins"`
	Top3           int        `json:"top3" db:"top3"`
	Top10          int        `json:"top10" db:"top10"`
	BestGross      *int       `json:"bestGross" db:"best_gross"`
	AverageGross   *string    `json:"averageGross" db:"average_gross"`
	BestStableford *int       `json:"bestStableford" db:"best_stableford"`
	FirstPlayed    string     `json:"firstPlayed" db:"first_played"`
	LastPlayed     string     `json:"lastPlayed" db:"last_played"`
}

// TournamentPlayerResult is one result of a player's history.
type TournamentPlayerResult struct {
	TournamentID   uuid.UUID `json:"tournamentId" db:"tournament_id"`
	Code           string    `json:"code" db:"code"`
	Name           string    `json:"name" db:"tournament_name"`
	EndDate        string    `json:"endDate" db:"end_date"`
	Source         string    `json:"source" db:"source"`
	Category       string    `json:"category" db:"category"`
	Division       *string   `json:"division" db:"division"`
	Position       *int      `json:"position" db:"position"`
	PositionLabel  string    `json:"positionLabel" db:"position_label"`
	Score          *int      `json:"score" db:"score"`
	ToPar          *int      `json:"toPar" db:"to_par"`
	TeamName       *string   `json:"teamName" db:"team_name"`
	TournamentType string    `json:"tournamentType" db:"tournament_type"`
}

// TournamentPlayerSeries is a player's Order of Merit result.
type TournamentPlayerSeries struct {
	SeriesID      uuid.UUID `json:"seriesId" db:"series_id"`
	SeriesName    string    `json:"seriesName" db:"series_name"`
	Season        int       `json:"season" db:"season"`
	PositionLabel string    `json:"positionLabel" db:"position_label"`
	Points        string    `json:"points" db:"points"`
	Events        int       `json:"events" db:"events"`
	Final         bool      `json:"final" db:"final"`
}

// TournamentPlayerHistory is the history of one player.
type TournamentPlayerHistory struct {
	Stats   TournamentPlayerStats    `json:"stats"`
	Results []TournamentPlayerResult `json:"results"`
	Series  []TournamentPlayerSeries `json:"series"`
}

// TournamentChampionLine is a line of the champion history.
type TournamentChampionLine struct {
	Year       int        `json:"year" db:"year"`
	Event      string     `json:"event" db:"event" doc:"Tournament or Order of Merit"`
	Kind       string     `json:"kind" db:"kind" enum:"tournament,order_of_merit"`
	Category   string     `json:"category" db:"category"`
	Division   *string    `json:"division" db:"division"`
	PlayerName string     `json:"playerName" db:"player_name"`
	CustomerID *uuid.UUID `json:"customerId" db:"customer_id"`
	Score      *string    `json:"score" db:"score"`
	EventID    uuid.UUID  `json:"eventId" db:"event_id"`
}

// HistoryFilter filters the archive.
type HistoryFilter struct {
	From, To, Type, Search string
	Limit                  int
}

// History lists the archived tournaments with their champions.
func History(ctx context.Context, q dbtx.Querier, property uuid.UUID, f HistoryFilter) ([]TournamentHistoryItem, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 200
	}
	items, err := handle.List[TournamentHistoryItem](q.Query(ctx, `SELECT t.id AS tournament_id, t.code, t.name AS tournament_name, t.tournament_type, t.format,
		t.start_date::text AS start_date, t.end_date::text AS end_date, t.source,
		(SELECT count(DISTINCT coalesce(x.customer_id::text, lower(x.player_name))) FROM golf.tournament_results x WHERE x.tournament_id = t.id)::int AS players,
		ts.format_snapshot->>'name' AS team_format,
		coalesce((SELECT array_agg(s.name || ' ' || s.season ORDER BY s.season) FROM golf.tournament_series_events e JOIN golf.tournament_series s ON s.id = e.series_id
		  WHERE e.tournament_id = t.id AND s.status <> 'cancelled'), '{}') AS series
		FROM golf.tournaments t LEFT JOIN golf.tournament_team_settings ts ON ts.tournament_id = t.id
		WHERE t.property_id = $1 AND t.status = 'completed' AND ($2 = '' OR t.end_date >= $2::date) AND ($3 = '' OR t.start_date <= $3::date)
		AND ($4 = '' OR t.tournament_type = $4) AND ($5 = '' OR t.name ILIKE '%' || $5 || '%' OR t.code ILIKE '%' || $5 || '%')
		ORDER BY t.end_date DESC, t.code LIMIT $6`, property, f.From, f.To, f.Type, f.Search, f.Limit))
	if err != nil || len(items) == 0 {
		return items, err
	}
	ids := make([]uuid.UUID, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.TournamentID)
	}
	type champ struct {
		TournamentID uuid.UUID `db:"tournament_id"`
		TournamentHistoryChampion
	}
	cs, err := handle.List[champ](q.Query(ctx, `SELECT tournament_id, category, player_name, customer_id, score FROM reporting.golf_tournament_history
		WHERE tournament_id = ANY($1) AND position = 1 AND division IS NULL ORDER BY category, player_name`, ids))
	if err != nil {
		return items, err
	}
	by := map[uuid.UUID][]TournamentHistoryChampion{}
	for _, c := range cs {
		by[c.TournamentID] = append(by[c.TournamentID], c.TournamentHistoryChampion)
	}
	for i := range items {
		items[i].Champions = by[items[i].TournamentID]
		if items[i].Champions == nil {
			items[i].Champions = []TournamentHistoryChampion{}
		}
		if items[i].Series == nil {
			items[i].Series = []string{}
		}
	}
	return items, nil
}

const playerStatsSelect = `SELECT player_key, (array_agg(customer_id) FILTER (WHERE customer_id IS NOT NULL))[1] AS customer_id,
	(array_agg(player_name ORDER BY end_date DESC))[1] AS player_name, count(DISTINCT tournament_id)::int AS events,
	count(DISTINCT tournament_id) FILTER (WHERE position = 1 AND division IS NULL)::int AS wins,
	count(DISTINCT tournament_id) FILTER (WHERE position <= 3 AND division IS NULL)::int AS top3,
	count(DISTINCT tournament_id) FILTER (WHERE position <= 10 AND division IS NULL)::int AS top10,
	min(score) FILTER (WHERE category = 'gross' AND format = 'stroke_play') AS best_gross,
	trim_scale(round(avg(score) FILTER (WHERE category = 'gross' AND format = 'stroke_play' AND division IS NULL), 1))::text AS average_gross,
	max(score) FILTER (WHERE category = 'stableford') AS best_stableford, min(end_date)::text AS first_played, max(end_date)::text AS last_played
	FROM reporting.golf_tournament_history WHERE property_id = $1`

// PlayerStats lists players with their statistics (search by name).
func PlayerStats(ctx context.Context, q dbtx.Querier, property uuid.UUID, search string, limit int) ([]TournamentPlayerStats, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return handle.List[TournamentPlayerStats](q.Query(ctx, playerStatsSelect+` AND ($2 = '' OR player_name ILIKE '%' || $2 || '%')
		GROUP BY player_key ORDER BY count(DISTINCT tournament_id) FILTER (WHERE position = 1 AND division IS NULL) DESC, count(DISTINCT tournament_id) DESC,
		3 LIMIT $3`, property, strings.TrimSpace(search), limit))
}

// PlayerHistory is the history of one player (by customer, else by name).
func PlayerHistory(ctx context.Context, q dbtx.Querier, property uuid.UUID, customer *uuid.UUID, name string) (TournamentPlayerHistory, error) {
	key := playerKey(customer, name)
	out := TournamentPlayerHistory{Results: []TournamentPlayerResult{}, Series: []TournamentPlayerSeries{}}
	stats, err := handle.List[TournamentPlayerStats](q.Query(ctx, playerStatsSelect+` AND player_key = $2 GROUP BY player_key`, property, key))
	if err != nil {
		return out, err
	}
	if len(stats) == 1 {
		out.Stats = stats[0]
	} else {
		out.Stats = TournamentPlayerStats{PlayerKey: key, CustomerID: customer, PlayerName: name}
	}
	if out.Results, err = handle.List[TournamentPlayerResult](q.Query(ctx, `SELECT h.tournament_id, h.code, h.tournament_name, h.end_date::text AS end_date,
		h.source, h.category, h.division, h.position, h.position_label, h.score, h.to_par, h.tournament_type, NULL::text AS team_name
		FROM reporting.golf_tournament_history h WHERE h.property_id = $1 AND h.player_key = $2
		UNION ALL
		SELECT t.id, t.code, t.name, t.end_date::text, t.source, r.category, NULL::text, r.position, r.position_label, r.score, r.to_par, t.tournament_type, r.team_name
		FROM golf.tournament_team_results r JOIN golf.tournaments t ON t.id = r.tournament_id CROSS JOIN LATERAL jsonb_array_elements(r.members) mb
		WHERE t.property_id = $1 AND coalesce(mb->>'customerId', 'n:' || lower(mb->>'playerName')) = $2
		ORDER BY 4 DESC, 6, 7 NULLS FIRST`, property, key)); err != nil {
		return out, err
	}
	if out.Results == nil {
		out.Results = []TournamentPlayerResult{}
	}
	out.Series, err = handle.List[TournamentPlayerSeries](q.Query(ctx, `SELECT series_id, series_name, season, position_label, trim_scale(points)::text AS points,
		events, final FROM reporting.golf_series_standings WHERE property_id = $1 AND player_key = $2 ORDER BY season DESC, series_name`, property, key))
	if out.Series == nil {
		out.Series = []TournamentPlayerSeries{}
	}
	return out, err
}

// ChampionHistory lists the champions of tournaments and Order of Merit seasons.
func ChampionHistory(ctx context.Context, q dbtx.Querier, property uuid.UUID, search string) ([]TournamentChampionLine, error) {
	return handle.List[TournamentChampionLine](q.Query(ctx, `SELECT * FROM (
		SELECT extract(year FROM h.end_date)::int AS year, h.tournament_name AS event, 'tournament' AS kind, h.category, h.division, h.player_name,
		  h.customer_id, h.score::text AS score, h.tournament_id AS event_id
		FROM reporting.golf_tournament_history h WHERE h.property_id = $1 AND h.position = 1
		UNION ALL
		SELECT s.season, s.series_name, 'order_of_merit', 'points', NULL::text, s.player_name, s.customer_id, trim_scale(s.points)::text, s.series_id
		FROM reporting.golf_series_standings s WHERE s.property_id = $1 AND s.final AND s.rank = 1) c
		WHERE $2 = '' OR c.event ILIKE '%' || $2 || '%' OR c.player_name ILIKE '%' || $2 || '%'
		ORDER BY c.year DESC, c.event, c.category, c.division NULLS FIRST LIMIT 500`, property, strings.TrimSpace(search)))
}

func (m *Module) registerHistory(reg *route.Registry) {
	db := m.DB
	add := func(rt route.Route) {
		rt.Tag = "Golf Tournament History"
		m.add(reg, rt)
	}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/tournament-history", Summary: "Tournament History: archive of completed and imported tournaments",
		Permission: "golf.tournament_history.view", Response: TournamentHistoryItem{}, List: true,
		Query: []route.Param{{Name: "from"}, {Name: "to"}, {Name: "type"}, {Name: "q"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[TournamentHistoryItem], error) {
			lp := httpx.ParseList(r)
			qv := r.URL.Query()
			return handle.Page(History(ctx, tx, handle.Property(ctx), HistoryFilter{From: qv.Get("from"), To: qv.Get("to"), Type: qv.Get("type"), Search: lp.Q,
				Limit: lp.Limit}))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/tournament-history/players", Summary: "Player statistics across tournaments",
		Permission: "golf.tournament_history.view", Response: TournamentPlayerStats{}, List: true, Query: []route.Param{{Name: "q"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[TournamentPlayerStats], error) {
			lp := httpx.ParseList(r)
			return handle.Page(PlayerStats(ctx, tx, handle.Property(ctx), lp.Q, lp.Limit))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/tournament-history/player", Summary: "History of one player: results, statistics, Order of Merit",
		Permission: "golf.tournament_history.view", Response: TournamentPlayerHistory{}, Query: []route.Param{{Name: "customerId"}, {Name: "name"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (TournamentPlayerHistory, error) {
			cust, err := handle.QueryUUID(r, "customerId")
			if err != nil {
				return TournamentPlayerHistory{}, err
			}
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			if cust == nil && name == "" {
				return TournamentPlayerHistory{}, handle.Invalid("customerId", "required", "customerId or name")
			}
			return PlayerHistory(ctx, tx, handle.Property(ctx), cust, name)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/golf/tournament-history/champions", Summary: "Champion history: tournament winners and Order of Merit champions",
		Permission: "golf.tournament_history.view", Response: TournamentChampionLine{}, List: true, Query: []route.Param{{Name: "q"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[TournamentChampionLine], error) {
			return handle.Page(ChampionHistory(ctx, tx, handle.Property(ctx), r.URL.Query().Get("q")))
		})})
}
