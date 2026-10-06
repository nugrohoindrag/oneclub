-- PRD P5 FR-HR-06: the employee who holds an asset (custodian), so the
-- offboarding checklist of a leaver lists the assets to return. Optional;
-- a plain reference to hris.employees (hris migrates after inventory, so no
-- foreign key; the API checks the employee belongs to the same property).

-- +goose Up
ALTER TABLE inventory.assets ADD COLUMN custodian_employee_id uuid;
CREATE INDEX assets_custodian_idx ON inventory.assets (custodian_employee_id) WHERE custodian_employee_id IS NOT NULL;

SELECT platform.grant_app('inventory');

-- +goose Down
DROP INDEX inventory.assets_custodian_idx;
ALTER TABLE inventory.assets DROP COLUMN custodian_employee_id;
