-- Migration wave 2 of PRD P2 (EP-31) on P1's Rhapsody staging: what each
-- legacy row of a P2 export file became, so a load can be repeated.

-- +goose Up
CREATE TABLE staging_rhapsody.id_map (
  property_id  uuid NOT NULL,
  entity       text NOT NULL,
  legacy_id    text NOT NULL,
  target_id    uuid NOT NULL,
  loaded_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (property_id, entity, legacy_id)
);
SELECT platform.enable_property_rls('staging_rhapsody.id_map');
SELECT platform.grant_app('staging_rhapsody');

-- +goose Down
DROP TABLE staging_rhapsody.id_map;
