'use client';
import { useState } from 'react';
import type { Lang } from '../../../../lib';
import { api } from '../shared';

/** Cek Booking form: the code and the phone or e-mail of the booker lead to the booking page. */
export function CheckBooking({ lang, propertyId }: { lang: Lang; propertyId: string }) {
  const id = lang === 'id';
  const [v, setV] = useState({ code: '', contact: '' });
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const go = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      const r = await api<{ token: string }>('/api/v1/public/stay-bookings:lookup', { method: 'POST', body: JSON.stringify({ propertyId, ...v }) });
      window.location.href = `/${lang}/book/bungalow/booking/${r.token}`;
    } catch (err) {
      setError((err as Error & { status?: number }).status === 404
        ? (id ? 'Reservasi tidak ditemukan. Periksa kode dan nomor HP / e-mail pemesan.' : 'Reservation not found. Check the code and the phone / e-mail of the booker.')
        : (err as Error).message);
      setBusy(false);
    }
  };
  return (
    <form className="w-card w-form" onSubmit={(e) => void go(e)}>
      <p style={{ gridColumn: '1 / -1' }}>{id ? 'Masukkan kode reservasi (GRP-…, RSV-… atau STY-…) dan nomor HP atau e-mail pemesan.'
        : 'Enter the reservation code (GRP-…, RSV-… or STY-…) and the phone or e-mail of the booker.'}</p>
      <label>{id ? 'Kode reservasi' : 'Reservation code'}<input value={v.code} onChange={(e) => setV({ ...v, code: e.target.value })} required /></label>
      <label>{id ? 'HP atau e-mail' : 'Phone or e-mail'}<input value={v.contact} onChange={(e) => setV({ ...v, contact: e.target.value })} required /></label>
      {error && <p className="w-error" role="alert">{error}</p>}
      <div><button type="submit" className="w-btn" disabled={busy}>{id ? 'Cek Booking' : 'Check'}</button></div>
    </form>
  );
}
