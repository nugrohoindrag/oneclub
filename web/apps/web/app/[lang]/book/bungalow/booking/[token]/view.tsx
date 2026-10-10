'use client';
import { useCallback, useEffect, useState } from 'react';
import type { Lang } from '../../../../../lib';
import { METHOD, PAY_STATUS, STATUS, api, cancelPolicy, dayLabel, money, taxLabel, writeCart, type BookingStay, type BookingView } from '../../shared';

/*
 * Reservasi Terkonfirmasi and Cek Booking of a link (docs/requirement-
 * booking-hotel-mgcc.md FR-H38–H43): code, booker, bungalows with their
 * status (§12.3) and payment, totals, deposit and rest, cancellation policy,
 * check-in / check-out, address and map; the e-voucher PDF, the calendar,
 * the link to copy, My Stay; the payment continued while held and the
 * cancellation allowed by the rate plan with its fee shown first.
 */
export function BookingPage({ lang, propertyId, token }: { lang: Lang; propertyId: string; token: string }) {
  const id = lang === 'id';
  const [v, setV] = useState<BookingView | null>(null);
  const [error, setError] = useState('');
  const [copied, setCopied] = useState(false);
  const [cancel, setCancel] = useState<BookingStay | null>(null);
  const [reason, setReason] = useState('');
  const [method, setMethod] = useState('');
  const [busy, setBusy] = useState(false);
  const base = `/api/v1/public/stay-bookings/${encodeURIComponent(token)}`;
  const load = useCallback(async () => {
    try {
      const x = await api<BookingView>(`${base}?propertyId=${propertyId}`);
      setV(x);
      if (x.status !== 'awaiting_payment' && x.status !== 'expired') writeCart(null);
    } catch (e) {
      setError((e as Error).message);
    }
  }, [base, propertyId]);
  useEffect(() => { void load(); }, [load]);
  if (error && !v) return <p className="w-error" role="alert">{error}</p>;
  if (!v) return <p>{id ? 'Memuat…' : 'Loading…'}</p>;
  const link = typeof window === 'undefined' ? '' : `${window.location.origin}/${lang}/book/bungalow/booking/${token}`;
  const file = (f: string) => `${base}/${f}?propertyId=${propertyId}&lang=${lang}`;
  const doCancel = async () => {
    if (!cancel) return;
    setBusy(true);
    try {
      setV(await api<BookingView>(`${base}:cancel`, { method: 'POST', body: JSON.stringify({ propertyId, stayIds: [cancel.id], reason }) }));
      setCancel(null);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  const payAgain = async () => {
    setBusy(true);
    try {
      await api<BookingView>(`${base}:pay`, { method: 'POST', body: JSON.stringify({ propertyId, method: method || v.methods[0] }) });
      window.location.href = `/${lang}/book/bungalow/pay/${token}`;
    } catch (e) {
      setError((e as Error).message);
      setBusy(false);
    }
  };
  const status = STATUS[v.status]?.[id ? 0 : 1] ?? v.status;
  return (
    <div className="w-sc-checkout">
      <div>
        <div className="w-card" role="status">
          <p className="w-muted">{id ? 'Kode reservasi' : 'Reservation code'}</p>
          <p><strong className="w-sc-code" style={{ fontSize: 24 }}>{v.code}</strong> <span className="w-sc-state" data-status={v.status}>{status}</span></p>
          <h2>{v.status === 'confirmed' ? (id ? 'Reservasi Terkonfirmasi' : 'Reservation confirmed')
            : v.status === 'awaiting_payment' ? (id ? 'Menunggu pembayaran' : 'Awaiting payment') : status}</h2>
          <p>{id ? 'Atas nama' : 'Booked by'} <strong>{v.bookerName}</strong>{v.bookerEmail ? ` · ${v.bookerEmail}` : ''}{v.bookerPhone ? ` · ${v.bookerPhone}` : ''}</p>
          <div className="w-bk-nav" style={{ justifyContent: 'flex-start' }}>
            <a className="w-btn" href={file('e-voucher.pdf')}>{id ? 'Unduh E-voucher (PDF)' : 'Download e-voucher (PDF)'}</a>
            <a className="w-btn w-btn-ghost" href={file('calendar.ics')}>{id ? 'Tambah ke Kalender' : 'Add to calendar'}</a>
            <button type="button" className="w-btn w-btn-ghost" onClick={() => { void navigator.clipboard?.writeText(link); setCopied(true); }}>
              {copied ? (id ? 'Tautan disalin' : 'Link copied') : (id ? 'Salin tautan' : 'Copy link')}</button>
            <a className="w-btn w-btn-ghost" href={`/${lang}/my-stay?token=${token}`}>My Stay</a>
          </div>
          <p className="w-muted w-bk-small">{id ? 'Simpan tautan ini: siapa pun yang memilikinya dapat melihat reservasi. Konfirmasi via e-mail/WhatsApp belum aktif.'
            : 'Keep this link: whoever has it can see the reservation. E-mail / WhatsApp confirmations are not active yet.'}</p>
        </div>
        {v.canPay && (
          <div className="w-card">
            <h3>{id ? 'Lanjutkan Pembayaran' : 'Continue the payment'}</h3>
            <p>{id ? 'Deposit' : 'Deposit'} <strong>{money(v.depositDue, lang)}</strong> · {id ? 'sisa waktu' : 'time left'} {Math.floor(v.holdSeconds / 60)} {id ? 'menit' : 'min'}</p>
            <div className="w-sc-methods">
              {v.methods.map((m) => (
                <label key={m} className="w-sc-method" data-on={(method || v.methods[0]) === m || undefined}>
                  <input type="radio" name="m" checked={(method || v.methods[0]) === m} onChange={() => setMethod(m)} /><span>{METHOD[m]?.[id ? 0 : 1] ?? m}</span>
                </label>
              ))}
            </div>
            <button type="button" className="w-btn" disabled={busy} onClick={() => void payAgain()}>{id ? 'Lanjutkan Pembayaran' : 'Continue to pay'}</button>
          </div>
        )}
        {v.stays.map((s) => (
          <div key={s.id} className="w-card">
            <h3>{s.typeName} · {s.ratePlanName}</h3>
            <p className="w-muted w-bk-small">{s.stayNo} · {STATUS[s.bookingStatus]?.[id ? 0 : 1] ?? s.bookingStatus} · {PAY_STATUS[s.paymentStatus]?.[id ? 0 : 1] ?? s.paymentStatus}</p>
            <p>{dayLabel(s.arrival, lang)} ({id ? 'check-in' : 'check-in'} {v.checkInTime}) – {dayLabel(s.departure, lang)} (check-out {v.checkOutTime}) · {s.nights} {id ? 'malam' : 'nights'}</p>
            <p>{s.adults} {id ? 'dewasa' : 'adults'}{s.children ? `, ${s.children} ${id ? 'anak' : 'children'}` : ''}{s.occupantName ? ` · ${id ? 'tamu' : 'guest'}: ${s.occupantName}` : ''}
              {' · '}{s.includesBreakfast ? (id ? 'termasuk sarapan' : 'breakfast included') : (id ? 'tanpa sarapan' : 'room only')}{s.unitName ? ` · ${s.unitName}` : ''}</p>
            {s.addons.filter((a) => !a.voidedAt).length > 0 && <p className="w-bk-small">Add-on: {s.addons.filter((a) => !a.voidedAt).map((a) => `${a.name} × ${a.quantity}`).join(', ')}</p>}
            <p className="w-bk-small">{cancelPolicy(s, lang)}{s.freeCancelUntil && !s.nonRefundable ? ` (${id ? 'gratis sampai' : 'free until'} ${dayLabel(s.freeCancelUntil, lang, { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })})` : ''}</p>
            <p><strong>{money(s.total, lang)}</strong> <span className="w-muted w-bk-small">{taxLabel(s.taxIncluded, lang)} · {id ? 'dibayar' : 'paid'} {money(s.paid, lang)}</span></p>
            {s.canCancel && <button type="button" className="w-btn w-btn-ghost" onClick={() => { setCancel(s); setReason(''); }}>{id ? 'Batalkan bungalow ini' : 'Cancel this bungalow'}</button>}
            {!s.canCancel && s.bookingStatus === 'confirmed' && s.nonRefundable && <p className="w-muted w-bk-small">{id ? 'Tarif non-refundable tidak dapat dibatalkan.' : 'A non-refundable rate cannot be cancelled.'}</p>}
          </div>
        ))}
        <p className="w-muted w-bk-small">{id ? 'Ingin mengubah tanggal? Hubungi front desk.' : 'Want to change the dates? Contact the front desk.'}</p>
        {error && <p className="w-error" role="alert">{error}</p>}
      </div>
      <div>
        <div className="w-card">
          <h3>{id ? 'Pembayaran' : 'Payment'}</h3>
          <dl className="w-sc-costs">
            <dt><strong>Total</strong></dt><dd><strong>{money(v.total, lang)}</strong></dd>
            <dt>{id ? 'Dibayar (termasuk deposit)' : 'Paid (incl. deposit)'}</dt><dd>{money(v.paid, lang)}</dd>
            <dt>{id ? 'Sisa dibayar di hotel' : 'Left to pay at the hotel'}</dt><dd>{money(v.balance, lang)}</dd>
          </dl>
        </div>
        <div className="w-card">
          <h3>{v.contact.name}</h3>
          {v.contact.address && <p>{v.contact.address}</p>}
          <p className="w-bk-small">{[v.contact.phone && `${id ? 'Telp' : 'Phone'} ${v.contact.phone}`, v.contact.email, v.contact.whatsApp && `WhatsApp ${v.contact.whatsApp}`]
            .filter(Boolean).join(' · ')}</p>
          <a className="w-bk-link" href={v.contact.mapUrl || `https://maps.google.com/?q=${encodeURIComponent(`${v.contact.name} ${v.contact.address}`)}`} target="_blank" rel="noreferrer">
            {id ? 'Buka peta' : 'Open the map'}</a>
          <h4>{id ? 'Aturan rumah' : 'House rules'}</h4>
          <p className="w-bk-small" style={{ whiteSpace: 'pre-wrap' }}>{id ? v.houseRules : v.houseRulesEn || v.houseRules}</p>
        </div>
      </div>
      {cancel && (
        <div className="w-bk-sheet" role="dialog" aria-modal="true" onClick={() => setCancel(null)}>
          <div className="w-bk-sheet-body w-bk-modal" onClick={(e) => e.stopPropagation()}>
            <button type="button" className="w-bk-close" onClick={() => setCancel(null)} aria-label={id ? 'Tutup' : 'Close'}>×</button>
            <h3>{id ? 'Batalkan' : 'Cancel'} {cancel.typeName}</h3>
            <p>{cancelPolicy(cancel, lang)}</p>
            <p>{id ? 'Biaya pembatalan bila dibatalkan sekarang' : 'Cancellation fee if cancelled now'}: <strong>{money(cancel.cancelFee, lang)}</strong></p>
            <p className="w-muted w-bk-small">{id ? 'Pengembalian dana (bila ada) diproses oleh Finance ke metode pembayaran Anda.' : 'Any refund is processed by Finance to your payment method.'}</p>
            <label>{id ? 'Alasan (opsional)' : 'Reason (optional)'}<input value={reason} onChange={(e) => setReason(e.target.value)} /></label>
            <div className="w-bk-nav">
              <button type="button" className="w-btn w-btn-ghost" onClick={() => setCancel(null)}>{id ? 'Tidak jadi' : 'Keep it'}</button>
              <button type="button" className="w-btn" disabled={busy} onClick={() => void doCancel()}>{id ? 'Ya, batalkan' : 'Yes, cancel'}</button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
