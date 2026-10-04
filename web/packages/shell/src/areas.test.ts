import { afterEach, describe, expect, it, vi } from 'vitest';
import { areasOf, currentSurface, landingPath, loadSurface, onSurface, area, surfaceUrl } from './areas';
import type { Me } from './context';

const user = (...shells: string[]) => ({ shells }) as unknown as Me;
const superAdmin = user('backoffice', 'management', 'ops', 'member', 'caddy', 'kitchen', 'screen');

async function on(surface: string | null) {
  vi.stubGlobal('fetch', async () =>
    surface
      ? new Response(JSON.stringify({ surface, domains: { dashboard: 'https://dashboard.club.test', cashier: 'https://cashier.club.test' } }))
      : new Response('not found', { status: 404 }),
  );
  await loadSurface();
}

afterEach(() => vi.unstubAllGlobals());

describe('Staff App surfaces (Technical Doc §6.1)', () => {
  it('without /surface.json every area opens by path, office areas first', async () => {
    await on(null);
    expect(currentSurface()).toBeNull();
    expect(landingPath(superAdmin)).toBe('/management');
    expect(landingPath(user('ops', 'caddy'))).toBe('/tablet');
    expect(landingPath(user('ops', 'kitchen'))).toBe('/kitchen');
    expect(landingPath(user('ops'))).toBe('/ops');
    expect(landingPath(user('screen'))).toBe('/screen');
  });

  it('dashboard never lands on a device area', async () => {
    await on('dashboard');
    expect(landingPath(superAdmin)).toBe('/management');
    expect(landingPath(user('backoffice', 'ops'))).toBe('/');
    expect(landingPath(user('screen'))).toBe('/screen');
    expect(landingPath(user('ops', 'caddy'))).toBeNull();
    expect(areasOf(superAdmin).map((a) => a.code)).toEqual(['management', 'backoffice', 'screen']);
  });

  it('a device domain opens only its area and ignores `next` elsewhere', async () => {
    await on('cashier');
    expect(landingPath(superAdmin)).toBe('/ops');
    expect(landingPath(superAdmin, '/golf/tee-sheet')).toBe('/ops');
    expect(landingPath(superAdmin, '/ops/pos')).toBe('/ops/pos');
    expect(areasOf(superAdmin).map((a) => a.code)).toEqual(['ops']);
    expect(onSurface(area('backoffice'))).toBe(false);
    expect(surfaceUrl('dashboard', '/golf/tee-sheet')).toBe('https://dashboard.club.test/golf/tee-sheet');
    expect(surfaceUrl('kitchen', '/kitchen')).toBeNull();
    await on('caddy');
    expect(landingPath(superAdmin)).toBe('/tablet');
    await on('kitchen');
    expect(landingPath(user('ops'))).toBeNull(); // a cashier has no Kitchen Display
  });

  it('keeps the last surface when the device is offline', async () => {
    const store = new Map<string, string>();
    vi.stubGlobal('localStorage', {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, v),
      removeItem: (k: string) => void store.delete(k),
    });
    await on('caddy');
    vi.stubGlobal('fetch', async () => {
      throw new TypeError('Failed to fetch');
    });
    await loadSurface();
    expect(currentSurface()).toBe('caddy');
  });
});
