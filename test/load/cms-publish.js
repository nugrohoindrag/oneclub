// CMS publication under website traffic (PRD P4 FR-REL-P4-01 "CMS publish",
// FR-CMS-10 "cache website di-invalidasi saat publish", NFR SEO & web):
// visitors keep reading the public website API (site revision, home page,
// navigation, news, sitemap) while Marketing creates pages, submits them for
// review and the approver publishes them (Website Content Publication
// workflow). Each publication must bump the website revision (the cache key
// the website revalidates on, cms.page_published) and the new page must be
// served by the public API right after the approval.
//
// Targets: visitor p95 < 500 ms (public API, cached by revision), publish
// (approve → page live and revision bumped) < 5 s, no 5xx, every published
// page live.
//
//   k6 run -e BASE_URL=... -e PROPERTY_ID=... -e MARKETING_EMAIL=... -e MARKETING_PASSWORD=... \
//          -e APPROVER_EMAIL=... -e APPROVER_PASSWORD=... [-e PAGES=30] [-e HOME_SLUG=beranda] test/load/cms-publish.js
import http from 'k6/http';
import { check, fail, sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';
import { BASE, PROPERTY, headers, session, uuid } from './lib.js';

const publishTime = new Trend('cms_publish_to_live_ms', true);
const live = new Counter('cms_pages_live');
const PAGES = Number(__ENV.PAGES || 30);

export const options = {
  setupTimeout: '2m',
  scenarios: {
    visitors: { executor: 'constant-arrival-rate', rate: 50, timeUnit: '1s', duration: '5m', preAllocatedVUs: 50, maxVUs: 150, exec: 'visit' },
    marketing: { executor: 'shared-iterations', iterations: PAGES, vus: 2, startTime: '15s', maxDuration: '6m', exec: 'publish' },
  },
  thresholds: {
    'http_req_duration{scenario:visitors}': ['p(95)<500'],
    cms_publish_to_live_ms: ['p(95)<5000'],
    cms_pages_live: [`count>=${PAGES}`],
    http_req_failed: ['rate<0.01'],
  },
};

// Public API: the property is a query parameter (default: the main property).
const prop = PROPERTY ? `propertyId=${PROPERTY}` : '';
const q = (path, extra = '') => `${BASE}/api/v1/public/cms/${path}?${[extra, prop].filter(Boolean).join('&')}`;

export function setup() {
  const marketing = session(__ENV.MARKETING_EMAIL, __ENV.MARKETING_PASSWORD);
  const approver = session(__ENV.APPROVER_EMAIL, __ENV.APPROVER_PASSWORD);
  const site = http.get(q('site'));
  if (!check(site, { 'public site ok': (x) => x.status === 200 })) fail(`public site: ${site.status}`);
  return { marketing, approver };
}

function revision() {
  const r = http.get(q('site'));
  return r.status === 200 ? Number(JSON.parse(r.body).revision) : -1;
}

// Website traffic: the reads the Next.js website makes per page view.
export function visit() {
  const lang = Math.random() < 0.7 ? 'id' : 'en';
  const home = __ENV.HOME_SLUG || 'beranda';
  const reads = [
    q('site'),
    q('navigation', `lang=${lang}`),
    q(`pages/${home}`, `lang=${lang}`),
    q('news', `lang=${lang}`),
    q('sitemap'),
  ];
  const r = http.get(reads[Math.floor(Math.random() * reads.length)]);
  check(r, { 'public read not 5xx': (x) => x.status < 500 });
}

// Marketing creates a page, submits it; the approver publishes it.
export function publish(data) {
  const slug = `k6-promo-${uuid().slice(0, 8)}`;
  const cms = `${BASE}/api/v1/cms/pages`;
  const body = {
    template: 'landing',
    translations: {
      id: { title: `Promo k6 ${slug}`, slug, summary: 'Uji beban publikasi' },
      en: { title: `k6 Promo ${slug}`, slug: `${slug}-en`, summary: 'Publication load test' },
    },
    blocks: [{ type: 'rich_text', content: { id: { html: '<p>Promo uji beban</p>' }, en: { html: '<p>Load test promo</p>' } } }],
    note: 'k6 cms-publish',
  };
  const c = http.post(cms, JSON.stringify(body), headers(data.marketing, { 'Idempotency-Key': uuid() }));
  if (!check(c, { 'page created': (x) => x.status === 201 })) return;
  const id = JSON.parse(c.body).id;
  const s = http.post(`${cms}/${id}:submit`, JSON.stringify({ note: 'k6' }), headers(data.marketing));
  if (!check(s, { 'page submitted': (x) => x.status === 200 })) return;
  const req = JSON.parse(s.body).approvalRequestId;
  const before = revision();
  const t0 = Date.now();
  const a = http.post(`${BASE}/api/v1/platform/approvals/${req}:approve`, JSON.stringify({ reason: 'k6 publish' }), headers(data.approver));
  if (!check(a, { 'publication approved': (x) => x.status === 200 })) return;
  // Live = the public API serves the page and the revision (cache key) moved.
  for (let i = 0; i < 50; i++) {
    const p = http.get(q(`pages/${slug}`, 'lang=id'));
    if (p.status === 200 && revision() > before) {
      publishTime.add(Date.now() - t0);
      live.add(1);
      return;
    }
    sleep(0.2);
  }
  check(null, { 'page live within 10 s': () => false });
}
