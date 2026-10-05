package inventory

// PRD P4 EP-29 FR-MIG-P4-03 / FR-MIG-P4-06: opening stock per warehouse
// with value, from the cut-over stock opname (CSV). Items, warehouses,
// stock locations, categories and assets are imported through the P0 Master
// Data Import (resource keys inventory.item, inventory.warehouse, …).
//
// Preview validates every row; commit posts one opening-balance adjustment
// per warehouse (no approval) and returns the reconciliation totals that
// must equal the cut-over opname. An item that already has opening stock in
// a warehouse is refused, so a committed file cannot be posted twice.

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/handle"
)

// OpeningStockInput is an opening stock file.
type OpeningStockInput struct {
	Mode         string `json:"mode" enum:"preview,commit"`
	Filename     string `json:"filename,omitempty"`
	BusinessDate string `json:"businessDate,omitempty" doc:"Cut-over date YYYY-MM-DD (default today)"`
	CSV          string `json:"csv" doc:"Header: warehouseCode,itemCode,quantity,unitCost[,uom,batchNo,expiryDate,serialNos] (serial numbers separated by |)"`
}

// OpeningStockError is a row error of the file.
type OpeningStockError struct {
	Row     int    `json:"row" doc:"1-based line (header = 1)"`
	Field   string `json:"field,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// OpeningStockWarehouse is the reconciliation of one warehouse.
type OpeningStockWarehouse struct {
	WarehouseCode   string  `json:"warehouseCode"`
	Lines           int     `json:"lines"`
	Value           string  `json:"value"`
	StockAdjustment *string `json:"adjustment" doc:"Posted opening balance adjustment (commit)"`
}

// OpeningStockResult is the outcome of an opening stock import.
type OpeningStockResult struct {
	Mode       string                  `json:"mode" enum:"preview,commit"`
	Status     string                  `json:"status" enum:"valid,completed,failed"`
	TotalRows  int                     `json:"totalRows"`
	TotalValue string                  `json:"totalValue" doc:"Must equal the value of the cut-over stock opname"`
	Warehouses []OpeningStockWarehouse `json:"warehouses"`
	Errors     []OpeningStockError     `json:"errors"`
}

// ImportOpeningStock validates (preview) or posts (commit) opening stock.
func (s *Stock) ImportOpeningStock(ctx context.Context, tx pgx.Tx, property uuid.UUID, in OpeningStockInput) (OpeningStockResult, error) {
	res := OpeningStockResult{Mode: in.Mode, Status: "valid", TotalValue: "0", Warehouses: []OpeningStockWarehouse{}, Errors: []OpeningStockError{}}
	if in.Mode != "preview" && in.Mode != "commit" {
		return res, handle.Invalid("mode", "invalid", "mode must be preview or commit")
	}
	if _, err := optDate("businessDate", in.BusinessDate); err != nil {
		return res, err
	}
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(in.CSV, string(rune(0xFEFF)))))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	header, err := r.Read()
	if err != nil {
		return res, handle.Invalid("csv", "empty", "the file needs a header row")
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, need := range []string{"warehousecode", "itemcode", "quantity", "unitcost"} {
		if _, ok := col[need]; !ok {
			return res, handle.Invalid("csv", "missing_column", "column "+need+" is required")
		}
	}
	get := func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return res, err
	}
	groups := map[uuid.UUID][]StockLineInput{}
	codes := map[uuid.UUID]string{}
	values := map[uuid.UUID]decimal.Decimal{}
	seen := map[string]int{}
	total := decimal.Zero
	line := 1
	fail := func(field, code, msg string) {
		res.Errors = append(res.Errors, OpeningStockError{Row: line, Field: field, Code: code, Message: msg})
	}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			fail("", "invalid_csv", err.Error())
			continue
		}
		if strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		res.TotalRows++
		whCode, itemCode := get(rec, "warehousecode"), get(rec, "itemcode")
		wh, err := warehouseByCode(ctx, tx, property, whCode)
		if err != nil {
			return res, err
		}
		if wh == nil {
			fail("warehouseCode", "not_found", "warehouse "+whCode+" not found")
			continue
		}
		var itemID uuid.UUID
		err = tx.QueryRow(ctx, `SELECT id FROM inventory.items WHERE property_id = $1 AND upper(code) = upper($2) AND archived_at IS NULL`, property, itemCode).
			Scan(&itemID)
		if dbtx.IsNoRows(err) {
			fail("itemCode", "not_found", "item "+itemCode+" not found")
			continue
		}
		if err != nil {
			return res, err
		}
		it, err := loadItem(ctx, tx, property, itemID, cfg.ValuationMethod)
		if err != nil {
			return res, err
		}
		if !it.stocked() {
			fail("itemCode", "not_stock", it.Code+" is not a stock item")
			continue
		}
		q, err := decimal.NewFromString(get(rec, "quantity"))
		if err != nil || !q.IsPositive() {
			fail("quantity", "invalid", "quantity must be a positive number")
			continue
		}
		cost, err := decimal.NewFromString(get(rec, "unitcost"))
		if err != nil || cost.IsNegative() {
			fail("unitCost", "invalid", "unitCost must be a number ≥ 0")
			continue
		}
		var uom *uuid.UUID
		if code := get(rec, "uom"); code != "" {
			var u uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT id FROM inventory.uoms WHERE property_id = $1 AND upper(code) = upper($2)`, property, code).Scan(&u); err != nil {
				if dbtx.IsNoRows(err) {
					fail("uom", "not_found", "UOM "+code+" not found")
					continue
				}
				return res, err
			}
			uom = &u
		}
		base, err := toBase(ctx, tx, it, q, uom)
		if err != nil {
			fail("uom", "no_uom_conversion", "no conversion to the stock UOM of "+it.Code)
			continue
		}
		baseCost := cost
		if !base.Equal(q) && base.IsPositive() {
			baseCost = cost.Mul(q).Div(base).Round(6) // unit cost entered per the file's UOM
		}
		if exp := get(rec, "expirydate"); exp != "" {
			if _, err := parseDate(exp); err != nil {
				fail("expiryDate", "invalid_date", "expiryDate must be YYYY-MM-DD")
				continue
			}
		}
		var serials []string
		for _, sn := range strings.Split(get(rec, "serialnos"), "|") {
			if sn = strings.TrimSpace(sn); sn != "" {
				serials = append(serials, sn)
			}
		}
		batch := get(rec, "batchno")
		if (it.Batched && batch == "") || (it.Serial && len(serials) == 0) {
			fail("batchNo", "tracking_required", it.Code+" needs its batch / serial numbers")
			continue
		}
		dupKey := fmt.Sprintf("%s/%s/%s", wh, it.ID, batch)
		if prev, ok := seen[dupKey]; ok {
			fail("itemCode", "duplicate", fmt.Sprintf("%s is already on row %d for this warehouse", it.Code, prev))
			continue
		}
		seen[dupKey] = line
		var already bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM inventory.adjustments a JOIN inventory.adjustment_lines l ON l.adjustment_id = a.id
			WHERE a.warehouse_id = $1 AND a.reason = 'opening_balance' AND a.status = 'posted' AND l.item_id = $2)`, *wh, it.ID).Scan(&already); err != nil {
			return res, err
		}
		if already {
			fail("itemCode", "already_imported", it.Code+" already has opening stock in "+whCode)
			continue
		}
		groups[*wh] = append(groups[*wh], StockLineInput{ItemID: it.ID, Quantity: base.String(), UnitCost: baseCost.String(), BatchNo: batch,
			ExpiryDate: get(rec, "expirydate"), SerialNos: serials})
		codes[*wh] = strings.ToUpper(whCode)
		v := base.Mul(baseCost).Round(2)
		values[*wh] = values[*wh].Add(v)
		total = total.Add(v)
	}
	res.TotalValue = total.String()
	whs := make([]uuid.UUID, 0, len(groups))
	for wh := range groups {
		whs = append(whs, wh)
	}
	sort.Slice(whs, func(i, j int) bool { return codes[whs[i]] < codes[whs[j]] })
	for _, wh := range whs {
		res.Warehouses = append(res.Warehouses, OpeningStockWarehouse{WarehouseCode: codes[wh], Lines: len(groups[wh]), Value: values[wh].String()})
	}
	if len(res.Errors) > 0 {
		res.Status = "failed"
		return res, nil
	}
	if res.TotalRows == 0 {
		return res, errs.Validation("empty_file", "the file has no rows")
	}
	if in.Mode == "preview" {
		return res, nil
	}
	for i, wh := range whs {
		a, err := s.CreateAdjustment(ctx, tx, property, StockAdjustmentInput{WarehouseID: wh, Reason: "opening_balance", BusinessDate: in.BusinessDate,
			Notes: strings.TrimSpace("Opening stock import " + in.Filename), Submit: true, Lines: groups[wh]})
		if err != nil {
			return res, err
		}
		n := a.Number
		res.Warehouses[i].StockAdjustment = &n
	}
	res.Status = "completed"
	return res, record(ctx, tx, property, "inventory.opening_stock", uuid.NewSHA1(property, []byte(in.Filename+in.BusinessDate)), in.Filename, "import", nil,
		map[string]any{"rows": res.TotalRows, "totalValue": res.TotalValue, "warehouses": len(whs)}, "")
}
