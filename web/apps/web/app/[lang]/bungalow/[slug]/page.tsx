import type { Metadata } from 'next';
import { notFound } from 'next/navigation';
import { getProperty, pub } from '../../../lib-p2';
import { StaySearchBar } from '../../../../components/mgcc/stay-search';
import { AMENITY_GROUP, VIEW, money, typeName, type RateCardRow, type StayPage } from '../../book/bungalow/shared';

// A page per room type (docs/requirement-booking-hotel-mgcc.md §11 SEO,
// FR-H18, FR-H73): its own title and description, the gallery, the facts,
// the amenities by group, the house rules, the FAQ and the prices of the
// Stay rate card, with the search bar of the booking.

type Props = { params: Promise<{ lang: string; slug: string }> };

async function load(slug: string) {
  const p = await getProperty();
  if (!p) return null;
  const [page, card] = await Promise.all([
    pub<StayPage>(`/api/v1/public/stay/types?propertyId=${p.id}`),
    pub<{ items: RateCardRow[] }>(`/api/v1/public/stay/rate-card?propertyId=${p.id}`),
  ]);
  const type = page?.types.find((t) => t.slug.toLowerCase() === slug.toLowerCase());
  if (!page || !type) return null;
  return { page, type, rates: (card?.items ?? []).filter((r) => r.typeId === type.id) };
}

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  const { lang, slug } = await params;
  const x = await load(slug);
  if (!x) return { title: 'Bungalow' };
  const name = typeName(x.type, lang === 'en' ? 'en' : 'id');
  const desc = (lang === 'en' ? x.type.descriptionEn || x.type.description : x.type.description) ?? '';
  return {
    title: `${name} — Bungalow Modern Golf & Country Club`,
    description: desc || (lang === 'id' ? `${name}: ${x.type.bedrooms} kamar tidur, maksimal ${x.type.maxAdults} dewasa. Pesan online.`
      : `${name}: ${x.type.bedrooms} bedroom(s), up to ${x.type.maxAdults} adults. Book online.`),
    alternates: { languages: { id: `/id/bungalow/${x.type.slug}`, en: `/en/bungalow/${x.type.slug}` } },
    openGraph: x.type.photos[0] ? { images: [x.type.photos[0]] } : undefined,
  };
}

export default async function RoomTypePage({ params }: Props) {
  const { lang: l, slug } = await params;
  const lang = l === 'id' ? 'id' : 'en';
  const id = lang === 'id';
  const x = await load(slug);
  if (!x) notFound();
  const { page, type: t, rates } = x;
  const groups = Object.entries(t.amenities.reduce<Record<string, string[]>>((m, a) => {
    (m[a.group] ??= []).push(id ? a.label : a.labelEn || a.label);
    return m;
  }, {}));
  return (
    <div className="w-bk w-bk-typepage">
      <h1>{typeName(t, lang)}</h1>
      {t.photos.length > 0 && (
        <div className="w-sc-gallery">{t.photos.slice(0, 5).map((p, i) => <img key={p} src={p} alt={`${typeName(t, lang)} ${i + 1}`} />)}</div>
      )}
      <StaySearchBar lang={lang} maxDate={page.maxDate} typeId={t.id} title={id ? `Pesan ${typeName(t, lang)}` : `Book the ${typeName(t, lang)}`} />
      <div className="w-sc-info">
        <div className="w-card">
          <p>{(id ? t.description : t.descriptionEn || t.description) ?? ''}</p>
          <ul>
            {t.sizeSqm && <li>{Number(t.sizeSqm)} m²</li>}
            <li>{t.bedrooms} {id ? 'kamar tidur' : 'bedroom(s)'}{t.bedConfiguration ? ` · ${t.bedConfiguration}` : ''}</li>
            <li>{id ? 'Maksimal' : 'Up to'} {t.maxAdults} {id ? 'dewasa' : 'adults'}{t.maxChildren ? ` + ${t.maxChildren} ${id ? 'anak' : 'children'}` : ''}</li>
            {t.views.length > 0 && <li>View {t.views.map((v) => VIEW[v]?.[id ? 0 : 1] ?? v).join(' / ')}{t.views.length > 1 ? (id ? ' (tergantung ketersediaan)' : ' (subject to availability)') : ''}</li>}
          </ul>
          {groups.map(([g, list]) => <p key={g}><strong>{AMENITY_GROUP[g]?.[id ? 0 : 1] ?? g}:</strong> {list.join(' · ')}</p>)}
          <h3>{id ? 'Aturan rumah' : 'House rules'}</h3>
          <p style={{ whiteSpace: 'pre-wrap' }}>{t.houseRules || (id ? page.houseRules : page.houseRulesEn || page.houseRules)}</p>
          <p>Check-in {page.checkInTime} · Check-out {page.checkOutTime}</p>
        </div>
        <div className="w-card">
          <h3>{id ? 'Tarif per malam' : 'Rates per night'}</h3>
          <table className="w-sc-rates"><tbody>
            {rates.map((r) => (
              <tr key={r.ratePlan}><td>{r.ratePlanName}{r.minNights > 1 ? <small> · min. {r.minNights} {id ? 'malam' : 'nights'}</small> : null}</td>
                <td>{money(r.weekday, lang)}{r.weekend !== r.weekday ? <small> · {id ? 'akhir pekan' : 'weekend'} {money(r.weekend, lang)}</small> : null}</td></tr>
            ))}
          </tbody></table>
          {t.faq.length > 0 && (
            <>
              <h3>FAQ</h3>
              {t.faq.map((f, i) => <details key={i}><summary>{id ? f.q : f.qEn || f.q}</summary><p>{id ? f.a : f.aEn || f.a}</p></details>)}
            </>
          )}
        </div>
      </div>
    </div>
  );
}
