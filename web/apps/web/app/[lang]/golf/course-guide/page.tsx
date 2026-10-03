import { getGolfInfo, type Lang } from '../../../lib';
import { GolfSection, Unavailable } from '../shared';

/** Course Guide (FR-WEB-02): routes and tee sets. */
export default async function CourseGuide({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  const info = await getGolfInfo();
  return (
    <GolfSection lang={lang as Lang} current="/course-guide" title="Course Guide">
      {!info && <Unavailable />}
      {info?.courses.map((c) => (
        <div key={c.id} className="w-card" style={{ marginBottom: 16 }}>
          <h2>{c.name}</h2>
          {c.guide && <p>{c.guide}</p>}
          <h3>Playing routes</h3>
          <ul>{c.routes.map((r) => <li key={r.code}>{r.name} — {r.holeCount} holes, par {r.par}</li>)}</ul>
          <h3>Tee sets</h3>
          <table className="w-table">
            <thead><tr><th>Tee</th><th>Course rating</th><th>Slope</th></tr></thead>
            <tbody>{c.teeSets.map((t) => <tr key={t.code}><td>{t.name}</td><td>{t.courseRating ?? '—'}</td><td>{t.slope ?? '—'}</td></tr>)}</tbody>
          </table>
        </div>
      ))}
    </GolfSection>
  );
}
