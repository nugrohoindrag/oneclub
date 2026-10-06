package procurement

// PRD P4 EP-29 FR-MIG-P4-04 (with FR-MIG-P4-06 / -07): migration of the
// suppliers (with bank account and contact), the supplier items & price
// lists and the OPEN purchase orders with the quantities already received
// before the cut-over, from CSV exports of the club's purchasing files
// (docs/runbooks/procurement-migration-p4.md).
//
// Every import runs as a dry run first (mode preview: the whole file is
// validated and applied inside a savepoint that is rolled back, so the
// reconciliation totals are the ones a commit would produce); commit
// writes it. Imports are idempotent: suppliers are matched on their code
// (updated), price list rows on supplier × item × UOM × valid-from
// (updated), purchase orders on their legacy number (skipped when present),
// so a file can be committed again after a partial failure. Received
// quantities of open orders are kept as opening received quantities: the
// stock comes from the opening stock import of inventory and the payable
// of goods received before the cut-over from the open AP import of
// accounting, so they are neither posted to stock nor invoiced again.

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/inventory"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// Import entities.
const (
	ImportSuppliers     = "suppliers"
	ImportSupplierItems = "supplier_items"
	ImportOpenOrders    = "open_purchase_orders"
)

// ProcurementImportInput is a migration file.
type ProcurementImportInput struct {
	Entity   string `json:"entity" enum:"suppliers,supplier_items,open_purchase_orders"`
	Mode     string `json:"mode" enum:"preview,commit" doc:"preview = dry run (nothing is written)"`
	Filename string `json:"filename,omitempty"`
	CSV      string `json:"csv" doc:"suppliers: code,name[,legalName,npwp,email,phone,address,categories,pkp,withholdingType,paymentTermDays,currency,leadTimeDays,contractSupplier,contactName,contactEmail,contactPhone,bankName,bankAccountNumber,bankAccountName] · supplier_items: supplierCode,itemCode,unitPrice[,uom,supplierItemCode,minOrderQuantity,leadTimeDays,validFrom,validTo,preferred,currency] · open_purchase_orders (one row per line): poNumber,supplierCode,orderDate,warehouseCode,itemCode,quantity,unitPrice[,uom,receivedQuantity,discountPercent,taxPercent,expectedDate,description,orderType,paymentTermDays,currency] (lists separated by |)"`
}

// ProcurementImportError is a row error of the file.
type ProcurementImportError struct {
	Row     int    `json:"row" doc:"1-based line (header = 1)"`
	Field   string `json:"field,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ProcurementImportReconciliation are the control totals to sign off
// against the source (FR-MIG-P4-06).
type ProcurementImportReconciliation struct {
	Suppliers        int    `json:"suppliers"`
	SupplierItems    int    `json:"supplierItems"`
	PurchaseOrders   int    `json:"purchaseOrders"`
	OrderLines       int    `json:"orderLines"`
	OrderedValue     string `json:"orderedValue" doc:"Σ quantity × net price of the imported open orders (excl. tax)"`
	ReceivedValue    string `json:"receivedValue" doc:"Σ received quantity × net price before the cut-over"`
	OutstandingValue string `json:"outstandingValue" doc:"Ordered − received: the Outstanding PO value after the import"`
}

// ProcurementImportResult is the outcome of an import.
type ProcurementImportResult struct {
	Entity         string                          `json:"entity"`
	Mode           string                          `json:"mode" enum:"preview,commit"`
	Status         string                          `json:"status" enum:"valid,completed,failed"`
	TotalRows      int                             `json:"totalRows"`
	Created        int                             `json:"created"`
	Updated        int                             `json:"updated"`
	Skipped        int                             `json:"skipped" doc:"Already imported (idempotent re-run)"`
	Reconciliation ProcurementImportReconciliation `json:"reconciliation"`
	Errors         []ProcurementImportError        `json:"errors"`
}

type csvFile struct {
	col  map[string]int
	rows [][]string
	line []int
}

func readCSV(raw string, required ...string) (csvFile, error) {
	f := csvFile{col: map[string]int{}}
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(raw, string(rune(0xFEFF)))))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	header, err := r.Read()
	if err != nil {
		return f, handle.Invalid("csv", "empty", "the file needs a header row")
	}
	for i, h := range header {
		f.col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, need := range required {
		if _, ok := f.col[strings.ToLower(need)]; !ok {
			return f, handle.Invalid("csv", "missing_column", "column "+need+" is required")
		}
	}
	line := 1
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			return f, handle.Invalid("csv", "invalid_csv", fmt.Sprintf("line %d: %v", line, err))
		}
		if strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		f.rows = append(f.rows, rec)
		f.line = append(f.line, line)
	}
	if len(f.rows) == 0 {
		return f, errs.Validation("empty_file", "the file has no rows")
	}
	return f, nil
}

func (f csvFile) get(rec []string, name string) string {
	if i, ok := f.col[strings.ToLower(name)]; ok && i < len(rec) {
		return strings.TrimSpace(rec[i])
	}
	return ""
}

func parseBool(v string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "n":
		return false, true
	case "1", "true", "yes", "y":
		return true, true
	}
	return false, false
}

func optInt(v string) (*int, bool) {
	if strings.TrimSpace(v) == "" {
		return nil, true
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return nil, false
	}
	return &n, true
}

func optDec(v string, def decimal.Decimal) (decimal.Decimal, bool) {
	if strings.TrimSpace(v) == "" {
		return def, true
	}
	d, err := decimal.NewFromString(strings.TrimSpace(v))
	if err != nil || d.IsNegative() {
		return d, false
	}
	return d, true
}

func optDateStr(v string) (*time.Time, bool) {
	if strings.TrimSpace(v) == "" {
		return nil, true
	}
	t, err := time.Parse("2006-01-02", strings.TrimSpace(v))
	if err != nil {
		return nil, false
	}
	return &t, true
}

func lookupID(ctx context.Context, tx pgx.Tx, sql string, args ...any) (*uuid.UUID, error) {
	var u uuid.UUID
	err := tx.QueryRow(ctx, sql, args...).Scan(&u)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// Import validates (preview, dry run) or writes (commit) a migration file.
func (m *Module) Import(ctx context.Context, tx pgx.Tx, property uuid.UUID, in ProcurementImportInput) (ProcurementImportResult, error) {
	res := ProcurementImportResult{Entity: in.Entity, Mode: in.Mode, Status: "valid", Errors: []ProcurementImportError{},
		Reconciliation: ProcurementImportReconciliation{OrderedValue: "0", ReceivedValue: "0", OutstandingValue: "0"}}
	if in.Mode != "preview" && in.Mode != "commit" {
		return res, handle.Invalid("mode", "invalid", "mode must be preview or commit")
	}
	var run func(context.Context, pgx.Tx, uuid.UUID, csvFile, *ProcurementImportResult) error
	var required []string
	switch in.Entity {
	case ImportSuppliers:
		run, required = m.importSuppliers, []string{"code", "name"}
	case ImportSupplierItems:
		run, required = m.importSupplierItems, []string{"supplierCode", "itemCode", "unitPrice"}
	case ImportOpenOrders:
		run, required = m.importOpenOrders, []string{"poNumber", "supplierCode", "orderDate", "warehouseCode", "itemCode", "quantity", "unitPrice"}
	default:
		return res, handle.Invalid("entity", "invalid", "suppliers, supplier_items or open_purchase_orders")
	}
	f, err := readCSV(in.CSV, required...)
	if err != nil {
		return res, err
	}
	res.TotalRows = len(f.rows)
	// The dry run applies the file in a savepoint and rolls it back.
	sp, err := tx.Begin(ctx)
	if err != nil {
		return res, err
	}
	if err := run(ctx, sp, property, f, &res); err != nil {
		_ = sp.Rollback(ctx)
		return res, err
	}
	if len(res.Errors) > 0 || in.Mode == "preview" {
		if err := sp.Rollback(ctx); err != nil {
			return res, err
		}
		if len(res.Errors) > 0 {
			res.Status = "failed"
		}
	} else {
		if err := sp.Commit(ctx); err != nil {
			return res, err
		}
		res.Status = "completed"
	}
	label := in.Entity
	if in.Filename != "" {
		label += " · " + in.Filename
	}
	return res, audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: "import_" + in.Mode, EntityType: "procurement.migration",
		EntityID: uuid.NewSHA1(property, []byte(in.Entity+"/"+in.Filename)).String(), EntityLabel: label, PropertyID: &property,
		Metadata: map[string]any{"status": res.Status, "rows": res.TotalRows, "created": res.Created, "updated": res.Updated, "skipped": res.Skipped,
			"errors": len(res.Errors), "reconciliation": res.Reconciliation}})
}

func (m *Module) importSuppliers(ctx context.Context, tx pgx.Tx, property uuid.UUID, f csvFile, res *ProcurementImportResult) error {
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return err
	}
	seen := map[string]int{}
	for i, rec := range f.rows {
		row := f.line[i]
		fail := func(field, code, msg string) {
			res.Errors = append(res.Errors, ProcurementImportError{Row: row, Field: field, Code: code, Message: msg})
		}
		code, name := strings.ToUpper(f.get(rec, "code")), f.get(rec, "name")
		if code == "" || name == "" {
			fail("code", "required", "code and name are required")
			continue
		}
		if prev, ok := seen[code]; ok {
			fail("code", "duplicate", fmt.Sprintf("supplier %s is already on row %d", code, prev))
			continue
		}
		seen[code] = row
		pkp, ok1 := parseBool(f.get(rec, "pkp"))
		contract, ok2 := parseBool(f.get(rec, "contractSupplier"))
		term, ok3 := optInt(f.get(rec, "paymentTermDays"))
		lead, ok4 := optInt(f.get(rec, "leadTimeDays"))
		if !ok1 || !ok2 {
			fail("pkp", "invalid", "pkp / contractSupplier must be true or false")
			continue
		}
		if !ok3 || !ok4 {
			fail("paymentTermDays", "invalid", "paymentTermDays / leadTimeDays must be whole numbers ≥ 0")
			continue
		}
		wh := strings.ToLower(f.get(rec, "withholdingType"))
		if wh == "" {
			wh = "none"
		}
		if wh != "none" && wh != "pph23" && wh != "pph4_2" {
			fail("withholdingType", "invalid", "none, pph23 or pph4_2")
			continue
		}
		cur := strings.ToUpper(f.get(rec, "currency"))
		if cur == "" {
			cur = cfg.DefaultCurrency
		}
		if !currencyRe.MatchString(cur) {
			fail("currency", "invalid", "ISO 4217 code, e.g. IDR")
			continue
		}
		if t := f.get(rec, "email"); t != "" && !strings.Contains(t, "@") {
			fail("email", "invalid", "invalid e-mail")
			continue
		}
		cats := []string{}
		for _, c := range strings.Split(f.get(rec, "categories"), "|") {
			if c = strings.TrimSpace(c); c != "" {
				cats = append(cats, c)
			}
		}
		termDays, leadDays := cfg.DefaultPaymentTermDays, 0
		if term != nil {
			termDays = *term
		}
		if lead != nil {
			leadDays = *lead
		}
		var sid uuid.UUID
		var inserted bool
		if err := tx.QueryRow(ctx, `INSERT INTO procurement.suppliers (id, property_id, code, name, legal_name, npwp, email, phone, address, categories, pkp,
			withholding_type, payment_term_days, currency, lead_time_days, contract_supplier, attributes)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,'{"migrated": true}')
			ON CONFLICT (property_id, code) DO UPDATE SET name = EXCLUDED.name, legal_name = coalesce(EXCLUDED.legal_name, suppliers.legal_name),
			npwp = coalesce(EXCLUDED.npwp, suppliers.npwp), email = coalesce(EXCLUDED.email, suppliers.email), phone = coalesce(EXCLUDED.phone, suppliers.phone),
			address = coalesce(EXCLUDED.address, suppliers.address), categories = CASE WHEN cardinality(EXCLUDED.categories) > 0 THEN EXCLUDED.categories
			ELSE suppliers.categories END, pkp = EXCLUDED.pkp, withholding_type = EXCLUDED.withholding_type, payment_term_days = EXCLUDED.payment_term_days,
			currency = EXCLUDED.currency, lead_time_days = EXCLUDED.lead_time_days, contract_supplier = EXCLUDED.contract_supplier
			RETURNING id, (xmax = 0)`, id.New(), property, code, name, nz(f.get(rec, "legalName")), nz(f.get(rec, "npwp")), nz(f.get(rec, "email")),
			nz(f.get(rec, "phone")), nz(f.get(rec, "address")), cats, pkp, wh, termDays, cur, leadDays, contract).Scan(&sid, &inserted); err != nil {
			return err
		}
		if inserted {
			res.Created++
		} else {
			res.Updated++
		}
		res.Reconciliation.Suppliers++
		if cn := f.get(rec, "contactName"); cn != "" {
			if _, err := tx.Exec(ctx, `INSERT INTO procurement.supplier_contacts (id, property_id, supplier_id, name, email, phone, is_primary)
				SELECT $1, $2, $3, $4, $5, $6, NOT EXISTS (SELECT 1 FROM procurement.supplier_contacts WHERE supplier_id = $3)
				WHERE NOT EXISTS (SELECT 1 FROM procurement.supplier_contacts WHERE supplier_id = $3 AND lower(name) = lower($4))`,
				id.New(), property, sid, cn, nz(f.get(rec, "contactEmail")), nz(f.get(rec, "contactPhone"))); err != nil {
				return err
			}
		}
		if acct := f.get(rec, "bankAccountNumber"); acct != "" {
			bank, holder := f.get(rec, "bankName"), f.get(rec, "bankAccountName")
			if bank == "" || holder == "" {
				fail("bankName", "required", "bankName and bankAccountName are required with bankAccountNumber")
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO procurement.supplier_bank_accounts (id, property_id, supplier_id, bank_name, account_number, account_name, currency,
				is_primary) SELECT $1, $2, $3, $4, $5, $6, $7, NOT EXISTS (SELECT 1 FROM procurement.supplier_bank_accounts WHERE supplier_id = $3)
				WHERE NOT EXISTS (SELECT 1 FROM procurement.supplier_bank_accounts WHERE supplier_id = $3 AND account_number = $5)`,
				id.New(), property, sid, bank, acct, holder, cur); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Module) importSupplierItems(ctx context.Context, tx pgx.Tx, property uuid.UUID, f csvFile, res *ProcurementImportResult) error {
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return err
	}
	for i, rec := range f.rows {
		row := f.line[i]
		fail := func(field, code, msg string) {
			res.Errors = append(res.Errors, ProcurementImportError{Row: row, Field: field, Code: code, Message: msg})
		}
		sc, ic := f.get(rec, "supplierCode"), f.get(rec, "itemCode")
		sid, err := lookupID(ctx, tx, `SELECT id FROM procurement.suppliers WHERE property_id = $1 AND upper(code) = upper($2) AND archived_at IS NULL`, property, sc)
		if err != nil {
			return err
		}
		if sid == nil {
			fail("supplierCode", "not_found", "supplier "+sc+" not found (import the suppliers first)")
			continue
		}
		iid, err := lookupID(ctx, tx, `SELECT id FROM inventory.items WHERE property_id = $1 AND upper(code) = upper($2) AND archived_at IS NULL`, property, ic)
		if err != nil {
			return err
		}
		if iid == nil {
			fail("itemCode", "not_found", "item "+ic+" not found (import the items first)")
			continue
		}
		it, err := inventory.ProcurementItemByID(ctx, tx, *iid)
		if err != nil {
			return err
		}
		uom := it.BaseUOMID
		if it.PurchaseUOMID != nil {
			uom = *it.PurchaseUOMID
		}
		if uc := f.get(rec, "uom"); uc != "" {
			u, err := lookupID(ctx, tx, `SELECT id FROM inventory.uoms WHERE property_id = $1 AND upper(code) = upper($2)`, property, uc)
			if err != nil {
				return err
			}
			if u == nil {
				fail("uom", "not_found", "UOM "+uc+" not found")
				continue
			}
			uom = *u
		}
		if _, err := inventory.ProcurementConvert(ctx, tx, iid, decimal.NewFromInt(1), uom, it.BaseUOMID); err != nil {
			fail("uom", "no_uom_conversion", "no conversion to the stock UOM of "+it.Code)
			continue
		}
		price, ok := optDec(f.get(rec, "unitPrice"), decimal.Zero)
		if !ok || f.get(rec, "unitPrice") == "" {
			fail("unitPrice", "invalid", "unitPrice must be a number ≥ 0")
			continue
		}
		minQty, ok1 := optDec(f.get(rec, "minOrderQuantity"), decimal.Zero)
		lead, ok2 := optInt(f.get(rec, "leadTimeDays"))
		from, ok3 := optDateStr(f.get(rec, "validFrom"))
		to, ok4 := optDateStr(f.get(rec, "validTo"))
		pref, ok5 := parseBool(f.get(rec, "preferred"))
		if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 {
			fail("", "invalid", "minOrderQuantity ≥ 0, leadTimeDays whole number, validFrom / validTo YYYY-MM-DD, preferred true / false")
			continue
		}
		if from != nil && to != nil && to.Before(*from) {
			fail("validTo", "invalid", "validTo is before validFrom")
			continue
		}
		cur := strings.ToUpper(f.get(rec, "currency"))
		if cur == "" {
			cur = cfg.DefaultCurrency
		}
		existing, err := lookupID(ctx, tx, `SELECT id FROM procurement.supplier_items WHERE supplier_id = $1 AND item_id = $2 AND uom_id = $3
			AND valid_from IS NOT DISTINCT FROM $4`, *sid, *iid, uom, from)
		if err != nil {
			return err
		}
		if existing != nil {
			if _, err := tx.Exec(ctx, `UPDATE procurement.supplier_items SET unit_price = $2::numeric, supplier_item_code = coalesce($3, supplier_item_code),
				min_order_quantity = $4::numeric, lead_time_days = coalesce($5, lead_time_days), valid_to = $6, preferred = $7, currency = $8, status = 'active'
				WHERE id = $1`, *existing, price.String(), nz(f.get(rec, "supplierItemCode")), minQty.String(), lead, to, pref, cur); err != nil {
				return err
			}
			res.Updated++
		} else {
			if _, err := tx.Exec(ctx, `INSERT INTO procurement.supplier_items (id, property_id, supplier_id, item_id, supplier_item_code, uom_id, unit_price,
				currency, min_order_quantity, lead_time_days, valid_from, valid_to, preferred) VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9::numeric,$10,$11,$12,$13)`,
				id.New(), property, *sid, *iid, nz(f.get(rec, "supplierItemCode")), uom, price.String(), cur, minQty.String(), lead, from, to, pref); err != nil {
				return err
			}
			res.Created++
		}
		res.Reconciliation.SupplierItems++
	}
	return nil
}

type importLine struct {
	row                       int
	item                      uuid.UUID
	uom                       uuid.UUID
	desc                      string
	qty, received, price      decimal.Decimal
	disc, tax                 decimal.Decimal
	base                      decimal.Decimal
	expected                  *time.Time
	supplierCode, wh, orderDt string
	orderType, cur            string
	term                      *int
}

func (m *Module) importOpenOrders(ctx context.Context, tx pgx.Tx, property uuid.UUID, f csvFile, res *ProcurementImportResult) error {
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return err
	}
	groups := map[string][]importLine{}
	var numbers []string
	for i, rec := range f.rows {
		row := f.line[i]
		fail := func(field, code, msg string) {
			res.Errors = append(res.Errors, ProcurementImportError{Row: row, Field: field, Code: code, Message: msg})
		}
		num := strings.ToUpper(f.get(rec, "poNumber"))
		if num == "" {
			fail("poNumber", "required", "poNumber is required")
			continue
		}
		if _, ok := optDateStr(f.get(rec, "orderDate")); !ok || f.get(rec, "orderDate") == "" {
			fail("orderDate", "invalid_date", "orderDate must be YYYY-MM-DD")
			continue
		}
		ic := f.get(rec, "itemCode")
		iid, err := lookupID(ctx, tx, `SELECT id FROM inventory.items WHERE property_id = $1 AND upper(code) = upper($2) AND archived_at IS NULL`, property, ic)
		if err != nil {
			return err
		}
		if iid == nil {
			fail("itemCode", "not_found", "item "+ic+" not found")
			continue
		}
		it, err := inventory.ProcurementItemByID(ctx, tx, *iid)
		if err != nil {
			return err
		}
		uom := it.BaseUOMID
		if it.PurchaseUOMID != nil {
			uom = *it.PurchaseUOMID
		}
		if uc := f.get(rec, "uom"); uc != "" {
			u, err := lookupID(ctx, tx, `SELECT id FROM inventory.uoms WHERE property_id = $1 AND upper(code) = upper($2)`, property, uc)
			if err != nil {
				return err
			}
			if u == nil {
				fail("uom", "not_found", "UOM "+uc+" not found")
				continue
			}
			uom = *u
		}
		qty, ok := optDec(f.get(rec, "quantity"), decimal.Zero)
		if !ok || !qty.IsPositive() {
			fail("quantity", "invalid", "quantity must be a positive number")
			continue
		}
		base, err := inventory.ProcurementConvert(ctx, tx, iid, qty, uom, it.BaseUOMID)
		if err != nil {
			fail("uom", "no_uom_conversion", "no conversion to the stock UOM of "+it.Code)
			continue
		}
		price, ok1 := optDec(f.get(rec, "unitPrice"), decimal.Zero)
		received, ok2 := optDec(f.get(rec, "receivedQuantity"), decimal.Zero)
		disc, ok3 := optDec(f.get(rec, "discountPercent"), decimal.Zero)
		tax, ok4 := optDec(f.get(rec, "taxPercent"), decimal.Zero)
		exp, ok5 := optDateStr(f.get(rec, "expectedDate"))
		term, ok6 := optInt(f.get(rec, "paymentTermDays"))
		if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 || disc.GreaterThan(hundred) || tax.GreaterThan(hundred) {
			fail("", "invalid", "unitPrice / receivedQuantity ≥ 0, discount / tax percent 0–100, expectedDate YYYY-MM-DD, paymentTermDays whole number")
			continue
		}
		if !received.LessThan(qty) {
			fail("receivedQuantity", "not_open", "the line is fully received: only open orders are migrated")
			continue
		}
		ot := strings.ToLower(f.get(rec, "orderType"))
		if ot == "" {
			ot = "goods"
		}
		if ot != "goods" && ot != "service" {
			fail("orderType", "invalid", "goods or service")
			continue
		}
		cur := strings.ToUpper(f.get(rec, "currency"))
		if cur == "" {
			cur = cfg.DefaultCurrency
		}
		desc := f.get(rec, "description")
		if desc == "" {
			desc = it.Name
		}
		if _, ok := groups[num]; !ok {
			numbers = append(numbers, num)
		}
		groups[num] = append(groups[num], importLine{row: row, item: *iid, uom: uom, desc: desc, qty: qty, received: received, price: price, disc: disc,
			tax: tax, base: base.Round(6), expected: exp, supplierCode: f.get(rec, "supplierCode"), wh: f.get(rec, "warehouseCode"),
			orderDt: f.get(rec, "orderDate"), orderType: ot, cur: cur, term: term})
	}
	sort.Strings(numbers)
	ordered, receivedV := decimal.Zero, decimal.Zero
	for _, num := range numbers {
		ls := groups[num]
		first := ls[0]
		fail := func(field, code, msg string) {
			res.Errors = append(res.Errors, ProcurementImportError{Row: first.row, Field: field, Code: code, Message: num + ": " + msg})
		}
		consistent := true
		for _, l := range ls[1:] {
			if !strings.EqualFold(l.supplierCode, first.supplierCode) || !strings.EqualFold(l.wh, first.wh) || l.orderDt != first.orderDt ||
				l.orderType != first.orderType || l.cur != first.cur {
				consistent = false
			}
		}
		if !consistent {
			fail("poNumber", "inconsistent", "supplier, warehouse, order date, order type and currency must be the same on every line")
			continue
		}
		exists, err := lookupID(ctx, tx, `SELECT id FROM procurement.purchase_orders WHERE property_id = $1 AND number = $2`, property, num)
		if err != nil {
			return err
		}
		if exists != nil {
			res.Skipped++
			continue
		}
		s, err := lookupID(ctx, tx, `SELECT id FROM procurement.suppliers WHERE property_id = $1 AND upper(code) = upper($2) AND archived_at IS NULL`,
			property, first.supplierCode)
		if err != nil {
			return err
		}
		if s == nil {
			fail("supplierCode", "not_found", "supplier "+first.supplierCode+" not found")
			continue
		}
		sup, err := loadSupplier(ctx, tx, *s)
		if err != nil {
			return err
		}
		wh, err := lookupID(ctx, tx, `SELECT id FROM inventory.warehouses WHERE property_id = $1 AND upper(code) = upper($2) AND archived_at IS NULL`,
			property, first.wh)
		if err != nil {
			return err
		}
		if wh == nil {
			fail("warehouseCode", "not_found", "warehouse "+first.wh+" not found")
			continue
		}
		odate, _ := optDateStr(first.orderDt)
		term := sup.TermDays
		if first.term != nil {
			term = *first.term
		}
		oid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO procurement.purchase_orders (id, property_id, number, order_type, supplier_id, status, order_date, expected_date,
			warehouse_id, currency, payment_term_days, contact_email, notes, source, approved_at, approved_total, sent_at, created_by, updated_by)
			VALUES ($1,$2,$3,$4,$5,'sent',$6,$7,$8,$9,$10,$11,'Migrated open purchase order (cut-over)','migration',now(),0,now(),$12,$12)`,
			oid, property, num, first.orderType, sup.ID, odate, first.expected, *wh, first.cur, term, nz(orderEmail(ctx, tx, sup.ID, sup.Email)), actor(ctx)); err != nil {
			return err
		}
		for i, l := range ls {
			sub, tax, total, _ := lineAmounts(l.qty, l.price, l.disc, l.tax, l.cur)
			hint := "inventory"
			if l.orderType == "service" {
				hint = "expense"
			}
			recvCol := "received_quantity"
			if l.orderType == "service" {
				recvCol = "service_confirmed_quantity"
			}
			exp := l.expected
			if exp == nil {
				exp = first.expected
			}
			if _, err := tx.Exec(ctx, `INSERT INTO procurement.purchase_order_lines (id, property_id, purchase_order_id, line_no, item_id, description, quantity, uom_id,
				base_quantity, unit_price, discount_percent, tax_percent, line_subtotal, tax_amount, line_total, expected_date, `+recvCol+`, opening_received_quantity,
				account_hint) VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9::numeric,$10::numeric,$11::numeric,$12::numeric,$13::numeric,$14::numeric,$15::numeric,$16,
				$17::numeric,$17::numeric,$18)`, id.New(), property, oid, i+1, l.item, l.desc, l.qty.String(), l.uom, l.base.String(), l.price.String(), l.disc.String(),
				l.tax.String(), sub.String(), tax.String(), total.String(), exp, l.received.String(), hint); err != nil {
				return err
			}
			net := netPrice(l.price, l.disc)
			ordered = ordered.Add(money(l.qty.Mul(net), l.cur))
			receivedV = receivedV.Add(money(l.received.Mul(net), l.cur))
			res.Reconciliation.OrderLines++
		}
		if err := refreshOrderTotals(ctx, tx, oid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_orders SET approved_total = total WHERE id = $1`, oid); err != nil {
			return err
		}
		if err := recomputeOrder(ctx, tx, oid); err != nil {
			return err
		}
		res.Created++
		res.Reconciliation.PurchaseOrders++
	}
	res.Reconciliation.OrderedValue = ordered.String()
	res.Reconciliation.ReceivedValue = receivedV.String()
	res.Reconciliation.OutstandingValue = ordered.Sub(receivedV).String()
	return nil
}

func (m *Module) registerMigration(reg *route.Registry) {
	db := m.DB
	reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/procurement/migration:import", Module: "procurement", Tag: "Procurement Migration",
		Scope: route.ScopeProperty, Permission: "procurement.migration.import",
		Summary:     "Import suppliers, supplier items or open purchase orders (dry run / commit, idempotent, reconciliation totals)",
		Description: "FR-MIG-P4-04: run mode=preview (dry run) until the file is valid and the reconciliation totals match the source, then mode=commit.",
		Request:     ProcurementImportInput{}, Response: ProcurementImportResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, _ *http.Request, in ProcurementImportInput) (ProcurementImportResult, error) {
			return m.Import(ctx, tx, handle.Property(ctx), in)
		})})
}
