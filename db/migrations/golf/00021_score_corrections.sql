-- Score correction requests (demo feedback 10 Oct 2026, item 36): once the
-- round is completed the scorecard is read-only in the Member App; a player
-- who finds a wrong score asks for a correction (hole, proposed strokes,
-- reason) that the starter / marshal / handicap committee approves or
-- rejects. An approved request is applied with the score audit (expand only).

-- +goose Up
CREATE TABLE golf.score_correction_requests (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  scorecard_id   uuid NOT NULL REFERENCES golf.scorecards (id),
  seq            int NOT NULL CHECK (seq >= 1),
  strokes        int NOT NULL CHECK (strokes BETWEEN 1 AND 20),
  current_strokes int,
  reason         text NOT NULL CHECK (length(reason) BETWEEN 1 AND 500),
  status         text NOT NULL DEFAULT 'requested' CHECK (status IN ('requested', 'approved', 'rejected')),
  requested_by   uuid,
  requested_name text,
  decided_at     timestamptz,
  decided_by     uuid,
  decision_note  text,
  created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX score_correction_requests_card_idx ON golf.score_correction_requests (scorecard_id, created_at);
CREATE INDEX score_correction_requests_open_idx ON golf.score_correction_requests (property_id, created_at) WHERE status = 'requested';
SELECT platform.enable_property_rls('golf.score_correction_requests');

SELECT platform.grant_app('golf');

-- +goose Down
DROP TABLE golf.score_correction_requests;
