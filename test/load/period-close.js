// Period close under operations (PRD P4 FR-REL-P4-01 "laporan keuangan
// tahunan di read replica", NFR Availability "closing tidak mengganggu
// operasional", §9.4 month-end close): while cashiers keep posting, Finance
// runs the month-end steps — postings run, posting reconciliation (journals
// = Daily Revenue Report), control-account reconciliation, the Trial
// Balance and the annual Profit & Loss / Balance Sheet / Cash Flow per
// property and consolidated (read replica), the Report Library export —
// and, when PERIOD_ID is given, soft-closes that period (Staging only: a
// reopen needs the Finance Manager's approval).
//
// Targets: every annual financial report < 30 s, soft-close < 60 s,
// cashier p95 < 800 ms during the close, no 5xx.
//
//   k6 run -e BASE_URL=... -e PROPERTY_ID=... -e STAFF_EMAIL=... -e STAFF_PASSWORD=... \
//          -e FINANCE_EMAIL=... -e FINANCE_PASSWORD=... [-e YEAR=2026] [-e PERIOD_ID=<uuid>] test/load/period-close.js
import http from 'k6/http';
import { check } from 'k6';
import { Trend } from 'k6/metrics';
import { BASE, PROPERTY, headers, session, uuid } from './lib.js';

const reportTime = new Trend('financial_report_ms', true);
const closeTime = new Trend('period_soft_close_ms', true);

export const options = {
  setupTimeout: '5m',
  scenarios: {
    cashiers: { executor: 'constant-arrival-rate', rate: 10, timeUnit: '1s', duration: '4m', preAllocatedVUs: 20, maxVUs: 60, exec: 'post' },
    close: { executor: 'shared-iterations', iterations: 1, vus: 1, startTime: '20s', exec: 'close', maxDuration: '10m' },
  },
  thresholds: {
    financial_report_ms: ['max<30000'],
    period_soft_close_ms: ['max<60000'],
    http_req_failed: ['rate<0.01'],
    'http_req_duration{scenario:cashiers}': ['p(95)<800'],
  },
};

export function setup() {
  const staff = session(__ENV.STAFF_EMAIL, __ENV.STAFF_PASSWORD);
  const finance = session(__ENV.FINANCE_EMAIL || __ENV.STAFF_EMAIL, __ENV.FINANCE_PASSWORD || __ENV.STAFF_PASSWORD);
  return { staff, finance };
}

let folio = null;

export function post(data) {
  if (!folio) {
    const f = http.post(`${BASE}/api/v1/billing/folios`, JSON.stringify({ holderName: `Close ${uuid().slice(0, 8)}` }), headers(data.staff));
    folio = JSON.parse(f.body).id;
  }
  const r = http.post(`${BASE}/api/v1/billing/folios/${folio}/lines`, JSON.stringify({ chargeType: 'other', description: 'During close', unitPrice: '35000' }),
    headers(data.staff, { 'Idempotency-Key': uuid() }));
  check(r, { 'charge posted during close': (x) => x.status === 201 });
}

function timed(data, name, url) {
  const t0 = Date.now();
  const r = http.get(url, headers(data.finance));
  reportTime.add(Date.now() - t0, { report: name });
  check(r, { [`${name} ok`]: (x) => x.status === 200 });
  return r;
}

export function close(data) {
  const year = __ENV.YEAR || String(new Date().getFullYear());
  const from = `${year}-01-01`;
  const today = new Date().toISOString().slice(0, 10);
  const to = today.startsWith(year) ? today : `${year}-12-31`;
  const acc = `${BASE}/api/v1/accounting`;
  check(http.post(`${acc}/postings:run`, JSON.stringify({ upTo: to }), headers(data.finance)), { 'postings run': (x) => x.status === 200 });
  check(http.get(`${acc}/posting-reconciliation?date=${to}`, headers(data.finance)), { 'posting reconciliation': (x) => x.status === 200 });
  check(http.get(`${acc}/reconciliation?asOf=${to}`, headers(data.finance)), { 'control reconciliation': (x) => x.status === 200 });
  timed(data, 'trial-balance', `${acc}/trial-balance?from=${from}&to=${to}&propertyId=${PROPERTY}`);
  for (const kind of ['profit-loss', 'balance-sheet', 'cash-flow', 'revenue-by-business-line']) {
    timed(data, `${kind}-property`, `${acc}/reports/${kind}?from=${from}&to=${to}&propertyId=${PROPERTY}&compare=last_year`);
    timed(data, `${kind}-consolidated`, `${acc}/reports/${kind}?from=${from}&to=${to}`);
  }
  timed(data, 'report-library-tb', `${BASE}/api/v1/reporting/reports/accounting.trial_balance?params[from]=${from}&params[to]=${to}`);
  const ex = http.post(`${BASE}/api/v1/reporting/exports`, JSON.stringify({ reportCode: 'accounting.profit_loss', format: 'pdf', params: { from, to } }),
    headers(data.finance, { 'Idempotency-Key': uuid() }));
  check(ex, { 'P&L PDF export accepted': (x) => x.status === 202 });
  if (__ENV.PERIOD_ID) {
    const t0 = Date.now();
    const r = http.post(`${acc}/periods/${__ENV.PERIOD_ID}:soft-close`, '{}', headers(data.finance));
    closeTime.add(Date.now() - t0);
    check(r, { 'period soft-closed': (x) => x.status === 200 });
  }
}
