import React, { useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router';
import { qs, useGet, type Schemas } from '@oneclub/api-client';
import { currentLocale, formatDate, formatDateTime, formatNumber } from '@oneclub/i18n';
import {
  Amount, BreakdownList, CircleButton, ColumnChart, DASH_COLORS, DASH_OTHER, DashButton, DashCard, DashGrid, DashHead, DashIcon, DashName, DashStatusPill,
  DashTable, Delta, ErrorAlert, Gauge, HeatBars, MiniCard, Note, PillSelect, RankBars, ReportCard, SegmentBar, Skeleton, SplitStats, monthOptions,
  type DashStatus,
} from '@oneclub/shell';
import { money, moneyShort, today } from './p1/common';

// Management › Profit Centers and Incidents on the dashboard kit. Figures
// come from /api/v1/reporting/profit-centers (general ledger per profit
// center) and /api/v1/reporting/incidents (caddy, golf cart and banquet
// incidents), for the month to date or the year to date.

type Centers = Schemas['ProfitCenters'];
type Center = Schemas['ProfitCenter'];
type Incidents = Schemas['IncidentDashboard'];
type Incident = Schemas['IncidentItem'];

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const n = (v?: string | null) => (v == null || v === '' ? null : Number(v));
const pct = (v?: string | number | null) => (v == null || v === '' ? '—' : `${(Number(v) * 100).toFixed(1)}%`);
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const monthLabel = (m: string) => MONTHS[Number(m.slice(5, 7)) - 1] ?? m;

const CENTER_ICON: Record<string, string> = {
  golf: 'golf_course', resto: 'restaurant', sportclub: 'sports_tennis', bungalow: 'cottage', wedding: 'favorite', event_mice: 'groups',
  vip_suite: 'workspace_premium', shared: 'apartment',
};

/** Period (month / year to date) and month of the page, kept in the URL. */
function usePeriod() {
  const [params, setParams] = useSearchParams();
  const month = params.get('month') ?? today().slice(0, 7);
  const period = params.get('period') ?? 'month';
  const set = (k: string, v: string) => { const p = new URLSearchParams(params); p.set(k, v); setParams(p, { replace: true }); };
  const controls = <>
    <PillSelect label="Period" value={period} onChange={(v) => set('period', v)} options={[{ value: 'month', label: 'Month' }, { value: 'year', label: 'Year to date' }]} />
    <PillSelect label="Month" value={month} onChange={(v) => set('month', v)} options={monthOptions(currentLocale())} />
  </>;
  return { month, period, params, set, controls };
}

/** Margin of a center: healthy, thin, low or a loss. */
function marginStatus(c: Center): [DashStatus, string] {
  const m = n(c.margin);
  if (m == null) return ['neutral', 'No revenue'];
  if (Number(c.contribution) < 0) return ['bad', 'Loss'];
  if (m >= 0.4) return ['good', 'Healthy'];
  if (m >= 0.15) return ['warn', 'Thin'];
  return ['bad', 'Low'];
}

/** Profit Centers: revenue, costs and contribution per center, overhead and net income. */
export function ProfitCentersDashboard() {
  const nav = useNavigate();
  const { month, period, params, set, controls } = usePeriod();
  const d = useGet<Centers>(`/api/v1/reporting/profit-centers${qs({ period, month })}`);
  const [q, setQ] = useState('');
  const title = 'Profit Centers';
  if (d.error) return <div className="oc-dash-page"><DashHead title={title} controls={controls} /><ErrorAlert error={d.error} /></div>;
  if (!d.data) return <div className="oc-dash-page"><DashHead title={title} controls={controls} /><Skeleton rows={8} /></div>;
  const pc = d.data;
  const centers = pc.centers.filter((c) => !c.shared);
  const shared = pc.centers.find((c) => c.shared);
  const sub = `${formatDate(pc.from)} – ${formatDate(pc.to)} · compared with ${formatDate(pc.compareFrom)} – ${formatDate(pc.compareTo)}`;
  if (centers.length === 0) {
    return (
      <div className="oc-dash-page">
        <DashHead title={title} sub={sub} controls={controls} />
        <DashGrid><DashCard span={12} icon="account_tree" title={title}><p className="oc-dash-empty">No posted revenue or cost in this period yet.</p></DashCard></DashGrid>
      </div>
    );
  }
  const selected = centers.find((c) => c.code === params.get('center')) ?? [...centers].sort((a, b) => Number(b.revenue) - Number(a.revenue))[0];
  const totalTrend = selected.trend.map((_, i) => centers.reduce((s, c) => s + Number(c.trend[i]?.contribution ?? 0), 0));
  const contributionChange = Number(pc.previousContribution) === 0 ? null : (Number(pc.contribution) - Number(pc.previousContribution)) / Math.abs(Number(pc.previousContribution));
  const netChange = Number(pc.previousNetIncome) === 0 ? null : (Number(pc.netIncome) - Number(pc.previousNetIncome)) / Math.abs(Number(pc.previousNetIncome));
  const revenuePrev = centers.reduce((s, c) => s + Number(c.previousRevenue), 0);
  const revenueChange = revenuePrev === 0 ? null : (Number(pc.revenue) - revenuePrev) / revenuePrev;
  const costs = centers.reduce((s, c) => s + Number(c.costOfSales) + Number(c.directExpense), 0);
  // Revenue mix: the five largest centers and the rest as Other (every row is also a part of the bar).
  const byRevenue = [...centers].sort((a, b) => Number(b.revenue) - Number(a.revenue));
  const rest = byRevenue.slice(5).reduce((s, c) => s + Number(c.revenue), 0);
  const mix = [...byRevenue.slice(0, 5).map((c, i) => ({ code: c.code, label: c.label, amount: Number(c.revenue), color: DASH_COLORS[i] })),
    ...(rest > 0 ? [{ code: '', label: 'Other', amount: rest, color: DASH_OTHER }] : [])];
  const pick = (code: string) => set('center', code);
  const trend = selected.trend.map((t, i) => ({
    label: monthLabel(t.month), title: `${monthLabel(t.month)} ${t.month.slice(0, 4)}`, a: Number(t.revenue), b: Math.max(0, Number(t.contribution)),
    state: (i === selected.trend.length - 1 ? 'current' : 'past') as 'current' | 'past',
  }));
  const ranked = [...centers].sort((a, b) => Number(b.contribution) - Number(a.contribution));
  const accounts = pc.accounts.filter((a) => a.profitCenter === selected.code);
  const SECTION: Record<string, string> = { revenue: 'Revenue', cogs: 'Cost of Sales', expense: 'Direct Expense', other: 'Other' };
  return (
    <div className="oc-dash-page">
      <DashHead title={title} sub={sub} controls={controls} />
      <DashGrid>
        <DashCard span={5} icon="account_tree" title="Contribution of the centers">
          <div className="oc-dash-hero">
            <div>
              <span title={money(pc.contribution)}><Amount text={moneyShort(pc.contribution)} size="hero" /></span>
              <Delta ratio={contributionChange} text={`vs ${period === 'year' ? 'last year' : 'last month'} to date.`} />
            </div>
            <HeatBars values={totalTrend} />
          </div>
          <div className="oc-dash-actions">
            <DashButton tone="blue" icon="description" to="/reports/accounting.profit_center">Report</DashButton>
            <DashButton tone="dark" icon="payments" to="/management/financial">Financial</DashButton>
            <DashButton tone="grey" icon="balance" to="/reports/accounting.profit_loss">P&amp;L</DashButton>
          </div>
        </DashCard>
        <DashCard span={3} icon="arrow_downward" tone="green" title="Revenue">
          <div className="oc-dash-figure">
            <span title={money(pc.revenue)}><Amount text={moneyShort(pc.revenue)} /></span>
            <Delta ratio={revenueChange} chip="" />
          </div>
          {revenueChange != null && (
            <Note tone={revenueChange >= 0 ? 'good' : 'bad'}>
              Revenue {revenueChange >= 0 ? 'increased' : 'decreased'} by <b>{Math.abs(revenueChange * 100).toFixed(1)}%</b> from the same days {period === 'year' ? 'last year' : 'last month'}.
            </Note>
          )}
          <SplitStats items={[
            { label: 'Margin', value: pct(pc.margin), color: 'var(--dash-blue)' },
            { label: 'Direct costs', value: moneyShort(costs), color: 'var(--dash-lime)' },
          ]} />
        </DashCard>
        <DashCard span={4}>
          <div className="oc-dash-inset">
            <div className="oc-dash-card-head"><DashIcon name="pie_chart" tone="red" /><h2>Revenue mix</h2></div>
            <span title={money(byRevenue[0].revenue)}><Amount text={byRevenue[0].label} /></span>
            <span className="oc-dash-delta-row"><span className="oc-dash-chip" data-good>{pct(byRevenue[0].revenueShare)}</span>
              <span className="oc-dash-chip-suffix">of the revenue, the largest center</span></span>
          </div>
          <SegmentBar parts={mix.map((m) => ({ label: m.label, value: m.amount, color: m.color }))} legend={false} format={moneyShort} />
          <BreakdownList rows={mix.map((m) => ({ label: m.label, value: moneyShort(m.amount), share: Number(pc.revenue) ? m.amount / Number(pc.revenue) : null, color: m.color,
            to: m.code ? `/management/profit-centers${qs({ period, month, center: m.code })}` : undefined }))} />
        </DashCard>

        <DashCard span={4} icon="percent" title="Net income" action={<CircleButton arrow label="Profit & Loss" to="/reports/accounting.profit_loss" />}>
          <Gauge title="Contribution margin" ratio={n(pc.margin)} caption="Margin" value={pct(pc.margin)} sub={<>of {moneyShort(pc.revenue)} revenue</>} />
          <SplitStats items={[
            { label: 'Contribution', value: moneyShort(pc.contribution), color: 'var(--dash-blue)' },
            { label: 'Shared & other', value: moneyShort(pc.sharedNet), color: 'var(--dash-lime)' },
          ]} />
          <Note tone={Number(pc.netIncome) >= 0 ? 'good' : 'bad'}>
            Net income <b title={money(pc.netIncome)}>{moneyShort(pc.netIncome)}</b>
            {netChange != null && <> ({netChange >= 0 ? '+' : '−'}{Math.abs(netChange * 100).toFixed(1)}%)</>} after the shared overhead.
          </Note>
        </DashCard>
        <DashCard span={4} icon="bar_chart" title="6-month trend"
          controls={<PillSelect label="Center" value={selected.code} onChange={pick} options={centers.map((c) => ({ value: c.code, label: c.label }))} />}>
          <ColumnChart points={trend} aLabel="Revenue" bLabel="Contribution" axis="Amount (IDR)" format={moneyShort} />
        </DashCard>
        <div className="oc-dash-stack" data-span="4">
          <MiniCard label={`${selected.label} contribution`} value={<Amount text={moneyShort(selected.contribution)} size="sm" />}
            delta={<Delta ratio={n(selected.contributionChange)} chip="" />} />
          <MiniCard label="Shared & other (net)" value={<Amount text={moneyShort(pc.sharedNet)} size="sm" />}
            delta={shared ? <span className="oc-dash-sub">{moneyShort(shared.directExpense)} expenses · {moneyShort(shared.revenue)} income</span> : undefined} />
          <ReportCard label="Track & Print Report" title="Profit Center Report" to="/reports/accounting.profit_center" />
        </div>

        <DashCard span={5} icon="leaderboard" title="Contribution by center">
          <RankBars format={moneyShort} legend={[{ label: 'Contribution', color: 'var(--dash-green)' }, { label: 'Cost of sales', color: 'var(--dash-red)' },
            { label: 'Direct expense', color: 'var(--dash-amber)' }]}
            rows={ranked.map((c, i) => ({ key: c.code, rank: i + 1, name: c.label, value: moneyShort(c.contribution), onOpen: () => pick(c.code),
              total: Number(c.revenue), parts: [
                { label: 'Contribution', value: Math.max(0, Number(c.contribution)), color: 'var(--dash-green)' },
                { label: 'Cost of sales', value: Number(c.costOfSales), color: 'var(--dash-red)' },
                { label: 'Direct expense', value: Number(c.directExpense), color: 'var(--dash-amber)' },
              ] }))} />
        </DashCard>
        <DashTable<Center> span={7} title="Profit centers" icon="account_tree" rowKey={(c) => c.code} search={q} onSearch={setQ}
          rows={pc.centers.filter((c) => !q || c.label.toLowerCase().includes(q.toLowerCase()))} onRow={(c) => !c.shared && pick(c.code)}
          info={(c) => (c.shared ? 'Salaries, utilities, depreciation, breakage and other income / expenses not booked to a center' : `Show the accounts of ${c.label}`)}
          columns={[
            { key: 'label', header: 'Center', render: (c) => <DashName icon={CENTER_ICON[c.code] ?? 'account_tree'} name={c.label} sub={c.shared ? 'overhead' : pct(c.revenueShare)} tone={c.code === selected.code ? 'blue' : undefined} /> },
            { key: 'revenue', header: 'Revenue', align: 'right', render: (c) => <span className="oc-dash-num" title={money(c.revenue)}>{moneyShort(c.revenue)}</span> },
            { key: 'costs', header: 'Costs', align: 'right', render: (c) => <span className="oc-dash-num" title={`Cost of sales ${money(c.costOfSales)} · direct expense ${money(c.directExpense)}`}>{moneyShort(Number(c.costOfSales) + Number(c.directExpense))}</span> },
            { key: 'contribution', header: 'Contribution', align: 'right', render: (c) => <strong className="oc-dash-num" title={money(c.contribution)}>{moneyShort(c.contribution)}</strong> },
            { key: 'margin', header: 'Margin', align: 'right', render: (c) => <span className="oc-dash-num">{pct(c.margin)}</span> },
            { key: 'change', header: 'vs prev.', render: (c) => <Delta ratio={n(c.contributionChange)} chip="" /> },
            { key: 'status', header: 'Status', align: 'center', render: (c) => { if (c.shared) return <DashStatusPill tone="neutral">Shared</DashStatusPill>; const [t, l] = marginStatus(c); return <DashStatusPill tone={t}>{l}</DashStatusPill>; } },
          ]} />
        <DashTable<Schemas['ProfitCenterAccount']> title={`${selected.label} · accounts`} icon="list_alt" rows={accounts} rowKey={(a) => `${a.section}-${a.account}`}
          empty="No posting for this center in the period." onRow={() => nav(`/reports/accounting.general_ledger${qs({ from: pc.from, to: pc.to })}`)}
          columns={[
            { key: 'section', header: 'Section', render: (a) => SECTION[a.section] ?? label(a.section) },
            { key: 'account', header: 'Account', render: (a) => <DashName icon={a.section === 'revenue' ? 'arrow_downward' : 'arrow_upward'} name={a.accountName} sub={a.account}
              tone={a.section === 'revenue' ? 'green' : 'red'} /> },
            { key: 'amount', header: 'Amount', align: 'right', render: (a) => <strong className="oc-dash-num">{money(a.amount)}</strong> },
          ]} />
      </DashGrid>
    </div>
  );
}

const SOURCE: Record<string, [string, string]> = { caddy: ['Caddy', 'person'], golf_cart: ['Golf Cart', 'electric_car'], banquet: ['Banquet Event', 'celebration'] };
const SEVERITY_COLOR: Record<string, string> = { critical: 'var(--dash-red)', high: 'var(--dash-amber)', medium: 'var(--dash-blue)', low: 'var(--dash-sky)' };
const SEVERITY_TONE: Record<string, DashStatus> = { critical: 'bad', high: 'bad', medium: 'warn', low: 'neutral' };
const STATUS_TONE: Record<string, [DashStatus, string]> = { open: ['warn', 'Open'], closed: ['good', 'Closed'], logged: ['neutral', 'Logged'] };
const sourceLabel = (s: string) => SOURCE[s]?.[0] ?? label(s);
const daysAgo = (iso: string) => Math.max(0, Math.floor((Date.now() - new Date(iso).getTime()) / 86_400_000));
/** Back Office list of an incident (golf incidents are closed there). */
const incidentPath = (i: Incident) => (i.source === 'caddy' ? '/golf/operations/caddy-incidents' : i.source === 'golf_cart' ? '/golf/operations/cart-incidents' : undefined);

/** Incidents: caddy, golf cart and banquet incidents of the period, what is still open. */
export function IncidentsDashboard() {
  const nav = useNavigate();
  const { month, period, controls } = usePeriod();
  const d = useGet<Incidents>(`/api/v1/reporting/incidents${qs({ period, month })}`);
  const [q, setQ] = useState('');
  const [source, setSource] = useState('all');
  const title = 'Incidents';
  if (d.error) return <div className="oc-dash-page"><DashHead title={title} controls={controls} /><ErrorAlert error={d.error} /></div>;
  if (!d.data) return <div className="oc-dash-page"><DashHead title={title} controls={controls} /><Skeleton rows={8} /></div>;
  const x = d.data;
  const sub = `${formatDate(x.from)} – ${formatDate(x.to)} · updated ${formatDateTime(x.generatedAt)}`;
  const totals = x.trend.map((t) => t.caddy + t.golfCart + t.banquet);
  const trend = x.trend.map((t, i) => ({
    label: monthLabel(t.month), title: `${monthLabel(t.month)} ${t.month.slice(0, 4)} · caddy ${t.caddy}, golf cart ${t.golfCart}, banquet ${t.banquet}`,
    a: totals[i], b: t.caddy, state: (i === x.trend.length - 1 ? 'current' : 'past') as 'current' | 'past',
  }));
  const sources = x.bySource.map((b, i) => ({ label: sourceLabel(b.label), value: Number(b.value), color: DASH_COLORS[i % DASH_COLORS.length] }));
  const severities = x.bySeverity.map((b) => ({ label: label(b.label), value: Number(b.value), color: SEVERITY_COLOR[b.label] ?? DASH_OTHER }));
  const categories = x.byCategory.slice(0, 6);
  const rows = x.items.filter((i) => (source === 'all' || i.source === source)
    && (!q || `${i.number} ${i.subject ?? ''} ${i.category} ${i.description}`.toLowerCase().includes(q.toLowerCase())));
  const open = (i: Incident) => { const p = incidentPath(i); if (p) nav(p); };
  const subject = (i: Incident) => <DashName icon={SOURCE[i.source]?.[1] ?? 'report'} name={i.subject ?? i.number} sub={`${i.number} · ${sourceLabel(i.source)}`} />;
  return (
    <div className="oc-dash-page">
      <DashHead title={title} sub={sub} controls={controls} />
      <DashGrid>
        <DashCard span={5} icon="report" title="Incidents in the period">
          <div className="oc-dash-hero">
            <div>
              <Amount text={formatNumber(x.total)} size="hero" />
              <Delta ratio={n(x.totalChange)} inverse text={`vs ${period === 'year' ? 'last year' : 'last month'} to date (${formatNumber(x.previousTotal)}).`} />
            </div>
            <HeatBars values={totals} />
          </div>
          <div className="oc-dash-actions">
            <DashButton tone="blue" icon="person" to="/golf/operations/caddy-incidents">Caddy</DashButton>
            <DashButton tone="dark" icon="electric_car" to="/golf/operations/cart-incidents">Golf cart</DashButton>
            <DashButton tone="grey" icon="description" to="/reports/reporting.incidents">Report</DashButton>
          </div>
        </DashCard>
        <DashCard span={3} icon="pending" tone="green" title="Open now">
          <div className="oc-dash-figure">
            <Amount text={formatNumber(x.open)} />
            {x.open > 0 && <span className="oc-dash-chip" data-good={x.openOver7Days === 0}>{x.openOver7Days} over 7 days</span>}
          </div>
          <Note tone={x.openOver7Days === 0 ? 'good' : 'bad'}>
            {x.openOver7Days === 0 ? <>Every open incident is less than a week old.</> : <><b>{x.openOver7Days}</b> open incident{x.openOver7Days === 1 ? '' : 's'} waiting more than 7 days for an action.</>}
          </Note>
          <SplitStats items={[
            { label: 'High / critical', value: formatNumber(x.highSeverity), color: 'var(--dash-blue)' },
            { label: 'Damage', value: moneyShort(x.damage), color: 'var(--dash-lime)' },
          ]} />
        </DashCard>
        <DashCard span={4}>
          <div className="oc-dash-inset">
            <div className="oc-dash-card-head"><DashIcon name="priority_high" tone="red" /><h2>High severity</h2></div>
            <Amount text={formatNumber(x.highSeverity)} />
            <span className="oc-dash-delta-row"><span className="oc-dash-chip" data-good={x.highSeverity === 0}>{x.total ? pct(x.highSeverity / x.total) : '0%'}</span>
              <span className="oc-dash-chip-suffix">of the incidents are high or critical</span></span>
          </div>
          {severities.length > 0 ? (
            <>
              <SegmentBar parts={severities} legend={false} format={(v) => `${v}`} />
              <BreakdownList rows={severities.map((s) => ({ label: s.label, value: formatNumber(s.value), share: x.total ? s.value / x.total : null, color: s.color }))} />
            </>
          ) : <p className="oc-dash-empty">No incident in this period.</p>}
        </DashCard>

        <DashCard span={4} icon="category" title="By source">
          {sources.length === 0 ? <p className="oc-dash-empty">No incident in this period.</p> : (
            <>
              <SegmentBar parts={sources} legend={false} format={(v) => `${v}`} />
              <BreakdownList rows={sources.map((s) => ({ label: s.label, value: formatNumber(s.value), share: x.total ? s.value / x.total : null, color: s.color }))} />
            </>
          )}
        </DashCard>
        <DashCard span={4} icon="bar_chart" title="Incidents · 6 months">
          {totals.every((t) => t === 0) ? <p className="oc-dash-empty">No incident in the last six months.</p>
            : <ColumnChart points={trend} aLabel="All incidents" bLabel="Caddy" axis="Incidents" format={(v) => formatNumber(v)} />}
        </DashCard>
        <div className="oc-dash-stack" data-span="4">
          <MiniCard label="Damage charged to folio" value={<Amount text={moneyShort(x.damageCharged)} size="sm" />}
            delta={<span className="oc-dash-sub">of {moneyShort(x.damage)} recorded</span>} to="/golf/operations/cart-incidents" />
          <DashCard span={4} icon="sell" title="Top categories">
            {categories.length === 0 ? <p className="oc-dash-empty">No incident in this period.</p> : (
              <RankBars ranked={false} format={(v) => formatNumber(v)}
                rows={categories.map((c, i) => ({ key: c.label, rank: i + 1, name: label(c.label), value: formatNumber(Number(c.value)), total: Number(c.value) }))} />
            )}
          </DashCard>
          <ReportCard label="Track & Print Report" title="Incident Report" to="/reports/reporting.incidents" />
        </div>

        <DashTable<Incident> title="Needs action" icon="pending_actions" rows={x.openItems} rowKey={(i) => i.id} onRow={open} empty="No open incident. Well done."
          info={(i) => i.description}
          columns={[
            { key: 'subject', header: 'Subject', render: subject },
            { key: 'category', header: 'Category', render: (i) => label(i.category) },
            { key: 'severity', header: 'Severity', align: 'center', render: (i) => <DashStatusPill tone={SEVERITY_TONE[i.severity] ?? 'neutral'}>{label(i.severity)}</DashStatusPill> },
            { key: 'age', header: 'Open for', align: 'right', render: (i) => { const a = daysAgo(i.occurredAt); return <strong className="oc-dash-num">{a === 0 ? 'today' : `${a} day${a === 1 ? '' : 's'}`}</strong>; } },
            { key: 'occurred', header: 'Occurred', render: (i) => formatDateTime(i.occurredAt) },
          ]} />
        <DashTable<Incident> title="Incidents of the period" icon="report" rows={rows} rowKey={(i) => i.id} search={q} onSearch={setQ} onRow={open}
          empty="No incident matches." info={(i) => [i.description, i.actionTaken && `Action: ${i.actionTaken}`].filter(Boolean).join(' — ')}
          filter={<PillSelect label="Source" value={source} onChange={setSource}
            options={[{ value: 'all', label: 'All sources' }, ...Object.entries(SOURCE).map(([v, [l]]) => ({ value: v, label: l }))]} />}
          columns={[
            { key: 'occurred', header: 'Occurred', render: (i) => formatDateTime(i.occurredAt) },
            { key: 'subject', header: 'Subject', render: subject },
            { key: 'category', header: 'Category', render: (i) => label(i.category) },
            { key: 'severity', header: 'Severity', align: 'center', render: (i) => <DashStatusPill tone={SEVERITY_TONE[i.severity] ?? 'neutral'}>{label(i.severity)}</DashStatusPill> },
            { key: 'damage', header: 'Damage', align: 'right', render: (i) => (i.damageAmount ? <span className="oc-dash-num" title={label(i.damageStatus)}>{money(i.damageAmount)}</span> : '—') },
            { key: 'status', header: 'Status', align: 'center', render: (i) => { const [t, l] = STATUS_TONE[i.status] ?? ['neutral', label(i.status)]; return <DashStatusPill tone={t}>{l}</DashStatusPill>; } },
          ]} />
      </DashGrid>
    </div>
  );
}
