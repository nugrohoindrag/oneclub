import React, { useState } from 'react';
import { Link } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { request, useGet, type Page, type Schemas } from '@oneclub/api-client';
import { formatDate } from '@oneclub/i18n';
import { Empty, ErrorAlert, Icon, Modal, Skeleton } from '@oneclub/shell';
import { MemberCard, MyApplications } from '../golf';
import { currentMembership, DateBadge, useMembership } from './home';
import { MethodPicker, PaymentPanel, type PayMethod } from './pay';
import { Chip, Head, money, Rows, StatusChip } from './ui';

// Membership journey: overview (type, number, validity, status, benefits,
// card), Expiring Soon → Renew → Payment → Renewed, and My Guests.

export function MembershipOverviewPage() {
  const m = useMembership();
  const qc = useQueryClient();
  const ms = currentMembership(m.data);
  const [renew, setRenew] = useState(false);
  const p = m.data?.profile;
  return (
    <div className="mj-page">
      <Head title="Membership" />
      <ErrorAlert error={m.error} />
      {m.isLoading && <Skeleton rows={6} />}
      {p && (
        <>
          <div className="mj-grid-2">
            <MemberCard name={p.name} club={m.data!.clubName} card={m.data!.card as never} />
            <div className="mj-card">
              <h2><Icon name="workspace_premium" size={20} /> {ms ? `${ms.typeName} Member` : 'Membership'}</h2>
              {ms ? (
                <Rows rows={[['Member ID', ms.memberNo], ['Membership Type', ms.typeName], ['Status', <StatusChip status={ms.status} />],
                  ['Valid Until', ms.endsOn ? formatDate(ms.endsOn) : '—'], ['Role', ms.role === 'principal' ? 'Principal' : ms.relationship ?? ms.role]]} />
              ) : <p className="mj-muted">No membership yet.</p>}
              {ms && ms.status === 'active' && ms.role === 'principal' && (
                <div className="oc-row-wrap" style={{ marginTop: 14 }}>
                  {ms.daysToExpiry !== null && ms.daysToExpiry !== undefined && ms.daysToExpiry <= 60 && <Chip tone="warn">Expiring soon · {Math.max(0, ms.daysToExpiry)} days</Chip>}
                  <span className="oc-spacer" />
                  {ms.renewalPending ? <Chip tone="info">Renewal in progress</Chip> : <button className="oc-btn oc-btn-primary" onClick={() => setRenew(true)}><Icon name="autorenew" size={18} /> Renew Membership</button>}
                </div>
              )}
            </div>
          </div>
          <div className="mj-card">
            <h2><Icon name="verified" size={20} /> Benefits</h2>
            {(m.data?.benefits ?? []).length === 0 ? <p className="mj-muted">Your club has not listed benefits for this membership.</p> : (
              <div className="mj-checklist">{m.data!.benefits.map((b) => <div key={b} className="mj-check" data-done="true"><i aria-hidden>✓</i><span>{b}</span></div>)}</div>
            )}
          </div>
          {(p.renewals ?? []).length > 0 && (
            <div className="mj-card">
              <h2><Icon name="history" size={20} /> Renewals</h2>
              <div className="mj-list">
                {p.renewals.map((r) => (
                  <div key={r.id} className="mj-item"><div className="mj-item-body"><strong>Valid until {formatDate(r.newEndsOn)}</strong><span className="mj-small mj-muted">Requested {formatDate(r.createdAt)}</span></div><StatusChip status={r.status} /></div>
                ))}
              </div>
            </div>
          )}
          <MyApplications />
        </>
      )}
      {renew && ms && <RenewModal membershipId={ms.id} typeName={ms.typeName} onClose={() => { setRenew(false); void qc.invalidateQueries(); }} />}
    </div>
  );
}

function RenewModal({ membershipId, typeName, onClose }: { membershipId: string; typeName: string; onClose: () => void }) {
  const [method, setMethod] = useState<PayMethod>('qris');
  const [payment, setPayment] = useState<Schemas['Payment'] | null>(null);
  const [done, setDone] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const start = async () => {
    setBusy(true);
    setError(null);
    try {
      const r = await request<Schemas['MyRenewal']>('POST', `/api/v1/member/memberships/${membershipId}:renew`, { method });
      if (r.payment && r.payment.status === 'pending') setPayment(r.payment);
      else setDone(true);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal open onClose={onClose} title={`Renew ${typeName}`} actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>{done ? 'Close' : 'Cancel'}</button>
      {!payment && !done && <button className="oc-btn oc-btn-primary" disabled={busy} onClick={() => void start()}>Continue to payment</button>}
    </>}>
      <ErrorAlert error={error} />
      {!payment && !done && <div className="oc-stack"><p className="mj-muted" style={{ margin: 0 }}>The renewal fee of the next period is charged; your membership is extended once it is paid.</p><MethodPicker value={method} onChange={setMethod} memberCharge={false} /></div>}
      {payment && !done && <PaymentPanel payment={payment} onPaid={() => setDone(true)} />}
      {done && <div className="mj-success"><span className="mj-success-mark"><Icon name="check" size={34} /></span><h1>Membership Renewed</h1><p className="mj-muted">Your new validity shows on your card in a moment.</p></div>}
    </Modal>
  );
}

export function GuestsPage() {
  const g = useGet<Page<Schemas['MyGuest']>>('/api/v1/member/golf/guests');
  return (
    <div className="mj-page">
      <Head title="My Guests" help="Guests you played with. Pick them when you book a tee time." actions={<Link className="oc-btn oc-btn-primary" to="/book/tee-time">Book with a guest</Link>} />
      <div className="mj-card">
        {g.isLoading && <Skeleton rows={4} />}
        {g.data?.items.length === 0 && <Empty title="No guests yet" help="Guests you add to a tee time booking appear here." icon="group_add" />}
        <div className="mj-list">
          {(g.data?.items ?? []).map((x) => (
            <div key={x.name} className="mj-item">
              <DateBadge iso={x.lastPlayed} />
              <div className="mj-item-body"><strong>{x.name}</strong><span className="mj-small mj-muted">{x.phone ?? 'No phone'} · last played {formatDate(x.lastPlayed)}</span></div>
              <Chip tone="info">{x.rounds} {x.rounds === 1 ? 'round' : 'rounds'}</Chip>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

// ── Transactions ──────────────────────────────────────────────────────────

const SOURCE: Record<string, [string, string]> = {
  golf_booking: ['sports_golf', 'Tee Time'], 'reservation.reservation': ['cottage', 'Stay'], reservation: ['cottage', 'Stay'], banquet_event: ['celebration', 'Event'],
  pos_order: ['restaurant', 'Food & Beverage'], membership_fee: ['card_membership', 'Membership Fee'], membership_renewal: ['autorenew', 'Membership Renewal'],
  package_booking: ['card_travel', 'Package'], tournament: ['emoji_events', 'Tournament'], sport_entry: ['sports_tennis', 'Sport Club'],
  class_enrollment: ['sports_tennis', 'Class'], locker: ['lock', 'Locker'], bag_storage: ['luggage', 'Bag Storage'],
};

export function TransactionsPage() {
  const t = useGet<Schemas['MyTransactions']>('/api/v1/member/transactions');
  const qc = useQueryClient();
  const [pay, setPay] = useState<Schemas['Payment'] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const folios = [...(t.data?.folios ?? [])].sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  const outstanding = folios.reduce((s, f) => s + Math.max(0, Number(f.summary.balance)), 0);
  const payFolio = async (id: string) => {
    setError(null);
    try {
      setPay(await request<Schemas['Payment']>('POST', `/api/v1/member/folios/${id}:pay-online`, { method: 'qris' }));
    } catch (e) {
      setError(e);
    }
  };
  const acct = t.data?.account as { balance?: string } | null | undefined;
  return (
    <div className="mj-page">
      <Head title="Transactions" />
      <ErrorAlert error={error ?? t.error} />
      <div className="mj-stats">
        <div className="mj-card" style={{ padding: 14 }}><span className="mj-small mj-muted">Outstanding</span><br /><strong style={{ color: outstanding > 0 ? 'var(--mj-bad)' : undefined }} className="mj-num">{money(outstanding)}</strong></div>
        <div className="mj-card" style={{ padding: 14 }}><span className="mj-small mj-muted">Member account</span><br /><strong className="mj-num">{money(acct?.balance)}</strong></div>
        <div className="mj-card" style={{ padding: 14 }}><span className="mj-small mj-muted">Transactions</span><br /><strong className="mj-num">{folios.length}</strong></div>
      </div>
      <div className="mj-card">
        {t.isLoading && <Skeleton rows={5} />}
        {!t.isLoading && folios.length === 0 && <Empty title="No transactions yet" icon="receipt_long" />}
        <div className="mj-list">
          {folios.map((f) => {
            const [icon, label] = SOURCE[f.sourceType ?? ''] ?? ['receipt', f.sourceRef || 'Transaction'];
            const bal = Number(f.summary.balance);
            return (
              <div key={f.id} className="mj-item">
                <DateBadge iso={f.createdAt} />
                <div className="mj-item-body">
                  <span className="mj-small mj-muted"><Icon name={icon} size={14} /> {label}</span>
                  <strong>{f.sourceRef || f.number}</strong>
                  <span className="mj-small mj-muted">{f.number}</span>
                </div>
                <div className="mj-item-end">
                  <strong className="mj-num">{money(f.summary.charges)}</strong>
                  {bal > 0 ? <Chip tone="warn">Outstanding {money(bal)}</Chip> : Number(f.summary.charges) > 0 ? <Chip tone="ok">Paid</Chip> : <StatusChip status={f.status} />}
                  {bal > 0 && <button className="oc-btn oc-btn-primary oc-btn-sm" onClick={() => void payFolio(f.id)}>Pay</button>}
                </div>
              </div>
            );
          })}
        </div>
      </div>
      <div className="oc-row-wrap">
        <Link className="oc-btn oc-btn-outline" to="/transactions/payments"><Icon name="receipt" size={18} /> Receipts</Link>
        <Link className="oc-btn oc-btn-outline" to="/membership/statements"><Icon name="description" size={18} /> Statements</Link>
      </div>
      <Modal open={!!pay} onClose={() => { setPay(null); void qc.invalidateQueries(); }} title="Pay outstanding" actions={<button className="oc-btn oc-btn-ink" onClick={() => { setPay(null); void qc.invalidateQueries(); }}>Close</button>}>
        {pay && <PaymentPanel payment={pay} onPaid={() => void qc.invalidateQueries()} />}
      </Modal>
    </div>
  );
}
