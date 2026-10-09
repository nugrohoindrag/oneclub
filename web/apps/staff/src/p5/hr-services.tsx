import React, { useState } from 'react';
import { Link } from 'react-router';
import { download, request, uuidv7, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate } from '@oneclub/i18n';
import {
  AutoResourcePage, DataTable, Empty, ErrorAlert, FilterPills, Icon, Modal, MoneyField, PageHeader, SelectField, StatusPill, TextArea, TextField,
  useAuth, useToast, type Option,
} from '@oneclub/shell';
import { KV, Tabs, money, today, type R } from '../p1/common';
import type { AreaRoute } from '../p3/types';
import { registerEssSection, useUrlTab } from './hr';
import { LoansWorkspace } from './hr-loans';

// HRIS improvement phase C (spec §24 Employee Services): Reimbursement — claims with receipts from ESS or HR, approval (Approvals inbox,
// comments and attachments), sent to Finance and paid (journal to the category's expense account) — and Benefits — plans with eligibility
// and contributions, enrollments, the employee contribution deducted by regular payroll. ESS: Reimbursement and My Benefits.

const HR = '/api/v1/hris';
const ESS = '/api/v1/ess';
const INV = [HR, ESS, '/api/v1/platform/approvals'];
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const date = (v: unknown) => (v ? formatDate(String(v).slice(0, 10)) : '—');
const clean = (o: Record<string, unknown>) => Object.fromEntries(Object.entries(o).filter(([, v]) => v !== '' && v !== undefined && v !== null));
const METHODS: Option[] = [{ value: 'bank_transfer', label: 'Bank transfer' }, { value: 'cash', label: 'Cash' }];
const CLAIM_LABELS: Record<string, string> = { submitted: 'Waiting for approval', approved: 'Approved', sent_to_finance: 'Sent to Finance', paid: 'Paid' };
const claimPill = (r: R) => <StatusPill status={String(r.status)} label={CLAIM_LABELS[String(r.status)] ?? label(r.status)} />;

/** A modal form posting a body. */
function Send({ title, path, body, submit, idempotent, onClose, children }: {
  title: string; path: string; body: () => Record<string, unknown>; submit: string; idempotent?: boolean; onClose: () => void; children: React.ReactNode;
}) {
  const toast = useToast();
  const send = useSend<Record<string, unknown>, R>('POST', path, INV, idempotent ? () => ({ 'Idempotency-Key': uuidv7() }) : undefined);
  return (
    <Modal open onClose={onClose} title={title} actions={
      <>
        <button className="oc-btn oc-btn-text" onClick={onClose}>Cancel</button>
        <button className="oc-btn oc-btn-ink" disabled={send.isPending} onClick={() => send.mutate(body(), { onSuccess: () => { toast(`${title}: done`); onClose(); } })}>{submit}</button>
      </>
    }>
      <div className="oc-stack"><ErrorAlert error={send.error} /><div className="oc-form">{children}</div></div>
    </Modal>
  );
}

/** Uploads a receipt and returns its id. */
function ReceiptUpload({ path, value, onChange }: { path: string; value: string; onChange: (id: string) => void }) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [name, setName] = useState('');
  const upload = async (f: File) => {
    setBusy(true);
    setErr(null);
    const fd = new FormData();
    fd.append('file', f);
    try {
      const r = await request<R>('POST', path, fd);
      onChange(String(r.id));
      setName(String(r.filename ?? f.name));
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="oc-stack" style={{ gap: 6, gridColumn: '1 / -1' }}>
      <ErrorAlert error={err} />
      <label className="oc-btn oc-btn-neutral oc-btn-sm" style={{ cursor: busy ? 'not-allowed' : 'pointer', width: 'fit-content', minHeight: 44 }}>
        <Icon name="receipt" size={18} /> {busy ? 'Uploading…' : value ? `Receipt: ${name || 'attached'} (replace)` : 'Attach receipt (photo or PDF)'}
        <input type="file" accept="image/jpeg,image/png,image/webp,application/pdf" capture="environment" className="oc-sr" disabled={busy}
          onChange={(e) => { const f = e.target.files?.[0]; e.target.value = ''; if (f) void upload(f); }} />
      </label>
    </div>
  );
}

function useCategories(): R[] {
  const l = useGet<Page<R>>(`${HR}/reimbursement-categories?limit=200&filter[status]=active`);
  return l.data?.items ?? [];
}

/** Claim form for HR (with the employee) or ESS. */
function ClaimForm({ path, upload, employees, categories, onClose }: { path: string; upload: string; employees?: Option[]; categories: R[]; onClose: () => void }) {
  const [f, setF] = useState<Record<string, string>>({ expenseDate: today() });
  const set = (k: string) => (v: string) => setF({ ...f, [k]: v });
  const cat = categories.find((c) => c.id === f.categoryId);
  return (
    <Send title="Claim an expense" path={path} submit="Submit claim" idempotent onClose={onClose}
      body={() => clean({ employeeId: f.employeeId, categoryId: f.categoryId, expenseDate: f.expenseDate, amount: f.amount, description: f.description,
        fileId: f.fileId })}>
      {employees && <SelectField label="Employee" value={f.employeeId ?? ''} onChange={set('employeeId')} options={employees} required span />}
      <SelectField label="Category" value={f.categoryId ?? ''} onChange={set('categoryId')} required
        options={categories.map((c) => ({ value: String(c.id), label: String(c.name) }))}
        help={cat?.maxAmount ? `At most ${money(cat.maxAmount)} per claim` : undefined} />
      <TextField label="Date of the expense" type="date" value={f.expenseDate} onChange={set('expenseDate')} required />
      <MoneyField label="Amount (IDR)" value={f.amount ?? ''} onChange={set('amount')} required />
      <TextArea label="Description" value={f.description ?? ''} onChange={set('description')} required span />
      <ReceiptUpload path={upload} value={f.fileId ?? ''} onChange={set('fileId')} />
    </Send>
  );
}

function Note({ title, path, required, onClose }: { title: string; path: string; required?: boolean; onClose: () => void }) {
  const [note, setNote] = useState('');
  return (
    <Send title={title} path={path} submit={title} onClose={onClose} body={() => (note ? { note } : {})}>
      <TextArea label={required ? 'Reason' : 'Note (optional)'} value={note} onChange={setNote} required={required} span />
    </Send>
  );
}

const CLAIM_FILTERS: Option[] = [
  { value: '', label: 'All' }, { value: 'submitted', label: 'Waiting for approval' }, { value: 'approved', label: 'To send to Finance' },
  { value: 'sent_to_finance', label: 'To pay' }, { value: 'paid', label: 'Paid' }, { value: 'rejected', label: 'Rejected' }, { value: 'cancelled', label: 'Cancelled' },
];

function Claims() {
  const { can } = useAuth();
  const toast = useToast();
  const [status, setStatus] = useState('');
  const [modal, setModal] = useState<{ kind: 'claim' | 'send' | 'pay' | 'cancel' | 'view'; r?: R } | null>(null);
  const [f, setF] = useState<Record<string, string>>({});
  const list = useGet<Page<R>>(`${HR}/reimbursements${status ? `?status=${status}` : ''}`);
  const categories = useCategories();
  const emps = useGet<Page<R>>(can('hris.reimbursement.manage') ? `${HR}/employees?limit=500&filter[status]=active` : null);
  const employees = (emps.data?.items ?? []).map((x) => ({ value: String(x.id), label: `${String(x.fullName)} (${String(x.employeeNo)})` }));
  const r = modal?.r;
  const base = r ? `${HR}/reimbursements/${String(r.id)}` : '';
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap">
        <FilterPills options={CLAIM_FILTERS} value={status} onChange={setStatus} />
        <span className="oc-spacer" />
        {can('hris.reimbursement.manage') && <button className="oc-btn oc-btn-ink" onClick={() => setModal({ kind: 'claim' })}><Icon name="add" size={18} /> Claim for an employee</button>}
      </div>
      <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error} onRowClick={(x) => setModal({ kind: 'view', r: x })}
        empty={<Empty title="No reimbursement claim" help="Claims from Employee Self Service and HR appear here with their approval and payment." icon="receipt" />}
        columns={[
          { key: 'number', header: 'Claim' }, { key: 'employeeName', header: 'Employee' }, { key: 'categoryName', header: 'Category' },
          { key: 'expenseDate', header: 'Date', render: (x) => date(x.expenseDate) }, { key: 'amount', header: 'Amount', align: 'right', render: (x) => money(x.amount) },
          { key: 'status', header: 'Status', render: claimPill },
        ]}
        actions={(x) => (
          <div className="oc-row" style={{ gap: 4 }}>
            {x.status === 'submitted' && x.approvalRequestId ? <Link className="oc-btn oc-btn-text oc-btn-sm" to={`/approvals/${String(x.approvalRequestId)}`}>Approval</Link> : null}
            {can('hris.reimbursement.manage') && x.status === 'approved' && <button className="oc-btn oc-btn-ink oc-btn-sm" onClick={() => setModal({ kind: 'send', r: x })}>Send to Finance</button>}
            {can('hris.reimbursement.pay') && x.status === 'sent_to_finance' && <button className="oc-btn oc-btn-ink oc-btn-sm" onClick={() => { setF({ paidOn: today(), method: 'bank_transfer' }); setModal({ kind: 'pay', r: x }); }}>Pay</button>}
            {can('hris.reimbursement.manage') && (x.status === 'submitted' || x.status === 'approved') && <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => setModal({ kind: 'cancel', r: x })}>Cancel</button>}
          </div>
        )} />
      {modal?.kind === 'claim' && <ClaimForm path={`${HR}/reimbursements`} upload={`${HR}/reimbursement-files`} employees={employees} categories={categories} onClose={() => setModal(null)} />}
      {modal?.kind === 'send' && r && <Note title="Send to Finance" path={`${base}:send-to-finance`} onClose={() => setModal(null)} />}
      {modal?.kind === 'cancel' && r && <Note title="Cancel claim" path={`${base}:cancel`} required onClose={() => setModal(null)} />}
      {modal?.kind === 'pay' && r && (
        <Send title={`Pay ${String(r.number)}`} path={`${base}:mark-paid`} submit="Record payment" onClose={() => setModal(null)}
          body={() => clean({ paidOn: f.paidOn, method: f.method, reference: f.reference, bankAccountCode: f.bankAccountCode })}>
          <p className="oc-muted oc-small" style={{ margin: 0 }}>{money(r.amount)} to {String(r.employeeName)} for {String(r.categoryName)}. Accounting books the expense of the category against cash or bank.</p>
          <TextField label="Paid on" type="date" value={f.paidOn ?? ''} onChange={(v) => setF({ ...f, paidOn: v })} required />
          <SelectField label="Method" value={f.method ?? ''} onChange={(v) => setF({ ...f, method: v })} options={METHODS} required />
          <TextField label="Reference" value={f.reference ?? ''} onChange={(v) => setF({ ...f, reference: v })} />
          <TextField label="GL cash / bank account code (empty = default)" value={f.bankAccountCode ?? ''} onChange={(v) => setF({ ...f, bankAccountCode: v })} />
        </Send>
      )}
      {modal?.kind === 'view' && r && (
        <Modal open onClose={() => setModal(null)} title={`${String(r.number)} · ${String(r.employeeName)}`}>
          <div className="oc-stack">
            <KV items={[['Category', String(r.categoryName)], ['Date', date(r.expenseDate)], ['Amount', money(r.amount)], ['Description', String(r.description)],
              ['Status', CLAIM_LABELS[String(r.status)] ?? label(r.status)], ['Requested by', r.requestSource === 'ess' ? 'Employee (ESS)' : 'HR'],
              ['Decision', String(r.decisionNote ?? '—')], ['Paid', r.paidOn ? `${date(r.paidOn)} · ${label(r.paymentMethod)} ${String(r.paymentRef ?? '')}` : '—']]} />
            {r.fileId ? (
              <button className="oc-btn oc-btn-neutral oc-btn-sm" style={{ width: 'fit-content' }} onClick={() => download('GET', `${base}/receipt`, undefined,
                String(r.fileName ?? 'receipt')).catch((e: Error) => toast(e.message, 'error'))}><Icon name="download" size={18} /> Receipt</button>
            ) : null}
          </div>
        </Modal>
      )}
    </div>
  );
}

const REIMB_TABS: Option[] = [{ value: 'claims', label: 'Claims' }, { value: 'categories', label: 'Categories' }];

export function ReimbursementPage() {
  const [tab, setTab] = useUrlTab('claims');
  return (
    <div className="oc-stack">
      <PageHeader title="Reimbursement" help="Expense claims of employees with their receipts: approval, sent to Finance, paid. HRIS keeps the claim; Finance settles it." />
      <Tabs tabs={REIMB_TABS} value={tab} onChange={setTab} />
      {tab === 'claims' ? <Claims /> : <AutoResourcePage resourceKey="hris.reimbursement_category" />}
    </div>
  );
}

// ── Loans & Advances ──────────────────────────────────────────────────────

/** Human Resources → Employee Services → Loans & Advances (HRIS phase B, spec §29). */
export function LoansPage() {
  return (
    <div className="oc-stack">
      <PageHeader title="Loans & Advances" help="Loans and cash advances of employees: request (HR or Employee Self Service), approval, paid by Finance, repaid through payroll installments or directly." />
      <LoansWorkspace />
    </div>
  );
}

// ── Benefits ──────────────────────────────────────────────────────────────

function Enrollments() {
  const { can } = useAuth();
  const [status, setStatus] = useState('active');
  const [modal, setModal] = useState<{ kind: 'enroll' | 'end'; r?: R } | null>(null);
  const [f, setF] = useState<Record<string, string>>({});
  const list = useGet<Page<R>>(`${HR}/benefit-enrollments${status ? `?status=${status}` : ''}`);
  const plans = useGet<Page<R>>(`${HR}/benefit-plans?limit=200&filter[status]=active`);
  const planOpts = (plans.data?.items ?? []).map((p) => ({ value: String(p.id), label: `${String(p.name)} (${String(p.code)})` }));
  const elig = useGet<Page<R>>(modal?.kind === 'enroll' && f.planId ? `${HR}/benefit-plans/${f.planId}/eligibility` : null);
  const eligible = (elig.data?.items ?? []).filter((e) => e.eligible && !e.enrolled)
    .map((e) => ({ value: String(e.employeeId), label: `${String(e.employeeName)} (${String(e.employeeNo)})` }));
  const notEligible = (elig.data?.items ?? []).filter((e) => !e.eligible).length;
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap">
        <FilterPills options={[{ value: 'active', label: 'Active' }, { value: 'ended', label: 'Ended' }, { value: '', label: 'All' }]} value={status} onChange={setStatus} />
        <span className="oc-spacer" />
        {can('hris.benefit_enrollment.manage') && <button className="oc-btn oc-btn-ink" onClick={() => { setF({ effectiveFrom: today() }); setModal({ kind: 'enroll' }); }}><Icon name="add" size={18} /> Enroll employee</button>}
      </div>
      <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error}
        empty={<Empty title="No enrollment" help="Enroll eligible employees in a benefit plan; payroll deducts the employee contribution." icon="verified_user" />}
        columns={[
          { key: 'employeeName', header: 'Employee', render: (x) => <Link to={`/hris/employees/${String(x.employeeId)}`}>{String(x.employeeName)}</Link> },
          { key: 'planName', header: 'Plan' }, { key: 'benefitType', header: 'Type', render: (x) => label(x.benefitType) },
          { key: 'effectiveFrom', header: 'From', render: (x) => date(x.effectiveFrom) }, { key: 'effectiveTo', header: 'To', render: (x) => date(x.effectiveTo) },
          { key: 'employerContribution', header: 'Employer / month', align: 'right', render: (x) => money(x.employerContribution) },
          { key: 'employeeContribution', header: 'Employee / month', align: 'right', render: (x) => money(x.employeeContribution) },
          { key: 'deducted', header: 'Deducted', align: 'right', render: (x) => money(x.deducted) },
          { key: 'status', header: 'Status', render: (x) => <StatusPill status={String(x.status)} label={label(x.status)} /> },
        ]}
        actions={(x) => (can('hris.benefit_enrollment.manage') && x.status === 'active'
          ? <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => { setF({ effectiveTo: today() }); setModal({ kind: 'end', r: x }); }}>End</button> : null)} />
      {modal?.kind === 'enroll' && (
        <Send title="Enroll employee" path={`${HR}/benefit-enrollments`} submit="Enroll" idempotent onClose={() => setModal(null)}
          body={() => clean({ planId: f.planId, employeeId: f.employeeId, effectiveFrom: f.effectiveFrom, employerContribution: f.employerContribution,
            employeeContribution: f.employeeContribution, notes: f.notes })}>
          <SelectField label="Plan" value={f.planId ?? ''} onChange={(v) => setF({ ...f, planId: v, employeeId: '' })} options={planOpts} required span />
          <SelectField label="Eligible employee" value={f.employeeId ?? ''} onChange={(v) => setF({ ...f, employeeId: v })} options={eligible} required span
            help={f.planId ? `${eligible.length} eligible and not enrolled${notEligible ? ` · ${notEligible} not eligible` : ''}` : 'Choose the plan first'} />
          <TextField label="Effective from" type="date" value={f.effectiveFrom ?? ''} onChange={(v) => setF({ ...f, effectiveFrom: v })} required />
          <TextField label="Employer contribution (empty = plan)" inputMode="decimal" value={f.employerContribution ?? ''} onChange={(v) => setF({ ...f, employerContribution: v })} />
          <TextField label="Employee contribution (empty = plan)" inputMode="decimal" value={f.employeeContribution ?? ''} onChange={(v) => setF({ ...f, employeeContribution: v })} />
          <TextArea label="Notes" value={f.notes ?? ''} onChange={(v) => setF({ ...f, notes: v })} span />
        </Send>
      )}
      {modal?.kind === 'end' && modal.r && (
        <Send title={`End ${String(modal.r.planName)}`} path={`${HR}/benefit-enrollments/${String(modal.r.id)}:end`} submit="End enrollment" onClose={() => setModal(null)}
          body={() => clean({ effectiveTo: f.effectiveTo, reason: f.reason })}>
          <TextField label="Last covered day" type="date" value={f.effectiveTo ?? ''} onChange={(v) => setF({ ...f, effectiveTo: v })} required />
          <TextArea label="Reason" value={f.reason ?? ''} onChange={(v) => setF({ ...f, reason: v })} required span />
        </Send>
      )}
    </div>
  );
}

const BENEFIT_TABS: Option[] = [{ value: 'enrollments', label: 'Enrollments' }, { value: 'plans', label: 'Plans' }];

export function BenefitPlansPage() {
  const [tab, setTab] = useUrlTab('enrollments');
  return (
    <div className="oc-stack">
      <PageHeader title="Benefits" help="Benefit plans (insurance, pension, allowances, facilities) with eligibility and contributions; enrollments of employees. Regular payroll deducts the employee contribution (BENEFIT_EE)." />
      <Tabs tabs={BENEFIT_TABS} value={tab} onChange={setTab} />
      {tab === 'enrollments' ? <Enrollments /> : <AutoResourcePage resourceKey="hris.benefit_plan" />}
    </div>
  );
}

// ── Employee Self Service ─────────────────────────────────────────────────

function EssBack({ base, title, action }: { base: string; title: string; action?: React.ReactNode }) {
  return (
    <div className="oc-row" style={{ gap: 8 }}>
      <Link className="oc-btn oc-btn-text" to={base} aria-label="Back to Employee Self Service"><Icon name="arrow_back" size={22} /></Link>
      <h1 style={{ margin: 0, fontSize: 24 }}>{title}</h1>
      <span className="oc-spacer" />
      {action}
    </div>
  );
}

function EssReimbursements({ base }: { base: string }) {
  const toast = useToast();
  const [modal, setModal] = useState<{ kind: 'claim' | 'cancel'; id?: string } | null>(null);
  const list = useGet<Page<R>>(`${ESS}/reimbursements`);
  const categories = useCategories();
  const items = list.data?.items ?? [];
  return (
    <div className="oc-stack">
      <EssBack base={base} title="Reimbursement" action={
        <button className="oc-btn oc-btn-ink" style={{ minHeight: 44 }} onClick={() => setModal({ kind: 'claim' })}><Icon name="add" size={18} /> Claim</button>} />
      {!list.isLoading && !list.error && items.length === 0 && <Empty title="No claim yet" help="Claim an expense with a photo of the receipt; HR and Finance approve and pay it." icon="receipt" />}
      {items.length > 0 && (
        <DataTable rows={items} loading={list.isLoading} error={list.error} columns={[
          { key: 'number', header: 'Claim' }, { key: 'categoryName', header: 'Category' }, { key: 'expenseDate', header: 'Date', render: (x) => date(x.expenseDate) },
          { key: 'amount', header: 'Amount', align: 'right', render: (x) => money(x.amount) }, { key: 'status', header: 'Status', render: claimPill },
        ]} actions={(x) => (
          <div className="oc-row" style={{ gap: 4 }}>
            {x.fileId ? <button className="oc-btn oc-btn-text" style={{ minHeight: 44 }} aria-label="Receipt" onClick={() => download('GET', `${ESS}/reimbursements/${String(x.id)}/receipt`,
              undefined, String(x.fileName ?? 'receipt')).catch((e: Error) => toast(e.message, 'error'))}><Icon name="receipt" size={20} /></button> : null}
            {x.status === 'submitted' && <button className="oc-btn oc-btn-text" style={{ minHeight: 44 }} onClick={() => setModal({ kind: 'cancel', id: String(x.id) })}>Withdraw</button>}
          </div>
        )} />
      )}
      {modal?.kind === 'claim' && <ClaimForm path={`${ESS}/reimbursements`} upload={`${ESS}/reimbursement-files`} categories={categories} onClose={() => setModal(null)} />}
      {modal?.kind === 'cancel' && modal.id && <Note title="Withdraw claim" path={`${ESS}/reimbursements/${modal.id}:cancel`} required onClose={() => setModal(null)} />}
    </div>
  );
}

function EssBenefits({ base }: { base: string }) {
  const list = useGet<Page<R>>(`${ESS}/benefits`);
  const items = list.data?.items ?? [];
  return (
    <div className="oc-stack">
      <EssBack base={base} title="My Benefits" />
      {!list.isLoading && !list.error && items.length === 0 && <Empty title="No benefit yet" help="Benefits HR enrolls you in appear here with their contributions." icon="verified_user" />}
      {items.map((x) => (
        <div key={String(x.id)} className="oc-card">
          <div className="oc-card-head"><span className="oc-icon-circle"><Icon name="verified_user" size={22} /></span><h3>{String(x.planName)}</h3>
            <span className="oc-spacer" /><StatusPill status={String(x.status)} label={label(x.status)} /></div>
          <KV items={[['Type', label(x.benefitType)], ['Provider', String(x.provider ?? '—')], ['Covered', `${date(x.effectiveFrom)} – ${x.effectiveTo ? date(x.effectiveTo) : 'ongoing'}`],
            ['Paid by the club / month', money(x.employerContribution)], ['Your contribution / month', money(x.employeeContribution)],
            ['Deducted from payroll so far', money(x.deducted)]]} />
        </div>
      ))}
    </div>
  );
}

registerEssSection('reimbursements', ({ base }) => <EssReimbursements base={base} />);
registerEssSection('benefits', ({ base }) => <EssBenefits base={base} />);

export const HR_SERVICES_ROUTES: AreaRoute[] = [
  { path: 'hris/loans', perm: 'hris.employee_loan.view', element: <LoansPage /> },
  { path: 'hris/reimbursements', perm: 'hris.reimbursement.view', element: <ReimbursementPage /> },
  { path: 'hris/benefit-plans', perm: 'hris.benefit_enrollment.view', element: <BenefitPlansPage /> },
];
