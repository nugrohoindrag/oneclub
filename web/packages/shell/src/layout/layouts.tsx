import React, { useState } from 'react';
import { Link, Navigate, NavLink, Outlet, useLocation } from 'react-router';
import { useGet } from '@oneclub/api-client';
import { useTranslation } from '@oneclub/i18n';
import { useAuth, useBootstrap, type Shell } from '../context';
import { Icon, Skeleton } from '../components/ui';
import { HeaderActions } from './header';
import { useArea } from '../areas';
import { ForbiddenPage, MaintenancePage } from '../pages/errors';

export interface NavItem {
  key: string;
  label: string;
  path: string;
  icon?: string;
  module?: string;
  comingSoon?: boolean;
  phase?: string;
  children?: NavItem[];
}

/** Server-built navigation for a shell (FR-SH-02). */
export function useNavigation(shell: Shell) {
  const { propertyId, me } = useAuth();
  return useGet<{ shell: string; items: NavItem[] }>(me ? `/api/v1/platform/navigation?shell=${shell}&p=${propertyId}` : null);
}

/** Logo linking to the home of the current area (`/` outside the Staff App). */
export function Brand() {
  const b = useBootstrap();
  const to = useArea()?.path ?? '/';
  return (
    <Link to={to} className="oc-brand" aria-label={b.branding.appName}>
      {b.branding.logoUrl ? <img src={b.branding.logoUrl} alt="" /> : <span className="oc-brand-mark">{b.branding.appName.slice(0, 1)}</span>}
      <span className="oc-brand-name">{b.branding.appName}</span>
    </Link>
  );
}

/**
 * Guard: requires a session that completed MFA/password change and access to
 * the shell; otherwise redirects to login or shows 403 (FR-SH-04).
 */
export function RequireShell({ shell, children, login = '/login' }: { shell: Shell; children: React.ReactNode; login?: string }) {
  const { me, loading, problem } = useAuth();
  const loc = useLocation();
  if (problem?.status === 503) return <MaintenancePage />;
  if (loading) return <div className="oc-content"><Skeleton rows={6} /></div>;
  if (!me || me.mfaPending || me.passwordChangeRequired) {
    return <Navigate to={`${login}?next=${encodeURIComponent(loc.pathname + loc.search)}`} replace />;
  }
  if (!me.shells.includes(shell)) return <ForbiddenPage />;
  return <>{children}</>;
}

/**
 * Page-level guard (FR-SH-04): shows the 403 page when the user lacks the
 * permission at the active property. The backend still enforces it.
 */
export function RequirePermission({ perm, children }: { perm: string | string[]; children: React.ReactNode }) {
  const { can } = useAuth();
  const perms = Array.isArray(perm) ? perm : [perm];
  return perms.some((p) => can(p)) ? <>{children}</> : <ForbiddenPage />;
}

function SideItem({ item }: { item: NavItem }) {
  const loc = useLocation();
  const { t } = useTranslation();
  const open = loc.pathname === item.path || loc.pathname.startsWith(item.path + '/');
  return (
    <>
      <NavLink to={item.path} end={item.path === '/'} title={item.label}>
        <Icon name={item.icon ?? 'chevron_right'} size={20} />
        <span className="oc-nav-label">{item.label}</span>
        {item.comingSoon && <span className="oc-nav-soon oc-nav-label" title={t('common.comingSoon', { phase: item.phase })}>{item.phase}</span>}
      </NavLink>
      {item.children && open && item.path !== '/' && (
        <div className="oc-nav-sub">
          {item.children.map((c) => (
            <React.Fragment key={c.key}>
              <NavLink to={c.path} end>{c.label}</NavLink>
              {c.children && (loc.pathname.startsWith(c.path)) && (
                <div className="oc-nav-sub">{c.children.map((g) => <NavLink key={g.key} to={g.path} end>{g.label}</NavLink>)}</div>
              )}
            </React.Fragment>
          ))}
        </div>
      )}
    </>
  );
}

/** Back Office layout: sidebar (68/196 px) + top bar (Technical Doc §6.5). */
export function SidebarLayout({ shell = 'backoffice' }: { shell?: Shell }) {
  const nav = useNavigation(shell);
  const [collapsed, setCollapsed] = useState(() => {
    try {
      return localStorage.getItem('oneclub.sidebar') === 'collapsed';
    } catch {
      return false;
    }
  });
  const toggle = () => {
    setCollapsed((c) => {
      try {
        localStorage.setItem('oneclub.sidebar', c ? 'expanded' : 'collapsed');
      } catch {
        /* ignore */
      }
      return !c;
    });
  };
  return (
    <div className="oc-app">
      <nav className="oc-sidebar" data-collapsed={collapsed} aria-label="Main">
        <Brand />
        <div className="oc-nav">
          {nav.isLoading && <Skeleton rows={8} />}
          {nav.data?.items.map((it) => <SideItem key={it.key} item={it} />)}
        </div>
        <span className="oc-spacer" />
        <button className="oc-btn oc-btn-text" onClick={toggle} aria-label={collapsed ? 'Expand menu' : 'Collapse menu'}>
          <Icon name={collapsed ? 'left_panel_open' : 'left_panel_close'} size={20} />
        </button>
      </nav>
      <div className="oc-main">
        <header className="oc-header">
          <span className="oc-spacer" />
          <HeaderActions />
        </header>
        <main className="oc-content" id="main">
          <Outlet />
        </main>
      </div>
    </div>
  );
}

/** Top pill navigation (Management Dashboard, Member Portal) — dashboard-ui.webp. */
export function TopNavLayout({ shell, bottomNav, property = true }: { shell: Shell; bottomNav?: boolean; property?: boolean }) {
  const nav = useNavigation(shell);
  const items = nav.data?.items ?? [];
  return (
    <div className="oc-topnav-frame">
      <header className="oc-topbar">
        <Brand />
        <span className="oc-spacer" />
        <nav className="oc-pill-nav" aria-label="Main" data-mobile-hide={bottomNav}>
          {items.map((it) => (
            <NavLink key={it.key} to={it.path} end>
              {it.icon && <Icon name={it.icon} size={18} />} {it.label}
              {it.comingSoon && <span className="oc-nav-soon">{it.phase}</span>}
            </NavLink>
          ))}
        </nav>
        <span className="oc-spacer" />
        <HeaderActions property={property} />
      </header>
      <main id="main"><Outlet /></main>
      {bottomNav && (
        <nav className="oc-bottom-nav" aria-label="Main">
          {items.slice(0, 5).map((it) => (
            <NavLink key={it.key} to={it.path} end>
              <Icon name={it.icon ?? 'circle'} size={24} />
              {it.label}
            </NavLink>
          ))}
        </nav>
      )}
    </div>
  );
}
