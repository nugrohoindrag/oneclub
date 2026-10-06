-- PRD P3 FR-EVT-03: golf course blocks used by an event. Requested from
-- golf (banquet.golf_block_requested) once the event is Definite; golf
-- closes the tee times with a course block whose source is this row (plain
-- references: golf owns courses and blocks).

-- +goose Up
CREATE TABLE banquet.event_golf_blocks (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  event_id          uuid NOT NULL REFERENCES banquet.events (id),
  course_id         uuid NOT NULL,             -- golf.courses (plain reference)
  course_name       text NOT NULL,             -- snapshot
  playing_route_id  uuid,                      -- golf.playing_routes (plain reference)
  start_at          timestamptz NOT NULL,
  end_at            timestamptz NOT NULL,
  notes             text,
  status            text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'requested', 'released')),
  requested_at      timestamptz,
  released_at       timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  CHECK (end_at > start_at)
);
CREATE INDEX event_golf_blocks_event ON banquet.event_golf_blocks (event_id);
SELECT platform.enable_property_rls('banquet.event_golf_blocks');
SELECT platform.add_touch_trigger('banquet.event_golf_blocks');

SELECT platform.grant_app('banquet');

-- +goose Down
DROP TABLE banquet.event_golf_blocks;
