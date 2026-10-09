import { describe, expect, it } from 'vitest';
import { formatMoneyValue, parseMoney } from './MoneyInput';

describe('money field', () => {
  it('formats rupiah with a thousands dot', () => {
    expect(formatMoneyValue('500000')).toBe('500.000');
    expect(formatMoneyValue('1234567')).toBe('1.234.567');
    expect(formatMoneyValue('0')).toBe('0');
    expect(formatMoneyValue('')).toBe('');
    expect(formatMoneyValue('-25000', { allowNegative: true })).toBe('-25.000');
    expect(formatMoneyValue('1234.5', { decimals: 2 })).toBe('1.234,5');
  });

  it('reads the plain value back from what was typed', () => {
    expect(parseMoney('Rp 500.0001')).toBe('5000001');
    expect(parseMoney('Rp 5')).toBe('5');
    expect(parseMoney('Rp ')).toBe('');
    expect(parseMoney('abc')).toBe('');
    expect(parseMoney('Rp 007')).toBe('7');
    expect(parseMoney('Rp -25.000', { allowNegative: true })).toBe('-25000');
    expect(parseMoney('-25.000')).toBe('25000');
    expect(parseMoney('Rp 1.234,56', { decimals: 2 })).toBe('1234.56');
    expect(parseMoney('Rp 1.234,', { decimals: 2 })).toBe('1234.');
  });
});
