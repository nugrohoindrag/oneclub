'use client';
import { useCallback, useEffect, useState } from 'react';
import type { Lang } from '../../../../../lib';
import { api, dayLabel, hhmm, money, STATE_LABEL, type BookingView } from '../../shared';

/*
 * Confirmation / Cek Booking page of a court booking (FR-36..FR-42): the
 * booking code, the QR to show at the Sport Club front desk, the courts and
 * hours, the bill, the club address with a map, the e-ticket PDF and the
 * calendar file. The link is personal (token); no cancellation (FR-118).
 */

export function CourtBookingView({ lang, propertyId, token }: { lang: Lang; propertyId: string; token: string }) {
  const id = lang === 'id';
  const [v, setV] = useState<BookingView | null>(null);
  const [error, setError] = useState('');
  const [copied, setCopied] = useState(false);
  const base = `/api/v1/public/court-bookings/${encodeURIComponent(token)}`;
  const q = `?propertyId=${propertyId}`;
  const load = useCallback(async () => {
    try {
      setV(await api<BookingView>(base + q));
    } catch (e) {
      setError((e as Error).message);
    }
  }, [base, q]);
  useEffect(() => {
    void load();
    const poll = setInterval(() => void load(), 5_000);
    return () => clearInterval(poll);
  }, [load]);
  if (error && !v) return <p className="w-error" role="alert">{error}</p>;
  if (!v) return <p>{id ? 'Memuat…' : 'Loading…'}</p>;
  const b = v.booking;
  const live = ['scheduled', 'late', 'playing'].includes(b.state);
  const addr = [v.club.name, v.club.address, v.club.city].filter(Boolean).join(', ');
  return (
    <div className="w-sc-checkout">
      <div className="w-card" role="status" style={{ textAlign: 'center' }}>
        <p className="w-muted">{id ? 'Kode booking' : 'Booking code'}</p>
        <p className="w-sc-code" style={{ fontSize: 28 }}>{b.code}</p>
        <p><span className="w-sc-state" data-state={b.state}>{STATE_LABEL[b.state]?.[id ? 0 : 1] ?? b.state}</span></p>
        {b.state === 'awaiting_payment' && (b.payStatus === 'paid' || b.payStatus === 'overpaid'
          ? <p>{id ? 'Pembayaran diterima — booking sedang dikonfirmasi…' : 'Payment received — confirming the booking…'}</p>
          : <a className="w-btn" href={`/${lang}/book/sport-club/pay/${token}`}>{id ? 'Selesaikan pembayaran' : 'Complete the payment'}</a>)}
        {live && (
          <>
            <img src={`${base}/qr.png${q}`} alt={id ? 'QR check-in' : 'Check-in QR'} width={220} height={220} style={{ margin: '12px auto', display: 'block' }} />
            <p className="w-muted">{id ? 'Tunjukkan QR ini di front desk Sport Club. Datang 15 menit sebelum jam main.' : 'Show this QR at the Sport Club front desk. Come 15 minutes before the start.'}</p>
          </>
        )}
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, justifyContent: 'center' }}>
          <a className="w-btn" href={`${base}/e-ticket.pdf${q}`} target="_blank" rel="noreferrer">E-ticket (PDF)</a>
          <a className="w-btn w-btn-ghost" href={`${base}/calendar.ics${q}`}>{id ? 'Tambah ke kalender' : 'Add to calendar'}</a>
          <button type="button" className="w-btn w-btn-ghost" onClick={() => void navigator.clipboard?.writeText(window.location.href).then(() => setCopied(true))}>
            {copied ? (id ? 'Tautan disalin' : 'Link copied') : (id ? 'Salin tautan' : 'Copy link')}</button>
        </div>
      </div>
      <div className="w-card">
        <h2>{id ? 'Detail booking' : 'Booking details'}</h2>
        <p>{b.name}</p>
        {b.lines.map((l) => (
          <div key={l.id} className="w-sc-line"><span><strong>{l.facilityName} · {l.courtName}</strong><br />
            <small className="w-muted">{dayLabel(l.start, lang, { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' })} · {hhmm(l.start)}–{hhmm(l.end)}</small></span>
            <span>{l.amount ? money(l.amount, lang) : ''}</span><span /></div>
        ))}
        <dl className="w-sc-costs">
          {v.bill.map((x, i) => <div key={i} style={{ display: 'contents' }}><dt>{x.description}</dt><dd>{money(x.total, lang)}</dd></div>)}
          <dt>{id ? 'Termasuk pajak' : 'Tax included'}</dt><dd>{money(b.tax, lang)}</dd>
          <dt><strong>Total</strong></dt><dd><strong>{money(b.charges, lang)}</strong></dd>
          <dt>{id ? 'Dibayar' : 'Paid'}</dt><dd>{money(b.paid, lang)}</dd>
          {Number(b.balance) > 0 && <><dt>{id ? 'Sisa' : 'Balance'}</dt><dd>{money(b.balance, lang)}</dd></>}
        </dl>
        <h2 style={{ marginTop: 16 }}>{id ? 'Lokasi' : 'Location'}</h2>
        <p>{addr}{v.club.phone ? <><br />{v.club.phone}</> : null}</p>
        <a className="w-btn w-btn-ghost" href={`https://www.google.com/maps/search/?api=1&query=${encodeURIComponent(addr)}`} target="_blank" rel="noreferrer">{id ? 'Buka peta' : 'Open map'}</a>
        <p className="w-sc-policy">{v.terms[lang] ?? v.terms.id}</p>
      </div>
    </div>
  );
}
