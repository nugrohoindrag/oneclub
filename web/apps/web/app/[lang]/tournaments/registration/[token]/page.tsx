import { getProperty } from '../../../../lib-p2';
import { ManageRegistration } from './manage';

/** Tournament registration behind its secure link: status, payment, start, withdrawal. */
export default async function RegistrationPage({ params }: { params: Promise<{ lang: string; token: string }> }) {
  const { lang, token } = await params;
  const p = await getProperty();
  return (
    <div>
      <h1>{lang === 'id' ? 'Pendaftaran turnamen' : 'Tournament registration'}</h1>
      <ManageRegistration lang={lang} propertyId={p?.id ?? ''} token={token} />
    </div>
  );
}
