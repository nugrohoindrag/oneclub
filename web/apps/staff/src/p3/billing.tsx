import React, { useEffect, useRef, useState } from 'react';
import { Link, useSearchParams } from 'react-router';
import { qs, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate, formatDateTime } from '@oneclub/i18n';
import {
  Card, Checkbox, DataTable, Drawer, Empty, ErrorAlert, Icon, Modal, MoneyField, PageHeader, SelectField, Skeleton, StatusPill, TextArea, TextField,
  useAuth, useDebounced, useToast, type StatusTone,
} from '@oneclub/shell';
import { ActionButton, KV, ListPage, Tabs, money, today, type R } from '../p1/common';
import { ImportCorporateAR } from './ar_import';
import { InvoiceDocuments, InvoiceWorkspacePage, invoiceWorkspaceUrl } from './invoice-workspace';

// Billing & Payment P3 (PRD P3 EP-17/18, Naming Convention §18): Invoices,
// Corporate Billing, Payment Schedules, Customer Folios, Cashier and Night
// Audit with Business Day.

const BILLING = ['/api/v1/billing'];
const pill = (k: string) => (r: R) => <StatusPill status={String(r[k])} />;
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const METHODS = [
  { value: 'cash', label: 'Cash' }, { value: 'bank_transfer', label: 'Bank Transfer' }, { value: 'virtual_account', label: 'Virtual Account' },
  { value: 'qris', label: 'QRIS' }, { value: 'card', label: 'Card' },
];

// ── Invoices ──────────────────────────────────────────────────────────────

/** e-Faktur (faktur pajak keluaran) of the invoices from Accounting (PRD P4 §7.1); empty when Accounting is off. */
function useEFaktur(invoiceId?: string) {
  const { data } = useGet<Page<R>>(`/api/v1/accounting/invoice-efaktur${qs({ 'filter[invoiceId]': invoiceId })}`, { retry: false });
  return new Map((data?.items ?? []).map((e) => [String(e.invoiceId), e]));
}

/** "010.000-26.00000001" with its status, or the status alone before the upload. */
function EFaktur({ e }: { e?: R }) {
  if (!e) return <span className="oc-muted">—</span>;
  return (
    <span className="oc-row" style={{ gap: 6 }}>
      {e.fakturNumber ? <span>{String(e.fakturNumber)}</span> : null}
      <StatusPill status={String(e.status)} label={`e-Faktur ${label(e.status)}`} />
    </span>
  );
}

const SOURCE_LABEL: Record<string, string> = {
  manual: 'Manual', folio: 'Folio', customer_folio: 'Customer folio', payment_schedule: 'Payment schedule', city_ledger: 'City ledger',
};

/** The source on one line ("Banquet event" for a folio) with its reference under it. */
function InvoiceSource({ r }: { r: R }) {
  const s = String(r.source ?? '');
  const name = s === 'folio' && r.sourceType ? label(r.sourceType) : SOURCE_LABEL[s] ?? label(s);
  return (
    <span className="oc-nowrap">
      {name.charAt(0).toUpperCase() + name.slice(1)}
      {r.sourceRef ? <div className="oc-small oc-muted">{String(r.sourceRef)}</div> : null}
    </span>
  );
}

const INVOICE_STATUS: Record<string, [string, StatusTone]> = {
  draft: ['Draft', 'neutral'], issued: ['Issued', 'info'], partially_paid: ['Partially paid', 'warning'], paid: ['Paid', 'success'],
  overdue: ['Overdue', 'error'], void: ['Void', 'neutral'],
};

/** Status of an invoice (the generic "issued" pill reads "Active", which is the voucher's). */
export function InvoiceStatus({ status }: { status: unknown }) {
  const s = String(status ?? '');
  const [l, tone] = INVOICE_STATUS[s] ?? [label(s), 'neutral' as StatusTone];
  return <StatusPill status={s} label={l} tone={tone} />;
}
const invoiceStatus = (r: R) => <InvoiceStatus status={r.status} />;
const day = (v: unknown) => (v ? <span className="oc-nowrap">{formatDate(String(v))}</span> : '—');

const FILTER_SOURCES = [{ value: '', label: 'All sources' }, ...Object.entries(SOURCE_LABEL).map(([value, l]) => ({ value, label: l }))];
const NO_FILTERS = { source: '', payer: '', from: '', to: '', dueFrom: '', dueTo: '', minTotal: '', maxTotal: '' };
type InvoiceFilters = typeof NO_FILTERS;

/** "Filters" button with the advanced invoice filters in a panel (FR list filters of Invoice Management §21). */
function InvoiceFilterPanel({ value, onChange }: { value: InvoiceFilters; onChange: (f: InvoiceFilters) => void }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const out = (e: MouseEvent) => { if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false); };
    const esc = (e: KeyboardEvent) => e.key === 'Escape' && setOpen(false);
    document.addEventListener('mousedown', out);
    document.addEventListener('keydown', esc);
    return () => { document.removeEventListener('mousedown', out); document.removeEventListener('keydown', esc); };
  }, [open]);
  const set = (k: keyof InvoiceFilters) => (v: string) => onChange({ ...value, [k]: v });
  // A range counts once.
  const active = [value.source, value.payer, value.from || value.to, value.dueFrom || value.dueTo, value.minTotal || value.maxTotal].filter(Boolean).length;
  return (
    <div className="oc-popover-anchor" ref={ref}>
      <button type="button" className="oc-btn oc-btn-outline oc-filter-btn" aria-expanded={open} aria-haspopup="dialog" onClick={() => setOpen((o) => !o)}>
        <Icon name="tune" size={18} /> Filters{active > 0 && <span className="oc-filter-count">{active}</span>}
      </button>
      {open && (
        <div className="oc-popover oc-filter-panel" role="dialog" aria-label="Invoice filters">
          <div className="oc-form oc-filter-grid">
            <SelectField label="Source" value={value.source} onChange={set('source')} options={FILTER_SOURCES} />
            <SelectField label="Customer type" value={value.payer} onChange={set('payer')}
              options={[{ value: '', label: 'Corporate & individual' }, { value: 'corporate', label: 'Corporate' }, { value: 'individual', label: 'Individual' }]} />
            <TextField label="Invoice date from" type="date" value={value.from} max={value.to || undefined} onChange={set('from')} />
            <TextField label="to" type="date" value={value.to} min={value.from || undefined} onChange={set('to')} />
            <TextField label="Due date from" type="date" value={value.dueFrom} max={value.dueTo || undefined} onChange={set('dueFrom')} />
            <TextField label="to" type="date" value={value.dueTo} min={value.dueFrom || undefined} onChange={set('dueTo')} />
            <MoneyField label="Amount from" value={value.minTotal} onChange={set('minTotal')} placeholder="Rp" />
            <TextField label="to" type="number" inputMode="decimal" value={value.maxTotal} onChange={set('maxTotal')} placeholder="Rp" />
          </div>
          <div className="oc-row" style={{ marginTop: 12 }}>
            <button type="button" className="oc-btn oc-btn-text oc-btn-sm" disabled={!active} onClick={() => onChange(NO_FILTERS)}>
              <Icon name="restart_alt" size={18} /> Reset</button>
            <span className="oc-spacer" />
            <button type="button" className="oc-btn oc-btn-primary oc-btn-sm" onClick={() => setOpen(false)}>Done</button>
          </div>
        </div>
      )}
    </div>
  );
}

export function InvoicesPage() {
  const [params, setParams] = useSearchParams();
  const { can } = useAuth();
  const [importing, setImporting] = useState(false);
  const [f, setF] = useState(NO_FILTERS);
  const minTotal = useDebounced(f.minTotal);
  const maxTotal = useDebounced(f.maxTotal);
  const efaktur = useEFaktur();
  const open = params.get('id');
  // Keeps the other parameters (?tab= when the list sits in Accounts Receivable).
  const openId = (id: string | null) => setParams((p) => { const n = new URLSearchParams(p); if (id) n.set('id', id); else n.delete('id'); return n; });
  return (
    <>
      <ListPage title="Invoices" help="Invoices generated from folios, customer folios, the corporate city ledger and payment schedules, and manual invoices for exceptions."
        path="/api/v1/billing/invoices"
        statuses={['draft', 'issued', 'partially_paid', 'paid', 'overdue', 'void'].map((s) => ({ value: s, label: label(s) }))}
        extraQuery={{ 'filter[source]': f.source, 'filter[payer]': f.payer, from: f.from, to: f.to, dueFrom: f.dueFrom, dueTo: f.dueTo, minTotal, maxTotal }}
        filters={<InvoiceFilterPanel value={f} onChange={setF} />}
        actions={<>
          {can('billing.invoice.import') && <button className="oc-btn oc-btn-neutral" onClick={() => setImporting(true)}>Import Corporate AR</button>}
          {can('billing.invoice.create') && <Link className="oc-btn oc-btn-primary" to={invoiceWorkspaceUrl()}>Create Invoice</Link>}
        </>}
        onRowClick={(r) => openId(String(r.id))}
        columns={[{ key: 'number', header: 'Invoice', render: (r) => String(r.number ?? 'Draft') }, { key: 'billToName', header: 'Customer' },
          { key: 'source', header: 'Source', render: (r) => <InvoiceSource r={r} /> },
          { key: 'issueDate', header: 'Invoice date', render: (r) => day(r.issueDate) }, { key: 'dueDate', header: 'Due', render: (r) => day(r.dueDate) },
          { key: 'total', header: 'Amount', align: 'right', render: (r) => money(r.total) }, { key: 'paidAmount', header: 'Paid', align: 'right', render: (r) => money(r.paidAmount) },
          { key: 'outstanding', header: 'Outstanding', align: 'right', render: (r) => money(r.outstanding) },
          { key: 'status', header: 'Status', render: invoiceStatus }, { key: 'efaktur', header: 'Tax status', render: (r) => <EFaktur e={efaktur.get(String(r.id))} /> }]} />
      {importing && <ImportCorporateAR onClose={() => setImporting(false)} />}
      {open && <InvoiceDrawer id={open} onClose={() => openId(null)} />}
    </>
  );
}

function InvoiceDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const toast = useToast();
  const d = useGet<R & { lines: R[]; allocations: R[]; creditNotes: R[]; writeOffs: R[]; internal?: { notes?: string | null; attachments: R[] } }>(
    `/api/v1/billing/invoices/${id}`);
  const ef = useEFaktur(id).get(id);
  const [modal, setModal] = useState<'' | 'pay' | 'credit' | 'writeoff' | 'send'>('');
  const x = d.data;
  const inv = [...BILLING, `/api/v1/billing/invoices/${id}`];
  const openInv = x && !['draft', 'void', 'paid'].includes(String(x.status));
  const discount = (x?.lines ?? []).reduce((a, l) => a + Number(l.discountAmount ?? 0), 0);
  const opt = (k: string, v: unknown): [string, React.ReactNode][] => (v ? [[k, String(v)]] : []);
  return (
    <Drawer open onClose={onClose} title={x ? `Invoice ${String(x.number ?? 'Draft')}` : 'Invoice'}>
      {d.isLoading && <Skeleton />}
      <ErrorAlert error={d.error} />
      {x && (
        <div className="oc-stack">
          <KV items={[['Bill to', String(x.billToName)], ['NPWP', String(x.billToNpwp ?? '—')], ...opt('Address', x.billToAddress),
            ['Status', <InvoiceStatus key="s" status={x.status} />], ['Source', <InvoiceSource key="src" r={x} />],
            ...opt('Customer PO', x.customerPo), ...opt('Contract', x.contractRef), ...opt('Billing reference', x.billingRef),
            ['Invoice date', x.issueDate ? formatDate(String(x.issueDate)) : '—'], ['Due date', x.dueDate ? formatDate(String(x.dueDate)) : '—'],
            ['Sent', x.sentAt ? formatDateTime(String(x.sentAt)) : 'Not sent'],
            ...(discount ? [['Discount', money(discount)] as [string, React.ReactNode]] : []),
            ['Subtotal', money(x.subtotal)], ['Service', money(x.serviceAmount)], ['Tax', money(x.taxAmount)], ['Total', <strong key="t">{money(x.total)}</strong>],
            ['Paid', money(x.paidAmount)], ['Credited', money(x.creditedAmount)], ['Written off', money(x.writtenOffAmount)],
            ['Outstanding', <strong key="o">{money(x.outstanding)}</strong>],
            ['e-Faktur', <EFaktur key="ef" e={ef} />], ...(ef ? [['Tax period', String(ef.taxPeriod)] as [string, React.ReactNode]] : []),
            ...opt('Customer notes', x.notes)]} />
          <div className="oc-row-wrap">
            {x.status === 'draft' && can('billing.invoice.issue') && <ActionButton label="Issue" kind="primary" path={`/api/v1/billing/invoices/${id}:issue`} invalidate={inv} />}
            {openInv && can('billing.invoice.issue') && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setModal('send')}>{x.sentAt ? 'Resend' : 'Send'}</button>}
            {openInv && can('billing.payment.create') && <button className="oc-btn oc-btn-sm oc-btn-primary" onClick={() => setModal('pay')}>Take Payment</button>}
            {openInv && can('billing.invoice.credit') && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setModal('credit')}>Credit Note</button>}
            {openInv && can('billing.invoice.write_off') && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setModal('writeoff')}>Write Off</button>}
            {x.status !== 'void' && x.status !== 'paid' && can('billing.invoice.void') && Number(x.paidAmount) === 0 &&
              <ActionButton label="Void" path={`/api/v1/billing/invoices/${id}:void`} invalidate={inv} reason="required" danger />}
            {x.number != null && <a className="oc-btn oc-btn-sm oc-btn-text" href={`/api/v1/billing/invoices/${id}/pdf`} target="_blank" rel="noreferrer">PDF</a>}
            {typeof x.payLink === 'string' && <button className="oc-btn oc-btn-sm oc-btn-text"
              onClick={() => { void navigator.clipboard?.writeText(String(x.payLink)); toast('Customer link copied'); }}>Copy customer link</button>}
          </div>
          <Card title="Lines" icon="receipt_long">
            <DataTable rows={x.lines} pageSize={0} columns={[{ key: 'description', header: 'Description' }, { key: 'businessLine', header: 'Line', render: (l) => label(l.businessLine) },
              { key: 'quantity', header: 'Qty', align: 'right', render: (l) => `${String(l.quantity)}${l.unit ? ` ${String(l.unit)}` : ''}` },
              { key: 'unitPrice', header: 'Unit price', align: 'right', render: (l) => money(l.unitPrice) },
              { key: 'discountAmount', header: 'Discount', align: 'right', render: (l) => (Number(l.discountAmount) ? money(l.discountAmount) : '—') },
              { key: 'total', header: 'Total', align: 'right', render: (l) => money(l.total) }]} />
          </Card>
          {x.internal && <InvoiceInternalCard id={id} internal={x.internal} invalidate={inv} editable={can('billing.invoice.create')} />}
          {x.allocations.length > 0 && <Card title="Payments" icon="payments">
            <DataTable rows={x.allocations} columns={[{ key: 'paymentNumber', header: 'Payment' }, { key: 'amount', header: 'Amount', align: 'right', render: (a) => money(a.amount) },
              { key: 'createdAt', header: 'Allocated', render: (a) => formatDateTime(String(a.createdAt)) }]} />
          </Card>}
          {(x.creditNotes.length > 0 || x.writeOffs.length > 0) && <Card title="Credit Notes & Write-offs" icon="money_off">
            <DataTable rows={[...x.creditNotes.map((c) => ({ ...c, kind: 'Credit note' })), ...x.writeOffs.map((w) => ({ ...w, kind: 'Write-off' }))] as R[]}
              columns={[{ key: 'number', header: 'Number' }, { key: 'kind', header: 'Kind' }, { key: 'amount', header: 'Amount', align: 'right', render: (c) => money(c.amount) },
                { key: 'reason', header: 'Reason' }, { key: 'status', header: 'Status', render: pill('status') }]} />
          </Card>}
          {modal === 'pay' && <AmountModal title="Take Payment" path={`/api/v1/billing/invoices/${id}:pay`} invalidate={inv} onClose={() => setModal('')}
            defaultAmount={String(x.outstanding)} method />}
          {modal === 'credit' && <AmountModal title="Credit Note" path="/api/v1/billing/credit-notes" invalidate={inv} onClose={() => setModal('')}
            extra={{ invoiceId: id }} reason idempotent />}
          {modal === 'writeoff' && <AmountModal title="Write Off" path={`/api/v1/billing/invoices/${id}:write-off`} invalidate={inv} onClose={() => setModal('')}
            defaultAmount={String(x.outstanding)} reason help="Above the approval threshold the write-off waits for the Finance Manager." />}
          {modal === 'send' && <SendModal path={`/api/v1/billing/invoices/${id}:send`} email={String(x.billToEmail ?? '')} invalidate={inv} onClose={() => setModal('')} />}
        </div>
      )}
    </Drawer>
  );
}

/** Internal notes and supporting documents of an invoice (staff only, never on the customer invoice). */
function InvoiceInternalCard({ id, internal, invalidate, editable }: {
  id: string; internal: { notes?: string | null; attachments: R[] }; invalidate: string[]; editable: boolean;
}) {
  const toast = useToast();
  const [notes, setNotes] = useState(String(internal.notes ?? ''));
  const docs = internal.attachments.map((a) => ({ id: String(a.fileId), filename: String(a.filename) }));
  const save = useSend<Record<string, unknown>>('PATCH', `/api/v1/billing/invoices/${id}/internal`, invalidate);
  return (
    <Card title="Internal" icon="lock">
      {docs.length > 0 && (
        <div className="oc-row-wrap" style={{ marginBottom: 12 }}>
          {docs.map((f) => (
            <a key={f.id} className="oc-chip" href={`/api/v1/billing/invoices/${id}/attachments/${f.id}`} target="_blank" rel="noreferrer">
              <Icon name="description" size={16} /> {f.filename}</a>
          ))}
        </div>
      )}
      {editable ? (
        <>
          <TextArea label="Internal notes" value={notes} onChange={setNotes} help="Never shown to the customer" />
          <div className="oc-row-wrap" style={{ marginTop: 8 }}>
            <button className="oc-btn oc-btn-sm oc-btn-neutral" disabled={save.isPending || notes === String(internal.notes ?? '')}
              onClick={() => save.mutate({ notes }, { onSuccess: () => toast('Internal notes saved') })}>Save notes</button>
          </div>
          <InvoiceDocuments docs={docs} onChange={(next) => save.mutate({ attachmentFileIds: next.map((f) => f.id) })} />
          <ErrorAlert error={save.error} />
        </>
      ) : <p style={{ margin: 0 }}>{internal.notes ? String(internal.notes) : <span className="oc-muted">No internal notes</span>}</p>}
    </Card>
  );
}

/** Amount (+ method / reason) form posting to an action. */
function AmountModal({ title, path, invalidate, onClose, defaultAmount = '', method, reason, extra, help, idempotent }: {
  title: string; path: string; invalidate: string[]; onClose: () => void; defaultAmount?: string; method?: boolean; reason?: boolean; extra?: R | Record<string, unknown>;
  help?: string; idempotent?: boolean;
}) {
  const toast = useToast();
  const [amount, setAmount] = useState(defaultAmount);
  const [methodType, setMethod] = useState('bank_transfer');
  const [why, setWhy] = useState('');
  const [reference, setReference] = useState('');
  const send = useSend<Record<string, unknown>>('POST', path, invalidate, idempotent ? () => ({ 'Idempotency-Key': crypto.randomUUID() }) : undefined);
  return (
    <Modal open onClose={onClose} title={title} actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending || (reason && !why)}
        onClick={() => send.mutate({ ...(extra ?? {}), amount: amount || undefined, ...(method ? { methodType, reference: reference || undefined } : {}), ...(reason ? { reason: why } : {}) },
          { onSuccess: () => { toast(`${title}: done`); onClose(); } })}>{title}</button></>}>
      <div className="oc-form">
        <MoneyField label="Amount" value={amount} onChange={setAmount} help={help} />
        {method && <SelectField label="Method" value={methodType} onChange={setMethod} options={METHODS} />}
        {method && <TextField label="Reference" value={reference} onChange={setReference} />}
        {reason && <TextArea label="Reason" value={why} onChange={setWhy} required />}
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function SendModal({ path, email, invalidate, onClose }: { path: string; email: string; invalidate: string[]; onClose: () => void }) {
  const toast = useToast();
  const [to, setTo] = useState(email);
  const send = useSend<Record<string, unknown>>('POST', path, invalidate);
  return (
    <Modal open onClose={onClose} title="Send" actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => send.mutate({ email: to || undefined }, { onSuccess: () => { toast('Sent'); onClose(); } })}>Send</button></>}>
      <div className="oc-form"><TextField label="E-mail" type="email" value={to} onChange={setTo} /></div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

// ── Corporate Billing ─────────────────────────────────────────────────────

export function CorporateBillingPage() {
  const [asOf, setAsOf] = useState(today());
  const [open, setOpen] = useState<R | null>(null);
  const aging = useGet<{ asOf: string; rows: R[]; totals: R }>(`/api/v1/billing/aging${qs({ asOf })}`);
  const corporates = useGet<Page<R>>('/api/v1/crm/corporate-accounts?limit=200');
  const names = new Map((corporates.data?.items ?? []).map((c) => [c.id, c]));
  const ageCols = [{ key: 'days0to30', header: '0–30' }, { key: 'days31to60', header: '31–60' }, { key: 'days61to90', header: '61–90' }, { key: 'over90', header: '> 90' },
    { key: 'total', header: 'Total' }, { key: 'overdue', header: 'Overdue' }].map((c) => ({ ...c, align: 'right' as const, render: (r: R) => money(r[c.key]) }));
  return (
    <div className="oc-stack">
      <PageHeader title="Corporate Billing" help="City ledger of corporate accounts: credit limit, terms, statements and operational ageing (FR-BIL-P3-05)." />
      <div style={{ width: 200 }}><TextField label="Ageing as of" type="date" value={asOf} onChange={setAsOf} /></div>
      <Card title="Accounts Receivable Ageing" icon="hourglass_bottom">
        <DataTable rows={aging.data?.rows} loading={aging.isLoading} error={aging.error} rowKey={(r) => String(r.accountId ?? r.customerId ?? r.billToName)}
          onRowClick={(r) => r.corporateAccountId && setOpen(names.get(String(r.corporateAccountId)) ?? { id: String(r.corporateAccountId), name: r.billToName } as R)}
          columns={[{ key: 'billToName', header: 'Bill To' }, { key: 'invoices', header: 'Invoices', align: 'right' }, ...ageCols]} />
        {aging.data && <div className="oc-row oc-small" style={{ marginTop: 8 }}><span className="oc-muted">Total open</span><span className="oc-spacer" />
          <strong>{money(aging.data.totals.total)}</strong><span className="oc-muted">· overdue</span><strong>{money(aging.data.totals.overdue)}</strong></div>}
      </Card>
      <Card title="Corporate Accounts" icon="apartment">
        <DataTable rows={corporates.data?.items} loading={corporates.isLoading} onRowClick={(r) => setOpen(r)}
          columns={[{ key: 'code', header: 'Code' }, { key: 'name', header: 'Company' }, { key: 'npwp', header: 'NPWP' }, { key: 'email', header: 'E-mail' },
            { key: 'status', header: 'Status', render: pill('status') }]} />
      </Card>
      {open && <StatementDrawer corp={open} onClose={() => setOpen(null)} />}
    </div>
  );
}

export function StatementDrawer({ corp, onClose }: { corp: R; onClose: () => void }) {
  const { can } = useAuth();
  const t = today();
  const [from, setFrom] = useState(`${t.slice(0, 8)}01`);
  const [to, setTo] = useState(t);
  const [modal, setModal] = useState<'' | 'send' | 'override'>('');
  const st = useGet<R & { account: R; entries: R[]; openInvoices: R[]; aging: { totals: R } }>(`/api/v1/billing/corporate-accounts/${corp.id}/statement${qs({ from, to })}`);
  const x = st.data;
  return (
    <Drawer open onClose={onClose} title={`Statement · ${String(corp.name)}`}>
      <div className="oc-row-wrap"><TextField label="From" type="date" value={from} onChange={setFrom} /><TextField label="To" type="date" value={to} onChange={setTo} /></div>
      {st.isLoading && <Skeleton />}
      {st.error && <Empty title="No billing account yet" help="The city ledger opens with the first charge to this company." icon="account_balance" />}
      {x && (
        <div className="oc-stack">
          <KV items={[['Account', String(x.account.number)], ['Credit limit', money(x.account.creditLimit)], ['Opening balance', money(x.openingBalance)],
            ['Charges', money(x.charges)], ['Payments', money(x.payments)], ['Closing balance', <strong key="c">{money(x.closingBalance)}</strong>],
            ['Overdue', money(x.aging.totals.overdue)]]} />
          <div className="oc-row-wrap">
            {can('billing.invoice.issue') && <button className="oc-btn oc-btn-sm oc-btn-primary" onClick={() => setModal('send')}>Send Statement</button>}
            {can('billing.credit_override.request') && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setModal('override')}>Request Credit Override</button>}
          </div>
          <Card title="Open Invoices" icon="receipt_long">
            <DataTable rows={x.openInvoices} columns={[{ key: 'number', header: 'Invoice' }, { key: 'dueDate', header: 'Due', render: (r) => formatDate(String(r.dueDate)) },
              { key: 'outstanding', header: 'Outstanding', align: 'right', render: (r) => money(r.outstanding) }, { key: 'status', header: 'Status', render: invoiceStatus }]} />
          </Card>
          <Card title="Movements" icon="swap_vert">
            <DataTable rows={x.entries} rowKey={(r) => `${String(r.occurredAt)}-${String(r.description)}`} columns={[
              { key: 'occurredAt', header: 'Date', render: (r) => formatDateTime(String(r.occurredAt)) }, { key: 'entryType', header: 'Type', render: (r) => label(r.entryType) },
              { key: 'description', header: 'Description' }, { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) }]} />
          </Card>
          {modal === 'send' && <SendModal path={`/api/v1/billing/corporate-accounts/${corp.id}/statement:send${qs({ from, to })}`} email={String(corp.email ?? '')}
            invalidate={BILLING} onClose={() => setModal('')} />}
          {modal === 'override' && <AmountModal title="Request Credit Override" path={`/api/v1/billing/customer-accounts/${String(x.account.id)}:credit-override`}
            invalidate={BILLING} onClose={() => setModal('')} reason help="Extra credit above the limit; valid for the days set in Credit Policies after approval." />}
        </div>
      )}
    </Drawer>
  );
}

// ── Customer Folios ───────────────────────────────────────────────────────

export function CustomerFoliosPage() {
  const [params, setParams] = useSearchParams();
  const open = params.get('id');
  return (
    <>
      <ListPage title="Customer Folios" help="One folio per customer or company across business lines; reservation folios merge into it (FR-BIL-P3-01)."
        path="/api/v1/billing/customer-folios" statuses={[{ value: 'open', label: 'Open' }, { value: 'closed', label: 'Closed' }]}
        onRowClick={(r) => setParams({ id: r.id })}
        columns={[{ key: 'number', header: 'Customer Folio' }, { key: 'holderName', header: 'Customer / Company' }, { key: 'folios', header: 'Folios', align: 'right' },
          { key: 'charges', header: 'Charges', align: 'right', render: (r) => money(r.charges) }, { key: 'paid', header: 'Paid', align: 'right', render: (r) => money(r.paid) },
          { key: 'balance', header: 'Balance', align: 'right', render: (r) => money(r.balance) }, { key: 'status', header: 'Status', render: pill('status') }]} />
      {open && <CustomerFolioDrawer id={open} onClose={() => setParams({})} />}
    </>
  );
}

function CustomerFolioDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const d = useGet<R & { byLine: R[]; folioList: R[]; invoices: R[] }>(`/api/v1/billing/customer-folios/${id}`);
  const [modal, setModal] = useState<'' | 'merge' | 'split'>('');
  const x = d.data;
  return (
    <Drawer open onClose={onClose} title={x ? `Customer Folio ${String(x.number)}` : 'Customer Folio'}>
      {d.isLoading && <Skeleton />}
      <ErrorAlert error={d.error} />
      {x && (
        <div className="oc-stack">
          <KV items={[['Customer / Company', String(x.holderName)], ['Charges', money(x.charges)], ['Paid', money(x.paid)], ['Balance', <strong key="b">{money(x.balance)}</strong>]]} />
          <div className="oc-row-wrap">
            {can('billing.customer_folio.manage') && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setModal('merge')}>Merge Folios</button>}
            {can('billing.customer_folio.split') && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setModal('split')}>Split Bill</button>}
            {can('billing.invoice.create') && <Link className="oc-btn oc-btn-sm oc-btn-primary" to={invoiceWorkspaceUrl('customerFolio', id)}>Create Invoice</Link>}
          </div>
          <Card title="By Business Line" icon="category">
            <DataTable rows={x.byLine} rowKey={(r) => String(r.businessLine)} columns={[{ key: 'businessLine', header: 'Line', render: (r) => label(r.businessLine) },
              { key: 'charges', header: 'Charges', align: 'right', render: (r) => money(r.charges) }]} />
          </Card>
          <Card title="Folios" icon="receipt">
            <DataTable rows={x.folioList} columns={[{ key: 'number', header: 'Folio' }, { key: 'holderName', header: 'Holder' }, { key: 'sourceRef', header: 'Reference' },
              { key: 'balance', header: 'Balance', align: 'right', render: (r) => money(r.balance) }, { key: 'status', header: 'Status', render: pill('status') }]} />
          </Card>
          {x.invoices.length > 0 && <Card title="Invoices" icon="request_quote">
            <DataTable rows={x.invoices} columns={[{ key: 'number', header: 'Invoice' }, { key: 'total', header: 'Total', align: 'right', render: (r) => money(r.total) },
              { key: 'status', header: 'Status', render: invoiceStatus }]} onRowClick={(r) => window.location.assign(`/billing/invoices?id=${r.id}`)} />
          </Card>}
          {modal === 'merge' && <MergeModal id={id} onClose={() => setModal('')} />}
          {modal === 'split' && <SplitModal folios={x.folioList} onClose={() => setModal('')} />}
        </div>
      )}
    </Drawer>
  );
}

function MergeModal({ id, onClose }: { id: string; onClose: () => void }) {
  const [q, setQ] = useState('');
  const [chosen, setChosen] = useState<string[]>([]);
  const folios = useGet<Page<R>>(`/api/v1/billing/folios${qs({ q, 'filter[status]': 'open', limit: 50 })}`);
  const send = useSend<Record<string, unknown>>('POST', `/api/v1/billing/customer-folios/${id}:merge`, BILLING);
  return (
    <Modal open onClose={onClose} title="Merge Folios" wide actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!chosen.length || send.isPending} onClick={() => send.mutate({ folioIds: chosen }, { onSuccess: onClose })}>Merge</button></>}>
      <TextField label="Search folios" value={q} onChange={setQ} />
      <div className="oc-stack" style={{ maxHeight: 320, overflow: 'auto' }}>
        {(folios.data?.items ?? []).map((f) => (
          <Checkbox key={f.id} label={`${String(f.number)} · ${String(f.holderName)} · ${money(f.balance)}`} checked={chosen.includes(f.id)}
            onChange={(v) => setChosen(v ? [...chosen, f.id] : chosen.filter((c) => c !== f.id))} />
        ))}
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function SplitModal({ folios, onClose }: { folios: R[]; onClose: () => void }) {
  const [folio, setFolio] = useState(folios[0]?.id ?? '');
  const detail = useGet<R & { lines: R[] }>(folio ? `/api/v1/billing/folios/${folio}` : null);
  const [lines, setLines] = useState<string[]>([]);
  const [percent, setPercent] = useState('100');
  const [payer, setPayer] = useState('customer');
  const [target, setTarget] = useState('');
  const [reason, setReason] = useState('');
  const customers = useGet<Page<R>>(payer === 'customer' ? '/api/v1/crm/customers?limit=200' : '/api/v1/crm/corporate-accounts?limit=200');
  const send = useSend<Record<string, unknown>>('POST', '/api/v1/billing/customer-folios:split', BILLING);
  return (
    <Modal open onClose={onClose} title="Split Bill" wide actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!lines.length || !target || !reason || send.isPending}
        onClick={() => send.mutate({ lineIds: lines, percent, reason, ...(payer === 'customer' ? { customerId: target } : { corporateAccountId: target }) }, { onSuccess: onClose })}>Split</button></>}>
      <div className="oc-form">
        <SelectField label="Folio" value={folio} onChange={(v) => { setFolio(v); setLines([]); }} options={folios.map((f) => ({ value: f.id, label: `${String(f.number)} · ${String(f.sourceRef ?? f.holderName)}` }))} />
        <TextField label="Share (%)" value={percent} onChange={setPercent} inputMode="decimal" />
        <SelectField label="Payer" value={payer} onChange={(v) => { setPayer(v); setTarget(''); }} options={[{ value: 'customer', label: 'Another customer' }, { value: 'corporate', label: 'Company' }]} />
        <SelectField label={payer === 'customer' ? 'Customer' : 'Company'} value={target} onChange={setTarget}
          options={(customers.data?.items ?? []).map((c) => ({ value: c.id, label: `${String(c.name)} (${String(c.code)})` }))} />
      </div>
      <div className="oc-stack" style={{ maxHeight: 240, overflow: 'auto' }}>
        {(detail.data?.lines ?? []).filter((l) => !l.voidedAt && !l.invoiceId).map((l) => (
          <Checkbox key={l.id} label={`${String(l.description)} · ${money(l.total)}`} checked={lines.includes(l.id)}
            onChange={(v) => setLines(v ? [...lines, l.id] : lines.filter((c) => c !== l.id))} />
        ))}
      </div>
      <TextArea label="Reason" value={reason} onChange={setReason} required />
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

// ── Payment Schedules ─────────────────────────────────────────────────────

export function PaymentSchedulesPage() {
  const [params, setParams] = useSearchParams();
  const open = params.get('id');
  return (
    <>
      <ListPage title="Payment Schedules" help="Down payments, installments and final payments with due-date reminders (FR-BIL-P3-03/07)."
        path="/api/v1/billing/payment-schedules" statuses={['active', 'completed', 'cancelled'].map((s) => ({ value: s, label: label(s) }))}
        onRowClick={(r) => setParams({ id: r.id })}
        columns={[{ key: 'number', header: 'Schedule' }, { key: 'title', header: 'Title' }, { key: 'sourceType', header: 'Source', render: (r) => label(r.sourceType) },
          { key: 'totalAmount', header: 'Total', align: 'right', render: (r) => money(r.totalAmount) }, { key: 'paidAmount', header: 'Paid', align: 'right', render: (r) => money(r.paidAmount) },
          { key: 'status', header: 'Status', render: pill('status') }]} />
      {open && <ScheduleDrawer id={open} onClose={() => setParams({})} />}
    </>
  );
}

function ScheduleDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const d = useGet<R & { lines: R[] }>(`/api/v1/billing/payment-schedules/${id}`);
  const [paying, setPaying] = useState<R | null>(null);
  const x = d.data;
  const inv = [...BILLING, `/api/v1/billing/payment-schedules/${id}`];
  return (
    <Drawer open onClose={onClose} title={x ? `Payment Schedule ${String(x.number)}` : 'Payment Schedule'}>
      {d.isLoading && <Skeleton />}
      <ErrorAlert error={d.error} />
      {x && (
        <div className="oc-stack">
          <KV items={[['Title', String(x.title)], ['Source', label(x.sourceType)], ['Total', money(x.totalAmount)], ['Paid', money(x.paidAmount)],
            ['Status', <StatusPill key="s" status={String(x.status)} />]]} />
          {x.status === 'active' && can('billing.payment_schedule.manage') &&
            <div><ActionButton label="Cancel unpaid lines" path={`/api/v1/billing/payment-schedules/${id}:cancel`} invalidate={inv} reason="required" danger /></div>}
          <DataTable rows={x.lines} columns={[{ key: 'label', header: 'Due item' }, { key: 'kind', header: 'Kind', render: (l) => label(l.kind) },
            { key: 'dueDate', header: 'Due', render: (l) => formatDate(String(l.dueDate)) }, { key: 'amount', header: 'Amount', align: 'right', render: (l) => money(l.amount) },
            { key: 'paidAmount', header: 'Paid', align: 'right', render: (l) => money(l.paidAmount) }, { key: 'status', header: 'Status', render: pill('status') }]}
            actions={(l) => ['pending', 'partially_paid', 'overdue'].includes(String(l.status)) ? (
              <div className="oc-row">
                {can('billing.payment.create') && <button className="oc-btn oc-btn-sm oc-btn-primary" onClick={() => setPaying(l)}>Pay</button>}
                {can('billing.invoice.create') && !l.invoiceId && <ActionButton label="Invoice" path={`/api/v1/billing/payment-schedules/${id}/lines/${l.id}:invoice`} invalidate={inv} />}
              </div>) : null} />
          {paying && <AmountModal title={`Pay ${String(paying.label)}`} path={`/api/v1/billing/payment-schedules/${id}/lines/${paying.id}:pay`} invalidate={inv}
            onClose={() => setPaying(null)} defaultAmount={String(Number(paying.amount) - Number(paying.paidAmount))} method idempotent />}
        </div>
      )}
    </Drawer>
  );
}

// ── Cashier (FR-EOD-01) ───────────────────────────────────────────────────

export function CashierPage() {
  const { can } = useAuth();
  const toast = useToast();
  const cur = useGet<R & { totals: R[] }>('/api/v1/billing/cashier-shifts/current', { retry: false });
  const shifts = useGet<Page<R>>('/api/v1/billing/cashier-shifts?limit=50');
  const [station, setStation] = useState('front_desk');
  const [float, setFloat] = useState('0');
  const [modal, setModal] = useState<'' | 'move' | 'close'>('');
  const open = useSend<Record<string, unknown>>('POST', '/api/v1/billing/cashier-shifts', BILLING);
  const x = cur.data;
  return (
    <div className="oc-stack">
      <PageHeader title="Cashier" help="Cashier shift across lines: opening float, payments per method, cash in / out, count and variance." />
      {cur.isLoading ? <Skeleton /> : x ? (
        <Card title={`My shift ${String(x.number)}`} icon="point_of_sale" ink>
          <KV items={[['Station', label(x.station)], ['Business date', formatDate(String(x.businessDate))], ['Opened', formatDateTime(String(x.openedAt))],
            ['Opening float', money(x.openingFloat)], ['Cash in', money(x.cashIn)], ['Cash out', money(x.cashOut)], ['Expected cash', <strong key="e">{money(x.expected)}</strong>]]} />
          <DataTable rows={x.totals} rowKey={(r) => String(r.methodType)} columns={[{ key: 'methodType', header: 'Method', render: (r) => label(r.methodType) },
            { key: 'count', header: 'Payments', align: 'right' }, { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) }]} />
          <div className="oc-row-wrap" style={{ marginTop: 12 }}>
            <button className="oc-btn oc-btn-neutral" onClick={() => setModal('move')}>Cash In / Out</button>
            <button className="oc-btn oc-btn-primary" onClick={() => setModal('close')}>Close Shift</button>
          </div>
        </Card>
      ) : can('billing.cashier_shift.operate') ? (
        <Card title="Open Shift" icon="lock_open">
          <div className="oc-form">
            <SelectField label="Station" value={station} onChange={setStation} options={['front_desk', 'sport_reception', 'banquet', 'golf', 'stay_desk', 'other']
              .map((s) => ({ value: s, label: label(s) }))} />
            <TextField label="Opening float" value={float} onChange={setFloat} inputMode="decimal" />
          </div>
          <ErrorAlert error={open.error} />
          <button className="oc-btn oc-btn-primary" disabled={open.isPending} onClick={() => open.mutate({ station, openingFloat: float }, { onSuccess: () => { toast('Shift opened'); void cur.refetch(); } })}>Open Shift</button>
        </Card>
      ) : <Empty title="No open shift" />}
      <Card title="Shifts" icon="history">
        <DataTable rows={shifts.data?.items} loading={shifts.isLoading} columns={[{ key: 'number', header: 'Shift' }, { key: 'cashierName', header: 'Cashier' },
          { key: 'station', header: 'Station', render: (r) => label(r.station) }, { key: 'businessDate', header: 'Business date', render: (r) => formatDate(String(r.businessDate)) },
          { key: 'expectedCash', header: 'Expected', align: 'right', render: (r) => money(r.expectedCash ?? r.expected) },
          { key: 'countedCash', header: 'Counted', align: 'right', render: (r) => money(r.countedCash) }, { key: 'variance', header: 'Variance', align: 'right', render: (r) => money(r.variance) },
          { key: 'status', header: 'Status', render: pill('status') }]} />
      </Card>
      {x && modal === 'move' && <CashMoveModal id={x.id} onClose={() => { setModal(''); void cur.refetch(); }} />}
      {x && modal === 'close' && <CloseShiftModal shift={x} onClose={() => { setModal(''); void cur.refetch(); }} />}
    </div>
  );
}

function CashMoveModal({ id, onClose }: { id: string; onClose: () => void }) {
  const [kind, setKind] = useState('cash_out');
  const [amount, setAmount] = useState('');
  const [reason, setReason] = useState('');
  const send = useSend<Record<string, unknown>>('POST', `/api/v1/billing/cashier-shifts/${id}/cash-movements`, BILLING);
  return (
    <Modal open onClose={onClose} title="Cash In / Out" actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!amount || !reason || send.isPending} onClick={() => send.mutate({ kind, amount, reason }, { onSuccess: onClose })}>Save</button></>}>
      <div className="oc-form">
        <SelectField label="Kind" value={kind} onChange={setKind} options={[{ value: 'cash_in', label: 'Cash In' }, { value: 'cash_out', label: 'Cash Out' }]} />
        <MoneyField label="Amount" value={amount} onChange={setAmount} required />
        <TextArea label="Reason" value={reason} onChange={setReason} required />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function CloseShiftModal({ shift, onClose }: { shift: R; onClose: () => void }) {
  const [counted, setCounted] = useState('');
  const [note, setNote] = useState('');
  const send = useSend<Record<string, unknown>>('POST', `/api/v1/billing/cashier-shifts/${shift.id}:close`, BILLING);
  const variance = counted ? Number(counted) - Number(shift.expected) : 0;
  return (
    <Modal open onClose={onClose} title="Close Shift" actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!counted || send.isPending} onClick={() => send.mutate({ countedCash: counted, note: note || undefined }, { onSuccess: onClose })}>Close Shift</button></>}>
      <div className="oc-form">
        <TextField label="Counted cash" value={counted} onChange={setCounted} inputMode="decimal" required help={`Expected ${money(shift.expected)}`} />
        {variance !== 0 && <TextArea label={`Variance ${money(variance)} — explain`} value={note} onChange={setNote} required />}
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

// ── Night Audit & Business Day (FR-EOD-03..06) ────────────────────────────

export function NightAuditPage() {
  const { can } = useAuth();
  const days = useGet<Page<R>>('/api/v1/billing/business-days?limit=30');
  const runs = useGet<Page<R & { checks: R[] }>>('/api/v1/billing/night-audit-runs?limit=20');
  const [tab, setTab] = useState('day');
  const current = days.data?.items.find((d) => d.current);
  const [date, setDate] = useState('');
  const day = date || String(current?.businessDate ?? '');
  const rev = useGet<R & { revenue: R[]; payments: R[]; liabilities: Record<string, string> }>(day ? `/api/v1/billing/daily-revenue${qs({ date: day })}` : null);
  const last = runs.data?.items[0];
  return (
    <div className="oc-stack">
      <PageHeader title="Night Audit" help="Closes the club's Business Day: all shifts closed, open folios reviewed, revenue frozen. Later transactions belong to the next business day."
        actions={can('billing.night_audit.run') && current ? <ActionButton label={`Run Night Audit · ${formatDate(String(current.businessDate))}`} kind="primary"
          path="/api/v1/billing/business-days:night-audit" invalidate={BILLING} confirm="Close the current business day? Figures are frozen afterwards." /> : undefined} />
      {last && last.status === 'blocked' && (
        <Card title={`Last run blocked · ${formatDate(String(last.businessDate))}`} icon="block">
          <DataTable rows={last.checks.filter((c) => c.severity !== 'info')} rowKey={(c) => String(c.check)} columns={[{ key: 'check', header: 'Check', render: (c) => label(c.check) },
            { key: 'severity', header: 'Severity', render: pill('severity') }, { key: 'count', header: 'Count', align: 'right' }, { key: 'message', header: 'What to do' },
            { key: 'items', header: 'Items', render: (c) => (Array.isArray(c.items) ? c.items.slice(0, 5).join(', ') : '—') }]} />
        </Card>
      )}
      <Tabs tabs={[{ value: 'day', label: 'Daily Revenue' }, { value: 'days', label: 'Business Days' }, { value: 'runs', label: 'Night Audit Runs' }]} value={tab} onChange={setTab} />
      {tab === 'day' && (
        <div className="oc-stack">
          <div style={{ width: 200 }}><TextField label="Business date" type="date" value={day} onChange={setDate} /></div>
          {rev.data && (
            <>
              <div className="oc-grid">
                {[['Charges', rev.data.charges], ['Payments', rev.data.paymentTotal], ['Refunds', rev.data.refunds], ['Shift payments', rev.data.shiftTotal],
                  ['Invoices issued', rev.data.invoiceTotal]].map(([k, v]) => (
                  <div key={String(k)} className="oc-card"><div className="oc-small oc-muted">{String(k)}</div><div className="oc-metric">{money(v)}</div></div>))}
              </div>
              <div className="oc-row"><StatusPill status={String(rev.data.status)} />{rev.data.frozen ? <span className="oc-small oc-muted">Frozen by the night audit</span> : null}</div>
              <Card title="Revenue by line & component" icon="bar_chart">
                <DataTable rows={rev.data.revenue} rowKey={(r) => `${String(r.businessLine)}-${String(r.revenueComponent)}-${String(r.liability)}`} columns={[
                  { key: 'businessLine', header: 'Line', render: (r) => label(r.businessLine) }, { key: 'revenueComponent', header: 'Component', render: (r) => label(r.revenueComponent) },
                  { key: 'net', header: 'Net', align: 'right', render: (r) => money(r.net) }, { key: 'service', header: 'Service', align: 'right', render: (r) => money(r.service) },
                  { key: 'tax', header: 'Tax', align: 'right', render: (r) => money(r.tax) }, { key: 'total', header: 'Total', align: 'right', render: (r) => money(r.total) }]} />
              </Card>
              <div className="oc-grid-2">
                <Card title="Payments by method" icon="payments">
                  <DataTable rows={rev.data.payments} rowKey={(r) => `${String(r.methodType)}-${String(r.purpose)}`} columns={[{ key: 'methodType', header: 'Method', render: (r) => label(r.methodType) },
                    { key: 'purpose', header: 'Purpose', render: (r) => label(r.purpose) }, { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) }]} />
                </Card>
                <Card title="Liabilities at the end of the day" icon="account_balance">
                  <KV items={Object.entries(rev.data.liabilities ?? {}).map(([k, v]) => [label(k), money(v)] as [string, React.ReactNode])} />
                </Card>
              </div>
            </>
          )}
        </div>
      )}
      {tab === 'days' && (
        <DataTable rows={days.data?.items} loading={days.isLoading} rowKey={(d) => String(d.businessDate)} columns={[
          { key: 'businessDate', header: 'Business Day', render: (d) => formatDate(String(d.businessDate)) }, { key: 'status', header: 'Status', render: pill('status') },
          { key: 'closedAt', header: 'Closed', render: (d) => (d.closedAt ? formatDateTime(String(d.closedAt)) : '—') }, { key: 'reopenReason', header: 'Reopen reason' }]}
          actions={(d) => d.status === 'closed' && can('billing.night_audit.reopen')
            ? <ActionButton label="Reopen" path={`/api/v1/billing/business-days/${String(d.businessDate)}:reopen`} invalidate={BILLING} reason="required" /> : null} />
      )}
      {tab === 'runs' && (
        <DataTable rows={runs.data?.items} loading={runs.isLoading} columns={[{ key: 'businessDate', header: 'Business Day', render: (r) => formatDate(String(r.businessDate)) },
          { key: 'mode', header: 'Mode', render: (r) => label(r.mode) }, { key: 'status', header: 'Result', render: pill('status') }, { key: 'exceptions', header: 'Exceptions', align: 'right' },
          { key: 'warnings', header: 'Warnings', align: 'right' }, { key: 'startedAt', header: 'Started', render: (r) => formatDateTime(String(r.startedAt)) }]} />
      )}
      <Link className="oc-small" to="/reports?module=billing">Billing Reports →</Link>
    </div>
  );
}

/** Back Office routes of Billing & Payment P3. */
export const BILLING_P3_ROUTES: { path: string; perm: string; element: React.ReactNode }[] = [
  { path: 'billing/invoices', perm: 'billing.invoice.view', element: <InvoicesPage /> },
  { path: 'billing/invoices/new', perm: 'billing.invoice.create', element: <InvoiceWorkspacePage /> },
  { path: 'billing/corporate-billing', perm: 'billing.invoice.view', element: <CorporateBillingPage /> },
  { path: 'billing/customer-folios', perm: 'billing.customer_folio.view', element: <CustomerFoliosPage /> },
  { path: 'billing/payment-schedules', perm: 'billing.payment_schedule.view', element: <PaymentSchedulesPage /> },
  { path: 'billing/cashier', perm: 'billing.cashier_shift.view', element: <CashierPage /> },
  { path: 'billing/night-audit', perm: 'billing.night_audit.view', element: <NightAuditPage /> },
];
