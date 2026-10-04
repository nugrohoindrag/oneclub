import { expect, test, type Page } from '@playwright/test';
import { CADDY, CASHIER, DASHBOARD, DEVICE, KITCHEN, LOCAL, MEMBER, PASSWORD, STAFF, WEB, email, login } from './helpers';

/** Visible link labels of the main navigation (icons and phase badges removed). */
async function menuLabels(page: Page) {
  const nav = page.getByRole('navigation', { name: 'Main' }).first();
  await expect(nav.locator('a:not(.oc-brand)').first()).toBeVisible(); // the menu, not only the logo
  return nav.getByRole('link').evaluateAll((els) =>
    els.map((e) => {
      const c = e.cloneNode(true) as HTMLElement;
      c.querySelectorAll('.material-symbols-rounded, .oc-nav-soon, .oc-brand-mark').forEach((x) => x.remove());
      return (c.textContent ?? '').trim();
    }),
  );
}

/** Opens the user menu and picks an area of the area switcher. */
async function switchArea(page: Page, label: string) {
  await page.locator('button.oc-user').click();
  await page.getByRole('menuitem', { name: label }).click();
}

test('login page follows the reference (split layout, pill inputs, black button, forgot password)', async ({ page }) => {
  await page.goto(`${DASHBOARD}/login`);
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

test('General Manager lands on Management and switches to the Back Office; 403 links to their areas', async ({ page }) => {
  await login(page, DASHBOARD, email('general_manager'));
  // Landing order (Technical Doc §6.1): Management comes before Back Office.
  await expect(page).toHaveURL(/\/management$/);
  await expect(page.getByRole('heading', { name: 'Executive Overview' })).toBeVisible();
  expect(await menuLabels(page)).toContain('Executive Overview');
  await switchArea(page, 'Back Office');
  await expect(page).toHaveURL(`${DASHBOARD}/`);
  const labels = await menuLabels(page);
  for (const l of ['Dashboard', 'Approvals', 'Golf', 'Membership', 'Booking', 'Reports']) expect(labels).toContain(l);
  expect(labels).not.toContain('Users');
  // FR-SH-04: 403 with links to the areas the user may open
  await page.goto(`${DASHBOARD}/settings/users`);
  await expect(page.getByText('403')).toBeVisible();
  const areas = page.getByRole('navigation', { name: 'Areas you can open' });
  await expect(areas.getByRole('link', { name: 'Management Dashboard' })).toBeVisible();
  await expect(areas.getByRole('link', { name: 'Back Office' })).toBeVisible();
  await expect(areas.getByRole('link', { name: 'Platform Administration' })).toHaveCount(0);
  await page.goto(`${DASHBOARD}/platform/users`);
  await expect(page.getByText('403')).toBeVisible();
  await areas.getByRole('link', { name: 'Management Dashboard' }).click();
  await expect(page.getByRole('heading', { name: 'Executive Overview' })).toBeVisible();
});

test('caddy domain: the caddy logs in with the PIN, lands on the tablet and cannot open the Back Office', async ({ page }) => {
  if (DEVICE) {
    // The device token is kept per domain: the tablet registers on the caddy domain.
    await page.goto(`${CADDY}/login`);
    await page.getByRole('link', { name: 'Register this device' }).click();
    await page.getByLabel(/Device token|Token perangkat/).fill(DEVICE);
    await page.getByRole('button', { name: /Register this device|Daftarkan/ }).click();
    await page.getByLabel(/^Email Address/).fill(email('caddy'));
    await page.getByLabel(/^PIN/).fill('246810');
    await page.getByRole('button', { name: 'Log in', exact: true }).click();
  } else {
    await login(page, CADDY, email('caddy'));
  }
  await expect(page).toHaveURL(/\/tablet$/);
  await expect(page.locator('.oc-bottom-nav')).toBeVisible();
  await page.goto(`${CADDY}/`);
  await expect(page).toHaveURL(/\/tablet$/); // the app root opens the caddy's own area
  await page.locator('button.oc-user').click();
  await expect(page.getByText('Switch area')).toHaveCount(0); // a device domain has no area switcher
  await page.keyboard.press('Escape');
  // The Back Office opens on the dashboard domain only.
  await page.goto(`${CADDY}/golf/tee-sheet`);
  await expect(page.getByText('403')).toBeVisible();
  await expect(page.getByRole('link', { name: /^Open on / })).toHaveAttribute('href', `${DASHBOARD}/golf/tee-sheet`);
  const areas = page.getByRole('navigation', { name: 'Areas you can open' });
  await expect(areas.getByRole('link', { name: 'Caddy Tablet' })).toBeVisible();
  await expect(areas.getByRole('link', { name: 'Back Office' })).toHaveCount(0);
});

test('dashboard domain: administrators land on Management, never on a device area; device areas are refused', async ({ browser }) => {
  for (const role of ['super_admin', 'platform_admin', 'property_admin']) {
    const ctx = await browser.newContext();
    const page = await ctx.newPage();
    await login(page, DASHBOARD, email(role));
    await expect(page).toHaveURL(`${DASHBOARD}/management`);
    for (const path of ['/ops', '/tablet', '/kitchen']) {
      await page.goto(`${DASHBOARD}${path}`);
      await expect(page.getByText('403')).toBeVisible();
    }
    await expect(page.getByRole('link', { name: /^Open on / })).toHaveAttribute('href', `${KITCHEN}/kitchen`);
    await ctx.close();
  }
});

test('dashboard domain: password login only; the Screen role opens the Clubhouse Screen full screen', async ({ page }) => {
  await page.goto(`${DASHBOARD}/login`);
  await expect(page.getByRole('heading', { name: 'Log in' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Register this device' })).toHaveCount(0);
  await login(page, DASHBOARD, email('screen'));
  await expect(page).toHaveURL(`${DASHBOARD}/screen`);
  await expect(page.locator('.oc-screen h1')).toBeVisible();
  await expect(page.getByRole('navigation', { name: 'Main' })).toHaveCount(0); // no menu
  await expect(page.locator('.oc-header, .oc-topbar, .oc-bottom-nav')).toHaveCount(0);
  await page.goto(`${DASHBOARD}/golf/tee-sheet`);
  await expect(page.getByText('403')).toBeVisible();
  await expect(page.getByRole('navigation', { name: 'Areas you can open' }).getByRole('link')).toHaveText([/Clubhouse Screen/]);
});

test('cashier domain: the cashier opens Operational; the Back Office is refused', async ({ page }) => {
  await login(page, CASHIER, email('cashier'));
  await expect(page).toHaveURL(`${CASHIER}/ops`);
  await page.goto(`${CASHIER}/golf/tee-sheet`);
  await expect(page.getByText('403')).toBeVisible();
  await expect(page.getByRole('link', { name: /^Open on / })).toHaveAttribute('href', `${DASHBOARD}/golf/tee-sheet`);
  await page.goto(`${CASHIER}/kitchen`);
  await expect(page.getByText('403')).toBeVisible();
});

test('kitchen domain: kitchen staff get the Kitchen Display full width, other areas are refused', async ({ page }) => {
  await login(page, KITCHEN, email('kitchen_staff'));
  await expect(page).toHaveURL(`${KITCHEN}/kitchen`);
  await expect(page.getByRole('heading', { name: 'Kitchen' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Preparing' })).toBeVisible();
  await expect(page.locator('.oc-bottom-nav')).toHaveCount(0); // no Operational menu
  await page.goto(`${KITCHEN}/ops`);
  await expect(page.getByText('403')).toBeVisible();
  await expect(page.getByRole('link', { name: /^Open on / })).toHaveAttribute('href', `${CASHIER}/ops`);
});

test('development without a surface: every area opens by path', async ({ page }) => {
  test.skip(!LOCAL, 'Staging serves only the four domains');
  await login(page, STAFF, email('super_admin'));
  await expect(page).toHaveURL(`${STAFF}/management`);
  for (const [path, heading] of [['/ops', /Super Admin/], ['/kitchen', 'Kitchen'], ['/screen', /./]] as const) {
    await page.goto(`${STAFF}${path}`);
    await expect(page.getByText('403')).toHaveCount(0);
    await expect(page.getByRole('heading', { name: heading }).first()).toBeVisible();
  }
});

test('Super Admin (MFA) reaches Settings; property switcher, notifications and language switch work', async ({ page }) => {
  await login(page, DASHBOARD, email('super_admin'), '/');
  const labels = await menuLabels(page);
  expect(labels).toContain('Settings');
  await page.goto(`${DASHBOARD}/settings/venues`);
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
  await page.goto(`${DASHBOARD}/settings/audit-logs`);
  await expect(page.locator('table.oc-table tbody tr').first()).toBeVisible();
});

test('a module disabled for the instance disappears from the menu', async ({ page }) => {
  await login(page, DASHBOARD, email('platform_admin'), '/platform/enabled-modules');
  const row = page.locator('tr', { hasText: 'Stay & Venue' });
  await expect(row).toBeVisible();
  // enable Stay & Venue, check it appears for GM, then disable again
  const toggle = row.getByRole('checkbox');
  // The switch is controlled: it flips after the server confirms.
  if (!(await toggle.isChecked())) await toggle.click();
  await expect(row.locator('.oc-status')).toHaveText('Enabled');
  const gm = await page.context().browser()!.newContext();
  const gmPage = await gm.newPage();
  await login(gmPage, DASHBOARD, email('general_manager'), '/');
  expect(await menuLabels(gmPage)).toContain('Stay & Venue');
  await toggle.click();
  await expect(row.locator('.oc-status')).toHaveText('Disabled');
  await gmPage.reload();
  expect(await menuLabels(gmPage)).not.toContain('Stay & Venue');
  await gm.close();
});

test('Platform Administration shows the §6.2 menu only to Platform Admin', async ({ page }) => {
  await login(page, DASHBOARD, email('platform_admin'), '/platform');
  const labels = await menuLabels(page);
  for (const l of ['Customer Instances', 'Instance Configuration', 'Enabled Modules', 'Feature Configuration', 'Branding', 'Custom Domain',
    'Users', 'Roles', 'Permissions', 'Integrations', 'Feature Flags', 'Locale', 'Currency', 'Timezone']) expect(labels).toContain(l);
  // Super Admin runs the club but not the platform (FR-SH-04).
  const other = await page.context().browser()!.newContext();
  const p2 = await other.newPage();
  await login(p2, DASHBOARD, email('super_admin'));
  await p2.goto(`${DASHBOARD}/platform`);
  await expect(p2.getByText('403')).toBeVisible();
  await other.close();
});

test('Member Portal: member logs in, top pill navigation, mobile bottom navigation', async ({ page }) => {
  await login(page, MEMBER, email('member'));
  await expect(page.getByText('Digital Member Card').first()).toBeVisible();
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

test('cashier domain: registered device + PIN, works offline and syncs the queue when back online (FR-SH-05)', async ({ page, context }) => {
  test.skip(!DEVICE, 'DEMO_DEVICE_TOKEN not set');
  await page.goto(`${CASHIER}/login`);
  await page.getByRole('link', { name: 'Register this device' }).click();
  await page.getByLabel(/Device token|Token perangkat/).fill(DEVICE);
  await page.getByRole('button', { name: /Register this device|Daftarkan/ }).click();
  // a registered device logs in with the staff PIN and opens the user's area
  await page.getByLabel(/^Email Address/).fill(email('starter_marshal'));
  await page.getByLabel(/^PIN/).fill('246810');
  await page.getByRole('button', { name: 'Log in', exact: true }).click();
  await expect(page).toHaveURL(/\/ops$/);
  await expect(page.getByText('Shift note')).toBeVisible();
  // service worker controls the page → app opens offline
  await page.waitForFunction(() => navigator.serviceWorker?.controller !== null, null, { timeout: 20_000 }).catch(() => undefined);
  await page.reload();
  await page.waitForFunction(() => navigator.serviceWorker?.controller !== null, null, { timeout: 20_000 });
  // 1. The app opens with the network cut (service worker cache).
  await context.setOffline(true);
  await page.reload();
  await expect(page.getByText('Shift note')).toBeVisible();
  // 2. Chrome on Windows does not flip navigator.onLine for service-worker
  //    pages under network emulation (Linux does), so the app's own offline
  //    switch is used when the app still believes it is online.
  await page.getByRole('link', { name: /pending/ }).click();
  await expect(page.getByRole('button', { name: /Simulate offline|Go back online/ })).toBeVisible();
  const simulate = page.getByRole('button', { name: 'Simulate offline' });
  if (await simulate.isVisible()) await simulate.click();
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
  // Where the browser reported offline itself, the 'online' event already
  // resumes sync and the button may disappear.
  await page.getByRole('button', { name: 'Go back online' }).click({ timeout: 3_000 }).catch(() => undefined);
  await expect(row.locator('.oc-status')).toHaveText('Completed', { timeout: 20_000 });
});

test('public website renders branding in Indonesian and English', async ({ page }) => {
  await page.goto(WEB);
  await expect(page).toHaveURL(/\/id$/);
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Selamat datang');
  await page.getByRole('link', { name: 'EN', exact: true }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Welcome');
  await page.getByRole('link', { name: 'Contact' }).click();
  await expect(page.getByRole('heading', { name: 'Contact' })).toBeVisible();
});
