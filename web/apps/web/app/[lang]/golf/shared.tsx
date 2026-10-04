import type { ReactNode } from 'react';
import type { Lang } from '../../lib';

const SUB: [string, string][] = [
  ['', 'Golf Course'],
  ['/course-guide', 'Course Guide'],
  ['/hole-by-hole', 'Hole-by-Hole'],
  ['/handicap', 'Handicap'],
  ['/facilities', 'Facilities'],
];

/** Golf sub navigation (PRD P1 §6.4). */
export function GolfSection({ lang, current, title, children }: { lang: Lang; current: string; title: string; children: ReactNode }) {
  return (
    <div>
      <nav className="w-sub" aria-label="Golf">
        {SUB.map(([p, label]) => (
          <a key={p} href={`/${lang}/golf${p}`} aria-current={current === p ? 'page' : undefined}>
            {label}
          </a>
        ))}
        <a href={`/${lang}/book-golf`} className="w-cta">Book Golf</a>
      </nav>
      <h1>{title}</h1>
      {children}
    </div>
  );
}

export function Unavailable() {
  return <p className="w-muted">Course information is not available right now. Please try again later.</p>;
}
