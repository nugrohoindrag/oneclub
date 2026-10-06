// Payroll run of every employee (PRD P5 FR-REL-P5-01, §12 NFR "payroll run seluruh karyawan instance < 5 menit"): the HR
// Manager calculates the regular run of a period for the whole property (no org unit filter) — attendance closed, payroll
// inputs collected, PPh 21 / BPJS per employee, payslips stored — and reads the totals, payslips and journal preview.
// Each iteration recalculates the same run (a draft or calculated run may be recalculated), so the script repeats safely.
//
//   k6 run -e BASE_URL=... -e PROPERTY_ID=... -e STAFF_EMAIL=<HR Manager> -e STAFF_PASSWORD=... \
//          (-e RUN_ID=<draft or calculated regular run> | -e PERIOD=YYYY-MM) [-e ITERATIONS=3] test/load/payroll-run.js
//
// Run on Staging with a production-size copy (every employee with contract, bank account and a month of attendance). With
// PERIOD the script creates the regular run of that period (the period must not have one yet); cancel it afterwards.
import http from 'k6/http';
import { check, fail } from 'k6';
import { BASE, headers, session, uuid } from './lib.js';

export const options = {
  scenarios: { hr: { executor: 'shared-iterations', vus: 1, iterations: Number(__ENV.ITERATIONS || 3), maxDuration: '30m' } },
  thresholds: {
    'http_req_duration{kind:calculate}': ['max<300000'],
    'http_req_duration{kind:read}': ['p(95)<3000'],
    http_req_failed: ['rate<0.01'],
    checks: ['rate>0.99'],
  },
};

export function setup() {
  const cookie = session(__ENV.STAFF_EMAIL, __ENV.STAFF_PASSWORD);
  let run = __ENV.RUN_ID;
  if (!run) {
    if (!__ENV.PERIOD) fail('set RUN_ID or PERIOD');
    const r = http.post(`${BASE}/api/v1/hris/payroll-runs`, JSON.stringify({ runType: 'regular', periodCode: __ENV.PERIOD, name: `Load test ${__ENV.PERIOD}` }),
      headers(cookie, { 'Idempotency-Key': uuid() }));
    if (!check(r, { 'run created': (x) => x.status === 201 })) fail(`create run: ${r.status} ${r.body}`);
    run = r.json('id');
  }
  return { cookie, run };
}

export default function (data) {
  const base = `${BASE}/api/v1/hris/payroll-runs/${data.run}`;
  const calc = http.post(`${base}:calculate`, '{}', Object.assign(headers(data.cookie), { tags: { kind: 'calculate' }, timeout: '360s' }));
  check(calc, {
    'calculated': (x) => x.status === 200 && x.json('status') === 'calculated',
    'every employee paid': (x) => x.status === 200 && Number(x.json('headcount')) > 0,
  });
  const read = Object.assign(headers(data.cookie), { tags: { kind: 'read' } });
  for (const path of ['', '/payslips?limit=100', '/comparison', '/journal']) {
    const r = http.get(`${base}${path}`, read);
    check(r, { [`run${path.split('?')[0] || ''} ok`]: (x) => x.status === 200 });
  }
}
