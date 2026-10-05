/**
 * Header navigation model of the website (PRD P4 FR-CMS-09, P3 FR-WEB-P3-01).
 * The header shows the CMS header menu (/api/v1/public/cms/navigation) with
 * one dropdown level; without a published menu it falls back to the built-in
 * Naming Convention §26 list. Shared by the server layout and the client nav,
 * so it imports types only.
 */
import type { CmsNavItem } from '../../components/cms/api';

export interface NavLink {
  label: string;
  href: string;
  external?: boolean;
  newTab?: boolean;
}

/** A header entry: a link, or a dropdown when it has children. */
export interface NavEntry extends NavLink {
  children: NavLink[];
}

const link = (i: CmsNavItem): NavLink => ({ label: i.label, href: i.href, external: i.external, newTab: i.newTab });

/**
 * Converts the CMS menu items into header entries: deeper levels are listed
 * in the dropdown of their top-level entry (the website shows one level),
 * and the link of a dropdown parent stays reachable as its first entry
 * unless one of the children already points there.
 */
export function navFromCms(items: CmsNavItem[] | undefined | null): NavEntry[] {
  const out: NavEntry[] = [];
  for (const it of items ?? []) {
    if (!it.label) continue;
    const children: NavLink[] = [];
    const add = (c: CmsNavItem) => {
      if (c.label && c.href && !children.some((x) => x.href === c.href && x.label === c.label)) children.push(link(c));
      (c.children ?? []).forEach(add);
    };
    (it.children ?? []).forEach(add);
    if (children.length > 0 && it.href && !children.some((c) => c.href === it.href)) children.unshift(link(it));
    if (!it.href && children.length === 0) continue;
    out.push({ ...link(it), children });
  }
  return out;
}

/** The CMS header menu to show: the HEADER menu, else the first header menu with items. */
export function pickHeaderMenu(menus: { code: string; items: CmsNavItem[] }[]): CmsNavItem[] | null {
  const withItems = menus.filter((m) => (m.items ?? []).length > 0);
  return (withItems.find((m) => m.code.toUpperCase() === 'HEADER') ?? withItems[0])?.items ?? null;
}

/** Whether a link is the current page (or a page below it). */
export function isCurrent(path: string, l: NavLink, lang: string): boolean {
  if (l.external || !l.href.startsWith('/')) return false;
  const href = l.href.split(/[?#]/)[0].replace(/\/$/, '') || '/';
  if (path === href) return true;
  return href !== `/${lang}` && href !== '/' && path.startsWith(`${href}/`);
}
