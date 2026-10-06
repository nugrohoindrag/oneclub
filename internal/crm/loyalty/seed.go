package loyalty

// Accounting export rows of loyalty (FR-INT-P3-05: liability & redemption
// of points) and the default loyalty set-up of a property (PRD P3 §16 #7).

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
)

// ExportRow is one accounting export row (billing.ExportRow without the
// billing types: internal/app adapts it).
type ExportRow struct {
	Section, Code, Description, Amount string
}

var exportKinds = []struct{ kind, desc string }{
	{KindEarned, "Loyalty points earned (liability added)"},
	{KindRedeemed, "Loyalty points redeemed (liability used)"},
	{KindExpired, "Loyalty points expired (breakage)"},
	{KindAdjusted, "Loyalty points adjusted"},
	{KindReversed, "Loyalty points reversed"},
}

// ExportRows are the point movements of a day valued at the redemption
// value in force (signed: positive adds to the liability).
func ExportRows(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time) ([]ExportRow, error) {
	pol, _, err := LoadPolicy(ctx, q, property)
	if err != nil {
		return nil, err
	}
	value := pol.Value()
	sums := map[string]int64{}
	rows, err := q.Query(ctx, `SELECT kind, coalesce(sum(points), 0)::bigint FROM crm.loyalty_ledger WHERE property_id = $1
		AND occurred_at >= $2 AND occurred_at < $3 GROUP BY kind`, property, from, to)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k string
		var n int64
		if err := rows.Scan(&k, &n); err != nil {
			rows.Close()
			return nil, err
		}
		sums[k] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	pl := int32(2)
	if c := currency(ctx, q); c == "IDR" || c == "JPY" {
		pl = 0
	}
	out := make([]ExportRow, 0, len(exportKinds))
	for _, k := range exportKinds {
		amt := value.Mul(decimal.NewFromInt(sums[k.kind]))
		out = append(out, ExportRow{Section: "loyalty", Code: k.kind, Description: k.desc, Amount: amt.StringFixed(pl)})
	}
	return out, nil
}

// SeedDefaults creates the demo tiers, activity rules and rewards of a
// property (idempotent by code).
func SeedDefaults(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	for _, t := range []struct {
		code, name string
		rank       int
		minPoints  int64
		mult       string
		benefits   string
	}{
		{"SILVER", "Silver", 1, 0, "1", "1 point per Rp10.000 net spend"},
		{"GOLD", "Gold", 2, 2500, "1.25", "Points × 1.25"},
		{"PLATINUM", "Platinum", 3, 10000, "1.5", "Points × 1.5, priority tee time requests"},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO crm.loyalty_tiers (id, property_id, code, name, rank, min_points, multiplier, benefits)
			VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8) ON CONFLICT (property_id, code) DO NOTHING`, id.New(), property, t.code, t.name, t.rank, t.minPoints,
			t.mult, t.benefits); err != nil {
			return err
		}
	}
	for _, r := range []struct {
		code, name, activity string
		points               int64
	}{{"ROUND", "Round finished", "round_finished", 10}, {"REFERRAL", "Member referral (deal won)", "referral", 500},
		{"EVENT", "Club event attended", "event_attended", 20}} {
		if _, err := tx.Exec(ctx, `INSERT INTO crm.loyalty_earning_rules (id, property_id, code, name, rule_type, activity, points)
			VALUES ($1,$2,$3,$4,'activity',$5,$6) ON CONFLICT (property_id, code) DO NOTHING`, id.New(), property, r.code, r.name, r.activity, r.points); err != nil {
			return err
		}
	}
	for _, w := range []struct {
		code, name, typ string
		cost            int64
	}{{"RW-COFFEE", "Coffee & pastry", "service", 300}, {"RW-BALLS", "Range bucket (50 balls)", "service", 500},
		{"RW-CAP", "Club cap", "merchandise", 1500}, {"RW-FNB100", "F&B voucher Rp100.000", "voucher", 1000}} {
		if _, err := tx.Exec(ctx, `INSERT INTO crm.loyalty_rewards (id, property_id, code, name, reward_type, points_cost)
			VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (property_id, code) DO NOTHING`, id.New(), property, w.code, w.name, w.typ, w.cost); err != nil {
			return err
		}
	}
	return nil
}
