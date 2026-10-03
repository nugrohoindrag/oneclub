package rhapsody

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Check is one reconciliation line (FR-MIG-11).
type Check struct {
	Metric   string
	Rhapsody string
	OneClub  string
	Match    bool
}

// Metrics computes the OneClub side of the reconciliation:
//
//	customers, corporate_accounts, caddies, golf_carts, lockers   migrated records
//	members                                                       migrated members
//	active_members:<TYPE>                                         active memberships per type
//	member_account_balance                                        total balance of migrated member accounts
//	future_bookings                                               migrated bookings still open
func Metrics(ctx context.Context, tx pgx.Tx, property uuid.UUID) (map[string]string, error) {
	out := map[string]string{}
	counts := []struct{ metric, sql string }{
		{"customers", `SELECT count(*)::text FROM crm.customers WHERE property_id = $1 AND legacy_ref IS NOT NULL AND code LIKE 'RH-%'`},
		{"corporate_accounts", `SELECT count(*)::text FROM crm.corporate_accounts WHERE property_id = $1 AND legacy_ref IS NOT NULL`},
		{"caddies", `SELECT count(*)::text FROM golf.caddies WHERE property_id = $1 AND legacy_ref IS NOT NULL AND archived_at IS NULL`},
		{"golf_carts", `SELECT count(*)::text FROM golf.golf_carts WHERE property_id = $1 AND legacy_ref IS NOT NULL AND archived_at IS NULL`},
		{"lockers", `SELECT count(*)::text FROM golf.lockers WHERE property_id = $1 AND legacy_ref IS NOT NULL AND archived_at IS NULL`},
		{"members", `SELECT count(*)::text FROM membership.members WHERE property_id = $1 AND legacy_ref IS NOT NULL`},
		{"member_account_balance", `SELECT coalesce(sum(e.amount), 0)::numeric(19,2)::text FROM billing.account_entries e
			JOIN billing.customer_accounts a ON a.id = e.account_id JOIN membership.members m ON m.id = a.member_id
			WHERE a.property_id = $1 AND m.legacy_ref IS NOT NULL AND a.account_type = 'member'`},
		{"future_bookings", `SELECT count(*)::text FROM golf.bookings WHERE property_id = $1 AND legacy_ref IS NOT NULL AND status IN ('pending', 'confirmed')`},
	}
	for _, c := range counts {
		var v string
		if err := tx.QueryRow(ctx, c.sql, property).Scan(&v); err != nil {
			return nil, fmt.Errorf("%s: %w", c.metric, err)
		}
		out[c.metric] = v
	}
	rows, err := tx.Query(ctx, `SELECT upper(t.code), count(*) FROM membership.memberships ms JOIN membership.types t ON t.id = ms.type_id
		JOIN membership.members m ON m.id = ms.member_id WHERE ms.property_id = $1 AND ms.status = 'active' AND m.legacy_ref IS NOT NULL GROUP BY 1`, property)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var code string
		var n int
		if err := rows.Scan(&code, &n); err != nil {
			return nil, err
		}
		out["active_members:"+code] = fmt.Sprint(n)
	}
	return out, rows.Err()
}

// ReadTotals reads the Rhapsody control totals (metric,value per line).
func ReadTotals(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cr := csv.NewReader(f)
	out := map[string]string{}
	first := true
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if first {
			first = false
			if strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(rec[0], string(rune(0xFEFF)))), "metric") {
				continue
			}
		}
		if len(rec) < 2 {
			continue
		}
		out[strings.TrimSpace(rec[0])] = strings.TrimSpace(strings.ReplaceAll(rec[1], ",", ""))
	}
	return out, nil
}

// Reconcile compares the control totals with OneClub.
func (d *Deps) Reconcile(ctx context.Context, property uuid.UUID, totals map[string]string) ([]Check, error) {
	ctx = scoped(ctx, property)
	var ours map[string]string
	err := d.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		ours, err = Metrics(ctx, tx, property)
		return err
	})
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for k := range totals {
		names[k] = true
	}
	for k := range ours {
		if strings.HasPrefix(k, "active_members:") {
			names[k] = true
		}
	}
	var keys []string
	for k := range names {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []Check
	for _, k := range keys {
		want, have := totals[k], ours[k]
		if have == "" {
			have = "0"
		}
		if want == "" {
			want = "(not provided)"
		}
		a, errA := decimal.NewFromString(want)
		b, errB := decimal.NewFromString(have)
		match := errA == nil && errB == nil && a.Equal(b)
		out = append(out, Check{Metric: k, Rhapsody: want, OneClub: have, Match: match})
	}
	return out, nil
}

// WriteChecks writes the reconciliation for sign-off.
func WriteChecks(path string, checks []Check) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	_ = w.Write([]string{"metric", "rhapsody", "oneclub", "result"})
	for _, c := range checks {
		res := "MATCH"
		if !c.Match {
			res = "MISMATCH"
		}
		_ = w.Write([]string{c.Metric, c.Rhapsody, c.OneClub, res})
	}
	w.Flush()
	return w.Error()
}

// WriteIssues writes the validation report.
func WriteIssues(path string, issues []Issue) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	_ = w.Write([]string{"entity", "row", "field", "code", "message"})
	for _, i := range issues {
		_ = w.Write([]string{i.Entity, fmt.Sprint(i.Row), i.Field, i.Code, i.Message})
	}
	w.Flush()
	return w.Error()
}
