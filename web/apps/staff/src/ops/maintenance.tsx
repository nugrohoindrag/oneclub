import React, { useMemo, useState } from 'react';
import { qs, request, useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDateTime } from '@oneclub/i18n';
import {
  Checkbox, DataTable, Empty, ErrorAlert, Icon, Modal, PageHeader, SelectField, StatTile, StatusPill, TextArea, TextField, useAuth, useToast,
} from '@oneclub/shell';
import { GOLF_STREAM, useLive } from '../live';

// Course maintenance (demo feedback 9 Oct 2026, replaces Smartscore Golf
// O&M): the day's plan per hole or area for the groundstaff, the work
// planned → in progress → done with a photo and notes, and the history of
// every hole. One page for the Back Office (Golf › Course Maintenance) and
// the groundstaff (Operational › Golf Staff › Course Maintenance).

type Task = Schemas['MaintenanceTask'];
type Course = Schemas['MaintenanceCourse'];
type Board = Schemas['MaintenanceBoard'];

export const AREAS = ['green', 'fairway', 'tee', 'bunker', 'rough', 'irrigation', 'driving_range', 'whole_course', 'other'];
export const WORK: [string, string, string][] = [
  ['mowing_green', 'Mowing greens', 'green'], ['mowing_fairway', 'Mowing fairways', 'fairway'], ['mowing_rough', 'Mowing rough', 'rough'],
  ['bunker', 'Bunker raking', 'bunker'], ['irrigation', 'Irrigation', 'irrigation'], ['fertilizer', 'Fertiliser', 'fairway'],
  ['pin_position', 'Pin position', 'green'], ['aeration', 'Aeration', 'green'], ['top_dressing', 'Top dressing', 'green'], ['repair', 'Repair', 'other'],
  ['other', 'Other', 'other'],
];
export const workLabel = (t: string) => WORK.find((w) => w[0] === t)?.[1] ?? t.replace(/_/g, ' ');
const label = (s: string) => s.replace(/_/g, ' ');

function todayISO(offset = 0) {
  const d = new Date();
  d.setDate(d.getDate() + offset);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}
const shift = (day: string, n: number) => {
  const d = new Date(`${day}T00:00:00`);
  d.setDate(d.getDate() + n);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
};

export function MaintenancePage() {
  const { can } = useAuth();
  const toast = useToast();
  const [date, setDate] = useState(todayISO());
  const [tab, setTab] = useState<'day' | 'history'>('day');
  const courses = useGet<Page<Course>>('/api/v1/golf/maintenance-courses');
  const [courseId, setCourse] = useState('');
  const course = (courses.data?.items ?? []).find((c) => c.courseId === courseId) ?? courses.data?.items[0];
  const path = `/api/v1/golf/maintenance-board${qs({ date, courseId: course?.courseId })}`;
  const board = useGet<Board>(course ? path : null, { refetchInterval: 60_000 });
  useLive(GOLF_STREAM, ['golf.pace'], useMemo(() => () => { void board.refetch(); void courses.refetch(); }, [board, courses]));
  const inv = ['/api/v1/golf/maintenance'];
  const move = useSend<{ id: string; op: string; reason?: string }, Task>('POST', (v) => `/api/v1/golf/maintenance-tasks/${v.id}:${v.op}`, inv);
  const copy = useSend<Record<string, unknown>, Page<Task>>('POST', '/api/v1/golf/maintenance-tasks:copy-day', inv);
  const [planning, setPlanning] = useState(false);
  const [finishing, setFinishing] = useState<Task | null>(null);
  const plan = can('golf.maintenance_task.create');
  const work = can('golf.maintenance_task.work');
  const b = board.data;
  return (
    <div className="oc-stack">
      <PageHeader title="Course Maintenance" help="The day's work per hole for the groundstaff: planned → in progress → done, with a photo. A task can close its hole while it is worked."
        actions={<>
          {plan && <button className="oc-btn oc-btn-primary" onClick={() => setPlanning(true)}><Icon name="add" size={18} /> Plan work</button>}
          {plan && <button className="oc-btn oc-btn-neutral" disabled={copy.isPending || !course}
            onClick={() => copy.mutate({ courseId: course!.courseId, from: shift(date, -1), to: date }, { onSuccess: (r) => toast(`${r.items.length} tasks copied from the day before`) })}>
            <Icon name="content_copy" size={18} /> Copy the day before</button>}
        </>} />
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <div className="oc-row-wrap" role="tablist">
          <button className={`oc-btn ${tab === 'day' ? 'oc-btn-ink' : 'oc-btn-neutral'}`} onClick={() => setTab('day')}>Day plan</button>
          <button className={`oc-btn ${tab === 'history' ? 'oc-btn-ink' : 'oc-btn-neutral'}`} onClick={() => setTab('history')}>History per hole</button>
        </div>
        {tab === 'day' && <TextField label="Date" type="date" value={date} onChange={setDate} />}
        {(courses.data?.items.length ?? 0) > 1 && <SelectField label="Course" value={course?.courseId ?? ''} onChange={setCourse}
          options={(courses.data?.items ?? []).map((c) => ({ value: c.courseId, label: c.name }))} />}
      </div>
      <ErrorAlert error={courses.error ?? board.error ?? move.error ?? copy.error} />
      {tab === 'day' && b && (
        <>
          <div className="oc-row-wrap">
            <StatTile label="Planned" value={b.planned} icon="event_note" />
            <StatTile label="In progress" value={b.inProgress} icon="construction" />
            <StatTile label="Done" value={b.done} icon="task_alt" />
          </div>
          {b.tasks.length === 0 ? <Empty title="Nothing planned" help={plan ? 'Plan the work, or copy the plan of the day before.' : 'The plan of the day is made in the Back Office.'} icon="grass" /> : (
            <DataTable rows={b.tasks} columns={[{ key: 'plannedStart', header: 'Start', render: (t) => t.plannedStart ?? '—' },
              { key: 'holeLabel', header: 'Hole / area', render: (t) => <>{t.holeLabel ? <strong>Hole {t.holeLabel}</strong> : <strong>{label(t.area)}</strong>}{t.holeLabel ? <span className="oc-small oc-muted"> · {label(t.area)}</span> : null}{t.closesHole ? <span className="oc-small oc-muted"> · closes the hole</span> : null}</> },
              { key: 'taskType', header: 'Work', render: (t) => <>{workLabel(t.taskType)}{t.notes ? <div className="oc-small oc-muted">{t.notes}</div> : null}</> },
              { key: 'assigneeName', header: 'Groundstaff', render: (t) => t.assigneeName ?? '—' },
              { key: 'status', header: 'Status', render: (t) => <><StatusPill status={t.status.replace(/_/g, '-')} />
                {t.status === 'done' && t.completedAt ? <div className="oc-small oc-muted">{formatDateTime(t.completedAt)}</div> : null}</> },
              { key: 'photoUrl', header: 'Photo', render: (t) => (t.photoUrl ? <a href={t.photoUrl} target="_blank" rel="noreferrer"><img src={t.photoUrl} alt="" className="mnt-thumb" /></a> : '—') }]}
              actions={(t) => (
                <div className="oc-row">
                  {work && t.status === 'planned' && <button className="oc-btn oc-btn-neutral oc-btn-sm" disabled={move.isPending}
                    onClick={() => move.mutate({ id: t.id, op: 'start' }, { onSuccess: () => toast(t.closesHole ? `Started — hole ${t.holeLabel} closed` : 'Started') })}>Start</button>}
                  {work && (t.status === 'planned' || t.status === 'in_progress') && <button className="oc-btn oc-btn-primary oc-btn-sm" onClick={() => setFinishing(t)}>Done</button>}
                  {can('golf.maintenance_task.update') && (t.status === 'planned' || t.status === 'in_progress') && <button className="oc-btn oc-btn-text oc-btn-sm"
                    disabled={move.isPending} onClick={() => move.mutate({ id: t.id, op: 'cancel', reason: 'Cancelled' })}>Cancel</button>}
                </div>
              )} />
          )}
        </>
      )}
      {tab === 'history' && course && <HoleHistory course={course} />}
      {planning && course && <PlanModal course={course} date={date} onClose={() => setPlanning(false)} />}
      {finishing && <DoneModal task={finishing} onClose={() => setFinishing(null)} />}
    </div>
  );
}

/** One kind of work on several holes at once. */
function PlanModal({ course, date, onClose }: { course: Course; date: string; onClose: () => void }) {
  const toast = useToast();
  const [v, setV] = useState({ taskType: 'mowing_green', area: 'green', plannedStart: '06:00', assigneeName: '', closesHole: false, notes: '' });
  const [holes, setHoles] = useState<string[]>(course.holes.map((h) => h.holeId));
  const send = useSend<Record<string, unknown>, Page<Task>>('POST', '/api/v1/golf/maintenance-tasks:plan', ['/api/v1/golf/maintenance']);
  const whole = ['irrigation', 'driving_range', 'whole_course'].includes(v.area);
  return (
    <Modal open wide onClose={onClose} title={`Plan work · ${course.name} · ${date}`}
      actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
        <button className="oc-btn oc-btn-ink" disabled={send.isPending || (!whole && holes.length === 0)}
          onClick={() => send.mutate({ courseId: course.courseId, workDate: date, holeIds: whole ? [] : holes, ...v, plannedStart: v.plannedStart || undefined,
            assigneeName: v.assigneeName || undefined, notes: v.notes || undefined }, { onSuccess: (r) => { toast(`${r.items.length} tasks planned`); onClose(); } })}>
          Plan {whole ? 'the work' : `${holes.length} holes`}</button></>}>
      <div className="oc-stack">
        <div className="oc-form">
          <SelectField label="Work" value={v.taskType} onChange={(x) => setV({ ...v, taskType: x, area: WORK.find((w) => w[0] === x)?.[2] ?? v.area })}
            options={WORK.map(([value, l]) => ({ value, label: l }))} />
          <SelectField label="Area" value={v.area} onChange={(x) => setV({ ...v, area: x })} options={AREAS.map((a) => ({ value: a, label: label(a) }))} />
          <TextField label="Start" type="time" value={v.plannedStart} onChange={(x) => setV({ ...v, plannedStart: x })} />
          <TextField label="Groundstaff" value={v.assigneeName} onChange={(x) => setV({ ...v, assigneeName: x })} placeholder="Name or team" />
        </div>
        {!whole && (
          <>
            <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
              <strong>Holes</strong>
              <button type="button" className="oc-btn oc-btn-text oc-btn-sm" onClick={() => setHoles(course.holes.map((h) => h.holeId))}>All</button>
              <button type="button" className="oc-btn oc-btn-text oc-btn-sm" onClick={() => setHoles([])}>None</button>
            </div>
            <div className="oc-row-wrap">
              {course.holes.map((h) => (
                <button key={h.holeId} type="button" className="oc-chip" aria-pressed={holes.includes(h.holeId)}
                  title={h.lastDone[v.taskType] ? `Last ${workLabel(v.taskType).toLowerCase()}: ${h.lastDone[v.taskType]}` : undefined}
                  onClick={() => setHoles(holes.includes(h.holeId) ? holes.filter((x) => x !== h.holeId) : [...holes, h.holeId])}>{h.label}</button>
              ))}
            </div>
            <Checkbox label="Close the hole while it is worked (Course Status, Course Monitor, tee sheet)" checked={v.closesHole} onChange={(x) => setV({ ...v, closesHole: x })} />
          </>
        )}
        <TextArea label="Notes" rows={2} value={v.notes} onChange={(x) => setV({ ...v, notes: x })} />
        <ErrorAlert error={send.error} />
      </div>
    </Modal>
  );
}

/** Work done: notes and a photo of the result. */
function DoneModal({ task, onClose }: { task: Task; onClose: () => void }) {
  const toast = useToast();
  const [notes, setNotes] = useState('');
  const [photo, setPhoto] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const done = useSend<Record<string, unknown>, Task>('POST', (v) => `/api/v1/golf/maintenance-tasks/${String(v.id)}:complete`, ['/api/v1/golf/maintenance']);
  const upload = async (file: File) => {
    setBusy(true);
    setErr(null);
    const fd = new FormData();
    fd.append('file', file);
    try {
      setPhoto((await request<{ url: string }>('POST', '/api/v1/platform/images?resource=golf.maintenance_task', fd)).url);
    } catch (x) {
      setErr(x);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal open onClose={onClose} title={`Done · ${workLabel(task.taskType)}${task.holeLabel ? ` · hole ${task.holeLabel}` : ''}`}
      actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
        <button className="oc-btn oc-btn-ink" disabled={busy || done.isPending}
          onClick={() => done.mutate({ id: task.id, doneNotes: notes || undefined, photoUrl: photo || undefined },
            { onSuccess: () => { toast(task.closesHole && task.status === 'in_progress' ? `Done — hole ${task.holeLabel} open again` : 'Work done'); onClose(); } })}>Mark done</button></>}>
      <div className="oc-stack">
        <TextArea label="Work notes (e.g. greens cut at 3.2 mm, sprinkler 7 fixed)" rows={3} value={notes} onChange={setNotes} />
        <div className="oc-row" style={{ alignItems: 'center' }}>
          <div className="oc-image-preview">{photo ? <img src={photo} alt="" /> : <Icon name="photo_camera" size={28} />}</div>
          <label className="oc-btn oc-btn-outline oc-btn-sm" style={{ cursor: busy ? 'wait' : 'pointer' }}>
            <Icon name="upload" size={18} /> {busy ? 'Uploading…' : photo ? 'Change photo' : 'Photo of the work'}
            <input type="file" accept="image/png,image/jpeg,image/webp" capture="environment" className="oc-sr" disabled={busy}
              onChange={(e) => { const f = e.target.files?.[0]; e.target.value = ''; if (f) void upload(f); }} />
          </label>
        </div>
        <ErrorAlert error={err ?? done.error} />
      </div>
    </Modal>
  );
}

/** Per hole the last day of each kind of work, and the work done. */
function HoleHistory({ course }: { course: Course }) {
  const [hole, setHole] = useState('');
  const [from, setFrom] = useState(todayISO(-30));
  const list = useGet<Page<Task>>(`/api/v1/golf/maintenance-history${qs({ courseId: course.courseId, holeId: hole || undefined, from })}`);
  const kinds = WORK.filter(([k]) => course.holes.some((h) => h.lastDone[k]));
  return (
    <div className="oc-stack">
      <div className="oc-table-wrap">
        <table className="oc-table">
          <thead><tr><th>Hole</th>{kinds.map(([k, l]) => <th key={k}>{l}</th>)}<th>Now</th></tr></thead>
          <tbody>
            {course.holes.map((h) => (
              <tr key={h.holeId} onClick={() => setHole(h.holeId)} style={{ cursor: 'pointer' }} aria-selected={hole === h.holeId}>
                <td><strong>{h.label}</strong> <span className="oc-small oc-muted">par {h.par}</span></td>
                {kinds.map(([k]) => <td key={k}>{h.lastDone[k] ?? '—'}</td>)}
                <td>{h.working.length ? <StatusPill status="in-progress" label={h.working.map(workLabel).join(', ')} /> : ''}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {kinds.length === 0 && <p className="oc-small oc-muted" style={{ margin: 0 }}>No work done yet.</p>}
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <SelectField label="Hole" value={hole} onChange={setHole} placeholder="All holes" options={course.holes.map((h) => ({ value: h.holeId, label: `Hole ${h.label}` }))} />
        <TextField label="From" type="date" value={from} onChange={setFrom} />
      </div>
      <ErrorAlert error={list.error} />
      <DataTable rows={list.data?.items} loading={list.isLoading} columns={[{ key: 'workDate', header: 'Date' },
        { key: 'holeLabel', header: 'Hole / area', render: (t) => (t.holeLabel ? `Hole ${t.holeLabel}` : label(t.area)) },
        { key: 'taskType', header: 'Work', render: (t) => workLabel(t.taskType) }, { key: 'assigneeName', header: 'Groundstaff', render: (t) => t.assigneeName ?? '—' },
        { key: 'doneNotes', header: 'Notes', render: (t) => t.doneNotes ?? '' },
        { key: 'photoUrl', header: 'Photo', render: (t) => (t.photoUrl ? <a href={t.photoUrl} target="_blank" rel="noreferrer"><img src={t.photoUrl} alt="" className="mnt-thumb" /></a> : '') }]} />
    </div>
  );
}

/** The holes worked today, for the tee sheet. */
export function MaintenanceBanner({ courseId, date }: { courseId?: string; date: string }) {
  const { can } = useAuth();
  const b = useGet<Board>(can('golf.maintenance_task.view') ? `/api/v1/golf/maintenance-board${qs({ date, courseId })}` : null, { refetchInterval: 60_000 });
  const open = (b.data?.tasks ?? []).filter((t) => t.status === 'in_progress' || (t.status === 'planned' && t.closesHole));
  if (!open.length) return null;
  return (
    <div className="oc-alert oc-alert-warning" role="status">
      <Icon name="construction" size={18} /> Course maintenance: {open.map((t) => `${t.holeLabel ? `hole ${t.holeLabel}` : label(t.area)} ${workLabel(t.taskType).toLowerCase()}`
        + `${t.status === 'in_progress' ? ' (now' : ` (${t.plannedStart ?? 'planned'}`}${t.closesHole ? ', closed)' : ')'}`).join(' · ')}
    </div>
  );
}
