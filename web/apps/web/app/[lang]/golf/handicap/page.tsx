import type { Lang } from '../../../lib';
import { GolfSection } from '../shared';

/** Handicap information (PRD P1 §6.4). */
export default async function Handicap({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  return (
    <GolfSection lang={lang as Lang} current="/handicap" title="Handicap">
      <div className="w-card">
        <p>Members can see their Handicap Index in the Member Portal. The club records handicap updates; handicap calculation from scorecards arrives with Scoring in a later phase.</p>
        <p>Course rating and slope per tee set are listed in the Course Guide.</p>
      </div>
    </GolfSection>
  );
}
