import React, { useMemo, useState } from 'react';
import { useNavigate, useParams } from 'react-router';
import { download, qs, uuidv7, useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDateTime, formatMoney, formatRelative, useTranslation } from '@oneclub/i18n';
import { useAuth, useBootstrap } from '../context';
import {
  Card, ConfirmDialog, DataTable, Drawer, ErrorAlert, FilterPills, Icon, Labeled, Modal, PageHeader, SearchBox, SelectField, Skeleton, StatusPill,
  TextArea, TextField, fieldErrors, useDebounced,
} from '../components/ui';
import { useToast } from '../components/toast';

type ApprovalRequest = Schemas['Request'];

// ── Approvals inbox (FR-APR-06) ───────────────────────────────────────────

export function ApprovalsPage() {
  const { can } = useAuth();
  const { t } = useTranslation();
  const nav = useNavigate();
  const toast = useToast();
  const [box, setBox] = useState('inbox');
  const [q, setQ] = useState('');
  const [testOpen, setTestOpen] = useState(false);
  const [title, setTitle] = useState('Buy new golf carts');
  const [amount, setAmount] = useState('15000000');
  const boot = useBootstrap();
  const query = useDebounced(q);
  const list = useGet<Page<ApprovalRequest>>(`/api/v1/platform/approvals${qs({ box, q: query, limit: 100 })}`);
  const test = useSend<Record<string, unknown>, ApprovalRequest>('POST', '/api/v1/platform/approvals:test', ['/api/v1/platform/approvals'],
    () => ({ 'Idempotency-Key': uuidv7() }));
  const boxes = [{ value: 'inbox', label: 'Waiting for me' }, { value: 'mine', label: 'My requests' }];
  if (can('platform.approval.view_all')) boxes.push({ value: 'all', label: 'All' });
  return (
    <div className="oc-stack">
      <PageHeader title="Approvals" help={t('help.approvals')} actions={can('platform.approval.request_test') &&
        <button className="oc-btn oc-btn-neutral" onClick={() => setTestOpen(true)}><Icon name="science" size={18} /> Create Test Approval</button>} />
      <div className="oc-card">
        <div className="oc-toolbar"><SearchBox value={q} onChange={setQ} /><span className="oc-spacer" /><FilterPills options={boxes} value={box} onChange={setBox} /></div>
        <DataTable rows={list.data?.items as unknown as Record<string, unknown>[]} loading={list.isLoading} error={list.error}
          onRowClick={(r) => nav(`/approvals/${r.id}`)}
          empty={<div className="oc-empty"><Icon name="task_alt" size={40} /><div style={{ fontWeight: 600 }}>{box === 'inbox' ? 'Nothing waiting for you' : 'No requests'}</div></div>}
          columns={[
            { key: 'title', header: 'Document', render: (r) => <div><div style={{ fontWeight: 600 }}>{String(r.title)}</div><div className="oc-small oc-muted">{String(r.documentName)} · {String(r.documentRef)}</div></div> },
            { key: 'requesterName', header: 'Requested by' },
            { key: 'amount', header: 'Amount', align: 'right', render: (r) => {
              const a = (r.attributes as Record<string, unknown>)?.amount;
              return a ? <span className="oc-num">{formatMoney(String(a), boot.currency)}</span> : '—';
            } },
            { key: 'createdAt', header: 'Submitted', render: (r) => formatRelative(String(r.createdAt)) },
            { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> },
          ]} />
      </div>
      <Modal open={testOpen} onClose={() => setTestOpen(false)} title="Create Test Approval" actions={<>
        <button className="oc-btn oc-btn-neutral" onClick={() => setTestOpen(false)}>Cancel</button>
        <button className="oc-btn oc-btn-ink" disabled={test.isPending} onClick={() => test.mutate({ title, amount }, {
          onSuccess: (r) => { setTestOpen(false); toast('Submitted'); nav(`/approvals/${r.id}`); },
        })}>Submit</button></>}>
        <p className="oc-muted">Reference document type used to verify approval workflows end-to-end. Conditions can use the amount.</p>
        <div className="oc-form">
          <TextField label="Title" value={title} onChange={setTitle} />
          <TextField label="Amount (IDR)" inputMode="decimal" value={amount} onChange={setAmount} error={fieldErrors(test.error).amount} />
        </div>
        <ErrorAlert error={test.error} />
      </Modal>
    </div>
  );
}

export function ApprovalDetailPage() {
  const { id } = useParams();
  const boot = useBootstrap();
  const toast = useToast();
  const req = useGet<ApprovalRequest>(`/api/v1/platform/approvals/${id}`);
  const [action, setAction] = useState<ApprovalAction | null>(null);
  const act = useSend<{ reason?: string }>('POST', () => `/api/v1/platform/approvals/${id}:${action}`, ['/api/v1/platform/approvals']);
  if (req.error) return <ErrorAlert error={req.error} />;
  if (!req.data) return <Skeleton />;
  const r = req.data;
  // a draft with a decision reason was returned by an approver for revision
  const revision = r.status === 'draft' && !!r.decisionReason;
  const titles: Record<ApprovalAction, string> = {
    approve: 'Approve', reject: 'Reject', 'request-revision': 'Request revision', cancel: 'Cancel request', submit: revision ? 'Resubmit' : 'Submit',
  };
  return (
    <div className="oc-stack">
      <PageHeader title={r.title} help={`${r.documentName} · ${r.documentRef}`} actions={<>
        <StatusPill status={revision ? 'pending' : r.status} label={revision ? 'Revision requested' : undefined} />
        {r.status === 'draft' && r.canCancel && <button className="oc-btn oc-btn-ink" onClick={() => setAction('submit')}>{titles.submit}</button>}
        {r.canDecide && <><button className="oc-btn oc-btn-danger" onClick={() => setAction('reject')}>Reject</button>
          <button className="oc-btn oc-btn-outline" onClick={() => setAction('request-revision')}>Request revision</button>
          <button className="oc-btn oc-btn-primary" onClick={() => setAction('approve')}>Approve</button></>}
        {r.canCancel && (r.status === 'pending' || revision) && <button className="oc-btn oc-btn-outline" onClick={() => setAction('cancel')}>Cancel</button>}
      </>} />
      <div className="oc-grid-2">
        <Card title="Document" icon="description">
          <div className="oc-form">
            <Labeled label="Requested by">{r.requesterName}</Labeled>
            <Labeled label="Submitted">{formatDateTime(r.createdAt)}</Labeled>
            {Object.entries(r.attributes ?? {}).filter(([k]) => k !== 'propertyId').map(([k, v]) => (
              <Labeled key={k} label={k}>{k === 'amount' ? formatMoney(String(v), boot.currency) : String(v)}</Labeled>
            ))}
            {r.decisionReason && <Labeled label={revision ? 'Revision requested' : 'Reason'}>{r.decisionReason}</Labeled>}
          </div>
        </Card>
        <Card title="Steps" icon="account_tree">
          {revision && !(r.steps ?? []).length && <p className="oc-muted oc-small" style={{ margin: 0 }}>The steps are rebuilt from the workflow when the request is resubmitted.</p>}
          <ol style={{ margin: 0, paddingLeft: 18 }} className="oc-stack">
            {(r.steps ?? []).map((s) => (
              <li key={s.stepNo}>
                <div className="oc-row-wrap"><strong>{s.name}</strong><StatusPill status={s.status} /></div>
                <div className="oc-small oc-muted">
                  Approver: {s.approverRoleName ?? s.approverUserName}
                  {s.decidedByName && <> · decided by {s.decidedByName}{s.onBehalfOfName && <> on behalf of {s.onBehalfOfName}</>} {s.decidedAt && formatRelative(s.decidedAt)}</>}
                  {s.dueAt && s.status === 'pending' && <> · due {formatDateTime(s.dueAt)}</>}
                </div>
                {s.reason && <div className="oc-small">“{s.reason}”</div>}
              </li>
            ))}
          </ol>
        </Card>
      </div>
      {(r.history ?? []).length > 0 && (
        <Card title="Approval history" icon="history">
          <DataTable rows={(r.history ?? []).map((h, i) => ({ ...h, id: String(i) })) as unknown as Record<string, unknown>[]} columns={[
            { key: 'at', header: 'When', render: (h) => formatDateTime(String(h.at)) },
            { key: 'action', header: 'Action', render: (h) => HISTORY_LABELS[String(h.action)] ?? String(h.action) },
            { key: 'actorName', header: 'By', render: (h) => String(h.actorName ?? '—') },
            { key: 'stepNo', header: 'Step', render: (h) => (h.stepNo ? String(h.stepNo) : '—') },
            { key: 'reason', header: 'Reason / note', render: (h) => String(h.reason ?? '—') },
          ]} />
        </Card>
      )}
      <ConfirmDialog open={!!action} onClose={() => { setAction(null); act.reset(); }} busy={act.isPending} error={act.error}
        title={action ? titles[action] : ''} confirmLabel={action ? titles[action] : ''} danger={action === 'reject' || action === 'cancel'}
        reason={action === 'reject' || action === 'request-revision' ? 'required' : action === 'submit' ? undefined : 'optional'}
        onConfirm={(reason) => act.mutate(reason ? { reason } : {}, { onSuccess: () => { setAction(null); toast('Saved'); void req.refetch(); } })} />
    </div>
  );
}

type ApprovalAction = 'approve' | 'reject' | 'request-revision' | 'cancel' | 'submit';

const HISTORY_LABELS: Record<string, string> = {
  create: 'Submitted', approval_submitted: 'Resubmitted', approval_approved: 'Approved', approval_rejected: 'Rejected',
  approval_revision_requested: 'Revision requested', approval_cancelled: 'Cancelled', approver_reassigned: 'Approver reassigned',
};

// ── Approval Workflows (FR-APR-01/02) ─────────────────────────────────────

type Workflow = Schemas['Workflow'];
type WfStep = Schemas['WorkflowStep'];

const OPERATORS: Record<string, string> = { eq: '=', neq: '≠', gt: '>', gte: '≥', lt: '<', lte: '≤' };
type WfCondition = WfStep['conditions'][number];
type DocAttr = Schemas['DocumentTypeView']['attributes'][number];

const isAmount = (attr: string) => attr.toLowerCase().includes('amount');

/** One condition as text, e.g. "Amount > Rp 5.000.000". */
function conditionText(c: WfCondition, attrs: DocAttr[]): string {
  const a = attrs.find((x) => x.key === c.attribute);
  const v = a?.type === 'number' && isAmount(c.attribute) && typeof c.value === 'number' ? formatMoney(c.value) : String(c.value ?? '');
  return `${a?.label ?? c.attribute} ${OPERATORS[c.operator] ?? c.operator} ${v}`;
}

const newStep = (n: number): WfStep => ({ stepNo: n, name: n === 1 ? 'Approval' : `Step ${n}`, approverType: 'role', approverRoleId: null, approverUserId: null,
  conditions: [], slaHours: 24 });

/**
 * Approval workflow editor (FR-APR-01/02, PO decision 4d): steps with an
 * approver role, SLA and conditions — e.g. an amount threshold per document
 * type — added, edited, reordered and removed; the workflow is saved as a
 * whole (audited). A workflow for all properties needs an instance-wide
 * administrator; a Property Admin saves one for the property.
 */
function WorkflowEditor({ wf, copy, onDone }: { wf?: Workflow; copy?: Workflow; onDone: () => void }) {
  const toast = useToast();
  const { can, me, propertyId } = useAuth();
  const types = useGet<Page<Schemas['DocumentTypeView']>>('/api/v1/platform/approval-document-types');
  const roles = useGet<Page<Schemas['Role']>>('/api/v1/platform/roles?limit=200');
  const src = wf ?? copy;
  const [docType, setDocType] = useState(src?.documentType ?? 'test_approval');
  const [name, setName] = useState(copy ? `${copy.name} (copy)` : (wf?.name ?? ''));
  const [prop, setProp] = useState<string>(copy ? propertyId : (wf?.propertyId ?? ''));
  const [priority, setPriority] = useState(String(src?.priority ?? 100));
  const [steps, setSteps] = useState<WfStep[]>(src?.steps?.length ? src.steps.map((x) => ({ ...x, conditions: [...(x.conditions ?? [])] })) : [newStep(1)]);
  const save = useSend<Record<string, unknown>>(wf ? 'PATCH' : 'POST', wf ? `/api/v1/platform/approval-workflows/${wf.id}` : '/api/v1/platform/approval-workflows',
    ['/api/v1/platform/approval-workflows']);
  const editable = can('platform.approval_workflow.manage');
  const attrs = types.data?.items.find((t) => t.code === docType)?.attributes ?? [];
  const attrType = (key: string) => attrs.find((a) => a.key === key)?.type ?? (key === 'propertyId' ? 'uuid' : 'string');
  const renumber = (list: WfStep[]) => list.map((x, j) => ({ ...x, stepNo: j + 1 }));
  const upd = (i: number, patch: Partial<WfStep>) => setSteps((s) => s.map((x, j) => (j === i ? { ...x, ...patch } : x)));
  const move = (i: number, d: -1 | 1) => setSteps((s) => {
    const n = [...s];
    [n[i], n[i + d]] = [n[i + d], n[i]];
    return renumber(n);
  });
  const setCond = (i: number, k: number, patch: Partial<WfCondition>) =>
    upd(i, { conditions: steps[i].conditions.map((x, m) => (m === k ? { ...x, ...patch } : x)) });
  const fe = fieldErrors(save.error);
  const stepErr = (i: number, f: string) => fe[`steps[${i}].${f}`];
  return (
    <div className="oc-stack">
      <div className="oc-form">
        <SelectField label="Document Type" value={docType} onChange={(v) => { if (!wf) setDocType(v); }}
          options={(types.data?.items ?? []).map((t) => ({ value: t.code, label: t.name }))} help={wf ? 'The document type of a saved workflow cannot change' : undefined}
          error={fe.documentType} />
        <TextField label="Name" value={name} onChange={setName} required error={fe.name} disabled={!editable} />
        <SelectField label="Property" value={prop} onChange={setProp} placeholder="All properties" error={fe.propertyId}
          help="All properties needs an instance-wide administrator; a property workflow takes precedence at its property"
          options={(me?.properties ?? []).map((p) => ({ value: p.id, label: p.name }))} />
        <TextField label="Priority" type="number" min={0} value={priority} onChange={setPriority} help="Lower wins when several workflows match"
          error={fe.priority} disabled={!editable} />
      </div>
      {fe.steps && <p className="oc-alert oc-alert-error" role="alert">{fe.steps}</p>}
      {steps.map((s, i) => (
        <div key={i} className="oc-card" style={{ background: 'var(--md-sys-color-surface-container-low)' }}>
          <div className="oc-row"><strong>Step {s.stepNo}</strong>
            <span className="oc-small oc-muted">{(s.conditions ?? []).length ? `Only when ${s.conditions.map((c) => conditionText(c, attrs)).join(' and ')}` : 'Always'}</span>
            <span className="oc-spacer" />
            {editable && <>
              <button type="button" className="oc-icon-btn" aria-label={`Move step ${s.stepNo} up`} disabled={i === 0} onClick={() => move(i, -1)}><Icon name="arrow_upward" size={18} /></button>
              <button type="button" className="oc-icon-btn" aria-label={`Move step ${s.stepNo} down`} disabled={i === steps.length - 1} onClick={() => move(i, 1)}><Icon name="arrow_downward" size={18} /></button>
              <button type="button" className="oc-icon-btn" aria-label={`Remove step ${s.stepNo}`} onClick={() => setSteps(renumber(steps.filter((_, j) => j !== i)))}><Icon name="delete" size={18} /></button>
            </>}</div>
          <div className="oc-form">
            <TextField label="Step name" value={s.name} onChange={(v) => upd(i, { name: v })} error={stepErr(i, 'name')} disabled={!editable} />
            <SelectField label="Approver role" value={s.approverRoleId ?? ''} placeholder="Select…" onChange={(v) => upd(i, { approverType: 'role', approverRoleId: v })}
              options={(roles.data?.items ?? []).map((r) => ({ value: r.id, label: r.name }))} error={stepErr(i, 'approverRoleId') ?? stepErr(i, 'approverType')} />
            <TextField label="SLA (hours)" type="number" min={1} value={String(s.slaHours ?? '')} onChange={(v) => upd(i, { slaHours: v ? Number(v) : null })}
              error={stepErr(i, 'slaHours')} disabled={!editable} />
          </div>
          <div className="oc-label" style={{ marginTop: 12 }}>Conditions (all must match; none = always)</div>
          {(s.conditions ?? []).map((c, k) => {
            const num = attrType(c.attribute) === 'number';
            const err = (f: string) => stepErr(i, `conditions[${k}].${f}`);
            return (
              <div key={k} className="oc-row-wrap">
                <div style={{ width: 200 }}><SelectField label="Attribute" value={c.attribute} onChange={(v) => setCond(i, k, { attribute: v })} error={err('attribute')}
                  options={[...attrs.map((a) => ({ value: a.key, label: a.label })), { value: 'propertyId', label: 'Property' }]} /></div>
                <div style={{ width: 120 }}><SelectField label="Operator" value={c.operator} onChange={(v) => setCond(i, k, { operator: v as WfCondition['operator'] })}
                  error={err('operator')} options={Object.entries(OPERATORS).map(([value, label]) => ({ value, label }))} /></div>
                <div style={{ width: 200 }}><TextField label={num && isAmount(c.attribute) ? 'Value (IDR)' : 'Value'} type={num ? 'number' : 'text'}
                  min={num ? 0 : undefined} inputMode={num ? 'decimal' : undefined} value={String(c.value ?? '')} error={err('value')} disabled={!editable}
                  help={num && typeof c.value === 'number' && isAmount(c.attribute) ? formatMoney(c.value) : undefined}
                  onChange={(v) => setCond(i, k, { value: v === '' || isNaN(Number(v)) ? v : Number(v) })} /></div>
                {editable && <button type="button" className="oc-icon-btn" style={{ alignSelf: 'flex-end' }} aria-label="Remove condition"
                  onClick={() => upd(i, { conditions: s.conditions.filter((_, m) => m !== k) })}><Icon name="close" size={18} /></button>}
              </div>
            );
          })}
          {editable && <div className="oc-row-wrap">
            <button type="button" className="oc-btn oc-btn-text oc-btn-sm" onClick={() => upd(i, { conditions: [...(s.conditions ?? []), { attribute: attrs[0]?.key ?? 'amount', operator: 'gt', value: 0 }] })}>
              <Icon name="add" size={18} /> Add condition</button>
            {attrs.some((a) => a.key === 'amount') && !(s.conditions ?? []).some((c) => c.attribute === 'amount') &&
              <button type="button" className="oc-btn oc-btn-text oc-btn-sm" onClick={() => upd(i, { conditions: [...(s.conditions ?? []), { attribute: 'amount', operator: 'gt', value: 5000000 }] })}>
                <Icon name="payments" size={18} /> Add amount threshold</button>}
          </div>}
        </div>
      ))}
      {editable && <div><button type="button" className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setSteps([...steps, newStep(steps.length + 1)])}>
        <Icon name="add" size={18} /> Add step</button></div>}
      <ErrorAlert error={Object.keys(fe).length ? null : save.error} />
      <div className="oc-row"><span className="oc-spacer" /><button className="oc-btn oc-btn-neutral" onClick={onDone}>{editable ? 'Cancel' : 'Close'}</button>
        {editable && <button className="oc-btn oc-btn-ink" disabled={save.isPending || !name.trim()} onClick={() => {
          const body: Record<string, unknown> = { name, priority: Number(priority), steps, ...(prop ? { propertyId: prop } : {}) };
          if (!wf) body.documentType = docType;
          save.mutate(body, { onSuccess: () => { toast('Saved'); onDone(); } });
        }}>Save</button>}</div>
    </div>
  );
}

export function ApprovalWorkflowsPage() {
  const { can } = useAuth();
  const { t } = useTranslation();
  const [docType, setDocType] = useState('');
  const list = useGet<Page<Workflow>>(`/api/v1/platform/approval-workflows${qs({ 'filter[documentType]': docType || undefined })}`);
  const types = useGet<Page<Schemas['DocumentTypeView']>>('/api/v1/platform/approval-document-types');
  const [edit, setEdit] = useState<Workflow | 'new' | null>(null);
  const [copy, setCopy] = useState<Workflow | null>(null);
  const toggle = useSend<{ id: string; status: string }>('PATCH', (b) => `/api/v1/platform/approval-workflows/${b.id}`, ['/api/v1/platform/approval-workflows']);
  const typeOf = (code: string) => types.data?.items.find((x) => x.code === code);
  const manage = can('platform.approval_workflow.manage');
  const close = () => { setEdit(null); setCopy(null); };
  return (
    <div className="oc-stack">
      <PageHeader title="Approval Workflows" help={t('help.approvalWorkflows')} actions={manage &&
        <button className="oc-btn oc-btn-primary" onClick={() => setEdit('new')}><Icon name="add" size={18} /> Add Workflow</button>} />
      <div className="oc-card">
        <div style={{ maxWidth: 360 }}><SelectField label="Document Type" value={docType} onChange={setDocType} placeholder="All document types"
          options={(types.data?.items ?? []).map((x) => ({ value: x.code, label: x.name }))} /></div>
        <DataTable rows={list.data?.items as unknown as Record<string, unknown>[]} loading={list.isLoading} onRowClick={(r) => setEdit(r as unknown as Workflow)}
          columns={[
            { key: 'name', header: 'Workflow' },
            { key: 'documentType', header: 'Document Type', render: (w) => typeOf(String(w.documentType))?.name ?? String(w.documentType) },
            { key: 'propertyId', header: 'Property', render: (w) => (w.propertyId ? 'One property' : 'All properties') },
            { key: 'steps', header: 'Steps', render: (w) => (w.steps as WfStep[]).map((s) => s.name + ((s.conditions ?? []).length
              ? ` (${s.conditions.map((c) => conditionText(c, typeOf(String(w.documentType))?.attributes ?? [])).join(', ')})` : '')).join(' → ') },
            { key: 'priority', header: 'Priority', align: 'right' },
            { key: 'status', header: 'Status', render: (w) => <StatusPill status={String(w.status)} /> },
          ]}
          actions={(w) => manage && (<div className="oc-row-wrap">
            <button className="oc-btn oc-btn-outline oc-btn-sm" onClick={() => setCopy(w as unknown as Workflow)}>Copy</button>
            <button className="oc-btn oc-btn-outline oc-btn-sm" onClick={() => toggle.mutate({ id: String(w.id), status: w.status === 'active' ? 'inactive' : 'active' } as never)}>
              {w.status === 'active' ? 'Deactivate' : 'Activate'}</button></div>)} />
        <ErrorAlert error={toggle.error} />
      </div>
      <Drawer open={!!edit || !!copy} onClose={close} title={copy ? `Copy of ${copy.name}` : edit === 'new' ? 'Add Workflow' : (edit?.name ?? '')}>
        {copy ? <WorkflowEditor key={`copy-${copy.id}`} copy={copy} onDone={close} />
          : edit && <WorkflowEditor key={edit === 'new' ? 'new' : edit.id} wf={edit === 'new' ? undefined : edit} onDone={close} />}
      </Drawer>
    </div>
  );
}

// ── Audit Logs (FR-AUD-04/05/06) ──────────────────────────────────────────

type AuditLog = Schemas['Log'];

function Diff({ log }: { log: AuditLog }) {
  const keys = useMemo(() => [...new Set([...Object.keys(log.before ?? {}), ...Object.keys(log.after ?? {})])].sort(), [log]);
  if (!log.before && !log.after) return <div className="oc-muted oc-small">No data snapshot for this event.</div>;
  const show = (v: unknown) => (v === undefined ? '' : typeof v === 'object' ? JSON.stringify(v) : String(v));
  return (
    <div className="oc-table-wrap">
      <table className="oc-table">
        <thead><tr><th>Field</th><th>Before</th><th>After</th></tr></thead>
        <tbody>
          {keys.map((k) => {
            const changed = log.changed.includes(k) || !log.before || !log.after;
            return (
              <tr key={k} style={{ background: changed && log.before && log.after ? 'var(--md-sys-color-warning-container)' : undefined }}>
                <td><code className="oc-code">{k}</code></td>
                <td className="oc-small" style={{ wordBreak: 'break-word' }}>{show(log.before?.[k])}</td>
                <td className="oc-small" style={{ wordBreak: 'break-word' }}>{show(log.after?.[k])}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

export function AuditLogsPage() {
  const { can } = useAuth();
  const { t } = useTranslation();
  const [q, setQ] = useState('');
  const [module, setModule] = useState('');
  const [category, setCategory] = useState('');
  const [entityType, setEntityType] = useState('');
  const [actorId, setActorId] = useState('');
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [open, setOpen] = useState<AuditLog | null>(null);
  const [cursors, setCursors] = useState<string[]>([]);
  const query = useDebounced(q);
  const params = { q: query, 'filter[module]': module, 'filter[category]': category, 'filter[entityType]': entityType, 'filter[actorId]': actorId,
    from: from ? new Date(from).toISOString() : undefined, to: to ? new Date(to).toISOString() : undefined, limit: 50 };
  const list = useGet<Page<AuditLog>>(`/api/v1/audit/logs${qs({ ...params, cursor: cursors[cursors.length - 1] })}`);
  const users = useGet<Page<Schemas['User']>>(can('platform.user.view') ? '/api/v1/platform/users?limit=500' : null);
  const reset = () => setCursors([]);
  return (
    <div className="oc-stack">
      <PageHeader title="Audit Logs" help={t('help.auditLogs')} actions={can('audit.log.export') &&
        <button className="oc-btn oc-btn-outline" onClick={() => download('POST', '/api/v1/audit/exports', {
          filters: Object.fromEntries(Object.entries({ module, category, entityType, actorId }).filter(([, v]) => v)),
          from: params.from, to: params.to, q: query,
        }, 'audit-logs.csv')}><Icon name="download" size={18} /> Export CSV</button>} />
      <div className="oc-card">
        <div className="oc-toolbar">
          <SearchBox value={q} onChange={(v) => { setQ(v); reset(); }} />
          <div style={{ width: 160 }}><SelectField label="Module" value={module} onChange={(v) => { setModule(v); reset(); }} placeholder="All"
            options={['platform', 'audit', 'golf', 'billing', 'commercial', 'crm', 'membership', 'sportclub', 'reservation', 'procurement', 'reporting'].map((m) => ({ value: m, label: m }))} /></div>
          <div style={{ width: 150 }}><SelectField label="Category" value={category} onChange={(v) => { setCategory(v); reset(); }} placeholder="All"
            options={[{ value: 'data', label: 'Data' }, { value: 'security', label: 'Security' }, { value: 'system', label: 'System' }]} /></div>
          <div style={{ width: 200 }}><TextField label="Entity" value={entityType} onChange={(v) => { setEntityType(v); reset(); }} placeholder="platform.venue" /></div>
          {users.data && <div style={{ width: 200 }}><SelectField label="User" value={actorId} onChange={(v) => { setActorId(v); reset(); }} placeholder="All"
            options={users.data.items.map((u) => ({ value: u.id, label: u.fullName }))} /></div>}
          <div style={{ width: 200 }}><TextField label="From" type="datetime-local" value={from} onChange={(v) => { setFrom(v); reset(); }} /></div>
          <div style={{ width: 200 }}><TextField label="To" type="datetime-local" value={to} onChange={(v) => { setTo(v); reset(); }} /></div>
        </div>
        <DataTable rows={list.data?.items as unknown as Record<string, unknown>[]} loading={list.isLoading} error={list.error} onRowClick={(r) => setOpen(r as unknown as AuditLog)}
          columns={[
            { key: 'occurredAt', header: 'Time', render: (l) => <span className="oc-num oc-small">{formatDateTime(String(l.occurredAt))}</span> },
            { key: 'actorName', header: 'Actor', render: (l) => <div><div>{String(l.actorName ?? l.actorType)}</div><div className="oc-small oc-muted">{(l.actorRoles as string[]).join(', ')}</div></div> },
            { key: 'action', header: 'Action', render: (l) => <span className="oc-chip" style={{ cursor: 'default' }}>{String(l.action)}</span> },
            { key: 'entityType', header: 'Entity', render: (l) => <div><div>{String(l.entityLabel ?? '')}</div><div className="oc-small oc-muted">{String(l.entityType)}</div></div> },
            { key: 'category', header: 'Category', render: (l) => <StatusPill status={l.category === 'security' ? 'pending' : 'inactive'} label={String(l.category)} /> },
          ]} />
        <div className="oc-row" style={{ marginTop: 12 }}>
          <span className="oc-spacer" />
          {cursors.length > 0 && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setCursors(cursors.slice(0, -1))}>Previous</button>}
          {list.data?.nextCursor && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setCursors([...cursors, list.data!.nextCursor!])}>Next</button>}
        </div>
      </div>
      <Drawer open={!!open} onClose={() => setOpen(null)} title={open ? `${open.action} · ${open.entityType}` : ''}>
        {open && (
          <div className="oc-stack">
            {open.masked && <div className="oc-alert oc-alert-info">Personal data is masked (requires audit.log.view_sensitive).</div>}
            <div className="oc-form">
              <Labeled label="Time">{formatDateTime(open.occurredAt)}</Labeled>
              <Labeled label="Actor">{open.actorName ?? open.actorType}</Labeled>
              <Labeled label="Roles">{open.actorRoles.join(', ') || '—'}</Labeled>
              <Labeled label="Entity">{open.entityLabel ?? open.entityId}</Labeled>
              <Labeled label="IP">{open.ip}</Labeled>
              <Labeled label="Request">{open.requestId}</Labeled>
              {open.reason && <Labeled label="Reason">{open.reason}</Labeled>}
            </div>
            <Diff log={open} />
            {Object.keys(open.metadata ?? {}).length > 0 && <pre className="oc-pre">{JSON.stringify(open.metadata, null, 2)}</pre>}
          </div>
        )}
      </Drawer>
    </div>
  );
}

// ── Notifications (FR-NOT-02/04) ──────────────────────────────────────────

type Template = Schemas['Template'];

export function NotificationSettingsPage() {
  const { can } = useAuth();
  const { t } = useTranslation();
  const toast = useToast();
  const list = useGet<Page<Template>>('/api/v1/platform/notification-templates');
  const [edit, setEdit] = useState<Template | null>(null);
  const [subject, setSubject] = useState('');
  const [body, setBody] = useState('');
  const save = useSend('PATCH', () => `/api/v1/platform/notification-templates/${edit?.id}`, ['/api/v1/platform/notification-templates']);
  const test = useSend('POST', '/api/v1/platform/notifications:send-test');
  return (
    <div className="oc-stack">
      <PageHeader title="Notifications" help={t('help.notifications')} actions={can('platform.notification.send_test') &&
        <button className="oc-btn oc-btn-neutral" onClick={() => test.mutate({}, { onSuccess: () => toast('Test notification queued (in-app + e-mail)') })}>
          <Icon name="send" size={18} /> Send test to me</button>} />
      <div className="oc-card">
        <DataTable rows={list.data?.items as unknown as Record<string, unknown>[]} loading={list.isLoading}
          onRowClick={can('platform.notification_template.update') ? (r) => { const x = r as unknown as Template; setEdit(x); setSubject(x.subject); setBody(x.body); } : undefined}
          columns={[
            { key: 'eventCode', header: 'Event', render: (r) => <code className="oc-code">{String(r.eventCode)}</code> },
            { key: 'channel', header: 'Channel' }, { key: 'locale', header: 'Language', render: (r) => (r.locale === 'id' ? 'Bahasa Indonesia' : 'English') },
            { key: 'subject', header: 'Subject' },
            { key: 'isActive', header: 'Status', render: (r) => <StatusPill status={r.isActive ? 'active' : 'inactive'} /> },
          ]} />
      </div>
      <Modal open={!!edit} onClose={() => setEdit(null)} title={edit ? `${edit.eventCode} · ${edit.channel} · ${edit.locale}` : ''} wide actions={<>
        <button className="oc-btn oc-btn-neutral" onClick={() => setEdit(null)}>Cancel</button>
        <button className="oc-btn oc-btn-ink" onClick={() => save.mutate({ subject, body }, { onSuccess: () => { setEdit(null); toast(t('common.saved')); } })}>Save</button></>}>
        <div className="oc-stack">
          <TextField label="Subject" value={subject} onChange={setSubject} error={fieldErrors(save.error).subject} />
          <TextArea label="Body" value={body} onChange={setBody} rows={8} error={fieldErrors(save.error).body} help="Variables: {{.name}}, {{.link}}, {{.title}} … Go template syntax." />
          <ErrorAlert error={Object.keys(fieldErrors(save.error)).length ? null : save.error} />
        </div>
      </Modal>
    </div>
  );
}

export function NotificationHistoryPage() {
  const [status, setStatus] = useState('');
  const [q, setQ] = useState('');
  const query = useDebounced(q);
  const list = useGet<Page<Schemas['Delivery']>>(`/api/v1/platform/notification-deliveries${qs({ 'filter[status]': status, q: query, limit: 200 })}`);
  return (
    <div className="oc-stack">
      <PageHeader title="Notification History" help="Delivery status per channel: Pending, Sent, Failed (after automatic retries)." />
      <div className="oc-card">
        <div className="oc-toolbar"><SearchBox value={q} onChange={setQ} /><span className="oc-spacer" />
          <FilterPills value={status} onChange={setStatus} options={[{ value: '', label: 'All' }, { value: 'pending', label: 'Pending' }, { value: 'sent', label: 'Sent' }, { value: 'failed', label: 'Failed' }]} /></div>
        <DataTable rows={list.data?.items as unknown as Record<string, unknown>[]} loading={list.isLoading}
          columns={[
            { key: 'createdAt', header: 'Time', render: (d) => formatDateTime(String(d.createdAt)) },
            { key: 'eventCode', header: 'Event', render: (d) => <code className="oc-code">{String(d.eventCode)}</code> },
            { key: 'channel', header: 'Channel' }, { key: 'recipient', header: 'Recipient' }, { key: 'subject', header: 'Subject' },
            { key: 'attempts', header: 'Attempts', align: 'right' },
            { key: 'status', header: 'Status', render: (d) => <div><StatusPill status={String(d.status)} />{d.lastError ? <div className="oc-small oc-muted">{String(d.lastError)}</div> : null}</div> },
          ]} />
      </div>
    </div>
  );
}
