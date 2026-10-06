import React, { useState } from 'react';
import { Link, useSearchParams } from 'react-router';
import { qs, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDateTime } from '@oneclub/i18n';
import {
  Card, Checkbox, CoursesPage, DataTable, Drawer, Empty, ErrorAlert, Icon, Modal, PageHeader, ResourcePage, SelectField, Skeleton, StatusPill,
  TextField, fieldErrors, statusCol, useAuth, useToast, type ResourceConfig,
} from '@oneclub/shell';
import { ActionButton, CourseDateBar, KV, ListPage, Tabs, money, today, useCourseDate, useStream, type R } from './common';
import { MemberNoTierBadge } from '../p5/tiers';

const pill = (k: string) => (r: R) => <StatusPill status={String(r[k] ?? '').replace(/_/g, '-')} />;
const SESSION: Record<string, string> = { morning: 'Morning', afternoon: 'Afternoon', night: 'Night' };

// ── Tee Sheet (FR-TEE-05..08, real time FR-OPS-04) ─────────────────────────

export function TeeSheetPage() {
  const cd = useCourseDate();
  const { can } = useAuth();
  const path = cd.courseId ? `/api/v1/golf/tee-sheet${qs({ courseId: cd.courseId, date: cd.date })}` : null;
  const sheet = useGet<{ slots: (R & { flights: R[] })[]; courseStatus: R }>(path);
  useStream(cd.courseId ? `/api/v1/golf/tee-sheet/stream${qs({ courseId: cd.courseId, date: cd.date })}` : null, () => sheet.refetch());
  const [booking, setBooking] = useState<R | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [session, setSession] = useState('');
  const slots = (sheet.data?.slots ?? []).filter((s) => !session || s.session === session);
  return (
    <div className="oc-stack">
      <PageHeader title="Tee Sheet" help="Live tee sheet; updates arrive in real time." actions={
        can('golf.booking.create') ? <Link className="oc-btn oc-btn-primary" to={`/golf/bookings/new${qs({ courseId: cd.courseId, date: cd.date })}`}>New Booking</Link> : null} />
      <div className="oc-row-wrap">
        <CourseDateBar cd={cd} />
        <Tabs tabs={[{ value: '', label: 'All sessions' }, { value: 'morning', label: 'Morning' }, { value: 'afternoon', label: 'Afternoon' }, { value: 'night', label: 'Night' }]}
          value={session} onChange={setSession} />
        {sheet.data && <span className="oc-muted">Course {String(sheet.data.courseStatus?.courseState)} · weather {String(sheet.data.courseStatus?.weather ?? '').replace(/_/g, ' ')}</span>}
      </div>
      {sheet.isLoading && <Skeleton rows={10} />}
      <ErrorAlert error={sheet.error} />
      {sheet.data && slots.length === 0 && <Empty title="No tee times" help="Generate the tee sheet from Golf Settings." icon="event_busy" />}
      {slots.length > 0 && (
        <div className="oc-table-wrap">
          <table className="oc-table">
            <thead><tr><th>Tee Time</th><th>Tee</th><th>Session</th><th>Status</th><th>Places</th><th>Flights</th><th aria-label="Actions" /></tr></thead>
            <tbody>
              {slots.map((s) => (
                <tr key={s.id}>
                  <td><strong>{String(s.localTime)}</strong>{s.peak ? <span className="oc-muted"> · peak</span> : null}</td>
                  <td>{String(s.startTee)}</td>
                  <td>{SESSION[String(s.session)]}</td>
                  <td><StatusPill status={String(s.status)} />{s.blockReason ? <div className="oc-muted">{String(s.blockReason)}</div> : null}</td>
                  <td>{String(s.remaining)} / {String(s.capacity)}</td>
                  <td>
                    {(s.flights ?? []).map((f) => (
                      <div key={String(f.id)} className="oc-row-wrap" style={{ marginBottom: 4 }}>
                        <StatusPill status={String(f.status).replace(/_/g, '-')} />
                        {f.bookingId ? <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => setOpen(String(f.bookingId))}>
                          {((f.players as R[]) ?? []).map((p) => String(p.name)).join(', ')}</button> : <span className="oc-muted">open</span>}
                        {(f.golfCarts as string[] | undefined)?.length ? <span className="oc-muted">· {(f.golfCarts as string[]).join(', ')}</span> : null}
                      </div>
                    ))}
                  </td>
                  <td className="oc-actions">
                    {Number(s.remaining) > 0 && s.status !== 'blocked' && can('golf.booking.create') && (
                      <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setBooking(s)}>Book</button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {booking && <BookingForm slot={booking} onClose={() => setBooking(null)} onDone={(b) => { setBooking(null); setOpen(String(b.id)); }} />}
      {open && <BookingDrawer id={open} onClose={() => setOpen(null)} />}
    </div>
  );
}

// ── Booking create (FR-BKG-01..07) ─────────────────────────────────────────

interface PlayerIn { playerType: string; memberNo: string; name: string; phone: string }

export function BookingForm({ slot, onClose, onDone }: { slot: R; onClose: () => void; onDone: (b: R) => void }) {
  const toast = useToast();
  const [bookingType, setType] = useState('member');
  const [players, setPlayers] = useState<PlayerIn[]>([{ playerType: 'member', memberNo: '', name: '', phone: '' }]);
  const [contactName, setContactName] = useState('');
  const [contactPhone, setContactPhone] = useState('');
  const [paymentMode, setPaymentMode] = useState('');
  const [carts, setCarts] = useState('');
  const [caddy, setCaddy] = useState('');
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/golf/bookings', ['/api/v1/golf']);
  const errs = fieldErrors(send.error);
  const setP = (i: number, k: keyof PlayerIn, v: string) => setPlayers((ps) => ps.map((p, j) => (j === i ? { ...p, [k]: v } : p)));
  const submit = () => send.mutate({
    bookingType, channel: 'back_office', teeTimeId: slot.id, contactName: contactName || undefined, contactPhone: contactPhone || undefined,
    paymentMode: paymentMode || undefined, golfCartRequest: carts ? Number(carts) : undefined, caddyRequest: caddy || undefined,
    players: players.map((p) => ({ playerType: p.playerType, memberNo: p.playerType === 'member' ? p.memberNo || undefined : undefined,
      name: p.name || undefined, phone: p.phone || undefined, tba: p.playerType !== 'member' && !p.name ? true : undefined,
      hostIndex: p.playerType === 'guest_of_member' ? 0 : undefined })),
  }, { onSuccess: (b) => { toast(`Booking ${b.code} created`); onDone(b); } });
  return (
    <Modal open onClose={onClose} wide title={`New Booking · ${String(slot.localTime)} tee ${String(slot.startTee)}`}
      actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
        <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={submit}>Create Booking</button></>}>
      <div className="oc-form">
        <SelectField label="Booking type" value={bookingType} onChange={setType} options={['member', 'guest', 'non_member', 'walk_in', 'corporate'].map((v) => ({ value: v, label: v.replace('_', ' ') }))} />
        <SelectField label="Payment" value={paymentMode} onChange={setPaymentMode} placeholder="Payment Policy default"
          options={[{ value: 'pay_at_venue', label: 'Pay at venue' }, { value: 'prepaid', label: 'Prepaid' }, { value: 'deposit', label: 'Deposit' }, { value: 'member_charge', label: 'Member charge' }]} />
        <TextField label="Contact name" value={contactName} onChange={setContactName} />
        <TextField label="Contact phone" value={contactPhone} onChange={setContactPhone} />
        <TextField label="Golf carts requested" type="number" min={0} value={carts} onChange={setCarts} help="Above the buggy sharing rule adds a surcharge" />
        <TextField label="Caddy request" value={caddy} onChange={setCaddy} help="Caddy number or name" />
      </div>
      <h3>Players ({players.length}/{String(slot.remaining)})</h3>
      {players.map((p, i) => (
        <div className="oc-row-wrap" key={i}>
          <SelectField label="Player type" value={p.playerType} onChange={(v) => setP(i, 'playerType', v)}
            options={[{ value: 'member', label: 'Member' }, { value: 'guest_of_member', label: 'Guest of Member' }, { value: 'non_member', label: 'Non-Member' }, { value: 'reciprocal', label: 'Reciprocal' }]} />
          {p.playerType === 'member'
            ? <><TextField label="Member No." value={p.memberNo} onChange={(v) => setP(i, 'memberNo', v)} error={errs[`players[${i}].memberNo`]} />
              <MemberNoTierBadge memberNo={p.memberNo} /></>
            : <TextField label="Name (empty = TBA)" value={p.name} onChange={(v) => setP(i, 'name', v)} error={errs[`players[${i}].name`]} />}
          {p.playerType !== 'member' && <TextField label="Phone" value={p.phone} onChange={(v) => setP(i, 'phone', v)} />}
          {players.length > 1 && <button className="oc-icon-btn" aria-label="Remove player" onClick={() => setPlayers((ps) => ps.filter((_, j) => j !== i))}><Icon name="close" size={18} /></button>}
        </div>
      ))}
      {players.length < Number(slot.remaining) && (
        <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => setPlayers((ps) => [...ps, { playerType: bookingType === 'member' ? 'guest_of_member' : 'non_member', memberNo: '', name: '', phone: '' }])}>
          Add player</button>
      )}
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

/** Booking create page: pick a slot then fill the form. */
export function BookingNewPage() {
  const cd = useCourseDate();
  const slots = useGet<Page<R>>(cd.courseId ? `/api/v1/golf/tee-times${qs({ courseId: cd.courseId, date: cd.date })}` : null);
  const [slot, setSlot] = useState<R | null>(null);
  const [done, setDone] = useState<string | null>(null);
  return (
    <div className="oc-stack">
      <PageHeader title="New Booking" help="Choose a tee time, then add the players." />
      <CourseDateBar cd={cd} />
      <DataTable rows={(slots.data?.items ?? []).filter((s) => Number(s.remaining) > 0 && s.status !== 'blocked')} loading={slots.isLoading} error={slots.error}
        columns={[{ key: 'localTime', header: 'Tee Time' }, { key: 'startTee', header: 'Tee' }, { key: 'session', header: 'Session' },
          { key: 'remaining', header: 'Places', align: 'right' }, { key: 'status', header: 'Status', render: pill('status') }]}
        onRowClick={setSlot} />
      {slot && <BookingForm slot={slot} onClose={() => setSlot(null)} onDone={(b) => { setSlot(null); setDone(String(b.id)); }} />}
      {done && <BookingDrawer id={done} onClose={() => setDone(null)} />}
    </div>
  );
}

// ── Booking detail (FR-BKG-08..12) ─────────────────────────────────────────

export function BookingDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const b = useGet<R & { players: R[]; flights: R[]; folio?: R }>(`/api/v1/golf/bookings/${id}`);
  const hist = useGet<Page<R>>(`/api/v1/golf/bookings/${id}/history`);
  const [moving, setMoving] = useState(false);
  const [adding, setAdding] = useState(false);
  const inv = ['/api/v1/golf'];
  const x = b.data;
  const active = x && ['pending', 'confirmed'].includes(String(x.status));
  return (
    <Drawer open onClose={onClose} title={x ? `Booking ${x.code}` : 'Booking'}>
      {b.isLoading && <Skeleton />}
      <ErrorAlert error={b.error} />
      {x && (
        <div className="oc-stack">
          <KV items={[
            ['Status', <StatusPill key="s" status={String(x.status).replace(/_/g, '-')} />], ['Tee time', `${String(x.playDate)} ${String(x.localTime)} · ${String(x.courseName)}`],
            ['Type', String(x.bookingType).replace('_', ' ')], ['Channel', String(x.channel).replace('_', ' ')], ['Booked by', String(x.contactName)],
            ['Payment', String(x.paymentMode ?? '—').replace(/_/g, ' ')], ['Charges', money(x.folio?.charges)], ['Balance', money(x.folio?.balance)],
          ]} />
          <div className="oc-row-wrap">
            {x.status === 'pending' && can('golf.booking.update') && <ActionButton label="Confirm" path={`/api/v1/golf/bookings/${id}:confirm`} invalidate={inv} kind="primary" />}
            {active && can('golf.booking.update') && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setMoving(true)}>Reschedule</button>}
            {active && can('golf.booking.update') && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setAdding(true)}>Add player</button>}
            {active && can('golf.booking.no_show') && <ActionButton label="No-show" path={`/api/v1/golf/bookings/${id}:no-show`} invalidate={inv} reason="optional" danger />}
            {active && can('golf.booking.cancel') && <ActionButton label="Cancel booking" path={`/api/v1/golf/bookings/${id}:cancel`} invalidate={inv} reason="required" danger
              confirm="Cancellation fees follow the Cancellation Policy." />}
            {x.folioId && can('billing.folio.view') ? <Link className="oc-btn oc-btn-sm oc-btn-text" to={`/billing/folios?id=${String(x.folioId)}`}>Folio</Link> : null}
          </div>
          <Card title="Players" icon="group">
            <DataTable rows={x.players} columns={[
              { key: 'name', header: 'Player', render: (p) => <>{String(p.name)}{p.tba ? <span className="oc-muted"> (TBA)</span> : null}</> },
              { key: 'playerType', header: 'Type', render: (p) => String(p.playerType).replace(/_/g, ' ') }, { key: 'segment', header: 'Segment' },
              { key: 'priceTotal', header: 'Price', align: 'right', render: (p) => money(p.priceTotal) }, { key: 'status', header: 'Status', render: pill('status') },
            ]} actions={(p) => (active && p.status === 'booked' && can('golf.booking.update') && x.players.length > 1
              ? <ActionButton label="Remove" method="DELETE" path={`/api/v1/golf/bookings/${id}/players/${p.id}`} invalidate={inv} danger confirm="Remove this player?" />
              : null)} />
          </Card>
          <Card title="History" icon="history">
            <DataTable rows={hist.data?.items} loading={hist.isLoading} columns={[
              { key: 'event', header: 'Event', render: (h) => String(h.event).replace(/_/g, ' ') }, { key: 'reason', header: 'Reason' }, { key: 'actorName', header: 'By' },
              { key: 'occurredAt', header: 'When', render: (h) => formatDateTime(String(h.occurredAt)) },
            ]} />
          </Card>
          {moving && <RescheduleModal booking={x} onClose={() => setMoving(false)} />}
          {adding && <AddPlayerModal bookingId={id} onClose={() => setAdding(false)} />}
        </div>
      )}
    </Drawer>
  );
}

function RescheduleModal({ booking, onClose }: { booking: R; onClose: () => void }) {
  const [date, setDate] = useState(String(booking.playDate));
  const [slot, setSlot] = useState('');
  const [reason, setReason] = useState('');
  const slots = useGet<Page<R>>(`/api/v1/golf/tee-times${qs({ courseId: String(booking.courseId), date })}`);
  const send = useSend<Record<string, unknown>>('POST', `/api/v1/golf/bookings/${booking.id}:reschedule`, ['/api/v1/golf']);
  return (
    <Modal open onClose={onClose} title="Reschedule" actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!slot || !reason || send.isPending} onClick={() => send.mutate({ teeTimeId: slot, reason }, { onSuccess: onClose })}>Reschedule</button></>}>
      <div className="oc-form">
        <TextField label="Date" type="date" value={date} onChange={setDate} />
        <SelectField label="Tee time" value={slot} onChange={setSlot} required
          options={(slots.data?.items ?? []).filter((s) => Number(s.remaining) >= Number(booking.playerCount) && s.id !== booking.teeTimeId)
            .map((s) => ({ value: s.id, label: `${String(s.localTime)} · tee ${String(s.startTee)} · ${String(s.remaining)} places` }))} />
        <TextField label="Reason" value={reason} onChange={setReason} required span />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function AddPlayerModal({ bookingId, onClose }: { bookingId: string; onClose: () => void }) {
  const [p, setP] = useState({ playerType: 'non_member', memberNo: '', name: '', phone: '' });
  const send = useSend<Record<string, unknown>>('POST', `/api/v1/golf/bookings/${bookingId}/players`, ['/api/v1/golf']);
  return (
    <Modal open onClose={onClose} title="Add player" actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => send.mutate({ player: { playerType: p.playerType,
        memberNo: p.playerType === 'member' ? p.memberNo : undefined, name: p.name || undefined, phone: p.phone || undefined, hostIndex: p.playerType === 'guest_of_member' ? 0 : undefined } },
      { onSuccess: onClose })}>Add</button></>}>
      <div className="oc-form">
        <SelectField label="Player type" value={p.playerType} onChange={(v) => setP({ ...p, playerType: v })}
          options={[{ value: 'member', label: 'Member' }, { value: 'guest_of_member', label: 'Guest of Member' }, { value: 'non_member', label: 'Non-Member' }]} />
        {p.playerType === 'member' ? <TextField label="Member No." value={p.memberNo} onChange={(v) => setP({ ...p, memberNo: v })} />
          : <><TextField label="Name" value={p.name} onChange={(v) => setP({ ...p, name: v })} /><TextField label="Phone" value={p.phone} onChange={(v) => setP({ ...p, phone: v })} /></>}
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

// ── Bookings list (Golf → Bookings, Booking → All Bookings / Cancellations) ─

const BOOKING_STATUSES = ['pending', 'confirmed', 'checked_in', 'completed', 'cancelled', 'no_show'].map((v) => ({ value: v, label: v.replace('_', ' ') }));

export function BookingsPage({ title = 'Bookings', preset, noDate }: { title?: string; preset?: Record<string, string>; noDate?: boolean }) {
  const cd = useCourseDate();
  const [open, setOpen] = useState<string | null>(null);
  const { can } = useAuth();
  return (
    <>
      <ListPage title={title} path="/api/v1/golf/bookings" statuses={preset?.['filter[status]'] ? undefined : BOOKING_STATUSES}
        extraQuery={{ ...(noDate ? {} : { date: cd.date }), ...(preset ?? {}) }} filters={noDate ? undefined : <CourseDateBar cd={cd} noCourse />}
        actions={can('golf.booking.create') ? <Link className="oc-btn oc-btn-primary" to="/golf/bookings/new">New Booking</Link> : undefined}
        onRowClick={(r) => setOpen(r.id)}
        columns={[{ key: 'code', header: 'Booking' }, { key: 'playDate', header: 'Date' }, { key: 'localTime', header: 'Tee Time' }, { key: 'contactName', header: 'Booked by' },
          { key: 'bookingType', header: 'Type', render: (r) => String(r.bookingType).replace('_', ' ') }, { key: 'playerCount', header: 'Players', align: 'right' },
          { key: 'channel', header: 'Channel', render: (r) => String(r.channel).replace('_', ' ') }, { key: 'status', header: 'Status', render: pill('status') }]} />
      {open && <BookingDrawer id={open} onClose={() => setOpen(null)} />}
    </>
  );
}

export function FlightsPage() {
  const cd = useCourseDate();
  return (
    <ListPage title="Flights" path="/api/v1/golf/flights" search={false} extraQuery={{ date: cd.date }} filters={<CourseDateBar cd={cd} noCourse />}
      statuses={['confirmed', 'checked_in', 'ready', 'on_hold', 'in_play', 'completed'].map((v) => ({ value: v, label: v.replace('_', ' ') }))}
      columns={[{ key: 'flightNo', header: 'Flight' }, { key: 'bookingStatus', header: 'Booking', render: pill('bookingStatus') },
        { key: 'players', header: 'Players', render: (f) => ((f.players as R[]) ?? []).map((p) => String(p.name)).join(', ') },
        { key: 'readiness', header: 'Ready', render: (f) => { const rd = f.readiness as R; return `${String(rd?.checkedIn)}/${String(rd?.players)} checked-in${rd?.caddiesOk ? '' : ' · caddy'}${rd?.golfCartsOk ? '' : ' · golf cart'}`; } },
        { key: 'golfCarts', header: 'Golf Carts', render: (f) => ((f.golfCarts as string[]) ?? []).join(', ') }, { key: 'status', header: 'Status', render: pill('status') }]} />
  );
}

/** Today's Players (FR-RPT-03: same definition as the Daily Tee Sheet Report). */
export function PlayersPage() {
  const cd = useCourseDate();
  return (
    <ListPage title="Players" help="Players of active bookings (Today's Players)." path="/api/v1/golf/players" extraQuery={{ date: cd.date }}
      filters={<CourseDateBar cd={cd} noCourse />}
      columns={[{ key: 'localTime', header: 'Tee Time' }, { key: 'name', header: 'Player' }, { key: 'bookingCode', header: 'Booking' },
        { key: 'playerType', header: 'Type', render: (r) => String(r.playerType).replace(/_/g, ' ') }, { key: 'segment', header: 'Segment' },
        { key: 'priceTotal', header: 'Price', align: 'right', render: (r) => money(r.priceTotal) }, { key: 'status', header: 'Status', render: pill('status') }]} />
  );
}

// ── Check-in (FR-CHK-01..03) ───────────────────────────────────────────────

export function CheckInPage() {
  const toast = useToast();
  const [method, setMethod] = useState('booking_code');
  const [value, setValue] = useState('');
  const [date, setDate] = useState(today());
  const [search, setSearch] = useState<string | null>(null);
  const cands = useGet<Page<R>>(search);
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/golf/check-ins', ['/api/v1/golf']);
  const go = () => setSearch(`/api/v1/golf/check-ins:lookup${qs({ method, value, date })}`);
  return (
    <div className="oc-stack">
      <PageHeader title="Check-in" help="Scan a member card or booking QR, or search by booking code or name." />
      <div className="oc-row-wrap">
        <SelectField label="Method" value={method} onChange={setMethod} options={[{ value: 'booking_code', label: 'Booking code' }, { value: 'booking_qr', label: 'Booking QR' },
          { value: 'member_card', label: 'Member card' }, { value: 'name', label: 'Name' }]} />
        <TextField label="Value" value={value} onChange={setValue} onKeyDown={(e) => e.key === 'Enter' && go()} autoFocus />
        <TextField label="Date" type="date" value={date} onChange={setDate} />
        <button className="oc-btn oc-btn-primary" onClick={go} disabled={!value}>Find</button>
      </div>
      <ErrorAlert error={cands.error ?? send.error} />
      <DataTable rows={cands.data?.items} loading={cands.isLoading && !!search} rowKey={(c) => String(c.bookingId)}
        columns={[{ key: 'code', header: 'Booking' }, { key: 'localTime', header: 'Tee Time' }, { key: 'contactName', header: 'Booked by' },
          { key: 'players', header: 'Players', render: (c) => ((c.players as R[]) ?? []).map((p) => `${String(p.name)}${p.status === 'checked_in' ? ' ✓' : ''}`).join(', ') },
          { key: 'paymentDue', header: 'To pay', align: 'right', render: (c) => (Number(c.paymentDue) > 0 ? <strong>{money(c.paymentDue)}</strong> : '—') },
          { key: 'status', header: 'Status', render: pill('status') }]}
        actions={(c) => (
          <button className="oc-btn oc-btn-sm oc-btn-primary" disabled={send.isPending}
            onClick={() => send.mutate({ method, value, bookingId: c.bookingId, date, playerIds: (c.matchedPlayerIds as string[])?.length ? c.matchedPlayerIds : undefined },
              { onSuccess: (r) => { toast(`Checked in ${((r.checkedIn as string[]) ?? []).length} player(s)`); setSearch(`${search}&t=${Date.now()}`); } })}>Check-in</button>
        )} />
    </div>
  );
}

// ── Starter (FR-CHK-07..10) ────────────────────────────────────────────────

export function StarterPage() {
  const cd = useCourseDate();
  const { can } = useAuth();
  const path = cd.courseId ? `/api/v1/golf/starter-queue${qs({ courseId: cd.courseId, date: cd.date })}` : null;
  const q = useGet<{ active: R[]; onHold: R[]; dispatched: R[]; courseStatus: R }>(path);
  useStream(cd.courseId ? `/api/v1/golf/tee-sheet/stream${qs({ courseId: cd.courseId, date: cd.date })}` : null, () => q.refetch());
  const inv = ['/api/v1/golf'];
  const ctl = can('golf.starter.control');
  const sq = (f: R, a: string) => `/api/v1/golf/starter-queue/${String(f.flightId)}:${a}`;
  const players = (f: R) => ((f.players as R[]) ?? []).map((p) => String(p.name)).join(', ');
  return (
    <div className="oc-stack">
      <PageHeader title="Starter" help="Active Dispatch Queue; held flights keep their position." />
      <CourseDateBar cd={cd} />
      {q.data && <CourseStatusCard courseId={cd.courseId} status={q.data.courseStatus} />}
      <ErrorAlert error={q.error} />
      <Card title={`Queue (${q.data?.active.length ?? 0})`} icon="format_list_numbered">
        <DataTable rows={q.data?.active} loading={q.isLoading} rowKey={(f) => String(f.flightId)}
          columns={[{ key: 'position', header: '#' }, { key: 'localTime', header: 'Tee Time' }, { key: 'startTee', header: 'Tee' }, { key: 'bookingCode', header: 'Booking' },
            { key: 'players', header: 'Players', render: players }, { key: 'golfCarts', header: 'Golf Carts', render: (f) => ((f.golfCarts as string[]) ?? []).join(', ') },
            { key: 'waitMinutes', header: 'Waiting', render: (f) => `${String(f.waitMinutes)} min` }]}
          actions={(f) => ctl && (<div className="oc-row">
            <ActionButton label="Call" path={sq(f, 'call')} invalidate={inv} kind="text" />
            <ActionButton label="Skip" path={sq(f, 'skip')} invalidate={inv} kind="text" />
            <ActionButton label="Hold" path={sq(f, 'hold')} invalidate={inv} reason="required" />
            <ActionButton label="Tee-Off" path={sq(f, 'tee-off')} invalidate={inv} kind="primary" />
          </div>)} />
      </Card>
      <Card title={`On Hold (${q.data?.onHold.length ?? 0})`} icon="pause_circle">
        <DataTable rows={q.data?.onHold} rowKey={(f) => String(f.flightId)}
          columns={[{ key: 'localTime', header: 'Tee Time' }, { key: 'bookingCode', header: 'Booking' }, { key: 'players', header: 'Players', render: players }, { key: 'holdReason', header: 'Reason' }]}
          actions={(f) => ctl && <ActionButton label="Release" path={sq(f, 'release')} invalidate={inv} kind="primary" />} />
      </Card>
      <Card title={`In Play (${q.data?.dispatched.length ?? 0})`} icon="sports_golf">
        <DataTable rows={q.data?.dispatched} rowKey={(f) => String(f.flightId)}
          columns={[{ key: 'localTime', header: 'Tee Time' }, { key: 'bookingCode', header: 'Booking' }, { key: 'players', header: 'Players', render: players },
            { key: 'dispatchedAt', header: 'Tee-Off', render: (f) => formatDateTime(String(f.dispatchedAt)) }]}
          actions={(f) => ctl && <FinishButton flightId={String(f.flightId)} />} />
      </Card>
    </div>
  );
}

function FinishButton({ flightId }: { flightId: string }) {
  const [open, setOpen] = useState(false);
  const [holes, setHoles] = useState('18');
  const [rain, setRain] = useState(false);
  const finish = useSend<Record<string, unknown>>('POST', `/api/v1/golf/starter-queue/${flightId}:finish`, ['/api/v1/golf']);
  const rc = useSend<Record<string, unknown>>('POST', '/api/v1/golf/rain-checks', ['/api/v1/golf']);
  const busy = finish.isPending || rc.isPending;
  const go = () => (rain ? rc.mutate({ flightId, holesPlayed: Number(holes) }, { onSuccess: () => setOpen(false) })
    : finish.mutate({ holesPlayed: Number(holes) }, { onSuccess: () => setOpen(false) }));
  return (
    <>
      <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setOpen(true)}>Round Finish</button>
      <Modal open={open} onClose={() => setOpen(false)} title="Round Finish" actions={<><button className="oc-btn oc-btn-neutral" onClick={() => setOpen(false)}>Cancel</button>
        <button className="oc-btn oc-btn-primary" disabled={busy} onClick={go}>{rain ? 'Issue Rain Checks' : 'Finish'}</button></>}>
        <div className="oc-form">
          <TextField label="Holes played" type="number" min={0} max={36} value={holes} onChange={setHoles} />
          <Checkbox label="Stopped by weather (issue rain checks per the Weather Policy)" checked={rain} onChange={setRain} />
        </div>
        <ErrorAlert error={finish.error ?? rc.error} />
      </Modal>
    </>
  );
}

export function CourseStatusCard({ courseId, status }: { courseId: string; status: R }) {
  const { can } = useAuth();
  const [v, setV] = useState({ courseState: String(status?.courseState ?? 'open'), weather: String(status?.weather ?? 'normal'), lighting: String(status?.lighting ?? 'off'), notes: '' });
  const send = useSend<Record<string, unknown>>('PUT', `/api/v1/golf/course-status/${courseId}`, ['/api/v1/golf']);
  return (
    <Card title="Course Status" icon="partly_cloudy_day">
      <div className="oc-row-wrap">
        <SelectField label="Course" value={v.courseState} onChange={(x) => setV({ ...v, courseState: x })} options={[{ value: 'open', label: 'Open' }, { value: 'closed', label: 'Closed' }]} />
        <SelectField label="Weather" value={v.weather} onChange={(x) => setV({ ...v, weather: x })}
          options={['normal', 'rain', 'lightning_warning', 'rain_stop', 'heat_warning'].map((w) => ({ value: w, label: w.replace('_', ' ') }))} />
        <SelectField label="Lighting" value={v.lighting} onChange={(x) => setV({ ...v, lighting: x })} options={[{ value: 'off', label: 'Off' }, { value: 'on', label: 'On' }]} />
        <TextField label="Notes" value={v.notes} onChange={(x) => setV({ ...v, notes: x })} />
        {can('golf.course_status.update') && <button className="oc-btn oc-btn-ink" disabled={send.isPending} onClick={() => send.mutate({ ...v, notes: v.notes || undefined })}>Update</button>}
      </div>
      <ErrorAlert error={send.error} />
    </Card>
  );
}

// ── Caddies (EP-09) ─────────────────────────────────────────────────────────

const caddyCfg: ResourceConfig = {
  title: 'Caddy Master', singular: 'Caddy', path: '/api/v1/golf/caddies', perm: 'golf.caddy',
  columns: [{ key: 'code', header: 'Caddy No.' }, { key: 'name', header: 'Name' }, { key: 'gender', header: 'Gender' }, { key: 'partnershipStatus', header: 'Partnership' }, statusCol],
  fields: [{ name: 'code', label: 'Caddy No.', required: true }, { name: 'name', label: 'Name', required: true },
    { name: 'gender', label: 'Gender', type: 'select', options: [{ value: 'female', label: 'Female' }, { value: 'male', label: 'Male' }] },
    { name: 'phone', label: 'Phone' }, { name: 'partnershipStatus', label: 'Partnership', type: 'select', default: 'partner',
      options: [{ value: 'partner', label: 'Partner' }, { value: 'trainee', label: 'Trainee' }, { value: 'employee', label: 'Employee' }] },
    { name: 'status', label: 'Status', type: 'select', default: 'active', options: [{ value: 'active', label: 'Active' }, { value: 'inactive', label: 'Inactive' }] }],
};

export function CaddiesPage() {
  const [tab, setTab] = useState('board');
  return (
    <div className="oc-stack">
      <Tabs tabs={[{ value: 'board', label: 'Caddy Queue' }, { value: 'assignments', label: 'Caddy Assignment' }, { value: 'master', label: 'Caddy Master' }, { value: 'tips', label: 'Tips' }]}
        value={tab} onChange={setTab} />
      {tab === 'board' && <CaddyBoard />}
      {tab === 'assignments' && <CaddyAssignments />}
      {tab === 'master' && <ResourcePage cfg={caddyCfg} />}
      {tab === 'tips' && <CaddyTips />}
    </div>
  );
}

export function CaddyBoard() {
  const cd = useCourseDate();
  const { can } = useAuth();
  const board = useGet<Page<R>>(`/api/v1/golf/caddy-availability?date=${cd.date}`);
  const att = useSend<Record<string, unknown>>('PUT', '/api/v1/golf/caddy-availability', ['/api/v1/golf/caddy']);
  const reorder = useSend<Record<string, unknown>>('POST', '/api/v1/golf/caddy-queue:reorder', ['/api/v1/golf/caddy']);
  const rows = board.data?.items ?? [];
  const present = rows.filter((c) => c.attendance === 'present');
  const manage = can('golf.caddy_assignment.manage');
  const mark = (c: R, status: string) => att.mutate({ date: cd.date, entries: [{ caddyId: c.caddyId, status }] });
  const move = (c: R, dir: -1 | 1) => {
    const ids = present.map((p) => String(p.caddyId));
    const i = ids.indexOf(String(c.caddyId));
    const j = i + dir;
    if (j < 0 || j >= ids.length) return;
    [ids[i], ids[j]] = [ids[j], ids[i]];
    reorder.mutate({ date: cd.date, caddyIds: ids });
  };
  return (
    <div className="oc-stack">
      <PageHeader title="Caddy Queue" help="Attendance and rotation of the day (first available caddy is assigned first)." actions={manage ? (
        <button className="oc-btn oc-btn-neutral" onClick={() => att.mutate({ date: cd.date, entries: rows.filter((c) => !c.attendance).map((c) => ({ caddyId: c.caddyId, status: 'present' })) })}>
          Mark all present</button>) : undefined} />
      <CourseDateBar cd={cd} noCourse />
      <ErrorAlert error={att.error ?? reorder.error} />
      <DataTable rows={rows} loading={board.isLoading} rowKey={(c) => String(c.caddyId)}
        columns={[{ key: 'queueNo', header: '#', render: (c) => (c.attendance === 'present' ? present.indexOf(c) + 1 : '—') }, { key: 'code', header: 'Caddy No.' }, { key: 'name', header: 'Name' },
          { key: 'attendance', header: 'Attendance', render: pill('attendance') }, { key: 'status', header: 'Status', render: pill('status') },
          { key: 'teeTime', header: 'Tee Time' }, { key: 'roundsToday', header: 'Rounds', align: 'right' }]}
        actions={(c) => manage && (<div className="oc-row">
          {c.attendance !== 'present' && <button className="oc-btn oc-btn-sm oc-btn-text" onClick={() => mark(c, 'present')}>Present</button>}
          {c.attendance === 'present' && c.status === 'available' && <>
            <button className="oc-icon-btn" aria-label="Move up" onClick={() => move(c, -1)}><Icon name="arrow_upward" size={18} /></button>
            <button className="oc-icon-btn" aria-label="Move down" onClick={() => move(c, 1)}><Icon name="arrow_downward" size={18} /></button>
            <button className="oc-btn oc-btn-sm oc-btn-text" onClick={() => mark(c, 'absent')}>Absent</button></>}
        </div>)} />
    </div>
  );
}

export function CaddyAssignments() {
  const cd = useCourseDate();
  const { can } = useAuth();
  const list = useGet<Page<R>>(`/api/v1/golf/caddy-assignments?date=${cd.date}`);
  const flights = useGet<Page<R>>(`/api/v1/golf/flights?date=${cd.date}`);
  const auto = useSend<Record<string, unknown>>('POST', '/api/v1/golf/caddy-assignments', ['/api/v1/golf']);
  const inv = ['/api/v1/golf'];
  const open = (flights.data?.items ?? []).filter((f) => !['completed', 'cancelled'].includes(String(f.status)) && !(f.readiness as R)?.caddiesOk);
  return (
    <div className="oc-stack">
      <PageHeader title="Caddy Assignment" />
      <CourseDateBar cd={cd} noCourse />
      <ErrorAlert error={auto.error} />
      {can('golf.caddy_assignment.manage') && open.length > 0 && (
        <Card title="Flights without caddies" icon="person_add">
          <DataTable rows={open} columns={[{ key: 'flightNo', header: 'Flight' }, { key: 'players', header: 'Players', render: (f) => ((f.players as R[]) ?? []).map((p) => String(p.name)).join(', ') }]}
            actions={(f) => <button className="oc-btn oc-btn-sm oc-btn-primary" disabled={auto.isPending} onClick={() => auto.mutate({ flightId: f.id, auto: true })}>Assign from queue</button>} />
        </Card>
      )}
      <DataTable rows={list.data?.items} loading={list.isLoading}
        columns={[{ key: 'teeTime', header: 'Tee Time' }, { key: 'caddyCode', header: 'Caddy No.' }, { key: 'caddyName', header: 'Caddy' }, { key: 'bookingCode', header: 'Booking' },
          { key: 'playerNames', header: 'Players', render: (a) => ((a.playerNames as string[]) ?? []).join(', ') }, { key: 'feeAmount', header: 'Caddy Fee', align: 'right', render: (a) => money(a.feeAmount) },
          { key: 'status', header: 'Status', render: pill('status') }]}
        actions={(a) => can('golf.caddy_assignment.manage') && ['assigned', 'in_play'].includes(String(a.status)) && (
          <ActionButton label="Cancel" path={`/api/v1/golf/caddy-assignments/${a.id}:cancel`} invalidate={inv} reason="optional" danger />)} />
    </div>
  );
}

function CaddyTips() {
  const cd = useCourseDate();
  const tips = useGet<Page<R>>(`/api/v1/golf/caddy-tips?date=${cd.date}`);
  return (
    <div className="oc-stack">
      <PageHeader title="Caddy Tips" help="Tips recorded per caddy for the settlement (P2)." />
      <CourseDateBar cd={cd} noCourse />
      <DataTable rows={tips.data?.items} loading={tips.isLoading} columns={[{ key: 'caddyName', header: 'Caddy' }, { key: 'method', header: 'Method' },
        { key: 'amount', header: 'Amount', align: 'right', render: (t) => money(t.amount) }, { key: 'tipDate', header: 'Date' }]} />
    </div>
  );
}

// ── Golf carts (EP-10) ──────────────────────────────────────────────────────

const cartCfg: ResourceConfig = {
  title: 'Golf Cart Master', singular: 'Golf Cart', path: '/api/v1/golf/golf-carts', perm: 'golf.golf_cart',
  columns: [{ key: 'code', header: 'Golf Cart No.' }, { key: 'name', header: 'Name' }, { key: 'cartType', header: 'Type' }, { key: 'capacity', header: 'Seats', align: 'right' }, statusCol],
  fields: [{ name: 'code', label: 'Golf Cart No.', required: true }, { name: 'name', label: 'Name', required: true },
    { name: 'cartType', label: 'Type', type: 'select', default: 'electric', options: [{ value: 'electric', label: 'Electric' }, { value: 'gasoline', label: 'Gasoline' }, { value: 'other', label: 'Other' }] },
    { name: 'capacity', label: 'Seats', type: 'number', default: 2 },
    { name: 'status', label: 'Status', type: 'select', default: 'active', options: [{ value: 'active', label: 'Active' }, { value: 'inactive', label: 'Inactive' }] }],
};

export function GolfCartsPage() {
  const [tab, setTab] = useState('board');
  return (
    <div className="oc-stack">
      <Tabs tabs={[{ value: 'board', label: 'Golf Cart Readiness' }, { value: 'assignments', label: 'Golf Cart Assignment' }, { value: 'master', label: 'Golf Cart Master' }]} value={tab} onChange={setTab} />
      {tab === 'board' && <CartBoard />}
      {tab === 'assignments' && <CartAssignments />}
      {tab === 'master' && <ResourcePage cfg={cartCfg} />}
    </div>
  );
}

export function CartBoard() {
  const { can } = useAuth();
  const board = useGet<Page<R>>(`/api/v1/golf/golf-cart-board?date=${today()}`);
  const [edit, setEdit] = useState<R | null>(null);
  return (
    <div className="oc-stack">
      <PageHeader title="Golf Cart Readiness" help="Ready carts are assigned first; returned carts go to Charging per the Golf Cart Policy." />
      <DataTable rows={board.data?.items} loading={board.isLoading}
        columns={[{ key: 'code', header: 'Golf Cart No.' }, { key: 'name', header: 'Name' }, { key: 'readiness', header: 'Readiness', render: pill('readiness') },
          { key: 'readinessReason', header: 'Reason' }, { key: 'roundsToday', header: 'Rounds', align: 'right' }]}
        actions={(c) => can('golf.golf_cart.update') && c.readiness !== 'in_use' && <button className="oc-btn oc-btn-sm oc-btn-text" onClick={() => setEdit(c)}>Set readiness</button>} />
      {edit && <ReadinessModal cart={edit} onClose={() => setEdit(null)} />}
    </div>
  );
}

function ReadinessModal({ cart, onClose }: { cart: R; onClose: () => void }) {
  const [readiness, setReadiness] = useState(String(cart.readiness));
  const [reason, setReason] = useState('');
  const send = useSend<Record<string, unknown>>('POST', `/api/v1/golf/golf-carts/${cart.id}:set-readiness`, ['/api/v1/golf']);
  return (
    <Modal open onClose={onClose} title={`Golf Cart ${String(cart.code)}`} actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => send.mutate({ readiness, reason: reason || undefined }, { onSuccess: onClose })}>Save</button></>}>
      <div className="oc-form">
        <SelectField label="Readiness" value={readiness} onChange={setReadiness}
          options={['ready', 'not_ready', 'charging', 'maintenance', 'out_of_service'].map((v) => ({ value: v, label: v.replace(/_/g, ' ') }))} />
        <TextField label="Reason" value={reason} onChange={setReason} />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

export function CartAssignments() {
  const cd = useCourseDate();
  const { can } = useAuth();
  const list = useGet<Page<R>>(`/api/v1/golf/golf-cart-assignments?date=${cd.date}`);
  const flights = useGet<Page<R>>(`/api/v1/golf/flights?date=${cd.date}`);
  const auto = useSend<Record<string, unknown>>('POST', '/api/v1/golf/golf-cart-assignments', ['/api/v1/golf']);
  const open = (flights.data?.items ?? []).filter((f) => !['completed', 'cancelled'].includes(String(f.status)) && !(f.readiness as R)?.golfCartsOk);
  return (
    <div className="oc-stack">
      <PageHeader title="Golf Cart Assignment" help="Buggy sharing: the number of carts follows the Golf Cart Policy." />
      <CourseDateBar cd={cd} noCourse />
      <ErrorAlert error={auto.error} />
      {can('golf.golf_cart_assignment.manage') && open.length > 0 && (
        <Card title="Flights without golf carts" icon="electric_car">
          <DataTable rows={open} columns={[{ key: 'flightNo', header: 'Flight' }, { key: 'golfCartsNeeded', header: 'Needed', align: 'right' },
            { key: 'players', header: 'Players', render: (f) => ((f.players as R[]) ?? []).map((p) => String(p.name)).join(', ') }]}
            actions={(f) => <button className="oc-btn oc-btn-sm oc-btn-primary" disabled={auto.isPending} onClick={() => auto.mutate({ flightId: f.id, auto: true })}>Assign Ready carts</button>} />
        </Card>
      )}
      <DataTable rows={list.data?.items} loading={list.isLoading}
        columns={[{ key: 'golfCartCode', header: 'Golf Cart' }, { key: 'bookingCode', header: 'Booking' }, { key: 'extra', header: 'Surcharge', render: (a) => (a.extra ? 'Yes' : '—') },
          { key: 'outAt', header: 'Out', render: (a) => (a.outAt ? formatDateTime(String(a.outAt)) : '—') }, { key: 'status', header: 'Status', render: pill('status') }]}
        actions={(a) => can('golf.golf_cart_assignment.manage') && ['assigned', 'in_use'].includes(String(a.status)) && (
          <ActionButton label="Return" path={`/api/v1/golf/golf-cart-assignments/${a.id}:return`} invalidate={['/api/v1/golf']} />)} />
    </div>
  );
}

// ── Course (EP-02) ──────────────────────────────────────────────────────────

const courseRef = { path: '/api/v1/golf/courses', label: (r: R) => `${String(r.name)} (${String(r.code)})` };
const st = { name: 'status', label: 'Status', type: 'select' as const, default: 'active', options: [{ value: 'active', label: 'Active' }, { value: 'inactive', label: 'Inactive' }] };

const COURSE_TABS: Record<string, ResourceConfig> = {
  sections: { title: 'Course Sections', singular: 'Course Section', path: '/api/v1/golf/course-sections', perm: 'golf.course',
    columns: [{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'sequence', header: 'Order', align: 'right' }, statusCol],
    fields: [{ name: 'courseId', label: 'Course', type: 'reference', required: true, ref: courseRef }, { name: 'code', label: 'Code', required: true },
      { name: 'name', label: 'Name', required: true }, { name: 'sequence', label: 'Order', type: 'number', default: 1 }, st] },
  holes: { title: 'Holes', singular: 'Hole', path: '/api/v1/golf/holes', perm: 'golf.course',
    columns: [{ key: 'number', header: 'Hole', align: 'right' }, { key: 'par', header: 'Par', align: 'right' }, { key: 'strokeIndex', header: 'Stroke Index', align: 'right' },
      { key: 'distances', header: 'Distance (m)', render: (r) => Object.entries((r.distances as Record<string, number>) ?? {}).map(([k, v]) => `${k} ${v}`).join(' · ') }, statusCol],
    fields: [{ name: 'courseId', label: 'Course', type: 'reference', required: true, ref: courseRef },
      { name: 'sectionId', label: 'Course Section', type: 'reference', required: true, ref: { path: '/api/v1/golf/course-sections', label: (r: R) => String(r.name) } },
      { name: 'code', label: 'Code', required: true }, { name: 'number', label: 'Hole number', type: 'number', required: true },
      { name: 'par', label: 'Par', type: 'number', required: true, default: 4 }, { name: 'strokeIndex', label: 'Stroke Index', type: 'number' },
      { name: 'description', label: 'Hole-by-Hole description', type: 'textarea', span: true }, st] },
  teeSets: { title: 'Tee Sets', singular: 'Tee Set', path: '/api/v1/golf/tee-sets', perm: 'golf.course',
    columns: [{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'courseRating', header: 'Course Rating', align: 'right' }, { key: 'slope', header: 'Slope', align: 'right' }, statusCol],
    fields: [{ name: 'courseId', label: 'Course', type: 'reference', required: true, ref: courseRef }, { name: 'code', label: 'Code', required: true },
      { name: 'name', label: 'Name', required: true }, { name: 'color', label: 'Colour' }, { name: 'courseRating', label: 'Course Rating', type: 'decimal' },
      { name: 'slope', label: 'Slope', type: 'number' }, { name: 'gender', label: 'Gender', type: 'select', options: [{ value: 'any', label: 'Any' }, { value: 'male', label: 'Male' }, { value: 'female', label: 'Female' }] }, st] },
  routes: { title: 'Playing Routes', singular: 'Playing Route', path: '/api/v1/golf/playing-routes', perm: 'golf.course',
    columns: [{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'sectionCodes', header: 'Sections' }, { key: 'holeCount', header: 'Holes', align: 'right' },
      { key: 'isDefault', header: 'Default', render: (r) => (r.isDefault ? 'Yes' : '—') }, statusCol],
    fields: [{ name: 'courseId', label: 'Course', type: 'reference', required: true, ref: courseRef }, { name: 'code', label: 'Code', required: true },
      { name: 'name', label: 'Name', required: true }, { name: 'sectionCodes', label: 'Sections in order', required: true, help: 'e.g. FRONT,BACK' },
      { name: 'isDefault', label: 'Default route', type: 'boolean' }, st] },
  assets: { title: 'Course Assets', singular: 'Course Asset', path: '/api/v1/golf/course-assets', perm: 'golf.course',
    columns: [{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'assetType', header: 'Type' }, statusCol],
    fields: [{ name: 'courseId', label: 'Course', type: 'reference', required: true, ref: courseRef }, { name: 'code', label: 'Code', required: true },
      { name: 'name', label: 'Name', required: true }, { name: 'assetType', label: 'Type', type: 'select', required: true,
        options: ['course_map', 'panorama', 'hazard', 'point_of_interest', 'green_front', 'green_center', 'green_back', 'distance_marker'].map((v) => ({ value: v, label: v.replace(/_/g, ' ') })) },
      { name: 'holeId', label: 'Hole', type: 'reference', ref: { path: '/api/v1/golf/holes', label: (r: R) => `Hole ${String(r.number)}` } }, st] },
};

export function CoursePage() {
  const [tab, setTab] = useState('courses');
  return (
    <div className="oc-stack">
      <Tabs tabs={[{ value: 'courses', label: 'Courses' }, { value: 'sections', label: 'Course Sections' }, { value: 'holes', label: 'Holes' }, { value: 'teeSets', label: 'Tee Sets' },
        { value: 'routes', label: 'Playing Routes' }, { value: 'assets', label: 'Course Assets' }, { value: 'blocks', label: 'Course Blocks' }, { value: 'handicaps', label: 'Handicap Index' }]}
        value={tab} onChange={setTab} />
      {tab === 'courses' && <CoursesPage />}
      {COURSE_TABS[tab] && <ResourcePage key={tab} cfg={COURSE_TABS[tab]} />}
      {tab === 'blocks' && <CourseBlocks />}
      {tab === 'handicaps' && <Handicaps />}
    </div>
  );
}

function CourseBlocks() {
  const cd = useCourseDate();
  const { can } = useAuth();
  const list = useGet<Page<R>>(cd.courseId ? `/api/v1/golf/course-blocks?courseId=${cd.courseId}` : null);
  const [v, setV] = useState({ startsAt: '', endsAt: '', reason: 'maintenance', notes: '' });
  const send = useSend<Record<string, unknown>>('POST', '/api/v1/golf/course-blocks', ['/api/v1/golf']);
  return (
    <div className="oc-stack">
      <PageHeader title="Course Blocks" help="Blocked periods make tee times unbookable; existing bookings are listed for follow-up." />
      <CourseDateBar cd={cd} />
      {can('golf.tee_sheet.manage') && (
        <Card title="Block the course" icon="block">
          <div className="oc-row-wrap">
            <TextField label="From" type="datetime-local" value={v.startsAt} onChange={(x) => setV({ ...v, startsAt: x })} />
            <TextField label="To" type="datetime-local" value={v.endsAt} onChange={(x) => setV({ ...v, endsAt: x })} />
            <SelectField label="Reason" value={v.reason} onChange={(x) => setV({ ...v, reason: x })}
              options={['maintenance', 'tournament', 'private_event', 'weather_closure', 'management_hold'].map((r) => ({ value: r, label: r.replace('_', ' ') }))} />
            <TextField label="Notes" value={v.notes} onChange={(x) => setV({ ...v, notes: x })} />
            <button className="oc-btn oc-btn-ink" disabled={!v.startsAt || !v.endsAt || send.isPending}
              onClick={() => send.mutate({ courseId: cd.courseId, startsAt: new Date(v.startsAt).toISOString(), endsAt: new Date(v.endsAt).toISOString(), reason: v.reason, notes: v.notes || undefined })}>Block</button>
          </div>
          <ErrorAlert error={send.error} />
        </Card>
      )}
      <DataTable rows={list.data?.items} loading={list.isLoading}
        columns={[{ key: 'startsAt', header: 'From', render: (b) => formatDateTime(String(b.startsAt)) }, { key: 'endsAt', header: 'To', render: (b) => formatDateTime(String(b.endsAt)) },
          { key: 'reason', header: 'Reason', render: (b) => String(b.reason).replace('_', ' ') }, { key: 'blockedSlots', header: 'Slots', align: 'right' },
          { key: 'affectedBookings', header: 'Affected bookings', render: (b) => ((b.affectedBookings as R[]) ?? []).map((a) => String(a.code)).join(', ') || '—' }, statusCol]}
        actions={(b) => can('golf.tee_sheet.manage') && b.status === 'active' && <ActionButton label="Cancel block" path={`/api/v1/golf/course-blocks/${b.id}:cancel`} invalidate={['/api/v1/golf']} />} />
    </div>
  );
}

function Handicaps() {
  const { can } = useAuth();
  const list = useGet<Page<R>>('/api/v1/golf/handicaps');
  const [v, setV] = useState({ customerId: '', handicapIndex: '', notes: '' });
  const customers = useGet<Page<R>>('/api/v1/crm/customers?limit=500&filter[status]=active');
  const send = useSend<Record<string, unknown>>('POST', '/api/v1/golf/handicaps', ['/api/v1/golf/handicaps']);
  const names = new Map((customers.data?.items ?? []).map((c) => [c.id, String(c.name)]));
  return (
    <div className="oc-stack">
      <PageHeader title="Handicap Index" />
      {can('golf.handicap.manage') && (
        <div className="oc-row-wrap">
          <SelectField label="Customer" value={v.customerId} onChange={(x) => setV({ ...v, customerId: x })} options={(customers.data?.items ?? []).map((c) => ({ value: c.id, label: String(c.name) }))} />
          <TextField label="Handicap Index" value={v.handicapIndex} onChange={(x) => setV({ ...v, handicapIndex: x })} />
          <TextField label="Notes" value={v.notes} onChange={(x) => setV({ ...v, notes: x })} />
          <button className="oc-btn oc-btn-ink" disabled={!v.customerId || !v.handicapIndex} onClick={() => send.mutate({ ...v, notes: v.notes || undefined })}>Record</button>
        </div>
      )}
      <ErrorAlert error={send.error} />
      <DataTable rows={list.data?.items} loading={list.isLoading} columns={[{ key: 'customerId', header: 'Customer', render: (h) => names.get(String(h.customerId)) ?? '—' },
        { key: 'handicapIndex', header: 'Handicap Index', align: 'right' }, { key: 'source', header: 'Source' }, { key: 'effectiveAt', header: 'Effective', render: (h) => formatDateTime(String(h.effectiveAt)) }]} />
    </div>
  );
}

// ── Rain checks, golf settings (EP-03 templates, lockers, policies) ─────────

export function RainChecksPage() {
  return (
    <ListPage title="Rain Checks" help="Credits issued when weather stops a round; redeemed on a later booking." path="/api/v1/golf/rain-checks" search={false}
      statuses={['issued', 'redeemed', 'expired'].map((v) => ({ value: v, label: v }))}
      columns={[{ key: 'number', header: 'Rain Check' }, { key: 'bookingCode', header: 'Booking' }, { key: 'playerName', header: 'Player' },
        { key: 'holesPlayed', header: 'Holes played', render: (r) => `${String(r.holesPlayed)}/${String(r.holesTotal)}` }, { key: 'creditAmount', header: 'Credit', align: 'right', render: (r) => money(r.creditAmount) },
        { key: 'expiresOn', header: 'Expires' }, { key: 'status', header: 'Status', render: pill('status') }]} />
  );
}

const templateCfg: ResourceConfig = {
  title: 'Tee Sheet Templates', singular: 'Tee Sheet Template', path: '/api/v1/golf/tee-sheet-templates', perm: 'golf.tee_sheet',
  columns: [{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'dayTypeCode', header: 'Day Type' }, { key: 'session', header: 'Session' },
    { key: 'startTime', header: 'From' }, { key: 'endTime', header: 'To' }, { key: 'intervalMinutes', header: 'Interval', align: 'right' }, { key: 'effectiveFrom', header: 'Effective' }, statusCol],
  fields: [{ name: 'courseId', label: 'Course', type: 'reference', required: true, ref: courseRef }, { name: 'code', label: 'Code', required: true }, { name: 'name', label: 'Name', required: true },
    { name: 'dayTypeCode', label: 'Day Type', required: true, help: 'WEEKDAY, WEEKEND …' },
    { name: 'session', label: 'Session', type: 'select', required: true, options: [{ value: 'morning', label: 'Morning' }, { value: 'afternoon', label: 'Afternoon' }, { value: 'night', label: 'Night' }] },
    { name: 'startTime', label: 'First tee time', required: true, placeholder: '05:30' }, { name: 'endTime', label: 'Last tee time', required: true, placeholder: '08:10' },
    { name: 'intervalMinutes', label: 'Interval (minutes)', type: 'number', required: true, default: 8 },
    { name: 'startTees', label: 'Start tees', type: 'select', default: '1', options: [{ value: '1', label: 'Tee 1' }, { value: '1,10', label: 'Tee 1 and 10 (two-tee start)' }] },
    { name: 'minPlayers', label: 'Min players', type: 'number', default: 2 }, { name: 'maxPlayers', label: 'Max players', type: 'number', default: 4 },
    { name: 'playingRouteId', label: 'Playing Route', type: 'reference', ref: { path: '/api/v1/golf/playing-routes', label: (r: R) => String(r.name) } },
    { name: 'peak', label: 'Peak', type: 'boolean' }, { name: 'memberOnly', label: 'Member only', type: 'boolean' }, { name: 'lighting', label: 'Night golf lighting', type: 'boolean' },
    { name: 'effectiveFrom', label: 'Effective from', type: 'date', required: true }, { name: 'effectiveTo', label: 'Effective to', type: 'date' }, st],
};

const lockerCfg: ResourceConfig = {
  title: 'Lockers', singular: 'Locker', path: '/api/v1/golf/lockers', perm: 'golf.locker',
  columns: [{ key: 'code', header: 'Locker No.' }, { key: 'name', header: 'Name' }, { key: 'area', header: 'Area' }, { key: 'zone', header: 'Zone' },
    { key: 'lockerStatus', header: 'Locker', render: pill('lockerStatus') }, statusCol],
  fields: [{ name: 'code', label: 'Locker No.', required: true }, { name: 'name', label: 'Name', required: true },
    { name: 'area', label: 'Area', type: 'select', required: true, options: [{ value: 'male', label: 'Male' }, { value: 'female', label: 'Female' }] }, { name: 'zone', label: 'Zone' },
    { name: 'lockerStatus', label: 'Locker status', type: 'select', default: 'available', options: [{ value: 'available', label: 'Available' }, { value: 'maintenance', label: 'Maintenance' }] }, st],
};

export function GolfSettingsPage() {
  const [tab, setTab] = useState('generate');
  return (
    <div className="oc-stack">
      <Tabs tabs={[{ value: 'generate', label: 'Tee Sheet Generation' }, { value: 'templates', label: 'Tee Sheet Templates' }, { value: 'lockers', label: 'Lockers' }, { value: 'policies', label: 'Golf Policies' }]}
        value={tab} onChange={setTab} />
      {tab === 'generate' && <GenerateCard />}
      {tab === 'templates' && <ResourcePage cfg={templateCfg} />}
      {tab === 'lockers' && <ResourcePage cfg={lockerCfg} />}
      {tab === 'policies' && <PoliciesView />}
    </div>
  );
}

function GenerateCard() {
  const cd = useCourseDate();
  const [days, setDays] = useState('14');
  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/golf/tee-sheets:generate', ['/api/v1/golf']);
  return (
    <Card title="Generate tee sheet" icon="calendar_month">
      <p className="oc-muted">Slots are generated from the templates in force; slots with bookings are never changed. The nightly job keeps the booking window generated.</p>
      <div className="oc-row-wrap">
        <CourseDateBar cd={cd} />
        <TextField label="Days" type="number" min={1} max={90} value={days} onChange={setDays} />
        <button className="oc-btn oc-btn-ink" disabled={send.isPending} onClick={() => send.mutate({ courseId: cd.courseId, from: cd.date, days: Number(days) })}>Generate</button>
      </div>
      {send.data && <p>Created {String(send.data.created)}, updated {String(send.data.updated)}, closed {String(send.data.closed)}, unchanged {String(send.data.unchanged)}.</p>}
      <ErrorAlert error={send.error} />
    </Card>
  );
}

function PoliciesView() {
  const pol = useGet<Page<R>>('/api/v1/golf/policies');
  return (
    <div className="oc-stack">
      <PageHeader title="Golf Policies" help="Versions in force. Edit them in Settings → Club Policies." actions={<Link className="oc-btn oc-btn-neutral" to="/settings/club-policies">Club Policies</Link>} />
      {pol.isLoading && <Skeleton />}
      <div className="oc-grid-2">
        {(pol.data?.items ?? []).map((p) => (
          <Card key={String(p.code)} title={String(p.code).replace(/[._]/g, ' ')} icon="policy">
            <div className="oc-muted">{Number(p.version) > 0 ? `Version ${String(p.version)}` : 'Built-in default (no version saved)'}</div>
            <pre style={{ whiteSpace: 'pre-wrap', fontSize: 12 }}>{JSON.stringify(p.value, null, 2)}</pre>
          </Card>
        ))}
      </div>
    </div>
  );
}

// ── Booking module pages ────────────────────────────────────────────────────

export function AvailabilityPage() {
  const cd = useCourseDate();
  const [players, setPlayers] = useState('2');
  const list = useGet<Page<R>>(cd.courseId ? `/api/v1/golf/availability${qs({ courseId: cd.courseId, date: cd.date, players })}` : null);
  return (
    <div className="oc-stack">
      <PageHeader title="Availability" help="Open tee times with prices for the number of players." />
      <div className="oc-row-wrap"><CourseDateBar cd={cd} /><TextField label="Players" type="number" min={1} max={4} value={players} onChange={setPlayers} /></div>
      <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error}
        columns={[{ key: 'localTime', header: 'Tee Time' }, { key: 'startTee', header: 'Tee' }, { key: 'session', header: 'Session' }, { key: 'remaining', header: 'Places', align: 'right' },
          { key: 'prices', header: 'Prices', render: (s) => Object.entries((s.prices as Record<string, string>) ?? {}).map(([seg, v]) => `${seg.replace(/_/g, ' ')} ${money(v)}`).join(' · ') }]} />
    </div>
  );
}

export function BookingHistoryPage() {
  const [params] = useSearchParams();
  return <BookingsPage title="Booking History" noDate preset={{ to: params.get('to') ?? today() }} />;
}

export function CancellationsPage() {
  const [status, setStatus] = useState('cancelled');
  return (
    <div className="oc-stack">
      <Tabs tabs={[{ value: 'cancelled', label: 'Cancelled' }, { value: 'no_show', label: 'No-show' }]} value={status} onChange={setStatus} />
      <BookingsPage key={status} title="Cancellations" noDate preset={{ 'filter[status]': status }} />
    </div>
  );
}
