-- Court bookings of the Sport Club (docs/requirement-booking-sportclub-mgcc.md
-- §13.3): one booking holds several lines (court × hours) and each line has
-- its own status — a line can be a No-show while another one is played.
-- The public token of the booking confirmation / Cek Booking page is looked
-- up by an expression index. Expand-only.

-- +goose Up
ALTER TABLE reservation.reservation_lines DROP CONSTRAINT reservation_lines_status_check;
ALTER TABLE reservation.reservation_lines ADD CONSTRAINT reservation_lines_status_check
  CHECK (status IN ('held', 'confirmed', 'checked_in', 'completed', 'released', 'cancelled', 'no_show'));

CREATE INDEX reservations_public_token ON reservation.reservations ((attributes ->> 'publicToken')) WHERE attributes ? 'publicToken';
CREATE INDEX reservation_lines_period ON reservation.reservation_lines USING gist (period);

-- +goose Down
DROP INDEX reservation.reservation_lines_period;
DROP INDEX reservation.reservations_public_token;
UPDATE reservation.reservation_lines SET status = 'released' WHERE status = 'no_show';
ALTER TABLE reservation.reservation_lines DROP CONSTRAINT reservation_lines_status_check;
ALTER TABLE reservation.reservation_lines ADD CONSTRAINT reservation_lines_status_check
  CHECK (status IN ('held', 'confirmed', 'checked_in', 'completed', 'released', 'cancelled'));
