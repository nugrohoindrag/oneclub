package tournament

// PRD P5 FR-TRN-P5-02 Tournament Series & Order of Merit (§9.6 Club
// Championship Series): points tables (Golf → Tournaments → Series), series
// per season with their events (weight; the final counts double by
// default), points per player and event counted when an event is
// finalized (golf.tournament_finalized; team events give every member the
// team's points), standings over the best N events with a minimum number
// of events, the final standings and the champion (Hall of Fame) when the
// season is completed, golf.series_standing_updated after every change;
// historical seasons are imported. The standings are read from the view
// reporting.golf_series_standings (one definition for the API, the Member
// App, the website and the Tournament Series Report).

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/golf/experience"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/membership"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/resource"
)

// EventSeriesStandingUpdated is published after the standings of a series
// change (PRD P5 §11; payload in docs/p5-contracts.md).
const EventSeriesStandingUpdated = "golf.series_standing_updated"

// SeriesPointsTables (Golf → Tournaments → Series → Points Tables).
var SeriesPointsTables = &resource.Def{
	Key: "golf.series_points_table", Module: "golf", Perm: "golf.series_points_table", Path: "/api/v1/golf/series-points-tables",
	Table: "golf.series_points_tables", Name: "Points Table", Plural: "Points Tables", Tag: tagTournaments, PropertyScoped: true, CodeField: "code",
	OrderBy: "code", SchemaName: "SeriesPointsTable",
	Fields: []resource.Field{
		{Name: "code", Column: "code", Label: "Code", Kind: resource.String, Required: true, Max: 20, Upper: true, CreateOnly: true, Pattern: code20,
			PatternMsg: "1–20 characters: A–Z, 0–9, - or _", Search: true},
		resource.Name(),
		{Name: "points", Column: "points", Label: "Points of positions 1, 2, 3 …", Kind: resource.IntList, Required: true, Min: resource.Min(0),
			MaxN: resource.Max(10000)},
		{Name: "participationPoints", Column: "participation_points", Label: "Participation Points (beyond the table, missed cut)", Kind: resource.Int,
			Default: int64(0), Min: resource.Min(0), MaxN: resource.Max(10000)},
		{Name: "tieRule", Column: "tie_rule", Label: "Ties", Kind: resource.Enum, Enum: []string{"split", "full"}, Default: "split"},
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 2000},
		{Name: "status", Column: "status", Label: "Status", Kind: resource.Enum, Enum: []string{"active", "inactive"}, Default: "active", Filter: true},
	},
}

// TournamentSeries is a series (season of an Order of Merit).
type TournamentSeries struct {
	ID                 uuid.UUID  `json:"id" db:"id"`
	Code               string     `json:"code" db:"code"`
	Name               string     `json:"name" db:"name"`
	Season             int        `json:"season" db:"season"`
	Description        *string    `json:"description" db:"description"`
	Category           string     `json:"category" db:"category" enum:"primary,gross,net,stableford" doc:"Results counted: primary = each event's primary category"`
	PointsTableID      *uuid.UUID `json:"pointsTableId" db:"points_table_id"`
	PointsTableName    *string    `json:"pointsTableName" db:"points_table_name"`
	BestOf             *int       `json:"bestOf" db:"best_of" doc:"Best N events count (null = all)"`
	MinEvents          int        `json:"minEvents" db:"min_events" doc:"Events needed to be ranked"`
	Public             bool       `json:"public" db:"public" doc:"Order of Merit on the website (names with consent)"`
	Status             string     `json:"status" db:"status" enum:"draft,active,completed,cancelled"`
	Source             string     `json:"source" db:"source" enum:"oneclub,import"`
	ChampionCustomerID *uuid.UUID `json:"championCustomerId" db:"champion_customer_id"`
	ChampionName       *string    `json:"championName" db:"champion_name"`
	Events             int        `json:"events" db:"events"`
	CountedEvents      int        `json:"countedEvents" db:"counted_events"`
	ActivatedAt        *time.Time `json:"activatedAt" db:"activated_at"`
	CompletedAt        *time.Time `json:"completedAt" db:"completed_at"`
}

const seriesSelect = `SELECT s.id, s.code, s.name, s.season, s.description, s.category, s.points_table_id, pt.name AS points_table_name, s.best_of,
	s.min_events, s.public, s.status, s.source, s.champion_customer_id, s.champion_name, s.activated_at, s.completed_at,
	(SELECT count(*) FROM golf.tournament_series_events e WHERE e.series_id = s.id)::int AS events,
	(SELECT count(*) FROM golf.tournament_series_events e WHERE e.series_id = s.id AND e.status = 'counted')::int AS counted_events
	FROM golf.tournament_series s LEFT JOIN golf.series_points_tables pt ON pt.id = s.points_table_id`

// TournamentSeriesEvent is an event of a series.
type TournamentSeriesEvent struct {
	ID               uuid.UUID  `json:"id" db:"id"`
	TournamentID     uuid.UUID  `json:"tournamentId" db:"tournament_id"`
	TournamentCode   string     `json:"tournamentCode" db:"code"`
	TournamentName   string     `json:"tournamentName" db:"name"`
	StartDate        string     `json:"startDate" db:"start_date"`
	TournamentStatus string     `json:"tournamentStatus" db:"tournament_status"`
	Format           string     `json:"format" db:"format"`
	TeamFormat       *string    `json:"teamFormat" db:"team_format"`
	Sequence         int        `json:"sequence" db:"sequence"`
	Weight           string     `json:"weight" db:"weight"`
	IsFinal          bool       `json:"isFinal" db:"is_final"`
	Status           string     `json:"status" db:"status" enum:"scheduled,counted"`
	CountedAt        *time.Time `json:"countedAt" db:"counted_at"`
	Players          int        `json:"players" db:"players" doc:"Players with points"`
}

const seriesEventSelect = `SELECT e.id, e.tournament_id, t.code, t.name, t.start_date::text AS start_date, t.status AS tournament_status, t.format,
	ts.format_snapshot->>'name' AS team_format, e.sequence, trim_scale(e.weight)::text AS weight, e.is_final, e.status, e.counted_at,
	(SELECT count(*) FROM golf.tournament_series_points p WHERE p.series_event_id = e.id)::int AS players
	FROM golf.tournament_series_events e JOIN golf.tournaments t ON t.id = e.tournament_id
	LEFT JOIN golf.tournament_team_settings ts ON ts.tournament_id = t.id`

// TournamentSeriesDetail is a series with its events and points table.
type TournamentSeriesDetail struct {
	TournamentSeries
	PointsTable *SeriesPointsSnapshot   `json:"pointsTable" doc:"Snapshot used (active / completed), else the current table"`
	EventsList  []TournamentSeriesEvent `json:"eventList"`
}

// OrderOfMeritEventPoints is a player's points of one event.
type OrderOfMeritEventPoints struct {
	SeriesEventID uuid.UUID `json:"seriesEventId" db:"series_event_id"`
	TournamentID  uuid.UUID `json:"tournamentId" db:"tournament_id"`
	PositionLabel string    `json:"positionLabel" db:"position_label"`
	Points        string    `json:"points" db:"points"`
	TeamName      *string   `json:"teamName" db:"team_name"`
}

// OrderOfMeritEntry is a line of the Order of Merit.
type OrderOfMeritEntry struct {
	Rank          *int                      `json:"rank" db:"rank"`
	PositionLabel string                    `json:"positionLabel" db:"position_label"`
	PlayerKey     string                    `json:"-" db:"player_key"`
	CustomerID    *uuid.UUID                `json:"customerId" db:"customer_id"`
	PlayerName    string                    `json:"playerName" db:"player_name"`
	Points        string                    `json:"points" db:"points"`
	Events        int                       `json:"events" db:"events"`
	Wins          int                       `json:"wins" db:"wins"`
	Top3          int                       `json:"top3" db:"top3"`
	BestPosition  *int                      `json:"bestPosition" db:"best_position"`
	Consent       bool                      `json:"-" db:"public_consent"`
	Final         bool                      `json:"-" db:"final"`
	Me            bool                      `json:"me,omitempty" db:"-" doc:"Member App: my line"`
	EventPoints   []OrderOfMeritEventPoints `json:"eventPoints" db:"-"`
}

// OrderOfMerit is the standing of a series.
type OrderOfMerit struct {
	SeriesID  uuid.UUID               `json:"seriesId"`
	Code      string                  `json:"code"`
	Name      string                  `json:"name"`
	Season    int                     `json:"season"`
	Status    string                  `json:"status"`
	Final     bool                    `json:"final" doc:"Completed season: frozen standings"`
	BestOf    *int                    `json:"bestOf"`
	MinEvents int                     `json:"minEvents"`
	Champion  *string                 `json:"champion"`
	Events    []TournamentSeriesEvent `json:"events"`
	Standings []OrderOfMeritEntry     `json:"standings"`
	UpdatedAt time.Time               `json:"updatedAt"`
}

func getSeries(ctx context.Context, q dbtx.Querier, property, sid uuid.UUID) (TournamentSeries, error) {
	rows, err := q.Query(ctx, seriesSelect+` WHERE s.id = $1 AND s.property_id = $2`, sid, property)
	return handle.One[TournamentSeries](rows, err, "tournament series")
}

func lockSeries(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID) (TournamentSeries, error) {
	var p uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id FROM golf.tournament_series WHERE id = $1 FOR UPDATE`, sid).Scan(&p); err != nil || p != property {
		if err == nil || dbtx.IsNoRows(err) {
			return TournamentSeries{}, errs.NotFound("tournament series")
		}
		return TournamentSeries{}, err
	}
	return getSeries(ctx, tx, property, sid)
}

func oneSeriesEvent(ctx context.Context, q dbtx.Querier, eid, sid uuid.UUID) (TournamentSeriesEvent, error) {
	rows, err := q.Query(ctx, seriesEventSelect+` WHERE e.id = $1 AND ($2 = '00000000-0000-0000-0000-000000000000'::uuid OR e.series_id = $2)`, eid, sid)
	return handle.One[TournamentSeriesEvent](rows, err, "series event")
}

func seriesEvents(ctx context.Context, q dbtx.Querier, sid uuid.UUID) ([]TournamentSeriesEvent, error) {
	return handle.List[TournamentSeriesEvent](q.Query(ctx, seriesEventSelect+` WHERE e.series_id = $1 ORDER BY e.sequence, t.start_date`, sid))
}

func pointsTable(ctx context.Context, q dbtx.Querier, table uuid.UUID) (SeriesPointsSnapshot, string, error) {
	var s SeriesPointsSnapshot
	var pts []int32
	var status string
	err := q.QueryRow(ctx, `SELECT code, name, points, participation_points, tie_rule, status FROM golf.series_points_tables WHERE id = $1`, table).
		Scan(&s.Code, &s.Name, &pts, &s.ParticipationPoints, &s.TieRule, &status)
	if dbtx.IsNoRows(err) {
		return s, "", handle.Invalid("pointsTableId", "not_found", "points table of this property")
	}
	for _, p := range pts {
		s.Points = append(s.Points, int(p))
	}
	return s, status, err
}

// SeriesDetail loads a series with its events.
func (m *Module) SeriesDetail(ctx context.Context, q dbtx.Querier, property, sid uuid.UUID) (TournamentSeriesDetail, error) {
	s, err := getSeries(ctx, q, property, sid)
	if err != nil {
		return TournamentSeriesDetail{}, err
	}
	d := TournamentSeriesDetail{TournamentSeries: s}
	var raw []byte
	if err := q.QueryRow(ctx, `SELECT points_snapshot FROM golf.tournament_series WHERE id = $1`, sid).Scan(&raw); err != nil {
		return d, err
	}
	if raw != nil {
		var snap SeriesPointsSnapshot
		if err := json.Unmarshal(raw, &snap); err == nil {
			d.PointsTable = &snap
		}
	} else if s.PointsTableID != nil {
		snap, _, err := pointsTable(ctx, q, *s.PointsTableID)
		if err == nil {
			d.PointsTable = &snap
		}
	}
	d.EventsList, err = seriesEvents(ctx, q, sid)
	if d.EventsList == nil {
		d.EventsList = []TournamentSeriesEvent{}
	}
	return d, err
}

// TournamentSeriesInput creates a series.
type TournamentSeriesInput struct {
	Code          string     `json:"code,omitempty" doc:"Default: from the name and season"`
	Name          string     `json:"name"`
	Season        int        `json:"season"`
	Description   string     `json:"description,omitempty"`
	Category      string     `json:"category,omitempty" enum:"primary,gross,net,stableford"`
	PointsTableID *uuid.UUID `json:"pointsTableId,omitempty"`
	BestOf        *int       `json:"bestOf,omitempty"`
	MinEvents     int        `json:"minEvents,omitempty"`
	Public        bool       `json:"public,omitempty"`
}

// TournamentSeriesPatch changes a series (draft or active).
type TournamentSeriesPatch struct {
	Name          *string    `json:"name,omitempty"`
	Description   *string    `json:"description,omitempty"`
	Category      *string    `json:"category,omitempty" enum:"primary,gross,net,stableford"`
	PointsTableID *uuid.UUID `json:"pointsTableId,omitempty" doc:"An active series takes a new snapshot and recalculates"`
	BestOf        *int       `json:"bestOf,omitempty" doc:"0 = every event counts"`
	MinEvents     *int       `json:"minEvents,omitempty"`
	Public        *bool      `json:"public,omitempty"`
}

// CreateSeries creates a draft series.
func (m *Module) CreateSeries(ctx context.Context, tx pgx.Tx, property uuid.UUID, in TournamentSeriesInput) (TournamentSeriesDetail, error) {
	if err := handle.Required("name", in.Name); err != nil {
		return TournamentSeriesDetail{}, err
	}
	if in.Season < 1990 || in.Season > 2200 {
		return TournamentSeriesDetail{}, handle.Invalid("season", "invalid", "a year")
	}
	code := strings.ToUpper(strings.TrimSpace(in.Code))
	if code == "" {
		code = strings.Trim(nonCode.ReplaceAllString(strings.ToUpper(in.Name), "-"), "-")
		if len(code) > 30 {
			code = code[:30]
		}
		code = strings.Trim(code, "-") + "-" + strconv.Itoa(in.Season)
	}
	if !codeRe.MatchString(code) {
		return TournamentSeriesDetail{}, handle.Invalid("code", "invalid", "1–40 characters: A–Z, 0–9, - or _")
	}
	cat := in.Category
	if cat == "" {
		cat = "primary"
	}
	if err := oneOf("category", cat, "primary", "gross", "net", "stableford"); err != nil {
		return TournamentSeriesDetail{}, err
	}
	if in.BestOf != nil && *in.BestOf < 1 {
		return TournamentSeriesDetail{}, handle.Invalid("bestOf", "invalid", "≥ 1")
	}
	if in.MinEvents < 0 {
		return TournamentSeriesDetail{}, handle.Invalid("minEvents", "invalid", "≥ 0")
	}
	if in.PointsTableID != nil {
		if _, _, err := pointsTable(ctx, tx, *in.PointsTableID); err != nil {
			return TournamentSeriesDetail{}, err
		}
	}
	sid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO golf.tournament_series (id, property_id, code, name, season, description, category, points_table_id, best_of,
		min_events, public, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12)`, sid, property, code, strings.TrimSpace(in.Name),
		in.Season, nullStr(in.Description), cat, in.PointsTableID, in.BestOf, in.MinEvents, in.Public, actor(ctx)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return TournamentSeriesDetail{}, errs.Conflict("duplicate_code", "a series with code "+code+" exists")
		}
		return TournamentSeriesDetail{}, err
	}
	d, err := m.SeriesDetail(ctx, tx, property, sid)
	if err != nil {
		return d, err
	}
	return d, record(ctx, tx, "golf.tournament_series", sid, code+" · "+d.Name, audit.ActionCreate, property, nil, d.TournamentSeries, "")
}

// UpdateSeries changes a draft or active series (an active series is recalculated).
func (m *Module) UpdateSeries(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID, in TournamentSeriesPatch) (TournamentSeriesDetail, error) {
	s, err := lockSeries(ctx, tx, property, sid)
	if err != nil {
		return TournamentSeriesDetail{}, err
	}
	if s.Status != "draft" && s.Status != "active" {
		return TournamentSeriesDetail{}, errs.Conflict("invalid_status", "a "+s.Status+" series cannot be changed")
	}
	if in.Name != nil {
		if err := handle.Required("name", *in.Name); err != nil {
			return TournamentSeriesDetail{}, err
		}
		s.Name = strings.TrimSpace(*in.Name)
	}
	if in.Description != nil {
		s.Description = nullStr(*in.Description)
	}
	if in.Category != nil {
		if err := oneOf("category", *in.Category, "primary", "gross", "net", "stableford"); err != nil {
			return TournamentSeriesDetail{}, err
		}
		s.Category = *in.Category
	}
	resnap := false
	if in.PointsTableID != nil {
		if _, _, err := pointsTable(ctx, tx, *in.PointsTableID); err != nil {
			return TournamentSeriesDetail{}, err
		}
		resnap = s.PointsTableID == nil || *s.PointsTableID != *in.PointsTableID
		s.PointsTableID = in.PointsTableID
	}
	if in.BestOf != nil {
		if *in.BestOf < 0 {
			return TournamentSeriesDetail{}, handle.Invalid("bestOf", "invalid", "≥ 0")
		}
		s.BestOf = in.BestOf
		if *in.BestOf == 0 {
			s.BestOf = nil
		}
	}
	if in.MinEvents != nil {
		if *in.MinEvents < 0 {
			return TournamentSeriesDetail{}, handle.Invalid("minEvents", "invalid", "≥ 0")
		}
		s.MinEvents = *in.MinEvents
	}
	if in.Public != nil {
		s.Public = *in.Public
	}
	before, err := getSeries(ctx, tx, property, sid)
	if err != nil {
		return TournamentSeriesDetail{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_series SET name = $2, description = $3, category = $4, points_table_id = $5, best_of = $6, min_events = $7,
		public = $8, updated_by = $9 WHERE id = $1`, sid, s.Name, s.Description, s.Category, s.PointsTableID, s.BestOf, s.MinEvents, s.Public, actor(ctx)); err != nil {
		return TournamentSeriesDetail{}, err
	}
	if s.Status == "active" {
		if resnap {
			if err := snapshotTable(ctx, tx, sid, *s.PointsTableID); err != nil {
				return TournamentSeriesDetail{}, err
			}
		}
		if _, err := m.recalculate(ctx, tx, property, sid, nil); err != nil {
			return TournamentSeriesDetail{}, err
		}
	}
	d, err := m.SeriesDetail(ctx, tx, property, sid)
	if err != nil {
		return d, err
	}
	return d, record(ctx, tx, "golf.tournament_series", sid, s.Code+" · "+s.Name, audit.ActionUpdate, property, before, d.TournamentSeries, "")
}

func snapshotTable(ctx context.Context, tx pgx.Tx, sid, table uuid.UUID) error {
	snap, status, err := pointsTable(ctx, tx, table)
	if err != nil {
		return err
	}
	if status != "active" {
		return handle.Invalid("pointsTableId", "inactive", "an active points table")
	}
	if len(snap.Points) == 0 {
		return handle.Invalid("pointsTableId", "empty", "a points table with points")
	}
	_, err = tx.Exec(ctx, `UPDATE golf.tournament_series SET points_snapshot = $2 WHERE id = $1`, sid, jsonOf(snap))
	return err
}

// TournamentSeriesEventInput adds a tournament to a series.
type TournamentSeriesEventInput struct {
	TournamentID uuid.UUID `json:"tournamentId"`
	Sequence     *int      `json:"sequence,omitempty" doc:"Default: after the last event"`
	Weight       *string   `json:"weight,omitempty" doc:"Points multiplier (default 1; the final 2)"`
	IsFinal      *bool     `json:"isFinal,omitempty"`
}

// TournamentSeriesEventPatch changes an event of a series.
type TournamentSeriesEventPatch struct {
	Sequence *int    `json:"sequence,omitempty"`
	Weight   *string `json:"weight,omitempty"`
	IsFinal  *bool   `json:"isFinal,omitempty"`
}

func weightOf(field string, v *string, def string) (string, error) {
	if v == nil || strings.TrimSpace(*v) == "" {
		return def, nil
	}
	w, err := decimal.NewFromString(strings.TrimSpace(*v))
	if err != nil || !w.IsPositive() || w.GreaterThan(decimal.NewFromInt(10)) {
		return "", handle.Invalid(field, "invalid", "a multiplier above 0 and up to 10")
	}
	return w.String(), nil
}

// AddSeriesEvent adds a tournament (an event of the season) to a series.
func (m *Module) AddSeriesEvent(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID, in TournamentSeriesEventInput) (TournamentSeriesEvent, error) {
	s, err := lockSeries(ctx, tx, property, sid)
	if err != nil {
		return TournamentSeriesEvent{}, err
	}
	if s.Status != "draft" && s.Status != "active" {
		return TournamentSeriesEvent{}, errs.Conflict("invalid_status", "events are added to a draft or active series")
	}
	t, err := tournamentAt(ctx, tx, property, in.TournamentID)
	if err != nil {
		return TournamentSeriesEvent{}, handle.Invalid("tournamentId", "not_found", "tournament of this property")
	}
	if t.Status == "cancelled" {
		return TournamentSeriesEvent{}, handle.Invalid("tournamentId", "cancelled", "a tournament that is not cancelled")
	}
	final := in.IsFinal != nil && *in.IsFinal
	def := "1"
	if final {
		def = "2"
	}
	weight, err := weightOf("weight", in.Weight, def)
	if err != nil {
		return TournamentSeriesEvent{}, err
	}
	seq := 0
	if in.Sequence != nil {
		seq = *in.Sequence
	} else if err := tx.QueryRow(ctx, `SELECT coalesce(max(sequence), 0) + 1 FROM golf.tournament_series_events WHERE series_id = $1`, sid).Scan(&seq); err != nil {
		return TournamentSeriesEvent{}, err
	}
	eid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO golf.tournament_series_events (id, property_id, series_id, tournament_id, sequence, weight, is_final, created_by)
		VALUES ($1,$2,$3,$4,$5,$6::numeric,$7,$8)`, eid, property, sid, t.ID, seq, weight, final, actor(ctx)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return TournamentSeriesEvent{}, errs.Conflict("duplicate_event", t.Name+" is already an event of the series")
		}
		return TournamentSeriesEvent{}, err
	}
	if s.Status == "active" && t.Status == "completed" {
		if _, err := m.countEvent(ctx, tx, property, s, eid); err != nil {
			return TournamentSeriesEvent{}, err
		}
		if err := m.standingUpdated(ctx, tx, property, sid, &t.ID); err != nil {
			return TournamentSeriesEvent{}, err
		}
	}
	ev, err := oneSeriesEvent(ctx, tx, eid, uuid.Nil)
	if err != nil {
		return ev, err
	}
	return ev, record(ctx, tx, "golf.tournament_series", sid, s.Code+" + "+t.Code, "add_event", property, nil, ev, "")
}

// UpdateSeriesEvent changes the sequence, weight or final flag of an event.
func (m *Module) UpdateSeriesEvent(ctx context.Context, tx pgx.Tx, property, sid, eid uuid.UUID, in TournamentSeriesEventPatch) (TournamentSeriesEvent, error) {
	s, err := lockSeries(ctx, tx, property, sid)
	if err != nil {
		return TournamentSeriesEvent{}, err
	}
	if s.Status != "draft" && s.Status != "active" {
		return TournamentSeriesEvent{}, errs.Conflict("invalid_status", "events change in a draft or active series")
	}
	before, err := oneSeriesEvent(ctx, tx, eid, sid)
	if err != nil {
		return before, err
	}
	seq, final := before.Sequence, before.IsFinal
	if in.Sequence != nil {
		seq = *in.Sequence
	}
	if in.IsFinal != nil {
		final = *in.IsFinal
	}
	weight, err := weightOf("weight", in.Weight, before.Weight)
	if err != nil {
		return before, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_series_events SET sequence = $2, weight = $3::numeric, is_final = $4 WHERE id = $1`, eid, seq, weight,
		final); err != nil {
		return before, err
	}
	if s.Status == "active" && before.Status == "counted" && weight != before.Weight {
		if _, err := m.countEvent(ctx, tx, property, s, eid); err != nil {
			return before, err
		}
		if err := m.standingUpdated(ctx, tx, property, sid, &before.TournamentID); err != nil {
			return before, err
		}
	}
	after, err := oneSeriesEvent(ctx, tx, eid, uuid.Nil)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.tournament_series", sid, s.Code+" "+before.TournamentCode, "update_event", property, before, after, "")
}

// RemoveSeriesEvent removes an event (and its points) from a series.
func (m *Module) RemoveSeriesEvent(ctx context.Context, tx pgx.Tx, property, sid, eid uuid.UUID) error {
	s, err := lockSeries(ctx, tx, property, sid)
	if err != nil {
		return err
	}
	if s.Status != "draft" && s.Status != "active" {
		return errs.Conflict("invalid_status", "events change in a draft or active series")
	}
	before, err := oneSeriesEvent(ctx, tx, eid, sid)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM golf.tournament_series_events WHERE id = $1`, eid); err != nil {
		return err
	}
	if before.Status == "counted" && s.Status == "active" {
		if err := m.standingUpdated(ctx, tx, property, sid, &before.TournamentID); err != nil {
			return err
		}
	}
	return record(ctx, tx, "golf.tournament_series", sid, s.Code+" − "+before.TournamentCode, "remove_event", property, before, nil, "")
}

// ── counting ──────────────────────────────────────────────────────────────

type seriesResult struct {
	CustomerID *uuid.UUID `db:"customer_id"`
	PlayerName string     `db:"player_name"`
	Position   *int       `db:"position"`
	Label      string     `db:"position_label"`
	Tied       bool       `db:"tied"`
	Consent    bool       `db:"consent"`
	TeamName   *string    `db:"team_name"`
}

func playerKey(customer *uuid.UUID, name string) string {
	if customer != nil {
		return customer.String()
	}
	return "n:" + strings.ToLower(strings.TrimSpace(name))
}

// eventResults are the overall results of a completed tournament in the
// series category: individual, or every member with the team's result.
func eventResults(ctx context.Context, q dbtx.Querier, t Tournament, category string) ([]seriesResult, error) {
	_, f, err := teamSettings(ctx, q, t.ID)
	if err != nil {
		return nil, err
	}
	cats := categories(t)
	if category == "primary" || !containsStr(cats, category) {
		category = primaryCategory(t)
	}
	if f != nil {
		var n int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM golf.tournament_team_results WHERE tournament_id = $1`, t.ID).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			return handle.List[seriesResult](q.Query(ctx, `SELECT (mb->>'customerId')::uuid AS customer_id, mb->>'playerName' AS player_name, r.position,
				r.position_label, r.tied, coalesce((mb->>'consent')::boolean, false) AS consent, r.team_name
				FROM golf.tournament_team_results r CROSS JOIN LATERAL jsonb_array_elements(r.members) mb
				WHERE r.tournament_id = $1 AND r.category = $2`, t.ID, category))
		}
	}
	return handle.List[seriesResult](q.Query(ctx, `SELECT x.customer_id, x.player_name, x.position, x.position_label, x.tied,
		coalesce(r.public_consent, false) AS consent, NULL::text AS team_name
		FROM golf.tournament_results x LEFT JOIN golf.tournament_registrations r ON r.id = x.registration_id
		WHERE x.tournament_id = $1 AND x.category = $2 AND x.division_id IS NULL AND x.division_label IS NULL`, t.ID, category))
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// countEvent (re)computes the points of an event of an active series.
func (m *Module) countEvent(ctx context.Context, tx pgx.Tx, property uuid.UUID, s TournamentSeries, eid uuid.UUID) (int, error) {
	var tid uuid.UUID
	var weight string
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT e.tournament_id, e.weight::text, s.points_snapshot FROM golf.tournament_series_events e
		JOIN golf.tournament_series s ON s.id = e.series_id WHERE e.id = $1`, eid).Scan(&tid, &weight, &raw); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM golf.tournament_series_points WHERE series_event_id = $1`, eid); err != nil {
		return 0, err
	}
	t, err := GetTournament(ctx, tx, tid)
	if err != nil {
		return 0, err
	}
	if t.Status != "completed" || raw == nil {
		_, err := tx.Exec(ctx, `UPDATE golf.tournament_series_events SET status = 'scheduled', counted_at = NULL WHERE id = $1`, eid)
		return 0, err
	}
	var table SeriesPointsSnapshot
	if err := json.Unmarshal(raw, &table); err != nil {
		return 0, err
	}
	results, err := eventResults(ctx, tx, t, s.Category)
	if err != nil {
		return 0, err
	}
	var fs []seriesFinisher
	by := map[string]seriesResult{}
	for _, r := range results {
		if r.Position == nil && (r.Label == "-" || r.Label == "") {
			continue // registered but did not play
		}
		k := playerKey(r.CustomerID, r.PlayerName)
		if _, dup := by[k]; dup {
			continue
		}
		by[k] = r
		fs = append(fs, seriesFinisher{Key: k, Position: r.Position, Label: r.Label})
	}
	pts := seriesEventPoints(table, fs)
	w := dec(weight)
	for _, f := range fs {
		r := by[f.Key]
		base := pts[f.Key]
		if _, err := tx.Exec(ctx, `INSERT INTO golf.tournament_series_points (id, property_id, series_id, series_event_id, tournament_id, player_key, customer_id,
			player_name, team_name, position, position_label, tied, base_points, weight, points, public_consent, event_date)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::numeric,$14::numeric,$15::numeric,$16,$17::date)`, id.New(), property, s.ID, eid, tid, f.Key,
			r.CustomerID, r.PlayerName, r.TeamName, r.Position, r.Label, r.Tied, base.String(), w.String(), base.Mul(w).Round(2).String(), r.Consent,
			t.EndDate); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_series_events SET status = 'counted', counted_at = now() WHERE id = $1`, eid); err != nil {
		return 0, err
	}
	return len(fs), nil
}

// recalculate counts every event of an active series (only the events of
// one tournament when tid is set).
func (m *Module) recalculate(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID, tid *uuid.UUID) (int, error) {
	s, err := getSeries(ctx, tx, property, sid)
	if err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM golf.tournament_series_events WHERE series_id = $1 AND ($2::uuid IS NULL OR tournament_id = $2) ORDER BY sequence`,
		sid, tid)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, err
	}
	n := 0
	for _, eid := range ids {
		c, err := m.countEvent(ctx, tx, property, s, eid)
		if err != nil {
			return n, err
		}
		n += c
	}
	return n, nil
}

// OrderOfMerit returns the standings of a series (viewer masks others
// without consent; public masks everyone without consent).
func (m *Module) OrderOfMerit(ctx context.Context, q dbtx.Querier, property, sid uuid.UUID) (OrderOfMerit, error) {
	s, err := getSeries(ctx, q, property, sid)
	if err != nil {
		return OrderOfMerit{}, err
	}
	out := OrderOfMerit{SeriesID: sid, Code: s.Code, Name: s.Name, Season: s.Season, Status: s.Status, Final: s.Status == "completed", BestOf: s.BestOf,
		MinEvents: s.MinEvents, Champion: s.ChampionName, UpdatedAt: now()}
	if out.Events, err = seriesEvents(ctx, q, sid); err != nil {
		return out, err
	}
	if out.Events == nil {
		out.Events = []TournamentSeriesEvent{}
	}
	if out.Standings, err = handle.List[OrderOfMeritEntry](q.Query(ctx, `SELECT rank, position_label, player_key, customer_id, player_name,
		trim_scale(points)::text AS points, events, wins, top3, best_position, public_consent, final FROM reporting.golf_series_standings WHERE series_id = $1
		ORDER BY rank NULLS LAST, points DESC, player_name`, sid)); err != nil {
		return out, err
	}
	type ep struct {
		PlayerKey string `db:"player_key"`
		OrderOfMeritEventPoints
	}
	eps, err := handle.List[ep](q.Query(ctx, `SELECT p.player_key, p.series_event_id, p.tournament_id, p.position_label, trim_scale(p.points)::text AS points,
		p.team_name FROM golf.tournament_series_points p JOIN golf.tournament_series_events e ON e.id = p.series_event_id WHERE p.series_id = $1
		ORDER BY e.sequence`, sid))
	if err != nil {
		return out, err
	}
	by := map[string][]OrderOfMeritEventPoints{}
	for _, e := range eps {
		by[e.PlayerKey] = append(by[e.PlayerKey], e.OrderOfMeritEventPoints)
	}
	for i := range out.Standings {
		out.Standings[i].EventPoints = by[out.Standings[i].PlayerKey]
		if out.Standings[i].EventPoints == nil {
			out.Standings[i].EventPoints = []OrderOfMeritEventPoints{}
		}
	}
	return out, nil
}

// maskStandings hides players without public consent (UU PDP) except me.
func maskStandings(o *OrderOfMerit, me *uuid.UUID) {
	for i := range o.Standings {
		e := &o.Standings[i]
		if me != nil && e.CustomerID != nil && *e.CustomerID == *me {
			e.Me = true
			continue
		}
		if !e.Consent {
			var initials []string
			for _, w := range strings.Fields(e.PlayerName) {
				r := []rune(w)
				initials = append(initials, strings.ToUpper(string(r[0]))+".")
			}
			e.PlayerName = "Player " + strings.Join(initials, "")
		}
		e.CustomerID = nil
		for k := range e.EventPoints {
			e.EventPoints[k].TeamName = nil
		}
	}
}

// standingUpdated publishes golf.series_standing_updated with the leaders
// and notifies the players of a counted event of their position.
func (m *Module) standingUpdated(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID, tid *uuid.UUID) error {
	o, err := m.OrderOfMerit(ctx, tx, property, sid)
	if err != nil {
		return err
	}
	leaders := []map[string]any{}
	for _, e := range o.Standings {
		if len(leaders) == 5 || e.Rank == nil {
			break
		}
		leaders = append(leaders, map[string]any{"rank": e.Rank, "positionLabel": e.PositionLabel, "customerId": e.CustomerID, "playerName": e.PlayerName,
			"points": e.Points, "events": e.Events})
	}
	if err := m.publish(ctx, tx, EventSeriesStandingUpdated, "golf.tournament_series", sid, property, map[string]any{"seriesId": sid, "code": o.Code,
		"name": o.Name, "season": o.Season, "status": o.Status, "final": o.Final, "tournamentId": tid, "players": len(o.Standings), "leaders": leaders}); err != nil {
		return err
	}
	if tid != nil {
		var tname string
		if err := tx.QueryRow(ctx, `SELECT name FROM golf.tournaments WHERE id = $1`, *tid).Scan(&tname); err != nil {
			return err
		}
		played := map[string]bool{}
		rows, err := tx.Query(ctx, `SELECT player_key FROM golf.tournament_series_points WHERE series_id = $1 AND tournament_id = $2`, sid, *tid)
		if err != nil {
			return err
		}
		keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, k := range keys {
			played[k] = true
		}
		for _, e := range o.Standings {
			if !played[e.PlayerKey] || e.CustomerID == nil {
				continue
			}
			var email *string
			if err := tx.QueryRow(ctx, `SELECT email FROM crm.customers WHERE id = $1`, *e.CustomerID).Scan(&email); err != nil && !dbtx.IsNoRows(err) {
				return err
			}
			user, err := customerUser(ctx, tx, *e.CustomerID)
			if err != nil {
				return err
			}
			if err := m.notifyCustomer(ctx, tx, property, deref(email), e.PlayerName, user, "golf.series_standing", map[string]any{"name": e.PlayerName,
				"series": o.Name, "tournament": tname, "position": e.PositionLabel, "points": e.Points}); err != nil {
				return err
			}
		}
	}
	return nil
}

// ActivateSeries freezes the points table and counts the completed events.
func (m *Module) ActivateSeries(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID) (TournamentSeriesDetail, error) {
	s, err := lockSeries(ctx, tx, property, sid)
	if err != nil {
		return TournamentSeriesDetail{}, err
	}
	if s.Status != "draft" {
		return TournamentSeriesDetail{}, errs.Conflict("invalid_status", "only a draft series is activated, not "+s.Status)
	}
	if s.PointsTableID == nil {
		return TournamentSeriesDetail{}, errs.Validation("points_table_required", "choose the points table before activating",
			errs.Field("pointsTableId", "required", "points table"))
	}
	if err := snapshotTable(ctx, tx, sid, *s.PointsTableID); err != nil {
		return TournamentSeriesDetail{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_series SET status = 'active', activated_at = now(), updated_by = $2 WHERE id = $1`, sid, actor(ctx)); err != nil {
		return TournamentSeriesDetail{}, err
	}
	if _, err := m.recalculate(ctx, tx, property, sid, nil); err != nil {
		return TournamentSeriesDetail{}, err
	}
	if err := m.standingUpdated(ctx, tx, property, sid, nil); err != nil {
		return TournamentSeriesDetail{}, err
	}
	d, err := m.SeriesDetail(ctx, tx, property, sid)
	if err != nil {
		return d, err
	}
	return d, record(ctx, tx, "golf.tournament_series", sid, s.Code, "activate", property, map[string]any{"status": "draft"},
		map[string]any{"status": "active", "pointsTable": d.PointsTable}, "")
}

// TournamentSeriesRecalcResult reports a recalculation.
type TournamentSeriesRecalcResult struct {
	Series TournamentSeriesDetail `json:"series"`
	Points int                    `json:"points" doc:"Player points written"`
}

// RecalculateSeries recounts every event of an active series.
func (m *Module) RecalculateSeries(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID) (TournamentSeriesRecalcResult, error) {
	s, err := lockSeries(ctx, tx, property, sid)
	if err != nil {
		return TournamentSeriesRecalcResult{}, err
	}
	if s.Status != "active" {
		return TournamentSeriesRecalcResult{}, errs.Conflict("invalid_status", "only an active series is recalculated")
	}
	n, err := m.recalculate(ctx, tx, property, sid, nil)
	if err != nil {
		return TournamentSeriesRecalcResult{}, err
	}
	if err := m.standingUpdated(ctx, tx, property, sid, nil); err != nil {
		return TournamentSeriesRecalcResult{}, err
	}
	d, err := m.SeriesDetail(ctx, tx, property, sid)
	if err != nil {
		return TournamentSeriesRecalcResult{}, err
	}
	return TournamentSeriesRecalcResult{Series: d, Points: n}, record(ctx, tx, "golf.tournament_series", sid, s.Code, "recalculate", property, nil,
		map[string]any{"points": n, "counted": d.CountedEvents}, "")
}

// CompleteSeries freezes the final standings, records the champion and
// adds them to the Hall of Fame (public only with consent).
func (m *Module) CompleteSeries(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID) (OrderOfMerit, error) {
	s, err := lockSeries(ctx, tx, property, sid)
	if err != nil {
		return OrderOfMerit{}, err
	}
	if s.Status != "active" {
		return OrderOfMerit{}, errs.Conflict("invalid_status", "only an active series is completed")
	}
	var open []string
	rows, err := tx.Query(ctx, `SELECT t.name FROM golf.tournament_series_events e JOIN golf.tournaments t ON t.id = e.tournament_id
		WHERE e.series_id = $1 AND t.status NOT IN ('completed', 'cancelled') ORDER BY e.sequence`, sid)
	if err != nil {
		return OrderOfMerit{}, err
	}
	if open, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return OrderOfMerit{}, err
	}
	if len(open) > 0 {
		return OrderOfMerit{}, errs.Conflict("events_open", "events not finished yet: "+strings.Join(open, ", "))
	}
	if _, err := m.recalculate(ctx, tx, property, sid, nil); err != nil {
		return OrderOfMerit{}, err
	}
	o, err := m.OrderOfMerit(ctx, tx, property, sid)
	if err != nil {
		return o, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM golf.tournament_series_standings WHERE series_id = $1`, sid); err != nil {
		return o, err
	}
	var champs []OrderOfMeritEntry
	for _, e := range o.Standings {
		if _, err := tx.Exec(ctx, `INSERT INTO golf.tournament_series_standings (id, property_id, series_id, player_key, customer_id, player_name, rank,
			position_label, points, events, wins, top3, best_position, public_consent) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10,$11,$12,$13,$14)`,
			id.New(), property, sid, e.PlayerKey, e.CustomerID, e.PlayerName, e.Rank, e.PositionLabel, e.Points, e.Events, e.Wins, e.Top3, e.BestPosition,
			e.Consent); err != nil {
			return o, err
		}
		if e.Rank != nil && *e.Rank == 1 {
			champs = append(champs, e)
		}
	}
	var champion *string
	var championCustomer *uuid.UUID
	if len(champs) > 0 {
		names := make([]string, 0, len(champs))
		for _, c := range champs {
			names = append(names, c.PlayerName)
		}
		champion = ptr(strings.Join(names, " & "))
		championCustomer = champs[0].CustomerID
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_series SET status = 'completed', completed_at = now(), champion_name = $2, champion_customer_id = $3,
		updated_by = $4 WHERE id = $1`, sid, champion, championCustomer, actor(ctx)); err != nil {
		return o, err
	}
	if m.Experience != nil {
		end := time.Date(s.Season, 12, 31, 0, 0, 0, 0, time.UTC)
		var last *string
		if err := tx.QueryRow(ctx, `SELECT max(t.end_date)::text FROM golf.tournament_series_events e JOIN golf.tournaments t ON t.id = e.tournament_id
			WHERE e.series_id = $1`, sid).Scan(&last); err != nil {
			return o, err
		}
		if last != nil {
			if d, err := time.Parse("2006-01-02", *last); err == nil {
				end = d
			}
		}
		for _, c := range champs {
			pts := int(dec(c.Points).IntPart())
			if _, err := m.Experience.TournamentHallOfFameEntry(ctx, tx, property, experience.TournamentHallOfFameInput{Category: "tournament_champion",
				Title: fmt.Sprintf("%s %d — Order of Merit Champion", s.Name, s.Season), Year: s.Season, CustomerID: c.CustomerID, PlayerName: c.PlayerName,
				Score: &pts, AchievedOn: end, SourceID: uuid.NewSHA1(sid, []byte(c.PlayerKey)), Consent: c.Consent}); err != nil {
				return o, err
			}
		}
	}
	if err := m.standingUpdated(ctx, tx, property, sid, nil); err != nil {
		return o, err
	}
	out, err := m.OrderOfMerit(ctx, tx, property, sid)
	if err != nil {
		return out, err
	}
	return out, record(ctx, tx, "golf.tournament_series", sid, s.Code, "complete", property, map[string]any{"status": "active"},
		map[string]any{"status": "completed", "champion": champion, "players": len(out.Standings)}, "")
}

// CancelSeries cancels a draft or active series.
func (m *Module) CancelSeries(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID, reason string) (TournamentSeriesDetail, error) {
	if err := handle.Required("reason", reason); err != nil {
		return TournamentSeriesDetail{}, err
	}
	s, err := lockSeries(ctx, tx, property, sid)
	if err != nil {
		return TournamentSeriesDetail{}, err
	}
	if s.Status != "draft" && s.Status != "active" {
		return TournamentSeriesDetail{}, errs.Conflict("invalid_status", "a "+s.Status+" series cannot be cancelled")
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_series SET status = 'cancelled', cancelled_at = now(), cancel_reason = $2, updated_by = $3 WHERE id = $1`,
		sid, reason, actor(ctx)); err != nil {
		return TournamentSeriesDetail{}, err
	}
	d, err := m.SeriesDetail(ctx, tx, property, sid)
	if err != nil {
		return d, err
	}
	return d, record(ctx, tx, "golf.tournament_series", sid, s.Code, audit.ActionStatusChange, property, map[string]any{"status": s.Status},
		map[string]any{"status": "cancelled"}, reason)
}

// OnTournamentFinalized freezes the team results of a team tournament and
// counts the tournament in its active series (golf.tournament_finalized).
func (m *Module) OnTournamentFinalized(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		TournamentID uuid.UUID `json:"tournamentId"`
	}
	if err := e.Decode(&p); err != nil || p.TournamentID == uuid.Nil || e.PropertyID == nil {
		return nil //nolint:nilerr // foreign payload
	}
	property := *e.PropertyID
	ctx = reqctx.WithProperty(dbtx.System(ctx), property)
	if _, err := m.FreezeTeamResults(ctx, tx, property, p.TournamentID); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT e.series_id FROM golf.tournament_series_events e JOIN golf.tournament_series s ON s.id = e.series_id
		WHERE e.tournament_id = $1 AND s.status = 'active'`, p.TournamentID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	for _, sid := range ids {
		n, err := m.recalculate(ctx, tx, property, sid, &p.TournamentID)
		if err != nil {
			return err
		}
		if err := m.standingUpdated(ctx, tx, property, sid, &p.TournamentID); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "count_event", EntityType: "golf.tournament_series", EntityID: sid.String(),
			EntityLabel: "Order of Merit", PropertyID: &property, After: map[string]any{"tournamentId": p.TournamentID, "points": n},
			Metadata: map[string]any{"event": e.ID}}); err != nil {
			return err
		}
	}
	return nil
}

// ── historical seasons (import) ───────────────────────────────────────────

// SeriesImportColumns are the columns of a historical Order of Merit CSV.
var SeriesImportColumns = []string{"seriesCode", "seriesName", "season", "rank", "playerName", "memberNo", "customerCode", "points", "events", "wins",
	"publicConsent"}

// TournamentSeriesImportInput imports the final standings of past seasons.
type TournamentSeriesImportInput struct {
	Mode string `json:"mode" enum:"preview,commit"`
	CSV  string `json:"csv" doc:"Header row with seriesCode, seriesName, season, rank, playerName, memberNo, customerCode, points, events, wins, publicConsent"`
}

// TournamentSeriesImportResult reports an import.
type TournamentSeriesImportResult struct {
	Mode      string                         `json:"mode"`
	TotalRows int                            `json:"totalRows"`
	Series    int                            `json:"series" doc:"Series created"`
	Standings int                            `json:"standings"`
	Failed    int                            `json:"failed"`
	Errors    []TournamentHistoryImportError `json:"errors"`
}

// ImportSeries imports historical Order of Merit standings: each series
// becomes a completed, imported series (idempotent per series code and player).
func (m *Module) ImportSeries(ctx context.Context, tx pgx.Tx, property uuid.UUID, in TournamentSeriesImportInput) (TournamentSeriesImportResult, error) {
	res := TournamentSeriesImportResult{Mode: in.Mode, Errors: []TournamentHistoryImportError{}}
	if err := oneOf("mode", in.Mode, "preview", "commit"); err != nil {
		return res, err
	}
	cr := csv.NewReader(strings.NewReader(strings.TrimPrefix(in.CSV, "\uFEFF")))
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return res, handle.Invalid("csv", "csv_invalid", "the file is empty or not a CSV")
	}
	cols := map[string]int{}
	for i, h := range header {
		for _, c := range SeriesImportColumns {
			if strings.EqualFold(strings.TrimSpace(h), c) {
				cols[c] = i
			}
		}
	}
	for _, c := range []string{"seriesCode", "seriesName", "season", "playerName", "points"} {
		if _, ok := cols[c]; !ok {
			res.Errors = append(res.Errors, TournamentHistoryImportError{Row: 1, Field: c, Code: "missing_column", Message: "column " + c + " is required"})
		}
	}
	if len(res.Errors) > 0 {
		return res, nil
	}
	outer, err := tx.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer func() { _ = outer.Rollback(ctx) }()
	created := map[string]uuid.UUID{}
	line := 1
	for {
		rec, err := cr.Read()
		line++
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			res.TotalRows++
			res.Failed++
			res.Errors = append(res.Errors, TournamentHistoryImportError{Row: line, Code: "csv_invalid", Message: err.Error()})
			continue
		}
		get := func(c string) string {
			if i, ok := cols[c]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		if strings.Join(rec, "") == "" {
			continue
		}
		res.TotalRows++
		if res.TotalRows > 20000 {
			return res, handle.Invalid("csv", "too_many_rows", "imports are limited to 20,000 rows per file")
		}
		sp, err := outer.Begin(ctx)
		if err != nil {
			return res, err
		}
		isNew, rowErr := m.importSeriesRow(ctx, sp, property, get, created)
		if rowErr != nil {
			_ = sp.Rollback(ctx)
			res.Failed++
			if de, ok := errs.As(rowErr); ok && de.Kind != errs.KindInternal {
				res.Errors = append(res.Errors, TournamentHistoryImportError{Row: line, Code: de.Code, Message: de.Message})
				continue
			}
			return res, rowErr
		}
		if err := sp.Commit(ctx); err != nil {
			return res, err
		}
		res.Standings++
		if isNew {
			res.Series++
		}
	}
	if in.Mode == "commit" {
		if err := outer.Commit(ctx); err != nil {
			return res, err
		}
	}
	return res, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: audit.ActionImport, EntityType: "golf.tournament_series", EntityID: property.String(),
		EntityLabel: "Order of Merit history import (" + in.Mode + ")", PropertyID: &property, After: map[string]any{"mode": in.Mode, "rows": res.TotalRows,
			"series": res.Series, "standings": res.Standings, "failed": res.Failed}})
}

func (m *Module) importSeriesRow(ctx context.Context, tx pgx.Tx, property uuid.UUID, get func(string) string, created map[string]uuid.UUID) (bool, error) {
	code := strings.ToUpper(get("seriesCode"))
	if !codeRe.MatchString(code) {
		return false, errs.Validation("invalid_code", "seriesCode: 1–40 characters A–Z, 0–9, - or _")
	}
	season, err := strconv.Atoi(get("season"))
	if err != nil || season < 1990 || season > 2200 {
		return false, errs.Validation("invalid_season", "season must be a year")
	}
	name := get("playerName")
	if name == "" {
		return false, errs.Validation("player_required", "playerName is required")
	}
	pts, err := decimal.NewFromString(get("points"))
	if err != nil || pts.IsNegative() {
		return false, errs.Validation("invalid_points", "points must be a number ≥ 0")
	}
	isNew := false
	sid, ok := created[code]
	if !ok {
		var source string
		err := tx.QueryRow(ctx, `SELECT id, source FROM golf.tournament_series WHERE property_id = $1 AND code = $2`, property, code).Scan(&sid, &source)
		switch {
		case dbtx.IsNoRows(err):
			sid = id.New()
			sname := get("seriesName")
			if sname == "" {
				sname = code
			}
			if _, err := tx.Exec(ctx, `INSERT INTO golf.tournament_series (id, property_id, code, name, season, status, source, legacy_ref, completed_at,
				created_by, updated_by) VALUES ($1,$2,$3,$4,$5,'completed','import',$3,now(),$6,$6)`, sid, property, code, sname, season, actor(ctx)); err != nil {
				return false, err
			}
			isNew = true
		case err != nil:
			return false, err
		case source != "import":
			return false, errs.Conflict("series_exists", "series "+code+" exists in OneClub and is not an imported season")
		}
		created[code] = sid
	}
	var customer *uuid.UUID
	if mn := get("memberNo"); mn != "" {
		mid, err := membership.CardLookup(ctx, tx, property, mn)
		if err != nil {
			return false, err
		}
		if mid != nil {
			if err := tx.QueryRow(ctx, `SELECT customer_id FROM membership.members WHERE id = $1`, *mid).Scan(&customer); err != nil {
				return false, err
			}
		}
	}
	if cc := get("customerCode"); customer == nil && cc != "" {
		var c uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM crm.customers WHERE property_id = $1 AND upper(code) = upper($2)`, property, cc).Scan(&c); err == nil {
			customer = &c
		} else if !dbtx.IsNoRows(err) {
			return false, err
		}
	}
	var rank *int
	if r := get("rank"); r != "" {
		n, err := strconv.Atoi(r)
		if err != nil || n < 1 {
			return false, errs.Validation("invalid_rank", "rank must be a positive number")
		}
		rank = &n
	}
	label := "-"
	if rank != nil {
		label = strconv.Itoa(*rank)
	}
	atoiOr := func(s string) int {
		n, _ := strconv.Atoi(s)
		return max(n, 0)
	}
	consent := strings.EqualFold(get("publicConsent"), "true") || get("publicConsent") == "1" || strings.EqualFold(get("publicConsent"), "yes")
	key := playerKey(customer, name)
	if _, err := tx.Exec(ctx, `INSERT INTO golf.tournament_series_standings (id, property_id, series_id, player_key, customer_id, player_name, rank, position_label,
		points, events, wins, public_consent) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10,$11,$12)
		ON CONFLICT (series_id, player_key) DO UPDATE SET player_name = EXCLUDED.player_name, rank = EXCLUDED.rank, position_label = EXCLUDED.position_label,
		points = EXCLUDED.points, events = EXCLUDED.events, wins = EXCLUDED.wins, public_consent = EXCLUDED.public_consent`, uuid.NewSHA1(sid, []byte(key)),
		property, sid, key, customer, name, rank, label, pts.String(), atoiOr(get("events")), atoiOr(get("wins")), consent); err != nil {
		return false, err
	}
	if rank != nil && *rank == 1 {
		if _, err := tx.Exec(ctx, `UPDATE golf.tournament_series SET champion_name = $2, champion_customer_id = $3 WHERE id = $1`, sid, name, customer); err != nil {
			return false, err
		}
	}
	return isNew, nil
}

// ── routes ────────────────────────────────────────────────────────────────

const seriesBase = "/api/v1/golf/tournament-series"

// ListSeries lists the series of a property.
func ListSeries(ctx context.Context, q dbtx.Querier, property uuid.UUID, status string, season int, public bool) ([]TournamentSeries, error) {
	return handle.List[TournamentSeries](q.Query(ctx, seriesSelect+` WHERE s.property_id = $1 AND ($2 = '' OR s.status = ANY(string_to_array($2, ',')))
		AND ($3 = 0 OR s.season = $3) AND (NOT $4 OR (s.public AND s.status IN ('active', 'completed'))) ORDER BY s.season DESC, s.name`, property, status, season, public))
}

func (m *Module) registerSeries(reg *route.Registry) {
	db := m.DB
	add := func(rt route.Route) {
		rt.Tag = "Golf Tournament Series"
		m.add(reg, rt)
	}
	add(route.Route{Method: http.MethodGet, Path: seriesBase, Summary: "Tournament Series (Order of Merit seasons)", Permission: "golf.tournament_series.view",
		Response: TournamentSeries{}, List: true, Query: []route.Param{{Name: "filter[status]"}, {Name: "season", Type: "integer"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[TournamentSeries], error) {
			lp := httpx.ParseList(r)
			return handle.Page(ListSeries(ctx, tx, handle.Property(ctx), lp.Filters["status"], handle.QueryInt(r, "season", 0), false))
		})})
	add(route.Route{Method: http.MethodPost, Path: seriesBase, Summary: "Create a Tournament Series (draft)", Permission: "golf.tournament_series.manage",
		Request: TournamentSeriesInput{}, Response: TournamentSeriesDetail{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, _ *http.Request, in TournamentSeriesInput) (TournamentSeriesDetail, error) {
			return m.CreateSeries(ctx, tx, handle.Property(ctx), in)
		})})
	add(route.Route{Method: http.MethodPost, Path: seriesBase + ":import", Summary: "Import historical Order of Merit standings (CSV; preview or commit)",
		Permission: "golf.tournament_series.manage", Request: TournamentSeriesImportInput{}, Response: TournamentSeriesImportResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, _ *http.Request, in TournamentSeriesImportInput) (TournamentSeriesImportResult, error) {
			return m.ImportSeries(ctx, tx, handle.Property(ctx), in)
		})})
	add(route.Route{Method: http.MethodGet, Path: seriesBase + "/{id}", Summary: "Tournament Series with events and points table",
		Permission: "golf.tournament_series.view", Response: TournamentSeriesDetail{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (TournamentSeriesDetail, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return TournamentSeriesDetail{}, err
			}
			return m.SeriesDetail(ctx, tx, handle.Property(ctx), sid)
		})})
	add(route.Route{Method: http.MethodPatch, Path: seriesBase + "/{id}", Summary: "Change a series (an active series is recalculated)",
		Permission: "golf.tournament_series.manage", Request: TournamentSeriesPatch{}, Response: TournamentSeriesDetail{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentSeriesPatch) (TournamentSeriesDetail, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return TournamentSeriesDetail{}, err
			}
			return m.UpdateSeries(ctx, tx, handle.Property(ctx), sid, in)
		})})
	add(route.Route{Method: http.MethodGet, Path: seriesBase + "/{id}/order-of-merit", Summary: "Order of Merit: standings (best N events, minimum events)",
		Permission: "golf.tournament_series.view", Response: OrderOfMerit{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (OrderOfMerit, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return OrderOfMerit{}, err
			}
			return m.OrderOfMerit(ctx, tx, handle.Property(ctx), sid)
		})})
	action := func(name, summary string, fn func(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID) (any, error), res any) {
		add(route.Route{Method: http.MethodPost, Path: seriesBase + "/{id}:" + name, Summary: summary, Permission: "golf.tournament_series.manage",
			Response: res, Status: http.StatusOK, Handler: m.write(http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request) (any, error) {
				var in handle.Empty
				if err := httpx.Decode(r, &in); err != nil {
					return nil, err
				}
				sid, err := handle.ID(r)
				if err != nil {
					return nil, err
				}
				return fn(ctx, tx, handle.Property(ctx), sid)
			})})
	}
	action("activate", "Activate: the points table is frozen and completed events are counted", func(ctx context.Context, tx pgx.Tx, p, sid uuid.UUID) (any, error) {
		return m.ActivateSeries(ctx, tx, p, sid)
	}, TournamentSeriesDetail{})
	action("recalculate", "Recalculate the points of every event", func(ctx context.Context, tx pgx.Tx, p, sid uuid.UUID) (any, error) {
		return m.RecalculateSeries(ctx, tx, p, sid)
	}, TournamentSeriesRecalcResult{})
	action("complete", "Complete the season: final standings, champion, Hall of Fame", func(ctx context.Context, tx pgx.Tx, p, sid uuid.UUID) (any, error) {
		return m.CompleteSeries(ctx, tx, p, sid)
	}, OrderOfMerit{})
	add(route.Route{Method: http.MethodPost, Path: seriesBase + "/{id}:cancel", Summary: "Cancel a series", Permission: "golf.tournament_series.manage",
		Request: TournamentReasonInput{}, Response: TournamentSeriesDetail{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentReasonInput) (TournamentSeriesDetail, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return TournamentSeriesDetail{}, err
			}
			return m.CancelSeries(ctx, tx, handle.Property(ctx), sid, in.Reason)
		})})
	add(route.Route{Method: http.MethodPost, Path: seriesBase + "/{id}/events", Summary: "Add a tournament to the series", Permission: "golf.tournament_series.manage",
		Request: TournamentSeriesEventInput{}, Response: TournamentSeriesEvent{}, Status: http.StatusCreated,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentSeriesEventInput) (TournamentSeriesEvent, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return TournamentSeriesEvent{}, err
			}
			return m.AddSeriesEvent(ctx, tx, handle.Property(ctx), sid, in)
		})})
	add(route.Route{Method: http.MethodPatch, Path: seriesBase + "/{id}/events/{eventId}", Summary: "Change an event of the series (sequence, weight, final)",
		Permission: "golf.tournament_series.manage", Request: TournamentSeriesEventPatch{}, Response: TournamentSeriesEvent{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentSeriesEventPatch) (TournamentSeriesEvent, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return TournamentSeriesEvent{}, err
			}
			eid, err := pathID(r, "eventId")
			if err != nil {
				return TournamentSeriesEvent{}, err
			}
			return m.UpdateSeriesEvent(ctx, tx, handle.Property(ctx), sid, eid, in)
		})})
	add(route.Route{Method: http.MethodDelete, Path: seriesBase + "/{id}/events/{eventId}", Summary: "Remove an event from the series",
		Permission: "golf.tournament_series.manage",
		Handler: m.write(http.StatusNoContent, func(ctx context.Context, tx pgx.Tx, r *http.Request) (any, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return nil, err
			}
			eid, err := pathID(r, "eventId")
			if err != nil {
				return nil, err
			}
			return nil, m.RemoveSeriesEvent(ctx, tx, handle.Property(ctx), sid, eid)
		})})
}
