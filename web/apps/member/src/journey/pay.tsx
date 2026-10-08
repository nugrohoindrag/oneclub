import React, { useEffect, useRef, useState } from 'react';
import { request, useGet, type Schemas } from '@oneclub/api-client';
import { ErrorAlert, Icon, QRCode } from '@oneclub/shell';
import { Chip, money } from './ui';

// Payment step of the member journey. Online payments go through the
// club's payment gateway; with the sandbox (mock) gateway the hosted
// payment page is the "Sandbox gateway" box, where the payer finishes the
// payment by hand (paid or failed) — settled exactly like a gateway webhook.

type Payment = Schemas['Payment'];
export type PayMethod = 'member_charge' | 'qris' | 'virtual_account' | 'card';

const METHODS: { value: PayMethod; label: string; help: string; icon: string }[] = [
  { value: 'member_charge', label: 'Member Account', help: 'Charged to your account, settled on the statement', icon: 'account_balance_wallet' },
  { value: 'qris', label: 'QRIS', help: 'Scan with any banking or e-wallet app', icon: 'qr_code_2' },
  { value: 'virtual_account', label: 'Virtual Account', help: 'Bank transfer to your VA number', icon: 'account_balance' },
  { value: 'card', label: 'Card', help: 'Credit or debit card on the payment page', icon: 'credit_card' },
];

export function MethodPicker({ value, onChange, memberCharge = true }: { value: PayMethod; onChange: (m: PayMethod) => void; memberCharge?: boolean }) {
  return (
    <div className="mj-choices" role="radiogroup" aria-label="Payment method">
      {METHODS.filter((m) => memberCharge || m.value !== 'member_charge').map((m) => (
        <button key={m.value} type="button" className="mj-choice" role="radio" aria-checked={value === m.value} aria-pressed={value === m.value} onClick={() => onChange(m.value)}>
          <span className="mj-icon"><Icon name={m.icon} size={20} /></span>
          <span><strong>{m.label}</strong><small>{m.help}</small></span>
        </button>
      ))}
    </div>
  );
}

/** The mock gateway's hosted payment page (sandbox integrations only). */
export function SandboxGateway({ number, onDone }: { number: string; onDone: () => void }) {
  const page = useGet<Schemas['SandboxCheckout']>(`/api/v1/public/sandbox-checkout/${encodeURIComponent(number)}`, { retry: false });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  if (!page.data || page.data.status !== 'pending') return null;
  const complete = async (outcome: 'paid' | 'failed') => {
    setBusy(true);
    setError(null);
    try {
      await request('POST', `/api/v1/public/sandbox-checkout/${encodeURIComponent(number)}:complete`, { outcome });
      await page.refetch();
      onDone();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="mj-gateway">
      <div className="mj-gateway-head"><Icon name="science" size={18} /> Sandbox payment gateway</div>
      <div className="mj-small">This club uses the mock gateway: no real money moves. Finish the payment here as the payer would on the bank or e-wallet page.</div>
      <ErrorAlert error={error} />
      <div className="oc-row-wrap">
        <button className="oc-btn oc-btn-primary" disabled={busy} onClick={() => void complete('paid')}><Icon name="check_circle" size={18} /> Pay {money(page.data.amount)}</button>
        <button className="oc-btn oc-btn-neutral" disabled={busy} onClick={() => void complete('failed')}>Simulate failure</button>
      </div>
    </div>
  );
}

/**
 * A pending online payment: amount, QRIS / VA / payment page and the
 * sandbox gateway. Polls the member's payment until it is settled.
 */
export function PaymentPanel({ payment, onPaid, onFailed, poll = true }: { payment: Payment; onPaid?: () => void; onFailed?: () => void; poll?: boolean }) {
  const st = useGet<Payment>(poll ? `/api/v1/member/payments/${payment.id}` : null, { refetchInterval: (q) => (q.state.data?.status === 'pending' || !q.state.data ? 3000 : false) });
  const p = st.data ?? payment;
  const fired = useRef(false);
  useEffect(() => {
    if (fired.current) return;
    if (p.status === 'completed') {
      fired.current = true;
      onPaid?.();
    } else if (p.status === 'cancelled') {
      fired.current = true;
      onFailed?.();
    }
  }, [p.status, onPaid, onFailed]);
  const sandbox = (p.checkoutUrl ?? '').includes('sandbox.pay.local');
  return (
    <div className="oc-stack" style={{ alignItems: 'center', gap: 12 }}>
      <div className="mj-small mj-muted">Amount to pay</div>
      <div className="mj-price mj-num" style={{ fontSize: 30 }}>{money(p.amount)}</div>
      {p.status === 'pending' && <Chip tone="warn">Waiting for payment</Chip>}
      {p.status === 'completed' && <Chip tone="ok">Paid</Chip>}
      {p.status === 'cancelled' && <Chip tone="bad">Payment failed or expired</Chip>}
      {p.status === 'pending' && p.qrString && <QRCode value={p.qrString} size={200} label="QRIS payment code" />}
      {p.status === 'pending' && p.vaNumber && <div className="oc-stack" style={{ alignItems: 'center', gap: 4 }}><span className="mj-small mj-muted">Virtual Account number</span><span className="mj-code">{p.vaNumber}</span></div>}
      {p.status === 'pending' && p.checkoutUrl && !sandbox && <a className="oc-btn oc-btn-outline" href={p.checkoutUrl} target="_blank" rel="noreferrer">Open payment page</a>}
      {p.status === 'pending' && <SandboxGateway number={p.number} onDone={() => void st.refetch()} />}
      <div className="mj-small mj-muted">Payment {p.number}</div>
    </div>
  );
}
