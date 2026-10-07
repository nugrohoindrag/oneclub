import React, { useState } from 'react';
import { Link, useNavigate } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { qs, request, useGet, uuidv7, type Page, type Schemas } from '@oneclub/api-client';
import { ErrorAlert, Icon, SelectField, Skeleton, TextArea, TextField } from '@oneclub/shell';
import { MethodPicker, PaymentPanel, type PayMethod } from './pay';
import { DateStrip } from './teetime';
import { Chip, dayLabel, downloadICS, Head, isoDay, money, Rows, Steps } from './ui';

// Resort branch of the member journey: Book Bungalow (dates → bungalow →
// guests → review → payment → confirmation) and Book Meeting Room (date &
// time → room → capacity & purpose → review → payment → confirmation).

type Catalog = Schemas['StayCatalog'];
type Quote = Schemas['StayQuote'];
type StayResult = Schemas['StayResult'];
type Payment = Schemas['Payment'];

/** Book (member charge, or online payment of the folio) — shared by both wizards. */
function useStayBooking() {
  const qc = useQueryClient();
  const [result, setResult] = useState<StayResult | null>(null);
  const [payment, setPayment] = useState<Payment | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const book = async (body: Record<string, unknown>, method: PayMethod) => {
    setBusy(true);
    setError(null);
    try {
      const r = await request<StayResult>('POST', '/api/v1/member/stays', { ...body, memberCharge: method === 'member_charge' }, { 'Idempotency-Key': uuidv7() });
      void qc.invalidateQueries({ queryKey: ['/api/v1/member/stays'] });
      // the checkout first, then the result: the wizard moves on once both are known
      if (method !== 'member_charge' && r.folio && Number(r.folio.summary.balance) > 0) {
        try {
          setPayment(await request<Payment>('POST', `/api/v1/member/folios/${r.folio.id}:pay-online`, { method }));
        } catch (e) {
          setError(e); // booked; pay later from Transactions
        }
      }
      setResult(r);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return { result, payment, setPayment, busy, error, book };
}

function useQuote(body: Record<string, unknown> | null) {
  const [quote, setQuote] = useState<Quote | null>(null);
  const [error, setError] = useState<unknown>(null);
  const key = body ? JSON.stringify(body) : '';
  React.useEffect(() => {
    if (!body) return;
    let live = true;
    setQuote(null);
    setError(null);
    request<Quote>('POST', '/api/v1/member/stays:quote', body).then((q) => live && setQuote(q), (e) => live && setError(e));
    return () => { live = false; };
  }, [key]); // eslint-disable-line react-hooks/exhaustive-deps
  return { quote, error };
}

function QuoteCard({ quote, error }: { quote: Quote | null; error: unknown }) {
  return (
    <div className="mj-card">
      <h2><Icon name="calculate" size={20} /> Price</h2>
      <ErrorAlert error={error} />
      {!quote && !error && <Skeleton rows={3} />}
      {quote && (
        <>
          <Rows rows={quote.lines.map((l) => [l.description, <span className="mj-num">{money(l.total)}</span>])} />
          <div className="mj-total"><span>Total</span><strong className="mj-num">{money(quote.total)}</strong></div>
          {Number(quote.depositRequired) > 0 && <p className="mj-small mj-muted">Deposit required now: {money(quote.depositRequired)}; the rest is settled at check-out.</p>}
        </>
      )}
    </div>
  );
}

function PayCard({ method, setMethod, busy, onBack, onConfirm, label }: { method: PayMethod; setMethod: (m: PayMethod) => void; busy: boolean; onBack: () => void; onConfirm: () => void; label: string }) {
  return (
    <div className="mj-card oc-stack">
      <h2><Icon name="payments" size={20} /> Payment</h2>
      <MethodPicker value={method} onChange={setMethod} />
      <div className="mj-actions mj-sticky">
        <button className="oc-btn oc-btn-neutral" onClick={onBack}>Back</button>
        <button className="oc-btn oc-btn-primary" disabled={busy} onClick={onConfirm}><Icon name="check" size={18} /> {label}</button>
      </div>
    </div>
  );
}

function StayConfirmed({ result, title, paid }: { result: StayResult; title: string; paid: boolean }) {
  const nav = useNavigate();
  const s = result.stay;
  return (
    <div className="mj-card mj-success">
      <span className="mj-success-mark"><Icon name="check" size={36} /></span>
      <h1>{title}</h1>
      <div className="mj-muted">{s.unitName} · {new Date(s.start).toLocaleString('en-GB', { dateStyle: 'medium', timeStyle: 'short' })} – {new Date(s.end).toLocaleString('en-GB', { dateStyle: 'medium', timeStyle: 'short' })}</div>
      <div className="oc-stack" style={{ alignItems: 'center', gap: 4 }}><span className="mj-small mj-muted">Booking ID</span><span className="mj-code">{s.stayNo}</span></div>
      <Chip tone={paid ? 'ok' : 'warn'}>{paid ? 'Paid / charged to your account' : 'Payment pending'}</Chip>
      <div className="mj-actions" style={{ justifyContent: 'center' }}>
        <button className="oc-btn oc-btn-primary" onClick={() => nav('/activity/stays')}>My Stays</button>
        <button className="oc-btn oc-btn-outline" onClick={() => downloadICS({ title: `${s.unitName} ${s.stayNo}`, start: new Date(s.start), end: new Date(s.end), description: `Booking ${s.stayNo}` })}>
          <Icon name="event" size={18} /> Add to Calendar</button>
      </div>
    </div>
  );
}

// ── Book Bungalow ─────────────────────────────────────────────────────────

const B_STEPS = ['Dates', 'Bungalow', 'Guests', 'Review', 'Payment', 'Confirmed'];

export function BungalowWizard() {
  const [step, setStep] = useState(0);
  const [from, setFrom] = useState(isoDay(1));
  const [nights, setNights] = useState(1);
  const catalog = useGet<Catalog>('/api/v1/member/stay-catalog');
  const avail = useGet<Page<Schemas['TypeAvailability']>>(step >= 1 ? `/api/v1/member/bungalow-availability${qs({ from, nights })}` : null);
  const [typeId, setType] = useState('');
  const [ratePlan, setRatePlan] = useState('');
  const [adults, setAdults] = useState(2);
  const [children, setChildren] = useState(0);
  const [requests, setRequests] = useState('');
  const [method, setMethod] = useState<PayMethod>('member_charge');
  const type = catalog.data?.bungalowTypes.find((t) => t.id === typeId);
  const plans = (catalog.data?.ratePlans ?? []).filter((p) => p.minNights <= nights);
  const plan = ratePlan || plans[0]?.code || '';
  const departure = isoDay(nights, from);
  const body = { kind: 'bungalow', bungalowTypeId: typeId, arrivalDate: from, departureDate: departure, ratePlan: plan || undefined, adults, children, specialRequests: requests || undefined };
  const q = useQuote(step === 3 ? body : null);
  const bk = useStayBooking();
  const [paid, setPaid] = useState(false);
  const confirm = async () => {
    await bk.book(body, method);
  };
  React.useEffect(() => {
    if (bk.result && !bk.payment) { setPaid(method === 'member_charge'); setStep(5); } else if (bk.payment) setStep(4);
  }, [bk.result, bk.payment]); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <div className="mj-page mj-narrow">
      <Head title="Book Bungalow" back={['/book', 'Book']} />
      <Steps steps={B_STEPS} current={step} />
      <ErrorAlert error={bk.error} />
      {step === 0 && (
        <div className="mj-card oc-stack">
          <h2><Icon name="calendar_month" size={20} /> Arrival</h2>
          <DateStrip value={from} onChange={setFrom} days={60} />
          <div className="oc-row-wrap">
            <strong>Nights</strong>
            <div className="mj-seg" role="group" aria-label="Nights">
              {[1, 2, 3, 4, 5, 7].map((n) => <button key={n} type="button" aria-pressed={nights === n} onClick={() => setNights(n)}>{n}</button>)}
            </div>
          </div>
          <div className="mj-small mj-muted">{dayLabel(from)} → {dayLabel(departure)}</div>
          <div className="mj-actions mj-sticky"><button className="oc-btn oc-btn-primary" onClick={() => setStep(1)}>Continue <Icon name="arrow_forward" size={18} /></button></div>
        </div>
      )}
      {step === 1 && (
        <div className="mj-card oc-stack">
          <h2><Icon name="cottage" size={20} /> Select bungalow</h2>
          {(catalog.isLoading || avail.isLoading) && <Skeleton rows={3} />}
          <div className="mj-choices" style={{ gridTemplateColumns: 'repeat(auto-fill, minmax(260px, 1fr))' }}>
            {(catalog.data?.bungalowTypes ?? []).map((t) => {
              const a = avail.data?.items.find((x) => x.typeId === t.id);
              const left = a ? Math.min(...a.nights.map((n) => n.available)) : 0;
              return (
                <button key={t.id} type="button" className="mj-choice" aria-pressed={typeId === t.id} disabled={left <= 0} onClick={() => { setType(t.id); setAdults(Math.min(adults, t.maxAdults)); }}>
                  <span className="mj-icon"><Icon name="cottage" size={20} /></span>
                  <span>
                    <strong>{t.name}</strong>
                    <small>{t.bedrooms} bedroom{t.bedrooms === 1 ? '' : 's'} · up to {t.maxAdults} adults{t.facilities.length ? ` · ${t.facilities.slice(0, 3).join(', ')}` : ''}</small>
                    <Chip tone={left > 0 ? (left < 2 ? 'warn' : 'ok') : 'bad'}>{left > 0 ? `${left} available` : 'Full'}</Chip>
                  </span>
                </button>
              );
            })}
          </div>
          {plans.length > 0 && (
            <div className="oc-stack" style={{ gap: 8 }}>
              <strong>Rate plan</strong>
              <div className="mj-seg" role="group" aria-label="Rate plan">
                {plans.map((p) => <button key={p.code} type="button" aria-pressed={plan === p.code} onClick={() => setRatePlan(p.code)}>{p.name}{p.includesBreakfast ? ' · breakfast' : ''}</button>)}
              </div>
            </div>
          )}
          <div className="mj-actions mj-sticky">
            <button className="oc-btn oc-btn-neutral" onClick={() => setStep(0)}>Back</button>
            <button className="oc-btn oc-btn-primary" disabled={!typeId} onClick={() => setStep(2)}>Continue <Icon name="arrow_forward" size={18} /></button>
          </div>
        </div>
      )}
      {step === 2 && type && (
        <div className="mj-card oc-stack">
          <h2><Icon name="group" size={20} /> Guest details</h2>
          <div className="oc-row-wrap">
            <strong style={{ width: 90 }}>Adults</strong>
            <div className="mj-seg" role="group" aria-label="Adults">
              {Array.from({ length: type.maxAdults }, (_, i) => i + 1).map((n) => <button key={n} type="button" aria-pressed={adults === n} onClick={() => setAdults(n)}>{n}</button>)}
            </div>
          </div>
          {type.maxChildren > 0 && (
            <div className="oc-row-wrap">
              <strong style={{ width: 90 }}>Children</strong>
              <div className="mj-seg" role="group" aria-label="Children">
                {Array.from({ length: type.maxChildren + 1 }, (_, i) => i).map((n) => <button key={n} type="button" aria-pressed={children === n} onClick={() => setChildren(n)}>{n}</button>)}
              </div>
            </div>
          )}
          <TextArea label="Special requests (optional)" value={requests} onChange={setRequests} rows={3} />
          <div className="mj-actions mj-sticky">
            <button className="oc-btn oc-btn-neutral" onClick={() => setStep(1)}>Back</button>
            <button className="oc-btn oc-btn-primary" onClick={() => setStep(3)}>Review <Icon name="arrow_forward" size={18} /></button>
          </div>
        </div>
      )}
      {step === 3 && type && (
        <>
          <div className="mj-card">
            <h2><Icon name="receipt_long" size={20} /> Review</h2>
            <Rows rows={[['Bungalow', type.name], ['Check-in', dayLabel(from)], ['Check-out', dayLabel(departure)], ['Nights', nights],
              ['Guests', `${adults} adult${adults === 1 ? '' : 's'}${children ? `, ${children} child${children === 1 ? '' : 'ren'}` : ''}`],
              ['Rate plan', plans.find((p) => p.code === plan)?.name ?? 'Room Only']]} />
          </div>
          <QuoteCard quote={q.quote} error={q.error} />
          <div className="mj-actions mj-sticky">
            <button className="oc-btn oc-btn-neutral" onClick={() => setStep(2)}>Back</button>
            <button className="oc-btn oc-btn-primary" disabled={!q.quote} onClick={() => setStep(4)}>Continue to payment <Icon name="arrow_forward" size={18} /></button>
          </div>
        </>
      )}
      {step === 4 && !bk.result && <PayCard method={method} setMethod={setMethod} busy={bk.busy} onBack={() => setStep(3)} onConfirm={() => void confirm()} label="Confirm Booking" />}
      {step === 4 && bk.payment && (
        <div className="mj-card">
          <PaymentPanel payment={bk.payment} onPaid={() => { setPaid(true); setStep(5); }} />
          <div className="mj-actions" style={{ marginTop: 14 }}><Link className="oc-btn oc-btn-neutral" to="/activity/stays">Pay later</Link></div>
        </div>
      )}
      {step === 5 && bk.result && <StayConfirmed result={bk.result} title="Bungalow Booked" paid={paid} />}
    </div>
  );
}

// ── Book Meeting Room ─────────────────────────────────────────────────────

const M_STEPS = ['Date & Time', 'Room', 'Details', 'Review', 'Payment', 'Confirmed'];
const TIMES = Array.from({ length: 27 }, (_, i) => `${String(7 + Math.floor(i / 2)).padStart(2, '0')}:${i % 2 ? '30' : '00'}`);
const LAYOUTS: Record<string, string> = { round_table: 'Round table', classroom: 'Classroom', u_shape: 'U-shape', theater: 'Theater', boardroom: 'Boardroom', cocktail: 'Cocktail' };

export function MeetingRoomWizard() {
  const [step, setStep] = useState(0);
  const [date, setDate] = useState(isoDay(1));
  const [time, setTime] = useState('09:00');
  const [hours, setHours] = useState(4);
  const catalog = useGet<Catalog>('/api/v1/member/stay-catalog');
  const day = useGet<Page<Schemas['RoomDay']>>(step >= 1 ? `/api/v1/member/meeting-room-availability?date=${date}` : null);
  const [roomId, setRoom] = useState('');
  const [pax, setPax] = useState('10');
  const [layout, setLayout] = useState('');
  const [purpose, setPurpose] = useState('');
  const [method, setMethod] = useState<PayMethod>('member_charge');
  const start = new Date(`${date}T${time}:00`);
  const end = new Date(start.getTime() + hours * 3600_000);
  const room = catalog.data?.meetingRooms.find((r) => r.id === roomId);
  const fits = (room?.layouts ?? []).filter((l) => l.capacity >= Number(pax || 0));
  const body = { kind: 'meeting_room', unitId: roomId, start: start.toISOString(), end: end.toISOString(), pax: Number(pax) || undefined, layout: layout || undefined,
    specialRequests: purpose ? `Purpose: ${purpose}` : undefined };
  const q = useQuote(step === 3 ? body : null);
  const bk = useStayBooking();
  const [paid, setPaid] = useState(false);
  React.useEffect(() => {
    if (bk.result && !bk.payment) { setPaid(method === 'member_charge'); setStep(5); } else if (bk.payment) setStep(4);
  }, [bk.result, bk.payment]); // eslint-disable-line react-hooks/exhaustive-deps
  const busyFor = (id: string) => (day.data?.items.find((d) => d.roomId === id)?.busy ?? []);
  const clash = (id: string) => busyFor(id).some((b) => new Date(b.start) < end && new Date(b.end) > start);
  return (
    <div className="mj-page mj-narrow">
      <Head title="Book Meeting Room" back={['/book', 'Book']} />
      <Steps steps={M_STEPS} current={step} />
      <ErrorAlert error={bk.error} />
      {step === 0 && (
        <div className="mj-card oc-stack">
          <h2><Icon name="calendar_month" size={20} /> Date & time</h2>
          <DateStrip value={date} onChange={setDate} days={60} />
          <div className="oc-row-wrap">
            <div style={{ width: 160 }}><SelectField label="Start" value={time} onChange={setTime} options={TIMES.map((t) => ({ value: t, label: t }))} /></div>
            <div className="oc-stack" style={{ gap: 6 }}>
              <span className="mj-small mj-muted">Duration</span>
              <div className="mj-seg" role="group" aria-label="Duration">
                {[[2, '2 h'], [4, 'Half day'], [8, 'Full day']].map(([h, l]) => <button key={h} type="button" aria-pressed={hours === h} onClick={() => setHours(Number(h))}>{l}</button>)}
              </div>
            </div>
          </div>
          <div className="mj-small mj-muted">{dayLabel(date)} · {time} – {end.toTimeString().slice(0, 5)}</div>
          <div className="mj-actions mj-sticky"><button className="oc-btn oc-btn-primary" onClick={() => setStep(1)}>Continue <Icon name="arrow_forward" size={18} /></button></div>
        </div>
      )}
      {step === 1 && (
        <div className="mj-card oc-stack">
          <h2><Icon name="meeting_room" size={20} /> Select room</h2>
          {(catalog.isLoading || day.isLoading) && <Skeleton rows={3} />}
          {(catalog.data?.meetingRooms ?? []).map((r) => {
            const busy = clash(r.id);
            return (
              <button key={r.id} type="button" className="mj-choice" aria-pressed={roomId === r.id} disabled={busy} onClick={() => setRoom(r.id)} style={{ flexDirection: 'column', alignItems: 'stretch' }}>
                <span className="oc-row"><span className="mj-icon"><Icon name="meeting_room" size={20} /></span>
                  <span style={{ flex: 1 }}><strong>{r.name}</strong><small>{r.sizeSqm ? `${r.sizeSqm} m² · ` : ''}up to {Math.max(0, ...r.layouts.map((l) => l.capacity))} people{r.facilities.length ? ` · ${r.facilities.slice(0, 3).join(', ')}` : ''}</small></span>
                  <Chip tone={busy ? 'bad' : 'ok'}>{busy ? 'Booked at this time' : 'Available'}</Chip></span>
                <DayBar busy={busyFor(r.id)} date={date} start={start} end={end} />
              </button>
            );
          })}
          <div className="mj-actions mj-sticky">
            <button className="oc-btn oc-btn-neutral" onClick={() => setStep(0)}>Back</button>
            <button className="oc-btn oc-btn-primary" disabled={!roomId || clash(roomId)} onClick={() => setStep(2)}>Continue <Icon name="arrow_forward" size={18} /></button>
          </div>
        </div>
      )}
      {step === 2 && room && (
        <div className="mj-card oc-stack">
          <h2><Icon name="groups" size={20} /> Capacity & purpose</h2>
          <div style={{ width: 160 }}><TextField label="Number of people" type="number" min={1} value={pax} onChange={setPax} /></div>
          <div className="oc-stack" style={{ gap: 6 }}>
            <span className="mj-small mj-muted">Layout</span>
            <div className="mj-seg" role="group" aria-label="Layout">
              {room.layouts.map((l) => <button key={l.layout} type="button" aria-pressed={layout === l.layout} disabled={l.capacity < Number(pax || 0)} onClick={() => setLayout(l.layout)}>{LAYOUTS[l.layout] ?? l.layout} · {l.capacity}</button>)}
            </div>
            {room.layouts.length > 0 && fits.length === 0 && <span className="mj-small" style={{ color: 'var(--mj-bad)' }}>The room is too small for {pax} people.</span>}
          </div>
          <TextArea label="Purpose" value={purpose} onChange={setPurpose} rows={3} />
          <div className="mj-actions mj-sticky">
            <button className="oc-btn oc-btn-neutral" onClick={() => setStep(1)}>Back</button>
            <button className="oc-btn oc-btn-primary" disabled={!purpose.trim() || (room.layouts.length > 0 && fits.length === 0)} onClick={() => setStep(3)}>Review <Icon name="arrow_forward" size={18} /></button>
          </div>
        </div>
      )}
      {step === 3 && room && (
        <>
          <div className="mj-card">
            <h2><Icon name="receipt_long" size={20} /> Review</h2>
            <Rows rows={[['Room', room.name], ['Date', dayLabel(date)], ['Time', `${time} – ${end.toTimeString().slice(0, 5)}`], ['People', pax],
              ['Layout', LAYOUTS[layout] ?? 'Club default'], ['Purpose', purpose]]} />
          </div>
          <QuoteCard quote={q.quote} error={q.error} />
          <div className="mj-actions mj-sticky">
            <button className="oc-btn oc-btn-neutral" onClick={() => setStep(2)}>Back</button>
            <button className="oc-btn oc-btn-primary" disabled={!q.quote} onClick={() => setStep(4)}>Continue to payment <Icon name="arrow_forward" size={18} /></button>
          </div>
        </>
      )}
      {step === 4 && !bk.result && <PayCard method={method} setMethod={setMethod} busy={bk.busy} onBack={() => setStep(3)} onConfirm={() => void bk.book(body, method)} label="Confirm Booking" />}
      {step === 4 && bk.payment && (
        <div className="mj-card">
          <PaymentPanel payment={bk.payment} onPaid={() => { setPaid(true); setStep(5); }} />
          <div className="mj-actions" style={{ marginTop: 14 }}><Link className="oc-btn oc-btn-neutral" to="/activity/stays">Pay later</Link></div>
        </div>
      )}
      {step === 5 && bk.result && <StayConfirmed result={bk.result} title="Meeting Room Booked" paid={paid} />}
    </div>
  );
}

/** A meeting room's day 07:00–21:00: booked periods red, the chosen time outlined. */
function DayBar({ busy, date, start, end }: { busy: { start: string; end: string }[]; date: string; start: Date; end: Date }) {
  const d0 = new Date(`${date}T07:00:00`).getTime();
  const span = 14 * 3600_000;
  const pct = (t: number) => `${Math.min(100, Math.max(0, ((t - d0) / span) * 100))}%`;
  const w = (a: number, b: number) => `${Math.max(0, Math.min(100, ((b - a) / span) * 100))}%`;
  return (
    <span style={{ display: 'block', marginTop: 8 }}>
      <span className="mj-day" aria-hidden style={{ display: 'block' }}>
        {busy.map((b, i) => <b key={i} style={{ left: pct(new Date(b.start).getTime()), width: w(Math.max(d0, new Date(b.start).getTime()), new Date(b.end).getTime()) }} />)}
        <b style={{ left: pct(start.getTime()), width: w(start.getTime(), end.getTime()), background: 'transparent', border: '2px solid var(--md-sys-color-primary)', opacity: 1, borderRadius: 6 }} />
      </span>
      <span className="mj-day-scale"><span>07:00</span><span>10:30</span><span>14:00</span><span>17:30</span><span>21:00</span></span>
    </span>
  );
}
