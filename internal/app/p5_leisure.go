package app

// PRD P5 EP-22–23: advanced package management (multi-business, capacity & time blocks, profitability) and advanced tournament (team formats, series & order of merit, history, federation). internal/app/p5.go calls these functions; the area fills them.
//
// Commercial sits below the business lines (Technical Doc §4.2), so the
// package rules run inside the P3 booking transaction through the hook of
// the commercial package, and the package costs (Package Profitability) are
// refreshed from commercial's own events (package booked, consumed,
// cancelled). Tournaments count in their Order of Merit when they are
// finalized (golf.tournament_finalized), after the team results of a team
// tournament are frozen.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/commercial"
	"oneclub/internal/golf/tournament"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/storage"
	"oneclub/internal/reporting"
)

// p5Leisure holds the services of the area.
type p5Leisure struct {
	Packages   *commercial.P3Module // P3 package engine extended by P5 (EP-22)
	Tournament *tournament.Module   // P3 tournament extended by P5 (EP-23)
}

func init() {
	// FR-RPT-P5-03: Package Profitability Report, Tournament Series Report.
	Reports = append(Reports, reporting.LeisureReports()...)
}

func p5LeisureContributions() []catalog.Contribution {
	return []catalog.Contribution{commercial.P5Contribution(), tournament.P5Contribution(), reporting.LeisureContribution()}
}
func p5LeisureDocumentTypes() []provision.DocumentType { return nil }
func p5LeisureTemplates() []provision.Template         { return tournament.P5Templates() }

// buildP5Leisure wires routes, hooks, approval decisions and jobs.
func (a *App) buildP5Leisure(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
	a.Leisure.Packages = a.Promo.Module
	a.Leisure.Tournament = a.Tournament.Module
	a.Promo.Module.RegisterP5(reg, a.Engine)
	a.Tournament.Module.RegisterP5(reg, a.Engine)
}

// subscribeP5Leisure registers the event subscribers.
func (a *App) subscribeP5Leisure() {
	a.Bus.Subscribe(commercial.EventPackageBooked, "commercial.package_costs_booked", commercial.OnPackageCostEvent)
	a.Bus.Subscribe(commercial.EventPackageConsumed, "commercial.package_costs_consumed", commercial.OnPackageCostEvent)
	a.Bus.Subscribe(commercial.EventPackageCancelled, "commercial.package_costs_cancelled", commercial.OnPackageCostEvent)
	a.Bus.Subscribe(tournament.EventFinalized, "golf.tournament_series_points", a.Tournament.Module.OnTournamentFinalized)
}

// demoP5Leisure seeds the demo data of the area.
func demoP5Leisure(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	return seedLeisureDemo(ctx, tx, property)
}

// seedLeisureDemo seeds the demo of EP-22/23 on the MAIN property
// (idempotent): the multi-business package Golf, Spa & Dinner Day with a
// choice group, sequence, capacity and cost rules (published); the team
// formats (Scramble and Four-ball active, Foursomes and Texas Scramble by
// configuration); the Order of Merit points table; tournament history of
// 2025 and the Club Championship Series 2026 with three events and its
// standings.
func seedLeisureDemo(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	ctx = reqctx.WithProperty(ctx, property)
	if err := seedLeisurePackage(ctx, tx, property); err != nil {
		return err
	}
	for _, f := range []struct {
		code, name, typ string
		size, best      int
		allow           []int32
		status          string
	}{{"SCRAMBLE4", "Scramble (4 players)", "scramble", 4, 1, []int32{25, 20, 15, 10}, "active"},
		{"FOURBALL", "Four-ball Better Ball", "four_ball", 2, 1, []int32{90}, "active"},
		{"FOURSOMES", "Foursomes", "foursomes", 2, 1, []int32{50, 50}, "inactive"},
		{"TEXAS4", "Texas Scramble (4 players)", "texas_scramble", 4, 1, []int32{10, 10, 10, 10}, "inactive"}} {
		if _, err := tx.Exec(ctx, `INSERT INTO golf.team_formats (id, property_id, code, name, format_type, team_size, scores_per_hole, allowances, status)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (property_id, code) DO NOTHING`, id.New(), property, f.code, f.name, f.typ, f.size, f.best, f.allow,
			f.status); err != nil {
			return err
		}
	}
	var table uuid.UUID
	if err := tx.QueryRow(ctx, `INSERT INTO golf.series_points_tables (id, property_id, code, name, points, participation_points, tie_rule, description)
		VALUES ($1,$2,'OOM-STD','Order of Merit (25 · 18 · 15 …)','{25,18,15,12,10,8,6,4,2,1}',1,'split','Top 10 score points; every other finisher 1 point')
		ON CONFLICT (property_id, code) DO UPDATE SET code = EXCLUDED.code RETURNING id`, id.New(), property).Scan(&table); err != nil {
		return err
	}
	var exists, course bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.tournament_series WHERE property_id = $1 AND code = 'CCS-2026'),
		EXISTS (SELECT 1 FROM golf.courses WHERE property_id = $1 AND code = $2)`, property, DemoCourseCode).Scan(&exists, &course); err != nil {
		return err
	}
	if exists || !course {
		return nil // seeded already, or no golf demo
	}
	players := []string{"Budi Santoso", "Andi Wijaya", "Rina Kartika", "Dewi Lestari", "Hendra Gunawan", "Siti Rahma", "Agus Pratama", "Lina Hartono"}
	var b strings.Builder
	b.WriteString("tournamentRef,tournamentName,startDate,tournamentType,format,courseCode,category,position,playerName,score,publicConsent\n")
	for _, e := range []struct {
		ref, name, date, typ string
		order                []int
	}{{"CC2025", "Club Championship", "2025-09-20", "club_championship", []int{1, 0, 3, 2, 5, 4, 7, 6}},
		{"CCS26-1", "Club Championship Series — Leg 1", "2026-03-14", "club", []int{0, 1, 2, 3, 4, 5, 6, 7}},
		{"CCS26-2", "Club Championship Series — Leg 2", "2026-05-16", "club", []int{2, 0, 1, 4, 3, 6, 5, 7}},
		{"CCS26-3", "Club Championship Series — Final", "2026-07-18", "club", []int{0, 2, 1, 3, 5, 4, 7, 6}}} {
		for pos, p := range e.order {
			fmt.Fprintf(&b, "%s,%s,%s,%s,stroke_play,%s,gross,%d,%s,%d,%t\n", e.ref, e.name, e.date, e.typ, DemoCourseCode, pos+1, players[p], 74+pos*2, p%2 == 0)
		}
	}
	m := &tournament.Module{}
	if _, err := m.ImportHistory(ctx, tx, property, tournament.TournamentHistoryImportInput{Mode: "commit", CSV: b.String()}); err != nil {
		return err
	}
	s, err := m.CreateSeries(ctx, tx, property, tournament.TournamentSeriesInput{Code: "CCS-2026", Name: "Club Championship Series", Season: 2026,
		Description: "Three legs; the final counts double", PointsTableID: &table, Public: true})
	if err != nil {
		return err
	}
	for _, ref := range []string{"CCS26-1", "CCS26-2", "CCS26-3"} {
		var tid uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM golf.tournaments WHERE property_id = $1 AND legacy_ref = $2`, property, ref).Scan(&tid); err != nil {
			return err
		}
		final := ref == "CCS26-3"
		if _, err := m.AddSeriesEvent(ctx, tx, property, s.ID, tournament.TournamentSeriesEventInput{TournamentID: tid, IsFinal: &final}); err != nil {
			return err
		}
	}
	_, err = m.ActivateSeries(ctx, tx, property, s.ID)
	return err
}

// seedLeisurePackage seeds the multi-business package Golf, Spa & Dinner Day.
func seedLeisurePackage(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	pkg := id.New()
	tag, err := tx.Exec(ctx, `INSERT INTO commercial.packages (id, property_id, code, name, package_type, description, pricing_mode, price, min_pax, max_pax,
		start_time, allocation_method, public, status) VALUES ($1,$2,'GOLF-SPA-DAY','Golf, Spa & Dinner Day','golf_day',
		'18 holes in the morning, then a spa treatment or a tennis lesson and dinner at the clubhouse','per_pax',2450000,1,8,'07:00','standalone',false,'draft')
		ON CONFLICT (property_id, code) DO NOTHING`, pkg, property)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	type comp struct {
		typ, name, line, rev, start string
		price, dur                  int
	}
	comps := []comp{{"service", "Golf 18 Holes with Caddy", "golf", "green_fee", "07:00", 1300000, 300},
		{"service", "Spa Massage 60'", "sportclub", "other", "", 650000, 60},
		{"service", "Tennis Lesson 60'", "sportclub", "other", "", 450000, 60},
		{"fnb", "Dinner at the Clubhouse", "pos", "fnb", "18:30", 400000, 90}}
	ids := make([]uuid.UUID, len(comps))
	for i, c := range comps {
		ids[i] = id.New()
		var st *string
		if c.start != "" {
			st = &c.start
		}
		if _, err := tx.Exec(ctx, `INSERT INTO commercial.package_components (id, property_id, package_id, seq, component_type, name, quantity, per_pax,
			standalone_price, revenue_component, business_line, start_time, duration_minutes) VALUES ($1,$2,$3,$4,$5,$6,1,true,$7,$8,$9,$10::time,$11)`,
			ids[i], property, pkg, i+1, c.typ, c.name, c.price, c.rev, c.line, st, c.dur); err != nil {
			return err
		}
	}
	// spa or tennis (choice), after golf with a 60-minute gap, served 10:00–18:00
	for _, k := range []int{1, 2} {
		if _, err := tx.Exec(ctx, `INSERT INTO commercial.package_component_rules (id, property_id, component_id, choice_group, choice_pick, after_component_id,
			min_gap_minutes, max_gap_minutes, window_start, window_end) VALUES ($1,$2,$3,'WELLNESS',1,$4,60,240,'10:00','18:00')`, id.New(), property, ids[k],
			ids[0]); err != nil {
			return err
		}
	}
	// capacity: 16 pax per day, spa 4 guests per hour, no package on 17 August
	year := time.Now().Year()
	for _, c := range []struct {
		comp         *uuid.UUID
		typ, basis   string
		quota, block *int
		from, to     string
		reason       string
		wstart, wend *string
	}{{nil, "allotment", "pax", leisurePtr(16), nil, fmt.Sprintf("%d-01-01", year), "", "Package allotment per day", nil, nil},
		{&ids[1], "time_block", "units", leisurePtr(4), leisurePtr(60), fmt.Sprintf("%d-01-01", year), "", "Four spa therapists", leisurePtr("10:00"), leisurePtr("18:00")},
		{nil, "blackout", "bookings", nil, nil, fmt.Sprintf("%d-08-17", year), fmt.Sprintf("%d-08-17", year), "Independence Day tournament", nil, nil}} {
		var to *string
		if c.to != "" {
			to = &c.to
		}
		if _, err := tx.Exec(ctx, `INSERT INTO commercial.package_capacities (id, property_id, package_id, component_id, capacity_type, date_from, date_to, quota,
			basis, block_minutes, window_start, window_end, reason) VALUES ($1,$2,$3,$4,$5,$6::date,$7::date,$8,$9,$10,$11::time,$12::time,$13)`, id.New(), property,
			pkg, c.comp, c.typ, c.from, to, c.quota, c.basis, c.block, c.wstart, c.wend, c.reason); err != nil {
			return err
		}
	}
	// direct costs: caddy fee per player, spa commission 20 %, tennis coach
	for _, c := range []struct {
		comp            uuid.UUID
		typ, basis, amt string
	}{{ids[0], "caddy_fee", "per_unit", "200000"}, {ids[1], "commission", "percent_of_revenue", "20"}, {ids[2], "labour", "per_unit", "150000"}} {
		if _, err := tx.Exec(ctx, `INSERT INTO commercial.package_cost_rules (id, property_id, package_id, component_id, cost_type, basis, amount)
			VALUES ($1,$2,$3,$4,$5,$6,$7::numeric)`, id.New(), property, pkg, c.comp, c.typ, c.basis, c.amt); err != nil {
			return err
		}
	}
	_, err = (&commercial.P3Module{}).Publish(ctx, tx, pkg)
	return err
}

func leisurePtr[T any](v T) *T { return &v }
