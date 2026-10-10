import type { Metadata } from 'next';
import { getProperty } from '../../../../../lib-p2';
import { BookingPage } from './view';

export const metadata: Metadata = { title: 'Reservasi Bungalow', robots: { index: false } };

/** Reservasi Terkonfirmasi / Cek Booking of a link (docs/requirement-booking-hotel-mgcc.md FR-H38–H43). */
export default async function BungalowBookingPage({ params }: { params: Promise<{ lang: string; token: string }> }) {
  const { lang, token } = await params;
  const p = await getProperty();
  if (!p) return <div className="w-card">Not available</div>;
  return (
    <div className="w-sc-page">
      <BookingPage lang={lang === 'id' ? 'id' : 'en'} propertyId={p.id} token={token} />
    </div>
  );
}
