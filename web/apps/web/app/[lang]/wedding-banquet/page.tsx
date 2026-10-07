import { getProperty, pub, rp } from '../../lib-p2';
import { BanquetInquiry } from '../events/forms';
import { VenueLayout } from '../../../components/mgcc/venue';

interface Pkg {
  id: string; code: string; name: string; category: string; pricingMethod: string; price: string; pricingMode: string; minPax: number; includedPax: number;
  durationHours: number; inclusions: string[]; description?: string | null;
}
interface Venue {
  id: string; code: string; name: string; venueType: string; sizeSqm?: string | null; maxCapacity?: number | null; minPax: number; addonPrice: string;
  facilities: string[]; description?: string | null; layouts: { layout: string; capacity: number }[];
}
interface Menu { id: string; code: string; name: string; menuType: string; pricePerPax: string; pricingMode: string; description?: string | null; items: string[] }

const label = (s: string) => s.replace(/_/g, ' ');

function price(p: Pkg, id: boolean) {
  const mode = p.pricingMode === 'nett' ? 'nett' : '++';
  switch (p.pricingMethod) {
    case 'per_pax':
      return `${rp(p.price)}${mode} / pax${p.minPax ? ` · min. ${p.minPax} pax` : ''}`;
    case 'per_pax_per_day':
      return `${rp(p.price)}${mode} / pax / ${id ? 'hari' : 'day'}${p.minPax ? ` · min. ${p.minPax} pax` : ''}`;
  }
  return `${rp(p.price)} ${mode}${p.includedPax ? ` · ${p.includedPax} pax` : ''}`;
}

/** Wedding & Banquet page from structured data (PRD P3 FR-WEB-P3-01). */
export default async function WeddingBanquet({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  const p = await getProperty();
  if (!p) return <div className="w-card">Not available</div>;
  const data = await pub<{ currency: string; packages: Pkg[]; venues: Venue[]; menus: Menu[] }>(`/api/v1/public/wedding-banquet?propertyId=${p.id}`);
  const id = lang === 'id';
  const packages = data?.packages ?? [];
  const venues = data?.venues ?? [];
  const menus = data?.menus ?? [];
  const ld = [
    ...packages.map((x) => ({ '@context': 'https://schema.org', '@type': 'Offer', name: x.name, description: x.description ?? x.inclusions.join(', '),
      price: x.price, priceCurrency: data?.currency ?? 'IDR', category: x.category, seller: { '@type': 'Organization', name: p.name } })),
    ...venues.map((v) => ({ '@context': 'https://schema.org', '@type': 'EventVenue', name: `${v.name} · ${p.name}`, maximumAttendeeCapacity: v.maxCapacity ?? undefined,
      description: v.description ?? undefined, amenityFeature: v.facilities.map((f) => ({ '@type': 'LocationFeatureSpecification', name: f, value: true })) })),
  ];
  return (
    <VenueLayout venue="meeting-room" lang={lang} title="Wedding &amp; Banquet"
      intro={<>{id ? 'Paket pernikahan, banquet dan acara sosial dengan ballroom, function room dan venue outdoor. ' : 'Wedding, banquet and social event packages with the ballroom, function rooms and outdoor venues. '}
        <a href={`/${lang}/events`}>{id ? 'Lihat acara club' : 'See club events'}</a>.</>}>
      {packages.length > 0 && (
        <>
          <h4 className="oc-sec">{id ? 'Paket' : 'Packages'}</h4>
          <div className="columns is-multiline oc-units">
            {packages.map((x) => (
              <div key={x.id} className="column is-6">
                <div className="oc-unit">
                  <span className="oc-tag">{label(x.category)}</span>
                  <h4>{x.name}</h4>
                  <div className="oc-price">{price(x, id)}</div>
                  <span>{x.durationHours} {id ? 'jam' : 'hours'}</span>
                  {x.inclusions.length > 0 && <ul>{x.inclusions.map((i) => <li key={i}>{i}</li>)}</ul>}
                  {x.description && <p>{x.description}</p>}
                </div>
              </div>
            ))}
          </div>
        </>
      )}
      {venues.length > 0 && (
        <>
          <h4 className="oc-sec">Venues</h4>
          <table className="oc-table">
            <thead><tr><th>Venue</th><th>{id ? 'Kapasitas per layout' : 'Capacity per layout'}</th><th>{id ? 'Catatan' : 'Notes'}</th></tr></thead>
            <tbody>{venues.map((v) => (
              <tr key={v.id}>
                <td><strong>{v.name}</strong><small>{label(v.venueType)}{v.sizeSqm ? ` · ${v.sizeSqm} m²` : ''}</small></td>
                <td>{v.layouts.length ? v.layouts.map((l) => <span key={l.layout} className="oc-chipline">{label(l.layout)} <b>{l.capacity}</b></span>) : v.maxCapacity ?? '—'}</td>
                <td>{[Number(v.addonPrice) > 0 ? `Add-on ${rp(v.addonPrice)}` : '', v.minPax ? `min. ${v.minPax} pax` : '', v.facilities.join(', ')].filter(Boolean).join(' · ') || '—'}</td>
              </tr>
            ))}</tbody>
          </table>
        </>
      )}
      {menus.length > 0 && (
        <>
          <h4 className="oc-sec">Menu</h4>
          <div className="columns is-multiline oc-units">
            {menus.map((m) => (
              <div key={m.id} className="column is-6">
                <div className="oc-unit">
                  <span className="oc-tag">{label(m.menuType)}</span>
                  <h4>{m.name}</h4>
                  {Number(m.pricePerPax) > 0 && <div className="oc-price">{rp(m.pricePerPax)}{m.pricingMode === 'nett' ? ' nett' : '++'} / pax</div>}
                  {m.items.length > 0 && <p>{m.items.join(' · ')}</p>}
                </div>
              </div>
            ))}
          </div>
        </>
      )}
      <BanquetInquiry propertyId={p.id} lang={lang} />
      <script type="application/ld+json" dangerouslySetInnerHTML={{ __html: JSON.stringify(ld) }} />
    </VenueLayout>
  );
}
