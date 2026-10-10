import type { Metadata } from 'next';
import Link from 'next/link';
import { getProperty, pub, type Rate } from '../../../lib-p2';
import { ClassRegistration } from '../../booking';
import { UnitCards, VenueLayout } from '../../../../components/mgcc/venue';
import { money, sportIcon, sportName, sportSlug, type SportClubPage } from './shared';

export async function generateMetadata({ params }: { params: Promise<{ lang: string }> }): Promise<Metadata> {
  const { lang } = await params;
  return lang === 'id'
    ? { title: 'Sewa Lapangan Sport Club — Tenis, Futsal, Basket & Voli', description: 'Pilih cabor, lihat jadwal kosong dan harga per jam, lalu bayar online.' }
    : { title: 'Book a Sport Club Court — Tennis, Futsal, Basketball & Volleyball', description: 'Choose a sport, see the free hours and prices, and pay online.' };
}

/** Sport Club booking: the sports bookable online (FR-05..08), the other facilities and the classes. */
export default async function SportClub({ params }: { params: Promise<{ lang: string }> }) {
  const { lang: l } = await params;
  const lang = l === 'id' ? 'id' : 'en';
  const id = lang === 'id';
  const p = await getProperty();
  if (!p) return <div className="w-card">Not available</div>;
  const [page, rates] = await Promise.all([
    pub<SportClubPage>(`/api/v1/public/sport-club?propertyId=${p.id}`),
    pub<{ items: Rate[] }>(`/api/v1/public/rates/sportclub?propertyId=${p.id}&limit=300`),
  ]);
  const sports = (page?.facilities ?? []).filter((f) => f.usageMode === 'slot_booking' && f.courts > 0).sort((a, b) => a.sortOrder - b.sortOrder);
  return (
    <VenueLayout venue="sport-club" lang={lang} rates={rates?.items}
      title={id ? 'Sewa Lapangan Sport Club' : 'Book a Sport Club Court'}
      intro={id ? 'Pilih cabang olahraga, lihat jadwal kosong dan harga per jam, masukkan ke keranjang, lalu bayar online. Booking tidak dapat dibatalkan dan tidak ada refund.'
        : 'Choose a sport, see the free hours and the price of each, add them to the cart and pay online. Bookings cannot be cancelled and are not refunded.'}>
      <div className="w-sc-sports">
        {sports.map((f) => (
          <Link key={f.id} href={`/${lang}/book/sport-club/${sportSlug(f)}`} className="w-card w-sc-sport">
            {f.content?.photos?.[0] ? <img src={f.content.photos[0]} alt="" className="w-sc-cover" /> : <span style={{ fontSize: 28 }} aria-hidden="true">{sportIcon(f)}</span>}
            <strong>{sportName(f, lang)}</strong>
            <span className="w-muted">{f.courts} {id ? 'lapangan' : 'courts'} · {f.indoor ? 'Indoor' : 'Outdoor'}</span>
            {f.fromPrice && <span>{id ? 'Mulai' : 'From'} <strong>{money(f.fromPrice, lang)}</strong>/{id ? 'jam' : 'hour'}</span>}
            <span className="w-btn" style={{ justifySelf: 'start' }}>{id ? 'Pesan' : 'Book'}</span>
          </Link>
        ))}
        {sports.length === 0 && <p className="w-muted">{id ? 'Belum ada lapangan yang bisa dipesan online.' : 'No court can be booked online yet.'}</p>}
      </div>
      <p><Link href={`/${lang}/book/sport-club/check`}>{id ? 'Cek Booking — sudah pesan? Lihat QR dan e-ticket Anda' : 'Check Booking — already booked? See your QR and e-ticket'}</Link></p>
      <UnitCards units={(page?.facilities ?? []).filter((f) => f.usageMode !== 'slot_booking')
        .map((f) => ({ id: f.id, name: f.name, line: f.facilityType ? f.facilityType.replace(/_/g, ' ') : '' }))} />
      {page && page.classPrograms.length > 0 && <ClassRegistration lang={lang} propertyId={p.id} programs={page.classPrograms} />}
    </VenueLayout>
  );
}
