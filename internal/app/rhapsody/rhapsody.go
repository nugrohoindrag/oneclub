// Package rhapsody migrates Modern Golf data from Rhapsody Golf into OneClub
// (PRD P1 EP-18). The export format of Rhapsody is agreed with the club
// (docs/migration/rhapsody-mapping.md); every file is a UTF-8 CSV with a
// header row. The tool runs in four repeatable steps:
//
//	stage      load the CSV files into the staging_rhapsody schema
//	validate   per-entity rules with a row-level error report (FR-MIG-10)
//	load       idempotent upsert into OneClub by legacy reference
//	reconcile  compare OneClub totals with the Rhapsody control totals (FR-MIG-11)
//
// The package lives in the composition root because a migration touches
// every module; writes go through the owning module's public API or the
// generic import engine (P0 Master Data Import).
package rhapsody

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
)

// Schema holds the staged files and the validation issues.
const Schema = "staging_rhapsody"

// Entity is one export file of the Rhapsody data contract.
type Entity struct {
	Name     string   // file name without .csv and staging table name
	Columns  []string // required header (order free, case-insensitive)
	Optional []string
	Key      string // unique key column
}

// Entities is the data contract, in load order.
var Entities = []Entity{
	{Name: "customers", Key: "legacy_id", Columns: []string{"legacy_id", "name"},
		Optional: []string{"gender", "birth_date", "email", "phone", "address", "city", "id_number", "handicap_index"}},
	{Name: "corporate_accounts", Key: "legacy_id", Columns: []string{"legacy_id", "name"},
		Optional: []string{"npwp", "contact_name", "email", "phone", "address"}},
	{Name: "corporate_nominees", Columns: []string{"corporate_legacy_id", "customer_legacy_id"}, Optional: []string{"title"}},
	{Name: "members", Key: "member_no", Columns: []string{"member_no", "customer_legacy_id", "type_code", "starts_on", "status"},
		Optional: []string{"ends_on", "card_number", "principal_member_no", "relationship", "corporate_legacy_id"}},
	{Name: "opening_balances", Key: "member_no", Columns: []string{"member_no", "balance", "as_of"}},
	{Name: "caddies", Key: "code", Columns: []string{"code", "name"}, Optional: []string{"gender", "phone", "partnership_status"}},
	{Name: "golf_carts", Key: "code", Columns: []string{"code", "name"}, Optional: []string{"cart_type", "capacity"}},
	{Name: "lockers", Key: "code", Columns: []string{"code", "name", "area"}, Optional: []string{"zone"}},
	{Name: "future_bookings", Key: "legacy_ref", Columns: []string{"legacy_ref", "course_code", "play_date", "tee_time", "contact_name", "players"},
		Optional: []string{"start_tee", "member_no", "contact_phone", "player_names", "paid_amount", "notes"}},
}

func entityByName(n string) (Entity, bool) {
	for _, e := range Entities {
		if e.Name == n {
			return e, true
		}
	}
	return Entity{}, false
}

var identRe = regexp.MustCompile(`^[a-z_]+$`)

// Stage loads every <entity>.csv found in dir into staging_rhapsody.<entity>
// (replacing a previous run) and returns the row counts.
func Stage(ctx context.Context, db *dbtx.DB, dir string) (map[string]int, error) {
	ctx = dbtx.System(ctx)
	counts := map[string]int{}
	err := db.WithTx(ctx, func(tx pgx.Tx) error {
		for _, e := range Entities {
			path := filepath.Join(dir, e.Name+".csv")
			f, err := os.Open(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			n, err := stageFile(ctx, tx, e, f)
			f.Close()
			if err != nil {
				return fmt.Errorf("%s: %w", e.Name, err)
			}
			counts[e.Name] = n
		}
		return nil
	})
	return counts, err
}

func stageFile(ctx context.Context, tx pgx.Tx, e Entity, r io.Reader) (int, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return 0, fmt.Errorf("empty file or not a CSV: %w", err)
	}
	allowed := map[string]bool{}
	for _, c := range append(append([]string{}, e.Columns...), e.Optional...) {
		allowed[c] = true
	}
	cols := make([]string, len(header))
	seen := map[string]bool{}
	for i, h := range header {
		h = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, string(rune(0xFEFF)))))
		if !allowed[h] || !identRe.MatchString(h) {
			return 0, fmt.Errorf("unknown column %q (see docs/migration/rhapsody-mapping.md)", h)
		}
		cols[i], seen[h] = h, true
	}
	for _, c := range e.Columns {
		if !seen[c] {
			return 0, fmt.Errorf("missing required column %q", c)
		}
	}
	all := append(append([]string{}, e.Columns...), e.Optional...)
	var defs []string
	for _, c := range all {
		defs = append(defs, c+" text")
	}
	if _, err := tx.Exec(ctx, `DROP TABLE IF EXISTS `+Schema+`.`+e.Name); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE `+Schema+`.`+e.Name+` (row_no int PRIMARY KEY, `+strings.Join(defs, ", ")+`)`); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM `+Schema+`.issues WHERE entity = $1`, e.Name); err != nil {
		return 0, err
	}
	var rows [][]any
	line := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			return 0, fmt.Errorf("line %d: %w", line, err)
		}
		vals := make([]any, len(all)+1)
		vals[0] = line
		for i := range all {
			vals[i+1] = nil
		}
		for i, v := range rec {
			if i >= len(cols) {
				break
			}
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			for j, c := range all {
				if c == cols[i] {
					vals[j+1] = v
				}
			}
		}
		rows = append(rows, vals)
	}
	_, err = tx.CopyFrom(ctx, pgx.Identifier{Schema, e.Name}, append([]string{"row_no"}, all...), pgx.CopyFromRows(rows))
	return len(rows), err
}

// parseDate accepts ISO dates and the dd/mm/yyyy format of Rhapsody reports.
func parseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02", "02/01/2006", "2/1/2006", "02-01-2006", "2006/01/02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid date %q", s)
}

// staged reads all rows of an entity as maps (row_no included).
func staged(ctx context.Context, q dbtx.Querier, entity string) ([]map[string]string, error) {
	var exists bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = $1 AND table_name = $2)`, Schema, entity).
		Scan(&exists); err != nil || !exists {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT * FROM `+Schema+`.`+entity+` ORDER BY row_no`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	fields := rows.FieldDescriptions()
	var out []map[string]string
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		m := map[string]string{}
		for i, f := range fields {
			if vals[i] != nil {
				m[f.Name] = fmt.Sprint(vals[i])
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
