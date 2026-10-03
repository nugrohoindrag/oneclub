import 'fake-indexeddb/auto';
import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('@oneclub/api-client', async () => {
  const sent: unknown[] = [];
  let fail = false;
  return {
    uuidv7: () => crypto.randomUUID(),
    __sent: sent,
    __setFail: (v: boolean) => (fail = v),
    request: vi.fn(async (_m: string, _p: string, body: { items: { id: string }[] }) => {
      if (fail) throw new Error('network down');
      sent.push(...body.items);
      return { results: body.items.map((i) => ({ id: i.id, status: 'accepted', result: {} })) };
    }),
  };
});

import * as api from '@oneclub/api-client';
import { clearAll, db, enqueue, flush, setForcedOffline } from './index';

const mock = api as unknown as { __sent: { id: string }[]; __setFail: (v: boolean) => void };

describe('offline queue', () => {
  afterEach(async () => {
    await clearAll();
    mock.__sent.length = 0;
    mock.__setFail(false);
  });

  it('queues while offline and syncs in order when back online', async () => {
    setForcedOffline(true);
    const a = await enqueue('ops.shift_note', { text: 'one' }, 'p1');
    const b = await enqueue('ops.shift_note', { text: 'two' }, 'p1');
    expect(await db.queue.where('status').equals('queued').count()).toBe(2);
    expect(mock.__sent).toHaveLength(0);
    setForcedOffline(false);
    await flush();
    expect(mock.__sent.map((i) => i.id)).toEqual([a.id, b.id]);
    expect(await db.queue.where('status').equals('accepted').count()).toBe(2);
  });

  it('keeps items for retry when the network fails', async () => {
    setForcedOffline(true);
    await enqueue('ops.shift_note', { text: 'x' }, 'p1');
    mock.__setFail(true);
    setForcedOffline(false);
    await flush();
    expect(await db.queue.where('status').equals('failed').count()).toBe(1);
    mock.__setFail(false);
    await flush();
    expect(await db.queue.where('status').equals('accepted').count()).toBe(1);
  });
});
