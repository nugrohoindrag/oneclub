import { getProperty, pub } from '../../../lib-p2';

interface Club {
  code: string;
  name: string;
  country: string;
  city?: string | null;
}

export default async function ReciprocalClubs() {
  const p = await getProperty();
  const clubs = p ? await pub<{ items: Club[] }>(`/api/v1/public/reciprocal-clubs?propertyId=${p.id}`) : null;
  const byCountry = new Map<string, Club[]>();
  for (const c of clubs?.items ?? []) byCountry.set(c.country, [...(byCountry.get(c.country) ?? []), c]);
  return (
    <div className="w-grid">
      <div className="w-card">
        <h1 style={{ marginTop: 0 }}>Reciprocal Clubs</h1>
        <p>Members play at our partner clubs with an introduction letter requested in the Member App.</p>
      </div>
      {[...byCountry.entries()].map(([country, list]) => (
        <div key={country} className="w-card">
          <h2 style={{ marginTop: 0 }}>{country}</h2>
          <ul>{list.map((c) => <li key={c.code}>{c.name}{c.city ? ` — ${c.city}` : ''}</li>)}</ul>
        </div>
      ))}
    </div>
  );
}
