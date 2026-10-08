import React, { useEffect, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router';
import { qs, request, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDateTime } from '@oneclub/i18n';
import { Checkbox, CrowdLabel, DataTable, ErrorAlert, Icon, Modal, PlayTime, SelectField, StatusPill, TextField, crowdClass, useToast } from '@oneclub/shell';
import { BookingForm, type BookingPrefill } from '../p1/golf';
import { Btn, Head, money, today, useCached } from './golf';

// Front Desk (demo feedback 9 Oct 2026): players book at the desk with or
// without an account, the desk moves tee times first come first served,
// edits the players, assigns the caddies (the caddy is in the rate) and
// takes the payment at the start, at the end or in part — for everyone,
// split per player or merged with other bookings into one bill.

type R = Record<string, unknown> & { id: string };
type Booking = R & { players: R[]; flights: R[]; folio?: R; teeTimeId: string; courseId: string; playDate: string };

const METHODS = [['cash', 'Cash'], ['card', 'Card (EDC)'], ['qris', 'QRIS'], ['bank_transfer', 'Bank transfer']].map(([value, label]) => ({ value, label }));
const digits = (v: string) => v.replace(/\D/g, '');
/** Rows keyed by another id field (DataTable needs id). */
const withId = (xs: R[] | undefined, key: string): R[] => (xs ?? []).map((x) => ({ ...x, id: String(x[key]) }) as R);
const live = (p: R) => !['removed', 'cancelled'].includes(String(p.status));

export function FrontDeskPage() {
  const [date, setDate] = useState(today());
  const toast = useToast();
  const bookings = useCached<Page<R>>(`/api/v1/golf/bookings${qs({ date, limit: 500 })}`, `bookings:${date}`);
  const [open, setOpen] = useState<string | null>(null);
  const [merge, setMerge] = useState<string[] | null>(null);
  const [paying, setPaying] = useState(false);
  const toggle = (id: string, on: boolean) => setMerge((m) => (on ? [...(m ?? []), id] : (m ?? []).filter((x) => x !== id)));
  return (
    <div className="oc-stack">
      <Head title="Reservations" help={`${date}${bookings.offline ? ' · offline copy' : ''}`} actions={<>
        <Link className="oc-btn oc-btn-primary" to="/ops/front-desk/new"><Icon name="add" size={18} /> New Booking</Link>
        <button className="oc-btn oc-btn-neutral" onClick={() => setMerge(merge ? null : [])}>{merge ? 'Cancel merge' : 'Merge bills'}</button>
        <Link className="oc-btn oc-btn-ink" to="/ops/check-in">Check-in</Link></>} />
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <TextField label="Date" type="date" value={date} onChange={setDate} />
        {merge && <span className="oc-muted">Choose the bookings one person pays for · {merge.length} selected</span>}
        {merge && <Btn label="Pay merged bill" kind="primary" disabled={merge.length < 2} onClick={() => setPaying(true)} />}
      </div>
      <DataTable rows={bookings.data?.items} loading={bookings.isLoading} columns={[{ key: 'localTime', header: 'Tee Time' }, { key: 'code', header: 'Booking' },
        { key: 'contactName', header: 'Booked by' }, { key: 'playerCount', header: 'Players', align: 'right' },
        { key: 'paymentMode', header: 'Payment', render: (b) => String(b.paymentMode ?? '—').replace(/_/g, ' ') },
        { key: 'status', header: 'Status', render: (b) => <StatusPill status={String(b.status).replace(/_/g, '-')} /> },
        { key: 'teeOffAt', header: 'Play time', render: (b) => <PlayTime start={b.teeOffAt as string} end={b.roundFinishAt as string} pausedAt={b.pausedAt as string | null | undefined} pausedSeconds={Number(b.pausedSeconds ?? 0)} label={false} fallback="—" /> }]}
        actions={(b) => (bookings.offline ? null : merge
          ? <Checkbox label="Merge" checked={merge.includes(b.id)} disabled={['cancelled', 'no_show'].includes(String(b.status))} onChange={(on) => toggle(b.id, on)} />
          : <Btn label="Manage" onClick={() => setOpen(b.id)} />)} />
      {open && <DeskBookingModal id={open} onClose={() => { setOpen(null); void bookings.refetch(); }} />}
      {paying && merge && <MergeBillModal ids={merge} onClose={() => setPaying(false)}
        onDone={() => { setPaying(false); setMerge(null); toast('Merged bill paid'); void bookings.refetch(); }} />}
    </div>
  );
}

/** New booking at the desk: a tee time or the driving range, walk-in (no
 * account needed) or by phone. */
export function DeskNewBookingPage() {
  const [params, setParams] = useSearchParams();
  const range = params.get('kind') === 'range';
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap" role="tablist">
        <Btn label="Tee Time" kind={range ? 'neutral' : 'ink'} onClick={() => setParams({})} />
        <Btn label="Driving Range" kind={range ? 'ink' : 'neutral'} onClick={() => setParams({ kind: 'range' })} />
      </div>
      {range ? <DeskRangeBooking /> : <DeskTeeTimeBooking />}
    </div>
  );
}

function DeskTeeTimeBooking() {
  const nav = useNavigate();
  const [params] = useSearchParams();
  const prefill = useRebook(params.get('rebook'));
  const [date, setDate] = useState(today());
  const courses = useGet<Page<R>>('/api/v1/golf/courses?filter[status]=active&limit=50');
  const [courseId, setCourse] = useState('');
  const course = courseId || courses.data?.items[0]?.id || '';
  const [walkIn, setWalkIn] = useState(true);
  const slots = useGet<Page<R>>(course ? `/api/v1/golf/tee-times${qs({ courseId: course, date })}` : null);
  const [slot, setSlot] = useState<R | null>(null);
  // never refused when busy (FIFO): red peak, green quiet
  const open = (slots.data?.items ?? []).filter((s) => s.status !== 'blocked');
  return (
    <div className="oc-stack">
      <Head title="New Booking" help="Pick a tee time, then the players: members by member no., guests and walk-ins by name — no account needed."
        actions={<Link className="oc-btn oc-btn-neutral" to="/ops/front-desk">Reservations</Link>} />
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <TextField label="Date" type="date" value={date} onChange={(v) => { setDate(v); setSlot(null); }} />
        {(courses.data?.items.length ?? 0) > 1 && <SelectField label="Course" value={course} onChange={setCourse}
          options={(courses.data?.items ?? []).map((c) => ({ value: c.id, label: String(c.name) }))} />}
        <Btn label="Walk-in" kind={walkIn ? 'ink' : 'neutral'} onClick={() => setWalkIn(true)} />
        <Btn label="Phone / reservation" kind={walkIn ? 'neutral' : 'ink'} onClick={() => setWalkIn(false)} />
      </div>
      <ErrorAlert error={slots.error} />
      {slots.data && open.length === 0 && <p className="oc-muted">No tee times on this date.</p>}
      {open.length > 0 && <p className="oc-small oc-muted" style={{ margin: 0 }}>Red: peak (full or peak time) — the flight joins the starter queue, first come first served. Green: quiet.</p>}
      <div className="oc-row-wrap">
        {open.map((s) => (
          <button key={s.id} type="button" className={`oc-btn oc-btn-neutral ${crowdClass(s.crowd as string)}`} style={{ minHeight: 56, minWidth: 104, flexDirection: 'column' }} onClick={() => setSlot(s)}>
            <strong>{String(s.localTime)}</strong>
            <span className="oc-small"><CrowdLabel crowd={s.crowd as string} />{Number(s.startTee) > 1 ? ` · T${String(s.startTee)}` : ''}</span>
          </button>
        ))}
      </div>
      {prefill && <div className="oc-alert oc-alert-info">Rain rebooking: {prefill.players.length} players with {prefill.rainCheckIds.length} rain check(s). Pick the new tee time.</div>}
      {slot && <BookingForm key={`${slot.id}-${walkIn}`} slot={slot} channel={walkIn ? 'walk_in' : 'back_office'} prefill={prefill ?? undefined} onClose={() => setSlot(null)}
        onDone={() => nav('/ops/front-desk')} />}
    </div>
  );
}

/** Driving range: a bay and a time, or the visit only (balls at the counter). */
function DeskRangeBooking() {
  const toast = useToast();
  const [date, setDate] = useState(today());
  const [area, setArea] = useState('outdoor');
  const [minutes, setMinutes] = useState('60');
  const [bay, setBay] = useState(true);
  const [time, setTime] = useState('');
  const [bayId, setBayId] = useState('');
  const [guest, setGuest] = useState({ name: '', phone: '', players: '1' });
  const slots = useGet<Page<R>>(`/api/v1/golf/range-availability${qs({ date, area, minutes })}`);
  const book = useSend<Record<string, unknown>, R>('POST', '/api/v1/golf/range-bookings', ['/api/v1/golf/range-bookings']);
  const list = slots.data?.items ?? [];
  const picked = list.find((s) => s.time === time);
  return (
    <div className="oc-stack">
      <Head title="Book Driving Range" help="The time is flexible: a held bay is kept 15 minutes; a late guest gets the next free bay or the queue."
        actions={<Link className="oc-btn oc-btn-neutral" to="/ops/driving-range">Driving Range</Link>} />
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <Btn label="Bay & time" kind={bay ? 'ink' : 'neutral'} onClick={() => setBay(true)} />
        <Btn label="Visit only" kind={bay ? 'neutral' : 'ink'} onClick={() => { setBay(false); setBayId(''); }} />
        <TextField label="Date" type="date" value={date} onChange={(v) => { setDate(v); setTime(''); }} />
        <SelectField label="Area" value={area} onChange={(v) => { setArea(v); setTime(''); }} options={[{ value: 'outdoor', label: 'Outdoor' }, { value: 'indoor', label: 'Indoor' }]} />
        <SelectField label="Length" value={minutes} onChange={(v) => { setMinutes(v); setTime(''); }}
          options={['30', '60', '90', '120'].map((m) => ({ value: m, label: `${m} min` }))} />
      </div>
      <ErrorAlert error={slots.error} />
      <div className="oc-row-wrap">
        {list.map((s) => (
          <button key={String(s.time)} type="button" className={`oc-btn ${time === s.time ? 'oc-btn-ink' : 'oc-btn-neutral'} ${crowdClass(s.crowd as string)}`}
            style={{ minHeight: 56, minWidth: 92, flexDirection: 'column' }} onClick={() => { setTime(String(s.time)); setBayId(''); }}>
            <strong>{String(s.time)}</strong>
            <span className="oc-small"><CrowdLabel crowd={s.crowd as string} quiet={bay ? `${String(s.freeBays)} bays` : 'Quiet'} /></span>
          </button>
        ))}
      </div>
      {time && (
        <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
          {bay && <SelectField label="Bay" value={bayId} onChange={setBayId} placeholder="Any free bay"
            options={((picked?.bays as R[] | undefined) ?? []).map((b) => ({ value: String(b.id), label: String(b.code) }))} />}
          <TextField label="Guest name" value={guest.name} onChange={(v) => setGuest({ ...guest, name: v })} />
          <TextField label="Phone" value={guest.phone} onChange={(v) => setGuest({ ...guest, phone: v })} />
          <SelectField label="Players" value={guest.players} onChange={(v) => setGuest({ ...guest, players: v })} options={['1', '2', '3', '4'].map((n) => ({ value: n, label: n }))} />
          <Btn label="Book" kind="primary" disabled={!guest.name || book.isPending}
            onClick={() => book.mutate({ date, time, minutes: Number(minutes), area, reserveBay: bay, bayId: bayId || undefined, players: Number(guest.players),
              guestName: guest.name, guestPhone: guest.phone || undefined }, { onSuccess: (b) => { toast(`Range booking ${String(b.number)}`); setTime(''); setGuest({ name: '', phone: '', players: '1' }); } })} />
        </div>
      )}
      <ErrorAlert error={book.error} />
    </div>
  );
}

// ── one booking at the desk ────────────────────────────────────────────────

const TABS = [['bill', 'Bill'], ['time', 'Tee Time'], ['players', 'Players'], ['caddy', 'Caddy & Cart'], ['profile', 'Profile']] as const;
type Tab = (typeof TABS)[number][0];

function DeskBookingModal({ id, onClose }: { id: string; onClose: () => void }) {
  const b = useGet<Booking>(`/api/v1/golf/bookings/${id}`);
  const [tab, setTab] = useState<Tab>('bill');
  const x = b.data;
  return (
    <Modal open wide onClose={onClose} title={x ? `${String(x.code)} · ${String(x.localTime)} · ${String(x.contactName)}` : 'Booking'}
      actions={<Btn label="Close" onClick={onClose} />}>
      <ErrorAlert error={b.error} />
      {x && (
        <div className="oc-stack">
          <div className="oc-row-wrap">
            <StatusPill status={String(x.status).replace(/_/g, '-')} />
            <BookingPlayTime flights={x.flights} />
            <span className="oc-muted">{String(x.playDate)} · {String(x.courseName)} · {x.players.filter(live).length} players · payment {String(x.paymentMode ?? '—').replace(/_/g, ' ')}</span>
          </div>
          <div className="oc-row-wrap" role="tablist">
            {TABS.map(([k, l]) => <Btn key={k} label={l} kind={tab === k ? 'ink' : 'neutral'} onClick={() => setTab(k)} />)}
          </div>
          {tab === 'bill' && <BillTab id={id} />}
          {tab === 'time' && <TimeTab b={x} onDone={() => void b.refetch()} />}
          {tab === 'players' && <PlayersTab b={x} onDone={() => void b.refetch()} />}
          {tab === 'caddy' && <CaddyTab b={x} />}
          {tab === 'profile' && <ProfileTab b={x} />}
        </div>
      )}
    </Modal>
  );
}

/** Pay all, any part, or per player (split bill: more than one player). */
function BillTab({ id }: { id: string }) {
  const bill = useGet<R & { players: R[]; lines: R[]; payments: R[] }>(`/api/v1/golf/bookings/${id}/bill`);
  const pay = useSend<Record<string, unknown>>('POST', `/api/v1/golf/bookings/${id}/bill:pay`, ['/api/v1/golf', '/api/v1/billing']);
  const toast = useToast();
  const [split, setSplit] = useState(false);
  const [picked, setPicked] = useState<string[]>([]);
  const [amount, setAmount] = useState('');
  const [method, setMethod] = useState('cash');
  const [ref, setRef] = useState('');
  const [payer, setPayer] = useState('');
  const x = bill.data;
  if (!x) return <ErrorAlert error={bill.error} />;
  const balance = Number(x.balance);
  const players = x.players ?? [];
  const pickedDue = players.filter((p) => picked.includes(String(p.playerId))).reduce((a, p) => a + Number(p.due), 0);
  const n = amount === '' ? balance : Number(amount);
  const done = () => { toast('Payment recorded'); setPicked([]); setAmount(''); setRef(''); void bill.refetch(); };
  const go = () => pay.mutate(split
    ? { playerIds: picked, methodType: method, reference: ref || undefined }
    : { amount: amount || undefined, methodType: method, reference: ref || undefined, payerName: payer || undefined }, { onSuccess: done });
  return (
    <div className="oc-stack">
      <p style={{ margin: 0 }}>Charges {money(x.charges)} · paid {money(x.paid)} · balance <strong>{money(x.balance)}</strong></p>
      <DataTable rows={withId(x.lines, 'id').filter((l) => !l.voidedAt)} columns={[{ key: 'description', header: 'Charge' },
        { key: 'total', header: 'Amount', align: 'right', render: (l) => money(l.total) }]} />
      {balance > 0 && (
        <>
          <div className="oc-row-wrap">
            <Btn label="One payer" kind={split ? 'neutral' : 'ink'} onClick={() => setSplit(false)} />
            {players.length > 1 && <Btn label="Split per player" kind={split ? 'ink' : 'neutral'} onClick={() => setSplit(true)} />}
          </div>
          {split ? (
            <DataTable rows={withId(players, 'playerId')} columns={[{ key: 'name', header: 'Player' },
              { key: 'share', header: 'Share', align: 'right', render: (p) => money(p.share) }, { key: 'paid', header: 'Paid', align: 'right', render: (p) => money(p.paid) },
              { key: 'due', header: 'Due', align: 'right', render: (p) => <strong>{money(p.due)}</strong> }]}
              actions={(p) => Number(p.due) > 0 && <Checkbox label="Pays now" checked={picked.includes(p.id)}
                onChange={(on) => setPicked((s) => (on ? [...s, p.id] : s.filter((v) => v !== p.id)))} />} />
          ) : (
            <div className="oc-row-wrap">
              <TextField label="Amount (empty = all)" value={amount} onChange={(v) => setAmount(digits(v))} inputMode="numeric" />
              <TextField label="Payer name" value={payer} onChange={setPayer} />
            </div>
          )}
          <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
            <SelectField label="Method" value={method} onChange={setMethod} options={METHODS} />
            {method !== 'cash' && <TextField label="Reference" value={ref} onChange={setRef} />}
            <Btn label={`Receive ${money(split ? pickedDue : n)}`} kind="primary" onClick={go}
              disabled={pay.isPending || (split ? picked.length === 0 : !(n > 0 && n <= balance))} />
          </div>
          <p className="oc-small oc-muted" style={{ margin: 0 }}>
            The rest can be paid later — here, at check-out, or by the member in the app. On-course F&B is added from the POS (Golfer Bill).
          </p>
        </>
      )}
      <ErrorAlert error={pay.error} />
      {(x.payments ?? []).length > 0 && (
        <DataTable rows={withId(x.payments, 'id')} columns={[{ key: 'number', header: 'Payment' },
          { key: 'payerName', header: 'Payer', render: (p) => String(p.payerName ?? '—') }, { key: 'methodType', header: 'Method', render: (p) => String(p.methodType).replace(/_/g, ' ') },
          { key: 'amount', header: 'Amount', align: 'right', render: (p) => money(p.amount) },
          { key: 'paidAt', header: 'Paid', render: (p) => (p.paidAt ? formatDateTime(String(p.paidAt)) : '—') },
          { key: 'status', header: 'Status', render: (p) => <StatusPill status={String(p.status)} /> }]} />
      )}
    </div>
  );
}

/** The players and issued rain checks of a rained-off booking. */
function useRebook(id: string | null): BookingPrefill | null {
  const b = useGet<Booking>(id ? `/api/v1/golf/bookings/${id}` : null);
  const rc = useGet<Page<R>>(id ? '/api/v1/golf/rain-checks?filter[status]=issued&limit=200' : null);
  if (!id || !b.data) return null;
  const players = b.data.players.filter((p) => live(p));
  return {
    bookingType: String(b.data.bookingType), contactName: String(b.data.contactName ?? ''), contactPhone: String(b.data.contactPhone ?? ''),
    players: players.map((p) => ({ playerType: String(p.playerType), memberId: p.memberId ? String(p.memberId) : undefined, memberNo: '', name: String(p.name ?? ''), phone: String(p.phone ?? '') })),
    rainCheckIds: (rc.data?.items ?? []).filter((r) => r.bookingId === id).map((r) => r.id),
  };
}

/** Rain: pause / resume the play time; before half of the round the
 * players get a full rain check and are rebooked. */
function RainPanel({ b, onDone }: { b: Booking; onDone: () => void }) {
  const toast = useToast();
  const nav = useNavigate();
  const inv = ['/api/v1/golf'];
  const pause = useSend<Record<string, unknown>>('POST', (v) => `/api/v1/golf/flights/${String(v.id)}:${String(v.op)}`, inv);
  const stop = useSend<Record<string, unknown>, Page<R>>('POST', '/api/v1/golf/rain-checks', inv);
  const [holes, setHoles] = useState<Record<string, string>>({});
  const [issued, setIssued] = useState<R[]>([]);
  const playing = b.flights.filter((f) => f.teeOffAt && !f.roundFinishAt);
  if (!playing.length && !issued.length) return null;
  return (
    <div className="oc-card oc-stack">
      <strong>Rain</strong>
      {playing.map((f) => {
        const done = Math.max(Number(f.currentHole ?? 0) - 1, 0);
        const h = holes[f.id] ?? String(done);
        return (
          <div key={f.id} className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
            <span style={{ minWidth: 200 }}>Flight {String(f.flightNo)} · hole {String(f.currentHole ?? 0)}{f.pausedAt ? ' · paused for rain' : ''}</span>
            {f.pausedAt
              ? <Btn label="Resume play" kind="primary" disabled={pause.isPending} onClick={() => pause.mutate({ id: f.id, op: 'resume' }, { onSuccess: () => { toast('Play resumed'); onDone(); } })} />
              : <Btn label="Pause (rain)" disabled={pause.isPending} onClick={() => pause.mutate({ id: f.id, op: 'pause', reason: 'rain' }, { onSuccess: () => { toast('Play time paused'); onDone(); } })} />}
            <TextField label="Holes played" value={h} onChange={(v) => setHoles({ ...holes, [f.id]: v.replace(/\D/g, '') })} inputMode="numeric" />
            <Btn label="Rain stop" kind="danger" disabled={stop.isPending || h === ''}
              onClick={() => stop.mutate({ flightId: f.id, holesPlayed: Number(h) }, { onSuccess: (r) => { setIssued(r.items ?? []); toast('Rain checks issued'); onDone(); } })} />
          </div>
        );
      })}
      <span className="oc-small oc-muted">Rain before half of the holes gives a 100% rain check: the players reschedule at no cost.</span>
      {issued.length > 0 && (
        <>
          <DataTable rows={issued} columns={[{ key: 'number', header: 'Rain check' }, { key: 'playerName', header: 'Player' },
            { key: 'creditPercent', header: 'Credit', align: 'right', render: (r) => `${String(r.creditPercent)}%` }, { key: 'creditAmount', header: 'Value', align: 'right', render: (r) => money(r.creditAmount) },
            { key: 'expiresOn', header: 'Valid until' }]} />
          <div><Btn label="Book the new tee time" kind="primary" onClick={() => nav(`/ops/front-desk/new?rebook=${b.id}`)} /></div>
        </>
      )}
      <ErrorAlert error={pause.error ?? stop.error} />
    </div>
  );
}

/** First come, first served: move the flight to another tee time. */
function TimeTab({ b, onDone }: { b: Booking; onDone: () => void }) {
  const toast = useToast();
  const [date, setDate] = useState(b.playDate);
  const slots = useGet<Page<R>>(`/api/v1/golf/tee-times${qs({ courseId: b.courseId, date })}`);
  const [slot, setSlot] = useState('');
  const [keep, setKeep] = useState(true);
  const [reason, setReason] = useState('');
  const move = useSend<Record<string, unknown>>('POST', `/api/v1/golf/bookings/${b.id}:reschedule`, ['/api/v1/golf']);
  const n = b.players.filter((p) => p.status === 'booked' || p.status === 'checked_in').length;
  const free = (slots.data?.items ?? []).filter((s) => s.id !== b.teeTimeId && s.status !== 'blocked' && n <= Number(s.maxPlayers || 4));
  return (
    <div className="oc-stack">
      <RainPanel b={b} onDone={onDone} />
      <p style={{ margin: 0 }}>Now {String(b.playDate)} {String(b.localTime)}. Late or early? Move the flight to the next free tee time.</p>
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <TextField label="Date" type="date" value={date} onChange={(v) => { setDate(v); setSlot(''); }} />
        <SelectField label="New tee time" value={slot} onChange={setSlot} placeholder="Choose"
          options={free.map((s) => ({ value: s.id, label: `${String(s.localTime)}${Number(s.startTee) > 1 ? ` · tee ${String(s.startTee)}` : ''} · ${s.crowd === 'peak' ? '🔴 peak' : '🟢 quiet'}` }))} />
        <TextField label="Reason" value={reason} onChange={setReason} placeholder="FIFO at the front desk" />
      </div>
      <Checkbox label="Keep the price (time change only)" checked={keep} onChange={setKeep} />
      <ErrorAlert error={move.error} />
      <div><Btn label="Move tee time" kind="primary" disabled={!slot || move.isPending}
        onClick={() => move.mutate({ teeTimeId: slot, reason: reason || 'FIFO at the front desk', keepPrice: keep },
          { onSuccess: () => { toast('Tee time moved'); setSlot(''); onDone(); } })} /></div>
    </div>
  );
}

/** Complete, edit, add or remove players. */
function PlayersTab({ b, onDone }: { b: Booking; onDone: () => void }) {
  const toast = useToast();
  const [edit, setEdit] = useState<Record<string, { name: string; phone: string }>>({});
  const [add, setAdd] = useState({ playerType: 'non_member', memberNo: '', name: '', phone: '' });
  const inv = ['/api/v1/golf'];
  const patch = useSend<Record<string, unknown>>('PATCH', (v) => `/api/v1/golf/bookings/${b.id}/players/${String(v.playerId)}`, inv);
  const remove = useSend<Record<string, unknown>>('DELETE', (v) => `/api/v1/golf/bookings/${b.id}/players/${String(v.playerId)}`, inv);
  const create = useSend<Record<string, unknown>>('POST', `/api/v1/golf/bookings/${b.id}/players`, inv);
  const players = b.players.filter(live);
  const ok = () => { toast('Players updated'); setEdit({}); onDone(); };
  return (
    <div className="oc-stack">
      {players.map((p) => {
        const e = edit[p.id];
        return (
          <div key={p.id} className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
            {e ? (
              <>
                <TextField label="Name" value={e.name} onChange={(v) => setEdit({ ...edit, [p.id]: { ...e, name: v } })} />
                <TextField label="Phone" value={e.phone} onChange={(v) => setEdit({ ...edit, [p.id]: { ...e, phone: v } })} />
                <Btn label="Save" kind="primary" disabled={patch.isPending} onClick={() => patch.mutate({ playerId: p.id, name: e.name, phone: e.phone || undefined }, { onSuccess: ok })} />
              </>
            ) : (
              <span style={{ minWidth: 220 }}><strong>{String(p.name || 'Guest (TBA)')}</strong>
                <span className="oc-small oc-muted"> · {String(p.playerType).replace(/_/g, ' ')} · {String(p.status).replace(/_/g, ' ')}</span>
                <FlightPlayTime flights={b.flights} flightId={p.flightId} /></span>
            )}
            {!e && p.playerType !== 'member' && <Btn label="Edit" onClick={() => setEdit({ ...edit, [p.id]: { name: String(p.name ?? ''), phone: String(p.phone ?? '') } })} />}
            {!e && p.status === 'booked' && players.length > 1 && <Btn label="Remove" kind="danger" disabled={remove.isPending}
              onClick={() => remove.mutate({ playerId: p.id }, { onSuccess: ok })} />}
          </div>
        );
      })}
      <ErrorAlert error={patch.error ?? remove.error} />
      <h3 style={{ margin: '8px 0 0' }}>Add player</h3>
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <SelectField label="Type" value={add.playerType} onChange={(v) => setAdd({ ...add, playerType: v })}
          options={[{ value: 'non_member', label: 'Non-member' }, { value: 'guest_of_member', label: 'Guest of member' }, { value: 'member', label: 'Member' }]} />
        {add.playerType === 'member'
          ? <TextField label="Member No." value={add.memberNo} onChange={(v) => setAdd({ ...add, memberNo: v })} />
          : <><TextField label="Name" value={add.name} onChange={(v) => setAdd({ ...add, name: v })} />
            <TextField label="Phone" value={add.phone} onChange={(v) => setAdd({ ...add, phone: v })} /></>}
        <Btn label="Add" kind="primary" disabled={create.isPending || (add.playerType === 'member' ? !add.memberNo : !add.name)}
          onClick={() => create.mutate({ player: { playerType: add.playerType, memberNo: add.playerType === 'member' ? add.memberNo : undefined,
            name: add.playerType === 'member' ? undefined : add.name, phone: add.phone || undefined } },
          { onSuccess: () => { setAdd({ playerType: 'non_member', memberNo: '', name: '', phone: '' }); ok(); } })} />
      </div>
      <ErrorAlert error={create.error} />
    </div>
  );
}

/** The front desk is also the Caddy Master: the caddy (in the rate) per
 * player, and the golf carts of each flight. */
function CaddyTab({ b }: { b: Booking }) {
  return (
    <div className="oc-stack">
      {b.flights.filter((f) => f.status !== 'cancelled').map((f) => (
        <div key={String(f.id)} className="oc-stack">
          {b.flights.length > 1 && <strong>Flight {String(f.flightNo)}</strong>}
          <FlightCaddies bookingId={b.id} code={String(b.code)} flightId={String(f.id)} date={b.playDate} />
          <FlightCarts flightId={String(f.id)} date={b.playDate} players={b.players.filter((p) => live(p) && p.flightId === f.id)} />
        </div>
      ))}
    </div>
  );
}

/** Golf carts: one cart carries 2 players and their 2 caddies. */
function FlightCarts({ flightId, date, players }: { flightId: string; date: string; players: R[] }) {
  const toast = useToast();
  const list = useGet<Page<R>>(`/api/v1/golf/golf-cart-assignments${qs({ date })}`);
  const ready = useGet<Page<R>>('/api/v1/golf/golf-carts?filter[readiness]=ready&filter[status]=active&limit=200');
  const inv = ['/api/v1/golf'];
  const assign = useSend<Record<string, unknown>>('POST', '/api/v1/golf/golf-cart-assignments', inv);
  const ret = useSend<{ id: string }>('POST', (v) => `/api/v1/golf/golf-cart-assignments/${v.id}:return`, inv);
  const [pick, setPick] = useState('');
  const mine = (list.data?.items ?? []).filter((a) => a.flightId === flightId && !['returned', 'cancelled'].includes(String(a.status)));
  const ok = (what: string) => () => { toast(what); setPick(''); void list.refetch(); void ready.refetch(); };
  return (
    <div className="oc-stack">
      <h3 style={{ margin: '8px 0 0' }}>Golf carts <span className="oc-small oc-muted">· 1 cart = 2 players + their 2 caddies · {Math.ceil(players.length / 2)} needed</span></h3>
      {mine.length === 0 && <span className="oc-muted">No golf cart yet</span>}
      {mine.map((a, i) => (
        <div key={a.id} className="oc-row-wrap" style={{ alignItems: 'center' }}>
          <strong style={{ minWidth: 200 }}>Cart {String(a.golfCartCode)}{a.extra ? ' · extra (surcharge)' : ''}</strong>
          <span className="oc-muted">{players.slice(i * 2, i * 2 + 2).map((p) => String(p.name || 'Guest')).join(' & ') || '—'}</span>
          <StatusPill status={String(a.status).replace(/_/g, '-')} />
          <Btn label="Return" disabled={ret.isPending} onClick={() => ret.mutate({ id: a.id }, { onSuccess: ok('Golf cart returned') })} />
        </div>
      ))}
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <SelectField label="Ready golf cart" value={pick} onChange={setPick} placeholder="Choose"
          options={(ready.data?.items ?? []).map((c) => ({ value: c.id, label: String(c.code) }))} />
        <Btn label="Assign" kind="primary" disabled={!pick || assign.isPending}
          onClick={() => assign.mutate({ flightId, golfCartIds: [pick] }, { onSuccess: ok('Golf cart assigned') })} />
        <Btn label="Assign by sharing rule" disabled={assign.isPending}
          onClick={() => assign.mutate({ flightId, auto: true }, { onSuccess: ok('Golf carts assigned') })} />
      </div>
      <ErrorAlert error={assign.error ?? ret.error} />
    </div>
  );
}

/** After check-in the desk asks who the player's caddy is, or recommends
 * one: requested, favourite, usual caddy (most rounds together), else the
 * next caddy of the queue. One caddy serves one player. */
function FlightCaddies({ bookingId, code, flightId, date }: { bookingId: string; code: string; flightId: string; date: string }) {
  const toast = useToast();
  const sugg = useGet<Page<R>>(`/api/v1/golf/bookings/${bookingId}/caddy-suggestions`);
  const board = useGet<Page<R>>(`/api/v1/golf/caddy-availability${qs({ date })}`);
  const inv = ['/api/v1/golf'];
  const assign = useSend<Record<string, unknown>>('POST', '/api/v1/golf/caddy-assignments', inv);
  const replace = useSend<Record<string, unknown>>('POST', (v) => `/api/v1/golf/caddy-assignments/${String(v.id)}:replace`, inv);
  const checkIn = useSend<Record<string, unknown>>('POST', '/api/v1/golf/check-ins', inv);
  const [pick, setPick] = useState<Record<string, string>>({});
  const players = (sugg.data?.items ?? []).filter((p) => p.flightId === flightId);
  const free = (board.data?.items ?? []).filter((c) => c.status === 'available').map((c) => ({ value: String(c.caddyId), label: `${String(c.code)} · ${String(c.name)}` }));
  const ok = (what: string) => () => { toast(what); setPick({}); void sugg.refetch(); void board.refetch(); };
  const REASON: Record<string, string> = { requested: 'asked for', favourite: 'favourite', usual: 'usual caddy', queue: 'next in queue' };
  return (
    <div className="oc-stack">
      <h3 style={{ margin: '8px 0 0' }}>Caddies <span className="oc-small oc-muted">· 1 caddy per player, assigned after check-in</span></h3>
      <ErrorAlert error={sugg.error} />
      {players.map((p) => {
        const pid = String(p.playerId);
        const current = p.current as R | null;
        const rec = p.recommended as R | null;
        const known = [p.requested as R | null, ...((p.favourites as R[]) ?? []), ...((p.usual as R[]) ?? [])].filter(Boolean) as R[];
        const chosen = pick[pid] ?? (current ? '' : String(rec?.caddyId ?? ''));
        return (
          <div key={pid} className="oc-card oc-stack" style={{ gap: 8 }}>
            <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
              <strong style={{ minWidth: 180 }}>{String(p.name || 'Guest (TBA)')}</strong>
              {current ? <span>Caddy <strong>{String(current.code)} · {String(current.name)}</strong></span>
                : p.checkedIn ? <span className="oc-muted">No caddy yet</span> : <span className="oc-muted">Not checked in</span>}
              {p.checkedIn !== true && <Btn label="Check-in" kind="primary" disabled={checkIn.isPending}
                onClick={() => checkIn.mutate({ method: 'booking_code', value: code, bookingId, playerIds: [pid], date }, { onSuccess: ok('Checked in — now the caddy') })} />}
            </div>
            {p.checkedIn === true && (
              <>
                {known.length > 0 && (
                  <div className="oc-row-wrap" aria-label="Known caddies">
                    {known.map((c, i) => (
                      <button key={`${String(c.caddyId)}-${i}`} type="button" className="oc-chip" aria-pressed={chosen === c.caddyId} disabled={!c.available}
                        title={c.available ? undefined : 'Not free today'} onClick={() => setPick({ ...pick, [pid]: String(c.caddyId) })}>
                        {String(c.code)} · {String(c.name)} · {REASON[String(c.reason)] ?? ''}{Number(c.rounds) > 0 ? ` (${String(c.rounds)} rounds)` : ''}{c.available ? '' : ' · busy'}
                      </button>
                    ))}
                  </div>
                )}
                <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
                  <SelectField label={current ? 'Replace with' : rec ? `Caddy (recommended: ${String(rec.code)}, ${REASON[String(rec.reason)] ?? ''})` : 'Caddy'}
                    value={chosen} onChange={(v) => setPick({ ...pick, [pid]: v })} placeholder="Choose" options={free} />
                  <Btn label={current ? 'Replace' : 'Assign'} kind="primary" disabled={!chosen || assign.isPending || replace.isPending}
                    onClick={() => (current && p.assignmentId
                      ? replace.mutate({ id: p.assignmentId, caddyId: chosen, reason: 'Changed at the front desk' }, { onSuccess: ok('Caddy replaced') })
                      : assign.mutate({ flightId, assignments: [{ caddyId: chosen, playerIds: [pid] }] }, { onSuccess: ok('Caddy assigned') }))} />
                </div>
              </>
            )}
          </div>
        );
      })}
      <ErrorAlert error={assign.error ?? replace.error ?? checkIn.error} />
    </div>
  );
}

/** Caddies and golf carts of a booking, opened from the Check-in page. */
export function CaddyCartModal({ id, onClose }: { id: string; onClose: () => void }) {
  const b = useGet<Booking>(`/api/v1/golf/bookings/${id}`);
  return (
    <Modal open wide onClose={onClose} title={b.data ? `Caddy & Cart · ${String(b.data.code)} · ${String(b.data.localTime)}` : 'Caddy & Cart'}
      actions={<Btn label="Done" kind="primary" onClick={onClose} />}>
      <ErrorAlert error={b.error} />
      {b.data && <CaddyTab b={b.data} />}
    </Modal>
  );
}

// ── player profile (personalised service) ─────────────────────────────────

/** What the desk needs for personalised service: the preferred / favourite
 * caddies and the history with them, favourite food, recent F&B orders,
 * preferences (diet, allergy …), handicap and notes. */
function ProfileTab({ b }: { b: Booking }) {
  const players = b.players.filter((p) => live(p) && p.customerId);
  const [pick, setPick] = useState(String(players[0]?.customerId ?? ''));
  if (!players.length) return <p className="oc-muted">No player of this booking has a customer profile (walk-in guests without contact details).</p>;
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap" role="tablist">
        {players.map((p) => <Btn key={p.id} label={String(p.name)} kind={pick === p.customerId ? 'ink' : 'neutral'} onClick={() => setPick(String(p.customerId))} />)}
      </div>
      {pick && <PlayerProfile customerId={pick} />}
    </div>
  );
}

function PlayerProfile({ customerId }: { customerId: string }) {
  const ctx = useGet<R & { preferences: R[]; highlights: string[] }>(`/api/v1/crm/customers/${customerId}/context`);
  const c360 = useGet<R & { sections: Record<string, R> }>(`/api/v1/crm/customers/${customerId}/360`);
  const caddies = useGet<Page<R>>(`/api/v1/golf/customers/${customerId}/caddies`);
  const orders = useGet<Page<R>>(`/api/v1/commercial/orders${qs({ 'filter[customerId]': customerId, limit: 10 })}`);
  const golf = c360.data?.sections?.golf as R | undefined;
  const pos = c360.data?.sections?.pos as (R & { topProducts?: R[] }) | undefined;
  const prefs = ctx.data?.preferences ?? [];
  const food = prefs.filter((p) => ['food', 'beverage', 'favorite_food', 'diet', 'allergy'].includes(String(p.category)));
  const other = prefs.filter((p) => !food.includes(p) && p.category !== 'favorite_caddy');
  return (
    <div className="oc-stack">
      <ErrorAlert error={ctx.error ?? c360.error} />
      {(ctx.data?.highlights ?? []).length > 0 && (
        <div className="oc-alert oc-alert-info">{(ctx.data?.highlights ?? []).join(' · ')}</div>
      )}
      <div className="oc-row-wrap" style={{ alignItems: 'flex-start' }}>
        <div className="oc-card" style={{ flex: 1, minWidth: 240 }}>
          <strong>Golf</strong>
          <p style={{ margin: '6px 0 0' }}>{String(golf?.rounds ?? 0)} rounds{golf?.handicapIndex ? ` · HI ${String(golf.handicapIndex)}` : ''}
            {golf?.lastRound ? ` · last ${formatDateTime(String(golf.lastRound))}` : ''}</p>
          {ctx.data?.notes ? <p className="oc-small oc-muted">Note: {String(ctx.data.notes)}</p> : null}
        </div>
        <div className="oc-card" style={{ flex: 1, minWidth: 240 }}>
          <strong>Favourite food & drinks</strong>
          <ul style={{ margin: '6px 0 0', paddingLeft: 18 }}>
            {food.map((p) => <li key={String(p.id)}>{String(p.value)} <span className="oc-small oc-muted">({String(p.category).replace(/_/g, ' ')})</span></li>)}
            {(pos?.topProducts ?? []).map((t) => <li key={String(t.productId)}>{String(t.name)} <span className="oc-small oc-muted">× {String(t.quantity)} ordered</span></li>)}
            {!food.length && !(pos?.topProducts ?? []).length && <li className="oc-muted">Nothing recorded yet</li>}
          </ul>
        </div>
      </div>
      <h3 style={{ margin: '8px 0 0' }}>Caddies (preferred, favourite, history)</h3>
      <DataTable rows={withId(caddies.data?.items, 'caddyId')} loading={caddies.isLoading} columns={[{ key: 'code', header: 'Caddy', render: (c) => `${String(c.code)} · ${String(c.name)}` },
        { key: 'favourite', header: 'Favourite', render: (c) => (c.favourite ? '★ favourite' : '') }, { key: 'rounds', header: 'Times together', align: 'right' },
        { key: 'requested', header: 'Asked for', align: 'right' }, { key: 'avgRating', header: 'Rating given', render: (c) => (c.avgRating ? `★ ${String(c.avgRating)}` : '—') },
        { key: 'lastRound', header: 'Last', render: (c) => formatDateTime(String(c.lastRound)) }]} />
      <h3 style={{ margin: '8px 0 0' }}>Recent food & drink orders</h3>
      <DataTable rows={orders.data?.items} loading={orders.isLoading} columns={[{ key: 'createdAt', header: 'When', render: (o) => formatDateTime(String(o.createdAt)) },
        { key: 'outletName', header: 'Outlet' }, { key: 'lines', header: 'Items', render: (o) => ((o.lines as R[] | undefined) ?? []).map((l) => `${String(l.quantity)}× ${String(l.name)}`).join(', ') || String(o.orderNo) },
        { key: 'total', header: 'Total', align: 'right', render: (o) => money(o.total) }]} />
      {other.length > 0 && (
        <>
          <h3 style={{ margin: '8px 0 0' }}>Other preferences</h3>
          <div className="oc-row-wrap">{other.map((p) => <span key={String(p.id)} className="oc-chip">{String(p.category).replace(/_/g, ' ')}: {String(p.value)}</span>)}</div>
        </>
      )}
    </div>
  );
}

// ── actual play time ───────────────────────────────────────────────────────

/** The play time of the booking: first tee-off until the last flight finishes. */
export function BookingPlayTime({ flights }: { flights: R[] }) {
  const live = flights.filter((f) => f.status !== 'cancelled');
  const starts = live.map((f) => f.teeOffAt as string | null).filter(Boolean) as string[];
  if (!starts.length) return null;
  const done = live.every((f) => f.roundFinishAt);
  const end = done ? live.map((f) => String(f.roundFinishAt)).sort().at(-1) : undefined;
  const paused = live.find((f) => f.pausedAt);
  return <PlayTime start={starts.sort()[0]} end={end} pausedAt={paused?.pausedAt as string | undefined} pausedSeconds={Math.max(0, ...live.map((f) => Number(f.pausedSeconds ?? 0)))} />;
}

/** The play time of a player (the flight they play in). */
export function FlightPlayTime({ flights, flightId }: { flights: R[]; flightId: unknown }) {
  const f = flights.find((x) => x.id === flightId);
  if (!f?.teeOffAt) return null;
  return <> · <PlayTime start={f.teeOffAt as string} end={f.roundFinishAt as string | null} pausedAt={f.pausedAt as string | null | undefined} pausedSeconds={Number(f.pausedSeconds ?? 0)} /></>;
}

// ── merged bill ────────────────────────────────────────────────────────────

/** Several bookings paid by one person with one tender. */
function MergeBillModal({ ids, onClose, onDone }: { ids: string[]; onClose: () => void; onDone: () => void }) {
  const [bills, setBills] = useState<R[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [method, setMethod] = useState('cash');
  const [ref, setRef] = useState('');
  const [payer, setPayer] = useState('');
  const pay = useSend<Record<string, unknown>>('POST', '/api/v1/golf/bills:pay-combined', ['/api/v1/golf', '/api/v1/billing']);
  useEffect(() => {
    Promise.all(ids.map((id) => request<R>('GET', `/api/v1/golf/bookings/${id}/bill`))).then(setBills).catch(setError);
  }, [ids]);
  const total = (bills ?? []).reduce((a, b) => a + Math.max(0, Number(b.balance)), 0);
  return (
    <Modal open onClose={onClose} title="Merged bill" actions={<><Btn label="Cancel" onClick={onClose} />
      <Btn label={`Receive ${money(total)}`} kind="primary" disabled={!bills || total <= 0 || pay.isPending}
        onClick={() => pay.mutate({ bookingIds: ids, methodType: method, reference: ref || undefined, payerName: payer || undefined }, { onSuccess: onDone })} /></>}>
      <div className="oc-stack">
        <ErrorAlert error={error} />
        <DataTable rows={withId(bills ?? [], 'bookingId')} loading={!bills && !error} columns={[{ key: 'code', header: 'Booking' },
          { key: 'localTime', header: 'Tee Time' }, { key: 'contactName', header: 'Booked by' },
          { key: 'balance', header: 'Balance', align: 'right', render: (b) => money(b.balance) }]} />
        <p style={{ margin: 0 }}>Total <strong>{money(total)}</strong></p>
        <div className="oc-row-wrap">
          <TextField label="Payer name" value={payer} onChange={setPayer} />
          <SelectField label="Method" value={method} onChange={setMethod} options={METHODS} />
          {method !== 'cash' && <TextField label="Reference" value={ref} onChange={setRef} />}
        </div>
        <ErrorAlert error={pay.error} />
      </div>
    </Modal>
  );
}
