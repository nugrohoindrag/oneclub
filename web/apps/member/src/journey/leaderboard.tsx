import React, { useState } from 'react';
import { qs, useGet, type Page, type Schemas } from '@oneclub/api-client';
import { ErrorAlert, Icon, Skeleton } from '@oneclub/shell';
import { dayLabel, Head } from './ui';

// Leaderboard (demo feedback 9 Oct 2026): the Weekly Hole Leader, the
// Monthly Hole Record and the Top Player of the week / month, ranked on the
// score — never on the play time.

type Board = Schemas['ScoreBoard'];

const toPar = (n: number) => (n === 0 ? 'E' : n > 0 ? `+${n}` : String(n));

export function LeaderboardPage() {
  const [period, setPeriod] = useState<'week' | 'month'>('week');
  const d = useGet<Board>(`/api/v1/member/golf/leaderboard${qs({ period })}`);
  const x = d.data;
  return (
    <div className="mj-page mj-narrow">
      <Head title="Leaderboard" back={['/activity', 'My Activity']} help={x ? `${dayLabel(x.from, { day: 'numeric', month: 'short' })} – ${dayLabel(x.to, { day: 'numeric', month: 'short' })}` : undefined} />
      <div className="mj-seg" role="group" aria-label="Period">
        <button type="button" aria-pressed={period === 'week'} onClick={() => setPeriod('week')}>This week</button>
        <button type="button" aria-pressed={period === 'month'} onClick={() => setPeriod('month')}>This month</button>
      </div>
      <ErrorAlert error={d.error} />
      {d.isLoading && <Skeleton rows={6} />}
      {x && (
        <>
          <div className="mj-card">
            <h2><Icon name="emoji_events" size={20} /> Top Player of the {period === 'week' ? 'Week' : 'Month'}</h2>
            {x.topPlayers.length === 0 && <p className="mj-muted">No complete 18-hole round yet.</p>}
            <div className="mj-list">
              {x.topPlayers.map((p) => (
                <div key={`${p.rank}-${p.name}`} className="mj-item">
                  <span className="mj-avatar">{p.rank}</span>
                  <div className="mj-item-body"><strong>{p.name}</strong><span className="mj-small mj-muted">{dayLabel(p.playedOn, { weekday: 'short', day: 'numeric', month: 'short' })} · {p.rounds} round{p.rounds === 1 ? '' : 's'}</span></div>
                  <div className="mj-item-end"><strong className="mj-num" style={{ fontSize: 20 }}>{p.gross}</strong><span className="mj-small mj-muted">{toPar(p.toPar)}</span></div>
                </div>
              ))}
            </div>
          </div>
          <div className="mj-card">
            <h2><Icon name="flag" size={20} /> {period === 'week' ? 'Weekly Hole Leader' : 'Monthly Hole Record'}</h2>
            {x.holeLeaders.length === 0 && <p className="mj-muted">No scores entered in this period yet.</p>}
            <div className="mj-list">
              {x.holeLeaders.map((h) => (
                <div key={h.holeId} className="mj-item">
                  <span className="mj-avatar">{h.holeNumber}</span>
                  <div className="mj-item-body">
                    <strong>{h.players.map((p) => p.name).join(', ')}</strong>
                    <span className="mj-small mj-muted">Hole {h.sectionCode}-{h.holeNumber} · par {h.par}{h.courseName ? ` · ${h.courseName}` : ''}</span>
                  </div>
                  <div className="mj-item-end"><strong className="mj-num" style={{ fontSize: 20 }}>{h.strokes}</strong><span className="mj-small mj-muted">{toPar(h.toPar)}</span></div>
                </div>
              ))}
            </div>
          </div>
        </>
      )}
    </div>
  );
}

/** Best score per hole among the players of a booking. */
export function HoleBests({ bookingId }: { bookingId: string }) {
  const d = useGet<Page<Schemas['HoleBest']>>(`/api/v1/member/bookings/${bookingId}/hole-bests`, { retry: false });
  const items = d.data?.items ?? [];
  if (!items.length) return null;
  return (
    <div className="mj-card">
      <h2><Icon name="flag" size={20} /> Best per hole</h2>
      <div className="mj-list">
        {items.map((h) => (
          <div key={h.seq} className="mj-item">
            <span className="mj-avatar">{h.holeNumber}</span>
            <div className="mj-item-body"><strong>{h.players.join(', ')}</strong><span className="mj-small mj-muted">Hole {h.sectionCode}-{h.holeNumber} · par {h.par}</span></div>
            <div className="mj-item-end"><strong className="mj-num">{h.strokes}</strong><span className="mj-small mj-muted">{toPar(h.strokes - h.par)}</span></div>
          </div>
        ))}
      </div>
    </div>
  );
}
