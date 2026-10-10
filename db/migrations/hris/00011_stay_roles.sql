-- Accommodation roles on the roster (docs/requirement-booking-hotel-mgcc.md
-- FR-H83, FR-H84): housekeeping (room attendants, supervisor) and the front
-- office get their workforce role, so housekeeping tasks go to the room
-- attendants on duty today; the stay front desk is a work area of an
-- employee next to golf and sportclub. Expand-only.

-- +goose Up
ALTER TABLE hris.positions DROP CONSTRAINT positions_workforce_role_check;
ALTER TABLE hris.positions ADD CONSTRAINT positions_workforce_role_check CHECK (workforce_role IS NULL OR workforce_role IN ('lifeguard', 'caddy',
  'instructor', 'food_handler', 'engineering', 'course_maintenance', 'security', 'sport_staff', 'starter', 'housekeeping', 'front_office', 'other'));
ALTER TABLE hris.shift_templates DROP CONSTRAINT shift_templates_workforce_role_check;
ALTER TABLE hris.shift_templates ADD CONSTRAINT shift_templates_workforce_role_check CHECK (workforce_role IS NULL OR workforce_role IN ('lifeguard',
  'caddy', 'instructor', 'food_handler', 'engineering', 'course_maintenance', 'security', 'sport_staff', 'starter', 'housekeeping', 'front_office', 'other'));
ALTER TABLE hris.employees DROP CONSTRAINT employees_work_areas_check;
ALTER TABLE hris.employees ADD CONSTRAINT employees_work_areas_check CHECK (work_areas <@ ARRAY['golf', 'sportclub', 'stay']::text[]);

-- +goose Down
ALTER TABLE hris.employees DROP CONSTRAINT employees_work_areas_check;
ALTER TABLE hris.employees ADD CONSTRAINT employees_work_areas_check CHECK (work_areas <@ ARRAY['golf', 'sportclub']::text[]);
ALTER TABLE hris.shift_templates DROP CONSTRAINT shift_templates_workforce_role_check;
ALTER TABLE hris.shift_templates ADD CONSTRAINT shift_templates_workforce_role_check CHECK (workforce_role IS NULL OR workforce_role IN ('lifeguard',
  'caddy', 'instructor', 'food_handler', 'engineering', 'course_maintenance', 'security', 'sport_staff', 'starter', 'other'));
ALTER TABLE hris.positions DROP CONSTRAINT positions_workforce_role_check;
ALTER TABLE hris.positions ADD CONSTRAINT positions_workforce_role_check CHECK (workforce_role IS NULL OR workforce_role IN ('lifeguard', 'caddy',
  'instructor', 'food_handler', 'engineering', 'course_maintenance', 'security', 'sport_staff', 'starter', 'other'));
