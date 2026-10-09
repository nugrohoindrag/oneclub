'use client';
import { useCallback, useEffect, useState } from 'react';
import type { Lang } from '../../../lib';
import { checkoutHref } from '../../../pay-link';

interface PublicInvoice {
  number: string; kind: string; billToName: string; issueDate: string; dueDate: string; currency: string; total: string; outstanding: string; status: string;
  lines: { description: string; quantity: string; total: string }[];
}
interface Checkout { number: string; status: string; amount: string; checkoutUrl?: string | null; qrString?: string | null; vaNumber?: string | null }

const fmt = (v: string, lang: Lang) => new Intl.NumberFormat(lang === 'id' ? 'id-ID' : 'en-US', { style: 'currency', currency: 'IDR', maximumFractionDigits: 0 }).format(Number(v));

export function PayInvoice({ lang, token }: { lang: Lang; token: string }) {
  const id = lang === 'id';
  const [inv, setInv] = useState<PublicInvoice | null>(null);
  const [checkout, setCheckout] = useState<Checkout | null>(null);
  const [method, setMethod] = useState('qris');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const load = useCallback(() => {
    fetch(`/api/v1/public/invoices/${token}`).then(async (r) => {
      const d = await r.json();
      if (r.ok) setInv(d as PublicInvoice);
      else setError(d.detail ?? 'Not found');
    }).catch(() => setError('Network error'));
  }, [token]);
  useEffect(() => {
    load();
    const t = setInterval(load, 5000); // the payment arrives by webhook
    return () => clearInterval(t);
  }, [load]);
  const pay = async () => {
    setBusy(true);
    const r = await fetch(`/api/v1/public/invoices/${token}:pay`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ method }) });
    const d = await r.json();
    setBusy(false);
    if (r.ok) setCheckout(d as Checkout);
    else setError(d.detail ?? 'Error');
  };
  if (error) return <p className="w-error">{error}</p>;
  if (!inv) return <p className="w-muted">{id ? 'Memuat…' : 'Loading…'}</p>;
  const open = ['issued', 'partially_paid', 'overdue'].includes(inv.status);
  return (
    <div className="w-card">
      <h2>{inv.number} · {inv.status.replace(/_/g, ' ')}</h2>
      <p>{inv.billToName} · {id ? 'jatuh tempo' : 'due'} {inv.dueDate}</p>
      <table className="w-table">
        <tbody>
          {inv.lines.map((l, i) => <tr key={i}><td>{l.description}</td><td style={{ textAlign: 'right' }}>{fmt(l.total, lang)}</td></tr>)}
          <tr><th>{id ? 'Total' : 'Total'}</th><th style={{ textAlign: 'right' }}>{fmt(inv.total, lang)}</th></tr>
          <tr><th>{id ? 'Sisa tagihan' : 'Outstanding'}</th><th style={{ textAlign: 'right' }}>{fmt(inv.outstanding, lang)}</th></tr>
        </tbody>
      </table>
      {inv.status === 'paid' && <p>{id ? 'Lunas — terima kasih.' : 'Paid — thank you.'}</p>}
      {open && !checkout && (
        <div className="w-form" style={{ marginTop: 16 }}>
          <label>{id ? 'Metode pembayaran' : 'Payment method'}
            <select value={method} onChange={(e) => setMethod(e.target.value)}>
              <option value="qris">QRIS</option><option value="virtual_account">Virtual Account</option><option value="card">{id ? 'Kartu' : 'Card'}</option>
            </select>
          </label>
          <div><button className="w-btn" disabled={busy} onClick={() => void pay()}>{id ? 'Bayar' : 'Pay'} {fmt(inv.outstanding, lang)}</button></div>
        </div>
      )}
      {checkout && checkout.status === 'pending' && (
        <div style={{ marginTop: 16 }}>
          <p>{id ? 'Menunggu pembayaran' : 'Waiting for payment'} {fmt(checkout.amount, lang)}.</p>
          {checkout.vaNumber && <p>Virtual Account: <strong>{checkout.vaNumber}</strong></p>}
          {checkout.qrString && <p className="w-muted">QRIS: {checkout.qrString.slice(0, 24)}…</p>}
          {checkout.checkoutUrl && <a className="w-btn" href={checkoutHref(checkout.checkoutUrl, lang) ?? undefined}>{id ? 'Lanjut ke pembayaran' : 'Continue to payment'}</a>}
        </div>
      )}
    </div>
  );
}
