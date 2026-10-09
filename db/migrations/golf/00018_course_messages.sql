-- Course messenger (demo feedback 9 Oct 2026, OneClub replaces Smartscore):
-- two-way messages between a flight's caddy tablet and course control —
-- the Marshal (Operational) and the back office (Course Monitor). A message
-- to every flight on the course is stored once per flight (broadcast), so
-- each tablet confirms it (expand only).

-- +goose Up
CREATE TABLE golf.course_messages (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  course_id          uuid NOT NULL REFERENCES golf.courses (id),
  flight_id          uuid NOT NULL REFERENCES golf.flights (id),
  sender             text NOT NULL CHECK (sender IN ('tablet', 'marshal', 'office')),
  sender_name        text,
  body               text NOT NULL CHECK (length(body) BETWEEN 1 AND 500),
  broadcast          boolean NOT NULL DEFAULT false,
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  read_by_tablet_at  timestamptz,
  read_by_course_at  timestamptz
);
CREATE INDEX course_messages_flight_idx ON golf.course_messages (flight_id, created_at);
CREATE INDEX course_messages_course_idx ON golf.course_messages (course_id, created_at);
SELECT platform.enable_property_rls('golf.course_messages');

SELECT platform.grant_app('golf');

-- +goose Down
DROP TABLE golf.course_messages;
