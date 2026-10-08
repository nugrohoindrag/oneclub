-- Management Dashboard › Profit Centers and Incidents: read models of the
-- general ledger per profit center and of the operational incidents of the
-- modules (caddy, golf cart, banquet event).

-- +goose Up

-- P&L lines of the general ledger with the profit center they belong to.
-- Revenue follows the business line and revenue component of the line,
-- costs the cost center (outlet / store of the stock movement); lines
-- without a dimension fall back on the account of the standard chart
-- (41xx golf, 42xx sport club, 43xx stay …). What maps to no profit center
-- (salaries, utilities, depreciation, other income and expenses) is
-- 'shared': the overhead below the contribution of the centers.
-- profit = credit − debit: revenue positive, costs negative.
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

-- Operational incidents of every module in one list: caddy and golf cart
-- incidents (golf, open until closed with the action taken) and banquet
-- event incidents (notes of the event day, status 'logged').
CREATE VIEW reporting.incidents WITH (security_invoker = true) AS
SELECT i.id, i.property_id, i.subject_type AS source, i.number, i.category, i.severity, i.status, i.description, i.action_taken,
       coalesce(c.code || ' · ' || c.name, g.code || ' · ' || g.name) AS subject, i.damage_amount, i.damage_status, i.occurred_at, i.created_at
FROM golf.incidents i
LEFT JOIN golf.caddies c ON c.id = i.caddy_id
LEFT JOIN golf.golf_carts g ON g.id = i.golf_cart_id
UNION ALL
SELECT b.id, b.property_id, 'banquet', e.number, 'event', b.severity, 'logged', b.note, NULL, e.number || ' · ' || e.title, NULL, NULL,
       b.created_at, b.created_at
FROM banquet.event_incidents b
JOIN banquet.events e ON e.id = b.event_id;

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.incidents, reporting.acc_profit_center_lines;
