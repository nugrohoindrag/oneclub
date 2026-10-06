import type { Lang } from '../../lib';

/** A package as the public API lists it (contract K5). */
export interface PublicPackage {
  id: string; code: string; name: string; packageType: string; description?: string | null; pricingMode: string; price: string; taxMode: string;
  minPax: number; maxPax?: number | null; nights: number; validDays: number[]; sellFrom?: string | null; sellTo?: string | null; validFrom?: string | null;
  validTo?: string | null; version: number;
  components: { name: string; componentType: string; quantity: string; perPax: boolean; perNight: boolean; dayOffset: number }[];
  addons: { id: string; name: string; price: string; perPax: boolean; perNight: boolean }[];
}

/** The price unit of a package ("/ person ++", "nett" …). */
export function priceUnit(p: PublicPackage, lang: Lang) {
  const id = lang === 'id';
  const unit = { fixed: '', per_pax: id ? '/ orang' : '/ person', per_night: id ? '/ malam' : '/ night', per_pax_per_night: id ? '/ orang / malam' : '/ person / night' }[p.pricingMode] ?? '';
  return `${unit} ${p.taxMode === 'plus_plus' ? '++' : 'nett'}`;
}
