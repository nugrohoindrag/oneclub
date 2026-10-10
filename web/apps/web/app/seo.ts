/**
 * SEO files of the website (PRD P4 FR-CMS-08): /sitemap.xml and /robots.txt
 * follow the CMS (published pages, Content Policies) and add the website's
 * own structured routes and private paths.
 */
import { headers } from 'next/headers';
import { LANGS } from './lib';

/** Public structured pages of the website (below /{lang}); the CMS adds its pages, news and albums. */
export const STATIC_ROUTES = [
  '', 'golf', 'golf/course-guide', 'golf/hole-by-hole', 'golf/handicap', 'golf/facilities', 'golf/reciprocal-clubs', 'sport-club', 'bungalow',
  'vip-suite', 'meeting', 'wedding-banquet', 'events', 'tournaments', 'membership', 'packages', 'promotions', 'hall-of-fame', 'news', 'gallery',
  'contact', 'location', 'book/sport-club', 'book/sport-club/check',
];

/** Private pages (personal links with a token, previews): never crawled. */
export const PRIVATE_PATHS = [
  'payment/', 'quotation/', 'supplier/', 'unsubscribe/', 'invoice/', 'booking/', 'preview/', 'events/ticket/', 'tournaments/registration/',
  'book-golf/manage', 'book/sport-club/pay/', 'book/sport-club/booking/', 'sport-club/ticket/',
];

/** The origin of the website: ONECLUB_WEBSITE_URL, else as requested (behind the proxy: X-Forwarded-*). */
export async function siteOrigin(): Promise<string> {
  const env = process.env.ONECLUB_WEBSITE_URL;
  if (env) return env.replace(/\/$/, '');
  const h = await headers();
  const host = h.get('x-forwarded-host') ?? h.get('host') ?? 'localhost:3000';
  const local = host.startsWith('localhost') || host.startsWith('127.');
  const proto = h.get('x-forwarded-proto')?.split(',')[0].trim() || (local ? 'http' : 'https');
  return `${proto}://${host}`;
}

/** An absolute URL (the CMS returns absolute URLs when its website URL is configured). */
export const absolute = (origin: string, href: string) => (/^https?:\/\//i.test(href) ? href : `${origin}${href.startsWith('/') ? '' : '/'}${href}`);

/** Disallowed paths: the API, the website's private pages and the Content Policies paths, in every language. */
export function disallowed(policy: string[] = []): string[] {
  const out = new Set<string>(['/api/']);
  for (const p of PRIVATE_PATHS) for (const l of LANGS) out.add(`/${l}/${p}`);
  for (const raw of policy) {
    if (!raw.trim()) continue;
    const p = raw.startsWith('/') ? raw : `/${raw}`;
    out.add(p);
    const scoped = p === '/api/' || LANGS.some((l) => p === `/${l}` || p.startsWith(`/${l}/`));
    if (!scoped) for (const l of LANGS) out.add(`/${l}${p}`);
  }
  return [...out];
}
