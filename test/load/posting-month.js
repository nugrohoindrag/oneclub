// Posting throughput (PRD P4 FR-REL-P4-01 "posting 1 bulan volume transaksi",
// NFR Performance "posting lag < 5 min"): the setup posts a month's worth of
// billing documents on the property (default 12,000 charges on 1,200
// folios, half of them paid) — every charge and payment publishes an event
// that Accounting posts asynchronously (FR-PST-01) — then the scenario
// measures how long Accounting needs until no published event is left to
// post, while cashiers keep posting. Run on Staging with the book of the
// property open (Accounting → Setup) and the dispatcher running.
//
// Targets: posting lag (last event published → processed) < 5 min, no new
// posting exception caused by the load, cashier p95 < 800 ms, no 5xx.
//
//   k6 run -e BASE_URL=... -e PROPERTY_ID=... -e STAFF_EMAIL=... -e STAFF_PASSWORD=... \
//          -e FINANCE_EMAIL=... -e FINANCE_PASSWORD=... [-e TRANSACTIONS=12000] test/load/posting-month.js
import http from 'k6/http';
import { check, fail, sleep } from 'k6';
import { Trend } from 'k6/metrics';
import { BASE, headers, session, uuid } from './lib.js';

const lag = new Trend('posting_lag_ms', true);
const exceptionsAdded = new Trend('posting_exceptions_added');

export const options = {
  setupTimeout: '60m',
  scenarios: {
    cashiers: { executor: 'constant-arrival-rate', rate: 5, timeUnit: '1s', duration: '5m', preAllocatedVUs: 10, maxVUs: 40, exec: 'post' },
    posting: { executor: 'shared-iterations', iterations: 1, vus: 1, exec: 'drain', maxDuration: '15m' },
  },
  thresholds: {
    posting_lag_ms: ['max<300000'],
    posting_exceptions_added: ['max<1'],
    http_req_failed: ['rate<0.01'],
    'http_req_duration{scenario:cashiers}': ['p(95)<800'],
  },
};

function charge(cookie, folio, amount) {
  const r = http.post(`${BASE}/api/v1/billing/folios/${folio}/lines`, JSON.stringify({ chargeType: 'other', description: 'Posting load', unitPrice: String(amount) }),
    headers(cookie, { 'Idempotency-Key': uuid() }));
  check(r, { 'charge posted': (x) => x.status === 201 });
}

function openExceptions(cookie) {
  const r = http.get(`${BASE}/api/v1/accounting/posting-exceptions?filter[status]=open&limit=1000`, headers(cookie));
  return r.status === 200 ? JSON.parse(r.body).items.length : 0;
}

export function setup() {
  const staff = session(__ENV.STAFF_EMAIL, __ENV.STAFF_PASSWORD);
  const finance = session(__ENV.FINANCE_EMAIL || __ENV.STAFF_EMAIL, __ENV.FINANCE_PASSWORD || __ENV.STAFF_PASSWORD);
  const book = http.get(`${BASE}/api/v1/accounting/book`, headers(finance));
  if (book.status !== 200 || !JSON.parse(book.body).book) fail('open the accounting book of the property first (Accounting → Setup)');
  const n = Number(__ENV.TRANSACTIONS || 12000);
  for (let i = 0; i < n / 10; i++) {
    const f = http.post(`${BASE}/api/v1/billing/folios`, JSON.stringify({ holderName: `Posting load ${i}` }), headers(staff));
    if (f.status !== 201) fail(`folio: ${f.status} ${f.body}`);
    const id = JSON.parse(f.body).id;
    for (let j = 0; j < 10; j++) charge(staff, id, 50000 + j * 1000);
    if (i % 2 === 0) {
      http.post(`${BASE}/api/v1/billing/payments`, JSON.stringify({ folioId: id, methodType: 'cash', channel: 'venue' }), headers(staff, { 'Idempotency-Key': uuid() }));
    }
  }
  return { staff, finance, exceptions: openExceptions(finance), published: Date.now() };
}

let folio = null;

export function post(data) {
  if (!folio) {
    const f = http.post(`${BASE}/api/v1/billing/folios`, JSON.stringify({ holderName: `Walk-in ${uuid().slice(0, 8)}` }), headers(data.staff));
    folio = JSON.parse(f.body).id;
  }
  charge(data.staff, folio, 25000);
}

// drain waits until the processed events stop growing (every published
// event posted) and records the lag from the end of the setup.
export function drain(data) {
  let last = -1;
  let stable = 0;
  for (let i = 0; i < 180 && stable < 3; i++) {
    const r = http.get(`${BASE}/api/v1/accounting/processed-events?limit=1`, headers(data.finance));
    check(r, { 'processed events': (x) => x.status === 200 });
    const head = r.status === 200 ? JSON.stringify(JSON.parse(r.body).items[0] || {}) : '';
    const run = http.post(`${BASE}/api/v1/accounting/postings:run`, '{}', headers(data.finance));
    check(run, { 'posting run': (x) => x.status === 200 });
    const pending = run.status === 200 ? (JSON.parse(run.body).journals || []).length : 1;
    stable = head === last && pending === 0 ? stable + 1 : 0;
    last = head;
    sleep(5);
  }
  lag.add(Date.now() - data.published);
  exceptionsAdded.add(Math.max(0, openExceptions(data.finance) - data.exceptions));
  const rec = http.get(`${BASE}/api/v1/accounting/posting-reconciliation`, headers(data.finance));
  check(rec, { 'posting reconciliation available': (x) => x.status === 200 });
}
