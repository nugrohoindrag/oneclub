import { getProperty, pub, type Rate } from '../../lib-p2';
import { BungalowBooking } from '../booking';
import { RateTable } from '../rates';

interface Unit {
  id: string;
  name: string;
  description?: string | null;
  maxAdults?: number | null;
  bedrooms?: number | null;
  facilities: string[];
}

export default async function Bungalow({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  const p = await getProperty();
  if (!p) return <div className="w-card">Not available</div>;
  const [stay, rates] = await Promise.all([
    pub<{ bungalowTypes: Unit[] }>(`/api/v1/public/stay?propertyId=${p.id}`),
    pub<{ items: Rate[] }>(`/api/v1/public/rates/stay?propertyId=${p.id}&limit=200`),
  ]);
  const types = stay?.bungalowTypes ?? [];
  return (
    <div className="w-grid">
      <div className="w-card">
        <h1 style={{ marginTop: 0 }}>Stay &amp; Venue</h1>
        <p>Bungalows below; see also the <a href={`/${lang}/vip-suite`}>VIP Suite</a> and <a href={`/${lang}/meeting`}>Meeting Rooms</a>.</p>
      </div>
      {types.map((t) => (
        <div key={t.id} className="w-card">
          <h2 style={{ marginTop: 0 }}>{t.name}</h2>
          <p>{t.description}</p>
          <p className="w-muted">{t.bedrooms ?? 1} bedroom · up to {t.maxAdults ?? 2} adults{t.facilities.length ? ` · ${t.facilities.join(', ')}` : ''}</p>
        </div>
      ))}
      <RateTable rates={rates?.items.filter((r) => r.serviceType === 'bungalow')} title="Bungalow rates" />
      {types.length > 0 && <BungalowBooking propertyId={p.id} types={types} />}
    </div>
  );
}
