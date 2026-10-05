import type { Metadata } from 'next';
import { notFound } from 'next/navigation';
import { getAlbum } from '../../../../components/cms/api';
import { CmsView, cmsMetadata } from '../../../../components/cms/content';

type Props = { params: Promise<{ lang: string; slug: string }> };

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  const { lang, slug } = await params;
  return cmsMetadata(await getAlbum(slug, lang));
}

/** A gallery album with its images and captions. */
export default async function Album({ params }: Props) {
  const { lang, slug } = await params;
  const c = await getAlbum(slug, lang);
  if (!c) notFound();
  return <CmsView c={c} lang={lang} />;
}
