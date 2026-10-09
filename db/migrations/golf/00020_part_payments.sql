-- Pay part now (demo feedback 10 Oct 2026, item 17): the part paid online
-- for a golf booking is now a payment of the bill. Deposits still held for
-- live bookings are applied so the front desk bill, the Member App and the
-- check-out count them as Paid (data only).

-- +goose Up
UPDATE billing.deposits d SET applied_amount = d.amount, status = 'applied', applied_at = now()
 WHERE d.status = 'held'
   AND d.folio_id IN (SELECT b.folio_id FROM golf.bookings b
                       WHERE b.folio_id IS NOT NULL AND b.status NOT IN ('cancelled', 'no_show', 'draft'));

-- +goose Down
SELECT 1;
