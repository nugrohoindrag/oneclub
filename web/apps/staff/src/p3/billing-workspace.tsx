import React, { useEffect, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router';
import { qs, useGet, useSend, type Schemas } from '@oneclub/api-client';
import { formatDate, formatDateTime } from '@oneclub/i18n';
import {
  ActionMenu, Card, Checkbox, DataTable, Drawer, Empty, ErrorAlert, Icon, Modal, MoneyField, PageHeader, SearchBox, SelectField, Skeleton, StatTile,
  StatusPill, TextArea, TextField, useAuth, useDebounced, usePagedList, useToast, type StatusTone,
} from '@oneclub/shell';
import { KV, money, type R } from '../p1/common';
import { InvoiceStatus } from './billing';
import { BUSINESS_LINES, COMPONENTS, InvoiceDocuments, TERMS } from './invoice-workspace';
import { CorporatePicker, CustomerPicker } from './sales';

// Revenue & Billing → Billing (docs/Revenue_Billing_Billing_Workspace_Improvement_Requirements.md):
// an action workspace, not a report. Every row answers "what is the next
// action": Pending Billing → Prepare Billing, Ready to Invoice → Generate
// Invoice, Exception → Resolve, Invoiced → View Invoice. Exceptions, the
// validation checklist and the amount to invoice come from the server
// (/api/v1/billing/billing-queue, /billing-records/{folio}).

type Item = Schemas['BillingItem'];
type Detail = Schemas['BillingRecordDetail'];
type Exception = Schemas['BillingException'];

const API = '/api/v1/billing';
const INVALIDATE = [API];
const PREPARE = 'billing.billing.prepare';
const APPROVE = 'billing.billing.approve';

const STATUS: Record<string, [StatusTone, string]> = {
  pending_billing: ['warning', 'Pending Billing'], ready_to_invoice: ['success', 'Ready to Invoice'], exception: ['error', 'Exception'],
  invoiced: ['info', 'Invoiced'], cancelled: ['neutral', 'Cancelled'], settled: ['neutral', 'Settled'],
};
const TABS: [string, string][] = [['pending_billing', 'Pending Billing'], ['ready_to_invoice', 'Ready to Invoice'], ['exception', 'Exceptions'], ['', 'All']];
const EMPTY: Record<string, [string, string]> = {
  pending_billing: ['All billing is up to date.', 'There are no transactions waiting for billing.'],
  ready_to_invoice: ['Nothing ready to invoice.', 'Billing records will appear here after validation.'],
  exception: ['No billing exceptions.', 'All billing records are ready to proceed.'],
  '': ['Nothing to bill.', 'Folios with charges to bill and their invoices appear here.'],
};
const GROUP_ICON: Record<string, string> = {
  golf: 'golf_course', resort: 'hotel', events: 'celebration', membership: 'card_membership', sport: 'sports_tennis', fnb: 'restaurant', other: 'receipt_long',
};
const SOURCE: Record<string, string> = {
  golf_booking: 'Golf booking', tournament: 'Tournament', golf_round: 'Golf round', walk_in: 'Walk-in', bag_storage: 'Bag storage', reservation: 'Reservation',
  stay: 'Stay', package_booking: 'Package', banquet_event: 'Event', quotation: 'Quotation', membership_fee: 'Membership fee', membership_renewal: 'Renewal',
  membership: 'Membership', sport_entry: 'Sport entry', class_enrollment: 'Class', locker: 'Locker', pos_order: 'F&B / Retail', voucher_sale: 'Voucher',
  split: 'Split billing', other: 'Other',
};
const ADJUST_KINDS = [
  { value: 'discount', label: 'Discount' }, { value: 'quantity', label: 'Quantity' }, { value: 'price', label: 'Unit price' },
  { value: 'service_charge', label: 'Service charge' }, { value: 'tax', label: 'Tax' }, { value: 'revenue_allocation', label: 'Revenue allocation' },
];

const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const opt = (v: string) => (v.trim() ? v.trim() : undefined);
export const prepareBillingUrl = (folioId: string) => `/accounting/revenue/billing/${folioId}`;
const invoiceUrl = (id: string) => `/accounting/revenue?tab=invoices&id=${id}`;
const sourceUrl = (it: Item) => (it.sourceType === 'banquet_event' && it.sourceId ? `/banquet-event/events/${it.sourceId}` : null);

export function BillingStatus({ status }: { status: string }) {
  const [tone, l] = STATUS[status] ?? ['neutral', label(status)];
  return <StatusPill status={status} tone={tone} label={l} />;
}

function Severity({ e }: { e: Exception }) {
  if (e.resolved) return <StatusPill status="resolved" tone="neutral" label="Resolved" />;
  return <StatusPill status={e.severity} tone={e.severity === 'high' ? 'error' : 'warning'} label={e.severity === 'high' ? 'High' : 'Medium'} />;
}

// ── workspace ─────────────────────────────────────────────────────────────

type ModalState =
  | { kind: 'generate'; items: Item[] } | { kind: 'consolidate'; items: Item[] } | { kind: 'assign'; items: Item[] }
  | { kind: 'resolve'; item: Item; code?: string } | { kind: 'note'; item: Item } | { kind: 'split'; item: Item }
  | { kind: 'review'; item: Item; view?: 'overview' | 'charges' | 'history' } | null;

/** Billing workspace: pending billing, ready to invoice, exceptions and all, per business. */
export function BillingWorkspace() {
  const { can } = useAuth();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const status = params.get('status') ?? 'pending_billing';
  const group = params.get('group') ?? '';
  const owner = params.get('owner') ?? '';
  const [search, setSearch] = useState(params.get('q') ?? '');
  const q = useDebounced(search);
  const set = (next: Record<string, string>) => {
    const p = new URLSearchParams(params);
    p.set('tab', 'billing');
    for (const [k, v] of Object.entries(next)) (v ? p.set(k, v) : p.delete(k));
    setParams(p, { replace: true });
  };
  const list = usePagedList<Item, Schemas['BillingQueue']>(
    `${API}/billing-queue${qs({ 'filter[status]': status || undefined, 'filter[group]': group || undefined, 'filter[owner]': owner || undefined, q: q || undefined })}`, 25);
  const [sel, setSel] = useState<Map<string, Item>>(new Map());
  const [modal, setModal] = useState<ModalState>(null);
  useEffect(() => setSel(new Map()), [status, group, owner, q]);
  const rows = list.rows;
  const groups = list.data?.groups ?? [];
  const counts = (list.data?.counts ?? {}) as Record<string, number>;
  const prepare = can(PREPARE);
  const issue = can('billing.invoice.issue');

  const toggle = (it: Item, on: boolean) => setSel((m) => { const n = new Map(m); if (on) n.set(it.folioId, it); else n.delete(it.folioId); return n; });
  const pageAll = !!rows?.length && rows.every((r) => sel.has(r.folioId));
  const selected = [...sel.values()];

  const rowMenu = (it: Item) => (
    <ActionMenu items={[
        { label: 'View Folio', icon: 'receipt', onClick: () => setModal({ kind: 'review', item: it, view: 'charges' }) },
        { label: 'View Source Transaction', icon: 'open_in_new', to: sourceUrl(it) ?? undefined, hidden: !sourceUrl(it) },
        { label: 'View Customer', icon: 'person', to: it.customerId ? `/crm/customers/${it.customerId}` : undefined, hidden: !it.customerId },
        { label: 'Edit Billing', icon: 'edit', to: prepareBillingUrl(it.folioId), hidden: !prepare || it.status === 'invoiced' || it.status === 'cancelled' },
        { label: 'Preview Invoice', icon: 'preview', to: `${prepareBillingUrl(it.folioId)}?preview=1`, hidden: it.status !== 'ready_to_invoice' },
        { label: 'View Revenue Allocation', icon: 'account_tree', onClick: () => setModal({ kind: 'review', item: it, view: 'charges' }), hidden: it.status !== 'ready_to_invoice' },
        { label: 'View Tax', icon: 'percent', onClick: () => setModal({ kind: 'review', item: it, view: 'charges' }), hidden: it.status !== 'ready_to_invoice' },
        { label: 'View Billing History', icon: 'history', onClick: () => setModal({ kind: 'review', item: it, view: 'history' }) },
        { label: 'Resolve Exception', icon: 'build', onClick: () => setModal({ kind: 'resolve', item: it }), hidden: !it.exceptions.length || it.status === 'exception' },
        { label: 'Split Billing', icon: 'alt_route', onClick: () => setModal({ kind: 'split', item: it }), hidden: !prepare || it.folioStatus !== 'open' || it.status === 'invoiced' },
        { label: 'Assign Owner', icon: 'person_add', onClick: () => setModal({ kind: 'assign', items: [it] }), hidden: !prepare || it.status === 'invoiced' },
        { label: 'Add Note', icon: 'sticky_note_2', onClick: () => setModal({ kind: 'note', item: it }), hidden: !prepare },
        { label: 'View Invoice', icon: 'receipt_long', to: it.invoiceId ? invoiceUrl(it.invoiceId) : undefined, hidden: !it.invoiceId || it.status === 'invoiced' },
    ]} />
  );
  // The last column of every row: the next action of the record.
  const actions = (it: Item) => {
    const review = <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setModal({ kind: 'review', item: it })}>Review</button>;
    const menu = rowMenu(it);
    switch (it.status) {
      case 'pending_billing':
        return <>{review}{prepare && <Link className="oc-btn oc-btn-sm oc-btn-primary" to={prepareBillingUrl(it.folioId)}>Prepare Billing</Link>}{menu}</>;
      case 'ready_to_invoice':
        return <>{review}{issue && <button className="oc-btn oc-btn-sm oc-btn-primary" onClick={() => setModal({ kind: 'generate', items: [it] })}>Generate Invoice</button>}{menu}</>;
      case 'exception':
        return <>
          <button className="oc-btn oc-btn-sm oc-btn-primary" onClick={() => setModal({ kind: 'resolve', item: it })}>Resolve</button>
          <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setModal({ kind: 'review', item: it })}>View Details</button>{menu}</>;
      case 'invoiced':
        return <>{it.invoiceId && <Link className="oc-btn oc-btn-sm oc-btn-neutral" to={invoiceUrl(it.invoiceId)}>View Invoice</Link>}{menu}</>;
      default:
        return <>{review}{menu}</>;
    }
  };

  const select = { key: 'select', width: 36, header: <input type="checkbox" aria-label="Select all on this page" checked={pageAll}
      onChange={(e) => rows?.forEach((r) => toggle(r, e.target.checked))} />,
    render: (r: Item) => <input type="checkbox" aria-label={`Select ${r.number}`} checked={sel.has(r.folioId)} onChange={(e) => toggle(r, e.target.checked)} /> };
  const folio = { key: 'number', header: 'Folio', render: (r: Item) => (
    <button className="oc-link-btn oc-nowrap" onClick={() => setModal({ kind: 'review', item: r })}><strong>{r.number}</strong></button>) };
  const source = { key: 'sourceType', header: 'Source', render: (r: Item) => <span className="oc-nowrap">{SOURCE[r.sourceType] ?? label(r.sourceType)}
    {r.sourceRef && <div className="oc-small oc-muted">{r.sourceRef}</div>}</span> };
  const customer = { key: 'holderName', header: 'Customer', render: (r: Item) => r.customerName ?? r.holderName };
  const billTo = { key: 'billTo', header: 'Bill To', render: (r: Item) => (r.billTo ? <>{r.billTo}{r.billToKind === 'company' && <div className="oc-small oc-muted">Company</div>}</>
    : <span className="oc-text-error">Not set</span>) };
  const charges = { key: 'charges', header: 'Charges', align: 'right' as const, render: (r: Item) => money(r.charges) };
  const toBill = { key: 'toInvoice', header: status === 'ready_to_invoice' ? 'To Invoice' : 'To Bill', align: 'right' as const, render: (r: Item) => <strong>{money(r.toInvoice)}</strong> };
  const exc = { key: 'exceptions', header: 'Exception', render: (r: Item) => {
    const open = r.exceptions.filter((e) => !e.resolved);
    if (!open.length) return <span className="oc-muted">—</span>;
    return <span className={open.some((e) => e.severity === 'high') ? 'oc-text-error' : 'oc-text-warning'} title={open.map((e) => e.label).join(', ')}>
      {open[0].label}{open.length > 1 ? ` +${open.length - 1}` : ''}</span>;
  } };
  const stat = { key: 'status', header: 'Status', render: (r: Item) => (r.status === 'invoiced' && r.invoiceStatus ? <InvoiceStatus status={r.invoiceStatus} /> : <BillingStatus status={r.status} />) };

  const columns = status === 'pending_billing'
    ? [select, folio, source, customer, billTo, charges,
      { key: 'paid', header: 'Paid / Deposit', align: 'right' as const, render: (r: Item) => money(Number(r.paid) + Number(r.deposit)) }, toBill, exc, stat]
    : status === 'ready_to_invoice'
      ? [select, folio, customer, billTo, charges, { key: 'deposit', header: 'Deposit', align: 'right' as const, render: (r: Item) => money(Number(r.paid) + Number(r.deposit)) }, toBill,
        { key: 'termsDays', header: 'Payment Term', render: (r: Item) => (r.termsDays ? `Net ${r.termsDays}` : 'Immediate') }, stat]
      : [folio, source, customer, billTo, charges, { key: 'paid', header: 'Paid', align: 'right' as const, render: (r: Item) => money(Number(r.paid) + Number(r.deposit)) },
        { key: 'toInvoice', header: 'To Bill', align: 'right' as const, render: (r: Item) => (r.status === 'invoiced' ? <span className="oc-muted">—</span> : money(r.toInvoice)) },
        { key: 'invoiceNumber', header: 'Invoice', render: (r: Item) => (r.invoiceId ? <Link to={invoiceUrl(r.invoiceId)}>{r.invoiceNumber ?? 'Draft'}</Link> : <span className="oc-muted">—</span>) }, stat];

  // Exceptions: one row per open exception.
  type ExRow = { id: string; item: Item; e: Exception };
  const exRows: ExRow[] = (rows ?? []).flatMap((it) => it.exceptions.filter((e) => !e.resolved).map((e) => ({ id: `${it.folioId}:${e.code}`, item: it, e })));
  const [emptyTitle, emptyHelp] = EMPTY[status] ?? EMPTY[''];
  const empty = <Empty title={emptyTitle} help={emptyHelp} icon="task_alt" />;

  const exportCsv = () => {
    const data = selected.length ? selected : rows ?? [];
    const head = ['Folio', 'Source', 'Source ref', 'Customer', 'Bill to', 'Charges', 'Paid', 'Deposit', 'To invoice', 'Status', 'Exceptions', 'Invoice', 'Owner'];
    const esc = (v: unknown) => `"${String(v ?? '').replace(/"/g, '""')}"`;
    const lines = data.map((r) => [r.number, r.sourceType, r.sourceRef, r.customerName ?? r.holderName, r.billTo, r.charges, r.paid, r.deposit, r.toInvoice,
      STATUS[r.status]?.[1] ?? r.status, r.exceptions.filter((e) => !e.resolved).map((e) => e.label).join('; '), r.invoiceNumber, r.ownerName].map(esc).join(','));
    const url = URL.createObjectURL(new Blob([[head.join(','), ...lines].join('\n')], { type: 'text/csv' }));
    const a = document.createElement('a');
    a.href = url;
    a.download = `billing-${status || 'all'}.csv`;
    a.click();
    URL.revokeObjectURL(url);
  };

  const invoiceable = selected.filter((r) => r.canInvoice && (r.status === 'pending_billing' || r.status === 'ready_to_invoice'));
  const attention = selected.filter((r) => !invoiceable.includes(r));

  return (
    <div className="oc-stack">
      <div className="oc-stat-grid">
        {groups.map((g) => (
          <StatTile key={g.key} label={g.label} icon={GROUP_ICON[g.key] ?? 'receipt_long'} value={money(g.totalBillable)} title="Total billable"
            muted={!!group && group !== g.key}>
            {([['pending_billing', 'Pending Billing', g.pending], ['ready_to_invoice', 'Ready to Invoice', g.ready], ['exception', 'Exceptions', g.exceptions]] as const).map(([s, l, n]) => (
              <button key={s} className="oc-stat-row" aria-pressed={group === g.key && status === s} onClick={() => set({ group: g.key, status: s })}>
                <i className="oc-stat-dot" data-status={s} /><span>{l}</span><span className="oc-spacer" />
                {s === 'exception' && n ? <span className="oc-dash-chip" data-good={false}>{n}</span> : <strong>{n}</strong>}
              </button>
            ))}
          </StatTile>
        ))}
      </div>

      <div className="oc-row-wrap">
        <div className="oc-row-wrap" role="tablist" aria-label="Billing status">
          {TABS.map(([v, l]) => (
            <button key={v || 'all'} role="tab" className="oc-chip" aria-pressed={status === v} aria-selected={status === v} onClick={() => set({ status: v || 'all' })}>
              {l}{v && counts[v] != null ? ` · ${counts[v]}` : ''}
            </button>
          ))}
        </div>
        <span className="oc-spacer" />
        {group && <button className="oc-chip" aria-pressed="true" onClick={() => set({ group: '' })}>{groups.find((g) => g.key === group)?.label ?? group} ✕</button>}
        <button className="oc-chip" aria-pressed={owner === 'me'} onClick={() => set({ owner: owner === 'me' ? '' : 'me' })}><Icon name="person" size={16} /> Mine</button>
        <SearchBox value={search} onChange={(v) => { setSearch(v); set({ q: v }); }} placeholder="Folio, customer, bill to, invoice" />
      </div>

      {selected.length > 0 && (
        <div className="oc-bulkbar" role="region" aria-label="Bulk actions">
          <strong>{selected.length} selected</strong>
          {invoiceable.length > 0 && attention.length > 0 && <span className="oc-small">{invoiceable.length} can be invoiced · {attention.length} require attention</span>}
          <span className="oc-spacer" />
          {prepare && selected.length === 1 && selected[0].status !== 'invoiced' &&
            <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => navigate(prepareBillingUrl(selected[0].folioId))}>Prepare Billing</button>}
          {attention.length > 0 && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => set({ status: 'exception' })}>Review {attention.length} Exception{attention.length > 1 ? 's' : ''}</button>}
          {issue && invoiceable.length > 0 && <button className="oc-btn oc-btn-sm oc-btn-primary" onClick={() => setModal({ kind: 'generate', items: selected })}>
            Generate {invoiceable.length} Invoice{invoiceable.length > 1 ? 's' : ''}</button>}
          {can(APPROVE) && issue && selected.length > 1 && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setModal({ kind: 'consolidate', items: selected })}>Consolidate</button>}
          {prepare && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setModal({ kind: 'assign', items: selected })}>Assign Owner</button>}
          <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={exportCsv}>Export</button>
          <button className="oc-btn oc-btn-sm oc-btn-text" onClick={() => setSel(new Map())}>Clear</button>
        </div>
      )}

      <div className="oc-sticky-actions">
      {status === 'exception'
        ? <DataTable rows={rows ? exRows : undefined} loading={list.isLoading} error={list.error} server={list.pager} empty={empty} actionsHeader="Action"
          columns={[{ key: 'folio', header: 'Folio', render: (x) => <button className="oc-link-btn" onClick={() => setModal({ kind: 'review', item: x.item })}><strong>{x.item.number}</strong></button> },
            { key: 'customer', header: 'Customer', render: (x) => x.item.customerName ?? x.item.holderName },
            { key: 'amount', header: 'Amount', align: 'right', render: (x) => money(x.item.toInvoice) },
            { key: 'exception', header: 'Exception', render: (x) => <>{x.e.label}<div className="oc-small oc-muted">{x.e.message}</div></> },
            { key: 'severity', header: 'Severity', render: (x) => <Severity e={x.e} /> },
            { key: 'created', header: 'Created', render: (x) => formatDate(x.item.dueSince) },
            { key: 'owner', header: 'Owner', render: (x) => x.item.ownerName ?? <span className="oc-muted">Unassigned</span> },
            { key: 'status', header: 'Status', render: () => <StatusPill status="open" tone="error" label="Open" /> }]}
          actions={(x) => <>
            <button className="oc-btn oc-btn-sm oc-btn-primary" onClick={() => setModal({ kind: 'resolve', item: x.item, code: x.e.code })}>Resolve</button>
            <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setModal({ kind: 'review', item: x.item })}>View Details</button>
            {rowMenu(x.item)}
          </>} />
        : <DataTable<Item> rows={rows} loading={list.isLoading} error={list.error} rowKey={(r) => r.folioId} server={list.pager} empty={empty} actionsHeader="Action"
          columns={columns} actions={actions} />}
      </div>

      {modal?.kind === 'review' && <BillingReviewDrawer folioId={modal.item.folioId} view={modal.view} onClose={() => setModal(null)}
        onResolve={(code) => setModal({ kind: 'resolve', item: modal.item, code })} onGenerate={(it) => setModal({ kind: 'generate', items: [it] })} />}
      {modal?.kind === 'generate' && <GenerateModal items={modal.items} onClose={() => { setModal(null); setSel(new Map()); }}
        onReview={() => { setModal(null); set({ status: 'exception' }); }} />}
      {modal?.kind === 'consolidate' && <ConsolidateModal items={modal.items} onClose={() => { setModal(null); setSel(new Map()); }} />}
      {modal?.kind === 'assign' && <AssignModal items={modal.items} onClose={() => setModal(null)} />}
      {modal?.kind === 'resolve' && <ResolveModal folioId={modal.item.folioId} code={modal.code} onClose={() => setModal(null)} />}
      {modal?.kind === 'note' && <NoteModal folioId={modal.item.folioId} onClose={() => setModal(null)} />}
      {modal?.kind === 'split' && <SplitModal item={modal.item} onClose={() => setModal(null)} />}
    </div>
  );
}

// ── review drawer ─────────────────────────────────────────────────────────

/** Review: the validation checklist, exceptions, calculation, charges and history of a folio. */
function BillingReviewDrawer({ folioId, view = 'overview', onClose, onResolve, onGenerate }: {
  folioId: string; view?: 'overview' | 'charges' | 'history'; onClose: () => void; onResolve: (code?: string) => void; onGenerate: (it: Item) => void;
}) {
  const { can } = useAuth();
  const d = useGet<Detail>(`${API}/billing-records/${folioId}`);
  const [tab, setTab] = useState(view);
  const x = d.data;
  const it = x?.item;
  return (
    <Drawer open onClose={onClose} title={it ? `Billing ${it.number}` : 'Billing'}>
      {d.isLoading && <Skeleton />}
      <ErrorAlert error={d.error} />
      {x && it && (
        <div className="oc-stack">
          <div className="oc-row-wrap">
            <BillingStatus status={it.status} />
            {it.ownerName && <span className="oc-small oc-muted">Owner: {it.ownerName}</span>}
            <span className="oc-spacer" />
            {can(PREPARE) && !['invoiced', 'cancelled', 'settled'].includes(it.status) &&
              <Link className="oc-btn oc-btn-sm oc-btn-neutral" to={prepareBillingUrl(folioId)}>Prepare Billing</Link>}
            {it.canInvoice && ['pending_billing', 'ready_to_invoice'].includes(it.status) && can('billing.invoice.issue') &&
              <button className="oc-btn oc-btn-sm oc-btn-primary" onClick={() => onGenerate(it)}>Generate Invoice</button>}
            {it.blocking > 0 && <button className="oc-btn oc-btn-sm oc-btn-primary" onClick={() => onResolve()}>Resolve</button>}
            {it.invoiceId && <Link className="oc-btn oc-btn-sm oc-btn-neutral" to={invoiceUrl(it.invoiceId)}>View Invoice {it.invoiceNumber ?? ''}</Link>}
          </div>
          <div className="oc-row-wrap" role="tablist">
            {(['overview', 'charges', 'history'] as const).map((t) => (
              <button key={t} role="tab" className="oc-chip" aria-pressed={tab === t} aria-selected={tab === t} onClick={() => setTab(t)}>{{ overview: 'Overview', charges: 'Charges', history: 'History' }[t]}</button>
            ))}
          </div>
          {tab === 'overview' && <>
            <KV items={[['Folio', <>{it.number} · {label(it.folioStatus)}</>], ['Source', <>{SOURCE[it.sourceType] ?? label(it.sourceType)}{it.sourceRef ? ` · ${it.sourceRef}` : ''}</>],
              ['Customer', it.customerName ?? it.holderName], ['Bill to', it.billTo || <span className="oc-text-error">Not set</span>],
              ['Payment term', it.termsDays ? `Net ${it.termsDays} days` : 'Immediate'], ['Invoice date', formatDate(x.businessDate)]]} />
            <Card title="Validation" icon="checklist"><Checklist checks={x.checks} /></Card>
            {it.exceptions.length > 0 && <Card title="Exceptions" icon="report"><ExceptionList item={it} onResolve={onResolve} /></Card>}
            <Card title="Calculation" icon="calculate"><Breakdown b={x.breakdown} item={it} /></Card>
          </>}
          {tab === 'charges' && <>
            <Card title="Charges" icon="receipt"><ChargeTable lines={x.lines} /></Card>
            {x.adjustments.length > 0 && <Card title="Adjustments" icon="tune"><Adjustments rows={x.adjustments} /></Card>}
            {x.deposits.length > 0 && <Card title="Deposits" icon="savings">
              <DataTable rows={x.deposits as unknown as R[]} pageSize={0} columns={[{ key: 'number', header: 'Deposit' },
                { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) },
                { key: 'appliedAmount', header: 'Applied', align: 'right', render: (r) => money(r.appliedAmount) }, { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]} />
            </Card>}
          </>}
          {tab === 'history' && <History rows={x.history} />}
        </div>
      )}
    </Drawer>
  );
}

function Checklist({ checks }: { checks: Schemas['BillingCheck'][] }) {
  return (
    <ul className="oc-checklist">
      {checks.map((c) => (
        <li key={c.key} data-ok={c.ok}>
          <Icon name={c.ok ? 'check_circle' : 'cancel'} size={18} />
          <span>{c.label}{c.detail && <span className="oc-small oc-muted"> — {c.detail}</span>}</span>
        </li>
      ))}
    </ul>
  );
}

function ExceptionList({ item, onResolve }: { item: Item; onResolve: (code: string) => void }) {
  return (
    <div className="oc-stack" style={{ gap: 8 }}>
      {item.exceptions.map((e) => (
        <div key={e.code} className="oc-row-wrap" style={{ alignItems: 'flex-start' }}>
          <Severity e={e} />
          <div style={{ flex: 1, minWidth: 200 }}>
            <strong>{e.label}</strong>
            <div className="oc-small oc-muted">{e.message}</div>
            {e.resolved && <div className="oc-small">Resolved by {e.resolved.byName} · {e.resolved.reason}</div>}
          </div>
          {!e.resolved && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => onResolve(e.code)}>Resolve</button>}
        </div>
      ))}
    </div>
  );
}

function Breakdown({ b, item }: { b: Schemas['BillingBreakdown']; item: Item }) {
  if (item.status === 'invoiced' || item.status === 'settled') {
    return <p style={{ margin: 0 }}>{item.invoiceId
      ? <>Every charge is on invoice <Link to={invoiceUrl(item.invoiceId)}>{item.invoiceNumber ?? 'Draft'}</Link>; payment continues in Accounts Receivable.</>
      : 'Nothing left to bill: the folio is settled.'}</p>;
  }
  const row = (l: string, v: string, sign = '', strong = false) => (
    <div className="oc-row" key={l}><span>{l}</span><span className="oc-spacer" />{strong ? <strong>{sign}{money(v)}</strong> : <span>{sign}{money(v)}</span>}</div>
  );
  return (
    <div className="oc-stack" style={{ gap: 6 }}>
      {row('Gross charges', b.grossCharges)}
      {Number(b.discount) !== 0 && row('Discount', b.discount, '− ')}
      {row('Service charge', b.serviceCharge, '+ ')}
      {row('Tax', b.tax, '+ ')}
      {Number(b.deposit) !== 0 && row('Deposit / advance', b.deposit, '− ')}
      {Number(b.payments) !== 0 && row('Payments received', b.payments, '− ')}
      {Number(b.transferred) !== 0 && row('Already billed to AR', b.transferred, '− ')}
      <div className="oc-menu-sep" />
      {row('Amount to invoice', b.toInvoice, '', true)}
    </div>
  );
}

function ChargeTable({ lines, onAdjust }: { lines: Schemas['BillingLine'][]; onAdjust?: (l: Schemas['BillingLine']) => void }) {
  return (
    <DataTable rows={lines as unknown as R[]} pageSize={0} actionsHeader={onAdjust ? 'Action' : undefined}
      columns={[{ key: 'description', header: 'Charge', render: (l) => <>{String(l.description)}
        <div className="oc-small oc-muted">{l.referenceType === 'billing.adjustment' ? 'Billing adjustment · ' : ''}
          {l.invoiceId ? <Link to={invoiceUrl(String(l.invoiceId))}>Invoiced</Link> : 'To bill'}</div></> },
        { key: 'businessLine', header: 'Revenue allocation', render: (l) => <>{label(l.businessLine)}<div className="oc-small oc-muted">{label(l.revenueComponent)}</div></> },
        { key: 'quantity', header: 'Qty × Price / Net', align: 'right', render: (l) => <span className="oc-nowrap">{String(l.quantity)} × {money(l.unitPrice)}
          <div className="oc-small oc-muted">{money(l.netAmount)}</div></span> },
        { key: 'taxAmount', header: 'Service / Tax', align: 'right', render: (l) => <span className="oc-nowrap">{money(l.serviceAmount)}<div className="oc-small oc-muted">{money(l.taxAmount)}</div></span> },
        { key: 'total', header: 'Total', align: 'right', render: (l) => <strong className="oc-nowrap">{money(l.total)}</strong> }]}
      actions={onAdjust ? (l) => (!l.invoiceId && l.referenceType !== 'billing.adjustment'
        ? <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => onAdjust(l as unknown as Schemas['BillingLine'])}>Adjust</button> : null) : undefined} />
  );
}

function Adjustments({ rows }: { rows: Schemas['BillingAdjustment'][] }) {
  return (
    <DataTable rows={rows as unknown as R[]} pageSize={0} columns={[{ key: 'createdAt', header: 'When', render: (a) => formatDateTime(String(a.createdAt)) },
      { key: 'lineLabel', header: 'Charge' }, { key: 'kind', header: 'Change', render: (a) => `${label(a.kind)}: ${String(a.previousValue)} → ${String(a.newValue)}` },
      { key: 'amount', header: 'Amount', align: 'right', render: (a) => money(a.amount) },
      { key: 'reason', header: 'Reason', render: (a) => <>{String(a.reason)}{a.reference ? <div className="oc-small oc-muted">Ref {String(a.reference)}</div> : null}</> },
      { key: 'createdByName', header: 'By' },
      { key: 'approvedAt', header: 'Approval', render: (a) => (a.approvedAt ? <StatusPill status="approved" tone="success" label={`Approved · ${String(a.approvedByName ?? '')}`} />
        : <StatusPill status="pending" tone="warning" label="Waiting" />) }]} />
  );
}

const HISTORY_ICON: Record<string, string> = {
  charge_created: 'add_shopping_cart', charge_voided: 'remove', deposit_received: 'savings', deposit_applied: 'savings', payment: 'payments',
  ar_transfer: 'account_balance', billing_opened: 'description', billing_edited: 'edit', discount_changed: 'sell', tax_changed: 'percent',
  billing_adjusted: 'tune', billing_approved: 'verified', billing_marked_ready: 'task_alt', exception_resolved: 'build', owner_assigned: 'person_add',
  billing_split: 'alt_route', note: 'sticky_note_2', invoice_generated: 'receipt_long', invoice_created: 'receipt_long', invoice_posted: 'menu_book',
  invoice_sent: 'send', invoice_voided: 'block', credit_note_created: 'receipt',
};

function History({ rows }: { rows: Schemas['BillingHistoryEntry'][] }) {
  if (!rows.length) return <Empty title="No history yet" icon="history" />;
  return (
    <ol className="oc-timeline">
      {rows.map((h, i) => (
        <li key={i}>
          <Icon name={HISTORY_ICON[h.kind] ?? 'circle'} size={18} />
          <div>
            <div>{h.ref && h.kind.startsWith('invoice') ? <Link to={invoiceUrl(h.ref)}>{h.summary}</Link> : h.summary}</div>
            <div className="oc-small oc-muted">{formatDateTime(h.at)}{h.actor ? ` · ${h.actor}` : ''}{h.amount ? ` · ${money(h.amount)}` : ''}</div>
          </div>
        </li>
      ))}
    </ol>
  );
}

// ── modals ────────────────────────────────────────────────────────────────

/** Generate Invoice(s): never for folios with a blocking exception. */
function GenerateModal({ items, onClose, onReview }: { items: Item[]; onClose: () => void; onReview: () => void }) {
  const toast = useToast();
  const ok = items.filter((r) => r.canInvoice && (r.status === 'pending_billing' || r.status === 'ready_to_invoice'));
  const bad = items.filter((r) => !ok.includes(r));
  const [send, setSend] = useState(true);
  const gen = useSend<Record<string, unknown>, Schemas['BillingGenerateResults']>('POST', `${API}/billing-records:generate`, INVALIDATE);
  const res = gen.data;
  return (
    <Modal open onClose={onClose} title={res ? 'Invoices Generated' : ok.length === 1 ? 'Generate Invoice' : `Generate ${ok.length} Invoices`} wide
      actions={res ? <button className="oc-btn oc-btn-primary" onClick={onClose}>Done</button> : <>
        <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
        {bad.length > 0 && items.length > 1 && <button className="oc-btn oc-btn-neutral" onClick={onReview}>Review {bad.length} Exception{bad.length > 1 ? 's' : ''}</button>}
        <button className="oc-btn oc-btn-primary" disabled={!ok.length || gen.isPending}
          onClick={() => gen.mutate({ folioIds: ok.map((r) => r.folioId), send }, { onSuccess: (r) => toast(`${r.generated} invoice(s) generated`) })}>
          Generate {ok.length} Invoice{ok.length === 1 ? '' : 's'}</button></>}>
      {!res && <div className="oc-stack">
        {items.length > 1 && <p style={{ margin: 0 }}><strong>{items.length} selected</strong> · {ok.length} can be invoiced · {bad.length} require attention</p>}
        {ok.length > 0 && <DataTable rows={ok} rowKey={(r) => r.folioId} pageSize={0} columns={[{ key: 'number', header: 'Folio' }, { key: 'billTo', header: 'Bill To' },
          { key: 'termsDays', header: 'Term', render: (r) => (r.termsDays ? `Net ${r.termsDays}` : 'Immediate') },
          { key: 'toInvoice', header: 'To Invoice', align: 'right', render: (r) => money(r.toInvoice) }]} />}
        {bad.length > 0 && <div className="oc-alert oc-alert-warning">
          <strong>Not invoiced (blocking exceptions):</strong> {bad.map((r) => `${r.number} (${r.exceptions.filter((e) => !e.resolved).map((e) => e.label).join(', ') || label(r.status)})`).join('; ')}
        </div>}
        <Checkbox label="Send each invoice to the customer (e-mail / WhatsApp / portal notification)" checked={send} onChange={setSend} />
        <p className="oc-small oc-muted" style={{ margin: 0 }}>Each invoice gets its number, is posted to Accounts Receivable and the ledger, is linked back to its folio
          and appears in the Customer Portal with its PDF.</p>
        <ErrorAlert error={gen.error} />
      </div>}
      {res && <DataTable rows={res.results} rowKey={(r) => r.folioId} pageSize={0} columns={[{ key: 'number', header: 'Folio' },
        { key: 'invoiceNumber', header: 'Invoice', render: (r) => (r.invoiceId ? <Link to={invoiceUrl(r.invoiceId)} onClick={onClose}>{r.invoiceNumber}</Link> : '—') },
        { key: 'total', header: 'Total', align: 'right', render: (r) => (r.total ? money(r.total) : '—') },
        { key: 'sent', header: 'Result', render: (r) => (r.error ? <span className="oc-text-error">{r.error}</span> : r.sent ? 'Issued & sent' : 'Issued') }]} />}
    </Modal>
  );
}

/** Consolidate: one invoice for folios of the same payer (Finance Manager). */
function ConsolidateModal({ items, onClose }: { items: Item[]; onClose: () => void }) {
  const toast = useToast();
  const navigate = useNavigate();
  const [term, setTerm] = useState('');
  const [send, setSend] = useState(true);
  const payer = (r: Item) => (r.billToKind === 'company' ? `c:${r.corporateAccountId}` : r.billToKind === 'customer' ? `p:${r.customerId}` : 'none');
  const same = new Set(items.map(payer)).size === 1 && payer(items[0]) !== 'none';
  const total = items.reduce((a, r) => a + Number(r.toInvoice), 0);
  const con = useSend<Record<string, unknown>, Schemas['BillingGenerateResult']>('POST', `${API}/billing-records:consolidate`, INVALIDATE);
  return (
    <Modal open onClose={onClose} title="Consolidate Billing" wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!same || con.isPending} onClick={() => con.mutate({
        folioIds: items.map((r) => r.folioId), termsDays: term ? Number(term) : undefined, send,
      }, { onSuccess: (r) => { toast(`Consolidated invoice ${r.invoiceNumber ?? ''} generated`); onClose(); if (r.invoiceId) navigate(invoiceUrl(r.invoiceId)); } })}>Consolidate</button></>}>
      <div className="oc-stack">
        <DataTable rows={items} rowKey={(r) => r.folioId} pageSize={0} columns={[{ key: 'number', header: 'Folio' }, { key: 'sourceType', header: 'Business', render: (r) => label(r.group) },
          { key: 'billTo', header: 'Bill To' }, { key: 'toInvoice', header: 'Amount', align: 'right', render: (r) => money(r.toInvoice) }]} />
        <div className="oc-row"><strong>Total</strong><span className="oc-spacer" /><strong>{money(total)}</strong></div>
        {!same && <div className="oc-alert oc-alert-warning">Every folio of a consolidated invoice must be billed to the same company or customer. Set the Bill To in Prepare Billing first.</div>}
        <SelectField label="Payment term" value={term} onChange={setTerm} options={TERMS.filter((t) => t.value !== 'custom')} />
        <Checkbox label="Send the invoice to the customer" checked={send} onChange={setSend} />
        <p className="oc-small oc-muted" style={{ margin: 0 }}>One invoice; every line stays linked to its source folio.</p>
        <ErrorAlert error={con.error} />
      </div>
    </Modal>
  );
}

function AssignModal({ items, onClose }: { items: Item[]; onClose: () => void }) {
  const toast = useToast();
  const { me } = useAuth();
  const owners = useGet<{ items: Schemas['BillingOwner'][] }>(`${API}/billing-owners`);
  const [owner, setOwner] = useState(items.length === 1 ? items[0].ownerId ?? '' : '');
  const send = useSend<Record<string, unknown>>('POST', `${API}/billing-records:assign`, INVALIDATE);
  const run = (id: string) => send.mutate({ folioIds: items.map((r) => r.folioId), ownerId: id || undefined }, { onSuccess: () => { toast('Owner assigned'); onClose(); } });
  return (
    <Modal open onClose={onClose} title={`Assign Owner · ${items.length} folio${items.length > 1 ? 's' : ''}`} actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      {me?.id && <button className="oc-btn oc-btn-neutral" disabled={send.isPending} onClick={() => run(me.id)}>Assign to me</button>}
      <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => run(owner)}>{owner ? 'Assign' : 'Unassign'}</button></>}>
      <div className="oc-form">
        <SelectField label="Owner" value={owner} onChange={setOwner} placeholder="Unassigned"
          options={(owners.data?.items ?? []).map((u) => ({ value: u.id, label: u.fullName }))} />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

/** Resolve: fix (Prepare Billing), acknowledge (accountant), override (Finance Manager) or approve. */
function ResolveModal({ folioId, code, onClose }: { folioId: string; code?: string; onClose: () => void }) {
  const d = useGet<Detail>(`${API}/billing-records/${folioId}`);
  const it = d.data?.item;
  const open = (it?.exceptions ?? []).filter((e) => !e.resolved);
  const list = open.some((e) => e.code === code) ? open.filter((e) => e.code === code) : open;
  return (
    <Modal open onClose={onClose} title={it ? `Resolve · ${it.number}` : 'Resolve'} wide actions={<button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button>}>
      {d.isLoading && <Skeleton />}
      <ErrorAlert error={d.error} />
      {it && !list.length && <Empty title="No open exception" help="The billing can proceed." icon="task_alt" />}
      <div className="oc-stack">
        {list.map((e) => <ResolveCard key={e.code} folioId={folioId} e={e} onClose={onClose} />)}
      </div>
    </Modal>
  );
}

/** One exception with the way to resolve it. */
function ResolveCard({ folioId, e, onClose }: { folioId: string; e: Exception; onClose: () => void }) {
  const { can } = useAuth();
  const toast = useToast();
  const [reason, setReason] = useState('');
  const resolve = useSend<{ reason: string }>('POST', `${API}/billing-records/${folioId}/exceptions/${e.code}:resolve`, INVALIDATE);
  const approve = useSend<{ reason?: string }>('POST', `${API}/billing-records/${folioId}:approve`, INVALIDATE);
  const fix = <Link className="oc-btn oc-btn-sm oc-btn-text" to={prepareBillingUrl(folioId)} onClick={onClose}>Fix in Prepare Billing</Link>;
  return (
    <Card title={e.label} icon={e.severity === 'high' ? 'error' : 'warning'}>
      <p style={{ marginTop: 0 }}>{e.message}</p>
      {e.resolution === 'fix' && <Link className="oc-btn oc-btn-sm oc-btn-primary" to={prepareBillingUrl(folioId)} onClick={onClose}>Fix in Prepare Billing</Link>}
      {e.resolution === 'approve' && (can(APPROVE)
        ? <button className="oc-btn oc-btn-sm oc-btn-primary" disabled={approve.isPending}
          onClick={() => approve.mutate({}, { onSuccess: () => toast('Billing approved') })}>Approve billing</button>
        : <p className="oc-small oc-muted" style={{ margin: 0 }}>Waiting for the Finance Manager (or General Manager / Director above the executive threshold).</p>)}
      {e.resolution === 'override' && !can(APPROVE) &&
        <p className="oc-small oc-muted" style={{ margin: 0 }}>Fix the data in <Link to={prepareBillingUrl(folioId)} onClick={onClose}>Prepare Billing</Link>, or ask the Finance Manager to override it.</p>}
      {(e.resolution === 'acknowledge' || (e.resolution === 'override' && can(APPROVE))) && (
        <div className="oc-stack" style={{ gap: 8 }}>
          <TextArea label={e.resolution === 'override' ? 'Override reason' : 'Reason'} value={reason} onChange={setReason} required />
          <div className="oc-row-wrap">
            <button className="oc-btn oc-btn-sm oc-btn-primary" disabled={!reason.trim() || resolve.isPending}
              onClick={() => resolve.mutate({ reason }, { onSuccess: () => toast(`${e.label} resolved`) })}>{e.resolution === 'override' ? 'Override' : 'Acknowledge'}</button>
            {fix}
          </div>
        </div>
      )}
      <ErrorAlert error={resolve.error ?? approve.error} />
    </Card>
  );
}

function NoteModal({ folioId, onClose }: { folioId: string; onClose: () => void }) {
  const toast = useToast();
  const [body, setBody] = useState('');
  const send = useSend<Record<string, unknown>>('POST', `${API}/billing-records/${folioId}/notes`, INVALIDATE);
  return (
    <Modal open onClose={onClose} title="Add Note" actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!body.trim() || send.isPending} onClick={() => send.mutate({ body }, { onSuccess: () => { toast('Note added'); onClose(); } })}>Add Note</button></>}>
      <TextArea label="Note" value={body} onChange={setBody} required help="Shown in the billing history" />
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

/** Split Billing: move a share of the folio to another company or customer (open folios). */
function SplitModal({ item, onClose }: { item: Item; onClose: () => void }) {
  const toast = useToast();
  const [corporate, setCorporate] = useState('');
  const [customer, setCustomer] = useState('');
  const [amount, setAmount] = useState('');
  const [reason, setReason] = useState('');
  const send = useSend<Record<string, unknown>, R>('POST', `${API}/billing-records/${item.folioId}:split`, INVALIDATE);
  const rest = Number(item.charges) - Number(amount || 0);
  return (
    <Modal open onClose={onClose} title={`Split Billing · ${item.number}`} actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={(!corporate && !customer) || !amount || !reason.trim() || send.isPending}
        onClick={() => send.mutate({ corporateAccountId: opt(corporate), customerId: corporate ? undefined : opt(customer), amount, reason },
          { onSuccess: (r) => { toast(`Moved ${money(r.moved)} to folio ${String((r.targetFolio as R)?.number ?? '')}`); onClose(); } })}>Split Billing</button></>}>
      <div className="oc-form">
        <CorporatePicker value={corporate} onChange={setCorporate} />
        {!corporate && <CustomerPicker value={customer} onChange={setCustomer} label="Or another customer" />}
        <MoneyField label="Amount for this party" value={amount} onChange={setAmount} help={`Folio charges ${money(item.charges)}`} />
        <TextArea label="Reason" value={reason} onChange={setReason} required />
      </div>
      {amount && <p className="oc-small">This party: <strong>{money(amount)}</strong> · stays on {item.billTo || item.holderName}: <strong>{money(rest)}</strong></p>}
      <p className="oc-small oc-muted">Every uninvoiced charge is shared in proportion; the original folio and its charges are kept with the split history.</p>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

// ── Prepare Billing (full page) ───────────────────────────────────────────

type Form = {
  corporateAccountId: string; customerId: string; billToName: string; billToAddress: string; billToNpwp: string; billToEmail: string; billToPhone: string;
  term: string; customDays: string; customerPo: string; contractRef: string; billingRef: string; notes: string; internalNotes: string;
  docs: { id: string; filename: string }[];
};

const formOf = (d: Detail): Form => {
  const r = d.record;
  const t = r.termsDays;
  const preset = t == null ? '' : TERMS.some((x) => x.value === String(t)) ? String(t) : 'custom';
  return {
    corporateAccountId: r.corporateAccountId ?? '', customerId: r.customerId ?? '', billToName: r.billToName ?? '', billToAddress: r.billToAddress ?? '',
    billToNpwp: r.billToNpwp ?? '', billToEmail: r.billToEmail ?? '', billToPhone: r.billToPhone ?? '', term: preset, customDays: preset === 'custom' ? String(t) : '',
    customerPo: r.customerPo ?? '', contractRef: r.contractRef ?? '', billingRef: r.billingRef ?? '', notes: r.notes ?? '', internalNotes: r.internalNotes ?? '',
    docs: (r.attachmentFileIds ?? []).map((id, i) => ({ id, filename: `Document ${i + 1}` })),
  };
};

const bodyOf = (f: Form) => ({
  corporateAccountId: opt(f.corporateAccountId), customerId: opt(f.customerId), billToName: opt(f.billToName), billToAddress: opt(f.billToAddress),
  billToNpwp: opt(f.billToNpwp), billToEmail: opt(f.billToEmail), billToPhone: opt(f.billToPhone),
  termsDays: f.term === 'custom' ? (f.customDays ? Number(f.customDays) : undefined) : f.term ? Number(f.term) : undefined,
  customerPo: opt(f.customerPo), contractRef: opt(f.contractRef), billingRef: opt(f.billingRef), notes: opt(f.notes), internalNotes: opt(f.internalNotes),
  attachmentFileIds: f.docs.length ? f.docs.map((x) => x.id) : undefined,
});

/** Prepare Billing: bill-to, payment term, references, charges & adjustments, calculation and validation of one folio. */
export function PrepareBillingPage() {
  const { id = '' } = useParams();
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const toast = useToast();
  const { can } = useAuth();
  const path = `${API}/billing-records/${id}`;
  const d = useGet<Detail>(path);
  const [form, setForm] = useState<Form | null>(null);
  const [saved, setSaved] = useState('');
  const [adjust, setAdjust] = useState<Schemas['BillingLine'] | null>(null);
  const [modal, setModal] = useState<'' | 'preview' | 'split' | 'generate' | 'resolve'>(params.get('preview') ? 'preview' : '');
  const [code, setCode] = useState<string | undefined>();
  useEffect(() => {
    if (d.data && form === null) {
      const f = formOf(d.data);
      setForm(f);
      setSaved(JSON.stringify(bodyOf(f)));
    }
  }, [d.data, form]);
  const save = useSend<Record<string, unknown>, Detail>('PUT', path, INVALIDATE);
  const ready = useSend<Record<string, unknown>, Detail>('POST', `${path}:mark-ready`, INVALIDATE);
  const approve = useSend<Record<string, unknown>, Detail>('POST', `${path}:approve`, INVALIDATE);
  const apply = useSend<{ id: string }>('POST', (v) => `${API}/deposits/${v.id}:apply`, INVALIDATE);
  const x = d.data;
  const it = x?.item;
  const editable = can(PREPARE) && !!it && !['invoiced', 'cancelled', 'settled'].includes(it.status);
  const dirty = !!form && JSON.stringify(bodyOf(form)) !== saved;
  const back = () => navigate('/accounting/revenue?tab=billing');
  const f = form;
  const setF = (patch: Partial<Form>) => f && setForm({ ...f, ...patch });
  const doSave = async () => {
    if (!f) return;
    await save.mutateAsync(bodyOf(f));
    setSaved(JSON.stringify(bodyOf(f)));
  };

  if (!x || !it || !f) return <div className="oc-stack"><PageHeader title="Prepare Billing" />{d.isLoading ? <Skeleton /> : <ErrorAlert error={d.error} />}</div>;
  const days = f.term === 'custom' ? Number(f.customDays || 0) : f.term ? Number(f.term) : it.termsDays;

  return (
    <div className="oc-stack">
      <PageHeader title={`Prepare Billing · ${it.number}`} help="Review who is billed, the payment term and the charges, then mark the billing ready to invoice."
        actions={<button className="oc-btn oc-btn-text" onClick={back}><Icon name="arrow_back" size={18} /> Back to Billing</button>} />
      <div className="oc-workspace">
        <div className="oc-stack">
          <Card title="Folio" icon="receipt" actions={<BillingStatus status={it.status} />}>
            <KV items={[['Folio', <>{it.number} · {label(it.folioStatus)}</>],
              ['Source', <>{SOURCE[it.sourceType] ?? label(it.sourceType)}{it.sourceRef ? ` · ${it.sourceRef}` : ''}{sourceUrl(it) && <> · <Link to={sourceUrl(it)!}>View source</Link></>}</>],
              ['Customer', <>{it.customerName ?? it.holderName}{it.customerId && <> · <Link to={`/crm/customers/${it.customerId}`}>View customer</Link></>}</>],
              ['Billing date', `${formatDate(x.businessDate)} (business date)`], ['Owner', it.ownerName ?? 'Unassigned'],
              ...(it.invoiceId ? [['Invoice', <Link key="i" to={invoiceUrl(it.invoiceId)}>{it.invoiceNumber ?? 'Draft'}</Link>] as [string, React.ReactNode]] : [])]} />
          </Card>

          <Card title="Customer & Bill To" icon="badge">
            <p className="oc-small" style={{ marginTop: 0 }}>Billed to: <strong>{it.billTo || 'nobody yet'}</strong>
              {it.billToKind !== 'none' && <span className="oc-muted"> ({it.billToKind === 'company' ? 'company' : 'customer'})</span>}</p>
            <fieldset className="oc-form" disabled={!editable} style={{ border: 0, padding: 0, margin: 0 }}>
              <CorporatePicker value={f.corporateAccountId} onChange={(v) => setF({ corporateAccountId: v })} />
              <CustomerPicker value={f.customerId} onChange={(v) => setF({ customerId: v })} label="Customer (when no company)" />
              <TextField label="Bill To" value={f.billToName} onChange={(v) => setF({ billToName: v })} placeholder={it.billTo || 'From the customer or company'} />
              <TextField label="NPWP" value={f.billToNpwp} onChange={(v) => setF({ billToNpwp: v })} placeholder="From the company" help="Needed for the tax invoice (e-Faktur)" />
              <TextField label="Billing address" value={f.billToAddress} onChange={(v) => setF({ billToAddress: v })} placeholder="From the customer or company" span />
              <TextField label="E-mail" type="email" value={f.billToEmail} onChange={(v) => setF({ billToEmail: v })} placeholder="From the customer or company" />
              <TextField label="Phone" value={f.billToPhone} onChange={(v) => setF({ billToPhone: v })} />
              <SelectField label="Payment term" value={f.term} onChange={(v) => setF({ term: v })} options={TERMS} />
              {f.term === 'custom' && <TextField label="Days" type="number" value={f.customDays} onChange={(v) => setF({ customDays: v })} />}
            </fieldset>
            <p className="oc-small oc-muted" style={{ marginBottom: 0 }}>Empty fields come from the customer or company master data. The folio's company is billed unless another company is chosen.</p>
          </Card>

          <Card title="Charges" icon="list_alt">
            <ChargeTable lines={x.lines} onAdjust={editable && it.folioStatus === 'open' ? setAdjust : undefined} />
            <p className="oc-small oc-muted" style={{ marginBottom: 0 }}>Adjustments post a correcting line linked to the charge; the original is kept. Adjustments by an accountant wait for the Finance Manager's approval.</p>
          </Card>
          {x.adjustments.length > 0 && <Card title="Adjustments" icon="tune"><Adjustments rows={x.adjustments} /></Card>}
          {x.deposits.length > 0 && <Card title="Deposit / Advance" icon="savings">
            <DataTable rows={x.deposits as unknown as R[]} pageSize={0} columns={[{ key: 'number', header: 'Deposit' },
              { key: 'createdAt', header: 'Received', render: (r) => formatDate(String(r.createdAt)) },
              { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) },
              { key: 'appliedAmount', header: 'Applied', align: 'right', render: (r) => money(r.appliedAmount) },
              { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]}
              actions={(r) => (r.status === 'held' && it.folioStatus === 'open' && can('billing.payment.create')
                ? <button className="oc-btn oc-btn-sm oc-btn-neutral" disabled={apply.isPending} onClick={() => apply.mutate({ id: String(r.id) }, { onSuccess: () => toast('Deposit applied') })}>Apply</button>
                : null)} />
            <p className="oc-small oc-muted" style={{ marginBottom: 0 }}>Held deposits are applied to the folio when the invoice is issued, linked to the original payment.</p>
          </Card>}

          <Card title="References" icon="tag">
            <fieldset className="oc-form" disabled={!editable} style={{ border: 0, padding: 0, margin: 0 }}>
              <TextField label="Customer PO" value={f.customerPo} onChange={(v) => setF({ customerPo: v })} />
              <TextField label="Contract" value={f.contractRef} onChange={(v) => setF({ contractRef: v })} />
              <TextField label="Billing reference" value={f.billingRef} onChange={(v) => setF({ billingRef: v })} placeholder={it.sourceRef ?? ''} />
            </fieldset>
          </Card>
          <Card title="Notes & Supporting Documents" icon="attach_file">
            <fieldset className="oc-form" disabled={!editable} style={{ border: 0, padding: 0, margin: 0 }}>
              <TextArea label="Customer notes" value={f.notes} onChange={(v) => setF({ notes: v })} help="Printed on the invoice" />
              <TextArea label="Internal notes" value={f.internalNotes} onChange={(v) => setF({ internalNotes: v })} help="Never shown to the customer" />
            </fieldset>
            {editable && <InvoiceDocuments docs={f.docs} onChange={(docs) => setF({ docs })} />}
          </Card>
          <Card title="Billing History" icon="history"><History rows={x.history} /></Card>
        </div>

        <aside className="oc-workspace-aside oc-stack">
          <Card title="Amount to Invoice" icon="calculate">
            <Breakdown b={x.breakdown} item={it} />
            <div className="oc-row" style={{ marginTop: 8 }}><span className="oc-small oc-muted">Payment term</span><span className="oc-spacer" />
              <span className="oc-small">{days ? `Net ${days} days` : 'Immediate'}</span></div>
          </Card>
          <Card title="Validation" icon="checklist">
            <Checklist checks={x.checks} />
            {it.exceptions.length > 0 && <div style={{ marginTop: 12 }}>
              <ExceptionList item={it} onResolve={(c) => { setCode(c); setModal('resolve'); }} /></div>}
            {x.approval && <p className="oc-small oc-muted" style={{ marginBottom: 0 }}>
              {x.approval === 'executive' ? 'Approved by the General Manager / Director.' : 'Approved by the Finance Manager.'}
              {x.record.approvedAt && ` Approved ${formatDateTime(x.record.approvedAt)} by ${x.record.approvedByName ?? ''}.`}</p>}
          </Card>
          <Card title="Actions" icon="bolt">
            <div className="oc-stack" style={{ gap: 8 }}>
              {editable && <button className="oc-btn oc-btn-neutral" disabled={!dirty || save.isPending} onClick={() => void doSave().then(() => toast('Billing saved'))}>Save Billing</button>}
              {editable && it.status === 'pending_billing' && <button className="oc-btn oc-btn-primary" disabled={save.isPending || ready.isPending}
                onClick={() => void doSave().then(() => ready.mutateAsync({})).then(() => toast('Marked ready to invoice'))}>Mark Ready to Invoice</button>}
              {it.exceptions.some((e) => e.code === 'approval_required' && !e.resolved) && can(APPROVE) &&
                <button className="oc-btn oc-btn-primary" disabled={approve.isPending} onClick={() => approve.mutate({}, { onSuccess: () => toast('Billing approved') })}>Approve Billing</button>}
              {it.canInvoice && ['pending_billing', 'ready_to_invoice'].includes(it.status) && can('billing.invoice.issue') &&
                <button className={`oc-btn ${it.status === 'ready_to_invoice' ? 'oc-btn-primary' : 'oc-btn-neutral'}`} disabled={dirty}
                  title={dirty ? 'Save the billing first' : undefined} onClick={() => setModal('generate')}>Generate Invoice</button>}
              {!['invoiced', 'settled'].includes(it.status) && <button className="oc-btn oc-btn-neutral" onClick={() => setModal('preview')}>Preview Invoice</button>}
              {editable && it.folioStatus === 'open' && <button className="oc-btn oc-btn-text" onClick={() => setModal('split')}>Split Billing</button>}
            </div>
            <ErrorAlert error={save.error ?? ready.error ?? approve.error ?? apply.error} />
          </Card>
        </aside>
      </div>
      {adjust && <AdjustModal folioId={id} line={adjust} onClose={() => setAdjust(null)} />}
      {modal === 'preview' && f && <PreviewModal folioId={id} body={bodyOf(f)} onClose={() => setModal('')} />}
      {modal === 'split' && <SplitModal item={it} onClose={() => setModal('')} />}
      {modal === 'generate' && <GenerateModal items={[it]} onClose={() => setModal('')} onReview={() => setModal('resolve')} />}
      {modal === 'resolve' && <ResolveModal folioId={id} code={code} onClose={() => setModal('')} />}
    </div>
  );
}

/** A billing adjustment of one charge (quantity, price, discount, service charge, tax, revenue allocation). */
function AdjustModal({ folioId, line, onClose }: { folioId: string; line: Schemas['BillingLine']; onClose: () => void }) {
  const toast = useToast();
  const [kind, setKind] = useState('discount');
  const [value, setValue] = useState('');
  const [bl, setBl] = useState(line.businessLine);
  const [comp, setComp] = useState(line.revenueComponent);
  const [reason, setReason] = useState('');
  const [reference, setReference] = useState('');
  const send = useSend<Record<string, unknown>>('POST', `${API}/billing-records/${folioId}/adjustments`, INVALIDATE);
  const current: Record<string, string> = { quantity: line.quantity, price: line.unitPrice, service_charge: line.serviceAmount, tax: line.taxAmount, discount: '0' };
  const body = { lineId: line.id, kind, reason, reference: opt(reference),
    ...(kind === 'quantity' ? { quantity: value } : kind === 'price' ? { unitPrice: value } : kind === 'revenue_allocation' ? { businessLine: bl, revenueComponent: comp } : { amount: value }) };
  const valueLabel: Record<string, string> = { quantity: 'New quantity', price: 'New unit price', discount: 'Discount (before tax)', service_charge: 'New service charge', tax: 'New tax' };
  return (
    <Modal open onClose={onClose} title={`Adjust · ${line.description}`} actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!reason.trim() || (kind !== 'revenue_allocation' && !value) || send.isPending}
        onClick={() => send.mutate(body, { onSuccess: () => { toast('Adjustment posted'); onClose(); } })}>Apply Adjustment</button></>}>
      <div className="oc-form">
        <SelectField label="Adjust" value={kind} onChange={(v) => { setKind(v); setValue(''); }} options={ADJUST_KINDS} />
        {kind !== 'revenue_allocation'
          ? <TextField label={valueLabel[kind]} value={value} onChange={setValue} inputMode="decimal"
            help={kind === 'discount' ? `Net of the charge ${money(line.netAmount)}; service and tax follow` : `Now ${kind === 'quantity' ? current[kind] : money(current[kind])}`} />
          : <>
            <SelectField label="Business line" value={bl} onChange={(v) => { setBl(v); setComp((COMPONENTS[v] ?? ['other'])[0]); }} options={BUSINESS_LINES} />
            <SelectField label="Revenue component" value={comp} onChange={setComp}
              options={[...new Set([...(COMPONENTS[bl] ?? []), comp])].map((c) => ({ value: c, label: label(c) }))} />
          </>}
        <TextArea label="Reason" value={reason} onChange={setReason} required />
        <TextField label="Reference" value={reference} onChange={setReference} placeholder="Approval memo, complaint, PO …" />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

/** Preview Invoice: what Generate Invoice would issue (nothing saved). */
function PreviewModal({ folioId, body, onClose }: { folioId: string; body: Record<string, unknown>; onClose: () => void }) {
  const pv = useSend<Record<string, unknown>, R & { lines: R[] }>('POST', `${API}/invoices:preview`);
  const { mutate } = pv;
  const key = JSON.stringify(body);
  useEffect(() => {
    const b = JSON.parse(key) as Record<string, unknown>;
    mutate({ folioId, corporateAccountId: b.corporateAccountId, customerId: b.customerId, termsDays: b.termsDays,
      billTo: { name: b.billToName, address: b.billToAddress, npwp: b.billToNpwp, email: b.billToEmail, phone: b.billToPhone } });
  }, [folioId, key, mutate]);
  const x = pv.data;
  return (
    <Modal open onClose={onClose} title="Preview Invoice" wide actions={<button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button>}>
      {pv.isPending && <Skeleton />}
      <ErrorAlert error={pv.error} />
      {x && <div className="oc-stack">
        <KV items={[['Bill to', String(x.billToName)], ['NPWP', String(x.billToNpwp ?? '—')], ['Address', String(x.billToAddress ?? '—')],
          ['Payment term', Number(x.termsDays) ? `Net ${String(x.termsDays)} days` : 'Immediate']]} />
        <DataTable rows={x.lines} pageSize={0} columns={[{ key: 'description', header: 'Description' }, { key: 'quantity', header: 'Qty', align: 'right' },
          { key: 'netAmount', header: 'Net', align: 'right', render: (l) => money(l.netAmount) }, { key: 'total', header: 'Total', align: 'right', render: (l) => money(l.total) }]} />
        <KV items={[['Subtotal', money(x.subtotal)], ['Service', money(x.serviceAmount)], ['Tax', money(x.taxAmount)], ['Total', <strong key="t">{money(x.total)}</strong>]]} />
        <p className="oc-small oc-muted" style={{ margin: 0 }}>The number, invoice date and due date are assigned at issue.</p>
      </div>}
    </Modal>
  );
}
