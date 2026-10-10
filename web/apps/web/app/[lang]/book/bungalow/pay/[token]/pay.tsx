'use client';
import { useCallback, useEffect, useState } from 'react';
import type { Lang } from '../../../../../lib';
import { checkoutHref } from '../../../../../pay-link';
import { METHOD, api, dayLabel, money, writeCart, type BookingView, type Cart } from '../../shared';

/*
 * Payment page of a bungalow booking (docs/requirement-booking-hotel-mgcc.md
 * FR-H33): method, amount, QR / VA number, the time left from the server
 * clock (FR-H96) and — on the mock gateway — Bayar / Simulasikan gagal.
 * Paid → the confirmation. Failed or expired → the bungalows are released
 * and the guest goes back to the cart and the form, both still filled
 * (FR-H36, FR-H99).
 */
export function PayBungalow({ lang, propertyId, token }: { lang: Lang; propertyId: string; token: string }) {
  const id = lang === 'id';
  const [v, setV] = useState<BookingView | null>(null);
  const [left, setLeft] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const base = `/api/v1/public/stay-bookings/${encodeURIComponent(token)}`;
  const done = `/${lang}/book/bungalow/booking/${token}`;
  const load = useCallback(async () => {
    try {
      const x = await api<BookingView>(`${base}?propertyId=${propertyId}`);
      setV(x);
      setLeft(x.holdSeconds);
      if (x.status !== 'awaiting_payment' && x.status !== 'expired') window.location.replace(done);
    } catch (e) {
      setError((e as Error).message);
    }
  }, [base, propertyId, done]);
  useEffect(() => {
    void load();
    const poll = setInterval(() => void load(), 5000);
    return () => clearInterval(poll);
  }, [load]);
  useEffect(() => {
    const tick = setInterval(() => setLeft((s) => Math.max(0, s - 1)), 1000);
    return () => clearInterval(tick);
  }, []);
  const backToCart = (x: BookingView) => {
    let cart: Cart | null = null;
    try { cart = JSON.parse(sessionStorage.getItem(`oneclub.bungalow.paid-cart.${token}`) ?? 'null') as Cart | null; } catch { /* ignore */ }
    if (cart) writeCart(cart);
    const s = x.stays[0];
    const q = new URLSearchParams({ checkin: (cart?.checkin ?? s?.arrival.slice(0, 10) ?? ''), checkout: (cart?.checkout ?? s?.departure.slice(0, 10) ?? ''), step: 'details' });
    window.location.href = `/${lang}/book/bungalow?${q}`;
  };
  const settle = async (outcome: 'paid' | 'failed') => {
    if (!v?.payment) return;
    setBusy(true);
    setError('');
    try {
      for (const n of v.payment.numbers) {
        await api(`/api/v1/public/sandbox-checkout/${encodeURIComponent(n)}:complete`, { method: 'POST', body: JSON.stringify({ outcome }) });
      }
      if (outcome === 'paid') {
        window.location.replace(done);
        return;
      }
      backToCart(await api<BookingView>(`${base}:abandon`, { method: 'POST', body: JSON.stringify({ propertyId }) }));
    } catch (e) {
      setError((e as Error).message);
      setBusy(false);
    }
  };
  if (error && !v) return <p className="w-error" role="alert">{error}</p>;
  if (!v) return <p>{id ? 'Memuat…' : 'Loading…'}</p>;
  const pay = v.payment;
  const expired = v.status === 'expired' || left <= 0;
  const mm = `${Math.floor(left / 60)}:${String(left % 60).padStart(2, '0')}`;
  return (
    <div className="w-sc-checkout">
      <div className="w-card" role="status">
        <p className="w-muted">{id ? 'Kode reservasi' : 'Reservation code'} <strong className="w-sc-code">{v.code}</strong></p>
        {expired ? (
          <>
            <h2>{id ? 'Waktu pembayaran habis atau pembayaran gagal' : 'The payment failed or the time ran out'}</h2>
            <p>{id ? 'Bungalow sudah dilepas. Kembali ke keranjang — pilihan dan data Anda tetap tersimpan.' : 'The bungalows are released. Back to the cart — your choice and data are kept.'}</p>
            <button type="button" className="w-btn" disabled={busy}
              onClick={() => void (v.status === 'expired' ? Promise.resolve(backToCart(v)) : settle('failed'))}>{id ? 'Kembali ke keranjang' : 'Back to the cart'}</button>
          </>
        ) : pay ? (
          <>
            <p style={{ fontSize: 28, margin: '8px 0' }}><strong>{money(pay.amount, lang)}</strong></p>
            <p>{id ? 'Metode' : 'Method'}: <strong>{METHOD[pay.method]?.[id ? 0 : 1] ?? pay.method}</strong>
              {pay.vaNumber ? <> · VA <strong className="w-sc-code">{pay.vaNumber}</strong></> : null}</p>
            {pay.qrString && <p className="w-muted" style={{ wordBreak: 'break-all' }}>QRIS: <code>{pay.qrString}</code></p>}
            <p className="w-sc-timer" data-low={left < 120 || undefined}>{id ? 'Selesaikan pembayaran dalam' : 'Pay within'} <strong>{mm}</strong></p>
            {pay.sandbox ? (
              <>
                <p className="w-muted">{id ? 'Gateway pembayaran sandbox: tidak ada uang sungguhan yang berpindah.' : 'Sandbox payment gateway: no real money moves.'}</p>
                <div style={{ display: 'flex', flexWrap: 'wrap', gap: 12 }}>
                  <button type="button" className="w-btn" disabled={busy} onClick={() => void settle('paid')}>{id ? 'Bayar' : 'Pay'} {money(pay.amount, lang)}</button>
                  <button type="button" className="w-btn w-btn-ghost" disabled={busy} onClick={() => void settle('failed')}>{id ? 'Simulasikan gagal' : 'Simulate failure'}</button>
                </div>
              </>
            ) : (
              <a className="w-btn" href={checkoutHref(null, lang) ?? '#'}>{id ? 'Buka halaman pembayaran' : 'Open the payment page'}</a>
            )}
          </>
        ) : <p>{id ? 'Menunggu konfirmasi pembayaran…' : 'Waiting for the payment confirmation…'}</p>}
        {error && <p className="w-error" role="alert">{error}</p>}
      </div>
      <div className="w-card">
        <h2>{id ? 'Ringkasan' : 'Summary'}</h2>
        {v.stays.map((s) => (
          <div key={s.id} className="w-sc-line"><span>{s.typeName} · {s.ratePlanName}<br />
            <small className="w-muted">{dayLabel(s.arrival, lang)} – {dayLabel(s.departure, lang)} · {s.nights} {id ? 'malam' : 'nights'}</small></span>
            <span>{money(s.total, lang)}</span><span /></div>
        ))}
        <dl className="w-sc-costs">
          <dt><strong>Total</strong></dt><dd><strong>{money(v.total, lang)}</strong></dd>
          <dt>{id ? 'Dibayar sekarang' : 'Pay now'}</dt><dd>{money(v.depositDue, lang)}</dd>
          <dt>{id ? 'Sisa di hotel' : 'At the hotel'}</dt><dd>{money(Number(v.total) - Number(v.depositDue), lang)}</dd>
        </dl>
      </div>
    </div>
  );
}
