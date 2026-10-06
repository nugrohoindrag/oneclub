'use client';
import { BookingForm, consentLabel } from '../booking';

/*
 * Book Event (PRD P3 FR-WEB-P3-03): registration for an open event with the
 * online payment of the fee; the confirmation shows the QR ticket code. The
 * Wedding & Banquet inquiry goes to CRM Leads (FR-WEB-P3-02).
 */

export function EventRegistration({ propertyId, eventId, lang, paid }: { propertyId: string; eventId: string; lang: string; paid: boolean }) {
  return (
    <BookingForm title={lang === 'id' ? 'Daftar' : 'Register'} path={`/api/v1/public/events/${eventId}/registrations`} propertyId={propertyId} pay={paid}
      submitLabel={lang === 'id' ? 'Daftar' : 'Register'}
      fields={[
        { name: 'partySize', label: lang === 'id' ? 'Jumlah kursi' : 'Seats', type: 'number', initial: '1' },
        { name: 'company', label: lang === 'id' ? 'Perusahaan' : 'Company' },
        { name: 'dietaryNotes', label: lang === 'id' ? 'Catatan makanan' : 'Dietary notes' },
      ]}
      build={(v) => ({ partySize: Number(v.partySize || 1), company: v.company || undefined, dietaryNotes: v.dietaryNotes || undefined })}
      done={(r) => (
        <p>
          {lang === 'id' ? 'Kode tiket' : 'Ticket code'} <strong>{String(r.ticketCode)}</strong> · {String(r.status)}
          {r.waitlistRank ? ` #${String(r.waitlistRank)}` : ''} — <a href={`/${lang}/events/ticket/${String(r.ticketCode)}`}>{lang === 'id' ? 'lihat tiket' : 'view ticket'}</a>
        </p>
      )} />
  );
}

const INQUIRY_LINES: Record<string, { line: string; eventType?: string }> = {
  wedding: { line: 'wedding', eventType: 'wedding' }, banquet: { line: 'banquet' }, mice: { line: 'mice', eventType: 'meeting' }, event: { line: 'event' },
  corporate_golf: { line: 'golf', eventType: 'tournament' }, tournament: { line: 'tournament', eventType: 'tournament' }, membership: { line: 'membership' },
};

/**
 * Inquiry form (FR-WEB-P3-02): wedding, banquet, MICE, corporate golf,
 * tournament and membership become a CRM lead; marketing consent is an
 * explicit, unticked opt-in (FR-LEAD-09).
 */
export function BanquetInquiry({ propertyId, lang, initial = 'wedding' }: { propertyId: string; lang: string; initial?: string }) {
  const id = lang === 'id';
  return (
    <BookingForm title={id ? 'Minta penawaran' : 'Request a quotation'} path="/api/v1/public/inquiries" propertyId={propertyId} pay={false}
      submitLabel={id ? 'Kirim' : 'Send'} consent={consentLabel(lang)}
      fields={[
        { name: 'line', label: id ? 'Jenis acara' : 'Event', type: 'select', initial, options: [{ value: 'wedding', label: 'Wedding' },
          { value: 'banquet', label: 'Banquet' }, { value: 'mice', label: 'Meeting & MICE' }, { value: 'corporate_golf', label: id ? 'Golf korporat' : 'Corporate golf' },
          { value: 'tournament', label: id ? 'Turnamen golf' : 'Golf tournament' }, { value: 'membership', label: id ? 'Keanggotaan' : 'Membership' },
          { value: 'event', label: id ? 'Acara lain' : 'Other event' }] },
        { name: 'companyName', label: id ? 'Perusahaan (opsional)' : 'Company (optional)' },
        { name: 'eventDate', label: id ? 'Tanggal acara' : 'Event date', type: 'date' },
        { name: 'pax', label: 'Pax', type: 'number' },
        { name: 'message', label: id ? 'Pesan' : 'Message', type: 'textarea', required: true },
      ]}
      build={(v) => ({ ...INQUIRY_LINES[v.line], companyName: v.companyName || undefined, eventDate: v.eventDate || undefined, pax: v.pax ? Number(v.pax) : undefined,
        message: v.message, channel: `website_${v.line}` })}
      done={() => <p>{id ? 'Terima kasih — tim sales kami akan menghubungi Anda.' : 'Thank you — our sales team will contact you.'}</p>} />
  );
}
