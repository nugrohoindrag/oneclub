import type { Lang } from '../../../lib';
import { ManageBooking } from './manage';

/** Manage a website booking from the link in the confirmation (FR-WEB-08). */
export default async function BookingPage({ params }: { params: Promise<{ lang: string; token: string }> }) {
  const { lang, token } = await params;
  return (
    <div>
      <h1>{lang === 'id' ? 'Pemesanan Anda' : 'Your booking'}</h1>
      <ManageBooking lang={lang as Lang} token={token} />
    </div>
  );
}
