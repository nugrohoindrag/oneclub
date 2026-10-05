-- PRD P3 fulfilment gaps: bungalow / VIP suite / meeting room stays made
-- from the reservation components of commercial packages (FR-PKG-04/06,
-- §9.2; plain references, commercial owns the package booking) and the
-- nightly room charge posted by the night audit (FR-EOD-03, §9.5): a stay
-- booked under Stay Policies "nightly" keeps its priced room quote and the
-- night audit (or the check-out) posts one night at a time, once per night.
-- Additive only.

-- +goose Up
ALTER TABLE stay.stays
  ADD COLUMN package_booking_id   uuid,
  ADD COLUMN package_component_id uuid,
  ADD COLUMN room_posting         text NOT NULL DEFAULT 'at_booking' CHECK (room_posting IN ('at_booking', 'nightly', 'package')),
  ADD COLUMN room_quote           jsonb;
CREATE UNIQUE INDEX stays_package_unit ON stay.stays (package_component_id, unit_id) WHERE package_component_id IS NOT NULL;
CREATE INDEX stays_package ON stay.stays (package_booking_id) WHERE package_booking_id IS NOT NULL;
CREATE INDEX stays_in_house ON stay.stays (property_id, status, room_posting);

CREATE TABLE stay.night_postings (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  stay_id        uuid NOT NULL REFERENCES stay.stays (id),
  night          date NOT NULL,
  business_date  date NOT NULL,
  folio_line_id  uuid REFERENCES billing.folio_lines (id),
  net            numeric(19,4) NOT NULL,
  service        numeric(19,4) NOT NULL DEFAULT 0,
  tax            numeric(19,4) NOT NULL DEFAULT 0,
  total          numeric(19,4) NOT NULL,
  source         text NOT NULL CHECK (source IN ('night_audit', 'check_out')),
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  UNIQUE (stay_id, night)
);
SELECT platform.enable_property_rls('stay.night_postings');

SELECT platform.grant_app('stay');

-- +goose Down
DROP TABLE stay.night_postings;
DROP INDEX stay.stays_in_house;
DROP INDEX stay.stays_package;
DROP INDEX stay.stays_package_unit;
ALTER TABLE stay.stays DROP COLUMN package_booking_id, DROP COLUMN package_component_id, DROP COLUMN room_posting, DROP COLUMN room_quote;
