import { notFound } from 'next/navigation';
import { contractLabel, getPosition } from '../lib';
import { ApplyForm } from './apply-form';

/** An open position with the application form (consent required, CV optional). */
export default async function Position({ params }: { params: Promise<{ lang: string; id: string }> }) {
  const { lang, id: pid } = await params;
  const id = lang === 'id';
  const { propertyId, position: p } = await getPosition(pid);
  if (!p || !propertyId) notFound();
  return (
    <div className="w-grid">
      <div className="w-card">
        <p><a href={`/${lang}/careers`}>← {id ? 'Semua lowongan' : 'All positions'}</a></p>
        <h1 style={{ marginTop: 0 }}>{p.title}</h1>
        <p>{p.department}{p.location ? ` · ${p.location}` : ''} · {contractLabel(p.contractType, id)} · {p.number}</p>
        {p.description && (<><h2>{id ? 'Tugas' : 'The job'}</h2><p style={{ whiteSpace: 'pre-wrap' }}>{p.description}</p></>)}
        {p.requirements && (<><h2>{id ? 'Persyaratan' : 'Requirements'}</h2><p style={{ whiteSpace: 'pre-wrap' }}>{p.requirements}</p></>)}
      </div>
      <ApplyForm propertyId={propertyId} requisitionId={p.id} lang={lang} />
    </div>
  );
}
