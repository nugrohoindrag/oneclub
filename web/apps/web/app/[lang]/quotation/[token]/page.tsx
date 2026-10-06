import type { Lang } from '../../../lib';
import { AcceptQuotation } from './accept';

/** Quotation behind its secure link: review, accept or reject (PRD P3 FR-QUO-05). */
export default async function QuotationPage({ params }: { params: Promise<{ lang: string; token: string }> }) {
  const { lang, token } = await params;
  return (
    <div>
      <h1>{lang === 'id' ? 'Penawaran Anda' : 'Your quotation'}</h1>
      <AcceptQuotation lang={lang as Lang} token={token} />
    </div>
  );
}
