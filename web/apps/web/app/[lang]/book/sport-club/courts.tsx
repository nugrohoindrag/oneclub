'use client';
import { useCallback, useEffect, useMemo, useState } from 'react';
import type { Lang } from '../../../lib';
import { checkoutHref } from '../../../pay-link';

/*
 * Book a Sport Club court like AYO, minus the venue search (one club; demo
 * feedback 10 Oct 2026 #41, docs/requirement-booking-sportclub-mgcc.md):
 * pick the sport → the date → the hour grid of every court (free / booked /
 * closed, with the price of each hour) → the cart → guest checkout → pay.
 * The price on a slot is the price charged (rate card of Rates 2025, tax
 * included); several hours and courts go into one booking and one payment.
 */

interface Facility { id: string; code: string; name: string; facilityType?: string | null; usageMode: string }
interface Court { id: string; code: string; name: string; facilityId: string; surface?: string | null; indoor: boolean; resourceId?: string | null }
interface Slot { start: string; end: string; status: string; price?: string | null }
interface Availability { resources: { resource: { id: string }; slots: Slot[] }[] }
interface Line { courtId: string; courtName: string; start: string; end: string; price: string }
interface Problem { detail?: string; title?: string }

const CART_KEY = 'oneclub.sportclub.cart';
const T = {
  id: {
    sport: 'Pilih cabang olahraga', courts: 'lapangan', date: 'Pilih tanggal', more: 'Tanggal lain', free: 'jadwal tersedia', booked: 'Booked', closed: 'Tutup',
    past: 'Lewat', chosen: 'Dipilih', cart: 'Jadwal dipilih', empty: 'Belum ada jadwal dipilih. Ketuk jam yang tersedia.', subtotal: 'Total (termasuk pajak)',
    next: 'Selanjutnya', back: 'Tambah jadwal', checkout: 'Checkout', name: 'Nama lengkap', phone: 'Nomor ponsel (WhatsApp)', email: 'E-mail',
    method: 'Metode pembayaran', voucher: 'Kode voucher (opsional)', terms: 'Saya setuju dengan syarat & ketentuan: booking tidak dapat dibatalkan dan tidak ada refund.',
    consent: 'Kirimi saya berita dan penawaran (opsional).', pay: 'Bayar sekarang', none: 'Tidak ada jadwal pada tanggal ini — coba tanggal lain.',
    error: 'Jadwal gagal dimuat.', retry: 'Muat ulang', policy: 'Booking tidak dapat dibatalkan dan tidak ada refund. Harga sudah termasuk pajak.',
    taken: 'Jadwal ini baru saja dipesan orang lain — hapus dari keranjang atau pilih jam lain.', indoor: 'Indoor', outdoor: 'Outdoor', from: 'mulai',
    remove: 'Hapus', hour: 'jam', done: 'Booking dibuat', ref: 'Kode booking', due: 'Selesaikan pembayaran', reload: 'Perbarui',
  },
  en: {
    sport: 'Choose a sport', courts: 'courts', date: 'Choose the date', more: 'Other date', free: 'times free', booked: 'Booked', closed: 'Closed',
    past: 'Past', chosen: 'Chosen', cart: 'Chosen times', empty: 'No time chosen yet. Tap a free hour.', subtotal: 'Total (tax included)',
    next: 'Next', back: 'Add more times', checkout: 'Checkout', name: 'Full name', phone: 'Mobile (WhatsApp)', email: 'E-mail',
    method: 'Payment method', voucher: 'Voucher code (optional)', terms: 'I agree to the terms: bookings cannot be cancelled and are not refunded.',
    consent: 'Send me news and offers (optional).', pay: 'Pay now', none: 'No times on this date — try another date.',
    error: 'The times could not be loaded.', retry: 'Reload', policy: 'Bookings cannot be cancelled and are not refunded. Prices include tax.',
    taken: 'Someone just booked this time — remove it from the cart or choose another hour.', indoor: 'Indoor', outdoor: 'Outdoor', from: 'from',
    remove: 'Remove', hour: 'h', done: 'Booking created', ref: 'Booking code', due: 'Complete the payment', reload: 'Refresh',
  },
};

const money = (v: string | number, lang: Lang) =>
  new Intl.NumberFormat(lang === 'id' ? 'id-ID' : 'en-US', { style: 'currency', currency: 'IDR', maximumFractionDigits: 0 }).format(Number(v));
const hhmm = (iso: string) => new Date(iso).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', timeZone: 'Asia/Jakarta' });
const ymd = (d: Date) => d.toLocaleDateString('sv', { timeZone: 'Asia/Jakarta' });
const SPORT_ICON: Record<string, string> = { tennis: '🎾', futsal: '⚽', basketball: '🏀', volleyball: '🏐' };

function readCart(): Line[] {
  try { return JSON.parse(localStorage.getItem(CART_KEY) ?? '[]') as Line[]; } catch { return []; }
}
function writeCart(c: Line[]) {
  try { localStorage.setItem(CART_KEY, JSON.stringify(c)); } catch { /* private window: the cart lives in the page only */ }
}

export function SportCourtBooking({ lang, propertyId, facilities, courts }: { lang: Lang; propertyId: string; facilities: Facility[]; courts: Court[] }) {
  const t = T[lang];
  const id = lang === 'id';
  const sports = facilities.filter((f) => f.usageMode === 'slot_booking' && courts.some((c) => c.facilityId === f.id));
  const [sport, setSport] = useState(sports.length === 1 ? sports[0].id : '');
  const days = useMemo(() => Array.from({ length: 7 }, (_, i) => { const d = new Date(); d.setDate(d.getDate() + i); return d; }), []);
  const [date, setDate] = useState(ymd(new Date()));
  const [grid, setGrid] = useState<Record<string, Slot[]>>({});
  const [failed, setFailed] = useState(false);
  const [open, setOpen] = useState<string>('');
  const [cart, setCartState] = useState<Line[]>([]);
  const [step, setStep] = useState<'pick' | 'checkout'>('pick');
  const [guest, setGuest] = useState({ name: '', phone: '', email: '', website: '' });
  const [method, setMethod] = useState('qris');
  const [code, setCode] = useState('');
  const [agree, setAgree] = useState(false);
  const [optIn, setOptIn] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [done, setDone] = useState<{ reference: string; status: string } | null>(null);
  useEffect(() => setCartState(readCart()), []);
  const setCart = (c: Line[]) => { setCartState(c); writeCart(c); };
  const mine = courts.filter((c) => c.facilityId === sport && c.resourceId);
  const max = ymd(new Date(Date.now() + 30 * 86_400_000));

  const load = useCallback(async () => {
    if (!sport) return;
    setFailed(false);
    try {
      const out: Record<string, Slot[]> = {};
      await Promise.all(courts.filter((c) => c.facilityId === sport && c.resourceId).map(async (c) => {
        const r = await fetch(`/api/v1/public/availability?propertyId=${propertyId}&resourceType=sport_court&resourceId=${c.resourceId}&date=${date}`);
        if (!r.ok) throw new Error(String(r.status));
        const a = (await r.json()) as Availability;
        out[c.id] = a.resources[0]?.slots ?? [];
      }));
      setGrid(out);
      setOpen((o) => o || courts.find((c) => c.facilityId === sport)?.id || '');
    } catch {
      setFailed(true);
    }
  }, [sport, date, courts, propertyId]);
  useEffect(() => {
    void load();
    const timer = setInterval(() => void load(), 30_000);
    return () => clearInterval(timer);
  }, [load]);

  const inCart = (c: Court, s: Slot) => cart.some((l) => l.courtId === c.id && l.start === s.start);
  const toggle = (c: Court, s: Slot) => {
    if (inCart(c, s)) setCart(cart.filter((l) => !(l.courtId === c.id && l.start === s.start)));
    else setCart([...cart, { courtId: c.id, courtName: c.name, start: s.start, end: s.end, price: s.price ?? '0' }].sort((a, b) => a.start.localeCompare(b.start)));
  };
  const total = cart.reduce((n, l) => n + Number(l.price), 0);
  // a chosen hour someone else took meanwhile
  const taken = (l: Line) => (grid[l.courtId] ?? []).some((s) => s.start === l.start && s.status !== 'available');
  const fromPrice = (f: Facility) => {
    const prices = courts.filter((c) => c.facilityId === f.id).flatMap((c) => grid[c.id] ?? []).map((s) => Number(s.price)).filter((p) => p > 0);
    return prices.length ? Math.min(...prices) : 0;
  };

  const pay = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      const body = { propertyId, guest, lines: cart.map((l) => ({ courtId: l.courtId, start: l.start, end: l.end })), payMethod: method,
        voucherCode: code || undefined, consent: optIn };
      const r = await fetch('/api/v1/public/court-bookings', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      const d = (await r.json().catch(() => ({}))) as Problem & { reference?: string; status?: string; checkout?: { online?: { checkoutUrl?: string | null; status?: string } } };
      if (!r.ok) throw new Error(d.detail ?? d.title ?? `Error ${r.status}`);
      setCart([]);
      const url = checkoutHref(d.checkout?.online?.checkoutUrl, lang, `/${lang}/book/sport-club?ref=${encodeURIComponent(d.reference ?? '')}`);
      if (url && d.checkout?.online?.status === 'pending') { window.location.href = url; return; }
      setDone({ reference: d.reference ?? '', status: d.status ?? '' });
    } catch (err) {
      setError((err as Error).message); // the cart stays: change it or try again
    } finally {
      setBusy(false);
    }
  };

  if (done) {
    return (
      <div className="w-card" role="status">
        <h2>{t.done}</h2>
        <p>{t.ref} <strong>{done.reference}</strong> · {done.status}</p>
        <p className="w-muted">{t.policy}</p>
      </div>
    );
  }

  if (step === 'checkout') {
    return (
      <form className="w-card w-form" onSubmit={pay}>
        <h2 style={{ gridColumn: '1 / -1', margin: 0 }}>{t.checkout}</h2>
        <CartList lang={lang} cart={cart} taken={taken} onRemove={(l) => setCart(cart.filter((x) => x !== l))} />
        <label>{t.name}<input required value={guest.name} onChange={(e) => setGuest({ ...guest, name: e.target.value })} /></label>
        <label>{t.phone}<input type="tel" value={guest.phone} onChange={(e) => setGuest({ ...guest, phone: e.target.value })} /></label>
        <label>{t.email}<input type="email" value={guest.email} onChange={(e) => setGuest({ ...guest, email: e.target.value })} /></label>
        <label aria-hidden="true" style={{ position: 'absolute', left: -9999 }}>Website<input tabIndex={-1} autoComplete="off" value={guest.website}
          onChange={(e) => setGuest({ ...guest, website: e.target.value })} /></label>
        <label>{t.method}
          <select value={method} onChange={(e) => setMethod(e.target.value)}>
            <option value="qris">QRIS</option><option value="virtual_account">Virtual Account</option><option value="card">{id ? 'Kartu kredit' : 'Card'}</option>
          </select>
        </label>
        <label>{t.voucher}<input value={code} onChange={(e) => setCode(e.target.value)} /></label>
        <label style={{ gridColumn: '1 / -1', display: 'flex', gap: 8, alignItems: 'flex-start' }}>
          <input type="checkbox" checked={agree} onChange={(e) => setAgree(e.target.checked)} style={{ width: 'auto', marginTop: 4 }} /><span>{t.terms}</span></label>
        <label style={{ gridColumn: '1 / -1', display: 'flex', gap: 8, alignItems: 'flex-start' }}>
          <input type="checkbox" checked={optIn} onChange={(e) => setOptIn(e.target.checked)} style={{ width: 'auto', marginTop: 4 }} /><span>{t.consent}</span></label>
        {error && <p className="w-error" role="alert" style={{ gridColumn: '1 / -1' }}>{error}</p>}
        <div style={{ gridColumn: '1 / -1', display: 'flex', flexWrap: 'wrap', gap: 8 }}>
          <button type="button" className="w-btn w-btn-ghost" onClick={() => setStep('pick')}>{t.back}</button>
          <button className="w-btn" disabled={busy || !cart.length || !agree || !guest.name || (!guest.phone && !guest.email) || cart.some(taken)}>
            {busy ? '…' : `${t.pay} · ${money(total, lang)}`}</button>
        </div>
      </form>
    );
  }

  return (
    <div className="w-sc">
      <section aria-label={t.sport}>
        <h2>{t.sport}</h2>
        <div className="w-grid">
          {sports.map((f) => {
            const n = courts.filter((c) => c.facilityId === f.id);
            const p = sport === f.id ? fromPrice(f) : 0;
            return (
              <button key={f.id} type="button" className="w-card w-sc-sport" aria-pressed={sport === f.id} onClick={() => { setSport(f.id); setOpen(''); }}>
                <span style={{ fontSize: 28 }} aria-hidden="true">{SPORT_ICON[f.facilityType ?? ''] ?? '🏟️'}</span>
                <strong>{f.name}</strong>
                <span className="w-muted">{n.length} {t.courts} · {n.some((c) => c.indoor) ? t.indoor : t.outdoor}{p ? ` · ${t.from} ${money(p, lang)}/${t.hour}` : ''}</span>
              </button>
            );
          })}
        </div>
      </section>
      {sport && (
        <section aria-label={t.date} style={{ marginTop: 24 }}>
          <h2>{t.date}</h2>
          <div className="w-sc-days" role="tablist">
            {days.map((d) => {
              const v = ymd(d);
              return (
                <button key={v} type="button" role="tab" aria-selected={date === v} className="w-sc-day" onClick={() => setDate(v)}>
                  <span>{d.toLocaleDateString(id ? 'id-ID' : 'en-GB', { weekday: 'short', timeZone: 'Asia/Jakarta' })}</span>
                  <strong>{d.toLocaleDateString(id ? 'id-ID' : 'en-GB', { day: 'numeric', month: 'short', timeZone: 'Asia/Jakarta' })}</strong>
                </button>
              );
            })}
            <label className="w-sc-day">{t.more}<input type="date" value={date} min={ymd(new Date())} max={max} onChange={(e) => e.target.value && setDate(e.target.value)} /></label>
          </div>
          <p className="w-muted">{t.policy}</p>
          {failed && <p className="w-error" role="alert">{t.error} <button type="button" className="w-btn w-btn-ghost" onClick={() => void load()}>{t.retry}</button></p>}
          {mine.map((c) => {
            const slots = grid[c.id] ?? [];
            const free = slots.filter((s) => s.status === 'available').length;
            return (
              <div key={c.id} className="w-card w-sc-court">
                <button type="button" className="w-sc-court-head" aria-expanded={open === c.id} onClick={() => setOpen(open === c.id ? '' : c.id)}>
                  <strong>{c.name}</strong><span className="w-muted">{c.surface ?? ''} · {c.indoor ? t.indoor : t.outdoor}</span>
                  <span className="w-sc-free">{free} {t.free}</span>
                </button>
                {open === c.id && (
                  slots.length === 0 ? <p className="w-muted">{t.none}</p> : (
                    <div className="w-sc-slots">
                      {slots.map((s) => {
                        const chosen = inCart(c, s);
                        const label = s.status === 'available' ? (s.price ? money(s.price, lang) : '—')
                          : s.status === 'reserved' ? t.booked : new Date(s.end) < new Date() ? t.past : t.closed;
                        return (
                          <button key={s.start} type="button" className="w-sc-slot" data-status={chosen ? 'chosen' : s.status} disabled={s.status !== 'available' && !chosen}
                            aria-pressed={chosen} onClick={() => toggle(c, s)}>
                            <small>60 min</small><strong>{hhmm(s.start)}–{hhmm(s.end)}</strong><span>{chosen ? t.chosen : label}</span>
                          </button>
                        );
                      })}
                    </div>
                  )
                )}
              </div>
            );
          })}
        </section>
      )}
      <aside className="w-card w-sc-cart" aria-label={t.cart}>
        <h2>{t.cart} ({cart.length})</h2>
        {cart.length === 0 ? <p className="w-muted">{t.empty}</p> : (
          <>
            <CartList lang={lang} cart={cart} taken={taken} onRemove={(l) => setCart(cart.filter((x) => x !== l))} />
            <button type="button" className="w-btn" disabled={cart.some(taken)} onClick={() => setStep('checkout')}>{t.next}</button>
          </>
        )}
      </aside>
    </div>
  );
}

function CartList({ lang, cart, taken, onRemove }: { lang: Lang; cart: Line[]; taken: (l: Line) => boolean; onRemove: (l: Line) => void }) {
  const t = T[lang];
  const total = cart.reduce((n, l) => n + Number(l.price), 0);
  const byCourt = [...new Set(cart.map((l) => l.courtName))];
  return (
    <div style={{ gridColumn: '1 / -1' }}>
      {byCourt.map((court) => (
        <div key={court} style={{ marginBottom: 8 }}>
          <strong>{court}</strong>
          {cart.filter((l) => l.courtName === court).map((l) => (
            <div key={l.start} className="w-sc-line" data-taken={taken(l) || undefined}>
              <span>{new Date(l.start).toLocaleDateString(lang === 'id' ? 'id-ID' : 'en-GB', { weekday: 'short', day: 'numeric', month: 'short', timeZone: 'Asia/Jakarta' })}
                {' '}{hhmm(l.start)}–{hhmm(l.end)}</span>
              <span>{money(l.price, lang)}</span>
              <button type="button" className="w-sc-remove" aria-label={`${t.remove} ${hhmm(l.start)}`} onClick={() => onRemove(l)}>✕</button>
              {taken(l) && <span className="w-error" style={{ gridColumn: '1 / -1' }}>{t.taken}</span>}
            </div>
          ))}
        </div>
      ))}
      <div className="w-sc-line"><strong>{t.subtotal}</strong><strong>{money(total, lang)}</strong><span /></div>
    </div>
  );
}
