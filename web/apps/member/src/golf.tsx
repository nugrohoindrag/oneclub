import React, { useEffect, useState } from 'react';
import { Link, useNavigate } from 'react-router';
import QRCode from 'qrcode';
import { qs, request, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate, formatDateTime, formatMoney } from '@oneclub/i18n';
import {
  AuthFrame, Card, DataTable, ErrorAlert, Icon, Modal, ProfilePage, SelectField, Skeleton, StatusPill, TextField, useAuth, useToast,
} from '@oneclub/shell';

type R = Record<string, unknown> & { id: string };
const money = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : formatMoney(String(v)));
const pill = (k: string) => (r: R) => <StatusPill status={String(r[k] ?? '').replace(/_/g, '-')} />;

function today(offset = 0): string {
  const d = new Date();
  d.setDate(d.getDate() + offset);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

function Head({ title, help, actions }: { title: string; help?: string; actions?: React.ReactNode }) {
  return <div className="oc-page-head"><div><h1>{title}</h1>{help && <p>{help}</p>}</div><span className="oc-spacer" />{actions}</div>;
}

/** QR code image of a value. */
export function QR({ value, size = 200 }: { value: string; size?: number }) {
  const [src, setSrc] = useState('');
  useEffect(() => {
    void QRCode.toDataURL(value, { margin: 1, width: size }).then(setSrc);
  }, [value, size]);
  return src ? <img src={src} width={size} height={size} alt="QR code" style={{ background: '#fff', borderRadius: 12, padding: 8 }} /> : null;
}

// ── OTP login (FR-APP-01) ───────────────────────────────────────────────────

export function OtpLoginPage() {
  const { refresh } = useAuth();
  const nav = useNavigate();
  const [email, setEmail] = useState('');
  const [channel, setChannel] = useState('email');
  const [sent, setSent] = useState(false);
  const [code, setCode] = useState('');
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const go = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      if (!sent) {
        await request('POST', '/api/v1/auth/otp/request', { email, channel });
        setSent(true);
      } else {
        await request('POST', '/api/v1/auth/otp/verify', { email, code });
        await refresh();
        nav('/', { replace: true });
      }
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };
  return (
    <AuthFrame>
      <form className="oc-stack" onSubmit={go}>
        <h1 style={{ fontSize: 32 }}>Log in with a code</h1>
        <p className="oc-muted" style={{ margin: 0 }}>{sent ? 'Enter the 6-digit code we sent you.' : 'We send a one-time code to your e-mail or WhatsApp.'}</p>
        <TextField label="E-mail" type="email" value={email} onChange={setEmail} required disabled={sent} autoComplete="username" />
        {!sent && <SelectField label="Send via" value={channel} onChange={setChannel} options={[{ value: 'email', label: 'E-mail' }, { value: 'whatsapp', label: 'WhatsApp' }]} />}
        {sent && <TextField label="Code" inputMode="numeric" maxLength={6} value={code} onChange={setCode} required autoComplete="one-time-code" autoFocus />}
        <ErrorAlert error={error} />
        <button className="oc-btn oc-btn-ink oc-btn-block" disabled={busy || !email || (sent && code.length !== 6)}>{sent ? 'Log in' : 'Send code'}</button>
        <Link className="oc-small" to="/login">Log in with password</Link>
      </form>
    </AuthFrame>
  );
}

// ── Book Golf (FR-APP-02/03) ────────────────────────────────────────────────

interface PlayerIn { kind: 'self' | 'member' | 'guest'; memberNo: string; name: string; phone: string }

export function BookGolfPage({ browseOnly }: { browseOnly?: boolean }) {
  const toast = useToast();
  const nav = useNavigate();
  const courses = useGet<Page<R>>('/api/v1/member/golf/courses');
  const [courseId, setCourse] = useState('');
  const [date, setDate] = useState(today(1));
  const [session, setSession] = useState('');
  const course = courseId || courses.data?.items[0]?.id || '';
  const avail = useGet<Page<R>>(course ? `/api/v1/member/golf/availability${qs({ courseId: course, date, session })}` : null);
  const [hold, setHold] = useState<R | null>(null);
  const [slot, setSlot] = useState<R | null>(null);
  const [players, setPlayers] = useState<PlayerIn[]>([{ kind: 'self', memberNo: '', name: '', phone: '' }]);
  const holdSend = useSend<Record<string, unknown>, R>('POST', '/api/v1/member/golf/holds');
  const release = useSend<Record<string, unknown>>('DELETE', () => `/api/v1/member/golf/holds/${hold?.id}`);
  const book = useSend<Record<string, unknown>, R>('POST', '/api/v1/member/golf/bookings', ['/api/v1/member']);
  const start = (s: R, n: number) => holdSend.mutate({ teeTimeId: s.id, players: n, channel: 'member_app' }, {
    onSuccess: (h) => { setHold(h); setSlot(s); setPlayers([{ kind: 'self', memberNo: '', name: '', phone: '' }, ...Array.from({ length: n - 1 }, () => ({ kind: 'guest' as const, memberNo: '', name: '', phone: '' }))]); },
  });
  const cancel = () => { if (hold) release.mutate({}); setHold(null); setSlot(null); };
  const submit = () => book.mutate({
    bookingType: 'member', holdId: hold?.id,
    players: players.map((p) => (p.kind === 'self' ? { playerType: 'member' } : p.kind === 'member' ? { playerType: 'member', memberNo: p.memberNo }
      : { playerType: 'guest_of_member', name: p.name || undefined, phone: p.phone || undefined, tba: !p.name, hostIndex: 0 })),
  }, { onSuccess: (b) => { toast(`Booking ${String(b.code)} ${String(b.status)}`); nav(`/bookings?id=${b.id}`); } });
  const set = (i: number, k: keyof PlayerIn, v: string) => setPlayers((ps) => ps.map((p, j) => (j === i ? { ...p, [k]: v } : p)));
  return (
    <div className="oc-stack">
      <Head title={browseOnly ? 'Tee Time' : 'Book Golf'} help="Member Rate applies to you; guests pay the guest rate." />
      <div className="oc-row-wrap">
        {(courses.data?.items.length ?? 0) > 1 && <SelectField label="Course" value={course} onChange={setCourse} options={(courses.data?.items ?? []).map((c) => ({ value: c.id, label: String(c.name) }))} />}
        <TextField label="Date" type="date" value={date} min={today()} onChange={setDate} />
        <SelectField label="Session" value={session} onChange={setSession} placeholder="All"
          options={[{ value: 'morning', label: 'Morning' }, { value: 'afternoon', label: 'Afternoon' }, { value: 'night', label: 'Night Golf' }]} />
      </div>
      <ErrorAlert error={avail.error ?? holdSend.error} />
      {avail.isLoading && <Skeleton rows={6} />}
      <div className="oc-grid">
        {(avail.data?.items ?? []).filter((s) => Number(s.remaining) > 0).map((s) => (
          <div key={s.id} className="oc-card">
            <div className="oc-row"><strong style={{ fontSize: 24 }}>{String(s.localTime)}</strong><span className="oc-spacer" /><span className="oc-muted">tee {String(s.startTee)}</span></div>
            <div className="oc-small oc-muted">{String(s.remaining)} places · {Object.entries((s.prices as Record<string, string>) ?? {}).map(([k, v]) => `${k.replace(/_/g, ' ')} ${money(v)}`).join(' · ')}</div>
            {!browseOnly && (
              <div className="oc-row-wrap" style={{ marginTop: 8 }}>
                {[1, 2, 3, 4].filter((n) => n <= Number(s.remaining) && n >= Number(s.minPlayers ?? 1)).map((n) => (
                  <button key={n} className="oc-chip" style={{ height: 44 }} disabled={holdSend.isPending} onClick={() => start(s, n)}>{n} {n === 1 ? 'player' : 'players'}</button>
                ))}
              </div>
            )}
          </div>
        ))}
      </div>
      {avail.data && (avail.data.items ?? []).length === 0 && <p className="oc-muted">No tee times available on this date.</p>}
      <Modal open={!!hold} onClose={cancel} wide title={`${String(slot?.localTime ?? '')} · ${date}`}
        actions={<><button className="oc-btn oc-btn-neutral" onClick={cancel}>Cancel</button><button className="oc-btn oc-btn-primary" disabled={book.isPending} onClick={submit}>Book</button></>}>
        <p className="oc-muted">Held for you until {hold ? formatDateTime(String(hold.expiresAt)) : ''}. Payment: member charge to your account.</p>
        {players.map((p, i) => (
          <div className="oc-row-wrap" key={i}>
            {i === 0 ? <strong>You</strong> : (
              <>
                <SelectField label={`Player ${i + 1}`} value={p.kind} onChange={(v) => set(i, 'kind', v)} options={[{ value: 'guest', label: 'Guest' }, { value: 'member', label: 'Another member' }]} />
                {p.kind === 'member' ? <TextField label="Member No." value={p.memberNo} onChange={(v) => set(i, 'memberNo', v)} />
                  : <><TextField label="Guest name (empty = TBA)" value={p.name} onChange={(v) => set(i, 'name', v)} /><TextField label="Phone" value={p.phone} onChange={(v) => set(i, 'phone', v)} /></>}
              </>
            )}
          </div>
        ))}
        <ErrorAlert error={book.error} />
      </Modal>
    </div>
  );
}

// ── My bookings (FR-APP-04) ─────────────────────────────────────────────────

export function MyBookingsPage() {
  const params = new URLSearchParams(window.location.search);
  const [open, setOpen] = useState<string | null>(params.get('id'));
  const list = useGet<Page<R>>('/api/v1/member/bookings');
  return (
    <div className="oc-stack">
      <Head title="Bookings" actions={<Link className="oc-btn oc-btn-primary" to="/golf">Book Golf</Link>} />
      <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error} onRowClick={(r) => setOpen(r.id)}
        columns={[{ key: 'playDate', header: 'Date' }, { key: 'localTime', header: 'Tee Time' }, { key: 'code', header: 'Booking' }, { key: 'playerCount', header: 'Players', align: 'right' },
          { key: 'status', header: 'Status', render: pill('status') }]} />
      {open && <BookingModal id={open} onClose={() => setOpen(null)} />}
    </div>
  );
}

function BookingModal({ id, onClose }: { id: string; onClose: () => void }) {
  const b = useGet<R & { players: R[]; folio?: R }>(`/api/v1/member/bookings/${id}`);
  const [mode, setMode] = useState<'' | 'cancel' | 'move'>('');
  const [reason, setReason] = useState('');
  const [slot, setSlot] = useState('');
  const x = b.data;
  const slots = useGet<Page<R>>(mode === 'move' && x ? `/api/v1/member/golf/availability${qs({ courseId: String(x.courseId), date: String(x.playDate) })}` : null);
  const cancel = useSend<Record<string, unknown>>('POST', `/api/v1/member/bookings/${id}:cancel`, ['/api/v1/member']);
  const move = useSend<Record<string, unknown>>('POST', `/api/v1/member/bookings/${id}:reschedule`, ['/api/v1/member']);
  const active = x && ['pending', 'confirmed'].includes(String(x.status));
  return (
    <Modal open onClose={onClose} title={x ? `Booking ${String(x.code)}` : 'Booking'} actions={<>
      {active && mode === '' && <><button className="oc-btn oc-btn-neutral" onClick={() => setMode('move')}>Reschedule</button><button className="oc-btn oc-btn-danger" onClick={() => setMode('cancel')}>Cancel booking</button></>}
      {mode === 'cancel' && <button className="oc-btn oc-btn-danger" disabled={!reason || cancel.isPending} onClick={() => cancel.mutate({ reason }, { onSuccess: onClose })}>Confirm cancellation</button>}
      {mode === 'move' && <button className="oc-btn oc-btn-primary" disabled={!slot || !reason || move.isPending} onClick={() => move.mutate({ teeTimeId: slot, reason }, { onSuccess: onClose })}>Reschedule</button>}
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button></>}>
      {!x && <Skeleton />}
      {x && (
        <div className="oc-stack">
          <div className="oc-row-wrap">
            {x.status === 'confirmed' && x.qrToken ? <QR value={`oneclub:booking:${String(x.qrToken)}`} size={160} /> : null}
            <div>
              <div style={{ fontSize: 22, fontWeight: 600 }}>{String(x.playDate)} · {String(x.localTime)}</div>
              <div className="oc-muted">{String(x.courseName)}</div>
              <StatusPill status={String(x.status).replace(/_/g, '-')} />
              <div>Total {money(x.folio?.charges)}</div>
            </div>
          </div>
          <ul>{x.players.map((p) => <li key={p.id}>{String(p.name)} · {String(p.playerType).replace(/_/g, ' ')} · {money(p.priceTotal)}</li>)}</ul>
          {mode !== '' && <TextField label="Reason" value={reason} onChange={setReason} />}
          {mode === 'cancel' && <p className="oc-muted">Free cancellation until the time set by the Cancellation Policy; later a fee applies.</p>}
          {mode === 'move' && <SelectField label="New tee time" value={slot} onChange={setSlot}
            options={(slots.data?.items ?? []).filter((s) => Number(s.remaining) >= Number(x.playerCount) && s.id !== x.teeTimeId).map((s) => ({ value: s.id, label: `${String(s.localTime)} · tee ${String(s.startTee)}` }))} />}
          <ErrorAlert error={cancel.error ?? move.error} />
        </div>
      )}
    </Modal>
  );
}

// ── My flights / caddy / golf cart ──────────────────────────────────────────

/** Rate the caddy after the round (PRD P2 EP-05): 1–5 stars and a comment. */
function RateCaddy({ assignmentId, caddy }: { assignmentId: string; caddy: string }) {
  const toast = useToast();
  const [rating, setRating] = useState(0);
  const [comment, setComment] = useState('');
  const [done, setDone] = useState(false);
  const rate = useSend('POST', `/api/v1/member/golf/caddy-assignments/${assignmentId}:rate`, []);
  if (done) return <div className="oc-small oc-muted">Thank you for rating {caddy}.</div>;
  return (
    <div className="oc-stack" style={{ gap: 4 }}>
      <div className="oc-row-wrap" role="group" aria-label={`Rate caddy ${caddy}`}>
        {[1, 2, 3, 4, 5].map((n) => (
          <button key={n} className="oc-icon-btn" aria-label={`${n} of 5`} aria-pressed={rating === n} onClick={() => setRating(n)}>
            <Icon name="star" filled={rating >= n} size={22} />
          </button>
        ))}
        <input className="oc-input" style={{ maxWidth: 260 }} placeholder="Comment (optional)" aria-label={`Comment for ${caddy}`} value={comment}
          onChange={(e) => setComment(e.target.value)} />
        <button className="oc-btn oc-btn-ink oc-btn-sm" disabled={!rating || rate.isPending}
          onClick={() => rate.mutate({ rating, comment }, { onSuccess: () => { setDone(true); toast('Rating sent'); } })}>Rate caddy</button>
      </div>
      <ErrorAlert error={rate.error} />
    </div>
  );
}

export function MyFlightsPage({ focus }: { focus?: 'caddy' | 'cart' }) {
  const list = useGet<Page<R>>('/api/v1/member/golf/my-flights');
  const title = focus === 'caddy' ? 'My Caddy' : focus === 'cart' ? 'My Golf Cart' : 'My Flights';
  return (
    <div className="oc-stack">
      <Head title={title} help="Today's and upcoming flights." />
      {list.isLoading && <Skeleton />}
      {list.data?.items.length === 0 && <p className="oc-muted">No flights yet.</p>}
      {(list.data?.items ?? []).map((f) => (
        <Card key={String(f.bookingId) + String(f.localTime)} title={`${String(f.localTime)} · ${String(f.courseName)}`} icon="golf_course">
          <div className="oc-row-wrap"><StatusPill status={String(f.status).replace(/_/g, '-')} /><span className="oc-muted">{String(f.bookingCode)}</span></div>
          {focus !== 'cart' && <ul>{((f.players as R[]) ?? []).map((p) => (
            <li key={String(p.id)}>{String(p.name)}{p.caddyName ? ` · caddy ${String(p.caddyCode)} ${String(p.caddyName)}` : focus === 'caddy' ? ' · caddy not assigned yet' : ''}
              {focus === 'caddy' && f.status === 'completed' && p.caddyAssignmentId ? <RateCaddy assignmentId={String(p.caddyAssignmentId)} caddy={String(p.caddyName)} /> : null}
            </li>
          ))}</ul>}
          {focus !== 'caddy' && <div>Golf carts: {((f.golfCarts as string[]) ?? []).join(', ') || 'not assigned yet'}</div>}
        </Card>
      ))}
    </div>
  );
}

// ── Membership (FR-APP-05) ──────────────────────────────────────────────────

const CARD_KEY = 'oneclub.memberCard';

function useMyMembership() {
  const m = useGet<{ profile: R; card?: R; benefits: string[]; clubName: string }>('/api/v1/member/membership');
  useEffect(() => {
    try {
      if (m.data?.card) localStorage.setItem(CARD_KEY, JSON.stringify({ card: m.data.card, name: m.data.profile.name, club: m.data.clubName }));
    } catch { /* ignore */ }
  }, [m.data]);
  return m;
}

export function MyMembershipPage() {
  const m = useMyMembership();
  const p = m.data?.profile;
  return (
    <div className="oc-stack">
      <Head title="My Membership" />
      <ErrorAlert error={m.error} />
      {m.isLoading && <Skeleton />}
      {p && (
        <>
          <div className="oc-grid-2">
            <MemberCard name={String(p.name)} club={m.data!.clubName} card={m.data!.card} />
            <Card title="Membership" icon="card_membership">
              <DataTable rows={(p.memberships as R[]) ?? []} columns={[{ key: 'typeName', header: 'Type' }, { key: 'role', header: 'Role' }, { key: 'endsOn', header: 'Valid until' },
                { key: 'status', header: 'Status', render: pill('status') }]} />
              {p.account ? <p>Member account balance <strong>{money((p.account as R).balance)}</strong></p> : null}
            </Card>
          </div>
          <MyApplications />
        </>
      )}
    </div>
  );
}

function MyApplications() {
  const apps = useGet<Page<R>>('/api/v1/member/membership-applications');
  if (!apps.data?.items.length) return null;
  return (
    <Card title="My applications" icon="assignment">
      <DataTable rows={apps.data.items} columns={[{ key: 'number', header: 'Application' }, { key: 'typeName', header: 'Type' }, { key: 'status', header: 'Status', render: pill('status') }]} />
    </Card>
  );
}

export function MemberCard({ name, club, card }: { name: string; club: string; card?: R | null }) {
  return (
    <div className="oc-card oc-card-ink" style={{ minHeight: 220, display: 'flex', flexDirection: 'column', gap: 12 }}>
      <div className="oc-row"><strong>{club}</strong><span className="oc-spacer" /><Icon name="contactless" size={24} /></div>
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        {card?.qrToken ? <QR value={`oneclub:card:${String(card.qrToken)}`} size={140} /> : null}
        <div>
          <div className="oc-small" style={{ opacity: 0.7 }}>Digital Member Card</div>
          <div style={{ fontSize: 22, fontWeight: 600 }}>{name}</div>
          {card && <div className="oc-small" style={{ opacity: 0.8 }}>{String(card.cardNumber)} · valid until {card.validUntil ? formatDate(String(card.validUntil)) : '—'}</div>}
        </div>
      </div>
    </div>
  );
}

/** Digital Member Card, available offline from the last copy (FR-APP-05). */
export function CardPage() {
  const m = useMyMembership();
  let cached: { card: R; name: string; club: string } | null = null;
  try {
    cached = JSON.parse(localStorage.getItem(CARD_KEY) ?? 'null');
  } catch { /* ignore */ }
  const card = m.data?.card ?? cached?.card;
  return (
    <div className="oc-stack">
      <Head title="Digital Member Card" help="Show the QR code at check-in. It also works offline." />
      {card ? <MemberCard name={String(m.data?.profile.name ?? cached?.name ?? '')} club={String(m.data?.clubName ?? cached?.club ?? '')} card={card} />
        : m.isLoading ? <Skeleton /> : <p className="oc-muted">No card issued yet.</p>}
    </div>
  );
}

export function BenefitsPage() {
  const m = useMyMembership();
  return (
    <div className="oc-stack">
      <Head title="Membership Benefits" />
      <Card title="Your benefits" icon="workspace_premium"><ul>{(m.data?.benefits ?? []).map((b) => <li key={b}>{b}</li>)}</ul></Card>
    </div>
  );
}

export function FamilyPage() {
  const m = useMyMembership();
  return (
    <div className="oc-stack">
      <Head title="Family Members" />
      <DataTable rows={(m.data?.profile.family as R[]) ?? []} loading={m.isLoading} columns={[{ key: 'name', header: 'Name' }, { key: 'relationship', header: 'Relationship' },
        { key: 'memberNo', header: 'Member No.' }, { key: 'status', header: 'Status', render: pill('status') }]} />
    </div>
  );
}

export function StatementsPage() {
  const list = useGet<Page<R>>('/api/v1/member/statements');
  return (
    <div className="oc-stack">
      <Head title="Membership Statement" />
      <DataTable rows={list.data?.items} loading={list.isLoading} columns={[{ key: 'periodStart', header: 'Period', render: (s) => `${String(s.periodStart)} – ${String(s.periodEnd)}` },
        { key: 'totalCharges', header: 'Charges', align: 'right', render: (s) => money(s.totalCharges) }, { key: 'totalPayments', header: 'Payments', align: 'right', render: (s) => money(s.totalPayments) },
        { key: 'closingBalance', header: 'Balance', align: 'right', render: (s) => money(s.closingBalance) }]}
        actions={(s) => (s.hasPdf ? <a className="oc-btn oc-btn-sm oc-btn-text" href={`/api/v1/member/statements/${s.id}/pdf`} target="_blank" rel="noreferrer">PDF</a> : null)} />
    </div>
  );
}

// ── Transactions (FR-APP-06) ────────────────────────────────────────────────

export function TransactionsPage() {
  const t = useGet<{ account?: R; folios: (R & { summary: R; lines: R[] })[] }>('/api/v1/member/transactions');
  return (
    <div className="oc-stack">
      <Head title="My Transactions" />
      {t.isLoading && <Skeleton />}
      {t.data?.account && <Card title="Member account" icon="account_balance_wallet"><div className="oc-metric">{money(t.data.account.balance)}</div></Card>}
      <DataTable rows={t.data?.folios} columns={[{ key: 'number', header: 'Folio' }, { key: 'sourceRef', header: 'For' },
        { key: 'summary', header: 'Total', align: 'right', render: (f) => money((f.summary as R)?.charges) }, { key: 'status', header: 'Status', render: pill('status') }]} />
    </div>
  );
}

export function MyPaymentsPage() {
  const list = useGet<Page<R>>('/api/v1/member/payments');
  return (
    <div className="oc-stack">
      <Head title="Payments" />
      <DataTable rows={list.data?.items} loading={list.isLoading} columns={[{ key: 'number', header: 'Payment' }, { key: 'methodType', header: 'Method', render: (p) => String(p.methodType).replace(/_/g, ' ') },
        { key: 'amount', header: 'Amount', align: 'right', render: (p) => money(p.amount) }, { key: 'paidAt', header: 'Paid', render: (p) => (p.paidAt ? formatDateTime(String(p.paidAt)) : '—') },
        { key: 'status', header: 'Status', render: pill('status') }]}
        actions={(p) => (p.status === 'completed' ? <a className="oc-btn oc-btn-sm oc-btn-text" href={`/api/v1/member/payments/${p.id}/receipt`} target="_blank" rel="noreferrer">Receipt</a> : null)} />
    </div>
  );
}

export function MyChargesPage() {
  const list = useGet<Page<R>>('/api/v1/member/member-charges');
  return (
    <div className="oc-stack">
      <Head title="Member Charges" />
      <DataTable rows={list.data?.items} loading={list.isLoading} columns={[{ key: 'occurredAt', header: 'When', render: (e) => formatDateTime(String(e.occurredAt)) },
        { key: 'description', header: 'Description' }, { key: 'amount', header: 'Amount', align: 'right', render: (e) => money(e.amount) }]} />
    </div>
  );
}

// ── Profile (FR-APP-07) ─────────────────────────────────────────────────────

export function MemberProfilePage() {
  const p = useGet<R>('/api/v1/member/profile');
  const [phone, setPhone] = useState<string | null>(null);
  const [optIn, setOptIn] = useState<boolean | null>(null);
  const save = useSend<Record<string, unknown>>('PATCH', '/api/v1/member/profile', ['/api/v1/member/profile']);
  const toast = useToast();
  return (
    <div className="oc-stack">
      <ProfilePage />
      {p.data && (
        <Card title="Member profile" icon="badge">
          <div className="oc-form">
            <TextField label="Phone" value={phone ?? String(p.data.phone ?? '')} onChange={setPhone} />
            <SelectField label="Club news and offers" value={String(optIn ?? Boolean(p.data.marketingOptIn))} onChange={(v) => setOptIn(v === 'true')}
              options={[{ value: 'true', label: 'Yes, send me news' }, { value: 'false', label: 'No' }]} />
          </div>
          <button className="oc-btn oc-btn-ink" disabled={save.isPending} onClick={() => save.mutate({ phone: phone ?? undefined, marketingOptIn: optIn ?? undefined, consent: true },
            { onSuccess: () => toast('Profile saved') })}>Save</button>
          <ErrorAlert error={save.error} />
        </Card>
      )}
    </div>
  );
}

/** Home shortcuts (P1 features live). */
export function HomeShortcuts() {
  const items: [string, string, string][] = [['golf_course', 'Book Golf', '/golf'], ['event_available', 'My Bookings', '/bookings'],
    ['qr_code_2', 'Digital Member Card', '/membership/card'], ['receipt_long', 'My Transactions', '/transactions']];
  return (
    <div className="oc-grid">
      {items.map(([icon, label, to]) => (
        <Link key={label} to={to} className="oc-card" style={{ textDecoration: 'none' }}>
          <div className="oc-card-head" style={{ marginBottom: 6 }}><span className="oc-icon-circle"><Icon name={icon} size={20} /></span><h3>{label}</h3></div>
        </Link>
      ))}
    </div>
  );
}
