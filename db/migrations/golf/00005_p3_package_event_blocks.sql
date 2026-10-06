-- PRD P3 fulfilment gaps (FR-PKG-04/06/08, FR-EVT-03): golf bookings made
-- from the tee time components of commercial packages (one per component
-- and tee time; plain references, commercial owns the package booking) and
-- course blocks requested by banquet events (source = the banquet golf
-- block, plain reference). Additive only.

-- +goose Up
ALTER TABLE golf.bookings ADD COLUMN package_booking_id uuid, ADD COLUMN package_component_id uuid;
CREATE UNIQUE INDEX bookings_package_component ON golf.bookings (package_component_id, tee_time_id) WHERE package_component_id IS NOT NULL;
CREATE INDEX bookings_package ON golf.bookings (package_booking_id) WHERE package_booking_id IS NOT NULL;

ALTER TABLE golf.course_blocks ADD COLUMN source_type text, ADD COLUMN source_id uuid;
CREATE UNIQUE INDEX course_blocks_source ON golf.course_blocks (source_type, source_id) WHERE source_id IS NOT NULL AND status = 'active';

SELECT platform.grant_app('golf');

-- +goose Down
DROP INDEX golf.course_blocks_source;
ALTER TABLE golf.course_blocks DROP COLUMN source_type, DROP COLUMN source_id;
DROP INDEX golf.bookings_package;
DROP INDEX golf.bookings_package_component;
ALTER TABLE golf.bookings DROP COLUMN package_booking_id, DROP COLUMN package_component_id;
