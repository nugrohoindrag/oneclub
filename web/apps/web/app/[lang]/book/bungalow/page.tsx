import type { Metadata } from 'next';
import Link from 'next/link';
import { getProperty, pub } from '../../../lib-p2';
import { VenueLayout } from '../../../../components/mgcc/venue';
import { BungalowEngine, type EngineQuery } from './engine';
import { money, typeName, type RateCardRow, type StayPage } from './shared';

export async function generateMetadata({ params }: { params: Promise<{ lang: string }> }): Promise<Metadata> {
  const { lang } = await params;
  return lang === 'id'
    ? { title: 'Pesan Bungalow — Modern Golf & Country Club', description: 'Cek ketersediaan bungalow Birdie, Eagle, Albatros dan VIP Room, lihat harga per tanggal, lalu bayar online.' }
    : { title: 'Book a Bungalow — Modern Golf & Country Club', description: 'Check the availability of the Birdie, Eagle, Albatros and VIP Room bungalows, see the price per date and pay online.' };
}

/** Pesan Bungalow (docs/requirement-booking-hotel-mgcc.md Bagian A): the booking engine with the rate table of the Stay prices (FR-H01). */
export default async function Bungalow({ params, searchParams }: { params: Promise<{ lang: string }>; searchParams: Promise<EngineQuery> }) {
  const { lang: l } = await params;
  const query = await searchParams;
  const lang = l === 'id' ? 'id' : 'en';
  const id = lang === 'id';
  const p = await getProperty();
  if (!p) return <div className="w-card">Not available</div>;
  const [page, card] = await Promise.all([
    pub<StayPage>(`/api/v1/public/stay/types?propertyId=${p.id}`),
    pub<{ items: RateCardRow[] }>(`/api/v1/public/stay/rate-card?propertyId=${p.id}`),
  ]);
  if (!page) return <div className="w-card">{id ? 'Halaman bungalow belum tersedia. Muat ulang sebentar lagi.' : 'The bungalow page is not available. Reload in a moment.'}</div>;
  const rows = card?.items ?? [];
  const table = rows.length > 0 && (
    <table className="oc-rates">
      <tbody>
        {rows.map((r) => (
          <tr key={`${r.typeId}-${r.ratePlan}`}>
            <td>{typeName({ name: r.typeName, nameEn: r.typeNameEn }, lang)} · {r.ratePlanName}
              <small>{r.minNights > 1 ? (id ? `min. ${r.minNights} malam · ` : `min. ${r.minNights} nights · `) : ''}{r.includesBreakfast ? (id ? 'dengan sarapan' : 'with breakfast') : (id ? 'tanpa sarapan' : 'room only')}</small></td>
            <td>{money(r.weekday, lang)}{r.weekend !== r.weekday ? <small>{id ? 'akhir pekan' : 'weekend'} {money(r.weekend, lang)}</small> : null}<small>/ {id ? 'malam' : 'night'}</small></td>
          </tr>
        ))}
      </tbody>
    </table>
  );
  return (
    <VenueLayout venue="bungalow" lang={lang} rateTable={table || undefined}
      title={id ? 'Pesan Bungalow' : 'Book Your Bungalow'}
      intro={id ? 'Pilih tanggal menginap dan jumlah tamu, bandingkan tipe dan rate plan, masukkan ke keranjang, lalu bayar online. Bungalow menghadap lapangan golf, danau atau kolam renang.'
        : 'Choose your dates and guests, compare the room types and rate plans, add them to the cart and pay online. The bungalows face the golf course, the lake or the pool.'}>
      <BungalowEngine lang={lang} propertyId={p.id} page={page} query={query} />
      <p className="w-bk-small"><Link href={`/${lang}/book/bungalow/check`}>{id ? 'Cek Booking — sudah pesan? Lihat status, e-voucher dan My Stay' : 'Check Booking — already booked? See the status, e-voucher and My Stay'}</Link></p>
    </VenueLayout>
  );
}
