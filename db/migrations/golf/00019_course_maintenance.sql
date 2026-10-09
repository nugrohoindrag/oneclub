-- Course maintenance (demo feedback 9 Oct 2026, replaces Smartscore Golf
-- O&M): the daily plan per hole or area — mowing greens and fairways,
-- bunkers, irrigation, fertiliser, pin positions … — given to the
-- groundstaff, worked planned → in progress → done with a photo and notes,
-- shown on the Course Monitor and the tee sheet, and kept as the history
-- of every hole (expand only).

-- +goose Up
CREATE TABLE golf.maintenance_tasks (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  course_id      uuid NOT NULL REFERENCES golf.courses (id),
  hole_id        uuid REFERENCES golf.holes (id),
  area           text NOT NULL CHECK (area IN ('green', 'fairway', 'tee', 'bunker', 'rough', 'irrigation', 'driving_range', 'whole_course', 'other')),
  task_type      text NOT NULL CHECK (task_type IN ('mowing_green', 'mowing_fairway', 'mowing_rough', 'bunker', 'irrigation', 'fertilizer',
                   'pin_position', 'aeration', 'top_dressing', 'repair', 'other')),
  work_date      date NOT NULL,
  planned_start  time,
  assignee_name  text,
  closes_hole    boolean NOT NULL DEFAULT false,
  notes          text,
  status         text NOT NULL DEFAULT 'planned' CHECK (status IN ('planned', 'in_progress', 'done', 'cancelled')),
  started_at     timestamptz,
  completed_at   timestamptz,
  done_notes     text,
  photo_url      text,
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid
);
CREATE INDEX maintenance_tasks_day_idx ON golf.maintenance_tasks (property_id, work_date);
CREATE INDEX maintenance_tasks_hole_idx ON golf.maintenance_tasks (hole_id, work_date DESC);
SELECT platform.enable_property_rls('golf.maintenance_tasks');
SELECT platform.add_touch_trigger('golf.maintenance_tasks');

SELECT platform.grant_app('golf');

-- +goose Down
DROP TABLE golf.maintenance_tasks;
