import React, { useState } from 'react';
import { Link, useParams } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { qs, request, useGet, type Page, type Schemas } from '@oneclub/api-client';
import { ErrorAlert, Icon, Modal, PlayTime, QRCode, SelectField, Skeleton, TextField, useToast } from '@oneclub/shell';
import { MethodPicker, PaymentPanel } from './pay';
import { Check, Chip, dayLabel, downloadICS, Head, initials, money, Rows, StatusChip } from './ui';

// My booking (golf) across the journey: Before Arrival checklist, check-in
// QR, Checked In, Round in progress / completed with the score, the caddy
// the front desk assigned to each player, caddy rating, payment of the
// balance in full or in part, and reschedule / cancel under the
// Cancellation Policy.

type Booking = Schemas['Booking'];
type Journey = Schemas['BookingJourney'];
type PlayerJ = Schemas['PlayerJourney'];

export function GolfBookingPage() {
  const { id } = useParams();
  const qc = useQueryClient();
  const b = useGet<Booking>(`/api/v1/member/bookings/${id}`, { refetchInterval: 30_000 });
  const j = useGet<Journey>(`/api/v1/member/bookings/${id}/journey`, { refetchInterval: 30_000 });
  const [pay, setPay] = useState<Schemas['Payment'] | null>(null);
  const [amountOpen, setAmountOpen] = useState(false);
  const [mode, setMode] = useState<'' | 'cancel' | 'move'>('');
  const [error, setError] = useState<unknown>(null);
  const x = b.data;
  if (b.error) return <div className="mj-page mj-narrow"><Head title="Booking" back={['/activity', 'My Activity']} /><ErrorAlert error={b.error} /></div>;
  if (!x) return <div className="mj-page mj-narrow"><Skeleton rows={8} /></div>;
  const flight = x.flights[0];
  const start = new Date(x.startAt);
  const active = ['pending', 'confirmed'].includes(x.status);
  const checkedIn = x.status === 'checked_in' || x.status === 'completed';
  const teedOff = !!flight?.teeOffAt;
  const finished = x.status === 'completed' || !!flight?.roundFinishAt;
  const balance = Number(x.folio?.balance ?? 0);
  const memberCharge = x.paymentMode === 'member_charge';
  const paid = memberCharge || (x.folio ? balance <= 0 : false);
  const players = j.data?.players ?? [];
  const wantCaddy = players.filter((p) => p.caddyPreference !== 'none');
  const caddyDone = wantCaddy.length > 0 && wantCaddy.every((p) => p.caddy);
  const mine = x.players.find((p) => p.customerId && p.customerId === x.customerId) ?? x.players[0];
  const myScore = players.find((p) => p.playerId === mine?.id)?.scorecard;
  // a pending checkout is reused; otherwise the member picks the amount (all or part)
  const payNow = () => {
    setError(null);
    if (x.payment && x.payment.status === 'pending') setPay(x.payment);
    else setAmountOpen(true);
  };
  const refresh = () => { void qc.invalidateQueries(); };
  return (
    <div className="mj-page mj-narrow">
      <Head title={`Tee Time · ${x.localTime}`} back={['/activity', 'My Activity']} help={`${dayLabel(x.playDate)} · ${x.courseName}`}
        actions={<StatusChip status={x.status} label={x.status === 'pending' ? 'Awaiting payment' : undefined} />} />
      <ErrorAlert error={error} />

      {/* the stage of the visit */}
      {finished ? (
        <div className="mj-card">
          <h2><Icon name="flag" size={20} /> Round Completed</h2>
          <Rows rows={[['Date', dayLabel(x.playDate)], ['Course', x.courseName],
            ['Play time', <PlayTime start={flight?.teeOffAt} end={flight?.roundFinishAt} label={false} fallback="—" />],
            ['Score', myScore?.gross ? <strong className="mj-num">{myScore.gross}</strong> : <span className="mj-muted">Not entered</span>]]} />
          {myScore && <div className="mj-actions" style={{ marginTop: 12 }}><Link className="oc-btn oc-btn-outline" to={`/golf/scores/${myScore.id}`}>View Round Detail</Link></div>}
        </div>
      ) : teedOff ? (
        <div className="mj-card">
          <h2><Icon name="sports_golf" size={20} /> Round In Progress</h2>
          <Rows rows={[['Tee Time', x.localTime], ['Teed off', new Date(flight!.teeOffAt!).toLocaleTimeString('en-GB', { timeStyle: 'short' })],
            ['Playing for', <PlayTime start={flight!.teeOffAt} label={false} />], ['Players', x.playerCount]]} />
          {myScore && <div className="mj-actions" style={{ marginTop: 12 }}><Link className="oc-btn oc-btn-primary" to={`/golf/scores/${myScore.id}`}>Scorecard</Link></div>}
        </div>
      ) : checkedIn ? (
        <div className="mj-card mj-success">
          <span className="mj-success-mark"><Icon name="how_to_reg" size={34} /></span>
          <h1>Checked In</h1>
          <Rows rows={[['Tee Time', x.localTime], ['Flight', flight ? `${flight.flightNo}${flight.startTee > 1 ? ` · tee ${flight.startTee}` : ''}` : '—'], ['Players', x.playerCount]]} />
          <div className="mj-muted">Enjoy your round.</div>
        </div>
      ) : active ? (
        <div className="mj-grid-2">
          <div className="mj-card">
            <h2><Icon name="checklist" size={20} /> Your Checklist</h2>
            <div className="mj-checklist">
              <Check done={x.status === 'confirmed'}>Tee Time confirmed</Check>
              <Check done={!x.players.some((p) => p.tba)}>Players confirmed{x.players.some((p) => p.tba) ? ' (guest names to complete)' : ''}</Check>
              <Check done={caddyDone || wantCaddy.length === 0}>Caddy {wantCaddy.length === 0 ? 'not requested' : caddyDone ? 'assigned' : 'pending assignment'}</Check>
              <Check done={paid}>Payment {memberCharge ? '(member account)' : ''}</Check>
              <Check done={false}>Check-in</Check>
              <Check done={false}>Start round</Check>
            </div>
          </div>
          {x.status === 'confirmed' && x.qrToken ? (
            <div className="mj-card mj-success">
              <h2 style={{ margin: 0 }}>Check-in QR</h2>
              <QRCode value={`oneclub:booking:${x.qrToken}`} size={180} label="Check-in QR" />
              <div className="mj-small mj-muted">Arrive 30 minutes before your tee time and show this QR at the clubhouse.</div>
              <span className="mj-code">{x.code}</span>
            </div>
          ) : (
            <div className="mj-card">
              <h2><Icon name="payments" size={20} /> Payment needed</h2>
              <p className="mj-muted">Your tee time is held; it is confirmed once the payment is received.</p>
              <button className="oc-btn oc-btn-primary" onClick={payNow}>Pay now</button>
            </div>
          )}
        </div>
      ) : (
        <div className="mj-card"><p className="mj-muted" style={{ margin: 0 }}>{x.status === 'cancelled' ? `Cancelled${x.cancelReason ? ` · ${x.cancelReason}` : ''}.` : 'This booking is closed.'}</p></div>
      )}

      <PlayersCard booking={x} journey={j.data} finished={finished} />

      <div className="mj-card">
        <h2><Icon name="receipt_long" size={20} /> Payment</h2>
        <Rows rows={[['Payment', memberCharge ? 'Member account' : x.paymentMode ? x.paymentMode.replace(/_/g, ' ') : '—'],
          ['Charges', <span className="mj-num">{money(x.folio?.charges)}</span>], ['Paid', <span className="mj-num">{money(x.folio?.payments)}</span>],
          ['Balance', <span className="mj-num">{money(x.folio?.balance)}</span>]]} />
        {!memberCharge && balance > 0 && x.status !== 'cancelled' && (
          <>
            <p className="mj-small mj-muted">Pay all or part of the balance here, or at the front desk.</p>
            <div className="mj-actions"><button className="oc-btn oc-btn-primary" onClick={payNow}>Pay now</button></div>
          </>
        )}
      </div>

      {active && (
        <div className="mj-actions">
          <button className="oc-btn oc-btn-outline" onClick={() => downloadICS({ title: `Tee Time ${x.code}`, start, end: new Date(start.getTime() + 5 * 3600_000), location: x.courseName,
            description: `${x.playerCount} players · Booking ${x.code}` })}><Icon name="event" size={18} /> Add to Calendar</button>
          <button className="oc-btn oc-btn-neutral" onClick={() => setMode('move')}>Reschedule</button>
          <button className="oc-btn oc-btn-danger" onClick={() => setMode('cancel')}>Cancel Booking</button>
        </div>
      )}

      {amountOpen && x.folioId && <PayAmount folioId={x.folioId} balance={balance} onClose={() => setAmountOpen(false)} onPayment={(p) => { setAmountOpen(false); setPay(p); }} />}
      {mode && <ChangeBooking booking={x} mode={mode} onClose={() => { setMode(''); refresh(); }} />}
      <Modal open={!!pay} onClose={() => { setPay(null); refresh(); }} title="Pay booking" actions={<button className="oc-btn oc-btn-ink" onClick={() => { setPay(null); refresh(); }}>Close</button>}>
        {pay && <PaymentPanel payment={pay} onPaid={refresh} />}
      </Modal>
    </div>
  );
}

function PlayersCard({ booking, journey, finished }: { booking: Booking; journey?: Journey; finished: boolean }) {
  const byId = new Map((journey?.players ?? []).map((p) => [p.playerId, p]));
  return (
    <div className="mj-card">
      <h2><Icon name="hiking" size={20} /> Players & Caddy</h2>
      <div className="mj-list">
        {[...booking.players].sort((a, b) => a.seq - b.seq).filter((p) => p.status !== 'removed').map((p) => (
          <div key={p.id} className="mj-item" style={{ alignItems: 'flex-start', flexWrap: 'wrap' }}>
            <span className="mj-avatar">{initials(p.name || 'TBA')}</span>
            <div className="mj-item-body">
              <strong>{p.name || 'Guest (TBA)'}</strong>
              <span className="mj-small mj-muted">{p.playerType.replace(/_/g, ' ')}{p.status === 'checked_in' ? ' · checked in' : ''}</span>
              <PlayerPlayTime booking={booking} flightId={p.flightId} />
            </div>
            <CaddyState pj={byId.get(p.id)} finished={finished} />
          </div>
        ))}
      </div>
      {journey && journey.players.some((p) => p.caddyPreference !== 'none' && !p.caddy) && !finished && (
        <p className="mj-small mj-muted" style={{ marginBottom: 0 }}>The caddy is included in your rate; the front desk assigns one before your tee time.</p>
      )}
    </div>
  );
}

/** The play time of a player: the flight they play in (tee-off → finish). */
function PlayerPlayTime({ booking, flightId }: { booking: Booking; flightId: string }) {
  const f = booking.flights.find((x) => x.id === flightId);
  if (!f?.teeOffAt) return null;
  return <PlayTime start={f.teeOffAt} end={f.roundFinishAt} className="oc-playtime mj-small" />;
}

function CaddyState({ pj, finished }: { pj?: PlayerJ; finished: boolean }) {
  if (!pj) return null;
  if (pj.caddy) {
    return (
      <div className="mj-item-end">
        <Chip tone="ok"><Icon name="check" size={14} /> {pj.caddy.name}</Chip>
        <span className="mj-small mj-muted">Caddy No. {pj.caddy.code}{pj.caddy.accepted ? ' · accepted' : ''}</span>
        {finished && !pj.caddy.rated && <RateCaddy assignmentId={pj.caddy.assignmentId} name={pj.caddy.name} />}
        {pj.caddy.rated && <span className="mj-small mj-muted">Rated — thank you</span>}
      </div>
    );
  }
  if (pj.caddyPreference === 'none') return <div className="mj-item-end"><Chip>No Caddy</Chip></div>;
  return <div className="mj-item-end"><Chip tone="warn">Assigned by the front desk</Chip></div>;
}

/** Pay the balance or any part of it online (QRIS, VA, card). */
function PayAmount({ folioId, balance, onClose, onPayment }: { folioId: string; balance: number; onClose: () => void; onPayment: (p: Schemas['Payment']) => void }) {
  const [amount, setAmount] = useState(String(Math.round(balance)));
  const [method, setMethod] = useState<'qris' | 'virtual_account' | 'card'>('qris');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const n = Number(amount);
  const go = async () => {
    setBusy(true);
    setError(null);
    try {
      onPayment(await request<Schemas['Payment']>('POST', `/api/v1/member/folios/${folioId}:pay-online`, { method, amount }));
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal open onClose={onClose} title="Pay booking" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Back</button>
      <button className="oc-btn oc-btn-primary" disabled={busy || !(n > 0 && n <= balance)} onClick={() => void go()}>Pay {money(n)}</button></>}>
      <div className="oc-stack">
        <TextField label={`Amount (balance ${money(balance)})`} value={amount} onChange={(v) => setAmount(v.replace(/\D/g, ''))} inputMode="numeric" />
        <div className="oc-row-wrap">
          <button type="button" className="oc-chip" aria-pressed={n === Math.round(balance)} onClick={() => setAmount(String(Math.round(balance)))}>Full balance</button>
          <button type="button" className="oc-chip" aria-pressed={n === Math.round(balance / 2)} onClick={() => setAmount(String(Math.round(balance / 2)))}>Half</button>
        </div>
        <MethodPicker value={method} onChange={(m) => setMethod(m as 'qris' | 'virtual_account' | 'card')} memberCharge={false} />
        <ErrorAlert error={error} />
      </div>
    </Modal>
  );
}

/** How was your caddy service? 1–5 stars (feeds Caddy Management). */
export function RateCaddy({ assignmentId, name }: { assignmentId: string; name: string }) {
  const toast = useToast();
  const qc = useQueryClient();
  const [rating, setRating] = useState(0);
  const [comment, setComment] = useState('');
  const [error, setError] = useState<unknown>(null);
  const send = async () => {
    try {
      await request('POST', `/api/v1/member/golf/caddy-assignments/${assignmentId}:rate`, { rating, comment });
      toast('Thank you for your review');
      void qc.invalidateQueries();
    } catch (e) {
      setError(e);
    }
  };
  return (
    <div className="oc-stack" style={{ gap: 6, alignItems: 'flex-end' }}>
      <div className="oc-row" role="group" aria-label={`Rate ${name}`}>
        {[1, 2, 3, 4, 5].map((n) => (
          <button key={n} type="button" className="oc-icon-btn" aria-label={`${n} of 5`} aria-pressed={rating === n} onClick={() => setRating(n)}>
            <Icon name="star" filled={rating >= n} size={22} />
          </button>
        ))}
      </div>
      {rating > 0 && <input className="oc-input" placeholder="Comment (optional)" aria-label="Comment" value={comment} onChange={(e) => setComment(e.target.value)} />}
      <ErrorAlert error={error} />
      <button className="oc-btn oc-btn-primary oc-btn-sm" disabled={!rating} onClick={() => void send()}>Submit Review</button>
    </div>
  );
}

function ChangeBooking({ booking, mode, onClose }: { booking: Booking; mode: 'cancel' | 'move'; onClose: () => void }) {
  const [reason, setReason] = useState('');
  const [slot, setSlot] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const slots = useGet<Page<Schemas['AvailableSlot']>>(mode === 'move' ? `/api/v1/member/golf/availability${qs({ courseId: booking.courseId, date: booking.playDate })}` : null);
  const go = async () => {
    setBusy(true);
    setError(null);
    try {
      if (mode === 'cancel') await request('POST', `/api/v1/member/bookings/${booking.id}:cancel`, { reason });
      else await request('POST', `/api/v1/member/bookings/${booking.id}:reschedule`, { teeTimeId: slot, reason });
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal open onClose={onClose} title={mode === 'cancel' ? 'Cancel booking' : 'Reschedule'} actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Back</button>
      <button className={`oc-btn ${mode === 'cancel' ? 'oc-btn-danger' : 'oc-btn-primary'}`} disabled={busy || !reason || (mode === 'move' && !slot)} onClick={() => void go()}>
        {mode === 'cancel' ? 'Confirm cancellation' : 'Reschedule'}</button></>}>
      <div className="oc-stack">
        {mode === 'cancel' && <p className="mj-muted" style={{ margin: 0 }}>Free cancellation until the time set by the club's Cancellation Policy; after that a fee applies.</p>}
        {mode === 'move' && <SelectField label="New tee time (same day)" value={slot} onChange={setSlot} placeholder="Choose"
          options={(slots.data?.items ?? []).filter((s) => s.status !== 'blocked' && s.id !== booking.teeTimeId)
            .map((s) => ({ value: s.id, label: `${s.localTime}${s.startTee > 1 ? ` · tee ${s.startTee}` : ''} · ${s.crowd === 'peak' ? '🔴 peak' : '🟢 quiet'}` }))} />}
        <TextField label="Reason" value={reason} onChange={setReason} />
        <ErrorAlert error={error} />
      </div>
    </Modal>
  );
}
