-- P1 booking flows look allocations up by reservation (booking) and by
-- resource + period (tee time seat availability).

-- +goose Up
CREATE INDEX allocations_reservation ON reservation.allocations (reservation_id);
CREATE INDEX allocations_resource_active ON reservation.allocations USING gist (resource_id, period)
  WHERE status IN ('held', 'confirmed');
CREATE INDEX resources_type ON reservation.resources (property_id, resource_type);

-- +goose Down
DROP INDEX reservation.resources_type;
DROP INDEX reservation.allocations_resource_active;
DROP INDEX reservation.allocations_reservation;
