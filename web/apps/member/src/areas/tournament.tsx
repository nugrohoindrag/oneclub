import React, { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router';
import { useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDate, formatDateTime, formatNumber } from '@oneclub/i18n';
import { Card, Checkbox, DataTable, Empty, ErrorAlert, PageHeader, SelectField, Skeleton, StatusPill, TextField } from '@oneclub/shell';
import { CheckoutModal } from '../p2';

// Member App — Tournaments (PRD P3 EP-16/19, FR-APP-P3-04; Member Portal
// Golf → Tournaments: Register, My Tournaments, Leaderboard): register and
// pay the fee (online or member charge), withdraw until the policy cut-off,
// the published start sheet, the live leaderboard, my scorecard and the
// final results.

type Row = Record<string, unknown> & { id?: string };
const API = '/api/v1/member/golf';
const money = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : `Rp ${formatNumber(Number(v))}`);
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const list = <T,>(v: unknown) => (Array.isArray(v) ? (v as T[]) : []);
const toCheckout = (c: unknown): Schemas['Payment'] | null => {
  const x = c as Row | null;
  if (!x) return null;
  return { id: String(x.paymentId), amount: String(x.amount), status: String(x.status), qrString: x.qrString ?? null, vaNumber: x.vaNumber ?? null,
    checkoutUrl: x.checkoutUrl ?? null } as unknown as Schemas['Payment'];
};

function dates(t: Row) {
  return t.startDate === t.endDate ? formatDate(String(t.startDate)) : `${formatDate(String(t.startDate))} – ${formatDate(String(t.endDate))}`;
}

/** Register: upcoming, live and recent tournaments. */
export function TournamentsPage() {
  const nav = useNavigate();
  const ls = useGet<Page<Row>>(`${API}/tournaments`);
  return (
    <div className="oc-stack">
      <PageHeader title="Tournaments" help="Register, pay the tournament fee and follow the live leaderboard." />
      <ErrorAlert error={ls.error} />
      {ls.isLoading && <Skeleton />}
      {ls.data?.items.length === 0 && <Empty title="No tournaments scheduled" icon="emoji_events" />}
      <div className="oc-grid">
        {ls.data?.items.map((t) => {
          const my = t.myRegistration as Row | null;
          return (
            <button key={String(t.id)} className="oc-card" style={{ textAlign: 'left', cursor: 'pointer' }} onClick={() => nav(`/golf/tournaments/${String(t.id)}`)}>
              <div className="oc-small oc-muted">{dates(t)} · {String(t.courseName)}</div>
              <h3 style={{ margin: '4px 0' }}>{String(t.name)}</h3>
              <div className="oc-row-wrap">
                <span>{label(t.format)} · {label(t.startType)}</span>
                <StatusPill status={String(t.status)} />
                {t.registrationOpen ? <span className="oc-small">{String(t.placesLeft)} places left</span> : null}
                {my && my.status !== 'withdrawn' ? <StatusPill status={String(my.status)} label={`My: ${label(my.status)}`} /> : null}
              </div>
            </button>
          );
        })}
      </div>
    </div>
  );
}

/** One tournament: information, registration & payment, start sheet, leaderboard, results. */
export function TournamentPage() {
  const { id = '' } = useParams();
  const t = useGet<Row>(`${API}/tournaments/${id}`);
  const [tab, setTab] = useState('info');
  if (t.error) return <ErrorAlert error={t.error} />;
  if (!t.data) return <Skeleton />;
  const d = t.data;
  const my = d.myRegistration as Row | null;
  const playing = d.status === 'in_progress' || d.status === 'completed';
  const tabs: [string, string][] = [['info', 'Register'], ['start', 'Start Sheet']];
  if (playing) tabs.push(['leaderboard', 'Leaderboard']);
  if (playing && my && my.status !== 'withdrawn') tabs.push(['card', 'My Scorecard']);
  if (d.status === 'completed') tabs.push(['results', 'Results']);
  return (
    <div className="oc-stack">
      <PageHeader title={String(d.name)} help={`${dates(d)} · ${String(d.courseName)} · ${label(d.format)} ${label(d.scoringBasis)}`} />
      <div className="oc-row-wrap" role="tablist">
        {tabs.map(([k, l]) => <button key={k} role="tab" className="oc-chip" aria-selected={tab === k} aria-pressed={tab === k} onClick={() => setTab(k)}>{l}</button>)}
      </div>
      {tab === 'info' && <Register t={d} my={my} onChange={() => void t.refetch()} />}
      {tab === 'start' && <StartSheet id={id} />}
      {tab === 'leaderboard' && <Leaderboard id={id} />}
      {tab === 'card' && <MyCard id={id} />}
      {tab === 'results' && <Results id={id} />}
    </div>
  );
}

function Register({ t, my, onChange }: { t: Row; my: Row | null; onChange: () => void }) {
  const [pkg, setPkg] = useState('');
  const [hcp, setHcp] = useState('');
  const [shirt, setShirt] = useState('');
  const [consent, setConsent] = useState(false);
  const [payment, setPayment] = useState('online');
  const [checkout, setCheckout] = useState<Schemas['Payment'] | null>(null);
  const reg = useSend<Row, Row>('POST', `${API}/tournaments/${String(t.id)}/registrations`, [`${API}/`]);
  const wd = useSend<Row, Row>('POST', `${API}/my-tournaments/${String(my?.registrationId ?? '')}:withdraw`, [`${API}/`]);
  const packages = list<Row>(t.packages);
  const active = my && my.status !== 'withdrawn';
  return (
    <div className="oc-stack">
      <Card title="Tournament" icon="emoji_events">
        <dl className="oc-kv" style={{ display: 'grid', gridTemplateColumns: 'max-content 1fr', gap: '6px 16px', margin: 0 }}>
          <dt className="oc-muted">Rounds</dt><dd style={{ margin: 0 }}>{list<Row>(t.rounds).map((r) => `${formatDate(String(r.playDate))} ${String(r.startTime)}`).join(', ')}</dd>
          <dt className="oc-muted">Start</dt><dd style={{ margin: 0 }}>{label(t.startType)}</dd>
          <dt className="oc-muted">Field</dt><dd style={{ margin: 0 }}>{String(t.fieldSize)} players · {String(t.placesLeft)} places left{t.waitlistEnabled ? ' · waitlist' : ''}</dd>
          <dt className="oc-muted">Maximum handicap</dt><dd style={{ margin: 0 }}>{String(t.maxHandicap)}</dd>
          <dt className="oc-muted">Registration</dt><dd style={{ margin: 0 }}>{t.registrationOpen ? `open until ${formatDateTime(String(t.registrationClosesAt))}` : 'closed'}</dd>
        </dl>
        {t.description ? <p>{String(t.description)}</p> : null}
        {list<Row>(t.sponsors).length > 0 && <p className="oc-small oc-muted">Sponsored by {list<Row>(t.sponsors).map((s) => String(s.name)).join(', ')}</p>}
      </Card>
      {active ? (
        <Card title={`My registration ${String(my.number)}`} icon="how_to_reg" actions={<StatusPill status={String(my.status)} />}>
          <dl className="oc-kv" style={{ display: 'grid', gridTemplateColumns: 'max-content 1fr', gap: '6px 16px', margin: 0 }}>
            {my.waitlistPosition ? <><dt className="oc-muted">Waitlist</dt><dd style={{ margin: 0 }}>#{String(my.waitlistPosition)}</dd></> : null}
            <dt className="oc-muted">Package</dt><dd style={{ margin: 0 }}>{String(my.packageName ?? '—')}</dd>
            <dt className="oc-muted">Fee</dt><dd style={{ margin: 0 }}>{money(my.feeTotal)} · {label(my.paymentStatus)}</dd>
            {my.start ? <><dt className="oc-muted">Start</dt><dd style={{ margin: 0 }}>Flight {String((my.start as Row).flightNo)} · {String((my.start as Row).startLabel)} · {String((my.start as Row).localTime)}</dd></> : null}
          </dl>
          <div className="oc-row-wrap" style={{ marginTop: 12 }}>
            {my.checkout ? <button className="oc-btn oc-btn-primary" onClick={() => setCheckout(toCheckout(my.checkout))}>Pay {money(my.balance)}</button> : null}
            {my.canWithdraw ? <button className="oc-btn oc-btn-danger" disabled={wd.isPending} onClick={() => wd.mutate({ reason: 'withdrawn in the Member App' }, { onSuccess: onChange })}>
              Withdraw (refund {String(my.refundPercent)}%)</button> : null}
          </div>
          <ErrorAlert error={wd.error} />
        </Card>
      ) : t.registrationOpen ? (
        <Card title="Register" icon="edit_note">
          <div className="oc-form">
            <SelectField label="Package" value={pkg} onChange={setPkg} placeholder="Default" options={packages.filter((p) => p.id).map((p) => ({ value: String(p.id),
              label: `${String(p.name)} · member ${money(p.memberTotal)}` }))} />
            <TextField label="Handicap index" value={hcp} onChange={setHcp} help="Only needed when the club has no handicap for you" />
            <TextField label="Shirt size" value={shirt} onChange={setShirt} />
            <SelectField label="Payment" value={payment} onChange={setPayment} options={[{ value: 'online', label: 'Pay online (QRIS)' }, { value: 'member_charge', label: 'Charge to my member account' }]} />
            <Checkbox label="Show my name on the public leaderboard and the Hall of Fame" checked={consent} onChange={setConsent} />
          </div>
          <button className="oc-btn oc-btn-primary" disabled={reg.isPending} onClick={() => reg.mutate({ packageId: pkg || undefined, handicapIndex: hcp, shirtSize: shirt,
            publicConsent: consent, payment, paymentMethod: payment === 'online' ? 'qris' : undefined }, {
            onSuccess: (r) => { onChange(); if (r.checkout) setCheckout(toCheckout(r.checkout)); },
          })}>Register</button>
          <ErrorAlert error={reg.error} />
        </Card>
      ) : null}
      <CheckoutModal checkout={checkout} onClose={() => { setCheckout(null); onChange(); }} />
    </div>
  );
}

function StartSheet({ id }: { id: string }) {
  const s = useGet<Row>(`${API}/tournaments/${id}/start-sheet`, { retry: false });
  if (s.error) return <Empty title="The start sheet is not published yet" icon="schedule" />;
  if (!s.data) return <Skeleton />;
  return (
    <div className="oc-grid">
      {list<Row>(s.data.flights).map((f) => (
        <Card key={String(f.id)} title={`Flight ${String(f.flightNo)} · ${String(f.startLabel)} · ${String(f.localTime)}`} icon="flag">
          {list<Row>(f.players).map((p) => <div key={String(p.registrationId)}>{String(p.playerName)} <span className="oc-muted">({String(p.playingHandicap ?? p.handicapIndex ?? '—')})</span></div>)}
        </Card>
      ))}
    </div>
  );
}

function Board({ lb }: { lb: Row }) {
  const boards = list<Row>(lb.boards);
  const [i, setI] = useState(0);
  const b = boards[Math.min(i, boards.length - 1)];
  if (!b) return <Empty title="No scores yet" icon="leaderboard" />;
  const stb = b.category === 'stableford';
  return (
    <Card title={`${label(b.category)} · ${String(b.division)}`} icon="leaderboard">
      <SelectField label="Board" value={String(i)} onChange={(v) => setI(Number(v))} options={boards.map((x, k) => ({ value: String(k), label: `${label(x.category)} · ${String(x.division)}` }))} />
      <DataTable rows={list<Row>(b.entries).map((e, k) => ({ ...e, id: String(k) }))} columns={[{ key: 'positionLabel', header: 'Pos' }, { key: 'playerName', header: 'Player' },
        { key: 'thru', header: 'Thru' }, stb ? { key: 'points', header: 'Points' } : { key: 'toPar', header: 'To par' }]} />
    </Card>
  );
}

function Leaderboard({ id }: { id: string }) {
  const lb = useGet<Row>(`${API}/tournaments/${id}/leaderboard`, { refetchInterval: 30_000 });
  if (lb.error) return <ErrorAlert error={lb.error} />;
  if (!lb.data) return <Skeleton />;
  return <Board lb={lb.data} />;
}

/** My scorecard: enter my strokes (when my flight keeps its own card). */
function MyCard({ id }: { id: string }) {
  const cards = useGet<Page<Row>>(`${API}/tournaments/${id}/my-scorecards`);
  const [strokes, setStrokes] = useState<Record<number, string>>({});
  const send = useSend<Row, Row>('POST', `${API}/tournaments/${id}/scores`, [`${API}/tournaments/${id}`]);
  const card = cards.data?.items.at(-1);
  if (!card) return cards.isLoading ? <Skeleton /> : <Empty title="No scorecard yet" icon="scoreboard" />;
  const done = card.status === 'finalized' || card.status === 'submitted';
  const entries = Object.entries(strokes).filter(([, v]) => v !== '').map(([s, v]) => ({ seq: Number(s), strokes: Number(v), clientAt: new Date().toISOString() }));
  return (
    <Card title={`Round ${String(card.roundNo)} · ${label(card.status)}`} icon="scoreboard">
      <p>Gross {String(card.gross ?? '—')} · Net {String(card.net ?? '—')} · {String(card.points)} points · playing handicap {String(card.playingHandicap ?? '—')}</p>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(76px, 1fr))', gap: 8 }}>
        {list<Row>(card.holes).map((h) => (
          <TextField key={String(h.seq)} label={`${String(h.holeNumber)} · par ${String(h.par)}`} type="number" value={strokes[Number(h.seq)] ?? (h.strokes != null ? String(h.strokes) : '')}
            onChange={(v) => setStrokes({ ...strokes, [Number(h.seq)]: v })} />
        ))}
      </div>
      {!done && <button className="oc-btn oc-btn-primary" disabled={entries.length === 0 || send.isPending} style={{ marginTop: 12 }}
        onClick={() => send.mutate({ round: card.roundNo, entries }, { onSuccess: () => { setStrokes({}); void cards.refetch(); } })}>Save</button>}
      <ErrorAlert error={send.error} />
    </Card>
  );
}

function Results({ id }: { id: string }) {
  const r = useGet<Row>(`${API}/tournaments/${id}/results`);
  if (r.error) return <ErrorAlert error={r.error} />;
  if (!r.data) return <Skeleton />;
  return (
    <div className="oc-stack">
      <Card title="Champions" icon="emoji_events">
        {list<Row>(r.data.champions).map((c, k) => <div key={k}><strong>{String(c.playerName)}</strong> — {String(c.division)} {label(c.category)} ({String(c.score ?? '—')})</div>)}
      </Card>
      <Card title="Prizes" icon="military_tech">
        {list<Row>(r.data.awards).map((a) => <div key={String(a.id)}>{String(a.name)}: <strong>{String(a.recipientName ?? '—')}</strong>{a.resultText ? ` (${String(a.resultText)})` : ''}</div>)}
      </Card>
    </div>
  );
}

/** My Tournaments. */
export function MyTournamentsPage() {
  const ls = useGet<Page<Row>>(`${API}/my-tournaments`);
  return (
    <div className="oc-stack">
      <PageHeader title="My Tournaments" />
      <DataTable rows={ls.data?.items.map((x) => ({ ...x, id: String(x.registrationId) }) as Row)} loading={ls.isLoading} error={ls.error}
        empty={<Empty title="No tournaments yet" icon="emoji_events" action={<Link className="oc-btn oc-btn-primary" to="/golf/tournaments">Register</Link>} />}
        columns={[{ key: 'tournamentName', header: 'Tournament', render: (r) => <Link to={`/golf/tournaments/${String(r.tournamentId)}`}>{String(r.tournamentName)}</Link> },
          { key: 'startDate', header: 'Date', render: (r) => formatDate(String(r.startDate)) },
          { key: 'start', header: 'Start', render: (r) => (r.start ? `Flight ${String((r.start as Row).flightNo)} · ${String((r.start as Row).startLabel)} · ${String((r.start as Row).localTime)}` : '—') },
          { key: 'paymentStatus', header: 'Payment', render: (r) => <StatusPill status={String(r.paymentStatus)} /> },
          { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]} />
    </div>
  );
}

/** Leaderboard: the tournament in play (or the latest finished). */
export function LiveLeaderboardPage() {
  const ls = useGet<Page<Row>>(`${API}/tournaments`);
  const live = ls.data?.items.find((t) => t.status === 'in_progress') ?? ls.data?.items.find((t) => t.status === 'completed');
  return (
    <div className="oc-stack">
      <PageHeader title={live ? `Leaderboard · ${String(live.name)}` : 'Leaderboard'} />
      {ls.isLoading && <Skeleton />}
      {ls.isSuccess && !live && <Empty title="No tournament in play" icon="leaderboard" />}
      {live && <Leaderboard id={String(live.id)} />}
    </div>
  );
}

/** Member App routes of the area. */
export const TOURNAMENT_MEMBER_ROUTES: { path: string; element: React.ReactNode }[] = [
  { path: 'golf/tournaments', element: <TournamentsPage /> },
  { path: 'golf/tournaments/leaderboard', element: <LiveLeaderboardPage /> },
  { path: 'golf/tournaments/:id', element: <TournamentPage /> },
  { path: 'golf/my-tournaments', element: <MyTournamentsPage /> },
];
