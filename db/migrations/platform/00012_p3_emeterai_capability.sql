-- PRD P3 §16 #18: e-Meterai (stamp duty on quotations / contracts above
-- Rp5 jt) is an integration with the capability "e_meterai".
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
  IF def IS NULL OR position('''e_meterai''' IN def) > 0 THEN
    RETURN;
  END IF;
  ALTER TABLE platform.integrations DROP CONSTRAINT integrations_capability_check;
  EXECUTE 'ALTER TABLE platform.integrations ADD CONSTRAINT integrations_capability_check '
    || regexp_replace(def, '\]\)', ', ''e_meterai''::text])');
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
DECLARE
  def text;
BEGIN
  DELETE FROM platform.integrations WHERE capability = 'e_meterai';
  SELECT pg_get_constraintdef(c.oid) INTO def FROM pg_constraint c
  WHERE c.conname = 'integrations_capability_check' AND c.conrelid = 'platform.integrations'::regclass;
  IF def IS NULL OR position('''e_meterai''' IN def) = 0 THEN
    RETURN;
  END IF;
  ALTER TABLE platform.integrations DROP CONSTRAINT integrations_capability_check;
  EXECUTE 'ALTER TABLE platform.integrations ADD CONSTRAINT integrations_capability_check '
    || replace(def, ', ''e_meterai''::text', '');
END $$;
-- +goose StatementEnd
