-- P2 shared platform additions (additive, Technical Doc §12.3 #6):
--  * document numbering (folio, reservation, order, voucher … numbers)
--  * closures in P1's Day Calendar (Reservation Engine availability)
--  * hardware command queue delivered to bridge agents (PRD P2 FR-INT-P2-01)

-- +goose Up
CREATE TABLE platform.document_sequences (
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  prefix       text NOT NULL CHECK (prefix ~ '^[A-Z]{2,6}$'),
  period       text NOT NULL,
  last_value   bigint NOT NULL,
  PRIMARY KEY (property_id, prefix, period)
);
SELECT platform.enable_property_rls('platform.document_sequences');

ALTER TABLE platform.calendar_days DROP CONSTRAINT calendar_days_kind_check;
ALTER TABLE platform.calendar_days ADD CONSTRAINT calendar_days_kind_check CHECK (kind IN ('public_holiday', 'special', 'closed'));

CREATE TABLE platform.bridge_commands (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  agent_id      uuid NOT NULL REFERENCES platform.bridge_agents (id),
  device        text NOT NULL,
  command       text NOT NULL,
  payload       jsonb NOT NULL DEFAULT '{}'::jsonb,
  status        text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'sent', 'succeeded', 'failed', 'expired')),
  result        jsonb NOT NULL DEFAULT '{}'::jsonb,
  source_type   text,
  source_id     uuid,
  deadline      timestamptz NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid,
  sent_at       timestamptz,
  completed_at  timestamptz
);
CREATE INDEX bridge_commands_queue ON platform.bridge_commands (agent_id, created_at) WHERE status IN ('queued', 'sent');
SELECT platform.enable_property_rls('platform.bridge_commands');

SELECT platform.grant_app('platform');

-- +goose Down
DROP TABLE platform.bridge_commands;
ALTER TABLE platform.calendar_days DROP CONSTRAINT calendar_days_kind_check;
ALTER TABLE platform.calendar_days ADD CONSTRAINT calendar_days_kind_check CHECK (kind IN ('public_holiday', 'special'));
DROP TABLE platform.document_sequences;
