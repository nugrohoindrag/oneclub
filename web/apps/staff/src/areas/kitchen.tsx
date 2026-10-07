import { useMemo, useState } from 'react';
import { NavLink, Outlet, useRoutes } from 'react-router';
import { qs, useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDateTime } from '@oneclub/i18n';
import { ErrorAlert, Icon, NotFoundPage, NotificationsPage, ProfilePage, Skeleton, logoOf, useAuth, useBootstrap, useToast } from '@oneclub/shell';
import { useLive } from '../live';
import { today } from '../p1/common';
import { TodayLabel, elapsed, useTick } from '../pos/shared';
import '../pos/pos.css';

// Kitchen Display area (`/kitchen`, EP-21): the screen in the kitchen or bar,
// in the look of the POS Cashier (blue rail, POS tokens). PRD P3 FR-BEO-04:
// the dishes of the issued BEOs appear on the same display at their serving
// time (Banquet Production).

type Row = Record<string, unknown>;
type Ticket = Schemas['Ticket'];

/** Blue rail: board, banquet production, notifications, profile and logout; the board gets the screen. */
function KitchenLayout() {
  const b = useBootstrap();
  const { can, logout } = useAuth();
  return (
    <div className="pos">
      <nav className="pos-rail" aria-label="Kitchen">
        <span className="pos-rail-logo"><img src={logoOf(b.branding)} alt={b.branding.appName} /></span>
        <NavLink to="/kitchen" end aria-label="Orders" title="Orders"><Icon name="soup_kitchen" size={26} /></NavLink>
        {can('banquet.production.view') && <NavLink to="/kitchen/banquet" aria-label="Banquet Production" title="Banquet Production"><Icon name="celebration" size={26} /></NavLink>}
        <NavLink to="/kitchen/notifications" aria-label="Notifications" title="Notifications"><Icon name="notifications" size={26} /></NavLink>
        <NavLink to="/kitchen/profile" aria-label="Profile" title="Profile"><Icon name="person" size={26} /></NavLink>
        <span className="pos-spacer" />
        <button onClick={() => void logout()} aria-label="Log out" title="Log out"><Icon name="logout" size={26} /></button>
      </nav>
      <main className="pos-main"><Outlet /></main>
    </div>
  );
}

const SOURCE: Record<string, string> = { pos: 'POS', member_app: 'Member App', caddy_tablet: 'Caddy Tablet', vip_suite: 'VIP Suite', meeting_catering: 'Meeting', website: 'Website',
  driving_range: 'Driving Range' };
const LATE_MIN = 15; // a ticket waiting longer turns red

/** Where the dishes go: table, hole, pickup … */
function destination(t: Ticket) {
  if (t.tableNo) return { badge: t.tableNo.split(',')[0].trim(), label: `Table ${t.tableNo}` };
  if (t.servingDestination === 'hole') return { badge: `H${t.destinationRef ?? ''}`, label: `Hole ${t.destinationRef ?? ''}` };
  const label = (t.destinationRef ?? t.servingDestination).replace(/_/g, ' ');
  return { badge: label.slice(0, 2).toUpperCase(), label };
}

/** Kitchen Display: POS tickets per station, Received → Preparing → Ready → Served. */
function OrdersBoard() {
  const toast = useToast();
  const [station, setStation] = useState('');
  const tickets = useGet<Page<Ticket>>(`/api/v1/commercial/kitchen-orders${qs({})}`, { refetchInterval: 30_000 });
  useLive('/api/v1/commercial/kds/stream', ['commercial.kds'], useMemo(() => () => void tickets.refetch(), [tickets]));
  const state = useSend<Row>('POST', (b) => `/api/v1/commercial/kitchen-orders/${b.id}:state`, ['/api/v1/commercial/kitchen-orders']);
  useTick(30_000);
  const all = tickets.data?.items ?? [];
  const stations = [...new Set(all.map((t) => t.station))].sort();
  const shown = all.filter((t) => !station || t.station === station);
  const cols: [string, string, string][] = [['received', 'Received', 'Start'], ['preparing', 'Preparing', 'Ready'], ['ready', 'Ready to serve', 'Served']];
  const next: Record<string, string> = { received: 'preparing', preparing: 'ready', ready: 'served' };
  return (
    <>
      <div className="pos-head">
        <h1>Kitchen Display</h1>
        <span className="pos-muted">{shown.filter((t) => t.status !== 'served').length} open tickets</span>
        <span className="pos-spacer" />
        <TodayLabel />
      </div>
      <div className="pos-cats" role="group" aria-label="Stations">
        <button className="pos-chip" aria-pressed={!station} onClick={() => setStation('')}><Icon name="grid_view" size={20} />All Stations</button>
        {stations.map((s) => <button key={s} className="pos-chip" aria-pressed={station === s} onClick={() => setStation(s)}>
          <Icon name={/bar|drink/.test(s) ? 'local_bar' : 'skillet'} size={20} />{s.replace(/_/g, ' ')}</button>)}
      </div>
      <div className="pos-body">
        <ErrorAlert error={tickets.error ?? state.error} />
        {tickets.isLoading ? <Skeleton rows={6} /> : (
          <div className="pos-kds">
            {cols.map(([s, label, action]) => {
              const list = shown.filter((t) => t.status === s || (s === 'ready' && t.status === 'out_for_delivery'));
              return (
                <section key={s} className="pos-kds-col" aria-label={label}>
                  <header><span className="pos-dot" data-kds={s} />{label}<span className="pos-kds-count">{list.length}</span></header>
                  {list.map((t) => {
                    const d = destination(t);
                    const late = s !== 'ready' && (Date.now() - new Date(t.receivedAt).getTime()) / 60_000 > LATE_MIN;
                    return (
                      <article key={t.id} className="pos-ticket-card" data-kds={s}>
                        <div className="pos-ticket-card-head">
                          <span className="pos-kds-badge" data-kds={s}>{d.badge}</span>
                          <div style={{ flex: 1, minWidth: 0 }}>
                            <strong>{d.label}</strong>
                            <div className="pos-muted" style={{ fontSize: 12 }}>{t.orderNo} · {SOURCE[t.source] ?? t.source} · {t.station}</div>
                          </div>
                          <span className="pos-kds-timer" data-late={late || undefined} title={formatDateTime(t.receivedAt)}><Icon name="timer" size={14} />{elapsed(t.receivedAt)}</span>
                        </div>
                        {t.dueAt && (t.orderType === 'pre_order' || t.orderType === 'catering' || new Date(t.dueAt).getTime() > Date.now()) && <div className="pos-kitchen" data-kitchen="preparing" style={{ alignSelf: 'flex-start' }}>Due {formatDateTime(t.dueAt)}</div>}
                        <ul className="pos-kds-items">
                          {(t.items as unknown as Row[]).map((i, n) => (
                            <li key={n}><span className="pos-kds-qty">{String(Number(i.quantity))}x</span><span>{String(i.name)}
                              {i.notes ? <em>{String(i.notes)}</em> : null}</span></li>
                          ))}
                        </ul>
                        <button className="pos-btn" data-variant={s === 'ready' ? 'outline' : undefined} data-block disabled={state.isPending}
                          onClick={() => state.mutate({ id: t.id, state: next[s] }, { onSuccess: () => toast(`${t.orderNo}: ${action}`) })}>{action}</button>
                      </article>
                    );
                  })}
                  {list.length === 0 && <div className="pos-empty">No tickets</div>}
                </section>
              );
            })}
          </div>
        )}
      </div>
    </>
  );
}

/** Banquet Production (FR-BEO-04): dishes of the issued BEOs per serving time; a BEO revision replaces the list. */
function BanquetProductionBoard() {
  const toast = useToast();
  const { can } = useAuth();
  const [day, setDay] = useState(today());
  const [station, setStation] = useState('');
  const q = useGet<Page<Row>>(`/api/v1/banquet/production${qs({ date: day, 'filter[station]': station })}`, { refetchInterval: 30_000 });
  const move = useSend<Row>('POST', (b) => `/api/v1/banquet/production-items/${String(b.id)}:status`, ['/api/v1/banquet/production']);
  const cols: [string, string][] = [['pending', 'To produce'], ['in_progress', 'In progress'], ['ready', 'Ready']];
  const next: Record<string, [string, string]> = { pending: ['in_progress', 'Start'], in_progress: ['ready', 'Ready'], ready: ['served', 'Served'] };
  const kds: Record<string, string> = { pending: 'received', in_progress: 'preparing', ready: 'ready' };
  return (
    <>
      <div className="pos-head">
        <h1>Banquet Production</h1>
        <span className="pos-muted">Dishes of the issued BEOs by serving time</span>
        <span className="pos-spacer" />
        <input className="pos-input" type="date" style={{ width: 180 }} value={day} onChange={(e) => setDay(e.target.value)} aria-label="Date" />
        <input className="pos-input" style={{ width: 200 }} value={station} onChange={(e) => setStation(e.target.value)} placeholder="Station (buffet, kitchen…)" aria-label="Station" />
      </div>
      <div className="pos-body">
        <ErrorAlert error={q.error ?? move.error} />
        {q.isSuccess && (q.data?.items.length ?? 0) === 0 && <div className="pos-empty"><Icon name="celebration" size={36} />No banquet dishes for this day.</div>}
        <div className="pos-kds">
          {cols.map(([s, label]) => {
            const list = (q.data?.items ?? []).filter((p) => p.status === s);
            return (
              <section key={s} className="pos-kds-col" aria-label={label}>
                <header><span className="pos-dot" data-kds={kds[s]} />{label}<span className="pos-kds-count">{list.length}</span></header>
                {list.map((p) => (
                  <article key={String(p.id)} className="pos-ticket-card" data-kds={kds[s]}>
                    <div className="pos-ticket-card-head">
                      <span className="pos-kds-badge" data-kds={kds[s]}>{String(p.quantity)}</span>
                      <div style={{ flex: 1, minWidth: 0 }}>
                        <strong>{String(p.name)}</strong>
                        <div className="pos-muted" style={{ fontSize: 12 }}>{String(p.eventNumber)} {String(p.eventTitle)} · BEO v{String(p.beoVersion)}{p.station ? ` · ${String(p.station)}` : ''}</div>
                      </div>
                      <span className="pos-kds-timer"><Icon name="schedule" size={14} />{formatDateTime(String(p.serveAt))}</span>
                    </div>
                    {can('banquet.production.update') && (
                      <button className="pos-btn" data-variant={s === 'ready' ? 'outline' : undefined} data-block disabled={move.isPending}
                        onClick={() => move.mutate({ id: p.id, status: next[s][0] }, { onSuccess: () => toast('Updated') })}>{next[s][1]}</button>
                    )}
                  </article>
                ))}
                {list.length === 0 && <div className="pos-empty">Nothing here</div>}
              </section>
            );
          })}
        </div>
      </div>
    </>
  );
}

/** Notifications and profile inside the kitchen frame. */
function Page({ children }: { children: React.ReactNode }) {
  return <div className="pos-body" style={{ paddingTop: 24 }}>{children}</div>;
}

const routes = [
  {
    element: <KitchenLayout />,
    children: [
      { index: true, element: <OrdersBoard /> },
      { path: 'banquet', element: <BanquetProductionBoard /> },
      { path: 'notifications', element: <Page><NotificationsPage /></Page> },
      { path: 'profile', element: <Page><ProfilePage showPin /></Page> },
      { path: '*', element: <Page><NotFoundPage /></Page> },
    ],
  },
];

export default function KitchenArea() {
  return useRoutes(routes);
}
