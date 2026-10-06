import { useMemo, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router';
import { qs, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate, formatDateTime, formatNumber, formatRelative } from '@oneclub/i18n';
import {
  Card, Checkbox, DataTable, DateRange, Drawer, Empty, ErrorAlert, Icon, Modal, PageHeader, RequirePermission, SelectField, Skeleton, StatTile, StatusPill, TextField,
  useAuth, useToast, type Column,
} from '@oneclub/shell';
import { ActionButton, KV, Tabs, money, today, type R } from '../p1/common';
import type { AreaRoute, OpsRoute, OpsTile } from '../p3/types';

// PRD P5 — Management Dashboard & BI (EP-21) and the HR KPI framework (EP-27): Executive Overview across domains with
// targets, MoM / YoY, year to date and per property comparison; drill-down from a KPI to its dimensions and the source
// folio lines; KPI target plans (annual budget per month, approval, versions); HR Performance; scheduled reports and
// the self-service report builder. Every figure comes from /api/v1/reporting (analytics store on the read replica).

const API = '/api/v1/reporting';
const BI = [API];
const items = <T,>(v: unknown): T[] => (Array.isArray(v) ? (v as T[]) : []);
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

type KPI = {
  key: string; label: string; unit: string; kind: string; direction: string; definition: string; status: string; value?: string | null;
  target?: string | null; targetToDate?: string | null; achievement?: string | null; variance?: string | null; indicator: string;
  previous?: string | null; previousChange?: string | null; lastYear?: string | null; lastYearChange?: string | null; ytd?: string | null;
  ytdTarget?: string | null; breakdown?: { label: string; value: string }[]; dashboard: string; sourceKpi: string; report?: string; drillBy: string[];
  refreshedAt?: string | null;
};
type Domain = { code: string; label: string; module: string; dashboardPath: string; kpis: KPI[] };
type Overview = {
  propertyId: string; period: string; month: string; from: string; to: string; dataAsOf?: string | null; stale: boolean;
  targetPlan?: { id: string; year: number; version: number; title: string } | null; domains: Domain[]; generatedAt: string;
};

/** Formats a KPI value by unit. */
export function fmtKPI(v: unknown, unit: string) {
  if (v === null || v === undefined || v === '') return '—';
  const n = Number(v);
  if (unit === 'idr') return money(String(v));
  if (unit === 'ratio') return `${(n * 100).toFixed(1)}%`;
  if (unit === 'points' || unit === 'hours' || unit === 'days') return formatNumber(n, undefined, 1);
  return formatNumber(n);
}

function change(v?: string | null) {
  if (v === null || v === undefined) return null;
  const n = Number(v) * 100;
  const up = n >= 0;
  return <span className="oc-small" style={{ color: up ? 'var(--md-sys-color-primary)' : 'var(--md-sys-color-error)' }}>{up ? '▲' : '▼'} {Math.abs(n).toFixed(1)}%</span>;
}

const INDICATOR: Record<string, [string, string]> = {
  on_track: ['On Track', 'approved'], watch: ['Watch', 'pending'], off_track: ['Off Track', 'rejected'], no_target: ['No Target', 'draft'],
};

function Indicator({ value }: { value: string }) {
  const [l, tone] = INDICATOR[value] ?? [label(value), value];
  return <StatusPill status={tone} label={l} />;
}

function Freshness({ ov, onRefreshed }: { ov: Overview; onRefreshed: () => void }) {
  const { can } = useAuth();
  const toast = useToast();
  const refresh = useSend<Record<string, unknown>, R>('POST', `${API}/analytics:refresh`, BI);
  return (
    <div className="oc-row-wrap oc-small" role="status">
      <Icon name={ov.stale ? 'warning' : 'schedule'} size={18} />
      <span className={ov.stale ? '' : 'oc-muted'}>
        {ov.dataAsOf ? `Data as of ${formatDateTime(ov.dataAsOf)} (${formatRelative(ov.dataAsOf)})` : 'The analytics store has not been refreshed yet'}
        {ov.stale && ' · older than the BI Policies allow / lebih lama dari batas kebijakan BI'}
      </span>
      {can('reporting.analytics.refresh') && (
        <button className="oc-btn oc-btn-sm oc-btn-outline" disabled={refresh.isPending}
          onClick={() => refresh.mutate({}, { onSuccess: () => { toast('Analytics refreshed'); onRefreshed(); }, onError: (e) => toast(e.message, 'error') })}>
          Refresh now
        </button>
      )}
    </div>
  );
}

/** Icon per Executive Overview domain (Overview.domains[].code). */
export const DOMAIN_ICON: Record<string, string> = {
  golf: 'golf_course', sportclub: 'sports_tennis', membership: 'card_membership', booking: 'event_available', banquet: 'celebration',
  commercial: 'local_offer', inventory: 'warehouse', procurement: 'request_quote', finance: 'payments', crm: 'support_agent', hr: 'badge',
};

function KPICard({ k, ov }: { k: KPI; ov: Overview }) {
  const nav = useNavigate();
  const soon = k.status === 'coming_soon';
  const num = (v?: string | null) => (v == null ? null : Number(v));
  return (
    <StatTile label={k.label} title={k.definition} muted={soon} value={soon ? '—' : fmtKPI(k.value, k.unit)}
      change={soon ? null : num(k.previousChange)} changeLabel={ov.period === 'year' ? 'vs last year' : 'vs last month'}
      status={soon ? <span className="oc-nav-soon">Coming soon</span> : <Indicator value={k.indicator} />}
      progress={soon ? null : num(k.achievement)}
      onOpen={soon ? undefined : () => nav(`/management/drilldown${qs({ kpi: k.key, from: ov.from, to: ov.to })}`)}>
      {k.status === 'not_refreshed' && <div>Not refreshed yet</div>}
      {!soon && k.target != null && (
        <div className="oc-row"><span>Target{k.targetToDate !== k.target ? ' to date' : ''}</span><span className="oc-spacer" />
          <span className="oc-num">{fmtKPI(k.targetToDate ?? k.target, k.unit)}{k.achievement != null && ` · ${(Number(k.achievement) * 100).toFixed(1)}%`}</span></div>
      )}
      {!soon && k.previous != null && (
        <div className="oc-row"><span>{ov.period === 'year' ? 'Last year' : 'Previous month'}</span><span className="oc-spacer" /><span className="oc-num">{fmtKPI(k.previous, k.unit)}</span></div>
      )}
      {!soon && ov.period === 'month' && k.lastYear != null && (
        <div className="oc-row"><span>Same month last year</span><span className="oc-spacer" /><span className="oc-num">{fmtKPI(k.lastYear, k.unit)} {change(k.lastYearChange)}</span></div>
      )}
      {!soon && ov.period === 'month' && k.ytd != null && (
        <div className="oc-row"><span>Year to date</span><span className="oc-spacer" />
          <span className="oc-num">{fmtKPI(k.ytd, k.unit)}{k.ytdTarget != null && ` / ${fmtKPI(k.ytdTarget, k.unit)}`}</span></div>
      )}
      {!soon && items<{ label: string; value: string }>(k.breakdown).slice(0, 4).map((b) => (
        <div key={b.label} className="oc-row"><span>{label(b.label)}</span><span className="oc-spacer" /><strong>{fmtKPI(b.value, k.unit)}</strong></div>
      ))}
    </StatTile>
  );
}

function PropertyComparison({ month }: { month: string }) {
  const d = useGet<R>(`${API}/executive/properties${qs({ month })}`);
  if (!d.data) return <><ErrorAlert error={d.error} /><Skeleton rows={6} /></>;
  const props = items<R>(d.data.properties);
  const rows = items<R>(d.data.kpis).map((k): R => ({ ...k, id: String(k.key) }));
  const cols: Column<R>[] = [{ key: 'label', header: 'KPI', render: (r) => <span>{String(r.label)} <span className="oc-muted oc-small">{label(r.domain)}</span></span> },
    ...props.map((p) => ({ key: String(p.id), header: String(p.name), align: 'right' as const, render: (r: R) => {
      const v = items<R>(r.values).find((x) => x.propertyId === p.id);
      return <span>{fmtKPI(v?.value, String(r.unit))}{v?.target != null && <span className="oc-muted oc-small"> / {fmtKPI(v.target, String(r.unit))}</span>}</span>;
    } }))];
  return <DataTable rows={rows} columns={cols} />;
}

/** Executive Overview with targets (FR-BI-02/03/08): the Management Dashboard home. */
export function BIExecutiveOverviewPage() {
  const [params, setParams] = useSearchParams();
  const period = params.get('period') ?? 'month';
  const month = params.get('month') ?? today().slice(0, 7);
  const view = params.get('view') ?? 'domains';
  const set = (k: string, v: string) => { const n = new URLSearchParams(params); n.set(k, v); setParams(n, { replace: true }); };
  const d = useGet<Overview>(`${API}/executive${qs({ period, month })}`);
  const live = useGet<R>(`${API}/dashboards/executive-overview`);
  const ov = d.data;
  return (
    <div className="oc-stack">
      <PageHeader title="Executive Overview" help={ov ? `${formatDate(ov.from)} – ${formatDate(ov.to)}${ov.targetPlan ? ` · Target plan ${ov.targetPlan.year} v${ov.targetPlan.version}` : ' · no approved target plan'}` : undefined}
        actions={<>
          <Tabs tabs={[{ value: 'month', label: 'Month' }, { value: 'year', label: 'Year to date' }]} value={period} onChange={(v) => set('period', v)} />
          <input className="oc-input oc-filter" type="month" aria-label="Month" value={month} onChange={(e) => e.target.value && set('month', e.target.value)} />
        </>} />
      <ErrorAlert error={d.error} />
      <div className="oc-row-wrap">
        <Tabs tabs={[{ value: 'domains', label: 'Domains' }, { value: 'properties', label: 'By property' }, { value: 'today', label: 'Today' }]} value={view}
          onChange={(v) => set('view', v)} />
        <span className="oc-spacer" />
        {ov && <Freshness ov={ov} onRefreshed={() => d.refetch()} />}
      </div>
      {view === 'properties' && <Card title="By property"><PropertyComparison month={month} /></Card>}
      {view === 'today' && (
        <div className="oc-stat-grid">
          {items<R>(live.data?.widgets).filter((w) => w.status === 'available').map((w) => (
            <StatTile key={String(w.key)} label={String(w.label)} value={formatNumber(Number(w.value ?? 0))} />
          ))}
        </div>
      )}
      {view === 'domains' && !ov && !d.error && <Skeleton rows={8} />}
      {view === 'domains' && ov?.domains.map((dm) => (
        <Card key={dm.code} title={dm.label} icon={DOMAIN_ICON[dm.code] ?? 'insights'}
          actions={<Link className="oc-btn oc-btn-sm oc-btn-outline" to={`${dm.dashboardPath}${qs({ from: ov.from, to: ov.to })}`}>Open dashboard</Link>}>
          <div className="oc-stat-grid">{dm.kpis.map((k) => <KPICard key={k.key} k={k} ov={ov} />)}</div>
        </Card>
      ))}
    </div>
  );
}

// ── drill-down (FR-BI-04) ────────────────────────────────────────────────

const DIM_LABEL: Record<string, string> = { day: 'Day', weekday: 'Weekday', daypart: 'Daypart', business_line: 'Business Line', revenue_component: 'Revenue Component', outlet: 'Outlet', segment: 'Segment', lines: 'Transactions' };

function Trend({ kpi }: { kpi: string }) {
  const t = useGet<R>(`${API}/executive/trend${qs({ kpi, months: 13 })}`);
  const pts = items<R>(t.data?.points);
  const max = Math.max(1, ...pts.map((p) => Math.max(Number(p.value ?? 0), Number(p.target ?? 0), Number(p.lastYear ?? 0))));
  if (!pts.length) return null;
  return (
    <Card title="Trend — 13 months (bar: actual · line: target · dot: last year)" icon="show_chart">
      <div role="img" aria-label="Monthly trend of the KPI with target and last year" style={{ display: 'flex', alignItems: 'flex-end', gap: 6, height: 140 }}>
        {pts.map((p) => {
          const h = (v: unknown) => `${(Number(v ?? 0) / max) * 120}px`;
          return (
            <div key={String(p.month)} style={{ flex: 1, position: 'relative', height: 140, display: 'flex', flexDirection: 'column', justifyContent: 'flex-end', alignItems: 'center' }}
              title={`${p.month}: ${fmtKPI(p.value, String(t.data?.unit))}${p.target != null ? ` / target ${fmtKPI(p.target, String(t.data?.unit))}` : ''}`}>
              {p.target != null && <div style={{ position: 'absolute', bottom: h(p.target), left: 0, right: 0, borderTop: '2px dashed var(--md-sys-color-error)' }} />}
              {p.lastYear != null && <div style={{ position: 'absolute', bottom: h(p.lastYear), width: 6, height: 6, borderRadius: 3, background: 'var(--md-sys-color-outline)' }} />}
              <div style={{ width: '70%', height: h(p.value), background: 'var(--md-sys-color-primary)', borderRadius: 4 }} />
              <div className="oc-small oc-muted">{MONTHS[Number(String(p.month).slice(5, 7)) - 1]}</div>
            </div>
          );
        })}
      </div>
    </Card>
  );
}

export function BIDrilldownPage() {
  const [params, setParams] = useSearchParams();
  const kpi = params.get('kpi') ?? '';
  const by = params.get('by') ?? 'day';
  const filters = Object.fromEntries([...params.entries()].filter(([k]) => k.startsWith('filter[')));
  const d = useGet<R>(kpi ? `${API}/drilldown${qs({ kpi, from: params.get('from'), to: params.get('to'), by, ...filters })}` : null);
  const r = d.data;
  const go = (next: Record<string, string | null>) => {
    const n = new URLSearchParams(params);
    for (const [k, v] of Object.entries(next)) if (v === null) n.delete(k); else n.set(k, v);
    setParams(n);
  };
  const unit = String(r?.unit ?? 'idr');
  const dims = items<string>(r?.dimensions);
  return (
    <div className="oc-stack">
      <PageHeader title={`${String(r?.label ?? 'KPI')} — drill-down`} help={r ? `${formatDate(String(r.from))} – ${formatDate(String(r.to))} · total ${fmtKPI(r.total, unit)}` : undefined}
        actions={<Link className="oc-btn oc-btn-neutral" to="/management">Executive Overview</Link>} />
      <ErrorAlert error={d.error} />
      <div className="oc-row-wrap">
        <div style={{ width: 170 }}><TextField label="From" type="date" value={params.get('from') ?? String(r?.from ?? '')} onChange={(v) => go({ from: v })} /></div>
        <div style={{ width: 170 }}><TextField label="To" type="date" value={params.get('to') ?? String(r?.to ?? '')} onChange={(v) => go({ to: v })} /></div>
        {Object.entries(filters).map(([k, v]) => (
          <button key={k} className="oc-chip" aria-pressed="true" aria-label={`Remove filter ${k}`} onClick={() => go({ [k]: null })}>{label(k.slice(7, -1))}: {label(v)} ✕</button>
        ))}
      </div>
      {kpi && <Trend kpi={kpi} />}
      {r && (
        <Card title={`By ${DIM_LABEL[by] ?? label(by)}`} icon="account_tree" actions={
          <div className="oc-row-wrap">{[by, ...dims].filter((x, i, a) => a.indexOf(x) === i).map((x) => (
            <button key={x} className="oc-chip" aria-pressed={x === by} onClick={() => go({ by: x })}>{DIM_LABEL[x] ?? label(x)}</button>
          ))}</div>}>
          {by === 'lines' ? (
            <DataTable rows={items<R>(r.lines).map((l): R => ({ ...l, id: String(l.lineId) }))} columns={[
              { key: 'postedAt', header: 'Posted', render: (l) => formatDateTime(String(l.postedAt)) },
              { key: 'folioNumber', header: 'Folio' }, { key: 'holderName', header: 'Guest / Holder' }, { key: 'description', header: 'Description' },
              { key: 'revenueComponent', header: 'Component', render: (l) => label(l.revenueComponent) }, { key: 'outlet', header: 'Outlet' },
              { key: 'segment', header: 'Segment', render: (l) => label(l.segment) }, { key: 'quantity', header: 'Qty', align: 'right' },
              { key: 'total', header: 'Total', align: 'right', render: (l) => money(String(l.total)) },
            ]} />
          ) : (
            <DataTable rows={items<R>(r.rows).map((x): R => ({ ...x, id: String(x.key) }))} onRowClick={(x) => {
              if (by === 'day' && unit !== 'idr' && dims.length === 0) return;
              // time → revenue component → source lines (EP-21 AC 2); other dimensions through the chips
              const next = ['day', 'weekday', 'daypart'].includes(by) && dims.includes('revenue_component') ? 'revenue_component' : 'lines';
              go({ [`filter[${by}]`]: String(x.key), by: next });
            }} columns={[
              { key: 'label', header: DIM_LABEL[by] ?? label(by), render: (x) => (by === 'day' ? formatDate(String(x.key)) : label(x.label)) },
              { key: 'value', header: String(r.label), align: 'right', render: (x) => fmtKPI(x.value, unit) },
              { key: 'share', header: 'Share', align: 'right', render: (x) => (x.share != null ? `${(Number(x.share) * 100).toFixed(1)}%` : '—') },
              { key: 'lines', header: 'Lines', align: 'right' },
            ]} />
          )}
          {Boolean(r.truncated) && <div className="oc-small oc-muted">First rows only — narrow the filters or open the source report</div>}
        </Card>
      )}
      {r && items<R>(r.links).length > 0 && (
        <div className="oc-row-wrap">{items<R>(r.links).map((l) => <Link key={String(l.path)} className="oc-btn oc-btn-outline" to={String(l.path)}>{String(l.label)}</Link>)}</div>
      )}
    </div>
  );
}

// ── KPI targets (FR-BI-03) ───────────────────────────────────────────────

function PlanDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const toast = useToast();
  const p = useGet<R>(`${API}/kpi-targets/${id}`);
  const defs = useGet<Page<R>>(`${API}/kpi-definitions`);
  const execDefs = items<R>(defs.data?.items).filter((d) => d.executive && d.status === 'available');
  const [edits, setEdits] = useState<Record<string, string>>({});
  const [annualKPI, setAnnualKPI] = useState('');
  const [annual, setAnnual] = useState('');
  const save = useSend<Record<string, unknown>, R>('PATCH', `${API}/kpi-targets/${id}`, BI);
  const plan = p.data;
  const editable = plan && (plan.status === 'draft' || plan.status === 'rejected') && can('reporting.kpi_target.manage');
  const grid = useMemo(() => {
    const m: Record<string, Record<number, string>> = {};
    for (const t of items<R>(plan?.targets)) (m[String(t.kpiKey)] ??= {})[Number(t.month)] = String(t.target);
    return m;
  }, [plan]);
  const keys = Array.from(new Set([...Object.keys(grid), ...Object.keys(edits).map((k) => k.split('|')[0])]));
  const defOf = (k: string) => execDefs.find((d) => d.executiveKey === k);
  const submitEdits = () => {
    const targets = Object.entries(edits).filter(([, v]) => v !== '').map(([k, v]) => ({ kpiKey: k.split('|')[0], month: Number(k.split('|')[1]), target: v }));
    save.mutate({ targets }, { onSuccess: () => { setEdits({}); toast('Targets saved'); p.refetch(); }, onError: (e) => toast(e.message, 'error') });
  };
  return (
    <Drawer open onClose={onClose} title={plan ? `KPI Target Plan ${String(plan.year)} v${String(plan.version)}` : 'KPI Target Plan'}>
      {!plan ? <Skeleton rows={6} /> : (
        <div className="oc-stack">
          <KV items={[['Title', String(plan.title)], ['Status', <StatusPill key="s" status={String(plan.status)} />], ['Targets', String(plan.targetCount)],
            ['Decision', plan.decisionReason ? String(plan.decisionReason) : '—']]} />
          <div className="oc-row-wrap">
            {editable && <ActionButton label="Submit for approval" kind="ink" path={`${API}/kpi-targets/${id}:submit`} invalidate={BI} onDone={() => p.refetch()} />}
            {(plan.status === 'approved' || plan.status === 'superseded') && can('reporting.kpi_target.manage') && (
              <ActionButton label="Revise" path={`${API}/kpi-targets/${id}:revise`} invalidate={BI} onDone={() => onClose()} />
            )}
            {editable && <ActionButton label="Delete" danger confirm="Delete this draft plan?" method="DELETE" path={`${API}/kpi-targets/${id}`} invalidate={BI} onDone={onClose} />}
          </div>
          {editable && (
            <Card title="Annual budget → per month" icon="calendar_month">
              <p className="oc-small oc-muted" style={{ marginTop: 0 }}>Flow KPIs are split evenly over 12 months; ratios and balances keep the same value each month. /
                Anggaran tahunan dibagi rata per bulan untuk KPI kumulatif.</p>
              <div className="oc-row-wrap">
                <div style={{ minWidth: 260 }}><SelectField label="KPI" value={annualKPI} onChange={setAnnualKPI} placeholder="Choose a KPI"
                  options={execDefs.map((d) => ({ value: String(d.executiveKey), label: `${String(d.label)} (${label(d.domain)})` }))} /></div>
                <div style={{ width: 200 }}><TextField label="Annual amount" value={annual} onChange={setAnnual} inputMode="decimal" /></div>
                <button className="oc-btn oc-btn-ink" style={{ alignSelf: 'flex-end' }} disabled={!annualKPI || !annual || save.isPending}
                  onClick={() => save.mutate({ remove: [annualKPI], annual: [{ kpiKey: annualKPI, amount: annual }] }, {
                    onSuccess: () => { setAnnual(''); toast('Annual budget split per month'); p.refetch(); }, onError: (e) => toast(e.message, 'error') })}>Apply</button>
              </div>
            </Card>
          )}
          <div className="oc-table-wrap">
            <table className="oc-table">
              <thead><tr><th>KPI</th>{MONTHS.map((m) => <th key={m} style={{ textAlign: 'right' }}>{m}</th>)}</tr></thead>
              <tbody>
                {keys.map((k) => (
                  <tr key={k}>
                    <td>{String(defOf(k)?.label ?? label(k))}</td>
                    {MONTHS.map((_, i) => {
                      const cell = `${k}|${i + 1}`;
                      const v = edits[cell] ?? grid[k]?.[i + 1] ?? '';
                      return (
                        <td key={cell} style={{ textAlign: 'right' }}>
                          {editable ? <input className="oc-input" aria-label={`${k} ${MONTHS[i]}`} style={{ width: 110, textAlign: 'right' }} value={v} inputMode="decimal"
                            onChange={(e) => setEdits({ ...edits, [cell]: e.target.value })} /> : fmtKPI(v, String(defOf(k)?.unit ?? 'count'))}
                        </td>
                      );
                    })}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {keys.length === 0 && <Empty title="No targets yet" help="Apply an annual budget per KPI" />}
          {editable && Object.keys(edits).length > 0 && <button className="oc-btn oc-btn-ink" disabled={save.isPending} onClick={submitEdits}>Save targets</button>}
          <ErrorAlert error={save.error} />
        </div>
      )}
    </Drawer>
  );
}

function NewPlan({ open, onClose, onCreated }: { open: boolean; onClose: () => void; onCreated: (id: string) => void }) {
  const [year, setYear] = useState(String(new Date().getFullYear() + (new Date().getMonth() >= 10 ? 1 : 0)));
  const [title, setTitle] = useState('');
  const create = useSend<Record<string, unknown>, R>('POST', `${API}/kpi-targets`, BI);
  return (
    <Modal open={open} onClose={onClose} title="New KPI target plan" actions={<>
      <button className="oc-btn oc-btn-text" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-ink" disabled={create.isPending} onClick={() => create.mutate({ year: Number(year), title: title || undefined },
        { onSuccess: (r) => { onClose(); onCreated(String(r.id)); } })}>Create</button>
    </>}>
      <div className="oc-form-grid">
        <TextField label="Budget year" type="number" value={year} onChange={setYear} required />
        <TextField label="Title" value={title} onChange={setTitle} help="e.g. Budget 2027 (Finance Manager & GM)" />
      </div>
      <ErrorAlert error={create.error} />
    </Modal>
  );
}

export function BITargetsPage() {
  const { can } = useAuth();
  const [year, setYear] = useState(String(new Date().getFullYear()));
  const [tab, setTab] = useState('plans');
  const [open, setOpen] = useState<string | null>(null);
  const [create, setCreate] = useState(false);
  const plans = useGet<Page<R>>(`${API}/kpi-targets${qs({ year })}`);
  const defs = useGet<Page<R>>(tab === 'definitions' ? `${API}/kpi-definitions` : null);
  return (
    <div className="oc-stack">
      <PageHeader title="KPI Targets" help="Annual budget per month set by the Finance Manager and the GM, approved by the Board / Owner (Approval Workflows)."
        actions={can('reporting.kpi_target.manage') && <button className="oc-btn oc-btn-ink" onClick={() => setCreate(true)}><Icon name="add" size={18} /> New plan</button>} />
      <Tabs tabs={[{ value: 'plans', label: 'Target Plans' }, { value: 'definitions', label: 'KPI Definitions' }]} value={tab} onChange={setTab} />
      {tab === 'plans' && (
        <>
          <div style={{ width: 140 }}><TextField label="Year" type="number" value={year} onChange={setYear} /></div>
          <DataTable rows={plans.data?.items} loading={plans.isLoading} error={plans.error} onRowClick={(r) => setOpen(r.id)} columns={[
            { key: 'year', header: 'Year' }, { key: 'version', header: 'Version' }, { key: 'title', header: 'Title' },
            { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }, { key: 'targetCount', header: 'Targets', align: 'right' },
            { key: 'decidedAt', header: 'Decided', render: (r) => (r.decidedAt ? formatDateTime(String(r.decidedAt)) : '—') },
          ]} />
        </>
      )}
      {tab === 'definitions' && (
        <DataTable rows={items<R>(defs.data?.items).map((d): R => ({ ...d, id: `${String(d.dashboard)}/${String(d.key)}` }))} loading={defs.isLoading} columns={[
          { key: 'label', header: 'KPI' }, { key: 'dashboardName', header: 'Dashboard' }, { key: 'unit', header: 'Unit' },
          { key: 'kind', header: 'Kind', render: (d) => label(d.kind) }, { key: 'definition', header: 'Definition' },
          { key: 'executive', header: 'Executive', render: (d) => (d.executive ? <Icon name="check" size={18} /> : '') },
          { key: 'status', header: 'Status', render: (d) => <StatusPill status={String(d.status)} label={d.status === 'coming_soon' ? 'Coming soon' : 'Available'} /> },
        ]} />
      )}
      <NewPlan open={create} onClose={() => setCreate(false)} onCreated={(id) => setOpen(id)} />
      {open && <PlanDrawer id={open} onClose={() => setOpen(null)} />}
    </div>
  );
}

// ── HR Performance (EP-27) ───────────────────────────────────────────────

export function BIHRPerformancePage() {
  const [from, setFrom] = useState(`${today().slice(0, 8)}01`);
  const [to, setTo] = useState(today());
  const d = useGet<R>(`${API}/hr-performance${qs({ from, to })}`);
  return (
    <div className="oc-stack">
      <PageHeader title="HR Performance" help="Headcount, attendance, overtime, payroll cost, caddy attendance & rating, turnover and certification compliance."
        actions={<DateRange from={from} to={to} onFrom={setFrom} onTo={setTo} />} />
      <ErrorAlert error={d.error} />
      {!d.data && <Skeleton rows={6} />}
      <div className="oc-stat-grid">
        {items<R>(d.data?.kpis).map((k) => {
          const soon = k.status === 'coming_soon';
          return (
            <StatTile key={String(k.key)} label={String(k.label)} title={String(k.definition)} muted={soon} value={soon ? '—' : fmtKPI(k.value, String(k.unit))}
              status={soon ? <span className="oc-nav-soon">Coming soon — HRIS</span> : undefined}>
              {items<R>(k.breakdown).map((b) => <div key={String(b.label)} className="oc-row"><span>{label(b.label)}</span><span className="oc-spacer" /><strong>{fmtKPI(b.value, String(k.unit) === 'ratio' ? 'count' : String(k.unit))}</strong></div>)}
              <div>{String(k.definition)}</div>
            </StatTile>
          );
        })}
      </div>
    </div>
  );
}

// ── Scheduled reports (FR-BI-05) ─────────────────────────────────────────

const ROLE_OPTIONS = ['general_manager', 'club_manager', 'resort_manager', 'finance_manager', 'accountant', 'hr_manager', 'golf_manager',
  'sport_club_manager', 'banquet_manager', 'inventory_manager', 'procurement_manager', 'crm_admin', 'property_admin'];
const WEEKDAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
const PERIODS = [['previous_day', 'Previous day'], ['previous_week', 'Previous week'], ['previous_month', 'Previous month'],
  ['month_to_date', 'Month to date'], ['year_to_date', 'Year to date']];

function ScheduleForm({ initial, onClose }: { initial?: R; onClose: () => void }) {
  const toast = useToast();
  const reports = useGet<Page<R>>(`${API}/reports`);
  const [f, setF] = useState<Record<string, unknown>>(() => initial ? { ...initial } : {
    name: '', reportCode: '', format: 'pdf', period: 'previous_week', frequency: 'weekly', weekday: 1, monthDay: 1, sendTime: '07:00',
    channels: ['email', 'in_app'], recipientRoles: ['general_manager'],
  });
  const set = (k: string, v: unknown) => setF({ ...f, [k]: v });
  const toggle = (k: string, v: string) => {
    const cur = items<string>(f[k]);
    set(k, cur.includes(v) ? cur.filter((x) => x !== v) : [...cur, v]);
  };
  const send = useSend<Record<string, unknown>, R>(initial ? 'PATCH' : 'POST', initial ? `${API}/scheduled-reports/${String(initial.id)}` : `${API}/scheduled-reports`, BI);
  const body = () => {
    const b: Record<string, unknown> = { name: f.name, format: f.format, period: f.period, frequency: f.frequency, sendTime: f.sendTime,
      channels: f.channels, recipientRoles: f.recipientRoles };
    if (!initial) b.reportCode = f.reportCode;
    if (f.frequency === 'weekly') b.weekday = Number(f.weekday ?? 1);
    if (f.frequency === 'monthly') b.monthDay = Number(f.monthDay ?? 1);
    return b;
  };
  return (
    <Modal open wide onClose={onClose} title={initial ? 'Edit scheduled report' : 'Schedule a report'} actions={<>
      <button className="oc-btn oc-btn-text" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-ink" disabled={send.isPending} onClick={() => send.mutate(body(), { onSuccess: () => { toast('Schedule saved'); onClose(); } })}>Save</button>
    </>}>
      <div className="oc-form-grid">
        <TextField label="Name" value={String(f.name ?? '')} onChange={(v) => set('name', v)} />
        {!initial && <SelectField label="Report" value={String(f.reportCode ?? '')} onChange={(v) => set('reportCode', v)} placeholder="Choose a report" required
          options={items<R>(reports.data?.items).map((r) => ({ value: String(r.code), label: String(r.name) }))} />}
        <SelectField label="Format" value={String(f.format)} onChange={(v) => set('format', v)} options={[{ value: 'pdf', label: 'PDF' }, { value: 'xlsx', label: 'XLSX' }, { value: 'csv', label: 'CSV' }]} />
        <SelectField label="Period of the data" value={String(f.period)} onChange={(v) => set('period', v)} options={PERIODS.map(([value, l]) => ({ value, label: l }))} />
        <SelectField label="Frequency" value={String(f.frequency)} onChange={(v) => set('frequency', v)}
          options={[{ value: 'daily', label: 'Daily' }, { value: 'weekly', label: 'Weekly' }, { value: 'monthly', label: 'Monthly' }]} />
        {f.frequency === 'weekly' && <SelectField label="Weekday" value={String(f.weekday ?? 1)} onChange={(v) => set('weekday', v)} options={WEEKDAYS.map((w, i) => ({ value: String(i), label: w }))} />}
        {f.frequency === 'monthly' && <TextField label="Day of month (1–28)" type="number" value={String(f.monthDay ?? 1)} onChange={(v) => set('monthDay', v)} />}
        <TextField label="Send time" type="time" value={String(f.sendTime ?? '07:00')} onChange={(v) => set('sendTime', v)} help="Club time zone" />
      </div>
      <fieldset className="oc-stack" style={{ border: 0, padding: 0 }}>
        <legend className="oc-small oc-muted">Channels</legend>
        <div className="oc-row-wrap">{[['email', 'E-mail'], ['whatsapp', 'WhatsApp'], ['in_app', 'In-app']].map(([c, l]) => (
          <Checkbox key={c} label={l} checked={items<string>(f.channels).includes(c)} onChange={() => toggle('channels', c)} />))}</div>
        <legend className="oc-small oc-muted">Recipients (roles at this property; users without the report permission are skipped)</legend>
        <div className="oc-row-wrap">{ROLE_OPTIONS.map((r) => (
          <Checkbox key={r} label={label(r)} checked={items<string>(f.recipientRoles).includes(r)} onChange={() => toggle('recipientRoles', r)} />))}</div>
      </fieldset>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function Runs({ id, onClose }: { id: string; onClose: () => void }) {
  const runs = useGet<Page<R>>(`${API}/scheduled-reports/${id}/runs`);
  return (
    <Drawer open onClose={onClose} title="Deliveries">
      <DataTable rows={runs.data?.items} loading={runs.isLoading} columns={[
        { key: 'createdAt', header: 'Run', render: (r) => formatDateTime(String(r.createdAt)) }, { key: 'trigger', header: 'Trigger', render: (r) => label(r.trigger) },
        { key: 'period', header: 'Period', render: (r) => `${formatDate(String(r.periodFrom))} – ${formatDate(String(r.periodTo))}` },
        { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> },
        { key: 'delivered', header: 'Delivered', align: 'right' }, { key: 'skipped', header: 'Skipped', align: 'right' }, { key: 'error', header: 'Note' },
      ]} />
    </Drawer>
  );
}

export function BIScheduledReportsPage() {
  const { can } = useAuth();
  const list = useGet<Page<R>>(`${API}/scheduled-reports`);
  const [edit, setEdit] = useState<R | null | undefined>(undefined);
  const [runs, setRuns] = useState<string | null>(null);
  const manage = can('reporting.scheduled_report.manage');
  return (
    <div className="oc-stack">
      <PageHeader title="Scheduled Reports" help="Reports delivered daily, weekly or monthly by e-mail, WhatsApp and in-app to the roles of this property; files under Reports → Exports."
        actions={manage && <button className="oc-btn oc-btn-ink" onClick={() => setEdit(null)}><Icon name="add" size={18} /> Schedule a report</button>} />
      <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error} columns={[
        { key: 'name', header: 'Name' }, { key: 'reportName', header: 'Report' }, { key: 'format', header: 'Format', render: (r) => String(r.format).toUpperCase() },
        { key: 'frequency', header: 'Frequency', render: (r) => `${label(r.frequency)}${r.frequency === 'weekly' ? ` · ${WEEKDAYS[Number(r.weekday)]}` : ''}${r.frequency === 'monthly' ? ` · day ${String(r.monthDay)}` : ''} · ${String(r.sendTime)}` },
        { key: 'recipientRoles', header: 'Recipients', render: (r) => items<string>(r.recipientRoles).map(label).join(', ') },
        { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> },
        { key: 'nextRunAt', header: 'Next run', render: (r) => (r.nextRunAt ? formatDateTime(String(r.nextRunAt)) : '—') },
        { key: 'lastStatus', header: 'Last', render: (r) => (r.lastStatus ? <StatusPill status={String(r.lastStatus)} /> : '—') },
      ]} actions={(r) => (
        <div className="oc-row">
          <button className="oc-btn oc-btn-sm oc-btn-text" onClick={() => setRuns(r.id)}>Deliveries</button>
          {manage && <>
            <ActionButton label="Send now" path={`${API}/scheduled-reports/${r.id}:run`} invalidate={BI} />
            {r.status === 'active' ? <ActionButton label="Pause" path={`${API}/scheduled-reports/${r.id}:pause`} invalidate={BI} />
              : <ActionButton label="Resume" path={`${API}/scheduled-reports/${r.id}:resume`} invalidate={BI} />}
            <button className="oc-btn oc-btn-sm oc-btn-text" onClick={() => setEdit(r)}>Edit</button>
            <ActionButton label="Remove" danger confirm="Stop and remove this schedule?" method="DELETE" path={`${API}/scheduled-reports/${r.id}`} invalidate={BI} />
          </>}
        </div>
      )} />
      {edit !== undefined && <ScheduleForm initial={edit ?? undefined} onClose={() => setEdit(undefined)} />}
      {runs && <Runs id={runs} onClose={() => setRuns(null)} />}
    </div>
  );
}

// ── Report builder (FR-BI-06) ────────────────────────────────────────────

export function BIReportBuilderPage() {
  const { can } = useAuth();
  const toast = useToast();
  const ds = useGet<Page<R>>(`${API}/datasets`);
  const saved = useGet<Page<R>>(`${API}/saved-reports`);
  const [code, setCode] = useState('');
  const [dims, setDims] = useState<string[]>([]);
  const [metrics, setMetrics] = useState<string[]>([]);
  const [period, setPeriod] = useState('month_to_date');
  const [filterKey, setFilterKey] = useState('');
  const [filterValue, setFilterValue] = useState('');
  const [run, setRun] = useState<string | null>(null);
  const [name, setName] = useState('');
  const [shared, setShared] = useState(false);
  const dataset = items<R>(ds.data?.items).find((d) => d.code === code);
  const filters = filterKey && filterValue ? { [`filter[${filterKey}]`]: filterValue } : {};
  const res = useGet<R>(run);
  const save = useSend<Record<string, unknown>, R>('POST', `${API}/saved-reports`, BI);
  const toggle = (list: string[], set: (v: string[]) => void, v: string) => set(list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);
  const go = () => setRun(`${API}/datasets/${code}/query${qs({ dimensions: dims.join(','), metrics: metrics.join(','), period, ...filters })}`);
  const result = res.data;
  return (
    <div className="oc-stack">
      <PageHeader title="Report Builder" help="Pick a dataset, dimensions, metrics and filters; datasets follow your report permissions." />
      <div className="oc-grid-2">
        <Card title="Query" icon="tune">
          <div className="oc-stack">
            <SelectField label="Dataset" value={code} onChange={(v) => { setCode(v); setDims([]); setMetrics([]); setFilterKey(''); }} placeholder="Choose a dataset"
              options={items<R>(ds.data?.items).map((d) => ({ value: String(d.code), label: String(d.name) }))} />
            {dataset && <div className="oc-small oc-muted">{String(dataset.description)}</div>}
            {dataset && <>
              <div className="oc-small oc-muted">Dimensions</div>
              <div className="oc-row-wrap">{items<R>(dataset.dimensions).map((d) => <Checkbox key={String(d.key)} label={String(d.label)} checked={dims.includes(String(d.key))} onChange={() => toggle(dims, setDims, String(d.key))} />)}</div>
              <div className="oc-small oc-muted">Metrics</div>
              <div className="oc-row-wrap">{items<R>(dataset.metrics).map((d) => <Checkbox key={String(d.key)} label={String(d.label)} checked={metrics.includes(String(d.key))} onChange={() => toggle(metrics, setMetrics, String(d.key))} />)}</div>
              <div className="oc-row-wrap">
                <div style={{ width: 200 }}><SelectField label="Period" value={period} onChange={setPeriod} options={[{ value: 'month_to_date', label: 'Month to date' },
                  { value: 'previous_month', label: 'Previous month' }, { value: 'year_to_date', label: 'Year to date' }, { value: 'last_30_days', label: 'Last 30 days' }]} /></div>
                <div style={{ width: 200 }}><SelectField label="Filter" value={filterKey} onChange={setFilterKey} placeholder="None"
                  options={items<R>(dataset.dimensions).map((d) => ({ value: String(d.key), label: String(d.label) }))} /></div>
                {filterKey && <div style={{ width: 200 }}><TextField label="Equals" value={filterValue} onChange={setFilterValue} /></div>}
              </div>
              <button className="oc-btn oc-btn-ink" disabled={metrics.length === 0} onClick={go}>Run</button>
            </>}
          </div>
        </Card>
        <Card title="Saved reports" icon="bookmark">
          <DataTable rows={saved.data?.items} loading={saved.isLoading} columns={[{ key: 'name', header: 'Name' }, { key: 'dataset', header: 'Dataset' },
            { key: 'shared', header: 'Shared', render: (r) => (r.shared ? 'Yes' : 'No') }]}
            actions={(r) => (
              <div className="oc-row">
                <button className="oc-btn oc-btn-sm oc-btn-text" onClick={() => setRun(`${API}/saved-reports/${r.id}/run`)}>Run</button>
                {Boolean(r.mine) && can('reporting.saved_report.manage') && (
                  <ActionButton label="Delete" danger confirm="Delete this saved report?" method="DELETE" path={`${API}/saved-reports/${r.id}`} invalidate={BI} />
                )}
              </div>
            )} />
        </Card>
      </div>
      <ErrorAlert error={res.error} />
      {result && (
        <Card title={`Result · ${formatDate(String(result.from))} – ${formatDate(String(result.to))}`} icon="table" actions={can('reporting.saved_report.manage') && code && (
          <div className="oc-row-wrap">
            <div style={{ width: 220 }}><TextField label="Save as" value={name} onChange={setName} /></div>
            <Checkbox label="Share with the property" checked={shared} onChange={setShared} />
            <button className="oc-btn oc-btn-sm oc-btn-outline" style={{ alignSelf: 'flex-end' }} disabled={!name || save.isPending} onClick={() => save.mutate({
              name, dataset: code, shared, definition: { dimensions: dims, metrics, period, filters: filterKey && filterValue ? { [filterKey]: filterValue } : undefined },
            }, { onSuccess: () => { setName(''); toast('Saved'); }, onError: (e) => toast(e.message, 'error') })}>Save</button>
          </div>)}>
          <DataTable rows={items<R>(result.rows).map((r, i): R => ({ ...r, id: String(i) }))} columns={items<R>(result.columns).map((c) => ({
            key: String(c.key), header: String(c.label), align: c.type === 'number' ? 'right' as const : undefined,
            render: (r: R) => (c.type === 'number' ? formatNumber(Number(r[String(c.key)] ?? 0), undefined, 2) : label(r[String(c.key)])),
          }))} />
          {Boolean(result.truncated) && <div className="oc-small oc-muted">First rows only (BI query budget)</div>}
        </Card>
      )}
    </div>
  );
}

// ── route registries ─────────────────────────────────────────────────────

/** Back Office routes: Reports → Scheduled Reports, Report Builder; KPI targets also reachable from Reports. */
export const BI_ROUTES: AreaRoute[] = [
  { path: '/reports/scheduled', perm: 'reporting.scheduled_report.view', element: <BIScheduledReportsPage /> },
  { path: '/reports/builder', perm: 'reporting.dataset.view', element: <BIReportBuilderPage /> },
  { path: '/reports/kpi-targets', perm: 'reporting.kpi_target.view', element: <BITargetsPage /> },
];

/** Management Dashboard routes (`/management/...`, registered in areas/management.tsx). */
export const BI_MANAGEMENT_ROUTES = [
  { path: 'drilldown', element: <RequirePermission perm="reporting.dashboard.view"><BIDrilldownPage /></RequirePermission> },
  { path: 'targets', element: <RequirePermission perm="reporting.kpi_target.view"><BITargetsPage /></RequirePermission> },
  { path: 'hr-performance', element: <RequirePermission perm="reporting.hr_performance.view"><BIHRPerformancePage /></RequirePermission> },
];

export const BI_OPS_TILES: OpsTile[] = [];
export const BI_OPS_ROUTES: OpsRoute[] = [];
