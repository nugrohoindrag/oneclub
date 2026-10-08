import { useEffect, useState } from 'react';
import { Link, NavLink, Outlet, useNavigate, useParams, useRoutes } from 'react-router';
import { request, uuidv7, useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDate, formatDateTime, formatNumber } from '@oneclub/i18n';
import { cacheGet, cachePut, enqueue, useOnline, useQueue } from '@oneclub/offline';
import { DataTable, ErrorAlert, Icon, NotFoundPage, NotificationsPage, PlayTime, ProfilePage, Skeleton, StatusPill, logoOf, useAuth, useBootstrap, useToast } from '@oneclub/shell';
import { SyncPage, read, write } from '../offline';
import { TabletTournamentCard, TabletTournamentPage } from '../p3/tournament';
import { PAYOUTS_TABLET_ROUTES } from '../p5/payouts';
import { ProductImage, TodayLabel } from '../pos/shared';
import '../pos/pos.css';

/*
 * Caddy Tablet area (`/tablet`, PRD P2 EP-06): My Assignments → Current Round
 * (players, scorecard, hole progress, course map, on-course order) → Earnings,
 * in the look of the POS Cashier (blue rail, POS tokens).
 * Every round action goes through the offline sync queue (UUIDv7 ids):
 * a hole without signal is recorded and synced later without duplicates.
 */

const TABLET_KEY = 'oneclub.caddy.tabletId';
type Round = Schemas['RoundInfo'];
type Row = Record<string, unknown>;

function tabletId() {
  let id = read(TABLET_KEY);
  if (!id) {
    id = `TAB-${uuidv7().slice(-8)}`;
    write(TABLET_KEY, id);
  }
  return id;
}

const money = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : `Rp ${formatNumber(Number(v))}`);
const label = (s: string) => s.replace(/_/g, ' ');

/** Duty / round state as a POS chip. */
function StateChip({ status }: { status: string }) {
  const tone = ['in_play', 'preparing'].includes(status) ? 'preparing' : ['completed', 'finished', 'available'].includes(status) ? 'ready' : 'sent';
  return <span className="pos-kitchen" data-kitchen={tone} style={{ textTransform: 'capitalize' }}>{label(status)}</span>;
}

/** Blue rail: assignments, earnings, payouts, sync, profile and logout. */
function Layout() {
  const b = useBootstrap();
  const online = useOnline();
  const { can, logout } = useAuth();
  return (
    <div className="pos">
      <nav className="pos-rail" aria-label="Caddy Tablet">
        <span className="pos-rail-logo"><img src={logoOf(b.branding)} alt={b.branding.appName} /></span>
        <NavLink to="/tablet" end aria-label="My Assignments" title="My Assignments"><Icon name="assignment" size={26} /></NavLink>
        <NavLink to="/tablet/earnings" aria-label="Earnings" title="Earnings"><Icon name="payments" size={26} /></NavLink>
        {can('hris.payout.own') && <NavLink to="/tablet/payouts" aria-label="Payouts" title="Payouts"><Icon name="account_balance_wallet" size={26} /></NavLink>}
        <NavLink to="/tablet/sync" aria-label="Sync" title="Sync"><Icon name={online ? 'sync' : 'cloud_off'} size={26} /></NavLink>
        <NavLink to="/tablet/profile" aria-label="Profile" title="Profile"><Icon name="person" size={26} /></NavLink>
        <span className="pos-spacer" />
        <button onClick={() => void logout()} aria-label="Log out" title="Log out"><Icon name="logout" size={26} /></button>
      </nav>
      <main className="pos-main">
        {!online && <div className="pos-banner" data-tone="warn" style={{ marginTop: 16 }} role="status"><Icon name="cloud_off" size={20} />
          No signal — round actions are saved on the tablet and synced later.</div>}
        <Outlet />
      </main>
    </div>
  );
}

/** Pages shared with other areas, inside the tablet frame. */
function Body({ children }: { children: React.ReactNode }) {
  return <div className="pos-body" style={{ paddingTop: 24 }}>{children}</div>;
}

/** My Assignments: current and next (FR-CTB-02). */
function AssignmentsPage() {
  const toast = useToast();
  const { propertyId } = useAuth();
  const my = useGet<Schemas['MyAssignments']>('/api/v1/golf/my-assignments', { refetchInterval: 30_000 });
  // accepted on this tablet: shown at once, also while the action waits in the offline queue
  const [accepted, setAccepted] = useState<string[]>([]);
  const accept = async (id: string, flightId: string) => {
    await enqueue('golf.round', { op: 'accept', flightId, assignmentId: id }, propertyId);
    setAccepted((a) => [...a, id]);
    toast('Assignment accepted');
    void my.refetch();
  };
  const a = my.data;
  return (
    <>
      <div className="pos-head">
        <h1>My Assignments</h1>
        {a && <><span className="pos-tablebar-chip" style={{ height: 32 }}>#{a.code} {a.name}</span><StateChip status={a.dutyStatus} /></>}
        <span className="pos-spacer" />
        <TodayLabel />
      </div>
      <div className="pos-body">
        <ErrorAlert error={my.error} />
        {my.isLoading && <Skeleton rows={4} />}
        {a?.current && (
          <Link to={`/tablet/round/${a.current.flightId}`} className="pos-hole" style={{ textDecoration: 'none' }}>
            <span className="pos-muted-inverse">Current Round</span>
            <strong className="pos-hole-title">{a.current.bookingCode ?? 'Walk-in flight'}</strong>
            <span>{a.current.teeTime} · {a.current.playerNames.join(', ')}</span>
            {a.current.startedAt && <PlayTime start={a.current.startedAt} end={a.current.finishedAt} />}
            <span className="pos-btn" data-variant="outline" style={{ alignSelf: 'flex-start', marginTop: 8 }}>Open Round <Icon name="chevron_right" size={20} /></span>
          </Link>
        )}
        <div style={{ margin: '18px 0' }}><TabletTournamentCard /></div>
        <h2 style={{ fontSize: 18, margin: '8px 0 14px' }}>Next Assignment</h2>
        {a?.next.length === 0 && (
          <div className="pos-empty"><Icon name="sports_golf" size={36} />{a.queuePosition ? `Position ${a.queuePosition} in today's rotation` : 'No assignment yet'}</div>
        )}
        <div className="pos-cards">
          {a?.next.map((n) => (
            <div key={n.id} className="pos-card">
              <div className="pos-card-top">
                <span className="pos-kds-badge" data-kds={n.status === 'assigned' ? 'received' : 'ready'}>{n.teeTime.slice(0, 5)}</span>
                <div style={{ flex: 1, minWidth: 0 }}><strong>{n.bookingCode ?? 'Walk-in flight'}</strong><div className="pos-muted">{formatDate(n.playDate)} · tee {n.teeTime}</div></div>
                <StateChip status={n.status} />
              </div>
              <div className="pos-card-actions">
                <Link className="pos-btn" data-variant="soft" data-size="sm" to={`/tablet/round/${n.flightId}`}>Open</Link>
                {n.status === 'assigned' && (n.acceptedAt || accepted.includes(n.id)
                  ? <span className="pos-kitchen" data-kitchen="ready"><Icon name="check_circle" size={16} />Accepted</span>
                  : <button className="pos-btn" data-size="sm" onClick={() => void accept(n.id, n.flightId)}>Accept</button>)}
              </div>
            </div>
          ))}
        </div>
      </div>
    </>
  );
}

/** Current Round: players, scorecard, hole progress (FR-CTB-03..09). */
function RoundPage() {
  const { id = '' } = useParams();
  const nav = useNavigate();
  const toast = useToast();
  const { propertyId } = useAuth();
  const online = useOnline();
  const live = useGet<Round>(`/api/v1/golf/rounds/${id}`, { retry: false });
  const [round, setRound] = useState<Round | null>(null);
  const [seq, setSeq] = useState(0);
  const [status, setStatus] = useState('');
  // play time counts from the tee-off at once on this tablet, also offline
  const [localStart, setLocalStart] = useState<string>();
  const [localEnd, setLocalEnd] = useState<string>();
  // rain pause on this tablet (undefined: the server's state)
  const [localPause, setLocalPause] = useState<string | null>();
  const [scores, setScores] = useState<Record<string, Record<number, number>>>({});
  const [tab, setTab] = useState<'cart' | 'score' | 'players' | 'map' | 'order'>('cart');
  // the last round action: once the server answers, the round is read again (scorecards open on tee-off)
  const [lastId, setLastId] = useState('');
  const last = useQueue().find((q) => q.id === lastId);
  const answered = !!last && ['accepted', 'duplicate', 'rejected', 'conflict'].includes(last.status);
  useEffect(() => {
    if (answered) void live.refetch();
  }, [answered, lastId]); // eslint-disable-line react-hooks/exhaustive-deps
  const refused = last && (last.status === 'rejected' || last.status === 'conflict') ? last : null;
  useEffect(() => {
    if (live.data) {
      setRound(live.data);
      setLocalPause(undefined);
      setSeq(live.data.round.currentSeq);
      setStatus(live.data.round.status);
      void cachePut(propertyId, `round:${id}`, live.data);
    } else if (live.isError) {
      void cacheGet<Round>(propertyId, `round:${id}`).then((r) => {
        if (r) {
          setRound(r);
          setSeq(r.round.currentSeq);
          setStatus(r.round.status);
        }
      });
    }
  }, [live.data, live.isError, id, propertyId]);
  if (!round) {
    return live.isLoading ? <Body><Skeleton rows={6} /></Body>
      : <div className="pos-empty" style={{ margin: 'auto' }}><Icon name="cloud_off" size={40} />Round not available offline</div>;
  }
  const device = tabletId();
  const act = async (payload: Row, msg: string) => {
    const item = await enqueue('golf.round', { flightId: id, deviceId: device, at: new Date().toISOString(), ...payload }, propertyId);
    setLastId(item.id);
    toast(online ? msg : `${msg} (queued)`);
  };
  const hole = round.holes[Math.max(seq, 1) - 1];
  const pausedAt = localPause !== undefined ? localPause : round.round.pausedAt;
  const teeOff = round.round.teeOffAt ?? localStart;
  const finish = round.round.roundFinishAt ?? localEnd;
  const carts = (round.round.golfCarts ?? []).filter((c) => c.outAt && !c.returnedAt);
  const playerOf = (scorecardId: string) => round.players.find((p) => p.scorecardId === scorecardId);
  const setScore = (sc: string, s: number, v: number) => setScores({ ...scores, [sc]: { ...(scores[sc] ?? {}), [s]: v } });
  const score = (sc: string, n: number) => { setScore(sc, seq, n); void act({ op: 'score', scorecardId: sc, entries: [{ seq, strokes: n, clientAt: new Date().toISOString() }] }, `Score ${n}`); };
  const tabs: [typeof tab, string, string][] = [['cart', 'Cart View', 'golf_course'], ['score', 'Scorecard', 'scoreboard'], ['players', 'Players', 'group'],
    ['map', 'Course Map', 'map'], ['order', 'On-Course Order', 'local_cafe']];
  return (
    <>
      <div className="pos-head">
        <button className="pos-icon-btn" style={{ border: 0 }} onClick={() => nav('/tablet')} aria-label="Back to My Assignments"><Icon name="arrow_back" size={22} /></button>
        <div>
          <h1>{round.round.bookingCode ?? `Flight ${round.round.flightNo}`}</h1>
          <span className="pos-muted">{round.round.playingRouteName ?? ''} · {formatDateTime(round.round.teeTime)}</span>
        </div>
        <PlayTime start={teeOff} end={finish} pausedAt={pausedAt} pausedSeconds={round.round.pausedSeconds} />
        <StateChip status={status} />
        <span className="pos-spacer" />
        {(status === 'checked_in' || status === 'ready') && (
          <button className="pos-btn" onClick={() => { void act({ op: 'tee_off' }, 'Round started'); setStatus('in_play'); setSeq(1); setLocalStart(new Date().toISOString()); }}><Icon name="sports_golf" size={20} />Start Round</button>
        )}
        {status === 'in_play' && seq < round.holes.length && (
          <button className="pos-btn" onClick={() => { void act({ op: 'hole', seq: seq + 1 }, `Hole ${seq + 1}`); setSeq(seq + 1); }}>Next hole <Icon name="chevron_right" size={20} /></button>
        )}
        {status === 'in_play' && (pausedAt
          ? <button className="pos-btn" onClick={() => { void act({ op: 'resume' }, 'Play resumed'); setLocalPause(null); }}><Icon name="play_arrow" size={20} />Resume play</button>
          : <button className="pos-btn" data-variant="outline" onClick={() => { void act({ op: 'pause', reason: 'rain' }, 'Rain reported: play time paused'); setLocalPause(new Date().toISOString()); }}>
            <Icon name="rainy" size={20} />Rain</button>)}
        {status === 'in_play' && <button className="pos-btn" data-variant="outline" onClick={() => { void act({ op: 'finish' }, 'Round completed'); setStatus('completed'); setLocalEnd(new Date().toISOString()); }}>Complete Round</button>}
        <button className="pos-btn" data-variant="soft" onClick={async () => {
          try {
            const r = await request<Round>('POST', `/api/v1/golf/rounds/${id}:handover`, { deviceId: device });
            setRound(r);
            setSeq(r.round.currentSeq);
            toast('This tablet now drives the round');
          } catch (e) {
            toast(String((e as Error).message));
          }
        }}>Take over</button>
      </div>
      <div className="pos-body">
        {refused && <div className="pos-banner" data-tone="warn" style={{ margin: '0 0 14px' }} role="alert"><Icon name="error" size={20} />
          The club system did not accept the last action{refused.error ? `: ${refused.error}` : ''}. The flight must be checked in, with caddies and a golf cart, in the starter queue.</div>}
        {status === 'in_play' && pausedAt && (
          <div className="pos-banner" data-tone="warn" style={{ margin: '0 0 14px' }} role="status"><Icon name="rainy" size={20} />
            Paused for rain since {new Date(pausedAt).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })} — the play time has stopped.
            {Math.max(seq - 1, 0) * 2 < round.holes.length ? ` Less than half of the round played (${Math.max(seq - 1, 0)} of ${round.holes.length}): the players can reschedule at the front desk.` : ''}</div>
        )}
        {status === 'in_play' && <MarshalMessages flightId={id} initial={round.interventions} />}
        {hole && (
          <div className="pos-hole">
            <div style={{ display: 'flex', alignItems: 'baseline', gap: 16, flexWrap: 'wrap' }}>
              <strong className="pos-hole-title">Hole {hole.sectionCode}-{hole.number}</strong>
              <span>Par {hole.par}{hole.strokeIndex ? ` · SI ${hole.strokeIndex}` : ''}</span>
              <span className="pos-spacer" />
              <span className="pos-muted-inverse">{Math.max(seq, 0)} / {round.holes.length}</span>
            </div>
            {teeOff && (
              <div className="pos-timers">
                <span className="pos-timer"><Icon name="timer" size={22} /><PlayTime start={teeOff} end={finish} pausedAt={pausedAt} pausedSeconds={round.round.pausedSeconds} label={false} className="pos-timer-text" /></span>
                {carts.map((c) => (
                  <span key={c.id} className="pos-timer" title="Golf cart time this session"><Icon name="electric_car" size={20} />Cart {c.golfCartCode}
                    <PlayTime start={c.outAt} end={finish} pausedAt={pausedAt} label={false} className="pos-timer-text" /></span>
                ))}
              </div>
            )}
            <div className="pos-progress" aria-label="Hole progress">
              {round.holes.map((h, i) => <i key={h.holeId + i} data-on={i < seq || undefined} data-current={i === seq - 1 || undefined} />)}
            </div>
          </div>
        )}
        <div className="pos-cats" role="group" aria-label="Round" style={{ padding: '18px 0' }}>
          {tabs.map(([x, l, icon]) => <button key={x} className="pos-chip" aria-pressed={tab === x} onClick={() => setTab(x)}><Icon name={icon} size={20} />{l}</button>)}
        </div>
        {tab === 'score' && (
          <div className="pos-stack">
            {round.scorecards.length === 0 && <div className="pos-empty"><Icon name="scoreboard" size={36} />The scorecards open when the round starts (tee-off).</div>}
            {round.scorecards.map((sc) => {
              const total = sc.scores.filter((h) => h.strokes || scores[sc.id]?.[h.seq]).reduce((s, h) => s + (scores[sc.id]?.[h.seq] ?? h.strokes ?? 0), 0);
              const pc = playerOf(sc.id);
              const got = seq > 0 ? pc?.holeStrokes?.[seq - 1] ?? 0 : 0;
              const current = scores[sc.id]?.[seq] ?? sc.scores.find((h) => h.seq === seq)?.strokes ?? undefined;
              const quick = hole ? [hole.par - 1, hole.par, hole.par + 1, hole.par + 2].filter((n) => n > 0) : [];
              return (
                <div key={sc.id} className="pos-score-row">
                  <div style={{ minWidth: 160 }}><strong>{sc.playerName}</strong><div className="pos-muted">Total {total}</div>
                    {pc?.courseHandicap != null && (
                      <div className="pos-muted" title="Course handicap from this player's tee; strokes received on this hole by stroke index">
                        CH {pc.courseHandicap}{got !== 0 ? ` · ${got > 0 ? '+' : ''}${got} stroke${Math.abs(got) > 1 ? 's' : ''} here` : ''}</div>
                    )}</div>
                  <div className="pos-guests" role="group" aria-label={`Score for ${sc.playerName}`}>
                    {quick.map((n) => (
                      <button key={n} type="button" className="pos-guest pos-score" aria-pressed={current === n} onClick={() => score(sc.id, n)}>{n}</button>
                    ))}
                    {seq > 0 && <ManualScore name={sc.playerName} current={current} quick={quick} onSave={(n) => score(sc.id, n)} />}
                  </div>
                </div>
              );
            })}
          </div>
        )}
        {tab === 'cart' && <CartView round={round} seq={seq} scores={scores} onScore={score} />}
        {tab === 'players' && <PlayersTab round={round} />}
        {tab === 'map' && hole && <MapTab hole={hole} />}
        {tab === 'order' && <OrderTab round={round} seq={seq} />}
      </div>
    </>
  );
}

/** Marshal messages for the flight (FR-PLX-04): shown until the caddy
 * confirms the flight got them; checked every 30 s, apart from the round
 * state so a queued hole is never overwritten. */
function MarshalMessages({ flightId, initial }: { flightId: string; initial: Schemas['PaceIntervention'][] }) {
  const poll = useGet<Round>(`/api/v1/golf/rounds/${flightId}?part=marshal`, { refetchInterval: 30_000 });
  const ack = useSend<Row>('POST', (b) => `/api/v1/golf/pace-interventions/${b.id}:acknowledge`, [`/api/v1/golf/rounds/${flightId}?part=marshal`]);
  const [done, setDone] = useState<string[]>([]);
  const open = (poll.data?.interventions ?? initial).filter((i) => !done.includes(i.id));
  if (open.length === 0) return null;
  return (
    <div className="pos-stack" style={{ marginBottom: 14 }}>
      {open.map((i) => (
        <div key={i.id} className="pos-banner" data-tone="warn" role="alert" style={{ margin: 0 }}>
          <Icon name={i.kind === 'reminder' || i.kind === 'note' ? 'campaign' : 'flag'} size={22} />
          <div style={{ flex: 1 }}><strong style={{ textTransform: 'capitalize' }}>{label(i.kind)}</strong> · {i.message}
            <div className="pos-muted">{i.createdByName ?? 'Marshal'} · {formatDateTime(i.createdAt)}{i.hole ? ` · ${i.hole}` : ''}</div></div>
          <button className="pos-btn" data-size="sm" disabled={ack.isPending}
            onClick={() => ack.mutate({ id: i.id }, { onSuccess: () => setDone([...done, i.id]) })}>Got it</button>
        </div>
      ))}
    </div>
  );
}

/** Customer context without unnecessary personal data (FR-CTB-04). */
function PlayersTab({ round }: { round: Round }) {
  const toast = useToast();
  const [pref, setPref] = useState<Record<string, string>>({});
  const save = useSend<Row>('POST', (b) => `/api/v1/golf/customers/${b.customerId}/preferences`, []);
  return (
    <div className="pos-cards">
      <ErrorAlert error={save.error} />
      {round.players.map((p) => {
        const player = round.round.players.find((x) => x.id === p.playerId);
        return (
          <div key={p.playerId} className="pos-card">
            <div className="pos-card-top">
              <span className="pos-tablebar-icon"><Icon name="person" size={22} /></span>
              <div style={{ flex: 1, minWidth: 0 }}><strong>{p.name}</strong>
                <div className="pos-muted" style={{ textTransform: 'capitalize' }}>{label(p.playerType)}{p.teeSet ? ` · ${p.teeSet} tee` : ''}{p.handicap ? ` · HCP ${p.handicap}` : ''}
                  {p.courseHandicap != null ? ` · course handicap ${p.courseHandicap}` : ''}</div></div>
            </div>
            {p.roundsWithMe > 0 && <div className="pos-muted">{p.roundsWithMe} rounds with you{p.lastRoundWithMe ? `, last ${formatDate(p.lastRoundWithMe)}` : ''}
              {p.favoriteCaddyIsMe ? ' · you are the favourite caddy' : ''}</div>}
            {p.highlights.map((h) => <div key={h}>• {h}</div>)}
            {player?.customerId && (
              <div style={{ display: 'flex', gap: 10, marginTop: 8 }}>
                <input className="pos-input" placeholder="Preference (e.g. Es teh tawar)" value={pref[p.playerId] ?? ''} onChange={(e) => setPref({ ...pref, [p.playerId]: e.target.value })} />
                <button className="pos-btn" data-variant="outline" disabled={!pref[p.playerId]} onClick={() => save.mutate({ customerId: player.customerId, category: 'beverage', value: pref[p.playerId] },
                  { onSuccess: () => { toast('Preference recorded'); setPref({ ...pref, [p.playerId]: '' }); } })}>Save</button>
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}

/** Course map & GPS distance (FR-CTB-11, FR-PLX-01/02). */
function MapTab({ hole }: { hole: Round['holes'][number] }) {
  const { pos, map } = useHoleMap(hole.holeId);
  const image = map.data?.assets.find((a) => a.fileUrl)?.fileUrl;
  return (
    <div className="pos-section">
      <h2>Course Map · Hole {hole.number}</h2>
      {image ? <img src={image} alt={`Hole ${hole.number} map`} style={{ width: '100%', borderRadius: 16 }} />
        : <div className="pos-empty"><Icon name="map" size={36} />No map for this hole.</div>}
      <div className="pos-guests" style={{ marginTop: 12, flexWrap: 'wrap' }}>
        {map.data?.distances.map((d) => <span key={d.target + (d.name ?? '')} className="pos-kitchen" style={{ fontSize: 14, padding: '6px 12px' }}>{d.name || d.target}: {d.meters} m</span>)}
        {!pos && <span className="pos-muted">Waiting for GPS…</span>}
      </div>
    </div>
  );
}

/** The hole map with the tablet's GPS position (watched while shown). */
function useHoleMap(holeId: string) {
  const [pos, setPos] = useState<GeolocationPosition | null>(null);
  const at = pos ? `?lat=${pos.coords.latitude}&lng=${pos.coords.longitude}` : '';
  const map = useGet<Schemas['CourseMap']>(`/api/v1/golf/course-maps/${holeId}${at}`);
  useEffect(() => {
    if (!('geolocation' in navigator)) return;
    const w = navigator.geolocation.watchPosition(setPos, () => undefined, { enableHighAccuracy: true });
    return () => navigator.geolocation.clearWatch(w);
  }, []);
  return { pos, map };
}

const GREEN: Record<string, string> = { green_front: 'Front', green_center: 'Center', green_back: 'Back' };

/** Cart View (golf cart GPS screen): the hole with the distances to the
 * green, the course map with the green and "you are here", and the
 * scorecard of the whole round (FR-CTB-11, FR-PLX-01/02). */
function CartView({ round, seq, scores, onScore }: {
  round: Round; seq: number; scores: Record<string, Record<number, number>>; onScore: (scorecardId: string, strokes: number) => void;
}) {
  const hole = round.holes[Math.max(seq, 1) - 1];
  const { pos, map } = useHoleMap(hole?.holeId ?? '');
  if (!hole) return null;
  const m = map.data;
  const image = m?.assets.find((a) => a.fileUrl)?.fileUrl;
  const green = (m?.distances ?? []).filter((d) => GREEN[d.target]);
  const others = (m?.distances ?? []).filter((d) => !GREEN[d.target]);
  const strokes = (sc: Round['scorecards'][number], s: number) => scores[sc.id]?.[s] ?? sc.scores.find((h) => h.seq === s)?.strokes ?? undefined;
  // nine holes per table (OUT / IN), the whole round in TOT
  const nines: Round['holes'][] = [];
  for (let i = 0; i < round.holes.length; i += 9) nines.push(round.holes.slice(i, i + 9));
  const seqOf = (h: Round['holes'][number]) => round.holes.indexOf(h) + 1;
  const sum = (sc: Round['scorecards'][number], hs: Round['holes'][number][]) => hs.reduce((t, h) => t + (strokes(sc, seqOf(h)) ?? 0), 0);
  return (
    <div className="pos-cart">
      <div className="pos-cart-hole">
        <div className="pos-cart-yards" aria-label="Distance to the green">
          {green.length > 0 ? green.map((d) => (
            <div key={d.target} data-main={d.target === 'green_center' || undefined}><span>{GREEN[d.target]}</span><strong>{formatNumber(d.meters)}</strong><small>m</small></div>
          )) : <div><span>Green</span><strong>—</strong><small>{pos ? 'no green point' : 'waiting for GPS'}</small></div>}
        </div>
        {others.length > 0 && (
          <div className="pos-guests" style={{ flexWrap: 'wrap' }}>
            {others.map((d) => <span key={d.target + (d.name ?? '')} className="pos-kitchen" style={{ fontSize: 14, padding: '6px 12px' }}>{d.name || d.target}: {d.meters} m</span>)}
          </div>
        )}
        {image ? <img className="pos-cart-card" src={image} alt={`Hole ${hole.number} layout`} />
          : <div className="pos-empty"><Icon name="map" size={36} />No layout for this hole.</div>}
        {m?.overviewUrl && (
          <div className="pos-cart-map">
            <img src={m.overviewUrl} alt="Course map" />
            {m.greenX != null && m.greenY != null && (
              <span className="pos-cart-pin" style={{ left: `${m.greenX * 100}%`, top: `${m.greenY * 100}%` }} title={`Green of hole ${hole.number}`}>
                <Icon name="flag" size={18} /></span>
            )}
            {m.hereX != null && m.hereY != null && (
              <span className="pos-cart-here" style={{ left: `${m.hereX * 100}%`, top: `${m.hereY * 100}%` }} title="You are here"><Icon name="my_location" size={18} /></span>
            )}
          </div>
        )}
      </div>
      <div className="pos-cart-side">
        {nines.map((hs, n) => (
          <table key={n} className="pos-cart-table">
            <thead>
              <tr><th>Hole</th>{hs.map((h) => <th key={h.holeId + h.sequence} data-current={seqOf(h) === seq || undefined}>{h.number}</th>)}<th>{n === 0 ? 'OUT' : 'IN'}</th>{n === nines.length - 1 && <th>TOT</th>}</tr>
            </thead>
            <tbody>
              <tr className="pos-cart-par"><th>Par</th>{hs.map((h) => <td key={h.holeId + h.sequence} data-current={seqOf(h) === seq || undefined}>{h.par}</td>)}
                <td>{hs.reduce((t, h) => t + h.par, 0)}</td>{n === nines.length - 1 && <td>{round.holes.reduce((t, h) => t + h.par, 0)}</td>}</tr>
              <tr className="pos-cart-si"><th>SI</th>{hs.map((h) => <td key={h.holeId + h.sequence} data-current={seqOf(h) === seq || undefined}>{h.strokeIndex ?? ''}</td>)}<td />{n === nines.length - 1 && <td />}</tr>
              {round.scorecards.map((sc) => (
                <tr key={sc.id}><th>{sc.playerName.split(' ')[0]}</th>
                  {hs.map((h) => {
                    const v = strokes(sc, seqOf(h));
                    return <td key={h.holeId + h.sequence} data-current={seqOf(h) === seq || undefined} data-score={v == null ? undefined : v < h.par ? 'under' : v > h.par ? 'over' : 'par'}>{v ?? ''}</td>;
                  })}
                  <td><strong>{sum(sc, hs) || ''}</strong></td>{n === nines.length - 1 && <td><strong>{sum(sc, round.holes) || ''}</strong></td>}</tr>
              ))}
            </tbody>
          </table>
        ))}
        {round.scorecards.length === 0 ? <div className="pos-empty"><Icon name="scoreboard" size={32} />The scorecards open when the round starts (tee-off).</div> : (
          <div className="pos-stack">
            <strong>Hole {hole.number} · Par {hole.par}</strong>
            {round.scorecards.map((sc) => {
              const quick = [hole.par - 1, hole.par, hole.par + 1, hole.par + 2].filter((x) => x > 0);
              return (
                <div key={sc.id} className="pos-score-row" style={{ padding: 0 }}>
                  <span style={{ minWidth: 110 }}>{sc.playerName}</span>
                  <div className="pos-guests" role="group" aria-label={`Score for ${sc.playerName}`}>
                    {quick.map((x) => (
                      <button key={x} type="button" className="pos-guest pos-score" aria-pressed={strokes(sc, seq) === x} onClick={() => onScore(sc.id, x)}>{x}</button>
                    ))}
                    <ManualScore name={sc.playerName} current={strokes(sc, seq)} quick={quick} onSave={(n) => onScore(sc.id, n)} />
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}

/** Any other number of strokes: type it, then Save (or Enter). A saved
 * score that is not one of the quick buttons stays shown in the box. */
function ManualScore({ name, current, quick, onSave }: { name: string; current?: number; quick: number[]; onSave: (n: number) => void }) {
  const [typed, setTyped] = useState('');
  useEffect(() => setTyped(''), [current]);
  const other = current != null && !quick.includes(current) ? current : undefined;
  const n = Number(typed);
  const ok = typed !== '' && n >= 1 && n <= 20 && n !== current;
  return (
    <form className="pos-guests" style={{ gap: 8 }} onSubmit={(e) => { e.preventDefault(); if (ok) onSave(n); }}>
      <input className="pos-input pos-score" inputMode="numeric" enterKeyHint="done" aria-label={`Other score for ${name}`} placeholder="…"
        data-saved={typed === '' && other != null ? true : undefined} value={typed !== '' ? typed : other != null ? String(other) : ''}
        onFocus={(e) => e.target.select()} onChange={(e) => setTyped(e.target.value.replace(/\D/g, '').slice(0, 2))} />
      <button type="submit" className="pos-btn" style={{ height: 56 }} disabled={!ok} aria-label={`Save score for ${name}`}><Icon name="check" size={20} />Save</button>
    </form>
  );
}

/** On-course order charged to the player's folio (FR-CTB-07). */
function OrderTab({ round, seq }: { round: Round; seq: number }) {
  const toast = useToast();
  const outlets = useGet<Page<Row>>('/api/v1/commercial/outlets?filter[status]=active');
  const [outlet, setOutlet] = useState('');
  const [player, setPlayer] = useState(round.round.players[0]?.id ?? '');
  const menu = useGet<Page<Schemas['MenuItem']>>(outlet ? `/api/v1/commercial/outlets/${outlet}/menu?channel=caddy_tablet` : null);
  const [cart, setCart] = useState<Record<string, number>>({});
  const items = menu.data?.items ?? [];
  const count = Object.values(cart).reduce((s, n) => s + n, 0);
  const total = items.reduce((s, m) => s + Number(m.price) * (cart[m.productId] ?? 0), 0);
  const place = async () => {
    const lines = Object.entries(cart).filter(([, n]) => n > 0).map(([productId, n]) => ({ productId, quantity: String(n) }));
    try {
      await request('POST', '/api/v1/golf/on-course-orders', { id: uuidv7(), flightId: round.round.flightId, playerId: player, outletId: outlet, lines }, { 'Idempotency-Key': uuidv7() });
      toast(`Order sent — delivered at hole ${seq + 1}`);
      setCart({});
    } catch (e) {
      toast(String((e as Error).message));
    }
  };
  return (
    <div className="pos-stack">
      <div className="pos-cats" role="group" aria-label="Outlet" style={{ padding: 0 }}>
        {(outlets.data?.items ?? []).filter((o) => o.outletType !== 'retail').map((o) => (
          <button key={String(o.id)} className="pos-chip" aria-pressed={outlet === o.id} onClick={() => { setOutlet(String(o.id)); setCart({}); }}>
            <Icon name="restaurant" size={20} />{String(o.name)}</button>
        ))}
      </div>
      <div className="pos-guests" role="group" aria-label="Player" style={{ flexWrap: 'wrap' }}>
        {round.round.players.map((p) => <button key={p.id} type="button" className="pos-guest" data-wide aria-pressed={player === p.id} onClick={() => setPlayer(p.id)}>{p.name}</button>)}
      </div>
      {!outlet ? <div className="pos-empty"><Icon name="local_cafe" size={36} />Choose an outlet (e.g. Halfway House).</div> : (
        <div className="pos-grid">
          {items.map((m) => (
            <button key={m.productId} className="pos-product" disabled={m.soldOut} onClick={() => setCart({ ...cart, [m.productId]: (cart[m.productId] ?? 0) + 1 })}>
              <ProductImage item={m} className="pos-product-img" />
              {cart[m.productId] ? <span className="pos-product-qty">{cart[m.productId]}</span> : null}
              <span className="pos-product-row"><span className="pos-product-name">{m.name}</span><span className="pos-product-price">{money(m.price)}</span></span>
            </button>
          ))}
        </div>
      )}
      <div className="pos-tablebar" style={{ margin: 0, position: 'sticky', bottom: 0 }}>
        <span className="pos-tablebar-icon"><Icon name="local_cafe" size={24} /></span>
        <div className="pos-tableinfo"><strong>{count} items · {money(total)}</strong><span className="pos-muted">Charged to the player's folio · delivered at hole {seq + 1}</span></div>
        <span className="pos-spacer" />
        {count > 0 && <button className="pos-btn pos-pill" data-variant="outline" onClick={() => setCart({})}>Clear</button>}
        <button className="pos-btn pos-pill" disabled={!outlet || count === 0} onClick={() => void place()}>Send Order</button>
      </div>
    </div>
  );
}

/** Earnings: caddy fee, tips, settlements, attendance, history (FR-CTB-10). */
function EarningsPage() {
  const e = useGet<Schemas['Earnings']>('/api/v1/golf/my-earnings');
  return (
    <>
      <div className="pos-head"><h1>Earnings</h1>{e.data && <span className="pos-muted">{formatDate(e.data.from)} – {formatDate(e.data.to)}</span>}</div>
      <div className="pos-body">
        <ErrorAlert error={e.error} />
        {!e.data ? <Skeleton rows={6} /> : <>
          <div className="pos-cards" style={{ marginBottom: 18 }}>
            <div className="pos-hole"><span className="pos-muted-inverse">Caddy Fee</span><strong className="pos-hole-title">{money(e.data.caddyFee)}</strong></div>
            <div className="pos-card"><span className="pos-muted">Tip</span><strong style={{ fontSize: 28 }}>{money(e.data.tips)}</strong></div>
          </div>
          <div className="pos-section"><h2>Settlement</h2>
            <DataTable rows={e.data.settlements as unknown as Row[]} columns={[{ key: 'number', header: 'Settlement' }, { key: 'periodEnd', header: 'Period end', render: (r) => formatDate(String(r.periodEnd)) },
              { key: 'total', header: 'Total', render: (r) => money(r.total) }, { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]} /></div>
          <div className="pos-section"><h2>Attendance</h2>
            <DataTable rows={e.data.attendance as unknown as Row[]} columns={[{ key: 'workDate', header: 'Date', render: (r) => formatDate(String(r.workDate)) },
              { key: 'clockedInAt', header: 'In', render: (r) => (r.clockedInAt ? formatDateTime(String(r.clockedInAt)) : '—') }, { key: 'roundsToday', header: 'Rounds' }]} /></div>
          <div className="pos-section"><h2>Assignment History</h2>
            <DataTable rows={e.data.assignments as unknown as Row[]} columns={[{ key: 'playDate', header: 'Date', render: (r) => `${formatDate(String(r.playDate))} ${String(r.teeTime)}` },
              { key: 'bookingCode', header: 'Booking' }, { key: 'feeAmount', header: 'Fee', render: (r) => money(r.feeAmount) },
              { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]} /></div>
        </>}
      </div>
    </>
  );
}

const routes = [
  {
    element: <Layout />,
    children: [
      { index: true, element: <AssignmentsPage /> },
      { path: 'round/:id', element: <RoundPage /> },
      { path: 'tournament/:tid/:fid', element: <Body><TabletTournamentPage /></Body> }, // PRD P3 §7.3 tournament scorecard
      { path: 'earnings', element: <EarningsPage /> },
      ...PAYOUTS_TABLET_ROUTES.map((r) => ({ ...r, element: <Body>{r.element}</Body> })), // PRD P5 EP-13 Payout History & Statement
      { path: 'sync', element: <Body><SyncPage /></Body> },
      { path: 'notifications', element: <Body><NotificationsPage /></Body> },
      { path: 'profile', element: <Body><ProfilePage showPin /></Body> },
      { path: '*', element: <Body><NotFoundPage /></Body> },
    ],
  },
];

export default function TabletArea() {
  return useRoutes(routes);
}
