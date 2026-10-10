/*
 * Shared types and helpers of the bungalow booking engine of the website
 * (docs/requirement-booking-hotel-mgcc.md Bagian A): the room types and
 * their rates (one price function in the API, FR-H17 — the page never
 * computes a price), the cart kept in the browser (FR-H21), the booking of
 * a link and the labels in the language of the page (FR-H92).
 */
import type { Lang } from '../../../lib';

export interface Amenity { group: string; icon: string; label: string; labelEn?: string }
export interface FAQ { q: string; a: string; qEn?: string; aEn?: string }
export interface RoomType {
  id: string; code: string; name: string; nameEn?: string | null; slug: string; description?: string | null; descriptionEn?: string | null;
  maxAdults: number; maxChildren: number; bedrooms: number; bedConfiguration?: string | null; sizeSqm?: string | null; view?: string | null;
  views: string[]; photos: string[]; facilities: string[]; amenities: Amenity[]; faq: FAQ[]; houseRules?: string | null; units: number; sortOrder: number;
}
export interface Addon { id: string; name: string; category: string; description?: string | null; price: string; unit: string }
export interface Contact { name: string; address: string; phone: string; email: string; whatsApp: string; mapUrl: string }
export interface StayPage {
  types: RoomType[]; checkInTime: string; checkOutTime: string; terms: string; termsEn: string; houseRules: string; houseRulesEn: string; childPolicy: string;
  contact: Contact; methods: string[]; holdMinutes: number; minDate: string; maxDate: string; addons: Addon[];
}
export interface NightPrice { date: string; bar: string; price: string; weekend: boolean; holiday: boolean; season?: string | null }
export interface Rate {
  code: string; name: string; kind: 'rate_plan' | 'package'; eligibility: string; total: string; averagePerNight: string; listTotal: string; savePercent: number;
  discount: string; promotionName?: string | null; includesBreakfast: boolean; taxIncluded: boolean; paymentPolicy: string; depositPercent: string;
  freeCancelHours: number; cancelFeePercent: string; noShowFeePercent: string; nonRefundable: boolean; minNights: number; maxNights?: number | null;
  nights: NightPrice[]; inclusions: { name: string; included: boolean }[];
}
export interface SearchType extends RoomType {
  available: number; full: boolean; lowAvailability: boolean; fitsGuests: boolean; capacityNote?: string | null; closedReason?: string | null;
  fromPerNight?: string | null; rates: Rate[]; packages: Rate[];
}
export interface Search {
  arrivalDate: string; departureDate: string; nights: number; adults: number; children: number; promoCode?: string; promoError?: string | null;
  types: SearchType[]; minDate: string; maxDate: string; holdMinutes: number; methods: string[];
}
export interface RateCardRow { typeId: string; typeName: string; typeNameEn?: string | null; slug: string; ratePlan: string; ratePlanName: string; minNights: number;
  includesBreakfast: boolean; taxIncluded: boolean; weekday: string; weekend: string; sortOrder: number }

/** One bungalow in the cart (FR-H19, FR-H20). */
export interface CartItem {
  key: string; bungalowTypeId: string; typeName: string; ratePlan: string; ratePlanName: string; kind: 'rate_plan' | 'package'; adults: number; children: number;
  maxAdults: number; maxChildren: number; total: string; includesBreakfast: boolean; addons: { addonId: string; quantity: number }[]; occupantName?: string;
}
export interface Cart { checkin: string; checkout: string; promo?: string; items: CartItem[] }

export interface StayAddon { id: string; name: string; quantity: number; units: number; unitPrice: string; total: string; included: boolean; voidedAt?: string | null }
export interface CartLine {
  index: number; typeId: string; typeName: string; ratePlan: string; ratePlanName: string; adults: number; children: number; occupantName?: string;
  nights: NightPrice[]; roomTotal: string; roomList: string; discount: string; promotionName?: string | null; addons: StayAddon[]; addonsTotal: string;
  net: string; service: string; tax: string; total: string; depositRequired: string; paymentPolicy: string; includesBreakfast: boolean; taxIncluded: boolean;
  freeCancelHours: number; cancelFeePercent: string; nonRefundable: boolean; error?: string | null; errorCode?: string | null;
}
export interface CartQuote {
  arrivalDate: string; departureDate: string; nights: number; items: CartLine[]; subtotal: string; discount: string; service: string; tax: string; total: string;
  depositNow: string; payAtHotel: string; promoCode?: string; promoError?: string | null; ok: boolean; holdMinutes: number;
}
export interface BookingStay {
  id: string; stayNo: string; reservationCode: string; typeId?: string | null; typeName: string; unitName?: string | null; arrival: string; departure: string;
  nights: number; adults: number; children: number; occupantName?: string | null; ratePlan: string; ratePlanName: string; includesBreakfast: boolean;
  taxIncluded: boolean; addons: StayAddon[]; total: string; paid: string; depositRequired: string; bookingStatus: string; paymentStatus: string;
  freeCancelHours: number; cancelFeePercent: string; nonRefundable: boolean; freeCancelUntil?: string | null; canCancel: boolean; cancelFee: string;
  token?: string | null;
}
export interface BookingView {
  code: string; token: string; groupNo?: string | null; status: string; bookerName: string; bookerPhone?: string | null; bookerEmail?: string | null;
  stays: BookingStay[]; total: string; paid: string; balance: string; depositDue: string; holdSeconds: number; holdExpiresAt?: string | null;
  payment?: { numbers: string[]; method: string; amount: string; qrString?: string | null; vaNumber?: string | null; expiresAt?: string | null; sandbox: boolean } | null;
  canPay: boolean; checkInTime: string; checkOutTime: string; terms: string; termsEn: string; houseRules: string; houseRulesEn: string; childPolicy: string;
  contact: Contact; methods: string[];
}
export interface Problem { detail?: string; title?: string; code?: string; status?: number; errors?: { field: string; code: string; message: string }[] }

export const money = (v: string | number | null | undefined, lang: Lang) =>
  new Intl.NumberFormat(lang === 'id' ? 'id-ID' : 'en-US', { style: 'currency', currency: 'IDR', maximumFractionDigits: 0 }).format(Number(v ?? 0));
export const dayLabel = (iso: string, lang: Lang, opts: Intl.DateTimeFormatOptions = { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric' }) =>
  new Date(iso.length === 10 ? `${iso}T12:00:00+07:00` : iso).toLocaleDateString(lang === 'id' ? 'id-ID' : 'en-GB', { ...opts, timeZone: 'Asia/Jakarta' });
/** The club's date (WIB) of a moment, YYYY-MM-DD (FR-H94). */
export const ymd = (d: Date) => d.toLocaleDateString('sv', { timeZone: 'Asia/Jakarta' });
export const addDays = (day: string, n: number) => {
  const d = new Date(`${day}T12:00:00+07:00`);
  d.setDate(d.getDate() + n);
  return ymd(d);
};
export const nightsBetween = (a: string, b: string) => Math.round((new Date(`${b}T12:00:00Z`).getTime() - new Date(`${a}T12:00:00Z`).getTime()) / 86_400_000);
export const typeName = (t: { name: string; nameEn?: string | null }, lang: Lang) => (lang === 'en' && t.nameEn ? t.nameEn : t.name);

export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const r = await fetch(path, { cache: 'no-store', ...init, headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) } });
  const d = (await r.json().catch(() => ({}))) as T & Problem;
  if (!r.ok) {
    const e = new Error(d.detail ?? d.title ?? `Error ${r.status}`) as Error & { status?: number; code?: string; errors?: Problem['errors'] };
    e.status = r.status;
    e.code = d.code;
    e.errors = d.errors;
    throw e;
  }
  return d;
}

/** The cart of this browser (FR-H21): nothing is held until the payment. */
const CART_KEY = 'oneclub.bungalow.cart.v1';
const GUEST_KEY = 'oneclub.bungalow.guest.v1';
export function readCart(): Cart | null {
  try {
    const c = JSON.parse(localStorage.getItem(CART_KEY) ?? 'null') as Cart | null;
    if (!c || c.checkin < ymd(new Date())) return null;
    return c;
  } catch {
    return null;
  }
}
export function writeCart(c: Cart | null) {
  try {
    if (c && c.items.length) localStorage.setItem(CART_KEY, JSON.stringify(c));
    else localStorage.removeItem(CART_KEY);
  } catch { /* private window: the cart lives in the page only */ }
}
/** The guest data of the form, kept while paying so a failed payment does not empty it (FR-H36, FR-H99). */
export function readGuest<T>(): T | null {
  try { return JSON.parse(sessionStorage.getItem(GUEST_KEY) ?? 'null') as T | null; } catch { return null; }
}
export function writeGuest(v: unknown) {
  try { sessionStorage.setItem(GUEST_KEY, JSON.stringify(v)); } catch { /* ignore */ }
}

/** Status of §12.3 in the language of the page. */
export const STATUS: Record<string, [string, string]> = {
  awaiting_payment: ['Menunggu Pembayaran', 'Awaiting payment'], confirmed: ['Terkonfirmasi', 'Confirmed'], in_house: ['Menginap', 'In-house'],
  checked_out: ['Check-out', 'Checked out'], cancelled: ['Dibatalkan', 'Cancelled'], no_show: ['No-show', 'No-show'], expired: ['Kedaluwarsa', 'Expired'],
  void: ['Void', 'Void'], requested: ['Permintaan', 'Request'], mixed: ['Beberapa status', 'Mixed'],
};
export const PAY_STATUS: Record<string, [string, string]> = {
  unpaid: ['Belum bayar', 'Unpaid'], pending: ['Menunggu', 'Pending'], partially_paid: ['Deposit dibayar', 'Deposit paid'], paid: ['Lunas', 'Paid'],
  overpaid: ['Lebih bayar', 'Overpaid'], refund_pending: ['Refund diproses', 'Refund pending'], refunded: ['Refund', 'Refunded'], failed: ['Gagal', 'Failed'],
};
export const METHOD: Record<string, [string, string]> = { qris: ['QRIS', 'QRIS'], virtual_account: ['Virtual Account', 'Virtual Account'],
  card: ['Kartu kredit / debit', 'Credit / debit card'] };
export const VIEW: Record<string, [string, string]> = { golf: ['lapangan golf', 'golf course'], lake: ['danau', 'lake'], pool: ['kolam renang', 'pool'],
  garden: ['taman', 'garden'], other: ['lainnya', 'other'] };
export const AMENITY_GROUP: Record<string, [string, string]> = { bathroom: ['Kamar mandi', 'Bathroom'], entertainment: ['Hiburan', 'Entertainment'],
  internet: ['Internet', 'Internet'], kitchen: ['Dapur & minibar', 'Kitchen & minibar'], general: ['Umum', 'General'] };
export const UNIT: Record<string, [string, string]> = { per_stay: ['per menginap', 'per stay'], per_night: ['per malam', 'per night'], per_item: ['per item', 'per item'],
  per_person: ['per orang', 'per person'], per_person_night: ['per orang per malam', 'per person per night'] };

/** Cancellation policy of a rate in a sentence (FR-H14). */
export function cancelPolicy(r: { nonRefundable: boolean; freeCancelHours: number; cancelFeePercent: string }, lang: Lang) {
  const id = lang === 'id';
  if (r.nonRefundable) return id ? 'Non-refundable: tidak dapat dibatalkan atau diubah.' : 'Non-refundable: cannot be cancelled or changed.';
  if (r.freeCancelHours > 0) {
    return id ? `Batal gratis sampai ${r.freeCancelHours} jam sebelum kedatangan; setelahnya biaya ${Number(r.cancelFeePercent)}% dari total.`
      : `Free cancellation until ${r.freeCancelHours} hours before arrival; then a fee of ${Number(r.cancelFeePercent)}% of the total.`;
  }
  return id ? `Biaya pembatalan ${Number(r.cancelFeePercent)}% dari total.` : `Cancellation fee ${Number(r.cancelFeePercent)}% of the total.`;
}
/** Payment policy of a rate in a sentence. */
export function payPolicy(r: { paymentPolicy: string; depositPercent?: string }, lang: Lang) {
  const id = lang === 'id';
  switch (r.paymentPolicy) {
    case 'full_prepayment': return id ? 'Bayar penuh sekarang' : 'Pay in full now';
    case 'pay_at_hotel': return id ? 'Bayar di hotel, tanpa deposit' : 'Pay at the hotel, no deposit';
  }
  return id ? `Deposit ${Number(r.depositPercent || 50)}% sekarang, sisa di hotel` : `${Number(r.depositPercent || 50)}% deposit now, the rest at the hotel`;
}
export const taxLabel = (included: boolean, lang: Lang) => (included
  ? (lang === 'id' ? 'sudah termasuk pajak & service' : 'tax & service included')
  : (lang === 'id' ? 'belum termasuk pajak & service' : 'excluding tax & service'));
