-- Driving range areas (demo feedback 9 Oct 2026): the front desk switches the
-- Indoor and Outdoor areas on or off. A switched-off area is not offered for
-- booking (Member App, website, front desk). No row = on (expand only).

-- +goose Up
CREATE TABLE golf.range_areas (
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  area         text NOT NULL CHECK (area IN ('indoor', 'outdoor')),
  enabled      boolean NOT NULL DEFAULT true,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  PRIMARY KEY (property_id, area)
);
SELECT platform.enable_property_rls('golf.range_areas');
SELECT platform.add_touch_trigger('golf.range_areas');

SELECT platform.grant_app('golf');

-- +goose Down
DROP TABLE golf.range_areas;
