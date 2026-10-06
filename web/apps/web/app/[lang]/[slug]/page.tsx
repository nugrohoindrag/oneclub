import type { Metadata } from 'next';
import { notFound, permanentRedirect, redirect } from 'next/navigation';
import { findRedirect, getPage } from '../../../components/cms/api';
import { CmsView, cmsMetadata } from '../../../components/cms/content';

type Props = { params: Promise<{ lang: string; slug: string }> };

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  const { lang, slug } = await params;
  return cmsMetadata(await getPage(slug, lang));
}

/** A page managed in the CMS (FR-CMS-01), by slug or key; old URLs follow their redirect. */
export default async function CmsPage({ params }: Props) {
  const { lang, slug } = await params;
  const c = await getPage(slug, lang);
  if (!c) {
    const r = await findRedirect(`/${lang}/${slug}`);
    if (r) {
      if (r.statusCode === 301 || r.statusCode === 308) permanentRedirect(r.to);
      redirect(r.to);
    }
    notFound();
  }
  return <CmsView c={c} lang={lang} />;
}
