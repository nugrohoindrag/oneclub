/** Server-side helpers for the public website. */
export type Lang = 'id' | 'en';
export const LANGS: Lang[] = ['id', 'en'];

export interface Bootstrap {
  name: string;
  defaultLocale: string;
  currency: string;
  timezone: string;
  branding: {
    appName: string;
    logoUrl?: string | null;
    loginImageUrl?: string | null;
    accent: string;
    primaryColor?: string | null;
    customAccent?: { light: Record<string, string>; dark: Record<string, string> } | null;
  };
  enabledModules: string[];
}

const API = process.env.ONECLUB_API_URL ?? 'http://localhost:8080';

/** Fetches the instance bootstrap (cached 60 s); falls back if the API is down. */
export async function getBootstrap(): Promise<Bootstrap> {
  try {
    const r = await fetch(`${API}/api/v1/public/bootstrap`, { next: { revalidate: 60 } });
    if (r.ok) return (await r.json()) as Bootstrap;
  } catch {
    /* fall through */
  }
  return { name: 'OneClub', defaultLocale: 'id', currency: 'IDR', timezone: 'Asia/Jakarta', branding: { appName: 'OneClub', accent: 'lime' }, enabledModules: [] };
}

export interface CourseInfo {
  id: string; code: string; name: string; holes: number; lengthMeters?: number | null; par?: number | null; description?: string | null; guide?: string | null;
  routes: { code: string; name: string; holeCount: number; par: number; holes: { number: number; par: number; strokeIndex?: number | null; distances: Record<string, number> }[] }[];
  teeSets: { code: string; name: string; color?: string | null; courseRating?: string | null; slope?: number | null }[];
}
export interface GolfInfo {
  clubName: string; courses: CourseInfo[]; dressCode: string; clubRules: string; maxPlayers: number; holdMinutes: number; bookingWindowDays: number;
  captchaSiteKey?: string | null; paymentMethods: string[];
}

/** Golf Course, Course Guide and Hole-by-Hole data (FR-WEB-01..03). */
export async function getGolfInfo(): Promise<GolfInfo | null> {
  try {
    const r = await fetch(`${API}/api/v1/public/golf/info`, { next: { revalidate: 300 } });
    if (r.ok) return (await r.json()) as GolfInfo;
  } catch {
    /* fall through */
  }
  return null;
}

export interface RateRow { segment: string; dayType: string; timeBand: string; price: string; currency: string; pricingMode: string }

/** Published rate card (FR-WEB-04). */
export async function getRates(): Promise<RateRow[]> {
  try {
    const r = await fetch(`${API}/api/v1/public/golf/rates`, { next: { revalidate: 300 } });
    if (r.ok) return ((await r.json()) as { items: RateRow[] }).items ?? [];
  } catch {
    /* fall through */
  }
  return [];
}

export function idr(v: string | number, lang: Lang) {
  return new Intl.NumberFormat(lang === 'id' ? 'id-ID' : 'en-US', { style: 'currency', currency: 'IDR', maximumFractionDigits: 0 }).format(Number(v));
}

/** Website copy (FR-L10N-01) of the OneClub pages the live site design does not cover. */
export const copy = {
  id: { locationTitle: 'Location', locationText: 'Kunjungi club kami.' },
  en: { locationTitle: 'Location', locationText: 'Visit our club.' },
};
