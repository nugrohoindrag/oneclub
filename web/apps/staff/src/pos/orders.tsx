import React, { useState } from 'react';
import { useNavigate } from 'react-router';
import { qs, request, uuidv7, useGet, type Page } from '@oneclub/api-client';
import { formatDateTime, formatNumber } from '@oneclub/i18n';
import { ErrorAlert, Icon, Skeleton, useAuth, useToast } from '@oneclub/shell';
import { useOnline } from '@oneclub/offline';
import { localTotal, useCachedGet, useLocalOrders } from './offline';
import { PaymentDialog, SuccessDialog } from './pay';
import { KITCHEN, ProductImage, TodayLabel, money, useMenu, useOutletId, useShift, type Order } from './shared';

// Payment Confirm (open orders waiting for payment) and Order History
// (today's settled orders with details, receipt and refund request).

const who = (o: Order) => o.customerName ?? (o.tableNo ? `Table ${o.tableNo}` : 'Walk-in guest');

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
          <span className="pos-blue">{o.customerName ?? 'Walk-in guest'}, {o.tableNo ? `Table (${o.tableNo})` : o.orderType.replace('_', ' ')}</span>
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
                <span className="pos-line-note">{l.notes || '—'}</span>
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
