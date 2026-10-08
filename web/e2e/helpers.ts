import { expect, type Locator, type Page } from '@playwright/test';
import { TOTP } from 'otpauth';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';

/** Base URLs and accounts of the browser tests (e2e/setup.sql). */
export const PASSWORD = process.env.E2E_PASSWORD ?? 'Demo#Club2026';
/** Staff App without a surface (development): every area opens by path. Local runs only. */
export const STAFF = process.env.E2E_STAFF ?? 'http://localhost:5173';
/**
 * The Staff App domains (Technical Doc §6.1). Locally <surface>.localhost on
 * the Staff App port, where vite.config.ts serves each /surface.json like
 * Caddy does; Staging sets E2E_DASHBOARD, E2E_CASHIER, E2E_CADDY, E2E_KITCHEN.
 */
const staffDomain = (surface: string) => process.env[`E2E_${surface.toUpperCase()}`] ?? `http://${surface}.localhost:${new URL(STAFF).port}`;
export const DASHBOARD = staffDomain('dashboard');
export const CASHIER = staffDomain('cashier');
export const CADDY = staffDomain('caddy');
export const KITCHEN = staffDomain('kitchen');
/** Staging runs against the deployed domains; the surface-less origin exists only locally. */
export const LOCAL = !process.env.E2E_DASHBOARD;
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

/** Response of an API call made by apiOf. */
export interface ApiResponse {
  status(): number;
  ok(): boolean;
  text(): Promise<string>;
  json(): Promise<any>;
}

/**
 * API calls with the session of a logged-in page, sent the way the app sends
 * them: by the page itself (its domain, cookie and Origin) with property MAIN,
 * where the e2e accounts work. The browser also resolves the
 * <surface>.localhost domains, which Node does not reliably do.
 */
export async function apiOf(page: Page) {
  /** Any status; the caller decides. */
  const raw = async (method: string, path: string, data: unknown, property: string): Promise<ApiResponse> => {
    const r = await page.evaluate(async ([method, path, body, property]) => {
      const headers: Record<string, string> = body === undefined ? {} : { 'Content-Type': 'application/json' };
      if (property) headers['X-Property-Id'] = property;
      const res = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), cache: 'no-store' });
      return { status: res.status, body: await res.text() };
    }, [method, path, data, property] as const);
    return { status: () => r.status, ok: () => r.status >= 200 && r.status < 300, text: async () => r.body, json: async () => JSON.parse(r.body) };
  };
  const me = await (await raw('GET', '/api/v1/auth/me', undefined, '')).json();
  const property: string = me.properties.find((p: { code: string }) => p.code === 'MAIN').id;
  const send = (method: string, path: string, data?: unknown) => raw(method, path, data, property);
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

/** Opens the cashier shift of the outlet in POS Settings (opening cash 500,000); payments need one. */
export async function openPosShift(page: Page) {
  await page.getByRole('link', { name: 'Settings' }).click();
  await page.getByRole('button', { name: 'Open Shift' }).click();
  await expect(page.getByRole('heading', { name: /^Cashier shift \S/ })).toBeVisible();
}

/** Runs an action of a table row: the actions sit in the row's ⋮ menu (dashboard kit). */
export async function rowAction(row: Locator, name: string) {
  await row.getByRole('button', { name: 'Row actions' }).click();
  await row.page().getByRole('menu').getByRole('button', { name, exact: true }).click();
}
