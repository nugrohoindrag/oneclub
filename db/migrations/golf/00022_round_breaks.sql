-- Breaks and rain stops during the round (demo feedback 10 Oct 2026, items
-- 28, 29, 31):
--  * every pause of a flight is kept with its kind and duration — rain and
--    lightning stop the play time; a break (halfway house, the turn, a tee
--    house stop) keeps counting with the break allowance of the Golf Policy;
--  * a rain stop / lightning warning set by the Marshal on the course pauses
--    every flight in play at once and is kept per course for the rain check
--    and the management report (expand only).

-- +goose Up
ALTER TABLE golf.flights ADD COLUMN break_seconds int NOT NULL DEFAULT 0 CHECK (break_seconds >= 0);

CREATE TABLE golf.course_stops (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  course_id     uuid NOT NULL REFERENCES golf.courses (id),
  kind          text NOT NULL CHECK (kind IN ('rain_stop', 'lightning_warning')),
  reason        text,
  started_at    timestamptz NOT NULL DEFAULT now(),
  started_by    uuid,
  ended_at      timestamptz,
  ended_by      uuid,
  flights       int NOT NULL DEFAULT 0
);
CREATE INDEX course_stops_course_idx ON golf.course_stops (course_id, started_at);
CREATE UNIQUE INDEX course_stops_open_idx ON golf.course_stops (course_id) WHERE ended_at IS NULL;
SELECT platform.enable_property_rls('golf.course_stops');

CREATE TABLE golf.flight_pauses (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  flight_id     uuid NOT NULL REFERENCES golf.flights (id),
  kind          text NOT NULL CHECK (kind IN ('rain', 'lightning', 'other', 'halfway', 'turn', 'tee_house', 'break_other')),
  source        text NOT NULL DEFAULT 'tablet' CHECK (source IN ('tablet', 'starter', 'marshal', 'course_stop')),
  course_stop_id uuid REFERENCES golf.course_stops (id),
  hole_seq      int,
  started_at    timestamptz NOT NULL,
  ended_at      timestamptz,
  seconds       int,
  created_by    uuid
);
CREATE INDEX flight_pauses_flight_idx ON golf.flight_pauses (flight_id, started_at);
SELECT platform.enable_property_rls('golf.flight_pauses');

SELECT platform.grant_app('golf');

-- +goose Down
DROP TABLE golf.flight_pauses;
DROP TABLE golf.course_stops;
ALTER TABLE golf.flights DROP COLUMN break_seconds;
