package inventory

// PRD P4 EP-01 Item Master & Inventory Setup and EP-27 Inventory
// Configuration / Inventory Policies: the P2 item is extended into the full
// item master (categories, tracking, barcodes, supplier items, consignment)
// and the warehouse structure of PRD P4 §7.5 is added (warehouses, stock
// locations, par stock, reorder points), plus the master data of Assets &
// Equipment (EP-09). Everything is a resource.Def: list / view / add / edit /
// delete, CSV/XLSX export and CSV import (EP-29) come from the engine.

import (
	"context"
	"regexp"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/resource"
	"oneclub/internal/platform/rules"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func supplierRef() *resource.Ref {
	return &resource.Ref{Table: "procurement.suppliers", SameProperty: true, Label: "supplier"}
}

// ItemTypes are the item types of the P4 item master (FR-INV-01).
var ItemTypes = []string{"raw", "semi_finished", "finished", "packaging", "consumable", "retail", "spare_part", "asset", "service", "non_stock"}

// Categories are hierarchical item categories with default accounts and
// valuation method (FR-INV-02).
var Categories = &resource.Def{
	Key: "inventory.item_category", Module: "inventory", Perm: "inventory.item_category", Path: "/api/v1/inventory/categories", Table: "inventory.item_categories",
	Name: "Item Category", Plural: "Categories", SchemaName: "InventoryCategory", Tag: "Inventory Setup", PropertyScoped: true, Archive: true,
	CodeField: "code", OrderBy: "code, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "parentId", Column: "parent_id", Label: "Parent Category", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "inventory.item_categories", SameProperty: true, Label: "category"}},
		{Name: "valuationMethod", Column: "valuation_method", Label: "Valuation Method", Kind: resource.Enum, Enum: []string{"moving_average", "fifo"}, Default: "moving_average"},
		{Name: "inventoryAccount", Column: "inventory_account", Label: "Inventory Account", Kind: resource.String, Max: 40},
		{Name: "cogsAccount", Column: "cogs_account", Label: "COGS Account", Kind: resource.String, Max: 40},
		{Name: "expenseAccount", Column: "expense_account", Label: "Expense Account (issues)", Kind: resource.String, Max: 40},
		{Name: "wasteAccount", Column: "waste_account", Label: "Waste Account", Kind: resource.String, Max: 40},
		{Name: "varianceAccount", Column: "variance_account", Label: "Stock Variance Account", Kind: resource.String, Max: 40},
		{Name: "opnameTolerancePercent", Column: "opname_tolerance_percent", Label: "Stock Opname Tolerance (%)", Kind: resource.Decimal,
			Min: resource.Min(0), MaxN: resource.Max(100)},
		resource.Status("active", "inactive")},
}

// Warehouses are the stock-holding locations of PRD P4 §7.5 (FR-INV-04).
var Warehouses = &resource.Def{
	Key: "inventory.warehouse", Module: "inventory", Perm: "inventory.warehouse", Path: "/api/v1/inventory/warehouses", Table: "inventory.warehouses",
	Name: "Warehouse", SchemaName: "InventoryWarehouse", Plural: "Warehouses", Tag: "Inventory Setup", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "code, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "parentId", Column: "parent_id", Label: "Replenished From (store)", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "inventory.warehouses", SameProperty: true, Label: "warehouse"}},
		{Name: "locationType", Column: "location_type", Label: "Location Type", Kind: resource.Enum, Enum: []string{"store", "kitchen", "outlet", "transit", "quarantine"},
			Default: "store", Filter: true},
		{Name: "outletId", Column: "outlet_id", Label: "Outlet (POS sales consume here)", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "commercial.outlets", SameProperty: true, Label: "outlet"}},
		{Name: "costCenter", Column: "cost_center", Label: "Cost Center", Kind: resource.String, Max: 40},
		{Name: "allowNegative", Column: "allow_negative", Label: "Allow Negative Stock (flagged for review)", Kind: resource.Bool, Default: false},
		{Name: "keeperUserIds", Column: "keeper_user_ids", Label: "Stock Holders (user ids)", Kind: resource.StringList, Pattern: uuidPattern,
			PatternMsg: "must be user ids"},
		resource.Status("active", "inactive")},
}

// StockLocations are bins / shelves / cold rooms inside a warehouse.
var StockLocations = &resource.Def{
	Key: "inventory.stock_location", Module: "inventory", Perm: "inventory.stock_location", Path: "/api/v1/inventory/locations", Table: "inventory.stock_locations",
	Name: "Stock Location", SchemaName: "InventoryStockLocation", Plural: "Stock Locations", Tag: "Inventory Setup", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "code, id",
	Fields: []resource.Field{
		{Name: "warehouseId", Column: "warehouse_id", Label: "Warehouse", Kind: resource.UUID, Required: true, Filter: true,
			Ref: &resource.Ref{Table: "inventory.warehouses", SameProperty: true, Label: "warehouse"}},
		resource.Code("Code"), resource.Name(),
		{Name: "locationType", Column: "location_type", Label: "Location Type", Kind: resource.Enum,
			Enum: []string{"bin", "shelf", "cold_room", "freezer", "quarantine", "staging"}, Default: "bin", Filter: true},
		resource.Status("active", "inactive")},
}

// ParStocks are par / minimum stock levels per item × warehouse (FR-RPL-01).
var ParStocks = &resource.Def{
	Key: "inventory.par_stock", Module: "inventory", Perm: "inventory.par_stock", Path: "/api/v1/inventory/par-stocks", Table: "inventory.par_stocks",
	Name: "Par Stock", SchemaName: "InventoryParStock", Plural: "Par Stock", Tag: "Inventory Setup", PropertyScoped: true, OrderBy: "created_at, id",
	Fields: []resource.Field{
		{Name: "itemId", Column: "item_id", Label: "Item", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "inventory.items", SameProperty: true, Label: "item"}},
		{Name: "warehouseId", Column: "warehouse_id", Label: "Warehouse", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "inventory.warehouses", SameProperty: true, Label: "warehouse"}},
		{Name: "parLevel", Column: "par_level", Label: "Par Level (stock UOM)", Kind: resource.Decimal, Required: true, Min: resource.Min(0)},
		{Name: "minStock", Column: "min_stock", Label: "Minimum Stock", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "stockLocationId", Column: "stock_location_id", Label: "Default Stock Location", Kind: resource.UUID,
			Ref: &resource.Ref{Table: "inventory.stock_locations", SameProperty: true, Label: "stock location"}}},
}

// ReorderPoints trigger the automatic purchase requisition (FR-RPL-02).
var ReorderPoints = &resource.Def{
	Key: "inventory.reorder_point", Module: "inventory", Perm: "inventory.reorder_point", Path: "/api/v1/inventory/reorder-points", Table: "inventory.reorder_points",
	Name: "Reorder Point", SchemaName: "InventoryReorderPoint", Plural: "Reorder Points", Tag: "Inventory Setup", PropertyScoped: true, OrderBy: "created_at, id",
	Fields: []resource.Field{
		{Name: "itemId", Column: "item_id", Label: "Item", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "inventory.items", SameProperty: true, Label: "item"}},
		{Name: "warehouseId", Column: "warehouse_id", Label: "Warehouse", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "inventory.warehouses", SameProperty: true, Label: "warehouse"}},
		{Name: "reorderPoint", Column: "reorder_point", Label: "Reorder Point (stock UOM)", Kind: resource.Decimal, Required: true, Min: resource.Min(0)},
		{Name: "reorderQuantity", Column: "reorder_quantity", Label: "Fixed Order Quantity (empty = par − stock − on order)", Kind: resource.Decimal, Min: resource.Min(0)},
		{Name: "preferredSupplierId", Column: "preferred_supplier_id", Label: "Preferred Supplier", Kind: resource.UUID, Ref: supplierRef()},
		{Name: "leadTimeDays", Column: "lead_time_days", Label: "Lead Time (days)", Kind: resource.Int, Min: resource.Min(0)}},
}

// ItemBarcodes are additional barcodes per UOM (e.g. the carton barcode).
var ItemBarcodes = &resource.Def{
	Key: "inventory.item_barcode", Module: "inventory", Perm: "inventory.item", Path: "/api/v1/inventory/item-barcodes", Table: "inventory.item_barcodes",
	Name: "Item Barcode", SchemaName: "InventoryItemBarcode", Plural: "Item Barcodes", Tag: "Inventory Setup", PropertyScoped: true, OrderBy: "created_at, id",
	Fields: []resource.Field{
		{Name: "itemId", Column: "item_id", Label: "Item", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "inventory.items", SameProperty: true, Label: "item"}},
		{Name: "barcode", Column: "barcode", Label: "Barcode", Kind: resource.String, Required: true, Max: 64, Search: true},
		{Name: "uomId", Column: "uom_id", Label: "UOM of the barcode (empty = stock UOM)", Kind: resource.UUID,
			Ref: &resource.Ref{Table: "inventory.uoms", SameProperty: true, Label: "UOM"}}},
}

// SupplierItems are the supplier's codes, purchase UOM and last price.
var SupplierItems = &resource.Def{
	Key: "inventory.supplier_item", Module: "inventory", Perm: "inventory.item", Path: "/api/v1/inventory/supplier-items", Table: "inventory.supplier_items",
	Name: "Supplier Item", SchemaName: "InventorySupplierItem", Plural: "Supplier Items", Tag: "Inventory Setup", PropertyScoped: true, OrderBy: "created_at, id",
	Fields: []resource.Field{
		{Name: "itemId", Column: "item_id", Label: "Item", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "inventory.items", SameProperty: true, Label: "item"}},
		{Name: "supplierId", Column: "supplier_id", Label: "Supplier", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true, Ref: supplierRef()},
		{Name: "supplierItemCode", Column: "supplier_item_code", Label: "Supplier Item Code", Kind: resource.String, Max: 60, Search: true},
		{Name: "purchaseUomId", Column: "purchase_uom_id", Label: "Purchase UOM", Kind: resource.UUID,
			Ref: &resource.Ref{Table: "inventory.uoms", SameProperty: true, Label: "UOM"}},
		{Name: "lastPrice", Column: "last_price", Label: "Last Price per Stock UOM", Kind: resource.Decimal, Min: resource.Min(0)},
		{Name: "leadTimeDays", Column: "lead_time_days", Label: "Lead Time (days)", Kind: resource.Int, Min: resource.Min(0)},
		{Name: "preferred", Column: "preferred", Label: "Preferred Supplier", Kind: resource.Bool, Default: false}},
}

// AssetCategories carry the depreciation defaults (FR-AST-05).
var AssetCategories = &resource.Def{
	Key: "inventory.asset_category", Module: "inventory", Perm: "inventory.asset_category", Path: "/api/v1/inventory/asset-categories",
	Table: "inventory.asset_categories", Name: "Asset Category", SchemaName: "InventoryAssetCategory", Plural: "Asset Categories", Tag: "Assets & Equipment", PropertyScoped: true, Archive: true,
	CodeField: "code", OrderBy: "code, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "assetClass", Column: "asset_class", Label: "Asset Class", Kind: resource.Enum, Enum: []string{"golf_cart", "gym_equipment", "rental_equipment",
			"banquet_equipment", "maintenance_equipment", "it_equipment", "vehicle", "furniture", "other"}, Default: "other", Filter: true},
		{Name: "depreciationMethod", Column: "depreciation_method", Label: "Depreciation Method", Kind: resource.Enum,
			Enum: []string{"straight_line", "declining_balance"}, Default: "straight_line"},
		{Name: "usefulLifeMonths", Column: "useful_life_months", Label: "Useful Life (months)", Kind: resource.Int, Default: int64(60), Min: resource.Min(1)},
		{Name: "residualPercent", Column: "residual_percent", Label: "Residual Value (%)", Kind: resource.Decimal, Default: "0", Min: resource.Min(0), MaxN: resource.Max(99)},
		{Name: "decliningRatePercent", Column: "declining_rate_percent", Label: "Declining Balance Rate (% per year)", Kind: resource.Decimal,
			Min: resource.Min(0), MaxN: resource.Max(100)},
		{Name: "assetAccount", Column: "asset_account", Label: "Fixed Asset Account", Kind: resource.String, Max: 40},
		{Name: "accumulatedAccount", Column: "accumulated_account", Label: "Accumulated Depreciation Account", Kind: resource.String, Max: 40},
		{Name: "expenseAccount", Column: "expense_account", Label: "Depreciation Expense Account", Kind: resource.String, Max: 40},
		resource.Status("active", "inactive")},
}

// Assets is the Asset Register (FR-AST-01).
var Assets = &resource.Def{
	Key: "inventory.asset", Module: "inventory", Perm: "inventory.asset", Path: "/api/v1/inventory/assets", Table: "inventory.assets",
	Name: "Asset", SchemaName: "InventoryAsset", Plural: "Assets & Equipment", Tag: "Assets & Equipment", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "code, id",
	Fields: []resource.Field{resource.Code("Asset Code"), resource.Name(),
		{Name: "categoryId", Column: "category_id", Label: "Asset Category", Kind: resource.UUID, Required: true, Filter: true,
			Ref: &resource.Ref{Table: "inventory.asset_categories", SameProperty: true, Label: "asset category"}},
		{Name: "serialNo", Column: "serial_no", Label: "Serial No.", Kind: resource.String, Max: 80, Search: true},
		{Name: "brand", Column: "brand", Label: "Brand", Kind: resource.String, Max: 80},
		{Name: "model", Column: "model", Label: "Model", Kind: resource.String, Max: 80},
		{Name: "warehouseId", Column: "warehouse_id", Label: "Location", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "inventory.warehouses", SameProperty: true, Label: "warehouse"}},
		{Name: "departmentId", Column: "department_id", Label: "Department", Kind: resource.UUID,
			Ref: &resource.Ref{Table: "platform.departments", SameProperty: true, Label: "department"}},
		{Name: "costCenter", Column: "cost_center", Label: "Cost Center", Kind: resource.String, Max: 40},
		{Name: "acquisitionDate", Column: "acquisition_date", Label: "Acquisition Date", Kind: resource.Date},
		{Name: "acquisitionCost", Column: "acquisition_cost", Label: "Acquisition Cost", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "residualValue", Column: "residual_value", Label: "Residual Value", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "usefulLifeMonths", Column: "useful_life_months", Label: "Useful Life (months; empty = category)", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "depreciationMethod", Column: "depreciation_method", Label: "Depreciation Method (empty = category)", Kind: resource.Enum,
			Enum: []string{"straight_line", "declining_balance"}},
		{Name: "depreciationStart", Column: "depreciation_start", Label: "Depreciation Start", Kind: resource.Date},
		{Name: "openingAccumulated", Column: "opening_accumulated", Label: "Accumulated Depreciation at Migration", Kind: resource.Decimal, Default: "0",
			Min: resource.Min(0), CreateOnly: true},
		{Name: "accumulatedDepreciation", Column: "accumulated_depreciation", Label: "Accumulated Depreciation", Kind: resource.Decimal, ReadOnly: true},
		{Name: "bookValue", Column: "book_value", Label: "Book Value", Kind: resource.Decimal, ReadOnly: true},
		{Name: "supplierId", Column: "supplier_id", Label: "Supplier", Kind: resource.UUID, Ref: supplierRef()},
		{Name: "warrantyUntil", Column: "warranty_until", Label: "Warranty Until", Kind: resource.Date},
		{Name: "golfCartRef", Column: "golf_cart_ref", Label: "Golf Cart (P2 fleet)", Kind: resource.UUID},
		{Name: "rentable", Column: "rentable", Label: "Rental Equipment", Kind: resource.Bool, Default: false, Filter: true},
		{Name: "rentalStatus", Column: "rental_status", Label: "Rental Status", Kind: resource.Enum, Enum: []string{"available", "out"}, ReadOnly: true},
		{Name: "usageHours", Column: "usage_hours", Label: "Usage Hours", Kind: resource.Decimal, ReadOnly: true},
		{Name: "status", Column: "status", Label: "Status", Kind: resource.Enum, Enum: []string{"active", "maintenance", "out_of_service", "disposed"},
			Default: "active", Filter: true},
		{Name: "disposedOn", Column: "disposed_on", Label: "Disposed On", Kind: resource.Date, ReadOnly: true},
		{Name: "disposalReason", Column: "disposal_reason", Label: "Disposal Reason", Kind: resource.Text, ReadOnly: true},
		{Name: "notes", Column: "notes", Label: "Notes", Kind: resource.Text, Max: 2000}},
}

// MaintenanceSchedules are time- or usage-based maintenance plans (FR-AST-02).
var MaintenanceSchedules = &resource.Def{
	Key: "inventory.maintenance_schedule", Module: "inventory", Perm: "inventory.maintenance_schedule", Path: "/api/v1/inventory/maintenance-schedules",
	Table: "inventory.maintenance_schedules", Name: "Maintenance Schedule", SchemaName: "InventoryMaintenanceSchedule", Plural: "Maintenance Schedules", Tag: "Assets & Equipment",
	PropertyScoped: true, OrderBy: "next_due_on NULLS LAST, id",
	Fields: []resource.Field{
		{Name: "assetId", Column: "asset_id", Label: "Asset", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "inventory.assets", SameProperty: true, Label: "asset"}},
		resource.Name(),
		{Name: "triggerType", Column: "trigger_type", Label: "Based On", Kind: resource.Enum, Enum: []string{"time", "usage_hours"}, Default: "time"},
		{Name: "intervalDays", Column: "interval_days", Label: "Every (days)", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "intervalHours", Column: "interval_hours", Label: "Every (usage hours)", Kind: resource.Decimal, Min: resource.Min(0)},
		{Name: "lastDoneOn", Column: "last_done_on", Label: "Last Done", Kind: resource.Date},
		{Name: "lastDoneHours", Column: "last_done_hours", Label: "Usage Hours at Last Service", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "nextDueOn", Column: "next_due_on", Label: "Next Due", Kind: resource.Date},
		{Name: "reminderDaysBefore", Column: "reminder_days_before", Label: "Remind (days before)", Kind: resource.Int, Default: int64(7), Min: resource.Min(0)},
		resource.Status("active", "inactive")},
}

// P4Defs are the resource definitions added by PRD P4.
var P4Defs = []*resource.Def{Categories, Warehouses, StockLocations, ParStocks, ReorderPoints, ItemBarcodes, SupplierItems, AssetCategories, Assets,
	MaintenanceSchedules}

func init() {
	// The P2 item becomes the item master (FR-INV-01): additive fields only.
	for i := range Items.Fields {
		if Items.Fields[i].Name == "itemType" {
			Items.Fields[i].Enum = ItemTypes
		}
	}
	statusAt := len(Items.Fields) - 1
	extra := []resource.Field{
		{Name: "categoryId", Column: "category_id", Label: "Item Category", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "inventory.item_categories", SameProperty: true, Label: "category"}},
		{Name: "usageUomId", Column: "usage_uom_id", Label: "Usage UOM", Kind: resource.UUID, Ref: &resource.Ref{Table: "inventory.uoms", SameProperty: true, Label: "UOM"}},
		{Name: "barcode", Column: "barcode", Label: "Barcode", Kind: resource.String, Max: 64, Search: true},
		{Name: "valuationMethod", Column: "valuation_method", Label: "Valuation Method (empty = category)", Kind: resource.Enum, Enum: []string{"moving_average", "fifo"}},
		{Name: "trackBatch", Column: "track_batch", Label: "Track Batch", Kind: resource.Bool, Default: false},
		{Name: "trackSerial", Column: "track_serial", Label: "Track Serial Number", Kind: resource.Bool, Default: false},
		{Name: "trackExpiry", Column: "track_expiry", Label: "Track Expiry", Kind: resource.Bool, Default: false},
		{Name: "shelfLifeDays", Column: "shelf_life_days", Label: "Shelf Life (days)", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "storageCondition", Column: "storage_condition", Label: "Storage Condition", Kind: resource.Enum, Enum: []string{"ambient", "dry", "chilled", "frozen", "hazardous"}},
		{Name: "productId", Column: "product_id", Label: "Retail Product (sold 1:1)", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "commercial.products", SameProperty: true, Label: "product"}},
		{Name: "preferredSupplierId", Column: "preferred_supplier_id", Label: "Preferred Supplier", Kind: resource.UUID, Ref: supplierRef()},
		{Name: "consignment", Column: "consignment", Label: "Consignment (supplier-owned)", Kind: resource.Bool, Default: false, Filter: true},
		{Name: "consignmentSupplierId", Column: "consignment_supplier_id", Label: "Consignment Supplier", Kind: resource.UUID, Ref: supplierRef()},
		{Name: "consignmentCommissionPercent", Column: "consignment_commission_percent", Label: "Club Commission (%)", Kind: resource.Decimal,
			Min: resource.Min(0), MaxN: resource.Max(100)},
		{Name: "notes", Column: "notes", Label: "Notes", Kind: resource.Text, Max: 2000},
	}
	fields := append(append(append([]resource.Field{}, Items.Fields[:statusAt]...), extra...), Items.Fields[statusAt:]...)
	Items.Fields = fields
	Items.Tag = "Inventory Setup"

	Categories.Hooks.BeforeWrite = noCycle("inventory.item_categories", "parentId", "category")
	Warehouses.Hooks.BeforeWrite = noCycle("inventory.warehouses", "parentId", "warehouse")
	Items.Hooks.BeforeWrite = itemBeforeWrite
	Assets.Hooks.BeforeWrite = assetBeforeWrite
	Assets.Hooks.AfterCreate = func(ctx context.Context, tx pgx.Tx, row map[string]any) error {
		_, err := tx.Exec(ctx, `UPDATE inventory.assets SET accumulated_depreciation = opening_accumulated WHERE id = $1`, row["id"])
		return err
	}
	MaintenanceSchedules.Hooks.BeforeWrite = scheduleBeforeWrite

	for _, c := range []string{CategoryPolicies, CategoryConfiguration} {
		if !slices.Contains(rules.PolicyCategories, c) {
			rules.PolicyCategories = append(rules.PolicyCategories, c)
		}
	}
	rules.RegisterPolicy(rules.PolicyDef{Code: PolicyCode, Category: CategoryPolicies, Name: "Inventory Policies",
		Description: "Stock opname tolerance, adjustment & requisition approval thresholds, negative stock, expiry alert and slow-moving days, automatic purchase requisition",
		Default:     DefaultPolicy})
	rules.RegisterPolicy(rules.PolicyDef{Code: ConfigurationCode, Category: CategoryConfiguration, Name: "Inventory Configuration",
		Description: "Default valuation method, consumption locations of sales / packages / banquets / golf rounds, semi-finished stock, transit and spare part stores, depreciation",
		Default:     DefaultConfiguration})
}

// noCycle rejects a parent that is the row itself or one of its descendants.
func noCycle(table, field, label string) func(ctx context.Context, tx pgx.Tx, values, before map[string]any) error {
	return func(ctx context.Context, tx pgx.Tx, values, before map[string]any) error {
		parent, ok := values[field].(string)
		if !ok || parent == "" || before == nil {
			return nil
		}
		self := str(before["id"])
		if parent == self {
			return errs.Validation("invalid_parent", "a "+label+" cannot be its own parent", errs.Field(field, "invalid", "cannot be itself"))
		}
		var loop bool
		if err := tx.QueryRow(ctx, `WITH RECURSIVE up AS (SELECT id, parent_id, 1 AS depth FROM `+table+` WHERE id = $1::uuid
			UNION ALL SELECT t.id, t.parent_id, up.depth + 1 FROM `+table+` t JOIN up ON t.id = up.parent_id WHERE up.depth < 20)
			SELECT EXISTS (SELECT 1 FROM up WHERE id = $2::uuid)`, parent, self).Scan(&loop); err != nil {
			return err
		}
		if loop {
			return errs.Validation("invalid_parent", "the parent "+label+" is below this one", errs.Field(field, "cycle", "would create a cycle"))
		}
		return nil
	}
}

func itemBeforeWrite(ctx context.Context, tx pgx.Tx, values, before map[string]any) error {
	merged := map[string]any{}
	for k, v := range before {
		merged[k] = v
	}
	for k, v := range values {
		merged[k] = v
	}
	if merged["consignment"] == true && (merged["consignmentSupplierId"] == nil || str(merged["consignmentSupplierId"]) == "") {
		return errs.Validation("consignment_supplier_required", "a consignment item needs its supplier",
			errs.Field("consignmentSupplierId", "required", "required for consignment items"))
	}
	// The category label of P2 follows the category.
	if cid, ok := values["categoryId"].(string); ok && cid != "" {
		if _, set := values["category"]; !set {
			var name string
			if err := tx.QueryRow(ctx, `SELECT name FROM inventory.item_categories WHERE id = $1::uuid`, cid).Scan(&name); err == nil {
				values["category"] = name
			}
		}
	}
	if b, ok := values["barcode"].(string); ok && b != "" {
		pid, _ := reqctx.Property(ctx)
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM inventory.item_barcodes WHERE property_id = $1 AND barcode = $2)`, pid, b).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return errs.Validation("duplicate", "barcode already exists", errs.Field("barcode", "taken", "barcode already used by another item"))
		}
	}
	return nil
}

func assetBeforeWrite(_ context.Context, _ pgx.Tx, values, before map[string]any) error {
	if st, ok := values["status"].(string); ok && st == "disposed" && (before == nil || before["status"] != "disposed") {
		return errs.Validation("use_dispose", "assets are disposed through Dispose (approval)", errs.Field("status", "invalid", "use the Dispose action"))
	}
	if before != nil && before["status"] == "disposed" {
		return errs.Conflict("asset_disposed", "a disposed asset cannot be changed")
	}
	return nil
}

func scheduleBeforeWrite(_ context.Context, _ pgx.Tx, values, before map[string]any) error {
	merged := map[string]any{}
	for k, v := range before {
		merged[k] = v
	}
	for k, v := range values {
		merged[k] = v
	}
	if merged["triggerType"] == "usage_hours" && (merged["intervalHours"] == nil || str(merged["intervalHours"]) == "") {
		return errs.Validation("interval_required", "usage-based schedules need an hour interval", errs.Field("intervalHours", "required", "required"))
	}
	if _, set := values["nextDueOn"]; !set {
		last, _ := merged["lastDoneOn"].(string)
		if days := toInt(merged["intervalDays"]); last != "" && days > 0 {
			if d, err := parseDate(last); err == nil {
				values["nextDueOn"] = d.AddDate(0, 0, days).Format("2006-01-02")
			}
		}
	}
	return nil
}

// ── policies (EP-27) ──────────────────────────────────────────────────────

// Policy categories proposed by PRD P4 §7.6 (NC §33 pattern).
const (
	CategoryPolicies      = "Inventory Policies"
	CategoryConfiguration = "Inventory Configuration"
	PolicyCode            = "inventory.policy"
	ConfigurationCode     = "inventory.configuration"
)

// Policy is the Inventory Policies document (FR-POL-P4-01).
type Policy struct {
	OpnameTolerancePercent   string   `json:"opnameTolerancePercent" doc:"Variance tolerance (% of the line quantity) above which a stock opname needs approval; categories may override"`
	AdjustmentApprovalAbove  string   `json:"adjustmentApprovalAbove" doc:"Stock adjustments whose absolute value exceeds this amount need approval (0 = every valued adjustment)"`
	RequisitionApprovalAbove string   `json:"requisitionApprovalAbove" doc:"Store requisitions above this estimated value need approval (0 = every valued requisition)"`
	AllowNegativeStock       bool     `json:"allowNegativeStock" doc:"Allow negative stock in every warehouse (otherwise only warehouses that allow it)"`
	NegativeStockWarehouses  []string `json:"negativeStockWarehouses" doc:"Warehouse codes where negative stock is allowed and flagged for review"`
	ExpiryAlertDays          int      `json:"expiryAlertDays" doc:"Expiry warning N days before a batch expires (H-N)"`
	SlowMovingDays           int      `json:"slowMovingDays" doc:"Items without an outbound movement for this many days are slow moving"`
	AutoPurchaseRequisition  bool     `json:"autoPurchaseRequisition" doc:"The daily replenishment job requests purchase requisitions for items below their reorder point"`
	AutoStoreRequisition     bool     `json:"autoStoreRequisition" doc:"The daily replenishment job drafts store requisitions for sub-stores below par"`
	FreezeDuringOpname       bool     `json:"freezeDuringOpname" doc:"Default of new stock opnames: block manual movements of the warehouse while counting (otherwise a recorded cut-off)"`
	BlindCount               bool     `json:"blindCount" doc:"Default of new stock opnames: counters do not see the system quantity"`
}

// DefaultPolicy follows PRD P4 §16 #8 (tolerance 2% for F&B; categories
// carry their own tolerance) and the PRD P4 §8 acceptance criteria.
var DefaultPolicy = Policy{OpnameTolerancePercent: "2", AdjustmentApprovalAbove: "0", RequisitionApprovalAbove: "0", ExpiryAlertDays: 7,
	SlowMovingDays: 90, AutoPurchaseRequisition: true, AutoStoreRequisition: true, BlindCount: true, NegativeStockWarehouses: []string{}}

// Configuration is the Inventory Configuration document (FR-POL-P4-01).
type Configuration struct {
	ValuationMethod             string `json:"valuationMethod" doc:"moving_average or fifo: default of categories / items without their own"`
	Currency                    string `json:"currency"`
	ConsumeSemiFinishedStock    bool   `json:"consumeSemiFinishedStock" doc:"Sales consume produced semi-finished items (sub-recipes with an output item) instead of their ingredients"`
	DefaultConsumptionWarehouse string `json:"defaultConsumptionWarehouse" doc:"Warehouse code consumed when an outlet has no mapped warehouse ('' = posting exception)"`
	BanquetWarehouse            string `json:"banquetWarehouse" doc:"Warehouse code of banquet consumption when the event has no outlet"`
	PackageWarehouse            string `json:"packageWarehouse" doc:"Warehouse code of package consumption when the component has no outlet"`
	GolfRoundWarehouse          string `json:"golfRoundWarehouse" doc:"Warehouse code consumed by the service BOM of golf rounds"`
	GolfRoundServiceRef         string `json:"golfRoundServiceRef" doc:"Service reference of the golf round service BOM (recipe type service); <ref>.<holes> wins over <ref>"`
	SparePartWarehouse          string `json:"sparePartWarehouse" doc:"Engineering store issuing spare parts"`
	TransitWarehouse            string `json:"transitWarehouse" doc:"Code of the In Transit warehouse of stock transfers (created when missing)"`
	AutoDepreciation            bool   `json:"autoDepreciation" doc:"Post the monthly depreciation of the previous month automatically"`
	RefundRestock               bool   `json:"refundRestock" doc:"Refunded POS sales return their consumption to stock (commercial.sale_refunded; retail items 1:1 always, F&B per the next setting)"`
	RefundRestockPrepared       bool   `json:"refundRestockPrepared" doc:"Refunded F&B lines the kitchen had already started are returned to stock too (default: they stay consumed)"`
	PriceVarianceTo             string `json:"priceVarianceTo" doc:"cogs or price_variance: account of the invoice − receipt price difference of goods already used (FR-VAL-06)"`
}

// DefaultConfiguration: moving average for every category (PRD P4 §16 #8)
// and the Modern Golf structure of PRD P4 §7.5.
var DefaultConfiguration = Configuration{ValuationMethod: "moving_average", Currency: "IDR", ConsumeSemiFinishedStock: true, BanquetWarehouse: "KITCHEN",
	GolfRoundWarehouse: "GOLF-OPS", GolfRoundServiceRef: "golf.round", SparePartWarehouse: "ENGINEERING", TransitWarehouse: "TRANSIT",
	AutoDepreciation: true, RefundRestock: true, PriceVarianceTo: "cogs"}

// LoadPolicy returns the Inventory Policies in force.
func LoadPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (Policy, error) {
	p, _, err := rules.PolicyAt(ctx, q, PolicyCode, property, DefaultPolicy)
	return p, err
}

// LoadConfiguration returns the Inventory Configuration in force.
func LoadConfiguration(ctx context.Context, q dbtx.Querier, property uuid.UUID) (Configuration, error) {
	c, _, err := rules.PolicyAt(ctx, q, ConfigurationCode, property, DefaultConfiguration)
	if c.ValuationMethod != "fifo" {
		c.ValuationMethod = "moving_average"
	}
	if c.Currency == "" {
		c.Currency = "IDR"
	}
	if c.TransitWarehouse == "" {
		c.TransitWarehouse = "TRANSIT"
	}
	if c.PriceVarianceTo != "price_variance" {
		c.PriceVarianceTo = "cogs"
	}
	if c.GolfRoundServiceRef == "" {
		c.GolfRoundServiceRef = "golf.round"
	}
	return c, err
}

// ── approval document types ───────────────────────────────────────────────

var (
	// DocRequisition is the approval of store requisitions (FR-REQ-01).
	DocRequisition = provision.DocumentType{Code: "store_requisition", Module: "inventory", Name: "Store Requisition",
		Attributes: []provision.DocumentAttribute{{Key: "amount", Label: "Estimated Value", Type: "number"}, {Key: "requestType", Label: "Request Type", Type: "string"},
			{Key: "warehouse", Label: "Store (code)", Type: "string"}}}
	// DocAdjustment is the approval of stock adjustments (FR-OPN-03).
	DocAdjustment = provision.DocumentType{Code: "stock_adjustment", Module: "inventory", Name: "Stock Adjustment",
		Attributes: []provision.DocumentAttribute{{Key: "amount", Label: "Absolute Value", Type: "number"}, {Key: "reason", Label: "Reason", Type: "string"},
			{Key: "warehouse", Label: "Warehouse (code)", Type: "string"}}}
	// DocOpname is the approval of stock opname variances above tolerance (FR-OPN-02).
	DocOpname = provision.DocumentType{Code: "stock_opname", Module: "inventory", Name: "Stock Opname Variance",
		Attributes: []provision.DocumentAttribute{{Key: "amount", Label: "Absolute Variance Value", Type: "number"},
			{Key: "variancePercent", Label: "Largest Line Variance (%)", Type: "number"}, {Key: "warehouse", Label: "Warehouse (code)", Type: "string"}}}
	// DocAssetDisposal is the approval of asset disposal / write-off (FR-AST-06).
	DocAssetDisposal = provision.DocumentType{Code: "asset_disposal", Module: "inventory", Name: "Asset Disposal",
		Attributes: []provision.DocumentAttribute{{Key: "amount", Label: "Book Value", Type: "number"}, {Key: "assetClass", Label: "Asset Class", Type: "string"}}}
)

// DocumentTypes are the approval document types of PRD P4 inventory.
var DocumentTypes = []provision.DocumentType{DocRequisition, DocAdjustment, DocOpname, DocAssetDisposal}

// Templates are the inventory notification templates (expiry, maintenance).
func Templates() []provision.Template {
	t := map[string]map[string][2]string{
		"inventory.expiry_alert": {
			"en": {"{{.count}} batch(es) expire within {{.days}} days", "Batches expiring soon:\n{{.lines}}\nUse them first (FEFO) or record waste."},
			"id": {"{{.count}} batch kedaluwarsa dalam {{.days}} hari", "Batch yang segera kedaluwarsa:\n{{.lines}}\nGunakan lebih dahulu (FEFO) atau catat sebagai waste."},
		},
		"inventory.maintenance_due": {
			"en": {"Maintenance due: {{.asset}}", "{{.schedule}} of {{.asset}} is due on {{.dueOn}}."},
			"id": {"Jadwal maintenance: {{.asset}}", "{{.schedule}} untuk {{.asset}} jatuh tempo pada {{.dueOn}}."},
		},
		"inventory.low_stock": {
			"en": {"{{.count}} item(s) below reorder point at {{.warehouse}}", "Purchase requisition requested for:\n{{.lines}}"},
			"id": {"{{.count}} item di bawah reorder point di {{.warehouse}}", "Purchase requisition diajukan untuk:\n{{.lines}}"},
		},
	}
	var out []provision.Template
	for ev, locs := range t {
		for loc, c := range locs {
			for _, ch := range []string{"email", "in_app"} {
				out = append(out, provision.Template{Event: ev, Channel: ch, Locale: loc, Subject: c[0], Body: c[1]})
			}
		}
	}
	return out
}

// ── permissions (catalogue) ───────────────────────────────────────────────

// p4Actions are the document permissions of PRD P4 inventory.
var p4Actions = map[string][]string{
	"stock_balance":      {"view"},
	"stock_movement":     {"view", "reverse"},
	"requisition":        {"view", "create", "submit", "fulfill", "cancel", "request_purchase"},
	"issue":              {"create"},
	"transfer":           {"view", "create", "ship", "receive", "cancel"},
	"adjustment":         {"view", "create", "submit", "cancel"},
	"stock_opname":       {"view", "create", "count", "submit", "post", "cancel"},
	"production":         {"view", "create", "complete", "cancel"},
	"waste":              {"view", "create"},
	"valuation":          {"view"},
	"expiry":             {"view"},
	"replenishment":      {"view", "run"},
	"consignment":        {"view", "create", "settle"},
	"opening_stock":      {"import"},
	"asset":              {"dispose", "rent", "record_usage"},
	"maintenance":        {"view", "create", "update", "complete"},
	"spare_part_request": {"view", "create"},
	"depreciation":       {"view", "run"},
	"posting_exception":  {"view", "resolve"},
}

func perms(object string, actions ...string) []string {
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		out = append(out, "inventory."+object+"."+a)
	}
	return out
}

func join(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		for _, s := range l {
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

// P4Contribution adds the PRD P4 inventory permissions and maps them to the
// role templates of Product Overview §44 (P2 permissions are kept).
func P4Contribution() catalog.Contribution {
	defs := []*resource.Def{Categories, Warehouses, StockLocations, ParStocks, ReorderPoints, AssetCategories, Assets, MaintenanceSchedules}
	ps := resource.Permissions(defs...)
	var all []string
	all = append(all, resource.AllActions(defs...)...)
	keys := make([]string, 0, len(p4Actions))
	for k := range p4Actions {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, obj := range keys {
		ps = append(ps, catalog.P("inventory", obj, p4Actions[obj]...)...)
		all = append(all, perms(obj, p4Actions[obj]...)...)
	}
	p2 := resource.AllActions(UOMs, Items, Recipes)
	view := func(objs ...string) []string {
		var out []string
		for _, o := range objs {
			out = append(out, "inventory."+o+".view")
		}
		return out
	}
	setupView := view("item", "uom", "item_category", "warehouse", "stock_location", "par_stock", "reorder_point")
	docsView := view("stock_balance", "stock_movement", "requisition", "transfer", "adjustment", "stock_opname", "production", "waste", "expiry")
	financeView := join(setupView, docsView, view("recipe", "valuation", "replenishment", "consignment", "asset", "asset_category", "maintenance",
		"maintenance_schedule", "depreciation", "posting_exception", "food_cost"))
	assetView := view("asset", "asset_category", "maintenance", "maintenance_schedule", "spare_part_request")
	access := []string{catalog.ModuleAccess("inventory")}
	return catalog.Contribution{
		Permissions: ps,
		RolePermissions: map[string][]string{
			"property_admin":    all,
			"inventory_manager": join(all, p2),
			"warehouse_staff": join(setupView, docsView, view("replenishment", "consignment", "recipe"),
				perms("requisition", "create", "submit", "fulfill", "request_purchase"), perms("issue", "create"),
				perms("transfer", "create", "ship", "receive", "cancel"), perms("adjustment", "create", "submit"),
				perms("stock_opname", "create", "count", "submit"), perms("waste", "create"), perms("consignment", "create")),
			"kitchen_staff": join(access, view("stock_balance", "warehouse", "item_category", "requisition", "transfer", "production", "waste", "expiry", "stock_opname"),
				perms("requisition", "create", "submit", "cancel"), perms("transfer", "receive"), perms("production", "create", "complete", "cancel"),
				perms("waste", "create"), perms("stock_opname", "count")),
			"outlet_manager": join(p2, setupView, view("stock_balance", "stock_movement", "requisition", "transfer", "stock_opname", "production", "waste",
				"expiry", "replenishment"), perms("requisition", "create", "submit", "cancel"), perms("transfer", "receive"), perms("waste", "create"),
				perms("stock_opname", "count", "submit"), resource.AllActions(ParStocks)),
			"pos_staff": join(access, view("item", "stock_balance", "warehouse", "requisition", "waste"), perms("requisition", "create", "submit"),
				perms("waste", "create")),
			"golf_staff": join(access, assetView, view("item", "warehouse", "stock_balance"), perms("spare_part_request", "create")),
			"golf_manager": join(access, assetView, view("item", "warehouse", "stock_balance", "depreciation", "requisition"),
				resource.AllActions(Assets, MaintenanceSchedules), perms("maintenance", "create", "update", "complete"), perms("spare_part_request", "create"),
				perms("asset", "dispose", "rent", "record_usage")),
			"finance_manager": join(access, financeView, perms("depreciation", "run"), perms("consignment", "settle"), perms("posting_exception", "resolve"),
				resource.AllActions(AssetCategories), perms("asset", "dispose")),
			"accountant":          join(access, financeView, perms("depreciation", "run")),
			"general_manager":     join(financeView, view("spare_part_request")),
			"approver":            join(access, view("requisition", "adjustment", "stock_opname", "asset", "item", "warehouse")),
			"procurement_staff":   join(access, view("item", "item_category", "warehouse", "stock_balance", "replenishment", "reorder_point", "par_stock", "requisition")),
			"procurement_manager": join(access, view("item", "item_category", "warehouse", "stock_balance", "replenishment", "reorder_point", "par_stock", "requisition")),
		},
	}
}

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	if u, ok := v.(uuid.UUID); ok {
		return u.String()
	}
	return ""
}
