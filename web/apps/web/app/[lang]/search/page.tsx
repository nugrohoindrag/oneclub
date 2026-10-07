import { fragment } from '../../../components/mgcc/site';
import { EVENT_DETAILS } from '../../../components/mgcc/pages';

// Search Result of the live site (www.moderngolf.co.id/search?q=…): the
// captured pages whose text contains the query, as cards with the match
// highlighted. The live site's result links are broken; here they open the page.

const PAGES: [string, string][] = [
  ['golf-course', '/golf-course'], ['golf-course-hole-by-hole', '/golf-course-hole-by-hole'], ['golf-course-handycap-index', '/golf-course-handycap-index'],
  ['golf-course-reciprocal', '/golf-course-reciprocal'], ['golf-course-facilities', '/golf-course-facilities'], ['mice-and-wedding', '/mice-and-wedding'],
  ['sport-club', '/sport-club'], ['bungalow', '/bungalow'], ['vip-suite', '/vip-suite'], ['about', '/about'], ['events', '/events'], ['contact', '/contact'],
  ...EVENT_DETAILS.map((s) => [`event-detail--${s}`, `/event-detail/${s}`] as [string, string]),
];

const decode = (s: string) => s.replace(/&nbsp;/g, ' ').replace(/&amp;/g, '&').replace(/&#0?39;/g, "'").replace(/&quot;/g, '"').replace(/&lt;/g, '<').replace(/&gt;/g, '>');
const text = (html: string) => decode(html.replace(/<(script|style)[\s\S]*?<\/\1>/g, ' ').replace(/<[^>]+>/g, ' ')).replace(/\s+/g, ' ').trim();

function Marked({ s, q }: { s: string; q: string }) {
  const parts = s.split(new RegExp(`(${q.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')})`, 'gi'));
  return <>{parts.map((p, i) => (p.toLowerCase() === q.toLowerCase() ? <mark key={i}>{p}</mark> : p))}</>;
}

export default async function Search({ params, searchParams }: { params: Promise<{ lang: string }>; searchParams: Promise<{ q?: string }> }) {
  const { lang } = await params;
  const q = ((await searchParams).q ?? '').trim();
  const hits = q.length < 2 ? [] : PAGES.flatMap(([name, path]) => {
    const html = fragment(lang, name);
    if (!html) return [];
    const body = text(html);
    const at = body.toLowerCase().indexOf(q.toLowerCase());
    if (at < 0) return [];
    const title = text(html.match(/<h2>([\s\S]*?)<\/h2>/)?.[1] ?? html.match(/<h3>([\s\S]*?)<\/h3>/)?.[1] ?? name);
    const img = name.startsWith('event-detail--') ? html.match(/<img src="([^"]+)"/)?.[1] : undefined;
    const from = Math.max(0, at - 60);
    return [{ path, title, img, snippet: `${from > 0 ? '…' : ''}${body.slice(from, from + 160)}…` }];
  });
  return (
    <>
      <section className="page-cover sm">
        <figure><img src="/themes/modern-golf/assets/images/banks/vip-suite-cover.jpg" alt="" /></figure>
        <div className="text regular-txt"><h2>Search Result</h2></div>
      </section>
      <section className="events no-bg">
        <div className="container">
          <div className="columns is-multiline">
            {hits.map((h) => (
              <div key={h.path} className="column is-3">
                {h.img && <figure className="ratio ratio-135"><a href={`/${lang}${h.path}`}><img src={h.img} alt={h.title} /></a></figure>}
                <div className="text">
                  <h3><a href={`/${lang}${h.path}`}><Marked s={h.title} q={q} /></a></h3>
                  <Marked s={h.snippet} q={q} />
                </div>
              </div>
            ))}
            {hits.length === 0 && (
              <div className="column is-12"><div className="text"><p>{lang === 'id' ? `Tidak ada hasil untuk "${q}".` : `No results for "${q}".`}</p></div></div>
            )}
          </div>
        </div>
      </section>
    </>
  );
}
