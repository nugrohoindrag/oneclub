'use client';

import type { ReactNode } from 'react';
import { usePathname } from 'next/navigation';

// Pages of the live site render as captured; every OneClub page (online
// booking, membership, news…) gets the live site's page cover with its
// title and breadcrumb, then its content in the theme container.

/** The captured pages of the live site (no extra frame). */
const LIVE = new Set(['', '/about', '/events', '/golf-course', '/golf-course-reciprocal', '/golf-course-hole-by-hole', '/golf-course-handycap-index',
  '/golf-course-facilities', '/mice-and-wedding', '/sport-club', '/bungalow', '/vip-suite', '/contact', '/search']);

const COVER = '/themes/modern-golf/assets/images/banks/';

/** Title (EN, ID) and cover photo of the OneClub pages, by path prefix (longest first). */
const PAGES: [string, string, string, string][] = [
  ['/book-golf', 'Book Tee Time', 'Pesan Tee Time', 'golf-course-cover.jpg'],
  ['/book/bungalow', 'Book Bungalow', 'Pesan Bungalow', 'bungalow-cover.jpg'],
  ['/book/vip-suite', 'Book VIP Suite', 'Pesan VIP Suite', 'vip-suite-cover.jpg'],
  ['/book/meeting-room', 'Book Meeting Room', 'Pesan Meeting Room', 'wedding-cover.jpg'],
  ['/book/sport-club', 'Book Sport Club', 'Pesan Sport Club', 'sports-club-cover.jpg'],
  ['/booking', 'My Booking', 'Booking Saya', 'golf-course-cover.jpg'],
  ['/payment', 'Payment', 'Pembayaran', 'golf-course-cover.jpg'],
  ['/membership', 'Membership', 'Keanggotaan', 'golf-course-cover.jpg'],
  ['/events/upcoming', 'Event Tickets', 'Acara & Tiket', 'wedding-cover.jpg'],
  ['/events/ticket', 'Event Ticket', 'Tiket Acara', 'wedding-cover.jpg'],
  ['/events', 'Event', 'Acara', 'wedding-cover.jpg'],
  ['/event-detail', "What's On", "What's On", 'golf-course-cover.jpg'],
  ['/wedding-banquet', 'Wedding & Banquet', 'Pernikahan & Banquet', 'wedding-cover.jpg'],
  ['/tournaments', 'Tournaments', 'Turnamen', 'golf-course-cover.jpg'],
  ['/packages', 'Packages', 'Paket', 'bungalow-cover.jpg'],
  ['/promotions', 'Promotions', 'Promosi', 'golf-course-cover.jpg'],
  ['/news', 'News', 'Berita', 'golf-course-cover.jpg'],
  ['/gallery', 'Gallery', 'Galeri', 'golf-course-cover.jpg'],
  ['/hall-of-fame', 'Hall of Fame', 'Hall of Fame', 'golf-course-cover.jpg'],
  ['/careers', 'Careers', 'Karier', 'golf-course-cover.jpg'],
  ['/complaint', 'Complaint', 'Keluhan', 'golf-course-cover.jpg'],
  ['/location', 'Location', 'Lokasi', 'golf-course-cover.jpg'],
  ['/golf', 'Golf Course', 'Lapangan Golf', 'golf-course-cover.jpg'],
  ['/invoice', 'Invoice', 'Invoice', 'golf-course-cover.jpg'],
  ['/quotation', 'Quotation', 'Penawaran', 'golf-course-cover.jpg'],
  ['/supplier', 'Supplier', 'Supplier', 'golf-course-cover.jpg'],
];

export function PageFrame({ lang, children }: { lang: 'en' | 'id'; children: ReactNode }) {
  const rest = (usePathname() ?? '').replace(/^\/(en|id)(?=\/|$)/, '').replace(/\/$/, '');
  if (LIVE.has(rest) || rest.startsWith('/event-detail/')) return <>{children}</>;
  const page = PAGES.find(([p]) => rest === p || rest.startsWith(`${p}/`));
  const title = page ? (lang === 'id' ? page[2] : page[1]) : 'Modern Golf & Country Club';
  return (
    <>
      <section className="page-cover sm">
        <figure><img src={COVER + (page?.[3] ?? 'golf-course-cover.jpg')} alt="" /></figure>
        <div className="text regular-txt">
          <h2>{title}</h2>
          <div className="breadcrumb"><a href={`/${lang}`}>Home</a> - <a href={`/${lang}${rest}`}>{title}</a></div>
        </div>
      </section>
      <section className="oc-page">
        <div className="container">{children}</div>
      </section>
    </>
  );
}
