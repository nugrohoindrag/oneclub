-- PRD P3 EP-10 Pricing & Promotion Engine and EP-11 Package Management on
-- P1–P2 pricing: promotions (percent / amount discount, happy hour, Buy N
-- Get X, Buy N Price X, bundle, member discount, promo code, period) with
-- scope, stacking and priority, budgets and approval; promo codes; the
-- redemption ledger; cross-line packages with components, versions,
-- all-or-nothing bookings, consumption and revenue allocation. Expand-only.

-- +goose Up
-- ── EP-10 Promotions ──────────────────────────────────────────────────────
CREATE TABLE commercial.promotions (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  code                 text NOT NULL,
  name                 text NOT NULL,
  description          text,
  promo_type           text NOT NULL CHECK (promo_type IN ('percent_discount', 'amount_discount', 'happy_hour', 'buy_n_get_x', 'buy_n_price_x',
                         'bundle', 'member_discount', 'period')),
  channels             text[] NOT NULL DEFAULT '{}',   -- pos, member_app, website, back_office, booking, quotation (empty = all)
  business_lines       text[] NOT NULL DEFAULT '{}',
  service_types        text[] NOT NULL DEFAULT '{}',   -- pricing service types (golf, bungalow …, pos)
  outlet_ids           uuid[] NOT NULL DEFAULT '{}',
  product_ids          uuid[] NOT NULL DEFAULT '{}',
  categories           text[] NOT NULL DEFAULT '{}',   -- product categories / types
  segments             text[] NOT NULL DEFAULT '{}',   -- member, guest, non_member … (empty = everyone)
  customer_segment_ids uuid[] NOT NULL DEFAULT '{}',   -- CRM segments
  time_windows         jsonb NOT NULL DEFAULT '[]'::jsonb, -- [{"days":[1..7],"start":"16:00","end":"19:00"}]
  valid_from           date,
  valid_to             date,
  discount_percent     numeric(9,4) CHECK (discount_percent IS NULL OR (discount_percent > 0 AND discount_percent <= 100)),
  discount_amount      numeric(19,4) CHECK (discount_amount IS NULL OR discount_amount > 0),
  buy_quantity         int CHECK (buy_quantity IS NULL OR buy_quantity > 0),
  get_quantity         int CHECK (get_quantity IS NULL OR get_quantity > 0),
  bundle_price         numeric(19,4) CHECK (bundle_price IS NULL OR bundle_price >= 0),
  bundle_items         jsonb NOT NULL DEFAULT '[]'::jsonb, -- [{"productId": "…", "quantity": 1}]
  min_purchase         numeric(19,4),
  requires_code        boolean NOT NULL DEFAULT false,
  stackable            boolean NOT NULL DEFAULT false,
  priority             int NOT NULL DEFAULT 100,        -- lower = applied first
  budget_amount        numeric(19,4),
  max_redemptions      int,
  used_amount          numeric(19,4) NOT NULL DEFAULT 0,
  used_count           int NOT NULL DEFAULT 0,
  status               text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'pending_approval', 'active', 'inactive', 'rejected')),
  version              int NOT NULL DEFAULT 1,
  approval_request_id  uuid,
  activated_at         timestamptz,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  archived_at          timestamptz,
  UNIQUE (property_id, code)
);
CREATE INDEX promotions_active ON commercial.promotions (property_id) WHERE status = 'active' AND archived_at IS NULL;
SELECT platform.enable_property_rls('commercial.promotions');
SELECT platform.add_touch_trigger('commercial.promotions');

-- FR-PRM-04 promo codes: general or unique per recipient (campaigns).
CREATE TABLE commercial.promo_codes (
  id                     uuid PRIMARY KEY,
  property_id            uuid NOT NULL REFERENCES platform.properties (id),
  promotion_id           uuid NOT NULL REFERENCES commercial.promotions (id),
  code                   text NOT NULL,
  customer_id            uuid REFERENCES crm.customers (id),
  campaign_id            uuid,
  max_uses               int,
  max_uses_per_customer  int,
  used_count             int NOT NULL DEFAULT 0,
  expires_at             timestamptz,
  status                 text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive', 'exhausted')),
  created_at             timestamptz NOT NULL DEFAULT now(),
  created_by             uuid,
  updated_at             timestamptz NOT NULL DEFAULT now(),
  updated_by             uuid,
  UNIQUE (property_id, code)
);
CREATE INDEX promo_codes_promotion ON commercial.promo_codes (promotion_id);
SELECT platform.enable_property_rls('commercial.promo_codes');
SELECT platform.add_touch_trigger('commercial.promo_codes');

-- FR-PRM-07: every application of a promotion with the rule version and the
-- discount per line (immutable; a void reverses it).
CREATE TABLE commercial.promotion_redemptions (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  promotion_id      uuid NOT NULL REFERENCES commercial.promotions (id),
  promotion_version int NOT NULL,
  promo_code_id     uuid REFERENCES commercial.promo_codes (id),
  source_type       text NOT NULL,                  -- pos_order, package_booking, quotation, booking, public
  source_id         uuid NOT NULL,
  customer_id       uuid,
  channel           text NOT NULL,
  discount_amount   numeric(19,4) NOT NULL CHECK (discount_amount >= 0),
  lines             jsonb NOT NULL DEFAULT '[]'::jsonb,
  offline           boolean NOT NULL DEFAULT false,
  needs_review      boolean NOT NULL DEFAULT false,
  reversed_at       timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid
);
CREATE INDEX promotion_redemptions_source ON commercial.promotion_redemptions (source_type, source_id);
CREATE INDEX promotion_redemptions_promotion ON commercial.promotion_redemptions (promotion_id, created_at);
SELECT platform.enable_property_rls('commercial.promotion_redemptions');

-- Pricing snapshots record the promotions applied (FR-PRM-07).
ALTER TABLE commercial.pricing_snapshots ADD COLUMN promotions jsonb NOT NULL DEFAULT '[]'::jsonb;

-- POS: promotion per line, codes and rejected promotions per order, and the
-- offline price the terminal computed (FR-PRM-06, FR-PRM-09).
ALTER TABLE commercial.order_lines ADD COLUMN promotion_id uuid REFERENCES commercial.promotions (id);
ALTER TABLE commercial.orders
  ADD COLUMN promo_codes            text[] NOT NULL DEFAULT '{}',
  ADD COLUMN promotion_exclusions   uuid[] NOT NULL DEFAULT '{}',
  ADD COLUMN client_total           numeric(19,4),
  ADD COLUMN promotion_mismatch     boolean NOT NULL DEFAULT false;

-- ── EP-11 Packages ────────────────────────────────────────────────────────
CREATE TABLE commercial.packages (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  code               text NOT NULL,
  name               text NOT NULL,
  package_type       text NOT NULL DEFAULT 'other' CHECK (package_type IN ('golf_day', 'stay_golf', 'corporate', 'wedding', 'family', 'other')),
  description        text,
  pricing_mode       text NOT NULL DEFAULT 'fixed' CHECK (pricing_mode IN ('fixed', 'per_pax', 'per_night')),
  price              numeric(19,4) NOT NULL CHECK (price >= 0),
  min_pax            int NOT NULL DEFAULT 1 CHECK (min_pax >= 1),
  max_pax            int,
  nights             int NOT NULL DEFAULT 0 CHECK (nights >= 0),
  duration_minutes   int NOT NULL DEFAULT 1440 CHECK (duration_minutes > 0),
  tax_mode           text NOT NULL DEFAULT 'nett' CHECK (tax_mode IN ('nett', 'plus_plus')),
  tax_codes          text[] NOT NULL DEFAULT '{}',
  sell_from          date,
  sell_to            date,
  valid_days         int[] NOT NULL DEFAULT '{}',     -- ISO weekdays the package can start (empty = every day)
  daily_quota        int,
  segments           text[] NOT NULL DEFAULT '{}',
  channels           text[] NOT NULL DEFAULT '{}',    -- back_office, member_app, website, quotation (empty = all)
  allocation_method  text NOT NULL DEFAULT 'standalone' CHECK (allocation_method IN ('standalone', 'fixed', 'percent')),
  cancellation_hours int NOT NULL DEFAULT 48,
  status             text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'inactive')),
  version            int NOT NULL DEFAULT 0,
  published_at       timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  updated_by         uuid,
  archived_at        timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('commercial.packages');
SELECT platform.add_touch_trigger('commercial.packages');

CREATE TABLE commercial.package_components (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  package_id         uuid NOT NULL REFERENCES commercial.packages (id),
  seq                int NOT NULL DEFAULT 1,
  component_type     text NOT NULL CHECK (component_type IN ('tee_time', 'court', 'class', 'bungalow', 'vip_suite', 'meeting_room', 'venue',
                       'golf_cart', 'equipment', 'product', 'voucher', 'service')),
  name               text NOT NULL,
  ref_code           text,           -- resource type / bungalow type / product / voucher type code
  resource_id        uuid,           -- a specific bookable resource
  quantity           numeric(19,4) NOT NULL DEFAULT 1 CHECK (quantity > 0),
  per_pax            boolean NOT NULL DEFAULT false,
  offset_minutes     int NOT NULL DEFAULT 0,   -- start after the package start
  duration_minutes   int,                     -- default: the package duration
  standalone_price   numeric(19,4) NOT NULL DEFAULT 0 CHECK (standalone_price >= 0),
  allocation_value   numeric(19,4),           -- fixed amount or percent (allocation method)
  revenue_component  text NOT NULL DEFAULT 'package',
  business_line      text NOT NULL DEFAULT 'other',
  liability          boolean NOT NULL DEFAULT false,   -- e.g. caddy fee held for caddies
  recipe_id          uuid,                    -- BOM link (P2 recipes) for stock deduction in P4 (K6)
  optional           boolean NOT NULL DEFAULT false,
  addon_price        numeric(19,4),
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  updated_by         uuid
);
CREATE INDEX package_components_package ON commercial.package_components (package_id, seq);
SELECT platform.enable_property_rls('commercial.package_components');
SELECT platform.add_touch_trigger('commercial.package_components');

-- FR-PKG-09: every published version is kept; bookings refer to theirs.
CREATE TABLE commercial.package_versions (
  package_id    uuid NOT NULL REFERENCES commercial.packages (id),
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  version       int NOT NULL,
  snapshot      jsonb NOT NULL,
  published_at  timestamptz NOT NULL DEFAULT now(),
  published_by  uuid,
  PRIMARY KEY (package_id, version)
);
SELECT platform.enable_property_rls('commercial.package_versions');

CREATE TABLE commercial.package_bookings (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  number                text NOT NULL,
  package_id            uuid NOT NULL REFERENCES commercial.packages (id),
  package_version       int NOT NULL,
  customer_id           uuid REFERENCES crm.customers (id),
  corporate_account_id  uuid REFERENCES crm.corporate_accounts (id),
  guest_name            text,
  guest_phone           text,
  guest_email           text,
  start_at              timestamptz NOT NULL,
  end_at                timestamptz NOT NULL,
  pax                   int NOT NULL DEFAULT 1 CHECK (pax >= 1),
  nights                int NOT NULL DEFAULT 0,
  status                text NOT NULL CHECK (status IN ('held', 'confirmed', 'consumed', 'cancelled', 'expired')),
  hold_expires_at       timestamptz,
  currency              char(3) NOT NULL DEFAULT 'IDR',
  price_total           numeric(19,4) NOT NULL,
  discount_total        numeric(19,4) NOT NULL DEFAULT 0,
  folio_id              uuid,
  reservation_id        uuid,
  channel               text NOT NULL DEFAULT 'back_office',
  source_type           text,
  source_id             uuid,
  idempotency_key       text,
  notes                 text,
  cancel_reason         text,
  cancelled_at          timestamptz,
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid,
  UNIQUE (property_id, number)
);
CREATE UNIQUE INDEX package_bookings_idempotency ON commercial.package_bookings (property_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE UNIQUE INDEX package_bookings_source ON commercial.package_bookings (source_type, source_id) WHERE source_id IS NOT NULL AND status <> 'cancelled';
CREATE INDEX package_bookings_day ON commercial.package_bookings (package_id, start_at);
SELECT platform.enable_property_rls('commercial.package_bookings');
SELECT platform.add_touch_trigger('commercial.package_bookings');

CREATE TABLE commercial.package_booking_components (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  booking_id          uuid NOT NULL REFERENCES commercial.package_bookings (id),
  component_id        uuid NOT NULL REFERENCES commercial.package_components (id),
  seq                 int NOT NULL,
  component_type      text NOT NULL,
  name                text NOT NULL,
  quantity            numeric(19,4) NOT NULL,
  scheduled_start     timestamptz,
  scheduled_end       timestamptz,
  resource_id         uuid,
  allocation_ref      text,            -- reservation line, golf hold code, voucher code …
  allocated_net       numeric(19,4) NOT NULL DEFAULT 0,
  allocated_service   numeric(19,4) NOT NULL DEFAULT 0,
  allocated_tax       numeric(19,4) NOT NULL DEFAULT 0,
  allocated_total     numeric(19,4) NOT NULL DEFAULT 0,
  revenue_component   text NOT NULL,
  business_line       text NOT NULL,
  liability           boolean NOT NULL DEFAULT false,
  recipe_id           uuid,
  folio_line_id       uuid,
  status              text NOT NULL DEFAULT 'unused' CHECK (status IN ('unused', 'consumed', 'expired', 'cancelled')),
  consumed_at         timestamptz,
  consumed_ref        text,
  consumed_by         uuid
);
CREATE INDEX package_booking_components_booking ON commercial.package_booking_components (booking_id, seq);
SELECT platform.enable_property_rls('commercial.package_booking_components');

SELECT platform.grant_app('commercial');

-- +goose Down
DROP TABLE commercial.package_booking_components, commercial.package_bookings, commercial.package_versions, commercial.package_components,
  commercial.packages;
ALTER TABLE commercial.orders DROP COLUMN promotion_mismatch, DROP COLUMN client_total, DROP COLUMN promotion_exclusions, DROP COLUMN promo_codes;
ALTER TABLE commercial.order_lines DROP COLUMN promotion_id;
ALTER TABLE commercial.pricing_snapshots DROP COLUMN promotions;
DROP TABLE commercial.promotion_redemptions, commercial.promo_codes, commercial.promotions;
