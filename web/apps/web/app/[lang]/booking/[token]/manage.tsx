'use client';
import { useCallback, useEffect, useState } from 'react';
import type { Lang } from '../../../lib';

interface PublicBooking {
  code: string; status: string; courseName: string; localTime: string; playDate: string; playerCount: number; contactName: string; canCancel: boolean;
  folio?: { charges: string; balance: string } | null;
  payment?: { number: string; status: string; amount: string; checkoutUrl?: string | null; qrString?: string | null; vaNumber?: string | null } | null;
  qrToken?: string | null;
}

const fmt = (v: string, lang: Lang) => new Intl.NumberFormat(lang === 'id' ? 'id-ID' : 'en-US', { style: 'currency', currency: 'IDR', maximumFractionDigits: 0 }).format(Number(v));

export function ManageBooking({ lang, token }: { lang: Lang; token: string }) {
  const id = lang === 'id';
  const [b, setB] = useState<PublicBooking | null>(null);
  const [error, setError] = useState('');
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [amount, setAmount] = useState('');
  const [method, setMethod] = useState('qris');
  const load = useCallback(() => {
    fetch(`/api/v1/public/bookings/${token}`).then(async (r) => {
      const d = await r.json();
      if (r.ok) setB(d as PublicBooking);
      else setError(d.detail ?? 'Not found');
    }).catch(() => setError('Network error'));
  }, [token]);
  useEffect(() => {
    load();
    const t = setInterval(load, 5000); // payment confirmation arrives by webhook
    return () => clearInterval(t);
  }, [load]);
  const cancel = async () => {
    setBusy(true);
    const r = await fetch(`/api/v1/public/bookings/${token}:cancel`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ reason }) });
    const d = await r.json();
    setBusy(false);
    if (r.ok) setB(d as PublicBooking);
    else setError(d.detail ?? 'Error');
  };
  // pay the balance, or part of it, online (the rest at the front desk)
  const pay = async () => {
    setBusy(true);
    setError('');
    const r = await fetch(`/api/v1/public/bookings/${token}:pay`, { method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ method, amount: amount || undefined }) });
    const d = await r.json();
    setBusy(false);
    if (!r.ok) return setError(d.detail ?? 'Error');
    const nb = d as PublicBooking;
    if (nb.payment?.checkoutUrl) window.location.href = nb.payment.checkoutUrl;
    else setB(nb);
  };
  if (error) return <p className="w-error">{error}</p>;
  if (!b) return <p className="w-muted">{id ? 'Memuat…' : 'Loading…'}</p>;
  return (
    <div className="w-card">
      <h2>{b.code} · {b.status.replace(/_/g, ' ')}</h2>
      <p>{b.courseName} · {b.playDate} {b.localTime} · {b.playerCount} {id ? 'pemain' : 'players'}</p>
      {b.folio && <p>Total {fmt(b.folio.charges, lang)}{Number(b.folio.balance) > 0 ? ` · ${id ? 'sisa' : 'due'} ${fmt(b.folio.balance, lang)}` : ''}</p>}
      {b.payment?.status === 'pending' && (
        <div>
          <p>{id ? 'Menunggu pembayaran' : 'Waiting for payment'} {fmt(b.payment.amount, lang)}.</p>
          {b.payment.vaNumber && <p>Virtual Account: <strong>{b.payment.vaNumber}</strong></p>}
          {b.payment.checkoutUrl && <a className="w-btn" href={b.payment.checkoutUrl}>{id ? 'Bayar sekarang' : 'Pay now'}</a>}
        </div>
      )}
      {b.status === 'confirmed' && <p>{id ? 'Tunjukkan kode pemesanan saat check-in.' : 'Show your booking code at check-in.'}</p>}
      {b.folio && Number(b.folio.balance) > 0 && b.payment?.status !== 'pending' && ['pending', 'confirmed', 'checked_in', 'completed'].includes(b.status) && (
        <div className="w-form" style={{ marginTop: 16 }}>
          <label>{id ? `Bayar sekarang (Rp, kosong = seluruh sisa ${fmt(b.folio.balance, lang)})` : `Pay now (IDR, empty = the whole ${fmt(b.folio.balance, lang)})`}
            <input inputMode="numeric" value={amount} onChange={(e) => setAmount(e.target.value.replace(/\D/g, ''))} />
          </label>
          <label>{id ? 'Metode pembayaran' : 'Payment method'}
            <select value={method} onChange={(e) => setMethod(e.target.value)}>
              <option value="qris">QRIS</option><option value="virtual_account">Virtual Account</option><option value="card">{id ? 'Kartu kredit' : 'Card'}</option>
            </select>
          </label>
          <p className="w-muted">{id ? 'Atau bayar di front desk saat datang maupun setelah bermain.' : 'Or pay at the front desk when you arrive or after your round.'}</p>
          <div><button className="w-btn" disabled={busy || Number(amount) > Number(b.folio.balance)} onClick={() => void pay()}>{id ? 'Bayar' : 'Pay'}</button></div>
        </div>
      )}
      {b.canCancel && (
        <div className="w-form" style={{ marginTop: 16 }}>
          <label>{id ? 'Alasan pembatalan' : 'Reason for cancelling'}<input value={reason} onChange={(e) => setReason(e.target.value)} /></label>
          <div><button className="w-btn w-btn-ghost" disabled={busy} onClick={() => void cancel()}>{id ? 'Batalkan pemesanan' : 'Cancel booking'}</button></div>
        </div>
      )}
    </div>
  );
}
