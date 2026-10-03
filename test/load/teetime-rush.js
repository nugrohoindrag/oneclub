// k6 load test — tee time rush when the booking window opens (PRD P1
// FR-REL-04, Technical Doc §12.2). Many members try to hold the same few
// prime tee times at the same moment.
//
//   k6 run -e BASE=https://api.staging.mgcc.oneclub.id -e PROPERTY=<uuid> \
//          -e COURSE=<uuid> -e DATE=2026-10-20 -e USERS=member@demo.oneclub.id test/load/teetime-rush.js
//
// Pass criteria: no 5xx, p95 hold latency < 1 s, and never more held seats
// than capacity (checked afterwards with the tee sheet).
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';

const BASE = __ENV.BASE || 'http://localhost:8080';
const PROPERTY = __ENV.PROPERTY;
const COURSE = __ENV.COURSE;
const DATE = __ENV.DATE;
const PASSWORD = __ENV.PASSWORD || 'Demo#Club2026';
const USERS = (__ENV.USERS || 'golf.manager@demo.oneclub.id').split(',');

const held = new Counter('holds_granted');
const full = new Counter('holds_rejected_full');
const holdTime = new Trend('hold_latency', true);

export const options = {
  scenarios: {
    rush: { executor: 'ramping-arrival-rate', startRate: 10, timeUnit: '1s', preAllocatedVUs: 200, maxVUs: 600,
      stages: [{ target: 300, duration: '30s' }, { target: 300, duration: '60s' }, { target: 0, duration: '15s' }] },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    'http_req_duration{name:hold}': ['p(95)<1000'],
    'http_req_duration{name:availability}': ['p(95)<800'],
    checks: ['rate>0.99'],
  },
};

const headers = { 'Content-Type': 'application/json', 'X-Property-Id': PROPERTY, Origin: BASE };

export function setup() {
  const jars = USERS.map((email) => {
    const res = http.post(`${BASE}/api/v1/auth/login`, JSON.stringify({ email, password: PASSWORD }), { headers });
    check(res, { 'login ok': (r) => r.status === 200 });
    return res.cookies;
  });
  const slots = http.get(`${BASE}/api/v1/golf/tee-times?date=${DATE}&courseId=${COURSE}`, { headers, cookies: flatten(jars[0]) }).json('items') || [];
  // the five earliest morning slots are the "prime" ones everybody wants
  const prime = slots.filter((s) => s.session === 'morning').slice(0, 5).map((s) => s.id);
  return { jars, prime };
}

function flatten(c) {
  const out = {};
  for (const k of Object.keys(c || {})) out[k] = c[k][0].value;
  return out;
}

export default function (data) {
  const cookies = flatten(data.jars[__VU % data.jars.length]);
  const avail = http.get(`${BASE}/api/v1/golf/availability?date=${DATE}&courseId=${COURSE}&players=2`, { headers, cookies, tags: { name: 'availability' } });
  check(avail, { 'availability 200': (r) => r.status === 200 });
  const slot = data.prime[Math.floor(Math.random() * data.prime.length)];
  const res = http.post(`${BASE}/api/v1/golf/holds`, JSON.stringify({ teeTimeId: slot, players: 1 + Math.floor(Math.random() * 2) }),
    { headers, cookies, tags: { name: 'hold' } });
  holdTime.add(res.timings.duration);
  check(res, { 'hold granted or full (never 5xx)': (r) => r.status === 201 || r.status === 409 || r.status === 429 });
  if (res.status === 201) {
    held.add(1);
    // release most holds again, like members who abandon the checkout
    if (Math.random() < 0.8) http.del(`${BASE}/api/v1/golf/holds/${res.json('id')}`, null, { headers, cookies, tags: { name: 'release' } });
  } else if (res.status === 409) {
    full.add(1);
  }
  sleep(0.2);
}
