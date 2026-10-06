import { getProperty, pub } from '../../lib-p2';

interface Entry {
  id: string;
  category: string;
  title: string;
  year?: number | null;
  playerName?: string | null;
  teeSet?: string | null;
  score?: number | null;
}

export default async function HallOfFame({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  const p = await getProperty();
  const entries = p ? (await pub<{ items: Entry[] }>(`/api/v1/public/hall-of-fame?propertyId=${p.id}`))?.items ?? [] : [];
  const groups = new Map<string, Entry[]>();
  // Tournament and Club Champions first (finalized tournaments and imported history, PRD P3 FR-TRN-11).
  const order = (c: string) => (c === 'club_champion' ? 0 : c === 'tournament_champion' ? 1 : 2);
  for (const e of [...entries].sort((a, b) => order(a.category) - order(b.category))) groups.set(e.category, [...(groups.get(e.category) ?? []), e]);
  return (
    <div className="w-grid">
      <div className="w-card">
        <h1 style={{ marginTop: 0 }}>Hall of Fame</h1>
        <p>Entries are shown only with each player&apos;s consent. Members travelling abroad: see our <a href={`/${lang}/golf/reciprocal-clubs`}>Reciprocal Clubs</a>. Champions come from our <a href={`/${lang}/tournaments`}>tournaments</a>.</p>
      </div>
      {[...groups.entries()].map(([cat, list]) => (
        <div key={cat} className="w-card">
          <h2 style={{ marginTop: 0, textTransform: 'capitalize' }}>{cat.replace(/_/g, ' ')}</h2>
          <ul>{list.map((e) => <li key={e.id}>{e.year ? `${e.year} · ` : ''}{e.title} — {e.playerName}{e.score ? ` (${e.score})` : ''}</li>)}</ul>
        </div>
      ))}
    </div>
  );
}
