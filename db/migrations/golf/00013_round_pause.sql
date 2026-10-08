-- Rain during the round (demo feedback 9 Oct 2026): the caddy reports rain
-- on the tablet and the play time of the flight stops until play resumes
-- (expand only).

-- +goose Up
ALTER TABLE golf.flights ADD COLUMN paused_at timestamptz, ADD COLUMN pause_reason text,
  ADD COLUMN paused_seconds int NOT NULL DEFAULT 0 CHECK (paused_seconds >= 0);

-- +goose Down
ALTER TABLE golf.flights DROP COLUMN paused_at, DROP COLUMN pause_reason, DROP COLUMN paused_seconds;
