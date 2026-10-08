import { getProperty, pub, rp } from '../../../lib-p2';
import { eventLD, when, type PublicEvent } from '../shared';

/** Events page from structured data (PRD P3 FR-WEB-P3-01). */
export default async function Events({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  const p = await getProperty();
  if (!p) return <div className="w-card">Not available</div>;
  const data = await pub<{ items: PublicEvent[] }>(`/api/v1/public/events?propertyId=${p.id}`);
  const events = data?.items ?? [];
  const id = lang === 'id';
  return (
    <div className="w-grid">
      <div className="w-card">
        <h1 style={{ marginTop: 0 }}>{id ? 'Acara' : 'Events'}</h1>
        <p>{id ? 'Acara club yang terbuka untuk pendaftaran.' : 'Club events open for registration.'}</p>
        <p><a href={`/${lang}/wedding-banquet`}>Wedding &amp; Banquet</a></p>
      </div>
      {events.length === 0 && <div className="w-card">{id ? 'Belum ada acara terbuka.' : 'No open events at the moment.'}</div>}
      {events.map((e) => (
        <article key={e.id} className="w-card">
          <h2 style={{ marginTop: 0 }}><a href={`/${lang}/events/${e.id}`}>{e.title}</a></h2>
          <p>{e.eventType} · {when(e.start, lang)}{e.venues.length ? ` · ${e.venues.join(', ')}` : ''}</p>
          {e.description && <p>{e.description}</p>}
          <p>
            {Number(e.fee) > 0 ? `${rp(e.fee)} / ${id ? 'kursi' : 'seat'}` : id ? 'Gratis' : 'Free'}
            {e.seatsLeft !== null && e.seatsLeft !== undefined ? ` · ${e.seatsLeft} ${id ? 'kursi tersisa' : 'seats left'}` : ''}
            {e.membersOnly ? ` · ${id ? 'khusus member' : 'members only'}` : ''}
          </p>
          {e.registrationOpen && !e.membersOnly && <a className="w-btn" href={`/${lang}/events/${e.id}`}>Book Event</a>}
        </article>
      ))}
      <script type="application/ld+json" dangerouslySetInnerHTML={{ __html: JSON.stringify(events.map((e) => eventLD(e, `/${lang}/events/${e.id}`, p.name))) }} />
    </div>
  );
}
