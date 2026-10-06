-- PRD P3 FR-WEB-P3-05: the payment schedule an accepted quotation produced,
-- whichever module converted it (generic quotation schedule, banquet event,
-- corporate tournament), so the public quotation page can offer the "Pay
-- down payment" step without billing importing those modules (Technical Doc
-- §4.2 #3). security_invoker keeps the Row Level Security of the sources.

-- +goose Up
CREATE VIEW reporting.quotation_payment_schedules WITH (security_invoker = true) AS
SELECT s.id AS schedule_id, s.property_id, q.id AS quotation_id, q.number AS quotation_number, s.source_type, s.status, s.created_at
FROM billing.payment_schedules s
JOIN LATERAL (
  SELECT s.source_id AS quotation_id WHERE s.source_type = 'quotation'
  UNION ALL
  SELECT e.quotation_id FROM banquet.events e WHERE s.source_type = 'banquet_event' AND e.id = s.source_id
  UNION ALL
  SELECT t.quotation_id FROM golf.tournaments t WHERE s.source_type = 'tournament' AND t.id = s.source_id
) x ON x.quotation_id IS NOT NULL
JOIN crm.sales_quotations q ON q.id = x.quotation_id;

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.quotation_payment_schedules;
