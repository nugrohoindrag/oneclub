// Shift change clock-in burst (PRD P5 FR-REL-P5-01, §12 NFR "Clock-in < 2 detik"): at 07:00 the morning shift clocks in and
// the night shift clocks out at the six face recognition / fingerprint points (§16 #7) within a few minutes. The devices push
// their events through the bridge agent in batches, a share of them twice (offline resend); every event must be accepted
// once (FR-ATT-07) and each push answered in under 2 s.
//
//   k6 run -e BASE_URL=... -e PROPERTY_ID=... -e STAFF_EMAIL=<HR Manager> -e STAFF_PASSWORD=... \
//          [-e AGENT_TOKEN=ocb_... -e DEVICE_SERIAL=<serial of a device served by the agent>] \
//          [-e EMPLOYEES=400] [-e DEVICES=6] [-e BATCH=20] [-e RESEND=0.1] test/load/clockin-shift-change.js
//
// Without AGENT_TOKEN the burst goes through the mock adapter of the trial (POST /hris/attendance-devices/{id}:simulate, the same
// ingestion path as the bridge): the setup creates DEVICES mock devices and records the biometric consent of EMPLOYEES employees.
import http from 'k6/http';
import { check } from 'k6';
import { Counter, Trend } from 'k6/metrics';
import { BASE, headers, session, uuid } from './lib.js';

const EMPLOYEES = Number(__ENV.EMPLOYEES || 400);
const DEVICES = Number(__ENV.DEVICES || 6);
const BATCH = Number(__ENV.BATCH || 20);
const RESEND = Number(__ENV.RESEND || 0.1);

const accepted = new Counter('clockin_accepted');
const duplicates = new Counter('clockin_duplicates');
const rejected = new Counter('clockin_rejected');
const pushTime = new Trend('clockin_push_ms', true);

export const options = {
  setupTimeout: '15m',
  scenarios: {
    // the shift change: every device pushes its queue in ~3 minutes
    shift_change: { executor: 'shared-iterations', vus: DEVICES * 2, iterations: Math.ceil(EMPLOYEES / BATCH), maxDuration: '5m' },
  },
  thresholds: {
    'http_req_duration{kind:clock}': ['p(95)<2000'],
    clockin_rejected: ['count==0'],
    checks: ['rate>0.99'],
  },
};

export function setup() {
  const cookie = session(__ENV.STAFF_EMAIL, __ENV.STAFF_PASSWORD);
  const h = headers(cookie);
  const emps = [];
  for (let cursor = ''; emps.length < EMPLOYEES;) {
    const r = http.get(`${BASE}/api/v1/hris/employees?limit=200&filter[status]=active${cursor ? `&cursor=${cursor}` : ''}`, h);
    const page = r.json();
    for (const e of page.items) emps.push(e.id);
    if (!page.nextCursor) break;
    cursor = page.nextCursor;
  }
  if (__ENV.AGENT_TOKEN) {
    const profiles = http.get(`${BASE}/api/v1/hris/attendance-profiles`, h).json('items').filter((p) => p.biometricConsent);
    return { cookie, users: profiles.slice(0, EMPLOYEES).map((p) => p.deviceUserNo), bridge: true };
  }
  const run = `${Date.now() % 1e6}`;
  const devices = [];
  for (let i = 0; i < DEVICES; i++) {
    const d = http.post(`${BASE}/api/v1/hris/attendance-devices`, JSON.stringify({ code: `K6D${run}${i}`, name: `Load device ${i}`, deviceKind: 'biometric' }),
      headers(cookie, { 'Idempotency-Key': uuid() }));
    check(d, { 'device created': (x) => x.status === 201 });
    devices.push(d.json('id'));
  }
  const today = new Date().toISOString().slice(0, 10);
  for (const e of emps.slice(0, EMPLOYEES)) {
    http.post(`${BASE}/api/v1/hris/attendance-profiles/${e}:consent`, JSON.stringify({ consent: true, signedOn: today }), h);
  }
  return { cookie, employees: emps.slice(0, EMPLOYEES), devices, run, bridge: false };
}

export default function (data) {
  const i = __ITER;
  const now = new Date().toISOString().replace(/\.\d+Z$/, 'Z');
  if (data.bridge) {
    const users = data.users.slice(i * BATCH, (i + 1) * BATCH);
    const events = users.map((u) => ({ eventId: `K6-${u}-${now}`, deviceUserNo: u, occurredAt: now, method: 'face_recognition' }));
    for (const e of events.slice(0, Math.round(events.length * RESEND))) events.push(e); // offline resend
    const r = http.post(`${BASE}/api/v1/bridge/hris/attendance-events`, JSON.stringify({ deviceSerial: __ENV.DEVICE_SERIAL, events }),
      { headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${__ENV.AGENT_TOKEN}` }, tags: { kind: 'clock' } });
    pushTime.add(r.timings.duration);
    check(r, { 'push answered': (x) => x.status === 200 });
    for (const res of r.json('results') || []) {
      if (res.status === 'accepted') accepted.add(1);
      else if (res.status === 'duplicate') duplicates.add(1);
      else rejected.add(1);
    }
    return;
  }
  const emps = data.employees.slice(i * BATCH, (i + 1) * BATCH);
  const device = data.devices[i % data.devices.length];
  for (const e of emps) {
    const body = JSON.stringify({ employeeId: e, method: 'face_recognition', eventId: `K6-${data.run}-${e}` });
    for (let n = 0; n < (Math.random() < RESEND ? 2 : 1); n++) {
      const r = http.post(`${BASE}/api/v1/hris/attendance-devices/${device}:simulate`, body,
        Object.assign(headers(data.cookie, { 'Idempotency-Key': uuid() }), { tags: { kind: 'clock' } }));
      pushTime.add(r.timings.duration);
      if (r.status !== 200) {
        rejected.add(1);
        continue;
      }
      if (r.json('duplicate')) duplicates.add(1);
      else accepted.add(1);
    }
  }
}
