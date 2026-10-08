import { getProperty, pub, type Rate } from '../../../lib-p2';
import { ClassRegistration, CourtBooking } from '../../booking';
import { UnitCards, VenueLayout } from '../../../../components/mgcc/venue';

interface SportPage {
  facilities: { id: string; name: string; facilityType?: string | null }[];
  courts: { id: string; name: string }[];
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
      intro={id ? 'Pesan lapangan tenis, squash dan badminton, atau daftar kelas olahraga.' : 'Book tennis, squash and badminton courts, or join a sport class.'}>
      <UnitCards units={(page?.facilities ?? []).map((f) => ({ id: f.id, name: f.name, line: f.facilityType ? f.facilityType.replace(/_/g, ' ') : '' }))} />
      {page && page.courts.length > 0 && <CourtBooking propertyId={p.id} courts={page.courts} />}
      {page && page.classPrograms.length > 0 && <ClassRegistration propertyId={p.id} programs={page.classPrograms} />}
    </VenueLayout>
  );
}
