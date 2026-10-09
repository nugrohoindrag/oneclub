'use client';
import { useCallback, useEffect, useState } from 'react';
import { checkoutHref } from '../../../../pay-link';

interface Start { flightNo: number; startLabel: string; localTime: string; roundNo: number }
interface Registration {
  number: string; tournamentId: string; tournamentName: string; startDate: string; playerName: string; status: string; waitlistPosition?: number | null;
  packageName?: string | null; feeTotal: string; balance: string; paymentStatus: string; paymentDueAt?: string | null;
  checkout?: { amount: string; checkoutUrl?: string | null; vaNumber?: string | null; qrString?: string | null } | null;
  start?: Start | null; canWithdraw: boolean; withdrawUntil?: string | null; refundPercent: string;
}

const rp = (v: string) => `Rp ${Number(v).toLocaleString('id-ID')}`;

export function ManageRegistration({ lang, propertyId, token }: { lang: string; propertyId: string; token: string }) {
  const id = lang === 'id';
  const [r, setR] = useState<Registration | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const load = useCallback(() => {
    fetch(`/api/v1/public/tournament-registrations/${token}?propertyId=${propertyId}`).then(async (x) => {
      const d = await x.json().catch(() => ({}));
      if (x.ok) setR(d as Registration);
      else setError(d.detail ?? (id ? 'Pendaftaran tidak ditemukan' : 'Registration not found'));
    }).catch(() => setError(id ? 'Gangguan jaringan' : 'Network error'));
  }, [token, propertyId, id]);
  useEffect(load, [load]);
  const withdraw = async () => {
    setBusy(true);
    const x = await fetch(`/api/v1/public/tournament-registrations/${token}:withdraw?propertyId=${propertyId}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
    const d = await x.json().catch(() => ({}));
    setBusy(false);
    if (x.ok) setR(d as Registration);
    else setError(d.detail ?? 'Error');
  };
  if (!r) return error ? <p className="w-error">{error}</p> : <p className="w-muted">{id ? 'Memuat…' : 'Loading…'}</p>;
  return (
    <div className="w-card">
      <h2>{r.tournamentName} · {r.number}</h2>
      <p>{r.playerName} · {r.startDate}</p>
      <p><strong>{r.status.replace(/_/g, ' ')}</strong>{r.waitlistPosition ? ` #${r.waitlistPosition}` : ''} · {r.packageName ?? ''} · {rp(r.feeTotal)} ({r.paymentStatus.replace(/_/g, ' ')})</p>
      {r.checkout && Number(r.balance) > 0 && (
        <p>{id ? 'Sisa pembayaran' : 'To pay'}: <strong>{rp(r.balance)}</strong>{r.checkout.vaNumber ? ` · VA ${r.checkout.vaNumber}` : ''}
          {r.checkout.checkoutUrl && <> · <a href={checkoutHref(r.checkout.checkoutUrl, lang) ?? undefined}>{id ? 'Bayar' : 'Pay'}</a></>}</p>
      )}
      {r.start && <p>{id ? 'Start' : 'Start'}: {id ? 'ronde' : 'round'} {r.start.roundNo} · flight {r.start.flightNo} · {r.start.startLabel} · {r.start.localTime}</p>}
      <p><a href={`/${lang}/tournaments/${r.tournamentId}`}>{id ? 'Detail turnamen' : 'Tournament details'}</a></p>
      {r.canWithdraw && (
        <button className="w-btn w-btn-ghost" style={{ border: 0, cursor: 'pointer' }} disabled={busy} onClick={() => void withdraw()}>
          {id ? `Batalkan (refund ${r.refundPercent}%)` : `Withdraw (refund ${r.refundPercent}%)`}</button>
      )}
      {error && <p className="w-error" role="alert">{error}</p>}
    </div>
  );
}
