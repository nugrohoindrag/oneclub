'use client';
import { useState } from 'react';
import type { Lang } from '../../../lib';
import type { PublicTournament } from '../lib';

interface Checkout { method: string; amount: string; status: string; checkoutUrl?: string | null; qrString?: string | null; vaNumber?: string | null; expiresAt?: string | null }
interface Registration { number: string; status: string; waitlistPosition?: number | null; feeTotal: string; paymentStatus: string; checkout?: Checkout | null; manageToken?: string }

const rp = (v: string) => `Rp ${Number(v).toLocaleString('id-ID')}`;

/** Website registration with an online payment (QRIS, VA, card); the manage link is shown once. */
export function RegisterTournament({ lang, propertyId, t }: { lang: Lang; propertyId: string; t: PublicTournament }) {
  const id = lang === 'id';
  const [f, setF] = useState({ name: '', phone: '', email: '', gender: '', handicapIndex: '', packageId: '', shirtSize: '', paymentMethod: 'qris', website: '' });
  const [consent, setConsent] = useState(false);
  const [publicConsent, setPublicConsent] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [done, setDone] = useState<Registration | null>(null);
  const set = (k: keyof typeof f) => (e: { target: { value: string } }) => setF({ ...f, [k]: e.target.value });
  const submit = async () => {
    setBusy(true);
    setError('');
    const body = { propertyId, guest: { name: f.name, phone: f.phone, email: f.email, website: f.website }, gender: f.gender || undefined,
      handicapIndex: f.handicapIndex, packageId: f.packageId || undefined, shirtSize: f.shirtSize, publicConsent, consent, paymentMethod: f.paymentMethod };
    const r = await fetch(`/api/v1/public/tournaments/${t.id}/registrations`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const d = await r.json().catch(() => ({}));
    setBusy(false);
    if (r.ok) setDone(d as Registration);
    else setError(d.detail ?? (id ? 'Pendaftaran gagal' : 'Registration failed'));
  };
  if (done) {
    const link = `/${lang}/tournaments/registration/${done.manageToken}`;
    return (
      <div className="w-card" style={{ marginTop: 16 }}>
        <h2>{id ? 'Terima kasih!' : 'Thank you!'} {done.number}</h2>
        <p>{done.status === 'waitlisted' ? `${id ? 'Turnamen penuh — Anda di daftar tunggu nomor' : 'The field is full — you are on the waitlist, number'} ${done.waitlistPosition}.`
          : id ? 'Anda terdaftar.' : 'You are registered.'}</p>
        {done.checkout && (
          <div>
            <p>{id ? 'Bayar biaya turnamen' : 'Pay the tournament fee'}: <strong>{rp(done.checkout.amount)}</strong>{done.checkout.expiresAt ? ` · ${id ? 'sebelum' : 'by'} ${new Date(done.checkout.expiresAt).toLocaleString()}` : ''}</p>
            {done.checkout.vaNumber && <p>Virtual Account: <strong>{done.checkout.vaNumber}</strong></p>}
            {done.checkout.qrString && <p className="w-muted" style={{ wordBreak: 'break-all' }}>QRIS: {done.checkout.qrString}</p>}
            {done.checkout.checkoutUrl && <a className="w-btn" href={done.checkout.checkoutUrl}>{id ? 'Bayar sekarang' : 'Pay now'}</a>}
          </div>
        )}
        <p>{id ? 'Simpan tautan ini untuk melihat status, start sheet, atau membatalkan:' : 'Keep this link to see your status and start, or to withdraw:'} <a href={link}>{link}</a></p>
      </div>
    );
  }
  return (
    <div className="w-card" style={{ marginTop: 16 }}>
      <h2>{id ? 'Daftar' : 'Register'}</h2>
      <div className="w-form">
        <label>{id ? 'Nama lengkap' : 'Full name'}<input value={f.name} onChange={set('name')} autoComplete="name" required /></label>
        <label>{id ? 'Telepon' : 'Phone'}<input value={f.phone} onChange={set('phone')} autoComplete="tel" /></label>
        <label>E-mail<input type="email" value={f.email} onChange={set('email')} autoComplete="email" required /></label>
        <label>{id ? 'Jenis kelamin' : 'Gender'}<select value={f.gender} onChange={set('gender')}><option value="">—</option><option value="male">{id ? 'Pria' : 'Male'}</option><option value="female">{id ? 'Wanita' : 'Female'}</option></select></label>
        <label>Handicap index<input value={f.handicapIndex} onChange={set('handicapIndex')} inputMode="decimal" /></label>
        <label>{id ? 'Paket' : 'Package'}<select value={f.packageId} onChange={set('packageId')}>
          {t.packages.map((p) => <option key={p.code} value={p.id ?? ''}>{p.name} · {rp(p.guestTotal)}</option>)}
        </select></label>
        <label>{id ? 'Ukuran kaos' : 'Shirt size'}<input value={f.shirtSize} onChange={set('shirtSize')} /></label>
        <label>{id ? 'Pembayaran' : 'Payment'}<select value={f.paymentMethod} onChange={set('paymentMethod')}>
          <option value="qris">QRIS</option><option value="virtual_account">Virtual Account</option><option value="card">{id ? 'Kartu kredit' : 'Card'}</option>
        </select></label>
        <input aria-hidden="true" tabIndex={-1} style={{ display: 'none' }} value={f.website} onChange={set('website')} name="website" autoComplete="off" />
      </div>
      <label style={{ display: 'flex', gap: 8, alignItems: 'center', marginTop: 12 }}>
        <input type="checkbox" checked={publicConsent} onChange={(e) => setPublicConsent(e.target.checked)} />
        {id ? 'Nama saya boleh tampil di leaderboard publik dan Hall of Fame' : 'My name may appear on the public leaderboard and the Hall of Fame'}
      </label>
      <label style={{ display: 'flex', gap: 8, alignItems: 'center', marginTop: 8 }}>
        <input type="checkbox" checked={consent} onChange={(e) => setConsent(e.target.checked)} />
        {id ? 'Saya menyetujui kebijakan privasi' : 'I agree to the privacy notice'}
      </label>
      {error && <p className="w-error" role="alert">{error}</p>}
      <button className="w-btn" style={{ marginTop: 12, border: 0, cursor: 'pointer' }} disabled={busy || !f.name || !f.email || !consent} onClick={() => void submit()}>
        {id ? 'Daftar & bayar' : 'Register & pay'}</button>
    </div>
  );
}
