-- PRD P4 EP-01–09 Inventory (extends the P2 BOM foundation without
-- destructive changes, PRD P4 §6 #9): item master (categories, tracking,
-- barcodes, supplier items, consignment), warehouses & stock locations,
-- the append-only stock movement ledger with balances and FIFO cost layers,
-- batches / serials / expiry, store requisitions, stock transfers, stock
-- adjustments, stock opname, production orders, waste, par stock & reorder
-- points, consignment, assets & equipment with maintenance and
-- depreciation. The accounting period status (K10) is read through a guard
-- registered by internal/app; supplier references stay plain uuid
-- (procurement migrates after inventory).

-- +goose Up
-- ── item master ───────────────────────────────────────────────────────────
CREATE TABLE inventory.item_categories (
  id                        uuid PRIMARY KEY,
  property_id               uuid NOT NULL REFERENCES platform.properties (id),
  code                      text NOT NULL,
  name                      text NOT NULL,
  parent_id                 uuid REFERENCES inventory.item_categories (id),
  valuation_method          text NOT NULL DEFAULT 'moving_average' CHECK (valuation_method IN ('moving_average', 'fifo')),
  inventory_account         text,
  cogs_account              text,
  expense_account           text,
  waste_account             text,
  variance_account          text,
  opname_tolerance_percent  numeric(9,4) CHECK (opname_tolerance_percent IS NULL OR opname_tolerance_percent >= 0),
  status                    text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at                timestamptz NOT NULL DEFAULT now(),
  created_by                uuid,
  updated_at                timestamptz NOT NULL DEFAULT now(),
  updated_by                uuid,
  archived_at               timestamptz,
  UNIQUE (property_id, code),
  CHECK (parent_id IS NULL OR parent_id <> id)
);
SELECT platform.enable_property_rls('inventory.item_categories');
SELECT platform.add_touch_trigger('inventory.item_categories');

ALTER TABLE inventory.items
  ADD COLUMN category_id                     uuid REFERENCES inventory.item_categories (id),
  ADD COLUMN usage_uom_id                    uuid REFERENCES inventory.uoms (id),
  ADD COLUMN barcode                         text,
  ADD COLUMN valuation_method                text CHECK (valuation_method IS NULL OR valuation_method IN ('moving_average', 'fifo')),
  ADD COLUMN track_batch                     boolean NOT NULL DEFAULT false,
  ADD COLUMN track_serial                    boolean NOT NULL DEFAULT false,
  ADD COLUMN track_expiry                    boolean NOT NULL DEFAULT false,
  ADD COLUMN shelf_life_days                 int CHECK (shelf_life_days IS NULL OR shelf_life_days > 0),
  ADD COLUMN storage_condition               text CHECK (storage_condition IS NULL OR storage_condition IN ('ambient', 'dry', 'chilled', 'frozen', 'hazardous')),
  ADD COLUMN product_id                      uuid REFERENCES commercial.products (id),   -- retail product sold 1:1 (FR-INV-05)
  ADD COLUMN preferred_supplier_id           uuid,                                       -- procurement.suppliers (plain)
  ADD COLUMN consignment                     boolean NOT NULL DEFAULT false,             -- FR-INV-07: supplier-owned stock
  ADD COLUMN consignment_supplier_id         uuid,
  ADD COLUMN consignment_commission_percent  numeric(9,4) CHECK (consignment_commission_percent IS NULL OR
                                                                 (consignment_commission_percent >= 0 AND consignment_commission_percent <= 100)),
  ADD COLUMN notes                           text;
-- Item types widen (P2: raw, semi_finished, packaging, consumable).
ALTER TABLE inventory.items DROP CONSTRAINT items_item_type_check;
ALTER TABLE inventory.items ADD CONSTRAINT items_item_type_check CHECK (item_type IN ('raw', 'semi_finished', 'finished', 'packaging',
  'consumable', 'retail', 'spare_part', 'asset', 'service', 'non_stock'));
ALTER TABLE inventory.items ADD CONSTRAINT items_consignment_supplier CHECK (NOT consignment OR consignment_supplier_id IS NOT NULL);
CREATE UNIQUE INDEX items_barcode ON inventory.items (property_id, barcode) WHERE barcode IS NOT NULL;
CREATE UNIQUE INDEX items_product ON inventory.items (product_id) WHERE product_id IS NOT NULL AND archived_at IS NULL;

-- Additional barcodes, e.g. the carton barcode of a bottled item.
CREATE TABLE inventory.item_barcodes (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  item_id      uuid NOT NULL REFERENCES inventory.items (id),
  barcode      text NOT NULL,
  uom_id       uuid REFERENCES inventory.uoms (id),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  UNIQUE (property_id, barcode)
);
SELECT platform.enable_property_rls('inventory.item_barcodes');
SELECT platform.add_touch_trigger('inventory.item_barcodes');

CREATE TABLE inventory.supplier_items (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  item_id             uuid NOT NULL REFERENCES inventory.items (id),
  supplier_id         uuid NOT NULL,                     -- procurement.suppliers (plain)
  supplier_item_code  text,
  purchase_uom_id     uuid REFERENCES inventory.uoms (id),
  last_price          numeric(19,6) CHECK (last_price IS NULL OR last_price >= 0),
  lead_time_days      int CHECK (lead_time_days IS NULL OR lead_time_days >= 0),
  preferred           boolean NOT NULL DEFAULT false,
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid,
  updated_at          timestamptz NOT NULL DEFAULT now(),
  updated_by          uuid,
  UNIQUE (item_id, supplier_id)
);
SELECT platform.enable_property_rls('inventory.supplier_items');
SELECT platform.add_touch_trigger('inventory.supplier_items');

-- ── warehouses & stock locations (PRD P4 §7.5) ────────────────────────────
CREATE TABLE inventory.warehouses (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  code             text NOT NULL,
  name             text NOT NULL,
  parent_id        uuid REFERENCES inventory.warehouses (id),        -- the store that replenishes it
  location_type    text NOT NULL DEFAULT 'store' CHECK (location_type IN ('store', 'kitchen', 'outlet', 'transit', 'quarantine')),
  outlet_id        uuid REFERENCES commercial.outlets (id),           -- POS sales of the outlet consume here
  cost_center      text,
  allow_negative   boolean NOT NULL DEFAULT false,                    -- FR-STK-04
  keeper_user_ids  text[],                                            -- stock holders (ops lists)
  status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  updated_by       uuid,
  archived_at      timestamptz,
  UNIQUE (property_id, code),
  CHECK (parent_id IS NULL OR parent_id <> id)
);
SELECT platform.enable_property_rls('inventory.warehouses');
SELECT platform.add_touch_trigger('inventory.warehouses');

-- Bins / shelves / cold rooms inside a warehouse (put-away, count sheets).
CREATE TABLE inventory.stock_locations (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  warehouse_id   uuid NOT NULL REFERENCES inventory.warehouses (id),
  code           text NOT NULL,
  name           text NOT NULL,
  location_type  text NOT NULL DEFAULT 'bin' CHECK (location_type IN ('bin', 'shelf', 'cold_room', 'freezer', 'quarantine', 'staging')),
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid,
  archived_at    timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('inventory.stock_locations');
SELECT platform.add_touch_trigger('inventory.stock_locations');

-- ── replenishment rules (FR-RPL-01) ───────────────────────────────────────
CREATE TABLE inventory.par_stocks (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  item_id            uuid NOT NULL REFERENCES inventory.items (id),
  warehouse_id       uuid NOT NULL REFERENCES inventory.warehouses (id),
  par_level          numeric(19,6) NOT NULL CHECK (par_level >= 0),
  min_stock          numeric(19,6) NOT NULL DEFAULT 0 CHECK (min_stock >= 0),
  stock_location_id  uuid REFERENCES inventory.stock_locations (id),
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  updated_by         uuid,
  UNIQUE (warehouse_id, item_id)
);
SELECT platform.enable_property_rls('inventory.par_stocks');
SELECT platform.add_touch_trigger('inventory.par_stocks');

CREATE TABLE inventory.reorder_points (
  id                     uuid PRIMARY KEY,
  property_id            uuid NOT NULL REFERENCES platform.properties (id),
  item_id                uuid NOT NULL REFERENCES inventory.items (id),
  warehouse_id           uuid NOT NULL REFERENCES inventory.warehouses (id),
  reorder_point          numeric(19,6) NOT NULL CHECK (reorder_point >= 0),
  reorder_quantity       numeric(19,6) CHECK (reorder_quantity IS NULL OR reorder_quantity > 0),   -- fixed lot; else par − stock − on order
  preferred_supplier_id  uuid,
  lead_time_days         int CHECK (lead_time_days IS NULL OR lead_time_days >= 0),
  created_at             timestamptz NOT NULL DEFAULT now(),
  created_by             uuid,
  updated_at             timestamptz NOT NULL DEFAULT now(),
  updated_by             uuid,
  UNIQUE (warehouse_id, item_id)
);
SELECT platform.enable_property_rls('inventory.reorder_points');
SELECT platform.add_touch_trigger('inventory.reorder_points');

-- ── batches & serial numbers (FR-INV-06, FR-PRD-03/04) ────────────────────
CREATE TABLE inventory.batches (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  item_id            uuid NOT NULL REFERENCES inventory.items (id),
  batch_no           text NOT NULL,
  expiry_date        date,
  manufactured_date  date,
  supplier_id        uuid,
  received_at        timestamptz NOT NULL DEFAULT now(),
  created_at         timestamptz NOT NULL DEFAULT now(),
  UNIQUE (property_id, item_id, batch_no)
);
CREATE INDEX batches_expiry ON inventory.batches (property_id, expiry_date) WHERE expiry_date IS NOT NULL;
SELECT platform.enable_property_rls('inventory.batches');

CREATE TABLE inventory.serials (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  item_id           uuid NOT NULL REFERENCES inventory.items (id),
  serial_no         text NOT NULL,
  batch_id          uuid REFERENCES inventory.batches (id),
  warehouse_id      uuid REFERENCES inventory.warehouses (id),
  status            text NOT NULL DEFAULT 'in_stock' CHECK (status IN ('in_stock', 'in_transit', 'issued', 'sold', 'returned', 'written_off')),
  unit_cost         numeric(19,6) NOT NULL DEFAULT 0,
  received_at       timestamptz NOT NULL DEFAULT now(),
  last_movement_id  uuid,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  UNIQUE (property_id, item_id, serial_no)
);
SELECT platform.enable_property_rls('inventory.serials');
SELECT platform.add_touch_trigger('inventory.serials');

-- ── stock movement ledger (FR-STK-01, append-only) ────────────────────────
CREATE TABLE inventory.stock_movements (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  number                text NOT NULL,
  movement_type         text NOT NULL CHECK (movement_type IN ('receipt', 'issue', 'transfer_out', 'transfer_in', 'adjustment', 'opname',
                          'waste', 'production_in', 'production_out', 'consumption', 'return_out')),
  business_date         date NOT NULL,
  warehouse_id          uuid NOT NULL REFERENCES inventory.warehouses (id),
  counter_warehouse_id  uuid REFERENCES inventory.warehouses (id),
  cost_center           text,
  outlet_id             uuid,
  department_id         uuid,
  asset_id              uuid,
  supplier_id           uuid,
  source_type           text NOT NULL CHECK (source_type IN ('goods_receipt', 'requisition', 'transfer', 'adjustment', 'opname', 'waste',
                          'production', 'sale', 'package', 'banquet', 'golf_round', 'purchase_return', 'consignment', 'revaluation')),
  source_id             uuid,
  reversal_of           uuid REFERENCES inventory.stock_movements (id),
  reason                text,
  notes                 text,
  currency              char(3) NOT NULL DEFAULT 'IDR',
  total_cost            numeric(19,4) NOT NULL DEFAULT 0,
  flagged               boolean NOT NULL DEFAULT false,      -- negative stock posted by automatic consumption (review)
  posted_at             timestamptz NOT NULL DEFAULT now(),
  posted_by             uuid,
  seq                   bigint GENERATED ALWAYS AS IDENTITY,
  created_at            timestamptz NOT NULL DEFAULT now(),
  UNIQUE (property_id, number)
);
-- Idempotency per source (FR-CNS-05): one movement per source, type and warehouse.
CREATE UNIQUE INDEX stock_movements_source ON inventory.stock_movements (property_id, source_type, source_id, movement_type, warehouse_id)
  WHERE source_id IS NOT NULL AND reversal_of IS NULL;
CREATE UNIQUE INDEX stock_movements_reversal ON inventory.stock_movements (reversal_of) WHERE reversal_of IS NOT NULL;
CREATE INDEX stock_movements_date ON inventory.stock_movements (property_id, business_date);
CREATE INDEX stock_movements_warehouse ON inventory.stock_movements (warehouse_id, posted_at);
SELECT platform.enable_property_rls('inventory.stock_movements');

CREATE TABLE inventory.stock_movement_lines (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  movement_id        uuid NOT NULL REFERENCES inventory.stock_movements (id),
  line_no            int NOT NULL,
  item_id            uuid NOT NULL REFERENCES inventory.items (id),
  batch_id           uuid REFERENCES inventory.batches (id),
  serial_nos         text[],
  stock_location_id  uuid REFERENCES inventory.stock_locations (id),
  quantity           numeric(19,6) NOT NULL CHECK (quantity <> 0),   -- signed, item base UOM
  unit_cost          numeric(19,6) NOT NULL DEFAULT 0,
  total_cost         numeric(19,4) NOT NULL DEFAULT 0,               -- signed
  consignment        boolean NOT NULL DEFAULT false,
  balance_after      numeric(19,6) NOT NULL,                         -- item quantity in the warehouse after the line
  value_after        numeric(19,4) NOT NULL,
  negative           boolean NOT NULL DEFAULT false,
  seq                bigint GENERATED ALWAYS AS IDENTITY,
  created_at         timestamptz NOT NULL DEFAULT now(),
  UNIQUE (movement_id, line_no)
);
CREATE INDEX stock_movement_lines_item ON inventory.stock_movement_lines (item_id, seq);
SELECT platform.enable_property_rls('inventory.stock_movement_lines');

-- +goose StatementBegin
CREATE FUNCTION inventory.forbid_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION '% is append-only (corrections are new movements)', TG_TABLE_NAME USING ERRCODE = 'insufficient_privilege';
END $$;
-- +goose StatementEnd
CREATE TRIGGER stock_movements_append_only BEFORE UPDATE OR DELETE ON inventory.stock_movements
  FOR EACH ROW EXECUTE FUNCTION inventory.forbid_change();
CREATE TRIGGER stock_movement_lines_append_only BEFORE UPDATE OR DELETE ON inventory.stock_movement_lines
  FOR EACH ROW EXECUTE FUNCTION inventory.forbid_change();

-- Balance per warehouse × item × batch = Σ movement lines (FR-STK-02).
CREATE TABLE inventory.stock_balances (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  warehouse_id      uuid NOT NULL REFERENCES inventory.warehouses (id),
  item_id           uuid NOT NULL REFERENCES inventory.items (id),
  batch_id          uuid REFERENCES inventory.batches (id),
  quantity          numeric(19,6) NOT NULL DEFAULT 0,
  value             numeric(19,4) NOT NULL DEFAULT 0,
  last_movement_at  timestamptz,
  updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX stock_balances_key ON inventory.stock_balances (warehouse_id, item_id, coalesce(batch_id, '00000000-0000-0000-0000-000000000000'::uuid));
CREATE INDEX stock_balances_item ON inventory.stock_balances (property_id, item_id);
SELECT platform.enable_property_rls('inventory.stock_balances');

-- FIFO cost layers (FR-VAL-01).
CREATE TABLE inventory.cost_layers (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  warehouse_id      uuid NOT NULL REFERENCES inventory.warehouses (id),
  item_id           uuid NOT NULL REFERENCES inventory.items (id),
  batch_id          uuid REFERENCES inventory.batches (id),
  movement_line_id  uuid NOT NULL REFERENCES inventory.stock_movement_lines (id),
  received_at       timestamptz NOT NULL,
  quantity          numeric(19,6) NOT NULL CHECK (quantity > 0),
  remaining         numeric(19,6) NOT NULL CHECK (remaining >= 0),
  unit_cost         numeric(19,6) NOT NULL,
  updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX cost_layers_open ON inventory.cost_layers (warehouse_id, item_id, received_at, id) WHERE remaining > 0;
SELECT platform.enable_property_rls('inventory.cost_layers');

-- Events that could not be posted (unknown item / warehouse): no event is lost.
CREATE TABLE inventory.posting_exceptions (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  event_type   text NOT NULL,
  event_id     uuid,
  source_type  text NOT NULL,
  source_id    uuid,
  item_id      uuid,
  warehouse_id uuid,
  quantity     numeric(19,6),
  reason       text NOT NULL,
  status       text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved')),
  resolved_at  timestamptz,
  resolved_by  uuid,
  resolution   text,
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX posting_exceptions_open ON inventory.posting_exceptions (property_id, created_at) WHERE status = 'open';
SELECT platform.enable_property_rls('inventory.posting_exceptions');

-- ── assets & equipment (EP-09) ────────────────────────────────────────────
CREATE TABLE inventory.asset_categories (
  id                      uuid PRIMARY KEY,
  property_id             uuid NOT NULL REFERENCES platform.properties (id),
  code                    text NOT NULL,
  name                    text NOT NULL,
  asset_class             text NOT NULL DEFAULT 'other' CHECK (asset_class IN ('golf_cart', 'gym_equipment', 'rental_equipment',
                            'banquet_equipment', 'maintenance_equipment', 'it_equipment', 'vehicle', 'furniture', 'other')),
  depreciation_method     text NOT NULL DEFAULT 'straight_line' CHECK (depreciation_method IN ('straight_line', 'declining_balance')),
  useful_life_months      int NOT NULL DEFAULT 60 CHECK (useful_life_months > 0),
  residual_percent        numeric(9,4) NOT NULL DEFAULT 0 CHECK (residual_percent >= 0 AND residual_percent < 100),
  declining_rate_percent  numeric(9,4) CHECK (declining_rate_percent IS NULL OR (declining_rate_percent > 0 AND declining_rate_percent <= 100)),
  asset_account           text,
  accumulated_account     text,
  expense_account         text,
  status                  text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at              timestamptz NOT NULL DEFAULT now(),
  created_by              uuid,
  updated_at              timestamptz NOT NULL DEFAULT now(),
  updated_by              uuid,
  archived_at             timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('inventory.asset_categories');
SELECT platform.add_touch_trigger('inventory.asset_categories');

CREATE TABLE inventory.assets (
  id                        uuid PRIMARY KEY,
  property_id               uuid NOT NULL REFERENCES platform.properties (id),
  code                      text NOT NULL,
  name                      text NOT NULL,
  category_id               uuid NOT NULL REFERENCES inventory.asset_categories (id),
  serial_no                 text,
  brand                     text,
  model                     text,
  warehouse_id              uuid REFERENCES inventory.warehouses (id),     -- location
  department_id             uuid REFERENCES platform.departments (id),
  cost_center               text,
  acquisition_date          date,
  acquisition_cost          numeric(19,4) NOT NULL DEFAULT 0 CHECK (acquisition_cost >= 0),
  residual_value            numeric(19,4) NOT NULL DEFAULT 0 CHECK (residual_value >= 0),
  useful_life_months        int CHECK (useful_life_months IS NULL OR useful_life_months > 0),
  depreciation_method       text CHECK (depreciation_method IS NULL OR depreciation_method IN ('straight_line', 'declining_balance')),
  depreciation_start        date,
  opening_accumulated       numeric(19,4) NOT NULL DEFAULT 0 CHECK (opening_accumulated >= 0),   -- migrated assets (FR-MIG-P4-05)
  accumulated_depreciation  numeric(19,4) NOT NULL DEFAULT 0,
  book_value                numeric(19,4) GENERATED ALWAYS AS (acquisition_cost - accumulated_depreciation) STORED,
  supplier_id               uuid,
  warranty_until            date,
  golf_cart_ref             uuid,                                          -- golf.golf_carts (P2), plain reference
  rentable                  boolean NOT NULL DEFAULT false,
  rental_status             text NOT NULL DEFAULT 'available' CHECK (rental_status IN ('available', 'out')),
  usage_hours               numeric(19,2) NOT NULL DEFAULT 0,
  status                    text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'maintenance', 'out_of_service', 'disposed')),
  disposed_on               date,
  disposal_reason           text,
  disposal_proceeds         numeric(19,4),
  disposal_approval_id      uuid,
  notes                     text,
  created_at                timestamptz NOT NULL DEFAULT now(),
  created_by                uuid,
  updated_at                timestamptz NOT NULL DEFAULT now(),
  updated_by                uuid,
  archived_at               timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('inventory.assets');
SELECT platform.add_touch_trigger('inventory.assets');

CREATE TABLE inventory.maintenance_schedules (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  asset_id              uuid NOT NULL REFERENCES inventory.assets (id),
  name                  text NOT NULL,
  trigger_type          text NOT NULL DEFAULT 'time' CHECK (trigger_type IN ('time', 'usage_hours')),
  interval_days         int CHECK (interval_days IS NULL OR interval_days > 0),
  interval_hours        numeric(19,2) CHECK (interval_hours IS NULL OR interval_hours > 0),
  last_done_on          date,
  last_done_hours       numeric(19,2) NOT NULL DEFAULT 0,
  next_due_on           date,
  reminder_days_before  int NOT NULL DEFAULT 7 CHECK (reminder_days_before >= 0),
  last_reminded_on      date,
  status                text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid
);
SELECT platform.enable_property_rls('inventory.maintenance_schedules');
SELECT platform.add_touch_trigger('inventory.maintenance_schedules');

CREATE TABLE inventory.maintenance_records (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  number            text NOT NULL,
  asset_id          uuid NOT NULL REFERENCES inventory.assets (id),
  schedule_id       uuid REFERENCES inventory.maintenance_schedules (id),
  maintenance_type  text NOT NULL DEFAULT 'corrective' CHECK (maintenance_type IN ('preventive', 'corrective', 'inspection')),
  description       text NOT NULL,
  status            text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'in_progress', 'completed', 'cancelled')),
  scheduled_on      date,
  started_at        timestamptz,
  completed_at      timestamptz,
  usage_hours_at    numeric(19,2),
  labor_cost        numeric(19,4) NOT NULL DEFAULT 0,
  vendor_cost       numeric(19,4) NOT NULL DEFAULT 0,
  parts_cost        numeric(19,4) NOT NULL DEFAULT 0,
  requisition_id    uuid,
  notes             text,
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('inventory.maintenance_records');
SELECT platform.add_touch_trigger('inventory.maintenance_records');

-- Usage history: rentals, operating hours (FR-AST-03/04).
CREATE TABLE inventory.asset_usages (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  asset_id      uuid NOT NULL REFERENCES inventory.assets (id),
  usage_type    text NOT NULL CHECK (usage_type IN ('rental', 'operation')),
  started_at    timestamptz NOT NULL,
  ended_at      timestamptz,
  hours         numeric(19,2),
  customer_ref  text,
  reference     text,
  notes         text,
  created_by    uuid,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX asset_usages_asset ON inventory.asset_usages (asset_id, started_at);
SELECT platform.enable_property_rls('inventory.asset_usages');
SELECT platform.add_touch_trigger('inventory.asset_usages');

CREATE TABLE inventory.depreciation_runs (
  id          uuid PRIMARY KEY,
  property_id uuid NOT NULL REFERENCES platform.properties (id),
  number      text NOT NULL,
  period      text NOT NULL CHECK (period ~ '^\d{4}-\d{2}$'),
  period_end  date NOT NULL,
  currency    char(3) NOT NULL DEFAULT 'IDR',
  total       numeric(19,4) NOT NULL DEFAULT 0,
  assets      int NOT NULL DEFAULT 0,
  status      text NOT NULL DEFAULT 'posted' CHECK (status IN ('posted')),
  posted_by   uuid,
  posted_at   timestamptz NOT NULL DEFAULT now(),
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (property_id, period),
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('inventory.depreciation_runs');

CREATE TABLE inventory.depreciation_lines (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  run_id             uuid NOT NULL REFERENCES inventory.depreciation_runs (id),
  asset_id           uuid NOT NULL REFERENCES inventory.assets (id),
  amount             numeric(19,4) NOT NULL CHECK (amount > 0),
  accumulated_after  numeric(19,4) NOT NULL,
  book_value_after   numeric(19,4) NOT NULL,
  created_at         timestamptz NOT NULL DEFAULT now(),
  UNIQUE (run_id, asset_id)
);
SELECT platform.enable_property_rls('inventory.depreciation_lines');
CREATE TRIGGER depreciation_runs_append_only BEFORE UPDATE OR DELETE ON inventory.depreciation_runs
  FOR EACH ROW EXECUTE FUNCTION inventory.forbid_change();
CREATE TRIGGER depreciation_lines_append_only BEFORE UPDATE OR DELETE ON inventory.depreciation_lines
  FOR EACH ROW EXECUTE FUNCTION inventory.forbid_change();

-- ── documents ─────────────────────────────────────────────────────────────
-- Store Requisition (FR-REQ-01/02), direct issues and spare part requests.
CREATE TABLE inventory.requisitions (
  id                       uuid PRIMARY KEY,
  property_id              uuid NOT NULL REFERENCES platform.properties (id),
  number                   text NOT NULL,
  request_type             text NOT NULL DEFAULT 'store' CHECK (request_type IN ('store', 'department', 'spare_part', 'issue')),
  origin                   text NOT NULL DEFAULT 'manual' CHECK (origin IN ('manual', 'ops', 'replenishment', 'maintenance')),
  source_warehouse_id      uuid NOT NULL REFERENCES inventory.warehouses (id),
  requesting_warehouse_id  uuid REFERENCES inventory.warehouses (id),
  cost_center              text,
  department_id            uuid REFERENCES platform.departments (id),
  asset_id                 uuid REFERENCES inventory.assets (id),
  maintenance_record_id    uuid REFERENCES inventory.maintenance_records (id),
  needed_by                date,
  status                   text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'submitted', 'approved', 'rejected', 'partially_issued',
                             'issued', 'closed', 'cancelled')),
  approval_request_id      uuid,
  estimated_value          numeric(19,4) NOT NULL DEFAULT 0,
  reason                   text,
  notes                    text,
  rejection_reason         text,
  purchase_requested_at    timestamptz,
  requested_by             uuid,
  submitted_at             timestamptz,
  approved_at              timestamptz,
  closed_at                timestamptz,
  created_at               timestamptz NOT NULL DEFAULT now(),
  created_by               uuid,
  updated_at               timestamptz NOT NULL DEFAULT now(),
  updated_by               uuid,
  UNIQUE (property_id, number),
  CHECK (requesting_warehouse_id IS NULL OR requesting_warehouse_id <> source_warehouse_id)
);
CREATE INDEX requisitions_status ON inventory.requisitions (property_id, status);
SELECT platform.enable_property_rls('inventory.requisitions');
SELECT platform.add_touch_trigger('inventory.requisitions');

CREATE TABLE inventory.requisition_lines (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  requisition_id      uuid NOT NULL REFERENCES inventory.requisitions (id),
  line_no             int NOT NULL,
  item_id             uuid NOT NULL REFERENCES inventory.items (id),
  uom_id              uuid REFERENCES inventory.uoms (id),
  quantity            numeric(19,6) NOT NULL CHECK (quantity > 0),          -- entered UOM
  base_quantity       numeric(19,6) NOT NULL CHECK (base_quantity > 0),     -- item base UOM
  issued_quantity     numeric(19,6) NOT NULL DEFAULT 0 CHECK (issued_quantity >= 0),
  cancelled_quantity  numeric(19,6) NOT NULL DEFAULT 0 CHECK (cancelled_quantity >= 0),
  notes               text,
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now(),
  UNIQUE (requisition_id, line_no)
);
SELECT platform.enable_property_rls('inventory.requisition_lines');
SELECT platform.add_touch_trigger('inventory.requisition_lines');

-- One row per fulfilment (partial or full): the source of its movements.
CREATE TABLE inventory.requisition_issues (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  requisition_id  uuid NOT NULL REFERENCES inventory.requisitions (id),
  issued_at       timestamptz NOT NULL DEFAULT now(),
  issued_by       uuid,
  total_cost      numeric(19,4) NOT NULL DEFAULT 0,
  notes           text
);
SELECT platform.enable_property_rls('inventory.requisition_issues');

-- Stock Transfer (FR-REQ-03): in transit through the property's transit warehouse.
CREATE TABLE inventory.transfers (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  number                text NOT NULL,
  from_warehouse_id     uuid NOT NULL REFERENCES inventory.warehouses (id),
  to_warehouse_id       uuid NOT NULL REFERENCES inventory.warehouses (id),
  transit_warehouse_id  uuid REFERENCES inventory.warehouses (id),
  status                text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'in_transit', 'received', 'cancelled')),
  requisition_id        uuid REFERENCES inventory.requisitions (id),
  notes                 text,
  shipped_at            timestamptz,
  shipped_by            uuid,
  received_at           timestamptz,
  received_by           uuid,
  shipped_value         numeric(19,4) NOT NULL DEFAULT 0,
  discrepancy_value     numeric(19,4) NOT NULL DEFAULT 0,
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid,
  UNIQUE (property_id, number),
  CHECK (from_warehouse_id <> to_warehouse_id)
);
SELECT platform.enable_property_rls('inventory.transfers');
SELECT platform.add_touch_trigger('inventory.transfers');

CREATE TABLE inventory.transfer_lines (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  transfer_id         uuid NOT NULL REFERENCES inventory.transfers (id),
  line_no             int NOT NULL,
  item_id             uuid NOT NULL REFERENCES inventory.items (id),
  batch_id            uuid REFERENCES inventory.batches (id),
  serial_nos          text[],
  quantity            numeric(19,6) NOT NULL CHECK (quantity > 0),
  shipped_quantity    numeric(19,6) NOT NULL DEFAULT 0,
  received_quantity   numeric(19,6) NOT NULL DEFAULT 0,
  shipped_value       numeric(19,4) NOT NULL DEFAULT 0,
  discrepancy_reason  text,
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now(),
  UNIQUE (transfer_id, line_no)
);
SELECT platform.enable_property_rls('inventory.transfer_lines');
SELECT platform.add_touch_trigger('inventory.transfer_lines');

-- Stock Adjustment with reason & approval (FR-OPN-03); opening balances.
CREATE TABLE inventory.adjustments (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  warehouse_id         uuid NOT NULL REFERENCES inventory.warehouses (id),
  reason               text NOT NULL CHECK (reason IN ('damage', 'expired', 'theft', 'count_correction', 'found', 'opening_balance', 'other')),
  status               text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'pending_approval', 'posted', 'rejected', 'cancelled')),
  business_date        date,
  approval_request_id  uuid,
  movement_id          uuid REFERENCES inventory.stock_movements (id),
  total_value          numeric(19,4) NOT NULL DEFAULT 0,
  notes                text,
  rejection_reason     text,
  submitted_at         timestamptz,
  posted_at            timestamptz,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('inventory.adjustments');
SELECT platform.add_touch_trigger('inventory.adjustments');

CREATE TABLE inventory.adjustment_lines (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  adjustment_id  uuid NOT NULL REFERENCES inventory.adjustments (id),
  line_no        int NOT NULL,
  item_id        uuid NOT NULL REFERENCES inventory.items (id),
  quantity       numeric(19,6) NOT NULL CHECK (quantity <> 0),       -- signed, base UOM
  unit_cost      numeric(19,6) CHECK (unit_cost IS NULL OR unit_cost >= 0),
  batch_no       text,
  expiry_date    date,
  serial_nos     text[],
  notes          text,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (adjustment_id, line_no)
);
SELECT platform.enable_property_rls('inventory.adjustment_lines');
SELECT platform.add_touch_trigger('inventory.adjustment_lines');

-- Stock Opname (FR-OPN-01/02/04): snapshot, blind count, variance, approval.
CREATE TABLE inventory.stock_opnames (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  warehouse_id         uuid NOT NULL REFERENCES inventory.warehouses (id),
  category_id          uuid REFERENCES inventory.item_categories (id),
  stock_location_id    uuid REFERENCES inventory.stock_locations (id),
  blind                boolean NOT NULL DEFAULT true,
  freeze_movements     boolean NOT NULL DEFAULT false,
  status               text NOT NULL DEFAULT 'in_progress' CHECK (status IN ('in_progress', 'counted', 'pending_approval', 'posted', 'cancelled')),
  snapshot_at          timestamptz NOT NULL DEFAULT now(),
  counted_at           timestamptz,
  posted_at            timestamptz,
  approval_request_id  uuid,
  movement_id          uuid REFERENCES inventory.stock_movements (id),
  tolerance_percent    numeric(9,4) NOT NULL DEFAULT 0,
  system_value         numeric(19,4) NOT NULL DEFAULT 0,
  variance_value       numeric(19,4) NOT NULL DEFAULT 0,
  notes                text,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('inventory.stock_opnames');
SELECT platform.add_touch_trigger('inventory.stock_opnames');

CREATE TABLE inventory.stock_opname_lines (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  opname_id          uuid NOT NULL REFERENCES inventory.stock_opnames (id),
  item_id            uuid NOT NULL REFERENCES inventory.items (id),
  batch_id           uuid REFERENCES inventory.batches (id),
  system_quantity    numeric(19,6) NOT NULL DEFAULT 0,      -- snapshot at start
  counted_quantity   numeric(19,6),
  counted_at         timestamptz,
  counted_by         uuid,
  expected_quantity  numeric(19,6),                         -- snapshot + movements until the count (cut-off)
  variance_quantity  numeric(19,6),
  unit_cost          numeric(19,6) NOT NULL DEFAULT 0,
  variance_value     numeric(19,4),
  tolerance_percent  numeric(9,4) NOT NULL DEFAULT 0,
  over_tolerance     boolean NOT NULL DEFAULT false,
  recounts           int NOT NULL DEFAULT 0,
  notes              text,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX stock_opname_lines_key ON inventory.stock_opname_lines (opname_id, item_id,
  coalesce(batch_id, '00000000-0000-0000-0000-000000000000'::uuid));
SELECT platform.enable_property_rls('inventory.stock_opname_lines');
SELECT platform.add_touch_trigger('inventory.stock_opname_lines');

-- Production Order for semi-finished items (FR-PRD-01).
CREATE TABLE inventory.production_orders (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  number            text NOT NULL,
  recipe_id         uuid NOT NULL REFERENCES inventory.recipes (id),
  output_item_id    uuid NOT NULL REFERENCES inventory.items (id),
  warehouse_id      uuid NOT NULL REFERENCES inventory.warehouses (id),
  planned_quantity  numeric(19,6) NOT NULL CHECK (planned_quantity > 0),     -- output item base UOM
  actual_quantity   numeric(19,6),
  status            text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'completed', 'cancelled')),
  scheduled_for     date,
  source_type       text CHECK (source_type IS NULL OR source_type IN ('banquet_event')),   -- FR-PRD-05: scheduled from a BEO
  source_id         uuid,
  source_ref        text,
  batch_id          uuid REFERENCES inventory.batches (id),
  input_cost        numeric(19,4) NOT NULL DEFAULT 0,
  output_unit_cost  numeric(19,6) NOT NULL DEFAULT 0,
  notes             text,
  completed_at      timestamptz,
  completed_by      uuid,
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  UNIQUE (property_id, number)
);
CREATE UNIQUE INDEX production_orders_source ON inventory.production_orders (source_type, source_id, output_item_id)
  WHERE source_id IS NOT NULL AND status <> 'cancelled';
SELECT platform.enable_property_rls('inventory.production_orders');
SELECT platform.add_touch_trigger('inventory.production_orders');

CREATE TABLE inventory.production_inputs (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  production_order_id  uuid NOT NULL REFERENCES inventory.production_orders (id),
  item_id              uuid NOT NULL REFERENCES inventory.items (id),
  planned_quantity     numeric(19,6) NOT NULL,
  actual_quantity      numeric(19,6),
  total_cost           numeric(19,4) NOT NULL DEFAULT 0,
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  UNIQUE (production_order_id, item_id)
);
SELECT platform.enable_property_rls('inventory.production_inputs');
SELECT platform.add_touch_trigger('inventory.production_inputs');

-- Record Waste / Spoilage (FR-PRD-02); the lines are the movement lines.
CREATE TABLE inventory.waste_records (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  number         text NOT NULL,
  warehouse_id   uuid NOT NULL REFERENCES inventory.warehouses (id),
  reason         text NOT NULL CHECK (reason IN ('expired', 'damaged', 'spoiled', 'wrong_preparation', 'overproduction', 'breakage', 'other')),
  business_date  date NOT NULL,
  movement_id    uuid REFERENCES inventory.stock_movements (id),
  total_cost     numeric(19,4) NOT NULL DEFAULT 0,
  notes          text,
  recorded_by    uuid,
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('inventory.waste_records');

-- ── replenishment (FR-RPL-02) ─────────────────────────────────────────────
-- Purchase requests sent to procurement (inventory.reorder_needed): one per
-- warehouse × item × business day; open quantity counts as "on order".
CREATE TABLE inventory.reorder_requests (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  warehouse_id        uuid NOT NULL REFERENCES inventory.warehouses (id),
  item_id             uuid NOT NULL REFERENCES inventory.items (id),
  business_date       date NOT NULL,
  on_hand             numeric(19,6) NOT NULL,
  reorder_point       numeric(19,6) NOT NULL,
  par_level           numeric(19,6) NOT NULL,
  suggested_quantity  numeric(19,6) NOT NULL CHECK (suggested_quantity > 0),
  received_quantity   numeric(19,6) NOT NULL DEFAULT 0,
  status              text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'received', 'cancelled')),
  source              text NOT NULL DEFAULT 'reorder' CHECK (source IN ('reorder', 'requisition')),
  requisition_id      uuid REFERENCES inventory.requisitions (id),
  event_id            uuid,
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX reorder_requests_day ON inventory.reorder_requests (warehouse_id, item_id, business_date) WHERE source = 'reorder';
SELECT platform.enable_property_rls('inventory.reorder_requests');
SELECT platform.add_touch_trigger('inventory.reorder_requests');

-- Expiry alerts sent (H-N, once per batch × warehouse).
CREATE TABLE inventory.expiry_alerts (
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  batch_id      uuid NOT NULL REFERENCES inventory.batches (id),
  warehouse_id  uuid NOT NULL REFERENCES inventory.warehouses (id),
  quantity      numeric(19,6) NOT NULL,
  expiry_date   date NOT NULL,
  alerted_on    date NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (batch_id, warehouse_id)
);
SELECT platform.enable_property_rls('inventory.expiry_alerts');

-- ── consignment (FR-INV-07, PRD P4 §16 #4) ────────────────────────────────
CREATE TABLE inventory.consignment_settlements (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  number             text NOT NULL,
  supplier_id        uuid NOT NULL,
  period             text NOT NULL CHECK (period ~ '^\d{4}-\d{2}$'),
  quantity           numeric(19,6) NOT NULL DEFAULT 0,
  sales_amount       numeric(19,4) NOT NULL DEFAULT 0,
  commission_amount  numeric(19,4) NOT NULL DEFAULT 0,
  payable_amount     numeric(19,4) NOT NULL DEFAULT 0,
  currency           char(3) NOT NULL DEFAULT 'IDR',
  status             text NOT NULL DEFAULT 'issued' CHECK (status IN ('issued')),
  notes              text,
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  UNIQUE (property_id, supplier_id, period),
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('inventory.consignment_settlements');

CREATE TABLE inventory.consignment_sales (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  supplier_id         uuid NOT NULL,
  item_id             uuid NOT NULL REFERENCES inventory.items (id),
  warehouse_id        uuid NOT NULL REFERENCES inventory.warehouses (id),
  movement_id         uuid NOT NULL REFERENCES inventory.stock_movements (id),
  source_type         text NOT NULL,
  source_id           uuid,
  quantity            numeric(19,6) NOT NULL,
  sales_amount        numeric(19,4) NOT NULL DEFAULT 0,
  commission_percent  numeric(9,4) NOT NULL DEFAULT 0,
  commission_amount   numeric(19,4) NOT NULL DEFAULT 0,
  unit_cost           numeric(19,6) NOT NULL DEFAULT 0,       -- payable per unit
  payable_amount      numeric(19,4) NOT NULL DEFAULT 0,
  currency            char(3) NOT NULL DEFAULT 'IDR',
  business_date       date NOT NULL,
  settlement_id       uuid REFERENCES inventory.consignment_settlements (id),
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now(),
  UNIQUE (movement_id, item_id)
);
SELECT platform.enable_property_rls('inventory.consignment_sales');
SELECT platform.add_touch_trigger('inventory.consignment_sales');

SELECT platform.grant_app('inventory');

-- +goose Down
DROP TABLE inventory.consignment_sales;
DROP TABLE inventory.consignment_settlements;
DROP TABLE inventory.expiry_alerts;
DROP TABLE inventory.reorder_requests;
DROP TABLE inventory.waste_records;
DROP TABLE inventory.production_inputs;
DROP TABLE inventory.production_orders;
DROP TABLE inventory.stock_opname_lines;
DROP TABLE inventory.stock_opnames;
DROP TABLE inventory.adjustment_lines;
DROP TABLE inventory.adjustments;
DROP TABLE inventory.transfer_lines;
DROP TABLE inventory.transfers;
DROP TABLE inventory.requisition_issues;
DROP TABLE inventory.requisition_lines;
DROP TABLE inventory.requisitions;
DROP TABLE inventory.depreciation_lines;
DROP TABLE inventory.depreciation_runs;
DROP TABLE inventory.asset_usages;
DROP TABLE inventory.maintenance_records;
DROP TABLE inventory.maintenance_schedules;
DROP TABLE inventory.assets;
DROP TABLE inventory.asset_categories;
DROP TABLE inventory.posting_exceptions;
DROP TABLE inventory.cost_layers;
DROP TABLE inventory.stock_balances;
DROP TABLE inventory.stock_movement_lines;
DROP TABLE inventory.stock_movements;
DROP FUNCTION inventory.forbid_change();
DROP TABLE inventory.serials;
DROP TABLE inventory.batches;
DROP TABLE inventory.reorder_points;
DROP TABLE inventory.par_stocks;
DROP TABLE inventory.stock_locations;
DROP TABLE inventory.warehouses;
DROP TABLE inventory.supplier_items;
DROP TABLE inventory.item_barcodes;
DROP INDEX inventory.items_product;
DROP INDEX inventory.items_barcode;
ALTER TABLE inventory.items DROP CONSTRAINT items_consignment_supplier;
ALTER TABLE inventory.items DROP CONSTRAINT items_item_type_check;
ALTER TABLE inventory.items ADD CONSTRAINT items_item_type_check CHECK (item_type IN ('raw', 'semi_finished', 'packaging', 'consumable'));
ALTER TABLE inventory.items DROP COLUMN notes, DROP COLUMN consignment_commission_percent, DROP COLUMN consignment_supplier_id,
  DROP COLUMN consignment, DROP COLUMN preferred_supplier_id, DROP COLUMN product_id, DROP COLUMN storage_condition,
  DROP COLUMN shelf_life_days, DROP COLUMN track_expiry, DROP COLUMN track_serial, DROP COLUMN track_batch, DROP COLUMN valuation_method,
  DROP COLUMN barcode, DROP COLUMN usage_uom_id, DROP COLUMN category_id;
DROP TABLE inventory.item_categories;
