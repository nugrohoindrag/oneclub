import { getProperty, pub } from '../../lib-p2';
import { idr, type Lang } from '../../lib';
import { priceUnit, type PublicPackage } from './shared';

/** Packages page from the structured package data (PRD P3 FR-WEB-P3-01). */
export default async function Packages({ params }: { params: Promise<{ lang: string }> }) {
  const { lang: l } = await params;
  const lang = l as Lang;
  const id = lang === 'id';
  const p = await getProperty();
  if (!p) return <div className="w-card">Not available</div>;
  const data = await pub<{ items: PublicPackage[] }>(`/api/v1/public/packages?propertyId=${p.id}`);
  const items = data?.items ?? [];
  return (
    <div className="w-grid">
      <div className="w-card">
        <h1 style={{ marginTop: 0 }}>{id ? 'Paket' : 'Packages'}</h1>
        <p>{id ? 'Golf, menginap, meeting dan kuliner dalam satu pemesanan — semua komponen dipesan sekaligus.'
          : 'Golf, stays, meetings and dining in one booking — every part is reserved together.'}</p>
      </div>
      {items.length === 0 && <div className="w-card"><p className="w-muted">{id ? 'Belum ada paket.' : 'No packages on sale right now.'}</p></div>}
      {items.map((x) => (
        <div key={x.id} className="w-card">
          <h2 style={{ marginTop: 0 }}>{x.name}</h2>
          {x.description && <p>{x.description}</p>}
          <ul>{x.components.map((c, i) => <li key={i}>{c.name}{c.perPax ? (id ? ' (per orang)' : ' (per person)') : ''}</li>)}</ul>
          <p><strong>{idr(x.price, lang)}</strong> <span className="w-muted">{priceUnit(x, lang)}</span></p>
          <a className="w-btn" href={`/${lang}/packages/${x.code.toLowerCase()}`}>{id ? 'Lihat & pesan' : 'View & book'}</a>
        </div>
      ))}
    </div>
  );
}
