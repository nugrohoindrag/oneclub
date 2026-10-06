-- PRD P3 §16 #18 (FR-QUO-05): quotation acceptance on the public link with a
-- one-time code (OTP) and an audit trail, and e-Meterai on contracts above
-- the Sales Policies threshold (Rp5 jt). Only a salted HMAC of each code is
-- kept; codes expire, lock after the maximum attempts and are single-use.
-- Expand-only on crm/00004.

-- +goose Up
CREATE TABLE crm.sales_quotation_otps (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  quotation_id          uuid NOT NULL REFERENCES crm.sales_quotations (id) ON DELETE CASCADE,
  code_hash             text NOT NULL,                 -- hex HMAC-SHA256(pepper, salt | quotation | code); never the code
  salt                  text NOT NULL,
  channel               text NOT NULL CHECK (channel IN ('email', 'whatsapp')),
  destination_masked    text NOT NULL,
  status                text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'verified', 'consumed', 'locked', 'superseded')),
  attempts              int NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  max_attempts          int NOT NULL CHECK (max_attempts > 0),
  expires_at            timestamptz NOT NULL,
  resend_after          timestamptz NOT NULL,
  requested_ip          text,
  requested_user_agent  text,
  verified_at           timestamptz,
  consumed_at           timestamptz,
  locked_at             timestamptz,
  created_at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sales_quotation_otps_quotation ON crm.sales_quotation_otps (quotation_id, created_at DESC);
SELECT platform.enable_property_rls('crm.sales_quotation_otps');

-- Acceptance evidence and e-Meterai of the quotation.
ALTER TABLE crm.sales_quotations
  ADD COLUMN accepted_ip           text,
  ADD COLUMN accepted_user_agent   text,
  ADD COLUMN otp_verified_at       timestamptz,
  ADD COLUMN otp_channel           text CHECK (otp_channel IS NULL OR otp_channel IN ('email', 'whatsapp')),
  ADD COLUMN otp_destination       text,               -- masked destination of the verified code
  ADD COLUMN e_meterai_status      text NOT NULL DEFAULT 'not_required' CHECK (e_meterai_status IN ('not_required', 'pending', 'stamped', 'failed')),
  ADD COLUMN e_meterai_serial      text,
  ADD COLUMN e_meterai_stamped_at  timestamptz,
  ADD COLUMN e_meterai_reference   text,
  ADD COLUMN e_meterai_provider    text,               -- integration code that stamped
  ADD COLUMN e_meterai_sandbox     boolean NOT NULL DEFAULT false,
  ADD COLUMN e_meterai_error       text;
CREATE INDEX sales_quotations_e_meterai ON crm.sales_quotations (e_meterai_status) WHERE e_meterai_status IN ('pending', 'failed');

SELECT platform.grant_app('crm');

-- +goose Down
DROP INDEX crm.sales_quotations_e_meterai;
ALTER TABLE crm.sales_quotations DROP COLUMN e_meterai_error, DROP COLUMN e_meterai_sandbox, DROP COLUMN e_meterai_provider,
  DROP COLUMN e_meterai_reference, DROP COLUMN e_meterai_stamped_at, DROP COLUMN e_meterai_serial, DROP COLUMN e_meterai_status,
  DROP COLUMN otp_destination, DROP COLUMN otp_channel, DROP COLUMN otp_verified_at, DROP COLUMN accepted_user_agent, DROP COLUMN accepted_ip;
DROP TABLE crm.sales_quotation_otps;
