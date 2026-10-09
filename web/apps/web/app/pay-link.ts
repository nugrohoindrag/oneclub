/**
 * Where to pay an online payment (demo feedback 10 Oct 2026 #40): a payment
 * gateway's own checkout page, or — for the mock gateway of a sandbox
 * instance, whose checkout URL points at a host that does not exist — the
 * sandbox payment page of this website, which returns to `back` once paid.
 * Client-side helper (it reads the current page for the defaults).
 */
export function checkoutHref(url: string | null | undefined, lang?: string, back?: string): string | null {
  if (!url) return null;
  const m = /sandbox\.pay\.local\/checkout\/([^/?#]+)/.exec(url);
  if (!m) return url;
  const here = typeof window === 'undefined' ? '' : window.location.pathname + window.location.search;
  const l = lang ?? (here.split('/')[1] === 'en' ? 'en' : 'id');
  const to = back ?? here;
  return `/${l}/pay/sandbox/${encodeURIComponent(m[1])}${to ? `?back=${encodeURIComponent(to)}` : ''}`;
}
