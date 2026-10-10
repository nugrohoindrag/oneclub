'use client';
import { useState } from 'react';
import type { Lang } from '../../../../lib';
import { api } from '../shared';

/** Cek Booking (FR-42): the booking code and the phone or e-mail used → the booking page. */
export function CourtLookup({ lang, propertyId }: { lang: Lang; propertyId: string }) {
  const id = lang === 'id';
  const [code, setCode] = useState('');
  const [contact, setContact] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const find = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      const r = await api<{ token: string }>('/api/v1/public/court-bookings:lookup', { method: 'POST', body: JSON.stringify({ propertyId, code: code.trim(), contact: contact.trim() }) });
      window.location.href = `/${lang}/book/sport-club/booking/${r.token}`;
    } catch {
      setError(id ? 'Booking tidak ditemukan. Periksa kode booking dan nomor ponsel / e-mail.' : 'Booking not found. Check the booking code and the phone / e-mail.');
      setBusy(false);
    }
  };
  return (
    <form className="w-card w-form" onSubmit={find}>
      <label>{id ? 'Kode booking' : 'Booking code'}<input required value={code} onChange={(e) => setCode(e.target.value.toUpperCase())} /></label>
      <label>{id ? 'Nomor ponsel atau e-mail' : 'Phone or e-mail'}<input required value={contact} onChange={(e) => setContact(e.target.value)} /></label>
      {error && <p className="w-error" role="alert" style={{ gridColumn: '1 / -1' }}>{error}</p>}
      <div style={{ gridColumn: '1 / -1' }}><button className="w-btn" disabled={busy}>{busy ? '…' : id ? 'Cek Booking' : 'Find my booking'}</button></div>
    </form>
  );
}
