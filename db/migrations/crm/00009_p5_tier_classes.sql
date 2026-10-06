-- PRD P5 EP-18 member tier classes & classification (product owner request
-- on top of FR-LOY-P5-01/02, §16 #14): the Tier master gets a class badge
-- (colour, icon), qualification rules (spend and / or points over a window,
-- membership types), evaluation settings per tier (window, grace months,
-- downgrade allowed) and versioned thresholds: a threshold change waits for
-- the next evaluation (or "re-evaluate now") instead of silently re-tiering
-- members. Manual classification overrides per member (tier, reason, valid
-- until; permission crm.loyalty.tier_override, approval engine) keep the
-- source (auto / manual) on the account and in the tier history.
-- Expand-only (Technical Doc §7.5).

-- +goose Up
ALTER TABLE crm.loyalty_tiers
  ADD COLUMN color                    text NOT NULL DEFAULT '#6B7280' CHECK (color ~ '^#[0-9A-Fa-f]{6}$'),
  ADD COLUMN icon                     text NOT NULL DEFAULT 'workspace_premium' CHECK (icon ~ '^[a-z0-9_]{1,40}$'),
  ADD COLUMN qualify_mode             text NOT NULL DEFAULT 'all' CHECK (qualify_mode IN ('all', 'any')),
  ADD COLUMN membership_type_ids      text[] NOT NULL DEFAULT '{}',
  ADD COLUMN period_months            int CHECK (period_months IS NULL OR period_months BETWEEN 1 AND 60),
  ADD COLUMN grace_months             int CHECK (grace_months IS NULL OR grace_months BETWEEN 0 AND 24),
  ADD COLUMN downgrade_allowed        boolean NOT NULL DEFAULT true,
  -- thresholds in force (the evaluation engine reads these) and their version
  ADD COLUMN threshold_version        int NOT NULL DEFAULT 1,
  ADD COLUMN effective_version        int NOT NULL DEFAULT 1,
  ADD COLUMN pending_thresholds       boolean NOT NULL DEFAULT false,
  ADD COLUMN eff_min_points           bigint NOT NULL DEFAULT 0 CHECK (eff_min_points >= 0),
  ADD COLUMN eff_min_spend            numeric(19,4) NOT NULL DEFAULT 0 CHECK (eff_min_spend >= 0),
  ADD COLUMN eff_qualify_mode         text NOT NULL DEFAULT 'all' CHECK (eff_qualify_mode IN ('all', 'any')),
  ADD COLUMN eff_membership_type_ids  text[] NOT NULL DEFAULT '{}',
  ADD COLUMN eff_period_months        int;
UPDATE crm.loyalty_tiers SET eff_min_points = min_points, eff_min_spend = min_spend;

-- Threshold versions of a tier: pending until the next evaluation run (or
-- "re-evaluate now"), then effective; the older effective one superseded.
CREATE TABLE crm.loyalty_tier_versions (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  tier_id              uuid NOT NULL REFERENCES crm.loyalty_tiers (id) ON DELETE CASCADE,
  version              int NOT NULL,
  min_points           bigint NOT NULL,
  min_spend            numeric(19,4) NOT NULL,
  qualify_mode         text NOT NULL,
  membership_type_ids  text[] NOT NULL DEFAULT '{}',
  period_months        int,
  status               text NOT NULL CHECK (status IN ('pending', 'effective', 'superseded')),
  effective_on         date,
  evaluation_id        uuid REFERENCES crm.loyalty_tier_evaluations (id),
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  UNIQUE (tier_id, version)
);
SELECT platform.enable_property_rls('crm.loyalty_tier_versions');
INSERT INTO crm.loyalty_tier_versions (id, property_id, tier_id, version, min_points, min_spend, qualify_mode, status, effective_on, created_at, created_by)
SELECT gen_random_uuid(), t.property_id, t.id, 1, t.min_points, t.min_spend, 'all', 'effective',
  (t.created_at AT TIME ZONE coalesce((SELECT nullif(p.timezone, '') FROM platform.properties p WHERE p.id = t.property_id),
    (SELECT nullif(i.timezone, '') FROM platform.instance i LIMIT 1), 'Asia/Jakarta'))::date,
  t.created_at, t.created_by FROM crm.loyalty_tiers t;

-- Thresholds written directly (seeds, imports, a new tier) are in force at
-- once as a new effective version; the Tier master API instead raises
-- threshold_version and leaves the change pending (loyalty.tierBeforeWrite).
-- +goose StatementBegin
CREATE FUNCTION crm.loyalty_tier_thresholds() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'UPDATE' THEN
    IF NEW.threshold_version <> OLD.threshold_version OR (NEW.min_points = OLD.min_points AND NEW.min_spend = OLD.min_spend
       AND NEW.qualify_mode = OLD.qualify_mode AND NEW.membership_type_ids = OLD.membership_type_ids
       AND NEW.period_months IS NOT DISTINCT FROM OLD.period_months) THEN
      RETURN NEW;
    END IF;
    NEW.threshold_version := OLD.threshold_version + 1;
  END IF;
  NEW.effective_version := NEW.threshold_version;
  NEW.pending_thresholds := false;
  NEW.eff_min_points := NEW.min_points;
  NEW.eff_min_spend := NEW.min_spend;
  NEW.eff_qualify_mode := NEW.qualify_mode;
  NEW.eff_membership_type_ids := NEW.membership_type_ids;
  NEW.eff_period_months := NEW.period_months;
  RETURN NEW;
END $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION crm.loyalty_tier_version_row() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'UPDATE' AND (NEW.threshold_version = OLD.threshold_version OR NEW.pending_thresholds) THEN
    RETURN NULL;
  END IF;
  UPDATE crm.loyalty_tier_versions SET status = 'superseded' WHERE tier_id = NEW.id AND status IN ('pending', 'effective');
  INSERT INTO crm.loyalty_tier_versions (id, property_id, tier_id, version, min_points, min_spend, qualify_mode, membership_type_ids, period_months,
    status, effective_on, created_by)
  VALUES (gen_random_uuid(), NEW.property_id, NEW.id, NEW.threshold_version, NEW.min_points, NEW.min_spend, NEW.qualify_mode, NEW.membership_type_ids,
    NEW.period_months, 'effective', (now() AT TIME ZONE coalesce((SELECT timezone FROM platform.instance), 'Asia/Jakarta'))::date,
    coalesce(NEW.updated_by, NEW.created_by));
  RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE TRIGGER loyalty_tier_thresholds BEFORE INSERT OR UPDATE ON crm.loyalty_tiers FOR EACH ROW EXECUTE FUNCTION crm.loyalty_tier_thresholds();
CREATE TRIGGER loyalty_tier_version_row AFTER INSERT OR UPDATE ON crm.loyalty_tiers FOR EACH ROW EXECUTE FUNCTION crm.loyalty_tier_version_row();

ALTER TABLE crm.loyalty_tier_evaluations ADD COLUMN applied_versions jsonb NOT NULL DEFAULT '[]'::jsonb;

-- Manual classification overrides.
CREATE TABLE crm.loyalty_tier_overrides (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  account_id           uuid NOT NULL REFERENCES crm.loyalty_accounts (id),
  customer_id          uuid NOT NULL REFERENCES crm.customers (id),
  tier_id              uuid NOT NULL REFERENCES crm.loyalty_tiers (id),
  previous_tier_id     uuid REFERENCES crm.loyalty_tiers (id),
  previous_locked      boolean NOT NULL DEFAULT false,
  reason               text NOT NULL,
  valid_until          date,
  status               text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'active', 'ended', 'revoked', 'rejected', 'cancelled')),
  approval_request_id  uuid,
  applied_at           timestamptz,
  ended_at             timestamptz,
  end_reason           text,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, number)
);
CREATE UNIQUE INDEX loyalty_tier_overrides_open ON crm.loyalty_tier_overrides (account_id) WHERE status IN ('pending', 'active');
CREATE INDEX loyalty_tier_overrides_due ON crm.loyalty_tier_overrides (valid_until) WHERE status = 'active';
SELECT platform.enable_property_rls('crm.loyalty_tier_overrides');
SELECT platform.add_touch_trigger('crm.loyalty_tier_overrides');

-- Source of the tier of an account (auto: the evaluation; manual: Set Tier
-- or an override) and the override in force.
ALTER TABLE crm.loyalty_accounts
  ADD COLUMN tier_source       text NOT NULL DEFAULT 'auto' CHECK (tier_source IN ('auto', 'manual')),
  ADD COLUMN tier_override_id  uuid REFERENCES crm.loyalty_tier_overrides (id);
UPDATE crm.loyalty_accounts SET tier_source = 'manual' WHERE tier_locked;

ALTER TABLE crm.loyalty_tier_history
  ADD COLUMN source       text NOT NULL DEFAULT 'auto' CHECK (source IN ('auto', 'manual')),
  ADD COLUMN override_id  uuid REFERENCES crm.loyalty_tier_overrides (id);
UPDATE crm.loyalty_tier_history SET source = 'manual' WHERE reason LIKE 'manual%';

SELECT platform.grant_app('crm');

-- +goose Down
DROP TRIGGER loyalty_tier_version_row ON crm.loyalty_tiers;
DROP TRIGGER loyalty_tier_thresholds ON crm.loyalty_tiers;
DROP FUNCTION crm.loyalty_tier_version_row();
DROP FUNCTION crm.loyalty_tier_thresholds();
ALTER TABLE crm.loyalty_tier_history DROP COLUMN override_id, DROP COLUMN source;
ALTER TABLE crm.loyalty_accounts DROP COLUMN tier_override_id, DROP COLUMN tier_source;
DROP TABLE crm.loyalty_tier_overrides;
ALTER TABLE crm.loyalty_tier_evaluations DROP COLUMN applied_versions;
DROP TABLE crm.loyalty_tier_versions;
ALTER TABLE crm.loyalty_tiers DROP COLUMN eff_period_months, DROP COLUMN eff_membership_type_ids, DROP COLUMN eff_qualify_mode,
  DROP COLUMN eff_min_spend, DROP COLUMN eff_min_points, DROP COLUMN pending_thresholds, DROP COLUMN effective_version,
  DROP COLUMN threshold_version, DROP COLUMN downgrade_allowed, DROP COLUMN grace_months, DROP COLUMN period_months,
  DROP COLUMN membership_type_ids, DROP COLUMN qualify_mode, DROP COLUMN icon, DROP COLUMN color;
