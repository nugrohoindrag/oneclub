'use client';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { useRouter } from 'next/navigation';
import type { Lang } from '../../../lib';
import { StaySearchBar, type StaySearchValue } from '../../../../components/mgcc/stay-search';
import {
  AMENITY_GROUP, METHOD, UNIT, VIEW, addDays, api, cancelPolicy, dayLabel, money, payPolicy, readCart, readGuest, taxLabel, typeName, writeCart, writeGuest,
  type BookingView, type Cart, type CartItem, type CartQuote, type Rate, type Search, type SearchType, type StayPage,
} from './shared';

/*
 * Booking engine of the bungalows (docs/requirement-booking-hotel-mgcc.md
 * §3–§4): 1 search → 2 room types with their rate plans → 3 cart → 4 add-ons
 * → 5 guest data, terms → 6 summary and payment. Every step has its URL
 * (FR-H08); the prices come from the API (one quote function, FR-H17); the
 * cart stays in this browser and nothing is held before "Bayar Sekarang"
 * (FR-H21, FR-H35). Layout of a hotel booking engine, look of the website.
 */

export interface EngineQuery { checkin?: string; checkout?: string; adults?: string; children?: string; promo?: string; step?: string; type?: string }

interface GuestForm {
  title: string; first: string; last: string; phone: string; email: string; nationality: string; eta: string; requests: string; terms: boolean;
  consent: boolean; method: string; occupants: Record<string, string>;
}

const emptyGuest = (method: string): GuestForm => ({ title: 'mr', first: '', last: '', phone: '+62', email: '', nationality: '', eta: '', requests: '',
  terms: false, consent: false, method, occupants: {} });

export function BungalowEngine({ lang, propertyId, page, query }: { lang: Lang; propertyId: string; page: StayPage; query: EngineQuery }) {
  const id = lang === 'id';
  const router = useRouter();
  const checkin = query.checkin ?? '';
  const checkout = query.checkout ?? '';
  const adults = Math.max(Number(query.adults || 2), 1);
  const children = Math.max(Number(query.children || 0), 0);
  const promo = (query.promo ?? '').toUpperCase();
  const step = query.step ?? '';
  const [search, setSearch] = useState<Search | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const [cart, setCartState] = useState<Cart | null>(null);
  const [detail, setDetail] = useState<SearchType | null>(null);
  const [open, setOpen] = useState<string>('');
  const [mode, setMode] = useState<'type' | 'package'>('type');
  const [sort, setSort] = useState<'order' | 'price'>('order');
  const [sheet, setSheet] = useState(false);
  const [waitlist, setWaitlist] = useState<SearchType | null>(null);
  const [taken, setTaken] = useState<Record<string, string>>({});

  const url = useCallback((next: Partial<EngineQuery>) => {
    const q = new URLSearchParams();
    const v = { checkin, checkout, adults: String(adults), children: String(children), promo, step, ...next };
    for (const [k, x] of Object.entries(v)) if (x) q.set(k, String(x));
    return `/${lang}/book/bungalow?${q}`;
  }, [lang, checkin, checkout, adults, children, promo, step]);

  // the cart of this browser, for the dates searched
  useEffect(() => {
    const c = readCart();
    setCartState(c && (!checkin || (c.checkin === checkin && c.checkout === checkout)) ? c : null);
  }, [checkin, checkout]);
  const setCart = (c: Cart | null) => {
    writeCart(c);
    setCartState(c && c.items.length ? c : null);
  };

  const load = useCallback(async () => {
    if (!checkin || !checkout) return;
    setLoading(true);
    setError('');
    try {
      const q = new URLSearchParams({ propertyId, checkin, checkout, adults: String(adults), children: String(children) });
      if (promo) q.set('promo', promo);
      setSearch(await api<Search>(`/api/v1/public/stay/search?${q}`));
    } catch (e) {
      setSearch(null);
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [propertyId, checkin, checkout, adults, children, promo]);
  useEffect(() => { void load(); }, [load]);

  const goSearch = (v: StaySearchValue) => router.push(url({ checkin: v.checkin, checkout: v.checkout, adults: String(v.adults), children: String(v.children),
    promo: v.promo.trim().toUpperCase(), step: '' }));

  const countOf = (typeId: string) => (cart?.items ?? []).filter((i) => i.bungalowTypeId === typeId).length;
  const add = (t: SearchType, r: Rate) => {
    const base: Cart = cart && cart.checkin === checkin && cart.checkout === checkout ? cart : { checkin, checkout, promo, items: [] };
    if (countOf(t.id) >= t.available) return;
    const item: CartItem = { key: `${t.id}-${Date.now()}`, bungalowTypeId: t.id, typeName: typeName(t, lang), ratePlan: r.code, ratePlanName: r.name, kind: r.kind,
      adults: Math.min(adults, t.maxAdults), children: Math.min(children, t.maxChildren), maxAdults: t.maxAdults, maxChildren: t.maxChildren, total: r.total,
      includesBreakfast: r.includesBreakfast, addons: [] };
    setCart({ ...base, promo, items: [...base.items, item] });
  };
  const removeOne = (typeId: string) => {
    if (!cart) return;
    const idx = cart.items.map((i) => i.bungalowTypeId).lastIndexOf(typeId);
    if (idx >= 0) setCart({ ...cart, items: cart.items.filter((_, i) => i !== idx) });
  };
  const update = (key: string, patch: Partial<CartItem>) => cart && setCart({ ...cart, items: cart.items.map((i) => (i.key === key ? { ...i, ...patch } : i)) });
  const remove = (key: string) => cart && setCart({ ...cart, items: cart.items.filter((i) => i.key !== key) });
  const subtotal = (cart?.items ?? []).reduce((s, i) => s + Number(i.total), 0);

  const types = useMemo(() => {
    const list = [...(search?.types ?? [])];
    if (sort === 'price') list.sort((a, b) => Number(a.fromPerNight ?? 1e15) - Number(b.fromPerNight ?? 1e15));
    return list;
  }, [search, sort]);

  if (!checkin || !checkout) {
    return (
      <div className="w-bk">
        <StaySearchBar lang={lang} maxDate={page.maxDate} onSearch={goSearch} initial={{ adults, children, promo }} />
        <RoomTypeList lang={lang} page={page} onDetail={(t) => setDetail({ ...t, available: t.units, full: false, lowAvailability: false, fitsGuests: true, rates: [], packages: [] })} />
        {detail && <DetailModal lang={lang} type={detail} page={page} onClose={() => setDetail(null)} />}
      </div>
    );
  }
  const cartPanel = (
    <CartPanel lang={lang} cart={cart} subtotal={subtotal} nights={search?.nights ?? 0} taken={taken} onUpdate={update} onRemove={remove}
      onNext={() => { setSheet(false); router.push(url({ step: 'addons' })); }} step={step} />
  );
  return (
    <div className="w-bk">
      <StaySearchBar lang={lang} maxDate={page.maxDate} onSearch={goSearch} initial={{ checkin, checkout, adults, children, promo }} />
      <ol className="w-bk-steps" aria-label={id ? 'Langkah' : 'Steps'}>
        {[['', id ? 'Pilih bungalow' : 'Choose bungalows'], ['addons', id ? 'Add-on' : 'Add-ons'], ['details', id ? 'Data tamu & bayar' : 'Guest & payment']]
          .map(([k, l], i) => <li key={k} aria-current={step === k ? 'step' : undefined}><span>{i + 1}</span>{l}</li>)}
      </ol>
      {step === '' && (
        <div className="w-bk-layout">
          <div className="w-bk-main">
            {search?.promoError && <p className="w-bk-note" role="alert">{id ? 'Kode promo tidak berlaku' : 'Promo code not valid'}: {search.promoError}</p>}
            {search?.promoCode && !search.promoError && <p className="w-bk-note w-bk-ok">{id ? 'Kode promo' : 'Promo code'} <strong>{search.promoCode}</strong> {id ? 'diterapkan pada harga di bawah.' : 'is applied to the prices below.'}</p>}
            <div className="w-bk-tools">
              <div className="w-sc-seg" role="group" aria-label={id ? 'Tampilan' : 'View'}>
                <button type="button" aria-pressed={mode === 'type'} onClick={() => setMode('type')}>{id ? 'Per tipe' : 'By room type'}</button>
                <button type="button" aria-pressed={mode === 'package'} onClick={() => setMode('package')}>{id ? 'Paket' : 'Packages'}</button>
              </div>
              <label className="w-bk-sort">{id ? 'Urutkan' : 'Sort'}
                <select value={sort} onChange={(e) => setSort(e.target.value as 'order' | 'price')}>
                  <option value="order">{id ? 'Rekomendasi' : 'Recommended'}</option>
                  <option value="price">{id ? 'Harga termurah' : 'Lowest price'}</option>
                </select>
              </label>
            </div>
            {loading && <p>{id ? 'Mencari bungalow…' : 'Searching…'}</p>}
            {error && (
              <div className="w-card" role="alert">
                <p className="w-error">{error}</p>
                <button type="button" className="w-btn" onClick={() => void load()}>{id ? 'Muat ulang' : 'Reload'}</button>
              </div>
            )}
            {search && types.length === 0 && <p>{id ? 'Belum ada bungalow yang bisa dipesan online.' : 'No bungalow can be booked online yet.'}</p>}
            {search && types.every((t) => t.full || t.closedReason) && types.length > 0 && (
              <p className="w-bk-note">{id ? 'Tidak ada bungalow tersedia untuk tanggal ini — coba tanggal lain atau masuk waitlist.'
                : 'No bungalow is available on these dates — try other dates or join the waitlist.'}</p>
            )}
            {search && types.map((t) => (
              <TypeCard key={t.id} lang={lang} t={t} mode={mode} nights={search.nights} count={countOf(t.id)} open={open} setOpen={setOpen}
                onDetail={() => setDetail(t)} onAdd={(r) => add(t, r)} onMinus={() => removeOne(t.id)} onWaitlist={() => setWaitlist(t)} />
            ))}
          </div>
          <aside className="w-bk-side" aria-label={id ? 'Ringkasan reservasi' : 'Reservation summary'}>{cartPanel}</aside>
          {cart && (
            <button type="button" className="w-bk-sticky" onClick={() => setSheet(true)}>
              {id ? 'Reservasi Anda' : 'Your reservation'} ({cart.items.length}) · {money(subtotal, lang)}
            </button>
          )}
          {sheet && (
            <div className="w-bk-sheet" role="dialog" aria-modal="true" aria-label={id ? 'Reservasi Anda' : 'Your reservation'}>
              <div className="w-bk-sheet-body">
                <button type="button" className="w-bk-close" onClick={() => setSheet(false)} aria-label={id ? 'Tutup' : 'Close'}>×</button>
                {cartPanel}
              </div>
            </div>
          )}
        </div>
      )}
      {step === 'addons' && (
        cart ? <AddonsStep lang={lang} page={page} cart={cart} nights={search?.nights ?? 0} onUpdate={update} onBack={() => router.push(url({ step: '' }))}
          onNext={() => router.push(url({ step: 'details' }))} />
          : <EmptyCart lang={lang} onBack={() => router.push(url({ step: '' }))} />
      )}
      {step === 'details' && (
        cart ? <DetailsStep lang={lang} page={page} propertyId={propertyId} cart={cart} onBack={() => router.push(url({ step: 'addons' }))}
          onTaken={(t) => { setTaken(t); router.push(url({ step: '' })); }} />
          : <EmptyCart lang={lang} onBack={() => router.push(url({ step: '' }))} />
      )}
      {detail && <DetailModal lang={lang} type={detail} page={page} onClose={() => setDetail(null)} />}
      {waitlist && <WaitlistModal lang={lang} propertyId={propertyId} type={waitlist} checkin={checkin} checkout={checkout} adults={adults} children={children}
        onClose={() => setWaitlist(null)} />}
    </div>
  );
}

function EmptyCart({ lang, onBack }: { lang: Lang; onBack: () => void }) {
  const id = lang === 'id';
  return (
    <div className="w-card">
      <p>{id ? 'Keranjang Anda kosong. Pilih bungalow dulu.' : 'Your cart is empty. Choose a bungalow first.'}</p>
      <button type="button" className="w-btn" onClick={onBack}>{id ? 'Pilih bungalow' : 'Choose bungalows'}</button>
    </div>
  );
}

/** The room types before a search (the bungalow page itself). */
function RoomTypeList({ lang, page, onDetail }: { lang: Lang; page: StayPage; onDetail: (t: StayPage['types'][number]) => void }) {
  const id = lang === 'id';
  return (
    <div className="w-bk-types">
      {page.types.map((t) => (
        <article key={t.id} className="w-card w-bk-type">
          {t.photos[0] ? <img src={t.photos[0]} alt={typeName(t, lang)} className="w-bk-photo" /> : <div className="w-bk-photo w-bk-nophoto" aria-hidden="true">🏡</div>}
          <div>
            <h3>{typeName(t, lang)}</h3>
            <p className="w-muted">{facts(t, lang)}</p>
            <button type="button" className="w-bk-link" onClick={() => onDetail(t)}>{id ? 'Lihat Detail' : 'View details'}</button>
          </div>
        </article>
      ))}
    </div>
  );
}

function facts(t: { bedrooms: number; maxAdults: number; maxChildren: number; sizeSqm?: string | null; bedConfiguration?: string | null; views: string[] }, lang: Lang) {
  const id = lang === 'id';
  const out: string[] = [];
  if (t.sizeSqm) out.push(`${Number(t.sizeSqm)} m²`);
  out.push(`${t.bedrooms} ${id ? 'kamar tidur' : t.bedrooms > 1 ? 'bedrooms' : 'bedroom'}`);
  out.push(`${id ? 'maks.' : 'up to'} ${t.maxAdults} ${id ? 'dewasa' : 'adults'}${t.maxChildren ? ` + ${t.maxChildren} ${id ? 'anak' : 'children'}` : ''}`);
  if (t.bedConfiguration) out.push(t.bedConfiguration);
  if (t.views.length) out.push(`view ${t.views.map((v) => VIEW[v]?.[id ? 0 : 1] ?? v).join(' / ')}`);
  return out.join(' · ');
}

function TypeCard({ lang, t, mode, nights, count, open, setOpen, onDetail, onAdd, onMinus, onWaitlist }: {
  lang: Lang; t: SearchType; mode: 'type' | 'package'; nights: number; count: number; open: string; setOpen: (k: string) => void; onDetail: () => void;
  onAdd: (r: Rate) => void; onMinus: () => void; onWaitlist: () => void;
}) {
  const id = lang === 'id';
  const rates = mode === 'package' ? t.packages : t.rates;
  const blocked = t.full || !!t.closedReason || !t.fitsGuests;
  if (mode === 'package' && rates.length === 0) return null;
  return (
    <article className="w-card w-bk-type" data-full={t.full || undefined}>
      {t.photos[0] ? <img src={t.photos[0]} alt={typeName(t, lang)} className="w-bk-photo" /> : <div className="w-bk-photo w-bk-nophoto" aria-hidden="true">🏡</div>}
      <div>
        <div className="w-bk-type-head">
          <h3>{typeName(t, lang)}</h3>
          {t.full && <span className="w-bk-badge" data-tone="red">{id ? 'Penuh' : 'Sold out'}</span>}
          {t.lowAvailability && <span className="w-bk-badge" data-tone="gold">{id ? `Sisa ${t.available} bungalow` : `Only ${t.available} left`}</span>}
        </div>
        <p className="w-muted">{facts(t, lang)}</p>
        {t.fromPerNight && <p>{id ? 'Mulai dari' : 'From'} <strong>{money(t.fromPerNight, lang)}</strong> / {id ? 'malam' : 'night'}</p>}
        {t.capacityNote && <p className="w-bk-note">{id ? t.capacityNote : `At most ${t.maxAdults} adults and ${t.maxChildren} children per bungalow — add a bungalow or choose another type`}</p>}
        {t.closedReason && <p className="w-bk-note">{t.closedReason}</p>}
        <div className="w-bk-row-actions">
          <button type="button" className="w-bk-link" onClick={onDetail}>{id ? 'Lihat Detail' : 'View details'}</button>
          {t.full && <button type="button" className="w-btn w-btn-ghost" onClick={onWaitlist}>{id ? 'Masuk Waitlist' : 'Join the waitlist'}</button>}
        </div>
      </div>
      {!blocked && rates.length > 0 && (
        <div className="w-bk-rates">
          {rates.map((r) => {
            const k = `${t.id}:${r.code}`;
            return (
              <div key={r.code} className="w-bk-rate">
                <div>
                  <strong>{r.name}</strong>{r.eligibility === 'member' && <span className="w-bk-badge" data-tone="gold">Member</span>}
                  <div className="w-muted w-bk-small">
                    {r.includesBreakfast ? (id ? '☕ Termasuk sarapan' : '☕ Breakfast included') : (id ? 'Tanpa sarapan' : 'Room only')}
                    {' · '}{taxLabel(r.taxIncluded, lang)}{' · '}{payPolicy(r, lang)}
                  </div>
                  <div className="w-bk-links">
                    <button type="button" className="w-bk-link" aria-expanded={open === `${k}:c`} onClick={() => setOpen(open === `${k}:c` ? '' : `${k}:c`)}>
                      {id ? 'Kebijakan Pembatalan' : 'Cancellation policy'}</button>
                    <button type="button" className="w-bk-link" aria-expanded={open === `${k}:i`} onClick={() => setOpen(open === `${k}:i` ? '' : `${k}:i`)}>
                      {id ? 'Inklusi & Rincian Harga' : 'Inclusions & price details'}</button>
                  </div>
                  {open === `${k}:c` && <p className="w-bk-small">{cancelPolicy(r, lang)}</p>}
                  {open === `${k}:i` && (
                    <div className="w-bk-small">
                      {r.inclusions.length > 0 && <p>{id ? 'Termasuk' : 'Includes'}: {r.inclusions.map((x) => x.name).join(', ')}</p>}
                      <table className="w-bk-nights"><tbody>
                        {r.nights.map((n) => (
                          <tr key={n.date}><td>{dayLabel(n.date, lang, { weekday: 'short', day: 'numeric', month: 'short' })}{n.season ? ` · ${n.season}` : ''}</td>
                            <td>{money(n.price, lang)}</td></tr>
                        ))}
                      </tbody></table>
                    </div>
                  )}
                </div>
                <div className="w-bk-price">
                  {r.savePercent > 0 && <><s>{money(r.listTotal, lang)}</s> <span className="w-bk-badge" data-tone="green">{id ? 'Hemat' : 'Save'} {r.savePercent}%</span></>}
                  <strong>{money(r.total, lang)}</strong>
                  <small>{nights} {id ? 'malam' : nights === 1 ? 'night' : 'nights'} · {money(r.averagePerNight, lang)} / {id ? 'malam' : 'night'}</small>
                  {r.promotionName && <small>{r.promotionName}</small>}
                  <div className="w-bk-qty">
                    {count > 0 && <button type="button" aria-label={id ? 'Kurangi' : 'Remove one'} onClick={onMinus}>−</button>}
                    {count > 0 && <output>{count}</output>}
                    <button type="button" className="w-btn" disabled={count >= t.available} onClick={() => onAdd(r)}>{count > 0 ? '+' : (id ? 'Pilih' : 'Select')}</button>
                  </div>
                </div>
              </div>
            );
          })}
        </div>
      )}
    </article>
  );
}

function CartPanel({ lang, cart, subtotal, nights, taken, onUpdate, onRemove, onNext, step }: {
  lang: Lang; cart: Cart | null; subtotal: number; nights: number; taken: Record<string, string>; onUpdate: (k: string, p: Partial<CartItem>) => void;
  onRemove: (k: string) => void; onNext: () => void; step: string;
}) {
  const id = lang === 'id';
  if (!cart || cart.items.length === 0) {
    return <div className="w-card w-bk-cart"><h3>{id ? 'Ringkasan Reservasi' : 'Reservation summary'}</h3><p className="w-muted">{id ? 'Belum ada bungalow dipilih.' : 'No bungalow chosen yet.'}</p></div>;
  }
  return (
    <div className="w-card w-bk-cart">
      <h3>{id ? 'Ringkasan Reservasi' : 'Reservation summary'}</h3>
      <p className="w-muted w-bk-small">{dayLabel(cart.checkin, lang)} – {dayLabel(cart.checkout, lang)} · {nights} {id ? 'malam' : 'nights'}</p>
      {cart.items.map((i, n) => (
        <div key={i.key} className="w-bk-cart-line" data-taken={taken[String(n)] ? true : undefined}>
          <div>
            <strong>{i.typeName}</strong> <span className="w-muted w-bk-small">· {i.ratePlanName}</span>
            {taken[String(n)] && <p className="w-error w-bk-small">{taken[String(n)]}</p>}
            <div className="w-bk-guests">
              <label>{id ? 'Dewasa' : 'Adults'}
                <select value={i.adults} onChange={(e) => onUpdate(i.key, { adults: Number(e.target.value) })}>
                  {Array.from({ length: i.maxAdults }, (_, k) => k + 1).map((k) => <option key={k}>{k}</option>)}
                </select>
              </label>
              <label>{id ? 'Anak' : 'Children'}
                <select value={i.children} onChange={(e) => onUpdate(i.key, { children: Number(e.target.value) })}>
                  {Array.from({ length: i.maxChildren + 1 }, (_, k) => k).map((k) => <option key={k}>{k}</option>)}
                </select>
              </label>
            </div>
          </div>
          <div className="w-bk-cart-price">{money(i.total, lang)}
            <button type="button" className="w-sc-remove" aria-label={id ? 'Hapus' : 'Remove'} onClick={() => onRemove(i.key)}>×</button>
          </div>
        </div>
      ))}
      <dl className="w-sc-costs">
        <dt><strong>Subtotal</strong></dt><dd><strong>{money(subtotal, lang)}</strong></dd>
      </dl>
      <p className="w-muted w-bk-small">{id ? 'Bungalow belum ditahan sampai Anda menekan Bayar Sekarang. Jumlah tamu yang diubah dihitung ulang di ringkasan biaya.'
        : 'The bungalows are not held until you press Pay Now. A change of guests is priced again in the cost summary.'}</p>
      {step === '' && <button type="button" className="w-btn" onClick={onNext}>{id ? 'Lanjutkan' : 'Continue'}</button>}
    </div>
  );
}

function AddonsStep({ lang, page, cart, nights, onUpdate, onBack, onNext }: {
  lang: Lang; page: StayPage; cart: Cart; nights: number; onUpdate: (k: string, p: Partial<CartItem>) => void; onBack: () => void; onNext: () => void;
}) {
  const id = lang === 'id';
  return (
    <div className="w-bk-addons">
      <h2>{id ? 'Add-on & paket' : 'Add-ons'}</h2>
      <p className="w-muted">{id ? 'Pilih per bungalow. Harga mengikuti satuannya (per malam, per orang …) dan tidak mengubah harga kamar.'
        : 'Choose per bungalow. Prices follow their unit (per night, per person …) and do not change the room price.'}</p>
      {cart.items.map((i, n) => {
        const list = page.addons.filter((a) => !(i.includesBreakfast && a.category === 'breakfast'));
        return (
          <section key={i.key} className="w-card">
            <h3>{n + 1}. {i.typeName} <span className="w-muted w-bk-small">· {i.ratePlanName}</span></h3>
            <label>{id ? 'Nama tamu yang menginap (bila berbeda dari pemesan)' : 'Name of the guest staying (if not the booker)'}
              <input value={i.occupantName ?? ''} maxLength={120} onChange={(e) => onUpdate(i.key, { occupantName: e.target.value })} />
            </label>
            {list.length === 0 && <p className="w-muted">{id ? 'Tidak ada add-on.' : 'No add-ons.'}</p>}
            {list.map((a) => {
              const q = i.addons.find((x) => x.addonId === a.id)?.quantity ?? 0;
              const set = (v: number) => onUpdate(i.key, { addons: [...i.addons.filter((x) => x.addonId !== a.id), ...(v > 0 ? [{ addonId: a.id, quantity: v }] : [])] });
              return (
                <div key={a.id} className="w-bk-addon">
                  <div><strong>{a.name}</strong><div className="w-muted w-bk-small">{money(a.price, lang)} {UNIT[a.unit]?.[id ? 0 : 1] ?? a.unit}{a.description ? ` · ${a.description}` : ''}</div></div>
                  <div className="w-bk-qty">
                    <button type="button" aria-label={`${a.name} −`} disabled={q <= 0} onClick={() => set(q - 1)}>−</button>
                    <output>{q}</output>
                    <button type="button" aria-label={`${a.name} +`} disabled={q >= 10} onClick={() => set(q + 1)}>+</button>
                  </div>
                </div>
              );
            })}
          </section>
        );
      })}
      <p className="w-muted w-bk-small">{nights} {id ? 'malam' : 'nights'} · {id ? 'Total add-on terlihat di ringkasan biaya.' : 'The add-on totals show in the cost summary.'}</p>
      <div className="w-bk-nav">
        <button type="button" className="w-btn w-btn-ghost" onClick={onBack}>{id ? 'Kembali' : 'Back'}</button>
        <button type="button" className="w-btn" onClick={onNext}>{id ? 'Lanjutkan' : 'Continue'}</button>
      </div>
    </div>
  );
}

function DetailsStep({ lang, page, propertyId, cart, onBack, onTaken }: {
  lang: Lang; page: StayPage; propertyId: string; cart: Cart; onBack: () => void;
  onTaken: (t: Record<string, string>) => void;
}) {
  const id = lang === 'id';
  const [g, setG] = useState<GuestForm>(() => readGuest<GuestForm>() ?? emptyGuest(page.methods[0] ?? 'qris'));
  const [quote, setQuote] = useState<CartQuote | null>(null);
  const [qErr, setQErr] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [modal, setModal] = useState<'terms' | 'cancel' | null>(null);
  useEffect(() => writeGuest(g), [g]);
  const body = useMemo(() => ({ propertyId, arrivalDate: cart.checkin, departureDate: cart.checkout, promoCode: cart.promo || undefined,
    items: cart.items.map((i) => ({ bungalowTypeId: i.bungalowTypeId, ...(i.kind === 'package' ? { stayPackage: i.ratePlan } : { ratePlan: i.ratePlan }), adults: i.adults,
      children: i.children, addons: i.addons, occupantName: i.occupantName || undefined })) }), [propertyId, cart]);
  useEffect(() => {
    let live = true;
    setQErr('');
    api<CartQuote>('/api/v1/public/stays:quote', { method: 'POST', body: JSON.stringify(body) })
      .then((q) => live && setQuote(q), (e) => live && setQErr((e as Error).message));
    return () => { live = false; };
  }, [body]);
  const deposit = Number(quote?.depositNow ?? 0);
  const phoneOk = /^\+?[0-9 ()-]{8,}$/.test(g.phone.trim()) && g.phone.replace(/\D/g, '').length >= 9;
  const emailOk = /^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(g.email.trim());
  const ready = !!quote?.ok && g.first.trim() && g.last.trim() && phoneOk && emailOk && g.terms && (deposit <= 0 || !!g.method);
  const pay = async () => {
    setBusy(true);
    setError('');
    try {
      const v = await api<BookingView>('/api/v1/public/stay-bookings', { method: 'POST', body: JSON.stringify({ ...body,
        guest: { name: `${g.first.trim()} ${g.last.trim()}`, phone: g.phone.trim(), email: g.email.trim() }, bookerTitle: g.title, nationality: g.nationality || undefined,
        expectedArrival: g.eta || undefined, specialRequests: g.requests.trim() || undefined, acceptTerms: g.terms, marketingConsent: g.consent,
        payMethod: deposit > 0 ? g.method : undefined }) });
      try { sessionStorage.setItem(`oneclub.bungalow.paid-cart.${v.token}`, JSON.stringify(cart)); } catch { /* ignore */ }
      window.location.href = v.status === 'awaiting_payment' ? `/${lang}/book/bungalow/pay/${v.token}` : `/${lang}/book/bungalow/booking/${v.token}`;
    } catch (e) {
      const err = e as Error & { status?: number; errors?: { field: string; message: string }[] };
      // a bungalow sold out meanwhile: back to the cart with that bungalow marked (FR-H35, FR-H99)
      const taken: Record<string, string> = {};
      for (const f of err.errors ?? []) {
        const m = /^items\[(\d+)\]$/.exec(f.field);
        if (m) taken[m[1]] = f.message;
      }
      if (err.status === 409 && Object.keys(taken).length) {
        onTaken(taken);
        return;
      }
      setError(err.message);
      setBusy(false);
    }
  };
  const set = (k: keyof GuestForm, v: string | boolean) => setG({ ...g, [k]: v });
  return (
    <div className="w-bk-layout">
      <div className="w-bk-main">
        <section className="w-card">
          <h2>{id ? 'Data Pemesan' : 'Booker'}</h2>
          <p className="w-muted w-bk-small">{id ? 'Tidak perlu akun. Konfirmasi tampil di layar dan bisa diunduh.' : 'No account needed. The confirmation shows on screen and can be downloaded.'}</p>
          <div className="w-bk-form">
            <label>{id ? 'Sapaan' : 'Title'}
              <select value={g.title} onChange={(e) => set('title', e.target.value)}>
                <option value="mr">{id ? 'Bapak' : 'Mr'}</option><option value="mrs">{id ? 'Ibu' : 'Mrs'}</option><option value="ms">{id ? 'Nona' : 'Ms'}</option>
              </select>
            </label>
            <label>{id ? 'Nama depan *' : 'First name *'}<input value={g.first} onChange={(e) => set('first', e.target.value)} required autoComplete="given-name" /></label>
            <label>{id ? 'Nama belakang *' : 'Last name *'}<input value={g.last} onChange={(e) => set('last', e.target.value)} required autoComplete="family-name" /></label>
            <label>{id ? 'Nomor HP *' : 'Mobile *'}<input value={g.phone} onChange={(e) => set('phone', e.target.value)} inputMode="tel" autoComplete="tel" required
              aria-invalid={!!g.phone && g.phone !== '+62' && !phoneOk} /></label>
            <label>E-mail *<input type="email" value={g.email} onChange={(e) => set('email', e.target.value)} autoComplete="email" required
              aria-invalid={!!g.email && !emailOk} /></label>
            <label>{id ? 'Kewarganegaraan' : 'Nationality'}<input value={g.nationality} onChange={(e) => set('nationality', e.target.value)} placeholder="Indonesia" /></label>
            <label>{id ? 'Estimasi jam tiba' : 'Expected arrival'}<input type="time" value={g.eta} onChange={(e) => set('eta', e.target.value)} /></label>
          </div>
          <label>{id ? 'Permintaan khusus' : 'Special requests'}
            <textarea rows={2} maxLength={255} value={g.requests} onChange={(e) => set('requests', e.target.value)} />
            <small className="w-muted">{id ? 'Tergantung ketersediaan, tidak dikenai biaya kecuali dikonfirmasi.' : 'Subject to availability, free unless confirmed.'} {g.requests.length}/255</small>
          </label>
          <p className="w-bk-small"><a href={`/${lang}/my-stay`}>{id ? 'Member MGCC? Masuk ke Member App untuk harga member dan member charge →' : 'MGCC member? Sign in to the Member App for member rates and member charge →'}</a></p>
        </section>
        <section className="w-card">
          <h2>{id ? 'Metode pembayaran' : 'Payment method'}</h2>
          {deposit > 0 ? (
            <div className="w-sc-methods">
              {page.methods.map((m) => (
                <label key={m} className="w-sc-method" data-on={g.method === m || undefined}>
                  <input type="radio" name="method" checked={g.method === m} onChange={() => set('method', m)} />
                  <span><strong>{METHOD[m]?.[id ? 0 : 1] ?? m}</strong></span>
                </label>
              ))}
            </div>
          ) : <p>{id ? 'Tarif yang dipilih dibayar di hotel: reservasi langsung terkonfirmasi tanpa pembayaran online.' : 'The chosen rates are paid at the hotel: the reservation is confirmed without an online payment.'}</p>}
          <p className="w-muted w-bk-small">{id ? `Bungalow ditahan ${page.holdMinutes} menit setelah Anda menekan Bayar Sekarang.` : `The bungalows are held ${page.holdMinutes} minutes after you press Pay Now.`}</p>
        </section>
        <section className="w-card">
          <label className="w-sc-check"><input type="checkbox" checked={g.terms} onChange={(e) => set('terms', e.target.checked)} />
            <span>{id ? 'Saya menyetujui ' : 'I accept the '}<button type="button" className="w-bk-link" onClick={() => setModal('terms')}>{id ? 'Syarat & Ketentuan' : 'Terms & Conditions'}</button>
              {id ? ' dan ' : ' and the '}<button type="button" className="w-bk-link" onClick={() => setModal('cancel')}>{id ? 'Kebijakan Pembatalan' : 'Cancellation Policy'}</button>{id ? ' tarif yang dipilih.' : ' of the chosen rates.'}</span>
          </label>
          <label className="w-sc-check"><input type="checkbox" checked={g.consent} onChange={(e) => set('consent', e.target.checked)} />
            <span>{id ? 'Kirimi saya berita dan penawaran (opsional).' : 'Send me news and offers (optional).'}</span></label>
          {error && <p className="w-error" role="alert">{error}</p>}
          <div className="w-bk-nav">
            <button type="button" className="w-btn w-btn-ghost" onClick={onBack}>{id ? 'Kembali' : 'Back'}</button>
            <button type="button" className="w-btn" disabled={!ready || busy} onClick={() => void pay()}>
              {busy ? '…' : deposit > 0 ? `${id ? 'Bayar Sekarang' : 'Pay Now'} ${money(deposit, lang)}` : (id ? 'Konfirmasi Reservasi' : 'Confirm reservation')}</button>
          </div>
        </section>
      </div>
      <aside className="w-bk-side">
        <CostSummary lang={lang} quote={quote} error={qErr} cart={cart} />
      </aside>
      {modal && (
        <div className="w-bk-sheet" role="dialog" aria-modal="true" onClick={() => setModal(null)}>
          <div className="w-bk-sheet-body w-bk-modal" onClick={(e) => e.stopPropagation()}>
            <button type="button" className="w-bk-close" onClick={() => setModal(null)} aria-label={id ? 'Tutup' : 'Close'}>×</button>
            {modal === 'terms' ? (
              <>
                <h3>{id ? 'Syarat & Ketentuan' : 'Terms & Conditions'}</h3>
                <p style={{ whiteSpace: 'pre-wrap' }}>{id ? page.terms : page.termsEn || page.terms}</p>
                <h4>{id ? 'Aturan rumah' : 'House rules'}</h4>
                <p style={{ whiteSpace: 'pre-wrap' }}>{id ? page.houseRules : page.houseRulesEn || page.houseRules}</p>
                {page.childPolicy && <><h4>{id ? 'Kebijakan anak' : 'Children'}</h4><p>{page.childPolicy}</p></>}
                <p>Check-in {page.checkInTime} · Check-out {page.checkOutTime}</p>
              </>
            ) : (
              <>
                <h3>{id ? 'Kebijakan Pembatalan' : 'Cancellation Policy'}</h3>
                {(quote?.items ?? []).map((l) => <p key={l.index}><strong>{l.typeName} · {l.ratePlanName}</strong><br />{cancelPolicy(l, lang)}</p>)}
              </>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

/** Ringkasan Biaya (FR-H30): per bungalow, add-ons, promotion, tax & service, total, deposit now, rest at the hotel. */
function CostSummary({ lang, quote, error, cart }: { lang: Lang; quote: CartQuote | null; error: string; cart: Cart }) {
  const id = lang === 'id';
  if (error) return <div className="w-card w-bk-cart" role="alert"><p className="w-error">{error}</p></div>;
  if (!quote) return <div className="w-card w-bk-cart"><p>{id ? 'Menghitung…' : 'Pricing…'}</p></div>;
  return (
    <div className="w-card w-bk-cart">
      <h3>{id ? 'Ringkasan Biaya' : 'Cost summary'}</h3>
      <p className="w-muted w-bk-small">{dayLabel(cart.checkin, lang)} – {dayLabel(cart.checkout, lang)} · {quote.nights} {id ? 'malam' : 'nights'}</p>
      {quote.items.map((l) => (
        <div key={l.index} className="w-bk-cost-line">
          <strong>{l.typeName}</strong> <span className="w-muted w-bk-small">{l.ratePlanName}{l.occupantName ? ` · ${l.occupantName}` : ''}</span>
          {l.error ? <p className="w-error w-bk-small">{l.error}</p> : (
            <dl className="w-sc-costs">
              <dt>{quote.nights} × {id ? 'malam' : 'night'} · {l.adults} {id ? 'dewasa' : 'adults'}{l.children ? ` + ${l.children} ${id ? 'anak' : 'children'}` : ''}</dt>
              <dd>{l.roomList !== l.roomTotal && <s>{money(l.roomList, lang)} </s>}{money(l.roomTotal, lang)}</dd>
              {l.addons.filter((a) => !a.included).map((a) => <div key={a.id} style={{ display: 'contents' }}><dt>+ {a.name} × {a.quantity}</dt><dd>{money(a.total, lang)}</dd></div>)}
            </dl>
          )}
        </div>
      ))}
      <dl className="w-sc-costs">
        {Number(quote.discount) > 0 && <><dt>{id ? 'Diskon promo' : 'Promo discount'}</dt><dd>−{money(quote.discount, lang)}</dd></>}
        <dt>Subtotal</dt><dd>{money(quote.subtotal, lang)}</dd>
        <dt>{id ? 'Pajak & service' : 'Tax & service'}</dt><dd>{money(Number(quote.tax) + Number(quote.service), lang)}</dd>
        <dt><strong>Total</strong></dt><dd><strong>{money(quote.total, lang)}</strong></dd>
        <dt>{id ? 'Dibayar sekarang' : 'Pay now'}</dt><dd><strong>{money(quote.depositNow, lang)}</strong></dd>
        <dt>{id ? 'Sisa dibayar di hotel' : 'Pay at the hotel'}</dt><dd>{money(quote.payAtHotel, lang)}</dd>
      </dl>
      {quote.items.some((l) => l.taxIncluded) && <p className="w-muted w-bk-small">{id ? 'Harga kamar sudah termasuk pajak & service; rincian pajak di atas adalah bagian dari total.' : 'Room prices include tax & service; the tax shown is part of the total.'}</p>}
      {quote.promoError && <p className="w-error w-bk-small">{id ? 'Kode promo tidak berlaku' : 'Promo code not valid'}: {quote.promoError}</p>}
    </div>
  );
}

/** Lihat Detail (FR-H18): gallery, description, size, beds, capacity, view, amenities by group, house rules. */
function DetailModal({ lang, type: t, page, onClose }: { lang: Lang; type: SearchType; page: StayPage; onClose: () => void }) {
  const id = lang === 'id';
  const [photo, setPhoto] = useState(0);
  const groups = Object.entries(t.amenities.reduce<Record<string, string[]>>((m, a) => {
    (m[a.group] ??= []).push(id ? a.label : a.labelEn || a.label);
    return m;
  }, {}));
  return (
    <div className="w-bk-sheet" role="dialog" aria-modal="true" aria-label={typeName(t, lang)} onClick={onClose}>
      <div className="w-bk-sheet-body w-bk-modal w-bk-detail" onClick={(e) => e.stopPropagation()}>
        <button type="button" className="w-bk-close" onClick={onClose} aria-label={id ? 'Tutup' : 'Close'}>×</button>
        <h3>{typeName(t, lang)}</h3>
        {t.photos.length > 0 && (
          <div className="w-bk-gallery">
            <img src={t.photos[photo]} alt={`${typeName(t, lang)} ${photo + 1}`} />
            {t.photos.length > 1 && (
              <div className="w-bk-thumbs">
                {t.photos.map((p, i) => <button key={p} type="button" aria-pressed={i === photo} onClick={() => setPhoto(i)}><img src={p} alt="" /></button>)}
              </div>
            )}
          </div>
        )}
        <p className="w-muted">{facts(t, lang)}</p>
        {(id ? t.description : t.descriptionEn || t.description) && <p>{id ? t.description : t.descriptionEn || t.description}</p>}
        {t.views.length > 1 && <p className="w-bk-small w-muted">{id ? 'View bungalow tergantung ketersediaan saat check-in.' : 'The view of the bungalow depends on availability at check-in.'}</p>}
        {groups.length > 0 && (
          <div className="w-bk-amenities">
            {groups.map(([g, list]) => <div key={g}><strong>{AMENITY_GROUP[g]?.[id ? 0 : 1] ?? g}</strong><ul>{list.map((x) => <li key={x}>✓ {x}</li>)}</ul></div>)}
          </div>
        )}
        {t.facilities.length > 0 && groups.length === 0 && <p>{t.facilities.join(' · ')}</p>}
        <h4>{id ? 'Aturan rumah' : 'House rules'}</h4>
        <p style={{ whiteSpace: 'pre-wrap' }}>{t.houseRules || (id ? page.houseRules : page.houseRulesEn || page.houseRules)}</p>
        <p className="w-bk-small">Check-in {page.checkInTime} · Check-out {page.checkOutTime}</p>
        <p className="w-bk-small"><a href={`/${lang}/bungalow/${t.slug}`}>{id ? 'Halaman tipe ini →' : 'Page of this room type →'}</a></p>
      </div>
    </div>
  );
}

function WaitlistModal({ lang, propertyId, type: t, checkin, checkout, adults, children, onClose }: {
  lang: Lang; propertyId: string; type: SearchType; checkin: string; checkout: string; adults: number; children: number; onClose: () => void;
}) {
  const id = lang === 'id';
  const [v, setV] = useState({ name: '', phone: '+62', email: '' });
  const [done, setDone] = useState('');
  const [error, setError] = useState('');
  const send = async () => {
    setError('');
    try {
      const r = await api<{ waitlistNo: string }>('/api/v1/public/stay-waitlist', { method: 'POST', body: JSON.stringify({ propertyId, guest: v, bungalowTypeId: t.id,
        arrivalDate: checkin, departureDate: checkout, adults, children }) });
      setDone(r.waitlistNo);
    } catch (e) {
      setError((e as Error).message);
    }
  };
  return (
    <div className="w-bk-sheet" role="dialog" aria-modal="true" onClick={onClose}>
      <div className="w-bk-sheet-body w-bk-modal" onClick={(e) => e.stopPropagation()}>
        <button type="button" className="w-bk-close" onClick={onClose} aria-label={id ? 'Tutup' : 'Close'}>×</button>
        <h3>{id ? 'Waitlist' : 'Waitlist'} · {typeName(t, lang)}</h3>
        <p className="w-muted">{dayLabel(checkin, lang)} – {dayLabel(addDays(checkout, 0), lang)}</p>
        {done ? <p role="status">{id ? `Anda masuk waitlist ${done}. Kami menghubungi Anda bila bungalow tersedia.` : `You are on the waitlist ${done}. We contact you when a bungalow frees up.`}</p> : (
          <>
            <div className="w-bk-form">
              <label>{id ? 'Nama' : 'Name'}<input value={v.name} onChange={(e) => setV({ ...v, name: e.target.value })} /></label>
              <label>{id ? 'Nomor HP' : 'Mobile'}<input value={v.phone} onChange={(e) => setV({ ...v, phone: e.target.value })} /></label>
              <label>E-mail<input value={v.email} onChange={(e) => setV({ ...v, email: e.target.value })} /></label>
            </div>
            {error && <p className="w-error">{error}</p>}
            <button type="button" className="w-btn" disabled={!v.name || (v.phone.length < 9 && !v.email)} onClick={() => void send()}>{id ? 'Masuk Waitlist' : 'Join the waitlist'}</button>
          </>
        )}
      </div>
    </div>
  );
}
