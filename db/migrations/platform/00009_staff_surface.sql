-- Staff App (Technical Doc §6.1): one application for every staff area, so a
-- custom domain can point at surface 'staff'. Expand-only: the old staff
-- surfaces stay valid for domains registered before.

-- +goose Up
ALTER TABLE platform.domains DROP CONSTRAINT domains_surface_check;
ALTER TABLE platform.domains ADD CONSTRAINT domains_surface_check
  CHECK (surface IN ('web', 'member', 'staff', 'backoffice', 'ops', 'platform-admin', 'api'));

-- +goose Down
ALTER TABLE platform.domains DROP CONSTRAINT domains_surface_check;
ALTER TABLE platform.domains ADD CONSTRAINT domains_surface_check
  CHECK (surface IN ('web', 'member', 'backoffice', 'ops', 'platform-admin', 'api'));
