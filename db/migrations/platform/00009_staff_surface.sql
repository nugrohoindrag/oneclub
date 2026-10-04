-- Staff App (Technical Doc §6.1): one build served on four surfaces, so a
-- custom domain can point at dashboard, cashier, caddy or kitchen.
-- Expand-only: the surfaces of the former staff apps stay valid for domains
-- registered before (Caddy serves backoffice and platform-admin as
-- dashboard, ops as cashier).

-- +goose Up
ALTER TABLE platform.domains DROP CONSTRAINT domains_surface_check;
ALTER TABLE platform.domains ADD CONSTRAINT domains_surface_check
  CHECK (surface IN ('web', 'member', 'dashboard', 'cashier', 'caddy', 'kitchen', 'api', 'backoffice', 'ops', 'platform-admin'));

-- +goose Down
ALTER TABLE platform.domains DROP CONSTRAINT domains_surface_check;
ALTER TABLE platform.domains ADD CONSTRAINT domains_surface_check
  CHECK (surface IN ('web', 'member', 'backoffice', 'ops', 'platform-admin', 'api'));
