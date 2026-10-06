/** An open event of the Events page (contract K5, GET /api/v1/public/events). */
export interface PublicEvent {
  id: string;
  number: string;
  title: string;
  eventType: string;
  category: string;
  start: string;
  end: string;
  venues: string[];
  description?: string | null;
  capacity?: number | null;
  seatsLeft?: number | null;
  registrationOpen: boolean;
  fee: string;
  currency: string;
  membersOnly: boolean;
}

export const when = (s: string, lang: string, timeZone = 'Asia/Jakarta') =>
  new Date(s).toLocaleString(lang === 'id' ? 'id-ID' : 'en-GB', { dateStyle: 'full', timeStyle: 'short', timeZone });

/** schema.org Event of an open event (structured data, FR-WEB-P3-01). */
export function eventLD(e: PublicEvent, url: string, club: string) {
  return {
    '@context': 'https://schema.org', '@type': 'Event', name: e.title, startDate: e.start, endDate: e.end, eventStatus: 'https://schema.org/EventScheduled',
    eventAttendanceMode: 'https://schema.org/OfflineEventAttendanceMode', description: e.description ?? undefined, url,
    location: { '@type': 'Place', name: e.venues.length ? `${e.venues.join(', ')} · ${club}` : club },
    organizer: { '@type': 'Organization', name: club },
    offers: { '@type': 'Offer', price: e.fee, priceCurrency: e.currency, url,
      availability: e.seatsLeft === 0 ? 'https://schema.org/SoldOut' : 'https://schema.org/InStock' },
  };
}
