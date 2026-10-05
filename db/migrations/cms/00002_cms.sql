-- PRD P4 EP-24 Landing Page & CMS (Technical Doc §4.1 schema cms): pages,
-- news articles, banners and gallery albums share one versioned content
-- model (every save is an immutable version; the workflow draft → in review
-- → scheduled → published → unpublished acts on versions), plus the media
-- library, navigation menus, redirects, contact information, course guide
-- texts and news categories. Structured data (rates, packages, promotions,
-- events, hall of fame …) is never copied here: data blocks only reference
-- it by source + filter (contract K5), the website reads it from the public
-- API of the owning module.

-- +goose Up
CREATE TABLE cms.categories (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  code         text NOT NULL,
  name         text NOT NULL,
  labels       jsonb NOT NULL DEFAULT '{}'::jsonb,     -- {lang: label}
  sort_order   int NOT NULL DEFAULT 0,
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('cms.categories');
SELECT platform.add_touch_trigger('cms.categories');

-- Images library (FR-CMS-03): the bytes live in platform storage (public
-- files); resized web variants are separate files listed in variants.
CREATE TABLE cms.media (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  file_id       uuid NOT NULL REFERENCES platform.files (id),
  filename      text NOT NULL,
  content_type  text NOT NULL,
  size_bytes    bigint NOT NULL,
  width         int,
  height        int,
  checksum      text NOT NULL,
  alt           jsonb NOT NULL DEFAULT '{}'::jsonb,     -- {lang: alt text}
  caption       jsonb NOT NULL DEFAULT '{}'::jsonb,
  tags          text[] NOT NULL DEFAULT '{}',
  folder        text,
  variants      jsonb NOT NULL DEFAULT '[]'::jsonb,     -- [{name, width, height, fileId, url, contentType}]
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  updated_by    uuid,
  archived_at   timestamptz
);
CREATE INDEX media_property ON cms.media (property_id, created_at DESC);
CREATE INDEX media_checksum ON cms.media (property_id, checksum);
SELECT platform.enable_property_rls('cms.media');
SELECT platform.add_touch_trigger('cms.media');

-- Pages, news articles, banners and gallery albums.
CREATE TABLE cms.contents (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  kind                text NOT NULL CHECK (kind IN ('page', 'article', 'banner', 'gallery')),
  key                 text,                               -- page key / banner code
  template            text,
  parent_id           uuid REFERENCES cms.contents (id),
  sort_order          int NOT NULL DEFAULT 0,
  show_in_sitemap     boolean NOT NULL DEFAULT true,
  category_id         uuid REFERENCES cms.categories (id),
  tags                text[] NOT NULL DEFAULT '{}',
  author_name         text,
  featured            boolean NOT NULL DEFAULT false,
  display_date        date,                               -- news "publish date" shown on the website
  placement           text,
  page_ids            uuid[] NOT NULL DEFAULT '{}',       -- banner target pages
  link                jsonb,
  title               text NOT NULL DEFAULT '',           -- default-language title of the latest version
  translation_status  jsonb NOT NULL DEFAULT '{}'::jsonb, -- {lang: {status, title, slug, path}}
  status              text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'in_review', 'scheduled', 'published', 'unpublished')),
  status_before_review text CHECK (status_before_review IN ('draft', 'scheduled', 'published', 'unpublished')),
  live                boolean NOT NULL DEFAULT false,     -- the published version is on the website
  latest_version      int NOT NULL DEFAULT 0,
  review_version      int,
  approved_version    int,
  published_version   int,
  approval_request_id uuid,
  publish_at          timestamptz,
  unpublish_at        timestamptz,
  published_at        timestamptz,
  first_published_at  timestamptz,
  unpublished_at      timestamptz,
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid,
  updated_at          timestamptz NOT NULL DEFAULT now(),
  updated_by          uuid,
  archived_at         timestamptz,
  CHECK (unpublish_at IS NULL OR publish_at IS NULL OR unpublish_at > publish_at)
);
CREATE UNIQUE INDEX contents_key ON cms.contents (property_id, kind, key) WHERE key IS NOT NULL AND archived_at IS NULL;
CREATE INDEX contents_list ON cms.contents (property_id, kind, status) WHERE archived_at IS NULL;
CREATE INDEX contents_schedule ON cms.contents (publish_at) WHERE status = 'scheduled';
CREATE INDEX contents_expiry ON cms.contents (unpublish_at) WHERE live;
SELECT platform.enable_property_rls('cms.contents');
SELECT platform.add_touch_trigger('cms.contents');

-- Immutable versions (FR-CMS-06 versions & rollback). Only the review and
-- publication markers change after insert.
CREATE TABLE cms.content_versions (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  content_id          uuid NOT NULL REFERENCES cms.contents (id) ON DELETE CASCADE,
  version_no          int NOT NULL CHECK (version_no > 0),
  document            jsonb NOT NULL,
  lang_hash           jsonb NOT NULL DEFAULT '{}'::jsonb,
  lang_rev            jsonb NOT NULL DEFAULT '{}'::jsonb, -- {lang: version where the language last changed}
  note                text,
  source              text NOT NULL DEFAULT 'edit' CHECK (source IN ('create', 'edit', 'restore', 'rollback', 'import')),
  restored_from       int,
  review_status       text CHECK (review_status IN ('in_review', 'approved', 'rejected', 'withdrawn')),
  approval_request_id uuid,
  approved_at         timestamptz,
  published_at        timestamptz,
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid,
  UNIQUE (content_id, version_no)
);
SELECT platform.enable_property_rls('cms.content_versions');

-- Slugs reserved by the latest and the published version (unique per URL space and language).
CREATE TABLE cms.content_slugs (
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  scope        text NOT NULL CHECK (scope IN ('page', 'article', 'gallery')),
  language     text NOT NULL,
  slug         text NOT NULL,
  content_id   uuid NOT NULL REFERENCES cms.contents (id) ON DELETE CASCADE,
  PRIMARY KEY (property_id, scope, language, slug)
);
CREATE INDEX content_slugs_content ON cms.content_slugs (content_id);
SELECT platform.enable_property_rls('cms.content_slugs');

-- Publishing log (FR-CMS-10, CMS publishing log report).
CREATE TABLE cms.publish_events (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  content_id   uuid NOT NULL REFERENCES cms.contents (id) ON DELETE CASCADE,
  kind         text NOT NULL,
  action       text NOT NULL CHECK (action IN ('created', 'saved', 'submitted', 'approved', 'rejected', 'withdrawn', 'scheduled',
                 'published', 'unpublished', 'restored', 'rolled_back', 'archived', 'imported')),
  version_no   int,
  actor_id     uuid,
  actor_name   text,
  note         text,
  at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX publish_events_at ON cms.publish_events (property_id, at DESC);
SELECT platform.enable_property_rls('cms.publish_events');

-- Public navigation menus (FR-CMS-09, Naming Convention §26): the item tree
-- with labels per language lives in items.
CREATE TABLE cms.menus (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  code         text NOT NULL,
  name         text NOT NULL,
  location     text NOT NULL DEFAULT 'header' CHECK (location IN ('header', 'footer', 'mobile', 'sidebar', 'legal')),
  items        jsonb NOT NULL DEFAULT '[]'::jsonb,
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('cms.menus');
SELECT platform.add_touch_trigger('cms.menus');

-- Redirects (old URL → new URL), manual or created when a published slug changes.
CREATE TABLE cms.redirects (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  from_path    text NOT NULL CHECK (from_path ~ '^/'),
  to_path      text NOT NULL,
  status_code  int NOT NULL DEFAULT 301 CHECK (status_code IN (301, 302, 307, 308)),
  source       text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'auto', 'import')),
  note         text,
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz
);
CREATE UNIQUE INDEX redirects_from ON cms.redirects (property_id, from_path) WHERE archived_at IS NULL;
SELECT platform.enable_property_rls('cms.redirects');
SELECT platform.add_touch_trigger('cms.redirects');

-- Contact Information (FR-CMS-04): address, phones, WhatsApp, e-mail, map, opening hours.
CREATE TABLE cms.contacts (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  code           text NOT NULL,
  name           text NOT NULL,
  is_primary     boolean NOT NULL DEFAULT false,
  address        text,
  phones         text[] NOT NULL DEFAULT '{}',
  whatsapp       text,
  email          text,
  latitude       numeric(9, 6) CHECK (latitude BETWEEN -90 AND 90),
  longitude      numeric(9, 6) CHECK (longitude BETWEEN -180 AND 180),
  map_url        text,
  opening_hours  jsonb NOT NULL DEFAULT '[]'::jsonb,   -- [{day, open, close, closed}]
  social_links   jsonb NOT NULL DEFAULT '{}'::jsonb,   -- {instagram: url, …}
  translations   jsonb NOT NULL DEFAULT '{}'::jsonb,   -- {lang: {name, address, note}}
  sort_order     int NOT NULL DEFAULT 0,
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid,
  archived_at    timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('cms.contacts');
SELECT platform.add_touch_trigger('cms.contacts');

-- Course Guide texts per hole (FR-CMS-04): they complete the golf course
-- data (par, distances) which the website reads from /public/golf/info.
CREATE TABLE cms.course_guides (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  course_code   text NOT NULL,
  hole_number   int NOT NULL CHECK (hole_number BETWEEN 1 AND 36),
  title         text,
  translations  jsonb NOT NULL DEFAULT '{}'::jsonb,    -- {lang: {title, description, tips}}
  media_ids     text[] NOT NULL DEFAULT '{}',
  sort_order    int NOT NULL DEFAULT 0,
  status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  updated_by    uuid,
  archived_at   timestamptz,
  UNIQUE (property_id, course_code, hole_number)
);
SELECT platform.enable_property_rls('cms.course_guides');
SELECT platform.add_touch_trigger('cms.course_guides');

-- Website revision per property: bumped by every public change so the
-- website (and its CDN) can revalidate cached pages (FR-CMS-10).
CREATE TABLE cms.site_state (
  property_id  uuid PRIMARY KEY REFERENCES platform.properties (id),
  revision     bigint NOT NULL DEFAULT 0,
  changed_at   timestamptz NOT NULL DEFAULT now()
);
SELECT platform.enable_property_rls('cms.site_state');

SELECT platform.grant_app('cms');

-- +goose Down
DROP TABLE cms.site_state, cms.course_guides, cms.contacts, cms.redirects, cms.menus, cms.publish_events, cms.content_slugs,
  cms.content_versions, cms.contents, cms.media, cms.categories;
