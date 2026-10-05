import { describe, expect, it } from 'vitest';
import type { CmsNavItem } from '../../components/cms/api';
import { isCurrent, navFromCms, pickHeaderMenu } from './nav-model';
import { disallowed } from '../seo';

const item = (label: string, href: string, children: CmsNavItem[] = [], extra: Partial<CmsNavItem> = {}): CmsNavItem => ({
  id: label, label, href, external: false, newTab: false, children, ...extra,
});

describe('navFromCms (FR-CMS-09)', () => {
  it('keeps links and one dropdown level, listing deeper levels in the dropdown', () => {
    const nav = navFromCms([
      item('Membership', '/en/membership'),
      item('Golf', '/en/golf', [item('Golf', '/en/golf'), item('Tournaments', '/en/tournaments', [item('Leaderboard', '/en/tournaments/1/leaderboard')])]),
    ]);
    expect(nav.map((e) => e.label)).toEqual(['Membership', 'Golf']);
    expect(nav[0].children).toEqual([]);
    expect(nav[1].children.map((c) => c.href)).toEqual(['/en/golf', '/en/tournaments', '/en/tournaments/1/leaderboard']);
  });

  it('keeps the link of a dropdown parent reachable as its first entry', () => {
    const [e] = navFromCms([item('Facilities', '/en/sport-club', [item('VIP Suite', '/en/vip-suite')])]);
    expect(e.children.map((c) => `${c.label}=${c.href}`)).toEqual(['Facilities=/en/sport-club', 'VIP Suite=/en/vip-suite']);
  });

  it('drops entries without a label or a target and keeps external flags', () => {
    const nav = navFromCms([item('', '/en'), item('Empty', ''), item('PGI', 'https://pgi.or.id', [], { external: true, newTab: true })]);
    expect(nav).toEqual([{ label: 'PGI', href: 'https://pgi.or.id', external: true, newTab: true, children: [] }]);
    expect(navFromCms(null)).toEqual([]);
  });

  it('prefers the HEADER menu, else the first menu with items', () => {
    const a = [item('A', '/en')];
    const b = [item('B', '/en')];
    expect(pickHeaderMenu([{ code: 'AAA', items: a }, { code: 'HEADER', items: b }])).toBe(b);
    expect(pickHeaderMenu([{ code: 'HEADER', items: [] }, { code: 'X', items: a }])).toBe(a);
    expect(pickHeaderMenu([])).toBeNull();
  });
});

describe('isCurrent', () => {
  it('matches the page and the pages below it, never home prefixes or external links', () => {
    expect(isCurrent('/en/golf', { label: '', href: '/en/golf' }, 'en')).toBe(true);
    expect(isCurrent('/en/golf/course-guide', { label: '', href: '/en/golf' }, 'en')).toBe(true);
    expect(isCurrent('/en/golfers', { label: '', href: '/en/golf' }, 'en')).toBe(false);
    expect(isCurrent('/en/golf', { label: '', href: '/en' }, 'en')).toBe(false);
    expect(isCurrent('/en', { label: '', href: '/en' }, 'en')).toBe(true);
    expect(isCurrent('/en/x', { label: '', href: 'https://x.test/en/x', external: true }, 'en')).toBe(false);
  });
});

describe('robots disallow (FR-CMS-08)', () => {
  it('blocks the private pages in both languages and scopes Content Policies paths', () => {
    const d = disallowed(['/booking/', 'member/', '/en/old/', '/api/']);
    for (const p of ['/api/', '/id/payment/', '/en/payment/', '/id/quotation/', '/en/supplier/', '/id/unsubscribe/', '/booking/', '/id/member/', '/en/member/', '/en/old/'])
      expect(d).toContain(p);
    expect(d).not.toContain('/id/en/old/');
    expect(new Set(d).size).toBe(d.length);
  });
});
