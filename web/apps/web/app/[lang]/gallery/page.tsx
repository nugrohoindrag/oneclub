import type { Metadata } from 'next';
import { getAlbums } from '../../../components/cms/api';
import { CmsImage } from '../../../components/cms/blocks';

type Props = { params: Promise<{ lang: string }>; searchParams: Promise<{ cursor?: string }> };

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  const { lang } = await params;
  return { title: lang === 'id' ? 'Galeri' : 'Gallery', alternates: { languages: { id: '/id/gallery', en: '/en/gallery' } } };
}

/** Gallery (FR-CMS-04): photo albums. */
export default async function Gallery({ params, searchParams }: Props) {
  const { lang } = await params;
  const { cursor = '' } = await searchParams;
  const albums = await getAlbums(lang, cursor);
  return (
    <div className="w-stack">
      <div className="w-card"><h1 style={{ margin: 0 }}>{lang === 'id' ? 'Galeri' : 'Gallery'}</h1></div>
      {(albums?.items ?? []).length === 0 && <p className="w-muted">{lang === 'id' ? 'Belum ada album.' : 'No albums yet.'}</p>}
      <div className="w-grid">
        {(albums?.items ?? []).map((a) => (
          <a key={a.id} className="w-card" href={a.path} style={{ color: 'inherit', textDecoration: 'none' }}>
            {a.image && <CmsImage m={a.image} sizes="(max-width: 768px) 100vw, 33vw" />}
            <h2>{a.title}</h2>
            <p className="w-muted">{a.imageCount ?? 0} {lang === 'id' ? 'foto' : 'photos'}</p>
          </a>
        ))}
      </div>
      {albums?.nextCursor && (
        <p><a className="w-btn w-btn-ghost" href={`?cursor=${encodeURIComponent(albums.nextCursor)}`}>{lang === 'id' ? 'Album lainnya' : 'More albums'}</a></p>
      )}
    </div>
  );
}
