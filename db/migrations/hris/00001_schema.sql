-- HRIS module schema (PRD P5 §5.4.1, Technical Doc §4.1): core HR
-- (organization, employees, contracts, documents, training & certification,
-- Employee Self Service) and the later P5 areas (time, payroll) of the hris
-- module.

-- +goose Up
CREATE SCHEMA IF NOT EXISTS hris;
SELECT platform.grant_app('hris');

-- +goose Down
DROP SCHEMA hris CASCADE;
