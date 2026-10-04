package rhapsody

// Migration wave 2 of PRD P2 (EP-31) on P1's Rhapsody pipeline: the P2
// export files are staged, validated, loaded and reconciled by the same
// steps. Loads go through the P2 modules' public API; staging_rhapsody.id_map
// remembers what each legacy row became so a load can be repeated.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/commercial/voucher"
	"oneclub/internal/golf/experience"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/reservation"
	"oneclub/internal/sportclub"
)

// P2Deps are the P2 modules the wave-2 load writes through.
type P2Deps struct {
	Vouchers     *voucher.Module
	Reservations *reservation.Engine
	Experience   *experience.Module
}

// p2Entities are the wave-2 export files (runbook appendix), loaded after
// P1's entities.
var p2Entities = []Entity{
	{Name: "outlets", Key: "code", Columns: []string{"code", "name", "outlet_type"}},
	{Name: "products", Key: "code", Columns: []string{"code", "name", "product_type", "price"}, Optional: []string{"category", "member_price"}},
	{Name: "member_charges", Key: "legacy_id", Columns: []string{"legacy_id", "member_no", "amount"}, Optional: []string{"description"}},
	{Name: "vouchers", Key: "code", Columns: []string{"code", "voucher_type_code", "original_quantity", "remaining_quantity", "price_paid"},
		Optional: []string{"customer_legacy_id", "expires_on"}},
	{Name: "reservations", Key: "legacy_id", Columns: []string{"legacy_id", "resource_code", "start_at", "end_at"},
		Optional: []string{"business_line", "customer_legacy_id", "guest_name", "notes"}},
	{Name: "enrollments", Key: "legacy_id", Columns: []string{"legacy_id", "program_code", "customer_legacy_id", "valid_until"}},
	{Name: "caddy_profiles", Key: "caddy_code", Columns: []string{"caddy_code"}, Optional: []string{"level_code", "joined_on"}},
	{Name: "hio", Key: "legacy_id", Columns: []string{"legacy_id", "player_name", "section_code", "hole_number", "achieved_on"},
		Optional: []string{"customer_legacy_id", "witnesses"}},
	{Name: "hall_of_fame", Key: "legacy_id", Columns: []string{"legacy_id", "category", "title"},
		Optional: []string{"division", "player_name", "achieved_on", "description", "year", "score"}},
}

func init() { Entities = append(Entities, p2Entities...) }

// p2Load loads one wave-2 row; handled reports whether the entity is P2's.
func (d *Deps) p2Load(ctx context.Context, tx pgx.Tx, property uuid.UUID, entity string, r map[string]string) (inserted, handled bool, err error) {
	key := ""
	for _, e := range p2Entities {
		if e.Name == entity {
			key, handled = r[e.Key], true
		}
	}
	if !handled {
		return false, false, nil
	}
	if done, err := mapped(ctx, tx, property, entity, key); err != nil || done {
		return false, true, err
	}
	target, err := d.p2Row(ctx, tx, property, entity, r)
	if err != nil {
		return false, true, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO `+Schema+`.id_map (property_id, entity, legacy_id, target_id) VALUES ($1,$2,$3,$4)
		ON CONFLICT (property_id, entity, legacy_id) DO UPDATE SET target_id = EXCLUDED.target_id`, property, entity, key, target)
	return true, true, err
}

func mapped(ctx context.Context, q dbtx.Querier, property uuid.UUID, entity, legacy string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+Schema+`.id_map WHERE property_id = $1 AND entity = $2 AND legacy_id = $3)`,
		property, entity, legacy).Scan(&ok)
	return ok, err
}

func (d *Deps) optionalCustomer(ctx context.Context, tx pgx.Tx, property uuid.UUID, legacy string) (*uuid.UUID, error) {
	if legacy == "" {
		return nil, nil
	}
	c, err := d.customer(ctx, tx, property, legacy)
	return &c, err
}

func amountOf(s string) (decimal.Decimal, error) {
	v, err := decimal.NewFromString(strings.ReplaceAll(strings.TrimSpace(s), ",", ""))
	if err != nil {
		return v, fmt.Errorf("invalid amount %q", s)
	}
	return v, nil
}

func (d *Deps) p2Row(ctx context.Context, tx pgx.Tx, property uuid.UUID, entity string, r map[string]string) (uuid.UUID, error) {
	switch entity {
	case "outlets":
		out, err := d.Engine.CreateRow(ctx, tx, commercial.Outlets, map[string]any{"code": r["code"], "name": r["name"], "outletType": r["outlet_type"]})
		if err != nil {
			return uuid.Nil, err
		}
		return uuid.Parse(fmt.Sprint(out["id"]))
	case "products":
		in := map[string]any{"code": r["code"], "name": r["name"], "productType": r["product_type"], "price": r["price"]}
		if r["category"] != "" {
			in["category"] = r["category"]
		}
		if r["member_price"] != "" {
			in["memberPrice"] = r["member_price"]
		}
		out, err := d.Engine.CreateRow(ctx, tx, commercial.Products, in)
		if err != nil {
			return uuid.Nil, err
		}
		return uuid.Parse(fmt.Sprint(out["id"]))
	case "member_charges":
		var memberID, cid uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id, customer_id FROM membership.members WHERE property_id = $1 AND code = $2`, property, r["member_no"]).
			Scan(&memberID, &cid); err != nil {
			return uuid.Nil, fmt.Errorf("member %s was not loaded", r["member_no"])
		}
		amt, err := amountOf(r["amount"])
		if err != nil {
			return uuid.Nil, err
		}
		acct, err := d.Billing.EnsureAccount(ctx, tx, property, cid, "member", &memberID)
		if err != nil {
			return uuid.Nil, err
		}
		desc := r["description"]
		if desc == "" {
			desc = "Rhapsody F&B outstanding (migrated)"
		}
		return acct.ID, billing.PostEntry(ctx, tx, property, acct.ID, "opening_balance", amt, "IDR", desc, nil, nil)
	case "vouchers":
		cust, err := d.optionalCustomer(ctx, tx, property, r["customer_legacy_id"])
		if err != nil {
			return uuid.Nil, err
		}
		orig, err := amountOf(r["original_quantity"])
		if err != nil {
			return uuid.Nil, err
		}
		rem, err := amountOf(r["remaining_quantity"])
		if err != nil {
			return uuid.Nil, err
		}
		paid, err := amountOf(r["price_paid"])
		if err != nil {
			return uuid.Nil, err
		}
		var exp *time.Time
		if r["expires_on"] != "" {
			e, err := parseDate(r["expires_on"])
			if err != nil {
				return uuid.Nil, err
			}
			e = time.Date(e.Year(), e.Month(), e.Day(), 23, 59, 59, 0, d.loc())
			exp = &e
		}
		v, err := d.P2.Vouchers.Issue(ctx, tx, voucher.IssueRequest{PropertyID: property, TypeCode: strings.ToUpper(r["voucher_type_code"]), CustomerID: cust,
			Via: "migration", PricePaid: paid, Quantity: &rem, Original: &orig, ExpiresAt: exp, Code: r["code"], Notes: "Migrated from Rhapsody"})
		return v.ID, err
	case "reservations":
		var rid uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM reservation.resources WHERE property_id = $1 AND code = upper($2)`, property, r["resource_code"]).Scan(&rid); err != nil {
			return uuid.Nil, fmt.Errorf("resource %s not found", r["resource_code"])
		}
		st, err1 := time.Parse(time.RFC3339, r["start_at"])
		en, err2 := time.Parse(time.RFC3339, r["end_at"])
		if err1 != nil || err2 != nil {
			return uuid.Nil, fmt.Errorf("start_at and end_at must be RFC 3339")
		}
		cust, err := d.optionalCustomer(ctx, tx, property, r["customer_legacy_id"])
		if err != nil {
			return uuid.Nil, err
		}
		line := r["business_line"]
		if line == "" {
			line = "other"
		}
		res, err := d.P2.Reservations.Book(ctx, tx, property, reservation.BookRequest{BusinessLine: line, CustomerID: cust, GuestName: r["guest_name"],
			Channel: "import", Confirm: true, SourceType: "migration.rhapsody", Notes: r["notes"], Attributes: map[string]any{"legacyId": r["legacy_id"]},
			Lines: []reservation.LineRequest{{ResourceID: rid, Start: st, End: en, Description: "Migrated booking"}}})
		return res.ID, err
	case "enrollments":
		cust, err := d.customer(ctx, tx, property, r["customer_legacy_id"])
		if err != nil {
			return uuid.Nil, err
		}
		vu, err := parseDate(r["valid_until"])
		if err != nil {
			return uuid.Nil, err
		}
		return sportclub.ImportEnrollment(ctx, tx, property, r["program_code"], cust, vu)
	case "caddy_profiles":
		var cid uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM golf.caddies WHERE property_id = $1 AND code = $2`, property, r["caddy_code"]).Scan(&cid); err != nil {
			return uuid.Nil, fmt.Errorf("caddy %s was not loaded", r["caddy_code"])
		}
		in := experience.CaddyProfileInput{JoinedOn: r["joined_on"]}
		if r["level_code"] != "" {
			var lv uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT id FROM golf.caddy_levels WHERE property_id = $1 AND code = upper($2)`, property, r["level_code"]).Scan(&lv); err != nil {
				return uuid.Nil, fmt.Errorf("caddy level %s not found", r["level_code"])
			}
			in.LevelID = &lv
		}
		_, err := d.P2.Experience.SetCaddyProfile(ctx, tx, property, cid, in)
		return cid, err
	case "hio":
		n, err := strconv.Atoi(r["hole_number"])
		if err != nil {
			return uuid.Nil, fmt.Errorf("hole_number must be a number")
		}
		hid, err := experience.HoleByLabel(ctx, tx, property, r["section_code"], n)
		if err != nil {
			return uuid.Nil, err
		}
		cust, err := d.optionalCustomer(ctx, tx, property, r["customer_legacy_id"])
		if err != nil {
			return uuid.Nil, err
		}
		var witnesses []experience.Witness
		for _, w := range strings.Split(r["witnesses"], ";") {
			if w = strings.TrimSpace(w); w != "" {
				witnesses = append(witnesses, experience.Witness{Name: w})
			}
		}
		h, err := d.P2.Experience.ImportHIO(ctx, tx, property, experience.HIOInput{CustomerID: cust, PlayerName: r["player_name"], HoleID: hid,
			AchievedOn: r["achieved_on"], Witnesses: witnesses, Notes: "Migrated " + r["legacy_id"]})
		return h.ID, err
	case "hall_of_fame":
		in := map[string]any{"category": r["category"], "title": r["title"]}
		for k, f := range map[string]string{"division": "division", "player_name": "playerName", "achieved_on": "achievedOn", "description": "description"} {
			if r[k] != "" {
				in[f] = r[k]
			}
		}
		for k, f := range map[string]string{"year": "year", "score": "score"} {
			if r[k] != "" {
				n, err := strconv.Atoi(r[k])
				if err != nil {
					return uuid.Nil, fmt.Errorf("%s must be a number", k)
				}
				in[f] = float64(n)
			}
		}
		if r["category"] == "club_history" {
			in["consent"] = "not_required"
		}
		out, err := d.Engine.CreateRow(ctx, tx, experience.HallOfFame, in)
		if err != nil {
			return uuid.Nil, err
		}
		return uuid.Parse(fmt.Sprint(out["id"]))
	}
	return uuid.Nil, fmt.Errorf("no loader for %s", entity)
}

// p2Metrics are the wave-2 reconciliation figures (FR-MIG-P2-07).
func p2Metrics(ctx context.Context, tx pgx.Tx, property uuid.UUID, out map[string]string) error {
	for _, c := range []struct{ metric, sql string }{
		{"outlets", `SELECT count(*)::text FROM ` + Schema + `.id_map WHERE property_id = $1 AND entity = 'outlets'`},
		{"products", `SELECT count(*)::text FROM ` + Schema + `.id_map WHERE property_id = $1 AND entity = 'products'`},
		{"voucher_liability", `SELECT coalesce(sum(e.amount), 0)::numeric(19,2)::text FROM billing.deferred_revenue_entries e
			JOIN ` + Schema + `.id_map i ON i.target_id = e.ref_id AND i.entity = 'vouchers' WHERE e.property_id = $1 AND e.ref_type = 'commercial.voucher'`},
		{"future_reservations", `SELECT count(*)::text FROM reservation.reservations WHERE property_id = $1 AND channel = 'import' AND status = 'confirmed'`},
		{"enrollments", `SELECT count(*)::text FROM ` + Schema + `.id_map WHERE property_id = $1 AND entity = 'enrollments'`},
		{"hio", `SELECT count(*)::text FROM ` + Schema + `.id_map WHERE property_id = $1 AND entity = 'hio'`},
		{"hall_of_fame", `SELECT count(*)::text FROM ` + Schema + `.id_map WHERE property_id = $1 AND entity = 'hall_of_fame'`},
	} {
		var v string
		if err := tx.QueryRow(ctx, c.sql, property).Scan(&v); err != nil {
			return fmt.Errorf("%s: %w", c.metric, err)
		}
		out[c.metric] = v
	}
	return nil
}
