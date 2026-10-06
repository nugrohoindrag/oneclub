import React, { useState } from 'react';
import { useNavigate, useParams } from 'react-router';
import { download, uuidv7, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate } from '@oneclub/i18n';
import {
  AutoResourcePage, Card, DataTable, Empty, ErrorAlert, Icon, Modal, PageHeader, SelectField, Skeleton, StatusPill, TextArea, TextField, useAuth,
  useToast, type Option,
} from '@oneclub/shell';
import { ActionButton, KV, ListPage, Tabs, money, today, type R } from '../p1/common';
import type { AreaRoute, OpsRoute, OpsTile } from '../p3/types';
import { registerEssSection } from './hr';

// PRD P5 — payouts & distributions (EP-11–14): HRIS → Service Charge (distribution of the month's pool, 95/5 with attendance
// factor), Commissions (approved commission statements paid with payroll, bonus programmes), Caddy and Instructors (payout runs
// with PPh 21 non-employee, BPJS BPU, bank file and statements, partner profiles, approved settlements / fees). Partner views:
// Caddy App "Payout History" & "Statement" (tablet shell) and the instructor "Honor Statement" (ops). Employee Self Service
// sections Service Charge and Commission & Bonus. Routes are registered through p5/payroll.tsx.

const HR = '/api/v1/hris';
const ESS = '/api/v1/ess';
const INV = [HR, ESS];
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const date = (v: unknown) => (v ? formatDate(String(v).slice(0, 10)) : '—');
const pill = (k: string) => (r: R) => <StatusPill status={String(r[k] ?? '')} label={label(r[k])} />;
const opts = (vals: string[]): Option[] => vals.map((v) => ({ value: v, label: label(v) }));
const idem = () => ({ 'Idempotency-Key': uuidv7() });
const clean = (o: Record<string, unknown>) => Object.fromEntries(Object.entries(o).filter(([, v]) => v !== '' && v !== undefined && v !== null));
const rate = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : `${String(v)}%`);

/** A form in a modal that sends a body. */
function FormModal({ open, onClose, title, method = 'POST', path, body, children, submit = 'Save', onDone }: {
  open: boolean; onClose: () => void; title: string; method?: 'POST' | 'PUT'; path: string; body: () => Record<string, unknown>;
  children: React.ReactNode; submit?: string; onDone?: (r: R) => void;
}) {
  const toast = useToast();
  const send = useSend<Record<string, unknown>, R>(method, path, INV, method === 'POST' ? idem : undefined);
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

function useEmployees(): Option[] {
  const l = useGet<Page<R>>(`${HR}/employees?limit=500&filter[status]=active`);
  return (l.data?.items ?? []).map((x) => ({ value: x.id, label: `${String(x.fullName)} (${String(x.employeeNo)})` }));
}

/** Approve / reject / cancel actions of a document in an approval flow. */
function Decisions({ base, status, submittable, perm, cancelPerm }: { base: string; status: string; submittable: string[]; perm: string; cancelPerm: string }) {
  const { can } = useAuth();
  return (
    <>
      {can(perm) && (submittable.includes(status) || status === 'pending_approval') && (
        <ActionButton label="Approve" path={`${base}:approve`} invalidate={INV} kind="ink" />
      )}
      {can(perm) && status === 'pending_approval' && <ActionButton label="Reject" path={`${base}:reject`} invalidate={INV} reason="required" />}
      {can(cancelPerm) && [...submittable, 'draft', 'pending_approval'].includes(status) && (
        <ActionButton label="Cancel" path={`${base}:cancel`} invalidate={INV} reason="required" danger />
      )}
    </>
  );
}

// ── Service Charge (EP-11) ────────────────────────────────────────────────

const DIST_STATUSES = opts(['draft', 'simulated', 'pending_approval', 'approved', 'paid', 'cancelled']);

function lastMonth(): { year: string; month: string } {
  const d = new Date();
  d.setDate(1);
  d.setMonth(d.getMonth() - 1);
  return { year: String(d.getFullYear()), month: String(d.getMonth() + 1) };
}

export function ServiceChargePage() {
  const nav = useNavigate();
  const { can } = useAuth();
  const [open, setOpen] = useState(false);
  const lm = lastMonth();
  const [year, setYear] = useState(lm.year);
  const [month, setMonth] = useState(lm.month);
  const [payPeriod, setPayPeriod] = useState('');
  return (
    <>
      <ListPage title="Service Charge" path={`${HR}/service-charge-distributions`} search={false} statuses={DIST_STATUSES}
        help="Monthly distribution of the service charge pool (Accounting): 5% breakage & loss reserve, 95% split equally per eligible employee × attendance factor (Service Charge Policy). Approved lines are paid with payroll."
        actions={can('hris.service_charge.manage') && <button className="oc-btn oc-btn-ink" onClick={() => setOpen(true)}><Icon name="add" size={18} /> Distribute a month</button>}
        onRowClick={(r) => nav(`/hris/service-charge/${r.id}`)}
        columns={[
          { key: 'number', header: 'Distribution' },
          { key: 'month', header: 'Pool Month', render: (r) => `${String(r.year)}-${String(r.month).padStart(2, '0')}` },
          { key: 'payPeriod', header: 'Payroll' },
          { key: 'collected', header: 'Pool', align: 'right', render: (r) => money(r.collected) },
          { key: 'distributed', header: 'Distributed', align: 'right', render: (r) => money(r.distributed) },
          { key: 'eligible', header: 'Employees', align: 'right' },
          { key: 'status', header: 'Status', render: pill('status') },
        ]} />
      {open && (
        <FormModal open onClose={() => setOpen(false)} title="Distribute a month" path={`${HR}/service-charge-distributions`} submit="Create"
          body={() => clean({ year: Number(year), month: Number(month), payPeriod })} onDone={(r) => nav(`/hris/service-charge/${r.id}`)}>
          <TextField label="Year" type="number" value={year} onChange={setYear} required />
          <TextField label="Month" type="number" min={1} max={12} value={month} onChange={setMonth} required />
          <TextField label="Paid with the payroll of (YYYY-MM)" value={payPeriod} onChange={setPayPeriod} help="Default: the next month" />
        </FormModal>
      )}
    </>
  );
}

export function ServiceChargeDetailPage() {
  const { id } = useParams();
  const { can } = useAuth();
  const d = useGet<R>(`${HR}/service-charge-distributions/${id}`);
  if (d.isLoading) return <Skeleton rows={8} />;
  if (!d.data) return <ErrorAlert error={d.error} />;
  const x = d.data;
  const base = `${HR}/service-charge-distributions/${id}`;
  const status = String(x.status);
  const lines = (x.lines as R[] | undefined) ?? [];
  return (
    <div className="oc-stack">
      <PageHeader title={`Service Charge ${String(x.year)}-${String(x.month).padStart(2, '0')}`} help={String(x.number)} actions={
        <div className="oc-row" style={{ gap: 6 }}>
          {can('hris.service_charge.manage') && ['draft', 'simulated'].includes(status) && (
            <ActionButton label="Simulate" path={`${base}:simulate`} invalidate={INV} kind="primary" />
          )}
          <Decisions base={base} status={status} submittable={['simulated']} perm="hris.service_charge.approve" cancelPerm="hris.service_charge.manage" />
        </div>
      } />
      <Card title="Pool & distribution" icon="room_service">
        <KV items={[
          ['Status', <StatusPill key="s" status={status} label={label(status)} />],
          ['Pool (Accounting)', <span key="p">{money(x.collected)} · {label(x.poolStatus)}</span>],
          ['Reserve (breakage & loss)', `${money(x.reserve)} (${rate(x.reservePercent)})`],
          ['Distributable', `${money(x.distributable)} (${rate(x.distributedPercent)})`],
          ['Distributed', money(x.distributed)],
          ['Undistributed', money(x.undistributed)],
          ['Rounding difference', `${money(x.roundingDifference)}${x.roundingAccountCode ? ` → ${String(x.roundingAccountCode)}` : ''}`],
          ['Rule', `${label(x.method)} · attendance factor ${x.attendanceFactor ? 'on' : 'off'} · redistribution ${x.redistribute ? 'on' : 'off'} · policy v${String(x.policyVersion)}`],
          ['Employees', `${String(x.eligible)} eligible, ${String(x.excluded)} not eligible`],
          ['Paid with payroll', String(x.payPeriod)],
          ['Decision note', String(x.decisionNote ?? '—')],
        ]} />
      </Card>
      <DataTable rows={lines} empty={<Empty title="Not simulated yet" help="Simulate to see the amount (or the reason) per employee." />} columns={[
        { key: 'fullName', header: 'Employee', render: (r) => `${String(r.fullName)} (${String(r.employeeNo)})` },
        { key: 'orgUnitName', header: 'Org Unit' },
        { key: 'employmentStatus', header: 'Status', render: (r) => label(r.employmentStatus) },
        { key: 'eligible', header: 'Eligible', render: (r) => (r.eligible ? 'Yes' : label(r.exclusionReason)) },
        { key: 'presentDays', header: 'Present / Scheduled', render: (r) => `${String(r.presentDays)} / ${String(r.scheduledDays)}` },
        { key: 'attendanceFactor', header: 'Factor', align: 'right' },
        { key: 'baseShare', header: 'Base Share', align: 'right', render: (r) => money(r.baseShare) },
        { key: 'attendanceAmount', header: 'After Attendance', align: 'right', render: (r) => money(r.attendanceAmount) },
        { key: 'redistributed', header: 'Redistributed', align: 'right', render: (r) => money(r.redistributed) },
        { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) },
        { key: 'consumedAt', header: 'Paid', render: (r) => (r.consumedAt ? date(r.consumedAt) : '—') },
      ]} />
    </div>
  );
}

// ── Commissions & bonuses (EP-12) ─────────────────────────────────────────

const COMM_TABS: Option[] = [{ value: 'commissions', label: 'Commission Statements' }, { value: 'bonuses', label: 'Bonus Programmes' }];

function MatchButton({ row }: { row: R }) {
  const [open, setOpen] = useState(false);
  const [emp, setEmp] = useState('');
  const employees = useEmployees();
  return (
    <>
      <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setOpen(true)}>Match</button>
      {open && (
        <FormModal open onClose={() => setOpen(false)} title={`Match ${String(row.number)}`} path={`${HR}/commission-payouts/${row.id}:match`} submit="Match"
          body={() => clean({ employeeId: emp })}>
          <SelectField label="Employee" value={emp} onChange={setEmp} options={employees} placeholder="The employee linked to the user" />
        </FormModal>
      )}
    </>
  );
}

function CommissionList() {
  const { can } = useAuth();
  return (
    <ListPage title="Commission Statements" path={`${HR}/commission-payouts`} search={false}
      help="Approved commission statements (CRM) paid with payroll: COMMISSION earning and the clawback deduction. The statement turns Paid in CRM when the payroll run is posted."
      statuses={opts(['unmatched', 'ready', 'paid'])}
      rowActions={(r) => (can('hris.commission_payout.manage') && ['unmatched', 'ready'].includes(String(r.status)) ? <MatchButton row={r} /> : null)}
      columns={[
        { key: 'number', header: 'Statement' },
        { key: 'period', header: 'Month' },
        { key: 'employeeName', header: 'Employee', render: (r) => String(r.employeeName ?? r.userName ?? '—') },
        { key: 'earning', header: 'Earning', align: 'right', render: (r) => money(r.earning) },
        { key: 'deduction', header: 'Clawback', align: 'right', render: (r) => money(r.deduction) },
        { key: 'total', header: 'Net', align: 'right', render: (r) => money(r.total) },
        { key: 'status', header: 'Status', render: pill('status') },
      ]} />
  );
}

function BonusList() {
  const nav = useNavigate();
  const { can } = useAuth();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState('');
  const [type, setType] = useState('performance');
  const [period, setPeriod] = useState(today().slice(0, 7));
  return (
    <>
      <ListPage title="Bonus Programmes" path={`${HR}/bonus-programmes`} search={false} statuses={opts(['draft', 'pending_approval', 'approved', 'paid', 'cancelled'])}
        help="Performance / annual bonuses (BONUS, irregular income) entered by HR or generated from the completed performance reviews, approved and paid with payroll."
        actions={can('hris.bonus_programme.manage') && <button className="oc-btn oc-btn-ink" onClick={() => setOpen(true)}><Icon name="add" size={18} /> New programme</button>}
        onRowClick={(r) => nav(`/hris/commissions/bonuses/${r.id}`)}
        columns={[
          { key: 'number', header: 'Programme' }, { key: 'name', header: 'Name' }, { key: 'bonusType', header: 'Type', render: (r) => label(r.bonusType) },
          { key: 'payPeriod', header: 'Payroll' }, { key: 'employees', header: 'Employees', align: 'right' },
          { key: 'total', header: 'Total', align: 'right', render: (r) => money(r.total) }, { key: 'status', header: 'Status', render: pill('status') },
        ]} />
      {open && (
        <FormModal open onClose={() => setOpen(false)} title="New bonus programme" path={`${HR}/bonus-programmes`} submit="Create"
          body={() => ({ name, bonusType: type, payPeriod: period })} onDone={(r) => nav(`/hris/commissions/bonuses/${r.id}`)}>
          <TextField label="Name" value={name} onChange={setName} required />
          <SelectField label="Type" value={type} onChange={setType} options={opts(['performance', 'annual', 'incentive', 'other'])} />
          <TextField label="Paid with the payroll of (YYYY-MM)" value={period} onChange={setPeriod} required />
        </FormModal>
      )}
    </>
  );
}

export function CommissionsPage() {
  const [tab, setTab] = useState('commissions');
  return (
    <div className="oc-stack">
      <Tabs tabs={COMM_TABS} value={tab} onChange={setTab} />
      {tab === 'commissions' ? <CommissionList /> : <BonusList />}
    </div>
  );
}

export function BonusDetailPage() {
  const { id } = useParams();
  const { can } = useAuth();
  const b = useGet<R>(`${HR}/bonus-programmes/${id}`);
  const employees = useEmployees();
  const [open, setOpen] = useState(false);
  const [emp, setEmp] = useState('');
  const [amount, setAmount] = useState('');
  const [note, setNote] = useState('');
  if (b.isLoading) return <Skeleton rows={6} />;
  if (!b.data) return <ErrorAlert error={b.error} />;
  const x = b.data;
  const base = `${HR}/bonus-programmes/${id}`;
  const status = String(x.status);
  const lines = (x.lines as R[] | undefined) ?? [];
  const edit = can('hris.bonus_programme.manage') && status === 'draft';
  const current = lines.map((l) => ({ employeeId: l.employeeId, amount: String(l.amount), note: String(l.note ?? '') }));
  return (
    <div className="oc-stack">
      <PageHeader title={String(x.name)} help={`${String(x.number)} · ${label(x.bonusType)} · payroll ${String(x.payPeriod)}`} actions={
        <div className="oc-row" style={{ gap: 6 }}>
          {edit && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setOpen(true)}>Add line</button>}
          {edit && <ActionButton label="Generate from reviews" path={`${base}:generate`} invalidate={INV} confirm="Replace the lines with the bonus months of the completed performance reviews × fixed wage?" />}
          <Decisions base={base} status={status} submittable={['draft']} perm="hris.bonus_programme.approve" cancelPerm="hris.bonus_programme.manage" />
        </div>
      } />
      <KV items={[['Status', <StatusPill key="s" status={status} label={label(status)} />], ['Employees', String(x.employees)], ['Total', money(x.total)],
        ['Decision note', String(x.decisionNote ?? '—')]]} />
      <DataTable rows={lines} empty={<Empty title="No bonus lines" />}
        actions={edit ? (r) => <ActionButton label="Remove" method="PUT" path={`${base}/lines`} invalidate={INV}
          body={{ lines: current.filter((l) => l.employeeId !== r.employeeId) }} /> : undefined}
        columns={[
          { key: 'fullName', header: 'Employee', render: (r) => `${String(r.fullName)} (${String(r.employeeNo)})` },
          { key: 'amount', header: 'Bonus', align: 'right', render: (r) => money(r.amount) },
          { key: 'basis', header: 'Basis' }, { key: 'note', header: 'Note' },
          { key: 'consumedAt', header: 'Paid', render: (r) => (r.consumedAt ? date(r.consumedAt) : '—') },
        ]} />
      {open && (
        <FormModal open onClose={() => setOpen(false)} title="Add bonus line" method="PUT" path={`${base}/lines`}
          body={() => ({ lines: [...current.filter((l) => l.employeeId !== emp), clean({ employeeId: emp, amount, note })] })}>
          <SelectField label="Employee" value={emp} onChange={setEmp} options={employees} required />
          <TextField label="Amount" type="number" value={amount} onChange={setAmount} required />
          <TextArea label="Note" value={note} onChange={setNote} />
        </FormModal>
      )}
    </div>
  );
}

// ── Payout runs: Caddy (EP-13) and Instructors (EP-14) ───────────────────

const RUN_STATUSES = opts(['draft', 'calculated', 'pending_approval', 'approved', 'paid', 'cancelled']);
const KIND_PATH: Record<string, string> = { caddy: 'caddy', instructor: 'instructors' };

function RunList({ kind }: { kind: 'caddy' | 'instructor' }) {
  const nav = useNavigate();
  const { can } = useAuth();
  const [open, setOpen] = useState(false);
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [notes, setNotes] = useState('');
  const schedule = kind === 'caddy' ? 'twice a month (1–15, 16–month end)' : 'monthly';
  return (
    <>
      <ListPage title="Payout Runs" path={`${HR}/payout-runs`} extraQuery={{ kind }} search={false} statuses={RUN_STATUSES}
        help={`Partner payouts ${schedule}: approved ${kind === 'caddy' ? 'caddy settlements and non-cash tips' : 'instructor fees'}, PPh 21 non-employee (Article 17 × 50% of gross), BPJS BPU and deductions; Finance approval posts the journal, then the bank file and the payment.`}
        actions={can('hris.payout_run.create') && <button className="oc-btn oc-btn-ink" onClick={() => setOpen(true)}><Icon name="add" size={18} /> New payout run</button>}
        onRowClick={(r) => nav(`/hris/${KIND_PATH[kind]}/runs/${r.id}`)}
        columns={[
          { key: 'number', header: 'Run' },
          { key: 'periodStart', header: 'Period', render: (r) => `${date(r.periodStart)} – ${date(r.periodEnd)}` },
          { key: 'payDate', header: 'Pay Date', render: (r) => date(r.payDate) },
          { key: 'partners', header: 'Partners', align: 'right' },
          { key: 'gross', header: 'Gross', align: 'right', render: (r) => money(r.gross) },
          { key: 'pph21', header: 'PPh 21', align: 'right', render: (r) => money(r.pph21) },
          { key: 'net', header: 'Net', align: 'right', render: (r) => money(r.net) },
          { key: 'status', header: 'Status', render: pill('status') },
        ]} />
      {open && (
        <FormModal open onClose={() => setOpen(false)} title="New payout run" path={`${HR}/payout-runs`} submit="Create"
          body={() => clean({ kind, periodStart: from, periodEnd: to, notes })} onDone={(r) => nav(`/hris/${KIND_PATH[kind]}/runs/${r.id}`)}>
          <TextField label="Period start" type="date" value={from} onChange={setFrom} help="Empty: the last completed period" />
          <TextField label="Period end" type="date" value={to} onChange={setTo} />
          <TextArea label="Notes" value={notes} onChange={setNotes} />
        </FormModal>
      )}
    </>
  );
}

function SourceList({ kind }: { kind: 'caddy' | 'instructor' }) {
  return (
    <ListPage title={kind === 'caddy' ? 'Approved Settlements' : 'Approved Instructor Fees'} path={`${HR}/payout-sources`} extraQuery={{ kind }} search={false}
      help="Approved in Golf / Sport Club and waiting for (or paid by) a payout run; fees of employee instructors are paid with payroll."
      statuses={opts(['open', 'claimed', 'paid'])}
      columns={[
        { key: 'number', header: 'Document' }, { key: 'partnerName', header: kind === 'caddy' ? 'Caddy' : 'Instructor' },
        { key: 'periodEnd', header: 'Period', render: (r) => `${date(r.periodStart)} – ${date(r.periodEnd)}` },
        { key: 'units', header: kind === 'caddy' ? 'Rounds' : 'Sessions', align: 'right' },
        { key: 'gross', header: 'Gross', align: 'right', render: (r) => money(r.gross) },
        { key: 'deductions', header: 'Deductions', align: 'right', render: (r) => money(r.deductions) },
        { key: 'channel', header: 'Paid by', render: (r) => (r.channel === 'payroll' ? 'Payroll' : 'Payout run') },
        { key: 'status', header: 'Status', render: pill('status') },
      ]} />
  );
}

export function PayoutRunsPage({ kind }: { kind: 'caddy' | 'instructor' }) {
  const [tab, setTab] = useState('runs');
  const tabs: Option[] = [{ value: 'runs', label: 'Payout Runs' }, { value: 'sources', label: kind === 'caddy' ? 'Settlements' : 'Instructor Fees' },
    { value: 'profiles', label: 'Partner Profiles' }];
  return (
    <div className="oc-stack">
      <PageHeader title={kind === 'caddy' ? 'Caddy' : 'Instructors'} help="Non-employee workforce (partners): payout runs, statements and payout profiles." />
      <Tabs tabs={tabs} value={tab} onChange={setTab} />
      {tab === 'runs' && <RunList kind={kind} />}
      {tab === 'sources' && <SourceList kind={kind} />}
      {tab === 'profiles' && <AutoResourcePage resourceKey="hris.partner_profile" />}
    </div>
  );
}

function PaidButton({ base }: { base: string }) {
  const [open, setOpen] = useState(false);
  const [paidOn, setPaidOn] = useState(today());
  const [reference, setReference] = useState('');
  return (
    <>
      <button className="oc-btn oc-btn-ink oc-btn-sm" onClick={() => setOpen(true)}>Mark paid</button>
      {open && (
        <FormModal open onClose={() => setOpen(false)} title="Confirm the payment" path={`${base}:mark-paid`} submit="Mark paid"
          body={() => clean({ paidOn, reference })}>
          <TextField label="Paid on" type="date" value={paidOn} onChange={setPaidOn} required />
          <TextField label="Bank / transfer reference" value={reference} onChange={setReference} />
        </FormModal>
      )}
    </>
  );
}

export function PayoutRunDetailPage() {
  const { id } = useParams();
  const { can } = useAuth();
  const toast = useToast();
  const r = useGet<R>(`${HR}/payout-runs/${id}`);
  if (r.isLoading) return <Skeleton rows={8} />;
  if (!r.data) return <ErrorAlert error={r.error} />;
  const x = r.data;
  const base = `${HR}/payout-runs/${id}`;
  const status = String(x.status);
  const lines = (x.lines as R[] | undefined) ?? [];
  const versions = ((x.policyVersions as R[] | undefined) ?? []).map((p) => `${String(p.code)} v${String(p.version)}`).join(', ');
  const file = (format: string) => download('GET', `${base}/bank-file?format=${format}`, undefined, `${String(x.number)}-${format}.csv`)
    .catch((e: Error) => toast(e.message, 'error'));
  return (
    <div className="oc-stack">
      <PageHeader title={`Payout Run ${String(x.number)}`} help={`${x.kind === 'caddy' ? 'Caddy' : 'Instructor'} · ${date(x.periodStart)} – ${date(x.periodEnd)}`} actions={
        <div className="oc-row" style={{ gap: 6 }}>
          {can('hris.payout_run.calculate') && ['draft', 'calculated'].includes(status) && (
            <ActionButton label="Calculate" path={`${base}:calculate`} invalidate={INV} kind="primary" />
          )}
          <Decisions base={base} status={status} submittable={['calculated']} perm="hris.payout_run.approve" cancelPerm="hris.payout_run.create" />
          {can('hris.payout_run.export') && ['approved', 'paid'].includes(status) && (
            <>
              <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => file('generic_csv')}><Icon name="download" size={18} /> Bank file</button>
              <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => file('bca_csv')}>BCA layout</button>
            </>
          )}
          {can('hris.payout_run.pay') && status === 'approved' && <PaidButton base={base} />}
        </div>
      } />
      <Card title="Totals" icon="payments">
        <KV items={[
          ['Status', <StatusPill key="s" status={status} label={label(status)} />],
          ['Partners', `${String(x.partners)}${Number(x.heldPartners) ? ` (${String(x.heldPartners)} below the minimum net, next run)` : ''}`],
          ['Gross', money(x.gross)], ['Settlement deductions', money(x.sourceDeductions)], ['PPh 21 non-employee', money(x.pph21)],
          ['BPJS Ketenagakerjaan BPU', money(x.bpu)], ['Deductions', money(x.otherDeductions)], ['Net', money(x.net)],
          ['Bank transfer / cash', `${money(x.bankAmount)} / ${money(x.cashAmount)}`], ['Pay date', date(x.payDate)],
          ['Paid', x.paidOn ? `${date(x.paidOn)} · ${String(x.paidReference ?? '')}` : '—'], ['Policy versions', versions || '—'],
          ['Decision note', String(x.decisionNote ?? '—')],
        ]} />
        {Boolean(x.taxNote) && <p className="oc-small oc-muted">{String(x.taxNote)}</p>}
      </Card>
      <DataTable rows={lines} empty={<Empty title="Not calculated yet" help="Calculate to collect the approved settlements / fees of the period." />}
        actions={(l) => (
          <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => download('GET', `${base}/statements/${l.id}`, undefined, `${String(x.number)}-${String(l.partnerCode ?? l.partnerName)}.pdf`)
            .catch((e: Error) => toast(e.message, 'error'))}>Statement</button>
        )}
        columns={[
          { key: 'partnerName', header: 'Partner', render: (l) => `${String(l.partnerName)}${l.partnerCode ? ` (${String(l.partnerCode)})` : ''}` },
          { key: 'units', header: x.kind === 'caddy' ? 'Rounds' : 'Sessions', align: 'right' },
          { key: 'gross', header: 'Gross', align: 'right', render: (l) => money(l.gross) },
          { key: 'pph21', header: 'PPh 21', align: 'right', render: (l) => `${money(l.pph21)}${l.hasTaxId ? '' : ' *'}` },
          { key: 'bpu', header: 'BPU', align: 'right', render: (l) => money(Number(l.bpuJkk) + Number(l.bpuJkm)) },
          { key: 'deductions', header: 'Deductions', align: 'right', render: (l) => money(Number(l.sourceDeductions) + Number(l.otherDeductions)) },
          { key: 'net', header: 'Net', align: 'right', render: (l) => money(l.net) },
          { key: 'paymentMethod', header: 'Payment', render: (l) => (l.paymentMethod === 'cash' ? 'Cash' : `${String(l.bankName ?? 'Bank')} ${String(l.bankAccountNo ?? '')}`) },
        ]} />
      <p className="oc-small oc-muted">* without NPWP / NIK: PPh 21 raised by the non-NPWP surcharge.</p>
    </div>
  );
}

// ── Partner statements: Caddy App & instructor Honor Statement ───────────

/** The signed-in partner's payout history (Caddy App "Payout History", instructor "Honor Statement"). */
export function MyPayoutsPage({ kind, base }: { kind: 'caddy' | 'instructor'; base: string }) {
  const nav = useNavigate();
  const h = useGet<Page<R>>(`${HR}/my-payouts?kind=${kind}`);
  const title = kind === 'caddy' ? 'Payout History' : 'Honor Statement';
  return (
    <div className="oc-stack">
      <div className="oc-page-head"><div><h1>{title}</h1><p>Approved and paid statements</p></div></div>
      <ErrorAlert error={h.error} />
      {h.isLoading ? <Skeleton rows={4} /> : (h.data?.items ?? []).length === 0 ? <Empty title="No statement yet" icon="receipt_long" /> : (
        <div className="oc-stack">
          {(h.data?.items ?? []).map((s) => (
            <button key={String(s.lineId)} className="oc-card" style={{ textAlign: 'left', minHeight: 64 }} onClick={() => nav(`${base}/${String(s.lineId)}`)}>
              <div className="oc-row" style={{ justifyContent: 'space-between' }}>
                <div>
                  <strong>{date(s.periodStart)} – {date(s.periodEnd)}</strong>
                  <div className="oc-small oc-muted">{String(s.number)} · {String(s.units)} {kind === 'caddy' ? 'rounds' : 'sessions'}</div>
                </div>
                <div style={{ textAlign: 'right' }}>
                  <div className="oc-metric" style={{ fontSize: 20 }}>{money(s.net)}</div>
                  <StatusPill status={String(s.runStatus)} label={label(s.runStatus)} />
                </div>
              </div>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

/** One statement of the signed-in partner. */
export function MyStatementPage() {
  const { lineId } = useParams();
  const toast = useToast();
  const s = useGet<R>(`${HR}/my-payouts/${lineId}`);
  if (s.isLoading) return <Skeleton rows={6} />;
  if (!s.data) return <ErrorAlert error={s.error} />;
  const x = s.data;
  const l = (x.line ?? {}) as R;
  const sources = (l.sources as R[] | undefined) ?? [];
  const deductions = (l.deductions as R[] | undefined) ?? [];
  return (
    <div className="oc-stack">
      <div className="oc-page-head">
        <div><h1>{String(x.title)}</h1><p>{String(x.number)} · {date(x.periodStart)} – {date(x.periodEnd)}</p></div>
        <button className="oc-btn oc-btn-neutral" style={{ minHeight: 48 }} onClick={() => download('GET', `${HR}/my-payouts/${lineId}/pdf`, undefined, `${String(x.number)}.pdf`)
          .catch((e: Error) => toast(e.message, 'error'))}><Icon name="picture_as_pdf" size={20} /> PDF</button>
      </div>
      <Card title="Statement" icon="receipt_long">
        <KV items={[
          [x.kind === 'caddy' ? 'Rounds' : 'Sessions', String(l.units)],
          ...(x.kind === 'caddy' ? [['Caddy fee', money(l.fee)], ['Non-cash tips', money(l.tips)]] as [string, React.ReactNode][] : [['Honorarium', money(l.fee)]] as [string, React.ReactNode][]),
          ['Gross', money(l.gross)], ['Settlement deductions', money(l.sourceDeductions)],
          ['PPh 21', `${money(l.pph21)} (${String(l.pph21Rate)}% of ${money(l.taxBase)})`],
          ['BPJS BPU', money(Number(l.bpuJkk) + Number(l.bpuJkm))],
          ...deductions.map((d) => [String(d.label), money(d.amount)] as [string, React.ReactNode]),
          ['Net', <strong key="n">{money(l.net)}</strong>],
          ['Payment', l.paymentMethod === 'cash' ? 'Cash' : `${String(l.bankName ?? 'Bank')} ${String(l.bankAccountNo ?? '')}`],
          ['Status', <StatusPill key="s" status={String(x.runStatus)} label={label(x.runStatus)} />],
          ['Paid on', date(x.paidOn)],
        ]} />
      </Card>
      <Card title={x.kind === 'caddy' ? 'Settlements' : 'Instructor fees'} icon="list_alt">
        <DataTable rows={sources.map((src, i) => ({ ...src, id: String(src.sourceId ?? i) }) as R)} columns={[
          { key: 'number', header: 'Document' }, { key: 'gross', header: 'Gross', align: 'right', render: (r) => money(r.gross) },
          { key: 'deductions', header: 'Deductions', align: 'right', render: (r) => money(r.deductions) },
        ]} />
      </Card>
      {Boolean(x.taxNote) && <p className="oc-small oc-muted">{String(x.taxNote)}</p>}
    </div>
  );
}

// ── Employee Self Service (EP-16) ─────────────────────────────────────────

function EssServiceCharge() {
  const l = useGet<Page<R>>(`${ESS}/service-charge`);
  return (
    <div className="oc-stack">
      <PageHeader title="Service Charge" help="Your share of the service charge per month: equal share × attendance factor (days present ÷ working days), paid with payroll." />
      <ErrorAlert error={l.error} />
      <DataTable rows={(l.data?.items ?? []).map((r) => ({ ...r, id: String(r.distributionId) }) as R)} loading={l.isLoading}
        empty={<Empty title="No service charge yet" icon="room_service" />} columns={[
          { key: 'period', header: 'Month' },
          { key: 'attendanceFactor', header: 'Attendance', render: (r) => (r.eligible ? `${String(r.presentDays)} / ${String(r.scheduledDays)} days (${String(r.attendanceFactor)})` : '—') },
          { key: 'amount', header: 'Amount', align: 'right', render: (r) => (r.eligible ? money(r.amount) : `Not eligible: ${label(r.exclusionReason)}`) },
          { key: 'payPeriod', header: 'Payroll' },
          { key: 'paidAt', header: 'Paid', render: (r) => (r.paidAt ? date(r.paidAt) : 'Waiting for payroll') },
        ]} />
    </div>
  );
}

function EssCommissions() {
  const l = useGet<Page<R>>(`${ESS}/commissions`);
  return (
    <div className="oc-stack">
      <PageHeader title="Commission & Bonus" help="Approved sales commission (with clawbacks of earlier deals) and bonuses, paid with payroll." />
      <ErrorAlert error={l.error} />
      <DataTable rows={l.data?.items} loading={l.isLoading} empty={<Empty title="No commission or bonus yet" icon="workspace_premium" />} columns={[
        { key: 'type', header: 'Type', render: (r) => label(r.type) }, { key: 'description', header: 'Description' }, { key: 'period', header: 'Period' },
        { key: 'earning', header: 'Earning', align: 'right', render: (r) => money(r.earning) },
        { key: 'deduction', header: 'Clawback', align: 'right', render: (r) => money(r.deduction) },
        { key: 'net', header: 'Net', align: 'right', render: (r) => money(r.net) },
        { key: 'status', header: 'Status', render: (r) => (r.status === 'paid' ? `Paid ${date(r.paidAt)}` : 'Waiting for payroll') },
      ]} />
    </div>
  );
}

registerEssSection('service-charge', () => <EssServiceCharge />);
registerEssSection('commissions', () => <EssCommissions />);

export const PAYOUTS_ROUTES: AreaRoute[] = [
  { path: 'hris/service-charge', perm: 'hris.service_charge.view', element: <ServiceChargePage /> },
  { path: 'hris/service-charge/:id', perm: 'hris.service_charge.view', element: <ServiceChargeDetailPage /> },
  { path: 'hris/commissions', perm: 'hris.commission_payout.view', element: <CommissionsPage /> },
  { path: 'hris/commissions/bonuses/:id', perm: 'hris.bonus_programme.view', element: <BonusDetailPage /> },
  { path: 'hris/caddy', perm: 'hris.payout_run.view', element: <PayoutRunsPage kind="caddy" /> },
  { path: 'hris/caddy/runs/:id', perm: 'hris.payout_run.view', element: <PayoutRunDetailPage /> },
  { path: 'hris/instructors', perm: 'hris.payout_run.view', element: <PayoutRunsPage kind="instructor" /> },
  { path: 'hris/instructors/runs/:id', perm: 'hris.payout_run.view', element: <PayoutRunDetailPage /> },
];

export const PAYOUTS_OPS_TILES: OpsTile[] = [['request_quote', 'Honor Statement', '/ops/instructor/honor', 'hris.payout.own']];

export const PAYOUTS_OPS_ROUTES: OpsRoute[] = [
  { path: 'instructor/honor', element: <MyPayoutsPage kind="instructor" base="/ops/instructor/honor" /> },
  { path: 'instructor/honor/:lineId', element: <MyStatementPage /> },
];

/** Caddy App (tablet shell) routes: Payout History and Statement (PRD P5 §7.3). */
export const PAYOUTS_TABLET_ROUTES = [
  { path: 'payouts', element: <MyPayoutsPage kind="caddy" base="/tablet/payouts" /> },
  { path: 'payouts/:lineId', element: <MyStatementPage /> },
];
