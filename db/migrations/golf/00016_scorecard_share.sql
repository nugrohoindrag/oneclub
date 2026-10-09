-- Scorecard to print and share after the round (demo feedback 9 Oct 2026):
-- the front desk prints the card or sends the player a link to its PDF,
-- guests without a Member App account too. The link carries a random
-- token, made the first time the card is shared (expand only).

-- +goose Up
ALTER TABLE golf.scorecards ADD COLUMN share_token text UNIQUE;

-- +goose Down
ALTER TABLE golf.scorecards DROP COLUMN share_token;
