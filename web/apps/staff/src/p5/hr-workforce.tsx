import React, { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router';
import { download, uuidv7, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate } from '@oneclub/i18n';
import {
  Card, Checkbox, DataTable, Empty, ErrorAlert, FilterPills, Icon, Modal, PageHeader, SelectField, Skeleton, StatTile, StatusPill, TextArea, TextField,
  useAuth, useToast, type Option,
} from '@oneclub/shell';
import { ActionButton, KV, today, type R } from '../p1/common';
import type { AreaRoute } from '../p3/types';

// HRIS improvement phase B (spec §8 Workforce Planning): HRIS → Time & Attendance → Workforce Planning. A plan states the headcount a
// department needs per position and shift for a period and scenario (weekend, holiday, peak season, tournament, event); the gap review
// compares every day with the available (active, not on leave or suspended) and scheduled staff. Staff are assigned in the department's
// Shift & Roster; the planning result exports as CSV.

const HR = '/api/v1/hris';
const API = `${HR}/workforce-plans`;
const INV = [HR];
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const date = (v: unknown) => (v ? formatDate(String(v).slice(0, 10)) : '—');
const clean = (o: Record<string, unknown>) => Object.fromEntries(Object.entries(o).filter(([, v]) => v !== '' && v !== undefined && v !== null));
const SCENARIOS: Option[] = ['normal', 'weekend', 'holiday', 'peak_season', 'low_season', 'tournament', 'event'].map((v) => ({ value: v, label: label(v) }));
const addDays = (d: string, n: number) => {
  const x = new Date(`${d}T00:00:00Z`);
  x.setUTCDate(x.getUTCDate() + n);
  return x.toISOString().slice(0, 10);
};

function useOptions(path: string, text: (r: R) => string): Option[] {
  const l = useGet<Page<R>>(path);
  return (l.data?.items ?? []).map((x) => ({ value: String(x.id), label: text(x) }));
}

type Line = { positionId: string; shiftTemplateId: string; workDate: string; required: string; notes: string };
const emptyLine = (): Line => ({ positionId: '', shiftTemplateId: '', workDate: '', required: '1', notes: '' });

/** Requirement lines editor: position, shift, date (empty = every day), required headcount. */
function LinesEditor({ lines, setLines, positions, shifts }: { lines: Line[]; setLines: (l: Line[]) => void; positions: Option[]; shifts: Option[] }) {
  const set = (i: number, k: keyof Line, v: string) => setLines(lines.map((l, j) => (j === i ? { ...l, [k]: v } : l)));
  return (
    <div className="oc-stack" style={{ gridColumn: '1 / -1' }}>
      {lines.map((l, i) => (
        <div key={i} className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
          <SelectField label="Position" value={l.positionId} onChange={(v) => set(i, 'positionId', v)} options={positions} placeholder="Any" />
          <SelectField label="Shift" value={l.shiftTemplateId} onChange={(v) => set(i, 'shiftTemplateId', v)} options={shifts} placeholder="Any" />
          <TextField label="Date (empty = every day)" type="date" value={l.workDate} onChange={(v) => set(i, 'workDate', v)} />
          <TextField label="Required" inputMode="numeric" value={l.required} onChange={(v) => set(i, 'required', v)} required />
          <TextField label="Note" value={l.notes} onChange={(v) => set(i, 'notes', v)} />
          <button className="oc-btn oc-btn-text oc-btn-sm" aria-label="Remove line" onClick={() => setLines(lines.filter((_, j) => j !== i))}><Icon name="delete" size={18} /></button>
        </div>
      ))}
      <div><button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setLines([...lines, emptyLine()])}><Icon name="add" size={18} /> Add requirement</button></div>
    </div>
  );
}

const linesBody = (lines: Line[]) => lines.map((l) => clean({ positionId: l.positionId, shiftTemplateId: l.shiftTemplateId, workDate: l.workDate,
  required: Number(l.required || 0), notes: l.notes }));

/** Create (or adjust) a plan. */
function PlanForm({ plan, onClose, onDone }: { plan?: R; onClose: () => void; onDone?: (r: R) => void }) {
  const toast = useToast();
  const units = useOptions(`${HR}/org-units?limit=500&filter[status]=active`, (x) => `${String(x.name)} (${String(x.code)})`);
  const positions = useOptions(`${HR}/positions?limit=500&filter[status]=active`, (x) => String(x.name));
  const shifts = useOptions(`${HR}/shift-templates?limit=200&filter[status]=active`, (x) => `${String(x.name)} ${String(x.startTime ?? '').slice(0, 5)}–${String(x.endTime ?? '').slice(0, 5)}`);
  const [f, setF] = useState<Record<string, string>>(plan ? {
    name: String(plan.name), periodStart: String(plan.periodStart).slice(0, 10), periodEnd: String(plan.periodEnd).slice(0, 10), scenario: String(plan.scenario),
    notes: String(plan.notes ?? ''),
  } : { periodStart: today(), periodEnd: addDays(today(), 6), scenario: 'normal' });
  const [prefill, setPrefill] = useState(!plan);
  const [lines, setLines] = useState<Line[]>(plan ? ((plan.lines as R[] | undefined) ?? []).map((l) => ({
    positionId: String(l.positionId ?? ''), shiftTemplateId: String(l.shiftTemplateId ?? ''), workDate: l.workDate ? String(l.workDate).slice(0, 10) : '',
    required: String(l.required), notes: String(l.notes ?? ''),
  })) : []);
  const send = useSend<Record<string, unknown>, R>(plan ? 'PATCH' : 'POST', plan ? `${API}/${String(plan.id)}` : API, INV,
    plan ? undefined : () => ({ 'Idempotency-Key': uuidv7() }));
  const set = (k: string) => (v: string) => setF({ ...f, [k]: v });
  const body = () => clean({ ...f, orgUnitId: plan ? undefined : f.orgUnitId, lines: lines.length || plan ? linesBody(lines) : undefined,
    fromRequirements: !plan && prefill && lines.length === 0 ? true : undefined });
  return (
    <Modal open wide onClose={onClose} title={plan ? `Adjust ${String(plan.number)}` : 'New workforce plan'} actions={
      <>
        <button className="oc-btn oc-btn-text" onClick={onClose}>Cancel</button>
        <button className="oc-btn oc-btn-ink" disabled={send.isPending} onClick={() => send.mutate(body(), {
          onSuccess: (r) => { toast('Workforce plan saved'); onClose(); onDone?.(r); },
        })}>Save</button>
      </>
    }>
      <div className="oc-stack">
        <ErrorAlert error={send.error} />
        <div className="oc-form">
          {!plan && <SelectField label="Department" value={f.orgUnitId ?? ''} onChange={set('orgUnitId')} options={units} required />}
          <TextField label="Name (empty = department and month)" value={f.name ?? ''} onChange={set('name')} />
          <TextField label="From" type="date" value={f.periodStart} onChange={set('periodStart')} required />
          <TextField label="To (at most 93 days)" type="date" value={f.periodEnd} onChange={set('periodEnd')} required />
          <SelectField label="Scenario" value={f.scenario} onChange={set('scenario')} options={SCENARIOS} />
          <TextArea label="Notes" value={f.notes ?? ''} onChange={set('notes')} span />
          {!plan && lines.length === 0 && (
            <Checkbox label="Start from the staffing requirements of the department" checked={prefill} onChange={setPrefill} />
          )}
          <LinesEditor lines={lines} setLines={setLines} positions={positions} shifts={shifts} />
        </div>
      </div>
    </Modal>
  );
}

export function WorkforcePlansPage() {
  const nav = useNavigate();
  const { can } = useAuth();
  const [status, setStatus] = useState('');
  const [open, setOpen] = useState(false);
  const list = useGet<Page<R>>(`${API}${status ? `?status=${status}` : ''}`);
  return (
    <div className="oc-stack">
      <PageHeader title="Workforce Planning" help="Headcount each department needs per position and shift for a period and scenario (weekend, holiday, peak season, tournament, event), compared with the available and scheduled staff."
        actions={can('hris.workforce_plan.manage') && <button className="oc-btn oc-btn-ink" onClick={() => setOpen(true)}><Icon name="add" size={18} /> New plan</button>} />
      <FilterPills options={[{ value: '', label: 'All' }, { value: 'draft', label: 'Draft' }, { value: 'active', label: 'Active' }, { value: 'archived', label: 'Archived' }]}
        value={status} onChange={setStatus} />
      <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error} onRowClick={(r) => nav(`/hris/workforce/${String(r.id)}`)}
        empty={<Empty title="No workforce plan" help="Plan the manpower of a department for a tournament, holiday or season and review the gap." icon="groups" />}
        columns={[
          { key: 'number', header: 'Plan' }, { key: 'name', header: 'Name' }, { key: 'orgUnitName', header: 'Department' },
          { key: 'period', header: 'Period', render: (r) => `${date(r.periodStart)} – ${date(r.periodEnd)}` },
          { key: 'scenario', header: 'Scenario', render: (r) => label(r.scenario) }, { key: 'lineCount', header: 'Requirements', align: 'right' },
          { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} label={label(r.status)} /> },
        ]} />
      {open && <PlanForm onClose={() => setOpen(false)} onDone={(r) => nav(`/hris/workforce/${String(r.id)}`)} />}
    </div>
  );
}

export function WorkforcePlanPage() {
  const { id = '' } = useParams();
  const nav = useNavigate();
  const { can } = useAuth();
  const toast = useToast();
  const [edit, setEdit] = useState(false);
  const [onlyGaps, setOnlyGaps] = useState(true);
  const gap = useGet<R>(`${API}/${id}/gap`);
  if (gap.isLoading) return <Skeleton rows={8} />;
  if (gap.error || !gap.data) return <ErrorAlert error={gap.error} />;
  const g = gap.data;
  const p = g.plan as R;
  const st = String(p.status);
  const manage = can('hris.workforce_plan.manage') && st !== 'archived';
  const rows = ((g.rows as R[]) ?? []).filter((x) => !onlyGaps || Number(x.gap) > 0 || Number(x.shortage) > 0)
    .map((x, i) => ({ ...x, id: `${String(x.date)}-${String(x.lineNo)}-${i}` } as R));
  const roster = `/hris/schedules?orgUnitId=${String(p.orgUnitId)}`;
  return (
    <div className="oc-stack">
      <div className="oc-row" style={{ gap: 8 }}>
        <Link className="oc-btn oc-btn-text" to="/hris/workforce" aria-label="Back to Workforce Planning"><Icon name="arrow_back" size={22} /></Link>
        <PageHeader title={`${String(p.number)} · ${String(p.name)}`} help={`${String(p.orgUnitName)} · ${date(p.periodStart)} – ${date(p.periodEnd)} · ${label(p.scenario)}`} />
      </div>
      <div className="oc-row-wrap">
        <StatusPill status={st} label={label(st)} />
        <span className="oc-spacer" />
        {manage && <button className="oc-btn oc-btn-neutral" onClick={() => setEdit(true)}><Icon name="tune" size={18} /> Adjust requirements</button>}
        {manage && st === 'draft' && <ActionButton label="Activate" path={`${API}/${id}:activate`} invalidate={INV} kind="ink" onDone={() => void gap.refetch()} />}
        {manage && <ActionButton label="Archive" path={`${API}/${id}:archive`} invalidate={INV} kind="text" confirm="Archive this plan? It can no longer be adjusted." onDone={() => void gap.refetch()} />}
        <button className="oc-btn oc-btn-neutral" onClick={() => download('GET', `${API}/${id}/export`, undefined, `${String(p.number)}.csv`).catch((e: Error) => toast(e.message, 'error'))}>
          <Icon name="download" size={18} /> Export
        </button>
        {can('hris.schedule.view') && <Link className="oc-btn oc-btn-ink" to={roster}><Icon name="calendar_month" size={18} /> Assign in Shift & Roster</Link>}
      </div>
      <div className="oc-grid">
        <StatTile label="Required (person-shifts)" icon="groups" value={Number(g.required)} />
        <StatTile label="Scheduled" icon="event_available" value={Number(g.scheduled)} />
        <StatTile label="Gap to assign" icon="person_search" value={Number(g.gap)} onOpen={can('hris.schedule.view') ? () => nav(roster) : undefined} />
        <StatTile label="Shortage (hire / cover)" icon="person_add" value={Number(g.shortage)} />
      </div>
      <Card title="Requirements" icon="checklist">
        {((p.lines as R[]) ?? []).length === 0 ? <Empty title="No requirement yet" help="Adjust the plan to add the headcount per position and shift." icon="checklist" /> : (
          <KV items={((p.lines as R[]) ?? []).map((l) => [`#${String(l.lineNo)} ${String(l.positionName ?? 'Any position')} · ${String(l.shiftName ?? 'any shift')}`,
            `${String(l.required)} ${l.workDate ? `on ${date(l.workDate)}` : 'every day'}${l.notes ? ` · ${String(l.notes)}` : ''}`])} />
        )}
      </Card>
      <Card title="Gap review" icon="query_stats" actions={
        <FilterPills options={[{ value: 'gaps', label: 'Days with a gap' }, { value: 'all', label: 'All days' }]} value={onlyGaps ? 'gaps' : 'all'} onChange={(v) => setOnlyGaps(v === 'gaps')} />
      }>
        <DataTable rows={rows} empty={<Empty title={onlyGaps ? 'No gap' : 'No requirement in the period'} help={onlyGaps ? 'Every day is fully scheduled.' : undefined} icon="task_alt" />}
          columns={[
            { key: 'date', header: 'Date', render: (x) => date(x.date) }, { key: 'positionName', header: 'Position', render: (x) => String(x.positionName ?? 'Any') },
            { key: 'shiftName', header: 'Shift', render: (x) => String(x.shiftName ?? 'Any') },
            { key: 'required', header: 'Required', align: 'right' }, { key: 'available', header: 'Available', align: 'right' },
            { key: 'scheduled', header: 'Scheduled', align: 'right' },
            { key: 'gap', header: 'Gap', align: 'right', render: (x) => (Number(x.gap) > 0 ? <strong>{String(x.gap)}</strong> : '0') },
            { key: 'shortage', header: 'Shortage', align: 'right', render: (x) => (Number(x.shortage) > 0 ? <StatusPill status="error" label={String(x.shortage)} /> : '0') },
          ]} />
      </Card>
      {edit && <PlanForm plan={p} onClose={() => setEdit(false)} onDone={() => void gap.refetch()} />}
    </div>
  );
}

export const WORKFORCE_ROUTES: AreaRoute[] = [
  { path: 'hris/workforce', perm: 'hris.workforce_plan.view', element: <WorkforcePlansPage /> },
  { path: 'hris/workforce/:id', perm: 'hris.workforce_plan.view', element: <WorkforcePlanPage /> },
];
