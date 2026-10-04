// Package migration is the data migration pipeline `oneclub import rhapsody`
// (Technical Doc §8.2, PRD P2 EP-31 "Migrasi Gelombang 2"): extract (the
// agreed CSV extracts of Rhapsody and club spreadsheets) → staging schema →
// validation → load through the module services → reconciliation. Loads
// are idempotent: id_map remembers every legacy id already loaded, so a run
// can be repeated (dry runs, deltas at cutover).
package migration

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/crm"
	"oneclub/internal/golf"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/membership"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/resource"
	"oneclub/internal/reservation"
	"oneclub/internal/sportclub"
)

const source = "rhapsody"

// Scopes and the entities (CSV files) they load, in dependency order.
var Scopes = map[string][]string{
	"customers":      {"customers"},
	"pos":            {"outlets", "products"},
	"member_charges": {"member_charges"},
	"vouchers":       {"vouchers"},
	"memberships":    {"memberships"},
	"reservations":   {"reservations", "enrollments"},
	"golf_history":   {"caddies", "hio", "hall_of_fame"},
}

// ScopeOrder is the order of --scope=all.
var ScopeOrder = []string{"customers", "pos", "member_charges", "vouchers", "memberships", "reservations", "golf_history"}

// Columns are the required CSV headers per entity (runbook appendix).
var Columns = map[string][]string{
	"customers":      {"legacy_id", "code", "name"},
	"outlets":        {"legacy_id", "code", "name", "outlet_type"},
	"products":       {"legacy_id", "code", "name", "product_type", "price"},
	"member_charges": {"legacy_id", "customer_legacy_id", "amount"},
	"vouchers":       {"legacy_id", "code", "voucher_type_code", "original_quantity", "remaining_quantity", "price_paid"},
	"memberships":    {"legacy_id", "customer_legacy_id", "type_code", "membership_no", "start_date"},
	"reservations":   {"legacy_id", "resource_code", "start", "end"},
	"enrollments":    {"legacy_id", "program_code", "customer_legacy_id", "valid_until"},
	"caddies":        {"legacy_id", "code", "name"},
	"hio":            {"legacy_id", "player_name", "section_code", "hole_number", "achieved_on"},
	"hall_of_fame":   {"legacy_id", "category", "title"},
}

// Options of one run.
type Options struct {
	Scope    string // a key of Scopes or "all"
	Dir      string // folder with <entity>.csv files
	Property string // property code
	DryRun   bool   // validate and load inside a transaction that is rolled back
}

// Deps are the module services the loaders use (wired by the CLI).
type Deps struct {
	DB         *dbtx.DB
	Engine     *resource.Engine
	Billing    *billing.Service
	Commercial *commercial.Module
	Membership *membership.Module
	Res        *reservation.Engine
	Golf       *golf.Module
}

// EntityResult counts one entity of a batch.
type EntityResult struct {
	Entity  string   `json:"entity"`
	Rows    int      `json:"rows"`
	Loaded  int      `json:"loaded"`
	Skipped int      `json:"skipped" doc:"Already loaded by an earlier run"`
	Invalid int      `json:"invalid"`
	Errors  []string `json:"errors,omitempty"`
}

// Report is the outcome of a run: counts and the reconciliation figures to
// be signed off by the club (FR-MIG-P2-07).
type Report struct {
	BatchID        uuid.UUID         `json:"batchId"`
	Scope          string            `json:"scope"`
	DryRun         bool              `json:"dryRun"`
	Entities       []EntityResult    `json:"entities"`
	Reconciliation map[string]string `json:"reconciliation"`
}

type row struct {
	no       int
	legacyID string
	data     map[string]string
}

func readCSV(dir, entity string) ([]row, error) {
	f, err := os.Open(filepath.Join(dir, entity+".csv"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.TrimLeadingSpace = true
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("%s.csv: %w", entity, err)
	}
	for i := range header {
		header[i] = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(header[i], string(rune(0xFEFF)))))
	}
	for _, c := range Columns[entity] {
		if !slices.Contains(header, c) {
			return nil, fmt.Errorf("%s.csv: missing column %s", entity, c)
		}
	}
	var out []row
	for n := 2; ; n++ {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s.csv line %d: %w", entity, n, err)
		}
		m := map[string]string{}
		for i, h := range header {
			if i < len(rec) {
				m[h] = strings.TrimSpace(rec[i])
			}
		}
		out = append(out, row{no: n, legacyID: m["legacy_id"], data: m})
	}
	return out, nil
}

// Run executes the pipeline for one property.
func Run(ctx context.Context, d Deps, o Options) (Report, error) {
	scopes := []string{o.Scope}
	if o.Scope == "all" {
		scopes = ScopeOrder
	}
	for _, s := range scopes {
		if _, ok := Scopes[s]; !ok {
			return Report{}, fmt.Errorf("unknown scope %q (one of %s, all)", s, strings.Join(ScopeOrder, ", "))
		}
	}
	sys := dbtx.System(authz.WithPrincipal(ctx, authz.System()))
	var pid uuid.UUID
	if err := d.DB.WithReadTx(sys, func(tx pgx.Tx) error {
		return tx.QueryRow(sys, `SELECT id FROM platform.properties WHERE code = upper($1)`, o.Property).Scan(&pid)
	}); err != nil {
		return Report{}, fmt.Errorf("property %s: %w", o.Property, err)
	}
	ctx = reqctx.WithProperty(dbtx.WithScope(authz.WithPrincipal(ctx, authz.System()), dbtx.Scope{AllProperties: true}), pid)
	rep := Report{BatchID: id.New(), Scope: o.Scope, DryRun: o.DryRun, Reconciliation: map[string]string{}}
	errDry := errors.New("dry run")
	err := d.DB.WithTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO migration.batches (id, property_id, source, scope, dry_run) VALUES ($1,$2,$3,$4,$5)`,
			rep.BatchID, pid, source, o.Scope, o.DryRun); err != nil {
			return err
		}
		l := &loader{d: d, tx: tx, property: pid, batch: rep.BatchID, sums: map[string]decimal.Decimal{}}
		for _, s := range scopes {
			for _, entity := range Scopes[s] {
				rows, err := readCSV(o.Dir, entity)
				if err != nil {
					return err
				}
				res, err := l.load(ctx, entity, rows)
				if err != nil {
					return err
				}
				if len(rows) > 0 {
					rep.Entities = append(rep.Entities, res)
				}
			}
		}
		rec, err := l.reconcile(ctx)
		if err != nil {
			return err
		}
		rep.Reconciliation = rec
		counts, _ := json.Marshal(rep.Entities)
		recon, _ := json.Marshal(rec)
		if _, err := tx.Exec(ctx, `UPDATE migration.batches SET status = 'loaded', counts = $2, reconciliation = $3, finished_at = now() WHERE id = $1`,
			rep.BatchID, counts, recon); err != nil {
			return err
		}
		if o.DryRun {
			return errDry
		}
		return nil
	})
	if errors.Is(err, errDry) {
		err = nil
	}
	return rep, err
}

// SignOff records the club's reconciliation sign-off of a batch.
func SignOff(ctx context.Context, db *dbtx.DB, batch uuid.UUID, by string) error {
	if strings.TrimSpace(by) == "" {
		return errors.New("--by is required")
	}
	sys := dbtx.System(ctx)
	return db.WithTx(sys, func(tx pgx.Tx) error {
		tag, err := tx.Exec(sys, `UPDATE migration.batches SET signed_off_by = $2, signed_off_at = now() WHERE id = $1 AND status = 'loaded' AND NOT dry_run`, batch, by)
		if err == nil && tag.RowsAffected() == 0 {
			err = errors.New("batch not found, not loaded or a dry run")
		}
		return err
	})
}

type loader struct {
	d        Deps
	tx       pgx.Tx
	property uuid.UUID
	batch    uuid.UUID
	sums     map[string]decimal.Decimal
}

func (l *loader) mapped(ctx context.Context, entity, legacy string) (uuid.UUID, bool, error) {
	var t uuid.UUID
	err := l.tx.QueryRow(ctx, `SELECT target_id FROM migration.id_map WHERE property_id = $1 AND source = $2 AND entity = $3 AND legacy_id = $4`,
		l.property, source, entity, legacy).Scan(&t)
	if dbtx.IsNoRows(err) {
		return t, false, nil
	}
	return t, err == nil, err
}

func (l *loader) customer(ctx context.Context, legacy string) (*uuid.UUID, error) {
	if legacy == "" {
		return nil, nil
	}
	t, ok, err := l.mapped(ctx, "customers", legacy)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errs.Validation("unknown_customer", "customer "+legacy+" is not migrated")
	}
	return &t, nil
}

func dec(s string) (decimal.Decimal, error) {
	if s == "" {
		return decimal.Zero, nil
	}
	return decimal.NewFromString(strings.ReplaceAll(s, ",", ""))
}

func date(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", s)
	return &t, err
}

// load stages, validates and loads the rows of one entity; every row runs in
// a savepoint so a bad row is reported without stopping the batch.
func (l *loader) load(ctx context.Context, entity string, rows []row) (EntityResult, error) {
	res := EntityResult{Entity: entity, Rows: len(rows)}
	for _, r := range rows {
		raw, _ := json.Marshal(r.data)
		if _, err := l.tx.Exec(ctx, `INSERT INTO migration.staging_rows (batch_id, property_id, entity, row_no, legacy_id, data) VALUES ($1,$2,$3,$4,$5,$6)`,
			l.batch, l.property, entity, r.no, r.legacyID, raw); err != nil {
			return res, err
		}
		if r.legacyID == "" {
			res.Invalid++
			res.Errors = append(res.Errors, fmt.Sprintf("line %d: legacy_id is empty", r.no))
			continue
		}
		if t, ok, err := l.mapped(ctx, entity, r.legacyID); err != nil {
			return res, err
		} else if ok {
			res.Skipped++
			if _, err := l.tx.Exec(ctx, `UPDATE migration.staging_rows SET status = 'skipped', target_id = $4 WHERE batch_id = $1 AND entity = $2 AND row_no = $3`,
				l.batch, entity, r.no, t); err != nil {
				return res, err
			}
			continue
		}
		sp, err := l.tx.Begin(ctx)
		if err != nil {
			return res, err
		}
		target, lerr := l.one(ctx, sp, entity, r)
		if lerr != nil {
			_ = sp.Rollback(ctx)
			var de *errs.Error
			if !errors.As(lerr, &de) && !strings.Contains(lerr.Error(), "parsing") && !strings.Contains(lerr.Error(), "cannot parse") {
				return res, fmt.Errorf("%s line %d: %w", entity, r.no, lerr)
			}
			res.Invalid++
			msg := lerr.Error()
			if de != nil {
				for _, f := range de.Fields {
					msg += "; " + f.Field + ": " + f.Message
				}
			}
			res.Errors = append(res.Errors, fmt.Sprintf("line %d (%s): %s", r.no, r.legacyID, msg))
			if _, err := l.tx.Exec(ctx, `UPDATE migration.staging_rows SET status = 'invalid', error = $4 WHERE batch_id = $1 AND entity = $2 AND row_no = $3`,
				l.batch, entity, r.no, lerr.Error()); err != nil {
				return res, err
			}
			continue
		}
		if _, err := sp.Exec(ctx, `INSERT INTO migration.id_map (property_id, source, entity, legacy_id, target_id, batch_id) VALUES ($1,$2,$3,$4,$5,$6)`,
			l.property, source, entity, r.legacyID, target, l.batch); err != nil {
			_ = sp.Rollback(ctx)
			return res, err
		}
		if _, err := sp.Exec(ctx, `UPDATE migration.staging_rows SET status = 'loaded', target_id = $4 WHERE batch_id = $1 AND entity = $2 AND row_no = $3`,
			l.batch, entity, r.no, target); err != nil {
			_ = sp.Rollback(ctx)
			return res, err
		}
		if err := sp.Commit(ctx); err != nil {
			return res, err
		}
		res.Loaded++
	}
	if len(res.Errors) > 20 {
		res.Errors = append(res.Errors[:20], fmt.Sprintf("… %d more", len(res.Errors)-20))
	}
	return res, nil
}

func (l *loader) one(ctx context.Context, tx pgx.Tx, entity string, r row) (uuid.UUID, error) {
	d := r.data
	switch entity {
	case "customers":
		in := map[string]any{"code": d["code"], "name": d["name"]}
		for k, f := range map[string]string{"phone": "phone", "email": "email", "birth_date": "birthDate", "gender": "gender", "customer_type": "customerType"} {
			if d[k] != "" {
				in[f] = d[k]
			}
		}
		out, err := l.d.Engine.CreateRow(ctx, tx, crm.Customers, in)
		if err != nil {
			return uuid.Nil, err
		}
		return uuid.Parse(fmt.Sprint(out["id"]))
	case "outlets":
		out, err := l.d.Engine.CreateRow(ctx, tx, commercial.Outlets, map[string]any{"code": d["code"], "name": d["name"], "outletType": d["outlet_type"]})
		if err != nil {
			return uuid.Nil, err
		}
		return uuid.Parse(fmt.Sprint(out["id"]))
	case "products":
		in := map[string]any{"code": d["code"], "name": d["name"], "productType": d["product_type"], "price": d["price"]}
		if d["category"] != "" {
			in["category"] = d["category"]
		}
		if d["member_price"] != "" {
			in["memberPrice"] = d["member_price"]
		}
		out, err := l.d.Engine.CreateRow(ctx, tx, commercial.Products, in)
		if err != nil {
			return uuid.Nil, err
		}
		return uuid.Parse(fmt.Sprint(out["id"]))
	case "member_charges":
		cust, err := l.customer(ctx, d["customer_legacy_id"])
		if err != nil {
			return uuid.Nil, err
		}
		if cust == nil {
			return uuid.Nil, errs.Validation("required", "customer_legacy_id is required")
		}
		amt, err := dec(d["amount"])
		if err != nil {
			return uuid.Nil, err
		}
		acc, err := l.d.Billing.EnsureMemberAccount(ctx, tx, l.property, *cust, nil, decimal.NewFromInt(5000000))
		if err != nil {
			return uuid.Nil, err
		}
		desc := d["description"]
		if desc == "" {
			desc = "Rhapsody F&B outstanding (migrated)"
		}
		if _, err := l.d.Billing.PostAccountEntry(ctx, tx, acc.ID, billing.AccountEntryRequest{EntryType: "opening_balance", Amount: amt, BusinessLine: billing.LinePOS,
			Description: desc, SourceType: "migration.member_charge"}); err != nil {
			return uuid.Nil, err
		}
		l.sums["member_charges_source"] = l.sums["member_charges_source"].Add(amt)
		return acc.ID, nil
	case "vouchers":
		cust, err := l.customer(ctx, d["customer_legacy_id"])
		if err != nil {
			return uuid.Nil, err
		}
		orig, err := dec(d["original_quantity"])
		if err != nil {
			return uuid.Nil, err
		}
		rem, err := dec(d["remaining_quantity"])
		if err != nil {
			return uuid.Nil, err
		}
		paid, err := dec(d["price_paid"])
		if err != nil {
			return uuid.Nil, err
		}
		exp, err := date(d["expires_on"])
		if err != nil {
			return uuid.Nil, err
		}
		if exp != nil {
			e := time.Date(exp.Year(), exp.Month(), exp.Day(), 23, 59, 59, 0, calendar.Location(ctx, tx))
			exp = &e
		}
		v, err := l.d.Commercial.Issue(ctx, tx, commercial.IssueRequest{PropertyID: l.property, TypeCode: strings.ToUpper(d["voucher_type_code"]), CustomerID: cust,
			Via: "migration", PricePaid: paid, Quantity: &rem, Original: &orig, ExpiresAt: exp, Code: d["code"], Notes: "Migrated from Rhapsody " + r.legacyID})
		if err != nil {
			return uuid.Nil, err
		}
		if orig.IsPositive() {
			l.sums["voucher_liability_source"] = l.sums["voucher_liability_source"].Add(rem.Mul(paid.Div(orig)).Round(0))
		}
		return v.ID, nil
	case "memberships":
		cust, err := l.customer(ctx, d["customer_legacy_id"])
		if err != nil {
			return uuid.Nil, err
		}
		if cust == nil {
			return uuid.Nil, errs.Validation("required", "customer_legacy_id is required")
		}
		var tid uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM membership.types WHERE property_id = $1 AND code = upper($2)`, l.property, d["type_code"]).Scan(&tid); err != nil {
			return uuid.Nil, errs.Validation("unknown_type", "membership type "+d["type_code"]+" not found")
		}
		sd, err := date(d["start_date"])
		if err != nil || sd == nil {
			return uuid.Nil, errs.Validation("invalid_date", "start_date must be YYYY-MM-DD")
		}
		ed, err := date(d["end_date"])
		if err != nil {
			return uuid.Nil, err
		}
		ms, err := l.d.Membership.Create(ctx, tx, membership.CreateRequest{PropertyID: l.property, TypeID: tid, CustomerID: *cust, StartDate: *sd, EndDate: ed,
			MembershipNo: d["membership_no"], CardNo: d["card_no"], LegacyCardNo: d["legacy_card_no"]})
		if err != nil {
			return uuid.Nil, err
		}
		if ms.Status == "active" {
			l.sums["active_members_source"] = l.sums["active_members_source"].Add(decimal.NewFromInt(1))
		}
		return ms.ID, nil
	case "reservations":
		var rid uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM reservation.resources WHERE property_id = $1 AND code = upper($2)`, l.property, d["resource_code"]).Scan(&rid); err != nil {
			return uuid.Nil, errs.Validation("unknown_resource", "resource "+d["resource_code"]+" not found")
		}
		st, err1 := time.Parse(time.RFC3339, d["start"])
		en, err2 := time.Parse(time.RFC3339, d["end"])
		if err1 != nil || err2 != nil {
			return uuid.Nil, errs.Validation("invalid_time", "start and end must be RFC 3339")
		}
		cust, err := l.customer(ctx, d["customer_legacy_id"])
		if err != nil {
			return uuid.Nil, err
		}
		line := d["business_line"]
		if line == "" {
			line = "other"
		}
		r, err := l.d.Res.Book(ctx, tx, l.property, reservation.BookRequest{BusinessLine: line, CustomerID: cust, GuestName: d["guest_name"], Channel: "import",
			Confirm: true, SourceType: "migration.rhapsody", Notes: d["notes"], Attributes: map[string]any{"legacyId": r.legacyID},
			Lines: []reservation.LineRequest{{ResourceID: rid, Start: st, End: en, Description: "Migrated booking"}}})
		if err != nil {
			return uuid.Nil, err
		}
		l.sums["future_reservations_source"] = l.sums["future_reservations_source"].Add(decimal.NewFromInt(1))
		return r.ID, nil
	case "enrollments":
		cust, err := l.customer(ctx, d["customer_legacy_id"])
		if err != nil {
			return uuid.Nil, err
		}
		if cust == nil {
			return uuid.Nil, errs.Validation("required", "customer_legacy_id is required")
		}
		vu, err := date(d["valid_until"])
		if err != nil || vu == nil {
			return uuid.Nil, errs.Validation("invalid_date", "valid_until must be YYYY-MM-DD")
		}
		return sportclub.ImportEnrollment(ctx, tx, l.property, d["program_code"], *cust, *vu)
	case "caddies":
		in := map[string]any{"code": d["code"], "name": d["name"]}
		if d["level_code"] != "" {
			var lv uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT id FROM golf.caddy_levels WHERE property_id = $1 AND code = upper($2)`, l.property, d["level_code"]).Scan(&lv); err != nil {
				return uuid.Nil, errs.Validation("unknown_level", "caddy level "+d["level_code"]+" not found")
			}
			in["levelId"] = lv.String()
		}
		for k, f := range map[string]string{"joined_on": "joinedOn", "phone": "phone", "gender": "gender"} {
			if d[k] != "" {
				in[f] = d[k]
			}
		}
		out, err := l.d.Engine.CreateRow(ctx, tx, golf.Caddies, in)
		if err != nil {
			return uuid.Nil, err
		}
		return uuid.Parse(fmt.Sprint(out["id"]))
	case "hio":
		n, err := strconv.Atoi(d["hole_number"])
		if err != nil {
			return uuid.Nil, errs.Validation("invalid_hole", "hole_number must be a number")
		}
		hid, err := golf.HoleByLabel(ctx, tx, l.property, d["section_code"], n)
		if err != nil {
			return uuid.Nil, err
		}
		cust, err := l.customer(ctx, d["customer_legacy_id"])
		if err != nil {
			return uuid.Nil, err
		}
		var witnesses []golf.Witness
		if d["witnesses"] != "" {
			for _, w := range strings.Split(d["witnesses"], ";") {
				witnesses = append(witnesses, golf.Witness{Name: strings.TrimSpace(w)})
			}
		}
		h, err := l.d.Golf.ImportHIO(ctx, tx, l.property, golf.HIOInput{CustomerID: cust, PlayerName: d["player_name"], HoleID: hid, AchievedOn: d["achieved_on"],
			Witnesses: witnesses, Notes: "Migrated " + r.legacyID})
		return h.ID, err
	case "hall_of_fame":
		in := map[string]any{"category": d["category"], "title": d["title"]}
		for k, f := range map[string]string{"division": "division", "player_name": "playerName", "achieved_on": "achievedOn", "description": "description"} {
			if d[k] != "" {
				in[f] = d[k]
			}
		}
		for k, f := range map[string]string{"year": "year", "score": "score"} {
			if d[k] != "" {
				n, err := strconv.Atoi(d[k])
				if err != nil {
					return uuid.Nil, errs.Validation("invalid_number", k+" must be a number")
				}
				in[f] = float64(n)
			}
		}
		if d["category"] == "club_history" {
			in["consent"] = "not_required"
		}
		out, err := l.d.Engine.CreateRow(ctx, tx, golf.HallOfFame, in)
		if err != nil {
			return uuid.Nil, err
		}
		return uuid.Parse(fmt.Sprint(out["id"]))
	}
	return uuid.Nil, fmt.Errorf("no loader for %s", entity)
}

// reconcile compares source totals with what OneClub holds (FR-MIG-P2-07).
func (l *loader) reconcile(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range l.sums {
		out[k] = v.String()
	}
	var outstanding, liability string
	var members, future int
	if err := l.tx.QueryRow(ctx, `SELECT trim_scale(coalesce(sum(amount), 0))::text FROM billing.member_account_entries WHERE property_id = $1
		AND entry_type = 'opening_balance'`, l.property).Scan(&outstanding); err != nil {
		return out, err
	}
	if err := l.tx.QueryRow(ctx, `SELECT trim_scale(coalesce(sum(e.amount), 0))::text FROM billing.deferred_revenue_entries e
		JOIN commercial.vouchers v ON v.id = e.ref_id WHERE e.property_id = $1 AND e.ref_type = 'commercial.voucher' AND v.issued_via = 'migration'`, l.property).Scan(&liability); err != nil {
		return out, err
	}
	if err := l.tx.QueryRow(ctx, `SELECT count(*) FROM membership.memberships m JOIN migration.id_map i ON i.target_id = m.id AND i.entity = 'memberships'
		WHERE m.property_id = $1 AND m.status = 'active'`, l.property).Scan(&members); err != nil {
		return out, err
	}
	if err := l.tx.QueryRow(ctx, `SELECT count(*) FROM reservation.reservations WHERE property_id = $1 AND channel = 'import' AND status = 'confirmed'`, l.property).Scan(&future); err != nil {
		return out, err
	}
	out["member_charges_oneclub"] = outstanding
	out["voucher_liability_oneclub"] = liability
	out["active_members_oneclub"] = strconv.Itoa(members)
	out["future_reservations_oneclub"] = strconv.Itoa(future)
	return out, nil
}
