-- Banquet & Event module schema (Technical Doc §4.1): events, banquet,
-- MICE & weddings, BEO and venues (PRD P3 EP-12..EP-15). Event is the parent
-- entity; a banquet is an event with the banquet sales flow and BEO (PRD P3
-- §6 #16).

-- +goose Up
CREATE SCHEMA IF NOT EXISTS banquet;
SELECT platform.grant_app('banquet');

-- +goose Down
DROP SCHEMA banquet CASCADE;
