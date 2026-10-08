import { getProperty, pub, type Rate } from '../../../lib-p2';
import { MeetingBooking } from '../../booking';
import { BanquetInquiry } from '../../events/forms';
import { UnitCards, VenueLayout } from '../../../../components/mgcc/venue';

interface Unit {
  id: string;
  name: string;
  facilities: string[];
  sizeSqm?: string | null;
}

export default async function Meeting({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  const id = lang === 'id';
  const p = await getProperty();
  if (!p) return <div className="w-card">Not available</div>;
  const [stay, rates] = await Promise.all([
    pub<{ meetingRooms: Unit[] }>(`/api/v1/public/stay?propertyId=${p.id}`),
    pub<{ items: Rate[] }>(`/api/v1/public/rates/meeting?propertyId=${p.id}&limit=200`),
  ]);
  const rooms = stay?.meetingRooms ?? [];
  return (
    <VenueLayout venue="meeting-room" lang={lang} rates={rates?.items}
      title={id ? 'Pesan Meeting Room' : 'Book a Meeting Room'}
      intro={<>{id ? 'Ruang rapat dan venue acara dengan paket meeting dan peralatan. Untuk pernikahan dan banquet, ' : 'Meeting rooms and event venues with meeting packages and equipment. For weddings and banquets, '}
        <a href={`/${lang}/wedding-banquet`}>{id ? 'kirim permintaan di sini' : 'send an inquiry here'}</a>.</>}>
      <UnitCards units={rooms.map((r) => ({ id: r.id, name: r.name,
        line: `${r.sizeSqm ? `${r.sizeSqm} m²` : ''}${r.sizeSqm && r.facilities.length ? ' · ' : ''}${r.facilities.join(', ')}` }))} />
      {rooms.length > 0 ? <MeetingBooking propertyId={p.id} rooms={rooms} /> : (
        // no meeting room is bookable online yet: the MICE inquiry reaches the sales team
        <BanquetInquiry propertyId={p.id} lang={lang} initial="mice" />
      )}
    </VenueLayout>
  );
}
