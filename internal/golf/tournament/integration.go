package tournament

// Integration of tournaments with the other lines (docs/p3-p4-contracts.md):
// a corporate tournament sold through CRM Sales (quotation line
// "tournament") becomes a draft tournament with the deposit folio and the
// payment schedule of the quotation (FR-QUO-06/07, once per quotation); the
// banquet event of a tournament (venue, catering) is followed through the
// banquet.event_* events (FR-TRN-13, no import of banquet); the Customer 360
// Tournament section (FR-C360-01); periodic jobs (registration deadline,
// unpaid online registrations).

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/notification"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/outbox"
)

// Consumed events of other modules (docs/p3-p4-contracts.md).
const (
	EventBanquetConfirmed = "banquet.event_confirmed"
	EventBanquetCancelled = "banquet.event_cancelled"
)

// AcceptedQuotation is the part of crm.quotation_accepted a tournament needs.
type AcceptedQuotation struct {
	QuotationID        uuid.UUID  `json:"quotationId"`
	Number             string     `json:"number"`
	PropertyID         uuid.UUID  `json:"propertyId"`
	Line               string     `json:"line"`
	CustomerID         *uuid.UUID `json:"customerId"`
	CorporateAccountID *uuid.UUID `json:"corporateAccountId"`
	EventDate          *string    `json:"eventDate"`
	EndDate            *string    `json:"endDate"`
	Pax                *int       `json:"pax"`
	Title              string     `json:"title"`
	Currency           string     `json:"currency"`
	Service            string     `json:"service"`
	Tax                string     `json:"tax"`
	Total              string     `json:"total"`
	PaymentTerms       []struct {
		Label   string `json:"label"`
		Percent string `json:"percent"`
		Amount  string `json:"amount"`
		DueDate string `json:"dueDate"`
	} `json:"paymentTerms"`
}

// OnQuotationAccepted converts an accepted quotation of the line
// "tournament" into a draft corporate tournament (idempotent per quotation).
func (m *Module) OnQuotationAccepted(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p AcceptedQuotation
	if err := e.Decode(&p); err != nil {
		return nil //nolint:nilerr // foreign payload
	}
	if p.Line != "tournament" {
		return nil
	}
	property := p.PropertyID
	if property == uuid.Nil && e.PropertyID != nil {
		property = *e.PropertyID
	}
	ctx = reqctx.WithProperty(dbtx.System(ctx), property)
	_, err := m.ConvertQuotation(ctx, tx, property, p)
	return err
}

// ConvertQuotation creates the corporate tournament of a quotation; a second
// delivery returns the existing tournament.
func (m *Module) ConvertQuotation(ctx context.Context, tx pgx.Tx, property uuid.UUID, p AcceptedQuotation) (uuid.UUID, error) {
	var existing uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM golf.tournaments WHERE quotation_id = $1`, p.QuotationID).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !dbtx.IsNoRows(err) {
		return uuid.Nil, err
	}
	pol, _, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return uuid.Nil, err
	}
	var course uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT c.id FROM golf.courses c WHERE c.property_id = $1 AND c.status = 'active' AND c.archived_at IS NULL
		AND EXISTS (SELECT 1 FROM golf.playing_routes r WHERE r.course_id = c.id AND r.status = 'active' AND r.archived_at IS NULL)
		ORDER BY c.created_at, c.code LIMIT 1`, property).Scan(&course); err != nil {
		if dbtx.IsNoRows(err) {
			return uuid.Nil, fmt.Errorf("quotation %s: the property has no active golf course for the tournament", p.Number)
		}
		return uuid.Nil, err
	}
	loc := location(ctx, tx, property)
	start := localDate(now(), loc).AddDate(0, 0, 30)
	if p.EventDate != nil {
		if d, err := time.Parse("2006-01-02", *p.EventDate); err == nil {
			start = d
		}
	}
	end := start
	if p.EndDate != nil {
		if d, err := time.Parse("2006-01-02", *p.EndDate); err == nil && !d.Before(start) && d.Sub(start) < 8*24*time.Hour {
			end = d
		}
	}
	var rounds []TournamentRoundInput
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		rounds = append(rounds, TournamentRoundInput{PlayDate: d.Format("2006-01-02"), StartTime: pol.DefaultStartTime})
	}
	field := 72
	if p.Pax != nil && *p.Pax > 0 {
		field = min(*p.Pax, 288)
	}
	name := strings.TrimSpace(p.Title)
	if name == "" {
		name = "Corporate Tournament " + p.Number
	}
	d, err := m.Create(ctx, tx, property, TournamentInput{Name: name, TournamentType: "corporate", CourseID: course, Format: pol.DefaultFormat,
		Eligibility: "invitation", FieldSize: field, CustomerID: p.CustomerID, CorporateAccountID: p.CorporateAccountID, Rounds: rounds,
		Notes: "Created from accepted quotation " + p.Number})
	if err != nil {
		return uuid.Nil, err
	}
	total, _ := decimal.NewFromString(p.Total)
	var folio, schedule *uuid.UUID
	if total.IsPositive() && p.CustomerID != nil && m.Billing != nil {
		f, s, err := m.quotationBilling(ctx, tx, property, d.Tournament, p, total)
		if err != nil {
			return uuid.Nil, err
		}
		folio, schedule = &f, &s
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournaments SET quotation_id = $2, quotation_number = $3, folio_id = $4, schedule_id = $5,
		quotation_total = $6::numeric, quotation_service = nullif($7, '')::numeric, quotation_tax = nullif($8, '')::numeric WHERE id = $1`,
		d.ID, p.QuotationID, p.Number, folio, schedule, total.String(), p.Service, p.Tax); err != nil {
		return uuid.Nil, err
	}
	if err := record(ctx, tx, "golf.tournament", d.ID, d.Code+" · "+d.Name, "convert_quotation", property, nil,
		map[string]any{"quotationId": p.QuotationID, "quotation": p.Number, "fieldSize": field, "total": total.String(), "scheduleId": schedule}, ""); err != nil {
		return uuid.Nil, err
	}
	if m.Notify != nil {
		users, err := notification.UserIDsWithPermission(ctx, tx, "golf.tournament.manage", &property)
		if err != nil {
			return uuid.Nil, err
		}
		if len(users) > 0 {
			if err := m.Notify.Send(ctx, tx, notify.Message{Event: "golf.tournament_from_quotation", Category: "golf", UserIDs: users, PropertyID: &property,
				Data: map[string]any{"tournament": d.Name, "quotation": p.Number, "date": d.StartDate, "pax": field}}); err != nil {
				return uuid.Nil, err
			}
		}
	}
	return d.ID, nil
}

// quotationBilling opens the tournament folio of the company (on its
// customer folio) and issues the payment terms of the quotation, the DP
// first (FR-QUO-06).
func (m *Module) quotationBilling(ctx context.Context, tx pgx.Tx, property uuid.UUID, t Tournament, p AcceptedQuotation, total decimal.Decimal) (uuid.UUID, uuid.UUID, error) {
	holder, _, _, err := crm.Contact(ctx, tx, p.CustomerID, nil)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	corporate := ""
	if p.CorporateAccountID != nil {
		if err := tx.QueryRow(ctx, `SELECT name FROM crm.corporate_accounts WHERE id = $1`, *p.CorporateAccountID).Scan(&corporate); err != nil {
			return uuid.Nil, uuid.Nil, err
		}
	}
	f, err := m.Billing.OpenLineFolio(ctx, tx, billing.LineFolioInput{FolioInput: billing.FolioInput{Property: property, CustomerID: p.CustomerID,
		HolderName: holder, SourceType: "tournament", SourceID: &t.ID, SourceRef: t.Code + " " + p.Number}, BusinessLine: billing.LineGolf,
		CorporateName: corporate})
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	payer := billing.CustomerFolioInput{CustomerID: p.CustomerID}
	if p.CorporateAccountID != nil {
		payer = billing.CustomerFolioInput{CorporateAccountID: p.CorporateAccountID}
	}
	if _, err := m.Billing.AttachFolio(ctx, tx, property, f.ID, payer); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	var lines []billing.ScheduleLineInput
	acc := decimal.Zero
	for i, term := range p.PaymentTerms {
		amt, _ := decimal.NewFromString(term.Amount)
		if i == len(p.PaymentTerms)-1 {
			amt = total.Sub(acc)
		}
		if !amt.IsPositive() {
			continue
		}
		acc = acc.Add(amt)
		lines = append(lines, billing.ScheduleLineInput{Label: term.Label, Amount: amt.String(), DueDate: term.DueDate})
	}
	if len(lines) == 0 {
		lines = []billing.ScheduleLineInput{{Label: "Full Payment", Kind: "final", Amount: total.String(),
			DueDate: localDate(now(), location(ctx, tx, property)).Format("2006-01-02")}}
	}
	s, err := m.Billing.CreateSchedule(ctx, tx, property, billing.ScheduleInput{Title: p.Number + " · " + t.Name, FolioID: &f.ID, CustomerID: p.CustomerID,
		CorporateAccountID: p.CorporateAccountID, SourceType: "tournament", SourceID: &t.ID, SourceRef: p.Number, TotalAmount: total.String(), Lines: lines})
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return f.ID, s.ID, nil
}

// bannerEvent is the part of banquet.event_confirmed / _cancelled kept on
// the tournament (contract in docs/p3-p4-contracts.md).
type bannerEvent struct {
	EventID uuid.UUID `json:"eventId"`
	Number  string    `json:"number"`
	Title   string    `json:"title"`
	Status  string    `json:"status"`
}

// OnBanquetEvent keeps the snapshot of the banquet event linked to a
// tournament (venue & catering of the tournament dinner, FR-TRN-13).
func (m *Module) OnBanquetEvent(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p bannerEvent
	if err := e.Decode(&p); err != nil || p.EventID == uuid.Nil {
		return nil //nolint:nilerr // foreign payload
	}
	status := p.Status
	if status == "" {
		status = strings.TrimPrefix(e.Type, "banquet.event_")
	}
	ctx = dbtx.System(ctx)
	rows, err := tx.Query(ctx, `UPDATE golf.tournaments SET event_number = $2, event_title = $3, event_status = $4, event_synced_at = now()
		WHERE event_id = $1 RETURNING id, property_id, code`, p.EventID, nullStr(p.Number), nullStr(p.Title), status)
	if err != nil {
		return err
	}
	type hit struct {
		id, property uuid.UUID
		code         string
	}
	var hits []hit
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.id, &h.property, &h.code); err != nil {
			rows.Close()
			return err
		}
		hits = append(hits, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, h := range hits {
		if err := audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "event_synced", Category: audit.CategorySystem, EntityType: "golf.tournament",
			EntityID: h.id.String(), EntityLabel: h.code, PropertyID: &h.property,
			After: map[string]any{"eventId": p.EventID, "event": p.Number, "eventStatus": status}}); err != nil {
			return err
		}
	}
	return nil
}

// CustomerTournament is a line of the Customer 360 Tournament section.
type CustomerTournament struct {
	TournamentID  uuid.UUID `json:"tournamentId" db:"tournament_id"`
	Code          string    `json:"code" db:"code"`
	Name          string    `json:"name" db:"name"`
	StartDate     string    `json:"startDate" db:"start_date"`
	Status        string    `json:"status" db:"status" doc:"Registration status"`
	Position      *string   `json:"position" db:"position" doc:"Final position (primary category)"`
	PaymentStatus string    `json:"paymentStatus" db:"payment_status"`
}

// CustomerSection is the Tournament section of Customer 360.
func (m *Module) CustomerSection(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (any, error) {
	type section struct {
		Tournaments int                  `json:"tournaments"`
		Wins        int                  `json:"wins"`
		Recent      []CustomerTournament `json:"recent"`
	}
	out := section{Recent: []CustomerTournament{}}
	if err := q.QueryRow(ctx, `SELECT count(DISTINCT r.tournament_id) FILTER (WHERE r.status <> 'withdrawn')::int,
		(SELECT count(DISTINCT x.tournament_id) FROM golf.tournament_results x WHERE x.customer_id = $1 AND x.position = 1 AND x.division_id IS NULL)::int
		FROM golf.tournament_registrations r WHERE r.customer_id = $1`, customer).Scan(&out.Tournaments, &out.Wins); err != nil {
		return out, err
	}
	var err error
	out.Recent, err = listCustomerTournaments(ctx, q, customer)
	return out, err
}

func listCustomerTournaments(ctx context.Context, q dbtx.Querier, customer uuid.UUID) ([]CustomerTournament, error) {
	rows, err := q.Query(ctx, `SELECT t.id AS tournament_id, t.code, t.name, t.start_date::text AS start_date, r.status, r.payment_status,
		(SELECT x.position_label FROM golf.tournament_results x WHERE x.registration_id = r.id AND x.division_id IS NULL
		 ORDER BY CASE x.category WHEN 'stableford' THEN 0 WHEN 'net' THEN 1 ELSE 2 END LIMIT 1) AS position
		FROM golf.tournament_registrations r JOIN golf.tournaments t ON t.id = r.tournament_id WHERE r.customer_id = $1
		ORDER BY t.start_date DESC LIMIT 10`, customer)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByNameLax[CustomerTournament])
}

// ── jobs ──────────────────────────────────────────────────────────────────

// JobResult reports one run of the tournament housekeeping.
type JobResult struct {
	Closed   int `json:"closed" doc:"Registrations closed at their deadline"`
	Released int `json:"released" doc:"Unpaid online registrations released (waitlist promoted)"`
}

// RunHousekeeping closes registrations at their deadline and releases
// unpaid online registrations (Tournament Policies payment due).
func (m *Module) RunHousekeeping(ctx context.Context) (JobResult, error) {
	var res JobResult
	ctx = dbtx.System(ctx)
	err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		if res.Released, err = m.ReleaseUnpaid(ctx, tx); err != nil {
			return err
		}
		res.Closed, err = m.CloseDueRegistrations(ctx, tx)
		return err
	})
	return res, err
}

// HousekeepingArgs runs the tournament housekeeping.
type HousekeepingArgs struct{}

// Kind implements river.JobArgs.
func (HousekeepingArgs) Kind() string { return "golf_tournament_housekeeping" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (HousekeepingArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

// HousekeepingWorker runs RunHousekeeping.
type HousekeepingWorker struct {
	river.WorkerDefaults[HousekeepingArgs]
	M *Module
}

// Work implements river.Worker.
func (w *HousekeepingWorker) Work(ctx context.Context, _ *river.Job[HousekeepingArgs]) error {
	_, err := w.M.RunHousekeeping(ctx)
	return err
}

// RegisterJobs adds the periodic tournament housekeeping (every 5 minutes:
// registration deadlines and unpaid releases are time-critical when a
// popular tournament opens).
func (m *Module) RegisterJobs(reg *jobs.Registrar) {
	river.AddWorker(reg.Workers, &HousekeepingWorker{M: m})
	reg.Periodic = append(reg.Periodic,
		river.NewPeriodicJob(river.PeriodicInterval(5*time.Minute), func() (river.JobArgs, *river.InsertOpts) { return HousekeepingArgs{}, nil }, nil))
}
