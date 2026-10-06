-- Collections (AR) and Vendor Follow-up (AP): the follow-up log of a
-- customer or supplier with open items — reminders sent, calls, promises to
-- pay, disputes — with the person responsible and the next follow-up date.
-- The open amounts stay derived from the AR / AP open items; this table only
-- records the collection work.

-- +goose Up
CREATE TABLE accounting.follow_ups (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  party_type         text NOT NULL CHECK (party_type IN ('customer', 'supplier')),
  party_id           uuid NOT NULL,
  party_name         text NOT NULL,
  invoice_id         uuid,
  invoice_number     text,
  action             text NOT NULL CHECK (action IN ('reminder', 'call', 'email', 'meeting', 'promise_to_pay', 'dispute', 'note')),
  notes              text,
  promised_date      date,
  promised_amount    numeric(19,4),
  next_follow_up     date,
  responsible_id     uuid,
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  updated_by         uuid
);
CREATE INDEX follow_ups_party_idx ON accounting.follow_ups (property_id, party_type, party_id, created_at DESC);
SELECT platform.enable_property_rls('accounting.follow_ups');
SELECT platform.add_touch_trigger('accounting.follow_ups');

SELECT platform.grant_app('accounting');
SELECT accounting.protect_append_only();

-- +goose Down
DROP TABLE IF EXISTS accounting.follow_ups;
