import React, { useMemo, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router';
import { getActiveProperty, request, uuidv7, useGet, useSend, type Page } from '@oneclub/api-client';
import { currentLocale, formatDate, formatDateTime } from '@oneclub/i18n';
import { cacheGet, cachePut, enqueue, useOnline } from '@oneclub/offline';
import {
  AutoResourcePage, Card, Checkbox, DataTable, DateRange, Empty, ErrorAlert, FilterPills, Icon, Modal, PageHeader, QRCode, SelectField, SelectFilter,
  Skeleton, StatusPill, TextArea, TextField, useAuth, useToast, type Option,
} from '@oneclub/shell';
import { KV, Tabs, today, type R } from '../p1/common';
import { ScanField } from '../p4/inventory';
import type { AreaRoute, OpsRoute, OpsTile } from '../p3/types';
import { registerEssSection, useUrlTab } from './hr';
import { WORKFORCE_ROUTES } from './hr-workforce';
import { OpenShiftsPanel } from './hr-openshifts';

// PRD P5 — schedules, attendance, leave & overtime (EP-06–08). Back Office routes (HRIS → Schedules, Attendance, Leave &
// Permission, Overtime), the Attendance Kiosk of the ops shell (QR / PIN on a registered device, offline queue) and the
// Employee Self Service sections My Schedule, Clock In / Out, Attendance History, Leave & Permission, Overtime and the manager's
// Approvals, Team Schedule and Team Attendance (registered with registerEssSection). Registered in p3/index.tsx and ops/p3.tsx.

const HR = '/api/v1/hris';
const ESS = '/api/v1/ess';
const INV = [HR, ESS];
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const val = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : String(v));
const date = (v: unknown) => (v ? formatDate(String(v).slice(0, 10)) : '—');
const time = (v: unknown) => (v ? new Date(String(v)).toLocaleTimeString(currentLocale() === 'id' ? 'id-ID' : 'en-GB', { hour: '2-digit', minute: '2-digit' }) : '—');
const opts = (vals: string[]): Option[] => vals.map((v) => ({ value: v, label: label(v) }));
const pill = (k: string) => (r: R) => <StatusPill status={String(r[k] ?? '')} label={label(r[k])} />;
const idem = () => ({ 'Idempotency-Key': uuidv7() });
const clean = (o: Record<string, unknown>) => Object.fromEntries(Object.entries(o).filter(([, v]) => v !== '' && v !== undefined && v !== null));
const withIds = (rows: unknown, key: (r: R, i: number) => string) => ((rows as R[] | undefined) ?? []).map((r, i) => ({ ...r, id: key(r, i) } as R));
const hours = (min: unknown) => (Number(min) ? `${(Number(min) / 60).toFixed(1)} h` : '—');
const ymd = (d: Date) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
const addDays = (day: string, n: number) => {
  const d = new Date(`${day}T00:00:00`);
  d.setDate(d.getDate() + n);
  return ymd(d);
};
const monday = (day: string) => {
  const d = new Date(`${day}T00:00:00`);
  d.setDate(d.getDate() - ((d.getDay() + 6) % 7));
  return ymd(d);
};
const DAY_STATUSES = ['scheduled', 'present', 'late', 'early_leave', 'absent', 'on_leave', 'off', 'holiday'];
const REQ_STATUSES = opts(['submitted', 'approved', 'rejected', 'cancelled']);

function useOptions(path: string | null, text: (r: R) => string): Option[] {
  const l = useGet<Page<R>>(path);
  return (l.data?.items ?? []).map((x) => ({ value: x.id, label: text(x) }));
}
const useEmployees = () => useOptions(`${HR}/employees?limit=500&filter[status]=active`, (x) => `${String(x.fullName)} (${String(x.employeeNo)})`);
const useUnits = () => useOptions(`${HR}/org-units?limit=500&filter[status]=active`, (x) => `${String(x.name)} (${String(x.code)})`);
const useTemplates = () => useOptions(`${HR}/shift-templates?limit=200&filter[status]=active`, (x) => `${String(x.code)} · ${String(x.startTime)}–${String(x.endTime)}`);

/** A form in a modal that POSTs / PATCHes a body. */
function FormModal({ open, onClose, title, method = 'POST', path, body, invalidate = INV, children, submit = 'Save', onDone }: {
  open: boolean; onClose: () => void; title: string; method?: 'POST' | 'PATCH'; path: string; body: () => Record<string, unknown>; invalidate?: string[];
  children: React.ReactNode; submit?: string; onDone?: (r: R) => void;
}) {
  const toast = useToast();
  const send = useSend<Record<string, unknown>, R>(method, path, invalidate, method === 'POST' ? idem : undefined);
  return (
    <Modal open={open} onClose={onClose} title={title} actions={
      <>
        <button className="oc-btn oc-btn-text" onClick={onClose}>Cancel</button>
        <button className="oc-btn oc-btn-ink" disabled={send.isPending} onClick={() => send.mutate(body(), {
          onSuccess: (r) => { toast(`${title}: done`); onClose(); onDone?.(r); },
        })}>{submit}</button>
      </>
    }>
      <div className="oc-stack">
        <ErrorAlert error={send.error} />
        <div className="oc-form">{children}</div>
      </div>
    </Modal>
  );
}

/** Posts an action without body, or with a note when asked (required note → modal). */
function Act({ label: text, path, note, kind = 'neutral', body }: {
  label: string; path: string; note?: 'required' | 'optional'; kind?: 'neutral' | 'ink' | 'text'; body?: Record<string, unknown>;
}) {
  const toast = useToast();
  const [open, setOpen] = useState(false);
  const [why, setWhy] = useState('');
  const send = useSend<Record<string, unknown>, R>('POST', path, INV);
  const run = (b: Record<string, unknown>) => send.mutate(b, { onSuccess: () => { setOpen(false); setWhy(''); toast(`${text}: done`); }, onError: (e) => toast(e.message, 'error') });
  return (
    <>
      <button className={`oc-btn oc-btn-${kind}`} style={{ minHeight: 40 }} disabled={send.isPending} onClick={() => (note ? setOpen(true) : run(body ?? {}))}>{text}</button>
      {open && (
        <Modal open onClose={() => setOpen(false)} title={text} actions={
          <>
            <button className="oc-btn oc-btn-text" onClick={() => setOpen(false)}>Cancel</button>
            <button className="oc-btn oc-btn-ink" disabled={send.isPending || (note === 'required' && !why.trim())} onClick={() => run({ ...(body ?? {}), note: why })}>{text}</button>
          </>
        }>
          <ErrorAlert error={send.error} />
          <TextArea label={note === 'required' ? 'Reason' : 'Note (optional)'} value={why} onChange={setWhy} required={note === 'required'} />
        </Modal>
      )}
    </>
  );
}

/** Approve / reject buttons of a request (base path without the action). */
function Decide({ base }: { base: string }) {
  return (
    <div className="oc-row" style={{ gap: 6 }}>
      <Act label="Approve" path={`${base}:approve`} note="optional" kind="ink" />
      <Act label="Reject" path={`${base}:reject`} note="required" kind="text" />
    </div>
  );
}

function Approvals({ steps }: { steps: unknown }) {
  const list = (steps as R[] | undefined) ?? [];
  if (!list.length) return null;
  return (
    <span className="oc-small oc-muted">{list.map((s) => `${label(s.level)}${s.approverName ? ` (${String(s.approverName)})` : ''}: ${label(s.status)}`).join(' → ')}</span>
  );
}

// ── Schedules (EP-06) ─────────────────────────────────────────────────────

const SCHED_TABS: Option[] = [
  { value: 'schedules', label: 'Schedules' }, { value: 'swaps', label: 'Shift Swaps' }, { value: 'hris.shift_template', label: 'Shift Templates' },
  { value: 'hris.staffing_requirement', label: 'Staffing Requirements' }, { value: 'demand', label: 'Operational Demand' },
];

export function SchedulesPage() {
  const [tab, setTab] = useUrlTab('schedules');
  return (
    <div className="oc-stack">
      <PageHeader title="Schedules" help="Weekly / monthly shift schedules per department from shift templates, coverage against staffing requirements, publishing to Employee Self Service and shift swaps." />
      <Tabs tabs={SCHED_TABS} value={tab} onChange={setTab} />
      {tab === 'schedules' && <ScheduleList />}
      {tab === 'swaps' && <SwapList />}
      {tab === 'demand' && <DemandPanel from={today()} />}
      {tab.startsWith('hris.') && <AutoResourcePage key={tab} resourceKey={tab} />}
    </div>
  );
}

function ScheduleList() {
  const nav = useNavigate();
  const units = useUnits();
  const [params] = useSearchParams();
  const [unit, setUnit] = useState(params.get('orgUnitId') ?? '');
  const [open, setOpen] = useState(false);
  const [f, setF] = useState<Record<string, string>>({ periodStart: monday(addDays(today(), 7)) });
  const list = useGet<Page<R>>(`${HR}/schedules?${unit ? `orgUnitId=${unit}` : ''}`);
  const schedules = useOptions(`${HR}/schedules`, (x) => `${String(x.name)}`);
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap">
        <SelectField label="Department" value={unit} onChange={setUnit} options={units} placeholder="All" />
        <span className="oc-spacer" />
        <button className="oc-btn oc-btn-ink" onClick={() => setOpen(true)}><Icon name="add" size={18} /> New schedule</button>
      </div>
      <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error} onRowClick={(r) => nav(`/hris/schedules/${r.id}`)} columns={[
        { key: 'name', header: 'Schedule' }, { key: 'orgUnitName', header: 'Department' },
        { key: 'periodStart', header: 'Period', render: (r) => `${date(r.periodStart)} – ${date(r.periodEnd)}` },
        { key: 'shifts', header: 'Shifts', align: 'right' }, { key: 'employeeCount', header: 'Employees', align: 'right' },
        { key: 'status', header: 'Status', render: pill('status') }, { key: 'version', header: 'Version', align: 'right' },
      ]} />
      {open && (
        <FormModal open onClose={() => setOpen(false)} title="New Schedule" path={`${HR}/schedules`} onDone={(r) => nav(`/hris/schedules/${r.id}`)}
          body={() => clean({ orgUnitId: f.orgUnitId, periodStart: f.periodStart, periodEnd: f.periodEnd, name: f.name, copyFromScheduleId: f.copy })}>
          <SelectField label="Department" value={f.orgUnitId ?? ''} onChange={(v) => setF({ ...f, orgUnitId: v })} options={units} required />
          <TextField label="First day" type="date" value={f.periodStart ?? ''} onChange={(v) => setF({ ...f, periodStart: v })} required />
          <TextField label="Last day (default one week)" type="date" value={f.periodEnd ?? ''} onChange={(v) => setF({ ...f, periodEnd: v })} />
          <TextField label="Name" value={f.name ?? ''} onChange={(v) => setF({ ...f, name: v })} />
          <SelectField label="Copy shifts of" value={f.copy ?? ''} onChange={(v) => setF({ ...f, copy: v })} options={schedules} placeholder="Start empty" span />
        </FormModal>
      )}
    </div>
  );
}

export function ScheduleDetailPage() {
  const { id = '' } = useParams();
  const path = `${HR}/schedules/${id}`;
  const d = useGet<R>(path);
  const templates = useTemplates();
  const toast = useToast();
  const assign = useSend<Record<string, unknown>, R>('POST', `${path}:assign`, INV);
  const [pattern, setPattern] = useState(false);
  if (d.isLoading) return <Skeleton rows={10} />;
  if (d.error || !d.data) return <ErrorAlert error={d.error} />;
  const s = d.data;
  const days: string[] = [];
  for (let x = String(s.periodStart).slice(0, 10); x <= String(s.periodEnd).slice(0, 10); x = addDays(x, 1)) days.push(x);
  const cell = new Map<string, R>();
  for (const a of (s.assignments as R[]) ?? []) cell.set(`${String(a.employeeId)}|${String(a.workDate).slice(0, 10)}`, a);
  const leave = new Map<string, R>();
  for (const l of (s.leave as R[]) ?? []) leave.set(`${String(l.employeeId)}|${String(l.date).slice(0, 10)}`, l);
  const editable = s.canEdit === true;
  const set = (employeeId: string, workDate: string, v: string) => assign.mutate({
    assignments: [v === '' ? { employeeId, workDate, clear: true } : v === 'off' ? { employeeId, workDate, off: true } : { employeeId, workDate, shiftTemplateId: v }],
  }, { onError: (e) => toast(e.message, 'error') });
  const issues = (s.issues as R[]) ?? [];
  return (
    <div className="oc-stack">
      <PageHeader title={String(s.name)} help={`${String(s.orgUnitName)} · ${date(s.periodStart)} – ${date(s.periodEnd)} · version ${String(s.version)}`} actions={
        <div className="oc-row-wrap">
          <StatusPill status={String(s.status)} label={label(s.status)} />
          {editable && <Act label="Copy previous period" path={`${path}:copy`} />}
          {editable && <button className="oc-btn oc-btn-neutral" onClick={() => setPattern(true)}>Repeat pattern</button>}
          {editable && s.status === 'draft' && <Act label="Publish" path={`${path}:publish`} kind="ink" />}
          {editable && s.status === 'draft' && <Act label="Cancel" path={`${path}:cancel`} kind="text" />}
        </div>
      } />
      <ErrorAlert error={assign.error} />
      {issues.length > 0 && (
        <Card title={`Validation (${issues.length})`} icon="rule">
          <ul className="oc-stack" style={{ gap: 4, margin: 0, paddingLeft: 18 }}>
            {issues.map((i, n) => <li key={n} className={i.severity === 'error' ? 'oc-danger' : 'oc-muted'}><strong>{label(i.severity)}</strong> · {String(i.message)}</li>)}
          </ul>
        </Card>
      )}
      <div className="oc-card" style={{ overflowX: 'auto' }}>
        <table className="oc-table" aria-label="Shift grid">
          <thead>
            <tr><th>Employee</th>{days.map((x) => <th key={x}>{formatDate(x)}</th>)}<th>Hours</th></tr>
          </thead>
          <tbody>
            {((s.employees as R[]) ?? []).map((e) => (
              <tr key={String(e.id)}>
                <th scope="row">{String(e.fullName)}<div className="oc-small oc-muted">{val(e.position)}</div></th>
                {days.map((x) => {
                  const a = cell.get(`${String(e.id)}|${x}`);
                  const l = leave.get(`${String(e.id)}|${x}`);
                  const v = a ? (a.kind === 'off' ? 'off' : String(a.shiftTemplateId)) : '';
                  return (
                    <td key={x} style={{ minWidth: 130, background: a?.color ? `${String(a.color)}22` : undefined }}>
                      {editable ? (
                        <select className="oc-input" aria-label={`${String(e.fullName)} ${x}`} value={v} disabled={assign.isPending} onChange={(ev) => set(String(e.id), x, ev.target.value)}>
                          <option value="">—</option><option value="off">Day off</option>
                          {templates.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}
                        </select>
                      ) : a ? (a.kind === 'off' ? 'Off' : `${String(a.shiftCode)} ${String(a.startTime)}–${String(a.endTime)}`) : '—'}
                      {l && <div className="oc-small"><StatusPill status={l.status === 'approved' ? 'on_leave' : 'pending'} label={`${label(l.leaveType)} leave`} /></div>}
                      {a?.status === 'on_leave' && <div className="oc-small oc-muted">on leave</div>}
                    </td>
                  );
                })}
                <td>{hours(e.scheduledMinutes)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <Card title="Coverage" icon="groups">
        <DataTable rows={withIds(s.coverage, (r, i) => `${String(r.date)}-${i}`)} columns={[
          { key: 'date', header: 'Date', render: (r) => date(r.date) }, { key: 'position', header: 'Position', render: (r) => val(r.position ?? 'Any') },
          { key: 'shift', header: 'Shift', render: (r) => val(r.shift ?? 'Any') }, { key: 'required', header: 'Required', align: 'right' },
          { key: 'assigned', header: 'Assigned', align: 'right' },
          { key: 'short', header: 'Short', render: (r) => (Number(r.short) > 0 ? <StatusPill status="error" label={String(r.short)} /> : 'OK') },
        ]} empty={<p className="oc-muted">No staffing requirements for this department.</p>} />
      </Card>
      {s.status !== 'cancelled' && <OpenShiftsPanel scheduleId={id} editable={editable} days={days} templates={templates} />}
      <DemandPanel from={String(s.periodStart).slice(0, 10)} to={String(s.periodEnd).slice(0, 10)} />
      {pattern && <PatternModal path={path} employees={(s.employees as R[]) ?? []} templates={templates} onClose={() => setPattern(false)} start={String(s.periodStart).slice(0, 10)} />}
    </div>
  );
}

function PatternModal({ path, employees, templates, onClose, start }: { path: string; employees: R[]; templates: Option[]; onClose: () => void; start: string }) {
  const [emps, setEmps] = useState<string[]>([]);
  const [cycle, setCycle] = useState<string[]>(['', '', '', '', '', 'off', 'off']);
  const [from, setFrom] = useState(start);
  return (
    <FormModal open onClose={onClose} title="Repeat Pattern" path={`${path}:apply-pattern`} submit="Apply"
      body={() => ({ employeeIds: emps, pattern: cycle.map((c) => (c === 'off' || c === '' ? null : c)), startDate: from })}>
      <div className="oc-span">
        <p className="oc-muted">Choose the employees and a cycle of days (shift or day off); the cycle repeats over the schedule period.</p>
        {employees.map((e) => (
          <Checkbox key={String(e.id)} label={String(e.fullName)} checked={emps.includes(String(e.id))}
            onChange={(c) => setEmps(c ? [...emps, String(e.id)] : emps.filter((x) => x !== String(e.id)))} />
        ))}
      </div>
      <TextField label="Cycle starts on" type="date" value={from} onChange={setFrom} />
      {cycle.map((c, i) => (
        <SelectField key={i} label={`Day ${i + 1}`} value={c} onChange={(v) => setCycle(cycle.map((x, j) => (j === i ? v : x)))}
          options={[{ value: 'off', label: 'Day off' }, ...templates]} placeholder="Day off" />
      ))}
      <div className="oc-span oc-row" style={{ gap: 8 }}>
        <button type="button" className="oc-btn oc-btn-text" onClick={() => setCycle([...cycle, 'off'])} disabled={cycle.length >= 28}>Add day</button>
        <button type="button" className="oc-btn oc-btn-text" onClick={() => setCycle(cycle.slice(0, -1))} disabled={cycle.length <= 1}>Remove day</button>
      </div>
    </FormModal>
  );
}

function DemandPanel({ from, to }: { from: string; to?: string }) {
  const d = useGet<Page<R>>(`${HR}/staffing-demand?from=${from}&to=${to ?? addDays(from, 6)}`);
  return (
    <Card title="Operational demand" icon="insights">
      <DataTable rows={withIds(d.data?.items, (r) => String(r.date))} loading={d.isLoading} error={d.error} columns={[
        { key: 'date', header: 'Date', render: (r) => date(r.date) },
        ...['golfBookings', 'golfers', 'events', 'eventPax', 'classSessions'].map((k) => ({
          key: k, header: label(k.replace(/([A-Z])/g, ' $1')).replace(/^./, (c) => c.toUpperCase()), align: 'right' as const,
          render: (r: R) => String((r.values as Record<string, number>)?.[k] ?? 0),
        })),
      ]} />
    </Card>
  );
}

function SwapList() {
  const [status, setStatus] = useState('submitted');
  const l = useGet<Page<R>>(`${HR}/shift-swaps?status=${status}`);
  return (
    <div className="oc-stack">
      <FilterPills options={[{ value: '', label: 'All' }, ...opts(['requested', 'submitted', 'approved', 'rejected', 'declined', 'cancelled'])]} value={status} onChange={setStatus} />
      <DataTable rows={l.data?.items} loading={l.isLoading} error={l.error} columns={[
        { key: 'number', header: 'Swap' }, { key: 'requesterName', header: 'Requested by', render: (r) => `${String(r.requesterName)} · ${date(r.requesterDate)} ${val(r.requesterShift)}` },
        { key: 'counterpartName', header: 'With', render: (r) => `${String(r.counterpartName)}${r.counterpartDate ? ` · ${date(r.counterpartDate)} ${val(r.counterpartShift)}` : ' (cover)'}` },
        { key: 'reason', header: 'Reason' }, { key: 'status', header: 'Status', render: pill('status') },
      ]} actions={(r) => (r.status === 'submitted' ? <Decide base={`${HR}/shift-swaps/${r.id}`} /> : null)} />
    </div>
  );
}

// ── Attendance (EP-07) ────────────────────────────────────────────────────

const ATT_TABS: Option[] = [
  { value: 'days', label: 'Attendance Days' }, { value: 'review', label: 'Review Queue' }, { value: 'corrections', label: 'Corrections' },
  { value: 'profiles', label: 'Attendance Profiles' }, { value: 'devices', label: 'Devices' }, { value: 'hris.geofence', label: 'Geofences' },
  { value: 'periods', label: 'Payroll Periods' },
];

export function AttendancePage() {
  const [tab, setTab] = useUrlTab('days');
  return (
    <div className="oc-stack">
      <PageHeader title="Attendance" help="Clock-in / out from devices (face recognition, fingerprint), the Attendance Kiosk and mobile GPS matched to the published shifts; corrections, review of out-of-area clock-ins, devices and payroll period locks." />
      <Tabs tabs={ATT_TABS} value={tab} onChange={setTab} />
      {tab === 'days' && <DaysPanel />}
      {tab === 'review' && <ReviewPanel />}
      {tab === 'corrections' && <CorrectionsPanel />}
      {tab === 'profiles' && <ProfilesPanel />}
      {tab === 'devices' && <DevicesPanel />}
      {tab === 'periods' && <PeriodsPanel />}
      {tab.startsWith('hris.') && <AutoResourcePage key={tab} resourceKey={tab} />}
    </div>
  );
}

const dayColumns = [
  { key: 'workDate', header: 'Date', render: (r: R) => date(r.workDate) },
  { key: 'employeeName', header: 'Employee', render: (r: R) => <strong>{String(r.employeeName)}</strong> },
  { key: 'orgUnitName', header: 'Department', render: (r: R) => val(r.orgUnitName) },
  { key: 'shiftCode', header: 'Shift', render: (r: R) => (r.scheduledStart ? `${val(r.shiftCode)} ${time(r.scheduledStart)}–${time(r.scheduledEnd)}` : '—') },
  { key: 'firstIn', header: 'In', render: (r: R) => time(r.firstIn) }, { key: 'lastOut', header: 'Out', render: (r: R) => time(r.lastOut) },
  { key: 'status', header: 'Status', render: pill('status') }, { key: 'lateMinutes', header: 'Late', render: (r: R) => (Number(r.lateMinutes) ? `${String(r.lateMinutes)} min` : '—') },
  { key: 'workedMinutes', header: 'Worked', render: (r: R) => hours(r.workedMinutes) }, { key: 'overtimeMinutes', header: 'Overtime', render: (r: R) => hours(r.overtimeMinutes) },
  { key: 'flags', header: 'Flags', render: (r: R) => ((r.flags as string[]) ?? []).map(label).join(', ') || '—' },
];

// Exception filters of the attendance days (flag query of attendance-days).
const DAY_FLAGS: Option[] = [
  { value: '', label: 'All days' }, { value: 'any', label: 'Any exception' }, { value: 'missing', label: 'Missing clock-in / out' }, { value: 'out_of_area', label: 'Out of area' },
  { value: 'unapproved_overtime', label: 'Overtime without approval' }, { value: 'no_schedule', label: 'Worked without schedule' },
  { value: 'worked_on_leave', label: 'Worked on leave' },
];

function DaysPanel() {
  const units = useUnits();
  const employees = useEmployees();
  const { can } = useAuth();
  // Filters open from the URL (HR Dashboard drill-downs: ?from=&to=&status=&flag=).
  const [params] = useSearchParams();
  const [f, setF] = useState<Record<string, string>>(() => ({
    from: params.get('from') ?? addDays(today(), -6), to: params.get('to') ?? today(), orgUnitId: params.get('orgUnitId') ?? '',
    status: params.get('status') ?? '', flag: params.get('flag') ?? '',
  }));
  const [clock, setClock] = useState(false);
  const [recalc, setRecalc] = useState(false);
  const [correct, setCorrect] = useState<R | null>(null);
  const nav = useNavigate();
  const q = new URLSearchParams(clean(f) as Record<string, string>).toString();
  const l = useGet<Page<R>>(`${HR}/attendance-days?${q}`);
  const set = (k: string) => (v: string) => setF({ ...f, [k]: v });
  const open = (r: R) => nav(`/hris/employees/${String(r.employeeId)}?tab=attendance`);
  return (
    <div className="oc-stack">
      <FilterPills options={DAY_FLAGS} value={f.flag ?? ''} onChange={set('flag')} />
      <div className="oc-row-wrap">
        <DateRange from={f.from} to={f.to} onFrom={set('from')} onTo={set('to')} />
        <SelectFilter label="Department" all="All departments" value={f.orgUnitId ?? ''} onChange={set('orgUnitId')} options={units} />
        <SelectFilter label="Status" all="All statuses" value={f.status ?? ''} onChange={set('status')} options={opts(DAY_STATUSES)} />
        <span className="oc-spacer" />
        <button className="oc-btn oc-btn-text" onClick={() => setRecalc(true)}><Icon name="refresh" size={18} /> Recalculate</button>
        <button className="oc-btn oc-btn-ink" onClick={() => setClock(true)}><Icon name="add" size={18} /> Record clock-in / out</button>
      </div>
      <DataTable rows={withIds(l.data?.items, (r) => `${String(r.employeeId)}-${String(r.workDate)}`)} loading={l.isLoading} error={l.error} columns={dayColumns}
        onRowClick={open}
        empty={<Empty title={f.flag ? 'No exception in this period' : 'No attendance in this period'} icon="task_alt" />}
        actions={(r) => (!r.locked && can('hris.attendance_correction.create') && ((r.flags as string[]) ?? []).length + (r.status === 'absent' ? 1 : 0) > 0 && (
          <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setCorrect(r)}>Correct</button>
        ))} />
      {correct && (
        <CorrectionForm path={`${HR}/attendance-corrections`} employees={employees} onClose={() => setCorrect(null)}
          initial={{ employeeId: String(correct.employeeId), workDate: String(correct.workDate).slice(0, 10) }} />
      )}
      {clock && <HRClockModal employees={employees} onClose={() => setClock(false)} />}
      {recalc && (
        <FormModal open onClose={() => setRecalc(false)} title="Recalculate Attendance" path={`${HR}/attendance:recalculate`} body={() => ({ from: f.from, to: f.to })} submit="Recalculate">
          <p className="oc-span oc-muted">Re-evaluates the days {date(f.from)} – {date(f.to)} against the published shifts, leave and the Attendance Policy. Locked payroll periods are kept.</p>
        </FormModal>
      )}
    </div>
  );
}

function HRClockModal({ employees, onClose }: { employees: Option[]; onClose: () => void }) {
  const [f, setF] = useState<Record<string, string>>({ direction: 'in', day: today(), at: '08:00' });
  return (
    <FormModal open onClose={onClose} title="Record Clock-in / out" path={`${HR}/attendance:clock`}
      body={() => ({ employeeId: f.employeeId, direction: f.direction, occurredAt: new Date(`${f.day}T${f.at}:00`).toISOString(), note: f.note ?? '' })}>
      <SelectField label="Employee" value={f.employeeId ?? ''} onChange={(v) => setF({ ...f, employeeId: v })} options={employees} required span />
      <SelectField label="Direction" value={f.direction} onChange={(v) => setF({ ...f, direction: v })} options={opts(['in', 'out'])} />
      <TextField label="Date" type="date" value={f.day} onChange={(v) => setF({ ...f, day: v })} />
      <TextField label="Time" type="time" value={f.at} onChange={(v) => setF({ ...f, at: v })} />
      <TextArea label="Reason" value={f.note ?? ''} onChange={(v) => setF({ ...f, note: v })} required span />
    </FormModal>
  );
}

const eventColumns = [
  { key: 'occurredAt', header: 'Time', render: (r: R) => formatDateTime(String(r.occurredAt)) }, { key: 'employeeName', header: 'Employee' },
  { key: 'direction', header: 'In / Out', render: (r: R) => label(r.direction) }, { key: 'method', header: 'Method', render: (r: R) => label(r.method) },
  { key: 'distanceMeters', header: 'Distance', render: (r: R) => (r.distanceMeters != null ? `${Math.round(Number(r.distanceMeters))} m · ${val(r.geofenceName)}` : val(r.deviceName)) },
  { key: 'flags', header: 'Flags', render: (r: R) => ((r.flags as string[]) ?? []).map(label).join(', ') || '—' },
  { key: 'reviewStatus', header: 'Review', render: pill('reviewStatus') },
];

function ReviewPanel() {
  const l = useGet<Page<R>>(`${HR}/attendance-events?review=pending&from=${addDays(today(), -30)}`);
  return (
    <DataTable rows={l.data?.items} loading={l.isLoading} error={l.error} columns={eventColumns} empty={<Empty title="Nothing to review" icon="task_alt" />}
      actions={(r) => (
        <div className="oc-row" style={{ gap: 6 }}>
          <Act label="Accept" path={`${HR}/attendance-events/${r.id}:review`} body={{ decision: 'accepted' }} note="optional" kind="ink" />
          <Act label="Reject" path={`${HR}/attendance-events/${r.id}:review`} body={{ decision: 'rejected' }} note="required" kind="text" />
        </div>
      )} />
  );
}

function CorrectionForm({ path, employees, onClose, initial }: { path: string; employees?: Option[]; onClose: () => void; initial?: Record<string, string> }) {
  const [f, setF] = useState<Record<string, string>>({ workDate: addDays(today(), -1), ...initial });
  return (
    <FormModal open onClose={onClose} title="Attendance Correction" path={path} submit="Send"
      body={() => clean({ employeeId: f.employeeId, workDate: f.workDate, clockIn: f.clockIn, clockOut: f.clockOut, reason: f.reason ?? '' })}>
      {employees && <SelectField label="Employee" value={f.employeeId ?? ''} onChange={(v) => setF({ ...f, employeeId: v })} options={employees} required span />}
      <TextField label="Day" type="date" value={f.workDate} onChange={(v) => setF({ ...f, workDate: v })} required />
      <TextField label="Clock-in" type="time" value={f.clockIn ?? ''} onChange={(v) => setF({ ...f, clockIn: v })} />
      <TextField label="Clock-out" type="time" value={f.clockOut ?? ''} onChange={(v) => setF({ ...f, clockOut: v })} />
      <TextArea label="Reason" value={f.reason ?? ''} onChange={(v) => setF({ ...f, reason: v })} required span />
    </FormModal>
  );
}

function CorrectionsPanel() {
  const employees = useEmployees();
  const [status, setStatus] = useState('submitted');
  const [open, setOpen] = useState(false);
  const l = useGet<Page<R>>(`${HR}/attendance-corrections?status=${status}`);
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap">
        <FilterPills options={[{ value: '', label: 'All' }, ...REQ_STATUSES]} value={status} onChange={setStatus} />
        <span className="oc-spacer" />
        <button className="oc-btn oc-btn-ink" onClick={() => setOpen(true)}>New correction</button>
      </div>
      <DataTable rows={l.data?.items} loading={l.isLoading} error={l.error} columns={[
        { key: 'number', header: 'Correction' }, { key: 'employeeName', header: 'Employee' }, { key: 'workDate', header: 'Day', render: (r) => date(r.workDate) },
        { key: 'clockIn', header: 'In', render: (r) => time(r.clockIn) }, { key: 'clockOut', header: 'Out', render: (r) => time(r.clockOut) },
        { key: 'reason', header: 'Reason' }, { key: 'status', header: 'Status', render: pill('status') },
      ]} actions={(r) => (r.status === 'submitted' ? <Decide base={`${HR}/attendance-corrections/${r.id}`} /> : null)} />
      {open && <CorrectionForm path={`${HR}/attendance-corrections`} employees={employees} onClose={() => setOpen(false)} />}
    </div>
  );
}

function ProfilesPanel() {
  const [q, setQ] = useState('');
  const l = useGet<Page<R>>(`${HR}/attendance-profiles?q=${encodeURIComponent(q)}`);
  const [pin, setPin] = useState<R | null>(null);
  const [consent, setConsent] = useState<R | null>(null);
  return (
    <div className="oc-stack">
      <p className="oc-muted">Biometric templates stay on the devices; OneClub keeps the device user number and the written consent (UU PDP).</p>
      <TextField label="Search" value={q} onChange={setQ} />
      <DataTable rows={withIds(l.data?.items, (r) => String(r.employeeId))} loading={l.isLoading} error={l.error} columns={[
        { key: 'employeeName', header: 'Employee', render: (r) => `${String(r.employeeName)} (${String(r.employeeNo)})` }, { key: 'orgUnitName', header: 'Department', render: (r) => val(r.orgUnitName) },
        { key: 'deviceUserNo', header: 'Device No.', render: (r) => val(r.deviceUserNo) }, { key: 'pinSet', header: 'Kiosk PIN', render: (r) => (r.pinLocked ? 'Locked' : r.pinSet ? 'Set' : '—') },
        { key: 'biometricConsent', header: 'Biometric consent', render: (r) => (r.biometricConsent ? `Signed ${date(r.consentSignedOn)}` : r.consentWithdrawnAt ? 'Withdrawn' : '—') },
      ]} actions={(r) => (
        <div className="oc-row" style={{ gap: 6 }}>
          <button className="oc-btn oc-btn-text" onClick={() => setPin(r)}>Reset PIN</button>
          <button className="oc-btn oc-btn-text" onClick={() => setConsent(r)}>Consent</button>
        </div>
      )} />
      {pin && <PinModal path={`${HR}/attendance-profiles/${String(pin.employeeId)}:set-pin`} onClose={() => setPin(null)} />}
      {consent && <ConsentModal r={consent} onClose={() => setConsent(null)} />}
    </div>
  );
}

function PinModal({ path, onClose, current }: { path: string; onClose: () => void; current?: boolean }) {
  const [pin, setPin] = useState('');
  const [old, setOld] = useState('');
  return (
    <FormModal open onClose={onClose} title="Attendance PIN" path={path} body={() => clean({ pin, currentPin: old })}>
      {current && <TextField label="Current PIN" type="password" inputMode="numeric" value={old} onChange={setOld} />}
      <TextField label="New PIN (6 digits)" type="password" inputMode="numeric" maxLength={6} value={pin} onChange={setPin} required />
    </FormModal>
  );
}

function ConsentModal({ r, onClose }: { r: R; onClose: () => void }) {
  const [consent, setConsent] = useState(!r.biometricConsent);
  const [signed, setSigned] = useState(today());
  return (
    <FormModal open onClose={onClose} title="Biometric Consent" path={`${HR}/attendance-profiles/${String(r.employeeId)}:consent`}
      body={() => (consent ? { consent, signedOn: signed } : { consent })}>
      <div className="oc-span"><Checkbox label="Written consent signed (face / fingerprint enrolment allowed)" checked={consent} onChange={setConsent} /></div>
      {consent ? <TextField label="Signed on" type="date" value={signed} onChange={setSigned} required />
        : <p className="oc-span oc-muted">Withdrawing removes the employee from the biometric devices; the kiosk QR / PIN remains.</p>}
    </FormModal>
  );
}

function DevicesPanel() {
  const employees = useEmployees();
  const l = useGet<Page<R>>(`${HR}/attendance-devices?limit=200`);
  const [sim, setSim] = useState<R | null>(null);
  return (
    <div className="oc-stack">
      <DataTable rows={l.data?.items} loading={l.isLoading} error={l.error} columns={[
        { key: 'code', header: 'Code' }, { key: 'name', header: 'Device' }, { key: 'deviceKind', header: 'Kind', render: (r) => label(r.deviceKind) },
        { key: 'locationPoint', header: 'Location', render: (r) => val(r.locationPoint) }, { key: 'vendor', header: 'Vendor', render: (r) => label(r.vendor) },
        { key: 'lastEventAt', header: 'Last event', render: (r) => (r.lastEventAt ? formatDateTime(String(r.lastEventAt)) : '—') },
        { key: 'lastSyncAt', header: 'Employee sync', render: (r) => (r.lastSyncAt ? formatDateTime(String(r.lastSyncAt)) : '—') },
        { key: 'status', header: 'Status', render: pill('status') },
      ]} actions={(r) => (r.deviceKind === 'biometric' ? (
        <div className="oc-row" style={{ gap: 6 }}>
          <Act label="Sync employees" path={`${HR}/attendance-devices/${r.id}:sync-employees`} />
          {r.vendor === 'mock' && <button className="oc-btn oc-btn-text" onClick={() => setSim(r)}>Simulate scan</button>}
        </div>
      ) : null)} />
      <details><summary>Add or edit devices</summary><AutoResourcePage resourceKey="hris.attendance_device" /></details>
      {sim && <SimModal device={sim} employees={employees} onClose={() => setSim(null)} />}
    </div>
  );
}

function SimModal({ device, employees, onClose }: { device: R; employees: Option[]; onClose: () => void }) {
  const [f, setF] = useState<Record<string, string>>({ method: 'face_recognition' });
  return (
    <FormModal open onClose={onClose} title={`Simulate scan · ${String(device.code)}`} path={`${HR}/attendance-devices/${device.id}:simulate`} submit="Scan"
      body={() => clean({ employeeId: f.employeeId, method: f.method, direction: f.direction })}>
      <SelectField label="Employee" value={f.employeeId ?? ''} onChange={(v) => setF({ ...f, employeeId: v })} options={employees} required span />
      <SelectField label="Method" value={f.method} onChange={(v) => setF({ ...f, method: v })} options={opts(['face_recognition', 'fingerprint'])} />
      <SelectField label="Direction" value={f.direction ?? ''} onChange={(v) => setF({ ...f, direction: v })} options={opts(['in', 'out'])} placeholder="Automatic" />
    </FormModal>
  );
}

function PeriodsPanel() {
  const [f, setF] = useState<Record<string, string>>({ from: addDays(today(), -30), to: addDays(today(), -1) });
  const [lock, setLock] = useState(false);
  const locks = useGet<Page<R>>(`${HR}/time-locks`);
  const sum = useGet<Page<R>>(`${HR}/time-summary?from=${f.from}&to=${f.to}`);
  return (
    <div className="oc-stack">
      <Card title="Payroll period locks" icon="lock" actions={<button className="oc-btn oc-btn-neutral" onClick={() => setLock(true)}>Lock a period</button>}>
        <DataTable rows={locks.data?.items} loading={locks.isLoading} error={locks.error} columns={[
          { key: 'reference', header: 'Reference' }, { key: 'periodStart', header: 'Period', render: (r) => `${date(r.periodStart)} – ${date(r.periodEnd)}` },
          { key: 'status', header: 'Status', render: pill('status') }, { key: 'lockedAt', header: 'Locked', render: (r) => formatDateTime(String(r.lockedAt)) },
        ]} actions={(r) => (r.status === 'locked' ? <Act label="Release" path={`${HR}/time-locks/${r.id}:release`} note="required" kind="text" /> : null)} />
      </Card>
      <Card title="Time summary (payroll input)" icon="summarize">
        <div className="oc-row-wrap">
          <TextField label="From" type="date" value={f.from} onChange={(v) => setF({ ...f, from: v })} />
          <TextField label="To" type="date" value={f.to} onChange={(v) => setF({ ...f, to: v })} />
        </div>
        <DataTable rows={withIds(sum.data?.items, (r) => String(r.employeeId))} loading={sum.isLoading} error={sum.error} columns={[
          { key: 'fullName', header: 'Employee' }, { key: 'scheduledDays', header: 'Scheduled', align: 'right' }, { key: 'presentDays', header: 'Present', align: 'right' },
          { key: 'lateDays', header: 'Late', align: 'right' }, { key: 'absentDays', header: 'Absent', align: 'right' },
          { key: 'paidLeaveDays', header: 'Paid leave', align: 'right' }, { key: 'unpaidLeaveDays', header: 'Unpaid leave', align: 'right' },
          { key: 'overtime', header: 'Overtime (payable / multiplied)', render: (r) => { const o = r.overtime as R; return `${String(o.payableHours)} h / ${String(o.multipliedHours)}`; } },
          { key: 'exceptions', header: 'Exceptions', align: 'right' },
        ]} />
      </Card>
      {lock && (
        <LockModal onClose={() => setLock(false)} />
      )}
    </div>
  );
}

function LockModal({ onClose }: { onClose: () => void }) {
  const [f, setF] = useState<Record<string, string>>({ periodStart: addDays(today(), -30), periodEnd: addDays(today(), -1) });
  return (
    <FormModal open onClose={onClose} title="Lock Payroll Period" path={`${HR}/time-locks`} submit="Close & lock" body={() => f}>
      <TextField label="First day" type="date" value={f.periodStart} onChange={(v) => setF({ ...f, periodStart: v })} />
      <TextField label="Last day" type="date" value={f.periodEnd} onChange={(v) => setF({ ...f, periodEnd: v })} />
      <TextField label="Reference" value={f.reference ?? ''} onChange={(v) => setF({ ...f, reference: v })} required span placeholder="PAY-2026-10" />
    </FormModal>
  );
}

// ── Leave & Permission (EP-08) ────────────────────────────────────────────

const LEAVE_TABS: Option[] = [
  { value: 'requests', label: 'Leave Requests' }, { value: 'permission', label: 'Permission' }, { value: 'balances', label: 'Leave Balances' },
  { value: 'calendar', label: 'Leave Calendar' }, { value: 'hris.holiday', label: 'Holidays' }, { value: 'types', label: 'Leave Types' },
];

export function LeavePage() {
  const [tab, setTab] = useUrlTab('requests');
  return (
    <div className="oc-stack">
      <PageHeader title="Leave & Permission" help="Leave types of the Leave Policy, balances (12 days after 12 months, carry-over at most 6 days until 31 March), requests with approval, permission by hours and the holiday calendar." />
      <Tabs tabs={LEAVE_TABS} value={tab} onChange={setTab} />
      {tab === 'requests' && <LeaveRequests />}
      {tab === 'permission' && <PermissionRequests />}
      {tab === 'balances' && <Balances />}
      {tab === 'calendar' && <LeaveCalendar path={`${HR}/leave-calendar`} />}
      {tab === 'types' && <LeaveTypes path={`${HR}/leave-types`} />}
      {tab.startsWith('hris.') && <AutoResourcePage key={tab} resourceKey={tab} />}
    </div>
  );
}

function LeaveForm({ path, employees, types, onClose }: { path: string; employees?: Option[]; types: R[]; onClose: () => void }) {
  const [f, setF] = useState<Record<string, string>>({ startDate: addDays(today(), 7) });
  const t = types.find((x) => x.code === f.leaveType);
  return (
    <FormModal open onClose={onClose} title="Request Leave" path={path} submit="Send"
      body={() => clean({ employeeId: f.employeeId, leaveType: f.leaveType, startDate: f.startDate, endDate: f.endDate, halfDay: f.halfDay, reason: f.reason })}>
      {employees && <SelectField label="Employee" value={f.employeeId ?? ''} onChange={(v) => setF({ ...f, employeeId: v })} options={employees} required span />}
      <SelectField label="Leave type" value={f.leaveType ?? ''} onChange={(v) => setF({ ...f, leaveType: v })} required
        options={types.filter((x) => x.kind === 'leave').map((x) => ({ value: String(x.code), label: String(x.name) }))} />
      <TextField label="First day" type="date" value={f.startDate} onChange={(v) => setF({ ...f, startDate: v })} required />
      <TextField label="Last day" type="date" value={f.endDate ?? ''} onChange={(v) => setF({ ...f, endDate: v })} />
      <SelectField label="Half day" value={f.halfDay ?? ''} onChange={(v) => setF({ ...f, halfDay: v })} options={[{ value: 'am', label: 'Morning off' }, { value: 'pm', label: 'Afternoon off' }]} placeholder="Full days" />
      <TextArea label="Reason" value={f.reason ?? ''} onChange={(v) => setF({ ...f, reason: v })} span />
      {t?.requiresDocument === true && <p className="oc-span oc-alert oc-alert-warning">{String(t.name)} needs a supporting document: attach it in Human Resources → Employees → Documents and send the request from HR.</p>}
    </FormModal>
  );
}

function LeaveRequests() {
  const employees = useEmployees();
  const types = useGet<Page<R>>(`${HR}/leave-types`);
  const [status, setStatus] = useState('submitted');
  const [open, setOpen] = useState(false);
  const l = useGet<Page<R>>(`${HR}/leave-requests?status=${status}`);
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap">
        <FilterPills options={[{ value: '', label: 'All' }, ...REQ_STATUSES]} value={status} onChange={setStatus} />
        <span className="oc-spacer" />
        <button className="oc-btn oc-btn-ink" onClick={() => setOpen(true)}>Request leave</button>
      </div>
      <DataTable rows={l.data?.items} loading={l.isLoading} error={l.error} columns={[
        { key: 'number', header: 'Request' }, { key: 'employeeName', header: 'Employee' }, { key: 'leaveType', header: 'Type', render: (r) => label(r.leaveType) },
        { key: 'startDate', header: 'Period', render: (r) => `${date(r.startDate)}${r.endDate !== r.startDate ? ` – ${date(r.endDate)}` : ''}${r.halfDay ? ` (${String(r.halfDay)})` : ''}` },
        { key: 'days', header: 'Days', align: 'right' }, { key: 'paid', header: 'Paid', render: (r) => (r.paid ? 'Yes' : 'No') },
        { key: 'scheduleConflicts', header: 'Shifts', render: (r) => (Number(r.scheduleConflicts) ? <StatusPill status="pending" label={`${String(r.scheduleConflicts)} shifts`} /> : '—') },
        { key: 'status', header: 'Status', render: pill('status') },
      ]} actions={(r) => (r.status === 'submitted' ? <Decide base={`${HR}/leave-requests/${r.id}`} />
        : r.status === 'approved' ? <Act label="Cancel" path={`${HR}/leave-requests/${r.id}:cancel`} note="required" kind="text" /> : null)} />
      {open && <LeaveForm path={`${HR}/leave-requests`} employees={employees} types={types.data?.items ?? []} onClose={() => setOpen(false)} />}
    </div>
  );
}

function PermissionForm({ path, employees, types, onClose }: { path: string; employees?: Option[]; types: R[]; onClose: () => void }) {
  const [f, setF] = useState<Record<string, string>>({ workDate: today(), startTime: '08:00', endTime: '10:00' });
  return (
    <FormModal open onClose={onClose} title="Request Permission" path={path} submit="Send"
      body={() => clean({ employeeId: f.employeeId, permissionType: f.permissionType, workDate: f.workDate, startTime: f.startTime, endTime: f.endTime, reason: f.reason ?? '' })}>
      {employees && <SelectField label="Employee" value={f.employeeId ?? ''} onChange={(v) => setF({ ...f, employeeId: v })} options={employees} required span />}
      <SelectField label="Permission" value={f.permissionType ?? ''} onChange={(v) => setF({ ...f, permissionType: v })} required
        options={types.filter((x) => x.kind === 'permission').map((x) => ({ value: String(x.code), label: `${String(x.name)} (max ${String(x.maxHours)} h)` }))} />
      <TextField label="Day" type="date" value={f.workDate} onChange={(v) => setF({ ...f, workDate: v })} required />
      <TextField label="From" type="time" value={f.startTime} onChange={(v) => setF({ ...f, startTime: v })} />
      <TextField label="Until" type="time" value={f.endTime} onChange={(v) => setF({ ...f, endTime: v })} />
      <TextArea label="Reason" value={f.reason ?? ''} onChange={(v) => setF({ ...f, reason: v })} required span />
    </FormModal>
  );
}

function PermissionRequests() {
  const employees = useEmployees();
  const types = useGet<Page<R>>(`${HR}/leave-types`);
  const [status, setStatus] = useState('submitted');
  const [open, setOpen] = useState(false);
  const l = useGet<Page<R>>(`${HR}/permission-requests?status=${status}`);
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap">
        <FilterPills options={[{ value: '', label: 'All' }, ...REQ_STATUSES]} value={status} onChange={setStatus} />
        <span className="oc-spacer" />
        <button className="oc-btn oc-btn-ink" onClick={() => setOpen(true)}>Request permission</button>
      </div>
      <DataTable rows={l.data?.items} loading={l.isLoading} error={l.error} columns={[
        { key: 'number', header: 'Request' }, { key: 'employeeName', header: 'Employee' }, { key: 'permissionType', header: 'Type', render: (r) => label(r.permissionType) },
        { key: 'workDate', header: 'Day', render: (r) => `${date(r.workDate)} ${time(r.startsAt)}–${time(r.endsAt)}` },
        { key: 'paid', header: 'Paid', render: (r) => (r.paid ? 'Yes' : 'No') }, { key: 'reason', header: 'Reason' }, { key: 'status', header: 'Status', render: pill('status') },
      ]} actions={(r) => (r.status === 'submitted' ? <Decide base={`${HR}/permission-requests/${r.id}`} /> : null)} />
      {open && <PermissionForm path={`${HR}/permission-requests`} employees={employees} types={types.data?.items ?? []} onClose={() => setOpen(false)} />}
    </div>
  );
}

function Balances() {
  const units = useUnits();
  const employees = useEmployees();
  const [year, setYear] = useState(String(new Date().getFullYear()));
  const [unit, setUnit] = useState('');
  const [adjust, setAdjust] = useState(false);
  const [imp, setImp] = useState(false);
  const l = useGet<Page<R>>(`${HR}/leave-balances?year=${year}${unit ? `&orgUnitId=${unit}` : ''}`);
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap">
        <TextField label="Year" inputMode="numeric" value={year} onChange={setYear} />
        <SelectField label="Department" value={unit} onChange={setUnit} options={units} placeholder="All" />
        <span className="oc-spacer" />
        <Act label="Run accrual" path={`${HR}/leave-balances:accrue`} />
        <button className="oc-btn oc-btn-neutral" onClick={() => setAdjust(true)}>Adjust</button>
        <button className="oc-btn oc-btn-neutral" onClick={() => setImp(true)}>Import</button>
      </div>
      <DataTable rows={l.data?.items} loading={l.isLoading} error={l.error} columns={[
        { key: 'employeeName', header: 'Employee' }, { key: 'orgUnitName', header: 'Department', render: (r) => val(r.orgUnitName) },
        { key: 'leaveType', header: 'Type', render: (r) => label(r.leaveType) }, { key: 'entitled', header: 'Entitled', align: 'right' },
        { key: 'carriedOver', header: 'Carried over', render: (r) => `${String(r.carriedOver)}${Number(r.carriedExpired) ? ` (lapsed ${String(r.carriedExpired)})` : r.carryOverExpiresOn ? ` until ${date(r.carryOverExpiresOn)}` : ''}` },
        { key: 'adjusted', header: 'Adjusted', align: 'right' }, { key: 'used', header: 'Used', align: 'right' }, { key: 'pending', header: 'Pending', align: 'right' },
        { key: 'available', header: 'Available', align: 'right' },
      ]} />
      {adjust && <AdjustModal employees={employees} year={year} onClose={() => setAdjust(false)} />}
      {imp && <ImportModal onClose={() => setImp(false)} />}
    </div>
  );
}

function AdjustModal({ employees, year, onClose }: { employees: Option[]; year: string; onClose: () => void }) {
  const [f, setF] = useState<Record<string, string>>({ leaveType: 'ANNUAL', year });
  return (
    <FormModal open onClose={onClose} title="Adjust Leave Balance" path={`${HR}/leave-balances:adjust`}
      body={() => ({ employeeId: f.employeeId, leaveType: f.leaveType, year: Number(f.year), days: f.days ?? '', note: f.note ?? '' })}>
      <SelectField label="Employee" value={f.employeeId ?? ''} onChange={(v) => setF({ ...f, employeeId: v })} options={employees} required span />
      <TextField label="Leave type" value={f.leaveType} onChange={(v) => setF({ ...f, leaveType: v })} />
      <TextField label="Year" inputMode="numeric" value={f.year} onChange={(v) => setF({ ...f, year: v })} />
      <TextField label="Days (+ adds, − deducts)" inputMode="decimal" value={f.days ?? ''} onChange={(v) => setF({ ...f, days: v })} required />
      <TextArea label="Reason" value={f.note ?? ''} onChange={(v) => setF({ ...f, note: v })} required span />
    </FormModal>
  );
}

function ImportModal({ onClose }: { onClose: () => void }) {
  const [csv, setCsv] = useState('employeeNo,leaveType,year,entitled,carriedOver,carryOverExpiresOn,used,note\n');
  const [dry, setDry] = useState(true);
  const [res, setRes] = useState<R | null>(null);
  return (
    <FormModal open onClose={onClose} title="Import Leave Balances" path={`${HR}/leave-balances:import`} submit={dry ? 'Check' : 'Import'} body={() => ({ csv, dryRun: dry })}
      onDone={(r) => setRes(r)}>
      <TextArea label="CSV (balances at cut-over)" value={csv} onChange={setCsv} rows={8} span />
      <div className="oc-span"><Checkbox label="Dry run (validate only)" checked={dry} onChange={setDry} /></div>
      {res && <p className="oc-span">Rows {String(res.rows)} · inserted {String(res.inserted)} · updated {String(res.updated)} · failed {String(res.failed)}</p>}
    </FormModal>
  );
}

function LeaveCalendar({ path }: { path: string }) {
  const [from, setFrom] = useState(today());
  const [to, setTo] = useState(addDays(today(), 30));
  const l = useGet<Page<R>>(`${path}?from=${from}&to=${to}`);
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap">
        <TextField label="From" type="date" value={from} onChange={setFrom} />
        <TextField label="To" type="date" value={to} onChange={setTo} />
      </div>
      <DataTable rows={withIds(l.data?.items, (r) => String(r.requestId))} loading={l.isLoading} error={l.error} columns={[
        { key: 'employeeName', header: 'Employee' }, { key: 'orgUnitName', header: 'Department', render: (r) => val(r.orgUnitName) },
        { key: 'leaveType', header: 'Type', render: (r) => label(r.leaveType) }, { key: 'startDate', header: 'Period', render: (r) => `${date(r.startDate)} – ${date(r.endDate)}` },
        { key: 'days', header: 'Days', align: 'right' }, { key: 'status', header: 'Status', render: pill('status') },
        { key: 'scheduleConflicts', header: 'Shifts in the period', render: (r) => (Number(r.scheduleConflicts) ? <StatusPill status="pending" label={`${String(r.scheduleConflicts)} to cover`} /> : '—') },
      ]} empty={<p className="oc-muted">Nobody is on leave in this period.</p>} />
    </div>
  );
}

function LeaveTypes({ path }: { path: string }) {
  const l = useGet<Page<R>>(path);
  return (
    <DataTable rows={withIds(l.data?.items, (r) => `${String(r.kind)}-${String(r.code)}`)} loading={l.isLoading} error={l.error} columns={[
      { key: 'kind', header: 'Kind', render: (r) => label(r.kind) }, { key: 'code', header: 'Code' }, { key: 'name', header: 'Name' },
      { key: 'paid', header: 'Paid', render: (r) => (r.paid ? 'Yes' : 'No') },
      { key: 'days', header: 'Entitlement', render: (r) => (r.kind === 'permission' ? `max ${String(r.maxHours)} h` : Number(r.days) ? `${String(r.days)} days ${r.accrual === 'annual' ? 'a year' : 'per event'}` : 'as approved') },
      { key: 'eligibleAfterMonths', header: 'After', render: (r) => (Number(r.eligibleAfterMonths) ? `${String(r.eligibleAfterMonths)} months` : '—') },
      { key: 'requiresDocument', header: 'Document', render: (r) => (r.requiresDocument ? 'Required' : '—') },
    ]} />
  );
}

// ── Overtime (EP-08) ──────────────────────────────────────────────────────

export function OvertimePage() {
  const [tab, setTab] = useUrlTab('requests');
  const employees = useEmployees();
  const [status, setStatus] = useState('submitted');
  const [open, setOpen] = useState(false);
  const l = useGet<Page<R>>(`${HR}/overtime-requests?status=${status}`);
  const ex = useGet<Page<R>>(tab === 'exceptions' ? `${HR}/overtime-exceptions` : null);
  return (
    <div className="oc-stack">
      <PageHeader title="Overtime" help="Overtime requests (before or after the work) with approval; payable hours follow the attendance; pay basis 1/173 of the monthly wage with the multipliers of the Overtime Policy (PP 35/2021)." />
      <Tabs tabs={[{ value: 'requests', label: 'Requests' }, { value: 'exceptions', label: 'Without approval' }]} value={tab} onChange={setTab} />
      {tab === 'requests' && (
        <>
          <div className="oc-row-wrap">
            <FilterPills options={[{ value: '', label: 'All' }, ...REQ_STATUSES]} value={status} onChange={setStatus} />
            <span className="oc-spacer" />
            <button className="oc-btn oc-btn-ink" onClick={() => setOpen(true)}>Request overtime</button>
          </div>
          <DataTable rows={l.data?.items} loading={l.isLoading} error={l.error} columns={overtimeColumns} actions={(r) => (r.status === 'submitted' ? <Decide base={`${HR}/overtime-requests/${r.id}`} /> : null)} />
        </>
      )}
      {tab === 'exceptions' && (
        <DataTable rows={withIds(ex.data?.items, (r) => `${String(r.employeeId)}-${String(r.workDate)}`)} loading={ex.isLoading} error={ex.error} columns={[
          { key: 'workDate', header: 'Day', render: (r) => date(r.workDate) }, { key: 'employeeName', header: 'Employee' },
          { key: 'scheduledEnd', header: 'Shift end', render: (r) => time(r.scheduledEnd) }, { key: 'lastOut', header: 'Clocked out', render: (r) => time(r.lastOut) },
          { key: 'overtimeMinutes', header: 'Overtime', render: (r) => hours(r.overtimeMinutes) }, { key: 'approvedHours', header: 'Approved (h)', align: 'right' },
        ]} empty={<Empty title="No overtime without approval" icon="task_alt" />} />
      )}
      {open && <OvertimeForm path={`${HR}/overtime-requests`} employees={employees} onClose={() => setOpen(false)} />}
    </div>
  );
}

const overtimeColumns = [
  { key: 'number', header: 'Request' }, { key: 'employeeName', header: 'Employee' },
  { key: 'workDate', header: 'Day', render: (r: R) => `${date(r.workDate)} ${time(r.startsAt)}–${time(r.endsAt)}` },
  { key: 'dayKind', header: 'Day kind', render: (r: R) => label(r.dayKind) }, { key: 'hours', header: 'Hours', align: 'right' as const },
  { key: 'payableHours', header: 'Payable', render: (r: R) => `${String(r.payableHours)} h${r.actualHours ? ` (clocked ${String(r.actualHours)})` : ''}` },
  { key: 'tiers', header: 'Multipliers', render: (r: R) => ((r.tiers as R[]) ?? []).map((t) => `${String(t.hours)} h × ${String(t.factor)}`).join(' + ') || '—' },
  { key: 'estimatedPay', header: 'Est. pay', render: (r: R) => (r.estimatedPay ? `Rp${Number(r.estimatedPay).toLocaleString('id-ID')}` : '—') },
  { key: 'status', header: 'Status', render: pill('status') },
];

function OvertimeForm({ path, employees, onClose }: { path: string; employees?: Option[]; onClose: () => void }) {
  const [f, setF] = useState<Record<string, string>>({ workDate: today(), startTime: '17:00', endTime: '19:00' });
  return (
    <FormModal open onClose={onClose} title="Request Overtime" path={path} submit="Send"
      body={() => clean({ employeeId: f.employeeId, workDate: f.workDate, startTime: f.startTime, endTime: f.endTime, reason: f.reason ?? '' })}>
      {employees && <SelectField label="Employee" value={f.employeeId ?? ''} onChange={(v) => setF({ ...f, employeeId: v })} options={employees} required span />}
      <TextField label="Day" type="date" value={f.workDate} onChange={(v) => setF({ ...f, workDate: v })} required />
      <TextField label="From" type="time" value={f.startTime} onChange={(v) => setF({ ...f, startTime: v })} />
      <TextField label="Until" type="time" value={f.endTime} onChange={(v) => setF({ ...f, endTime: v })} />
      <TextArea label="Reason" value={f.reason ?? ''} onChange={(v) => setF({ ...f, reason: v })} required span />
    </FormModal>
  );
}

// ── Attendance Kiosk (ops, FR-ATT-02, FR-ATT-07) ──────────────────────────

export function KioskPage() {
  const info = useGet<R>(`${HR}/attendance/kiosk`);
  const online = useOnline();
  const toast = useToast();
  const [no, setNo] = useState('');
  const [pin, setPin] = useState('');
  const [last, setLast] = useState<R | null>(null);
  const [busy, setBusy] = useState(false);
  const clock = async (body: Record<string, unknown>) => {
    const payload = { ...body, clientEventId: uuidv7() };
    setBusy(true);
    try {
      if (!online) {
        await enqueue('hris.kiosk_clock', { ...payload, occurredAt: new Date().toISOString() }, getActiveProperty() ?? '');
        setLast({ id: 'offline', message: 'Saved offline — sent when the connection returns' });
      } else {
        const r = await request<R>('POST', `${HR}/attendance/kiosk:clock`, payload);
        setLast({ ...r, id: 'online' });
      }
      setNo('');
      setPin('');
    } catch (e) {
      toast((e as Error).message, 'error');
    } finally {
      setBusy(false);
    }
  };
  if (info.isLoading) return <Skeleton rows={6} />;
  if (info.error) return <Empty title="Not an Attendance Kiosk" help="Register this tablet in Settings → Devices and link it in Human Resources → Time & Attendance → Attendance → Devices (kind Kiosk), then sign in with your PIN." icon="qr_code_scanner" />;
  const k = info.data as R;
  const ev = (last?.event as R | undefined) ?? undefined;
  return (
    <div className="oc-stack" style={{ maxWidth: 720, margin: '0 auto' }}>
      <PageHeader title="Attendance Kiosk" help={`${String(k.name)}${k.location ? ` · ${String(k.location)}` : ''} — scan your QR from Employee Self Service or enter your employee number and PIN.`} />
      {!online && <div className="oc-alert oc-alert-warning" role="status">Offline: clock-ins are kept on this kiosk and sent once the connection returns.</div>}
      {last && (
        <div className="oc-alert oc-alert-success" role="status" aria-live="polite">
          {ev ? <><strong>{String(ev.employeeName)}</strong> · clocked {String(ev.direction)} at {time(ev.occurredAt)} · {label((last.day as R)?.status)}</> : String(last.message)}
        </div>
      )}
      <Card title="Scan QR" icon="qr_code_scanner">
        <ScanField label="QR code" onCode={(c) => void clock({ qrToken: c })} />
      </Card>
      <Card title="Employee number & PIN" icon="pin">
        <div className="oc-form">
          <TextField label="Employee No." value={no} onChange={setNo} autoComplete="off" />
          <TextField label="PIN" type="password" inputMode="numeric" maxLength={6} value={pin} onChange={setPin} autoComplete="off" />
        </div>
        <div className="oc-row" style={{ gap: 8, marginTop: 12 }}>
          <button className="oc-btn oc-btn-ink" style={{ minHeight: 56, flex: 1 }} disabled={busy || !no || pin.length < 6} onClick={() => void clock({ employeeNo: no, pin, direction: 'in' })}>Clock in</button>
          <button className="oc-btn oc-btn-neutral" style={{ minHeight: 56, flex: 1 }} disabled={busy || !no || pin.length < 6} onClick={() => void clock({ employeeNo: no, pin, direction: 'out' })}>Clock out</button>
        </div>
      </Card>
    </div>
  );
}

// ── Employee Self Service sections (EP-16) ───────────────────────────────

function EssBack({ base, title }: { base: string; title: string }) {
  return (
    <div className="oc-row" style={{ gap: 8 }}>
      <Link className="oc-btn oc-btn-text" to={base} aria-label="Back to Employee Self Service"><Icon name="arrow_back" size={22} /></Link>
      <h1 style={{ margin: 0, fontSize: 24 }}>{title}</h1>
    </div>
  );
}

function shiftText(a: R) {
  return a.kind === 'off' ? 'Day off' : `${val(a.shiftName)} ${String(a.startTime)}–${String(a.endTime)}`;
}

/** My Schedule: the published shifts (kept offline), swaps with colleagues. */
function EssSchedule({ base }: { base: string }) {
  const [from, setFrom] = useState(monday(today()));
  const path = `${ESS}/schedule?from=${from}&to=${addDays(from, 13)}`;
  const s = useGet<R>(path);
  const online = useOnline();
  const [cached, setCached] = useState<R | null>(null);
  const [swap, setSwap] = useState<R | null>(null);
  const property = getActiveProperty() ?? '';
  React.useEffect(() => {
    if (s.data) void cachePut(property, 'ess.schedule', s.data);
    else if (s.error || !online) void cacheGet<R>(property, 'ess.schedule').then((c) => setCached(c ?? null));
  }, [s.data, s.error, online, property]);
  const data = s.data ?? cached;
  return (
    <div className="oc-stack">
      <EssBack base={base} title="My Schedule" />
      <div className="oc-row-wrap">
        <button className="oc-btn oc-btn-text" onClick={() => setFrom(addDays(from, -7))} aria-label="Previous week"><Icon name="chevron_left" size={22} /></button>
        <strong>{formatDate(from)} – {formatDate(addDays(from, 13))}</strong>
        <button className="oc-btn oc-btn-text" onClick={() => setFrom(addDays(from, 7))} aria-label="Next week"><Icon name="chevron_right" size={22} /></button>
        {!s.data && cached && <span className="oc-chip">Offline copy · {formatDateTime(String(cached.cachedAt))}</span>}
      </div>
      {!data ? (s.isLoading ? <Skeleton rows={6} /> : <ErrorAlert error={s.error} />) : (
        <>
          <div className="oc-stack" style={{ gap: 8 }}>
            {Array.from({ length: 14 }, (_, i) => addDays(from, i)).map((d) => {
              const a = ((data.assignments as R[]) ?? []).find((x) => String(x.workDate).slice(0, 10) === d);
              const l = ((data.leave as R[]) ?? []).find((x) => String(x.startDate).slice(0, 10) <= d && String(x.endDate).slice(0, 10) >= d);
              const h = ((data.holidays as R[]) ?? []).find((x) => x.date === d);
              return (
                <div key={d} className="oc-card" style={{ padding: 12, display: 'flex', gap: 12, alignItems: 'center', minHeight: 56 }}>
                  <strong style={{ width: 120 }}>{formatDate(d)}</strong>
                  <span style={{ flex: 1 }}>{a ? shiftText(a) : '—'}{h ? ` · ${String(h.name)}` : ''}{l ? ` · ${label(l.leaveType)} leave (${String(l.status)})` : ''}</span>
                  {a && a.kind === 'shift' && a.status === 'scheduled' && d >= today() && online && (
                    <button className="oc-btn oc-btn-neutral" style={{ minHeight: 44 }} onClick={() => setSwap(a)}>Swap</button>
                  )}
                </div>
              );
            })}
          </div>
          <Card title="Shift swaps" icon="swap_horiz">
            <DataTable rows={(data.swaps as R[]) ?? []} columns={[
              { key: 'number', header: 'Swap' }, { key: 'requesterName', header: 'From', render: (r) => `${String(r.requesterName)} · ${date(r.requesterDate)}` },
              { key: 'counterpartName', header: 'With', render: (r) => `${String(r.counterpartName)}${r.counterpartDate ? ` · ${date(r.counterpartDate)}` : ''}` },
              { key: 'status', header: 'Status', render: pill('status') },
            ]} actions={(r) => {
              const mine = ((data.colleagues as R[]) ?? []).every((c) => c.id !== r.requesterId);
              if (r.status === 'requested' && !mine) return <div className="oc-row" style={{ gap: 6 }}><Act label="Accept" path={`${ESS}/shift-swaps/${r.id}:accept`} kind="ink" /><Act label="Decline" path={`${ESS}/shift-swaps/${r.id}:decline`} note="optional" kind="text" /></div>;
              if ((r.status === 'requested' || r.status === 'submitted') && mine) return <Act label="Withdraw" path={`${ESS}/shift-swaps/${r.id}:cancel`} kind="text" />;
              return null;
            }} empty={<p className="oc-muted">No swaps.</p>} />
          </Card>
        </>
      )}
      {swap && data && <SwapModal mine={swap} colleagues={(data.colleagues as R[]) ?? []} onClose={() => setSwap(null)} />}
    </div>
  );
}

function SwapModal({ mine, colleagues, onClose }: { mine: R; colleagues: R[]; onClose: () => void }) {
  const [who, setWho] = useState('');
  const [back, setBack] = useState('');
  const [reason, setReason] = useState('');
  const c = colleagues.find((x) => x.id === who);
  return (
    <FormModal open onClose={onClose} title={`Swap ${formatDate(String(mine.workDate).slice(0, 10))}`} path={`${ESS}/shift-swaps`} submit="Ask colleague"
      body={() => clean({ assignmentId: mine.id, counterpartEmployeeId: who, counterpartAssignmentId: back, reason })}>
      <SelectField label="Colleague" value={who} onChange={setWho} options={colleagues.map((x) => ({ value: String(x.id), label: String(x.fullName) }))} required span />
      <SelectField label="Take one of their shifts in return" value={back} onChange={setBack} placeholder="No — the colleague covers my shift" span
        options={((c?.assignments as R[]) ?? []).map((a) => ({ value: String(a.id), label: `${formatDate(String(a.workDate).slice(0, 10))} · ${shiftText(a)}` }))} />
      <TextArea label="Reason" value={reason} onChange={setReason} required span />
    </FormModal>
  );
}

/** Clock In / Out with mobile GPS (offline queue) and the personal kiosk QR. */
function EssClock({ base }: { base: string }) {
  const st = useGet<R>(`${ESS}/clock-status`);
  const online = useOnline();
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState('');
  const [qr, setQr] = useState(false);
  const [pin, setPin] = useState(false);
  const q = useGet<R>(qr ? `${ESS}/attendance-qr` : null, { refetchInterval: 90_000 });
  const clock = () => {
    if (!navigator.geolocation) {
      toast('Location is not available on this device', 'error');
      return;
    }
    setBusy(true);
    navigator.geolocation.getCurrentPosition(async (pos) => {
      const body = { latitude: pos.coords.latitude, longitude: pos.coords.longitude, accuracyMeters: Math.round(pos.coords.accuracy), clientEventId: uuidv7() };
      try {
        if (!online) {
          await enqueue('hris.ess_clock', { ...body, occurredAt: new Date().toISOString() }, getActiveProperty() ?? '');
          setMsg('Saved offline — sent when the connection returns');
        } else {
          const r = await request<R>('POST', `${ESS}/attendance:clock`, body);
          setMsg(String(r.message));
          void st.refetch();
        }
      } catch (e) {
        toast((e as Error).message, 'error');
      } finally {
        setBusy(false);
      }
    }, (e) => { setBusy(false); toast(e.message || 'Location denied', 'error'); }, { enableHighAccuracy: true, timeout: 15000 });
  };
  const s = st.data;
  return (
    <div className="oc-stack">
      <EssBack base={base} title="Clock In / Out" />
      {st.isLoading ? <Skeleton rows={4} /> : st.error ? <ErrorAlert error={st.error} /> : s && (
        <>
          <Card title={formatDate(String(s.today))} icon="today">
            <KV items={[['Shift', s.shift ? shiftText(s.shift as R) : 'No shift today'], ['Status', label((s.day as R)?.status)],
              ['In', time((s.day as R)?.firstIn)], ['Out', time((s.day as R)?.lastOut)]]} />
          </Card>
          {msg && <div className="oc-alert oc-alert-success" role="status" aria-live="polite">{msg}</div>}
          {s.mobileGps ? (
            <button className="oc-btn oc-btn-ink" style={{ minHeight: 72, fontSize: 20 }} disabled={busy} onClick={clock}>
              <Icon name="my_location" size={26} /> {busy ? 'Locating…' : `Clock ${String(s.nextDirection)}`}
            </button>
          ) : <p className="oc-muted">Clock in at the Attendance Kiosk or a face / fingerprint device of your area.</p>}
          <div className="oc-row-wrap">
            <button className="oc-btn oc-btn-neutral" style={{ minHeight: 48 }} onClick={() => setQr(!qr)}>{qr ? 'Hide my kiosk QR' : 'Show my kiosk QR'}</button>
            <button className="oc-btn oc-btn-text" style={{ minHeight: 48 }} onClick={() => setPin(true)}>{s.pinSet ? 'Change kiosk PIN' : 'Set kiosk PIN'}</button>
          </div>
          {qr && q.data && <Card title="Kiosk QR" icon="qr_code"><QRCode value={String(q.data.token)} size={240} label="Attendance QR" /><p className="oc-small oc-muted">Valid for 2 minutes; refreshed automatically.</p></Card>}
        </>
      )}
      {pin && <PinModal path={`${ESS}/attendance-pin`} current={s?.pinSet === true} onClose={() => setPin(false)} />}
    </div>
  );
}

function EssAttendance({ base }: { base: string }) {
  const a = useGet<R>(`${ESS}/attendance`);
  const corr = useGet<Page<R>>(`${ESS}/attendance-corrections`);
  const [open, setOpen] = useState(false);
  return (
    <div className="oc-stack">
      <EssBack base={base} title="Attendance History" />
      <Card title="Last 30 days" icon="event_available" actions={<button className="oc-btn oc-btn-neutral" style={{ minHeight: 44 }} onClick={() => setOpen(true)}>Request correction</button>}>
        <DataTable rows={withIds((a.data as R | undefined)?.days, (r) => String(r.workDate))} loading={a.isLoading} error={a.error} columns={dayColumns.filter((c) => c.key !== 'employeeName')} />
      </Card>
      <Card title="My corrections" icon="edit_calendar">
        <DataTable rows={corr.data?.items} loading={corr.isLoading} error={corr.error} columns={[
          { key: 'workDate', header: 'Day', render: (r) => date(r.workDate) }, { key: 'clockIn', header: 'In', render: (r) => time(r.clockIn) },
          { key: 'clockOut', header: 'Out', render: (r) => time(r.clockOut) }, { key: 'status', header: 'Status', render: pill('status') },
          { key: 'approvals', header: 'Approval', render: (r) => <Approvals steps={r.approvals} /> },
        ]} actions={(r) => (r.status === 'submitted' ? <Act label="Withdraw" path={`${ESS}/attendance-corrections/${r.id}:cancel`} kind="text" /> : null)}
          empty={<p className="oc-muted">No corrections.</p>} />
      </Card>
      {open && <CorrectionForm path={`${ESS}/attendance-corrections`} onClose={() => setOpen(false)} />}
    </div>
  );
}

function EssLeave({ base }: { base: string }) {
  const bal = useGet<Page<R>>(`${ESS}/leave-balances`);
  const types = useGet<Page<R>>(`${ESS}/leave-types`);
  const reqs = useGet<Page<R>>(`${ESS}/leave-requests`);
  const perms = useGet<Page<R>>(`${ESS}/permission-requests`);
  const [leave, setLeave] = useState(false);
  const [perm, setPerm] = useState(false);
  return (
    <div className="oc-stack">
      <EssBack base={base} title="Leave & Permission" />
      <div className="oc-grid">
        {(bal.data?.items ?? []).map((b) => (
          <div key={b.id} className="oc-card"><div className="oc-muted">{label(b.leaveType)} {String(b.year)}</div><div style={{ fontSize: 28, fontWeight: 600 }}>{String(b.available)} days</div>
            <div className="oc-small oc-muted">entitled {String(b.entitled)} · carried {String(b.carriedOver)} · used {String(b.used)} · pending {String(b.pending)}</div></div>
        ))}
      </div>
      <div className="oc-row-wrap">
        <button className="oc-btn oc-btn-ink" style={{ minHeight: 48 }} onClick={() => setLeave(true)}>Request leave</button>
        <button className="oc-btn oc-btn-neutral" style={{ minHeight: 48 }} onClick={() => setPerm(true)}>Request permission</button>
      </div>
      <Card title="My leave" icon="beach_access">
        <DataTable rows={reqs.data?.items} loading={reqs.isLoading} error={reqs.error} columns={[
          { key: 'leaveType', header: 'Type', render: (r) => label(r.leaveType) }, { key: 'startDate', header: 'Period', render: (r) => `${date(r.startDate)} – ${date(r.endDate)}` },
          { key: 'days', header: 'Days', align: 'right' }, { key: 'status', header: 'Status', render: pill('status') },
          { key: 'approvals', header: 'Approval', render: (r) => <Approvals steps={r.approvals} /> },
        ]} actions={(r) => ((r.status === 'submitted' || (r.status === 'approved' && String(r.startDate).slice(0, 10) > today()))
          ? <Act label={r.status === 'submitted' ? 'Withdraw' : 'Cancel'} path={`${ESS}/leave-requests/${r.id}:cancel`} kind="text" /> : null)}
          empty={<p className="oc-muted">No leave requests.</p>} />
      </Card>
      <Card title="My permission" icon="schedule">
        <DataTable rows={perms.data?.items} loading={perms.isLoading} error={perms.error} columns={[
          { key: 'permissionType', header: 'Type', render: (r) => label(r.permissionType) }, { key: 'workDate', header: 'Day', render: (r) => `${date(r.workDate)} ${time(r.startsAt)}–${time(r.endsAt)}` },
          { key: 'status', header: 'Status', render: pill('status') },
        ]} actions={(r) => (r.status === 'submitted' ? <Act label="Withdraw" path={`${ESS}/permission-requests/${r.id}:cancel`} kind="text" /> : null)}
          empty={<p className="oc-muted">No permission requests.</p>} />
      </Card>
      {leave && <LeaveForm path={`${ESS}/leave-requests`} types={types.data?.items ?? []} onClose={() => setLeave(false)} />}
      {perm && <PermissionForm path={`${ESS}/permission-requests`} types={types.data?.items ?? []} onClose={() => setPerm(false)} />}
    </div>
  );
}

function EssOvertime({ base }: { base: string }) {
  const l = useGet<Page<R>>(`${ESS}/overtime-requests`);
  const [open, setOpen] = useState(false);
  return (
    <div className="oc-stack">
      <EssBack base={base} title="Overtime" />
      <button className="oc-btn oc-btn-ink" style={{ minHeight: 48, width: 'fit-content' }} onClick={() => setOpen(true)}>Request overtime</button>
      <DataTable rows={l.data?.items} loading={l.isLoading} error={l.error} columns={[...overtimeColumns.filter((c) => c.key !== 'employeeName' && c.key !== 'number'),
        { key: 'approvals', header: 'Approval', render: (r: R) => <Approvals steps={r.approvals} /> }]}
        actions={(r) => (r.status === 'submitted' ? <Act label="Withdraw" path={`${ESS}/overtime-requests/${r.id}:cancel`} kind="text" /> : null)}
        empty={<p className="oc-muted">No overtime yet.</p>} />
      {open && <OvertimeForm path={`${ESS}/overtime-requests`} onClose={() => setOpen(false)} />}
    </div>
  );
}

function EssApprovals({ base }: { base: string }) {
  const l = useGet<Page<R>>(`${ESS}/approvals`);
  return (
    <div className="oc-stack">
      <EssBack base={base} title="Approvals" />
      <DataTable rows={withIds(l.data?.items, (r) => `${String(r.kind)}-${String(r.requestId)}`)} loading={l.isLoading} error={l.error} columns={[
        { key: 'kind', header: 'Request', render: (r) => `${label(r.kind)} ${String(r.number)}` }, { key: 'employeeName', header: 'Employee' },
        { key: 'summary', header: 'Details' }, { key: 'submittedAt', header: 'Since', render: (r) => formatDateTime(String(r.submittedAt)) },
      ]} actions={(r) => <Decide base={`${ESS}/approvals/${String(r.kind)}/${String(r.requestId)}`} />}
        empty={<Empty title="Nothing waiting for you" icon="task_alt" />} />
    </div>
  );
}

function EssTeamSchedule({ base }: { base: string }) {
  const [from, setFrom] = useState(monday(today()));
  const s = useGet<R>(`${ESS}/team-schedule?from=${from}&to=${addDays(from, 6)}`);
  const cal = useGet<Page<R>>(`${ESS}/team-calendar?from=${from}&to=${addDays(from, 30)}`);
  const days = useMemo(() => Array.from({ length: 7 }, (_, i) => addDays(from, i)), [from]);
  const d = s.data;
  return (
    <div className="oc-stack">
      <EssBack base={base} title="Team Schedule" />
      <div className="oc-row-wrap">
        <button className="oc-btn oc-btn-text" onClick={() => setFrom(addDays(from, -7))} aria-label="Previous week"><Icon name="chevron_left" size={22} /></button>
        <strong>{formatDate(from)} – {formatDate(addDays(from, 6))}</strong>
        <button className="oc-btn oc-btn-text" onClick={() => setFrom(addDays(from, 7))} aria-label="Next week"><Icon name="chevron_right" size={22} /></button>
      </div>
      {s.isLoading ? <Skeleton rows={6} /> : s.error ? <ErrorAlert error={s.error} /> : d && (
        <div className="oc-card" style={{ overflowX: 'auto' }}>
          <table className="oc-table" aria-label="Team schedule">
            <thead><tr><th>Employee</th>{days.map((x) => <th key={x}>{formatDate(x)}</th>)}</tr></thead>
            <tbody>
              {((d.colleagues as R[]) ?? []).map((c) => (
                <tr key={String(c.id)}>
                  <th scope="row">{String(c.fullName)}</th>
                  {days.map((x) => {
                    const a = ((d.assignments as R[]) ?? []).find((y) => y.employeeId === c.id && String(y.workDate).slice(0, 10) === x);
                    const l = ((d.leave as R[]) ?? []).find((y) => y.employeeId === c.id && String(y.startDate).slice(0, 10) <= x && String(y.endDate).slice(0, 10) >= x);
                    return <td key={x}>{a ? shiftText(a) : '—'}{l && <div className="oc-small"><StatusPill status={l.status === 'approved' ? 'on_leave' : 'pending'} label="leave" /></div>}</td>;
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <Card title="Leave calendar" icon="beach_access">
        <DataTable rows={withIds(cal.data?.items, (r) => String(r.requestId))} loading={cal.isLoading} error={cal.error} columns={[
          { key: 'employeeName', header: 'Employee' }, { key: 'leaveType', header: 'Type', render: (r) => label(r.leaveType) },
          { key: 'startDate', header: 'Period', render: (r) => `${date(r.startDate)} – ${date(r.endDate)}` }, { key: 'status', header: 'Status', render: pill('status') },
          { key: 'scheduleConflicts', header: 'Shifts', render: (r) => (Number(r.scheduleConflicts) ? <StatusPill status="pending" label={`${String(r.scheduleConflicts)} to cover`} /> : '—') },
        ]} empty={<p className="oc-muted">Nobody is on leave.</p>} />
      </Card>
    </div>
  );
}

function EssTeamAttendance({ base }: { base: string }) {
  const [day, setDay] = useState(today());
  const t = useGet<R>(`${ESS}/team-attendance?date=${day}`);
  const d = t.data;
  return (
    <div className="oc-stack">
      <EssBack base={base} title="Team Attendance" />
      <TextField label="Day" type="date" value={day} onChange={setDay} />
      {t.isLoading ? <Skeleton rows={6} /> : t.error ? <ErrorAlert error={t.error} /> : d && (
        <>
          <Card title="Clock-ins to review" icon="location_off">
            <DataTable rows={(d.pendingReview as R[]) ?? []} columns={eventColumns} empty={<p className="oc-muted">Nothing to review.</p>} actions={(r) => (
              <div className="oc-row" style={{ gap: 6 }}>
                <Act label="Accept" path={`${ESS}/attendance-events/${r.id}:review`} body={{ decision: 'accepted' }} note="optional" kind="ink" />
                <Act label="Reject" path={`${ESS}/attendance-events/${r.id}:review`} body={{ decision: 'rejected' }} note="required" kind="text" />
              </div>
            )} />
          </Card>
          <Card title={formatDate(day)} icon="how_to_reg">
            <DataTable rows={withIds(d.days, (r) => String(r.employeeId))} columns={dayColumns.filter((c) => c.key !== 'workDate')} empty={<p className="oc-muted">No shifts.</p>} />
          </Card>
          <Card title="Overtime without approval" icon="more_time">
            <DataTable rows={withIds(d.exceptions, (r) => `${String(r.employeeId)}-${String(r.workDate)}`)} columns={[
              { key: 'workDate', header: 'Day', render: (r) => date(r.workDate) }, { key: 'employeeName', header: 'Employee' },
              { key: 'overtimeMinutes', header: 'Overtime', render: (r) => hours(r.overtimeMinutes) }, { key: 'approvedHours', header: 'Approved (h)', align: 'right' },
            ]} empty={<p className="oc-muted">None.</p>} />
          </Card>
        </>
      )}
    </div>
  );
}

registerEssSection('schedule', ({ base }) => <EssSchedule base={base} />);
registerEssSection('clock', ({ base }) => <EssClock base={base} />);
registerEssSection('attendance', ({ base }) => <EssAttendance base={base} />);
registerEssSection('leave', ({ base }) => <EssLeave base={base} />);
registerEssSection('overtime', ({ base }) => <EssOvertime base={base} />);
registerEssSection('approvals', ({ base }) => <EssApprovals base={base} />);
registerEssSection('team-schedule', ({ base }) => <EssTeamSchedule base={base} />);
registerEssSection('team-attendance', ({ base }) => <EssTeamAttendance base={base} />);

// ── registrations ─────────────────────────────────────────────────────────

export const HR_TIME_ROUTES: AreaRoute[] = [
  { path: 'hris/schedules', perm: 'hris.schedule.view', element: <SchedulesPage /> },
  { path: 'hris/schedules/:id', perm: 'hris.schedule.view', element: <ScheduleDetailPage /> },
  { path: 'hris/attendance', perm: 'hris.attendance.view', element: <AttendancePage /> },
  { path: 'hris/leave', perm: 'hris.leave_request.view', element: <LeavePage /> },
  { path: 'hris/overtime', perm: 'hris.overtime_request.view', element: <OvertimePage /> },
  ...WORKFORCE_ROUTES, // HRIS phase B (spec §8)
];

export const HR_TIME_OPS_TILES: OpsTile[] = [['qr_code_scanner', 'Attendance Kiosk', '/ops/attendance-kiosk', 'hris.attendance.kiosk']];

export const HR_TIME_OPS_ROUTES: OpsRoute[] = [{ path: 'attendance-kiosk', element: <KioskPage /> }];
