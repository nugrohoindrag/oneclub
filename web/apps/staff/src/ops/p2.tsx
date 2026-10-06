import React, { useMemo, useState } from 'react';
import { qs, request, uuidv7, useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDateTime, formatNumber } from '@oneclub/i18n';
import { enqueue } from '@oneclub/offline';
import { Link } from 'react-router';
import { useLive } from '../live';
import { OUTLET_KEY, read } from '../offline';
import { PosPromotionPanel, usePosPromotions } from '../p3/commercial';
import { PosTierLine, posTierDiscount, usePosTierDiscount, useCustomerTiers } from '../p5/tiers';
import {
  Card, Checkbox, DataTable, Empty, ErrorAlert, Icon, SelectField, StatusPill, TextField, useAuth, useToast,
} from '@oneclub/shell';

type Row = Record<string, unknown>;
const money = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : `Rp ${formatNumber(Number(v))}`);
const idem = () => ({ 'Idempotency-Key': uuidv7() });

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

/** POS customer: search CRM customers (member price, personal promo codes, Redeem Points). */
function PosCustomerPicker({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const [q, setQ] = useState('');
  const list = useGet<Page<Row>>(q.trim().length >= 2 ? `/api/v1/crm/customers${qs({ q, limit: 20, 'filter[status]': 'active' })}` : null);
  const tiers = useCustomerTiers((list.data?.items ?? []).map((c) => String(c.id))); // PRD P5 tier class in the picker
  return (
    <>
      <div style={{ width: 220 }}><TextField label="Find customer" value={q} onChange={setQ} placeholder="Name, phone or code" /></div>
      <div style={{ width: 240 }}><SelectField label="Customer" value={value} onChange={onChange} placeholder="Walk-in guest"
        options={(list.data?.items ?? []).map((c) => ({ value: String(c.id), label: `${String(c.name)} (${String(c.code)})${tiers.get(String(c.id))?.tierName
          ? ` · ${String(tiers.get(String(c.id))?.tierName)}` : ''}` }))} /></div>
    </>
  );
}

const POS_METHODS = ['cash', 'qris', 'card', 'member_account'];

export function POSPage() {
  const toast = useToast();
  const { propertyId, can } = useAuth();
  const outlet = read(OUTLET_KEY);
  // PRD P4 FR-CNS-07: stock-tracked retail items carry their outlet stock (K9) and Sold Out
  const menu = useGet<Page<Schemas['MenuItem'] & { stockTracked?: boolean; available?: string | null; soldOut?: boolean }>>(
    outlet ? `/api/v1/commercial/outlets/${outlet}/menu` : null, { refetchInterval: 60_000 });
  const shifts = useGet<Page<Row>>(`/api/v1/commercial/shifts${qs({ 'filter[status]': 'open', 'filter[outletId]': outlet })}`);
  const openShift = useSend<Row>('POST', '/api/v1/commercial/shifts:open', ['/api/v1/commercial/shifts']);
  const [cart, setCart] = useState<Record<string, number>>({});
  const [table, setTable] = useState('');
  const [method, setMethod] = useState('cash');
  const [customer, setCustomer] = useState('');
  const [points, setPoints] = useState('');
  const [rest, setRest] = useState('cash');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const online = typeof navigator === 'undefined' || navigator.onLine;
  // PRD P3 FR-LOY-05 / FR-OPS-P3-03: points of the customer as a tender
  const acctQ = useGet<Page<Row>>(customer && can('crm.loyalty_account.view') ? `/api/v1/crm/loyalty/accounts${qs({ 'filter[customerId]': customer })}` : null);
  const acct = acctQ.data?.items.find((a) => a.status === 'active');
  const shift = shifts.data?.items.find((s) => s.outletId === outlet);
  const items = menu.data?.items ?? [];
  const total = items.reduce((s, p) => s + Number(p.price) * (cart[p.productId] ?? 0), 0);
  // PRD P3 FR-OPS-P3-03: promotions of the cart (online and offline); the customer unlocks personal codes
  const promo = usePosPromotions(outlet, items.filter((p) => (cart[p.productId] ?? 0) > 0)
    .map((p) => ({ productId: p.productId, quantity: cart[p.productId], unitPrice: Number(p.price) })), customer);
  // PRD P5: tier F&B discount of the member (cached with the member for offline sales)
  const tierDisc = usePosTierDiscount(customer);
  const tierAmount = posTierDiscount(tierDisc, items.filter((p) => (cart[p.productId] ?? 0) > 0)
    .map((p) => ({ productType: String(p.productType), amount: Number(p.price) * (cart[p.productId] ?? 0) })), promo.discount);
  if (!outlet) return <Empty title="Choose an outlet on the Home screen first" icon="storefront" />;
  const due = Math.max(total - promo.discount - tierAmount, 0);
  const pointValue = Number(acct?.redemptionValue ?? 0);
  const maxPoints = acct && pointValue > 0 ? Math.min(Number(acct.balance ?? 0), Math.floor(due / pointValue)) : 0;
  const usePoints = method === 'loyalty_points';
  const methods = [...POS_METHODS, ...(acct && online ? ['loyalty_points'] : [])];
  const reset = () => { setCart({}); setTable(''); setPoints(''); setMethod('cash'); };
  const lines = () => Object.entries(cart).filter(([, n]) => n > 0).map(([productId, n]) => ({ productId, quantity: String(n) }));
  const checkout = async () => {
    const id = uuidv7();
    const order = { id, outletId: outlet, shiftId: shift?.id, tableNo: table, send: true, offline: !navigator.onLine, customerId: customer || undefined,
      lines: lines(), ...promo.orderFields(total - tierAmount) };
    await enqueue('commercial.pos_order', { order, payment: { shiftId: shift?.id, tenders: [{ methodType: method }] } }, propertyId);
    reset();
    toast(navigator.onLine ? 'Order sent' : 'Offline: order queued and will sync automatically');
  };
  // Redeem Points needs the live balance: the sale goes online at once (never queued).
  const checkoutWithPoints = async () => {
    setBusy(true);
    setError(null);
    try {
      const fields = promo.orderFields(total);
      const order = await request<Row>('POST', '/api/v1/commercial/orders', { outletId: outlet, shiftId: shift?.id, tableNo: table || undefined, send: true,
        customerId: customer, lines: lines(), promoCodes: fields.promoCodes, promotionExclusions: fields.promotionExclusions }, idem());
      const n = Math.min(Number(points || maxPoints), maxPoints);
      await request<Row>('POST', `/api/v1/commercial/orders/${String(order.id)}:pay`, { shiftId: shift?.id, tenders: [
        { methodType: 'loyalty_points', tender: { points: n } }, { methodType: rest }] }, idem());
      reset();
      toast(`Paid ${formatNumber(n)} points and ${rest.replace('_', ' ')}`);
      void acctQ.refetch();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="oc-stack">
      <Head title="POS" help={shift ? `Shift ${String(shift.shiftNo)} open` : 'Open a shift to start selling'} />
      <ErrorAlert error={openShift.error ?? error} />
      {!shift && (
        <button className="oc-btn oc-btn-ink" onClick={() => {
          const cash = window.prompt('Opening cash', '500000');
          if (cash !== null) openShift.mutate({ outletId: outlet, openingCash: cash });
        }}>Open shift</button>
      )}
      <div className="oc-grid">
        {items.map((p) => (
          <button key={p.productId} className="oc-card" disabled={p.soldOut} aria-label={p.soldOut ? `${p.name}, sold out` : undefined}
            style={{ textAlign: 'left', cursor: p.soldOut ? 'not-allowed' : 'pointer', minHeight: 90, opacity: p.soldOut ? 0.6 : 1 }}
            onClick={() => setCart({ ...cart, [p.productId]: (cart[p.productId] ?? 0) + 1 })}>
            <strong>{p.name}</strong><div className="oc-small">{money(p.price)}</div>
            {p.soldOut ? <span className="oc-status" data-tone="error">Sold Out</span>
              : p.stockTracked && p.available != null ? <div className="oc-small oc-muted">{formatNumber(Number(p.available))} in stock</div> : null}
            {cart[p.productId] ? <span className="oc-chip">× {cart[p.productId]}</span> : null}
          </button>
        ))}
      </div>
      <Card title="Current order" icon="receipt">
        <div className="oc-row-wrap" aria-label="Customer">
          <PosCustomerPicker value={customer} onChange={(v) => { setCustomer(v); setPoints(''); if (method === 'loyalty_points') setMethod('cash'); }} />
          {acct && <span className="oc-chip" style={{ alignSelf: 'flex-end' }}>{formatNumber(Number(acct.balance))} points · {money(acct.balanceValue)}</span>}
          {tierDisc && <span className="oc-chip" style={{ alignSelf: 'flex-end' }}>{tierDisc.label}</span>}
          {customer && !acctQ.isLoading && !acct && can('crm.loyalty_account.view') && <span className="oc-small oc-muted" style={{ alignSelf: 'flex-end' }}>Not a loyalty member</span>}
        </div>
        <div className="oc-row-wrap">
          <div style={{ width: 140 }}><TextField label="Table" value={table} onChange={setTable} /></div>
          <div style={{ width: 200 }}><SelectField label="Payment" value={method} onChange={setMethod}
            options={methods.map((m) => ({ value: m, label: m === 'loyalty_points' ? 'Redeem Points' : m.replace('_', ' ') }))} /></div>
          {usePoints && <>
            <div style={{ width: 160 }}><TextField label="Points" type="number" inputMode="numeric" min={1} max={maxPoints} value={points} onChange={setPoints}
              placeholder={String(maxPoints)} help={`= ${money(Math.min(Number(points || maxPoints), maxPoints) * pointValue)}`} /></div>
            <div style={{ width: 180 }}><SelectField label="Rest paid by" value={rest} onChange={setRest}
              options={POS_METHODS.map((m) => ({ value: m, label: m.replace('_', ' ') }))} /></div>
          </>}
          <div className="oc-metric" style={{ alignSelf: 'flex-end' }}>{money(due)}</div>
          <span className="oc-spacer" />
          <button className="oc-btn oc-btn-neutral" style={{ alignSelf: 'flex-end' }} onClick={() => setCart({})}>Clear</button>
          <button className="oc-btn oc-btn-ink" style={{ alignSelf: 'flex-end' }} disabled={total === 0 || !shift || busy || (usePoints && (maxPoints <= 0 || !online))}
            onClick={() => void (usePoints ? checkoutWithPoints() : checkout())}>Pay & send</button>
        </div>
        {usePoints && !online && <div className="oc-small oc-muted" role="status">Redeem Points needs a connection; choose another payment while offline.</div>}
        <PosPromotionPanel promo={promo} />
        <PosTierLine t={tierDisc} amount={tierAmount} />
      </Card>
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
];

/** Home tiles of the P2 workstations (P1's golf tiles come first). */
export function P2Tiles() {
  const { can } = useAuth();
  const tiles: [string, string, string, string][] = [
    ['sports_golf', 'Driving Range', '/ops/driving-range', 'golf.range.operate'], ['sports_tennis', 'Sport Reception', '/ops/sport-reception', 'sportclub.access.validate'],
    ['school', 'Instructor', '/ops/instructor', 'sportclub.class.attendance'], ['hotel', 'Stay Front Desk', '/ops/stay-desk', 'stay.stay.view'],
    ['point_of_sale', 'POS', '/ops/pos', 'commercial.order.create'],
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
