-- Work areas of an employee (docs/requirement-booking-sportclub-mgcc.md
-- FR-99): the front desk areas (golf, sportclub) the employee may open on
-- the domain cashier. Empty = every area the role allows. Expand-only.

-- +goose Up
ALTER TABLE hris.employees ADD COLUMN work_areas text[] NOT NULL DEFAULT '{}'
  CHECK (work_areas <@ ARRAY['golf', 'sportclub']::text[]);

-- +goose Down
ALTER TABLE hris.employees DROP COLUMN work_areas;
