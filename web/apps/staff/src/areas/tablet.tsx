import { useEffect, useState } from 'react';
import { Link, Outlet, useParams, useRoutes } from 'react-router';
import { request, uuidv7, useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDate, formatDateTime, formatNumber } from '@oneclub/i18n';
import { cacheGet, cachePut, enqueue, useOnline } from '@oneclub/offline';
import {
  Brand, Card, DataTable, Empty, ErrorAlert, HeaderActions, Icon, NotFoundPage, NotificationsPage, ProfilePage, SelectField, StatusPill, useAuth, useToast,
} from '@oneclub/shell';
import { ConnectivityChip, SyncPage, read, write } from '../offline';

/*
 * Caddy Tablet area (`/tablet`, PRD P2 EP-06): My Assignments → Current Round
 * (players, scorecard, hole progress, course map, on-course order) → Earnings.
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

function Layout() {
  const online = useOnline();
  return (
    <div className="oc-topnav-frame" style={{ maxWidth: 900 }}>
      <header className="oc-topbar">
        <Brand />
        <span className="oc-spacer" />
        <ConnectivityChip />
        <HeaderActions property={false} />
      </header>
      {!online && <div className="oc-alert oc-alert-warning" role="status" style={{ marginBottom: 12 }}>No signal — round actions are saved on the tablet and synced later.</div>}
      <Outlet />
      <nav className="oc-bottom-nav" data-always="true" aria-label="Main">
        <Link to="/tablet"><Icon name="assignment" size={26} />Assignments</Link>
        <Link to="/tablet/earnings"><Icon name="payments" size={26} />Earnings</Link>
        <Link to="/tablet/sync"><Icon name="sync" size={26} />Sync</Link>
        <Link to="/tablet/profile"><Icon name="person" size={26} />Profile</Link>
      </nav>
    </div>
  );
}

/** My Assignments: current and next (FR-CTB-02). */
function AssignmentsPage() {
  const toast = useToast();
  const { propertyId } = useAuth();
  const my = useGet<Schemas['MyAssignments']>('/api/v1/golf/my-assignments', { refetchInterval: 30_000 });
  const accept = async (id: string, flightId: string) => {
    await enqueue('golf.round', { op: 'accept', flightId, assignmentId: id }, propertyId);
    toast('Assignment accepted');
    void my.refetch();
  };
  const a = my.data;
  return (
    <div className="oc-stack">
      <div className="oc-page-head"><div><h1>{a ? `#${a.code} ${a.name}` : 'My Assignments'}</h1>{a && <p><StatusPill status={a.dutyStatus} /></p>}</div></div>
      <ErrorAlert error={my.error} />
      {a?.current && (
        <Link to={`/tablet/round/${a.current.flightId}`} className="oc-card oc-card-ink" style={{ textDecoration: 'none' }}>
          <div className="oc-small" style={{ opacity: 0.7 }}>Current Round</div>
          <h2 style={{ margin: '4px 0' }}>{a.current.bookingCode ?? 'Walk-in flight'}</h2>
          <div>{a.current.teeTime} · {a.current.playerNames.join(', ')}</div>
        </Link>
      )}
      <Card title="Next Assignment" icon="schedule">
        {a?.next.length === 0 && <div className="oc-muted">{a.queuePosition ? `Position ${a.queuePosition} in today's rotation` : 'No assignment yet'}</div>}
        <div className="oc-stack">
          {a?.next.map((n) => (
            <div key={n.id} className="oc-row-wrap">
              <strong>{formatDate(n.playDate)} {n.teeTime}</strong><span>{n.bookingCode ?? 'Walk-in flight'}</span><StatusPill status={n.status} /><span className="oc-spacer" />
              {n.status === 'assigned' && <button className="oc-btn oc-btn-ink" onClick={() => void accept(n.id, n.flightId)}>Accept Assignment</button>}
              <Link className="oc-btn oc-btn-outline" to={`/tablet/round/${n.flightId}`}>Open</Link>
            </div>
          ))}
        </div>
      </Card>
    </div>
  );
}

/** Current Round: players, scorecard, hole progress (FR-CTB-03..09). */
function RoundPage() {
  const { id = '' } = useParams();
  const toast = useToast();
  const { propertyId } = useAuth();
  const online = useOnline();
  const live = useGet<Round>(`/api/v1/golf/rounds/${id}`, { retry: false });
  const [round, setRound] = useState<Round | null>(null);
  const [seq, setSeq] = useState(0);
  const [status, setStatus] = useState('');
  const [scores, setScores] = useState<Record<string, Record<number, number>>>({});
  const [tab, setTab] = useState<'score' | 'players' | 'map' | 'order'>('score');
  useEffect(() => {
    if (live.data) {
      setRound(live.data);
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
  if (!round) return live.isLoading ? <div className="oc-card">Loading round…</div> : <Empty title="Round not available offline" icon="cloud_off" />;
  const device = tabletId();
  const act = async (payload: Row, msg: string) => {
    await enqueue('golf.round', { flightId: id, deviceId: device, at: new Date().toISOString(), ...payload }, propertyId);
    toast(online ? msg : `${msg} (queued)`);
  };
  const hole = round.holes[Math.max(seq, 1) - 1];
  const setScore = (sc: string, s: number, v: number) => setScores({ ...scores, [sc]: { ...(scores[sc] ?? {}), [s]: v } });
  return (
    <div className="oc-stack">
      <div className="oc-page-head"><div><h1>{round.round.bookingCode ?? `Flight ${round.round.flightNo}`}</h1>
        <p>{round.round.playingRouteName ?? ''} · {formatDateTime(round.round.teeTime)} · <StatusPill status={status} /></p></div></div>
      <div className="oc-row-wrap">
        {(status === 'checked_in' || status === 'ready') && <button className="oc-btn oc-btn-ink" onClick={() => { void act({ op: 'tee_off' }, 'Round started'); setStatus('in_play'); setSeq(1); }}>Start Round</button>}
        {status === 'in_play' && seq < round.holes.length && (
          <button className="oc-btn oc-btn-ink" onClick={() => { void act({ op: 'hole', seq: seq + 1 }, `Hole ${seq + 1}`); setSeq(seq + 1); }}>Next hole → {seq + 1}</button>
        )}
        {status === 'in_play' && <button className="oc-btn oc-btn-outline" onClick={() => { void act({ op: 'finish' }, 'Round completed'); setStatus('completed'); }}>Complete Round</button>}
        <button className="oc-btn oc-btn-neutral" onClick={async () => {
          try {
            const r = await request<Round>('POST', `/api/v1/golf/rounds/${id}:handover`, { deviceId: device });
            setRound(r);
            setSeq(r.round.currentSeq);
            toast('This tablet now drives the round');
          } catch (e) {
            toast(String((e as Error).message));
          }
        }}>Take over on this tablet</button>
      </div>
      {hole && (
        <div className="oc-card oc-card-ink">
          <div className="oc-row"><h2 style={{ margin: 0 }}>Hole {hole.sectionCode}-{hole.number}</h2><span className="oc-spacer" />
            <span>Par {hole.par}{hole.strokeIndex ? ` · SI ${hole.strokeIndex}` : ''}</span></div>
        </div>
      )}
      <div className="oc-row-wrap">
        {(['score', 'players', 'map', 'order'] as const).map((x) => <button key={x} className="oc-chip" aria-pressed={tab === x} onClick={() => setTab(x)}>{x === 'score' ? 'Scorecard' : x === 'players' ? 'Players' : x === 'map' ? 'Course Map' : 'On-Course Order'}</button>)}
      </div>
      {tab === 'score' && (
        <Card title="Score entry" icon="scoreboard">
          <div className="oc-stack">
            {round.scorecards.map((sc) => (
              <div key={sc.id} className="oc-row-wrap">
                <strong style={{ minWidth: 140 }}>{sc.playerName}</strong>
                {hole && [hole.par - 1, hole.par, hole.par + 1, hole.par + 2].filter((n) => n > 0).map((n) => (
                  <button key={n} className={`oc-btn oc-btn-sm ${scores[sc.id]?.[seq] === n ? 'oc-btn-ink' : 'oc-btn-outline'}`} style={{ minWidth: 44, minHeight: 44 }}
                    onClick={() => { setScore(sc.id, seq, n); void act({ op: 'score', scorecardId: sc.id, entries: [{ seq, strokes: n, clientAt: new Date().toISOString() }] }, `Score ${n}`); }}>{n}</button>
                ))}
                <input className="oc-input" style={{ width: 60, minHeight: 44 }} inputMode="numeric" aria-label={`Other score for ${sc.playerName}`} placeholder="…"
                  onBlur={(e) => { const n = Number(e.target.value); if (n > 0) { setScore(sc.id, seq, n); void act({ op: 'score', scorecardId: sc.id, entries: [{ seq, strokes: n, clientAt: new Date().toISOString() }] }, `Score ${n}`); e.target.value = ''; } }} />
                <span className="oc-small oc-muted">{sc.scores.filter((h) => h.strokes || scores[sc.id]?.[h.seq]).reduce((s, h) => s + (scores[sc.id]?.[h.seq] ?? h.strokes ?? 0), 0)} total</span>
              </div>
            ))}
          </div>
        </Card>
      )}
      {tab === 'players' && <PlayersTab round={round} />}
      {tab === 'map' && hole && <MapTab hole={hole} />}
      {tab === 'order' && <OrderTab round={round} seq={seq} />}
    </div>
  );
}

/** Customer context without unnecessary personal data (FR-CTB-04). */
function PlayersTab({ round }: { round: Round }) {
  const toast = useToast();
  const [pref, setPref] = useState<Record<string, string>>({});
  const save = useSend<Row>('POST', (b) => `/api/v1/golf/customers/${b.customerId}/preferences`, []);
  return (
    <div className="oc-stack">
      <ErrorAlert error={save.error} />
      {round.players.map((p) => {
        const player = round.round.players.find((x) => x.id === p.playerId);
        return (
          <Card key={p.playerId} title={p.name} icon="person">
            <div className="oc-small">{p.playerType.replace(/_/g, ' ')}{p.teeSet ? ` · ${p.teeSet} tee` : ''}{p.handicap ? ` · HCP ${p.handicap}` : ''}</div>
            {p.roundsWithMe > 0 && <div className="oc-small">{p.roundsWithMe} rounds with you{p.lastRoundWithMe ? `, last ${formatDate(p.lastRoundWithMe)}` : ''}{p.favoriteCaddyIsMe ? ' · you are the favourite caddy' : ''}</div>}
            {p.highlights.map((h) => <div key={h} className="oc-small">• {h}</div>)}
            {player?.customerId && (
              <div className="oc-row" style={{ marginTop: 8 }}>
                <input className="oc-input" placeholder="Record preference (e.g. Es teh tawar)" value={pref[p.playerId] ?? ''} onChange={(e) => setPref({ ...pref, [p.playerId]: e.target.value })} />
                <button className="oc-btn oc-btn-outline" disabled={!pref[p.playerId]} onClick={() => save.mutate({ customerId: player.customerId, category: 'beverage', value: pref[p.playerId] },
                  { onSuccess: () => { toast('Preference recorded'); setPref({ ...pref, [p.playerId]: '' }); } })}>Save</button>
              </div>
            )}
          </Card>
        );
      })}
    </div>
  );
}

/** Course map & GPS distance (FR-CTB-11, FR-PLX-01/02). */
function MapTab({ hole }: { hole: Round['holes'][number] }) {
  const [pos, setPos] = useState<GeolocationPosition | null>(null);
  const map = useGet<Schemas['CourseMap']>(`/api/v1/golf/course-maps/${hole.holeId}${pos ? `?lat=${pos.coords.latitude}&lng=${pos.coords.longitude}` : ''}`);
  const image = map.data?.assets.find((a) => a.fileUrl)?.fileUrl;
  useEffect(() => {
    if (!('geolocation' in navigator)) return;
    const w = navigator.geolocation.watchPosition(setPos, () => undefined, { enableHighAccuracy: true });
    return () => navigator.geolocation.clearWatch(w);
  }, []);
  return (
    <Card title="Course Map" icon="map">
      {image ? <img src={image} alt={`Hole ${hole.number} map`} style={{ width: '100%', borderRadius: 12 }} /> : <div className="oc-muted">No map for this hole.</div>}
      <div className="oc-row-wrap" style={{ marginTop: 8 }}>
        {map.data?.distances.map((d) => <span key={d.target + (d.name ?? '')} className="oc-chip">{d.name || d.target}: {d.meters} m</span>)}
        {!pos && <span className="oc-small oc-muted">Waiting for GPS…</span>}
      </div>
    </Card>
  );
}

/** On-course order charged to the player's folio (FR-CTB-07). */
function OrderTab({ round, seq }: { round: Round; seq: number }) {
  const toast = useToast();
  const { propertyId } = useAuth();
  const outlets = useGet<Page<Row>>('/api/v1/commercial/outlets?filter[status]=active');
  const [outlet, setOutlet] = useState('');
  const [player, setPlayer] = useState(round.round.players[0]?.id ?? '');
  const menu = useGet<Page<Schemas['MenuItem']>>(outlet ? `/api/v1/commercial/outlets/${outlet}/menu?channel=caddy_tablet` : null);
  const [cart, setCart] = useState<Record<string, number>>({});
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
  void propertyId;
  return (
    <Card title="On-Course Order" icon="local_cafe">
      <div className="oc-row-wrap">
        <div style={{ width: 220 }}><SelectField label="Outlet" value={outlet} onChange={setOutlet} placeholder="Halfway House…"
          options={(outlets.data?.items ?? []).map((o) => ({ value: String(o.id), label: String(o.name) }))} /></div>
        <div style={{ width: 220 }}><SelectField label="Player" value={player} onChange={setPlayer}
          options={round.round.players.map((p) => ({ value: p.id, label: p.name }))} /></div>
      </div>
      <div className="oc-stack" style={{ marginTop: 8 }}>
        {menu.data?.items.map((m) => (
          <div key={m.productId} className="oc-row"><span>{m.name}</span><span className="oc-small oc-muted">{money(m.price)}</span><span className="oc-spacer" />
            <button className="oc-btn oc-btn-outline oc-btn-sm" style={{ minWidth: 44, minHeight: 44 }} onClick={() => setCart({ ...cart, [m.productId]: (cart[m.productId] ?? 0) + 1 })}>
              + {cart[m.productId] ? `(${cart[m.productId]})` : ''}</button></div>
        ))}
      </div>
      <button className="oc-btn oc-btn-ink" style={{ marginTop: 12 }} disabled={!outlet || !Object.values(cart).some((n) => n > 0)} onClick={() => void place()}>Send order</button>
    </Card>
  );
}

/** Earnings: caddy fee, tips, settlements, attendance, history (FR-CTB-10). */
function EarningsPage() {
  const e = useGet<Schemas['Earnings']>('/api/v1/golf/my-earnings');
  if (!e.data) return <ErrorAlert error={e.error} />;
  return (
    <div className="oc-stack">
      <div className="oc-page-head"><div><h1>Earnings</h1><p>{formatDate(e.data.from)} – {formatDate(e.data.to)}</p></div></div>
      <div className="oc-grid">
        <div className="oc-card oc-card-ink"><div className="oc-small" style={{ opacity: 0.7 }}>Caddy Fee</div><div className="oc-metric">{money(e.data.caddyFee)}</div></div>
        <div className="oc-card"><div className="oc-small oc-muted">Tip</div><div className="oc-metric">{money(e.data.tips)}</div></div>
      </div>
      <Card title="Settlement" icon="receipt_long">
        <DataTable rows={e.data.settlements as unknown as Row[]} columns={[{ key: 'number', header: 'Settlement' }, { key: 'periodEnd', header: 'Period end', render: (r) => formatDate(String(r.periodEnd)) },
          { key: 'total', header: 'Total', render: (r) => money(r.total) }, { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]} />
      </Card>
      <Card title="Attendance" icon="how_to_reg">
        <DataTable rows={e.data.attendance as unknown as Row[]} columns={[{ key: 'workDate', header: 'Date', render: (r) => formatDate(String(r.workDate)) },
          { key: 'clockedInAt', header: 'In', render: (r) => (r.clockedInAt ? formatDateTime(String(r.clockedInAt)) : '—') }, { key: 'roundsToday', header: 'Rounds' }]} />
      </Card>
      <Card title="Assignment History" icon="history">
        <DataTable rows={e.data.assignments as unknown as Row[]} columns={[{ key: 'playDate', header: 'Date', render: (r) => `${formatDate(String(r.playDate))} ${String(r.teeTime)}` },
          { key: 'bookingCode', header: 'Booking' }, { key: 'feeAmount', header: 'Fee', render: (r) => money(r.feeAmount) }, { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]} />
      </Card>
    </div>
  );
}

const routes = [
  {
    element: <Layout />,
    children: [
      { index: true, element: <AssignmentsPage /> },
      { path: 'round/:id', element: <RoundPage /> },
      { path: 'earnings', element: <EarningsPage /> },
      { path: 'sync', element: <SyncPage /> },
      { path: 'notifications', element: <NotificationsPage /> },
      { path: 'profile', element: <ProfilePage showPin /> },
      { path: '*', element: <NotFoundPage /> },
    ],
  },
];

export default function TabletArea() {
  return useRoutes(routes);
}
