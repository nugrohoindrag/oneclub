// POS peak during an event (FR-REL-P2-01): cashiers at several outlets
// create, send and pay orders continuously; half of them arrive through the
// offline sync queue at the same time.
//
//   k6 run -e BASE_URL=... -e PROPERTY_ID=... -e OUTLET_IDS=... -e PRODUCT_IDS=... -e SHIFT_IDS=... \
//          -e STAFF_EMAIL=... -e STAFF_PASSWORD=... test/load/pos-peak.js
import http from 'k6/http';
import { check, sleep } from 'k6';
import { BASE, headers, session, uuid } from './lib.js';

export const options = {
  scenarios: {
    peak: { executor: 'ramping-arrival-rate', startRate: 5, timeUnit: '1s', preAllocatedVUs: 50, maxVUs: 200,
      stages: [{ target: 30, duration: '1m' }, { target: 30, duration: '3m' }, { target: 0, duration: '30s' }] },
  },
  thresholds: { http_req_duration: ['p(95)<600', 'p(99)<1500'], http_req_failed: ['rate<0.01'] },
};

export function setup() {
  return { cookie: session(__ENV.STAFF_EMAIL, __ENV.STAFF_PASSWORD) };
}

const pick = (list) => list[Math.floor(Math.random() * list.length)];

export default function (data) {
  const outlets = __ENV.OUTLET_IDS.split(',');
  const shifts = (__ENV.SHIFT_IDS || '').split(',');
  const products = __ENV.PRODUCT_IDS.split(',');
  const i = Math.floor(Math.random() * outlets.length);
  const order = { id: uuid(), outletId: outlets[i], shiftId: shifts[i] || undefined, send: true,
    lines: [{ productId: pick(products), quantity: '1' }, { productId: pick(products), quantity: '2' }] };
  const payment = { shiftId: shifts[i] || undefined, tenders: [{ methodType: Math.random() < 0.5 ? 'cash' : 'qris' }] };
  if (Math.random() < 0.5) {
    // offline terminal flushing its queue
    const r = http.post(`${BASE}/api/v1/platform/sync`, JSON.stringify({ items: [{ id: uuid(), action: 'commercial.pos_order', payload: { order, payment } }] }),
      headers(data.cookie));
    check(r, { 'sync accepted': (x) => x.status === 200 && JSON.parse(x.body).results[0].status !== 'rejected' });
  } else {
    const o = http.post(`${BASE}/api/v1/commercial/orders`, JSON.stringify(order), headers(data.cookie, { 'Idempotency-Key': uuid() }));
    if (!check(o, { 'order created': (x) => x.status === 201 })) return;
    const id = JSON.parse(o.body).id;
    const p = http.post(`${BASE}/api/v1/commercial/orders/${id}:pay`, JSON.stringify(payment), headers(data.cookie, { 'Idempotency-Key': uuid() }));
    check(p, { 'order paid': (x) => x.status === 200 });
  }
  sleep(0.1);
}
