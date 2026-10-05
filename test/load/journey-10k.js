// Journey to 10,000 customers (PRD P5 FR-REL-P5-01, FR-JRN-01/05/07): a win-back journey (§9.4) is activated and 10.000
// customers are enrolled; the journey engine runs its steps in throttled rounds until every enrollment has executed its first
// message step. Consent, suppression and the frequency cap (§16 #15: 2 marketing messages a week) apply at every step: no customer
// may get more than the cap, and every run round answers in time.
//
//   k6 run -e BASE_URL=... -e PROPERTY_ID=... -e STAFF_EMAIL=<CRM Admin> -e STAFF_PASSWORD=... \
//          [-e SEGMENT_ID=<static segment with 10.000 members>] [-e RECIPIENTS=10000] test/load/journey-10k.js
//
// Without SEGMENT_ID the setup creates RECIPIENTS customers with marketing consent and a static segment of them (allow ~10 min).
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';
import { BASE, headers, session, uuid } from './lib.js';

const RECIPIENTS = Number(__ENV.RECIPIENTS || 10000);
const runTime = new Trend('journey_run_ms', true);
const completed = new Counter('journey_first_step_done');
const overCap = new Counter('journey_customers_over_frequency_cap');

export const options = {
  setupTimeout: '20m',
  scenarios: { journey: { executor: 'per-vu-iterations', vus: 1, iterations: 1, maxDuration: '40m' } },
  thresholds: {
    journey_first_step_done: ['count==1'],
    journey_customers_over_frequency_cap: ['count==0'],
    'http_req_duration{kind:run}': ['p(95)<60000'],
    'http_req_duration{kind:api}': ['p(95)<1500'],
    checks: ['rate==1'],
  },
};

function post(cookie, path, body, kind = 'api') {
  return http.post(`${BASE}${path}`, JSON.stringify(body ?? {}), Object.assign(headers(cookie, { 'Idempotency-Key': uuid() }), { tags: { kind } }));
}

export function setup() {
  const cookie = session(__ENV.STAFF_EMAIL, __ENV.STAFF_PASSWORD);
  const run = `${Date.now() % 1e7}`;
  const ids = [];
  let segment = __ENV.SEGMENT_ID;
  if (segment) {
    for (let cursor = ''; ;) {
      const p = http.get(`${BASE}/api/v1/crm/segments/${segment}/members?limit=1000${cursor ? `&cursor=${cursor}` : ''}`, headers(cookie)).json();
      for (const m of p.items) ids.push(m.customerId ?? m.id);
      if (!p.nextCursor) break;
      cursor = p.nextCursor;
    }
  } else {
    for (let i = 0; i < RECIPIENTS; i++) {
      const r = post(cookie, '/api/v1/crm/customers', { code: `K6J${run}-${i}`, name: `Journey Guest ${i}`, email: `k6j-${run}-${i}@load.test`,
        marketingOptIn: true, duplicateAcknowledged: true });
      check(r, { 'customer created': (x) => x.status === 201 });
      ids.push(r.json('id'));
    }
  }
  return { cookie, ids, run };
}

export default function (data) {
  const j = post(data.cookie, '/api/v1/crm/journeys:from-template', { template: 'win_back', code: `K6WB${data.run}`, name: `Load win-back ${data.run}` });
  check(j, { 'journey created': (x) => x.status === 201 || x.status === 200 });
  const id = j.json('id');
  const act = post(data.cookie, `/api/v1/crm/journeys/${id}:activate`, {});
  check(act, { 'journey active (approved below the threshold)': (x) => x.status === 200 && x.json('status') === 'active' });
  let enrolled = 0;
  for (let i = 0; i < data.ids.length; i += 1000) {
    const r = post(data.cookie, `/api/v1/crm/journeys/${id}/enrollments`, { customerIds: data.ids.slice(i, i + 1000), occurrence: `k6:${data.run}` });
    check(r, { 'enrolled': (x) => x.status === 200 });
    enrolled += r.json('enrolled') || 0;
  }
  check(null, { 'every customer enrolled once': () => enrolled === data.ids.length });
  for (let round = 0; round < 60; round++) {
    const r = post(data.cookie, `/api/v1/crm/journeys/${id}:run`, {}, 'run');
    runTime.add(r.timings.duration);
    check(r, { 'run answered': (x) => x.status === 200 });
    const perf = http.get(`${BASE}/api/v1/crm/journeys/${id}/performance`, Object.assign(headers(data.cookie), { tags: { kind: 'api' } })).json();
    const steps = perf.steps || [];
    const first = steps.find((s) => s.stepType === 'message') || steps[0];
    if (first && first.executed >= enrolled) {
      completed.add(1);
      break;
    }
    sleep(30);
  }
  // frequency cap: no customer of the run got more marketing messages this week than the cap
  const events = http.get(`${BASE}/api/v1/crm/journeys/${id}/events?limit=1000&filter[outcome]=sent`, headers(data.cookie)).json('items') || [];
  const per = {};
  for (const e of events) per[e.customerId] = (per[e.customerId] || 0) + 1;
  for (const n of Object.values(per)) if (n > 2) overCap.add(1);
}
