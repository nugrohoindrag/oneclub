// Simultaneous voucher redemption (FR-REL-P2-01): several terminals redeem
// the same 5x voucher at the same moment. The voucher must never go below
// zero and no unit may be used twice (row lock + idempotency, FR-VCH-06).
//
//   k6 run -e BASE_URL=... -e PROPERTY_ID=... -e VOUCHER_CODE=... -e STAFF_EMAIL=... -e STAFF_PASSWORD=... test/load/voucher-redeem.js
import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';
import { BASE, headers, session, uuid } from './lib.js';

const redeemed = new Counter('voucher_units_redeemed');

export const options = {
  scenarios: { burst: { executor: 'shared-iterations', vus: 20, iterations: Number(__ENV.ITERATIONS || 40), maxDuration: '1m' } },
  thresholds: {
    voucher_units_redeemed: [`count<=${Number(__ENV.UNITS || 5)}`],
    http_req_duration: ['p(95)<800'],
  },
};

export function setup() {
  return { cookie: session(__ENV.STAFF_EMAIL, __ENV.STAFF_PASSWORD) };
}

export default function (data) {
  const r = http.post(`${BASE}/api/v1/commercial/vouchers:redeem`, JSON.stringify({ code: __ENV.VOUCHER_CODE, quantity: '1', terminal: `k6-${__VU}` }),
    headers(data.cookie, { 'Idempotency-Key': uuid() }));
  if (r.status === 200 || r.status === 201) redeemed.add(1);
  check(r, { 'redeemed or refused cleanly': (x) => [200, 201, 409, 422].includes(x.status) });
}
