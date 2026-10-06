// BI queries on 2 years of data (PRD P5 FR-REL-P5-01, §12 NFR "query BI p95 < 5 detik di analytics store"): management users
// open the Executive Overview (month, MoM / YoY), 24-month trends, drill-downs from Golf Revenue to the folio lines (FR-BI-04),
// HR Performance and report builder queries over two years, concurrently, on the read replica.
//
//   k6 run -e BASE_URL=... -e PROPERTY_ID=... -e STAFF_EMAIL=<General Manager> -e STAFF_PASSWORD=... [-e VUS=20] [-e DURATION=10m] \
//          test/load/bi-2y.js
//
// Run on Staging with a production-size copy whose analytics store holds 24 months (backfill with
// POST /api/v1/reporting/analytics:refresh month by month, or the nightly backfill of the BI Policies).
import http from 'k6/http';
import { check, sleep } from 'k6';
import { BASE, headers, session } from './lib.js';

const VUS = Number(__ENV.VUS || 20);

export const options = {
  scenarios: { managers: { executor: 'constant-vus', vus: VUS, duration: __ENV.DURATION || '10m' } },
  thresholds: {
    'http_req_duration{kind:bi}': ['p(95)<5000'],
    http_req_failed: ['rate<0.01'],
    checks: ['rate>0.99'],
  },
};

const iso = (d) => d.toISOString().slice(0, 10);

export function setup() {
  return { cookie: session(__ENV.STAFF_EMAIL, __ENV.STAFF_PASSWORD) };
}

export default function (data) {
  const h = Object.assign(headers(data.cookie), { tags: { kind: 'bi' } });
  const today = new Date();
  const from2y = new Date(today);
  from2y.setFullYear(today.getFullYear() - 2);
  const m = new Date(today);
  m.setMonth(today.getMonth() - Math.floor(Math.random() * 24));
  const month = `${m.getFullYear()}-${String(m.getMonth() + 1).padStart(2, '0')}`;
  const calls = [
    `/api/v1/reporting/executive?period=month&month=${month}`,
    `/api/v1/reporting/executive/trend?kpi=golf_revenue&months=24`,
    `/api/v1/reporting/executive/trend?kpi=total_sales&months=24`,
    `/api/v1/reporting/drilldown?kpi=golf_revenue&from=${month}-01&to=${month}-28&by=day`,
    `/api/v1/reporting/drilldown?kpi=golf_revenue&from=${month}-01&to=${month}-28&by=revenue_component`,
    `/api/v1/reporting/hr-performance?from=${iso(from2y)}&to=${iso(today)}`,
    `/api/v1/reporting/datasets/revenue/query?dimensions=month,business_line&metrics=amount,lines&from=${iso(from2y)}&to=${iso(today)}`,
    `/api/v1/reporting/datasets/kpi_targets/query?dimensions=month,kpi&metrics=actual,target,achievement&from=${iso(from2y)}&to=${iso(today)}`,
    `/api/v1/reporting/reports/reporting.kpi_target_vs_actual?params[from]=${iso(from2y)}&params[to]=${iso(today)}`,
  ];
  for (const path of calls) {
    const r = http.get(`${BASE}${path}`, h);
    check(r, { [`${path.split('?')[0]} ok`]: (x) => x.status === 200 });
  }
  sleep(1 + Math.random() * 2);
}
