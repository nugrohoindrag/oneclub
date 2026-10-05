import { getSeriesList } from './lib';

/** Order of Merit seasons of the club (PRD P5 EP-23, public series leaderboard with consent). */
export default async function SeriesList({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  const id = lang === 'id';
  const items = await getSeriesList();
  return (
    <div>
      <h1>Order of Merit</h1>
      <p>{id ? 'Poin dari setiap turnamen seri sepanjang musim. Nama pemain tampil hanya dengan persetujuan pemain.'
        : 'Points from every event of the season. Player names are shown only with the player\'s consent.'}</p>
      {items.length === 0 && <p className="w-muted">{id ? 'Belum ada seri turnamen.' : 'No tournament series yet.'}</p>}
      <div className="w-grid">
        {items.map((s) => (
          <div key={s.id} className="w-card">
            <h2>{s.name} {s.season}</h2>
            <p>{s.countedEvents} / {s.events} {id ? 'turnamen dimainkan' : 'events played'}{s.status === 'completed' ? ` · ${id ? 'final' : 'final'}` : ''}</p>
            {s.champion && <p>{id ? 'Juara' : 'Champion'}: <strong>{s.champion}</strong></p>}
            <a className="w-btn" href={`/${lang}/tournaments/series/${s.id}`}>{id ? 'Klasemen' : 'Standings'}</a>
          </div>
        ))}
      </div>
      <p><a href={`/${lang}/tournaments`}>{id ? '← Turnamen' : '← Tournaments'}</a></p>
    </div>
  );
}
