package rhapsody

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/crm"
	"oneclub/internal/golf"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/membership"
	"oneclub/internal/platform/resource"
)

// Deps are the modules the migration writes through.
type Deps struct {
	DB       *dbtx.DB
	Engine   *resource.Engine
	Golf     *golf.Module
	Billing  *billing.Service
	Location func() *time.Location
}

// Issue is one validation finding.
type Issue struct {
	Entity  string `json:"entity"`
	Row     int    `json:"row"`
	Field   string `json:"field,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// PropertyByCode resolves the target property.
func PropertyByCode(ctx context.Context, db *dbtx.DB, code string) (uuid.UUID, error) {
	var p uuid.UUID
	err := db.Primary.QueryRow(dbtx.System(ctx), `SELECT id FROM platform.properties WHERE code = $1`, code).Scan(&p)
	if err != nil {
		return p, fmt.Errorf("property %s not found: %w", code, err)
	}
	return p, nil
}

func scoped(ctx context.Context, property uuid.UUID) context.Context {
	return reqctx.WithProperty(dbtx.System(ctx), property)
}

// csvOf renders rows as a CSV for the generic import engine.
func csvOf(header []string, rows [][]string) *bytes.Reader {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write(header)
	for _, r := range rows {
		_ = w.Write(r)
	}
	w.Flush()
	return bytes.NewReader(b.Bytes())
}

func gender(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "m", "male", "l", "laki-laki", "pria":
		return "male"
	case "f", "female", "p", "perempuan", "wanita":
		return "female"
	}
	return ""
}

func memberStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "a", "active", "aktif":
		return "active"
	case "e", "expired", "kadaluarsa", "kedaluwarsa":
		return "expired"
	case "i", "inactive", "nonaktif", "non-aktif", "suspended":
		return "inactive"
	}
	return ""
}

// customerCode is the OneClub code of a migrated customer.
func customerCode(legacy string) string { return "RH-" + legacy }

// engineRows builds the import CSV of master entities.
func engineRows(rows []map[string]string, entity string) ([]string, [][]string) {
	var header []string
	var out [][]string
	switch entity {
	case "customers":
		header = []string{"code", "name", "email", "phone", "gender", "birthDate", "address", "city", "idNumber", "legacyRef", "duplicateAcknowledged"}
		for _, r := range rows {
			bd := ""
			if d, err := parseDate(r["birth_date"]); err == nil {
				bd = d.Format("2006-01-02")
			}
			out = append(out, []string{customerCode(r["legacy_id"]), r["name"], r["email"], r["phone"], gender(r["gender"]), bd, r["address"], r["city"],
				r["id_number"], r["legacy_id"], "true"})
		}
	case "corporate_accounts":
		header = []string{"code", "name", "npwp", "contactName", "email", "phone", "address", "legacyRef"}
		for _, r := range rows {
			out = append(out, []string{"RHC-" + r["legacy_id"], r["name"], r["npwp"], r["contact_name"], r["email"], r["phone"], r["address"], r["legacy_id"]})
		}
	case "caddies":
		header = []string{"code", "name", "gender", "phone", "partnershipStatus", "legacyRef"}
		for _, r := range rows {
			ps := strings.ToLower(r["partnership_status"])
			if ps == "" {
				ps = "partner"
			}
			out = append(out, []string{r["code"], r["name"], gender(r["gender"]), r["phone"], ps, r["code"]})
		}
	case "golf_carts":
		header = []string{"code", "name", "cartType", "capacity", "legacyRef"}
		for _, r := range rows {
			ct, cp := strings.ToLower(r["cart_type"]), r["capacity"]
			if ct == "" {
				ct = "electric"
			}
			if cp == "" {
				cp = "2"
			}
			out = append(out, []string{r["code"], r["name"], ct, cp, r["code"]})
		}
	case "lockers":
		header = []string{"code", "name", "area", "zone", "legacyRef"}
		for _, r := range rows {
			area := gender(r["area"])
			if area == "" {
				area = strings.ToLower(r["area"])
			}
			out = append(out, []string{r["code"], r["name"], area, r["zone"], r["code"]})
		}
	}
	return header, out
}

var engineDefs = map[string]*resource.Def{"customers": crm.Customers, "corporate_accounts": crm.CorporateAccounts,
	"caddies": golf.Caddies, "golf_carts": golf.GolfCarts, "lockers": golf.Lockers}

// importMaster runs the generic import (preview rolls back).
func (d *Deps) importMaster(ctx context.Context, tx pgx.Tx, entity string, rows []map[string]string) (resource.ImportResult, error) {
	header, data := engineRows(rows, entity)
	return d.Engine.Import(ctx, tx, engineDefs[entity], csvOf(header, data))
}

// Validate checks the staged data and stores the issues (FR-MIG-10).
func (d *Deps) Validate(ctx context.Context, property uuid.UUID) ([]Issue, error) {
	ctx = scoped(ctx, property)
	var issues []Issue
	add := func(e string, row int, field, code, msg string) {
		issues = append(issues, Issue{Entity: e, Row: row, Field: field, Code: code, Message: msg})
	}
	data := map[string][]map[string]string{}
	err := d.DB.WithTx(ctx, func(tx pgx.Tx) error {
		for _, e := range Entities {
			rows, err := staged(ctx, tx, e.Name)
			if err != nil {
				return err
			}
			data[e.Name] = rows
			if e.Key != "" {
				seen := map[string]int{}
				for _, r := range rows {
					row, _ := strconv.Atoi(r["row_no"])
					k := strings.ToUpper(r[e.Key])
					if k == "" {
						add(e.Name, row, e.Key, "required", e.Key+" is required")
					} else if first, dup := seen[k]; dup {
						add(e.Name, row, e.Key, "duplicate", fmt.Sprintf("%s %s already on row %d", e.Key, r[e.Key], first))
					} else {
						seen[k] = row
					}
				}
			}
		}
		customers, corps, members := keys(data["customers"], "legacy_id"), keys(data["corporate_accounts"], "legacy_id"), keys(data["members"], "member_no")
		// master entities: the import engine's own row validation, rolled back
		for name := range engineDefs {
			if len(data[name]) == 0 {
				continue
			}
			sp, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			res, err := d.importMaster(ctx, sp, name, data[name])
			_ = sp.Rollback(ctx)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			for _, re := range res.Errors {
				row := re.Row
				if row > 1 && row-2 < len(data[name]) {
					row, _ = strconv.Atoi(data[name][row-2]["row_no"])
				}
				add(name, row, re.Field, re.Code, re.Message)
			}
		}
		for _, r := range data["customers"] {
			row, _ := strconv.Atoi(r["row_no"])
			if v := r["birth_date"]; v != "" {
				if _, err := parseDate(v); err != nil {
					add("customers", row, "birth_date", "invalid_date", err.Error())
				}
			}
			if v := r["gender"]; v != "" && gender(v) == "" {
				add("customers", row, "gender", "invalid_gender", "gender must be M/F")
			}
			if v := r["handicap_index"]; v != "" {
				if h, err := decimal.NewFromString(v); err != nil || h.LessThan(decimal.NewFromInt(-10)) || h.GreaterThan(decimal.NewFromInt(54)) {
					add("customers", row, "handicap_index", "invalid_handicap", "handicap index must be -10 … 54")
				}
			}
		}
		for _, r := range data["corporate_nominees"] {
			row, _ := strconv.Atoi(r["row_no"])
			if !corps[strings.ToUpper(r["corporate_legacy_id"])] {
				add("corporate_nominees", row, "corporate_legacy_id", "unknown_reference", "corporate account not in corporate_accounts.csv")
			}
			if !customers[strings.ToUpper(r["customer_legacy_id"])] {
				add("corporate_nominees", row, "customer_legacy_id", "unknown_reference", "customer not in customers.csv")
			}
		}
		types := map[string]bool{}
		trows, err := tx.Query(ctx, `SELECT upper(code) FROM membership.types WHERE property_id = $1 AND archived_at IS NULL`, property)
		if err != nil {
			return err
		}
		for trows.Next() {
			var c string
			if err := trows.Scan(&c); err != nil {
				trows.Close()
				return err
			}
			types[c] = true
		}
		trows.Close()
		for _, r := range data["members"] {
			row, _ := strconv.Atoi(r["row_no"])
			if !customers[strings.ToUpper(r["customer_legacy_id"])] {
				add("members", row, "customer_legacy_id", "unknown_reference", "customer not in customers.csv")
			}
			if !types[strings.ToUpper(r["type_code"])] {
				add("members", row, "type_code", "unknown_type", "membership type "+r["type_code"]+" is not configured in OneClub")
			}
			if memberStatus(r["status"]) == "" {
				add("members", row, "status", "invalid_status", "status must be active, expired or inactive")
			}
			start, err := parseDate(r["starts_on"])
			if err != nil {
				add("members", row, "starts_on", "invalid_date", err.Error())
			}
			if v := r["ends_on"]; v != "" {
				if end, err := parseDate(v); err != nil {
					add("members", row, "ends_on", "invalid_date", err.Error())
				} else if end.Before(start) {
					add("members", row, "ends_on", "invalid_period", "ends_on is before starts_on")
				}
			} else if memberStatus(r["status"]) == "active" {
				add("members", row, "ends_on", "required", "an active membership needs an end date")
			}
			if p := r["principal_member_no"]; p != "" && !strings.EqualFold(p, r["member_no"]) && !members[strings.ToUpper(p)] {
				add("members", row, "principal_member_no", "unknown_reference", "principal member not in members.csv")
			}
			if c := r["corporate_legacy_id"]; c != "" && !corps[strings.ToUpper(c)] {
				add("members", row, "corporate_legacy_id", "unknown_reference", "corporate account not in corporate_accounts.csv")
			}
		}
		for _, r := range data["opening_balances"] {
			row, _ := strconv.Atoi(r["row_no"])
			if !members[strings.ToUpper(r["member_no"])] {
				add("opening_balances", row, "member_no", "unknown_reference", "member not in members.csv")
			}
			if _, err := decimal.NewFromString(strings.ReplaceAll(r["balance"], ",", "")); err != nil {
				add("opening_balances", row, "balance", "invalid_amount", "balance must be a number")
			}
			if _, err := parseDate(r["as_of"]); err != nil {
				add("opening_balances", row, "as_of", "invalid_date", err.Error())
			}
		}
		courses := map[string]bool{}
		crows, err := tx.Query(ctx, `SELECT upper(code) FROM golf.courses WHERE property_id = $1 AND archived_at IS NULL`, property)
		if err != nil {
			return err
		}
		for crows.Next() {
			var c string
			if err := crows.Scan(&c); err != nil {
				crows.Close()
				return err
			}
			courses[c] = true
		}
		crows.Close()
		today := time.Now().In(d.loc()).Format("2006-01-02")
		for _, r := range data["future_bookings"] {
			row, _ := strconv.Atoi(r["row_no"])
			if !courses[strings.ToUpper(r["course_code"])] {
				add("future_bookings", row, "course_code", "unknown_course", "course "+r["course_code"]+" is not configured")
			}
			if day, err := parseDate(r["play_date"]); err != nil {
				add("future_bookings", row, "play_date", "invalid_date", err.Error())
			} else if day.Format("2006-01-02") < today {
				add("future_bookings", row, "play_date", "past_booking", "only bookings from the cutover date are migrated (history goes to the archive)")
			}
			if _, err := time.Parse("15:04", r["tee_time"]); err != nil {
				add("future_bookings", row, "tee_time", "invalid_time", "tee time must be HH:MM")
			}
			if n, err := strconv.Atoi(r["players"]); err != nil || n < 1 || n > 4 {
				add("future_bookings", row, "players", "invalid_players", "players must be 1 … 4")
			}
			if m := r["member_no"]; m != "" && !members[strings.ToUpper(m)] {
				add("future_bookings", row, "member_no", "unknown_reference", "member not in members.csv")
			}
			if v := r["paid_amount"]; v != "" {
				if _, err := decimal.NewFromString(strings.ReplaceAll(v, ",", "")); err != nil {
					add("future_bookings", row, "paid_amount", "invalid_amount", "paid amount must be a number")
				}
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM `+Schema+`.issues`); err != nil {
			return err
		}
		for _, is := range issues {
			if _, err := tx.Exec(ctx, `INSERT INTO `+Schema+`.issues (entity, row_no, field, code, message) VALUES ($1,$2,$3,$4,$5)`,
				is.Entity, is.Row, is.Field, is.Code, is.Message); err != nil {
				return err
			}
		}
		return nil
	})
	return issues, err
}

func keys(rows []map[string]string, col string) map[string]bool {
	out := map[string]bool{}
	for _, r := range rows {
		out[strings.ToUpper(r[col])] = true
	}
	return out
}

func (d *Deps) loc() *time.Location {
	if d.Location != nil {
		return d.Location()
	}
	return time.UTC
}

// LoadReport counts what the load wrote.
type LoadReport struct {
	Entity   string
	Inserted int
	Updated  int
	Skipped  int
	Failed   int
	Errors   []string
}

// Load writes the staged data (validate first: rows with issues are
// skipped). Re-running a load updates instead of duplicating.
func (d *Deps) Load(ctx context.Context, property uuid.UUID) ([]LoadReport, error) {
	ctx = scoped(ctx, property)
	var out []LoadReport
	bad := map[string]map[int]bool{}
	err := d.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT entity, row_no FROM `+Schema+`.issues`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e string
			var r int
			if err := rows.Scan(&e, &r); err != nil {
				return err
			}
			if bad[e] == nil {
				bad[e] = map[int]bool{}
			}
			bad[e][r] = true
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	clean := func(tx pgx.Tx, entity string) ([]map[string]string, int, error) {
		rows, err := staged(ctx, tx, entity)
		if err != nil {
			return nil, 0, err
		}
		var ok []map[string]string
		skipped := 0
		for _, r := range rows {
			n, _ := strconv.Atoi(r["row_no"])
			if bad[entity][n] {
				skipped++
				continue
			}
			ok = append(ok, r)
		}
		return ok, skipped, nil
	}
	for _, e := range Entities {
		rep := LoadReport{Entity: e.Name}
		err := d.DB.WithTx(ctx, func(tx pgx.Tx) error {
			rows, skipped, err := clean(tx, e.Name)
			if err != nil {
				return err
			}
			rep.Skipped = skipped
			if len(rows) == 0 {
				return nil
			}
			if _, ok := engineDefs[e.Name]; ok {
				res, err := d.importMaster(ctx, tx, e.Name, rows)
				if err != nil {
					return err
				}
				rep.Inserted, rep.Updated, rep.Failed = res.InsertedRows, res.UpdatedRows, res.FailedRows
				for _, re := range res.Errors {
					rep.Errors = append(rep.Errors, fmt.Sprintf("row %d %s: %s", re.Row, re.Field, re.Message))
				}
				if e.Name == "customers" {
					return d.loadHandicaps(ctx, tx, property, rows)
				}
				return nil
			}
			for _, r := range rows {
				sp, err := tx.Begin(ctx)
				if err != nil {
					return err
				}
				inserted, err := d.loadRow(ctx, sp, property, e.Name, r)
				if err != nil {
					_ = sp.Rollback(ctx)
					rep.Failed++
					rep.Errors = append(rep.Errors, fmt.Sprintf("row %s: %v", r["row_no"], err))
					continue
				}
				if err := sp.Commit(ctx); err != nil {
					return err
				}
				if inserted {
					rep.Inserted++
				} else {
					rep.Updated++
				}
			}
			return nil
		})
		if err != nil {
			return out, fmt.Errorf("%s: %w", e.Name, err)
		}
		out = append(out, rep)
	}
	return out, nil
}

func (d *Deps) loadHandicaps(ctx context.Context, tx pgx.Tx, property uuid.UUID, rows []map[string]string) error {
	for _, r := range rows {
		if r["handicap_index"] == "" {
			continue
		}
		cid, err := crm.ByLegacyRef(ctx, tx, property, "customers", r["legacy_id"])
		if err != nil || cid == nil {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO golf.handicaps (id, property_id, customer_id, handicap_index, source, notes)
			SELECT gen_random_uuid(), $1, $2, $3::numeric, 'import', 'Rhapsody migration'
			WHERE NOT EXISTS (SELECT 1 FROM golf.handicaps WHERE customer_id = $2 AND source = 'import')`, property, *cid, r["handicap_index"]); err != nil {
			return err
		}
	}
	return nil
}

func (d *Deps) customer(ctx context.Context, tx pgx.Tx, property uuid.UUID, legacy string) (uuid.UUID, error) {
	cid, err := crm.ByLegacyRef(ctx, tx, property, "customers", legacy)
	if err != nil {
		return uuid.Nil, err
	}
	if cid == nil {
		return uuid.Nil, fmt.Errorf("customer %s was not loaded", legacy)
	}
	return *cid, nil
}

// loadRow loads one row of the non-master entities; inserted reports a new record.
func (d *Deps) loadRow(ctx context.Context, tx pgx.Tx, property uuid.UUID, entity string, r map[string]string) (bool, error) {
	switch entity {
	case "corporate_nominees":
		corp, err := crm.ByLegacyRef(ctx, tx, property, "corporate_accounts", r["corporate_legacy_id"])
		if err != nil || corp == nil {
			return false, fmt.Errorf("corporate account %s was not loaded", r["corporate_legacy_id"])
		}
		cid, err := d.customer(ctx, tx, property, r["customer_legacy_id"])
		if err != nil {
			return false, err
		}
		return true, crm.LinkNominee(ctx, tx, property, *corp, cid, r["title"])
	case "members":
		return d.loadMember(ctx, tx, property, r)
	case "opening_balances":
		return d.loadBalance(ctx, tx, property, r)
	case "future_bookings":
		return d.loadBooking(ctx, tx, property, r)
	}
	return false, nil
}

func (d *Deps) loadMember(ctx context.Context, tx pgx.Tx, property uuid.UUID, r map[string]string) (bool, error) {
	cid, err := d.customer(ctx, tx, property, r["customer_legacy_id"])
	if err != nil {
		return false, err
	}
	start, _ := parseDate(r["starts_on"])
	var end *time.Time
	if v := r["ends_on"]; v != "" {
		e, _ := parseDate(v)
		end = &e
	}
	var corp *uuid.UUID
	if c := r["corporate_legacy_id"]; c != "" {
		corp, _ = crm.ByLegacyRef(ctx, tx, property, "corporate_accounts", c)
	}
	var existed bool
	_ = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM membership.members WHERE property_id = $1 AND code = $2)`, property, r["member_no"]).Scan(&existed)
	_, err = membership.ImportMember(ctx, tx, property, membership.MemberImport{MemberNo: r["member_no"], CustomerID: cid, TypeCode: r["type_code"],
		StartsOn: start, EndsOn: end, Status: memberStatus(r["status"]), CardNumber: r["card_number"], PrincipalMemberNo: r["principal_member_no"],
		Relationship: r["relationship"], CorporateID: corp, LegacyRef: r["member_no"]})
	return !existed, err
}

const balanceNote = "Opening balance from Rhapsody as of "

func (d *Deps) loadBalance(ctx context.Context, tx pgx.Tx, property uuid.UUID, r map[string]string) (bool, error) {
	var memberID, cid uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id, customer_id FROM membership.members WHERE property_id = $1 AND code = $2`, property, r["member_no"]).
		Scan(&memberID, &cid); err != nil {
		return false, fmt.Errorf("member %s was not loaded", r["member_no"])
	}
	amount, _ := decimal.NewFromString(strings.ReplaceAll(r["balance"], ",", ""))
	asOf, _ := parseDate(r["as_of"])
	acct, err := d.Billing.EnsureAccount(ctx, tx, property, cid, "member", &memberID)
	if err != nil {
		return false, err
	}
	note := balanceNote + asOf.Format("2006-01-02")
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM billing.account_entries WHERE account_id = $1 AND entry_type = 'opening_balance')`, acct.ID).
		Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		// a dry run is repeated: replace the opening balance
		if _, err := tx.Exec(ctx, `UPDATE billing.account_entries SET amount = $2::numeric, description = $3 WHERE account_id = $1 AND entry_type = 'opening_balance'`,
			acct.ID, amount.String(), note); err != nil {
			return false, err
		}
		return false, nil
	}
	if amount.IsZero() {
		return true, nil
	}
	return true, billing.PostEntry(ctx, tx, property, acct.ID, "opening_balance", amount, "IDR", note, nil, nil)
}

func (d *Deps) loadBooking(ctx context.Context, tx pgx.Tx, property uuid.UUID, r map[string]string) (bool, error) {
	if b, err := golf.BookingByLegacyRef(ctx, tx, property, r["legacy_ref"]); err != nil || b != nil {
		return false, err
	}
	var course uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM golf.courses WHERE property_id = $1 AND upper(code) = upper($2)`, property, r["course_code"]).Scan(&course); err != nil {
		return false, err
	}
	day, _ := parseDate(r["play_date"])
	if _, err := d.Golf.Generate(ctx, tx, property, &course, day, 1); err != nil {
		return false, err
	}
	hm, _ := time.Parse("15:04", r["tee_time"])
	start := time.Date(day.Year(), day.Month(), day.Day(), hm.Hour(), hm.Minute(), 0, 0, d.loc())
	tee := 1
	if r["start_tee"] == "10" {
		tee = 10
	}
	var slot uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM golf.tee_times WHERE course_id = $1 AND start_at = $2 AND start_tee = $3 AND status <> 'closed'`,
		course, start, tee).Scan(&slot); err != nil {
		return false, fmt.Errorf("no tee time %s %s tee %d in the OneClub tee sheet", r["play_date"], r["tee_time"], tee)
	}
	n, _ := strconv.Atoi(r["players"])
	names := strings.Split(r["player_names"], ";")
	req := golf.BookingRequest{TeeTimeID: &slot, BookingType: "non_member", Channel: "import", ContactName: r["contact_name"], ContactPhone: r["contact_phone"],
		PaymentMode: "pay_at_venue", Notes: strings.TrimSpace("Migrated from Rhapsody " + r["legacy_ref"] + ". " + r["notes"])}
	if m := r["member_no"]; m != "" {
		req.BookingType = "member"
		req.Players = append(req.Players, golf.PlayerInput{PlayerType: "member", MemberNo: m})
	}
	for i := len(req.Players); i < n; i++ {
		name := ""
		if i < len(names) {
			name = strings.TrimSpace(names[i])
		}
		p := golf.PlayerInput{PlayerType: "non_member", Name: name, TBA: name == ""}
		if req.BookingType == "member" {
			p.PlayerType, p.HostIndex = "guest_of_member", new(int)
		}
		if p.TBA {
			p.Name = "Guest"
		}
		req.Players = append(req.Players, p)
	}
	b, err := d.Golf.CreateBooking(ctx, tx, property, req, true)
	if err != nil {
		return false, err
	}
	if err := golf.SetLegacyRef(ctx, tx, b.ID, r["legacy_ref"]); err != nil {
		return false, err
	}
	if v := strings.ReplaceAll(r["paid_amount"], ",", ""); v != "" && b.FolioID != nil {
		paid, _ := decimal.NewFromString(v)
		if paid.IsPositive() {
			if _, err := d.Billing.TakePayment(ctx, tx, billing.PaymentInput{FolioID: b.FolioID, MethodType: "bank_transfer", Channel: "venue",
				Purpose: "settlement", Amount: paid, Reference: "RHAPSODY-" + r["legacy_ref"], PayerName: r["contact_name"],
				Description: "Paid in Rhapsody before cutover"}); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}
