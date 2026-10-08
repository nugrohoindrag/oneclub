import { getProperty, pub, type Rate } from '../../../lib-p2';
import { BungalowBooking } from '../../booking';
import { UnitCards, VenueLayout } from '../../../../components/mgcc/venue';

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
  const id = lang === 'id';
  const p = await getProperty();
  if (!p) return <div className="w-card">Not available</div>;
  const [stay, rates] = await Promise.all([
    pub<{ bungalowTypes: Unit[] }>(`/api/v1/public/stay?propertyId=${p.id}`),
    pub<{ items: Rate[] }>(`/api/v1/public/rates/stay?propertyId=${p.id}&limit=200`),
  ]);
  const types = stay?.bungalowTypes ?? [];
  return (
    <VenueLayout venue="bungalow" lang={lang} rates={rates?.items.filter((r) => r.serviceType === 'bungalow')}
      title={id ? 'Pesan Bungalow' : 'Book Your Bungalow'}
      intro={id ? 'Pilih tipe bungalow dan tanggal menginap. Semua bungalow menghadap lapangan golf, danau atau kolam renang.'
        : 'Choose your bungalow type and dates. Every bungalow faces the golf course, the lake or the pool.'}>
      <UnitCards units={types.map((t) => ({ id: t.id, name: t.name, description: t.description,
        line: `${t.bedrooms ?? 1} ${id ? 'kamar tidur' : 'bedroom'} · ${id ? 'maks.' : 'up to'} ${t.maxAdults ?? 2} ${id ? 'dewasa' : 'adults'}${t.facilities.length ? ` · ${t.facilities.join(', ')}` : ''}` }))} />
      {types.length > 0 && <BungalowBooking propertyId={p.id} types={types} />}
    </VenueLayout>
  );
}
