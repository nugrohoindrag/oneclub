import React, { useEffect, useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router';
import { qs, request, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDateTime } from '@oneclub/i18n';
import {
  AutoResourcePage, Card, Checkbox, DataTable, Drawer, Empty, ErrorAlert, Modal, PageHeader, SelectField, Skeleton, StatusPill, TextArea, TextField,
  useAuth, useToast, type Option,
} from '@oneclub/shell';
import { ActionButton, KV, ListPage, Tabs, type R } from '../p1/common';
import type { AreaRoute, OpsRoute, OpsTile } from '../p3/types';

// Landing Page & CMS (PRD P4 EP-24, Naming Convention §26 CMS menu): Pages,
// Banners, Images, Packages / Promotions / Pricing / Events (structured data
// shown by data blocks, owned by their modules), News, Gallery, Course
// Guide, Contact Information and Languages; plus navigation menus,
// redirects and the publishing desk (schedule, log, cache purge, import).

const CMS = ['/api/v1/cms', '/api/v1/public/cms'];
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const opts = (vals: string[], labels: Record<string, string> = {}): Option[] => vals.map((v) => ({ value: v, label: labels[v] ?? label(v) }));
const STATUSES = opts(['draft', 'in_review', 'scheduled', 'published', 'unpublished'], { in_review: 'In Review' });
const TEMPLATES = opts(['home', 'standard', 'landing', 'golf', 'course_guide', 'sport_club', 'bungalow', 'vip_suite', 'meeting_mice', 'wedding_banquet', 'events',
  'membership', 'packages', 'promotions', 'hall_of_fame', 'news', 'gallery', 'contact', 'location']);
const PLACEMENTS = opts(['home_hero', 'home_highlight', 'page_header', 'page_inline', 'sidebar', 'popup', 'announcement_bar', 'footer']);
const ROUTE_KEYS = opts(['home', 'golf', 'course_guide', 'hole_by_hole', 'handicap', 'facilities', 'reciprocal_clubs', 'sport_club', 'bungalow', 'vip_suite',
  'meeting_mice', 'wedding_banquet', 'events', 'tournaments', 'membership', 'packages', 'promotions', 'hall_of_fame', 'news', 'gallery', 'contact', 'location', 'book_golf',
  'book_sport_club', 'book_bungalow', 'book_meeting_room', 'book_event', 'member_portal']);
const BLOCK_TYPES = opts(['rich_text', 'image', 'gallery', 'video', 'cta', 'faq', 'contact_form', 'map', 'data', 'banner_slot'],
  { rich_text: 'Text', cta: 'Call to action', faq: 'FAQ', data: 'Structured data (rates, packages …)' });
const toLocalInput = (v: unknown) => {
  if (!v) return '';
  const d = new Date(String(v));
  return new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
};
const fromLocalInput = (v: string) => (v ? new Date(v).toISOString() : undefined);

type Kind = { kind: string; path: string; name: string; plural: string; perm: string; help: string; slug: boolean; blocks: boolean; items: boolean };
const KINDS: Record<string, Kind> = {
  page: { kind: 'page', path: '/api/v1/cms/pages', name: 'Page', plural: 'Pages', perm: 'cms.page', slug: true, blocks: true, items: false,
    help: 'Website pages built from blocks: text, images, galleries, calls to action, maps and structured data (rates, packages, promotions, events, hall of fame) that always follow the system.' },
  article: { kind: 'article', path: '/api/v1/cms/articles', name: 'News Article', plural: 'News', perm: 'cms.article', slug: true, blocks: true, items: false,
    help: 'News and articles with categories, tags, featured image and the publish date shown on the website.' },
  banner: { kind: 'banner', path: '/api/v1/cms/banners', name: 'Banner', plural: 'Banners', perm: 'cms.banner', slug: false, blocks: false, items: false,
    help: 'Banners per placement and target page, shown in their display period and taken down automatically at its end.' },
  gallery: { kind: 'gallery', path: '/api/v1/cms/galleries', name: 'Gallery Album', plural: 'Gallery', perm: 'cms.gallery', slug: true, blocks: false, items: true,
    help: 'Photo albums with captions per language.' },
};

/** Website languages (Content Policies). */
function useLanguages() {
  const l = useGet<R & { defaultLanguage: string; languages: R[]; requireApproval: boolean }>('/api/v1/cms/languages');
  const langs = (l.data?.languages ?? []).map((x) => String(x.code));
  return { langs: langs.length ? langs : ['id', 'en'], def: l.data?.defaultLanguage ?? 'id', requireApproval: l.data?.requireApproval ?? true };
}

/** Images of the library as options. */
function useMediaOptions(): Option[] {
  const m = useGet<Page<R>>('/api/v1/cms/media?limit=200');
  return (m.data?.items ?? []).map((x) => ({ value: x.id, label: String(x.filename) }));
}

function TranslationPills({ r }: { r: R }) {
  const tr = (r.translations ?? {}) as Record<string, { status: string }>;
  return (
    <span className="oc-row-wrap">
      {Object.entries(tr).map(([lang, s]) => <StatusPill key={lang} status={s.status === 'complete' ? 'active' : s.status === 'missing' ? 'inactive' : 'pending'}
        label={`${lang.toUpperCase()} ${label(s.status)}`} />)}
    </span>
  );
}

// ── content lists ─────────────────────────────────────────────────────────

export function ContentListPage({ kind }: { kind: string }) {
  const k = KINDS[kind];
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const [adding, setAdding] = useState(false);
  const [extra, setExtra] = useState('');
  const open = params.get('id');
  const filterKey = kind === 'page' ? 'filter[template]' : kind === 'banner' ? 'filter[placement]' : '';
  return (
    <>
      <ListPage title={k.plural} help={k.help} path={k.path} statuses={STATUSES} extraQuery={filterKey ? { [filterKey]: extra } : undefined}
        filters={kind === 'page' ? <SelectField label="Template" value={extra} onChange={setExtra} placeholder="All templates" options={TEMPLATES} />
          : kind === 'banner' ? <SelectField label="Placement" value={extra} onChange={setExtra} placeholder="All placements" options={PLACEMENTS} /> : undefined}
        actions={can(`${k.perm}.create`) ? <button className="oc-btn oc-btn-primary" onClick={() => setAdding(true)}>Add {k.name}</button> : undefined}
        onRowClick={(r) => setParams({ id: r.id })}
        columns={[
          { key: 'title', header: 'Title', render: (r) => <strong>{String(r.title)}</strong> },
          { key: 'detail', header: kind === 'page' ? 'Template' : kind === 'banner' ? 'Placement' : kind === 'article' ? 'Date' : 'Key',
            render: (r) => label(kind === 'page' ? r.template : kind === 'banner' ? r.placement : kind === 'article' ? r.displayDate ?? r.firstPublishedAt?.toString().slice(0, 10) : r.key) },
          { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> },
          { key: 'live', header: 'Website', render: (r) => (r.live ? `Live v${String(r.publishedVersion)}` : '—') + (r.hasUnpublishedChanges && r.live ? ' · changes' : '') },
          { key: 'translations', header: 'Languages', render: (r) => <TranslationPills r={r} /> },
          { key: 'updatedAt', header: 'Updated', render: (r) => formatDateTime(String(r.updatedAt)) },
        ]} />
      <NewContentModal k={k} open={adding} onClose={() => setAdding(false)} onCreated={(id) => setParams({ id })} />
      <Drawer open={!!open} onClose={() => setParams({})} title={k.name}>
        {open && <ContentEditor k={k} id={open} />}
      </Drawer>
    </>
  );
}

function NewContentModal({ k, open, onClose, onCreated }: { k: Kind; open: boolean; onClose: () => void; onCreated: (id: string) => void }) {
  const { def } = useLanguages();
  const [title, setTitle] = useState('');
  const [template, setTemplate] = useState('standard');
  const [placement, setPlacement] = useState('home_hero');
  const send = useSend<Record<string, unknown>, R>('POST', k.path, CMS);
  useEffect(() => {
    if (open) setTitle('');
  }, [open]);
  const body: Record<string, unknown> = { translations: { [def]: { title } } };
  if (k.kind === 'page') body.template = template;
  if (k.kind === 'banner') body.placement = placement;
  return (
    <Modal open={open} onClose={onClose} title={`Add ${k.name}`}
      actions={<>
        <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
        <button className="oc-btn oc-btn-primary" disabled={!title.trim() || send.isPending}
          onClick={() => send.mutate(body, { onSuccess: (r) => { onClose(); onCreated(r.id); } })}>Create draft</button>
      </>}>
      <div className="oc-stack">
        <TextField label={`Title (${def.toUpperCase()})`} value={title} onChange={setTitle} required />
        {k.kind === 'page' && <SelectField label="Template" value={template} onChange={setTemplate} options={TEMPLATES} />}
        {k.kind === 'banner' && <SelectField label="Placement" value={placement} onChange={setPlacement} options={PLACEMENTS} />}
        <ErrorAlert error={send.error} />
      </div>
    </Modal>
  );
}

// ── editor ────────────────────────────────────────────────────────────────

type Doc = { translations: Record<string, R>; blocks?: Block[]; items?: { mediaId: string; caption?: Record<string, string> }[] };
type Block = { id?: string; type: string; config?: Record<string, unknown>; content?: Record<string, Record<string, unknown>>; hidden?: boolean };

function ContentEditor({ k, id }: { k: Kind; id: string }) {
  const [tab, setTab] = useState('content');
  const d = useGet<R & { document: Doc; latestVersion: number }>(`${k.path}/${id}`);
  if (d.isLoading) return <Skeleton rows={6} />;
  if (d.error || !d.data) return <ErrorAlert error={d.error} />;
  const c = d.data;
  return (
    <div className="oc-stack">
      <KV items={[
        ['Status', <StatusPill key="s" status={String(c.status)} />],
        ['Website', c.live ? `Live (version ${String(c.publishedVersion)})` : 'Not on the website'],
        ['Latest version', String(c.latestVersion) + (c.hasUnpublishedChanges ? ' · not published yet' : '')],
        ['Go-live', c.publishAt ? formatDateTime(String(c.publishAt)) : '—'],
        ['Take-down', c.unpublishAt ? formatDateTime(String(c.unpublishAt)) : '—'],
        ['Languages', <TranslationPills key="t" r={c} />],
      ]} />
      <Tabs tabs={opts(['content', 'settings', 'publishing', 'versions'])} value={tab} onChange={setTab} />
      {tab === 'content' && <ContentForm k={k} c={c} />}
      {tab === 'settings' && <SettingsForm k={k} c={c} />}
      {tab === 'publishing' && <PublishingPanel k={k} c={c} />}
      {tab === 'versions' && <VersionsPanel k={k} c={c} />}
    </div>
  );
}

function ContentForm({ k, c }: { k: Kind; c: R & { document: Doc; latestVersion: number } }) {
  const { langs, def } = useLanguages();
  const media = useMediaOptions();
  const toast = useToast();
  const [doc, setDoc] = useState<Doc>(() => JSON.parse(JSON.stringify(c.document)) as Doc);
  const [lang, setLang] = useState(def);
  const [note, setNote] = useState('');
  const save = useSend<Record<string, unknown>>('PUT', `${k.path}/${c.id}/content`, CMS, () => ({ 'If-Match': `"${c.latestVersion}"` }));
  const tr = (doc.translations[lang] ?? {}) as Record<string, unknown>;
  const seo = (tr.seo ?? {}) as Record<string, unknown>;
  const setTr = (key: string, v: unknown) => setDoc((d) => ({ ...d, translations: { ...d.translations, [lang]: { ...(d.translations[lang] ?? {}), [key]: v } as R } }));
  const setSeo = (key: string, v: unknown) => setTr('seo', { ...seo, [key]: v === '' ? undefined : v });
  const disabled = c.status === 'in_review';
  return (
    <div className="oc-stack">
      <Tabs tabs={langs.map((l) => ({ value: l, label: `${l.toUpperCase()}${l === def ? ' (default)' : ''}` }))} value={lang} onChange={setLang} />
      <div className="oc-form-grid">
        <TextField label="Title" value={String(tr.title ?? '')} onChange={(v) => setTr('title', v)} required={lang === def} />
        {k.slug && <TextField label="Slug (URL)" value={String(tr.slug ?? '')} onChange={(v) => setTr('slug', v)} help="Generated from the title when empty" />}
        <TextArea label={k.kind === 'banner' ? 'Subtitle' : k.kind === 'article' ? 'Excerpt' : 'Summary'} value={String(tr.summary ?? '')} onChange={(v) => setTr('summary', v)} rows={2} span />
        {k.kind === 'banner' && <TextField label="Button label" value={String(tr.buttonLabel ?? '')} onChange={(v) => setTr('buttonLabel', v)} />}
        {k.kind !== 'page' && <SelectField label={k.kind === 'banner' ? 'Banner image' : k.kind === 'gallery' ? 'Cover image' : 'Featured image'}
          value={String(tr.mediaId ?? '')} onChange={(v) => setTr('mediaId', v || undefined)} options={media} placeholder="None" />}
        {k.kind !== 'page' && <TextField label="Alternative text" value={String(tr.alt ?? '')} onChange={(v) => setTr('alt', v)} />}
      </div>
      {k.kind !== 'banner' && (
        <Card title="SEO" icon="travel_explore">
          <div className="oc-form-grid">
            <TextField label="Meta title" value={String(seo.metaTitle ?? '')} onChange={(v) => setSeo('metaTitle', v)} maxLength={120} />
            <TextField label="Canonical URL" value={String(seo.canonicalUrl ?? '')} onChange={(v) => setSeo('canonicalUrl', v)} />
            <TextArea label="Meta description" value={String(seo.metaDescription ?? '')} onChange={(v) => setSeo('metaDescription', v)} rows={2} span />
            <SelectField label="Open Graph image" value={String(seo.ogImageId ?? '')} onChange={(v) => setSeo('ogImageId', v)} options={media} placeholder="Default" />
            <Checkbox label="Hide from search engines (noindex)" checked={seo.noindex === true} onChange={(v) => setSeo('noindex', v || undefined)} />
          </div>
        </Card>
      )}
      {k.blocks && <BlocksEditor blocks={doc.blocks ?? []} lang={lang} def={def} media={media} onChange={(blocks) => setDoc((d) => ({ ...d, blocks }))} />}
      {k.items && <AlbumItems items={doc.items ?? []} lang={lang} media={media} onChange={(items) => setDoc((d) => ({ ...d, items }))} />}
      <TextField label="Version note" value={note} onChange={setNote} />
      <ErrorAlert error={save.error} />
      <div className="oc-row-wrap">
        <button className="oc-btn oc-btn-primary" disabled={disabled || save.isPending}
          onClick={() => save.mutate({ translations: doc.translations, ...(k.blocks ? { blocks: doc.blocks ?? [] } : {}), ...(k.items ? { items: doc.items ?? [] } : {}), note },
            { onSuccess: () => toast('Saved as a new version') })}>Save version</button>
        {disabled && <span className="oc-muted">In review: withdraw the review to edit.</span>}
      </div>
    </div>
  );
}

function AlbumItems({ items, lang, media, onChange }: { items: NonNullable<Doc['items']>; lang: string; media: Option[]; onChange: (v: NonNullable<Doc['items']>) => void }) {
  const [add, setAdd] = useState('');
  return (
    <Card title="Album images" icon="photo_library">
      <div className="oc-stack">
        {items.map((it, i) => (
          <div key={`${it.mediaId}-${i}`} className="oc-row-wrap">
            <span style={{ minWidth: 160 }}>{media.find((m) => m.value === it.mediaId)?.label ?? it.mediaId}</span>
            <TextField label={`Caption (${lang.toUpperCase()})`} value={it.caption?.[lang] ?? ''}
              onChange={(v) => onChange(items.map((x, j) => (j === i ? { ...x, caption: { ...(x.caption ?? {}), [lang]: v } } : x)))} />
            <button className="oc-btn oc-btn-sm oc-btn-text" onClick={() => onChange(items.filter((_, j) => j !== i))}>Remove</button>
          </div>
        ))}
        <div className="oc-row-wrap">
          <SelectField label="Add image" value={add} onChange={setAdd} options={media} placeholder="Choose" />
          <button className="oc-btn oc-btn-sm oc-btn-neutral" disabled={!add} onClick={() => { onChange([...items, { mediaId: add }]); setAdd(''); }}>Add</button>
        </div>
      </div>
    </Card>
  );
}

/** Block editor: typed fields for common blocks, JSON for the rest. */
function BlocksEditor({ blocks, lang, def, media, onChange }: { blocks: Block[]; lang: string; def: string; media: Option[]; onChange: (b: Block[]) => void }) {
  const [type, setType] = useState('rich_text');
  const set = (i: number, b: Block) => onChange(blocks.map((x, j) => (j === i ? b : x)));
  const move = (i: number, d: number) => {
    const j = i + d;
    if (j < 0 || j >= blocks.length) return;
    const next = [...blocks];
    [next[i], next[j]] = [next[j], next[i]];
    onChange(next);
  };
  return (
    <Card title="Blocks" icon="view_agenda">
      <div className="oc-stack">
        {blocks.length === 0 && <Empty title="No blocks yet" help="Add text, images or structured data blocks." />}
        {blocks.map((b, i) => (
          <div key={b.id ?? i} className="oc-card" style={{ padding: 12 }}>
            <div className="oc-row-wrap">
              <strong>{BLOCK_TYPES.find((t) => t.value === b.type)?.label ?? b.type}</strong>
              <span className="oc-spacer" />
              <Checkbox label="Hidden" checked={b.hidden === true} onChange={(v) => set(i, { ...b, hidden: v || undefined })} />
              <button className="oc-btn oc-btn-sm oc-btn-text" aria-label="Move up" onClick={() => move(i, -1)}>↑</button>
              <button className="oc-btn oc-btn-sm oc-btn-text" aria-label="Move down" onClick={() => move(i, 1)}>↓</button>
              <button className="oc-btn oc-btn-sm oc-btn-text" onClick={() => onChange(blocks.filter((_, j) => j !== i))}>Remove</button>
            </div>
            <BlockFields b={b} lang={lang} def={def} media={media} onChange={(nb) => set(i, nb)} />
          </div>
        ))}
        <div className="oc-row-wrap">
          <SelectField label="New block" value={type} onChange={setType} options={BLOCK_TYPES} />
          <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => onChange([...blocks, { type, config: type === 'data' ? { source: 'rates' } : {}, content: {} }])}>Add block</button>
        </div>
      </div>
    </Card>
  );
}

function BlockFields({ b, lang, def, media, onChange }: { b: Block; lang: string; def: string; media: Option[]; onChange: (b: Block) => void }) {
  const sources = useGet<Page<R>>(b.type === 'data' ? '/api/v1/cms/data-sources' : null);
  const cfg = b.config ?? {};
  const txt = (b.content?.[lang] ?? {}) as Record<string, unknown>;
  const setCfg = (key: string, v: unknown) => onChange({ ...b, config: { ...cfg, [key]: v === '' ? undefined : v } });
  const setTxt = (key: string, v: unknown) => onChange({ ...b, content: { ...(b.content ?? {}), [lang]: { ...txt, [key]: v === '' ? undefined : v } } });
  const heading = <TextField label={`Heading (${lang.toUpperCase()})`} value={String(txt.heading ?? '')} onChange={(v) => setTxt('heading', v)} />;
  switch (b.type) {
    case 'rich_text':
      return <div className="oc-stack">{heading}<TextArea label={`Text (${lang.toUpperCase()}, HTML: p, b, i, a, lists, tables)`} value={String(txt.html ?? '')}
        onChange={(v) => setTxt('html', v)} rows={6} /></div>;
    case 'image':
      return <div className="oc-form-grid">
        <SelectField label="Image" value={String(cfg.mediaId ?? '')} onChange={(v) => setCfg('mediaId', v)} options={media} placeholder="Choose" required />
        <SelectField label="Layout" value={String(cfg.layout ?? '')} onChange={(v) => setCfg('layout', v)} options={opts(['full', 'wide', 'inline'])} placeholder="Default" />
        <TextField label={`Caption (${lang.toUpperCase()})`} value={String(txt.caption ?? '')} onChange={(v) => setTxt('caption', v)} />
        <TextField label={`Alternative text (${lang.toUpperCase()})`} value={String(txt.alt ?? '')} onChange={(v) => setTxt('alt', v)} />
      </div>;
    case 'video':
      return <div className="oc-form-grid">{heading}<TextField label="YouTube or Vimeo URL" value={String(cfg.url ?? '')} onChange={(v) => setCfg('url', v)} required /></div>;
    case 'cta': {
      const link = (cfg.link ?? { type: 'route' }) as Record<string, unknown>;
      return <div className="oc-form-grid">
        {heading}
        <TextField label={`Button label (${lang.toUpperCase()})`} value={String(txt.label ?? '')} onChange={(v) => setTxt('label', v)} required={lang === def} />
        <SelectField label="Link to" value={String(link.type ?? 'route')} onChange={(v) => setCfg('link', { type: v })} options={opts(['route', 'url'], { route: 'Website page / booking', url: 'Address' })} />
        {link.type === 'url' ? <TextField label="Address" value={String(link.url ?? '')} onChange={(v) => setCfg('link', { type: 'url', url: v })} />
          : <SelectField label="Website page" value={String(link.routeKey ?? '')} onChange={(v) => setCfg('link', { type: 'route', routeKey: v })} options={ROUTE_KEYS} placeholder="Choose" />}
        <SelectField label="Style" value={String(cfg.style ?? '')} onChange={(v) => setCfg('style', v)} options={opts(['primary', 'secondary', 'link'])} placeholder="Default" />
      </div>;
    }
    case 'data': {
      const src = (sources.data?.items ?? []).find((s) => s.key === cfg.source);
      const filter = (cfg.filter ?? {}) as Record<string, unknown>;
      return <div className="oc-stack">
        <div className="oc-form-grid">
          <SelectField label="Structured data" value={String(cfg.source ?? '')} onChange={(v) => onChange({ ...b, config: { source: v } })}
            options={(sources.data?.items ?? []).map((s) => ({ value: String(s.key), label: String(s.label) }))} />
          <TextField label="Number of items" type="number" value={String(cfg.limit ?? '')} onChange={(v) => setCfg('limit', v ? Number(v) : '')} />
          <SelectField label="Layout" value={String(cfg.layout ?? '')} onChange={(v) => setCfg('layout', v)} options={opts(['grid', 'list', 'table', 'carousel', 'calendar', 'widget'])} placeholder="Default" />
          {((src?.filters ?? []) as R[]).map((f) => (
            f.type === 'enum'
              ? <SelectField key={String(f.key)} label={String(f.label)} value={String(filter[String(f.key)] ?? '')} required={f.required === true}
                onChange={(v) => setCfg('filter', { ...filter, [String(f.key)]: v || undefined })} options={opts((f.enum ?? []) as string[])} placeholder="Any" />
              : <TextField key={String(f.key)} label={String(f.label)} value={String(filter[String(f.key)] ?? '')} type={f.type === 'date' ? 'date' : 'text'}
                onChange={(v) => setCfg('filter', { ...filter, [String(f.key)]: v || undefined })} />
          ))}
          {heading}
          <TextField label={`Text when empty (${lang.toUpperCase()})`} value={String(txt.emptyText ?? '')} onChange={(v) => setTxt('emptyText', v)} />
        </div>
        {src && <p className="oc-muted" style={{ margin: 0 }}>{String(src.description ?? '')} Data stays in {label(src.owner)}; the website reads {String(src.endpoint)}.</p>}
      </div>;
    }
    case 'banner_slot':
      return <SelectField label="Placement" value={String(cfg.placement ?? '')} onChange={(v) => setCfg('placement', v)} options={PLACEMENTS} placeholder="Choose" required />;
    default:
      return <JSONBlock b={b} onChange={onChange} />;
  }
}

/** Settings and texts of the other blocks (FAQ, contact form, map, gallery) as JSON. */
function JSONBlock({ b, onChange }: { b: Block; onChange: (b: Block) => void }) {
  const [cfg, setCfg] = useState(JSON.stringify(b.config ?? {}, null, 1));
  const [content, setContent] = useState(JSON.stringify(b.content ?? {}, null, 1));
  const [err, setErr] = useState('');
  const apply = () => {
    try {
      onChange({ ...b, config: JSON.parse(cfg) as Record<string, unknown>, content: JSON.parse(content) as Block['content'] });
      setErr('');
    } catch {
      setErr('Not valid JSON');
    }
  };
  return (
    <div className="oc-stack">
      <TextArea label="Settings (JSON)" value={cfg} onChange={setCfg} rows={3} />
      <TextArea label="Texts per language (JSON, e.g. {&quot;id&quot;: {&quot;heading&quot;: …}})" value={content} onChange={setContent} rows={4} error={err || undefined} />
      <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={apply}>Apply</button>
    </div>
  );
}

function SettingsForm({ k, c }: { k: Kind; c: R }) {
  const toast = useToast();
  const pages = useGet<Page<R>>(k.kind === 'page' || k.kind === 'banner' ? '/api/v1/cms/pages?limit=200' : null);
  const cats = useGet<Page<R>>(k.kind === 'article' ? '/api/v1/cms/categories?limit=200' : null);
  const [s, setS] = useState<Record<string, unknown>>({});
  const v = (key: string) => (key in s ? s[key] : c[key]);
  const set = (key: string, val: unknown) => setS((x) => ({ ...x, [key]: val }));
  const save = useSend<Record<string, unknown>>('PATCH', `${k.path}/${c.id}`, CMS);
  const pageOpts = (pages.data?.items ?? []).filter((p) => p.id !== c.id).map((p) => ({ value: p.id, label: String(p.title) }));
  const link = (v('link') ?? {}) as Record<string, unknown>;
  const submit = () => {
    const body: Record<string, unknown> = {};
    const clear: string[] = [];
    for (const [key, val] of Object.entries(s)) {
      if (val === '' || val === undefined) clear.push(key);
      else body[key] = val;
    }
    if (clear.length) body.clear = clear;
    save.mutate(body, { onSuccess: () => { setS({}); toast('Settings saved'); } });
  };
  return (
    <div className="oc-stack">
      <div className="oc-form-grid">
        {k.kind !== 'article' && <TextField label="Key" value={String(v('key') ?? '')} onChange={(x) => set('key', x)} help="Page key (home, golf …) or banner code" />}
        {k.kind === 'page' && <SelectField label="Template" value={String(v('template') ?? '')} onChange={(x) => set('template', x)} options={TEMPLATES} />}
        {k.kind === 'page' && <SelectField label="Parent page" value={String(v('parentId') ?? '')} onChange={(x) => set('parentId', x)} options={pageOpts} placeholder="None" />}
        {k.kind === 'article' && <SelectField label="Category" value={String(v('categoryId') ?? '')} onChange={(x) => set('categoryId', x)}
          options={(cats.data?.items ?? []).map((x) => ({ value: x.id, label: String(x.name) }))} placeholder="None" />}
        {k.kind === 'article' && <TextField label="Tags (comma-separated)" value={Array.isArray(v('tags')) ? (v('tags') as string[]).join(', ') : String(v('tags') ?? '')}
          onChange={(x) => set('tags', x.split(',').map((t) => t.trim()).filter(Boolean))} />}
        {k.kind === 'article' && <TextField label="Author" value={String(v('authorName') ?? '')} onChange={(x) => set('authorName', x)} />}
        {k.kind === 'article' && <TextField label="Publish date shown" type="date" value={String(v('displayDate') ?? '')} onChange={(x) => set('displayDate', x)} />}
        {k.kind === 'banner' && <SelectField label="Placement" value={String(v('placement') ?? '')} onChange={(x) => set('placement', x)} options={PLACEMENTS} />}
        {k.kind === 'banner' && <SelectField label="Link" value={String(link.type ?? '')} onChange={(x) => set('link', x ? { type: x } : '')}
          options={opts(['page', 'route', 'url'])} placeholder="No link" />}
        {k.kind === 'banner' && link.type === 'page' && <SelectField label="Linked page" value={String(link.pageId ?? '')} onChange={(x) => set('link', { type: 'page', pageId: x })} options={pageOpts} placeholder="Choose" />}
        {k.kind === 'banner' && link.type === 'route' && <SelectField label="Website page" value={String(link.routeKey ?? '')} onChange={(x) => set('link', { type: 'route', routeKey: x })} options={ROUTE_KEYS} placeholder="Choose" />}
        {k.kind === 'banner' && link.type === 'url' && <TextField label="Address" value={String(link.url ?? '')} onChange={(x) => set('link', { type: 'url', url: x })} />}
        {k.kind === 'banner' && <SelectField label="Show on page (empty: every page with the placement)" value={((v('pageIds') ?? []) as string[])[0] ?? ''}
          onChange={(x) => set('pageIds', x ? [x] : '')} options={pageOpts} placeholder="Every page" />}
        <TextField label="Sort order" type="number" value={String(v('sortOrder') ?? 0)} onChange={(x) => set('sortOrder', Number(x))} />
        {(k.kind === 'article' || k.kind === 'gallery') && <Checkbox label="Featured" checked={v('featured') === true} onChange={(x) => set('featured', x)} />}
        {k.kind !== 'banner' && <Checkbox label="Show in sitemap" checked={v('showInSitemap') !== false} onChange={(x) => set('showInSitemap', x)} />}
        {!c.live && c.status !== 'published' && c.status !== 'unpublished' && (
          <TextField label="Go live at (after approval)" type="datetime-local" value={toLocalInput(v('publishAt'))} onChange={(x) => set('publishAt', fromLocalInput(x) ?? '')} />
        )}
        <TextField label={k.kind === 'banner' ? 'End of display period' : 'Take down at'} type="datetime-local" value={toLocalInput(v('unpublishAt'))}
          onChange={(x) => set('unpublishAt', fromLocalInput(x) ?? '')} />
      </div>
      <ErrorAlert error={save.error} />
      <div><button className="oc-btn oc-btn-primary" disabled={Object.keys(s).length === 0 || save.isPending} onClick={submit}>Save settings</button></div>
    </div>
  );
}

function PublishingPanel({ k, c }: { k: Kind; c: R }) {
  const { can } = useAuth();
  const { requireApproval } = useLanguages();
  const toast = useToast();
  const base = `${k.path}/${c.id}`;
  const [at, setAt] = useState('');
  const [preview, setPreview] = useState<R | null>(null);
  const previewSend = useSend<Record<string, unknown>, R>('POST', `${base}:preview-link`, []);
  const schedule = useSend<Record<string, unknown>>('POST', `${base}:schedule`, CMS);
  const inReview = c.status === 'in_review';
  return (
    <div className="oc-stack">
      <p className="oc-muted" style={{ margin: 0 }}>
        {requireApproval ? 'Publishing needs an approved review (Approval Workflows → Website Content Publication). An approved version goes live at once, or at its go-live time.'
          : 'Content Policies allow publishing without review at this property.'}
      </p>
      <div className="oc-row-wrap">
        {can(`${k.perm}.update`) && !inReview && <ActionButton label="Submit for review" kind="primary" path={`${base}:submit`} body={{}} invalidate={CMS} />}
        {can(`${k.perm}.update`) && inReview && <ActionButton label="Withdraw review" path={`${base}:withdraw`} body={{}} invalidate={CMS} reason="optional" />}
        {can(`${k.perm}.publish`) && !inReview && <ActionButton label="Publish now" kind="ink" path={`${base}:publish`} body={{}} invalidate={CMS} />}
        {can(`${k.perm}.publish`) && (c.live || c.status === 'scheduled') && <ActionButton label={c.live ? 'Unpublish' : 'Cancel go-live'} danger path={`${base}:unpublish`} invalidate={CMS} reason="required" />}
        <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => previewSend.mutate({}, { onSuccess: setPreview })}>Preview link</button>
      </div>
      {preview && <Card title="Preview link" icon="visibility">
        <KV items={[['Version', String(preview.versionNo)], ['Language', String(preview.language).toUpperCase()], ['Valid until', formatDateTime(String(preview.expiresAt))],
          ['Website', <a key="u" href={String(preview.url)} target="_blank" rel="noreferrer">{String(preview.url)}</a>]]} />
      </Card>}
      {can(`${k.perm}.publish`) && !inReview && (
        <div className="oc-row-wrap">
          <TextField label="Schedule go-live of the approved version" type="datetime-local" value={at} onChange={setAt} />
          <button className="oc-btn oc-btn-sm oc-btn-neutral" disabled={!at || schedule.isPending}
            onClick={() => schedule.mutate({ publishAt: fromLocalInput(at) }, { onSuccess: () => toast('Scheduled') })}>Schedule</button>
        </div>
      )}
      <ErrorAlert error={schedule.error ?? previewSend.error} />
      <PublishingLog contentId={c.id} />
    </div>
  );
}

function VersionsPanel({ k, c }: { k: Kind; c: R }) {
  const { can } = useAuth();
  const base = `${k.path}/${c.id}`;
  const v = useGet<Page<R>>(`${base}/versions`);
  return (
    <DataTable rows={v.data?.items} loading={v.isLoading} error={v.error}
      columns={[
        { key: 'versionNo', header: 'Version', render: (r) => `v${String(r.versionNo)}${r.versionNo === c.publishedVersion ? ' · live' : ''}` },
        { key: 'source', header: 'Source', render: (r) => label(r.source) + (r.restoredFrom ? ` (from v${String(r.restoredFrom)})` : '') },
        { key: 'reviewStatus', header: 'Review', render: (r) => (r.reviewStatus ? <StatusPill status={String(r.reviewStatus)} /> : '—') },
        { key: 'note', header: 'Note', render: (r) => String(r.note ?? '') },
        { key: 'createdAt', header: 'Saved', render: (r) => `${formatDateTime(String(r.createdAt))} · ${String(r.createdByName ?? '')}` },
      ]}
      actions={(r) => <span className="oc-row-wrap">
        {can(`${k.perm}.update`) && r.versionNo !== c.latestVersion && <ActionButton label="Restore" path={`${base}:restore`} body={{ versionNo: r.versionNo }} invalidate={CMS} />}
        {can(`${k.perm}.publish`) && !!r.publishedAt && r.versionNo !== c.publishedVersion &&
          <ActionButton label="Roll back" danger path={`${base}:rollback`} body={{ versionNo: r.versionNo }} invalidate={CMS} reason="required" />}
      </span>} />
  );
}

function PublishingLog({ contentId }: { contentId?: string }) {
  const log = useGet<Page<R>>(`/api/v1/cms/publishing-log${qs({ 'filter[contentId]': contentId, limit: 100 })}`);
  return (
    <DataTable rows={log.data?.items} loading={log.isLoading} error={log.error}
      columns={[
        { key: 'at', header: 'At', render: (r) => formatDateTime(String(r.at)) },
        ...(contentId ? [] : [{ key: 'title', header: 'Content', render: (r: R) => `${label(r.kind)} · ${String(r.title)}` }]),
        { key: 'action', header: 'Action', render: (r) => label(r.action) },
        { key: 'versionNo', header: 'Version', render: (r) => (r.versionNo ? `v${String(r.versionNo)}` : '—') },
        { key: 'actorName', header: 'By', render: (r) => String(r.actorName ?? '') },
        { key: 'note', header: 'Note', render: (r) => String(r.note ?? '') },
      ]} />
  );
}

// ── images library ────────────────────────────────────────────────────────

export function ImagesPage() {
  const { can } = useAuth();
  const toast = useToast();
  const { langs } = useLanguages();
  const [q, setQ] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [alt, setAlt] = useState('');
  const [edit, setEdit] = useState<R | null>(null);
  const list = useGet<Page<R>>(`/api/v1/cms/media${qs({ q, limit: 200 })}`);
  const upload = async (f: File) => {
    setBusy(true);
    setErr(null);
    const fd = new FormData();
    fd.append('file', f);
    if (alt) fd.append('alt', alt);
    try {
      await request('POST', '/api/v1/cms/media', fd);
      toast('Image uploaded');
      setAlt('');
      void list.refetch();
    } catch (x) {
      setErr(x);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="oc-stack">
      <PageHeader title="Images" help="Images library: JPEG, PNG, GIF and WebP (type checked from the file); web sizes (320, 960, 1920 px) are generated; alternative text per language." />
      {can('cms.media.create') && (
        <div className="oc-row-wrap">
          <TextField label="Alternative text (default language)" value={alt} onChange={setAlt} />
          <label className="oc-btn oc-btn-primary" aria-busy={busy}>
            {busy ? 'Uploading…' : 'Upload image'}
            <input type="file" accept="image/jpeg,image/png,image/gif,image/webp" className="oc-sr" onChange={(e) => { const f = e.target.files?.[0]; if (f) void upload(f); }} />
          </label>
        </div>
      )}
      <ErrorAlert error={err} />
      <TextField label="Search" value={q} onChange={setQ} placeholder="File name or alt text" />
      <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error}
        columns={[
          { key: 'preview', header: 'Image', render: (r) => <img src={String(((r.variants as R[] | undefined)?.find((v) => v.name === 'small') ?? r).url)} alt={String((r.alt as Record<string, string>)?.[langs[0]] ?? '')} style={{ height: 48, borderRadius: 8 }} /> },
          { key: 'filename', header: 'File' },
          { key: 'size', header: 'Size', render: (r) => `${String(r.width)}×${String(r.height)} · ${Math.round(Number(r.sizeBytes) / 1024)} KB` },
          { key: 'alt', header: 'Alt text', render: (r) => Object.entries((r.alt ?? {}) as Record<string, string>).map(([l, t]) => `${l.toUpperCase()}: ${t}`).join(' · ') },
          { key: 'tags', header: 'Tags', render: (r) => ((r.tags ?? []) as string[]).join(', ') },
        ]}
        actions={(r) => <span className="oc-row-wrap">
          {can('cms.media.update') && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setEdit(r)}>Edit</button>}
          {can('cms.media.delete') && <ActionButton label="Remove" danger method="DELETE" path={`/api/v1/cms/media/${r.id}`} invalidate={CMS} confirm="Remove this image from the library? Images used by website content cannot be removed." />}
        </span>} />
      {edit && <MediaEdit m={edit} langs={langs} onClose={() => setEdit(null)} />}
    </div>
  );
}

function MediaEdit({ m, langs, onClose }: { m: R; langs: string[]; onClose: () => void }) {
  const [alt, setAlt] = useState<Record<string, string>>({ ...((m.alt ?? {}) as Record<string, string>) });
  const [caption, setCaption] = useState<Record<string, string>>({ ...((m.caption ?? {}) as Record<string, string>) });
  const [tags, setTags] = useState(((m.tags ?? []) as string[]).join(', '));
  const [folder, setFolder] = useState(String(m.folder ?? ''));
  const send = useSend<Record<string, unknown>>('PATCH', `/api/v1/cms/media/${m.id}`, CMS);
  return (
    <Modal open onClose={onClose} title={String(m.filename)}
      actions={<>
        <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
        <button className="oc-btn oc-btn-primary" disabled={send.isPending}
          onClick={() => send.mutate({ alt, caption, tags: tags.split(',').map((t) => t.trim()).filter(Boolean), folder }, { onSuccess: onClose })}>Save</button>
      </>}>
      <div className="oc-form-grid">
        {langs.map((l) => <TextField key={`a${l}`} label={`Alternative text (${l.toUpperCase()})`} value={alt[l] ?? ''} onChange={(v) => setAlt({ ...alt, [l]: v })} />)}
        {langs.map((l) => <TextField key={`c${l}`} label={`Caption (${l.toUpperCase()})`} value={caption[l] ?? ''} onChange={(v) => setCaption({ ...caption, [l]: v })} />)}
        <TextField label="Tags (comma-separated)" value={tags} onChange={setTags} />
        <TextField label="Folder" value={folder} onChange={setFolder} />
        <ErrorAlert error={send.error} />
      </div>
    </Modal>
  );
}

// ── structured data (Packages, Promotions, Pricing, Events) ───────────────

const OWNER_LINKS: Record<string, [string, string]> = {
  rates: ['/commercial/pricing/rate-plans', 'Commercial → Pricing'],
};

export function DataSourcePage() {
  const { source = 'rates' } = useParams();
  const ds = useGet<Page<R>>('/api/v1/cms/data-sources');
  const s = (ds.data?.items ?? []).find((x) => x.key === source);
  if (ds.isLoading) return <Skeleton rows={4} />;
  if (!s) return <ErrorAlert error={ds.error} />;
  const owner = OWNER_LINKS[source];
  const others = (ds.data?.items ?? []).filter((x) => x.key !== source && x.key !== 'news' && x.key !== 'gallery');
  return (
    <div className="oc-stack">
      <PageHeader title={String(s.cmsMenu ?? s.label)} help={`${String(s.description ?? '')} The website always shows the live data of ${label(s.owner)}: add a structured data block to a page instead of copying prices or texts.`} />
      <Card title="Data source" icon="dataset">
        <KV items={[['Data', String(s.label)], ['Owner module', label(s.owner)], ['Public API', <code key="e">{String(s.endpoint)}</code>],
          ['Filters', ((s.filters ?? []) as R[]).map((f) => String(f.label)).join(', ') || '—'],
          ['Maintained in', owner ? <Link key="l" to={owner[0]}>{owner[1]}</Link> : label(s.owner)]]} />
      </Card>
      <Card title="Shown on" icon="web">
        <DataTable rows={(s.usedBy ?? []) as R[]} empty={<Empty title="Not used yet" help="Add a structured data block to a page." />}
          columns={[{ key: 'title', header: 'Content', render: (r) => `${label(r.kind)} · ${String(r.title)}` }, { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> },
            { key: 'live', header: 'Website', render: (r) => (r.live ? 'Live' : '—') }]} />
      </Card>
      <Card title="Other structured data" icon="hub">
        <div className="oc-row-wrap">{others.map((o) => <Link key={String(o.key)} className="oc-chip" to={`/cms/data/${String(o.key)}`}>{String(o.label)}</Link>)}</div>
      </Card>
    </div>
  );
}

// ── languages ─────────────────────────────────────────────────────────────

export function LanguagesPage() {
  const l = useGet<R & { languages: R[] }>('/api/v1/cms/languages');
  const [lang, setLang] = useState('en');
  const [st, setSt] = useState('missing,incomplete,outdated');
  const rows = useGet<Page<R>>(`/api/v1/cms/translation-status${qs({ 'filter[language]': lang, 'filter[translation]': st })}`);
  return (
    <div className="oc-stack">
      <PageHeader title="Languages" help="Website languages, the default language used as fallback, and the translation status of every page, article, banner and album. Edit them in Settings → Club Policies → Content Policies." />
      {l.data && <DataTable rows={l.data.languages} columns={[
        { key: 'name', header: 'Language', render: (r) => `${String(r.name)} (${String(r.code).toUpperCase()})${r.default ? ' · default' : ''}` },
        { key: 'required', header: 'Required before review', render: (r) => (r.required ? 'Yes' : 'No') },
        { key: 'complete', header: 'Complete', align: 'right' }, { key: 'incomplete', header: 'Incomplete', align: 'right' },
        { key: 'outdated', header: 'Outdated', align: 'right' }, { key: 'missing', header: 'Missing', align: 'right' }]} />}
      <div className="oc-row-wrap">
        <SelectField label="Language" value={lang} onChange={setLang} options={(l.data?.languages ?? []).map((x) => ({ value: String(x.code), label: String(x.name) }))} />
        <SelectField label="Translation" value={st} onChange={setSt} options={[{ value: 'missing,incomplete,outdated', label: 'Needs work' }, ...opts(['missing', 'incomplete', 'outdated', 'complete'])]} />
      </div>
      <DataTable rows={rows.data?.items} loading={rows.isLoading} error={rows.error} columns={[
        { key: 'kind', header: 'Type', render: (r) => label(r.kind) }, { key: 'title', header: 'Title' },
        { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> },
        { key: 'translation', header: 'Translation', render: (r) => label(((r.translations as Record<string, R>)?.[lang])?.status) }]} />
    </div>
  );
}

// ── navigation, redirects, categories; publishing desk ────────────────────

const SITE_MASTER: Option[] = [{ value: 'cms.menu', label: 'Navigation Menus' }, { value: 'cms.redirect', label: 'Redirects' }, { value: 'cms.category', label: 'News Categories' }];

export function NavigationPage() {
  const [params, setParams] = useSearchParams();
  const tab = params.get('tab') ?? SITE_MASTER[0].value;
  return (
    <div className="oc-stack">
      <PageHeader title="Navigation & Redirects" help="Header and footer menus of the website (Naming Convention §26, labels per language, page / website route / address links, up to 3 levels), redirects of old URLs and news categories." />
      <Tabs tabs={SITE_MASTER} value={tab} onChange={(v) => setParams({ tab: v })} />
      <AutoResourcePage key={tab} resourceKey={tab} />
    </div>
  );
}

export function PublishingPage() {
  const { can } = useAuth();
  const toast = useToast();
  const sched = useGet<Page<R>>('/api/v1/cms/schedule');
  const [bundle, setBundle] = useState('{\n "dryRun": true,\n "pages": [],\n "articles": [],\n "redirects": []\n}');
  const [res, setRes] = useState<R | null>(null);
  const imp = useSend<Record<string, unknown>, R>('POST', '/api/v1/cms/imports', CMS);
  return (
    <div className="oc-stack">
      <PageHeader title="Publishing" help="Scheduled go-lives and take-downs, the publishing log, the website cache and the import of the old website."
        actions={can('cms.page.publish') ? <ActionButton label="Refresh website cache" path="/api/v1/cms/site:revalidate" invalidate={CMS} onDone={() => toast('The website refreshes its pages')} /> : undefined} />
      <Card title="Schedule (next 60 days)" icon="event_upcoming">
        <DataTable rows={sched.data?.items} loading={sched.isLoading} error={sched.error} columns={[
          { key: 'at', header: 'At', render: (r) => formatDateTime(String(r.at)) + (r.overdue ? ' · overdue' : '') },
          { key: 'action', header: 'Action', render: (r) => (r.action === 'publish' ? 'Go live' : 'Take down') },
          { key: 'title', header: 'Content', render: (r) => `${label(r.kind)} · ${String(r.title)}` }, { key: 'version', header: 'Version' }]} />
      </Card>
      <Card title="Publishing log" icon="history"><PublishingLog /></Card>
      {can('cms.import.create') && (
        <Card title="Import old website (pages, news, redirects)" icon="upload">
          <div className="oc-stack">
            <TextArea label="Import bundle (JSON)" value={bundle} onChange={setBundle} rows={8} help="Imported content arrives as draft versions and goes through the normal review; dryRun only validates." />
            <div><button className="oc-btn oc-btn-neutral" disabled={imp.isPending} onClick={() => {
              try {
                imp.mutate(JSON.parse(bundle) as Record<string, unknown>, { onSuccess: setRes });
              } catch {
                toast('Not valid JSON', 'error');
              }
            }}>Run import</button></div>
            <ErrorAlert error={imp.error} />
            {res && <KV items={[['Dry run', res.dryRun ? 'Yes' : 'No'], ['Created', String(res.created)], ['Updated', String(res.updated)], ['Redirects', String(res.redirects)],
              ['Errors', ((res.errors ?? []) as R[]).map((e) => `${String(e.kind)} #${String(e.index)} ${String(e.ref)}: ${String(e.message)}`).join(' · ') || 'None']]} />}
          </div>
        </Card>
      )}
    </div>
  );
}

/** Back Office routes of the area. */
export const CMS_ROUTES: AreaRoute[] = [
  { path: 'cms/pages', perm: 'cms.page.view', element: <ContentListPage kind="page" /> },
  { path: 'cms/banners', perm: 'cms.banner.view', element: <ContentListPage kind="banner" /> },
  { path: 'cms/news', perm: 'cms.article.view', element: <ContentListPage kind="article" /> },
  { path: 'cms/gallery', perm: 'cms.gallery.view', element: <ContentListPage kind="gallery" /> },
  { path: 'cms/images', perm: 'cms.media.view', element: <ImagesPage /> },
  { path: 'cms/data/:source', perm: 'cms.page.view', element: <DataSourcePage /> },
  { path: 'cms/course-guide', perm: 'cms.course_guide.view', element: <div className="oc-stack"><PageHeader title="Course Guide" help="Text and images per hole completing the course data of Golf (par, distances) on the Hole-by-Hole pages." /><AutoResourcePage resourceKey="cms.course_guide" /></div> },
  { path: 'cms/contact', perm: 'cms.contact.view', element: <div className="oc-stack"><PageHeader title="Contact Information" help="Address, phones, WhatsApp, e-mail, map and opening hours shown on the Contact and Location pages." /><AutoResourcePage resourceKey="cms.contact" /></div> },
  { path: 'cms/languages', perm: 'cms.language.view', element: <LanguagesPage /> },
  { path: 'cms/navigation', perm: 'cms.menu.view', element: <NavigationPage /> },
  { path: 'cms/publishing', perm: 'cms.page.view', element: <PublishingPage /> },
];

/** Ops workstation tiles and routes of the area (none: the CMS is a Back Office module). */
export const CMS_OPS_TILES: OpsTile[] = [];
export const CMS_OPS_ROUTES: OpsRoute[] = [];
