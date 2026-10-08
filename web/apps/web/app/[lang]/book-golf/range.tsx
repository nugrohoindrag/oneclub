'use client';
import { useEffect, useState } from 'react';
import type { Lang } from '../../lib';
import { BookGolf } from './book';

// Book Golf on the website: Tee Time or Driving Range (demo feedback 9 Oct
// 2026). The range is a bay and a time, or just the visit; no account is
// needed and balls are bought at the Driving Range Counter.

interface Slot { time: string; freeBays: number; bays: { id: string; code: string }[] }
interface Booked { number: string; bayCode?: string | null; holdsBay: boolean; startAt: string; endAt: string }
interface Problem { detail?: string; title?: string }

function today() {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

export function BookGolfOrRange({ lang, propertyId }: { lang: Lang; propertyId: string }) {
  const id = lang === 'id';
  const [kind, setKind] = useState<'tee' | 'range'>('tee');
  return (
    <div>
      <div className="w-form" style={{ display: 'flex', gap: 8, marginBottom: 16 }} role="tablist">
        <button type="button" className={kind === 'tee' ? 'w-btn' : 'w-btn w-btn-ghost'} onClick={() => setKind('tee')}>Tee Time</button>
        <button type="button" className={kind === 'range' ? 'w-btn' : 'w-btn w-btn-ghost'} onClick={() => setKind('range')}>Driving Range</button>
      </div>
      {kind === 'tee' ? <BookGolf lang={lang} /> : <BookRange lang={lang} propertyId={propertyId} />}
      {kind === 'range' && <p className="w-muted">{id ? 'Bola dibeli di Driving Range Counter.' : 'Balls are bought at the Driving Range Counter.'}</p>}
    </div>
  );
}

function BookRange({ lang, propertyId }: { lang: Lang; propertyId: string }) {
  const id = lang === 'id';
  const [date, setDate] = useState(today());
  const [area, setArea] = useState('outdoor');
  const [minutes, setMinutes] = useState(60);
  const [players, setPlayers] = useState(1);
  const [bay, setBay] = useState(true);
  const [slots, setSlots] = useState<Slot[] | null>(null);
  const [slot, setSlot] = useState<Slot | null>(null);
  const [guest, setGuest] = useState({ name: '', phone: '', email: '' });
  const [consent, setConsent] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [done, setDone] = useState<Booked | null>(null);

  useEffect(() => {
    setSlots(null);
    setSlot(null);
    fetch(`/api/v1/public/golf/range-availability?propertyId=${propertyId}&date=${date}&area=${area}&minutes=${minutes}`)
      .then(async (r) => {
        const d = await r.json();
        if (r.ok) setSlots((d as { items: Slot[] }).items ?? []);
        else setError((d as Problem).detail ?? 'Error');
      })
      .catch(() => setError('Network error'));
  }, [propertyId, date, area, minutes]);

  const book = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!slot) return;
    setBusy(true);
    setError('');
    const r = await fetch('/api/v1/public/golf/range-bookings', { method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ propertyId, guest, date, time: slot.time, minutes, area, players, reserveBay: bay }) });
    const d = await r.json().catch(() => ({}));
    setBusy(false);
    if (r.ok) setDone(d as Booked);
    else setError((d as Problem).detail ?? (d as Problem).title ?? `Error ${r.status}`);
  };

  const t = (iso: string) => new Date(iso).toLocaleTimeString(lang, { hour: '2-digit', minute: '2-digit' });
  if (done) {
    return (
      <div className="w-card">
        <h2>{id ? 'Driving range dipesan' : 'Driving range booked'} · {done.number}</h2>
        <p>{date} · {t(done.startAt)} – {t(done.endAt)}{done.bayCode ? ` · bay ${done.bayCode}` : ''}</p>
        <p className="w-muted">{done.holdsBay
          ? (id ? 'Bay ditahan sampai 15 menit setelah jam pesan. Datang lebih lambat? Anda mendapat bay kosong berikutnya atau masuk antrean.'
            : 'Your bay is kept until 15 minutes past the booked time. Later? You get the next free bay or a place in the queue.')
          : (id ? 'Tunjukkan nomor pemesanan di Driving Range Counter.' : 'Show the booking number at the Driving Range Counter.')}</p>
      </div>
    );
  }
  return (
    <form className="w-card" onSubmit={book}>
      <div className="w-form">
        <label>{id ? 'Jenis' : 'Kind'}
          <select value={bay ? 'bay' : 'visit'} onChange={(e) => setBay(e.target.value === 'bay')}>
            <option value="bay">{id ? 'Bay & jam' : 'Bay & time'}</option>
            <option value="visit">{id ? 'Datang saja (tanpa pesan bay)' : 'Just drop by (no bay held)'}</option>
          </select>
        </label>
        <label>{id ? 'Tanggal' : 'Date'}<input type="date" value={date} min={today()} onChange={(e) => setDate(e.target.value)} /></label>
        <label>{id ? 'Area' : 'Area'}
          <select value={area} onChange={(e) => setArea(e.target.value)}>
            <option value="outdoor">Outdoor</option><option value="indoor">Indoor</option>
          </select>
        </label>
        <label>{id ? 'Durasi' : 'Length'}
          <select value={minutes} onChange={(e) => setMinutes(Number(e.target.value))}>
            {[30, 60, 90, 120].map((n) => <option key={n} value={n}>{n} {id ? 'menit' : 'minutes'}</option>)}
          </select>
        </label>
        <label>{id ? 'Pemain' : 'Players'}
          <select value={players} onChange={(e) => setPlayers(Number(e.target.value))}>{[1, 2, 3, 4].map((n) => <option key={n} value={n}>{n}</option>)}</select>
        </label>
      </div>
      {slots === null && !error && <p className="w-muted">{id ? 'Memuat…' : 'Loading…'}</p>}
      {slots && slots.length === 0 && <p>{id ? 'Tidak ada jam tersedia pada tanggal ini.' : 'No times left on this date.'}</p>}
      <div className="w-slots" style={{ margin: '16px 0' }}>
        {slots?.filter((s) => !bay || s.freeBays > 0).map((s) => (
          <button key={s.time} type="button" className="w-slot" aria-pressed={slot?.time === s.time} onClick={() => setSlot(s)}
            style={slot?.time === s.time ? { outline: '2px solid currentColor' } : undefined}>
            <strong>{s.time}</strong>
            {bay && <div className="w-muted" style={{ fontSize: 12 }}>{s.freeBays} bay</div>}
          </button>
        ))}
      </div>
      {slot && (
        <div className="w-form">
          <label>{id ? 'Nama' : 'Name'}<input value={guest.name} onChange={(e) => setGuest({ ...guest, name: e.target.value })} required /></label>
          <label>{id ? 'No. HP / WhatsApp' : 'Mobile / WhatsApp'}<input value={guest.phone} onChange={(e) => setGuest({ ...guest, phone: e.target.value })} required /></label>
          <label>E-mail<input type="email" value={guest.email} onChange={(e) => setGuest({ ...guest, email: e.target.value })} /></label>
        </div>
      )}
      <label style={{ display: 'flex', gap: 8, margin: '16px 0' }}>
        <input type="checkbox" checked={consent} onChange={(e) => setConsent(e.target.checked)} required />
        <span>{id ? 'Saya setuju data saya diproses untuk pemesanan ini (UU PDP).' : 'I agree that my data is processed for this booking (UU PDP).'}</span>
      </label>
      {error && <p className="w-error" role="alert">{error}</p>}
      <button className="w-btn" disabled={busy || !slot || !consent || !guest.name || !guest.phone}>{id ? 'Pesan' : 'Book'}</button>
    </form>
  );
}
