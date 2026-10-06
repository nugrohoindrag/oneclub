import React, { useState } from 'react';
import { Link } from 'react-router';
import { uuidv7, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate } from '@oneclub/i18n';
import {
  DataTable, Empty, ErrorAlert, FilterPills, Icon, Modal, PageHeader, Skeleton, StatusPill, TextArea, TextField, useAuth, useToast,
} from '@oneclub/shell';
import { KV, today, type R } from '../p1/common';
import type { AreaRoute } from '../p3/types';
import { registerEssSection } from './hr';

// HRIS improvement phase C (spec §16 Timesheets): the employee records the time per day and activity of a period in Employee Self Service
// and submits it; the supervisor approves it in ESS → Approvals (then the approval workflow); HR sees the timesheets next to the attendance
// of the same days and decides at the HR level. A rejected timesheet is corrected and resubmitted.

const HR = '/api/v1/hris';
const ESS = '/api/v1/ess';
const INV = [HR, ESS];
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const date = (v: unknown) => (v ? formatDate(String(v).slice(0, 10)) : '—');
const addDays = (d: string, n: number) => {
  const x = new Date(`${d}T00:00:00Z`);
  x.setUTCDate(x.getUTCDate() + n);
  return x.toISOString().slice(0, 10);
};
const LABELS: Record<string, string> = { submitted: 'Waiting for approval' };
const pill = (r: R) => <StatusPill status={String(r.status)} label={LABELS[String(r.status)] ?? label(r.status)} />;

/** Detail of a timesheet: entries, the attendance of the same days, the approval trail. */
function TimesheetDetail({ t }: { t: R }) {
  const entries = ((t.entries as R[]) ?? []).map((e, i) => ({ ...e, id: String(i) } as R));
  const days = ((t.days as R[]) ?? []).map((d, i) => ({ ...d, id: String(i) } as R));
  const steps = ((t.steps as R[]) ?? []).map((s, i) => ({ ...s, id: String(i) } as R));
  return (
    <div className="oc-stack">
      <KV items={[['Employee', `${String(t.employeeName)} (${String(t.employeeNo)})`], ['Period', `${date(t.periodStart)} – ${date(t.periodEnd)}`],
        ['Total', `${String(t.totalHours)} hours`], ['Status', LABELS[String(t.status)] ?? label(t.status)], ['Decision', String(t.decisionNote ?? '—')],
        ['Notes', String(t.notes ?? '—')]]} />
      <DataTable rows={entries} empty={<Empty title="No entry" />} columns={[
        { key: 'workDate', header: 'Date', render: (e) => date(e.workDate) }, { key: 'activity', header: 'Activity' },
        { key: 'reference', header: 'Reference', render: (e) => String(e.reference ?? '—') }, { key: 'hours', header: 'Hours', align: 'right' },
      ]} />
      <h3 style={{ margin: 0 }}>Against attendance</h3>
      <DataTable rows={days.filter((d) => Number(d.hours) > 0 || Number(d.attendanceHours) > 0)} empty={<Empty title="No time recorded" />} columns={[
        { key: 'date', header: 'Date', render: (d) => date(d.date) }, { key: 'hours', header: 'Timesheet', align: 'right' },
        { key: 'attendanceHours', header: 'Attendance', align: 'right' },
        { key: 'diff', header: 'Difference', align: 'right', render: (d) => {
          const x = Number(d.hours) - Number(d.attendanceHours);
          return Math.abs(x) >= 1 ? <StatusPill status="warning" label={x.toFixed(1)} /> : x.toFixed(1);
        } },
      ]} />
      {steps.length > 0 && (
        <DataTable rows={steps} columns={[
          { key: 'level', header: 'Approval', render: (s) => label(s.level) }, { key: 'approverName', header: 'Approver', render: (s) => String(s.approverName ?? 'HR') },
          { key: 'status', header: 'Status', render: (s) => <StatusPill status={String(s.status)} label={label(s.status)} /> },
          { key: 'note', header: 'Note', render: (s) => String(s.note ?? '—') },
        ]} />
      )}
    </div>
  );
}

function Decide({ id, approve, onClose }: { id: string; approve: boolean; onClose: () => void }) {
  const toast = useToast();
  const [note, setNote] = useState('');
  const send = useSend<Record<string, unknown>, R>('POST', `${HR}/timesheets/${id}:${approve ? 'approve' : 'reject'}`, INV);
  const title = approve ? 'Approve timesheet' : 'Reject timesheet';
  return (
    <Modal open onClose={onClose} title={title} actions={<>
      <button className="oc-btn oc-btn-text" onClick={onClose}>Cancel</button>
      <button className={`oc-btn ${approve ? 'oc-btn-ink' : 'oc-btn-danger'}`} disabled={send.isPending || (!approve && !note.trim())}
        onClick={() => send.mutate(note ? { note } : {}, { onSuccess: () => { toast(`${title}: done`); onClose(); } })}>{approve ? 'Approve' : 'Reject'}</button>
    </>}>
      <ErrorAlert error={send.error} />
      <TextArea label={approve ? 'Note (optional)' : 'Reason'} value={note} onChange={setNote} required={!approve} />
    </Modal>
  );
}

export function TimesheetsPage() {
  const { can } = useAuth();
  const [status, setStatus] = useState('submitted');
  const [open, setOpen] = useState('');
  const [decide, setDecide] = useState<boolean | null>(null);
  const list = useGet<Page<R>>(`${HR}/timesheets${status ? `?status=${status}` : ''}`);
  const one = useGet<R>(open ? `${HR}/timesheets/${open}` : null);
  return (
    <div className="oc-stack">
      <PageHeader title="Timesheets" help="Actual working time per day and activity recorded by employees in Employee Self Service, approved by their supervisor; compared with attendance. Timesheets record time, not productivity." />
      <FilterPills options={[{ value: 'submitted', label: 'Waiting for approval' }, { value: 'approved', label: 'Approved' }, { value: 'rejected', label: 'Rejected' },
        { value: 'draft', label: 'Draft' }, { value: '', label: 'All' }]} value={status} onChange={setStatus} />
      <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error} onRowClick={(r) => setOpen(String(r.id))}
        empty={<Empty title="No timesheet" help="Employees record their time in Employee Self Service → Timesheet." icon="schedule" />}
        columns={[
          { key: 'number', header: 'Timesheet' }, { key: 'employeeName', header: 'Employee' }, { key: 'orgUnitName', header: 'Department', render: (r) => String(r.orgUnitName ?? '—') },
          { key: 'period', header: 'Period', render: (r) => `${date(r.periodStart)} – ${date(r.periodEnd)}` },
          { key: 'totalHours', header: 'Hours', align: 'right' }, { key: 'status', header: 'Status', render: pill },
        ]} />
      {open && (
        <Modal open wide onClose={() => setOpen('')} title={one.data ? String(one.data.number) : 'Timesheet'} actions={one.data?.status === 'submitted' && can('hris.timesheet.approve') ? <>
          <button className="oc-btn oc-btn-danger" onClick={() => setDecide(false)}>Reject</button>
          <button className="oc-btn oc-btn-ink" onClick={() => setDecide(true)}>Approve</button>
        </> : undefined}>
          {one.isLoading && <Skeleton rows={5} />}
          <ErrorAlert error={one.error} />
          {one.data && <TimesheetDetail t={one.data} />}
        </Modal>
      )}
      {decide !== null && open && <Decide id={open} approve={decide} onClose={() => { setDecide(null); void one.refetch(); }} />}
    </div>
  );
}

// ── Employee Self Service → Timesheet ─────────────────────────────────────

type Entry = { workDate: string; hours: string; activity: string; reference: string };

/** Week (or period) editor: rows of date, hours, activity, reference. */
function TimesheetForm({ t, onClose }: { t?: R; onClose: () => void }) {
  const toast = useToast();
  const monday = (() => {
    const d = new Date(`${today()}T00:00:00Z`);
    d.setUTCDate(d.getUTCDate() - ((d.getUTCDay() + 6) % 7));
    return d.toISOString().slice(0, 10);
  })();
  const [from, setFrom] = useState(t ? String(t.periodStart).slice(0, 10) : monday);
  const [to, setTo] = useState(t ? String(t.periodEnd).slice(0, 10) : addDays(monday, 6));
  const [notes, setNotes] = useState(String(t?.notes ?? ''));
  const [rows, setRows] = useState<Entry[]>(t ? ((t.entries as R[]) ?? []).map((e) => ({ workDate: String(e.workDate).slice(0, 10), hours: String(e.hours),
    activity: String(e.activity), reference: String(e.reference ?? '') })) : [{ workDate: monday, hours: '8', activity: '', reference: '' }]);
  const send = useSend<Record<string, unknown>, R>(t ? 'PATCH' : 'POST', t ? `${ESS}/timesheets/${String(t.id)}` : `${ESS}/timesheets`, INV,
    t ? undefined : () => ({ 'Idempotency-Key': uuidv7() }));
  const set = (i: number, k: keyof Entry, v: string) => setRows(rows.map((r, j) => (j === i ? { ...r, [k]: v } : r)));
  const save = (submit: boolean) => send.mutate({ ...(t ? {} : { periodStart: from, periodEnd: to }), notes, submit,
    entries: rows.filter((r) => r.activity.trim() || Number(r.hours) > 0).map((r) => ({ workDate: r.workDate, hours: r.hours, activity: r.activity,
      reference: r.reference || undefined })) }, { onSuccess: () => { toast(submit ? 'Timesheet submitted' : 'Timesheet saved'); onClose(); } });
  const total = rows.reduce((s, r) => s + (Number(r.hours) || 0), 0);
  return (
    <Modal open wide onClose={onClose} title={t ? `Timesheet ${String(t.number)}` : 'New timesheet'} actions={<>
      <button className="oc-btn oc-btn-text" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-neutral" disabled={send.isPending} onClick={() => save(false)}>Save draft</button>
      <button className="oc-btn oc-btn-ink" disabled={send.isPending} onClick={() => save(true)}>Submit</button>
    </>}>
      <div className="oc-stack">
        <ErrorAlert error={send.error} />
        {!t && (
          <div className="oc-row-wrap">
            <TextField label="From" type="date" value={from} onChange={setFrom} />
            <TextField label="To (at most 31 days)" type="date" value={to} onChange={setTo} />
          </div>
        )}
        {rows.map((r, i) => (
          <div key={i} className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
            <TextField label="Date" type="date" value={r.workDate} onChange={(v) => set(i, 'workDate', v)} />
            <TextField label="Hours" inputMode="decimal" value={r.hours} onChange={(v) => set(i, 'hours', v)} />
            <TextField label="Activity" value={r.activity} onChange={(v) => set(i, 'activity', v)} />
            <TextField label="Event / work order" value={r.reference} onChange={(v) => set(i, 'reference', v)} />
            <button className="oc-btn oc-btn-text" style={{ minHeight: 44 }} aria-label="Remove row" onClick={() => setRows(rows.filter((_, j) => j !== i))}><Icon name="delete" size={20} /></button>
          </div>
        ))}
        <div className="oc-row-wrap">
          <button className="oc-btn oc-btn-neutral" style={{ minHeight: 44 }} onClick={() => setRows([...rows, { workDate: rows.length ? rows[rows.length - 1].workDate : from,
            hours: '', activity: '', reference: '' }])}><Icon name="add" size={18} /> Add row</button>
          <span className="oc-spacer" />
          <strong>{total.toFixed(1)} hours</strong>
        </div>
        <TextArea label="Notes" value={notes} onChange={setNotes} />
      </div>
    </Modal>
  );
}

function EssTimesheets({ base }: { base: string }) {
  const toast = useToast();
  const [edit, setEdit] = useState<R | null | undefined>(undefined);
  const [view, setView] = useState('');
  const list = useGet<Page<R>>(`${ESS}/timesheets`);
  const one = useGet<R>(view ? `${ESS}/timesheets/${view}` : null);
  const submit = useSend<Record<string, unknown>, R>('POST', () => `${ESS}/timesheets/${view}:submit`, INV);
  const withdraw = useSend<Record<string, unknown>, R>('POST', () => `${ESS}/timesheets/${view}:withdraw`, INV);
  const items = list.data?.items ?? [];
  const t = one.data;
  return (
    <div className="oc-stack">
      <div className="oc-row" style={{ gap: 8 }}>
        <Link className="oc-btn oc-btn-text" to={base} aria-label="Back to Employee Self Service"><Icon name="arrow_back" size={22} /></Link>
        <h1 style={{ margin: 0, fontSize: 24 }}>Timesheet</h1>
        <span className="oc-spacer" />
        <button className="oc-btn oc-btn-ink" style={{ minHeight: 44 }} onClick={() => setEdit(null)}><Icon name="add" size={18} /> New</button>
      </div>
      {!list.isLoading && !list.error && items.length === 0 && <Empty title="No timesheet yet" help="Record your hours per day and activity, then submit them to your supervisor." icon="schedule" />}
      {items.length > 0 && (
        <DataTable rows={items} loading={list.isLoading} error={list.error} onRowClick={(r) => setView(String(r.id))} columns={[
          { key: 'period', header: 'Period', render: (r) => `${date(r.periodStart)} – ${date(r.periodEnd)}` },
          { key: 'totalHours', header: 'Hours', align: 'right' }, { key: 'status', header: 'Status', render: pill },
        ]} />
      )}
      {edit !== undefined && <TimesheetForm t={edit ?? undefined} onClose={() => { setEdit(undefined); void one.refetch(); }} />}
      {view && edit === undefined && (
        <Modal open wide onClose={() => setView('')} title={t ? String(t.number) : 'Timesheet'} actions={t ? <>
          {(t.status === 'draft' || t.status === 'rejected') && <button className="oc-btn oc-btn-neutral" onClick={() => setEdit(t)}>Edit</button>}
          {(t.status === 'draft' || t.status === 'rejected') && <button className="oc-btn oc-btn-ink" disabled={submit.isPending}
            onClick={() => submit.mutate({}, { onSuccess: () => { toast('Timesheet submitted'); void one.refetch(); } })}>Submit</button>}
          {t.status === 'submitted' && <button className="oc-btn oc-btn-outline" disabled={withdraw.isPending}
            onClick={() => withdraw.mutate({}, { onSuccess: () => { toast('Back to draft'); void one.refetch(); } })}>Withdraw to edit</button>}
        </> : undefined}>
          {one.isLoading && <Skeleton rows={5} />}
          <ErrorAlert error={one.error ?? submit.error ?? withdraw.error} />
          {t && <TimesheetDetail t={t} />}
        </Modal>
      )}
    </div>
  );
}

registerEssSection('timesheets', ({ base }) => <EssTimesheets base={base} />);

export const TIMESHEET_ROUTES: AreaRoute[] = [{ path: 'hris/timesheets', perm: 'hris.timesheet.view', element: <TimesheetsPage /> }];

