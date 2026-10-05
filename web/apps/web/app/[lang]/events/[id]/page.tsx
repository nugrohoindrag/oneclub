import { getProperty, pub, rp } from '../../../lib-p2';
import { EventRegistration } from '../forms';
import { eventLD, when, type PublicEvent } from '../shared';

/** An open event with Book Event (PRD P3 FR-WEB-P3-03). */
export default async function EventPage({ params }: { params: Promise<{ lang: string; id: string }> }) {
  const { lang, id } = await params;
  const p = await getProperty();
  if (!p) return <div className="w-card">Not available</div>;
  const e = await pub<PublicEvent>(`/api/v1/public/events/${encodeURIComponent(id)}?propertyId=${p.id}`);
  const ind = lang === 'id';
  if (!e) return <div className="w-card">{ind ? 'Acara tidak ditemukan.' : 'Event not found.'}</div>;
  const full = e.seatsLeft === 0;
  return (
    <div className="w-grid">
      <article className="w-card">
        <h1 style={{ marginTop: 0 }}>{e.title}</h1>
        <p>{e.eventType} · {when(e.start, lang)} – {when(e.end, lang)}</p>
        {e.venues.length > 0 && <p>{e.venues.join(', ')}</p>}
        {e.description && <p>{e.description}</p>}
        <p>
          {Number(e.fee) > 0 ? `${rp(e.fee)} / ${ind ? 'kursi' : 'seat'}` : ind ? 'Gratis' : 'Free'}
          {e.seatsLeft !== null && e.seatsLeft !== undefined ? ` · ${full ? (ind ? 'penuh — daftar tunggu' : 'full — waitlist') : `${e.seatsLeft} ${ind ? 'kursi tersisa' : 'seats left'}`}` : ''}
        </p>
        {e.membersOnly && <p>{ind ? 'Khusus member: daftar lewat Member App.' : 'Members only: register in the Member App.'}</p>}
      </article>
      {e.registrationOpen && !e.membersOnly && <EventRegistration propertyId={p.id} eventId={e.id} lang={lang} paid={Number(e.fee) > 0 && !full} />}
      <script type="application/ld+json" dangerouslySetInnerHTML={{ __html: JSON.stringify(eventLD(e, `/${lang}/events/${e.id}`, p.name)) }} />
    </div>
  );
}
