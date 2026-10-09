-- Who an order and each of its items is for (demo feedback 10 Oct 2026,
-- items 34–35): an on-course order shows the player and the booking instead
-- of "Walk-in guest", and every item carries the player / member it is for
-- (kitchen, tee house, receipt, bill per player) (expand only).

-- +goose Up
ALTER TABLE commercial.orders
  ADD COLUMN guest_name text,
  ADD COLUMN reference  text;

ALTER TABLE commercial.order_lines
  ADD COLUMN guest_name  text,
  ADD COLUMN customer_id uuid REFERENCES crm.customers (id),
  ADD COLUMN guest_ref   uuid;

-- +goose Down
ALTER TABLE commercial.order_lines DROP COLUMN guest_ref, DROP COLUMN customer_id, DROP COLUMN guest_name;
ALTER TABLE commercial.orders DROP COLUMN reference, DROP COLUMN guest_name;
