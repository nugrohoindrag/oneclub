-- HRIS improvement phase C (docs/HRIS_Product_Requirements_UI_Backend_Audit.md
-- §27): comments with an optional attachment on an approval request, by the
-- requester and the approvers (and Approvals view-all), shown in the
-- request detail next to the approval history.

-- +goose Up
CREATE TABLE platform.approval_request_comments (
  id           uuid PRIMARY KEY,
  request_id   uuid NOT NULL REFERENCES platform.approval_requests (id) ON DELETE CASCADE,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  author_id    uuid NOT NULL REFERENCES platform.users (id),
  body         text NOT NULL CHECK (length(body) BETWEEN 1 AND 4000),
  file_id      uuid REFERENCES platform.files (id),
  created_at   timestamptz NOT NULL DEFAULT now()
);
SELECT platform.enable_property_rls('platform.approval_request_comments');
CREATE INDEX approval_request_comments_request ON platform.approval_request_comments (request_id, created_at);

SELECT platform.grant_app('platform');

-- +goose Down
DROP TABLE platform.approval_request_comments;
