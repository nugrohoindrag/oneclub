import { useEffect, useMemo, useRef, useState } from 'react';
import { useLocation } from 'react-router';
import { qs, useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDateTime } from '@oneclub/i18n';
import {
  Empty, ErrorAlert, Icon, Modal, PageHeader, PlayTime, SelectField, Skeleton, StatTile, StatusPill, TextArea, useAuth, useToast,
} from '@oneclub/shell';
import { GOLF_STREAM, useLive } from '../live';
import { LiveWeather } from './weather';
import './marshal.css';

/*
 * Course Monitor (PRD P2 FR-PLX-04/05, Product Overview §11 "Marshal &
 * status lapangan"): the course map with every flight in play coloured by
 * its pace and the golf carts from GPS, the flights most behind first, and
 * the Marshal's actions — reminder, warning, final warning, skip a hole, let
 * the flight behind play through — which show on the flight's caddy tablet.
 * One page for the Starter's desk (Back Office / Operational) and the
 * Marshal's phone or tablet on the buggy.
 */

type Monitor = Schemas['CourseMonitor'];
type Flight = Monitor['flights'][number];
type Row = Record<string, unknown>;

const KINDS: [kind: string, label: string, icon: string][] = [
  ['reminder', 'Reminder', 'campaign'], ['warning', 'Warning', 'warning'], ['final_warning', 'Final warning', 'report'],
  ['skip_hole', 'Skip hole', 'start'], ['play_through', 'Let play through', 'route'], ['note', 'Note', 'edit_note'],
];
const hhmm = (iso: string) => new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
const kindLabel = (k: string) => KINDS.find((x) => x[0] === k)?.[1] ?? k;

const BREAK: Record<string, string> = { halfway: 'Halfway House', turn: 'the turn', tee_house: 'tee house stop', break_other: 'break' };
const minutesSince = (iso: string) => Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 60_000));

/** on pace · behind (within tolerance) · slow · paused (rain: pace frozen) · on break */
const toneOf = (f: Flight) => (f.pausedAt && !f.onBreak ? 'paused' : f.onBreak && !f.breakOverMinutes ? 'break' : f.slow ? 'slow' : f.behindMinutes > 0 ? 'behind' : 'on-pace');

function behindText(f: Flight) {
  if (f.pausedAt && !f.onBreak) return `Paused · ${f.pauseReason === 'lightning' ? 'lightning' : 'rain'} ${hhmm(f.pausedAt)} (${minutesSince(f.pausedAt)} min)`;
  if (f.pausedAt && f.onBreak) {
    return `On break · ${BREAK[String(f.pauseReason)] ?? 'break'} since ${hhmm(f.pausedAt)}${f.breakOverMinutes ? ` · ${f.breakOverMinutes} min over the ${f.breakAllowanceMinutes} min allowed` : ''}`;
  }
  if (f.behindMinutes > 0) return `${f.behindMinutes} min behind`;
  if (f.behindMinutes < 0) return `${-f.behindMinutes} min ahead`;
  return 'On target';
}

export function CourseMonitorPage() {
  const toast = useToast();
  const { can } = useAuth();
  const courses = useGet<Page<Row>>('/api/v1/golf/courses?filter[status]=active&limit=50');
  const [course, setCourse] = useState('');
  const path = `/api/v1/golf/course-monitor${qs({ courseId: course || undefined })}`;
  const mon = useGet<Monitor>(path, { refetchInterval: 30_000 });
  useLive(GOLF_STREAM, ['golf.pace', 'golf.cart', 'golf.course_status'], useMemo(() => () => void mon.refetch(), [mon]));
  // a flight pausing (rain from the caddy tablet) or going on a break is told at once (demo feedback #28)
  const seen = useRef<Set<string> | null>(null);
  useEffect(() => {
    const now = new Set((mon.data?.flights ?? []).filter((f) => f.pausedAt).map((f) => f.flightId));
    if (seen.current) {
      for (const f of mon.data?.flights ?? []) {
        if (f.pausedAt && !seen.current.has(f.flightId)) toast(`${f.label}: ${behindText(f)}`, f.onBreak ? 'info' : 'error');
      }
    }
    if (mon.data) seen.current = now;
  }, [mon.data]); // eslint-disable-line react-hooks/exhaustive-deps
  const [selected, setSelected] = useState('');
  const [compose, setCompose] = useState<{ flight: Flight; kind: string } | null>(null);
  const [message, setMessage] = useState('');
  const send = useSend<Row, Schemas['PaceIntervention']>('POST', (b) => `/api/v1/golf/flights/${b.flightId}/pace-interventions`, [path]);
  const manage = can('golf.pace.manage');
  const m = mon.data;
  const courseOptions = (courses.data?.items ?? []).map((c) => ({ value: String(c.id), label: String(c.name) }));

  const act = (flight: Flight, kind: string) => {
    setMessage('');
    setCompose({ flight, kind });
  };
  const submit = () => {
    if (!compose) return;
    send.mutate({ flightId: compose.flight.flightId, kind: compose.kind, message: message || undefined }, {
      onSuccess: (r) => { toast(`${kindLabel(r.kind)} sent to ${r.flightLabel}`); setCompose(null); },
    });
  };

  return (
    <div className="oc-stack mon">
      <PageHeader title="Course Monitor" help="Flights in play on the course map, coloured by pace; actions reach the flight's caddy tablet."
        actions={courseOptions.length > 1 ? <div style={{ width: 240 }}><SelectField label="Course" value={course || String(m?.courseId ?? '')} onChange={setCourse} options={courseOptions} /></div> : undefined} />
      <ErrorAlert error={mon.error ?? send.error} />
      {!m ? <Skeleton rows={6} /> : (
        <>
          <CourseStops courseId={m.courseId} paused={m.flights.filter((f) => f.pausedAt && !f.onBreak).length} inPlay={m.inPlay} onChange={() => void mon.refetch()} />
          <div className="mon-stats">
            <StatTile label="In play" value={m.inPlay} icon="golf_course" />
            <StatTile label="Slow" value={m.slow} icon="hourglass_bottom" />
            <StatTile label="Paused · rain" value={m.flights.filter((f) => f.pausedAt && !f.onBreak).length} icon="rainy" />
            <StatTile label="On break" value={m.flights.filter((f) => f.onBreak).length} icon="local_cafe" />
            <StatTile label="Interventions today" value={m.interventions.length} icon="flag" />
            <StatTile label="Golf carts on GPS" value={m.golfCarts.filter((c) => c.source === 'tablet').length} icon="electric_car" />
          </div>
          <div className="mon-layout">
            <section className="oc-card mon-map-card" aria-label="Course map">
              <div className="oc-row-wrap" style={{ marginBottom: 4 }}>
                <strong>{m.courseName}</strong>
                <LiveWeather courseId={m.courseId} />
                <span className="oc-spacer" />
                <span className="oc-small oc-muted">Updated {hhmm(m.generatedAt)}</span>
              </div>
              <div className="oc-row-wrap" style={{ marginBottom: 10 }}>
                <span className="mon-legend"><i data-tone="on-pace" />On pace</span>
                <span className="mon-legend"><i data-tone="behind" />Behind</span>
                <span className="mon-legend"><i data-tone="slow" />Slow</span>
                <span className="mon-legend"><i data-tone="paused" />Paused (rain)</span>
                <span className="mon-legend"><i data-tone="break" />On break</span>
                <span className="mon-legend"><Icon name="electric_car" size={16} />Golf cart (caddy tablet GPS; faded: estimated on the hole)</span>
                <span className="mon-legend"><Icon name="construction" size={16} />Maintenance</span>
              </div>
              <CourseMap m={m} selected={selected} onSelect={setSelected} />
            </section>
            <section className="mon-flights" aria-label="Flights in play">
              {m.flights.length === 0 && <Empty title="No flight on the course" help="Flights appear after tee-off from the starter queue." icon="golf_course" />}
              {m.flights.map((f) => (
                <FlightCard key={f.flightId} f={f} selected={f.flightId === selected} onSelect={() => setSelected(f.flightId === selected ? '' : f.flightId)}
                  onAct={manage ? (kind) => act(f, kind) : undefined} />
              ))}
            </section>
          </div>
          <section className="oc-card">
            <h2 className="mon-h2">Messages with the caddy tablets</h2>
            <CourseMessenger courseId={m.courseId} flights={m.flights} selected={selected} onSelect={setSelected} canSend={manage} />
          </section>
          {can('golf.scorecard.correct') && <ScoreCorrections />}
          <section className="oc-card">
            <h2 className="mon-h2">Today's log</h2>
            {m.interventions.length === 0 && <p className="oc-small oc-muted" style={{ margin: 0 }}>No intervention today.</p>}
            <ul className="mon-log">
              {m.interventions.map((i) => (
                <li key={i.id}>
                  <span className="mon-log-time">{hhmm(i.createdAt)}</span>
                  <div className="mon-log-body">
                    <strong>{kindLabel(i.kind)} · {i.flightLabel}</strong>{i.hole ? <span className="oc-muted"> · {i.hole}</span> : null}
                    <div className="oc-small">{i.message}</div>
                    <div className="oc-small oc-muted">{i.createdByName ?? '—'}{i.behindMinutes ? ` · ${i.behindMinutes} min behind` : ''}</div>
                  </div>
                  {i.acknowledgedAt ? <StatusPill status="completed" label={`Seen ${hhmm(i.acknowledgedAt)}`} /> : <StatusPill status="pending" label="Not yet seen" />}
                </li>
              ))}
            </ul>
          </section>
        </>
      )}
      <Modal open={!!compose} onClose={() => setCompose(null)} title={compose ? `${kindLabel(compose.kind)} · ${compose.flight.label}` : ''}
        actions={<>
          <button className="oc-btn oc-btn-neutral" onClick={() => setCompose(null)}>Cancel</button>
          <button className="oc-btn oc-btn-ink" disabled={send.isPending || (compose?.kind === 'note' && !message.trim())} onClick={submit}>Send to caddy tablet</button>
        </>}>
        {compose && <>
          <p className="oc-muted">{compose.flight.holeNumber ? `Hole ${compose.flight.holeNumber}` : 'On the course'} · {behindText(compose.flight)}
            {compose.flight.caddies.length ? ` · caddy ${compose.flight.caddies.join(', ')}` : ''}</p>
          <TextArea label={compose.kind === 'note' ? 'Message' : 'Message (optional — a standard text is sent when empty)'} rows={3} value={message} onChange={setMessage}
            required={compose.kind === 'note'} />
        </>}
      </Modal>
    </div>
  );
}

type Stop = Schemas['CourseStop'];

/** Rain stop / lightning warning for the whole course from the Marshal's
 * phone or tablet (demo feedback 10 Oct 2026 #29): every flight in play
 * pauses, the caddy tablets get the message, the tee-off is held; Resume
 * play restarts them together. The day's stops stay listed for the report. */
function CourseStops({ courseId, paused, inPlay, onChange }: { courseId: string; paused: number; inPlay: number; onChange: () => void }) {
  const { can } = useAuth();
  const toast = useToast();
  const path = `/api/v1/golf/course-stops${qs({ courseId })}`;
  const stops = useGet<Page<Stop>>(path, { refetchInterval: 30_000 });
  const start = useSend<Row, Stop>('POST', '/api/v1/golf/course-stops', [path, '/api/v1/golf/course-monitor']);
  const resume = useSend<{ id: string }, Stop>('POST', (v) => `/api/v1/golf/course-stops/${v.id}:resume`, [path, '/api/v1/golf/course-monitor']);
  const [ask, setAsk] = useState<'rain_stop' | 'lightning_warning' | null>(null);
  const [reason, setReason] = useState('');
  const list = stops.data?.items ?? [];
  const open = list.find((x) => !x.endedAt);
  const manage = can('golf.course_status.update');
  const name = (k: string) => (k === 'lightning_warning' ? 'Lightning warning' : 'Rain stop');
  return (
    <>
      {open ? (
        <div className="oc-alert oc-alert-error mon-stop" role="alert">
          <Icon name={open.kind === 'lightning_warning' ? 'thunderstorm' : 'rainy'} size={22} />
          <div style={{ flex: 1 }}><strong>{name(open.kind)} since {hhmm(open.startedAt)}</strong> · {open.flights} flight{open.flights === 1 ? '' : 's'} paused · {open.minutes} min
            {open.reason ? ` · ${open.reason}` : ''}<div className="oc-small">Tee-off is suspended; the caddy tablets show the stop.</div></div>
          {manage && <button className="oc-btn oc-btn-ink" disabled={resume.isPending} onClick={() => resume.mutate({ id: open.id }, { onSuccess: () => { toast('Play resumed'); onChange(); } })}>
            <Icon name="play_arrow" size={18} /> Resume play</button>}
        </div>
      ) : (
        <div className="oc-row-wrap mon-stop-bar">
          {paused > 1 && <span className="oc-alert oc-alert-warning" style={{ padding: '6px 12px' }}><Icon name="rainy" size={18} /> {paused} of {inPlay} flights paused for rain{manage ? ' — set a rain stop for the course?' : ''}</span>}
          <span className="oc-spacer" />
          {manage && <>
            <button className="oc-btn oc-btn-outline" onClick={() => { setReason(''); setAsk('rain_stop'); }}><Icon name="rainy" size={18} /> Rain stop</button>
            <button className="oc-btn oc-btn-outline" onClick={() => { setReason(''); setAsk('lightning_warning'); }}><Icon name="thunderstorm" size={18} /> Lightning warning</button>
          </>}
        </div>
      )}
      {list.filter((x) => x.endedAt).length > 0 && (
        <p className="oc-small oc-muted" style={{ margin: 0 }}>Today: {list.filter((x) => x.endedAt).map((x) => `${name(x.kind)} ${hhmm(x.startedAt)}–${hhmm(x.endedAt!)} (${x.minutes} min, ${x.flights} flights)`).join(' · ')}</p>
      )}
      <ErrorAlert error={stops.error ?? start.error ?? resume.error} />
      <Modal open={!!ask} onClose={() => setAsk(null)} title={ask ? name(ask) : ''} actions={<>
        <button className="oc-btn oc-btn-neutral" onClick={() => setAsk(null)}>Cancel</button>
        <button className="oc-btn oc-btn-danger" disabled={start.isPending} onClick={() => ask && start.mutate({ courseId, kind: ask, reason: reason || undefined },
          { onSuccess: (r) => { toast(`${name(r.kind)}: ${r.flights} flight${r.flights === 1 ? '' : 's'} paused`); setAsk(null); onChange(); } })}>Stop play on the course</button>
      </>}>
        <p style={{ marginTop: 0 }}>Every flight in play pauses now (the pace stops), every caddy tablet gets "go to the nearest shelter", and the starter cannot tee off until you resume play.</p>
        <TextArea label="Reason (optional)" rows={2} value={reason} onChange={setReason} />
      </Modal>
    </>
  );
}

type Correction = Schemas['CorrectionRequest'];

/** Players' score corrections after the round (demo feedback 10 Oct 2026 #36):
 * the Marshal / handicap committee approves (the hole is changed, audited)
 * or rejects with a note the player sees in the Member App. */
function ScoreCorrections() {
  const toast = useToast();
  const path = '/api/v1/golf/score-correction-requests?filter[status]=requested';
  const list = useGet<Page<Correction>>(path, { refetchInterval: 60_000 });
  const decide = useSend<{ id: string; verb: string; note?: string }, Correction>('POST', (v) => `/api/v1/golf/score-correction-requests/${v.id}:${v.verb}`, [path]);
  const items = list.data?.items ?? [];
  if (!items.length) return null;
  return (
    <section className="oc-card">
      <h2 className="mon-h2">Score correction requests</h2>
      {items.map((x) => (
        <div key={x.id} className="oc-row-wrap" style={{ alignItems: 'center', marginBottom: 8 }}>
          <strong>{x.playerName}</strong><span className="oc-muted">{x.playedOn.slice(0, 10)}</span>
          <span>Hole {x.holeNumber} (par {x.par}): {x.currentStrokes ?? '—'} → <strong>{x.strokes}</strong></span>
          <span className="oc-small oc-muted" style={{ flex: 1 }}>{x.reason}</span>
          <button className="oc-btn oc-btn-sm oc-btn-ink" disabled={decide.isPending} onClick={() => decide.mutate({ id: x.id, verb: 'approve' }, { onSuccess: () => toast('Score corrected') })}>Approve</button>
          <button className="oc-btn oc-btn-sm oc-btn-outline" disabled={decide.isPending} onClick={() => {
            const note = window.prompt(`Why is the correction of ${x.playerName} rejected?`);
            if (note) decide.mutate({ id: x.id, verb: 'reject', note }, { onSuccess: () => toast('Correction rejected') });
          }}>Reject</button>
        </div>
      ))}
      <ErrorAlert error={list.error ?? decide.error} />
    </section>
  );
}

type Msg = Schemas['CourseMessage'];

/** Messenger of course control (OneClub replaces Smartscore's cart
 * messenger): one conversation per flight with its caddy tablet, and a
 * message to every flight on the course. The Marshal writes from
 * Operational, the back office from Golf › Course Monitor. */
function CourseMessenger({ courseId, flights, selected, onSelect, canSend }: {
  courseId: string; flights: Flight[]; selected: string; onSelect: (id: string) => void; canSend: boolean;
}) {
  const desk = useLocation().pathname.startsWith('/ops') ? 'marshal' : 'office';
  const path = `/api/v1/golf/course-messages${qs({ courseId })}`;
  const list = useGet<Page<Msg>>(path, { refetchInterval: 20_000 });
  useLive(GOLF_STREAM, ['golf.pace'], useMemo(() => () => void list.refetch(), [list]));
  const send = useSend<Row, Page<Msg>>('POST', '/api/v1/golf/course-messages', [path]);
  const read = useSend<{ flightId: string }, Page<Msg>>('POST', '/api/v1/golf/course-messages:read', [path]);
  const [text, setText] = useState('');
  const items = list.data?.items ?? [];
  const unreadOf = (fid: string) => items.filter((x) => x.flightId === fid && x.sender === 'tablet' && !x.readByCourseAt).length;
  // flights in play and flights that wrote today
  const threads = [...flights.map((f) => ({ id: f.flightId, label: f.label })),
    ...[...new Map(items.map((x) => [x.flightId, x.flightLabel])).entries()].filter(([fid]) => !flights.some((f) => f.flightId === fid)).map(([id, label]) => ({ id, label }))];
  const thread = items.filter((x) => x.flightId === selected);
  const broadcasts = [...new Map(items.filter((x) => x.broadcast).map((x) => [`${x.body}|${x.createdAt.slice(0, 16)}`, x])).values()];
  useEffect(() => {
    if (selected && unreadOf(selected) > 0 && !read.isPending) read.mutate({ flightId: selected });
  }, [selected, items.length]); // eslint-disable-line react-hooks/exhaustive-deps
  const post = () => {
    if (!text.trim()) return;
    send.mutate({ courseId, flightId: selected || undefined, body: text.trim(), desk }, { onSuccess: () => setText('') });
  };
  return (
    <div className="mon-msg-layout">
      <div className="mon-threads" role="list">
        <button className="mon-thread" aria-pressed={!selected} onClick={() => onSelect('')}><Icon name="campaign" size={18} /><span style={{ flex: 1 }}>Every flight on the course</span></button>
        {threads.map((t) => (
          <button key={t.id} className="mon-thread" aria-pressed={t.id === selected} onClick={() => onSelect(t.id)}>
            <Icon name="forum" size={18} /><span style={{ flex: 1 }}>{t.label}</span>
            {unreadOf(t.id) > 0 && <span className="mon-unread" aria-label={`${unreadOf(t.id)} unread`}>{unreadOf(t.id)}</span>}
          </button>
        ))}
      </div>
      <div className="oc-stack" style={{ gap: 10 }}>
        <div className="mon-chat" aria-live="polite">
          {(selected ? thread : broadcasts).length === 0 && <span className="oc-small oc-muted">{selected ? 'No message with this flight yet.' : 'No message to every flight today.'}</span>}
          {(selected ? thread : broadcasts).map((x) => (
            <div key={x.id} className="mon-bubble" data-mine={x.sender !== 'tablet' || undefined}>
              {x.body}
              <small>{x.senderName ?? x.sender} · {x.sender === 'tablet' ? 'caddy tablet' : x.sender === 'marshal' ? 'Marshal' : 'back office'} · {hhmm(x.createdAt)}
                {selected && x.sender !== 'tablet' ? (x.readByTabletAt ? ' · read' : ' · not yet read') : ''}</small>
            </div>
          ))}
        </div>
        {canSend && (
          <div className="oc-row-wrap" onKeyDown={(e) => { if (e.key === 'Enter') post(); }}>
            <input className="oc-input" style={{ flex: 1, minWidth: 200 }} maxLength={500} value={text} onChange={(e) => setText(e.target.value)}
              placeholder={selected ? `Message to ${threads.find((t) => t.id === selected)?.label ?? 'the flight'}` : 'Message to every flight on the course'} aria-label="Message" />
            <button className="oc-btn oc-btn-ink" disabled={send.isPending || !text.trim()} onClick={post}><Icon name="send" size={16} /> Send</button>
          </div>
        )}
        <ErrorAlert error={send.error ?? list.error} />
      </div>
    </div>
  );
}

/** A pin near the left or right edge of the map opens inwards. */
const pinAnchor = (x: number) => (x > 0.85 ? '-100%' : x < 0.15 ? '0%' : '-50%');

/** The course map with flights and golf carts; a hole board without a map. */
function CourseMap({ m, selected, onSelect }: { m: Monitor; selected: string; onSelect: (id: string) => void }) {
  const placed = m.mapUrl && m.holes.some((h) => h.mapX != null);
  if (!placed) {
    return (
      <div className="mon-board">
        {m.holes.map((h) => {
          const here = m.flights.filter((f) => f.holeNumber === h.number);
          return (
            <div key={h.holeId} className="mon-hole" data-busy={here.length > 0 || undefined}>
              <span className="mon-hole-no">{h.number}</span><span className="oc-small oc-muted">Par {h.par}</span>
              {h.maintenance.length > 0 && <span className="oc-small mon-maint" title="Course maintenance now"><Icon name="construction" size={14} />{h.maintenance.map((x) => x.replace(/_/g, ' ')).join(', ')}</span>}
              <div className="mon-hole-flights">
                {here.map((f) => <button key={f.flightId} className="mon-tag" data-tone={toneOf(f)} aria-pressed={f.flightId === selected}
                  onClick={() => onSelect(f.flightId)}>{f.label}</button>)}
              </div>
            </div>
          );
        })}
      </div>
    );
  }
  // flights on the same hole fan out around its marker
  const perHole = new Map<number, number>();
  return (
    <div className="mon-map">
      <img src={m.mapUrl!} alt={`${m.courseName} course map`} />
      {m.holes.filter((h) => h.maintenance.length > 0 && h.mapX != null).map((h) => (
        <span key={`mnt-${h.holeId}`} className="mon-maint-pin" style={{ left: `${(h.mapX ?? 0) * 100}%`, top: `${(h.mapY ?? 0) * 100}%` }}
          title={`Hole ${h.number}: ${h.maintenance.map((x) => x.replace(/_/g, ' ')).join(', ')}`}><Icon name="construction" size={14} /></span>
      ))}
      {m.golfCarts.map((c) => (
        <span key={c.golfCartId} className="mon-cart" data-source={c.source} style={{ left: `${(c.mapX ?? 0) * 100}%`, top: `${(c.mapY ?? 0) * 100}%` }}
          title={`Golf cart ${c.code} · ${c.source === 'tablet' ? `GPS ${hhmm(c.at)}` : 'estimated on the hole'}`}>
          <Icon name="electric_car" size={14} />
        </span>
      ))}
      {m.flights.filter((f) => f.mapX != null).map((f) => {
        // a flight on its tablet's GPS stands where it is; the others fan out on their hole
        const n = f.live ? 0 : perHole.get(f.holeNumber ?? 0) ?? 0;
        if (!f.live) perHole.set(f.holeNumber ?? 0, n + 1);
        return (
          <button key={f.flightId} className="mon-pin" data-tone={toneOf(f)} aria-pressed={f.flightId === selected}
            style={{ left: `${(f.mapX ?? 0) * 100}%`, top: `calc(${(f.mapY ?? 0) * 100}% + ${n * 26}px)`, transform: `translate(${pinAnchor(f.mapX ?? 0.5)}, -120%)` }}
            onClick={() => onSelect(f.flightId)} title={`${f.label} · hole ${f.holeNumber} · ${behindText(f)}`}>
            {f.label}
          </button>
        );
      })}
    </div>
  );
}

function FlightCard({ f, selected, onSelect, onAct }: { f: Flight; selected: boolean; onSelect: () => void; onAct?: (kind: string) => void }) {
  const last = f.interventions[0];
  return (
    <article className="oc-card mon-flight" data-tone={toneOf(f)} aria-current={selected || undefined}>
      <button className="mon-flight-head" onClick={onSelect} aria-expanded={selected}>
        <span className="mon-dot" data-tone={toneOf(f)} />
        <strong>{f.label}</strong>
        <span className="oc-muted">{f.holeNumber ? `Hole ${f.holeNumber}` : '—'} · {f.currentSeq}/{f.holes}</span>
        {f.live && <span className="oc-small mon-gps" title="Placed by the caddy tablet's GPS"><Icon name="gps_fixed" size={14} />GPS</span>}
        <span className="oc-spacer" />
        <strong className="mon-behind" data-tone={toneOf(f)}>{behindText(f)}</strong>
      </button>
      <div className="oc-small oc-muted">
        Tee-off {hhmm(f.teeOffAt)} · <PlayTime start={f.teeOffAt} pausedAt={f.pausedAt && !f.onBreak ? f.pausedAt : null} pausedSeconds={f.pausedMinutes * 60} label={false} /> played (target {f.targetMinutes} min)
        {f.breakMinutes ? ` · breaks ${f.breakMinutes} min` : ''}
        {f.aheadLabel ? ` · ${f.gapHoles ?? 0} hole${f.gapHoles === 1 ? '' : 's'} behind ${f.aheadLabel}` : ' · first on the route'}
      </div>
      <div className="oc-small">{f.players.join(', ') || '—'}</div>
      <div className="oc-small oc-muted">Caddy {f.caddies.join(', ') || '—'} · Golf cart {f.golfCarts.join(', ') || '—'}</div>
      {last && (
        <div className="oc-small mon-last">
          <Icon name="flag" size={14} /> {kindLabel(last.kind)} {formatDateTime(last.createdAt)}{last.acknowledgedAt ? ' · seen by the caddy' : ' · not yet seen'}
          {f.interventions.length > 1 ? ` · ${f.interventions.length} today` : ''}
        </div>
      )}
      {onAct && (selected || f.slow) && (
        <div className="oc-row-wrap mon-actions">
          {KINDS.map(([k, l, icon]) => (
            <button key={k} className={`oc-btn oc-btn-sm ${k === 'warning' || k === 'final_warning' ? 'oc-btn-outline' : 'oc-btn-neutral'}`} onClick={() => onAct(k)}>
              <Icon name={icon} size={16} />{l}</button>
          ))}
        </div>
      )}
    </article>
  );
}
