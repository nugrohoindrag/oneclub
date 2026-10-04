import { expect, test, type BrowserContext, type Page } from '@playwright/test';
import { CADDY, CASHIER, DASHBOARD, MEMBER, apiOf, email, login, playDay } from './helpers';

/**
 * Offline areas of the Staff App (Technical Doc §6.4, PRD FR-SH-05,
 * FR-CTB): the POS keeps selling and the caddy tablet plays a full round
 * without a connection; back online the queue syncs once, without duplicates.
 */

const SA = email('super_admin');
const stamp = String(Date.now()).slice(-6);

/**
 * Cuts the connection: the browser goes offline and the app's own switch is
 * used as well, because Chrome on Windows keeps navigator.onLine true for
 * service-worker pages under network emulation.
 */
async function goOffline(page: Page, context: BrowserContext) {
  await page.getByRole('link', { name: /pending/ }).click();
  await page.getByRole('button', { name: 'Simulate offline' }).click();
  await expect(page.getByRole('link', { name: /^Offline/ })).toBeVisible();
  await context.setOffline(true);
}

/** Restores the connection on the sync queue page and waits until every action is synced. */
async function syncAll(page: Page, context: BrowserContext, actions: number) {
  const rows = page.locator('table.oc-table tbody tr');
  await expect(rows).toHaveCount(actions);
  await expect(rows.locator('.oc-status', { hasText: 'Pending' })).toHaveCount(actions);
  await context.setOffline(false);
  await page.getByRole('button', { name: 'Go back online' }).click({ timeout: 3_000 }).catch(() => undefined);
  await expect(rows.locator('.oc-status', { hasText: 'Completed' })).toHaveCount(actions, { timeout: 30_000 });
}

test('POS: a sale without connection is queued and synced as one order', async ({ browser }) => {
  // Outlet, product and menu for this run (the demo seed has no outlet).
  const admin = await (await browser.newContext()).newPage();
  await login(admin, DASHBOARD, SA, '/');
  const api = await apiOf(admin);
  const outlet = await api.post('/api/v1/commercial/outlets', { code: `E2E-${stamp}`, name: `E2E Kiosk ${stamp}`, outletType: 'restaurant' });
  const coffee = await api.post('/api/v1/commercial/products', { code: `E2E-KOPI-${stamp}`, name: `Kopi ${stamp}`, category: 'Drinks', productType: 'beverage', price: '25000' });
  await api.post('/api/v1/commercial/menus', { code: `E2E-MENU-${stamp}`, name: 'All Day', outletId: outlet.id, productIds: [coffee.id], channels: ['pos'] });

  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page, CASHIER, email('cashier'));
  await expect(page).toHaveURL(/\/ops$/);
  await page.getByRole('button', { name: outlet.name }).click();
  await page.getByRole('link', { name: 'POS' }).click();
  page.once('dialog', (d) => void d.accept('500000'));
  await page.getByRole('button', { name: 'Open shift' }).click();
  await expect(page.getByText(/Shift \S+ open/)).toBeVisible();
  await expect(page.getByRole('button', { name: new RegExp(`Kopi ${stamp}`) })).toBeVisible();

  await goOffline(page, context);
  await page.goBack(); // the POS screen keeps working from what it already loaded
  await page.getByRole('button', { name: new RegExp(`Kopi ${stamp}`) }).click();
  await page.getByRole('button', { name: new RegExp(`Kopi ${stamp}`) }).click();
  await page.getByRole('button', { name: 'Pay & send' }).click();
  await page.getByRole('link', { name: /pending/ }).click();
  await syncAll(page, context, 1);

  const orders = await api.get(`/api/v1/commercial/orders?filter[outletId]=${outlet.id}`);
  expect(orders.items).toHaveLength(1);
  const order = await api.get(`/api/v1/commercial/orders/${orders.items[0].id}`);
  expect(Number(order.lines[0].quantity)).toBe(2);
  await context.close();
});

test('Caddy Tablet: an 18-hole round without signal syncs once; the member rates the caddy', async ({ browser }) => {
  // A checked-in flight of course MGC with the e2e caddy (C001, e2e/setup.sql).
  const admin = await (await browser.newContext()).newPage();
  await login(admin, DASHBOARD, SA, '/');
  const api = await apiOf(admin);
  const mgc = (await api.get('/api/v1/golf/courses?limit=100')).items.find((c: { code: string }) => c.code === 'MGC');
  // One caddy per player (Caddy Policy); C001 is the e2e caddy's (e2e/setup.sql).
  const all = (await api.get('/api/v1/golf/caddies?limit=200')).items;
  const caddies = ['C001', 'C002', 'C003'].map((code) => all.find((c: { code: string }) => c.code === code).id as string);
  const ch18 = (await api.get(`/api/v1/golf/playing-routes?filter[courseId]=${mgc.id}&limit=100`)).items.find((r: { holeCount: number }) => r.holeCount === 18);
  type Booking = { code: string; flights: { id: string }[]; players: { id: string }[]; folioId: string; folio: { balance: string } };
  let bk: Booking | undefined;
  let day = '';
  let refused = '';
  // The latest free 18-hole slot (late slots play nine) of a day where the
  // demo member and the caddy are still free (earlier local runs used some).
  for (let ahead = 6; !bk && ahead < 30; ahead++) {
    day = playDay(ahead);
    await api.post('/api/v1/golf/tee-sheets:generate', { courseId: mgc.id, from: day, days: 1 });
    const slots = (await api.get(`/api/v1/golf/tee-times?date=${day}&courseId=${mgc.id}&limit=500`)).items
      .filter((s: { playingRouteId: string }) => s.playingRouteId === ch18.id);
    for (const slot of slots.reverse().slice(0, 5)) {
      const r = await api.send('POST', '/api/v1/golf/bookings', { bookingType: 'member', channel: 'back_office', teeTimeId: slot.id,
        players: [{ playerType: 'member', memberNo: 'D0001' }, { playerType: 'guest_of_member', name: 'Tablet Guest 1' }, { playerType: 'guest_of_member', name: 'Tablet Guest 2' }] });
      if (r.status() !== 201) {
        refused = `${r.status()} ${await r.text()}`;
        continue;
      }
      const b: Booking = await r.json();
      await api.put('/api/v1/golf/caddy-availability', { date: day, entries: caddies.map((caddyId) => ({ caddyId, status: 'present' })) });
      const a = await api.send('POST', '/api/v1/golf/caddy-assignments', { flightId: b.flights[0].id, assignments: caddies.map((caddyId, i) => ({ caddyId, playerIds: [b.players[i].id] })) });
      if (a.status() === 201) bk = b;
      else refused = `${a.status()} ${await a.text()}`;
      break;
    }
  }
  expect(bk, `a free tee time and caddy (last refusal: ${refused})`).toBeDefined();
  if (Number(bk!.folio.balance) > 0) {
    await api.post('/api/v1/billing/payments', { folioId: bk!.folioId, amount: bk!.folio.balance, methodType: 'cash', channel: 'venue' });
  }
  const flightId = bk!.flights[0].id;
  await api.post('/api/v1/golf/golf-cart-assignments', { flightId, auto: true }); // with carts the flight joins the starter queue
  await api.post('/api/v1/golf/check-ins', { method: 'booking_code', value: bk!.code, date: day });
  expect((await api.get(`/api/v1/golf/rounds/${flightId}`)).round.status).toBe('ready'); // in the starter queue

  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page, CADDY, email('caddy'));
  await expect(page).toHaveURL(/\/tablet$/);
  await page.locator('.oc-row-wrap', { hasText: bk!.code }).getByRole('link', { name: 'Open' }).click();
  await expect(page.getByRole('heading', { name: bk!.code })).toBeVisible();

  await goOffline(page, context);
  await page.goBack(); // the round stays open from what the tablet already loaded
  await page.getByRole('button', { name: 'Start Round' }).click();
  for (let hole = 2; hole <= 18; hole++) await page.getByRole('button', { name: `Next hole → ${hole}` }).click();
  await page.getByRole('button', { name: 'Complete Round' }).click();
  await page.getByRole('link', { name: /pending/ }).click();
  await syncAll(page, context, 19); // tee-off, holes 2–18, finish

  const round = (await api.get(`/api/v1/golf/rounds/${flightId}`)).round;
  expect(round.status).toBe('completed');
  await expect.poll(async () => (await api.get(`/api/v1/golf/rounds/${flightId}/times`)).holes.length, { timeout: 20_000 }).toBe(18);
  await context.close();

  // After the round the member rates the caddy in the Member App (My Caddy).
  const member = await (await browser.newContext()).newPage();
  await login(member, MEMBER, 'member@demo.oneclub.id');
  await member.goto(`${MEMBER}/golf/my-caddy`);
  const rate = member.locator('.oc-card', { hasText: bk!.code }).getByRole('group', { name: /^Rate caddy/ }).first();
  await rate.getByRole('button', { name: '5 of 5' }).click();
  await rate.getByRole('button', { name: 'Rate caddy' }).click();
  await expect(member.getByText(/Thank you for rating/)).toBeVisible();
});
