import type { Metadata } from 'next';
import { getProperty } from '../../../../lib-p2';
import { CheckBooking } from './lookup';

export async function generateMetadata({ params }: { params: Promise<{ lang: string }> }): Promise<Metadata> {
  const { lang } = await params;
  return { title: lang === 'id' ? 'Cek Booking Bungalow' : 'Check a Bungalow Booking' };
}

/** Cek Booking: reservation code + phone or e-mail, without an account (FR-H41). */
export default async function CheckPage({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  const p = await getProperty();
  if (!p) return <div className="w-card">Not available</div>;
  return (
    <div className="w-sc-page">
      <h1>{lang === 'id' ? 'Cek Booking Bungalow' : 'Check a Bungalow Booking'}</h1>
      <CheckBooking lang={lang === 'id' ? 'id' : 'en'} propertyId={p.id} />
    </div>
  );
}
