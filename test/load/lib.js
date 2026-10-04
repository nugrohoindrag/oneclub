// Shared helpers of the P2 k6 scripts (Technical Doc §13, PRD P2 FR-REL-P2-01).
import http from 'k6/http';
import { check, fail } from 'k6';

export const BASE = __ENV.BASE_URL || 'http://localhost:8080';
export const PROPERTY = __ENV.PROPERTY_ID;

/** Logs in once (setup) and returns the session cookie header. */
export function session(email, password) {
  const r = http.post(`${BASE}/api/v1/auth/login`, JSON.stringify({ email, password }), { headers: { 'Content-Type': 'application/json' } });
  if (!check(r, { 'login ok': (x) => x.status === 200 })) fail(`login failed: ${r.status} ${r.body}`);
  const c = r.cookies.oneclub_session;
  if (!c || !c[0]) fail('no session cookie');
  return `oneclub_session=${c[0].value}`;
}

export function headers(cookie, extra = {}) {
  return { headers: { 'Content-Type': 'application/json', Cookie: cookie, 'X-Property-Id': PROPERTY, ...extra } };
}

export function uuid() {
  // RFC 4122 v4 is enough for idempotency keys in load tests.
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    return (c === 'x' ? r : (r & 0x3) | 0x8).toString(16);
  });
}
