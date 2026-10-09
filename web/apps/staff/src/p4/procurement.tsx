import React, { useState } from 'react';
import { Link, useSearchParams } from 'react-router';
import { download, qs, request, uuidv7, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate, formatDateTime } from '@oneclub/i18n';
import { enqueue } from '@oneclub/offline';
import {
  AutoResourcePage, Card, Checkbox, DataTable, Drawer, ErrorAlert, Icon, Modal, MoneyField, PageHeader, SelectField, Skeleton, StatusPill, TextArea,
  TextField, useAuth, useToast, type Option,
} from '@oneclub/shell';
import { ActionButton, KV, ListPage, Tabs, money, today, type R } from '../p1/common';
import { KPIDashboardPage } from '../p2';
import type { AreaRoute, OpsRoute, OpsTile } from '../p3/types';
import { ScanField } from './inventory';

// Procurement (PRD P4 EP-10–15, Naming Convention §20): Suppliers (master,
// contacts, bank accounts, documents, price lists, blacklist), Purchase
// Requisitions (manual, reorder, banquet BEO, store requisition), Approvals,
// RFQ with the quotation comparison, Vendor Quotations, Purchase Orders
// (approval, send, revise, cancel, close, service confirmation), Goods
// Receipts and Purchase Returns, Vendor Invoices with 3-way matching, debit
// notes and holds, Vendor Performance, Procurement Migration and Reports.
// The `ops` Warehouse workstation gets Goods Receipt with barcode / camera
// scanning, manual entry and an offline queue (EP-26 FR-OPS-P4-01/04).

const PRC = ['/api/v1/procurement'];
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const pill = (k: string) => (r: R) => <StatusPill status={String(r[k] ?? '').replace(/_/g, '-')} label={label(r[k])} />;
const opts = (vals: string[]): Option[] => vals.map((v) => ({ value: v, label: label(v) }));
const val = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : String(v));
const date = (v: unknown) => (v ? formatDate(String(v)) : '—');
const rows = (v: unknown) => ((v as R[] | undefined) ?? []).map((x, i) => ({ ...x, id: String(x.id ?? i) } as R));
const idem = () => ({ 'Idempotency-Key': uuidv7() });

const PR_STATUS = opts(['draft', 'submitted', 'approved', 'rejected', 'partially_ordered', 'ordered', 'cancelled']);
const PO_STATUS = opts(['draft', 'pending_approval', 'approved', 'sent', 'partially_received', 'received', 'closed', 'cancelled']);
const VI_STATUS = opts(['draft', 'matched', 'mismatch', 'on_hold', 'approved', 'partially_paid', 'paid', 'cancelled']);
const DOC_TYPES = ['purchase_requisition', 'purchase_order', 'vendor_quotation_selection', 'goods_receipt_exception', 'vendor_invoice',
  'vendor_invoice_override', 'supplier_status_change'];

function useOptions(path: string, text: (r: R) => string): Option[] {
  const l = useGet<Page<R>>(path);
  return (l.data?.items ?? []).map((x) => ({ value: x.id, label: text(x) }));
}
const useSuppliers = () => useOptions('/api/v1/procurement/suppliers?limit=500&filter[status]=active', (x) => `${String(x.name)} (${String(x.code)})`);
const useWarehouses = () => useOptions('/api/v1/inventory/warehouses?limit=500&filter[status]=active', (x) => `${String(x.name)} (${String(x.code)})`);
const useItems = () => useOptions('/api/v1/inventory/items?limit=500&filter[status]=active', (x) => `${String(x.name)} (${String(x.code)})`);
const useUoms = () => useOptions('/api/v1/inventory/uoms?limit=500', (x) => String(x.code));

/** Opens the document of `?open=<id>` (notification links) in a drawer. */
function useOpen(): [string | null, (id: string | null) => void] {
  const [params, setParams] = useSearchParams();
  return [params.get('open'), (id) => setParams(id ? { open: id } : {})];
}

// ── document lines editor ─────────────────────────────────────────────────

type Line = { itemId: string; description: string; quantity: string; uomId: string; price: string; tax: string; supplierId: string; neededBy: string };
const emptyLine = (): Line => ({ itemId: '', description: '', quantity: '', uomId: '', price: '', tax: '', supplierId: '', neededBy: '' });

function LinesEditor({ lines, onChange, priceLabel = 'Estimated price', tax, supplier, needed }: {
  lines: Line[]; onChange: (l: Line[]) => void; priceLabel?: string; tax?: boolean; supplier?: boolean; needed?: boolean;
}) {
  const items = useItems();
  const uoms = useUoms();
  const sups = useSuppliers();
  const set = (i: number, k: keyof Line, v: string) => onChange(lines.map((l, j) => (j === i ? { ...l, [k]: v } : l)));
  return (
    <div className="oc-stack" style={{ gap: 8 }}>
      {lines.map((l, i) => (
        <div key={i} className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
          <SelectField label={`Item ${i + 1}`} value={l.itemId} onChange={(v) => set(i, 'itemId', v)} options={items} placeholder="Service / non-stock" />
          {!l.itemId && <TextField label="Description" value={l.description} onChange={(v) => set(i, 'description', v)} />}
          <TextField label="Quantity" type="number" value={l.quantity} onChange={(v) => set(i, 'quantity', v)} />
          {l.itemId && <SelectField label="UOM" value={l.uomId} onChange={(v) => set(i, 'uomId', v)} options={uoms} placeholder="Purchase UOM" />}
          <TextField label={priceLabel} type="number" value={l.price} onChange={(v) => set(i, 'price', v)} placeholder="Price list" />
          {tax && <TextField label="PPN %" type="number" value={l.tax} onChange={(v) => set(i, 'tax', v)} placeholder="Default" />}
          {supplier && <SelectField label="Suggested supplier" value={l.supplierId} onChange={(v) => set(i, 'supplierId', v)} options={sups} placeholder="None" />}
          {needed && <TextField label="Needed by" type="date" value={l.neededBy} onChange={(v) => set(i, 'neededBy', v)} />}
          <button className="oc-btn oc-btn-text" aria-label={`Remove line ${i + 1}`} onClick={() => onChange(lines.filter((_, j) => j !== i))}><Icon name="delete" size={18} /></button>
        </div>
      ))}
      <div><button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => onChange([...lines, emptyLine()])}>Add line</button></div>
    </div>
  );
}

const optional = (k: string, v: string) => (v ? { [k]: v } : {});
const requisitionLines = (lines: Line[]) => lines.filter((l) => l.quantity && (l.itemId || l.description)).map((l) => ({
  quantity: l.quantity, ...optional('itemId', l.itemId), ...optional('description', l.description), ...optional('uomId', l.uomId),
  ...optional('estimatedUnitPrice', l.price), ...optional('suggestedSupplierId', l.supplierId), ...optional('neededBy', l.neededBy),
}));
const rfqLines = (lines: Line[]) => lines.filter((l) => l.quantity && (l.itemId || l.description)).map((l) => ({
  quantity: l.quantity, ...optional('itemId', l.itemId), ...optional('description', l.description), ...optional('uomId', l.uomId),
  ...optional('estimatedUnitPrice', l.price), ...optional('neededBy', l.neededBy),
}));
const orderLines = (lines: Line[]) => lines.filter((l) => l.quantity && (l.itemId || l.description)).map((l) => ({
  quantity: l.quantity, ...optional('itemId', l.itemId), ...optional('description', l.description), ...optional('uomId', l.uomId),
  ...optional('unitPrice', l.price), ...optional('taxPercent', l.tax),
}));

// ── Suppliers (EP-10) ─────────────────────────────────────────────────────

const SUPPLIER_TABS: Option[] = [
  { value: 'procurement.supplier', label: 'Suppliers' }, { value: 'procurement.supplier_contact', label: 'Contacts' },
  { value: 'procurement.supplier_address', label: 'Addresses' }, { value: 'procurement.supplier_bank_account', label: 'Bank Accounts' },
  { value: 'procurement.supplier_document', label: 'Documents' }, { value: 'procurement.supplier_item', label: 'Supplier Items & Price Lists' },
  { value: 'blacklist', label: 'Blacklist' },
];

export function SuppliersPage() {
  const [tab, setTab] = useState('procurement.supplier');
  return (
    <div className="oc-stack">
      <PageHeader title="Suppliers" help="Supplier master: NPWP, PKP, PPh withholding, payment terms, supply categories, contacts receiving RFQ / PO, bank accounts (masked), documents and price lists." />
      <Tabs tabs={SUPPLIER_TABS} value={tab} onChange={setTab} />
      {tab === 'blacklist' ? <BlacklistPanel /> : <AutoResourcePage key={tab} resourceKey={tab} />}
    </div>
  );
}

function BlacklistPanel() {
  const { can } = useAuth();
  const sups = useOptions('/api/v1/procurement/suppliers?limit=500', (x) => `${String(x.name)} (${String(x.code)}) · ${label(x.status)}`);
  const [sid, setSid] = useState('');
  const [action, setAction] = useState('block');
  const [reason, setReason] = useState('');
  const send = useSend<Record<string, string>, R>('POST', () => `/api/v1/procurement/suppliers/${sid}:${action}`, PRC);
  return (
    <div className="oc-stack">
      {can('procurement.supplier.block') && (
        <Card title="Block / unblock a supplier" icon="block">
          <p className="oc-small oc-muted">A blocked supplier cannot receive RFQs or purchase orders. The change takes effect once approved.</p>
          <ErrorAlert error={send.error} />
          <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
            <SelectField label="Supplier" value={sid} onChange={setSid} options={sups} placeholder="Select" />
            <SelectField label="Action" value={action} onChange={setAction} options={opts(['block', 'unblock'])} />
            <TextField label="Reason" value={reason} onChange={setReason} required />
            <button className="oc-btn oc-btn-primary" disabled={!sid || !reason || send.isPending}
              onClick={() => send.mutate({ reason }, { onSuccess: () => setReason('') })}>Request {action}</button>
          </div>
        </Card>
      )}
      <ListPage title="Block / unblock requests" path="/api/v1/procurement/supplier-status-requests" search={false}
        statuses={opts(['pending', 'approved', 'rejected', 'cancelled'])}
        columns={[{ key: 'createdAt', header: 'Requested', render: (r) => formatDateTime(String(r.createdAt)) }, { key: 'action', header: 'Action', render: (r) => label(r.action) },
          { key: 'reason', header: 'Reason' }, { key: 'status', header: 'Status', render: pill('status') },
          { key: 'supplierStatus', header: 'Supplier now', render: (r) => label(r.supplierStatus) }]} />
    </div>
  );
}

// ── Purchase Requisitions (EP-11) ─────────────────────────────────────────

export function RequisitionsPage() {
  const { can } = useAuth();
  const [open, setOpen] = useOpen();
  const [creating, setCreating] = useState(false);
  const [source, setSource] = useState('');
  const [tab, setTab] = useState('requisitions');
  return (
    <div className="oc-stack">
      <Tabs tabs={[{ value: 'requisitions', label: 'Purchase Requisitions' }, { value: 'lines', label: 'Open lines to order' }]} value={tab} onChange={setTab} />
      {tab === 'requisitions' ? (
        <ListPage title="Purchase Requisitions" help="Manual, reorder-based (inventory reorder point), banquet material (BEO) and store requisitions the store cannot supply; approval by amount, category and cost center."
          path="/api/v1/procurement/requisitions" statuses={PR_STATUS} extraQuery={{ 'filter[source]': source }} onRowClick={(r) => setOpen(r.id)}
          filters={<SelectField label="Source" value={source} onChange={setSource} options={opts(['manual', 'reorder', 'banquet', 'store_requisition'])} placeholder="All sources" />}
          actions={can('procurement.requisition.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setCreating(true)}>New Requisition</button> : undefined}
          columns={[{ key: 'number', header: 'Requisition' }, { key: 'source', header: 'Source', render: (r) => label(r.source) }, { key: 'title', header: 'Title', render: (r) => val(r.title) },
            { key: 'neededBy', header: 'Needed by', render: (r) => date(r.neededBy) }, { key: 'estimatedTotal', header: 'Estimated', align: 'right', render: (r) => money(r.estimatedTotal) },
            { key: 'ageDays', header: 'Age (days)', align: 'right' }, { key: 'status', header: 'Status', render: pill('status') },
            { key: 'attention', header: 'Attention', render: (r) => (r.attention ? <StatusPill status="warning" label="Check" /> : '—') }]} />
      ) : <OpenLinesPanel />}
      {creating && <RequisitionModal onClose={() => setCreating(false)} onDone={(id) => { setCreating(false); setOpen(id); }} />}
      {open && <RequisitionDrawer id={open} onClose={() => setOpen(null)} />}
    </div>
  );
}

function RequisitionModal({ onClose, onDone }: { onClose: () => void; onDone: (id: string) => void }) {
  const whs = useWarehouses();
  const [f, setF] = useState({ title: '', warehouseId: '', costCenter: '', category: '', neededBy: '', notes: '' });
  const [lines, setLines] = useState<Line[]>([emptyLine()]);
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/procurement/requisitions', PRC, idem);
  const set = (k: keyof typeof f) => (v: string) => setF({ ...f, [k]: v });
  const go = (submit: boolean) => send.mutate({ ...Object.fromEntries(Object.entries(f).filter(([, v]) => v)), lines: requisitionLines(lines), submit },
    { onSuccess: (r) => onDone(r.id) });
  return (
    <Modal open onClose={onClose} title="New Purchase Requisition" wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-neutral" disabled={send.isPending} onClick={() => go(false)}>Save draft</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => go(true)}>Submit for approval</button>
    </>}>
      <ErrorAlert error={send.error} />
      <div className="oc-row-wrap">
        <TextField label="Title" value={f.title} onChange={set('title')} />
        <SelectField label="Destination warehouse" value={f.warehouseId} onChange={set('warehouseId')} options={whs} placeholder="Select" />
        <TextField label="Cost center" value={f.costCenter} onChange={set('costCenter')} />
        <TextField label="Category" value={f.category} onChange={set('category')} />
        <TextField label="Needed by" type="date" value={f.neededBy} onChange={set('neededBy')} />
      </div>
      <LinesEditor lines={lines} onChange={setLines} supplier needed />
      <TextArea label="Notes" value={f.notes} onChange={set('notes')} rows={2} />
    </Modal>
  );
}

function RequisitionDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const q = useGet<R>(`/api/v1/procurement/requisitions/${id}`);
  const d = q.data;
  const base = `/api/v1/procurement/requisitions/${id}`;
  const st = String(d?.status ?? '');
  return (
    <Drawer open onClose={onClose} title={`Purchase Requisition ${String(d?.number ?? '')}`}>
      {!d ? <Skeleton /> : <>
        {d.attention ? <div role="alert" className="oc-alert oc-alert-warning">{String(d.attention)}</div> : null}
        <KV items={[['Status', label(st)], ['Source', label(d.source)], ['Reference', val(d.sourceRef)], ['Title', val(d.title)], ['Cost center', val(d.costCenter)],
          ['Needed by', date(d.neededBy)], ['Event date', date(d.eventDate)], ['Estimated', money(d.estimatedTotal, String(d.currency))],
          ['Rejected', val(d.rejectedReason)], ['Version', String(d.version)]]} />
        <DataTable rows={rows(d.lines)} columns={[{ key: 'lineNo', header: '#' }, { key: 'description', header: 'Item / service' },
          { key: 'quantity', header: 'Qty', align: 'right', render: (l) => `${String(l.quantity)} ${val(l.uom)}` },
          { key: 'estimatedUnitPrice', header: 'Est. price', align: 'right', render: (l) => money(l.estimatedUnitPrice) },
          { key: 'orderedQuantity', header: 'Ordered', align: 'right' }, { key: 'neededBy', header: 'Needed by', render: (l) => date(l.neededBy) },
          { key: 'status', header: 'Line', render: (l) => label(l.status) }]} />
        <div className="oc-row-wrap">
          {st === 'draft' && can('procurement.requisition.submit') && <ActionButton label="Submit for approval" kind="primary" path={`${base}:submit`} invalidate={PRC} />}
          {st === 'submitted' && can('procurement.requisition.approve') && <>
            <ActionButton label="Approve" kind="primary" path={`${base}:approve`} body={{}} invalidate={PRC} />
            <ActionButton label="Reject" path={`${base}:reject`} invalidate={PRC} reason="required" danger />
          </>}
          {['draft', 'submitted', 'approved', 'rejected'].includes(st) && can('procurement.requisition.cancel') && (
            <ActionButton label="Cancel" path={`${base}:cancel`} invalidate={PRC} reason="required" danger />
          )}
        </div>
        {st === 'rejected' && can('procurement.requisition.update') && (
          <ActionButton label="Back to draft for correction" method="PATCH" path={base} body={{}} invalidate={PRC} />
        )}
      </>}
    </Drawer>
  );
}

/** Approved requisition lines: consolidated into an RFQ or purchase orders (FR-PR-05). */
function OpenLinesPanel() {
  const { can } = useAuth();
  const sups = useSuppliers();
  const lines = useGet<Page<R>>('/api/v1/procurement/requisition-lines?limit=500');
  const [sel, setSel] = useState<Record<string, boolean>>({});
  const [rfq, setRfq] = useState(false);
  const [supplier, setSupplier] = useState('');
  const [skip, setSkip] = useState('');
  const ids = Object.keys(sel).filter((k) => sel[k]);
  const po = useSend<Record<string, unknown>, Page<R>>('POST', '/api/v1/procurement/purchase-orders:from-requisitions', PRC, idem);
  const toast = useToast();
  return (
    <div className="oc-stack">
      <PageHeader title="Open requisition lines" help="Select approved lines to consolidate: one RFQ line per item, or one purchase order per supplier." />
      <ErrorAlert error={po.error} />
      <DataTable rows={lines.data?.items} loading={lines.isLoading} error={lines.error}
        columns={[{ key: 'sel', header: 'Select', render: (l) => <Checkbox label={`Select ${String(l.description)}`} checked={!!sel[l.id]} onChange={(v) => setSel({ ...sel, [l.id]: v })} /> },
          { key: 'description', header: 'Item / service' }, { key: 'openQuantity', header: 'Open qty', align: 'right', render: (l) => `${String(l.openQuantity)} ${val(l.uom)}` },
          { key: 'neededBy', header: 'Needed by', render: (l) => date(l.neededBy) }, { key: 'estimatedUnitPrice', header: 'Est. price', align: 'right', render: (l) => money(l.estimatedUnitPrice) }]} />
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        {can('procurement.rfq.create') && <button className="oc-btn oc-btn-neutral" disabled={!ids.length} onClick={() => setRfq(true)}>Create RFQ</button>}
        {can('procurement.purchase_order.create') && <>
          <SelectField label="Supplier (default: suggested)" value={supplier} onChange={setSupplier} options={sups} placeholder="Suggested per line" />
          <TextField label="RFQ skip reason (above threshold)" value={skip} onChange={setSkip} />
          <button className="oc-btn oc-btn-primary" disabled={!ids.length || po.isPending} onClick={() => po.mutate({ requisitionLineIds: ids, ...optional('supplierId', supplier),
            ...optional('rfqSkipReason', skip) }, { onSuccess: (r) => { toast(`${r.items.length} purchase order(s) created`); setSel({}); } })}>Create purchase orders</button>
        </>}
      </div>
      {rfq && <RFQModal requisitionLineIds={ids} onClose={() => setRfq(false)} onDone={() => { setRfq(false); setSel({}); }} />}
    </div>
  );
}

// ── Approvals ─────────────────────────────────────────────────────────────

export function ProcurementApprovalsPage() {
  const inbox = useGet<Page<R>>('/api/v1/platform/approvals?box=inbox&filter[status]=pending&limit=200');
  const items = (inbox.data?.items ?? []).filter((r) => DOC_TYPES.includes(String(r.documentType)));
  return (
    <div className="oc-stack">
      <PageHeader title="Approvals" help="Procurement documents waiting for your approval step (requisitions, purchase orders, quotation selections, receipt exceptions, vendor invoices, supplier blacklist)." />
      <DataTable rows={items} loading={inbox.isLoading} error={inbox.error}
        columns={[{ key: 'title', header: 'Document' }, { key: 'documentType', header: 'Type', render: (r) => label(r.documentType) },
          { key: 'createdAt', header: 'Requested', render: (r) => formatDateTime(String(r.createdAt)) }]}
        actions={(r) => <div className="oc-row-wrap">
          <ActionButton label="Approve" kind="primary" path={`/api/v1/platform/approvals/${r.id}:approve`} body={{}} invalidate={['/api/v1/platform/approvals', ...PRC]} />
          <ActionButton label="Reject" path={`/api/v1/platform/approvals/${r.id}:reject`} invalidate={['/api/v1/platform/approvals', ...PRC]} reason="required" danger />
        </div>} />
    </div>
  );
}

// ── RFQ & Vendor Quotations (EP-12) ───────────────────────────────────────

export function RFQsPage() {
  const { can } = useAuth();
  const [open, setOpen] = useOpen();
  const [creating, setCreating] = useState(false);
  return (
    <div className="oc-stack">
      <ListPage title="RFQ" help="Requests for quotation e-mailed to several suppliers with a response link and deadline; compare the quotations per item and select."
        path="/api/v1/procurement/rfqs" statuses={opts(['draft', 'sent', 'closed', 'awarded', 'cancelled'])} onRowClick={(r) => setOpen(r.id)}
        actions={can('procurement.rfq.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setCreating(true)}>New RFQ</button> : undefined}
        columns={[{ key: 'number', header: 'RFQ' }, { key: 'title', header: 'Title' }, { key: 'responseDueAt', header: 'Due', render: (r) => (r.responseDueAt ? formatDateTime(String(r.responseDueAt)) : '—') },
          { key: 'quotations', header: 'Quotations', align: 'right' }, { key: 'estimatedTotal', header: 'Estimated', align: 'right', render: (r) => money(r.estimatedTotal) },
          { key: 'status', header: 'Status', render: pill('status') }]} />
      {creating && <RFQModal onClose={() => setCreating(false)} onDone={(id) => { setCreating(false); setOpen(id); }} />}
      {open && <RFQDrawer id={open} onClose={() => setOpen(null)} />}
    </div>
  );
}

function RFQModal({ requisitionLineIds, onClose, onDone }: { requisitionLineIds?: string[]; onClose: () => void; onDone: (id: string) => void }) {
  const sups = useSuppliers();
  const [title, setTitle] = useState('');
  const [due, setDue] = useState('');
  const [chosen, setChosen] = useState<Record<string, boolean>>({});
  const [lines, setLines] = useState<Line[]>(requisitionLineIds?.length ? [] : [emptyLine()]);
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/procurement/rfqs', PRC, idem);
  const go = (s: boolean) => send.mutate({ title, supplierIds: Object.keys(chosen).filter((k) => chosen[k]), send: s,
    ...(requisitionLineIds?.length ? { requisitionLineIds } : {}), ...(lines.length ? { lines: rfqLines(lines) } : {}),
    ...(due ? { responseDueAt: new Date(due).toISOString() } : {}) }, { onSuccess: (r) => onDone(r.id) });
  return (
    <Modal open onClose={onClose} title="New RFQ" wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-neutral" disabled={!title || send.isPending} onClick={() => go(false)}>Save draft</button>
      <button className="oc-btn oc-btn-primary" disabled={!title || send.isPending} onClick={() => go(true)}>Send to suppliers</button>
    </>}>
      <ErrorAlert error={send.error} />
      <div className="oc-row-wrap">
        <TextField label="Title" value={title} onChange={setTitle} required />
        <TextField label="Response deadline" type="datetime-local" value={due} onChange={setDue} />
      </div>
      {requisitionLineIds?.length ? <p className="oc-small">{requisitionLineIds.length} requisition line(s), consolidated per item.</p> : <LinesEditor lines={lines} onChange={setLines} />}
      <fieldset className="oc-stack" style={{ border: 0, padding: 0 }}>
        <legend>Invite suppliers (at least 3 above the RFQ threshold)</legend>
        <div className="oc-row-wrap">
          {sups.map((s) => <Checkbox key={s.value} label={s.label} checked={!!chosen[s.value]} onChange={(v) => setChosen({ ...chosen, [s.value]: v })} />)}
        </div>
      </fieldset>
    </Modal>
  );
}

function RFQDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const q = useGet<R>(`/api/v1/procurement/rfqs/${id}`);
  const cmp = useGet<R>(`/api/v1/procurement/rfqs/${id}/comparison`);
  const [quote, setQuote] = useState<R | null>(null);
  const d = q.data;
  const base = `/api/v1/procurement/rfqs/${id}`;
  const st = String(d?.status ?? '');
  return (
    <Drawer open onClose={onClose} title={`RFQ ${String(d?.number ?? '')}`}>
      {!d ? <Skeleton /> : <>
        <KV items={[['Status', label(st)], ['Title', String(d.title)], ['Due', d.responseDueAt ? formatDateTime(String(d.responseDueAt)) : '—'],
          ['Delivery', date(d.deliveryDate)], ['Estimated', money(d.estimatedTotal)]]} />
        <DataTable rows={rows(d.suppliers)} columns={[{ key: 'supplierName', header: 'Supplier' }, { key: 'email', header: 'E-mail', render: (s) => val(s.email) },
          { key: 'status', header: 'Status', render: pill('status') }]}
          actions={(s) => (['draft', 'sent', 'closed'].includes(st) && can('procurement.vendor_quotation.create')
            ? <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setQuote(s)}>Record quotation</button> : null)} />
        <h3>Comparison</h3>
        {(cmp.data?.lines as R[] | undefined)?.map((l) => (
          <Card key={String(l.rfqLineId)} title={`${String(l.description)} · ${String(l.quantity)} ${val(l.uom)}`} icon="compare_arrows">
            <DataTable rows={rows(l.offers).map((o) => ({ ...o, id: String(o.quotationLineId) } as R))}
              columns={[{ key: 'supplierName', header: 'Supplier' }, { key: 'netUnitPrice', header: 'Net price', align: 'right', render: (o) => money(o.netUnitPrice) },
                { key: 'taxPercent', header: 'PPN %' }, { key: 'lineTotal', header: 'Total', align: 'right', render: (o) => money(o.lineTotal) },
                { key: 'leadTimeDays', header: 'Lead (days)', render: (o) => val(o.leadTimeDays) },
                { key: 'flags', header: '', render: (o) => <>{o.cheapest ? <StatusPill status="success" label="Cheapest" /> : null} {o.fastest ? <StatusPill status="info" label="Fastest" /> : null}
                  {o.selected ? <StatusPill status="approved" label="Selected" /> : null}</> }]} />
          </Card>
        ))}
        {cmp.data && <KV items={[['Best total', money(cmp.data.bestTotal)], ['Quotations', String(cmp.data.quotations)]]} />}
        <QuotationsOfRFQ rfqId={id} />
        <div className="oc-row-wrap">
          <a className="oc-btn oc-btn-sm oc-btn-neutral" href={`${base}/pdf`} target="_blank" rel="noreferrer">PDF</a>
          {['draft', 'sent'].includes(st) && can('procurement.rfq.send') && <ActionButton label="Send (e-mail)" kind="primary" path={`${base}:send`} invalidate={PRC} />}
          {st === 'sent' && can('procurement.rfq.send') && <ActionButton label="Close for quotations" path={`${base}:close`} invalidate={PRC} />}
          {!['awarded', 'cancelled'].includes(st) && can('procurement.rfq.cancel') && <ActionButton label="Cancel" path={`${base}:cancel`} invalidate={PRC} reason="required" danger />}
        </div>
        {quote && <QuotationModal rfq={d} supplier={quote} onClose={() => { setQuote(null); void cmp.refetch(); }} />}
      </>}
    </Drawer>
  );
}

function QuotationsOfRFQ({ rfqId }: { rfqId: string }) {
  const list = useGet<Page<R>>(`/api/v1/procurement/vendor-quotations?filter[rfqId]=${rfqId}`);
  return (
    <DataTable rows={list.data?.items} loading={list.isLoading} columns={[{ key: 'number', header: 'Quotation' }, { key: 'supplierName', header: 'Supplier' },
      { key: 'total', header: 'Total', align: 'right', render: (r) => money(r.total) }, { key: 'status', header: 'Status', render: pill('status') }]}
      actions={(r) => (r.status === 'received' ? <SelectQuotationButton q={r} /> : null)} />
  );
}

function SelectQuotationButton({ q }: { q: R }) {
  const { can } = useAuth();
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState('');
  const [createPO, setCreatePO] = useState(true);
  const send = useSend<Record<string, unknown>, R>('POST', `/api/v1/procurement/vendor-quotations/${q.id}:select`, PRC);
  if (!can('procurement.vendor_quotation.select')) return null;
  return (
    <>
      <button className="oc-btn oc-btn-sm oc-btn-primary" onClick={() => setOpen(true)}>Select</button>
      <Modal open={open} onClose={() => setOpen(false)} title={`Select ${String(q.number)}`} actions={<>
        <button className="oc-btn oc-btn-neutral" onClick={() => setOpen(false)}>Cancel</button>
        <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => send.mutate({ ...optional('reason', reason), createPurchaseOrder: createPO },
          { onSuccess: () => setOpen(false) })}>Select</button>
      </>}>
        <ErrorAlert error={send.error} />
        <TextArea label="Reason (required when not the cheapest: approval)" value={reason} onChange={setReason} rows={2} />
        <Checkbox label="Create the draft purchase order" checked={createPO} onChange={setCreatePO} />
      </Modal>
    </>
  );
}

function QuotationModal({ rfq, supplier, onClose }: { rfq: R; supplier: R; onClose: () => void }) {
  const [prices, setPrices] = useState<Record<string, { price: string; disc: string; lead: string }>>({});
  const [ref, setRef] = useState('');
  const [valid, setValid] = useState('');
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/procurement/vendor-quotations', PRC, idem);
  const lines = rows(rfq.lines);
  return (
    <Modal open onClose={onClose} title={`Quotation of ${String(supplier.supplierName)}`} wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => send.mutate({ rfqId: rfq.id, supplierId: supplier.supplierId,
        ...optional('supplierReference', ref), ...optional('validUntil', valid),
        lines: lines.filter((l) => prices[l.id]?.price).map((l) => ({ rfqLineId: l.id, unitPrice: prices[l.id].price, ...optional('discountPercent', prices[l.id].disc),
          ...(prices[l.id].lead ? { leadTimeDays: Number(prices[l.id].lead) } : {}) })) }, { onSuccess: onClose })}>Record</button>
    </>}>
      <ErrorAlert error={send.error} />
      <div className="oc-row-wrap">
        <TextField label="Supplier reference" value={ref} onChange={setRef} />
        <TextField label="Valid until" type="date" value={valid} onChange={setValid} />
      </div>
      {lines.map((l) => {
        const p = prices[l.id] ?? { price: '', disc: '', lead: '' };
        const set = (k: 'price' | 'disc' | 'lead') => (v: string) => setPrices({ ...prices, [l.id]: { ...p, [k]: v } });
        return (
          <div key={l.id} className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
            <span style={{ minWidth: 200 }}>{String(l.description)} · {String(l.quantity)} {val(l.uom)}</span>
            <MoneyField label="Unit price" value={p.price} onChange={set('price')} />
            <TextField label="Discount %" type="number" value={p.disc} onChange={set('disc')} />
            <TextField label="Lead time (days)" type="number" value={p.lead} onChange={set('lead')} />
          </div>
        );
      })}
    </Modal>
  );
}

export function VendorQuotationsPage() {
  const [open, setOpen] = useOpen();
  return (
    <div className="oc-stack">
      <ListPage title="Vendor Quotations" help="Prices, discount, PPN, lead time, payment terms and validity per supplier; recorded by the buyer or submitted through the supplier link."
        path="/api/v1/procurement/vendor-quotations" statuses={opts(['received', 'pending_approval', 'selected', 'not_selected', 'cancelled'])} onRowClick={(r) => setOpen(r.id)}
        columns={[{ key: 'number', header: 'Quotation' }, { key: 'rfqNumber', header: 'RFQ', render: (r) => val(r.rfqNumber) }, { key: 'supplierName', header: 'Supplier' },
          { key: 'quotationDate', header: 'Date', render: (r) => date(r.quotationDate) }, { key: 'validUntil', header: 'Valid until', render: (r) => date(r.validUntil) },
          { key: 'total', header: 'Total', align: 'right', render: (r) => money(r.total) }, { key: 'source', header: 'Source', render: (r) => label(r.source) },
          { key: 'status', header: 'Status', render: pill('status') }]} />
      {open && <QuotationDrawer id={open} onClose={() => setOpen(null)} />}
    </div>
  );
}

function QuotationDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const q = useGet<R>(`/api/v1/procurement/vendor-quotations/${id}`);
  const d = q.data;
  return (
    <Drawer open onClose={onClose} title={`Vendor Quotation ${String(d?.number ?? '')}`}>
      {!d ? <Skeleton /> : <>
        <KV items={[['Supplier', String(d.supplierName)], ['Status', label(d.status)], ['Lead time', val(d.leadTimeDays)], ['Payment term', val(d.paymentTermDays)],
          ['Subtotal', money(d.subtotal)], ['Discount', money(d.discountTotal)], ['PPN', money(d.taxTotal)], ['Total', money(d.total)],
          ['Selection reason', val(d.selectionReason)]]} />
        <DataTable rows={rows(d.lines)} columns={[{ key: 'description', header: 'Item' }, { key: 'quantity', header: 'Qty', align: 'right' },
          { key: 'unitPrice', header: 'Price', align: 'right', render: (l) => money(l.unitPrice) }, { key: 'discountPercent', header: 'Disc %' },
          { key: 'lineTotal', header: 'Total', align: 'right', render: (l) => money(l.lineTotal) }, { key: 'selected', header: 'Selected', render: (l) => (l.selected ? 'Yes' : '—') }]} />
        {d.status === 'received' && <SelectQuotationButton q={d as R} />}
      </>}
    </Drawer>
  );
}

// ── Purchase Orders (EP-13) ───────────────────────────────────────────────

export function PurchaseOrdersPage() {
  const { can } = useAuth();
  const [open, setOpen] = useOpen();
  const [creating, setCreating] = useState(false);
  const [outstanding, setOutstanding] = useState(false);
  return (
    <div className="oc-stack">
      <ListPage title="Purchase Orders" help="From requisitions, a selected quotation or direct (RFQ threshold per Procurement Policies); approval matrix, sent by e-mail with a PDF link, versioned revisions."
        path="/api/v1/procurement/purchase-orders" statuses={PO_STATUS} extraQuery={{ outstanding: outstanding ? 'true' : '' }} onRowClick={(r) => setOpen(r.id)}
        filters={<Checkbox label="Outstanding PO only" checked={outstanding} onChange={setOutstanding} />}
        actions={can('procurement.purchase_order.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setCreating(true)}>New Purchase Order</button> : undefined}
        columns={[{ key: 'number', header: 'PO' }, { key: 'version', header: 'Ver.' }, { key: 'supplierName', header: 'Supplier' },
          { key: 'orderType', header: 'Type', render: (r) => label(r.orderType) }, { key: 'orderDate', header: 'Date', render: (r) => date(r.orderDate) },
          { key: 'expectedDate', header: 'Expected', render: (r) => date(r.expectedDate) }, { key: 'total', header: 'Total', align: 'right', render: (r) => money(r.total) },
          { key: 'outstandingValue', header: 'Outstanding', align: 'right', render: (r) => money(r.outstandingValue) }, { key: 'status', header: 'Status', render: pill('status') }]} />
      {creating && <OrderModal onClose={() => setCreating(false)} onDone={(id) => { setCreating(false); setOpen(id); }} />}
      {open && <OrderDrawer id={open} onClose={() => setOpen(null)} />}
    </div>
  );
}

function OrderModal({ onClose, onDone }: { onClose: () => void; onDone: (id: string) => void }) {
  const sups = useSuppliers();
  const whs = useWarehouses();
  const [f, setF] = useState({ supplierId: '', orderType: 'goods', warehouseId: '', expectedDate: '', rfqSkipReason: '', notes: '' });
  const [lines, setLines] = useState<Line[]>([emptyLine()]);
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/procurement/purchase-orders', PRC, idem);
  const set = (k: keyof typeof f) => (v: string) => setF({ ...f, [k]: v });
  const go = (submit: boolean) => send.mutate({ ...Object.fromEntries(Object.entries(f).filter(([, v]) => v)), lines: orderLines(lines), submit },
    { onSuccess: (r) => onDone(r.id) });
  return (
    <Modal open onClose={onClose} title="New Purchase Order" wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-neutral" disabled={send.isPending} onClick={() => go(false)}>Save draft</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => go(true)}>Submit for approval</button>
    </>}>
      <ErrorAlert error={send.error} />
      <div className="oc-row-wrap">
        <SelectField label="Supplier" value={f.supplierId} onChange={set('supplierId')} options={sups} required placeholder="Select" />
        <SelectField label="Type" value={f.orderType} onChange={set('orderType')} options={opts(['goods', 'service'])} />
        {f.orderType === 'goods' && <SelectField label="Receiving warehouse" value={f.warehouseId} onChange={set('warehouseId')} options={whs} placeholder="Select" />}
        <TextField label="Expected delivery" type="date" value={f.expectedDate} onChange={set('expectedDate')} />
        <TextField label="RFQ skip reason (above the threshold)" value={f.rfqSkipReason} onChange={set('rfqSkipReason')} />
      </div>
      <LinesEditor lines={lines} onChange={setLines} priceLabel="Unit price" tax />
      <TextArea label="Notes" value={f.notes} onChange={set('notes')} rows={2} />
    </Modal>
  );
}

function OrderDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const q = useGet<R>(`/api/v1/procurement/purchase-orders/${id}`);
  const revs = useGet<Page<R>>(`/api/v1/procurement/purchase-orders/${id}/revisions`);
  const [modal, setModal] = useState<'' | 'revise' | 'service' | 'send'>('');
  const d = q.data;
  const base = `/api/v1/procurement/purchase-orders/${id}`;
  const st = String(d?.status ?? '');
  const open = ['approved', 'sent', 'partially_received'].includes(st);
  const lines = rows(d?.lines);
  return (
    <Drawer open onClose={onClose} title={`Purchase Order ${String(d?.number ?? '')} v${String(d?.version ?? '')}`}>
      {!d ? <Skeleton /> : <>
        <KV items={[['Status', label(st)], ['Supplier', String(d.supplierName)], ['Type', label(d.orderType)], ['Order date', date(d.orderDate)],
          ['Expected', date(d.expectedDate)], ['Payment term', `${String(d.paymentTermDays)} days`], ['Subtotal', money(d.subtotal)], ['PPN', money(d.taxTotal)],
          ['Total', money(d.total)], ['Outstanding', money(d.outstandingValue)], ['Sent to', val(d.sentTo)], ['RFQ skipped', val(d.rfqSkipReason)],
          ['Rejected', val(d.rejectedReason)], ['Source', label(d.source)]]} />
        <DataTable rows={lines} columns={[{ key: 'description', header: 'Item / service' }, { key: 'quantity', header: 'Qty', align: 'right', render: (l) => `${String(l.quantity)} ${val(l.uom)}` },
          { key: 'unitPrice', header: 'Price', align: 'right', render: (l) => money(l.unitPrice) }, { key: 'taxPercent', header: 'PPN %' },
          { key: 'receivedQuantity', header: d.orderType === 'service' ? 'Confirmed' : 'Received', align: 'right',
            render: (l) => String(d.orderType === 'service' ? l.serviceConfirmedQuantity : l.receivedQuantity) },
          { key: 'cancelledQuantity', header: 'Cancelled', align: 'right' }, { key: 'outstandingQuantity', header: 'Outstanding', align: 'right' },
          { key: 'expectedDate', header: 'Delivery', render: (l) => date(l.expectedDate) }]} />
        <div className="oc-row-wrap">
          <a className="oc-btn oc-btn-sm oc-btn-neutral" href={`${base}/pdf`} target="_blank" rel="noreferrer">PDF</a>
          {st === 'draft' && can('procurement.purchase_order.submit') && <ActionButton label="Submit for approval" kind="primary" path={`${base}:submit`} invalidate={PRC} />}
          {st === 'pending_approval' && can('procurement.purchase_order.approve') && <>
            <ActionButton label="Approve" kind="primary" path={`${base}:approve`} body={{}} invalidate={PRC} />
            <ActionButton label="Reject" path={`${base}:reject`} invalidate={PRC} reason="required" danger />
          </>}
          {open && can('procurement.purchase_order.send') && <button className="oc-btn oc-btn-sm oc-btn-primary" onClick={() => setModal('send')}>Send to supplier</button>}
          {open && can('procurement.purchase_order.update') && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setModal('revise')}>Revise</button>}
          {open && d.orderType === 'service' && can('procurement.goods_receipt.create') && (
            <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setModal('service')}>Confirm service</button>
          )}
          {open && can('procurement.purchase_order.cancel') && (
            <ActionButton label="Cancel outstanding lines" path={`${base}:cancel-lines`} body={{ lines: lines.filter((l) => Number(l.outstandingQuantity) > 0).map((l) => ({ lineId: l.id })) }}
              invalidate={PRC} reason="required" danger />
          )}
          {['draft', 'pending_approval', 'approved', 'sent'].includes(st) && can('procurement.purchase_order.cancel') && (
            <ActionButton label="Cancel order" path={`${base}:cancel`} invalidate={PRC} reason="required" danger />
          )}
          {(open || st === 'received') && can('procurement.purchase_order.close') && (
            <ActionButton label="Close" path={`${base}:close`} invalidate={PRC} reason={st === 'received' ? 'optional' : 'required'} />
          )}
        </div>
        {(revs.data?.items.length ?? 0) > 0 && <Card title="Previous versions" icon="history">
          <DataTable rows={revs.data?.items.map((r) => ({ ...r, id: String(r.version) } as R))} columns={[{ key: 'version', header: 'Version' }, { key: 'reason', header: 'Revision reason' },
            { key: 'createdAt', header: 'Revised', render: (r) => formatDateTime(String(r.createdAt)) }]} />
        </Card>}
        {modal === 'send' && <SendOrderModal order={d} onClose={() => setModal('')} />}
        {modal === 'revise' && <ReviseModal order={d} onClose={() => setModal('')} />}
        {modal === 'service' && <ConfirmServiceModal order={d} onClose={() => setModal('')} />}
      </>}
    </Drawer>
  );
}

function SendOrderModal({ order, onClose }: { order: R; onClose: () => void }) {
  const [email, setEmail] = useState(String(order.contactEmail ?? ''));
  const send = useSend<Record<string, string>, R>('POST', `/api/v1/procurement/purchase-orders/${order.id}:send`, PRC);
  return (
    <Modal open onClose={onClose} title={`Send ${String(order.number)}`} actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => send.mutate({ ...optional('email', email) }, { onSuccess: onClose })}>Send PDF link</button>
    </>}>
      <ErrorAlert error={send.error} />
      <TextField label="Supplier e-mail" type="email" value={email} onChange={setEmail} />
    </Modal>
  );
}

function ReviseModal({ order, onClose }: { order: R; onClose: () => void }) {
  const [reason, setReason] = useState('');
  const [expected, setExpected] = useState('');
  const [edits, setEdits] = useState<Record<string, { quantity: string; unitPrice: string }>>({});
  const send = useSend<Record<string, unknown>, R>('POST', `/api/v1/procurement/purchase-orders/${order.id}:revise`, PRC);
  const lines = rows(order.lines);
  return (
    <Modal open onClose={onClose} title={`Revise ${String(order.number)} (new version)`} wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!reason || send.isPending} onClick={() => send.mutate({ reason, ...optional('expectedDate', expected),
        lines: Object.entries(edits).map(([lineId, e]) => ({ lineId, ...optional('quantity', e.quantity), ...optional('unitPrice', e.unitPrice) })) }, { onSuccess: onClose })}>Revise</button>
    </>}>
      <p className="oc-small oc-muted">A revision above the approved total goes back to approval; the new version must be sent again.</p>
      <ErrorAlert error={send.error} />
      <TextField label="Reason" value={reason} onChange={setReason} required />
      <TextField label="Expected delivery" type="date" value={expected} onChange={setExpected} />
      {lines.map((l) => {
        const e = edits[l.id] ?? { quantity: '', unitPrice: '' };
        return (
          <div key={l.id} className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
            <span style={{ minWidth: 200 }}>{String(l.description)} ({String(l.quantity)} × {money(l.unitPrice)})</span>
            <TextField label="New quantity" type="number" value={e.quantity} onChange={(v) => setEdits({ ...edits, [l.id]: { ...e, quantity: v } })} />
            <MoneyField label="New unit price" value={e.unitPrice} onChange={(v) => setEdits({ ...edits, [l.id]: { ...e, unitPrice: v } })} />
          </div>
        );
      })}
    </Modal>
  );
}

function ConfirmServiceModal({ order, onClose }: { order: R; onClose: () => void }) {
  const [q, setQ] = useState<Record<string, string>>({});
  const [notes, setNotes] = useState('');
  const send = useSend<Record<string, unknown>, R>('POST', `/api/v1/procurement/purchase-orders/${order.id}:confirm-service`, PRC);
  const lines = rows(order.lines).filter((l) => Number(l.outstandingQuantity) > 0);
  return (
    <Modal open onClose={onClose} title="Confirm delivered services" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => send.mutate({ notes, lines: Object.entries(q).filter(([, v]) => v).map(([lineId, quantity]) => ({ lineId, quantity })) },
        { onSuccess: onClose })}>Confirm</button>
    </>}>
      <ErrorAlert error={send.error} />
      {lines.map((l) => <TextField key={l.id} label={`${String(l.description)} (outstanding ${String(l.outstandingQuantity)})`} type="number" value={q[l.id] ?? ''}
        onChange={(v) => setQ({ ...q, [l.id]: v })} />)}
      <TextArea label="Notes" value={notes} onChange={setNotes} rows={2} />
    </Modal>
  );
}

// ── Goods Receipts & Purchase Returns (EP-14) ─────────────────────────────

export function GoodsReceiptsPage() {
  const { can } = useAuth();
  const [open, setOpen] = useOpen();
  const [creating, setCreating] = useState(false);
  return (
    <div className="oc-stack">
      <ListPage title="Goods Receipts" help="Receipts against purchase orders (partial or full, rejected quantity with reason, batch / expiry / serial) or without PO for allowed categories with approval."
        path="/api/v1/procurement/goods-receipts" statuses={opts(['pending_approval', 'posted', 'rejected'])} onRowClick={(r) => setOpen(r.id)}
        actions={can('procurement.goods_receipt.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setCreating(true)}>Receive Goods</button> : undefined}
        columns={[{ key: 'number', header: 'GR' }, { key: 'receivedDate', header: 'Date', render: (r) => date(r.receivedDate) }, { key: 'poNumber', header: 'PO', render: (r) => val(r.poNumber) },
          { key: 'supplierName', header: 'Supplier' }, { key: 'deliveryNoteNo', header: 'Delivery note', render: (r) => val(r.deliveryNoteNo) },
          { key: 'total', header: 'Value', align: 'right', render: (r) => money(r.total) }, { key: 'status', header: 'Status', render: pill('status') }]} />
      {creating && <Modal open onClose={() => setCreating(false)} title="Receive Goods" wide><ReceiveGoods onDone={() => setCreating(false)} /></Modal>}
      {open && <ReceiptDrawer id={open} onClose={() => setOpen(null)} />}
    </div>
  );
}

type DNFile = { id: string; filename: string };

/** Delivery note (surat jalan) photos / documents of a goods receipt
 * (FR-GR-01): each file is uploaded at once and its id is sent with the
 * receipt (attachmentFileIds), so a receipt queued offline keeps the files
 * uploaded before the connection dropped. On the warehouse device the
 * camera opens directly. */
function DeliveryNoteFiles({ files, onChange, camera }: { files: DNFile[]; onChange: (f: DNFile[]) => void; camera?: boolean }) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const online = typeof navigator === 'undefined' || navigator.onLine;
  const upload = async (f: File) => {
    setBusy(true);
    setErr(null);
    const fd = new FormData();
    fd.append('file', f);
    try {
      const r = await request<R>('POST', '/api/v1/procurement/delivery-note-files', fd);
      onChange([...files, { id: String(r.id), filename: String(r.filename ?? f.name) }]);
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="oc-stack" style={{ gap: 8 }}>
      <ErrorAlert error={err} />
      <div className="oc-row-wrap" style={{ alignItems: 'center' }} aria-label="Delivery note files">
        <label className={`oc-btn oc-btn-neutral${camera ? '' : ' oc-btn-sm'}`} style={{ cursor: online && !busy ? 'pointer' : 'not-allowed' }} aria-disabled={!online || busy}>
          <Icon name={camera ? 'photo_camera' : 'attach_file'} size={18} /> {busy ? 'Uploading…' : camera ? 'Photo of delivery note' : 'Attach delivery note'}
          <input type="file" accept="image/jpeg,image/png,image/webp,application/pdf" capture={camera ? 'environment' : undefined} className="oc-sr"
            disabled={!online || busy} onChange={(e) => { const f = e.target.files?.[0]; e.target.value = ''; if (f) void upload(f); }} />
        </label>
        {files.map((f) => (
          <span key={f.id} className="oc-chip">
            <Icon name="description" size={16} /> {f.filename}
            <button type="button" className="oc-btn oc-btn-text oc-btn-sm" aria-label={`Remove ${f.filename}`} onClick={() => onChange(files.filter((x) => x.id !== f.id))}>
              <Icon name="close" size={16} />
            </button>
          </span>
        ))}
      </div>
      {!online && <span className="oc-small oc-muted" role="status">Uploading a delivery note needs a connection; the receipt can still be saved offline.</span>}
    </div>
  );
}

type RLine = { accepted: string; rejected: string; reason: string; batch: string; expiry: string; serials: string };
const emptyRLine = (): RLine => ({ accepted: '', rejected: '', reason: '', batch: '', expiry: '', serials: '' });

/** Goods receipt form shared by the Back Office and the `ops` Warehouse:
 * barcode / camera scanning adds one unit to the matching line, every field
 * can be typed (manual fallback); offline, the receipt is queued and synced
 * once (FR-OPS-P4-04). */
function ReceiveGoods({ onDone, ops }: { onDone: () => void; ops?: boolean }) {
  const toast = useToast();
  const { propertyId } = useAuth();
  const [q, setQ] = useState('');
  const [orderId, setOrderId] = useState('');
  const [noPO, setNoPO] = useState(false);
  const [dn, setDn] = useState('');
  const [dnFiles, setDnFiles] = useState<DNFile[]>([]);
  const [reason, setReason] = useState('');
  const [lines, setLines] = useState<Record<string, RLine>>({});
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const orders = useGet<Page<R>>(`/api/v1/procurement/receivable-orders${qs({ q, limit: 100 })}`);
  const order = (orders.data?.items ?? []).find((o) => o.id === orderId);
  const olines = rows(order?.lines);
  const set = (id: string, k: keyof RLine, v: string) => setLines({ ...lines, [id]: { ...(lines[id] ?? emptyRLine()), [k]: v } });
  const scan = async (code: string) => {
    try {
      const b = await request<R>('GET', `/api/v1/inventory/barcodes/${encodeURIComponent(code)}`);
      const l = olines.find((x) => x.itemId === b.itemId);
      if (!l) {
        toast(`${String(b.itemName ?? code)} is not on this order`, 'error');
        return;
      }
      const cur = lines[l.id] ?? emptyRLine();
      set(l.id, 'accepted', String(Number(cur.accepted || 0) + 1));
    } catch {
      toast(`Barcode ${code} is not known`, 'error');
    }
  };
  const body = () => ({
    purchaseOrderId: orderId, ...optional('deliveryNoteNo', dn), ...optional('reason', reason),
    ...(dnFiles.length ? { attachmentFileIds: dnFiles.map((f) => f.id) } : {}),
    lines: olines.filter((l) => lines[l.id]?.accepted || lines[l.id]?.rejected).map((l) => {
      const x = lines[l.id];
      return { purchaseOrderLineId: l.id, acceptedQuantity: x.accepted || '0', ...optional('rejectedQuantity', x.rejected), ...optional('rejectionReason', x.reason),
        ...optional('batchNo', x.batch), ...optional('expiryDate', x.expiry),
        ...(x.serials ? { serialNos: x.serials.split(/[\s,]+/).filter(Boolean) } : {}) };
    }),
  });
  const save = async () => {
    setBusy(true);
    setErr(null);
    try {
      if (!navigator.onLine) {
        await enqueue('procurement.goods_receipt', body(), propertyId ?? '');
        toast('Offline: goods receipt queued and will sync once');
      } else {
        const g = await request<R>('POST', '/api/v1/procurement/goods-receipts', body(), idem());
        toast(`Goods Receipt ${String(g.number)}: ${label(g.status)}`);
      }
      setLines({});
      setDnFiles([]);
      setOrderId('');
      onDone();
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  };
  if (noPO) return <ReceiptWithoutPO onDone={onDone} onBack={() => setNoPO(false)} />;
  return (
    <div className="oc-stack">
      <ErrorAlert error={err} />
      {!orderId ? <>
        <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
          <TextField label="Find purchase order" value={q} onChange={setQ} placeholder="PO number or supplier" />
          {!ops && <button className="oc-btn oc-btn-neutral" onClick={() => setNoPO(true)}>Receipt without PO</button>}
        </div>
        <DataTable rows={orders.data?.items} loading={orders.isLoading} error={orders.error} onRowClick={(o) => setOrderId(o.id)}
          columns={[{ key: 'number', header: 'PO' }, { key: 'supplierName', header: 'Supplier' }, { key: 'expectedDate', header: 'Expected', render: (o) => date(o.expectedDate) },
            { key: 'lines', header: 'Open lines', render: (o) => String((o.lines as R[]).length) }]} />
      </> : <>
        <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
          <strong>{String(order?.number ?? '')} · {String(order?.supplierName ?? '')}</strong>
          <button className="oc-btn oc-btn-text" onClick={() => setOrderId('')}>Change order</button>
        </div>
        <ScanField label="Scan items (+1 per scan)" onCode={(c) => void scan(c)} />
        <TextField label="Delivery note no. (surat jalan)" value={dn} onChange={setDn} />
        <DeliveryNoteFiles files={dnFiles} onChange={setDnFiles} camera={ops} />
        {olines.map((l) => {
          const x = lines[l.id] ?? emptyRLine();
          return (
            <Card key={l.id} title={`${String(l.description)} · outstanding ${String(l.outstandingQuantity)} ${val(l.uom)}`} icon="inventory">
              <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
                <TextField label="Accepted" type="number" inputMode="decimal" value={x.accepted} onChange={(v) => set(l.id, 'accepted', v)} />
                <TextField label="Rejected" type="number" inputMode="decimal" value={x.rejected} onChange={(v) => set(l.id, 'rejected', v)} />
                {x.rejected && <TextField label="Rejection reason" value={x.reason} onChange={(v) => set(l.id, 'reason', v)} />}
                <TextField label="Batch" value={x.batch} onChange={(v) => set(l.id, 'batch', v)} />
                <TextField label="Expiry" type="date" value={x.expiry} onChange={(v) => set(l.id, 'expiry', v)} />
                <TextField label="Serial numbers" value={x.serials} onChange={(v) => set(l.id, 'serials', v)} placeholder="Separated by space or comma" />
              </div>
            </Card>
          );
        })}
        <TextField label="Reason (over-receipt: approval)" value={reason} onChange={setReason} />
        <div><button className="oc-btn oc-btn-primary oc-btn-lg" disabled={busy} onClick={() => void save()}>Save goods receipt</button></div>
      </>}
    </div>
  );
}

function ReceiptWithoutPO({ onDone, onBack }: { onDone: () => void; onBack: () => void }) {
  const sups = useSuppliers();
  const whs = useWarehouses();
  const items = useItems();
  const [f, setF] = useState({ supplierId: '', warehouseId: '', reason: '', deliveryNoteNo: '' });
  const [ls, setLs] = useState([{ itemId: '', acceptedQuantity: '', unitCost: '', batchNo: '', expiryDate: '' }]);
  const [dnFiles, setDnFiles] = useState<DNFile[]>([]);
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/procurement/goods-receipts', PRC, idem);
  const set = (k: keyof typeof f) => (v: string) => setF({ ...f, [k]: v });
  return (
    <div className="oc-stack">
      <p className="oc-small oc-muted">Only for the categories allowed by the Procurement Policies; the receipt waits for approval.</p>
      <ErrorAlert error={send.error} />
      <div className="oc-row-wrap">
        <SelectField label="Supplier" value={f.supplierId} onChange={set('supplierId')} options={sups} required placeholder="Select" />
        <SelectField label="Warehouse" value={f.warehouseId} onChange={set('warehouseId')} options={whs} required placeholder="Select" />
        <TextField label="Delivery note" value={f.deliveryNoteNo} onChange={set('deliveryNoteNo')} />
        <TextField label="Reason" value={f.reason} onChange={set('reason')} required />
      </div>
      <DeliveryNoteFiles files={dnFiles} onChange={setDnFiles} />
      {ls.map((l, i) => (
        <div key={i} className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
          <SelectField label={`Item ${i + 1}`} value={l.itemId} onChange={(v) => setLs(ls.map((x, j) => (j === i ? { ...x, itemId: v } : x)))} options={items} placeholder="Select" />
          <TextField label="Quantity" type="number" value={l.acceptedQuantity} onChange={(v) => setLs(ls.map((x, j) => (j === i ? { ...x, acceptedQuantity: v } : x)))} />
          <MoneyField label="Unit cost" value={l.unitCost} onChange={(v) => setLs(ls.map((x, j) => (j === i ? { ...x, unitCost: v } : x)))} />
          <TextField label="Batch" value={l.batchNo} onChange={(v) => setLs(ls.map((x, j) => (j === i ? { ...x, batchNo: v } : x)))} />
          <TextField label="Expiry" type="date" value={l.expiryDate} onChange={(v) => setLs(ls.map((x, j) => (j === i ? { ...x, expiryDate: v } : x)))} />
        </div>
      ))}
      <div className="oc-row-wrap">
        <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setLs([...ls, { itemId: '', acceptedQuantity: '', unitCost: '', batchNo: '', expiryDate: '' }])}>Add line</button>
        <button className="oc-btn oc-btn-text" onClick={onBack}>Back</button>
        <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => send.mutate({ ...Object.fromEntries(Object.entries(f).filter(([, v]) => v)),
          ...(dnFiles.length ? { attachmentFileIds: dnFiles.map((x) => x.id) } : {}),
          lines: ls.filter((l) => l.itemId && l.acceptedQuantity).map((l) => Object.fromEntries(Object.entries(l).filter(([, v]) => v))) }, { onSuccess: onDone })}>Submit receipt</button>
      </div>
    </div>
  );
}

function ReceiptDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const q = useGet<R>(`/api/v1/procurement/goods-receipts/${id}`);
  const [ret, setRet] = useState(false);
  const d = q.data;
  return (
    <Drawer open onClose={onClose} title={`Goods Receipt ${String(d?.number ?? '')}`}>
      {!d ? <Skeleton /> : <>
        <KV items={[['Status', label(d.status)], ['Date', date(d.receivedDate)], ['PO', val(d.poNumber)], ['Supplier', String(d.supplierName)],
          ['Delivery note', val(d.deliveryNoteNo)], ['Approval', val(label(d.approvalReason))], ['Value', money(d.total)], ['Notes', val(d.notes)]]} />
        {((d.attachmentFileIds as string[] | null) ?? []).length > 0 && (
          <div className="oc-row-wrap" aria-label="Delivery note files">
            {((d.attachmentFileIds as string[] | null) ?? []).map((fid, i) => (
              <button key={fid} className="oc-btn oc-btn-neutral oc-btn-sm"
                onClick={() => void download('GET', `/api/v1/procurement/goods-receipts/${id}/attachments/${fid}`, undefined, `delivery-note-${String(d.number)}-${i + 1}`)}>
                <Icon name="description" size={16} /> Delivery note {i + 1}
              </button>
            ))}
          </div>
        )}
        <DataTable rows={rows(d.lines)} columns={[{ key: 'description', header: 'Item' }, { key: 'deliveredQuantity', header: 'Delivered', align: 'right' },
          { key: 'acceptedQuantity', header: 'Accepted', align: 'right' }, { key: 'rejectedQuantity', header: 'Rejected', align: 'right' },
          { key: 'rejectionReason', header: 'Reason', render: (l) => val(l.rejectionReason) }, { key: 'batchNo', header: 'Batch', render: (l) => val(l.batchNo) },
          { key: 'expiryDate', header: 'Expiry', render: (l) => date(l.expiryDate) }, { key: 'returnedQuantity', header: 'Returned', align: 'right' },
          { key: 'totalCost', header: 'Cost', align: 'right', render: (l) => money(l.totalCost) }]} />
        {d.status === 'posted' && can('procurement.purchase_return.create') && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setRet(true)}>Return to supplier</button>}
        {ret && <ReturnModal receipt={d} onClose={() => setRet(false)} />}
      </>}
    </Drawer>
  );
}

function ReturnModal({ receipt, onClose }: { receipt: R; onClose: () => void }) {
  const [reason, setReason] = useState('');
  const [q, setQ] = useState<Record<string, string>>({});
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/procurement/purchase-returns', PRC, idem);
  const lines = rows(receipt.lines).filter((l) => Number(l.acceptedQuantity) > Number(l.returnedQuantity));
  return (
    <Modal open onClose={onClose} title={`Purchase Return of ${String(receipt.number)}`} actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!reason || send.isPending} onClick={() => send.mutate({ goodsReceiptId: receipt.id, reason,
        lines: Object.entries(q).filter(([, v]) => v).map(([goodsReceiptLineId, quantity]) => ({ goodsReceiptLineId, quantity })) }, { onSuccess: onClose })}>Return (debit note)</button>
    </>}>
      <ErrorAlert error={send.error} />
      <TextField label="Reason" value={reason} onChange={setReason} required />
      {lines.map((l) => <TextField key={l.id} label={`${String(l.description)} (up to ${Number(l.acceptedQuantity) - Number(l.returnedQuantity)})`} type="number"
        value={q[l.id] ?? ''} onChange={(v) => setQ({ ...q, [l.id]: v })} />)}
    </Modal>
  );
}

export function PurchaseReturnsPage() {
  const [open, setOpen] = useOpen();
  return (
    <div className="oc-stack">
      <ListPage title="Purchase Returns" help="Goods returned to the supplier with a reason: stock out at the receipt cost and a debit note reducing the payable."
        path="/api/v1/procurement/purchase-returns" search={false} onRowClick={(r) => setOpen(r.id)}
        columns={[{ key: 'number', header: 'Return' }, { key: 'returnDate', header: 'Date', render: (r) => date(r.returnDate) }, { key: 'grNumber', header: 'GR' },
          { key: 'supplierName', header: 'Supplier' }, { key: 'reason', header: 'Reason' }, { key: 'total', header: 'Total', align: 'right', render: (r) => money(r.total) },
          { key: 'debitNoteNumber', header: 'Debit note', render: (r) => val(r.debitNoteNumber) }]} />
      {open && <ReturnDrawer id={open} onClose={() => setOpen(null)} />}
    </div>
  );
}

function ReturnDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const q = useGet<R>(`/api/v1/procurement/purchase-returns/${id}`);
  const d = q.data;
  return (
    <Drawer open onClose={onClose} title={`Purchase Return ${String(d?.number ?? '')}`}>
      {!d ? <Skeleton /> : <>
        <KV items={[['Goods receipt', String(d.grNumber)], ['Supplier', String(d.supplierName)], ['Reason', String(d.reason)], ['Subtotal', money(d.subtotal)],
          ['PPN', money(d.taxAmount)], ['Total', money(d.total)], ['Debit note', val(d.debitNoteNumber)]]} />
        <DataTable rows={rows(d.lines)} columns={[{ key: 'description', header: 'Item' }, { key: 'quantity', header: 'Qty', align: 'right' },
          { key: 'unitCost', header: 'Cost', align: 'right', render: (l) => money(l.unitCost) }, { key: 'totalCost', header: 'Total', align: 'right', render: (l) => money(l.totalCost) },
          { key: 'batchNo', header: 'Batch', render: (l) => val(l.batchNo) }]} />
      </>}
    </Drawer>
  );
}

// ── Vendor Invoices & 3-Way Matching (EP-15) ──────────────────────────────

export function VendorInvoicesPage() {
  const { can } = useAuth();
  const [open, setOpen] = useOpen();
  const [creating, setCreating] = useState(false);
  const [overdue, setOverdue] = useState(false);
  const [tab, setTab] = useState('invoices');
  return (
    <div className="oc-stack">
      <Tabs tabs={[{ value: 'invoices', label: 'Vendor Invoices' }, { value: 'debit', label: 'Debit Notes' }]} value={tab} onChange={setTab} />
      {tab === 'invoices' ? (
        <ListPage title="Vendor Invoices" help="Supplier invoices matched 3-way (PO + GR + invoice, 2-way for services) within the Procurement Policies tolerance; mismatches are held until corrected, covered by a debit note or approved as an override."
          path="/api/v1/procurement/vendor-invoices" statuses={VI_STATUS} extraQuery={{ overdue: overdue ? 'true' : '' }} onRowClick={(r) => setOpen(r.id)}
          filters={<Checkbox label="Overdue only" checked={overdue} onChange={setOverdue} />}
          actions={can('procurement.vendor_invoice.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setCreating(true)}>Record Vendor Invoice</button> : undefined}
          columns={[{ key: 'number', header: 'Invoice' }, { key: 'supplierInvoiceNo', header: 'Supplier No.' }, { key: 'supplierName', header: 'Supplier' },
            { key: 'invoiceDate', header: 'Date', render: (r) => date(r.invoiceDate) }, { key: 'dueDate', header: 'Due', render: (r) => date(r.dueDate) },
            { key: 'total', header: 'Total', align: 'right', render: (r) => money(r.total) }, { key: 'outstanding', header: 'Outstanding', align: 'right', render: (r) => money(r.outstanding) },
            { key: 'matchType', header: 'Match', render: (r) => val(label(r.matchType)) }, { key: 'status', header: 'Status', render: pill('status') }]} />
      ) : (
        <ListPage title="Debit Notes" path="/api/v1/procurement/debit-notes" search={false}
          columns={[{ key: 'number', header: 'Debit note' }, { key: 'issueDate', header: 'Date', render: (r) => date(r.issueDate) }, { key: 'supplierName', header: 'Supplier' },
            { key: 'reason', header: 'Reason' }, { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) }, { key: 'status', header: 'Status', render: pill('status') }]} />
      )}
      {creating && <InvoiceModal onClose={() => setCreating(false)} onDone={(id) => { setCreating(false); setOpen(id); }} />}
      {open && <InvoiceDrawer id={open} onClose={() => setOpen(null)} />}
    </div>
  );
}

function InvoiceModal({ onClose, onDone }: { onClose: () => void; onDone: (id: string) => void }) {
  const orders = useOptions('/api/v1/procurement/purchase-orders?limit=300&filter[status]=partially_received,received,sent,approved,closed', (x) => `${String(x.number)} · ${String(x.supplierName)}`);
  const sups = useSuppliers();
  const [f, setF] = useState({ purchaseOrderId: '', supplierId: '', supplierInvoiceNo: '', taxInvoiceNo: '', invoiceDate: today(), dueDate: '' });
  const [lines, setLines] = useState<Line[]>([]);
  const [landed, setLanded] = useState<LandedLine[]>([]);
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/procurement/vendor-invoices', PRC, idem);
  const set = (k: keyof typeof f) => (v: string) => setF({ ...f, [k]: v });
  const allLines = [
    ...lines.filter((l) => l.quantity && l.price).map((l) => ({ quantity: l.quantity, unitPrice: l.price, ...optional('itemId', l.itemId),
      ...optional('description', l.description), ...optional('taxPercent', l.tax) })),
    ...landed.filter((l) => l.amount && l.description).map((l) => ({ quantity: '1', unitPrice: l.amount, description: l.description, landedCost: l.basis,
      ...optional('landedGoodsReceiptId', l.goodsReceiptId), ...optional('taxPercent', l.tax) })),
  ];
  return (
    <Modal open onClose={onClose} title="Record Vendor Invoice" wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!f.supplierInvoiceNo || send.isPending} onClick={() => send.mutate({ ...Object.fromEntries(Object.entries(f).filter(([, v]) => v)),
        ...(allLines.length ? { lines: allLines } : {}), match: true }, { onSuccess: (r) => onDone(r.id) })}>Record & match</button>
    </>}>
      <ErrorAlert error={send.error} />
      <div className="oc-row-wrap">
        <SelectField label="Purchase order" value={f.purchaseOrderId} onChange={set('purchaseOrderId')} options={orders} placeholder="Select (bills the received quantities)" />
        {!f.purchaseOrderId && <SelectField label="Supplier" value={f.supplierId} onChange={set('supplierId')} options={sups} placeholder="Select" />}
        <TextField label="Supplier invoice no." value={f.supplierInvoiceNo} onChange={set('supplierInvoiceNo')} required />
        <TextField label="Faktur Pajak no." value={f.taxInvoiceNo} onChange={set('taxInvoiceNo')} />
        <TextField label="Invoice date" type="date" value={f.invoiceDate} onChange={set('invoiceDate')} />
        <TextField label="Due date" type="date" value={f.dueDate} onChange={set('dueDate')} placeholder="Payment term" />
      </div>
      <p className="oc-small oc-muted">Without lines, the received and not yet invoiced quantities of the order are billed at the order price; add lines for other amounts.</p>
      <LinesEditor lines={lines} onChange={setLines} priceLabel="Invoiced price" tax />
      <LandedCostEditor lines={landed} onChange={setLanded} />
    </Modal>
  );
}

type LandedLine = { description: string; amount: string; basis: string; goodsReceiptId: string; tax: string };

/** Landed cost lines (FR-VAL-03): freight, duty or insurance allocated to the
 * received stock of a goods receipt by value or quantity; the stock still on
 * hand is revalued, the part already used goes to cost of sales. */
function LandedCostEditor({ lines, onChange }: { lines: LandedLine[]; onChange: (l: LandedLine[]) => void }) {
  const grs = useOptions('/api/v1/procurement/goods-receipts?limit=300&filter[status]=posted', (x) => `${String(x.number)} · ${String(x.supplierName ?? '')}`);
  const set = (i: number, k: keyof LandedLine, v: string) => onChange(lines.map((l, j) => (j === i ? { ...l, [k]: v } : l)));
  return (
    <Card title="Landed cost" icon="local_shipping">
      <p className="oc-small oc-muted">Freight, duty or insurance added to the cost of the received goods. Without a goods receipt, the receipts of the invoice lines above are used.
        With lines, the order is not billed automatically.</p>
      {lines.map((l, i) => (
        <div key={i} className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
          <TextField label={`Landed cost ${i + 1}`} value={l.description} onChange={(v) => set(i, 'description', v)} placeholder="Freight, import duty" required />
          <MoneyField label="Amount" value={l.amount} onChange={(v) => set(i, 'amount', v)} required />
          <SelectField label="Allocate by" value={l.basis} onChange={(v) => set(i, 'basis', v)} options={[{ value: 'value', label: 'Value' }, { value: 'quantity', label: 'Quantity' }]} />
          <SelectField label="Goods receipt" value={l.goodsReceiptId} onChange={(v) => set(i, 'goodsReceiptId', v)} options={grs} placeholder="Receipts of the invoice" />
          <TextField label="PPN %" type="number" value={l.tax} onChange={(v) => set(i, 'tax', v)} placeholder="0" />
          <button className="oc-btn oc-btn-text" aria-label={`Remove landed cost ${i + 1}`} onClick={() => onChange(lines.filter((_, j) => j !== i))}><Icon name="delete" size={18} /></button>
        </div>
      ))}
      <div><button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => onChange([...lines, { description: '', amount: '', basis: 'value', goodsReceiptId: '', tax: '' }])}>Add landed cost</button></div>
    </Card>
  );
}

function InvoiceDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const q = useGet<R>(`/api/v1/procurement/vendor-invoices/${id}`);
  const runs = useGet<Page<R>>(`/api/v1/procurement/vendor-invoices/${id}/match-runs`);
  const pays = useGet<Page<R>>(`/api/v1/procurement/vendor-invoices/${id}/payments`);
  const [dn, setDn] = useState(false);
  const d = q.data;
  const base = `/api/v1/procurement/vendor-invoices/${id}`;
  const st = String(d?.status ?? '');
  return (
    <Drawer open onClose={onClose} title={`Vendor Invoice ${String(d?.number ?? '')}`}>
      {!d ? <Skeleton /> : <>
        {d.holdReason ? <div role="alert" className="oc-alert oc-alert-warning">{String(d.holdReason)}</div> : null}
        <KV items={[['Status', label(st)], ['Supplier', String(d.supplierName)], ['Supplier invoice', String(d.supplierInvoiceNo)], ['Faktur Pajak', val(d.taxInvoiceNo)],
          ['Invoice date', date(d.invoiceDate)], ['Due', date(d.dueDate)], ['Match', val(label(d.matchType))], ['Subtotal', money(d.subtotal)], ['PPN', money(d.taxAmount)],
          ['Total', money(d.total)], ['PPh withheld', money(d.withholdingAmount)], ['Debit notes', money(d.debitNoteTotal)], ['Paid', money(d.paidAmount)],
          ['Outstanding', money(d.outstanding)], ['Override reason', val(d.overrideReason)]]} />
        <DataTable rows={rows(d.lines)} columns={[{ key: 'description', header: 'Line' }, { key: 'quantity', header: 'Qty', align: 'right' },
          { key: 'expectedQuantity', header: 'Received, not invoiced', align: 'right', render: (l) => val(l.expectedQuantity) },
          { key: 'unitPrice', header: 'Price', align: 'right', render: (l) => money(l.unitPrice) }, { key: 'expectedUnitPrice', header: 'Ordered price', align: 'right', render: (l) => money(l.expectedUnitPrice) },
          { key: 'lineTotal', header: 'Total', align: 'right', render: (l) => money(l.lineTotal) }, { key: 'matchStatus', header: 'Match', render: pill('matchStatus') },
          { key: 'matchNote', header: 'Note', render: (l) => (l.landedCost ? `Landed cost (${label(l.landedCost)}) · ${val(l.matchNote)}` : val(l.matchNote)) }]} />
        <div className="oc-row-wrap">
          {['draft', 'mismatch', 'on_hold', 'matched'].includes(st) && can('procurement.vendor_invoice.match') && <ActionButton label="Perform 3-Way Matching" kind="primary" path={`${base}:match`} body={{}} invalidate={PRC} />}
          {['draft', 'matched', 'mismatch'].includes(st) && can('procurement.vendor_invoice.hold') && <ActionButton label="Put on hold" path={`${base}:hold`} invalidate={PRC} reason="required" />}
          {['on_hold', 'mismatch'].includes(st) && can('procurement.vendor_invoice.hold') && <ActionButton label="Release hold" path={`${base}:release-hold`} invalidate={PRC} reason="required" />}
          {['matched', 'on_hold', 'mismatch'].includes(st) && can('procurement.vendor_invoice.approve') && <ActionButton label="Approve" kind="primary" path={`${base}:approve`} body={{}} invalidate={PRC} />}
          {!['draft', 'cancelled', 'paid'].includes(st) && can('procurement.debit_note.create') && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setDn(true)}>Debit note</button>}
          {['draft', 'matched', 'mismatch', 'on_hold'].includes(st) && can('procurement.vendor_invoice.cancel') && <ActionButton label="Cancel" path={`${base}:cancel`} invalidate={PRC} reason="required" danger />}
        </div>
        <Card title="Matching history" icon="rule">
          <DataTable rows={runs.data?.items} loading={runs.isLoading} columns={[{ key: 'runAt', header: 'Run', render: (r) => formatDateTime(String(r.runAt)) },
            { key: 'matchType', header: 'Type', render: (r) => label(r.matchType) }, { key: 'result', header: 'Result', render: pill('result') }]} />
        </Card>
        <Card title="Payments (Accounts Payable)" icon="payments">
          <DataTable rows={pays.data?.items.map((p) => ({ ...p, id: String(p.paymentId) } as R))} loading={pays.isLoading}
            columns={[{ key: 'paymentNumber', header: 'Payment', render: (p) => val(p.paymentNumber) }, { key: 'paidDate', header: 'Date', render: (p) => date(p.paidDate) },
              { key: 'amount', header: 'Amount', align: 'right', render: (p) => money(p.amount) }]} />
        </Card>
        {dn && <DebitNoteModal invoice={d} onClose={() => setDn(false)} />}
      </>}
    </Drawer>
  );
}

function DebitNoteModal({ invoice, onClose }: { invoice: R; onClose: () => void }) {
  const [f, setF] = useState({ subtotal: '', taxAmount: '', reason: '' });
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/procurement/debit-notes', PRC, idem);
  const set = (k: keyof typeof f) => (v: string) => setF({ ...f, [k]: v });
  return (
    <Modal open onClose={onClose} title={`Debit note on ${String(invoice.number)}`} actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!f.subtotal || !f.reason || send.isPending} onClick={() => send.mutate({ vendorInvoiceId: invoice.id,
        ...Object.fromEntries(Object.entries(f).filter(([, v]) => v)) }, { onSuccess: onClose })}>Issue</button>
    </>}>
      <ErrorAlert error={send.error} />
      <MoneyField label="Amount (excl. PPN)" value={f.subtotal} onChange={set('subtotal')} required />
      <TextField label="PPN" type="number" value={f.taxAmount} onChange={set('taxAmount')} />
      <TextField label="Reason" value={f.reason} onChange={set('reason')} required />
    </Modal>
  );
}

// ── Vendor Performance, Reports, Migration ────────────────────────────────

export function VendorPerformancePage() {
  const { can } = useAuth();
  const [from, setFrom] = useState(() => { const d = new Date(); d.setDate(d.getDate() - 90); return d.toISOString().slice(0, 10); });
  const [to, setTo] = useState(today());
  const [tab, setTab] = useState('live');
  const [period, setPeriod] = useState(today().slice(0, 7));
  const live = useGet<Page<R>>(tab === 'live' ? `/api/v1/procurement/vendor-performance${qs({ from, to })}` : null);
  const cards = useGet<Page<R>>(tab === 'cards' ? `/api/v1/procurement/vendor-scorecards${qs({ 'filter[period]': period })}` : null);
  const pct = (v: unknown) => (v === null || v === undefined ? '—' : `${(Number(v) * 100).toFixed(1)} %`);
  const cols = [{ key: 'supplierName', header: 'Supplier' }, { key: 'orders', header: 'Orders', align: 'right' as const },
    { key: 'purchaseValue', header: 'Purchase value', align: 'right' as const, render: (r: R) => money(r.purchaseValue) }, { key: 'receipts', header: 'Receipts', align: 'right' as const },
    { key: 'onTimeRate', header: 'On time', render: (r: R) => pct(r.onTimeRate) }, { key: 'fillRate', header: 'Fill', render: (r: R) => pct(r.fillRate) },
    { key: 'qualityRate', header: 'Quality', render: (r: R) => pct(r.qualityRate) }, { key: 'returnRate', header: 'Returns', render: (r: R) => pct(r.returnRate) },
    { key: 'priceVariancePercent', header: 'Price var. %', render: (r: R) => val(r.priceVariancePercent) }, { key: 'responseHours', header: 'RFQ response (h)', render: (r: R) => val(r.responseHours) },
    { key: 'score', header: 'Score', align: 'right' as const }, { key: 'grade', header: 'Grade' }];
  return (
    <div className="oc-stack">
      <PageHeader title="Vendor Performance" help="On-time delivery, fill rate, quality at goods receipt, returns, invoice price variance and RFQ response time with a weighted score (Procurement Configuration)." />
      <Tabs tabs={[{ value: 'live', label: 'Period' }, { value: 'cards', label: 'Monthly scorecards' }]} value={tab} onChange={setTab} />
      {tab === 'live' ? <>
        <div className="oc-row-wrap">
          <TextField label="From" type="date" value={from} onChange={setFrom} />
          <TextField label="To" type="date" value={to} onChange={setTo} />
        </div>
        <DataTable rows={live.data?.items.map((r) => ({ ...r, id: String(r.supplierId) } as R))} loading={live.isLoading} error={live.error} columns={cols} />
      </> : <>
        <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
          <TextField label="Period" type="month" value={period} onChange={setPeriod} />
          {can('procurement.vendor_performance.compute') && (
            <ActionButton label="Compute scorecards" kind="primary" path="/api/v1/procurement/vendor-performance:compute" body={{ period }} invalidate={PRC} />
          )}
        </div>
        <DataTable rows={cards.data?.items} loading={cards.isLoading} error={cards.error} columns={cols} />
      </>}
    </div>
  );
}

export function ProcurementReportsPage() {
  const reports = useGet<Page<R & { code: string; module: string; name: string; description: string }>>('/api/v1/reporting/reports');
  return (
    <div className="oc-stack">
      <PageHeader title="Procurement Reports" help="Reports run on the read replica; filter by date, supplier, warehouse and status, export CSV / XLSX." />
      {reports.isLoading && <Skeleton />}
      <div className="oc-grid">
        {reports.data?.items.filter((r) => r.module === 'procurement').map((r) => (
          <Link key={r.code} to={`/reports/${r.code}`} className="oc-card" style={{ textDecoration: 'none' }}>
            <div className="oc-card-head"><span className="oc-icon-circle"><Icon name="table_chart" size={20} /></span><h3>{r.name}</h3></div>
            <div className="oc-small oc-muted">{r.description}</div>
          </Link>
        ))}
      </div>
      <Link to="/procurement/performance" className="oc-btn oc-btn-neutral">Procurement Performance dashboard</Link>
    </div>
  );
}

const IMPORT_HEADERS: Record<string, string> = {
  suppliers: 'code,name,legalName,npwp,email,phone,address,categories,pkp,withholdingType,paymentTermDays,currency,leadTimeDays,contractSupplier,contactName,contactEmail,contactPhone,bankName,bankAccountNumber,bankAccountName\n',
  supplier_items: 'supplierCode,itemCode,unitPrice,uom,supplierItemCode,minOrderQuantity,leadTimeDays,validFrom,validTo,preferred,currency\n',
  open_purchase_orders: 'poNumber,supplierCode,orderDate,warehouseCode,itemCode,quantity,unitPrice,uom,receivedQuantity,discountPercent,taxPercent,expectedDate,description,orderType,paymentTermDays,currency\n',
};

export function ProcurementMigrationPage() {
  const [entity, setEntity] = useState('suppliers');
  const [csv, setCsv] = useState(IMPORT_HEADERS.suppliers);
  const [filename, setFilename] = useState('');
  const [res, setRes] = useState<R | null>(null);
  const send = useSend<Record<string, string>, R>('POST', '/api/v1/procurement/migration:import', PRC);
  const run = (mode: string) => send.mutate({ entity, mode, csv, ...optional('filename', filename) }, { onSuccess: setRes });
  const file = (f: File | undefined) => {
    if (!f) return;
    setFilename(f.name);
    void f.text().then(setCsv);
  };
  const rec = (res?.reconciliation ?? {}) as R;
  return (
    <div className="oc-stack">
      <PageHeader title="Procurement Migration" help="Suppliers, supplier items & price lists and open purchase orders (with quantities received before the cut-over): dry run until valid and reconciled, then commit; re-running a file is safe." />
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <SelectField label="Entity" value={entity} onChange={(v) => { setEntity(v); setCsv(IMPORT_HEADERS[v]); setRes(null); }}
          options={[{ value: 'suppliers', label: 'Suppliers' }, { value: 'supplier_items', label: 'Supplier Items' }, { value: 'open_purchase_orders', label: 'Open Purchase Orders' }]} />
        <label className="oc-btn oc-btn-neutral">Load CSV file<input type="file" accept=".csv,text/csv" hidden onChange={(e) => file(e.target.files?.[0])} /></label>
      </div>
      <TextArea label="CSV" value={csv} onChange={setCsv} rows={10} />
      <div className="oc-row-wrap">
        <button className="oc-btn oc-btn-neutral" disabled={send.isPending} onClick={() => run('preview')}>Dry run</button>
        <button className="oc-btn oc-btn-primary" disabled={send.isPending || res?.status !== 'valid'} onClick={() => run('commit')}>Commit</button>
      </div>
      <ErrorAlert error={send.error} />
      {res && <Card title={`Result: ${label(res.status)} (${label(res.mode)})`} icon="fact_check">
        <KV items={[['Rows', String(res.totalRows)], ['Created', String(res.created)], ['Updated', String(res.updated)], ['Skipped (already imported)', String(res.skipped)],
          ['Suppliers', String(rec.suppliers)], ['Supplier items', String(rec.supplierItems)], ['Purchase orders', String(rec.purchaseOrders)], ['Order lines', String(rec.orderLines)],
          ['Ordered value', money(rec.orderedValue)], ['Received before cut-over', money(rec.receivedValue)], ['Outstanding PO value', money(rec.outstandingValue)]]} />
        {(res.errors as R[]).length > 0 && <DataTable rows={rows(res.errors)} columns={[{ key: 'row', header: 'Row' }, { key: 'field', header: 'Field', render: (e) => val(e.field) },
          { key: 'message', header: 'Problem' }]} />}
      </Card>}
    </div>
  );
}

// ── ops Warehouse: Goods Receipt (EP-26) ──────────────────────────────────

function GoodsReceiptOps() {
  const [n, setN] = useState(0);
  return (
    <div className="oc-stack">
      <div className="oc-page-head"><div><h1>Goods Receipt</h1><p>Pick the purchase order, scan the items (or type the quantities), add batch / expiry, take a photo of the delivery note and save. Offline receipts sync once.</p></div></div>
      <ReceiveGoods key={n} ops onDone={() => setN((x) => x + 1)} />
    </div>
  );
}

/** Back Office routes of the area. */
export const PROCUREMENT_ROUTES: AreaRoute[] = [
  { path: 'procurement/suppliers', perm: 'procurement.supplier.view', element: <SuppliersPage /> },
  { path: 'procurement/requisitions', perm: 'procurement.requisition.view', element: <RequisitionsPage /> },
  { path: 'procurement/approvals', perm: 'procurement.requisition.view', element: <ProcurementApprovalsPage /> },
  { path: 'procurement/rfqs', perm: 'procurement.rfq.view', element: <RFQsPage /> },
  { path: 'procurement/vendor-quotations', perm: 'procurement.vendor_quotation.view', element: <VendorQuotationsPage /> },
  { path: 'procurement/purchase-orders', perm: 'procurement.purchase_order.view', element: <PurchaseOrdersPage /> },
  { path: 'procurement/goods-receipts', perm: 'procurement.goods_receipt.view', element: <GoodsReceiptsPage /> },
  { path: 'procurement/purchase-returns', perm: 'procurement.purchase_return.view', element: <PurchaseReturnsPage /> },
  { path: 'procurement/vendor-invoices', perm: 'procurement.vendor_invoice.view', element: <VendorInvoicesPage /> },
  { path: 'procurement/vendor-performance', perm: 'procurement.vendor_performance.view', element: <VendorPerformancePage /> },
  { path: 'procurement/migration', perm: 'procurement.migration.import', element: <ProcurementMigrationPage /> },
  { path: 'procurement/reports', perm: 'reporting.report.view', element: <ProcurementReportsPage /> },
  { path: 'procurement/performance', perm: 'reporting.dashboard.view', element: <KPIDashboardPage code="procurement-performance" /> },
];

/** Ops workstation tiles and routes of the area (the Warehouse tile of inventory opens Goods Receipt from its menu). */
export const PROCUREMENT_OPS_TILES: OpsTile[] = [
  ['local_shipping', 'Goods Receipt', '/ops/warehouse/goods-receipt', 'procurement.goods_receipt.create'],
];
export const PROCUREMENT_OPS_ROUTES: OpsRoute[] = [
  { path: 'warehouse/goods-receipt', element: <GoodsReceiptOps /> },
];
