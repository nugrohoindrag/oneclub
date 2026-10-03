import type { Lang } from '../../../lib';
import { GolfSection } from '../shared';

const FACILITIES: [string, string][] = [
  ['Caddies', 'Every flight plays with professional caddies (one per player).'],
  ['Golf carts', 'Electric golf carts, shared by two players.'],
  ['Locker rooms', 'Separate locker rooms for men and women.'],
  ['Bag drop & storage', 'Drop your bag on arrival; long-term bag storage is available for members.'],
  ['Night golf', 'Floodlit Front Nine in the evening.'],
];

/** Facilities (PRD P1 §6.4). */
export default async function Facilities({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  return (
    <GolfSection lang={lang as Lang} current="/facilities" title="Facilities">
      <div className="w-grid">
        {FACILITIES.map(([t, d]) => (
          <div key={t} className="w-card"><h2>{t}</h2><p style={{ margin: 0 }}>{d}</p></div>
        ))}
      </div>
    </GolfSection>
  );
}
