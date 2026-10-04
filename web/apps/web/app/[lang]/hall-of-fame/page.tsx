import { getProperty, pub } from '../../lib';

interface Entry {
  id: string;
  category: string;
  title: string;
  year?: number | null;
  playerName?: string | null;
  teeSet?: string | null;
  score?: number | null;
}

export default async function HallOfFame() {
  const p = await getProperty();
  const entries = p ? (await pub<{ items: Entry[] }>(`/api/v1/public/hall-of-fame?propertyId=${p.id}`))?.items ?? [] : [];
  const groups = new Map<string, Entry[]>();
  for (const e of entries) groups.set(e.category, [...(groups.get(e.category) ?? []), e]);
  return (
    <div className="w-grid">
      <div className="w-card">
        <h1 style={{ marginTop: 0 }}>Hall of Fame</h1>
        <p>Entries are shown only with each player&apos;s consent.</p>
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
