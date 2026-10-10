import type { ReactNode } from 'react';
import { rp, type Rate } from '../../app/lib-p2';

// Booking venue pages (Book Bungalow, VIP Suite, Meeting Room, Sport Club)
// in the layout of the live site's inner pages: the article with the
// booking on the right, the "Fees & Rates" sidebar on the left (the club's
// rate flyer, then the live rates of the Pricing Engine).

const FLYER: Record<string, string> = {
  bungalow: '/storage/app/uploads/public/67a/c19/d35/67ac19d35d48e805861055.jpg',
  'vip-suite': '/storage/app/uploads/public/67a/c1f/f24/67ac1ff2494ac852825907.jpg',
  'sport-club': '/storage/app/uploads/public/67a/c12/256/67ac1225631a2503865950.jpg',
  'meeting-room': '/storage/app/uploads/public/631/88f/a3b/63188fa3b8356133011351.jpg',
};

export function VenueLayout({ venue, title, intro, rates, rateTable, lang, children }: {
  venue: keyof typeof FLYER; title: string; intro: ReactNode; rates?: Rate[]; rateTable?: ReactNode; lang: string; children: ReactNode;
}) {
  return (
    <div className="columns is-rtl oc-venue">
      <div className="column is-8">
        <div className="article">
          <h3>{title}</h3>
          <p>{intro}</p>
          {children}
        </div>
      </div>
      <div className="column is-4 left-sidebar">
        <div className="wi-title">Fees &amp; Rates</div>
        <figure><img src={FLYER[venue]} alt="Fees & Rates" /></figure>
        {rateTable}
        {!rateTable && rates && rates.length > 0 && (
          <table className="oc-rates">
            <tbody>
              {rates.map((r, i) => (
                <tr key={i}>
                  <td>{r.name}{r.ratePlan ? ` · ${r.ratePlan}` : ''}{r.package ? ` · ${r.package}` : ''}
                    {r.dayType && r.dayType !== 'any' ? <small>{r.dayType}{r.timeBand ? ` · ${r.timeBand}` : ''}</small> : null}</td>
                  <td>{rp(r.price)}<small>/ {r.unit}</small></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <p className="oc-venue-help">{lang === 'id' ? 'Harga dapat berubah sesuai tanggal dan paket. Total final tampil sebelum Anda membayar.' : 'Rates depend on the date and package. You see the final total before you pay.'}</p>
      </div>
    </div>
  );
}

/** A bookable unit shown as a card (type, capacity, facilities). */
export function UnitCards({ units }: { units: { id: string; name: string; description?: string | null; line: string }[] }) {
  return (
    <div className="columns is-multiline oc-units">
      {units.map((u) => (
        <div key={u.id} className="column is-6">
          <div className="oc-unit">
            <h4>{u.name}</h4>
            {u.description && <p>{u.description}</p>}
            <span>{u.line}</span>
          </div>
        </div>
      ))}
    </div>
  );
}
