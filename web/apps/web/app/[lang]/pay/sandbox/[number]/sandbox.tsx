'use client';
import { useCallback, useEffect, useState } from 'react';
import type { Lang } from '../../../../lib';

/*
 * Sandbox gateway page of the website (demo feedback 10 Oct 2026 #40): the
 * club runs the mock payment gateway, so no real money moves. The payer
 * finishes the payment here — Pay or Simulate failure — through the same
 * settlement as a gateway webhook; the booking then shows Confirmed.
 */

interface Checkout { number: string; description?: string | null; methodType: string; amount: string; currency: string; status: string;
  qrString?: string | null; vaNumber?: string | null; expiresAt?: string | null; paidAt?: string | null }
interface Problem { detail?: string; title?: string }

const fmt = (v: string, cur: string, lang: Lang) =>
  new Intl.NumberFormat(lang === 'id' ? 'id-ID' : 'en-US', { style: 'currency', currency: cur || 'IDR', maximumFractionDigits: cur === 'IDR' ? 0 : 2 }).format(Number(v));

const METHOD: Record<string, [string, string]> = { qris: ['QRIS', 'QRIS'], virtual_account: ['Virtual Account', 'Virtual Account'], card: ['Kartu kredit', 'Card'],
  payment_gateway: ['Payment gateway', 'Payment gateway'] };

export function SandboxPay({ lang, number, back }: { lang: Lang; number: string; back: string }) {
  const id = lang === 'id';
  const [co, setCo] = useState<Checkout | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const path = `/api/v1/public/sandbox-checkout/${encodeURIComponent(number)}`;
  const load = useCallback(() => {
    fetch(path).then(async (r) => {
      const d = await r.json().catch(() => ({}));
      if (r.ok) setCo(d as Checkout);
      else setError(r.status === 404 ? (id ? 'Pembayaran tidak ditemukan.' : 'Payment not found.') : ((d as Problem).detail ?? 'Error'));
    }).catch(() => setError(id ? 'Gangguan jaringan' : 'Network error'));
  }, [path, id]);
  useEffect(() => { load(); }, [load]);
  const complete = async (outcome: 'paid' | 'failed') => {
    setBusy(true);
    setError('');
    const r = await fetch(`${path}:complete`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ outcome }) });
    const d = await r.json().catch(() => ({}));
    setBusy(false);
    if (r.ok) setCo(d as Checkout);
    else setError((d as Problem).detail ?? (d as Problem).title ?? 'Error');
  };
  if (error && !co) return <p className="w-error" role="alert">{error}</p>;
  if (!co) return <p>{id ? 'Memuat…' : 'Loading…'}</p>;
  const m = METHOD[co.methodType] ?? [co.methodType, co.methodType];
  return (
    <div className="w-card" role="status">
      <p className="w-muted">{id ? 'Klub ini memakai gateway pembayaran sandbox: tidak ada uang sungguhan yang berpindah. Selesaikan pembayaran di sini seperti di halaman bank atau e-wallet.'
        : 'This club uses the sandbox payment gateway: no real money moves. Finish the payment here as you would on the bank or e-wallet page.'}</p>
      <p>{id ? 'Pembayaran' : 'Payment'} <strong>{co.number}</strong>{co.description ? ` · ${co.description}` : ''}</p>
      <p style={{ fontSize: 28, margin: '8px 0' }}><strong>{fmt(co.amount, co.currency, lang)}</strong></p>
      <p>{id ? 'Metode' : 'Method'}: {id ? m[0] : m[1]}{co.vaNumber ? <> · VA <strong>{co.vaNumber}</strong></> : null}</p>
      {co.qrString && <p className="w-muted" style={{ wordBreak: 'break-all' }}>QRIS: {co.qrString}</p>}
      {error && <p className="w-error" role="alert">{error}</p>}
      {co.status === 'pending' && (
        <div className="w-form" style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
          <button className="w-btn" disabled={busy} onClick={() => void complete('paid')}>{id ? 'Bayar' : 'Pay'} {fmt(co.amount, co.currency, lang)}</button>
          <button className="w-btn w-btn-ghost" disabled={busy} onClick={() => void complete('failed')}>{id ? 'Simulasikan gagal' : 'Simulate failure'}</button>
        </div>
      )}
      {co.status === 'completed' && (
        <>
          <p><strong>{id ? 'Pembayaran berhasil — pemesanan Anda terkonfirmasi.' : 'Payment received — your booking is confirmed.'}</strong></p>
          <a className="w-btn" href={back}>{id ? 'Lihat pemesanan' : 'See the booking'}</a>
        </>
      )}
      {co.status === 'cancelled' && (
        <>
          <p className="w-error">{id ? 'Pembayaran gagal. Silakan coba lagi dari halaman pemesanan.' : 'The payment failed. Try again from the booking page.'}</p>
          <a className="w-btn" href={back}>{id ? 'Kembali' : 'Back'}</a>
        </>
      )}
    </div>
  );
}
