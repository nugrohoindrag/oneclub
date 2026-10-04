import type { Lang } from '../../../lib';
import { PayInvoice } from './pay';

/** Invoice behind a payment link: DP, installment or invoice (FR-INT-P3-04, FR-WEB-P3-05). */
export default async function InvoicePage({ params }: { params: Promise<{ lang: string; token: string }> }) {
  const { lang, token } = await params;
  return (
    <div>
      <h1>{lang === 'id' ? 'Tagihan Anda' : 'Your invoice'}</h1>
      <PayInvoice lang={lang as Lang} token={token} />
    </div>
  );
}
