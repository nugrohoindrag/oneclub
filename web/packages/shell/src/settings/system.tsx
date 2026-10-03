import React, { useState } from 'react';
import { Link } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { qs, request, useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDateTime, formatRelative, useTranslation } from '@oneclub/i18n';
import { useAuth } from '../context';
import {
  Card, Checkbox, ConfirmDialog, DataTable, Drawer, ErrorAlert, FilterPills, Icon, Modal, PageHeader, SearchBox, SelectField, StatusPill, TextArea,
  TextField, fieldErrors, useDebounced,
} from '../components/ui';
import { useToast } from '../components/toast';

function OnceSecret({ label, value, onClose }: { label: string; value: string | null; onClose: () => void }) {
  const toast = useToast();
  const { t } = useTranslation();
  return (
    <Modal open={!!value} onClose={onClose} title={label} actions={<button className="oc-btn oc-btn-ink" onClick={onClose}>Done</button>}>
      <div className="oc-alert oc-alert-warning" style={{ marginBottom: 12 }}>Copy it now — it is shown only once and stored hashed.</div>
      <div className="oc-row"><code className="oc-code" style={{ flex: 1, padding: 10 }}>{value}</code>
        <button className="oc-icon-btn" aria-label="Copy" onClick={() => { void navigator.clipboard?.writeText(value ?? ''); toast(t('common.copied')); }}><Icon name="content_copy" size={18} /></button></div>
    </Modal>
  );
}

// ── Integrations (FR-INT-02) ──────────────────────────────────────────────

type Integration = Schemas['Integration'];
type Adapter = Schemas['AdapterInfo'];

function IntegrationForm({ integration, adapters, onDone }: { integration?: Integration; adapters: Adapter[]; onDone: () => void }) {
  const toast = useToast();
  const { t } = useTranslation();
  const [adapter, setAdapter] = useState(integration?.adapter ?? adapters[0]?.key ?? '');
  const [code, setCode] = useState(integration?.code ?? '');
  const [name, setName] = useState(integration?.name ?? '');
  const [mode, setMode] = useState(integration?.mode ?? 'sandbox');
  const [enabled, setEnabled] = useState(integration?.enabled ?? false);
  const [creds, setCreds] = useState<Record<string, string>>({});
  const [settings, setSettings] = useState<Record<string, unknown>>(integration?.settings ?? {});
  const info = adapters.find((a) => a.key === adapter);
  const save = useSend<Record<string, unknown>>(integration ? 'PATCH' : 'POST', integration ? `/api/v1/platform/integrations/${integration.id}` : '/api/v1/platform/integrations',
    ['/api/v1/platform/integrations']);
  const fe = fieldErrors(save.error);
  return (
    <div className="oc-stack">
      {info && <div className="oc-alert oc-alert-info">{info.description}</div>}
      <div className="oc-form">
        {!integration && <SelectField label="Adapter" value={adapter} onChange={setAdapter}
          options={adapters.map((a) => ({ value: a.key, label: `${a.name} · ${a.capability}${a.sandbox ? ' (sandbox)' : ''}` }))} />}
        {!integration && <TextField label="Code" value={code} onChange={setCode} required error={fe.code} help="lower-case, e.g. midtrans-main" />}
        <TextField label="Name" value={name} onChange={setName} required error={fe.name} />
        <SelectField label="Mode" value={mode} onChange={(v) => setMode(v as typeof mode)} options={[{ value: 'sandbox', label: 'Sandbox' }, { value: 'production', label: 'Production' }]} />
        <div className="oc-field"><span className="oc-label">&nbsp;</span><Checkbox label="Enabled" checked={enabled} onChange={setEnabled} /></div>
      </div>
      {(info?.credentials ?? []).length > 0 && <div className="oc-label">Credentials {integration && <span className="oc-muted oc-small">(set: {integration.credentialKeys.join(', ') || 'none'}; leave empty to keep)</span>}</div>}
      <div className="oc-form">
        {(info?.credentials ?? []).map((f) => (
          <TextField key={f.key} label={f.label} required={f.required && !integration} type={f.type === 'secret' ? 'password' : f.type === 'number' ? 'number' : 'text'}
            value={creds[f.key] ?? ''} onChange={(v) => setCreds((c) => ({ ...c, [f.key]: v }))} error={fe[`credentials.${f.key}`]} autoComplete="off" />
        ))}
        {(info?.settings ?? []).map((f) => f.type === 'boolean'
          ? <div key={f.key} className="oc-field"><span className="oc-label">&nbsp;</span><Checkbox label={f.label} checked={Boolean(settings[f.key])} onChange={(v) => setSettings((s) => ({ ...s, [f.key]: v }))} /></div>
          : <TextField key={f.key} label={f.label} value={String(settings[f.key] ?? '')} onChange={(v) => setSettings((s) => ({ ...s, [f.key]: f.type === 'number' ? Number(v) : v }))} />)}
      </div>
      <ErrorAlert error={Object.keys(fe).length ? null : save.error} />
      <div className="oc-row"><span className="oc-spacer" /><button className="oc-btn oc-btn-neutral" onClick={onDone}>Cancel</button>
        <button className="oc-btn oc-btn-ink" disabled={save.isPending} onClick={() => {
          const c = Object.fromEntries(Object.entries(creds).filter(([, v]) => v !== ''));
          const body: Record<string, unknown> = { name, mode, enabled, settings, ...(Object.keys(c).length ? { credentials: c } : {}) };
          if (!integration) Object.assign(body, { code, adapter });
          save.mutate(body, { onSuccess: () => { toast(t('common.saved')); onDone(); } });
        }}>Save</button></div>
    </div>
  );
}

export function IntegrationsPage() {
  const { can } = useAuth();
  const { t } = useTranslation();
  const toast = useToast();
  const qc = useQueryClient();
  const list = useGet<Page<Integration>>('/api/v1/platform/integrations');
  const adapters = useGet<Page<Adapter>>('/api/v1/platform/integrations/adapters');
  const [edit, setEdit] = useState<Integration | 'new' | null>(null);
  const [secret, setSecret] = useState<string | null>(null);
  const test = async (i: Integration) => {
    const r = await request<Schemas['TestResult']>('POST', `/api/v1/platform/integrations/${i.id}:test`);
    toast(`${r.ok ? '✓' : '✗'} ${r.message}`, r.ok ? 'success' : 'error');
    void qc.invalidateQueries({ predicate: (q) => String(q.queryKey[0]).startsWith('/api/v1/platform/integrations') });
  };
  return (
    <div className="oc-stack">
      <PageHeader title="Integrations" help={t('help.integrations')} actions={<>
        <Link className="oc-btn oc-btn-outline" to="/settings/system/integration-logs">Integration Logs</Link>
        {can('platform.integration.manage') && <button className="oc-btn oc-btn-primary" onClick={() => setEdit('new')}><Icon name="add" size={18} /> Add Integration</button>}
      </>} />
      <div className="oc-card">
        <DataTable rows={list.data?.items as unknown as Record<string, unknown>[]} loading={list.isLoading}
          onRowClick={can('platform.integration.manage') ? (r) => setEdit(r as unknown as Integration) : undefined}
          columns={[
            { key: 'name', header: 'Integration', render: (i) => <div><div style={{ fontWeight: 600 }}>{String(i.name)}</div><div className="oc-small oc-muted">{String(i.adapterName)} · <code>{String(i.code)}</code></div></div> },
            { key: 'capability', header: 'Capability' },
            { key: 'mode', header: 'Mode', render: (i) => <StatusPill status={String(i.mode)} /> },
            { key: 'enabled', header: 'Status', render: (i) => <StatusPill status={i.enabled ? 'active' : 'inactive'} label={i.enabled ? 'Enabled' : 'Disabled'} /> },
            { key: 'lastTestedAt', header: 'Last test', render: (i) => (i.lastTestedAt ? <span className="oc-small">{i.lastTestOk ? '✓' : '✗'} {formatRelative(String(i.lastTestedAt))}</span> : '—') },
          ]}
          actions={(i) => <div className="oc-row">
            {can('platform.integration.test') && <button className="oc-btn oc-btn-outline oc-btn-sm" onClick={() => void test(i as unknown as Integration)}>Test Connection</button>}
            {can('platform.integration.manage') && i.webhookUrl ? <button className="oc-btn oc-btn-text oc-btn-sm" onClick={async () => {
              const r = await request<{ webhookSecret: string; webhookUrl: string }>('POST', `/api/v1/platform/integrations/${i.id}:rotate-webhook-secret`);
              setSecret(`${window.location.origin}${r.webhookUrl}\nsecret: ${r.webhookSecret}`);
            }}>Webhook secret</button> : null}
          </div>} />
      </div>
      <Drawer open={!!edit} onClose={() => setEdit(null)} title={edit === 'new' ? 'Add Integration' : (edit?.name ?? '')}>
        {edit && adapters.data && <IntegrationForm key={edit === 'new' ? 'new' : edit.id} integration={edit === 'new' ? undefined : edit} adapters={adapters.data.items} onDone={() => setEdit(null)} />}
      </Drawer>
      <OnceSecret label="Webhook endpoint & signing secret" value={secret} onClose={() => setSecret(null)} />
    </div>
  );
}

export function IntegrationLogsPage() {
  const [q, setQ] = useState('');
  const [success, setSuccess] = useState('');
  const [open, setOpen] = useState<Schemas['IntegrationLog'] | null>(null);
  const query = useDebounced(q);
  const list = useGet<Page<Schemas['IntegrationLog']>>(`/api/v1/platform/integration-logs${qs({ q: query, 'filter[success]': success, limit: 200 })}`);
  return (
    <div className="oc-stack">
      <PageHeader title="Integration Logs" help="Every inbound and outbound call, with personal data and secrets masked." />
      <div className="oc-card">
        <div className="oc-toolbar"><SearchBox value={q} onChange={setQ} /><span className="oc-spacer" />
          <FilterPills value={success} onChange={setSuccess} options={[{ value: '', label: 'All' }, { value: 'true', label: 'Success' }, { value: 'false', label: 'Failed' }]} /></div>
        <DataTable rows={list.data?.items as unknown as Record<string, unknown>[]} loading={list.isLoading} onRowClick={(r) => setOpen(r as unknown as Schemas['IntegrationLog'])}
          columns={[
            { key: 'createdAt', header: 'Time', render: (l) => formatDateTime(String(l.createdAt)) },
            { key: 'integrationCode', header: 'Integration' }, { key: 'direction', header: 'Direction' }, { key: 'operation', header: 'Operation' },
            { key: 'statusCode', header: 'HTTP', align: 'right' }, { key: 'durationMs', header: 'ms', align: 'right' },
            { key: 'success', header: 'Result', render: (l) => <StatusPill status={l.success ? 'completed' : 'failed'} /> },
          ]} />
      </div>
      <Drawer open={!!open} onClose={() => setOpen(null)} title={open ? `${open.integrationCode} · ${open.operation}` : ''}>
        {open && <div className="oc-stack">
          {open.error && <div className="oc-alert oc-alert-error">{open.error}</div>}
          <div className="oc-label">Request</div><pre className="oc-pre">{JSON.stringify(open.request, null, 2)}</pre>
          <div className="oc-label">Response</div><pre className="oc-pre">{JSON.stringify(open.response, null, 2)}</pre>
        </div>}
      </Drawer>
    </div>
  );
}

// ── Background Jobs (FR-JOB-04) ───────────────────────────────────────────

type Job = Schemas['Job'];

export function BackgroundJobsPage() {
  const { can } = useAuth();
  const { t } = useTranslation();
  const toast = useToast();
  const [state, setState] = useState('retryable,discarded');
  const [open, setOpen] = useState<Job | null>(null);
  const [discard, setDiscard] = useState<Job | null>(null);
  const list = useGet<Page<Job>>(`/api/v1/platform/jobs${qs({ 'filter[state]': state, limit: 100 })}`, { refetchInterval: 10_000 });
  const retry = useSend<{ id: number }>('POST', (b) => `/api/v1/platform/jobs/${b.id}:retry`, ['/api/v1/platform/jobs']);
  const disc = useSend<{ reason: string }>('POST', () => `/api/v1/platform/jobs/${discard?.id}:discard`, ['/api/v1/platform/jobs']);
  return (
    <div className="oc-stack">
      <PageHeader title="Background Jobs" help={t('help.backgroundJobs')} />
      <div className="oc-card">
        <div className="oc-toolbar"><FilterPills value={state} onChange={setState} options={[
          { value: 'retryable,discarded', label: 'Failed & retrying' }, { value: 'discarded', label: 'Failed' }, { value: 'retryable', label: 'Retrying' },
          { value: 'available,scheduled,running', label: 'Queued' }, { value: 'cancelled', label: 'Discarded' }, { value: 'completed', label: 'Completed' }]} /></div>
        <DataTable rows={list.data?.items as unknown as Record<string, unknown>[]} loading={list.isLoading} rowKey={(j) => String(j.id)} onRowClick={(j) => setOpen(j as unknown as Job)}
          columns={[
            { key: 'id', header: '#', align: 'right' },
            { key: 'kind', header: 'Job', render: (j) => <code className="oc-code">{String(j.kind)}</code> },
            { key: 'attempt', header: 'Attempts', render: (j) => `${j.attempt}/${j.maxAttempts}` },
            { key: 'error', header: 'Last error', render: (j) => <span className="oc-small">{((j.errors as Schemas['JobError'][]).at(-1)?.error ?? '').slice(0, 90)}</span> },
            { key: 'createdAt', header: 'Created', render: (j) => formatRelative(String(j.createdAt)) },
            { key: 'state', header: 'Status', render: (j) => <StatusPill status={String(j.state)} /> },
          ]}
          actions={(j) => <div className="oc-row">
            {can('platform.job.retry') && j.state !== 'running' && j.state !== 'completed' && <button className="oc-btn oc-btn-outline oc-btn-sm" disabled={retry.isPending}
              onClick={() => retry.mutate({ id: Number(j.id) } as never, { onSuccess: () => toast('Retry scheduled') })}>Retry</button>}
            {can('platform.job.discard') && ['retryable', 'available', 'scheduled', 'discarded'].includes(String(j.state)) && <button className="oc-btn oc-btn-text oc-btn-sm"
              onClick={() => setDiscard(j as unknown as Job)}>Discard</button>}
          </div>} />
        <ErrorAlert error={retry.error} />
      </div>
      <Drawer open={!!open} onClose={() => setOpen(null)} title={open ? `${open.kind} #${open.id}` : ''}>
        {open && <div className="oc-stack">
          <pre className="oc-pre">{JSON.stringify(open.args, null, 2)}</pre>
          {open.errors.map((e) => <div key={e.attempt} className="oc-alert oc-alert-error"><strong>Attempt {e.attempt}</strong> · {formatDateTime(e.at)}<div>{e.error}</div></div>)}
        </div>}
      </Drawer>
      <ConfirmDialog open={!!discard} onClose={() => { setDiscard(null); disc.reset(); }} danger reason="required" confirmLabel="Discard" title="Discard job" error={disc.error}
        message="The job will not run again. The reason is recorded in the audit log."
        onConfirm={(reason) => disc.mutate({ reason }, { onSuccess: () => { setDiscard(null); toast('Discarded'); } })} />
    </div>
  );
}

// ── Devices (FR-IAM-09) ───────────────────────────────────────────────────

export function DevicesPage() {
  const { can } = useAuth();
  const { t } = useTranslation();
  const list = useGet<Page<Schemas['Device']>>('/api/v1/platform/devices');
  const [open, setOpen] = useState(false);
  const [name, setName] = useState('');
  const [type, setType] = useState('pos');
  const [token, setToken] = useState<string | null>(null);
  const add = useSend<Record<string, unknown>, Schemas['Device']>('POST', '/api/v1/platform/devices', ['/api/v1/platform/devices']);
  const patch = useSend<{ id: string; status: string }>('PATCH', (b) => `/api/v1/platform/devices/${b.id}`, ['/api/v1/platform/devices']);
  const rotate = useSend<{ id: string }, Schemas['Device']>('POST', (b) => `/api/v1/platform/devices/${b.id}:rotate-token`, ['/api/v1/platform/devices']);
  return (
    <div className="oc-stack">
      <PageHeader title="Devices" help={t('help.devices')} actions={can('platform.device.manage') && <button className="oc-btn oc-btn-primary" onClick={() => setOpen(true)}><Icon name="add" size={18} /> Register Device</button>} />
      <div className="oc-card">
        <DataTable rows={list.data?.items as unknown as Record<string, unknown>[]} loading={list.isLoading}
          columns={[{ key: 'name', header: 'Device' }, { key: 'deviceType', header: 'Type' },
            { key: 'lastSeenAt', header: 'Last seen', render: (d) => (d.lastSeenAt ? formatRelative(String(d.lastSeenAt)) : 'Never') },
            { key: 'status', header: 'Status', render: (d) => <StatusPill status={String(d.status)} /> }]}
          actions={(d) => can('platform.device.manage') && <div className="oc-row">
            <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => rotate.mutate({ id: String(d.id) } as never, { onSuccess: (r) => setToken(r.deviceToken ?? null) })}>New token</button>
            <button className="oc-btn oc-btn-outline oc-btn-sm" onClick={() => patch.mutate({ id: String(d.id), status: d.status === 'active' ? 'inactive' : 'active' } as never)}>
              {d.status === 'active' ? 'Deactivate' : 'Activate'}</button></div>} />
      </div>
      <Modal open={open} onClose={() => setOpen(false)} title="Register Device" actions={<>
        <button className="oc-btn oc-btn-neutral" onClick={() => setOpen(false)}>Cancel</button>
        <button className="oc-btn oc-btn-ink" onClick={() => add.mutate({ name, deviceType: type }, { onSuccess: (d) => { setOpen(false); setName(''); setToken(d.deviceToken ?? null); } })}>Register</button></>}>
        <div className="oc-form">
          <TextField label="Name" value={name} onChange={setName} placeholder="POS Halfway House" />
          <SelectField label="Type" value={type} onChange={setType} options={[{ value: 'pos', label: 'POS' }, { value: 'tablet', label: 'Tablet' }, { value: 'kiosk', label: 'Kiosk' }, { value: 'other', label: 'Other' }]} />
        </div>
        <ErrorAlert error={add.error} />
      </Modal>
      <OnceSecret label="Device token" value={token} onClose={() => setToken(null)} />
    </div>
  );
}

// ── Master Data Import (FR-MD-06) ─────────────────────────────────────────

export function MasterDataImportPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const entities = useGet<Page<Schemas['ImportEntity']>>('/api/v1/platform/imports/entities');
  const history = useGet<Page<Schemas['ImportResult']>>('/api/v1/platform/imports');
  const [entity, setEntity] = useState('');
  const [file, setFile] = useState<File | null>(null);
  const [result, setResult] = useState<Schemas['ImportResult'] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const ent = entities.data?.items.find((e) => e.key === entity);
  const run = async (mode: 'preview' | 'commit') => {
    if (!file || !entity) return;
    setBusy(true);
    setError(null);
    const fd = new FormData();
    fd.append('entity', entity);
    fd.append('mode', mode);
    fd.append('file', file);
    try {
      const r = await request<Schemas['ImportResult']>('POST', '/api/v1/platform/imports', fd);
      setResult(r);
      if (mode === 'commit') { toast(`${r.insertedRows} added, ${r.updatedRows} updated, ${r.failedRows} failed`); void history.refetch(); }
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  const template = () => {
    if (!ent) return;
    const blob = new Blob([ent.columns.join(',') + '\n'], { type: 'text/csv' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `${ent.key}-template.csv`;
    a.click();
  };
  return (
    <div className="oc-stack">
      <PageHeader title="Master Data Import" help={t('help.masterDataImport')} />
      <Card title="Import CSV" icon="upload_file">
        <div className="oc-form">
          <SelectField label="Entity" value={entity} onChange={(v) => { setEntity(v); setResult(null); }} placeholder="Select…"
            options={(entities.data?.items ?? []).map((e) => ({ value: e.key, label: `${e.plural}${e.propertyScoped ? ' (this property)' : ''}` }))} />
          <div className="oc-field"><label className="oc-label" htmlFor="import-file">CSV file</label>
            <input id="import-file" className="oc-input" type="file" accept=".csv,text/csv" onChange={(e) => { setFile(e.target.files?.[0] ?? null); setResult(null); }} style={{ paddingTop: 10 }} /></div>
        </div>
        {ent && <p className="oc-small oc-muted">Columns: <code className="oc-code">{ent.columns.join(', ')}</code> · required: {ent.required.join(', ')} · matched by <strong>{ent.codeField}</strong>{' '}
          <button className="oc-btn oc-btn-text oc-btn-sm" onClick={template}>Download template</button></p>}
        <div className="oc-row" style={{ marginTop: 12 }}>
          <button className="oc-btn oc-btn-neutral" disabled={!file || !entity || busy} onClick={() => void run('preview')}>Preview</button>
          <button className="oc-btn oc-btn-ink" disabled={!file || !entity || busy || !result || result.mode !== 'preview'} onClick={() => void run('commit')}>Import</button>
        </div>
        <ErrorAlert error={error} />
        {result && (
          <div className="oc-stack" style={{ marginTop: 16 }}>
            <div className="oc-row-wrap">
              <StatusPill status={result.status} /><span className="oc-chip">{result.totalRows} rows</span>
              <span className="oc-chip">{result.insertedRows} new</span><span className="oc-chip">{result.updatedRows} updated</span>
              <span className="oc-chip">{result.failedRows} errors</span>{result.mode === 'preview' && <span className="oc-small oc-muted">Preview — nothing saved yet.</span>}
            </div>
            {result.errors.length > 0 && <DataTable rows={result.errors as unknown as Record<string, unknown>[]} rowKey={(e) => `${e.row}-${e.field}-${e.code}`}
              columns={[{ key: 'row', header: 'Line', align: 'right' }, { key: 'field', header: 'Field' }, { key: 'message', header: 'Error' }]} />}
          </div>
        )}
      </Card>
      <Card title="History" icon="history">
        <DataTable rows={history.data?.items as unknown as Record<string, unknown>[]} loading={history.isLoading} rowKey={(r) => String(r.id)}
          columns={[{ key: 'createdAt', header: 'Time', render: (r) => formatDateTime(String(r.createdAt)) }, { key: 'entity', header: 'Entity' }, { key: 'filename', header: 'File' },
            { key: 'insertedRows', header: 'New', align: 'right' }, { key: 'updatedRows', header: 'Updated', align: 'right' }, { key: 'failedRows', header: 'Errors', align: 'right' },
            { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]} />
      </Card>
    </div>
  );
}

// ── API keys (FR-INT-06) ──────────────────────────────────────────────────

export function ApiKeysPage() {
  const { can } = useAuth();
  const { t } = useTranslation();
  const list = useGet<Page<Schemas['APIKey']>>('/api/v1/platform/api-keys');
  const perms = useGet<Page<Schemas['PermissionInfo']>>('/api/v1/platform/permissions');
  const [open, setOpen] = useState(false);
  const [name, setName] = useState('');
  const [scopes, setScopes] = useState<string[]>([]);
  const [filter, setFilter] = useState('');
  const [key, setKey] = useState<string | null>(null);
  const create = useSend<Record<string, unknown>, Schemas['APIKey']>('POST', '/api/v1/platform/api-keys', ['/api/v1/platform/api-keys']);
  const act = useSend<{ id: string; a: string }, Schemas['APIKey']>('POST', (b) => `/api/v1/platform/api-keys/${b.id}:${b.a}`, ['/api/v1/platform/api-keys']);
  return (
    <div className="oc-stack">
      <PageHeader title="API Keys" help={t('help.apiKeys')} actions={can('platform.api_key.manage') && <button className="oc-btn oc-btn-primary" onClick={() => setOpen(true)}><Icon name="add" size={18} /> Create Key</button>} />
      <div className="oc-card">
        <DataTable rows={list.data?.items as unknown as Record<string, unknown>[]} loading={list.isLoading}
          columns={[{ key: 'name', header: 'Name' }, { key: 'prefix', header: 'Prefix', render: (k) => <code className="oc-code">ock_{String(k.prefix)}_…</code> },
            { key: 'scopes', header: 'Scopes', render: (k) => <span className="oc-small">{(k.scopes as string[]).join(', ')}</span> },
            { key: 'lastUsedAt', header: 'Last used', render: (k) => (k.lastUsedAt ? formatRelative(String(k.lastUsedAt)) : 'Never') },
            { key: 'status', header: 'Status', render: (k) => <StatusPill status={k.revokedAt ? 'inactive' : 'active'} label={k.revokedAt ? 'Revoked' : 'Active'} /> }]}
          actions={(k) => can('platform.api_key.manage') && !k.revokedAt && <div className="oc-row">
            <button className="oc-btn oc-btn-outline oc-btn-sm" onClick={() => act.mutate({ id: String(k.id), a: 'rotate' } as never, { onSuccess: (r) => setKey(r.key ?? null) })}>Rotate</button>
            <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => act.mutate({ id: String(k.id), a: 'revoke' } as never)}>Revoke</button></div>} />
      </div>
      <Modal open={open} onClose={() => setOpen(false)} title="Create API Key" wide actions={<>
        <button className="oc-btn oc-btn-neutral" onClick={() => setOpen(false)}>Cancel</button>
        <button className="oc-btn oc-btn-ink" disabled={!name || scopes.length === 0} onClick={() => create.mutate({ name, scopes }, { onSuccess: (k) => { setOpen(false); setName(''); setScopes([]); setKey(k.key ?? null); } })}>Create</button></>}>
        <div className="oc-stack">
          <TextField label="Name" value={name} onChange={setName} placeholder="Rhapsody migration" />
          <SearchBox value={filter} onChange={setFilter} placeholder="Filter permissions" />
          <div className="oc-row-wrap" style={{ maxHeight: 260, overflow: 'auto' }}>
            {(perms.data?.items ?? []).filter((p) => p.code.includes(filter)).map((p) => (
              <button key={p.code} className="oc-chip" aria-pressed={scopes.includes(p.code)} onClick={() => setScopes((s) => (s.includes(p.code) ? s.filter((x) => x !== p.code) : [...s, p.code]))}>{p.code}</button>
            ))}
          </div>
          <ErrorAlert error={create.error} />
        </div>
      </Modal>
      <OnceSecret label="API key" value={key} onClose={() => setKey(null)} />
    </div>
  );
}

// ── Bridge agents (FR-INT-07) ─────────────────────────────────────────────

export function BridgeAgentsPage() {
  const { can } = useAuth();
  const list = useGet<Page<Schemas['BridgeAgent']>>('/api/v1/platform/bridge-agents', { refetchInterval: 30_000 });
  const [open, setOpen] = useState(false);
  const [name, setName] = useState('');
  const [token, setToken] = useState<string | null>(null);
  const add = useSend<Record<string, unknown>, Schemas['BridgeAgent']>('POST', '/api/v1/platform/bridge-agents', ['/api/v1/platform/bridge-agents']);
  return (
    <div className="oc-stack">
      <PageHeader title="Bridge Agents" help="Local agents in the club network for lockers, turnstiles and ball dispensers. Hardware is never exposed to the internet."
        actions={can('platform.bridge_agent.manage') && <button className="oc-btn oc-btn-primary" onClick={() => setOpen(true)}><Icon name="add" size={18} /> Register Agent</button>} />
      <div className="oc-card">
        <DataTable rows={list.data?.items as unknown as Record<string, unknown>[]} loading={list.isLoading}
          columns={[{ key: 'name', header: 'Agent' }, { key: 'agentVersion', header: 'Version' },
            { key: 'hardware', header: 'Hardware', render: (a) => (a.hardware as unknown[]).length },
            { key: 'lastHeartbeatAt', header: 'Last heartbeat', render: (a) => (a.lastHeartbeatAt ? formatRelative(String(a.lastHeartbeatAt)) : 'Never') },
            { key: 'online', header: 'Status', render: (a) => <StatusPill status={a.status !== 'active' ? 'inactive' : a.online ? 'online' : 'offline'} /> }]} />
      </div>
      <Modal open={open} onClose={() => setOpen(false)} title="Register Agent" actions={<>
        <button className="oc-btn oc-btn-neutral" onClick={() => setOpen(false)}>Cancel</button>
        <button className="oc-btn oc-btn-ink" onClick={() => add.mutate({ name }, { onSuccess: (a) => { setOpen(false); setName(''); setToken(a.token ?? null); } })}>Register</button></>}>
        <TextField label="Name" value={name} onChange={setName} placeholder="Locker room bridge" />
        <p className="oc-small oc-muted">Run on the club server: <code className="oc-code">ONECLUB_API_URL={window.location.origin} ONECLUB_AGENT_TOKEN=… bridge-agent</code></p>
      </Modal>
      <OnceSecret label="Agent token" value={token} onClose={() => setToken(null)} />
    </div>
  );
}

// ── Business Rules / Club Policies ────────────────────────────────────────

const POLICY_CATEGORIES = ['Golf Policies', 'Sport Club Policies', 'Banquet Policies', 'Pricing Policies', 'Cancellation Policies', 'Refund Policies',
  'Guest Policies', 'Member Policies', 'Caddy Policies', 'Golf Cart Policies', 'Weather Policies'];

export function RulesPage({ kind }: { kind: 'business_rule' | 'club_policy' }) {
  const { can } = useAuth();
  const { t } = useTranslation();
  const toast = useToast();
  const base = kind === 'business_rule' ? '/api/v1/platform/business-rules' : '/api/v1/platform/club-policies';
  const perm = kind === 'business_rule' ? 'platform.business_rule.manage' : 'platform.club_policy.manage';
  const title = kind === 'business_rule' ? 'Business Rules' : 'Club Policies';
  const [history, setHistory] = useState(false);
  const list = useGet<Page<Schemas['Rule']>>(`${base}${history ? '?history=true' : ''}`);
  const [open, setOpen] = useState(false);
  const [v, setV] = useState({ category: kind === 'club_policy' ? POLICY_CATEGORIES[0] : '', code: '', name: '', description: '', value: '{}', effectiveFrom: '' });
  const add = useSend<Record<string, unknown>>('POST', base, [base]);
  const fe = fieldErrors(add.error);
  return (
    <div className="oc-stack">
      <PageHeader title={title} help={t(kind === 'business_rule' ? 'help.businessRules' : 'help.clubPolicies')} actions={<>
        <button className="oc-chip" aria-pressed={history} onClick={() => setHistory((h) => !h)}>Show history</button>
        {can(perm) && <button className="oc-btn oc-btn-primary" onClick={() => setOpen(true)}><Icon name="add" size={18} /> Add Version</button>}</>} />
      <div className="oc-card">
        <DataTable rows={list.data?.items as unknown as Record<string, unknown>[]} loading={list.isLoading}
          columns={[{ key: 'category', header: 'Category' }, { key: 'code', header: 'Code', render: (r) => <code className="oc-code">{String(r.code)}</code> },
            { key: 'name', header: 'Name' }, { key: 'version', header: 'Version', align: 'right' },
            { key: 'value', header: 'Value', render: (r) => <code className="oc-code">{JSON.stringify(r.value).slice(0, 60)}</code> },
            { key: 'effectiveFrom', header: 'Effective From', render: (r) => formatDateTime(String(r.effectiveFrom)) },
            { key: 'inEffect', header: 'Status', render: (r) => r.inEffect ? <StatusPill status="active" label="In force" /> : new Date(String(r.effectiveFrom)) > new Date() ? <StatusPill status="scheduled" /> : <StatusPill status={String(r.status)} /> }]} />
      </div>
      <Modal open={open} onClose={() => setOpen(false)} title={`Add ${title === 'Club Policies' ? 'Policy' : 'Rule'} Version`} wide actions={<>
        <button className="oc-btn oc-btn-neutral" onClick={() => setOpen(false)}>Cancel</button>
        <button className="oc-btn oc-btn-ink" onClick={() => {
          let value: unknown;
          try { value = JSON.parse(v.value); } catch { value = v.value; }
          add.mutate({ category: v.category, code: v.code, name: v.name, description: v.description, value, ...(v.effectiveFrom ? { effectiveFrom: new Date(v.effectiveFrom).toISOString() } : {}) },
            { onSuccess: () => { setOpen(false); toast(t('common.saved')); } });
        }}>Save</button></>}>
        <div className="oc-form">
          {kind === 'club_policy'
            ? <SelectField label="Category" value={v.category} onChange={(x) => setV({ ...v, category: x })} options={POLICY_CATEGORIES.map((c) => ({ value: c, label: c }))} />
            : <TextField label="Category" value={v.category} onChange={(x) => setV({ ...v, category: x })} error={fe.category} />}
          <TextField label="Code" value={v.code} onChange={(x) => setV({ ...v, code: x })} placeholder="golf.cancel_hours" error={fe.code} help="Same code = next version" />
          <TextField label="Name" value={v.name} onChange={(x) => setV({ ...v, name: x })} error={fe.name} />
          <TextField label="Effective From" type="datetime-local" value={v.effectiveFrom} onChange={(x) => setV({ ...v, effectiveFrom: x })} error={fe.effectiveFrom} />
          <TextArea label="Value (JSON)" span value={v.value} onChange={(x) => setV({ ...v, value: x })} error={fe.value} />
          <TextArea label="Description" span value={v.description} onChange={(x) => setV({ ...v, description: x })} rows={2} />
        </div>
        <ErrorAlert error={Object.keys(fe).length ? null : add.error} />
      </Modal>
    </div>
  );
}

/** System Settings hub (PRD §6.3 additions). */
export function SystemSettingsPage() {
  const { can } = useAuth();
  const links: [string, string, string, string, string][] = [
    ['background-jobs', 'Background Jobs', 'work_history', 'platform.job.view', 'Failed and retrying jobs; Retry or Discard.'],
    ['devices', 'Devices', 'point_of_sale', 'platform.device.view', 'POS and tablets with staff PIN login.'],
    ['master-data-import', 'Master Data Import', 'upload_file', 'platform.import.view', 'CSV import with preview and row errors.'],
    ['api-keys', 'API Keys', 'key', 'platform.api_key.view', 'Server-to-server access with limited scopes.'],
    ['bridge-agents', 'Bridge Agents', 'router', 'platform.bridge_agent.view', 'Hardware bridges in the club network.'],
    ['integration-logs', 'Integration Logs', 'receipt_long', 'platform.integration_log.view', 'Masked request/response history.'],
    ['notification-history', 'Notification History', 'mark_email_read', 'platform.notification_delivery.view', 'E-mail, in-app and WhatsApp deliveries.'],
  ];
  return (
    <div className="oc-stack">
      <PageHeader title="System Settings" help="Operational tools for administrators." />
      <div className="oc-grid">
        {links.filter((l) => can(l[3])).map(([path, label, icon, , help]) => (
          <Link key={path} to={`/settings/system/${path}`} className="oc-card" style={{ textDecoration: 'none' }}>
            <div className="oc-card-head"><span className="oc-icon-circle"><Icon name={icon} size={20} /></span><h3>{label}</h3></div>
            <div className="oc-small oc-muted">{help}</div>
          </Link>
        ))}
      </div>
    </div>
  );
}
