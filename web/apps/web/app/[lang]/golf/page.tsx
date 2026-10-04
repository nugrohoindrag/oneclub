import { getGolfInfo, getRates, idr, type Lang } from '../../lib';
import { GolfSection, Unavailable } from './shared';

/** Golf Course and published rates (FR-WEB-01/04). */
export default async function GolfCourse({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  const l = lang as Lang;
  const [info, rates] = await Promise.all([getGolfInfo(), getRates()]);
  return (
    <GolfSection lang={l} current="" title="Golf Course">
      {!info && <Unavailable />}
      <div className="w-grid">
        {info?.courses.map((c) => (
          <div key={c.id} className="w-card">
            <h2>{c.name}</h2>
            <p>{[`${c.holes} holes`, c.par ? `par ${c.par}` : '', c.lengthMeters ? `${new Intl.NumberFormat(l).format(c.lengthMeters)} m` : ''].filter(Boolean).join(' · ')}</p>
            {c.description && <p>{c.description}</p>}
          </div>
        ))}
      </div>
      {rates.length > 0 && (
        <div className="w-card" style={{ marginTop: 16 }}>
          <h2>Green fee (all-in)</h2>
          <table className="w-table">
            <thead><tr><th>Player</th><th>Day</th><th>Session</th><th>Price</th></tr></thead>
            <tbody>
              {rates.map((r, i) => (
                <tr key={i}><td>{r.segment.replace(/_/g, ' ')}</td><td>{r.dayType || 'Every day'}</td><td>{r.timeBand || 'All day'}</td><td>{idr(r.price, l)}</td></tr>
              ))}
            </tbody>
          </table>
          <p className="w-muted">Prices include green fee, caddy fee, buggy fee, HIO and tax{rates[0]?.pricingMode === 'nett' ? '' : ' (++)'}.</p>
        </div>
      )}
      {info && (
        <div className="w-card" style={{ marginTop: 16 }}>
          <h2>Club rules</h2>
          <p>{info.clubRules}</p>
          <p><strong>Dress code:</strong> {info.dressCode}</p>
          <p className="w-muted">Online booking opens {info.bookingWindowDays} days ahead · up to {info.maxPlayers} players per flight.</p>
        </div>
      )}
    </GolfSection>
  );
}
