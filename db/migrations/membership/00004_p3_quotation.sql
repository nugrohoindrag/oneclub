-- PRD P3 FR-QUO-06/07: an accepted membership quotation becomes a draft
-- Membership Application exactly once (the quotation is recorded on it).

-- +goose Up
ALTER TABLE membership.applications ADD COLUMN quotation_id uuid, ADD COLUMN quotation_number text;
CREATE UNIQUE INDEX applications_quotation ON membership.applications (quotation_id) WHERE quotation_id IS NOT NULL;

-- +goose Down
DROP INDEX membership.applications_quotation;
ALTER TABLE membership.applications DROP COLUMN quotation_number, DROP COLUMN quotation_id;
