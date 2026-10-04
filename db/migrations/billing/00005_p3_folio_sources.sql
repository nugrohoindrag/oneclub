-- PRD P3 EP-03 FR-QUO-06: the deposit folio of an accepted quotation whose
-- business line has no converting module (golf, stay, other).

-- +goose Up
ALTER TABLE billing.folios DROP CONSTRAINT folios_source_type_check;
ALTER TABLE billing.folios ADD CONSTRAINT folios_source_type_check CHECK (source_type IN ('golf_booking', 'walk_in', 'membership_fee',
  'membership_renewal', 'bag_storage', 'other', 'reservation', 'stay', 'pos_order', 'voucher_sale', 'sport_entry', 'class_enrollment',
  'locker', 'golf_round', 'membership', 'banquet_event', 'package_booking', 'tournament', 'split', 'quotation'));

-- +goose Down
ALTER TABLE billing.folios DROP CONSTRAINT folios_source_type_check;
ALTER TABLE billing.folios ADD CONSTRAINT folios_source_type_check CHECK (source_type IN ('golf_booking', 'walk_in', 'membership_fee',
  'membership_renewal', 'bag_storage', 'other', 'reservation', 'stay', 'pos_order', 'voucher_sale', 'sport_entry', 'class_enrollment',
  'locker', 'golf_round', 'membership', 'banquet_event', 'package_booking', 'tournament', 'split'));
