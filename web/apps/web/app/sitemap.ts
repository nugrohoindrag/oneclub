import type { MetadataRoute } from 'next';
import { getSitemap } from '../components/cms/api';
import { getProperty, pub } from './lib-p2';
import { LANGS } from './lib';
import { STATIC_ROUTES, absolute, siteOrigin } from './seo';

const FREQS = ['always', 'hourly', 'daily', 'weekly', 'monthly', 'yearly', 'never'] as const;
type Freq = (typeof FREQS)[number];

/**
 * /sitemap.xml (PRD P4 FR-CMS-08): the published CMS pages, news and albums
 * and the structured routes, in Bahasa Indonesia and English with hreflang
 * alternates (CMS data cached with the "cms" tag, refreshed on publication).
 * Without the API the structured routes are still listed.
 */
export default async function sitemap(): Promise<MetadataRoute.Sitemap> {
  const origin = await siteOrigin();
  const out: MetadataRoute.Sitemap = [];
  const seen = new Set<string>();
  for (const e of (await getSitemap()) ?? []) {
    const url = absolute(origin, e.loc || e.path);
    if (seen.has(url)) continue;
    seen.add(url);
    const languages = Object.fromEntries((e.alternates ?? []).map((a) => [a.language, absolute(origin, a.href)]));
    const prio = e.priority === '' ? NaN : Number(e.priority);
    out.push({
      url,
      lastModified: e.lastmod || undefined,
      changeFrequency: (FREQS as readonly string[]).includes(e.changefreq) ? (e.changefreq as Freq) : undefined,
      priority: Number.isFinite(prio) ? prio : undefined,
      alternates: Object.keys(languages).length ? { languages } : undefined,
    });
  }
  const covered = new Set(out.map((x) => new URL(x.url).pathname.replace(/\/$/, '')));
  for (const r of STATIC_ROUTES) {
    const paths = LANGS.map((l) => (r ? `/${l}/${r}` : `/${l}`));
    const languages = Object.fromEntries(LANGS.map((l, i) => [l, `${origin}${paths[i]}`]));
    for (const p of paths) {
      if (covered.has(p)) continue;
      out.push({ url: `${origin}${p}`, changeFrequency: 'weekly', priority: r ? 0.5 : 1, alternates: { languages } });
    }
  }
  // the page of every sport bookable online (Sport Club court booking, FR-77)
  const property = await getProperty();
  const sc = property ? await pub<{ facilities: { code: string; usageMode: string; courts: number; content?: { slug?: string } }[] }>(
    `/api/v1/public/sport-club?propertyId=${property.id}`) : null;
  for (const f of (sc?.facilities ?? []).filter((x) => x.usageMode === 'slot_booking' && x.courts > 0)) {
    const slug = f.content?.slug || f.code.toLowerCase();
    const languages = Object.fromEntries(LANGS.map((l) => [l, `${origin}/${l}/book/sport-club/${slug}`]));
    for (const l of LANGS) out.push({ url: languages[l], changeFrequency: 'daily', priority: 0.6, alternates: { languages } });
  }
  return out;
}
