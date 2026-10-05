'use client';
import { BookingForm } from '../../app/[lang]/booking';

/** Contact form block: the public contact or inquiry endpoint of CRM. */
export function CmsForm({ propertyId, action, topic, line, heading, submitLabel, successMessage }: {
  propertyId: string; action: string; topic?: string; line?: string; heading?: string; submitLabel?: string; successMessage?: string;
}) {
  const thanks = () => <p>{successMessage || 'Thank you — we will get back to you soon.'}</p>;
  if (action.endsWith('/inquiries')) {
    return (
      <BookingForm title={heading || 'Request a quotation'} path={action} propertyId={propertyId} pay={false} submitLabel={submitLabel || 'Send'}
        fields={[
          { name: 'eventDate', label: 'Event date', type: 'date' },
          { name: 'pax', label: 'Guests', type: 'number' },
          { name: 'message', label: 'Message', type: 'textarea', required: true },
        ]}
        build={(v) => ({ line: line || undefined, eventDate: v.eventDate || undefined, pax: v.pax ? Number(v.pax) : undefined, message: v.message,
          channel: typeof window === 'undefined' ? undefined : window.location.pathname })}
        done={thanks} />
    );
  }
  return (
    <BookingForm title={heading || 'Send us a message'} path={action} propertyId={propertyId} pay={false} submitLabel={submitLabel || 'Send'}
      fields={[{ name: 'message', label: 'Message', type: 'textarea', required: true }]}
      build={(v) => ({ topic: topic || 'general', message: v.message })} done={thanks} />
  );
}
