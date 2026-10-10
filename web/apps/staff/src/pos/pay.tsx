import React, { useState } from 'react';
import { qs, request, uuidv7, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDateTime, formatNumber } from '@oneclub/i18n';
import { ErrorAlert, Icon, useAuth } from '@oneclub/shell';
import { MoneyInput } from '@oneclub/ui';
import { PosDialog, money, type Order, type Row } from './shared';

// Payment Method, the shift it needs, and Order Successful (POS design).

type Method = 'qris' | 'card' | 'member_account' | 'golfer_bill' | 'court_bill' | 'room_bill' | 'voucher_prepaid' | 'loyalty_points' | 'cash';
const REST: Method[] = ['qris', 'card', 'cash'];
const LABEL: Record<Method, string> = { qris: 'QRIS', card: 'Credit Card', member_account: 'Member Account', golfer_bill: 'Golfer Bill', court_bill: 'Court Bill (Sport Club)', room_bill: 'Charge to Room (Bungalow)', voucher_prepaid: 'Voucher', loyalty_points: 'Redeem Points', cash: 'Cash' };

export function Brand({ m }: { m: Method }) {
  if (m === 'qris') return <span className="pos-brand-qris" aria-hidden="true">QRIS</span>;
  if (m === 'card') return <span className="pos-brand-card" aria-hidden="true"><i /><i /></span>;
  const icon = m === 'cash' ? 'payments' : m === 'member_account' ? 'card_membership' : m === 'golfer_bill' ? 'sports_golf' : m === 'court_bill' ? 'sports_tennis' : m === 'room_bill' ? 'hotel' : m === 'voucher_prepaid' ? 'redeem' : 'loyalty';
  return <span className="pos-brand-icon" aria-hidden="true"><Icon name={icon} size={16} /></span>;
}

/** Opens the POS shift of the outlet (opening cash). */
export function OpenShift({ outletId, onOpened }: { outletId: string; onOpened: (shift: Row) => void }) {
  const open = useSend<Row, Row>('POST', '/api/v1/commercial/shifts:open', ['/api/v1/commercial/shifts']);
  const [cash, setCash] = useState('500000');
  return (
    <div className="pos-section" style={{ marginBottom: 0 }}>
      <h2>Open a shift first</h2>
      <p className="pos-muted" style={{ marginTop: -6 }}>Payments are recorded in the cashier shift of this outlet.</p>
      <label className="pos-label" htmlFor="pos-opening">Opening cash</label>
      <div style={{ display: 'flex', gap: 12 }}>
        <MoneyInput id="pos-opening" className="pos-input" value={cash} onChange={setCash} />
        <button className="pos-btn" disabled={open.isPending} onClick={() => open.mutate({ outletId, openingCash: cash || '0' }, { onSuccess: onOpened })}>Open Shift</button>
      </div>
      <ErrorAlert error={open.error} />
    </div>
  );
}

/** Payment Method: one tender (or points + rest) for the open order. */
export function PaymentDialog({ order, shiftId, outletId, onClose, onPaid, onShift }: {
  order: Order; shiftId?: string; outletId: string; onClose: () => void; onPaid: (o: Order, method: string) => void; onShift: (s: Row) => void;
}) {
  const { can } = useAuth();
  const due = Math.max(Number(order.total) - order.bills.reduce((s, b) => s + Number(b.paid), 0), 0);
  const acctQ = useGet<Page<Row>>(order.customerId && can('crm.loyalty_account.view') ? `/api/v1/crm/loyalty/accounts${qs({ 'filter[customerId]': order.customerId })}` : null);
  const acct = acctQ.data?.items.find((a) => a.status === 'active');
  const pointValue = Number(acct?.redemptionValue ?? 0);
  const maxPoints = acct && pointValue > 0 ? Math.min(Number(acct.balance ?? 0), Math.floor(due / pointValue)) : 0;
  const methods: Method[] = ['qris', 'card', ...(order.customerId ? ['member_account' as Method] : []), ...(can('commercial.order.pay') && due === Number(order.total) ? ['golfer_bill' as Method, 'court_bill' as Method, ...(can('stay.stay.charge_order') ? ['room_bill' as Method] : [])] : []),
    'voucher_prepaid', ...(maxPoints > 0 ? ['loyalty_points' as Method] : []), 'cash'];
  const [method, setMethod] = useState<Method>('qris');
  const [ref, setRef] = useState('');
  const [voucher, setVoucher] = useState('');
  const [points, setPoints] = useState('');
  const [rest, setRest] = useState<Method>('cash');
  const [received, setReceived] = useState('');
  const [golfer, setGolfer] = useState<Row | null>(null);
  const [court, setCourt] = useState<Row | null>(null);
  const [room, setRoom] = useState<Row | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const pts = Math.min(Number(points || maxPoints), maxPoints);
  const change = method === 'cash' && received ? Number(received) - due : 0;
  const ready = !!shiftId && due > 0 && !(method === 'voucher_prepaid' && !voucher.trim()) && !(method === 'cash' && received !== '' && Number(received) < due)
    && !(method === 'loyalty_points' && pts <= 0) && !(method === 'golfer_bill' && !golfer) && !(method === 'court_bill' && !court) && !(method === 'room_bill' && !room);
  const pay = async () => {
    setBusy(true);
    setError(null);
    const tender = (m: Method) => ({ methodType: m, reference: ref || undefined });
    const tenders = method === 'loyalty_points' ? [{ methodType: 'loyalty_points', tender: { points: pts } }, tender(rest)]
      : method === 'voucher_prepaid' ? [{ methodType: 'voucher_prepaid', tender: { code: voucher.trim() } }] : [tender(method)];
    try {
      if (method === 'golfer_bill' && golfer) {
        // the whole order goes on the golfer's booking folio, settled at the Golfer Check-out
        const o = await request<Order>('POST', `/api/v1/commercial/orders/${order.id}:charge`, { folioId: golfer.folioId });
        onPaid(o, `${LABEL.golfer_bill} · ${String(golfer.playerName)} (${String(golfer.bookingCode)})`);
        return;
      }
      if (method === 'room_bill' && room) {
        // the order goes on the folio of the in-house guest, every item with the guest and the bungalow (accommodation FR-H82)
        await request('POST', `/api/v1/stay/stays/${String(room.stayId)}:charge-order`, { orderId: order.id });
        const o = await request<Order>('GET', `/api/v1/commercial/orders/${order.id}`);
        onPaid(o, `${LABEL.room_bill} · ${String(room.bungalow)} · ${String(room.guest)}`);
        return;
      }
      if (method === 'court_bill' && court) {
        // the café order goes on the bill of the court booking in play, settled at Selesai Main (FR-135)
        const o = await request<Order>('POST', `/api/v1/commercial/orders/${order.id}:charge`, { folioId: court.folioId });
        onPaid(o, `${LABEL.court_bill} · ${String(court.name)} (${String(court.bookingCode)})`);
        return;
      }
      const o = await request<Order>('POST', `/api/v1/commercial/orders/${order.id}:pay`, { shiftId, tenders }, { 'Idempotency-Key': uuidv7() });
      onPaid(o, method === 'loyalty_points' ? `${LABEL.loyalty_points} + ${LABEL[rest]}` : LABEL[method]);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <PosDialog title="Payments Method" sub="Select a payment method below. Please double check before finishing your payment" onClose={onClose}>
      <div className="pos-amount" aria-label="Amount due">{money(due)}</div>
      {!shiftId ? <OpenShift outletId={outletId} onOpened={onShift} /> : methods.map((m) => (
        <div key={m} className="pos-method" aria-expanded={method === m}>
          <button type="button" className="pos-method-head" onClick={() => setMethod(m)} aria-pressed={method === m}>
            <span className="pos-radio" data-on={method === m} /><Brand m={m} />{m === 'qris' ? null : LABEL[m]}
            {m === 'qris' && <span className="pos-muted" style={{ fontWeight: 500 }}>Scan to pay</span>}
            <span className="pos-spacer" />
            {m !== 'qris' && <Icon name="expand_more" size={22} />}
          </button>
          {method === m && (m === 'card' || m === 'qris') && (
            <div className="pos-method-body">
              <input className="pos-input" placeholder={m === 'card' ? 'Approval code (EDC)' : 'QRIS reference (optional)'} value={ref} onChange={(e) => setRef(e.target.value)} />
            </div>
          )}
          {method === m && m === 'member_account' && (
            <div className="pos-method-body pos-muted">Charged to the member account of {order.customerName ?? 'the customer'}; it appears on the member statement.</div>
          )}
          {method === m && m === 'golfer_bill' && <GolferPicker value={golfer} onChange={setGolfer} />}
          {method === m && m === 'court_bill' && <CourtPicker value={court} onChange={setCourt} />}
          {method === m && m === 'room_bill' && <RoomPicker value={room} onChange={setRoom} />}
          {method === m && m === 'voucher_prepaid' && (
            <div className="pos-method-body"><input className="pos-input" placeholder="Voucher code" value={voucher} onChange={(e) => setVoucher(e.target.value)} autoFocus /></div>
          )}
          {method === m && m === 'loyalty_points' && (
            <div className="pos-method-body">
              <span className="pos-muted">{formatNumber(Number(acct?.balance ?? 0))} points available · up to {formatNumber(maxPoints)} for this bill</span>
              <input className="pos-input" type="number" min={1} max={maxPoints} placeholder={String(maxPoints)} value={points} onChange={(e) => setPoints(e.target.value)} />
              <span className="pos-muted">= {money(pts * pointValue)}; the rest ({money(Math.max(due - pts * pointValue, 0))}) by</span>
              <div className="pos-guests">{REST.map((r) => <button key={r} type="button" className="pos-guest" data-wide aria-pressed={rest === r} onClick={() => setRest(r)}>{LABEL[r]}</button>)}</div>
            </div>
          )}
          {method === m && m === 'cash' && (
            <div className="pos-method-body">
              <MoneyInput className="pos-input" aria-label="Cash received" placeholder={`Cash received (${money(due)})`} value={received} onChange={setReceived} />
              <div className="pos-guests">
                {[due, Math.ceil(due / 50000) * 50000, Math.ceil(due / 100000) * 100000].filter((v, i, a) => a.indexOf(v) === i).map((v) => (
                  <button key={v} type="button" className="pos-guest" data-wide aria-pressed={Number(received) === v} onClick={() => setReceived(String(v))}>{money(v)}</button>
                ))}
              </div>
              {change > 0 && <strong>Change: {money(change)}</strong>}
            </div>
          )}
        </div>
      ))}
      <ErrorAlert error={error} />
      {shiftId && <button className="pos-btn pos-pill" data-block style={{ marginTop: 8 }} disabled={!ready || busy} onClick={() => void pay()}>Next</button>}
    </PosDialog>
  );
}

/** A checked-in golfer of today (name, booking code, bag tag or locker) whose booking folio takes the order. */
function GolferPicker({ value, onChange }: { value: Row | null; onChange: (g: Row | null) => void }) {
  const [q, setQ] = useState('');
  const list = useGet<Page<Row>>(`/api/v1/golf/charge-targets${qs({ q: q.trim() || undefined })}`);
  return (
    <div className="pos-method-body">
      <input className="pos-input" placeholder="Golfer name, booking code, bag tag or locker" value={q} onChange={(e) => setQ(e.target.value)} autoFocus />
      <ErrorAlert error={list.error} />
      <div className="pos-guests">
        {(list.data?.items ?? []).slice(0, 12).map((g) => (
          <button key={`${String(g.folioId)}-${String(g.playerName)}`} type="button" className="pos-guest" data-wide aria-pressed={value?.playerName === g.playerName && value?.folioId === g.folioId}
            onClick={() => onChange(g)}>
            {String(g.playerName)} · {String(g.bookingCode)}{g.bagTag ? ` · bag ${String(g.bagTag)}` : ''}{g.locker ? ` · locker ${String(g.locker)}` : ''}
          </button>
        ))}
      </div>
      {list.data && list.data.items.length === 0 && <span className="pos-muted">No checked-in golfer with an open bill.</span>}
      <span className="pos-muted">Goes on the booking bill and is settled at the Golfer Check-out.</span>
    </div>
  );
}

/** Payment while offline: cash, QRIS or card, queued with the order (member charge, vouchers and points need the server). */
export function OfflinePayDialog({ due, onClose, onPay }: { due: number; onClose: () => void; onPay: (method: string, label: string, reference?: string) => void }) {
  const [method, setMethod] = useState<Method>('cash');
  const [ref, setRef] = useState('');
  const [received, setReceived] = useState('');
  const change = method === 'cash' && received ? Number(received) - due : 0;
  return (
    <PosDialog title="Payments Method" sub="Offline: the payment is kept on this device and sent with the order when the connection returns." onClose={onClose}>
      <div className="pos-amount" aria-label="Amount due">{money(due)}</div>
      {(['cash', 'qris', 'card'] as Method[]).map((m) => (
        <div key={m} className="pos-method" aria-expanded={method === m}>
          <button type="button" className="pos-method-head" onClick={() => setMethod(m)} aria-pressed={method === m}>
            <span className="pos-radio" data-on={method === m} /><Brand m={m} />{m === 'qris' ? null : LABEL[m]}
          </button>
          {method === m && m !== 'cash' && <div className="pos-method-body">
            <input className="pos-input" placeholder={m === 'card' ? 'Approval code (EDC)' : 'QRIS reference'} value={ref} onChange={(e) => setRef(e.target.value)} /></div>}
          {method === m && m === 'cash' && <div className="pos-method-body">
            <MoneyInput className="pos-input" aria-label="Cash received" placeholder={`Cash received (${money(due)})`} value={received} onChange={setReceived} />
            {change > 0 && <strong>Change: {money(change)}</strong>}
          </div>}
        </div>
      ))}
      <button className="pos-btn pos-pill" data-block style={{ marginTop: 8 }} disabled={due <= 0 || (method === 'cash' && received !== '' && Number(received) < due)}
        onClick={() => onPay(method, LABEL[method], ref || undefined)}>Next</button>
    </PosDialog>
  );
}

/** Basket illustration of Order Successful (drawn for OneClub). */
export function Basket() {
  return (
    <svg className="pos-success-art" viewBox="0 0 132 96" aria-hidden="true">
      <path d="M96 10l2.6 7.4L106 20l-7.4 2.6L96 30l-2.6-7.4L86 20l7.4-2.6z" fill="#5cc46a" />
      <path d="M80 4l1.4 4L85 9.4 81.4 11 80 15l-1.4-4L75 9.4 78.6 8z" fill="#5cc46a" />
      <path d="M76 24l1.6 4.4L82 30l-4.4 1.6L76 36l-1.6-4.4L70 30l4.4-1.6z" fill="#5cc46a" />
      <rect x="26" y="40" width="80" height="9" rx="4.5" fill="#2b2f4a" />
      <path d="M31 49h70l-6 32a6 6 0 0 1-6 5H43a6 6 0 0 1-6-5z" fill="#2f5fe6" />
      <g stroke="#6f93ff" strokeWidth="4" strokeLinecap="round"><path d="M50 56l-3 22" /><path d="M60 56l-2 22" /><path d="M70 56l-1 22" /><path d="M80 56l0 22" /></g>
      <rect x="40" y="84" width="52" height="6" rx="3" fill="#2b2f4a" />
      <path d="M16 62L58 30" stroke="#33b0f5" strokeWidth="6" strokeLinecap="round" />
      <path d="M100 44l14 6" stroke="#33b0f5" strokeWidth="6" strokeLinecap="round" />
      <circle cx="58" cy="30" r="3" fill="#fff" />
    </svg>
  );
}

/** Order Successful: the receipt of the payment. */
export function SuccessDialog({ order, method, onClose, offline }: { order: Order; method: string; onClose: () => void; offline?: boolean }) {
  const lines = order.lines.filter((l) => l.status === 'active');
  const subtotal = lines.reduce((s, l) => s + Number(l.unitPrice) * Number(l.quantity), 0);
  const discount = lines.reduce((s, l) => s + Number(l.discountAmount) + Number(l.promotionDiscount ?? 0) + Number(l.tierDiscount ?? 0), 0);
  const tax = lines.reduce((s, l) => s + Number(l.taxAmount) + Number(l.serviceAmount), 0);
  const included = Math.abs(subtotal - discount - Number(order.total)) < 1; // nett prices include tax & service
  const print = async () => {
    const r = await request<{ text: string }>('GET', `/api/v1/commercial/orders/${order.id}/receipt`);
    const w = window.open('', '_blank', 'width=380,height=640');
    if (!w) return;
    w.document.title = order.orderNo;
    const pre = w.document.createElement('pre');
    pre.textContent = r.text;
    pre.style.font = '12px/1.4 monospace';
    w.document.body.appendChild(pre);
    w.print();
  };
  return (
    <PosDialog title="Order Successful" onClose={onClose} label="Order successful" art={<Basket />}>
      <div className="pos-receipt-row"><span>No. Transaction</span><strong>{order.orderNo}</strong></div>
      <div className="pos-receipt-row"><span>Table</span><strong>{order.tableNo ?? (order.orderType === 'dine_in' ? '—' : 'Takeaway')}</strong></div>
      <div className="pos-receipt-row"><span>Payment</span><strong>{method}</strong></div>
      <div className="pos-receipt-row"><span>Date</span><strong>{formatDateTime(new Date())}</strong></div>
      <div className="pos-receipt-cut" />
      <div className="pos-receipt-row"><span>Total item</span><strong>{formatNumber(lines.reduce((s, l) => s + Number(l.quantity), 0))} Item</strong></div>
      {lines.map((l) => (
        <div key={l.id} className="pos-receipt-item"><span>+{formatNumber(Number(l.quantity))} {l.name}</span><span>{money(Number(l.unitPrice) * Number(l.quantity))}</span></div>
      ))}
      <div className="pos-receipt-cut" />
      <div className="pos-receipt-row"><span>Subtotal</span><strong>{money(subtotal)}</strong></div>
      <div className="pos-receipt-row"><span>Discount sales</span><strong>−{money(discount)}</strong></div>
      <div className="pos-receipt-row"><span>Total sales tax{included ? ' (incl.)' : ''}</span><strong>{money(tax)}</strong></div>
      <div className="pos-receipt-cut" />
      <div className="pos-receipt-total"><span>Total</span><span>{money(order.total)}</span></div>
      <div className="pos-dialog-actions">
        <button className="pos-btn" data-variant="outline" disabled={offline} title={offline ? 'The receipt prints once the sale is synced' : undefined}
          onClick={() => void print()}><Icon name="print" size={20} />Print Receipt</button>
        <button className="pos-btn" onClick={onClose}>Done</button>
      </div>
    </PosDialog>
  );
}

/** An in-house bungalow guest (name or bungalow) whose folio takes the order; refused after check-out or over the charge limit. */
function RoomPicker({ value, onChange }: { value: Row | null; onChange: (r: Row | null) => void }) {
  const [q, setQ] = useState('');
  const list = useGet<Page<Row>>(`/api/v1/stay/in-house${qs({ q: q.trim() || undefined })}`, { retry: false });
  return (
    <div className="pos-method-body">
      <input className="pos-input" placeholder="Nama tamu atau nomor bungalow" value={q} onChange={(e) => setQ(e.target.value)} />
      <ErrorAlert error={list.error} />
      <div className="pos-guests">
        {(list.data?.items ?? []).map((r) => (
          <button key={String(r.stayId)} type="button" className="pos-guest" data-wide aria-pressed={value?.stayId === r.stayId} onClick={() => onChange(r)}>
            {String(r.bungalow)} · {String(r.guest)}<br /><small>{String(r.stayNo)}{r.limitLeft != null ? ` · sisa limit ${money(Number(r.limitLeft))}` : ''}</small></button>
        ))}
        {list.data && list.data.items.length === 0 && <span className="pos-muted">Tidak ada tamu in-house.</span>}
      </div>
      <span className="pos-muted">Masuk folio tamu dan terlihat di My Stay serta saat check-out.</span>
    </div>
  );
}

/** A court booking in play (renter, booking code, court) whose bill takes the café order (Sport Club FR-135). */
function CourtPicker({ value, onChange }: { value: Row | null; onChange: (c: Row | null) => void }) {
  const [q, setQ] = useState('');
  const list = useGet<Page<Row>>(`/api/v1/sportclub/charge-targets${qs({ q: q.trim() || undefined })}`, { retry: false });
  return (
    <div className="pos-method-body">
      <input className="pos-input" placeholder="Cari kode booking, nama penyewa" value={q} onChange={(e) => setQ(e.target.value)} />
      <div className="pos-guests">
        {(list.data?.items ?? []).map((c) => (
          <button key={String(c.reservationId)} type="button" className="pos-guest" data-wide aria-pressed={value?.reservationId === c.reservationId}
            onClick={() => onChange(c)}>{String(c.name)} · {String(c.bookingCode)}<br /><small>{String(c.courts)}</small></button>
        ))}
        {list.data && list.data.items.length === 0 && <span className="pos-muted">Tidak ada booking lapangan yang sedang main.</span>}
      </div>
    </div>
  );
}
