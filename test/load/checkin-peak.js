// k6 load test — morning check-in peak (PRD P1 FR-REL-04): Front Desk and
// Starter tablets look up bookings, check players in (through the offline
// sync endpoint, like the ops app) and poll the starter queue, while the
// tee sheet stream stays open.
//
//   k6 run -e BASE=https://api.staging.mgcc.oneclub.id -e PROPERTY=<uuid> -e COURSE=<uuid> \
//          -e DATE=2026-10-20 test/load/checkin-peak.js
//
// Prepare the day first (bookings for the morning) e.g. with the Rhapsody
// future_bookings import or the seed. Pass criteria: no 5xx, p95 < 1 s.
import http from 'k6/http';
import { check, sleep } from 'k6';

const BASE = __ENV.BASE || 'http://localhost:8080';
const PROPERTY = __ENV.PROPERTY;
const COURSE = __ENV.COURSE;
const DATE = __ENV.DATE;
const EMAIL = __ENV.EMAIL || 'golf.manager@demo.oneclub.id';
const PASSWORD = __ENV.PASSWORD || 'Demo#Club2026';

export const options = {
  scenarios: {
    desk: { executor: 'constant-vus', vus: 20, duration: '3m', exec: 'desk' },
    starter: { executor: 'constant-vus', vus: 5, duration: '3m', exec: 'starter' },
  },
  thresholds: { http_req_failed: ['rate<0.01'], http_req_duration: ['p(95)<1000'], checks: ['rate>0.99'] },
};

const headers = { 'Content-Type': 'application/json', 'X-Property-Id': PROPERTY, Origin: BASE };

export function setup() {
  const res = http.post(`${BASE}/api/v1/auth/login`, JSON.stringify({ email: EMAIL, password: PASSWORD }), { headers });
  check(res, { 'login ok': (r) => r.status === 200 });
  const cookies = {};
  for (const k of Object.keys(res.cookies)) cookies[k] = res.cookies[k][0].value;
  const bookings = http.get(`${BASE}/api/v1/golf/bookings?date=${DATE}&limit=500`, { headers, cookies }).json('items') || [];
  return { cookies, codes: bookings.filter((b) => b.status === 'confirmed').map((b) => b.code) };
}

function uuidv7() {
  const ts = Date.now().toString(16).padStart(12, '0');
  const rnd = () => Math.floor(Math.random() * 0x10000).toString(16).padStart(4, '0');
  return `${ts.slice(0, 8)}-${ts.slice(8, 12)}-7${rnd().slice(1)}-${(0x8000 | (Math.random() * 0x3fff)).toString(16)}-${rnd()}${rnd()}${rnd()}`;
}

export function desk(data) {
  if (data.codes.length === 0) return;
  const code = data.codes[Math.floor(Math.random() * data.codes.length)];
  const look = http.get(`${BASE}/api/v1/golf/check-ins:lookup?method=booking_code&value=${code}&date=${DATE}`, { headers, cookies: data.cookies, tags: { name: 'lookup' } });
  check(look, { 'lookup 200': (r) => r.status === 200 });
  // a repeated check-in of the same booking is accepted without duplicates
  const sync = http.post(`${BASE}/api/v1/platform/sync`, JSON.stringify({ items: [{ id: uuidv7(), action: 'golf.check_in',
    payload: { method: 'booking_code', value: code, date: DATE }, clientTime: new Date().toISOString() }] }), { headers, cookies: data.cookies, tags: { name: 'sync' } });
  check(sync, { 'sync 200': (r) => r.status === 200 });
  sleep(1);
}

export function starter(data) {
  const q = http.get(`${BASE}/api/v1/golf/starter-queue?courseId=${COURSE}&date=${DATE}`, { headers, cookies: data.cookies, tags: { name: 'queue' } });
  check(q, { 'queue 200': (r) => r.status === 200 });
  const sheet = http.get(`${BASE}/api/v1/golf/tee-sheet?courseId=${COURSE}&date=${DATE}`, { headers, cookies: data.cookies, tags: { name: 'tee-sheet' } });
  check(sheet, { 'tee sheet 200': (r) => r.status === 200 });
  sleep(2);
}
