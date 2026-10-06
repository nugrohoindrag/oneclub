'use client';
import type { Lang } from '../../lib';
import { BookingForm } from '../booking';

const LINES = ['other', 'golf', 'sportclub', 'stay', 'pos', 'membership', 'banquet'];
const LINE_LABELS: Record<string, [string, string]> = {
  other: ['Lainnya', 'Other'], golf: ['Golf', 'Golf'], sportclub: ['Sport Club', 'Sport Club'], stay: ['Bungalow & VIP Suite', 'Bungalow & VIP Suite'],
  pos: ['Restoran & Bar', 'Restaurant & Bar'], membership: ['Keanggotaan', 'Membership'], banquet: ['Banquet & Event', 'Banquet & Event'],
};

export function ComplaintForm({ propertyId, lang }: { propertyId: string; lang: Lang }) {
  const id = lang === 'id';
  return (
    <BookingForm title={id ? 'Keluhan Anda' : 'Your complaint'} path="/api/v1/public/complaints" propertyId={propertyId} pay={false}
      submitLabel={id ? 'Kirim' : 'Send'}
      fields={[
        { name: 'businessLine', label: id ? 'Area' : 'Area', type: 'select', initial: 'other',
          options: LINES.map((l) => ({ value: l, label: LINE_LABELS[l][id ? 0 : 1] })) },
        { name: 'subject', label: id ? 'Perihal' : 'Subject', required: true },
        { name: 'description', label: id ? 'Ceritakan apa yang terjadi' : 'What happened?', type: 'textarea', required: true },
      ]}
      build={(v) => ({ businessLine: v.businessLine, subject: v.subject, description: v.description })}
      done={(r) => <p role="status">{id ? `Terima kasih. Nomor keluhan Anda ${String(r.number)}.` : `Thank you. Your complaint number is ${String(r.number)}.`}</p>} />
  );
}
