'use client';
import { useState } from 'react';
import type { Lang } from '../../lib';

/** Self-activation: the portal activation link is e-mailed (no account enumeration). */
export function ActivateForm({ lang }: { lang: Lang }) {
  const [v, setV] = useState({ memberNo: '', email: '', birthDate: '' });
  const [state, setState] = useState<'idle' | 'busy' | 'sent' | 'error'>('idle');
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setState('busy');
    try {
      const r = await fetch('/api/v1/public/membership/activate', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(v) });
      setState(r.ok ? 'sent' : 'error');
    } catch {
      setState('error');
    }
  };
  if (state === 'sent') {
    return <p>{lang === 'id' ? 'Jika data cocok, tautan aktivasi telah dikirim ke e-mail Anda.' : 'If the details match, an activation link has been sent to your e-mail.'}</p>;
  }
  return (
    <form className="w-form" onSubmit={submit}>
      <label>Member No.<input value={v.memberNo} onChange={(e) => setV({ ...v, memberNo: e.target.value })} required /></label>
      <label>E-mail<input type="email" value={v.email} onChange={(e) => setV({ ...v, email: e.target.value })} required /></label>
      <label>{lang === 'id' ? 'Tanggal lahir' : 'Date of birth'}<input type="date" value={v.birthDate} onChange={(e) => setV({ ...v, birthDate: e.target.value })} required /></label>
      <div><button className="w-btn" disabled={state === 'busy'}>{lang === 'id' ? 'Kirim tautan aktivasi' : 'Send activation link'}</button></div>
      {state === 'error' && <p className="w-error">{lang === 'id' ? 'Coba lagi beberapa saat lagi.' : 'Please try again shortly.'}</p>}
    </form>
  );
}
