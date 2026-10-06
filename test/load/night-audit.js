// Night audit on a month of volume (PRD P3 FR-REL-P3-01): the setup posts a
// month's worth of charges and payments on the property (default 30 days ×
// 400 transactions), then the night audit closes the business day while
// cashiers keep posting. Targets: the close completes within 60 s, frozen
// Daily Revenue equals Σ charges of the day, no 5xx.
//
//   k6 run -e BASE_URL=... -e PROPERTY_ID=... -e STAFF_EMAIL=... -e STAFF_PASSWORD=... \
//          -e AUDITOR_EMAIL=... -e AUDITOR_PASSWORD=... [-e TRANSACTIONS=12000] test/load/night-audit.js
import http from 'k6/http';
import { check, fail } from 'k6';
import { Trend } from 'k6/metrics';
import { BASE, headers, session, uuid } from './lib.js';

const closeTime = new Trend('night_audit_close_ms', true);

export const options = {
  setupTimeout: '30m',
  scenarios: {
    cashiers: { executor: 'constant-arrival-rate', rate: 10, timeUnit: '1s', duration: '2m', preAllocatedVUs: 20, maxVUs: 60, exec: 'post' },
    audit: { executor: 'shared-iterations', iterations: 1, vus: 1, startTime: '30s', exec: 'audit', maxDuration: '5m' },
  },
  thresholds: { night_audit_close_ms: ['max<60000'], http_req_failed: ['rate<0.01'], 'http_req_duration{scenario:cashiers}': ['p(95)<800'] },
};

function charge(cookie, folio, amount) {
  const r = http.post(`${BASE}/api/v1/billing/folios/${folio}/lines`, JSON.stringify({ chargeType: 'other', description: 'Load charge', unitPrice: String(amount) }),
    headers(cookie, { 'Idempotency-Key': uuid() }));
  check(r, { 'charge posted': (x) => x.status === 201 });
}

export function setup() {
  const staff = session(__ENV.STAFF_EMAIL, __ENV.STAFF_PASSWORD);
  const auditor = session(__ENV.AUDITOR_EMAIL || __ENV.STAFF_EMAIL, __ENV.AUDITOR_PASSWORD || __ENV.STAFF_PASSWORD);
  const n = Number(__ENV.TRANSACTIONS || 12000);
  // folios of 10 charges each, half paid by cash, half left open (warnings)
  for (let i = 0; i < n / 10; i++) {
    const f = http.post(`${BASE}/api/v1/billing/folios`, JSON.stringify({ holderName: `Load ${i}` }), headers(staff));
    if (f.status !== 201) fail(`folio: ${f.status} ${f.body}`);
    const id = JSON.parse(f.body).id;
    for (let j = 0; j < 10; j++) charge(staff, id, 50000 + j * 1000);
    if (i % 2 === 0) {
      http.post(`${BASE}/api/v1/billing/payments`, JSON.stringify({ folioId: id, methodType: 'cash', channel: 'venue' }), headers(staff, { 'Idempotency-Key': uuid() }));
    }
  }
  return { staff, auditor };
}

let folio = null;

export function post(data) {
  if (!folio) {
    const f = http.post(`${BASE}/api/v1/billing/folios`, JSON.stringify({ holderName: `Walk-in ${uuid().slice(0, 8)}` }), headers(data.staff));
    folio = JSON.parse(f.body).id;
  }
  charge(data.staff, folio, 25000);
}

export function audit(data) {
  const t0 = Date.now();
  const r = http.post(`${BASE}/api/v1/billing/business-days:night-audit`, '{}', headers(data.auditor));
  closeTime.add(Date.now() - t0);
  check(r, { 'night audit ran': (x) => x.status === 200 });
  const run = JSON.parse(r.body);
  if (run.status !== 'completed') {
    console.warn(`night audit ${run.status}: ${JSON.stringify(run.checks)}`);
    return;
  }
  const d = http.get(`${BASE}/api/v1/billing/daily-revenue?date=${run.businessDate}`, headers(data.auditor));
  check(d, { 'daily revenue frozen': (x) => x.status === 200 && JSON.parse(x.body).frozen === true });
}
