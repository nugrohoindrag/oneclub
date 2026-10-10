'use client';
import { useCallback, useEffect, useMemo, useState } from 'react';
import type { Lang } from '../../../lib';
import {
  api, dayLabel, hhmm, money, readCart, sportName, writeCart, ymd, type Court, type Facility, type Grid, type Line, type Method, type Quote, type Slot,
} from './shared';

/*
 * Book a Sport Club court like AYO, minus the venue search (one club;
 * docs/requirement-booking-sportclub-mgcc.md §5): the date (7-day tabs, a
 * calendar up to the booking window) → the time of day and the court type →
 * the hour grid of every court with its price → the cart → checkout (renter,
 * method with its service fee, voucher, cost breakdown, terms) → the payment
 * page. Slots refresh every 30 seconds; the server refuses a taken slot and
 * the cart stays (FR-138).
 */

const T = {
  id: {
    date: 'Pilih tanggal', calendar: 'Kalender', time: 'Waktu', all: 'Semua', morning: 'Pagi', afternoon: 'Siang', evening: 'Malam', type: 'Jenis lapangan',
    free: 'jadwal tersedia', booked: 'Booked', closed: 'Tutup', past: 'Lewat', blocked: 'Tutup', chosen: 'Dipilih', min: '60 menit', cart: 'Keranjang',
    empty: 'Belum ada jadwal dipilih. Ketuk jam yang tersedia.', next: 'Lanjut ke pembayaran', none: 'Tidak ada jadwal pada tanggal ini — coba tanggal lain.',
    error: 'Jadwal gagal dimuat.', retry: 'Muat ulang', taken: 'Jadwal ini baru saja dipesan orang lain — hapus dari keranjang.', indoor: 'Indoor', outdoor: 'Outdoor',
    remove: 'Hapus', subtotal: 'Subtotal sewa', back: 'Tambah jadwal', checkout: 'Checkout', renter: 'Data Penyewa', name: 'Nama lengkap',
    phone: 'Nomor ponsel (WhatsApp)', email: 'E-mail', schedule: 'Jadwal', method: 'Metode pembayaran', pick: 'Pilih metode', cheapest: 'Termurah',
    fee: 'biaya layanan', voucher: 'Kode voucher / promo', apply: 'Pakai', costs: 'Rincian biaya', rent: 'Biaya sewa', discount: 'Diskon',
    tax: 'Pajak', service: 'Biaya layanan', total: 'Total bayar', consent: 'Kirimi saya berita dan penawaran (opsional).', pay: 'Bayar Sekarang',
    takenServer: 'Ada jadwal yang sudah tidak tersedia. Jadwal diperbarui — hapus yang bertanda merah lalu coba lagi.', priceNote: 'Harga per jam',
    taxIn: 'sudah termasuk pajak', taxEx: 'belum termasuk pajak', cartOther: 'jadwal cabor lain di keranjang', clear: 'Kosongkan',
    voucherNote: 'Saldo voucher dipakai saat pembayaran; sisa tagihan (bila ada) dibayar dengan metode pilihan.',
  },
  en: {
    date: 'Choose the date', calendar: 'Calendar', time: 'Time', all: 'All', morning: 'Morning', afternoon: 'Afternoon', evening: 'Evening', type: 'Court type',
    free: 'times free', booked: 'Booked', closed: 'Closed', past: 'Past', blocked: 'Closed', chosen: 'Chosen', min: '60 min', cart: 'Cart',
    empty: 'No time chosen yet. Tap a free hour.', next: 'Continue to payment', none: 'No times on this date — try another date.',
    error: 'The times could not be loaded.', retry: 'Reload', taken: 'Someone just booked this time — remove it from the cart.', indoor: 'Indoor', outdoor: 'Outdoor',
    remove: 'Remove', subtotal: 'Rent subtotal', back: 'Add more times', checkout: 'Checkout', renter: 'Renter details', name: 'Full name',
    phone: 'Mobile (WhatsApp)', email: 'E-mail', schedule: 'Schedule', method: 'Payment method', pick: 'Choose a method', cheapest: 'Cheapest',
    fee: 'service fee', voucher: 'Voucher / promo code', apply: 'Apply', costs: 'Cost breakdown', rent: 'Court rent', discount: 'Discount',
    tax: 'Tax', service: 'Service fee', total: 'Total to pay', consent: 'Send me news and offers (optional).', pay: 'Pay Now',
    takenServer: 'Some times are no longer free. The grid is refreshed — remove the ones in red and try again.', priceNote: 'Price per hour',
    taxIn: 'tax included', taxEx: 'before tax', cartOther: 'times of another sport in the cart', clear: 'Clear',
    voucherNote: 'The voucher balance pays at checkout; any rest is paid with the chosen method.',
  },
};

type Band = '' | 'morning' | 'afternoon' | 'evening';
const bandOf = (iso: string): Band => {
  const h = Number(new Date(iso).toLocaleTimeString('en-GB', { hour: '2-digit', hour12: false, timeZone: 'Asia/Jakarta' }).slice(0, 2));
  return h < 12 ? 'morning' : h < 17 ? 'afternoon' : 'evening';
};

export function SportCourtBooking({ lang, propertyId, sport, courts, methods, terms, windowDays, taxIncluded }: {
  lang: Lang; propertyId: string; sport: Facility; courts: Court[]; methods: Method[]; terms: Record<string, string>; windowDays: number; taxIncluded: boolean;
}) {
  const t = T[lang];
  const days = useMemo(() => Array.from({ length: 7 }, (_, i) => { const d = new Date(); d.setDate(d.getDate() + i); return ymd(d); }), []);
  const [date, setDate] = useState(days[0]);
  const [band, setBand] = useState<Band>('');
  const [surface, setSurface] = useState('');
  const [grid, setGrid] = useState<Grid | null>(null);
  const [failed, setFailed] = useState('');
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const [cart, setCartState] = useState<Line[]>([]);
  const [step, setStep] = useState<'pick' | 'checkout'>('pick');
  useEffect(() => setCartState(readCart()), []);
  const setCart = (c: Line[]) => { setCartState(c); writeCart(c); };
  const max = ymd(new Date(Date.now() + windowDays * 86_400_000));

  const load = useCallback(async () => {
    try {
      setGrid(await api<Grid>(`/api/v1/public/sport-club/grid?propertyId=${propertyId}&facility=${sport.id}&date=${date}`));
      setFailed('');
    } catch (e) {
      setFailed((e as Error).message);
    }
  }, [propertyId, sport.id, date]);
  useEffect(() => {
    void load();
    const timer = setInterval(() => void load(), 30_000); // FR-24
    return () => clearInterval(timer);
  }, [load]);

  const inCart = (c: Court, s: Slot) => cart.some((l) => l.courtId === c.id && l.start === s.start);
  const toggle = (c: Court, s: Slot) => {
    if (inCart(c, s)) setCart(cart.filter((l) => !(l.courtId === c.id && l.start === s.start)));
    else setCart([...cart, { courtId: c.id, courtName: c.name, facility: sportName(sport, lang), sport: sport.id, start: s.start, end: s.end, price: s.price ?? '0' }]
      .sort((a, b) => a.start.localeCompare(b.start)));
  };
  // a chosen hour someone else took meanwhile (this sport and date)
  const taken = (l: Line) => (grid?.courts ?? []).some((g) => g.court.id === l.courtId && g.slots.some((s) => s.start === l.start && s.status !== 'available'));
  const surfaces = [...new Set(courts.map((c) => c.surface ?? '').filter(Boolean))];
  const shown = (grid?.courts ?? []).filter((g) => !surface || (g.court.surface ?? '') === surface);

  if (step === 'checkout') {
    return <Checkout lang={lang} propertyId={propertyId} cart={cart} setCart={setCart} taken={taken} methods={methods} terms={terms}
      onBack={() => setStep('pick')} onTaken={() => { setStep('pick'); void load(); }} />;
  }
  return (
    <div className="w-sc">
      <section aria-label={t.date}>
        <div className="w-sc-days" role="tablist" aria-label={t.date}>
          {days.map((v) => (
            <button key={v} type="button" role="tab" aria-selected={date === v} className="w-sc-day" onClick={() => setDate(v)}>
              <span>{dayLabel(`${v}T12:00:00+07:00`, lang, { weekday: 'short' })}</span>
              <strong>{dayLabel(`${v}T12:00:00+07:00`, lang, { day: 'numeric', month: 'short' })}</strong>
            </button>
          ))}
          <label className="w-sc-day" aria-selected={!days.includes(date)}>📅 {t.calendar}
            <input type="date" value={date} min={days[0]} max={max} onChange={(e) => e.target.value && setDate(e.target.value)} /></label>
        </div>
        <div className="w-sc-filters">
          <div className="w-sc-seg" role="group" aria-label={t.time}>
            {(['', 'morning', 'afternoon', 'evening'] as Band[]).map((b) => (
              <button key={b || 'all'} type="button" aria-pressed={band === b} onClick={() => setBand(b)}>{b ? t[b] : t.all}</button>
            ))}
          </div>
          {surfaces.length > 1 && (
            <div className="w-sc-seg" role="group" aria-label={t.type}>
              <button type="button" aria-pressed={!surface} onClick={() => setSurface('')}>{t.all}</button>
              {surfaces.map((x) => <button key={x} type="button" aria-pressed={surface === x} onClick={() => setSurface(x)}>{x}</button>)}
            </div>
          )}
        </div>
        <p className="w-muted">{t.priceNote} {taxIncluded ? t.taxIn : t.taxEx}.</p>
        {failed && <p className="w-error" role="alert">{t.error} {failed} <button type="button" className="w-btn w-btn-ghost" onClick={() => void load()}>{t.retry}</button></p>}
        {shown.map((g, i) => {
          const c = g.court;
          const slots = g.slots.filter((s) => !band || bandOf(s.start) === band);
          const isOpen = open[c.id] ?? i === 0;
          return (
            <div key={c.id} className="w-card w-sc-court">
              <button type="button" className="w-sc-court-head" aria-expanded={isOpen} onClick={() => setOpen({ ...open, [c.id]: !isOpen })}>
                {c.photoUrl && <img src={c.photoUrl} alt="" className="w-sc-thumb" />}
                <span><strong>{c.name}</strong><br /><span className="w-muted">{[sportName(sport, lang), c.surface, c.indoor ? t.indoor : t.outdoor].filter(Boolean).join(' · ')}</span></span>
                <span className="w-sc-free" data-none={g.free === 0 || undefined}>{g.free} {t.free}</span>
              </button>
              {isOpen && (slots.length === 0 ? <p className="w-muted">{t.none}</p> : (
                <div className="w-sc-slots">
                  {slots.map((s) => {
                    const chosen = inCart(c, s);
                    const label = s.status === 'available' ? <>{s.listPrice && <s>{money(s.listPrice, lang)}</s>} {money(s.price, lang)}</>
                      : s.status === 'booked' ? t.booked : s.status === 'past' ? t.past : t.closed;
                    return (
                      <button key={s.start} type="button" className="w-sc-slot" data-status={chosen ? 'chosen' : s.status} disabled={s.status !== 'available' && !chosen}
                        aria-pressed={chosen} onClick={() => toggle(c, s)}>
                        <small>{t.min}</small><strong>{hhmm(s.start)}–{hhmm(s.end)}</strong><span>{chosen ? t.chosen : label}</span>
                      </button>
                    );
                  })}
                </div>
              ))}
            </div>
          );
        })}
      </section>
      <aside className="w-card w-sc-cart" aria-label={t.cart}>
        <h2>{t.cart} ({cart.length})</h2>
        {cart.length === 0 ? <p className="w-muted">{t.empty}</p> : (
          <>
            <CartList lang={lang} cart={cart} taken={taken} onRemove={(l) => setCart(cart.filter((x) => x !== l))} />
            <div className="w-sc-line"><strong>{t.subtotal}</strong><strong>{money(cart.reduce((n, l) => n + Number(l.price), 0), lang)}</strong><span /></div>
            <button type="button" className="w-btn" disabled={cart.some(taken)} onClick={() => setStep('checkout')}>{t.next}</button>
            <button type="button" className="w-btn w-btn-ghost" onClick={() => setCart([])}>{t.clear}</button>
          </>
        )}
      </aside>
    </div>
  );
}

export function CartList({ lang, cart, taken, onRemove }: { lang: Lang; cart: Line[]; taken: (l: Line) => boolean; onRemove?: (l: Line) => void }) {
  const t = T[lang];
  const groups = [...new Set(cart.map((l) => `${l.facility} · ${l.courtName}`))];
  return (
    <div>
      {groups.map((g) => (
        <div key={g} style={{ marginBottom: 8 }}>
          <strong>{g}</strong>
          {cart.filter((l) => `${l.facility} · ${l.courtName}` === g).map((l) => (
            <div key={l.courtId + l.start} className="w-sc-line" data-taken={taken(l) || undefined}>
              <span>{dayLabel(l.start, lang)} {hhmm(l.start)}–{hhmm(l.end)}</span>
              <span>{money(l.price, lang)}</span>
              {onRemove ? <button type="button" className="w-sc-remove" aria-label={`${t.remove} ${hhmm(l.start)}`} onClick={() => onRemove(l)}>✕</button> : <span />}
              {taken(l) && <span className="w-error" style={{ gridColumn: '1 / -1' }}>{t.taken}</span>}
            </div>
          ))}
        </div>
      ))}
    </div>
  );
}

function Checkout({ lang, propertyId, cart, setCart, taken, methods, terms, onBack, onTaken }: {
  lang: Lang; propertyId: string; cart: Line[]; setCart: (c: Line[]) => void; taken: (l: Line) => boolean; methods: Method[]; terms: Record<string, string>;
  onBack: () => void; onTaken: () => void;
}) {
  const t = T[lang];
  const [guest, setGuest] = useState({ name: '', phone: '', email: '', website: '' });
  const [method, setMethod] = useState('');
  const [code, setCode] = useState('');
  const [promo, setPromo] = useState('');
  const [agree, setAgree] = useState(false);
  const [optIn, setOptIn] = useState(false);
  const [quote, setQuote] = useState<Quote | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const key = JSON.stringify([cart.map((l) => [l.courtId, l.start]), promo, method]);
  useEffect(() => {
    if (!cart.length) return undefined;
    let live = true;
    api<Quote>('/api/v1/public/sport-club/quote', { method: 'POST', body: JSON.stringify({ propertyId, lines: cart.map((l) => ({ courtId: l.courtId, start: l.start, end: l.end })),
      promoCode: promo || undefined, method: method || undefined }) })
      .then((q) => { if (live) { setQuote(q); setError(''); } }, (e: Error) => { if (live) setError(e.message); });
    return () => { live = false; };
  }, [key]);
  const fees = new Map((quote?.methods ?? []).map((m) => [m.code, m]));
  const pay = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      const d = await api<{ token?: string; reference: string }>('/api/v1/public/court-bookings', { method: 'POST', body: JSON.stringify({
        propertyId, guest, lines: cart.map((l) => ({ courtId: l.courtId, start: l.start, end: l.end })), payMethod: method, voucherCode: promo || undefined,
        consent: optIn, terms: agree }) });
      // the cart of this booking is kept for a failed payment (back to the cart, FR-34)
      try { sessionStorage.setItem(`oneclub.sportclub.paid-cart.${d.token}`, JSON.stringify(cart)); } catch { /* ignore */ }
      setCart([]);
      window.location.href = `/${lang}/book/sport-club/pay/${d.token}`;
    } catch (err) {
      const status = (err as { status?: number }).status;
      if (status === 409) { setError(t.takenServer); onTaken(); } else setError((err as Error).message); // the cart stays (FR-138)
      setBusy(false);
    }
  };
  const valid = agree && method && guest.name.trim() && (guest.phone.trim() || guest.email.trim()) && cart.length > 0 && !cart.some(taken);
  return (
    <form className="w-sc-checkout" onSubmit={pay}>
      <div className="w-card">
        <h2>{t.renter}</h2>
        <div className="w-form">
          <label>{t.name}<input required autoComplete="name" value={guest.name} onChange={(e) => setGuest({ ...guest, name: e.target.value })} /></label>
          <label>{t.phone}<input type="tel" autoComplete="tel" value={guest.phone} onChange={(e) => setGuest({ ...guest, phone: e.target.value })} /></label>
          <label>{t.email}<input type="email" autoComplete="email" value={guest.email} onChange={(e) => setGuest({ ...guest, email: e.target.value })} /></label>
          <label aria-hidden="true" style={{ position: 'absolute', left: -9999 }}>Website<input tabIndex={-1} autoComplete="off" value={guest.website}
            onChange={(e) => setGuest({ ...guest, website: e.target.value })} /></label>
        </div>
        <h2 style={{ marginTop: 20 }}>{t.schedule}</h2>
        <CartList lang={lang} cart={cart} taken={taken} onRemove={(l) => setCart(cart.filter((x) => x !== l))} />
        <button type="button" className="w-btn w-btn-ghost" onClick={onBack}>{t.back}</button>
      </div>
      <div className="w-card">
        <h2>{t.method}</h2>
        <div className="w-sc-methods" role="radiogroup" aria-label={t.method}>
          {methods.filter((m) => m.active !== false).map((m) => {
            const q = fees.get(m.code);
            return (
              <label key={m.code} className="w-sc-method" data-on={method === m.code || undefined}>
                <input type="radio" name="method" value={m.code} checked={method === m.code} onChange={() => setMethod(m.code)} />
                <span><strong>{m.label}</strong>{q?.cheapest && <em className="w-sc-badge">{t.cheapest}</em>}<br />
                  <small className="w-muted">{t.fee} {q ? money(q.fee, lang) : Number(m.percent) ? `${m.percent}%` : money(m.fee, lang)}{q ? ` · ${t.total} ${money(q.total, lang)}` : ''}</small></span>
              </label>
            );
          })}
        </div>
        <div className="w-sc-voucher">
          <input aria-label={t.voucher} placeholder={t.voucher} value={code} onChange={(e) => setCode(e.target.value.toUpperCase())} />
          <button type="button" className="w-btn w-btn-ghost" disabled={!code.trim()} onClick={() => setPromo(code.trim())}>{t.apply}</button>
        </div>
        {promo && quote?.promoError && <p className="w-error">{quote.promoError}</p>}
        {promo && quote?.voucher && <p className="w-muted">{t.voucherNote}</p>}
        <h2 style={{ marginTop: 20 }}>{t.costs}</h2>
        {quote && (
          <dl className="w-sc-costs">
            <dt>{t.rent}</dt><dd>{money(quote.rent, lang)}</dd>
            {Number(quote.discount) > 0 && <><dt>{t.discount}</dt><dd>−{money(quote.discount, lang)}</dd></>}
            <dt>{t.tax}</dt><dd>{money(quote.tax, lang)}</dd>
            <dt>{t.service}</dt><dd>{method ? money(quote.serviceFee, lang) : '—'}</dd>
            <dt><strong>{t.total}</strong></dt><dd><strong>{money(quote.total, lang)}</strong></dd>
          </dl>
        )}
        <label className="w-sc-check"><input type="checkbox" checked={agree} onChange={(e) => setAgree(e.target.checked)} /><span>{terms[lang] ?? terms.id}</span></label>
        <label className="w-sc-check"><input type="checkbox" checked={optIn} onChange={(e) => setOptIn(e.target.checked)} /><span>{t.consent}</span></label>
        {error && <p className="w-error" role="alert">{error}</p>}
        <button className="w-btn" style={{ width: '100%' }} disabled={busy || !valid}>{busy ? '…' : `${t.pay}${quote && method ? ` · ${money(quote.total, lang)}` : ''}`}</button>
        {!method && <p className="w-muted">{t.pick}</p>}
      </div>
    </form>
  );
}
