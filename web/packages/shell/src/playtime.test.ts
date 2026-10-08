import { describe, expect, it } from 'vitest';
import { formatPlayTime, playMinutes } from './components/playtime';

describe('play time', () => {
  const tee = '2026-10-09T07:00:00Z';
  it('counts from the tee-off until now while playing', () => {
    expect(playMinutes(tee, null, Date.parse('2026-10-09T08:05:30Z'))).toBe(65);
  });
  it('stops at the round finish', () => {
    expect(playMinutes(tee, '2026-10-09T11:12:00Z', Date.parse('2026-10-09T15:00:00Z'))).toBe(252);
  });
  it('is empty before the tee-off', () => {
    expect(playMinutes(null)).toBeNull();
  });
  it('reads as hours and minutes', () => {
    expect(formatPlayTime(42)).toBe('42m');
    expect(formatPlayTime(65)).toBe('1h 05m');
    expect(formatPlayTime(252)).toBe('4h 12m');
  });
});
