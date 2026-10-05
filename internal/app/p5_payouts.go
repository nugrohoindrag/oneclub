package app

// PRD P5 EP-11–14 payouts & distributions (owner: hris/payouts), called
// from p5_payroll.go: service charge distribution, commission & bonus
// payout through payroll, caddy and partner instructor payout runs.
//
// The composition root plugs in the payout Directory — golf caddies and
// sport club instructors (name, login, employee instructors), the content of
// CRM commission statements, and the Paid status the business lines show
// once hris paid their settlements, fees and statements (hris imports none
// of them) — the approval document types, the payroll input sources and the
// subscribers of golf.caddy_settlement_approved (H1),
// sportclub.instructor_fee_approved (H2) and crm.commission_approved (H3).

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/hris/payouts"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/storage"
)

// p5Payouts holds the services of the area.
type p5Payouts struct {
	Module *payouts.Module
}

func p5PayoutsContributions() []catalog.Contribution {
	return []catalog.Contribution{(&payouts.Module{}).Contribution()}
}
func p5PayoutsDocumentTypes() []provision.DocumentType { return payouts.DocumentTypes() }
func p5PayoutsTemplates() []provision.Template         { return payouts.Templates() }

// buildP5Payouts wires routes, approval decisions and the payroll input
// sources.
func (a *App) buildP5Payouts(reg *route.Registry, cfg *config.Config, db *dbtx.DB, _ *storage.Files) {
	m := &payouts.Module{DB: db, Engine: a.Engine, Events: a.Bus, Approvals: a.Approvals, Notify: a.Notification, Directory: payoutDirectory{},
		StaffURL: func() string { return cfg.PublicBaseURL }}
	m.Register(reg)
	m.RegisterDecisions(a.Approvals.RegisterDocumentType)
	m.RegisterPayrollInputs()
	a.Payroll.Payouts.Module = m
}

// subscribeP5Payouts registers the subscribers of the approved caddy
// settlements, instructor fees and commission statements.
func (a *App) subscribeP5Payouts() {
	for event, h := range a.Payroll.Payouts.Module.Subscriptions() {
		a.Bus.Subscribe(event, "hris.payouts:"+event, h)
	}
}

// demoP5Payouts seeds a service charge distribution of last month, a paid
// caddy payout run and a calculated instructor payout run.
func demoP5Payouts(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	var done bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.payout_runs WHERE property_id = $1)`, property).Scan(&done); err != nil || done {
		return err
	}
	loc := calendar.Location(ctx, tx)
	now := clock.Now().In(loc)
	last := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
	// last month's service charge pool (Accounting, H4) when the demo has none
	if _, err := tx.Exec(ctx, `INSERT INTO accounting.service_charge_pools (id, property_id, year, month, collected, reserve_percent, reserve, distributable,
		basis, status, approved_at) VALUES ($1,$2,$3,$4,48500000,5,2425000,46075000,'business_line','approved',now())
		ON CONFLICT (property_id, year, month) DO NOTHING`, uuid.New(), property, last.Year(), int(last.Month())); err != nil {
		return err
	}
	// one partner instructor when the sport club has none
	if _, err := tx.Exec(ctx, `INSERT INTO sportclub.instructors (id, property_id, code, name, partnership, disciplines, fee_scheme, fee_rate)
		SELECT $1, $2, 'DEMO-COACH-RINA', 'Coach Rina (partner)', 'partner', '{tennis}', 'per_session', 150000
		WHERE NOT EXISTS (SELECT 1 FROM sportclub.instructors WHERE property_id = $2 AND partnership = 'partner' AND archived_at IS NULL)`,
		uuid.New(), property); err != nil {
		return err
	}
	var ps payouts.DemoPartners
	for _, q := range []struct {
		kind, sql string
		into      *[]payouts.Partner
	}{
		{hris.PayoutKindCaddy, `SELECT id, code, name FROM golf.caddies WHERE property_id = $1 AND archived_at IS NULL AND status = 'active' ORDER BY code LIMIT 6`,
			&ps.Caddies},
		{hris.PayoutKindInstructor, `SELECT id, code, name FROM sportclub.instructors WHERE property_id = $1 AND archived_at IS NULL AND partnership = 'partner'
			ORDER BY code LIMIT 3`, &ps.Instructors},
	} {
		rows, err := tx.Query(ctx, q.sql, property)
		if err != nil {
			return err
		}
		err = forEachRow(rows, func(r pgx.Rows) error {
			p := payouts.Partner{Kind: q.kind, Partnership: "partner"}
			if err := r.Scan(&p.ID, &p.Code, &p.Name); err != nil {
				return err
			}
			*q.into = append(*q.into, p)
			return nil
		})
		if err != nil {
			return err
		}
	}
	return payouts.SeedDemo(ctx, tx, property, ps)
}

// payoutDirectory is the payouts' view of golf, sport club and CRM.
type payoutDirectory struct{}

func (payoutDirectory) partner(ctx context.Context, q dbtx.Querier, kind, where string, args ...any) (*payouts.Partner, error) {
	sql := `SELECT c.id, c.code, c.name, cp.user_id, NULL::uuid, c.partnership_status FROM golf.caddies c LEFT JOIN golf.caddy_profiles cp ON cp.caddy_id = c.id
		WHERE c.archived_at IS NULL AND ` + where
	if kind == hris.PayoutKindInstructor {
		sql = `SELECT c.id, c.code, c.name, c.user_id, c.employee_id, c.partnership FROM sportclub.instructors c WHERE c.archived_at IS NULL AND ` + where
	} else if kind != hris.PayoutKindCaddy {
		return nil, nil
	}
	p := payouts.Partner{Kind: kind}
	err := q.QueryRow(ctx, sql, args...).Scan(&p.ID, &p.Code, &p.Name, &p.UserID, &p.EmployeeID, &p.Partnership)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	return &p, err
}

func (d payoutDirectory) Partner(ctx context.Context, q dbtx.Querier, property uuid.UUID, kind string, id uuid.UUID) (*payouts.Partner, error) {
	return d.partner(ctx, q, kind, `c.id = $1 AND c.property_id = $2`, id, property)
}

func (d payoutDirectory) PartnerOfUser(ctx context.Context, q dbtx.Querier, property uuid.UUID, kind string, user uuid.UUID) (*payouts.Partner, error) {
	col := "cp.user_id"
	if kind == hris.PayoutKindInstructor {
		col = "c.user_id"
	}
	return d.partner(ctx, q, kind, col+` = $1 AND c.property_id = $2`, user, property)
}

func (payoutDirectory) Commission(ctx context.Context, q dbtx.Querier, statementID uuid.UUID) (payouts.CommissionBreakdown, bool, error) {
	var b payouts.CommissionBreakdown
	var name *string
	err := q.QueryRow(ctx, `SELECT s.earned, s.clawback, s.adjustments, u.full_name FROM crm.sales_commission_statements s
		LEFT JOIN platform.users u ON u.id = s.user_id WHERE s.id = $1`, statementID).Scan(&b.Earned, &b.Clawback, &b.Adjustments, &name)
	if dbtx.IsNoRows(err) {
		return b, false, nil
	}
	if name != nil {
		b.UserName = *name
	}
	return b, err == nil, err
}

func (payoutDirectory) Units(ctx context.Context, q dbtx.Querier, sourceType string, id uuid.UUID) (int, error) {
	if sourceType != payouts.SourceInstructorFee {
		return 0, nil
	}
	var n int
	err := q.QueryRow(ctx, `SELECT sessions FROM sportclub.instructor_fees WHERE id = $1`, id).Scan(&n)
	if dbtx.IsNoRows(err) {
		return 0, nil
	}
	return n, err
}

// payoutTables maps a payout source type to the owner table of its status.
var payoutTables = map[string]struct{ table, module, entity, approved string }{
	payouts.SourceCaddySettlement:     {"golf.caddy_settlements", "golf", "golf.caddy_settlement", "approved"},
	payouts.SourceInstructorFee:       {"sportclub.instructor_fees", "sportclub", "sportclub.instructor_fee", "approved"},
	payouts.SourceCommissionStatement: {"crm.sales_commission_statements", "crm", "crm.commission_statement", "approved"},
}

func (payoutDirectory) PaidElsewhere(ctx context.Context, q dbtx.Querier, sourceType string, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	t, ok := payoutTables[sourceType]
	out := map[uuid.UUID]bool{}
	if !ok || len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT id FROM `+t.table+` WHERE id = ANY($1) AND status = 'paid'`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// MarkPaid sets the approved settlements, fees or statements Paid in their
// module (FR-CDY-03, FR-CMS-HR-03), audited as a status change of that
// module.
func (payoutDirectory) MarkPaid(ctx context.Context, q dbtx.Querier, property uuid.UUID, sourceType string, ids []uuid.UUID, reference string) error {
	t, ok := payoutTables[sourceType]
	if !ok || len(ids) == 0 {
		return nil
	}
	set, args := "status = 'paid'", []any{ids, property}
	if sourceType == payouts.SourceCommissionStatement {
		set, args = "status = 'paid', paid_at = now(), paid_reference = $3", append(args, reference)
	}
	rows, err := q.Query(ctx, `UPDATE `+t.table+` SET `+set+` WHERE id = ANY($1) AND property_id = $2 AND status = '`+t.approved+`' RETURNING id`, args...)
	if err != nil {
		return err
	}
	var done []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		done = append(done, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range done {
		if err := audit.Record(ctx, q, audit.Entry{Module: t.module, Action: audit.ActionStatusChange, EntityType: t.entity, EntityID: id.String(),
			EntityLabel: reference, PropertyID: &property, Before: map[string]any{"status": t.approved},
			After: map[string]any{"status": "paid", "paidBy": "hris", "reference": reference}}); err != nil {
			return err
		}
	}
	return nil
}
