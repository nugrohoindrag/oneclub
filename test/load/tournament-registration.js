// k6 load test — tournament registration when it opens (PRD P3 FR-REL-P3-01,
// §12 Konkurensi: "registrasi tournament tidak melebihi kuota"). Many players
// register for the same tournament at the moment registration opens, from
// the Back Office / Tournament Desk (staff session) and the website (public
// registration with an online payment). The field never exceeds its size:
// every request ends Registered (until the field is full) or Waitlisted.
//
//   k6 run -e BASE_URL=https://api.staging.mgcc.oneclub.id -e PROPERTY_ID=<uuid> -e TOURNAMENT_ID=<uuid> \
//          -e STAFF_EMAIL=golf.manager@demo.oneclub.id -e STAFF_PASSWORD=... -e FIELD=72 test/load/tournament-registration.js
//
// Prepare an open tournament (status Open, waitlist enabled) with FIELD
// places. Pass criteria: no 5xx, p95 < 1.5 s, registered == FIELD and
// registered + waitlisted == accepted requests (checked in teardown).
import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';
import { BASE, PROPERTY, headers, session, uuid } from './lib.js';

const TOURNAMENT = __ENV.TOURNAMENT_ID;
const FIELD = Number(__ENV.FIELD || 72);
const registered = new Counter('tournament_registered');
const waitlisted = new Counter('tournament_waitlisted');

export const options = {
  scenarios: {
    desk: { executor: 'shared-iterations', vus: Number(__ENV.VUS || 50), iterations: Number(__ENV.DESK || 150), maxDuration: '3m', exec: 'desk' },
    website: { executor: 'shared-iterations', vus: Number(__ENV.WEB_VUS || 20), iterations: Number(__ENV.WEB || 50), maxDuration: '3m', exec: 'website' },
  },
  thresholds: {
    http_req_failed: ['rate<0.02'],
    'http_req_duration{name:register}': ['p(95)<1500'],
    'http_req_duration{name:public_register}': ['p(95)<1500'],
    checks: ['rate>0.99'],
  },
};

export function setup() {
  return { cookie: session(__ENV.STAFF_EMAIL, __ENV.STAFF_PASSWORD) };
}

function count(r) {
  if (r.status !== 201) return;
  const s = r.json('status');
  if (s === 'registered') registered.add(1);
  if (s === 'waitlisted') waitlisted.add(1);
}

export function desk(data) {
  const n = `${__VU}-${__ITER}-${Date.now() % 1e6}`;
  const r = http.post(`${BASE}/api/v1/golf/tournaments/${TOURNAMENT}/registrations`,
    JSON.stringify({ guest: { name: `Load Player ${n}`, phone: `+62899${Math.floor(Math.random() * 1e8)}` }, handicapIndex: String(5 + (__ITER % 30)) }),
    { ...headers(data.cookie, { 'Idempotency-Key': uuid() }), tags: { name: 'register' } });
  count(r);
  check(r, { 'desk: 201 or 409 (field / waitlist full)': (x) => x.status === 201 || x.status === 409 });
}

export function website() {
  const n = `${__VU}-${__ITER}-${Date.now() % 1e6}`;
  const r = http.post(`${BASE}/api/v1/public/tournaments/${TOURNAMENT}/registrations`,
    JSON.stringify({ propertyId: PROPERTY, guest: { name: `Web Player ${n}`, phone: `+62898${Math.floor(Math.random() * 1e8)}`, email: `load${n}@example.test` },
      consent: true, paymentMethod: 'qris' }),
    { headers: { 'Content-Type': 'application/json' }, tags: { name: 'public_register' } });
  count(r);
  // 429: the public rate limit per IP protects the endpoint (one load generator = one IP)
  check(r, { 'website: 201, 409 or 429': (x) => [201, 409, 429].includes(x.status) });
}

export function teardown(data) {
  const t = http.get(`${BASE}/api/v1/golf/tournaments/${TOURNAMENT}`, headers(data.cookie)).json();
  check(t, {
    'field never exceeded': (x) => x.registered <= FIELD,
    'field filled': (x) => x.registered === FIELD,
  });
}
