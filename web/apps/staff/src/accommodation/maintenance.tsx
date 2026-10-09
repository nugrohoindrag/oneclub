import React, { useState } from 'react';
import { qs, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDateTime } from '@oneclub/i18n';
import {
  AutoResourcePage, Checkbox, DataTable, Empty, ErrorAlert, Icon, Modal, PageHeader, SearchBox, SelectField, StatusPill, TextArea, TextField, useAuth,
  useDebounced, useToast,
} from '@oneclub/shell';
import { Tabs } from '../p1/common';
import { INV, PRIORITIES, WO_CATEGORIES, label, money, type Row, type RoomState, type WorkOrder } from './shared';
import './accommodation.css';

// Maintenance (requirements §22): work orders Open → Assigned → In Progress
// → Resolved → Closed (a work order that closes the bungalow puts it Out of
// Order until resolved), preventive maintenance schedules (AC cleaning,
// water heater, electrical inspection …) and the maintenance history.

const WO_TONE: Record<string, 'info' | 'warning' | 'success' | 'neutral' | 'error'> = {
  open: 'warning', assigned: 'info', in_progress: 'info', resolved: 'success', closed: 'neutral', cancelled: 'neutral',
};

export function StayMaintenancePage({ ops }: { ops?: boolean }) {
  const { can } = useAuth();
  const [tab, setTab] = useState('open');
  return (
    <div className="oc-stack">
      <PageHeader title="Maintenance" help="Work orders of the bungalows. A work order that needs the bungalow closed puts it Out of Order: it cannot be booked until resolved."
        actions={can('stay.work_order.create') ? <NewWorkOrderButton /> : undefined} />
      <Tabs value={tab} onChange={setTab} tabs={[{ value: 'open', label: 'Work Orders' }, ...(ops ? [] : [{ value: 'preventive', label: 'Preventive Maintenance' }]),
        { value: 'history', label: 'Maintenance History' }]} />
      {tab === 'open' && <WorkOrders status="open" />}
      {tab === 'history' && <WorkOrders status="resolved,closed,cancelled" />}
      {tab === 'preventive' && <Preventive />}
    </div>
  );
}

function WorkOrders({ status }: { status: string }) {
  const { can } = useAuth();
  const toast = useToast();
  const [q, setQ] = useState('');
  const query = useDebounced(q);
  const [move, setMove] = useState<{ wo: WorkOrder; op: string } | null>(null);
  const list = useGet<Page<WorkOrder>>(`/api/v1/stay/work-orders${qs({ status, q: query, limit: 200 })}`, { refetchInterval: 30_000 });
  const start = useSend<{ id: string }>('POST', (b) => `/api/v1/stay/work-orders/${b.id}:start`, INV);
  const close = useSend<{ id: string }>('POST', (b) => `/api/v1/stay/work-orders/${b.id}:close`, INV);
  return (
    <div className="oc-stack">
      <SearchBox value={q} onChange={setQ} placeholder="Work order, bungalow, issue" />
      <ErrorAlert error={list.error ?? start.error ?? close.error} />
      <div className="oc-card">
        <DataTable rows={list.data?.items} loading={list.isLoading} rowKey={(w) => w.id} inlineActions empty={<Empty title="No work orders" icon="build" />}
          columns={[
            { key: 'woNo', header: 'Work order', render: (w) => <><strong>{w.woNo}</strong><div className="oc-small oc-muted">{formatDateTime(w.createdAt)} · {label(w.source)}</div></> },
            { key: 'bungalow', header: 'Bungalow', render: (w) => <>{w.bungalowCode ?? 'General'}{w.closesUnit ? <div className="oc-small acc-full">Out of Order</div> : null}</> },
            { key: 'title', header: 'Issue', render: (w) => <>{w.title}<div className="oc-small oc-muted">{WO_CATEGORIES.find((c) => c[0] === w.category)?.[1]}{w.scheduleName ? ' · preventive' : ''}{w.description ? ` · ${w.description}` : ''}</div></> },
            { key: 'priority', header: 'Priority', render: (w) => <StatusPill status={w.priority} label={label(w.priority)} tone={w.priority === 'urgent' ? 'error' : w.priority === 'high' ? 'warning' : 'neutral'} /> },
            { key: 'assignedTo', header: 'Technician', render: (w) => w.assignedTo ?? '—' },
            { key: 'status', header: 'Status', render: (w) => <>{<StatusPill status={w.status} label={label(w.status)} tone={WO_TONE[w.status]} />}<div className="oc-small oc-muted">{w.hoursOpen} h{w.cost ? ` · ${money(w.cost)}` : ''}</div></> },
            ...(status !== 'open' ? [{ key: 'resolution', header: 'Resolution', render: (w: WorkOrder) => w.resolution ?? '—' }] : [])]}
          actions={(w) => (
            <div className="oc-row">
              {['open', 'assigned', 'in_progress'].includes(w.status) && can('stay.work_order.manage') && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setMove({ wo: w, op: 'assign' })}>Assign</button>}
              {['open', 'assigned'].includes(w.status) && can('stay.work_order.work') && <button className="oc-btn oc-btn-neutral oc-btn-sm" disabled={start.isPending}
                onClick={() => start.mutate({ id: w.id }, { onSuccess: () => toast('Started') })}>Start</button>}
              {['open', 'assigned', 'in_progress'].includes(w.status) && can('stay.work_order.work') && <button className="oc-btn oc-btn-primary oc-btn-sm" onClick={() => setMove({ wo: w, op: 'resolve' })}>Resolve</button>}
              {w.status === 'resolved' && can('stay.work_order.manage') && <button className="oc-btn oc-btn-ink oc-btn-sm" disabled={close.isPending} onClick={() => close.mutate({ id: w.id }, { onSuccess: () => toast('Closed') })}>Close</button>}
              {['open', 'assigned', 'in_progress'].includes(w.status) && can('stay.work_order.manage') && <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => setMove({ wo: w, op: 'cancel' })}>Cancel</button>}
            </div>
          )} />
      </div>
      {move && <MoveModal wo={move.wo} op={move.op} onClose={() => setMove(null)} />}
    </div>
  );
}

function MoveModal({ wo, op, onClose }: { wo: WorkOrder; op: string; onClose: () => void }) {
  const toast = useToast();
  const [v, setV] = useState({ assignedTo: wo.assignedTo ?? '', priority: wo.priority, resolution: '', cost: '', reason: '' });
  const send = useSend<Row>('POST', `/api/v1/stay/work-orders/${wo.id}:${op}`, INV);
  const body = op === 'assign' ? { assignedTo: v.assignedTo, priority: v.priority } : op === 'resolve' ? { resolution: v.resolution, cost: v.cost || undefined } : { reason: v.reason };
  return (
    <Modal open onClose={onClose} title={`${label(op)} · ${wo.woNo}`} actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button>
      <button className={`oc-btn ${op === 'cancel' ? 'oc-btn-danger' : 'oc-btn-ink'}`} disabled={send.isPending || (op === 'assign' && !v.assignedTo) || (op === 'resolve' && !v.resolution)}
        onClick={() => send.mutate(body, { onSuccess: () => { toast(`${label(op)}: done`); onClose(); } })}>{label(op)}</button></>}>
      {op === 'assign' && <div className="oc-form">
        <TextField label="Technician / team" value={v.assignedTo} onChange={(x) => setV({ ...v, assignedTo: x })} />
        <SelectField label="Priority" value={v.priority} onChange={(x) => setV({ ...v, priority: x })} options={PRIORITIES.map((p) => ({ value: p, label: label(p) }))} />
      </div>}
      {op === 'resolve' && <>
        {wo.closesUnit && <p className="oc-muted">The bungalow is put back on sale and waits for a housekeeping inspection before it is Ready.</p>}
        <div className="oc-form">
          <TextArea label="Resolution" span rows={3} value={v.resolution} onChange={(x) => setV({ ...v, resolution: x })} required />
          <TextField label="Cost" value={v.cost} onChange={(x) => setV({ ...v, cost: x })} />
        </div>
      </>}
      {op === 'cancel' && <TextArea label="Reason" rows={2} value={v.reason} onChange={(x) => setV({ ...v, reason: x })} />}
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

export function NewWorkOrderButton({ bungalowId }: { bungalowId?: string }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button className="oc-btn oc-btn-primary" onClick={() => setOpen(true)}><Icon name="add" size={18} /> Report issue</button>
      {open && <NewWorkOrderModal bungalowId={bungalowId} onClose={() => setOpen(false)} />}
    </>
  );
}

function NewWorkOrderModal({ bungalowId, onClose }: { bungalowId?: string; onClose: () => void }) {
  const toast = useToast();
  const rooms = useGet<Page<RoomState>>('/api/v1/stay/room-status');
  const tomorrow = new Date(Date.now() + 86_400_000);
  const [v, setV] = useState({ bungalowId: bungalowId ?? '', category: 'ac', title: '', description: '', priority: 'normal', assignedTo: '', closesUnit: false,
    expectedEnd: `${tomorrow.toISOString().slice(0, 10)}T12:00` });
  const send = useSend<Row>('POST', '/api/v1/stay/work-orders', INV);
  return (
    <Modal open wide onClose={onClose} title="Report a maintenance issue" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button>
      <button className="oc-btn oc-btn-ink" disabled={!v.title || send.isPending}
        onClick={() => send.mutate({ bungalowId: v.bungalowId || undefined, category: v.category, title: v.title, description: v.description || undefined, priority: v.priority,
          assignedTo: v.assignedTo || undefined, closesUnit: v.closesUnit || undefined, expectedEndAt: v.closesUnit ? new Date(v.expectedEnd).toISOString() : undefined },
        { onSuccess: () => { toast('Work order created'); onClose(); } })}>Create work order</button></>}>
      <div className="oc-form">
        <SelectField label="Bungalow" value={v.bungalowId} onChange={(x) => setV({ ...v, bungalowId: x })} placeholder="General (no bungalow)"
          options={(rooms.data?.items ?? []).map((r) => ({ value: r.id, label: `${r.code} · ${r.typeName}` }))} />
        <SelectField label="Category" value={v.category} onChange={(x) => setV({ ...v, category: x })} options={WO_CATEGORIES.map(([value, l]) => ({ value, label: l }))} />
        <TextField label="Issue" value={v.title} onChange={(x) => setV({ ...v, title: x })} required />
        <SelectField label="Priority" value={v.priority} onChange={(x) => setV({ ...v, priority: x })} options={PRIORITIES.map((p) => ({ value: p, label: label(p) }))} />
        <TextField label="Technician" value={v.assignedTo} onChange={(x) => setV({ ...v, assignedTo: x })} />
        <TextArea label="Description" span rows={2} value={v.description} onChange={(x) => setV({ ...v, description: x })} />
      </div>
      {v.bungalowId && <Checkbox label="Close the bungalow: Out of Order (not bookable) until resolved" checked={v.closesUnit} onChange={(x) => setV({ ...v, closesUnit: x })} />}
      {v.closesUnit && <TextField label="Out of Order until (expected)" type="datetime-local" value={v.expectedEnd} onChange={(x) => setV({ ...v, expectedEnd: x })} />}
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function Preventive() {
  const { can } = useAuth();
  const toast = useToast();
  const gen = useSend<Row, Page<WorkOrder>>('POST', '/api/v1/stay/preventive-schedules:generate', INV);
  return (
    <div className="oc-stack">
      {can('stay.work_order.manage') && <div><button className="oc-btn oc-btn-neutral" disabled={gen.isPending}
        onClick={() => gen.mutate({}, { onSuccess: (r) => toast(`${r.items.length} work order(s) raised`) })}><Icon name="play_arrow" size={18} /> Raise the work orders due today</button>
        <span className="oc-small oc-muted" style={{ marginLeft: 8 }}>The daily accommodation job (07:00) raises them automatically.</span></div>}
      <ErrorAlert error={gen.error} />
      <AutoResourcePage resourceKey="stay.preventive_schedule" />
    </div>
  );
}
