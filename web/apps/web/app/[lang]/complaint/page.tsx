import type { Lang } from '../../lib';
import { getProperty } from '../../lib-p2';
import { ComplaintForm } from './complaint-form';

/** Website complaint form (PRD P3 FR-TKT-01): becomes a complaint ticket with SLA. */
export default async function ComplaintPage({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  const p = await getProperty();
  const id = (lang as Lang) === 'id';
  return (
    <div className="w-grid">
      <div className="w-card">
        <h1 style={{ marginTop: 0 }}>{id ? 'Sampaikan keluhan' : 'Make a complaint'}</h1>
        <p>{id ? 'Kami menanggapi setiap keluhan sesuai target waktu layanan kami dan mengabari Anda melalui e-mail atau WhatsApp.'
          : 'We answer every complaint within our service targets and keep you posted by e-mail or WhatsApp.'}</p>
      </div>
      {p && <ComplaintForm propertyId={p.id} lang={lang as Lang} />}
    </div>
  );
}
