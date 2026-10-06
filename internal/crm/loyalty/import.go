package loyalty

// Loyalty opening balances (PRD P3 EP-25 migration wave 3): accounts and
// point balances carried over from the previous system. Preview validates
// every row; commit is idempotent per customer (a re-import adds nothing).

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/audit"
)

// LoyaltyImportInput is an opening balance file.
type LoyaltyImportInput struct {
	Mode     string `json:"mode" enum:"preview,commit" doc:"preview validates without saving"`
	Filename string `json:"filename,omitempty"`
	CSV      string `json:"csv" doc:"Header: customerCode,points[,expiresOn][,tierCode][,reference] — one lot per customer and reference"`
}

// LoyaltyImportRowError is a rejected row.
type LoyaltyImportRowError struct {
	Row     int    `json:"row" doc:"1-based line (header = 1)"`
	Field   string `json:"field,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// LoyaltyImportResult reports an import.
type LoyaltyImportResult struct {
	Mode      string                  `json:"mode"`
	TotalRows int                     `json:"totalRows"`
	Imported  int                     `json:"imported"`
	Skipped   int                     `json:"skipped" doc:"Already imported"`
	Points    int64                   `json:"points"`
	Errors    []LoyaltyImportRowError `json:"errors"`
}

// utf8BOM is stripped from the start of the file (Excel CSV exports).
var utf8BOM = string(rune(0xFEFF))

type importRow struct {
	line     int
	customer uuid.UUID
	code     string
	points   int64
	expires  *time.Time
	tier     *uuid.UUID
	ref      string
}

// ImportOpeningBalances validates and (in commit mode) posts the balances.
func (m *Module) ImportOpeningBalances(ctx context.Context, tx pgx.Tx, property uuid.UUID, in LoyaltyImportInput) (LoyaltyImportResult, error) {
	if in.Mode == "" {
		in.Mode = "preview"
	}
	if in.Mode != "preview" && in.Mode != "commit" {
		return LoyaltyImportResult{}, errs.Validation("invalid_mode", "mode must be preview or commit", errs.Field("mode", "invalid", "preview or commit"))
	}
	res := LoyaltyImportResult{Mode: in.Mode, Errors: []LoyaltyImportRowError{}}
	rd := csv.NewReader(bytes.NewReader([]byte(strings.TrimPrefix(in.CSV, utf8BOM))))
	rd.FieldsPerRecord = -1
	header, err := rd.Read()
	if err != nil {
		return res, errs.Validation("invalid_csv", "the file needs a header row", errs.Field("csv", "invalid", "customerCode,points,…"))
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, need := range []string{"customercode", "points"} {
		if _, ok := col[need]; !ok {
			return res, errs.Validation("invalid_csv", "missing column "+need, errs.Field("csv", "missing_column", need))
		}
	}
	get := func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	var rows []importRow
	line := 1
	for {
		rec, err := rd.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line++
		if err != nil {
			res.Errors = append(res.Errors, LoyaltyImportRowError{Row: line, Code: "invalid_csv", Message: err.Error()})
			continue
		}
		res.TotalRows++
		row := importRow{line: line, code: strings.ToUpper(get(rec, "customercode")), ref: get(rec, "reference")}
		bad := func(field, code, msg string) {
			res.Errors = append(res.Errors, LoyaltyImportRowError{Row: line, Field: field, Code: code, Message: msg})
		}
		if row.code == "" {
			bad("customerCode", "required", "customer code is required")
			continue
		}
		if err := tx.QueryRow(ctx, `SELECT id FROM crm.customers WHERE property_id = $1 AND upper(code) = $2 AND status <> 'merged'`, property, row.code).
			Scan(&row.customer); err != nil {
			if dbtx.IsNoRows(err) {
				bad("customerCode", "not_found", "customer "+row.code+" not found")
				continue
			}
			return res, err
		}
		n, err := strconv.ParseInt(get(rec, "points"), 10, 64)
		if err != nil || n <= 0 {
			bad("points", "invalid_points", "points must be a whole number above 0")
			continue
		}
		row.points = n
		if s := get(rec, "expireson"); s != "" {
			t, err := time.Parse("2006-01-02", s)
			if err != nil {
				bad("expiresOn", "invalid_date", "expiresOn must be YYYY-MM-DD")
				continue
			}
			row.expires = &t
		}
		if s := get(rec, "tiercode"); s != "" {
			var t uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT id FROM crm.loyalty_tiers WHERE property_id = $1 AND upper(code) = upper($2) AND archived_at IS NULL`,
				property, s).Scan(&t); err != nil {
				if dbtx.IsNoRows(err) {
					bad("tierCode", "not_found", "tier "+s+" not found")
					continue
				}
				return res, err
			}
			row.tier = &t
		}
		rows = append(rows, row)
	}
	if in.Mode == "preview" || len(res.Errors) > 0 {
		for _, r := range rows {
			res.Points += r.points
		}
		if in.Mode == "commit" {
			res.Mode = "preview" // nothing is saved while a row is rejected
		}
		return res, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "import_preview", EntityType: "crm.loyalty_opening_balance",
			EntityID: property.String(), EntityLabel: in.Filename, PropertyID: &property, After: res})
	}
	for _, r := range rows {
		a, _, err := m.Enrol(ctx, tx, property, r.customer, "migration", r.tier)
		if err != nil {
			return res, err
		}
		if r.tier != nil {
			if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_accounts SET tier_id = $2 WHERE id = $1`, a.ID, *r.tier); err != nil {
				return res, err
			}
		}
		// One opening lot per customer and reference (or row): a re-import adds nothing.
		key := "opening:" + r.code + ":" + r.ref
		if r.ref == "" {
			key = fmt.Sprintf("opening:%s:row%d", r.code, r.line)
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.loyalty_ledger WHERE account_id = $1 AND idempotency_key = $2)`, a.ID, key).Scan(&exists); err != nil {
			return res, err
		}
		if exists {
			res.Skipped++
			continue
		}
		desc := "Opening balance (migration)"
		if r.ref != "" {
			desc += " " + r.ref
		}
		if _, err := m.PostAdjustment(ctx, tx, a.ID, r.points, "migration", nil, r.ref, key, desc, r.expires); err != nil {
			return res, err
		}
		res.Imported++
		res.Points += r.points
	}
	return res, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionImport, EntityType: "crm.loyalty_opening_balance",
		EntityID: property.String(), EntityLabel: in.Filename, PropertyID: &property, After: res})
}
