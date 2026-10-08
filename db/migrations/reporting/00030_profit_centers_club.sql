-- Profit centers of the club (product owner, 9 Oct 2026): Golf, Resto,
-- Sport Club, Bungalow, Wedding, Event MICE and VIP Suite; the rest is
-- shared overhead.
--
-- Golf takes the golf business line with the membership fees and the pro
-- shop; Resto the F&B outlets (POS food & beverage, bar, kitchen, halfway
-- house, tee houses); Bungalow and VIP Suite the stay lines by their revenue
-- component, meeting rooms and their equipment go to Event MICE. Banquet
-- lines are split between Wedding and Event MICE by the category of the
-- event: a line posted from a folio line follows its event, a line of the
-- daily revenue journal is allocated by the share of each category in the
-- banquet folio lines of that business day (same revenue component first,
-- the whole day otherwise; Event MICE when the day has none).
-- profit = credit − debit: revenue positive, costs negative.

-- +goose Up
DROP VIEW reporting.acc_profit_center_lines;

CREATE VIEW reporting.acc_profit_center_lines WITH (security_invoker = true) AS
WITH l AS (
  SELECT l.line_id, l.property_id, l.journal_date, l.account_code, l.account_name, l.business_line, l.revenue_component, l.cost_center,
         l.source_type, l.source_id, l.credit - l.debit AS profit,
         CASE WHEN l.subtype IN ('other_income', 'other_expense') THEN 'other' WHEN l.account_type = 'revenue' THEN 'revenue'
              WHEN l.account_type = 'cogs' THEN 'cogs' ELSE 'expense' END AS section,
         CASE
           WHEN l.cost_center IN ('bar', 'kitchen', 'halfway_house', 'restaurant', 'fnb') OR l.cost_center LIKE 'tee_house%' THEN 'resto'
           WHEN l.cost_center IN ('pro_shop', 'store', 'golf_ops', 'golf', 'course', 'caddy') THEN 'golf'
           WHEN l.cost_center IN ('sport_club', 'sportclub') THEN 'sportclub'
           WHEN l.cost_center IN ('banquet', 'events') THEN 'banquet'
           WHEN l.cost_center IN ('stay', 'housekeeping', 'bungalow') THEN 'bungalow'
           WHEN l.cost_center = 'vip_suite' THEN 'vip_suite'
         END AS by_cost_center
  FROM reporting.acc_ledger l
  WHERE l.account_type IN ('revenue', 'cogs', 'expense') AND l.journal_type <> 'closing'
), c AS (
  SELECT l.*,
    CASE
      WHEN l.section = 'other' THEN 'shared'
      WHEN l.revenue_component = 'vip_suite' THEN 'vip_suite'
      WHEN l.revenue_component IN ('meeting', 'equipment') THEN 'event_mice'
      WHEN l.revenue_component IN ('bungalow', 'late_checkout_fee') THEN 'bungalow'
      WHEN l.business_line IN ('golf', 'membership') THEN 'golf'
      WHEN l.business_line = 'pos' AND (l.revenue_component = 'pro_shop' OR l.by_cost_center = 'golf') THEN 'golf'
      WHEN l.business_line = 'pos' THEN 'resto'
      WHEN l.business_line = 'sportclub' THEN 'sportclub'
      WHEN l.business_line = 'stay' THEN 'bungalow'
      WHEN l.business_line = 'banquet' THEN 'banquet'
      WHEN l.revenue_component IN ('fnb', 'banquet_fnb') AND l.business_line = 'package' THEN 'resto'
      WHEN l.revenue_component IN ('class', 'court', 'sport_entry', 'registration_fee', 'locker') THEN 'sportclub'
      WHEN l.revenue_component IN ('green_fee', 'buggy_fee', 'golf_round', 'range', 'caddy_fee') THEN 'golf'
      WHEN l.by_cost_center IS NOT NULL THEN l.by_cost_center
      WHEN l.account_code LIKE '41%' OR l.account_code LIKE '46%' OR l.account_code IN ('4510', '4830', '5120', '5130', '6130', '6210', '6220') THEN 'golf'
      WHEN l.account_code LIKE '42%' OR l.account_code = '6120' THEN 'sportclub'
      WHEN l.account_code = '4310' THEN 'bungalow'
      WHEN l.account_code = '4320' THEN 'vip_suite'
      WHEN l.account_code = '4330' THEN 'event_mice'
      WHEN l.account_code IN ('4420', '4430', '5140') THEN 'banquet'
      WHEN l.account_code IN ('4410', '5110') THEN 'resto'
      ELSE 'shared'
    END AS bucket
  FROM l
), shares AS (
  -- Banquet folio lines per business day, revenue component and center.
  SELECT fl.property_id, fl.business_date, coalesce(fl.revenue_component, fl.charge_type) AS component,
         CASE WHEN e.category = 'wedding' THEN 'wedding' ELSE 'event_mice' END AS center, sum(fl.net_amount) AS amount
  FROM billing.folio_lines fl JOIN banquet.events e ON e.folio_id = fl.folio_id
  WHERE fl.voided_at IS NULL AND fl.business_line = 'banquet' AND fl.business_date IS NOT NULL
  GROUP BY 1, 2, 3, 4 HAVING sum(fl.net_amount) > 0
)
SELECT c.line_id, c.property_id, c.journal_date, c.section, c.account_code, c.account_name, c.business_line, c.revenue_component, c.cost_center,
       c.profit, c.bucket AS profit_center
FROM c WHERE c.bucket <> 'banquet'
UNION ALL
SELECT c.line_id, c.property_id, c.journal_date, c.section, c.account_code, c.account_name, c.business_line, c.revenue_component, c.cost_center,
       c.profit * coalesce(a.weight, 1), coalesce(d.center, a.center, 'event_mice')
FROM c
LEFT JOIN LATERAL (
  SELECT CASE WHEN e.category = 'wedding' THEN 'wedding' ELSE 'event_mice' END AS center
  FROM billing.folio_lines fl JOIN banquet.events e ON e.folio_id = fl.folio_id
  WHERE c.source_type = 'billing.folio_line' AND fl.id::text = c.source_id
) d ON true
LEFT JOIN LATERAL (
  SELECT s.center, s.amount / sum(s.amount) OVER () AS weight FROM shares s
  WHERE d.center IS NULL AND s.property_id = c.property_id AND s.business_date = c.journal_date
    AND (s.component = c.revenue_component OR NOT EXISTS (SELECT 1 FROM shares x WHERE x.property_id = c.property_id
      AND x.business_date = c.journal_date AND x.component = c.revenue_component))
) a ON true
WHERE c.bucket = 'banquet';

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.acc_profit_center_lines;

CREATE VIEW reporting.acc_profit_center_lines WITH (security_invoker = true) AS
WITH l AS (
  SELECT l.line_id, l.property_id, l.journal_date, l.account_code, l.account_name, l.account_type, l.subtype, l.business_line,
         l.revenue_component, l.cost_center, l.credit - l.debit AS profit,
         CASE WHEN l.subtype IN ('other_income', 'other_expense') THEN 'other' WHEN l.account_type = 'revenue' THEN 'revenue'
              WHEN l.account_type = 'cogs' THEN 'cogs' ELSE 'expense' END AS section,
         CASE
           WHEN l.cost_center IN ('bar', 'kitchen', 'halfway_house', 'restaurant', 'fnb') OR l.cost_center LIKE 'tee_house%' THEN 'fnb'
           WHEN l.cost_center IN ('pro_shop', 'store') THEN 'pro_shop'
           WHEN l.cost_center IN ('golf_ops', 'golf', 'course', 'caddy') THEN 'golf'
           WHEN l.cost_center IN ('sport_club', 'sportclub') THEN 'sportclub'
           WHEN l.cost_center IN ('banquet', 'events') THEN 'banquet'
           WHEN l.cost_center IN ('stay', 'housekeeping') THEN 'stay'
         END AS by_cost_center
  FROM reporting.acc_ledger l
  WHERE l.account_type IN ('revenue', 'cogs', 'expense') AND l.journal_type <> 'closing'
)
SELECT l.line_id, l.property_id, l.journal_date, l.section, l.account_code, l.account_name, l.business_line, l.revenue_component, l.cost_center, l.profit,
  CASE
    WHEN l.section = 'other' THEN 'shared'
    WHEN l.revenue_component IN ('membership_fee', 'renewal_fee', 'joining_fee', 'annual_fee') THEN 'membership'
    WHEN l.business_line = 'membership' THEN 'membership'
    WHEN l.business_line = 'pos' AND l.revenue_component = 'pro_shop' THEN 'pro_shop'
    WHEN l.business_line = 'pos' AND l.by_cost_center = 'pro_shop' THEN 'pro_shop'
    WHEN l.business_line = 'pos' THEN 'fnb'
    WHEN l.business_line IN ('golf', 'sportclub', 'stay', 'banquet', 'package') THEN l.business_line
    WHEN l.by_cost_center IS NOT NULL THEN l.by_cost_center
    WHEN l.revenue_component IN ('class', 'court', 'sport_entry', 'registration_fee') THEN 'sportclub'
    WHEN l.account_code LIKE '41%' OR l.account_code IN ('4830', '6210', '6220') THEN 'golf'
    WHEN l.account_code LIKE '42%' OR l.account_code = '6120' THEN 'sportclub'
    WHEN l.account_code IN ('4330', '4420', '4430', '5140') THEN 'banquet'
    WHEN l.account_code LIKE '43%' THEN 'stay'
    WHEN l.account_code IN ('4410', '5110') THEN 'fnb'
    WHEN l.account_code IN ('4510', '5120', '5130') THEN 'pro_shop'
    WHEN l.account_code LIKE '46%' OR l.account_code = '6130' THEN 'membership'
    WHEN l.account_code = '4700' THEN 'package'
    ELSE 'shared'
  END AS profit_center
FROM l;

SELECT platform.grant_app('reporting');
