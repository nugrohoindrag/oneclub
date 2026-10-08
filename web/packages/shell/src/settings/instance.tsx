import React, { useEffect, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { request, useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDateTime, useTranslation } from '@oneclub/i18n';
import { useAuth } from '../context';
import { applyBranding, type BrandingInfo } from '../theme';
import {
  Card, Checkbox, ConfirmDialog, DataTable, ErrorAlert, Icon, Labeled, Modal, PageHeader, SelectField, Skeleton, StatusPill, TextArea, TextField,
  fieldErrors,
} from '../components/ui';
import { useToast } from '../components/toast';

const TIMEZONES = ['Asia/Jakarta', 'Asia/Makassar', 'Asia/Jayapura', 'Asia/Singapore', 'UTC'];

function useSavedToast() {
  const toast = useToast();
  const { t } = useTranslation();
  return () => toast(t('common.saved'));
}

export function OrganizationPage() {
  const { can } = useAuth();
  const { t } = useTranslation();
  const org = useGet<Schemas['Organization']>('/api/v1/platform/organization');
  const save = useSend<Record<string, unknown>>('PATCH', '/api/v1/platform/organization', ['/api/v1/platform/organization']);
  const saved = useSavedToast();
  const [v, setV] = useState<Record<string, string>>({});
  useEffect(() => {
    if (org.data) {
      const d = org.data as unknown as Record<string, unknown>;
      setV(Object.fromEntries(['legalName', 'displayName', 'npwp', 'address', 'city', 'province', 'postalCode', 'phone', 'email', 'website'].map((k) => [k, (d[k] as string) ?? ''])));
    }
  }, [org.data]);
  const fe = fieldErrors(save.error);
  const ro = !can('platform.organization.update');
  const f = (k: string, label: string, extra: Record<string, unknown> = {}) => (
    <TextField key={k} label={label} value={v[k] ?? ''} disabled={ro} onChange={(x) => setV((s) => ({ ...s, [k]: x }))} error={fe[k]} {...extra} />
  );
  if (!org.data) return <Skeleton />;
  return (
    <div className="oc-stack">
      <PageHeader title="Organization" help="Legal identity of the club. Properties, departments and employees are managed in the sub-menus." />
      <Card title="Organization Profile" icon="domain">
        <form className="oc-stack" onSubmit={(e) => {
          e.preventDefault();
          const body = Object.fromEntries(Object.entries(v).filter(([, x]) => x !== ''));
          save.mutate(body, { onSuccess: saved });
        }}>
          <div className="oc-form">
            {f('legalName', 'Legal Name', { required: true })}
            {f('displayName', 'Display Name', { required: true })}
            {f('npwp', 'NPWP', { help: '15 or 16 digits' })}
            {f('phone', 'Phone')}
            {f('email', 'E-mail', { type: 'email' })}
            {f('website', 'Website')}
            <TextArea label="Address" span value={v.address ?? ''} onChange={(x) => setV((s) => ({ ...s, address: x }))} />
            {f('city', 'City')}
            {f('province', 'Province')}
            {f('postalCode', 'Postal Code')}
          </div>
          {Object.keys(fe).length === 0 && <ErrorAlert error={save.error} />}
          {!ro && <div><button className="oc-btn oc-btn-ink" disabled={save.isPending}>Save</button></div>}
        </form>
      </Card>
      <p className="oc-small oc-muted">{t('help.properties')}</p>
    </div>
  );
}

/** Customer Instance (read-only for Super Admin; editable by Platform Admin). */
export function CustomerInstancePage({ title = 'Customer Instance', focus }: { title?: string; focus?: 'locale' | 'currency' | 'timezone' }) {
  const { can } = useAuth();
  const inst = useGet<Schemas['Instance']>('/api/v1/platform/instance');
  const modules = useGet<Page<Schemas['Module']>>('/api/v1/platform/modules');
  const qc = useQueryClient();
  const saved = useSavedToast();
  const editable = can('platform.instance.update');
  const save = useSend<Record<string, unknown>>('PATCH', '/api/v1/platform/instance', ['/api/v1/platform/instance', '/api/v1/public/bootstrap']);
  const [v, setV] = useState({ name: '', defaultLocale: 'id', currency: 'IDR', timezone: 'Asia/Jakarta', defaultTheme: 'light' });
  const [suspend, setSuspend] = useState(false);
  useEffect(() => {
    if (inst.data) setV({ name: inst.data.name, defaultLocale: inst.data.defaultLocale, currency: inst.data.currency, timezone: inst.data.timezone, defaultTheme: inst.data.defaultTheme });
  }, [inst.data]);
  if (!inst.data) return <Skeleton />;
  const d = inst.data;
  return (
    <div className="oc-stack">
      <PageHeader title={title} help={editable ? 'Instance configuration of this dedicated customer instance (Platform Administration).' : 'Read-only. Changes are made by the OneClub Platform Admin.'}
        actions={<StatusPill status={d.status} />} />
      <div className="oc-grid-2">
        <Card title="Instance" icon="dns">
          {editable ? (
            <form className="oc-stack" onSubmit={(e) => { e.preventDefault(); save.mutate(v, { onSuccess: () => { saved(); void qc.invalidateQueries(); } }); }}>
              <div className="oc-form">
                <TextField label="Instance Name" value={v.name} onChange={(x) => setV({ ...v, name: x })} />
                <Labeled label="Instance Code"><code className="oc-code">{d.code}</code></Labeled>
                <SelectField label="Locale" value={v.defaultLocale} onChange={(x) => setV({ ...v, defaultLocale: x })}
                  options={[{ value: 'id', label: 'Bahasa Indonesia' }, { value: 'en', label: 'English' }]} />
                <TextField label="Currency" value={v.currency} onChange={(x) => setV({ ...v, currency: x.toUpperCase() })} autoFocus={focus === 'currency'} />
                <SelectField label="Timezone" value={v.timezone} onChange={(x) => setV({ ...v, timezone: x })}
                  options={[...new Set([v.timezone, ...TIMEZONES])].map((z) => ({ value: z, label: z }))} />
                <SelectField label="Default Theme" value={v.defaultTheme} onChange={(x) => setV({ ...v, defaultTheme: x })}
                  options={[{ value: 'light', label: 'Light' }, { value: 'dark', label: 'Dark' }]} />
              </div>
              <ErrorAlert error={save.error} />
              <div className="oc-row"><button className="oc-btn oc-btn-ink" disabled={save.isPending}>Save</button><span className="oc-spacer" />
                <button type="button" className={`oc-btn ${d.status === 'active' ? 'oc-btn-danger' : 'oc-btn-primary'}`} onClick={() => setSuspend(true)}>
                  {d.status === 'active' ? 'Suspend Instance' : 'Reactivate Instance'}</button></div>
            </form>
          ) : (
            <div className="oc-form">
              <Labeled label="Instance Name">{d.name}</Labeled>
              <Labeled label="Instance Code">{d.code}</Labeled>
              <Labeled label="Locale">{d.defaultLocale === 'id' ? 'Bahasa Indonesia' : 'English'}</Labeled>
              <Labeled label="Currency">{d.currency}</Labeled>
              <Labeled label="Timezone">{d.timezone}</Labeled>
              <Labeled label="Updated">{formatDateTime(d.updatedAt)}</Labeled>
            </div>
          )}
        </Card>
        <Card title="Enabled Modules" icon="apps">
          <div className="oc-row-wrap">
            {(modules.data?.items ?? []).map((m) => <StatusPill key={m.code} status={m.enabled ? 'active' : 'inactive'} label={m.name} />)}
          </div>
        </Card>
      </div>
      <ConfirmDialog open={suspend} onClose={() => setSuspend(false)} danger={d.status === 'active'} title={d.status === 'active' ? 'Suspend instance' : 'Reactivate instance'}
        message={d.status === 'active' ? 'Every user except Platform Admin sees the maintenance page until the instance is reactivated.' : undefined}
        confirmLabel={d.status === 'active' ? 'Suspend' : 'Reactivate'} error={save.error}
        onConfirm={() => save.mutate({ status: d.status === 'active' ? 'suspended' : 'active' }, { onSuccess: () => { setSuspend(false); saved(); } })} />
    </div>
  );
}

/** Features (read-only) / Enabled Modules (Platform Admin) — FR-INS-04. */
export function FeaturesPage({ title = 'Features' }: { title?: string }) {
  const { can } = useAuth();
  const { t } = useTranslation();
  const qc = useQueryClient();
  const saved = useSavedToast();
  const modules = useGet<Page<Schemas['Module']>>('/api/v1/platform/modules');
  const put = useSend<{ modules: { code: string; enabled: boolean }[] }>('PUT', '/api/v1/platform/modules', ['/api/v1/platform/modules']);
  const editable = can('platform.module.update');
  return (
    <div className="oc-stack">
      <PageHeader title={title} help={t('help.features')} />
      <div className="oc-card">
        <DataTable rows={modules.data?.items as unknown as Record<string, unknown>[]} loading={modules.isLoading} rowKey={(r) => String(r.code)}
          columns={[
            { key: 'name', header: 'Module', render: (m) => <div><div style={{ fontWeight: 600 }}>{String(m.name)}</div><div className="oc-small oc-muted">{String(m.layer)}</div></div> },
            { key: 'enabled', header: 'Status', render: (m) => <StatusPill status={m.enabled ? 'active' : 'inactive'} label={m.enabled ? 'Enabled' : 'Disabled'} /> },
            { key: 'updatedAt', header: 'Updated', render: (m) => formatDateTime(String(m.updatedAt)) },
          ]}
          actions={(m) => editable && !m.alwaysOn && (
            <Checkbox label={m.enabled ? 'On' : 'Off'} checked={Boolean(m.enabled)} onChange={(v) => put.mutate({ modules: [{ code: String(m.code), enabled: v }] },
              { onSuccess: () => { saved(); void qc.invalidateQueries(); } })} />
          )} />
        <ErrorAlert error={put.error} />
      </div>
    </div>
  );
}

/** Feature Configuration / Feature Flags — FR-INS-05. */
export function FeatureFlagsPage({ title = 'Feature Configuration' }: { title?: string }) {
  const { can } = useAuth();
  const { t } = useTranslation();
  const saved = useSavedToast();
  const flags = useGet<Page<Schemas['FeatureFlag']>>('/api/v1/platform/feature-flags');
  const [edit, setEdit] = useState<{ key: string; value: string; description: string; valueType: string; clientVisible: boolean } | null>(null);
  const put = useSend<Record<string, unknown>>('PUT', () => `/api/v1/platform/feature-flags/${edit?.key}`, ['/api/v1/platform/feature-flags', '/api/v1/public/bootstrap']);
  const editable = can('platform.feature_flag.update');
  const toggle = (f: Schemas['FeatureFlag']) => {
    setEdit(null);
    void request('PUT', `/api/v1/platform/feature-flags/${f.key}`, { value: !(JSON.parse(JSON.stringify(f.value)) === true) }).then(() => { saved(); void flags.refetch(); });
  };
  return (
    <div className="oc-stack">
      <PageHeader title={title} help={t('help.featureConfiguration')} actions={editable &&
        <button className="oc-btn oc-btn-primary" onClick={() => setEdit({ key: '', value: 'false', description: '', valueType: 'boolean', clientVisible: true })}>
          <Icon name="add" size={18} /> Add Flag</button>} />
      <div className="oc-card">
        <DataTable rows={flags.data?.items as unknown as Record<string, unknown>[]} loading={flags.isLoading} rowKey={(r) => String(r.key)}
          columns={[
            { key: 'key', header: 'Flag', render: (f) => <div><code className="oc-code">{String(f.key)}</code><div className="oc-small oc-muted">{String(f.description)}</div></div> },
            { key: 'value', header: 'Value', render: (f) => (f.valueType === 'boolean' ? <StatusPill status={f.value === true ? 'active' : 'inactive'} label={f.value === true ? 'On' : 'Off'} /> : <code className="oc-code">{JSON.stringify(f.value)}</code>) },
            { key: 'clientVisible', header: 'Apps', render: (f) => (f.clientVisible ? 'Visible' : 'Server only') },
          ]}
          actions={(f) => editable && (f.valueType === 'boolean'
            ? <button className="oc-btn oc-btn-outline oc-btn-sm" onClick={() => toggle(f as unknown as Schemas['FeatureFlag'])}>{f.value === true ? 'Turn off' : 'Turn on'}</button>
            : <button className="oc-btn oc-btn-outline oc-btn-sm" onClick={() => setEdit({ key: String(f.key), value: JSON.stringify(f.value), description: String(f.description), valueType: String(f.valueType), clientVisible: Boolean(f.clientVisible) })}>Edit</button>)} />
      </div>
      <Modal open={!!edit} onClose={() => setEdit(null)} title={edit?.key ? `Edit ${edit.key}` : 'Add Flag'} actions={<>
        <button className="oc-btn oc-btn-neutral" onClick={() => setEdit(null)}>Cancel</button>
        <button className="oc-btn oc-btn-ink" disabled={!edit?.key} onClick={() => {
          let value: unknown;
          try { value = JSON.parse(edit!.value); } catch { value = edit!.value; }
          put.mutate({ value, description: edit!.description, valueType: edit!.valueType, clientVisible: edit!.clientVisible }, { onSuccess: () => { setEdit(null); saved(); } });
        }}>Save</button></>}>
        {edit && <div className="oc-form">
          <TextField label="Key" value={edit.key} onChange={(k) => setEdit({ ...edit, key: k })} placeholder="module.feature_name" />
          <SelectField label="Type" value={edit.valueType} onChange={(x) => setEdit({ ...edit, valueType: x })}
            options={['boolean', 'string', 'number', 'json'].map((x) => ({ value: x, label: x }))} />
          <TextField label="Value (JSON)" value={edit.value} onChange={(x) => setEdit({ ...edit, value: x })} span />
          <TextField label="Description" value={edit.description} onChange={(x) => setEdit({ ...edit, description: x })} span />
          <Checkbox label="Readable by the apps" checked={edit.clientVisible} onChange={(x) => setEdit({ ...edit, clientVisible: x })} />
        </div>}
        <ErrorAlert error={put.error} />
      </Modal>
    </div>
  );
}

export function LocalizationPage() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const saved = useSavedToast();
  const inst = useGet<Schemas['Instance']>('/api/v1/platform/instance');
  const save = useSend<Record<string, unknown>>('PATCH', '/api/v1/platform/localization', ['/api/v1/platform/instance', '/api/v1/public/bootstrap']);
  const [v, setV] = useState({ defaultLocale: 'id', currency: 'IDR', timezone: 'Asia/Jakarta', defaultTheme: 'light' });
  useEffect(() => {
    if (inst.data) setV({ defaultLocale: inst.data.defaultLocale, currency: inst.data.currency, timezone: inst.data.timezone, defaultTheme: inst.data.defaultTheme });
  }, [inst.data]);
  const fe = fieldErrors(save.error);
  return (
    <div className="oc-stack">
      <PageHeader title="Localization" help={t('help.localization')} />
      <Card title="Defaults" icon="translate">
        <form className="oc-stack" onSubmit={(e) => { e.preventDefault(); save.mutate(v, { onSuccess: () => { saved(); void qc.invalidateQueries(); } }); }}>
          <div className="oc-form">
            <SelectField label="Default Language" value={v.defaultLocale} onChange={(x) => setV({ ...v, defaultLocale: x })}
              options={[{ value: 'id', label: 'Bahasa Indonesia' }, { value: 'en', label: 'English' }]} />
            <TextField label="Currency" value={v.currency} onChange={(x) => setV({ ...v, currency: x.toUpperCase() })} error={fe.currency} help="ISO 4217, e.g. IDR" />
            <SelectField label="Timezone" value={v.timezone} onChange={(x) => setV({ ...v, timezone: x })} error={fe.timezone}
              options={[...new Set([v.timezone, ...TIMEZONES])].map((z) => ({ value: z, label: z }))} />
            <SelectField label="Default Theme" value={v.defaultTheme} onChange={(x) => setV({ ...v, defaultTheme: x })}
              options={[{ value: 'light', label: 'Light' }, { value: 'dark', label: 'Dark' }]} />
          </div>
          <ErrorAlert error={Object.keys(fe).length ? null : save.error} />
          <div><button className="oc-btn oc-btn-ink" disabled={save.isPending}>Save</button></div>
        </form>
      </Card>
    </div>
  );
}

const PRESETS = ['lime', 'blue', 'violet', 'orange', 'rose'] as const;

function Upload({ label, onUploaded }: { label: string; onUploaded: (url: string) => void }) {
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  return (
    <label className="oc-btn oc-btn-outline oc-btn-sm" style={{ cursor: 'pointer' }}>
      <Icon name="upload" size={18} /> {busy ? 'Uploading…' : label}
      <input type="file" accept="image/png,image/jpeg,image/webp,image/svg+xml,image/x-icon" className="oc-sr" onChange={async (e) => {
        const f = e.target.files?.[0];
        if (!f) return;
        setBusy(true);
        setErr(null);
        const fd = new FormData();
        fd.append('file', f);
        try {
          const r = await request<{ url: string }>('POST', '/api/v1/platform/files', fd);
          onUploaded(r.url);
        } catch (x) {
          setErr(x);
        } finally {
          setBusy(false);
        }
      }} />
      {err ? <ErrorAlert error={err} /> : null}
    </label>
  );
}

/** Branding (FR-BRD-01..03): logo, favicon, login photo, colours, names. */
export function BrandingPage() {
  const { t } = useTranslation();
  const saved = useSavedToast();
  const qc = useQueryClient();
  const br = useGet<Schemas['Branding']>('/api/v1/public/branding');
  const save = useSend<Record<string, unknown>, Schemas['Branding']>('PATCH', '/api/v1/platform/branding', ['/api/v1/public/branding', '/api/v1/public/bootstrap']);
  const preview = useSend<{ primaryColor: string }, Schemas['AccentTokens']>('POST', '/api/v1/platform/branding/accent-preview');
  const [v, setV] = useState<Record<string, string>>({});
  useEffect(() => {
    if (br.data) setV({ appName: br.data.appName, emailSenderName: br.data.emailSenderName, logoUrl: br.data.logoUrl ?? '', faviconUrl: br.data.faviconUrl ?? '',
      loginImageUrl: br.data.loginImageUrl ?? '', primaryColor: br.data.primaryColor ?? '#0B6E4F', accent: br.data.accent });
  }, [br.data]);
  const fe = fieldErrors(save.error);
  const set = (k: string) => (x: string) => setV((s) => ({ ...s, [k]: x }));
  const submit = () => save.mutate(v, {
    onSuccess: (b) => { saved(); applyBranding(b as unknown as BrandingInfo); void qc.invalidateQueries(); },
  });
  if (!br.data) return <Skeleton />;
  return (
    <div className="oc-stack">
      <PageHeader title="Branding" help={t('help.branding')} actions={<button className="oc-btn oc-btn-ink" disabled={save.isPending} onClick={submit}>Save</button>} />
      <ErrorAlert error={Object.keys(fe).length ? null : save.error} />
      <div className="oc-grid-2">
        <Card title="Identity" icon="badge">
          <div className="oc-form">
            <TextField label="App Name" value={v.appName ?? ''} onChange={set('appName')} error={fe.appName} />
            <TextField label="E-mail Sender Name" value={v.emailSenderName ?? ''} onChange={set('emailSenderName')} error={fe.emailSenderName} />
          </div>
        </Card>
        <Card title="Images" icon="image">
          <div className="oc-stack">
            {([['logoUrl', 'Logo'], ['faviconUrl', 'Favicon'], ['loginImageUrl', 'Login Photo']] as const).map(([k, label]) => (
              <div key={k} className="oc-row-wrap">
                <div style={{ width: 64, height: 48, borderRadius: 12, background: 'var(--md-sys-color-surface-container)', overflow: 'hidden', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
                  {v[k] ? <img src={v[k]} alt="" style={{ maxWidth: '100%', maxHeight: '100%', objectFit: 'contain' }} /> : <Icon name="image" size={20} />}
                </div>
                <div style={{ flex: 1 }}><strong>{label}</strong>{fe[k] && <div className="oc-field-error">{fe[k]}</div>}</div>
                <Upload label="Upload" onUploaded={(url) => setV((s) => ({ ...s, [k]: url }))} />
                {v[k] && <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => setV((s) => ({ ...s, [k]: '' }))}>Remove</button>}
              </div>
            ))}
          </div>
        </Card>
        <Card title="Accent Colour" icon="palette">
          <div className="oc-stack">
            <div className="oc-row-wrap">
              {PRESETS.map((p) => (
                <button key={p} type="button" className="oc-chip" aria-pressed={v.accent === p} onClick={() => { setV((s) => ({ ...s, accent: p })); document.documentElement.dataset.accent = p; }}>
                  <span data-accent={p} style={{ width: 14, height: 14, borderRadius: 999, background: 'var(--md-sys-color-primary)', display: 'inline-block' }} /> {p}
                </button>
              ))}
              <button type="button" className="oc-chip" aria-pressed={v.accent === 'custom'} onClick={() => setV((s) => ({ ...s, accent: 'custom' }))}>Custom</button>
            </div>
            {v.accent === 'custom' && (
              <div className="oc-stack">
                <div className="oc-row-wrap">
                  <input type="color" aria-label="Brand colour" value={v.primaryColor || '#0B6E4F'} onChange={(e) => set('primaryColor')(e.target.value.toUpperCase())}
                    style={{ width: 52, height: 44, border: 0, background: 'transparent' }} />
                  <div style={{ width: 160 }}><TextField label="Brand colour" value={v.primaryColor ?? ''} onChange={set('primaryColor')} error={fe.primaryColor} /></div>
                  <button type="button" className="oc-btn oc-btn-neutral" style={{ alignSelf: 'flex-end' }} onClick={() => preview.mutate({ primaryColor: v.primaryColor })}>Check contrast</button>
                </div>
                {preview.data && (
                  <table className="oc-table"><tbody>
                    {preview.data.checks.map((c) => (
                      <tr key={c.pair}><td>{c.pair}</td><td className="oc-num">{c.ratio.toFixed(2)} : 1</td><td><StatusPill status={c.pass ? 'approved' : 'rejected'} label={c.pass ? 'WCAG AA' : 'Fails AA'} /></td></tr>
                    ))}
                    <tr><td>Generated primary</td><td colSpan={2}><span className="oc-code">{preview.data.light['--md-sys-color-primary']}</span> light ·{' '}
                      <span className="oc-code">{preview.data.dark['--md-sys-color-primary']}</span> dark</td></tr>
                  </tbody></table>
                )}
                <ErrorAlert error={preview.error} />
              </div>
            )}
          </div>
        </Card>
        <Card title="Preview" icon="visibility">
          <div className="oc-stack">
            <div className="oc-row-wrap"><button className="oc-btn oc-btn-primary">Primary</button><button className="oc-btn oc-btn-ink">Secondary</button>
              <button className="oc-btn oc-btn-neutral">Tertiary</button></div>
            <div className="oc-row-wrap"><StatusPill status="completed" /><StatusPill status="pending" /><StatusPill status="cancelled" /></div>
          </div>
        </Card>
      </div>
    </div>
  );
}

/** Custom Domain (FR-INS-06): register, add DNS TXT, verify; TLS automatic. */
export function CustomDomainPage() {
  const { t } = useTranslation();
  const saved = useSavedToast();
  const list = useGet<Page<Schemas['Domain']>>('/api/v1/platform/domains');
  const [open, setOpen] = useState(false);
  const [surface, setSurface] = useState('web');
  const [host, setHost] = useState('');
  const add = useSend('POST', '/api/v1/platform/domains', ['/api/v1/platform/domains']);
  const qc = useQueryClient();
  const action = async (path: string, method: 'POST' | 'DELETE') => {
    await request(method, path);
    await qc.invalidateQueries({ predicate: (q) => String(q.queryKey[0]).startsWith('/api/v1/platform/domains') });
    saved();
  };
  return (
    <div className="oc-stack">
      <PageHeader title="Custom Domain" help={t('help.customDomain')} actions={<button className="oc-btn oc-btn-primary" onClick={() => setOpen(true)}><Icon name="add" size={18} /> Add Domain</button>} />
      <div className="oc-card">
        <DataTable rows={list.data?.items as unknown as Record<string, unknown>[]} loading={list.isLoading}
          columns={[
            { key: 'hostname', header: 'Hostname', render: (d) => <strong>{String(d.hostname)}</strong> },
            { key: 'surface', header: 'Application' },
            { key: 'dns', header: 'DNS TXT record', render: (d) => <div className="oc-small"><code className="oc-code">{String(d.verificationRecordName)}</code><br /><code className="oc-code">{String(d.verificationRecordValue)}</code></div> },
            { key: 'status', header: 'Status', render: (d) => <StatusPill status={String(d.status)} /> },
            { key: 'lastError', header: '', render: (d) => <span className="oc-small oc-muted">{(d.lastError as string) ?? ''}</span> },
          ]}
          actions={(d) => <div className="oc-row">
            <button className="oc-btn oc-btn-outline oc-btn-sm" onClick={() => void action(`/api/v1/platform/domains/${d.id}:verify`, 'POST')}>Verify</button>
            <button className="oc-icon-btn" aria-label="Remove domain" onClick={() => void action(`/api/v1/platform/domains/${d.id}`, 'DELETE')}><Icon name="delete" size={18} /></button>
          </div>} />
      </div>
      <Modal open={open} onClose={() => setOpen(false)} title="Add Domain" actions={<>
        <button className="oc-btn oc-btn-neutral" onClick={() => setOpen(false)}>Cancel</button>
        <button className="oc-btn oc-btn-ink" onClick={() => add.mutate({ surface, hostname: host }, { onSuccess: () => { setOpen(false); setHost(''); saved(); } })}>Add</button></>}>
        <div className="oc-form">
          <SelectField label="Application" value={surface} onChange={setSurface} options={[
            { value: 'web', label: 'Website' }, { value: 'member', label: 'Member Portal' }, { value: 'dashboard', label: 'Staff App · Dashboard' },
            { value: 'cashier', label: 'Staff App · Cashier' }, { value: 'caddy', label: 'Staff App · Caddy Tablet' },
            { value: 'kitchen', label: 'Staff App · Kitchen Display' }, { value: 'presence', label: 'Staff App · Attendance Form' }, { value: 'api', label: 'API' }]} />
          <TextField label="Hostname" value={host} onChange={setHost} placeholder="booking.club.com" error={fieldErrors(add.error).hostname} />
        </div>
      </Modal>
    </div>
  );
}
