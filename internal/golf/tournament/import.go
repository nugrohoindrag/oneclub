package tournament

// Tournament history import (PRD P3 EP-25 FR-MIG-P3-03): the results of the
// last two years of club tournaments (Rhapsody / the golf office's Excel,
// PRD P3 §16 #17) become a read-only archive — completed tournaments with
// source "import" and their results per category and division — and the
// winners become Hall of Fame champions (public only with consent, P2).
// CSV with a header row; preview validates without saving; re-importing a
// file is idempotent (tournaments by tournamentRef, results by their legacy
// key, Hall of Fame entries by the result).

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/golf/experience"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/membership"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/org"
)

// HistoryColumns are the accepted CSV columns.
var HistoryColumns = []string{"tournamentRef", "tournamentName", "startDate", "endDate", "tournamentType", "format", "courseCode", "category",
	"division", "hallOfFameDivision", "position", "positionLabel", "playerName", "memberNo", "customerCode", "score", "toPar", "publicConsent"}

// HistoryImportInput is a CSV import of tournament history.
type TournamentHistoryImportInput struct {
	Mode string `json:"mode" enum:"preview,commit" doc:"preview validates without saving"`
	CSV  string `json:"csv" doc:"Header row with: tournamentRef, tournamentName, startDate (YYYY-MM-DD), endDate, tournamentType (club|club_championship|corporate|invitational|sponsor|charity), format (stroke_play|stableford), courseCode, category (gross|net|stableford), division, hallOfFameDivision (men|ladies|senior|junior|open), position, positionLabel, playerName, memberNo, customerCode, score, toPar, publicConsent (true|false)"`
}

// HistoryImportError is a rejected row.
type TournamentHistoryImportError struct {
	Row     int    `json:"row" doc:"1-based line (header = 1)"`
	Field   string `json:"field,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// HistoryImportResult reports an import.
type TournamentHistoryImportResult struct {
	Mode        string                         `json:"mode" enum:"preview,commit"`
	TotalRows   int                            `json:"totalRows"`
	Tournaments int                            `json:"tournaments" doc:"Tournaments created"`
	Results     int                            `json:"results" doc:"Result rows created or updated"`
	Champions   int                            `json:"champions" doc:"Hall of Fame champion entries"`
	Failed      int                            `json:"failed"`
	Errors      []TournamentHistoryImportError `json:"errors"`
}

var nonCode = regexp.MustCompile(`[^A-Z0-9_-]+`)

// historyNS seeds the stable ids of imported results.
var historyNS = uuid.MustParse("6f1d5c1e-3a8b-4e0f-9a57-2b8f7c1d0e42")

// ImportHistory imports tournament history and champions.
func (m *Module) ImportHistory(ctx context.Context, tx pgx.Tx, property uuid.UUID, in TournamentHistoryImportInput) (TournamentHistoryImportResult, error) {
	res := TournamentHistoryImportResult{Mode: in.Mode, Errors: []TournamentHistoryImportError{}}
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
		h = strings.TrimSpace(h)
		matched := false
		for _, c := range HistoryColumns {
			if strings.EqualFold(h, c) {
				cols[c], matched = i, true
			}
		}
		if !matched && h != "" {
			res.Errors = append(res.Errors, TournamentHistoryImportError{Row: 1, Field: h, Code: "unknown_column", Message: "unknown column " + h})
		}
	}
	for _, c := range []string{"tournamentRef", "tournamentName", "startDate", "category", "playerName"} {
		if _, ok := cols[c]; !ok {
			res.Errors = append(res.Errors, TournamentHistoryImportError{Row: 1, Field: c, Code: "missing_column", Message: "column " + c + " is required"})
		}
	}
	if len(res.Errors) > 0 {
		return res, nil
	}
	outer, err := tx.Begin(ctx) // preview rolls everything back
	if err != nil {
		return res, err
	}
	defer func() { _ = outer.Rollback(ctx) }()
	created := map[string]bool{}
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
		empty := true
		for _, c := range rec {
			empty = empty && strings.TrimSpace(c) == ""
		}
		if empty {
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
		newT, champion, rowErr := m.importHistoryRow(ctx, sp, property, get)
		if rowErr != nil {
			_ = sp.Rollback(ctx)
			res.Failed++
			if de, ok := errs.As(rowErr); ok && de.Kind != errs.KindInternal {
				if len(de.Fields) == 0 {
					res.Errors = append(res.Errors, TournamentHistoryImportError{Row: line, Code: de.Code, Message: de.Message})
				}
				for _, f := range de.Fields {
					res.Errors = append(res.Errors, TournamentHistoryImportError{Row: line, Field: f.Field, Code: f.Code, Message: f.Message})
				}
				continue
			}
			return res, rowErr
		}
		if err := sp.Commit(ctx); err != nil {
			return res, err
		}
		res.Results++
		if newT != "" && !created[newT] {
			created[newT] = true
			res.Tournaments++
		}
		if champion {
			res.Champions++
		}
	}
	if in.Mode == "commit" {
		if err := outer.Commit(ctx); err != nil {
			return res, err
		}
	}
	return res, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: audit.ActionImport, EntityType: "golf.tournament", EntityID: property.String(),
		EntityLabel: "Tournament history import (" + in.Mode + ")", PropertyID: &property, After: map[string]any{"mode": in.Mode, "rows": res.TotalRows,
			"tournaments": res.Tournaments, "results": res.Results, "champions": res.Champions, "failed": res.Failed}})
}

// importHistoryRow imports one result row; it returns the legacy reference
// of a tournament it created and whether a champion entry was made.
func (m *Module) importHistoryRow(ctx context.Context, tx pgx.Tx, property uuid.UUID, get func(string) string) (string, bool, error) {
	ref := get("tournamentRef")
	if ref == "" {
		return "", false, handle.Invalid("tournamentRef", "required", "tournamentRef is required")
	}
	player := get("playerName")
	if player == "" {
		return "", false, handle.Invalid("playerName", "required", "playerName is required")
	}
	category := strings.ToLower(get("category"))
	if err := oneOf("category", category, "gross", "net", "stableford"); err != nil {
		return "", false, err
	}
	tid, typ, endDate, name, newRef, err := m.historyTournament(ctx, tx, property, ref, get)
	if err != nil {
		return "", false, err
	}
	var pos *int
	if v := get("position"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return "", false, handle.Invalid("position", "invalid", "a whole number from 1")
		}
		pos = &n
	}
	label := get("positionLabel")
	if label == "" {
		label = "-"
		if pos != nil {
			label = itoa(*pos)
		}
	}
	intOf := func(field string) (*int, error) {
		v := get(field)
		if v == "" {
			return nil, nil
		}
		n, err := strconv.Atoi(strings.TrimPrefix(v, "+"))
		if err != nil {
			return nil, handle.Invalid(field, "invalid", "a whole number")
		}
		return &n, nil
	}
	score, err := intOf("score")
	if err != nil {
		return "", false, err
	}
	toPar, err := intOf("toPar")
	if err != nil {
		return "", false, err
	}
	var customer *uuid.UUID
	if v := get("memberNo"); v != "" {
		mid, err := membership.CardLookup(ctx, tx, property, v)
		if err != nil {
			return "", false, err
		}
		if mid == nil {
			if err := tx.QueryRow(ctx, `SELECT id FROM membership.members WHERE property_id = $1 AND upper(code) = upper($2)`, property, v).Scan(&mid); err != nil &&
				!dbtx.IsNoRows(err) {
				return "", false, err
			}
		}
		if mid == nil {
			return "", false, handle.Invalid("memberNo", "not_found", "member "+v+" not found")
		}
		if err := tx.QueryRow(ctx, `SELECT customer_id FROM membership.members WHERE id = $1`, *mid).Scan(&customer); err != nil {
			return "", false, err
		}
	} else if v := get("customerCode"); v != "" {
		var cid uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM crm.customers WHERE property_id = $1 AND upper(code) = upper($2)`, property, v).Scan(&cid); err != nil {
			if dbtx.IsNoRows(err) {
				return "", false, handle.Invalid("customerCode", "not_found", "customer "+v+" not found")
			}
			return "", false, err
		}
		customer = &cid
	}
	division := get("division")
	key := strings.ToLower(ref + "|" + category + "|" + division + "|" + player)
	rid := uuid.NewSHA1(historyNS, []byte(property.String()+"|"+key))
	if _, err := tx.Exec(ctx, `INSERT INTO golf.tournament_results (id, property_id, tournament_id, customer_id, player_name, division_label, category, position,
		position_label, tied, score, to_par, legacy_key) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (tournament_id, legacy_key) WHERE legacy_key IS NOT NULL DO UPDATE SET customer_id = EXCLUDED.customer_id, position = EXCLUDED.position,
		position_label = EXCLUDED.position_label, tied = EXCLUDED.tied, score = EXCLUDED.score, to_par = EXCLUDED.to_par`,
		rid, property, tid, customer, player, nullStr(division), category, pos, label, strings.HasPrefix(strings.ToUpper(label), "T"), score, toPar, key); err != nil {
		return "", false, err
	}
	if pos == nil || *pos != 1 || m.Experience == nil {
		return newRef, false, nil
	}
	hofCategory := "tournament_champion"
	if typ == "club_championship" {
		hofCategory = "club_champion"
	}
	var hofDiv *string
	if v := strings.ToLower(get("hallOfFameDivision")); v != "" {
		if err := oneOf("hallOfFameDivision", v, "men", "ladies", "senior", "junior", "open"); err != nil {
			return "", false, err
		}
		hofDiv = &v
	} else if division == "" && typ == "club_championship" {
		hofDiv = ptr("open")
	}
	end, _ := time.Parse("2006-01-02", endDate)
	catLabel := map[string]string{"gross": "Gross Champion", "net": "Net Champion", "stableford": "Stableford Champion"}[category]
	title := fmt.Sprintf("%s %d — %s", name, end.Year(), catLabel)
	if division != "" {
		title = fmt.Sprintf("%s %d — %s %s", name, end.Year(), division, catLabel)
	}
	consent := strings.EqualFold(get("publicConsent"), "true") || get("publicConsent") == "1"
	if _, err := m.Experience.TournamentHallOfFameEntry(ctx, tx, property, experience.TournamentHallOfFameInput{Category: hofCategory, Title: title,
		Year: end.Year(), Division: hofDiv, CustomerID: customer, PlayerName: player, Score: score, AchievedOn: end, SourceID: rid, Consent: consent}); err != nil {
		return "", false, err
	}
	return newRef, true, nil
}

// historyTournament finds or creates the archived tournament of a row.
func (m *Module) historyTournament(ctx context.Context, tx pgx.Tx, property uuid.UUID, ref string, get func(string) string) (uuid.UUID, string, string, string,
	string, error) {
	var tid uuid.UUID
	var typ, end, name string
	err := tx.QueryRow(ctx, `SELECT id, tournament_type, end_date::text, name FROM golf.tournaments WHERE property_id = $1 AND legacy_ref = $2`, property, ref).
		Scan(&tid, &typ, &end, &name)
	if err == nil {
		return tid, typ, end, name, "", nil
	}
	if !dbtx.IsNoRows(err) {
		return tid, "", "", "", "", err
	}
	name = get("tournamentName")
	if name == "" {
		return tid, "", "", "", "", handle.Invalid("tournamentName", "required", "tournamentName is required")
	}
	start, err := time.Parse("2006-01-02", get("startDate"))
	if err != nil {
		return tid, "", "", "", "", handle.Invalid("startDate", "invalid_date", "YYYY-MM-DD")
	}
	endD := start
	if v := get("endDate"); v != "" {
		if endD, err = time.Parse("2006-01-02", v); err != nil || endD.Before(start) {
			return tid, "", "", "", "", handle.Invalid("endDate", "invalid_date", "YYYY-MM-DD on or after the start")
		}
	}
	typ = strings.ToLower(get("tournamentType"))
	if typ == "" {
		typ = "club"
	}
	if err := oneOf("tournamentType", typ, "club", "club_championship", "corporate", "invitational", "sponsor", "charity"); err != nil {
		return tid, "", "", "", "", err
	}
	format := strings.ToLower(get("format"))
	if format == "" {
		format = "stroke_play"
		if strings.EqualFold(get("category"), "stableford") {
			format = "stableford"
		}
	}
	if err := oneOf("format", format, "stroke_play", "stableford"); err != nil {
		return tid, "", "", "", "", err
	}
	var course uuid.UUID
	if code := get("courseCode"); code != "" {
		err = tx.QueryRow(ctx, `SELECT id FROM golf.courses WHERE property_id = $1 AND upper(code) = upper($2)`, property, code).Scan(&course)
	} else {
		err = tx.QueryRow(ctx, `SELECT c.id FROM golf.courses c WHERE c.property_id = $1 AND c.archived_at IS NULL
			AND EXISTS (SELECT 1 FROM golf.playing_routes r WHERE r.course_id = c.id AND r.status = 'active') ORDER BY c.created_at, c.code LIMIT 1`, property).
			Scan(&course)
	}
	if err != nil {
		if dbtx.IsNoRows(err) {
			return tid, "", "", "", "", handle.Invalid("courseCode", "not_found", "golf course not found")
		}
		return tid, "", "", "", "", err
	}
	route, err := defaultRoute(ctx, tx, course)
	if err != nil {
		return tid, "", "", "", "", err
	}
	code := nonCode.ReplaceAllString(strings.ToUpper(ref), "-")
	code = strings.Trim(code, "-_")
	if len(code) > 34 {
		code = code[:34]
	}
	code = "HIST-" + code
	var taken bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.tournaments WHERE property_id = $1 AND code = $2)`, property, code).Scan(&taken); err != nil {
		return tid, "", "", "", "", err
	}
	if taken || !codeRe.MatchString(code) {
		code = "HIST-" + strings.ToUpper(uuid.NewSHA1(historyNS, []byte(ref)).String()[:8])
	}
	cur, err := org.Currency(ctx, tx)
	if err != nil {
		return tid, "", "", "", "", err
	}
	tid = uuid.NewSHA1(historyNS, []byte(property.String()+"|tournament|"+ref))
	if _, err := tx.Exec(ctx, `INSERT INTO golf.tournaments (id, property_id, code, name, tournament_type, course_id, playing_route_id, start_date, end_date,
		format, eligibility, field_size, status, current_round, finalized_at, source, legacy_ref, currency, notes, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'members_and_guests',1,'completed',1,($9::date + 1)::timestamptz,'import',$11,$12,
		'Imported tournament history (read-only archive)',$13,$13)`, tid, property, code, name, typ, course, route, start.Format("2006-01-02"),
		endD.Format("2006-01-02"), format, ref, cur, actor(ctx)); err != nil {
		return tid, "", "", "", "", err
	}
	if err := record(ctx, tx, "golf.tournament", tid, code+" · "+name, audit.ActionImport, property, nil,
		map[string]any{"legacyRef": ref, "startDate": start.Format("2006-01-02"), "source": "import"}, ""); err != nil {
		return tid, "", "", "", "", err
	}
	return tid, typ, endD.Format("2006-01-02"), name, ref, nil
}
