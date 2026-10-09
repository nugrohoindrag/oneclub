import React, { useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router';
import { download, request, uuidv7, useGet, useSend, type Page } from '@oneclub/api-client';
import { currentLocale, formatDate, formatDateTime } from '@oneclub/i18n';
import {
  AutoResourcePage, Board, BoardCard, Card, Checkbox, DataTable, Empty, ErrorAlert, FilterPills, Icon, Modal, MoneyField, PageHeader, SelectField,
  Skeleton, StatusPill, TextArea, TextField, useAuth, useToast, type Option,
} from '@oneclub/shell';
import { ActionButton, KV, ListPage, Tabs, money, today, type R } from '../p1/common';
import type { AreaRoute } from '../p3/types';

// PRD P5 EP-03 Recruitment and EP-05 Performance Review. Back Office: HRIS → Recruitment (requisitions with approval, pipeline,
// candidates with CV and erasure, interviews & scorecards, offers with approval and letter, hire and onboarding checklist) and
// HRIS → Performance Review (cycles, calibration, reviews, templates). Employee Self Service: My Reviews and, for managers, Team
// Reviews (registered in hr.tsx through registerEssSection). Imported by hr.tsx.

const HR = '/api/v1/hris';
const ESS = '/api/v1/ess';
const INV = [HR, ESS];
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const pill = (k: string) => (r: R) => <StatusPill status={String(r[k] ?? '')} label={label(r[k])} />;
const opts = (vals: string[]): Option[] => vals.map((v) => ({ value: v, label: label(v) }));
const val = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : String(v));
const date = (v: unknown) => (v ? formatDate(String(v)) : '—');
const idem = () => ({ 'Idempotency-Key': uuidv7() });
const clean = (o: Record<string, unknown>) => Object.fromEntries(Object.entries(o).filter(([, v]) => v !== '' && v !== undefined && v !== null));
const list = (v: unknown) => ((v as R[] | undefined) ?? []);
const ID = () => currentLocale() === 'id';

const STAGES = ['applied', 'screening', 'interview', 'offered', 'hired', 'rejected', 'withdrawn'];
const OPEN_STAGES = STAGES.slice(0, 4);
const SOURCES = ['website', 'referral', 'walk_in', 'job_portal', 'agency', 'internal', 'social_media', 'other'];
const RECOMMENDATIONS = ['none', 'salary_increase', 'bonus', 'promotion', 'confirm_employment', 'extend_probation', 'improvement_plan', 'terminate'];

function useOptions(path: string | null, text: (r: R) => string): Option[] {
  const l = useGet<Page<R>>(path);
  return (l.data?.items ?? []).map((x) => ({ value: x.id, label: text(x) }));
}
const useEmployees = () => useOptions(`${HR}/employees?limit=500&filter[status]=active`, (x) => `${String(x.fullName)} (${String(x.employeeNo)})`);
const usePositions = () => useOptions(`${HR}/positions?limit=500&filter[status]=active`, (x) => `${String(x.name)} (${String(x.code)})`);
const useGrades = () => useOptions(`${HR}/grades?limit=50&filter[status]=active`, (x) => `${String(x.code)} · ${String(x.name)}`);
const useUnits = () => useOptions(`${HR}/org-units?limit=500&filter[status]=active`, (x) => `${String(x.name)} (${String(x.code)})`);

/** A form in a modal that POSTs / PATCHes a body. */
function FormModal({ open = true, onClose, title, method = 'POST', path, body, children, submit = 'Save', onDone, wide, idempotent }: {
  open?: boolean; onClose: () => void; title: string; method?: 'POST' | 'PATCH'; path: string; body: () => Record<string, unknown>; children: React.ReactNode;
  submit?: string; onDone?: (r: R) => void; wide?: boolean; idempotent?: boolean;
}) {
  const toast = useToast();
  const send = useSend<Record<string, unknown>, R>(method, path, INV, idempotent ? idem : undefined);
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

/** Uploads a CV and returns its file id. */
function CvUpload({ value, onChange }: { value: string; onChange: (id: string) => void }) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const upload = async (f: File) => {
    setBusy(true);
    setErr(null);
    const fd = new FormData();
    fd.append('file', f);
    try {
      const r = await request<R>('POST', `${HR}/recruitment-files`, fd);
      onChange(String(r.id));
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
        <Icon name="attach_file" size={18} /> {busy ? 'Uploading…' : value ? 'Replace CV (attached)' : 'Attach CV'}
        <input type="file" accept="application/pdf,image/jpeg,image/png,.doc,.docx" className="oc-sr" disabled={busy}
          onChange={(e) => { const f = e.target.files?.[0]; e.target.value = ''; if (f) void upload(f); }} />
      </label>
    </div>
  );
}

// ── Recruitment (EP-03) ───────────────────────────────────────────────────

const RECRUIT_TABS: Option[] = [
  { value: 'requisitions', label: 'Job Requisitions' }, { value: 'applications', label: 'Applications' }, { value: 'candidates', label: 'Candidates' },
  { value: 'interviews', label: 'Interviews' }, { value: 'offers', label: 'Offers' },
];

export function RecruitmentPage() {
  const [params, setParams] = useSearchParams();
  const tab = params.get('tab') ?? 'requisitions';
  return (
    <div className="oc-stack">
      <Tabs tabs={RECRUIT_TABS} value={tab} onChange={(v) => setParams({ tab: v })} />
      {tab === 'requisitions' && <RequisitionList />}
      {tab === 'applications' && <ApplicationList />}
      {tab === 'candidates' && <CandidateList />}
      {tab === 'interviews' && <InterviewList />}
      {tab === 'offers' && <OfferList />}
    </div>
  );
}

function RequisitionList() {
  const nav = useNavigate();
  const { can } = useAuth();
  const [open, setOpen] = useState(false);
  return (
    <>
      <ListPage title="Job Requisitions" help="Staff requests of the department heads with headcount and approval (FR-RCT-01). Open public requisitions show on the website careers page."
        path={`${HR}/job-requisitions`} statuses={opts(['draft', 'submitted', 'open', 'on_hold', 'filled', 'closed', 'rejected'])}
        actions={can('hris.job_requisition.create') && <button className="oc-btn oc-btn-ink" onClick={() => setOpen(true)}><Icon name="person_add" size={18} /> Request Staff</button>}
        onRowClick={(r) => nav(`/hris/recruitment/requisitions/${r.id}`)}
        columns={[
          { key: 'number', header: 'No.' }, { key: 'title', header: 'Position' }, { key: 'orgUnitName', header: 'Org Unit' },
          { key: 'headcount', header: 'Hired / Headcount', render: (r) => `${String(r.hiredCount)} / ${String(r.headcount)}` },
          { key: 'inPipeline', header: 'In Pipeline' }, { key: 'isPublic', header: 'Careers Page', render: (r) => (r.isPublic ? 'Yes' : 'No') },
          { key: 'daysOpen', header: 'Days Open', render: (r) => val(r.daysOpen) }, { key: 'status', header: 'Status', render: pill('status') },
        ]} />
      {open && <RequisitionForm onClose={() => setOpen(false)} onDone={(r) => nav(`/hris/recruitment/requisitions/${r.id}`)} />}
    </>
  );
}

function RequisitionForm({ onClose, onDone, edit }: { onClose: () => void; onDone?: (r: R) => void; edit?: R }) {
  const positions = usePositions();
  const employees = useEmployees();
  const [f, setF] = useState<Record<string, string>>((): Record<string, string> => edit ? {
    title: String(edit.title ?? ''), headcount: String(edit.headcount ?? '1'), contractType: String(edit.contractType ?? 'pkwt'),
    workerCategory: String(edit.workerCategory ?? 'regular'), reason: String(edit.reason ?? 'new_position'), location: String(edit.location ?? ''),
    description: String(edit.description ?? ''), requirements: String(edit.requirements ?? ''), publishUntil: String(edit.publishUntil ?? '').slice(0, 10),
    targetStartDate: String(edit.targetStartDate ?? '').slice(0, 10), isPublic: edit.isPublic ? 'true' : '',
  } : { headcount: '1', contractType: 'pkwt', workerCategory: 'regular', reason: 'new_position' });
  const set = (k: string) => (v: string) => setF({ ...f, [k]: v });
  const locked = !!edit && !['draft', 'rejected'].includes(String(edit.status));
  const body = () => {
    const b: Record<string, unknown> = clean({ ...f, headcount: f.headcount ? Number(f.headcount) : undefined, isPublic: undefined });
    b.isPublic = f.isPublic === 'true';
    if (locked) for (const k of ['title', 'positionId', 'headcount', 'contractType', 'workerCategory', 'reason', 'salaryMin', 'salaryMax', 'replacementForId']) delete b[k];
    return b;
  };
  return (
    <FormModal title={edit ? 'Edit Requisition' : 'Request Staff'} method={edit ? 'PATCH' : 'POST'} path={edit ? `${HR}/job-requisitions/${edit.id}` : `${HR}/job-requisitions`}
      body={body} onClose={onClose} onDone={onDone} wide idempotent={!edit}>
      {!locked && <>
        {!edit && <SelectField label="Position" value={f.positionId ?? ''} onChange={set('positionId')} options={positions} placeholder="—" />}
        <TextField label="Title" value={f.title ?? ''} onChange={set('title')} placeholder="Default: the position" />
        <TextField label="Headcount" type="number" min={1} value={f.headcount} onChange={set('headcount')} required />
        <SelectField label="Contract" value={f.contractType} onChange={set('contractType')} options={[{ value: 'pkwt', label: 'PKWT (fixed term)' }, { value: 'pkwtt', label: 'PKWTT (permanent)' }]} />
        <SelectField label="Worker category" value={f.workerCategory} onChange={set('workerCategory')} options={opts(['regular', 'daily', 'intern'])} />
        <SelectField label="Reason" value={f.reason} onChange={set('reason')} options={opts(['new_position', 'replacement', 'seasonal', 'other'])} />
        {f.reason === 'replacement' && <SelectField label="Replaces" value={f.replacementForId ?? ''} onChange={set('replacementForId')} options={employees} placeholder="—" />}
        <MoneyField label="Salary budget from" value={f.salaryMin ?? ''} onChange={set('salaryMin')} />
        <MoneyField label="Salary budget to" value={f.salaryMax ?? ''} onChange={set('salaryMax')} />
      </>}
      <SelectField label="Hiring manager" value={f.hiringManagerId ?? ''} onChange={set('hiringManagerId')} options={employees} placeholder={edit ? 'Unchanged' : 'Me'} />
      <TextField label="Target start" type="date" value={f.targetStartDate ?? ''} onChange={set('targetStartDate')} />
      <TextField label="Location" value={f.location ?? ''} onChange={set('location')} />
      <TextArea label="Job description" value={f.description ?? ''} onChange={set('description')} span />
      <TextArea label="Requirements" value={f.requirements ?? ''} onChange={set('requirements')} span />
      <Checkbox label="Publish on the website careers page once open" checked={f.isPublic === 'true'} onChange={(v) => set('isPublic')(v ? 'true' : '')} />
      <TextField label="Publish until" type="date" value={f.publishUntil ?? ''} onChange={set('publishUntil')} />
    </FormModal>
  );
}

export function RequisitionDetailPage() {
  const { id = '' } = useParams();
  const nav = useNavigate();
  const { can } = useAuth();
  const q = useGet<R>(`${HR}/job-requisitions/${id}`);
  const apps = useGet<Page<R>>(`${HR}/applications?requisitionId=${id}`);
  const [edit, setEdit] = useState(false);
  const [add, setAdd] = useState(false);
  if (q.isLoading) return <Skeleton rows={8} />;
  if (q.error || !q.data) return <ErrorAlert error={q.error} />;
  const r = q.data;
  const st = String(r.status);
  const base = `${HR}/job-requisitions/${id}`;
  return (
    <div className="oc-stack">
      <PageHeader title={`${String(r.title)} · ${String(r.number)}`} help={`${String(r.orgUnitName)} · ${String(r.hiredCount)} of ${String(r.headcount)} hired`} actions={
        <div className="oc-row-wrap">
          {['draft', 'rejected', 'open', 'on_hold', 'submitted'].includes(st) && can('hris.job_requisition.update') && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setEdit(true)}>Edit</button>}
          {['draft', 'rejected'].includes(st) && can('hris.job_requisition.submit') && <ActionButton label="Submit for Approval" kind="ink" path={`${base}:submit`} invalidate={INV} />}
          {st === 'submitted' && can('hris.job_requisition.approve') && <ActionButton label="Approve" kind="ink" path={`${base}:approve`} invalidate={INV} reason="optional" />}
          {st === 'submitted' && can('hris.job_requisition.approve') && <ActionButton label="Reject" danger path={`${base}:reject`} invalidate={INV} reason="required" />}
          {st === 'open' && can('hris.job_requisition.close') && <ActionButton label="Hold" path={`${base}:hold`} invalidate={INV} reason="optional" />}
          {['on_hold', 'closed'].includes(st) && can('hris.job_requisition.close') && <ActionButton label="Reopen" path={`${base}:reopen`} invalidate={INV} />}
          {['draft', 'rejected', 'submitted', 'open', 'on_hold'].includes(st) && can('hris.job_requisition.close') && <ActionButton label="Close" danger path={`${base}:close`} invalidate={INV} reason="required" />}
        </div>
      } />
      <Card title="Requisition" icon="work">
        <KV items={[
          ['Status', <StatusPill key="s" status={st} label={label(st)} />], ['Position', val(r.positionName)], ['Grade', val(r.gradeCode)],
          ['Hiring manager', val(r.hiringManagerName)], ['Contract', `${String(r.contractType).toUpperCase()} · ${label(r.workerCategory)}`], ['Reason', label(r.reason)],
          ['Salary budget', r.salaryMax ? `${money(r.salaryMin ?? 0)} – ${money(r.salaryMax)}` : '—'], ['Target start', date(r.targetStartDate)],
          ['Careers page', r.isPublic ? `Yes${r.publishUntil ? ` until ${date(r.publishUntil)}` : ''}` : 'No'], ['Requested by', val(r.requestedByName)],
          ['Approved', r.approvedAt ? formatDateTime(String(r.approvedAt)) : '—'], ['Decision note', val(r.decisionNote ?? r.closeReason)],
        ]} />
        {!!r.description && <p style={{ whiteSpace: 'pre-wrap' }}>{String(r.description)}</p>}
      </Card>
      <Card title="Pipeline" icon="view_kanban" actions={st === 'open' && can('hris.application.manage') && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setAdd(true)}>Add Application</button>}>
        {apps.isLoading ? <Skeleton rows={3} /> : (
          <Board label="Recruitment stages" empty="No candidate" lanes={STAGES.map((s) => {
            const items = (apps.data?.items ?? []).filter((a) => a.stage === s);
            return {
              key: s, title: label(s), count: items.length,
              tone: s === 'hired' ? 'green' : s === 'rejected' || s === 'withdrawn' ? 'red' : undefined,
              children: items.map((a) => (
                <BoardCard key={a.id} tag={String(a.number)} title={String(a.candidateName)} onOpen={() => nav(`/hris/recruitment/applications/${a.id}`)}
                  chips={[{ icon: 'campaign', text: label(a.source) }, !!a.rating && { icon: 'star', text: String(a.rating), tone: 'blue' }]} />
              )),
            };
          })} />
        )}
      </Card>
      {edit && <RequisitionForm edit={r} onClose={() => setEdit(false)} />}
      {add && <AddApplication requisitionId={id} onClose={() => setAdd(false)} onDone={(a) => nav(`/hris/recruitment/applications/${a.id}`)} />}
    </div>
  );
}

function AddApplication({ requisitionId, onClose, onDone }: { requisitionId: string; onClose: () => void; onDone: (r: R) => void }) {
  const candidates = useOptions(`${HR}/candidates?limit=500&filter[status]=active`, (x) => `${String(x.fullName)} · ${val(x.email ?? x.phone)}`);
  const [f, setF] = useState<Record<string, string>>({});
  return (
    <FormModal title="Add Application" path={`${HR}/applications`} body={() => clean({ ...f, requisitionId })} onClose={onClose} onDone={onDone} idempotent>
      <SelectField label="Candidate" value={f.candidateId ?? ''} onChange={(v) => setF({ ...f, candidateId: v })} options={candidates} placeholder="—" required />
      <SelectField label="Source" value={f.source ?? ''} onChange={(v) => setF({ ...f, source: v })} options={opts(SOURCES)} placeholder="The candidate's" />
      <TextArea label="Note / cover letter" value={f.coverLetter ?? ''} onChange={(v) => setF({ ...f, coverLetter: v })} span />
    </FormModal>
  );
}

function ApplicationList() {
  const nav = useNavigate();
  const [stage, setStage] = useState('');
  return (
    <ListPage title="Applications" help="Candidates in the pipeline Applied → Screening → Interview → Offered → Hired, or Rejected / Withdrawn (FR-RCT-02)."
      path={`${HR}/applications`} extraQuery={stage ? { stage } : { open: 'true' }}
      filters={<FilterPills options={[{ value: '', label: 'In pipeline' }, ...opts(STAGES)]} value={stage} onChange={setStage} />}
      onRowClick={(r) => nav(`/hris/recruitment/applications/${r.id}`)}
      columns={[
        { key: 'number', header: 'No.' }, { key: 'candidateName', header: 'Candidate' }, { key: 'requisitionTitle', header: 'Position' },
        { key: 'orgUnitName', header: 'Org Unit' }, { key: 'source', header: 'Source', render: (r) => label(r.source) }, { key: 'stage', header: 'Stage', render: pill('stage') },
        { key: 'daysInStage', header: 'Days in Stage' }, { key: 'rating', header: 'Rating', render: (r) => val(r.rating) },
        { key: 'appliedAt', header: 'Applied', render: (r) => date(r.appliedAt) },
      ]} />
  );
}

function CandidateList() {
  const { can } = useAuth();
  const toast = useToast();
  const [open, setOpen] = useState<R | null>(null);
  const [create, setCreate] = useState(false);
  return (
    <>
      <ListPage title="Candidates" help="Applicants with their consent (UU PDP). Applicants who were not hired are erased after the retention period (talent pool consent: HR Configuration, otherwise Recruitment Configuration)."
        path={`${HR}/candidates`} statuses={opts(['active', 'hired', 'erased'])}
        actions={can('hris.candidate.create') && <button className="oc-btn oc-btn-ink" onClick={() => setCreate(true)}><Icon name="person_add" size={18} /> Add Candidate</button>}
        onRowClick={(r) => setOpen(r)}
        rowActions={(r) => (r.cvFileId ? <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => download('GET', `${HR}/candidates/${r.id}/cv`, undefined, `CV ${String(r.fullName)}`).catch((e: Error) => toast(e.message, 'error'))}>CV</button> : null)}
        columns={[
          { key: 'fullName', header: 'Name' }, { key: 'email', header: 'E-mail', render: (r) => val(r.email) }, { key: 'phone', header: 'Phone', render: (r) => val(r.phone) },
          { key: 'source', header: 'Source', render: (r) => label(r.source) }, { key: 'talentPoolConsent', header: 'Talent Pool', render: (r) => (r.talentPoolConsent ? 'Yes' : 'No') },
          { key: 'retentionUntil', header: 'Erased On', render: (r) => date(r.retentionUntil) }, { key: 'status', header: 'Status', render: pill('status') },
        ]} />
      {create && <CandidateForm onClose={() => setCreate(false)} />}
      {open && <CandidateForm edit={open} onClose={() => setOpen(null)} />}
    </>
  );
}

function CandidateForm({ edit, onClose }: { edit?: R; onClose: () => void }) {
  const { can } = useAuth();
  const [f, setF] = useState<Record<string, string>>(() => {
    const out: Record<string, string> = { source: 'walk_in' };
    if (edit) for (const k of ['fullName', 'email', 'phone', 'city', 'education', 'currentEmployer', 'currentTitle', 'experienceYears', 'expectedSalary', 'source', 'notes', 'cvFileId']) {
      if (edit[k] !== null && edit[k] !== undefined) out[k] = String(edit[k]);
    }
    if (edit?.talentPoolConsent) out.talentPoolConsent = 'true';
    return out;
  });
  const [consent, setConsent] = useState(!!edit?.consentAt);
  const set = (k: string) => (v: string) => setF({ ...f, [k]: v });
  const erased = edit?.status === 'erased';
  if (erased) {
    return <Modal open onClose={onClose} title="Erased applicant"><p>The personal data of this applicant was erased on {date(edit?.erasedAt)} (UU PDP).</p></Modal>;
  }
  return (
    <FormModal title={edit ? String(edit.fullName) : 'Add Candidate'} method={edit ? 'PATCH' : 'POST'} path={edit ? `${HR}/candidates/${edit.id}` : `${HR}/candidates`} wide idempotent={!edit}
      body={() => {
        const b: Record<string, unknown> = clean({ ...f, talentPoolConsent: undefined });
        b.talentPoolConsent = f.talentPoolConsent === 'true';
        if (b.expectedSalary === '[REDACTED]') delete b.expectedSalary;
        if (consent && !edit?.consentAt) b.consentAt = new Date().toISOString();
        return b;
      }} onClose={onClose}>
      <TextField label="Full name" value={f.fullName ?? ''} onChange={set('fullName')} required />
      <TextField label="E-mail" type="email" value={f.email ?? ''} onChange={set('email')} />
      <TextField label="Phone" value={f.phone ?? ''} onChange={set('phone')} />
      <TextField label="City" value={f.city ?? ''} onChange={set('city')} />
      <SelectField label="Education" value={f.education ?? ''} onChange={set('education')} options={opts(['sd', 'smp', 'sma', 'd1', 'd3', 's1', 's2', 's3', 'other']).map((o) => ({ ...o, label: o.label.toUpperCase() }))} placeholder="—" />
      <TextField label="Current employer" value={f.currentEmployer ?? ''} onChange={set('currentEmployer')} />
      <TextField label="Current job title" value={f.currentTitle ?? ''} onChange={set('currentTitle')} />
      <TextField label="Experience (years)" inputMode="decimal" value={f.experienceYears ?? ''} onChange={set('experienceYears')} />
      {can('hris.candidate.view_sensitive') && <MoneyField label="Expected salary" value={f.expectedSalary ?? ''} onChange={set('expectedSalary')} />}
      <SelectField label="Source" value={f.source ?? ''} onChange={set('source')} options={opts(SOURCES)} />
      <TextArea label="Notes" value={f.notes ?? ''} onChange={set('notes')} span />
      <CvUpload value={f.cvFileId ?? ''} onChange={set('cvFileId')} />
      <Checkbox label="The candidate consented to the processing of the application data" checked={consent} onChange={setConsent} disabled={!!edit?.consentAt} />
      <Checkbox label="Talent pool consent (keep for other positions)" checked={f.talentPoolConsent === 'true'} onChange={(v) => set('talentPoolConsent')(v ? 'true' : '')} />
      {edit && can('hris.candidate.erase') && <ActionButton label="Erase personal data" danger path={`${HR}/candidates/${edit.id}:erase`} invalidate={INV} reason="required"
        confirm="Erase the name, contact, CV and notes of this applicant (UU PDP request)? This cannot be undone." onDone={onClose} />}
    </FormModal>
  );
}

function InterviewList() {
  const nav = useNavigate();
  const [mine, setMine] = useState('');
  return (
    <ListPage title="Interviews" help="Interview calendar with the scorecards of the interviewers (FR-RCT-03)." path={`${HR}/interviews`} search={false}
      extraQuery={mine ? { mine: 'true' } : {}} filters={<FilterPills options={[{ value: '', label: 'All' }, { value: 'mine', label: 'Where I interview' }]} value={mine} onChange={setMine} />}
      statuses={opts(['scheduled', 'completed', 'cancelled', 'no_show'])} onRowClick={(r) => nav(`/hris/recruitment/applications/${String(r.applicationId)}`)}
      columns={[
        { key: 'scheduledAt', header: 'When', render: (r) => formatDateTime(String(r.scheduledAt)) }, { key: 'candidateName', header: 'Candidate' },
        { key: 'title', header: 'Position' }, { key: 'round', header: 'Round' }, { key: 'interviewType', header: 'Type', render: (r) => label(r.interviewType) },
        { key: 'interviewers', header: 'Interviewers', render: (r) => list(r.interviewers).join(', ') }, { key: 'status', header: 'Status', render: pill('status') },
        { key: 'canScore', header: 'My scorecard', render: (r) => (r.canScore ? <StatusPill status="pending" label="To do" /> : '—') },
      ]} />
  );
}

function OfferList() {
  const nav = useNavigate();
  return (
    <ListPage title="Offers" help="Job offers with approval, offer letter and the candidate's answer (FR-RCT-04)." path={`${HR}/job-offers`} search={false}
      statuses={opts(['draft', 'submitted', 'approved', 'sent', 'accepted', 'declined', 'rejected', 'expired'])}
      onRowClick={(r) => nav(`/hris/recruitment/applications/${String(r.applicationId)}`)}
      columns={[
        { key: 'number', header: 'No.' }, { key: 'candidateName', header: 'Candidate' }, { key: 'jobTitle', header: 'Position', render: (r) => val(r.jobTitle) },
        { key: 'contractType', header: 'Contract', render: (r) => String(r.contractType).toUpperCase() }, { key: 'startDate', header: 'Start', render: (r) => date(r.startDate) },
        { key: 'baseSalary', header: 'Base Salary', render: (r) => (String(r.baseSalary).includes('*') || String(r.baseSalary).includes('REDACTED') ? '•••' : money(r.baseSalary)) },
        { key: 'expiresOn', header: 'Expires', render: (r) => date(r.expiresOn) }, { key: 'status', header: 'Status', render: pill('status') },
      ]} />
  );
}

export function ApplicationDetailPage() {
  const { id = '' } = useParams();
  const { can } = useAuth();
  const toast = useToast();
  const q = useGet<R>(`${HR}/applications/${id}`);
  const [modal, setModal] = useState<'' | 'interview' | 'offer' | 'hire'>('');
  const [score, setScore] = useState<R | null>(null);
  const [hired, setHired] = useState<R | null>(null);
  if (q.isLoading) return <Skeleton rows={8} />;
  if (q.error || !q.data) return <ErrorAlert error={q.error} />;
  const d = q.data;
  const a = d.application as R;
  const c = d.candidate as R;
  const stage = String(a.stage);
  const base = `${HR}/applications/${id}`;
  const open = OPEN_STAGES.includes(stage);
  const offers = list(d.offers);
  const liveOffer = offers.find((o) => ['draft', 'submitted', 'approved', 'sent', 'accepted', 'rejected'].includes(String(o.status)));
  return (
    <div className="oc-stack">
      <PageHeader title={`${String(c.fullName)} · ${String(a.requisitionTitle)}`} help={`${String(a.number)} · ${String(a.orgUnitName)} · applied ${date(a.appliedAt)}`} actions={
        <div className="oc-row-wrap">
          {['applied'].includes(stage) && can('hris.application.manage') && <ActionButton label="To Screening" path={`${base}:move-stage`} body={{ stage: 'screening' }} invalidate={INV} />}
          {['applied', 'screening'].includes(stage) && can('hris.interview.manage') && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setModal('interview')}>Schedule Interview</button>}
          {stage === 'interview' && can('hris.interview.manage') && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setModal('interview')}>Next Round</button>}
          {['screening', 'interview'].includes(stage) && can('hris.job_offer.manage') && !liveOffer && <button className="oc-btn oc-btn-ink oc-btn-sm" onClick={() => setModal('offer')}>Make Offer</button>}
          {stage === 'offered' && can('hris.application.hire') && liveOffer?.status === 'accepted' && <button className="oc-btn oc-btn-ink oc-btn-sm" onClick={() => setModal('hire')}>Hire</button>}
          {open && can('hris.application.manage') && <ActionButton label="Withdrawn" path={`${base}:withdraw`} invalidate={INV} reason="required" />}
          {open && can('hris.application.manage') && <ActionButton label="Reject" danger path={`${base}:reject`} invalidate={INV} reason="required" />}
        </div>
      } />
      <div className="oc-grid">
        <Card title="Candidate" icon="person">
          <KV items={[['Stage', <StatusPill key="s" status={stage} label={label(stage)} />], ['E-mail', val(c.email)], ['Phone', val(c.phone)], ['City', val(c.city)],
            ['Education', val(c.education).toUpperCase()], ['Current', [c.currentTitle, c.currentEmployer].filter(Boolean).join(' · ') || '—'],
            ['Experience', c.experienceYears ? `${String(c.experienceYears)} years` : '—'], ['Source', label(a.source)], ['Rating', val(a.rating)],
            ['Consent', c.consentAt ? formatDateTime(String(c.consentAt)) : '—'], ['Talent pool', c.talentPoolConsent ? 'Yes' : 'No'],
            ['Erased on', date(c.retentionUntil)]]} />
          {!!c.hasCv && can('hris.candidate.view_sensitive') && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => download('GET', `${HR}/candidates/${String(c.id)}/cv`, undefined, `CV ${String(c.fullName)}`).catch((e: Error) => toast(e.message, 'error'))}><Icon name="description" size={18} /> CV</button>}
          {!!a.coverLetter && <p style={{ whiteSpace: 'pre-wrap' }}>{String(a.coverLetter)}</p>}
        </Card>
        <Card title="History" icon="timeline">
          <ol className="oc-stack" style={{ gap: 4, paddingLeft: 18, margin: 0 }}>
            {list(d.history).map((h) => <li key={String(h.id)}>{formatDateTime(String(h.createdAt))} · <strong>{label(h.toStage)}</strong>{h.note ? ` — ${String(h.note)}` : ''}{h.byName ? ` (${String(h.byName)})` : ''}</li>)}
          </ol>
        </Card>
      </div>
      <Card title="Interviews" icon="groups">
        <DataTable rows={list(d.interviews)} empty={<p className="oc-muted">No interview yet.</p>} columns={[
          { key: 'round', header: 'Round' }, { key: 'scheduledAt', header: 'When', render: (r) => formatDateTime(String(r.scheduledAt)) },
          { key: 'interviewType', header: 'Type', render: (r) => label(r.interviewType) }, { key: 'interviewers', header: 'Interviewers', render: (r) => list(r.interviewers).join(', ') },
          { key: 'scorecards', header: 'Scorecards', render: (r) => list(r.scorecards).map((s) => `${String(s.interviewerName)}: ${String(s.overallScore)} (${label(s.recommendation)})`).join('; ') || '—' },
          { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} label={`${label(r.status)}${r.result ? ` · ${String(r.result)}` : ''}${r.score ? ` · ${String(r.score)}` : ''}`} /> },
        ]} actions={(r) => (
          <div className="oc-row-wrap">
            {!!r.canScore && <button className="oc-btn oc-btn-ink oc-btn-sm" onClick={() => setScore(r)}>Scorecard</button>}
            {r.status === 'scheduled' && can('hris.interview.manage') && <ActionButton label="Complete" path={`${HR}/interviews/${r.id}:complete`} invalidate={INV} />}
            {r.status === 'scheduled' && can('hris.interview.manage') && <ActionButton label="No-show" path={`${HR}/interviews/${r.id}:complete`} body={{ noShow: true }} invalidate={INV} />}
            {r.status === 'scheduled' && can('hris.interview.manage') && <ActionButton label="Cancel" danger path={`${HR}/interviews/${r.id}:cancel`} invalidate={INV} reason="required" />}
          </div>
        )} />
      </Card>
      <Card title="Offers" icon="handshake">
        <DataTable rows={offers} empty={<p className="oc-muted">No offer yet.</p>} columns={[
          { key: 'number', header: 'No.' }, { key: 'jobTitle', header: 'Position', render: (r) => val(r.jobTitle) },
          { key: 'contractType', header: 'Contract', render: (r) => `${String(r.contractType).toUpperCase()}${r.endDate ? ` until ${date(r.endDate)}` : ''}${Number(r.probationMonths) ? ` · probation ${String(r.probationMonths)} mo` : ''}` },
          { key: 'startDate', header: 'Start', render: (r) => date(r.startDate) },
          { key: 'baseSalary', header: 'Base Salary', render: (r) => (String(r.baseSalary).includes('REDACTED') ? '•••' : money(r.baseSalary)) },
          { key: 'status', header: 'Status', render: pill('status') }, { key: 'decisionNote', header: 'Note', render: (r) => val(r.decisionNote ?? r.responseNote) },
        ]} actions={(o) => {
          const p = `${HR}/job-offers/${o.id}`;
          const s = String(o.status);
          return (
            <div className="oc-row-wrap">
              {can('hris.job_offer.view_salary') && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => download('GET', `${p}/letter`, undefined, `Offer ${String(o.number)}.pdf`).catch((e: Error) => toast(e.message, 'error'))}>Letter</button>}
              {['draft', 'rejected'].includes(s) && can('hris.job_offer.manage') && <ActionButton label="Submit" kind="ink" path={`${p}:submit`} invalidate={INV} />}
              {s === 'submitted' && can('hris.job_offer.approve') && <ActionButton label="Approve" kind="ink" path={`${p}:approve`} invalidate={INV} reason="optional" />}
              {s === 'submitted' && can('hris.job_offer.approve') && <ActionButton label="Reject" danger path={`${p}:reject`} invalidate={INV} reason="required" />}
              {s === 'approved' && can('hris.job_offer.manage') && <ActionButton label="Send" kind="ink" path={`${p}:send`} body={{ email: true }} invalidate={INV} />}
              {['sent', 'approved'].includes(s) && can('hris.job_offer.manage') && <ActionButton label="Accepted" kind="ink" path={`${p}:accept`} invalidate={INV} reason="optional" />}
              {['sent', 'approved'].includes(s) && can('hris.job_offer.manage') && <ActionButton label="Declined" path={`${p}:decline`} invalidate={INV} reason="required" />}
              {['draft', 'submitted', 'approved', 'sent', 'accepted', 'rejected'].includes(s) && can('hris.job_offer.manage') && <ActionButton label="Cancel" danger path={`${p}:cancel`} invalidate={INV} reason="required" />}
            </div>
          );
        }} />
      </Card>
      {stage === 'hired' && !!a.employeeId && <Onboarding employeeId={String(a.employeeId)} />}
      {hired && <div className="oc-alert oc-alert-success" role="status">Hired as {String((hired.employee as R).employeeNo)}. <Link to={`/hris/employees/${String((hired.employee as R).employeeId)}`}>Open the employee</Link></div>}
      {modal === 'interview' && <InterviewForm applicationId={id} onClose={() => setModal('')} />}
      {modal === 'offer' && <OfferForm applicationId={id} contractType="" onClose={() => setModal('')} />}
      {modal === 'hire' && <HireForm applicationId={id} email={String(c.email ?? '')} onClose={() => setModal('')} onDone={setHired} />}
      {score && <ScorecardForm interview={score} criteria={list(d.criteria)} scale={Number(d.scoreScale ?? 5)} onClose={() => setScore(null)} />}
    </div>
  );
}

function InterviewForm({ applicationId, onClose }: { applicationId: string; onClose: () => void }) {
  const employees = useEmployees();
  const [f, setF] = useState<Record<string, string>>({ interviewType: 'onsite', durationMinutes: '60', at: `${today()}T10:00` });
  const [who, setWho] = useState<string[]>([]);
  const [invite, setInvite] = useState(true);
  const set = (k: string) => (v: string) => setF({ ...f, [k]: v });
  return (
    <FormModal title="Schedule Interview" path={`${HR}/applications/${applicationId}/interviews`} onClose={onClose} idempotent wide
      body={() => clean({ scheduledAt: f.at ? new Date(f.at).toISOString() : '', durationMinutes: Number(f.durationMinutes), interviewType: f.interviewType,
        location: f.location, interviewerIds: who, inviteCandidate: invite })}>
      <TextField label="Date & time" type="datetime-local" value={f.at} onChange={set('at')} required />
      <TextField label="Duration (minutes)" type="number" value={f.durationMinutes} onChange={set('durationMinutes')} />
      <SelectField label="Type" value={f.interviewType} onChange={set('interviewType')} options={opts(['phone', 'video', 'onsite', 'panel', 'practical'])} />
      <TextField label="Location / link" value={f.location ?? ''} onChange={set('location')} />
      <SelectField label="Add interviewer" value="" onChange={(v) => v && !who.includes(v) && setWho([...who, v])} options={employees} placeholder="—" />
      <div className="oc-row-wrap oc-span">{who.map((w) => <button key={w} className="oc-chip" onClick={() => setWho(who.filter((x) => x !== w))} aria-label="Remove interviewer">{employees.find((e) => e.value === w)?.label ?? w} ✕</button>)}</div>
      <Checkbox label="E-mail the invitation to the candidate" checked={invite} onChange={setInvite} />
    </FormModal>
  );
}

function ScorecardForm({ interview, criteria, scale, onClose }: { interview: R; criteria: R[]; scale: number; onClose: () => void }) {
  const [scores, setScores] = useState<Record<string, string>>({});
  const [rec, setRec] = useState('yes');
  const [comments, setComments] = useState('');
  const scaleOpts = Array.from({ length: scale }, (_, i) => ({ value: String(i + 1), label: String(i + 1) }));
  return (
    <FormModal title={`Scorecard · round ${String(interview.round)}`} path={`${HR}/interviews/${interview.id}/scorecards`} onClose={onClose} idempotent submit="Submit"
      body={() => ({ scores: criteria.map((c) => ({ code: c.code, score: scores[String(c.code)] ?? '' })), recommendation: rec, comments })}>
      {criteria.map((c) => <SelectField key={String(c.code)} label={`${String(c.label)} (×${String(c.weight)})`} value={scores[String(c.code)] ?? ''} onChange={(v) => setScores({ ...scores, [String(c.code)]: v })} options={scaleOpts} placeholder="—" required />)}
      <SelectField label="Recommendation" value={rec} onChange={setRec} options={opts(['strong_yes', 'yes', 'no', 'strong_no'])} />
      <TextArea label="Comments" value={comments} onChange={setComments} span />
    </FormModal>
  );
}

function OfferForm({ applicationId, onClose }: { applicationId: string; contractType: string; onClose: () => void }) {
  const positions = usePositions();
  const grades = useGrades();
  const [f, setF] = useState<Record<string, string>>({ contractType: 'pkwt', workWeekDays: '5' });
  const [allow, setAllow] = useState<{ name: string; amount: string }[]>([]);
  const set = (k: string) => (v: string) => setF({ ...f, [k]: v });
  return (
    <FormModal title="Make Offer" path={`${HR}/applications/${applicationId}:offer`} onClose={onClose} idempotent wide submit="Submit for approval"
      body={() => clean({ ...f, probationMonths: f.probationMonths ? Number(f.probationMonths) : undefined, workWeekDays: Number(f.workWeekDays),
        allowances: allow.filter((x) => x.name && x.amount), endDate: f.contractType === 'pkwt' ? f.endDate : undefined })}>
      <SelectField label="Position" value={f.positionId ?? ''} onChange={set('positionId')} options={positions} placeholder="The requisition's" />
      <SelectField label="Grade" value={f.gradeId ?? ''} onChange={set('gradeId')} options={grades} placeholder="The position's" />
      <TextField label="Job title" value={f.jobTitle ?? ''} onChange={set('jobTitle')} />
      <SelectField label="Contract" value={f.contractType} onChange={set('contractType')} options={[{ value: 'pkwt', label: 'PKWT (fixed term)' }, { value: 'pkwtt', label: 'PKWTT (permanent)' }]} />
      <TextField label="Start date" type="date" value={f.startDate ?? ''} onChange={set('startDate')} required />
      {f.contractType === 'pkwt' ? <TextField label="End date" type="date" value={f.endDate ?? ''} onChange={set('endDate')} required />
        : <TextField label="Probation (months)" type="number" min={0} max={3} value={f.probationMonths ?? ''} onChange={set('probationMonths')} />}
      <MoneyField label="Base salary" value={f.baseSalary ?? ''} onChange={set('baseSalary')} required />
      <SelectField label="Work week" value={f.workWeekDays} onChange={set('workWeekDays')} options={[{ value: '5', label: '5 days' }, { value: '6', label: '6 days' }]} />
      {allow.map((x, i) => (
        <React.Fragment key={i}>
          <TextField label={`Allowance ${i + 1}`} value={x.name} onChange={(v) => setAllow(allow.map((y, j) => (j === i ? { ...y, name: v } : y)))} />
          <MoneyField label="Amount" value={x.amount} onChange={(v) => setAllow(allow.map((y, j) => (j === i ? { ...y, amount: v } : y)))} />
        </React.Fragment>
      ))}
      <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => setAllow([...allow, { name: '', amount: '' }])}><Icon name="add" size={18} /> Allowance</button>
      <TextArea label="Notes" value={f.notes ?? ''} onChange={set('notes')} span />
    </FormModal>
  );
}

function HireForm({ applicationId, email, onClose, onDone }: { applicationId: string; email: string; onClose: () => void; onDone: (r: R) => void }) {
  const employees = useEmployees();
  const [f, setF] = useState<Record<string, string>>({ workEmail: '' });
  const [login, setLogin] = useState(true);
  return (
    <FormModal title="Hire" path={`${HR}/applications/${applicationId}:hire`} onClose={onClose} onDone={onDone} submit="Hire"
      body={() => clean({ ...f, createLogin: login })}>
      <p className="oc-span oc-muted">Creates the employee, the active contract of the accepted offer{login ? ', the Employee Self Service login' : ''} and the onboarding checklist.</p>
      <TextField label="Employee No." value={f.employeeNo ?? ''} onChange={(v) => setF({ ...f, employeeNo: v })} placeholder="Next number" />
      <TextField label="Work e-mail" type="email" value={f.workEmail} onChange={(v) => setF({ ...f, workEmail: v })} placeholder={email || 'none'} />
      <SelectField label="Supervisor" value={f.supervisorId ?? ''} onChange={(v) => setF({ ...f, supervisorId: v })} options={employees} placeholder="The hiring manager" />
      <Checkbox label="Create the Employee Self Service login" checked={login} onChange={setLogin} />
    </FormModal>
  );
}

function Onboarding({ employeeId }: { employeeId: string }) {
  const items = useGet<Page<R>>(`${HR}/employees/${employeeId}/onboarding`);
  const send = useSend<Record<string, unknown>, R>('PATCH', (b) => `${HR}/onboarding-items/${String(b.id)}`, INV);
  return (
    <Card title="Onboarding checklist" icon="checklist" actions={<Link className="oc-btn oc-btn-text oc-btn-sm" to={`/hris/employees/${employeeId}`}>Employee</Link>}>
      <ErrorAlert error={send.error} />
      <DataTable rows={items.data?.items} loading={items.isLoading} error={items.error} columns={[
        { key: 'label', header: 'Item' }, { key: 'dueDate', header: 'Due', render: (r) => date(r.dueDate) }, { key: 'status', header: 'Status', render: pill('status') },
        { key: 'doneByName', header: 'Done by', render: (r) => val(r.doneByName) },
      ]} actions={(r) => (
        <div className="oc-row-wrap">
          {r.status !== 'done' && <button className="oc-btn oc-btn-neutral oc-btn-sm" style={{ minHeight: 40 }} onClick={() => send.mutate({ id: r.id, status: 'done' })}>Done</button>}
          {r.status === 'pending' && <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => send.mutate({ id: r.id, status: 'not_applicable' })}>N/A</button>}
          {r.status !== 'pending' && <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => send.mutate({ id: r.id, status: 'pending' })}>Undo</button>}
        </div>
      )} />
    </Card>
  );
}

// ── Performance Review (EP-05) ────────────────────────────────────────────

const PERF_TABS: Option[] = [{ value: 'cycles', label: 'Review Cycles' }, { value: 'reviews', label: 'Reviews' }, { value: 'templates', label: 'Templates' }];

export function PerformancePage() {
  const [params, setParams] = useSearchParams();
  const tab = params.get('tab') ?? 'cycles';
  return (
    <div className="oc-stack">
      <Tabs tabs={PERF_TABS} value={tab} onChange={(v) => setParams({ tab: v })} />
      {tab === 'cycles' && <CycleList />}
      {tab === 'reviews' && <ReviewList />}
      {tab === 'templates' && <AutoResourcePage resourceKey="hris.review_template" />}
    </div>
  );
}

function CycleList() {
  const nav = useNavigate();
  const { can } = useAuth();
  const [open, setOpen] = useState(false);
  return (
    <>
      <ListPage title="Review Cycles" help="Annual, semester and probation reviews: self assessment and manager review in Employee Self Service, HR calibration, results to the employment history (FR-PRF-HR-01–04)."
        path={`${HR}/review-cycles`} statuses={opts(['draft', 'in_progress', 'calibration', 'completed', 'cancelled'])}
        actions={can('hris.review_cycle.manage') && <button className="oc-btn oc-btn-ink" onClick={() => setOpen(true)}><Icon name="add" size={18} /> New Cycle</button>}
        onRowClick={(r) => nav(`/hris/performance/cycles/${r.id}`)}
        columns={[
          { key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'cycleType', header: 'Type', render: (r) => label(r.cycleType) },
          { key: 'periodEnd', header: 'Period', render: (r) => `${date(r.periodStart)} – ${date(r.periodEnd)}` }, { key: 'reviews', header: 'Reviews' },
          { key: 'completionRate', header: 'Completion', render: (r) => `${Math.round(Number(r.completionRate) * 100)}%` }, { key: 'status', header: 'Status', render: pill('status') },
        ]} />
      {open && <CycleForm onClose={() => setOpen(false)} onDone={(r) => nav(`/hris/performance/cycles/${r.id}`)} />}
    </>
  );
}

function CycleForm({ onClose, onDone }: { onClose: () => void; onDone: (r: R) => void }) {
  const units = useUnits();
  const templates = useOptions(`${HR}/review-templates?limit=200&filter[status]=active`, (x) => `${String(x.name)} (${String(x.code)})`);
  const y = new Date().getFullYear();
  const [f, setF] = useState<Record<string, string>>({ cycleType: 'annual', periodStart: `${y}-01-01`, periodEnd: `${y}-12-31` });
  const set = (k: string) => (v: string) => setF({ ...f, [k]: v });
  return (
    <FormModal title="New Review Cycle" path={`${HR}/review-cycles`} body={() => clean(f)} onClose={onClose} onDone={onDone} idempotent wide>
      <TextField label="Code" value={f.code ?? ''} onChange={(v) => set('code')(v.toUpperCase())} required />
      <TextField label="Name" value={f.name ?? ''} onChange={set('name')} required />
      <SelectField label="Type" value={f.cycleType} onChange={set('cycleType')} options={opts(['annual', 'semester', 'probation'])} />
      <SelectField label="Org unit" value={f.orgUnitId ?? ''} onChange={set('orgUnitId')} options={units} placeholder="Whole property" />
      <TextField label="Period start" type="date" value={f.periodStart} onChange={set('periodStart')} required />
      <TextField label="Period end" type="date" value={f.periodEnd} onChange={set('periodEnd')} required />
      <TextField label="Self assessment due" type="date" value={f.selfDue ?? ''} onChange={set('selfDue')} />
      <TextField label="Manager review due" type="date" value={f.managerDue ?? ''} onChange={set('managerDue')} />
      <TextField label="Calibration due" type="date" value={f.calibrationDue ?? ''} onChange={set('calibrationDue')} />
      <SelectField label="Default template" value={f.defaultTemplateId ?? ''} onChange={set('defaultTemplateId')} options={templates} placeholder="By position" />
    </FormModal>
  );
}

export function CycleDetailPage() {
  const { id = '' } = useParams();
  const nav = useNavigate();
  const { can } = useAuth();
  const toast = useToast();
  const q = useGet<R>(`${HR}/review-cycles/${id}`);
  const reviews = useGet<Page<R>>(`${HR}/reviews?cycleId=${id}`);
  const cal = useGet<R>(q.data && ['calibration', 'in_progress', 'completed'].includes(String(q.data.status)) ? `${HR}/review-cycles/${id}/calibration` : null);
  const [calibrate, setCalibrate] = useState<R | null>(null);
  const [close, setClose] = useState(false);
  if (q.isLoading) return <Skeleton rows={8} />;
  if (q.error || !q.data) return <ErrorAlert error={q.error} />;
  const c = q.data;
  const st = String(c.status);
  const base = `${HR}/review-cycles/${id}`;
  const manage = can('hris.review_cycle.manage');
  return (
    <div className="oc-stack">
      <PageHeader title={`${String(c.name)} · ${String(c.code)}`} help={`${label(c.cycleType)} · ${date(c.periodStart)} – ${date(c.periodEnd)}${c.orgUnitName ? ` · ${String(c.orgUnitName)}` : ''}`} actions={
        <div className="oc-row-wrap">
          {['draft', 'in_progress'].includes(st) && manage && <ActionButton label={st === 'draft' ? 'Launch' : 'Add Late Joiners'} kind="ink" path={`${base}:launch`} invalidate={INV}
            onDone={(r) => { const x = r as R; toast(`${String(x.created)} review(s) created${list(x.skipped).length ? `; skipped: ${list(x.skipped).join('; ')}` : ''}`); }} />}
          {st === 'in_progress' && manage && <ActionButton label="Start Calibration" path={`${base}:start-calibration`} invalidate={INV} />}
          {['in_progress', 'calibration'].includes(st) && manage && <button className="oc-btn oc-btn-ink oc-btn-sm" onClick={() => setClose(true)}>Close Cycle</button>}
          {!['completed', 'cancelled'].includes(st) && manage && <ActionButton label="Cancel" danger path={`${base}:cancel`} invalidate={INV} reason="required" />}
        </div>
      } />
      <Card title="Progress" icon="donut_large">
        <KV items={[['Status', <StatusPill key="s" status={st} label={label(st)} />], ['Reviews', val(c.reviews)], ['Self assessment', val(c.selfPending)],
          ['With the manager', val(c.managerPending)], ['Submitted', val(c.submitted)], ['Calibrated', val(c.calibrated)], ['Completed', val(c.completed)],
          ['Completion', `${Math.round(Number(c.completionRate) * 100)}%`], ['Self due', date(c.selfDue)], ['Manager due', date(c.managerDue)],
          ['Calibration due', date(c.calibrationDue)], ['Template', val(c.defaultTemplateName)]]} />
      </Card>
      {cal.data && (
        <Card title="Calibration guide" icon="equalizer">
          <div className="oc-stack" style={{ gap: 6 }}>
            {list(cal.data.distribution).map((b) => (
              <div key={String(b.code)} className="oc-row" style={{ gap: 12 }}>
                <span style={{ width: 200 }}>{String(b.label)}</span>
                <div aria-hidden="true" style={{ flex: 1, height: 12, background: 'var(--md-sys-color-surface-container-high)', borderRadius: 6 }}>
                  <div style={{ width: `${Math.min(100, Number(b.sharePercent))}%`, height: 12, borderRadius: 6, background: b.over ? 'var(--md-sys-color-error)' : 'var(--md-sys-color-primary)' }} />
                </div>
                <span style={{ width: 160 }}>{String(b.count)} · {String(b.sharePercent)}%{Number(b.maxSharePercent) ? ` (max ${String(b.maxSharePercent)}%)` : ''}</span>
              </div>
            ))}
          </div>
        </Card>
      )}
      <Card title="Reviews" icon="reviews">
        <DataTable rows={reviews.data?.items} loading={reviews.isLoading} error={reviews.error} onRowClick={(r) => nav(`/hris/performance/reviews/${r.id}`)} columns={[
          { key: 'employeeName', header: 'Employee' }, { key: 'orgUnitName', header: 'Unit', render: (r) => val(r.orgUnitName) }, { key: 'reviewerName', header: 'Reviewer', render: (r) => val(r.reviewerName) },
          { key: 'selfScore', header: 'Self', render: (r) => val(r.selfScore) }, { key: 'managerScore', header: 'Manager', render: (r) => val(r.managerScore) },
          { key: 'finalRating', header: 'Rating', render: (r) => label(r.finalRating ?? r.recommendedRating ?? '') || '—' }, { key: 'status', header: 'Status', render: pill('status') },
        ]} actions={(r) => (['submitted', 'calibrated'].includes(String(r.status)) && can('hris.performance_review.calibrate') && st !== 'completed'
          ? <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={(e) => { e.stopPropagation(); setCalibrate(r); }}>Calibrate</button> : null)} />
      </Card>
      {calibrate && <CalibrateForm review={calibrate} bands={list(cal.data?.bands)} onClose={() => setCalibrate(null)} />}
      {close && <CloseCycle path={`${base}:close`} open={Number(c.selfPending) + Number(c.managerPending) + Number(c.submitted)} onClose={() => setClose(false)} />}
    </div>
  );
}

function CloseCycle({ path, open, onClose }: { path: string; open: number; onClose: () => void }) {
  const [reason, setReason] = useState('');
  return (
    <FormModal title="Close Review Cycle" path={path} body={() => clean({ force: open > 0, reason })} onClose={onClose} submit="Close">
      <p className="oc-span">Calibrated reviews become Completed: the result is written to the employment history and shown to the employee.</p>
      {open > 0 && <p className="oc-span oc-alert oc-alert-warning">{open} review(s) are not calibrated yet and will be cancelled.</p>}
      {open > 0 && <TextArea label="Reason" value={reason} onChange={setReason} span required />}
    </FormModal>
  );
}

function CalibrateForm({ review, bands, onClose }: { review: R; bands: R[]; onClose: () => void }) {
  const [f, setF] = useState<Record<string, string>>({});
  const set = (k: string) => (v: string) => setF({ ...f, [k]: v });
  return (
    <FormModal title={`Calibrate · ${String(review.employeeName)}`} path={`${HR}/reviews/${review.id}:calibrate`} body={() => clean(f)} onClose={onClose}>
      <p className="oc-span oc-muted">Manager score {val(review.managerScore)} · recommended {label(review.recommendedRating)}. Empty fields take the band of the score.</p>
      <TextField label="Final score" inputMode="decimal" value={f.finalScore ?? ''} onChange={set('finalScore')} placeholder={val(review.managerScore)} />
      <SelectField label="Final rating" value={f.finalRating ?? ''} onChange={set('finalRating')} options={bands.map((b) => ({ value: String(b.code), label: String(b.label) }))} placeholder="Band of the score" />
      <SelectField label="Recommendation" value={f.recommendation ?? ''} onChange={set('recommendation')} options={opts(RECOMMENDATIONS)} placeholder="Default" />
      <TextField label="Salary increase %" inputMode="decimal" value={f.increasePercent ?? ''} onChange={set('increasePercent')} placeholder="Band" />
      <TextField label="Bonus (months)" inputMode="decimal" value={f.bonusMonths ?? ''} onChange={set('bonusMonths')} placeholder="Band" />
      <TextArea label="Calibration note" value={f.note ?? ''} onChange={set('note')} span />
    </FormModal>
  );
}

function ReviewList() {
  const nav = useNavigate();
  return (
    <ListPage title="Performance Reviews" help="Every review of every cycle." path={`${HR}/reviews`} statuses={opts(['self_assessment', 'manager_review', 'submitted', 'calibrated', 'completed'])}
      onRowClick={(r) => nav(`/hris/performance/reviews/${r.id}`)}
      columns={[
        { key: 'cycleName', header: 'Cycle' }, { key: 'employeeName', header: 'Employee' }, { key: 'orgUnitName', header: 'Unit', render: (r) => val(r.orgUnitName) },
        { key: 'reviewerName', header: 'Reviewer', render: (r) => val(r.reviewerName) }, { key: 'finalScore', header: 'Final', render: (r) => val(r.finalScore ?? r.managerScore) },
        { key: 'finalRating', header: 'Rating', render: (r) => label(r.finalRating ?? r.recommendedRating ?? '') || '—' }, { key: 'status', header: 'Status', render: pill('status') },
      ]} />
  );
}

/** Scores table with editable self or manager column. */
function ScoresEditor({ detail, mode, values, onChange }: { detail: R; mode: 'self' | 'manager' | 'none'; values: Record<string, R>; onChange: (k: string, v: R) => void }) {
  const scale = Number(detail.scoreScale ?? 5);
  const scaleOpts = Array.from({ length: scale }, (_, i) => ({ value: String(i + 1), label: String(i + 1) }));
  const k = (r: R) => `${String(r.itemKind)}:${String(r.code)}`;
  return (
    <DataTable rows={list(detail.scores)} columns={[
      { key: 'label', header: 'Competency / KPI', render: (r) => <span><strong>{String(r.label)}</strong>{r.itemKind === 'kpi' ? ' (KPI)' : ''} ×{String(r.weight)}{r.target ? <><br /><span className="oc-muted">Target {String(r.target)}</span></> : null}</span> },
      { key: 'selfScore', header: 'Self', render: (r) => (mode === 'self'
        ? <SelectField label="Self score" value={String(values[k(r)]?.score ?? r.selfScore ?? '')} onChange={(v) => onChange(k(r), { ...values[k(r)], score: v })} options={scaleOpts} placeholder="—" />
        : <span>{val(r.selfScore)}{r.selfComment ? <><br /><span className="oc-muted">{String(r.selfComment)}</span></> : null}</span>) },
      { key: 'managerScore', header: 'Manager', render: (r) => (mode === 'manager'
        ? <SelectField label="Manager score" value={String(values[k(r)]?.score ?? r.managerScore ?? '')} onChange={(v) => onChange(k(r), { ...values[k(r)], score: v })} options={scaleOpts} placeholder="—" />
        : <span>{val(r.managerScore)}{r.managerComment ? <><br /><span className="oc-muted">{String(r.managerComment)}</span></> : null}</span>) },
      { key: 'actual', header: 'Actual', render: (r) => (r.itemKind === 'kpi' && mode !== 'none'
        ? <TextField label="Actual" value={String(values[k(r)]?.actual ?? r.actual ?? '')} onChange={(v) => onChange(k(r), { ...values[k(r)], actual: v })} />
        : val(r.actual)) },
    ]} rowKey={(r) => k(r)} />
  );
}

function scorePayload(values: Record<string, R>) {
  return Object.entries(values).map(([key, v]) => {
    const [itemKind, code] = key.split(':');
    return clean({ itemKind, code, score: v.score, comment: v.comment, actual: v.actual });
  });
}

/** The review screen of HR, the manager (team) and the employee (self). */
function ReviewScreen({ path, mode, back, hr }: { path: string; mode: 'hr' | 'team' | 'self'; back?: React.ReactNode; hr?: boolean }) {
  const q = useGet<R>(path);
  const { can } = useAuth();
  const toast = useToast();
  const [values, setValues] = useState<Record<string, R>>({});
  const [text, setText] = useState<Record<string, string>>({});
  const [promote, setPromote] = useState(false);
  const save = useSend<Record<string, unknown>, R>('PATCH', path, INV);
  if (q.isLoading) return <Skeleton rows={8} />;
  if (q.error || !q.data) return <ErrorAlert error={q.error} />;
  const d = q.data;
  const r = d.review as R;
  const editSelf = mode === 'self' && d.canEditSelf === true;
  const editManager = mode !== 'self' && d.canEditManager === true;
  const editMode = editSelf ? 'self' : editManager ? 'manager' : 'none';
  const onChange = (k: string, v: R) => setValues({ ...values, [k]: v });
  const submitPath = mode === 'self' ? `${path}:submit` : mode === 'team' ? `${path}:submit` : `${path}:submit`;
  const body = () => (editSelf ? clean({ scores: scorePayload(values), comment: text.comment })
    : clean({ scores: scorePayload(values), comment: text.comment, strengths: text.strengths, improvements: text.improvements, goals: text.goals, recommendation: text.recommendation }));
  const band = list(d.bands).find((b) => b.code === (r.finalRating ?? r.recommendedRating));
  return (
    <div className="oc-stack">
      {back}
      <PageHeader title={`${String(r.employeeName)} · ${String(r.cycleName)}`} help={`${label(r.cycleType)} · ${date(r.periodStart)} – ${date(r.periodEnd)} · reviewer ${val(r.reviewerName)}`} actions={
        <div className="oc-row-wrap">
          {hr && ['submitted', 'calibrated'].includes(String(r.status)) && can('hris.performance_review.manage') && <ActionButton label="Send Back" path={`${path}:reopen`} invalidate={INV} reason="required" />}
          {hr && ['calibrated', 'completed'].includes(String(r.status)) && !r.employmentChangeId && can('hris.performance_review.promote') && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setPromote(true)}>Promote</button>}
          {mode === 'self' && r.status === 'completed' && !r.acknowledgedAt && <ActionButton label="Acknowledge" kind="ink" path={`${path}:acknowledge`} invalidate={INV} />}
        </div>
      } />
      <Card title="Result" icon="star_rate">
        <KV items={[['Status', <StatusPill key="s" status={String(r.status)} label={label(r.status)} />], ['Self score', val(r.selfScore)], ['Manager score', val(r.managerScore)],
          ['Final score', val(r.finalScore)], ['Rating', band ? String(ID() ? band.labelId : band.label) : label(r.finalRating ?? r.recommendedRating ?? '') || '—'],
          ['Recommendation', label(r.recommendation)], ...(r.increasePercent ? [['Salary increase', `${String(r.increasePercent)}%`] as [string, React.ReactNode]] : []),
          ...(r.bonusMonths ? [['Bonus', `${String(r.bonusMonths)} month(s)`] as [string, React.ReactNode]] : []), ['Calibration note', val(r.calibrationNote)],
          ['Acknowledged', r.acknowledgedAt ? formatDateTime(String(r.acknowledgedAt)) : '—']]} />
      </Card>
      {list(d.inputs).length > 0 && (
        <Card title="Operational data of the period" icon="insights">
          <KV items={list(d.inputs).map((i) => [String(i.label), `${String(i.value)}${i.unit === 'percent' ? '%' : ''}${i.note ? ` · ${String(i.note)}` : ''}`] as [string, React.ReactNode])} />
        </Card>
      )}
      <Card title="Competencies & KPIs" icon="fact_check">
        <ErrorAlert error={save.error} />
        <ScoresEditor detail={d} mode={editMode} values={values} onChange={onChange} />
        {editMode !== 'none' && (
          <div className="oc-form" style={{ marginTop: 12 }}>
            <TextArea label={editSelf ? 'My comment' : 'Overall comment'} value={text.comment ?? String((editSelf ? r.selfComment : r.managerComment) ?? '')} onChange={(v) => setText({ ...text, comment: v })} span />
            {editManager && <>
              <TextArea label="Strengths" value={text.strengths ?? String(r.strengths ?? '')} onChange={(v) => setText({ ...text, strengths: v })} span />
              <TextArea label="To improve" value={text.improvements ?? String(r.improvements ?? '')} onChange={(v) => setText({ ...text, improvements: v })} span />
              <TextArea label="Goals for next period" value={text.goals ?? String(r.goals ?? '')} onChange={(v) => setText({ ...text, goals: v })} span />
              <SelectField label="Recommendation" value={text.recommendation ?? String(r.recommendation ?? 'none')} onChange={(v) => setText({ ...text, recommendation: v })} options={opts(RECOMMENDATIONS)} />
            </>}
            <div className="oc-row-wrap oc-span">
              <button className="oc-btn oc-btn-neutral" style={{ minHeight: 44 }} disabled={save.isPending} onClick={() => save.mutate(body(), { onSuccess: () => { toast('Saved'); setValues({}); } })}>Save</button>
              <ActionButton label="Submit" kind="ink" path={submitPath} invalidate={INV} confirm={editSelf ? 'Send your self assessment to your manager? You cannot change it afterwards.' : 'Submit the review for HR calibration?'} />
            </div>
          </div>
        )}
        {editMode === 'none' && (r.strengths || r.improvements || r.goals) ? <KV items={[['Strengths', val(r.strengths)], ['To improve', val(r.improvements)], ['Goals', val(r.goals)]]} /> : null}
      </Card>
      {list(d.previous).length > 0 && (
        <Card title="Earlier reviews" icon="history">
          <DataTable rows={list(d.previous)} columns={[{ key: 'cycleName', header: 'Cycle' }, { key: 'finalScore', header: 'Score', render: (x) => val(x.finalScore) },
            { key: 'finalRating', header: 'Rating', render: (x) => label(x.finalRating) }]} />
        </Card>
      )}
      {promote && <PromoteForm path={`${path}:promote`} onClose={() => setPromote(false)} />}
    </div>
  );
}

function PromoteForm({ path, onClose }: { path: string; onClose: () => void }) {
  const positions = usePositions();
  const grades = useGrades();
  const [f, setF] = useState<Record<string, string>>({ effectiveDate: today() });
  const set = (k: string) => (v: string) => setF({ ...f, [k]: v });
  return (
    <FormModal title="Promote" path={path} body={() => clean(f)} onClose={onClose} submit="Promote">
      <p className="oc-span oc-muted">Recorded through Core HR (employment change with effective date).</p>
      <SelectField label="New position" value={f.positionId ?? ''} onChange={set('positionId')} options={positions} placeholder="Unchanged" />
      <SelectField label="New grade" value={f.gradeId ?? ''} onChange={set('gradeId')} options={grades} placeholder="The position's" />
      <TextField label="Effective date" type="date" value={f.effectiveDate} onChange={set('effectiveDate')} required />
      <TextArea label="Reason" value={f.reason ?? ''} onChange={set('reason')} span />
    </FormModal>
  );
}

export function ReviewDetailPage() {
  const { id = '' } = useParams();
  return <ReviewScreen path={`${HR}/reviews/${id}`} mode="hr" hr back={<Link className="oc-btn oc-btn-text" to="/hris/performance?tab=reviews"><Icon name="arrow_back" size={20} /> Reviews</Link>} />;
}

// ── Employee Self Service: My Reviews, Team Reviews ──────────────────────

function EssBackLink({ base, title }: { base: string; title: string }) {
  return (
    <div className="oc-row" style={{ gap: 8 }}>
      <Link className="oc-btn oc-btn-text" to={base} aria-label="Back"><Icon name="arrow_back" size={22} /></Link>
      <h1 style={{ margin: 0, fontSize: 24 }}>{title}</h1>
    </div>
  );
}

function EssReviewList({ base, team }: { base: string; team?: boolean }) {
  const [params, setParams] = useSearchParams();
  const sel = params.get('id');
  const path = team ? `${ESS}/team-reviews` : `${ESS}/reviews`;
  const l = useGet<Page<R>>(sel ? null : path);
  const title = team ? (ID() ? 'Penilaian Tim' : 'Team Reviews') : (ID() ? 'Penilaian Saya' : 'My Reviews');
  if (sel) {
    return <ReviewScreen path={`${path}/${sel}`} mode={team ? 'team' : 'self'}
      back={<button className="oc-btn oc-btn-text" style={{ minHeight: 44, width: 'fit-content' }} onClick={() => setParams({})}><Icon name="arrow_back" size={20} /> {title}</button>} />;
  }
  return (
    <div className="oc-stack">
      <EssBackLink base={base} title={title} />
      <DataTable rows={l.data?.items} loading={l.isLoading} error={l.error} onRowClick={(r) => setParams({ id: r.id })}
        empty={<Empty title="No reviews" help={team ? 'No review of your team is open.' : 'You have no performance review yet.'} icon="star_rate" />}
        columns={[
          ...(team ? [{ key: 'employeeName', header: 'Employee' }] : []),
          { key: 'cycleName', header: 'Cycle' }, { key: 'status', header: 'Status', render: pill('status') },
          { key: team ? 'managerDue' : 'selfDue', header: 'Due', render: (r: R) => date(team ? r.managerDue : r.selfDue) },
          { key: 'finalRating', header: 'Rating', render: (r: R) => label(r.finalRating ?? '') || '—' },
        ]} />
    </div>
  );
}

export const TALENT_ESS: [string, (props: { base: string }) => React.ReactNode][] = [
  ['reviews', ({ base }) => <EssReviewList base={base} />],
  ['team-reviews', ({ base }) => <EssReviewList base={base} team />],
];

export const TALENT_ROUTES: AreaRoute[] = [
  { path: 'hris/recruitment', perm: 'hris.job_requisition.view', element: <RecruitmentPage /> },
  { path: 'hris/recruitment/requisitions/:id', perm: 'hris.job_requisition.view', element: <RequisitionDetailPage /> },
  { path: 'hris/recruitment/applications/:id', perm: 'hris.application.view', element: <ApplicationDetailPage /> },
  { path: 'hris/performance', perm: 'hris.review_cycle.view', element: <PerformancePage /> },
  { path: 'hris/performance/cycles/:id', perm: 'hris.review_cycle.view', element: <CycleDetailPage /> },
  { path: 'hris/performance/reviews/:id', perm: 'hris.performance_review.view', element: <ReviewDetailPage /> },
];
