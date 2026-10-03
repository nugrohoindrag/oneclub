/**
 * Offline storage and sync queue for operational surfaces (Technical Doc
 * §6.4, PRD FR-SH-05):
 *
 *   user action (offline) → IndexedDB queue (UUIDv7 = idempotency key)
 *   → UI updates optimistically
 *   connection returns → queue sent in order to POST /api/v1/platform/sync
 *   → server processes idempotently → client reconciles results/conflicts
 *
 * Cached data is scoped per property and shift and cleared on logout.
 */
import Dexie, { type Table } from 'dexie';
import { useEffect, useState, useSyncExternalStore } from 'react';
import { request, uuidv7 } from '@oneclub/api-client';

export type QueueStatus = 'queued' | 'sending' | 'accepted' | 'duplicate' | 'rejected' | 'conflict' | 'failed';

export interface QueueItem {
  id: string; // UUIDv7
  action: string;
  payload: unknown;
  propertyId: string;
  createdAt: string;
  status: QueueStatus;
  attempts: number;
  error?: string;
  result?: unknown;
}

export interface CacheEntry {
  key: string; // `${propertyId}:${name}`
  value: unknown;
  updatedAt: string;
}

class OfflineDB extends Dexie {
  queue!: Table<QueueItem, string>;
  cache!: Table<CacheEntry, string>;
  constructor(name = 'oneclub-offline') {
    super(name);
    this.version(1).stores({ queue: 'id, status, createdAt, propertyId', cache: 'key, updatedAt' });
  }
}

export const db = new OfflineDB();

type Listener = () => void;
const listeners = new Set<Listener>();
const notify = () => listeners.forEach((l) => l());

/** Adds an action to the queue (works offline). */
export async function enqueue(action: string, payload: unknown, propertyId: string): Promise<QueueItem> {
  const item: QueueItem = { id: uuidv7(), action, payload, propertyId, createdAt: new Date().toISOString(), status: 'queued', attempts: 0 };
  await db.queue.add(item);
  notify();
  if (isOnline()) void flush();
  return item;
}

interface SyncResult {
  id: string;
  status: 'accepted' | 'duplicate' | 'rejected' | 'conflict';
  result?: unknown;
  error?: string;
}

let flushing: Promise<void> | null = null;

/**
 * Sends queued items in creation order. Network failures leave items queued
 * (retried on the next flush); the server answers per item.
 */
export function flush(): Promise<void> {
  if (flushing) return flushing;
  flushing = (async () => {
    try {
      const pending = await db.queue.where('status').anyOf('queued', 'failed').sortBy('createdAt');
      if (pending.length === 0) return;
      for (let i = 0; i < pending.length; i += 50) {
        const batch = pending.slice(i, i + 50);
        await db.queue.bulkUpdate(batch.map((b) => ({ key: b.id, changes: { status: 'sending' as QueueStatus, attempts: b.attempts + 1 } })));
        notify();
        try {
          const res = await request<{ results: SyncResult[] }>('POST', '/api/v1/platform/sync', {
            items: batch.map((b) => ({ id: b.id, action: b.action, payload: b.payload, clientTime: b.createdAt })),
          }, batch[0]?.propertyId ? { 'X-Property-Id': batch[0].propertyId } : undefined);
          for (const r of res.results) {
            await db.queue.update(r.id, { status: r.status, result: r.result, error: r.error });
          }
        } catch (e) {
          await db.queue.bulkUpdate(batch.map((b) => ({ key: b.id, changes: { status: 'failed' as QueueStatus, error: String((e as Error).message ?? e) } })));
          break;
        } finally {
          notify();
        }
      }
    } finally {
      flushing = null;
    }
  })();
  return flushing;
}

/** Removes processed items older than a day. */
export async function prune() {
  const cutoff = new Date(Date.now() - 86400_000).toISOString();
  await db.queue.where('createdAt').below(cutoff).and((i) => i.status === 'accepted' || i.status === 'duplicate').delete();
  notify();
}

/** Clears all offline data (logout, Technical Doc §6.4). */
export async function clearAll() {
  await db.queue.clear();
  await db.cache.clear();
  notify();
}

// ── cache (read-only master data, refreshed periodically) ─────────────────

export async function cachePut(propertyId: string, name: string, value: unknown) {
  await db.cache.put({ key: `${propertyId}:${name}`, value, updatedAt: new Date().toISOString() });
}

export async function cacheGet<T>(propertyId: string, name: string): Promise<T | undefined> {
  return (await db.cache.get(`${propertyId}:${name}`))?.value as T | undefined;
}

// ── connectivity ──────────────────────────────────────────────────────────

let forcedOffline = false;
/** Simulates offline mode (used by the offline acceptance test and demo). */
export function setForcedOffline(v: boolean) {
  forcedOffline = v;
  notify();
  if (!v) void flush();
}

export function isOnline() {
  return !forcedOffline && (typeof navigator === 'undefined' || navigator.onLine);
}

if (typeof window !== 'undefined') {
  window.addEventListener('online', () => {
    notify();
    void flush();
  });
  window.addEventListener('offline', notify);
}

function subscribe(l: Listener) {
  listeners.add(l);
  return () => listeners.delete(l);
}

/** React hook: online state. */
export function useOnline() {
  return useSyncExternalStore(subscribe, isOnline, () => true);
}

/** React hook: queue contents (newest first). */
export function useQueue() {
  const [items, setItems] = useState<QueueItem[]>([]);
  useEffect(() => {
    let alive = true;
    const load = () => db.queue.orderBy('createdAt').reverse().toArray().then((x) => alive && setItems(x));
    load();
    const un = subscribe(load);
    return () => {
      alive = false;
      un();
    };
  }, []);
  return items;
}

// ── service worker ────────────────────────────────────────────────────────

/** Registers the app's service worker (generated by vite-plugin-pwa). */
export async function registerServiceWorker(url = '/sw.js') {
  if (!('serviceWorker' in navigator)) return undefined;
  try {
    return await navigator.serviceWorker.register(url, { type: 'module' });
  } catch {
    return navigator.serviceWorker.register(url).catch(() => undefined);
  }
}
