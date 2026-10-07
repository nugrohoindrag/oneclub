import { useEffect, useState } from 'react';
import { useGet } from '@oneclub/api-client';
import { cacheGet, cachePut, useOnline, useQueue } from '@oneclub/offline';
import { useAuth } from '@oneclub/shell';

/*
 * POS offline (FR-POS-11, Technical Doc §6.4): the screens keep working on
 * the last data they saw (snapshots in the device's IndexedDB cache) and
 * every sale action goes to the sync queue (commercial.pos_order). Until
 * the queue is synced, the actions are kept as local orders so the Table
 * View, the Current Order and Payment Confirm show them.
 */

/** A server GET that falls back to its last snapshot on this device when offline. */
export function useCachedGet<T>(path: string | null, name: string, opts?: { refetchInterval?: number; staleTime?: number }) {
  const { propertyId } = useAuth();
  const online = useOnline();
  const q = useGet<T>(online ? path : null, opts);
  const [cached, setCached] = useState<T | undefined>(undefined);
  const key = `pos:${name}`;
  useEffect(() => {
    if (path && q.data !== undefined) void cachePut(propertyId, key, q.data);
  }, [q.data, path, propertyId, key]);
  useEffect(() => {
    if (!path || q.data !== undefined) return;
    let live = true;
    void cacheGet<T>(propertyId, key).then((v) => { if (live) setCached(v); });
    return () => { live = false; };
  }, [q.data, path, propertyId, key]);
  return { ...q, data: q.data ?? cached, isLoading: q.isLoading && cached === undefined, fromCache: q.data === undefined && cached !== undefined };
}

export type LocalLine = { productId: string; name: string; quantity: number; unitPrice: number; notes?: string };

/**
 * An order changed offline: a new order (server = false, it gets its client
 * id) or an order of the server with items added, tables moved or paid.
 */
export type LocalOrder = {
  id: string;
  server: boolean;
  orderNo: string;
  tableIds?: string[];
  tableCodes?: string[];
  guestCount?: number;
  customerId?: string;
  customerName?: string;
  lines: LocalLine[];
  createdAt: string;
  paid?: string;
};

const LOCAL = (outlet: string) => `pos:local:${outlet}`;
const EVENT = 'oneclub-pos-local';

export async function loadLocal(propertyId: string, outlet: string): Promise<LocalOrder[]> {
  return (await cacheGet<LocalOrder[]>(propertyId, LOCAL(outlet))) ?? [];
}

/** Adds or merges an offline change of an order. */
export async function saveLocal(propertyId: string, outlet: string, change: LocalOrder) {
  const all = await loadLocal(propertyId, outlet);
  const i = all.findIndex((o) => o.id === change.id);
  if (i < 0) all.push(change);
  else {
    const defined = Object.fromEntries(Object.entries(change).filter(([, v]) => v !== undefined)) as Partial<LocalOrder>;
    all[i] = { ...all[i], ...defined, lines: [...all[i].lines, ...change.lines] };
  }
  await cachePut(propertyId, LOCAL(outlet), all);
  window.dispatchEvent(new Event(EVENT));
}

export async function clearLocal(propertyId: string, outlet: string) {
  await cachePut(propertyId, LOCAL(outlet), []);
  window.dispatchEvent(new Event(EVENT));
}

/** The offline changes of the outlet, live. */
export function useLocalOrders(outlet: string) {
  const { propertyId } = useAuth();
  const [orders, setOrders] = useState<LocalOrder[]>([]);
  useEffect(() => {
    let live = true;
    const load = () => void loadLocal(propertyId, outlet).then((x) => { if (live) setOrders(x); });
    load();
    window.addEventListener(EVENT, load);
    return () => { live = false; window.removeEventListener(EVENT, load); };
  }, [propertyId, outlet]);
  return orders;
}

/** POS actions still waiting in the sync queue. */
export function usePendingSales() {
  return useQueue().filter((q) => q.action === 'commercial.pos_order' && ['queued', 'sending', 'failed'].includes(q.status)).length;
}

/** Total / net of the outlet's tax & service, remembered from the last online quote (offline estimates). */
const FACTOR = (outlet: string) => `oneclub.pos.taxFactor.${outlet}`;
export function rememberTaxFactor(outlet: string, factor: number) {
  try { localStorage.setItem(FACTOR(outlet), String(factor)); } catch { /* storage unavailable */ }
}
export function taxFactor(outlet: string) {
  try { return Number(localStorage.getItem(FACTOR(outlet))) || 1; } catch { return 1; }
}

/** Estimated total of offline lines. */
export const localTotal = (outlet: string, lines: LocalLine[]) => Math.round(lines.reduce((s, l) => s + l.unitPrice * l.quantity, 0) * taxFactor(outlet));
