import { expect, test, type Page } from '@playwright/test';
import { WEB } from './helpers';

/**
 * Website header from the CMS (PRD P4 FR-CMS-09, P3 FR-WEB-P3-01) and the
 * SEO files (FR-CMS-08). The header follows whatever the club published as
 * its header menu (the demo seeds a grouped proposal); dropdowns are
 * disclosure buttons that work with keyboard, pointer and touch.
 */

interface NavItem { label: string; href: string; children: NavItem[] }

/** The published header menu of the website (same API the website renders). */
async function headerMenu(page: Page, lang: string): Promise<NavItem[]> {
  const r = await page.request.get(`${WEB}/api/v1/public/cms/navigation?lang=${lang}&location=header`);
  expect(r.ok()).toBeTruthy();
  const menus = ((await r.json()) as { menus: { code: string; items: NavItem[] }[] }).menus.filter((m) => m.items.length > 0);
  return (menus.find((m) => m.code.toUpperCase() === 'HEADER') ?? menus[0])?.items ?? [];
}

/** Opens a website page and waits until the header responds (hydrated). */
async function open(page: Page, path: string) {
  await page.goto(`${WEB}${path}`);
  await expect(page.getByRole('navigation', { name: 'Main' })).toHaveAttribute('data-ready', 'true');
}

async function firstGroup(page: Page, lang: string) {
  const items = await headerMenu(page, lang);
  const group = items.find((i) => i.children.length > 0);
  expect(group, 'the published header menu has a dropdown (demo: grouped proposal)').toBeTruthy();
  return { items, group: group! };
}

test('Website: the header shows the CMS menu with keyboard dropdowns', async ({ page }) => {
  const { items, group } = await firstGroup(page, 'en');
  await open(page, '/en');
  const nav = page.getByRole('navigation', { name: 'Main' });
  // Top-level entries in the CMS order: buttons for dropdowns, links otherwise.
  await expect(nav.locator('.w-nav-list > li > a, .w-nav-list > li > button')).toHaveText(items.map((i) => i.label));
  await expect(nav.getByRole('link', { name: 'Book Golf' })).toBeVisible();

  const button = nav.getByRole('button', { name: group.label, exact: true });
  const child = group.children[group.children.length - 1];
  await expect(button).toHaveAttribute('aria-expanded', 'false');
  await expect(nav.getByRole('link', { name: child.label, exact: true })).toHaveCount(0); // hidden while closed

  // Keyboard: Enter opens, Tab reaches the entries, Escape closes and returns focus.
  await button.focus();
  await page.keyboard.press('Enter');
  await expect(button).toHaveAttribute('aria-expanded', 'true');
  const sub = page.locator(`#${await button.getAttribute('aria-controls')}`);
  await expect(sub.getByRole('link')).toHaveText(group.children.map((c) => c.label));
  await page.keyboard.press('Tab');
  await expect(sub.getByRole('link').first()).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(button).toHaveAttribute('aria-expanded', 'false');
  await expect(button).toBeFocused();

  // Pointer: a click outside closes; choosing an entry opens the page and marks it current.
  await button.click();
  await page.getByRole('heading', { level: 1 }).click();
  await expect(button).toHaveAttribute('aria-expanded', 'false');
  await button.click();
  await sub.getByRole('link', { name: child.label, exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`${child.href.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`));
  await expect(nav).toHaveAttribute('data-ready', 'true');
  await expect(nav.getByRole('button', { name: group.label, exact: true })).toHaveAttribute('data-current', 'true');
  await nav.getByRole('button', { name: group.label, exact: true }).click();
  await expect(nav.getByRole('link', { name: child.label, exact: true })).toHaveAttribute('aria-current', 'page');

  // Indonesian labels come from the same menu.
  const id = await firstGroup(page, 'id');
  await open(page, '/id');
  await expect(page.getByRole('navigation', { name: 'Main' }).getByRole('button', { name: id.group.label, exact: true })).toBeVisible();
});

test('Website: VIP Suite and Meeting & MICE are reachable from the header (demo menu)', async ({ page }) => {
  const items = await headerMenu(page, 'en');
  const all = items.flatMap((i) => [i, ...i.children]);
  for (const [label, href] of [['VIP Suite', '/en/vip-suite'], ['Meeting & MICE', '/en/meeting']]) {
    const it = all.find((x) => x.href === href);
    expect(it, `${label} in the header menu`).toBeTruthy();
    await open(page, '/en');
    const nav = page.getByRole('navigation', { name: 'Main' });
    const parent = items.find((i) => i.children.some((c) => c.href === href));
    if (parent) await nav.getByRole('button', { name: parent.label, exact: true }).click();
    await nav.getByRole('link', { name: it!.label, exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`${href}$`));
  }
});

test('Website: on a phone the header folds into an expandable list', async ({ page }) => {
  const { group } = await firstGroup(page, 'en');
  await page.setViewportSize({ width: 390, height: 844 });
  await open(page, '/en');
  const nav = page.getByRole('navigation', { name: 'Main' });
  const menu = nav.getByRole('button', { name: 'Menu' });
  await expect(menu).toHaveAttribute('aria-expanded', 'false');
  await expect(nav.getByRole('link', { name: 'Book Golf' })).toBeVisible();
  const button = nav.getByRole('button', { name: group.label, exact: true });
  await expect(button).toBeHidden();
  await menu.tap().catch(() => menu.click()); // touch where the context supports it
  await expect(menu).toHaveAttribute('aria-expanded', 'true');
  await button.click();
  const first = nav.getByRole('link', { name: group.children[0].label, exact: true });
  await expect(first).toBeVisible();
  // The entries expand in place below their button (no floating panel off-screen).
  const [b, l] = [await button.boundingBox(), await first.boundingBox()];
  expect(l!.y).toBeGreaterThan(b!.y);
  expect(l!.x + l!.width).toBeLessThanOrEqual(390);
  await page.keyboard.press('Escape');
  await expect(button).toHaveAttribute('aria-expanded', 'false');
  await page.keyboard.press('Escape');
  await expect(menu).toHaveAttribute('aria-expanded', 'false');
  await expect(menu).toBeFocused();
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
