-- Marshal (PRD P2 FR-PLX-04, Product Overview §11 "Marshal & status
-- lapangan"): what the Starter / Marshal did about a flight on the course —
-- a reminder, a warning, a final warning, asking the flight to skip a hole or
-- to let the flight behind play through, or a plain note. The caddy tablet
-- of the flight shows the message until the caddy acknowledges it.

-- +goose Up
CREATE TABLE golf.pace_interventions (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  flight_id        uuid NOT NULL REFERENCES golf.flights (id),
  kind             text NOT NULL CHECK (kind IN ('reminder', 'warning', 'final_warning', 'skip_hole', 'play_through', 'note')),
  message          text NOT NULL,
  current_seq      int,
  hole             text,
  behind_minutes   int,
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid,
  created_by_name  text,
  acknowledged_at  timestamptz,
  acknowledged_by  uuid
);
CREATE INDEX pace_interventions_flight ON golf.pace_interventions (flight_id, created_at DESC);
SELECT platform.enable_property_rls('golf.pace_interventions');

SELECT platform.grant_app('golf');

-- +goose Down
DROP TABLE golf.pace_interventions;
