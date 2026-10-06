// Stock opname (PRD P4 FR-REL-P4-01 / FR-REL-P4-02 §9.3, FR-MIG-P4-07
// cut-over opname): one opname of a large warehouse (default: every item
// with stock) counted from several handheld devices at once — barcode or
// item counts with offline replays of the same Idempotency-Key — then
// submitted and posted (the difference journal is posted by Accounting).
//
// Targets: count p95 < 800 ms with 8 counters, replayed counts counted
// once, post < 30 s, no 5xx. Use a Staging warehouse (the opname freezes
// it while counting when freeze=true).
//
//   k6 run -e BASE_URL=... -e PROPERTY_ID=... -e STAFF_EMAIL=... -e STAFF_PASSWORD=... \
//          -e MANAGER_EMAIL=... -e MANAGER_PASSWORD=... -e WAREHOUSE_ID=... [-e COUNTERS=8] test/load/opname.js
import http from 'k6/http';
import { check, fail, sleep } from 'k6';
import exec from 'k6/execution';
import { Trend } from 'k6/metrics';
import { BASE, headers, session, uuid } from './lib.js';

const postTime = new Trend('opname_post_ms', true);
const COUNTERS = Number(__ENV.COUNTERS || 8);

export const options = {
  setupTimeout: '5m',
  scenarios: {
    count: { executor: 'per-vu-iterations', vus: COUNTERS, iterations: 1, exec: 'count', maxDuration: '20m' },
    post: { executor: 'shared-iterations', iterations: 1, vus: 1, exec: 'post', startTime: '5s', maxDuration: '25m' },
  },
  thresholds: {
    'http_req_duration{name:count}': ['p(95)<800'],
    opname_post_ms: ['max<30000'],
    http_req_failed: ['rate<0.01'],
  },
};

export function setup() {
  if (!__ENV.WAREHOUSE_ID) fail('WAREHOUSE_ID is required');
  const staff = session(__ENV.STAFF_EMAIL, __ENV.STAFF_PASSWORD);
  const manager = session(__ENV.MANAGER_EMAIL || __ENV.STAFF_EMAIL, __ENV.MANAGER_PASSWORD || __ENV.STAFF_PASSWORD);
  const r = http.post(`${BASE}/api/v1/inventory/stock-opnames`, JSON.stringify({ warehouseId: __ENV.WAREHOUSE_ID, freeze: true, blind: true }),
    headers(staff, { 'Idempotency-Key': uuid() }));
  if (r.status !== 201) fail(`start opname: ${r.status} ${r.body}`);
  const o = JSON.parse(r.body);
  const items = [...new Set((o.lines || []).map((l) => l.itemId))];
  if (items.length === 0) fail('the warehouse has no stock to count');
  return { staff, manager, id: o.id, items };
}

// count: each counter takes its share of the items in batches of 20 and
// replays every second batch with the same key (offline sync).
export function count(data) {
  const vu = exec.vu.idInTest - 1;
  const mine = data.items.filter((_, i) => i % COUNTERS === vu);
  for (let i = 0; i < mine.length; i += 20) {
    const body = JSON.stringify({ lines: mine.slice(i, i + 20).map((itemId) => ({ itemId, quantity: String(1 + (i % 7)) })) });
    const key = uuid();
    const url = `${BASE}/api/v1/inventory/stock-opnames/${data.id}:count`;
    check(http.post(url, body, Object.assign(headers(data.staff, { 'Idempotency-Key': key }), { tags: { name: 'count' } })), { counted: (x) => x.status === 200 });
    if ((i / 20) % 2 === 1) {
      check(http.post(url, body, Object.assign(headers(data.staff, { 'Idempotency-Key': key }), { tags: { name: 'count' } })), { 'replay accepted': (x) => x.status === 200 });
    }
  }
}

// post: waits until every line is counted, submits and posts the opname.
export function post(data) {
  const url = `${BASE}/api/v1/inventory/stock-opnames/${data.id}`;
  for (let i = 0; i < 450; i++) {
    const o = JSON.parse(http.get(url, headers(data.manager)).body);
    if ((o.lines || []).every((l) => l.countedQuantity !== null && l.countedQuantity !== undefined)) break;
    sleep(2);
  }
  check(http.post(`${url}:submit`, '{}', headers(data.staff)), { submitted: (x) => x.status === 200 });
  const t0 = Date.now();
  const r = http.post(`${url}:post`, '{}', headers(data.manager));
  postTime.add(Date.now() - t0);
  check(r, { 'posted or waiting for approval': (x) => x.status === 200 && ['posted', 'pending_approval'].includes(JSON.parse(x.body).status) });
}
