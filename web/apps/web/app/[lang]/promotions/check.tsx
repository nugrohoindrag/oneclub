'use client';
import { useState } from 'react';
import type { Lang } from '../../lib';

export interface CodeCheck { code: string; valid: boolean; reason?: string; promotion?: { name: string } | null }

/** Checks a promo code (public, rate limited — FR-WEB-P3-07). */
export async function checkPromoCode(propertyId: string, code: string, amount?: string, businessLine?: string): Promise<CodeCheck | { error: string }> {
  const r = await fetch('/api/v1/public/promo-codes:check', { method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ propertyId, code, amount: amount || undefined, businessLine: businessLine || undefined }) });
  const d = await r.json().catch(() => ({}));
  if (r.status === 429) return { error: 'too_many' };
  if (!r.ok) return { error: String(d.detail ?? r.status) };
  return d as CodeCheck;
}

export function PromoCodeCheck({ lang, propertyId }: { lang: Lang; propertyId: string }) {
  const id = lang === 'id';
  const [code, setCode] = useState('');
  const [res, setRes] = useState<CodeCheck | { error: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const run = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setRes(await checkPromoCode(propertyId, code));
    setBusy(false);
  };
  return (
    <form className="w-card w-form" onSubmit={run}>
      <h2 style={{ marginTop: 0 }}>{id ? 'Cek kode promo' : 'Check a promo code'}</h2>
      <label htmlFor="promo-code">{id ? 'Kode promo' : 'Promo code'}<input id="promo-code" value={code} onChange={(e) => setCode(e.target.value)} required /></label>
      <button className="w-btn" disabled={busy || !code}>{busy ? '…' : id ? 'Cek' : 'Check'}</button>
      {res && 'error' in res && <p className="w-error" role="alert">{res.error === 'too_many' ? (id ? 'Terlalu banyak percobaan, coba lagi sebentar.'
        : 'Too many attempts — please try again in a minute.') : res.error}</p>}
      {res && 'valid' in res && (
        <p role="status">{res.valid ? `${id ? 'Kode berlaku' : 'Valid code'}: ${res.promotion?.name ?? ''}` : `${id ? 'Kode tidak berlaku' : 'Not valid'}: ${res.reason ?? ''}`}</p>
      )}
    </form>
  );
}
