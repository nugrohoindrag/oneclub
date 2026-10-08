import React, { useEffect, useMemo, useState } from 'react';
import { useLocation, useNavigate, useParams } from 'react-router';
import { request, uuidv7 } from '@oneclub/api-client';
import { formatNumber } from '@oneclub/i18n';
import { enqueue, useOnline } from '@oneclub/offline';
import { ErrorAlert, Icon, Skeleton, useAuth, useToast } from '@oneclub/shell';
import { PosPromotionPanel, usePosPromotions } from '../p3/commercial';
import { PosTierLine, posTierDiscount, usePosTierDiscount } from '../p5/tiers';
import { type LocalLine, localTotal, rememberTaxFactor, saveLocal, taxFactor, useCachedGet, useLocalOrders } from './offline';
import { OfflinePayDialog, PaymentDialog, SuccessDialog } from './pay';
import { CustomerSelect, type OrderStart } from './tables';
import {
  KITCHEN, ProductImage, categoryIcon, money, useMenu, useOutlet, useOutletId, useShift, type MenuItem, type Order, type OrderLine,
} from './shared';

// Menu and Current Order (POS design): items of the outlet menu with
// photos, the cart and the lines already sent with their kitchen state,
// totals, Send to Kitchen and Charge. A new order is created when it is sent
// or charged. Offline, every action goes to the sync queue and is kept as a
// local order until it is sent (pos/offline.ts).

type Draft = { productId: string; quantity: number; notes: string };
type Start = OrderStart & { orderType?: string; tableIds?: string[]; tableCodes?: string[]; tableReservationId?: string };
const idem = () => ({ 'Idempotency-Key': uuidv7() });

/** An order made only of offline lines, for the receipt of an offline sale. */
function offlineReceipt(base: Partial<Order>, lines: LocalLine[], total: number): Order {
  const gross = lines.reduce((s, l) => s + l.unitPrice * l.quantity, 0);
  return {
    ...base, status: 'paid', total: String(total), bills: [],
    lines: lines.map((l, i) => ({ id: `l${i}`, lineNo: i + 1, productId: l.productId, name: l.name, quantity: String(l.quantity), unitPrice: String(l.unitPrice),
      discountAmount: '0', promotionDiscount: '0', tierDiscount: '0', serviceAmount: '0', taxAmount: i === 0 ? String(Math.max(total - gross, 0)) : '0',
      netAmount: String(l.unitPrice * l.quantity), totalAmount: String(l.unitPrice * l.quantity), status: 'active', modifiers: [], promotions: [] })),
  } as unknown as Order;
}

export function MenuPage() {
  const { id } = useParams();
  const start = (useLocation().state ?? {}) as Start;
  const nav = useNavigate();
  const toast = useToast();
  const online = useOnline();
  const { can, propertyId } = useAuth();
  const outletId = useOutletId();
  const outlet = useOutlet(outletId);
  const menu = useMenu(outletId);
  const shiftQ = useShift(outletId);
  const orderQ = useCachedGet<Order>(id ? `/api/v1/commercial/orders/${id}` : null, `order:${id}`);
  const local = useLocalOrders(outletId).find((l) => l.id === id);
  // an order created offline exists only on this device until it is synced
  const order: Order | undefined = orderQ.data ?? (local && !local.server ? {
    id: local.id, orderNo: local.orderNo, status: local.paid ? 'paid' : 'open', tableNo: local.tableCodes?.join(', ') ?? null, guestCount: local.guestCount ?? null,
    customerName: local.customerName ?? null, orderType: local.tableIds?.length ? 'dine_in' : 'takeaway', serviceStatus: 'new', lines: [], bills: [], total: '0',
  } as unknown as Order : undefined);
  const pending = local?.lines ?? [];
  const [draft, setDraft] = useState<Draft[]>([]);
  const [customer, setCustomer] = useState({ id: start.customerId ?? '', name: start.customerName ?? '' });
  const [q, setQ] = useState('');
  const [search, setSearch] = useState('');
  const [cat, setCat] = useState('');
  const [open, setOpen] = useState('');
  const [editing, setEditing] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [paying, setPaying] = useState<Order | null>(null);
  const [payingOffline, setPayingOffline] = useState(false);
  const [paid, setPaid] = useState<{ order: Order; method: string; offline?: boolean } | null>(null);
  const items = menu.data?.items ?? [];
  const byId = useMemo(() => new Map(items.map((p) => [p.productId, p])), [items]);
  const cats = useMemo(() => [...new Set(items.map((p) => p.category ?? 'Other'))], [items]);
  const shown = items.filter((p) => (!cat || (p.category ?? 'Other') === cat) && (!search || p.name.toLowerCase().includes(search.toLowerCase())));
  const customerId = order?.customerId ?? customer.id;
  // draft totals with the promotions and the tier discount the server will apply
  const draftLines = draft.map((d) => ({ d, p: byId.get(d.productId) })).filter((x): x is { d: Draft; p: MenuItem } => !!x.p);
  const promo = usePosPromotions(outletId, draftLines.map(({ d, p }) => ({ productId: d.productId, quantity: d.quantity, unitPrice: Number(p.price) })), customerId);
  const tierDisc = usePosTierDiscount(customerId);
  const draftGross = draftLines.reduce((s, { d, p }) => s + Number(p.price) * d.quantity, 0);
  const tierAmount = posTierDiscount(tierDisc, draftLines.map(({ d, p }) => ({ productType: p.productType, amount: Number(p.price) * d.quantity })), promo.discount);
  const sent = (order?.lines ?? []).filter((l) => l.status === 'active');
  const sentGross = sent.reduce((s, l) => s + Number(l.unitPrice) * Number(l.quantity), 0);
  const sentDiscount = sent.reduce((s, l) => s + Number(l.discountAmount) + Number(l.promotionDiscount ?? 0) + Number(l.tierDiscount ?? 0), 0);
  const sentTax = sent.reduce((s, l) => s + Number(l.taxAmount) + Number(l.serviceAmount), 0);
  const pendingGross = pending.reduce((s, l) => s + l.unitPrice * l.quantity, 0);
  const pendingTotal = localTotal(outletId, pending);
  const nett = outlet.data?.pricingMode !== 'plus_plus';
  const draftNet = Math.max(draftGross - promo.discount - tierAmount, 0);
  // tax & service of the cart at this outlet (++ prices add them; nett prices include them); offline: the last factor seen
  const [quote, setQuote] = useState<{ amount: number; tax: number; total: number } | null>(null);
  useEffect(() => {
    if (!online || draftNet <= 0) { setQuote(null); return; }
    let live = true;
    const t = window.setTimeout(() => {
      request<{ total: string; lines: { amount: string }[] }>('POST', `/api/v1/commercial/outlets/${outletId}:quote`, { amount: String(draftNet) })
        .then((b) => {
          if (!live) return;
          setQuote({ amount: draftNet, tax: b.lines.reduce((s, l) => s + Number(l.amount), 0), total: Number(b.total) });
          rememberTaxFactor(outletId, Number(b.total) / draftNet);
        })
        .catch(() => { if (live) setQuote(null); });
    }, 250);
    return () => { live = false; window.clearTimeout(t); };
  }, [draftNet, online, outletId]);
  const q2 = quote && quote.amount === draftNet ? quote
    : !online && draftNet > 0 ? { amount: draftNet, total: Math.round(draftNet * taxFactor(outletId)), tax: Math.round(draftNet * (taxFactor(outletId) - 1)) } : null;
  const subtotal = sentGross + pendingGross + draftGross;
  const discount = sentDiscount + promo.discount + tierAmount;
  const draftTotal = draftNet > 0 ? (nett ? draftNet : q2?.total ?? draftNet) : 0;
  const total = Number(order?.total ?? 0) + pendingTotal + draftTotal;
  const taxShown = draftNet > 0 && !nett && !q2 ? null : sentTax + (pendingTotal - pendingGross) + (q2?.tax ?? 0);
  const closed = !!order && order.status !== 'open' || !!local?.paid;
  const tableLabel = order ? (order.tableNo ? `Table (${order.tableNo})` : order.orderType.replace('_', ' ')) : start.tableCodes?.length ? `Table (${start.tableCodes.join(', ')})` : 'Manual order';
  const kitchen = order && (order.lines.length || pending.length) ? (pending.length && !online ? 'new' : order.serviceStatus) : null;

  const add = (p: MenuItem) => {
    const i = draft.findIndex((d) => d.productId === p.productId && !d.notes);
    setDraft(i >= 0 ? draft.map((d, j) => (j === i ? { ...d, quantity: d.quantity + 1 } : d)) : [...draft, { productId: p.productId, quantity: 1, notes: '' }]);
  };
  const qtyOf = (pid: string) => draft.filter((d) => d.productId === pid).reduce((s, d) => s + d.quantity, 0);
  const lineInputs = () => draft.map((d) => ({ productId: d.productId, quantity: String(d.quantity), notes: d.notes || undefined }));
  const localLines = (): LocalLine[] => draftLines.map(({ d, p }) => ({ productId: d.productId, name: p.name, quantity: d.quantity, unitPrice: Number(p.price), notes: d.notes || undefined }));
  const newOrderBody = (oid?: string) => {
    const f = promo.orderFields(draftGross - tierAmount);
    return {
      id: oid, outletId, shiftId: shiftQ.shift?.id, orderType: start.orderType ?? 'dine_in', tableIds: start.tableIds, tableReservationId: start.tableReservationId,
      guestCount: start.guestCount, customerId: customer.id || undefined, lines: lineInputs(), send: true,
      promoCodes: f.promoCodes, promotionExclusions: f.promotionExclusions, servingDestination: start.tableIds?.length ? 'table' : 'pickup',
      offline: oid ? true : undefined, clientTotal: oid ? f.clientTotal : undefined, clientCreatedAt: oid ? f.clientCreatedAt : undefined,
    };
  };
  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    try { await fn(); } catch (e) { setError(e); } finally { setBusy(false); }
  };
  /** Creates the order (new) or adds the cart to it (existing); both go to the kitchen. */
  const commit = async (): Promise<Order> => {
    if (order) {
      let o = order;
      if (draft.length) {
        o = await request<Order>('POST', `/api/v1/commercial/orders/${order.id}/lines`, { lines: lineInputs() }, idem());
        o = await request<Order>('POST', `/api/v1/commercial/orders/${order.id}:send`, {}, idem());
      }
      return o;
    }
    return request<Order>('POST', '/api/v1/commercial/orders', newOrderBody(), idem());
  };
  /** Offline: the cart (and the payment) go to the sync queue; the order is kept on this device until then. */
  const queue = async (payment?: { methodType: string; reference?: string }) => {
    const pay = payment && { shiftId: shiftQ.shift?.id, tenders: [payment], receivedAt: new Date().toISOString() };
    const lines = localLines();
    if (order) {
      await enqueue('commercial.pos_order', { orderId: order.id, lines: draft.length ? lineInputs() : undefined, payment: pay }, propertyId);
      await saveLocal(propertyId, outletId, { id: order.id, server: !!orderQ.data, orderNo: order.orderNo, lines, createdAt: new Date().toISOString(),
        paid: payment?.methodType });
      return { id: order.id, orderNo: order.orderNo, lines };
    }
    const oid = uuidv7();
    const orderNo = `Offline ${oid.slice(-4).toUpperCase()}`;
    await enqueue('commercial.pos_order', { order: newOrderBody(oid), payment: pay }, propertyId);
    if (!payment) {
      await saveLocal(propertyId, outletId, { id: oid, server: false, orderNo, tableIds: start.tableIds, tableCodes: start.tableCodes, guestCount: start.guestCount,
        customerId: customer.id || undefined, customerName: customer.name || undefined, lines, createdAt: new Date().toISOString() });
    }
    return { id: oid, orderNo, lines };
  };
  const sendToKitchen = () => run(async () => {
    if (!online) {
      const o = await queue();
      setDraft([]);
      toast(`Offline: ${o.orderNo} kept on this device and sent to the kitchen when the connection returns`);
      nav('/ops/pos');
      return;
    }
    const o = await commit();
    setDraft([]);
    toast(`${o.orderNo} sent to the kitchen`);
    nav('/ops/pos');
  });
  const charge = () => run(async () => {
    if (!online) {
      if (!shiftQ.shift) throw new Error('Offline payments need a shift opened on this terminal while online.');
      setPayingOffline(true);
      return;
    }
    const o = await commit();
    setDraft([]);
    if (id) void orderQ.refetch();
    setPaying(o);
  });
  const payOffline = (method: string, label: string, reference?: string) => run(async () => {
    const all = [...sent.map((l) => ({ productId: l.productId, name: l.name, quantity: Number(l.quantity), unitPrice: Number(l.unitPrice) })), ...pending, ...localLines()];
    const o = await queue({ methodType: method, reference });
    setPayingOffline(false);
    setDraft([]);
    setPaid({ order: offlineReceipt({ id: o.id, orderNo: o.orderNo, tableNo: order?.tableNo ?? start.tableCodes?.join(', ') ?? null,
      orderType: order?.orderType ?? start.orderType ?? 'takeaway' }, all, total), method: label, offline: true });
  });
  const voidLine = (l: OrderLine) => run(async () => {
    const reason = window.prompt(`Void ${l.name}? Reason`);
    if (!reason) return;
    await request('POST', `/api/v1/commercial/orders/${order!.id}/lines/${l.id}:void`, { reason }, idem());
    await orderQ.refetch();
  });

  if (id && orderQ.isLoading && !local) return <div className="pos-body" style={{ paddingTop: 24 }}><Skeleton rows={8} /></div>;
  if (id && !order) return <div className="pos-empty" style={{ margin: 'auto' }}><Icon name="cloud_off" size={40} />This order is not available offline on this device.</div>;
  return (
    <div className="pos-split">
      <section className="pos-split-main" aria-label="Menu">
        <div className="pos-head">
          <button className="pos-icon-btn" style={{ border: 0 }} onClick={() => nav('/ops/pos')} aria-label="Back to Table View"><Icon name="arrow_back" size={22} /></button>
          <h1>{order ? `Order #${order.orderNo}` : 'New Order'}</h1>
          <span className="pos-spacer" />
          {!online && <span className="pos-kitchen" data-kitchen="preparing"><Icon name="cloud_off" size={14} />Offline</span>}
          <span className="pos-muted">{String(outlet.data?.name ?? '')}</span>
        </div>
        <form className="pos-searchbar" role="search" onSubmit={(e) => { e.preventDefault(); setSearch(q); }}>
          <input className="pos-input" placeholder="Search all product here..." value={q} onChange={(e) => { setQ(e.target.value); if (!e.target.value) setSearch(''); }} aria-label="Search menu" />
          <button className="pos-btn" style={{ minWidth: 120 }}>Search</button>
        </form>
        <div className="pos-cats" role="group" aria-label="Categories">
          <button className="pos-chip" aria-pressed={!cat} onClick={() => setCat('')}><Icon name="grid_view" size={20} />All Menu</button>
          {cats.map((c) => <button key={c} className="pos-chip" aria-pressed={cat === c} onClick={() => setCat(c)}><Icon name={categoryIcon(c)} size={20} />{c}</button>)}
        </div>
        <div className="pos-body">
          <ErrorAlert error={online ? menu.error : null} />
          {menu.isLoading ? <Skeleton rows={6} /> : (
            <div className="pos-grid">
              {shown.map((p) => (
                <button key={p.productId} className="pos-product" disabled={p.soldOut || closed} onClick={() => add(p)} aria-label={`${p.name}, ${money(p.price)}${p.soldOut ? ', sold out' : ''}`}>
                  <ProductImage item={p} className="pos-product-img" />
                  {p.soldOut && <span className="pos-product-flag">Sold Out</span>}
                  {qtyOf(p.productId) > 0 && <span className="pos-product-qty">{qtyOf(p.productId)}</span>}
                  <span className="pos-product-row">
                    <span className="pos-product-name">{p.name}</span>
                    <span className="pos-product-price">{money(p.price)}</span>
                  </span>
                  {p.stockTracked && p.available != null && !p.soldOut && <span className="pos-muted" style={{ fontSize: 12 }}>{formatNumber(Number(p.available))} in stock</span>}
                </button>
              ))}
              {shown.length === 0 && <div className="pos-empty">{items.length ? 'No items match.' : 'The menu is not available offline on this device yet.'}</div>}
            </div>
          )}
        </div>
      </section>

      <aside className="pos-order" aria-label="Current Order">
        <div className="pos-order-head">
          <div style={{ flex: 1 }}>
            <h2>Current Order</h2>
            <span className="pos-muted" style={{ textTransform: tableLabel === 'Manual order' ? undefined : 'none' }}>{tableLabel}
              {(order?.guestCount ?? start.guestCount) ? ` · ${order?.guestCount ?? start.guestCount} guests` : ''}</span>
            {kitchen && <div style={{ marginTop: 6 }}><span className="pos-kitchen" data-kitchen={kitchen}><Icon name="soup_kitchen" size={14} />{KITCHEN[kitchen]}</span></div>}
          </div>
          <button className="pos-icon-btn" aria-label="Order options" aria-pressed={editing === 'customer'} onClick={() => setEditing(editing === 'customer' ? '' : 'customer')}>
            <Icon name="tune" size={20} /></button>
        </div>
        {editing === 'customer' && (
          <div className="pos-extras" style={{ marginBottom: 12 }}>
            {order ? <span><strong>Customer:</strong> {order.customerName ?? 'Walk-in guest'}</span>
              : can('crm.customer.view') && online && <><span className="pos-label" style={{ margin: 0 }}>Customer</span>
                <CustomerSelect value={customer.id} label={customer.name} onChange={(cid, name) => setCustomer({ id: cid, name })} /></>}
            {!order && online && <PosPromotionPanel promo={promo} />}
          </div>
        )}
        <div className="pos-order-lines">
          {sent.map((l) => (
            <div key={l.id} className="pos-line-wrap">
              {!closed && online && can('commercial.order.void') && <div className="pos-line-actions"><span style={{ width: 74 }} />
                <button className="del" aria-label={`Void ${l.name}`} onClick={() => void voidLine(l)}><Icon name="delete" size={22} /></button></div>}
              <button className="pos-line" data-open={open === l.id} onClick={() => setOpen(open === l.id ? '' : l.id)}>
                <ProductImage item={byId.get(l.productId)} className="pos-line-img" />
                <span className="pos-line-body">
                  <span className="pos-line-name">{l.name}<span className="pos-line-sent">{l.sentAt ? 'Sent' : 'Saved'}</span></span>
                  <span className="pos-line-note">{l.notes || '—'}</span>
                  <span className="pos-line-foot"><span className="pos-muted">{formatNumber(Number(l.quantity))}x</span><span className="pos-spacer" />
                    <strong className="pos-blue">{money(l.totalAmount)}</strong></span>
                </span>
              </button>
            </div>
          ))}
          {pending.map((l, i) => (
            <div key={`p${i}`} className="pos-line-wrap">
              <div className="pos-line" style={{ cursor: 'default' }}>
                <ProductImage item={byId.get(l.productId)} className="pos-line-img" />
                <span className="pos-line-body">
                  <span className="pos-line-name">{l.name}<span className="pos-line-sent">Offline</span></span>
                  <span className="pos-line-note">{l.notes || '—'}</span>
                  <span className="pos-line-foot"><span className="pos-muted">{l.quantity}x</span><span className="pos-spacer" />
                    <strong className="pos-blue">{money(l.unitPrice * l.quantity)}</strong></span>
                </span>
              </div>
            </div>
          ))}
          {draft.map((d, i) => {
            const p = byId.get(d.productId);
            const key = `d${i}`;
            return (
              <div key={key} className="pos-line-wrap">
                <div className="pos-line-actions">
                  <button className="edit" aria-label={`Edit ${p?.name ?? ''}`} onClick={() => { setEditing(key); setOpen(''); }}><Icon name="edit" size={22} /></button>
                  <button className="del" aria-label={`Remove ${p?.name ?? ''}`} onClick={() => { setDraft(draft.filter((_, j) => j !== i)); setOpen(''); }}><Icon name="delete" size={22} /></button>
                </div>
                <div className="pos-line" data-open={open === key} role="button" tabIndex={0} onClick={() => setOpen(open === key ? '' : key)}
                  onKeyDown={(e) => { if (e.key === 'Enter') setOpen(open === key ? '' : key); }}>
                  <ProductImage item={p} className="pos-line-img" />
                  <span className="pos-line-body">
                    <span className="pos-line-name">{p?.name ?? 'Item'}</span>
                    {editing === key ? (
                      <input className="pos-input" style={{ height: 34 }} autoFocus placeholder="Note (e.g. Not spicy)" value={d.notes}
                        onClick={(e) => e.stopPropagation()} onBlur={() => setEditing('')} onKeyDown={(e) => { if (e.key === 'Enter') setEditing(''); }}
                        onChange={(e) => setDraft(draft.map((x, j) => (j === i ? { ...x, notes: e.target.value } : x)))} />
                    ) : <span className="pos-line-note">{d.notes || 'No note'}</span>}
                    <span className="pos-line-foot">
                      <span className="pos-qty" onClick={(e) => e.stopPropagation()}>
                        <button aria-label="Less" onClick={() => setDraft(d.quantity > 1 ? draft.map((x, j) => (j === i ? { ...x, quantity: x.quantity - 1 } : x)) : draft.filter((_, j) => j !== i))}>
                          <Icon name="remove" size={16} /></button>
                        <span className="pos-muted">{d.quantity}x</span>
                        <button aria-label="More" onClick={() => setDraft(draft.map((x, j) => (j === i ? { ...x, quantity: x.quantity + 1 } : x)))}><Icon name="add" size={16} /></button>
                      </span>
                      <span className="pos-spacer" />
                      <strong className="pos-blue">{money(Number(p?.price ?? 0) * d.quantity)}</strong>
                    </span>
                  </span>
                </div>
              </div>
            );
          })}
          {!sent.length && !pending.length && !draft.length && <div className="pos-empty"><Icon name="restaurant_menu" size={36} />Tap the menu to add items.</div>}
          <PosTierLine t={tierDisc} amount={tierAmount} />
        </div>
        <div className="pos-summary">
          <div className="pos-ticket">
            <div className="pos-ticket-row"><span>Subtotal</span><strong>{money(subtotal)}</strong></div>
            <div className="pos-ticket-row"><span>Discount sales</span><strong>−{money(discount)}</strong></div>
            <div className="pos-ticket-row"><span>Total sales tax{nett ? ' (incl.)' : ''}</span><strong>{taxShown === null ? 'on sending' : money(taxShown)}</strong></div>
            <div className="pos-ticket-cut" />
            <div className="pos-ticket-total"><span>Total{!online ? ' (est.)' : ''}</span><span>{money(total)}</span></div>
          </div>
        </div>
        <div className="pos-order-foot">
          <ErrorAlert error={error} />
          {closed ? <div className="pos-banner" style={{ margin: 0 }}>This order is {local?.paid ? 'paid (offline, waiting to sync)' : order!.status}.</div> : <>
            <div className="pos-row">
              {draft.length > 0 ? (
                <button className="pos-btn" data-variant="outline" disabled={busy} onClick={() => void sendToKitchen()}><Icon name="soup_kitchen" size={20} />Send to Kitchen</button>
              ) : order && online && orderQ.data ? (
                <button className="pos-btn" data-variant="outline" disabled={busy || !!order.billedAt} onClick={() => void run(async () => {
                  await request('POST', `/api/v1/commercial/orders/${order.id}:bill`, {}, idem());
                  await orderQ.refetch();
                  toast('Bill presented');
                })}><Icon name="print" size={20} />{order.billedAt ? 'Billed' : 'Print Bill'}</button>
              ) : null}
            </div>
            <button className="pos-btn" data-block disabled={busy || total <= 0 || (online && pending.length > 0)} onClick={() => void charge()}>Charge {money(total)}</button>
            {online && pending.length > 0 && <span className="pos-muted" style={{ fontSize: 12 }}>Syncing the offline items of this order…</span>}
          </>}
        </div>
      </aside>

      {paying && (
        <PaymentDialog order={paying} shiftId={shiftQ.shift?.id as string | undefined} outletId={outletId} onClose={() => { setPaying(null); if (!id) nav(`/ops/pos/order/${paying.id}`); }}
          onShift={() => void shiftQ.refetch()} onPaid={(o, method) => { setPaying(null); setPaid({ order: o, method }); }} />
      )}
      {payingOffline && <OfflinePayDialog due={total} onClose={() => setPayingOffline(false)} onPay={(m, label, ref) => void payOffline(m, label, ref)} />}
      {paid && <SuccessDialog order={paid.order} method={paid.method} offline={paid.offline} onClose={() => { setPaid(null); nav('/ops/pos'); }} />}
    </div>
  );
}
