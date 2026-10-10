'use client';
import { useCallback, useEffect, useState } from 'react';
import type { Lang } from '../../../../../lib';
import { checkoutHref } from '../../../../../pay-link';
import { api, dayLabel, hhmm, money, readCart, writeCart, type BookingView, type Line } from '../../shared';

/*
 * Payment page of a court booking (FR-28..FR-34): method, amount, QRIS / VA,
 * the time left to pay from the server clock, and — on the sandbox gateway —
 * Pay / Simulate failure. Paid → the confirmation; failed or expired → the
 * slots are released and the visitor goes back to the cart with the hours
 * that are still free.
 */

const METHOD: Record<string, [string, string]> = { qris: ['QRIS', 'QRIS'], virtual_account: ['Virtual Account', 'Virtual Account'], card: ['Kartu kredit', 'Card'],
  payment_gateway: ['E-wallet', 'E-wallet'] };

export function PayCourt({ lang, propertyId, token }: { lang: Lang; propertyId: string; token: string }) {
  const id = lang === 'id';
  const [v, setV] = useState<BookingView | null>(null);
  const [error, setError] = useState('');
  const [left, setLeft] = useState(0);
  const [busy, setBusy] = useState(false);
  const base = `/api/v1/public/court-bookings/${encodeURIComponent(token)}`;
  const done = `/${lang}/book/sport-club/booking/${token}`;
  const load = useCallback(async () => {
    try {
      const x = await api<BookingView>(`${base}?propertyId=${propertyId}`);
      setV(x);
      setLeft(x.booking.holdSeconds);
      if (x.booking.payStatus === 'paid' || x.booking.payStatus === 'overpaid' || ['scheduled', 'late', 'playing', 'finished'].includes(x.booking.state)) {
        window.location.replace(done);
      }
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
  // back to the cart: the hours of this booking that are still in the future, with the sport page
  const backToCart = useCallback((x: BookingView) => {
    let lines: Line[] = [];
    try { lines = JSON.parse(sessionStorage.getItem(`oneclub.sportclub.paid-cart.${token}`) ?? '[]') as Line[]; } catch { /* ignore */ }
    if (!lines.length) {
      lines = x.booking.lines.filter((l) => l.courtId).map((l) => ({ courtId: l.courtId!, courtName: l.courtName, facility: l.facilityName ?? '', sport: '',
        start: l.start, end: l.end, price: l.amount ?? '0' }));
    }
    const cart = readCart();
    writeCart([...cart, ...lines.filter((l) => !cart.some((c) => c.courtId === l.courtId && c.start === l.start))]);
    const code = x.booking.lines[0]?.facilityCode;
    window.location.href = code ? `/${lang}/book/sport-club/${code.toLowerCase()}` : `/${lang}/book/sport-club`;
  }, [lang, token]);
  const settle = async (outcome: 'paid' | 'failed') => {
    if (!v?.payment) return;
    setBusy(true);
    setError('');
    try {
      await api(`/api/v1/public/sandbox-checkout/${encodeURIComponent(v.payment.number)}:complete`, { method: 'POST', body: JSON.stringify({ outcome }) });
      if (outcome === 'paid') { window.location.replace(done); return; }
      const x = await api<BookingView>(`${base}:abandon?propertyId=${propertyId}`, { method: 'POST', body: '{}' });
      backToCart(x);
    } catch (e) {
      setError((e as Error).message);
      setBusy(false);
    }
  };
  if (error && !v) return <p className="w-error" role="alert">{error}</p>;
  if (!v) return <p>{id ? 'Memuat…' : 'Loading…'}</p>;
  const b = v.booking;
  const pay = v.payment;
  const expired = b.state === 'expired' || b.state === 'void' || (pay?.status === 'cancelled');
  const mm = `${Math.floor(left / 60)}:${String(left % 60).padStart(2, '0')}`;
  return (
    <div className="w-sc-checkout">
      <div className="w-card" role="status">
        <p className="w-muted">{id ? 'Kode booking' : 'Booking code'} <strong>{b.code}</strong></p>
        {expired ? (
          <>
            <h2>{id ? 'Waktu pembayaran habis atau pembayaran gagal' : 'The payment failed or the time ran out'}</h2>
            <p>{id ? 'Slot sudah dilepas. Kembali ke keranjang — jadwal yang masih kosong tetap tersimpan.' : 'The slots are released. Back to the cart — the hours still free are kept.'}</p>
            <button type="button" className="w-btn" onClick={() => backToCart(v)}>{id ? 'Kembali ke keranjang' : 'Back to the cart'}</button>
          </>
        ) : pay ? (
          <>
            <p style={{ fontSize: 28, margin: '8px 0' }}><strong>{money(pay.amount, lang)}</strong></p>
            <p>{id ? 'Metode' : 'Method'}: <strong>{METHOD[pay.method]?.[id ? 0 : 1] ?? pay.method}</strong>{pay.vaNumber ? <> · VA <strong className="w-sc-code">{pay.vaNumber}</strong></> : null}</p>
            {pay.qrString && <p className="w-muted" style={{ wordBreak: 'break-all' }}>QRIS: <code>{pay.qrString}</code></p>}
            <p className="w-sc-timer" data-low={left < 120 || undefined}>{id ? 'Selesaikan pembayaran dalam' : 'Pay within'} <strong>{mm}</strong></p>
            {pay.sandbox ? (
              <>
                <p className="w-muted">{id ? 'Klub ini memakai gateway pembayaran sandbox: tidak ada uang sungguhan yang berpindah.' : 'This club uses the sandbox payment gateway: no real money moves.'}</p>
                <div style={{ display: 'flex', flexWrap: 'wrap', gap: 12 }}>
                  <button type="button" className="w-btn" disabled={busy} onClick={() => void settle('paid')}>{id ? 'Bayar' : 'Pay'} {money(pay.amount, lang)}</button>
                  <button type="button" className="w-btn w-btn-ghost" disabled={busy} onClick={() => void settle('failed')}>{id ? 'Simulasikan gagal' : 'Simulate failure'}</button>
                </div>
              </>
            ) : pay.checkoutUrl ? (
              <a className="w-btn" href={checkoutHref(pay.checkoutUrl, lang, `/${lang}/book/sport-club/pay/${token}`) ?? pay.checkoutUrl}>{id ? 'Buka halaman pembayaran' : 'Open the payment page'}</a>
            ) : null}
          </>
        ) : <p>{id ? 'Menunggu konfirmasi pembayaran…' : 'Waiting for the payment confirmation…'}</p>}
        {error && <p className="w-error" role="alert">{error}</p>}
      </div>
      <div className="w-card">
        <h2>{id ? 'Ringkasan' : 'Summary'}</h2>
        {b.lines.map((l) => (
          <div key={l.id} className="w-sc-line"><span>{l.facilityName} · {l.courtName}<br /><small className="w-muted">{dayLabel(l.start, lang)} {hhmm(l.start)}–{hhmm(l.end)}</small></span>
            <span>{l.amount ? money(l.amount, lang) : ''}</span><span /></div>
        ))}
        <dl className="w-sc-costs">
          {v.bill.filter((x) => x.kind !== 'rent').map((x, i) => <div key={i} style={{ display: 'contents' }}><dt>{x.description}</dt><dd>{money(x.total, lang)}</dd></div>)}
          <dt>{id ? 'Pajak' : 'Tax'}</dt><dd>{money(b.tax, lang)}</dd>
          <dt><strong>{id ? 'Total' : 'Total'}</strong></dt><dd><strong>{money(b.charges, lang)}</strong></dd>
        </dl>
      </div>
    </div>
  );
}
