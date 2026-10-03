import { describe, expect, it } from 'vitest';
import { en, id, formatMoney, formatNumber } from './index';

function keys(o: object, prefix = ''): string[] {
  return Object.entries(o).flatMap(([k, v]) => (typeof v === 'object' ? keys(v, prefix + k + '.') : [prefix + k]));
}

describe('i18n', () => {
  it('has the same keys in English and Indonesian', () => {
    expect(keys(id).sort()).toEqual(keys(en).sort());
  });
  it('formats IDR without decimals per locale', () => {
    expect(formatMoney('150000', 'IDR', 'id').replace(/\s/g, ' ')).toMatch(/Rp.?150\.000/);
    expect(formatMoney(150000, 'IDR', 'en')).toContain('150,000');
    expect(formatNumber(1234567, 'id')).toBe('1.234.567');
  });
});
