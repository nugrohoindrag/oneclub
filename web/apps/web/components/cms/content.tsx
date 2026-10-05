/** A rendered CMS page, news article or gallery album with SEO (FR-CMS-08). */
import type { Metadata } from 'next';
import type { CmsContent } from './api';
import { Blocks, CmsImage } from './blocks';

/** Metadata of a CMS content: title, description, canonical, hreflang alternates, Open Graph, robots. */
export function cmsMetadata(c: CmsContent | null): Metadata {
  if (!c) return {};
  const languages: Record<string, string> = {};
  for (const a of c.seo.alternates) languages[a.language] = a.href;
  return {
    title: c.seo.title,
    description: c.seo.description || undefined,
    alternates: { canonical: c.seo.canonical || undefined, languages },
    openGraph: { title: c.seo.title, description: c.seo.description || undefined, url: c.seo.canonical || undefined, locale: c.language,
      type: c.kind === 'article' ? 'article' : 'website', images: c.seo.ogImage ? [c.seo.ogImage] : undefined },
    robots: c.seo.noindex ? { index: false, follow: false } : undefined,
  };
}

export function CmsView({ c, lang }: { c: CmsContent; lang: string }) {
  return (
    <article className="w-stack" lang={c.language}>
      {c.preview && <p className="w-card" role="status">{lang === 'id' ? 'Pratinjau' : 'Preview'} · v{c.versionNo}</p>}
      {c.breadcrumbs.length > 1 && (
        <nav aria-label="Breadcrumb" className="w-muted" style={{ margin: '0 0 12px' }}>
          {c.breadcrumbs.map((b, i) => <span key={b.path}>{i > 0 && ' › '}{i < c.breadcrumbs.length - 1 ? <a href={b.path}>{b.title}</a> : b.title}</span>)}
        </nav>
      )}
      <header className="w-card" style={{ marginBottom: 16 }}>
        <h1 style={{ marginTop: 0 }}>{c.title}</h1>
        {(c.date || c.category) && <p className="w-muted">{[c.date, c.category?.label, c.authorName].filter(Boolean).join(' · ')}</p>}
        {c.summary && <p style={{ fontSize: 18 }}>{c.summary}</p>}
        {c.fallback && <p className="w-muted">{lang === 'id' ? 'Halaman ini belum tersedia dalam Bahasa Indonesia.' : 'This page is not available in English yet.'}</p>}
        {c.image && <CmsImage m={c.image} />}
      </header>
      <Blocks blocks={c.blocks} lang={lang} />
      {c.items && c.items.length > 0 && (
        <div className="w-grid">
          {c.items.map((it) => <figure key={it.media.id} style={{ margin: 0 }}><CmsImage m={it.media} sizes="(max-width: 768px) 100vw, 33vw" />
            {it.caption && <figcaption className="w-muted">{it.caption}</figcaption>}</figure>)}
        </div>
      )}
      {c.tags && c.tags.length > 0 && <p className="w-muted">{c.tags.map((t) => `#${t}`).join(' ')}</p>}
      <script type="application/ld+json" dangerouslySetInnerHTML={{ __html: JSON.stringify(c.structuredData).replace(/</g, '\\u003c') }} />
    </article>
  );
}
