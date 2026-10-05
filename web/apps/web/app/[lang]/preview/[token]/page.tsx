import type { Metadata } from 'next';
import { notFound } from 'next/navigation';
import { getPreview } from '../../../../components/cms/api';
import { CmsView } from '../../../../components/cms/content';

export const dynamic = 'force-dynamic';
export const metadata: Metadata = { robots: { index: false, follow: false } };

/** Preview of any version through a signed, expiring link (FR-CMS-06). */
export default async function Preview({ params }: { params: Promise<{ lang: string; token: string }> }) {
  const { lang, token } = await params;
  const c = await getPreview(token);
  if (!c) notFound();
  return <CmsView c={c} lang={lang} />;
}
