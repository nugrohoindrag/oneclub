import i18n from 'i18next';
import { initReactI18next, useTranslation } from 'react-i18next';
import { en, id } from './resources';

export type Locale = 'en' | 'id';
export { en, id };

const KEY = 'oneclub.locale';

/** Initialises i18next once per app (FR-L10N-01). */
export function initI18n(defaultLocale: Locale = 'id') {
  let stored: string | null = null;
  try {
    stored = localStorage.getItem(KEY);
  } catch {
    /* ignore */
  }
  const lng = stored === 'en' || stored === 'id' ? stored : defaultLocale;
  if (!i18n.isInitialized) {
    void i18n.use(initReactI18next).init({
      resources: { en: { translation: en }, id: { translation: id } },
      lng,
      fallbackLng: 'en',
      interpolation: { escapeValue: false },
      returnNull: false,
    });
  }
  document.documentElement.lang = lng;
  return i18n;
}

export function setLocale(l: Locale) {
  try {
    localStorage.setItem(KEY, l);
  } catch {
    /* ignore */
  }
  document.documentElement.lang = l;
  return i18n.changeLanguage(l);
}

export function currentLocale(): Locale {
  return (i18n.language === 'en' ? 'en' : 'id') as Locale;
}

export { i18n, useTranslation };

// ── formatting (FR-L10N-03) ─────────────────────────────────────────────────

const intlLocale = (l: Locale) => (l === 'en' ? 'en-GB' : 'id-ID');

/** Formats money; IDR has no minor unit. Accepts decimal strings. */
export function formatMoney(amount: string | number, currency = 'IDR', locale: Locale = currentLocale()) {
  const n = typeof amount === 'string' ? Number(amount) : amount;
  const fraction = currency === 'IDR' || currency === 'JPY' ? 0 : 2;
  return new Intl.NumberFormat(intlLocale(locale), {
    style: 'currency', currency, minimumFractionDigits: fraction, maximumFractionDigits: fraction,
  }).format(n);
}

export function formatNumber(n: number | string, locale: Locale = currentLocale(), digits = 0) {
  return new Intl.NumberFormat(intlLocale(locale), { maximumFractionDigits: digits }).format(Number(n));
}

/** Date-time in the instance/property timezone (conversion only at presentation). */
export function formatDateTime(iso: string | Date | null | undefined, timeZone?: string, locale: Locale = currentLocale()) {
  if (!iso) return '—';
  const d = typeof iso === 'string' ? new Date(iso) : iso;
  return new Intl.DateTimeFormat(intlLocale(locale), { dateStyle: 'medium', timeStyle: 'short', timeZone }).format(d);
}

export function formatDate(iso: string | Date | null | undefined, timeZone?: string, locale: Locale = currentLocale()) {
  if (!iso) return '—';
  const d = typeof iso === 'string' ? (iso.length === 10 ? new Date(iso + 'T00:00:00') : new Date(iso)) : iso;
  return new Intl.DateTimeFormat(intlLocale(locale), { dateStyle: 'medium', timeZone: iso && typeof iso === 'string' && iso.length === 10 ? undefined : timeZone }).format(d);
}

/** Relative time ("5 minutes ago" / "5 menit yang lalu"). */
export function formatRelative(iso: string, locale: Locale = currentLocale()) {
  const diff = (new Date(iso).getTime() - Date.now()) / 1000;
  const rtf = new Intl.RelativeTimeFormat(intlLocale(locale), { numeric: 'auto' });
  const abs = Math.abs(diff);
  if (abs < 60) return rtf.format(Math.round(diff), 'second');
  if (abs < 3600) return rtf.format(Math.round(diff / 60), 'minute');
  if (abs < 86400) return rtf.format(Math.round(diff / 3600), 'hour');
  return rtf.format(Math.round(diff / 86400), 'day');
}

/** Translates a validation code from the API (falls back to the server message). */
export function validationMessage(code: string, fallback: string) {
  const key = `validation.${code}`;
  return i18n.exists(key) ? i18n.t(key) : fallback;
}
