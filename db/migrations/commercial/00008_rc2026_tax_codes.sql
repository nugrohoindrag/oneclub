-- Golf rate rules now apply only their own Tax & Service codes (empty = all
-- rules of the property). The demo all-in rate card RC2026 ("incl. PPN
-- 11%") was seeded with empty codes, so environments seeded before the fix
-- would split the golf round with every Tax & Service rule of the property
-- (F&B service, PB1, banquet rules …). Restrict its rules to PPN, only
-- where they are still the untouched seed and a PPN rule exists.

-- +goose Up
UPDATE commercial.pricing_rules r SET tax_codes = '{PPN}'
FROM commercial.rate_plans p
WHERE r.rate_plan_id = p.id AND p.code = 'RC2026' AND p.property_id = r.property_id AND r.tax_codes = '{}'
  AND p.description LIKE '%PPN 11%%'
  AND EXISTS (SELECT 1 FROM commercial.tax_service_rules t WHERE t.property_id = r.property_id AND t.code = 'PPN');

-- +goose Down
-- Data correction only: the previous (empty) codes are not restored.
SELECT 1;
