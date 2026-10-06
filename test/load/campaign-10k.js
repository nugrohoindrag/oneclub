// Campaign to 10,000 recipients (PRD P3 §12 NFR "Campaign", FR-CMP-07): a
// campaign to a 10.000-member segment is queued, approved (above the
// Campaign Policies threshold) and sent in throttled batches — never more
// than the policy batch size per dispatch minute (WhatsApp BSP limits) —
// until every opted-in recipient has a recorded delivery status.
//
//   k6 run -e BASE_URL=... -e PROPERTY_ID=... -e STAFF_EMAIL=... -e STAFF_PASSWORD=... \
//          [-e SEGMENT_ID=<static segment with 10.000 members>] [-e RECIPIENTS=10000] [-e BATCH=500] [-e CHANNEL=email] \
//          test/load/campaign-10k.js
//
// Without SEGMENT_ID the setup creates RECIPIENTS customers with marketing
// consent and a static segment of them (allow ~10 minutes).
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';
import { BASE, headers, session, uuid } from './lib.js';

const RECIPIENTS = Number(__ENV.RECIPIENTS || 10000);
const BATCH = Number(__ENV.BATCH || 500);
const CHANNEL = __ENV.CHANNEL || 'email';

const overLimit = new Counter('campaign_minutes_over_bsp_limit');
const sentPerMinute = new Trend('campaign_sent_per_minute');
const completed = new Counter('campaign_completed');

export const options = {
  setupTimeout: '20m',
  scenarios: { campaign: { executor: 'per-vu-iterations', vus: 1, iterations: 1, maxDuration: '45m' } },
  thresholds: {
    campaign_minutes_over_bsp_limit: ['count==0'],
    campaign_completed: ['count==1'],
    http_req_duration: ['p(95)<1500'],
    checks: ['rate==1'],
  },
};

function post(cookie, path, body) {
  return http.post(`${BASE}${path}`, JSON.stringify(body), headers(cookie, { 'Idempotency-Key': uuid() }));
}

export function setup() {
  const cookie = session(__ENV.STAFF_EMAIL, __ENV.STAFF_PASSWORD);
  let segment = __ENV.SEGMENT_ID;
  const run = `${Date.now() % 1e7}`;
  if (!segment) {
    const ids = [];
    for (let i = 0; i < RECIPIENTS; i++) {
      const r = post(cookie, '/api/v1/crm/customers', { code: `K6C${run}-${i}`, name: `Load Guest ${i}`, email: `k6-${run}-${i}@load.test`,
        marketingOptIn: true, duplicateAcknowledged: true });
      check(r, { 'customer created': (x) => x.status === 201 });
      ids.push(r.json('id'));
    }
    const s = post(cookie, '/api/v1/crm/segments', { code: `K6SEG${run}`, name: `Load segment ${run}`, segmentType: 'static' });
    segment = s.json('id');
    for (let i = 0; i < ids.length; i += 1000) {
      const r = post(cookie, `/api/v1/crm/segments/${segment}/members:add`, { customerIds: ids.slice(i, i + 1000) });
      check(r, { 'members added': (x) => x.status === 200 });
    }
  }
  return { cookie, segment, run };
}

export default function (data) {
  const c = post(data.cookie, '/api/v1/crm/campaigns', { code: `K6CMP${data.run}`, name: `Load campaign ${data.run}`, segmentId: data.segment,
    channel: CHANNEL, templateEvent: 'crm.campaign_message', subject: 'Weekend offer', body: 'Hello {{.name}}, see you this weekend!' });
  check(c, { 'campaign created': (x) => x.status === 201 });
  const id = c.json('id');
  const pv = http.get(`${BASE}/api/v1/crm/campaigns/${id}/preview`, headers(data.cookie));
  const eligible = pv.json('eligible');
  const s = post(data.cookie, `/api/v1/crm/campaigns/${id}:schedule`, {});
  check(s, { 'campaign scheduled (approved above the threshold)': (x) => x.status === 200 && x.json('status') === 'scheduled' });

  let last = 0;
  for (let minute = 0; minute < 44; minute++) {
    sleep(60);
    const st = http.get(`${BASE}/api/v1/crm/campaigns/${id}/stats`, headers(data.cookie));
    if (st.status !== 200) continue;
    const sent = st.json('sent');
    sentPerMinute.add(sent - last);
    if (sent - last > BATCH) overLimit.add(1);
    last = sent;
    if (st.json('status') === 'sent') {
      completed.add(1);
      check(st, {
        'every eligible recipient sent': (x) => x.json('sent') === eligible,
        'no recipient left queued': (x) => x.json('queued') === 0,
      });
      break;
    }
  }
}
