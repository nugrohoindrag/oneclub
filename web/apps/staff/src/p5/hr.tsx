import React, { useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router';
import { download, request, uuidv7, useGet, useSend, type Page } from '@oneclub/api-client';
import { currentLocale, formatDate, formatDateTime } from '@oneclub/i18n';
import {
  AutoResourcePage, Card, Checkbox, DataTable, Drawer, Empty, ErrorAlert, FilterPills, Icon, Modal, PageHeader, SelectField, Skeleton, StatusPill, TextArea,
  TextField, useAuth, useToast, type Option,
} from '@oneclub/shell';
import { ActionButton, KV, ListPage, Tabs, money, today, type R } from '../p1/common';
import type { AreaRoute, OpsRoute, OpsTile } from '../p3/types';
// Recruitment & Performance Review (EP-03, EP-05) live in hr_talent.tsx.
import { TALENT_ESS, TALENT_ROUTES } from './hr_talent';
// Gap closure (partner clock-in, migration reconciliation, push notifications) lives in gaps.tsx.
import { GAPS_OPS_ROUTES, GAPS_OPS_TILES, GAPS_ROUTES, PushSettings } from './gaps';

// PRD P5 — core HR (EP-01 Organization & Employee, EP-02 Contracts & Documents, EP-04 Training & Certification, EP-24 HR Policies)
// and Employee Self Service (EP-16). Back Office routes (HRIS → Employees, Organization, Training & Certification), the ESS area of
// the ops shell (/ops/ess, personal login) and of the Back Office (/ess), and the ESS section registry the time and payroll areas plug
// into (registerEssSection). Routes are registered in p3/index.tsx and ops/p3.tsx.

const HR = '/api/v1/hris';
const INV = [HR];
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const pill = (k: string) => (r: R) => <StatusPill status={String(r[k] ?? '')} label={label(r[k])} />;
const opts = (vals: string[]): Option[] => vals.map((v) => ({ value: v, label: label(v) }));
const val = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : String(v));
const date = (v: unknown) => (v ? formatDate(String(v)) : '—');
const rows = (v: unknown) => ((v as R[] | undefined) ?? []).map((x, i) => ({ ...x, id: String(x.id ?? i) } as R));
const withId = (r: R, id: string): R => ({ ...r, id });
const idem = () => ({ 'Idempotency-Key': uuidv7() });
const clean = (o: Record<string, unknown>) => Object.fromEntries(Object.entries(o).filter(([, v]) => v !== '' && v !== undefined && v !== null));

const EMPLOYMENT = opts(['probation', 'contract', 'permanent', 'resigned', 'terminated']);
const TERMINATION = opts(['resigned', 'terminated', 'contract_ended', 'retired', 'deceased']);
const PTKP = ['TK/0', 'TK/1', 'TK/2', 'TK/3', 'K/0', 'K/1', 'K/2', 'K/3'].map((v) => ({ value: v, label: v }));
const DOC_TYPES = opts(['ktp', 'kk', 'npwp', 'passport', 'ijazah', 'cv', 'bpjs_kesehatan', 'bpjs_ketenagakerjaan', 'certificate', 'contract',
  'warning_letter', 'medical', 'reference_letter', 'photo', 'other']);
const ROLES = opts(['lifeguard', 'caddy', 'instructor', 'food_handler', 'engineering', 'course_maintenance', 'security', 'sport_staff', 'starter', 'other']);

function useOptions(path: string | null, text: (r: R) => string): Option[] {
  const l = useGet<Page<R>>(path);
  return (l.data?.items ?? []).map((x) => ({ value: x.id, label: text(x) }));
}
const useEmployees = () => useOptions(`${HR}/employees?limit=500&filter[status]=active`, (x) => `${String(x.fullName)} (${String(x.employeeNo)})`);
const useUnits = () => useOptions(`${HR}/org-units?limit=500&filter[status]=active`, (x) => `${String(x.name)} (${String(x.code)})`);
const usePositions = () => useOptions(`${HR}/positions?limit=500&filter[status]=active`, (x) => `${String(x.name)} (${String(x.code)})`);
const useGrades = () => useOptions(`${HR}/grades?limit=50&filter[status]=active`, (x) => `${String(x.code)} · ${String(x.name)}`);

/** Uploads an HR document file and returns its id. */
function FileUpload({ value, onChange, label: text = 'Attach file' }: { value: string; onChange: (id: string) => void; label?: string }) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [name, setName] = useState('');
  const upload = async (f: File) => {
    setBusy(true);
    setErr(null);
    const fd = new FormData();
    fd.append('file', f);
    try {
      const r = await request<R>('POST', `${HR}/document-files`, fd);
      onChange(String(r.id));
      setName(String(r.filename ?? f.name));
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="oc-stack" style={{ gap: 6 }}>
      <ErrorAlert error={err} />
      <label className="oc-btn oc-btn-neutral oc-btn-sm" style={{ cursor: busy ? 'not-allowed' : 'pointer', width: 'fit-content' }}>
        <Icon name="attach_file" size={18} /> {busy ? 'Uploading…' : value ? `Replace (${name || 'attached'})` : text}
        <input type="file" accept="image/jpeg,image/png,image/webp,application/pdf" className="oc-sr" disabled={busy}
          onChange={(e) => { const f = e.target.files?.[0]; e.target.value = ''; if (f) void upload(f); }} />
      </label>
    </div>
  );
}

/** A form in a modal that POSTs / PATCHes a body. */
function FormModal({ open, onClose, title, method = 'POST', path, body, invalidate = INV, children, submit = 'Save', onDone, wide }: {
  open: boolean; onClose: () => void; title: string; method?: 'POST' | 'PATCH'; path: string; body: () => Record<string, unknown>; invalidate?: string[];
  children: React.ReactNode; submit?: string; onDone?: (r: R) => void; wide?: boolean;
}) {
  const toast = useToast();
  const send = useSend<Record<string, unknown>, R>(method, path, invalidate, method === 'POST' ? idem : undefined);
  return (
    <Modal open={open} onClose={onClose} title={title} wide={wide} actions={
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

// ── Employees (EP-01) ─────────────────────────────────────────────────────

const EMPLOYEE_TABS: Option[] = [
  { value: 'list', label: 'Employees' }, { value: 'contracts', label: 'Contracts' }, { value: 'documents', label: 'Documents' },
  { value: 'changes', label: 'Employment Changes' }, { value: 'data', label: 'Data Changes' }, { value: 'import', label: 'Import' },
];

export function EmployeesPage() {
  const [params, setParams] = useSearchParams();
  const tab = params.get('tab') ?? 'list';
  const { can } = useAuth();
  const tabs = EMPLOYEE_TABS.filter((t) => (t.value === 'import' ? can('hris.import.create') : t.value === 'data' ? can('hris.profile_change.view')
    : t.value === 'contracts' ? can('hris.contract.view') : t.value === 'documents' ? can('hris.employee_document.view') : true));
  return (
    <div className="oc-stack">
      <Tabs tabs={tabs} value={tab} onChange={(v) => setParams({ tab: v })} />
      {tab === 'list' && <EmployeeList />}
      {tab === 'contracts' && <ContractsPanel />}
      {tab === 'documents' && <DocumentsPanel />}
      {tab === 'changes' && <ChangesPanel />}
      {tab === 'data' && <DataChangesPanel />}
      {tab === 'import' && <ImportPanel />}
    </div>
  );
}

function EmployeeList() {
  const nav = useNavigate();
  const { can } = useAuth();
  const [open, setOpen] = useState(false);
  const [emp, setEmp] = useState('');
  return (
    <>
      <ListPage title="Employees" help="Employee master (HRIS): organization, position, grade, employment status, contracts, documents and certifications. Identity numbers and salaries are masked by permission (UU PDP)."
        path={`${HR}/employees`} statuses={[{ value: 'active', label: 'Active' }, { value: 'inactive', label: 'Left' }]}
        extraQuery={emp ? { 'filter[employmentStatus]': emp } : {}}
        filters={<FilterPills options={[{ value: '', label: 'All statuses' }, ...EMPLOYMENT]} value={emp} onChange={setEmp} />}
        actions={can('hris.employee.create') && <button className="oc-btn oc-btn-ink" onClick={() => setOpen(true)}><Icon name="person_add" size={18} /> Add Employee</button>}
        onRowClick={(r) => nav(`/hris/employees/${r.id}`)}
        columns={[
          { key: 'employeeNo', header: 'No.' }, { key: 'fullName', header: 'Name' }, { key: 'jobTitle', header: 'Job Title', render: (r) => val(r.jobTitle) },
          { key: 'employmentStatus', header: 'Employment', render: pill('employmentStatus') }, { key: 'joinDate', header: 'Joined', render: (r) => date(r.joinDate) },
          { key: 'terminationDate', header: 'Leaves', render: (r) => date(r.terminationDate) },
        ]} />
      {open && <NewEmployee onClose={() => setOpen(false)} onDone={(r) => nav(`/hris/employees/${r.id}`)} />}
    </>
  );
}

function NewEmployee({ onClose, onDone }: { onClose: () => void; onDone: (r: R) => void }) {
  const positions = usePositions();
  const units = useUnits();
  const [f, setF] = useState<Record<string, string>>({ employmentStatus: 'probation', workerCategory: 'regular', joinDate: today() });
  const set = (k: string) => (v: string) => setF({ ...f, [k]: v });
  return (
    <FormModal open onClose={onClose} title="Add Employee" path={`${HR}/employees`} body={() => clean(f)} onDone={onDone} wide>
      <TextField label="Full name" value={f.fullName ?? ''} onChange={set('fullName')} required />
      <TextField label="Employee No." value={f.employeeNo ?? ''} onChange={set('employeeNo')} placeholder="Next number" />
      <SelectField label="Position" value={f.positionId ?? ''} onChange={set('positionId')} options={positions} placeholder="None" />
      <SelectField label="Org unit" value={f.orgUnitId ?? ''} onChange={set('orgUnitId')} options={units} placeholder="From the position" />
      <SelectField label="Employment status" value={f.employmentStatus} onChange={set('employmentStatus')} options={EMPLOYMENT.slice(0, 3)} />
      <SelectField label="Worker category" value={f.workerCategory} onChange={set('workerCategory')} options={opts(['regular', 'daily', 'intern'])} />
      <TextField label="Join date" type="date" value={f.joinDate} onChange={set('joinDate')} />
      <SelectField label="Gender" value={f.gender ?? ''} onChange={set('gender')} options={opts(['male', 'female'])} placeholder="—" />
      <TextField label="Date of birth" type="date" value={f.birthDate ?? ''} onChange={set('birthDate')} />
      <TextField label="NIK" value={f.nik ?? ''} onChange={set('nik')} inputMode="numeric" maxLength={16} help="16 digits" />
      <TextField label="NPWP" value={f.npwp ?? ''} onChange={set('npwp')} />
      <SelectField label="PTKP" value={f.ptkpStatus ?? ''} onChange={set('ptkpStatus')} options={PTKP} placeholder="—" />
      <TextField label="Work e-mail" type="email" value={f.email ?? ''} onChange={set('email')} />
      <TextField label="Phone" value={f.phone ?? ''} onChange={set('phone')} />
    </FormModal>
  );
}

const DETAIL_TABS: Option[] = [
  { value: 'profile', label: 'Profile' }, { value: 'employment', label: 'Employment' }, { value: 'contracts', label: 'Contracts' },
  { value: 'documents', label: 'Documents' }, { value: 'certifications', label: 'Certifications' }, { value: 'bank', label: 'Bank & Contacts' },
];

export function EmployeeDetailPage() {
  const { id = '' } = useParams();
  const [tab, setTab] = useState('profile');
  const p = useGet<R>(`${HR}/employees/${id}/profile`);
  if (p.isLoading) return <Skeleton rows={8} />;
  if (p.error || !p.data) return <ErrorAlert error={p.error} />;
  const d = p.data;
  const e = d.employee as R;
  return (
    <div className="oc-stack">
      <PageHeader title={`${String(e.fullName)} · ${String(e.employeeNo)}`}
        help={`${val(e.jobTitle)} — ${val(d.orgUnitName)}${d.gradeCode ? ` · ${String(d.gradeCode)}` : ''}`}
        actions={<><Link className="oc-btn oc-btn-text" to="/hris/employees">All employees</Link><EmployeeActions d={d} /></>} />
      <div className="oc-row-wrap">
        <StatusPill status={String(e.employmentStatus)} label={label(e.employmentStatus)} />
        {e.terminationStatus === 'scheduled' && <StatusPill status="warning" label={`Leaves ${date(e.terminationDate)}`} />}
        {(d.certificationGaps as R[]).length > 0 && <StatusPill status="error" label="Certification gap" />}
        {Number(d.expiringDocuments) > 0 && <StatusPill status="pending" label={`${String(d.expiringDocuments)} documents expiring`} />}
      </div>
      <Tabs tabs={DETAIL_TABS} value={tab} onChange={setTab} />
      {tab === 'profile' && <ProfileTab d={d} />}
      {tab === 'employment' && <EmploymentTab id={id} d={d} />}
      {tab === 'contracts' && <ContractsPanel employeeId={id} />}
      {tab === 'documents' && <DocumentsPanel employeeId={id} />}
      {tab === 'certifications' && <CertificatesPanel employeeId={id} gaps={d.certificationGaps as R[]} />}
      {tab === 'bank' && <BankContacts id={id} />}
    </div>
  );
}

function EmployeeActions({ d }: { d: R }) {
  const { can } = useAuth();
  const e = d.employee as R;
  const id = String(e.id);
  const [open, setOpen] = useState('');
  const active = e.status === 'active';
  return (
    <>
      {active && can('hris.employee.transfer') && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setOpen('transfer')}>Transfer</button>}
      {active && can('hris.employee.transfer') && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setOpen('promote')}>Promote</button>}
      {active && !d.account && can('hris.employee.manage_account') && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setOpen('account')}>Create Login</button>}
      {active && e.terminationStatus !== 'scheduled' && can('hris.employee.terminate') && <button className="oc-btn oc-btn-danger oc-btn-sm" onClick={() => setOpen('terminate')}>Offboard</button>}
      {e.terminationStatus === 'scheduled' && can('hris.employee.terminate') && (
        <ActionButton label="Withdraw Resignation" path={`${HR}/employees/${id}:cancel-termination`} invalidate={INV} reason="required" />
      )}
      {can('hris.letter.generate') && <LetterButton id={id} />}
      {(open === 'transfer' || open === 'promote') && <ChangeModal id={id} kind={open} onClose={() => setOpen('')} />}
      {open === 'terminate' && <TerminateModal id={id} onClose={() => setOpen('')} />}
      {open === 'account' && <AccountModal id={id} email={String(e.email ?? '')} onClose={() => setOpen('')} />}
    </>
  );
}

function LetterButton({ id }: { id: string }) {
  const letters = useOptions(`${HR}/letter-templates?limit=100&filter[status]=active`, (x) => String(x.name));
  const [code, setCode] = useState('');
  const toast = useToast();
  const tpl = useGet<Page<R>>(`${HR}/letter-templates?limit=100&filter[status]=active`);
  const codeOf = (tid: string) => String((tpl.data?.items ?? []).find((x) => x.id === tid)?.code ?? '');
  if (letters.length === 0) return null;
  return (
    <span className="oc-row" style={{ gap: 4 }}>
      <select className="oc-select" aria-label="HR letter" value={code} onChange={(e) => setCode(e.target.value)} style={{ height: 32 }}>
        <option value="">HR letter…</option>
        {letters.map((l) => <option key={l.value} value={l.value}>{l.label}</option>)}
      </select>
      <button className="oc-btn oc-btn-neutral oc-btn-sm" disabled={!code} onClick={() => {
        download('GET', `${HR}/employees/${id}/letters/${codeOf(code)}`, undefined, `${codeOf(code)}.pdf`).catch((e: Error) => toast(e.message, 'error'));
      }}>PDF</button>
    </span>
  );
}

function ChangeModal({ id, kind, onClose }: { id: string; kind: string; onClose: () => void }) {
  const units = useUnits();
  const positions = usePositions();
  const grades = useGrades();
  const people = useEmployees();
  const [f, setF] = useState<Record<string, string>>({ effectiveDate: today(), kind: kind === 'promote' ? 'promotion' : 'transfer' });
  const set = (k: string) => (v: string) => setF({ ...f, [k]: v });
  return (
    <FormModal open onClose={onClose} title={kind === 'promote' ? 'Promote / Demote' : 'Transfer / Rotate'} path={`${HR}/employees/${id}:${kind}`}
      body={() => clean(f)}>
      <SelectField label="Kind" value={f.kind} onChange={set('kind')} options={opts(kind === 'promote' ? ['promotion', 'demotion'] : ['transfer', 'rotation'])} />
      <TextField label="Effective date" type="date" value={f.effectiveDate} onChange={set('effectiveDate')} help="A future date is applied on that day" />
      <SelectField label="Position" value={f.positionId ?? ''} onChange={set('positionId')} options={positions} placeholder="Unchanged" />
      {kind !== 'promote' && <SelectField label="Org unit" value={f.orgUnitId ?? ''} onChange={set('orgUnitId')} options={units} placeholder="From the position" />}
      <SelectField label="Grade" value={f.gradeId ?? ''} onChange={set('gradeId')} options={grades} placeholder="From the position" />
      <SelectField label="Supervisor" value={f.supervisorId ?? ''} onChange={set('supervisorId')} options={people} placeholder="Unchanged" />
      <TextArea label="Reason" value={f.reason ?? ''} onChange={set('reason')} span required />
    </FormModal>
  );
}

function TerminateModal({ id, onClose }: { id: string; onClose: () => void }) {
  const [f, setF] = useState<Record<string, string>>({ terminationType: 'resigned', effectiveDate: today() });
  const set = (k: string) => (v: string) => setF({ ...f, [k]: v });
  return (
    <FormModal open onClose={onClose} title="Offboard Employee" path={`${HR}/employees/${id}:terminate`} body={() => clean(f)} submit="Confirm">
      <SelectField label="Type" value={f.terminationType} onChange={set('terminationType')} options={TERMINATION} />
      <TextField label="Effective date" type="date" value={f.effectiveDate} onChange={set('effectiveDate')}
        help="From this date the login is deactivated and pending approvals move to the supervisor" />
      <TextArea label="Reason" value={f.reason ?? ''} onChange={set('reason')} span required />
    </FormModal>
  );
}

function AccountModal({ id, email, onClose }: { id: string; email: string; onClose: () => void }) {
  const [f, setF] = useState<Record<string, string>>({ email, locale: currentLocale() });
  const set = (k: string) => (v: string) => setF({ ...f, [k]: v });
  return (
    <FormModal open onClose={onClose} title="Create Login" path={`${HR}/employees/${id}:create-account`} submit="Create & invite"
      body={() => clean({ email: f.email, locale: f.locale, ...(f.roles ? { roleCodes: f.roles.split(',').map((s) => s.trim()).filter(Boolean) } : {}) })}>
      <TextField label="E-mail" type="email" value={f.email} onChange={set('email')} help="An invitation to set the password is sent" />
      <SelectField label="Language" value={f.locale} onChange={set('locale')} options={[{ value: 'id', label: 'Bahasa Indonesia' }, { value: 'en', label: 'English' }]} />
      <TextField label="Roles (codes)" value={f.roles ?? ''} onChange={set('roles')} placeholder="employee_self_service" span />
    </FormModal>
  );
}

function ProfileTab({ d }: { d: R }) {
  const e = d.employee as R;
  const { can } = useAuth();
  const [edit, setEdit] = useState(false);
  const acc = d.account as R | null;
  const ctr = d.contract as R | null;
  return (
    <div className="oc-grid-2">
      <Card title="Personal" icon="person" actions={can('hris.employee.update') && <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => setEdit(true)}>Edit</button>}>
        <KV items={[['Gender', label(e.gender)], ['Born', `${val(e.birthPlace)}, ${date(e.birthDate)}`], ['Marital status', label(e.maritalStatus)],
          ['NIK', val(e.nik)], ['NPWP', val(e.npwp)], ['PTKP', val(e.ptkpStatus)], ['BPJS Kesehatan', val(e.bpjsKesehatanNo)],
          ['BPJS Ketenagakerjaan', val(e.bpjsKetenagakerjaanNo)], ['E-mail', val(e.email)], ['Phone', val(e.phone)], ['Address', val(e.address)],
          ['Health notes', val(e.healthNotes)]]} />
      </Card>
      <Card title="Employment" icon="badge">
        <KV items={[['Org unit', val(d.orgUnitName)], ['Position', val(d.positionName)], ['Grade', val(d.gradeCode)], ['Supervisor', val(d.supervisorName)],
          ['Worker category', label(e.workerCategory)], ['Joined', date(e.joinDate)], ['Probation ends', date(e.probationEndDate)],
          ['Permanent since', date(e.permanentDate)], ['Service', `${String(d.serviceMonths)} months`],
          ['Contract', ctr ? `${String(ctr.number)} · ${String(ctr.contractType).toUpperCase()} · ${label(ctr.status)}${ctr.endDate ? ` until ${date(ctr.endDate)}` : ''}` : '—'],
          ['Login', acc ? `${String(acc.email)} (${label(acc.status)}) · ${(acc.roles as string[]).join(', ')}` : 'No login']]} />
      </Card>
      {(d.scheduled as R[]).length > 0 && (
        <Card title="Scheduled changes" icon="event">
          {(d.scheduled as R[]).map((s) => <p key={String(s.id)} style={{ margin: 0 }}>{date(s.effectiveDate)} — {label(s.kind)}: {val(s.toPosition)} · {val(s.toOrgUnit)}</p>)}
        </Card>
      )}
      {edit && <EditEmployee e={e} onClose={() => setEdit(false)} />}
    </div>
  );
}

const EDITABLE = ['fullName', 'preferredName', 'gender', 'birthDate', 'birthPlace', 'religion', 'maritalStatus', 'nik', 'npwp', 'ptkpStatus', 'bpjsKesehatanNo',
  'bpjsKetenagakerjaanNo', 'email', 'personalEmail', 'phone', 'address', 'city', 'postalCode', 'jobTitle', 'costCenter', 'workerCategory', 'joinDate',
  'healthNotes', 'bloodType'];

function EditEmployee({ e, onClose }: { e: R; onClose: () => void }) {
  const [f, setF] = useState<Record<string, string>>(Object.fromEntries(EDITABLE.map((k) => [k, String(e[k] ?? '')])));
  const set = (k: string) => (v: string) => setF({ ...f, [k]: v });
  const changed = () => Object.fromEntries(EDITABLE.filter((k) => f[k] !== String(e[k] ?? '')).map((k) => [k, f[k]]));
  const text = (k: string, l: string, extra?: Partial<React.ComponentProps<typeof TextField>>) =>
    <TextField key={k} label={l} value={f[k]} onChange={set(k)} {...extra} />;
  return (
    <FormModal open onClose={onClose} title="Edit Employee" method="PATCH" path={`${HR}/employees/${String(e.id)}`} body={changed} wide>
      {text('fullName', 'Full name')}{text('preferredName', 'Preferred name')}
      <SelectField label="Gender" value={f.gender} onChange={set('gender')} options={opts(['male', 'female'])} placeholder="—" />
      {text('birthDate', 'Date of birth', { type: 'date' })}{text('birthPlace', 'Place of birth')}{text('religion', 'Religion')}
      <SelectField label="Marital status" value={f.maritalStatus} onChange={set('maritalStatus')} options={opts(['single', 'married', 'divorced', 'widowed'])} placeholder="—" />
      {text('nik', 'NIK')}{text('npwp', 'NPWP')}
      <SelectField label="PTKP" value={f.ptkpStatus} onChange={set('ptkpStatus')} options={PTKP} placeholder="—" />
      {text('bpjsKesehatanNo', 'BPJS Kesehatan')}{text('bpjsKetenagakerjaanNo', 'BPJS Ketenagakerjaan')}{text('email', 'Work e-mail', { type: 'email' })}
      {text('personalEmail', 'Personal e-mail', { type: 'email' })}{text('phone', 'Phone')}{text('address', 'Address')}{text('city', 'City')}
      {text('postalCode', 'Postal code')}{text('jobTitle', 'Job title')}{text('costCenter', 'Cost center')}{text('joinDate', 'Join date', { type: 'date' })}
      <SelectField label="Worker category" value={f.workerCategory} onChange={set('workerCategory')} options={opts(['regular', 'daily', 'intern'])} />
      {text('bloodType', 'Blood type')}{text('healthNotes', 'Health notes')}
    </FormModal>
  );
}

function EmploymentTab({ id, d }: { id: string; d: R }) {
  const hist = useGet<Page<R>>(`${HR}/employees/${id}/history`);
  const { can } = useAuth();
  const off = useGet<Page<R>>(d.offboarding && can('hris.offboarding.view') ? `${HR}/employees/${id}/offboarding` : null);
  return (
    <div className="oc-stack">
      {d.offboarding !== null && d.offboarding !== undefined && (
        <Card title="Offboarding checklist" icon="checklist">
          <DataTable rows={off.data?.items} loading={off.isLoading} error={off.error} columns={[
            { key: 'label', header: 'Item', render: (r) => (
              <>
                {String(r.label)}
                {/* FR-HR-06: assets of the asset register still in the leaver's custody */}
                {((r.assets as R[] | undefined) ?? []).length > 0 && (
                  <div className="oc-small oc-muted">Still held: {(r.assets as R[]).map((a) => `${String(a.code)} · ${String(a.name)}`).join(', ')}</div>
                )}
              </>
            ) },
            { key: 'status', header: 'Status', render: pill('status') },
            { key: 'doneByName', header: 'By', render: (r) => (r.doneAt ? `${val(r.doneByName)} · ${formatDateTime(String(r.doneAt))}` : '—') },
            { key: 'notes', header: 'Notes', render: (r) => val(r.notes) },
          ]} actions={(r) => can('hris.offboarding.manage') && r.status === 'pending' ? (
            <span className="oc-row" style={{ gap: 4 }}>
              <ActionButton label="Done" method="PATCH" path={`${HR}/offboarding-items/${r.id}`} body={{ status: 'done' }} invalidate={INV} />
              <ActionButton label="N/A" method="PATCH" path={`${HR}/offboarding-items/${r.id}`} body={{ status: 'not_applicable' }} invalidate={INV} kind="text" />
            </span>
          ) : null} />
        </Card>
      )}
      <Card title="Employment history" icon="history">
        <DataTable rows={hist.data?.items} loading={hist.isLoading} error={hist.error} columns={changeColumns(false)}
          actions={(r) => r.status === 'scheduled' && r.kind !== 'termination' && can('hris.employee.transfer') ? (
            <ActionButton label="Cancel" path={`${HR}/employment-changes/${r.id}:cancel`} invalidate={INV} reason="required" />) : null} />
      </Card>
    </div>
  );
}

const changeColumns = (who: boolean) => [
  ...(who ? [{ key: 'employeeName', header: 'Employee', render: (r: R) => <Link to={`/hris/employees/${String(r.employeeId)}`}>{String(r.employeeName)}</Link> }] : []),
  { key: 'effectiveDate', header: 'Effective', render: (r: R) => date(r.effectiveDate) }, { key: 'kind', header: 'Change', render: (r: R) => label(r.kind) },
  { key: 'to', header: 'To', render: (r: R) => [r.toPosition, r.toOrgUnit, r.toGrade, r.toStatus].filter(Boolean).map(String).join(' · ') || '—' },
  { key: 'reason', header: 'Reason', render: (r: R) => val(r.reason) }, { key: 'status', header: 'Status', render: pill('status') },
];

function ChangesPanel() {
  const [status, setStatus] = useState('');
  const l = useGet<Page<R>>(`${HR}/employment-changes${status ? `?status=${status}` : ''}`);
  return (
    <div className="oc-stack">
      <PageHeader title="Employment Changes" help="Hires, transfers, rotations, promotions, contract and status changes and terminations with their effective date (FR-HR-05)." />
      <FilterPills options={[{ value: '', label: 'All' }, ...opts(['scheduled', 'applied', 'cancelled'])]} value={status} onChange={setStatus} />
      <DataTable rows={l.data?.items} loading={l.isLoading} error={l.error} columns={changeColumns(true)} />
    </div>
  );
}

// ── Contracts (EP-02) ─────────────────────────────────────────────────────

function ContractsPanel({ employeeId }: { employeeId?: string }) {
  const { can } = useAuth();
  const [params] = useSearchParams();
  const [expiring, setExpiring] = useState(params.get('expiring') === '1');
  const [open, setOpen] = useState<{ kind: string; c?: R } | null>(null);
  const path = `${HR}/contracts?${employeeId ? `employeeId=${employeeId}&` : ''}${expiring ? 'expiringWithin=30&' : ''}`;
  const l = useGet<Page<R>>(path);
  return (
    <div className="oc-stack">
      <PageHeader title="Contracts" help="PKWT (fixed term, at most 5 years with renewals) and PKWTT (permanent, probation up to 3 months). Reminders H-30 / H-7 before a PKWT ends."
        actions={can('hris.contract.create') && <button className="oc-btn oc-btn-ink" onClick={() => setOpen({ kind: 'new' })}>New Contract</button>} />
      {!employeeId && <Checkbox label="Ending within 30 days" checked={expiring} onChange={setExpiring} />}
      <DataTable rows={l.data?.items} loading={l.isLoading} error={l.error} columns={[
        { key: 'number', header: 'Contract' },
        ...(employeeId ? [] : [{ key: 'employeeName', header: 'Employee', render: (r: R) => <Link to={`/hris/employees/${String(r.employeeId)}`}>{String(r.employeeName)}</Link> }]),
        { key: 'contractType', header: 'Type', render: (r) => `${String(r.contractType).toUpperCase()} #${String(r.sequenceNo)}` },
        { key: 'startDate', header: 'Start', render: (r) => date(r.startDate) }, { key: 'endDate', header: 'End', render: (r) => date(r.endedOn ?? r.endDate) },
        { key: 'daysRemaining', header: 'Days left', render: (r) => val(r.daysRemaining) },
        { key: 'baseSalary', header: 'Base salary', align: 'right', render: (r) => (r.baseSalary === null ? '••••' : money(r.baseSalary)) },
        { key: 'status', header: 'Status', render: pill('status') },
      ]} actions={(r) => <ContractActions c={r} onOpen={(kind) => setOpen({ kind, c: r })} />} />
      {open?.kind === 'new' && <ContractModal employeeId={employeeId} onClose={() => setOpen(null)} />}
      {(open?.kind === 'renew' || open?.kind === 'make-permanent') && open.c && <RenewModal c={open.c} kind={open.kind} onClose={() => setOpen(null)} />}
    </div>
  );
}

function ContractActions({ c, onOpen }: { c: R; onOpen: (kind: string) => void }) {
  const { can } = useAuth();
  const id = String(c.id);
  const live = c.status === 'active' || c.status === 'expiring';
  return (
    <span className="oc-row" style={{ gap: 4 }}>
      {c.status === 'draft' && can('hris.contract.activate') && <ActionButton label="Activate" path={`${HR}/contracts/${id}:activate`} invalidate={INV} kind="primary" />}
      {c.status === 'draft' && can('hris.contract.update') && <ActionButton label="Cancel" path={`${HR}/contracts/${id}:cancel`} invalidate={INV} reason="optional" />}
      {live && c.contractType === 'pkwt' && can('hris.contract.renew') && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => onOpen('renew')}>Renew</button>}
      {live && c.contractType === 'pkwt' && can('hris.contract.renew') && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => onOpen('make-permanent')}>Make Permanent</button>}
      {live && can('hris.contract.end') && <EndContract id={id} />}
      {Boolean(c.signedFileId) && <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => void download('GET', `${HR}/contracts/${id}/file`, undefined, `${String(c.number)}.pdf`)}>Signed</button>}
    </span>
  );
}

function EndContract({ id }: { id: string }) {
  const [open, setOpen] = useState(false);
  const [f, setF] = useState({ endDate: today(), reason: '' });
  return (
    <>
      <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => setOpen(true)}>End</button>
      <FormModal open={open} onClose={() => setOpen(false)} title="End Contract" path={`${HR}/contracts/${id}:end`} body={() => f}>
        <TextField label="Last day" type="date" value={f.endDate} onChange={(v) => setF({ ...f, endDate: v })} />
        <TextArea label="Reason" value={f.reason} onChange={(v) => setF({ ...f, reason: v })} span required />
      </FormModal>
    </>
  );
}

type Allow = { code: string; name: string; amount: string };

function AllowancesEditor({ list, onChange }: { list: Allow[]; onChange: (l: Allow[]) => void }) {
  const set = (i: number, k: keyof Allow, v: string) => onChange(list.map((a, j) => (j === i ? { ...a, [k]: v } : a)));
  return (
    <div className="oc-stack oc-span" style={{ gap: 6 }}>
      {list.map((a, i) => (
        <div key={i} className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
          <TextField label="Code" value={a.code} onChange={(v) => set(i, 'code', v)} />
          <TextField label="Allowance" value={a.name} onChange={(v) => set(i, 'name', v)} />
          <TextField label="Amount / month" type="number" value={a.amount} onChange={(v) => set(i, 'amount', v)} />
          <button className="oc-btn oc-btn-text" aria-label={`Remove allowance ${i + 1}`} onClick={() => onChange(list.filter((_, j) => j !== i))}><Icon name="delete" size={18} /></button>
        </div>
      ))}
      <div><button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => onChange([...list, { code: '', name: '', amount: '' }])}>Add fixed allowance</button></div>
    </div>
  );
}

function ContractModal({ employeeId, onClose }: { employeeId?: string; onClose: () => void }) {
  const people = useEmployees();
  const [f, setF] = useState<Record<string, string>>({ employeeId: employeeId ?? '', contractType: 'pkwt', startDate: today(), workWeekDays: '5' });
  const [allow, setAllow] = useState<Allow[]>([]);
  const [activate, setActivate] = useState(true);
  const set = (k: string) => (v: string) => setF({ ...f, [k]: v });
  return (
    <FormModal open onClose={onClose} title="New Contract" path={`${HR}/contracts`} wide body={() => clean({
      ...f, probationMonths: f.probationMonths ? Number(f.probationMonths) : undefined, workWeekDays: Number(f.workWeekDays),
      allowances: allow.filter((a) => a.code), activate, signedFileId: f.signedFileId,
    })}>
      {!employeeId && <SelectField label="Employee" value={f.employeeId} onChange={set('employeeId')} options={people} required />}
      <SelectField label="Type" value={f.contractType} onChange={set('contractType')} options={[{ value: 'pkwt', label: 'PKWT (fixed term)' }, { value: 'pkwtt', label: 'PKWTT (permanent)' }]} />
      <TextField label="Start" type="date" value={f.startDate} onChange={set('startDate')} />
      {f.contractType === 'pkwt' ? <TextField label="End" type="date" value={f.endDate ?? ''} onChange={set('endDate')} required />
        : <TextField label="Probation (months)" type="number" min={0} max={3} value={f.probationMonths ?? ''} onChange={set('probationMonths')} />}
      <TextField label="Base salary / month" type="number" value={f.baseSalary ?? ''} onChange={set('baseSalary')} required />
      <SelectField label="Work week" value={f.workWeekDays} onChange={set('workWeekDays')} options={[{ value: '5', label: '5 days' }, { value: '6', label: '6 days' }]} />
      <AllowancesEditor list={allow} onChange={setAllow} />
      <div className="oc-span"><FileUpload value={f.signedFileId ?? ''} onChange={set('signedFileId')} label="Attach signed contract" /></div>
      <Checkbox label="Activate now" checked={activate} onChange={setActivate} />
    </FormModal>
  );
}

function RenewModal({ c, kind, onClose }: { c: R; kind: string; onClose: () => void }) {
  const [f, setF] = useState<Record<string, string>>({});
  const set = (k: string) => (v: string) => setF({ ...f, [k]: v });
  return (
    <FormModal open onClose={onClose} title={kind === 'renew' ? `Renew ${String(c.number)}` : `Make Permanent (${String(c.employeeName)})`}
      path={`${HR}/contracts/${String(c.id)}:${kind}`} body={() => clean(f)}>
      <TextField label="Start" type="date" value={f.startDate ?? ''} onChange={set('startDate')} help="Default: the day after the current contract ends" />
      {kind === 'renew' && <TextField label="End" type="date" value={f.endDate ?? ''} onChange={set('endDate')} required />}
      <TextField label="Base salary / month" type="number" value={f.baseSalary ?? ''} onChange={set('baseSalary')} placeholder="Unchanged" />
      <TextArea label="Notes" value={f.notes ?? ''} onChange={set('notes')} span />
    </FormModal>
  );
}

// ── Documents (FR-CTR-04) ─────────────────────────────────────────────────

function DocumentsPanel({ employeeId }: { employeeId?: string }) {
  const { can } = useAuth();
  const [within, setWithin] = useState(employeeId ? '' : '60');
  const [open, setOpen] = useState(false);
  const toast = useToast();
  const l = useGet<Page<R>>(`${HR}/employee-documents?${employeeId ? `employeeId=${employeeId}&` : ''}${within ? `expiringWithin=${within}` : ''}`);
  return (
    <div className="oc-stack">
      <PageHeader title="Documents" help="KTP, NPWP, BPJS, diplomas, certificates and warning letters with validity. Confidential documents (warning letters, medical) need their own permission."
        actions={employeeId && can('hris.employee_document.manage') && <button className="oc-btn oc-btn-ink" onClick={() => setOpen(true)}>Add Document</button>} />
      {!employeeId && <FilterPills options={[{ value: '60', label: 'Expiring (60 days)' }, { value: '', label: 'All' }]} value={within} onChange={setWithin} />}
      <DataTable rows={l.data?.items} loading={l.isLoading} error={l.error} columns={[
        ...(employeeId ? [] : [{ key: 'employeeName', header: 'Employee', render: (r: R) => <Link to={`/hris/employees/${String(r.employeeId)}`}>{String(r.employeeName)}</Link> }]),
        { key: 'title', header: 'Document', render: (r) => `${String(r.title)}${r.confidential ? ' 🔒' : ''}` }, { key: 'documentNo', header: 'No.', render: (r) => val(r.documentNo) },
        { key: 'issuedOn', header: 'Issued', render: (r) => date(r.issuedOn) }, { key: 'expiresOn', header: 'Expires', render: (r) => date(r.expiresOn) },
        { key: 'validity', header: 'Validity', render: (r) => <StatusPill status={r.validity === 'expired' ? 'error' : r.validity === 'expiring' ? 'pending' : 'active'} label={label(r.validity)} /> },
      ]} actions={(r) => (
        <span className="oc-row" style={{ gap: 4 }}>
          {Boolean(r.fileId) && <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => download('GET', `${HR}/employee-documents/${r.id}/file`, undefined, String(r.fileName ?? 'document')).catch((e: Error) => toast(e.message, 'error'))}>File</button>}
          {can('hris.employee_document.manage') && <ActionButton label="Archive" method="DELETE" path={`${HR}/employee-documents/${r.id}`} invalidate={INV} confirm="Archive this document?" kind="text" />}
        </span>
      )} />
      {open && employeeId && <DocumentModal employeeId={employeeId} onClose={() => setOpen(false)} />}
    </div>
  );
}

function DocumentModal({ employeeId, onClose }: { employeeId: string; onClose: () => void }) {
  const [f, setF] = useState<Record<string, string>>({ documentType: 'ktp' });
  const set = (k: string) => (v: string) => setF({ ...f, [k]: v });
  return (
    <FormModal open onClose={onClose} title="Add Document" path={`${HR}/employees/${employeeId}/documents`}
      body={() => clean({ ...f, warningLevel: f.warningLevel ? Number(f.warningLevel) : undefined })}>
      <SelectField label="Type" value={f.documentType} onChange={set('documentType')} options={DOC_TYPES} />
      {f.documentType === 'warning_letter' && <SelectField label="Level" value={f.warningLevel ?? ''} onChange={set('warningLevel')} options={[{ value: '1', label: 'SP1' }, { value: '2', label: 'SP2' }, { value: '3', label: 'SP3' }]} required />}
      <TextField label="Title" value={f.title ?? ''} onChange={set('title')} placeholder="Document type" />
      <TextField label="Document No." value={f.documentNo ?? ''} onChange={set('documentNo')} />
      <TextField label="Issued" type="date" value={f.issuedOn ?? ''} onChange={set('issuedOn')} />
      <TextField label="Expires" type="date" value={f.expiresOn ?? ''} onChange={set('expiresOn')} />
      <div className="oc-span"><FileUpload value={f.fileId ?? ''} onChange={set('fileId')} /></div>
    </FormModal>
  );
}

// ── Data changes from ESS (FR-ESS-06) ─────────────────────────────────────

function changesText(c: R) {
  const ch = (c.changes ?? {}) as R;
  return Object.entries(ch).map(([k, v]) => {
    if (k === 'bankAccount') { const b = v as R; return `bank: ${String(b.bankName)} ${String(b.accountNo)} (${String(b.accountName)})`; }
    if (k === 'emergencyContacts') return `emergency contacts: ${(v as R[]).map((x) => `${String(x.name)} ${String(x.phone)}`).join('; ')}`;
    return `${k}: ${String(v)}`;
  }).join(' · ');
}

function DataChangesPanel() {
  const [status, setStatus] = useState('submitted');
  const { can } = useAuth();
  const l = useGet<Page<R>>(`${HR}/profile-changes${status ? `?status=${status}` : ''}`);
  return (
    <div className="oc-stack">
      <PageHeader title="Data Changes" help="Personal data changes sent from Employee Self Service; HR verifies them (e.g. against the bank book) before they apply." />
      <FilterPills options={[...opts(['submitted', 'approved', 'rejected', 'cancelled']), { value: '', label: 'All' }]} value={status} onChange={setStatus} />
      <DataTable rows={l.data?.items} loading={l.isLoading} error={l.error} columns={[
        { key: 'createdAt', header: 'Sent', render: (r) => formatDateTime(String(r.createdAt)) }, { key: 'employeeName', header: 'Employee' },
        { key: 'changes', header: 'Changes', render: changesText }, { key: 'status', header: 'Status', render: pill('status') },
        { key: 'reviewNote', header: 'Note', render: (r) => val(r.reviewNote) },
      ]} actions={(r) => r.status === 'submitted' && can('hris.profile_change.review') ? (
        <span className="oc-row" style={{ gap: 4 }}>
          <ActionButton label="Approve" path={`${HR}/profile-changes/${r.id}:approve`} invalidate={INV} kind="primary" />
          <RejectButton id={r.id} />
        </span>
      ) : null} />
    </div>
  );
}

function RejectButton({ id }: { id: string }) {
  const [open, setOpen] = useState(false);
  const [note, setNote] = useState('');
  return (
    <>
      <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => setOpen(true)}>Reject</button>
      <FormModal open={open} onClose={() => setOpen(false)} title="Reject Data Change" path={`${HR}/profile-changes/${id}:reject`} body={() => ({ note })}>
        <TextArea label="Note to the employee" value={note} onChange={setNote} span required />
      </FormModal>
    </>
  );
}

// ── Import (EP-28) ────────────────────────────────────────────────────────

const IMPORT_HEADERS: Record<string, string> = {
  employees: 'employeeNo,fullName,orgUnitCode,positionCode,gradeCode,supervisorNo,employmentStatus,workerCategory,joinDate,gender,birthDate,birthPlace,religion,maritalStatus,nik,npwp,ptkpStatus,bpjsKesehatanNo,bpjsKetenagakerjaanNo,email,personalEmail,phone,address,city,postalCode,bankName,bankCode,accountNo,accountName,legacyRef',
  contracts: 'employeeNo,contractType,startDate,endDate,probationMonths,positionCode,gradeCode,baseSalary,allowances,workWeekDays,status,previousNumber,notes',
  certifications: 'holderKind,employeeNo,partnerCode,typeCode,certificateNo,issuer,issuedOn,expiresOn,notes',
  grades: 'code,name,level,minSalary,maxSalary,description,status',
  org_units: 'code,name,parentCode,unitType,costCenter,headEmployeeNo,description,sortOrder,status',
  positions: 'code,name,orgUnitCode,gradeCode,reportsToCode,isHead,workforceRole,requiredCertifications,headcount,description,status',
  documents: 'employeeNo,documentType,title,documentNo,issuedOn,expiresOn,warningLevel,confidential,file,notes (file = id of an uploaded file)',
};

function ImportPanel() {
  const [entity, setEntity] = useState('employees');
  const [csv, setCsv] = useState('');
  const [dry, setDry] = useState(true);
  const send = useSend<Record<string, unknown>, R>('POST', `${HR}/imports`, INV);
  const rep = send.data;
  return (
    <div className="oc-stack">
      <PageHeader title="HR Migration Import" help="Organization (grades, org units, positions), employees, contracts, documents and certifications from the club's HR system or Excel (CSV), in that order; loads are repeatable. Reconcile the headcount and leave balances afterwards in HR Migration Reconciliation. Also available as `oneclub import hris`." />
      <div className="oc-form">
        <SelectField label="Entity" value={entity} onChange={setEntity} options={opts(['grades', 'org_units', 'positions', 'employees', 'contracts', 'documents', 'certifications'])} />
        <label className="oc-btn oc-btn-neutral oc-btn-sm" style={{ alignSelf: 'end', width: 'fit-content', cursor: 'pointer' }}>
          <Icon name="upload_file" size={18} /> Load CSV file
          <input type="file" accept=".csv,text/csv" className="oc-sr" onChange={(e) => { const f = e.target.files?.[0]; e.target.value = ''; if (f) void f.text().then(setCsv); }} />
        </label>
        <TextArea label="CSV" value={csv} onChange={setCsv} rows={8} span help={`Header: ${IMPORT_HEADERS[entity]}`} />
        <Checkbox label="Dry run (validate only)" checked={dry} onChange={setDry} />
      </div>
      <div><button className="oc-btn oc-btn-ink" disabled={!csv.trim() || send.isPending} onClick={() => send.mutate({ entity, csv, dryRun: dry })}>{dry ? 'Validate' : 'Import'}</button></div>
      <ErrorAlert error={send.error} />
      {rep && (
        <Card title={`${label(rep.entity)}${rep.dryRun ? ' (dry run)' : ''}`} icon="fact_check">
          <KV items={[['Rows', String(rep.rows)], ['Inserted', String(rep.inserted)], ['Updated', String(rep.updated)], ['Skipped', String(rep.skipped)], ['Failed', String(rep.failed)]]} />
          <DataTable rows={rows(rep.issues)} columns={[{ key: 'row', header: 'Row' }, { key: 'key', header: 'Key' }, { key: 'message', header: 'Issue' }]} empty={<p className="oc-muted">No issues.</p>} />
        </Card>
      )}
    </div>
  );
}

// ── Bank accounts & emergency contacts ────────────────────────────────────

function BankContacts({ id }: { id: string }) {
  const { can } = useAuth();
  const banks = useGet<Page<R>>(can('hris.bank_account.view') ? `${HR}/bank-accounts?filter[employeeId]=${id}` : null);
  const contacts = useGet<Page<R>>(`${HR}/emergency-contacts?filter[employeeId]=${id}`);
  const [open, setOpen] = useState('');
  const [f, setF] = useState<Record<string, string>>({});
  const set = (k: string) => (v: string) => setF({ ...f, [k]: v });
  return (
    <div className="oc-grid-2">
      {can('hris.bank_account.view') && (
        <Card title="Bank accounts" icon="account_balance" actions={can('hris.bank_account.create') && <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => { setF({ employeeId: id }); setOpen('bank'); }}>Add</button>}>
          <DataTable rows={banks.data?.items} loading={banks.isLoading} error={banks.error} columns={[
            { key: 'bankName', header: 'Bank' }, { key: 'accountNo', header: 'Account' }, { key: 'accountName', header: 'Name' },
            { key: 'isPrimary', header: 'Salary', render: (r) => (r.isPrimary ? 'Primary' : '') },
          ]} actions={(r) => can('hris.bank_account.delete') ? <ActionButton label="Remove" method="DELETE" path={`${HR}/bank-accounts/${r.id}`} invalidate={INV} confirm="Remove this account?" kind="text" /> : null} />
        </Card>
      )}
      <Card title="Emergency contacts" icon="contact_emergency" actions={can('hris.emergency_contact.create') && <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => { setF({ employeeId: id, relationship: 'spouse' }); setOpen('contact'); }}>Add</button>}>
        <DataTable rows={contacts.data?.items} loading={contacts.isLoading} error={contacts.error} columns={[
          { key: 'name', header: 'Name' }, { key: 'relationship', header: 'Relationship', render: (r) => label(r.relationship) }, { key: 'phone', header: 'Phone' },
        ]} actions={(r) => can('hris.emergency_contact.delete') ? <ActionButton label="Remove" method="DELETE" path={`${HR}/emergency-contacts/${r.id}`} invalidate={INV} confirm="Remove this contact?" kind="text" /> : null} />
      </Card>
      <FormModal open={open === 'bank'} onClose={() => setOpen('')} title="Add Bank Account" path={`${HR}/bank-accounts`} body={() => clean(f)}>
        <TextField label="Bank" value={f.bankName ?? ''} onChange={set('bankName')} required />
        <TextField label="Bank code" value={f.bankCode ?? ''} onChange={set('bankCode')} />
        <TextField label="Account No." value={f.accountNo ?? ''} onChange={set('accountNo')} inputMode="numeric" required />
        <TextField label="Account name" value={f.accountName ?? ''} onChange={set('accountName')} required />
      </FormModal>
      <FormModal open={open === 'contact'} onClose={() => setOpen('')} title="Add Emergency Contact" path={`${HR}/emergency-contacts`} body={() => clean(f)}>
        <TextField label="Name" value={f.name ?? ''} onChange={set('name')} required />
        <SelectField label="Relationship" value={f.relationship ?? ''} onChange={set('relationship')} options={opts(['spouse', 'parent', 'child', 'sibling', 'relative', 'friend', 'other'])} />
        <TextField label="Phone" value={f.phone ?? ''} onChange={set('phone')} required />
      </FormModal>
    </div>
  );
}

// ── Organization (FR-HR-01, FR-HR-07) ─────────────────────────────────────

const ORG_TABS: Option[] = [
  { value: 'chart', label: 'Org Chart' }, { value: 'hris.org_unit', label: 'Org Units' }, { value: 'hris.position', label: 'Positions' },
  { value: 'hris.grade', label: 'Grades' },
];

export function OrganizationPage() {
  const [tab, setTab] = useState('chart');
  return (
    <div className="oc-stack">
      <PageHeader title="Organization" help="Org units (departments with hierarchy and cost center), positions with grade, reporting line, workforce role and required certifications, grades G1–G7." />
      <Tabs tabs={ORG_TABS} value={tab} onChange={setTab} />
      {tab === 'chart' ? <OrgChart /> : <AutoResourcePage key={tab} resourceKey={tab} />}
    </div>
  );
}

function OrgNode({ n, depth }: { n: R; depth: number }) {
  const [open, setOpen] = useState(depth < 2);
  const kids = (n.children as R[]) ?? [];
  const people = (n.employees as R[]) ?? [];
  return (
    <li style={{ listStyle: 'none', marginLeft: depth ? 20 : 0 }}>
      <div className="oc-row" style={{ gap: 8, padding: '6px 0' }}>
        {kids.length > 0 || people.length > 0 ? (
          <button className="oc-btn oc-btn-text oc-btn-sm" aria-expanded={open} aria-label={`${open ? 'Collapse' : 'Expand'} ${String(n.name)}`} onClick={() => setOpen(!open)}>
            <Icon name={open ? 'expand_more' : 'chevron_right'} size={18} />
          </button>
        ) : <span style={{ width: 32 }} />}
        <strong>{String(n.name)}</strong>
        <span className="oc-muted oc-small">{String(n.code)} · {label(n.unitType)}{n.costCenter ? ` · CC ${String(n.costCenter)}` : ''}</span>
        <span className="oc-small">{n.headName ? `Head: ${String(n.headName)}` : 'No head'}</span>
        <StatusPill status="info" label={`${String(n.headcount)} / ${String(n.total)} staff${Number(n.budgeted) ? ` (budget ${String(n.budgeted)})` : ''}`} />
      </div>
      {open && (
        <ul style={{ margin: 0, padding: 0 }}>
          {people.length > 0 && <li style={{ listStyle: 'none', marginLeft: 52 }} className="oc-small oc-muted">{people.map((p) => `${String(p.fullName)} (${val(p.jobTitle)})`).join(' · ')}</li>}
          {kids.map((k) => <OrgNode key={String(k.id)} n={k} depth={depth + 1} />)}
        </ul>
      )}
    </li>
  );
}

function OrgChart() {
  const [people, setPeople] = useState(false);
  const l = useGet<Page<R>>(`${HR}/org-chart${people ? '?employees=true' : ''}`);
  if (l.isLoading) return <Skeleton rows={6} />;
  return (
    <Card title="Org chart" icon="account_tree" actions={<Checkbox label="Show employees" checked={people} onChange={setPeople} />}>
      <ErrorAlert error={l.error} />
      <ul style={{ margin: 0, padding: 0 }}>{(l.data?.items ?? []).map((n) => <OrgNode key={String(n.id)} n={n} depth={0} />)}</ul>
    </Card>
  );
}

// ── Training & Certification (EP-04) ──────────────────────────────────────

const TRAINING_TABS: Option[] = [
  { value: 'certificates', label: 'Certificates' }, { value: 'compliance', label: 'Compliance' }, { value: 'sessions', label: 'Training Sessions' },
  { value: 'matrix', label: 'Training Matrix' }, { value: 'hris.certification_type', label: 'Certification Types' },
  { value: 'hris.training_program', label: 'Training Programs' }, { value: 'hris.letter_template', label: 'Letter Templates' },
];

export function TrainingPage() {
  const [tab, setTab] = useState('certificates');
  return (
    <div className="oc-stack">
      <PageHeader title="Training & Certification" help="Mandatory certifications (lifeguard, caddy, food handler, K3 …) of employees, caddies and instructors with expiry reminders (H-60 / H-30); staff without a valid mandatory certificate cannot be scheduled or assigned." />
      <Tabs tabs={TRAINING_TABS} value={tab} onChange={setTab} />
      {tab === 'certificates' && <CertificatesPanel />}
      {tab === 'compliance' && <CompliancePanel />}
      {tab === 'sessions' && <SessionsPanel />}
      {tab === 'matrix' && <MatrixPanel />}
      {tab.startsWith('hris.') && <AutoResourcePage key={tab} resourceKey={tab} />}
    </div>
  );
}

const validityPill = (r: R) => (
  <StatusPill status={r.validity === 'expired' || r.validity === 'revoked' ? 'error' : r.validity === 'expiring' ? 'pending' : 'active'} label={label(r.validity)} />
);

function CertificatesPanel({ employeeId, gaps }: { employeeId?: string; gaps?: R[] }) {
  const { can } = useAuth();
  const toast = useToast();
  const [validity, setValidity] = useState(employeeId ? '' : 'expiring');
  const [kind, setKind] = useState('');
  const [open, setOpen] = useState(false);
  const l = useGet<Page<R>>(`${HR}/certification-status?${employeeId ? `employeeId=${employeeId}&` : ''}${validity ? `validity=${validity}&` : ''}${kind ? `holderKind=${kind}` : ''}`);
  return (
    <div className="oc-stack">
      {gaps && gaps.length > 0 && (
        <div className="oc-alert oc-alert-error" role="alert">Missing or expired mandatory certification: {gaps.map((g) => `${String(g.typeName)} (${label(g.reason)})`).join(', ')}</div>
      )}
      <div className="oc-row-wrap">
        <FilterPills options={[{ value: '', label: 'All' }, ...opts(['valid', 'expiring', 'expired', 'renewed', 'revoked'])]} value={validity} onChange={setValidity} />
        {!employeeId && <FilterPills options={[{ value: '', label: 'Everyone' }, ...opts(['employee', 'caddy', 'instructor'])]} value={kind} onChange={setKind} />}
        <span className="oc-spacer" />
        {can('hris.certification.create') && <button className="oc-btn oc-btn-ink" onClick={() => setOpen(true)}>Add Certificate</button>}
      </div>
      <DataTable rows={l.data?.items} loading={l.isLoading} error={l.error} columns={[
        { key: 'typeName', header: 'Certification' }, { key: 'holderName', header: 'Holder', render: (r) => `${String(r.holderName)}${r.holderKind !== 'employee' ? ` (${label(r.holderKind)})` : ''}` },
        { key: 'certificateNo', header: 'No.', render: (r) => val(r.certificateNo) }, { key: 'issuedOn', header: 'Issued', render: (r) => date(r.issuedOn) },
        { key: 'expiresOn', header: 'Expires', render: (r) => date(r.expiresOn) }, { key: 'validity', header: 'Validity', render: validityPill },
      ]} actions={(r) => (
        <span className="oc-row" style={{ gap: 4 }}>
          {Boolean(r.fileId) && <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => download('GET', `${HR}/certifications/${r.id}/file`, undefined, 'certificate').catch((e: Error) => toast(e.message, 'error'))}>File</button>}
          {can('hris.certification.update') && (r.status === 'active' || r.status === 'expired') && <ActionButton label="Revoke" path={`${HR}/certifications/${r.id}:revoke`} invalidate={INV} reason="required" danger />}
        </span>
      )} />
      {open && <CertificateModal employeeId={employeeId} onClose={() => setOpen(false)} />}
    </div>
  );
}

function CertificateModal({ employeeId, onClose }: { employeeId?: string; onClose: () => void }) {
  const types = useOptions(`${HR}/certification-types?limit=200&filter[status]=active`, (x) => String(x.name));
  const people = useEmployees();
  const [f, setF] = useState<Record<string, string>>({ holderKind: 'employee', employeeId: employeeId ?? '', issuedOn: today() });
  const caddies = useOptions(f.holderKind === 'caddy' ? '/api/v1/golf/caddies?limit=500&filter[status]=active' : null, (x) => `${String(x.name)} (${String(x.code)})`);
  const coaches = useOptions(f.holderKind === 'instructor' ? '/api/v1/sportclub/instructors?limit=500&filter[status]=active' : null, (x) => `${String(x.name)} (${String(x.code)})`);
  const set = (k: string) => (v: string) => setF({ ...f, [k]: v });
  return (
    <FormModal open onClose={onClose} title="Add Certificate" path={`${HR}/certifications`}
      body={() => clean({ ...f, employeeId: f.holderKind === 'employee' ? f.employeeId : '', partnerId: f.holderKind !== 'employee' ? f.partnerId : '' })}>
      <SelectField label="Certification" value={f.certificationTypeId ?? ''} onChange={set('certificationTypeId')} options={types} required />
      {!employeeId && <SelectField label="Holder" value={f.holderKind} onChange={set('holderKind')} options={opts(['employee', 'caddy', 'instructor'])} />}
      {f.holderKind === 'employee' && !employeeId && <SelectField label="Employee" value={f.employeeId} onChange={set('employeeId')} options={people} required />}
      {f.holderKind === 'caddy' && <SelectField label="Caddy" value={f.partnerId ?? ''} onChange={set('partnerId')} options={caddies} required />}
      {f.holderKind === 'instructor' && <SelectField label="Instructor" value={f.partnerId ?? ''} onChange={set('partnerId')} options={coaches} required />}
      <TextField label="Certificate No." value={f.certificateNo ?? ''} onChange={set('certificateNo')} />
      <TextField label="Issued" type="date" value={f.issuedOn} onChange={set('issuedOn')} />
      <TextField label="Expires" type="date" value={f.expiresOn ?? ''} onChange={set('expiresOn')} help="Default: from the validity of the type" />
      <div className="oc-span"><FileUpload value={f.fileId ?? ''} onChange={set('fileId')} /></div>
    </FormModal>
  );
}

function CompliancePanel() {
  const [role, setRole] = useState('lifeguard');
  const l = useGet<Page<R>>(`${HR}/certification-compliance?role=${role}`);
  const list = l.data?.items ?? [];
  const ok = list.filter((r) => r.compliant).length;
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap">
        <SelectField label="Workforce role" value={role} onChange={setRole} options={ROLES} />
        <StatusPill status={ok === list.length ? 'active' : 'warning'} label={`${ok} / ${list.length} compliant`} />
      </div>
      <DataTable rows={l.data?.items?.map((r) => withId(r, String(r.holderId)))} loading={l.isLoading} error={l.error} columns={[
        { key: 'holderName', header: 'Holder' }, { key: 'orgUnit', header: 'Org unit', render: (r) => val(r.orgUnit ?? label(r.holderKind)) },
        { key: 'required', header: 'Required', render: (r) => (r.required as string[]).join(', ') },
        { key: 'gaps', header: 'Gaps', render: (r) => (r.gaps as R[]).map((g) => `${String(g.typeName)} (${label(g.reason)})`).join(', ') || '—' },
        { key: 'compliant', header: 'Status', render: (r) => <StatusPill status={r.compliant ? 'active' : 'error'} label={r.compliant ? 'Compliant' : 'Not compliant'} /> },
      ]} />
    </div>
  );
}

function SessionsPanel() {
  const { can } = useAuth();
  const [sel, setSel] = useState<R | null>(null);
  const [open, setOpen] = useState(false);
  const programs = useOptions(`${HR}/training-programs?limit=200&filter[status]=active`, (x) => String(x.name));
  const [f, setF] = useState<Record<string, string>>({});
  const set = (k: string) => (v: string) => setF({ ...f, [k]: v });
  const iso = (v: string) => (v ? new Date(v).toISOString() : '');
  return (
    <>
      <ListPage title="Training Sessions" path={`${HR}/training-sessions`} statuses={opts(['planned', 'completed', 'cancelled'])}
        actions={can('hris.training_session.create') && <button className="oc-btn oc-btn-ink" onClick={() => { setF({}); setOpen(true); }}>Plan Session</button>}
        onRowClick={setSel} columns={[
          { key: 'startsAt', header: 'Date', render: (r) => formatDateTime(String(r.startsAt)) }, { key: 'title', header: 'Session' },
          { key: 'location', header: 'Location', render: (r) => val(r.location) }, { key: 'costTotal', header: 'Cost', align: 'right', render: (r) => money(r.costTotal) },
          { key: 'status', header: 'Status', render: pill('status') },
        ]} />
      <FormModal open={open} onClose={() => setOpen(false)} title="Plan Training Session" path={`${HR}/training-sessions`}
        body={() => clean({ ...f, startsAt: iso(f.startsAt ?? ''), endsAt: iso(f.endsAt ?? ''), capacity: f.capacity ? Number(f.capacity) : undefined })}>
        <SelectField label="Program" value={f.programId ?? ''} onChange={set('programId')} options={programs} required />
        <TextField label="Title" value={f.title ?? ''} onChange={set('title')} placeholder="Program name" />
        <TextField label="Starts" type="datetime-local" value={f.startsAt ?? ''} onChange={set('startsAt')} required />
        <TextField label="Ends" type="datetime-local" value={f.endsAt ?? ''} onChange={set('endsAt')} required />
        <TextField label="Location" value={f.location ?? ''} onChange={set('location')} />
        <TextField label="Trainer" value={f.trainer ?? ''} onChange={set('trainer')} />
        <TextField label="Capacity" type="number" value={f.capacity ?? ''} onChange={set('capacity')} />
      </FormModal>
      <Drawer open={!!sel} onClose={() => setSel(null)} title={sel ? String(sel.title) : ''}>{sel && <SessionDetail s={sel} />}</Drawer>
    </>
  );
}

function SessionDetail({ s }: { s: R }) {
  const { can } = useAuth();
  const id = String(s.id);
  const parts = useGet<Page<R>>(`${HR}/training-sessions/${id}/participants`);
  const people = useEmployees();
  const [add, setAdd] = useState('');
  const send = useSend<Record<string, unknown>>('POST', `${HR}/training-sessions/${id}/participants`, INV);
  const planned = s.status === 'planned';
  const edit = can('hris.training_session.update');
  return (
    <div className="oc-stack">
      <KV items={[['When', `${formatDateTime(String(s.startsAt))} – ${formatDateTime(String(s.endsAt))}`], ['Location', val(s.location)], ['Trainer', val(s.trainer)],
        ['Capacity', val(s.capacity)], ['Status', label(s.status)]]} />
      {planned && edit && (
        <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
          <SelectField label="Add participant" value={add} onChange={setAdd} options={people} placeholder="Employee" />
          <button className="oc-btn oc-btn-neutral oc-btn-sm" disabled={!add || send.isPending} onClick={() => send.mutate({ employeeIds: [add] }, { onSuccess: () => setAdd('') })}>Add</button>
        </div>
      )}
      <ErrorAlert error={send.error} />
      <DataTable rows={parts.data?.items} loading={parts.isLoading} error={parts.error} columns={[
        { key: 'employeeName', header: 'Participant' }, { key: 'attendance', header: 'Attendance', render: pill('attendance') },
        { key: 'result', header: 'Result', render: (r) => val(r.result) }, { key: 'score', header: 'Score', render: (r) => val(r.score) },
      ]} actions={(r) => edit && s.status !== 'cancelled' ? (
        <span className="oc-row" style={{ gap: 4 }}>
          {planned && <ActionButton label="Attended" method="PATCH" path={`${HR}/training-participants/${r.id}`} body={{ attendance: 'attended' }} invalidate={INV} />}
          {planned && r.attendance === 'attended' && <ActionButton label="Passed" method="PATCH" path={`${HR}/training-participants/${r.id}`} body={{ result: 'passed' }} invalidate={INV} kind="primary" />}
          {planned && r.attendance === 'attended' && <ActionButton label="Failed" method="PATCH" path={`${HR}/training-participants/${r.id}`} body={{ result: 'failed' }} invalidate={INV} kind="text" />}
          {planned && <ActionButton label="Remove" method="DELETE" path={`${HR}/training-participants/${r.id}`} invalidate={INV} kind="text" />}
        </span>
      ) : null} />
      {planned && edit && <div><ActionButton label="Complete Session" path={`${HR}/training-sessions/${id}:complete`} invalidate={INV} kind="ink"
        confirm="Participants not marked attended become absent; passed participants receive the certificate of the program." /></div>}
    </div>
  );
}

function MatrixPanel() {
  const units = useUnits();
  const [unit, setUnit] = useState('');
  const [status, setStatus] = useState('');
  const l = useGet<Page<R>>(`${HR}/training-matrix?${unit ? `orgUnitId=${unit}&` : ''}${status ? `status=${status}` : ''}`);
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <SelectField label="Org unit" value={unit} onChange={setUnit} options={units} placeholder="All" />
        <FilterPills options={[{ value: '', label: 'All' }, ...opts(['missing', 'due', 'compliant'])]} value={status} onChange={setStatus} />
      </div>
      <DataTable rows={l.data?.items?.map((r, i) => withId(r, `${String(r.employeeId)}-${String(r.programId)}-${i}`))} loading={l.isLoading} error={l.error} columns={[
        { key: 'employeeName', header: 'Employee' }, { key: 'orgUnit', header: 'Org unit', render: (r) => val(r.orgUnit) }, { key: 'position', header: 'Position' },
        { key: 'programName', header: 'Mandatory training' }, { key: 'lastCompleted', header: 'Completed', render: (r) => date(r.lastCompleted) },
        { key: 'dueDate', header: 'Due', render: (r) => date(r.dueDate) },
        { key: 'status', header: 'Status', render: (r) => <StatusPill status={r.status === 'compliant' ? 'active' : r.status === 'due' ? 'pending' : 'error'} label={label(r.status)} /> },
      ]} />
    </div>
  );
}

// ── Employee Self Service (EP-16) ─────────────────────────────────────────

/** An ESS section screen; the key is the last segment of the section path. */
type EssView = (props: { base: string }) => React.ReactNode;
const ESS_VIEWS = new Map<string, EssView>();

/**
 * Registers the screen of an Employee Self Service section (the section
 * itself is registered on the server with hris.RegisterESSSection). The
 * time and payroll areas call it from their module, e.g.
 * `registerEssSection('schedule', () => <MySchedule />)`.
 */
export function registerEssSection(key: string, view: EssView) {
  ESS_VIEWS.set(key, view);
}

const sectionKey = (path: string) => path.split('/').filter(Boolean).pop() ?? '';

function useEssMe() {
  return useGet<R>('/api/v1/ess/me');
}

export function EssHome({ base }: { base: string }) {
  const me = useEssMe();
  if (me.isLoading) return <Skeleton rows={6} />;
  if (me.error) return <Empty title="No employee profile" help="Your login is not linked to an employee profile yet. Ask HR to link it." icon="person_off" />;
  const d = me.data as R;
  const e = d.employee as R;
  const id = currentLocale() === 'id';
  return (
    <div className="oc-stack">
      <div className="oc-page-head"><div><h1>{String(e.preferredName ?? e.fullName)}</h1><p>{val(e.jobTitle)} · {val(e.orgUnit)} · {String(e.employeeNo)}</p></div></div>
      {Number(d.expiringItems) > 0 && <div className="oc-alert oc-alert-warning" role="status">{String(d.expiringItems)} of your documents or certificates expire within 30 days.</div>}
      <div className="oc-grid">
        {(d.sections as R[]).map((s) => (
          <Link key={String(s.key)} to={`${base}/${sectionKey(String(s.path))}`} className="oc-card" style={{ minHeight: 110, textDecoration: 'none' }}>
            <div className="oc-card-head"><span className="oc-icon-circle"><Icon name={String(s.icon)} size={22} /></span><h3>{String(id ? s.labelId : s.label)}</h3></div>
            {s.key === 'team' && <p className="oc-muted" style={{ margin: 0 }}>{String(d.teamSize)} people</p>}
            {s.key === 'profile' && Number(d.pendingChanges) > 0 && <p className="oc-muted" style={{ margin: 0 }}>Data change waiting for HR</p>}
          </Link>
        ))}
      </div>
    </div>
  );
}

function EssBack({ base, title }: { base: string; title: string }) {
  return (
    <div className="oc-row" style={{ gap: 8 }}>
      <Link className="oc-btn oc-btn-text" to={base} aria-label="Back to Employee Self Service"><Icon name="arrow_back" size={22} /></Link>
      <h1 style={{ margin: 0, fontSize: 24 }}>{title}</h1>
    </div>
  );
}

function EssProfile({ base }: { base: string }) {
  const me = useEssMe();
  const changes = useGet<Page<R>>('/api/v1/ess/profile-changes');
  const [open, setOpen] = useState(false);
  if (me.isLoading) return <Skeleton rows={6} />;
  if (me.error || !me.data) return <ErrorAlert error={me.error} />;
  const e = me.data.employee as R;
  const pending = (changes.data?.items ?? []).find((c) => c.status === 'submitted');
  return (
    <div className="oc-stack">
      <EssBack base={base} title="Profile" />
      <Card title="Employment" icon="badge">
        <KV items={[['Employee No.', String(e.employeeNo)], ['Position', val(e.position ?? e.jobTitle)], ['Org unit', val(e.orgUnit)], ['Grade', val(e.grade)],
          ['Supervisor', val(e.supervisor)], ['Status', label(e.employmentStatus)], ['Joined', date(e.joinDate)], ['Probation ends', date(e.probationEndDate)]]} />
      </Card>
      <Card title="Personal data" icon="person" actions={!pending && <button className="oc-btn oc-btn-neutral" style={{ minHeight: 44 }} onClick={() => setOpen(true)}>Request change</button>}>
        <KV items={[['Phone', val(e.phone)], ['Personal e-mail', val(e.personalEmail)], ['Address', `${val(e.address)}${e.city ? `, ${String(e.city)}` : ''}`],
          ['Marital status / PTKP', `${label(e.maritalStatus)} · ${val(e.ptkpStatus)}`], ['NIK', val(e.nik)], ['NPWP', val(e.npwp)],
          ['Salary account', e.bankName ? `${String(e.bankName)} ${String(e.bankAccountNo)} (${String(e.bankAccountName)})` : '—'],
          ['Emergency contacts', ((e.emergencyContacts as R[]) ?? []).map((c) => `${String(c.name)} (${label(c.relationship)}) ${String(c.phone)}`).join('; ') || '—']]} />
        {pending && <p className="oc-muted">Your change ({changesText(pending)}) is waiting for HR. <ActionButton label="Withdraw" path={`/api/v1/ess/profile-changes/${pending.id}:cancel`} invalidate={['/api/v1/ess']} kind="text" /></p>}
      </Card>
      <Card title="My data changes" icon="history">
        <DataTable rows={changes.data?.items} loading={changes.isLoading} error={changes.error} columns={[
          { key: 'createdAt', header: 'Sent', render: (r) => formatDateTime(String(r.createdAt)) }, { key: 'changes', header: 'Changes', render: changesText },
          { key: 'status', header: 'Status', render: pill('status') }, { key: 'reviewNote', header: 'HR note', render: (r) => val(r.reviewNote) },
        ]} empty={<p className="oc-muted">No changes sent.</p>} />
      </Card>
      <PushSettings surface="staff" />
      {open && <EssChangeModal e={e} onClose={() => setOpen(false)} />}
    </div>
  );
}

function EssChangeModal({ e, onClose }: { e: R; onClose: () => void }) {
  const [f, setF] = useState<Record<string, string>>({});
  const [bank, setBank] = useState(false);
  const [contact, setContact] = useState(false);
  const set = (k: string) => (v: string) => setF({ ...f, [k]: v });
  const body = () => {
    const changes: Record<string, unknown> = clean({ phone: f.phone, personalEmail: f.personalEmail, address: f.address, city: f.city, postalCode: f.postalCode,
      maritalStatus: f.maritalStatus, ptkpStatus: f.ptkpStatus });
    if (bank) changes.bankAccount = { bankName: f.bankName ?? '', accountNo: f.accountNo ?? '', accountName: f.accountName ?? '' };
    if (contact) changes.emergencyContacts = [{ name: f.contactName ?? '', relationship: f.relationship || 'other', phone: f.contactPhone ?? '' }];
    return { changes };
  };
  return (
    <FormModal open onClose={onClose} title="Request Data Change" path="/api/v1/ess/profile-changes" invalidate={['/api/v1/ess']} body={body} submit="Send to HR">
      <TextField label="Phone" value={f.phone ?? ''} onChange={set('phone')} placeholder={val(e.phone)} />
      <TextField label="Personal e-mail" type="email" value={f.personalEmail ?? ''} onChange={set('personalEmail')} />
      <TextField label="Address" value={f.address ?? ''} onChange={set('address')} span />
      <TextField label="City" value={f.city ?? ''} onChange={set('city')} />
      <TextField label="Postal code" value={f.postalCode ?? ''} onChange={set('postalCode')} />
      <SelectField label="Marital status" value={f.maritalStatus ?? ''} onChange={set('maritalStatus')} options={opts(['single', 'married', 'divorced', 'widowed'])} placeholder="Unchanged" />
      <SelectField label="PTKP" value={f.ptkpStatus ?? ''} onChange={set('ptkpStatus')} options={PTKP} placeholder="Unchanged" />
      <div className="oc-span"><Checkbox label="New salary account" checked={bank} onChange={setBank} /></div>
      {bank && <><TextField label="Bank" value={f.bankName ?? ''} onChange={set('bankName')} /><TextField label="Account No." inputMode="numeric" value={f.accountNo ?? ''} onChange={set('accountNo')} />
        <TextField label="Account name" value={f.accountName ?? ''} onChange={set('accountName')} /></>}
      <div className="oc-span"><Checkbox label="Replace emergency contact" checked={contact} onChange={setContact} /></div>
      {contact && <><TextField label="Name" value={f.contactName ?? ''} onChange={set('contactName')} />
        <SelectField label="Relationship" value={f.relationship ?? ''} onChange={set('relationship')} options={opts(['spouse', 'parent', 'child', 'sibling', 'relative', 'friend', 'other'])} />
        <TextField label="Phone" value={f.contactPhone ?? ''} onChange={set('contactPhone')} /></>}
    </FormModal>
  );
}

function EssDocuments({ base }: { base: string }) {
  const docs = useGet<Page<R>>('/api/v1/ess/documents');
  const toast = useToast();
  return (
    <div className="oc-stack">
      <EssBack base={base} title="My Documents" />
      <DataTable rows={docs.data?.items} loading={docs.isLoading} error={docs.error} columns={[
        { key: 'title', header: 'Document' }, { key: 'documentNo', header: 'No.', render: (r) => val(r.documentNo) },
        { key: 'expiresOn', header: 'Valid until', render: (r) => date(r.expiresOn) }, { key: 'validity', header: 'Validity', render: (r) => label(r.validity) },
      ]} actions={(r) => r.fileId ? <button className="oc-btn oc-btn-neutral" style={{ minHeight: 44 }} onClick={() => download('GET', `/api/v1/ess/documents/${r.id}/file`, undefined, String(r.fileName ?? 'document')).catch((e: Error) => toast(e.message, 'error'))}>Open</button> : null} />
    </div>
  );
}

function EssTraining({ base }: { base: string }) {
  const t = useGet<R>('/api/v1/ess/training');
  if (t.isLoading) return <Skeleton rows={6} />;
  if (t.error || !t.data) return <ErrorAlert error={t.error} />;
  return (
    <div className="oc-stack">
      <EssBack base={base} title="My Training" />
      <Card title="Certificates" icon="workspace_premium">
        <DataTable rows={rows(t.data.certifications)} columns={[{ key: 'typeName', header: 'Certification' }, { key: 'expiresOn', header: 'Valid until', render: (r) => date(r.expiresOn) },
          { key: 'validity', header: 'Validity', render: validityPill }]} empty={<p className="oc-muted">No certificates on file.</p>} />
      </Card>
      <Card title="Mandatory training" icon="rule">
        <DataTable rows={rows(t.data.mandatory).map((r, i) => withId(r, String(i)))} columns={[{ key: 'programName', header: 'Training' },
          { key: 'lastCompleted', header: 'Completed', render: (r) => date(r.lastCompleted) }, { key: 'status', header: 'Status', render: pill('status') }]}
          empty={<p className="oc-muted">No mandatory training for your position.</p>} />
      </Card>
      <Card title="Sessions" icon="school">
        <DataTable rows={rows(t.data.sessions).map((r) => withId(r, String(r.sessionId)))} columns={[{ key: 'startsAt', header: 'Date', render: (r) => formatDateTime(String(r.startsAt)) },
          { key: 'title', header: 'Session' }, { key: 'location', header: 'Location', render: (r) => val(r.location) }, { key: 'attendance', header: 'Attendance', render: (r) => label(r.attendance) },
          { key: 'result', header: 'Result', render: (r) => val(r.result) }]} empty={<p className="oc-muted">No training sessions.</p>} />
      </Card>
    </div>
  );
}

function EssTeam({ base }: { base: string }) {
  const team = useGet<Page<R>>('/api/v1/ess/team');
  return (
    <div className="oc-stack">
      <EssBack base={base} title="My Team" />
      <DataTable rows={team.data?.items} loading={team.isLoading} error={team.error} columns={[
        { key: 'fullName', header: 'Name', render: (r) => `${String(r.fullName)}${r.direct ? '' : ' ·'}` }, { key: 'jobTitle', header: 'Job title', render: (r) => val(r.jobTitle) },
        { key: 'orgUnit', header: 'Unit', render: (r) => val(r.orgUnit) }, { key: 'employmentStatus', header: 'Status', render: (r) => label(r.employmentStatus) },
        { key: 'contractEnds', header: 'Contract ends', render: (r) => date(r.contractEnds) },
        { key: 'certificationGaps', header: 'Certification', render: (r) => ((r.certificationGaps as R[]).length ? <StatusPill status="error" label={(r.certificationGaps as R[]).map((g) => String(g.typeCode)).join(', ')} /> : 'OK') },
        { key: 'phone', header: 'Phone', render: (r) => (r.phone ? <a href={`tel:${String(r.phone)}`}>{String(r.phone)}</a> : '—') },
      ]} />
    </div>
  );
}

registerEssSection('profile', ({ base }) => <EssProfile base={base} />);
registerEssSection('documents', ({ base }) => <EssDocuments base={base} />);
registerEssSection('training', ({ base }) => <EssTraining base={base} />);
registerEssSection('team', ({ base }) => <EssTeam base={base} />);
// EP-05: My Reviews and Team Reviews (hr_talent.tsx).
for (const [key, view] of TALENT_ESS) registerEssSection(key, view);

/** Renders a registered ESS section (ops /ops/ess/:section, Back Office /ess/:section). */
function EssSectionPage({ base }: { base: string }) {
  const { section = '' } = useParams();
  const view = ESS_VIEWS.get(section);
  if (!view) return <Empty title="Coming soon" help="This part of Employee Self Service arrives with a later release." icon="schedule" action={<Link className="oc-btn oc-btn-neutral" to={base}>Back</Link>} />;
  return <>{view({ base })}</>;
}

// ── registrations ─────────────────────────────────────────────────────────

export const HR_ROUTES: AreaRoute[] = [
  { path: 'hris/employees', perm: 'hris.employee.view', element: <EmployeesPage /> },
  { path: 'hris/employees/:id', perm: 'hris.employee.view', element: <EmployeeDetailPage /> },
  { path: 'hris/contracts', perm: 'hris.contract.view', element: <div className="oc-stack"><ContractsPanel /></div> },
  { path: 'hris/documents', perm: 'hris.employee_document.view', element: <div className="oc-stack"><DocumentsPanel /></div> },
  { path: 'hris/profile-changes', perm: 'hris.profile_change.view', element: <DataChangesPanel /> },
  { path: 'hris/organization', perm: 'hris.org_unit.view', element: <OrganizationPage /> },
  { path: 'hris/training', perm: 'hris.certification.view', element: <TrainingPage /> },
  { path: 'hris/certifications', perm: 'hris.certification.view', element: <TrainingPage /> },
  { path: 'hris/import', perm: 'hris.import.create', element: <ImportPanel /> },
  { path: 'ess', perm: 'hris.ess.use', element: <EssHome base="/ess" /> },
  { path: 'ess/:section', perm: 'hris.ess.use', element: <EssSectionPage base="/ess" /> },
  // EP-03 Recruitment, EP-05 Performance Review (hr_talent.tsx).
  ...TALENT_ROUTES,
  ...GAPS_ROUTES,
];

export const HR_OPS_TILES: OpsTile[] = [['badge', 'Employee Self Service', '/ops/ess', 'hris.ess.use'], ...GAPS_OPS_TILES];

export const HR_OPS_ROUTES: OpsRoute[] = [
  { path: 'ess', element: <EssHome base="/ops/ess" /> },
  { path: 'ess/:section', element: <EssSectionPage base="/ops/ess" /> },
  ...GAPS_OPS_ROUTES,
];
