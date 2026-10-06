/**
 * Branding → Morphic theme mapping (FR-BRD-01..04). Applied at runtime from
 * /api/v1/public/bootstrap, so branding changes need no rebuild or redeploy
 * (FR-BRD-02). The primary colour maps to `data-accent` (5 presets) or to a
 * server-generated custom accent that passed WCAG AA checks (FR-BRD-03).
 */
export type Accent = 'lime' | 'blue' | 'violet' | 'orange' | 'rose' | 'custom';
export type ThemeMode = 'light' | 'dark' | 'system';

export interface BrandingInfo {
  appName: string;
  emailSenderName?: string;
  logoUrl?: string | null;
  faviconUrl?: string | null;
  loginImageUrl?: string | null;
  primaryColor?: string | null;
  accent: Accent | string;
  customAccent?: { light: Record<string, string>; dark: Record<string, string> } | null;
}

// Demo defaults: the club logo and login photo shipped with the app, used
// while the instance has none in Settings → Branding (an uploaded one wins).
const DEFAULT_LOGO = new URL('./assets/club-logo.png', import.meta.url).href;
const DEFAULT_LOGIN_PHOTO = new URL('./assets/login-photo.jpg', import.meta.url).href;

/** The logo of the instance, or the default one. */
export const logoOf = (b: BrandingInfo) => b.logoUrl || DEFAULT_LOGO;
/** The login photo of the instance, or the default one. */
export const loginPhotoOf = (b: BrandingInfo) => b.loginImageUrl || DEFAULT_LOGIN_PHOTO;

const THEME_KEY = 'oneclub.theme';
const STYLE_ID = 'oc-custom-accent';

function cssBlock(selector: string, tokens: Record<string, string>) {
  return `${selector}{${Object.entries(tokens).map(([k, v]) => `${k}:${v};`).join('')}}`;
}

/** Applies accent, custom accent tokens, favicon and document title. */
export function applyBranding(b: BrandingInfo) {
  const root = document.documentElement;
  root.dataset.accent = b.accent || 'lime';
  let style = document.getElementById(STYLE_ID);
  if (b.accent === 'custom' && b.customAccent) {
    if (!style) {
      style = document.createElement('style');
      style.id = STYLE_ID;
      document.head.appendChild(style);
    }
    style.textContent =
      cssBlock("[data-accent='custom']", b.customAccent.light) +
      cssBlock("[data-theme='dark'][data-accent='custom']", b.customAccent.dark);
  } else if (style) {
    style.remove();
  }
  if (b.faviconUrl) {
    let link = document.querySelector<HTMLLinkElement>("link[rel='icon']");
    if (!link) {
      link = document.createElement('link');
      link.rel = 'icon';
      document.head.appendChild(link);
    }
    link.href = b.faviconUrl;
  }
}

export function storedTheme(): ThemeMode | null {
  try {
    const v = localStorage.getItem(THEME_KEY);
    return v === 'light' || v === 'dark' || v === 'system' ? v : null;
  } catch {
    return null;
  }
}

/** Applies Light/Dark (FR-BRD-04): user choice, else instance default. */
export function applyTheme(mode: ThemeMode, instanceDefault: 'light' | 'dark' = 'light') {
  const root = document.documentElement;
  let resolved: 'light' | 'dark' = instanceDefault;
  if (mode === 'light' || mode === 'dark') resolved = mode;
  if (mode === 'system') resolved = window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  root.dataset.theme = resolved;
  root.classList.toggle('dark', resolved === 'dark');
  try {
    localStorage.setItem(THEME_KEY, mode);
  } catch {
    /* ignore */
  }
  return resolved;
}
