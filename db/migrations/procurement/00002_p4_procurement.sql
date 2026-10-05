-- PRD P4 EP-10..EP-15 Procurement (procure-to-pay) on the P0 supplier
-- foundation (00001): supplier master with contacts, addresses, bank
-- accounts, documents and price lists; vendor scorecards; purchase
-- requisitions (manual, reorder — inventory.reorder_needed —, banquet BEO —
-- contract K1 —, unfulfilled store requisition); RFQ and vendor quotations;
-- purchase orders with versions and delivery schedule; goods receipts,
-- purchase returns and debit notes; vendor invoices with 3-way matching
-- and payments from accounting.vendor_payment_made.
--
-- Stock is posted by inventory from procurement.goods_received /
-- procurement.purchase_returned; GRNI and AP by accounting
-- (docs/p3-p4-contracts.md). Warehouses are created by inventory in
-- parallel, so warehouse ids are plain uuids; banquet events / BEOs and
-- accounting payments are referenced by plain uuid as well.
-- Expand-only (Technical Doc §7.5): P0 supplier rows keep their meaning.

-- +goose Up
-- ── EP-10 Supplier master ─────────────────────────────────────────────────
ALTER TABLE procurement.suppliers DROP CONSTRAINT suppliers_status_check;
ALTER TABLE procurement.suppliers ADD CONSTRAINT suppliers_status_check CHECK (status IN ('active', 'inactive', 'blocked'));
ALTER TABLE procurement.suppliers
  ADD COLUMN legal_name         text,
  ADD COLUMN categories         text[] NOT NULL DEFAULT '{}',          -- supply categories (FR-SUP-01)
  ADD COLUMN pkp                boolean NOT NULL DEFAULT false,        -- Pengusaha Kena Pajak: issues Faktur Pajak
  ADD COLUMN withholding_type   text NOT NULL DEFAULT 'none' CHECK (withholding_type IN ('none', 'pph23', 'pph4_2')),
  ADD COLUMN payment_term_days  int NOT NULL DEFAULT 30 CHECK (payment_term_days BETWEEN 0 AND 365),
  ADD COLUMN currency           text NOT NULL DEFAULT 'IDR' CHECK (currency ~ '^[A-Z]{3}$'),
  ADD COLUMN lead_time_days     int NOT NULL DEFAULT 0 CHECK (lead_time_days >= 0),
  ADD COLUMN contract_supplier  boolean NOT NULL DEFAULT false,        -- RFQ may be skipped (FR-RFQ-04)
  ADD COLUMN website            text,
  ADD COLUMN notes              text,
  ADD COLUMN blocked_reason     text,
  ADD COLUMN blocked_at         timestamptz;

CREATE TABLE procurement.supplier_contacts (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  supplier_id      uuid NOT NULL REFERENCES procurement.suppliers (id),
  name             text NOT NULL,
  position         text,
  email            text,
  phone            text,
  is_primary       boolean NOT NULL DEFAULT false,
  receives_orders  boolean NOT NULL DEFAULT true,   -- RFQ / PO e-mails go to this contact
  notes            text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  updated_by       uuid
);
CREATE INDEX supplier_contacts_supplier ON procurement.supplier_contacts (supplier_id);
SELECT platform.enable_property_rls('procurement.supplier_contacts');
SELECT platform.add_touch_trigger('procurement.supplier_contacts');

CREATE TABLE procurement.supplier_addresses (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  supplier_id   uuid NOT NULL REFERENCES procurement.suppliers (id),
  address_type  text NOT NULL DEFAULT 'office' CHECK (address_type IN ('office', 'billing', 'warehouse', 'pickup')),
  address       text NOT NULL,
  city          text,
  province      text,
  postal_code   text,
  is_primary    boolean NOT NULL DEFAULT false,
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  updated_by    uuid
);
CREATE INDEX supplier_addresses_supplier ON procurement.supplier_addresses (supplier_id);
SELECT platform.enable_property_rls('procurement.supplier_addresses');
SELECT platform.add_touch_trigger('procurement.supplier_addresses');

-- Bank accounts are masked for viewers without
-- procurement.supplier_bank_account.view_sensitive (FR-SUP-01).
CREATE TABLE procurement.supplier_bank_accounts (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  supplier_id     uuid NOT NULL REFERENCES procurement.suppliers (id),
  bank_name       text NOT NULL,
  branch          text,
  account_number  text NOT NULL,
  account_name    text NOT NULL,
  currency        text NOT NULL DEFAULT 'IDR' CHECK (currency ~ '^[A-Z]{3}$'),
  is_primary      boolean NOT NULL DEFAULT false,
  status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid
);
CREATE INDEX supplier_bank_accounts_supplier ON procurement.supplier_bank_accounts (supplier_id);
SELECT platform.enable_property_rls('procurement.supplier_bank_accounts');
SELECT platform.add_touch_trigger('procurement.supplier_bank_accounts');

CREATE TABLE procurement.supplier_documents (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  supplier_id    uuid NOT NULL REFERENCES procurement.suppliers (id),
  document_type  text NOT NULL DEFAULT 'other' CHECK (document_type IN ('npwp', 'nib', 'siup', 'sppkp', 'contract', 'bank_letter', 'certificate', 'other')),
  document_no    text,
  file_id        uuid REFERENCES platform.files (id),
  valid_until    date,
  notes          text,
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid
);
CREATE INDEX supplier_documents_supplier ON procurement.supplier_documents (supplier_id);
SELECT platform.enable_property_rls('procurement.supplier_documents');
SELECT platform.add_touch_trigger('procurement.supplier_documents');

-- Supplier items & price lists (FR-SUP-02): supplier code, price per
-- purchase UOM with validity, minimum order, lead time; last price follows
-- approved purchase orders.
CREATE TABLE procurement.supplier_items (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  supplier_id         uuid NOT NULL REFERENCES procurement.suppliers (id),
  item_id             uuid NOT NULL REFERENCES inventory.items (id),
  supplier_item_code  text,
  uom_id              uuid NOT NULL REFERENCES inventory.uoms (id),
  unit_price          numeric(19,6) NOT NULL CHECK (unit_price >= 0),
  currency            text NOT NULL DEFAULT 'IDR' CHECK (currency ~ '^[A-Z]{3}$'),
  min_order_quantity  numeric(19,6) NOT NULL DEFAULT 0 CHECK (min_order_quantity >= 0),
  lead_time_days      int CHECK (lead_time_days >= 0),
  valid_from          date,
  valid_to            date,
  preferred           boolean NOT NULL DEFAULT false,
  last_price          numeric(19,6),
  last_price_at       timestamptz,
  status              text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid,
  updated_at          timestamptz NOT NULL DEFAULT now(),
  updated_by          uuid,
  CHECK (valid_to IS NULL OR valid_from IS NULL OR valid_to >= valid_from)
);
CREATE INDEX supplier_items_item ON procurement.supplier_items (item_id, supplier_id);
SELECT platform.enable_property_rls('procurement.supplier_items');
SELECT platform.add_touch_trigger('procurement.supplier_items');

-- Blacklist / reactivation with reason and approval (FR-SUP-04).
CREATE TABLE procurement.supplier_status_requests (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  supplier_id          uuid NOT NULL REFERENCES procurement.suppliers (id),
  action               text NOT NULL CHECK (action IN ('block', 'unblock')),
  reason               text NOT NULL,
  status               text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled')),
  approval_request_id  uuid,
  requested_by         uuid,
  decided_at           timestamptz,
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX supplier_status_requests_pending ON procurement.supplier_status_requests (supplier_id) WHERE status = 'pending';
SELECT platform.enable_property_rls('procurement.supplier_status_requests');
SELECT platform.add_touch_trigger('procurement.supplier_status_requests');

-- Periodic vendor scorecard (FR-SUP-03): rates 0–1, score 0–100.
CREATE TABLE procurement.vendor_scorecards (
  id                      uuid PRIMARY KEY,
  property_id             uuid NOT NULL REFERENCES platform.properties (id),
  supplier_id             uuid NOT NULL REFERENCES procurement.suppliers (id),
  period                  text NOT NULL CHECK (period ~ '^\d{4}-\d{2}$'),
  orders                  int NOT NULL DEFAULT 0,
  receipts                int NOT NULL DEFAULT 0,
  on_time_rate            numeric(9,4),
  fill_rate               numeric(9,4),
  quality_rate            numeric(9,4),
  return_rate             numeric(9,4),
  price_variance_percent  numeric(9,4),
  response_hours          numeric(9,2),
  purchase_value          numeric(19,4) NOT NULL DEFAULT 0,
  score                   numeric(7,2) NOT NULL DEFAULT 0,
  grade                   text NOT NULL DEFAULT 'n/a',
  computed_at             timestamptz NOT NULL DEFAULT now(),
  computed_by             uuid,
  UNIQUE (property_id, supplier_id, period)
);
SELECT platform.enable_property_rls('procurement.vendor_scorecards');

-- ── EP-11 Purchase Requisition ────────────────────────────────────────────
CREATE TABLE procurement.purchase_requisitions (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  source               text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'reorder', 'banquet', 'store_requisition')),
  status               text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'submitted', 'approved', 'rejected', 'partially_ordered', 'ordered', 'cancelled')),
  title                text,
  department_id        uuid REFERENCES platform.departments (id),
  outlet_id            uuid,
  warehouse_id         uuid,
  cost_center          text,
  budget_code          text,
  category             text,
  needed_by            date,
  currency             text NOT NULL DEFAULT 'IDR' CHECK (currency ~ '^[A-Z]{3}$'),
  estimated_total      numeric(19,4) NOT NULL DEFAULT 0,
  notes                text,
  source_ref           text,           -- BEO no. / store requisition no. / reorder run
  event_id             uuid,           -- banquet event (K1)
  beo_id               uuid,
  beo_version          int,
  event_date           date,
  attention            text,           -- e.g. the BEO was revised after ordering
  requested_by         uuid,
  submitted_at         timestamptz,
  approval_request_id  uuid,
  approved_at          timestamptz,
  approved_amount      numeric(19,4),
  decided_at           timestamptz,
  rejected_reason      text,
  cancelled_reason     text,
  version              int NOT NULL DEFAULT 1,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, number)
);
CREATE INDEX purchase_requisitions_status ON procurement.purchase_requisitions (property_id, status);
CREATE INDEX purchase_requisitions_beo ON procurement.purchase_requisitions (beo_id) WHERE beo_id IS NOT NULL;
-- One open (draft) automatic requisition per warehouse (EP-08 idempotency).
CREATE UNIQUE INDEX purchase_requisitions_reorder_draft ON procurement.purchase_requisitions (property_id, warehouse_id)
  WHERE source = 'reorder' AND status = 'draft';
SELECT platform.enable_property_rls('procurement.purchase_requisitions');
SELECT platform.add_touch_trigger('procurement.purchase_requisitions');

CREATE TABLE procurement.purchase_requisition_lines (
  id                     uuid PRIMARY KEY,
  property_id            uuid NOT NULL REFERENCES platform.properties (id),
  requisition_id         uuid NOT NULL REFERENCES procurement.purchase_requisitions (id),
  line_no                int NOT NULL,
  item_id                uuid REFERENCES inventory.items (id),
  description            text NOT NULL,
  quantity               numeric(19,6) NOT NULL CHECK (quantity > 0),
  uom_id                 uuid REFERENCES inventory.uoms (id),
  base_quantity          numeric(19,6),
  estimated_unit_price   numeric(19,6) NOT NULL DEFAULT 0 CHECK (estimated_unit_price >= 0),
  estimated_total        numeric(19,4) NOT NULL DEFAULT 0,
  needed_by              date,
  cost_center            text,
  warehouse_id           uuid,
  suggested_supplier_id  uuid REFERENCES procurement.suppliers (id),
  ordered_quantity       numeric(19,6) NOT NULL DEFAULT 0 CHECK (ordered_quantity >= 0),
  rfq_id                 uuid,          -- consolidated into an open RFQ
  status                 text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'cancelled')),
  cancel_reason          text,
  source_data            jsonb NOT NULL DEFAULT '{}'::jsonb,   -- reorder figures / BEO requirement
  notes                  text,
  created_at             timestamptz NOT NULL DEFAULT now(),
  updated_at             timestamptz NOT NULL DEFAULT now(),
  UNIQUE (requisition_id, line_no)
);
CREATE INDEX purchase_requisition_lines_item ON procurement.purchase_requisition_lines (item_id) WHERE status = 'open';
SELECT platform.enable_property_rls('procurement.purchase_requisition_lines');
SELECT platform.add_touch_trigger('procurement.purchase_requisition_lines');

-- ── EP-12 RFQ & Vendor Quotation ──────────────────────────────────────────
CREATE TABLE procurement.rfqs (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  number            text NOT NULL,
  title             text NOT NULL,
  status            text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'sent', 'closed', 'awarded', 'cancelled')),
  currency          text NOT NULL DEFAULT 'IDR' CHECK (currency ~ '^[A-Z]{3}$'),
  response_due_at   timestamptz,
  delivery_date     date,
  warehouse_id      uuid,
  estimated_total   numeric(19,4) NOT NULL DEFAULT 0,
  notes             text,
  terms             text,
  sent_at           timestamptz,
  closed_at         timestamptz,
  awarded_at        timestamptz,
  cancelled_reason  text,
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('procurement.rfqs');
SELECT platform.add_touch_trigger('procurement.rfqs');

CREATE TABLE procurement.rfq_lines (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  rfq_id                uuid NOT NULL REFERENCES procurement.rfqs (id),
  line_no               int NOT NULL,
  item_id               uuid REFERENCES inventory.items (id),
  description           text NOT NULL,
  quantity              numeric(19,6) NOT NULL CHECK (quantity > 0),
  uom_id                uuid REFERENCES inventory.uoms (id),
  needed_by             date,
  estimated_unit_price  numeric(19,6) NOT NULL DEFAULT 0,
  created_at            timestamptz NOT NULL DEFAULT now(),
  UNIQUE (rfq_id, line_no)
);
SELECT platform.enable_property_rls('procurement.rfq_lines');

-- Consolidation: one RFQ line may carry several requisition lines (FR-PR-05).
CREATE TABLE procurement.rfq_line_sources (
  rfq_line_id          uuid NOT NULL REFERENCES procurement.rfq_lines (id),
  requisition_line_id  uuid NOT NULL REFERENCES procurement.purchase_requisition_lines (id),
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  quantity             numeric(19,6) NOT NULL CHECK (quantity > 0),
  PRIMARY KEY (rfq_line_id, requisition_line_id)
);
SELECT platform.enable_property_rls('procurement.rfq_line_sources');

CREATE TABLE procurement.rfq_suppliers (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  rfq_id           uuid NOT NULL REFERENCES procurement.rfqs (id),
  supplier_id      uuid NOT NULL REFERENCES procurement.suppliers (id),
  email            text,
  token_hash       text UNIQUE,          -- supplier response link (e-mail)
  status           text NOT NULL DEFAULT 'invited' CHECK (status IN ('invited', 'sent', 'responded', 'declined')),
  sent_at          timestamptz,
  responded_at     timestamptz,
  declined_reason  text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (rfq_id, supplier_id)
);
SELECT platform.enable_property_rls('procurement.rfq_suppliers');
SELECT platform.add_touch_trigger('procurement.rfq_suppliers');

CREATE TABLE procurement.vendor_quotations (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  rfq_id               uuid REFERENCES procurement.rfqs (id),
  supplier_id          uuid NOT NULL REFERENCES procurement.suppliers (id),
  supplier_reference   text,
  quotation_date       date NOT NULL,
  valid_until          date,
  currency             text NOT NULL DEFAULT 'IDR' CHECK (currency ~ '^[A-Z]{3}$'),
  lead_time_days       int CHECK (lead_time_days >= 0),
  payment_term_days    int CHECK (payment_term_days >= 0),
  delivery_terms       text,
  subtotal             numeric(19,4) NOT NULL DEFAULT 0,
  discount_total       numeric(19,4) NOT NULL DEFAULT 0,
  tax_total            numeric(19,4) NOT NULL DEFAULT 0,
  total                numeric(19,4) NOT NULL DEFAULT 0,
  status               text NOT NULL DEFAULT 'received' CHECK (status IN ('received', 'pending_approval', 'selected', 'not_selected', 'cancelled')),
  source               text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'supplier_link')),
  notes                text,
  selection_reason     text,
  selection_line_ids   uuid[] NOT NULL DEFAULT '{}',   -- lines of a pending selection
  create_order         boolean NOT NULL DEFAULT false, -- create a draft PO once selected
  approval_request_id  uuid,
  selected_at          timestamptz,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, number)
);
CREATE INDEX vendor_quotations_rfq ON procurement.vendor_quotations (rfq_id) WHERE rfq_id IS NOT NULL;
SELECT platform.enable_property_rls('procurement.vendor_quotations');
SELECT platform.add_touch_trigger('procurement.vendor_quotations');

CREATE TABLE procurement.vendor_quotation_lines (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  quotation_id      uuid NOT NULL REFERENCES procurement.vendor_quotations (id),
  line_no           int NOT NULL,
  rfq_line_id       uuid REFERENCES procurement.rfq_lines (id),
  item_id           uuid REFERENCES inventory.items (id),
  description       text NOT NULL,
  quantity          numeric(19,6) NOT NULL CHECK (quantity > 0),
  uom_id            uuid REFERENCES inventory.uoms (id),
  unit_price        numeric(19,6) NOT NULL CHECK (unit_price >= 0),
  discount_percent  numeric(7,4) NOT NULL DEFAULT 0 CHECK (discount_percent BETWEEN 0 AND 100),
  tax_percent       numeric(7,4) NOT NULL DEFAULT 0 CHECK (tax_percent BETWEEN 0 AND 100),
  line_subtotal     numeric(19,4) NOT NULL DEFAULT 0,
  tax_amount        numeric(19,4) NOT NULL DEFAULT 0,
  line_total        numeric(19,4) NOT NULL DEFAULT 0,
  lead_time_days    int CHECK (lead_time_days >= 0),
  selected          boolean NOT NULL DEFAULT false,
  notes             text,
  created_at        timestamptz NOT NULL DEFAULT now(),
  UNIQUE (quotation_id, line_no)
);
SELECT platform.enable_property_rls('procurement.vendor_quotation_lines');

-- ── EP-13 Purchase Order ──────────────────────────────────────────────────
CREATE TABLE procurement.purchase_orders (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  version              int NOT NULL DEFAULT 1,
  order_type           text NOT NULL DEFAULT 'goods' CHECK (order_type IN ('goods', 'service')),
  supplier_id          uuid NOT NULL REFERENCES procurement.suppliers (id),
  status               text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'pending_approval', 'approved', 'sent', 'partially_received',
                         'received', 'closed', 'cancelled')),
  order_date           date NOT NULL,
  expected_date        date,
  warehouse_id         uuid,
  delivery_address     text,
  currency             text NOT NULL DEFAULT 'IDR' CHECK (currency ~ '^[A-Z]{3}$'),
  payment_term_days    int NOT NULL DEFAULT 30 CHECK (payment_term_days >= 0),
  subtotal             numeric(19,4) NOT NULL DEFAULT 0,
  discount_total       numeric(19,4) NOT NULL DEFAULT 0,
  tax_total            numeric(19,4) NOT NULL DEFAULT 0,
  total                numeric(19,4) NOT NULL DEFAULT 0,
  quotation_id         uuid REFERENCES procurement.vendor_quotations (id),
  rfq_id               uuid REFERENCES procurement.rfqs (id),
  rfq_skip_reason      text,
  contact_email        text,
  notes                text,
  terms                text,
  approval_request_id  uuid,
  submitted_at         timestamptz,
  approved_at          timestamptz,
  approved_by          uuid,
  approved_total       numeric(19,4),
  rejected_reason      text,
  sent_at              timestamptz,
  sent_to              text,
  public_token_hash    text UNIQUE,      -- PDF link e-mailed to the supplier
  revision_reason      text,
  revised_at           timestamptz,
  closed_at            timestamptz,
  close_reason         text,
  cancelled_at         timestamptz,
  cancel_reason        text,
  source               text NOT NULL DEFAULT 'oneclub' CHECK (source IN ('oneclub', 'migration')),   -- open PO migrated at cut-over (EP-29)
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, number)
);
CREATE INDEX purchase_orders_status ON procurement.purchase_orders (property_id, status);
CREATE INDEX purchase_orders_supplier ON procurement.purchase_orders (supplier_id);
SELECT platform.enable_property_rls('procurement.purchase_orders');
SELECT platform.add_touch_trigger('procurement.purchase_orders');

CREATE TABLE procurement.purchase_order_lines (
  id                          uuid PRIMARY KEY,
  property_id                 uuid NOT NULL REFERENCES platform.properties (id),
  purchase_order_id           uuid NOT NULL REFERENCES procurement.purchase_orders (id),
  line_no                     int NOT NULL,
  item_id                     uuid REFERENCES inventory.items (id),
  description                 text NOT NULL,
  quantity                    numeric(19,6) NOT NULL CHECK (quantity > 0),
  uom_id                      uuid REFERENCES inventory.uoms (id),
  base_quantity               numeric(19,6),
  unit_price                  numeric(19,6) NOT NULL CHECK (unit_price >= 0),
  discount_percent            numeric(7,4) NOT NULL DEFAULT 0 CHECK (discount_percent BETWEEN 0 AND 100),
  tax_percent                 numeric(7,4) NOT NULL DEFAULT 0 CHECK (tax_percent BETWEEN 0 AND 100),
  tax_code                    text,
  line_subtotal               numeric(19,4) NOT NULL DEFAULT 0,
  tax_amount                  numeric(19,4) NOT NULL DEFAULT 0,
  line_total                  numeric(19,4) NOT NULL DEFAULT 0,
  expected_date               date,              -- delivery schedule per line
  received_quantity           numeric(19,6) NOT NULL DEFAULT 0 CHECK (received_quantity >= 0),
  returned_quantity           numeric(19,6) NOT NULL DEFAULT 0 CHECK (returned_quantity >= 0),
  cancelled_quantity          numeric(19,6) NOT NULL DEFAULT 0 CHECK (cancelled_quantity >= 0),
  invoiced_quantity           numeric(19,6) NOT NULL DEFAULT 0 CHECK (invoiced_quantity >= 0),
  service_confirmed_quantity  numeric(19,6) NOT NULL DEFAULT 0 CHECK (service_confirmed_quantity >= 0),
  opening_received_quantity   numeric(19,6) NOT NULL DEFAULT 0 CHECK (opening_received_quantity >= 0),  -- received before the cut-over
  quotation_line_id           uuid REFERENCES procurement.vendor_quotation_lines (id),
  account_hint                text CHECK (account_hint IN ('inventory', 'expense', 'asset')),
  notes                       text,
  created_at                  timestamptz NOT NULL DEFAULT now(),
  updated_at                  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (purchase_order_id, line_no),
  CHECK (cancelled_quantity <= quantity)
);
CREATE INDEX purchase_order_lines_item ON procurement.purchase_order_lines (item_id);
SELECT platform.enable_property_rls('procurement.purchase_order_lines');
SELECT platform.add_touch_trigger('procurement.purchase_order_lines');

-- Requisition lines fulfilled by an order line (consolidation, FR-PR-05).
CREATE TABLE procurement.purchase_order_line_sources (
  purchase_order_line_id  uuid NOT NULL REFERENCES procurement.purchase_order_lines (id),
  requisition_line_id     uuid NOT NULL REFERENCES procurement.purchase_requisition_lines (id),
  property_id             uuid NOT NULL REFERENCES platform.properties (id),
  quantity                numeric(19,6) NOT NULL CHECK (quantity > 0),
  PRIMARY KEY (purchase_order_line_id, requisition_line_id)
);
SELECT platform.enable_property_rls('procurement.purchase_order_line_sources');

-- Every revision keeps the previous version (FR-PO-03).
CREATE TABLE procurement.purchase_order_revisions (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  purchase_order_id  uuid NOT NULL REFERENCES procurement.purchase_orders (id),
  version            int NOT NULL,
  reason             text NOT NULL,
  snapshot           jsonb NOT NULL,
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  UNIQUE (purchase_order_id, version)
);
SELECT platform.enable_property_rls('procurement.purchase_order_revisions');

-- ── EP-14 Goods Receipt & Purchase Return ─────────────────────────────────
CREATE TABLE procurement.goods_receipts (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  purchase_order_id    uuid REFERENCES procurement.purchase_orders (id),   -- NULL: receipt without PO (FR-GR-03)
  supplier_id          uuid NOT NULL REFERENCES procurement.suppliers (id),
  warehouse_id         uuid NOT NULL,
  received_date        date NOT NULL,
  delivery_note_no     text,
  notes                text,
  status               text NOT NULL DEFAULT 'posted' CHECK (status IN ('pending_approval', 'posted', 'rejected')),
  approval_reason      text CHECK (approval_reason IN ('over_receipt', 'without_po')),
  approval_request_id  uuid,
  currency             text NOT NULL DEFAULT 'IDR' CHECK (currency ~ '^[A-Z]{3}$'),
  total                numeric(19,4) NOT NULL DEFAULT 0,
  attachment_file_ids  uuid[] NOT NULL DEFAULT '{}',   -- delivery note photos (FR-GR-01)
  received_by          uuid,
  posted_at            timestamptz,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, number)
);
CREATE INDEX goods_receipts_po ON procurement.goods_receipts (purchase_order_id);
SELECT platform.enable_property_rls('procurement.goods_receipts');
SELECT platform.add_touch_trigger('procurement.goods_receipts');

CREATE TABLE procurement.goods_receipt_lines (
  id                      uuid PRIMARY KEY,
  property_id             uuid NOT NULL REFERENCES platform.properties (id),
  goods_receipt_id        uuid NOT NULL REFERENCES procurement.goods_receipts (id),
  line_no                 int NOT NULL,
  purchase_order_line_id  uuid REFERENCES procurement.purchase_order_lines (id),
  item_id                 uuid REFERENCES inventory.items (id),
  description             text NOT NULL,
  uom_id                  uuid REFERENCES inventory.uoms (id),
  delivered_quantity      numeric(19,6) NOT NULL CHECK (delivered_quantity >= 0),
  accepted_quantity       numeric(19,6) NOT NULL CHECK (accepted_quantity >= 0),
  rejected_quantity       numeric(19,6) NOT NULL DEFAULT 0 CHECK (rejected_quantity >= 0),
  rejection_reason        text,
  base_quantity           numeric(19,6) NOT NULL DEFAULT 0,
  unit_cost               numeric(19,6) NOT NULL DEFAULT 0,
  base_unit_cost          numeric(19,6) NOT NULL DEFAULT 0,
  total_cost              numeric(19,4) NOT NULL DEFAULT 0,
  tax_percent             numeric(7,4) NOT NULL DEFAULT 0,
  tax_code                text,
  batch_no                text,
  expiry_date             date,
  serial_nos              text[] NOT NULL DEFAULT '{}',
  returned_quantity       numeric(19,6) NOT NULL DEFAULT 0 CHECK (returned_quantity >= 0),
  notes                   text,
  created_at              timestamptz NOT NULL DEFAULT now(),
  UNIQUE (goods_receipt_id, line_no),
  CHECK (delivered_quantity = accepted_quantity + rejected_quantity),
  CHECK (returned_quantity <= accepted_quantity)
);
CREATE INDEX goods_receipt_lines_po_line ON procurement.goods_receipt_lines (purchase_order_line_id);
SELECT platform.enable_property_rls('procurement.goods_receipt_lines');

CREATE TABLE procurement.purchase_returns (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  number             text NOT NULL,
  goods_receipt_id   uuid NOT NULL REFERENCES procurement.goods_receipts (id),
  purchase_order_id  uuid REFERENCES procurement.purchase_orders (id),
  supplier_id        uuid NOT NULL REFERENCES procurement.suppliers (id),
  warehouse_id       uuid NOT NULL,
  return_date        date NOT NULL,
  reason             text NOT NULL,
  status             text NOT NULL DEFAULT 'posted' CHECK (status IN ('posted')),
  currency           text NOT NULL DEFAULT 'IDR' CHECK (currency ~ '^[A-Z]{3}$'),
  subtotal           numeric(19,4) NOT NULL DEFAULT 0,
  tax_amount         numeric(19,4) NOT NULL DEFAULT 0,
  total              numeric(19,4) NOT NULL DEFAULT 0,
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('procurement.purchase_returns');
SELECT platform.add_touch_trigger('procurement.purchase_returns');

CREATE TABLE procurement.purchase_return_lines (
  id                     uuid PRIMARY KEY,
  property_id            uuid NOT NULL REFERENCES platform.properties (id),
  purchase_return_id     uuid NOT NULL REFERENCES procurement.purchase_returns (id),
  line_no                int NOT NULL,
  goods_receipt_line_id  uuid NOT NULL REFERENCES procurement.goods_receipt_lines (id),
  item_id                uuid REFERENCES inventory.items (id),
  description            text NOT NULL,
  quantity               numeric(19,6) NOT NULL CHECK (quantity > 0),
  uom_id                 uuid REFERENCES inventory.uoms (id),
  base_quantity          numeric(19,6) NOT NULL DEFAULT 0,
  unit_cost              numeric(19,6) NOT NULL DEFAULT 0,
  base_unit_cost         numeric(19,6) NOT NULL DEFAULT 0,
  total_cost             numeric(19,4) NOT NULL DEFAULT 0,
  tax_percent            numeric(7,4) NOT NULL DEFAULT 0,
  tax_amount             numeric(19,4) NOT NULL DEFAULT 0,
  batch_no               text,
  serial_nos             text[] NOT NULL DEFAULT '{}',
  reason                 text,
  UNIQUE (purchase_return_id, line_no)
);
SELECT platform.enable_property_rls('procurement.purchase_return_lines');

-- ── EP-15 Vendor Invoice & 3-Way Matching ─────────────────────────────────
CREATE TABLE procurement.vendor_invoices (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  supplier_id          uuid NOT NULL REFERENCES procurement.suppliers (id),
  supplier_invoice_no  text NOT NULL,
  tax_invoice_no       text,                    -- Faktur Pajak masukan
  invoice_date         date NOT NULL,
  received_date        date,
  due_date             date NOT NULL,
  payment_term_days    int NOT NULL DEFAULT 30,
  currency             text NOT NULL DEFAULT 'IDR' CHECK (currency ~ '^[A-Z]{3}$'),
  subtotal             numeric(19,4) NOT NULL DEFAULT 0,
  tax_amount           numeric(19,4) NOT NULL DEFAULT 0,
  total                numeric(19,4) NOT NULL DEFAULT 0,   -- subtotal + PPN
  withholding_type     text NOT NULL DEFAULT 'none' CHECK (withholding_type IN ('none', 'pph23', 'pph4_2')),
  withholding_amount   numeric(19,4) NOT NULL DEFAULT 0,   -- PPh withheld on payment
  status               text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'matched', 'mismatch', 'on_hold', 'approved', 'partially_paid',
                         'paid', 'cancelled')),
  match_type           text CHECK (match_type IN ('three_way', 'two_way')),
  matched_at           timestamptz,
  match_summary        jsonb NOT NULL DEFAULT '{}'::jsonb,
  hold_reason          text,
  held_at              timestamptz,
  override_reason      text,
  approval_request_id  uuid,
  approval_kind        text CHECK (approval_kind IN ('approval', 'override')),
  approved_at          timestamptz,
  approved_by          uuid,
  paid_amount          numeric(19,4) NOT NULL DEFAULT 0,
  debit_note_total     numeric(19,4) NOT NULL DEFAULT 0,
  paid_at              timestamptz,
  notes                text,
  cancelled_reason     text,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, number)
);
CREATE UNIQUE INDEX vendor_invoices_supplier_no ON procurement.vendor_invoices (property_id, supplier_id, lower(supplier_invoice_no))
  WHERE status <> 'cancelled';
SELECT platform.enable_property_rls('procurement.vendor_invoices');
SELECT platform.add_touch_trigger('procurement.vendor_invoices');

CREATE TABLE procurement.vendor_invoice_lines (
  id                      uuid PRIMARY KEY,
  property_id             uuid NOT NULL REFERENCES platform.properties (id),
  vendor_invoice_id       uuid NOT NULL REFERENCES procurement.vendor_invoices (id),
  line_no                 int NOT NULL,
  purchase_order_line_id  uuid REFERENCES procurement.purchase_order_lines (id),
  goods_receipt_line_id   uuid REFERENCES procurement.goods_receipt_lines (id),
  item_id                 uuid REFERENCES inventory.items (id),
  description             text NOT NULL,
  quantity                numeric(19,6) NOT NULL CHECK (quantity > 0),
  unit_price              numeric(19,6) NOT NULL CHECK (unit_price >= 0),
  tax_percent             numeric(7,4) NOT NULL DEFAULT 0 CHECK (tax_percent BETWEEN 0 AND 100),
  line_subtotal           numeric(19,4) NOT NULL DEFAULT 0,
  tax_amount              numeric(19,4) NOT NULL DEFAULT 0,
  line_total              numeric(19,4) NOT NULL DEFAULT 0,
  account_hint            text NOT NULL DEFAULT 'expense' CHECK (account_hint IN ('inventory', 'expense', 'asset')),
  match_status            text NOT NULL DEFAULT 'pending' CHECK (match_status IN ('pending', 'matched', 'quantity_mismatch', 'price_mismatch',
                            'tax_mismatch', 'not_received', 'not_on_order')),
  expected_quantity       numeric(19,6),
  expected_unit_price     numeric(19,6),
  match_note              text,
  UNIQUE (vendor_invoice_id, line_no)
);
CREATE INDEX vendor_invoice_lines_po_line ON procurement.vendor_invoice_lines (purchase_order_line_id);
SELECT platform.enable_property_rls('procurement.vendor_invoice_lines');

-- Matching Result history (PRD P4 §10).
CREATE TABLE procurement.vendor_invoice_match_runs (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  vendor_invoice_id  uuid NOT NULL REFERENCES procurement.vendor_invoices (id),
  result             text NOT NULL CHECK (result IN ('matched', 'mismatch')),
  match_type         text NOT NULL CHECK (match_type IN ('three_way', 'two_way')),
  details            jsonb NOT NULL DEFAULT '[]'::jsonb,
  run_by             uuid,
  run_at             timestamptz NOT NULL DEFAULT now()
);
SELECT platform.enable_property_rls('procurement.vendor_invoice_match_runs');

-- Payments applied by accounting (accounting.vendor_payment_made), idempotent.
CREATE TABLE procurement.vendor_invoice_payments (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  vendor_invoice_id  uuid NOT NULL REFERENCES procurement.vendor_invoices (id),
  payment_id         uuid NOT NULL,
  payment_number     text,
  paid_date          date,
  amount             numeric(19,4) NOT NULL,
  currency           text NOT NULL DEFAULT 'IDR',
  created_at         timestamptz NOT NULL DEFAULT now(),
  UNIQUE (payment_id, vendor_invoice_id)
);
SELECT platform.enable_property_rls('procurement.vendor_invoice_payments');

CREATE TABLE procurement.debit_notes (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  number              text NOT NULL,
  supplier_id         uuid NOT NULL REFERENCES procurement.suppliers (id),
  purchase_return_id  uuid REFERENCES procurement.purchase_returns (id),
  purchase_order_id   uuid REFERENCES procurement.purchase_orders (id),
  vendor_invoice_id   uuid REFERENCES procurement.vendor_invoices (id),
  issue_date          date NOT NULL,
  reason              text NOT NULL,
  subtotal            numeric(19,4) NOT NULL DEFAULT 0,
  tax_amount          numeric(19,4) NOT NULL DEFAULT 0,
  amount              numeric(19,4) NOT NULL CHECK (amount > 0),
  currency            text NOT NULL DEFAULT 'IDR' CHECK (currency ~ '^[A-Z]{3}$'),
  status              text NOT NULL DEFAULT 'issued' CHECK (status IN ('issued', 'applied')),
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid,
  updated_at          timestamptz NOT NULL DEFAULT now(),
  UNIQUE (property_id, number)
);
CREATE INDEX debit_notes_po ON procurement.debit_notes (purchase_order_id);
SELECT platform.enable_property_rls('procurement.debit_notes');
SELECT platform.add_touch_trigger('procurement.debit_notes');

SELECT platform.grant_app('procurement');

-- +goose Down
DROP TABLE procurement.debit_notes, procurement.vendor_invoice_payments, procurement.vendor_invoice_match_runs,
  procurement.vendor_invoice_lines, procurement.vendor_invoices, procurement.purchase_return_lines, procurement.purchase_returns,
  procurement.goods_receipt_lines, procurement.goods_receipts, procurement.purchase_order_revisions, procurement.purchase_order_line_sources,
  procurement.purchase_order_lines, procurement.purchase_orders, procurement.vendor_quotation_lines, procurement.vendor_quotations,
  procurement.rfq_suppliers, procurement.rfq_line_sources, procurement.rfq_lines, procurement.rfqs, procurement.purchase_requisition_lines,
  procurement.purchase_requisitions, procurement.vendor_scorecards, procurement.supplier_status_requests, procurement.supplier_items,
  procurement.supplier_documents, procurement.supplier_bank_accounts, procurement.supplier_addresses, procurement.supplier_contacts;
UPDATE procurement.suppliers SET status = 'inactive' WHERE status = 'blocked';
ALTER TABLE procurement.suppliers DROP CONSTRAINT suppliers_status_check;
ALTER TABLE procurement.suppliers ADD CONSTRAINT suppliers_status_check CHECK (status IN ('active', 'inactive'));
ALTER TABLE procurement.suppliers DROP COLUMN legal_name, DROP COLUMN categories, DROP COLUMN pkp, DROP COLUMN withholding_type,
  DROP COLUMN payment_term_days, DROP COLUMN currency, DROP COLUMN lead_time_days, DROP COLUMN contract_supplier, DROP COLUMN website,
  DROP COLUMN notes, DROP COLUMN blocked_reason, DROP COLUMN blocked_at;
