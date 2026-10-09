import React, { useState } from 'react';
import { Link, Navigate, useNavigate, useParams } from 'react-router';
import { download, uuidv7, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate, formatNumber } from '@oneclub/i18n';
import {
  AutoResourcePage, Card, Checkbox, DataTable, Empty, ErrorAlert, FilterPills, Icon, Modal, MoneyField, PageHeader, SearchBox, SelectField, Skeleton,
  StatTile, StatusPill, TextArea, TextField, useAuth, useToast, type Option,
} from '@oneclub/shell';
import { ActionButton, KV, Tabs, money, today, type R } from '../p1/common';
import type { AreaRoute, OpsRoute, OpsTile } from '../p3/types';
import { registerEssSection, useUrlTab } from './hr';
import { PAYOUTS_OPS_ROUTES, PAYOUTS_OPS_TILES, PAYOUTS_ROUTES } from './payouts';

// PRD P5 — payroll (EP-09 Payroll Engine, EP-10 PPh 21 & BPJS, EP-15 Payroll Accounting, Payment & Payslip): HRIS → Payroll (runs with
// calculate → approve → post → bank file → paid, payslips, comparison with the previous period, journal, parallel run; adjustments and
// bonuses, salary structures, pay components, statutory rates, statutory exports and migration imports), HRIS → Benefits (PTKP,
// TER category, BPJS and bank readiness) and the Employee Self Service section Payslip (own payslips, PDF; never cached offline).
// The payouts area (service charge, commissions, caddy & instructor payouts) adds its own file and routes next to these.

const HR = '/api/v1/hris';
const ESS = '/api/v1/ess';
const INV = [HR, ESS];
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const val = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : String(v));
const date = (v: unknown) => (v ? formatDate(String(v).slice(0, 10)) : '—');
const idem = () => ({ 'Idempotency-Key': uuidv7() });
const clean = (o: Record<string, unknown>) => Object.fromEntries(Object.entries(o).filter(([, v]) => v !== '' && v !== undefined && v !== null));
const opts = (vals: string[]): Option[] => vals.map((v) => ({ value: v, label: label(v) }));
const pill = (k: string) => (r: R) => <StatusPill status={String(r[k] ?? '')} label={label(r[k])} />;
const num = (k: string) => (r: R) => money(r[k]);
const thisPeriod = () => today().slice(0, 7);
const RUN_TYPES = ['regular', 'thr', 'bonus', 'adjustment', 'final_settlement'];
const RUN_STATUSES = opts(['draft', 'calculated', 'submitted', 'approved', 'posted', 'paid', 'cancelled']);

function useOptions(path: string | null, text: (r: R) => string): Option[] {
  const l = useGet<Page<R>>(path);
  return (l.data?.items ?? []).map((x) => ({ value: x.id, label: text(x) }));
}
const useEmployees = () => useOptions(`${HR}/employees?limit=500`, (x) => `${String(x.fullName)} (${String(x.employeeNo)})`);
const useUnits = () => useOptions(`${HR}/org-units?limit=500&filter[status]=active`, (x) => `${String(x.name)} (${String(x.code)})`);

/** A form in a modal that POSTs / PATCHes a body. */
function FormModal({ title, method = 'POST', path, body, children, submit = 'Save', onClose, onDone, wide }: {
  title: string; method?: 'POST' | 'PATCH'; path: string; body: () => Record<string, unknown>; children: React.ReactNode; submit?: string; onClose: () => void;
  onDone?: (r: R) => void; wide?: boolean;
}) {
  const toast = useToast();
  const send = useSend<Record<string, unknown>, R>(method, path, INV, method === 'POST' ? idem : undefined);
  return (
    <Modal open onClose={onClose} title={title} wide={wide} actions={
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

/** Posts an action; a note is asked when required / optional. */
function Act({ label: text, path, note, kind = 'neutral', body }: {
  label: string; path: string; note?: 'required' | 'optional'; kind?: 'neutral' | 'ink' | 'text'; body?: Record<string, unknown>;
}) {
  const toast = useToast();
  const [open, setOpen] = useState(false);
  const [why, setWhy] = useState('');
  const send = useSend<Record<string, unknown>, R>('POST', path, INV);
  const run = (b: Record<string, unknown>) => send.mutate(b, {
    onSuccess: () => { setOpen(false); setWhy(''); toast(`${text}: done`); },
    onError: (e) => toast(e.message, 'error'),
  });
  return (
    <>
      <button className={`oc-btn oc-btn-${kind}`} style={{ minHeight: 40 }} disabled={send.isPending} onClick={() => (note ? setOpen(true) : run(body ?? {}))}>{text}</button>
      {open && (
        <Modal open onClose={() => setOpen(false)} title={text} actions={
          <>
            <button className="oc-btn oc-btn-text" onClick={() => setOpen(false)}>Cancel</button>
            <button className="oc-btn oc-btn-ink" disabled={send.isPending || (note === 'required' && !why.trim())} onClick={() => run({ ...(body ?? {}), ...(why ? { note: why } : {}) })}>{text}</button>
          </>
        }>
          <ErrorAlert error={send.error} />
          <TextArea label={note === 'required' ? 'Reason' : 'Note (optional)'} value={why} onChange={setWhy} required={note === 'required'} />
        </Modal>
      )}
    </>
  );
}

function Dl({ label: text, path, file, icon = 'download' }: { label: string; path: string; file: string; icon?: string }) {
  const toast = useToast();
  return (
    <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => download('GET', path, undefined, file).catch((e: Error) => toast(e.message, 'error'))}>
      <Icon name={icon} size={18} /> {text}
    </button>
  );
}

// ── HRIS → Payroll ────────────────────────────────────────────────────────

const PAYROLL_TABS: Option[] = [
  { value: 'runs', label: 'Payroll Runs' }, { value: 'adjustments', label: 'Adjustments & Bonuses' },
  { value: 'structures', label: 'Salary Structures' }, { value: 'components', label: 'Pay Components' }, { value: 'rates', label: 'Statutory Rates' },
  { value: 'exports', label: 'Exports & Imports' },
];

export function PayrollPage() {
  const [tab, setTab] = useUrlTab('runs');
  // Loans & Advances moved to Employee Services; older notification links keep working.
  if (tab === 'loans') return <Navigate to="/hris/loans" replace />;
  return (
    <div className="oc-stack">
      <PageHeader title="Payroll" help="Monthly payroll of the property: calculate from salary structures, contracts, attendance, approved overtime, unpaid leave, service charge and commissions; PPh 21 (TER, annual in December) and BPJS; approval by Finance & HR, payroll journal, bank file, payment and payslips in Employee Self Service." />
      <Tabs tabs={PAYROLL_TABS} value={tab} onChange={setTab} />
      {tab === 'runs' && <RunList />}
      {tab === 'adjustments' && <AdjustmentList />}
      {tab === 'structures' && <StructureList />}
      {tab === 'components' && <Components />}
      {tab === 'rates' && <RateList />}
      {tab === 'exports' && <ExportsImports />}
    </div>
  );
}

function RunList() {
  const nav = useNavigate();
  const units = useUnits();
  const employees = useEmployees();
  const [status, setStatus] = useState('');
  const [open, setOpen] = useState(false);
  const [f, setF] = useState<Record<string, string>>({ runType: 'regular', periodCode: thisPeriod() });
  const [leavers, setLeavers] = useState<string[]>([]);
  const list = useGet<Page<R>>(`${HR}/payroll-runs${status ? `?status=${status}` : ''}`);
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap">
        <FilterPills options={[{ value: '', label: 'All' }, ...RUN_STATUSES]} value={status} onChange={setStatus} />
        <span className="oc-spacer" />
        <button className="oc-btn oc-btn-ink" onClick={() => setOpen(true)}><Icon name="add" size={18} /> New payroll run</button>
      </div>
      <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error} onRowClick={(r) => nav(`/hris/payroll/runs/${r.id}`)} columns={[
        { key: 'number', header: 'Run' }, { key: 'name', header: 'Name' }, { key: 'runType', header: 'Type', render: (r) => label(r.runType) },
        { key: 'periodCode', header: 'Period' }, { key: 'paymentDate', header: 'Payment', render: (r) => date(r.paymentDate) },
        { key: 'headcount', header: 'Employees', align: 'right' }, { key: 'gross', header: 'Gross', align: 'right', render: num('gross') },
        { key: 'net', header: 'Net pay', align: 'right', render: num('net') }, { key: 'status', header: 'Status', render: pill('status') },
      ]} />
      {open && (
        <FormModal title="New Payroll Run" path={`${HR}/payroll-runs`} onClose={() => setOpen(false)} onDone={(r) => nav(`/hris/payroll/runs/${r.id}`)}
          body={() => clean({ runType: f.runType, periodCode: f.periodCode, name: f.name, paymentDate: f.paymentDate, orgUnitId: f.orgUnitId,
            correctsPeriod: f.runType === 'adjustment' ? f.correctsPeriod : undefined, thrDate: f.runType === 'thr' ? f.thrDate : undefined,
            employeeIds: leavers.length ? leavers : undefined })}>
          <SelectField label="Run type" value={f.runType} onChange={(v) => setF({ ...f, runType: v })} options={opts(RUN_TYPES)} required />
          <TextField label="Period (YYYY-MM)" value={f.periodCode} onChange={(v) => setF({ ...f, periodCode: v })} required />
          <TextField label="Payment date (default: pay day)" type="date" value={f.paymentDate ?? ''} onChange={(v) => setF({ ...f, paymentDate: v })} />
          <SelectField label="Department (empty = all)" value={f.orgUnitId ?? ''} onChange={(v) => setF({ ...f, orgUnitId: v })} options={units} placeholder="All" />
          {f.runType === 'adjustment' && <TextField label="Corrects period (YYYY-MM)" value={f.correctsPeriod ?? ''} onChange={(v) => setF({ ...f, correctsPeriod: v })} />}
          {f.runType === 'thr' && <TextField label="Religious holiday" type="date" value={f.thrDate ?? ''} onChange={(v) => setF({ ...f, thrDate: v })} />}
          {f.runType === 'final_settlement' && (
            <SelectField label="Add a leaver" value="" onChange={(v) => v && !leavers.includes(v) && setLeavers([...leavers, v])} options={employees} span
              help={leavers.length ? `${leavers.length} employee(s): ${leavers.map((x) => employees.find((e) => e.value === x)?.label ?? x).join(', ')}` : 'Name the leavers'} />
          )}
          <TextField label="Name" value={f.name ?? ''} onChange={(v) => setF({ ...f, name: v })} span />
        </FormModal>
      )}
    </div>
  );
}

// The life of a run, shown as steps on its page (cancelled runs show a pill instead).
const RUN_STEPS: [string, string][] = [
  ['draft', 'Draft'], ['calculated', 'Calculated'], ['submitted', 'Under review'], ['approved', 'Approved'], ['posted', 'Posted to Finance'],
  ['paid', 'Paid'],
];

function RunSteps({ status }: { status: string }) {
  const at = RUN_STEPS.findIndex(([s]) => s === status);
  return (
    <ol className="oc-steps" aria-label="Payroll run progress">
      {RUN_STEPS.map(([s, text], i) => (
        <li key={s} data-state={i < at ? 'done' : i === at ? 'current' : 'todo'} aria-current={i === at ? 'step' : undefined}>
          <span className="oc-steps-dot">{i < at ? <Icon name="check" size={14} /> : i + 1}</span>
          <span>{text}</span>
        </li>
      ))}
    </ol>
  );
}

/**
 * Payroll run page: what the run is, where it stands and the next action
 * at the top; totals as tiles; payslips, exceptions, comparison, journal,
 * parallel run and the run details in tabs.
 */
export function PayrollRunPage() {
  const { id = '' } = useParams();
  const { can } = useAuth();
  const base = `${HR}/payroll-runs/${id}`;
  const run = useGet<R>(base);
  const st = String(run.data?.status ?? '');
  const checked = ['draft', 'calculated'].includes(st) && Number(run.data?.calculationCount) > 0;
  const exceptions = useGet<Page<R>>(checked ? `${base}/exceptions` : null);
  const [tab, setTab] = useUrlTab('payslips');
  const [paying, setPaying] = useState(false);
  const [layout, setLayout] = useState('generic_csv');
  const [pay, setPay] = useState<Record<string, string>>({ paidOn: today() });
  if (run.isLoading) return <Skeleton rows={8} />;
  if (run.error || !run.data) return <ErrorAlert error={run.error} />;
  const r = run.data;
  const issues = exceptions.data?.items ?? [];
  const blocking = issues.filter((x) => x.severity === 'error').length;
  const tabs: Option[] = [
    { value: 'payslips', label: `Payslips · ${String(r.headcount ?? 0)}` },
    ...(issues.length ? [{ value: 'exceptions', label: `Exceptions · ${issues.length}` }] : []),
    { value: 'comparison', label: 'Comparison' }, { value: 'journal', label: 'Journal' }, { value: 'parallel', label: 'Parallel Run' },
    { value: 'details', label: 'Details' },
  ];
  const current = tabs.some((t) => t.value === tab) ? tab : 'payslips';
  const deductions = Number(r.bpjsEmployee ?? 0) + Number(r.pph21 ?? 0) + Number(r.otherDeductions ?? 0);
  const manage = can('hris.payroll_run.manage');
  return (
    <div className="oc-stack">
      <div className="oc-row" style={{ gap: 8, alignItems: 'flex-start' }}>
        <Link className="oc-btn oc-btn-text" to="/hris/payroll" aria-label="Back to Payroll"><Icon name="arrow_back" size={22} /></Link>
        <PageHeader title={`${String(r.number)} · ${String(r.name)}`}
          help={`${label(r.runType)} run · ${date(r.periodStart)} – ${date(r.periodEnd)} · payment ${date(r.paymentDate)}`}
          actions={<>
            {manage && ['draft', 'calculated', 'submitted'].includes(st) && <Act label="Cancel run" path={`${base}:cancel`} note="required" kind="text" />}
            {manage && (st === 'draft' || st === 'calculated') && (
              <Act label={st === 'draft' ? 'Calculate' : 'Recalculate'} path={`${base}:calculate`} kind={st === 'draft' ? 'ink' : 'neutral'} />
            )}
            {can('hris.payroll_run.approve') && st === 'calculated' && <Act label="Submit for approval" path={`${base}:approve`} note="optional" kind="ink" />}
            {can('hris.payroll_run.post') && st === 'approved' && <Act label="Post" path={`${base}:post`} kind="ink" />}
            {can('hris.payroll_run.pay') && st === 'posted' && (
              <button className="oc-btn oc-btn-ink" onClick={() => setPaying(true)}><Icon name="payments" size={18} /> Mark paid</button>
            )}
          </>} />
      </div>
      {st === 'cancelled' ? <StatusPill status={st} label="Cancelled" /> : <RunSteps status={st} />}
      {r.statutoryVerified === false && (
        <div className="oc-alert oc-alert-warning" role="status">The statutory rates {String(r.statutoryRateCode)} are still to be verified by the tax consultant (FR-TAX-HR-05).</div>
      )}
      {issues.length > 0 && current !== 'exceptions' && (
        <div className={`oc-alert ${blocking ? 'oc-alert-error' : 'oc-alert-warning'} oc-row-wrap`} role={blocking ? 'alert' : 'status'}>
          <span style={{ flex: 1 }}>
            {blocking
              ? `${blocking} blocking exception(s): fix them and recalculate before submitting the run for approval.`
              : `${new Set(issues.map((x) => String(x.employeeId))).size} employee(s) with warnings: review them before submitting the run.`}
          </span>
          <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setTab('exceptions')}>Review exceptions</button>
        </div>
      )}
      <div className="oc-stat-grid">
        <StatTile label="Employees" icon="groups" value={formatNumber(Number(r.headcount ?? 0))}
          status={Number(r.warnings) ? <StatusPill status="warning" label={`${String(r.warnings)} with warnings`} /> : undefined} />
        <StatTile label="Gross pay" icon="payments" value={money(r.gross)} />
        <StatTile label="Deductions" icon="money_off" value={money(deductions)}
          title={`BPJS employee ${money(r.bpjsEmployee)} · PPh 21 ${money(r.pph21)} · other ${money(r.otherDeductions)}`} />
        <StatTile label="Net pay" icon="account_balance_wallet" value={money(r.net)} />
        <StatTile label="Employer cost" icon="domain" value={money(r.employerCost)} title={`Gross + BPJS employer ${money(r.bpjsEmployer)}`} />
      </div>
      {['approved', 'posted', 'paid'].includes(st) && <FinancePosting run={r} />}
      {can('hris.payroll_run.pay') && ['approved', 'posted', 'paid'].includes(st) && (
        <div className="oc-row-wrap">
          <SelectField label="Bank file layout" value={layout} onChange={setLayout} options={[{ value: 'generic_csv', label: 'Generic CSV' }, { value: 'bca_payroll', label: 'BCA payroll (fixed width)' }]} />
          <Dl label="Bank file" path={`${base}/bank-file?layout=${layout}`} file={`${String(r.number)}-${layout}.${layout === 'generic_csv' ? 'csv' : 'txt'}`} />
        </div>
      )}
      <Tabs tabs={tabs} value={current} onChange={setTab} />
      {current === 'payslips' && <RunPayslips runId={id} />}
      {current === 'exceptions' && <RunExceptions runId={id} items={issues} canFix={manage} />}
      {current === 'comparison' && <Comparison runId={id} />}
      {current === 'journal' && <Journal runId={id} />}
      {current === 'parallel' && <Parallel runId={id} run={r} />}
      {current === 'details' && (
        <Card title="Run details" icon="info">
          <KV items={[
            ['Period', `${String(r.periodCode)} (${date(r.periodStart)} – ${date(r.periodEnd)})`], ['Payment date', date(r.paymentDate)],
            ['BPJS employee', money(r.bpjsEmployee)], ['PPh 21', money(r.pph21)], ['Other deductions', money(r.otherDeductions)],
            ['BPJS employer', money(r.bpjsEmployer)], ['Attendance lock', val(r.timeLockStatus)], ['Statutory rates', val(r.statutoryRateCode)],
            ['Policies', ((r.policyRefs as R[] | undefined) ?? []).map((p) => `${String(p.code)} v${String(p.version)}`).join(', ') || '—'],
            ['Paid', r.paidOn ? `${date(r.paidOn)} · ${val(r.paymentReference)}` : '—'], ['Decision', val(r.decisionNote)],
          ]} />
        </Card>
      )}
      {paying && (
        <FormModal title="Mark Paid" path={`${base}:mark-paid`} onClose={() => setPaying(false)} submit="Confirm payment"
          body={() => clean({ paidOn: pay.paidOn, reference: pay.reference, bankAccountCode: pay.bankAccountCode })}>
          <TextField label="Paid on" type="date" value={pay.paidOn ?? ''} onChange={(v) => setPay({ ...pay, paidOn: v })} required />
          <TextField label="Bank reference" value={pay.reference ?? ''} onChange={(v) => setPay({ ...pay, reference: v })} required />
          <TextField label="GL bank account code (empty = default)" value={pay.bankAccountCode ?? ''} onChange={(v) => setPay({ ...pay, bankAccountCode: v })} />
        </FormModal>
      )}
    </div>
  );
}

/**
 * Integration with Finance & Accounting of a posted run: the status the run
 * follows from accounting's events (Posted to Finance / Posting failed with
 * the reason), the payroll and payment journals booked from
 * hris.payroll_posted / hris.payroll_paid (looked up by source) and the open
 * posting exceptions with a repost, for users of Accounting.
 */
function FinancePosting({ run }: { run: R }) {
  const { can } = useAuth();
  const ACC = '/api/v1/accounting';
  const runId = String(run.id);
  const status = String(run.status);
  const journals = useGet<Page<R>>(can('accounting.journal.view') ? `${ACC}/journals?filter[sourceId]=${runId}&limit=20` : null);
  const exceptions = useGet<Page<R>>(can('accounting.posting.view') ? `${ACC}/posting-exceptions?filter[status]=open&limit=200` : null);
  const failed = (exceptions.data?.items ?? []).filter((e) => String(e.eventType).startsWith('hris.payroll_')
    && (e.sourceId === runId || (e.eventPayload as R | undefined)?.runId === runId));
  const booked = journals.data?.items ?? [];
  const [tone, text] = ({ failed: ['error', 'Posting failed'], posted: ['approved', 'Posted to Finance'], pending: ['pending', 'Posting in progress'] } as
    Record<string, [string, string]>)[String(run.financeStatus)] ?? ['pending', 'Not posted yet'];
  const numbers = (run.financeJournals as string[] | undefined) ?? [];
  return (
    <Card title="Finance & Accounting" icon="account_balance" actions={<StatusPill status={tone} label={text} />}>
      {run.financeStatus === 'failed' && !failed.length && (
        <div className="oc-alert oc-alert-error" role="alert">{String(run.financeMessage ?? 'Accounting could not book the payroll journal')}. {can('accounting.posting.manage') ? '' : 'Ask Finance to fix the mapping and retry the posting.'}</div>
      )}
      {numbers.length > 0 && !booked.length && <p className="oc-small" style={{ margin: 0 }}>Journals: {numbers.join(', ')}</p>}
      {failed.map((e) => (
        <div key={String(e.id)} className="oc-alert oc-alert-error oc-row-wrap" role="alert">
          <span>{label(e.reason)}: {String(e.message ?? '')}{Number(e.attempts) ? ` · ${String(e.attempts)} attempts` : ''}</span>
          <span className="oc-spacer" />
          {can('accounting.posting.manage') && (
            <ActionButton label="Retry posting" path={`${ACC}/posting-exceptions/${String(e.id)}:repost`} invalidate={[ACC]} kind="ink" />
          )}
          <Link className="oc-btn oc-btn-text oc-btn-sm" to="/accounting/closing">Posting exceptions</Link>
        </div>
      ))}
      {booked.length > 0 ? (
        <DataTable rows={booked} columns={[
          { key: 'number', header: 'Journal' }, { key: 'journalDate', header: 'Date', render: (j) => date(j.journalDate) },
          { key: 'sourceType', header: 'Source', render: (j) => (String(j.sourceType).endsWith('paid') ? 'Payment' : 'Payroll') },
          { key: 'total', header: 'Total', align: 'right', render: num('total') }, { key: 'status', header: 'Status', render: pill('status') },
        ]} actions={() => <Link className="oc-btn oc-btn-text oc-btn-sm" to="/accounting/general-ledger">General Ledger</Link>} />
      ) : !failed.length && (
        <p className="oc-muted oc-small" style={{ margin: 0 }}>
          {status === 'posted' || status === 'paid' ? 'The payroll journal is booked by Accounting from the posting event; it appears here once processed.'
            : 'Post the approved run to send the payroll journal to Finance & Accounting.'}
        </p>
      )}
    </Card>
  );
}

function SlipLines({ slip }: { slip: R }) {
  const lines = (slip.lines as R[] | undefined) ?? [];
  const rows = lines.map((l, i) => ({ ...l, id: String(i) } as R));
  return (
    <div className="oc-stack">
      <DataTable rows={rows} columns={[
        { key: 'name', header: 'Component', render: (l) => `${String(l.name)}${l.description ? ` · ${String(l.description)}` : ''}` },
        { key: 'kind', header: 'Kind', render: (l) => label(l.kind) }, { key: 'amount', header: 'Amount', align: 'right', render: num('amount') },
      ]} />
      <KV items={[['Gross', money(slip.gross)], ['BPJS employee', money(slip.bpjsEmployee)], ['PPh 21', money(slip.pph21)],
        ['Net pay', <strong key="net">{money(slip.net)}</strong>]]} />
    </div>
  );
}

function RunPayslips({ runId }: { runId: string }) {
  const [q, setQ] = useState('');
  const [check, setCheck] = useState('');
  const [open, setOpen] = useState('');
  const list = useGet<Page<R>>(`${HR}/payroll-runs/${runId}/payslips${q ? `?q=${encodeURIComponent(q)}` : ''}`);
  const slip = useGet<R>(open ? `${HR}/payslips/${open}` : null);
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap">
        <SearchBox value={q} onChange={setQ} placeholder="Search employee name or number" />
        <FilterPills options={[{ value: '', label: 'All' }, { value: 'warning', label: 'With warnings' }]} value={check} onChange={setCheck} />
      </div>
      <DataTable rows={check ? list.data?.items.filter((r) => r.status === check) : list.data?.items} loading={list.isLoading} error={list.error} onRowClick={(r) => setOpen(r.id)} columns={[
        { key: 'employeeNo', header: 'No.' }, { key: 'fullName', header: 'Employee' }, { key: 'orgUnitName', header: 'Department', render: (r) => val(r.orgUnitName) },
        { key: 'gross', header: 'Gross', align: 'right', render: num('gross') }, { key: 'bpjsEmployee', header: 'BPJS', align: 'right', render: num('bpjsEmployee') },
        { key: 'pph21', header: 'PPh 21', align: 'right', render: num('pph21') }, { key: 'net', header: 'Net', align: 'right', render: num('net') },
        { key: 'status', header: 'Check', render: (r) => (r.status === 'warning' ? <span title={((r.messages as string[]) ?? []).join('\n')}><StatusPill status="warning" label="Warning" /></span> : '') },
      ]} />
      {open && (
        <Modal open wide onClose={() => setOpen('')} title={slip.data ? `${String(slip.data.fullName)} · ${String(slip.data.periodCode)}` : 'Payslip'}
          actions={<Dl label="PDF" icon="picture_as_pdf" path={`${HR}/payslips/${open}/pdf`} file={`payslip-${open}.pdf`} />}>
          {slip.isLoading && <Skeleton rows={6} />}
          <ErrorAlert error={slip.error} />
          {slip.data && (
            <div className="oc-stack">
              {((slip.data.messages as string[]) ?? []).map((m) => <div key={m} className="oc-alert oc-alert-warning">{m}</div>)}
              <KV items={[['PTKP / TER', `${val(slip.data.ptkpStatus)} · ${val(slip.data.terCategory)} ${val(slip.data.terRate)}% (${label(slip.data.taxMethod)})`],
                ['Proration', val(slip.data.prorationFactor)], ['Bank', `${val(slip.data.bankName)} ${val(slip.data.accountNo)}`]]} />
              <SlipLines slip={slip.data} />
            </div>
          )}
        </Modal>
      )}
    </div>
  );
}

function Comparison({ runId }: { runId: string }) {
  const c = useGet<R>(`${HR}/payroll-runs/${runId}/comparison`);
  if (c.isLoading) return <Skeleton rows={6} />;
  if (c.error || !c.data) return <ErrorAlert error={c.error} />;
  const rows = ((c.data.rows as R[] | undefined) ?? []).map((r) => ({ ...r, id: String(r.employeeId) } as R));
  return (
    <div className="oc-stack">
      <KV items={[['Previous run', c.data.previousNumber ? `${String(c.data.previousNumber)} (${String(c.data.previousPeriod)})` : 'none'],
        ['Net pay', `${money(c.data.net)} (previous ${money(c.data.previousNet)})`], ['Employees', `${String(c.data.headcount)} (previous ${String(c.data.previousHeadcount)})`]]} />
      <DataTable rows={rows} columns={[
        { key: 'fullName', header: 'Employee' }, { key: 'previousNet', header: 'Previous net', align: 'right', render: num('previousNet') },
        { key: 'net', header: 'Net', align: 'right', render: num('net') }, { key: 'netDiff', header: 'Difference', align: 'right', render: num('netDiff') },
        { key: 'changePercent', header: '%', align: 'right', render: (r) => val(r.changePercent) }, { key: 'flag', header: 'Flag', render: pill('flag') },
      ]} />
    </div>
  );
}

function Journal({ runId }: { runId: string }) {
  const j = useGet<Page<R>>(`${HR}/payroll-runs/${runId}/journal`);
  const rows = (j.data?.items ?? []).map((r, i) => ({ ...r, id: String(i) } as R));
  return (
    <DataTable rows={rows} loading={j.isLoading} error={j.error} columns={[
      { key: 'part', header: 'Part', render: (r) => label(r.part) }, { key: 'componentName', header: 'Component' },
      { key: 'orgUnitCode', header: 'Department', render: (r) => val(r.orgUnitCode) }, { key: 'debitRole', header: 'Debit', render: (r) => label(r.debitRole) },
      { key: 'creditRole', header: 'Credit', render: (r) => label(r.creditRole) }, { key: 'amount', header: 'Amount', align: 'right', render: num('amount') },
    ]} />
  );
}

function Parallel({ runId, run }: { runId: string; run: R }) {
  const { can } = useAuth();
  const p = useGet<R>(`${HR}/payroll-runs/${runId}/parallel-run`);
  if (p.isLoading) return <Skeleton rows={6} />;
  if (p.error || !p.data) return <ErrorAlert error={p.error} />;
  const rows = ((p.data.rows as R[] | undefined) ?? []).map((r, i) => ({ ...r, id: String(i) } as R));
  return (
    <div className="oc-stack">
      <KV items={[['Employees compared', String(p.data.employees)], ['Matched', String(p.data.matched)], ['With differences', String(p.data.withDifferences)],
        ['Not in the legacy file', String(p.data.missingInLegacy)], ['Not in OneClub', String(p.data.missingInOneClub)],
        ['Net OneClub / legacy', `${money(p.data.netOneClub)} / ${money(p.data.netLegacy)}`],
        ['Signed off', ((p.data.signOffs as R[] | undefined) ?? []).map((s) => `${label(s.capacity)}: ${String(s.userName)}`).join(', ') || '—']]} />
      {Number(p.data.employees) === 0 && <Empty title="No legacy payroll" help={`Import the legacy payroll of ${String(run.periodCode)} under Exports & Imports.`} icon="upload_file" />}
      <DataTable rows={rows} columns={[
        { key: 'fullName', header: 'Employee' }, { key: 'componentCode', header: 'Component' }, { key: 'oneClub', header: 'OneClub', align: 'right', render: num('oneClub') },
        { key: 'legacy', header: 'Legacy', align: 'right', render: num('legacy') }, { key: 'difference', header: 'Difference', align: 'right', render: num('difference') },
      ]} />
      {can('hris.payroll_run.sign_off') && Number(p.data.employees) > 0 && (
        <div className="oc-row-wrap">
          <Act label="Sign off as HR Manager" path={`${HR}/payroll-runs/${runId}:sign-off-parallel-run`} body={{ capacity: 'hr_manager' }} note={Number(p.data.withDifferences) ? 'required' : 'optional'} />
          <Act label="Sign off as Finance Manager" path={`${HR}/payroll-runs/${runId}:sign-off-parallel-run`} body={{ capacity: 'finance_manager' }} note={Number(p.data.withDifferences) ? 'required' : 'optional'} />
        </div>
      )}
    </div>
  );
}

function AdjustmentList() {
  const employees = useEmployees();
  const cycles = useOptions(`${HR}/review-cycles?limit=100`, (x) => `${String(x.name)} (${String(x.code)})`);
  const [status, setStatus] = useState('');
  const [open, setOpen] = useState('');
  const [f, setF] = useState<Record<string, string>>({ periodCode: thisPeriod(), componentCode: 'BONUS', targetRunType: 'regular' });
  const list = useGet<Page<R>>(`${HR}/payroll-adjustments${status ? `?status=${status}` : ''}`);
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap">
        <FilterPills options={[{ value: '', label: 'All' }, ...opts(['submitted', 'approved', 'rejected', 'cancelled', 'paid'])]} value={status} onChange={setStatus} />
        <span className="oc-spacer" />
        <button className="oc-btn oc-btn-neutral" onClick={() => setOpen('reviews')}>Bonuses from a review cycle</button>
        <button className="oc-btn oc-btn-ink" onClick={() => setOpen('new')}><Icon name="add" size={18} /> New adjustment</button>
      </div>
      <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error} columns={[
        { key: 'number', header: 'No.' }, { key: 'employeeName', header: 'Employee' }, { key: 'componentName', header: 'Component' },
        { key: 'amount', header: 'Amount', align: 'right', render: num('amount') }, { key: 'periodCode', header: 'Period' },
        { key: 'targetRunType', header: 'Run', render: (r) => label(r.targetRunType) }, { key: 'reason', header: 'Reason' },
        { key: 'status', header: 'Status', render: pill('status') }, { key: 'runNumber', header: 'Paid by', render: (r) => val(r.runNumber) },
      ]} actions={(r) => (['submitted', 'approved'].includes(String(r.status)) && !r.runNumber ? <Act label="Cancel" path={`${HR}/payroll-adjustments/${r.id}:cancel`} note="required" kind="text" /> : null)} />
      {open === 'new' && (
        <FormModal title="New Payroll Adjustment" path={`${HR}/payroll-adjustments`} onClose={() => setOpen('')}
          body={() => clean({ employeeId: f.employeeId, componentCode: f.componentCode, amount: f.amount, periodCode: f.periodCode, targetRunType: f.targetRunType, reason: f.reason })}>
          <SelectField label="Employee" value={f.employeeId ?? ''} onChange={(v) => setF({ ...f, employeeId: v })} options={employees} required span />
          <TextField label="Component code" value={f.componentCode ?? ''} onChange={(v) => setF({ ...f, componentCode: v.toUpperCase() })} required />
          <MoneyField label="Amount" value={f.amount ?? ''} onChange={(v) => setF({ ...f, amount: v })} required />
          <TextField label="Period (YYYY-MM)" value={f.periodCode ?? ''} onChange={(v) => setF({ ...f, periodCode: v })} required />
          <SelectField label="Paid by run" value={f.targetRunType ?? ''} onChange={(v) => setF({ ...f, targetRunType: v })} options={opts(['regular', 'bonus', 'adjustment', 'final_settlement'])} />
          <TextField label="Reason" value={f.reason ?? ''} onChange={(v) => setF({ ...f, reason: v })} required span />
        </FormModal>
      )}
      {open === 'reviews' && (
        <FormModal title="Bonuses from Reviews" path={`${HR}/payroll-adjustments:from-reviews`} submit="Request bonuses" onClose={() => setOpen('')}
          body={() => clean({ cycleId: f.cycleId, periodCode: f.periodCode, targetRunType: 'bonus' })}>
          <SelectField label="Review cycle (closed)" value={f.cycleId ?? ''} onChange={(v) => setF({ ...f, cycleId: v })} options={cycles} required span />
          <TextField label="Payment period (YYYY-MM)" value={f.periodCode ?? ''} onChange={(v) => setF({ ...f, periodCode: v })} required
            help="Bonus = bonus months of the review × fixed wage; each bonus goes through the approval workflow." />
        </FormModal>
      )}
    </div>
  );
}

type Line = { componentCode: string; amount: string; method?: string };

function StructureList() {
  const { can } = useAuth();
  const employees = useEmployees();
  const grades = useOptions(`${HR}/grades?limit=50`, (x) => `${String(x.code)} · ${String(x.name)}`);
  const positions = useOptions(`${HR}/positions?limit=500`, (x) => `${String(x.name)} (${String(x.code)})`);
  const [scope, setScope] = useState('');
  const [open, setOpen] = useState<R | null>(null);
  const [f, setF] = useState<Record<string, string>>({ scope: 'grade', effectiveFrom: today() });
  const [lines, setLines] = useState<Line[]>([{ componentCode: 'MEAL', amount: '' }]);
  const [revise, setRevise] = useState('');
  const [rev, setRev] = useState(today());
  const list = useGet<Page<R>>(`${HR}/salary-structures${scope ? `?scope=${scope}` : ''}`);
  const target = (r: R) => val(r.gradeCode ?? r.positionName ?? r.employeeName);
  const edit = (r: R | null) => {
    setOpen(r ?? ({ id: '' } as R));
    setF(r ? { name: String(r.name), effectiveFrom: String(r.effectiveFrom).slice(0, 10) } : { scope: 'grade', effectiveFrom: today() });
    setLines(r ? ((r.lines as Line[]) ?? []) : [{ componentCode: 'MEAL', amount: '' }]);
  };
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap">
        <FilterPills options={[{ value: '', label: 'All' }, ...opts(['grade', 'position', 'employee'])]} value={scope} onChange={setScope} />
        <span className="oc-spacer" />
        {can('hris.salary_structure.manage') && <button className="oc-btn oc-btn-ink" onClick={() => edit(null)}><Icon name="add" size={18} /> New structure</button>}
      </div>
      <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error} columns={[
        { key: 'code', header: 'Code', render: (r) => `${String(r.code)} v${String(r.version)}` }, { key: 'name', header: 'Name' },
        { key: 'scope', header: 'Scope', render: (r) => `${label(r.scope)} · ${target(r)}` }, { key: 'effectiveFrom', header: 'From', render: (r) => date(r.effectiveFrom) },
        { key: 'lines', header: 'Components', render: (r) => ((r.lines as Line[]) ?? []).map((l) => `${l.componentCode} ${l.amount}`).join(', ') },
        { key: 'status', header: 'Status', render: (r) => <span><StatusPill status={String(r.status)} label={label(r.status)} />{r.inForce ? ' in force' : ''}</span> },
      ]} actions={(r) => (
        <div className="oc-row" style={{ gap: 4 }}>
          {r.status === 'draft' && can('hris.salary_structure.manage') && <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => edit(r)}>Edit</button>}
          {r.status === 'draft' && can('hris.salary_structure.activate') && <Act label="Activate" path={`${HR}/salary-structures/${r.id}:activate`} note="optional" kind="text" />}
          {r.status === 'active' && can('hris.salary_structure.manage') && <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => setRevise(r.id)}>Revise</button>}
          {r.status !== 'inactive' && can('hris.salary_structure.activate') && <Act label="Deactivate" path={`${HR}/salary-structures/${r.id}:deactivate`} note="required" kind="text" />}
        </div>
      )} />
      <p className="oc-small oc-muted">A structure is activated by another person than its last editor (two-person review of payroll rules). Basic salary and fixed allowances of the employment contract take precedence.</p>
      {open && (
        <FormModal wide title={open.id ? 'Edit Salary Structure' : 'New Salary Structure'} method={open.id ? 'PATCH' : 'POST'}
          path={open.id ? `${HR}/salary-structures/${open.id}` : `${HR}/salary-structures`} onClose={() => setOpen(null)}
          body={() => clean({ code: open.id ? undefined : f.code, name: f.name, scope: open.id ? undefined : f.scope, effectiveFrom: f.effectiveFrom,
            gradeId: !open.id && f.scope === 'grade' ? f.target : undefined, positionId: !open.id && f.scope === 'position' ? f.target : undefined,
            employeeId: !open.id && f.scope === 'employee' ? f.target : undefined, lines: lines.filter((l) => l.componentCode && l.amount).map((l) => clean({ ...l })) })}>
          {!open.id && <TextField label="Code" value={f.code ?? ''} onChange={(v) => setF({ ...f, code: v.toUpperCase() })} required />}
          <TextField label="Name" value={f.name ?? ''} onChange={(v) => setF({ ...f, name: v })} required />
          {!open.id && <SelectField label="Scope" value={f.scope ?? ''} onChange={(v) => setF({ ...f, scope: v, target: '' })} options={opts(['grade', 'position', 'employee'])} required />}
          {!open.id && <SelectField label={label(f.scope)} value={f.target ?? ''} onChange={(v) => setF({ ...f, target: v })}
            options={f.scope === 'grade' ? grades : f.scope === 'position' ? positions : employees} required />}
          <TextField label="Effective from" type="date" value={f.effectiveFrom ?? ''} onChange={(v) => setF({ ...f, effectiveFrom: v })} required />
          {lines.map((l, i) => (
            <React.Fragment key={i}>
              <TextField label={`Component ${i + 1}`} value={l.componentCode} onChange={(v) => setLines(lines.map((x, j) => (j === i ? { ...x, componentCode: v.toUpperCase() } : x)))} />
              <TextField label="Amount / rate / %" inputMode="decimal" value={l.amount} onChange={(v) => setLines(lines.map((x, j) => (j === i ? { ...x, amount: v } : x)))} />
            </React.Fragment>
          ))}
          <button className="oc-btn oc-btn-text" onClick={() => setLines([...lines, { componentCode: '', amount: '' }])}><Icon name="add" size={18} /> Component</button>
        </FormModal>
      )}
      {revise && (
        <FormModal title="Revise Structure" path={`${HR}/salary-structures/${revise}:revise`} onClose={() => setRevise('')} body={() => ({ effectiveFrom: rev })}>
          <TextField label="New version effective from" type="date" value={rev} onChange={setRev} required />
        </FormModal>
      )}
    </div>
  );
}

function Components() {
  const { can } = useAuth();
  return (
    <div className="oc-stack">
      {can('hris.pay_component.create') && (
        <div className="oc-row-wrap"><span className="oc-spacer" /><Act label="Load default components" path={`${HR}/pay-components:load-defaults`} /></div>
      )}
      <AutoResourcePage resourceKey="hris.pay_component" />
    </div>
  );
}

function RateList() {
  const { can } = useAuth();
  const list = useGet<Page<R>>(`${HR}/statutory-rates`);
  const [view, setView] = useState<R | null>(null);
  const [json, setJson] = useState('');
  const [f, setF] = useState<Record<string, string>>({});
  const [creating, setCreating] = useState(false);
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap">
        <p className="oc-small oc-muted" style={{ margin: 0 }}>PTKP, PPh 21 TER (PP 58/2023) and Article 17 rates, final tax on severance and BPJS rates with wage caps, versioned by effective date. The set in force on the last day of a period applies.</p>
        <span className="oc-spacer" />
        {can('hris.statutory_rate.manage') && <button className="oc-btn oc-btn-ink" onClick={() => setCreating(true)}><Icon name="add" size={18} /> New version</button>}
      </div>
      <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error} onRowClick={(r) => { setView(r); setJson(JSON.stringify(r.rates, null, 2)); }} columns={[
        { key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'effectiveFrom', header: 'From', render: (r) => date(r.effectiveFrom) },
        { key: 'status', header: 'Status', render: (r) => <span><StatusPill status={String(r.status)} label={label(r.status)} />{r.inForce ? ' in force' : ''}</span> },
        { key: 'verificationStatus', header: 'Tax consultant', render: (r) => (r.verificationStatus === 'verified' ? 'Verified' : <StatusPill status="warning" label="To be verified" />) },
      ]} actions={(r) => (
        <div className="oc-row" style={{ gap: 4 }}>
          {r.status === 'draft' && can('hris.statutory_rate.activate') && <Act label="Activate" path={`${HR}/statutory-rates/${r.id}:activate`} note="optional" kind="text" />}
          {r.verificationStatus !== 'verified' && can('hris.statutory_rate.verify') && <Act label="Verify" path={`${HR}/statutory-rates/${r.id}:verify`} note="required" kind="text" />}
          {r.status !== 'inactive' && can('hris.statutory_rate.activate') && <Act label="Deactivate" path={`${HR}/statutory-rates/${r.id}:deactivate`} note="required" kind="text" />}
        </div>
      )} />
      {view && (
        view.status === 'draft' && can('hris.statutory_rate.manage') ? (
          <FormModal wide title={`Edit ${String(view.code)}`} method="PATCH" path={`${HR}/statutory-rates/${view.id}`} onClose={() => setView(null)}
            body={() => { try { return { rates: JSON.parse(json) as unknown }; } catch { return { rates: null }; } }}>
            <TextArea label="Rates (JSON)" value={json} onChange={setJson} rows={20} span />
          </FormModal>
        ) : (
          <Modal open wide onClose={() => setView(null)} title={`${String(view.code)} · ${String(view.name)}`}>
            <RateSummary rates={view.rates as R} />
          </Modal>
        )
      )}
      {creating && (
        <FormModal title="New Statutory Rate Set" path={`${HR}/statutory-rates`} onClose={() => setCreating(false)}
          body={() => clean({ code: f.code, name: f.name, effectiveFrom: f.effectiveFrom, regulation: f.regulation })}>
          <TextField label="Code" value={f.code ?? ''} onChange={(v) => setF({ ...f, code: v.toUpperCase() })} required help="A copy of the set in force; edit it before activation." />
          <TextField label="Name" value={f.name ?? ''} onChange={(v) => setF({ ...f, name: v })} required />
          <TextField label="Effective from" type="date" value={f.effectiveFrom ?? ''} onChange={(v) => setF({ ...f, effectiveFrom: v })} required />
          <TextField label="Regulation" value={f.regulation ?? ''} onChange={(v) => setF({ ...f, regulation: v })} />
        </FormModal>
      )}
    </div>
  );
}

function RateSummary({ rates }: { rates: R }) {
  const bpjs = (rates?.bpjs ?? {}) as Record<string, Record<string, string>>;
  const rows = ['kesehatan', 'jht', 'jp', 'jkk', 'jkm'].map((p) => ({ id: p, programme: p.toUpperCase(), ...(bpjs[p] ?? {}) } as R));
  const ptkp = Object.entries((rates?.ptkp ?? {}) as Record<string, string>).map(([k, v]) => ({ id: k, status: k, amount: v,
    category: ((rates?.terCategories ?? {}) as Record<string, string>)[k] } as R));
  return (
    <div className="oc-stack">
      <DataTable rows={rows} columns={[{ key: 'programme', header: 'BPJS' }, { key: 'employerPercent', header: 'Employer %', align: 'right' },
        { key: 'employeePercent', header: 'Employee %', align: 'right' }, { key: 'wageCap', header: 'Wage cap', align: 'right', render: (r) => (r.wageCap ? money(r.wageCap) : 'none') }]} />
      <DataTable rows={ptkp} columns={[{ key: 'status', header: 'PTKP status' }, { key: 'amount', header: 'PTKP per year', align: 'right', render: num('amount') },
        { key: 'category', header: 'TER category' }]} />
    </div>
  );
}

function ExportsImports() {
  const { can } = useAuth();
  const toast = useToast();
  const [period, setPeriod] = useState(thisPeriod());
  const [year, setYear] = useState(today().slice(0, 4));
  const [kind, setKind] = useState('ytd');
  const [csv, setCsv] = useState('');
  const [dry, setDry] = useState(true);
  const [legacyPeriod, setLegacyPeriod] = useState(thisPeriod());
  const imp = useSend<Record<string, unknown>, R>('POST', `${HR}/payroll-imports`, INV);
  const rep = imp.data;
  return (
    <div className="oc-stack">
      {can('hris.payroll_run.export') && (
        <Card title="Statutory exports" icon="download">
          <div className="oc-row-wrap">
            <TextField label="Period (YYYY-MM)" value={period} onChange={setPeriod} />
            <Dl label="e-Bupot PPh 21" path={`${HR}/payroll-exports/e-bupot?periodCode=${period}`} file={`e-bupot-${period}.csv`} />
            <Dl label="BPJS Kesehatan" path={`${HR}/payroll-exports/bpjs-kesehatan?periodCode=${period}`} file={`bpjs-kesehatan-${period}.csv`} />
            <Dl label="BPJS Ketenagakerjaan" path={`${HR}/payroll-exports/bpjs-ketenagakerjaan?periodCode=${period}`} file={`bpjs-ketenagakerjaan-${period}.csv`} />
            <TextField label="Tax year" value={year} onChange={setYear} />
            <Dl label="1721-A1" path={`${HR}/payroll-exports/1721-a1?year=${year}`} file={`1721-A1-${year}.csv`} />
          </div>
        </Card>
      )}
      {can('hris.payroll_import.create') && (
        <Card title="Migration imports" icon="upload_file">
          <div className="oc-form">
            <SelectField label="File" value={kind} onChange={setKind} options={[{ value: 'ytd', label: 'Opening year-to-date payroll' }, { value: 'legacy', label: 'Legacy payroll (parallel run)' }]} />
            {kind === 'legacy' && <TextField label="Period (YYYY-MM)" value={legacyPeriod} onChange={setLegacyPeriod} />}
            <TextArea span rows={8} label="CSV" value={csv} onChange={setCsv}
              help={kind === 'ytd' ? 'employeeNo, taxYear, throughMonth, gross, deductible, pph21, monthsWorked, legacyRef' : 'employeeNo, componentCode (a component or GROSS, BPJS_EE, PPH21, NET), amount, periodCode, batchRef'} />
            <input type="file" accept=".csv,text/csv" aria-label="CSV file" onChange={(e) => { const file = e.target.files?.[0]; if (file) void file.text().then(setCsv); }} />
            <Checkbox label="Dry run (validate only)" checked={dry} onChange={setDry} />
          </div>
          <ErrorAlert error={imp.error} />
          <button className="oc-btn oc-btn-ink" disabled={imp.isPending || !csv.trim()} onClick={() => imp.mutate(clean({ kind, csv, dryRun: dry, periodCode: kind === 'legacy' ? legacyPeriod : undefined }),
            { onSuccess: () => toast('Import: done') })}>Import</button>
          {rep && (
            <div className="oc-stack">
              <KV items={[['Rows', String(rep.rows)], ['Inserted', String(rep.inserted)], ['Updated', String(rep.updated)], ['Failed', String(rep.failed)], ['Dry run', rep.dryRun ? 'yes' : 'no']]} />
              {((rep.issues as R[]) ?? []).map((i) => <div key={`${String(i.row)}`} className="oc-small">Row {String(i.row)} {String(i.key)}: {String(i.message)}</div>)}
            </div>
          )}
        </Card>
      )}
      <YTD year={year} />
    </div>
  );
}

function YTD({ year }: { year: string }) {
  const y = useGet<Page<R>>(/^\d{4}$/.test(year) ? `${HR}/payroll-ytd?year=${year}` : null);
  const rows = (y.data?.items ?? []).map((r) => ({ ...r, id: String(r.employeeId) } as R));
  return (
    <Card title={`Year to date ${year}`} icon="calculate">
      <DataTable rows={rows} loading={y.isLoading} error={y.error} columns={[
        { key: 'fullName', header: 'Employee' }, { key: 'openingGross', header: 'Opening gross', align: 'right', render: num('openingGross') },
        { key: 'gross', header: 'OneClub gross', align: 'right', render: num('gross') }, { key: 'totalGross', header: 'Total gross', align: 'right', render: num('totalGross') },
        { key: 'totalPph21', header: 'PPh 21', align: 'right', render: num('totalPph21') }, { key: 'net', header: 'Net paid', align: 'right', render: num('net') },
      ]} />
    </Card>
  );
}

// ── HRIS → Benefits ───────────────────────────────────────────────────────

export function BenefitsPage() {
  const [issues, setIssues] = useState(false);
  const list = useGet<Page<R>>(`${HR}/payroll-profiles${issues ? '?issues=true' : ''}`);
  const rates = useGet<Page<R>>(`${HR}/statutory-rates`);
  const inForce = (rates.data?.items ?? []).find((r) => r.inForce);
  return (
    <div className="oc-stack">
      <PageHeader title="Benefits" help="PTKP status and TER category, NPWP / NIK, BPJS Kesehatan & Ketenagakerjaan enrolment and the salary account of every employee — the data payroll needs. Edit them on the employee profile." />
      {inForce && (
        <Card title={`BPJS & PPh 21 rates in force: ${String(inForce.code)}`} icon="receipt_long">
          {inForce.verificationStatus !== 'verified' && <div className="oc-alert oc-alert-warning">To be verified by the tax consultant before go-live.</div>}
          <RateSummary rates={inForce.rates as R} />
        </Card>
      )}
      <Checkbox label="Only employees with missing data" checked={issues} onChange={setIssues} />
      <DataTable rows={(list.data?.items ?? []).map((r) => ({ ...r, id: String(r.employeeId) } as R))} loading={list.isLoading} error={list.error} columns={[
        { key: 'employeeNo', header: 'No.' }, { key: 'fullName', header: 'Employee' }, { key: 'orgUnitName', header: 'Department', render: (r) => val(r.orgUnitName) },
        { key: 'ptkpStatus', header: 'PTKP', render: (r) => `${val(r.ptkpStatus)} · TER ${String(r.terCategory)}` }, { key: 'npwp', header: 'NPWP', render: (r) => val(r.npwp) },
        { key: 'bpjsKesehatanNo', header: 'BPJS Kesehatan', render: (r) => val(r.bpjsKesehatanNo) },
        { key: 'bpjsKetenagakerjaanNo', header: 'BPJS TK', render: (r) => (r.bpjsEnrolled ? val(r.bpjsKetenagakerjaanNo) : 'not enrolled') },
        { key: 'accountNo', header: 'Bank', render: (r) => `${val(r.bankName)} ${val(r.accountNo)}` },
        { key: 'issues', header: 'Issues', render: (r) => ((r.issues as string[]) ?? []).join('; ') || '—' },
      ]} />
    </div>
  );
}

// ── Employee Self Service → Payslip (FR-ESS-04, FR-PPY-03) ─────────────────

function EssPayslip({ base }: { base: string }) {
  const toast = useToast();
  const [open, setOpen] = useState('');
  const list = useGet<Page<R>>(`${ESS}/payslips`);
  const slip = useGet<R>(open ? `${ESS}/payslips/${open}` : null);
  return (
    <div className="oc-stack">
      <div className="oc-row" style={{ gap: 8 }}>
        <Link className="oc-btn oc-btn-text" to={base} aria-label="Back to Employee Self Service"><Icon name="arrow_back" size={22} /></Link>
        <h1 style={{ margin: 0, fontSize: 24 }}>Payslip</h1>
      </div>
      {!list.isLoading && !list.error && (list.data?.items ?? []).length === 0 && <Empty title="No payslip yet" help="Your payslips appear here once payroll is posted." icon="receipt_long" />}
      <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error} onRowClick={(r) => setOpen(r.id)} columns={[
        { key: 'periodCode', header: 'Period' }, { key: 'runName', header: 'Payroll' }, { key: 'paymentDate', header: 'Paid on', render: (r) => date(r.paymentDate) },
        { key: 'net', header: 'Net pay', align: 'right', render: num('net') },
      ]} actions={(r) => (
        <button className="oc-btn oc-btn-neutral" style={{ minHeight: 44 }} aria-label={`PDF of ${String(r.periodCode)}`}
          onClick={() => download('GET', `${ESS}/payslips/${r.id}/pdf`, undefined, `payslip-${String(r.periodCode)}.pdf`).catch((e: Error) => toast(e.message, 'error'))}>
          <Icon name="picture_as_pdf" size={20} /> PDF
        </button>
      )} />
      {open && (
        <Modal open wide onClose={() => setOpen('')} title={slip.data ? `Payslip ${String(slip.data.periodCode)}` : 'Payslip'}>
          {slip.isLoading && <Skeleton rows={6} />}
          <ErrorAlert error={slip.error} />
          {slip.data && <SlipLines slip={slip.data} />}
        </Modal>
      )}
    </div>
  );
}

registerEssSection('payslip', ({ base }) => <EssPayslip base={base} />);

// ── registrations ─────────────────────────────────────────────────────────

export const PAYROLL_ROUTES: AreaRoute[] = [
  { path: 'hris/payroll', perm: 'hris.payroll_run.view', element: <PayrollPage /> },
  { path: 'hris/payroll/runs/:id', perm: 'hris.payroll_run.view', element: <PayrollRunPage /> },
  { path: 'hris/benefits', perm: 'hris.payroll_profile.view', element: <BenefitsPage /> },
  ...PAYOUTS_ROUTES,
];
export const PAYROLL_OPS_TILES: OpsTile[] = [...PAYOUTS_OPS_TILES];
export const PAYROLL_OPS_ROUTES: OpsRoute[] = [...PAYOUTS_OPS_ROUTES];

// ── Payroll exception queue (HRIS phase B, spec §35) ─────────────────────

// Where each exception is fixed: the employee workspace, attendance or the runs.
const EXCEPTION_FIX: Record<string, { text: string; to: (x: R, run: string) => string }> = {
  negative_net: { text: 'Payslip & adjustments', to: (x) => `/hris/employees/${String(x.employeeId)}?tab=payroll` },
  duplicate_payroll: { text: 'Payroll runs', to: () => '/hris/payroll?tab=runs' },
  missing_salary: { text: 'Contract', to: (x) => `/hris/employees/${String(x.employeeId)}` },
  missing_contract: { text: 'Contract', to: (x) => `/hris/employees/${String(x.employeeId)}` },
  missing_tax_profile: { text: 'Employee data', to: (x) => `/hris/employees/${String(x.employeeId)}` },
  missing_tax_id: { text: 'Employee data', to: (x) => `/hris/employees/${String(x.employeeId)}` },
  missing_bpjs: { text: 'Employee data', to: (x) => `/hris/employees/${String(x.employeeId)}` },
  missing_bank: { text: 'Bank account', to: (x) => `/hris/employees/${String(x.employeeId)}` },
  attendance_exception: { text: 'Attendance', to: (x) => `/hris/employees/${String(x.employeeId)}?tab=attendance` },
  employee_suspended: { text: 'Employee', to: (x) => `/hris/employees/${String(x.employeeId)}` },
};

/**
 * Exceptions of a calculated run, one row per employee: what is wrong
 * (blocking first) and where to fix it, each place once.
 */
function RunExceptions({ runId, items, canFix }: { runId: string; items: R[]; canFix: boolean }) {
  const [sev, setSev] = useState('');
  const byEmployee = new Map<string, R[]>();
  for (const x of items) {
    if (sev && x.severity !== sev) continue;
    const k = String(x.employeeId ?? x.slipId);
    byEmployee.set(k, [...(byEmployee.get(k) ?? []), x]);
  }
  const rows = [...byEmployee.entries()].map(([k, list]) => ({
    id: k, employeeName: list[0].employeeName, employeeNo: list[0].employeeNo, blocking: list.some((x) => x.severity === 'error'),
    list: [...list].sort((a, b) => (a.severity === 'error' ? 0 : 1) - (b.severity === 'error' ? 0 : 1)),
  } as R)).sort((a, b) => Number(b.blocking) - Number(a.blocking));
  const errors = items.filter((x) => x.severity === 'error').length;
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap">
        <FilterPills options={[{ value: '', label: 'All' }, { value: 'error', label: `Blocking · ${errors}` }, { value: 'warning', label: `Warnings · ${items.length - errors}` }]}
          value={sev} onChange={setSev} />
        <span className="oc-spacer" />
        {canFix && <Act label="Revalidate" path={`${HR}/payroll-runs/${runId}:calculate`} kind="neutral" />}
      </div>
      {errors > 0 && <div className="oc-alert oc-alert-error" role="alert">Fix the blocking exceptions and revalidate (recalculate) before submitting the run for approval.</div>}
      <DataTable rows={rows} columns={[
        { key: 'employeeName', header: 'Employee', render: (x) => (
          <div><div>{String(x.employeeName)}</div><div className="oc-small oc-muted">{String(x.employeeNo)}</div></div>
        ) },
        { key: 'list', header: 'Issues', render: (x) => (
          <div className="oc-stack" style={{ gap: 6 }}>
            {(x.list as R[]).map((e) => (
              <div key={String(e.code)} className="oc-row" style={{ gap: 8, alignItems: 'baseline' }}>
                <StatusPill status={e.severity === 'error' ? 'error' : 'pending'} label={e.severity === 'error' ? 'Blocking' : 'Warning'} />
                <span><strong>{label(e.code)}</strong> <span className="oc-muted">· {String(e.message)}</span></span>
              </div>
            ))}
          </div>
        ) },
      ]} actions={(x) => {
        const fixes = new Map<string, Set<string>>();
        for (const e of x.list as R[]) {
          const fix = EXCEPTION_FIX[String(e.code)];
          if (!fix) continue;
          const to = fix.to(e, runId);
          fixes.set(to, (fixes.get(to) ?? new Set<string>()).add(fix.text.toLowerCase()));
        }
        return [...fixes].map(([to, texts]) => <Link key={to} className="oc-btn oc-btn-neutral oc-btn-sm" to={to}>Open {[...texts].join(' & ')}</Link>);
      }} />
    </div>
  );
}
