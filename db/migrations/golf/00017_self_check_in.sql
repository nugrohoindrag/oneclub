-- Self check-in (demo feedback 9 Oct 2026): a member checks in from the
-- Member App on arrival (GPS within the club's course area) or a golfer
-- scans the booking QR at the kiosk; the front desk still picks the caddy
-- and the golf cart. The two new check-in methods (expand only).

-- +goose Up
ALTER TABLE golf.booking_players DROP CONSTRAINT booking_players_check_in_method_check;
ALTER TABLE golf.booking_players ADD CONSTRAINT booking_players_check_in_method_check
  CHECK (check_in_method IS NULL OR check_in_method IN ('member_card', 'booking_qr', 'booking_code', 'name', 'offline', 'self_app', 'kiosk'));

-- +goose Down
UPDATE golf.booking_players SET check_in_method = 'booking_qr' WHERE check_in_method IN ('self_app', 'kiosk');
ALTER TABLE golf.booking_players DROP CONSTRAINT booking_players_check_in_method_check;
ALTER TABLE golf.booking_players ADD CONSTRAINT booking_players_check_in_method_check
  CHECK (check_in_method IS NULL OR check_in_method IN ('member_card', 'booking_qr', 'booking_code', 'name', 'offline'));
