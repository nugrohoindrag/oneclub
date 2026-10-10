import React from 'react';
import { Link } from 'react-router';
import { useGet, type Page, type Schemas } from '@oneclub/api-client';
import { formatDate } from '@oneclub/i18n';
import { Empty, Icon, PlayTime, Skeleton } from '@oneclub/shell';
import { DateBadge } from './home';
import { Chip, Head, money, StatusChip } from './ui';

// My Activity (post-visit): the member's bookings across golf, stay and
// events, the golf history with scores, and the stay history. Over time it
// becomes the member's personal activity history.

type Golf = Schemas['BookingSummary'];
type Stay = Schemas['Stay'];
type Ticket = Schemas['EventTicket'];

interface Entry {
  key: string; when: string; icon: string; kind: string; title: string; sub: string; status: string; to?: string; amount?: string | null;
  /** golf: actual play time (tee-off → round finish) */
  play?: [string | null | undefined, string | null | undefined, string | null | undefined, number | undefined];
}

const STAY_KIND: Record<string, string> = { bungalow: 'Bungalow', vip_suite: 'VIP Suite', meeting_room: 'Meeting Room' };
const OPEN_GOLF = ['draft', 'pending', 'confirmed', 'checked_in'];
const OPEN_STAY = ['requested', 'reserved', 'checked_in'];

export function ActivityPage() {
  const golf = useGet<Page<Golf>>('/api/v1/member/bookings');
  const stays = useGet<Page<Stay>>('/api/v1/member/stays', { retry: false });
  const events = useGet<Schemas['MyEvents']>('/api/v1/member/my-events', { retry: false });
  const now = Date.now();
  const entries: (Entry & { upcoming: boolean })[] = [
    ...(golf.data?.items ?? []).filter((b) => b.status !== 'draft').map((b) => ({
      key: `g${b.id}`, when: b.startAt, icon: 'sports_golf', kind: 'Tee Time', title: `${b.localTime} · ${b.courseName}`, sub: `${b.playerCount} players · ${b.code}`,
      status: b.status, to: `/bookings/${b.id}`, amount: b.total, upcoming: OPEN_GOLF.includes(b.status) && new Date(b.startAt).getTime() > now - 6 * 3600_000,
      play: [b.teeOffAt, b.roundFinishAt, b.pausedAt, b.pausedSeconds] as Entry['play'],
    })),
    ...(stays.data?.items ?? []).map((s) => ({
      key: `s${s.id}`, when: s.start, icon: s.kind === 'meeting_room' ? 'meeting_room' : 'cottage', kind: STAY_KIND[s.kind] ?? s.kind, title: s.unitName,
      sub: `${formatDate(s.start)} – ${formatDate(s.end)} · ${s.stayNo}`, status: s.status, to: '/activity/stays', upcoming: OPEN_STAY.includes(s.status) && new Date(s.end).getTime() > now,
    })),
    ...(events.data?.tickets ?? []).map((t: Ticket) => ({
      key: `e${t.ticketCode}`, when: t.eventStart, icon: 'celebration', kind: 'Event', title: t.eventTitle, sub: `${t.partySize} ${t.partySize === 1 ? 'person' : 'people'} · ${t.ticketCode}`,
      status: t.status, to: '/events/my-events', amount: t.fee, upcoming: new Date(t.eventStart).getTime() > now && t.status !== 'withdrawn',
    })),
  ];
  const upcoming = entries.filter((e) => e.upcoming).sort((a, b) => a.when.localeCompare(b.when));
  const history = entries.filter((e) => !e.upcoming).sort((a, b) => b.when.localeCompare(a.when));
  const loading = golf.isLoading;
  return (
    <div className="mj-page">
      <Head title="My Activity" help="Your bookings, rounds and stays." actions={<Link className="oc-btn oc-btn-primary" to="/book">Book Next Visit</Link>} />
      {loading && <Skeleton rows={6} />}
      {!loading && (
        <>
          <section className="mj-card">
            <h2 className="mj-section-title">Upcoming</h2>
            {upcoming.length === 0 ? <Empty title="Nothing booked yet" icon="event_available" action={<Link className="oc-btn oc-btn-primary" to="/book">Plan Visit</Link>} />
              : <EntryList entries={upcoming} />}
          </section>
          <section className="mj-card">
            <h2 className="mj-section-title">History</h2>
            {history.length === 0 ? <p className="mj-muted">Your past visits appear here.</p> : <EntryList entries={history} />}
          </section>
        </>
      )}
    </div>
  );
}

function EntryList({ entries }: { entries: Entry[] }) {
  return (
    <div className="mj-list">
      {entries.map((e) => {
        const body = (
          <>
            <DateBadge iso={e.when} />
            <div className="mj-item-body">
              <span className="mj-small mj-muted"><Icon name={e.icon} size={14} /> {e.kind}</span>
              <strong>{e.title}</strong>
              <span className="mj-small mj-muted">{e.sub}</span>
            </div>
            <div className="mj-item-end">
              <StatusChip status={e.status} />
              {e.play?.[0] && <PlayTime start={e.play[0]} end={e.play[1]} pausedAt={e.play[2]} pausedSeconds={e.play[3]} className="oc-playtime mj-small" />}
              {e.amount && Number(e.amount) > 0 && <span className="mj-small mj-num">{money(e.amount)}</span>}
            </div>
          </>
        );
        return e.to ? <Link key={e.key} to={e.to} className="mj-item">{body}</Link> : <div key={e.key} className="mj-item">{body}</div>;
      })}
    </div>
  );
}

export function GolfHistoryPage() {
  const stats = useGet<Schemas['RoundStats']>('/api/v1/member/golf/stats', { retry: false });
  const golf = useGet<Page<Golf>>('/api/v1/member/bookings');
  const s = stats.data;
  const played = (golf.data?.items ?? []).filter((b) => b.status === 'completed').sort((a, b) => b.startAt.localeCompare(a.startAt));
  const cards = [...(s?.history ?? [])].sort((a, b) => b.playedOn.localeCompare(a.playedOn));
  return (
    <div className="mj-page">
      <Head title="Golf History" help="Rounds, scores and handicap." actions={<Link className="oc-btn oc-btn-outline" to="/golf/scores">Scores & Handicap</Link>} />
      <div className="mj-stats">
        {[['Handicap', s?.officialHandicap ?? s?.handicapIndex ?? '—'], ['Rounds', s?.rounds ?? '—'], ['Best gross', s?.bestGross || '—']].map(([k, v]) => (
          <div key={String(k)} className="mj-card" style={{ padding: 14 }}><span className="mj-small mj-muted">{k}</span><br /><strong className="mj-num">{String(v)}</strong></div>
        ))}
      </div>
      <section className="mj-card">
        <h2 className="mj-section-title">Rounds</h2>
        {(stats.isLoading || golf.isLoading) && <Skeleton rows={4} />}
        {cards.length === 0 && played.length === 0 && !stats.isLoading && <Empty title="No rounds yet" icon="sports_golf" />}
        <div className="mj-list">
          {cards.map((c) => (
            <Link key={c.id} to={`/golf/scores/${c.id}`} className="mj-item">
              <DateBadge iso={c.playedOn} />
              <div className="mj-item-body"><strong>{c.holes} Holes{c.playingRouteName ? ` · ${c.playingRouteName}` : ''}</strong><span className="mj-small mj-muted">Par {c.par}</span></div>
              <div className="mj-item-end">
                {c.gross ? <span><span className="mj-small mj-muted">Score </span><strong style={{ fontSize: 20 }} className="mj-num">{c.gross}</strong></span> : <Chip>No score</Chip>}
                <StatusChip status={c.status} />
              </div>
            </Link>
          ))}
          {played.filter((b) => !cards.some((c) => c.playedOn === b.playDate)).map((b) => (
            <Link key={b.id} to={`/bookings/${b.id}`} className="mj-item">
              <DateBadge iso={b.playDate} />
              <div className="mj-item-body"><strong>{b.courseName} · {b.localTime}</strong><span className="mj-small mj-muted">{b.playerCount} players · {b.code}</span></div>
              <div className="mj-item-end">
                {b.teeOffAt && <PlayTime start={b.teeOffAt} end={b.roundFinishAt} pausedAt={b.pausedAt as string | null | undefined} pausedSeconds={Number(b.pausedSeconds ?? 0)} className="oc-playtime mj-small" />}
                <Chip>Rate caddy · details</Chip>
              </div>
            </Link>
          ))}
        </div>
      </section>
    </div>
  );
}

export function StayHistoryPage() {
  const stays = useGet<Page<Stay>>('/api/v1/member/stays');
  const list = [...(stays.data?.items ?? [])].sort((a, b) => b.start.localeCompare(a.start));
  const nights = (s: Stay) => Math.max(1, Math.round((new Date(s.end).getTime() - new Date(s.start).getTime()) / 86400_000));
  return (
    <div className="mj-page">
      <Head title="Stay History" help="Bungalows, suites and meeting rooms." actions={<Link className="oc-btn oc-btn-primary" to="/book/bungalow">Book Bungalow</Link>} />
      <section className="mj-card">
        {stays.isLoading && <Skeleton rows={4} />}
        {!stays.isLoading && list.length === 0 && <Empty title="No stays yet" icon="cottage" />}
        <div className="mj-list">
          {list.map((s) => (
            <Link key={s.id} to={s.kind === 'bungalow' ? `/activity/stays/${s.id}` : '/activity/stays'} className="mj-item" style={{ textDecoration: 'none', color: 'inherit' }}>
              <DateBadge iso={s.start} />
              <div className="mj-item-body">
                <span className="mj-small mj-muted">{STAY_KIND[s.kind] ?? s.kind}</span>
                <strong>{s.unitName}</strong>
                <span className="mj-small mj-muted">
                  {s.kind === 'bungalow' ? `${nights(s)} night${nights(s) === 1 ? '' : 's'} · ${s.adults} adult${s.adults === 1 ? '' : 's'}`
                    : `${new Date(s.start).toLocaleTimeString('en-GB', { timeStyle: 'short' })} – ${new Date(s.end).toLocaleTimeString('en-GB', { timeStyle: 'short' })}${s.pax ? ` · ${s.pax} people` : ''}`}
                  {' · '}{s.stayNo}
                </span>
              </div>
              <StatusChip status={s.bookingStatus ?? s.status} />
            </Link>
          ))}
        </div>
      </section>
    </div>
  );
}
