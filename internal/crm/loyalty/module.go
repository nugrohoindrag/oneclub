package loyalty

// Catalogue, notification templates, jobs, Customer 360 section and the
// identity events (merge, erasure) of loyalty.

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/resource"
)

// Permissions of loyalty.
func Permissions() []catalog.Permission {
	perms := resource.Permissions(Tiers, EarningRules, Rewards)
	perms = append(perms, catalog.P("crm", "loyalty_account", "view", "create", "update", "adjust", "redeem", "import")...)
	perms = append(perms, catalog.P("crm", "loyalty_redemption", "view", "fulfil")...)
	return perms
}

// RolePermissions maps loyalty permissions to the role templates.
func RolePermissions() map[string][]string {
	all := append(resource.AllActions(Tiers, EarningRules, Rewards), "crm.loyalty_account.view", "crm.loyalty_account.create",
		"crm.loyalty_account.update", "crm.loyalty_account.adjust", "crm.loyalty_account.redeem", "crm.loyalty_account.import",
		"crm.loyalty_redemption.view", "crm.loyalty_redemption.fulfil")
	view := []string{"crm.loyalty_account.view", "crm.loyalty_tier.view", "crm.loyalty_rule.view", "crm.loyalty_reward.view", "crm.loyalty_redemption.view"}
	desk := append(append([]string{}, view...), "crm.loyalty_account.create", "crm.loyalty_account.redeem", "crm.loyalty_redemption.fulfil")
	cashier := []string{"crm.loyalty_account.view", "crm.loyalty_account.redeem", "crm.loyalty_reward.view", "crm.loyalty_redemption.view",
		"crm.loyalty_redemption.fulfil"}
	return map[string][]string{
		"property_admin":          all,
		"crm_admin":               all,
		"marketing_staff":         append(append([]string{}, view...), resource.AllActions(Rewards, EarningRules)...),
		"membership_manager":      append(append([]string{}, view...), "crm.loyalty_account.create", "crm.loyalty_account.update", "crm.loyalty_account.adjust"),
		"membership_admin":        append(append([]string{}, view...), "crm.loyalty_account.create"),
		"sales_executive":         view,
		"general_manager":         view,
		"club_manager":            view,
		"finance_manager":         view,
		"accountant":              {"crm.loyalty_account.view", "crm.loyalty_redemption.view"},
		"front_desk":              desk,
		"sport_club_receptionist": desk,
		"reservation_staff":       desk,
		"cashier":                 cashier,
		"pos_staff":               cashier,
		"outlet_manager":          cashier,
	}
}

// Templates are the loyalty notification templates (FR-INT-P3-06: poin & reward).
func Templates() []provision.Template {
	t := map[string]map[string][2]string{
		"crm.loyalty_points_earned": {
			"en": {"You earned {{.points}} points", "Hello {{.name}}, you earned {{.points}} points ({{.reference}}). Your balance is {{.balance}} points."},
			"id": {"Anda mendapat {{.points}} poin", "Halo {{.name}}, Anda mendapat {{.points}} poin ({{.reference}}). Saldo poin Anda {{.balance}}."},
		},
		"crm.loyalty_points_expiring": {
			"en": {"{{.points}} points expire on {{.date}}", "Hello {{.name}}, {{.points}} of your points expire on {{.date}}. Redeem them before then."},
			"id": {"{{.points}} poin berakhir pada {{.date}}", "Halo {{.name}}, {{.points}} poin Anda akan berakhir pada {{.date}}. Tukarkan sebelum tanggal tersebut."},
		},
		"crm.loyalty_tier_changed": {
			"en": {"Your tier is now {{.tier}}", "Hello {{.name}}, your loyalty tier is now {{.tier}}."},
			"id": {"Tier Anda sekarang {{.tier}}", "Halo {{.name}}, tier loyalty Anda sekarang {{.tier}}."},
		},
		"crm.loyalty_reward_redeemed": {
			"en": {"Reward {{.reward}} redeemed", "Hello {{.name}}, you redeemed {{.reward}} for {{.points}} points ({{.number}}).{{if .code}} Your code: {{.code}}.{{end}}"},
			"id": {"Reward {{.reward}} ditukar", "Halo {{.name}}, Anda menukar {{.reward}} dengan {{.points}} poin ({{.number}}).{{if .code}} Kode Anda: {{.code}}.{{end}}"},
		},
		"crm.loyalty_points_adjusted": {
			"en": {"Your points were adjusted", "Hello {{.name}}, your points were adjusted by {{.points}} ({{.reason}}). Balance: {{.balance}} points."},
			"id": {"Poin Anda disesuaikan", "Halo {{.name}}, poin Anda disesuaikan sebesar {{.points}} ({{.reason}}). Saldo: {{.balance}} poin."},
		},
	}
	var out []provision.Template
	for ev, locs := range t {
		for loc, c := range locs {
			for _, ch := range []string{"in_app", "email", "whatsapp"} {
				out = append(out, provision.Template{Event: ev, Channel: ch, Locale: loc, Subject: c[0], Body: c[1]})
			}
		}
	}
	return out
}

// ── jobs ──────────────────────────────────────────────────────────────────

// DailyArgs runs the daily loyalty housekeeping.
type DailyArgs struct{}

func (DailyArgs) Kind() string { return "crm_loyalty_daily" }

func (DailyArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

// DailyWorker expires points, sends expiry notices and evaluates tiers on
// the first day of the month.
type DailyWorker struct {
	river.WorkerDefaults[DailyArgs]
	M *Module
}

func (w *DailyWorker) Work(ctx context.Context, _ *river.Job[DailyArgs]) error {
	_, err := w.M.RunDaily(ctx)
	return err
}

// RunDaily runs expiry, notices and (on the 1st) the tier evaluation for
// every property; it returns the expiry results per property.
func (m *Module) RunDaily(ctx context.Context) (map[uuid.UUID]ExpiryResult, error) {
	ctx = dbtx.System(ctx)
	props, err := properties(ctx, m.DB)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID]ExpiryResult{}
	for _, p := range props {
		err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
			res, err := m.ExpireDue(ctx, tx, p)
			if err != nil {
				return err
			}
			out[p] = res
			if localToday(ctx, tx, p).Day() == 1 {
				_, err = m.EvaluateTiers(ctx, tx, p)
			}
			return err
		})
		if err != nil {
			return out, fmt.Errorf("loyalty daily for %s: %w", p, err)
		}
	}
	return out, nil
}

func properties(ctx context.Context, db *dbtx.DB) ([]uuid.UUID, error) {
	var out []uuid.UUID
	err := db.WithReadTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM platform.properties WHERE status = 'active' AND archived_at IS NULL ORDER BY id`)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		return err
	})
	return out, err
}

// RegisterJobs adds the loyalty worker and its daily schedule.
func (m *Module) RegisterJobs(reg *jobs.Registrar, loc func() *time.Location) {
	river.AddWorker(reg.Workers, &DailyWorker{M: m})
	reg.Periodic = append(reg.Periodic, river.NewPeriodicJob(jobs.DailyAt{Hour: 0, Minute: 20, Location: loc},
		func() (river.JobArgs, *river.InsertOpts) { return DailyArgs{}, nil }, nil))
}

// ── Customer 360 (FR-C360-01) ─────────────────────────────────────────────

// LoyaltySection is the Loyalty section of the Customer 360.
type LoyaltySection struct {
	Enrolled bool            `json:"enrolled"`
	Account  *LoyaltyAccount `json:"account"`
	Recent   []LoyaltyEntry  `json:"recent"`
}

// CustomerSection loads the Loyalty section of a customer.
func (m *Module) CustomerSection(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (any, error) {
	out := LoyaltySection{Recent: []LoyaltyEntry{}}
	a, err := AccountByCustomer(ctx, q, customer)
	if err != nil || a == nil {
		return out, err
	}
	out.Enrolled, out.Account = a.Status == "active", a
	out.Recent, err = Ledger(ctx, q, a.ID, "", 10)
	return out, err
}

// ── identity events ───────────────────────────────────────────────────────

// OnCustomerMerged moves the loyalty account of a merged profile to the kept
// profile, or transfers its open lots (with their validity) and balance.
func (m *Module) OnCustomerMerged(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p crm.MergedPayload
	if err := e.Decode(&p); err != nil || p.SourceID == uuid.Nil || p.TargetID == uuid.Nil {
		return nil // not a merge payload
	}
	var src uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM crm.loyalty_accounts WHERE customer_id = $1`, p.SourceID).Scan(&src)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var dst uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM crm.loyalty_accounts WHERE customer_id = $1`, p.TargetID).Scan(&dst)
	if dbtx.IsNoRows(err) {
		_, err := tx.Exec(ctx, `UPDATE crm.loyalty_accounts SET customer_id = $2 WHERE id = $1`, src, p.TargetID)
		return err
	}
	if err != nil {
		return err
	}
	a, err := lockAccount(ctx, tx, src)
	if err != nil {
		return err
	}
	b, err := lockAccount(ctx, tx, dst)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT ledger_id, remaining, expires_on FROM crm.loyalty_lots WHERE account_id = $1 AND remaining > 0 ORDER BY expires_on`, src)
	if err != nil {
		return err
	}
	type lot struct {
		ID        uuid.UUID `db:"ledger_id"`
		Remaining int64     `db:"remaining"`
		Expires   time.Time `db:"expires_on"`
	}
	lots, err := pgx.CollectRows(rows, pgx.RowToStructByName[lot])
	if err != nil {
		return err
	}
	moved := int64(0)
	for _, l := range lots {
		exp := l.Expires
		lid := l.ID
		if _, _, err := m.post(ctx, tx, &b, posting{Kind: KindAdjusted, Points: l.Remaining, SourceType: "crm.customer_merge", SourceID: &lid,
			Key: "merge-in:" + l.ID.String(), ExpiresOn: &exp, Description: "Points moved from merged account " + a.Number}); err != nil {
			return err
		}
		moved += l.Remaining
	}
	if debt := a.Balance - moved; debt < 0 {
		if _, _, err := m.post(ctx, tx, &b, posting{Kind: KindAdjusted, Points: debt, SourceType: "crm.customer_merge", SourceID: &src,
			Key: "merge-debt:" + src.String(), Description: "Negative balance moved from merged account " + a.Number}); err != nil {
			return err
		}
	}
	if a.Balance != 0 {
		if _, _, err := m.post(ctx, tx, &a, posting{Kind: KindAdjusted, Points: -a.Balance, SourceType: "crm.customer_merge", SourceID: &dst,
			Key: "merge-out:" + src.String(), Description: "Points moved to account " + b.Number}); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE crm.loyalty_accounts SET status = 'inactive', status_reason = 'merged into ' || $2 WHERE id = $1`, src, b.Number)
	return err
}

// OnCustomerErased closes the account of an erased customer (UU PDP); the
// ledger keeps its amounts without personal data.
func (m *Module) OnCustomerErased(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p crm.ErasedPayload
	if err := e.Decode(&p); err != nil || p.CustomerID == uuid.Nil {
		return nil // not an erasure payload
	}
	_, err := tx.Exec(ctx, `UPDATE crm.loyalty_accounts SET status = 'inactive', status_reason = 'personal data erased' WHERE customer_id = $1`, p.CustomerID)
	return err
}
