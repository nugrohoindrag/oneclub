-- Attendance Form (Technical Doc §6.1): the Staff App is also served on a
-- presence domain (clock in / out with employee ID, attendance PIN and GPS,
-- without signing in), so a custom domain can point at it. Expand-only.

-- +goose Up
ALTER TABLE platform.domains DROP CONSTRAINT domains_surface_check;
ALTER TABLE platform.domains ADD CONSTRAINT domains_surface_check
  CHECK (surface IN ('web', 'member', 'dashboard', 'cashier', 'caddy', 'kitchen', 'presence', 'api', 'backoffice', 'ops', 'platform-admin'));

-- +goose Down
ALTER TABLE platform.domains DROP CONSTRAINT domains_surface_check;
ALTER TABLE platform.domains ADD CONSTRAINT domains_surface_check
  CHECK (surface IN ('web', 'member', 'dashboard', 'cashier', 'caddy', 'kitchen', 'api', 'backoffice', 'ops', 'platform-admin'));
