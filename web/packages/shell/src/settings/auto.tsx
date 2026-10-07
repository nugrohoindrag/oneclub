import React from 'react';
import { Link } from 'react-router';
import { useGet, type Page, type Schemas } from '@oneclub/api-client';
import { formatDate, formatDateTime } from '@oneclub/i18n';
import { Card, Icon, Skeleton, StatusPill, Empty, type Column } from '../components/ui';
import { ResourcePage, type FieldDef, type ResourceConfig, type Row } from './resource';

type DefMeta = Schemas['DefMeta'];

/** Resource definitions the user may view (GET /platform/resource-definitions). */
export function useResourceDefs() {
  return useGet<Page<DefMeta>>('/api/v1/platform/resource-definitions', { staleTime: 5 * 60_000 });
}

const labelOf = (r: Row) => String(r.name ?? r.title ?? r.code ?? r.id) + (r.code && r.name ? ` (${String(r.code)})` : '');

/** Builds a ResourceConfig from server metadata. */
export function configFromMeta(d: DefMeta): ResourceConfig {
  const fields: FieldDef[] = d.fields
    .filter((f) => !f.readOnly && f.name !== 'attributes')
    .map((f) => ({
      name: f.name, label: f.label, required: f.required, createOnly: f.createOnly, default: f.default ?? undefined,
      type: (f.type === 'jsonlist' ? 'json' : f.type) as FieldDef['type'],
      options: f.options?.map((o) => ({ value: o, label: o.replace(/_/g, ' ') })),
      ref: f.type === 'reference' && f.refPath ? { path: f.refPath, label: labelOf } : undefined,
      span: ['textarea', 'json', 'jsonlist'].includes(f.type),
    }))
    .map((f) => (f.type === 'reference' && !f.ref ? { ...f, type: 'text' as const } : f));
  const shown = d.fields.filter((f) => !['json', 'jsonlist', 'textarea', 'reference'].includes(f.type) && f.name !== 'attributes').slice(0, 7);
  const columns: Column<Row>[] = shown.map((f) => ({
    key: f.name, header: f.label,
    render: (r: Row) => {
      const v = r[f.name];
      if (v === null || v === undefined || v === '') return '—';
      if (f.name === 'status' || f.name === 'readiness' || f.name === 'dutyStatus') return <StatusPill status={String(v)} />;
      if (f.type === 'image') return <img className="oc-image-thumb" src={String(v)} alt="" />;
      if (f.type === 'boolean') return v ? 'Yes' : 'No';
      if (f.type === 'date') return formatDate(String(v));
      if (f.type === 'datetime') return formatDateTime(String(v));
      if (Array.isArray(v)) return v.join(', ');
      return String(v);
    },
  }));
  const status = d.fields.find((f) => f.name === 'status');
  return {
    resourceKey: d.key, title: d.plural, singular: d.name, path: d.path, perm: d.perm, fields, columns, noDelete: d.noDelete,
    statusOptions: status?.options?.map((o) => ({ value: o, label: o.replace(/_/g, ' ') })),
  };
}

/** Master data screen generated from the resource definition. */
export function AutoResourcePage({ resourceKey }: { resourceKey: string }) {
  const defs = useResourceDefs();
  if (defs.isLoading) return <Skeleton rows={8} />;
  const d = defs.data?.items.find((x) => x.key === resourceKey);
  if (!d) return <Empty title="Not available" help="You do not have access to this list, or the module is not enabled." icon="lock" />;
  return <ResourcePage key={d.key} cfg={configFromMeta(d)} />;
}

/** Cards linking to the master data resources of module tags, or of the
 * listed resource keys only. */
export function ResourceIndex({ modules = [], only, base }: { modules?: string[]; only?: string[]; base: string }) {
  const defs = useResourceDefs();
  const items = (defs.data?.items ?? []).filter((d) => (only ? only.includes(d.key) : modules.includes(d.module)));
  if (defs.isLoading) return <Skeleton />;
  if (items.length === 0) return null;
  return (
    <Card title="Master data" icon="dataset">
      <div className="oc-grid">
        {items.map((d) => (
          <Link key={d.key} to={`${base}/${d.key}`} className="oc-card" style={{ textDecoration: 'none' }}>
            <div className="oc-card-head" style={{ marginBottom: 0 }}><span className="oc-icon-circle"><Icon name="table_rows" size={20} /></span><h3>{d.plural}</h3></div>
          </Link>
        ))}
      </div>
    </Card>
  );
}

export type { DefMeta };
export const _auto = React;
