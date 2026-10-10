import type { Metadata } from 'next';
import { getProperty } from '../../../../lib-p2';
import { CourtLookup } from './lookup';

export const metadata: Metadata = { title: 'Cek Booking Lapangan' };

export default async function CheckBookingPage({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  const p = await getProperty();
  if (!p) return <div className="w-card">Not available</div>;
  const id = lang === 'id';
  return (
    <div className="w-sc-page" style={{ maxWidth: 640 }}>
      <h1>{id ? 'Cek Booking' : 'Check Booking'}</h1>
      <p className="w-muted">{id ? 'Masukkan kode booking dan nomor ponsel atau e-mail yang dipakai saat memesan untuk melihat QR dan e-ticket.'
        : 'Enter the booking code and the phone or e-mail you booked with to see the QR and the e-ticket.'}</p>
      <CourtLookup lang={id ? 'id' : 'en'} propertyId={p.id} />
    </div>
  );
}
