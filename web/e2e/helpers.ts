import { expect, type Page } from '@playwright/test';
import { TOTP } from 'otpauth';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';

/** Base URLs and accounts of the browser tests (e2e/setup.sql). */
export const PASSWORD = process.env.E2E_PASSWORD ?? 'Demo#Club2026';
export const STAFF = process.env.E2E_STAFF ?? 'http://localhost:5173';
export const MEMBER = process.env.E2E_MEMBER ?? 'http://localhost:5174';
export const WEB = process.env.E2E_WEB ?? 'http://localhost:3000';
export const DEVICE = process.env.DEMO_DEVICE_TOKEN ?? '';
export const email = (role: string) => `e2e.${role}@test.oneclub.id`;

/**
 * TOTP secrets enrolled in this run (global-setup resets MFA). Kept in a file
 * so they survive the worker restart that follows a failed test.
 */
export const SECRETS_FILE = resolve('test-results/totp-secrets.json'); // Playwright runs from web/

const secrets = {
  all(): Record<string, string> {
    try {
      return JSON.parse(readFileSync(SECRETS_FILE, 'utf8'));
    } catch {
      return {};
    }
  },
  get(user: string): string | undefined {
    return this.all()[user];
  },
  set(user: string, secret: string) {
    mkdirSync(dirname(SECRETS_FILE), { recursive: true });
    writeFileSync(SECRETS_FILE, JSON.stringify({ ...this.all(), [user]: secret }));
  },
};

/**
 * Logs in through the login page (FR-SH-07), completing MFA when asked: the
 * first login of a run enrols (QR + setup key), later logins only ask for the
 * code. `next` is the page to open afterwards; without it the Staff App opens
 * the user's first area.
 */
export async function login(page: Page, base: string, user: string, next?: string) {
  await page.goto(`${base}/login${next ? `?next=${encodeURIComponent(next)}` : ''}`);
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
  await page.getByLabel(/^Email Address/).fill(user);
  await page.getByLabel(/^Password/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Log in', exact: true }).click();
  const code = page.getByLabel(/Authentication code|Kode autentikasi/);
  await Promise.race([
    page.waitForURL((u) => !u.pathname.startsWith('/login'), { timeout: 15_000 }).catch(() => undefined),
    code.waitFor({ timeout: 15_000 }).catch(() => undefined),
  ]);
  if (await code.isVisible().catch(() => false)) {
    if (await page.getByRole('heading', { name: /Set up two-step|Aktifkan verifikasi/ }).isVisible().catch(() => false)) {
      await expect(page.getByText(/Setup key|Kunci pengaturan/)).toBeVisible();
      await expect(page.locator('code.oc-code').first()).toHaveText(/\S+/);
      secrets.set(user, (await page.locator('code.oc-code').first().textContent())!.trim());
    }
    const secret = secrets.get(user);
    if (!secret) throw new Error(`no TOTP secret known for ${user}`);
    await code.fill(new TOTP({ secret, digits: 6, period: 30 }).generate());
    await page.getByRole('button', { name: 'Verify' }).click();
  }
  await page.waitForURL((u) => !u.pathname.startsWith('/login'));
}

/**
 * API calls with the session of a logged-in page, sent like the app sends
 * them: property MAIN (where the e2e accounts work) and Origin for CSRF.
 */
export async function apiOf(page: Page) {
  const me = await (await page.request.get(`${STAFF}/api/v1/auth/me`)).json();
  const property = me.properties.find((p: { code: string }) => p.code === 'MAIN').id;
  const headers = { 'X-Property-Id': property, Origin: STAFF };
  /** Any status; the caller decides. */
  const send = (method: string, path: string, data?: unknown) => page.request.fetch(`${STAFF}${path}`, { method, headers, data });
  const call = async (method: string, path: string, data?: unknown) => {
    const r = await send(method, path, data);
    expect(r.ok(), `${method} ${path}: ${r.status()} ${await r.text()}`).toBeTruthy();
    return r.status() === 204 ? null : r.json();
  };
  return {
    send,
    get: (path: string) => call('GET', path),
    post: (path: string, data?: unknown) => call('POST', path, data ?? {}),
    put: (path: string, data: unknown) => call('PUT', path, data),
  };
}

/** A weekday at least `min` days ahead (local time). */
export function playDay(min: number) {
  const d = new Date();
  d.setDate(d.getDate() + min);
  while (d.getDay() === 0 || d.getDay() === 6) d.setDate(d.getDate() + 1);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}
