import { expect, test, type Page } from '@playwright/test';
import { TOTP } from 'otpauth';

const PASSWORD = process.env.E2E_PASSWORD ?? 'Demo#Club2026';
const BO = process.env.E2E_BACKOFFICE ?? 'http://localhost:5173';
const MEMBER = process.env.E2E_MEMBER ?? 'http://localhost:5174';
const OPS = process.env.E2E_OPS ?? 'http://localhost:5175';
const PA = process.env.E2E_PLATFORM_ADMIN ?? 'http://localhost:5176';
const WEB = process.env.E2E_WEB ?? 'http://localhost:3000';
const DEVICE = process.env.DEMO_DEVICE_TOKEN ?? '';

const email = (role: string) => `e2e.${role}@test.oneclub.id`;

/** Logs in through the login page (FR-SH-07), completing MFA when asked. */
async function login(page: Page, base: string, user: string) {
  await page.goto(`${base}/login`);
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
  await page.getByLabel(/^Email Address/).fill(user);
  await page.getByLabel(/^Password/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Log in', exact: true }).click();
  const setup = page.getByText(/Setup key|Kunci pengaturan/);
  const code = page.getByLabel(/Authentication code|Kode autentikasi/);
  await Promise.race([
    page.waitForURL((u) => !u.pathname.startsWith('/login'), { timeout: 15_000 }).catch(() => undefined),
    code.waitFor({ timeout: 15_000 }).catch(() => undefined),
  ]);
  if (await code.isVisible().catch(() => false)) {
    // First login enrols (QR + setup key); later logins only ask for the code.
    const enrolling = await page.getByRole('heading', { name: /Set up two-step|Aktifkan verifikasi/ }).isVisible().catch(() => false);
    if (enrolling) {
      await expect(setup).toBeVisible();
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

const secrets = new Map<string, string>();

/** Visible link labels of the main navigation (icons and phase badges removed). */
async function menuLabels(page: Page) {
  const nav = page.getByRole('navigation', { name: 'Main' }).first();
  await expect(nav.getByRole('link').first()).toBeVisible();
  return nav.getByRole('link').evaluateAll((els) =>
    els.map((e) => {
      const c = e.cloneNode(true) as HTMLElement;
      c.querySelectorAll('.material-symbols-rounded, .oc-nav-soon, .oc-brand-mark').forEach((x) => x.remove());
      return (c.textContent ?? '').trim();
    }),
  );
}

test('login page follows the reference (split layout, pill inputs, black button, forgot password)', async ({ page }) => {
  await page.goto(`${BO}/login`);
  await expect(page.locator('.oc-login-visual')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Log in' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Forgot Password?' })).toBeVisible();
  const btn = page.getByRole('button', { name: 'Log in', exact: true });
  await expect(btn).toHaveClass(/oc-btn-ink/);
  await expect(page.getByText('Create an Account')).toHaveCount(0); // no sign-up for staff
  await page.getByLabel(/^Email Address/).fill('nobody@test.oneclub.id');
  await page.getByLabel(/^Password/).fill('wrong');
  await btn.click();
  await expect(page.getByRole('alert')).toContainText(/incorrect|salah/);
});

test('General Manager sees modules and management menus but not user administration', async ({ page }) => {
  await login(page, BO, email('general_manager'));
  const labels = await menuLabels(page);
  for (const l of ['Dashboard', 'Approvals', 'Golf', 'Membership', 'Booking', 'Reports']) expect(labels).toContain(l);
  expect(labels).not.toContain('Users');
  await page.goto(`${BO}/settings/users`);
  await expect(page.getByText('403')).toBeVisible(); // FR-SH-04
  await page.goto(`${BO}/management`);
  await expect(page.getByRole('heading', { name: 'Executive Overview' })).toBeVisible();
  const pills = await menuLabels(page);
  expect(pills).toContain('Executive Overview');
});

test('Super Admin (MFA) reaches Settings; property switcher, notifications and language switch work', async ({ page }) => {
  await login(page, BO, email('super_admin'));
  const labels = await menuLabels(page);
  expect(labels).toContain('Settings');
  await page.goto(`${BO}/settings/venues`);
  await expect(page.getByRole('heading', { name: 'Venues' })).toBeVisible();
  await expect(page.locator('.oc-status').first()).toBeVisible(); // status pills (FR-SH-09)
  // property switcher (two properties in the demo)
  const switcher = page.getByLabel('Property').first();
  await expect(switcher).toBeVisible();
  // language switch changes helper text (labels stay English)
  await page.getByRole('button', { name: /Language/ }).click();
  await expect(page.getByText('Venue mengelompokkan course')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Venues' })).toBeVisible();
  await page.getByRole('button', { name: /Bahasa|Language/ }).click();
  // notification center
  await page.getByRole('button', { name: /Notifications/ }).click();
  await expect(page.locator('.oc-popover')).toBeVisible();
  // audit logs page renders entries
  await page.goto(`${BO}/settings/audit-logs`);
  await expect(page.locator('table.oc-table tbody tr').first()).toBeVisible();
});

test('a module disabled for the instance disappears from the menu', async ({ page, request }) => {
  await login(page, PA, email('platform_admin'));
  await page.goto(`${PA}/enabled-modules`);
  const row = page.locator('tr', { hasText: 'Stay & Venue' });
  await expect(row).toBeVisible();
  // enable Stay & Venue, check it appears for GM, then disable again
  const toggle = row.getByRole('checkbox');
  // The switch is controlled: it flips after the server confirms.
  if (!(await toggle.isChecked())) await toggle.click();
  await expect(row.locator('.oc-status')).toHaveText('Enabled');
  const gm = await page.context().browser()!.newContext();
  const gmPage = await gm.newPage();
  await login(gmPage, BO, email('general_manager'));
  expect(await menuLabels(gmPage)).toContain('Stay & Venue');
  await toggle.click();
  await expect(row.locator('.oc-status')).toHaveText('Disabled');
  await gmPage.reload();
  expect(await menuLabels(gmPage)).not.toContain('Stay & Venue');
  await gm.close();
  void request;
});

test('Platform Administration shows the §6.2 menu only to Platform Admin', async ({ page }) => {
  await login(page, PA, email('platform_admin'));
  const labels = await menuLabels(page);
  for (const l of ['Customer Instances', 'Instance Configuration', 'Enabled Modules', 'Feature Configuration', 'Branding', 'Custom Domain',
    'Users', 'Roles', 'Permissions', 'Integrations', 'Feature Flags', 'Locale', 'Currency', 'Timezone']) expect(labels).toContain(l);
  const other = await page.context().browser()!.newContext();
  const p2 = await other.newPage();
  await p2.goto(`${PA}/login`);
  await p2.getByLabel(/^Email Address/).fill(email('general_manager'));
  await p2.getByLabel(/^Password/).fill(PASSWORD);
  await p2.getByRole('button', { name: 'Log in', exact: true }).click();
  await expect(p2.getByText(/do not have access|tidak memiliki akses/)).toBeVisible();
  await other.close();
});

test('Member Portal: member logs in, top pill navigation, mobile bottom navigation', async ({ page }) => {
  await login(page, MEMBER, email('member'));
  await expect(page.getByText('Digital Member Card')).toBeVisible();
  expect(await menuLabels(page)).toEqual(expect.arrayContaining(['Home', 'Profile']));
  await page.setViewportSize({ width: 390, height: 800 });
  await expect(page.locator('.oc-bottom-nav')).toBeVisible(); // FR-SH-06 mobile
  // staff without the member portal are refused
  const ctx = await page.context().browser()!.newContext();
  const p = await ctx.newPage();
  await p.goto(`${MEMBER}/login`);
  await p.getByLabel(/^Email Address/).fill(email('starter_marshal'));
  await p.getByLabel(/^Password/).fill(PASSWORD);
  await p.getByRole('button', { name: 'Log in', exact: true }).click();
  await expect(p.getByText(/do not have access|tidak memiliki akses/)).toBeVisible();
  await ctx.close();
});

test('Ops: device + PIN login, works offline and syncs the queue when back online (FR-SH-05)', async ({ page, context }) => {
  test.skip(!DEVICE, 'DEMO_DEVICE_TOKEN not set');
  await page.goto(`${OPS}/login`);
  await page.getByLabel(/Device token|Token perangkat/).fill(DEVICE);
  await page.getByRole('button', { name: /Register this device|Daftarkan/ }).click();
  await page.getByLabel(/^Email Address/).fill(email('starter_marshal'));
  await page.getByLabel(/^PIN/).fill('246810');
  await page.getByRole('button', { name: 'Log in', exact: true }).click();
  await expect(page.getByText('Shift note')).toBeVisible();
  // service worker controls the page → app opens offline
  await page.waitForFunction(() => navigator.serviceWorker?.controller !== null, null, { timeout: 20_000 }).catch(() => undefined);
  await page.reload();
  await page.waitForFunction(() => navigator.serviceWorker?.controller !== null, null, { timeout: 20_000 });
  // 1. The app opens with the network cut (service worker cache).
  await context.setOffline(true);
  await page.reload();
  await expect(page.getByText('Shift note')).toBeVisible();
  // 2. Chrome's network emulation does not flip navigator.onLine for
  //    service-worker pages, so the app's own offline switch is used too.
  await page.getByRole('link', { name: /pending/ }).click();
  await page.getByRole('button', { name: 'Simulate offline' }).click();
  await expect(page.getByRole('link', { name: /^Offline/ })).toBeVisible();
  await page.locator('.oc-brand').click();
  // 3. The action is queued locally.
  const note = `Offline note ${Date.now()}`;
  await page.getByLabel(/^Note/).fill(note);
  await page.getByRole('button', { name: 'Save note' }).click();
  await page.getByRole('link', { name: /^Offline/ }).click();
  const row = page.locator('tr', { hasText: note.slice(0, 30) });
  await expect(row.locator('.oc-status')).toHaveText('Pending');
  // 4. Back online → the queue syncs to the server.
  await context.setOffline(false);
  await page.getByRole('button', { name: 'Go back online' }).click();
  await expect(row.locator('.oc-status')).toHaveText('Completed', { timeout: 20_000 });
});

test('public website renders branding in Indonesian and English', async ({ page }) => {
  await page.goto(WEB);
  await expect(page).toHaveURL(/\/id$/);
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Selamat datang');
  await page.getByRole('link', { name: 'EN' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Welcome');
  await page.getByRole('link', { name: 'Contact' }).click();
  await expect(page.getByRole('heading', { name: 'Contact' })).toBeVisible();
});
