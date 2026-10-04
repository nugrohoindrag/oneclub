-- Accounting module schema (Technical Doc §4.1, §7.4): the general ledger,
-- posting, receivables and payables, cash & bank, revenue & tax and
-- financial reports of PRD P4 EP-16..EP-23.

-- +goose Up
CREATE SCHEMA IF NOT EXISTS accounting;
SELECT platform.grant_app('accounting');

-- +goose Down
DROP SCHEMA accounting CASCADE;
