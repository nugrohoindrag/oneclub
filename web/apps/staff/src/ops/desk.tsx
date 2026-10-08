import React, { useEffect, useState } from 'react';
import { Link, useNavigate } from 'react-router';
import { qs, request, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDateTime } from '@oneclub/i18n';
import { Checkbox, DataTable, ErrorAlert, Icon, Modal, SelectField, StatusPill, TextField, useToast } from '@oneclub/shell';
import { BookingForm } from '../p1/golf';
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
        { key: 'status', header: 'Status', render: (b) => <StatusPill status={String(b.status).replace(/_/g, '-')} /> }]}
        actions={(b) => (bookings.offline ? null : merge
          ? <Checkbox label="Merge" checked={merge.includes(b.id)} disabled={['cancelled', 'no_show'].includes(String(b.status))} onChange={(on) => toggle(b.id, on)} />
          : <Btn label="Manage" onClick={() => setOpen(b.id)} />)} />
      {open && <DeskBookingModal id={open} onClose={() => { setOpen(null); void bookings.refetch(); }} />}
      {paying && merge && <MergeBillModal ids={merge} onClose={() => setPaying(false)}
        onDone={() => { setPaying(false); setMerge(null); toast('Merged bill paid'); void bookings.refetch(); }} />}
    </div>
  );
}

/** New booking at the desk: walk-in (no account needed) or by phone. */
export function DeskNewBookingPage() {
  const nav = useNavigate();
  const [date, setDate] = useState(today());
  const courses = useGet<Page<R>>('/api/v1/golf/courses?filter[status]=active&limit=50');
  const [courseId, setCourse] = useState('');
  const course = courseId || courses.data?.items[0]?.id || '';
  const [walkIn, setWalkIn] = useState(true);
  const slots = useGet<Page<R>>(course ? `/api/v1/golf/tee-times${qs({ courseId: course, date })}` : null);
  const [slot, setSlot] = useState<R | null>(null);
  const open = (slots.data?.items ?? []).filter((s) => Number(s.remaining) > 0 && s.status !== 'blocked');
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
      {slots.data && open.length === 0 && <p className="oc-muted">No tee time with free places on this date.</p>}
      <div className="oc-row-wrap">
        {open.map((s) => (
          <button key={s.id} type="button" className="oc-btn oc-btn-neutral" style={{ minHeight: 56, minWidth: 104, flexDirection: 'column' }} onClick={() => setSlot(s)}>
            <strong>{String(s.localTime)}</strong>
            <span className="oc-small">{String(s.remaining)} left{Number(s.startTee) > 1 ? ` · T${String(s.startTee)}` : ''}</span>
          </button>
        ))}
      </div>
      {slot && <BookingForm key={`${slot.id}-${walkIn}`} slot={slot} channel={walkIn ? 'walk_in' : 'back_office'} onClose={() => setSlot(null)}
        onDone={() => nav('/ops/front-desk')} />}
    </div>
  );
}

// ── one booking at the desk ────────────────────────────────────────────────

const TABS = [['bill', 'Bill'], ['time', 'Tee Time'], ['players', 'Players'], ['caddy', 'Caddy']] as const;
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
            <span className="oc-muted">{String(x.playDate)} · {String(x.courseName)} · {x.players.filter(live).length} players · payment {String(x.paymentMode ?? '—').replace(/_/g, ' ')}</span>
          </div>
          <div className="oc-row-wrap" role="tablist">
            {TABS.map(([k, l]) => <Btn key={k} label={l} kind={tab === k ? 'ink' : 'neutral'} onClick={() => setTab(k)} />)}
          </div>
          {tab === 'bill' && <BillTab id={id} />}
          {tab === 'time' && <TimeTab b={x} onDone={() => void b.refetch()} />}
          {tab === 'players' && <PlayersTab b={x} onDone={() => void b.refetch()} />}
          {tab === 'caddy' && <CaddyTab b={x} />}
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
  const free = (slots.data?.items ?? []).filter((s) => s.id !== b.teeTimeId && s.status !== 'blocked' && Number(s.remaining) >= n);
  return (
    <div className="oc-stack">
      <p style={{ margin: 0 }}>Now {String(b.playDate)} {String(b.localTime)}. Late or early? Move the flight to the next free tee time.</p>
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <TextField label="Date" type="date" value={date} onChange={(v) => { setDate(v); setSlot(''); }} />
        <SelectField label="New tee time" value={slot} onChange={setSlot} placeholder="Choose"
          options={free.map((s) => ({ value: s.id, label: `${String(s.localTime)}${Number(s.startTee) > 1 ? ` · tee ${String(s.startTee)}` : ''} · ${String(s.remaining)} free` }))} />
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
                <span className="oc-small oc-muted"> · {String(p.playerType).replace(/_/g, ' ')} · {String(p.status).replace(/_/g, ' ')}</span></span>
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

/** The caddy is in the rate: the desk picks one per player (or from the queue). */
function CaddyTab({ b }: { b: Booking }) {
  return (
    <div className="oc-stack">
      {b.flights.map((f) => <FlightCaddies key={String(f.id)} flightId={String(f.id)} date={b.playDate} players={b.players.filter(live)} />)}
    </div>
  );
}

function FlightCaddies({ flightId, date, players }: { flightId: string; date: string; players: R[] }) {
  const toast = useToast();
  const list = useGet<Page<R>>(`/api/v1/golf/caddy-assignments${qs({ flightId })}`);
  const board = useGet<Page<R>>(`/api/v1/golf/caddy-availability${qs({ date })}`);
  const assign = useSend<Record<string, unknown>>('POST', '/api/v1/golf/caddy-assignments', ['/api/v1/golf']);
  const replace = useSend<Record<string, unknown>>('POST', (v) => `/api/v1/golf/caddy-assignments/${String(v.id)}:replace`, ['/api/v1/golf']);
  const [pick, setPick] = useState<Record<string, string>>({});
  const current = (list.data?.items ?? []).filter((a) => !['cancelled', 'replaced'].includes(String(a.status)));
  const free = (board.data?.items ?? []).filter((c) => c.status === 'available').map((c) => ({ value: String(c.caddyId), label: `${String(c.code)} · ${String(c.name)}` }));
  const of = (pid: string) => current.find((a) => ((a.playerIds as string[] | undefined) ?? []).includes(pid));
  const ok = () => { toast('Caddy assigned'); setPick({}); void list.refetch(); void board.refetch(); };
  return (
    <div className="oc-stack">
      {players.map((p) => {
        const a = of(p.id);
        return (
          <div key={p.id} className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
            <span style={{ minWidth: 200 }}><strong>{String(p.name || 'Guest (TBA)')}</strong><br />
              <span className="oc-small oc-muted">{a ? `Caddy ${String(a.caddyCode)} · ${String(a.caddyName)}` : 'No caddy yet'}</span></span>
            <SelectField label={a ? 'Replace with' : 'Caddy'} value={pick[p.id] ?? ''} onChange={(v) => setPick({ ...pick, [p.id]: v })} placeholder="Choose" options={free} />
            <Btn label={a ? 'Replace' : 'Assign'} kind="primary" disabled={!pick[p.id] || assign.isPending || replace.isPending}
              onClick={() => (a
                ? replace.mutate({ id: a.id, caddyId: pick[p.id], reason: 'Changed at the front desk' }, { onSuccess: ok })
                : assign.mutate({ flightId, assignments: [{ caddyId: pick[p.id], playerIds: [p.id] }] }, { onSuccess: ok }))} />
          </div>
        );
      })}
      <div><Btn label="Assign the rest from the queue" disabled={assign.isPending || players.every((p) => of(p.id))}
        onClick={() => assign.mutate({ flightId, auto: true }, { onSuccess: ok })} /></div>
      <ErrorAlert error={assign.error ?? replace.error} />
    </div>
  );
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
