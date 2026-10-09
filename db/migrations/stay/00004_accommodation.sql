-- Accommodation & Bungalow Management (docs/oneclub-accommodation-bungalow-
-- management-requirements.md): bungalows run like a resort — room type
-- details and rates, the housekeeping status of every unit apart from the
-- reservation status, room blocks / out of order on the Reservation Engine,
-- housekeeping tasks and inspections, maintenance work orders and
-- preventive schedules, guest requests, the waitlist, accommodation rate
-- plans with seasons, add-ons, stay packages and promotions, corporate
-- terms and the guest profile. Expand-only: the P2/P3 columns of stay.*
-- keep their meaning (readiness follows the housekeeping status).

-- +goose Up
-- ── Room types (§8) ───────────────────────────────────────────────────────
ALTER TABLE stay.bungalow_types
  ADD COLUMN photos             text[] NOT NULL DEFAULT '{}',
  ADD COLUMN bed_configuration  text,
  ADD COLUMN size_sqm           numeric(9,2) CHECK (size_sqm IS NULL OR size_sqm > 0),
  ADD COLUMN view               text,
  ADD COLUMN base_rate          numeric(19,4) CHECK (base_rate IS NULL OR base_rate >= 0),
  ADD COLUMN weekend_rate       numeric(19,4) CHECK (weekend_rate IS NULL OR weekend_rate >= 0),
  ADD COLUMN sort_order         int NOT NULL DEFAULT 0;

-- ── Bungalow inventory and housekeeping status (§9, §10) ──────────────────
-- hk_status is the physical condition; Maintenance / Out of Order / Blocked
-- come from the room blocks below; the reservation status from the stays.
ALTER TABLE stay.bungalows
  ADD COLUMN location       text,
  ADD COLUMN capacity       int CHECK (capacity IS NULL OR capacity > 0),
  ADD COLUMN hk_status      text NOT NULL DEFAULT 'ready' CHECK (hk_status IN ('dirty', 'cleaning', 'cleaned', 'inspected', 'ready')),
  ADD COLUMN hk_updated_at  timestamptz,
  ADD COLUMN notes          text;
UPDATE stay.bungalows SET hk_status = CASE WHEN readiness = 'ready' THEN 'ready' ELSE 'dirty' END;

-- Room blocks (§6, §7, §22): Blocked, Maintenance and Out of Order periods.
-- Each block is a reservation of kind "block" in the Reservation Engine, so
-- the EXCLUDE constraint keeps the unit off sale for the period.
CREATE TABLE stay.room_blocks (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  block_no        text NOT NULL,
  bungalow_id     uuid NOT NULL REFERENCES stay.bungalows (id),
  kind            text NOT NULL CHECK (kind IN ('blocked', 'maintenance', 'out_of_order')),
  start_at        timestamptz NOT NULL,
  end_at          timestamptz NOT NULL,
  reason          text NOT NULL,
  reservation_id  uuid REFERENCES reservation.reservations (id),
  work_order_id   uuid,
  status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'released')),
  released_at     timestamptz,
  released_by     uuid,
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  UNIQUE (property_id, block_no),
  CHECK (end_at > start_at)
);
CREATE INDEX room_blocks_unit ON stay.room_blocks (bungalow_id, start_at) WHERE status = 'active';
SELECT platform.enable_property_rls('stay.room_blocks');
SELECT platform.add_touch_trigger('stay.room_blocks');

-- ── Stays (§4, §17–§19, §28, §29) ─────────────────────────────────────────
ALTER TABLE stay.stays
  ADD COLUMN booking_source        text CHECK (booking_source IN ('website', 'member_app', 'guest_app', 'front_desk', 'phone', 'walk_in', 'corporate')),
  ADD COLUMN corporate_account_id  uuid REFERENCES crm.corporate_accounts (id),
  ADD COLUMN billing_arrangement   text CHECK (billing_arrangement IN ('guest_pays', 'company_pays_room', 'company_pays_all')),
  ADD COLUMN stay_package_id       uuid,
  ADD COLUMN promotion_id          uuid,
  ADD COLUMN promotion_code        text,
  ADD COLUMN discount              numeric(19,4) NOT NULL DEFAULT 0,
  ADD COLUMN notes                 text,
  ADD COLUMN vip                   boolean NOT NULL DEFAULT false,
  ADD COLUMN expected_arrival      text,
  ADD COLUMN early_check_in        boolean NOT NULL DEFAULT false,
  ADD COLUMN late_check_out_until  timestamptz,
  ADD COLUMN checked_in_by         uuid,
  ADD COLUMN checked_out_by        uuid,
  ADD COLUMN cancelled_at          timestamptz,
  ADD COLUMN cancel_reason         text,
  ADD COLUMN no_show_fee           numeric(19,4),
  ADD COLUMN waitlist_id           uuid,
  ADD COLUMN updated_reason        text,
  ADD COLUMN checkin_reminded_at   timestamptz,
  ADD COLUMN checkout_reminded_at  timestamptz;
UPDATE stay.stays SET booking_source = CASE channel WHEN 'website' THEN 'website' WHEN 'member_app' THEN 'member_app' ELSE 'front_desk' END;
CREATE INDEX stays_corporate ON stay.stays (corporate_account_id) WHERE corporate_account_id IS NOT NULL;
CREATE INDEX stays_customer ON stay.stays (customer_id, start_at DESC);

-- ── Housekeeping (§20, §21) ───────────────────────────────────────────────
CREATE TABLE stay.housekeeping_tasks (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  task_no           text NOT NULL,
  bungalow_id       uuid NOT NULL REFERENCES stay.bungalows (id),
  stay_id           uuid REFERENCES stay.stays (id),
  task_type         text NOT NULL CHECK (task_type IN ('checkout_cleaning', 'stayover_cleaning', 'deep_cleaning', 'turndown', 'inspection', 'other')),
  priority          text NOT NULL DEFAULT 'normal' CHECK (priority IN ('low', 'normal', 'high', 'urgent')),
  task_date         date NOT NULL,
  assigned_to       text,
  assignee_user_id  uuid,
  status            text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'in_progress', 'completed', 'inspected', 'cancelled')),
  started_at        timestamptz,
  completed_at      timestamptz,
  inspected_at      timestamptz,
  reworks           int NOT NULL DEFAULT 0,
  checklist         jsonb NOT NULL DEFAULT '[]'::jsonb,
  notes             text,
  source            text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'check_out', 'guest_request', 'schedule', 'inspection')),
  guest_request_id  uuid,
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  UNIQUE (property_id, task_no)
);
CREATE INDEX housekeeping_tasks_day ON stay.housekeeping_tasks (property_id, task_date);
CREATE INDEX housekeeping_tasks_unit ON stay.housekeeping_tasks (bungalow_id, created_at DESC);
SELECT platform.enable_property_rls('stay.housekeeping_tasks');
SELECT platform.add_touch_trigger('stay.housekeeping_tasks');

CREATE TABLE stay.room_inspections (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  bungalow_id     uuid NOT NULL REFERENCES stay.bungalows (id),
  task_id         uuid REFERENCES stay.housekeeping_tasks (id),
  inspector_id    uuid,
  inspector_name  text,
  result          text NOT NULL CHECK (result IN ('passed', 'failed')),
  checklist       jsonb NOT NULL DEFAULT '[]'::jsonb,
  notes           text,
  inspected_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX room_inspections_unit ON stay.room_inspections (bungalow_id, inspected_at DESC);
SELECT platform.enable_property_rls('stay.room_inspections');

-- ── Maintenance (§22) ─────────────────────────────────────────────────────
CREATE TABLE stay.preventive_schedules (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  code            text NOT NULL,
  name            text NOT NULL,
  category        text NOT NULL DEFAULT 'other' CHECK (category IN ('ac', 'plumbing', 'electrical', 'water_heater', 'furniture', 'appliance', 'structure', 'pest', 'other')),
  bungalow_id     uuid REFERENCES stay.bungalows (id),   -- NULL: every active bungalow
  interval_days   int NOT NULL CHECK (interval_days > 0),
  next_due        date NOT NULL,
  closes_unit     boolean NOT NULL DEFAULT false,
  duration_hours  int NOT NULL DEFAULT 2 CHECK (duration_hours > 0),
  assigned_to     text,
  notes           text,
  last_generated  date,
  status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  archived_at     timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('stay.preventive_schedules');
SELECT platform.add_touch_trigger('stay.preventive_schedules');

CREATE TABLE stay.work_orders (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  wo_no             text NOT NULL,
  bungalow_id       uuid REFERENCES stay.bungalows (id),
  category          text NOT NULL DEFAULT 'other' CHECK (category IN ('ac', 'plumbing', 'electrical', 'water_heater', 'furniture', 'appliance', 'structure', 'pest', 'other')),
  title             text NOT NULL,
  description       text,
  priority          text NOT NULL DEFAULT 'normal' CHECK (priority IN ('low', 'normal', 'high', 'urgent')),
  status            text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'assigned', 'in_progress', 'resolved', 'closed', 'cancelled')),
  source            text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'guest_request', 'inspection', 'housekeeping', 'preventive')),
  assigned_to       text,
  reported_by       text,
  closes_unit       boolean NOT NULL DEFAULT false,
  block_id          uuid REFERENCES stay.room_blocks (id),
  expected_end_at   timestamptz,
  schedule_id       uuid REFERENCES stay.preventive_schedules (id),
  due_date          date,
  guest_request_id  uuid,
  cost              numeric(19,4),
  resolution        text,
  assigned_at       timestamptz,
  started_at        timestamptz,
  resolved_at       timestamptz,
  closed_at         timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  UNIQUE (property_id, wo_no)
);
CREATE INDEX work_orders_open ON stay.work_orders (property_id, status);
CREATE INDEX work_orders_unit ON stay.work_orders (bungalow_id, created_at DESC);
SELECT platform.enable_property_rls('stay.work_orders');
SELECT platform.add_touch_trigger('stay.work_orders');
ALTER TABLE stay.room_blocks ADD CONSTRAINT room_blocks_work_order_fk FOREIGN KEY (work_order_id) REFERENCES stay.work_orders (id);

-- ── Add-ons and stay packages (§13) ───────────────────────────────────────
CREATE TABLE stay.addons (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  code            text NOT NULL,
  name            text NOT NULL,
  description     text,
  category        text NOT NULL DEFAULT 'other' CHECK (category IN ('breakfast', 'extra_bed', 'extra_pillow', 'extra_towel', 'bbq', 'dinner', 'lunch',
                    'transportation', 'laundry', 'other')),
  price           numeric(19,4) NOT NULL CHECK (price >= 0),
  unit            text NOT NULL DEFAULT 'per_item' CHECK (unit IN ('per_stay', 'per_night', 'per_item', 'per_person', 'per_person_night')),
  pricing_mode    text NOT NULL DEFAULT 'plus_plus' CHECK (pricing_mode IN ('nett', 'plus_plus')),
  taxable         boolean NOT NULL DEFAULT true,
  service_charge  boolean NOT NULL DEFAULT true,
  availability    text NOT NULL DEFAULT 'both' CHECK (availability IN ('booking', 'in_stay', 'both')),
  max_quantity    int CHECK (max_quantity IS NULL OR max_quantity > 0),
  status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  archived_at     timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('stay.addons');
SELECT platform.add_touch_trigger('stay.addons');

CREATE TABLE stay.packages (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  code               text NOT NULL,
  name               text NOT NULL,
  description        text,
  price_mode         text NOT NULL DEFAULT 'fixed' CHECK (price_mode IN ('fixed', 'room_plus')),
  price              numeric(19,4) CHECK (price IS NULL OR price >= 0),   -- fixed: per night for room + inclusions
  inclusion_discount numeric(9,4) CHECK (inclusion_discount IS NULL OR (inclusion_discount >= 0 AND inclusion_discount <= 100)),
  inclusions         jsonb NOT NULL DEFAULT '[]'::jsonb,                    -- [{"addonId": "…", "quantity": 1}] priced per the add-on unit
  bungalow_type_ids  text[] NOT NULL DEFAULT '{}',
  min_nights         int NOT NULL DEFAULT 1 CHECK (min_nights > 0),
  max_nights         int CHECK (max_nights IS NULL OR max_nights > 0),
  includes_breakfast boolean NOT NULL DEFAULT false,
  valid_from         date,
  valid_to           date,
  photo              text,
  status             text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  updated_by         uuid,
  archived_at        timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('stay.packages');
SELECT platform.add_touch_trigger('stay.packages');

CREATE TABLE stay.stay_addons (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  stay_id        uuid NOT NULL REFERENCES stay.stays (id),
  addon_id       uuid NOT NULL REFERENCES stay.addons (id),
  name           text NOT NULL,
  quantity       int NOT NULL CHECK (quantity > 0),
  units          int NOT NULL DEFAULT 1,              -- nights / persons multiplier of the add-on unit
  unit_price     numeric(19,4) NOT NULL,
  total          numeric(19,4) NOT NULL,
  included       boolean NOT NULL DEFAULT false,      -- part of a stay package
  folio_line_id  uuid REFERENCES billing.folio_lines (id),
  source         text NOT NULL DEFAULT 'booking' CHECK (source IN ('booking', 'front_desk', 'guest_request', 'package')),
  voided_at      timestamptz,
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid
);
CREATE INDEX stay_addons_stay ON stay.stay_addons (stay_id);
SELECT platform.enable_property_rls('stay.stay_addons');

-- ── Rates (§11, §12) ──────────────────────────────────────────────────────
-- The Best Available Rate of a night is the season price of the room type
-- (highest priority season), else the type's base / weekend rate; a rate
-- plan is the BAR, a percentage or amount from it, or its own fixed prices.
CREATE TABLE stay.rate_plans (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  code                 text NOT NULL,
  name                 text NOT NULL,
  description          text,
  plan_type            text NOT NULL DEFAULT 'standard' CHECK (plan_type IN ('standard', 'weekend', 'peak_season', 'holiday', 'member', 'corporate',
                         'promotional', 'other')),
  derivation           text NOT NULL DEFAULT 'bar' CHECK (derivation IN ('bar', 'percent', 'amount', 'fixed')),
  adjust_value         numeric(19,4) NOT NULL DEFAULT 0,   -- percent / amount from the BAR (negative = discount)
  pricing_mode         text NOT NULL DEFAULT 'nett' CHECK (pricing_mode IN ('nett', 'plus_plus')),
  includes_breakfast   boolean NOT NULL DEFAULT false,
  min_nights           int NOT NULL DEFAULT 1 CHECK (min_nights > 0),
  max_nights           int CHECK (max_nights IS NULL OR max_nights > 0),
  weekend_only         boolean NOT NULL DEFAULT false,
  free_cancel_hours    int NOT NULL DEFAULT 24 CHECK (free_cancel_hours >= 0),
  cancel_fee_percent   numeric(9,4) NOT NULL DEFAULT 50 CHECK (cancel_fee_percent BETWEEN 0 AND 100),
  no_show_fee_percent  numeric(9,4) NOT NULL DEFAULT 100 CHECK (no_show_fee_percent BETWEEN 0 AND 100),
  non_refundable       boolean NOT NULL DEFAULT false,
  deposit_percent      numeric(9,4) CHECK (deposit_percent IS NULL OR deposit_percent BETWEEN 0 AND 100),
  payment_policy       text NOT NULL DEFAULT 'deposit' CHECK (payment_policy IN ('deposit', 'full_prepayment', 'pay_at_hotel')),
  eligibility          text NOT NULL DEFAULT 'everyone' CHECK (eligibility IN ('everyone', 'member', 'corporate')),
  booking_sources      text[] NOT NULL DEFAULT '{}',
  facility_access      text[] NOT NULL DEFAULT '{}',        -- facility types staying guests enter free (* = all)
  valid_from           date,
  valid_to             date,
  is_default           boolean NOT NULL DEFAULT false,
  sort_order           int NOT NULL DEFAULT 0,
  status               text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  archived_at          timestamptz,
  UNIQUE (property_id, code),
  CHECK (valid_to IS NULL OR valid_from IS NULL OR valid_to >= valid_from)
);
SELECT platform.enable_property_rls('stay.rate_plans');
SELECT platform.add_touch_trigger('stay.rate_plans');

CREATE TABLE stay.rate_plan_prices (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  rate_plan_id      uuid NOT NULL REFERENCES stay.rate_plans (id),
  bungalow_type_id  uuid NOT NULL REFERENCES stay.bungalow_types (id),
  weekday_price     numeric(19,4) NOT NULL CHECK (weekday_price >= 0),
  weekend_price     numeric(19,4) CHECK (weekend_price IS NULL OR weekend_price >= 0),
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  UNIQUE (rate_plan_id, bungalow_type_id)
);
SELECT platform.enable_property_rls('stay.rate_plan_prices');
SELECT platform.add_touch_trigger('stay.rate_plan_prices');

CREATE TABLE stay.seasons (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  code            text NOT NULL,
  name            text NOT NULL,
  season_type     text NOT NULL DEFAULT 'normal' CHECK (season_type IN ('normal', 'low', 'peak', 'holiday', 'event')),
  start_date      date NOT NULL,
  end_date        date NOT NULL,
  priority        int NOT NULL DEFAULT 100,           -- lower wins when seasons overlap
  adjust_percent  numeric(9,4),                       -- uplift of the base rate when the type has no season price
  status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  archived_at     timestamptz,
  UNIQUE (property_id, code),
  CHECK (end_date >= start_date)
);
SELECT platform.enable_property_rls('stay.seasons');
SELECT platform.add_touch_trigger('stay.seasons');

CREATE TABLE stay.season_prices (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  season_id         uuid NOT NULL REFERENCES stay.seasons (id),
  bungalow_type_id  uuid NOT NULL REFERENCES stay.bungalow_types (id),
  weekday_price     numeric(19,4) NOT NULL CHECK (weekday_price >= 0),
  weekend_price     numeric(19,4) CHECK (weekend_price IS NULL OR weekend_price >= 0),
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  UNIQUE (season_id, bungalow_type_id)
);
SELECT platform.enable_property_rls('stay.season_prices');
SELECT platform.add_touch_trigger('stay.season_prices');

-- ── Promotions (§31) ──────────────────────────────────────────────────────
CREATE TABLE stay.promotions (
  id                     uuid PRIMARY KEY,
  property_id            uuid NOT NULL REFERENCES platform.properties (id),
  code                   text NOT NULL,
  name                   text NOT NULL,
  description            text,
  promo_type             text NOT NULL CHECK (promo_type IN ('percent', 'fixed', 'stay_pay', 'early_booking', 'last_minute', 'long_stay', 'weekend',
                           'holiday', 'corporate')),
  discount_percent       numeric(9,4) CHECK (discount_percent IS NULL OR (discount_percent > 0 AND discount_percent <= 100)),
  discount_amount        numeric(19,4) CHECK (discount_amount IS NULL OR discount_amount > 0),
  stay_nights            int CHECK (stay_nights IS NULL OR stay_nights > 1),   -- Stay X …
  pay_nights             int CHECK (pay_nights IS NULL OR pay_nights > 0),     -- … Pay Y
  min_days_before        int CHECK (min_days_before IS NULL OR min_days_before >= 0),
  max_days_before        int CHECK (max_days_before IS NULL OR max_days_before >= 0),
  min_nights             int CHECK (min_nights IS NULL OR min_nights > 0),
  max_nights             int CHECK (max_nights IS NULL OR max_nights > 0),
  bungalow_type_ids      text[] NOT NULL DEFAULT '{}',
  rate_plan_codes        text[] NOT NULL DEFAULT '{}',
  booking_sources        text[] NOT NULL DEFAULT '{}',
  corporate_account_ids  text[] NOT NULL DEFAULT '{}',
  book_from              date,
  book_to                date,
  stay_from              date,
  stay_to                date,
  promo_code             text,
  usage_limit            int CHECK (usage_limit IS NULL OR usage_limit > 0),
  used_count             int NOT NULL DEFAULT 0,
  priority               int NOT NULL DEFAULT 100,
  status                 text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at             timestamptz NOT NULL DEFAULT now(),
  created_by             uuid,
  updated_at             timestamptz NOT NULL DEFAULT now(),
  updated_by             uuid,
  archived_at            timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('stay.promotions');
SELECT platform.add_touch_trigger('stay.promotions');

CREATE TABLE stay.promotion_redemptions (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  promotion_id  uuid NOT NULL REFERENCES stay.promotions (id),
  stay_id       uuid NOT NULL REFERENCES stay.stays (id),
  discount      numeric(19,4) NOT NULL,
  status        text NOT NULL DEFAULT 'applied' CHECK (status IN ('applied', 'reversed')),
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX promotion_redemptions_promo ON stay.promotion_redemptions (promotion_id);
SELECT platform.enable_property_rls('stay.promotion_redemptions');

-- ── Corporate booking (§29) ───────────────────────────────────────────────
CREATE TABLE stay.corporate_terms (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  corporate_account_id  uuid NOT NULL REFERENCES crm.corporate_accounts (id),
  rate_plan_code        text,
  billing_arrangement   text NOT NULL DEFAULT 'company_pays_room' CHECK (billing_arrangement IN ('guest_pays', 'company_pays_room', 'company_pays_all')),
  max_rooms_per_night   int CHECK (max_rooms_per_night IS NULL OR max_rooms_per_night > 0),
  monthly_room_nights   int CHECK (monthly_room_nights IS NULL OR monthly_room_nights > 0),
  payment_terms_days    int NOT NULL DEFAULT 30 CHECK (payment_terms_days >= 0),
  notes                 text,
  status                text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid,
  UNIQUE (property_id, corporate_account_id)
);
SELECT platform.enable_property_rls('stay.corporate_terms');
SELECT platform.add_touch_trigger('stay.corporate_terms');

-- ── Guests (§14), guest requests (§23), waitlist (§30) ────────────────────
CREATE TABLE stay.guest_profiles (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  customer_id       uuid NOT NULL REFERENCES crm.customers (id),
  address           text,
  id_type           text CHECK (id_type IN ('ktp', 'passport', 'sim', 'kitas', 'other')),
  id_number_masked  text,
  date_of_birth     date,
  nationality       text,
  preferences       text,
  notes             text,
  vip               boolean NOT NULL DEFAULT false,
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  UNIQUE (property_id, customer_id)
);
SELECT platform.enable_property_rls('stay.guest_profiles');
SELECT platform.add_touch_trigger('stay.guest_profiles');

CREATE TABLE stay.guest_requests (
  id                     uuid PRIMARY KEY,
  property_id            uuid NOT NULL REFERENCES platform.properties (id),
  request_no             text NOT NULL,
  stay_id                uuid NOT NULL REFERENCES stay.stays (id),
  bungalow_id            uuid REFERENCES stay.bungalows (id),
  request_type           text NOT NULL CHECK (request_type IN ('extra_towel', 'extra_bed', 'room_cleaning', 'maintenance', 'laundry', 'transportation',
                           'food_beverage', 'other')),
  description            text,
  quantity               int NOT NULL DEFAULT 1 CHECK (quantity > 0),
  addon_id               uuid REFERENCES stay.addons (id),
  charge_amount          numeric(19,4) CHECK (charge_amount IS NULL OR charge_amount >= 0),
  status                 text NOT NULL DEFAULT 'requested' CHECK (status IN ('requested', 'assigned', 'in_progress', 'completed', 'cancelled')),
  assigned_to            text,
  source                 text NOT NULL DEFAULT 'front_desk' CHECK (source IN ('front_desk', 'phone', 'member_app', 'guest_app')),
  scheduled_for          timestamptz,
  assigned_at            timestamptz,
  started_at             timestamptz,
  completed_at           timestamptz,
  folio_line_id          uuid REFERENCES billing.folio_lines (id),
  housekeeping_task_id   uuid REFERENCES stay.housekeeping_tasks (id),
  work_order_id          uuid REFERENCES stay.work_orders (id),
  notes                  text,
  created_at             timestamptz NOT NULL DEFAULT now(),
  created_by             uuid,
  updated_at             timestamptz NOT NULL DEFAULT now(),
  updated_by             uuid,
  UNIQUE (property_id, request_no)
);
CREATE INDEX guest_requests_stay ON stay.guest_requests (stay_id);
CREATE INDEX guest_requests_open ON stay.guest_requests (property_id, status);
SELECT platform.enable_property_rls('stay.guest_requests');
SELECT platform.add_touch_trigger('stay.guest_requests');

CREATE TABLE stay.waitlist (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  waitlist_no       text NOT NULL,
  customer_id       uuid REFERENCES crm.customers (id),
  guest_name        text NOT NULL,
  guest_phone       text,
  guest_email       text,
  bungalow_type_id  uuid NOT NULL REFERENCES stay.bungalow_types (id),
  arrival_date      date NOT NULL,
  nights            int NOT NULL CHECK (nights > 0),
  adults            int NOT NULL DEFAULT 1 CHECK (adults > 0),
  children          int NOT NULL DEFAULT 0 CHECK (children >= 0),
  rate_plan_code    text,
  priority          text NOT NULL DEFAULT 'normal' CHECK (priority IN ('low', 'normal', 'high', 'vip')),
  booking_source    text NOT NULL DEFAULT 'front_desk',
  status            text NOT NULL DEFAULT 'waiting' CHECK (status IN ('waiting', 'offered', 'converted', 'cancelled', 'expired')),
  offered_at        timestamptz,
  stay_id           uuid REFERENCES stay.stays (id),
  notes             text,
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  UNIQUE (property_id, waitlist_no)
);
CREATE INDEX waitlist_open ON stay.waitlist (property_id, status, arrival_date);
SELECT platform.enable_property_rls('stay.waitlist');
SELECT platform.add_touch_trigger('stay.waitlist');

SELECT platform.grant_app('stay');

-- +goose Down
DROP TABLE stay.waitlist;
DROP TABLE stay.guest_requests;
DROP TABLE stay.guest_profiles;
DROP TABLE stay.corporate_terms;
DROP TABLE stay.promotion_redemptions;
DROP TABLE stay.promotions;
DROP TABLE stay.season_prices;
DROP TABLE stay.seasons;
DROP TABLE stay.rate_plan_prices;
DROP TABLE stay.rate_plans;
DROP TABLE stay.stay_addons;
DROP TABLE stay.packages;
DROP TABLE stay.addons;
ALTER TABLE stay.room_blocks DROP CONSTRAINT room_blocks_work_order_fk;
DROP TABLE stay.work_orders;
DROP TABLE stay.preventive_schedules;
DROP TABLE stay.room_inspections;
DROP TABLE stay.housekeeping_tasks;
DROP INDEX stay.stays_customer;
DROP INDEX stay.stays_corporate;
ALTER TABLE stay.stays DROP COLUMN booking_source, DROP COLUMN corporate_account_id, DROP COLUMN billing_arrangement, DROP COLUMN stay_package_id,
  DROP COLUMN promotion_id, DROP COLUMN promotion_code, DROP COLUMN discount, DROP COLUMN notes, DROP COLUMN vip, DROP COLUMN expected_arrival,
  DROP COLUMN early_check_in, DROP COLUMN late_check_out_until, DROP COLUMN checked_in_by, DROP COLUMN checked_out_by, DROP COLUMN cancelled_at,
  DROP COLUMN cancel_reason, DROP COLUMN no_show_fee, DROP COLUMN waitlist_id, DROP COLUMN updated_reason, DROP COLUMN checkin_reminded_at,
  DROP COLUMN checkout_reminded_at;
DROP TABLE stay.room_blocks;
ALTER TABLE stay.bungalows DROP COLUMN location, DROP COLUMN capacity, DROP COLUMN hk_status, DROP COLUMN hk_updated_at, DROP COLUMN notes;
ALTER TABLE stay.bungalow_types DROP COLUMN photos, DROP COLUMN bed_configuration, DROP COLUMN size_sqm, DROP COLUMN view, DROP COLUMN base_rate,
  DROP COLUMN weekend_rate, DROP COLUMN sort_order;
