import { expect, test } from '@playwright/test';
import { CASHIER, DASHBOARD, KITCHEN, MEMBER, WEB, apiOf, email, login } from './helpers';

/**
 * PRD P3 browser acceptance of the channel screens: Redeem Points with a
 * customer at the POS (FR-LOY-05, FR-OPS-P3-03), the BEO production on the
 * Kitchen Display (FR-BEO-04), the website inquiry with explicit marketing
 * consent (FR-WEB-P3-02, FR-LEAD-09) and paying a down payment online in the
 * Member App (FR-APP-P3-07). Run against a running instance like the other
 * specs (see playwright.config.ts): `cd web && pnpm e2e e2e/p3.spec.ts`.
 */

const SA = email('super_admin');
const stamp = String(Date.now()).slice(-6);

test('POS: the cashier picks the customer and pays part of the bill with loyalty points', async ({ browser }) => {
  const admin = await (await browser.newContext()).newPage();
  await login(admin, DASHBOARD, SA, '/');
  const api = await apiOf(admin);
  const outlet = await api.post('/api/v1/commercial/outlets', { code: `E2E-LP-${stamp}`, name: `E2E Points Cafe ${stamp}`, outletType: 'cafe' });
  const coffee = await api.post('/api/v1/commercial/products', { code: `E2E-LPK-${stamp}`, name: `Kopi Poin ${stamp}`, category: 'Drinks', productType: 'beverage', price: '50000' });
  await api.post('/api/v1/commercial/menus', { code: `E2E-LPM-${stamp}`, name: 'All Day', outletId: outlet.id, productIds: [coffee.id], channels: ['pos'] });
  const customer = await api.post('/api/v1/crm/customers', { code: `E2E-LP-${stamp}`, name: `Pelanggan Poin ${stamp}`, phone: `+62819${stamp}` });
  const account = await api.post('/api/v1/crm/loyalty/accounts', { customerId: customer.id });
  await api.post(`/api/v1/crm/loyalty/accounts/${account.id}:adjust`, { points: 500, reason: 'E2E welcome points' });

  const context = await browser.newContext();
  const page = await context.newPage();
  await login(page, CASHIER, email('cashier'));
  await page.getByRole('button', { name: outlet.name }).click();
  await page.getByRole('link', { name: 'POS' }).click();
  page.once('dialog', (d) => void d.accept('0'));
  await page.getByRole('button', { name: 'Open shift' }).click();
  await expect(page.getByText(/Shift \S+ open/)).toBeVisible();
  await page.getByRole('button', { name: new RegExp(`Kopi Poin ${stamp}`) }).click();

  await page.getByLabel('Find customer').fill(`Pelanggan Poin ${stamp}`);
  const pick = page.getByRole('combobox', { name: 'Customer', exact: true }); // the order row is also labelled "Customer"
  await expect(pick.locator('option', { hasText: `Pelanggan Poin ${stamp}` })).toHaveCount(1);
  await pick.selectOption({ label: `Pelanggan Poin ${stamp} (E2E-LP-${stamp})` });
  await expect(page.getByText('500 points')).toBeVisible();
  await page.getByLabel('Payment', { exact: true }).selectOption({ label: 'Redeem Points' });
  await page.getByLabel('Points', { exact: true }).fill('200');
  await page.getByLabel('Rest paid by').selectOption('cash');
  await page.getByRole('button', { name: 'Pay & send' }).click();
  await expect(page.getByText(/Paid 200 points and cash/)).toBeVisible();

  const ledger = await api.get(`/api/v1/crm/loyalty/accounts/${account.id}/ledger?filter[kind]=redeemed`);
  expect(ledger.items).toHaveLength(1);
  expect(ledger.items[0].points).toBe(-200);
  await context.close();
  await admin.context().close();
});

test('Kitchen Display: kitchen staff switch to the BEO production of the day', async ({ page }) => {
  await login(page, KITCHEN, email('kitchen_staff'));
  await expect(page).toHaveURL(`${KITCHEN}/kitchen`);
  await expect(page.getByRole('heading', { name: 'Kitchen' })).toBeVisible();
  await page.getByRole('tab', { name: 'Banquet Production' }).click();
  await expect(page.getByRole('heading', { name: 'Banquet Production' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'To produce' })).toBeVisible();
  await expect(page.getByLabel('Date')).toHaveValue(/\d{4}-\d{2}-\d{2}/);
  await page.getByRole('tab', { name: 'Orders' }).click();
  await expect(page.getByRole('heading', { name: 'Preparing' })).toBeVisible();
});

test('Website: inquiry with an unticked marketing consent, corporate golf and the complaint link in the footer', async ({ browser, page }) => {
  await page.goto(`${WEB}/en/wedding-banquet`);
  const consent = page.getByRole('checkbox', { name: /Send me news and offers/ });
  await expect(consent).not.toBeChecked(); // explicit opt-in (UU PDP)
  // a select inside its label: the label text also holds the options, so find it by role
  await page.getByRole('combobox', { name: /^Event/ }).selectOption('corporate_golf');
  await page.getByLabel(/^Company/).fill(`PT E2E Golf ${stamp}`);
  await page.getByLabel('Message', { exact: true }).fill('Corporate golf day for 40 clients');
  await page.getByLabel('Name', { exact: true }).fill(`E2E Inquiry ${stamp}`);
  await page.getByLabel('E-mail', { exact: true }).fill(`inquiry${stamp}@e2e.test`);
  await consent.check();
  await page.getByRole('button', { name: 'Send' }).click();
  await expect(page.getByText(/our sales team will contact you/)).toBeVisible();
  await expect(page.locator('footer').getByRole('link', { name: 'Make a complaint' })).toHaveAttribute('href', '/en/complaint');

  const admin = await (await browser.newContext()).newPage();
  await login(admin, DASHBOARD, SA, '/');
  const api = await apiOf(admin);
  const leads = await api.get(`/api/v1/crm/leads?q=inquiry${stamp}`);
  expect(leads.items).toHaveLength(1);
  expect(leads.items[0].line).toBe('golf');
  expect(leads.items[0].marketingConsent).toBe(true);

  await page.goto(`${WEB}/en/contact`);
  await expect(page.getByRole('checkbox', { name: /Send me news and offers/ })).not.toBeChecked();
  await expect(page.getByLabel('Topic').locator('option')).toContainText(['corporate golf', 'tournament']);
  await admin.context().close();
});

test('Member App: the member pays the down payment of a payment schedule online', async ({ browser, page }) => {
  const admin = await (await browser.newContext()).newPage();
  await login(admin, DASHBOARD, SA, '/');
  const api = await apiOf(admin);
  const me = (await api.get('/api/v1/crm/customers?q=E2E-MEMBER')).items[0];
  const folio = await api.post('/api/v1/billing/folios', { holderName: `Family wedding ${stamp}`, customerId: me.id });
  const today = new Date().toISOString().slice(0, 10);
  await api.post('/api/v1/billing/payment-schedules', { title: `Family wedding ${stamp}`, folioId: folio.id, customerId: me.id, sourceType: 'other',
    totalAmount: '10000000', lines: [{ label: `DP ${stamp}`, kind: 'down_payment', percent: '30', dueDate: today },
      { label: `Pelunasan ${stamp}`, kind: 'final', percent: '70', dueDate: today }] });

  await login(page, MEMBER, email('member'));
  await page.goto(`${MEMBER}/transactions/invoices`);
  await expect(page.getByText(`Family wedding ${stamp}`)).toBeVisible();
  await page.getByRole('button', { name: `Pay DP ${stamp}` }).click();
  await expect(page.getByRole('heading', { name: 'Online payment' })).toBeVisible();
  await expect(page.locator('.oc-metric', { hasText: /Rp\s?3[.,]000[.,]000/ })).toBeVisible();
  await admin.context().close();
});
