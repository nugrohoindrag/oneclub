import React, { useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router';
import { qs, request, uuidv7, useGet, type Page } from '@oneclub/api-client';
import { formatDateTime } from '@oneclub/i18n';
import { enqueue, useOnline } from '@oneclub/offline';
import { ErrorAlert, Icon, useAuth, useToast } from '@oneclub/shell';
import { useCustomerTiers } from '../p5/tiers';
import { type LocalOrder, localTotal, saveLocal, useCachedGet, useLocalOrders } from './offline';
import { KITCHEN, PosDialog, TodayLabel, elapsed, money, useOutlet, useOutletId, useTick, type Row, type TableState } from './shared';

// Table View (PRD P2 FR-POS-01, POS design): the floor plan of the outlet
// with the state of every table, table selection and the New Order dialog.

const STATES: [string, string][] = [['available', 'Available'], ['booked', 'Booked'], ['billed', 'Billed'], ['occupied', 'Occupied']];

/** Chairs around a table (absolute positions inside the table top). */
export function chairs(t: Pick<TableState, 'shape' | 'seats'>): React.CSSProperties[] {
  const out: React.CSSProperties[] = [];
  const h = { width: 30, height: 12 }, v = { width: 12, height: 30 };
  if (t.shape === 'seat') return out;
  if (t.shape === 'round') {
    for (let i = 0; i < t.seats; i++) {
      const a = (2 * Math.PI * i) / t.seats - Math.PI / 2;
      out.push({ ...h, left: `calc(50% + ${Math.cos(a) * 58}px - 15px)`, top: `calc(50% + ${Math.sin(a) * 58}px - 6px)`,
        transform: `rotate(${(a * 180) / Math.PI + 90}deg)` });
    }
    return out;
  }
  const sides = t.shape === 'rect' || t.seats > 4 ? [Math.ceil(t.seats / 2), Math.floor(t.seats / 2), 0, 0]
    : t.seats <= 2 ? [0, 0, 1, t.seats - 1] : [1, 1, 1, t.seats - 3];
  const [top, bottom, left, right] = sides;
  const row = (n: number, y: string) => {
    for (let i = 0; i < n; i++) out.push({ ...h, left: `calc(${((i + 1) * 100) / (n + 1)}% - 15px)`, top: y });
  };
  const col = (n: number, x: string) => {
    for (let i = 0; i < n; i++) out.push({ ...v, top: `calc(${((i + 1) * 100) / (n + 1)}% - 15px)`, left: x });
  };
  row(top, '-18px');
  row(bottom, 'calc(100% + 6px)');
  col(left, '-18px');
  col(right, 'calc(100% + 6px)');
  return out;
}

type Seat = TableState & { offline?: boolean };

/** The tables with the offline changes of this device on top (until the sync queue is sent). */
function withLocal(tables: TableState[], locals: LocalOrder[], outlet: string): Seat[] {
  const out: Seat[] = tables.map((t) => ({ ...t }));
  const byId = new Map(out.map((t) => [t.id, t]));
  const free = (t: Seat) => { t.order = null; t.offline = true; t.state = t.reservation ? 'booked' : 'available'; };
  for (const l of locals) {
    const current = out.filter((t) => t.order?.id === l.id);
    const base = current[0]?.order;
    if (l.paid) { current.forEach(free); continue; }
    if (!l.server && !l.tableIds?.length) continue; // counter sale
    const order = {
      ...(base ?? { id: l.id, orderNo: l.orderNo, guestCount: l.guestCount ?? null, customerName: l.customerName ?? null, total: '0', serviceStatus: 'new' as const,
        seatedAt: l.createdAt, billedAt: null }),
      tableNo: l.tableCodes?.join(', ') ?? base?.tableNo ?? null,
      total: String(Number(base?.total ?? 0) + localTotal(outlet, l.lines)),
    };
    const seats = l.tableIds?.length ? l.tableIds : current.map((t) => t.id);
    if (l.tableIds?.length) current.forEach(free);
    for (const id of seats) {
      const t = byId.get(id);
      if (t) { t.order = order; t.state = order.billedAt ? 'billed' : 'occupied'; t.offline = true; }
    }
  }
  return out;
}

function TableButton({ t, selected, disabled, onClick }: { t: Seat; selected: boolean; disabled?: boolean; onClick: () => void }) {
  const k = t.order?.serviceStatus;
  return (
    <button className="pos-table" data-shape={t.shape} data-state={t.state} data-offline={t.offline || undefined} aria-pressed={selected} disabled={disabled}
      onClick={onClick} style={{ left: `${t.posX}%`, top: `${t.posY}%` }}
      aria-label={`${t.code}, ${t.state}${t.order ? `, order ${t.order.orderNo}, ${KITCHEN[k ?? 'new']}` : ''}${t.reservation ? `, reserved for ${t.reservation.guestName}` : ''}`}>
      <span className="pos-table-top">
        {chairs(t).map((s, i) => <span key={i} className="pos-chair" style={s} />)}
        <span className="pos-table-code">{t.code}</span>
        {t.order && <span className="pos-timer" data-kitchen={k}>{k === 'ready' || k === 'out_for_delivery' ? 'Ready' : elapsed(t.order.seatedAt)}</span>}
      </span>
    </button>
  );
}

export function TableViewPage() {
  const outletId = useOutletId();
  const nav = useNavigate();
  const toast = useToast();
  const online = useOnline();
  const { propertyId } = useAuth();
  const outlet = useOutlet(outletId);
  const tables = useCachedGet<Page<TableState>>(`/api/v1/commercial/outlets/${outletId}/tables`, `tables:${outletId}`, { refetchInterval: 15_000 });
  const locals = useLocalOrders(outletId);
  const [sel, setSel] = useState<string[]>([]);
  const [moving, setMoving] = useState<{ id: string; orderNo: string; server: boolean } | null>(null);
  const [dialog, setDialog] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  useTick(30_000);
  const items = useMemo(() => withLocal(tables.data?.items ?? [], locals, outletId), [tables.data, locals, outletId]);
  const byId = useMemo(() => new Map(items.map((t) => [t.id, t])), [items]);
  const chosen = sel.map((id) => byId.get(id)).filter((t): t is Seat => !!t);
  const seated = moving ? undefined : chosen.find((t) => t.order);
  const booking = chosen.find((t) => t.reservation)?.reservation;
  const localNew = (id: string) => locals.some((l) => l.id === id && !l.server);
  const toggle = (t: Seat) => {
    if (moving) { setSel(sel.includes(t.id) ? sel.filter((x) => x !== t.id) : [...sel, t.id]); return; }
    if (t.order) { setSel(sel.includes(t.id) ? [] : [t.id]); return; } // an occupied table opens its order
    const free = sel.filter((id) => !byId.get(id)?.order);
    setSel(free.includes(t.id) ? free.filter((x) => x !== t.id) : [...free, t.id]);
  };
  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    try { await fn(); } catch (e) { setError(e); } finally { setBusy(false); }
  };
  const bill = () => run(async () => {
    if (!seated?.order) return;
    await request('POST', `/api/v1/commercial/orders/${seated.order.id}:bill`, {}, { 'Idempotency-Key': uuidv7() });
    toast(`Bill of ${seated.order.orderNo} presented`);
    void tables.refetch();
  });
  const startMove = () => {
    if (!seated?.order) return;
    setMoving({ id: seated.order.id, orderNo: seated.order.orderNo, server: !localNew(seated.order.id) });
    setSel(items.filter((t) => t.order?.id === seated.order!.id).map((t) => t.id));
  };
  const saveMove = () => run(async () => {
    if (!moving || !chosen.length) return;
    const codes = chosen.map((t) => t.code);
    if (online && moving.server) {
      await request('POST', `/api/v1/commercial/orders/${moving.id}:tables`, { tableIds: sel }, { 'Idempotency-Key': uuidv7() });
      void tables.refetch();
    } else { // offline (or an order not synced yet): queued after the order itself
      await enqueue('commercial.pos_order', { orderId: moving.id, tableIds: sel }, propertyId);
      await saveLocal(propertyId, outletId, { id: moving.id, server: moving.server, orderNo: moving.orderNo, tableIds: sel, tableCodes: codes, lines: [],
        createdAt: new Date().toISOString() });
    }
    toast(`${moving.orderNo} moved to ${codes.join(', ')}`);
    setMoving(null);
    setSel([]);
  });
  return (
    <>
      <div className="pos-head">
        <h1>Table View</h1>
        <span className="pos-spacer" />
        <Link className="pos-notify" to="/ops/notifications"><Icon name="notifications" size={20} /><span className="pos-badge" aria-hidden="true" />Notification</Link>
        <span className="pos-spacer" />
        <TodayLabel />
      </div>
      <div className="pos-legend" aria-label="Legend">
        {STATES.map(([s, l]) => <span key={s}><i className="pos-dot" data-state={s} />{l}</span>)}
        <span className="pos-spacer" />
        <span className="pos-muted">{String(outlet.data?.name ?? '')}</span>
      </div>
      {!online && <div className="pos-banner" data-tone="warn"><Icon name="cloud_off" size={20} />
        Offline: orders and payments are kept on this device and sent when the connection returns{tables.fromCache ? ' · floor plan from the last sync' : ''}.</div>}
      {moving && <div className="pos-banner"><Icon name="table_restaurant" size={20} />Move {moving.orderNo}: choose all the tables it seats, then Save.</div>}
      <ErrorAlert error={(online ? tables.error : null) ?? error} />
      <div className="pos-floor-wrap">
        {items.length === 0 && !tables.isLoading ? (
          <div className="pos-empty">
            <Icon name="table_restaurant" size={40} />
            <strong>No floor plan for this outlet</strong>
            <span>Sell over the counter with a manual order, or set up the tables in Back Office → Commercial → Floor Plan.</span>
            <button className="pos-btn" onClick={() => nav('/ops/pos/order', { state: { orderType: 'takeaway' } })}>Manual Order</button>
          </div>
        ) : (
          <div className="pos-floor">
            {items.map((t) => <TableButton key={t.id} t={t} selected={sel.includes(t.id)} onClick={() => toggle(t)}
              disabled={!!moving && !!t.order && t.order.id !== moving.id} />)}
          </div>
        )}
      </div>
      <div className="pos-tablebar">
        <span className="pos-tablebar-icon"><Icon name="table_restaurant" size={24} /></span>
        <div className="pos-tableinfo">
          <strong>{moving ? `Move ${moving.orderNo}` : 'Table'}</strong>
          <span className="pos-muted">
            {moving ? 'New tables'
              : seated?.order ? `Order ${seated.order.orderNo} · ${seated.order.guestCount ?? '–'} guests · ${money(seated.order.total)} · ${KITCHEN[seated.order.serviceStatus] ?? ''}`
                : booking ? `Booked: ${booking.guestName} · ${formatDateTime(booking.reservedFor)} · ${booking.guestCount} guests`
                  : chosen.length ? 'New order' : 'Choose a table'}
          </span>
        </div>
        {chosen.map((t) => (
          <span key={t.id} className="pos-tablebar-chip">{t.code}
            <button aria-label={`Remove ${t.code}`} onClick={() => setSel(sel.filter((x) => x !== t.id))}><Icon name="close" size={14} /></button>
          </span>
        ))}
        <span className="pos-spacer" />
        {moving ? <>
          <button className="pos-btn pos-pill" data-variant="outline" onClick={() => { setMoving(null); setSel([]); }}>Cancel</button>
          <button className="pos-btn pos-pill" disabled={busy || !chosen.length} onClick={() => void saveMove()}><Icon name="check" size={20} />Save Tables</button>
        </> : seated?.order ? <>
          <button className="pos-btn pos-pill" data-variant="outline" onClick={startMove}><Icon name="swap_horiz" size={20} />Move Table</button>
          <button className="pos-btn pos-pill" data-variant="outline" disabled={busy || !online || !!seated.order.billedAt || seated.offline} onClick={() => void bill()}>
            <Icon name="print" size={20} />Print Bill</button>
          <button className="pos-btn pos-pill" onClick={() => nav(`/ops/pos/order/${seated.order!.id}`)}><Icon name="restaurant_menu" size={20} />Open Order</button>
        </> : <>
          <button className="pos-btn pos-pill" data-variant="outline" onClick={() => nav('/ops/pos/order', { state: { orderType: 'takeaway' } })}>Manual Order</button>
          <button className="pos-btn pos-pill" disabled={!chosen.length} onClick={() => setDialog(true)}><Icon name="restaurant" size={20} />Place Order</button>
        </>}
      </div>
      {dialog && (
        <NewOrderDialog tables={chosen} reservation={booking} onClose={() => setDialog(false)}
          onManual={(c) => nav('/ops/pos/order', { state: { ...c, orderType: 'takeaway' } })}
          onBook={(c) => nav('/ops/pos/order', { state: { ...c, orderType: 'dine_in', tableIds: chosen.map((t) => t.id), tableCodes: chosen.map((t) => t.code),
            tableReservationId: booking?.id } })} />
      )}
    </>
  );
}

export type OrderStart = { customerId?: string; customerName?: string; guestCount?: number };

/** Customer search (name, phone or code) with the member tier. */
export function CustomerSelect({ value, label, onChange }: { value: string; label: string; onChange: (id: string, name: string) => void }) {
  const [q, setQ] = useState('');
  const [open, setOpen] = useState(false);
  const list = useGet<Page<Row>>(q.trim().length >= 2 ? `/api/v1/crm/customers${qs({ q, limit: 20, 'filter[status]': 'active' })}` : null);
  const tiers = useCustomerTiers((list.data?.items ?? []).map((c) => String(c.id)));
  return (
    <div style={{ position: 'relative' }}>
      <input className="pos-input pos-select" value={open ? q : label} placeholder="Walk-in guest — type a name, phone or code" aria-label="Customer"
        onFocus={() => { setOpen(true); setQ(''); }} onBlur={() => window.setTimeout(() => setOpen(false), 150)} onChange={(e) => setQ(e.target.value)} />
      {open && (
        <div className="pos-section" role="listbox" style={{ position: 'absolute', zIndex: 2, left: 0, right: 0, top: 52, padding: 6, background: 'var(--pos-surface)',
          maxHeight: 240, overflow: 'auto', boxShadow: 'var(--pos-shadow)' }}>
          <button type="button" className="pos-method-head" style={{ height: 40 }} onMouseDown={() => onChange('', '')}>Walk-in guest</button>
          {(list.data?.items ?? []).map((c) => {
            const tier = tiers.get(String(c.id))?.tierName;
            return (
              <button type="button" key={String(c.id)} className="pos-method-head" style={{ height: 44, fontWeight: value === c.id ? 700 : 500 }}
                onMouseDown={() => onChange(String(c.id), String(c.name))}>
                {String(c.name)} <span className="pos-muted">{String(c.code)}{tier ? ` · ${String(tier)}` : ''}</span>
              </button>
            );
          })}
          {q.trim().length < 2 && <div className="pos-muted" style={{ padding: '6px 22px' }}>Type at least 2 letters</div>}
        </div>
      )}
    </div>
  );
}

/** New Order: tables, customer and guests; Book & Order seats the tables. */
function NewOrderDialog({ tables, reservation, onClose, onManual, onBook }: {
  tables: TableState[]; reservation?: TableState['reservation']; onClose: () => void; onManual: (c: OrderStart) => void; onBook: (c: OrderStart) => void;
}) {
  const { can } = useAuth();
  const seats = tables.reduce((s, t) => s + t.seats, 0);
  const [customer, setCustomer] = useState<{ id: string; name: string }>({ id: reservation?.customerId ?? '', name: reservation?.customerId ? reservation.guestName : '' });
  const [guests, setGuests] = useState(reservation?.guestCount ?? Math.min(Math.max(seats, 1), 2));
  const [other, setOther] = useState(guests > 5);
  const start = { customerId: customer.id || undefined, customerName: customer.name || reservation?.guestName, guestCount: guests };
  return (
    <PosDialog title="New Order" sub="There's just a few steps left to complete your purchase." onClose={onClose}>
      <div className="pos-field"><label className="pos-label">Table</label>
        <input className="pos-input" readOnly value={tables.map((t) => t.code).join(', ')} aria-label="Table" /></div>
      <div className="pos-field"><label className="pos-label">Choose Customers</label>
        {can('crm.customer.view') ? <CustomerSelect value={customer.id} label={customer.name} onChange={(id, name) => setCustomer({ id, name })} />
          : <input className="pos-input" readOnly value={reservation?.guestName ?? 'Walk-in guest'} />}
      </div>
      <div className="pos-field"><span className="pos-label">Guest</span>
        <div className="pos-guests" role="group" aria-label="Guests">
          {[1, 2, 3, 4, 5].map((n) => <button key={n} type="button" className="pos-guest" aria-pressed={!other && guests === n} onClick={() => { setOther(false); setGuests(n); }}>{n}</button>)}
          {other ? <input className="pos-input" style={{ width: 90, height: 42 }} type="number" min={1} max={200} value={guests} aria-label="Guests"
            onChange={(e) => setGuests(Math.max(1, Number(e.target.value) || 1))} />
            : <button type="button" className="pos-guest" data-wide aria-pressed={false} onClick={() => { setOther(true); setGuests(6); }}>Other</button>}
        </div>
      </div>
      <div className="pos-dialog-actions">
        <button className="pos-btn" data-variant="outline" onClick={() => onManual(start)}>Manual Order</button>
        <button className="pos-btn" onClick={() => onBook(start)}>Book &amp; Order</button>
      </div>
    </PosDialog>
  );
}
