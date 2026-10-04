import { expect, test, type Page } from '@playwright/test';
import { TOTP } from 'otpauth';

/**
 * PRD P2 browser acceptance (FR-REL-P2-02): module hubs and KPI dashboards,
 * ops interfaces, the caddy tablet with its offline queue, the member app
 * and website booking pages.
 */

const PASSWORD = process.env.E2E_PASSWORD ?? 'Demo#Club2026';
const BO = process.env.E2E_BACKOFFICE ?? 'http://localhost:5173';
const MEMBER = process.env.E2E_MEMBER ?? 'http://localhost:5174';
const OPS = process.env.E2E_OPS ?? 'http://localhost:5175';
const CADDY = process.env.E2E_CADDY ?? 'http://localhost:5177';
const WEB = process.env.E2E_WEB ?? 'http://localhost:3000';
const email = (role: string) => `e2e.${role}@test.oneclub.id`;
const secrets = new Map<string, string>();

async function login(page: Page, base: string, user: string, path = '/login') {
  await page.goto(`${base}${path}`);
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
      secrets.set(user, (await page.locator('code.oc-code').first().textContent())!.trim());
    }
    const secret = secrets.get(user);
    if (!secret) throw new Error(`no TOTP secret for ${user}`);
    await code.fill(new TOTP({ secret, digits: 6, period: 30 }).generate());
    await page.getByRole('button', { name: 'Verify' }).click();
  }
  await page.waitForURL((u) => !u.pathname.startsWith('/login'));
}

test('Back Office: P2 module hubs list master data and operations', async ({ page }) => {
  await login(page, BO, email('golf_manager'));
  await page.goto(`${BO}/golf`);
  await expect(page.getByRole('heading', { name: 'Golf', level: 1 })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Caddy Settlement' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Caddies' })).toBeVisible(); // generated from the resource definitions
  await page.getByRole('link', { name: /Golf Carts/ }).first().click();
  await expect(page.getByRole('button', { name: /Add Golf Cart/ })).toBeVisible();
});

test('Management: KPI dashboards show P2 figures', async ({ page }) => {
  await login(page, BO, email('general_manager'));
  for (const [path, title] of [['golf-performance', 'Golf Performance'], ['commercial-performance', 'Commercial Performance']]) {
    await page.goto(`${BO}/management/${path}`);
    await expect(page.getByRole('heading', { name: title })).toBeVisible();
    await expect(page.locator('.oc-metric').first()).toBeVisible();
  }
});

test('Ops: Starter sees Pace of Play; Sport Reception validates access', async ({ page }) => {
  await login(page, OPS, email('starter_marshal'), '/login/password');
  await page.goto(`${OPS}/starter`);
  await expect(page.getByRole('heading', { name: 'Pace of Play' })).toBeVisible();
  const ctx = await page.context().browser()!.newContext();
  const p = await ctx.newPage();
  await login(p, OPS, email('sport_club_receptionist'), '/login/password');
  await p.goto(`${OPS}/sport-reception`);
  await expect(p.getByRole('heading', { name: 'Facility Access' })).toBeVisible();
  await ctx.close();
});

test('Caddy Tablet: assignments, earnings and the offline queue', async ({ page }) => {
  await login(page, CADDY, email('caddy'), '/login/password');
  await expect(page.locator('.oc-bottom-nav')).toBeVisible();
  await page.goto(`${CADDY}/earnings`);
  await expect(page.getByRole('heading', { name: 'Earnings' })).toBeVisible();
  await page.goto(`${CADDY}/sync`);
  await page.getByRole('button', { name: 'Simulate offline' }).click();
  await expect(page.getByText(/No signal/)).toBeVisible();
  await page.getByRole('button', { name: 'Go back online' }).click();
});

test('Member App: card, membership, vouchers and transactions pages', async ({ page }) => {
  await login(page, MEMBER, email('member'));
  await expect(page.getByText('Digital Member Card')).toBeVisible();
  for (const [path, title] of [['membership', 'Membership'], ['vouchers', 'Voucher & Prepaid'], ['transactions', 'Transactions'], ['golf', 'Golf']]) {
    await page.goto(`${MEMBER}/${path}`);
    await expect(page.getByRole('heading', { name: title, level: 1 })).toBeVisible();
  }
});

test('Website: Sport Club page, structured rates and the contact form', async ({ page }) => {
  await page.goto(`${WEB}/en/sport-club`);
  await expect(page.getByRole('heading', { name: 'Sport Club', level: 1 })).toBeVisible();
  await page.goto(`${WEB}/en/contact`);
  await page.getByLabel('Name').fill('Playwright Visitor');
  await page.getByLabel('E-mail').fill('visitor@playwright.test');
  await page.getByLabel('Message').fill('Do you have a family package?');
  await page.getByRole('button', { name: 'Send' }).click();
  await expect(page.getByText(/we will get back to you/)).toBeVisible();
});
