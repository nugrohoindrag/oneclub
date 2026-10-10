'use client';

import { useState } from 'react';

// Search bar of the bungalow booking (docs/requirement-booking-hotel-mgcc.md
// FR-H07, FR-H09): check-in – check-out, adults, children and a promo code;
// on the bungalow pages, the booking page and the Home. The search has its
// own URL (FR-H08) so it can be shared from Instagram / WhatsApp. "Today"
// is the club's date (WIB).

type Lang = 'id' | 'en';

const ymd = (d: Date) => d.toLocaleDateString('sv', { timeZone: 'Asia/Jakarta' });
const plus = (day: string, n: number) => {
  const d = new Date(`${day}T12:00:00+07:00`);
  d.setDate(d.getDate() + n);
  return ymd(d);
};
const nights = (a: string, b: string) => Math.round((new Date(`${b}T12:00:00Z`).getTime() - new Date(`${a}T12:00:00Z`).getTime()) / 86_400_000);

export interface StaySearchValue { checkin: string; checkout: string; adults: number; children: number; promo: string }

export function StaySearchBar({ lang, initial, maxDate, onSearch, typeId, title }: {
  lang: Lang; initial?: Partial<StaySearchValue>; maxDate?: string; onSearch?: (v: StaySearchValue) => void; typeId?: string; title?: string;
}) {
  const id = lang === 'id';
  const today = ymd(new Date());
  const [v, setV] = useState<StaySearchValue>({
    checkin: initial?.checkin || today, checkout: initial?.checkout || plus(initial?.checkin || today, 1), adults: initial?.adults || 2,
    children: initial?.children ?? 0, promo: initial?.promo ?? '',
  });
  const [error, setError] = useState('');
  const max = maxDate || plus(today, 365);
  const n = nights(v.checkin, v.checkout);
  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    if (v.checkin < today) return setError(id ? 'Check-in paling cepat hari ini.' : 'Check-in is today at the earliest.');
    if (v.checkin > max) return setError(id ? `Booking paling jauh sampai ${max}.` : `Bookings open until ${max}.`);
    if (n < 1) return setError(id ? 'Check-out harus setelah check-in.' : 'Check-out must be after check-in.');
    if (n > 60) return setError(id ? 'Paling lama 60 malam per pencarian.' : 'At most 60 nights per search.');
    setError('');
    if (onSearch) return onSearch(v);
    const q = new URLSearchParams({ checkin: v.checkin, checkout: v.checkout, adults: String(v.adults), children: String(v.children) });
    if (v.promo.trim()) q.set('promo', v.promo.trim().toUpperCase());
    if (typeId) q.set('type', typeId);
    window.location.href = `/${lang}/book/bungalow?${q}`;
  };
  const stepper = (label: string, key: 'adults' | 'children', min: number, top: number) => (
    <div className="w-bk-stepper">
      <span>{label}</span>
      <div>
        <button type="button" aria-label={`${label} −`} disabled={v[key] <= min} onClick={() => setV({ ...v, [key]: v[key] - 1 })}>−</button>
        <output aria-live="polite">{v[key]}</output>
        <button type="button" aria-label={`${label} +`} disabled={v[key] >= top} onClick={() => setV({ ...v, [key]: v[key] + 1 })}>+</button>
      </div>
    </div>
  );
  return (
    <form className="w-bk-search" onSubmit={submit} aria-label={id ? 'Cari bungalow' : 'Search bungalows'}>
      {title && <h3>{title}</h3>}
      <label>Check-in
        <input type="date" value={v.checkin} min={today} max={max} required
          onChange={(e) => {
            const ci = e.target.value;
            setV({ ...v, checkin: ci, checkout: v.checkout <= ci ? plus(ci, 1) : v.checkout });
          }} />
      </label>
      <label>Check-out
        <input type="date" value={v.checkout} min={plus(v.checkin, 1)} required onChange={(e) => setV({ ...v, checkout: e.target.value })} />
      </label>
      <div className="w-bk-nightcount">{n > 0 ? `${n} ${id ? 'malam' : n === 1 ? 'night' : 'nights'}` : '—'}</div>
      {stepper(id ? 'Dewasa' : 'Adults', 'adults', 1, 12)}
      {stepper(id ? 'Anak' : 'Children', 'children', 0, 8)}
      <label>{id ? 'Kode promo' : 'Promo code'}
        <input value={v.promo} onChange={(e) => setV({ ...v, promo: e.target.value })} placeholder={id ? 'opsional' : 'optional'} autoComplete="off" />
      </label>
      <button type="submit" className="w-btn w-bk-go">{id ? 'Cek Ketersediaan' : 'Check Availability'}</button>
      {error && <p className="w-error" role="alert">{error}</p>}
    </form>
  );
}
