const API = process.env.ONECLUB_API_URL ?? 'http://localhost:8080';

interface Ticket {
  ticketCode: string;
  status: string;
  waitlistRank?: number | null;
  name: string;
  partySize: number;
  fee: string;
  paymentStatus: string;
  eventTitle: string;
  eventStart: string;
}

/** The event ticket behind its QR code: status and payment (FR-WEB-P3-03). */
export default async function TicketPage({ params }: { params: Promise<{ lang: string; code: string }> }) {
  const { lang, code } = await params;
  const id = lang === 'id';
  let t: Ticket | null = null;
  try {
    const r = await fetch(`${API}/api/v1/public/event-tickets/${encodeURIComponent(code)}`, { cache: 'no-store' });
    if (r.ok) t = (await r.json()) as Ticket;
  } catch {
    /* unavailable */
  }
  if (!t) return <div className="w-card">{id ? 'Tiket tidak ditemukan.' : 'Ticket not found.'}</div>;
  return (
    <div className="w-card" role="status">
      <h1 style={{ marginTop: 0 }}>{t.eventTitle}</h1>
      <p>{new Date(t.eventStart).toLocaleString(id ? 'id-ID' : 'en-GB', { dateStyle: 'full', timeStyle: 'short', timeZone: 'Asia/Jakarta' })}</p>
      <p>{t.name} · {t.partySize} {id ? 'kursi' : 'seat(s)'}</p>
      <p style={{ fontSize: '2rem', letterSpacing: '0.2em' }}><strong>{t.ticketCode}</strong></p>
      <p>{id ? 'Tunjukkan kode ini saat check-in.' : 'Show this code at the check-in.'}</p>
      <p>{id ? 'Status' : 'Status'}: {t.status}{t.waitlistRank ? ` #${t.waitlistRank}` : ''} · {id ? 'Pembayaran' : 'Payment'}: {t.paymentStatus}</p>
    </div>
  );
}
