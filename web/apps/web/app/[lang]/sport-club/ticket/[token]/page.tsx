import type { Metadata } from 'next';
import { notFound } from 'next/navigation';
import { getProperty } from '../../../../lib-p2';

export const metadata: Metadata = { title: 'Tiket Sport Club', robots: { index: false } };

const API = process.env.ONECLUB_API_URL ?? 'http://localhost:8080';

interface Ticket { ticketNo: string; facilityName: string; entryType: string; guestName?: string | null; visitDate: string; status: string; qrToken: string }

/**
 * Guest With Member ticket shared by a member from the Member App (FR-117):
 * the QR to show at the Sport Club front desk, valid with the member present.
 */
export default async function GuestTicketPage({ params }: { params: Promise<{ lang: string; token: string }> }) {
  const { lang, token } = await params;
  const id = lang === 'id';
  const p = await getProperty();
  if (!p) notFound();
  const r = await fetch(`${API}/api/v1/public/sport-club/tickets/${encodeURIComponent(token)}?propertyId=${p.id}`, { cache: 'no-store' }).catch(() => null);
  if (!r?.ok) notFound();
  const t = (await r.json()) as Ticket;
  const day = new Date(t.visitDate).toLocaleDateString(id ? 'id-ID' : 'en-GB', { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric', timeZone: 'UTC' });
  return (
    <div className="w-sc-page" style={{ maxWidth: 520 }}>
      <h1>{id ? 'Tiket Tamu Sport Club' : 'Sport Club Guest Ticket'}</h1>
      <div className="w-card" style={{ textAlign: 'center' }}>
        <p className="w-sc-code">{t.ticketNo}</p>
        {t.guestName && <p><strong>{t.guestName}</strong></p>}
        <p>{t.facilityName} · {day}</p>
        {t.status === 'issued' ? (
          <img src={`/api/v1/public/sport-club/tickets/${encodeURIComponent(token)}/qr.png?propertyId=${p.id}`} alt="QR" width={240} height={240} style={{ margin: '12px auto', display: 'block' }} />
        ) : <p className="w-error">{t.status === 'used' ? (id ? 'Tiket sudah dipakai.' : 'This ticket has been used.') : (id ? 'Tiket tidak berlaku.' : 'This ticket is not valid.')}</p>}
        <p className="w-muted">{id ? 'Tunjukkan QR ini di front desk Sport Club bersama member yang mengundang Anda.' : 'Show this QR at the Sport Club front desk together with the member who invited you.'}</p>
      </div>
    </div>
  );
}
