import React, { useState } from 'react';
import {
  Amount, CircleButton, ColumnChart, DashButton, DashCard, DashGrid, DashHead, DashIcon, DashName, DashStatusPill, DashTable, Delta, Gauge, HeatBars,
  MiniCard, Note, PillSelect, ProgressRow, PromoCard, ReportCard, SegmentBar, SplitStats, type DashStatus,
} from '../components/dashboard/index.js';

// The dashboard reference (docs/product/dashboard-ui.webp) composed from the
// kit with sample data: the layout every OneClub dashboard follows — a hero
// card with actions, an income card, a cost card with a red head, goals with
// a gauge, a column chart, a promotion card over two small cards, and a
// table with search. Apps fill the same places with their domain's figures.

const idr = (v: number) => new Intl.NumberFormat('id-ID', { style: 'currency', currency: 'IDR', maximumFractionDigits: 0 }).format(v);
const short = (v: number) => new Intl.NumberFormat('id-ID', { style: 'currency', currency: 'IDR', notation: 'compact', maximumFractionDigits: 2 }).format(v);

type Tx = { id: string; name: string; icon: string; amount: number; method: string; date: string; status: DashStatus; label: string };

const TX: Tx[] = [
  { id: '1', name: 'Green fee — Flight 07:20', icon: 'golf_course', amount: 3_400_000, method: 'Member charge', date: '20 Okt 2026', status: 'good', label: 'Complete' },
  { id: '2', name: 'Wedding deposit — Ballroom', icon: 'celebration', amount: 25_000_000, method: 'Bank transfer', date: '19 Okt 2026', status: 'bad', label: 'Canceled' },
  { id: '3', name: 'Pro shop — Driver', icon: 'storefront', amount: 6_750_000, method: 'Credit card', date: '19 Okt 2026', status: 'good', label: 'Complete' },
  { id: '4', name: 'Bungalow — 2 nights', icon: 'hotel', amount: 4_200_000, method: 'QRIS', date: '18 Okt 2026', status: 'warn', label: 'Pending' },
];

const MONTHS: { label: string; a: number; b: number; state: 'past' | 'current' | 'future' }[] = [
  { label: 'Jan', a: 410, b: 260, state: 'past' }, { label: 'Feb', a: 380, b: 240, state: 'past' }, { label: 'Mar', a: 520, b: 300, state: 'current' },
  { label: 'Apr', a: 460, b: 0, state: 'future' }, { label: 'May', a: 480, b: 0, state: 'future' }, { label: 'Jun', a: 500, b: 0, state: 'future' },
];

/** The dashboard reference screen, built only from the kit. */
export function DashboardOverview() {
  const [month, setMonth] = useState('2026-10');
  const [q, setQ] = useState('');
  const pill = <PillSelect label="Month" value={month} onChange={setMonth} options={[{ value: '2026-10', label: 'Oktober 2026' }, { value: '2026-09', label: 'September 2026' }]} />;
  const rows = TX.filter((t) => !q || t.name.toLowerCase().includes(q.toLowerCase()));
  return (
    <div className="oc-dash-page">
      <DashHead title="Dashboard" sub="Dashboard overview — the reference every OneClub dashboard follows" controls={pill} />
      <DashGrid>
        <DashCard span={5} icon="account_balance_wallet" title="My balance" controls={pill}>
          <div className="oc-dash-hero">
            <div>
              <Amount text={short(507_640_000)} size="hero" />
              <Delta ratio={0.12} text="balance increase, good progress." />
            </div>
            <HeatBars values={[3, 5, 2, 6, 4, 5, 6, 4]} />
          </div>
          <div className="oc-dash-actions">
            <DashButton tone="blue" icon="add">Add money</DashButton>
            <DashButton tone="dark" icon="arrow_upward">Send money</DashButton>
            <DashButton tone="grey" icon="arrow_downward">Request money</DashButton>
          </div>
        </DashCard>
        <DashCard span={3} icon="arrow_downward" tone="green" title="Income" controls={pill}>
          <div className="oc-dash-figure"><Amount text={short(50_000_000)} /><Delta ratio={0.091} chip="" /></div>
          <Note>Income increased by <b>9.1%</b> from last month.</Note>
          <SplitStats items={[{ label: 'Salary', value: short(30_000_000), color: 'var(--dash-blue)' }, { label: 'Freelance', value: short(20_000_000), color: 'var(--dash-lime)' }]} />
        </DashCard>
        <DashCard span={4}>
          <div className="oc-dash-inset">
            <div className="oc-dash-card-head"><DashIcon name="arrow_upward" tone="red" /><h2>Expense</h2><span className="oc-spacer" />{pill}</div>
            <Amount text={short(40_000_000)} />
            <Delta ratio={0.045} chip="" inverse suffix="vs last month" />
          </div>
          <SegmentBar format={(v) => short(v)} parts={[
            { label: 'Education', value: 20_000_000 }, { label: 'Goal', value: 13_000_000 }, { label: 'Entertainment', value: 7_000_000 },
          ]} />
        </DashCard>

        <DashCard span={4} icon="target" title="My goals" action={<CircleButton icon="tune" label="Goal settings" dot />}>
          <Gauge title="Swiss Holiday" ratio={0.61} caption="Target" value={short(12_240_000)} sub={<>/ {short(20_000_000)}</>} />
          <div className="oc-dash-list">
            <ProgressRow label="New golf set" ratio={0.12} />
            <ProgressRow label="Membership upgrade" ratio={0.56} />
          </div>
        </DashCard>
        <DashCard span={4} icon="bar_chart" title="Cashflow chart" controls={pill}>
          <ColumnChart points={MONTHS.map((m) => ({ ...m, a: m.a * 100_000, b: m.b ? m.b * 100_000 : null }))} aLabel="Income" bLabel="Expense"
            axis="Amount (IDR)" format={(v) => short(v)} />
        </DashCard>
        <div className="oc-dash-stack" data-span="4">
          <PromoCard span={4} badge="14 day free-trial" title="Get premium feature" cta="Join pro plan" to="#" />
          <MiniCard label="Today received" value={<Amount text={short(1_008_900)} size="sm" />} delta={<Delta ratio={-0.12} chip="" />} to="#" />
          <ReportCard label="Track & Print Report" title="Financial Report" to="#" />
        </div>

        <DashTable<Tx> title="Transaction history" icon="history" rows={rows} rowKey={(t) => t.id} search={q} onSearch={setQ}
          filter={<CircleButton icon="tune" label="Filter" dot />} info={(t) => `${t.name} · ${idr(t.amount)}`}
          columns={[
            { key: 'name', header: 'Name', render: (t) => <DashName icon={t.icon} name={t.name} /> },
            { key: 'amount', header: 'Amount', render: (t) => <span className="oc-dash-num">{idr(t.amount)}</span> },
            { key: 'method', header: 'Method', render: (t) => t.method },
            { key: 'date', header: 'Date', render: (t) => t.date },
            { key: 'status', header: 'Status', align: 'center', render: (t) => <DashStatusPill tone={t.status}>{t.label}</DashStatusPill> },
          ]} />
      </DashGrid>
    </div>
  );
}

export default DashboardOverview;
