import type { Metadata } from 'next';
import { getProperty } from '../../../../../lib-p2';
import { CourtBookingView } from './view';

export const metadata: Metadata = { title: 'Booking Lapangan', robots: { index: false } };

export default async function CourtBookingPage({ params }: { params: Promise<{ lang: string; token: string }> }) {
  const { lang, token } = await params;
  const p = await getProperty();
  if (!p) return <div className="w-card">Not available</div>;
  return (
    <div className="w-sc-page">
      <h1>{lang === 'id' ? 'Booking Lapangan' : 'Court Booking'}</h1>
      <CourtBookingView lang={lang === 'id' ? 'id' : 'en'} propertyId={p.id} token={token} />
    </div>
  );
}
