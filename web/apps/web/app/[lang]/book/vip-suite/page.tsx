import { getProperty, pub, type Rate } from '../../../lib-p2';
import { VIPRequest } from '../../booking';
import { UnitCards, VenueLayout } from '../../../../components/mgcc/venue';

interface Unit {
  id: string;
  name: string;
  facilities: string[];
  blockHours?: number | null;
}

export default async function VIPSuite({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  const id = lang === 'id';
  const p = await getProperty();
  if (!p) return <div className="w-card">Not available</div>;
  const [stay, rates] = await Promise.all([
    pub<{ vipSuites: Unit[] }>(`/api/v1/public/stay?propertyId=${p.id}`),
    pub<{ items: Rate[] }>(`/api/v1/public/rates/stay?propertyId=${p.id}&limit=200`),
  ]);
  const suites = stay?.vipSuites ?? [];
  return (
    <VenueLayout venue="vip-suite" lang={lang} rates={rates?.items.filter((r) => r.serviceType === 'vip_suite')}
      title={id ? 'Pesan VIP Suite' : 'Book the VIP Suite'}
      intro={id ? 'Ajukan pemesanan VIP Suite per blok waktu. Tim kami mengonfirmasi ketersediaan sebelum pembayaran.'
        : 'Request the VIP Suite per time block. Our team confirms the availability before you pay.'}>
      <UnitCards units={suites.map((s) => ({ id: s.id, name: s.name,
        line: `${s.blockHours} ${id ? 'jam per blok' : 'hour block'}${s.facilities.length ? ` · ${s.facilities.join(', ')}` : ''}` }))} />
      {suites.length > 0 ? <VIPRequest propertyId={p.id} suites={suites} /> : (
        <div className="oc-unit">
          <h4>{id ? 'Reservasi VIP Suite' : 'VIP Suite reservation'}</h4>
          <p>{id ? 'Reservasi VIP Suite saat ini dilayani langsung oleh tim kami.' : 'The VIP Suite is currently reserved through our team.'}</p>
          <a className="btn btn-primary" href={`/${lang}/contact`}>{id ? 'Hubungi Kami' : 'Contact Us'}</a>
        </div>
      )}
    </VenueLayout>
  );
}
