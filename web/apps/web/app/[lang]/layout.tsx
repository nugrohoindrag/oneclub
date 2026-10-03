import type { ReactNode } from 'react';
import type { Metadata } from 'next';
import { notFound } from 'next/navigation';
import { copy, getBootstrap, LANGS, type Lang } from '../lib';
import { SiteNav } from './nav';

export async function generateMetadata(): Promise<Metadata> {
  const b = await getBootstrap();
  return { title: b.branding.appName, icons: { icon: '/favicon.svg' } };
}

export default async function LangLayout({ children, params }: { children: ReactNode; params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  if (!LANGS.includes(lang as Lang)) notFound();
  const b = await getBootstrap();
  const t = copy[lang as Lang];
  const custom =
    b.branding.accent === 'custom' && b.branding.customAccent
      ? `[data-accent='custom']{${Object.entries(b.branding.customAccent.light).map(([k, v]) => `${k}:${v}`).join(';')}}`
      : '';
  return (
    <html lang={lang} data-accent={b.branding.accent || 'lime'} data-theme="light">
      <head>
        <link rel="preconnect" href="https://fonts.googleapis.com" />
        <link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet" />
        {custom && <style>{custom}</style>}
      </head>
      <body>
        <div className="w-frame">
          <header className="w-top">
            <a className="w-brand" href={`/${lang}`}>
              {b.branding.logoUrl ? <img src={b.branding.logoUrl} alt="" /> : <span className="w-mark">{b.branding.appName.slice(0, 1)}</span>}
              {b.branding.appName}
            </a>
            <SiteNav lang={lang as Lang} labels={t.nav} />
            <a className="w-lang" href={lang === 'id' ? '/en' : '/id'} hrefLang={lang === 'id' ? 'en' : 'id'}>
              {lang === 'id' ? 'EN' : 'ID'}
            </a>
          </header>
          <main>{children}</main>
          <footer className="w-foot">
            © {new Date().getFullYear()} {b.branding.appName} · {t.footer}
          </footer>
        </div>
      </body>
    </html>
  );
}
