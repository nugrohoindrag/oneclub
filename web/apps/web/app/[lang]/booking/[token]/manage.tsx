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
  if (error) return <p className="w-error">{error}</p>;
  if (!b) return <p className="w-muted">{id ? 'Memuat…' : 'Loading…'}</p>;
  return (
    <div className="w-card">
      <h2>{b.code} · {b.status.replace(/_/g, ' ')}</h2>
      <p>{b.courseName} · {b.playDate} {b.localTime} · {b.playerCount} {id ? 'pemain' : 'players'}</p>
      {b.folio && <p>Total {fmt(b.folio.charges, lang)}{Number(b.folio.balance) > 0 ? ` · ${id ? 'sisa' : 'due'} ${fmt(b.folio.balance, lang)}` : ''}</p>}
      {b.status === 'pending' && b.payment && (
        <div>
          <p>{id ? 'Menunggu pembayaran' : 'Waiting for payment'} {fmt(b.payment.amount, lang)}.</p>
          {b.payment.vaNumber && <p>Virtual Account: <strong>{b.payment.vaNumber}</strong></p>}
          {b.payment.checkoutUrl && <a className="w-btn" href={b.payment.checkoutUrl}>{id ? 'Bayar sekarang' : 'Pay now'}</a>}
        </div>
      )}
      {b.status === 'confirmed' && <p>{id ? 'Tunjukkan kode pemesanan saat check-in.' : 'Show your booking code at check-in.'}</p>}
      {b.canCancel && (
        <div className="w-form" style={{ marginTop: 16 }}>
          <label>{id ? 'Alasan pembatalan' : 'Reason for cancelling'}<input value={reason} onChange={(e) => setReason(e.target.value)} /></label>
          <div><button className="w-btn w-btn-ghost" disabled={busy} onClick={() => void cancel()}>{id ? 'Batalkan pemesanan' : 'Cancel booking'}</button></div>
        </div>
      )}
    </div>
  );
}
