import { expect, test, type Page } from '@playwright/test';
import { WEB } from './helpers';

/**
 * Website header (demo: the live site www.moderngolf.co.id, product owner
 * 7 Oct 2026) and the SEO files (FR-CMS-08). The header keeps the club's
 * main menu; the OneClub pages (online booking, membership, tickets…) sit in
 * its sub-menus, which open on hover and fold behind the burger on a phone.
 */

const MENU = ['ABOUT US', "WHAT'S ON", 'GOLF COURSE', 'MICE & WEDDING', 'SPORT CLUB', 'BUNGALOW', 'VIP SUITE', 'CONTACT US'];

/** A top-level entry of the header menu. */
const entry = (page: Page, label: string) => page.locator('header .main-menu > li').filter({ has: page.locator(':scope > a', { hasText: label }) });

test('Website: the header shows the club menu with the OneClub pages in its sub-menus', async ({ page }) => {
  await page.goto(`${WEB}/en`);
  await expect(page.locator('header .main-menu > li > a')).toHaveText(MENU);
  const golf = entry(page, 'GOLF COURSE');
  const book = golf.locator('.sub-menu').getByRole('link', { name: 'Book Tee Time' });
  await expect(golf.locator('.sub-menu')).toHaveCSS('opacity', '0'); // closed until hovered
  await golf.hover();
  await expect(golf.locator('.sub-menu')).toHaveCSS('opacity', '1');
  await book.click();
  await expect(page).toHaveURL(/\/en\/book-golf$/);
  await expect(page.getByRole('heading', { name: 'Book Tee Time' }).first()).toBeVisible();

  // Indonesian labels on the same menu.
  await page.goto(`${WEB}/id`);
  await expect(entry(page, 'MICE & WEDDING').locator('.sub-menu').getByRole('link', { name: 'Pesan Meeting Room' })).toHaveAttribute('href', '/id/book/meeting-room');
});

test('Website: VIP Suite and Meeting Room booking are reachable from the header', async ({ page }) => {
  for (const [parent, label, href] of [['VIP SUITE', 'Book VIP Suite', '/en/book/vip-suite'], ['MICE & WEDDING', 'Book Meeting Room', '/en/book/meeting-room']]) {
    await page.goto(`${WEB}/en`);
    await entry(page, parent).hover();
    await entry(page, parent).locator('.sub-menu').getByRole('link', { name: label }).click();
    await expect(page).toHaveURL(new RegExp(`${href}$`));
    await expect(page.getByRole('heading', { name: label }).first()).toBeVisible();
  }
});

test('Website: on a phone the header folds behind the burger', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(`${WEB}/en`);
  const golf = page.locator('header .main-menu > li > a', { hasText: 'GOLF COURSE' });
  const left = async () => (await golf.boundingBox())!.x;
  expect(await left()).toBeGreaterThanOrEqual(390); // the menu waits off-screen
  // the theme script binds the burger once it has loaded
  await expect(async () => {
    await page.locator('header .burger').click();
    await expect(page.locator('header')).toHaveClass(/active/, { timeout: 1_000 });
  }).toPass();
  await expect.poll(left).toBeLessThan(195); // slides in over the page
  await expect(golf).toBeInViewport();
  await golf.click();
  await expect(page).toHaveURL(/\/en\/golf-course$/);
});

test('Website: sitemap.xml and robots.txt', async ({ request }) => {
  const sm = await request.get(`${WEB}/sitemap.xml`);
  expect(sm.status()).toBe(200);
  expect(sm.headers()['content-type']).toContain('xml');
  const xml = await sm.text();
  expect(xml).toContain('<urlset');
  for (const path of ['/id', '/en/golf', '/id/golf', '/en/tournaments', '/en/vip-suite', '/en/meeting']) expect(xml).toContain(`${path}</loc>`);
  expect(xml).toMatch(/hreflang="id"/);
  expect(xml).not.toContain('/payment/');

  const rb = await request.get(`${WEB}/robots.txt`);
  expect(rb.status()).toBe(200);
  const txt = await rb.text();
  expect(txt).toMatch(/^User-Agent: \*/m);
  expect(txt).toMatch(/^Sitemap: https?:\/\/\S+\/sitemap\.xml$/m);
  if (!/^Disallow: \/$/m.test(txt)) {
    for (const p of ['/api/', '/id/payment/', '/en/payment/', '/en/quotation/', '/id/supplier/', '/en/unsubscribe/']) expect(txt).toContain(`Disallow: ${p}`);
  }
});
