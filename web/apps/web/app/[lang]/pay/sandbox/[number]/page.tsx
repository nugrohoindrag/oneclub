import type { Lang } from '../../../../lib';
import { SandboxPay } from './sandbox';

/** The mock payment gateway's page on a sandbox instance (demo feedback 10 Oct 2026 #40). */
export default async function SandboxPayPage({ params, searchParams }: {
  params: Promise<{ lang: string; number: string }>; searchParams: Promise<{ back?: string }>;
}) {
  const { lang, number } = await params;
  const { back } = await searchParams;
  // only a page of this website is a place to return to
  const to = back && back.startsWith('/') && !back.startsWith('//') ? back : `/${lang}`;
  return (
    <div>
      <h1>{lang === 'id' ? 'Pembayaran (sandbox)' : 'Payment (sandbox)'}</h1>
      <SandboxPay lang={lang as Lang} number={number} back={to} />
    </div>
  );
}
