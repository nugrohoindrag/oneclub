import type { Metadata } from 'next';
import { notFound } from 'next/navigation';
import { getArticle } from '../../../../components/cms/api';
import { CmsView, cmsMetadata } from '../../../../components/cms/content';

type Props = { params: Promise<{ lang: string; slug: string }> };

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  const { lang, slug } = await params;
  return cmsMetadata(await getArticle(slug, lang));
}

/** A news article (FR-CMS-04). */
export default async function Article({ params }: Props) {
  const { lang, slug } = await params;
  const c = await getArticle(slug, lang);
  if (!c) notFound();
  return <CmsView c={c} lang={lang} />;
}
