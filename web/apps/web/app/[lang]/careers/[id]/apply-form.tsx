'use client';
import { useState } from 'react';

const EDUCATION: [string, string, string][] = [
  ['sma', 'SMA / SMK', 'High school'], ['d1', 'D1', 'Diploma 1'], ['d3', 'D3', 'Diploma 3'], ['s1', 'S1', "Bachelor's"], ['s2', 'S2', "Master's"],
  ['smp', 'SMP', 'Junior high'], ['other', 'Lainnya', 'Other'],
];

const MAX_MB = 5;

function toBase64(f: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(String(r.result).split(',')[1] ?? '');
    r.onerror = () => reject(r.error);
    r.readAsDataURL(f);
  });
}

/**
 * Website job application (PRD P5 FR-RCT-05): the consent to process the
 * application is required and never pre-checked (UU PDP); the talent pool
 * consent is optional; the honeypot field "website" stays hidden.
 */
export function ApplyForm({ propertyId, requisitionId, lang }: { propertyId: string; requisitionId: string; lang: string }) {
  const id = lang === 'id';
  const [v, setV] = useState<Record<string, string>>({
    fullName: '', email: '', phone: '', city: '', education: '', currentTitle: '', experienceYears: '', coverLetter: '', website: '',
  });
  const [cv, setCv] = useState<File | null>(null);
  const [consent, setConsent] = useState(false);
  const [pool, setPool] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [done, setDone] = useState<string | null>(null);
  const set = (k: string) => (e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>) => setV({ ...v, [k]: e.target.value });
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    if (cv && cv.size > MAX_MB * 1024 * 1024) {
      setError(id ? `CV maksimal ${MAX_MB} MB.` : `The CV may be at most ${MAX_MB} MB.`);
      return;
    }
    setBusy(true);
    try {
      const body: Record<string, unknown> = { propertyId, requisitionId, consent, talentPoolConsent: pool };
      for (const [k, x] of Object.entries(v)) if (x.trim()) body[k] = x.trim();
      if (cv) body.cv = { filename: cv.name, contentBase64: await toBase64(cv) };
      const r = await fetch('/api/v1/public/careers/applications', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      const data = (await r.json().catch(() => ({}))) as Record<string, unknown>;
      if (!r.ok) {
        const fields = (data.errors as { message: string }[] | undefined)?.map((f) => f.message).join('; ');
        throw new Error(fields || String(data.detail ?? data.title ?? `Error ${r.status}`));
      }
      setDone(String(data.number ?? ''));
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };
  if (done !== null) {
    return (
      <div className="w-card" role="status">
        <h2 style={{ marginTop: 0 }}>{id ? 'Lamaran terkirim' : 'Application sent'}</h2>
        <p>{id ? `Terima kasih. Nomor lamaran Anda ${done}. Kami mengirim konfirmasi ke e-mail Anda dan akan menghubungi Anda bila profil Anda sesuai.`
          : `Thank you. Your application number is ${done}. We e-mailed you a confirmation and will contact you if your profile matches.`}</p>
      </div>
    );
  }
  return (
    <form className="w-card w-form" onSubmit={submit}>
      <h2 style={{ marginTop: 0 }}>{id ? 'Lamar posisi ini' : 'Apply for this position'}</h2>
      <label>{id ? 'Nama lengkap' : 'Full name'} *<input required maxLength={120} value={v.fullName} onChange={set('fullName')} autoComplete="name" /></label>
      <label>E-mail *<input required type="email" value={v.email} onChange={set('email')} autoComplete="email" /></label>
      <label>{id ? 'No. HP / WhatsApp' : 'Mobile / WhatsApp'} *<input required type="tel" value={v.phone} onChange={set('phone')} autoComplete="tel" /></label>
      <label>{id ? 'Kota' : 'City'}<input value={v.city} onChange={set('city')} autoComplete="address-level2" /></label>
      <label>{id ? 'Pendidikan terakhir' : 'Education'}
        <select value={v.education} onChange={set('education')}>
          <option value="">—</option>
          {EDUCATION.map(([k, i, e]) => <option key={k} value={k}>{id ? i : e}</option>)}
        </select>
      </label>
      <label>{id ? 'Jabatan saat ini' : 'Current job title'}<input value={v.currentTitle} onChange={set('currentTitle')} /></label>
      <label>{id ? 'Pengalaman (tahun)' : 'Experience (years)'}<input inputMode="decimal" value={v.experienceYears} onChange={set('experienceYears')} /></label>
      <label>{id ? 'Surat lamaran / pesan' : 'Cover letter / message'}<textarea rows={5} maxLength={4000} value={v.coverLetter} onChange={set('coverLetter')} /></label>
      <label>CV (PDF, DOC/DOCX, JPG, PNG · max {MAX_MB} MB)
        <input type="file" accept="application/pdf,.doc,.docx,image/jpeg,image/png" onChange={(e) => setCv(e.target.files?.[0] ?? null)} />
      </label>
      <label aria-hidden="true" style={{ position: 'absolute', left: '-9999px' }}>Website<input tabIndex={-1} autoComplete="off" value={v.website} onChange={set('website')} /></label>
      <label style={{ display: "flex", gap: 8, alignItems: "flex-start", gridColumn: "1 / -1" }}><input style={{ height: 20, width: 20 }} type="checkbox" checked={consent} onChange={(e) => setConsent(e.target.checked)} required />
        {id ? ' Saya menyetujui pemrosesan data lamaran saya untuk rekrutmen sesuai UU Pelindungan Data Pribadi. *'
          : ' I agree to the processing of my application data for recruitment under the Personal Data Protection Law (UU PDP). *'}</label>
      <label style={{ display: "flex", gap: 8, alignItems: "flex-start", gridColumn: "1 / -1" }}><input style={{ height: 20, width: 20 }} type="checkbox" checked={pool} onChange={(e) => setPool(e.target.checked)} />
        {id ? ' Simpan profil saya di talent pool selama 1 tahun untuk lowongan lain.' : ' Keep my profile in the talent pool for 1 year for other openings.'}</label>
      {error && <p role="alert" className="w-error">{error}</p>}
      <button className="w-btn" type="submit" disabled={busy || !consent}>{busy ? (id ? 'Mengirim…' : 'Sending…') : id ? 'Kirim lamaran' : 'Send application'}</button>
    </form>
  );
}
