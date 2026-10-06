'use client';
import { useEffect, useState } from 'react';
import type { Lang } from '../../../lib';

// PRD P3 §16 #18: acceptance on the link with a one-time code sent to the
// contact on file, and the e-Meterai of quotations above Rp5 jt.

export interface QuotationEMeterai { status: string; serialNumber?: string | null; stampedAt?: string | null; sandbox?: boolean }

interface OtpSent { channel: 'email' | 'whatsapp'; destinationMasked: string; expiresAt: string; resendAfter: string }

const MESSAGES: Record<string, [string, string]> = {
  otp_required: ['Masukkan kode verifikasi yang kami kirim.', 'Enter the verification code we sent you.'],
  otp_not_requested: ['Minta kode verifikasi terlebih dahulu.', 'Request a verification code first.'],
  otp_invalid: ['Kode tidak sesuai.', 'The code is not correct.'],
  otp_expired: ['Kode sudah kedaluwarsa. Minta kode baru.', 'The code has expired. Request a new code.'],
  otp_locked: ['Terlalu banyak kode salah. Minta kode baru.', 'Too many wrong codes. Request a new code.'],
  otp_resend_too_soon: ['Tunggu sebentar sebelum meminta kode baru.', 'Please wait a moment before requesting a new code.'],
  otp_request_limit: ['Terlalu banyak permintaan kode hari ini. Hubungi sales kami.', 'Too many codes were requested today. Please contact our sales.'],
  no_contact: ['Kontak Anda belum tercatat. Minta sales kami mengirim ulang tautan atau mencatat persetujuan Anda.',
    'We have no contact on file for you. Ask our sales to resend the link or to record your acceptance.'],
  rate_limited: ['Terlalu banyak percobaan. Coba lagi nanti.', 'Too many attempts. Please try again later.'],
};

const problem = (d: { code?: string; detail?: string } | null, id: boolean) => {
  const m = d?.code ? MESSAGES[d.code] : undefined;
  if (m) return d?.code === 'otp_invalid' && d.detail ? `${m[id ? 0 : 1]} (${d.detail.replace(/^.*\(|\)$/g, '')})` : m[id ? 0 : 1];
  return d?.detail ?? (id ? 'Terjadi kesalahan' : 'Something went wrong');
};

const clockOf = (s: number) => `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;

/** Two-step acceptance: send a one-time code to the contact on file, then accept with name, terms and the code. */
export function OtpAccept<T>({ lang, token, name, agree, onAccepted }: {
  lang: Lang; token: string; name: string; agree: boolean; onAccepted: (q: T) => void;
}) {
  const id = lang === 'id';
  const [sent, setSent] = useState<OtpSent | null>(null);
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!sent) return undefined;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [sent]);
  const post = async (path: string, body?: unknown) => {
    setBusy(true);
    setError('');
    try {
      const r = await fetch(`/api/v1/public/quotations/${token}:${path}`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body),
      });
      const d = await r.json().catch(() => null);
      if (!r.ok) setError(problem(d, id));
      return r.ok ? d : null;
    } catch {
      setError(id ? 'Gangguan jaringan' : 'Network error');
      return null;
    } finally {
      setBusy(false);
    }
  };
  const request = async () => {
    const d = await post('request-otp');
    if (d) {
      setSent(d as OtpSent);
      setCode('');
      setNow(Date.now());
    }
  };
  const accept = async () => {
    const d = await post('accept', { name, termsAccepted: agree, otpCode: code });
    if (d) onAccepted(d as T);
  };
  const resendIn = sent ? Math.max(0, Math.ceil((Date.parse(sent.resendAfter) - now) / 1000)) : 0;
  const expiresIn = sent ? Math.max(0, Math.ceil((Date.parse(sent.expiresAt) - now) / 1000)) : 0;
  const via = sent?.channel === 'whatsapp' ? 'WhatsApp' : 'e-mail';
  if (!sent) {
    return (
      <div className="w-form">
        <p className="w-muted">{id
          ? 'Untuk keamanan, kami mengirim kode verifikasi ke kontak Anda yang tercatat (WhatsApp atau e-mail) sebelum penawaran disetujui.'
          : 'For your security we send a verification code to your contact on file (WhatsApp or e-mail) before the quotation is accepted.'}</p>
        <div>
          <button className="w-btn" disabled={busy || !name.trim() || !agree} onClick={() => void request()}>
            {id ? 'Kirim kode verifikasi' : 'Send verification code'}
          </button>
        </div>
        {error && <p className="w-error" role="alert">{error}</p>}
      </div>
    );
  }
  return (
    <div className="w-form">
      <p aria-live="polite">{id
        ? <>Kode 6 digit telah dikirim lewat {via} ke <strong>{sent.destinationMasked}</strong>. {expiresIn > 0 ? <>Berlaku {clockOf(expiresIn)}.</> : 'Kode sudah kedaluwarsa.'}</>
        : <>We sent a 6-digit code by {via} to <strong>{sent.destinationMasked}</strong>. {expiresIn > 0 ? <>Valid for {clockOf(expiresIn)}.</> : 'The code has expired.'}</>}</p>
      <label>{id ? 'Kode verifikasi' : 'Verification code'}
        <input value={code} onChange={(e) => setCode(e.target.value.replace(/\D/g, '').slice(0, 6))} inputMode="numeric" autoComplete="one-time-code"
          pattern="[0-9]{6}" maxLength={6} aria-describedby="otp-help" />
      </label>
      <p id="otp-help" className="w-muted">{id ? 'Jangan bagikan kode ini kepada siapa pun, termasuk staf kami.' : 'Never share this code, not even with our staff.'}</p>
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
        <button className="w-btn" disabled={busy || code.length !== 6 || !name.trim() || !agree} onClick={() => void accept()}>
          {id ? 'Setujui penawaran' : 'Accept quotation'}
        </button>
        <button className="w-btn w-btn-ghost" disabled={busy || resendIn > 0} onClick={() => void request()}>
          {resendIn > 0 ? (id ? `Kirim ulang (${resendIn} dtk)` : `Resend code (${resendIn} s)`) : (id ? 'Kirim ulang kode' : 'Resend code')}
        </button>
      </div>
      {error && <p className="w-error" role="alert">{error}</p>}
    </div>
  );
}

/** e-Meterai of the quotation: the notice before acceptance, the stamp after. */
export function EMeteraiNotice({ lang, required, eMeterai, accepted }: {
  lang: Lang; required: boolean; eMeterai?: QuotationEMeterai | null; accepted: boolean;
}) {
  const id = lang === 'id';
  if (!required) return null;
  if (eMeterai?.status === 'stamped' && eMeterai.serialNumber) {
    return (
      <div className="w-card" style={{ marginTop: 16 }}>
        <strong>e-Meterai{eMeterai.sandbox ? (id ? ' · MOCK / UJI COBA (tidak berlaku hukum)' : ' · MOCK / TRIAL (no legal value)') : ''}</strong>
        <p>{id ? 'No. seri' : 'Serial no.'} {eMeterai.serialNumber}{eMeterai.stampedAt
          ? ` · ${new Date(eMeterai.stampedAt).toLocaleString(id ? 'id-ID' : 'en-GB')}` : ''}</p>
      </div>
    );
  }
  return (
    <p className="w-muted">{accepted
      ? (id ? 'e-Meterai sedang diproses dan akan dibubuhkan oleh tim kami.' : 'The e-Meterai is being processed and will be applied by our team.')
      : (id ? 'Penawaran ini bernilai di atas Rp5 juta: e-Meterai akan dibubuhkan saat disetujui.' : 'This quotation is above Rp5 million: an e-Meterai will be applied on acceptance.')}</p>
  );
}
