import type { Lang } from '../../../lib';
import { PaySchedule } from './pay';

/** Payment schedule behind its payment link: DP and termin paid online (FR-WEB-P3-05, FR-INT-P3-04). */
export default async function PaymentSchedulePage({ params }: { params: Promise<{ lang: string; token: string }> }) {
  const { lang, token } = await params;
  return (
    <div>
      <h1>{lang === 'id' ? 'Jadwal pembayaran' : 'Payment schedule'}</h1>
      <PaySchedule lang={lang as Lang} token={token} />
    </div>
  );
}
