'use client';
import { useEffect, useState } from 'react';
import type { Lang } from '../../../lib';

interface Info { channel: string; address: string; campaign: string; status: 'subscribed' | 'unsubscribed' }

export function Unsubscribe({ lang, token }: { lang: Lang; token: string }) {
  const id = lang === 'id';
  const [info, setInfo] = useState<Info | null>(null);
  const [error, setError] = useState('');
  const [all, setAll] = useState(false);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    fetch(`/api/v1/public/unsubscribe/${token}`).then(async (r) => {
      const d = await r.json();
      if (r.ok) setInfo(d as Info);
      else setError(d.detail ?? (id ? 'Tautan tidak ditemukan' : 'Link not found'));
    }).catch(() => setError(id ? 'Gangguan jaringan' : 'Network error'));
  }, [token, id]);
  const submit = async () => {
    setBusy(true);
    setError('');
    const r = await fetch(`/api/v1/public/unsubscribe/${token}`, { method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ allChannels: all }) });
    const d = await r.json();
    setBusy(false);
    if (r.ok) setInfo(d as Info);
    else setError(d.detail ?? 'Error');
  };
  if (!info) return error ? <p className="w-error">{error}</p> : <p className="w-muted">{id ? 'Memuat…' : 'Loading…'}</p>;
  const channel = info.channel === 'whatsapp' ? 'WhatsApp' : info.channel === 'email' ? 'e-mail' : (id ? 'aplikasi' : 'in-app');
  return (
    <div className="w-card">
      <p>{id ? 'Pesan' : 'Message'}: <strong>{info.campaign}</strong> · {channel} {info.address}</p>
      {info.status === 'unsubscribed' ? (
        <p role="status">{id ? 'Anda tidak akan menerima penawaran lagi melalui kanal ini. Pesan layanan (booking, pembayaran) tetap dikirim.'
          : 'You will no longer receive offers on this channel. Service messages (bookings, payments) are still sent.'}</p>
      ) : (
        <div className="w-form">
          <label style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
            <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} />
            {id ? 'Berhenti dari semua kanal (e-mail, WhatsApp, aplikasi)' : 'Stop all channels (e-mail, WhatsApp, app)'}
          </label>
          <button className="w-btn" disabled={busy} onClick={submit}>{id ? 'Berhenti berlangganan' : 'Unsubscribe'}</button>
        </div>
      )}
      {error && <p className="w-error" role="alert">{error}</p>}
    </div>
  );
}
