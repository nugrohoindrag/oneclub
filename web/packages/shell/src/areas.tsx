import React, { createContext, useContext, useEffect } from 'react';
import { Navigate, useLocation } from 'react-router';
import { useAuth, type Me } from './context';
import { Skeleton } from './components/ui';
import { ForbiddenPage, MaintenancePage } from './pages/errors';
import { AREAS, type Area, type AreaCode } from './area-list';

export { AREAS, type Area, type AreaCode };

export function area(code: AreaCode): Area {
  return AREAS.find((a) => a.code === code)!;
}

/** Areas the user may open, in landing order. */
export function areasOf(me: Me | null): Area[] {
  return me ? AREAS.filter((a) => me.shells.includes(a.shell)) : [];
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
 * Guard of an area (FR-SH-04): login first, then the area permission. The
 * app root sends a user without Back Office to their own area; other paths
 * show 403 with links to the areas the user may open.
 */
export function RequireArea({ code, children }: { code: AreaCode; children: React.ReactNode }) {
  const { me, loading, problem } = useAuth();
  const loc = useLocation();
  if (problem?.status === 503) return <MaintenancePage />;
  if (loading) return <div className="oc-content"><Skeleton rows={6} /></div>;
  if (!me || me.mfaPending || me.passwordChangeRequired) {
    // The app root has no `next`: the user lands on their first area.
    const next = loc.pathname === '/' && !loc.search ? '' : `?next=${encodeURIComponent(loc.pathname + loc.search)}`;
    return <Navigate to={`/login${next}`} replace />;
  }
  if (!me.shells.includes(area(code).shell)) {
    const home = landingPath(me);
    if (loc.pathname === '/' && home) return <Navigate to={home} replace />;
    return <ForbiddenPage />;
  }
  return <>{children}</>;
}
