'use client';
import { useEffect, useId, useRef, useState, type FocusEvent } from 'react';
import { usePathname } from 'next/navigation';
import type { Lang } from '../lib';
import { p2Nav } from '../lib-p2';
import { isCurrent, type NavEntry, type NavLink } from './nav-model';

/** Built-in navigation (Naming Convention §26): shown while no CMS header menu is published. */
export function fallbackNav(lang: Lang, labels: Record<string, string>): NavEntry[] {
  const items: [string, string][] = [
    [`/${lang}`, labels.home],
    [`/${lang}/golf`, labels.golf],
    [`/${lang}/membership`, labels.membership],
    ...p2Nav(lang),
    [`/${lang}/vip-suite`, 'VIP Suite'], // PRD P3 FR-WEB-P3-01
    [`/${lang}/meeting`, 'Meeting & MICE'],
    [`/${lang}/news`, lang === 'id' ? 'Berita' : 'News'],
    [`/${lang}/gallery`, lang === 'id' ? 'Galeri' : 'Gallery'],
    [`/${lang}/packages`, lang === 'id' ? 'Paket' : 'Packages'],
    [`/${lang}/promotions`, lang === 'id' ? 'Promo' : 'Promotions'],
    [`/${lang}/wedding-banquet`, 'Wedding & Banquet'],
    [`/${lang}/events`, lang === 'id' ? 'Acara' : 'Events'],
    [`/${lang}/tournaments`, lang === 'id' ? 'Turnamen' : 'Tournaments'], // PRD P3 FR-WEB-P3-03
    [`/${lang}/contact`, labels.contact],
    [`/${lang}/location`, labels.location],
  ];
  return items.map(([href, label]) => ({ href, label, children: [] }));
}

function Link({ l, current }: { l: NavLink; current: boolean }) {
  return (
    <a href={l.href} aria-current={current ? 'page' : undefined} {...(l.newTab ? { target: '_blank', rel: 'noopener noreferrer' } : {})}>
      {l.label}
    </a>
  );
}

/**
 * Public header navigation (PRD P4 FR-CMS-09): the CMS header menu with one
 * dropdown level, else the built-in list. Dropdowns follow the disclosure
 * pattern (button with aria-expanded; Escape, a click outside or leaving
 * with Tab closes them). On narrow screens the list folds behind a Menu
 * button and dropdowns expand in place.
 */
export function SiteNav({ lang, labels, items }: { lang: Lang; labels: Record<string, string>; items?: NavEntry[] | null }) {
  const path = usePathname() ?? '';
  const entries = items && items.length > 0 ? items : fallbackNav(lang, labels);
  const [open, setOpen] = useState<number | null>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const navRef = useRef<HTMLElement>(null);
  const toggleRef = useRef<HTMLButtonElement>(null);
  const groupRefs = useRef<(HTMLButtonElement | null)[]>([]);
  const uid = useId();
  const listId = `${uid}-list`;

  useEffect(() => {
    setOpen(null);
    setMenuOpen(false);
  }, [path]);

  useEffect(() => {
    if (open === null && !menuOpen) return;
    const onPointer = (e: PointerEvent) => {
      if (navRef.current && !navRef.current.contains(e.target as Node)) {
        setOpen(null);
        setMenuOpen(false);
      }
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return;
      const inside = !!navRef.current?.contains(document.activeElement);
      if (open !== null) {
        if (inside) groupRefs.current[open]?.focus();
        setOpen(null);
      } else {
        if (inside) toggleRef.current?.focus();
        setMenuOpen(false);
      }
    };
    document.addEventListener('pointerdown', onPointer);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('pointerdown', onPointer);
      document.removeEventListener('keydown', onKey);
    };
  }, [open, menuOpen]);

  const leaveGroup = (i: number) => (e: FocusEvent<HTMLLIElement>) => {
    if (open === i && !e.currentTarget.contains(e.relatedTarget as Node | null)) setOpen(null);
  };

  return (
    <nav ref={navRef} className="w-nav" aria-label="Main" data-open={menuOpen ? 'true' : 'false'}>
      <button ref={toggleRef} type="button" className="w-nav-toggle" aria-expanded={menuOpen} aria-controls={listId} onClick={() => setMenuOpen((v) => !v)}>
        <span className="w-burger" aria-hidden="true" />
        Menu
      </button>
      <ul className="w-nav-list" id={listId}>
        {entries.map((e, i) => {
          if (e.children.length === 0) {
            return (
              <li key={`${i}-${e.href}`}>
                <Link l={e} current={isCurrent(path, e, lang)} />
              </li>
            );
          }
          const subId = `${uid}-sub-${i}`;
          const current = e.children.some((c) => isCurrent(path, c, lang));
          return (
            <li key={`${i}-${e.label}`} className="w-nav-group" onBlur={leaveGroup(i)}>
              <button
                ref={(el) => {
                  groupRefs.current[i] = el;
                }}
                type="button"
                className="w-nav-btn"
                aria-expanded={open === i}
                aria-controls={subId}
                data-current={current ? 'true' : undefined}
                onClick={() => setOpen(open === i ? null : i)}
              >
                {e.label}
                <span className="w-chev" aria-hidden="true" />
              </button>
              <ul className="w-nav-sub" id={subId} hidden={open !== i}>
                {e.children.map((c) => (
                  <li key={`${c.href}-${c.label}`}>
                    <Link l={c} current={isCurrent(path, c, lang)} />
                  </li>
                ))}
              </ul>
            </li>
          );
        })}
      </ul>
      <a className="w-cta" href={`/${lang}/book-golf`}>{labels.book}</a>
    </nav>
  );
}
