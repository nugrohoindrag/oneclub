'use client';
import { useCallback, useEffect, useState } from 'react';
import type { Lang } from '../../../lib';
import { PaySchedule } from '../../payment/[token]/pay';
import { EMeteraiNotice, OtpAccept, type QuotationEMeterai } from './otp';

interface Line { description: string; quantity: string; unitPrice: string; discount: string; total: string }
interface Term { label: string; percent?: string; amount: string; dueDate?: string }
interface PublicQuotation {
  number: string; version: number; title: string; status: string; customerName?: string | null; companyName?: string | null;
  eventType?: string | null; eventDate?: string | null; pax?: number | null; currency: string; lines: Line[]; subtotal: string; discount: string;
  serviceAmount: string; taxAmount: string; total: string; validUntil: string; paymentTerms: Term[]; terms?: string | null;
  acceptedAt?: string | null; acceptedByName?: string | null;
  otpRequired?: boolean; eMeteraiRequired?: boolean; eMeterai?: QuotationEMeterai | null;
}

const fmt = (v: string, cur: string, lang: Lang) =>
  new Intl.NumberFormat(lang === 'id' ? 'id-ID' : 'en-US', { style: 'currency', currency: cur || 'IDR', maximumFractionDigits: cur === 'IDR' ? 0 : 2 }).format(Number(v));

export function AcceptQuotation({ lang, token }: { lang: Lang; token: string }) {
  const id = lang === 'id';
  const [q, setQ] = useState<PublicQuotation | null>(null);
  const [error, setError] = useState('');
  const [name, setName] = useState('');
  const [agree, setAgree] = useState(false);
  const [rejecting, setRejecting] = useState(false);
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const load = useCallback(() => {
    fetch(`/api/v1/public/quotations/${token}`).then(async (r) => {
      const d = await r.json();
      if (r.ok) setQ(d as PublicQuotation);
      else setError(d.detail ?? (id ? 'Penawaran tidak ditemukan' : 'Quotation not found'));
    }).catch(() => setError(id ? 'Gangguan jaringan' : 'Network error'));
  }, [token, id]);
  useEffect(load, [load]);
  const decide = async (action: 'accept' | 'reject') => {
    setBusy(true);
    setError('');
    const body = action === 'accept' ? { name, termsAccepted: agree } : { reason };
    const r = await fetch(`/api/v1/public/quotations/${token}:${action}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const d = await r.json();
    setBusy(false);
    if (r.ok) setQ(d as PublicQuotation);
    else setError(d.detail ?? 'Error');
  };
  if (!q) return error ? <p className="w-error">{error}</p> : <p className="w-muted">{id ? 'Memuat…' : 'Loading…'}</p>;
  const cur = q.currency;
  const status: Record<string, string> = id
    ? { sent: 'Menunggu persetujuan', accepted: 'Disetujui', rejected: 'Ditolak', expired: 'Kedaluwarsa', revised: 'Sudah diganti versi baru' }
    : { sent: 'Awaiting your decision', accepted: 'Accepted', rejected: 'Rejected', expired: 'Expired', revised: 'Replaced by a newer version' };
  return (
    <div className="w-card">
      <h2>{q.number} v{q.version} · {q.title}</h2>
      <p>{[q.companyName, q.customerName].filter(Boolean).join(' · ')}{q.eventDate ? ` · ${q.eventDate}` : ''}{q.pax ? ` · ${q.pax} pax` : ''}</p>
      <p><strong>{status[q.status] ?? q.status}</strong> · {id ? 'berlaku sampai' : 'valid until'} {q.validUntil}</p>
      <table className="w-table">
        <tbody>
          {q.lines.map((l, i) => (
            <tr key={i}><td>{l.description} × {l.quantity}</td><td style={{ textAlign: 'right' }}>{fmt(l.total, cur, lang)}</td></tr>
          ))}
          <tr><td>Subtotal</td><td style={{ textAlign: 'right' }}>{fmt(q.subtotal, cur, lang)}</td></tr>
          {Number(q.discount) > 0 && <tr><td>{id ? 'Diskon' : 'Discount'}</td><td style={{ textAlign: 'right' }}>−{fmt(q.discount, cur, lang)}</td></tr>}
          {Number(q.serviceAmount) > 0 && <tr><td>Service</td><td style={{ textAlign: 'right' }}>{fmt(q.serviceAmount, cur, lang)}</td></tr>}
          {Number(q.taxAmount) > 0 && <tr><td>{id ? 'Pajak' : 'Tax'}</td><td style={{ textAlign: 'right' }}>{fmt(q.taxAmount, cur, lang)}</td></tr>}
          <tr><th>Total</th><th style={{ textAlign: 'right' }}>{fmt(q.total, cur, lang)}</th></tr>
        </tbody>
      </table>
      {q.paymentTerms.length > 0 && (
        <>
          <h3>{id ? 'Termin pembayaran' : 'Payment terms'}</h3>
          <ul>{q.paymentTerms.map((t, i) => <li key={i}>{t.label}: {fmt(t.amount, cur, lang)}{t.dueDate ? ` · ${t.dueDate}` : ''}</li>)}</ul>
        </>
      )}
      {q.terms && <details><summary>{id ? 'Syarat & ketentuan' : 'Terms & conditions'}</summary><p style={{ whiteSpace: 'pre-wrap' }}>{q.terms}</p></details>}
      <EMeteraiNotice lang={lang} required={!!q.eMeteraiRequired} eMeterai={q.eMeterai} accepted={q.status === 'accepted'} />
      {q.status === 'accepted' && <p>{id ? 'Terima kasih' : 'Thank you'}, {q.acceptedByName}. {id ? 'Tim kami akan menghubungi Anda untuk langkah berikutnya.' : 'Our team will contact you for the next steps.'}</p>}
      {q.status === 'accepted' && Number(q.total) > 0 && (
        <section style={{ marginTop: 16 }}>
          {/* FR-WEB-P3-05: pay the down payment right away through the payment gateway */}
          <h3>{id ? 'Bayar uang muka' : 'Pay down payment'}</h3>
          <PaySchedule lang={lang} quotationToken={token} />
        </section>
      )}
      {q.status === 'sent' && !rejecting && (
        <div className="w-form" style={{ marginTop: 16 }}>
          <label>{id ? 'Nama lengkap' : 'Full name'}<input value={name} onChange={(e) => setName(e.target.value)} autoComplete="name" /></label>
          <label style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
            <input type="checkbox" checked={agree} onChange={(e) => setAgree(e.target.checked)} />
            {id ? 'Saya menyetujui penawaran beserta syarat & ketentuannya' : 'I accept the quotation and its terms & conditions'}
          </label>
          {q.otpRequired && <OtpAccept<PublicQuotation> lang={lang} token={token} name={name} agree={agree} onAccepted={setQ} />}
          <div style={{ display: 'flex', gap: 8 }}>
            {!q.otpRequired && <button className="w-btn" disabled={busy || !name.trim() || !agree} onClick={() => void decide('accept')}>{id ? 'Setujui penawaran' : 'Accept quotation'}</button>}
            <button className="w-btn w-btn-ghost" disabled={busy} onClick={() => setRejecting(true)}>{id ? 'Tolak' : 'Decline'}</button>
          </div>
        </div>
      )}
      {q.status === 'sent' && rejecting && (
        <div className="w-form" style={{ marginTop: 16 }}>
          <label>{id ? 'Alasan' : 'Reason'}<textarea value={reason} onChange={(e) => setReason(e.target.value)} rows={3} /></label>
          <div style={{ display: 'flex', gap: 8 }}>
            <button className="w-btn" disabled={busy || !reason.trim()} onClick={() => void decide('reject')}>{id ? 'Kirim penolakan' : 'Send decline'}</button>
            <button className="w-btn w-btn-ghost" disabled={busy} onClick={() => setRejecting(false)}>{id ? 'Batal' : 'Back'}</button>
          </div>
        </div>
      )}
      {error && <p className="w-error" role="alert">{error}</p>}
    </div>
  );
}
