'use client';
import { useCallback, useEffect, useState } from 'react';
import type { Lang } from '../../../lib';

/*
 * Payment schedule behind its secure link (PRD P3 FR-WEB-P3-05,
 * FR-INT-P3-04): the DP and termin lines, each payable online through the
 * payment gateway. Also the "Pay down payment" step after the online
 * acceptance of a quotation (the schedule appears once the quotation is
 * converted, so the step keeps polling until then).
 */

interface Line { id: string; seq: number; label: string; kind: string; dueDate: string; amount: string; paidAmount: string; status: string; payable: boolean }
interface PublicSchedule { token: string; number: string; title: string; currency: string; totalAmount: string; paidAmount: string; status: string;
  nextLineId?: string | null; lines: Line[] }
interface Checkout { number: string; status: string; amount: string; checkoutUrl?: string | null; qrString?: string | null; vaNumber?: string | null }

const fmt = (v: string, cur: string, lang: Lang) =>
  new Intl.NumberFormat(lang === 'id' ? 'id-ID' : 'en-US', { style: 'currency', currency: cur || 'IDR', maximumFractionDigits: cur === 'IDR' ? 0 : 2 }).format(Number(v));

export function PaySchedule({ lang, token, quotationToken }: { lang: Lang; token?: string; quotationToken?: string }) {
  const id = lang === 'id';
  const [sc, setSc] = useState<PublicSchedule | null>(null);
  const [missing, setMissing] = useState(0); // 404 answers so far
  const [checkout, setCheckout] = useState<Checkout | null>(null);
  const [method, setMethod] = useState('qris');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const url = token ? `/api/v1/public/payment-schedules/${token}` : `/api/v1/public/quotations/${quotationToken}/payment-schedule`;
  const load = useCallback(() => {
    fetch(url).then(async (r) => {
      const d = await r.json().catch(() => ({}));
      if (r.ok) { setSc(d as PublicSchedule); setMissing(0); }
      else if (r.status === 404) setMissing((n) => n + 1);
      else setError(d.detail ?? 'Error');
    }).catch(() => setError(id ? 'Gangguan jaringan' : 'Network error'));
  }, [url, id]);
  // the quotation's schedule is created asynchronously: stop asking after ~1 minute
  const gaveUp = !sc && !!quotationToken && missing >= 12;
  useEffect(() => {
    if (gaveUp) return undefined;
    load();
    const t = setInterval(load, 5000); // conversion and payments arrive asynchronously
    return () => clearInterval(t);
  }, [load, gaveUp]);
  const pay = async (line: string) => {
    if (!sc) return;
    setBusy(true);
    setError('');
    const r = await fetch(`/api/v1/public/payment-schedules/${sc.token}/lines/${line}:pay`, { method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ method }) });
    const d = await r.json().catch(() => ({}));
    setBusy(false);
    if (r.ok) setCheckout(d as Checkout);
    else setError(d.detail ?? 'Error');
  };
  if (error) return <p className="w-error" role="alert">{error}</p>;
  if (!sc) {
    if (missing > 0 && token) return <p className="w-error">{id ? 'Jadwal pembayaran tidak ditemukan' : 'Payment schedule not found'}</p>;
    if (gaveUp) return <p>{id ? 'Tim kami akan mengirimkan tautan pembayaran kepada Anda.' : 'Our team will send you the payment link.'}</p>;
    return <p className="w-muted" role="status">{id ? 'Menyiapkan jadwal pembayaran Anda…' : 'Preparing your payment schedule…'}</p>;
  }
  const cur = sc.currency;
  const next = sc.lines.find((l) => l.id === sc.nextLineId);
  return (
    <div className="w-card" aria-label={id ? 'Jadwal pembayaran' : 'Payment schedule'}>
      <h2>{sc.number} · {sc.title}</h2>
      <table className="w-table">
        <tbody>
          {sc.lines.map((l) => (
            <tr key={l.id}>
              <td>{l.label}<br /><span className="w-muted">{id ? 'jatuh tempo' : 'due'} {l.dueDate} · {l.status.replace(/_/g, ' ')}</span></td>
              <td style={{ textAlign: 'right' }}>{fmt(l.amount, cur, lang)}</td>
            </tr>
          ))}
          <tr><th>Total</th><th style={{ textAlign: 'right' }}>{fmt(sc.totalAmount, cur, lang)}</th></tr>
          <tr><th>{id ? 'Sudah dibayar' : 'Paid'}</th><th style={{ textAlign: 'right' }}>{fmt(sc.paidAmount, cur, lang)}</th></tr>
        </tbody>
      </table>
      {sc.status === 'completed' && <p>{id ? 'Lunas — terima kasih.' : 'Paid in full — thank you.'}</p>}
      {next && !checkout && (
        <div className="w-form" style={{ marginTop: 16 }}>
          <label>{id ? 'Metode pembayaran' : 'Payment method'}
            <select value={method} onChange={(e) => setMethod(e.target.value)}>
              <option value="qris">QRIS</option><option value="virtual_account">Virtual Account</option><option value="card">{id ? 'Kartu' : 'Card'}</option>
            </select>
          </label>
          <div>
            <button className="w-btn" disabled={busy} onClick={() => void pay(next.id)}>
              {id ? 'Bayar' : 'Pay'} {next.label} {fmt(String(Number(next.amount) - Number(next.paidAmount)), cur, lang)}
            </button>
          </div>
        </div>
      )}
      {checkout && checkout.status === 'pending' && (
        <div style={{ marginTop: 16 }} role="status">
          <p>{id ? 'Menunggu pembayaran' : 'Waiting for payment'} {fmt(checkout.amount, cur, lang)}.</p>
          {checkout.vaNumber && <p>Virtual Account: <strong>{checkout.vaNumber}</strong></p>}
          {checkout.qrString && <p className="w-muted">QRIS: {checkout.qrString.slice(0, 24)}…</p>}
          {checkout.checkoutUrl && <a className="w-btn" href={checkout.checkoutUrl}>{id ? 'Lanjut ke pembayaran' : 'Continue to payment'}</a>}
        </div>
      )}
    </div>
  );
}
