import { expect, test } from '@playwright/test';
import { CASHIER, DASHBOARD, MEMBER, WEB, apiOf, login, playDay, rowAction } from './helpers';

/**
 * PRD P1 FR-REL-05: the critical golf flow in the browser —
 * booking → payment → caddy & golf cart → check-in → tee-off.
 * Uses the demo seed (Modern Golf course MGC, member D0001, demo staff).
 */

test('golf day: booking → payment → caddy & golf cart → check-in → tee-off', async ({ browser }) => {
  const day = playDay(3);
  const gm = await (await browser.newContext()).newPage();
  await login(gm, DASHBOARD, 'golf.manager@demo.oneclub.id');

  // tee sheet of the day (generated on demand) on course MGC; the demo seed
  // has more courses (P0), so every page below keeps its courseId
  await gm.goto(`${DASHBOARD}/golf/settings`);
  await gm.getByLabel('Course').selectOption({ label: 'Modern Golf Championship Course (MGC)' });
  await gm.waitForURL(/courseId=/);
  const mgc = new URL(gm.url()).searchParams.get('courseId')!;
  await gm.getByLabel('Date').fill(day);
  await gm.getByRole('button', { name: 'Generate' }).click();
  await expect(gm.getByText(/Created \d+/)).toBeVisible();

  // book the first open slot: the demo member and one guest
  await gm.goto(`${DASHBOARD}/golf/tee-sheet?courseId=${mgc}&date=${day}`);
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
  await gm.goto(`${DASHBOARD}/golf/caddies?courseId=${mgc}&date=${day}`);
  // each action waits for the server before the page changes
  const saved = (path: string, method: string) => gm.waitForResponse((r) => r.url().includes(path) && r.request().method() === method);
  await Promise.all([saved('/api/v1/golf/caddy-availability', 'PUT'), gm.getByRole('button', { name: 'Mark all present' }).click()]);
  await gm.getByRole('tab', { name: 'Caddy Assignment' }).click();
  const [caddy] = await Promise.all([saved('/api/v1/golf/caddy-assignments', 'POST'), rowAction(gm.locator('tbody tr').first(), 'Assign from queue')]);
  expect(caddy.status()).toBe(201);
  await gm.goto(`${DASHBOARD}/golf/golf-carts?courseId=${mgc}&date=${day}`);
  await gm.getByRole('tab', { name: 'Golf Cart Assignment' }).click();
  const [cart] = await Promise.all([saved('/api/v1/golf/golf-cart-assignments', 'POST'), rowAction(gm.locator('tbody tr').first(), 'Assign Ready carts')]);
  expect(cart.status()).toBe(201);

  // payment at the Front Desk (Operational area, password login)
  const fd = await (await browser.newContext()).newPage();
  await login(fd, CASHIER, 'front.desk@demo.oneclub.id');
  const api = await apiOf(fd);
  const b = await api.get(`/api/v1/golf/bookings?q=${code}&limit=1`);
  const booking = await api.get(`/api/v1/golf/bookings/${b.items[0].id}`);
  await api.post('/api/v1/billing/payments', { folioId: booking.folioId, amount: booking.folio.balance, methodType: 'cash', channel: 'venue' });

  // check-in by booking code → the flight becomes Ready
  await gm.goto(`${DASHBOARD}/golf/check-in?courseId=${mgc}`);
  await gm.getByLabel('Value').fill(code);
  await gm.getByLabel('Date').fill(day);
  await gm.getByRole('button', { name: 'Find' }).click();
  await rowAction(gm.getByRole('row', { name: new RegExp(code) }), 'Check-in');
  await expect(gm.getByText(/Checked in 2 player/)).toBeVisible();

  // starter: tee-off moves the flight to In Play
  await gm.goto(`${DASHBOARD}/golf/starter?courseId=${mgc}&date=${day}`);
  const row = gm.getByRole('row', { name: new RegExp(code) });
  await row.getByRole('button', { name: 'Tee-Off' }).click(); // the starter queue keeps its actions inline
  await expect(gm.getByText(/In Play \(\d+\)/)).toBeVisible();
  await expect(gm.locator('.oc-card', { hasText: 'In Play' }).getByText(code)).toBeVisible();
});

test('member portal: digital card and Book Golf', async ({ page }) => {
  await login(page, MEMBER, 'member@demo.oneclub.id');
  await page.goto(`${MEMBER}/membership/card`);
  await expect(page.getByRole('img', { name: 'QR code' })).toBeVisible();
  await page.goto(`${MEMBER}/golf`); // the old Book Golf link opens the tee time booking
  await expect(page.getByRole('heading', { name: 'Book Tee Time' })).toBeVisible();
});

test('website: golf pages and Book Golf', async ({ page }) => {
  await page.goto(`${WEB}/en/golf`);
  await expect(page.getByRole('heading', { name: 'Golf Course', level: 1 })).toBeVisible();
  await page.goto(`${WEB}/en/golf/hole-by-hole`);
  await expect(page.getByRole('heading', { name: 'Hole-by-Hole' })).toBeVisible();
  await page.goto(`${WEB}/en/book-golf`);
  await expect(page.getByRole('heading', { name: 'Book Golf' })).toBeVisible();
});
