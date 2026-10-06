-- PRD P3 FR-CMP-04 campaign tracking (crm/engagement): delivery and read
-- statuses of campaign messages are found by the recipient's tracking token
-- (e-mail / WhatsApp payload) or the in-app link /c/<token>; indexes keep
-- the Campaign Performance views fast for 10.000-recipient campaigns.

-- +goose Up
CREATE INDEX notification_deliveries_tracking ON platform.notification_deliveries ((payload->>'trackingToken'))
  WHERE payload ? 'trackingToken';
CREATE INDEX notifications_campaign_link ON platform.notifications (link) WHERE link LIKE '/c/%';

-- +goose Down
DROP INDEX platform.notifications_campaign_link;
DROP INDEX platform.notification_deliveries_tracking;
