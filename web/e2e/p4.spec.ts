import { expect, test, type Page } from '@playwright/test';
import { CASHIER, DASHBOARD, email, login } from './helpers';

/**
 * PRD P4 smoke: the back-office screens of Accounting, Procurement and
 * Inventory and the Warehouse workstation open for their roles without
 * console errors or failing API calls. Run against a running instance like
 * the other specs (see playwright.config.ts): `cd web && pnpm e2e e2e/p4.spec.ts`.
 */

/** Console errors, page errors and failed API calls from now on (after login: the anonymous /auth/me 401 is expected). */
function watchErrors(page: Page) {
  const errors: string[] = [];
  page.on('console', (m) => { // the app's own errors; a blocked web font is the network's business
    if (m.type() === 'error' && !/fonts\.(googleapis|gstatic)\.com/.test(m.location().url)) errors.push(`console: ${m.text()} (${m.location().url})`);
  });
  page.on('pageerror', (e) => errors.push(`page: ${e.message}`));
  page.on('response', (r) => { if (r.url().includes('/api/') && r.status() >= 400) errors.push(`${r.request().method()} ${r.url()}: ${r.status()}`); });
  return errors;
}

/** Opens a screen and waits until its data has loaded (no loading state left). */
async function open(page: Page, url: string) {
  await page.goto(url);
  await page.waitForLoadState('networkidle');
}

test('Accounting: the accountant opens the General Ledger, its trial balance and chart of accounts', async ({ page }) => {
  await login(page, DASHBOARD, email('accountant'), '/accounting/general-ledger');
  const errors = watchErrors(page);
  await open(page, `${DASHBOARD}/accounting/general-ledger`);
  await expect(page.getByRole('heading', { name: 'General Ledger', level: 1 })).toBeVisible();
  for (const tab of ['Trial Balance', 'Chart of Accounts']) {
    await page.getByRole('tab', { name: tab }).click();
    await page.waitForLoadState('networkidle');
  }
  await expect(page.locator('table.oc-table tbody tr').first()).toBeVisible(); // the seeded chart of accounts
  expect(errors).toEqual([]);
});

test('Procurement: procurement staff open Purchase Requisitions', async ({ page }) => {
  await login(page, DASHBOARD, email('procurement_staff'), '/procurement/requisitions');
  const errors = watchErrors(page);
  await open(page, `${DASHBOARD}/procurement/requisitions`);
  await expect(page.getByRole('heading', { name: 'Purchase Requisitions', level: 1 })).toBeVisible();
  await expect(page.getByRole('button', { name: 'New Requisition' })).toBeVisible();
  await page.getByRole('tab', { name: 'Open lines to order' }).click();
  await page.waitForLoadState('networkidle');
  expect(errors).toEqual([]);
});

test('Inventory: the inventory manager opens the Stock Balance', async ({ page }) => {
  await login(page, DASHBOARD, email('inventory_manager'), '/inventory/stock-balance');
  const errors = watchErrors(page);
  await open(page, `${DASHBOARD}/inventory/stock-balance`);
  await expect(page.getByRole('heading', { name: 'Stock Balance', level: 1 })).toBeVisible();
  expect(errors).toEqual([]);
});

test('Warehouse: warehouse staff open Goods Receipt on the Operational workstation', async ({ page }) => {
  await login(page, CASHIER, email('warehouse_staff'));
  await expect(page).toHaveURL(`${CASHIER}/ops`);
  const errors = watchErrors(page);
  await open(page, `${CASHIER}/ops/warehouse/goods-receipt`);
  await expect(page.getByRole('heading', { name: 'Goods Receipt', level: 1 })).toBeVisible();
  await expect(page.getByRole('textbox', { name: 'Find purchase order' })).toBeVisible(); // the receipt starts from a purchase order
  expect(errors).toEqual([]);
});
