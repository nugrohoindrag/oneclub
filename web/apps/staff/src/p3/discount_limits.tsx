import React, { useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router';
import { useGet, useSend, type Page } from '@oneclub/api-client';
import { Card, DataTable, ErrorAlert, PageHeader, Skeleton, StatusPill, TextField, useAuth, useToast } from '@oneclub/shell';
import type { R } from '../p1/common';

// Discount Limits (PO decision 4b, PRD P3 §16 #5, FR-POL-P3-02): the manual
// discount limit per role of the Pricing Policies — one table for the POS
// (above the limit: supervisor override, commercial.pos.discount_override)
// and the quotations (above the limit: discount approval). Saving adds a new
// version of the Pricing Policies (versioned and audited by Club Policies).

const POLICY = 'commercial.pricing';
const POLICIES = '/api/v1/platform/club-policies';
const CATALOG = `${POLICIES}/catalog`;

type Entry = { code: string; category: string; name: string; default: Record<string, unknown>; inForce: Record<string, unknown> | null; version: number;
  propertyScoped: boolean };
type Role = R & { code: string; name: string; category?: string; status?: string; scope?: string };

/** Value of the policy: a number 0–100 or empty (the role is not listed). */
export function limitError(v: string): string | undefined {
  if (v.trim() === '') return undefined;
  if (!/^\d{1,3}(\.\d{1,2})?$/.test(v.trim())) return 'Enter a percentage, e.g. 5 or 12.5';
  const n = Number(v);
  return n < 0 || n > 100 ? 'From 0 to 100' : undefined;
}

export function DiscountLimitsPage() {
  const { can, propertyId } = useAuth();
  const toast = useToast();
  const catalog = useGet<Page<Entry>>(CATALOG);
  const roles = useGet<Page<Role>>('/api/v1/platform/roles?limit=200');
  const entry = catalog.data?.items.find((e) => e.code === POLICY);
  const fallback = (code: string) => {
    const e = catalog.data?.items.find((x) => x.code === code);
    return String((e?.inForce ?? e?.default ?? {}).maxDiscountPercent ?? '—');
  };
  const current = useMemo(() => {
    const v = (entry?.inForce ?? entry?.default ?? {}) as Record<string, unknown>;
    // a version without manualDiscountLimits keeps the default tiers
    return (v.manualDiscountLimits ?? (entry?.default ?? {}).manualDiscountLimits ?? {}) as Record<string, string>;
  }, [entry]);
  const [limits, setLimits] = useState<Record<string, string>>({});
  const [dirty, setDirty] = useState(false);
  useEffect(() => { if (!dirty) setLimits({ ...current }); }, [current, dirty]);
  const save = useSend<R, R>('POST', POLICIES, [POLICIES, CATALOG]);
  const fe = save.error?.fieldErrors ?? {};
  const canEdit = can('platform.club_policy.manage');

  const roleList = useMemo(() => {
    const list = (roles.data?.items ?? []).filter((r) => r.status !== 'inactive' && r.scope !== 'platform');
    // roles listed in the policy but missing from the catalogue stay visible
    for (const code of Object.keys(limits)) if (!list.some((r) => r.code === code)) list.push({ id: code, code, name: code });
    return list.sort((a, b) => a.name.localeCompare(b.name));
  }, [roles.data, limits]);
  const errors = Object.fromEntries(Object.entries(limits).map(([k, v]) => [k, limitError(v) ?? fe[`value.manualDiscountLimits.${k}`]]));
  const invalid = Object.values(limits).some((v) => limitError(v));

  const submit = () => {
    if (!entry) return;
    const manualDiscountLimits: Record<string, string> = {};
    for (const [k, v] of Object.entries(limits)) if (v.trim() !== '') manualDiscountLimits[k] = String(Number(v.trim()));
    const value = { ...(entry.inForce ?? entry.default), manualDiscountLimits };
    save.mutate({ category: entry.category, code: entry.code, name: entry.name, value,
      ...(entry.propertyScoped && propertyId ? { propertyId } : {}) } as unknown as R,
    { onSuccess: () => { setDirty(false); toast('Discount limits saved as a new Pricing Policies version'); } });
  };

  if (catalog.isLoading || roles.isLoading) return <Skeleton rows={8} />;
  return (
    <div className="oc-stack">
      <PageHeader title="Discount Limits"
        help="Maximum manual discount per role without approval (Pricing Policies). Above the limit the POS needs a supervisor override and a quotation needs approval."
        actions={<Link className="oc-btn oc-btn-neutral" to="/settings/club-policies">Club Policies</Link>} />
      <ErrorAlert error={catalog.error ?? roles.error} />
      <Card title="Manual discount limit per role" icon="percent"
        actions={entry && <StatusPill status="active" label={entry.version ? `Version ${entry.version}${entry.propertyScoped ? ' · this property' : ''}` : 'Default'} />}>
        <p className="oc-small oc-muted">
          A user with several roles gets the highest limit. Roles left empty use the POS Policies limit at the POS ({fallback('pos.policy')}%) and
          the Sales Policies limit for quotations ({fallback('crm.sales')}%).
        </p>
        <DataTable rows={roleList as unknown as Record<string, unknown>[]} empty="No roles"
          columns={[
            { key: 'name', header: 'Role', render: (r) => <span>{String(r.name)} <code className="oc-code">{String(r.code)}</code></span> },
            { key: 'limit', header: 'Max discount without approval (%)', render: (r) => {
              const code = String(r.code);
              return <TextField label={`Limit for ${String(r.name)}`} type="number" min={0} max={100} step="0.5" inputMode="decimal"
                value={limits[code] ?? ''} placeholder="Not listed" disabled={!canEdit} error={errors[code]}
                onChange={(v) => { setDirty(true); setLimits((x) => ({ ...x, [code]: v })); }} />;
            } },
            { key: 'above', header: 'Above the limit', render: (r) => (limits[String(r.code)] ?? '') === ''
              ? <span className="oc-small oc-muted">POS / Sales Policies limit applies</span>
              : <span className="oc-small">POS: supervisor override · Quotation: approval</span> },
          ]} />
        <ErrorAlert error={Object.keys(fe).length ? null : save.error} />
        {canEdit && <div className="oc-row-wrap">
          <button className="oc-btn oc-btn-neutral" disabled={!entry}
            onClick={() => { setDirty(true); setLimits({ ...((entry?.default ?? {}).manualDiscountLimits as Record<string, string> ?? {}) }); }}>
            Reset to defaults</button>
          <button className="oc-btn oc-btn-neutral" disabled={!dirty} onClick={() => { setDirty(false); setLimits({ ...current }); }}>Discard changes</button>
          <span className="oc-spacer" />
          <button className="oc-btn oc-btn-primary" disabled={!dirty || invalid || save.isPending || !entry} onClick={submit}>Save new version</button>
        </div>}
      </Card>
    </div>
  );
}
