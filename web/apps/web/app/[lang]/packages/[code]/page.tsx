import { getProperty, pub } from '../../../lib-p2';
import { idr, type Lang } from '../../../lib';
import { priceUnit, type PublicPackage } from '../shared';
import { BookPackage } from './book';

/** Package detail with Book Package (PRD P3 FR-WEB-P3-01/04, non-member flow of P2). */
export default async function PackageDetail({ params }: { params: Promise<{ lang: string; code: string }> }) {
  const { lang: l, code } = await params;
  const lang = l as Lang;
  const id = lang === 'id';
  const p = await getProperty();
  if (!p) return <div className="w-card">Not available</div>;
  const d = await pub<{ package: PublicPackage }>(`/api/v1/public/packages/${encodeURIComponent(code)}?propertyId=${p.id}`);
  if (!d) return <div className="w-card"><p>{id ? 'Paket tidak ditemukan.' : 'Package not found.'}</p></div>;
  const x = d.package;
  return (
    <div className="w-grid">
      <div className="w-card">
        <h1 style={{ marginTop: 0 }}>{x.name}</h1>
        {x.description && <p>{x.description}</p>}
        <ul>{x.components.map((c, i) => (
          <li key={i}>{c.name}{c.perPax ? (id ? ' (per orang)' : ' (per person)') : ''}{c.dayOffset > 0 ? ` — ${id ? 'hari' : 'day'} ${c.dayOffset + 1}` : ''}</li>
        ))}</ul>
        <p><strong>{idr(x.price, lang)}</strong> <span className="w-muted">{priceUnit(x, lang)}</span> · {id ? 'min.' : 'min.'} {x.minPax} {id ? 'orang' : 'pax'}
          {x.nights ? ` · ${x.nights} ${id ? 'malam' : 'night(s)'}` : ''}</p>
      </div>
      <BookPackage lang={lang} propertyId={p.id} pkg={x} />
    </div>
  );
}
