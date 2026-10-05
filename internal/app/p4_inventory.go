package app

// PRD P4 EP-01–09 Inventory: item master, stock movement & balance, requisition & transfer, opname, valuation & COGS, automatic consumption, production/waste/batch/expiry, replenishment, assets (owner: inventory).
// Inventory sits in the back office layer: stock is deducted from domain
// events of the business lines (PRD P4 §5.4.1), procurement posts receipts
// through its events, and the accounting period status (K10) is a guard
// registered here once Accounting provides it.

import (
	"context"
	"oneclub/internal/accounting"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/inventory"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/storage"
)

// p4Inventory holds the services of the area.
type p4Inventory struct {
	// Service is the P4 stock service (extends the P2 BOM module).
	Service *inventory.Stock
}

// p4InventoryContributions are the catalogue contributions (permissions, role mappings).
func p4InventoryContributions() []catalog.Contribution {
	return []catalog.Contribution{inventory.P4Contribution()}
}

// p4InventoryDocumentTypes are the approval document types.
func p4InventoryDocumentTypes() []provision.DocumentType { return inventory.DocumentTypes }

// p4InventoryTemplates are the notification templates.
func p4InventoryTemplates() []provision.Template { return inventory.Templates() }

// buildP4Inventory wires routes, hooks, approval decisions and jobs.
func (a *App) buildP4Inventory(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
	module := a.Inventory
	if module == nil {
		module = &inventory.Module{DB: db, Products: a.POS}
	}
	s := &inventory.Stock{Module: module, Events: a.Bus, Approvals: a.Approvals, Notify: a.Notification, Engine: a.Engine}
	s.RegisterP4(reg, a.Engine)
	a.Approvals.RegisterDocumentType(inventory.DocRequisition, s.RequisitionDecision)
	a.Approvals.RegisterDocumentType(inventory.DocAdjustment, s.AdjustmentDecision)
	a.Approvals.RegisterDocumentType(inventory.DocOpname, s.OpnameDecision)
	a.Approvals.RegisterDocumentType(inventory.DocAssetDisposal, s.DisposalDecision)
	s.RegisterJobs(a.Registrar, a.Instance.Location)
	a.Sync.Handle(inventory.SyncCountAction, s.SyncCount)
	// K10: documents dated in a closed accounting period are refused.
	s.SetPeriodGuard(accounting.PeriodStatus)
	a.Stock.Service = s
}

// subscribeP4Inventory registers the event subscribers: automatic
// consumption (K6, K7, golf service BOM), the BEO production schedule (K1)
// and the procurement goods receipts / returns.
func (a *App) subscribeP4Inventory() {
	s := a.Stock.Service
	a.Bus.Subscribe("commercial.sale_completed", "inventory.stock_consumption", s.OnSaleCompleted)
	a.Bus.Subscribe("commercial.sale_refunded", "inventory.refund_restock", s.OnSaleRefunded)
	a.Bus.Subscribe("commercial.package_consumed", "inventory.package_consumption", s.OnPackageConsumed)
	a.Bus.Subscribe("banquet.event_completed", "inventory.banquet_consumption", s.OnBanquetCompleted)
	a.Bus.Subscribe("golf.round_finished", "inventory.golf_round_consumption", s.OnRoundFinished)
	a.Bus.Subscribe("banquet.beo_issued", "inventory.banquet_production_schedule", s.OnBEOIssued)
	a.Bus.Subscribe("banquet.beo_revised", "inventory.banquet_production_revision", s.OnBEOIssued)
	a.Bus.Subscribe("procurement.goods_received", "inventory.goods_receipt", s.OnGoodsReceived)
	a.Bus.Subscribe("procurement.purchase_returned", "inventory.purchase_return", s.OnPurchaseReturned)
	a.Bus.Subscribe("procurement.invoice_price_variance", "inventory.price_revaluation", s.OnInvoicePriceVariance)
}

// demoP4Inventory seeds the inventory structure of PRD P4 §16 #8 / #9 (Modern
// Golf) on the MAIN property: categories valued at moving average with the
// opname tolerances of §16 #8, the warehouses and outlet sub-stores of §16
// #9 (plus Golf Ops, the service BOM store of the Inventory Configuration,
// and the transit warehouse), asset categories and the Stock Opname
// approval to the Finance Manager above tolerance (idempotent).
func demoP4Inventory(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	// §16 #8: moving average for every category; tolerance F&B 2%, beverage /
	// alcohol 0.5%, pro shop 0.5%, general supplies 1%.
	for _, c := range [][3]string{{"FNB", "Food & Beverage", "2"}, {"BEV", "Beverage", "0.5"}, {"PROSHOP", "Pro Shop", "0.5"},
		{"GOLFOPS", "Golf Operations Supplies", "1"}, {"SPORT", "Sport Club Supplies", "1"}, {"AMENITY", "Bungalow Amenities & Linen", "1"},
		{"SPARE", "Spare Parts (Engineering)", "1"}, {"GENERAL", "General Supplies", "1"}} {
		if _, err := tx.Exec(ctx, `INSERT INTO inventory.item_categories (id, property_id, code, name, valuation_method, opname_tolerance_percent)
			VALUES ($1,$2,$3,$4,'moving_average',$5::numeric) ON CONFLICT (property_id, code) DO NOTHING`,
			id.New(), property, c[0], c[1], c[2]); err != nil {
			return err
		}
	}
	type wh struct{ code, name, kind, parent, cc string }
	// §16 #9: stores Main Store, Cold Store, Beverage Store, Pro Shop Store,
	// Engineering Store; outlet sub-stores Clubhouse Restaurant, Bar, Halfway
	// House, Banquet Kitchen (code KITCHEN, the banquet warehouse of the
	// Inventory Configuration), Sport Club Café.
	for _, w := range []wh{{"MAIN-STORE", "Main Store", "store", "", "store"}, {"COLD-STORE", "Cold Store", "store", "", "store"},
		{"BEV-STORE", "Beverage Store", "store", "", "store"}, {"PRO-SHOP", "Pro Shop Store", "store", "", "pro_shop"},
		{"ENGINEERING", "Engineering Store", "store", "", "engineering"},
		{"CLUBHOUSE", "Clubhouse Restaurant", "kitchen", "MAIN-STORE", "kitchen"}, {"BAR", "Bar", "outlet", "BEV-STORE", "bar"},
		{"HALFWAY", "Halfway House", "outlet", "MAIN-STORE", "halfway_house"}, {"KITCHEN", "Banquet Kitchen", "kitchen", "MAIN-STORE", "banquet"},
		{"SPORT-CAFE", "Sport Club Café", "outlet", "MAIN-STORE", "sport_club"},
		{"GOLF-OPS", "Golf Ops", "store", "MAIN-STORE", "golf_ops"}, {"TRANSIT", "In Transit", "transit", "", "transit"}} {
		if _, err := tx.Exec(ctx, `INSERT INTO inventory.warehouses (id, property_id, code, name, location_type, parent_id, cost_center)
			VALUES ($1,$2,$3,$4,$5,(SELECT id FROM inventory.warehouses WHERE property_id = $2 AND code = $6),$7) ON CONFLICT (property_id, code) DO NOTHING`,
			id.New(), property, w.code, w.name, w.kind, w.parent, w.cc); err != nil {
			return err
		}
	}
	// §16 #8: an opname variance above tolerance is approved by the Finance Manager.
	var wf bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.approval_workflows WHERE document_type = $1)`, inventory.DocOpname.Code).Scan(&wf); err != nil {
		return err
	}
	if !wf {
		wid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO platform.approval_workflows (id, document_type, name) VALUES ($1,$2,'Stock Opname Variance above Tolerance')`,
			wid, inventory.DocOpname.Code); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.approval_workflow_steps (id, workflow_id, step_no, name, approver_type, approver_role_id, conditions, sla_hours)
			SELECT $1, $2, 1, 'Finance Manager approval', 'role', r.id, '[]'::jsonb, 24 FROM platform.roles r WHERE r.code = 'finance_manager'`,
			id.New(), wid); err != nil {
			return err
		}
	}
	for _, c := range []struct {
		code, name, class string
		life              int
	}{{"GOLF-CART", "Golf Cart Fleet", "golf_cart", 60}, {"GYM", "Gym Equipment", "gym_equipment", 60}, {"RENTAL", "Rental Equipment (clubs, rackets)", "rental_equipment", 36},
		{"BANQUET-EQ", "Meeting & Banquet Equipment", "banquet_equipment", 60}, {"MAINT-EQ", "Maintenance Equipment", "maintenance_equipment", 96}} {
		if _, err := tx.Exec(ctx, `INSERT INTO inventory.asset_categories (id, property_id, code, name, asset_class, useful_life_months)
			VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (property_id, code) DO NOTHING`, id.New(), property, c.code, c.name, c.class, c.life); err != nil {
			return err
		}
	}
	return nil
}
