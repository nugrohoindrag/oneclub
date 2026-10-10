'use client';
import { useCallback, useEffect, useState } from 'react';
import { MoneyInput } from '@oneclub/ui/money-input'; // the barrel's .js re-exports do not resolve in the website build
import type { Lang } from '../../../../lib';

interface RFQLine { rfqLineId: string; description: string; quantity: string; uom?: string | null; neededBy?: string | null }
interface PublicRFQ {
  number: string; title: string; club: string; supplierName: string; status: string; responseDueAt?: string | null; deliveryDate?: string | null;
  currency: string; notes?: string | null; terms?: string | null; responded: boolean; lines: RFQLine[];
}
interface Price { unitPrice: string; discountPercent: string; leadTimeDays: string }

/** The supplier quotes a price per line; a new submission replaces the previous one while the RFQ is open. */
export function SupplierQuote({ lang, token }: { lang: Lang; token: string }) {
  const id = lang === 'id';
  const [rfq, setRfq] = useState<PublicRFQ | null>(null);
  const [error, setError] = useState('');
  const [prices, setPrices] = useState<Record<string, Price>>({});
  const [ref, setRef] = useState('');
  const [valid, setValid] = useState('');
  const [notes, setNotes] = useState('');
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState('');
  const load = useCallback(() => {
    fetch(`/api/v1/public/procurement/rfqs/${token}`).then(async (r) => {
      const d = await r.json();
      if (r.ok) setRfq(d as PublicRFQ);
      else setError(d.detail ?? (id ? 'Permintaan tidak ditemukan' : 'Request not found'));
    }).catch(() => setError(id ? 'Gangguan jaringan' : 'Network error'));
  }, [token, id]);
  useEffect(load, [load]);
  const submit = async () => {
    if (!rfq) return;
    setBusy(true);
    setError('');
    const lines = rfq.lines.filter((l) => prices[l.rfqLineId]?.unitPrice).map((l) => {
      const p = prices[l.rfqLineId];
      return { rfqLineId: l.rfqLineId, unitPrice: p.unitPrice, ...(p.discountPercent ? { discountPercent: p.discountPercent } : {}),
        ...(p.leadTimeDays ? { leadTimeDays: Number(p.leadTimeDays) } : {}) };
    });
    const body = { lines, ...(ref ? { supplierReference: ref } : {}), ...(valid ? { validUntil: valid } : {}), ...(notes ? { notes } : {}) };
    const r = await fetch(`/api/v1/public/procurement/rfqs/${token}:quote`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const d = await r.json();
    setBusy(false);
    if (r.ok) {
      setDone(`${d.number} · ${d.total}`);
      load();
    } else setError(d.detail ?? 'Error');
  };
  if (!rfq) return error ? <p className="w-error">{error}</p> : <p className="w-muted">{id ? 'Memuat…' : 'Loading…'}</p>;
  const open = rfq.status === 'sent';
  const set = (line: string, k: keyof Price, v: string) =>
    setPrices({ ...prices, [line]: { ...(prices[line] ?? { unitPrice: '', discountPercent: '', leadTimeDays: '' }), [k]: v } });
  return (
    <div className="w-card">
      <h2>{rfq.number} · {rfq.title}</h2>
      <p>{rfq.club} → {rfq.supplierName}</p>
      {rfq.responseDueAt && <p>{id ? 'Batas waktu' : 'Deadline'}: {new Date(rfq.responseDueAt).toLocaleString(id ? 'id-ID' : 'en-US')}</p>}
      {rfq.deliveryDate && <p>{id ? 'Tanggal kirim' : 'Delivery date'}: {rfq.deliveryDate}</p>}
      {rfq.notes && <p style={{ whiteSpace: 'pre-wrap' }}>{rfq.notes}</p>}
      {done && <p role="status">{id ? 'Penawaran terkirim' : 'Quotation submitted'}: {done} ({rfq.currency})</p>}
      {rfq.responded && !done && <p role="status">{id ? 'Penawaran Anda sudah kami terima; kirim ulang untuk mengganti.' : 'We received your quotation; submit again to replace it.'}</p>}
      {!open && <p><strong>{id ? 'Permintaan ini sudah ditutup.' : 'This request is closed.'}</strong></p>}
      <div className="w-form">
        {rfq.lines.map((l) => (
          <fieldset key={l.rfqLineId} style={{ border: '1px solid var(--w-line, #ddd)', borderRadius: 8, padding: 12 }}>
            <legend>{l.description} · {l.quantity} {l.uom ?? ''}{l.neededBy ? ` · ${l.neededBy}` : ''}</legend>
            <label>{id ? `Harga satuan (${rfq.currency}, sebelum PPN)` : `Unit price (${rfq.currency}, excl. VAT)`}
              <MoneyInput decimals={2} value={prices[l.rfqLineId]?.unitPrice ?? ''} onChange={(v) => set(l.rfqLineId, 'unitPrice', v)} disabled={!open} />
            </label>
            <label>{id ? 'Diskon (%)' : 'Discount (%)'}
              <input inputMode="decimal" value={prices[l.rfqLineId]?.discountPercent ?? ''} onChange={(e) => set(l.rfqLineId, 'discountPercent', e.target.value)} disabled={!open} />
            </label>
            <label>{id ? 'Waktu kirim (hari)' : 'Lead time (days)'}
              <input inputMode="numeric" value={prices[l.rfqLineId]?.leadTimeDays ?? ''} onChange={(e) => set(l.rfqLineId, 'leadTimeDays', e.target.value)} disabled={!open} />
            </label>
          </fieldset>
        ))}
        <label>{id ? 'Nomor penawaran Anda' : 'Your quotation reference'}<input value={ref} onChange={(e) => setRef(e.target.value)} disabled={!open} /></label>
        <label>{id ? 'Berlaku sampai' : 'Valid until'}<input type="date" value={valid} onChange={(e) => setValid(e.target.value)} disabled={!open} /></label>
        <label>{id ? 'Catatan' : 'Notes'}<textarea value={notes} onChange={(e) => setNotes(e.target.value)} disabled={!open} /></label>
        {error && <p className="w-error" role="alert">{error}</p>}
        <button className="w-btn" disabled={!open || busy} onClick={() => void submit()}>{id ? 'Kirim penawaran' : 'Submit quotation'}</button>
      </div>
      {rfq.terms && <details><summary>{id ? 'Syarat & ketentuan' : 'Terms & conditions'}</summary><p style={{ whiteSpace: 'pre-wrap' }}>{rfq.terms}</p></details>}
    </div>
  );
}
