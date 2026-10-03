-- Resource foundation entity and the anti double-booking allocation table
-- (FR-TEC-06, Technical Doc §7.3), prepared for P1.

-- +goose Up
CREATE SCHEMA IF NOT EXISTS reservation;

CREATE TABLE reservation.resources (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  code           text NOT NULL,
  name           text NOT NULL,
  resource_type  text NOT NULL DEFAULT 'other',
  venue_id       uuid REFERENCES platform.venues (id),
  capacity       int CHECK (capacity IS NULL OR capacity > 0),
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  attributes     jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid,
  archived_at    timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('reservation.resources');
SELECT platform.add_touch_trigger('reservation.resources');

-- Every bookable resource is locked at the database level: two allocations
-- in status held/confirmed can never overlap for the same resource.
CREATE TABLE reservation.allocations (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  resource_id     uuid NOT NULL REFERENCES reservation.resources (id),
  reservation_id  uuid NOT NULL,
  period          tstzrange NOT NULL CHECK (NOT isempty(period)),
  status          text NOT NULL CHECK (status IN ('held', 'confirmed', 'released', 'cancelled')),
  expires_at      timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  CONSTRAINT allocations_no_overlap EXCLUDE USING gist (
    resource_id WITH =,
    period WITH &&
  ) WHERE (status IN ('held', 'confirmed')),
  CHECK (status <> 'held' OR expires_at IS NOT NULL)
);
CREATE INDEX allocations_expiring ON reservation.allocations (expires_at) WHERE status = 'held';
SELECT platform.enable_property_rls('reservation.allocations');
SELECT platform.add_touch_trigger('reservation.allocations');

SELECT platform.grant_app('reservation');

-- +goose Down
DROP SCHEMA reservation CASCADE;
