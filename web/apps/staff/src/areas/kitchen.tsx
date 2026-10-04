import { useMemo, useState } from 'react';
import { Outlet, useRoutes } from 'react-router';
import { qs, useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDateTime } from '@oneclub/i18n';
import { Brand, ErrorAlert, HeaderActions, NotFoundPage, NotificationsPage, ProfilePage, TextField, useToast } from '@oneclub/shell';
import { useLive } from '../live';

// Kitchen Display area (`/kitchen`, EP-21): the screen in the kitchen or bar,
// full width without the Operational menus (Technical Doc §6.1).

type Row = Record<string, unknown>;

/** Brand and the user menu only (PIN shift change, logout); the board gets the screen. */
function KitchenLayout() {
  return (
    <div className="oc-topnav-frame" style={{ maxWidth: 'none' }}>
      <header className="oc-topbar">
        <Brand />
        <span className="oc-spacer" />
        <HeaderActions property={false} />
      </header>
      <Outlet />
    </div>
  );
}

function KitchenBoardPage() {
  const toast = useToast();
  const [station, setStation] = useState('');
  const tickets = useGet<Page<Schemas['Ticket']>>(`/api/v1/commercial/kitchen-orders${qs({ station })}`, { refetchInterval: 30_000 });
  useLive('/api/v1/commercial/kds/stream', ['commercial.kds'], useMemo(() => () => void tickets.refetch(), [tickets]));
  const state = useSend<Row>('POST', (b) => `/api/v1/commercial/kitchen-orders/${b.id}:state`, ['/api/v1/commercial/kitchen-orders']);
  const cols: [string, string][] = [['received', 'Received'], ['preparing', 'Preparing'], ['ready', 'Ready']];
  const next: Record<string, string> = { received: 'preparing', preparing: 'ready', ready: 'served' };
  return (
    <div className="oc-stack">
      <div className="oc-page-head"><div><h1>Kitchen</h1></div><span className="oc-spacer" />
        <div style={{ width: 200 }}><TextField label="Station" value={station} onChange={setStation} placeholder="kitchen, bar…" /></div></div>
      <ErrorAlert error={state.error} />
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(260px, 1fr))', gap: 16 }}>
        {cols.map(([s, label]) => (
          <div key={s} className="oc-stack">
            <h3>{label}</h3>
            {(tickets.data?.items ?? []).filter((t) => t.status === s).map((t) => (
              <div key={t.id} className="oc-card">
                <div className="oc-row"><strong>{t.orderNo}</strong><span className="oc-spacer" /><span className="oc-small">{t.tableNo ? `Table ${t.tableNo}` : t.destinationRef ?? t.servingDestination}</span></div>
                <div className="oc-small oc-muted">{t.outletName} · {formatDateTime(t.receivedAt)}</div>
                <ul style={{ margin: '8px 0', paddingLeft: 18 }}>{(t.items as unknown as Row[]).map((i, n) => <li key={n}>{String(i.quantity)} × {String(i.name)}{i.notes ? ` (${String(i.notes)})` : ''}</li>)}</ul>
                <button className="oc-btn oc-btn-ink oc-btn-sm oc-btn-block" onClick={() => state.mutate({ id: t.id, state: next[s] }, { onSuccess: () => toast('Updated') })}>
                  {next[s] === 'served' ? 'Served' : next[s] === 'ready' ? 'Ready' : 'Start'}</button>
              </div>
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}

const routes = [
  {
    element: <KitchenLayout />,
    children: [
      { index: true, element: <KitchenBoardPage /> },
      { path: 'notifications', element: <NotificationsPage /> },
      { path: 'profile', element: <ProfilePage showPin /> },
      { path: '*', element: <NotFoundPage /> },
    ],
  },
];

export default function KitchenArea() {
  return useRoutes(routes);
}
