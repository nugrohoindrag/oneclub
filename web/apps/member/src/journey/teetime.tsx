import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Link, useNavigate } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { qs, request, useGet, uuidv7, type Page, type Schemas } from '@oneclub/api-client';
import { ErrorAlert, Icon, QRCode, SelectField, Skeleton, TextField, useAuth, useDebounced } from '@oneclub/shell';
import { MethodPicker, PaymentPanel, type PayMethod } from './pay';
import { Check, Chip, dayLabel, downloadICS, Head, initials, isoDay, money, Rows, Steps } from './ui';

// Book Tee Time (member journey §3–4): date & time → players (me, member,
// guest) → review with the fee estimate and golf carts → payment (member
// account, pay in full or part online, or pay at the end) → booking
// confirmed with the check-in QR. The caddy is part of the rate and the
// front desk assigns one per player (demo feedback 9 Oct 2026).

type Slot = Schemas['AvailableSlot'];
type Quote = Schemas['MemberQuote'];
type Booking = Schemas['Booking'];

interface Player {
  kind: 'self' | 'member' | 'guest';
  memberId?: string;
  memberNo?: string;
  name: string;
  phone: string;
}

/** When the booking is paid: member account, in full now, part now or at the end. */
type PayWhen = 'account' | 'now' | 'part' | 'later';
type OnlineMethod = Exclude<PayMethod, 'member_charge'>;

const STEPS = ['Date & Time', 'Players', 'Review', 'Payment', 'Confirmed'];
const SESSIONS = [['', 'All day'], ['morning', 'Morning'], ['afternoon', 'Afternoon'], ['night', 'Night Golf']];

export function TeeTimeWizard() {
  const { me } = useAuth();
  const nav = useNavigate();
  const qc = useQueryClient();
  const [step, setStep] = useState(0);
  const [date, setDate] = useState(isoDay(1));
  const [session, setSession] = useState('');
  const courses = useGet<Page<Schemas['CourseInfo']>>('/api/v1/member/golf/courses');
  const [courseId, setCourse] = useState('');
  // until the member picks a course, the first course with tee times that day
  const [autoIdx, setAutoIdx] = useState(0);
  const course = courseId || courses.data?.items[autoIdx]?.id || '';
  const avail = useGet<Page<Slot>>(course ? `/api/v1/member/golf/availability${qs({ courseId: course, date, session })}` : null);
  useEffect(() => {
    if (!courseId && avail.data && avail.data.items.length === 0 && autoIdx < (courses.data?.items.length ?? 0) - 1) setAutoIdx(autoIdx + 1);
  }, [avail.data, courseId, autoIdx, courses.data]);
  const [slot, setSlot] = useState<Slot | null>(null);
  const [count, setCount] = useState(4);
  const [hold, setHold] = useState<Schemas['Hold'] | null>(null);
  const [players, setPlayers] = useState<Player[]>([]);
  const [carts, setCarts] = useState('');
  const [when, setWhen] = useState<PayWhen>('account');
  const [method, setMethod] = useState<OnlineMethod>('qris');
  const [part, setPart] = useState('');
  const [notes, setNotes] = useState('');
  const [booking, setBooking] = useState<Booking | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const quote = useGet<Quote>(slot && step >= 2 ? `/api/v1/member/golf/quote?teeTimeId=${slot.id}` : null);
  const est = estimate(quote.data, players);

  // release the hold when the member leaves before booking
  const pending = useRef<{ hold: Schemas['Hold'] | null; booked: boolean }>({ hold: null, booked: false });
  pending.current = { hold, booked: !!booking };
  useEffect(() => () => {
    const { hold: h, booked } = pending.current;
    if (h && !booked) void request('DELETE', `/api/v1/member/golf/holds/${h.id}`).catch(() => undefined);
  }, []);

  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  const holdSlot = () => run(async () => {
    if (!slot) return;
    if (hold && hold.teeTimeId === slot.id && hold.players === count) {
      setStep(1);
      return;
    }
    if (hold) await request('DELETE', `/api/v1/member/golf/holds/${hold.id}`).catch(() => undefined);
    const h = await request<Schemas['Hold']>('POST', '/api/v1/member/golf/holds', { teeTimeId: slot.id, players: count, channel: 'member_app' });
    setHold(h);
    setPlayers((ps) => Array.from({ length: count }, (_, i) => ps[i] ?? (i === 0
      ? { kind: 'self', name: me?.fullName ?? 'Me', phone: '' }
      : { kind: 'guest', name: '', phone: '' })));
    setStep(1);
  });

  const confirm = () => run(async () => {
    if (!hold) return;
    const online = when === 'now' || when === 'part';
    const body = {
      bookingType: 'member', holdId: hold.id,
      paymentMode: ({ account: 'member_charge', now: 'prepaid', part: 'deposit', later: 'pay_at_venue' } as const)[when],
      paymentMethod: online ? method : undefined,
      depositAmount: when === 'part' ? part : undefined,
      golfCartRequest: carts === '' ? undefined : Number(carts),
      notes: notes || undefined,
      players: players.map((p) => (p.kind === 'self' ? { playerType: 'member' }
        : p.kind === 'member' ? { playerType: 'member', memberId: p.memberId, memberNo: p.memberId ? undefined : p.memberNo }
          : { playerType: 'guest_of_member', name: p.name || undefined, phone: p.phone || undefined, tba: !p.name, hostIndex: 0 })),
    };
    const b = await request<Booking>('POST', '/api/v1/member/golf/bookings', body, { 'Idempotency-Key': uuidv7() });
    setBooking(b);
    void qc.invalidateQueries({ queryKey: ['/api/v1/member/bookings'] });
    setStep(b.payment && b.payment.status === 'pending' ? 3 : 4);
  });

  // the paid booking is confirmed by the club's worker a moment later
  const refreshBooking = async () => {
    if (!booking) return;
    let b = booking;
    for (let i = 0; i < 10; i++) {
      b = await request<Booking>('GET', `/api/v1/member/bookings/${booking.id}`);
      if (b.status !== 'pending') break;
      await new Promise((r) => setTimeout(r, 1000));
    }
    setBooking(b);
    void qc.invalidateQueries();
    setStep(4);
  };

  return (
    <div className="mj-page mj-narrow">
      <Head title="Book Tee Time" back={['/book', 'Book']} help={hold && step < 4 && !booking ? <HoldTimer until={hold.expiresAt} /> : undefined} />
      <Steps steps={STEPS} current={step} />
      <ErrorAlert error={error} />

      {step === 0 && (
        <>
          <div className="mj-card oc-stack">
            {(courses.data?.items.length ?? 0) > 1 && <SelectField label="Course" value={course} onChange={setCourse} options={(courses.data?.items ?? []).map((c) => ({ value: c.id, label: c.name }))} />}
            <DateStrip value={date} onChange={(d) => { setDate(d); setSlot(null); setAutoIdx(0); }} />
            <div className="oc-row-wrap">
              <div className="mj-seg" role="group" aria-label="Session">
                {SESSIONS.map(([v, l]) => <button key={v} type="button" aria-pressed={session === v} onClick={() => { setSession(v); setSlot(null); }}>{l}</button>)}
              </div>
              <span className="oc-spacer" />
              <span className="mj-small mj-muted">{dayLabel(date)}</span>
            </div>
          </div>
          <div className="mj-card oc-stack">
            <div className="oc-row-wrap">
              <h2 style={{ margin: 0 }}><Icon name="group" size={20} /> Players</h2>
              <span className="oc-spacer" />
              <div className="mj-seg" role="group" aria-label="Number of players">
                {[1, 2, 3, 4].map((n) => (
                  <button key={n} type="button" aria-pressed={count === n} onClick={() => { setCount(n); if (slot && slot.remaining < n) setSlot(null); }}>{n}</button>
                ))}
              </div>
            </div>
            <h2 style={{ margin: 0 }}><Icon name="schedule" size={20} /> Select time</h2>
            {avail.isLoading && <Skeleton rows={4} />}
            <ErrorAlert error={avail.error} />
            {avail.data && avail.data.items.length === 0 && <p className="mj-muted">No tee times on this date. Try another day or session.</p>}
            <div className="mj-slots">
              {(avail.data?.items ?? []).map((s) => {
                // a slot fits when it has room for the players and its minimum is met
                const full = s.remaining <= 0 || s.status === 'full' || s.status === 'blocked';
                const fits = !full && s.remaining >= count && count >= (s.minPlayers || 1) && count <= (s.maxPlayers || 4);
                return (
                  <button key={s.id} type="button" className="mj-slot" data-full={!fits} data-few={fits && s.remaining < 4} aria-pressed={slot?.id === s.id} disabled={!fits}
                    title={full ? 'Full' : !fits ? `Not available for ${count} ${count === 1 ? 'player' : 'players'}` : undefined} onClick={() => setSlot(s)}>
                    <strong className="mj-num">{s.localTime}</strong>
                    <span>{full ? 'Full' : `${s.remaining} left`}{s.startTee > 1 ? ` · T${s.startTee}` : ''}</span>
                  </button>
                );
              })}
            </div>
            {slot && <div className="mj-small mj-muted">{slot.localTime}{slot.startTee > 1 ? ` · tee ${slot.startTee}` : ''} · Member Rate applies to you{slot.prices?.member ? ` (${money(slot.prices.member)})` : ''}</div>}
          </div>
          <div className="mj-actions mj-sticky">
            <button className="oc-btn oc-btn-primary" disabled={!slot || busy} onClick={() => void holdSlot()}>Continue <Icon name="arrow_forward" size={18} /></button>
          </div>
        </>
      )}

      {step === 1 && <PlayersStep players={players} setPlayers={setPlayers} onBack={() => setStep(0)} onNext={() => setStep(2)} />}

      {step === 2 && slot && (
        <ReviewStep slot={slot} date={date} players={players} quote={quote.data} est={est} carts={carts} setCarts={setCarts} notes={notes} setNotes={setNotes}
          onBack={() => setStep(1)} onNext={() => setStep(3)} />
      )}

      {step === 3 && !booking && (
        <PaymentStep when={when} setWhen={setWhen} method={method} setMethod={setMethod} part={part} setPart={setPart} total={est.total}
          busy={busy} onBack={() => setStep(2)} onConfirm={() => void confirm()} />
      )}

      {step === 3 && booking?.payment && (
        <div className="mj-card">
          <PaymentPanel payment={booking.payment} onPaid={() => void refreshBooking()} />
          <div className="mj-actions" style={{ marginTop: 14 }}>
            <Link className="oc-btn oc-btn-neutral" to={`/bookings/${booking.id}`}>Pay later</Link>
          </div>
        </div>
      )}

      {step === 4 && booking && <Confirmed booking={booking} onView={() => nav(`/bookings/${booking.id}`)} />}
    </div>
  );
}

function HoldTimer({ until }: { until: string }) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);
  const left = Math.max(0, Math.floor((new Date(until).getTime() - now) / 1000));
  return <span><Icon name="timer" size={16} /> Tee time held for you · {Math.floor(left / 60)}:{String(left % 60).padStart(2, '0')}</span>;
}

export function DateStrip({ value, onChange, days = 21 }: { value: string; onChange: (d: string) => void; days?: number }) {
  const list = useMemo(() => Array.from({ length: days }, (_, i) => isoDay(i)), [days]);
  return (
    <div className="mj-dates" role="group" aria-label="Date">
      {list.map((d) => (
        <button key={d} type="button" aria-pressed={value === d} onClick={() => onChange(d)} aria-label={dayLabel(d)}>
          <span>{dayLabel(d, { weekday: 'short' })}</span><b>{Number(d.slice(8))}</b><span>{dayLabel(d, { month: 'short' })}</span>
        </button>
      ))}
    </div>
  );
}

function PlayersStep({ players, setPlayers, onBack, onNext }: { players: Player[]; setPlayers: React.Dispatch<React.SetStateAction<Player[]>>; onBack: () => void; onNext: () => void }) {
  const guests = useGet<Page<Schemas['MyGuest']>>('/api/v1/member/golf/guests');
  const set = (i: number, p: Partial<Player>) => setPlayers((ps) => ps.map((x, j) => (j === i ? { ...x, ...p } : x)));
  const ok = players.every((p) => p.kind !== 'member' || p.memberId || p.memberNo);
  return (
    <div className="mj-card oc-stack">
      <h2><Icon name="group" size={20} /> Players</h2>
      {players.map((p, i) => (
        <div key={i} className="mj-player">
          <div className="mj-player-head">
            <span className="mj-avatar">{p.kind === 'self' ? initials(p.name) : i + 1}</span>
            <strong>{p.kind === 'self' ? `${p.name} (me)` : `Player ${i + 1}`}</strong>
            <span className="oc-spacer" />
            {p.kind !== 'self' && (
              <div className="mj-seg" role="group" aria-label={`Player ${i + 1} type`}>
                <button type="button" aria-pressed={p.kind === 'member'} onClick={() => set(i, { kind: 'member', name: '', memberId: undefined })}>Member</button>
                <button type="button" aria-pressed={p.kind === 'guest'} onClick={() => set(i, { kind: 'guest', name: '', memberId: undefined })}>Guest</button>
              </div>
            )}
          </div>
          {p.kind === 'member' && <MemberPicker player={p} onPick={(m) => set(i, m)} />}
          {p.kind === 'guest' && (
            <>
              <div className="oc-row-wrap">
                <div style={{ flex: 1, minWidth: 180 }}><TextField label="Guest name" value={p.name} onChange={(v) => set(i, { name: v })} placeholder="Empty = to be announced" /></div>
                <div style={{ flex: 1, minWidth: 160 }}><TextField label="Phone" value={p.phone} onChange={(v) => set(i, { phone: v })} inputMode="tel" /></div>
              </div>
              {(guests.data?.items.length ?? 0) > 0 && (
                <div className="mj-guests" aria-label="My Guests">
                  <span className="mj-small mj-muted">My Guests:</span>
                  {guests.data!.items.slice(0, 8).map((g) => (
                    <button key={g.name} type="button" className="oc-chip" aria-pressed={p.name === g.name} onClick={() => set(i, { name: g.name, phone: g.phone ?? '' })}>{g.name}</button>
                  ))}
                </div>
              )}
            </>
          )}
        </div>
      ))}
      <div className="mj-actions mj-sticky">
        <button className="oc-btn oc-btn-neutral" onClick={onBack}>Back</button>
        <button className="oc-btn oc-btn-primary" disabled={!ok} onClick={onNext}>Continue <Icon name="arrow_forward" size={18} /></button>
      </div>
    </div>
  );
}

function MemberPicker({ player, onPick }: { player: Player; onPick: (p: Partial<Player>) => void }) {
  const [q, setQ] = useState('');
  const dq = useDebounced(q, 300);
  const found = useGet<Page<Schemas['MemberLookup']>>(`/api/v1/member/golf/members${qs({ q: dq })}`);
  if (player.memberId || player.memberNo) {
    return (
      <div className="oc-row-wrap">
        <Chip tone="info"><Icon name="badge" size={14} /> {player.name || player.memberNo}</Chip>
        <button type="button" className="oc-btn oc-btn-text oc-btn-sm" onClick={() => onPick({ memberId: undefined, memberNo: undefined, name: '' })}>Change</button>
      </div>
    );
  }
  return (
    <div className="oc-stack" style={{ gap: 8 }}>
      <TextField label="Find a family member or member (name or member no.)" value={q} onChange={setQ} />
      <div className="mj-guests">
        {(found.data?.items ?? []).slice(0, 8).map((m) => (
          <button key={m.memberId} type="button" className="oc-chip" onClick={() => onPick({ memberId: m.memberId, memberNo: m.memberNo, name: m.name })}>
            {m.family && <Icon name="family_restroom" size={14} />} {m.name} · {m.memberNo}
          </button>
        ))}
        {q && /\d/.test(q) && <button type="button" className="oc-chip" onClick={() => onPick({ memberNo: q.trim(), name: q.trim() })}>Use member no. {q.trim()}</button>}
      </div>
    </div>
  );
}

/** The fee estimate per component (Green Fee, Caddy Fee, Golf Cart…) over the players. */
function estimate(quote: Quote | undefined, players: Player[]) {
  const selfSeg = quote?.segments.member ? 'member' : 'guest';
  const memberCount = players.filter((p) => p.kind !== 'guest').length;
  const guestCount = players.length - memberCount;
  const lines = new Map<string, number>();
  const add = (seg: string, n: number) => {
    const r = quote?.segments[seg];
    if (!r || n === 0) return;
    const comps = r.components?.length ? r.components : [{ name: 'Green Fee', amount: r.total } as Schemas['Component']];
    let sum = 0;
    for (const c of comps) {
      lines.set(c.name, (lines.get(c.name) ?? 0) + Number(c.amount) * n);
      sum += Number(c.amount);
    }
    const rest = Number(r.total) - sum; // tax & service
    if (rest > 0.5) lines.set('Tax & Service', (lines.get('Tax & Service') ?? 0) + rest * n);
  };
  add(selfSeg, memberCount);
  add('guest_of_member', guestCount);
  return { lines, memberCount, guestCount, total: [...lines.values()].reduce((a, b) => a + b, 0) };
}

function ReviewStep({ slot, date, players, quote, est, carts, setCarts, notes, setNotes, onBack, onNext }: {
  slot: Slot; date: string; players: Player[]; quote?: Quote; est: ReturnType<typeof estimate>; carts: string; setCarts: (v: string) => void;
  notes: string; setNotes: (v: string) => void; onBack: () => void; onNext: () => void;
}) {
  const { lines, memberCount, guestCount, total } = est;
  return (
    <>
      <div className="mj-card">
        <h2><Icon name="receipt_long" size={20} /> Booking Summary</h2>
        <Rows rows={[
          ['Date', dayLabel(date)], ['Tee Time', `${slot.localTime}${slot.startTee > 1 ? ` · tee ${slot.startTee}` : ''}`],
          ['Players', `${players.length} (${memberCount} member${memberCount === 1 ? '' : 's'}, ${guestCount} guest${guestCount === 1 ? '' : 's'})`],
          ['Golf carts', carts === '' ? 'Club rule' : carts],
        ]} />
      </div>
      <div className="mj-card">
        <h2><Icon name="group" size={20} /> Players</h2>
        <div className="mj-list">
          {players.map((p, i) => (
            <div key={i} className="mj-item">
              <span className="mj-avatar">{initials(p.name || `P ${i + 1}`)}</span>
              <div className="mj-item-body"><strong>{p.name || 'Guest (TBA)'}</strong><span className="mj-small mj-muted">{p.kind === 'self' ? 'You' : p.kind === 'member' ? 'Member' : 'Guest'}</span></div>
            </div>
          ))}
        </div>
        <p className="mj-small mj-muted" style={{ marginBottom: 0 }}><Icon name="hiking" size={16} /> A caddy for every player is included in the rate; the front desk assigns them before your tee time.</p>
        <div className="oc-row-wrap" style={{ marginTop: 12 }}>
          <strong>Golf carts</strong>
          <div className="mj-seg" role="group" aria-label="Golf carts">
            {[['', 'Club rule'], ['0', 'None'], ['1', '1'], ['2', '2']].map(([v, l]) => (
              <button key={v} type="button" aria-pressed={carts === v} disabled={v === '0' && quote?.cart.mandatory} onClick={() => setCarts(v)}>{l}</button>
            ))}
          </div>
          {quote?.cart.playersPerCart ? <span className="mj-small mj-muted">{quote.cart.playersPerCart} players share a cart</span> : null}
        </div>
      </div>
      <div className="mj-card">
        <h2><Icon name="calculate" size={20} /> Estimated Fee</h2>
        {!quote && <Skeleton rows={3} />}
        {quote && (
          <>
            <Rows rows={[...lines.entries()].map(([k, v]) => [k, <span className="mj-num">{money(v)}</span>])} />
            <div className="mj-total"><span>Total estimate</span><strong className="mj-num">{money(total)}</strong></div>
            <p className="mj-small mj-muted">The final price is set by the club's Pricing Policy when you confirm.</p>
          </>
        )}
        <TextField label="Notes for the club (optional)" value={notes} onChange={setNotes} />
      </div>
      <div className="mj-actions mj-sticky">
        <button className="oc-btn oc-btn-neutral" onClick={onBack}>Back</button>
        <button className="oc-btn oc-btn-primary" onClick={onNext}>Continue to payment <Icon name="arrow_forward" size={18} /></button>
      </div>
    </>
  );
}

const WHEN: { value: PayWhen; label: string; help: string; icon: string }[] = [
  { value: 'account', label: 'Member Account', help: 'Confirmed now, settled on your statement', icon: 'account_balance_wallet' },
  { value: 'now', label: 'Pay in full now', help: 'QRIS, Virtual Account or card', icon: 'bolt' },
  { value: 'part', label: 'Pay part now', help: 'Any amount now, the rest later in the app or at the front desk', icon: 'pie_chart' },
  { value: 'later', label: 'Pay later', help: 'Confirmed now; pay in the app or at the front desk after your round', icon: 'schedule' },
];

function PaymentStep({ when, setWhen, method, setMethod, part, setPart, total, busy, onBack, onConfirm }: {
  when: PayWhen; setWhen: (w: PayWhen) => void; method: OnlineMethod; setMethod: (m: OnlineMethod) => void; part: string; setPart: (v: string) => void;
  total: number; busy: boolean; onBack: () => void; onConfirm: () => void;
}) {
  const amount = Number(part);
  const partOk = when !== 'part' || (amount > 0 && (!total || amount <= total));
  return (
    <div className="mj-card oc-stack">
      <h2><Icon name="payments" size={20} /> Payment</h2>
      <div className="mj-choices" role="radiogroup" aria-label="When to pay">
        {WHEN.map((w) => (
          <button key={w.value} type="button" className="mj-choice" role="radio" aria-checked={when === w.value} aria-pressed={when === w.value} onClick={() => setWhen(w.value)}>
            <span className="mj-icon"><Icon name={w.icon} size={20} /></span>
            <span><strong>{w.label}</strong><small>{w.help}</small></span>
          </button>
        ))}
      </div>
      {when === 'part' && (
        <TextField label={`Amount to pay now${total ? ` (of ${money(total)})` : ''}`} value={part} onChange={(v) => setPart(v.replace(/\D/g, ''))} inputMode="numeric" />
      )}
      {(when === 'now' || when === 'part') && (
        <>
          <strong>Pay with</strong>
          <MethodPicker value={method} onChange={(m) => setMethod(m as OnlineMethod)} memberCharge={false} />
        </>
      )}
      <p className="mj-small mj-muted" style={{ margin: 0 }}>
        {when === 'account' ? 'The booking is confirmed now and charged to your member account.'
          : when === 'later' ? 'The booking is confirmed now. The balance is paid at the end, in the app or at the front desk.'
            : 'The tee time is held and confirmed once this payment is received; the rest can be paid later.'}
      </p>
      <div className="mj-actions mj-sticky">
        <button className="oc-btn oc-btn-neutral" onClick={onBack}>Back</button>
        <button className="oc-btn oc-btn-primary" disabled={busy || !partOk} onClick={onConfirm}><Icon name="check" size={18} /> Confirm Booking</button>
      </div>
    </div>
  );
}

function Confirmed({ booking, onView }: { booking: Booking; onView: () => void }) {
  const confirmed = booking.status === 'confirmed';
  const start = new Date(booking.startAt);
  return (
    <div className="mj-card mj-success">
      <span className="mj-success-mark"><Icon name={confirmed ? 'check' : 'hourglass_top'} size={36} /></span>
      <h1>{confirmed ? 'Booking Confirmed' : 'Booking Received'}</h1>
      <div className="mj-muted">{dayLabel(booking.playDate)} · {booking.localTime} · {booking.courseName}</div>
      <div className="oc-stack" style={{ alignItems: 'center', gap: 4 }}><span className="mj-small mj-muted">Booking ID</span><span className="mj-code">{booking.code}</span></div>
      {confirmed && booking.qrToken && <QRCode value={`oneclub:booking:${booking.qrToken}`} size={180} label="Check-in QR" />}
      {confirmed && <div className="mj-small mj-muted">Show this QR at the clubhouse to check in.</div>}
      <div className="mj-checklist" style={{ alignItems: 'flex-start' }}>
        <Check done={confirmed}>Tee Time confirmed</Check>
        <Check done>Players confirmed</Check>
        <Check done={false}>Caddy assigned by the front desk</Check>
      </div>
      <div className="mj-actions" style={{ justifyContent: 'center' }}>
        <button className="oc-btn oc-btn-primary" onClick={onView}>View Booking</button>
        <button className="oc-btn oc-btn-outline" onClick={() => downloadICS({ title: `Tee Time ${booking.code}`, start, end: new Date(start.getTime() + 5 * 3600_000),
          location: booking.courseName, description: `${booking.playerCount} players · Booking ${booking.code}` })}><Icon name="event" size={18} /> Add to Calendar</button>
      </div>
    </div>
  );
}
