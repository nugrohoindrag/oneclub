-- Caddy wage (demo feedback 9 Oct 2026): the monthly base salary of a caddy
-- next to the caddy fee earned per assignment (expand only).

-- +goose Up
ALTER TABLE golf.caddy_profiles ADD COLUMN base_salary numeric(19,4) CHECK (base_salary >= 0);

-- +goose Down
ALTER TABLE golf.caddy_profiles DROP COLUMN base_salary;
