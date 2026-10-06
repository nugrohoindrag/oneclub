-- PRD P4 FR-CMS-10 / EP-25: the public website is an integration with the
-- capability "website" (on-demand cache revalidation after a publication).
-- The capability list is extended in place, whatever capabilities other
-- migrations added before, so parallel additions do not overwrite each other.

-- +goose Up
-- +goose StatementBegin
DO $$
DECLARE
  def text;
BEGIN
  SELECT pg_get_constraintdef(c.oid) INTO def FROM pg_constraint c
  WHERE c.conname = 'integrations_capability_check' AND c.conrelid = 'platform.integrations'::regclass;
  IF def IS NULL OR position('''website''' IN def) > 0 THEN
    RETURN;
  END IF;
  ALTER TABLE platform.integrations DROP CONSTRAINT integrations_capability_check;
  EXECUTE 'ALTER TABLE platform.integrations ADD CONSTRAINT integrations_capability_check '
    || regexp_replace(def, '\]\)', ', ''website''::text])');
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
DECLARE
  def text;
BEGIN
  DELETE FROM platform.integrations WHERE capability = 'website';
  SELECT pg_get_constraintdef(c.oid) INTO def FROM pg_constraint c
  WHERE c.conname = 'integrations_capability_check' AND c.conrelid = 'platform.integrations'::regclass;
  IF def IS NULL OR position('''website''' IN def) = 0 THEN
    RETURN;
  END IF;
  ALTER TABLE platform.integrations DROP CONSTRAINT integrations_capability_check;
  EXECUTE 'ALTER TABLE platform.integrations ADD CONSTRAINT integrations_capability_check '
    || replace(def, ', ''website''::text', '');
END $$;
-- +goose StatementEnd
