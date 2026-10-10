import type { Metadata } from 'next';
import { getProperty } from '../../../../../lib-p2';
import { PayBungalow } from './pay';

export const metadata: Metadata = { title: 'Pembayaran Bungalow', robots: { index: false } };

export default async function PayBungalowPage({ params }: { params: Promise<{ lang: string; token: string }> }) {
  const { lang, token } = await params;
  const p = await getProperty();
  if (!p) return <div className="w-card">Not available</div>;
  return (
    <div className="w-sc-page">
      <h1>{lang === 'id' ? 'Pembayaran' : 'Payment'}</h1>
      <PayBungalow lang={lang === 'id' ? 'id' : 'en'} propertyId={p.id} token={token} />
    </div>
  );
}
