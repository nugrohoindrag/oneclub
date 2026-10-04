// Court / class rush when the schedule opens (FR-REL-P2-01): many members
// book the same court slot and the same class session at once. Exactly one
// court booking may win; the class session never exceeds its capacity
// (Reservation Engine EXCLUDE / capacity CHECK, FR-RSV-02).
//
//   k6 run -e BASE_URL=... -e PROPERTY_ID=... -e COURT_ID=... -e SESSION_ID=... \
//          -e STAFF_EMAIL=... -e STAFF_PASSWORD=... -e CUSTOMER_IDS=id1,id2,... test/load/court-rush.js
import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';
import { BASE, headers, session, uuid } from './lib.js';

const won = new Counter('court_booked');
const conflicts = new Counter('court_conflict');
const seats = new Counter('class_seat_booked');

export const options = {
  scenarios: {
    rush: { executor: 'shared-iterations', vus: Number(__ENV.VUS || 100), iterations: Number(__ENV.ITERATIONS || 100), maxDuration: '2m' },
  },
  thresholds: {
    http_req_duration: ['p(95)<800'],
    court_booked: ['count==1'],
    http_req_failed: ['rate<0.05'],
  },
};

export function setup() {
  return { cookie: session(__ENV.STAFF_EMAIL, __ENV.STAFF_PASSWORD) };
}

export default function (data) {
  const customers = (__ENV.CUSTOMER_IDS || '').split(',').filter(Boolean);
  const customerId = customers[__ITER % Math.max(customers.length, 1)];
  const start = __ENV.SLOT_START; // RFC 3339, same slot for everybody
  const end = __ENV.SLOT_END;
  const r = http.post(`${BASE}/api/v1/sportclub/bookings`, JSON.stringify({ courtId: __ENV.COURT_ID, start, end, customerId, hold: true }),
    headers(data.cookie, { 'Idempotency-Key': uuid() }));
  if (r.status === 201) won.add(1);
  else if (r.status === 409) conflicts.add(1);
  check(r, { 'court: 201 or 409': (x) => x.status === 201 || x.status === 409 });

  if (__ENV.SESSION_ID && customerId) {
    const s = http.post(`${BASE}/api/v1/sportclub/session-bookings`, JSON.stringify({ sessionId: __ENV.SESSION_ID, customerId }), headers(data.cookie));
    if (s.status === 201) seats.add(1);
    check(s, { 'class: 201, 409 or 422': (x) => [201, 409, 422].includes(x.status) });
  }
}
