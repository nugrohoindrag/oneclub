import type { MetadataRoute } from 'next';
import { getRobots } from '../components/cms/api';
import { absolute, disallowed, siteOrigin } from './seo';

/**
 * /robots.txt (PRD P4 FR-CMS-08): Content Policies decide whether search
 * engines may index the site (switched off on staging) and add paths; the
 * private pages (payment, quotation, supplier, unsubscribe … links) are
 * never crawled.
 */
export default async function robots(): Promise<MetadataRoute.Robots> {
  const origin = await siteOrigin();
  const cms = await getRobots();
  const sitemap = absolute(origin, '/sitemap.xml');
  if (cms && !cms.allowIndexing) return { rules: { userAgent: '*', disallow: '/' }, sitemap };
  return { rules: { userAgent: '*', allow: '/', disallow: disallowed(cms?.disallow ?? []) }, sitemap };
}
