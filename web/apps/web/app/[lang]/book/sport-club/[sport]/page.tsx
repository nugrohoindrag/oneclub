import type { Metadata } from 'next';
import Link from 'next/link';
import { notFound } from 'next/navigation';
import { getProperty, pub } from '../../../../lib-p2';
import { SportCourtBooking } from '../courts';
import { money, sportName, sportSlug, type Facility, type RateCard, type SportClubPage } from '../shared';

/*
 * One sport (docs/requirement-booking-sportclub-mgcc.md FR-08..FR-25): the
 * information block like AYO (photos, description, rules, amenities, the
 * rate table of the brochure, the no-refund policy) above the date tabs,
 * the court cards with their hour grid and the cart.
 */

type Props = { params: Promise<{ lang: string; sport: string }> };

async function load(key: string) {
  const p = await getProperty();
  if (!p) return null;
  const page = await pub<SportClubPage>(`/api/v1/public/sport-club?propertyId=${p.id}`);
  const k = key.toLowerCase();
  const sport = page?.facilities.find((f) => f.usageMode === 'slot_booking' && (sportSlug(f) === k || f.code.toLowerCase() === k || f.id === key));
  return page && sport ? { p, page, sport } : null;
}

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  const { lang, sport } = await params;
  const d = await load(sport);
  if (!d) return {};
  const name = sportName(d.sport, lang === 'id' ? 'id' : 'en');
  const title = d.sport.content?.seoTitle?.[lang] ?? (lang === 'id' ? `Sewa Lapangan ${name} — Sport Club` : `Book a ${name} Court — Sport Club`);
  return { title, description: d.sport.content?.description?.[lang] ?? d.sport.content?.description?.id };
}

const DAY: Record<string, [string, string]> = { WD: ['Senin–Jumat', 'Weekdays'], WE: ['Sabtu, Minggu & Libur', 'Weekends & holidays'], weekday: ['Senin–Jumat', 'Weekdays'],
  weekend: ['Sabtu, Minggu & Libur', 'Weekends & holidays'], holiday: ['Hari libur', 'Holidays'] };

export default async function SportPage({ params }: Props) {
  const { lang: l, sport: key } = await params;
  const lang = l === 'id' ? 'id' : 'en';
  const id = lang === 'id';
  const d = await load(key);
  if (!d) notFound();
  const { p, page, sport } = d;
  const rateCard = await pub<RateCard>(`/api/v1/public/sport-club/rate-card?propertyId=${p.id}`);
  const courts = page.courts.filter((c) => c.facilityId === sport.id && c.onlineBooking !== false).sort((a, b) => a.sortOrder - b.sortOrder);
  const items = new Set(courts.map((c) => c.priceItem));
  const rates = (rateCard?.rates ?? []).filter((r) => items.has(r.item));
  const packages = (rateCard?.packages ?? []).filter((x) => x.items.some((i) => items.has(i)) && !x.code.includes('-G-'));
  const c = sport.content ?? {};
  const rules = c.rules?.[lang] ?? c.rules?.id ?? [];
  return (
    <div className="w-sc-page">
      <nav className="w-muted" aria-label="Breadcrumb"><Link href={`/${lang}/book/sport-club`}>Sport Club</Link> › {sportName(sport, lang)}</nav>
      <h1>{sportName(sport, lang)}</h1>
      {(c.photos ?? []).length > 0 && (
        <div className="w-sc-gallery">{(c.photos ?? []).slice(0, 5).map((src) => <img key={src} src={src} alt={sportName(sport, lang)} loading="lazy" />)}</div>
      )}
      <div className="w-sc-info">
        <div>
          {(c.description?.[lang] ?? c.description?.id) && <p>{c.description?.[lang] ?? c.description?.id}</p>}
          {rules.length > 0 && (<><h2>{id ? 'Aturan lapangan' : 'Court rules'}</h2><ul>{rules.map((r) => <li key={r}>{r}</li>)}</ul></>)}
          {(c.amenities ?? []).length > 0 && (<><h2>{id ? 'Fasilitas' : 'Amenities'}</h2><p>{(c.amenities ?? []).join(' · ')}</p></>)}
          <p className="w-sc-policy">{page.terms?.[lang] ?? page.terms?.id}</p>
        </div>
        {rates.length > 0 && (
          <div className="w-card">
            <h2>{id ? 'Harga per jam' : 'Price per hour'}</h2>
            <table className="w-sc-rates">
              <tbody>
                {rates.map((r, i) => (
                  <tr key={i}>
                    <td>{r.name}<br /><small className="w-muted">{[r.dayType ? (DAY[r.dayType]?.[id ? 0 : 1] ?? r.dayType) : '', r.from && r.to ? `${r.from.slice(0, 5)}–${r.to.slice(0, 5)}` : r.band ?? '',
                      r.minHours > 1 ? (id ? `min. ${r.minHours} jam` : `min. ${r.minHours} hours`) : ''].filter(Boolean).join(' · ')}</small></td>
                    <td style={{ textAlign: 'right' }}>{money(r.price, lang)}</td>
                  </tr>
                ))}
                {packages.map((x) => (
                  <tr key={x.code}><td>{x.name}<br /><small className="w-muted">{id ? `Paket ${Number(x.uses)}x · beli di front desk / Member App` : `${Number(x.uses)}x package · at the front desk / Member App`}</small></td>
                    <td style={{ textAlign: 'right' }}>{money(x.price, lang)}</td></tr>
                ))}
              </tbody>
            </table>
            <p className="w-muted">{page.taxIncluded ? (id ? 'Harga sudah termasuk pajak.' : 'Prices include tax.') : (id ? 'Harga belum termasuk pajak; pajak dan biaya layanan tampil sebelum bayar.' : 'Prices exclude tax; tax and the service fee show before you pay.')}</p>
          </div>
        )}
      </div>
      <SportCourtBooking lang={lang} propertyId={p.id} sport={sport as Facility} courts={courts} methods={page.methods} terms={page.terms}
        windowDays={page.windowDays} taxIncluded={page.taxIncluded} />
    </div>
  );
}
