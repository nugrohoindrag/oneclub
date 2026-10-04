'use client';
import { usePathname } from 'next/navigation';
import type { Lang } from '../lib';
import { p2Nav } from '../lib-p2';

/** Public navigation (Naming Convention §26) — client component for the active state. */
export function SiteNav({ lang, labels }: { lang: Lang; labels: Record<string, string> }) {
  const path = usePathname();
  const items: [string, string][] = [
    [`/${lang}`, labels.home],
    [`/${lang}/golf`, labels.golf],
    [`/${lang}/membership`, labels.membership],
    ...p2Nav(lang),
    [`/${lang}/contact`, labels.contact],
    [`/${lang}/location`, labels.location],
  ];
  return (
    <nav className="w-nav" aria-label="Main">
      {items.map(([href, label]) => (
        <a key={href} href={href} aria-current={path === href || (href !== `/${lang}` && path.startsWith(href)) ? 'page' : undefined}>
          {label}
        </a>
      ))}
      <a className="w-cta" href={`/${lang}/book-golf`}>{labels.book}</a>
    </nav>
  );
}
