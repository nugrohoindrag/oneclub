import React, { useState } from 'react';
import { qs, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDateTime } from '@oneclub/i18n';
import {
  Checkbox, DataTable, Empty, ErrorAlert, Icon, Modal, PageHeader, SelectField, StatTile, StatusPill, TextArea, TextField, useAuth, useToast,
} from '@oneclub/shell';
import { Tabs } from '../p1/common';
import { HK_TYPES, INV, PRIORITIES, ROOM, RoomStatus, WO_CATEGORIES, label, todayISO, type ChecklistItem, type HKTask, type Row, type RoomState } from './shared';
import { RoomStatusBoard } from './rooms';
import './accommodation.css';

// Housekeeping (requirements §20, §21): the tasks of the day — check-out
// cleaning created by the check-out, stayover cleaning of the occupied
// bungalows, deep cleaning, turndown and inspections — worked Dirty →
// Cleaning → Cleaned → Inspected → Ready with the checklist; a failed
// inspection goes back to Cleaning (and can open a work order).

interface Board { date: string; tasks: HKTask[]; counts: Record<string, number>; rooms: RoomState[]; roomCounts: Record<string, number> }

export function HousekeepingPage({ ops }: { ops?: boolean }) {
  const { can, me } = useAuth();
  const toast = useToast();
  const [date, setDate] = useState(todayISO());
  const [tab, setTab] = useState('tasks');
  const [mine, setMine] = useState(false);
  const [creating, setCreating] = useState(false);
  const [working, setWorking] = useState<{ task: HKTask; mode: 'complete' | 'inspect' | 'assign' } | null>(null);
  const b = useGet<Board>(`/api/v1/stay/housekeeping${qs({ date, assignee: mine ? me?.fullName : undefined })}`, { refetchInterval: 30_000 });
  const start = useSend<{ id: string }>('POST', (v) => `/api/v1/stay/housekeeping-tasks/${v.id}:start`, INV);
  const cancel = useSend<{ id: string; reason: string }>('POST', (v) => `/api/v1/stay/housekeeping-tasks/${v.id}:cancel`, INV);
  const plan = useSend<Row, Page<HKTask>>('POST', '/api/v1/stay/housekeeping:plan-day', INV);
  const x = b.data;
  return (
    <div className="oc-stack">
      <PageHeader title="Housekeeping" help="Dirty → Cleaning → Cleaned → Inspected → Ready. Check-out cleaning is created at check-out; plan the stayover cleaning of the occupied bungalows."
        actions={<>
          {can('stay.housekeeping.manage') && <button className="oc-btn oc-btn-neutral" disabled={plan.isPending}
            onClick={() => plan.mutate({ date }, { onSuccess: (r) => toast(`${r.items.length} stayover cleaning task(s) planned`) })}><Icon name="event_note" size={18} /> Plan stayover cleaning</button>}
          {can('stay.housekeeping.manage') && <button className="oc-btn oc-btn-primary" onClick={() => setCreating(true)}><Icon name="add" size={18} /> New task</button>}
        </>} />
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <TextField label="Date" type="date" value={date} onChange={setDate} />
        {x && ['dirty', 'cleaning', 'cleaned', 'inspected', 'ready'].map((k) => <StatTile key={k} label={ROOM[k][0]} value={x.roomCounts[k] ?? 0} icon="bed" />)}
        {ops && <Checkbox label="My tasks" checked={mine} onChange={setMine} />}
      </div>
      <Tabs value={tab} onChange={setTab} tabs={[{ value: 'tasks', label: `Tasks (${x?.tasks.length ?? 0})` }, { value: 'rooms', label: 'Rooms' }, { value: 'inspections', label: 'Inspections' }]} />
      <ErrorAlert error={b.error ?? start.error ?? cancel.error ?? plan.error} />
      {tab === 'tasks' && (
        <div className="oc-card">
          <DataTable rows={x?.tasks} loading={b.isLoading} rowKey={(t) => t.id} inlineActions empty={<Empty title="No housekeeping tasks" icon="mop" />}
            columns={[
              { key: 'bungalow', header: 'Bungalow', render: (t) => <><strong>{t.bungalowCode}</strong><div className="oc-small oc-muted">{t.taskNo}{t.stayNo ? ` · ${t.stayNo}` : ''}</div></> },
              { key: 'type', header: 'Task', render: (t) => <>{label(t.taskType)}{t.notes ? <div className="oc-small oc-muted">{t.notes}</div> : null}{t.reworks ? <div className="oc-small acc-full">re-clean × {t.reworks}</div> : null}</> },
              { key: 'priority', header: 'Priority', render: (t) => <StatusPill status={t.priority} label={label(t.priority)} tone={t.priority === 'urgent' ? 'error' : t.priority === 'high' ? 'warning' : 'neutral'} /> },
              { key: 'assigned', header: 'Assigned', render: (t) => t.assignedTo ?? <span className="oc-muted">—</span> },
              { key: 'room', header: 'Room', render: (t) => <RoomStatus status={t.hkStatus} /> },
              { key: 'status', header: 'Task status', render: (t) => <>{<StatusPill status={t.status.replace(/_/g, '-')} label={label(t.status)} />}{t.minutesTaken != null ? <div className="oc-small oc-muted">{t.minutesTaken} min</div> : null}</> }]}
            actions={(t) => (
              <div className="oc-row">
                {t.status === 'open' && can('stay.housekeeping.manage') && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setWorking({ task: t, mode: 'assign' })}>Assign</button>}
                {t.status === 'open' && can('stay.housekeeping.work') && <button className="oc-btn oc-btn-neutral oc-btn-sm" disabled={start.isPending}
                  onClick={() => start.mutate({ id: t.id }, { onSuccess: () => toast(`${t.bungalowCode}: Cleaning`) })}>Start</button>}
                {(t.status === 'open' || t.status === 'in_progress') && can('stay.housekeeping.work') && <button className="oc-btn oc-btn-primary oc-btn-sm" onClick={() => setWorking({ task: t, mode: 'complete' })}>Complete</button>}
                {t.status === 'completed' && can('stay.housekeeping.inspect') && <button className="oc-btn oc-btn-ink oc-btn-sm" onClick={() => setWorking({ task: t, mode: 'inspect' })}>Inspect</button>}
                {['open', 'in_progress'].includes(t.status) && can('stay.housekeeping.manage') && <button className="oc-btn oc-btn-text oc-btn-sm" disabled={cancel.isPending}
                  onClick={() => cancel.mutate({ id: t.id, reason: 'cancelled' })}>Cancel</button>}
              </div>
            )} />
        </div>
      )}
      {tab === 'rooms' && <RoomStatusBoard date={date} />}
      {tab === 'inspections' && <Inspections />}
      {creating && <NewTaskModal rooms={x?.rooms ?? []} date={date} onClose={() => setCreating(false)} />}
      {working?.mode === 'complete' && <CompleteModal task={working.task} onClose={() => setWorking(null)} />}
      {working?.mode === 'inspect' && <InspectModal task={working.task} onClose={() => setWorking(null)} />}
      {working?.mode === 'assign' && <AssignTaskModal task={working.task} onClose={() => setWorking(null)} />}
    </div>
  );
}

function ChecklistEditor({ items, onChange }: { items: ChecklistItem[]; onChange: (v: ChecklistItem[]) => void }) {
  const areas = [...new Set(items.map((i) => i.area))];
  return (
    <div className="acc-checklist">
      {areas.map((a) => (
        <fieldset key={a}>
          <legend>{label(a)}</legend>
          {items.map((it, i) => it.area === a && (
            <Checkbox key={i} label={it.item} checked={it.done} onChange={(v) => onChange(items.map((x, j) => (j === i ? { ...x, done: v } : x)))} />
          ))}
        </fieldset>
      ))}
      <button type="button" className="oc-btn oc-btn-text oc-btn-sm" onClick={() => onChange(items.map((x) => ({ ...x, done: true })))}>Tick all</button>
    </div>
  );
}

function CompleteModal({ task, onClose }: { task: HKTask; onClose: () => void }) {
  const toast = useToast();
  const [items, setItems] = useState(task.checklist);
  const [notes, setNotes] = useState('');
  const send = useSend<Row>('POST', `/api/v1/stay/housekeeping-tasks/${task.id}:complete`, INV);
  return (
    <Modal open wide onClose={onClose} title={`Complete · ${task.bungalowCode} · ${label(task.taskType)}`} actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button>
      <button className="oc-btn oc-btn-ink" disabled={send.isPending} onClick={() => send.mutate({ checklist: items, notes: notes || undefined },
        { onSuccess: () => { toast(`${task.bungalowCode}: Cleaned`); onClose(); } })}>Cleaning done</button></>}>
      <ChecklistEditor items={items} onChange={setItems} />
      <TextArea label="Notes (missing amenities, damage …)" rows={2} value={notes} onChange={setNotes} />
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function InspectModal({ task, onClose }: { task: HKTask; onClose: () => void }) {
  const toast = useToast();
  const [items, setItems] = useState(task.checklist);
  const [notes, setNotes] = useState('');
  const [wo, setWo] = useState({ title: '', category: 'other' });
  const send = useSend<Row>('POST', `/api/v1/stay/housekeeping-tasks/${task.id}:inspect`, INV);
  const run = (result: string) => send.mutate({ result, checklist: items, notes: notes || undefined, workOrderTitle: result === 'failed' && wo.title ? wo.title : undefined,
    workOrderCategory: result === 'failed' && wo.title ? wo.category : undefined }, { onSuccess: () => { toast(result === 'passed' ? `${task.bungalowCode} passed` : `${task.bungalowCode} back to cleaning`); onClose(); } });
  return (
    <Modal open wide onClose={onClose} title={`Inspection · ${task.bungalowCode}`} actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button>
      <button className="oc-btn oc-btn-danger" disabled={send.isPending} onClick={() => run('failed')}>Failed → re-clean</button>
      <button className="oc-btn oc-btn-ink" disabled={send.isPending} onClick={() => run('passed')}>Passed</button></>}>
      <ChecklistEditor items={items} onChange={setItems} />
      <TextArea label="Notes" rows={2} value={notes} onChange={setNotes} />
      <div className="oc-form">
        <TextField label="Technical issue → work order (optional)" value={wo.title} onChange={(x) => setWo({ ...wo, title: x })} placeholder="e.g. AC not cooling" />
        <SelectField label="Category" value={wo.category} onChange={(x) => setWo({ ...wo, category: x })} options={WO_CATEGORIES.map(([value, l]) => ({ value, label: l }))} />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function AssignTaskModal({ task, onClose }: { task: HKTask; onClose: () => void }) {
  const toast = useToast();
  const [who, setWho] = useState(task.assignedTo ?? '');
  const [priority, setPriority] = useState(task.priority);
  const send = useSend<Row>('POST', `/api/v1/stay/housekeeping-tasks/${task.id}:assign`, INV);
  return (
    <Modal open onClose={onClose} title={`Assign ${task.taskNo} · ${task.bungalowCode}`} actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button>
      <button className="oc-btn oc-btn-ink" disabled={!who || send.isPending} onClick={() => send.mutate({ assignedTo: who, priority }, { onSuccess: () => { toast('Assigned'); onClose(); } })}>Assign</button></>}>
      <div className="oc-form">
        <TextField label="Attendant" value={who} onChange={setWho} />
        <SelectField label="Priority" value={priority} onChange={setPriority} options={PRIORITIES.map((p) => ({ value: p, label: label(p) }))} />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function NewTaskModal({ rooms, date, onClose }: { rooms: RoomState[]; date: string; onClose: () => void }) {
  const toast = useToast();
  const [v, setV] = useState({ bungalowId: rooms[0]?.id ?? '', taskType: 'deep_cleaning', priority: 'normal', assignedTo: '', notes: '', taskDate: date });
  const send = useSend<Row>('POST', '/api/v1/stay/housekeeping-tasks', INV);
  return (
    <Modal open onClose={onClose} title="New housekeeping task" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button>
      <button className="oc-btn oc-btn-ink" disabled={!v.bungalowId || send.isPending} onClick={() => send.mutate({ ...v, assignedTo: v.assignedTo || undefined, notes: v.notes || undefined },
        { onSuccess: () => { toast('Task created'); onClose(); } })}>Create</button></>}>
      <div className="oc-form">
        <SelectField label="Bungalow" value={v.bungalowId} onChange={(x) => setV({ ...v, bungalowId: x })} options={rooms.map((r) => ({ value: r.id, label: `${r.code} · ${ROOM[r.operationalStatus]?.[0] ?? r.operationalStatus}` }))} />
        <SelectField label="Task" value={v.taskType} onChange={(x) => setV({ ...v, taskType: x })} options={HK_TYPES.map(([value, l]) => ({ value, label: l }))} />
        <SelectField label="Priority" value={v.priority} onChange={(x) => setV({ ...v, priority: x })} options={PRIORITIES.map((p) => ({ value: p, label: label(p) }))} />
        <TextField label="Date" type="date" value={v.taskDate} onChange={(x) => setV({ ...v, taskDate: x })} />
        <TextField label="Attendant" value={v.assignedTo} onChange={(x) => setV({ ...v, assignedTo: x })} />
        <TextArea label="Notes" span rows={2} value={v.notes} onChange={(x) => setV({ ...v, notes: x })} />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

interface Inspection { id: string; bungalowCode: string; taskNo: string | null; inspectorName: string | null; result: string; notes: string | null; inspectedAt: string; checklist: ChecklistItem[] }

function Inspections() {
  const list = useGet<Page<Inspection>>('/api/v1/stay/inspections');
  return (
    <div className="oc-card">
      <DataTable rows={list.data?.items as unknown as Row[]} loading={list.isLoading} error={list.error} rowKey={(i) => String(i.id)} empty={<Empty title="No inspections" icon="fact_check" />}
        columns={[{ key: 'inspectedAt', header: 'When', render: (i) => formatDateTime(String(i.inspectedAt)) }, { key: 'bungalowCode', header: 'Bungalow' },
          { key: 'taskNo', header: 'Task', render: (i) => String(i.taskNo ?? '—') }, { key: 'inspectorName', header: 'Inspector', render: (i) => String(i.inspectorName ?? '—') },
          { key: 'result', header: 'Result', render: (i) => <StatusPill status={String(i.result)} label={label(String(i.result))} tone={i.result === 'passed' ? 'success' : 'error'} /> },
          { key: 'checklist', header: 'Checklist', render: (i) => { const c = i.checklist as ChecklistItem[]; return `${c.filter((x) => x.done).length}/${c.length}`; } },
          { key: 'notes', header: 'Notes', render: (i) => String(i.notes ?? '') }]} />
    </div>
  );
}
