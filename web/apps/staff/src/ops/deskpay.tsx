import React, { useEffect, useState } from 'react';
import { createPortal } from 'react-dom';
import { getActiveProperty, qs, request } from '@oneclub/api-client';
import { formatDateTime } from '@oneclub/i18n';
import { ErrorAlert, Icon } from '@oneclub/shell';
import { MoneyInput } from '@oneclub/ui';
import { Basket, Brand } from '../pos/pay';
import { PosDialog, money } from '../pos/shared';

// Payment page of the front desk (demo feedback 9 Oct 2026): the Bill tab,
// the merged bill and the Check-out settle take the payment like the POS
// Cashier — pick the method on cards, enter the reference or the cash
// received (change), confirm, then a success screen with the receipt to
// print or send. The amounts and the endpoints stay those of the booking
// bill; only the screens are shared with the POS.

type R = Record<string, unknown>;

export type DeskMethod = 'cash' | 'card' | 'qris' | 'bank_transfer' | 'member_account';
const LABEL: Record<DeskMethod, string> = { cash: 'Cash', card: 'Card (EDC)', qris: 'QRIS', bank_transfer: 'Bank Transfer', member_account: 'Member Account' };

/** One tender of the desk. */
export interface DeskTender { methodType: DeskMethod; reference?: string; customerId?: string }

/** A member whose account can be charged. */
export interface DeskMember { customerId: string; name: string }

function MethodIcon({ m }: { m: DeskMethod }) {
  if (m === 'bank_transfer') return <span className="pos-brand-icon" aria-hidden="true"><Icon name="account_balance" size={16} /></span>;
  return <Brand m={m} />;
}

/**
 * Payment Method → Payment Confirm → Payment Successful. `pay` takes the
 * tender and returns the payments it recorded (for the receipts).
 */
export function DeskPayDialog({ amount, summary, members = [], methods, onClose, pay, onFinish }: {
  amount: number; summary: [string, string][]; members?: DeskMember[]; methods?: DeskMethod[];
  onClose: () => void; pay: (t: DeskTender) => Promise<R[]>; onFinish: () => void;
}) {
  const offered: DeskMethod[] = (methods ?? ['cash', 'card', 'qris', 'bank_transfer', 'member_account']).filter((m) => m !== 'member_account' || members.length > 0);
  const [step, setStep] = useState<'method' | 'confirm' | 'done'>('method');
  const [method, setMethod] = useState<DeskMethod>(offered[0] ?? 'cash');
  const [ref, setRef] = useState('');
  const [received, setReceived] = useState('');
  const [member, setMember] = useState(members[0]?.customerId ?? '');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [paid, setPaid] = useState<R[]>([]);
  const [sent, setSent] = useState(false);
  const cash = method === 'cash' && received !== '' ? Number(received) : amount;
  const change = method === 'cash' ? Math.max(cash - amount, 0) : 0;
  const payer = members.find((m) => m.customerId === member);
  const ready = amount > 0 && !(method === 'cash' && cash < amount) && !(method === 'member_account' && !payer);
  const close = step === 'done' ? onFinish : onClose;
  const receipt = (p: R) => `/api/v1/billing/payments/${String(p.id)}/receipt${qs({ propertyId: getActiveProperty() })}`;

  // Escape closes this page only, not the booking window behind it
  useEffect(() => {
    const k = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return;
      e.stopPropagation();
      if (!busy) close();
    };
    window.addEventListener('keydown', k, true);
    return () => window.removeEventListener('keydown', k, true);
  }, [busy, close]);

  const confirm = async () => {
    setBusy(true);
    setError(null);
    try {
      setPaid(await pay({ methodType: method, reference: ref.trim() || undefined, customerId: method === 'member_account' ? member : undefined }));
      setStep('done');
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  const send = async () => {
    setError(null);
    try {
      for (const p of paid) await request('POST', `/api/v1/billing/payments/${String(p.id)}:send-receipt`, {});
      setSent(true);
    } catch (e) {
      setError(e);
    }
  };

  const body = step === 'method' ? (
    <PosDialog title="Payments Method" sub="Select a payment method below. Please double check before finishing your payment" onClose={close}>
      <div className="pos-amount" aria-label="Amount due">{money(amount)}</div>
      {offered.map((m) => (
        <div key={m} className="pos-method" aria-expanded={method === m}>
          <button type="button" className="pos-method-head" onClick={() => setMethod(m)} aria-pressed={method === m}>
            <span className="pos-radio" data-on={method === m} /><MethodIcon m={m} />{m === 'qris' ? null : LABEL[m]}
            {m === 'qris' && <span className="pos-muted" style={{ fontWeight: 500 }}>Scan to pay</span>}
          </button>
          {method === m && ['card', 'qris', 'bank_transfer'].includes(m) && (
            <div className="pos-method-body">
              <input className="pos-input" placeholder={m === 'card' ? 'Approval code (EDC)' : m === 'qris' ? 'QRIS reference (optional)' : 'Transfer reference'}
                value={ref} onChange={(e) => setRef(e.target.value)} />
            </div>
          )}
          {method === m && m === 'cash' && (
            <div className="pos-method-body">
              <MoneyInput className="pos-input" placeholder={`Cash received (${money(amount)})`} aria-label="Cash received" value={received} onChange={setReceived} />
              <div className="pos-guests">
                {[amount, Math.ceil(amount / 50000) * 50000, Math.ceil(amount / 100000) * 100000].filter((v, i, a) => a.indexOf(v) === i).map((v) => (
                  <button key={v} type="button" className="pos-guest" data-wide aria-pressed={Number(received) === v} onClick={() => setReceived(String(v))}>{money(v)}</button>
                ))}
              </div>
              {cash < amount && <span className="pos-muted">The cash received is below the amount.</span>}
              {change > 0 && <strong>Change: {money(change)}</strong>}
            </div>
          )}
          {method === m && m === 'member_account' && (
            <div className="pos-method-body">
              <div className="pos-guests">
                {members.map((p) => (
                  <button key={p.customerId} type="button" className="pos-guest" data-wide aria-pressed={member === p.customerId} onClick={() => setMember(p.customerId)}>{p.name}</button>
                ))}
              </div>
              <span className="pos-muted">Charged to the member account; it appears on the member statement.</span>
            </div>
          )}
        </div>
      ))}
      <button className="pos-btn pos-pill" data-block style={{ marginTop: 8 }} disabled={!ready} onClick={() => setStep('confirm')}>Next</button>
    </PosDialog>
  ) : step === 'confirm' ? (
    <PosDialog title="Payment Confirm" sub="Check the payment with the guest before you confirm it." onClose={close}>
      <div className="pos-amount" aria-label="Amount">{money(amount)}</div>
      {summary.map(([k, v]) => <div key={k} className="pos-receipt-row"><span>{k}</span><strong>{v}</strong></div>)}
      <div className="pos-receipt-cut" />
      <div className="pos-receipt-row"><span>Payment</span><strong>{LABEL[method]}{method === 'member_account' && payer ? ` · ${payer.name}` : ''}</strong></div>
      {ref.trim() && method !== 'cash' && method !== 'member_account' && <div className="pos-receipt-row"><span>Reference</span><strong>{ref.trim()}</strong></div>}
      {method === 'cash' && <div className="pos-receipt-row"><span>Cash received</span><strong>{money(cash)}</strong></div>}
      {method === 'cash' && <div className="pos-receipt-row"><span>Change</span><strong>{money(change)}</strong></div>}
      <ErrorAlert error={error} />
      <div className="pos-dialog-actions">
        <button className="pos-btn" data-variant="outline" disabled={busy} onClick={() => setStep('method')}>Back</button>
        <button className="pos-btn" disabled={busy} onClick={() => void confirm()}>Confirm Payment</button>
      </div>
    </PosDialog>
  ) : (
    <PosDialog title="Payment Successful" onClose={close} label="Payment successful" art={<Basket />}>
      {paid.map((p) => <div key={String(p.id)} className="pos-receipt-row"><span>No. Payment</span><strong>{String(p.number)}{paid.length > 1 && p.payerName ? ` · ${String(p.payerName)}` : ''}</strong></div>)}
      {summary.map(([k, v]) => <div key={k} className="pos-receipt-row"><span>{k}</span><strong>{v}</strong></div>)}
      <div className="pos-receipt-row"><span>Payment</span><strong>{LABEL[method]}{method === 'member_account' && payer ? ` · ${payer.name}` : ''}</strong></div>
      <div className="pos-receipt-row"><span>Date</span><strong>{formatDateTime(new Date())}</strong></div>
      <div className="pos-receipt-cut" />
      {method === 'cash' && change > 0 && <div className="pos-receipt-row"><span>Change</span><strong>{money(change)}</strong></div>}
      <div className="pos-receipt-total"><span>Paid</span><span>{money(amount)}</span></div>
      <ErrorAlert error={error} />
      <div className="pos-dialog-actions">
        {paid.length === 1
          ? <a className="pos-btn" data-variant="outline" href={receipt(paid[0])} target="_blank" rel="noreferrer"><Icon name="print" size={20} />Print Receipt</a>
          : <button className="pos-btn" data-variant="outline" disabled={!paid.length}
            onClick={() => paid.forEach((p) => window.open(receipt(p), '_blank'))}><Icon name="print" size={20} />Print Receipts</button>}
        <button className="pos-btn" data-variant="outline" disabled={sent || !paid.length} onClick={() => void send()}>
          <Icon name={sent ? 'check' : 'send'} size={20} />{sent ? 'Sent' : 'Send Receipt'}</button>
      </div>
      <button className="pos-btn pos-pill" data-block style={{ marginTop: 12 }} onClick={onFinish}>Done</button>
    </PosDialog>
  );
  // above the booking window (a modal of the shell)
  return createPortal(<div className="desk-pay">{body}</div>, document.body);
}

/** The payments of a bill that were not there before. */
export function newPayments(before: R[] | undefined, after: R[] | undefined): R[] {
  const seen = new Set((before ?? []).map((p) => String(p.id)));
  return (after ?? []).filter((p) => !seen.has(String(p.id)) && p.status === 'completed');
}
