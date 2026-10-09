import React, { useEffect, useRef, useState } from 'react';
import { useNavigate } from 'react-router';
import { qs, request, uuidv7, useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDateTime, formatNumber } from '@oneclub/i18n';
import { ErrorAlert, Icon, Skeleton, useAuth, useToast } from '@oneclub/shell';
import { useOnline } from '@oneclub/offline';
import { localTotal, useCachedGet, useLocalOrders } from './offline';
import { PaymentDialog, SuccessDialog } from './pay';
import { KITCHEN, ProductImage, TodayLabel, money, useMenu, useOutletId, useShift, type Order } from './shared';

// Payment Confirm (open orders waiting for payment) and Order History
// (today's settled orders with details, receipt and refund request).

// an on-course order is the player's, with the booking (demo feedback 10 Oct 2026 #34)
const who = (o: Order) => `${o.customerName ?? (o.tableNo ? `Table ${o.tableNo}` : 'Walk-in guest')}${o.reference ? ` · ${o.reference}` : ''}`;

export function PaymentConfirmPage() {
  const outletId = useOutletId();
  const nav = useNavigate();
  const shiftQ = useShift(outletId);
  const online = useOnline();
  const locals = useLocalOrders(outletId);
  const list = useCachedGet<Page<Order>>(`/api/v1/commercial/orders${qs({ 'filter[outletId]': outletId, 'filter[status]': 'open', limit: 100 })}`, `open:${outletId}`,
    { refetchInterval: 15_000 });
  const [paying, setPaying] = useState<Order | null>(null);
  const [paid, setPaid] = useState<{ order: Order; method: string } | null>(null);
  const [error, setError] = useState<unknown>(null);
  const accept = async (id: string) => {
    setError(null);
    if (!online) { nav(`/ops/pos/order/${id}`); return; } // offline payment from the order
    try { setPaying(await request<Order>('GET', `/api/v1/commercial/orders/${id}`)); } catch (e) { setError(e); }
  };
  // offline: orders made on this device join the list; orders paid offline leave it
  const paidHere = new Set(locals.filter((l) => l.paid).map((l) => l.id));
  const added = new Map(locals.map((l) => [l.id, localTotal(outletId, l.lines)]));
  const items = [
    ...locals.filter((l) => !l.server && !l.paid).map((l) => ({ id: l.id, orderNo: l.orderNo, customerName: l.customerName ?? null, tableNo: l.tableCodes?.join(', ') ?? null,
      createdAt: l.createdAt, total: '0', serviceStatus: 'new', billedAt: null } as unknown as Order)),
    ...(list.data?.items ?? []).filter((o) => !paidHere.has(o.id)),
  ].map((o) => (added.has(o.id) ? { ...o, total: String(Number(o.total) + (added.get(o.id) ?? 0)) } : o));
  return (
    <>
      <div className="pos-head"><h1>Payment Confirm</h1><span className="pos-muted">{items.length} waiting</span><span className="pos-spacer" /><TodayLabel /></div>
      <div className="pos-body">
        <ErrorAlert error={(online ? list.error : null) ?? error} />
        {list.isLoading ? <Skeleton rows={6} /> : items.length === 0 ? <div className="pos-empty"><Icon name="task_alt" size={40} />No orders are waiting for payment.</div> : (
          <div className="pos-cards">
            {items.map((o) => (
              <div key={o.id} className="pos-card">
                <div className="pos-card-top">
                  <div style={{ flex: 1, minWidth: 0 }}><strong>{who(o)}</strong><div className="pos-muted">{formatDateTime(o.createdAt)}</div></div>
                  <div style={{ textAlign: 'right' }}><div className="pos-muted">{o.orderNo}</div><div className="pos-waiting">{o.billedAt ? 'Billed' : 'Waiting'}</div>
                    <span className="pos-kitchen" data-kitchen={o.serviceStatus} style={{ marginTop: 4 }}>{KITCHEN[o.serviceStatus] ?? ''}</span></div>
                </div>
                <div className="pos-card-total"><span className="pos-muted">Total Payment</span><strong>{money(o.total)}</strong></div>
                <div className="pos-card-actions">
                  <button className="pos-btn" data-variant="soft" data-size="sm" onClick={() => nav(`/ops/pos/order/${o.id}`)}>Check</button>
                  <button className="pos-btn" data-size="sm" disabled={Number(o.total) <= 0} onClick={() => void accept(o.id)}>Accept</button>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
      {paying && <PaymentDialog order={paying} shiftId={shiftQ.shift?.id as string | undefined} outletId={outletId} onClose={() => setPaying(null)}
        onShift={() => void shiftQ.refetch()} onPaid={(o, method) => { setPaying(null); setPaid({ order: o, method }); void list.refetch(); }} />}
      {paid && <SuccessDialog order={paid.order} method={paid.method} onClose={() => setPaid(null)} />}
    </>
  );
}

function OrderDetail({ id, onChanged }: { id: string; onChanged: () => void }) {
  const { can } = useAuth();
  const toast = useToast();
  const outletId = useOutletId();
  const menu = useMenu(outletId);
  const q = useGet<Order>(`/api/v1/commercial/orders/${id}`);
  const [error, setError] = useState<unknown>(null);
  const o = q.data;
  if (!o) return <div className="pos-body"><ErrorAlert error={q.error} /><Skeleton rows={6} /></div>;
  const img = new Map((menu.data?.items ?? []).map((p) => [p.productId, p]));
  const lines = o.lines.filter((l) => l.status === 'active');
  const receipt = async () => {
    const r = await request<{ text: string }>('GET', `/api/v1/commercial/orders/${o.id}/receipt`);
    const w = window.open('', '_blank', 'width=380,height=640');
    if (!w) return;
    const pre = w.document.createElement('pre');
    pre.textContent = r.text;
    w.document.body.appendChild(pre);
    w.print();
  };
  const refund = async () => {
    const reason = window.prompt(`Request a refund of ${o.orderNo}? Reason`);
    if (!reason) return;
    setError(null);
    try {
      await request('POST', `/api/v1/commercial/orders/${o.id}:refund`, { reason }, { 'Idempotency-Key': uuidv7() });
      toast('Refund requested for approval');
      onChanged();
    } catch (e) { setError(e); }
  };
  return (
    <>
      <div className="pos-order-head">
        <div style={{ flex: 1 }}>
          <h2>Details Order #{o.orderNo}</h2>
          <span className="pos-blue">{who(o)}, {o.tableNo ? `Table (${o.tableNo})` : o.orderType === 'on_course' ? `on course · ${o.destinationRef ?? ''}` : o.orderType.replace('_', ' ')}</span>
          {o.status === 'charged' && o.orderType === 'on_course' && <div className="pos-muted">Charged to the golfer's bill — no payment here</div>}
          <div className="pos-muted">{formatDateTime(o.createdAt)} · <span className="pos-status-text" data-status={o.status}>{o.status}</span></div>
        </div>
      </div>
      <div className="pos-order-lines">
        {lines.map((l) => (
          <div key={l.id} className="pos-line-wrap">
            <div className="pos-line" style={{ cursor: 'default' }}>
              <ProductImage item={img.get(l.productId)} className="pos-line-img" />
              <span className="pos-line-body">
                <span className="pos-line-name">{l.name}</span>
                <span className="pos-line-note">{[l.guestName ? `For ${l.guestName}` : '', l.notes ?? ''].filter(Boolean).join(' · ') || '—'}</span>
                <span className="pos-line-foot"><span className="pos-muted">{formatNumber(Number(l.quantity))}x</span><span className="pos-spacer" />
                  <strong className="pos-blue">{money(l.totalAmount)}</strong></span>
              </span>
            </div>
          </div>
        ))}
      </div>
      <div className="pos-summary">
        <div className="pos-ticket">
          <div className="pos-ticket-row"><span>Items</span><strong>{formatNumber(lines.reduce((s, l) => s + Number(l.quantity), 0))}</strong></div>
          <div className="pos-ticket-row"><span>Discount sales</span>
            <strong>−{money(lines.reduce((s, l) => s + Number(l.discountAmount) + Number(l.promotionDiscount ?? 0) + Number(l.tierDiscount ?? 0), 0))}</strong></div>
          <div className="pos-ticket-row"><span>Total sales tax</span><strong>{money(lines.reduce((s, l) => s + Number(l.taxAmount) + Number(l.serviceAmount), 0))}</strong></div>
          <div className="pos-ticket-cut" />
          <div className="pos-ticket-total"><span>Total</span><span>{money(o.total)}</span></div>
        </div>
      </div>
      <div className="pos-order-foot">
        <ErrorAlert error={error} />
        <div className="pos-row">
          <button className="pos-btn" data-variant="outline" onClick={() => void receipt()}><Icon name="print" size={20} />Receipt</button>
          {can('commercial.order.refund') && o.status === 'paid' && <button className="pos-btn" data-variant="soft" onClick={() => void refund()}>Refund</button>}
        </div>
      </div>
    </>
  );
}

export function HistoryPage() {
  const outletId = useOutletId();
  const [date, setDate] = useState(() => new Date().toLocaleDateString('sv'));
  const [q, setQ] = useState('');
  const list = useGet<Page<Order>>(`/api/v1/commercial/orders${qs({ 'filter[outletId]': outletId, 'filter[status]': 'paid,charged,voided,refunded', date, limit: 200 })}`);
  const [sel, setSel] = useState('');
  const items = (list.data?.items ?? []).filter((o) => !q || `${o.orderNo} ${who(o)} ${o.tableNo ?? ''}`.toLowerCase().includes(q.toLowerCase()));
  const current = sel || items[0]?.id || '';
  return (
    <div className="pos-split">
      <section className="pos-split-main">
        <div className="pos-head"><h1>Order History</h1><span className="pos-spacer" />
          <input className="pos-input" type="date" style={{ width: 180 }} value={date} onChange={(e) => setDate(e.target.value)} aria-label="Date" /></div>
        <form className="pos-searchbar" role="search" onSubmit={(e) => e.preventDefault()}>
          <input className="pos-input" placeholder="Search order, guest or table..." value={q} onChange={(e) => setQ(e.target.value)} aria-label="Search orders" />
          <button className="pos-btn" style={{ minWidth: 120 }}>Search</button>
        </form>
        <div className="pos-body">
          <ErrorAlert error={list.error} />
          {list.isLoading ? <Skeleton rows={6} /> : items.length === 0 ? <div className="pos-empty"><Icon name="history" size={40} />No orders on this day.</div> : (
            <div className="pos-cards">
              {items.map((o) => (
                <button key={o.id} className="pos-card" style={{ textAlign: 'left', cursor: 'pointer' }} aria-pressed={current === o.id} onClick={() => setSel(o.id)}>
                  <div className="pos-card-top">
                    <div style={{ flex: 1, minWidth: 0 }}><strong>{who(o)}</strong><div className="pos-muted">{formatDateTime(o.createdAt)}</div></div>
                    <div style={{ textAlign: 'right' }}><div className="pos-muted">{o.orderNo}</div>
                      <div className="pos-status-text" data-status={o.status} style={{ fontWeight: 600, fontSize: 13, textTransform: 'capitalize' }}>{o.status}</div></div>
                  </div>
                  <div className="pos-card-total"><span className="pos-muted">Total Payment</span><strong>{money(o.total)}</strong></div>
                </button>
              ))}
            </div>
          )}
        </div>
      </section>
      <aside className="pos-detail" aria-label="Order details">
        {current ? <OrderDetail key={current} id={current} onChanged={() => void list.refetch()} /> : <div className="pos-empty">Choose an order.</div>}
      </aside>
    </div>
  );
}

type Ticket = Schemas['Ticket'];
const NEXT: Record<string, [string, string] | undefined> = { received: ['preparing', 'Preparing'], preparing: ['ready', 'Ready'], ready: ['served', 'Delivered'],
  out_for_delivery: ['served', 'Delivered'] };
const SERVICE: Record<string, string> = { new: 'New', sent: 'New', preparing: 'Preparing', ready: 'Ready', out_for_delivery: 'On the way', served: 'Delivered' };

/** On-course orders of the tee house (demo feedback 10 Oct 2026 #32): the
 * orders from the caddy tablet and the Member App, with the player, the
 * booking, where to bring them and their service status — charged to the
 * golfer's bill, so nothing to pay here. A new order rings once. */
export function OnCoursePage() {
  const outletId = useOutletId();
  const toast = useToast();
  const { can } = useAuth();
  const date = new Date().toLocaleDateString('sv');
  const list = useGet<Page<Order>>(`/api/v1/commercial/orders${qs({ 'filter[outletId]': outletId, 'filter[orderType]': 'on_course', date, limit: 100 })}`,
    { refetchInterval: 15_000 });
  const tickets = useGet<Page<Ticket>>(can('commercial.kitchen.view') ? '/api/v1/commercial/kitchen-orders?status=all' : null, { refetchInterval: 15_000 });
  const move = useSend<{ id: string; state: string }, Ticket>('POST', (v) => `/api/v1/commercial/kitchen-orders/${v.id}:state`, ['/api/v1/commercial']);
  const [done, setDone] = useState(false);
  const known = useRef<Set<string> | null>(null);
  const items = list.data?.items ?? [];
  useEffect(() => {
    if (!list.data) return;
    const ids = new Set(items.map((o) => o.id));
    if (known.current) {
      const fresh = items.filter((o) => !known.current!.has(o.id));
      if (fresh.length) toast(`New on-course order: ${fresh.map((o) => `${o.orderNo} · ${who(o)}`).join(', ')}`, 'info');
    }
    known.current = ids;
  }, [list.data]); // eslint-disable-line react-hooks/exhaustive-deps
  const open = (o: Order) => o.status !== 'voided' && o.serviceStatus !== 'served';
  const shown = items.filter((o) => open(o) !== done).sort((a, b) => (done ? b.createdAt.localeCompare(a.createdAt) : a.createdAt.localeCompare(b.createdAt)));
  const ticketsOf = (o: Order) => (tickets.data?.items ?? []).filter((t) => t.orderId === o.id && !['served', 'cancelled'].includes(t.status));
  const step = (o: Order) => {
    const ts = ticketsOf(o);
    const next = ts.length ? NEXT[ts[0].status] : undefined;
    if (!next) return null;
    return (
      <button className="pos-btn" data-size="sm" disabled={move.isPending}
        onClick={() => { for (const t of ts) move.mutate({ id: t.id, state: next[0] }, { onSuccess: () => void list.refetch() }); }}>{next[1]}</button>
    );
  };
  return (
    <>
      <div className="pos-head"><h1>On-course orders</h1><span className="pos-muted">{items.filter(open).length} open</span><span className="pos-spacer" />
        <div className="pos-guests"><button className="pos-guest" data-wide aria-pressed={!done} onClick={() => setDone(false)}>Open</button>
          <button className="pos-guest" data-wide aria-pressed={done} onClick={() => setDone(true)}>Delivered / cancelled</button></div>
        <TodayLabel /></div>
      <div className="pos-body">
        <ErrorAlert error={list.error ?? move.error} />
        {list.isLoading ? <Skeleton rows={6} /> : shown.length === 0 ? <div className="pos-empty"><Icon name="sports_golf" size={40} />{done ? 'Nothing delivered yet today.' : 'No on-course order waiting.'}</div> : (
          <div className="pos-cards">
            {shown.map((o) => (
              <div key={o.id} className="pos-card">
                <div className="pos-card-top">
                  <div style={{ flex: 1, minWidth: 0 }}><strong>{o.customerName ?? 'Player'}</strong>
                    <div className="pos-muted">{o.reference ?? ''} · {formatDateTime(o.createdAt)}</div></div>
                  <div style={{ textAlign: 'right' }}><div className="pos-muted">{o.orderNo}</div>
                    <span className="pos-kitchen" data-kitchen={o.status === 'voided' ? 'sent' : o.serviceStatus}>{o.status === 'voided' ? 'Cancelled' : SERVICE[o.serviceStatus] ?? o.serviceStatus}</span></div>
                </div>
                <div><Icon name={o.servingDestination === 'hole' ? 'flag' : 'storefront'} size={16} /> {o.servingDestination === 'hole' ? `Deliver to ${o.destinationRef ?? 'the hole'}` : `Pick-up at ${o.destinationRef ?? 'the tee house'}`}</div>
                <OnCourseLines id={o.id} />
                <div className="pos-card-total"><span className="pos-muted">{o.status === 'charged' ? "Charged to the golfer's bill" : 'Total'}</span><strong>{money(o.total)}</strong></div>
                {!done && <div className="pos-card-actions">{step(o)}</div>}
              </div>
            ))}
          </div>
        )}
      </div>
    </>
  );
}

/** The items of an order with the player each one is for. */
function OnCourseLines({ id }: { id: string }) {
  const q = useGet<Order>(`/api/v1/commercial/orders/${id}`);
  const lines = (q.data?.lines ?? []).filter((l) => l.status === 'active' || q.data?.status === 'voided');
  return (
    <ul className="pos-muted" style={{ margin: 0, paddingLeft: 18 }}>
      {lines.map((l) => <li key={l.id}>{formatNumber(Number(l.quantity))}× {l.name}{l.guestName ? ` — ${l.guestName}` : ''}</li>)}
    </ul>
  );
}
