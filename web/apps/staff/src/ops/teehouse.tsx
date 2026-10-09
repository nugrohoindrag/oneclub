import React, { useMemo, useState } from 'react';
import { useNavigate } from 'react-router';
import { qs, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDateTime } from '@oneclub/i18n';
import { Card, DataTable, ErrorAlert, Icon, StatusPill, useAuth, useToast } from '@oneclub/shell';
import { OUTLET_KEY, write } from '../offline';
import { Btn, Head, money, today } from './golf';

// Tee Houses (demo feedback 9 Oct 2026): six on-course F&B outlets in two
// groups of three. Per tee house the orders of the day and the open orders
// by service status (order tracking), the stock against par (stock
// monitoring), and the way into its POS. Each tee house has its own
// inventory warehouse; POS sales consume there.

type R = Record<string, unknown> & { id: string };

const SERVICE: [string, string][] = [['new', 'New'], ['sent', 'Sent'], ['preparing', 'Preparing'], ['ready', 'Ready'], ['out_for_delivery', 'On the way']];

export function TeeHousesPage() {
  const { can } = useAuth();
  const toast = useToast();
  const nav = useNavigate();
  const houses = useGet<Page<R>>('/api/v1/commercial/tee-houses', { refetchInterval: 20_000 });
  const setup = useSend<Record<string, unknown>>('POST', '/api/v1/commercial/tee-houses:setup', ['/api/v1/commercial']);
  const [group, setGroup] = useState('');
  const list = houses.data?.items ?? [];
  const groups = [...new Set(list.map((h) => String(h.group)))];
  const shown = list.filter((h) => !group || h.group === group);
  const openPOS = (h: R) => { write(OUTLET_KEY, h.id); nav('/ops/pos'); };
  return (
    <div className="oc-stack">
      <Head title="Tee Houses" help="On-course F&B: orders, order tracking and stock of every tee house." />
      <ErrorAlert error={houses.error ?? setup.error} />
      {houses.data && list.length === 0 && (
        <Card title="No tee houses yet" icon="storefront">
          <p style={{ marginTop: 0 }}>Set up 6 tee houses in two groups (A: front nine, B: back nine), each with the Halfway House menu and its own stock.</p>
          {can('commercial.outlet.create') && <Btn label="Set up 6 tee houses" kind="primary" disabled={setup.isPending}
            onClick={() => setup.mutate({}, { onSuccess: () => { toast('Tee houses ready'); void houses.refetch(); } })} />}
        </Card>
      )}
      {groups.length > 1 && (
        <div className="oc-row-wrap" role="tablist">
          <Btn label="All" kind={group ? 'neutral' : 'ink'} onClick={() => setGroup('')} />
          {groups.map((g) => <Btn key={g} label={`Group ${g} · ${String(list.find((h) => h.group === g)?.area ?? '')}`} kind={group === g ? 'ink' : 'neutral'} onClick={() => setGroup(g)} />)}
        </div>
      )}
      <div className="oc-row-wrap" style={{ alignItems: 'stretch' }}>
        {shown.map((h) => {
          const open = (h.open as Record<string, number>) ?? {};
          const openCount = Object.values(open).reduce((a, b) => a + b, 0);
          return (
            <div key={h.id} className="oc-card oc-stack" style={{ flex: '1 1 260px', gap: 8 }}>
              <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
                <Icon name="storefront" size={22} />
                <strong style={{ fontSize: 18 }}>{String(h.name)}</strong>
                <span className="oc-small oc-muted">Group {String(h.group)}</span>
              </div>
              <span>{String(h.ordersToday)} orders today · {money(h.salesToday)}</span>
              <div className="oc-row-wrap">
                {SERVICE.filter(([k]) => open[k]).map(([k, l]) => <span key={k} className="oc-chip">{l} {open[k]}</span>)}
                {openCount === 0 && <span className="oc-small oc-muted">No open order</span>}
              </div>
              <div className="oc-row-wrap">
                {Number(h.outOfStock) > 0 && <StatusPill status="out-of-stock" />}
                {Number(h.lowStock) > 0 && <span className="oc-alert oc-alert-warning" style={{ padding: '2px 8px' }}>{String(h.lowStock)} low</span>}
                {Number(h.outOfStock) > 0 && <span className="oc-alert oc-alert-error" style={{ padding: '2px 8px' }}>{String(h.outOfStock)} out</span>}
              </div>
              <div className="oc-row-wrap"><Btn label="Open POS" kind="primary" onClick={() => openPOS(h)} /></div>
            </div>
          );
        })}
      </div>
      {list.length > 0 && <OrderTracking houses={shown} />}
      {list.length > 0 && <StockMonitor houses={shown} />}
    </div>
  );
}

/** Order tracking: the open orders of the tee houses, oldest first. */
function OrderTracking({ houses }: { houses: R[] }) {
  const orders = useGet<Page<R>>(`/api/v1/commercial/orders${qs({ date: today(), limit: 200 })}`, { refetchInterval: 15_000 });
  const ids = useMemo(() => new Set(houses.map((h) => h.id)), [houses]);
  const rows = (orders.data?.items ?? []).filter((o) => ids.has(String(o.outletId)) && o.serviceStatus !== 'served' && !['voided', 'refunded'].includes(String(o.status)))
    .sort((a, b) => String(a.createdAt).localeCompare(String(b.createdAt)));
  return (
    <Card title="Order tracking" icon="receipt_long">
      <DataTable rows={rows} loading={orders.isLoading} empty="No open orders at the tee houses." columns={[
        { key: 'createdAt', header: 'Ordered', render: (o) => formatDateTime(String(o.createdAt)) }, { key: 'outletName', header: 'Tee house' },
        { key: 'orderNo', header: 'Order' }, { key: 'customerName', header: 'Player', render: (o) => `${String(o.customerName ?? '—')}${o.reference ? ` · ${String(o.reference)}` : ''}` },
        { key: 'destinationRef', header: 'To', render: (o) => (o.servingDestination === 'hole' ? String(o.destinationRef ?? 'Hole') : 'Pick-up') },
        { key: 'lines', header: 'Items', render: (o) => ((o.lines as R[] | undefined) ?? []).map((l) => `${String(l.quantity)}× ${String(l.name)}`).join(', ') || '—' },
        { key: 'total', header: 'Total', align: 'right', render: (o) => money(o.total) },
        { key: 'serviceStatus', header: 'Status', render: (o) => <StatusPill status={String(o.serviceStatus).replace(/_/g, '-')} /> }]} />
      <ErrorAlert error={orders.error} />
    </Card>
  );
}

/** Stock monitoring: each tee house's stock against its par level. */
function StockMonitor({ houses }: { houses: R[] }) {
  const stock = useGet<Page<R>>('/api/v1/commercial/tee-houses/stock', { refetchInterval: 60_000 });
  const [lowOnly, setLowOnly] = useState(true);
  const ids = new Set(houses.map((h) => h.id));
  const rows = (stock.data?.items ?? []).filter((l) => ids.has(String(l.outletId)) && (!lowOnly || l.status !== 'ok'))
    .map((l) => ({ ...l, id: `${String(l.warehouseId)}-${String(l.itemId)}` }) as R);
  return (
    <Card title="Stock monitoring" icon="inventory_2" actions={<Btn label={lowOnly ? 'Show all items' : 'Low stock only'} onClick={() => setLowOnly(!lowOnly)} />}>
      <DataTable rows={rows} loading={stock.isLoading} empty={lowOnly ? 'Every tee house is stocked to par.' : 'No stock recorded yet.'} columns={[
        { key: 'warehouse', header: 'Tee house' }, { key: 'itemName', header: 'Item' },
        { key: 'onHand', header: 'On hand', align: 'right', render: (l) => `${String(l.onHand)} ${String(l.unit)}` },
        { key: 'parLevel', header: 'Par', align: 'right', render: (l) => (l.parLevel ? String(l.parLevel) : '—') },
        { key: 'refill', header: 'To refill', align: 'right', render: (l) => (Number(l.refill) > 0 ? <strong>{String(l.refill)}</strong> : '—') },
        { key: 'status', header: 'Status', render: (l) => <StatusPill status={l.status === 'ok' ? 'ok' : l.status === 'low' ? 'low-stock' : 'out-of-stock'} /> }]} />
      <ErrorAlert error={stock.error} />
    </Card>
  );
}
