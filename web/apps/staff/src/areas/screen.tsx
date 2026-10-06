import { useEffect, useState } from 'react';
import { useRoutes } from 'react-router';
import { useGet, type Schemas } from '@oneclub/api-client';
import { Icon, NotFoundPage, UserMenu, useAuth } from '@oneclub/shell';
import { LeaderboardScreenPage } from '../p3/tournament';

// Clubhouse Screen area (`/screen`, FR-HOF-05): the TV in the clubhouse,
// signed in with the Screen role on the dashboard domain. Full screen without
// header or menus; the content refreshes and rotates by itself (Technical Doc §6.1).

function ClubhouseScreenPage() {
  const { propertyId } = useAuth();
  const feed = useGet<Schemas['Kiosk']>(propertyId ? `/api/v1/public/hall-of-fame/kiosk?propertyId=${propertyId}` : null, { refetchInterval: 5 * 60_000 });
  const [i, setI] = useState(0);
  const entries = feed.data?.entries ?? [];
  useEffect(() => {
    const t = setInterval(() => setI((x) => x + 1), (feed.data?.rotateSeconds ?? 12) * 1000);
    return () => clearInterval(t);
  }, [feed.data?.rotateSeconds]);
  const e = entries.length > 0 ? entries[i % entries.length] : null;
  return (
    <main className="oc-screen">
      {/* Hidden until hovered or focused: logout, or another area for an administrator. */}
      <div className="oc-screen-menu"><UserMenu /></div>
      <Icon name="emoji_events" size={72} />
      {e ? (
        <>
          <div className="oc-small" style={{ opacity: 0.7, textTransform: 'uppercase', letterSpacing: 2 }}>{e.category.replace(/_/g, ' ')}</div>
          <h1 style={{ fontSize: 56, margin: '12px 0' }}>{e.title}</h1>
          <div style={{ fontSize: 32 }}>{e.playerName}</div>
          <div style={{ opacity: 0.7, fontSize: 20 }}>{e.year ?? ''}{e.teeSet ? ` · ${e.teeSet}` : ''}{e.score ? ` · ${e.score}` : ''}</div>
        </>
      ) : (
        <>
          <h1 style={{ fontSize: 56, margin: '12px 0' }}>Hall of Fame</h1>
          {feed.isSuccess && <div style={{ opacity: 0.7, fontSize: 20 }}>No published entries yet.</div>}
        </>
      )}
    </main>
  );
}

const routes = [
  { index: true, element: <ClubhouseScreenPage /> },
  { path: 'leaderboard', element: <LeaderboardScreenPage /> }, // PRD P3 FR-OPS-P3-04 Leaderboard Screen
  { path: '*', element: <NotFoundPage /> },
];

export default function ScreenArea() {
  return useRoutes(routes);
}
