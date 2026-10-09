-- Nearest tee house on the caddy tablet (demo feedback 10 Oct 2026, item
-- 27): every tee house outlet knows the hole it stands by (attribute
-- "hole"); the Halfway House is after hole 9. Existing outlets get the
-- default positions once (data only).

-- +goose Up
UPDATE commercial.outlets SET attributes = attributes || jsonb_build_object('hole', 9)
 WHERE code = 'HALFWAY' AND NOT attributes ? 'hole';
UPDATE commercial.outlets o SET attributes = o.attributes || jsonb_build_object('hole', v.hole)
  FROM (VALUES ('TH1', 2), ('TH2', 8), ('TH3', 5), ('TH4', 12), ('TH5', 14), ('TH6', 17)) AS v(code, hole)
 WHERE o.code = v.code AND o.outlet_type = 'tee_house' AND NOT o.attributes ? 'hole';

-- +goose Down
SELECT 1;
