import type { Lang } from '../../../../lib';
import { SupplierQuote } from './quote';

/** Request for quotation behind the supplier's e-mail link: prices per line (PRD P4 FR-RFQ-01/02). */
export default async function SupplierRFQPage({ params }: { params: Promise<{ lang: string; token: string }> }) {
  const { lang, token } = await params;
  return (
    <div>
      <h1>{lang === 'id' ? 'Permintaan Penawaran' : 'Request for Quotation'}</h1>
      <SupplierQuote lang={lang as Lang} token={token} />
    </div>
  );
}
