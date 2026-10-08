-- Member journey (Member App): the caddy preference of each player of a
-- booking — No Caddy, Request Caddy (any, the club assigns) or a Preferred
-- Caddy. A request is not an assignment: the Caddy Master's auto-assign
-- serves the preferred caddy first and skips players without a caddy.

-- +goose Up
CREATE TABLE golf.player_caddy_requests (
  booking_player_id  uuid PRIMARY KEY REFERENCES golf.booking_players (id),
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  booking_id         uuid NOT NULL REFERENCES golf.bookings (id),
  preference         text NOT NULL CHECK (preference IN ('none', 'any', 'preferred')),
  caddy_id           uuid REFERENCES golf.caddies (id),
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  updated_by         uuid,
  CHECK ((preference = 'preferred') = (caddy_id IS NOT NULL))
);
CREATE INDEX player_caddy_requests_booking ON golf.player_caddy_requests (booking_id);
SELECT platform.enable_property_rls('golf.player_caddy_requests');
SELECT platform.add_touch_trigger('golf.player_caddy_requests');

SELECT platform.grant_app('golf');

-- +goose Down
DROP TABLE golf.player_caddy_requests;
