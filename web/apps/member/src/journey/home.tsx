import React from 'react';
import { Link } from 'react-router';
import { useGet, type Page, type Schemas } from '@oneclub/api-client';
import { formatDate } from '@oneclub/i18n';
import { Icon, PlayTime, Skeleton, useAuth, useBootstrap, useNavigation } from '@oneclub/shell';
import { RateCaddy } from './booking';
import { PromoCarousel } from './promo';
import { Chip, dayLabel, money, StatusChip } from './ui';

// Member Home: the starting point of the journey. It answers three
// questions — what have I booked, what can I do now, what is my membership
// status — and leads to the activity, not a report full of numbers.

type MyMembership = Schemas['MyMembership'];

export function useMembership() {
  return useGet<MyMembership>('/api/v1/member/membership', { retry: false });
}

/** The member's principal (or first) membership. */
export function currentMembership(m?: MyMembership) {
  const list = m?.profile.memberships ?? [];
  return list.find((x) => x.status === 'active' && x.role === 'principal') ?? list.find((x) => x.status === 'active') ?? list[0];
}

function greeting() {
  const h = new Date().getHours();
  return h < 11 ? 'Good Morning' : h < 15 ? 'Good Afternoon' : h < 19 ? 'Good Evening' : 'Good Night';
}

const QUICK: [string, string, string][] = [
  ['book-tee-time', 'sports_golf', 'Book Tee Time'], ['book-driving-range', 'golf_course', 'Driving Range'],
  ['book-bungalow', 'cottage', 'Book Bungalow'], ['upcoming-events', 'celebration', 'Join Event'],
];

export function MemberHome() {
  const { me } = useAuth();
  const boot = useBootstrap();
  const nav = useNavigation('member');
  const m = useMembership();
  const loyalty = useGet<Schemas['MyLoyalty']>('/api/v1/member/loyalty', { retry: false });
  const ms = currentMembership(m.data);
  const book = nav.data?.items.find((i) => i.key === 'book')?.children ?? [];
  const first = (me?.fullName ?? '').split(' ')[0];
  const acc = loyalty.data?.account;
  return (
    <div className="mj-page">
      <section className="mj-hero" aria-label="Membership">
        <div className="mj-muted">{boot.branding.appName}</div>
        <h1>{greeting()}, {first}</h1>
        <div className="mj-hero-row">
          {ms ? <span className="mj-tier"><Icon name="workspace_premium" size={16} /> Member · {ms.typeName}</span>
            : !m.isLoading && <span className="mj-tier"><Icon name="person" size={16} /> Non-member</span>}
          {(ms?.memberNo ?? m.data?.profile.memberNo) && <span className="mj-hero-tag">Member ID: {ms?.memberNo ?? m.data?.profile.memberNo}</span>}
          <span className="oc-spacer" />
          {ms ? <Link className="oc-btn oc-btn-sm" style={{ background: '#fff', color: 'var(--md-sys-color-primary-strong)' }} to="/membership/card"><Icon name="qr_code_2" size={18} /> Member Card</Link>
            : !m.isLoading && <Link className="oc-btn oc-btn-sm" style={{ background: '#fff', color: 'var(--md-sys-color-primary-strong)' }} to="/join"><Icon name="upgrade" size={18} /> Upgrade to Member</Link>}
        </div>
        <div className="mj-hero-stats">
          <div><span>Membership</span><strong>{ms?.status === 'active' ? 'Active' : ms?.status ?? '—'}</strong></div>
          <div><span>Valid Until</span><strong>{ms?.endsOn ? formatDate(ms.endsOn) : '—'}</strong></div>
          <div><span>{acc?.tierName ? `${acc.tierName} · Points` : 'Points'}</span><strong className="mj-num">{acc ? acc.balance.toLocaleString('en-GB') : '—'}</strong></div>
        </div>
      </section>

      {ms && ms.daysToExpiry !== undefined && ms.daysToExpiry !== null && ms.daysToExpiry <= 60 && ms.status === 'active' && (
        <Link to="/membership" className="mj-card oc-row" style={{ textDecoration: 'none', color: 'inherit', borderColor: 'var(--mj-warn)' }}>
          <span className="mj-icon" style={{ background: 'var(--mj-warn-soft)', color: 'var(--mj-warn)' }}><Icon name="autorenew" size={20} /></span>
          <span style={{ flex: 1 }}><strong>Membership expiring soon</strong><br /><span className="mj-small mj-muted">{ms.daysToExpiry <= 0 ? 'Expired' : `${ms.daysToExpiry} days left`} — renew to keep your benefits.</span></span>
          <span className="oc-btn oc-btn-primary oc-btn-sm">{ms.renewalPending ? 'Renewal pending' : 'Renew'}</span>
        </Link>
      )}

      <PromoCarousel />
      <RateLastRound />
      <Upcoming />

      <section>
        <h2 className="mj-section-title">Quick Actions <Link to="/book">All bookings</Link></h2>
        <div className="mj-quick">
          {QUICK.map(([key, icon, label]) => {
            const it = book.find((b) => b.key === key);
            return it ? <Link key={key} to={it.path}><span className="mj-icon"><Icon name={icon} size={22} /></span>{label}</Link> : null;
          })}
        </div>
      </section>

      <RecentActivity />
    </div>
  );
}

/** The next visit: golf first, then stays and event tickets. */
function Upcoming() {
  const golf = useGet<Page<Schemas['BookingSummary']>>('/api/v1/member/bookings');
  const stays = useGet<Page<Schemas['Stay']>>('/api/v1/member/stays', { retry: false });
  const now = Date.now() - 6 * 3600_000;
  const nextGolf = (golf.data?.items ?? []).filter((b) => ['pending', 'confirmed', 'checked_in'].includes(b.status) && new Date(b.startAt).getTime() >= now)
    .sort((a, b) => a.startAt.localeCompare(b.startAt))[0];
  const nextStay = (stays.data?.items ?? []).filter((s) => ['requested', 'reserved', 'checked_in'].includes(s.status) && new Date(s.end).getTime() >= Date.now())
    .sort((a, b) => a.start.localeCompare(b.start))[0];
  if (golf.isLoading) return <div className="mj-card"><Skeleton rows={3} /></div>;
  if (!nextGolf && !nextStay) {
    return (
      <div className="mj-card oc-row-wrap">
        <span className="mj-icon"><Icon name="event_available" size={20} /></span>
        <span style={{ flex: 1 }}><strong>No upcoming visit</strong><br /><span className="mj-small mj-muted">Plan your next round or stay.</span></span>
        <Link className="oc-btn oc-btn-primary" to="/book">Plan Visit</Link>
      </div>
    );
  }
  return (
    <section>
      <h2 className="mj-section-title">Upcoming <Link to="/activity">My Activity</Link></h2>
      <div className="mj-grid-2">
        {nextGolf && <UpcomingGolf b={nextGolf} />}
        {nextStay && (
          <Link to={nextStay.kind === 'bungalow' ? `/activity/stays/${nextStay.id}` : '/activity/stays'} className="mj-card mj-upcoming" style={{ textDecoration: 'none', color: 'inherit' }}>
            <DateBadge iso={nextStay.start} />
            <div style={{ flex: 1 }}>
              <div className="mj-small mj-muted">{nextStay.kind === 'meeting_room' ? 'Meeting Room' : nextStay.kind === 'vip_suite' ? 'VIP Suite' : 'Bungalow'}</div>
              <strong>{nextStay.unitName}</strong>
              <div className="mj-small mj-muted">{new Date(nextStay.start).toLocaleString('en-GB', { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })}</div>
            </div>
            <StatusChip status={nextStay.status} />
          </Link>
        )}
      </div>
    </section>
  );
}

/** End of the session: ask the member to rate the caddy of the last round (1–5). */
function RateLastRound() {
  const golf = useGet<Page<Schemas['BookingSummary']>>('/api/v1/member/bookings');
  const last = (golf.data?.items ?? []).filter((b) => (b.status === 'completed' || b.roundFinishAt) && Date.now() - new Date(b.startAt).getTime() < 3 * 86400_000)
    .sort((a, b) => b.startAt.localeCompare(a.startAt))[0];
  const bk = useGet<Schemas['Booking']>(last ? `/api/v1/member/bookings/${last.id}` : null);
  const j = useGet<Schemas['BookingJourney']>(last ? `/api/v1/member/bookings/${last.id}/journey` : null, { retry: false });
  if (!last || !bk.data || !j.data) return null;
  const mine = bk.data.players.find((p) => p.customerId && p.customerId === bk.data!.customerId) ?? bk.data.players[0];
  const me = j.data.players.find((p) => p.playerId === mine?.id);
  if (!me?.caddy || me.caddy.rated) return null;
  return (
    <div className="mj-card oc-stack" style={{ gap: 10, marginBottom: 16 }}>
      <strong><Icon name="hiking" size={18} /> How was your caddy today?</strong>
      <span className="mj-small mj-muted">{me.caddy.name} · caddy no. {me.caddy.code} · {last.courseName} {last.localTime}</span>
      <RateCaddy assignmentId={me.caddy.assignmentId} name={me.caddy.name} />
    </div>
  );
}

function UpcomingGolf({ b }: { b: Schemas['BookingSummary'] }) {
  const j = useGet<Schemas['BookingJourney']>(`/api/v1/member/bookings/${b.id}/journey`, { retry: false });
  const want = (j.data?.players ?? []).filter((p) => p.caddyPreference !== 'none');
  const assigned = want.filter((p) => p.caddy);
  return (
    <div className="mj-card oc-stack" style={{ gap: 12 }}>
      <div className="mj-upcoming">
        <DateBadge iso={b.playDate} />
        <div style={{ flex: 1 }}>
          <div className="mj-small mj-muted">Tee Time · {b.courseName}</div>
          <strong style={{ fontSize: 20 }} className="mj-num">{dayLabel(b.playDate, { weekday: 'long' })} · {b.localTime}</strong>
          <div className="mj-small mj-muted">{b.playerCount} {b.playerCount === 1 ? 'Player' : 'Players'} · {b.code}</div>
        </div>
      </div>
      <div className="oc-row-wrap">
        <Chip tone={b.status === 'pending' ? 'warn' : 'ok'}>Tee Time {b.status === 'pending' ? 'awaiting payment' : b.status === 'checked_in' ? '✓ Checked in' : '✓ Confirmed'}</Chip>
        {b.teeOffAt && <Chip tone="info"><PlayTime start={b.teeOffAt} end={b.roundFinishAt} pausedAt={b.pausedAt as string | null | undefined} pausedSeconds={Number(b.pausedSeconds ?? 0)} /></Chip>}
        {want.length > 0 && (assigned.length === want.length
          ? <Chip tone="ok">Caddy ✓ Assigned{assigned.length === 1 ? ` · ${assigned[0]!.caddy!.name}` : ''}</Chip>
          : <Chip tone="warn">Caddy pending assignment</Chip>)}
      </div>
      <Link className="oc-btn oc-btn-primary" to={`/bookings/${b.id}`}>View Booking</Link>
    </div>
  );
}

export function DateBadge({ iso }: { iso: string }) {
  const d = new Date(iso.length === 10 ? `${iso}T00:00:00` : iso);
  return <div className="mj-date-badge" aria-hidden><span>{d.toLocaleDateString('en-GB', { month: 'short' })}</span><b>{d.getDate()}</b><span>{d.toLocaleDateString('en-GB', { weekday: 'short' })}</span></div>;
}

const LINES: Record<string, [string, string]> = {
  golf: ['sports_golf', 'Golf Round'], stay: ['cottage', 'Stay'], sportclub: ['sports_tennis', 'Sport Club'], banquet: ['celebration', 'Event'],
  pos: ['restaurant', 'Food & Beverage'], commercial: ['card_travel', 'Package'],
};

function RecentActivity() {
  const r = useGet<Schemas['Bookings']>('/api/v1/member/reservations', { retry: false });
  const t = useGet<Schemas['MyTransactions']>('/api/v1/member/transactions', { retry: false });
  const recent = (r.data?.recent ?? []).slice(0, 4);
  const fb = (t.data?.folios ?? []).filter((f) => f.sourceType === 'pos_order').slice(0, 2);
  if (!recent.length && !fb.length) return null;
  return (
    <section className="mj-card">
      <h2 className="mj-section-title">Recent Activity <Link to="/activity">See all</Link></h2>
      <div className="mj-list">
        {recent.map((x) => {
          const [icon, label] = LINES[x.businessLine] ?? ['event', x.businessLine];
          return (
            <div key={x.id} className="mj-item">
              <span className="mj-icon"><Icon name={icon} size={20} /></span>
              <div className="mj-item-body"><strong>{label}</strong><span className="mj-small mj-muted">{x.resource} · {formatDate(x.start)}</span></div>
              <StatusChip status={x.status} />
            </div>
          );
        })}
        {fb.map((f) => (
          <Link key={f.id} to="/transactions" className="mj-item">
            <span className="mj-icon"><Icon name="restaurant" size={20} /></span>
            <div className="mj-item-body"><strong>F&B Transaction</strong><span className="mj-small mj-muted">{f.number} · {formatDate(f.createdAt)}</span></div>
            <span className="mj-num">{money(f.summary.charges)}</span>
          </Link>
        ))}
      </div>
    </section>
  );
}
