import { useMemo, useState } from 'react';
import { qs, useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDateTime } from '@oneclub/i18n';
import {
  Empty, ErrorAlert, Icon, Modal, PageHeader, PlayTime, SelectField, Skeleton, StatTile, StatusPill, TextArea, useAuth, useToast,
} from '@oneclub/shell';
import { GOLF_STREAM, useLive } from '../live';
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

/** on pace · behind (within tolerance) · slow */
const toneOf = (f: Flight) => (f.slow ? 'slow' : f.behindMinutes > 0 ? 'behind' : 'on-pace');

function behindText(f: Flight) {
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
  useLive(GOLF_STREAM, ['golf.pace', 'golf.cart'], useMemo(() => () => void mon.refetch(), [mon]));
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
          <div className="mon-stats">
            <StatTile label="In play" value={m.inPlay} icon="golf_course" />
            <StatTile label="Slow" value={m.slow} icon="hourglass_bottom" />
            <StatTile label="Interventions today" value={m.interventions.length} icon="flag" />
            <StatTile label="Golf carts on GPS" value={m.golfCarts.length} icon="electric_car" />
          </div>
          <div className="mon-layout">
            <section className="oc-card mon-map-card" aria-label="Course map">
              <div className="oc-row-wrap" style={{ marginBottom: 4 }}>
                <strong>{m.courseName}</strong>
                <span className="oc-spacer" />
                <span className="oc-small oc-muted">Updated {hhmm(m.generatedAt)}</span>
              </div>
              <div className="oc-row-wrap" style={{ marginBottom: 10 }}>
                <span className="mon-legend"><i data-tone="on-pace" />On pace</span>
                <span className="mon-legend"><i data-tone="behind" />Behind</span>
                <span className="mon-legend"><i data-tone="slow" />Slow</span>
                <span className="mon-legend"><Icon name="electric_car" size={16} />Golf cart</span>
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
      {m.golfCarts.map((c) => (
        <span key={c.golfCartId} className="mon-cart" style={{ left: `${(c.mapX ?? 0) * 100}%`, top: `${(c.mapY ?? 0) * 100}%` }} title={`Golf cart ${c.code}`}>
          <Icon name="electric_car" size={14} />
        </span>
      ))}
      {m.flights.filter((f) => f.mapX != null).map((f) => {
        const n = perHole.get(f.holeNumber ?? 0) ?? 0;
        perHole.set(f.holeNumber ?? 0, n + 1);
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
        <span className="oc-spacer" />
        <strong className="mon-behind" data-tone={toneOf(f)}>{behindText(f)}</strong>
      </button>
      <div className="oc-small oc-muted">
        Tee-off {hhmm(f.teeOffAt)} · <PlayTime start={f.teeOffAt} label={false} /> played (target {f.targetMinutes} min)
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
