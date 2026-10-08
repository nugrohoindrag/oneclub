import React, { useState } from 'react';
import { Link, useParams } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { qs, request, useGet, type Page, type Schemas } from '@oneclub/api-client';
import { ErrorAlert, Icon, Modal, QRCode, SelectField, Skeleton, TextField, useToast } from '@oneclub/shell';
import { PaymentPanel } from './pay';
import { Check, Chip, dayLabel, downloadICS, Head, initials, money, Rows, StatusChip } from './ui';

// My booking (golf) across the journey: Before Arrival checklist, check-in
// QR, Checked In, Round in progress / completed with the score, the caddy
// of each player (requested ≠ assigned), caddy rating, payment, and
// reschedule / cancel under the Cancellation Policy.

type Booking = Schemas['Booking'];
type Journey = Schemas['BookingJourney'];
type PlayerJ = Schemas['PlayerJourney'];

export function GolfBookingPage() {
  const { id } = useParams();
  const qc = useQueryClient();
  const b = useGet<Booking>(`/api/v1/member/bookings/${id}`, { refetchInterval: 30_000 });
  const j = useGet<Journey>(`/api/v1/member/bookings/${id}/journey`, { refetchInterval: 30_000 });
  const [pay, setPay] = useState<Schemas['Payment'] | null>(null);
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
  const payNow = async () => {
    setError(null);
    try {
      setPay(x.payment && x.payment.status === 'pending' ? x.payment : await request<Schemas['Payment']>('POST', `/api/v1/member/folios/${x.folioId}:pay-online`, { method: 'qris' }));
    } catch (e) {
      setError(e);
    }
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
          <Rows rows={[['Date', dayLabel(x.playDate)], ['Course', x.courseName], ['Score', myScore?.gross ? <strong className="mj-num">{myScore.gross}</strong> : <span className="mj-muted">Not entered</span>]]} />
          {myScore && <div className="mj-actions" style={{ marginTop: 12 }}><Link className="oc-btn oc-btn-outline" to={`/golf/scores/${myScore.id}`}>View Round Detail</Link></div>}
        </div>
      ) : teedOff ? (
        <div className="mj-card">
          <h2><Icon name="sports_golf" size={20} /> Round In Progress</h2>
          <Rows rows={[['Tee Time', x.localTime], ['Teed off', new Date(flight!.teeOffAt!).toLocaleTimeString('en-GB', { timeStyle: 'short' })], ['Players', x.playerCount]]} />
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
              <button className="oc-btn oc-btn-primary" onClick={() => void payNow()}>Pay {money(x.folio?.balance)}</button>
            </div>
          )}
        </div>
      ) : (
        <div className="mj-card"><p className="mj-muted" style={{ margin: 0 }}>{x.status === 'cancelled' ? `Cancelled${x.cancelReason ? ` · ${x.cancelReason}` : ''}.` : 'This booking is closed.'}</p></div>
      )}

      <PlayersCard booking={x} journey={j.data} editable={active} finished={finished} onSaved={() => void j.refetch()} />

      <div className="mj-card">
        <h2><Icon name="receipt_long" size={20} /> Payment</h2>
        <Rows rows={[['Payment', memberCharge ? 'Member account' : x.paymentMode ? x.paymentMode.replace(/_/g, ' ') : '—'],
          ['Charges', <span className="mj-num">{money(x.folio?.charges)}</span>], ['Paid', <span className="mj-num">{money(x.folio?.payments)}</span>],
          ['Balance', <span className="mj-num">{money(x.folio?.balance)}</span>]]} />
        {!memberCharge && balance > 0 && x.status !== 'cancelled' && <div className="mj-actions" style={{ marginTop: 12 }}><button className="oc-btn oc-btn-primary" onClick={() => void payNow()}>Pay now</button></div>}
      </div>

      {active && (
        <div className="mj-actions">
          <button className="oc-btn oc-btn-outline" onClick={() => downloadICS({ title: `Tee Time ${x.code}`, start, end: new Date(start.getTime() + 5 * 3600_000), location: x.courseName,
            description: `${x.playerCount} players · Booking ${x.code}` })}><Icon name="event" size={18} /> Add to Calendar</button>
          <button className="oc-btn oc-btn-neutral" onClick={() => setMode('move')}>Reschedule</button>
          <button className="oc-btn oc-btn-danger" onClick={() => setMode('cancel')}>Cancel Booking</button>
        </div>
      )}

      {mode && <ChangeBooking booking={x} mode={mode} onClose={() => { setMode(''); refresh(); }} />}
      <Modal open={!!pay} onClose={() => { setPay(null); refresh(); }} title="Pay booking" actions={<button className="oc-btn oc-btn-ink" onClick={() => { setPay(null); refresh(); }}>Close</button>}>
        {pay && <PaymentPanel payment={pay} onPaid={refresh} />}
      </Modal>
    </div>
  );
}

const PREF: Record<string, string> = { none: 'No Caddy', any: 'Request Caddy', preferred: 'Preferred' };

function PlayersCard({ booking, journey, editable, finished, onSaved }: { booking: Booking; journey?: Journey; editable: boolean; finished: boolean; onSaved: () => void }) {
  const [edit, setEdit] = useState(false);
  const [draft, setDraft] = useState<Record<string, { preference: string; caddyId?: string }>>({});
  const [error, setError] = useState<unknown>(null);
  const caddies = useGet<Page<Schemas['MemberCaddy']>>(edit ? `/api/v1/member/golf/caddies?date=${booking.playDate}` : null);
  const byId = new Map((journey?.players ?? []).map((p) => [p.playerId, p]));
  const pol = journey?.caddy;
  const save = async () => {
    setError(null);
    try {
      await request('PUT', `/api/v1/member/bookings/${booking.id}/caddy-requests`, {
        players: Object.entries(draft).map(([playerId, d]) => ({ playerId, preference: d.preference, caddyId: d.preference === 'preferred' ? d.caddyId : undefined })),
      });
      setEdit(false);
      setDraft({});
      onSaved();
    } catch (e) {
      setError(e);
    }
  };
  const anyAssigned = (journey?.players ?? []).some((p) => p.caddy);
  return (
    <div className="mj-card">
      <h2><Icon name="hiking" size={20} /> Players & Caddy
        {editable && !anyAssigned && !edit && <button className="oc-btn oc-btn-text oc-btn-sm" style={{ marginLeft: 'auto' }} onClick={() => setEdit(true)}>Change caddy</button>}
      </h2>
      <ErrorAlert error={error} />
      <div className="mj-list">
        {[...booking.players].sort((a, b) => a.seq - b.seq).filter((p) => p.status !== 'removed').map((p) => {
          const pj = byId.get(p.id);
          const d = draft[p.id] ?? { preference: pj?.caddyPreference ?? 'any', caddyId: pj?.preferredCaddyId ?? undefined };
          return (
            <div key={p.id} className="mj-item" style={{ alignItems: 'flex-start', flexWrap: 'wrap' }}>
              <span className="mj-avatar">{initials(p.name || 'TBA')}</span>
              <div className="mj-item-body">
                <strong>{p.name || 'Guest (TBA)'}</strong>
                <span className="mj-small mj-muted">{p.playerType.replace(/_/g, ' ')}{p.status === 'checked_in' ? ' · checked in' : ''}</span>
                {edit && (
                  <div className="oc-row-wrap" style={{ marginTop: 8 }}>
                    <div className="mj-seg" role="group" aria-label={`Caddy for ${p.name || 'guest'}`}>
                      {(['none', 'any', 'preferred'] as const).map((k) => (
                        <button key={k} type="button" aria-pressed={d.preference === k} disabled={(k === 'none' && pol?.mandatory) || (k === 'preferred' && pol && !pol.allowRequest)}
                          onClick={() => setDraft({ ...draft, [p.id]: { ...d, preference: k } })}>{PREF[k]}</button>
                      ))}
                    </div>
                    {d.preference === 'preferred' && (
                      <div style={{ minWidth: 200 }}><SelectField label="Caddy" value={d.caddyId ?? ''} onChange={(v) => setDraft({ ...draft, [p.id]: { ...d, caddyId: v } })} placeholder="Choose"
                        options={(caddies.data?.items ?? []).map((c) => ({ value: c.id, label: `${c.name} · ${c.code} · ⭐ ${c.rating ?? '—'}` }))} /></div>
                    )}
                  </div>
                )}
              </div>
              {!edit && <CaddyState pj={pj} finished={finished} />}
            </div>
          );
        })}
      </div>
      {edit && (
        <div className="mj-actions" style={{ marginTop: 12 }}>
          <button className="oc-btn oc-btn-neutral" onClick={() => { setEdit(false); setDraft({}); }}>Cancel</button>
          <button className="oc-btn oc-btn-primary" disabled={!Object.keys(draft).length} onClick={() => void save()}>Save</button>
        </div>
      )}
      {!edit && journey && journey.players.some((p) => p.caddyPreference !== 'none' && !p.caddy) && !finished && (
        <p className="mj-small mj-muted" style={{ marginBottom: 0 }}>Pending Assignment — the club will assign your caddy before your tee time.</p>
      )}
    </div>
  );
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
  return (
    <div className="mj-item-end">
      <Chip tone="warn">Pending Assignment</Chip>
      {pj.caddyPreference === 'preferred' && pj.preferredCaddyName && <span className="mj-small mj-muted">Preferred: {pj.preferredCaddyName}</span>}
    </div>
  );
}

/** How was your caddy service? 1–5 stars (feeds Caddy Management). */
function RateCaddy({ assignmentId, name }: { assignmentId: string; name: string }) {
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
          options={(slots.data?.items ?? []).filter((s) => s.remaining >= booking.playerCount && s.id !== booking.teeTimeId).map((s) => ({ value: s.id, label: `${s.localTime}${s.startTee > 1 ? ` · tee ${s.startTee}` : ''}` }))} />}
        <TextField label="Reason" value={reason} onChange={setReason} />
        <ErrorAlert error={error} />
      </div>
    </Modal>
  );
}
