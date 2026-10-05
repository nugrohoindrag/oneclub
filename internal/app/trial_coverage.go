package app

// Coverage summary and health checks of the trial dataset: the number of
// rows per module and entity of the main property, and the consistency
// checks a trial instance must pass (balanced trial balance, stock
// valuation = GL inventory, no open posting exceptions, no failed outbox
// events).

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/accounting"
	"oneclub/internal/kernel/dbtx"
)

// TrialCount is the number of rows of one entity.
type TrialCount struct {
	Module string
	Entity string
	Rows   int
}

// trialCoverageTables are the main entities of the coverage summary
// (module, entity, table with a property_id column).
var trialCoverageTables = [][3]string{
	{"crm", "customers", "crm.customers"},
	{"crm", "corporate accounts", "crm.corporate_accounts"},
	{"crm", "leads", "crm.sales_leads"},
	{"crm", "opportunities", "crm.sales_opportunities"},
	{"crm", "quotations", "crm.sales_quotations"},
	{"crm", "sales commissions", "crm.sales_commissions"},
	{"crm", "campaigns", "crm.campaigns"},
	{"crm", "tickets", "crm.tickets"},
	{"crm", "NPS answers (surveys, Member App)", "reporting.eng_nps"},
	{"crm", "feedback", "crm.feedback"},
	{"crm", "loyalty accounts", "crm.loyalty_accounts"},
	{"crm", "loyalty ledger", "crm.loyalty_ledger"},
	{"crm", "top spender snapshots", "crm.top_spender_snapshots"},
	{"crm", "interactions", "crm.interactions"},
	{"membership", "members", "membership.members"},
	{"membership", "memberships", "membership.memberships"},
	{"membership", "applications", "membership.applications"},
	{"membership", "renewals", "membership.renewals"},
	{"golf", "bookings", "golf.bookings"},
	{"golf", "booking players", "golf.booking_players"},
	{"golf", "flights", "golf.flights"},
	{"golf", "caddy assignments", "golf.caddy_assignments"},
	{"golf", "golf cart assignments", "golf.golf_cart_assignments"},
	{"golf", "scorecards", "golf.scorecards"},
	{"golf", "range sessions", "golf.range_sessions"},
	{"golf", "range buckets", "golf.range_buckets"},
	{"golf", "tournaments", "golf.tournaments"},
	{"golf", "tournament registrations", "golf.tournament_registrations"},
	{"golf", "tournament results", "golf.tournament_results"},
	{"sportclub", "entries", "sportclub.entries"},
	{"sportclub", "class sessions", "sportclub.class_sessions"},
	{"sportclub", "session bookings", "sportclub.session_bookings"},
	{"sportclub", "instructor fees", "sportclub.instructor_fees"},
	{"stay", "stays", "stay.stays"},
	{"reservation", "reservations", "reservation.reservations"},
	{"banquet", "events", "banquet.events"},
	{"banquet", "BEOs", "banquet.beos"},
	{"commercial", "POS orders", "commercial.orders"},
	{"commercial", "POS order lines", "commercial.order_lines"},
	{"commercial", "POS shifts", "commercial.pos_shifts"},
	{"commercial", "vouchers", "commercial.vouchers"},
	{"commercial", "promotion redemptions", "commercial.promotion_redemptions"},
	{"commercial", "package bookings", "commercial.package_bookings"},
	{"billing", "folios", "billing.folios"},
	{"billing", "folio lines", "billing.folio_lines"},
	{"billing", "payments", "billing.payments"},
	{"billing", "invoices", "billing.invoices"},
	{"billing", "cashier shifts", "billing.cashier_shifts"},
	{"billing", "night audit runs", "billing.night_audit_runs"},
	{"inventory", "items", "inventory.items"},
	{"inventory", "recipes", "inventory.recipes"},
	{"inventory", "stock movements", "inventory.stock_movements"},
	{"inventory", "stock opnames", "inventory.stock_opnames"},
	{"procurement", "purchase requisitions", "procurement.purchase_requisitions"},
	{"procurement", "purchase orders", "procurement.purchase_orders"},
	{"procurement", "goods receipts", "procurement.goods_receipts"},
	{"procurement", "vendor invoices", "procurement.vendor_invoices"},
	{"accounting", "journals", "accounting.journals"},
	{"accounting", "journal lines", "accounting.journal_lines"},
	{"accounting", "bank reconciliations", "accounting.bank_reconciliations"},
	{"accounting", "tax invoices", "accounting.tax_invoices"},
	{"cms", "contents", "cms.contents"},
	// PRD P5 (trial_p5.go and the P5 demo seed)
	{"hris", "employees", "hris.employees"},
	{"hris", "shift schedules", "hris.schedules"},
	{"hris", "attendance events", "hris.attendance_events"},
	{"hris", "attendance days", "hris.attendance_days"},
	{"hris", "overtime requests", "hris.overtime_requests"},
	{"hris", "leave requests", "hris.leave_requests"},
	{"hris", "partner clock events", "hris.partner_attendance_events"},
	{"crm", "journey enrollments", "crm.journey_enrollments"},
	{"crm", "tier evaluations", "crm.loyalty_tier_evaluations"},
	{"reporting", "analytics KPI values", "analytics.kpi_values"},
}

// TrialCoverage counts the rows of the main entities of a property.
func TrialCoverage(ctx context.Context, db *dbtx.DB, property uuid.UUID) ([]TrialCount, error) {
	ctx = dbtx.System(ctx)
	out := make([]TrialCount, 0, len(trialCoverageTables))
	err := db.WithReadTx(ctx, func(tx pgx.Tx) error {
		for _, c := range trialCoverageTables {
			n := 0
			// table names come from the constant list above
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+c[2]+` WHERE property_id = $1`, property).Scan(&n); err != nil { //nolint:gosec // G202: constant table list
				return fmt.Errorf("%s: %w", c[2], err)
			}
			out = append(out, TrialCount{Module: c[0], Entity: c[1], Rows: n})
		}
		return nil
	})
	return out, err
}

// TrialHealth are the consistency checks of a trial instance.
type TrialHealth struct {
	TrialBalanced     bool
	TotalDebit        string
	TotalCredit       string
	InventoryChecks   []accounting.ControlReconCheck
	InventoryMatches  bool
	PostingExceptions map[string]int // open posting exceptions per reason
	OutboxFailed      int            // events not dispatched with an error
	OutboxPending     int
	ClosedDays        int
	ClosedPeriods     int // closed financial periods (month-end close)
	BankRecsCompleted int // completed bank reconciliations
}

// TrialHealthOf runs the consistency checks for a property.
func TrialHealthOf(ctx context.Context, db *dbtx.DB, property uuid.UUID) (TrialHealth, error) {
	ctx = dbtx.System(ctx)
	h := TrialHealth{PostingExceptions: map[string]int{}}
	err := db.WithReadTx(ctx, func(tx pgx.Tx) error {
		tb, err := accounting.TrialBalanceOf(ctx, tx, &property, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().AddDate(1, 0, 0))
		if err != nil {
			return err
		}
		h.TotalDebit, h.TotalCredit, h.TrialBalanced = tb.TotalDebit, tb.TotalCredit, tb.Balanced && decimalEq(tb.TotalDebit, tb.TotalCredit)
		if h.InventoryChecks, err = accounting.ReconcileInventory(ctx, tx, property, time.Now()); err != nil {
			return err
		}
		h.InventoryMatches = true
		for _, c := range h.InventoryChecks {
			if !c.OK {
				h.InventoryMatches = false
			}
		}
		rows, err := tx.Query(ctx, `SELECT reason, count(*) FROM accounting.posting_exceptions WHERE property_id = $1 AND status = 'open' GROUP BY reason`, property)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r string
			var n int
			if err := rows.Scan(&r, &n); err != nil {
				return err
			}
			h.PostingExceptions[r] = n
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE last_error IS NOT NULL), count(*) FROM platform.outbox WHERE dispatched_at IS NULL`).
			Scan(&h.OutboxFailed, &h.OutboxPending); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM billing.business_days WHERE property_id = $1 AND status = 'closed'),
			(SELECT count(*) FROM accounting.periods WHERE property_id = $1 AND status = 'closed'),
			(SELECT count(*) FROM accounting.bank_reconciliations WHERE property_id = $1 AND status = 'completed')`, property).
			Scan(&h.ClosedDays, &h.ClosedPeriods, &h.BankRecsCompleted)
	})
	return h, err
}

func decimalEq(a, b string) bool {
	x, err1 := decimal.NewFromString(a)
	y, err2 := decimal.NewFromString(b)
	return err1 == nil && err2 == nil && x.Equal(y)
}
