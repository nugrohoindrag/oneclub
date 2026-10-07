import type { ReactNode } from 'react';
import type { Metadata } from 'next';
import { notFound } from 'next/navigation';
import { LANGS, type Lang } from '../lib';
import { Footer, Header, ThemeHead, ThemeScripts } from '../../components/mgcc/site';
import { PageFrame } from '../../components/mgcc/frame';

// Website in the look of www.moderngolf.co.id (demo, product owner 7 Oct
// 2026): the live site's header, footer, theme CSS and scripts around every
// page; OneClub pages get the live site's page cover (PageFrame).

export async function generateMetadata(): Promise<Metadata> {
  return { title: 'Modern Golf', icons: { icon: '/storage/app/uploads/public/630/662/fb4/630662fb47c4f626989939.png' } };
}

export default async function LangLayout({ children, params }: { children: ReactNode; params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  if (!LANGS.includes(lang as Lang)) notFound();
  return (
    <html lang={lang}>
      <head>
        <ThemeHead />
      </head>
      <body>
        <div className="master">
          <Header lang={lang as Lang} />
          <PageFrame lang={lang as Lang}>{children}</PageFrame>
          <Footer lang={lang as Lang} />
        </div>
        <ThemeScripts />
      </body>
    </html>
  );
}
