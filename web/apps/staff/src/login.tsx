import React, { useState } from 'react';
import { Link, Navigate, useNavigate, useSearchParams } from 'react-router';
import { request } from '@oneclub/api-client';
import { useTranslation } from '@oneclub/i18n';
import { AuthFrame, ErrorAlert, LoginPage, PasswordField, TextField, currentSurface, landingPath, useAuth, type Me } from '@oneclub/shell';
import { DEVICE_KEY, read, write } from './offline';

/*
 * One login for every staff area (Technical Doc §6.1). On the device domains
 * (cashier, caddy, kitchen) a registered shared device (POS, ops tablet,
 * caddy tablet, KDS; FR-IAM-09) logs staff in with their PIN for the shift;
 * the dashboard domain and a personal device use e-mail + password (+ MFA).
 * Both land on the user's first area of the domain. The device token is kept
 * per domain (browser storage is per origin).
 */

/** The dashboard domain never runs in device mode. */
const deviceMode = () => currentSurface() !== 'dashboard';

/** /login: PIN on a registered device, otherwise e-mail + password. */
export function StaffLoginPage() {
  return deviceMode() && read(DEVICE_KEY) ? <DevicePinPage /> : <PasswordLoginPage />;
}

/** /login/password: e-mail + password, also on a registered device. */
export function PasswordLoginPage() {
  if (!deviceMode()) return <LoginPage />;
  return (
    <LoginPage footer={
      <p className="oc-small oc-muted" style={{ margin: 0 }}>
        {read(DEVICE_KEY) ? <Link to="/login">PIN login</Link> : <Link to="/login/device">Register this device</Link>}
      </p>
    } />
  );
}

/** /login/device: stores the device token shown once when the device was registered. */
export function DeviceEnrollPage() {
  const { t } = useTranslation();
  const nav = useNavigate();
  const [token, setToken] = useState('');
  if (!deviceMode()) return <Navigate to="/login" replace />;
  return (
    <AuthFrame>
      <form className="oc-stack" onSubmit={(e) => { e.preventDefault(); write(DEVICE_KEY, token.trim()); nav('/login', { replace: true }); }}>
        <h1 style={{ fontSize: 32 }}>{t('auth.enrollDevice')}</h1>
        <p className="oc-muted" style={{ margin: 0 }}>{t('auth.enrollHelp')}</p>
        <PasswordField label={t('auth.deviceToken')} value={token} onChange={setToken} required />
        <button className="oc-btn oc-btn-ink oc-btn-block" disabled={!token.trim().startsWith('ocd_')}>{t('auth.enrollDevice')}</button>
        <Link className="oc-small" to="/login/password">Password login</Link>
      </form>
    </AuthFrame>
  );
}

/** Staff PIN login per shift on a registered device (FR-IAM-09, FR-SH-07). */
function DevicePinPage() {
  const { t } = useTranslation();
  const nav = useNavigate();
  const [params] = useSearchParams();
  const { refresh } = useAuth();
  const [email, setEmail] = useState('');
  const [pin, setPin] = useState('');
  const [error, setError] = useState<unknown>(null);
  const [denied, setDenied] = useState(false);
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await request('POST', '/api/v1/auth/device-login', { deviceToken: read(DEVICE_KEY), email, pin });
      const me = ((await refresh()) as { data?: Me | null }).data;
      const to = me && landingPath(me, params.get('next'));
      if (to) nav(to, { replace: true });
      else setDenied(true);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <AuthFrame>
      <form className="oc-stack" onSubmit={submit}>
        <h1 style={{ fontSize: 34 }}>{t('auth.deviceTitle')}</h1>
        <p className="oc-muted" style={{ margin: 0 }}>{t('auth.deviceHelp')}</p>
        <TextField label={t('auth.email')} type="email" value={email} onChange={setEmail} required autoComplete="username" />
        <TextField label={t('auth.pin')} type="password" inputMode="numeric" maxLength={6} value={pin} onChange={setPin} required autoComplete="off" />
        {denied && <div className="oc-alert oc-alert-error" role="alert">{t('auth.noAccess')}</div>}
        <ErrorAlert error={error} />
        <button className="oc-btn oc-btn-ink oc-btn-block" disabled={busy || pin.length !== 6 || !email}>{t('auth.login')}</button>
        <div className="oc-row">
          <Link className="oc-small" to="/login/password">Password login</Link>
          <span className="oc-spacer" />
          <button type="button" className="oc-btn oc-btn-text oc-btn-sm" onClick={() => { write(DEVICE_KEY, ''); nav('/login/device'); }}>Change device</button>
        </div>
      </form>
    </AuthFrame>
  );
}
