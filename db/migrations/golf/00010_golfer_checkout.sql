-- Golfer Check-out (PRD P1 FR-CHK-05): the booking is settled and closed
-- when the players leave the club; daily lockers return to Available then
-- instead of at the round finish (expand only).

-- +goose Up
ALTER TABLE golf.bookings ADD COLUMN checked_out_at timestamptz, ADD COLUMN checked_out_by uuid;

-- +goose Down
ALTER TABLE golf.bookings DROP COLUMN checked_out_at, DROP COLUMN checked_out_by;
