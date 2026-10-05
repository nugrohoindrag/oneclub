package banquet

// Migration wave 3 (PRD P3 FR-MIG-P3-01, FR-MIG-P3-05): future banquets and
// events from the banquet book (outside Rhapsody, so through the Master
// Data Import flow: preview, then commit) with the down payment already
// received and the balance still due, and the reconciliation figures the
// club signs — total DP = the deposit liability on the event folios, the
// number of future events.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
)

// LegacyEventRow is one future event of the banquet book.
type LegacyEventRow struct {
	LegacyRef            string `json:"legacyRef" doc:"Reference in the banquet book (idempotency key)"`
	Title                string `json:"title"`
	EventType            string `json:"eventType,omitempty" doc:"Event type code (WEDDING, MEETING, BIRTHDAY …); default by the package category"`
	CustomerName         string `json:"customerName"`
	CustomerPhone        string `json:"customerPhone,omitempty"`
	CustomerEmail        string `json:"customerEmail,omitempty"`
	CorporateAccountCode string `json:"corporateAccountCode,omitempty"`
	Date                 string `json:"date" doc:"YYYY-MM-DD"`
	StartTime            string `json:"startTime,omitempty" doc:"HH:MM (default Banquet Policies start time)"`
	EndTime              string `json:"endTime,omitempty" doc:"HH:MM (default start + package hours)"`
	Pax                  int    `json:"pax"`
	PackageCode          string `json:"packageCode,omitempty"`
	VenueCode            string `json:"venueCode,omitempty"`
	Layout               string `json:"layout,omitempty"`
	Menu                 string `json:"menu,omitempty" doc:"Menu agreed so far (free text)"`
	ContractTotal        string `json:"contractTotal" doc:"Contract value incl. tax & service"`
	DownPayment          string `json:"downPayment,omitempty" doc:"DP already received before the go-live"`
	DownPaymentDate      string `json:"downPaymentDate,omitempty" doc:"YYYY-MM-DD the DP was received"`
	BalanceDueDate       string `json:"balanceDueDate,omitempty" doc:"YYYY-MM-DD (default Banquet Policies final payment H-N)"`
	Notes                string `json:"notes,omitempty"`
}

// LegacyEventsImport is a batch of the banquet book.
type LegacyEventsImport struct {
	DryRun bool             `json:"dryRun,omitempty" doc:"Validate and preview without saving"`
	Rows   []LegacyEventRow `json:"rows"`
}

// LegacyEventResult is the outcome of one row.
type LegacyEventResult struct {
	Row         int        `json:"row"`
	LegacyRef   string     `json:"legacyRef"`
	Status      string     `json:"status" enum:"imported,existing,error"`
	Message     string     `json:"message,omitempty"`
	EventID     *uuid.UUID `json:"eventId"`
	EventNumber string     `json:"eventNumber,omitempty"`
	EventStatus string     `json:"eventStatus,omitempty"`
	DownPayment string     `json:"downPayment"`
	Balance     string     `json:"balance"`
}

// LegacyEventsResult reports an import batch.
type LegacyEventsResult struct {
	DryRun   bool                `json:"dryRun"`
	Imported int                 `json:"imported"`
	Existing int                 `json:"existing"`
	Errors   int                 `json:"errors"`
	Rows     []LegacyEventResult `json:"rows"`
}

// MigrationReconciliation are the figures signed by the club (FR-MIG-P3-05).
type BanquetMigrationReconciliation struct {
	AsOf                string `json:"asOf"`
	ImportedEvents      int    `json:"importedEvents"`
	FutureEvents        int    `json:"futureEvents" doc:"Live (inquiry, tentative, definite) events from today"`
	ImportedFuture      int    `json:"importedFutureEvents"`
	MigratedDownPayment string `json:"migratedDownPayment" doc:"Total DP declared in the banquet book"`
	DepositLiability    string `json:"depositLiability" doc:"Held deposits on the folios of the imported events"`
	TotalDepositHeld    string `json:"totalDepositHeld" doc:"Held deposits on every banquet event folio"`
	ContractTotal       string `json:"contractTotal" doc:"Contract value of the imported events"`
	OutstandingBalance  string `json:"outstandingBalance" doc:"Contract value − deposits of the imported events"`
	Balanced            bool   `json:"balanced" doc:"Migrated DP = deposit liability"`
}

func (m *Module) registerImport(reg *route.Registry) {
	db := m.DB
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/events:import",
		Summary: "Import future banquets / events with the DP received (migration wave 3; dry run first)", Permission: "banquet.event.import",
		Request: LegacyEventsImport{}, Response: LegacyEventsResult{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in LegacyEventsImport) (LegacyEventsResult, error) {
			return m.ImportEvents(ctx, tx, handle.Property(ctx), in)
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/banquet/migration/reconciliation",
		Summary: "Migration reconciliation: total DP = deposit liability, number of future events", Permission: "banquet.event.import",
		Response: BanquetMigrationReconciliation{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (BanquetMigrationReconciliation, error) {
			return Reconcile(ctx, tx, handle.Property(ctx))
		})})
}

// ImportEvents imports the rows (each in a savepoint; rows already imported
// are left unchanged). A dry run rolls everything back.
func (m *Module) ImportEvents(ctx context.Context, tx pgx.Tx, property uuid.UUID, in LegacyEventsImport) (LegacyEventsResult, error) {
	out := LegacyEventsResult{DryRun: in.DryRun, Rows: []LegacyEventResult{}}
	if len(in.Rows) == 0 {
		return out, handle.Invalid("rows", "required", "the file has no rows")
	}
	if len(in.Rows) > 1000 {
		return out, handle.Invalid("rows", "too_many", "at most 1000 events per batch")
	}
	batch, err := tx.Begin(ctx)
	if err != nil {
		return out, err
	}
	for i, row := range in.Rows {
		res := LegacyEventResult{Row: i + 1, LegacyRef: strings.TrimSpace(row.LegacyRef), DownPayment: "0", Balance: "0"}
		sp, err := batch.Begin(ctx)
		if err != nil {
			_ = batch.Rollback(ctx)
			return out, err
		}
		err = m.importRow(ctx, sp, property, row, &res)
		if err != nil {
			_ = sp.Rollback(ctx)
			de, ok := errs.As(err)
			if !ok || de.Kind == errs.KindInternal {
				_ = batch.Rollback(ctx)
				return out, err
			}
			res.Status, res.Message = "error", de.Message
			for _, f := range de.Fields {
				res.Message += "; " + f.Field + ": " + f.Message
			}
			out.Errors++
		} else {
			if err := sp.Commit(ctx); err != nil {
				_ = batch.Rollback(ctx)
				return out, err
			}
			if res.Status == "existing" {
				out.Existing++
			} else {
				out.Imported++
			}
		}
		out.Rows = append(out.Rows, res)
	}
	if in.DryRun {
		if err := batch.Rollback(ctx); err != nil {
			return out, err
		}
	} else if err := batch.Commit(ctx); err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: audit.ActionImport, EntityType: "banquet.event", EntityID: property.String(),
		EntityLabel: "banquet book import", PropertyID: &property, Metadata: map[string]any{"dryRun": in.DryRun, "imported": out.Imported,
			"existing": out.Existing, "errors": out.Errors}})
}

func parseClock(field, v, def string) (int, int, error) {
	if strings.TrimSpace(v) == "" {
		v = def
	}
	t, err := time.Parse("15:04", strings.TrimSpace(v))
	if err != nil {
		return 0, 0, handle.Invalid(field, "invalid_time", "HH:MM")
	}
	return t.Hour(), t.Minute(), nil
}

func (m *Module) importRow(ctx context.Context, tx pgx.Tx, property uuid.UUID, row LegacyEventRow, res *LegacyEventResult) error {
	ref := strings.TrimSpace(row.LegacyRef)
	if ref == "" {
		return handle.Invalid("legacyRef", "required", "the banquet book reference is required")
	}
	var existing uuid.UUID
	var number, status string
	err := tx.QueryRow(ctx, `SELECT id, number, status FROM banquet.events WHERE property_id = $1 AND legacy_ref = $2`, property, ref).Scan(&existing, &number, &status)
	if err == nil {
		res.Status, res.EventID, res.EventNumber, res.EventStatus = "existing", &existing, number, status
		return nil
	}
	if !dbtx.IsNoRows(err) {
		return err
	}
	if strings.TrimSpace(row.Title) == "" || strings.TrimSpace(row.CustomerName) == "" {
		return handle.Invalid("title", "required", "title and customer name are required")
	}
	total, err := handle.Decimal("contractTotal", row.ContractTotal, decimal.Zero)
	if err != nil {
		return err
	}
	dp, err := handle.Decimal("downPayment", row.DownPayment, decimal.Zero)
	if err != nil {
		return err
	}
	if !total.IsPositive() || dp.IsNegative() || dp.GreaterThan(total) {
		return handle.Invalid("downPayment", "invalid", "the contract total must be positive and the DP between 0 and the total")
	}
	if row.Pax < 0 {
		return handle.Invalid("pax", "invalid", "pax cannot be negative")
	}
	loc := calendar.Location(ctx, tx)
	day, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(row.Date), loc)
	if err != nil {
		return handle.Invalid("date", "invalid_date", "YYYY-MM-DD")
	}
	if day.Before(localDay(clock.Now(), loc)) {
		return handle.Invalid("date", "past", "only future events are migrated")
	}
	pol, _, err := bookingPolicy(ctx, tx, property)
	if err != nil {
		return err
	}
	var pkg *packageRow
	if strings.TrimSpace(row.PackageCode) != "" {
		code := strings.TrimSpace(row.PackageCode)
		if pkg, err = packageByRef(ctx, tx, property, &code); err != nil {
			return err
		}
		if pkg == nil {
			return handle.Invalid("packageCode", "not_found", "package "+code+" not found")
		}
	}
	h, mi, err := parseClock("startTime", row.StartTime, pol.DefaultStartTime)
	if err != nil {
		return err
	}
	start := time.Date(day.Year(), day.Month(), day.Day(), h, mi, 0, 0, loc)
	hours := 5
	if pkg != nil {
		hours = pkg.DurationHours
	}
	end := start.Add(time.Duration(hours) * time.Hour)
	if strings.TrimSpace(row.EndTime) != "" {
		eh, em, err := parseClock("endTime", row.EndTime, "")
		if err != nil {
			return err
		}
		end = time.Date(day.Year(), day.Month(), day.Day(), eh, em, 0, 0, loc)
		if !end.After(start) {
			end = end.AddDate(0, 0, 1) // ends after midnight
		}
	}
	code := strings.ToUpper(strings.TrimSpace(row.EventType))
	if code == "" {
		code = "BANQUET"
		if pkg != nil {
			switch pkg.Category {
			case "wedding":
				code = "WEDDING"
			case "mice", "corporate":
				code = "MEETING"
			case "birthday":
				code = "BIRTHDAY"
			}
		}
	}
	tid, err := ensureEventType(ctx, tx, property, code)
	if err != nil {
		return err
	}
	c, _, err := crm.FindOrCreate(ctx, tx, property, crm.Identity{Name: strings.TrimSpace(row.CustomerName), Phone: row.CustomerPhone, Email: row.CustomerEmail})
	if err != nil {
		return err
	}
	var corporate *uuid.UUID
	if cc := strings.TrimSpace(row.CorporateAccountCode); cc != "" {
		var cid uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM crm.corporate_accounts WHERE property_id = $1 AND upper(code) = upper($2)`, property, cc).Scan(&cid); err != nil {
			if dbtx.IsNoRows(err) {
				return handle.Invalid("corporateAccountCode", "not_found", "corporate account "+cc+" not found")
			}
			return err
		}
		corporate = &cid
	}
	notes := strings.TrimSpace("Migrated from the banquet book " + ref + ". " + row.Notes)
	e, err := m.createEvent(ctx, tx, property, eventSeed{EventInput: EventInput{Title: strings.TrimSpace(row.Title), EventTypeID: tid, CustomerID: &c.ID,
		CorporateAccountID: corporate, Start: start, End: end, ExpectedPax: row.Pax, Layout: strings.TrimSpace(row.Layout), Notes: notes,
		SpecialRequests: strings.TrimSpace(row.Menu)}, Source: "import"})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE banquet.events SET legacy_ref = $2, migrated_down_payment = $3::numeric, package_id = $4, charged_pax = $5
		WHERE id = $1`, e.ID, ref, dp.String(), packageID(pkg), row.Pax); err != nil {
		return err
	}
	if v := strings.TrimSpace(row.VenueCode); v != "" {
		var vid uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM banquet.venues WHERE property_id = $1 AND upper(code) = upper($2) AND archived_at IS NULL`, property, v).Scan(&vid); err != nil {
			if dbtx.IsNoRows(err) {
				return handle.Invalid("venueCode", "not_found", "venue "+v+" not found")
			}
			return err
		}
		if _, err := m.placeHold(ctx, tx, e.ID, VenueHoldInput{VenueID: vid}); err != nil {
			return err
		}
	}
	desc := "Migrated contract " + ref
	if pkg != nil {
		desc += " · " + pkg.Name
	}
	component := "banquet_package"
	if pkg != nil {
		component = pkg.Component
	}
	if _, err := m.postCharge(ctx, tx, e.ID, chargeSpec{Source: "import", Kind: "import", Description: desc, Quantity: decimal.NewFromInt(1), UnitPrice: total,
		Mode: "nett", Component: component, Preset: &presetAmounts{Net: total, Total: total}}); err != nil {
		return err
	}
	if e, err = m.Get(ctx, tx, e.ID); err != nil {
		return err
	}
	final := localDay(start, loc).AddDate(0, 0, -pol.FinalPaymentDaysBefore)
	if row.BalanceDueDate != "" {
		if final, err = time.ParseInLocation("2006-01-02", row.BalanceDueDate, loc); err != nil {
			return handle.Invalid("balanceDueDate", "invalid_date", "YYYY-MM-DD")
		}
	}
	today := localDay(clock.Now(), loc)
	if final.Before(today) {
		final = today
	}
	var lines []billing.ScheduleLineInput
	if dp.IsPositive() {
		received := today
		if row.DownPaymentDate != "" {
			if received, err = time.ParseInLocation("2006-01-02", row.DownPaymentDate, loc); err != nil {
				return handle.Invalid("downPaymentDate", "invalid_date", "YYYY-MM-DD")
			}
		}
		lines = append(lines, billing.ScheduleLineInput{Label: "Down Payment (migrated)", Kind: "down_payment", Amount: dp.String(),
			DueDate: received.Format("2006-01-02")})
	}
	if rest := total.Sub(dp); rest.IsPositive() {
		lines = append(lines, billing.ScheduleLineInput{Label: "Final Payment", Kind: "final", Amount: rest.String(), DueDate: final.Format("2006-01-02")})
	}
	if err := m.createSchedule(ctx, tx, e, lines); err != nil {
		return err
	}
	if dp.IsPositive() {
		if e, err = m.Get(ctx, tx, e.ID); err != nil {
			return err
		}
		sc, err := billing.GetSchedule(ctx, tx, *e.ScheduleID)
		if err != nil {
			return err
		}
		if _, err := m.Invoices.PayScheduleLine(ctx, tx, sc.Lines[0].ID, billing.PayScheduleLineInput{MethodType: "bank_transfer", Amount: dp.String(),
			Reference: "MIGRATED " + ref}); err != nil {
			return err
		}
		if _, err := m.makeDefinite(ctx, tx, e.ID, "migrated with the down payment received before the go-live"); err != nil {
			return err
		}
	}
	after, err := m.Get(ctx, tx, e.ID)
	if err != nil {
		return err
	}
	res.Status, res.EventID, res.EventNumber, res.EventStatus = "imported", &after.ID, after.Number, after.Status
	res.DownPayment, res.Balance = dp.String(), total.Sub(dp).String()
	return audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: audit.ActionImport, EntityType: "banquet.event", EntityID: after.ID.String(),
		EntityLabel: after.Number + " · " + after.Title, PropertyID: &property, After: after, Metadata: map[string]any{"legacyRef": ref, "downPayment": dp.String()}})
}

func packageID(p *packageRow) *uuid.UUID {
	if p == nil {
		return nil
	}
	return &p.ID
}

// Reconcile computes the reconciliation figures of the banquet migration.
func Reconcile(ctx context.Context, q dbtx.Querier, property uuid.UUID) (BanquetMigrationReconciliation, error) {
	loc := calendar.Location(ctx, q)
	today := localDay(clock.Now(), loc)
	out := BanquetMigrationReconciliation{AsOf: today.Format("2006-01-02")}
	var migrated, liability, all, contract string
	err := q.QueryRow(ctx, `WITH imp AS (SELECT * FROM banquet.events WHERE property_id = $1 AND legacy_ref IS NOT NULL AND status <> 'cancelled'),
		held AS (SELECT d.folio_id, sum(d.amount - d.applied_amount) AS amount FROM billing.deposits d WHERE d.property_id = $1 AND d.status = 'held'
		  GROUP BY d.folio_id)
		SELECT (SELECT count(*) FROM imp),
		  (SELECT count(*) FROM banquet.events WHERE property_id = $1 AND status IN ('inquiry', 'tentative', 'definite') AND start_at >= $2),
		  (SELECT count(*) FROM imp WHERE status IN ('inquiry', 'tentative', 'definite') AND start_at >= $2),
		  trim_scale(coalesce((SELECT sum(migrated_down_payment) FROM imp), 0))::text,
		  trim_scale(coalesce((SELECT sum(h.amount) FROM imp JOIN held h ON h.folio_id = imp.folio_id), 0))::text,
		  trim_scale(coalesce((SELECT sum(h.amount) FROM banquet.events e JOIN held h ON h.folio_id = e.folio_id WHERE e.property_id = $1), 0))::text,
		  trim_scale(coalesce((SELECT sum(contract_total) FROM imp), 0))::text`, property, today).
		Scan(&out.ImportedEvents, &out.FutureEvents, &out.ImportedFuture, &migrated, &liability, &all, &contract)
	if err != nil {
		return out, fmt.Errorf("banquet reconciliation: %w", err)
	}
	out.MigratedDownPayment, out.DepositLiability, out.TotalDepositHeld, out.ContractTotal = migrated, liability, all, contract
	out.OutstandingBalance = dec(contract).Sub(dec(liability)).String()
	out.Balanced = dec(migrated).Equal(dec(liability))
	return out, nil
}
