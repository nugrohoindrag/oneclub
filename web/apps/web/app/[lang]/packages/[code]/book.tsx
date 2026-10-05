'use client';
import { useState } from 'react';
import { idr, type Lang } from '../../../lib';
import { checkPromoCode, type CodeCheck } from '../../promotions/check';
import type { PublicPackage } from '../shared';

type Result = Record<string, unknown>;
interface Day { date: string; available: boolean; reason?: string; price: string }

/**
 * Book Package with the non-member flow of P2 (FR-WEB-P3-04): date and pax →
 * availability of every component → add-ons → promo code (checked, rate
 * limited, FR-WEB-P3-07) → guest information → online payment. The booking
 * is held until the payment arrives.
 */
export function BookPackage({ lang, propertyId, pkg }: { lang: Lang; propertyId: string; pkg: PublicPackage }) {
  const id = lang === 'id';
  const [date, setDate] = useState('');
  const [pax, setPax] = useState(String(pkg.minPax));
  const [nights, setNights] = useState(pkg.nights ? String(pkg.nights) : '');
  const [day, setDay] = useState<Day | null>(null);
  const [addons, setAddons] = useState<string[]>([]);
  const [code, setCode] = useState('');
  const [check, setCheck] = useState<CodeCheck | { error: string } | null>(null);
  const [guest, setGuest] = useState({ name: '', phone: '', email: '', website: '' });
  const [method, setMethod] = useState('qris');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [result, setResult] = useState<Result | null>(null);
  const availability = async () => {
    setError('');
    setDay(null);
    const r = await fetch(`/api/v1/public/packages/${pkg.code.toLowerCase()}?propertyId=${propertyId}&date=${date}&pax=${pax}&nights=${nights}`);
    const d = await r.json().catch(() => ({}));
    if (!r.ok) {
      setError(String(d.detail ?? r.status));
      return;
    }
    setDay(((d.availability as Day[]) ?? [])[0] ?? null);
  };
  const book = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError('');
    const r = await fetch('/api/v1/public/package-bookings', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({
      propertyId, guest, packageCode: pkg.code, startDate: date, pax: Number(pax) || undefined, nights: Number(nights) || undefined,
      addons: addons.length ? addons : undefined, promoCodes: code && check && 'valid' in check && check.valid ? [code] : undefined,
      paymentMethod: method || undefined }) });
    const d = (await r.json().catch(() => ({}))) as Result;
    setBusy(false);
    if (r.ok) setResult(d);
    else setError(String(d.detail ?? d.title ?? r.status));
  };
  if (result) {
    const b = result.booking as Result;
    const online = (result.checkout as Result | null)?.online as Result | undefined;
    return (
      <div className="w-card" role="status">
        <h2 style={{ marginTop: 0 }}>{id ? 'Konfirmasi Pemesanan' : 'Booking Confirmation'}</h2>
        <p>{id ? 'Nomor' : 'Reference'} <strong>{String(b.number)}</strong> · {idr(String(b.total), lang)}
          {Number(b.discountTotal) > 0 ? ` (${id ? 'promo' : 'promotion'} −${idr(String(b.discountTotal), lang)})` : ''}</p>
        {b.status === 'pending' && <p>{id ? 'Pemesanan ditahan sampai' : 'Your booking is held until'} {new Date(String(b.holdExpiresAt)).toLocaleString(lang)}.</p>}
        {online && online.status === 'pending' && (
          <p>{id ? 'Selesaikan pembayaran' : 'Complete the payment'}{online.vaNumber ? <> — VA <strong>{String(online.vaNumber)}</strong></> : null}
            {online.checkoutUrl ? <> — <a href={String(online.checkoutUrl)}>{id ? 'buka halaman pembayaran' : 'open the payment page'}</a></> : null}.</p>
        )}
      </div>
    );
  }
  return (
    <form className="w-card w-form" onSubmit={book}>
      <h2 style={{ marginTop: 0 }}>{id ? 'Pesan Paket' : 'Book Package'}</h2>
      <label htmlFor="pkg-date">{id ? 'Tanggal' : 'Date'}<input id="pkg-date" type="date" required value={date} onChange={(e) => { setDate(e.target.value); setDay(null); }} /></label>
      <label htmlFor="pkg-pax">{id ? 'Jumlah orang' : 'Persons'}<input id="pkg-pax" type="number" min={pkg.minPax} max={pkg.maxPax ?? undefined} value={pax}
        onChange={(e) => { setPax(e.target.value); setDay(null); }} /></label>
      {pkg.nights > 0 && <label htmlFor="pkg-nights">{id ? 'Malam' : 'Nights'}<input id="pkg-nights" type="number" min={1} value={nights}
        onChange={(e) => { setNights(e.target.value); setDay(null); }} /></label>}
      <button type="button" className="w-btn" disabled={!date} onClick={() => void availability()}>{id ? 'Cek ketersediaan' : 'Check availability'}</button>
      {day && (day.available
        ? <p role="status">{id ? 'Tersedia' : 'Available'} · {idr(day.price, lang)}</p>
        : <p className="w-error" role="alert">{id ? 'Tidak tersedia' : 'Not available'}: {day.reason}</p>)}
      {day?.available && (
        <>
          {pkg.addons.length > 0 && (
            <fieldset>
              <legend>{id ? 'Tambahan' : 'Add-ons'}</legend>
              {pkg.addons.map((a) => (
                <label key={a.id} className="w-check"><input type="checkbox" checked={addons.includes(a.id)}
                  onChange={(e) => setAddons(e.target.checked ? [...addons, a.id] : addons.filter((x) => x !== a.id))} />
                  {a.name} (+{idr(a.price, lang)}{a.perPax ? (id ? ' / orang' : ' / person') : ''})</label>
              ))}
            </fieldset>
          )}
          <label htmlFor="pkg-code">{id ? 'Kode promo' : 'Promo code'}<input id="pkg-code" value={code} onChange={(e) => { setCode(e.target.value); setCheck(null); }} /></label>
          {code && <button type="button" className="w-btn" onClick={() => void checkPromoCode(propertyId, code, day.price, 'package').then(setCheck)}>
            {id ? 'Pakai kode' : 'Apply code'}</button>}
          {check && 'valid' in check && <p role="status">{check.valid ? `${id ? 'Kode berlaku' : 'Code applied'}: ${check.promotion?.name ?? ''}`
            : `${id ? 'Kode tidak berlaku' : 'Code not valid'}: ${check.reason ?? ''}`}</p>}
          {check && 'error' in check && <p className="w-error" role="alert">{check.error === 'too_many' ? (id ? 'Terlalu banyak percobaan.' : 'Too many attempts.') : check.error}</p>}
          <fieldset>
            <legend>{id ? 'Data Tamu' : 'Guest Information'}</legend>
            <label>{id ? 'Nama' : 'Name'}<input required value={guest.name} onChange={(e) => setGuest({ ...guest, name: e.target.value })} /></label>
            <label>{id ? 'Telepon (WhatsApp)' : 'Phone (WhatsApp)'}<input type="tel" value={guest.phone} onChange={(e) => setGuest({ ...guest, phone: e.target.value })} /></label>
            <label>E-mail<input type="email" value={guest.email} onChange={(e) => setGuest({ ...guest, email: e.target.value })} /></label>
            <label aria-hidden="true" style={{ position: 'absolute', left: -9999 }}>Website<input tabIndex={-1} autoComplete="off" value={guest.website}
              onChange={(e) => setGuest({ ...guest, website: e.target.value })} /></label>
          </fieldset>
          <label>{id ? 'Pembayaran' : 'Payment'}
            <select value={method} onChange={(e) => setMethod(e.target.value)}>
              <option value="qris">QRIS</option><option value="virtual_account">Virtual Account</option><option value="card">{id ? 'Kartu' : 'Card'}</option>
              <option value="">{id ? 'Bayar nanti (sebelum batas waktu)' : 'Pay later (before the hold expires)'}</option>
            </select>
          </label>
          <button className="w-btn" disabled={busy || !guest.name || (!guest.phone && !guest.email)}>{busy ? '…' : id ? 'Pesan' : 'Book'}</button>
        </>
      )}
      {error && <p className="w-error" role="alert">{error}</p>}
    </form>
  );
}
