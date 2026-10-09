-- Member App sign-up (demo feedback 10 Oct 2026, items 37.1–37.2): a member
-- of the club registers the Member App account with the member no. and a
-- detail on file (date of birth, phone or e-mail), a guest registers as a
-- non-member with name, phone and e-mail; both confirm a one-time code and
-- choose a password. The account is linked to the customer profile
-- (expand only).

-- +goose Up
CREATE TABLE membership.portal_registrations (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  kind          text NOT NULL CHECK (kind IN ('member', 'guest')),
  customer_id   uuid NOT NULL REFERENCES crm.customers (id),
  member_id     uuid REFERENCES membership.members (id),
  email         text NOT NULL,
  phone         text,
  name          text NOT NULL,
  channel       text NOT NULL DEFAULT 'email' CHECK (channel IN ('email', 'whatsapp')),
  code_hash     text NOT NULL,
  expires_at    timestamptz NOT NULL,
  attempts      int NOT NULL DEFAULT 0,
  completed_at  timestamptz,
  user_id       uuid REFERENCES platform.users (id),
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX portal_registrations_customer_idx ON membership.portal_registrations (customer_id, created_at);
SELECT platform.enable_property_rls('membership.portal_registrations');

SELECT platform.grant_app('membership');

-- +goose Down
DROP TABLE membership.portal_registrations;
