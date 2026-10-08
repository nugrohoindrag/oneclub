import type { Lang } from '../../lib';
import { getProperty } from '../../lib-p2';
import { BookGolf } from './book';
import { BookGolfOrRange } from './range';

/** Book Golf for the public (FR-WEB-04..07): a tee time or the driving range. */
export default async function BookGolfPage({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  const p = await getProperty();
  return (
    <div>
      <h1>Book Golf</h1>
      {p ? <BookGolfOrRange lang={lang as Lang} propertyId={p.id} /> : <BookGolf lang={lang as Lang} />}
    </div>
  );
}
