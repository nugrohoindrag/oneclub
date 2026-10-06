import { useNavigate, useParams, useSearchParams } from 'react-router';
import { qs, useGet, type Schemas } from '@oneclub/api-client';
import { currentLocale, formatDate } from '@oneclub/i18n';
import { Card, Empty, ErrorAlert, Icon, PageHeader, Skeleton, StatTile, StatusPill } from '@oneclub/shell';
import { money } from '../p1/common';

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

/** IDR in short form for headline figures (Rp 9,98 M / IDR 9.98B). */
function short(v?: string | null) {
  return new Intl.NumberFormat(currentLocale() === 'en' ? 'en-US' : 'id-ID', {
    style: 'currency', currency: 'IDR', notation: 'compact', maximumFractionDigits: 2,
  }).format(num(v));
}

const DUE: Record<string, [string, string]> = {
  overdue: ['rejected', 'Overdue'], due_today: ['pending', 'Due today'], due_soon: ['pending', 'Due soon'], current: ['confirmed', 'Current'],
};
const BANK: Record<string, [string, string]> = {
  reconciled: ['approved', 'Reconciled'], in_progress: ['pending', 'In progress'], attention: ['rejected', 'Needs attention'], never: ['draft', 'Never reconciled'],
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

/** Horizontal bars of one measure (single hue); amounts stay visible as text. */
function Bars({ rows }: { rows: { key: string; label: string; amount: string; note?: string }[] }) {
  const max = Math.max(1, ...rows.map((r) => num(r.amount)));
  return (
    <div className="oc-fin-bars">
      {rows.map((r) => (
        <div key={r.key} className="oc-fin-bar" title={`${r.label}: ${money(r.amount)}`}>
          <span className="oc-fin-bar-label">{r.label}</span>
          <span className="oc-fin-bar-track"><span style={{ width: `${(num(r.amount) / max) * 100}%` }} /></span>
          <span className="oc-fin-bar-value oc-num">{short(r.amount)}{r.note && <span className="oc-muted"> · {r.note}</span>}</span>
        </div>
      ))}
    </div>
  );
}

/** Cash in / cash out per month (grouped bars, one axis) with the values as a table. */
function CashFlowChart({ months }: { months: Dashboard['cashFlow'] }) {
  const max = Math.max(1, ...months.flatMap((m) => [num(m.cashIn), num(m.cashOut)]));
  const label = (m: string) => new Intl.DateTimeFormat(currentLocale() === 'en' ? 'en-US' : 'id-ID', { month: 'short' }).format(new Date(`${m}-01T00:00:00`));
  return (
    <div className="oc-stack" style={{ gap: 12 }}>
      <div className="oc-row oc-small" aria-hidden="true">
        <span className="oc-fin-key" style={{ background: 'var(--oc-chart-a)' }} /> Cash in
        <span className="oc-fin-key" style={{ background: 'var(--oc-chart-b)', marginLeft: 12 }} /> Cash out
      </div>
      <div className="oc-fin-cols" role="img" aria-label="Cash in and cash out per month">
        {months.map((m) => (
          <div key={m.month} className="oc-fin-col">
            <div className="oc-fin-col-bars" title={`${label(m.month)} — in ${money(m.cashIn)}, out ${money(m.cashOut)}, net ${money(m.net)}`}>
              <span style={{ height: `${(num(m.cashIn) / max) * 100}%`, background: 'var(--oc-chart-a)' }} />
              <span style={{ height: `${(num(m.cashOut) / max) * 100}%`, background: 'var(--oc-chart-b)' }} />
            </div>
            <span className="oc-small oc-muted">{label(m.month)}</span>
          </div>
        ))}
      </div>
      <div className="oc-table-wrap">
        <table className="oc-table oc-small">
          <thead><tr><th>Month</th><th style={{ textAlign: 'right' }}>Cash in</th><th style={{ textAlign: 'right' }}>Cash out</th><th style={{ textAlign: 'right' }}>Net cash flow</th></tr></thead>
          <tbody>
            {months.map((m) => (
              <tr key={m.month}>
                <td>{label(m.month)} {m.month.slice(0, 4)}</td>
                <td className="oc-num" style={{ textAlign: 'right' }}>{short(m.cashIn)}</td>
                <td className="oc-num" style={{ textAlign: 'right' }}>{short(m.cashOut)}</td>
                <td className="oc-num" style={{ textAlign: 'right', fontWeight: 600 }}>{short(m.net)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function OpenItems({ items, party, empty }: { items: OpenItem[]; party: string; empty: string }) {
  if (items.length === 0) return <Empty title={empty} />;
  return (
    <div className="oc-table-wrap">
      <table className="oc-table oc-small">
        <thead><tr><th>{party}</th><th>Due date</th><th style={{ textAlign: 'right' }}>Amount</th><th>Status</th></tr></thead>
        <tbody>
          {items.map((i) => {
            const [tone, l] = DUE[i.status] ?? ['draft', i.status];
            return (
              <tr key={i.id}>
                <td><div style={{ fontWeight: 600 }}>{i.party}</div><div className="oc-muted">{i.number}</div></td>
                <td>{formatDate(i.dueDate)}</td>
                <td className="oc-num" style={{ textAlign: 'right' }}>{money(i.amount)}</td>
                <td><StatusPill status={tone} label={l} /></td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
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

/** Finance Dashboard (Accountant home; Accounting → Finance Dashboard). */
export function FinanceDashboardPage() {
  const nav = useNavigate();
  const { d, month, setMonth } = useDashboard();
  const x = d.data;
  const s = x?.summary;
  const period = x ? `${formatDate(x.from)} – ${formatDate(x.to)}` : undefined;
  return (
    <div className="oc-stack">
      <PageHeader title="Finance Dashboard" help={period && s ? `${period} · compared with ${formatDate(s.compareFrom)} – ${formatDate(s.compareTo)}` : period}
        actions={<MonthFilter value={month || (x?.from.slice(0, 7) ?? '')} onChange={setMonth} />} />
      <ErrorAlert error={d.error} />
      {!x && !d.error && <Skeleton rows={8} />}
      {x && !x.bookOpen && <NoBook />}
      {x && s && x.bookOpen && (
        <>
          <div className="oc-stat-grid">
            <StatTile label="Total Revenue" icon="payments" value={short(s.revenue)} change={ratio(s.revenueChange)} changeLabel="vs previous period"
              onOpen={() => nav('/accounting/reports')} />
            <StatTile label="Total Expenses" icon="receipt_long" value={short(s.expenses)} change={ratio(s.expensesChange)} changeLabel="vs previous period" inverse
              onOpen={() => nav('/accounting/reports')} />
            <StatTile label="Net Profit" icon="trending_up" value={short(s.netProfit)} change={ratio(s.netProfitChange)} changeLabel="vs previous period"
              onOpen={() => nav('/accounting/reports')} />
            <StatTile label="Cash & Bank Balance" icon="account_balance" value={short(s.cashBank)} onOpen={() => nav('/accounting/cash-bank?tab=accounts')} />
            <StatTile label="Accounts Receivable" icon="request_quote" value={short(s.receivables)} onOpen={() => nav('/accounting/receivables')} />
            <StatTile label="Accounts Payable" icon="receipt" value={short(s.payables)} onOpen={() => nav('/accounting/payables')} />
          </div>

          <div className="oc-fin-grid">
            <Card title="Cash & Bank" icon="account_balance" actions={<button className="oc-btn oc-btn-sm oc-btn-outline" onClick={() => nav('/accounting/cash-bank?tab=accounts')}>View accounts</button>}>
              <div className="oc-stat-value" style={{ marginBottom: 12 }}>{short(s.cashBank)}</div>
              <div className="oc-stack" style={{ gap: 0 }}>
                {x.cash.map((c) => (
                  <div key={c.accountId} className="oc-fin-line">
                    <span><Icon name={c.kind === 'bank' ? 'account_balance' : 'payments'} size={16} /> {c.name}</span>
                    <span className="oc-num">{money(c.balance)}</span>
                  </div>
                ))}
              </div>
              <h3 className="oc-fin-sub">Bank reconciliation</h3>
              {x.banks.length === 0 && <div className="oc-small oc-muted">No bank account yet.</div>}
              {x.banks.map((b) => {
                const [tone, l] = BANK[b.status] ?? ['draft', b.status];
                return (
                  <div key={b.bankAccountId} className="oc-fin-line">
                    <span>{b.name}<div className="oc-small oc-muted">{b.lastStatementDate ? `Statement ${formatDate(b.lastStatementDate)}` : 'No statement yet'}
                      {b.unmatchedLines > 0 && ` · ${b.unmatchedLines} unmatched lines`}</div></span>
                    <StatusPill status={tone} label={l} />
                  </div>
                );
              })}
            </Card>
            <Card title="Cash Flow Trend" icon="monitoring">
              <CashFlowChart months={x.cashFlow} />
            </Card>
          </div>

          <div className="oc-fin-grid">
            <Card title="Accounts Receivable" icon="request_quote" actions={<button className="oc-btn oc-btn-sm oc-btn-outline" onClick={() => nav('/accounting/receivables')}>View Receivables</button>}>
              <div className="oc-stat-value" style={{ marginBottom: 12 }}>{short(s.receivables)}</div>
              <Bars rows={x.receivableAging.map((b) => ({ key: b.key, label: b.label, amount: b.amount }))} />
              <h3 className="oc-fin-sub">Top outstanding receivables</h3>
              <OpenItems items={x.topReceivables} party="Customer" empty="No open receivable" />
            </Card>
            <Card title="Accounts Payable" icon="receipt" actions={<button className="oc-btn oc-btn-sm oc-btn-outline" onClick={() => nav('/accounting/payables')}>View Payables</button>}>
              <div className="oc-stat-value" style={{ marginBottom: 12 }}>{short(s.payables)}</div>
              <Bars rows={x.payableDue.map((b) => ({ key: b.key, label: b.label, amount: b.amount }))} />
              <h3 className="oc-fin-sub">Upcoming payments</h3>
              <OpenItems items={x.upcomingPayables} party="Supplier" empty="No upcoming payment" />
            </Card>
          </div>

          <div className="oc-fin-grid">
            <Card title="Expense Overview" icon="pie_chart">
              {x.expenses.length === 0 ? <Empty title="No expense in this period" /> : (
                <Bars rows={x.expenses.map((e, i) => ({ key: e.accountId ?? `other-${i}`, label: e.name, amount: e.amount, note: `${(Number(e.share) * 100).toFixed(1)}%` }))} />
              )}
            </Card>
            <Card title="Budget vs Actual" icon="balance">
              <BudgetTable d={x} />
            </Card>
          </div>
        </>
      )}
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
