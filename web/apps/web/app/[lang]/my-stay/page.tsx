import type { Lang } from '../../lib';
import { getProperty } from '../../lib-p2';
import { MyStay } from './stay';

/** My Stay for a guest without an account: reservation reference + e-mail or phone (accommodation requirements §26). */
export default async function MyStayPage({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  const p = await getProperty();
  return (
    <div>
      <h1>My Stay</h1>
      {p ? <MyStay lang={lang as Lang} propertyId={p.id} /> : <p className="w-muted">{lang === 'id' ? 'Belum tersedia.' : 'Not available yet.'}</p>}
    </div>
  );
}
