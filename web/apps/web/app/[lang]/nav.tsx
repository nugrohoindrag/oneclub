'use client';
import { usePathname } from 'next/navigation';
import type { Lang } from '../lib';

/** Public navigation (Naming Convention §26) — client component for the active state. */
export function SiteNav({ lang, labels }: { lang: Lang; labels: Record<string, string> }) {
  const path = usePathname();
  const items: [string, string][] = [
    [`/${lang}`, labels.home],
    [`/${lang}/location`, labels.location],
    [`/${lang}/contact`, labels.contact],
  ];
  return (
    <nav className="w-nav" aria-label="Main">
      {items.map(([href, label]) => (
        <a key={href} href={href} aria-current={path === href ? 'page' : undefined}>
          {label}
        </a>
      ))}
    </nav>
  );
}
