/**
 * CMS data of the public website (PRD P4 EP-24): published pages, news,
 * gallery, banners and navigation from /api/v1/public/cms/*. Every fetch is
 * tagged "cms" so the signed on-demand revalidation (/api/revalidate,
 * FR-CMS-10) refreshes the pages at once after a publication; the time
 * based revalidation is the fallback when the call is missed.
 */
import { getProperty } from '../../app/lib-p2';

const API = process.env.ONECLUB_API_URL ?? 'http://localhost:8080';

/** Cache tag of every CMS fetch (revalidated on publication). */
export const CMS_TAG = 'cms';

export interface CmsMedia {
  id: string; url: string; width?: number | null; height?: number | null; alt: string; caption?: string;
  variants: { name: string; width: number; height: number; url: string }[];
}
export interface CmsLink { href: string; external: boolean; newTab: boolean }
export interface CmsDataRef { source: string; owner: string; endpoint: string; query: Record<string, string>; url: string; limit: number }
export interface CmsBlock {
  id: string; type: string; config: Record<string, unknown>; content: Record<string, unknown>; media?: CmsMedia[]; link?: CmsLink;
  data?: CmsDataRef; form?: { action: string; method: string; fields: string[]; topic?: string; line?: string }; embedUrl?: string; map?: Record<string, unknown>;
}
export interface CmsContent {
  id: string; kind: string; key?: string | null; template?: string | null; language: string; requestedLanguage: string; fallback: boolean; title: string;
  slug: string; path: string; summary?: string; image?: CmsMedia | null;
  seo: { title: string; description: string; canonical: string; ogImage?: string; noindex: boolean; alternates: { language: string; href: string }[] };
  blocks: CmsBlock[]; items?: { media: CmsMedia; caption?: string }[]; breadcrumbs: { title: string; path: string }[];
  category?: { code: string; label: string } | null; tags?: string[] | null; authorName?: string | null; date?: string | null;
  structuredData: Record<string, unknown>; versionNo: number; publishedAt?: string | null; revision: number; preview: boolean;
}
export interface CmsSummary {
  id: string; kind: string; key?: string | null; title: string; slug: string; path: string; paths: Record<string, string>; summary?: string;
  image?: CmsMedia | null; category?: { code: string; label: string } | null; tags?: string[] | null; featured: boolean; date?: string | null; imageCount?: number;
}
export interface CmsList { language: string; items: CmsSummary[]; categories?: { code: string; label: string }[]; nextCursor?: string; revision: number }
export interface CmsBanner {
  id: string; placement: string; title: string; subtitle?: string; buttonLabel?: string; image?: CmsMedia | null; link?: CmsLink | null;
}
export interface CmsNavItem { id: string; label: string; href: string; external: boolean; newTab: boolean; children: CmsNavItem[] }

/** Appends the property of the website to a public API path. */
async function withProperty(path: string): Promise<string> {
  if (path.includes('propertyId=') || path.includes('property=')) return path;
  const p = await getProperty();
  if (!p) return path;
  return `${path}${path.includes('?') ? '&' : '?'}propertyId=${p.id}`;
}

/** GETs a public API path (tagged "cms"); null on 404 or when the API is down. */
export async function cmsGet<T>(path: string, revalidate = 300): Promise<T | null> {
  try {
    const r = await fetch(`${API}${await withProperty(path)}`, { next: { revalidate, tags: [CMS_TAG] } });
    if (r.ok) return (await r.json()) as T;
  } catch {
    /* fall through */
  }
  return null;
}

/** A published page by slug or key. */
export const getPage = (slug: string, lang: string) => cmsGet<CmsContent>(`/api/v1/public/cms/pages/${encodeURIComponent(slug)}?lang=${lang}`);
export const getArticle = (slug: string, lang: string) => cmsGet<CmsContent>(`/api/v1/public/cms/news/${encodeURIComponent(slug)}?lang=${lang}`);
export const getAlbum = (slug: string, lang: string) => cmsGet<CmsContent>(`/api/v1/public/cms/gallery/${encodeURIComponent(slug)}?lang=${lang}`);
export const getNews = (lang: string, q: Record<string, string> = {}) =>
  cmsGet<CmsList>(`/api/v1/public/cms/news?${new URLSearchParams({ lang, ...q }).toString()}`);
export const getAlbums = (lang: string, cursor = '') => cmsGet<CmsList>(`/api/v1/public/cms/gallery?${new URLSearchParams({ lang, ...(cursor ? { cursor } : {}) }).toString()}`);
export const getBanners = async (lang: string, placement: string, page = '') =>
  (await cmsGet<{ items: CmsBanner[] }>(`/api/v1/public/cms/banners?${new URLSearchParams({ lang, placement, ...(page ? { page } : {}) }).toString()}`))?.items ?? [];
export const getNavigation = async (lang: string, location: string) =>
  (await cmsGet<{ menus: { code: string; location: string; items: CmsNavItem[] }[] }>(`/api/v1/public/cms/navigation?lang=${lang}&location=${location}`))?.menus ?? [];

/** A preview of any version through a signed link (never cached). */
export async function getPreview(token: string): Promise<CmsContent | null> {
  try {
    const r = await fetch(`${API}/api/v1/public/cms/preview/${encodeURIComponent(token)}`, { cache: 'no-store' });
    if (r.ok) return (await r.json()) as CmsContent;
  } catch {
    /* fall through */
  }
  return null;
}

/** The redirect of an old website path, if any (EP-29 migration, slug changes). */
export async function findRedirect(path: string): Promise<{ to: string; statusCode: number } | null> {
  const r = await cmsGet<{ items: { from: string; to: string; statusCode: number }[] }>('/api/v1/public/cms/redirects');
  const hit = r?.items.find((x) => x.from.toLowerCase() === path.toLowerCase());
  return hit ? { to: hit.to, statusCode: hit.statusCode } : null;
}

/** Fetches the structured data of a data block from the owning module (K5); null when unavailable. */
export async function getBlockData(ref: CmsDataRef): Promise<unknown> {
  try {
    const r = await fetch(`${API}${ref.url}`, { next: { revalidate: 120, tags: [CMS_TAG, `data:${ref.source}`] } });
    if (r.ok) return await r.json();
  } catch {
    /* fall through */
  }
  return null;
}
