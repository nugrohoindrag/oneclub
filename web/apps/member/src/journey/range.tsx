import React, { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { qs, request, useGet, uuidv7, type Page, type Schemas } from '@oneclub/api-client';
import { ErrorAlert, Icon, Skeleton, TextField } from '@oneclub/shell';
import { DateStrip } from './teetime';
import { Chip, dayLabel, Head, isoDay, Rows, StatusChip } from './ui';

// Book Driving Range (demo feedback 9 Oct 2026): a bay and a time, or just
// the visit (balls are bought at the Driving Range Counter). The time is
// flexible: the bay is kept 15 minutes past the booked time; later, the
// front desk gives the next free bay or a place in the queue.

type Slot = Schemas['RangeSlot'];
type RangeBooking = Schemas['RangeBooking'];

const LENGTHS = [30, 60, 90, 120];
const time = (iso: string) => new Date(iso).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' });

export function RangeWizard() {
  const qc = useQueryClient();
  const [bay, setBay] = useState(true);
  const [date, setDate] = useState(isoDay(0));
  const [area, setArea] = useState<'outdoor' | 'indoor'>('outdoor');
  const [minutes, setMinutes] = useState(60);
  const [players, setPlayers] = useState(1);
  const [slot, setSlot] = useState<Slot | null>(null);
  const [bayId, setBayId] = useState('');
  const [notes, setNotes] = useState('');
  const [done, setDone] = useState<RangeBooking | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const avail = useGet<Page<Slot>>(`/api/v1/member/golf/range-availability${qs({ date, area, minutes })}`);
  const reset = () => { setSlot(null); setBayId(''); };
  const book = async () => {
    if (!slot) return;
    setBusy(true);
    setError(null);
    try {
      const b = await request<RangeBooking>('POST', '/api/v1/member/golf/range-bookings', {
        date, time: slot.time, minutes, area, players, reserveBay: bay, bayId: bay && bayId ? bayId : undefined, notes: notes || undefined,
      }, { 'Idempotency-Key': uuidv7() });
      setDone(b);
      void qc.invalidateQueries({ queryKey: ['/api/v1/member/golf/range-bookings'] });
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  if (done) {
    return (
      <div className="mj-page mj-narrow">
        <Head title="Driving Range" back={['/book', 'Book']} />
        <div className="mj-card mj-success">
          <span className="mj-success-mark"><Icon name="check" size={36} /></span>
          <h1>Range Booked</h1>
          <div className="mj-muted">{dayLabel(done.playDate)} · {time(done.startAt)} – {time(done.endAt)} · {done.area}</div>
          <span className="mj-code">{done.number}</span>
          <Rows rows={[['Bay', done.bayCode ?? 'At the counter'], ['Players', done.players]]} />
          <p className="mj-small mj-muted">{done.holdsBay
            ? 'Your bay is kept until 15 minutes past the booked time. Later? The front desk gives you the next free bay or a place in the queue.'
            : 'Show the booking number at the Driving Range Counter; you get the next free bay.'} Balls are bought at the counter.</p>
          <div className="mj-actions" style={{ justifyContent: 'center' }}>
            <button className="oc-btn oc-btn-primary" onClick={() => { setDone(null); reset(); }}>Book another</button>
          </div>
        </div>
        <MyRangeBookings />
      </div>
    );
  }

  const slots = avail.data?.items ?? [];
  return (
    <div className="mj-page mj-narrow">
      <Head title="Book Driving Range" back={['/book', 'Book']} help="Pick a bay and a time, or just tell us you are coming." />
      <ErrorAlert error={error} />
      <div className="mj-card oc-stack">
        <div className="mj-seg" role="group" aria-label="Booking kind">
          <button type="button" aria-pressed={bay} onClick={() => setBay(true)}>Bay & time</button>
          <button type="button" aria-pressed={!bay} onClick={() => { setBay(false); setBayId(''); }}>Just drop by</button>
        </div>
        <DateStrip value={date} onChange={(d) => { setDate(d); reset(); }} days={14} />
        <div className="oc-row-wrap">
          <div className="mj-seg" role="group" aria-label="Area">
            {(['outdoor', 'indoor'] as const).map((a) => <button key={a} type="button" aria-pressed={area === a} onClick={() => { setArea(a); reset(); }}>{a === 'outdoor' ? 'Outdoor' : 'Indoor'}</button>)}
          </div>
          <div className="mj-seg" role="group" aria-label="Length">
            {LENGTHS.map((n) => <button key={n} type="button" aria-pressed={minutes === n} onClick={() => { setMinutes(n); reset(); }}>{n < 60 ? `${n} min` : `${n / 60} h`}</button>)}
          </div>
          <div className="mj-seg" role="group" aria-label="Players">
            {[1, 2, 3, 4].map((n) => <button key={n} type="button" aria-pressed={players === n} onClick={() => setPlayers(n)}>{n} {n === 1 ? 'player' : 'players'}</button>)}
          </div>
        </div>
      </div>
      <div className="mj-card oc-stack">
        <h2 style={{ margin: 0 }}><Icon name="schedule" size={20} /> {bay ? 'Select time' : 'Arrival time'}</h2>
        {avail.isLoading && <Skeleton rows={3} />}
        <ErrorAlert error={avail.error} />
        {avail.data && slots.length === 0 && <p className="mj-muted">No times left on this date.</p>}
        <div className="mj-slots">
          {slots.map((s) => {
            // never refused when busy: red = peak (queue for a bay), green = quiet
            const peak = s.crowd === 'peak';
            return (
              <button key={s.time} type="button" className="mj-slot" data-peak={peak} aria-pressed={slot?.time === s.time}
                onClick={() => { setSlot(s); setBayId(''); }}>
                <strong className="mj-num">{s.time}</strong>
                <span>{peak ? 'Peak' : bay ? `${s.freeBays} bays` : 'Quiet'}</span>
              </button>
            );
          })}
        </div>
        {bay && slot && slot.bays.length > 0 && (
          <div className="mj-guests" aria-label="Bay">
            <span className="mj-small mj-muted">Bay:</span>
            <button type="button" className="oc-chip" aria-pressed={!bayId} onClick={() => setBayId('')}>Any free bay</button>
            {slot.bays.map((b) => <button key={b.id} type="button" className="oc-chip" aria-pressed={bayId === b.id} onClick={() => setBayId(b.id)}>{b.code}</button>)}
          </div>
        )}
        <TextField label="Notes (optional)" value={notes} onChange={setNotes} />
        <p className="mj-small mj-muted" style={{ margin: 0 }}>Red: peak, every bay is booked — you queue for the next free bay. Green: quiet. A booked bay is kept 15 minutes; arrive later and you get the next free bay or a place in the queue. Balls are paid at the counter.</p>
        <div className="mj-actions mj-sticky">
          <button className="oc-btn oc-btn-primary" disabled={!slot || busy} onClick={() => void book()}><Icon name="check" size={18} /> Confirm Booking</button>
        </div>
      </div>
      <MyRangeBookings />
    </div>
  );
}

function MyRangeBookings() {
  const list = useGet<Page<RangeBooking>>('/api/v1/member/golf/range-bookings');
  const [error, setError] = useState<unknown>(null);
  const items = (list.data?.items ?? []).filter((b) => b.status === 'booked' || b.status === 'checked_in');
  if (!items.length) return null;
  const cancel = async (b: RangeBooking) => {
    setError(null);
    try {
      await request('POST', `/api/v1/member/golf/range-bookings/${b.id}:cancel`, { reason: 'cancelled in the Member App' });
      void list.refetch();
    } catch (e) {
      setError(e);
    }
  };
  return (
    <div className="mj-card">
      <h2><Icon name="golf_course" size={20} /> My range bookings</h2>
      <ErrorAlert error={error} />
      <div className="mj-list">
        {items.map((b) => (
          <div key={b.id} className="mj-item">
            <div className="mj-item-body">
              <strong>{dayLabel(b.playDate, { weekday: 'short', day: 'numeric', month: 'short' })} · {time(b.startAt)} – {time(b.endAt)}</strong>
              <span className="mj-small mj-muted">{b.number} · {b.area}{b.bayCode ? ` · bay ${b.bayCode}` : ''} · {b.players} {b.players === 1 ? 'player' : 'players'}</span>
            </div>
            {b.status === 'booked' ? (
              <div className="mj-item-end">
                <Chip tone={b.holdsBay ? 'ok' : undefined}>{b.holdsBay ? 'Bay held' : 'Visit'}</Chip>
                <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => void cancel(b)}>Cancel</button>
              </div>
            ) : <StatusChip status={b.status} label={b.bayNow ? `Bay ${b.bayNow}` : b.sessionStatus === 'waiting' ? 'In the queue' : undefined} />}
          </div>
        ))}
      </div>
    </div>
  );
}
