import React, { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react';
import { QueryClientProvider, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  ApiError,
  createQueryClient,
  getActiveProperty,
  onActivePropertyChange,
  onAuthProblem,
  request,
  setActiveProperty,
  setRequestLocale,
  type Problem,
  type Schemas,
} from '@oneclub/api-client';
import { initI18n, setLocale, type Locale } from '@oneclub/i18n';
import { applyBranding, applyTheme, storedTheme, type BrandingInfo, type ThemeMode } from './theme';
import { ToastProvider } from './components/toast';

export type Bootstrap = Schemas['Bootstrap'];
export type Me = Schemas['MeResponse'];
export type Shell = 'backoffice' | 'management' | 'member' | 'ops' | 'platform-admin' | 'caddy';

// ── bootstrap (public) ────────────────────────────────────────────────────

const BootstrapCtx = createContext<Bootstrap | null>(null);

export function useBootstrap(): Bootstrap {
  const b = useContext(BootstrapCtx);
  if (!b) throw new Error('useBootstrap outside BootstrapProvider');
  return b;
}

/** Instance flag (FR-INS-05) readable by the frontend. */
export function useFlag(key: string): unknown {
  return useBootstrap().flags?.[key];
}

export function useModuleEnabled(module: string) {
  return useBootstrap().enabledModules.includes(module);
}

function BootstrapGate({ children }: { children: React.ReactNode }) {
  const q = useQuery<Bootstrap, ApiError>({
    queryKey: ['/api/v1/public/bootstrap'],
    queryFn: () => request<Bootstrap>('GET', '/api/v1/public/bootstrap'),
    staleTime: 60_000,
    refetchInterval: 5 * 60_000,
  });
  useEffect(() => {
    if (!q.data) return;
    applyBranding(q.data.branding as BrandingInfo);
    applyTheme(storedTheme() ?? (q.data.defaultTheme as 'light' | 'dark'), q.data.defaultTheme as 'light' | 'dark');
  }, [q.data]);
  if (q.error) {
    return (
      <div className="oc-login-bg">
        <div className="oc-card" style={{ maxWidth: 480 }}>
          <h2>OneClub</h2>
          <p className="oc-muted">{q.error.status === 503 ? 'This instance is under maintenance.' : 'The server cannot be reached. Check your connection and try again.'}</p>
          <button className="oc-btn oc-btn-ink" onClick={() => q.refetch()}>Try again</button>
        </div>
      </div>
    );
  }
  if (!q.data) return <div className="oc-login-bg"><div className="oc-skel" style={{ width: 160 }} /></div>;
  return <BootstrapCtx.Provider value={q.data}>{children}</BootstrapCtx.Provider>;
}

// ── auth ──────────────────────────────────────────────────────────────────

interface AuthState {
  me: Me | null;
  loading: boolean;
  /** Last auth problem (session expired, MFA pending, suspended …). */
  problem: Problem | null;
  refresh: () => Promise<unknown>;
  logout: () => Promise<void>;
  can: (perm: string) => boolean;
  propertyId: string;
  setProperty: (id: string) => void;
  locale: Locale;
  changeLocale: (l: Locale) => void;
  theme: ThemeMode;
  changeTheme: (t: ThemeMode) => void;
}

const AuthCtx = createContext<AuthState | null>(null);

export function useAuth(): AuthState {
  const a = useContext(AuthCtx);
  if (!a) throw new Error('useAuth outside AuthProvider');
  return a;
}

function AuthProvider({ children, onLogout }: { children: React.ReactNode; onLogout?: () => Promise<void> | void }) {
  const qc = useQueryClient();
  const boot = useBootstrap();
  const [problem, setProblem] = useState<Problem | null>(null);
  const [propertyId, setPropertyState] = useState(getActiveProperty());
  const [locale, setLocaleState] = useState<Locale>(() => (document.documentElement.lang === 'en' ? 'en' : 'id'));
  const [theme, setTheme] = useState<ThemeMode>(() => storedTheme() ?? (boot.defaultTheme as ThemeMode));

  useEffect(() => onActivePropertyChange(() => setPropertyState(getActiveProperty())), []);
  useEffect(() => onAuthProblem((p) => setProblem(p)), []);

  const meQ = useQuery<Me | null, ApiError>({
    queryKey: ['/api/v1/auth/me', propertyId],
    queryFn: async () => {
      try {
        return await request<Me>('GET', '/api/v1/auth/me');
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) return null;
        throw e;
      }
    },
    staleTime: 30_000,
  });
  const me = meQ.data ?? null;

  // Keep the active property valid for this user (property switcher).
  useEffect(() => {
    if (!me || me.properties.length === 0) return;
    if (!me.properties.some((p) => p.id === propertyId)) {
      setActiveProperty(me.properties[0].id);
    }
  }, [me, propertyId]);

  useEffect(() => {
    if (me?.locale && (me.locale === 'en' || me.locale === 'id')) {
      void setLocale(me.locale);
      setLocaleState(me.locale);
      setRequestLocale(me.locale);
    }
  }, [me?.locale]);

  const can = useCallback((perm: string) => !!me?.permissions.includes(perm), [me]);

  const logout = useCallback(async () => {
    try {
      await request('POST', '/api/v1/auth/logout');
    } catch {
      /* already logged out */
    }
    await onLogout?.();
    qc.clear();
    window.location.assign('/login');
  }, [qc, onLogout]);

  const changeLocale = useCallback(
    (l: Locale) => {
      void setLocale(l);
      setLocaleState(l);
      setRequestLocale(l);
      if (me) void request('PATCH', '/api/v1/auth/me/preferences', { locale: l }).catch(() => undefined);
    },
    [me],
  );

  const changeTheme = useCallback(
    (t: ThemeMode) => {
      applyTheme(t, boot.defaultTheme as 'light' | 'dark');
      setTheme(t);
      if (me && boot.flags?.['ui.theme_switch'] !== false) void request('PATCH', '/api/v1/auth/me/preferences', { theme: t }).catch(() => undefined);
    },
    [me, boot],
  );

  const value = useMemo<AuthState>(
    () => ({
      me, loading: meQ.isLoading, problem, refresh: () => meQ.refetch(), logout, can,
      propertyId, setProperty: setActiveProperty, locale, changeLocale, theme, changeTheme,
    }),
    [me, meQ, problem, logout, can, propertyId, locale, changeLocale, theme, changeTheme],
  );
  return <AuthCtx.Provider value={value}>{children}</AuthCtx.Provider>;
}

const queryClient = createQueryClient();

/** All providers every shell needs. */
export function AppProviders({ children, onLogout }: { children: React.ReactNode; onLogout?: () => Promise<void> | void }) {
  useState(() => initI18n((document.documentElement.lang as Locale) || 'id'));
  return (
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
        <BootstrapGate>
          <LocaleDefault />
          <AuthProvider onLogout={onLogout}>{children}</AuthProvider>
        </BootstrapGate>
      </ToastProvider>
    </QueryClientProvider>
  );
}

/** Applies the instance default language when the user has not chosen one. */
function LocaleDefault() {
  const boot = useBootstrap();
  useEffect(() => {
    let stored: string | null = null;
    try {
      stored = localStorage.getItem('oneclub.locale');
    } catch {
      /* ignore */
    }
    if (!stored && (boot.defaultLocale === 'en' || boot.defaultLocale === 'id')) {
      void setLocale(boot.defaultLocale);
      setRequestLocale(boot.defaultLocale);
    }
  }, [boot.defaultLocale]);
  return null;
}

export { queryClient };
