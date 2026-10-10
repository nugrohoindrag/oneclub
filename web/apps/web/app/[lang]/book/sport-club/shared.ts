/*
 * Shared types and helpers of the Sport Club court booking of the website
 * (docs/requirement-booking-sportclub-mgcc.md §5): the sport list, the slot
 * grid, the cart (kept in the browser, FR-26), checkout, the payment page and
 * the confirmation. Prices come from the API (one price function, FR-14);
 * the page never computes tax or fees itself.
 */
import type { Lang } from '../../../lib';

export interface FacilityContent {
  nameEn?: string; slug?: string; icon?: string; photos?: string[]; description?: Record<string, string>; rules?: Record<string, string[]>;
  amenities?: string[]; faq?: Record<string, string>[]; seoTitle?: Record<string, string>;
}
export interface Facility {
  id: string; code: string; name: string; facilityType?: string | null; usageMode: string; sortOrder: number; onlineBooking: boolean; content: FacilityContent;
  rules: { minHours?: number; maxHours?: number; eveningFrom?: string }; courts: number; indoor: boolean; fromPrice: string | null;
}
export interface Court {
  id: string; code: string; name: string; facilityId: string; surface?: string | null; indoor: boolean; sortOrder: number; onlineBooking: boolean;
  photoUrl?: string | null; priceItem: string;
}
export interface Method { code: string; methodType: string; label: string; fee: string; percent: string; active: boolean }
export interface SportClubPage {
  facilities: Facility[]; courts: Court[]; classPrograms: { id: string; name: string; discipline: string; description?: string | null }[];
  windowDays: number; holdMinutes: number; taxIncluded: boolean; methods: Method[]; terms: Record<string, string>;
}
export interface Slot { start: string; end: string; status: string; price: string | null; listPrice: string | null }
export interface Grid { date: string; courts: { court: Court; slots: Slot[]; free: number }[]; taxIncluded: boolean; windowDays: number }
export interface Line { courtId: string; courtName: string; facility: string; sport: string; start: string; end: string; price: string }
export interface Quote {
  rent: string; discount: string; net: string; tax: string; serviceFee: string; total: string; promoApplied: boolean; promoError?: string; voucher?: boolean; taxIncluded: boolean;
  methods: { code: string; label: string; fee: string; total: string; cheapest: boolean }[];
}
export interface RateCard {
  rates: { item: string; name: string; dayType: string | null; band: string | null; from: string | null; to: string | null; price: string; minHours: number }[];
  packages: { code: string; name: string; uses: string; price: string; items: string[] }[];
}
export interface BookingView {
  booking: {
    code: string; token: string; state: string; payStatus: string; name: string; channel: string; start: string | null; end: string | null; holdSeconds: number;
    charges: string; paid: string; balance: string; tax: string; serviceFee: string;
    lines: { id: string; courtId: string | null; courtName: string; facilityCode: string | null; facilityName: string | null; start: string; end: string; status: string; amount: string | null; state: string }[];
  };
  bill: { description: string; net: string; tax: string; total: string; kind: string }[];
  payment: { number: string; method: string; amount: string; status: string; qrString?: string | null; vaNumber?: string | null; expiresAt?: string | null; checkoutUrl?: string | null; sandbox: boolean } | null;
  club: { name: string; address?: string | null; city?: string | null; phone?: string | null };
  terms: Record<string, string>;
}
export interface Problem { detail?: string; title?: string; code?: string; status?: number }

export const money = (v: string | number | null | undefined, lang: Lang) =>
  new Intl.NumberFormat(lang === 'id' ? 'id-ID' : 'en-US', { style: 'currency', currency: 'IDR', maximumFractionDigits: 0 }).format(Number(v ?? 0));
export const hhmm = (iso: string) => new Date(iso).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', timeZone: 'Asia/Jakarta' });
export const ymd = (d: Date) => d.toLocaleDateString('sv', { timeZone: 'Asia/Jakarta' });
export const dayLabel = (iso: string, lang: Lang, opts: Intl.DateTimeFormatOptions = { weekday: 'short', day: 'numeric', month: 'short' }) =>
  new Date(iso).toLocaleDateString(lang === 'id' ? 'id-ID' : 'en-GB', { ...opts, timeZone: 'Asia/Jakarta' });
export const sportName = (f: Facility, lang: Lang) => (lang === 'en' && f.content?.nameEn ? f.content.nameEn : f.name);
export const sportSlug = (f: Facility) => f.content?.slug || f.code.toLowerCase();
export const SPORT_ICON: Record<string, string> = { tennis: '🎾', futsal: '⚽', basketball: '🏀', volleyball: '🏐', sports_tennis: '🎾', sports_soccer: '⚽',
  sports_basketball: '🏀', sports_volleyball: '🏐' };
export const sportIcon = (f: Facility) => SPORT_ICON[f.content?.icon ?? ''] ?? SPORT_ICON[f.facilityType ?? ''] ?? '🏟️';

/** The cart of the website, in this browser (FR-26): the same cart across the sports. */
const CART_KEY = 'oneclub.sportclub.cart.v2';
export function readCart(): Line[] {
  try {
    const c = JSON.parse(localStorage.getItem(CART_KEY) ?? '[]') as Line[];
    // an hour that has started can no longer be booked
    return c.filter((l) => new Date(l.start).getTime() > Date.now());
  } catch {
    return [];
  }
}
export function writeCart(c: Line[]) {
  try { localStorage.setItem(CART_KEY, JSON.stringify(c)); } catch { /* private window: the cart lives in the page only */ }
}

export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const r = await fetch(path, { cache: 'no-store', ...init, headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) } });
  const d = (await r.json().catch(() => ({}))) as T & Problem;
  if (!r.ok) {
    const e = new Error(d.detail ?? d.title ?? `Error ${r.status}`) as Error & { status?: number };
    e.status = r.status;
    throw e;
  }
  return d;
}

export const STATE_LABEL: Record<string, [string, string]> = {
  awaiting_payment: ['Menunggu pembayaran', 'Awaiting payment'], expired: ['Kedaluwarsa', 'Expired'], scheduled: ['Terkonfirmasi', 'Confirmed'],
  late: ['Belum datang', 'Not arrived'], playing: ['Sedang main', 'Playing'], finished: ['Selesai', 'Finished'], no_show: ['Tidak datang', 'No-show'], void: ['Dibatalkan klub', 'Voided'],
};
