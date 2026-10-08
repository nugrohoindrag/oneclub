import { useMemo, useState } from 'react';
import { Link, Navigate, useSearchParams } from 'react-router';
import { request, useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDateTime } from '@oneclub/i18n';
import {
  Card, DataTable, Drawer, Empty, ErrorAlert, Icon, Modal, PageHeader, Pager, SelectField, Skeleton, StatTile, StatusPill, TextArea, TextField, useAuth, usePager, useToast,
  type Column,
} from '@oneclub/shell';
import { useQueryClient } from '@tanstack/react-query';
import { ActionButton, KV, ListPage, Tabs, money, today, type R } from '../p1/common';
import { StatementDrawer } from '../p3/billing';
import { ACC, API, AccountSelect, BankSelect, addDays, dt, items, label, pill, rowsOf, SidebarTabs, useTab, type SidebarTab } from './accounting-common';

// Accounts Receivable and Accounts Payable of the Accountant, by activity:
// transactions (AR: invoices, payments, credit notes, collections,
// reconciliation; AP: bills, payments, debit/credit notes, vendor follow-up,
// reconciliation), reports (receivables, ledger, aging, statements) and
// adjustments (allowance & write-off). Invoices, payments, credit notes and
// write-offs stay in billing (contract K2); the open items, aging and the
// follow-up log come from /api/v1/accounting.

const BILLING = '/api/v1/billing';
const INV = [BILLING, API];
const OPEN = ['issued', 'partially_paid', 'overdue'];

type Worklist = Schemas['FollowUpWorklist'];
type FollowUpRow = Schemas['FollowUpRow'];
type FollowUpParty = Schemas['FollowUpParty'];

const DUE: Record<string, [string, string]> = {
  overdue: ['rejected', 'Overdue'], due_today: ['pending', 'Due today'], due_soon: ['pending', 'Due soon'], current: ['confirmed', 'Current'],
};
const ACTIONS: { value: string; label: string; party?: 'customer' | 'supplier' }[] = [
  { value: 'call', label: 'Call' }, { value: 'email', label: 'E-mail' }, { value: 'meeting', label: 'Meeting' },
  { value: 'reminder', label: 'Reminder sent', party: 'customer' }, { value: 'promise_to_pay', label: 'Promise to pay', party: 'customer' },
  { value: 'dispute', label: 'Dispute' }, { value: 'note', label: 'Note' },
];
const actionLabel = (v?: string | null) => ACTIONS.find((a) => a.value === v)?.label ?? label(v);

/** Legacy tab links (?tab=aging …) open their new place. */
function Legacy({ to }: { to: string }) {
  return <Navigate to={to} replace />;
}

// ── aging (reports) ───────────────────────────────────────────────────────

const agingCols = (ap: boolean): Column<R>[] => [
  { key: ap ? 'supplierName' : 'billToName', header: ap ? 'Supplier' : 'Customer' },
  ...(ap ? [{ key: 'current', header: 'Not due', align: 'right' as const, render: (r: R) => money(r.current) },
    { key: 'days1to30', header: '1–30', align: 'right' as const, render: (r: R) => money(r.days1to30) }]
    : [{ key: 'days0to30', header: '0–30', align: 'right' as const, render: (r: R) => money(r.days0to30) }]),
  { key: 'days31to60', header: '31–60', align: 'right', render: (r) => money(r.days31to60) },
  { key: 'days61to90', header: '61–90', align: 'right', render: (r) => money(r.days61to90) },
  { key: 'over90', header: '> 90', align: 'right', render: (r) => money(r.over90) },
  { key: 'total', header: 'Total', align: 'right', render: (r) => <strong>{money(r.total)}</strong> },
];

function AgingTab({ ap }: { ap?: boolean }) {
  const [asOf, setAsOf] = useState(today());
  const a = useGet<R>(`${API}/${ap ? 'ap' : 'ar'}-aging?asOf=${asOf}`);
  const totals = (a.data?.totals ?? {}) as R;
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap"><input className="oc-input oc-filter" type="date" aria-label="As of" value={asOf} onChange={(e) => e.target.value && setAsOf(e.target.value)} /></div>
      <ErrorAlert error={a.error} />
      {a.data && !ap && <KV items={[['AR control account', money(a.data.arControl)], ['Total AR aging', money(totals.total)],
        ['Difference', a.data.difference === '0' ? <StatusPill key="d" status="matched" label="0 (reconciled)" /> : money(a.data.difference)]]} />}
      <DataTable rows={items(a.data?.rows)} loading={a.isLoading} rowKey={(r) => String(r.supplierId ?? r.accountId ?? r.customerId ?? r.billToName)} columns={agingCols(!!ap)} />
    </div>
  );
}

// ── Collections / Vendor Follow-up ────────────────────────────────────────

function FollowUpModal({ party, data, invoiceId, onClose }: { party: 'customer' | 'supplier'; data: FollowUpParty; invoiceId?: string; onClose: () => void }) {
  const path = `${API}/${party === 'customer' ? 'collections' : 'vendor-follow-ups'}/${data.partyId}`;
  const send = useSend<Record<string, unknown>, R>('POST', `${path}/follow-ups`, [API]);
  const [f, setF] = useState({ action: 'call', notes: '', invoiceId: invoiceId ?? '', promisedDate: '', promisedAmount: '', nextFollowUp: addDays(7) });
  const upd = (k: keyof typeof f) => (v: string) => setF({ ...f, [k]: v });
  const inv = data.openItems.find((i) => i.id === f.invoiceId);
  return (
    <Modal open onClose={onClose} title={`Record follow-up · ${data.partyName}`} actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending || (f.action === 'promise_to_pay' && !f.promisedDate)}
        onClick={() => send.mutate({ partyName: data.partyName, action: f.action, notes: f.notes || undefined, invoiceId: f.invoiceId || undefined,
          invoiceNumber: inv?.number, promisedDate: f.promisedDate || undefined, promisedAmount: f.promisedAmount || undefined,
          nextFollowUp: f.nextFollowUp || undefined }, { onSuccess: onClose })}>Save</button></>}>
      <div className="oc-form">
        <SelectField label="Activity" value={f.action} onChange={upd('action')} options={ACTIONS.filter((a) => !a.party || a.party === party)} />
        <SelectField label={party === 'customer' ? 'Invoice' : 'Bill'} value={f.invoiceId} onChange={upd('invoiceId')} placeholder="All open items"
          options={data.openItems.map((i) => ({ value: i.id, label: `${i.number} · ${money(i.amount)}` }))} />
        {f.action === 'promise_to_pay' && <>
          <TextField label="Promised date" type="date" value={f.promisedDate} onChange={upd('promisedDate')} required />
          <TextField label="Promised amount" value={f.promisedAmount} onChange={upd('promisedAmount')} />
        </>}
        <TextField label="Next follow-up" type="date" value={f.nextFollowUp} onChange={upd('nextFollowUp')} help="Leave empty when nothing is planned" />
        <TextArea label="Notes" value={f.notes} onChange={upd('notes')} rows={3} span />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function PartyDrawer({ party, id, onClose }: { party: 'customer' | 'supplier'; id: string; onClose: () => void }) {
  const { can } = useAuth();
  const toast = useToast();
  const qc = useQueryClient();
  const base = `${API}/${party === 'customer' ? 'collections' : 'vendor-follow-ups'}/${id}`;
  const d = useGet<FollowUpParty>(base);
  const [selected, setSelected] = useState<string[]>([]);
  const [record, setRecord] = useState(false);
  const [sending, setSending] = useState(false);
  const x = d.data;
  const manage = can(party === 'customer' ? 'accounting.receivable.manage' : 'accounting.payable.manage');
  const remind = async () => {
    if (!x) return;
    const targets = x.openItems.filter((i) => selected.includes(i.id));
    setSending(true);
    try {
      for (const i of targets) {
        await request('POST', `${BILLING}/invoices/${i.id}:remind`, {});
        await request('POST', `${base}/follow-ups`, { partyName: x.partyName, action: 'reminder', invoiceId: i.id, invoiceNumber: i.number, nextFollowUp: addDays(7) });
      }
      toast(`${targets.length} reminder${targets.length === 1 ? '' : 's'} sent`);
      setSelected([]);
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Sending failed', 'error');
    } finally {
      setSending(false);
      void qc.invalidateQueries({ predicate: (q) => String(q.queryKey[0]).startsWith(API) });
    }
  };
  const toggle = (iid: string) => setSelected((s) => (s.includes(iid) ? s.filter((v) => v !== iid) : [...s, iid]));
  const itemsPage = usePager(d.data?.openItems);
  return (
    <Drawer open onClose={onClose} title={x?.partyName ?? (party === 'customer' ? 'Customer' : 'Supplier')}>
      {d.isLoading && <Skeleton />}
      <ErrorAlert error={d.error} />
      {x && (
        <div className="oc-stack">
          <div className="oc-row-wrap">
            {party === 'customer' && can('billing.invoice.issue') && manage && (
              <button className="oc-btn oc-btn-primary oc-btn-sm" disabled={selected.length === 0 || sending} onClick={() => void remind()}>
                <Icon name="send" size={16} /> Send reminder{selected.length > 0 ? ` (${selected.length})` : ''}
              </button>
            )}
            {manage && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setRecord(true)}><Icon name="edit_note" size={16} /> Record follow-up</button>}
          </div>
          <Card title={party === 'customer' ? 'Open invoices' : 'Open bills'}>
            {x.openItems.length === 0 ? <Empty title="Nothing open" /> : (<>
              <div className="oc-table-wrap">
                <table className="oc-table oc-small">
                  <thead><tr>{party === 'customer' && <th aria-label="Select" />}<th>{party === 'customer' ? 'Invoice' : 'Bill'}</th><th>Due date</th>
                    <th style={{ textAlign: 'right' }}>Outstanding</th><th>Status</th></tr></thead>
                  <tbody>
                    {itemsPage.visible.map((i) => {
                      const [tone, l] = DUE[i.status] ?? ['draft', i.status];
                      return (
                        <tr key={i.id}>
                          {party === 'customer' && <td><input type="checkbox" aria-label={`Select ${i.number}`} checked={selected.includes(i.id)} onChange={() => toggle(i.id)} /></td>}
                          <td style={{ whiteSpace: 'nowrap' }}>{party === 'customer'
                            ? <Link to={`/accounting/revenue?tab=invoices&id=${i.id}`} title="View invoice">{i.number}</Link> : i.number}</td>
                          <td style={{ whiteSpace: 'nowrap' }}>{dt(i.dueDate)}{i.daysOverdue > 0 && <div className="oc-muted">{i.daysOverdue} days late</div>}</td>
                          <td className="oc-num" style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>{money(i.amount)}</td>
                          <td><StatusPill status={tone} label={l} /></td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
              <Pager {...itemsPage} />
            </>)}
          </Card>
          <Card title="Follow-up log">
            {x.followUps.length === 0 ? <Empty title="No follow-up yet" /> : (
              <div className="oc-stack" style={{ gap: 0 }}>
                {x.followUps.map((f) => (
                  <div key={f.id} className="oc-fin-line" style={{ alignItems: 'flex-start' }}>
                    <span style={{ display: 'block' }}>
                      <strong>{actionLabel(f.action)}</strong>{f.invoiceNumber && <span className="oc-muted"> · {f.invoiceNumber}</span>}
                      {f.notes && <div>{f.notes}</div>}
                      {f.promisedDate && <div className="oc-small">Promised {dt(f.promisedDate)}{f.promisedAmount && ` · ${money(f.promisedAmount)}`}</div>}
                      <div className="oc-small oc-muted">{formatDateTime(f.createdAt)} · {f.createdByName ?? '—'}{f.responsibleName && ` · responsible ${f.responsibleName}`}</div>
                    </span>
                    {f.nextFollowUp && <span className="oc-small oc-muted" style={{ whiteSpace: 'nowrap' }}>Next {dt(f.nextFollowUp)}</span>}
                  </div>
                ))}
              </div>
            )}
          </Card>
          {record && <FollowUpModal party={party} data={x} invoiceId={selected[0]} onClose={() => setRecord(false)} />}
        </div>
      )}
    </Drawer>
  );
}

/** Collections (customers) or Vendor Follow-up (suppliers) worklist. */
function FollowUpWorklistTab({ party }: { party: 'customer' | 'supplier' }) {
  const [filter, setFilter] = useState<'all' | 'due' | 'overdue'>('all');
  const [open, setOpen] = useState('');
  const d = useGet<Worklist>(`${API}/${party === 'customer' ? 'collections' : 'vendor-follow-ups'}`);
  const rows = (d.data?.rows ?? []).filter((r) => filter === 'all' || (filter === 'due' ? r.followUpDue : r.status === 'overdue'));
  const who = party === 'customer' ? 'Customer' : 'Supplier';
  const cols: Column<FollowUpRow & Record<string, unknown>>[] = [
    { key: 'partyName', header: who, render: (r) => <><strong>{r.partyName}</strong><div className="oc-small oc-muted">{r.openItems} open</div></> },
    { key: 'outstanding', header: 'Outstanding', align: 'right', render: (r) => money(r.outstanding) },
    { key: 'overdue', header: 'Overdue', align: 'right', render: (r) => (Number(r.overdue) > 0
      ? <><span className="oc-num">{money(r.overdue)}</span><div className="oc-small oc-muted">{r.daysOverdue} days</div></> : '—') },
    { key: 'lastAction', header: party === 'customer' ? 'Last follow-up' : 'Last contact', render: (r) => (r.lastAction
      ? <>{actionLabel(r.lastAction)}<div className="oc-small oc-muted">{r.lastActionAt ? formatDateTime(r.lastActionAt) : ''}</div></> : <span className="oc-muted">None</span>) },
    { key: 'nextFollowUp', header: 'Next follow-up', render: (r) => (r.nextFollowUp
      ? <span style={{ color: r.followUpDue ? 'var(--md-sys-color-error)' : undefined, fontWeight: r.followUpDue ? 600 : undefined }}>{dt(r.nextFollowUp)}</span> : '—') },
    { key: 'responsibleName', header: 'Responsible', render: (r) => r.responsibleName ?? '—' },
    { key: 'status', header: 'Status', render: (r) => { const [tone, l] = DUE[r.status] ?? ['draft', r.status]; return <StatusPill status={tone} label={l} />; } },
  ];
  return (
    <div className="oc-stack">
      {d.data && (
        <div className="oc-stat-grid">
          <StatTile label="Outstanding" icon="request_quote" value={money(d.data.outstanding)} />
          <StatTile label="Overdue" icon="schedule" value={money(d.data.overdue)} />
          <StatTile label="Follow-ups due" icon="notifications" value={String(d.data.dueToday)} />
        </div>
      )}
      <div className="oc-row-wrap" role="tablist">
        {([['all', 'All'], ['due', 'Follow-up due'], ['overdue', 'Overdue']] as const).map(([v, l]) => (
          <button key={v} role="tab" className="oc-chip" aria-pressed={filter === v} aria-selected={filter === v} onClick={() => setFilter(v)}>{l}</button>
        ))}
      </div>
      <DataTable rows={rows as (FollowUpRow & Record<string, unknown>)[]} loading={d.isLoading} error={d.error} rowKey={(r) => r.partyId}
        onRowClick={(r) => setOpen(r.partyId)} columns={cols}
        empty={<Empty title={party === 'customer' ? 'No receivable to collect' : 'No payable to follow up'} />} />
      {open && <PartyDrawer party={party} id={open} onClose={() => setOpen('')} />}
    </div>
  );
}

// ── AR: payments, allocation, credit notes ────────────────────────────────

type Alloc = Record<string, string>;

/** Open invoices of a customer account with an amount to allocate each (FIFO by due date). */
function AllocationTable({ accountId, amount, alloc, setAlloc }: { accountId: string; amount: number; alloc: Alloc; setAlloc: (a: Alloc) => void }) {
  const inv = useGet<Page<R>>(accountId ? `${BILLING}/invoices?filter[accountId]=${accountId}&limit=200` : null);
  const open = useMemo(() => rowsOf(inv.data).filter((i) => OPEN.includes(String(i.status)) && Number(i.outstanding) > 0)
    .sort((a, b) => String(a.dueDate ?? '').localeCompare(String(b.dueDate ?? ''))), [inv.data]);
  const fifo = () => {
    let left = amount;
    const next: Alloc = {};
    for (const i of open) {
      const v = Math.min(left, Number(i.outstanding));
      if (v > 0) next[String(i.id)] = String(v);
      left -= v;
    }
    setAlloc(next);
  };
  const used = Object.values(alloc).reduce((s, v) => s + (Number(v) || 0), 0);
  const pg = usePager(open);
  if (!accountId) return null;
  if (inv.isLoading) return <Skeleton rows={3} />;
  if (open.length === 0) return <Empty title="No open invoice" help="The payment stays unallocated (customer advance) until an invoice is issued." />;
  return (
    <div className="oc-stack" style={{ gap: 8 }}>
      <div className="oc-row"><strong>Allocate to invoices</strong><span className="oc-spacer" />
        <button type="button" className="oc-btn oc-btn-sm oc-btn-text" onClick={fifo} disabled={!amount}>Oldest first</button></div>
      <div className="oc-table-wrap">
        <table className="oc-table oc-small">
          <thead><tr><th>Invoice</th><th>Due</th><th style={{ textAlign: 'right' }}>Outstanding</th><th style={{ textAlign: 'right' }}>Allocate</th></tr></thead>
          <tbody>
            {pg.visible.map((i) => (
              <tr key={String(i.id)}>
                <td>{String(i.number)}</td><td>{dt(i.dueDate)}</td>
                <td className="oc-num" style={{ textAlign: 'right' }}>{money(i.outstanding)}</td>
                <td style={{ textAlign: 'right' }}>
                  <input className="oc-input" style={{ width: 150, height: 36, textAlign: 'right' }} inputMode="decimal" aria-label={`Allocate to ${String(i.number)}`}
                    value={alloc[String(i.id)] ?? ''} onChange={(e) => setAlloc({ ...alloc, [String(i.id)]: e.target.value })} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <Pager {...pg} />
      <div className="oc-row oc-small"><span>Allocated {money(used)}</span><span className="oc-spacer" />
        <span style={{ color: used > amount ? 'var(--md-sys-color-error)' : undefined }}>
          {used > amount ? 'More than the payment' : `Unallocated ${money(Math.max(0, amount - used))}`}</span></div>
    </div>
  );
}

const allocations = (alloc: Alloc) => Object.entries(alloc).filter(([, v]) => Number(v) > 0).map(([invoiceId, amount]) => ({ invoiceId, amount }));

/** Receive Payment: customer → bank / method → amount → allocate to invoices → post. */
function ReceivePaymentModal({ onClose }: { onClose: () => void }) {
  const toast = useToast();
  const qc = useQueryClient();
  const customers = useGet<Page<R>>(`${API}/receivables`);
  const methods = useGet<Page<R>>(`${BILLING}/payment-methods?limit=100&filter[status]=active`);
  const [f, setF] = useState({ accountId: '', paymentMethodId: '', amount: '', reference: '', payerName: '' });
  const [alloc, setAlloc] = useState<Alloc>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const upd = (k: keyof typeof f) => (v: string) => setF({ ...f, [k]: v });
  const method = rowsOf(methods.data).find((m) => m.id === f.paymentMethodId);
  const amount = Number(f.amount) || 0;
  const used = Object.values(alloc).reduce((s, v) => s + (Number(v) || 0), 0);
  const customer = rowsOf(customers.data).find((c) => c.accountId === f.accountId);
  const post = async () => {
    setBusy(true);
    setError(null);
    try {
      const p = await request<R>('POST', `${BILLING}/payments`, { accountId: f.accountId, paymentMethodId: f.paymentMethodId || undefined,
        methodType: String(method?.methodType ?? 'bank_transfer'), amount: f.amount, reference: f.reference || undefined,
        payerName: f.payerName || String(customer?.billToName ?? '') || undefined }, { 'Idempotency-Key': crypto.randomUUID() });
      const a = allocations(alloc);
      if (a.length > 0) await request('POST', `${BILLING}/payment-allocations`, { paymentId: p.id, allocations: a });
      toast(`Payment ${String(p.number)} posted${a.length > 0 ? ` and allocated to ${a.length} invoice${a.length === 1 ? '' : 's'}` : ' (unallocated)'}`);
      void qc.invalidateQueries({ predicate: (q) => INV.some((b) => String(q.queryKey[0]).startsWith(b)) });
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal open onClose={onClose} title="Receive Payment" wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={busy || !f.accountId || amount <= 0 || used > amount} onClick={() => void post()}>Post payment</button></>}>
      <div className="oc-form">
        <SelectField label="Customer" value={f.accountId} onChange={(v) => { setF({ ...f, accountId: v }); setAlloc({}); }} required placeholder="Choose a customer"
          options={rowsOf(customers.data).filter((c) => c.accountId).map((c) => ({ value: String(c.accountId), label: `${String(c.billToName)} · ${money(c.balance)}` }))} />
        <SelectField label="Received in (method)" value={f.paymentMethodId} onChange={upd('paymentMethodId')} placeholder="Bank transfer"
          options={rowsOf(methods.data).filter((m) => !['member_account', 'voucher_prepaid', 'loyalty_points'].includes(String(m.methodType)))
            .map((m) => ({ value: String(m.id), label: String(m.name) }))} />
        <TextField label="Amount" value={f.amount} onChange={upd('amount')} inputMode="decimal" required />
        <TextField label="Reference" value={f.reference} onChange={upd('reference')} help="Transfer reference on the bank statement" />
        <TextField label="Payer" value={f.payerName} onChange={upd('payerName')} placeholder={String(customer?.billToName ?? '')} />
      </div>
      <AllocationTable accountId={f.accountId} amount={amount} alloc={alloc} setAlloc={setAlloc} />
      <ErrorAlert error={error} />
    </Modal>
  );
}

/** Allocate the open amount of a received payment to invoices. */
function AllocateModal({ payment, onClose }: { payment: R; onClose: () => void }) {
  const [alloc, setAlloc] = useState<Alloc>({});
  const send = useSend<Record<string, unknown>, R>('POST', `${BILLING}/payment-allocations`, INV);
  const amount = Number(payment.unallocated) || 0;
  const used = Object.values(alloc).reduce((s, v) => s + (Number(v) || 0), 0);
  return (
    <Modal open onClose={onClose} title={`Allocate ${String(payment.number)}`} wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending || used <= 0 || used > amount}
        onClick={() => send.mutate({ paymentId: payment.id, allocations: allocations(alloc) }, { onSuccess: onClose })}>Allocate</button></>}>
      <KV items={[['Customer', String(payment.accountName)], ['Received', formatDateTime(String(payment.paidAt))], ['Unallocated', money(payment.unallocated)]]} />
      <AllocationTable accountId={String(payment.accountId)} amount={amount} alloc={alloc} setAlloc={setAlloc} />
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

export function CreditNoteModal({ onClose }: { onClose: () => void }) {
  const inv = useGet<Page<R>>(`${BILLING}/invoices?limit=200`);
  const [f, setF] = useState({ invoiceId: '', amount: '', reason: '' });
  const send = useSend<Record<string, unknown>, R>('POST', `${BILLING}/credit-notes`, INV);
  const open = rowsOf(inv.data).filter((i) => OPEN.includes(String(i.status)));
  return (
    <Modal open onClose={onClose} title="New Credit Note" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending || !f.invoiceId || !f.amount || !f.reason}
        onClick={() => send.mutate(f, { onSuccess: onClose })}>Issue</button></>}>
      <div className="oc-form">
        <SelectField label="Invoice" value={f.invoiceId} onChange={(v) => setF({ ...f, invoiceId: v })} required placeholder="Choose an open invoice"
          options={open.map((i) => ({ value: String(i.id), label: `${String(i.number)} · ${String(i.billToName)} · ${money(i.outstanding)}` }))} />
        <TextField label="Amount" value={f.amount} onChange={(v) => setF({ ...f, amount: v })} inputMode="decimal" required help="At most the outstanding amount" />
        <TextArea label="Reason" value={f.reason} onChange={(v) => setF({ ...f, reason: v })} rows={2} required span
          help="Invoice correction, event cancelled, overbilling …" />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

/** Control account against its sub-ledger (AR or AP). */
function ControlCheck({ codes }: { codes: string[] }) {
  const r = useGet<R>(`${API}/reconciliation`);
  const checks = items(r.data?.checks).filter((c) => codes.includes(String(c.code)));
  return (
    <Card title="Control account vs sub-ledger" icon="balance">
      <ErrorAlert error={r.error} />
      {!r.data && !r.error && <Skeleton rows={2} />}
      {checks.map((c) => (
        <div key={String(c.code)} className="oc-fin-line">
          <span style={{ display: 'block' }}>{String(c.label)}
            <div className="oc-small oc-muted">GL {money(c.gl)} · sub-ledger {money(c.subledger)}</div></span>
          {c.ok ? <StatusPill status="approved" label="Reconciled" /> : <StatusPill status="rejected" label={`Difference ${money(c.difference)}`} />}
        </div>
      ))}
    </Card>
  );
}

// ── Accounts Receivable ───────────────────────────────────────────────────

const AR_TABS = [{ value: 'receivables', label: 'Receivables' }, { value: 'payments', label: 'Payments' }, { value: 'collections', label: 'Collections' },
  { value: 'reconciliation', label: 'Reconciliation' }, { value: 'aging', label: 'AR Aging' }, { value: 'statements', label: 'Statements' },
  { value: 'ledger', label: 'AR Ledger' }, { value: 'allowance', label: 'Allowance & Write-off' }];
// Invoices and credit notes moved to Revenue & Billing; old links keep the invoice id.
const AR_LEGACY: Record<string, string> = { invoices: '/accounting/revenue?tab=invoices', 'credit-notes': '/accounting/revenue?tab=notes', entries: '/accounting/receivables?tab=ledger' };

function ReceivablesTab() {
  const recv = useGet<Page<R>>(`${API}/receivables`);
  return (
    <DataTable rows={rowsOf(recv.data)} loading={recv.isLoading} error={recv.error} rowKey={(r) => String(r.billToName) + String(r.accountId)} columns={[
      { key: 'billToName', header: 'Customer / company' }, { key: 'openInvoices', header: 'Open invoices', align: 'right' },
      { key: 'invoiced', header: 'Invoiced', align: 'right', render: (r) => money(r.invoiced) }, { key: 'paid', header: 'Paid', align: 'right', render: (r) => money(r.paid) },
      { key: 'credited', header: 'Credited', align: 'right', render: (r) => money(r.credited) }, { key: 'overdue', header: 'Overdue', align: 'right', render: (r) => money(r.overdue) },
      { key: 'balance', header: 'Balance', align: 'right', render: (r) => <strong>{money(r.balance)}</strong> }]} />
  );
}

function ARReconciliationTab({ onAllocate }: { onAllocate: (r: R) => void }) {
  const { can } = useAuth();
  const unallocated = useGet<Page<R>>(`${BILLING}/unallocated-payments`);
  return (
    <div className="oc-fin-grid">
      <Card title="Unallocated receipts" icon="sync">
        <p className="oc-small oc-muted" style={{ marginTop: 0 }}>Payments received on a customer account that are not matched to invoices yet (transfers without an invoice number, advances, overpayments).</p>
        <DataTable rows={rowsOf(unallocated.data)} loading={unallocated.isLoading} error={unallocated.error}
          empty={<Empty title="Every receipt is allocated" icon="task_alt" />}
          columns={[{ key: 'number', header: 'Payment', render: (r) => <>{String(r.number)}<div className="oc-small oc-muted">{formatDateTime(String(r.paidAt))}</div></> },
            { key: 'accountName', header: 'Customer' }, { key: 'unallocated', header: 'Unallocated', align: 'right', render: (r) => <strong>{money(r.unallocated)}</strong> }]}
          actions={(r) => (can('billing.invoice.allocate') ? <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => onAllocate(r)}>Allocate</button> : null)} />
      </Card>
      <div className="oc-stack">
        <ControlCheck codes={['ar_control_vs_open_invoices', 'ar_control_vs_ar_ledger']} />
        <Card title="Bank receipts" icon="account_balance">
          <p className="oc-small oc-muted" style={{ marginTop: 0 }}>Match imported bank statement lines with the payments in the books.</p>
          <Link className="oc-btn oc-btn-sm oc-btn-outline" to="/accounting/cash-bank?tab=reconciliations">Open bank reconciliation</Link>
        </Card>
      </div>
    </div>
  );
}

function AllowanceWriteOffTab() {
  const { can } = useAuth();
  const [asOf, setAsOf] = useState(today());
  return (
    <>
      <ListPage title="Allowance for doubtful accounts" help="Provision per ageing bucket (Accounting Policies, FR-AR-05)." path={`${API}/allowances`} search={false}
        actions={can('accounting.receivable.manage') && <>
          <input className="oc-input oc-filter" type="date" aria-label="As of" value={asOf} onChange={(e) => e.target.value && setAsOf(e.target.value)} />
          <ActionButton label="Run allowance" kind="primary" path={`${API}/allowances`} body={{ asOf }} invalidate={ACC} /></>}
        columns={[{ key: 'number', header: 'Run' }, { key: 'asOf', header: 'As of', render: (r) => dt(r.asOf) },
          { key: 'required', header: 'Required', align: 'right', render: (r) => money(r.required) },
          { key: 'adjustment', header: 'Adjustment', align: 'right', render: (r) => money(r.adjustment) }, { key: 'status', header: 'Status', render: pill('status') }]} />
      <ListPage title="Write-offs" help="Requested from the invoice (Revenue & Billing → Invoices → open an invoice → Write off); each one is approved before it posts."
        path={`${BILLING}/write-offs`} search={false} statuses={['pending', 'approved', 'rejected'].map((v) => ({ value: v, label: label(v) }))}
        columns={[{ key: 'number', header: 'Write-off' }, { key: 'createdAt', header: 'Date', render: (r) => formatDateTime(String(r.createdAt)) },
          { key: 'invoiceNumber', header: 'Invoice', render: (r) => <Link to={`/accounting/revenue?tab=invoices&id=${String(r.invoiceId)}`}>{String(r.invoiceNumber ?? '—')}</Link> },
          { key: 'billToName', header: 'Customer' }, { key: 'reason', header: 'Reason' },
          { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) }, { key: 'status', header: 'Status', render: pill('status') }]} />
    </>
  );
}

/**
 * Accounts Receivable — who still owes the club and how the money comes in:
 * receivables, payments, collections, reconciliation, aging, statements, the
 * AR ledger and allowance & write-off. Invoices are in Revenue & Billing.
 */
export function ReceivablesPage() {
  const { can } = useAuth();
  const [params] = useSearchParams();
  const [tab, setTab] = useTab('receivables');
  const [pay, setPay] = useState(false);
  const [allocate, setAllocate] = useState<R | null>(null);
  const [statement, setStatement] = useState<R | null>(null);
  if (AR_LEGACY[tab]) {
    const id = params.get('id');
    return <Legacy to={`${AR_LEGACY[tab]}${id ? `&id=${id}` : ''}`} />;
  }
  return (
    <div className="oc-stack">
      <PageHeader title="Accounts Receivable" help="Who still owes the club and how the money comes in: payments and their allocation, collections, aging and statements."
        actions={(tab === 'payments' || tab === 'reconciliation') && can('billing.payment.create')
          && <button className="oc-btn oc-btn-primary" onClick={() => setPay(true)}><Icon name="payments" size={18} /> Receive Payment</button>} />
      <Tabs value={tab} onChange={setTab} tabs={AR_TABS} />
      {tab === 'receivables' && <ReceivablesTab />}
      {tab === 'payments' && <ListPage title="Customer Payments" help="Incoming payments on customer accounts; allocate the open part in Reconciliation."
        path={`${BILLING}/payments?filter[purpose]=account_settlement`} statuses={['completed', 'pending', 'cancelled'].map((s) => ({ value: s, label: label(s) }))}
        columns={[{ key: 'number', header: 'Payment' }, { key: 'paidAt', header: 'Received', render: (r) => (r.paidAt ? formatDateTime(String(r.paidAt)) : '—') },
          { key: 'payerName', header: 'Payer', render: (r) => String(r.payerName ?? '—') }, { key: 'methodType', header: 'Method', render: (r) => label(r.methodType) },
          { key: 'reference', header: 'Reference', render: (r) => String(r.reference ?? '—') },
          { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) }, { key: 'status', header: 'Status', render: pill('status') }]} />}
      {tab === 'collections' && <FollowUpWorklistTab party="customer" />}
      {tab === 'reconciliation' && <ARReconciliationTab onAllocate={setAllocate} />}
      {tab === 'aging' && <AgingTab />}
      {tab === 'statements' && <ListPage title="Customer Statements" help="Statement of account per company: invoices, payments and the balance."
        path="/api/v1/crm/corporate-accounts?filter[status]=active" columns={[{ key: 'code', header: 'Code' }, { key: 'name', header: 'Company' },
          { key: 'id', header: '', render: (r) => <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setStatement(r)}>Statement</button> }]} />}
      {tab === 'ledger' && <ListPage title="AR Ledger" path={`${API}/ar-entries`} search={false} columns={[{ key: 'entryDate', header: 'Date', render: (r) => dt(r.entryDate) },
        { key: 'invoiceNumber', header: 'Invoice' }, { key: 'billToName', header: 'Customer' }, { key: 'entryType', header: 'Movement', render: (r) => label(r.entryType) },
        { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) }]} />}
      {tab === 'allowance' && <AllowanceWriteOffTab />}
      {pay && <ReceivePaymentModal onClose={() => setPay(false)} />}
      {allocate && <AllocateModal payment={allocate} onClose={() => setAllocate(null)} />}
      {statement && <StatementDrawer corp={statement} onClose={() => setStatement(null)} />}
    </div>
  );
}

// ── Accounts Payable ──────────────────────────────────────────────────────

function VendorBillModal({ onClose }: { onClose: () => void }) {
  const suppliers = useGet<Page<R>>('/api/v1/procurement/suppliers?limit=200');
  const [f, setF] = useState({ supplierId: '', supplierInvoiceNo: '', invoiceDate: today(), dueDate: '', taxAmount: '', taxInvoiceNo: '', withholdingTaxCode: '' });
  const [line, setLine] = useState({ accountId: '', description: '', amount: '', costCenter: '' });
  const send = useSend<Record<string, unknown>, R>('POST', `${API}/vendor-bills`, ACC);
  const upd = (k: keyof typeof f) => (v: string) => setF({ ...f, [k]: v });
  return (
    <Modal open onClose={onClose} title="Service Vendor Bill" wide actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending || !f.supplierId || !line.accountId}
        onClick={() => send.mutate({ ...Object.fromEntries(Object.entries(f).filter(([, v]) => v !== '')),
          lines: [{ accountId: line.accountId, description: line.description, amount: line.amount, costCenter: line.costCenter || undefined }] }, { onSuccess: onClose })}>Record</button></>}>
      <div className="oc-form">
        <SelectField label="Supplier" value={f.supplierId} onChange={upd('supplierId')} required options={rowsOf(suppliers.data).map((s) => ({ value: s.id, label: String(s.name) }))} />
        <TextField label="Supplier invoice no." value={f.supplierInvoiceNo} onChange={upd('supplierInvoiceNo')} required />
        <TextField label="Invoice date" type="date" value={f.invoiceDate} onChange={upd('invoiceDate')} />
        <TextField label="Due date" type="date" value={f.dueDate} onChange={upd('dueDate')} />
        <TextField label="PPN input" value={f.taxAmount} onChange={upd('taxAmount')} />
        <TextField label="Tax invoice no." value={f.taxInvoiceNo} onChange={upd('taxInvoiceNo')} />
        <SelectField label="Withholding (PPh)" value={f.withholdingTaxCode} onChange={upd('withholdingTaxCode')} placeholder="None"
          options={[{ value: 'PPH23', label: 'PPh 23' }, { value: 'PPH42', label: 'PPh 4(2)' }]} />
        <AccountSelect label="Expense account" value={line.accountId} onChange={(v) => setLine({ ...line, accountId: v })} required />
        <TextField label="Description" value={line.description} onChange={(v) => setLine({ ...line, description: v })} required />
        <TextField label="Amount (DPP)" value={line.amount} onChange={(v) => setLine({ ...line, amount: v })} required />
        <TextField label="Cost center" value={line.costCenter} onChange={(v) => setLine({ ...line, costCenter: v })} />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function PaymentRunModal({ onClose }: { onClose: () => void }) {
  const [paymentDate, setPaymentDate] = useState(today());
  const [bank, setBank] = useState('');
  const [method, setMethod] = useState('transfer');
  const [dueBy, setDueBy] = useState(today());
  const send = useSend<Record<string, unknown>, R>('POST', `${API}/payment-runs`, ACC);
  return (
    <Modal open onClose={onClose} title="New Payment Run" actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!bank || send.isPending}
        onClick={() => send.mutate({ paymentDate, bankAccountId: bank, method, dueBy }, { onSuccess: onClose })}>Create</button></>}>
      <div className="oc-form">
        <TextField label="Payment date" type="date" value={paymentDate} onChange={setPaymentDate} />
        <BankSelect label="Paid from" value={bank} onChange={setBank} />
        <SelectField label="Method" value={method} onChange={setMethod} options={['transfer', 'cheque', 'cash'].map((m) => ({ value: m, label: label(m) }))} />
        <TextField label="Payables due by" type="date" value={dueBy} onChange={setDueBy} help="Every open payable due by this date" />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

const payableCols: Column<R>[] = [{ key: 'number', header: 'Document' }, { key: 'supplierName', header: 'Supplier' }, { key: 'itemType', header: 'Kind', render: (r) => label(r.itemType) },
  { key: 'dueDate', header: 'Due', render: (r) => dt(r.dueDate) }, { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) },
  { key: 'outstanding', header: 'Outstanding', align: 'right', render: (r) => money(r.outstanding) }, { key: 'status', header: 'Status', render: pill('status') }];

// Accounts Payable: each part is a sidebar item (no tab bar).
const AP_TABS: SidebarTab[] = [{ value: 'bills', label: 'Bills', own: true },
  { value: 'payments', label: 'Payments', help: 'Payment runs with approval and the payments made to suppliers.' }, { value: 'notes', label: 'Debit/Credit Notes', own: true },
  { value: 'follow-up', label: 'Vendor Follow-up', help: 'Follow-up with suppliers on open bills and disputes.' },
  { value: 'reconciliation', label: 'Reconciliation', help: 'The AP control account against the open payables, and the approved runs still to execute.' },
  { value: 'aging', label: 'AP Aging', help: 'What the club owes per supplier and age bucket.' },
  { value: 'statements', label: 'Vendor Statements', help: 'Bills, notes and payments of a supplier with the open balance.' }];
const AP_LEGACY: Record<string, string> = { payables: '/accounting/payables?tab=bills', runs: '/accounting/payables?tab=payments' };

/** Vendor statement: the supplier's bills, notes and payments with the open balance. */
function VendorStatementTab() {
  const suppliers = useGet<Page<R>>('/api/v1/procurement/suppliers?limit=200');
  const [supplier, setSupplier] = useState('');
  const bills = useGet<Page<R>>(supplier ? `${API}/payables?filter[supplierId]=${supplier}&limit=200` : null);
  const paid = useGet<Page<R>>(supplier ? `${API}/vendor-payments?supplierId=${supplier}&limit=200` : null);
  const open = rowsOf(bills.data).reduce((s, b) => s + Number(b.outstanding ?? 0), 0);
  const billed = rowsOf(bills.data).reduce((s, b) => s + Number(b.amount ?? 0), 0);
  const payments = rowsOf(paid.data).reduce((s, p) => s + Number(p.amount ?? 0), 0);
  return (
    <div className="oc-stack">
      <div style={{ maxWidth: 420 }}>
        <SelectField label="Supplier" value={supplier} onChange={setSupplier} placeholder="Choose a supplier"
          options={rowsOf(suppliers.data).map((s) => ({ value: String(s.id), label: String(s.name) }))} />
      </div>
      {!supplier && <Card><Empty title="Choose a supplier" help="The statement lists its bills, debit notes and payments with the open balance." icon="description" /></Card>}
      {supplier && (
        <>
          <div className="oc-stat-grid">
            <StatTile label="Billed" icon="receipt_long" value={money(billed)} />
            <StatTile label="Paid" icon="payments" value={money(payments)} />
            <StatTile label="Open balance" icon="account_balance_wallet" value={money(open)} />
          </div>
          <Card title="Bills & notes"><DataTable rows={rowsOf(bills.data)} loading={bills.isLoading} error={bills.error} columns={payableCols} /></Card>
          <Card title="Payments"><DataTable rows={rowsOf(paid.data)} loading={paid.isLoading} error={paid.error} columns={[{ key: 'number', header: 'Payment' },
            { key: 'paidDate', header: 'Paid', render: (r) => dt(r.paidDate) }, { key: 'method', header: 'Method', render: (r) => label(r.method) },
            { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) }]} /></Card>
        </>
      )}
    </div>
  );
}

/**
 * Accounts Payable — who the club has to pay: bills, payments and payment
 * runs, debit/credit notes, vendor follow-up, reconciliation, AP aging and
 * vendor statements (EP-19).
 */
export function PayablesPage() {
  const { can } = useAuth();
  const [tab] = useTab('bills');
  const [modal, setModal] = useState<'' | 'bill' | 'run'>('');
  const pendingRuns = useGet<Page<R>>(tab === 'reconciliation' ? `${API}/payment-runs?filter[status]=approved&limit=50` : null);
  if (AP_LEGACY[tab]) return <Legacy to={AP_LEGACY[tab]} />;
  return (
    <div className="oc-stack">
      <SidebarTabs tabs={AP_TABS} tab={tab}
        actions={tab === 'payments' && can('accounting.payment_run.create') && <button className="oc-btn oc-btn-primary" onClick={() => setModal('run')}>New Payment Run</button>} />
      {tab === 'bills' && <ListPage title="Bills" help="Matched vendor invoices (procurement) and service bills, with what is still to pay."
        actions={can('accounting.payable.manage') && <button className="oc-btn oc-btn-primary" onClick={() => setModal('bill')}>Service Bill</button>}
        path={`${API}/payables`} statuses={['open', 'partially_paid', 'paid'].map((s) => ({ value: s, label: label(s) }))} columns={payableCols} />}
      {tab === 'payments' && (
        <>
          <ListPage title="Payment Runs" path={`${API}/payment-runs`} search={false}
            statuses={['draft', 'pending_approval', 'approved', 'executed', 'cancelled'].map((s) => ({ value: s, label: label(s) }))}
            columns={[{ key: 'number', header: 'Run' }, { key: 'paymentDate', header: 'Payment date', render: (r) => dt(r.paymentDate) },
              { key: 'bankAccountName', header: 'Bank' }, { key: 'method', header: 'Method', render: (r) => label(r.method) },
              { key: 'total', header: 'Total', align: 'right', render: (r) => money(r.total) }, { key: 'status', header: 'Status', render: pill('status') }]}
            rowActions={(r) => (
              <div className="oc-row">
                {r.status === 'draft' && can('accounting.payment_run.create') && <ActionButton label="Submit" path={`${API}/payment-runs/${r.id}:submit`} invalidate={ACC} />}
                {r.status === 'approved' && can('accounting.payment_run.execute') && <ActionButton label="Execute" kind="primary" path={`${API}/payment-runs/${r.id}:execute`} invalidate={ACC}
                  confirm="Pay the payables of this run and post the payment journal?" />}
                {['draft', 'pending_approval', 'approved'].includes(String(r.status)) && <ActionButton label="Cancel" path={`${API}/payment-runs/${r.id}:cancel`} invalidate={ACC} reason="required" />}
                <a className="oc-btn oc-btn-sm oc-btn-text" href={`${API}/payment-runs/${r.id}/bank-file`}>Bank file</a>
              </div>)} />
          <ListPage title="Vendor Payments" path={`${API}/vendor-payments`} columns={[{ key: 'number', header: 'Payment' }, { key: 'supplierName', header: 'Supplier' },
            { key: 'paidDate', header: 'Paid', render: (r) => dt(r.paidDate) }, { key: 'method', header: 'Method', render: (r) => label(r.method) },
            { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) }]} />
        </>
      )}
      {tab === 'notes' && <ListPage title="Debit / Credit Notes" help="Debit notes to suppliers (purchase returns, price differences) that reduce the payable."
        path="/api/v1/procurement/debit-notes" search={false} statuses={['issued', 'applied'].map((s) => ({ value: s, label: label(s) }))}
        columns={[{ key: 'number', header: 'Note' }, { key: 'issueDate', header: 'Date', render: (r) => dt(r.issueDate) }, { key: 'supplierName', header: 'Supplier' },
          { key: 'reason', header: 'Reason' }, { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) }, { key: 'status', header: 'Status', render: pill('status') }]} />}
      {tab === 'follow-up' && <FollowUpWorklistTab party="supplier" />}
      {tab === 'reconciliation' && (
        <div className="oc-fin-grid">
          <ControlCheck codes={['ap_control_vs_payables']} />
          <Card title="Approved runs to execute" icon="pending_actions">
            <DataTable rows={rowsOf(pendingRuns.data)} loading={pendingRuns.isLoading} error={pendingRuns.error} empty={<Empty title="No approved run waiting" icon="task_alt" />}
              columns={[{ key: 'number', header: 'Run' }, { key: 'paymentDate', header: 'Payment date', render: (r) => dt(r.paymentDate) },
                { key: 'total', header: 'Total', align: 'right', render: (r) => money(r.total) }]} />
            <Link className="oc-btn oc-btn-sm oc-btn-outline" style={{ marginTop: 12 }} to="/accounting/cash-bank?tab=reconciliations">Open bank reconciliation</Link>
          </Card>
        </div>
      )}
      {tab === 'aging' && <AgingTab ap />}
      {tab === 'statements' && <VendorStatementTab />}
      {modal === 'bill' && <VendorBillModal onClose={() => setModal('')} />}
      {modal === 'run' && <PaymentRunModal onClose={() => setModal('')} />}
    </div>
  );
}
