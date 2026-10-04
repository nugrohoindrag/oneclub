import React, { createContext, useContext, useEffect } from 'react';
import { Navigate, useLocation } from 'react-router';
import { useAuth, type Me } from './context';
import { Skeleton } from './components/ui';
import { ForbiddenPage, MaintenancePage } from './pages/errors';
import { AREAS, SURFACES, type Area, type AreaCode, type Surface } from './area-list';

export { AREAS, SURFACES, type Area, type AreaCode, type Surface };

export function area(code: AreaCode): Area {
  return AREAS.find((a) => a.code === code)!;
}

// ── surface (Technical Doc §6.1) ──────────────────────────────────────────

/**
 * What the server says about this domain: `/surface.json`, served by Caddy
 * per domain (custom domains included), e.g.
 * `{"surface":"cashier","domains":{"dashboard":"https://dashboard.club.id",…}}`.
 */
export interface SurfaceInfo {
  surface: Surface;
  /** Origins of the instance's Staff App domains, for links to the right domain. */
  domains?: Partial<Record<Surface, string>>;
}

const SURFACE_KEY = 'oneclub.surface';
let info: SurfaceInfo | null = null;

function parseSurface(v: unknown): SurfaceInfo | null {
  const o = v as SurfaceInfo | null;
  return o && SURFACES.includes(o.surface) ? { surface: o.surface, domains: o.domains ?? {} } : null;
}

/**
 * Reads the surface before the Staff App renders. Without `/surface.json`
 * (development, tests) there is no surface and every area opens by path.
 * The last answer is kept per domain, so an offline device keeps its surface.
 */
export async function loadSurface(): Promise<SurfaceInfo | null> {
  try {
    const r = await fetch('/surface.json', { cache: 'no-cache', headers: { Accept: 'application/json' } });
    info = r.ok ? parseSurface(await r.json().catch(() => null)) : null;
    try {
      if (info) localStorage.setItem(SURFACE_KEY, JSON.stringify(info));
      else localStorage.removeItem(SURFACE_KEY);
    } catch {
      /* ignore */
    }
  } catch {
    // Offline: the surface this domain had last time.
    try {
      info = parseSurface(JSON.parse(localStorage.getItem(SURFACE_KEY) ?? 'null'));
    } catch {
      info = null;
    }
  }
  return info;
}

/** The surface of this domain; null when every area opens by path. */
export function currentSurface(): Surface | null {
  return info?.surface ?? null;
}

/** Whether an area opens on this domain. */
export function onSurface(a: Area): boolean {
  return !info || a.surface === info.surface;
}

/** URL of a path on the domain of another surface, when that domain is known. */
export function surfaceUrl(s: Surface, path: string): string | null {
  const origin = info?.domains?.[s];
  return origin ? origin.replace(/\/$/, '') + path : null;
}

// ── areas of a user ───────────────────────────────────────────────────────

/** Areas the user may open on this domain, in landing order. */
export function areasOf(me: Me | null): Area[] {
  return me ? AREAS.filter((a) => me.shells.includes(a.shell) && onSurface(a)) : [];
}

/** The area a path belongs to; everything outside the prefixed areas is Back Office. */
export function areaOfPath(pathname: string): Area {
  return AREAS.find((a) => a.path !== '/' && (pathname === a.path || pathname.startsWith(a.path + '/'))) ?? area('backoffice');
}

/** Path of a page inside an area, e.g. areaPath(ops, 'sync') → /ops/sync. */
export function areaPath(a: Area, sub = ''): string {
  if (!sub) return a.path;
  return a.path === '/' ? `/${sub}` : `${a.path}/${sub}`;
}

/** Where a user goes after login: `next` when its area is theirs, otherwise their first area; null without any area. */
export function landingPath(me: Me, next?: string | null): string | null {
  const mine = areasOf(me);
  if (next && next.startsWith('/') && mine.includes(areaOfPath(next.split('?')[0]))) return next;
  return mine[0]?.path ?? null;
}

const StaffCtx = createContext<Area | null>(null);

/** Current Staff App area; null outside the Staff App (Member App). */
export function useArea(): Area | null {
  return useContext(StaffCtx);
}

/** Root of the Staff App: tracks the current area for the header, area switcher and 403 page. */
export function StaffAreas({ children }: { children: React.ReactNode }) {
  const loc = useLocation();
  const current = areaOfPath(loc.pathname);
  // Touch areas get 44 px+ controls (Technical Doc §6.5).
  useEffect(() => {
    const root = document.documentElement;
    if (current.layout === 'touch') root.dataset.density = 'touch';
    else delete root.dataset.density;
  }, [current.layout]);
  return <StaffCtx.Provider value={current}>{children}</StaffCtx.Provider>;
}

/**
 * Guard of an area (FR-SH-04): the domain first, then login, then the area
 * permission. An area of another domain shows 403 with a link to that
 * domain; the app root sends the user to their first area; other paths
 * without permission show 403 with links to the areas the user may open.
 */
export function RequireArea({ code, children }: { code: AreaCode; children: React.ReactNode }) {
  const { me, loading, problem } = useAuth();
  const loc = useLocation();
  const a = area(code);
  const root = loc.pathname === '/' && !loc.search;
  const here = onSurface(a);
  if (!here && !root) return <ForbiddenPage elsewhere={a} />;
  if (problem?.status === 503) return <MaintenancePage />;
  if (loading) return <div className="oc-content"><Skeleton rows={6} /></div>;
  if (!me || me.mfaPending || me.passwordChangeRequired) {
    // The app root has no `next`: the user lands on their first area.
    const next = root ? '' : `?next=${encodeURIComponent(loc.pathname + loc.search)}`;
    return <Navigate to={`/login${next}`} replace />;
  }
  if (!here || !me.shells.includes(a.shell)) {
    const home = landingPath(me);
    if (root && home) return <Navigate to={home} replace />;
    return <ForbiddenPage />;
  }
  return <>{children}</>;
}
