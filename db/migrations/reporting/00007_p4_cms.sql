-- PRD P4 EP-24 Landing Page & CMS read models: Website Content Report
-- (status, live version, unpublished changes, schedule), Website Publishing
-- Report (the publishing log, FR-CMS-10) and Website Translation Report
-- (status per language, FR-CMS-05). Views use security_invoker so Row
-- Level Security of the source tables applies.

-- +goose Up
CREATE VIEW reporting.cms_contents WITH (security_invoker = true) AS
SELECT c.id AS content_id, c.property_id, c.kind, c.key, c.template, c.placement, c.title, c.status, c.live, c.latest_version, c.published_version,
       (c.latest_version > coalesce(c.published_version, 0)) AS has_unpublished_changes, c.publish_at, c.unpublish_at, c.published_at,
       c.first_published_at, c.unpublished_at, c.created_at, c.updated_at, u.full_name AS updated_by_name, c.translation_status
FROM cms.contents c
LEFT JOIN platform.users u ON u.id = c.updated_by
WHERE c.archived_at IS NULL;

CREATE VIEW reporting.cms_publish_events WITH (security_invoker = true) AS
SELECT e.id AS event_id, e.property_id, e.content_id, e.kind, c.title, e.action, e.version_no, e.actor_name, e.note, e.at
FROM cms.publish_events e
JOIN cms.contents c ON c.id = e.content_id;

CREATE VIEW reporting.cms_translations WITH (security_invoker = true) AS
SELECT c.id AS content_id, c.property_id, c.kind, c.title, c.status, c.live, t.key AS language, t.value ->> 'status' AS translation_status,
       t.value ->> 'title' AS translated_title, t.value ->> 'path' AS path, c.updated_at
FROM cms.contents c, jsonb_each(c.translation_status) t
WHERE c.archived_at IS NULL;

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.cms_translations, reporting.cms_publish_events, reporting.cms_contents;
