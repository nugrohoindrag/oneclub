import React, { useEffect, useState } from 'react';
import { Link } from 'react-router';
import { qs, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDateTime, formatMoney } from '@oneclub/i18n';
import { cacheGet, cachePut, enqueue, useOnline } from '@oneclub/offline';
import {
  Card, DataTable, ErrorAlert, Icon, Modal, PlayTime, SelectField, StatusPill, TextField, useAuth, useToast,
} from '@oneclub/shell';

type R = Record<string, unknown> & { id: string };

const pill = (k: string) => (r: R) => <StatusPill status={String(r[k] ?? '').replace(/_/g, '-')} />;
export const money = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : formatMoney(String(v)));

export function today(): string {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

export function Head({ title, help, actions }: { title: string; help?: string; actions?: React.ReactNode }) {
  return (
    <div className="oc-page-head">
      <div><h1>{title}</h1>{help && <p>{help}</p>}</div>
      <span className="oc-spacer" />
      {actions}
    </div>
  );
}

/** GET with an offline copy per property (FR-OPS-05): online responses are
 * cached; offline the last copy is shown. */
export function useCached<T>(path: string | null, name: string) {
  const { propertyId } = useAuth();
  const online = useOnline();
  const live = useGet<T>(online ? path : null);
  const [cached, setCached] = useState<T | undefined>();
  useEffect(() => {
    if (live.data && path) void cachePut(propertyId, name, live.data);
  }, [live.data, path, propertyId, name]);
  useEffect(() => {
    if (!online) void cacheGet<T>(propertyId, name).then(setCached);
  }, [online, propertyId, name]);
  return { data: online ? live.data : cached, isLoading: online && live.isLoading, error: online ? live.error : null, refetch: live.refetch, offline: !online };
}

function useStream(path: string | null, onEvent: () => void) {
  const { propertyId } = useAuth();
  useEffect(() => {
    if (!path || typeof EventSource === 'undefined') return;
    const es = new EventSource(`${path}&propertyId=${propertyId}`, { withCredentials: true });
    const h = () => onEvent();
    ['golf.tee_sheet', 'golf.starter_queue', 'golf.boards'].forEach((t) => es.addEventListener(t, h));
    return () => es.close();
  }, [path, propertyId]); // eslint-disable-line react-hooks/exhaustive-deps
}

function useCourse() {
  const courses = useCached<Page<R>>('/api/v1/golf/courses?filter[status]=active&limit=50', 'courses');
  const [courseId, setCourse] = useState('');
  const list = courses.data?.items ?? [];
  return { list, courseId: courseId || (list[0]?.id ?? ''), setCourse };
}

function CoursePicker({ c }: { c: ReturnType<typeof useCourse> }) {
  if (c.list.length < 2) return null;
  return <SelectField label="Course" value={c.courseId} onChange={c.setCourse} options={c.list.map((x) => ({ value: x.id, label: String(x.name) }))} />;
}

/** Big touch button (44px+, Technical Doc §6.5). */
export function Btn({ label, onClick, kind = 'neutral', disabled }: { label: string; onClick: () => void; kind?: 'neutral' | 'primary' | 'ink' | 'danger'; disabled?: boolean }) {
  return <button className={`oc-btn oc-btn-${kind}`} style={{ minHeight: 48, minWidth: 96 }} disabled={disabled} onClick={onClick}>{label}</button>;
}

// ── Home tiles ─────────────────────────────────────────────────────────────

export function OpsTiles() {
  const { can } = useAuth();
  const tiles: [string, string, string, string][] = [
    ['flag', 'Starter', '/ops/starter', 'golf.starter.view'], ['hiking', 'Caddy Master', '/ops/caddy', 'golf.caddy.view'],
    ['concierge', 'Front Desk', '/ops/front-desk', 'golf.check_in.perform'], ['golf_course', 'Golf Staff', '/ops/golf-staff', 'golf.bag.manage'],
    ['how_to_reg', 'Check-in', '/ops/check-in', 'golf.check_in.perform'],
  ];
  return (
    <div className="oc-grid">
      {tiles.filter((t) => can(t[3])).map(([icon, label, to]) => (
        <Link key={label} to={to} className="oc-card" style={{ minHeight: 120, textDecoration: 'none' }}>
          <div className="oc-card-head"><span className="oc-icon-circle"><Icon name={icon} size={22} /></span><h3>{label}</h3></div>
        </Link>
      ))}
    </div>
  );
}

// ── Starter (FR-CHK-07..10) ────────────────────────────────────────────────

export function StarterQueuePage({ view = 'queue' }: { view?: 'queue' | 'ready' | 'rounds' }) {
  const c = useCourse();
  const toast = useToast();
  const { can } = useAuth();
  const date = today();
  const path = c.courseId ? `/api/v1/golf/starter-queue${qs({ courseId: c.courseId, date })}` : null;
  const q = useCached<{ active: R[]; onHold: R[]; dispatched: R[]; courseStatus: R }>(path, `starter:${c.courseId}:${date}`);
  useStream(c.courseId ? `/api/v1/golf/tee-sheet/stream${qs({ courseId: c.courseId, date })}` : null, () => void q.refetch());
  const [holdFor, setHoldFor] = useState<R | null>(null);
  const [finishFor, setFinishFor] = useState<R | null>(null);
  const act = useSend<{ fid: string; action: string; reason?: string; holesPlayed?: number }>('POST', (v) => `/api/v1/golf/starter-queue/${v.fid}:${v.action}`, ['/api/v1/golf']);
  const run = (f: R, action: string, extra: Record<string, unknown> = {}) =>
    act.mutate({ fid: String(f.flightId), action, ...extra }, { onSuccess: () => { toast(`${action.replace('-', ' ')} done`); setHoldFor(null); setFinishFor(null); void q.refetch(); },
      onError: (e) => toast(e.message, 'error') });
  const ctl = can('golf.starter.control') && !q.offline;
  const players = (f: R) => ((f.players as R[]) ?? []).map((p) => String(p.name)).join(', ');
  const cs = q.data?.courseStatus;
  const stopped = cs && (cs.courseState === 'closed' || ['rain_stop', 'lightning_warning'].includes(String(cs.weather)));
  return (
    <div className="oc-stack">
      <Head title={view === 'rounds' ? 'Round Status' : view === 'ready' ? 'Ready Flights' : 'Starter Queue'} help={`${date} · ${cs ? `${String(cs.courseState)}, ${String(cs.weather).replace(/_/g, ' ')}` : ''}`} />
      <CoursePicker c={c} />
      {stopped && <div className="oc-alert oc-alert-warning" role="status">Tee-off suspended by course status.</div>}
      <ErrorAlert error={q.error} />
      {view !== 'rounds' && (
        <div className="oc-stack">
          {(q.data?.active ?? []).map((f, i) => (
            <div key={String(f.flightId)} className="oc-card">
              <div className="oc-row-wrap">
                <strong style={{ fontSize: 22 }}>#{i + 1} · {String(f.localTime)} · tee {String(f.startTee)}</strong>
                <span className="oc-muted">{String(f.bookingCode ?? '')} · waiting {String(f.waitMinutes)} min</span>
              </div>
              <div style={{ margin: '8px 0' }}>{players(f)}{(f.golfCarts as string[])?.length ? ` · carts ${(f.golfCarts as string[]).join(', ')}` : ''}</div>
              {ctl && (
                <div className="oc-row-wrap">
                  <Btn label="Call" onClick={() => run(f, 'call')} />
                  <Btn label="Skip" onClick={() => run(f, 'skip')} />
                  <Btn label="Hold" onClick={() => setHoldFor(f)} />
                  <Btn label="Tee-Off" kind="primary" disabled={!!stopped} onClick={() => run(f, 'tee-off')} />
                </div>
              )}
            </div>
          ))}
          {q.data && q.data.active.length === 0 && <p className="oc-muted">No flights waiting.</p>}
          {(q.data?.onHold ?? []).length > 0 && (
            <Card title="On Hold" icon="pause_circle">
              {(q.data?.onHold ?? []).map((f) => (
                <div key={String(f.flightId)} className="oc-row-wrap" style={{ marginBottom: 8 }}>
                  <strong>{String(f.localTime)}</strong><span>{players(f)}</span><span className="oc-muted">{String(f.holdReason ?? '')}</span>
                  {ctl && <Btn label="Release" kind="primary" onClick={() => run(f, 'release')} />}
                </div>
              ))}
            </Card>
          )}
        </div>
      )}
      {view === 'rounds' && (
        <DataTable rows={q.data?.dispatched} rowKey={(f) => String(f.flightId)}
          columns={[{ key: 'localTime', header: 'Tee Time' }, { key: 'players', header: 'Players', render: players },
            { key: 'dispatchedAt', header: 'Tee-Off', render: (f) => formatDateTime(String(f.dispatchedAt)) },
            { key: 'playTime', header: 'Play time', render: (f) => <PlayTime start={f.dispatchedAt as string} label={false} /> }]}
          actions={(f) => ctl && <Btn label="Round Finish" onClick={() => setFinishFor(f)} />} />
      )}
      <ReasonModal open={!!holdFor} title="Hold flight" label="Operational reason" onClose={() => setHoldFor(null)} onSubmit={(reason) => holdFor && run(holdFor, 'hold', { reason })} />
      <FinishModal flight={finishFor} onClose={() => setFinishFor(null)} onFinish={(holes, rain) => finishFor && (rain ? undefined : run(finishFor, 'finish', { holesPlayed: holes }))} />
    </div>
  );
}

function ReasonModal({ open, title, label, onClose, onSubmit }: { open: boolean; title: string; label: string; onClose: () => void; onSubmit: (v: string) => void }) {
  const [v, setV] = useState('');
  return (
    <Modal open={open} onClose={onClose} title={title} actions={<><Btn label="Cancel" onClick={onClose} /><Btn label="Save" kind="ink" disabled={!v.trim()} onClick={() => { onSubmit(v.trim()); setV(''); }} /></>}>
      <TextField label={label} value={v} onChange={setV} autoFocus />
    </Modal>
  );
}

function FinishModal({ flight, onClose, onFinish }: { flight: R | null; onClose: () => void; onFinish: (holes: number, rain: boolean) => void }) {
  const [holes, setHoles] = useState('18');
  const rc = useSend<Record<string, unknown>>('POST', '/api/v1/golf/rain-checks', ['/api/v1/golf']);
  return (
    <Modal open={!!flight} onClose={onClose} title="Round Finish" actions={<>
      <Btn label="Cancel" onClick={onClose} />
      <Btn label="Rain check" disabled={rc.isPending} onClick={() => flight && rc.mutate({ flightId: flight.flightId, holesPlayed: Number(holes) }, { onSuccess: onClose })} />
      <Btn label="Finish" kind="ink" onClick={() => onFinish(Number(holes), false)} /></>}>
      <TextField label="Holes played" type="number" inputMode="numeric" value={holes} onChange={setHoles} />
      <ErrorAlert error={rc.error} />
    </Modal>
  );
}

export function OpsTeeSheetPage() {
  const c = useCourse();
  const date = today();
  const sheet = useCached<{ slots: (R & { flights: R[] })[] }>(c.courseId ? `/api/v1/golf/tee-sheet${qs({ courseId: c.courseId, date })}` : null, `teesheet:${c.courseId}:${date}`);
  useStream(c.courseId ? `/api/v1/golf/tee-sheet/stream${qs({ courseId: c.courseId, date })}` : null, () => void sheet.refetch());
  const booked = (sheet.data?.slots ?? []).filter((s) => (s.flights ?? []).some((f) => f.bookingId));
  return (
    <div className="oc-stack">
      <Head title="Tee Sheet" help={`${date}${sheet.offline ? ' · offline copy' : ''}`} />
      <CoursePicker c={c} />
      <DataTable rows={booked} columns={[{ key: 'localTime', header: 'Tee Time' }, { key: 'startTee', header: 'Tee' },
        { key: 'flights', header: 'Players', render: (s) => (s.flights as R[]).map((f) => `${((f.players as R[]) ?? []).map((p) => `${String(p.name)}${p.status === 'checked_in' ? ' ✓' : ''}`).join(', ')} [${String(f.status).replace('_', ' ')}]`).join(' / ') }]} />
    </div>
  );
}

// ── Check-in (FR-CHK-01/02; offline write FR-OPS-06) ───────────────────────

export function OpsCheckInPage() {
  const { propertyId } = useAuth();
  const toast = useToast();
  const online = useOnline();
  const date = today();
  const bookings = useCached<Page<R>>(`/api/v1/golf/bookings${qs({ date, limit: 500 })}`, `bookings:${date}`);
  const [q, setQ] = useState('');
  const [scan, setScan] = useState('');
  const lookup = useGet<Page<R>>(online && scan ? `/api/v1/golf/check-ins:lookup${qs({ method: scan.startsWith('oneclub:booking:') ? 'booking_qr' : 'member_card', value: scan, date })}` : null);
  const list = (bookings.data?.items ?? []).filter((b) => ['confirmed', 'checked_in'].includes(String(b.status)) &&
    (!q || `${String(b.code)} ${String(b.contactName)}`.toLowerCase().includes(q.toLowerCase())));
  const checkIn = async (b: R) => {
    await enqueue('golf.check_in', { method: 'booking_code', value: b.code, bookingId: b.bookingId ?? b.id, date }, propertyId);
    toast(online ? `Check-in sent for ${String(b.code)}` : `Saved offline: ${String(b.code)} will sync when online`);
  };
  return (
    <div className="oc-stack">
      <Head title="Check-in" help={online ? 'Scan a member card or booking QR, or pick the booking.' : 'Offline: check-ins are queued and synced in order.'} />
      {online && (
        <div className="oc-row-wrap">
          <TextField label="Scan card / QR" value={scan} onChange={setScan} autoFocus />
        </div>
      )}
      <ErrorAlert error={lookup.error} />
      {(lookup.data?.items ?? []).map((cnd) => (
        <div key={String(cnd.bookingId)} className="oc-card oc-row-wrap">
          <strong>{String(cnd.code)} · {String(cnd.localTime)}</strong><span>{String(cnd.contactName)}</span>
          {Number(cnd.paymentDue) > 0 && <span className="oc-alert oc-alert-warning">Pay {money(cnd.paymentDue)} first</span>}
          <Btn label="Check-in" kind="primary" disabled={Number(cnd.paymentDue) > 0} onClick={() => void checkIn({ ...cnd, id: String(cnd.bookingId) })} />
        </div>
      ))}
      <TextField label="Search today's bookings" value={q} onChange={setQ} />
      <DataTable rows={list} loading={bookings.isLoading} columns={[{ key: 'localTime', header: 'Tee Time' }, { key: 'code', header: 'Booking' },
        { key: 'contactName', header: 'Booked by' }, { key: 'playerCount', header: 'Players', align: 'right' }, { key: 'status', header: 'Status', render: pill('status') }]}
        actions={(b) => b.status === 'confirmed' && <Btn label="Check-in" kind="primary" onClick={() => void checkIn(b)} />} />
    </div>
  );
}

// ── Check-out (FR-CHK-05): settle the day's charges, lockers, bags ─────────

export function OpsCheckOutPage() {
  const date = today();
  const toast = useToast();
  const desk = useGet<Page<R>>(`/api/v1/golf/check-outs${qs({ date })}`, { refetchInterval: 30000 });
  const [q, setQ] = useState('');
  const [open, setOpen] = useState<R | null>(null);
  const [done, setDone] = useState(false);
  const names = (v: unknown) => ((v as string[] | undefined) ?? []);
  const rows = (desk.data?.items ?? []).filter((b) => Boolean(b.checkedOutAt) === done &&
    (!q || `${String(b.code)} ${String(b.contactName)} ${names(b.players).join(' ')} ${names(b.bags).join(' ')}`.toLowerCase().includes(q.toLowerCase())));
  const list = (v: unknown) => names(v).join(', ') || '—';
  return (
    <div className="oc-stack">
      <Head title="Check-out" help="Settle what the day added (caddy tip, on-course F&B, golf cart, locker), then release the lockers and hand back the bags." />
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <TextField label="Search name, booking or bag tag" value={q} onChange={setQ} />
        <Btn label="To check out" kind={done ? 'neutral' : 'ink'} onClick={() => setDone(false)} />
        <Btn label="Checked out" kind={done ? 'ink' : 'neutral'} onClick={() => setDone(true)} />
      </div>
      <ErrorAlert error={desk.error} />
      <DataTable rows={rows} loading={desk.isLoading} columns={[{ key: 'localTime', header: 'Tee Time' }, { key: 'code', header: 'Booking' },
        { key: 'players', header: 'Players', render: (b) => list(b.players) },
        { key: 'inPlay', header: 'Round', render: (b) => <StatusPill status={Number(b.inPlay) > 0 ? 'in-play' : String(b.status).replace(/_/g, '-')} /> },
        { key: 'teeOffAt', header: 'Play time', render: (b) => <PlayTime start={b.teeOffAt as string} end={b.roundFinishAt as string} label={false} fallback="—" /> },
        { key: 'lockers', header: 'Lockers', render: (b) => list(b.lockers) }, { key: 'bags', header: 'Bags', render: (b) => list(b.bags) },
        { key: 'balance', header: 'Balance', align: 'right', render: (b) => <strong>{money(b.balance)}</strong> }]}
        actions={(b) => !b.checkedOutAt && <Btn label="Check-out" kind="primary" disabled={Number(b.inPlay) > 0} onClick={() => setOpen(b)} />} />
      {open && <CheckOutModal entry={open} onClose={() => setOpen(null)} onDone={(code) => { setOpen(null); toast(`${code} checked out`); void desk.refetch(); }} />}
    </div>
  );
}

const SETTLE = [['cash', 'Cash'], ['card', 'Card (EDC)'], ['qris', 'QRIS'], ['bank_transfer', 'Bank transfer']];

function CheckOutModal({ entry, onClose, onDone }: { entry: R; onClose: () => void; onDone: (code: string) => void }) {
  const b = useGet<R & { folio?: R; flights?: R[] }>(`/api/v1/golf/bookings/${String(entry.bookingId)}`);
  const due = Number(b.data?.folio?.balance ?? entry.balance ?? 0);
  const [method, setMethod] = useState(entry.memberAccount ? 'member_account' : 'cash');
  const [ref, setRef] = useState('');
  const [amount, setAmount] = useState('');
  const out = useSend<Record<string, unknown>>('POST', `/api/v1/golf/bookings/${String(entry.bookingId)}:check-out`, ['/api/v1/golf', '/api/v1/billing']);
  // a part payment first (split tenders); the last one settles and checks out
  const part = useSend<Record<string, unknown>>('POST', '/api/v1/billing/payments', ['/api/v1/billing', '/api/v1/golf']);
  const n = amount === '' ? due : Number(amount);
  const partial = due > 0 && n > 0 && n < due;
  const methods = [...(entry.memberAccount ? [['member_account', 'Member account']] : []), ...SETTLE];
  const count = (v: unknown) => ((v as string[] | undefined) ?? []).length;
  return (
    <Modal open onClose={onClose} title={`Check-out ${String(entry.code)}`} actions={<><Btn label="Cancel" onClick={onClose} />
      {partial ? (
        <Btn label={`Receive ${money(n)}`} kind="primary" disabled={part.isPending || !b.data?.folioId || method === 'member_account'}
          onClick={() => part.mutate({ folioId: b.data?.folioId, amount: String(n), methodType: method, channel: 'venue', reference: ref || undefined },
            { onSuccess: () => { setAmount(''); setRef(''); void b.refetch(); } })} />
      ) : (
        <Btn label={due > 0 ? `Settle ${money(due)} & check out` : 'Check out'} kind="primary" disabled={out.isPending || !b.data || n > due}
          onClick={() => out.mutate({ methodType: due > 0 ? method : undefined, reference: ref || undefined }, { onSuccess: () => onDone(String(entry.code)) })} />
      )}</>}>
      <div className="oc-stack">
        <p style={{ margin: 0 }}>{((entry.players as string[] | undefined) ?? []).join(', ')}</p>
        {(b.data?.flights ?? []).map((f) => <CaddyTips key={String(f.id)} flightId={String(f.id)} onTip={() => void b.refetch()} />)}
        <p style={{ margin: 0 }}>Charges {money(b.data?.folio?.charges)} · paid {money(b.data?.folio?.payments)} · balance <strong>{money(b.data?.folio?.balance)}</strong></p>
        {due > 0 && (
          <div className="oc-row-wrap">
            <TextField label="Amount (empty = all)" value={amount} onChange={(v) => setAmount(v.replace(/\D/g, ''))} inputMode="numeric" />
            <SelectField label="Pay by" value={method} onChange={setMethod} options={methods.map(([value, label]) => ({ value, label }))} />
            {method !== 'cash' && method !== 'member_account' && <TextField label="Reference" value={ref} onChange={setRef} />}
          </div>
        )}
        {partial && <p className="oc-small oc-muted" style={{ margin: 0 }}>Part payment: the rest ({money(due - n)}) stays on the bill for the next tender.</p>}
        <ErrorAlert error={part.error} />
        <p className="oc-small oc-muted" style={{ margin: 0 }}>
          Releases {count(entry.lockers)} locker(s) and hands back {count(entry.bags)} bag(s); the folio is closed.
        </p>
        <ErrorAlert error={out.error} />
      </div>
    </Modal>
  );
}

/** Caddy return: a non-cash tip goes on the booking folio (FR-CAD-08). */
function CaddyTips({ flightId, onTip }: { flightId: string; onTip: () => void }) {
  const list = useGet<Page<R>>(`/api/v1/golf/caddy-assignments${qs({ flightId })}`);
  const tip = useSend<Record<string, unknown>>('POST', '/api/v1/golf/caddy-tips', ['/api/v1/golf']);
  const [amount, setAmount] = useState<Record<string, string>>({});
  const caddies = (list.data?.items ?? []).filter((a) => a.status !== 'cancelled' && a.status !== 'replaced');
  if (caddies.length === 0) return null;
  return (
    <div className="oc-stack">
      {caddies.map((a) => (
        <div key={a.id} className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
          <span style={{ minWidth: 160 }}><strong>{String(a.caddyName)}</strong><br /><span className="oc-small oc-muted">tips {money(a.tips)}</span></span>
          <TextField label="Tip (non-cash)" value={amount[a.id] ?? ''} onChange={(v) => setAmount({ ...amount, [a.id]: v.replace(/\D/g, '') })} />
          <Btn label="Add tip" disabled={!amount[a.id] || tip.isPending}
            onClick={() => tip.mutate({ assignmentId: a.id, amount: amount[a.id], method: 'non_cash' }, { onSuccess: () => { setAmount({ ...amount, [a.id]: '' }); onTip(); } })} />
        </div>
      ))}
      <ErrorAlert error={tip.error} />
    </div>
  );
}

// ── Caddy Master (EP-09) ───────────────────────────────────────────────────

export function CaddyQueuePage() {
  const date = today();
  const { can } = useAuth();
  const board = useCached<Page<R>>(`/api/v1/golf/caddy-availability?date=${date}`, `caddies:${date}`);
  const att = useSend<Record<string, unknown>>('PUT', '/api/v1/golf/caddy-availability', ['/api/v1/golf/caddy']);
  const reorder = useSend<Record<string, unknown>>('POST', '/api/v1/golf/caddy-queue:reorder', ['/api/v1/golf/caddy']);
  const rows = board.data?.items ?? [];
  const present = rows.filter((c) => c.attendance === 'present');
  const manage = can('golf.caddy_assignment.manage') && !board.offline;
  const toBack = (c: R) => reorder.mutate({ date, caddyIds: [...present.filter((p) => p.caddyId !== c.caddyId).map((p) => p.caddyId), c.caddyId] });
  return (
    <div className="oc-stack">
      <Head title="Caddy Queue" help="Attendance and rotation of today." />
      <ErrorAlert error={att.error ?? reorder.error} />
      <div className="oc-grid">
        {rows.map((c) => (
          <div key={String(c.caddyId)} className="oc-card">
            <div className="oc-row"><strong style={{ fontSize: 20 }}>{String(c.code)}</strong><span className="oc-spacer" /><StatusPill status={String(c.status).replace(/_/g, '-')} /></div>
            <div>{String(c.name)}{c.teeTime ? ` · ${String(c.teeTime)}` : ''} · {String(c.roundsToday)} rounds</div>
            {manage && (
              <div className="oc-row-wrap" style={{ marginTop: 8 }}>
                {c.attendance !== 'present'
                  ? <Btn label="Present" kind="primary" onClick={() => att.mutate({ date, entries: [{ caddyId: c.caddyId, status: 'present' }] })} />
                  : c.status === 'available' && <><Btn label="To back" onClick={() => toBack(c)} /><Btn label="Absent" onClick={() => att.mutate({ date, entries: [{ caddyId: c.caddyId, status: 'absent' }] })} /></>}
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

/** A flight waiting for caddies or golf carts: tee time, booking and players, so the right flight is picked. */
function FlightCard({ f, note, children }: { f: R; note?: string; children: React.ReactNode }) {
  return (
    <div className="oc-card ops-flight">
      <span className="ops-flight-time">{String(f.localTime || '—')}</span>
      <div className="ops-flight-body">
        <strong>{String(f.bookingCode ?? `Flight ${String(f.flightNo)}`)}</strong>
        <span>{((f.players as R[]) ?? []).map((p) => String(p.name)).join(', ')}{note ? ` · ${note}` : ''}</span>
      </div>
      {children}
    </div>
  );
}

const byTeeTime = (a: R, b: R) => String(a.localTime ?? '').localeCompare(String(b.localTime ?? '')) || Number(a.flightNo) - Number(b.flightNo);

export function CaddyAssignmentPage({ history }: { history?: boolean }) {
  const [date, setDate] = useState(today());
  const { can } = useAuth();
  const list = useGet<Page<R>>(`/api/v1/golf/caddy-assignments?date=${date}`);
  const flights = useGet<Page<R>>(history ? null : `/api/v1/golf/flights?date=${date}`);
  const auto = useSend<Record<string, unknown>>('POST', '/api/v1/golf/caddy-assignments', ['/api/v1/golf']);
  const open = (flights.data?.items ?? []).filter((f) => !['completed', 'cancelled'].includes(String(f.status)) && !(f.readiness as R)?.caddiesOk).sort(byTeeTime);
  return (
    <div className="oc-stack">
      <Head title={history ? 'Caddy History' : 'Caddy Assignment'} />
      {history && <TextField label="Date" type="date" value={date} onChange={setDate} />}
      <ErrorAlert error={auto.error} />
      {!history && can('golf.caddy_assignment.manage') && open.map((f) => (
        <FlightCard key={f.id} f={f} note={`${String(f.caddiesNeeded)} caddies needed`}>
          <Btn label="Assign from queue" kind="primary" disabled={auto.isPending} onClick={() => auto.mutate({ flightId: f.id, auto: true })} />
        </FlightCard>
      ))}
      <DataTable rows={list.data?.items} loading={list.isLoading} columns={[{ key: 'teeTime', header: 'Tee Time' }, { key: 'caddyCode', header: 'Caddy' },
        { key: 'caddyName', header: 'Name' }, { key: 'playerNames', header: 'Players', render: (a) => ((a.playerNames as string[]) ?? []).join(', ') }, { key: 'status', header: 'Status', render: pill('status') }]} />
    </div>
  );
}

// ── Front Desk ─────────────────────────────────────────────────────────────

export function GuestPage() {
  const toast = useToast();
  const [v, setV] = useState({ name: '', phone: '' });
  const send = useSend<Record<string, unknown>>('POST', '/api/v1/crm/guests', ['/api/v1/crm']);
  return (
    <div className="oc-stack">
      <Head title="Guest" help="Minimal identity for a walk-in or member's guest." />
      <div className="oc-row-wrap">
        <TextField label="Name" value={v.name} onChange={(x) => setV({ ...v, name: x })} />
        <TextField label="Phone" value={v.phone} onChange={(x) => setV({ ...v, phone: x })} />
        <Btn label="Add Guest" kind="ink" disabled={!v.name || send.isPending}
          onClick={() => send.mutate({ code: `G-${Date.now().toString(36).toUpperCase()}`, name: v.name, phone: v.phone || undefined }, { onSuccess: () => { toast('Guest added'); setV({ name: '', phone: '' }); } })} />
      </div>
      <ErrorAlert error={send.error} />
    </div>
  );
}

export function FrontDeskPaymentsPage() {
  const list = useGet<Page<R>>(`/api/v1/billing/payments${qs({ limit: 100 })}`);
  return (
    <div className="oc-stack">
      <Head title="Payments" />
      <DataTable rows={list.data?.items} loading={list.isLoading} columns={[{ key: 'number', header: 'Payment' }, { key: 'methodType', header: 'Method' },
        { key: 'amount', header: 'Amount', align: 'right', render: (p) => money(p.amount) }, { key: 'paidAt', header: 'Paid', render: (p) => (p.paidAt ? formatDateTime(String(p.paidAt)) : '—') },
        { key: 'status', header: 'Status', render: pill('status') }]} />
    </div>
  );
}

export function FrontDeskFoliosPage() {
  const list = useGet<Page<R>>('/api/v1/billing/folios?filter[status]=open&limit=100');
  return (
    <div className="oc-stack">
      <Head title="Folios" help="Open folios." />
      <DataTable rows={list.data?.items} loading={list.isLoading} columns={[{ key: 'number', header: 'Folio' }, { key: 'holderName', header: 'Holder' },
        { key: 'charges', header: 'Charges', align: 'right', render: (f) => money(f.charges) }, { key: 'balance', header: 'Balance', align: 'right', render: (f) => money(f.balance) }]} />
    </div>
  );
}

// ── Golf Staff (bags, lockers, golf carts) ─────────────────────────────────

export function BagDropPage() {
  const { propertyId } = useAuth();
  const toast = useToast();
  const date = today();
  const players = useCached<Page<R>>(`/api/v1/golf/players?date=${date}&limit=500`, `players:${date}`);
  const drops = useGet<Page<R>>(`/api/v1/golf/bag-drops?date=${date}`);
  const collect = useSend<{ id: string }>('POST', (v) => `/api/v1/golf/bag-drops/${v.id}:collect`, ['/api/v1/golf']);
  const [player, setPlayer] = useState('');
  const [tag, setTag] = useState('');
  const drop = async () => {
    await enqueue('golf.bag_drop', { bookingPlayerId: player, tagNumber: tag, bagCount: 1 }, propertyId);
    toast(`Bag ${tag} recorded`);
    setTag('');
    setPlayer('');
  };
  return (
    <div className="oc-stack">
      <Head title="Bag Drop" help="Works offline: bag drops are queued and synced." />
      <div className="oc-row-wrap">
        <SelectField label="Player" value={player} onChange={setPlayer}
          options={(players.data?.items ?? []).map((p) => ({ value: p.id, label: `${String(p.localTime)} · ${String(p.name)} (${String(p.bookingCode)})` }))} />
        <TextField label="Bag tag" value={tag} onChange={setTag} />
        <Btn label="Drop bag" kind="primary" disabled={!player || !tag} onClick={() => void drop()} />
      </div>
      <DataTable rows={drops.data?.items} loading={drops.isLoading} columns={[{ key: 'tagNumber', header: 'Tag' }, { key: 'playerName', header: 'Player' },
        { key: 'bookingCode', header: 'Booking' }, { key: 'status', header: 'Status', render: pill('status') }]}
        actions={(d) => d.status !== 'collected' && <Btn label="Collected" onClick={() => collect.mutate({ id: d.id })} />} />
    </div>
  );
}

export function BagStoragePage() {
  const list = useGet<Page<R>>('/api/v1/golf/bag-storage?filter[status]=active');
  const end = useSend<{ id: string }>('POST', (v) => `/api/v1/golf/bag-storage/${v.id}:end`, ['/api/v1/golf']);
  const [v, setV] = useState({ customerId: '', rackNumber: '', fee: '' });
  const customers = useGet<Page<R>>('/api/v1/crm/customers?filter[status]=active&limit=500');
  const store = useSend<Record<string, unknown>>('POST', '/api/v1/golf/bag-storage', ['/api/v1/golf']);
  return (
    <div className="oc-stack">
      <Head title="Bag Storage" />
      <div className="oc-row-wrap">
        <SelectField label="Customer" value={v.customerId} onChange={(x) => setV({ ...v, customerId: x })} options={(customers.data?.items ?? []).map((c) => ({ value: c.id, label: String(c.name) }))} />
        <TextField label="Rack" value={v.rackNumber} onChange={(x) => setV({ ...v, rackNumber: x })} />
        <TextField label="Fee" value={v.fee} onChange={(x) => setV({ ...v, fee: x })} />
        <Btn label="Store" kind="ink" disabled={!v.customerId || !v.rackNumber} onClick={() => store.mutate({ ...v, fee: v.fee || undefined })} />
      </div>
      <ErrorAlert error={store.error ?? end.error} />
      <DataTable rows={list.data?.items} loading={list.isLoading} columns={[{ key: 'rackNumber', header: 'Rack' }, { key: 'holderName', header: 'Customer' },
        { key: 'startsOn', header: 'From' }, { key: 'feeAmount', header: 'Fee', align: 'right', render: (s) => money(s.feeAmount) }]}
        actions={(s) => <Btn label="End" onClick={() => end.mutate({ id: s.id })} />} />
    </div>
  );
}

export function LockersPage() {
  const date = today();
  const lockers = useGet<Page<R>>('/api/v1/golf/lockers?filter[status]=active&limit=500');
  const assigned = useGet<Page<R>>('/api/v1/golf/locker-assignments?filter[status]=active');
  const players = useGet<Page<R>>(`/api/v1/golf/players?date=${date}&limit=500`);
  const assign = useSend<Record<string, unknown>>('POST', '/api/v1/golf/locker-assignments', ['/api/v1/golf']);
  const release = useSend<{ id: string }>('POST', (v) => `/api/v1/golf/locker-assignments/${v.id}:release`, ['/api/v1/golf']);
  const [v, setV] = useState({ lockerId: '', bookingPlayerId: '', fee: '' });
  return (
    <div className="oc-stack">
      <Head title="Locker Assignment" help="Daily lockers stay in use until the player checks out; a fee goes on the booking bill." />
      <div className="oc-row-wrap">
        <SelectField label="Locker" value={v.lockerId} onChange={(x) => setV({ ...v, lockerId: x })}
          options={(lockers.data?.items ?? []).filter((l) => l.lockerStatus === 'available').map((l) => ({ value: l.id, label: `${String(l.code)} (${String(l.area)})` }))} />
        <SelectField label="Player" value={v.bookingPlayerId} onChange={(x) => setV({ ...v, bookingPlayerId: x })}
          options={(players.data?.items ?? []).map((p) => ({ value: p.id, label: `${String(p.name)} (${String(p.bookingCode)})` }))} />
        <TextField label="Fee (optional)" value={v.fee} onChange={(x) => setV({ ...v, fee: x.replace(/\D/g, '') })} />
        <Btn label="Assign" kind="ink" disabled={!v.lockerId || !v.bookingPlayerId}
          onClick={() => assign.mutate({ ...v, fee: v.fee || undefined, assignmentType: 'daily' }, { onSuccess: () => setV({ lockerId: '', bookingPlayerId: '', fee: '' }) })} />
      </div>
      <ErrorAlert error={assign.error ?? release.error} />
      <DataTable rows={assigned.data?.items} loading={assigned.isLoading} columns={[{ key: 'lockerCode', header: 'Locker' }, { key: 'holderName', header: 'Holder' },
        { key: 'assignmentType', header: 'Type' }]} actions={(a) => <Btn label="Release" onClick={() => release.mutate({ id: a.id })} />} />
    </div>
  );
}

export function CartReadinessPage() {
  const board = useGet<Page<R>>(`/api/v1/golf/golf-cart-board?date=${today()}`);
  const set = useSend<{ id: string; readiness: string }>('POST', (v) => `/api/v1/golf/golf-carts/${v.id}:set-readiness`, ['/api/v1/golf']);
  return (
    <div className="oc-stack">
      <Head title="Golf Cart Readiness" />
      <ErrorAlert error={set.error} />
      <div className="oc-grid">
        {(board.data?.items ?? []).map((c) => (
          <div key={c.id} className="oc-card">
            <div className="oc-row"><strong style={{ fontSize: 20 }}>{String(c.code)}</strong><span className="oc-spacer" /><StatusPill status={String(c.readiness).replace(/_/g, '-')} /></div>
            {c.readiness !== 'in_use' && (
              <div className="oc-row-wrap" style={{ marginTop: 8 }}>
                {['ready', 'charging', 'maintenance'].filter((r) => r !== c.readiness).map((r) => (
                  <Btn key={r} label={r.charAt(0).toUpperCase() + r.slice(1)} kind={r === 'ready' ? 'primary' : 'neutral'} onClick={() => set.mutate({ id: c.id, readiness: r })} />
                ))}
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

export function CartAssignmentPage() {
  const date = today();
  const flights = useGet<Page<R>>(`/api/v1/golf/flights?date=${date}`);
  const list = useGet<Page<R>>(`/api/v1/golf/golf-cart-assignments?date=${date}`);
  const auto = useSend<Record<string, unknown>>('POST', '/api/v1/golf/golf-cart-assignments', ['/api/v1/golf']);
  const ret = useSend<{ id: string }>('POST', (v) => `/api/v1/golf/golf-cart-assignments/${v.id}:return`, ['/api/v1/golf']);
  const open = (flights.data?.items ?? []).filter((f) => !['completed', 'cancelled'].includes(String(f.status)) && !(f.readiness as R)?.golfCartsOk).sort(byTeeTime);
  return (
    <div className="oc-stack">
      <Head title="Golf Cart Assignment" />
      <ErrorAlert error={auto.error ?? ret.error} />
      {open.map((f) => (
        <FlightCard key={f.id} f={f} note={`needs ${String(f.golfCartsNeeded)} golf cart${Number(f.golfCartsNeeded) === 1 ? '' : 's'}`}>
          <Btn label="Assign Ready carts" kind="primary" disabled={auto.isPending} onClick={() => auto.mutate({ flightId: f.id, auto: true })} />
        </FlightCard>
      ))}
      <DataTable rows={list.data?.items} loading={list.isLoading} columns={[{ key: 'golfCartCode', header: 'Golf Cart' }, { key: 'bookingCode', header: 'Booking' },
        { key: 'status', header: 'Status', render: pill('status') }]}
        actions={(a) => ['assigned', 'in_use'].includes(String(a.status)) && <Btn label="Return" onClick={() => ret.mutate({ id: a.id })} />} />
    </div>
  );
}
