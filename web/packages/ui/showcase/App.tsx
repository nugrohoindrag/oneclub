import React, { useState } from 'react';
import {
  Amount, BreakdownList, CircleButton, ColumnChart, DashButton, DashCard, DashGrid, DashStatusPill, Delta, Gauge, HeatBars, MiniCard, Note, PillSelect,
  ProgressRow, PromoCard, ReportCard, SegmentBar, SplitStats, DASH_COLORS, DASH_OTHER, type ColumnPoint,
} from '../src/components/index.js';
import { DashboardOverview } from '../src/templates/index.js';

// Showcase of the OneClub Design System (`pnpm showcase`): the dashboard
// reference template, the kit components one by one and the kit tokens.

const TOKENS = ['--dash-canvas', '--dash-card', '--dash-muted', '--dash-line', '--dash-text', '--dash-sub', '--dash-blue', '--dash-sky', '--dash-lime',
  '--dash-ink', '--dash-green', '--dash-red', '--dash-amber', '--dash-dark', '--dash-grey'];

const M = 1_000_000;
const COLUMNS: ColumnPoint[] = [
  { label: 'May', a: 410 * M, b: 380 * M, state: 'past' }, { label: 'Jun', a: 380 * M, b: 400 * M, state: 'past' },
  { label: 'Jul', a: 450 * M, b: 420 * M, state: 'past' }, { label: 'Aug', a: 520 * M, b: 450 * M, state: 'current' },
  { label: 'Sep', a: 470 * M, state: 'future' }, { label: 'Oct', a: 490 * M, state: 'future' },
];

const short = (v: number) => new Intl.NumberFormat('id-ID', { style: 'currency', currency: 'IDR', notation: 'compact', maximumFractionDigits: 2 }).format(v);

function Components() {
  const [m, setM] = useState('2026-10');
  return (
    <div className="oc-dash-page">
      <DashGrid>
        <DashCard span={4} icon="tune" title="Pills, buttons, circles" controls={<PillSelect label="Month" value={m} onChange={setM}
          options={[{ value: '2026-10', label: 'Oktober 2026' }, { value: '2026-09', label: 'September 2026' }]} />}>
          <div className="oc-dash-actions">
            <DashButton tone="blue" icon="add">Primary</DashButton>
            <DashButton tone="dark">Secondary</DashButton>
            <DashButton tone="grey">Tertiary</DashButton>
          </div>
          <div className="oc-dash-actions"><CircleButton icon="tune" label="Filter" dot /><CircleButton arrow label="Open" /><CircleButton arrow label="Open" dark /></div>
        </DashCard>
        <DashCard span={4} icon="payments" title="Amounts & change">
          <Amount text={short(9_980_000_000)} size="hero" />
          <Delta ratio={0.12} text="vs last month, good progress." />
          <span className="oc-dash-delta-row"><Delta ratio={0.091} chip="" /><Delta ratio={0.208} chip="" inverse suffix="vs previous period" /></span>
          <Note>Revenue increased by <b>9.1%</b> from last month.</Note>
        </DashCard>
        <DashCard span={4} icon="bar_chart" title="Status & heat">
          <div className="oc-dash-actions">
            <DashStatusPill tone="good">Complete</DashStatusPill><DashStatusPill tone="bad">Canceled</DashStatusPill>
            <DashStatusPill tone="warn">Pending</DashStatusPill><DashStatusPill tone="neutral">Draft</DashStatusPill>
          </div>
          <HeatBars values={[2, 4, 3, 6, 5, 4, 6, 3]} />
          <SplitStats items={[{ label: 'Salary', value: short(30_000_000), color: 'var(--dash-blue)' }, { label: 'Freelance', value: short(20_000_000), color: 'var(--dash-lime)' }]} />
        </DashCard>
        <DashCard span={6}>
          <div className="oc-dash-inset"><div className="oc-dash-card-head"><h2>Cost card · breakdown</h2></div><Amount text={short(41_440_000)} /></div>
          <SegmentBar legend={false} format={(v) => short(v)} parts={[
            { label: 'Commissions', value: 30, color: DASH_COLORS[0] }, { label: 'Pro shop', value: 6, color: DASH_COLORS[1] },
            { label: 'Instructor fees', value: 3, color: DASH_COLORS[2] }, { label: 'Utilities', value: 2, color: DASH_COLORS[3] }, { label: 'Other', value: 1, color: DASH_OTHER },
          ]} />
          <BreakdownList rows={[
            { label: 'Commissions', value: short(30_000_000), share: 0.714, color: DASH_COLORS[0] }, { label: 'Pro shop', value: short(6_000_000), share: 0.143, color: DASH_COLORS[1] },
            { label: 'Instructor fees', value: short(3_000_000), share: 0.071, color: DASH_COLORS[2] }, { label: 'Utilities', value: short(2_000_000), share: 0.048, color: DASH_COLORS[3] },
            { label: 'Other', value: short(1_000_000), share: 0.024, color: DASH_OTHER },
          ]} />
        </DashCard>
        <DashCard span={6} icon="target" title="Gauge & goals">
          <Gauge title="Revenue target" ratio={0.72} caption="Actual" value={short(720_000_000)} sub={<>/ {short(1_000_000_000)}</>} />
          <div className="oc-dash-list"><ProgressRow label="Rounds" ratio={0.95} /><ProgressRow label="F&B" ratio={0.68} /><ProgressRow label="Events" ratio={0.31} /></div>
        </DashCard>
        <DashCard span={8} icon="bar_chart" title="Column chart">
          <ColumnChart aLabel="Actual" bLabel="Target" axis="Amount (IDR)" format={(v) => short(v)} points={COLUMNS} />
        </DashCard>
        <div className="oc-dash-stack" data-span="4">
          <PromoCard span={4} badge="Badge" title="Promotion card" cta="Open" to="#" />
          <MiniCard label="Mini card" value={<Amount text={short(1_008_900)} size="sm" />} delta={<Delta ratio={-0.12} chip="" />} to="#" />
          <ReportCard label="Track & Print Report" title="Report card" to="#" />
        </div>
      </DashGrid>
    </div>
  );
}

function Tokens() {
  return (
    <div className="oc-dash-page">
      <DashGrid>
        {TOKENS.map((t) => (
          <DashCard key={t} span={3}>
            <div style={{ height: 64, borderRadius: 14, background: `var(${t})`, border: '1px solid var(--dash-line)' }} />
            <code style={{ fontSize: 13 }}>{t}</code>
          </DashCard>
        ))}
      </DashGrid>
    </div>
  );
}

const VIEWS = [{ value: 'overview', label: 'Dashboard overview' }, { value: 'components', label: 'Components' }, { value: 'tokens', label: 'Tokens' }];

export default function App() {
  const [view, setView] = useState('overview');
  const [theme, setTheme] = useState<'light' | 'dark'>('light');
  React.useEffect(() => { document.documentElement.dataset.theme = theme; }, [theme]);
  return (
    <div style={{ minHeight: '100vh', background: 'var(--dash-canvas)', padding: 24 }}>
      <div className="oc-dash-head" style={{ marginBottom: 16 }}>
        <div><h1>OneClub Design System</h1><p>Reference: docs/product/dashboard-ui.webp · tokens in src/tokens/dashboard.css · components in src/components/dashboard</p></div>
        <div className="oc-dash-controls">
          <PillSelect label="View" value={view} onChange={setView} options={VIEWS} />
          <PillSelect label="Theme" value={theme} onChange={(v) => setTheme(v as 'light' | 'dark')} options={[{ value: 'light', label: 'Light' }, { value: 'dark', label: 'Dark' }]} />
        </div>
      </div>
      {view === 'overview' && <DashboardOverview />}
      {view === 'components' && <Components />}
      {view === 'tokens' && <Tokens />}
    </div>
  );
}
