import type { Lang } from '../../../lib';
import { Unsubscribe } from './unsubscribe';

/** Unsubscribe link of a campaign message (PRD P3 FR-CMP-02, UU PDP). */
export default async function UnsubscribePage({ params }: { params: Promise<{ lang: string; token: string }> }) {
  const { lang, token } = await params;
  return (
    <div>
      <h1>{lang === 'id' ? 'Berhenti berlangganan' : 'Unsubscribe'}</h1>
      <Unsubscribe lang={lang as Lang} token={token} />
    </div>
  );
}
