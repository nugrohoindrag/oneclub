import React, { useEffect, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { ApiError, request, type Schemas } from '@oneclub/api-client';
import { useTranslation } from '@oneclub/i18n';
import { useAuth, useBootstrap, type Me, type Shell } from '../context';
import { ErrorAlert, Icon, PasswordField, TextField, fieldErrors } from '../components/ui';
import { landingPath, useArea } from '../areas';
import { loginPhotoOf, logoOf } from '../theme';

type LoginResponse = Schemas['LoginResponse'];
type Step = 'credentials' | 'mfa-setup' | 'mfa-verify' | 'password' | 'denied';

/** Split-layout frame (login-reference.webp): branding photo left, form right. */
export function AuthFrame({ children }: { children: React.ReactNode }) {
  const b = useBootstrap();
  return (
    <div className="oc-login-bg">
      <div className="oc-login">
        <div className="oc-login-visual" aria-hidden="true">
          <img className="oc-login-photo" src={loginPhotoOf(b.branding)} alt="" />
          <div>
            <img className="oc-login-logo" src={logoOf(b.branding)} alt="" />
          </div>
          <div>
            <div style={{ fontSize: 28, fontWeight: 600, lineHeight: 1.2 }}>{b.branding.appName}</div>
            <div style={{ opacity: 0.8 }}>Powered by OneClub</div>
          </div>
        </div>
        <div className="oc-login-form">{children}</div>
      </div>
    </div>
  );
}

function messageFor(e: unknown, t: (k: string) => string) {
  if (e instanceof ApiError) {
    if (e.code === 'invalid_credentials') return t('auth.invalidCredentials');
    if (e.code === 'account_locked') return t('auth.locked');
  }
  return undefined;
}

/**
 * Staff and member login: e-mail + password → MFA → temporary password change.
 * The Member App passes its shell; in the Staff App the user
 * goes to `next` when its area is theirs, otherwise to their first area.
 */
export function LoginPage({ shell, footer }: { shell?: Shell; footer?: React.ReactNode }) {
  const { t } = useTranslation();
  const { refresh, me, locale, changeLocale } = useAuth();
  const nav = useNavigate();
  const [params] = useSearchParams();
  const qc = useQueryClient();
  const [step, setStep] = useState<Step>('credentials');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [code, setCode] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [setup, setSetup] = useState<Schemas['MFASetupResponse'] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const next = params.get('next');
  const staff = useArea() !== null;
  const target = (m: Me) => (staff ? landingPath(m, next) : shell && m.shells.includes(shell) ? next || '/' : null);

  const finish = async () => {
    await qc.invalidateQueries();
    const r = (await refresh()) as { data?: Me | null };
    const m = r.data ?? null;
    if (m && m.mfaPending) return setStep('mfa-verify');
    if (m && m.passwordChangeRequired) return setStep('password');
    const to = m && target(m);
    if (to) nav(to, { replace: true });
    else setStep('denied');
  };

  // Resume an unfinished login (e.g. page reload while MFA was pending).
  useEffect(() => {
    if (!me) return;
    if (me.mfaPending) setStep(me.mfaEnabled ? 'mfa-verify' : 'mfa-setup');
    else if (me.passwordChangeRequired) setStep('password');
    else {
      const to = target(me);
      if (to) nav(to, { replace: true });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [me]);

  useEffect(() => {
    if (step === 'mfa-setup' && !setup) {
      request<Schemas['MFASetupResponse']>('POST', '/api/v1/auth/mfa/setup').then(setSetup).catch(setError);
    }
  }, [step, setup]);

  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  const submitCredentials = (e: React.FormEvent) => {
    e.preventDefault();
    void run(async () => {
      const r = await request<LoginResponse>('POST', '/api/v1/auth/login', { email, password });
      if (r.mfaRequired) setStep(r.mfaEnrolled ? 'mfa-verify' : 'mfa-setup');
      else if (r.passwordChangeRequired) setStep('password');
      else await finish();
    });
  };

  const submitCode = (e: React.FormEvent) => {
    e.preventDefault();
    void run(async () => {
      await request('POST', '/api/v1/auth/mfa/verify', { code: code.trim() });
      setCode('');
      await finish();
    });
  };

  const submitPassword = (e: React.FormEvent) => {
    e.preventDefault();
    void run(async () => {
      await request('POST', '/api/v1/auth/password/change', { currentPassword: password, newPassword });
      await finish();
    });
  };

  const fe = fieldErrors(error);
  const generic = messageFor(error, t);

  return (
    <AuthFrame>
      <div className="oc-row">
        <span className="oc-spacer" />
        <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => changeLocale(locale === 'id' ? 'en' : 'id')}>
          <Icon name="translate" size={18} /> {locale === 'id' ? 'English' : 'Bahasa Indonesia'}
        </button>
      </div>

      {step === 'credentials' && (
        <form className="oc-stack" onSubmit={submitCredentials} noValidate>
          <div>
            <h1>{t('auth.loginTitle')}</h1>
            <p className="oc-muted" style={{ margin: '8px 0 0' }}>{t('auth.loginSubtitle')}</p>
          </div>
          <TextField label={t('auth.email')} type="email" autoComplete="username" value={email} onChange={setEmail} required error={fe.email} />
          <PasswordField label={t('auth.password')} autoComplete="current-password" value={password} onChange={setPassword} required error={fe.password} />
          <div className="oc-row"><span className="oc-spacer" /><Link to="/reset-password" className="oc-small" style={{ fontWeight: 600 }}>{t('auth.forgot')}</Link></div>
          {generic ? <div className="oc-alert oc-alert-error" role="alert">{generic}</div> : !Object.keys(fe).length && <ErrorAlert error={error} />}
          <button className="oc-btn oc-btn-ink oc-btn-block" disabled={busy || !email || !password}>{t('auth.login')}</button>
          {footer}
        </form>
      )}

      {step === 'mfa-setup' && (
        <form className="oc-stack" onSubmit={submitCode}>
          <h1 style={{ fontSize: 30 }}>{t('auth.mfaSetupTitle')}</h1>
          <p className="oc-muted" style={{ margin: 0 }}>{t('auth.mfaSetupHelp')}</p>
          {setup && (
            <div className="oc-row" style={{ alignItems: 'flex-start', gap: 16 }}>
              {setup.qrCodePng && <img className="oc-qr" src={setup.qrCodePng} alt="QR code" />}
              <div>
                <div className="oc-small oc-muted">{t('auth.secretKey')}</div>
                <code className="oc-code">{setup.secret}</code>
              </div>
            </div>
          )}
          <TextField label={t('auth.mfaCode')} inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={code} onChange={setCode} required />
          <ErrorAlert error={error} />
          <button className="oc-btn oc-btn-ink oc-btn-block" disabled={busy || code.length < 6}>{t('auth.verify')}</button>
        </form>
      )}

      {step === 'mfa-verify' && (
        <form className="oc-stack" onSubmit={submitCode}>
          <h1 style={{ fontSize: 30 }}>{t('auth.mfaVerifyTitle')}</h1>
          <p className="oc-muted" style={{ margin: 0 }}>{t('auth.mfaVerifyHelp')}</p>
          <TextField label={t('auth.mfaCode')} inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={code} onChange={setCode} required autoFocus />
          <ErrorAlert error={error} />
          <button className="oc-btn oc-btn-ink oc-btn-block" disabled={busy || code.length < 6}>{t('auth.verify')}</button>
        </form>
      )}

      {step === 'password' && (
        <form className="oc-stack" onSubmit={submitPassword}>
          <h1 style={{ fontSize: 30 }}>{t('auth.changePasswordTitle')}</h1>
          <p className="oc-muted" style={{ margin: 0 }}>{t('auth.changePasswordHelp')}</p>
          {!password && <PasswordField label={t('auth.currentPassword')} value={password} onChange={setPassword} required />}
          <PasswordField label={t('auth.newPassword')} autoComplete="new-password" value={newPassword} onChange={setNewPassword} required
            help={t('auth.passwordRules')} error={fe.newPassword ?? fe.password} />
          <ErrorAlert error={Object.keys(fe).length ? null : error} />
          <button className="oc-btn oc-btn-ink oc-btn-block" disabled={busy || newPassword.length < 10}>{t('auth.changePassword')}</button>
        </form>
      )}

      {step === 'denied' && (
        <div className="oc-stack">
          <h1 style={{ fontSize: 30 }}>{t('shell.forbiddenTitle')}</h1>
          <p className="oc-muted">{t('auth.noAccess')}</p>
          <button className="oc-btn oc-btn-neutral" onClick={async () => {
            await request('POST', '/api/v1/auth/logout').catch(() => undefined);
            await qc.invalidateQueries();
            setStep('credentials');
          }}>{t('auth.backToLogin')}</button>
        </div>
      )}
    </AuthFrame>
  );
}

/** Reset password: request link, or set a new password with ?token=. */
export function ResetPasswordPage() {
  const { t } = useTranslation();
  const [params] = useSearchParams();
  const token = params.get('token');
  const [email, setEmail] = useState('');
  const [pw, setPw] = useState('');
  const [done, setDone] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      if (token) await request('POST', '/api/v1/auth/password/reset/confirm', { token, newPassword: pw });
      else await request('POST', '/api/v1/auth/password/reset', { email });
      setDone(true);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };
  const fe = fieldErrors(error);
  return (
    <AuthFrame>
      <form className="oc-stack" onSubmit={submit}>
        <h1 style={{ fontSize: 32 }}>{t('auth.resetTitle')}</h1>
        {done ? (
          <div className="oc-alert oc-alert-success" role="status">{token ? t('auth.resetDone') : t('auth.resetSent')}</div>
        ) : token ? (
          <>
            <PasswordField label={t('auth.newPassword')} autoComplete="new-password" value={pw} onChange={setPw} required help={t('auth.passwordRules')}
              error={fe.password} />
            <ErrorAlert error={Object.keys(fe).length ? null : error} />
            <button className="oc-btn oc-btn-ink oc-btn-block" disabled={busy || pw.length < 10}>{t('auth.setNewPassword')}</button>
          </>
        ) : (
          <>
            <p className="oc-muted" style={{ margin: 0 }}>{t('auth.resetHelp')}</p>
            <TextField label={t('auth.email')} type="email" value={email} onChange={setEmail} required />
            <ErrorAlert error={error} />
            <button className="oc-btn oc-btn-ink oc-btn-block" disabled={busy || !email}>{t('auth.sendLink')}</button>
          </>
        )}
        <Link to="/login" className="oc-small" style={{ fontWeight: 600 }}>← {t('auth.backToLogin')}</Link>
      </form>
    </AuthFrame>
  );
}
