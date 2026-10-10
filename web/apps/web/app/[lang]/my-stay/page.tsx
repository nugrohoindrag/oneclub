import type { Metadata } from 'next';
import type { Lang } from '../../lib';
import { getProperty } from '../../lib-p2';
import { MyStay } from './stay';

export const metadata: Metadata = { title: 'My Stay', robots: { index: false } };

/** My Stay without an account: the link of the booking, or the reservation code + e-mail or phone (FR-H41). */
export default async function MyStayPage({ params, searchParams }: { params: Promise<{ lang: string }>; searchParams: Promise<{ token?: string }> }) {
  const { lang } = await params;
  const { token } = await searchParams;
  const p = await getProperty();
  return (
    <div>
      <h1>My Stay</h1>
      {p ? <MyStay lang={lang as Lang} propertyId={p.id} token={token ?? ''} /> : <p className="w-muted">{lang === 'id' ? 'Belum tersedia.' : 'Not available yet.'}</p>}
    </div>
  );
}
