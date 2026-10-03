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
  const [action, setAction] = useState<'approve' | 'reject' | 'cancel' | 'submit' | null>(null);
  const act = useSend<{ reason?: string }>('POST', () => `/api/v1/platform/approvals/${id}:${action}`, ['/api/v1/platform/approvals']);
  if (req.error) return <ErrorAlert error={req.error} />;
  if (!req.data) return <Skeleton />;
  const r = req.data;
  return (
    <div className="oc-stack">
      <PageHeader title={r.title} help={`${r.documentName} · ${r.documentRef}`} actions={<>
        <StatusPill status={r.status} />
        {r.status === 'draft' && r.canCancel && <button className="oc-btn oc-btn-ink" onClick={() => setAction('submit')}>Submit</button>}
        {r.canDecide && <><button className="oc-btn oc-btn-danger" onClick={() => setAction('reject')}>Reject</button>
          <button className="oc-btn oc-btn-primary" onClick={() => setAction('approve')}>Approve</button></>}
        {r.canCancel && r.status === 'pending' && <button className="oc-btn oc-btn-outline" onClick={() => setAction('cancel')}>Cancel</button>}
      </>} />
      <div className="oc-grid-2">
        <Card title="Document" icon="description">
          <div className="oc-form">
            <Labeled label="Requested by">{r.requesterName}</Labeled>
            <Labeled label="Submitted">{formatDateTime(r.createdAt)}</Labeled>
            {Object.entries(r.attributes ?? {}).filter(([k]) => k !== 'propertyId').map(([k, v]) => (
              <Labeled key={k} label={k}>{k === 'amount' ? formatMoney(String(v), boot.currency) : String(v)}</Labeled>
            ))}
            {r.decisionReason && <Labeled label="Reason">{r.decisionReason}</Labeled>}
          </div>
        </Card>
        <Card title="Steps" icon="account_tree">
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
      <ConfirmDialog open={!!action} onClose={() => { setAction(null); act.reset(); }} busy={act.isPending} error={act.error}
        title={action === 'approve' ? 'Approve' : action === 'reject' ? 'Reject' : action === 'cancel' ? 'Cancel request' : 'Submit'}
        confirmLabel={action === 'approve' ? 'Approve' : action === 'reject' ? 'Reject' : action === 'cancel' ? 'Cancel request' : 'Submit'}
        danger={action === 'reject' || action === 'cancel'} reason={action === 'reject' ? 'required' : action === 'submit' ? undefined : 'optional'}
        onConfirm={(reason) => act.mutate(reason ? { reason } : {}, { onSuccess: () => { setAction(null); toast('Saved'); void req.refetch(); } })} />
    </div>
  );
}

// ── Approval Workflows (FR-APR-01/02) ─────────────────────────────────────

type Workflow = Schemas['Workflow'];
type WfStep = Schemas['WorkflowStep'];

function WorkflowEditor({ wf, onDone }: { wf?: Workflow; onDone: () => void }) {
  const toast = useToast();
  const types = useGet<Page<Schemas['DocumentTypeView']>>('/api/v1/platform/approval-document-types');
  const roles = useGet<Page<Schemas['Role']>>('/api/v1/platform/roles');
  const { me } = useAuth();
  const [docType, setDocType] = useState(wf?.documentType ?? 'test_approval');
  const [name, setName] = useState(wf?.name ?? '');
  const [prop, setProp] = useState(wf?.propertyId ?? '');
  const [priority, setPriority] = useState(String(wf?.priority ?? 100));
  const [steps, setSteps] = useState<WfStep[]>(wf?.steps ?? [{ stepNo: 1, name: 'Approval', approverType: 'role', approverRoleId: null, approverUserId: null, conditions: [], slaHours: 24 }]);
  const save = useSend<Record<string, unknown>>(wf ? 'PATCH' : 'POST', wf ? `/api/v1/platform/approval-workflows/${wf.id}` : '/api/v1/platform/approval-workflows',
    ['/api/v1/platform/approval-workflows']);
  const attrs = types.data?.items.find((t) => t.code === docType)?.attributes ?? [];
  const upd = (i: number, patch: Partial<WfStep>) => setSteps((s) => s.map((x, j) => (j === i ? { ...x, ...patch } : x)));
  const fe = fieldErrors(save.error);
  return (
    <div className="oc-stack">
      <div className="oc-form">
        <SelectField label="Document Type" value={docType} onChange={setDocType} options={(types.data?.items ?? []).map((t) => ({ value: t.code, label: t.name }))} />
        <TextField label="Name" value={name} onChange={setName} required error={fe.name} />
        <SelectField label="Property" value={prop} onChange={setProp} placeholder="All properties" options={(me?.properties ?? []).map((p) => ({ value: p.id, label: p.name }))} />
        <TextField label="Priority" type="number" value={priority} onChange={setPriority} help="Lower wins when several workflows match" />
      </div>
      {steps.map((s, i) => (
        <div key={i} className="oc-card" style={{ background: 'var(--md-sys-color-surface-container-low)' }}>
          <div className="oc-row"><strong>Step {s.stepNo}</strong><span className="oc-spacer" />
            <button type="button" className="oc-icon-btn" aria-label="Remove step" onClick={() => setSteps(steps.filter((_, j) => j !== i).map((x, j) => ({ ...x, stepNo: j + 1 })))}><Icon name="delete" size={18} /></button></div>
          <div className="oc-form">
            <TextField label="Step name" value={s.name} onChange={(v) => upd(i, { name: v })} />
            <SelectField label="Approver role" value={s.approverRoleId ?? ''} placeholder="Select…" onChange={(v) => upd(i, { approverType: 'role', approverRoleId: v })}
              options={(roles.data?.items ?? []).map((r) => ({ value: r.id, label: r.name }))} />
            <TextField label="SLA (hours)" type="number" value={String(s.slaHours ?? '')} onChange={(v) => upd(i, { slaHours: v ? Number(v) : null })} />
          </div>
          <div className="oc-label" style={{ marginTop: 12 }}>Conditions (all must match)</div>
          {(s.conditions ?? []).map((c, k) => (
            <div key={k} className="oc-row-wrap">
              <div style={{ width: 180 }}><SelectField label="Attribute" value={c.attribute} onChange={(v) => upd(i, { conditions: s.conditions.map((x, m) => (m === k ? { ...x, attribute: v } : x)) })}
                options={[...attrs.map((a) => ({ value: a.key, label: a.label })), { value: 'propertyId', label: 'Property' }]} /></div>
              <div style={{ width: 150 }}><SelectField label="Operator" value={c.operator} onChange={(v) => upd(i, { conditions: s.conditions.map((x, m) => (m === k ? { ...x, operator: v as typeof x.operator } : x)) })}
                options={['eq', 'neq', 'gt', 'gte', 'lt', 'lte'].map((o) => ({ value: o, label: { eq: '=', neq: '≠', gt: '>', gte: '≥', lt: '<', lte: '≤' }[o]! }))} /></div>
              <div style={{ width: 180 }}><TextField label="Value" value={String(c.value ?? '')} onChange={(v) => upd(i, { conditions: s.conditions.map((x, m) => (m === k ? { ...x, value: isNaN(Number(v)) || v === '' ? v : Number(v) } : x)) })} /></div>
              <button type="button" className="oc-icon-btn" style={{ alignSelf: 'flex-end' }} aria-label="Remove condition" onClick={() => upd(i, { conditions: s.conditions.filter((_, m) => m !== k) })}><Icon name="close" size={18} /></button>
            </div>
          ))}
          <button type="button" className="oc-btn oc-btn-text oc-btn-sm" onClick={() => upd(i, { conditions: [...(s.conditions ?? []), { attribute: attrs[0]?.key ?? 'amount', operator: 'gt', value: 0 }] })}>
            <Icon name="add" size={18} /> Add condition</button>
        </div>
      ))}
      <div><button type="button" className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setSteps([...steps, { stepNo: steps.length + 1, name: `Step ${steps.length + 1}`, approverType: 'role', approverRoleId: null, approverUserId: null, conditions: [], slaHours: 24 }])}>
        <Icon name="add" size={18} /> Add step</button></div>
      <ErrorAlert error={save.error} />
      <div className="oc-row"><span className="oc-spacer" /><button className="oc-btn oc-btn-neutral" onClick={onDone}>Cancel</button>
        <button className="oc-btn oc-btn-ink" disabled={save.isPending} onClick={() => {
          const body: Record<string, unknown> = { name, priority: Number(priority), steps, ...(prop ? { propertyId: prop } : {}) };
          if (!wf) body.documentType = docType;
          save.mutate(body, { onSuccess: () => { toast('Saved'); onDone(); } });
        }}>Save</button></div>
    </div>
  );
}

export function ApprovalWorkflowsPage() {
  const { can } = useAuth();
  const { t } = useTranslation();
  const list = useGet<Page<Workflow>>('/api/v1/platform/approval-workflows');
  const [edit, setEdit] = useState<Workflow | 'new' | null>(null);
  const toggle = useSend<{ id: string; status: string }>('PATCH', (b) => `/api/v1/platform/approval-workflows/${b.id}`, ['/api/v1/platform/approval-workflows']);
  return (
    <div className="oc-stack">
      <PageHeader title="Approval Workflows" help={t('help.approvalWorkflows')} actions={can('platform.approval_workflow.manage') &&
        <button className="oc-btn oc-btn-primary" onClick={() => setEdit('new')}><Icon name="add" size={18} /> Add Workflow</button>} />
      <div className="oc-card">
        <DataTable rows={list.data?.items as unknown as Record<string, unknown>[]} loading={list.isLoading} onRowClick={(r) => setEdit(r as unknown as Workflow)}
          columns={[
            { key: 'name', header: 'Workflow' }, { key: 'documentType', header: 'Document Type' },
            { key: 'steps', header: 'Steps', render: (w) => (w.steps as WfStep[]).map((s) => s.name).join(' → ') },
            { key: 'priority', header: 'Priority', align: 'right' },
            { key: 'status', header: 'Status', render: (w) => <StatusPill status={String(w.status)} /> },
          ]}
          actions={(w) => can('platform.approval_workflow.manage') && (
            <button className="oc-btn oc-btn-outline oc-btn-sm" onClick={() => toggle.mutate({ id: String(w.id), status: w.status === 'active' ? 'inactive' : 'active' } as never)}>
              {w.status === 'active' ? 'Deactivate' : 'Activate'}</button>)} />
      </div>
      <Drawer open={!!edit} onClose={() => setEdit(null)} title={edit === 'new' ? 'Add Workflow' : (edit?.name ?? '')}>
        {edit && <WorkflowEditor key={edit === 'new' ? 'new' : edit.id} wf={edit === 'new' ? undefined : edit} onDone={() => setEdit(null)} />}
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
