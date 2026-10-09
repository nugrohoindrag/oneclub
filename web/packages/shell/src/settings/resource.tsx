import React, { useEffect, useMemo, useState } from 'react';
import { download, qs, request, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate, formatDateTime, useTranslation } from '@oneclub/i18n';
import { useAuth } from '../context';
import {
  ConfirmDialog, DataTable, Drawer, ErrorAlert, FilterPills, Icon, MoneyField, PageHeader, SearchBox, SelectField, StatusPill, TextArea, TextField,
  Checkbox, fieldErrors, useDebounced, usePagedList, type Column, type Option,
} from '../components/ui';
import { useToast } from '../components/toast';

export type Row = Record<string, unknown> & { id: string };

export interface FieldDef {
  name: string;
  label: string;
  /** money: a decimal amount in Rupiah, formatted while typing (MoneyField) */
  type?: 'text' | 'email' | 'number' | 'decimal' | 'money' | 'textarea' | 'date' | 'datetime' | 'boolean' | 'select' | 'reference' | 'json' | 'list' | 'intlist' | 'time' | 'image';
  required?: boolean;
  options?: Option[];
  /** For type=reference: list endpoint and label key. */
  ref?: { path: string; label: (r: Row) => string; filter?: (r: Row) => boolean };
  createOnly?: boolean;
  help?: string;
  span?: boolean;
  placeholder?: string;
  default?: unknown;
}

export interface ResourceConfig {
  /** Resource key (e.g. commercial.product): photo uploads of image fields. */
  resourceKey?: string;
  title: string;
  help?: string;
  /** API collection path, e.g. /api/v1/platform/venues */
  path: string;
  /** Permission prefix, e.g. platform.venue */
  perm: string;
  singular: string;
  columns: Column<Row>[];
  fields: FieldDef[];
  statusOptions?: Option[];
  noDelete?: boolean;
  noEdit?: (r: Row) => string | undefined;
  rowActions?: (r: Row, refresh: () => void) => React.ReactNode;
  headerActions?: React.ReactNode;
  defaultFilter?: string;
}

export const statusCol: Column<Row> = { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> };
export const dateCol = (key: string, header: string): Column<Row> => ({ key, header, render: (r) => formatDate(r[key] as string) });
export const dateTimeCol = (key: string, header: string): Column<Row> => ({ key, header, render: (r) => formatDateTime(r[key] as string) });

function toInput(f: FieldDef, v: unknown): string | boolean {
  if (f.type === 'boolean') return Boolean(v);
  if (v === null || v === undefined) return '';
  if (f.type === 'list' || f.type === 'intlist') return Array.isArray(v) ? v.join(', ') : String(v);
  if (f.type === 'json') return typeof v === 'string' ? v : JSON.stringify(v, null, 2);
  if (f.type === 'datetime' && typeof v === 'string') return v.slice(0, 16);
  return String(v);
}

function fromInput(f: FieldDef, v: string | boolean): unknown {
  if (f.type === 'boolean') return v;
  if (v === '') return null;
  if (f.type === 'number') return Number(v);
  if (f.type === 'list') return String(v).split(',').map((s) => s.trim()).filter(Boolean);
  if (f.type === 'intlist') return String(v).split(',').map((s) => Number(s.trim())).filter((n) => !Number.isNaN(n));
  if (f.type === 'json') {
    try {
      return JSON.parse(String(v));
    } catch {
      return v;
    }
  }
  if (f.type === 'datetime') return new Date(v as string).toISOString();
  return v;
}

function RefSelect({ f, value, onChange, error }: { f: FieldDef; value: string; onChange: (v: string) => void; error?: string }) {
  const list = useGet<Page<Row>>(`${f.ref!.path}${f.ref!.path.includes('?') ? '&' : '?'}limit=500`);
  const options = (list.data?.items ?? []).filter((r) => (f.ref!.filter ? f.ref!.filter(r) : true)).map((r) => ({ value: r.id, label: f.ref!.label(r) }));
  return <SelectField label={f.label} value={value} onChange={onChange} options={options} required={f.required} error={error} help={f.help} span={f.span}
    placeholder={f.required ? 'Select…' : '—'} />;
}

/** Photo of a record (image field): upload, preview, remove. */
function ImageField({ f, resourceKey, value, onChange, error }: { f: FieldDef; resourceKey?: string; value: string; onChange: (v: string) => void; error?: string }) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  return (
    <div className="oc-field oc-span">
      <span className="oc-label">{f.label}</span>
      <div className="oc-row" style={{ alignItems: 'center' }}>
        <div className="oc-image-preview">{value ? <img src={value} alt="" /> : <Icon name="image" size={28} />}</div>
        <label className="oc-btn oc-btn-outline oc-btn-sm" style={{ cursor: busy ? 'wait' : 'pointer' }}>
          <Icon name="upload" size={18} /> {busy ? 'Uploading…' : value ? 'Change photo' : 'Upload photo'}
          <input type="file" accept="image/png,image/jpeg,image/webp" className="oc-sr" disabled={busy || !resourceKey} onChange={async (e) => {
            const file = e.target.files?.[0];
            e.target.value = '';
            if (!file) return;
            setBusy(true);
            setErr(null);
            const fd = new FormData();
            fd.append('file', file);
            try {
              onChange((await request<{ url: string }>('POST', `/api/v1/platform/images${qs({ resource: resourceKey })}`, fd)).url);
            } catch (x) {
              setErr(x);
            } finally {
              setBusy(false);
            }
          }} />
        </label>
        {value && <button type="button" className="oc-btn oc-btn-text oc-btn-sm" onClick={() => onChange('')}>Remove</button>}
      </div>
      {error && <span className="oc-field-error" role="alert">{error}</span>}
      <ErrorAlert error={err} />
    </div>
  );
}

/** Form for one resource (create or edit). */
export function ResourceForm({ cfg, row, onDone }: { cfg: ResourceConfig; row?: Row; onDone: () => void }) {
  const { t } = useTranslation();
  const toast = useToast();
  const editing = !!row;
  const [values, setValues] = useState<Record<string, string | boolean>>(() => {
    const out: Record<string, string | boolean> = {};
    for (const f of cfg.fields) out[f.name] = toInput(f, row ? row[f.name] : f.default);
    return out;
  });
  const save = useSend<Record<string, unknown>, Row>(editing ? 'PATCH' : 'POST', editing ? `${cfg.path}/${row!.id}` : cfg.path, [cfg.path]);
  const fe = fieldErrors(save.error);
  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const body: Record<string, unknown> = {};
    for (const f of cfg.fields) {
      if (editing && f.createOnly) continue;
      const v = fromInput(f, values[f.name]);
      if (editing && JSON.stringify(v ?? null) === JSON.stringify(row![f.name] ?? null)) continue;
      if (!editing && (v === null || v === '')) continue;
      body[f.name] = v;
    }
    save.mutate(body, { onSuccess: () => { toast(t('common.saved')); onDone(); } });
  };
  const set = (k: string) => (v: string | boolean) => setValues((x) => ({ ...x, [k]: v }));
  return (
    <form className="oc-stack" onSubmit={submit}>
      <div className="oc-form">
        {cfg.fields.map((f) => {
          const disabled = editing && f.createOnly;
          const common = { label: f.label, required: f.required, error: fe[f.name], help: f.help, span: f.span };
          switch (f.type) {
            case 'json':
            case 'textarea':
              return <TextArea key={f.name} {...common} span value={String(values[f.name])} onChange={set(f.name)} />;
            case 'boolean':
              return <div key={f.name} className="oc-field"><span className="oc-label">&nbsp;</span><Checkbox label={f.label} checked={Boolean(values[f.name])} onChange={set(f.name)} /></div>;
            case 'select':
              return <SelectField key={f.name} {...common} value={String(values[f.name])} onChange={set(f.name)} options={f.options ?? []}
                placeholder={f.required ? undefined : '—'} />;
            case 'reference':
              return <RefSelect key={f.name} f={f} value={String(values[f.name])} onChange={set(f.name)} error={fe[f.name]} />;
            case 'image':
              return <ImageField key={f.name} f={f} resourceKey={cfg.resourceKey} value={String(values[f.name])} onChange={set(f.name)} error={fe[f.name]} />;
            case 'money':
              return <MoneyField key={f.name} {...common} disabled={disabled} placeholder={f.placeholder} value={String(values[f.name] ?? '')} onChange={set(f.name)} />;
            default:
              return <TextField key={f.name} {...common} disabled={disabled} placeholder={f.placeholder}
                type={f.type === 'email' ? 'email' : f.type === 'number' ? 'number' : f.type === 'date' ? 'date' : f.type === 'datetime' ? 'datetime-local' : 'text'}
                inputMode={f.type === 'decimal' ? 'decimal' : undefined}
                value={String(values[f.name])} onChange={set(f.name)} />;
          }
        })}
      </div>
      {Object.keys(fe).length === 0 && <ErrorAlert error={save.error} />}
      <div className="oc-row">
        <span className="oc-spacer" />
        <button type="button" className="oc-btn oc-btn-neutral" onClick={onDone}>Cancel</button>
        <button className="oc-btn oc-btn-ink" disabled={save.isPending}>{editing ? 'Save' : `Add ${cfg.singular}`}</button>
      </div>
    </form>
  );
}

/** Generic master data page (list, filters, add/edit, delete-or-archive, export). */
export function ResourcePage({ cfg }: { cfg: ResourceConfig }) {
  const { t } = useTranslation();
  const { can, propertyId } = useAuth();
  const toast = useToast();
  const [q, setQ] = useState('');
  const [status, setStatus] = useState(cfg.defaultFilter ?? '');
  const [editing, setEditing] = useState<Row | 'new' | null>(null);
  const [deleting, setDeleting] = useState<Row | null>(null);
  const query = useDebounced(q);
  const path = `${cfg.path}${qs({ q: query, 'filter[status]': status })}`;
  const list = usePagedList<Row>(path);
  const del = useSend<unknown>('DELETE', () => `${cfg.path}/${deleting?.id}`, [cfg.path]);
  useEffect(() => setEditing(null), [propertyId]);
  const statusOptions = useMemo(() => [{ value: '', label: 'All' }, ...(cfg.statusOptions ?? [{ value: 'active', label: 'Active' }, { value: 'inactive', label: 'Inactive' }])], [cfg.statusOptions]);
  const canCreate = can(`${cfg.perm}.create`);
  const canUpdate = can(`${cfg.perm}.update`);
  const canDelete = !cfg.noDelete && can(`${cfg.perm}.delete`);
  const canExport = can(`${cfg.perm}.export`);
  return (
    <div className="oc-stack">
      <PageHeader title={cfg.title} help={cfg.help} actions={<>
        {cfg.headerActions}
        {canExport && <>
          <button className="oc-btn oc-btn-outline" onClick={() => download('GET', `${cfg.path}:export${qs({ format: 'csv', q: query, 'filter[status]': status })}`, undefined, `${cfg.title}.csv`)}>
            <Icon name="download" size={18} /> Export CSV</button>
          <button className="oc-btn oc-btn-outline" onClick={() => download('GET', `${cfg.path}:export${qs({ format: 'xlsx', q: query, 'filter[status]': status })}`, undefined, `${cfg.title}.xlsx`)}>
            XLSX</button>
        </>}
        {canCreate && <button className="oc-btn oc-btn-primary" onClick={() => setEditing('new')}><Icon name="add" size={18} /> Add {cfg.singular}</button>}
      </>} />
      <div className="oc-card">
        <div className="oc-toolbar">
          <SearchBox value={q} onChange={setQ} />
          <span className="oc-spacer" />
          <FilterPills options={statusOptions} value={status} onChange={setStatus} />
        </div>
        <DataTable columns={cfg.columns} rows={list.rows} loading={list.isLoading} error={list.error} server={list.pager}
          onRowClick={canUpdate ? (r) => setEditing(r) : undefined}
          actions={(r) => (
            <div className="oc-row" style={{ justifyContent: 'flex-end' }}>
              {cfg.rowActions?.(r, () => list.refetch())}
              {canUpdate && <button className="oc-icon-btn" aria-label={`Edit ${cfg.singular}`} onClick={() => setEditing(r)}><Icon name="edit" size={18} /></button>}
              {canDelete && <button className="oc-icon-btn" aria-label={`Delete ${cfg.singular}`} onClick={() => setDeleting(r)}><Icon name="delete" size={18} /></button>}
            </div>
          )} />
      </div>
      <Drawer open={editing !== null} onClose={() => setEditing(null)} title={editing === 'new' ? `Add ${cfg.singular}` : `Edit ${cfg.singular}`}>
        {editing !== null && (
          cfg.noEdit && editing !== 'new' && cfg.noEdit(editing) ? <div className="oc-alert oc-alert-info">{cfg.noEdit(editing)}</div> :
            <ResourceForm key={editing === 'new' ? 'new' : editing.id} cfg={cfg} row={editing === 'new' ? undefined : editing} onDone={() => setEditing(null)} />
        )}
      </Drawer>
      <ConfirmDialog open={!!deleting} onClose={() => { setDeleting(null); del.reset(); }} danger confirmLabel="Delete" busy={del.isPending} error={del.error}
        title={`Delete ${cfg.singular}`} message={t('common.confirmDelete', { name: String(deleting?.name ?? deleting?.code ?? '') })}
        onConfirm={() => del.mutate(undefined, { onSuccess: () => { toast(t('common.deleted')); setDeleting(null); } })} />
    </div>
  );
}
