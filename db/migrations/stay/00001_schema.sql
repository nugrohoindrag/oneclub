-- Stay & Venue module schema (Technical Doc §4.1, PRD P2 EP-16–18).

-- +goose Up
CREATE SCHEMA IF NOT EXISTS stay;
SELECT platform.grant_app('stay');

-- +goose Down
DROP SCHEMA stay CASCADE;
