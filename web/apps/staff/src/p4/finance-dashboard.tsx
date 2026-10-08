import { useState } from 'react';
import { useNavigate, useParams, useSearchParams } from 'react-router';
import { qs, useGet, type Schemas } from '@oneclub/api-client';
import { currentLocale, formatDate } from '@oneclub/i18n';
import {
  Amount, ColumnChart, BreakdownList, Card, CircleButton, DASH_COLORS, DASH_OTHER, DashButton, DashCard, DashGrid, DashHead, DashIcon, DashName, DashStatusPill, DashTable, Delta, Empty, ErrorAlert,
  Gauge, HeatBars, Icon, MiniCard, Note, PageHeader, PillSelect, ProgressRow, PromoCard, ReportCard, SegmentBar, Skeleton, SplitStats, monthOptions,
  type DashStatus,
} from '@oneclub/shell';
import { money, moneyShort as short } from '../p1/common';

// Finance Dashboard of the Back Office (Accountant home, Accounting → Finance
// Dashboard): financial summary, cash & bank with the cash flow trend and the
// bank reconciliation status, receivables, payables, expenses and budget vs
// actual. Every figure comes from GET /api/v1/accounting/dashboard (general
// ledger, AR / AP open items, bank statements, approved KPI target plan).

type Dashboard = Schemas['FinanceDashboard'];
type OpenItem = Schemas['OpenItem'];

const API = '/api/v1/accounting/dashboard';
const num = (v?: string | null) => Number(v ?? 0);
const ratio = (v?: string | null) => (v == null ? null : Number(v));

const DUE: Record<string, [string, string]> = {
  overdue: ['rejected', 'Overdue'], due_today: ['pending', 'Due today'], due_soon: ['pending', 'Due soon'], current: ['confirmed', 'Current'],
};

function useDashboard() {
  const [params, setParams] = useSearchParams();
  const month = params.get('month') ?? '';
  const d = useGet<Dashboard>(`${API}${qs({ month: month || undefined })}`);
  const setMonth = (v: string) => setParams(v ? { month: v } : {}, { replace: true });
  return { d, month, setMonth };
}

function MonthFilter({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return <input className="oc-input oc-filter" type="month" aria-label="Month" value={value} onChange={(e) => onChange(e.target.value)} />;
}

function BudgetTable({ d }: { d: Dashboard }) {
  if (d.budget.length === 0) {
    return <Empty title="No budget for this month" help={`There is no approved KPI target plan for ${d.from.slice(0, 4)}. Finance sets the targets in Management → KPI Targets.`} />;
  }
  return (
    <div className="oc-table-wrap">
      <table className="oc-table oc-small">
        <thead><tr><th>Line</th><th style={{ textAlign: 'right' }}>Budget</th><th style={{ textAlign: 'right' }}>Actual</th><th style={{ textAlign: 'right' }}>Variance</th></tr></thead>
        <tbody>
          {d.budget.map((b) => {
            const v = ratio(b.variance);
            return (
              <tr key={b.key}>
                <td>{b.label}{b.prorated && <div className="oc-muted">Budget to date</div>}</td>
                <td className="oc-num" style={{ textAlign: 'right' }}>{short(b.budget)}</td>
                <td className="oc-num" style={{ textAlign: 'right' }}>{short(b.actual)}</td>
                <td style={{ textAlign: 'right' }}>
                  {v == null ? '—' : (
                    <span className="oc-delta" data-tone={b.favorable ? 'up' : 'down'}>
                      {!b.favorable && <Icon name="warning" size={14} />}{v >= 0 ? '+' : '−'}{Math.abs(v * 100).toFixed(1)}%
                    </span>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function NoBook() {
  return (
    <Card>
      <Empty title="Accounting is not set up for this property" help="Open the accounting book with its cut-over date in Accounting → Setup; the dashboard fills from the general ledger." />
    </Card>
  );
}

/** Month pill of the finance cards (every card shows the page's month). */
function MonthPill({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return <PillSelect label="Month" value={value} onChange={onChange} options={monthOptions(currentLocale())} />;
}

const monthLabel = (m: string) => new Intl.DateTimeFormat(currentLocale() === 'en' ? 'en-US' : 'id-ID', { month: 'short' }).format(new Date(`${m}-01T00:00:00`));
const pct = (v: number | null) => (v == null ? '' : `${Math.abs(v * 100).toFixed(1)}%`);
const DUE_TONE: Record<string, DashStatus> = { overdue: 'bad', due_today: 'warn', due_soon: 'warn', current: 'good' };

type OpenRow = OpenItem & { kind: 'receivable' | 'payable' };

/** Finance Dashboard (Accountant home; Accounting → Finance Dashboard), on the dashboard kit. */
export function FinanceDashboardPage() {
  const nav = useNavigate();
  const { d, month, setMonth } = useDashboard();
  const [q, setQ] = useState('');
  const x = d.data;
  const s = x?.summary;
  const m = month || (x?.from.slice(0, 7) ?? '');
  const pill = <MonthPill value={m} onChange={setMonth} />;
  if (d.error) return <div className="oc-dash-page"><DashHead title="Finance Dashboard" /><ErrorAlert error={d.error} /></div>;
  if (!x || !s) return <div className="oc-dash-page"><DashHead title="Finance Dashboard" /><Skeleton rows={8} /></div>;
  if (!x.bookOpen) return <div className="oc-dash-page"><DashHead title="Finance Dashboard" /><NoBook /></div>;
  const flows = x.cashFlow;
  const last = flows[flows.length - 1];
  const opening = num(s.cashBank) - num(last?.net);
  const cashChange = last && opening > 0 ? num(last.net) / opening : null;
  const revenueChange = ratio(s.revenueChange);
  const expenseChange = ratio(s.expensesChange);
  // Every category in the list is also a part of the bar: the five largest, the rest as Other.
  const rest = x.expenses.slice(5).reduce((t, e) => t + num(e.amount), 0);
  const cats = [...x.expenses.slice(0, 5).map((e, i) => ({ label: e.name, amount: num(e.amount), share: ratio(e.share), color: DASH_COLORS[i] })),
    ...(rest > 0 ? [{ label: 'Other', amount: rest, share: num(s.expenses) ? rest / num(s.expenses) : null, color: DASH_OTHER }] : [])];
  const parts = cats.map((c) => ({ label: c.label, value: c.amount, color: c.color }));
  const [main, ...lines] = x.budget;
  const overdue = String(x.receivableAging.filter((b) => b.key !== 'current').reduce((t, b) => t + num(b.amount), 0));
  const unmatched = x.banks.reduce((t, b) => t + b.unmatchedLines, 0);
  const due = (key: string) => x.payableDue.find((b) => b.key === key)?.amount ?? '0';
  const open: OpenRow[] = [...x.topReceivables.map((i) => ({ ...i, kind: 'receivable' as const })), ...x.upcomingPayables.map((i) => ({ ...i, kind: 'payable' as const }))]
    .filter((i) => !q || `${i.party} ${i.number}`.toLowerCase().includes(q.toLowerCase()));
  return (
    <div className="oc-dash-page">
      <DashHead title="Finance Dashboard" sub={`${formatDate(x.from)} – ${formatDate(x.to)} · compared with ${formatDate(s.compareFrom)} – ${formatDate(s.compareTo)}`} />
      <DashGrid>
        <DashCard span={5} icon="account_balance_wallet" title="Cash & Bank" controls={pill}>
          <div className="oc-dash-hero">
            <div>
              <span title={money(s.cashBank)}><Amount text={short(s.cashBank)} size="hero" /></span>
              <Delta ratio={cashChange} text="net cash flow of the month" />
            </div>
            <HeatBars values={flows.map((f) => Math.max(0, num(f.cashIn)))} />
          </div>
          <div className="oc-dash-actions">
            <DashButton tone="blue" icon="add" to="/accounting/cash-bank?tab=cash">Record receipt</DashButton>
            <DashButton tone="dark" icon="arrow_upward" to="/accounting/payables?tab=payments">Pay supplier</DashButton>
            <DashButton tone="grey" icon="arrow_downward" to="/accounting/receivables?tab=collections">Collect</DashButton>
          </div>
        </DashCard>
        <DashCard span={3} icon="arrow_downward" tone="green" title="Revenue" controls={pill}>
          <div className="oc-dash-figure">
            <span title={money(s.revenue)}><Amount text={short(s.revenue)} /></span>
            <Delta ratio={revenueChange} chip="" />
          </div>
          {revenueChange != null && (
            <Note tone={revenueChange >= 0 ? 'good' : 'bad'}>Revenue {revenueChange >= 0 ? 'increased' : 'decreased'} by <b>{pct(revenueChange)}</b> from the previous period.</Note>
          )}
          <SplitStats items={[
            { label: 'Net profit', value: short(s.netProfit), color: 'var(--dash-blue)', to: '/accounting/reports' },
            { label: 'Receivables', value: short(s.receivables), color: 'var(--dash-lime)', to: '/accounting/receivables' },
          ]} />
        </DashCard>
        <DashCard span={4} className="oc-dash-expense">
          <div className="oc-dash-inset">
            <div className="oc-dash-card-head">
              <DashIcon name="arrow_upward" tone="red" /><h2>Expenses</h2><span className="oc-spacer" />{pill}
            </div>
            <span title={money(s.expenses)}><Amount text={short(s.expenses)} /></span>
            <Delta ratio={expenseChange} chip="" inverse suffix="vs previous period" />
          </div>
          {parts.length === 0 ? <p className="oc-dash-sub">No expense in this period.</p> : (
            <>
              <SegmentBar parts={parts} legend={false} format={(v) => short(String(v))} />
              <BreakdownList rows={cats.map((c) => ({ label: c.label, value: short(String(c.amount)), share: c.share, color: c.color, to: '/accounting/reports' }))} />
            </>
          )}
          <SplitStats items={[
            { label: 'Payables overdue', value: short(due('overdue')), color: 'var(--dash-red)', to: '/accounting/payables?tab=aging' },
            { label: 'Due this week', value: short(String(num(due('today')) + num(due('week')))), color: 'var(--dash-amber)', to: '/accounting/payables?tab=bills' },
          ]} />
        </DashCard>

        <DashCard span={4} icon="savings" title="Budget" action={<CircleButton icon="tune" label="KPI targets" to="/management/targets" dot={x.budget.some((b) => !b.favorable)} />}>
          {!main ? (
            <p className="oc-dash-empty">No approved KPI target plan for {x.from.slice(0, 4)}: set the targets in Management → KPI Targets.</p>
          ) : (
            <>
              <Gauge title={main.label} ratio={num(main.budget) ? num(main.actual) / num(main.budget) : null} caption="Actual" value={short(main.actual)}
                sub={<>/ {short(main.budget)} {main.favorable && <Icon name="check_circle" size={14} />}</>} />
              <div className="oc-dash-list">
                {lines.slice(0, 3).map((b) => (
                  <ProgressRow key={b.key} label={b.label} ratio={num(b.budget) ? num(b.actual) / num(b.budget) : null} to="/accounting/budget-vs-actual"
                    hint={`${short(b.actual)} of ${short(b.budget)}`} />
                ))}
              </div>
            </>
          )}
        </DashCard>
        <DashCard span={4} icon="bar_chart" title="Cash Flow" controls={pill}>
          <ColumnChart aLabel="Cash in" bLabel="Cash out" axis="Amount (IDR)" format={(v) => short(String(v))}
            points={flows.map((f, i) => ({ label: monthLabel(f.month), title: `${monthLabel(f.month)} ${f.month.slice(0, 4)}`, a: num(f.cashIn), b: num(f.cashOut),
              state: i === flows.length - 1 ? 'current' : 'past' }))} />
        </DashCard>
        <div className="oc-dash-stack" data-span="4">
          <PromoCard span={4} badge={unmatched ? `${unmatched} unmatched lines` : 'Bank reconciliation'} title="Reconcile bank statements" cta="Open"
            to="/accounting/cash-bank?tab=reconciliations" />
          <MiniCard label="Overdue receivables" value={<span title={money(overdue)}><Amount text={short(overdue)} size="sm" /></span>}
            to="/accounting/receivables?tab=aging" />
          <ReportCard label="Track & Print Report" title="Financial Report" to="/accounting/reports" />
        </div>

        <DashTable<OpenRow> title="Open Items" icon="history" rows={open} rowKey={(r) => `${r.kind}-${r.id}`} search={q} onSearch={setQ}
          empty="No open receivable or payable."
          onRow={(r) => nav(r.kind === 'receivable' ? '/accounting/receivables' : '/accounting/payables')}
          info={(r) => `${r.kind === 'receivable' ? 'Customer invoice' : 'Supplier bill'} ${r.number} · due ${formatDate(r.dueDate)}`}
          columns={[
            { key: 'party', header: 'Name', render: (r) => <DashName icon={r.kind === 'receivable' ? 'request_quote' : 'receipt_long'} tone={r.kind === 'receivable' ? 'green' : 'red'}
              name={r.party} sub={r.number} /> },
            { key: 'amount', header: 'Amount', render: (r) => <span className="oc-dash-num">{money(r.amount)}</span> },
            { key: 'kind', header: 'Type', render: (r) => <DashName icon={r.kind === 'receivable' ? 'arrow_downward' : 'arrow_upward'} name={r.kind === 'receivable' ? 'Receivable' : 'Payable'} /> },
            { key: 'dueDate', header: 'Due date', render: (r) => formatDate(r.dueDate) },
            { key: 'status', header: 'Status', align: 'center', render: (r) => <DashStatusPill tone={DUE_TONE[r.status] ?? 'neutral'}>{DUE[r.status]?.[1] ?? r.status}</DashStatusPill> },
          ]} />
      </DashGrid>
    </div>
  );
}

/** Budget vs Actual on its own (Budget & Control). */
export function BudgetVsActualPage() {
  const { d, month, setMonth } = useDashboard();
  const x = d.data;
  return (
    <div className="oc-stack">
      <PageHeader title="Budget vs Actual" help={x ? `${formatDate(x.from)} – ${formatDate(x.to)} · budget from the approved KPI target plan` : undefined}
        actions={<MonthFilter value={month || (x?.from.slice(0, 7) ?? '')} onChange={setMonth} />} />
      <ErrorAlert error={d.error} />
      {!x && !d.error && <Skeleton rows={6} />}
      {x && !x.bookOpen && <NoBook />}
      {x && x.bookOpen && <Card><BudgetTable d={x} /></Card>}
    </div>
  );
}

const SOON: Record<string, [string, string]> = {
  'tax-filing': ['Tax Filing', 'Monthly SPT filing and payment tracking. Today the e-Faktur export sits in Tax Transactions.'],
  budget: ['Budget', 'Budget per expense account and cost center. Today Budget vs Actual uses the approved KPI target plan.'],
  'cost-center': ['Cost Center', 'Cost center master data and reporting by cost center.'],
};

/** Placeholder of a role-menu item whose screen is not built yet. */
export function SoonPage() {
  const { key = '' } = useParams();
  const [title, text] = SOON[key] ?? ['Coming soon', ''];
  return (
    <div className="oc-stack">
      <PageHeader title={title} />
      <Card><Empty title="This screen is not available yet" help={text} /></Card>
    </div>
  );
}
