-- Facility foundation entity (FR-MD-05).

-- +goose Up
CREATE SCHEMA IF NOT EXISTS sportclub;

CREATE TABLE sportclub.facilities (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  code           text NOT NULL,
  name           text NOT NULL,
  facility_type  text,
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
SELECT platform.enable_property_rls('sportclub.facilities');
SELECT platform.add_touch_trigger('sportclub.facilities');

SELECT platform.grant_app('sportclub');

-- +goose Down
DROP SCHEMA sportclub CASCADE;
