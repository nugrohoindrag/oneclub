-- Player / tee colour classification (demo feedback 9 Oct 2026): each tee
-- set serves a player category — red women, blue and white general, black
-- professional — and the front desk can give a player a tee (expand only).

-- +goose Up
ALTER TABLE golf.tee_sets ADD COLUMN player_category text CHECK (player_category IN ('women', 'general', 'professional', 'senior', 'junior'));
UPDATE golf.tee_sets SET player_category = CASE lower(coalesce(color, code))
  WHEN 'red' THEN 'women' WHEN 'blue' THEN 'general' WHEN 'white' THEN 'general' WHEN 'black' THEN 'professional' END
WHERE player_category IS NULL;
ALTER TABLE golf.booking_players ADD COLUMN tee_set_id uuid REFERENCES golf.tee_sets (id);

-- +goose Down
ALTER TABLE golf.booking_players DROP COLUMN tee_set_id;
ALTER TABLE golf.tee_sets DROP COLUMN player_category;
