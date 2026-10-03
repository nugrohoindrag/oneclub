import { getGolfInfo, type Lang } from '../../../lib';
import { GolfSection, Unavailable } from '../shared';

/** Hole-by-Hole (FR-WEB-03): par, stroke index and distance per tee set. */
export default async function HoleByHole({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  const info = await getGolfInfo();
  return (
    <GolfSection lang={lang as Lang} current="/hole-by-hole" title="Hole-by-Hole">
      {!info && <Unavailable />}
      {info?.courses.map((c) => {
        const route = c.routes.reduce<(typeof c.routes)[number] | undefined>((best, r) => (!best || r.holeCount > best.holeCount ? r : best), undefined);
        if (!route) return null;
        return (
          <div key={c.id} className="w-card" style={{ marginBottom: 16, overflowX: 'auto' }}>
            <h2>{c.name} · {route.name}</h2>
            <table className="w-table">
              <thead><tr><th>Hole</th><th>Par</th><th>SI</th>{c.teeSets.map((t) => <th key={t.code}>{t.name} (m)</th>)}</tr></thead>
              <tbody>
                {route.holes.map((h) => (
                  <tr key={h.number}><td>{h.number}</td><td>{h.par}</td><td>{h.strokeIndex ?? '—'}</td>{c.teeSets.map((t) => <td key={t.code}>{h.distances?.[t.code] ?? '—'}</td>)}</tr>
                ))}
              </tbody>
            </table>
          </div>
        );
      })}
    </GolfSection>
  );
}
