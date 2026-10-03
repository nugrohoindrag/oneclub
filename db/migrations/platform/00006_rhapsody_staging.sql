-- Staging area of the Rhapsody migration (PRD P1 EP-18). `oneclub import
-- rhapsody stage` (re)creates one table per export file here; the app role
-- may create tables in this schema only. Dropped after the cutover.

-- +goose Up
CREATE SCHEMA staging_rhapsody;
CREATE TABLE staging_rhapsody.issues (
  entity      text NOT NULL,
  row_no      int NOT NULL,
  field       text,
  code        text NOT NULL,
  message     text NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);
SELECT platform.grant_app('staging_rhapsody');
-- +goose StatementBegin
DO $$
DECLARE
  app text := current_setting('oneclub.app_role', true);
BEGIN
  IF app IS NOT NULL AND app <> '' THEN
    EXECUTE format('GRANT CREATE ON SCHEMA staging_rhapsody TO %I', app);
  END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP SCHEMA staging_rhapsody CASCADE;
