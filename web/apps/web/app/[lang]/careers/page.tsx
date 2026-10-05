import type { Metadata } from 'next';
import { contractLabel, getPositions } from './lib';

export const metadata: Metadata = { title: 'Careers' };

/** Careers page (PRD P5 EP-03 FR-RCT-05): open public job requisitions of the club. */
export default async function Careers({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  const id = lang === 'id';
  const { items } = await getPositions();
  return (
    <div>
      <h1>{id ? 'Karier' : 'Careers'}</h1>
      <p>{id ? 'Bergabunglah dengan tim kami. Lamar posisi yang terbuka di bawah ini secara online.' : 'Join our team. Apply online for the open positions below.'}</p>
      {items.length === 0 && <p className="w-muted">{id ? 'Saat ini belum ada lowongan terbuka.' : 'There are no open positions at the moment.'}</p>}
      <div className="w-grid">
        {items.map((p) => (
          <div key={p.id} className="w-card">
            <h2>{p.title}</h2>
            <p>{p.department}{p.location ? ` · ${p.location}` : ''}<br />{contractLabel(p.contractType, id)} · {p.openings} {id ? 'posisi' : p.openings === 1 ? 'opening' : 'openings'}</p>
            {p.publishUntil && <p className="w-muted">{id ? 'Lamar sebelum' : 'Apply before'} {p.publishUntil.slice(0, 10)}</p>}
            <a className="w-btn" href={`/${lang}/careers/${p.id}`}>{id ? 'Lihat & lamar' : 'View & apply'}</a>
          </div>
        ))}
      </div>
    </div>
  );
}
