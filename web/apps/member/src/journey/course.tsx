import React, { useState } from 'react';
import { Link } from 'react-router';
import { useGet, type Page, type Schemas } from '@oneclub/api-client';
import { formatNumber } from '@oneclub/i18n';
import { Empty, ErrorAlert, Icon, Skeleton, TEE_CATEGORY } from '@oneclub/shell';
import { Head } from './ui';

/*
 * Course Guide (PRD P2 FR-PLX-01, roadmap §70 Hole by Hole and Handicap
 * Index): every hole with its par, stroke index, distance from each tee,
 * pictures and the club's description, plus the member's Course Handicap
 * from each tee — the number of strokes the member receives in a round.
 */

type Guide = Schemas['CourseGuide'];
type Tee = Guide['teeSets'][number];

const TEE_COLORS: Record<string, string> = { black: '#1d1d1f', blue: '#1f4fbf', white: '#ffffff', red: '#d1242f', gold: '#c9a227', green: '#2e7d32' };
const teeColor = (t: Pick<Tee, 'color' | 'code'>) => TEE_COLORS[(t.color ?? t.code).toLowerCase()] ?? '#8a8a8a';

export function TeeDot({ tee }: { tee: Pick<Tee, 'color' | 'code'> }) {
  return <i className="mj-tee-dot" style={{ background: teeColor(tee) }} aria-hidden />;
}

function useGuides() {
  return useGet<Page<Guide>>('/api/v1/member/golf/course-guide');
}

/** Course Handicap per tee for the member's Handicap Index. */
export function CourseHandicapCard({ guide, compact }: { guide: Guide; compact?: boolean }) {
  const has = guide.handicapIndex != null;
  return (
    <section className="mj-card">
      <h2 className="mj-section-title">Course Handicap · {guide.name}</h2>
      <p className="mj-small mj-muted" style={{ marginTop: 0 }}>
        {has ? <>With your Handicap Index <strong>{guide.handicapIndex}</strong> you receive this many strokes in a round, by the tee you play.</>
          : 'Play and finalize a few scorecards to get your Handicap Index; your course handicap per tee will show here.'}
      </p>
      <div className="mj-tee-table" role="table" aria-label="Course handicap per tee" style={{ '--cols': compact ? 3 : 4 } as React.CSSProperties}>
        <div role="row" className="mj-tee-row mj-tee-headrow">
          <span role="columnheader">Tee</span><span role="columnheader">Length</span>
          {!compact && <span role="columnheader">Rating / Slope</span>}
          <span role="columnheader">18 holes</span><span role="columnheader">9 holes</span>
        </div>
        {guide.teeSets.map((t) => (
          <div key={t.id} role="row" className="mj-tee-row">
            <span role="cell"><TeeDot tee={t} /> {t.name}{t.playerCategory ? <span className="mj-small mj-muted"> · {TEE_CATEGORY[t.playerCategory] ?? t.playerCategory}</span> : null}</span>
            <span role="cell" className="mj-num">{formatNumber(t.lengthMeters)} m</span>
            {!compact && <span role="cell" className="mj-num">{t.courseRating ?? '—'} / {t.slope ?? '—'}</span>}
            <span role="cell"><strong className="mj-num">{t.courseHandicap ?? '—'}</strong></span>
            <span role="cell" className="mj-num">{t.courseHandicap9 ?? '—'}</span>
          </div>
        ))}
      </div>
      {!compact && <TeeTables guide={guide} />}
    </section>
  );
}

const TEE_TABLES = ['red', 'blue', 'white', 'black'];

/** Tee classification: red women, blue / white general, black professional,
 * each with the club's Handicap Index → Course Handicap table. */
function TeeTables({ guide }: { guide: Guide }) {
  const [open, setOpen] = useState('');
  const tees = guide.teeSets.filter((t) => TEE_TABLES.includes((t.color ?? t.code).toLowerCase()));
  if (!tees.length) return null;
  return (
    <div className="oc-stack" style={{ marginTop: 12 }}>
      <strong>Tee classification</strong>
      <div className="mj-guests">
        {tees.map((t) => {
          const c = (t.color ?? t.code).toLowerCase();
          return (
            <button key={t.id} type="button" className="oc-chip" aria-pressed={open === c} onClick={() => setOpen(open === c ? '' : c)}>
              <TeeDot tee={t} /> {t.name} · {TEE_CATEGORY[t.playerCategory ?? ''] ?? '—'}
            </button>
          );
        })}
      </div>
      {open && <img src={`/tees/${open}.jpg`} alt={`Handicap table of the ${open} tee`} style={{ maxWidth: '100%', borderRadius: 12 }} loading="lazy" />}
    </div>
  );
}

/** The member's course handicap card for the Scores & Handicap page. */
export function MyCourseHandicap() {
  const guides = useGuides();
  const g = guides.data?.items[0];
  if (!g) return null;
  return (
    <>
      <CourseHandicapCard guide={g} compact />
      <Link className="oc-btn oc-btn-outline" to="/golf/course-guide" style={{ alignSelf: 'flex-start' }}><Icon name="map" size={18} /> Course Guide · Hole by Hole</Link>
    </>
  );
}

export function CourseGuidePage() {
  const guides = useGuides();
  const [ci, setCi] = useState(0);
  const [hi, setHi] = useState(0);
  const g = guides.data?.items[ci];
  const hole = g?.holes[hi];
  return (
    <div className="mj-page">
      <Head title="Course Guide" help="Hole by hole: par, stroke index, distances from every tee and how to play it." />
      <ErrorAlert error={guides.error} />
      {guides.isLoading && <Skeleton rows={6} />}
      {guides.data?.items.length === 0 && <Empty title="No course guide yet" icon="golf_course" />}
      {(guides.data?.items.length ?? 0) > 1 && (
        <nav className="mj-tabs" aria-label="Course">
          {guides.data!.items.map((c, i) => <button key={c.courseId} aria-pressed={i === ci} onClick={() => { setCi(i); setHi(0); }}>{c.name}</button>)}
        </nav>
      )}
      {g && (
        <>
          <section className="mj-card mj-course-top">
            <div>
              <h2 className="mj-section-title" style={{ marginBottom: 4 }}>{g.name}</h2>
              <div className="mj-small mj-muted">{g.holes.length} holes · Par {g.par}{g.lengthMeters ? ` · ${formatNumber(g.lengthMeters)} m` : ''}</div>
              {g.guide && <p className="mj-small" style={{ marginBottom: 0 }}>{g.guide}</p>}
            </div>
            {g.mapUrl && <img className="mj-course-map" src={g.mapUrl} alt={`${g.name} course map`} />}
          </section>
          <nav className="mj-hole-tabs" aria-label="Hole">
            {g.holes.map((h, i) => <button key={h.holeId} aria-pressed={i === hi} onClick={() => setHi(i)}>{h.number}</button>)}
          </nav>
          {hole && (
            <section className="mj-card mj-hole">
              <div className="mj-hole-pics">
                {hole.images.length === 0 && <div className="mj-hole-nopic"><Icon name="golf_course" size={40} /></div>}
                {hole.images.map((p) => <img key={p.code} src={p.url} alt={p.name} loading="lazy" />)}
              </div>
              <div className="mj-hole-body">
                <div className="mj-hole-title">
                  <h2>Hole {hole.number}</h2>
                  <span className="mj-chip">Par {hole.par}</span>
                  {hole.strokeIndex != null && <span className="mj-chip" title="Stroke Index: 1 is the hardest hole, 18 the easiest">Index {hole.strokeIndex}</span>}
                </div>
                <div className="mj-hole-tees">
                  {g.teeSets.filter((t) => hole.distances[t.code] != null).map((t) => (
                    <div key={t.id} className="mj-hole-tee"><TeeDot tee={t} /><span>{t.name.replace(/\s*\(.*\)$/, '')}</span><strong className="mj-num">{hole.distances[t.code]} m</strong></div>
                  ))}
                </div>
                {hole.description && <p>{hole.description}</p>}
                {g.teeSets.some((t) => t.courseHandicap != null) && hole.strokeIndex != null && (
                  <p className="mj-small mj-muted">
                    You receive a stroke here when your course handicap is {hole.strokeIndex} or more{g.holes.length >= 18 ? `, two when it is ${hole.strokeIndex + g.holes.length} or more` : ''}.
                  </p>
                )}
                <div className="mj-actions">
                  <button className="oc-btn oc-btn-outline" disabled={hi === 0} onClick={() => setHi(hi - 1)}><Icon name="chevron_left" size={18} /> Hole {g.holes[hi - 1]?.number ?? ''}</button>
                  <button className="oc-btn oc-btn-outline" disabled={hi >= g.holes.length - 1} onClick={() => setHi(hi + 1)}>Hole {g.holes[hi + 1]?.number ?? ''} <Icon name="chevron_right" size={18} /></button>
                </div>
              </div>
            </section>
          )}
          <CourseHandicapCard guide={g} />
          <section className="mj-card">
            <h2 className="mj-section-title">How handicaps work</h2>
            <ul className="mj-small" style={{ margin: 0, paddingLeft: 18 }}>
              <li><strong>Handicap Index</strong> is your playing ability: about how many strokes over par you play on a course of standard difficulty. It comes from your finalized scorecards.</li>
              <li><strong>Course Handicap</strong> turns it into strokes on this course from the tee you play: Index × Slope ÷ 113 + (Course Rating − Par).</li>
              <li><strong>Stroke Index</strong> ranks the holes from hardest (1) to easiest (18): your strokes are given on the hardest holes first.</li>
            </ul>
          </section>
        </>
      )}
    </div>
  );
}
