-- CMS module schema (Technical Doc §4.1): pages, banners, media, news,
-- gallery, navigation and translations of the public website (PRD P4 EP-24).

-- +goose Up
CREATE SCHEMA IF NOT EXISTS cms;
SELECT platform.grant_app('cms');

-- +goose Down
DROP SCHEMA cms CASCADE;
