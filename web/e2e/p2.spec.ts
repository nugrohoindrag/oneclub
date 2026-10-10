import { expect, test } from '@playwright/test';
import { CADDY, CASHIER, DASHBOARD, MEMBER, WEB, email, login } from './helpers';

/**
 * PRD P2 browser acceptance (FR-REL-P2-02): module hubs and KPI dashboards,
 * ops interfaces, the caddy tablet with its offline queue, the member app
 * and website booking pages.
 */

test('Back Office: P2 module hubs list master data and operations', async ({ page }) => {
  await login(page, DASHBOARD, email('golf_manager'));
  await page.goto(`${DASHBOARD}/golf/operations`);
  await expect(page.getByRole('heading', { name: 'Round Operations', level: 1 })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Caddy Settlement' })).toBeVisible();
  await page.goto(`${DASHBOARD}/golf/master`);
  await expect(page.getByRole('heading', { name: 'Caddy Levels' })).toBeVisible(); // generated from the resource definitions
  await page.getByRole('link', { name: /Caddy Levels/ }).first().click();
  await expect(page.getByRole('button', { name: /Add Caddy Level/ })).toBeVisible();
});

test('Management: KPI dashboards show P2 figures', async ({ page }) => {
  await login(page, DASHBOARD, email('general_manager'));
  for (const [path, title] of [['sport-club-performance', 'Sport Club Performance'], ['commercial-performance', 'Commercial Performance']]) {
    await page.goto(`${DASHBOARD}/management/${path}`);
    await expect(page.getByRole('heading', { name: title })).toBeVisible();
    await expect(page.locator('.oc-dash-hero').first()).toBeVisible(); // the lead KPI of the domain
  }
});

test('Ops: Starter sees Pace of Play; Sport Reception validates access', async ({ page }) => {
  await login(page, CASHIER, email('starter_marshal'));
  await page.goto(`${CASHIER}/ops/starter/pace`);
  await expect(page.getByRole('heading', { name: 'Pace of Play' })).toBeVisible();
  const ctx = await page.context().browser()!.newContext();
  const p = await ctx.newPage();
  await login(p, CASHIER, email('sport_club_receptionist'));
  await p.goto(`${CASHIER}/ops/sport-reception`); // the old path opens Tiket Masuk of the Sport Club desk
  await expect(p.getByRole('heading', { name: 'Tiket Masuk' })).toBeVisible();
  await p.getByRole('tab', { name: /Scan & Okupansi/ }).click();
  await expect(p.getByRole('heading', { name: 'Facility Access' })).toBeVisible();
  await ctx.close();
});

test('Caddy Tablet: assignments, earnings and the offline queue', async ({ page }) => {
  await login(page, CADDY, email('caddy'));
  await expect(page.getByRole('navigation', { name: 'Caddy Tablet' })).toBeVisible();
  await page.goto(`${CADDY}/tablet/earnings`);
  await expect(page.getByRole('heading', { name: 'Earnings' })).toBeVisible();
  await page.goto(`${CADDY}/tablet/sync`);
  await page.getByRole('button', { name: 'Simulate offline' }).click();
  await expect(page.getByText(/No signal/)).toBeVisible();
  await page.getByRole('button', { name: 'Go back online' }).click();
});

test('Member App: scores, sport club, vouchers, fees & requests and preferences', async ({ page }) => {
  await login(page, MEMBER, email('member'));
  for (const [path, title] of [['golf/scores', 'Scores & Handicap'], ['sport-club', 'Pesan Lapangan'], ['vouchers', 'Voucher & Prepaid'],
    ['membership/services', 'Fees & Requests'], ['preferences', 'Preferences']]) {
    await page.goto(`${MEMBER}/${path}`);
    await expect(page.getByRole('heading', { name: title, level: 1 })).toBeVisible();
  }
});

test('Website: Sport Club page, structured rates and the contact form', async ({ page }) => {
  await page.goto(`${WEB}/en/sport-club`);
  await expect(page.getByRole('heading', { name: 'Sports Club' }).first()).toBeVisible();
  // the live site's contact form, posted to the contact API
  await page.goto(`${WEB}/en/contact`);
  const form = page.locator('form.book-form').filter({ has: page.getByPlaceholder('Message') });
  await form.getByPlaceholder('Player Name').fill('Playwright Visitor');
  await form.getByPlaceholder('Email').fill('visitor@playwright.test');
  await form.getByPlaceholder('Subject').fill('Family package');
  await form.getByPlaceholder('Message').fill('Do you have a family package?');
  await form.getByRole('button', { name: 'SUBMIT' }).click();
  await expect(page.getByText('Thank you, we have received your message.')).toBeVisible();
});
