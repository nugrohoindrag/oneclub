import React, { useEffect, useState } from 'react';
import { qs, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate, formatDateTime, formatNumber } from '@oneclub/i18n';
import { Card, Checkbox, DataTable, Drawer, ErrorAlert, Icon, Modal, MoneyField, SelectField, Skeleton, StatusPill, TextArea, TextField, useAuth, useToast } from '@oneclub/shell';
import { ActionButton, KV, ListPage, money, type R } from '../p1/common';

// Member tier classes & classification (PRD P5 EP-18 §16 #14, product owner request "on member add tier class and with classification
// and CRUD"): CRM → Loyalty → Tiers (tier master with badge, qualification rules, evaluation settings, threshold versions and
// "re-evaluate now" with its preview), manual classifications, and the tier badge used on members, Customer 360, the POS / front desk
// customer pickers and the golf booking member lookup. The POS reads the tier F&B discount of its customer (cached for offline sales).

const CRM = ['/api/v1/crm'];
const day = (v: unknown) => (v ? formatDate(String(v)) : '—');
const num = (v: unknown) => formatNumber(Number(v ?? 0));
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const ICONS = ['workspace_premium', 'military_tech', 'star', 'diamond', 'emoji_events', 'verified', 'grade', 'local_activity'];

/** The tier fields of a row (benefits, member tier, customer tier, tier class). */
export type TierLike = Record<string, unknown>;

/** Tier class badge: icon in the tier colour, name, manual classification marker. */
export function TierBadge({ t, compact }: { t: TierLike | null | undefined; compact?: boolean }) {
  const name = t?.tierName ?? t?.name;
  if (!t || !name) return compact ? <span className="oc-muted">—</span> : <span className="oc-chip">No tier</span>;
  const color = String(t.tierColor ?? t.color ?? '#6B7280');
  const manual = t.tierSource === 'manual';
  const until = t.overrideUntil ? ` until ${day(t.overrideUntil)}` : '';
  return (
    <span className="oc-chip" role="img" aria-label={`Tier ${String(name)}${manual ? `, classified manually${until}` : ''}`}
      title={manual ? `Classified manually${until}` : 'Classified by the tier evaluation'}
      style={{ borderColor: color, borderLeftWidth: 4, display: 'inline-flex', alignItems: 'center', gap: 4, whiteSpace: 'nowrap' }}>
      <span style={{ color, display: 'inline-flex' }}><Icon name={String(t.tierIcon ?? t.icon ?? 'workspace_premium')} size={16} /></span>
      {String(name)}
      {manual && <span className="oc-small oc-muted">{compact ? '·M' : `· manual${until}`}</span>}
    </span>
  );
}

/** Tier badge of a customer (loyalty benefits; nothing without permission). */
export function CustomerTierBadge({ customerId, compact }: { customerId?: string | null; compact?: boolean }) {
  const { can } = useAuth();
  const b = useGet<R>(customerId && can('crm.loyalty_account.view') ? `/api/v1/crm/loyalty/tier-benefits?customerId=${customerId}` : null);
  if (!b.data) return null;
  if (!b.data.enrolled) return compact ? null : <span className="oc-small oc-muted">Not a loyalty member</span>;
  return <TierBadge t={b.data} compact={compact} />;
}

/** Tier badge of a member number (golf booking member lookup). */
export function MemberNoTierBadge({ memberNo }: { memberNo: string }) {
  const { can } = useAuth();
  const no = memberNo.trim();
  const m = useGet<Page<R>>(no.length >= 2 && can('crm.loyalty_account.view') ? `/api/v1/crm/loyalty/member-tiers${qs({ 'filter[memberNo]': no, limit: 1 })}` : null);
  const x = m.data?.items[0];
  if (!x) return null;
  return <span style={{ alignSelf: 'flex-end' }}><span className="oc-small">{String(x.memberName)} </span><TierBadge t={x} compact /></span>;
}

/** Tier names of customers for list labels (customer pickers). */
export function useCustomerTiers(ids: string[]): Map<string, R> {
  const { can } = useAuth();
  const list = ids.filter(Boolean).slice(0, 200);
  const q = useGet<Page<R>>(list.length && can('crm.loyalty_account.view') ? `/api/v1/crm/loyalty/customer-tiers?customerIds=${list.join(',')}` : null);
  return new Map((q.data?.items ?? []).map((t) => [String(t.customerId), t]));
}

/** Tier filter for member lists (tier id, none, or all). */
export function TierFilter({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const tiers = useGet<Page<R>>('/api/v1/crm/loyalty/tier-classes');
  return (
    <div style={{ width: 200 }}>
      <SelectField label="Tier" value={value} onChange={onChange} placeholder="All tiers"
        options={[{ value: '', label: 'All tiers' }, ...(tiers.data?.items ?? []).map((t) => ({ value: String(t.id), label: String(t.name) })), { value: 'none', label: 'No tier' }]} />
    </div>
  );
}

// ── Tier master (CRM → Loyalty → Tiers) ───────────────────────────────────

type Form = Record<string, string | boolean | string[]>;
const EMPTY: Form = {
  code: '', name: '', rank: '', color: '#6B7280', icon: 'workspace_premium', qualifyMode: 'all', minSpend: '0', minPoints: '0', periodMonths: '', membershipTypeIds: [],
  multiplier: '1', bookingWindowDays: '0', fnbDiscountPercent: '0', eventAccess: false, priorityService: false, benefits: '', graceMonths: '', downgradeAllowed: true, status: 'active',
};

function toForm(t?: R): Form {
  if (!t) return { ...EMPTY };
  const s = (k: string) => (t[k] === null || t[k] === undefined ? '' : String(t[k]));
  return {
    code: s('code'), name: s('name'), rank: s('rank'), color: s('color') || '#6B7280', icon: s('icon') || 'workspace_premium', qualifyMode: s('qualifyMode') || 'all',
    minSpend: s('minSpend') || '0', minPoints: s('minPoints') || '0', periodMonths: s('periodMonths'), membershipTypeIds: (t.membershipTypeIds as string[] | undefined) ?? [],
    multiplier: s('multiplier') || '1', bookingWindowDays: s('bookingWindowDays') || '0', fnbDiscountPercent: s('fnbDiscountPercent') || '0', eventAccess: !!t.eventAccess,
    priorityService: !!t.priorityService, benefits: s('benefits'), graceMonths: s('graceMonths'), downgradeAllowed: t.downgradeAllowed !== false, status: s('status') || 'active',
  };
}

/** The request body of the tier master (Go resource fields; empty optional numbers = null). */
function toBody(f: Form, creating: boolean): Record<string, unknown> {
  const intOrNull = (v: unknown) => (String(v).trim() === '' ? null : Number(v));
  const body: Record<string, unknown> = {
    name: f.name, rank: Number(f.rank), color: f.color, icon: f.icon, qualifyMode: f.qualifyMode, minSpend: String(f.minSpend || '0'),
    minPoints: Number(f.minPoints || 0), periodMonths: intOrNull(f.periodMonths), membershipTypeIds: f.membershipTypeIds, multiplier: String(f.multiplier || '1'),
    bookingWindowDays: Number(f.bookingWindowDays || 0), fnbDiscountPercent: String(f.fnbDiscountPercent || '0'), eventAccess: f.eventAccess, priorityService: f.priorityService,
    benefits: f.benefits, graceMonths: intOrNull(f.graceMonths), downgradeAllowed: f.downgradeAllowed, status: f.status,
  };
  if (creating) body.code = f.code;
  return body;
}

function TierForm({ tier, onClose }: { tier?: R; onClose: () => void }) {
  const toast = useToast();
  const [f, setF] = useState<Form>(toForm(tier));
  const types = useGet<Page<R>>('/api/v1/membership/types?filter[status]=active&limit=100');
  const send = useSend<Record<string, unknown>, R>(tier ? 'PATCH' : 'POST', tier ? `/api/v1/crm/loyalty/tiers/${tier.id}` : '/api/v1/crm/loyalty/tiers', CRM);
  const set = (k: string) => (v: string | boolean) => setF({ ...f, [k]: v });
  const sel = (f.membershipTypeIds as string[]) ?? [];
  const fieldErr = (k: string) => ((send.error as { problem?: { errors?: { field: string; message: string }[] } } | null)?.problem?.errors ?? []).find((e) => e.field === k)?.message;
  return (
    <Modal open wide onClose={onClose} title={tier ? `Edit tier ${String(tier.name)}` : 'New tier'}
      actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
        <button className="oc-btn oc-btn-primary" disabled={!f.code || !f.name || !f.rank || send.isPending}
          onClick={() => send.mutate(toBody(f, !tier), { onSuccess: (r) => { toast(r.pendingThresholds ? 'Saved: new thresholds apply at the next evaluation' : 'Tier saved'); onClose(); } })}>Save</button></>}>
      <ErrorAlert error={send.error} />
      <div className="oc-form">
        <h3 style={{ gridColumn: '1 / -1', margin: 0 }}>Class</h3>
        <TextField label="Code" value={String(f.code)} onChange={set('code')} required disabled={!!tier} />
        <TextField label="Name" value={String(f.name)} onChange={set('name')} required />
        <TextField label="Classification rank (1 = lowest)" type="number" min={1} value={String(f.rank)} onChange={set('rank')} required error={fieldErr('rank')} />
        <TextField label="Badge colour" type="color" value={String(f.color)} onChange={set('color')} />
        <SelectField label="Badge icon" value={String(f.icon)} onChange={set('icon')} options={ICONS.map((i) => ({ value: i, label: label(i) }))} />
        <div style={{ alignSelf: 'flex-end' }}><TierBadge t={{ name: f.name || 'Preview', color: f.color, icon: f.icon }} /></div>
        <h3 style={{ gridColumn: '1 / -1', margin: 0 }}>Qualification (versioned: a change applies at the next evaluation)</h3>
        <SelectField label="Thresholds" value={String(f.qualifyMode)} onChange={set('qualifyMode')}
          options={[{ value: 'all', label: 'Spend and points (all set thresholds)' }, { value: 'any', label: 'Spend or points (any threshold)' }]} />
        <MoneyField label="Minimum spend (window)" value={String(f.minSpend)} onChange={set('minSpend')} error={fieldErr('minSpend')} />
        <TextField label="Minimum points (window)" type="number" min={0} value={String(f.minPoints)} onChange={set('minPoints')} />
        <TextField label="Window (months; empty = programme)" type="number" min={1} max={60} value={String(f.periodMonths)} onChange={set('periodMonths')} />
        <fieldset style={{ gridColumn: '1 / -1', border: 0, padding: 0 }}>
          <legend className="oc-small">Membership types (none = any customer)</legend>
          <div className="oc-row-wrap">
            {(types.data?.items ?? []).map((t) => (
              <Checkbox key={t.id} label={String(t.name)} checked={sel.includes(t.id)}
                onChange={(on) => setF({ ...f, membershipTypeIds: on ? [...sel, t.id] : sel.filter((x) => x !== t.id) })} />
            ))}
          </div>
        </fieldset>
        <h3 style={{ gridColumn: '1 / -1', margin: 0 }}>Benefits</h3>
        <TextField label="Points multiplier" inputMode="decimal" value={String(f.multiplier)} onChange={set('multiplier')} />
        <TextField label="Booking window + days" type="number" min={0} max={60} value={String(f.bookingWindowDays)} onChange={set('bookingWindowDays')} />
        <TextField label="F&B discount (%)" inputMode="decimal" value={String(f.fnbDiscountPercent)} onChange={set('fnbDiscountPercent')} />
        <Checkbox label="VIP event access" checked={!!f.eventAccess} onChange={set('eventAccess')} />
        <Checkbox label="Priority service" checked={!!f.priorityService} onChange={set('priorityService')} />
        <TextArea label="Other perks" value={String(f.benefits)} onChange={set('benefits')} span />
        <h3 style={{ gridColumn: '1 / -1', margin: 0 }}>Evaluation</h3>
        <TextField label="Grace before downgrade (months; empty = programme)" type="number" min={0} max={24} value={String(f.graceMonths)} onChange={set('graceMonths')} />
        <Checkbox label="Downgrade allowed" checked={!!f.downgradeAllowed} onChange={set('downgradeAllowed')} />
        <SelectField label="Status" value={String(f.status)} onChange={set('status')} options={[{ value: 'active', label: 'Active' }, { value: 'inactive', label: 'Inactive' }]} />
      </div>
    </Modal>
  );
}

function VersionsDrawer({ tier, onClose }: { tier: R; onClose: () => void }) {
  const v = useGet<Page<R>>(`/api/v1/crm/loyalty/tiers/${tier.id}/versions`);
  return (
    <Drawer open onClose={onClose} title={`Threshold versions · ${String(tier.name)}`}>
      {!v.data ? <Skeleton /> : <DataTable rows={v.data.items} columns={[{ key: 'version', header: 'Version', align: 'right' },
        { key: 'status', header: 'Status', render: (r) => <StatusPill status={r.status === 'effective' ? 'active' : r.status === 'pending' ? 'pending' : 'inactive'} label={label(r.status)} /> },
        { key: 'minSpend', header: 'Spend', align: 'right', render: (r) => money(r.minSpend) }, { key: 'minPoints', header: 'Points', align: 'right', render: (r) => num(r.minPoints) },
        { key: 'qualifyMode', header: 'Rule', render: (r) => (r.qualifyMode === 'any' ? 'any' : 'all') }, { key: 'periodMonths', header: 'Window', render: (r) => (r.periodMonths ? `${String(r.periodMonths)} mo` : 'programme') },
        { key: 'effectiveOn', header: 'In force since', render: (r) => day(r.effectiveOn) }, { key: 'createdByName', header: 'By', render: (r) => String(r.createdByName ?? '—') }]} />}
    </Drawer>
  );
}

/** Re-evaluate now: preview of the members moving up / down, then the run with the pending thresholds. */
function ReevaluateModal({ onClose }: { onClose: () => void }) {
  const toast = useToast();
  const [kind, setKind] = useState('annual');
  const p = useGet<R>(`/api/v1/crm/loyalty/tier-evaluations:preview${qs({ kind, pending: true })}`);
  const run = useSend<Record<string, unknown>, R>('POST', '/api/v1/crm/loyalty/tier-evaluations', CRM);
  const x = p.data;
  return (
    <Modal open wide onClose={onClose} title="Re-evaluate now"
      actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
        <button className="oc-btn oc-btn-primary" disabled={!x || run.isPending}
          onClick={() => run.mutate({ kind, applyPendingVersions: true }, { onSuccess: (r) => { toast(`Evaluation ${String(r.number)}: ${num(r.upgraded)} up, ${num(r.downgraded)} down`); onClose(); } })}>
          Apply and evaluate</button></>}>
      <ErrorAlert error={p.error ?? run.error} />
      <SelectField label="Evaluation" value={kind} onChange={setKind}
        options={[{ value: 'annual', label: 'Annual (with grace)' }, { value: 'periodic', label: 'Periodic (upgrades, ended grace)' }, { value: 'grace_review', label: 'Grace review' }]} />
      {!x ? <Skeleton /> : (
        <div className="oc-stack">
          <p className="oc-small">Preview with the pending thresholds{((x.pendingTiers as string[]) ?? []).length ? ` of ${((x.pendingTiers as string[]) ?? []).join(', ')}` : ' (none pending)'}; nothing changes until you apply.</p>
          <KV items={[['Members evaluated', num(x.accounts)], ['Move up', <strong key="u">{num(x.upgraded)}</strong>], ['Move down', <strong key="d">{num(x.downgraded)}</strong>],
            ['Grace starts', num(x.graceStarted)], ['Unchanged', num(Number(x.retained ?? 0) + Number(x.inGrace ?? 0))], ['Manual (locked)', num(x.locked)]]} />
          <DataTable rows={((x.moves as R[]) ?? []).map((m, i) => ({ ...m, id: String(i) }) as R)} columns={[{ key: 'fromTierName', header: 'From', render: (r) => String(r.fromTierName ?? 'No tier') },
            { key: 'toTierName', header: 'To', render: (r) => String(r.toTierName ?? 'No tier') }, { key: 'direction', header: 'Move', render: (r) => (r.direction === 'up' ? '▲ up' : '▼ down') },
            { key: 'accounts', header: 'Members', align: 'right' }]} />
          <DataTable rows={((x.lines as R[]) ?? []).slice(0, 100).map((l) => ({ ...l, id: String(l.accountId) }) as R)} columns={[{ key: 'customerName', header: 'Member' },
            { key: 'fromTierName', header: 'From', render: (r) => String(r.fromTierName ?? '—') }, { key: 'toTierName', header: 'To', render: (r) => String(r.toTierName ?? '—') },
            { key: 'outcome', header: 'Outcome', render: (r) => <StatusPill status={String(r.outcome)} label={label(r.outcome)} /> },
            { key: 'spend', header: 'Spend', align: 'right', render: (r) => money(r.spend) }]} />
        </div>
      )}
    </Modal>
  );
}

/** CRM → Loyalty → Tiers: tier classes with badge, thresholds in force, members; add / edit / delete-or-archive, versions, re-evaluate now. */
export function TierClassesPanel() {
  const { can } = useAuth();
  const toast = useToast();
  const [status, setStatus] = useState('active');
  const [edit, setEdit] = useState<R | null | 'new'>(null);
  const [versions, setVersions] = useState<R | null>(null);
  const [reeval, setReeval] = useState(false);
  const list = useGet<Page<R>>(`/api/v1/crm/loyalty/tier-classes${qs({ 'filter[status]': status })}`);
  const full = useGet<Page<R>>('/api/v1/crm/loyalty/tiers?limit=200');
  const byId = new Map((full.data?.items ?? []).map((t) => [String(t.id), t]));
  const pending = (list.data?.items ?? []).some((t) => t.pendingThresholds);
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <div style={{ width: 180 }}><SelectField label="Status" value={status} onChange={setStatus} options={[{ value: 'active', label: 'Active' }, { value: 'inactive', label: 'Inactive' }, { value: '', label: 'All' }]} /></div>
        <span className="oc-spacer" />
        {can('crm.loyalty_tier.update') && <button className={`oc-btn ${pending ? 'oc-btn-ink' : 'oc-btn-neutral'}`} onClick={() => setReeval(true)}>
          <Icon name="published_with_changes" size={18} /> Re-evaluate now{pending ? ' (thresholds pending)' : ''}</button>}
        {can('crm.loyalty_tier.create') && <button className="oc-btn oc-btn-primary" onClick={() => setEdit('new')}>New tier</button>}
      </div>
      <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error} onRowClick={(r) => can('crm.loyalty_tier.update') && setEdit(byId.get(r.id) ?? r)}
        columns={[{ key: 'rank', header: 'Rank', align: 'right' }, { key: 'name', header: 'Tier', render: (r) => <TierBadge t={r} /> }, { key: 'code', header: 'Code' },
          { key: 'minSpend', header: 'Spend threshold', align: 'right', render: (r) => money(r.minSpend) }, { key: 'minPoints', header: 'Points', align: 'right', render: (r) => num(r.minPoints) },
          { key: 'multiplier', header: 'Points ×', align: 'right' }, { key: 'fnbDiscountPercent', header: 'F&B disc.', align: 'right', render: (r) => `${String(r.fnbDiscountPercent)}%` },
          { key: 'bookingWindowDays', header: 'Booking +days', align: 'right' },
          { key: 'accounts', header: 'Members', align: 'right', render: (r) => `${num(r.accounts)}${Number(r.manualAccounts) ? ` (${num(r.manualAccounts)} manual)` : ''}` },
          { key: 'pendingThresholds', header: 'Thresholds', render: (r) => (r.pendingThresholds ? <StatusPill status="pending" label="Change pending" /> : <StatusPill status="active" label="In force" />) }]}
        actions={(r) => (
          <div className="oc-row-wrap">
            <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => setVersions(r)}>Versions</button>
            {can('crm.loyalty_tier.delete') && <ActionButton label="Delete" danger method="DELETE" path={`/api/v1/crm/loyalty/tiers/${r.id}`} invalidate={CRM}
              confirm="Deleted if no member has ever been in this tier; otherwise archived with its history."
              onDone={(res) => toast((res as R).result === 'archived' ? 'Archived: members have been in this tier' : 'Tier deleted')} />}
          </div>
        )} />
      {edit && <TierForm tier={edit === 'new' ? undefined : edit} onClose={() => setEdit(null)} />}
      {versions && <VersionsDrawer tier={versions} onClose={() => setVersions(null)} />}
      {reeval && <ReevaluateModal onClose={() => setReeval(false)} />}
    </div>
  );
}

// ── Manual classification ──────────────────────────────────────────────

function OverrideModal({ onClose }: { onClose: () => void }) {
  const toast = useToast();
  const [q, setQ] = useState('');
  const [acct, setAcct] = useState('');
  const [tier, setTier] = useState('');
  const [reason, setReason] = useState('');
  const [until, setUntil] = useState('');
  const accounts = useGet<Page<R>>(`/api/v1/crm/loyalty/accounts${qs({ q, limit: 20, 'filter[status]': 'active' })}`);
  const tiers = useGet<Page<R>>('/api/v1/crm/loyalty/tier-classes?filter[status]=active');
  const send = useSend<Record<string, unknown>, R>('POST', `/api/v1/crm/loyalty/accounts/${acct}:override-tier`, CRM);
  return (
    <Modal open onClose={onClose} title="Classify a member manually"
      actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
        <button className="oc-btn oc-btn-primary" disabled={!acct || !tier || !reason.trim() || send.isPending}
          onClick={() => send.mutate({ tierId: tier, reason, validUntil: until || undefined }, {
            onSuccess: (o) => { toast(o.status === 'pending' ? `${String(o.number)} waits for approval` : `${String(o.customerName)} is now ${String(o.tierName)}`); onClose(); },
          })}>Classify</button></>}>
      <ErrorAlert error={send.error} />
      <div className="oc-form">
        <TextField label="Find member" value={q} onChange={setQ} placeholder="Name, code or account number" />
        <SelectField label="Loyalty account" value={acct} onChange={setAcct} required
          options={(accounts.data?.items ?? []).map((a) => ({ value: a.id, label: `${String(a.customerName)} · ${String(a.number)} · ${String(a.tierName ?? 'No tier')}` }))} />
        <SelectField label="Tier" value={tier} onChange={setTier} required options={(tiers.data?.items ?? []).map((t) => ({ value: t.id, label: String(t.name) }))} />
        <TextField label="Valid until (empty = until revoked)" type="date" value={until} onChange={setUntil} />
        <TextArea label="Reason" value={reason} onChange={setReason} required span />
      </div>
    </Modal>
  );
}

function HistoryDrawer({ accountId, title, onClose }: { accountId: string; title: string; onClose: () => void }) {
  const h = useGet<Page<R>>(`/api/v1/crm/loyalty/accounts/${accountId}/tier-history`);
  return (
    <Drawer open onClose={onClose} title={`Tier history · ${title}`}>
      {!h.data ? <Skeleton /> : <DataTable rows={h.data.items} columns={[{ key: 'createdAt', header: 'When', render: (r) => formatDateTime(String(r.createdAt)) },
        { key: 'toTierName', header: 'Tier', render: (r) => `${String(r.fromTierName ?? '—')} → ${String(r.toTierName ?? '—')} ${r.direction === 'up' ? '▲' : r.direction === 'down' ? '▼' : ''}` },
        { key: 'source', header: 'Source', render: (r) => <StatusPill status={r.source === 'manual' ? 'pending' : 'active'} label={r.source === 'manual' ? 'Manual' : 'Auto'} /> },
        { key: 'reason', header: 'Reason', render: (r) => `${String(r.reason)}${r.overrideNumber ? ` (${String(r.overrideNumber)})` : ''}` },
        { key: 'createdByName', header: 'By', render: (r) => String(r.createdByName ?? 'System') }]} />}
    </Drawer>
  );
}

/** Manual classifications: tier, reason, valid until (approval), revoke, history with the source. */
export function TierOverridesPanel() {
  const { can } = useAuth();
  const [creating, setCreating] = useState(false);
  const [hist, setHist] = useState<R | null>(null);
  const may = can('crm.loyalty.tier_override');
  return (
    <>
      <ListPage title="Manual Classification" help="A member kept in a tier for a reason, optionally until a date (permission crm.loyalty.tier_override; approval workflow Loyalty Tier Override). The evaluations keep the tier while it is in force; afterwards the member returns to the tier held before."
        path="/api/v1/crm/loyalty/tier-overrides" search={false} statuses={['active', 'pending', 'ended', 'revoked', 'rejected'].map((v) => ({ value: v, label: label(v) }))}
        actions={may ? <button className="oc-btn oc-btn-primary" onClick={() => setCreating(true)}>Classify manually</button> : undefined}
        onRowClick={(r) => setHist(r)}
        columns={[{ key: 'number', header: 'Number' }, { key: 'customerName', header: 'Member' }, { key: 'tierName', header: 'Tier', render: (r) => `${String(r.previousTierName ?? '—')} → ${String(r.tierName)}` },
          { key: 'reason', header: 'Reason' }, { key: 'validUntil', header: 'Valid until', render: (r) => (r.validUntil ? day(r.validUntil) : 'Until revoked') },
          { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }, { key: 'createdByName', header: 'By', render: (r) => String(r.createdByName ?? '—') }]}
        rowActions={(r) => may && r.status === 'active' && <ActionButton label="Revoke" path={`/api/v1/crm/loyalty/tier-overrides/${r.id}:revoke`} invalidate={CRM} reason="required" danger
          confirm="The member returns to the tier held before the manual classification." />} />
      {creating && <OverrideModal onClose={() => setCreating(false)} />}
      {hist && <HistoryDrawer accountId={String(hist.accountId)} title={String(hist.customerName)} onClose={() => setHist(null)} />}
    </>
  );
}

/** Tier card of a customer (Benefit Lookup, Customer 360): badge, source, benefits, history. */
export function CustomerTierCard({ customerId }: { customerId: string }) {
  const b = useGet<R>(`/api/v1/crm/loyalty/tier-benefits?customerId=${customerId}`);
  const [hist, setHist] = useState(false);
  const x = b.data;
  if (!x) return b.isLoading ? <Skeleton /> : null;
  return (
    <Card title="Loyalty tier" icon="workspace_premium" actions={x.accountId ? <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => setHist(true)}>History</button> : undefined}>
      {!x.enrolled ? <p className="oc-muted">Not a loyalty member.</p> : (
        <KV items={[['Tier', <TierBadge key="t" t={x} />], ['Classification', x.tierSource === 'manual' ? `Manual${x.overrideUntil ? ` until ${day(x.overrideUntil)}` : ''}` : 'Automatic (tier evaluation)'],
          ['Points multiplier', `× ${String(x.pointsMultiplier)}`], ['Booking window', `+${String(x.bookingWindowDays)} days`], ['F&B discount', `${String(x.fnbDiscountPercent)}%`],
          ['VIP events', x.eventAccess ? 'Yes' : 'No'], ['Priority service', x.priorityService ? 'Yes' : 'No'], ['Grace until', day(x.graceUntil)]]} />
      )}
      {hist && x.accountId ? <HistoryDrawer accountId={String(x.accountId)} title="" onClose={() => setHist(false)} /> : null}
    </Card>
  );
}

// ── POS: tier F&B discount (cached with the member for offline sales) ──────

export type PosTierDiscount = { enabled: boolean; percent: number; label: string; productTypes: string[]; stack: boolean; ceiling: number; tierName?: string };
const TIER_CACHE = (customer: string) => `oneclub.pos.tierDiscount.${customer}`;

/** The tier discount of the POS customer: online from the server, offline from the cache of the member. */
export function usePosTierDiscount(customerId: string): PosTierDiscount | null {
  const [online, setOnline] = useState(typeof navigator === 'undefined' ? true : navigator.onLine);
  useEffect(() => {
    const on = () => setOnline(true), off = () => setOnline(false);
    window.addEventListener('online', on);
    window.addEventListener('offline', off);
    return () => { window.removeEventListener('online', on); window.removeEventListener('offline', off); };
  }, []);
  const q = useGet<R>(customerId && online ? `/api/v1/commercial/pos/tier-discount?customerId=${customerId}` : null, { staleTime: 15 * 60_000 });
  useEffect(() => {
    if (!customerId || !q.data) return;
    try { localStorage.setItem(TIER_CACHE(customerId), JSON.stringify(q.data)); } catch { /* storage unavailable */ }
  }, [customerId, q.data]);
  let raw: R | undefined = q.data;
  if (!raw && customerId) {
    try { raw = JSON.parse(localStorage.getItem(TIER_CACHE(customerId)) ?? 'null') ?? undefined; } catch { raw = undefined; }
  }
  if (!raw || !raw.enabled) return null;
  return { enabled: true, percent: Number(raw.percent), label: String(raw.label), productTypes: (raw.productTypes as string[]) ?? ['food', 'beverage'],
    stack: !!raw.stackWithPromotions, ceiling: Number(raw.maxStackedPercent ?? 100), tierName: raw.tierName ? String(raw.tierName) : undefined };
}

/**
 * Tier discount of a cart (same rules as the server: F&B lines, after the promotions; not stacked = the better of the promotion and the tier
 * discount, stacked = on the rest within the stacking ceiling). The promotion discount of the cart is spread over its lines by amount.
 */
export function posTierDiscount(t: PosTierDiscount | null, lines: { productType: string; amount: number }[], promoDiscount: number): number {
  if (!t || t.percent <= 0) return 0;
  const total = lines.reduce((s, l) => s + l.amount, 0);
  return lines.reduce((sum, l) => {
    if (!t.productTypes.includes(l.productType) || l.amount <= 0) return sum;
    const promo = total > 0 ? (promoDiscount * l.amount) / total : 0;
    if (!t.stack) return sum + Math.max(Math.round((l.amount * t.percent) / 100) - promo, 0);
    let d = Math.round(((l.amount - promo) * t.percent) / 100);
    if (t.ceiling > 0 && t.ceiling < 100) d = Math.min(d, Math.max(Math.round((l.amount * t.ceiling) / 100) - promo, 0));
    return sum + Math.max(d, 0);
  }, 0);
}

/** Tier discount line of the POS cart ("Gold member 5%"). */
export function PosTierLine({ t, amount }: { t: PosTierDiscount | null; amount: number }) {
  if (!t || amount <= 0) return null;
  return (
    <div className="oc-row oc-small" aria-label="Tier discount">
      <Icon name="workspace_premium" size={16} /><span>{t.label}</span><span className="oc-spacer" /><strong>− {money(amount)}</strong>
    </div>
  );
}
