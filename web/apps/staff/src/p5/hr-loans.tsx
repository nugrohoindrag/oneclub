import React, { useState } from 'react';
import { Link } from 'react-router';
import { uuidv7, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate } from '@oneclub/i18n';
import {
  DataTable, Empty, ErrorAlert, FilterPills, Icon, Modal, MoneyField, SelectField, Skeleton, StatusPill, TextArea, TextField, useAuth, useToast,
  type Option,
} from '@oneclub/shell';
import { KV, money, today, type R } from '../p1/common';
import { registerEssSection } from './hr';

// HRIS improvement phase B (spec §24, §29): loans and cash advances as an employee service — HR requests one for an employee (or the
// employee in Employee Self Service), the approval workflow decides (Approvals inbox, with Request revision), Finance pays it (journal Dr
// employee receivables / Cr cash or bank) and records repayments outside payroll; regular payroll runs deduct the installments. The
// statement shows payment, installments and repayments with the running balance.

const HR = '/api/v1/hris';
const ESS = '/api/v1/ess';
const INV = [HR, ESS, '/api/v1/platform/approvals'];
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const date = (v: unknown) => (v ? formatDate(String(v).slice(0, 10)) : '—');
const clean = (o: Record<string, unknown>) => Object.fromEntries(Object.entries(o).filter(([, v]) => v !== '' && v !== undefined && v !== null));
const TYPES: Option[] = [{ value: 'loan', label: 'Loan' }, { value: 'cash_advance', label: 'Cash advance' }];
const METHODS: Option[] = [{ value: 'bank_transfer', label: 'Bank transfer' }, { value: 'cash', label: 'Cash' }];
const STATUS_LABELS: Record<string, string> = { submitted: 'Waiting for approval', approved: 'Approved · to pay', active: 'Paid · repaying' };
const statusPill = (r: R) => <StatusPill status={String(r.status)} label={STATUS_LABELS[String(r.status)] ?? label(r.status)} />;
const nextPeriod = () => {
  const [y, m] = today().split('-').map(Number);
  return m === 12 ? `${y + 1}-01` : `${y}-${String(m + 1).padStart(2, '0')}`;
};

/** A modal form posting a body; closes and toasts on success. */
function Send({ title, path, body, submit, idempotent, onClose, children }: {
  title: string; path: string; body: () => Record<string, unknown>; submit: string; idempotent?: boolean; onClose: () => void; children: React.ReactNode;
}) {
  const toast = useToast();
  const send = useSend<Record<string, unknown>, R>('POST', path, INV, idempotent ? () => ({ 'Idempotency-Key': uuidv7() }) : undefined);
  return (
    <Modal open onClose={onClose} title={title} actions={
      <>
        <button className="oc-btn oc-btn-text" onClick={onClose}>Cancel</button>
        <button className="oc-btn oc-btn-ink" disabled={send.isPending} onClick={() => send.mutate(body(), {
          onSuccess: () => { toast(`${title}: done`); onClose(); },
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

/** Request form shared by HR (with the employee) and ESS (yourself). */
function RequestForm({ path, employees, onClose }: { path: string; employees?: Option[]; onClose: () => void }) {
  const [f, setF] = useState<Record<string, string>>({ loanType: 'cash_advance', startPeriod: nextPeriod() });
  const set = (k: string) => (v: string) => setF({ ...f, [k]: v });
  return (
    <Send title="Request a loan or cash advance" path={path} submit="Submit for approval" idempotent onClose={onClose}
      body={() => clean({ employeeId: f.employeeId, loanType: f.loanType, principal: f.principal, installment: f.installment, startPeriod: f.startPeriod,
        purpose: f.purpose })}>
      {employees && <SelectField label="Employee" value={f.employeeId ?? ''} onChange={set('employeeId')} options={employees} required span />}
      <SelectField label="Type" value={f.loanType} onChange={set('loanType')} options={TYPES} required />
      <MoneyField label="Amount (IDR)" value={f.principal ?? ''} onChange={set('principal')} required />
      <TextField label="Installment per payroll (empty = deducted at once)" inputMode="decimal" value={f.installment ?? ''} onChange={set('installment')} />
      <TextField label="First deduction (YYYY-MM)" value={f.startPeriod ?? ''} onChange={set('startPeriod')} />
      <TextArea label="Purpose" value={f.purpose ?? ''} onChange={set('purpose')} required />
    </Send>
  );
}

/** Statement of one loan: payment, payroll installments, repayments. */
export function LoanStatement({ path, onClose }: { path: string; onClose: () => void }) {
  const s = useGet<R>(path);
  const loan = (s.data?.loan ?? {}) as R;
  const rows = ((s.data?.movements as R[] | undefined) ?? []).map((m, i) => ({ ...m, id: String(i) } as R));
  return (
    <Modal open wide onClose={onClose} title={s.data ? `${String(loan.number ?? 'Loan')} · ${String(loan.employeeName)}` : 'Loan statement'}>
      {s.isLoading && <Skeleton rows={5} />}
      <ErrorAlert error={s.error} />
      {s.data && (
        <div className="oc-stack">
          <KV items={[['Type', label(loan.loanType)], ['Status', STATUS_LABELS[String(loan.status)] ?? label(loan.status)], ['Amount', money(loan.principal)],
            ['Installment', money(loan.installment)], ['First deduction', String(loan.startPeriod)], ['Repaid', money(loan.repaid)],
            ['Outstanding', money(loan.outstanding)], ['Installments left', String(loan.installments)], ['Purpose', String(loan.purpose ?? '—')],
            ['Paid out', loan.disbursedOn ? `${date(loan.disbursedOn)} · ${label(loan.disbursementMethod)} ${String(loan.disbursementRef ?? '')}` : '—'],
            ['Decision', String(loan.decisionNote ?? '—')]]} />
          <DataTable rows={rows} empty={<Empty title="No movement yet" help="The payment by Finance, payroll installments and repayments appear here." icon="receipt_long" />}
            columns={[
              { key: 'date', header: 'Date', render: (m) => date(m.date) },
              { key: 'kind', header: 'Movement', render: (m) => ({ disbursement: 'Paid to employee', payroll: 'Payroll installment', repayment: 'Repayment' }[String(m.kind)] ?? label(m.kind)) },
              { key: 'reference', header: 'Reference', render: (m) => String(m.reference ?? '—') },
              { key: 'amount', header: 'Amount', align: 'right', render: (m) => money(m.amount) },
              { key: 'balance', header: 'Outstanding', align: 'right', render: (m) => money(m.balance) },
            ]} />
        </div>
      )}
    </Modal>
  );
}

/** Cancel with a required note (requests not yet paid). */
function CancelLoan({ path, onClose }: { path: string; onClose: () => void }) {
  const [note, setNote] = useState('');
  return (
    <Send title="Cancel request" path={path} submit="Cancel request" onClose={onClose} body={() => ({ note })}>
      <TextArea label="Reason" value={note} onChange={setNote} required span />
    </Send>
  );
}

const FILTERS: Option[] = [
  { value: '', label: 'All' }, { value: 'submitted', label: 'Waiting for approval' }, { value: 'approved', label: 'To pay' },
  { value: 'active', label: 'Repaying' }, { value: 'settled', label: 'Settled' }, { value: 'rejected', label: 'Rejected' }, { value: 'cancelled', label: 'Cancelled' },
];

/** Human Resources → Employee Services → Loans & Advances: the queue of requests, payments and balances. */
export function LoansWorkspace({ employeeId }: { employeeId?: string }) {
  const { can } = useAuth();
  const [status, setStatus] = useState('');
  const [modal, setModal] = useState<{ kind: 'request' | 'pay' | 'repay' | 'cancel' | 'statement'; loan?: R } | null>(null);
  const [f, setF] = useState<Record<string, string>>({});
  const q = new URLSearchParams(clean({ status, employeeId }) as Record<string, string>).toString();
  const list = useGet<Page<R>>(`${HR}/loans${q ? `?${q}` : ''}`);
  const emps = useGet<Page<R>>(can('hris.employee_loan.create') && !employeeId ? `${HR}/employees?limit=500&filter[status]=active` : null);
  const employees = (emps.data?.items ?? []).map((x) => ({ value: String(x.id), label: `${String(x.fullName)} (${String(x.employeeNo)})` }));
  const pay = can('hris.employee_loan.pay');
  const loan = modal?.loan;
  const base = loan ? `${HR}/employee-loans/${String(loan.id)}` : '';
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap">
        <FilterPills options={FILTERS} value={status} onChange={setStatus} />
        <span className="oc-spacer" />
        {can('hris.employee_loan.create') && !employeeId && (
          <button className="oc-btn oc-btn-ink" onClick={() => setModal({ kind: 'request' })}><Icon name="add" size={18} /> Request loan / advance</button>
        )}
      </div>
      <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error} onRowClick={(r) => setModal({ kind: 'statement', loan: r })}
        empty={<Empty title="No loans or cash advances" help="Requests from HR and from Employee Self Service appear here with their approval and balance." icon="account_balance_wallet" />}
        columns={[
          { key: 'number', header: 'Number', render: (r) => String(r.number ?? '—') },
          ...(employeeId ? [] : [{ key: 'employeeName', header: 'Employee', render: (r: R) => <Link to={`/hris/employees/${String(r.employeeId)}?tab=loans`} onClick={(e) => e.stopPropagation()}>{String(r.employeeName)}</Link> }]),
          { key: 'loanType', header: 'Type', render: (r) => label(r.loanType) },
          { key: 'principal', header: 'Amount', align: 'right', render: (r) => money(r.principal) },
          { key: 'installment', header: 'Installment', align: 'right', render: (r) => money(r.installment) },
          { key: 'outstanding', header: 'Outstanding', align: 'right', render: (r) => money(r.outstanding) },
          { key: 'requestSource', header: 'Requested by', render: (r) => (r.requestSource === 'ess' ? 'Employee (ESS)' : 'HR') },
          { key: 'status', header: 'Status', render: statusPill },
        ]}
        actions={(r) => (
          <div className="oc-row" style={{ gap: 4 }}>
            {r.status === 'submitted' && r.approvalRequestId ? <Link className="oc-btn oc-btn-text oc-btn-sm" to={`/approvals/${String(r.approvalRequestId)}`}>Approval</Link> : null}
            {pay && r.status === 'approved' && <button className="oc-btn oc-btn-ink oc-btn-sm" onClick={() => { setF({ disbursedOn: today(), method: 'bank_transfer' }); setModal({ kind: 'pay', loan: r }); }}>Pay</button>}
            {pay && r.status === 'active' && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => { setF({ paidOn: today(), method: 'cash', amount: String(r.outstanding) }); setModal({ kind: 'repay', loan: r }); }}>Repayment</button>}
            {can('hris.employee_loan.update') && (r.status === 'approved' || r.status === 'submitted') && <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => setModal({ kind: 'cancel', loan: r })}>Cancel</button>}
          </div>
        )} />
      {modal?.kind === 'request' && <RequestForm path={`${HR}/employee-loans:request`} employees={employees} onClose={() => setModal(null)} />}
      {modal?.kind === 'statement' && loan && <LoanStatement path={`${base}/statement`} onClose={() => setModal(null)} />}
      {modal?.kind === 'cancel' && loan && <CancelLoan path={`${base}:cancel`} onClose={() => setModal(null)} />}
      {modal?.kind === 'pay' && loan && (
        <Send title={`Pay ${String(loan.number)}`} path={`${base}:disburse`} submit="Record payment" onClose={() => setModal(null)}
          body={() => clean({ disbursedOn: f.disbursedOn, method: f.method, reference: f.reference, bankAccountCode: f.bankAccountCode })}>
          <p className="oc-muted oc-small" style={{ margin: 0 }}>{money(loan.principal)} to {String(loan.employeeName)}. Accounting books Dr employee receivables / Cr cash or bank; payroll deducts {money(loan.installment)} per period from {String(loan.startPeriod)}.</p>
          <TextField label="Paid on" type="date" value={f.disbursedOn ?? ''} onChange={(v) => setF({ ...f, disbursedOn: v })} required />
          <SelectField label="Method" value={f.method ?? ''} onChange={(v) => setF({ ...f, method: v })} options={METHODS} required />
          <TextField label="Reference" value={f.reference ?? ''} onChange={(v) => setF({ ...f, reference: v })} />
          <TextField label="GL cash / bank account code (empty = default)" value={f.bankAccountCode ?? ''} onChange={(v) => setF({ ...f, bankAccountCode: v })} />
        </Send>
      )}
      {modal?.kind === 'repay' && loan && (
        <Send title={`Repayment ${String(loan.number ?? '')}`} path={`${base}:repay`} submit="Record repayment" onClose={() => setModal(null)}
          body={() => clean({ paidOn: f.paidOn, amount: f.amount, method: f.method, reference: f.reference, notes: f.notes, bankAccountCode: f.bankAccountCode })}>
          <p className="oc-muted oc-small" style={{ margin: 0 }}>Outstanding {money(loan.outstanding)}. Payroll installments are recorded by the payroll run; use this for cash returned or a transfer.</p>
          <TextField label="Paid on" type="date" value={f.paidOn ?? ''} onChange={(v) => setF({ ...f, paidOn: v })} required />
          <MoneyField label="Amount (IDR)" value={f.amount ?? ''} onChange={(v) => setF({ ...f, amount: v })} required />
          <SelectField label="Method" value={f.method ?? ''} onChange={(v) => setF({ ...f, method: v })} options={METHODS} required />
          <TextField label="Reference" value={f.reference ?? ''} onChange={(v) => setF({ ...f, reference: v })} />
          <TextArea label="Notes" value={f.notes ?? ''} onChange={(v) => setF({ ...f, notes: v })} span />
        </Send>
      )}
    </div>
  );
}

// ── Employee Self Service → Loans & Advances ──────────────────────────────

function EssLoans({ base }: { base: string }) {
  const [modal, setModal] = useState<{ kind: 'request' | 'statement' | 'cancel'; id?: string } | null>(null);
  const list = useGet<Page<R>>(`${ESS}/loans`);
  const items = list.data?.items ?? [];
  const outstanding = items.filter((r) => r.status === 'active').reduce((s, r) => s + Number(r.outstanding ?? 0), 0);
  return (
    <div className="oc-stack">
      <div className="oc-row" style={{ gap: 8 }}>
        <Link className="oc-btn oc-btn-text" to={base} aria-label="Back to Employee Self Service"><Icon name="arrow_back" size={22} /></Link>
        <h1 style={{ margin: 0, fontSize: 24 }}>Loans & Advances</h1>
        <span className="oc-spacer" />
        <button className="oc-btn oc-btn-ink" style={{ minHeight: 44 }} onClick={() => setModal({ kind: 'request' })}><Icon name="add" size={18} /> Request</button>
      </div>
      {outstanding > 0 && <div className="oc-alert oc-alert-info" role="status">Outstanding balance {money(outstanding)}, deducted from your payroll.</div>}
      {!list.isLoading && !list.error && items.length === 0 && <Empty title="No loan or cash advance" help="Request one here; HR and Finance approve and pay it, payroll deducts the installments." icon="account_balance_wallet" />}
      {items.length > 0 && (
        <DataTable rows={items} loading={list.isLoading} error={list.error} onRowClick={(r) => setModal({ kind: 'statement', id: String(r.id) })} columns={[
          { key: 'number', header: 'Number', render: (r) => String(r.number ?? '—') }, { key: 'loanType', header: 'Type', render: (r) => label(r.loanType) },
          { key: 'principal', header: 'Amount', align: 'right', render: (r) => money(r.principal) },
          { key: 'outstanding', header: 'Outstanding', align: 'right', render: (r) => money(r.outstanding) },
          { key: 'status', header: 'Status', render: statusPill },
        ]} actions={(r) => (r.status === 'submitted'
          ? <button className="oc-btn oc-btn-text" style={{ minHeight: 44 }} onClick={() => setModal({ kind: 'cancel', id: String(r.id) })}>Withdraw</button> : null)} />
      )}
      {modal?.kind === 'request' && <RequestForm path={`${ESS}/loans`} onClose={() => setModal(null)} />}
      {modal?.kind === 'statement' && modal.id && <LoanStatement path={`${ESS}/loans/${modal.id}/statement`} onClose={() => setModal(null)} />}
      {modal?.kind === 'cancel' && modal.id && <CancelLoan path={`${ESS}/loans/${modal.id}:cancel`} onClose={() => setModal(null)} />}
    </div>
  );
}

registerEssSection('loans', ({ base }) => <EssLoans base={base} />);
