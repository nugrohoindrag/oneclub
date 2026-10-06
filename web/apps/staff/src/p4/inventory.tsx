import React, { useEffect, useRef, useState } from 'react';
import { Link, useSearchParams } from 'react-router';
import { qs, request, uuidv7, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate, formatDateTime } from '@oneclub/i18n';
import { enqueue } from '@oneclub/offline';
import {
  AutoResourcePage, Card, Checkbox, DataTable, Drawer, Empty, ErrorAlert, Icon, Modal, PageHeader, SelectField, Skeleton, StatusPill, TextArea,
  TextField, useAuth, useToast, type Option,
} from '@oneclub/shell';
import { ActionButton, KV, ListPage, Tabs, money, today, type R } from '../p1/common';
import { KPIDashboardPage } from '../p2';
import type { AreaRoute, OpsRoute, OpsTile } from '../p3/types';

// Inventory & Assets (PRD P4 EP-01–09, Naming Convention §19): Stock Balance,
// Stock Movement, Store Requisition, Stock Transfer, Stock Adjustment, Stock
// Opname, Stock Valuation, Reorder Point & replenishment, Production, Waste,
// Consignment, Assets & Equipment, Posting Exceptions and Inventory Reports.
// The `ops` workstations (EP-26): Warehouse (stock, issuing, transfer, stock
// opname with barcode / camera scanning and offline counts, store
// requisitions), Kitchen & Outlet Store (requisition, production, waste,
// outlet stock) and the Golf Staff Spare Part Request. Master data (items,
// categories, UOM, warehouses, stock locations, par stock, BOM & recipes) is
// rendered from the resource definitions under /inventory/m/<key>.

const INV = ['/api/v1/inventory'];
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const pill = (k: string) => (r: R) => <StatusPill status={String(r[k] ?? '').replace(/_/g, '-')} label={label(r[k])} />;
const opts = (vals: string[]): Option[] => vals.map((v) => ({ value: v, label: label(v) }));
const qty = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : String(v));
const ADJ_REASONS = opts(['damage', 'expired', 'theft', 'count_correction', 'found', 'other']);
const WASTE_REASONS = opts(['expired', 'damaged', 'spoiled', 'wrong_preparation', 'overproduction', 'breakage', 'other']);
const MOVE_TYPES = opts(['receipt', 'issue', 'transfer_out', 'transfer_in', 'adjustment', 'opname', 'waste', 'production_in', 'production_out', 'consumption', 'return_out']);

function useWarehouses(): Option[] {
  const w = useGet<Page<R>>('/api/v1/inventory/warehouses?limit=500&filter[status]=active');
  return (w.data?.items ?? []).map((x) => ({ value: x.id, label: `${String(x.name)} (${String(x.code)})` }));
}

function useItems(): { options: Option[]; byId: Record<string, R> } {
  const i = useGet<Page<R>>('/api/v1/inventory/items?limit=500&filter[status]=active');
  const items = i.data?.items ?? [];
  return { options: items.map((x) => ({ value: x.id, label: `${String(x.name)} (${String(x.code)})` })), byId: Object.fromEntries(items.map((x) => [x.id, x])) };
}

function useUoms(): Option[] {
  const u = useGet<Page<R>>('/api/v1/inventory/uoms?limit=500');
  return (u.data?.items ?? []).map((x) => ({ value: x.id, label: String(x.code) }));
}

function WarehouseSelect({ value, onChange, label: lbl = 'Warehouse', required, all }: {
  value: string; onChange: (v: string) => void; label?: string; required?: boolean; all?: boolean;
}) {
  const options = useWarehouses();
  return <SelectField label={lbl} value={value} onChange={onChange} required={required} placeholder={all ? 'All warehouses' : 'Select'} options={options} />;
}

// ── barcode / camera scanning (FR-OPS-P4-01) ──────────────────────────────

type Detector = { detect: (src: HTMLVideoElement) => Promise<{ rawValue: string }[]> };

/** Barcode input: USB / Bluetooth scanners type into the field (Enter),
 * the camera uses BarcodeDetector where the browser has it. */
export function ScanField({ onCode, label: lbl = 'Scan barcode' }: { onCode: (code: string) => void; label?: string }) {
  const [code, setCode] = useState('');
  const [camera, setCamera] = useState(false);
  const video = useRef<HTMLVideoElement>(null);
  const supported = typeof window !== 'undefined' && 'BarcodeDetector' in window;
  useEffect(() => {
    if (!camera || !supported) return undefined;
    let stream: MediaStream | undefined;
    let timer: number | undefined;
    const Ctor = (window as unknown as { BarcodeDetector: new (o: { formats: string[] }) => Detector }).BarcodeDetector;
    const detector = new Ctor({ formats: ['ean_13', 'ean_8', 'code_128', 'code_39', 'upc_a', 'upc_e', 'qr_code'] });
    void navigator.mediaDevices?.getUserMedia({ video: { facingMode: 'environment' } }).then((s) => {
      stream = s;
      if (video.current) {
        video.current.srcObject = s;
        void video.current.play();
      }
      timer = window.setInterval(() => {
        if (!video.current) return;
        void detector.detect(video.current).then((found) => {
          if (found[0]?.rawValue) {
            onCode(found[0].rawValue);
            setCamera(false);
          }
        }).catch(() => undefined);
      }, 400);
    }).catch(() => setCamera(false));
    return () => {
      if (timer) window.clearInterval(timer);
      stream?.getTracks().forEach((t) => t.stop());
    };
  }, [camera, supported, onCode]);
  const submit = () => {
    if (code.trim()) onCode(code.trim());
    setCode('');
  };
  return (
    <div className="oc-stack" style={{ gap: 8 }}>
      <div className="oc-row-wrap" onKeyDown={(e) => { if (e.key === 'Enter') submit(); }}>
        <TextField label={lbl} value={code} onChange={setCode} placeholder="Scan or type the barcode, then Enter" />
        <button className="oc-btn oc-btn-neutral" onClick={submit} aria-label="Look up barcode"><Icon name="search" size={18} /> Find</button>
        {supported && (
          <button className="oc-btn oc-btn-neutral" onClick={() => setCamera((c) => !c)} aria-pressed={camera} aria-label="Scan with the camera">
            <Icon name="photo_camera" size={18} /> {camera ? 'Stop camera' : 'Camera'}
          </button>
        )}
      </div>
      {camera && <video ref={video} muted playsInline aria-label="Camera preview" style={{ width: '100%', maxWidth: 420, borderRadius: 12 }} />}
    </div>
  );
}

/** Looks up a scanned barcode (item or carton). */
async function lookup(code: string) {
  return request<R>('GET', `/api/v1/inventory/barcodes/${encodeURIComponent(code)}`);
}

// ── document lines ────────────────────────────────────────────────────────

type Line = { itemId: string; quantity: string; uomId: string; batchNo: string; expiryDate: string; unitCost: string };
const emptyLine = (): Line => ({ itemId: '', quantity: '', uomId: '', batchNo: '', expiryDate: '', unitCost: '' });

/** Item lines with scanning; extra columns for incoming stock. */
function LinesEditor({ lines, onChange, incoming, signed }: { lines: Line[]; onChange: (l: Line[]) => void; incoming?: boolean; signed?: boolean }) {
  const items = useItems();
  const uoms = useUoms();
  const toast = useToast();
  const set = (i: number, k: keyof Line, v: string) => onChange(lines.map((l, j) => (j === i ? { ...l, [k]: v } : l)));
  const scan = async (code: string) => {
    try {
      const b = await lookup(code);
      const i = lines.findIndex((l) => l.itemId === b.itemId && l.uomId === b.uomId);
      if (i >= 0) set(i, 'quantity', String(Number(lines[i].quantity || 0) + 1));
      else onChange([...lines.filter((l) => l.itemId), { ...emptyLine(), itemId: String(b.itemId), uomId: String(b.uomId), quantity: '1' }]);
    } catch {
      toast(`Barcode ${code} is not known`, 'error');
    }
  };
  return (
    <div className="oc-stack" style={{ gap: 8 }}>
      <ScanField onCode={(c) => void scan(c)} />
      {lines.map((l, i) => (
        <div key={i} className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
          <SelectField label="Item" value={l.itemId} onChange={(v) => set(i, 'itemId', v)} options={items.options} placeholder="Select item" />
          <TextField label={signed ? 'Quantity (+ / −)' : 'Quantity'} type="number" value={l.quantity} onChange={(v) => set(i, 'quantity', v)} />
          <SelectField label="UOM" value={l.uomId} onChange={(v) => set(i, 'uomId', v)} options={uoms} placeholder="Stock UOM" />
          {incoming && <TextField label="Unit cost" type="number" value={l.unitCost} onChange={(v) => set(i, 'unitCost', v)} />}
          <TextField label="Batch" value={l.batchNo} onChange={(v) => set(i, 'batchNo', v)} />
          {incoming && <TextField label="Expiry" type="date" value={l.expiryDate} onChange={(v) => set(i, 'expiryDate', v)} />}
          <button className="oc-btn oc-btn-text" aria-label="Remove line" onClick={() => onChange(lines.filter((_, j) => j !== i))}><Icon name="delete" size={18} /></button>
        </div>
      ))}
      <div><button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => onChange([...lines, emptyLine()])}>Add line</button></div>
    </div>
  );
}

const linesBody = (lines: Line[]) => lines.filter((l) => l.itemId && l.quantity).map((l) => ({
  itemId: l.itemId, quantity: l.quantity, ...(l.uomId ? { uomId: l.uomId } : {}), ...(l.batchNo ? { batchNo: l.batchNo } : {}),
  ...(l.expiryDate ? { expiryDate: l.expiryDate } : {}), ...(l.unitCost ? { unitCost: l.unitCost } : {}),
}));

/** Prompt that posts one text field (e.g. a resolution). */
function PromptButton({ label: lbl, path, field, title }: { label: string; path: string; field: string; title: string }) {
  const [open, setOpen] = useState(false);
  const [v, setV] = useState('');
  const send = useSend<Record<string, string>>('POST', path, INV);
  return (
    <>
      <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setOpen(true)}>{lbl}</button>
      <Modal open={open} onClose={() => setOpen(false)} title={title} actions={<>
        <button className="oc-btn oc-btn-neutral" onClick={() => setOpen(false)}>Cancel</button>
        <button className="oc-btn oc-btn-primary" disabled={!v || send.isPending} onClick={() => send.mutate({ [field]: v }, { onSuccess: () => setOpen(false) })}>{lbl}</button>
      </>}>
        <ErrorAlert error={send.error} />
        <TextArea label={title} value={v} onChange={setV} />
      </Modal>
    </>
  );
}

// ── Stock Balance, Stock Movement, Stock Card ─────────────────────────────

export function StockBalancePage({ ops }: { ops?: boolean }) {
  const { can } = useAuth();
  const [wh, setWh] = useState('');
  const [q, setQ] = useState('');
  const [low, setLow] = useState(false);
  const [batch, setBatch] = useState(false);
  const [tab, setTab] = useState('balance');
  const [card, setCard] = useState<R | null>(null);
  const [importing, setImporting] = useState(false);
  const toast = useToast();
  const list = useGet<Page<R>>(`/api/v1/inventory/stock-balances${qs({ warehouseId: wh, q, belowReorder: low || undefined, byBatch: batch || undefined, limit: 500 })}`);
  const expiry = useGet<Page<R>>(tab === 'expiry' ? `/api/v1/inventory/expiry${qs({ warehouseId: wh, days: 30 })}` : null);
  return (
    <div className="oc-stack">
      <PageHeader title="Stock Balance" help="Stock per warehouse × item (× batch) with value at moving average / FIFO cost; quantities in stock UOM and packs."
        actions={!ops && can('inventory.opening_stock.import') ? <button className="oc-btn oc-btn-neutral" onClick={() => setImporting(true)}>Import Opening Stock</button> : undefined} />
      <Tabs tabs={[{ value: 'balance', label: 'Stock Balance' }, { value: 'expiry', label: 'Expiring Batches' }]} value={tab} onChange={setTab} />
      <div className="oc-row-wrap">
        <WarehouseSelect value={wh} onChange={setWh} all />
        {tab === 'balance' && <>
          <TextField label="Item" value={q} onChange={setQ} placeholder="Code, name or barcode" />
          <Checkbox label="Below reorder point" checked={low} onChange={setLow} />
          <Checkbox label="Per batch" checked={batch} onChange={setBatch} />
        </>}
      </div>
      <ScanField label="Scan to find an item" onCode={(c) => void lookup(c).then((b) => setQ(String(b.itemCode))).catch(() => toast(`Barcode ${c} is not known`, 'error'))} />
      {tab === 'balance' ? (
        <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error} onRowClick={(r) => setCard(r)} rowKey={(r) => `${String(r.warehouseId)}-${String(r.itemId)}-${String(r.batchId)}`}
          columns={[{ key: 'warehouseName', header: 'Warehouse' }, { key: 'itemCode', header: 'Code' }, { key: 'itemName', header: 'Item' },
            { key: 'category', header: 'Category', render: (r) => qty(r.category) }, ...(batch ? [{ key: 'batchNo', header: 'Batch', render: (r: R) => qty(r.batchNo) },
              { key: 'expiryDate', header: 'Expiry', render: (r: R) => (r.expiryDate ? formatDate(String(r.expiryDate)) : '—') }] : []),
            { key: 'quantity', header: 'Quantity', align: 'right', render: (r) => `${String(r.quantity)} ${String(r.uom)}` },
            { key: 'packQuantity', header: 'Packs' }, { key: 'unitCost', header: 'Unit Cost', align: 'right', render: (r) => money(r.unitCost) },
            { key: 'value', header: 'Value', align: 'right', render: (r) => money(r.value) },
            { key: 'belowReorder', header: 'Reorder', render: (r) => (r.belowReorder ? <StatusPill status="warning" label="Below reorder point" /> : '—') }]} />
      ) : (
        <DataTable rows={expiry.data?.items} loading={expiry.isLoading} error={expiry.error} rowKey={(r) => `${String(r.batchId)}-${String(r.warehouseId)}`}
          columns={[{ key: 'expiryDate', header: 'Expiry', render: (r) => formatDate(String(r.expiryDate)) }, { key: 'daysLeft', header: 'Days left' },
            { key: 'warehouseCode', header: 'Warehouse' }, { key: 'itemName', header: 'Item' }, { key: 'batchNo', header: 'Batch' },
            { key: 'quantity', header: 'Quantity', align: 'right', render: (r) => `${String(r.quantity)} ${String(r.uom)}` },
            { key: 'value', header: 'Value', align: 'right', render: (r) => money(r.value) }]} />
      )}
      {card && <StockCardDrawer item={String(card.itemId)} warehouse={String(card.warehouseId)} title={`${String(card.itemName)} · ${String(card.warehouseCode)}`} onClose={() => setCard(null)} />}
      {importing && <OpeningStockImport onClose={() => setImporting(false)} />}
    </div>
  );
}

function StockCardDrawer({ item, warehouse, title, onClose }: { item: string; warehouse: string; title: string; onClose: () => void }) {
  const first = `${today().slice(0, 8)}01`;
  const [from, setFrom] = useState(first);
  const [to, setTo] = useState(today());
  const c = useGet<R>(`/api/v1/inventory/stock-card${qs({ itemId: item, warehouseId: warehouse, from, to })}`);
  const d = c.data;
  return (
    <Drawer open onClose={onClose} title={`Stock Card · ${title}`}>
      <div className="oc-row-wrap">
        <TextField label="From" type="date" value={from} onChange={setFrom} />
        <TextField label="To" type="date" value={to} onChange={setTo} />
      </div>
      <ErrorAlert error={c.error} />
      {d && <KV items={[['Opening', `${String(d.openingQuantity)} ${String(d.uom)} · ${money(d.openingValue)}`], ['In', String(d.inQuantity)], ['Out', String(d.outQuantity)],
        ['Closing', `${String(d.closingQuantity)} ${String(d.uom)} · ${money(d.closingValue)}`], ['On hand now', String(d.onHand)]]} />}
      <DataTable rows={(d?.lines as R[] | undefined)?.map((l, i) => ({ ...l, id: `${String(l.movementId)}-${i}` } as R))} loading={c.isLoading}
        columns={[{ key: 'businessDate', header: 'Date', render: (r) => formatDate(String(r.businessDate)) }, { key: 'number', header: 'Movement' },
          { key: 'movementType', header: 'Type', render: (r) => label(r.movementType) }, { key: 'quantity', header: 'Qty', align: 'right' },
          { key: 'unitCost', header: 'Cost', align: 'right', render: (r) => money(r.unitCost) }, { key: 'balanceAfter', header: 'Balance', align: 'right' }]} />
    </Drawer>
  );
}

function OpeningStockImport({ onClose }: { onClose: () => void }) {
  const [csv, setCsv] = useState('warehouseCode,itemCode,quantity,unitCost,uom,batchNo,expiryDate\n');
  const [date, setDate] = useState(today());
  const [res, setRes] = useState<R | null>(null);
  const send = useSend<Record<string, string>, R>('POST', '/api/v1/inventory/opening-stock:import', INV);
  const run = (mode: string) => send.mutate({ mode, csv, businessDate: date, filename: 'opening-stock.csv' }, { onSuccess: setRes });
  return (
    <Modal open onClose={onClose} title="Import Opening Stock" wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button>
      <button className="oc-btn oc-btn-neutral" disabled={send.isPending} onClick={() => run('preview')}>Preview</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending || res?.status !== 'valid'} onClick={() => run('commit')}>Post opening stock</button>
    </>}>
      <p className="oc-small oc-muted">From the cut-over stock opname: one row per warehouse, item (and batch); unit cost per stock UOM or per the row's UOM.</p>
      <TextField label="Cut-over date" type="date" value={date} onChange={setDate} />
      <TextArea label="CSV" value={csv} onChange={setCsv} rows={8} />
      <ErrorAlert error={send.error} />
      {res && <Card title={`Result: ${label(res.status)}`} icon="fact_check">
        <KV items={[['Rows', String(res.totalRows)], ['Total value', money(res.totalValue)]]} />
        <DataTable rows={(res.warehouses as R[]).map((w) => ({ ...w, id: String(w.warehouseCode) } as R))} columns={[{ key: 'warehouseCode', header: 'Warehouse' },
          { key: 'lines', header: 'Lines' }, { key: 'value', header: 'Value', render: (r) => money(r.value) }, { key: 'adjustment', header: 'Adjustment', render: (r) => qty(r.adjustment) }]} />
        {(res.errors as R[]).length > 0 && <DataTable rows={(res.errors as R[]).map((e, i) => ({ ...e, id: String(i) } as R))} columns={[{ key: 'row', header: 'Row' },
          { key: 'field', header: 'Field' }, { key: 'message', header: 'Problem' }]} />}
      </Card>}
    </Modal>
  );
}

export function StockMovementPage() {
  const [params, setParams] = useSearchParams();
  const [wh, setWh] = useState('');
  const [type, setType] = useState('');
  const [flagged, setFlagged] = useState(false);
  const [from, setFrom] = useState('');
  const list = useGet<Page<R>>(`/api/v1/inventory/stock-movements${qs({ warehouseId: wh, 'filter[movementType]': type, flagged: flagged || undefined, from, limit: 200 })}`);
  const open = params.get('id');
  return (
    <div className="oc-stack">
      <PageHeader title="Stock Movement" help="Append-only ledger: every receipt, issue, transfer, adjustment, opname, waste, production and consumption with its cost." />
      <div className="oc-row-wrap">
        <WarehouseSelect value={wh} onChange={setWh} all />
        <SelectField label="Type" value={type} onChange={setType} options={MOVE_TYPES} placeholder="All types" />
        <TextField label="From" type="date" value={from} onChange={setFrom} />
        <Checkbox label="Flagged for review (negative stock)" checked={flagged} onChange={setFlagged} />
      </div>
      <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error} onRowClick={(r) => setParams({ id: r.id })}
        columns={[{ key: 'number', header: 'Movement' }, { key: 'businessDate', header: 'Date', render: (r) => formatDate(String(r.businessDate)) },
          { key: 'movementType', header: 'Type', render: (r) => label(r.movementType) }, { key: 'warehouseName', header: 'Warehouse' },
          { key: 'sourceType', header: 'Source', render: (r) => label(r.sourceType) }, { key: 'reason', header: 'Reason', render: (r) => qty(r.reason) },
          { key: 'totalCost', header: 'Value', align: 'right', render: (r) => money(r.totalCost) },
          { key: 'flagged', header: 'Review', render: (r) => (r.flagged ? <StatusPill status="warning" label="Negative stock" /> : '—') }]} />
      {open && <MovementDrawer id={open} onClose={() => setParams({})} />}
    </div>
  );
}

function MovementDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const m = useGet<R>(`/api/v1/inventory/stock-movements/${id}`);
  const d = m.data;
  return (
    <Drawer open onClose={onClose} title={`Stock Movement ${String(d?.number ?? '')}`}>
      {!d ? <Skeleton /> : <>
        <KV items={[['Type', label(d.movementType)], ['Date', formatDate(String(d.businessDate))], ['Warehouse', String(d.warehouseName)],
          ['Source', label(d.sourceType)], ['Cost center', qty(d.costCenter)], ['Reason', qty(d.reason)], ['Value', money(d.totalCost)],
          ['Posted', formatDateTime(String(d.postedAt))]]} />
        <DataTable rows={d.lines as R[]} columns={[{ key: 'itemCode', header: 'Code' }, { key: 'itemName', header: 'Item' }, { key: 'batchNo', header: 'Batch', render: (r) => qty(r.batchNo) },
          { key: 'quantity', header: 'Qty', align: 'right', render: (r) => `${String(r.quantity)} ${String(r.uom)}` },
          { key: 'unitCost', header: 'Unit Cost', align: 'right', render: (r) => money(r.unitCost) }, { key: 'totalCost', header: 'Total', align: 'right', render: (r) => money(r.totalCost) },
          { key: 'balanceAfter', header: 'Balance After', align: 'right' }]} />
        {can('inventory.stock_movement.reverse') && !d.reversalOf && !d.reversedBy && ['consumption', 'issue', 'waste'].includes(String(d.movementType)) && (
          <ActionButton label="Reverse (return to stock)" path={`/api/v1/inventory/stock-movements/${id}:reverse`} invalidate={INV} reason="required" danger />
        )}
      </>}
    </Drawer>
  );
}

// ── Store Requisition & Issue ─────────────────────────────────────────────

export function RequisitionsPage({ ops }: { ops?: boolean }) {
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const [modal, setModal] = useState<'' | 'new' | 'issue'>('');
  const [type, setType] = useState('');
  const open = params.get('id');
  return (
    <>
      <ListPage title="Store Requisition" help="Outlet / kitchen requests to the store, department issues and spare part requests; approval per Inventory Policies."
        path="/api/v1/inventory/requisitions" statuses={opts(['draft', 'submitted', 'approved', 'partially_issued', 'issued', 'closed', 'rejected', 'cancelled'])}
        extraQuery={{ 'filter[requestType]': type }}
        filters={<SelectField label="Request type" value={type} onChange={setType} placeholder="All" options={opts(['store', 'department', 'spare_part', 'issue'])} />}
        actions={<div className="oc-row-wrap">
          {can('inventory.requisition.create') && <button className="oc-btn oc-btn-primary" onClick={() => setModal('new')}>New Requisition</button>}
          {can('inventory.issue.create') && <button className="oc-btn oc-btn-neutral" onClick={() => setModal('issue')}>Issue Stock</button>}
        </div>}
        onRowClick={(r) => setParams({ id: r.id })}
        columns={[{ key: 'number', header: 'Requisition' }, { key: 'requestType', header: 'Type', render: (r) => label(r.requestType) },
          { key: 'sourceWarehouse', header: 'From' }, { key: 'requestingWarehouse', header: 'To', render: (r) => qty(r.requestingWarehouse ?? r.costCenter ?? r.assetCode) },
          { key: 'neededBy', header: 'Needed by', render: (r) => (r.neededBy ? formatDate(String(r.neededBy)) : '—') },
          { key: 'estimatedValue', header: 'Value', align: 'right', render: (r) => money(r.estimatedValue) }, { key: 'status', header: 'Status', render: pill('status') }]} />
      {modal === 'new' && <RequisitionForm ops={ops} onClose={() => setModal('')} onDone={(id) => { setModal(''); setParams({ id }); }} />}
      {modal === 'issue' && <IssueForm ops={ops} onClose={() => setModal('')} />}
      {open && <RequisitionDrawer id={open} onClose={() => setParams({})} />}
    </>
  );
}

function RequisitionForm({ onClose, onDone, ops }: { onClose: () => void; onDone: (id: string) => void; ops?: boolean }) {
  const [type, setType] = useState('store');
  const [to, setTo] = useState('');
  const [from, setFrom] = useState('');
  const [cc, setCc] = useState('');
  const [needed, setNeeded] = useState('');
  const [reason, setReason] = useState('');
  const [lines, setLines] = useState<Line[]>([emptyLine()]);
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/inventory/requisitions', INV, () => ({ 'Idempotency-Key': uuidv7() }));
  const body = { requestType: type, ...(to ? { requestingWarehouseId: to } : {}), ...(from ? { sourceWarehouseId: from } : {}), ...(cc ? { costCenter: cc } : {}),
    ...(needed ? { neededBy: needed } : {}), reason, submit: true, origin: ops ? 'ops' : 'manual', lines: linesBody(lines) };
  return (
    <Modal open onClose={onClose} title="New Store Requisition" wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending || linesBody(lines).length === 0} onClick={() => send.mutate(body, { onSuccess: (r) => onDone(r.id) })}>Submit</button>
    </>}>
      <ErrorAlert error={send.error} />
      <div className="oc-form">
        <SelectField label="Type" value={type} onChange={setType} options={opts(['store', 'department'])} />
        {type === 'store' ? <WarehouseSelect label="Requesting warehouse (kitchen / outlet)" value={to} onChange={setTo} required />
          : <TextField label="Cost center" value={cc} onChange={setCc} required />}
        <WarehouseSelect label="Issuing store (empty = its replenishing store)" value={from} onChange={setFrom} all />
        <TextField label="Needed by" type="date" value={needed} onChange={setNeeded} />
        <TextField label="Reason" value={reason} onChange={setReason} />
      </div>
      <LinesEditor lines={lines} onChange={setLines} />
    </Modal>
  );
}

function IssueForm({ onClose, ops }: { onClose: () => void; ops?: boolean }) {
  const toast = useToast();
  const [wh, setWh] = useState('');
  const [cc, setCc] = useState('');
  const [reason, setReason] = useState('');
  const [lines, setLines] = useState<Line[]>([emptyLine()]);
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/inventory/issues', INV, () => ({ 'Idempotency-Key': uuidv7() }));
  return (
    <Modal open onClose={onClose} title="Issue Stock" wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!wh || !reason || send.isPending} onClick={() => send.mutate({ warehouseId: wh, costCenter: cc, reason,
        origin: ops ? 'ops' : 'manual', lines: linesBody(lines) }, { onSuccess: (r) => { toast(`Issued ${String(r.number)}`); onClose(); } })}>Issue</button>
    </>}>
      <ErrorAlert error={send.error} />
      <div className="oc-form">
        <WarehouseSelect value={wh} onChange={setWh} required />
        <TextField label="Department / cost center" value={cc} onChange={setCc} placeholder="engineering, housekeeping…" />
        <TextField label="Reason" value={reason} onChange={setReason} required />
      </div>
      <LinesEditor lines={lines} onChange={setLines} />
    </Modal>
  );
}

function RequisitionDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const r = useGet<R>(`/api/v1/inventory/requisitions/${id}`);
  const d = r.data;
  const base = `/api/v1/inventory/requisitions/${id}`;
  const st = String(d?.status ?? '');
  return (
    <Drawer open onClose={onClose} title={`Requisition ${String(d?.number ?? '')}`}>
      {!d ? <Skeleton /> : <>
        <KV items={[['Status', <StatusPill key="s" status={st.replace(/_/g, '-')} label={label(st)} />], ['Type', label(d.requestType)], ['From', String(d.sourceWarehouse)],
          ['To', qty(d.requestingWarehouse ?? d.costCenter)], ['Asset', qty(d.assetCode)], ['Requested by', qty(d.requestedByName)], ['Value', money(d.estimatedValue)],
          ['Reason', qty(d.reason)]]} />
        <DataTable rows={d.lines as R[]} columns={[{ key: 'itemName', header: 'Item' }, { key: 'baseQuantity', header: 'Requested', align: 'right' },
          { key: 'issuedQuantity', header: 'Issued', align: 'right' }, { key: 'remainingQuantity', header: 'Remaining', align: 'right' },
          { key: 'availableAtSource', header: 'At store', align: 'right' }, { key: 'uom', header: 'UOM' }]} />
        <div className="oc-row-wrap">
          {st === 'draft' && can('inventory.requisition.submit') && <ActionButton label="Submit" kind="primary" path={`${base}:submit`} invalidate={INV} />}
          {['approved', 'partially_issued'].includes(st) && can('inventory.requisition.fulfill') && (
            <ActionButton label="Issue all remaining" kind="primary" path={`${base}:fulfill`} body={{}} invalidate={INV} />
          )}
          {['approved', 'partially_issued'].includes(st) && can('inventory.requisition.request_purchase') && (
            <ActionButton label="Request purchase for shortage" path={`${base}:request-purchase`} invalidate={INV} />
          )}
          {['draft', 'submitted', 'approved', 'partially_issued'].includes(st) && can('inventory.requisition.cancel') && (
            <ActionButton label={st === 'partially_issued' ? 'Close (cancel rest)' : 'Cancel'} path={`${base}:cancel`} invalidate={INV} reason="required" danger />
          )}
        </div>
      </>}
    </Drawer>
  );
}

// ── Stock Transfer ────────────────────────────────────────────────────────

export function TransfersPage() {
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const [modal, setModal] = useState(false);
  const open = params.get('id');
  return (
    <>
      <ListPage title="Stock Transfer" help="Transfers between locations stay In Transit until received; differences are recorded." path="/api/v1/inventory/transfers"
        statuses={opts(['draft', 'in_transit', 'received', 'cancelled'])} onRowClick={(r) => setParams({ id: r.id })}
        actions={can('inventory.transfer.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setModal(true)}>New Transfer</button> : undefined}
        columns={[{ key: 'number', header: 'Transfer' }, { key: 'fromWarehouse', header: 'From' }, { key: 'toWarehouse', header: 'To' },
          { key: 'shippedValue', header: 'Value', align: 'right', render: (r) => money(r.shippedValue) },
          { key: 'discrepancyValue', header: 'Difference', align: 'right', render: (r) => money(r.discrepancyValue) }, { key: 'status', header: 'Status', render: pill('status') }]} />
      {modal && <TransferForm onClose={() => setModal(false)} onDone={(id) => { setModal(false); setParams({ id }); }} />}
      {open && <TransferDrawer id={open} onClose={() => setParams({})} />}
    </>
  );
}

function TransferForm({ onClose, onDone }: { onClose: () => void; onDone: (id: string) => void }) {
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [ship, setShip] = useState(true);
  const [lines, setLines] = useState<Line[]>([emptyLine()]);
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/inventory/transfers', INV, () => ({ 'Idempotency-Key': uuidv7() }));
  return (
    <Modal open onClose={onClose} title="New Stock Transfer" wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!from || !to || send.isPending} onClick={() => send.mutate({ fromWarehouseId: from, toWarehouseId: to, ship,
        lines: linesBody(lines) }, { onSuccess: (r) => onDone(r.id) })}>{ship ? 'Create & ship' : 'Create'}</button>
    </>}>
      <ErrorAlert error={send.error} />
      <div className="oc-form">
        <WarehouseSelect label="From" value={from} onChange={setFrom} required />
        <WarehouseSelect label="To" value={to} onChange={setTo} required />
        <Checkbox label="Ship now (In Transit)" checked={ship} onChange={setShip} />
      </div>
      <LinesEditor lines={lines} onChange={setLines} />
    </Modal>
  );
}

function TransferDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const t = useGet<R>(`/api/v1/inventory/transfers/${id}`);
  const d = t.data;
  const [received, setReceived] = useState<Record<string, string>>({});
  const base = `/api/v1/inventory/transfers/${id}`;
  const lines = (d?.lines as R[] | undefined) ?? [];
  const recv = { lines: lines.map((l) => ({ lineId: l.id, receivedQuantity: received[l.id] ?? String(l.shippedQuantity) })) };
  return (
    <Drawer open onClose={onClose} title={`Transfer ${String(d?.number ?? '')}`}>
      {!d ? <Skeleton /> : <>
        <KV items={[['Status', label(d.status)], ['From', String(d.fromWarehouse)], ['To', String(d.toWarehouse)], ['Value', money(d.shippedValue)],
          ['Difference', money(d.discrepancyValue)]]} />
        <DataTable rows={lines} columns={[{ key: 'itemName', header: 'Item' }, { key: 'quantity', header: 'Qty', align: 'right' },
          { key: 'shippedQuantity', header: 'Shipped', align: 'right' },
          { key: 'receivedQuantity', header: 'Received', align: 'right', render: (l) => (d.status === 'in_transit'
            ? <TextField label={`Received ${String(l.itemName)}`} type="number" value={received[l.id] ?? String(l.shippedQuantity)} onChange={(v) => setReceived((x) => ({ ...x, [l.id]: v }))} />
            : String(l.receivedQuantity)) },
          { key: 'discrepancyReason', header: 'Reason', render: (l) => qty(l.discrepancyReason) }]} />
        <div className="oc-row-wrap">
          {d.status === 'draft' && can('inventory.transfer.ship') && <ActionButton label="Ship" kind="primary" path={`${base}:ship`} invalidate={INV} />}
          {d.status === 'in_transit' && can('inventory.transfer.receive') && <ActionButton label="Receive" kind="primary" path={`${base}:receive`} body={recv} invalidate={INV} />}
          {d.status === 'draft' && can('inventory.transfer.cancel') && <ActionButton label="Cancel" path={`${base}:cancel`} invalidate={INV} reason="required" danger />}
        </div>
      </>}
    </Drawer>
  );
}

// ── Stock Adjustment ──────────────────────────────────────────────────────

export function AdjustmentsPage() {
  const { can } = useAuth();
  const [modal, setModal] = useState(false);
  return (
    <>
      <ListPage title="Stock Adjustment" help="Manual adjustments with reason; above the Inventory Policies threshold they need approval." path="/api/v1/inventory/adjustments"
        statuses={opts(['draft', 'pending_approval', 'posted', 'rejected', 'cancelled'])}
        actions={can('inventory.adjustment.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setModal(true)}>New Adjustment</button> : undefined}
        columns={[{ key: 'number', header: 'Adjustment' }, { key: 'warehouse', header: 'Warehouse' }, { key: 'reason', header: 'Reason', render: (r) => label(r.reason) },
          { key: 'businessDate', header: 'Date', render: (r) => (r.businessDate ? formatDate(String(r.businessDate)) : '—') },
          { key: 'totalValue', header: 'Value', align: 'right', render: (r) => money(r.totalValue) }, { key: 'status', header: 'Status', render: pill('status') }]}
        rowActions={(r) => <div className="oc-row-wrap">
          {r.status === 'draft' && can('inventory.adjustment.submit') && <ActionButton label="Submit" path={`/api/v1/inventory/adjustments/${r.id}:submit`} invalidate={INV} />}
          {['draft', 'pending_approval'].includes(String(r.status)) && can('inventory.adjustment.cancel') && (
            <ActionButton label="Cancel" path={`/api/v1/inventory/adjustments/${r.id}:cancel`} invalidate={INV} reason="required" danger />
          )}
        </div>} />
      {modal && <AdjustmentForm onClose={() => setModal(false)} />}
    </>
  );
}

function AdjustmentForm({ onClose }: { onClose: () => void }) {
  const [wh, setWh] = useState('');
  const [reason, setReason] = useState('damage');
  const [date, setDate] = useState('');
  const [notes, setNotes] = useState('');
  const [lines, setLines] = useState<Line[]>([emptyLine()]);
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/inventory/adjustments', INV, () => ({ 'Idempotency-Key': uuidv7() }));
  return (
    <Modal open onClose={onClose} title="New Stock Adjustment" wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!wh || send.isPending} onClick={() => send.mutate({ warehouseId: wh, reason, ...(date ? { businessDate: date } : {}), notes,
        submit: true, lines: linesBody(lines) }, { onSuccess: onClose })}>Submit</button>
    </>}>
      <ErrorAlert error={send.error} />
      <div className="oc-form">
        <WarehouseSelect value={wh} onChange={setWh} required />
        <SelectField label="Reason" value={reason} onChange={setReason} options={ADJ_REASONS} />
        <TextField label="Date" type="date" value={date} onChange={setDate} help="Default today; not in a closed accounting period" />
        <TextField label="Notes" value={notes} onChange={setNotes} />
      </div>
      <LinesEditor lines={lines} onChange={setLines} incoming signed />
    </Modal>
  );
}

// ── Stock Opname ──────────────────────────────────────────────────────────

export function OpnamesPage() {
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const [start, setStart] = useState(false);
  const open = params.get('id');
  return (
    <>
      <ListPage title="Stock Opname" help="Snapshot, blind count with barcode, variance with tolerance, recount or approval, then the opname adjustment."
        path="/api/v1/inventory/stock-opnames" statuses={opts(['in_progress', 'counted', 'pending_approval', 'posted', 'cancelled'])} search={false}
        actions={can('inventory.stock_opname.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setStart(true)}>Start Stock Opname</button> : undefined}
        onRowClick={(r) => setParams({ id: r.id })}
        columns={[{ key: 'number', header: 'Stock Opname' }, { key: 'warehouse', header: 'Warehouse' },
          { key: 'snapshotAt', header: 'Snapshot', render: (r) => formatDateTime(String(r.snapshotAt)) },
          { key: 'countedLines', header: 'Counted', render: (r) => `${String(r.countedLines)} / ${String(r.lineCount)}` },
          { key: 'overTolerance', header: 'Above tolerance' }, { key: 'varianceValue', header: 'Variance', align: 'right', render: (r) => money(r.varianceValue) },
          { key: 'status', header: 'Status', render: pill('status') }]} />
      {start && <StartOpname onClose={() => setStart(false)} onDone={(id) => { setStart(false); setParams({ id }); }} />}
      {open && <OpnameDrawer id={open} onClose={() => setParams({})} />}
    </>
  );
}

function StartOpname({ onClose, onDone }: { onClose: () => void; onDone: (id: string) => void }) {
  const [wh, setWh] = useState('');
  const [blind, setBlind] = useState(true);
  const [freeze, setFreeze] = useState(false);
  const cats = useGet<Page<R>>('/api/v1/inventory/categories?limit=200');
  const [cat, setCat] = useState('');
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/inventory/stock-opnames', INV, () => ({ 'Idempotency-Key': uuidv7() }));
  return (
    <Modal open onClose={onClose} title="Start Stock Opname" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!wh || send.isPending} onClick={() => send.mutate({ warehouseId: wh, blind, freeze, ...(cat ? { categoryId: cat } : {}) },
        { onSuccess: (o) => onDone(o.id) })}>Start</button>
    </>}>
      <ErrorAlert error={send.error} />
      <WarehouseSelect value={wh} onChange={setWh} required />
      <SelectField label="Category (optional)" value={cat} onChange={setCat} placeholder="All categories"
        options={(cats.data?.items ?? []).map((c) => ({ value: c.id, label: String(c.name) }))} />
      <Checkbox label="Blind count (counters do not see the system quantity)" checked={blind} onChange={setBlind} />
      <Checkbox label="Freeze movements of the warehouse while counting" checked={freeze} onChange={setFreeze} />
    </Modal>
  );
}

/** Count sheet with scanning; offline counts are queued and synced once. */
function CountSheet({ o, onCounted }: { o: R; onCounted: () => void }) {
  const toast = useToast();
  const { propertyId } = useAuth();
  const [counts, setCounts] = useState<Record<string, string>>({});
  const lines = (o.lines as R[] | undefined) ?? [];
  const sendCount = async (body: { lines: Record<string, unknown>[] }) => {
    if (!navigator.onLine) {
      await enqueue('inventory.opname_count', { opnameId: o.id, lines: body.lines }, propertyId ?? '');
      toast('Offline: count queued and will sync once');
      return;
    }
    await request('POST', `/api/v1/inventory/stock-opnames/${o.id}:count`, body, { 'Idempotency-Key': uuidv7() });
    onCounted();
  };
  const scan = (code: string) => void sendCount({ lines: [{ barcode: code, quantity: '1', mode: 'add' }] }).catch((e: Error) => toast(e.message, 'error'));
  const save = () => {
    const ls = Object.entries(counts).filter(([, v]) => v !== '').map(([lineId, v]) => {
      const l = lines.find((x) => x.id === lineId)!;
      return { itemId: l.itemId, quantity: v, ...(l.batchNo ? { batchNo: l.batchNo } : {}) };
    });
    if (ls.length) void sendCount({ lines: ls }).then(() => setCounts({})).catch((e: Error) => toast(e.message, 'error'));
  };
  return (
    <div className="oc-stack">
      <ScanField label="Scan to count (+1 per scan; carton barcodes count cartons)" onCode={scan} />
      <DataTable rows={lines} columns={[{ key: 'itemCode', header: 'Code' }, { key: 'itemName', header: 'Item' }, { key: 'batchNo', header: 'Batch', render: (l) => qty(l.batchNo) },
        { key: 'systemQuantity', header: 'System', align: 'right', render: (l) => (l.systemQuantity == null ? 'blind' : String(l.systemQuantity)) },
        { key: 'countedQuantity', header: 'Counted', render: (l) => <TextField label={`Count ${String(l.itemName)}`} type="number"
          value={counts[l.id] ?? (l.countedQuantity == null ? '' : String(l.countedQuantity))} onChange={(v) => setCounts((c) => ({ ...c, [l.id]: v }))} /> },
        { key: 'uom', header: 'UOM' }]} />
      <div><button className="oc-btn oc-btn-primary" onClick={save}>Save counts</button></div>
    </div>
  );
}

function OpnameDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const o = useGet<R>(`/api/v1/inventory/stock-opnames/${id}`);
  const d = o.data;
  const base = `/api/v1/inventory/stock-opnames/${id}`;
  const st = String(d?.status ?? '');
  return (
    <Drawer open onClose={onClose} title={`Stock Opname ${String(d?.number ?? '')}`}>
      {!d ? <Skeleton /> : <>
        <KV items={[['Status', label(st)], ['Warehouse', String(d.warehouse)], ['Blind', d.blind ? 'Yes' : 'No'], ['Freeze', d.freeze ? 'Yes' : 'No (recorded cut-off)'],
          ['Tolerance', `${String(d.tolerancePercent)} %`], ['System value', money(d.systemValue)], ['Variance', money(d.varianceValue)]]} />
        {st === 'in_progress' && can('inventory.stock_opname.count') ? <CountSheet o={d} onCounted={() => void o.refetch()} /> : (
          <DataTable rows={d.lines as R[]} columns={[{ key: 'itemName', header: 'Item' }, { key: 'expectedQuantity', header: 'Expected', align: 'right', render: (l) => qty(l.expectedQuantity) },
            { key: 'countedQuantity', header: 'Counted', align: 'right', render: (l) => qty(l.countedQuantity) },
            { key: 'varianceQuantity', header: 'Variance', align: 'right', render: (l) => qty(l.varianceQuantity) },
            { key: 'varianceValue', header: 'Value', align: 'right', render: (l) => money(l.varianceValue) },
            { key: 'overTolerance', header: 'Tolerance', render: (l) => (l.overTolerance ? <StatusPill status="warning" label="Above" /> : 'OK') }]} />
        )}
        <div className="oc-row-wrap">
          {st === 'in_progress' && can('inventory.stock_opname.submit') && (
            <ActionButton label="Close count" kind="primary" path={`${base}:submit`} body={{ zeroUncounted: true }} invalidate={INV} confirm="Lines not counted are counted as zero." />
          )}
          {st === 'counted' && can('inventory.stock_opname.submit') && <ActionButton label="Recount above tolerance" path={`${base}:recount`} body={{}} invalidate={INV} />}
          {st === 'counted' && can('inventory.stock_opname.post') && <ActionButton label="Post variance" kind="primary" path={`${base}:post`} invalidate={INV} />}
          {['in_progress', 'counted', 'pending_approval'].includes(st) && can('inventory.stock_opname.cancel') && (
            <ActionButton label="Cancel" path={`${base}:cancel`} invalidate={INV} reason="required" danger />
          )}
        </div>
      </>}
    </Drawer>
  );
}

// ── Valuation, consumption variance, replenishment ────────────────────────

export function ValuationPage() {
  const [tab, setTab] = useState('valuation');
  const [asOf, setAsOf] = useState(today());
  const [groupBy, setGroupBy] = useState('warehouse');
  const [from, setFrom] = useState(`${today().slice(0, 8)}01`);
  const [to, setTo] = useState(today());
  const [wh, setWh] = useState('');
  const v = useGet<R>(tab === 'valuation' ? `/api/v1/inventory/valuation${qs({ asOf, groupBy, warehouseId: wh })}` : null);
  const cv = useGet<Page<R>>(tab === 'variance' ? `/api/v1/inventory/consumption-variance${qs({ from, to, warehouseId: wh })}` : null);
  return (
    <div className="oc-stack">
      <PageHeader title="Stock Valuation" help="Stock value as of a date (moving average / FIFO) — the balance of the inventory account; theoretical vs actual consumption per outlet." />
      <Tabs tabs={[{ value: 'valuation', label: 'Stock Valuation' }, { value: 'variance', label: 'Theoretical vs Actual' }]} value={tab} onChange={setTab} />
      <div className="oc-row-wrap">
        <WarehouseSelect value={wh} onChange={setWh} all />
        {tab === 'valuation' ? <>
          <TextField label="As of" type="date" value={asOf} onChange={setAsOf} />
          <SelectField label="Group by" value={groupBy} onChange={setGroupBy} options={opts(['warehouse', 'category', 'item'])} />
        </> : <>
          <TextField label="From" type="date" value={from} onChange={setFrom} />
          <TextField label="To" type="date" value={to} onChange={setTo} />
        </>}
      </div>
      {tab === 'valuation' ? <>
        <ErrorAlert error={v.error} />
        <div className="oc-card oc-card-ink"><div className="oc-small">Inventory value</div><div className="oc-metric">{money(v.data?.total)}</div></div>
        <DataTable rows={(v.data?.rows as R[] | undefined)?.map((r) => ({ ...r, id: String(r.key) } as R))} loading={v.isLoading}
          columns={[{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'group', header: 'Group', render: (r) => label(r.group) },
            { key: 'quantity', header: 'Quantity', align: 'right', render: (r) => (r.quantity ? `${String(r.quantity)} ${String(r.uom)}` : '—') },
            { key: 'unitCost', header: 'Unit Cost', align: 'right', render: (r) => money(r.unitCost) }, { key: 'value', header: 'Value', align: 'right', render: (r) => money(r.value) }]} />
      </> : (
        <DataTable rows={cv.data?.items?.map((r) => ({ ...r, id: `${String(r.warehouseId)}-${String(r.itemId)}` } as R))} loading={cv.isLoading} error={cv.error}
          columns={[{ key: 'warehouseCode', header: 'Outlet store' }, { key: 'itemName', header: 'Item' }, { key: 'opening', header: 'Opening', align: 'right' },
            { key: 'received', header: 'Received', align: 'right' }, { key: 'closing', header: 'Closing', align: 'right' }, { key: 'actual', header: 'Actual', align: 'right' },
            { key: 'theoretical', header: 'Theoretical', align: 'right' }, { key: 'variance', header: 'Variance', align: 'right' },
            { key: 'varianceValue', header: 'Variance Value', align: 'right', render: (r) => money(r.varianceValue) }]} />
      )}
    </div>
  );
}

export function ReplenishmentPage() {
  const { can } = useAuth();
  const [tab, setTab] = useState('suggestions');
  const [res, setRes] = useState<R | null>(null);
  const s = useGet<Page<R>>(tab === 'suggestions' ? '/api/v1/inventory/replenishment' : null);
  const slow = useGet<Page<R>>(tab === 'slow' ? '/api/v1/inventory/slow-moving' : null);
  return (
    <div className="oc-stack">
      <PageHeader title="Reorder Point" help="Par stock, minimum stock and reorder points; the daily job requests purchase requisitions and outlet requisitions."
        actions={can('inventory.replenishment.run') ? <ActionButton label="Run replenishment now" kind="primary" path="/api/v1/inventory/replenishment:run" invalidate={INV}
          onDone={(r) => setRes(r as R)} /> : undefined} />
      <Tabs tabs={[{ value: 'suggestions', label: 'Suggestions' }, { value: 'points', label: 'Reorder Points' }, { value: 'par', label: 'Par Stock' },
        { value: 'slow', label: 'Slow Moving' }]} value={tab} onChange={setTab} />
      {res && <Card title="Replenishment run" icon="autorenew"><KV items={[['Business date', String(res.businessDate)],
        ['Purchase requests', String((res.purchase as R[]).length)], ['Outlet requisitions', (res.requisitions as string[]).join(', ') || '—'], ['Already requested', String(res.skipped)]]} /></Card>}
      {tab === 'suggestions' && <DataTable rows={s.data?.items?.map((r) => ({ ...r, id: `${String(r.warehouseId)}-${String(r.itemId)}` } as R))} loading={s.isLoading} error={s.error}
        columns={[{ key: 'kind', header: 'Kind', render: (r) => (r.kind === 'purchase' ? 'Purchase Requisition' : 'Store Requisition') }, { key: 'warehouseCode', header: 'Warehouse' },
          { key: 'itemName', header: 'Item' }, { key: 'onHand', header: 'On hand', align: 'right' }, { key: 'onOrder', header: 'On order', align: 'right' },
          { key: 'reorderPoint', header: 'Reorder point', align: 'right' }, { key: 'parLevel', header: 'Par', align: 'right' },
          { key: 'suggestedQuantity', header: 'Suggested', align: 'right', render: (r) => `${String(r.suggestedQuantity)} ${String(r.uom)}` }]} />}
      {tab === 'points' && <AutoResourcePage resourceKey="inventory.reorder_point" />}
      {tab === 'par' && <AutoResourcePage resourceKey="inventory.par_stock" />}
      {tab === 'slow' && <DataTable rows={slow.data?.items?.map((r) => ({ ...r, id: `${String(r.warehouseId)}-${String(r.itemId)}` } as R))} loading={slow.isLoading}
        columns={[{ key: 'warehouseCode', header: 'Warehouse' }, { key: 'itemName', header: 'Item' }, { key: 'quantity', header: 'Quantity', align: 'right' },
          { key: 'value', header: 'Value', align: 'right', render: (r) => money(r.value) }, { key: 'lastOutbound', header: 'Last outbound', render: (r) => (r.lastOutbound ? formatDate(String(r.lastOutbound)) : '—') },
          { key: 'daysIdle', header: 'Days idle', align: 'right' }]} />}
    </div>
  );
}

// ── Production & Waste ────────────────────────────────────────────────────

export function ProductionPage() {
  const { can } = useAuth();
  const [modal, setModal] = useState(false);
  const [actual, setActual] = useState<Record<string, string>>({});
  return (
    <>
      <ListPage title="Production" help="Production orders for semi-finished items (sambal, stock, sauce): ingredients out, output in at the ingredient cost with the actual yield."
        path="/api/v1/inventory/production-orders" statuses={opts(['draft', 'completed', 'cancelled'])} search={false}
        actions={can('inventory.production.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setModal(true)}>Create Production Order</button> : undefined}
        columns={[{ key: 'number', header: 'Order' }, { key: 'outputItem', header: 'Output' }, { key: 'warehouse', header: 'Kitchen' },
          { key: 'scheduledFor', header: 'Scheduled', render: (r) => (r.scheduledFor ? formatDate(String(r.scheduledFor)) : '—') },
          { key: 'plannedQuantity', header: 'Planned', align: 'right', render: (r) => `${String(r.plannedQuantity)} ${String(r.uom)}` },
          { key: 'actualQuantity', header: 'Actual', align: 'right', render: (r) => (r.status === 'draft' && can('inventory.production.complete')
            ? <TextField label="Actual yield" type="number" value={actual[r.id] ?? ''} onChange={(v) => setActual((a) => ({ ...a, [r.id]: v }))} /> : qty(r.actualQuantity)) },
          { key: 'sourceRef', header: 'Source', render: (r) => qty(r.sourceRef) }, { key: 'status', header: 'Status', render: pill('status') }]}
        rowActions={(r) => r.status === 'draft' ? <div className="oc-row-wrap">
          {can('inventory.production.complete') && <ActionButton label="Complete" kind="primary" path={`/api/v1/inventory/production-orders/${r.id}:complete`}
            body={actual[r.id] ? { actualQuantity: actual[r.id] } : {}} invalidate={INV} />}
          {can('inventory.production.cancel') && <ActionButton label="Cancel" path={`/api/v1/inventory/production-orders/${r.id}:cancel`} invalidate={INV} reason="required" danger />}
        </div> : null} />
      {modal && <ProductionForm onClose={() => setModal(false)} />}
    </>
  );
}

function ProductionForm({ onClose }: { onClose: () => void }) {
  const recipes = useGet<Page<R>>('/api/v1/inventory/recipes?limit=500&filter[recipeType]=sub_recipe&filter[status]=active');
  const [recipe, setRecipe] = useState('');
  const [wh, setWh] = useState('');
  const [planned, setPlanned] = useState('');
  const [date, setDate] = useState(today());
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/inventory/production-orders', INV, () => ({ 'Idempotency-Key': uuidv7() }));
  return (
    <Modal open onClose={onClose} title="Create Production Order" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!recipe || !wh || !planned || send.isPending} onClick={() => send.mutate({ recipeId: recipe, warehouseId: wh,
        plannedQuantity: planned, scheduledFor: date }, { onSuccess: onClose })}>Create</button>
    </>}>
      <ErrorAlert error={send.error} />
      <SelectField label="Sub-recipe" value={recipe} onChange={setRecipe} options={(recipes.data?.items ?? []).map((r) => ({ value: r.id, label: String(r.name) }))} />
      <WarehouseSelect label="Kitchen" value={wh} onChange={setWh} required />
      <TextField label="Planned output (stock UOM of the output item)" type="number" value={planned} onChange={setPlanned} />
      <TextField label="Scheduled for" type="date" value={date} onChange={setDate} />
    </Modal>
  );
}

export function WastePage() {
  const { can } = useAuth();
  const [modal, setModal] = useState(false);
  return (
    <>
      <ListPage title="Waste" help="Waste and spoilage with reason per location at valuation cost." path="/api/v1/inventory/waste" search={false}
        actions={can('inventory.waste.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setModal(true)}>Record Waste</button> : undefined}
        columns={[{ key: 'number', header: 'Waste' }, { key: 'businessDate', header: 'Date', render: (r) => formatDate(String(r.businessDate)) },
          { key: 'warehouse', header: 'Location' }, { key: 'reason', header: 'Reason', render: (r) => label(r.reason) },
          { key: 'totalCost', header: 'Cost', align: 'right', render: (r) => money(r.totalCost) }, { key: 'recordedByName', header: 'Recorded by', render: (r) => qty(r.recordedByName) }]} />
      {modal && <WasteForm onClose={() => setModal(false)} />}
    </>
  );
}

function WasteForm({ onClose }: { onClose: () => void }) {
  const toast = useToast();
  const [wh, setWh] = useState('');
  const [reason, setReason] = useState('spoiled');
  const [notes, setNotes] = useState('');
  const [lines, setLines] = useState<Line[]>([emptyLine()]);
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/inventory/waste', INV, () => ({ 'Idempotency-Key': uuidv7() }));
  return (
    <Modal open onClose={onClose} title="Record Waste" wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!wh || send.isPending} onClick={() => send.mutate({ warehouseId: wh, reason, notes, lines: linesBody(lines) },
        { onSuccess: (w) => { toast(`Recorded ${String(w.number)}`); onClose(); } })}>Record</button>
    </>}>
      <ErrorAlert error={send.error} />
      <div className="oc-form">
        <WarehouseSelect label="Location" value={wh} onChange={setWh} required />
        <SelectField label="Reason" value={reason} onChange={setReason} options={WASTE_REASONS} />
        <TextField label="Notes" value={notes} onChange={setNotes} />
      </div>
      <LinesEditor lines={lines} onChange={setLines} />
    </Modal>
  );
}

// ── Consignment ───────────────────────────────────────────────────────────

export function ConsignmentPage() {
  const { can } = useAuth();
  const [period, setPeriod] = useState(today().slice(0, 7));
  const [modal, setModal] = useState(false);
  const stock = useGet<Page<R>>('/api/v1/inventory/consignment-stock');
  const st = useGet<Page<R>>(`/api/v1/inventory/consignment-settlements${qs({ period })}`);
  return (
    <div className="oc-stack">
      <PageHeader title="Consignment" help="Pro shop stock owned by suppliers until sold; monthly settlement with the club commission per supplier."
        actions={can('inventory.consignment.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setModal(true)}>Receive / Return</button> : undefined} />
      <Card title="Consignment stock" icon="inventory">
        <DataTable rows={stock.data?.items?.map((r) => ({ ...r, id: `${String(r.itemId)}-${String(r.warehouseId)}` } as R))} loading={stock.isLoading}
          columns={[{ key: 'supplierName', header: 'Supplier' }, { key: 'itemName', header: 'Item' }, { key: 'warehouse', header: 'Location' },
            { key: 'onHand', header: 'On hand', align: 'right', render: (r) => `${String(r.onHand)} ${String(r.uom)}` }]} />
      </Card>
      <Card title="Monthly settlement" icon="request_quote">
        <TextField label="Period" type="month" value={period} onChange={setPeriod} />
        <DataTable rows={st.data?.items?.map((r) => ({ ...r, id: String(r.supplierId) } as R))} loading={st.isLoading} error={st.error}
          columns={[{ key: 'supplierName', header: 'Supplier' }, { key: 'quantity', header: 'Sold', align: 'right' },
            { key: 'salesAmount', header: 'Sales', align: 'right', render: (r) => money(r.salesAmount) },
            { key: 'commissionAmount', header: 'Club commission', align: 'right', render: (r) => money(r.commissionAmount) },
            { key: 'payableAmount', header: 'Payable', align: 'right', render: (r) => money(r.payableAmount) },
            { key: 'settlementId', header: 'Settlement', render: (r) => (r.settlementId ? <StatusPill status="completed" label="Issued" /> : '—') }]}
          actions={(r) => (!r.settlementId && can('inventory.consignment.settle') ? <ActionButton label="Issue settlement" path="/api/v1/inventory/consignment-settlements"
            body={{ supplierId: r.supplierId, period }} invalidate={INV} /> : null)} />
      </Card>
      {modal && <ConsignmentForm onClose={() => setModal(false)} />}
    </div>
  );
}

function ConsignmentForm({ onClose }: { onClose: () => void }) {
  const sups = useGet<Page<R>>('/api/v1/procurement/suppliers?limit=500&filter[status]=active');
  const [dir, setDir] = useState('receipt');
  const [sup, setSup] = useState('');
  const [wh, setWh] = useState('');
  const [ref, setRef] = useState('');
  const [lines, setLines] = useState<Line[]>([emptyLine()]);
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/inventory/consignment-movements', INV, () => ({ 'Idempotency-Key': uuidv7() }));
  return (
    <Modal open onClose={onClose} title="Consignment Stock" wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!sup || !wh || send.isPending} onClick={() => send.mutate({ direction: dir, supplierId: sup, warehouseId: wh,
        reference: ref, lines: linesBody(lines) }, { onSuccess: onClose })}>Post</button>
    </>}>
      <ErrorAlert error={send.error} />
      <div className="oc-form">
        <SelectField label="Direction" value={dir} onChange={setDir} options={[{ value: 'receipt', label: 'Receive from supplier' }, { value: 'return', label: 'Return to supplier' }]} />
        <SelectField label="Supplier" value={sup} onChange={setSup} options={(sups.data?.items ?? []).map((s) => ({ value: s.id, label: String(s.name) }))} />
        <WarehouseSelect label="Location" value={wh} onChange={setWh} required />
        <TextField label="Delivery / return note" value={ref} onChange={setRef} />
      </div>
      <LinesEditor lines={lines} onChange={setLines} />
    </Modal>
  );
}

// ── Assets & Equipment ────────────────────────────────────────────────────

export function AssetsPage() {
  const [tab, setTab] = useState('register');
  return (
    <div className="oc-stack">
      <PageHeader title="Assets & Equipment" help="Asset register (golf carts, gym, rental clubs and rackets, banquet equipment), maintenance, usage and depreciation." />
      <Tabs tabs={[{ value: 'register', label: 'Asset Register' }, { value: 'operate', label: 'Rental & Usage' }, { value: 'due', label: 'Maintenance Due' },
        { value: 'work', label: 'Work Orders' }, { value: 'schedules', label: 'Maintenance Schedules' }, { value: 'depreciation', label: 'Depreciation' },
        { value: 'categories', label: 'Asset Categories' }]} value={tab} onChange={setTab} />
      {tab === 'register' && <AutoResourcePage resourceKey="inventory.asset" />}
      {tab === 'operate' && <AssetOperations />}
      {tab === 'due' && <MaintenanceDue />}
      {tab === 'work' && <WorkOrders />}
      {tab === 'schedules' && <AutoResourcePage resourceKey="inventory.maintenance_schedule" />}
      {tab === 'depreciation' && <Depreciation />}
      {tab === 'categories' && <AutoResourcePage resourceKey="inventory.asset_category" />}
    </div>
  );
}

function AssetOperations() {
  const { can } = useAuth();
  const [hist, setHist] = useState<R | null>(null);
  const [hours, setHours] = useState<Record<string, string>>({});
  const list = useGet<Page<R>>('/api/v1/inventory/assets?limit=500');
  return (
    <>
      <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error} onRowClick={setHist}
        columns={[{ key: 'code', header: 'Asset' }, { key: 'name', header: 'Name' }, { key: 'status', header: 'Status', render: pill('status') },
          { key: 'rentalStatus', header: 'Rental', render: (r) => (r.rentable ? label(r.rentalStatus) : '—') },
          { key: 'usageHours', header: 'Hours', align: 'right' }, { key: 'bookValue', header: 'Book value', align: 'right', render: (r) => money(r.bookValue) },
          { key: 'hours', header: 'Record hours', render: (r) => (can('inventory.asset.record_usage') && r.status !== 'disposed'
            ? <TextField label={`Hours ${String(r.code)}`} type="number" value={hours[r.id] ?? ''} onChange={(v) => setHours((h) => ({ ...h, [r.id]: v }))} /> : '—') }]}
        actions={(r) => <div className="oc-row-wrap">
          {Boolean(r.rentable) && r.rentalStatus === 'available' && r.status === 'active' && can('inventory.asset.rent') && <ActionButton label="Check out" path={`/api/v1/inventory/assets/${r.id}:checkout`} body={{}} invalidate={INV} />}
          {r.rentalStatus === 'out' && can('inventory.asset.rent') && <ActionButton label="Return" path={`/api/v1/inventory/assets/${r.id}:return`} body={{}} invalidate={INV} />}
          {hours[r.id] && <ActionButton label="Save hours" path={`/api/v1/inventory/assets/${r.id}:record-usage`} body={{ hours: hours[r.id] }} invalidate={INV} />}
          {r.status !== 'disposed' && can('inventory.asset.dispose') && <ActionButton label="Dispose" path={`/api/v1/inventory/assets/${r.id}:dispose`} invalidate={INV} reason="required" danger />}
        </div>} />
      {hist && <AssetHistoryDrawer asset={hist} onClose={() => setHist(null)} />}
    </>
  );
}

function AssetHistoryDrawer({ asset, onClose }: { asset: R; onClose: () => void }) {
  const h = useGet<R>(`/api/v1/inventory/assets/${asset.id}/history`);
  const d = h.data;
  return (
    <Drawer open onClose={onClose} title={`${String(asset.code)} · ${String(asset.name)}`}>
      {!d ? <Skeleton /> : <div className="oc-stack">
        <KV items={[['Category', String((d.asset as R).category)], ['Usage hours', String((d.asset as R).usageHours)], ['Golf cart hours (P2 fleet)', String(d.golfCartHours)],
          ['Book value', money((d.asset as R).bookValue)]]} />
        <Card title="Maintenance" icon="build"><DataTable rows={d.maintenance as R[]} columns={[{ key: 'number', header: 'Work order' }, { key: 'description', header: 'Description' },
          { key: 'totalCost', header: 'Cost', render: (r) => money(r.totalCost) }, { key: 'status', header: 'Status', render: pill('status') }]} /></Card>
        <Card title="Spare parts" icon="settings"><DataTable rows={d.spareParts as R[]} columns={[{ key: 'itemName', header: 'Part' }, { key: 'quantity', header: 'Qty' },
          { key: 'totalCost', header: 'Cost', render: (r) => money(r.totalCost) }]} /></Card>
        <Card title="Usage" icon="schedule"><DataTable rows={d.usages as R[]} columns={[{ key: 'usageType', header: 'Type' },
          { key: 'startedAt', header: 'From', render: (r) => formatDateTime(String(r.startedAt)) }, { key: 'hours', header: 'Hours', render: (r) => qty(r.hours) },
          { key: 'customerRef', header: 'Customer', render: (r) => qty(r.customerRef) }]} /></Card>
        <Card title="Depreciation" icon="trending_down"><DataTable rows={(d.depreciation as R[]).map((x) => ({ ...x, id: String(x.runId) } as R))} columns={[{ key: 'period', header: 'Period' },
          { key: 'amount', header: 'Amount', render: (r) => money(r.amount) }, { key: 'bookValueAfter', header: 'Book value', render: (r) => money(r.bookValueAfter) }]} /></Card>
      </div>}
    </Drawer>
  );
}

function MaintenanceDue() {
  const list = useGet<Page<R>>('/api/v1/inventory/maintenance-due?dueOnly=true');
  return list.data?.items.length === 0 ? <Empty title="No maintenance due" icon="task_alt" /> : (
    <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error}
      columns={[{ key: 'assetCode', header: 'Asset' }, { key: 'assetName', header: 'Name' }, { key: 'name', header: 'Schedule' },
        { key: 'triggerType', header: 'Based on', render: (r) => label(r.triggerType) }, { key: 'nextDueOn', header: 'Due', render: (r) => (r.nextDueOn ? formatDate(String(r.nextDueOn)) : '—') },
        { key: 'hoursSince', header: 'Hours since service', align: 'right' }]} />
  );
}

function WorkOrders() {
  const { can } = useAuth();
  const [modal, setModal] = useState<R | null>(null);
  const [create, setCreate] = useState(false);
  return (
    <>
      <ListPage title="Work Orders" path="/api/v1/inventory/maintenance-records" search={false} statuses={opts(['open', 'in_progress', 'completed', 'cancelled'])}
        actions={can('inventory.maintenance.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setCreate(true)}>New Work Order</button> : undefined}
        columns={[{ key: 'number', header: 'Work order' }, { key: 'assetCode', header: 'Asset' }, { key: 'maintenanceType', header: 'Type', render: (r) => label(r.maintenanceType) },
          { key: 'description', header: 'Description' }, { key: 'totalCost', header: 'Cost', align: 'right', render: (r) => money(r.totalCost) },
          { key: 'status', header: 'Status', render: pill('status') }]}
        rowActions={(r) => <div className="oc-row-wrap">
          {r.status === 'open' && can('inventory.maintenance.update') && <ActionButton label="Start" path={`/api/v1/inventory/maintenance-records/${r.id}:start`} invalidate={INV} />}
          {['open', 'in_progress'].includes(String(r.status)) && can('inventory.maintenance.complete') && <button className="oc-btn oc-btn-sm oc-btn-primary" onClick={() => setModal(r)}>Complete</button>}
          {['open', 'in_progress'].includes(String(r.status)) && can('inventory.maintenance.update') && (
            <ActionButton label="Cancel" path={`/api/v1/inventory/maintenance-records/${r.id}:cancel`} invalidate={INV} reason="required" danger />
          )}
        </div>} />
      {create && <WorkOrderForm onClose={() => setCreate(false)} />}
      {modal && <CompleteWorkOrder wo={modal} onClose={() => setModal(null)} />}
    </>
  );
}

function WorkOrderForm({ onClose }: { onClose: () => void }) {
  const assets = useGet<Page<R>>('/api/v1/inventory/assets?limit=500&filter[status]=active');
  const [asset, setAsset] = useState('');
  const [type, setType] = useState('corrective');
  const [desc, setDesc] = useState('');
  const [date, setDate] = useState('');
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/inventory/maintenance-records', INV, () => ({ 'Idempotency-Key': uuidv7() }));
  return (
    <Modal open onClose={onClose} title="New Work Order" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!asset || !desc || send.isPending} onClick={() => send.mutate({ assetId: asset, maintenanceType: type, description: desc,
        ...(date ? { scheduledOn: date } : {}) }, { onSuccess: onClose })}>Create</button>
    </>}>
      <ErrorAlert error={send.error} />
      <SelectField label="Asset" value={asset} onChange={setAsset} options={(assets.data?.items ?? []).map((a) => ({ value: a.id, label: `${String(a.code)} ${String(a.name)}` }))} />
      <SelectField label="Type" value={type} onChange={setType} options={opts(['preventive', 'corrective', 'inspection'])} />
      <TextArea label="Description" value={desc} onChange={setDesc} />
      <TextField label="Scheduled on" type="date" value={date} onChange={setDate} />
    </Modal>
  );
}

function CompleteWorkOrder({ wo, onClose }: { wo: R; onClose: () => void }) {
  const [labor, setLabor] = useState('');
  const [vendor, setVendor] = useState('');
  const [wh, setWh] = useState('');
  const [lines, setLines] = useState<Line[]>([]);
  const send = useSend<Record<string, unknown>, R>('POST', `/api/v1/inventory/maintenance-records/${wo.id}:complete`, INV);
  return (
    <Modal open onClose={onClose} title={`Complete ${String(wo.number)}`} wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => send.mutate({ laborCost: labor, vendorCost: vendor, ...(wh ? { warehouseId: wh } : {}),
        spareParts: linesBody(lines) }, { onSuccess: onClose })}>Complete</button>
    </>}>
      <ErrorAlert error={send.error} />
      <div className="oc-form">
        <TextField label="Labor cost" type="number" value={labor} onChange={setLabor} />
        <TextField label="Vendor cost" type="number" value={vendor} onChange={setVendor} />
        <WarehouseSelect label="Spare parts from (default Engineering)" value={wh} onChange={setWh} all />
      </div>
      <h3>Spare parts used</h3>
      <LinesEditor lines={lines} onChange={setLines} />
    </Modal>
  );
}

function Depreciation() {
  const { can } = useAuth();
  const [period, setPeriod] = useState('');
  const runs = useGet<Page<R>>('/api/v1/inventory/depreciation-runs');
  const send = useSend<Record<string, string>, R>('POST', '/api/v1/inventory/depreciation-runs', INV, () => ({ 'Idempotency-Key': uuidv7() }));
  return (
    <div className="oc-stack">
      {can('inventory.depreciation.run') && <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <TextField label="Period" type="month" value={period} onChange={setPeriod} help="The previous month is posted automatically by the daily job" />
        <button className="oc-btn oc-btn-primary" disabled={!period || send.isPending} onClick={() => send.mutate({ period })}>Post depreciation</button>
      </div>}
      <ErrorAlert error={send.error} />
      <DataTable rows={runs.data?.items} loading={runs.isLoading} columns={[{ key: 'number', header: 'Run' }, { key: 'period', header: 'Period' },
        { key: 'assets', header: 'Assets', align: 'right' }, { key: 'total', header: 'Depreciation', align: 'right', render: (r) => money(r.total) },
        { key: 'postedAt', header: 'Posted', render: (r) => formatDateTime(String(r.postedAt)) }]} />
    </div>
  );
}

// ── Posting exceptions, reports ───────────────────────────────────────────

export function PostingExceptionsPage() {
  const { can } = useAuth();
  return (
    <ListPage title="Posting Exceptions" help="Automatic postings (sales, packages, banquets, receipts) that need attention: unknown item, warehouse or recipe data."
      path="/api/v1/inventory/posting-exceptions" search={false} statuses={opts(['open', 'resolved'])}
      columns={[{ key: 'createdAt', header: 'When', render: (r) => formatDateTime(String(r.createdAt)) }, { key: 'eventType', header: 'Event' },
        { key: 'sourceType', header: 'Source', render: (r) => label(r.sourceType) }, { key: 'itemCode', header: 'Item', render: (r) => qty(r.itemCode) },
        { key: 'quantity', header: 'Qty', render: (r) => qty(r.quantity) }, { key: 'reason', header: 'Problem' }, { key: 'status', header: 'Status', render: pill('status') }]}
      rowActions={(r) => (r.status === 'open' && can('inventory.posting_exception.resolve')
        ? <PromptButton label="Resolve" title="Resolution" field="resolution" path={`/api/v1/inventory/posting-exceptions/${r.id}:resolve`} /> : null)} />
  );
}

export function InventoryReportsPage() {
  const reports = useGet<Page<R & { code: string; module: string; name: string; description: string }>>('/api/v1/reporting/reports');
  return (
    <div className="oc-stack">
      <PageHeader title="Inventory Reports" help="Reports run on the read replica; filter by date, warehouse and category, export CSV / XLSX." />
      {reports.isLoading && <Skeleton />}
      <div className="oc-grid">
        {reports.data?.items.filter((r) => r.module === 'inventory').map((r) => (
          <Link key={r.code} to={`/reports/${r.code}`} className="oc-card" style={{ textDecoration: 'none' }}>
            <div className="oc-card-head"><span className="oc-icon-circle"><Icon name="table_chart" size={20} /></span><h3>{r.name}</h3></div>
            <div className="oc-small oc-muted">{r.description}</div>
          </Link>
        ))}
      </div>
      <Link to="/inventory/performance" className="oc-btn oc-btn-neutral">Inventory Performance dashboard</Link>
    </div>
  );
}

// ── ops workstations (EP-26) ──────────────────────────────────────────────

function OpsHead({ title, help }: { title: string; help?: string }) {
  return <div className="oc-page-head"><div><h1>{title}</h1>{help && <p>{help}</p>}</div></div>;
}

/** Warehouse: scan an item to see its stock everywhere, then the stock list. */
function WarehouseStockOps() {
  const toast = useToast();
  const [found, setFound] = useState<R | null>(null);
  return (
    <div className="oc-stack">
      <OpsHead title="Warehouse · Stock Balance" help="Scan an item or carton barcode (camera or scanner) to see its stock per location." />
      <ScanField onCode={(c) => void lookup(c).then(setFound).catch(() => toast(`Barcode ${c} is not known`, 'error'))} />
      {found && <Card title={`${String(found.itemName)} (${String(found.itemCode)})`} icon="qr_code_scanner">
        <KV items={[['Scanned unit', `${String(found.uom)} = ${String(found.baseFactor)} ${String(found.baseUom)}`], ['Tracking', (found.tracking as string[]).join(', ') || '—']]} />
        <DataTable rows={(found.balances as R[]).map((b, i) => ({ ...b, id: String(i) } as R))} columns={[{ key: 'warehouseName', header: 'Location' },
          { key: 'batchNo', header: 'Batch', render: (r) => qty(r.batchNo) }, { key: 'packQuantity', header: 'Stock' }]} />
      </Card>}
      <StockBalancePage ops />
    </div>
  );
}

function IssuingOps() {
  return (
    <div className="oc-stack">
      <OpsHead title="Issuing" help="Issue stock to a department or cost center with reason; approved requisitions are fulfilled under Store Requisition." />
      <IssueInline />
    </div>
  );
}

function IssueInline() {
  const toast = useToast();
  const [wh, setWh] = useState('');
  const [cc, setCc] = useState('');
  const [reason, setReason] = useState('');
  const [lines, setLines] = useState<Line[]>([emptyLine()]);
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/inventory/issues', INV, () => ({ 'Idempotency-Key': uuidv7() }));
  return (
    <Card title="Issue Stock" icon="outbox">
      <ErrorAlert error={send.error} />
      <div className="oc-form">
        <WarehouseSelect value={wh} onChange={setWh} required />
        <TextField label="Department / cost center" value={cc} onChange={setCc} />
        <TextField label="Reason" value={reason} onChange={setReason} required />
      </div>
      <LinesEditor lines={lines} onChange={setLines} />
      <button className="oc-btn oc-btn-primary" style={{ minHeight: 48 }} disabled={!wh || !reason || send.isPending} onClick={() => send.mutate({ warehouseId: wh, costCenter: cc,
        reason, origin: 'ops', lines: linesBody(lines) }, { onSuccess: (r) => { toast(`Issued ${String(r.number)}`); setLines([emptyLine()]); } })}>Issue</button>
    </Card>
  );
}

function OpnameOps() {
  const list = useGet<Page<R>>('/api/v1/inventory/stock-opnames?filter[status]=in_progress');
  const [id, setId] = useState('');
  const o = useGet<R>(id ? `/api/v1/inventory/stock-opnames/${id}` : null);
  return (
    <div className="oc-stack">
      <OpsHead title="Stock Opname" help="Count with the scanner or camera; counts made offline are queued on this device and synced once." />
      <SelectField label="Stock opname in progress" value={id} onChange={setId} placeholder="Select"
        options={(list.data?.items ?? []).map((x) => ({ value: x.id, label: `${String(x.number)} · ${String(x.warehouse)}` }))} />
      {list.data?.items.length === 0 && <Empty title="No stock opname in progress" icon="inventory" />}
      {o.data && <CountSheet o={o.data} onCounted={() => void o.refetch()} />}
    </div>
  );
}

function OutletRequisitionOps() {
  const [created, setCreated] = useState('');
  return (
    <div className="oc-stack">
      <OpsHead title="Store Requisition" help="Request stock from the store for the kitchen or outlet (scan to add items)." />
      <RequisitionInline onDone={setCreated} />
      {created && <RequisitionDrawer id={created} onClose={() => setCreated('')} />}
      <RequisitionsPage ops />
    </div>
  );
}

function RequisitionInline({ onDone }: { onDone: (id: string) => void }) {
  const [to, setTo] = useState('');
  const [needed, setNeeded] = useState('');
  const [lines, setLines] = useState<Line[]>([emptyLine()]);
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/inventory/requisitions', INV, () => ({ 'Idempotency-Key': uuidv7() }));
  return (
    <Card title="New Store Requisition" icon="add_shopping_cart">
      <ErrorAlert error={send.error} />
      <div className="oc-form">
        <WarehouseSelect label="Kitchen / outlet" value={to} onChange={setTo} required />
        <TextField label="Needed by" type="date" value={needed} onChange={setNeeded} />
      </div>
      <LinesEditor lines={lines} onChange={setLines} />
      <button className="oc-btn oc-btn-primary" style={{ minHeight: 48 }} disabled={!to || send.isPending} onClick={() => send.mutate({ requestType: 'store', requestingWarehouseId: to,
        ...(needed ? { neededBy: needed } : {}), submit: true, origin: 'ops', lines: linesBody(lines) }, { onSuccess: (r) => { setLines([emptyLine()]); onDone(r.id); } })}>Send request</button>
    </Card>
  );
}

function SparePartOps() {
  const toast = useToast();
  const assets = useGet<Page<R>>('/api/v1/inventory/assets?limit=500');
  const [asset, setAsset] = useState('');
  const [notes, setNotes] = useState('');
  const [lines, setLines] = useState<Line[]>([emptyLine()]);
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/inventory/spare-part-requests', INV, () => ({ 'Idempotency-Key': uuidv7() }));
  return (
    <div className="oc-stack">
      <OpsHead title="Spare Part Request" help="Spare parts for golf cart (buggy) maintenance, issued by the Engineering store." />
      <Card title="Request" icon="build">
        <ErrorAlert error={send.error} />
        <SelectField label="Golf cart / asset" value={asset} onChange={setAsset} options={(assets.data?.items ?? []).filter((a) => a.status !== 'disposed')
          .map((a) => ({ value: a.id, label: `${String(a.code)} ${String(a.name)}` }))} />
        <TextField label="Notes" value={notes} onChange={setNotes} />
        <LinesEditor lines={lines} onChange={setLines} />
        <button className="oc-btn oc-btn-primary" style={{ minHeight: 48 }} disabled={!asset || send.isPending} onClick={() => send.mutate({ assetId: asset, notes,
          lines: linesBody(lines) }, { onSuccess: (r) => { toast(`Requested ${String(r.number)}`); setLines([emptyLine()]); } })}>Send request</button>
      </Card>
      <ListPage title="My requests" path="/api/v1/inventory/requisitions" search={false} extraQuery={{ 'filter[requestType]': 'spare_part' }}
        columns={[{ key: 'number', header: 'Request' }, { key: 'assetCode', header: 'Asset', render: (r) => qty(r.assetCode) },
          { key: 'createdAt', header: 'Requested', render: (r) => formatDateTime(String(r.createdAt)) }, { key: 'status', header: 'Status', render: pill('status') }]} />
    </div>
  );
}

// ── routes ────────────────────────────────────────────────────────────────

/** Back Office routes of the area. */
export const INVENTORY_ROUTES: AreaRoute[] = [
  { path: 'inventory/stock-balance', perm: 'inventory.stock_balance.view', element: <StockBalancePage /> },
  { path: 'inventory/stock-movement', perm: 'inventory.stock_movement.view', element: <StockMovementPage /> },
  { path: 'inventory/requisitions', perm: 'inventory.requisition.view', element: <RequisitionsPage /> },
  { path: 'inventory/transfers', perm: 'inventory.transfer.view', element: <TransfersPage /> },
  { path: 'inventory/adjustments', perm: 'inventory.adjustment.view', element: <AdjustmentsPage /> },
  { path: 'inventory/stock-opname', perm: 'inventory.stock_opname.view', element: <OpnamesPage /> },
  { path: 'inventory/valuation', perm: 'inventory.valuation.view', element: <ValuationPage /> },
  { path: 'inventory/replenishment', perm: 'inventory.reorder_point.view', element: <ReplenishmentPage /> },
  { path: 'inventory/production', perm: 'inventory.production.view', element: <ProductionPage /> },
  { path: 'inventory/waste', perm: 'inventory.waste.view', element: <WastePage /> },
  { path: 'inventory/consignment', perm: 'inventory.consignment.view', element: <ConsignmentPage /> },
  { path: 'inventory/assets', perm: 'inventory.asset.view', element: <AssetsPage /> },
  { path: 'inventory/posting-exceptions', perm: 'inventory.posting_exception.view', element: <PostingExceptionsPage /> },
  { path: 'inventory/reports', perm: 'reporting.report.view', element: <InventoryReportsPage /> },
  { path: 'inventory/performance', perm: 'reporting.dashboard.view', element: <KPIDashboardPage code="inventory-performance" /> },
];

/** Ops workstation tiles and routes of the area. */
export const INVENTORY_OPS_TILES: OpsTile[] = [
  ['warehouse', 'Warehouse', '/ops/warehouse', 'inventory.stock_balance.view'],
  ['kitchen', 'Kitchen & Outlet Store', '/ops/store', 'inventory.requisition.create'],
  ['build', 'Spare Part Request', '/ops/spare-parts', 'inventory.spare_part_request.create'],
];
export const INVENTORY_OPS_ROUTES: OpsRoute[] = [
  { path: 'warehouse', element: <WarehouseStockOps /> },
  { path: 'warehouse/issuing', element: <IssuingOps /> },
  { path: 'warehouse/transfer', element: <TransfersPage /> },
  { path: 'warehouse/opname', element: <OpnameOps /> },
  { path: 'warehouse/requisitions', element: <RequisitionsPage ops /> },
  { path: 'store', element: <OutletRequisitionOps /> },
  { path: 'store/production', element: <ProductionPage /> },
  { path: 'store/waste', element: <WastePage /> },
  { path: 'store/stock', element: <StockBalancePage ops /> },
  { path: 'spare-parts', element: <SparePartOps /> },
];
