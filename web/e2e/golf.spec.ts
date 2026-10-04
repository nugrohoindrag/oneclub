import { expect, test, type Page } from '@playwright/test';

/**
 * PRD P1 FR-REL-05: the critical golf flow in the browser —
 * booking → payment → caddy & golf cart → check-in → tee-off.
 * Uses the demo seed (Modern Golf course MGC, member D0001, demo staff).
 */
const PASSWORD = process.env.E2E_PASSWORD ?? 'Demo#Club2026';
const BO = process.env.E2E_BACKOFFICE ?? 'http://localhost:5173';
const OPS = process.env.E2E_OPS ?? 'http://localhost:5175';
const MEMBER = process.env.E2E_MEMBER ?? 'http://localhost:5174';
const WEB = process.env.E2E_WEB ?? 'http://localhost:3000';

/** A weekday at least `min` days ahead (local time). */
function playDay(min: number) {
  const d = new Date();
  d.setDate(d.getDate() + min);
  while (d.getDay() === 0 || d.getDay() === 6) d.setDate(d.getDate() + 1);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

async function login(page: Page, base: string, email: string, path = '/login') {
  await page.goto(`${base}${path}`);
  await page.getByLabel(/^Email Address/).fill(email);
  await page.getByLabel(/^Password/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Log in', exact: true }).click();
  await page.waitForURL((u) => !u.pathname.startsWith('/login'));
}

test('golf day: booking → payment → caddy & golf cart → check-in → tee-off', async ({ browser }) => {
  const day = playDay(3);
  const gm = await (await browser.newContext()).newPage();
  await login(gm, BO, 'golf.manager@demo.oneclub.id');

  // tee sheet of the day (generated on demand) on course MGC; the demo seed
  // has more courses (P0), so every page below keeps its courseId
  await gm.goto(`${BO}/golf/settings`);
  await gm.getByLabel('Course').selectOption({ label: 'Modern Golf Championship Course (MGC)' });
  await gm.waitForURL(/courseId=/);
  const mgc = new URL(gm.url()).searchParams.get('courseId')!;
  await gm.getByLabel('Date').fill(day);
  await gm.getByRole('button', { name: 'Generate' }).click();
  await expect(gm.getByText(/Created \d+/)).toBeVisible();

  // book the first open slot: the demo member and one guest
  await gm.goto(`${BO}/golf/tee-sheet?courseId=${mgc}&date=${day}`);
  await gm.getByRole('button', { name: 'Book', exact: true }).first().click();
  const dialog = gm.getByRole('dialog');
  await dialog.getByLabel('Member No.').fill('D0001');
  await dialog.getByRole('button', { name: 'Add player' }).click();
  await dialog.getByLabel('Name (empty = TBA)').last().fill('Playwright Guest');
  await dialog.getByRole('button', { name: 'Create Booking' }).click();
  const drawer = gm.getByRole('dialog', { name: /Booking BK-/ });
  await expect(drawer).toBeVisible();
  const code = (await drawer.getAttribute('aria-label'))!.replace('Booking ', '');
  await expect(drawer.getByText('Confirmed').first()).toBeVisible();

  // caddies present, assigned from the queue; golf carts from Ready
  await gm.goto(`${BO}/golf/caddies?courseId=${mgc}&date=${day}`);
  // each action waits for the server before the page changes
  const saved = (path: string, method: string) => gm.waitForResponse((r) => r.url().includes(path) && r.request().method() === method);
  await Promise.all([saved('/api/v1/golf/caddy-availability', 'PUT'), gm.getByRole('button', { name: 'Mark all present' }).click()]);
  await gm.getByRole('tab', { name: 'Caddy Assignment' }).click();
  const [caddy] = await Promise.all([saved('/api/v1/golf/caddy-assignments', 'POST'), gm.getByRole('button', { name: 'Assign from queue' }).first().click()]);
  expect(caddy.status()).toBe(201);
  await gm.goto(`${BO}/golf/golf-carts?courseId=${mgc}&date=${day}`);
  await gm.getByRole('tab', { name: 'Golf Cart Assignment' }).click();
  const [cart] = await Promise.all([saved('/api/v1/golf/golf-cart-assignments', 'POST'), gm.getByRole('button', { name: 'Assign Ready carts' }).first().click()]);
  expect(cart.status()).toBe(201);

  // payment at the Front Desk (ops, password login)
  const fd = await (await browser.newContext()).newPage();
  await login(fd, OPS, 'front.desk@demo.oneclub.id', '/login/password');
  // API calls carry what the app sends: the active property and the origin (CSRF)
  const property = await fd.evaluate(() => localStorage.getItem('oneclub.activeProperty') ?? '');
  const headers = { 'X-Property-Id': property, Origin: OPS };
  const api = fd.request;
  const b = await (await api.get(`${OPS}/api/v1/golf/bookings?q=${code}&limit=1`, { headers })).json();
  const booking = await (await api.get(`${OPS}/api/v1/golf/bookings/${b.items[0].id}`, { headers })).json();
  const pay = await api.post(`${OPS}/api/v1/billing/payments`, {
    headers, data: { folioId: booking.folioId, amount: booking.folio.balance, methodType: 'cash', channel: 'venue' },
  });
  expect(pay.status()).toBe(201);

  // check-in by booking code → the flight becomes Ready
  await gm.goto(`${BO}/golf/check-in?courseId=${mgc}`);
  await gm.getByLabel('Value').fill(code);
  await gm.getByLabel('Date').fill(day);
  await gm.getByRole('button', { name: 'Find' }).click();
  await gm.getByRole('button', { name: 'Check-in', exact: true }).click();
  await expect(gm.getByText(/Checked in 2 player/)).toBeVisible();

  // starter: tee-off moves the flight to In Play
  await gm.goto(`${BO}/golf/starter?courseId=${mgc}&date=${day}`);
  const row = gm.getByRole('row', { name: new RegExp(code) });
  await row.getByRole('button', { name: 'Tee-Off' }).click();
  await expect(gm.getByText(/In Play \(\d+\)/)).toBeVisible();
  await expect(gm.locator('.oc-card', { hasText: 'In Play' }).getByText(code)).toBeVisible();
});

test('member portal: digital card and Book Golf', async ({ page }) => {
  await login(page, MEMBER, 'member@demo.oneclub.id');
  await page.goto(`${MEMBER}/membership/card`);
  await expect(page.getByRole('img', { name: 'QR code' })).toBeVisible();
  await page.goto(`${MEMBER}/golf`);
  await expect(page.getByRole('heading', { name: 'Book Golf' })).toBeVisible();
});

test('website: golf pages and Book Golf', async ({ page }) => {
  await page.goto(`${WEB}/en/golf`);
  await expect(page.getByRole('heading', { name: 'Golf Course' })).toBeVisible();
  await page.goto(`${WEB}/en/golf/hole-by-hole`);
  await expect(page.getByRole('heading', { name: 'Hole-by-Hole' })).toBeVisible();
  await page.goto(`${WEB}/en/book-golf`);
  await expect(page.getByRole('heading', { name: 'Book Golf' })).toBeVisible();
});
