import React, { useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router';
import { qs, useGet } from '@oneclub/api-client';
import { currentLocale, formatDate, formatDateTime, formatNumber } from '@oneclub/i18n';
import {
  Amount, BarChart, BreakdownList, CircleButton, DASH_COLORS, DASH_OTHER, DashButton, DashCard, DashGrid, DashHead, DashIcon, DashName, DashStatusPill, DashTable, Delta, ErrorAlert, Gauge,
  HeatBars, Icon, MiniCard, Note, PillSelect, ProgressRow, PromoCard, ReportCard, SegmentBar, Skeleton, SplitStats, monthOptions, type DashStatus,
} from '@oneclub/shell';
import { moneyShort, today, type R } from '../p1/common';
import { DOMAIN_ICON, Freshness, fmtKPI, type Domain, type KPI, type Overview } from './bi';

// Management dashboards on the dashboard kit (dashboard overview design): the
// Executive Overview across domains and one dashboard per domain (golf, sport
// club, membership, booking, banquet, commercial, inventory, procurement,
// finance, CRM, HR). Figures, targets, previous month and the 13-month trend
// come from /api/v1/reporting/executive (analytics store on the read replica).

const API = '/api/v1/reporting';
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const n = (v?: string | null) => (v == null || v === '' ? null : Number(v));
const items = <T,>(v: unknown): T[] => (Array.isArray(v) ? (v as T[]) : []);
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');

/** A KPI value for a headline: money in short form, the rest by unit. */
export function headline(v: unknown, unit: string) {
  return unit === 'idr' ? moneyShort(v) : fmtKPI(v, unit);
}

const INDICATOR: Record<string, [DashStatus, string]> = {
  on_track: ['good', 'On Track'], watch: ['warn', 'Watch'], off_track: ['bad', 'Off Track'], no_target: ['neutral', 'No Target'],
};
const indicator = (k: KPI): [DashStatus, string] => (k.status === 'coming_soon' ? ['neutral', 'Coming soon'] : INDICATOR[k.indicator] ?? ['neutral', label(k.indicator)]);
const live = (k: KPI) => k.status !== 'coming_soon' && k.value != null;
/** "Lower is better" KPIs (costs, waiting time): a rise is bad. */
const inverse = (k: KPI) => k.direction === 'down' || k.direction === 'lower';

/** The month and period of a management dashboard, kept in the URL. */
function usePeriod() {
  const [params, setParams] = useSearchParams();
  const month = params.get('month') ?? today().slice(0, 7);
  const period = params.get('period') ?? 'month';
  const set = (k: string, v: string) => { const p = new URLSearchParams(params); p.set(k, v); setParams(p, { replace: true }); };
  return { month, period, params, set };
}

function MonthPill({ month, set }: { month: string; set: (k: string, v: string) => void }) {
  return <PillSelect label="Month" value={month} onChange={(v) => set('month', v)} options={monthOptions(currentLocale())} />;
}

/** Last six months of a KPI (grey), the chosen month highlighted with its target. */
function useTrend(kpi: string | undefined, month: string) {
  const t = useGet<R>(kpi ? `${API}/executive/trend${qs({ kpi, months: 13 })}` : null);
  const pts = items<R>(t.data?.points);
  const upto = pts.filter((p) => String(p.month).slice(0, 7) <= month).slice(-6);
  return upto.map((p, i) => ({
    label: MONTHS[Number(String(p.month).slice(5, 7)) - 1] ?? String(p.month),
    title: `${MONTHS[Number(String(p.month).slice(5, 7)) - 1]} ${String(p.month).slice(0, 4)}`,
    a: Number(p.value ?? 0), b: p.target == null ? null : Number(p.target),
    state: (i === upto.length - 1 ? 'current' : 'past') as 'current' | 'past',
  }));
}

/** Rows of the KPI table of a dashboard. */
function KPITable({ kpis, title = 'All KPIs', icon = 'monitoring' }: { kpis: KPI[]; title?: string; icon?: string }) {
  const nav = useNavigate();
  const { month } = usePeriod();
  const [q, setQ] = useState('');
  const rows = kpis.filter((k) => !q || k.label.toLowerCase().includes(q.toLowerCase()));
  return (
    <DashTable<KPI> title={title} icon={icon} rows={rows} rowKey={(k) => k.key} search={q} onSearch={setQ} empty="No KPI matches."
      onRow={(k) => live(k) && nav(`/management/drilldown${qs({ kpi: k.key, month })}`)} info={(k) => k.definition}
      columns={[
        { key: 'label', header: 'Name', render: (k) => <DashName icon={DOMAIN_ICON[k.dashboard] ?? 'insights'} name={k.label} sub={label(k.kind)} /> },
        { key: 'value', header: 'Value', render: (k) => <strong className="oc-dash-num">{live(k) ? fmtKPI(k.value, k.unit) : '—'}</strong> },
        { key: 'target', header: 'Target', render: (k) => <span className="oc-dash-num">{k.target != null ? fmtKPI(k.targetToDate ?? k.target, k.unit) : '—'}</span> },
        { key: 'previous', header: 'Last month', render: (k) => {
          const c = n(k.previousChange);
          return <span className="oc-dash-num">{fmtKPI(k.previous, k.unit)}{c != null && <Delta ratio={c} chip="" inverse={inverse(k)} />}</span>;
        } },
        { key: 'status', header: 'Status', align: 'center', render: (k) => { const [t, l] = indicator(k); return <DashStatusPill tone={t}>{l}</DashStatusPill>; } },
      ]} />
  );
}

/**
 * One domain on the dashboard kit: the lead KPI as the hero, the next two
 * as the income / expense cards, targets as gauge and goals, the lead KPI's
 * six months as the chart, every KPI in the table.
 */
export function DomainDashboard({ domain, title, extra }: { domain: string; title?: string; extra?: React.ReactNode }) {
  const { month, set } = usePeriod();
  const d = useGet<Overview>(`${API}/executive${qs({ period: 'month', month })}`);
  const dm: Domain | undefined = d.data?.domains.find((x) => x.code === domain);
  const kpis = dm?.kpis ?? [];
  const ready = kpis.filter(live);
  const lead = ready.find((k) => k.unit === 'idr') ?? ready[0];
  const others = ready.filter((k) => k !== lead);
  const [second, third, fourth] = others;
  const trend = useTrend(lead?.key, month);
  const name = title ?? (dm ? `${dm.label} Performance` : 'Performance');
  const pill = <MonthPill month={month} set={set} />;
  const icon = DOMAIN_ICON[domain] ?? 'insights';
  if (d.error) return <div className="oc-dash-page"><DashHead title={name} /><ErrorAlert error={d.error} /></div>;
  if (!d.data) return <div className="oc-dash-page"><DashHead title={name} /><Skeleton rows={8} /></div>;
  const ov = d.data;
  const sub = `${formatDate(ov.from)} – ${formatDate(ov.to)}${ov.dataAsOf ? ` · data as of ${formatDateTime(ov.dataAsOf)}` : ''}`;
  if (!dm || !lead) {
    return (
      <div className="oc-dash-page">
        <DashHead title={name} sub={sub} controls={pill} />
        <DashGrid>
          <DashCard span={12} icon={icon} title={dm?.label ?? name}>
            <p className="oc-dash-empty">No figure for this month yet: the KPIs fill once the analytics store has data of the module.</p>
          </DashCard>
          {extra}
        </DashGrid>
      </div>
    );
  }
  const drill = (k: KPI) => `/management/drilldown${qs({ kpi: k.key, from: ov.from, to: ov.to })}`;
  const leadChange = n(lead.previousChange);
  const breakdown = items<{ label: string; value: string }>((third ?? lead).breakdown);
  const total = breakdown.reduce((t, b) => t + Number(b.value), 0);
  // Every category in the list is also a part of the bar: the five largest, the rest as Other.
  const restSum = breakdown.slice(5).reduce((t, b) => t + Number(b.value), 0);
  const cats = [...breakdown.slice(0, 5).map((b, i) => ({ label: label(b.label), amount: Number(b.value), color: DASH_COLORS[i] })),
    ...(restSum > 0 ? [{ label: 'Other', amount: restSum, color: DASH_OTHER }] : [])];
  const parts = cats.map((c) => ({ label: c.label, value: c.amount, color: c.color }));
  const targeted = others.filter((k) => n(k.achievement) != null);
  const offTrack = kpis.filter((k) => k.indicator === 'off_track').length;
  return (
    <div className="oc-dash-page">
      <DashHead title={name} sub={sub} controls={pill} />
      <DashGrid>
        <DashCard span={5} icon={icon} title={lead.label} controls={pill}>
          <div className="oc-dash-hero">
            <div>
              <span title={fmtKPI(lead.value, lead.unit)}><Amount text={headline(lead.value, lead.unit)} size="hero" /></span>
              <Delta ratio={leadChange} inverse={inverse(lead)} text={`vs last month${lead.indicator === 'on_track' ? ', good progress.' : '.'}`} />
            </div>
            <HeatBars values={trend.map((p) => p.a)} />
          </div>
          <div className="oc-dash-actions">
            <DashButton tone="blue" icon="insights" to={drill(lead)}>Drill down</DashButton>
            <DashButton tone="dark" icon="target" to="/management/targets">Targets</DashButton>
            <DashButton tone="grey" icon="description" to={`/reports?module=${dm.module || domain}`}>Reports</DashButton>
          </div>
        </DashCard>
        {second ? (
          <DashCard span={3} icon="arrow_downward" tone="green" title={second.label} action={<CircleButton arrow label="Drill down" to={drill(second)} />}>
            <div className="oc-dash-figure">
              <span title={fmtKPI(second.value, second.unit)}><Amount text={headline(second.value, second.unit)} /></span>
              <Delta ratio={n(second.previousChange)} chip="" inverse={inverse(second)} />
            </div>
            {n(second.previousChange) != null && (
              <Note tone={(n(second.previousChange)! >= 0) !== inverse(second) ? 'good' : 'bad'}>
                {second.label} {n(second.previousChange)! >= 0 ? 'increased' : 'decreased'} by <b>{Math.abs(n(second.previousChange)! * 100).toFixed(1)}%</b> from last month.
              </Note>
            )}
            <SplitStats items={[
              { label: 'Target', value: second.target != null ? headline(second.targetToDate ?? second.target, second.unit) : '—', color: 'var(--dash-blue)' },
              { label: 'Year to date', value: second.ytd != null ? headline(second.ytd, second.unit) : '—', color: 'var(--dash-lime)' },
            ]} />
          </DashCard>
        ) : <DashCard span={3} icon="insights" title="More KPIs"><p className="oc-dash-sub">This domain reports one KPI.</p></DashCard>}
        <DashCard span={4}>
          <div className="oc-dash-inset">
            <div className="oc-dash-card-head">
              <DashIcon name="arrow_upward" tone="red" /><h2>{third?.label ?? 'Breakdown'}</h2><span className="oc-spacer" />{pill}
            </div>
            {third ? (
              <>
                <span title={fmtKPI(third.value, third.unit)}><Amount text={headline(third.value, third.unit)} /></span>
                <Delta ratio={n(third.previousChange)} chip="" inverse={inverse(third)} suffix="vs last month" />
              </>
            ) : <span className="oc-dash-sub">{lead.label} by its main parts</span>}
          </div>
          {parts.length > 0 ? (
            <>
              <SegmentBar parts={parts} legend={false} format={(v) => headline(v, (third ?? lead).unit)} />
              <BreakdownList rows={cats.map((c) => ({ label: c.label, value: headline(c.amount, (third ?? lead).unit), share: total ? c.amount / total : null,
                color: c.color, to: drill(third ?? lead) }))} />
            </>
          ) : <p className="oc-dash-sub">No breakdown for this KPI.</p>}
        </DashCard>

        <DashCard span={4} icon="target" title="Targets" action={<CircleButton icon="tune" label="KPI targets" to="/management/targets" dot={offTrack > 0} />}>
          {lead.target == null ? (
            <p className="oc-dash-empty">No approved target for {lead.label}: set the KPI targets of the year in KPI Targets.</p>
          ) : (
            <Gauge title={lead.label} ratio={n(lead.achievement)} caption="Actual" value={headline(lead.value, lead.unit)}
              sub={<>/ {headline(lead.targetToDate ?? lead.target, lead.unit)} {lead.indicator === 'on_track' && <Icon name="check_circle" size={14} />}</>} />
          )}
          <div className="oc-dash-list">
            {targeted.slice(0, 3).map((k) => <ProgressRow key={k.key} label={k.label} ratio={n(k.achievement)} to={drill(k)} hint={k.definition} />)}
          </div>
        </DashCard>
        <DashCard span={4} icon="bar_chart" title={`${lead.label} trend`} controls={pill}>
          {trend.length === 0 ? <p className="oc-dash-empty">No history yet.</p> : (
            <BarChart points={trend} aLabel="Actual" bLabel="Target" axis={lead.unit === 'idr' ? 'Amount (IDR)' : lead.label} format={(v) => headline(v, lead.unit)} />
          )}
        </DashCard>
        <div className="oc-dash-stack" data-span="4">
          <PromoCard span={4} badge={offTrack ? `${offTrack} off track` : '13-month trend'} title={`Explore ${dm.label} by dimension`} cta="Drill down" to={drill(lead)} />
          {fourth ? (
            <MiniCard label={fourth.label} to={drill(fourth)} value={<Amount text={headline(fourth.value, fourth.unit)} size="sm" />}
              delta={<Delta ratio={n(fourth.previousChange)} chip="" inverse={inverse(fourth)} />} />
          ) : <MiniCard label="KPIs" value={formatNumber(kpis.length)} to="/management/targets" />}
          <ReportCard label="Track & Print Report" title={`${dm.label} Reports`} to={`/reports?module=${dm.module || domain}`} />
        </div>
        <KPITable kpis={kpis} />
        {extra}
      </DashGrid>
    </div>
  );
}

const avg = (xs: number[]) => (xs.length ? xs.reduce((a, b) => a + b, 0) / xs.length : null);

/** Executive Overview with targets (FR-BI-02/03/08), the Management Dashboard home, on the dashboard kit. */
export function ExecutiveDashboard() {
  const nav = useNavigate();
  const { month, period, params, set } = usePeriod();
  const view = params.get('view') ?? 'domains';
  const d = useGet<Overview>(`${API}/executive${qs({ period, month })}`);
  const today = useGet<R>(`${API}/dashboards/executive-overview`);
  const [q, setQ] = useState('');
  const ov = d.data;
  const controls = <>
    <PillSelect label="Period" value={period} onChange={(v) => set('period', v)} options={[{ value: 'month', label: 'Month' }, { value: 'year', label: 'Year to date' }]} />
    <PillSelect label="View" value={view} onChange={(v) => set('view', v)}
      options={[{ value: 'domains', label: 'Domains' }, { value: 'properties', label: 'By property' }, { value: 'today', label: 'Today' }]} />
    <MonthPill month={month} set={set} />
  </>;
  const all = (ov?.domains ?? []).flatMap((x) => x.kpis);
  const lead = all.find((k) => k.dashboard === 'finance' && k.unit === 'idr' && live(k)) ?? all.find((k) => k.unit === 'idr' && live(k)) ?? all.find(live);
  const trend = useTrend(lead?.key, month);
  if (d.error) return <div className="oc-dash-page"><DashHead title="Executive Overview" controls={controls} /><ErrorAlert error={d.error} /></div>;
  if (!ov) return <div className="oc-dash-page"><DashHead title="Executive Overview" controls={controls} /><Skeleton rows={8} /></div>;
  const counted = all.filter(live);
  const by = (i: string) => counted.filter((k) => k.indicator === i).length;
  const onTrack = by('on_track');
  const targeted = counted.filter((k) => n(k.achievement) != null);
  const overall = avg(targeted.map((k) => Number(k.achievement)));
  const domains = ov.domains.map((dm) => {
    const ks = dm.kpis.filter(live);
    const ach = avg(ks.filter((k) => n(k.achievement) != null).map((k) => Number(k.achievement)));
    const worst = ks.some((k) => k.indicator === 'off_track') ? 'off_track' : ks.some((k) => k.indicator === 'watch') ? 'watch' : ks.some((k) => k.indicator === 'on_track') ? 'on_track' : 'no_target';
    return { ...dm, id: dm.code, ach, worst, on: ks.filter((k) => k.indicator === 'on_track').length, count: ks.length };
  });
  const widgets = items<R>(today.data?.widgets).filter((w) => w.status === 'available');
  const sub = `${formatDate(ov.from)} – ${formatDate(ov.to)}${ov.targetPlan ? ` · target plan ${ov.targetPlan.year} v${ov.targetPlan.version}` : ' · no approved target plan'}`;
  const drill = (k: KPI) => `/management/drilldown${qs({ kpi: k.key, from: ov.from, to: ov.to })}`;
  return (
    <div className="oc-dash-page">
      <DashHead title="Executive Overview" sub={sub} controls={controls} />
      <Freshness ov={ov} onRefreshed={() => d.refetch()} />
      {view === 'properties' && <PropertyTable month={month} />}
      {view === 'today' && (
        <DashGrid>
          {widgets.length === 0 && <DashCard span={12} icon="today" title="Today"><p className="oc-dash-empty">No live figure today.</p></DashCard>}
          {widgets.map((w) => <MiniCard key={String(w.key)} span={3} label={String(w.label)} value={<Amount text={formatNumber(Number(w.value ?? 0))} size="md" />} />)}
        </DashGrid>
      )}
      {view === 'domains' && (
        <DashGrid>
          <DashCard span={5} icon="insights" title={lead?.label ?? 'Overview'} controls={<MonthPill month={month} set={set} />}>
            <div className="oc-dash-hero">
              <div>
                {lead ? <Amount text={headline(lead.value, lead.unit)} size="hero" /> : <Amount text="—" size="hero" muted />}
                {lead && <Delta ratio={n(lead.previousChange)} text={`vs last month${lead.indicator === 'on_track' ? ', good progress.' : '.'}`} />}
              </div>
              <HeatBars values={trend.map((p) => p.a)} />
            </div>
            <div className="oc-dash-actions">
              {lead && <DashButton tone="blue" icon="insights" to={drill(lead)}>Drill down</DashButton>}
              <DashButton tone="dark" icon="target" to="/management/targets">KPI targets</DashButton>
              <DashButton tone="grey" icon="apartment" onClick={() => set('view', 'properties')}>By property</DashButton>
            </div>
          </DashCard>
          <DashCard span={3} icon="check_circle" tone="green" title="On Track">
            <div className="oc-dash-figure">
              <span className="oc-dash-amount" data-size="lg">{onTrack}<small>/{counted.length}</small></span>
              {counted.length > 0 && <span className="oc-dash-chip" data-good={onTrack / counted.length >= 0.5}>{Math.round((onTrack / counted.length) * 100)}%</span>}
            </div>
            <Note tone={onTrack >= counted.length / 2 ? 'good' : 'bad'}><b>{onTrack}</b> of {counted.length} KPIs are on track this {period === 'year' ? 'year' : 'month'}.</Note>
            <SplitStats items={[
              { label: 'Watch', value: by('watch'), color: 'var(--dash-blue)' },
              { label: 'No target', value: by('no_target'), color: 'var(--dash-lime)' },
            ]} />
          </DashCard>
          <DashCard span={4}>
            <div className="oc-dash-inset">
              <div className="oc-dash-card-head"><DashIcon name="trending_down" tone="red" /><h2>Off Track</h2><span className="oc-spacer" /><MonthPill month={month} set={set} /></div>
              <span className="oc-dash-amount" data-size="lg">{by('off_track')}</span>
              <span className="oc-dash-delta-row"><span className="oc-dash-chip" data-good={by('off_track') === 0}>{by('off_track')} KPIs</span>
                <span className="oc-dash-chip-suffix">below the target to date</span></span>
            </div>
            <SegmentBar format={(v) => `${v} KPIs`} parts={[
              { label: 'On track', value: onTrack, color: 'var(--dash-green)' }, { label: 'Watch', value: by('watch'), color: 'var(--dash-amber)' },
              { label: 'Off track', value: by('off_track'), color: 'var(--dash-red)' }, { label: 'No target', value: by('no_target'), color: 'var(--dash-grey)' },
            ]} />
          </DashCard>

          <DashCard span={4} icon="target" title="Targets" action={<CircleButton icon="tune" label="KPI targets" to="/management/targets" dot={by('off_track') > 0} />}>
            <Gauge title="Average achievement" ratio={overall} caption="Achieved" value={overall == null ? '—' : `${Math.round(overall * 100)}%`}
              sub={<>/ 100% of target {overall != null && overall >= 1 && <Icon name="check_circle" size={14} />}</>} />
            <div className="oc-dash-list">
              {domains.filter((x) => x.ach != null).sort((a, b) => (a.ach ?? 0) - (b.ach ?? 0)).slice(0, 3)
                .map((x) => <ProgressRow key={x.code} label={x.label} ratio={x.ach} to={x.dashboardPath} />)}
            </div>
          </DashCard>
          <DashCard span={4} icon="bar_chart" title={lead ? `${lead.label} trend` : 'Trend'} controls={<MonthPill month={month} set={set} />}>
            {!lead || trend.length === 0 ? <p className="oc-dash-empty">No history yet.</p> : (
              <BarChart points={trend} aLabel="Actual" bLabel="Target" axis={lead.unit === 'idr' ? 'Amount (IDR)' : lead.label} format={(v) => headline(v, lead.unit)} />
            )}
          </DashCard>
          <div className="oc-dash-stack" data-span="4">
            <PromoCard span={4} badge={ov.stale ? 'Data out of date' : ov.dataAsOf ? `Data ${formatDateTime(ov.dataAsOf)}` : 'Not refreshed yet'}
              title="Today's live figures" cta="Open today" to="/management?view=today" />
            <MiniCard label={String(widgets[0]?.label ?? 'Today')} value={<Amount text={formatNumber(Number(widgets[0]?.value ?? 0))} size="sm" />}
              to="/management?view=today" />
            <ReportCard label="Track & Print Report" title="Scheduled Reports" to="/reports/scheduled" />
          </div>

          <DashTable<(typeof domains)[number]> title="Domains" icon="dashboard" rows={domains.filter((x) => !q || x.label.toLowerCase().includes(q.toLowerCase()))}
            rowKey={(x) => x.code} search={q} onSearch={setQ} onRow={(x) => nav(x.dashboardPath)} info={(x) => `Open the ${x.label} dashboard`}
            columns={[
              { key: 'label', header: 'Name', render: (x) => <DashName icon={DOMAIN_ICON[x.code] ?? 'insights'} name={x.label} /> },
              { key: 'kpis', header: 'KPIs', render: (x) => <span className="oc-dash-num">{x.count}</span> },
              { key: 'on', header: 'On track', render: (x) => <span className="oc-dash-num">{x.on} / {x.count}</span> },
              { key: 'ach', header: 'Achievement', render: (x) => <strong className="oc-dash-num">{x.ach == null ? '—' : `${Math.round(x.ach * 100)}%`}</strong> },
              { key: 'status', header: 'Status', align: 'center', render: (x) => { const [t, l] = INDICATOR[x.worst] ?? ['neutral', label(x.worst)]; return <DashStatusPill tone={t}>{l}</DashStatusPill>; } },
            ]} />
        </DashGrid>
      )}
    </div>
  );
}

/** Each KPI per property with its target (FR-BI-08). */
function PropertyTable({ month }: { month: string }) {
  const d = useGet<R>(`${API}/executive/properties${qs({ month })}`);
  const [q, setQ] = useState('');
  if (!d.data) return <><ErrorAlert error={d.error} /><Skeleton rows={6} /></>;
  const props = items<R>(d.data.properties);
  const rows = items<R>(d.data.kpis).filter((k) => !q || String(k.label).toLowerCase().includes(q.toLowerCase()));
  return (
    <DashGrid>
      <DashTable<R> title="By Property" icon="apartment" rows={rows} rowKey={(k) => String(k.key)} search={q} onSearch={setQ}
        columns={[
          { key: 'label', header: 'KPI', render: (k) => <DashName icon={DOMAIN_ICON[String(k.domain)] ?? 'insights'} name={String(k.label)} sub={label(k.domain)} /> },
          ...props.map((p) => ({ key: String(p.id), header: String(p.name), render: (k: R) => {
            const v = items<R>(k.values).find((x) => x.propertyId === p.id);
            return <span className="oc-dash-num">{fmtKPI(v?.value, String(k.unit))}{v?.target != null && <span className="oc-dash-sub"> / {fmtKPI(v.target, String(k.unit))}</span>}</span>;
          } })),
        ]} />
    </DashGrid>
  );
}

/** Live golf operation of the day (golf-today widgets) under the Golf dashboard. */
export function GolfToday() {
  const d = useGet<{ widgets: R[] }>(`${API}/dashboards/golf-today?date=${today()}`);
  const widgets = d.data?.widgets ?? [];
  return (
    <>
      {widgets.map((w) => (
        <MiniCard key={String(w.key)} span={3} label={String(w.label)}
          value={<Amount text={`${formatNumber(Number(w.value ?? 0))}${w.key === 'avg_check_in_to_tee_off' ? ' min' : ''}`} size="md" />} />
      ))}
    </>
  );
}
