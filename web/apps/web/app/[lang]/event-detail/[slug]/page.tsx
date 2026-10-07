import { EventDetail, EVENT_DETAILS } from '../../../../components/mgcc/pages';

/** What's On detail of the live site (www.moderngolf.co.id). */
export default EventDetail;

export function generateStaticParams() {
  return EVENT_DETAILS.map((slug) => ({ slug }));
}
