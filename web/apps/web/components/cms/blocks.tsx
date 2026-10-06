/**
 * Rendering of CMS blocks (FR-CMS-01). Rich text arrives sanitised by the
 * API (allowlisted tags, safe links); data blocks are never copies: the
 * structured data is read live from the owning module's public API (K5),
 * and a missing endpoint or empty answer renders the block's empty text.
 */
import type { CSSProperties, ReactNode } from 'react';
import { RateTable } from '../../app/[lang]/rates';
import type { Rate } from '../../app/lib-p2';
import { getProperty } from '../../app/lib-p2';
import { getBanners, getBlockData, type CmsBanner, type CmsBlock, type CmsMedia } from './api';
import { CmsForm } from './forms';

const text = (v: unknown) => (typeof v === 'string' ? v : '');

/** A responsive image with the generated web sizes. */
export function CmsImage({ m, sizes = '100vw', style }: { m: CmsMedia; sizes?: string; style?: CSSProperties }) {
  const srcSet = [...(m.variants ?? []).map((v) => `${v.url} ${v.width}w`), ...(m.width ? [`${m.url} ${m.width}w`] : [])].join(', ');
  return <img src={m.url} srcSet={srcSet || undefined} sizes={srcSet ? sizes : undefined} alt={m.alt} width={m.width ?? undefined} height={m.height ?? undefined}
    loading="lazy" decoding="async" style={{ maxWidth: '100%', height: 'auto', borderRadius: 12, ...style }} />;
}

/** Link that opens external addresses safely. */
function A({ href, newTab, children, className }: { href: string; newTab?: boolean; children: ReactNode; className?: string }) {
  return <a className={className} href={href} {...(newTab ? { target: '_blank', rel: 'noopener noreferrer' } : {})}>{children}</a>;
}

export function Banners({ banners }: { banners: CmsBanner[] }) {
  if (banners.length === 0) return null;
  return (
    <div className="w-grid">
      {banners.map((b) => (
        <section key={b.id} className="w-card" aria-label={b.title}>
          {b.image && <CmsImage m={b.image} sizes="(max-width: 768px) 100vw, 50vw" />}
          <h2>{b.title}</h2>
          {b.subtitle && <p>{b.subtitle}</p>}
          {b.link && <A className="w-btn" href={b.link.href} newTab={b.link.newTab}>{b.buttonLabel || b.title}</A>}
        </section>
      ))}
    </div>
  );
}

type Item = Record<string, unknown>;

/** Items of a public API answer (list endpoints return {items}). */
function itemsOf(data: unknown): Item[] {
  if (Array.isArray(data)) return data as Item[];
  if (data && typeof data === 'object') {
    const d = data as Record<string, unknown>;
    for (const k of ['items', 'entries', 'rates', 'holes', 'courses', 'types', 'facilities']) if (Array.isArray(d[k])) return d[k] as Item[];
    return [d];
  }
  return [];
}

function money(v: unknown, lang: string) {
  const n = Number(v);
  if (!Number.isFinite(n)) return '';
  return new Intl.NumberFormat(lang === 'id' ? 'id-ID' : 'en-US', { style: 'currency', currency: 'IDR', maximumFractionDigits: 0 }).format(n);
}

/** Generic card of a structured item (package, promotion, event, tournament …). */
function ItemCard({ it, lang }: { it: Item; lang: string }) {
  const title = text(it.name) || text(it.title) || text(it.label) || text(it.code);
  const desc = text(it.description) || text(it.summary) || text(it.shortDescription);
  const price = it.price ?? it.fromPrice ?? it.priceFrom ?? it.total;
  const from = text(it.startDate) || text(it.validFrom) || text(it.date) || text(it.startsAt).slice(0, 10);
  const to = text(it.endDate) || text(it.validUntil) || text(it.validTo) || text(it.endsAt).slice(0, 10);
  const img = text(it.imageUrl) || text((it.image as Item | undefined)?.url);
  return (
    <div className="w-card">
      {img && <img src={img} alt="" loading="lazy" style={{ maxWidth: '100%', borderRadius: 12 }} />}
      <h3 style={{ marginTop: 0 }}>{title}</h3>
      {desc && <p>{desc}</p>}
      {(from || to) && <p className="w-muted">{from}{to && to !== from ? ` – ${to}` : ''}</p>}
      {price !== undefined && price !== null && price !== '' && <p><strong>{money(price, lang)}</strong></p>}
    </div>
  );
}

async function DataBlock({ b, lang }: { b: CmsBlock; lang: string }) {
  const heading = text(b.content.heading);
  const intro = text(b.content.intro);
  const empty = text(b.content.emptyText);
  const data = b.data ? await getBlockData(b.data) : null;
  let items = itemsOf(data).slice(0, b.data?.limit ?? 50);
  if (b.data?.source === 'rates') {
    const rates = items as unknown as Rate[];
    if (rates.length && 'name' in (rates[0] as object)) return <RateTable rates={rates} title={heading || 'Rates'} />;
  }
  if (b.data?.source === 'news' || b.data?.source === 'gallery') {
    items = items.map((x) => ({ ...x, imageUrl: (x.image as Item | undefined)?.url }));
  }
  return (
    <section className="w-card" style={{ gridColumn: '1 / -1' }}>
      {heading && <h2 style={{ marginTop: 0 }}>{heading}</h2>}
      {intro && <p>{intro}</p>}
      {items.length === 0 ? <p className="w-muted">{empty || (lang === 'id' ? 'Belum ada data.' : 'Nothing to show yet.')}</p> : (
        <div className="w-grid">
          {items.map((it, i) => (b.data?.source === 'news' || b.data?.source === 'gallery') && typeof it.path === 'string'
            ? <a key={String(it.id ?? i)} href={it.path} style={{ color: 'inherit', textDecoration: 'none' }}><ItemCard it={it} lang={lang} /></a>
            : <ItemCard key={String(it.id ?? i)} it={it} lang={lang} />)}
        </div>
      )}
    </section>
  );
}

async function BannerSlot({ b, lang }: { b: CmsBlock; lang: string }) {
  return <Banners banners={await getBanners(lang, text(b.config.placement), text(b.config.page))} />;
}

async function FormBlock({ b }: { b: CmsBlock }) {
  const p = await getProperty();
  if (!p || !b.form) return null;
  return <CmsForm propertyId={p.id} action={b.form.action} topic={b.form.topic} line={b.form.line} heading={text(b.content.heading)}
    submitLabel={text(b.content.submitLabel)} successMessage={text(b.content.successMessage)} />;
}

function Block({ b, lang }: { b: CmsBlock; lang: string }) {
  const heading = text(b.content.heading);
  switch (b.type) {
    case 'rich_text':
      return (
        <section className="w-card" style={{ gridColumn: '1 / -1' }}>
          {heading && <h2 style={{ marginTop: 0 }}>{heading}</h2>}
          {/* sanitised by the CMS API (allowlist) */}
          <div className="w-rich" dangerouslySetInnerHTML={{ __html: text(b.content.html) }} />
        </section>
      );
    case 'image': {
      const m = b.media?.[0];
      if (!m) return null;
      const img = <CmsImage m={m} />;
      return (
        <figure style={{ margin: 0, gridColumn: b.config.layout === 'inline' ? undefined : '1 / -1' }}>
          {b.link ? <A href={b.link.href} newTab={b.link.newTab}>{img}</A> : img}
          {m.caption && <figcaption className="w-muted">{m.caption}</figcaption>}
        </figure>
      );
    }
    case 'gallery':
      return (
        <section style={{ gridColumn: '1 / -1' }}>
          {heading && <h2>{heading}</h2>}
          <div className="w-grid">{(b.media ?? []).map((m) => <figure key={m.id} style={{ margin: 0 }}><CmsImage m={m} sizes="(max-width: 768px) 100vw, 33vw" />
            {m.caption && <figcaption className="w-muted">{m.caption}</figcaption>}</figure>)}</div>
        </section>
      );
    case 'video':
      if (!b.embedUrl) return null;
      return (
        <section style={{ gridColumn: '1 / -1' }}>
          {heading && <h2>{heading}</h2>}
          <div style={{ position: 'relative', paddingTop: '56.25%' }}>
            <iframe src={b.embedUrl} title={heading || 'Video'} loading="lazy" allow="encrypted-media; picture-in-picture" allowFullScreen
              referrerPolicy="strict-origin-when-cross-origin" sandbox="allow-scripts allow-same-origin allow-presentation"
              style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', border: 0, borderRadius: 12 }} />
          </div>
          {text(b.content.caption) && <p className="w-muted">{text(b.content.caption)}</p>}
        </section>
      );
    case 'cta':
      if (!b.link) return null;
      return (
        <section className="w-card">
          {b.media?.[0] && <CmsImage m={b.media[0]} sizes="(max-width: 768px) 100vw, 50vw" />}
          {heading && <h2 style={{ marginTop: 0 }}>{heading}</h2>}
          {text(b.content.text) && <p>{text(b.content.text)}</p>}
          <A className={b.config.style === 'secondary' ? 'w-btn w-btn-ghost' : 'w-btn'} href={b.link.href} newTab={b.link.newTab}>{text(b.content.label)}</A>
        </section>
      );
    case 'faq': {
      const items = (b.content.items ?? []) as { question: string; answer: string }[];
      return (
        <section className="w-card" style={{ gridColumn: '1 / -1' }}>
          {heading && <h2 style={{ marginTop: 0 }}>{heading}</h2>}
          {items.map((q, i) => (
            <details key={i}>
              <summary>{q.question}</summary>
              <div dangerouslySetInnerHTML={{ __html: q.answer }} />
            </details>
          ))}
        </section>
      );
    }
    case 'map': {
      const lat = Number(b.map?.latitude);
      const lng = Number(b.map?.longitude);
      if (!Number.isFinite(lat) || !Number.isFinite(lng)) return null;
      const d = 0.01;
      const src = `https://www.openstreetmap.org/export/embed.html?bbox=${lng - d},${lat - d},${lng + d},${lat + d}&layer=mapnik&marker=${lat},${lng}`;
      return (
        <section className="w-card" style={{ gridColumn: '1 / -1' }}>
          {heading && <h2 style={{ marginTop: 0 }}>{heading}</h2>}
          <iframe src={src} title={heading || 'Map'} loading="lazy" style={{ width: '100%', height: 360, border: 0, borderRadius: 12 }} />
          <p><a href={text(b.map?.mapUrl) || `https://www.google.com/maps?q=${lat},${lng}`} target="_blank" rel="noopener noreferrer">
            {lang === 'id' ? 'Buka peta' : 'Open map'}</a></p>
        </section>
      );
    }
    case 'data':
      return <DataBlock b={b} lang={lang} />;
    case 'banner_slot':
      return <BannerSlot b={b} lang={lang} />;
    case 'contact_form':
      return <FormBlock b={b} />;
  }
  return null;
}

/** Renders the visible blocks of a page / article in a grid. */
export function Blocks({ blocks, lang }: { blocks: CmsBlock[]; lang: string }) {
  return <div className="w-grid">{blocks.map((b) => <Block key={b.id} b={b} lang={lang} />)}</div>;
}
