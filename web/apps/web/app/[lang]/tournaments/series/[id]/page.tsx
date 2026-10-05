import { getOrderOfMerit } from '../lib';

/** Public Order of Merit of a season (names only with the player's consent; others by initials). */
export default async function SeriesStandings({ params }: { params: Promise<{ lang: string; id: string }> }) {
  const { lang, id: sid } = await params;
  const id = lang === 'id';
  const o = await getOrderOfMerit(sid);
  if (!o) return <p className="w-muted">{id ? 'Seri tidak ditemukan.' : 'Series not found.'}</p>;
  return (
    <div>
      <h1>{o.name} {o.season}</h1>
      <p>
        {o.final ? (id ? 'Klasemen akhir' : 'Final standings') : (id ? 'Klasemen sementara' : 'Current standings')}
        {o.bestOf ? ` · ${id ? `${o.bestOf} hasil terbaik dihitung` : `best ${o.bestOf} results count`}` : ''}
        {o.champion ? ` · ${id ? 'Juara' : 'Champion'}: ${o.champion}` : ''}
      </p>
      <table className="w-table">
        <thead>
          <tr>
            <th>Pos</th><th>{id ? 'Pemain' : 'Player'}</th>
            {o.events.map((e) => <th key={e.id} style={{ textAlign: 'right' }} title={e.tournamentName}>#{e.sequence}{e.isFinal ? ' F' : ''}</th>)}
            <th style={{ textAlign: 'right' }}>{id ? 'Poin' : 'Points'}</th>
          </tr>
        </thead>
        <tbody>
          {o.standings.map((s, i) => (
            <tr key={i}>
              <td>{s.positionLabel}</td><td>{s.playerName}</td>
              {o.events.map((e) => {
                const p = s.eventPoints.find((x) => x.seriesEventId === e.id);
                return <td key={e.id} style={{ textAlign: 'right' }}>{p ? p.points : '—'}</td>;
              })}
              <td style={{ textAlign: 'right' }}><strong>{s.points}</strong></td>
            </tr>
          ))}
        </tbody>
      </table>
      <h2>{id ? 'Turnamen seri' : 'Series events'}</h2>
      <ul>
        {o.events.map((e) => (
          <li key={e.id}>#{e.sequence} {e.tournamentName} · {e.startDate}{e.isFinal ? ` · final ×${e.weight}` : ''}{e.status === 'counted' ? '' : ` (${id ? 'akan datang' : 'upcoming'})`}</li>
        ))}
      </ul>
      <p><a href={`/${lang}/tournaments/series`}>{id ? '← Semua seri' : '← All series'}</a></p>
    </div>
  );
}
