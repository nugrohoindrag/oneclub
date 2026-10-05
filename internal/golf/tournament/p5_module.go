package tournament

// Wiring of PRD P5 EP-23 in golf/tournament: resources (team formats,
// points tables), routes of the Back Office (team formats & teams,
// registration categories, series & Order of Merit, history, federation),
// the Member App (series standings and my tournament history) and the
// website (public series leaderboard, names with consent), the permission
// catalogue and the notification templates (ID/EN).

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/resource"
)

// RegisterP5 adds the PRD P5 resources and routes.
func (m *Module) RegisterP5(reg *route.Registry, eng *resource.Engine) {
	for _, d := range []*resource.Def{TeamFormats, SeriesPointsTables} {
		eng.Register(reg, d)
	}
	m.registerTeams(reg)
	m.registerAdvancedRegistration(reg)
	m.registerSeries(reg)
	m.registerHistory(reg)
	m.registerFederation(reg)
	m.registerSeriesPortal(reg)
}

// MemberTournamentSeries is a series in the Member App with my standing.
type MemberTournamentSeries struct {
	ID           uuid.UUID `json:"id" db:"id"`
	Code         string    `json:"code" db:"code"`
	Name         string    `json:"name" db:"name"`
	Season       int       `json:"season" db:"season"`
	Status       string    `json:"status" db:"status" enum:"active,completed"`
	Events       int       `json:"events" db:"events"`
	Counted      int       `json:"countedEvents" db:"counted_events"`
	Champion     *string   `json:"champion" db:"champion_name"`
	MyPosition   *string   `json:"myPosition" db:"my_position"`
	MyPoints     *string   `json:"myPoints" db:"my_points"`
	MyEvents     *int      `json:"myEvents" db:"my_events"`
	PublicOnline bool      `json:"public" db:"public"`
}

// PublicTournamentSeries is a series on the website.
type PublicTournamentSeries struct {
	ID       uuid.UUID `json:"id" db:"id"`
	Code     string    `json:"code" db:"code"`
	Name     string    `json:"name" db:"name"`
	Season   int       `json:"season" db:"season"`
	Status   string    `json:"status" db:"status"`
	Events   int       `json:"events" db:"events"`
	Counted  int       `json:"countedEvents" db:"counted_events"`
	Champion *string   `json:"champion" db:"-"`
}

// MyTournamentHistory is the Member App history of the signed-in member.
type MyTournamentHistory struct {
	TournamentPlayerHistory
}

func publicSeries(ctx context.Context, q dbtx.Querier, property, sid uuid.UUID, members bool) error {
	var ok bool
	cond := `public AND status IN ('active', 'completed')`
	if members {
		cond = `status IN ('active', 'completed')`
	}
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.tournament_series WHERE id = $1 AND property_id = $2 AND `+cond+`)`, sid, property).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return errs.NotFound("tournament series")
	}
	return nil
}

func (m *Module) registerSeriesPortal(reg *route.Registry) {
	db := m.DB
	me := func(rt route.Route) { crm.MeRoute(reg, "golf", "Member Portal", rt) }
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/tournament-series", Summary: "Order of Merit seasons with my standing",
		Response: MemberTournamentSeries{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[MemberTournamentSeries], error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[MemberTournamentSeries]{}, err
			}
			return handle.Page(handle.List[MemberTournamentSeries](tx.Query(ctx, `SELECT s.id, s.code, s.name, s.season, s.status, s.public, s.champion_name,
				(SELECT count(*) FROM golf.tournament_series_events e WHERE e.series_id = s.id)::int AS events,
				(SELECT count(*) FROM golf.tournament_series_events e WHERE e.series_id = s.id AND e.status = 'counted')::int AS counted_events,
				st.position_label AS my_position, trim_scale(st.points)::text AS my_points, st.events AS my_events
				FROM golf.tournament_series s LEFT JOIN reporting.golf_series_standings st ON st.series_id = s.id AND st.player_key = $2
				WHERE s.property_id = $1 AND s.status IN ('active', 'completed') ORDER BY s.season DESC, s.name`, c.PropertyID, c.ID.String())))
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/tournament-series/{id}/order-of-merit", Summary: "Order of Merit (others without consent masked)",
		Response: OrderOfMerit{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (OrderOfMerit, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return OrderOfMerit{}, err
			}
			sid, err := handle.ID(r)
			if err != nil {
				return OrderOfMerit{}, err
			}
			if err := publicSeries(ctx, tx, c.PropertyID, sid, true); err != nil {
				return OrderOfMerit{}, err
			}
			o, err := m.OrderOfMerit(ctx, tx, c.PropertyID, sid)
			if err != nil {
				return o, err
			}
			maskStandings(&o, &c.ID)
			return o, nil
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/tournament-history", Summary: "My tournament history: results, statistics, Order of Merit",
		Response: MyTournamentHistory{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (MyTournamentHistory, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return MyTournamentHistory{}, err
			}
			h, err := PlayerHistory(ctx, tx, c.PropertyID, &c.ID, c.Name)
			return MyTournamentHistory{TournamentPlayerHistory: h}, err
		})})
	pub := func(rt route.Route) { crm.PublicRoute(reg, "golf", rt) }
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/tournament-series", Summary: "Public Order of Merit seasons (website)",
		Response: PublicTournamentSeries{}, List: true, Query: []route.Param{{Name: "propertyId", Required: true}},
		Handler: m.publicRead(func(ctx context.Context, tx pgx.Tx, pid uuid.UUID, r *http.Request) (any, error) {
			list, err := ListSeries(ctx, tx, pid, "", 0, true)
			if err != nil {
				return nil, err
			}
			out := make([]PublicTournamentSeries, 0, len(list))
			for _, s := range list {
				ps := PublicTournamentSeries{ID: s.ID, Code: s.Code, Name: s.Name, Season: s.Season, Status: s.Status, Events: s.Events, Counted: s.CountedEvents}
				if s.ChampionName != nil && s.Status == "completed" {
					// the champion is public only with consent (Hall of Fame rules)
					o, err := m.OrderOfMerit(ctx, tx, pid, s.ID)
					if err != nil {
						return nil, err
					}
					maskStandings(&o, nil)
					var names []string
					for _, e := range o.Standings {
						if e.Rank != nil && *e.Rank == 1 {
							names = append(names, e.PlayerName)
						}
					}
					if len(names) > 0 {
						ps.Champion = ptr(strings.Join(names, " & "))
					}
				}
				out = append(out, ps)
			}
			return httpx.Page[PublicTournamentSeries]{Items: out}, nil
		})})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/tournament-series/{id}/order-of-merit",
		Summary: "Public Order of Merit (names only with the player's consent)", Response: OrderOfMerit{}, Query: []route.Param{{Name: "propertyId", Required: true}},
		Handler: m.publicRead(func(ctx context.Context, tx pgx.Tx, pid uuid.UUID, r *http.Request) (any, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return nil, err
			}
			if err := publicSeries(ctx, tx, pid, sid, false); err != nil {
				return nil, err
			}
			o, err := m.OrderOfMerit(ctx, tx, pid, sid)
			if err != nil {
				return nil, err
			}
			maskStandings(&o, nil)
			return o, nil
		})})
}

// ── catalogue ─────────────────────────────────────────────────────────────

// P5Contribution adds the advanced tournament permissions and grants them
// to the role templates (Pengelola golf: Golf Manager, Golf Admin).
func P5Contribution() catalog.Contribution {
	perms := resource.Permissions(TeamFormats, SeriesPointsTables)
	perms = append(perms,
		catalog.Permission{Code: "golf.tournament_team.manage", Description: "Team format of a tournament, teams and their members"},
		catalog.Permission{Code: "golf.tournament_series.view", Description: "View Tournament Series and the Order of Merit"},
		catalog.Permission{Code: "golf.tournament_series.manage", Description: "Manage Tournament Series: events, activation, recalculation, completion, import"},
		catalog.Permission{Code: "golf.tournament_history.view", Description: "Tournament History: archive, player statistics, champions"},
		catalog.Permission{Code: "golf.federation.manage", Description: "Federation (PGI): official handicap entry and result reports"},
	)
	all := append(resource.AllActions(TeamFormats, SeriesPointsTables), "golf.tournament_team.manage", "golf.tournament_series.view",
		"golf.tournament_series.manage", "golf.tournament_history.view", "golf.federation.manage")
	view := []string{"golf.tournament_series.view", "golf.tournament_history.view", "golf.team_format.view", "golf.series_points_table.view"}
	return catalog.Contribution{Permissions: perms, RolePermissions: map[string][]string{
		"property_admin":  all,
		"golf_manager":    all,
		"golf_admin":      append(append([]string{}, view...), "golf.tournament_team.manage", "golf.tournament_series.manage", "golf.federation.manage"),
		"starter_marshal": {"golf.tournament_series.view", "golf.tournament_history.view"},
		"general_manager": view,
		"club_manager":    view,
		"marketing_staff": {"golf.tournament_series.view", "golf.tournament_history.view"},
		"caddy_manager":   {"golf.tournament_history.view"},
	}}
}

// P5Templates are the notification templates of EP-23 (ID/EN).
func P5Templates() []provision.Template {
	t := map[string]map[string][2]string{
		"golf.series_standing": {
			"en": {"Order of Merit: {{.series}}", "Hello {{.name}},\n\nAfter {{.tournament}} you are {{.position}} in the {{.series}} Order of Merit with {{.points}} points."},
			"id": {"Order of Merit: {{.series}}", "Halo {{.name}},\n\nSetelah {{.tournament}} Anda berada di posisi {{.position}} Order of Merit {{.series}} dengan {{.points}} poin."},
		},
	}
	var out []provision.Template
	for ev, locs := range t {
		for loc, c := range locs {
			for _, ch := range []string{"email", "in_app"} {
				out = append(out, provision.Template{Event: ev, Channel: ch, Locale: loc, Subject: c[0], Body: c[1]})
			}
		}
	}
	return out
}
