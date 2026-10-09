import { getProperty, pub, type Rate } from '../../../lib-p2';
import { ClassRegistration } from '../../booking';
import { SportCourtBooking } from './courts';
import { UnitCards, VenueLayout } from '../../../../components/mgcc/venue';

interface SportPage {
  facilities: { id: string; code: string; name: string; facilityType?: string | null; usageMode: string }[];
  courts: { id: string; code: string; name: string; facilityId: string; surface?: string | null; indoor: boolean; resourceId?: string | null }[];
  classPrograms: { id: string; name: string; discipline: string; description?: string | null }[];
}

export default async function SportClub({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  const id = lang === 'id';
  const p = await getProperty();
  if (!p) return <div className="w-card">Not available</div>;
  const [page, rates] = await Promise.all([
    pub<SportPage>(`/api/v1/public/sport-club?propertyId=${p.id}`),
    pub<{ items: Rate[] }>(`/api/v1/public/rates/sportclub?propertyId=${p.id}&limit=300`),
  ]);
  return (
    <VenueLayout venue="sport-club" lang={lang} rates={rates?.items}
      title={id ? 'Pesan Fasilitas Sport Club' : 'Book Sport Club Facilities'}
      intro={id ? 'Pesan lapangan tenis, futsal, basket & voli, dan basket indoor, atau daftar kelas renang, tenis lapangan dan aikido.'
        : 'Book tennis, futsal, basket & volley and indoor basketball courts, or join the swimming, tennis and aikido classes.'}>
      <UnitCards units={(page?.facilities ?? []).filter((f) => f.usageMode !== 'slot_booking')
        .map((f) => ({ id: f.id, name: f.name, line: f.facilityType ? f.facilityType.replace(/_/g, ' ') : '' }))} />
      {page && page.courts.length > 0 && <SportCourtBooking lang={id ? 'id' : 'en'} propertyId={p.id} facilities={page.facilities} courts={page.courts} />}
      {page && page.classPrograms.length > 0 && <ClassRegistration lang={id ? 'id' : 'en'} propertyId={p.id} programs={page.classPrograms} />}
    </VenueLayout>
  );
}
