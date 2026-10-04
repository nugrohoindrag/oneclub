import React, { useState } from 'react';
import ReactDOM from 'react-dom/client';
import { createBrowserRouter, Link, Navigate, Outlet, RouterProvider, useNavigate } from 'react-router';
import '@oneclub/shell/shell.css';
import { request, useGet, type Page } from '@oneclub/api-client';
import { formatDateTime, useTranslation } from '@oneclub/i18n';
import { clearAll, enqueue, flush, setForcedOffline, useOnline, useQueue } from '@oneclub/offline';
import {
  AppProviders, AuthFrame, Brand, Card, DataTable, ErrorAlert, ErrorBoundary, HeaderActions, Icon, LoginPage, NotFoundPage, NotificationsPage,
  PasswordField, ProfilePage, RequireShell, ResetPasswordPage, StatusPill, TextArea, TextField, useAuth, useToast,
} from '@oneclub/shell';
import {
  BagDropPage, BagStoragePage, CaddyAssignmentPage, CaddyQueuePage, CartAssignmentPage, CartReadinessPage, FrontDeskFoliosPage, FrontDeskPage,
  FrontDeskPaymentsPage, GuestPage, LockersPage, OpsCheckInPage, OpsTeeSheetPage, OpsTiles, StarterQueuePage,
} from './golf';

const DEVICE_KEY = 'oneclub.deviceToken';
const OUTLET_KEY = 'oneclub.outlet';

function read(k: string) {
  try {
    return localStorage.getItem(k) ?? '';
  } catch {
    return '';
  }
}

function write(k: string, v: string) {
  try {
    if (v) localStorage.setItem(k, v);
    else localStorage.removeItem(k);
  } catch {
    /* ignore */
  }
}

/** Device enrollment + staff PIN login per shift (FR-IAM-09, FR-SH-07). */
function DeviceLoginPage() {
  const { t } = useTranslation();
  const nav = useNavigate();
  const { refresh } = useAuth();
  const [device, setDevice] = useState(read(DEVICE_KEY));
  const [token, setToken] = useState('');
  const [email, setEmail] = useState('');
  const [pin, setPin] = useState('');
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  if (!device) {
    return (
      <AuthFrame>
        <form className="oc-stack" onSubmit={(e) => { e.preventDefault(); write(DEVICE_KEY, token.trim()); setDevice(token.trim()); }}>
          <h1 style={{ fontSize: 32 }}>{t('auth.enrollDevice')}</h1>
          <p className="oc-muted" style={{ margin: 0 }}>{t('auth.enrollHelp')}</p>
          <PasswordField label={t('auth.deviceToken')} value={token} onChange={setToken} required />
          <button className="oc-btn oc-btn-ink oc-btn-block" disabled={!token.startsWith('ocd_')}>{t('auth.enrollDevice')}</button>
          <Link className="oc-small" to="/login/password">Password login</Link>
        </form>
      </AuthFrame>
    );
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await request('POST', '/api/v1/auth/device-login', { deviceToken: device, email, pin });
      await refresh();
      nav('/', { replace: true });
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
        <ErrorAlert error={error} />
        <button className="oc-btn oc-btn-ink oc-btn-block" disabled={busy || pin.length !== 6 || !email}>{t('auth.login')}</button>
        <div className="oc-row">
          <Link className="oc-small" to="/login/password">Password login</Link>
          <span className="oc-spacer" />
          <button type="button" className="oc-btn oc-btn-text oc-btn-sm" onClick={() => { write(DEVICE_KEY, ''); setDevice(''); }}>Change device</button>
        </div>
      </form>
    </AuthFrame>
  );
}

function ConnectivityChip() {
  const online = useOnline();
  const queue = useQueue();
  const pending = queue.filter((q) => ['queued', 'sending', 'failed'].includes(q.status)).length;
  return (
    <Link to="/sync" className="oc-chip" aria-label={`${online ? 'Online' : 'Offline'}, ${pending} pending`}>
      <Icon name={online ? 'cloud_done' : 'cloud_off'} size={18} /> {online ? 'Online' : 'Offline'}{pending > 0 && ` · ${pending}`}
    </Link>
  );
}

/** Touch-first layout without dashboard chrome (Technical Doc §6.5). */
function OpsLayout() {
  const { t } = useTranslation();
  const online = useOnline();
  return (
    <div className="oc-topnav-frame" style={{ maxWidth: 1100 }}>
      <header className="oc-topbar">
        <Brand />
        <span className="oc-spacer" />
        <ConnectivityChip />
        <HeaderActions property={false} />
      </header>
      {!online && <div className="oc-alert oc-alert-warning" role="status" style={{ marginBottom: 12 }}>{t('common.offline')}</div>}
      <Outlet />
      <nav className="oc-bottom-nav" aria-label="Main">
        <Link to="/"><Icon name="home" size={26} />Home</Link>
        <Link to="/check-in"><Icon name="how_to_reg" size={26} />Check-in</Link>
        <Link to="/sync"><Icon name="sync" size={26} />Sync Queue</Link>
        <Link to="/notifications"><Icon name="notifications" size={26} />Notifications</Link>
        <Link to="/profile"><Icon name="person" size={26} />Profile</Link>
      </nav>
    </div>
  );
}

type Outlet = { id: string; name: string; code: string };

function OutletPicker() {
  const { can } = useAuth();
  const allowed = can('commercial.outlet.view');
  const outlets = useGet<Page<Outlet>>(allowed ? '/api/v1/commercial/outlets?filter[status]=active' : null);
  const [outlet, setOutlet] = useState(read(OUTLET_KEY));
  if (!allowed) return null;
  return (
    <Card title="Outlet" icon="storefront">
      <div className="oc-row-wrap">
        {(outlets.data?.items ?? []).map((o) => (
          <button key={o.id} className="oc-chip" style={{ height: 44 }} aria-pressed={outlet === o.id}
            onClick={() => { write(OUTLET_KEY, o.id); setOutlet(o.id); }}>{o.name}</button>
        ))}
        {outlets.data?.items.length === 0 && <span className="oc-muted">No outlets configured for this property.</span>}
      </div>
    </Card>
  );
}

function HomePage() {
  const { me, propertyId } = useAuth();
  const toast = useToast();
  const [note, setNote] = useState('');
  const property = me?.properties.find((p) => p.id === propertyId);
  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    await enqueue('ops.shift_note', { text: note }, propertyId);
    setNote('');
    toast('Saved to the sync queue');
  };
  return (
    <div className="oc-stack">
      <div className="oc-page-head">
        <div><h1>{me?.fullName}</h1><p>{property?.name}{me?.kind === 'device' ? ' · shift session' : ''}</p></div>
      </div>
      <OutletPicker />
      <Card title="Shift note" icon="edit_note">
        <form className="oc-stack" onSubmit={save}>
          <TextArea label="Note" value={note} onChange={setNote} rows={3} help="Works offline: the note is queued and synced when the connection returns." />
          <div><button className="oc-btn oc-btn-ink" disabled={!note.trim()}>Save note</button></div>
        </form>
      </Card>
      <OpsTiles />
    </div>
  );
}

/** Sync queue with simulated offline mode (FR-SH-05 acceptance). */
function SyncPage() {
  const online = useOnline();
  const items = useQueue();
  return (
    <div className="oc-stack">
      <div className="oc-page-head">
        <div><h1>Sync Queue</h1><p>Actions recorded on this device, sent in order when online.</p></div>
        <span className="oc-spacer" />
        <button className="oc-btn oc-btn-outline" onClick={() => setForcedOffline(online)}>{online ? 'Simulate offline' : 'Go back online'}</button>
        <button className="oc-btn oc-btn-ink" disabled={!online} onClick={() => void flush()}>Sync now</button>
      </div>
      <div className="oc-card">
        <DataTable rows={items as unknown as Record<string, unknown>[]}
          columns={[
            { key: 'createdAt', header: 'Recorded', render: (i) => formatDateTime(String(i.createdAt)) },
            { key: 'action', header: 'Action', render: (i) => <code className="oc-code">{String(i.action)}</code> },
            { key: 'payload', header: 'Data', render: (i) => <span className="oc-small">{JSON.stringify(i.payload).slice(0, 80)}</span> },
            { key: 'attempts', header: 'Attempts', align: 'right' },
            { key: 'status', header: 'Status', render: (i) => (
              <div><StatusPill status={String(i.status)} />{i.error ? <div className="oc-small oc-muted">{String(i.error)}</div> : null}</div>
            ) },
          ]} />
      </div>
    </div>
  );
}

const router = createBrowserRouter([
  {
    element: <ErrorBoundary><Outlet /></ErrorBoundary>,
    children: [
      { path: '/login', element: <DeviceLoginPage /> },
      { path: '/login/password', element: <LoginPage shell="ops" title="Staff log in" /> },
      { path: '/reset-password', element: <ResetPasswordPage /> },
      {
        path: '/',
        element: <RequireShell shell="ops"><OpsLayout /></RequireShell>,
        children: [
          { index: true, element: <HomePage /> },
          { path: 'sync', element: <SyncPage /> },
          { path: 'starter', element: <StarterQueuePage /> },
          { path: 'starter/ready', element: <StarterQueuePage view="ready" /> },
          { path: 'starter/dispatch', element: <StarterQueuePage /> },
          { path: 'starter/rounds', element: <StarterQueuePage view="rounds" /> },
          { path: 'starter/tee-sheet', element: <OpsTeeSheetPage /> },
          { path: 'check-in', element: <OpsCheckInPage /> },
          { path: 'caddy', element: <CaddyQueuePage /> },
          { path: 'caddy/availability', element: <CaddyQueuePage /> },
          { path: 'caddy/rotation', element: <CaddyQueuePage /> },
          { path: 'caddy/assignment', element: <CaddyAssignmentPage /> },
          { path: 'caddy/history', element: <CaddyAssignmentPage history /> },
          { path: 'front-desk', element: <FrontDeskPage /> },
          { path: 'front-desk/guest', element: <GuestPage /> },
          { path: 'front-desk/payments', element: <FrontDeskPaymentsPage /> },
          { path: 'front-desk/folios', element: <FrontDeskFoliosPage /> },
          { path: 'golf-staff', element: <BagDropPage /> },
          { path: 'golf-staff/bag-storage', element: <BagStoragePage /> },
          { path: 'golf-staff/lockers', element: <LockersPage /> },
          { path: 'golf-staff/golf-carts', element: <CartReadinessPage /> },
          { path: 'golf-staff/golf-cart-assignment', element: <CartAssignmentPage /> },
          { path: 'notifications', element: <NotificationsPage /> },
          { path: 'profile', element: <ProfilePage showPin /> },
          { path: 'home', element: <Navigate to="/" /> },
          { path: '*', element: <NotFoundPage /> },
        ],
      },
    ],
  },
]);

async function onLogout() {
  await clearAll();
  write(OUTLET_KEY, '');
  if ('caches' in window) await caches.delete('ops-api');
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <AppProviders onLogout={onLogout}>
      <RouterProvider router={router} />
    </AppProviders>
  </React.StrictMode>,
);
