package inventory

// PRD P4 FR-INV-07 and §16 #4: consignment items of the pro shop are
// supplier-owned stock without inventory value. Receipts and returns of
// consignment stock are movements with zero cost; sales publish
// inventory.consignment_sold (Stock.Post) and the monthly settlement per
// supplier computes sales, the club commission and the amount payable.

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/handle"
)

// ConsignmentInput receives or returns consignment stock.
type ConsignmentMovementInput struct {
	Direction   string           `json:"direction" enum:"receipt,return" doc:"receipt: the supplier delivers; return: stock goes back to the supplier"`
	SupplierID  uuid.UUID        `json:"supplierId"`
	WarehouseID uuid.UUID        `json:"warehouseId"`
	Reference   string           `json:"reference,omitempty" doc:"Delivery note / return note"`
	Notes       string           `json:"notes,omitempty"`
	Lines       []StockLineInput `json:"lines"`
}

// ConsignmentMovement posts consignment stock in or out (zero value).
func (s *Stock) ConsignmentMovement(ctx context.Context, tx pgx.Tx, property uuid.UUID, in ConsignmentMovementInput) (StockMovement, error) {
	sign, typ := 1, MoveReceipt
	switch in.Direction {
	case "receipt":
	case "return":
		sign, typ = -1, MoveReturnOut
	default:
		return StockMovement{}, handle.Invalid("direction", "invalid", "direction must be receipt or return")
	}
	lines, err := s.baseLines(ctx, tx, property, in.Lines, sign, true)
	if err != nil {
		return StockMovement{}, err
	}
	for i, l := range lines {
		var consignment bool
		var supplier *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT consignment, consignment_supplier_id FROM inventory.items WHERE id = $1`, l.ItemID).Scan(&consignment, &supplier); err != nil {
			return StockMovement{}, err
		}
		if !consignment || supplier == nil || *supplier != in.SupplierID {
			return StockMovement{}, handle.Invalid(lineField(i, "itemId"), "not_consignment", "the item is not a consignment item of this supplier")
		}
	}
	doc := id.New()
	sup := in.SupplierID
	m, _, err := s.Post(ctx, tx, property, PostInput{Type: typ, SourceType: "consignment", SourceID: &doc, WarehouseID: in.WarehouseID, SupplierID: &sup,
		Reason: "consignment_" + in.Direction, Notes: strings.TrimSpace(in.Reference + " " + in.Notes), Lines: lines})
	if err != nil {
		return m, err
	}
	return m, record(ctx, tx, property, "inventory.stock_movement", m.ID, m.Number, "consignment_"+in.Direction, nil,
		map[string]any{"supplierId": in.SupplierID, "lines": len(lines)}, in.Reference)
}

// ConsignmentStock is consignment stock on hand per supplier and item.
type ConsignmentStock struct {
	SupplierID   uuid.UUID `json:"supplierId" db:"supplier_id"`
	SupplierName *string   `json:"supplierName" db:"supplier_name"`
	ItemID       uuid.UUID `json:"itemId" db:"item_id"`
	ItemCode     string    `json:"itemCode" db:"item_code"`
	ItemName     string    `json:"itemName" db:"item_name"`
	WarehouseID  uuid.UUID `json:"warehouseId" db:"warehouse_id"`
	Warehouse    string    `json:"warehouse" db:"warehouse"`
	UOM          string    `json:"uom" db:"uom"`
	OnHand       string    `json:"onHand" db:"on_hand"`
}

// ListConsignmentStock lists consignment stock (supplier optional).
func ListConsignmentStock(ctx context.Context, q dbtx.Querier, property uuid.UUID, supplier *uuid.UUID) ([]ConsignmentStock, error) {
	return handle.List[ConsignmentStock](q.Query(ctx, `SELECT i.consignment_supplier_id AS supplier_id, sp.name AS supplier_name, i.id AS item_id, i.code AS item_code,
		i.name AS item_name, w.id AS warehouse_id, w.name AS warehouse, u.code AS uom, trim_scale(sum(b.quantity))::text AS on_hand
		FROM inventory.stock_balances b JOIN inventory.items i ON i.id = b.item_id JOIN inventory.warehouses w ON w.id = b.warehouse_id
		JOIN inventory.uoms u ON u.id = i.base_uom_id LEFT JOIN procurement.suppliers sp ON sp.id = i.consignment_supplier_id
		WHERE b.property_id = $1 AND i.consignment AND ($2::uuid IS NULL OR i.consignment_supplier_id = $2)
		GROUP BY 1, 2, 3, 4, 5, 6, 7, 8 HAVING sum(b.quantity) <> 0 ORDER BY 2, 4`, property, supplier))
}

// ConsignmentSettlementPreview is the consignment settlement of a supplier and month.
type ConsignmentSettlementPreview struct {
	SupplierID       uuid.UUID                   `json:"supplierId" db:"supplier_id"`
	SupplierName     *string                     `json:"supplierName" db:"supplier_name"`
	Period           string                      `json:"period" db:"period"`
	Quantity         string                      `json:"quantity" db:"quantity"`
	SalesAmount      string                      `json:"salesAmount" db:"sales_amount"`
	CommissionAmount string                      `json:"commissionAmount" db:"commission_amount"`
	PayableAmount    string                      `json:"payableAmount" db:"payable_amount"`
	SettlementID     *uuid.UUID                  `json:"settlementId" db:"settlement_id"`
	Lines            []ConsignmentSettlementLine `json:"lines" db:"-"`
}

// ConsignmentSettlementLine is the consignment sales of one item.
type ConsignmentSettlementLine struct {
	ItemID           uuid.UUID `json:"itemId" db:"item_id"`
	ItemCode         string    `json:"itemCode" db:"item_code"`
	ItemName         string    `json:"itemName" db:"item_name"`
	Quantity         string    `json:"quantity" db:"quantity"`
	SalesAmount      string    `json:"salesAmount" db:"sales_amount"`
	CommissionAmount string    `json:"commissionAmount" db:"commission_amount"`
	PayableAmount    string    `json:"payableAmount" db:"payable_amount"`
}

func periodRange(period string) (time.Time, time.Time, error) {
	p, err := time.Parse("2006-01", strings.TrimSpace(period))
	if err != nil {
		return p, p, handle.Invalid("period", "invalid", "period must be YYYY-MM")
	}
	return p, monthEnd(p.Year(), p.Month()), nil
}

// ConsignmentSettlements previews the settlement of a month per supplier.
func ConsignmentSettlements(ctx context.Context, q dbtx.Querier, property uuid.UUID, period string, supplier *uuid.UUID) ([]ConsignmentSettlementPreview, error) {
	from, to, err := periodRange(period)
	if err != nil {
		return nil, err
	}
	out, err := handle.List[ConsignmentSettlementPreview](q.Query(ctx, `SELECT c.supplier_id, sp.name AS supplier_name, $4::text AS period,
		trim_scale(sum(c.quantity))::text AS quantity, trim_scale(sum(c.sales_amount))::text AS sales_amount,
		trim_scale(sum(c.commission_amount))::text AS commission_amount, trim_scale(sum(c.payable_amount))::text AS payable_amount,
		(SELECT st.id FROM inventory.consignment_settlements st WHERE st.property_id = $1 AND st.supplier_id = c.supplier_id AND st.period = $4) AS settlement_id
		FROM inventory.consignment_sales c LEFT JOIN procurement.suppliers sp ON sp.id = c.supplier_id
		WHERE c.property_id = $1 AND c.business_date BETWEEN $2 AND $3 AND ($5::uuid IS NULL OR c.supplier_id = $5)
		GROUP BY c.supplier_id, sp.name ORDER BY sp.name`, property, from, to, from.Format("2006-01"), supplier))
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Lines, err = handle.List[ConsignmentSettlementLine](q.Query(ctx, `SELECT c.item_id, i.code AS item_code, i.name AS item_name,
			trim_scale(sum(c.quantity))::text AS quantity, trim_scale(sum(c.sales_amount))::text AS sales_amount,
			trim_scale(sum(c.commission_amount))::text AS commission_amount, trim_scale(sum(c.payable_amount))::text AS payable_amount
			FROM inventory.consignment_sales c JOIN inventory.items i ON i.id = c.item_id
			WHERE c.property_id = $1 AND c.supplier_id = $2 AND c.business_date BETWEEN $3 AND $4 GROUP BY 1, 2, 3 ORDER BY 2`,
			property, out[i].SupplierID, from, to))
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ConsignmentSettlementInput issues the monthly settlement of a supplier.
type ConsignmentSettlementInput struct {
	SupplierID uuid.UUID `json:"supplierId"`
	Period     string    `json:"period" doc:"YYYY-MM"`
	Notes      string    `json:"notes,omitempty"`
}

// ConsignmentSettlement is an issued consignment settlement.
type ConsignmentSettlement struct {
	ID               uuid.UUID `json:"id" db:"id"`
	Number           string    `json:"number" db:"number"`
	SupplierID       uuid.UUID `json:"supplierId" db:"supplier_id"`
	SupplierName     *string   `json:"supplierName" db:"supplier_name"`
	Period           string    `json:"period" db:"period"`
	Quantity         string    `json:"quantity" db:"quantity"`
	SalesAmount      string    `json:"salesAmount" db:"sales_amount"`
	CommissionAmount string    `json:"commissionAmount" db:"commission_amount"`
	PayableAmount    string    `json:"payableAmount" db:"payable_amount"`
	Currency         string    `json:"currency" db:"currency"`
	Status           string    `json:"status" db:"status" enum:"issued"`
	Notes            *string   `json:"notes" db:"notes"`
	CreatedAt        time.Time `json:"createdAt" db:"created_at"`
}

const settlementSelect = `SELECT s.id, s.number, s.supplier_id, sp.name AS supplier_name, s.period, trim_scale(s.quantity)::text AS quantity,
	trim_scale(s.sales_amount)::text AS sales_amount, trim_scale(s.commission_amount)::text AS commission_amount,
	trim_scale(s.payable_amount)::text AS payable_amount, s.currency, s.status, s.notes, s.created_at
	FROM inventory.consignment_settlements s LEFT JOIN procurement.suppliers sp ON sp.id = s.supplier_id`

// SettleConsignment issues the settlement of a supplier's month (the AP
// basis); the month's consignment sales are marked settled.
func (s *Stock) SettleConsignment(ctx context.Context, tx pgx.Tx, property uuid.UUID, in ConsignmentSettlementInput) (ConsignmentSettlement, error) {
	from, to, err := periodRange(in.Period)
	if err != nil {
		return ConsignmentSettlement{}, err
	}
	var qtyS, sales, comm, pay string
	if err := tx.QueryRow(ctx, `SELECT coalesce(sum(quantity), 0)::text, coalesce(sum(sales_amount), 0)::text, coalesce(sum(commission_amount), 0)::text,
		coalesce(sum(payable_amount), 0)::text FROM inventory.consignment_sales WHERE property_id = $1 AND supplier_id = $2 AND business_date BETWEEN $3 AND $4
		AND settlement_id IS NULL`, property, in.SupplierID, from, to).Scan(&qtyS, &sales, &comm, &pay); err != nil {
		return ConsignmentSettlement{}, err
	}
	if dec(qtyS).IsZero() {
		return ConsignmentSettlement{}, errs.Validation("nothing_to_settle", "no unsettled consignment sales of this supplier in "+from.Format("2006-01"))
	}
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return ConsignmentSettlement{}, err
	}
	sid := id.New()
	number, err := docNumber(ctx, tx, property, "CSG")
	if err != nil {
		return ConsignmentSettlement{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO inventory.consignment_settlements (id, property_id, number, supplier_id, period, quantity, sales_amount, commission_amount,
		payable_amount, currency, notes, created_by) VALUES ($1,$2,$3,$4,$5,$6::numeric,$7::numeric,$8::numeric,$9::numeric,$10,$11,$12)`,
		sid, property, number, in.SupplierID, from.Format("2006-01"), qtyS, sales, comm, pay, cfg.Currency, nullStr(in.Notes), uuidOrNil(handle.UserID(ctx))); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return ConsignmentSettlement{}, errs.Conflict("already_settled", "the consignment of this supplier is already settled for "+from.Format("2006-01"))
		}
		return ConsignmentSettlement{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.consignment_sales SET settlement_id = $5 WHERE property_id = $1 AND supplier_id = $2 AND business_date BETWEEN $3 AND $4
		AND settlement_id IS NULL`, property, in.SupplierID, from, to, sid); err != nil {
		return ConsignmentSettlement{}, err
	}
	rows, err := tx.Query(ctx, settlementSelect+` WHERE s.id = $1`, sid)
	st, err := handle.One[ConsignmentSettlement](rows, err, "settlement")
	if err != nil {
		return st, err
	}
	return st, record(ctx, tx, property, "inventory.consignment_settlement", sid, number, "create", nil, st, "")
}

var _ = decimal.Zero
