package tournament

// PRD P5 FR-TRN-P5-05 Federation (§16 #11: PGI): the official handicap
// index is entered manually — one player at a time (P2) or in bulk here —
// and is the handicap of the next registrations (Tournament Policies
// federation_first); the results of a completed tournament are reported to
// the federation as a file (PGI layout) whose submissions are recorded. The
// federation API is a Should (no PGI API is available): method "api"
// answers that it is not configured.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/golf/experience"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/membership"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// FederationHandicapEntry is one official handicap index.
type FederationHandicapEntry struct {
	CustomerID       *uuid.UUID `json:"customerId,omitempty"`
	MemberNo         string     `json:"memberNo,omitempty" doc:"Member or card number instead of customerId"`
	HandicapIndex    string     `json:"handicapIndex"`
	FederationNumber string     `json:"federationNumber,omitempty" doc:"PGI membership number"`
}

// FederationHandicapImportInput enters official handicap indexes in bulk.
type FederationHandicapImportInput struct {
	Federation string                    `json:"federation,omitempty" doc:"Default PGI"`
	Entries    []FederationHandicapEntry `json:"entries"`
}

// FederationHandicapImportResult reports the bulk entry.
type FederationHandicapImportResult struct {
	Imported int                            `json:"imported"`
	Failed   int                            `json:"failed"`
	Errors   []TournamentHistoryImportError `json:"errors"`
}

// ImportFederationHandicaps records official (federation) handicap indexes.
func (m *Module) ImportFederationHandicaps(ctx context.Context, tx pgx.Tx, property uuid.UUID, in FederationHandicapImportInput) (FederationHandicapImportResult, error) {
	out := FederationHandicapImportResult{Errors: []TournamentHistoryImportError{}}
	if m.Experience == nil {
		return out, errs.Unavailable("the golf experience module is not available")
	}
	if len(in.Entries) == 0 || len(in.Entries) > 2000 {
		return out, handle.Invalid("entries", "invalid", "1–2,000 entries")
	}
	fed := strings.ToUpper(strings.TrimSpace(in.Federation))
	if fed == "" {
		fed = "PGI"
	}
	for i, e := range in.Entries {
		row := i + 1
		fail := func(code, msg string) {
			out.Failed++
			out.Errors = append(out.Errors, TournamentHistoryImportError{Row: row, Code: code, Message: msg})
		}
		customer := e.CustomerID
		if customer == nil && strings.TrimSpace(e.MemberNo) != "" {
			mid, err := membership.CardLookup(ctx, tx, property, e.MemberNo)
			if err != nil {
				return out, err
			}
			if mid != nil {
				var cid *uuid.UUID
				if err := tx.QueryRow(ctx, `SELECT customer_id FROM membership.members WHERE id = $1`, *mid).Scan(&cid); err != nil {
					return out, err
				}
				customer = cid
			}
		}
		if customer == nil {
			fail("player_not_found", "customer or member not found")
			continue
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.customers WHERE id = $1 AND property_id = $2)`, *customer, property).Scan(&ok); err != nil {
			return out, err
		}
		if !ok {
			fail("player_not_found", "customer of this property not found")
			continue
		}
		source := fed
		if n := strings.TrimSpace(e.FederationNumber); n != "" {
			source += " #" + n
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return out, err
		}
		if err := m.Experience.SetOfficialHandicap(ctx, sp, property, *customer, experience.OfficialHandicapInput{Index: e.HandicapIndex, Source: source}); err != nil {
			_ = sp.Rollback(ctx)
			if de, ok := errs.As(err); ok && de.Kind != errs.KindInternal {
				fail(de.Code, de.Message)
				continue
			}
			return out, err
		}
		if err := sp.Commit(ctx); err != nil {
			return out, err
		}
		out.Imported++
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: audit.ActionImport, EntityType: "golf.handicap", EntityID: property.String(),
		EntityLabel: fed + " handicap indexes", PropertyID: &property, After: map[string]any{"federation": fed, "imported": out.Imported, "failed": out.Failed}})
}

// FederationReportLine is a player of the federation result report.
type FederationReportLine struct {
	Position       string     `json:"position"`
	PlayerName     string     `json:"playerName"`
	CustomerID     *uuid.UUID `json:"customerId"`
	MemberNo       *string    `json:"memberNo"`
	HandicapIndex  *string    `json:"handicapIndex"`
	HandicapSource *string    `json:"handicapSource" doc:"federation = PGI index"`
	Rounds         []*int     `json:"rounds" doc:"Gross per round"`
	Total          *int       `json:"total"`
}

// FederationSubmission is a recorded report to the federation.
type FederationSubmission struct {
	ID          uuid.UUID `json:"id" db:"id"`
	Federation  string    `json:"federation" db:"federation"`
	Method      string    `json:"method" db:"method" enum:"manual_upload,api"`
	Status      string    `json:"status" db:"status" enum:"exported,submitted,failed"`
	Reference   *string   `json:"reference" db:"reference"`
	Rows        int       `json:"rows" db:"rows"`
	SubmittedAt time.Time `json:"submittedAt" db:"submitted_at"`
}

// FederationReport is the result report of a completed tournament.
type FederationReport struct {
	TournamentID uuid.UUID              `json:"tournamentId"`
	Code         string                 `json:"code"`
	Name         string                 `json:"name"`
	Federation   string                 `json:"federation"`
	CourseName   string                 `json:"courseName"`
	StartDate    string                 `json:"startDate"`
	EndDate      string                 `json:"endDate"`
	Format       string                 `json:"format"`
	Lines        []FederationReportLine `json:"lines"`
	Submissions  []FederationSubmission `json:"submissions"`
}

// FederationResultReport builds the federation report (gross results).
func (m *Module) FederationResultReport(ctx context.Context, q dbtx.Querier, property, tid uuid.UUID) (FederationReport, error) {
	t, err := tournamentAt(ctx, q, property, tid)
	if err != nil {
		return FederationReport{}, err
	}
	out := FederationReport{TournamentID: tid, Code: t.Code, Name: t.Name, Federation: "PGI", CourseName: t.CourseName, StartDate: t.StartDate, EndDate: t.EndDate,
		Format: t.Format, Lines: []FederationReportLine{}, Submissions: []FederationSubmission{}}
	if t.Status != "completed" {
		return out, errs.Conflict("not_final", "the results of "+t.Name+" are not final yet")
	}
	type row struct {
		Label    string     `db:"position_label"`
		Name     string     `db:"player_name"`
		Customer *uuid.UUID `db:"customer_id"`
		MemberNo *string    `db:"member_no"`
		Hcp      *string    `db:"handicap_index"`
		Source   *string    `db:"handicap_source"`
		Rounds   []byte     `db:"rounds"`
		Score    *int       `db:"score"`
	}
	rows, err := handle.List[row](q.Query(ctx, `SELECT x.position_label, x.player_name, x.customer_id, mm.code AS member_no,
		trim_scale(r.handicap_index)::text AS handicap_index, r.handicap_source, x.rounds, x.score
		FROM golf.tournament_results x LEFT JOIN golf.tournament_registrations r ON r.id = x.registration_id
		LEFT JOIN membership.members mm ON mm.id = r.member_id
		WHERE x.tournament_id = $1 AND x.category = 'gross' AND x.division_id IS NULL AND x.division_label IS NULL
		ORDER BY x.position NULLS LAST, x.player_name`, tid))
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		l := FederationReportLine{Position: r.Label, PlayerName: r.Name, CustomerID: r.Customer, MemberNo: r.MemberNo, HandicapIndex: r.Hcp, HandicapSource: r.Source,
			Rounds: []*int{}, Total: r.Score}
		var rs []struct {
			Gross *int `json:"gross"`
		}
		if err := json.Unmarshal(r.Rounds, &rs); err == nil {
			for _, x := range rs {
				l.Rounds = append(l.Rounds, x.Gross)
			}
		}
		out.Lines = append(out.Lines, l)
	}
	out.Submissions, err = handle.List[FederationSubmission](q.Query(ctx, `SELECT id, federation, method, status, reference, rows, submitted_at
		FROM golf.tournament_federation_reports WHERE tournament_id = $1 ORDER BY submitted_at DESC`, tid))
	if out.Submissions == nil {
		out.Submissions = []FederationSubmission{}
	}
	return out, err
}

// FederationSubmitInput records the report sent to the federation.
type FederationSubmitInput struct {
	Method    string `json:"method" enum:"manual_upload,api" doc:"manual_upload: the exported file was uploaded on the federation portal"`
	Reference string `json:"reference,omitempty" doc:"Federation receipt / upload reference"`
}

// SubmitFederationReport records the report of a completed tournament.
func (m *Module) SubmitFederationReport(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, in FederationSubmitInput) (FederationSubmission, error) {
	if err := oneOf("method", in.Method, "manual_upload", "api"); err != nil {
		return FederationSubmission{}, err
	}
	rep, err := m.FederationResultReport(ctx, tx, property, tid)
	if err != nil {
		return FederationSubmission{}, err
	}
	if in.Method == "api" {
		return FederationSubmission{}, errs.Conflict("federation_api_unavailable",
			"no federation API is configured (PGI: Should, PRD P5 §16 #11); export the report and upload it on the federation portal")
	}
	sub := FederationSubmission{ID: id.New(), Federation: rep.Federation, Method: in.Method, Status: "exported", Reference: nullStr(in.Reference),
		Rows: len(rep.Lines), SubmittedAt: now()}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.tournament_federation_reports (id, property_id, tournament_id, federation, method, status, reference, rows, payload,
		submitted_at, submitted_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, sub.ID, property, tid, sub.Federation, sub.Method, sub.Status, sub.Reference,
		sub.Rows, jsonOf(rep.Lines), sub.SubmittedAt, actor(ctx)); err != nil {
		return sub, err
	}
	return sub, record(ctx, tx, "golf.tournament", tid, rep.Code+" federation report", "federation_report", property, nil, sub, "")
}

func (m *Module) registerFederation(reg *route.Registry) {
	db := m.DB
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/golf/federation/handicaps:import",
		Summary: "Enter official (PGI) handicap indexes in bulk", Permission: "golf.federation.manage", Tag: "Golf Federation",
		Request: FederationHandicapImportInput{}, Response: FederationHandicapImportResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, _ *http.Request, in FederationHandicapImportInput) (FederationHandicapImportResult, error) {
			return m.ImportFederationHandicaps(ctx, tx, handle.Property(ctx), in)
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: base + "/{id}/federation-report", Summary: "Federation result report (PGI layout) with its submissions",
		Permission: "golf.tournament.view", Tag: "Golf Federation", Response: FederationReport{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (FederationReport, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return FederationReport{}, err
			}
			return m.FederationResultReport(ctx, tx, handle.Property(ctx), tid)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: base + "/{id}/federation-report:submit", Summary: "Record the result report sent to the federation",
		Permission: "golf.federation.manage", Tag: "Golf Federation", Request: FederationSubmitInput{}, Response: FederationSubmission{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in FederationSubmitInput) (FederationSubmission, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return FederationSubmission{}, err
			}
			return m.SubmitFederationReport(ctx, tx, handle.Property(ctx), tid, in)
		})})
}
