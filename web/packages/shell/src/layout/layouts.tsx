import React, { useState } from 'react';
import { Link, Navigate, NavLink, Outlet, useLocation } from 'react-router';
import { useGet } from '@oneclub/api-client';
import { useTranslation } from '@oneclub/i18n';
import { useAuth, useBootstrap, type Shell } from '../context';
import { Icon, RowActionsStyle, Skeleton } from '../components/ui';
import { HeaderActions } from './header';
import { useArea } from '../areas';
import { logoOf } from '../theme';
import { ForbiddenPage, MaintenancePage } from '../pages/errors';

export interface NavItem {
  key: string;
  label: string;
  path: string;
  icon?: string;
  module?: string;
  comingSoon?: boolean;
  phase?: string;
  /** Heading of a group of items (role menus); not a link. */
  section?: boolean;
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
      <img src={logoOf(b.branding)} alt="" />
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

/**
 * Sub-menu link. NavLink ignores the query string, so `/reports?module=golf`
 * and `/reports` would all be active on /reports: the sibling that matches
 * the URL best is active — an item with a query on that exact URL, then the
 * same path, then the longest path the page sits below (a run of Payroll
 * Runs, an employee of Employees).
 */
function SubLink({ item, siblings }: { item: NavItem; siblings: NavItem[] }) {
  const loc = useLocation();
  const here = loc.pathname + loc.search;
  const score = (s: NavItem) => {
    if (s.path.includes('?')) return here === s.path ? 3000 : -1;
    if (loc.pathname === s.path) return 2000;
    return under(s.path, loc.pathname) ? s.path.length : -1;
  };
  const best = siblings.reduce<NavItem | null>((b, s) => (score(s) > (b ? score(b) : -1) ? s : b), null);
  const active = best?.key === item.key;
  return <Link to={item.path} className={active ? 'active' : undefined} aria-current={active ? 'page' : undefined}>{item.label}</Link>;
}

/**
 * Group of a module menu (Human Resources → Time & Attendance, Golf →
 * Tournaments): opens its items while its page or one of them is current;
 * the page itself is lit only when it is not also one of its items.
 */
function SubGroup({ item }: { item: NavItem }) {
  const loc = useLocation();
  const items = item.children ?? [];
  const own = !items.some((c) => c.path === item.path);
  const open = under(item.path, loc.pathname) || items.some((c) => under(c.path, loc.pathname));
  const active = own && loc.pathname === item.path;
  return (
    <>
      <Link to={item.path} className={active ? 'active' : open ? 'oc-nav-parent' : undefined} aria-expanded={open} aria-current={active ? 'page' : undefined}>
        {item.label}
      </Link>
      {open && <div className="oc-nav-sub">{items.map((g) => <SubLink key={g.key} item={g} siblings={items} />)}</div>}
    </>
  );
}

/**
 * Score of a menu path against the current URL: same path, every query
 * parameter of the item present with the same value; more parameters and an
 * exact path win. -1 = no match.
 */
function matchScore(path: string, pathname: string, search: URLSearchParams): number {
  const [p, q = ''] = path.split('?');
  const exact = pathname === p;
  if (!exact && !(p !== '/' && pathname.startsWith(p + '/'))) return -1;
  const params = [...new URLSearchParams(q)];
  if (params.some(([k, v]) => search.get(k) !== v)) return -1;
  return (exact ? 100 : 50) + params.length * 10;
}

/**
 * Role menu with section headings (RoleTrees, e.g. the Accountant): every
 * link has an icon and only the best-matching one is active, so items that
 * open tabs of one page (?tab=) do not light up together.
 */
function SectionedNav({ items }: { items: NavItem[] }) {
  const loc = useLocation();
  const { t } = useTranslation();
  const search = new URLSearchParams(loc.search);
  // Leaves: plain items, items of a section and the sub-items of a group.
  const flat = (list: NavItem[]): NavItem[] => list.flatMap((it) => (it.children?.length ? flat(it.children) : [it]));
  let active = '';
  let best = -1;
  for (const it of flat(items)) {
    const s = matchScore(it.path, loc.pathname, search);
    if (s > best) [best, active] = [s, it.key];
  }
  const link = (it: NavItem, extra?: string) => (
    <Link key={it.key} to={it.path} title={it.label} className={[it.key === active ? 'active' : '', extra ?? ''].join(' ').trim() || undefined}
      aria-current={it.key === active ? 'page' : undefined}>
      <Icon name={it.icon ?? 'chevron_right'} size={20} />
      <span className="oc-nav-label">{it.label}</span>
      {it.comingSoon && <span className="oc-nav-soon oc-nav-label" title={t('common.comingSoon', { phase: it.phase })}>{it.phase}</span>}
    </Link>
  );
  // A group opens its sub-items while one of them is the current page.
  const entry = (it: NavItem) => {
    if (!it.children?.length) return link(it);
    const open = it.children.some((c) => c.key === active);
    return (
      <React.Fragment key={it.key}>
        <Link to={it.path} title={it.label} className={open ? 'oc-nav-parent' : undefined} aria-expanded={open}>
          <Icon name={it.icon ?? 'chevron_right'} size={20} />
          <span className="oc-nav-label">{it.label}</span>
          <Icon name={open ? 'expand_less' : 'expand_more'} size={18} className="oc-nav-caret" />
        </Link>
        {open && <div className="oc-nav-sub oc-nav-tree">{it.children.map((c) => link(c))}</div>}
      </React.Fragment>
    );
  };
  return (
    <>
      {items.map((it) => (it.section ? (
        <React.Fragment key={it.key}>
          <div className="oc-nav-caption oc-nav-section oc-nav-label">{it.label}</div>
          {(it.children ?? []).map(entry)}
        </React.Fragment>
      ) : entry(it)))}
    </>
  );
}

/** Whether the current path is a menu path (its query aside) or below it. */
function under(path: string, pathname: string): boolean {
  const p = path.split('?')[0];
  return pathname === p || (p !== '/' && pathname.startsWith(p + '/'));
}

/**
 * Module of the sidebar: it stays open (and lit) on every page of its first
 * path segment (Human Resources on /hris/…, Golf on /golf/…); a group of
 * its menu opens while one of its items is the current page.
 */
function SideItem({ item }: { item: NavItem }) {
  const loc = useLocation();
  const { t } = useTranslation();
  const root = useArea()?.path ?? '/';
  const segment = item.children?.length ? '/' + (item.path.split('/')[1] ?? '') : item.path;
  const open = under(item.path, loc.pathname) || (segment !== '/' && under(segment, loc.pathname));
  return (
    <>
      <NavLink to={item.path} end={item.path === root} title={item.label} className={({ isActive }) => (isActive || (open && item.path !== root) ? 'active' : '')}>
        <Icon name={item.icon ?? 'chevron_right'} size={20} />
        <span className="oc-nav-label">{item.label}</span>
        {item.comingSoon && <span className="oc-nav-soon oc-nav-label" title={t('common.comingSoon', { phase: item.phase })}>{item.phase}</span>}
      </NavLink>
      {item.children && open && item.path !== root && (
        <div className="oc-nav-sub">
          {item.children.map((c) => (c.children?.length
            ? <SubGroup key={c.key} item={c} />
            : <SubLink key={c.key} item={c} siblings={item.children!} />))}
        </div>
      )}
    </>
  );
}

/** Back Office and Management Dashboard layout: sidebar (68/196 px) + top bar (Technical Doc §6.5). */
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
  const area = useArea();
  return (
    <div className="oc-app" data-shell={shell}>
      <nav className="oc-sidebar" data-collapsed={collapsed} aria-label="Main">
        <div className="oc-sidebar-top">
          <Brand />
          <button className="oc-icon-btn oc-sidebar-toggle" onClick={toggle} aria-label={collapsed ? 'Expand menu' : 'Collapse menu'}>
            <Icon name={collapsed ? 'left_panel_open' : 'left_panel_close'} size={20} />
          </button>
        </div>
        {area && <div className="oc-nav-caption oc-nav-label">{area.label}</div>}
        <div className="oc-nav">
          {nav.isLoading && <Skeleton rows={8} />}
          {nav.data && (nav.data.items.some((it) => it.section)
            ? <SectionedNav items={nav.data.items} />
            : nav.data.items.map((it) => <SideItem key={it.key} item={it} />))}
        </div>
      </nav>
      <div className="oc-main">
        <header className="oc-header">
          <span className="oc-spacer" />
          <HeaderActions />
        </header>
        <main className="oc-content" id="main">
          {/* office tables: row actions in a ⋮ menu */}
          <RowActionsStyle.Provider value="menu"><Outlet /></RowActionsStyle.Provider>
        </main>
      </div>
    </div>
  );
}

/** Top pill navigation (Member Portal) — dashboard-ui.webp. */
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
