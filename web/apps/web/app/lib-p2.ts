/** Server-side helpers of the P2 website pages (sport club, stay, venue,
 * Hall of Fame, reciprocal clubs); P1's helpers stay in lib.ts. */
import { getBootstrap, type Bootstrap } from './lib';

const API = process.env.ONECLUB_API_URL ?? 'http://localhost:8080';

export interface PublicProperty {
  id: string;
  code: string;
  name: string;
}

/**
 * The property the public pages book into and show the CMS content of:
 * ONECLUB_WEBSITE_PROPERTY (property code) when set, else the first property
 * of the instance — the website property of the CMS (/public/cms/site), as
 * the bootstrap lists the properties by name.
 */
export async function getProperty(): Promise<PublicProperty | null> {
  const b = (await getBootstrap()) as Bootstrap & { properties?: PublicProperty[] };
  const list = b.properties ?? [];
  const code = process.env.ONECLUB_WEBSITE_PROPERTY?.trim().toUpperCase();
  const byCode = code ? list.find((p) => p.code.toUpperCase() === code) : undefined;
  if (byCode) return byCode;
  if (list.length > 1) {
    const site = await pub<{ propertyId: string }>('/api/v1/public/cms/site');
    const hit = list.find((p) => p.id === site?.propertyId);
    if (hit) return hit;
  }
  return list[0] ?? null;
}

/** GET a public API path (revalidated every 5 minutes); null when unavailable. */
export async function pub<T>(path: string): Promise<T | null> {
  try {
    const r = await fetch(`${API}${path}`, { next: { revalidate: 300 } });
    if (r.ok) return (await r.json()) as T;
  } catch {
    /* fall through */
  }
  return null;
}

/** A published rate of a business line (FR-WEB-P2-05). */
export interface Rate {
  serviceType: string;
  itemRef?: string | null;
  name: string;
  segment: string;
  dayType?: string | null;
  timeBand?: string | null;
  ratePlan?: string | null;
  package?: string | null;
  unit: string;
  price: string;
  currency: string;
  pricingMode: string;
}

export const rp = (v: string | number) => `Rp ${Number(v).toLocaleString('id-ID')}`;

/** Website menu entries of P2 (added to P1's SiteNav). */
export function p2Nav(lang: string): [string, string][] {
  return [
    [`/${lang}/sport-club`, 'Sport Club'],
    [`/${lang}/bungalow`, 'Stay & Venue'],
    [`/${lang}/hall-of-fame`, 'Hall of Fame'],
  ];
}
