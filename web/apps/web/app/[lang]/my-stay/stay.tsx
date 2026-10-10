'use client';
import { useCallback, useEffect, useState } from 'react';
import type { Lang } from '../../lib';
import { PAY_STATUS, STATUS } from '../book/bungalow/shared';

interface Experience {
  stay: { stayNo: string; unitName: string; start: string; end: string; status: string; nights: number; adults: number; children: number;
    lateCheckOutUntil: string | null; bookingStatus: string; paymentStatus: string; unitAssigned: boolean };
  typeName: string; description: string | null; amenities: string[]; checkInTime: string; checkOutTime: string; houseRules: string; houseRulesEn: string;
  propertyInfo: string; requests: { id: string; requestNo: string; requestType: string; status: string }[]; folio: { description: string; total: string }[];
  folioTotal: string; folioPaid: string; folioBalance: string; canRequest: boolean; bookingToken: string | null;
  contact: { name: string; address: string; phone: string; email: string; whatsApp: string };
}

const fmt = (v: string, lang: Lang) => new Intl.NumberFormat(lang === 'id' ? 'id-ID' : 'en-US', { style: 'currency', currency: 'IDR', maximumFractionDigits: 0 }).format(Number(v));
const label = (s: string) => s.replace(/_/g, ' ').replace(/^./, (c) => c.toUpperCase());
const SERVICES: [string, string, string][] = [['extra_towel', 'Handuk tambahan', 'Extra towel'], ['room_cleaning', 'Bersihkan kamar', 'Room cleaning'],
  ['extra_bed', 'Extra bed', 'Extra bed'], ['laundry', 'Laundry', 'Laundry'], ['food_beverage', 'Makanan & minuman', 'Food & beverage'],
  ['transportation', 'Transportasi', 'Transportation'], ['maintenance', 'Perbaikan', 'Maintenance'], ['other', 'Lainnya', 'Other']];

/**
 * My Stay without an account (docs/requirement-booking-hotel-mgcc.md FR-H41):
 * from the link of the booking or the reservation number + e-mail or phone
 * — the bungalow, status and payment (§12.3), times, folio, guest services,
 * house rules and the booking page (e-voucher, payment, cancellation).
 * Access Control and Digital Key are out of scope.
 */
export function MyStay({ lang, propertyId, token }: { lang: Lang; propertyId: string; token?: string }) {
  const id = lang === 'id';
  const [lookup, setLookup] = useState({ reference: '', contact: '' });
  const [x, setX] = useState<Experience | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [service, setService] = useState('extra_towel');
  const [note, setNote] = useState('');
  const [sent, setSent] = useState('');
  const post = useCallback(async (path: string, body: Record<string, unknown>) => {
    setBusy(true);
    setError('');
    const who = token ? { token } : lookup;
    const r = await fetch(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ propertyId, ...who, ...body }) });
    const d = await r.json().catch(() => ({}));
    setBusy(false);
    if (!r.ok) {
      setError(d.detail ?? (id ? 'Reservasi tidak ditemukan' : 'Reservation not found'));
      return null;
    }
    return d;
  }, [propertyId, token, lookup, id]);
  const open = useCallback(async () => {
    const d = await post('/api/v1/public/my-stay', {});
    if (d) setX(d as Experience);
  }, [post]);
  useEffect(() => {
    if (token) void open();
    // the link opens My Stay at once
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token]);
  const ask = async () => {
    const d = await post('/api/v1/public/my-stay/requests', { requestType: service, description: note || undefined });
    if (d) {
      setSent((d as { requestNo: string }).requestNo);
      setNote('');
      void open();
    }
  };
  if (!x) {
    if (token && !error) return <p>{id ? 'Memuat…' : 'Loading…'}</p>;
    return (
      <div className="w-card w-form">
        <p>{id ? 'Masukkan nomor reservasi dan e-mail atau nomor telepon pemesan.' : 'Enter your reservation number and the e-mail or phone of the booking.'}</p>
        <label>{id ? 'Nomor reservasi' : 'Reservation number'}<input value={lookup.reference} onChange={(e) => setLookup({ ...lookup, reference: e.target.value })} placeholder="STY-…" /></label>
        <label>{id ? 'E-mail atau telepon' : 'E-mail or phone'}<input value={lookup.contact} onChange={(e) => setLookup({ ...lookup, contact: e.target.value })} /></label>
        {error && <p className="w-error">{error}</p>}
        <div><button className="w-btn" disabled={busy || !lookup.reference || !lookup.contact} onClick={() => void open()}>{id ? 'Buka My Stay' : 'Open My Stay'}</button></div>
      </div>
    );
  }
  const s = x.stay;
  const d = (iso: string) => new Date(iso).toLocaleDateString(id ? 'id-ID' : 'en-GB', { day: 'numeric', month: 'long', year: 'numeric', timeZone: 'Asia/Jakarta' });
  return (
    <div style={{ display: 'grid', gap: 16 }}>
      <div className="w-card">
        <h2>{s.unitAssigned && s.status === 'checked_in' ? s.unitName : x.typeName}</h2>
        <p className="w-muted">{x.typeName} · {s.stayNo} · <span className="w-sc-state">{STATUS[s.bookingStatus]?.[id ? 0 : 1] ?? label(s.status)}</span>
          {' · '}{PAY_STATUS[s.paymentStatus]?.[id ? 0 : 1] ?? s.paymentStatus}</p>
        <p><strong>{d(s.start)} – {d(s.end)}</strong> · {s.nights} {id ? 'malam' : 'night(s)'}</p>
        <p>Check-in {x.checkInTime} · Check-out {s.lateCheckOutUntil ? new Date(s.lateCheckOutUntil).toLocaleTimeString('en-GB', { timeStyle: 'short', timeZone: 'Asia/Jakarta' }) : x.checkOutTime}</p>
        {x.bookingToken && <p><a className="w-btn w-btn-ghost" href={`/${lang}/book/bungalow/booking/${x.bookingToken}`}>{id ? 'Reservasi, e-voucher & pembayaran' : 'Reservation, e-voucher & payment'}</a></p>}
      </div>
      {x.canRequest && (
        <div className="w-card w-form">
          <h3>{id ? 'Layanan Tamu' : 'Guest Services'}</h3>
          <label>{id ? 'Permintaan' : 'Request'}
            <select value={service} onChange={(e) => setService(e.target.value)}>{SERVICES.map(([k, a, b]) => <option key={k} value={k}>{id ? a : b}</option>)}</select>
          </label>
          <label>{id ? 'Detail' : 'Details'}<textarea rows={2} value={note} onChange={(e) => setNote(e.target.value)} /></label>
          <div><button className="w-btn" disabled={busy} onClick={() => void ask()}>{id ? 'Kirim' : 'Send'}</button></div>
          {sent && <p>{id ? 'Permintaan terkirim' : 'Request sent'}: {sent}</p>}
          {error && <p className="w-error">{error}</p>}
          {x.requests.length > 0 && <ul>{x.requests.map((r) => <li key={r.id}>{r.requestNo} · {label(r.requestType)} · {label(r.status)}</li>)}</ul>}
        </div>
      )}
      <div className="w-card">
        <h3>Folio</h3>
        <table className="w-table"><tbody>
          {x.folio.map((l, i) => <tr key={i}><td>{l.description}</td><td style={{ textAlign: 'right' }}>{fmt(l.total, lang)}</td></tr>)}
          <tr><th>Total</th><th style={{ textAlign: 'right' }}>{fmt(x.folioTotal, lang)}</th></tr>
          <tr><th>{id ? 'Dibayar (termasuk deposit)' : 'Paid (incl. deposit)'}</th><th style={{ textAlign: 'right' }}>{fmt(x.folioPaid, lang)}</th></tr>
          <tr><th>{id ? 'Sisa' : 'Balance'}</th><th style={{ textAlign: 'right' }}>{fmt(x.folioBalance, lang)}</th></tr>
        </tbody></table>
      </div>
      <div className="w-card">
        <h3>{id ? 'Informasi Menginap' : 'Stay Information'}</h3>
        {x.description && <p>{x.description}</p>}
        {x.amenities.length > 0 && <p>{x.amenities.map(label).join(' · ')}</p>}
        <h4>{id ? 'Tata Tertib' : 'House Rules'}</h4><p style={{ whiteSpace: 'pre-wrap' }}>{id ? x.houseRules : x.houseRulesEn || x.houseRules}</p>
        <h4>{id ? 'Informasi Properti' : 'Property Information'}</h4><p style={{ whiteSpace: 'pre-wrap' }}>{x.propertyInfo}</p>
        <p className="w-bk-small">{x.contact.name}{x.contact.address ? ` · ${x.contact.address}` : ''}{x.contact.phone ? ` · ${x.contact.phone}` : ''}
          {x.contact.whatsApp ? ` · WhatsApp ${x.contact.whatsApp}` : ''}</p>
      </div>
    </div>
  );
}
