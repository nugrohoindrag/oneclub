import React, { useEffect, useMemo, useState } from 'react';
import { API_BASE, qs, request, uuidv7, useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDateTime, formatNumber } from '@oneclub/i18n';
import { enqueue } from '@oneclub/offline';
import { Link } from 'react-router';
import { OUTLET_KEY, read } from '../offline';
import {
  Card, Checkbox, DataTable, Empty, ErrorAlert, Icon, QRCode, SelectField, StatusPill, TextField, useAuth, useToast,
} from '@oneclub/shell';

type Row = Record<string, unknown>;
const money = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : `Rp ${formatNumber(Number(v))}`);
const idem = () => ({ 'Idempotency-Key': uuidv7() });

/** Re-renders when the server pushes one of the topics on an SSE stream
 * (Technical Doc §3.4); the hub names each SSE event after its topic. */
export function useLive(path: string, topics: string[], onEvent: () => void) {
  const { propertyId } = useAuth();
  const key = topics.join(',');
  useEffect(() => {
    if (!propertyId || typeof EventSource === 'undefined') return;
    const es = new EventSource(`${API_BASE}${path}?propertyId=${propertyId}`, { withCredentials: true });
    const h = () => onEvent();
    for (const t of key.split(',')) es.addEventListener(t, h);
    return () => es.close();
  }, [path, key, propertyId, onEvent]);
}

/** P1's golf stream carries every golf.* topic, P2's included. */
const GOLF_STREAM = '/api/v1/golf/tee-sheet/stream';

function Head({ title, help }: { title: string; help?: string }) {
  return <div className="oc-page-head"><div><h1>{title}</h1>{help && <p>{help}</p>}</div></div>;
}

// ── Starter: Pace of Play (FR-PLX-04) ─────────────────────────────────────

export function PaceOfPlayPage() {
  const pace = useGet<Page<Schemas['PaceFlight']>>('/api/v1/golf/pace-of-play', { refetchInterval: 60_000 });
  useLive(GOLF_STREAM, ['golf.pace'], useMemo(() => () => void pace.refetch(), [pace]));
  return (
    <div className="oc-stack">
      <Head title="Pace of Play" help="Updates live from the caddy tablets." />
      <div className="oc-card">
        <DataTable rows={pace.data?.items as unknown as Row[]} loading={pace.isLoading} rowKey={(r) => String(r.flightId)} columns={[
          { key: 'label', header: 'Flight' }, { key: 'playingRouteName', header: 'Route' }, { key: 'hole', header: 'Hole' },
          { key: 'elapsedMinutes', header: 'Elapsed' }, { key: 'targetMinutes', header: 'Target' },
          { key: 'behindMinutes', header: 'Behind', render: (r) => <strong style={{ color: r.slow ? 'var(--md-sys-color-error)' : undefined }}>{String(r.behindMinutes)}</strong> },
          { key: 'aheadLabel', header: 'Flight ahead', render: (r) => r.aheadLabel ? `${String(r.aheadLabel)} (+${String(r.gapHoles)} holes)` : '—' },
          { key: 'slow', header: 'Status', render: (r) => <StatusPill status={r.slow ? 'slow' : 'on_pace'} label={r.slow ? 'Slow' : 'On pace'} /> }]} />
      </div>
    </div>
  );
}

// ── Caddy Master: clock-in / clock-out and incidents (FR-CDL-03, FR-CDL-08) ─

export function CaddyIncidentsPage() {
  const toast = useToast();
  const today = new Date().toISOString().slice(0, 10);
  const att = useGet<Page<Schemas['Attendance']>>(`/api/v1/golf/caddy-attendance${qs({ date: today })}`);
  const caddies = useGet<Page<Row>>('/api/v1/golf/caddies?filter[status]=active&limit=500');
  const incidents = useGet<Page<Schemas['Incident']>>('/api/v1/golf/caddy-incidents?filter[status]=open');
  const clockIn = useSend<Row>('POST', '/api/v1/golf/caddy-attendance:clock-in', ['/api/v1/golf/caddy-attendance']);
  const clockOut = useSend<Row>('POST', '/api/v1/golf/caddy-attendance:clock-out', ['/api/v1/golf/caddy-attendance']);
  const report = useSend<Row>('POST', '/api/v1/golf/caddy-incidents', ['/api/v1/golf/caddy-incidents']);
  const close = useSend<Row>('POST', (b) => `/api/v1/golf/caddy-incidents/${b.id}:close`, ['/api/v1/golf/caddy-incidents']);
  const [caddy, setCaddy] = useState('');
  const [category, setCategory] = useState('late');
  const [severity, setSeverity] = useState('low');
  const [description, setDescription] = useState('');
  const present = new Set((att.data?.items ?? []).filter((a) => a.clockedInAt && !a.clockedOutAt).map((a) => a.caddyId));
  const caddyOptions = (caddies.data?.items ?? []).map((c) => ({ value: String(c.id), label: `${String(c.code)} · ${String(c.name)}` }));
  return (
    <div className="oc-stack">
      <Head title="Incidents & Attendance" help="Clock-in puts the caddy in the rotation; clock-out takes the caddy out." />
      <ErrorAlert error={clockIn.error ?? clockOut.error ?? report.error ?? close.error} />
      <Card title="Caddy Attendance" icon="how_to_reg">
        <div className="oc-row-wrap">
          <div style={{ width: 280 }}><SelectField label="Caddy" value={caddy} onChange={setCaddy} placeholder="Select caddy" options={caddyOptions} /></div>
          <button className="oc-btn oc-btn-ink" style={{ alignSelf: 'flex-end' }} disabled={!caddy || present.has(caddy)}
            onClick={() => clockIn.mutate({ caddyId: caddy }, { onSuccess: () => toast('Clocked in') })}>Clock in</button>
          <button className="oc-btn oc-btn-outline" style={{ alignSelf: 'flex-end' }} disabled={!caddy || !present.has(caddy)}
            onClick={() => clockOut.mutate({ caddyId: caddy }, { onSuccess: () => toast('Clocked out') })}>Clock out</button>
        </div>
      </Card>
      <Card title="Report an incident" icon="report">
        <div className="oc-row-wrap">
          <div style={{ width: 160 }}><SelectField label="Category" value={category} onChange={setCategory}
            options={['late', 'misconduct', 'lost_item', 'accident', 'other'].map((c) => ({ value: c, label: c.replace('_', ' ') }))} /></div>
          <div style={{ width: 140 }}><SelectField label="Severity" value={severity} onChange={setSeverity}
            options={['low', 'medium', 'high', 'critical'].map((c) => ({ value: c, label: c }))} /></div>
          <div style={{ flex: 1, minWidth: 240 }}><TextField label="Description" value={description} onChange={setDescription} /></div>
          <button className="oc-btn oc-btn-ink" style={{ alignSelf: 'flex-end' }} disabled={!caddy || !description}
            onClick={() => report.mutate({ subjectType: 'caddy', caddyId: caddy, category, severity, description }, { onSuccess: () => { setDescription(''); toast('Incident recorded'); } })}>Report</button>
        </div>
      </Card>
      <Card title="Open incidents" icon="list_alt">
        <DataTable rows={incidents.data?.items as unknown as Row[]} loading={incidents.isLoading} columns={[{ key: 'number', header: 'Incident' },
          { key: 'category', header: 'Category' }, { key: 'severity', header: 'Severity' }, { key: 'description', header: 'Description' },
          { key: 'occurredAt', header: 'When', render: (r) => formatDateTime(String(r.occurredAt)) }]}
          actions={(r) => <button className="oc-btn oc-btn-outline oc-btn-sm" onClick={() => {
            const action = window.prompt('Action taken');
            if (action) close.mutate({ id: r.id, reason: action }, { onSuccess: () => toast('Closed') });
          }}>Close</button>} />
      </Card>
    </div>
  );
}

// ── Golf Staff: readiness board & inspection (FR-CTL-01/07) ───────────────

export function GolfCartInspectionPage() {
  const toast = useToast();
  const board = useGet<Schemas['Board']>('/api/v1/golf/golf-cart-readiness');
  useLive(GOLF_STREAM, ['golf.cart'], useMemo(() => () => void board.refetch(), [board]));
  const checklists = useGet<Page<Row>>('/api/v1/golf/golf-cart-checklists?filter[status]=active');
  const [cart, setCart] = useState<Row | null>(null);
  const [kind, setKind] = useState('pre_op');
  const [results, setResults] = useState<Record<string, boolean>>({});
  const [battery, setBattery] = useState('');
  const [notes, setNotes] = useState('');
  const items = useMemo(() => {
    const c = (checklists.data?.items ?? []).find((x) => x.inspectionKind === kind && (!cart || x.cartType === (cart as Row).cartType || true));
    return (c?.items as string[] | undefined) ?? ['Brakes', 'Battery', 'Tyres', 'Body', 'Lights'];
  }, [checklists.data, kind, cart]);
  const inspect = useSend<Row, Schemas['Inspection']>('POST', '/api/v1/golf/golf-cart-inspections', ['/api/v1/golf/golf-cart-readiness']);
  // A returned cart (Not Ready / Charging) is checked post-op first; the server enforces the order.
  const suggestKind = (r: Row) => (r.readiness === 'maintenance' ? 'release' : r.readiness === 'ready' ? 'pre_op' : 'post_op');
  return (
    <div className="oc-stack">
      <Head title="Golf Cart Inspection" help="A golf cart is Ready only after a passed inspection." />
      <div className="oc-row-wrap">{Object.entries(board.data?.counts ?? {}).map(([k, v]) => <span key={k} className="oc-chip"><StatusPill status={k} /> {v}</span>)}</div>
      <ErrorAlert error={inspect.error} />
      <div className="oc-grid">
        {board.data?.golfCarts.map((c) => (
          <button key={c.id} className="oc-card" style={{ textAlign: 'left', cursor: 'pointer', outline: cart?.id === c.id ? '2px solid var(--md-sys-color-primary)' : undefined }}
            onClick={() => { setCart(c as unknown as Row); setKind(suggestKind(c as unknown as Row)); setResults({}); }}>
            <div className="oc-row"><strong>{c.code}</strong><span className="oc-spacer" /><StatusPill status={c.readiness} /></div>
            <div className="oc-small">Battery {c.batteryPercent ?? '—'}% · {c.hoursSinceService} h since service{c.serviceDue ? ' · service due' : ''}</div>
            {c.bookingCode && <div className="oc-small">Booking {c.bookingCode}</div>}
          </button>
        ))}
      </div>
      {cart && (
        <Card title={`Golf Cart Inspection · ${String(cart.code)}`} icon="fact_check">
          <div className="oc-row-wrap">
            {['pre_op', 'post_op', 'release'].map((k) => <button key={k} className="oc-chip" aria-pressed={kind === k} onClick={() => setKind(k)}>{k.replace('_', '-')}</button>)}
          </div>
          <div className="oc-stack" style={{ marginTop: 12 }}>
            {items.map((it) => <Checkbox key={it} label={`${it} OK`} checked={results[it] ?? true} onChange={(v) => setResults({ ...results, [it]: v })} />)}
            <div className="oc-row-wrap">
              <div style={{ width: 160 }}><TextField label="Battery %" type="number" value={battery} onChange={setBattery} /></div>
              <div style={{ flex: 1, minWidth: 220 }}><TextField label="Notes" value={notes} onChange={setNotes} /></div>
            </div>
            <button className="oc-btn oc-btn-ink" onClick={() => inspect.mutate({
              golfCartId: cart.id, kind, notes, batteryPercent: battery ? Number(battery) : undefined,
              results: items.map((it) => ({ item: it, pass: results[it] ?? true })),
            }, { onSuccess: (r) => { toast(`Inspection saved — ${r.readinessAfter.replace('_', ' ')}`); setCart(null); } })}>Save inspection</button>
          </div>
        </Card>
      )}
    </div>
  );
}

// ── Driving Range (FR-RNG-02/04) ──────────────────────────────────────────

export function DrivingRangePage() {
  const toast = useToast();
  const sessions = useGet<Page<Schemas['RangeSession']>>('/api/v1/golf/range-sessions', { refetchInterval: 20_000 });
  useLive(GOLF_STREAM, ['golf.range'], useMemo(() => () => void sessions.refetch(), [sessions]));
  const start = useSend<Row>('POST', '/api/v1/golf/range-sessions', ['/api/v1/golf/range-sessions']);
  const end = useSend<Row>('POST', (b) => `/api/v1/golf/range-sessions/${b.id}:end`, ['/api/v1/golf/range-sessions']);
  const bucket = useSend<Row, Schemas['Bucket']>('POST', '/api/v1/golf/range-buckets', ['/api/v1/golf/range-sessions'], idem);
  const [guest, setGuest] = useState('');
  const [area, setArea] = useState('outdoor');
  const [last, setLast] = useState<Schemas['Bucket'] | null>(null);
  return (
    <div className="oc-stack">
      <Head title="Driving Range" help="Ball sales go through the Driving Range Counter POS; prepaid balls are redeemed here." />
      <ErrorAlert error={start.error ?? end.error ?? bucket.error} />
      <Card title="Walk-in" icon="sports_golf">
        <div className="oc-row-wrap">
          <div style={{ width: 240 }}><TextField label="Guest name" value={guest} onChange={setGuest} /></div>
          <div style={{ width: 160 }}><SelectField label="Area" value={area} onChange={setArea} options={[{ value: 'outdoor', label: 'Outdoor' }, { value: 'indoor', label: 'Indoor' }]} /></div>
          <button className="oc-btn oc-btn-ink" style={{ alignSelf: 'flex-end' }} disabled={!guest}
            onClick={() => start.mutate({ guestName: guest, area }, { onSuccess: () => { setGuest(''); toast('Checked in'); } })}>Assign bay / queue</button>
        </div>
      </Card>
      {last && <div className="oc-alert oc-alert-info">Dispenser code <strong className="oc-code">{last.dispenserCode}</strong> · {last.balls} balls{last.dispenseMode === 'bridge' ? ' (sent to dispenser)' : ''}{last.remainingBalance ? ` · balance ${last.remainingBalance}` : ''}</div>}
      <div className="oc-card">
        <DataTable rows={sessions.data?.items as unknown as Row[]} columns={[{ key: 'number', header: 'Session' }, { key: 'bayCode', header: 'Bay' },
          { key: 'customerName', header: 'Customer', render: (r) => String(r.customerName ?? r.guestName ?? '—') }, { key: 'queuePosition', header: 'Queue' },
          { key: 'balls', header: 'Balls' }, { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]}
          actions={(r) => (
            <div className="oc-row">
              {r.customerId ? <button className="oc-btn oc-btn-outline oc-btn-sm" onClick={() => bucket.mutate({ sessionId: r.id, balls: 50, source: 'prepaid' }, { onSuccess: (b) => setLast(b) })}>Prepaid 50</button> : null}
              {r.status === 'active' && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => end.mutate({ id: r.id })}>End</button>}
            </div>
          )} />
      </div>
    </div>
  );
}

// ── Sport Reception: access, entries, occupancy (FR-SPT-04/05/08) ─────────

const SCAN_KEY = 'oneclub.ops.facility';

export function SportReceptionPage() {
  const toast = useToast();
  const { propertyId } = useAuth();
  const facilities = useGet<Page<Row>>('/api/v1/sportclub/facilities?filter[status]=active');
  const occupancy = useGet<Page<Schemas['Occupancy']>>('/api/v1/sportclub/occupancy', { refetchInterval: 30_000 });
  const [facility, setFacility] = useState(() => localStorage.getItem(SCAN_KEY) ?? '');
  const [code, setCode] = useState('');
  const [result, setResult] = useState<Schemas['AccessResult'] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const validate = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    try {
      if (!navigator.onLine) {
        await enqueue('sportclub.access_validate', { code, facilityId: facility, direction: 'in', terminal: 'reception' }, propertyId);
        toast('Offline: access recorded in the sync queue');
      } else {
        setResult(await request<Schemas['AccessResult']>('POST', '/api/v1/sportclub/access:validate', { code, facilityId: facility, direction: 'in', terminal: 'reception' }));
      }
      setCode('');
      void occupancy.refetch();
    } catch (err) {
      setError(err);
    }
  };
  return (
    <div className="oc-stack">
      <Head title="Sport Reception" help="Scan a member card, entry ticket, booking or stay QR." />
      <Card title="Facility Access" icon="qr_code_scanner">
        <form className="oc-row-wrap" onSubmit={validate}>
          <div style={{ width: 260 }}><SelectField label="Facility" value={facility} onChange={(v) => { setFacility(v); localStorage.setItem(SCAN_KEY, v); }}
            options={(facilities.data?.items ?? []).map((f) => ({ value: String(f.id), label: String(f.name) }))} placeholder="Select facility" /></div>
          <div style={{ flex: 1, minWidth: 240 }}><TextField label="Scan / code" value={code} onChange={setCode} autoFocus /></div>
          <button className="oc-btn oc-btn-ink" style={{ alignSelf: 'flex-end' }} disabled={!facility || !code}>Validate</button>
        </form>
        <ErrorAlert error={error} />
        {result && (
          <div className={`oc-alert ${result.result === 'granted' ? 'oc-alert-success' : 'oc-alert-error'}`} style={{ marginTop: 12 }}>
            <strong>{result.result === 'granted' ? 'Access granted' : 'Access denied'}</strong> · {result.credentialType}{result.customerName ? ` · ${result.customerName}` : ''}
            {result.reason ? ` — ${result.reason}` : ''}
          </div>
        )}
      </Card>
      <Card title="Facility Occupancy" icon="groups">
        <DataTable rows={occupancy.data?.items as unknown as Row[]} rowKey={(r) => String(r.facilityId)} columns={[{ key: 'facilityName', header: 'Facility' },
          { key: 'inside', header: 'Inside' }, { key: 'capacity', header: 'Capacity' }, { key: 'entriesToday', header: 'Entries today' }, { key: 'booked', header: 'Booked' }]} />
      </Card>
    </div>
  );
}

// ── Instructor: My Classes & attendance (FR-CLS-07) ───────────────────────

export function InstructorPage() {
  const toast = useToast();
  const classes = useGet<Page<Schemas['MyClass']>>('/api/v1/sportclub/my-classes');
  const mark = useSend<Row>('POST', '/api/v1/sportclub/attendance', ['/api/v1/sportclub/my-classes']);
  return (
    <div className="oc-stack">
      <Head title="Instructor" help="My Classes and attendance" />
      <ErrorAlert error={mark.error} />
      {classes.data?.items.length === 0 && <Empty title="No classes today" icon="school" />}
      {classes.data?.items.map((c) => (
        <Card key={c.session.id} title={`${c.session.programName} · ${formatDateTime(c.session.start)}`} icon="school">
          <DataTable rows={c.roster as unknown as Row[]} columns={[{ key: 'customerName', header: 'Participant' }, { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]}
            actions={(r) => (
              <div className="oc-row">
                {(['present', 'absent', 'excused'] as const).map((s) => (
                  <button key={s} className={`oc-btn oc-btn-sm ${r.status === s ? 'oc-btn-ink' : 'oc-btn-outline'}`}
                    onClick={() => mark.mutate({ sessionBookingId: r.id, status: s }, { onSuccess: () => toast('Saved') })}>{s}</button>
                ))}
              </div>
            )} />
        </Card>
      ))}
    </div>
  );
}

// ── Stay Front Desk: stays & meetings (EP-16..18) ──────────────────────────────

export function StayDeskPage() {
  const toast = useToast();
  const today = new Date();
  const from = new Date(today.getFullYear(), today.getMonth(), today.getDate() - 1).toISOString();
  const to = new Date(today.getFullYear(), today.getMonth(), today.getDate() + 2).toISOString();
  const stays = useGet<Page<Schemas['Stay']>>(`/api/v1/stay/stays${qs({ from, to })}`);
  const checkIn = useSend<Row>('POST', (b) => `/api/v1/stay/stays/${b.id}:check-in`, ['/api/v1/stay/stays']);
  const checkOut = useSend<Row>('POST', (b) => `/api/v1/stay/stays/${b.id}:check-out`, ['/api/v1/stay/stays']);
  const confirm = useSend<Row>('POST', (b) => `/api/v1/stay/stays/${b.id}:confirm`, ['/api/v1/stay/stays']);
  return (
    <div className="oc-stack">
      <Head title="Stay Front Desk" help="Arrivals, in-house and departures (bungalow, VIP suite, meeting room)." />
      <ErrorAlert error={checkIn.error ?? checkOut.error ?? confirm.error} />
      <div className="oc-card">
        <DataTable rows={stays.data?.items as unknown as Row[]} loading={stays.isLoading} columns={[{ key: 'stayNo', header: 'Booking' }, { key: 'kind', header: 'Kind' },
          { key: 'unitName', header: 'Unit' }, { key: 'customerName', header: 'Guest', render: (r) => String(r.customerName ?? r.guestName ?? r.corporateName ?? '—') },
          { key: 'start', header: 'From', render: (r) => formatDateTime(String(r.start)) }, { key: 'end', header: 'To', render: (r) => formatDateTime(String(r.end)) },
          { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]}
          actions={(r) => (
            <div className="oc-row">
              {r.status === 'requested' && <button className="oc-btn oc-btn-outline oc-btn-sm" onClick={() => confirm.mutate({ id: r.id }, { onSuccess: () => toast('Confirmed') })}>Confirm</button>}
              {r.status === 'reserved' && <button className="oc-btn oc-btn-ink oc-btn-sm" onClick={() => {
                const idNumber = r.kind === 'bungalow' ? window.prompt('KTP / passport number') ?? '' : '';
                checkIn.mutate({ id: r.id, idType: idNumber ? 'ktp' : undefined, idNumber: idNumber || undefined }, { onSuccess: () => toast('Checked in') });
              }}>Check in</button>}
              {r.status === 'checked_in' && <button className="oc-btn oc-btn-primary oc-btn-sm" onClick={() => checkOut.mutate({ id: r.id }, { onSuccess: () => toast('Checked out') })}>Check out</button>}
            </div>
          )} />
      </div>
    </div>
  );
}

// ── POS (EP-20) — orders work offline through the sync queue ──────────────

export function POSPage() {
  const toast = useToast();
  const { propertyId } = useAuth();
  const outlet = read(OUTLET_KEY);
  const menu = useGet<Page<Schemas['MenuItem']>>(outlet ? `/api/v1/commercial/outlets/${outlet}/menu` : null);
  const shifts = useGet<Page<Row>>(`/api/v1/commercial/shifts${qs({ 'filter[status]': 'open', 'filter[outletId]': outlet })}`);
  const openShift = useSend<Row>('POST', '/api/v1/commercial/shifts:open', ['/api/v1/commercial/shifts']);
  const [cart, setCart] = useState<Record<string, number>>({});
  const [table, setTable] = useState('');
  const [method, setMethod] = useState('cash');
  const shift = shifts.data?.items.find((s) => s.outletId === outlet);
  const items = menu.data?.items ?? [];
  const total = items.reduce((s, p) => s + Number(p.price) * (cart[p.productId] ?? 0), 0);
  if (!outlet) return <Empty title="Choose an outlet on the Home screen first" icon="storefront" />;
  const checkout = async () => {
    const id = uuidv7();
    const order = { id, outletId: outlet, shiftId: shift?.id, tableNo: table, send: true, offline: !navigator.onLine,
      lines: Object.entries(cart).filter(([, n]) => n > 0).map(([productId, n]) => ({ productId, quantity: String(n) })) };
    await enqueue('commercial.pos_order', { order, payment: { shiftId: shift?.id, tenders: [{ methodType: method }] } }, propertyId);
    setCart({});
    setTable('');
    toast(navigator.onLine ? 'Order sent' : 'Offline: order queued and will sync automatically');
  };
  return (
    <div className="oc-stack">
      <Head title="POS" help={shift ? `Shift ${String(shift.shiftNo)} open` : 'Open a shift to start selling'} />
      <ErrorAlert error={openShift.error} />
      {!shift && (
        <button className="oc-btn oc-btn-ink" onClick={() => {
          const cash = window.prompt('Opening cash', '500000');
          if (cash !== null) openShift.mutate({ outletId: outlet, openingCash: cash });
        }}>Open shift</button>
      )}
      <div className="oc-grid">
        {items.map((p) => (
          <button key={p.productId} className="oc-card" style={{ textAlign: 'left', cursor: 'pointer', minHeight: 90 }} onClick={() => setCart({ ...cart, [p.productId]: (cart[p.productId] ?? 0) + 1 })}>
            <strong>{p.name}</strong><div className="oc-small">{money(p.price)}</div>
            {cart[p.productId] ? <span className="oc-chip">× {cart[p.productId]}</span> : null}
          </button>
        ))}
      </div>
      <Card title="Current order" icon="receipt">
        <div className="oc-row-wrap">
          <div style={{ width: 140 }}><TextField label="Table" value={table} onChange={setTable} /></div>
          <div style={{ width: 200 }}><SelectField label="Payment" value={method} onChange={setMethod}
            options={['cash', 'qris', 'card', 'member_account'].map((m) => ({ value: m, label: m.replace('_', ' ') }))} /></div>
          <div className="oc-metric" style={{ alignSelf: 'flex-end' }}>{money(total)}</div>
          <span className="oc-spacer" />
          <button className="oc-btn oc-btn-neutral" style={{ alignSelf: 'flex-end' }} onClick={() => setCart({})}>Clear</button>
          <button className="oc-btn oc-btn-ink" style={{ alignSelf: 'flex-end' }} disabled={total === 0 || !shift} onClick={() => void checkout()}>Pay & send</button>
        </div>
      </Card>
    </div>
  );
}

// ── Kitchen Display (EP-21) ───────────────────────────────────────────────

export function KitchenPage() {
  const toast = useToast();
  const [station, setStation] = useState('');
  const tickets = useGet<Page<Schemas['Ticket']>>(`/api/v1/commercial/kitchen-orders${qs({ station })}`, { refetchInterval: 30_000 });
  useLive('/api/v1/commercial/kds/stream', ['commercial.kds'], useMemo(() => () => void tickets.refetch(), [tickets]));
  const state = useSend<Row>('POST', (b) => `/api/v1/commercial/kitchen-orders/${b.id}:state`, ['/api/v1/commercial/kitchen-orders']);
  const cols: [string, string][] = [['received', 'Received'], ['preparing', 'Preparing'], ['ready', 'Ready']];
  const next: Record<string, string> = { received: 'preparing', preparing: 'ready', ready: 'served' };
  return (
    <div className="oc-stack">
      <div className="oc-page-head"><div><h1>Kitchen</h1></div><span className="oc-spacer" />
        <div style={{ width: 200 }}><TextField label="Station" value={station} onChange={setStation} placeholder="kitchen, bar…" /></div></div>
      <ErrorAlert error={state.error} />
      <div className="oc-grid-3" style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(260px, 1fr))', gap: 16 }}>
        {cols.map(([s, label]) => (
          <div key={s} className="oc-stack">
            <h3>{label}</h3>
            {(tickets.data?.items ?? []).filter((t) => t.status === s).map((t) => (
              <div key={t.id} className="oc-card">
                <div className="oc-row"><strong>{t.orderNo}</strong><span className="oc-spacer" /><span className="oc-small">{t.tableNo ? `Table ${t.tableNo}` : t.destinationRef ?? t.servingDestination}</span></div>
                <div className="oc-small oc-muted">{t.outletName} · {formatDateTime(t.receivedAt)}</div>
                <ul style={{ margin: '8px 0', paddingLeft: 18 }}>{(t.items as unknown as Row[]).map((i, n) => <li key={n}>{String(i.quantity)} × {String(i.name)}{i.notes ? ` (${String(i.notes)})` : ''}</li>)}</ul>
                <button className="oc-btn oc-btn-ink oc-btn-sm oc-btn-block" onClick={() => state.mutate({ id: t.id, state: next[s] }, { onSuccess: () => toast('Updated') })}>
                  {next[s] === 'served' ? 'Served' : next[s] === 'ready' ? 'Ready' : 'Start'}</button>
              </div>
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}

// ── Clubhouse Screen: Hall of Fame kiosk (FR-HOF-05) ──────────────────────

export function ClubhouseScreenPage() {
  const { propertyId } = useAuth();
  const feed = useGet<Schemas['Kiosk']>(propertyId ? `/api/v1/public/hall-of-fame/kiosk?propertyId=${propertyId}` : null, { refetchInterval: 10 * 60_000 });
  const [i, setI] = useState(0);
  const entries = feed.data?.entries ?? [];
  useEffect(() => {
    const t = setInterval(() => setI((x) => x + 1), (feed.data?.rotateSeconds ?? 12) * 1000);
    return () => clearInterval(t);
  }, [feed.data?.rotateSeconds]);
  if (entries.length === 0) return <Empty title="Hall of Fame" help="No published entries yet." icon="emoji_events" />;
  const e = entries[i % entries.length];
  return (
    <div className="oc-card oc-card-ink" style={{ minHeight: '70vh', display: 'flex', flexDirection: 'column', justifyContent: 'center', alignItems: 'center', textAlign: 'center' }}>
      <Icon name="emoji_events" size={72} />
      <div className="oc-small" style={{ opacity: 0.7, textTransform: 'uppercase', letterSpacing: 2 }}>{e.category.replace(/_/g, ' ')}</div>
      <h1 style={{ fontSize: 48, margin: '12px 0' }}>{e.title}</h1>
      <div style={{ fontSize: 28 }}>{e.playerName}</div>
      <div style={{ opacity: 0.7 }}>{e.year ?? ''}{e.teeSet ? ` · ${e.teeSet}` : ''}{e.score ? ` · ${e.score}` : ''}</div>
    </div>
  );
}

// ── routes and home tiles (mounted by areas/ops.tsx) ───────────

/** Ops routes of P2, at the paths of the server navigation. */
export const P2_OPS_ROUTES = [
  { path: 'starter/pace', element: <PaceOfPlayPage /> },
  { path: 'caddy/incidents', element: <CaddyIncidentsPage /> },
  { path: 'golf-staff/inspection', element: <GolfCartInspectionPage /> },
  { path: 'stay-desk', element: <StayDeskPage /> },
  { path: 'driving-range', element: <DrivingRangePage /> },
  { path: 'sport-reception', element: <SportReceptionPage /> },
  { path: 'instructor', element: <InstructorPage /> },
  { path: 'pos', element: <POSPage /> },
  { path: 'kitchen', element: <KitchenPage /> },
  { path: 'clubhouse-screen', element: <ClubhouseScreenPage /> },
];

/** Home tiles of the P2 workstations (P1's golf tiles come first). */
export function P2Tiles() {
  const { can } = useAuth();
  const tiles: [string, string, string, string][] = [
    ['sports_golf', 'Driving Range', '/ops/driving-range', 'golf.range.operate'], ['sports_tennis', 'Sport Reception', '/ops/sport-reception', 'sportclub.access.validate'],
    ['school', 'Instructor', '/ops/instructor', 'sportclub.class.attendance'], ['hotel', 'Stay Front Desk', '/ops/stay-desk', 'stay.stay.view'],
    ['point_of_sale', 'POS', '/ops/pos', 'commercial.order.create'], ['skillet', 'Kitchen', '/ops/kitchen', 'commercial.kitchen.view'],
  ];
  const shown = tiles.filter((t) => can(t[3]));
  if (shown.length === 0) return null;
  return (
    <div className="oc-grid">
      {shown.map(([icon, label, to]) => (
        <Link key={label} to={to} className="oc-card" style={{ minHeight: 120, textDecoration: 'none' }}>
          <div className="oc-card-head"><span className="oc-icon-circle"><Icon name={icon} size={22} /></span><h3>{label}</h3></div>
        </Link>
      ))}
    </div>
  );
}
