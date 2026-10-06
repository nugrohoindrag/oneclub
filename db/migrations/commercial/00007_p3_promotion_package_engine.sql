-- PRD P3 EP-10 Pricing & Promotion Engine and EP-11 Package Management:
-- completes the P3 schema of 00006 (Expand-only, Technical Doc §7.5) for the
-- engine, its channels and the package bookings:
--   * rate rules gain Corporate Rate (contract rate per corporate account) and
--     Holiday Rate next to P1's Peak flag (FR-PRC-P3-01);
--   * promotions gain the promo code type, per unit / maximum discount, the
--     item, membership type, weekday, day kind and day type scope, minimum
--     quantity, website visibility, stack groups, the per customer limit and
--     the Expired status (FR-PRM-01..05, FR-PRM-10); id lists become text[]
--     like the other id lists of the commercial resources;
--   * promo codes keep the CRM campaign and the generation batch (FR-PRM-04,
--     FR-CMP-05);
--   * the redemption ledger holds Applied → Redeemed / Reversed rows with the
--     promotion code, business line and folio (FR-PRM-07, contract K3);
--   * POS lines keep their promotions and discount; orders keep the time
--     the promotions were evaluated at (FR-PRM-06, FR-PRM-09);
--   * packages gain start time, service validity, cancellation fee, component
--     validity and website visibility; components the Reservation Engine
--     resource type, product, day / time / nights; bookings the package code,
--     start date, add-ons, the price breakdown, folio, schedule, snapshot and
--     promo codes; booking components the consumed quantity, service date
--     and allocation; a consumption log per use (FR-PKG-01..09, K6).
-- Rows written before this migration (none in practice: 00006 had no code)
-- are backfilled before NOT NULL / CHECK constraints are added.

-- +goose Up
-- ── FR-PRC-P3-01 rate rule dimensions ─────────────────────────────────────
ALTER TABLE commercial.pricing_rules
  ADD COLUMN corporate_account_id uuid REFERENCES crm.corporate_accounts (id),
  ADD COLUMN holiday              boolean;
CREATE INDEX pricing_rules_corporate ON commercial.pricing_rules (corporate_account_id) WHERE corporate_account_id IS NOT NULL;

-- ── EP-10 promotions ──────────────────────────────────────────────────────
ALTER TABLE commercial.promotions
  ADD COLUMN terms            text,
  ADD COLUMN per_unit         boolean NOT NULL DEFAULT false,
  ADD COLUMN max_discount     numeric(19,4) CHECK (max_discount IS NULL OR max_discount > 0),
  ADD COLUMN item_refs        text[] NOT NULL DEFAULT '{}',
  ADD COLUMN membership_types text[] NOT NULL DEFAULT '{}',
  ADD COLUMN weekdays         int[] NOT NULL DEFAULT '{}',
  ADD COLUMN day_kinds        text[] NOT NULL DEFAULT '{}',
  ADD COLUMN day_type_codes   text[] NOT NULL DEFAULT '{}',
  ADD COLUMN min_quantity     int CHECK (min_quantity IS NULL OR min_quantity > 0),
  ADD COLUMN public           boolean NOT NULL DEFAULT true,
  ADD COLUMN stack_group      text,
  ADD COLUMN max_per_customer int CHECK (max_per_customer IS NULL OR max_per_customer > 0),
  ADD COLUMN activated_by     uuid;
ALTER TABLE commercial.promotions
  ALTER COLUMN outlet_ids DROP DEFAULT,
  ALTER COLUMN outlet_ids TYPE text[] USING outlet_ids::text[],
  ALTER COLUMN outlet_ids SET DEFAULT '{}',
  ALTER COLUMN product_ids DROP DEFAULT,
  ALTER COLUMN product_ids TYPE text[] USING product_ids::text[],
  ALTER COLUMN product_ids SET DEFAULT '{}',
  ALTER COLUMN customer_segment_ids DROP DEFAULT,
  ALTER COLUMN customer_segment_ids TYPE text[] USING customer_segment_ids::text[],
  ALTER COLUMN customer_segment_ids SET DEFAULT '{}';
ALTER TABLE commercial.promotions DROP CONSTRAINT IF EXISTS promotions_promo_type_check;
ALTER TABLE commercial.promotions ADD CONSTRAINT promotions_promo_type_check CHECK (promo_type IN ('percent_discount', 'amount_discount', 'happy_hour',
  'buy_n_get_x', 'buy_n_price_x', 'bundle', 'member_discount', 'promo_code', 'period'));
ALTER TABLE commercial.promotions DROP CONSTRAINT IF EXISTS promotions_status_check;
UPDATE commercial.promotions SET status = 'pending' WHERE status = 'pending_approval';
ALTER TABLE commercial.promotions ADD CONSTRAINT promotions_status_check CHECK (status IN ('draft', 'pending', 'active', 'inactive', 'rejected', 'expired'));
UPDATE commercial.promotions SET min_purchase = NULL WHERE min_purchase < 0;
UPDATE commercial.promotions SET budget_amount = NULL WHERE budget_amount <= 0;
UPDATE commercial.promotions SET max_redemptions = NULL WHERE max_redemptions <= 0;
UPDATE commercial.promotions SET valid_to = valid_from WHERE valid_to < valid_from;
ALTER TABLE commercial.promotions
  ADD CONSTRAINT promotions_min_purchase_check CHECK (min_purchase IS NULL OR min_purchase >= 0),
  ADD CONSTRAINT promotions_budget_amount_check CHECK (budget_amount IS NULL OR budget_amount > 0),
  ADD CONSTRAINT promotions_max_redemptions_check CHECK (max_redemptions IS NULL OR max_redemptions > 0),
  ADD CONSTRAINT promotions_valid_period_check CHECK (valid_to IS NULL OR valid_from IS NULL OR valid_to >= valid_from);
DROP INDEX commercial.promotions_active;
CREATE INDEX promotions_active ON commercial.promotions (property_id, priority) WHERE status = 'active' AND archived_at IS NULL;

-- FR-PRM-04 promo codes: upper case codes, the CRM campaign (campaign_id is
-- the crm campaign of crm.campaign_sent, campaign_ref a free label) and the
-- bulk generation batch.
UPDATE commercial.promo_codes SET code = upper(code) WHERE code <> upper(code);
ALTER TABLE commercial.promo_codes
  ADD COLUMN campaign_ref text,
  ADD COLUMN batch_id     uuid,
  ADD CONSTRAINT promo_codes_code_upper_check CHECK (code = upper(code));
UPDATE commercial.promo_codes SET max_uses = NULL WHERE max_uses <= 0;
UPDATE commercial.promo_codes SET max_uses_per_customer = NULL WHERE max_uses_per_customer <= 0;
ALTER TABLE commercial.promo_codes
  ADD CONSTRAINT promo_codes_max_uses_check CHECK (max_uses IS NULL OR max_uses > 0),
  ADD CONSTRAINT promo_codes_max_uses_per_customer_check CHECK (max_uses_per_customer IS NULL OR max_uses_per_customer > 0);
CREATE INDEX promo_codes_customer ON commercial.promo_codes (customer_id) WHERE customer_id IS NOT NULL;
CREATE INDEX promo_codes_campaign ON commercial.promo_codes (campaign_id) WHERE campaign_id IS NOT NULL;

-- FR-PRM-07 redemption ledger: Applied rows belong to an open document (POS
-- order, priced booking snapshot, pending package); they become Redeemed when
-- the sale completes (commercial.promotion_applied, K3) and Reversed when the
-- document is voided, refunded or cancelled. Limits count Applied + Redeemed.
ALTER TABLE commercial.promotion_redemptions
  ADD COLUMN promotion_code text,
  ADD COLUMN promo_code     text,
  ADD COLUMN source_ref     text,
  ADD COLUMN folio_id       uuid,
  ADD COLUMN business_line  text NOT NULL DEFAULT 'other',
  ADD COLUMN currency       char(3) NOT NULL DEFAULT 'IDR',
  ADD COLUMN status         text NOT NULL DEFAULT 'applied' CHECK (status IN ('applied', 'redeemed', 'reversed')),
  ADD COLUMN redeemed_at    timestamptz,
  ADD COLUMN reverse_reason text;
UPDATE commercial.promotion_redemptions r SET promotion_code = p.code FROM commercial.promotions p WHERE p.id = r.promotion_id;
UPDATE commercial.promotion_redemptions SET status = CASE WHEN reversed_at IS NOT NULL THEN 'reversed' ELSE 'redeemed' END,
  redeemed_at = CASE WHEN reversed_at IS NULL THEN created_at END;
UPDATE commercial.promotion_redemptions SET source_type = 'pricing_snapshot' WHERE source_type NOT IN ('pos_order', 'pricing_snapshot', 'package_booking');
ALTER TABLE commercial.promotion_redemptions
  ALTER COLUMN promotion_code SET NOT NULL,
  ADD CONSTRAINT promotion_redemptions_source_type_check CHECK (source_type IN ('pos_order', 'pricing_snapshot', 'package_booking'));
DROP INDEX commercial.promotion_redemptions_promotion;
CREATE INDEX promotion_redemptions_promotion ON commercial.promotion_redemptions (promotion_id, status);
CREATE INDEX promotion_redemptions_code ON commercial.promotion_redemptions (promo_code_id) WHERE promo_code_id IS NOT NULL;
CREATE INDEX promotion_redemptions_time ON commercial.promotion_redemptions (property_id, created_at);

-- POS: the promotions of each line with their discount (order_lines.promotion_id
-- keeps the first one) and when the order's promotions were evaluated (the
-- client time of an offline sale).
ALTER TABLE commercial.order_lines
  ADD COLUMN promotion_discount numeric(19,4) NOT NULL DEFAULT 0,
  ADD COLUMN promotions         jsonb NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE commercial.orders ADD COLUMN promotion_at timestamptz;
-- promotions the cashier removed are kept as text ids like the other id lists
ALTER TABLE commercial.orders
  ALTER COLUMN promotion_exclusions DROP DEFAULT,
  ALTER COLUMN promotion_exclusions TYPE text[] USING promotion_exclusions::text[],
  ALTER COLUMN promotion_exclusions SET DEFAULT '{}';

-- ── EP-11 packages ────────────────────────────────────────────────────────
ALTER TABLE commercial.packages
  ADD COLUMN start_time               time NOT NULL DEFAULT '08:00',
  ADD COLUMN valid_from               date,
  ADD COLUMN valid_to                 date,
  ADD COLUMN cancellation_fee_percent numeric(9,4) NOT NULL DEFAULT 0
    CHECK (cancellation_fee_percent >= 0 AND cancellation_fee_percent <= 100),
  ADD COLUMN component_validity_days  int NOT NULL DEFAULT 0 CHECK (component_validity_days >= 0),
  ADD COLUMN public                   boolean NOT NULL DEFAULT true,
  ADD COLUMN published_by             uuid,
  ALTER COLUMN price SET DEFAULT 0;
ALTER TABLE commercial.packages DROP CONSTRAINT IF EXISTS packages_package_type_check;
ALTER TABLE commercial.packages ADD CONSTRAINT packages_package_type_check CHECK (package_type IN ('golf_day', 'golf_lunch', 'stay_golf', 'corporate',
  'wedding', 'family', 'other'));
ALTER TABLE commercial.packages DROP CONSTRAINT IF EXISTS packages_pricing_mode_check;
ALTER TABLE commercial.packages ADD CONSTRAINT packages_pricing_mode_check CHECK (pricing_mode IN ('fixed', 'per_pax', 'per_night', 'per_pax_per_night'));
UPDATE commercial.packages SET max_pax = NULL WHERE max_pax < 1;
UPDATE commercial.packages SET daily_quota = NULL WHERE daily_quota <= 0;
UPDATE commercial.packages SET cancellation_hours = 0 WHERE cancellation_hours < 0;
ALTER TABLE commercial.packages
  ADD CONSTRAINT packages_max_pax_check CHECK (max_pax IS NULL OR max_pax >= 1),
  ADD CONSTRAINT packages_daily_quota_check CHECK (daily_quota IS NULL OR daily_quota > 0),
  ADD CONSTRAINT packages_cancellation_hours_check CHECK (cancellation_hours >= 0);

-- Component types follow commercial.package_booked (docs/p3-p4-contracts.md):
-- reservation (any Reservation Engine resource type), tee_time, voucher, fnb,
-- service, banquet, other.
ALTER TABLE commercial.package_components DROP CONSTRAINT IF EXISTS package_components_component_type_check;
UPDATE commercial.package_components SET component_type = CASE
    WHEN component_type IN ('bungalow', 'vip_suite', 'meeting_room', 'venue', 'golf_cart', 'equipment', 'court') THEN 'reservation'
    WHEN component_type = 'product' THEN 'fnb'
    WHEN component_type = 'class' THEN 'service'
    ELSE component_type END
  WHERE component_type NOT IN ('reservation', 'tee_time', 'voucher', 'fnb', 'service', 'banquet', 'other');
ALTER TABLE commercial.package_components ADD CONSTRAINT package_components_component_type_check CHECK (component_type IN ('reservation', 'tee_time',
  'voucher', 'fnb', 'service', 'banquet', 'other'));
ALTER TABLE commercial.package_components
  ADD COLUMN resource_type_code text,
  ADD COLUMN product_id         uuid REFERENCES commercial.products (id),
  ADD COLUMN per_night          boolean NOT NULL DEFAULT false,
  ADD COLUMN day_offset         int NOT NULL DEFAULT 0 CHECK (day_offset >= 0),
  ADD COLUMN start_time         time,
  ADD COLUMN nights             int CHECK (nights IS NULL OR nights >= 0),
  ALTER COLUMN business_line SET DEFAULT 'package';
UPDATE commercial.package_components SET duration_minutes = NULL WHERE duration_minutes <= 0;
UPDATE commercial.package_components SET allocation_value = NULL WHERE allocation_value < 0;
UPDATE commercial.package_components SET addon_price = NULL WHERE addon_price < 0;
ALTER TABLE commercial.package_components
  ADD CONSTRAINT package_components_duration_check CHECK (duration_minutes IS NULL OR duration_minutes > 0),
  ADD CONSTRAINT package_components_allocation_value_check CHECK (allocation_value IS NULL OR allocation_value >= 0),
  ADD CONSTRAINT package_components_addon_price_check CHECK (addon_price IS NULL OR addon_price >= 0);

-- Package bookings: price_total of 00006 is the list total (base + add-ons
-- before promotions) and is renamed list_total; the net / service / tax /
-- total breakdown is added.
ALTER TABLE commercial.package_bookings RENAME COLUMN price_total TO list_total;
ALTER TABLE commercial.package_bookings
  ADD COLUMN package_code      text,
  ADD COLUMN start_date        date,
  ADD COLUMN addons            text[] NOT NULL DEFAULT '{}',
  ADD COLUMN net_total         numeric(19,4),
  ADD COLUMN service_total     numeric(19,4) NOT NULL DEFAULT 0,
  ADD COLUMN tax_total         numeric(19,4) NOT NULL DEFAULT 0,
  ADD COLUMN total             numeric(19,4),
  ADD COLUMN customer_folio_id uuid,
  ADD COLUMN schedule_id       uuid,
  ADD COLUMN snapshot_id       uuid REFERENCES commercial.pricing_snapshots (id),
  ADD COLUMN promo_codes       text[] NOT NULL DEFAULT '{}',
  ADD COLUMN policy_refs       jsonb NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN confirmed_at      timestamptz,
  ADD COLUMN completed_at      timestamptz,
  ADD COLUMN cancellation_fee  numeric(19,4),
  ADD COLUMN cancelled_by      uuid;
UPDATE commercial.package_bookings b SET package_code = p.code FROM commercial.packages p WHERE p.id = b.package_id;
UPDATE commercial.package_bookings b SET start_date = (b.start_at AT TIME ZONE coalesce((SELECT timezone FROM platform.instance), 'UTC'))::date,
  net_total = b.list_total - b.discount_total, total = b.list_total - b.discount_total;
ALTER TABLE commercial.package_bookings DROP CONSTRAINT IF EXISTS package_bookings_status_check;
UPDATE commercial.package_bookings SET status = CASE status WHEN 'held' THEN 'pending' WHEN 'consumed' THEN 'completed' ELSE status END;
UPDATE commercial.package_bookings SET channel = 'back_office' WHERE channel NOT IN ('back_office', 'website', 'member_app', 'ops', 'quotation');
UPDATE commercial.package_bookings b SET folio_id = NULL WHERE folio_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM billing.folios f WHERE f.id = b.folio_id);
ALTER TABLE commercial.package_bookings
  ALTER COLUMN package_code SET NOT NULL,
  ALTER COLUMN start_date SET NOT NULL,
  ALTER COLUMN net_total SET NOT NULL,
  ALTER COLUMN total SET NOT NULL,
  ADD CONSTRAINT package_bookings_folio_fk FOREIGN KEY (folio_id) REFERENCES billing.folios (id),
  ADD CONSTRAINT package_bookings_channel_check CHECK (channel IN ('back_office', 'website', 'member_app', 'ops', 'quotation'));
ALTER TABLE commercial.package_bookings ADD CONSTRAINT package_bookings_status_check CHECK (status IN ('pending', 'confirmed', 'completed', 'cancelled',
  'expired'));
DROP INDEX commercial.package_bookings_source;
CREATE UNIQUE INDEX package_bookings_source ON commercial.package_bookings (source_type, source_id)
  WHERE source_id IS NOT NULL AND status NOT IN ('cancelled', 'expired');
DROP INDEX commercial.package_bookings_day;
CREATE INDEX package_bookings_day ON commercial.package_bookings (package_id, start_date);
CREATE INDEX package_bookings_folio ON commercial.package_bookings (folio_id) WHERE folio_id IS NOT NULL;
CREATE INDEX package_bookings_customer ON commercial.package_bookings (customer_id) WHERE customer_id IS NOT NULL;
CREATE INDEX package_bookings_pending ON commercial.package_bookings (hold_expires_at) WHERE status = 'pending';

-- Booking components: consumed quantity, service date and the allocation
-- made by the business line (reservation, golf booking, vouchers …).
ALTER TABLE commercial.package_booking_components
  ADD COLUMN consumed_quantity  numeric(19,4) NOT NULL DEFAULT 0,
  ADD COLUMN service_date       date,
  ADD COLUMN resource_type_code text,
  ADD COLUMN ref_code           text,
  ADD COLUMN product_id         uuid,
  ADD COLUMN allocation_id      uuid,
  ADD COLUMN allocation_details jsonb NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN optional           boolean NOT NULL DEFAULT false,
  ADD COLUMN expires_at         timestamptz,
  ADD COLUMN cancelled_at       timestamptz,
  ADD COLUMN created_at         timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN updated_at         timestamptz NOT NULL DEFAULT now();
UPDATE commercial.package_booking_components c SET service_date = coalesce((c.scheduled_start AT TIME ZONE coalesce((SELECT timezone FROM platform.instance),
  'UTC'))::date, b.start_date) FROM commercial.package_bookings b WHERE b.id = c.booking_id;
UPDATE commercial.package_booking_components SET quantity = 1 WHERE quantity <= 0;
ALTER TABLE commercial.package_booking_components
  ALTER COLUMN service_date SET NOT NULL,
  ADD CONSTRAINT package_booking_components_quantity_check CHECK (quantity > 0);
CREATE INDEX package_booking_components_allocation ON commercial.package_booking_components (allocation_id) WHERE allocation_id IS NOT NULL;
CREATE INDEX package_booking_components_expiry ON commercial.package_booking_components (expires_at) WHERE status = 'unused';
SELECT platform.add_touch_trigger('commercial.package_booking_components');

-- FR-PKG-06 consumption log: each use of a component (partial quantities
-- allowed) with its share of the allocated revenue and the BOM lines (K6).
CREATE TABLE commercial.package_consumptions (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  booking_id            uuid NOT NULL REFERENCES commercial.package_bookings (id),
  booking_component_id  uuid NOT NULL REFERENCES commercial.package_booking_components (id),
  quantity              numeric(19,4) NOT NULL CHECK (quantity > 0),
  business_date         date NOT NULL,
  outlet_id             uuid,
  reference             text,
  source_type           text,
  source_id             uuid,
  net                   numeric(19,4) NOT NULL DEFAULT 0,
  service               numeric(19,4) NOT NULL DEFAULT 0,
  tax                   numeric(19,4) NOT NULL DEFAULT 0,
  total                 numeric(19,4) NOT NULL DEFAULT 0,
  consumption           jsonb NOT NULL DEFAULT '[]'::jsonb,
  idempotency_key       text,
  consumed_at           timestamptz NOT NULL DEFAULT now(),
  created_by            uuid
);
CREATE UNIQUE INDEX package_consumptions_idempotency ON commercial.package_consumptions (booking_component_id, idempotency_key)
  WHERE idempotency_key IS NOT NULL;
CREATE INDEX package_consumptions_booking ON commercial.package_consumptions (booking_id);
CREATE INDEX package_consumptions_time ON commercial.package_consumptions (property_id, consumed_at);
SELECT platform.enable_property_rls('commercial.package_consumptions');

SELECT platform.grant_app('commercial');

-- +goose Down
DROP TABLE commercial.package_consumptions;
DROP TRIGGER touch_updated_at ON commercial.package_booking_components;
DROP INDEX commercial.package_booking_components_expiry, commercial.package_booking_components_allocation;
ALTER TABLE commercial.package_booking_components DROP CONSTRAINT package_booking_components_quantity_check,
  DROP COLUMN updated_at, DROP COLUMN created_at, DROP COLUMN cancelled_at, DROP COLUMN expires_at, DROP COLUMN optional,
  DROP COLUMN allocation_details, DROP COLUMN allocation_id, DROP COLUMN product_id, DROP COLUMN ref_code, DROP COLUMN resource_type_code,
  DROP COLUMN service_date, DROP COLUMN consumed_quantity;
DROP INDEX commercial.package_bookings_pending, commercial.package_bookings_customer, commercial.package_bookings_folio, commercial.package_bookings_day,
  commercial.package_bookings_source;
CREATE INDEX package_bookings_day ON commercial.package_bookings (package_id, start_at);
CREATE UNIQUE INDEX package_bookings_source ON commercial.package_bookings (source_type, source_id) WHERE source_id IS NOT NULL AND status <> 'cancelled';
ALTER TABLE commercial.package_bookings DROP CONSTRAINT package_bookings_status_check, DROP CONSTRAINT package_bookings_channel_check,
  DROP CONSTRAINT package_bookings_folio_fk;
UPDATE commercial.package_bookings SET status = CASE status WHEN 'pending' THEN 'held' WHEN 'completed' THEN 'consumed' ELSE status END;
ALTER TABLE commercial.package_bookings ADD CONSTRAINT package_bookings_status_check CHECK (status IN ('held', 'confirmed', 'consumed', 'cancelled', 'expired'));
ALTER TABLE commercial.package_bookings DROP COLUMN cancelled_by, DROP COLUMN cancellation_fee, DROP COLUMN completed_at, DROP COLUMN confirmed_at,
  DROP COLUMN policy_refs, DROP COLUMN promo_codes, DROP COLUMN snapshot_id, DROP COLUMN schedule_id, DROP COLUMN customer_folio_id, DROP COLUMN total,
  DROP COLUMN tax_total, DROP COLUMN service_total, DROP COLUMN net_total, DROP COLUMN addons, DROP COLUMN start_date, DROP COLUMN package_code;
ALTER TABLE commercial.package_bookings RENAME COLUMN list_total TO price_total;
ALTER TABLE commercial.package_components DROP CONSTRAINT package_components_addon_price_check,
  DROP CONSTRAINT package_components_allocation_value_check, DROP CONSTRAINT package_components_duration_check,
  DROP COLUMN nights, DROP COLUMN start_time, DROP COLUMN day_offset, DROP COLUMN per_night, DROP COLUMN product_id, DROP COLUMN resource_type_code,
  ALTER COLUMN business_line SET DEFAULT 'other';
ALTER TABLE commercial.package_components DROP CONSTRAINT package_components_component_type_check;
UPDATE commercial.package_components SET component_type = CASE component_type WHEN 'reservation' THEN 'venue' WHEN 'fnb' THEN 'product'
  WHEN 'banquet' THEN 'venue' WHEN 'tee_time' THEN 'tee_time' WHEN 'voucher' THEN 'voucher' ELSE 'service' END;
ALTER TABLE commercial.package_components ADD CONSTRAINT package_components_component_type_check CHECK (component_type IN ('tee_time', 'court', 'class',
  'bungalow', 'vip_suite', 'meeting_room', 'venue', 'golf_cart', 'equipment', 'product', 'voucher', 'service'));
ALTER TABLE commercial.packages DROP CONSTRAINT packages_cancellation_hours_check, DROP CONSTRAINT packages_daily_quota_check,
  DROP CONSTRAINT packages_max_pax_check, DROP CONSTRAINT packages_pricing_mode_check, DROP CONSTRAINT packages_package_type_check;
UPDATE commercial.packages SET pricing_mode = 'per_pax' WHERE pricing_mode = 'per_pax_per_night';
UPDATE commercial.packages SET package_type = 'golf_day' WHERE package_type = 'golf_lunch';
ALTER TABLE commercial.packages ADD CONSTRAINT packages_pricing_mode_check CHECK (pricing_mode IN ('fixed', 'per_pax', 'per_night')),
  ADD CONSTRAINT packages_package_type_check CHECK (package_type IN ('golf_day', 'stay_golf', 'corporate', 'wedding', 'family', 'other')),
  ALTER COLUMN price DROP DEFAULT,
  DROP COLUMN published_by, DROP COLUMN public, DROP COLUMN component_validity_days, DROP COLUMN cancellation_fee_percent, DROP COLUMN valid_to,
  DROP COLUMN valid_from, DROP COLUMN start_time;
ALTER TABLE commercial.orders
  ALTER COLUMN promotion_exclusions DROP DEFAULT,
  ALTER COLUMN promotion_exclusions TYPE uuid[] USING promotion_exclusions::uuid[],
  ALTER COLUMN promotion_exclusions SET DEFAULT '{}';
ALTER TABLE commercial.orders DROP COLUMN promotion_at;
ALTER TABLE commercial.order_lines DROP COLUMN promotions, DROP COLUMN promotion_discount;
DROP INDEX commercial.promotion_redemptions_time, commercial.promotion_redemptions_code, commercial.promotion_redemptions_promotion;
CREATE INDEX promotion_redemptions_promotion ON commercial.promotion_redemptions (promotion_id, created_at);
ALTER TABLE commercial.promotion_redemptions DROP CONSTRAINT promotion_redemptions_source_type_check, DROP COLUMN reverse_reason,
  DROP COLUMN redeemed_at, DROP COLUMN status, DROP COLUMN currency, DROP COLUMN business_line, DROP COLUMN folio_id, DROP COLUMN source_ref,
  DROP COLUMN promo_code, DROP COLUMN promotion_code;
DROP INDEX commercial.promo_codes_campaign, commercial.promo_codes_customer;
ALTER TABLE commercial.promo_codes DROP CONSTRAINT promo_codes_max_uses_per_customer_check, DROP CONSTRAINT promo_codes_max_uses_check,
  DROP CONSTRAINT promo_codes_code_upper_check, DROP COLUMN batch_id, DROP COLUMN campaign_ref;
DROP INDEX commercial.promotions_active;
CREATE INDEX promotions_active ON commercial.promotions (property_id) WHERE status = 'active' AND archived_at IS NULL;
ALTER TABLE commercial.promotions DROP CONSTRAINT promotions_valid_period_check, DROP CONSTRAINT promotions_max_redemptions_check,
  DROP CONSTRAINT promotions_budget_amount_check, DROP CONSTRAINT promotions_min_purchase_check, DROP CONSTRAINT promotions_status_check;
UPDATE commercial.promotions SET status = 'pending_approval' WHERE status = 'pending';
UPDATE commercial.promotions SET status = 'inactive' WHERE status = 'expired';
ALTER TABLE commercial.promotions ADD CONSTRAINT promotions_status_check CHECK (status IN ('draft', 'pending_approval', 'active', 'inactive', 'rejected'));
ALTER TABLE commercial.promotions DROP CONSTRAINT promotions_promo_type_check;
UPDATE commercial.promotions SET promo_type = 'percent_discount' WHERE promo_type = 'promo_code';
ALTER TABLE commercial.promotions ADD CONSTRAINT promotions_promo_type_check CHECK (promo_type IN ('percent_discount', 'amount_discount', 'happy_hour',
  'buy_n_get_x', 'buy_n_price_x', 'bundle', 'member_discount', 'period'));
ALTER TABLE commercial.promotions
  ALTER COLUMN customer_segment_ids DROP DEFAULT,
  ALTER COLUMN customer_segment_ids TYPE uuid[] USING customer_segment_ids::uuid[],
  ALTER COLUMN customer_segment_ids SET DEFAULT '{}',
  ALTER COLUMN product_ids DROP DEFAULT,
  ALTER COLUMN product_ids TYPE uuid[] USING product_ids::uuid[],
  ALTER COLUMN product_ids SET DEFAULT '{}',
  ALTER COLUMN outlet_ids DROP DEFAULT,
  ALTER COLUMN outlet_ids TYPE uuid[] USING outlet_ids::uuid[],
  ALTER COLUMN outlet_ids SET DEFAULT '{}';
ALTER TABLE commercial.promotions DROP COLUMN activated_by, DROP COLUMN max_per_customer, DROP COLUMN stack_group, DROP COLUMN public,
  DROP COLUMN min_quantity, DROP COLUMN day_type_codes, DROP COLUMN day_kinds, DROP COLUMN weekdays, DROP COLUMN membership_types, DROP COLUMN item_refs,
  DROP COLUMN max_discount, DROP COLUMN per_unit, DROP COLUMN terms;
DROP INDEX commercial.pricing_rules_corporate;
ALTER TABLE commercial.pricing_rules DROP COLUMN holiday, DROP COLUMN corporate_account_id;
