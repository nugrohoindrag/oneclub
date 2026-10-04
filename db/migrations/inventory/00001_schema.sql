-- Inventory & BOM module schema (Technical Doc §4.1). P2 builds the BOM /
-- Recipe foundation (PRD P2 EP-22); stock is P4.

-- +goose Up
CREATE SCHEMA IF NOT EXISTS inventory;
SELECT platform.grant_app('inventory');

-- +goose Down
DROP SCHEMA inventory CASCADE;
