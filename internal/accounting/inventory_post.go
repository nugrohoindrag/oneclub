package accounting

// P4 ledger fixes around inventory, assets and commissions:
//   - the ledger accounts of an item category (FR-INV-01/02, inherited from
//     the parent category) come before the default posting rules for the
//     inventory side and the counter side of stock postings;
//   - the opening stock (inventory.movement_posted sourceType opening_stock)
//     is booked against opening balance equity, or not at all when the
//     posted GL opening balances already carry the inventory (FR-MIG-P4-03,
//     FR-TRS-02) — never against the P&L;
//   - Stock Valuation = GL inventory per inventory account (FR-VAL-05,
//     EP-05 AC2, EP-28 AC), part of the control reconciliation and of the
//     period closing checklist;
//   - asset disposal (FR-AST-06) and sales commission (FR-PST-02) journals.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
)

// itemAccounts are the ledger accounts of an item's category ("" = the
// posting rules decide).
type itemAccounts struct {
	Inventory, COGS, Expense, Waste, Variance string
}

// saleSources are the stock movement sources whose issues are a cost of sales.
var saleSources = map[string]bool{"sale": true, "package": true, "banquet": true, "golf_round": true}

// counter is the category account of the other side of a stock movement.
func (a itemAccounts) counter(movementType, sourceType string) string {
	switch movementType {
	case "consumption":
		return a.COGS
	case "issue":
		if saleSources[sourceType] {
			return a.COGS
		}
		return a.Expense
	case "waste":
		return a.Waste
	case "adjustment", "opname":
		if sourceType == "opening_stock" {
			return ""
		}
		return a.Variance
	case "transfer_in", "transfer_out":
		return a.Inventory // a transfer stays on the item's inventory account
	}
	return ""
}

// loadItemAccounts reads the category accounts of items
// (reporting.acc_inventory_item_accounts).
func loadItemAccounts(ctx context.Context, q dbtx.Querier, ids []uuid.UUID) (map[uuid.UUID]itemAccounts, error) {
	out := map[uuid.UUID]itemAccounts{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT item_id, coalesce(inventory_account, ''), coalesce(cogs_account, ''), coalesce(expense_account, ''),
		coalesce(waste_account, ''), coalesce(variance_account, '') FROM reporting.acc_inventory_item_accounts WHERE item_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var iid uuid.UUID
		var a itemAccounts
		if err := rows.Scan(&iid, &a.Inventory, &a.COGS, &a.Expense, &a.Waste, &a.Variance); err != nil {
			return nil, err
		}
		out[iid] = a
	}
	return out, rows.Err()
}

// amountLine is the value of one item line of a source document.
type amountLine struct {
	ItemID *uuid.UUID
	Amount decimal.Decimal
}

// accountGroup is the part of a document's value on one inventory account
// ("" = the default of the posting rule).
type accountGroup struct {
	Code   string
	Amount decimal.Decimal
}

// inventoryGroups splits the value of a document by the inventory account
// of its items; what the lines do not explain stays on the rule's account.
func inventoryGroups(ctx context.Context, q dbtx.Querier, total decimal.Decimal, lines []amountLine) ([]accountGroup, error) {
	var ids []uuid.UUID
	for _, l := range lines {
		if l.ItemID != nil {
			ids = append(ids, *l.ItemID)
		}
	}
	accts, err := loadItemAccounts(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	sums := map[string]decimal.Decimal{}
	var order []string
	explained := decimal.Zero
	for _, l := range lines {
		code := ""
		if l.ItemID != nil {
			code = accts[*l.ItemID].Inventory
		}
		if code == "" {
			continue
		}
		if _, ok := sums[code]; !ok {
			order = append(order, code)
		}
		sums[code] = sums[code].Add(l.Amount)
		explained = explained.Add(l.Amount)
	}
	out := []accountGroup{}
	if rest := total.Sub(explained); !rest.IsZero() || len(order) == 0 {
		out = append(out, accountGroup{Amount: rest})
	}
	for _, c := range order {
		out = append(out, accountGroup{Code: c, Amount: sums[c]})
	}
	return out, nil
}

// inventoryControlCodes are the inventory control accounts of a property:
// the default inventory account and the inventory accounts of its item
// categories.
func inventoryControlCodes(ctx context.Context, q dbtx.Querier, property uuid.UUID, cfg AccountingConfiguration) ([]string, error) {
	set := map[string]bool{}
	if c := cfg.Accounts["inventory"]; c != "" {
		set[c] = true
	}
	rows, err := q.Query(ctx, `SELECT DISTINCT inventory_account FROM reporting.acc_inventory_item_accounts WHERE property_id = $1
		AND inventory_account IS NOT NULL`, property)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return nil, err
		}
		set[c] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out, nil
}

// openingCarriesInventory reports whether a posted GL opening balance batch
// of the property has a line on an inventory control account: Finance
// carried the stock in the opening trial balance, so the opening stock
// import must not book it again.
func openingCarriesInventory(ctx context.Context, q dbtx.Querier, property uuid.UUID) (bool, error) {
	cfg, err := LoadConfiguration(ctx, q, property)
	if err != nil {
		return false, err
	}
	codes, err := inventoryControlCodes(ctx, q, property, cfg)
	if err != nil {
		return false, err
	}
	var ok bool
	err = q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounting.opening_balance_lines l JOIN accounting.opening_balances b ON b.id = l.batch_id
		JOIN accounting.accounts a ON a.id = l.account_id WHERE b.property_id = $1 AND b.status = 'posted' AND a.code = ANY($2)
		AND (l.debit <> 0 OR l.credit <> 0))`, property, codes).Scan(&ok)
	return ok, err
}

// ── Stock Valuation = GL inventory (FR-VAL-05) ────────────────────────────

// ReconcileInventory compares, per inventory account, the GL balance with
// the Stock Valuation at a date (valued movement lines up to the date, by
// the inventory account of the item's category or the default inventory
// account; consignment stock has no value).
func ReconcileInventory(ctx context.Context, q dbtx.Querier, property uuid.UUID, asOf time.Time) ([]ControlReconCheck, error) {
	cfg, err := LoadConfiguration(ctx, q, property)
	if err != nil {
		return nil, err
	}
	def := cfg.Accounts["inventory"]
	codes, err := inventoryControlCodes(ctx, q, property, cfg)
	if err != nil {
		return nil, err
	}
	val := map[string]decimal.Decimal{}
	rows, err := q.Query(ctx, `SELECT coalesce(inventory_account, $3), round(coalesce(sum(total_cost), 0), 2)::text FROM reporting.acc_inventory_valuation
		WHERE property_id = $1 AND business_date <= $2 AND NOT consignment GROUP BY 1`, property, dateOnly(asOf), def)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c, v string
		if err := rows.Scan(&c, &v); err != nil {
			rows.Close()
			return nil, err
		}
		val[c] = val[c].Add(dec(v))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for c := range val {
		if !containsString(codes, c) {
			codes = append(codes, c)
		}
	}
	sort.Strings(codes)
	out := []ControlReconCheck{}
	for _, c := range codes {
		gl := decimal.Zero
		aid, err := accountIDByCode(ctx, q, c)
		switch {
		case err == nil:
			if gl, err = accountBalance(ctx, q, &property, aid, &asOf); err != nil {
				return nil, err
			}
		case !dbtx.IsNoRows(err):
			return nil, err
		}
		out = append(out, check("inventory_valuation:"+c, "Inventory "+c+" = Stock Valuation", gl.Round(2), val[c]))
	}
	return out, nil
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// inventoryChecklistItem is the period closing check of FR-VAL-05.
func inventoryChecklistItem(ctx context.Context, q dbtx.Querier, property uuid.UUID, end time.Time, blocking bool) (PeriodChecklistItem, error) {
	checks, err := ReconcileInventory(ctx, q, property, end)
	if err != nil {
		return PeriodChecklistItem{}, err
	}
	item := PeriodChecklistItem{Code: "inventory_reconciled", Label: "Stock valuation = GL inventory", OK: true, Blocking: blocking}
	var diffs []string
	for _, c := range checks {
		if !c.OK {
			item.OK = false
			diffs = append(diffs, strings.TrimPrefix(c.Code, "inventory_valuation:")+" difference "+c.Difference)
		}
	}
	item.Detail = fmt.Sprintf("%d inventory accounts reconciled", len(checks))
	if len(diffs) > 0 {
		item.Detail = strings.Join(diffs, "; ")
	}
	return item, nil
}

// ── asset disposal (FR-AST-06) ────────────────────────────────────────────

// onAssetDisposed derecognises a disposed asset: Dr accumulated depreciation,
// Dr proceeds receivable, Cr asset cost and the gain (Cr) or loss (Dr) of
// proceeds − book value. The accounts of the asset category come first.
func (m *Module) onAssetDisposed(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		AssetID                 uuid.UUID `json:"assetId"`
		AssetCode               string    `json:"assetCode"`
		Name                    string    `json:"name"`
		Category                string    `json:"category"`
		DisposedOn              string    `json:"disposedOn"`
		AcquisitionCost         string    `json:"acquisitionCost"`
		AccumulatedDepreciation string    `json:"accumulatedDepreciation"`
		Proceeds                string    `json:"proceeds"`
		AssetAccount            string    `json:"assetAccount"`
		AccumulatedAccount      string    `json:"accumulatedAccount"`
	}
	if err := ev.decode(&p); err != nil || p.AssetID == uuid.Nil {
		return outcome{}, badPayload("asset_disposed payload")
	}
	cost, acc, proceeds := dec(p.AcquisitionCost), dec(p.AccumulatedDepreciation), dec(p.Proceeds)
	if cost.IsZero() && acc.IsZero() && proceeds.IsZero() {
		return outcome{Status: "no_posting", Note: "no value"}, nil
	}
	gain := proceeds.Sub(cost.Sub(acc))
	d := eventDate(ctx, tx, ev)
	if t, err := time.Parse("2006-01-02", p.DisposedOn); err == nil {
		d = t
	}
	desc := "Disposal " + p.AssetCode + " " + p.Name
	item := func(part string, amt decimal.Decimal) Item {
		return Item{Source: ev.Type, Attrs: map[string]string{"part": part, "category": p.Category}, Amount: amt, Description: desc,
			Dims: Dims{CostCenter: p.Category}, SourceType: "inventory.asset", SourceID: p.AssetCode}
	}
	ci := item("cost", cost)
	ci.CreditCode = p.AssetAccount
	ai := item("accumulated", acc)
	ai.DebitCode = p.AccumulatedAccount
	items := []Item{ci, ai, item("proceeds", proceeds)}
	if gain.IsPositive() {
		items = append(items, item("gain", gain))
	} else if gain.IsNegative() {
		items = append(items, item("loss", gain.Neg()))
	}
	var out outcome
	j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: ev.Property, Date: d, SourceType: ev.Type, SourceID: p.AssetID.String(),
		SourceRef: p.AssetCode, EventID: &ev.ID, Description: "Asset disposal " + p.AssetCode + " " + p.Name},
		Sources: []sourceMark{{Type: "inventory.asset_disposal", ID: p.AssetID, BusinessDate: &d, Key1: p.AssetCode, Amount: cost, Items: items}}})
	out.add(j, missing)
	return out, err
}

// ── sales commission (FR-PST-02) ──────────────────────────────────────────

// onCommissionApproved accrues an approved commission statement: Dr sales
// commission expense / Cr commission payable (its net total: clawbacks and
// adjustments are lines of the statement; a negative total reverses).
// Paying it (payroll) clears the payable through the bank.
func (m *Module) onCommissionApproved(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		StatementID uuid.UUID  `json:"statementId"`
		Number      string     `json:"number"`
		UserID      *uuid.UUID `json:"userId"`
		Period      string     `json:"period"`
		Total       string     `json:"total"`
	}
	if err := ev.decode(&p); err != nil || p.StatementID == uuid.Nil {
		return outcome{}, badPayload("commission_approved payload")
	}
	amt := dec(p.Total)
	if amt.IsZero() {
		return outcome{Status: "no_posting", Note: "commission statement " + p.Number + " without amount"}, nil
	}
	d := eventDate(ctx, tx, ev)
	var dims Dims
	if p.UserID != nil {
		u := *p.UserID
		dims = Dims{PartnerType: "employee", PartnerID: &u}
	}
	var out outcome
	j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: ev.Property, Date: d, SourceType: ev.Type, SourceID: p.StatementID.String(),
		SourceRef: p.Number, EventID: &ev.ID, Description: "Sales commission " + p.Number + " (" + p.Period + ")"},
		Sources: []sourceMark{{Type: "crm.commission_statement", ID: p.StatementID, BusinessDate: &d, Key1: p.Period, Amount: amt, Items: []Item{{Source: ev.Type,
			Attrs: map[string]string{"period": p.Period}, Amount: amt, Dims: dims, DimSide: "credit", Description: "Commission " + p.Number,
			SourceType: "crm.commission_statement", SourceID: p.StatementID.String()}}}}})
	out.add(j, missing)
	return out, err
}
