import React, { Suspense, lazy, useRef, useState } from 'react';
import { Link, Navigate, Outlet, useLocation, useRoutes } from 'react-router';
import { useGet, type Page } from '@oneclub/api-client';
import { useTranslation } from '@oneclub/i18n';
import { enqueue, useOnline } from '@oneclub/offline';
import {
  Brand, Card, HeaderActions, Icon, NotFoundPage, NotificationsPage, ProfilePage, Skeleton, TextArea, logoOf, useAuth, useBootstrap, useNavigation, useToast,
  type NavItem,
} from '@oneclub/shell';
import {
  BagDropPage, BagStoragePage, CaddyAssignmentPage, CaddyQueuePage, CartAssignmentPage, CartReadinessPage, FrontDeskFoliosPage,
  FrontDeskPaymentsPage, GuestPage, LockersPage, OpsCheckInPage, OpsCheckOutPage, OpsTeeSheetPage, OpsTiles, StarterQueuePage,
} from '../ops/golf';
import { DeskNewBookingPage, FrontDeskPage } from '../ops/desk';
import { KioskCheckInPage } from '../ops/kiosk';
import { MaintenancePage } from '../ops/maintenance';
import { CaddyHistoryPage } from '../ops/caddy';
import { TeeHousesPage } from '../ops/teehouse';
import { P2_OPS_ROUTES, P2Tiles } from '../ops/p2';
import { P3_OPS_ROUTES, P3Tiles } from '../ops/p3';
import { ConnectivityChip, OUTLET_KEY, SyncPage, read, write } from '../offline';
import '../pos/pos.css';

// Operational area (`/ops`): touch-first, offline-capable (Technical Doc §6.4, PRD FR-SH-05).

/** POS Cashier: a full-screen app of its own (pos/), loaded when opened. */
const PosApp = lazy(() => import('../pos'));

/** Entries kept at the foot of the rail. */
const RAIL_FOOT = new Set(['/ops/sync', '/ops/notifications']);

/** The area of the current page: the menu entry whose own path or one of its pages is the longest match;
 * a page listed in two menus stays in the menu it was opened from (demo feedback 10 Oct 2026 #22). */
function currentArea(items: NavItem[], pathname: string, previous?: string) {
  const hit = (p: string) => (p === '/ops' ? pathname === '/ops' : pathname === p || pathname.startsWith(`${p}/`));
  let area: NavItem | undefined;
  let best = -1;
  for (const i of items) {
    for (const p of [i.path, ...(i.children ?? []).filter((c) => !c.section).map((c) => c.path)]) {
      if (hit(p) && (p.length > best || (p.length === best && i.key === previous))) { best = p.length; area = i; }
    }
  }
  return area;
}

/**
 * Touch-first frame in the POS look (product owner, 8 Oct 2026): the blue rail
 * lists the user's areas (Front Desk, Starter / Marshal, Caddy Master, Golf
 * Staff, ESS …) from the server menu, the top bar keeps the club, the
 * connection chip and the user menu, and the pages of the current area sit
 * in tabs above the content (Technical Doc §6.5).
 */
function OpsLayout() {
  const { t } = useTranslation();
  const online = useOnline();
  const b = useBootstrap();
  const { pathname } = useLocation();
  const items = useNavigation('ops').data?.items ?? [];
  const last = useRef<string | undefined>(undefined);
  const area = currentArea(items, pathname, last.current);
  last.current = area?.key;
  const tabs = (area?.children ?? []).filter((c, i, all) => !c.section && all.findIndex((x) => x.path === c.path) === i);
  const entry = (i: NavItem) => (
    <Link key={i.key} to={i.path} title={i.label} aria-current={area?.key === i.key ? 'page' : undefined}>
      <Icon name={i.icon ?? 'apps'} size={24} /><span>{i.label}</span>
    </Link>
  );
  return (
    <div className="pos pos-ops">
      <nav className="pos-rail" aria-label="Operational">
        <Link to="/ops" className="pos-rail-logo" aria-label="Home"><img src={logoOf(b.branding)} alt="" /></Link>
        <div className="pos-rail-items">{items.filter((i) => !RAIL_FOOT.has(i.path)).map(entry)}</div>
        {items.filter((i) => RAIL_FOOT.has(i.path)).map(entry)}
      </nav>
      <main className="pos-main">
        <header className="pos-ops-top">
          <Brand />
          <span className="pos-spacer" />
          <ConnectivityChip />
          <HeaderActions property={false} />
        </header>
        {tabs.length > 1 && (
          <nav className="pos-ops-tabs" aria-label={area?.label}>
            {tabs.map((c) => <Link key={c.key} to={c.path} className="pos-chip" aria-current={pathname === c.path ? 'page' : undefined}>{c.label}</Link>)}
          </nav>
        )}
        {!online && <div className="pos-banner" data-tone="warn" role="status"><Icon name="cloud_off" size={20} />{t('common.offline')}</div>}
        <div className="pos-body"><Outlet /></div>
      </main>
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
      { path: 'starter/dispatch', element: <StarterQueuePage view="dispatch" /> },
      { path: 'starter/check-in', element: <OpsCheckInPage /> },
      { path: 'front-desk/check-in', element: <OpsCheckInPage /> },
      { path: 'starter/rounds', element: <StarterQueuePage view="rounds" /> },
      { path: 'starter/tee-sheet', element: <OpsTeeSheetPage /> },
      { path: 'check-in', element: <OpsCheckInPage /> },
      { path: 'kiosk', element: <KioskCheckInPage /> },
      { path: 'check-out', element: <OpsCheckOutPage /> },
      { path: 'caddy', element: <CaddyQueuePage /> },
      { path: 'caddy/availability', element: <CaddyQueuePage view="availability" /> },
      { path: 'caddy/rotation', element: <CaddyQueuePage /> },
      { path: 'caddy/assignment', element: <CaddyAssignmentPage /> },
      { path: 'caddy/history', element: <CaddyHistoryPage /> },
      { path: 'front-desk', element: <FrontDeskPage /> },
      { path: 'front-desk/new', element: <DeskNewBookingPage /> },
      { path: 'tee-houses', element: <TeeHousesPage /> },
      { path: 'front-desk/guest', element: <GuestPage /> },
      { path: 'front-desk/payments', element: <FrontDeskPaymentsPage /> },
      { path: 'front-desk/folios', element: <FrontDeskFoliosPage /> },
      { path: 'golf-staff', element: <BagDropPage /> },
      { path: 'golf-staff/bag-storage', element: <BagStoragePage /> },
      { path: 'golf-staff/lockers', element: <LockersPage /> },
      { path: 'golf-staff/golf-carts', element: <CartReadinessPage /> },
      { path: 'golf-staff/golf-cart-assignment', element: <CartAssignmentPage /> },
      { path: 'golf-staff/maintenance', element: <MaintenancePage /> },
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
