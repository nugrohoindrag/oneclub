-- PRD P4 FR-ACC-09 / §16 #18: time-bound role assignments (the external
-- Auditor has access for the audit period only). An assignment past its
-- valid_until grants nothing; authentication ignores it on every request.

-- +goose Up
ALTER TABLE platform.role_assignments ADD COLUMN valid_until timestamptz;
CREATE INDEX role_assignments_valid_until ON platform.role_assignments (valid_until) WHERE valid_until IS NOT NULL;

-- +goose Down
DROP INDEX platform.role_assignments_valid_until;
ALTER TABLE platform.role_assignments DROP COLUMN valid_until;
