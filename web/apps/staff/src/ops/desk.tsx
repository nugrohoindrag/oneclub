import React, { useEffect, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router';
import { getActiveProperty, qs, request, useGet, useSend, uuidv7, type Page, type Schemas } from '@oneclub/api-client';
import { formatDateTime } from '@oneclub/i18n';
import { Checkbox, CrowdLabel, DataTable, ErrorAlert, Icon, Modal, PlayTime, SelectField, StatusPill, TEE_CATEGORY, TeeBadge, TextField, crowdClass, useToast } from '@oneclub/shell';
import { BookingForm, type BookingPrefill } from '../p1/golf';
import { DeskPayDialog, newPayments, type DeskMember, type DeskTender } from './deskpay';
import { Btn, Head, money, today, useCached } from './golf';

// Front Desk (demo feedback 9 Oct 2026): players book at the desk with or
// without an account, the desk moves tee times first come first served,
// edits the players, assigns the caddies (the caddy is in the rate) and
// takes the payment at the start, at the end or in part — for everyone,
// split per player or merged with other bookings into one bill.

type R = Record<string, unknown> & { id: string };
type Booking = R & { players: R[]; flights: R[]; folio?: R; teeTimeId: string; courseId: string; playDate: string };

const idem = () => ({ 'Idempotency-Key': uuidv7() });
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
  // checked in from the Member App or the kiosk: the desk picks caddy and cart
  const selfs = useGet<Page<R>>(`/api/v1/golf/self-check-ins${qs({ date })}`, { refetchInterval: 20_000 });
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
      {(selfs.data?.items ?? []).length > 0 && (
        <div className="oc-card oc-stack" style={{ gap: 8 }}>
          <strong><Icon name="how_to_reg" size={18} /> Self check-in · waiting for caddy & golf cart</strong>
          {(selfs.data?.items ?? []).map((s) => (
            <div key={String(s.bookingId)} className="oc-row-wrap" style={{ alignItems: 'center' }}>
              <strong style={{ minWidth: 60 }}>{String(s.localTime)}</strong>
              <span style={{ flex: 1, minWidth: 200 }}>{String(s.code)} · {((s.players as string[]) ?? []).join(', ')}
                <span className="oc-small oc-muted"> · {s.method === 'kiosk' ? 'kiosk' : 'Member App'} {new Date(String(s.checkedInAt)).toLocaleTimeString('en-GB', { timeStyle: 'short' })}
                  {Number(s.waitingCaddy) > 0 ? ` · ${String(s.waitingCaddy)} without caddy` : ''}{s.waitingCart ? ' · no golf cart' : ''}</span></span>
              <Btn label="Caddy & Cart" kind="primary" onClick={() => setOpen(String(s.bookingId))} />
            </div>
          ))}
        </div>
      )}
      {open && <DeskBookingModal id={open} onClose={() => { setOpen(null); void bookings.refetch(); void selfs.refetch(); }} />}
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
  // only the areas switched on at the Driving Range page (and with bays)
  const areas = useGet<Page<Schemas['RangeArea']>>('/api/v1/golf/range-areas');
  const open: string[] = (areas.data?.items ?? []).filter((a) => a.offered).map((a) => a.area);
  const [chosenArea, setArea] = useState('');
  const area = open.includes(chosenArea) ? chosenArea : (open[0] ?? '');
  const [minutes, setMinutes] = useState('60');
  const [bay, setBay] = useState(true);
  const [time, setTime] = useState('');
  const [bayId, setBayId] = useState('');
  const [guest, setGuest] = useState({ name: '', phone: '', players: '1' });
  const slots = useGet<Page<R>>(area ? `/api/v1/golf/range-availability${qs({ date, area, minutes })}` : null);
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
        {open.length > 1 && <SelectField label="Area" value={area} onChange={(v) => { setArea(v); setTime(''); }}
          options={open.map((a) => ({ value: a, label: a === 'outdoor' ? 'Outdoor' : 'Indoor' }))} />}
        <SelectField label="Length" value={minutes} onChange={(v) => { setMinutes(v); setTime(''); }}
          options={['30', '60', '90', '120'].map((m) => ({ value: m, label: `${m} min` }))} />
      </div>
      <ErrorAlert error={slots.error ?? areas.error} />
      {areas.data && open.length === 0 && (
        <div className="oc-alert oc-alert-warning">No driving range area is open for booking. Switch one on at <Link to="/ops/driving-range">Driving Range</Link>.</div>
      )}
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

// After the guests arrive the desk checks them in and picks caddies and carts;
// the payment mostly comes at check-out.
const TABS = [['bill', 'Bill'], ['caddy', 'Caddy & Cart'], ['time', 'Tee Time'], ['players', 'Players'], ['profile', 'Profile']] as const;
type Tab = (typeof TABS)[number][0];
const CLOSED = ['completed', 'cancelled', 'no_show'];

function DeskBookingModal({ id, onClose }: { id: string; onClose: () => void }) {
  const b = useGet<Booking>(`/api/v1/golf/bookings/${id}`);
  const sugg = useGet<Page<R>>(`/api/v1/golf/bookings/${id}/caddy-suggestions`);
  const [picked, setTab] = useState<Tab | null>(null);
  const x = b.data;
  // opens on Caddy & Cart while a player still waits for check-in or a caddy
  const waiting = (sugg.data?.items ?? []).some((p) => p.checkedIn !== true || !p.current);
  const tab: Tab | null = picked ?? (!x || (!sugg.data && !sugg.error) ? null : !CLOSED.includes(String(x.status)) && waiting ? 'caddy' : 'bill');
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
          {tab === 'bill' && <BillTab id={id} members={x.players.filter((p) => live(p) && p.playerType === 'member' && p.customerId)
            .map((p) => ({ customerId: String(p.customerId), name: String(p.name) }))} />}
          {tab === 'time' && <TimeTab b={x} onDone={() => void b.refetch()} />}
          {tab === 'players' && <PlayersTab b={x} onDone={() => void b.refetch()} />}
          {tab === 'caddy' && <CaddyTab b={x} />}
          {tab === 'profile' && <ProfileTab b={x} />}
        </div>
      )}
    </Modal>
  );
}

type Bill = R & { code: string; players: R[]; lines: R[]; payments: R[] };

/** Pay all, any part, or per player (split bill: more than one player); the
 * payment itself runs on the payment page (method, confirm, receipt). */
function BillTab({ id, members }: { id: string; members: DeskMember[] }) {
  const bill = useGet<Bill>(`/api/v1/golf/bookings/${id}/bill`);
  const pay = useSend<Record<string, unknown>, Bill>('POST', `/api/v1/golf/bookings/${id}/bill:pay`, ['/api/v1/golf', '/api/v1/billing'], idem);
  const toast = useToast();
  const [split, setSplit] = useState(false);
  const [picked, setPicked] = useState<string[]>([]);
  const [amount, setAmount] = useState('');
  const [payer, setPayer] = useState('');
  const [paying, setPaying] = useState(false);
  const x = bill.data;
  if (!x) return <ErrorAlert error={bill.error} />;
  const balance = Number(x.balance);
  const players = x.players ?? [];
  const pickedDue = players.filter((p) => picked.includes(String(p.playerId))).reduce((a, p) => a + Number(p.due), 0);
  const n = amount === '' ? balance : Number(amount);
  const done = () => { setPaying(false); toast('Payment recorded'); setPicked([]); setAmount(''); void bill.refetch(); };
  const go = async (t: DeskTender) => {
    const after = await pay.mutateAsync(split ? { playerIds: picked, ...t } : { amount: amount || undefined, payerName: payer || undefined, ...t });
    return newPayments(x.payments, after.payments);
  };
  const who = split ? players.filter((p) => picked.includes(String(p.playerId))).map((p) => String(p.name)).join(', ') : payer;
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
          <div><Btn label={`Pay ${money(split ? pickedDue : n)}`} kind="primary" onClick={() => setPaying(true)}
            disabled={split ? picked.length === 0 : !(n > 0 && n <= balance)} /></div>
          {paying && <DeskPayDialog amount={split ? pickedDue : n} members={members} onClose={() => setPaying(false)} pay={go} onFinish={done}
            summary={[['Booking', `${x.code} · ${String(x.localTime)}`], [split ? 'Players' : 'Payer', who || String(x.contactName ?? '—')],
              ...(n < balance && !split ? [['Left on the bill', money(balance - n)] as [string, string]] : [])]} />}
          <p className="oc-small oc-muted" style={{ margin: 0 }}>
            The rest can be paid later — here, at check-out, or by the member in the app. On-course F&B is added from the POS (Golfer Bill).
          </p>
        </>
      )}
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

/** Complete, edit, add or remove players: one row per player — name, tee,
 * actions — and the add form in the same columns. */
function PlayersTab({ b, onDone }: { b: Booking; onDone: () => void }) {
  const toast = useToast();
  const [edit, setEdit] = useState<Record<string, { name: string; phone: string }>>({});
  const [removing, setRemoving] = useState<string | null>(null);
  const [add, setAdd] = useState({ playerType: 'non_member', memberNo: '', name: '', phone: '' });
  const inv = ['/api/v1/golf'];
  const patch = useSend<Record<string, unknown>>('PATCH', (v) => `/api/v1/golf/bookings/${b.id}/players/${String(v.playerId)}`, inv);
  const remove = useSend<Record<string, unknown>>('DELETE', (v) => `/api/v1/golf/bookings/${b.id}/players/${String(v.playerId)}`, inv);
  const create = useSend<Record<string, unknown>>('POST', `/api/v1/golf/bookings/${b.id}/players`, inv);
  const tees = useGet<Page<R>>(`/api/v1/golf/tee-sets${qs({ 'filter[courseId]': b.courseId, 'filter[status]': 'active', limit: 20 })}`);
  const teeOptions = (tees.data?.items ?? []).map((t) => ({ value: t.id, label: `${String(t.name)}${t.playerCategory ? ` · ${TEE_CATEGORY[String(t.playerCategory)] ?? String(t.playerCategory)}` : ''}` }));
  const players = b.players.filter(live);
  const ok = () => { toast('Players updated'); setEdit({}); setRemoving(null); onDone(); };
  const member = add.playerType === 'member';
  return (
    <div className="oc-stack">
      <div className="oc-table-wrap">
        <table className="oc-table desk-players">
          <thead><tr><th>Player</th><th>Tee</th><th className="desk-players-actions" aria-label="Actions" /></tr></thead>
          <tbody>
            {players.map((p) => {
              const e = edit[p.id];
              const who = String(p.name || 'guest');
              return (
                <tr key={p.id}>
                  <td>
                    {e ? (
                      <div className="desk-players-fields">
                        <input className="oc-input" aria-label="Name" placeholder="Name" value={e.name} onChange={(v) => setEdit({ [p.id]: { ...e, name: v.target.value } })} />
                        <input className="oc-input" aria-label="Phone" placeholder="Phone" value={e.phone} onChange={(v) => setEdit({ [p.id]: { ...e, phone: v.target.value } })} />
                      </div>
                    ) : (
                      <><strong>{String(p.name || 'Guest (TBA)')}</strong>
                        <div className="oc-small oc-muted">{String(p.playerType).replace(/_/g, ' ')} · {String(p.status).replace(/_/g, ' ')}<FlightPlayTime flights={b.flights} flightId={p.flightId} /></div></>
                    )}
                  </td>
                  <td>
                    {teeOptions.length > 0 ? (
                      <select className="oc-select" aria-label={`Tee of ${who}`} value={String(p.teeSetId ?? '')} disabled={patch.isPending}
                        onChange={(v) => { if (v.target.value) patch.mutate({ playerId: p.id, teeSetId: v.target.value }, { onSuccess: ok }); }}>
                        <option value="">By player category</option>
                        {teeOptions.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
                      </select>
                    ) : <span className="oc-muted">—</span>}
                  </td>
                  <td className="desk-players-actions">
                    <div className="desk-players-buttons">
                      {e ? (
                        <>
                          <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setEdit({})}>Cancel</button>
                          <button className="oc-btn oc-btn-primary oc-btn-sm" disabled={patch.isPending || !e.name.trim()}
                            onClick={() => patch.mutate({ playerId: p.id, name: e.name, phone: e.phone || undefined }, { onSuccess: ok })}>Save</button>
                        </>
                      ) : removing === p.id ? (
                        <>
                          <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setRemoving(null)}>Keep</button>
                          <button className="oc-btn oc-btn-danger oc-btn-sm" disabled={remove.isPending} onClick={() => remove.mutate({ playerId: p.id }, { onSuccess: ok })}>Remove {who}?</button>
                        </>
                      ) : (
                        <>
                          {p.playerType !== 'member'
                            ? <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setEdit({ [p.id]: { name: String(p.name ?? ''), phone: String(p.phone ?? '') } })}>Edit</button>
                            : <span className="desk-players-slot" aria-hidden="true" />}
                          {p.status === 'booked' && players.length > 1
                            ? <button className="oc-btn oc-btn-outline oc-btn-sm" aria-label={`Remove ${who}`} onClick={() => setRemoving(p.id)}>Remove</button>
                            : <span className="desk-players-slot" aria-hidden="true" />}
                        </>
                      )}
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
          <tbody className="desk-players-add">
            <tr><td colSpan={3}><strong>Add player</strong></td></tr>
            <tr>
              <td>
                <div className="desk-players-fields">
                  {member
                    ? <input className="oc-input" aria-label="Member No." placeholder="Member No." value={add.memberNo} onChange={(v) => setAdd({ ...add, memberNo: v.target.value })} />
                    : <><input className="oc-input" aria-label="Name" placeholder="Name" value={add.name} onChange={(v) => setAdd({ ...add, name: v.target.value })} />
                      <input className="oc-input" aria-label="Phone" placeholder="Phone" value={add.phone} onChange={(v) => setAdd({ ...add, phone: v.target.value })} /></>}
                </div>
              </td>
              <td>
                <select className="oc-select" aria-label="Player type" value={add.playerType} onChange={(v) => setAdd({ ...add, playerType: v.target.value })}>
                  <option value="non_member">Non-member</option><option value="guest_of_member">Guest of member</option><option value="member">Member</option>
                </select>
              </td>
              <td className="desk-players-actions">
                <div className="desk-players-buttons">
                  <button className="oc-btn oc-btn-primary oc-btn-sm" disabled={create.isPending || (member ? !add.memberNo : !add.name)}
                    onClick={() => create.mutate({ player: { playerType: add.playerType, memberNo: member ? add.memberNo : undefined,
                      name: member ? undefined : add.name, phone: add.phone || undefined } },
                    { onSuccess: () => { setAdd({ playerType: 'non_member', memberNo: '', name: '', phone: '' }); ok(); } })}>Add</button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <ErrorAlert error={patch.error ?? remove.error ?? create.error} />
      <BookingHoleBests id={b.id} />
      <BookingScorecards bookingId={b.id} />
    </div>
  );
}

/** The players' scorecards after the round: printed (PDF) at the desk or
 * shared as a link — by e-mail / WhatsApp, or copied — so guests without
 * the Member App get theirs too. */
export function BookingScorecards({ bookingId }: { bookingId: string }) {
  const toast = useToast();
  const cards = useGet<Page<R>>(`/api/v1/golf/bookings/${bookingId}/scorecards`);
  const share = useSend<Record<string, unknown>, R>('POST', (v) => `/api/v1/golf/scorecards/${String(v.id)}:share`, [`/api/v1/golf/bookings/${bookingId}/scorecards`]);
  const [open, setOpen] = useState<R | null>(null);
  const [to, setTo] = useState({ email: '', phone: '' });
  const [link, setLink] = useState('');
  const items = cards.data?.items ?? [];
  if (!items.length) return null;
  const pdf = (id: string) => `/api/v1/golf/scorecards/${id}/pdf${qs({ propertyId: getActiveProperty() })}`;
  const num = (v: unknown) => (v === null || v === undefined ? '—' : String(v));
  const wa = digits(to.phone).replace(/^0/, '62');
  const pick = (s: R) => { setOpen(s); setLink(''); setTo({ email: String(s.email ?? ''), phone: String(s.phone ?? '') }); };
  return (
    <>
      <h3 style={{ margin: '8px 0 0' }}>Scorecards</h3>
      <DataTable rows={items} columns={[{ key: 'playerName', header: 'Player' }, { key: 'teeSetName', header: 'Tee', render: (s) => num(s.teeSetName) },
        { key: 'gross', header: 'Gross', align: 'right', render: (s) => num(s.gross) }, { key: 'courseHandicap', header: 'Course HCP', align: 'right', render: (s) => num(s.courseHandicap) },
        { key: 'net', header: 'Net', align: 'right', render: (s) => <strong>{num(s.net)}</strong> }, { key: 'caddyName', header: 'Caddy', render: (s) => num(s.caddyName) },
        { key: 'status', header: 'Card', render: (s) => <StatusPill status={String(s.status)} /> }]}
        actions={(s) => (
          <div className="oc-row">
            <a className="oc-btn oc-btn-neutral oc-btn-sm" href={pdf(s.id)} target="_blank" rel="noreferrer"><Icon name="print" size={18} /> Print</a>
            <button className="oc-btn oc-btn-outline oc-btn-sm" onClick={() => pick(s)}><Icon name="share" size={18} /> Share</button>
          </div>
        )} />
      {open && (
        <div className="oc-card oc-stack">
          <strong>Share the scorecard of {String(open.playerName)}</strong>
          <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
            <TextField label="E-mail" value={to.email} onChange={(v) => setTo({ ...to, email: v })} />
            <TextField label="WhatsApp" value={to.phone} onChange={(v) => setTo({ ...to, phone: v })} inputMode="tel" />
            <Btn label="Send" kind="primary" disabled={share.isPending || (!to.email && !to.phone)}
              onClick={() => share.mutate({ id: open.id, send: true, email: to.email || undefined, phone: to.phone || undefined },
                { onSuccess: (r) => { setLink(String(r.link)); toast(`Scorecard sent to ${((r.sentTo as string[]) ?? []).join(', ')}`); } })} />
            <Btn label="Get link" disabled={share.isPending} onClick={() => share.mutate({ id: open.id }, { onSuccess: (r) => setLink(String(r.link)) })} />
            <Btn label="Close" onClick={() => setOpen(null)} />
          </div>
          {link && (
            <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
              <code className="oc-code" style={{ wordBreak: 'break-all' }}>{link}</code>
              <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => void navigator.clipboard?.writeText(link).then(() => toast('Link copied'))}>Copy</button>
              {wa && <a className="oc-btn oc-btn-neutral oc-btn-sm" target="_blank" rel="noreferrer"
                href={`https://wa.me/${wa}?text=${encodeURIComponent(`Your scorecard of ${String(open.playedOn ?? '')}: ${link}`)}`}>Open WhatsApp</a>}
            </div>
          )}
          <span className="oc-small oc-muted">The link opens the PDF without an account. Guests without a Member App account get theirs this way.</span>
          <ErrorAlert error={share.error} />
        </div>
      )}
      <ErrorAlert error={cards.error} />
    </>
  );
}

/** Best score per hole among the players of the booking. */
function BookingHoleBests({ id }: { id: string }) {
  const d = useGet<Page<R>>(`/api/v1/golf/bookings/${id}/hole-bests`);
  if (!(d.data?.items ?? []).length) return null;
  return (
    <>
      <h3 style={{ margin: '8px 0 0' }}>Best per hole</h3>
      <DataTable rows={withId(d.data?.items, 'seq')} columns={[{ key: 'hole', header: 'Hole', render: (h) => `${String(h.sectionCode)}-${String(h.holeNumber)} · par ${String(h.par)}` },
        { key: 'strokes', header: 'Best', align: 'right' }, { key: 'players', header: 'Player', render: (h) => ((h.players as string[]) ?? []).join(', ') }]} />
    </>
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
  // only a caddy who is present today and has no job can be picked
  const free = (board.data?.items ?? []).filter((c) => c.status === 'available').map((c) => ({ value: String(c.caddyId), label: `${String(c.code)} · ${String(c.name)}` }));
  const ok = (what: string) => () => { toast(what); setPick({}); void sugg.refetch(); void board.refetch(); };
  const REASON: Record<string, string> = { requested: 'asked for', favourite: 'favourite', usual: 'usual caddy', queue: 'next in queue' };
  const onBoard = new Map((board.data?.items ?? []).map((c) => [String(c.caddyId), c]));
  /** Why a known caddy cannot be picked: away today, or carrying a flight. */
  const why = (caddyId: unknown) => {
    const c = onBoard.get(String(caddyId));
    if (c && ['assigned', 'in_play'].includes(String(c.status))) return `on duty${c.teeTime ? ` · flight ${String(c.teeTime)}` : ''}`;
    return c?.attendance === 'present' ? 'not available' : 'not present';
  };
  const someoneWaits = players.some((p) => p.checkedIn === true && !p.current);
  return (
    <div className="oc-stack">
      <h3 style={{ margin: '8px 0 0' }}>Caddies <span className="oc-small oc-muted">· 1 caddy per player, assigned after check-in</span></h3>
      <ErrorAlert error={sugg.error ?? board.error} />
      {board.data && free.length === 0 && someoneWaits && (
        <div className="oc-alert oc-alert-warning">
          No caddy is present and free today — record the attendance first. <Link to="/ops/caddy/availability">Caddy Master → Caddy Availability</Link>
        </div>
      )}
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
                        onClick={() => setPick({ ...pick, [pid]: String(c.caddyId) })}>
                        {String(c.code)} · {String(c.name)} · {REASON[String(c.reason)] ?? ''}{Number(c.rounds) > 0 ? ` (${String(c.rounds)} rounds)` : ''}{c.available ? '' : ` · ${why(c.caddyId)}`}
                      </button>
                    ))}
                  </div>
                )}
                <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
                  <SelectField label={current ? 'Replace with' : rec ? `Caddy (recommended: ${String(rec.code)}, ${REASON[String(rec.reason)] ?? ''})` : 'Caddy'}
                    value={chosen} onChange={(v) => setPick({ ...pick, [pid]: v })} placeholder={free.length ? 'Choose' : 'No caddy present yet'} options={free} />
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
  const [bills, setBills] = useState<Bill[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [payer, setPayer] = useState('');
  const [paying, setPaying] = useState(false);
  const pay = useSend<Record<string, unknown>, Page<Bill>>('POST', '/api/v1/golf/bills:pay-combined', ['/api/v1/golf', '/api/v1/billing'], idem);
  useEffect(() => {
    Promise.all(ids.map((id) => request<Bill>('GET', `/api/v1/golf/bookings/${id}/bill`))).then(setBills).catch(setError);
  }, [ids]);
  const total = (bills ?? []).reduce((a, b) => a + Math.max(0, Number(b.balance)), 0);
  const go = async (t: DeskTender) => {
    const after = await pay.mutateAsync({ bookingIds: ids, payerName: payer || undefined, ...t });
    return after.items.flatMap((b) => newPayments(bills?.find((x) => x.bookingId === b.bookingId)?.payments, b.payments));
  };
  return (
    <Modal open onClose={onClose} title="Merged bill" actions={<><Btn label="Cancel" onClick={onClose} />
      <Btn label={`Pay ${money(total)}`} kind="primary" disabled={!bills || total <= 0} onClick={() => setPaying(true)} /></>}>
      <div className="oc-stack">
        <ErrorAlert error={error} />
        <DataTable rows={withId(bills ?? [], 'bookingId')} loading={!bills && !error} columns={[{ key: 'code', header: 'Booking' },
          { key: 'localTime', header: 'Tee Time' }, { key: 'contactName', header: 'Booked by' },
          { key: 'balance', header: 'Balance', align: 'right', render: (b) => money(b.balance) }]} />
        <p style={{ margin: 0 }}>Total <strong>{money(total)}</strong></p>
        <div className="oc-row-wrap">
          <TextField label="Payer name" value={payer} onChange={setPayer} />
        </div>
        {paying && <DeskPayDialog amount={total} methods={['cash', 'card', 'qris', 'bank_transfer']} onClose={() => setPaying(false)} pay={go} onFinish={onDone}
          summary={[['Bookings', (bills ?? []).map((b) => b.code).join(', ')], ['Payer', payer || '—']]} />}
      </div>
    </Modal>
  );
}
