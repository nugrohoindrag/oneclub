'use client';
import { BookingForm } from '../booking';

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

export function BanquetInquiry({ propertyId, lang }: { propertyId: string; lang: string }) {
  const id = lang === 'id';
  return (
    <BookingForm title={id ? 'Minta penawaran' : 'Request a quotation'} path="/api/v1/public/inquiries" propertyId={propertyId} pay={false}
      submitLabel={id ? 'Kirim' : 'Send'}
      fields={[
        { name: 'line', label: id ? 'Jenis acara' : 'Event', type: 'select', initial: 'wedding', options: [{ value: 'wedding', label: 'Wedding' },
          { value: 'banquet', label: 'Banquet' }, { value: 'mice', label: 'Meeting & MICE' }, { value: 'event', label: id ? 'Acara lain' : 'Other event' }] },
        { name: 'eventDate', label: id ? 'Tanggal acara' : 'Event date', type: 'date' },
        { name: 'pax', label: 'Pax', type: 'number' },
        { name: 'message', label: id ? 'Pesan' : 'Message', type: 'textarea', required: true },
      ]}
      build={(v) => ({ line: v.line, eventDate: v.eventDate || undefined, pax: v.pax ? Number(v.pax) : undefined, message: v.message, channel: 'website_wedding_banquet' })}
      done={() => <p>{id ? 'Terima kasih — tim sales kami akan menghubungi Anda.' : 'Thank you — our sales team will contact you.'}</p>} />
  );
}
