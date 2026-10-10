import type { Metadata } from 'next';
import { getProperty } from '../../../../../lib-p2';
import { PayCourt } from './pay';

export const metadata: Metadata = { title: 'Pembayaran Booking Lapangan', robots: { index: false } };

export default async function PayCourtPage({ params }: { params: Promise<{ lang: string; token: string }> }) {
  const { lang, token } = await params;
  const p = await getProperty();
  if (!p) return <div className="w-card">Not available</div>;
  return (
    <div className="w-sc-page">
      <h1>{lang === 'id' ? 'Pembayaran' : 'Payment'}</h1>
      <PayCourt lang={lang === 'id' ? 'id' : 'en'} propertyId={p.id} token={token} />
    </div>
  );
}
