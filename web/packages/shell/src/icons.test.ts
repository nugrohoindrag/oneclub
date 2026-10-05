import { describe, expect, it } from 'vitest';
import subset from './assets/material-symbols-rounded.icons.json';

// Decision 4g: the apps ship a subset of Material Symbols Rounded
// (assets/material-symbols-rounded.subset.woff2) so icons render offline. An
// icon name missing from the subset renders as its text ("home"), so every
// name the code gives an icon must be in the subset. The patterns mirror
// WEB_ICON_CONTEXTS in scripts/fonts/subset-icons.py; Go icons (navigation,
// ESS sections) are checked by internal/app/icons_test.go.

const sources = import.meta.glob(
  [
    '../../../apps/staff/src/**/*.{ts,tsx}',
    '../../../apps/member/src/**/*.{ts,tsx}',
    '../../*/src/**/*.{ts,tsx}',
    '!../../api-client/src/schema.ts',
    '!**/*.test.{ts,tsx}',
    '!**/*.spec.{ts,tsx}',
  ],
  { query: '?raw', import: 'default', eager: true },
) as Record<string, string>;

const LITERAL = /(['"`])([a-z0-9][a-z0-9_]*)\1/g;
const COMPARISON = /[!=]==?\s*(['"`])[^'"`]*\1|(['"`])[^'"`]*\2\s*[!=]==?/g;
const CONTEXTS: [RegExp, boolean][] = [
  [/<Icon\b[^>]*?\bname=(?:"([^"]*)"|'([^']*)')/g, false],
  [/<Icon\b[^>]*?\bname=\{([^{}]*)\}/g, true],
  [/\b(?:icon|[A-Za-z_]\w*Icon)\s*[:=]\s*\{?\s*(?:"([^"]*)"|'([^']*)')/g, false],
];

/** Icon names the code gives icons, with their file:line. */
function iconNames(file: string, text: string): [string, string][] {
  const out: [string, string][] = [];
  for (const [rx, expression] of CONTEXTS) {
    for (const m of text.matchAll(rx)) {
      const value = m.slice(1).find((g) => g !== undefined) ?? '';
      const at = `${file.replace(/^(\.\.\/)+/, 'web/')}:${text.slice(0, m.index).split('\n').length}`;
      const names = expression ? [...value.replace(COMPARISON, '').matchAll(LITERAL)].map((x) => x[2]) : [value];
      for (const n of names) if (n && !n.startsWith('/')) out.push([n, at]);
    }
  }
  return out;
}

describe('self-hosted Material Symbols subset', () => {
  const included = new Set(subset.icons);

  it('scans the app sources', () => {
    expect(Object.keys(sources).length).toBeGreaterThan(50);
    expect(Object.keys(sources).some((f) => f.includes('apps/staff/src/'))).toBe(true);
    expect(Object.keys(sources).some((f) => f.includes('apps/member/src/'))).toBe(true);
  });

  it('includes every icon name the code uses', () => {
    const missing = Object.entries(sources)
      .flatMap(([file, text]) => iconNames(file, text))
      .filter(([name]) => !included.has(name))
      .map(([name, at]) => `${at}: '${name}'`);
    expect(missing, 'icons missing from the subset font: run `python3 scripts/fonts/subset-icons.py` (pip install fonttools brotli) and commit the result').toEqual([]);
  });

  it('detects icon names in the shapes the code uses', () => {
    const src = `
      <Icon name="home" size={18} />
      <Icon size={14} name={open ? 'expand_less' : 'expand_more'} />
      <Icon name={order === 'asc' ? 'arrow_upward' : 'arrow_downward'} />
      const tiles = [{ icon: 'golf_course', to: '/golf' }];
      <Card title="Tier" icon="workspace_premium" />
      const trendIcon = 'trending_up';
      <Icon name={item.icon} />`;
    expect(iconNames('x.tsx', src).map(([n]) => n)).toEqual([
      'home', 'expand_less', 'expand_more', 'arrow_upward', 'arrow_downward', 'golf_course', 'workspace_premium', 'trending_up',
    ]);
  });
});
