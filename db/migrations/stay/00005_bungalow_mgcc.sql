-- Booking & operations of the MGCC bungalows (docs/requirement-booking-
-- hotel-mgcc.md): the website booking engine (cart of several bungalows in
-- one group, unit held while the guest pays, e-voucher and token link), the
-- stay statuses Expired and Void (§12.3), the registration card (identity
-- photo, nationality, signature) and the keys at check-in (FR-H47–H49),
-- room moves (FR-H53), group reservations with a rooming list (FR-H64),
-- restrictions per date (FR-H65), the website content of a room type
-- (FR-H68, FR-H73), the shift handover (FR-H61), the front office night
-- audit (FR-H74), bungalow incidents (FR-H86) and the campaign of a Stay
-- promotion (FR-H78). Expand-only.

-- +goose Up
-- ── stays (§12.3) ─────────────────────────────────────────────────────────
ALTER TABLE stay.stays DROP CONSTRAINT stays_status_check;
ALTER TABLE stay.stays ADD CONSTRAINT stays_status_check
  CHECK (status IN ('requested', 'reserved', 'checked_in', 'checked_out', 'cancelled', 'no_show', 'expired', 'void'));

CREATE TABLE stay.stay_groups (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  group_no              text NOT NULL,
  name                  text NOT NULL,
  customer_id           uuid REFERENCES crm.customers (id),
  contact_name          text,
  contact_phone         text,
  contact_email         text,
  corporate_account_id  uuid REFERENCES crm.corporate_accounts (id),
  folio_mode            text NOT NULL DEFAULT 'per_stay' CHECK (folio_mode IN ('combined', 'per_stay')),
  customer_folio_id     uuid,
  booking_source        text,
  channel               text NOT NULL DEFAULT 'back_office',
  public_token          text,
  notes                 text,
  status                text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'cancelled', 'closed')),
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid,
  UNIQUE (property_id, group_no)
);
CREATE UNIQUE INDEX stay_groups_token ON stay.stay_groups (public_token) WHERE public_token IS NOT NULL;
SELECT platform.enable_property_rls('stay.stay_groups');
SELECT platform.add_touch_trigger('stay.stay_groups');

ALTER TABLE stay.stays
  ADD COLUMN group_id           uuid REFERENCES stay.stay_groups (id),
  ADD COLUMN public_token       text,
  ADD COLUMN occupant_name      text,                 -- the guest staying when not the booker (per bungalow)
  ADD COLUMN booker_title       text CHECK (booker_title IS NULL OR booker_title IN ('mr', 'mrs', 'ms')),
  ADD COLUMN nationality        text,
  ADD COLUMN marketing_consent  boolean NOT NULL DEFAULT false,
  ADD COLUMN id_photo_file_id   uuid REFERENCES platform.files (id),
  ADD COLUMN signature_file_id  uuid REFERENCES platform.files (id),
  ADD COLUMN registered_at      timestamptz,
  ADD COLUMN keys_issued        int NOT NULL DEFAULT 0 CHECK (keys_issued >= 0),
  ADD COLUMN keys_returned      int NOT NULL DEFAULT 0 CHECK (keys_returned >= 0),
  ADD COLUMN key_numbers        text,
  ADD COLUMN early_fee_waive_reason text,
  ADD COLUMN voided_at          timestamptz,
  ADD COLUMN voided_by          uuid,
  ADD COLUMN void_reason        text;
CREATE UNIQUE INDEX stays_public_token ON stay.stays (public_token) WHERE public_token IS NOT NULL;
CREATE INDEX stays_group ON stay.stays (group_id) WHERE group_id IS NOT NULL;

-- Room move in the middle of a stay (FR-H53): one folio, the old bungalow
-- Dirty with a housekeeping task, the price difference only on an upgrade.
CREATE TABLE stay.room_moves (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  stay_id           uuid NOT NULL REFERENCES stay.stays (id),
  from_bungalow_id  uuid NOT NULL REFERENCES stay.bungalows (id),
  to_bungalow_id    uuid NOT NULL REFERENCES stay.bungalows (id),
  reason            text NOT NULL,
  upgrade           boolean NOT NULL DEFAULT false,
  charge            numeric(19,4) NOT NULL DEFAULT 0,
  moved_at          timestamptz NOT NULL DEFAULT now(),
  created_by        uuid
);
CREATE INDEX room_moves_stay ON stay.room_moves (stay_id);
SELECT platform.enable_property_rls('stay.room_moves');

-- ── room types and bungalows on the website (FR-H68, FR-H69, FR-H73) ─────
ALTER TABLE stay.bungalow_types
  ADD COLUMN name_en         text,
  ADD COLUMN description_en  text,
  ADD COLUMN slug            text,
  ADD COLUMN amenities       jsonb NOT NULL DEFAULT '[]'::jsonb,   -- [{"group": "bathroom", "icon": "shower", "label": "…", "labelEn": "…"}]
  ADD COLUMN faq             jsonb NOT NULL DEFAULT '[]'::jsonb,   -- [{"q": "…", "a": "…", "qEn": "…", "aEn": "…"}]
  ADD COLUMN house_rules     text,
  ADD COLUMN on_website      boolean NOT NULL DEFAULT true;
ALTER TABLE stay.bungalows ADD COLUMN smoking boolean NOT NULL DEFAULT false;

-- ── restrictions per date (FR-H65) ───────────────────────────────────────
CREATE TABLE stay.rate_restrictions (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  code                 text NOT NULL,
  bungalow_type_id     uuid REFERENCES stay.bungalow_types (id),   -- NULL: every room type
  start_date           date NOT NULL,
  end_date             date NOT NULL,
  closed               boolean NOT NULL DEFAULT false,             -- stop sale on these nights
  closed_to_arrival    boolean NOT NULL DEFAULT false,
  closed_to_departure  boolean NOT NULL DEFAULT false,
  min_nights           int CHECK (min_nights IS NULL OR min_nights > 0),
  booking_sources      text[] NOT NULL DEFAULT '{}',                -- empty: every channel
  reason               text,
  status               text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  archived_at          timestamptz,
  UNIQUE (property_id, code),
  CHECK (end_date >= start_date)
);
CREATE INDEX rate_restrictions_days ON stay.rate_restrictions (property_id, start_date, end_date) WHERE status = 'active' AND archived_at IS NULL;
SELECT platform.enable_property_rls('stay.rate_restrictions');
SELECT platform.add_touch_trigger('stay.rate_restrictions');

-- ── shift handover of the front desk (FR-H61) ────────────────────────────
CREATE TABLE stay.handover_notes (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  business_date    date NOT NULL,
  category         text NOT NULL DEFAULT 'general' CHECK (category IN ('vip', 'complaint', 'key', 'lost_found', 'payment', 'general')),
  body             text NOT NULL,
  stay_id          uuid REFERENCES stay.stays (id),
  status           text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'done')),
  author_name      text,
  done_at          timestamptz,
  done_by          uuid,
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid
);
CREATE INDEX handover_notes_day ON stay.handover_notes (property_id, business_date DESC);
SELECT platform.enable_property_rls('stay.handover_notes');

-- ── front office night audit (FR-H74) ────────────────────────────────────
CREATE TABLE stay.front_office_audits (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  business_date  date NOT NULL,
  checklist      jsonb NOT NULL DEFAULT '[]'::jsonb,
  figures        jsonb NOT NULL DEFAULT '{}'::jsonb,   -- the Manager Flash of the day, frozen
  notes          text,
  closed_at      timestamptz NOT NULL DEFAULT now(),
  closed_by      uuid,
  UNIQUE (property_id, business_date)
);
SELECT platform.enable_property_rls('stay.front_office_audits');

-- ── bungalow incidents (FR-H86) ──────────────────────────────────────────
CREATE TABLE stay.incidents (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  number          text NOT NULL,
  bungalow_id     uuid REFERENCES stay.bungalows (id),
  stay_id         uuid REFERENCES stay.stays (id),
  category        text NOT NULL DEFAULT 'other' CHECK (category IN ('damage', 'complaint', 'lost_item', 'safety', 'noise', 'other')),
  severity        text NOT NULL DEFAULT 'medium' CHECK (severity IN ('low', 'medium', 'high')),
  status          text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
  description     text NOT NULL,
  action_taken    text,
  damage_amount   numeric(19,4) CHECK (damage_amount IS NULL OR damage_amount >= 0),
  reported_by     text,
  occurred_at     timestamptz NOT NULL DEFAULT now(),
  closed_at       timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  UNIQUE (property_id, number)
);
CREATE INDEX stay_incidents_open ON stay.incidents (property_id, status, occurred_at DESC);
SELECT platform.enable_property_rls('stay.incidents');
SELECT platform.add_touch_trigger('stay.incidents');

-- ── the campaign a Stay promotion code belongs to (FR-H78) ────────────────
ALTER TABLE stay.promotions ADD COLUMN campaign_id uuid REFERENCES crm.campaigns (id);

SELECT platform.grant_app('stay');

-- +goose Down
ALTER TABLE stay.promotions DROP COLUMN campaign_id;
DROP TABLE stay.incidents;
DROP TABLE stay.front_office_audits;
DROP TABLE stay.handover_notes;
DROP TABLE stay.rate_restrictions;
ALTER TABLE stay.bungalows DROP COLUMN smoking;
ALTER TABLE stay.bungalow_types DROP COLUMN name_en, DROP COLUMN description_en, DROP COLUMN slug, DROP COLUMN amenities, DROP COLUMN faq,
  DROP COLUMN house_rules, DROP COLUMN on_website;
DROP TABLE stay.room_moves;
DROP INDEX stay.stays_group;
DROP INDEX stay.stays_public_token;
ALTER TABLE stay.stays DROP COLUMN group_id, DROP COLUMN public_token, DROP COLUMN occupant_name, DROP COLUMN booker_title, DROP COLUMN nationality,
  DROP COLUMN marketing_consent, DROP COLUMN id_photo_file_id, DROP COLUMN signature_file_id, DROP COLUMN registered_at, DROP COLUMN keys_issued,
  DROP COLUMN keys_returned, DROP COLUMN key_numbers, DROP COLUMN early_fee_waive_reason, DROP COLUMN voided_at, DROP COLUMN voided_by, DROP COLUMN void_reason;
DROP TABLE stay.stay_groups;
ALTER TABLE stay.stays DROP CONSTRAINT stays_status_check;
ALTER TABLE stay.stays ADD CONSTRAINT stays_status_check CHECK (status IN ('requested', 'reserved', 'checked_in', 'checked_out', 'cancelled', 'no_show'));
