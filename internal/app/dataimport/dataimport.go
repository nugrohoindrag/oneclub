// Package dataimport runs the migration importers of PRD P3 / P4 §11 from
// the command line (`oneclub import <scope>`): the same importer functions
// the Back Office / API use (preview + commit, idempotent re-run,
// reconciliation), executed as the system actor against the instance
// database of the process configuration, for one property.
//
//	sales               leads (and their opportunities)           crm/sales ImportLeads
//	banquet             events (future events with the DP)        banquet ImportEvents + Reconcile
//	tournament-history  results (history, Hall of Fame)           golf/tournament ImportHistory
//	corporate-ar        invoices (open corporate AR)              billing ImportCorporateAR
//	inventory           categories | warehouses | items           Master Data Import (resource engine)
//	                    opening-stock                             inventory ImportOpeningStock
//	procurement         suppliers | supplier-items | open-purchase-orders   procurement Import
//	accounting          accounts (chart of accounts)              Master Data Import (resource engine)
//	                    opening-balances (incl. AR / AP open items)         accounting ImportOpeningBalances + ReconcileOpening
//
// A run without Commit is a dry run: the importer validates and previews
// inside a transaction that is always rolled back. A committed re-run of
// the same file changes nothing: the importers skip rows already imported
// (same reference / code); opening stock rows already posted and an
// opening balance batch already posted are reported as existing, a draft
// batch of the same description is replaced.
package dataimport

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/accounting"
	"oneclub/internal/app"
	"oneclub/internal/banquet"
	"oneclub/internal/billing"
	"oneclub/internal/crm/sales"
	"oneclub/internal/golf/tournament"
	"oneclub/internal/inventory"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/audit"
	"oneclub/internal/procurement"
)

// Scope is a CLI import scope and its entities (the first is the default).
type Scope struct {
	Name     string
	Entities []string
	Help     string
}

// Scopes are the import scopes of `oneclub import`.
var Scopes = []Scope{
	{"sales", []string{"leads"}, "active leads & opportunities of the sales spreadsheet (FR-MIG-P3-04)"},
	{"banquet", []string{"events"}, "future banquets / events with the DP received (FR-MIG-P3-01/05)"},
	{"tournament-history", []string{"results"}, "tournament history and Hall of Fame champions (FR-MIG-P3-03)"},
	{"corporate-ar", []string{"invoices"}, "open corporate AR invoices with due dates (FR-MIG-P3-02/05)"},
	{"inventory", []string{"items", "categories", "warehouses", "opening-stock"}, "item master, locations and opening stock with value (FR-MIG-P4-03)"},
	{"procurement", []string{"suppliers", "supplier-items", "open-purchase-orders"}, "suppliers, supplier items and open POs (FR-MIG-P4-04)"},
	{"accounting", []string{"accounts", "opening-balances"}, "chart of accounts and opening balances incl. AR / AP open items (FR-MIG-P4-01/02)"},
}

// Request is one import run.
type Request struct {
	Scope    string
	Entity   string // default: the first entity of the scope
	Property uuid.UUID
	CSV      string
	Filename string
	Commit   bool // false = dry run (always rolled back)
	// ExpectedTotal is the control total of the open corporate AR (corporate-ar).
	ExpectedTotal string
	// BusinessDate is the cut-over date of the opening stock (inventory opening-stock).
	BusinessDate string
	// BalanceDate and Description of the opening balance batch (accounting opening-balances).
	BalanceDate string
	Description string
}

// Issue is a rejected row (row 1 = header).
type Issue struct {
	Row     int    `json:"row"`
	Field   string `json:"field,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

// Report is the outcome of a run with its reconciliation figures.
type Report struct {
	Scope          string         `json:"scope"`
	Entity         string         `json:"entity"`
	Mode           string         `json:"mode"`
	Rows           int            `json:"rows"`
	Created        int            `json:"created"`
	Updated        int            `json:"updated"`
	Existing       int            `json:"existing"`
	Failed         int            `json:"failed"`
	Issues         []Issue        `json:"issues"`
	Reconciliation map[string]any `json:"reconciliation"`
	Result         any            `json:"result"`
}

// errDryRun rolls the transaction of a dry run back.
var errDryRun = errors.New("dry run")

// Resolve returns the scope and entity of a request (entity defaulted).
func Resolve(scope, entity string) (Scope, string, error) {
	for _, s := range Scopes {
		if s.Name != scope {
			continue
		}
		if entity == "" {
			return s, s.Entities[0], nil
		}
		for _, e := range s.Entities {
			if e == entity {
				return s, e, nil
			}
		}
		return s, "", fmt.Errorf("unknown entity %q of scope %s (%s)", entity, scope, strings.Join(s.Entities, ", "))
	}
	names := make([]string, 0, len(Scopes))
	for _, s := range Scopes {
		names = append(names, s.Name)
	}
	return Scope{}, "", fmt.Errorf("unknown scope %q (%s)", scope, strings.Join(names, ", "))
}

// PropertyByCode resolves the target property.
func PropertyByCode(ctx context.Context, db *dbtx.DB, code string) (uuid.UUID, error) {
	var p uuid.UUID
	// Inside a transaction: the system scope is applied there (Row Level Security).
	err := db.WithReadTx(dbtx.System(ctx), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id FROM platform.properties WHERE upper(code) = upper($1)`, code).Scan(&p)
	})
	if err != nil {
		return p, fmt.Errorf("property %s not found: %w", code, err)
	}
	return p, nil
}

// Run executes the importer of the request as the system actor.
func Run(ctx context.Context, a *app.App, r Request) (Report, error) {
	_, entity, err := Resolve(r.Scope, r.Entity)
	if err != nil {
		return Report{}, err
	}
	r.Entity = entity
	if strings.TrimSpace(r.CSV) == "" {
		return Report{}, errors.New("the file is empty")
	}
	mode := "preview"
	if r.Commit {
		mode = "commit"
	}
	rep := Report{Scope: r.Scope, Entity: entity, Mode: mode, Issues: []Issue{}, Reconciliation: map[string]any{}}
	ctx = reqctx.WithProperty(dbtx.System(ctx), r.Property)
	err = a.DB.WithTx(ctx, func(tx pgx.Tx) error {
		if err := run(ctx, a, tx, r, &rep); err != nil {
			return err
		}
		if !r.Commit {
			return errDryRun
		}
		return nil
	})
	if errors.Is(err, errDryRun) {
		err = nil
	}
	if err != nil {
		if de, ok := errs.As(err); ok {
			msg := de.Message
			for _, f := range de.Fields {
				msg += "; " + f.Field + ": " + f.Message
			}
			return rep, errors.New(msg)
		}
		return rep, err
	}
	return rep, nil
}

func run(ctx context.Context, a *app.App, tx pgx.Tx, r Request, rep *Report) error {
	p := r.Property
	switch r.Scope + "/" + r.Entity {
	case "sales/leads":
		res, err := a.Sales.Module.ImportLeads(ctx, tx, p, sales.LeadImportInput{Mode: rep.Mode, CSV: r.CSV})
		if err != nil {
			return err
		}
		rep.Rows, rep.Created, rep.Existing, rep.Failed, rep.Result = res.TotalRows, res.Created, res.Existing, res.Failed, res
		for _, e := range res.Errors {
			rep.Issues = append(rep.Issues, Issue{Row: e.Row, Field: e.Field, Code: e.Code, Message: e.Message})
		}
		rep.Reconciliation["opportunities"] = res.Opportunities
		return nil
	case "banquet/events":
		rows, issues, err := banquetRows(r.CSV)
		if err != nil {
			return err
		}
		rep.Issues = append(rep.Issues, issues...)
		rep.Failed += len(issues)
		rep.Rows = len(rows) + len(issues)
		if len(rows) > 0 {
			res, err := a.Banquet.Module.ImportEvents(ctx, tx, p, banquet.LegacyEventsImport{DryRun: !r.Commit, Rows: rows})
			if err != nil {
				return err
			}
			rep.Created, rep.Existing, rep.Result = res.Imported, res.Existing, res
			rep.Failed += res.Errors
			for _, x := range res.Rows {
				if x.Status == "error" {
					rep.Issues = append(rep.Issues, Issue{Row: x.Row + 1, Code: "invalid", Message: x.LegacyRef + ": " + x.Message})
				}
			}
		}
		rec, err := banquet.Reconcile(ctx, tx, p)
		if err != nil {
			return err
		}
		return into(rep.Reconciliation, rec)
	case "tournament-history/results":
		res, err := a.Tournament.Module.ImportHistory(ctx, tx, p, tournament.TournamentHistoryImportInput{Mode: rep.Mode, CSV: r.CSV})
		if err != nil {
			return err
		}
		rep.Rows, rep.Failed, rep.Result = res.TotalRows, res.Failed, res
		rep.Created = res.Tournaments
		rep.Reconciliation["tournaments"], rep.Reconciliation["results"], rep.Reconciliation["champions"] = res.Tournaments, res.Results, res.Champions
		for _, e := range res.Errors {
			rep.Issues = append(rep.Issues, Issue{Row: e.Row, Field: e.Field, Code: e.Code, Message: e.Message})
		}
		return nil
	case "corporate-ar/invoices":
		res, err := a.Billing.ImportCorporateAR(ctx, tx, p, billing.CorporateARImportInput{Mode: rep.Mode, CSV: r.CSV, ExpectedTotal: r.ExpectedTotal})
		if err != nil {
			return err
		}
		rep.Rows, rep.Created, rep.Existing, rep.Failed, rep.Result = res.TotalRows, res.Imported, res.Existing, res.Failed, res
		for _, e := range res.Errors {
			rep.Issues = append(rep.Issues, Issue{Row: e.Row, Field: e.Field, Code: e.Code, Message: e.Message})
		}
		rep.Reconciliation["fileTotal"], rep.Reconciliation["migratedOpen"] = res.FileTotal, res.MigratedOpen
		rep.Reconciliation["difference"], rep.Reconciliation["reconciled"] = res.Difference, res.Reconciled
		if res.ExpectedTotal != nil {
			rep.Reconciliation["expectedTotal"] = *res.ExpectedTotal
		}
		return nil
	case "inventory/items", "inventory/categories", "inventory/warehouses", "accounting/accounts":
		return masterData(ctx, a, tx, r, rep)
	case "inventory/opening-stock":
		return openingStock(ctx, a, tx, r, rep)
	case "procurement/suppliers", "procurement/supplier-items", "procurement/open-purchase-orders":
		res, err := a.Purchasing.Procurement.Import(ctx, tx, p, procurement.ProcurementImportInput{Entity: strings.ReplaceAll(r.Entity, "-", "_"),
			Mode: rep.Mode, Filename: r.Filename, CSV: r.CSV})
		if err != nil {
			return err
		}
		rep.Rows, rep.Created, rep.Updated, rep.Existing, rep.Result = res.TotalRows, res.Created, res.Updated, res.Skipped, res
		for _, e := range res.Errors {
			rep.Issues = append(rep.Issues, Issue{Row: e.Row, Field: e.Field, Code: e.Code, Message: e.Message})
		}
		rep.Failed = failedRows(rep.Issues)
		return into(rep.Reconciliation, res.Reconciliation)
	case "accounting/opening-balances":
		return openingBalances(ctx, a, tx, r, rep)
	}
	return fmt.Errorf("scope %s / %s is not wired", r.Scope, r.Entity)
}

// masterData runs the Master Data Import (FR-MD-06) of a resource: rows are
// upserted by their code, so a re-run updates instead of duplicating.
func masterData(ctx context.Context, a *app.App, tx pgx.Tx, r Request, rep *Report) error {
	key := map[string]string{"inventory/items": "inventory.item", "inventory/categories": "inventory.item_category",
		"inventory/warehouses": "inventory.warehouse", "accounting/accounts": "accounting.account"}[r.Scope+"/"+r.Entity]
	d, ok := a.Engine.Def(key)
	if !ok || d.CodeField == "" {
		return fmt.Errorf("%s cannot be imported", key)
	}
	res, err := a.Engine.Import(ctx, tx, d, strings.NewReader(strings.TrimPrefix(r.CSV, "\uFEFF")))
	if err != nil {
		return err
	}
	rep.Rows, rep.Created, rep.Updated, rep.Failed, rep.Result = res.TotalRows, res.InsertedRows, res.UpdatedRows, res.FailedRows, res
	for _, e := range res.Errors {
		rep.Issues = append(rep.Issues, Issue{Row: e.Row, Field: e.Field, Code: e.Code, Message: e.Message})
	}
	rep.Reconciliation["status"] = res.Status
	if !r.Commit {
		return nil
	}
	// Recorded like a Back Office import (Imports history + audit).
	var pid *uuid.UUID
	if d.PropertyScoped {
		pid = &r.Property
	}
	filename := r.Filename
	if filename == "" {
		filename = key + ".csv"
	}
	iid := id.New()
	errsJSON, _ := json.Marshal(res.Errors)
	if _, err := tx.Exec(ctx, `INSERT INTO platform.imports (id, entity, property_id, filename, mode, status, total_rows, inserted_rows,
		updated_rows, failed_rows, errors, created_by) VALUES ($1, $2, $3, $4, 'commit', $5, $6, $7, $8, $9, $10, NULL)`,
		iid, d.Key, pid, filename, res.Status, res.TotalRows, res.InsertedRows, res.UpdatedRows, res.FailedRows, errsJSON); err != nil {
		return err
	}
	rep.Reconciliation["importId"] = iid.String()
	return audit.Record(ctx, tx, audit.Entry{Module: d.Module, Action: audit.ActionImport, EntityType: "platform.import", EntityID: iid.String(),
		EntityLabel: d.Plural + " · " + filename + " (oneclub import)", PropertyID: pid,
		After: map[string]any{"total": res.TotalRows, "inserted": res.InsertedRows, "updated": res.UpdatedRows, "failed": res.FailedRows}})
}

// openingStock posts the opening stock (Dr inventory / Cr opening balance
// equity). Rows whose item already has opening stock in the warehouse are
// reported as existing and left out, so a re-run posts nothing twice.
func openingStock(ctx context.Context, a *app.App, tx pgx.Tx, r Request, rep *Report) error {
	in := inventory.OpeningStockInput{Mode: "preview", Filename: r.Filename, BusinessDate: r.BusinessDate, CSV: r.CSV}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	pre, err := a.Stock.Service.ImportOpeningStock(ctx, sp, r.Property, in)
	_ = sp.Rollback(ctx)
	if err != nil {
		return err
	}
	skip := map[int]bool{}
	for _, e := range pre.Errors {
		if e.Code == "already_imported" {
			skip[e.Row] = true
		}
	}
	rep.Rows, rep.Existing = pre.TotalRows, len(skip)
	csvText := r.CSV
	if len(skip) > 0 {
		if csvText, err = dropRows(r.CSV, skip); err != nil {
			return err
		}
	}
	res := pre
	if len(skip) < pre.TotalRows || len(skip) == 0 {
		in.Mode, in.CSV = rep.Mode, csvText
		if res, err = a.Stock.Service.ImportOpeningStock(ctx, tx, r.Property, in); err != nil {
			return err
		}
	}
	rep.Result = res
	for _, e := range res.Errors {
		if e.Code != "already_imported" {
			rep.Issues = append(rep.Issues, Issue{Row: e.Row, Field: e.Field, Code: e.Code, Message: e.Message})
		}
	}
	rep.Failed = failedRows(rep.Issues)
	if len(rep.Issues) == 0 {
		rep.Created = rep.Rows - rep.Existing
	}
	rep.Reconciliation["totalValue"], rep.Reconciliation["status"] = res.TotalValue, res.Status
	for _, w := range res.Warehouses {
		rep.Reconciliation["value."+w.WarehouseCode] = w.Value
	}
	return nil
}

// openingBalances drafts the opening balance batch (posting stays with the
// Finance Manager in the Back Office, FR-TRS-01). A draft batch of the same
// description is replaced; a posted one makes the run report it as existing.
func openingBalances(ctx context.Context, a *app.App, tx pgx.Tx, r Request, rep *Report) error {
	desc := strings.TrimSpace(r.Description)
	if desc == "" {
		desc = "Opening balances (oneclub import)"
	}
	var prior []struct {
		id     uuid.UUID
		status string
		number string
	}
	rows, err := tx.Query(ctx, `SELECT id, status, number FROM accounting.opening_balances WHERE property_id = $1 AND description = $2 ORDER BY created_at`,
		r.Property, desc)
	if err != nil {
		return err
	}
	for rows.Next() {
		var x struct {
			id     uuid.UUID
			status string
			number string
		}
		if err := rows.Scan(&x.id, &x.status, &x.number); err != nil {
			rows.Close()
			return err
		}
		prior = append(prior, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, x := range prior {
		if x.status == "posted" {
			rep.Existing = 1
			rep.Reconciliation["batch"], rep.Reconciliation["batchStatus"] = x.number, "posted"
			rec, err := accounting.ReconcileOpening(ctx, tx, r.Property, x.id)
			if err != nil {
				return err
			}
			return into(rep.Reconciliation, rec)
		}
	}
	res, err := a.Ledger.Module.ImportOpeningBalances(ctx, tx, r.Property, accounting.OpeningImportInput{Content: r.CSV, Description: desc,
		BalanceDate: r.BalanceDate, Preview: true})
	if err != nil {
		return err
	}
	rep.Rows, rep.Result = res.Rows+len(res.Issues), res
	for _, i := range res.Issues {
		rep.Issues = append(rep.Issues, Issue{Row: i.Row, Message: i.Message})
	}
	rep.Failed = failedRows(rep.Issues)
	rep.Reconciliation["totalDebit"], rep.Reconciliation["totalCredit"], rep.Reconciliation["balanced"] = res.TotalDebit, res.TotalCredit, res.Balanced
	if !r.Commit || len(res.Issues) > 0 {
		return nil
	}
	for _, x := range prior {
		if err := a.Ledger.Module.DeleteOpeningBalance(ctx, tx, r.Property, x.id); err != nil {
			return err
		}
		rep.Updated = 1
	}
	res, err = a.Ledger.Module.ImportOpeningBalances(ctx, tx, r.Property, accounting.OpeningImportInput{Content: r.CSV, Description: desc,
		BalanceDate: r.BalanceDate})
	if err != nil {
		return err
	}
	rep.Result = res
	if res.Batch == nil {
		return errors.New("no opening balance batch drafted")
	}
	if rep.Updated == 0 {
		rep.Created = 1
	}
	rep.Reconciliation["batch"], rep.Reconciliation["batchStatus"] = res.Batch.Number, res.Batch.Status
	rec, err := accounting.ReconcileOpening(ctx, tx, r.Property, res.Batch.ID)
	if err != nil {
		return err
	}
	return into(rep.Reconciliation, rec)
}

// banquetRows reads the banquet book CSV into rows (header: the JSON names
// of banquet.LegacyEventRow, case and underscores ignored).
func banquetRows(text string) ([]banquet.LegacyEventRow, []Issue, error) {
	recs, err := readCSV(text)
	if err != nil {
		return nil, nil, err
	}
	fields := map[string]reflect.StructField{}
	t := reflect.TypeOf(banquet.LegacyEventRow{})
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		fields[norm(name)] = f
	}
	header := recs[0]
	var issues []Issue
	cols := make([]reflect.StructField, len(header))
	known := make([]bool, len(header))
	for i, h := range header {
		f, ok := fields[norm(h)]
		if !ok && strings.TrimSpace(h) != "" {
			return nil, nil, fmt.Errorf("unknown column %s", h)
		}
		cols[i], known[i] = f, ok
	}
	var out []banquet.LegacyEventRow
	for n, rec := range recs[1:] {
		row := banquet.LegacyEventRow{}
		v := reflect.ValueOf(&row).Elem()
		bad := ""
		for i, cell := range rec {
			if i >= len(cols) || !known[i] {
				continue
			}
			cell = strings.TrimSpace(cell)
			fv := v.FieldByIndex(cols[i].Index)
			switch fv.Kind() {
			case reflect.Int:
				if cell == "" {
					continue
				}
				x, err := strconv.Atoi(cell)
				if err != nil {
					bad = header[i] + " must be a whole number"
					continue
				}
				fv.SetInt(int64(x))
			case reflect.String:
				fv.SetString(cell)
			}
		}
		if bad != "" {
			issues = append(issues, Issue{Row: n + 2, Code: "invalid", Message: bad})
			continue
		}
		out = append(out, row)
	}
	return out, issues, nil
}

func norm(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(strings.TrimPrefix(s, "\uFEFF")), "_", ""))
}

func readCSV(text string) ([][]string, error) {
	cr := csv.NewReader(strings.NewReader(strings.TrimPrefix(text, "\uFEFF")))
	cr.FieldsPerRecord, cr.TrimLeadingSpace = -1, true
	recs, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("invalid CSV: %w", err)
	}
	if len(recs) == 0 {
		return nil, errors.New("the file needs a header row")
	}
	return recs, nil
}

// dropRows removes the given 1-based rows (header = 1) from a CSV.
func dropRows(text string, skip map[int]bool) (string, error) {
	recs, err := readCSV(text)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	for i, rec := range recs {
		if !skip[i+1] {
			_ = w.Write(rec)
		}
	}
	w.Flush()
	return b.String(), w.Error()
}

func failedRows(issues []Issue) int {
	rows := map[int]bool{}
	for _, i := range issues {
		rows[i.Row] = true
	}
	return len(rows)
}

// into copies the JSON fields of v into m.
func into(m map[string]any, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var x map[string]any
	if err := json.Unmarshal(raw, &x); err != nil {
		return err
	}
	for k, val := range x {
		m[k] = val
	}
	return nil
}

// Summary renders the report for the terminal.
func (r Report) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s (%s): %d rows · created %d · updated %d · existing %d · failed %d\n", r.Scope, r.Entity, r.Mode, r.Rows, r.Created,
		r.Updated, r.Existing, r.Failed)
	for _, i := range r.Issues {
		f := ""
		if i.Field != "" {
			f = " [" + i.Field + "]"
		}
		fmt.Fprintf(&b, "  row %d%s: %s\n", i.Row, f, i.Message)
	}
	keys := make([]string, 0, len(r.Reconciliation))
	for k := range r.Reconciliation {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		b.WriteString("reconciliation:\n")
	}
	for _, k := range keys {
		v := r.Reconciliation[k]
		switch v.(type) {
		case map[string]any, []any:
			raw, _ := json.Marshal(v)
			v = string(raw)
		}
		fmt.Fprintf(&b, "  %-24s %v\n", k, v)
	}
	if r.Mode == "preview" {
		b.WriteString("dry run: nothing was saved (run again with -commit)\n")
	}
	return b.String()
}

// Stamp is the timestamp of a report file name.
func Stamp(t time.Time) string { return t.UTC().Format("20060102T150405Z") }
