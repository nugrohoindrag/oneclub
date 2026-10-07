import React from 'react';
import fs from 'node:fs';
import path from 'node:path';
import { LangSwitch, ThemeScripts } from './client';

// Demo website in the exact look of www.moderngolf.co.id (product owner,
// 7 Oct 2026: "hardcode dulu untuk demo, sama persis"). The page content is
// the captured HTML of the live site (mgcc/{en,id}/*.html, assets under
// public/themes and public/storage), rendered inside the original header
// and footer with the original theme CSS and scripts. OneClub features the
// live site does not have (online booking, membership…) sit in sub-menus of
// the same header and use the same theme classes.

export type Lang = 'en' | 'id';

const DIR = path.join(process.cwd(), 'mgcc');

/** A captured page fragment with its links on the website's /{lang}/ routes. */
export function fragment(lang: string, name: string): string | null {
  const file = path.join(DIR, lang === 'id' ? 'id' : 'en', `${name}.html`);
  if (!fs.existsSync(file)) return null;
  return fs.readFileSync(file, 'utf8').replaceAll('{{LANG}}', lang === 'id' ? 'id' : 'en');
}

/** Renders captured HTML without a wrapping box (the theme CSS targets the sections). */
export function Html({ html }: { html: string }) {
  return <div style={{ display: 'contents' }} dangerouslySetInnerHTML={{ __html: html }} />;
}

/** A page of the live site, by its captured name. */
export function MgccPage({ lang, name, children }: { lang: string; name: string; children?: React.ReactNode }) {
  const html = fragment(lang, name);
  if (html === null) return null;
  return (
    <>
      <Html html={html} />
      {children}
    </>
  );
}

interface MenuItem {
  label: string;
  href: string;
  children?: { label: string; href: string }[];
}

/** The live site's main menu; the sub-menus add the OneClub pages. */
export function menu(lang: Lang): MenuItem[] {
  const l = (p: string) => `/${lang}${p}`;
  const id = lang === 'id';
  return [
    { label: 'ABOUT US', href: l('/about'), children: [
      { label: 'About Us', href: l('/about') }, { label: id ? 'Keanggotaan' : 'Membership', href: l('/membership') },
      { label: 'Hall of Fame', href: l('/hall-of-fame') }, { label: id ? 'Karier' : 'Careers', href: l('/careers') }] },
    { label: "WHAT'S ON", href: l('/events'), children: [
      { label: 'Events & Promos', href: l('/events') }, { label: id ? 'Acara & Tiket' : 'Event Tickets', href: l('/events/upcoming') },
      { label: 'Tournaments', href: l('/tournaments') }, { label: id ? 'Paket' : 'Packages', href: l('/packages') },
      { label: id ? 'Promosi' : 'Promotions', href: l('/promotions') }, { label: id ? 'Berita' : 'News', href: l('/news') }, { label: id ? 'Galeri' : 'Gallery', href: l('/gallery') }] },
    { label: 'GOLF COURSE', href: l('/golf-course'), children: [
      { label: 'Golf Course', href: l('/golf-course') }, { label: 'Reciprocal', href: l('/golf-course-reciprocal') },
      { label: 'Hole by Hole', href: l('/golf-course-hole-by-hole') }, { label: 'Handycap Index', href: l('/golf-course-handycap-index') },
      { label: 'Facilities', href: l('/golf-course-facilities') }, { label: 'Book Tee Time', href: l('/book-golf') }] },
    { label: 'MICE & WEDDING', href: l('/mice-and-wedding'), children: [
      { label: 'MICE & Wedding', href: l('/mice-and-wedding') }, { label: id ? 'Pesan Meeting Room' : 'Book Meeting Room', href: l('/book/meeting-room') },
      { label: id ? 'Pernikahan & Banquet' : 'Wedding & Banquet Inquiry', href: l('/wedding-banquet') }] },
    { label: 'SPORT CLUB', href: l('/sport-club'), children: [
      { label: 'Sport Club', href: l('/sport-club') }, { label: id ? 'Pesan Fasilitas' : 'Book Facility', href: l('/book/sport-club') }] },
    { label: 'BUNGALOW', href: l('/bungalow'), children: [
      { label: 'Bungalow', href: l('/bungalow') }, { label: id ? 'Pesan Bungalow' : 'Book Bungalow', href: l('/book/bungalow') }] },
    { label: 'VIP SUITE', href: l('/vip-suite'), children: [
      { label: 'VIP Suite', href: l('/vip-suite') }, { label: id ? 'Pesan VIP Suite' : 'Book VIP Suite', href: l('/book/vip-suite') }] },
    { label: 'CONTACT US', href: l('/contact'), children: [
      { label: 'Contact Us', href: l('/contact') }, { label: id ? 'Lokasi' : 'Location', href: l('/location') },
      { label: id ? 'Sampaikan Keluhan' : 'Complaint', href: l('/complaint') }] },
  ];
}

export function Header({ lang }: { lang: Lang }) {
  return (
    <header>
      <a href={`/${lang}`} className="logo"><img src="/themes/modern-golf/assets/images/logo.png" alt="" /></a>
      <ul className="main-menu">
        {/* the captured markup's whitespace (around the labels, between the items) sets the menu spacing */}
        {menu(lang).map((m) => (
          <React.Fragment key={m.label}>
          {' '}
          <li>
            <a href={m.href}>
              {' '}{m.label}{' '}
              {m.label === 'GOLF COURSE' && <> <i className="fa fa-caret-down"></i></>}
            </a>
            {m.children && (
              <div className="sub-menu">
                {m.children.map((c) => <a key={c.href + c.label} href={c.href}>{c.label}</a>)}
              </div>
            )}
          </li>
          {' '}
          </React.Fragment>
        ))}
      </ul>
      <form action={`/${lang}/search`} method="get" className="src-form">
        <input name="q" type="text" placeholder="Search Here..." />
        <button><i className="fa fa-search"></i></button>
      </form>
      <LangSwitch lang={lang} />
      <div className="burger">
        <i className="fa fa-bars"></i>
      </div>
    </header>
  );
}

export function Footer({ lang }: { lang: Lang }) {
  return <Html html={fragment(lang, '_footer') ?? ''} />;
}

/** The theme stylesheets, in the live site's order. */
export function ThemeHead() {
  const css = ['normalize', 'bulma.min', 'fontawesome-all', 'owl.carousel', 'main'];
  return (
    <>
      {css.slice(0, 2).map((c) => <link key={c} rel="stylesheet" href={`/themes/modern-golf/assets/css/${c}.css`} />)}
      <link rel="preconnect" href="https://fonts.googleapis.com" />
      <link rel="preconnect" href="https://fonts.gstatic.com" crossOrigin="" />
      <link href="https://fonts.googleapis.com/css2?family=Poppins:wght@400;600;700&display=swap" rel="stylesheet" />
      {css.slice(2).map((c) => <link key={c} rel="stylesheet" href={`/themes/modern-golf/assets/css/${c}.css`} />)}
      <link rel="stylesheet" href="/mgcc-oneclub.css" />
    </>
  );
}

export { ThemeScripts };
