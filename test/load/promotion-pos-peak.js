// Promotions at the POS peak (PRD P3 FR-REL-P3-01, NFR "apply promo p95 <
// 800 ms"): during happy hour cashiers at several outlets sell items with
// active promotions (Buy N Price X, percentage, promo codes); a third of the
// sales arrive through the offline sync queue with the total the terminal
// computed from its promotion cache (the server prices them at the client
// time and flags differences), the rest are created online, re-priced with
// a promo code (Apply Promotion) and paid. Terminals refresh the promotion
// cache of their shift and preview carts with the evaluation endpoint.
//
//   k6 run -e BASE_URL=... -e PROPERTY_ID=... -e OUTLET_IDS=... -e PRODUCT_IDS=... -e SHIFT_IDS=... \
//          -e PROMO_CODES=HH2026,WELCOME15 -e STAFF_EMAIL=... -e STAFF_PASSWORD=... test/load/promotion-pos-peak.js
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Trend } from 'k6/metrics';
import { BASE, headers, session, uuid } from './lib.js';

const applyPromo = new Trend('apply_promotion_duration', true);
const evaluate = new Trend('promotion_evaluate_duration', true);

export const options = {
  scenarios: {
    peak: { executor: 'ramping-arrival-rate', startRate: 5, timeUnit: '1s', preAllocatedVUs: 50, maxVUs: 250,
      stages: [{ target: 40, duration: '1m' }, { target: 40, duration: '4m' }, { target: 0, duration: '30s' }] },
    cache: { executor: 'constant-vus', vus: 5, duration: '5m30s', exec: 'refreshCache' },
  },
  thresholds: {
    http_req_duration: ['p(95)<800', 'p(99)<1500'],
    http_req_failed: ['rate<0.01'],
    apply_promotion_duration: ['p(95)<800'],
    promotion_evaluate_duration: ['p(95)<300'],
  },
};

export function setup() {
  return { cookie: session(__ENV.STAFF_EMAIL, __ENV.STAFF_PASSWORD) };
}

const pick = (list) => list[Math.floor(Math.random() * list.length)];
const list = (v) => (v || '').split(',').filter(Boolean);

/** An offline terminal refreshing the promotion cache of its shift. */
export function refreshCache(data) {
  const r = http.get(`${BASE}/api/v1/commercial/pos/promotions?outletId=${pick(list(__ENV.OUTLET_IDS))}`, headers(data.cookie));
  check(r, { 'promotion cache': (x) => x.status === 200 });
  sleep(5);
}

export default function (data) {
  const outlets = list(__ENV.OUTLET_IDS);
  const shifts = list(__ENV.SHIFT_IDS);
  const products = list(__ENV.PRODUCT_IDS);
  const codes = list(__ENV.PROMO_CODES);
  const i = Math.floor(Math.random() * outlets.length);
  const lines = [{ productId: pick(products), quantity: '2' }, { productId: pick(products), quantity: '1' }];
  // cart preview with the promotions of the outlet (POS screen)
  const ev = http.post(`${BASE}/api/v1/commercial/promotions:evaluate`, JSON.stringify({ channel: 'pos', businessLine: 'pos', outletId: outlets[i],
    lines: lines.map((l, k) => ({ key: String(k), productId: l.productId, quantity: l.quantity, unitPrice: '100000' })) }), headers(data.cookie));
  evaluate.add(ev.timings.duration);
  check(ev, { 'evaluated': (x) => x.status === 200 });
  const payment = { shiftId: shifts[i] || undefined, tenders: [{ methodType: Math.random() < 0.5 ? 'cash' : 'qris' }] };
  if (Math.random() < 0.33) {
    // offline terminal flushing its queue: client time and cached-promotion total
    const order = { id: uuid(), outletId: outlets[i], shiftId: shifts[i] || undefined, send: true, lines,
      clientCreatedAt: new Date(Date.now() - Math.random() * 600000).toISOString(), clientTotal: ev.status === 200 ? JSON.parse(ev.body).total : undefined,
      promoCodes: codes.length && Math.random() < 0.3 ? [pick(codes)] : undefined };
    const r = http.post(`${BASE}/api/v1/platform/sync`, JSON.stringify({ items: [{ id: uuid(), action: 'commercial.pos_order', payload: { order, payment } }] }),
      headers(data.cookie));
    check(r, { 'sync accepted': (x) => x.status === 200 && JSON.parse(x.body).results[0].status !== 'rejected' });
  } else {
    const o = http.post(`${BASE}/api/v1/commercial/orders`, JSON.stringify({ id: uuid(), outletId: outlets[i], shiftId: shifts[i] || undefined, send: true, lines }),
      headers(data.cookie, { 'Idempotency-Key': uuid() }));
    if (!check(o, { 'order created': (x) => x.status === 201 })) return;
    const id = JSON.parse(o.body).id;
    if (codes.length && Math.random() < 0.5) {
      const a = http.post(`${BASE}/api/v1/commercial/orders/${id}:apply-promotion`, JSON.stringify({ promoCode: pick(codes) }), headers(data.cookie));
      applyPromo.add(a.timings.duration);
      check(a, { 'promotion applied': (x) => x.status === 200 });
    }
    const p = http.post(`${BASE}/api/v1/commercial/orders/${id}:pay`, JSON.stringify(payment), headers(data.cookie, { 'Idempotency-Key': uuid() }));
    check(p, { 'order paid': (x) => x.status === 200 });
  }
  sleep(0.1);
}
