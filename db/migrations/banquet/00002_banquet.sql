-- Banquet & Event (PRD P3 EP-12 Event Management, EP-13 Banquet, MICE &
-- Wedding, EP-14 Banquet Event Order, EP-15 Banquet Venue & Event
-- Resource). The event is the parent entity (PRD P3 §6 #16); venues are
-- Reservation Engine resources of type banquet_venue (exclusive allocation
-- with setup / teardown buffers), money lives on the event folio in Billing
-- (business line banquet) with a payment schedule (DP, terms, final
-- payment), the BEO is versioned and its menu requirements are published to
-- P4 (contracts K1 and K6, docs/p3-p4-contracts.md).

-- +goose Up
-- Resource types prepared by PRD P2 FR-RSV-01 for P3 (FR-VEN-02).
UPDATE reservation.resource_types SET status = 'active' WHERE code IN ('banquet_venue', 'event');

-- ── master data ───────────────────────────────────────────────────────────
-- Event Types (Product Overview §27): category groups the Back Office menus
-- Events / Banquet / MICE / Weddings; banquet_flow = sales flow with BEO.
CREATE TABLE banquet.event_types (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  code           text NOT NULL,
  name           text NOT NULL,
  category       text NOT NULL DEFAULT 'other' CHECK (category IN ('wedding', 'banquet', 'mice', 'social', 'sport', 'tournament', 'other')),
  banquet_flow   boolean NOT NULL DEFAULT true,
  description    text,
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid,
  archived_at    timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('banquet.event_types');
SELECT platform.add_touch_trigger('banquet.event_types');

-- Venues (FR-VEN-01): ballroom, function rooms, outdoor; a parent venue
-- (whole ballroom) locks its parts and the parts lock the parent (FR-VEN-04).
CREATE TABLE banquet.venues (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  code               text NOT NULL,
  name               text NOT NULL,
  venue_type         text NOT NULL DEFAULT 'function_room' CHECK (venue_type IN ('ballroom', 'function_room', 'outdoor', 'meeting_room', 'vip_suite', 'other')),
  parent_venue_id    uuid REFERENCES banquet.venues (id),
  platform_venue_id  uuid REFERENCES platform.venues (id),
  size_sqm           numeric(9,2),
  max_capacity       int CHECK (max_capacity IS NULL OR max_capacity > 0),
  min_pax            int NOT NULL DEFAULT 0 CHECK (min_pax >= 0),
  addon_price        numeric(19,4) NOT NULL DEFAULT 0 CHECK (addon_price >= 0),
  rental_price       numeric(19,4) NOT NULL DEFAULT 0 CHECK (rental_price >= 0),
  electricity_watt   int NOT NULL DEFAULT 0 CHECK (electricity_watt >= 0),
  facilities         text[] NOT NULL DEFAULT '{}',
  description        text,
  public             boolean NOT NULL DEFAULT true,
  resource_id        uuid REFERENCES reservation.resources (id),
  status             text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  updated_by         uuid,
  archived_at        timestamptz,
  UNIQUE (property_id, code),
  CHECK (parent_venue_id IS NULL OR parent_venue_id <> id)
);
CREATE INDEX venues_parent ON banquet.venues (parent_venue_id);
CREATE UNIQUE INDEX venues_resource ON banquet.venues (resource_id) WHERE resource_id IS NOT NULL;
SELECT platform.enable_property_rls('banquet.venues');
SELECT platform.add_touch_trigger('banquet.venues');

-- Capacity per setup style.
CREATE TABLE banquet.venue_layouts (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  venue_id     uuid NOT NULL REFERENCES banquet.venues (id),
  layout       text NOT NULL CHECK (layout IN ('round_table', 'classroom', 'u_shape', 'theater', 'boardroom', 'standing', 'banquet', 'cocktail')),
  capacity     int NOT NULL CHECK (capacity > 0),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  UNIQUE (venue_id, layout)
);
SELECT platform.enable_property_rls('banquet.venue_layouts');
SELECT platform.add_touch_trigger('banquet.venue_layouts');

-- Menus (buffet, set menu, food stall, coffee break …) with category quotas
-- (FR-BQT-02) and items linked to Commercial products / Inventory recipes
-- (BOM per pax, FR-BQT-11).
CREATE TABLE banquet.menus (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  code           text NOT NULL,
  name           text NOT NULL,
  menu_type      text NOT NULL DEFAULT 'buffet' CHECK (menu_type IN ('buffet', 'set_menu', 'cocktail', 'coffee_break', 'food_stall', 'kids', 'beverage', 'other')),
  price_per_pax  numeric(19,4) NOT NULL DEFAULT 0 CHECK (price_per_pax >= 0),
  pricing_mode   text NOT NULL DEFAULT 'plus_plus' CHECK (pricing_mode IN ('nett', 'plus_plus')),
  tax_codes      text[] NOT NULL DEFAULT '{}',
  outlet_id      uuid REFERENCES commercial.outlets (id),
  description    text,
  public         boolean NOT NULL DEFAULT true,
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid,
  archived_at    timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('banquet.menus');
SELECT platform.add_touch_trigger('banquet.menus');

CREATE TABLE banquet.menu_categories (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  menu_id             uuid NOT NULL REFERENCES banquet.menus (id),
  name                text NOT NULL,
  quota               int NOT NULL DEFAULT 1 CHECK (quota >= 0),
  extra_choice_price  numeric(19,4) CHECK (extra_choice_price IS NULL OR extra_choice_price >= 0),
  sort_order          int NOT NULL DEFAULT 0,
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid,
  updated_at          timestamptz NOT NULL DEFAULT now(),
  updated_by          uuid
);
CREATE INDEX menu_categories_menu ON banquet.menu_categories (menu_id);
SELECT platform.enable_property_rls('banquet.menu_categories');
SELECT platform.add_touch_trigger('banquet.menu_categories');

CREATE TABLE banquet.menu_items (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  menu_id               uuid NOT NULL REFERENCES banquet.menus (id),
  category_id           uuid REFERENCES banquet.menu_categories (id),
  name                  text NOT NULL,
  course                text,
  station               text,
  product_id            uuid REFERENCES commercial.products (id),
  recipe_id             uuid REFERENCES inventory.recipes (id),
  portion_per_pax       numeric(12,4) NOT NULL DEFAULT 1 CHECK (portion_per_pax >= 0),
  serve_offset_minutes  int NOT NULL DEFAULT 0,
  allergens             text[] NOT NULL DEFAULT '{}',
  description           text,
  status                text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid,
  archived_at           timestamptz
);
CREATE INDEX menu_items_menu ON banquet.menu_items (menu_id);
SELECT platform.enable_property_rls('banquet.menu_items');
SELECT platform.add_touch_trigger('banquet.menu_items');

-- Banquet packages (FR-BQT-01): per pax ++ with minimum pax, nett package
-- price for the included pax, or per pax per day (MICE, FR-BQT-13); the
-- inclusions are bundled with the event (FR-BQT-06).
CREATE TABLE banquet.packages (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  code              text NOT NULL,
  name              text NOT NULL,
  category          text NOT NULL DEFAULT 'other' CHECK (category IN ('wedding', 'banquet', 'mice', 'birthday', 'social', 'corporate', 'other')),
  pricing_method    text NOT NULL DEFAULT 'per_pax' CHECK (pricing_method IN ('per_pax', 'per_pax_per_day', 'fixed')),
  price             numeric(19,4) NOT NULL DEFAULT 0 CHECK (price >= 0),
  pricing_mode      text NOT NULL DEFAULT 'plus_plus' CHECK (pricing_mode IN ('nett', 'plus_plus')),
  tax_codes         text[] NOT NULL DEFAULT '{}',
  min_pax           int NOT NULL DEFAULT 0 CHECK (min_pax >= 0),
  included_pax      int NOT NULL DEFAULT 0 CHECK (included_pax >= 0),
  extra_pax_price   numeric(19,4) NOT NULL DEFAULT 0 CHECK (extra_pax_price >= 0),
  extra_pax_mode    text NOT NULL DEFAULT 'plus_plus' CHECK (extra_pax_mode IN ('nett', 'plus_plus')),
  duration_hours    int NOT NULL DEFAULT 4 CHECK (duration_hours > 0),
  electricity_watt  int NOT NULL DEFAULT 0 CHECK (electricity_watt >= 0),
  menu_id           uuid REFERENCES banquet.menus (id),
  inclusions        jsonb NOT NULL DEFAULT '[]'::jsonb,
  revenue_component text NOT NULL DEFAULT 'banquet_package',
  description       text,
  public            boolean NOT NULL DEFAULT true,
  status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  archived_at       timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('banquet.packages');
SELECT platform.add_touch_trigger('banquet.packages');

-- Extra charge types (corkage, outdoor venue add-on, electricity, overtime,
-- decoration, AV, vendor fees, additional food stall …).
CREATE TABLE banquet.charge_types (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  code               text NOT NULL,
  name               text NOT NULL,
  kind               text NOT NULL DEFAULT 'other' CHECK (kind IN ('corkage', 'outdoor_venue', 'electricity', 'overtime', 'decoration',
                       'av_equipment', 'vendor_fee', 'additional_fnb', 'venue_rental', 'damage', 'other')),
  unit               text NOT NULL DEFAULT 'item' CHECK (unit IN ('item', 'bottle', 'hour', 'pax', 'kw', 'package', 'day')),
  unit_price         numeric(19,4) NOT NULL DEFAULT 0 CHECK (unit_price >= 0),
  pricing_mode       text NOT NULL DEFAULT 'plus_plus' CHECK (pricing_mode IN ('nett', 'plus_plus')),
  tax_codes          text[] NOT NULL DEFAULT '{}',
  revenue_component  text,
  description        text,
  status             text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  updated_by         uuid,
  archived_at        timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('banquet.charge_types');
SELECT platform.add_touch_trigger('banquet.charge_types');

-- Outside vendors (FR-EVT-06; vendor payment in P4).
CREATE TABLE banquet.vendors (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  code          text NOT NULL,
  name          text NOT NULL,
  vendor_type   text NOT NULL DEFAULT 'other' CHECK (vendor_type IN ('decoration', 'mc', 'band', 'photographer', 'videographer', 'florist',
                  'wedding_organizer', 'catering', 'av', 'makeup', 'other')),
  contact_name  text,
  phone         text,
  email         text,
  partner       boolean NOT NULL DEFAULT false,
  notes         text,
  status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  updated_by    uuid,
  archived_at   timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('banquet.vendors');
SELECT platform.add_touch_trigger('banquet.vendors');

-- Event checklist templates per event type (FR-EVT-07).
CREATE TABLE banquet.checklist_templates (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  code           text NOT NULL,
  name           text NOT NULL,
  event_type_id  uuid REFERENCES banquet.event_types (id),
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid,
  archived_at    timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('banquet.checklist_templates');
SELECT platform.add_touch_trigger('banquet.checklist_templates');

CREATE TABLE banquet.checklist_template_items (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  template_id  uuid NOT NULL REFERENCES banquet.checklist_templates (id),
  task         text NOT NULL,
  department   text NOT NULL DEFAULT 'banquet' CHECK (department IN ('banquet', 'sales', 'kitchen', 'fnb_service', 'venue', 'engineering',
                 'front_desk', 'golf', 'finance', 'security', 'housekeeping', 'other')),
  days_before  int NOT NULL DEFAULT 7,
  sort_order   int NOT NULL DEFAULT 0,
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid
);
CREATE INDEX checklist_template_items_template ON banquet.checklist_template_items (template_id);
SELECT platform.enable_property_rls('banquet.checklist_template_items');
SELECT platform.add_touch_trigger('banquet.checklist_template_items');

-- ── events ────────────────────────────────────────────────────────────────
-- Event (FR-EVT-01): inquiry → tentative (venue hold until the option date)
-- → definite (down payment paid, or manual with approval) → completed; or
-- cancelled (cancellation tiers of Banquet Policies). A quotation of the
-- banquet lines is one event, whatever its version: the tentative hold of
-- crm.quotation_sent and the conversion of crm.quotation_accepted share it.
CREATE TABLE banquet.events (
  id                     uuid PRIMARY KEY,
  property_id            uuid NOT NULL REFERENCES platform.properties (id),
  number                 text NOT NULL,
  title                  text NOT NULL,
  event_type_id          uuid NOT NULL REFERENCES banquet.event_types (id),
  category               text NOT NULL,
  status                 text NOT NULL DEFAULT 'inquiry' CHECK (status IN ('inquiry', 'tentative', 'definite', 'completed', 'cancelled')),
  customer_id            uuid REFERENCES crm.customers (id),
  corporate_account_id   uuid REFERENCES crm.corporate_accounts (id),
  contact_name           text,
  contact_phone          text,
  contact_email          text,
  sales_owner_id         uuid REFERENCES platform.users (id),
  start_at               timestamptz NOT NULL,
  end_at                 timestamptz NOT NULL,
  expected_pax           int NOT NULL DEFAULT 0 CHECK (expected_pax >= 0),
  guaranteed_pax         int CHECK (guaranteed_pax IS NULL OR guaranteed_pax >= 0),
  final_pax              int CHECK (final_pax IS NULL OR final_pax >= 0),
  pax_deadline           date,
  charged_pax            int NOT NULL DEFAULT 0,
  package_id             uuid REFERENCES banquet.packages (id),
  layout                 text,
  power_watt             int NOT NULL DEFAULT 0 CHECK (power_watt >= 0),
  quotation_id           uuid,                 -- crm quotation version (plain reference: crm owns it)
  quotation_number       text,
  quotation_version      int,
  converted_at           timestamptz,          -- crm.quotation_accepted applied (charges, schedule)
  opportunity_id         uuid,
  lead_id                uuid,
  tournament_id          uuid,                 -- golf/tournament link (PRD P3 §6 #17)
  package_booking_id     uuid,                 -- commercial package booking (commercial.package_booked)
  package_component_id   uuid,
  legacy_ref             text,                 -- migration wave 3 (FR-MIG-P3-01)
  migrated_down_payment  numeric(19,4),
  public                 boolean NOT NULL DEFAULT false,
  registration_open      boolean NOT NULL DEFAULT false,
  capacity               int CHECK (capacity IS NULL OR capacity > 0),
  registration_fee       numeric(19,4) NOT NULL DEFAULT 0 CHECK (registration_fee >= 0),
  members_only           boolean NOT NULL DEFAULT false,
  registration_closes_at timestamptz,
  description            text,
  folio_id               uuid REFERENCES billing.folios (id),
  schedule_id            uuid REFERENCES billing.payment_schedules (id),
  currency               char(3) NOT NULL DEFAULT 'IDR',
  contract_total         numeric(19,4) NOT NULL DEFAULT 0,
  option_date            timestamptz,
  source                 text NOT NULL DEFAULT 'back_office' CHECK (source IN ('back_office', 'quotation', 'website', 'member_app', 'import', 'package')),
  special_requests       text,
  notes                  text,
  definite_at            timestamptz,
  definite_reason        text,
  completed_at           timestamptz,
  cancelled_at           timestamptz,
  cancel_reason          text,
  cancellation_fee       numeric(19,4),
  final_invoice_id       uuid REFERENCES billing.invoices (id),
  final_billed_at        timestamptz,
  settled_at             timestamptz,
  policy_refs            jsonb NOT NULL DEFAULT '[]'::jsonb,
  version                int NOT NULL DEFAULT 1,
  created_at             timestamptz NOT NULL DEFAULT now(),
  created_by             uuid,
  updated_at             timestamptz NOT NULL DEFAULT now(),
  updated_by             uuid,
  UNIQUE (property_id, number),
  CHECK (end_at > start_at)
);
-- one live event per quotation number, however often the crm events are delivered
CREATE UNIQUE INDEX events_quotation ON banquet.events (property_id, quotation_number) WHERE quotation_number IS NOT NULL AND status <> 'cancelled';
CREATE UNIQUE INDEX events_package_component ON banquet.events (package_component_id) WHERE package_component_id IS NOT NULL;
CREATE UNIQUE INDEX events_legacy ON banquet.events (property_id, legacy_ref) WHERE legacy_ref IS NOT NULL;
CREATE INDEX events_time ON banquet.events (property_id, start_at);
CREATE INDEX events_folio ON banquet.events (folio_id);
CREATE INDEX events_option ON banquet.events (option_date) WHERE status = 'tentative';
SELECT platform.enable_property_rls('banquet.events');
SELECT platform.add_touch_trigger('banquet.events');

-- Venue holds (FR-EVT-03, FR-VEN-03): one Reservation Engine reservation per
-- hold; a waitlisted hold has none until it is promoted.
CREATE TABLE banquet.event_venues (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  event_id        uuid NOT NULL REFERENCES banquet.events (id),
  venue_id        uuid NOT NULL REFERENCES banquet.venues (id),
  function_name   text,
  layout          text,
  pax             int,
  start_at        timestamptz NOT NULL,
  end_at          timestamptz NOT NULL,
  status          text NOT NULL CHECK (status IN ('tentative', 'definite', 'waitlisted', 'released', 'expired', 'cancelled', 'completed')),
  reservation_id  uuid REFERENCES reservation.reservations (id),
  waitlist_rank   int,
  option_date     timestamptz,
  promoted_at     timestamptz,
  released_at     timestamptz,
  release_reason  text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  CHECK (end_at > start_at)
);
CREATE INDEX event_venues_event ON banquet.event_venues (event_id);
CREATE INDEX event_venues_venue ON banquet.event_venues (venue_id, start_at);
SELECT platform.enable_property_rls('banquet.event_venues');
SELECT platform.add_touch_trigger('banquet.event_venues');

-- Event Schedule / run-of-show (FR-EVT-02).
CREATE TABLE banquet.event_schedule (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  event_id       uuid NOT NULL REFERENCES banquet.events (id),
  seq            int NOT NULL,
  start_at       timestamptz NOT NULL,
  end_at         timestamptz,
  title          text NOT NULL,
  venue_id       uuid REFERENCES banquet.venues (id),
  owner_user_id  uuid REFERENCES platform.users (id),
  owner_name     text,
  department     text,
  notes          text,
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid
);
CREATE INDEX event_schedule_event ON banquet.event_schedule (event_id, seq);
SELECT platform.enable_property_rls('banquet.event_schedule');

-- Package inclusions bundled with the event (FR-BQT-06): resources held on
-- the Reservation Engine with the event, F&B vouchers issued once Definite.
CREATE TABLE banquet.event_inclusions (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  event_id           uuid NOT NULL REFERENCES banquet.events (id),
  package_id         uuid NOT NULL REFERENCES banquet.packages (id),
  seq                int NOT NULL,
  kind               text NOT NULL CHECK (kind IN ('resource', 'voucher', 'service')),
  label              text NOT NULL,
  resource_type      text,
  quantity           int NOT NULL DEFAULT 1 CHECK (quantity > 0),
  nights             int NOT NULL DEFAULT 0 CHECK (nights >= 0),
  hours              int NOT NULL DEFAULT 0 CHECK (hours >= 0),
  voucher_type_code  text,
  status             text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'held', 'issued', 'unavailable', 'cancelled')),
  voucher_codes      text[] NOT NULL DEFAULT '{}',
  note               text,
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  updated_by         uuid
);
CREATE INDEX event_inclusions_event ON banquet.event_inclusions (event_id, status);
SELECT platform.enable_property_rls('banquet.event_inclusions');
SELECT platform.add_touch_trigger('banquet.event_inclusions');

-- Other resources of the event through the Reservation Engine: bungalow,
-- meeting room, golf cart, equipment (FR-EVT-03, FR-BQT-06).
CREATE TABLE banquet.event_resources (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  event_id        uuid NOT NULL REFERENCES banquet.events (id),
  resource_id     uuid NOT NULL REFERENCES reservation.resources (id),
  reservation_id  uuid REFERENCES reservation.reservations (id),
  inclusion_id    uuid REFERENCES banquet.event_inclusions (id),  -- held for a package inclusion (no charge)
  description     text,
  quantity        int NOT NULL DEFAULT 1 CHECK (quantity > 0),
  start_at        timestamptz NOT NULL,
  end_at          timestamptz NOT NULL,
  status          text NOT NULL DEFAULT 'held' CHECK (status IN ('held', 'confirmed', 'released', 'completed')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid
);
CREATE INDEX event_resources_event ON banquet.event_resources (event_id);
SELECT platform.enable_property_rls('banquet.event_resources');
SELECT platform.add_touch_trigger('banquet.event_resources');

-- Event charges (Event Billing): package, extra pax, menu extras, venue
-- add-on, corkage, electricity, overtime, vendor fees … each one a folio line.
CREATE TABLE banquet.event_charges (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  event_id           uuid NOT NULL REFERENCES banquet.events (id),
  source             text NOT NULL CHECK (source IN ('package', 'extra_pax', 'menu', 'venue_addon', 'extra', 'vendor', 'resource', 'quotation',
                       'adjustment', 'import')),
  charge_type_id     uuid REFERENCES banquet.charge_types (id),
  kind               text,
  ref_id             uuid,                     -- hold, menu, vendor or resource the charge belongs to
  product_id         uuid REFERENCES commercial.products (id),
  description        text NOT NULL,
  quantity           numeric(12,4) NOT NULL DEFAULT 1,
  unit_price         numeric(19,4) NOT NULL DEFAULT 0,
  pricing_mode       text NOT NULL DEFAULT 'plus_plus',
  net_amount         numeric(19,4) NOT NULL DEFAULT 0,
  service_amount     numeric(19,4) NOT NULL DEFAULT 0,
  tax_amount         numeric(19,4) NOT NULL DEFAULT 0,
  total              numeric(19,4) NOT NULL DEFAULT 0,
  revenue_component  text NOT NULL,
  folio_line_id      uuid,
  snapshot_id        uuid,
  status             text NOT NULL DEFAULT 'posted' CHECK (status IN ('posted', 'voided')),
  void_reason        text,
  voided_at          timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  updated_by         uuid
);
CREATE INDEX event_charges_event ON banquet.event_charges (event_id, status);
SELECT platform.enable_property_rls('banquet.event_charges');
SELECT platform.add_touch_trigger('banquet.event_charges');

-- Vendors of an event with service, fee and arrival (FR-EVT-06).
CREATE TABLE banquet.event_vendors (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  event_id            uuid NOT NULL REFERENCES banquet.events (id),
  vendor_id           uuid NOT NULL REFERENCES banquet.vendors (id),
  service             text NOT NULL,
  fee                 numeric(19,4) NOT NULL DEFAULT 0 CHECK (fee >= 0),
  charge_to_customer  boolean NOT NULL DEFAULT false,
  charge_id           uuid REFERENCES banquet.event_charges (id),
  arrival_at          timestamptz,
  notes               text,
  status              text NOT NULL DEFAULT 'confirmed' CHECK (status IN ('confirmed', 'cancelled')),
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid,
  updated_at          timestamptz NOT NULL DEFAULT now(),
  updated_by          uuid
);
CREATE INDEX event_vendors_event ON banquet.event_vendors (event_id);
SELECT platform.enable_property_rls('banquet.event_vendors');
SELECT platform.add_touch_trigger('banquet.event_vendors');

-- Menu selection per event (FR-BQT-02).
CREATE TABLE banquet.event_menu_items (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  event_id      uuid NOT NULL REFERENCES banquet.events (id),
  menu_id       uuid NOT NULL REFERENCES banquet.menus (id),
  menu_item_id  uuid NOT NULL REFERENCES banquet.menu_items (id),
  notes         text,
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid,
  UNIQUE (event_id, menu_item_id)
);
SELECT platform.enable_property_rls('banquet.event_menu_items');

-- Food tasting, technical meeting and site visit (FR-BQT-09); recorded
-- changes are carried into the next BEO.
CREATE TABLE banquet.event_meetings (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  event_id      uuid NOT NULL REFERENCES banquet.events (id),
  kind          text NOT NULL CHECK (kind IN ('food_tasting', 'technical_meeting', 'site_visit')),
  scheduled_at  timestamptz NOT NULL,
  location      text,
  attendees     text[] NOT NULL DEFAULT '{}',
  status        text NOT NULL DEFAULT 'scheduled' CHECK (status IN ('scheduled', 'done', 'cancelled')),
  outcome       text,
  changes       jsonb NOT NULL DEFAULT '[]'::jsonb,
  recorded_at   timestamptz,
  recorded_by   uuid,
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  updated_by    uuid
);
CREATE INDEX event_meetings_event ON banquet.event_meetings (event_id);
SELECT platform.enable_property_rls('banquet.event_meetings');
SELECT platform.add_touch_trigger('banquet.event_meetings');

-- Event checklist with PIC and due date (FR-EVT-07).
CREATE TABLE banquet.event_checklist_items (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  event_id          uuid NOT NULL REFERENCES banquet.events (id),
  template_item_id  uuid REFERENCES banquet.checklist_template_items (id),
  task              text NOT NULL,
  department        text NOT NULL DEFAULT 'banquet',
  owner_user_id     uuid REFERENCES platform.users (id),
  owner_name        text,
  due_date          date,
  status            text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'done')),
  done_at           timestamptz,
  done_by           uuid,
  notes             text,
  reminded_at       timestamptz,
  sort_order        int NOT NULL DEFAULT 0,
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  UNIQUE (event_id, template_item_id)
);
CREATE INDEX event_checklist_due ON banquet.event_checklist_items (property_id, status, due_date);
SELECT platform.enable_property_rls('banquet.event_checklist_items');
SELECT platform.add_touch_trigger('banquet.event_checklist_items');

-- Incident notes of Event Operations (FR-OPS-P3-01).
CREATE TABLE banquet.event_incidents (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  event_id     uuid NOT NULL REFERENCES banquet.events (id),
  severity     text NOT NULL DEFAULT 'low' CHECK (severity IN ('low', 'medium', 'high')),
  note         text NOT NULL,
  reported_by  uuid,
  reporter     text,
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX event_incidents_event ON banquet.event_incidents (event_id);
SELECT platform.enable_property_rls('banquet.event_incidents');

-- Participants / Guest Registration with capacity, waitlist and QR
-- check-in (FR-EVT-04, FR-APP-P3-03, FR-WEB-P3-03).
CREATE TABLE banquet.participants (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  event_id        uuid NOT NULL REFERENCES banquet.events (id),
  customer_id     uuid REFERENCES crm.customers (id),
  name            text NOT NULL,
  email           text,
  phone           text,
  company         text,
  party_size      int NOT NULL DEFAULT 1 CHECK (party_size > 0),
  ticket_code     text NOT NULL UNIQUE,
  status          text NOT NULL CHECK (status IN ('registered', 'waitlisted', 'withdrawn', 'checked_in')),
  waitlist_rank   int,
  source          text NOT NULL DEFAULT 'staff' CHECK (source IN ('staff', 'import', 'website', 'member_app')),
  fee             numeric(19,4) NOT NULL DEFAULT 0,
  payment_status  text NOT NULL DEFAULT 'none' CHECK (payment_status IN ('none', 'unpaid', 'paid', 'refunded')),
  folio_id        uuid REFERENCES billing.folios (id),
  table_no        text,
  vip             boolean NOT NULL DEFAULT false,
  dietary_notes   text,
  registered_at   timestamptz NOT NULL DEFAULT now(),
  promoted_at     timestamptz,
  withdrawn_at    timestamptz,
  checked_in_at   timestamptz,
  checked_in_by   uuid,
  checked_in_via  text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid
);
CREATE INDEX participants_event ON banquet.participants (event_id, status);
CREATE INDEX participants_customer ON banquet.participants (customer_id);
CREATE INDEX participants_folio ON banquet.participants (folio_id);
SELECT platform.enable_property_rls('banquet.participants');
SELECT platform.add_touch_trigger('banquet.participants');

-- ── Banquet Event Order (EP-14) ───────────────────────────────────────────
-- One row per version: issuing v2 supersedes v1 (FR-BEO-02); rev is the
-- ETag of the row (If-Match).
CREATE TABLE banquet.beos (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  event_id         uuid NOT NULL REFERENCES banquet.events (id),
  number           text NOT NULL,
  version          int NOT NULL CHECK (version > 0),
  status           text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'issued', 'superseded')),
  pax              int NOT NULL DEFAULT 0,
  event_date       date NOT NULL,
  outlet_id        uuid REFERENCES commercial.outlets (id),
  content          jsonb NOT NULL DEFAULT '{}'::jsonb,
  instructions     jsonb NOT NULL DEFAULT '{}'::jsonb,
  notes            text,
  requirements     jsonb NOT NULL DEFAULT '[]'::jsonb,
  changes          jsonb NOT NULL DEFAULT '[]'::jsonb,
  revision_reason  text,
  supersedes_id    uuid REFERENCES banquet.beos (id),
  issued_at        timestamptz,
  issued_by        uuid,
  superseded_at    timestamptz,
  locked_at        timestamptz,
  rev              int NOT NULL DEFAULT 1,
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  updated_by       uuid,
  UNIQUE (event_id, version)
);
CREATE INDEX beos_date ON banquet.beos (property_id, event_date, status);
SELECT platform.enable_property_rls('banquet.beos');
SELECT platform.add_touch_trigger('banquet.beos');

-- Distribution to departments with read confirmation per version (FR-BEO-03).
CREATE TABLE banquet.beo_departments (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  beo_id           uuid NOT NULL REFERENCES banquet.beos (id),
  department       text NOT NULL,
  instructions     text,
  notified_at      timestamptz,
  acknowledged_at  timestamptz,
  acknowledged_by  uuid,
  UNIQUE (beo_id, department)
);
SELECT platform.enable_property_rls('banquet.beo_departments');

-- Kitchen "Banquet Production" from the issued BEO (FR-BEO-04).
CREATE TABLE banquet.production_items (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  beo_id        uuid NOT NULL REFERENCES banquet.beos (id),
  event_id      uuid NOT NULL REFERENCES banquet.events (id),
  menu_item_id  uuid REFERENCES banquet.menu_items (id),
  name          text NOT NULL,
  category      text,
  station       text,
  quantity      numeric(12,2) NOT NULL,
  serve_at      timestamptz NOT NULL,
  outlet_id     uuid REFERENCES commercial.outlets (id),
  status        text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'in_progress', 'ready', 'served', 'cancelled')),
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  updated_by    uuid
);
CREATE INDEX production_items_serve ON banquet.production_items (property_id, serve_at);
CREATE INDEX production_items_beo ON banquet.production_items (beo_id);
SELECT platform.enable_property_rls('banquet.production_items');
SELECT platform.add_touch_trigger('banquet.production_items');

SELECT platform.grant_app('banquet');

-- +goose Down
DROP TABLE banquet.production_items;
DROP TABLE banquet.beo_departments;
DROP TABLE banquet.beos;
DROP TABLE banquet.participants;
DROP TABLE banquet.event_incidents;
DROP TABLE banquet.event_checklist_items;
DROP TABLE banquet.event_meetings;
DROP TABLE banquet.event_menu_items;
DROP TABLE banquet.event_vendors;
DROP TABLE banquet.event_charges;
DROP TABLE banquet.event_resources;
DROP TABLE banquet.event_inclusions;
DROP TABLE banquet.event_schedule;
DROP TABLE banquet.event_venues;
DROP TABLE banquet.events;
DROP TABLE banquet.checklist_template_items;
DROP TABLE banquet.checklist_templates;
DROP TABLE banquet.vendors;
DROP TABLE banquet.charge_types;
DROP TABLE banquet.packages;
DROP TABLE banquet.menu_items;
DROP TABLE banquet.menu_categories;
DROP TABLE banquet.menus;
DROP TABLE banquet.venue_layouts;
DROP TABLE banquet.venues;
DROP TABLE banquet.event_types;
UPDATE reservation.resource_types SET status = 'inactive' WHERE code IN ('banquet_venue', 'event');
