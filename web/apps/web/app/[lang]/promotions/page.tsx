import { getProperty, pub } from '../../lib-p2';
import { idr, type Lang } from '../../lib';
import { PromoCodeCheck } from './check';

/** A promotion as the public API lists it (contract K5). */
interface PublicPromotion {
  id: string; code: string; name: string; description?: string | null; promoType: string; discountPercent?: string | null; discountAmount?: string | null;
  buyQuantity?: number | null; getQuantity?: number | null; bundlePrice?: string | null; validFrom?: string | null; validTo?: string | null;
  timeWindows: { days?: number[]; start: string; end: string }[]; minPurchase?: string | null; businessLines: string[];
}

const DAYS: Record<Lang, string[]> = { id: ['', 'Sen', 'Sel', 'Rab', 'Kam', 'Jum', 'Sab', 'Min'], en: ['', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'] };

function headline(p: PublicPromotion, lang: Lang) {
  const id = lang === 'id';
  switch (p.promoType) {
    case 'buy_n_get_x': return id ? `Beli ${p.buyQuantity} gratis ${p.getQuantity}` : `Buy ${p.buyQuantity}, get ${p.getQuantity} free`;
    case 'buy_n_price_x': return id ? `${p.buyQuantity} hanya ${idr(p.bundlePrice ?? 0, lang)}` : `${p.buyQuantity} for only ${idr(p.bundlePrice ?? 0, lang)}`;
    case 'bundle': return id ? `Paket hemat ${idr(p.bundlePrice ?? 0, lang)}` : `Bundle for ${idr(p.bundlePrice ?? 0, lang)}`;
    default: return p.discountPercent ? (id ? `Diskon ${p.discountPercent}%` : `${p.discountPercent}% off`)
      : p.discountAmount ? (id ? `Potongan ${idr(p.discountAmount, lang)}` : `${idr(p.discountAmount, lang)} off`) : '';
  }
}

/** Promotions page from the structured promotion data (PRD P3 FR-WEB-P3-01, FR-WEB-P3-07). */
export default async function Promotions({ params }: { params: Promise<{ lang: string }> }) {
  const { lang: l } = await params;
  const lang = l as Lang;
  const id = lang === 'id';
  const p = await getProperty();
  if (!p) return <div className="w-card">Not available</div>;
  const data = await pub<{ items: PublicPromotion[] }>(`/api/v1/public/promotions?propertyId=${p.id}`);
  const items = data?.items ?? [];
  return (
    <div className="w-grid">
      <div className="w-card">
        <h1 style={{ marginTop: 0 }}>{id ? 'Promo' : 'Promotions'}</h1>
        <p>{id ? 'Penawaran terbaru di club. Promo berlaku otomatis di kasir, website dan aplikasi member.'
          : 'The latest offers at the club. Promotions apply automatically at the cashier, on the website and in the member app.'}</p>
      </div>
      {items.length === 0 && <div className="w-card"><p className="w-muted">{id ? 'Belum ada promo saat ini.' : 'No promotions right now.'}</p></div>}
      {items.map((x) => (
        <div key={x.id} className="w-card">
          <h2 style={{ marginTop: 0 }}>{x.name}</h2>
          <p><strong>{headline(x, lang)}</strong></p>
          {x.description && <p>{x.description}</p>}
          <p className="w-muted">
            {x.timeWindows.map((w) => `${(w.days ?? []).map((d) => DAYS[lang][d]).join(', ') || (id ? 'Setiap hari' : 'Daily')} ${w.start}–${w.end}`).join(' · ')}
            {x.validTo ? ` · ${id ? 'sampai' : 'until'} ${x.validTo}` : ''}
            {x.minPurchase ? ` · ${id ? 'min. belanja' : 'min. purchase'} ${idr(x.minPurchase, lang)}` : ''}
          </p>
        </div>
      ))}
      <PromoCodeCheck lang={lang} propertyId={p.id} />
    </div>
  );
}
