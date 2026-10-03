-- Course header (EP-02 FR-ORG-04). Sections, playing routes, holes and tee
-- sets arrive in P1.

-- +goose Up
CREATE SCHEMA IF NOT EXISTS golf;

CREATE TABLE golf.courses (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  venue_id     uuid NOT NULL REFERENCES platform.venues (id),
  code         text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name         text NOT NULL,
  holes        int NOT NULL CHECK (holes IN (9, 18, 27, 36)),
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('golf.courses');
SELECT platform.add_touch_trigger('golf.courses');

SELECT platform.grant_app('golf');

-- +goose Down
DROP SCHEMA golf CASCADE;
