import { idr, type Rate } from '../lib';

/** Structured rate table from pricing (FR-WEB-P2-05) — never a flyer image. */
export function RateTable({ rates, title }: { rates: Rate[] | undefined; title: string }) {
  if (!rates || rates.length === 0) return null;
  return (
    <div className="w-card">
      <h2 style={{ marginTop: 0 }}>{title}</h2>
      <table className="w-table">
        <thead><tr><th>Item</th><th>Guest type</th><th>Day</th><th>Time</th><th>Unit</th><th style={{ textAlign: 'right' }}>Price</th></tr></thead>
        <tbody>
          {rates.map((r, i) => (
            <tr key={i}>
              <td>{r.name}{r.ratePlan ? ` · ${r.ratePlan}` : ''}{r.package ? ` · ${r.package}` : ''}</td>
              <td>{r.segment === 'any' ? 'All' : r.segment.replace(/_/g, ' ')}</td>
              <td>{r.dayType ?? 'All'}</td>
              <td>{r.timeBand ?? '—'}</td>
              <td>{r.unit}</td>
              <td style={{ textAlign: 'right' }}>{idr(r.price)}{r.pricingMode === 'plus_plus' ? '++' : ''}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
