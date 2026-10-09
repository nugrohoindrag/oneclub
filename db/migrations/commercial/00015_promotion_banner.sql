-- Promo banner of the Member App home (demo feedback 10 Oct 2026, item
-- 37.5): a promotion may carry a banner picture shown in the carousel and on
-- its detail page (expand only).

-- +goose Up
ALTER TABLE commercial.promotions ADD COLUMN image_url text;

-- +goose Down
ALTER TABLE commercial.promotions DROP COLUMN image_url;
