import type { Metadata } from 'next';
import { getNews } from '../../../components/cms/api';
import { CmsImage } from '../../../components/cms/blocks';

type Props = { params: Promise<{ lang: string }>; searchParams: Promise<{ category?: string; tag?: string; cursor?: string }> };

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  const { lang } = await params;
  return { title: lang === 'id' ? 'Berita' : 'News', alternates: { languages: { id: '/id/news', en: '/en/news' } } };
}

/** News (FR-CMS-04): latest articles, by category or tag. */
export default async function News({ params, searchParams }: Props) {
  const { lang } = await params;
  const { category = '', tag = '', cursor = '' } = await searchParams;
  const news = await getNews(lang, { ...(category ? { category } : {}), ...(tag ? { tag } : {}), ...(cursor ? { cursor } : {}) });
  const more = new URLSearchParams({ ...(category ? { category } : {}), ...(tag ? { tag } : {}), cursor: news?.nextCursor ?? '' }).toString();
  return (
    <div className="w-stack">
      <div className="w-card">
        <h1 style={{ marginTop: 0 }}>{lang === 'id' ? 'Berita' : 'News'}</h1>
        <nav aria-label={lang === 'id' ? 'Kategori' : 'Categories'} style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
          <a href={`/${lang}/news`} aria-current={!category ? 'page' : undefined}>{lang === 'id' ? 'Semua' : 'All'}</a>
          {(news?.categories ?? []).map((c) => (
            <a key={c.code} href={`/${lang}/news?category=${encodeURIComponent(c.code.toLowerCase())}`}
              aria-current={category.toUpperCase() === c.code ? 'page' : undefined}>{c.label}</a>
          ))}
        </nav>
      </div>
      {(news?.items ?? []).length === 0 && <p className="w-muted">{lang === 'id' ? 'Belum ada berita.' : 'No news yet.'}</p>}
      <div className="w-grid">
        {(news?.items ?? []).map((a) => (
          <a key={a.id} className="w-card" href={a.path} style={{ color: 'inherit', textDecoration: 'none' }}>
            {a.image && <CmsImage m={a.image} sizes="(max-width: 768px) 100vw, 33vw" />}
            <h2>{a.title}</h2>
            <p className="w-muted">{[a.date, a.category?.label].filter(Boolean).join(' · ')}</p>
            {a.summary && <p>{a.summary}</p>}
          </a>
        ))}
      </div>
      {news?.nextCursor && <p><a className="w-btn w-btn-ghost" href={`?${more}`}>{lang === 'id' ? 'Berita lainnya' : 'More news'}</a></p>}
    </div>
  );
}
