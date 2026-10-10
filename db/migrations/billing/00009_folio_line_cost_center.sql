-- Cost center of a charge (docs/requirement-booking-sportclub-mgcc.md FR-91,
-- FR-104): the court rent of the Sport Club carries its sport and court
-- (e.g. "sportclub:FUTSAL") to the journal, so the Sport Club profit center
-- can be drilled down per sport. Expand-only.

-- +goose Up
ALTER TABLE billing.folio_lines ADD COLUMN cost_center text;

-- +goose Down
ALTER TABLE billing.folio_lines DROP COLUMN cost_center;
