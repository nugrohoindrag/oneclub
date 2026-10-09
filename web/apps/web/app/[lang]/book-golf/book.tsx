'use client';
import { useEffect, useState } from 'react';
import { MoneyInput } from '@oneclub/ui';
import type { Lang } from '../../lib';
import { checkoutHref } from '../../pay-link';

interface Slot { id: string; localTime: string; startTee: number; remaining: number; minPlayers: number; maxPlayers: number; session: string; status: string; crowd?: string; prices: Record<string, string> }
interface Hold { id: string; holdToken: string; expiresAt: string; players: number }
interface Problem { detail?: string; title?: string }

async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  const r = await fetch(path, { method, headers: body ? { 'Content-Type': 'application/json' } : undefined, body: body ? JSON.stringify(body) : undefined });
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error((data as Problem).detail ?? (data as Problem).title ?? `Error ${r.status}`);
  return data as T;
}

function tomorrow() {
  const d = new Date();
  d.setDate(d.getDate() + 1);
  return d.toISOString().slice(0, 10);
}

const fmt = (v: string, lang: Lang) => new Intl.NumberFormat(lang === 'id' ? 'id-ID' : 'en-US', { style: 'currency', currency: 'IDR', maximumFractionDigits: 0 }).format(Number(v));

export function BookGolf({ lang }: { lang: Lang }) {
  const id = lang === 'id';
  const [date, setDate] = useState(tomorrow());
  const [players, setPlayers] = useState(2);
  const [slots, setSlots] = useState<Slot[] | null>(null);
  const [slot, setSlot] = useState<Slot | null>(null);
  const [hold, setHold] = useState<Hold | null>(null);
  const [contact, setContact] = useState({ name: '', phone: '', email: '' });
  const [others, setOthers] = useState<string[]>([]);
  const [method, setMethod] = useState('qris');
  // pay in full now, pay part now, or pay at the club after the round
  const [when, setWhen] = useState<'prepaid' | 'deposit' | 'pay_at_venue'>('prepaid');
  const [part, setPart] = useState('');
  const [consent, setConsent] = useState(false);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    setSlots(null);
    setError('');
    api<{ items: Slot[] }>('GET', `/api/v1/public/golf/availability?date=${date}&players=${players}`)
      .then((r) => setSlots(r.items ?? []))
      .catch((e: Error) => setError(e.message));
  }, [date, players]);

  const choose = async (s: Slot) => {
    setBusy(true);
    setError('');
    try {
      const h = await api<Hold>('POST', '/api/v1/public/golf/holds', { teeTimeId: s.id, players, channel: 'website' });
      setSlot(s);
      setHold(h);
      setOthers(Array.from({ length: players - 1 }, () => ''));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const book = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!hold) return;
    setBusy(true);
    setError('');
    try {
      const b = await api<{ manageToken: string; payment?: { checkoutUrl?: string | null } }>('POST', '/api/v1/public/golf/bookings', {
        holdId: hold.id, holdToken: hold.holdToken, consent, contact, players: others.map((n) => ({ name: n })),
        paymentMode: when, paymentMethod: when === 'pay_at_venue' ? undefined : method, depositAmount: when === 'deposit' ? part : undefined,
      });
      if (b.payment?.checkoutUrl) window.location.href = checkoutHref(b.payment.checkoutUrl, lang, `/${lang}/booking/${b.manageToken}`) ?? b.payment.checkoutUrl;
      else window.location.href = `/${lang}/booking/${b.manageToken}`;
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };

  if (hold && slot) {
    const total = Number(slot.prices.guest ?? slot.prices.non_member ?? Object.values(slot.prices)[0] ?? 0) * players;
    const partOk = when !== 'deposit' || (Number(part) > 0 && Number(part) <= total);
    const WHEN: [typeof when, string][] = [
      ['prepaid', id ? 'Bayar penuh sekarang' : 'Pay in full now'],
      ['deposit', id ? 'Bayar sebagian sekarang' : 'Pay part now'],
      ['pay_at_venue', id ? 'Bayar di klub (setelah bermain)' : 'Pay at the club (after the round)'],
    ];
    return (
      <form className="w-card" onSubmit={book}>
        <h2>{date} · {slot.localTime} · {players} {id ? 'pemain' : 'players'}</h2>
        <p className="w-muted">{id ? 'Tee time ditahan sampai' : 'Tee time held until'} {new Date(hold.expiresAt).toLocaleTimeString(lang)}.</p>
        <div className="w-form">
          <label>{id ? 'Nama' : 'Name'}<input value={contact.name} onChange={(e) => setContact({ ...contact, name: e.target.value })} required /></label>
          <label>{id ? 'No. HP / WhatsApp' : 'Mobile / WhatsApp'}<input value={contact.phone} onChange={(e) => setContact({ ...contact, phone: e.target.value })} required /></label>
          <label>E-mail<input type="email" value={contact.email} onChange={(e) => setContact({ ...contact, email: e.target.value })} required /></label>
          {others.map((n, i) => (
            <label key={i}>{id ? `Pemain ${i + 2} (opsional)` : `Player ${i + 2} (optional)`}<input value={n} onChange={(e) => setOthers(others.map((x, j) => (j === i ? e.target.value : x)))} /></label>
          ))}
          <label>{id ? 'Kapan membayar' : 'When to pay'}
            <select value={when} onChange={(e) => setWhen(e.target.value as typeof when)}>
              {WHEN.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
            </select>
          </label>
          {when === 'deposit' && (
            <label>{id ? 'Nominal dibayar sekarang (Rp)' : 'Amount paid now (IDR)'}
              <MoneyInput value={part} onChange={setPart} required />
            </label>
          )}
          {when !== 'pay_at_venue' && (
            <label>{id ? 'Metode pembayaran' : 'Payment method'}
              <select value={method} onChange={(e) => setMethod(e.target.value)}>
                <option value="qris">QRIS</option><option value="virtual_account">Virtual Account</option><option value="card">{id ? 'Kartu kredit' : 'Card'}</option>
              </select>
            </label>
          )}
        </div>
        <label style={{ display: 'flex', gap: 8, margin: '16px 0' }}>
          <input type="checkbox" checked={consent} onChange={(e) => setConsent(e.target.checked)} required />
          <span>{id ? 'Saya setuju data saya diproses untuk pemesanan ini (UU PDP).' : 'I agree that my data is processed for this booking (UU PDP).'}</span>
        </label>
        <p><strong>{id ? 'Perkiraan total' : 'Estimated total'}: {fmt(String(total), lang)}</strong></p>
        <p className="w-muted">{id ? 'Caddy sudah termasuk dalam harga dan dipilihkan oleh front desk. Sisa tagihan bisa dibayar lewat link booking atau di front desk.'
          : 'A caddy is included in the rate and assigned by the front desk. Any balance can be paid from the booking link or at the front desk.'}</p>
        {error && <p className="w-error" role="alert">{error}</p>}
        <button className="w-btn" disabled={busy || !consent || !partOk}>{when === 'pay_at_venue' ? (id ? 'Pesan' : 'Book') : (id ? 'Bayar & pesan' : 'Pay & book')}</button>
        <button type="button" className="w-btn w-btn-ghost" onClick={() => { setHold(null); setSlot(null); }}>{id ? 'Ganti tee time' : 'Change tee time'}</button>
      </form>
    );
  }

  return (
    <div className="w-card">
      <div className="w-form">
        <label>{id ? 'Tanggal' : 'Date'}<input type="date" value={date} min={tomorrow()} onChange={(e) => setDate(e.target.value)} /></label>
        <label>{id ? 'Pemain' : 'Players'}
          <select value={players} onChange={(e) => setPlayers(Number(e.target.value))}>{[1, 2, 3, 4].map((n) => <option key={n} value={n}>{n}</option>)}</select>
        </label>
      </div>
      {error && <p className="w-error" role="alert">{error}</p>}
      {slots === null && !error && <p className="w-muted">{id ? 'Memuat…' : 'Loading…'}</p>}
      {slots && slots.length === 0 && <p>{id ? 'Tidak ada tee time tersedia pada tanggal ini.' : 'No tee times available on this date.'}</p>}
      {slots && slots.length > 0 && <p className="w-muted">{id ? 'Merah: jam sibuk (peak), mungkin antre. Hijau: sepi. Starter melepas flight sesuai urutan datang.'
        : 'Red: peak time, you may queue. Green: quiet. The starter sends flights out first come, first served.'}</p>}
      <div className="w-slots" style={{ marginTop: 16 }}>
        {slots?.filter((s) => s.status !== 'blocked' && players >= (s.minPlayers ?? 1) && players <= (s.maxPlayers || 4)).map((s) => (
          <button key={s.id} className="w-slot" data-crowd={s.crowd} disabled={busy} onClick={() => void choose(s)}
            title={s.crowd === 'peak' ? (id ? 'Jam sibuk: mungkin antre' : 'Peak time: you may queue') : (id ? 'Sepi' : 'Quiet')}>
            <strong>{s.localTime}</strong>
            <div className="w-muted" style={{ fontSize: 12 }}>{s.prices.guest ? fmt(s.prices.guest, lang) : ''}</div>
          </button>
        ))}
      </div>
    </div>
  );
}
