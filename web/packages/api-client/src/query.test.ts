import { describe, expect, it } from 'vitest';
import { resolveSend } from './query';

describe('resolveSend', () => {
  it('keeps a static path and the whole body', () => {
    expect(resolveSend('/api/v1/x', { a: 1 })).toEqual({ url: '/api/v1/x', body: { a: 1 } });
  });

  it('drops the keys the path function reads from the body', () => {
    const r = resolveSend((v: { playerId: string; tee: string }) => `/api/v1/golf/bookings/b1/players/${v.playerId}`, { playerId: 'p1', tee: 'blue' });
    expect(r).toEqual({ url: '/api/v1/golf/bookings/b1/players/p1', body: { tee: 'blue' } });
  });

  it('drops every key used for the URL, leaving the rest', () => {
    const r = resolveSend((v: { fid: string; action: string; reason?: string }) => `/api/v1/golf/starter-queue/${v.fid}:${v.action}`,
      { fid: 'f1', action: 'hold', reason: 'slow' });
    expect(r).toEqual({ url: '/api/v1/golf/starter-queue/f1:hold', body: { reason: 'slow' } });
  });

  it('sends an empty object when only ids were given', () => {
    expect(resolveSend((v: { id: string }) => `/api/v1/golf/bag-drops/${v.id}:collect`, { id: 'b9' }).body).toEqual({});
  });

  it('keeps the body when the path function reads nothing from it', () => {
    const id = 'x';
    expect(resolveSend(() => `/api/v1/shifts/${id}:close`, { amount: 5 })).toEqual({ url: '/api/v1/shifts/x:close', body: { amount: 5 } });
  });
});
