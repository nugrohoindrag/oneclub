import { getTournament } from '../../lib';
import { LiveLeaderboard } from './live';

/** Public leaderboard (opt-in per tournament; names only with the player's consent — PRD P3 FR-WEB-P3-06). */
export default async function LeaderboardPage({ params }: { params: Promise<{ lang: string; id: string }> }) {
  const { lang, id: tid } = await params;
  const { propertyId, t } = await getTournament(tid);
  if (!t) return <p className="w-muted">{lang === 'id' ? 'Turnamen tidak ditemukan.' : 'Tournament not found.'}</p>;
  return (
    <div>
      <h1>{t.name} · Leaderboard</h1>
      <LiveLeaderboard lang={lang} propertyId={propertyId} tournamentId={t.id} />
    </div>
  );
}
