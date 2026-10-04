import { getProperty, pub, type Rate } from '../../lib';
import { ClassRegistration, CourtBooking } from '../booking';
import { RateTable } from '../rates';

interface SportPage {
  facilities: { id: string; name: string; facilityType?: string | null }[];
  courts: { id: string; name: string }[];
  classPrograms: { id: string; name: string; discipline: string; description?: string | null }[];
}

export default async function SportClub() {
  const p = await getProperty();
  if (!p) return <div className="w-card">Not available</div>;
  const [page, rates] = await Promise.all([
    pub<SportPage>(`/api/v1/public/sport-club?propertyId=${p.id}`),
    pub<{ items: Rate[] }>(`/api/v1/public/rates/sportclub?propertyId=${p.id}&limit=300`),
  ]);
  return (
    <div className="w-grid">
      <div className="w-card">
        <h1 style={{ marginTop: 0 }}>Sport Club</h1>
        <ul>{page?.facilities.map((f) => <li key={f.id}>{f.name}{f.facilityType ? ` · ${f.facilityType.replace(/_/g, ' ')}` : ''}</li>)}</ul>
      </div>
      <RateTable rates={rates?.items} title="Sport Club rates" />
      {page && page.courts.length > 0 && <CourtBooking propertyId={p.id} courts={page.courts} />}
      {page && page.classPrograms.length > 0 && <ClassRegistration propertyId={p.id} programs={page.classPrograms} />}
    </div>
  );
}
