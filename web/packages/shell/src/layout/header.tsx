import React, { useEffect, useRef, useState } from 'react';
import { useNavigate } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { request, useGet, type Page, type Schemas } from '@oneclub/api-client';
import { formatRelative, useTranslation } from '@oneclub/i18n';
import { useAuth, useFlag } from '../context';
import { Icon } from '../components/ui';

function useOutside(ref: React.RefObject<HTMLElement | null>, onOut: () => void, open: boolean) {
  useEffect(() => {
    if (!open) return;
    const h = (e: MouseEvent) => ref.current && !ref.current.contains(e.target as Node) && onOut();
    const k = (e: KeyboardEvent) => e.key === 'Escape' && onOut();
    document.addEventListener('mousedown', h);
    document.addEventListener('keydown', k);
    return () => {
      document.removeEventListener('mousedown', h);
      document.removeEventListener('keydown', k);
    };
  }, [ref, onOut, open]);
}

/** Property switcher (FR-ORG-06): only properties the user may access. */
export function PropertySwitcher() {
  const { me, propertyId, setProperty } = useAuth();
  const { t } = useTranslation();
  const qc = useQueryClient();
  if (!me || me.properties.length === 0) return null;
  const change = (id: string) => {
    setProperty(id);
    void qc.invalidateQueries();
  };
  if (me.properties.length === 1) {
    return (
      <span className="oc-chip" title={t('shell.property')} style={{ cursor: 'default' }}>
        <Icon name="apartment" size={18} /> {me.properties[0].name}
      </span>
    );
  }
  return (
    <label className="oc-row" style={{ gap: 6 }}>
      <span className="oc-sr">{t('shell.property')}</span>
      <Icon name="apartment" size={20} />
      <select className="oc-select" style={{ height: 40, minWidth: 180 }} value={propertyId} onChange={(e) => change(e.target.value)}
        aria-label={t('shell.property')}>
        {me.properties.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
      </select>
    </label>
  );
}

type Notification = Schemas['Notification'];

/** Notification center (FR-NOT-05): badge, list, mark as read. */
export function NotificationBell({ to = '/notifications' }: { to?: string }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const nav = useNavigate();
  const qc = useQueryClient();
  useOutside(ref, () => setOpen(false), open);
  const count = useGet<{ unread: number }>('/api/v1/platform/notifications/unread-count', { refetchInterval: 30_000 });
  const list = useGet<Page<Notification>>(open ? '/api/v1/platform/notifications?limit=8' : null);
  const refresh = () => {
    void qc.invalidateQueries({ predicate: (q) => String(q.queryKey[0]).startsWith('/api/v1/platform/notifications') });
  };
  const read = async (n: Notification) => {
    if (!n.readAt) await request('POST', `/api/v1/platform/notifications/${n.id}:read`).catch(() => undefined);
    refresh();
    setOpen(false);
    if (n.link) {
      try {
        const u = new URL(n.link, window.location.origin);
        if (u.origin === window.location.origin) nav(u.pathname + u.search);
        else window.location.assign(n.link);
      } catch {
        /* ignore */
      }
    }
  };
  const unread = count.data?.unread ?? 0;
  return (
    <div className="oc-popover-anchor" ref={ref}>
      <button className="oc-icon-btn" aria-label={`${t('shell.notifications')} (${unread})`} aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        <Icon name="notifications" size={20} />
        {unread > 0 && <span className="oc-dot">{unread > 99 ? '99+' : unread}</span>}
      </button>
      {open && (
        <div className="oc-popover" style={{ width: 360 }}>
          <div className="oc-row" style={{ padding: '6px 8px' }}>
            <strong>{t('shell.notifications')}</strong>
            <span className="oc-spacer" />
            {unread > 0 && (
              <button className="oc-btn oc-btn-text oc-btn-sm" onClick={async () => {
                await request('POST', '/api/v1/platform/notifications:read-all').catch(() => undefined);
                refresh();
              }}>{t('shell.markAllRead')}</button>
            )}
          </div>
          {list.data?.items.length === 0 && <div className="oc-empty oc-small">{t('shell.noNotifications')}</div>}
          {list.data?.items.map((n) => (
            <div key={n.id} className="oc-notif" data-unread={!n.readAt} role="button" tabIndex={0} onClick={() => read(n)}
              onKeyDown={(e) => e.key === 'Enter' && read(n)}>
              <div style={{ fontWeight: 600 }}>{n.title}</div>
              <div className="oc-small oc-muted" style={{ whiteSpace: 'pre-line', maxHeight: 60, overflow: 'hidden' }}>{n.body}</div>
              <div className="oc-small oc-muted">{formatRelative(n.createdAt)}</div>
            </div>
          ))}
          <div className="oc-menu-sep" />
          <button className="oc-menu-item" onClick={() => { setOpen(false); nav(to); }}>
            <Icon name="list" size={20} /> {t('shell.notifications')}
          </button>
        </div>
      )}
    </div>
  );
}

export function LanguageSwitcher() {
  const { locale, changeLocale } = useAuth();
  const { t } = useTranslation();
  return (
    <button className="oc-icon-btn" aria-label={`${t('shell.language')}: ${locale.toUpperCase()}`} title={t('shell.language')}
      onClick={() => changeLocale(locale === 'id' ? 'en' : 'id')} style={{ fontWeight: 700, fontSize: 12 }}>
      {locale.toUpperCase()}
    </button>
  );
}

export function ThemeToggle() {
  const { theme, changeTheme } = useAuth();
  const { t } = useTranslation();
  const enabled = useFlag('ui.theme_switch') !== false;
  if (!enabled) return null;
  const dark = document.documentElement.dataset.theme === 'dark';
  return (
    <button className="oc-icon-btn" aria-label={`${t('shell.theme')}: ${dark ? t('shell.dark') : t('shell.light')}`}
      onClick={() => changeTheme(dark ? 'light' : 'dark')} data-theme-mode={theme}>
      <Icon name={dark ? 'light_mode' : 'dark_mode'} size={20} />
    </button>
  );
}

export function initials(name: string) {
  return name.split(/\s+/).filter(Boolean).slice(0, 2).map((s) => s[0]?.toUpperCase()).join('');
}

export function UserMenu({ profilePath = '/profile', extra }: { profilePath?: string; extra?: React.ReactNode }) {
  const { me, logout } = useAuth();
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const nav = useNavigate();
  useOutside(ref, () => setOpen(false), open);
  if (!me) return null;
  return (
    <div className="oc-popover-anchor" ref={ref}>
      <button className="oc-user" aria-expanded={open} aria-haspopup="menu" onClick={() => setOpen((o) => !o)}>
        <span className="oc-avatar">{initials(me.fullName)}</span>
        <span style={{ fontWeight: 600, maxWidth: 160, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} className="oc-hide-sm">{me.fullName}</span>
        <Icon name="expand_more" size={20} />
      </button>
      {open && (
        <div className="oc-popover" role="menu">
          <div style={{ padding: '8px 12px' }}>
            <div style={{ fontWeight: 600 }}>{me.fullName}</div>
            <div className="oc-small oc-muted">{me.email}</div>
            <div className="oc-small oc-muted">{me.roles.map((r) => r.name).filter((v, i, a) => a.indexOf(v) === i).join(', ')}</div>
          </div>
          <div className="oc-menu-sep" />
          <button className="oc-menu-item" role="menuitem" onClick={() => { setOpen(false); nav(profilePath); }}>
            <Icon name="person" size={20} /> {t('shell.profile')}
          </button>
          {extra}
          <button className="oc-menu-item" role="menuitem" onClick={() => void logout()}>
            <Icon name="logout" size={20} /> {t('shell.logout')}
          </button>
        </div>
      )}
    </div>
  );
}

/** Standard header actions (FR-SH-03). */
export function HeaderActions({ property = true, notificationsPath }: { property?: boolean; notificationsPath?: string }) {
  return (
    <div className="oc-row" style={{ gap: 8 }}>
      {property && <PropertySwitcher />}
      <LanguageSwitcher />
      <ThemeToggle />
      <NotificationBell to={notificationsPath} />
      <UserMenu />
    </div>
  );
}
