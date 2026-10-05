import React, { useState } from 'react';
import { Link, useParams } from 'react-router';
import { useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDate, formatDateTime, formatNumber } from '@oneclub/i18n';
import { Card, DataTable, Empty, ErrorAlert, Modal, PageHeader, QRCode, SelectField, Skeleton, StatusPill, TextField } from '@oneclub/shell';
import { CheckoutModal } from '../p2';

// Member App — Events (PRD P3 FR-APP-P3-03): Upcoming Events, Event
// Registration with the QR ticket (online payment of the fee) and My Events —
// my tickets and the banquets I booked with their payment schedule
// (FR-APP-P3-07).

type Row = Record<string, unknown>;
const money = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : `Rp ${formatNumber(Number(v))}`);
const list = (v: unknown) => (Array.isArray(v) ? (v as Row[]) : []);
const INV = ['/api/v1/member/events', '/api/v1/member/my-events'];

function Ticket({ t }: { t: Row }) {
  return (
    <div className="oc-stack" style={{ alignItems: 'center' }}>
      <QRCode value={String(t.ticketCode)} size={180} label={`Ticket ${String(t.ticketCode)}`} />
      <div className="oc-code">{String(t.ticketCode)}</div>
      <div><StatusPill status={String(t.status)} />{t.waitlistRank ? ` waitlist #${String(t.waitlistRank)}` : ''}</div>
      <div className="oc-small">{String(t.partySize)} seat(s) · payment <StatusPill status={String(t.paymentStatus)} /></div>
    </div>
  );
}

function RegisterModal({ e, onClose }: { e: Row; onClose: () => void }) {
  const [seats, setSeats] = useState('1');
  const [diet, setDiet] = useState('');
  const [method, setMethod] = useState('qris');
  const [checkout, setCheckout] = useState<Schemas['Payment'] | null>(null);
  const send = useSend<Row, Row>('POST', `/api/v1/member/events/${String(e.id)}/registrations`, INV);
  const paid = Number(e.fee) > 0;
  const t = send.data;
  return (
    <>
      <Modal open onClose={onClose} title={`Register · ${String(e.title)}`} actions={<>
        <button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button>
        {!t && <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => send.mutate({ partySize: Number(seats) || 1, dietaryNotes: diet || undefined,
          payMethod: paid ? method : undefined }, { onSuccess: (r) => { const co = r.checkout as Row | null; if (co?.online) setCheckout(co.online as Schemas['Payment']); } })}>Register</button>}
      </>}>
        {!t ? (
          <div className="oc-form">
            <TextField label="Seats" type="number" value={seats} onChange={setSeats} />
            <TextField label="Dietary notes" value={diet} onChange={setDiet} />
            {paid && <>
              <p>Fee {money(e.fee)} per seat · total {money(Number(e.fee) * (Number(seats) || 1))}</p>
              <SelectField label="Pay with" value={method} onChange={setMethod} options={[{ value: 'qris', label: 'QRIS' }, { value: 'virtual_account', label: 'Virtual Account' },
                { value: 'card', label: 'Card' }]} />
            </>}
          </div>
        ) : <Ticket t={t} />}
        <ErrorAlert error={send.error} />
      </Modal>
      <CheckoutModal checkout={checkout} onClose={() => setCheckout(null)} />
    </>
  );
}

function EventCard({ e }: { e: Row }) {
  const [open, setOpen] = useState(false);
  const mine = e.myTicket as Row | null;
  const full = e.seatsLeft !== null && e.seatsLeft !== undefined && Number(e.seatsLeft) <= 0;
  return (
    <Card title={String(e.title)} icon="celebration" actions={mine ? <StatusPill status={String(mine.status)} /> : undefined}>
      <div className="oc-small oc-muted">{String(e.eventType)} · {formatDateTime(String(e.start))}{list(e.venues).length ? ` · ${list(e.venues).join(', ')}` : ''}</div>
      {e.description ? <p>{String(e.description)}</p> : null}
      <div className="oc-row-wrap oc-small">
        <span>{Number(e.fee) > 0 ? `${money(e.fee)} / seat` : 'Free'}</span>
        {e.seatsLeft !== null && e.seatsLeft !== undefined && <span>{full ? 'Full — waitlist' : `${String(e.seatsLeft)} seats left`}</span>}
        {e.membersOnly ? <span>Members only</span> : null}
      </div>
      <div className="oc-row-wrap">
        <Link className="oc-btn oc-btn-neutral oc-btn-sm" to={`/events/${String(e.id)}`}>Details</Link>
        {!mine && e.registrationOpen ? <button className="oc-btn oc-btn-primary oc-btn-sm" onClick={() => setOpen(true)}>{full ? 'Join waitlist' : 'Register'}</button> : null}
      </div>
      {open && <RegisterModal e={e} onClose={() => setOpen(false)} />}
    </Card>
  );
}

export function UpcomingEventsPage() {
  const q = useGet<Page<Row>>('/api/v1/member/events');
  return (
    <div className="oc-stack">
      <PageHeader title="Upcoming Events" help="Club events open for registration." />
      <Link to="/events/my-events">My Events</Link>
      {q.isLoading ? <Skeleton /> : (q.data?.items ?? []).length === 0 ? <Empty title="No upcoming events" icon="celebration" />
        : <div className="oc-grid">{(q.data?.items ?? []).map((e) => <EventCard key={String(e.id)} e={e} />)}</div>}
    </div>
  );
}

export function EventPage() {
  const { id = '' } = useParams();
  const q = useGet<Row>(`/api/v1/member/events/${id}`);
  const [open, setOpen] = useState(false);
  if (q.isLoading) return <Skeleton />;
  if (!q.data) return <ErrorAlert error={q.error} />;
  const e = q.data;
  const mine = e.myTicket as Row | null;
  return (
    <div className="oc-stack">
      <PageHeader title={String(e.title)} help={`${String(e.eventType)} · ${formatDateTime(String(e.start))} – ${formatDateTime(String(e.end))}`} />
      <Card title="About" icon="info">
        {e.description ? <p>{String(e.description)}</p> : null}
        <p className="oc-small">{list(e.venues).join(', ')} · {Number(e.fee) > 0 ? `${money(e.fee)} / seat` : 'Free'}
          {e.seatsLeft !== null && e.seatsLeft !== undefined ? ` · ${String(e.seatsLeft)} seats left` : ''}</p>
        {!mine && e.registrationOpen ? <button className="oc-btn oc-btn-primary" onClick={() => setOpen(true)}>Register</button> : null}
      </Card>
      {mine && <Card title="My ticket" icon="qr_code_2"><Ticket t={mine} /></Card>}
      {open && <RegisterModal e={e} onClose={() => { setOpen(false); void q.refetch(); }} />}
    </div>
  );
}

export function MyEventsPage() {
  const q = useGet<Row>('/api/v1/member/my-events');
  const withdraw = useSend<Row, Row>('POST', (b) => `/api/v1/member/event-registrations/${String(b.code)}:withdraw`, INV);
  const pay = useSend<Row, Schemas['Payment']>('POST', (b) => `/api/v1/member/folios/${String(b.folioId)}:pay-online`, INV);
  const [checkout, setCheckout] = useState<Schemas['Payment'] | null>(null);
  const [show, setShow] = useState<Row | null>(null);
  if (q.isLoading) return <Skeleton />;
  const tickets = list(q.data?.tickets);
  const hosted = list(q.data?.hosted);
  return (
    <div className="oc-stack">
      <PageHeader title="My Events" />
      <ErrorAlert error={withdraw.error ?? pay.error} />
      {hosted.map((h) => {
        const sc = h.schedule as Row | null;
        return (
          <Card key={String(h.id)} title={`${String(h.title)} · ${String(h.number)}`} icon="favorite" actions={<StatusPill status={String(h.status)} />}>
            <div className="oc-small oc-muted">{formatDateTime(String(h.start))} · {list(h.venues).join(', ')} · {String(h.pax)} pax · {money(h.contractTotal)}</div>
            {sc && <DataTable rows={list(sc.lines)} columns={[{ key: 'label', header: 'Payment' }, { key: 'dueDate', header: 'Due', render: (l) => formatDate(String(l.dueDate)) },
              { key: 'amount', header: 'Amount', render: (l) => money(l.amount) }, { key: 'paidAmount', header: 'Paid', render: (l) => money(l.paidAmount) },
              { key: 'status', header: 'Status', render: (l) => <StatusPill status={String(l.status)} /> }]}
              actions={(l) => (h.folioId && ['pending', 'partially_paid', 'overdue'].includes(String(l.status)) ? (
                <button className="oc-btn oc-btn-primary oc-btn-sm" disabled={pay.isPending} onClick={() => pay.mutate({ folioId: h.folioId, method: 'qris',
                  amount: String(Number(l.amount) - Number(l.paidAmount)) }, { onSuccess: (c) => setCheckout(c) })}>Pay</button>) : null)} />}
          </Card>
        );
      })}
      <Card title="My tickets" icon="confirmation_number">
        {tickets.length === 0 ? <Empty title="No registrations" action={<Link to="/events">Upcoming Events</Link>} /> : (
          <DataTable rows={tickets} columns={[{ key: 'eventTitle', header: 'Event', render: (t) => <Link to={`/events/${String(t.eventId)}`}>{String(t.eventTitle)}</Link> },
            { key: 'eventStart', header: 'Date', render: (t) => formatDateTime(String(t.eventStart)) }, { key: 'partySize', header: 'Seats' },
            { key: 'status', header: 'Status', render: (t) => <StatusPill status={String(t.status)} /> }]}
            actions={(t) => <div className="oc-row-wrap">
              {t.status !== 'withdrawn' && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setShow(t)}>Ticket</button>}
              {['registered', 'waitlisted'].includes(String(t.status)) && <button className="oc-btn oc-btn-text oc-btn-sm" disabled={withdraw.isPending}
                onClick={() => withdraw.mutate({ code: t.ticketCode })}>Withdraw</button>}
            </div>} />
        )}
      </Card>
      {show && <Modal open onClose={() => setShow(null)} title={String(show.eventTitle)} actions={<button className="oc-btn oc-btn-ink" onClick={() => setShow(null)}>Close</button>}>
        <Ticket t={show} />
      </Modal>}
      <CheckoutModal checkout={checkout} onClose={() => setCheckout(null)} />
    </div>
  );
}

/** Member App routes of the area. */
export const BANQUET_MEMBER_ROUTES: { path: string; element: React.ReactNode }[] = [
  { path: 'events', element: <UpcomingEventsPage /> },
  { path: 'events/my-events', element: <MyEventsPage /> },
  { path: 'events/:id', element: <EventPage /> },
];
