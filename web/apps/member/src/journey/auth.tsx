import React, { useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { request, type Schemas } from '@oneclub/api-client';
import { ErrorAlert, Icon, PasswordField, TextField, loginPhotoOf, logoOf, useAuth, useBootstrap } from '@oneclub/shell';

/*
 * Member App sign-in and sign-up (demo feedback 10 Oct 2026 #37): a
 * consumer screen of its own — club photo, big buttons — instead of the
 * staff login. Members and guests use the same Member App: a member
 * registers with the member no., a guest registers as a non-member and can
 * upgrade later (/join). The staff login stays as it is.
 */

const T = {
  en: {
    welcome: 'Welcome to', sub: 'Book tee times, courts and stays, follow your rounds and pay your bills.', email: 'E-mail', password: 'Password',
    signin: 'Sign in', otp: 'Sign in with a one-time code', forgot: 'Forgot password?', newHere: 'New here?', asMember: 'Register as a member',
    asMemberHelp: 'I am a member of the club and need my app account', asGuest: 'Register as a guest', asGuestHelp: 'Play and book without a membership',
    join: 'Become a member', joinHelp: 'Apply for a membership', bad: 'Wrong e-mail or password.', other: 'Use the full sign-in page',
  },
  id: {
    welcome: 'Selamat datang di', sub: 'Pesan tee time, lapangan dan menginap, pantau ronde Anda dan bayar tagihan.', email: 'E-mail', password: 'Kata sandi',
    signin: 'Masuk', otp: 'Masuk dengan kode OTP', forgot: 'Lupa kata sandi?', newHere: 'Belum punya akun?', asMember: 'Daftar sebagai member',
    asMemberHelp: 'Saya member klub dan ingin membuat akun aplikasi', asGuest: 'Daftar sebagai tamu', asGuestHelp: 'Main dan pesan tanpa keanggotaan',
    join: 'Jadi member', joinHelp: 'Ajukan keanggotaan', bad: 'E-mail atau kata sandi salah.', other: 'Pakai halaman masuk lengkap',
  },
};

function useLang(): 'id' | 'en' {
  const { locale } = useAuth();
  return locale === 'en' ? 'en' : 'id';
}

function AuthFrame({ children }: { children: React.ReactNode }) {
  const b = useBootstrap();
  return (
    <div className="mj-auth">
      <div className="mj-auth-photo" style={{ backgroundImage: `url(${loginPhotoOf(b.branding)})` }} aria-hidden="true" />
      <main className="mj-auth-panel">
        <div className="mj-auth-brand"><img src={logoOf(b.branding)} alt="" /><strong>{b.branding.appName}</strong></div>
        {children}
      </main>
    </div>
  );
}

/** Sign in: e-mail + password, or a one-time code; register as a member or a guest; become a member. */
export function MemberSignIn() {
  const t = T[useLang()];
  const b = useBootstrap();
  const nav = useNavigate();
  const qc = useQueryClient();
  const { refresh } = useAuth();
  const [params] = useSearchParams();
  const [email, setEmail] = useState(() => params.get('email') ?? '');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await request('POST', '/api/v1/auth/login', { email, password });
      await qc.invalidateQueries();
      const r = (await refresh()) as { data?: Schemas['MeResponse'] | null };
      const m = r.data;
      // MFA or a required password change: the full sign-in page handles them
      if (m && (m.mfaPending || m.passwordChangeRequired)) nav(`/login/full?email=${encodeURIComponent(email)}`, { replace: true });
      else nav(params.get('next') || '/', { replace: true });
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };
  return (
    <AuthFrame>
      <h1 className="mj-auth-title">{t.welcome} {b.branding.appName}</h1>
      <p className="mj-muted" style={{ marginTop: 0 }}>{t.sub}</p>
      <form className="oc-stack" onSubmit={submit}>
        <TextField label={t.email} type="email" autoComplete="username" value={email} onChange={setEmail} required />
        <PasswordField label={t.password} autoComplete="current-password" value={password} onChange={setPassword} required />
        <ErrorAlert error={error} />
        <button className="oc-btn oc-btn-primary mj-auth-btn" disabled={busy || !email || !password}>{busy ? '…' : t.signin}</button>
      </form>
      <div className="mj-auth-links">
        <Link to="/login/code">{t.otp}</Link>
        <Link to="/reset-password">{t.forgot}</Link>
      </div>
      <h2 className="mj-section-title" style={{ marginTop: 24 }}>{t.newHere}</h2>
      <div className="mj-auth-choices">
        <Link to="/register/member" className="mj-card"><span className="mj-icon"><Icon name="badge" size={22} /></span>
          <span><strong>{t.asMember}</strong><br /><span className="mj-small mj-muted">{t.asMemberHelp}</span></span></Link>
        <Link to="/register/guest" className="mj-card"><span className="mj-icon"><Icon name="person_add" size={22} /></span>
          <span><strong>{t.asGuest}</strong><br /><span className="mj-small mj-muted">{t.asGuestHelp}</span></span></Link>
        <Link to="/join" className="mj-card"><span className="mj-icon"><Icon name="workspace_premium" size={22} /></span>
          <span><strong>{t.join}</strong><br /><span className="mj-small mj-muted">{t.joinHelp}</span></span></Link>
      </div>
      <p className="mj-small" style={{ marginTop: 16 }}><Link to="/login/full">{t.other}</Link></p>
    </AuthFrame>
  );
}

const R = {
  en: {
    memberTitle: 'Register as a member', guestTitle: 'Register as a guest', memberHelp: 'Use the member no. on your card and one detail the club has on file.',
    guestHelp: 'A non-member account: book with the guest rates, pay online, see your history, scorecards and bills.',
    memberNo: 'Member no.', birth: 'Date of birth', phone: 'Mobile (WhatsApp)', email: 'E-mail', name: 'Full name', send: 'Send the code',
    codeTitle: 'Enter the code', sent: 'We sent a 6-digit code to', demo: 'Demo instance — the code is', code: 'Code', password: 'Choose a password',
    passwordHelp: 'At least 10 characters, not your e-mail.', create: 'Create my account', done: 'Your account is ready', signin: 'Sign in',
    member: 'Member', nonMember: 'Non-member', back: 'Back to sign in', again: 'Start again', via: 'Send the code by',
  },
  id: {
    memberTitle: 'Daftar sebagai member', guestTitle: 'Daftar sebagai tamu', memberHelp: 'Pakai No. Member di kartu Anda dan satu data yang tercatat di klub.',
    guestHelp: 'Akun non-member: pesan dengan tarif tamu, bayar online, lihat riwayat, scorecard dan tagihan.',
    memberNo: 'No. Member', birth: 'Tanggal lahir', phone: 'Nomor ponsel (WhatsApp)', email: 'E-mail', name: 'Nama lengkap', send: 'Kirim kode',
    codeTitle: 'Masukkan kode', sent: 'Kode 6 digit dikirim ke', demo: 'Instance demo — kodenya', code: 'Kode', password: 'Buat kata sandi',
    passwordHelp: 'Minimal 10 karakter, bukan e-mail Anda.', create: 'Buat akun saya', done: 'Akun Anda siap', signin: 'Masuk',
    member: 'Member', nonMember: 'Non-member', back: 'Kembali ke halaman masuk', again: 'Ulangi', via: 'Kirim kode lewat',
  },
};

/** Sign-up of a member (member no. + a detail on file) or a guest (non-member): code, then password. */
export function MemberSignUp({ kind }: { kind: 'member' | 'guest' }) {
  const lang = useLang();
  const t = R[lang];
  const b = useBootstrap();
  const nav = useNavigate();
  const property = b.properties?.[0]?.id ?? '';
  const [f, setF] = useState({ memberNo: '', birthDate: '', name: '', phone: '', email: '', website: '' });
  const [channel, setChannel] = useState<'email' | 'whatsapp'>('email');
  const [started, setStarted] = useState<Schemas['SignUp'] | null>(null);
  const [code, setCode] = useState('');
  const [password, setPassword] = useState('');
  const [done, setDone] = useState<Schemas['SignUpDone'] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    try { await fn(); } catch (e) { setError(e); } finally { setBusy(false); }
  };
  const start = (e: React.FormEvent) => {
    e.preventDefault();
    void run(async () => {
      setStarted(await request<Schemas['SignUp']>('POST', '/api/v1/public/portal-registrations', { propertyId: property, kind, memberNo: f.memberNo || undefined,
        birthDate: f.birthDate || undefined, name: f.name || undefined, phone: f.phone || undefined, email: f.email, channel, website: f.website || undefined }));
    });
  };
  const confirm = (e: React.FormEvent) => {
    e.preventDefault();
    if (!started) return;
    void run(async () => {
      setDone(await request<Schemas['SignUpDone']>('POST', `/api/v1/public/portal-registrations/${started.id}:confirm`, { code, password }));
    });
  };
  return (
    <AuthFrame>
      <h1 className="mj-auth-title">{kind === 'member' ? t.memberTitle : t.guestTitle}</h1>
      {done ? (
        <div className="oc-stack">
          <div className="mj-card oc-row-wrap">
            <span className="mj-icon"><Icon name="check_circle" size={22} /></span>
            <span style={{ flex: 1 }}><strong>{t.done}</strong><br /><span className="mj-small mj-muted">{done.name} · {done.email}</span></span>
            <span className="mj-tier">{done.status === 'member' ? `${t.member}${done.memberNo ? ` · ${done.memberNo}` : ''}` : t.nonMember}</span>
          </div>
          <button className="oc-btn oc-btn-primary mj-auth-btn" onClick={() => nav(`/login?email=${encodeURIComponent(done.email)}`)}>{t.signin}</button>
        </div>
      ) : started ? (
        <form className="oc-stack" onSubmit={confirm}>
          <p style={{ margin: 0 }}>{t.sent} <strong>{started.sentTo}</strong>.</p>
          {started.demoCode && <div className="oc-alert oc-alert-info">{t.demo} <strong>{started.demoCode}</strong></div>}
          <TextField label={t.code} value={code} onChange={(v) => setCode(v.replace(/\D/g, '').slice(0, 6))} inputMode="numeric" autoComplete="one-time-code" required autoFocus />
          <PasswordField label={t.password} help={t.passwordHelp} autoComplete="new-password" value={password} onChange={setPassword} required />
          <ErrorAlert error={error} />
          <button className="oc-btn oc-btn-primary mj-auth-btn" disabled={busy || code.length !== 6 || password.length < 8}>{busy ? '…' : t.create}</button>
          <button type="button" className="oc-btn oc-btn-text" onClick={() => { setStarted(null); setCode(''); }}>{t.again}</button>
        </form>
      ) : (
        <form className="oc-stack" onSubmit={start}>
          <p className="mj-muted" style={{ margin: 0 }}>{kind === 'member' ? t.memberHelp : t.guestHelp}</p>
          {kind === 'member' ? (
            <>
              <TextField label={t.memberNo} value={f.memberNo} onChange={(v) => setF({ ...f, memberNo: v })} required autoFocus />
              <TextField label={t.birth} type="date" value={f.birthDate} onChange={(v) => setF({ ...f, birthDate: v })} />
            </>
          ) : <TextField label={t.name} value={f.name} onChange={(v) => setF({ ...f, name: v })} required autoFocus autoComplete="name" />}
          <TextField label={t.phone} type="tel" value={f.phone} onChange={(v) => setF({ ...f, phone: v })} autoComplete="tel" />
          <TextField label={t.email} type="email" value={f.email} onChange={(v) => setF({ ...f, email: v })} required autoComplete="email" />
          <label aria-hidden="true" style={{ position: 'absolute', left: -9999 }}>Website<input tabIndex={-1} autoComplete="off" value={f.website}
            onChange={(e) => setF({ ...f, website: e.target.value })} /></label>
          <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
            <span className="mj-small">{t.via}</span>
            <button type="button" className="oc-chip" aria-pressed={channel === 'email'} onClick={() => setChannel('email')}>E-mail</button>
            <button type="button" className="oc-chip" aria-pressed={channel === 'whatsapp'} disabled={!f.phone} onClick={() => setChannel('whatsapp')}>WhatsApp</button>
          </div>
          <ErrorAlert error={error} />
          <button className="oc-btn oc-btn-primary mj-auth-btn" disabled={busy || !property || !f.email || (kind === 'member' ? !f.memberNo : !f.name)}>{busy ? '…' : t.send}</button>
        </form>
      )}
      <p className="mj-small" style={{ marginTop: 16 }}><Link to="/login">{t.back}</Link></p>
    </AuthFrame>
  );
}
