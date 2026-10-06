import { expect, test, type Locator, type Page } from '@playwright/test';
import { execFileSync } from 'node:child_process';
import { createHash, createHmac } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { CASHIER, DASHBOARD, WEB, apiOf, email, login } from './helpers';

/**
 * PRD P3 / P4 §9 flows chained end to end in the browser (FR-REL-P3-02,
 * FR-REL-P4-02): the steps a person does on a screen run in the browser (the
 * website inquiry, the customer accepting the quotation with the one-time
 * code on its public link, issuing the BEO, receiving the goods on the
 * Warehouse workstation, reviewing the closing checklist and soft-closing
 * the period); the back-office steps between them run through the API with
 * the session of a logged-in page. Run against a running instance like the
 * other specs (see playwright.config.ts): `cd web && pnpm e2e e2e/p34-flows.spec.ts`.
 */

const SA = email('super_admin');
const stamp = String(Date.now()).slice(-6);
const day = (n: number) => new Date(Date.now() + n * 86_400_000).toISOString().slice(0, 10);

/**
 * One value from the instance database (local and CI runs; e2e/global-setup
 * uses the same connection). Staging sets E2E_SKIP_SETUP and has no access.
 */
function sql(query: string): string {
  const url = process.env.E2E_DATABASE_URL ?? 'postgresql://postgres:postgres@localhost:5432/oneclub_mgcc';
  return execFileSync(process.env.PSQL ?? 'psql', [url, '-At', '-v', 'ON_ERROR_STOP=1', '-c', query], { encoding: 'utf8' }).trim();
}

/**
 * The customer's one-time code of a quotation (PRD P3 §16 #18). The code is erased from the delivery once sent (PO decision
 * 4f) and kept only in the sandbox inbox of the worker process, so the test stands in for the customer's mailbox: it replaces
 * the stored hash of the active code with the hash of a code it knows (hex HMAC-SHA256 with the instance secret as pepper, as
 * crm/sales otpHash). The secret comes from E2E_APP_SECRET or the stack's .env.local (local / CI runs).
 */
function knownAcceptanceCode(quotationId: string): string {
  let secret = process.env.E2E_APP_SECRET ?? '';
  if (!secret) {
    const env = readFileSync(resolve(process.env.E2E_ENV_FILE ?? '../.env.local'), 'utf8'); // Playwright runs from web/
    secret = /^APP_SECRET=(.*)$/m.exec(env)?.[1]?.trim() ?? '';
  }
  expect(secret, 'APP_SECRET of the instance (E2E_APP_SECRET or .env.local)').not.toBe('');
  const id = quotationId.replace(/[^0-9a-f-]/gi, '');
  const [otp, salt] = sql(`SELECT id, salt FROM crm.sales_quotation_otps WHERE quotation_id = '${id}' AND status = 'active'
    ORDER BY created_at DESC LIMIT 1`).split('|');
  expect(otp, 'an active acceptance code').toBeTruthy();
  const code = String(100000 + Math.floor(Math.random() * 900000));
  const key = createHash('sha256').update(Buffer.concat([Buffer.from('oneclub/crm/quotation-otp\x00'), Buffer.from(secret)])).digest();
  const hash = createHmac('sha256', key).update(`${salt}|${id}|${code}`).digest('hex');
  sql(`UPDATE crm.sales_quotation_otps SET code_hash = '${hash}' WHERE id = '${otp}'`);
  return code;
}

/** API calls of a logged-in page at a given property (apiOf works at MAIN). */
async function apiAt(page: Page, property: string) {
  const call = async (method: string, path: string, body?: unknown) => {
    const r = await page.evaluate(async ([method, path, body, property]) => {
      const res = await fetch(path, { method, headers: { 'Content-Type': 'application/json', 'X-Property-Id': property as string },
        body: body === undefined ? undefined : JSON.stringify(body), cache: 'no-store' });
      return { status: res.status, body: await res.text() };
    }, [method, path, body, property] as const);
    expect(r.status < 300, `${method} ${path}: ${r.status} ${r.body}`).toBeTruthy();
    return r.body ? JSON.parse(r.body) : null;
  };
  return { get: (p: string) => call('GET', p), post: (p: string, b: unknown = {}) => call('POST', p, b) };
}

/** Selects the option of a select whose text contains `text` (option labels carry codes and statuses). */
async function pick(select: Locator, text: string) {
  const option = select.locator('option', { hasText: text });
  await expect(option).toHaveCount(1);
  await select.selectOption((await option.getAttribute('value'))!);
}

test('P3 §9.1 wedding: website inquiry → quotation accepted with the one-time code → DP → Definite → BEO issued', async ({ browser, page }) => {
  test.skip(!!process.env.E2E_SKIP_SETUP, 'the one-time code is set through the instance database (local / CI runs)');
  test.setTimeout(180_000);
  const name = `E2E Wedding ${stamp}`;
  const mail = `wedding${stamp}@e2e.test`;

  // 1. Inquiry on the website (FR-WEB-P3-02) → lead on the wedding line.
  await page.goto(`${WEB}/en/wedding-banquet`);
  await page.getByRole('combobox', { name: /^Event/ }).selectOption('wedding');
  await page.getByLabel('Pax', { exact: true }).fill('200');
  await page.getByLabel('Message', { exact: true }).fill('Wedding reception for 200 guests');
  await page.getByLabel('Name', { exact: true }).fill(name);
  await page.getByLabel('E-mail', { exact: true }).fill(mail);
  await page.getByRole('button', { name: 'Send' }).click();
  await expect(page.getByText(/our sales team will contact you/)).toBeVisible();

  // 2. Sales converts the lead and sends a quotation that holds the ballroom (FR-LEAD-06, FR-QUO-01/04).
  const admin = await (await browser.newContext()).newPage();
  await login(admin, DASHBOARD, SA, '/');
  const api = await apiOf(admin);
  const leads = (await api.get(`/api/v1/crm/leads?q=wedding${stamp}`)).items;
  expect(leads).toHaveLength(1);
  expect(leads[0].line).toBe('wedding');
  const conv = await api.post(`/api/v1/crm/leads/${leads[0].id}:convert`, { createOpportunity: false });
  const customerId = conv.customerId ?? conv.customer?.id ?? conv.lead?.customerId;
  expect(customerId, JSON.stringify(conv)).toBeTruthy();
  const venue = await api.post('/api/v1/banquet/venues', { code: `E2E-WB-${stamp}`, name: `E2E Ballroom ${stamp}`, venueType: 'ballroom', maxCapacity: 400 });
  const draft = await api.post('/api/v1/crm/quotations', { customerId, title: `Wedding ${name}`, line: 'wedding', eventType: 'wedding', eventDate: day(90),
    pax: 200, venueResourceId: venue.resourceId, optionDate: day(5), pricingMode: 'nett',
    paymentTerms: [{ label: 'DP 30%', percent: '30', dueDays: 3 }, { label: 'Final Payment', percent: '70', dueDays: 30 }],
    lines: [{ itemType: 'banquet_package', description: 'Wedding package 200 pax', quantity: '1', unitPrice: '60000000' }] });
  const sent = await api.post(`/api/v1/crm/quotations/${draft.id}:send`, { channels: ['email'] });
  const token = String(sent.publicLink).split('/quotation/')[1];
  expect(token, String(sent.publicLink)).toBeTruthy();
  const eventOf = async () => (await api.get(`/api/v1/banquet/events?q=${encodeURIComponent(`Wedding ${name}`)}`)).items[0];
  await expect.poll(async () => (await eventOf())?.status, { timeout: 30_000 }).toBe('tentative');

  // 3. The customer accepts on the public link with the one-time code (PRD P3 §16 #18).
  await page.goto(`${WEB}/en/quotation/${token}`);
  await expect(page.getByText('Awaiting your decision')).toBeVisible();
  await page.getByLabel('Full name').fill(name);
  await page.getByRole('checkbox', { name: /I accept the quotation/ }).check();
  await page.getByRole('button', { name: 'Send verification code' }).click();
  await expect(page.getByText(/We sent a 6-digit code by e-mail/)).toBeVisible();
  const code = knownAcceptanceCode(String(draft.id));
  expect(code).toMatch(/^\d{6}$/);
  await page.getByLabel('Verification code').fill(code);
  await page.getByRole('button', { name: 'Accept quotation' }).click();
  await expect(page.getByText(`Thank you, ${name}`)).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Pay down payment' })).toBeVisible();

  // 4. The accepted quotation becomes the event with its DP schedule; the DP received makes it Definite (FR-BQT-08).
  await expect.poll(async () => (await eventOf())?.scheduleId ?? null, { timeout: 30_000 }).not.toBeNull();
  const ev = await api.get(`/api/v1/banquet/events/${(await eventOf()).id}`);
  const schedule = await api.get(`/api/v1/billing/payment-schedules/${ev.scheduleId}`);
  const dp = schedule.lines[0];
  expect(Number(dp.amount)).toBe(18_000_000);
  await api.post(`/api/v1/billing/payment-schedules/${schedule.id}/lines/${dp.id}:pay`, { methodType: 'bank_transfer', reference: `TRF-E2E-${stamp}` });
  await expect.poll(async () => (await api.get(`/api/v1/banquet/events/${ev.id}`)).status, { timeout: 30_000 }).toBe('definite');

  // 5. The Banquet Manager issues the BEO on the BEO screen (FR-BEO-01/02).
  await login(page, DASHBOARD, email('banquet_manager'), '/banquet-event/beo');
  await page.goto(`${DASHBOARD}/banquet-event/beo`);
  await expect(page.getByRole('heading', { name: 'Banquet Event Order', level: 1 })).toBeVisible();
  await page.getByLabel('Event', { exact: true }).selectOption({ label: `${ev.number} · ${ev.title}` });
  await page.getByRole('button', { name: 'New BEO' }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog.getByText(/· draft$/)).toBeVisible();
  await dialog.getByRole('button', { name: 'Issue BEO' }).click();
  await expect(dialog.getByText(/· issued$/)).toBeVisible();
  const beos = (await api.get(`/api/v1/banquet/beos?filter[eventId]=${ev.id}`)).items;
  expect(beos).toHaveLength(1);
  expect(beos[0].status).toBe('issued');

  // The event detail shows the definite event and its food cost (PRD P4 §9.2).
  await page.goto(`${DASHBOARD}/banquet-event/events/${ev.id}?tab=food_cost`);
  await expect(page.getByRole('heading', { name: `${ev.number} · ${ev.title}`, level: 1 })).toBeVisible();
  await expect(page.getByRole('tab', { name: 'Food Cost' })).toBeVisible();
  await admin.context().close();
});

test('P4 §9.1 procure-to-pay: PR → PO → goods received on the Warehouse workstation → vendor invoice matched', async ({ browser, page }) => {
  test.setTimeout(150_000);
  const admin = await (await browser.newContext()).newPage();
  await login(admin, DASHBOARD, SA, '/');
  const api = await apiOf(admin);
  const kg = await api.post('/api/v1/inventory/uoms', { code: `E2E-KG-${stamp}`, name: 'Kilogram', kind: 'mass' });
  const item = await api.post('/api/v1/inventory/items', { code: `E2E-SUGAR-${stamp}`, name: `E2E Sugar ${stamp}`, baseUomId: kg.id, standardCost: '12000' });
  const wh = await api.post('/api/v1/inventory/warehouses', { code: `E2E-MS-${stamp}`, name: `E2E Main Store ${stamp}`, locationType: 'store' });
  const supplier = await api.post('/api/v1/procurement/suppliers', { code: `E2E-SUP-${stamp}`, name: `PT E2E Pemasok ${stamp}`, email: `sup${stamp}@e2e.test` });

  // Procurement staff raise the purchase requisition on its screen (FR-PR-01/03); the approver (another user) approves it.
  await login(page, DASHBOARD, email('procurement_staff'), '/procurement/requisitions');
  await page.goto(`${DASHBOARD}/procurement/requisitions`);
  await page.getByRole('button', { name: 'New Requisition' }).click();
  const modal = page.getByRole('dialog', { name: 'New Purchase Requisition' });
  await modal.getByLabel('Title').fill(`E2E stock ${stamp}`);
  await pick(modal.getByLabel('Destination warehouse'), wh.name);
  await pick(modal.getByLabel('Item 1'), item.name);
  await modal.getByLabel('Quantity').fill('20');
  await modal.getByLabel('Estimated price').fill('12500');
  await modal.getByRole('button', { name: 'Submit for approval' }).click();
  await expect(modal).toBeHidden();
  await expect(page.getByRole('dialog', { name: /^Purchase Requisition PR-/ })).toBeVisible(); // the submitted requisition opens
  const staff = await apiOf(page);
  let pr = (await staff.get(`/api/v1/procurement/requisitions?q=${encodeURIComponent(`E2E stock ${stamp}`)}`)).items[0];
  expect(pr.status).toBe('submitted');
  const manager = await (await browser.newContext()).newPage();
  await login(manager, DASHBOARD, email('procurement_manager'), '/');
  const approver = await apiOf(manager);
  pr = await approver.post(`/api/v1/procurement/requisitions/${pr.id}:approve`, { reason: 'E2E stock' });
  expect(pr.status).toBe('approved');

  // Purchase order from the requisition line (FR-PO-01/02), approved by the approver.
  pr = await staff.get(`/api/v1/procurement/requisitions/${pr.id}`);
  let po = await staff.post('/api/v1/procurement/purchase-orders', { supplierId: supplier.id, warehouseId: wh.id, submit: true,
    lines: [{ requisitionLineId: pr.lines[0].id, unitPrice: '12500' }] });
  if (!['approved', 'sent'].includes(po.status)) po = await approver.post(`/api/v1/procurement/purchase-orders/${po.id}:approve`, {});
  await manager.context().close();
  expect(['approved', 'sent']).toContain(po.status);

  // Goods receipt on the Warehouse workstation (FR-GR-01, FR-OPS-P4-04): find the PO, accept 20 kg.
  await login(page, CASHIER, email('warehouse_staff'));
  await page.goto(`${CASHIER}/ops/warehouse/goods-receipt`);
  await page.getByRole('textbox', { name: 'Find purchase order' }).fill(po.number);
  await page.getByRole('cell', { name: po.number, exact: true }).click();
  await expect(page.getByText(`${po.number} · ${supplier.name}`)).toBeVisible();
  await page.getByLabel('Delivery note no. (surat jalan)').fill(`SJ-E2E-${stamp}`);
  await page.getByRole('spinbutton', { name: 'Accepted' }).fill('20');
  await page.getByRole('button', { name: 'Save goods receipt' }).click();
  await expect(page.getByText(/Goods Receipt \S+: /)).toBeVisible();
  await expect.poll(async () => {
    const bal = await api.get(`/api/v1/inventory/stock-balances?warehouseId=${wh.id}&itemId=${item.id}`);
    return Number(bal.items?.[0]?.quantity ?? 0);
  }, { timeout: 30_000 }).toBe(20);

  // Vendor invoice matched 3-way (FR-INV-P4-01, EP-15) and shown on the Vendor Invoices screen.
  const order = await api.get(`/api/v1/procurement/purchase-orders/${po.id}`);
  expect(order.status).toBe('received');
  const vi = await api.post('/api/v1/procurement/vendor-invoices', { supplierId: supplier.id, supplierInvoiceNo: `INV-E2E-${stamp}`, match: true,
    lines: [{ purchaseOrderLineId: order.lines[0].id, quantity: '20', unitPrice: '12500' }] });
  expect(vi.status).toBe('approved');
  await page.goto(`${DASHBOARD}/procurement/vendor-invoices`); // still signed in as procurement staff on the dashboard domain
  await expect(page.getByRole('heading', { name: 'Vendor Invoices', level: 1 })).toBeVisible();
  const row = page.getByRole('row', { name: new RegExp(`INV-E2E-${stamp}`) });
  await expect(row).toBeVisible();
  await expect(row.getByText(/approved|matched/i).first()).toBeVisible();
  await admin.context().close();
});

test('P4 §9.4 period close: opening stock posted, inventory = GL on the closing checklist, the period soft-closed', async ({ browser, page }) => {
  test.setTimeout(150_000);
  const admin = await (await browser.newContext()).newPage();
  await login(admin, DASHBOARD, SA, '/');
  const main = await apiOf(admin);
  // A property of its own: closing a period of it leaves the other tests' books open.
  const prop = await main.post('/api/v1/platform/properties', { code: `E2EC${stamp}`, name: `E2E Closing ${stamp}`, timezone: 'Asia/Jakarta' });
  const api = await apiAt(admin, prop.id);
  const month = new Date().toISOString().slice(0, 8);
  await api.post('/api/v1/accounting/book:load-template', { cutOverDate: `${month}01` });
  const kg = await api.post('/api/v1/inventory/uoms', { code: `E2E-CKG-${stamp}`, name: 'Kilogram', kind: 'mass' });
  await api.post('/api/v1/inventory/items', { code: `E2E-RICE-${stamp}`, name: `E2E Rice ${stamp}`, baseUomId: kg.id });
  await api.post('/api/v1/inventory/warehouses', { code: `E2E-CS-${stamp}`, name: `E2E Store ${stamp}`, locationType: 'store' });
  const os = await api.post('/api/v1/inventory/opening-stock:import', { mode: 'commit', businessDate: day(0),
    csv: `warehouseCode,itemCode,quantity,unitCost\nE2E-CS-${stamp},E2E-RICE-${stamp},40,15000\n` });
  expect(os.status).toBe('completed');
  // The worker posts the opening stock: Dr inventory 600,000 / Cr opening balance equity.
  await expect.poll(async () => {
    const tb = await api.get(`/api/v1/accounting/trial-balance?from=${month}01&to=${day(0)}&propertyId=${prop.id}`);
    return JSON.stringify(tb).includes('600000');
  }, { timeout: 30_000 }).toBe(true);

  // Finance (Super Admin at the new property) reviews the checklist and soft-closes the month.
  await login(page, DASHBOARD, SA, '/accounting/periods');
  await page.getByRole('combobox', { name: 'Property' }).selectOption({ label: prop.name });
  await page.goto(`${DASHBOARD}/accounting/periods`);
  await expect(page.getByRole('heading', { name: 'Financial Periods', level: 1 })).toBeVisible();
  await page.getByRole('cell', { name: month.slice(0, 7), exact: true }).click();
  const drawer = page.getByRole('dialog');
  const inv = drawer.getByRole('row', { name: /Stock valuation = GL inventory/ });
  await expect(inv).toBeVisible();
  await expect(inv.getByText('OK', { exact: true })).toBeVisible();
  await drawer.getByRole('button', { name: 'Soft close' }).click();
  await expect(drawer.getByText(/soft.closed/i).first()).toBeVisible();
  const periods = await api.get(`/api/v1/accounting/periods?year=${month.slice(0, 4)}`);
  expect(periods.items.find((p: { month: number }) => p.month === Number(month.slice(5, 7))).status).toBe('soft_closed');

  // Closing → Reconciliations: the control accounts agree with their sub-ledgers.
  await page.goto(`${DASHBOARD}/accounting/closing?tab=reconciliation`);
  await expect(page.getByText('Control accounts = sub-ledgers', { exact: false })).toBeVisible();
  await admin.context().close();
});
