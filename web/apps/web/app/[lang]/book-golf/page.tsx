import type { Lang } from '../../lib';
import { BookGolf } from './book';

/** Book Golf for the public (FR-WEB-04..07). */
export default async function BookGolfPage({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  return (
    <div>
      <h1>Book Golf</h1>
      <BookGolf lang={lang as Lang} />
    </div>
  );
}
