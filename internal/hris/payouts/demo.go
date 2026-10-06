package payouts

// Demo data of the area (idempotent): partner payout profiles, the service
// charge distribution of last month (approved), a paid caddy payout run of
// the last semi-monthly period and a calculated instructor payout run of
// last month. The settlements and fees are demo sources (hris only): golf
// and sport club demo data are not changed.

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/id"
)

// DemoPartners are the caddies and partner instructors of the demo.
type DemoPartners struct {
	Caddies     []Partner
	Instructors []Partner
}

// SeedDemo seeds the demo data of a property.
func SeedDemo(ctx context.Context, tx pgx.Tx, property uuid.UUID, ps DemoPartners) error {
	var done bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.payout_runs WHERE property_id = $1)`, property).Scan(&done); err != nil || done {
		return err
	}
	now := today(ctx, tx, property)
	if err := demoProfiles(ctx, tx, property, ps); err != nil {
		return err
	}
	if err := demoDistribution(ctx, tx, property, now); err != nil {
		return err
	}
	from, to := hris.PreviousPartnerPeriod("semi_monthly", now)
	caddy, err := demoRun(ctx, tx, property, hris.PayoutKindCaddy, from, to, ps.Caddies, func(i int) (int, decimal.Decimal, decimal.Decimal) {
		rounds := 6 + i%5
		return rounds, decimal.NewFromInt(int64(rounds) * 150000), decimal.NewFromInt(int64(i%3) * 50000)
	})
	if err != nil {
		return err
	}
	if caddy != uuid.Nil {
		if _, err := tx.Exec(ctx, `UPDATE hris.payout_runs SET status = 'paid', approved_at = now(), posted_at = now(), paid_at = now(), paid_on = $2,
			paid_reference = 'DEMO-TRF-' || number WHERE id = $1`, caddy, to); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.payout_lines SET status = 'paid' WHERE run_id = $1`, caddy); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.payout_sources SET status = 'paid', consumed_at = now() WHERE payout_run_id = $1`, caddy); err != nil {
			return err
		}
	}
	mf, mt := hris.PreviousPartnerPeriod("monthly", now)
	_, err = demoRun(ctx, tx, property, hris.PayoutKindInstructor, mf, mt, ps.Instructors, func(i int) (int, decimal.Decimal, decimal.Decimal) {
		sessions := 8 + i*2
		return sessions, decimal.NewFromInt(int64(sessions) * 150000), decimal.Zero
	})
	return err
}

func demoProfiles(ctx context.Context, tx pgx.Tx, property uuid.UUID, ps DemoPartners) error {
	all := append(append([]Partner{}, ps.Caddies...), ps.Instructors...)
	for i, p := range all {
		method := "bank_transfer"
		if i == 2 {
			method = "cash"
		}
		var npwp *string
		if i%2 == 0 {
			v := fmt.Sprintf("09.254.%03d.4-407.000", 100+i)
			npwp = &v
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hris.partner_profiles (id, property_id, partner_kind, partner_id, partner_code, partner_name, nik, npwp, bank_code,
			bank_name, bank_account_no, bank_account_name, payment_method, bpu_enrolled, bpu_no)
			SELECT $1,$2,$3,$4,$5,$6,$7,$8,'014','BCA',$9,$6,$10,$11,$12
			WHERE NOT EXISTS (SELECT 1 FROM hris.partner_profiles WHERE property_id = $2 AND partner_kind = $3 AND partner_id = $4 AND archived_at IS NULL)`,
			id.New(), property, p.Kind, p.ID, p.Code, p.Name, fmt.Sprintf("32710%011d", 4200000+i), npwp, fmt.Sprintf("88%08d", 1200+i), method,
			i%3 != 1, fmt.Sprintf("BPU%09d", 500+i)); err != nil {
			return err
		}
	}
	return nil
}

func demoRun(ctx context.Context, tx pgx.Tx, property uuid.UUID, kind string, from, to time.Time, partners []Partner,
	amounts func(i int) (int, decimal.Decimal, decimal.Decimal)) (uuid.UUID, error) {
	prefix := map[string]string{hris.PayoutKindCaddy: "CST", hris.PayoutKindInstructor: "IFE"}[kind]
	srcType := map[string]string{hris.PayoutKindCaddy: SourceCaddySettlement, hris.PayoutKindInstructor: SourceInstructorFee}[kind]
	for i, p := range partners {
		units, fee, tips := amounts(i)
		if _, err := tx.Exec(ctx, `INSERT INTO hris.payout_sources (id, property_id, kind, source_type, source_id, number, partner_id, partner_name,
			period_start, period_end, units, fee, tips, gross, total) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::numeric,$13::numeric,$14::numeric,
			$14::numeric)`, id.New(), property, kind, srcType, id.New(), fmt.Sprintf("DEMO-%s-%s-%02d", prefix, to.Format("060102"), i+1), p.ID, p.Name,
			from, to, units, fee.String(), tips.String(), fee.Add(tips).String()); err != nil {
			return uuid.Nil, err
		}
	}
	no, err := yearlyNumber(ctx, tx, property, "PYO", to.Year())
	if err != nil {
		return uuid.Nil, err
	}
	rid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.payout_runs (id, property_id, number, kind, period_start, period_end, pay_date, notes)
		VALUES ($1,$2,$3,$4,$5,$6,$6,'Demo')`, rid, property, no, kind, from, to); err != nil {
		return uuid.Nil, err
	}
	run, err := getOne[PayoutRun]("payout run")(tx.Query(ctx, runSelect+` WHERE id = $1`, rid))
	if err != nil {
		return uuid.Nil, err
	}
	return rid, (&Module{}).CalculateRun(ctx, tx, run)
}

// demoDistribution distributes last month's pool (seeded by internal/app
// when Accounting has none) and approves it without posting.
func demoDistribution(ctx context.Context, tx pgx.Tx, property uuid.UUID, now time.Time) error {
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
	p, err := pool(ctx, tx, property, start.Year(), int(start.Month()))
	if err != nil || p == nil {
		return err
	}
	no, err := yearlyNumber(ctx, tx, property, "SCD", start.Year())
	if err != nil {
		return err
	}
	did := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.service_charge_distributions (id, property_id, number, year, month, period_start, period_end, pay_period,
		pool_id, pool_status, collected, notes) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::numeric,'Demo')`, did, property, no, start.Year(),
		int(start.Month()), start, start.AddDate(0, 1, -1), now.Format("2006-01"), p.ID, p.Status, p.Collected); err != nil {
		return err
	}
	d, err := getOne[ServiceChargeDistribution]("service charge distribution")(tx.Query(ctx, distSelect+` WHERE id = $1`, did))
	if err != nil {
		return err
	}
	if err := simulate(ctx, tx, d, false); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE hris.service_charge_distributions SET status = 'approved', approved_at = now() WHERE id = $1`, did)
	return err
}
