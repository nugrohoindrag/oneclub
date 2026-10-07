import React, { Suspense, lazy, useState } from 'react';
import { Link, Navigate, Outlet, useRoutes } from 'react-router';
import { useGet, type Page } from '@oneclub/api-client';
import { useTranslation } from '@oneclub/i18n';
import { enqueue, useOnline } from '@oneclub/offline';
import { Brand, Card, HeaderActions, Icon, NotFoundPage, NotificationsPage, ProfilePage, Skeleton, TextArea, useAuth, useToast } from '@oneclub/shell';
import {
  BagDropPage, BagStoragePage, CaddyAssignmentPage, CaddyQueuePage, CartAssignmentPage, CartReadinessPage, FrontDeskFoliosPage, FrontDeskPage,
  FrontDeskPaymentsPage, GuestPage, LockersPage, OpsCheckInPage, OpsTeeSheetPage, OpsTiles, StarterQueuePage,
} from '../ops/golf';
import { P2_OPS_ROUTES, P2Tiles } from '../ops/p2';
import { P3_OPS_ROUTES, P3Tiles } from '../ops/p3';
import { ConnectivityChip, OUTLET_KEY, SyncPage, read, write } from '../offline';

// Operational area (`/ops`): touch-first, offline-capable (Technical Doc §6.4, PRD FR-SH-05).

/** POS Cashier: a full-screen app of its own (pos/), loaded when opened. */
const PosApp = lazy(() => import('../pos'));

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
        <Link to="/ops"><Icon name="home" size={26} />Home</Link>
        <Link to="/ops/check-in"><Icon name="how_to_reg" size={26} />Check-in</Link>
        <Link to="/ops/sync"><Icon name="sync" size={26} />Sync Queue</Link>
        <Link to="/ops/notifications"><Icon name="notifications" size={26} />Notifications</Link>
        <Link to="/ops/profile"><Icon name="person" size={26} />Profile</Link>
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
      <P2Tiles />
      <P3Tiles />
    </div>
  );
}

const routes = [
  { path: 'pos/*', element: <Suspense fallback={<Skeleton rows={6} />}><PosApp /></Suspense> },
  {
    element: <OpsLayout />,
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
      ...P2_OPS_ROUTES,
      ...P3_OPS_ROUTES,
      { path: 'notifications', element: <NotificationsPage /> },
      { path: 'profile', element: <ProfilePage showPin /> },
      { path: 'home', element: <Navigate to="/ops" /> },
      { path: '*', element: <NotFoundPage /> },
    ],
  },
];

export default function OpsArea() {
  return useRoutes(routes);
}
