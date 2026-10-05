-- commercial.rate_plans.effective_from defaulted to CURRENT_DATE, the UTC
-- date of the database session: from 17:00 to 24:00 UTC that is yesterday at
-- the club (Asia/Jakarta, UTC+7). Every insert sets the date itself — the
-- rate plan hook (internal/commercial/pricing_p2.go) defaults it to the
-- club's today for the API and the import, the demo seed passes it — so the
-- default is dropped: a writer that forgets the date now fails (NOT NULL)
-- instead of silently starting the plan a day early.

-- +goose Up
ALTER TABLE commercial.rate_plans ALTER COLUMN effective_from DROP DEFAULT;

-- +goose Down
ALTER TABLE commercial.rate_plans ALTER COLUMN effective_from SET DEFAULT CURRENT_DATE;
