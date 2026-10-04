import { getProperty, pub, type Rate } from '../../lib-p2';
import { VIPRequest } from '../booking';
import { RateTable } from '../rates';

interface Unit {
  id: string;
  name: string;
  facilities: string[];
  blockHours?: number | null;
}

export default async function VIPSuite() {
  const p = await getProperty();
  if (!p) return <div className="w-card">Not available</div>;
  const [stay, rates] = await Promise.all([
    pub<{ vipSuites: Unit[] }>(`/api/v1/public/stay?propertyId=${p.id}`),
    pub<{ items: Rate[] }>(`/api/v1/public/rates/stay?propertyId=${p.id}&limit=200`),
  ]);
  const suites = stay?.vipSuites ?? [];
  return (
    <div className="w-grid">
      {suites.map((s) => (
        <div key={s.id} className="w-card">
          <h2 style={{ marginTop: 0 }}>{s.name}</h2>
          <p>{s.blockHours} hour block{s.facilities.length ? ` · ${s.facilities.join(', ')}` : ''}</p>
        </div>
      ))}
      <RateTable rates={rates?.items.filter((r) => r.serviceType === 'vip_suite')} title="VIP Suite rates" />
      {suites.length > 0 && <VIPRequest propertyId={p.id} suites={suites} />}
    </div>
  );
}
