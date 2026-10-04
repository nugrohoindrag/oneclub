import { getProperty, pub, type Rate } from '../../lib-p2';
import { MeetingBooking } from '../booking';
import { RateTable } from '../rates';

interface Unit {
  id: string;
  name: string;
  facilities: string[];
  sizeSqm?: string | null;
}

export default async function Meeting() {
  const p = await getProperty();
  if (!p) return <div className="w-card">Not available</div>;
  const [stay, rates] = await Promise.all([
    pub<{ meetingRooms: Unit[] }>(`/api/v1/public/stay?propertyId=${p.id}`),
    pub<{ items: Rate[] }>(`/api/v1/public/rates/meeting?propertyId=${p.id}&limit=200`),
  ]);
  const rooms = stay?.meetingRooms ?? [];
  return (
    <div className="w-grid">
      <div className="w-card">
        <h1 style={{ marginTop: 0 }}>Meeting &amp; MICE</h1>
        <ul>{rooms.map((r) => <li key={r.id}>{r.name}{r.sizeSqm ? ` · ${r.sizeSqm} m²` : ''}{r.facilities.length ? ` · ${r.facilities.join(', ')}` : ''}</li>)}</ul>
      </div>
      <RateTable rates={rates?.items} title="Meeting packages & equipment" />
      {rooms.length > 0 && <MeetingBooking propertyId={p.id} rooms={rooms} />}
    </div>
  );
}
