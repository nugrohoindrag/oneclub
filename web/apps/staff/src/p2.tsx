import React, { useState } from 'react';
import { Link, useParams } from 'react-router';
import { qs, useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDate, formatDateTime, formatNumber } from '@oneclub/i18n';
import {
  AutoResourcePage, Card, DataTable, DateRange, Empty, ErrorAlert, Icon, PageHeader, ResourceIndex, Skeleton, StatTile, StatusPill, TextField, useAuth, useToast,
} from '@oneclub/shell';

type Row = Record<string, unknown>;

/** Column spec: key, header and optional kind. */
type Col = [key: string, header: string, kind?: 'date' | 'datetime' | 'status' | 'money' | 'bool' | 'list'];

interface OpList {
  slug: string;
  title: string;
  help?: string;
  path: string;
  perm: string;
  cols: Col[];
  dated?: boolean;
  action?: { label: string; perm: string; path: (r: Row) => string; body?: Row; show?: (r: Row) => boolean };
}

interface Hub {
  path: string;
  title: string;
  /** Master data: every resource of these modules, or only these keys. */
  modules?: string[];
  only?: string[];
  lists: OpList[];
}

const money = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : `Rp ${formatNumber(Number(v))}`);

function cell(r: Row, [key, , kind]: Col): React.ReactNode {
  const v = key.split('.').reduce<unknown>((o, k) => (o && typeof o === 'object' ? (o as Row)[k] : undefined), r);
  if (v === null || v === undefined || v === '') return '—';
  switch (kind) {
    case 'date': return formatDate(String(v));
    case 'datetime': return formatDateTime(String(v));
    case 'status': return <StatusPill status={String(v)} />;
    case 'money': return money(v);
    case 'bool': return v ? 'Yes' : 'No';
    case 'list': return Array.isArray(v) ? v.join(', ') : String(v);
  }
  return String(v);
}

/** P2 hubs (PRD P2 §7.1) at the paths of the server navigation: operational
 * lists plus the master data of P2's resources. P1's modules keep their own
 * pages; a hub inside them only adds what P2 brings. */
export const HUBS: Hub[] = [
  { path: 'golf/operations', title: 'Round Operations', lists: [
    { slug: 'pace', title: 'Pace of Play', path: '/api/v1/golf/pace-of-play', perm: 'golf.pace.view',
      cols: [['label', 'Flight'], ['playingRouteName', 'Route'], ['hole', 'Hole'], ['elapsedMinutes', 'Elapsed'], ['targetMinutes', 'Target'], ['behindMinutes', 'Behind'],
        ['aheadLabel', 'Ahead'], ['gapHoles', 'Gap (holes)'], ['slow', 'Slow', 'bool']] },
    { slug: 'caddy-rotation', title: 'Next Assignment', path: '/api/v1/golf/caddy-rotation', perm: 'golf.caddy.view', dated: true,
      cols: [['position', '#'], ['code', 'Caddy No.'], ['name', 'Caddy'], ['level', 'Level'], ['roundsToday', 'Rounds today'], ['lastAssignedAt', 'Last out', 'datetime']] },
    { slug: 'caddy-attendance', title: 'Caddy Attendance', path: '/api/v1/golf/caddy-attendance', perm: 'golf.caddy.view', dated: true,
      cols: [['queueNo', '#'], ['caddyCode', 'Caddy No.'], ['caddyName', 'Caddy'], ['shift', 'Shift'], ['clockedInAt', 'In', 'datetime'], ['clockedOutAt', 'Out', 'datetime'],
        ['roundsToday', 'Rounds']] },
    { slug: 'caddy-utilization', title: 'Caddy Utilization', path: '/api/v1/golf/caddy-utilization', perm: 'golf.caddy.view',
      cols: [['code', 'Caddy No.'], ['name', 'Caddy'], ['daysPresent', 'Days'], ['rounds', 'Rounds'], ['roundsPerDay', 'Rounds / day'], ['dutyHours', 'Duty h'], ['utilizationRate', 'Utilization']] },
    { slug: 'caddy-settlements', title: 'Caddy Settlement', path: '/api/v1/golf/caddy-settlements', perm: 'golf.caddy_settlement.view',
      cols: [['number', 'Settlement'], ['caddyName', 'Caddy'], ['periodStart', 'From', 'date'], ['periodEnd', 'To', 'date'], ['caddyFee', 'Caddy Fee', 'money'],
        ['tips', 'Tips', 'money'], ['deductions', 'Deductions', 'money'], ['total', 'Total', 'money'], ['status', 'Status', 'status']],
      action: { label: 'Record payment', perm: 'golf.caddy_settlement.pay', path: (r) => `/api/v1/golf/caddy-settlements/${r.id}:pay`, body: { methodType: 'bank_transfer' },
        show: (r) => r.status === 'approved' } },
    { slug: 'caddy-liabilities', title: 'Caddy Fee Liability', path: '/api/v1/golf/caddy-liabilities', perm: 'golf.caddy_settlement.view',
      cols: [['code', 'Caddy No.'], ['name', 'Caddy'], ['recorded', 'Recorded', 'money'], ['paid', 'Paid', 'money'], ['deducted', 'Deducted', 'money'], ['liability', 'Liability', 'money']] },
    { slug: 'caddy-incidents', title: 'Caddy Incidents', path: '/api/v1/golf/caddy-incidents', perm: 'golf.caddy_incident.view',
      cols: [['number', 'Incident'], ['category', 'Category'], ['severity', 'Severity'], ['description', 'Description'], ['occurredAt', 'When', 'datetime'], ['status', 'Status', 'status']] },
    { slug: 'cart-incidents', title: 'Golf Cart Incidents', path: '/api/v1/golf/golf-cart-incidents', perm: 'golf.cart_incident.view',
      cols: [['number', 'Incident'], ['category', 'Category'], ['severity', 'Severity'], ['damageAmount', 'Damage', 'money'], ['damageStatus', 'Damage charge', 'status'],
        ['occurredAt', 'When', 'datetime'], ['status', 'Status', 'status']] },
    { slug: 'cart-readiness', title: 'Golf Cart Readiness', path: '/api/v1/golf/golf-cart-readiness', perm: 'golf.golf_cart.view',
      cols: [['code', 'Golf Cart'], ['readiness', 'Readiness', 'status'], ['batteryPercent', 'Battery %'], ['hoursSinceService', 'Since service h'],
        ['serviceDue', 'Service due', 'bool'], ['bookingCode', 'Booking']] },
    { slug: 'cart-maintenance', title: 'Golf Cart Maintenance', path: '/api/v1/golf/golf-cart-maintenance', perm: 'golf.cart_maintenance.view',
      cols: [['number', 'Maintenance'], ['golfCartCode', 'Golf Cart'], ['category', 'Category'], ['description', 'Description'], ['cost', 'Cost', 'money'],
        ['openedAt', 'Opened', 'datetime'], ['status', 'Status', 'status']] },
    { slug: 'scorecards', title: 'Scoring', path: '/api/v1/golf/scorecards', perm: 'golf.scorecard.view_all',
      cols: [['playedOn', 'Date', 'date'], ['playerName', 'Player'], ['playingRouteName', 'Route'], ['teeSetName', 'Tee'], ['gross', 'Gross'], ['differential', 'Differential'],
        ['status', 'Status', 'status']],
      action: { label: 'Finalize', perm: 'golf.scorecard.finalize', path: (r) => `/api/v1/golf/scorecards/${r.id}:finalize`, show: (r) => r.status === 'submitted' } },
    { slug: 'hole-in-ones', title: 'Hole-in-One Records', path: '/api/v1/golf/hole-in-ones', perm: 'golf.hio.view',
      cols: [['number', 'Record'], ['playerName', 'Player'], ['hole', 'Hole'], ['achievedOn', 'Date', 'date'], ['insured', 'Insured', 'bool'], ['claimStatus', 'Claim'],
        ['status', 'Status', 'status']] },
    { slug: 'range-sessions', title: 'Driving Range', path: '/api/v1/golf/range-sessions', perm: 'golf.range.view',
      cols: [['number', 'Session'], ['bayCode', 'Bay'], ['area', 'Area'], ['customerName', 'Customer'], ['guestName', 'Guest'], ['queuePosition', 'Queue'], ['balls', 'Balls'],
        ['status', 'Status', 'status']] },
    { slug: 'reciprocal-visits', title: 'Reciprocal Visits', path: '/api/v1/golf/reciprocal-visits', perm: 'golf.reciprocal_visit.view',
      cols: [['number', 'Visit'], ['direction', 'Direction'], ['clubName', 'Club'], ['country', 'Country'], ['visitorName', 'Visitor'], ['visitDate', 'Date', 'date'],
        ['chargeAmount', 'Charge', 'money'], ['settlementStatus', 'Settlement', 'status']] },
    { slug: 'introduction-letters', title: 'Introduction Letters', path: '/api/v1/golf/introduction-letters', perm: 'golf.introduction_letter.view',
      cols: [['number', 'Letter'], ['customerName', 'Member'], ['clubName', 'Club'], ['playFrom', 'From', 'date'], ['playTo', 'To', 'date'], ['status', 'Status', 'status']] },
  ] },
  { path: 'golf/master', title: 'Golf Master Data', only: ['golf.caddy_level', 'golf.cart_checklist', 'golf.range_bay', 'golf.reciprocal_club', 'golf.hall_of_fame'], lists: [] },
  { path: 'sport-club', title: 'Sport Club', modules: ['sportclub'], lists: [
    { slug: 'bookings', title: 'Bookings', path: '/api/v1/sportclub/bookings', perm: 'sportclub.booking.view', dated: true,
      cols: [['code', 'Booking'], ['customerName', 'Customer'], ['guestName', 'Guest'], ['start', 'Start', 'datetime'], ['channel', 'Channel'], ['status', 'Status', 'status']] },
    { slug: 'entries', title: 'Entries', path: '/api/v1/sportclub/entries', perm: 'sportclub.entry.view', dated: true,
      cols: [['ticketNo', 'Ticket'], ['facilityName', 'Facility'], ['entryType', 'Entry'], ['customerName', 'Customer'], ['guestName', 'Guest'], ['amount', 'Amount', 'money'],
        ['status', 'Status', 'status']] },
    { slug: 'class-sessions', title: 'Class Schedule', path: '/api/v1/sportclub/class-sessions', perm: 'sportclub.class.view',
      cols: [['start', 'Start', 'datetime'], ['programName', 'Class'], ['instructorName', 'Instructor'], ['capacity', 'Capacity'], ['booked', 'Booked'], ['status', 'Status', 'status']] },
    { slug: 'instructor-fees', title: 'Instructor Fees', path: '/api/v1/sportclub/instructor-fees', perm: 'sportclub.instructor_fee.view',
      cols: [['feeNo', 'Statement'], ['instructorName', 'Instructor'], ['periodStart', 'From', 'date'], ['periodEnd', 'To', 'date'], ['sessions', 'Sessions'], ['amount', 'Amount', 'money'],
        ['status', 'Status', 'status']] },
    { slug: 'occupancy', title: 'Facility Occupancy', path: '/api/v1/sportclub/occupancy', perm: 'sportclub.access.view',
      cols: [['facilityName', 'Facility'], ['capacity', 'Capacity'], ['inside', 'Inside'], ['entriesToday', 'Entries today'], ['booked', 'Booked']] },
  ] },
  { path: 'membership/lifecycle', title: 'Lifecycle Requests', lists: [
    { slug: 'requests', title: 'Lifecycle Requests', path: '/api/v1/membership/requests', perm: 'membership.membership.view',
      cols: [['memberNo', 'Member No.'], ['requestType', 'Request'], ['reason', 'Reason'], ['channel', 'Channel'], ['createdAt', 'Requested', 'datetime'], ['status', 'Status', 'status']] },
    { slug: 'annual-fees', title: 'Annual Fees', path: '/api/v1/membership/annual-fees', perm: 'membership.membership.view',
      cols: [['memberNo', 'Member No.'], ['memberName', 'Member'], ['feeType', 'Fee'], ['dueDate', 'Due', 'date'], ['amount', 'Amount', 'money'], ['status', 'Status', 'status']] },
  ] },
  { path: 'booking/all-lines', title: 'All Business Lines', only: ['reservation.resource_type', 'reservation.resource'], lists: [
    { slug: 'reservations', title: 'Bookings (all lines)', path: '/api/v1/reservation/reservations', perm: 'reservation.reservation.view',
      cols: [['code', 'Booking'], ['businessLine', 'Line'], ['customerName', 'Customer'], ['guestName', 'Guest'], ['start', 'Start', 'datetime'], ['channel', 'Channel'],
        ['status', 'Status', 'status']] },
  ] },
  { path: 'stay-venue', title: 'Stay & Venue', modules: ['stay'], lists: [
    { slug: 'stays', title: 'Reservations', path: '/api/v1/stay/stays', perm: 'stay.stay.view',
      cols: [['stayNo', 'Stay'], ['kind', 'Kind'], ['unitName', 'Unit'], ['customerName', 'Customer'], ['guestName', 'Guest'], ['start', 'From', 'datetime'], ['end', 'To', 'datetime'],
        ['status', 'Status', 'status']],
      action: { label: 'Confirm request', perm: 'stay.stay.update', path: (r) => `/api/v1/stay/stays/${r.id}:confirm`, show: (r) => r.status === 'requested' } },
  ] },
  { path: 'crm/engagement', title: 'Feedback & Campaigns', only: ['crm.segment', 'crm.campaign'], lists: [
    { slug: 'feedback', title: 'Feedback', path: '/api/v1/crm/feedback', perm: 'crm.feedback.view',
      cols: [['createdAt', 'When', 'datetime'], ['customerName', 'Customer'], ['contextLabel', 'Visit'], ['rating', 'Rating'], ['nps', 'NPS'], ['comment', 'Comment'],
        ['followUp', 'Follow-up', 'status']] },
  ] },
  { path: 'commercial/operations', title: 'Voucher & Prepaid', only: ['commercial.voucher_type'], lists: [
    { slug: 'vouchers', title: 'Vouchers', path: '/api/v1/commercial/vouchers', perm: 'commercial.voucher.view',
      cols: [['code', 'Code'], ['typeName', 'Type'], ['customerName', 'Customer'], ['remainingQuantity', 'Remaining'], ['unit', 'Unit'], ['expiresAt', 'Expires', 'date'],
        ['status', 'Status', 'status']] },
    { slug: 'prepaid', title: 'Prepaid', path: '/api/v1/commercial/prepaid-balances', perm: 'commercial.voucher.view',
      cols: [['customerName', 'Customer'], ['typeName', 'Package'], ['remaining', 'Balance'], ['unit', 'Unit'], ['nextExpiry', 'Next expiry', 'date']] },
    { slug: 'orders', title: 'Orders', path: '/api/v1/commercial/orders', perm: 'commercial.order.view',
      cols: [['orderNo', 'Order'], ['outletName', 'Outlet'], ['orderType', 'Type'], ['source', 'Source'], ['total', 'Total', 'money'], ['serviceStatus', 'Kitchen', 'status'],
        ['status', 'Status', 'status']] },
    { slug: 'shifts', title: 'Shifts', path: '/api/v1/commercial/shifts', perm: 'commercial.shift.view',
      cols: [['shiftNo', 'Shift'], ['outletName', 'Outlet'], ['cashierName', 'Cashier'], ['openedAt', 'Opened', 'datetime'], ['closedAt', 'Closed', 'datetime'],
        ['status', 'Status', 'status']] },
  ] },
  { path: 'commercial/master', title: 'Outlets & Products', only: ['commercial.outlet', 'commercial.product', 'commercial.product_variant', 'commercial.menu',
    'commercial.modifier_group', 'commercial.modifier', 'commercial.package_rate', 'commercial.day_type_set', 'commercial.line_day_type'], lists: [] },
  { path: 'inventory', title: 'Inventory', modules: ['inventory'], lists: [] },
];

/** Module hub: master data + operational lists the user may open. */
export function HubPage({ hub }: { hub: Hub }) {
  const { can } = useAuth();
  const lists = hub.lists.filter((l) => can(l.perm));
  if (hub.lists.length === 1 && !hub.modules && !hub.only) return <OpListPage hub={hub} slug={hub.lists[0].slug} />;
  return (
    <div className="oc-stack">
      <PageHeader title={hub.title} />
      {lists.length > 0 && (
        <Card title="Operations" icon="dashboard">
          <div className="oc-grid">
            {lists.map((l) => (
              <Link key={l.slug} to={`/${hub.path}/${l.slug}`} className="oc-card" style={{ textDecoration: 'none' }}>
                <div className="oc-card-head" style={{ marginBottom: 0 }}><span className="oc-icon-circle"><Icon name="list_alt" size={20} /></span><h3>{l.title}</h3></div>
              </Link>
            ))}
          </div>
        </Card>
      )}
      {(hub.modules || hub.only) && <ResourceIndex modules={hub.modules} only={hub.only} base={`/${hub.path}/m`} />}
    </div>
  );
}

/** Operational list with optional date filter and one row action. */
export function OpListPage({ hub, slug }: { hub: Hub; slug: string }) {
  const l = hub.lists.find((x) => x.slug === slug);
  const toast = useToast();
  const { can } = useAuth();
  const [date, setDate] = useState(new Date().toISOString().slice(0, 10));
  const path = l ? `${l.path}${l.dated ? qs({ date }) : ''}` : null;
  const data = useGet<Page<Row> | { golfCarts: Row[]; counts: Record<string, number> }>(path, { refetchInterval: l?.slug === 'pace' || l?.slug === 'cart-readiness' ? 15000 : undefined });
  const act = useSend<Row>('POST', (b) => String(b.__path), l ? [l.path] : []);
  if (!l) return <Empty title="Not found" />;
  const rows = (data.data && 'items' in data.data ? data.data.items : data.data && 'golfCarts' in data.data ? data.data.golfCarts : []) as Row[];
  return (
    <div className="oc-stack">
      <PageHeader title={l.title} help={l.help} actions={hub.lists.length > 1 || hub.modules || hub.only ? <Link className="oc-btn oc-btn-neutral" to={`/${hub.path}`}>Back</Link> : undefined} />
      {l.dated && <div style={{ width: 220 }}><TextField label="Date" type="date" value={date} onChange={setDate} /></div>}
      {data.data && 'counts' in data.data && (
        <div className="oc-row-wrap">{Object.entries(data.data.counts).map(([k, v]) => <span key={k} className="oc-chip"><StatusPill status={k} /> {v}</span>)}</div>
      )}
      <ErrorAlert error={act.error} />
      <div className="oc-card">
        <DataTable rows={rows} loading={data.isLoading} error={data.error} rowKey={(r) => String(r.id ?? r.caddyId ?? r.flightId ?? r.code ?? JSON.stringify(r))}
          columns={l.cols.map((c) => ({ key: c[0], header: c[1], render: (r: Row) => cell(r, c) }))}
          actions={l.action && can(l.action.perm) ? (r) => (l.action!.show?.(r) ?? true) ? (
            <button className="oc-btn oc-btn-outline oc-btn-sm" disabled={act.isPending}
              onClick={() => act.mutate({ ...(l.action!.body ?? {}), __path: l.action!.path(r) }, { onSuccess: () => { toast('Done'); void data.refetch(); } })}>{l.action!.label}</button>
          ) : null : undefined} />
      </div>
    </div>
  );
}

export function HubMasterPage() {
  const { key } = useParams();
  return <AutoResourcePage resourceKey={String(key)} />;
}

// ── KPI dashboards (FR-RPT-P2-01..05) ─────────────────────────────────────

export const DASHBOARDS: [string, string][] = [
  ['golf-performance', 'Golf Performance'], ['sport-club-performance', 'Sport Club Performance'], ['membership-performance', 'Membership Performance'],
  ['booking-performance', 'Booking Performance'], ['commercial-performance', 'Commercial Performance'], ['crm-performance', 'CRM Performance'],
];

function kpiValue(k: Schemas['KPI']) {
  if (k.unit === 'idr') return money(k.value);
  if (k.unit === 'ratio') return `${(Number(k.value) * 100).toFixed(1)}%`;
  return formatNumber(Number(k.value));
}

export function KPIDashboardPage({ code }: { code: string }) {
  const today = new Date();
  const [from, setFrom] = useState(new Date(today.getFullYear(), today.getMonth(), 1).toISOString().slice(0, 10));
  const [to, setTo] = useState(today.toISOString().slice(0, 10));
  const d = useGet<Schemas['PerformanceDashboard']>(`/api/v1/reporting/dashboards/${code}${qs({ from, to })}`);
  return (
    <div className="oc-stack">
      <PageHeader title={d.data?.name ?? DASHBOARDS.find((x) => x[0] === code)?.[1] ?? ''} help={d.data ? `${formatDate(d.data.from)} – ${formatDate(d.data.to)}` : undefined}
        actions={<DateRange from={from} to={to} onFrom={setFrom} onTo={setTo} />} />
      <ErrorAlert error={d.error} />
      {!d.data && <Skeleton rows={6} />}
      <div className="oc-stat-grid">
        {d.data?.kpis.map((k) => (
          <StatTile key={k.key} label={k.label} title={k.definition} value={kpiValue(k)}>
            {k.breakdown?.map((b) => <div key={b.label} className="oc-row"><span>{b.label.replace(/_/g, ' ')}</span><span className="oc-spacer" />
              <strong>{k.unit === 'idr' ? money(b.value) : formatNumber(Number(b.value))}</strong></div>)}
            {k.definition && <div>{k.definition}</div>}
          </StatTile>
        ))}
      </div>
    </div>
  );
}

// ── Customer 360 (FR-CRM-01) ──────────────────────────────────────────────

export function CustomerLines360Page() {
  const { id } = useParams();
  const v = useGet<Schemas['Customer360']>(`/api/v1/crm/customers/${id}/360`);
  const b = useGet<Schemas['BehaviorProfile']>(`/api/v1/crm/customers/${id}/behavior`);
  if (v.isLoading) return <Skeleton rows={10} />;
  if (!v.data) return <ErrorAlert error={v.error} />;
  const p = v.data.profile;
  const sections = v.data.sections as Record<string, unknown>;
  return (
    <div className="oc-stack">
      <PageHeader title={p.name} help={[p.code, p.email, p.phone].filter(Boolean).join(' · ')}
        actions={<Link className="oc-btn oc-btn-neutral" to={`/crm/customer-360?id=${id}`}>Back to Customer 360</Link>} />
      <div className="oc-grid-2">
        <Card title="Preferences" icon="favorite">
          {v.data.preferences.length === 0 ? <div className="oc-muted oc-small">No preferences recorded</div> : (
            <div className="oc-stack">{v.data.preferences.map((x) => <div key={`${x.category}-${x.key}`} className="oc-row"><StatusPill status={x.category} label={x.category.replace(/_/g, ' ')} /><span>{x.value}</span></div>)}</div>
          )}
        </Card>
        <Card title="Behaviour" icon="psychology">
          {b.data && !b.data.consent ? <div className="oc-muted oc-small">No profiling consent (UU PDP)</div> : (
            <div className="oc-stack">{Object.entries(b.data?.lines ?? {}).flatMap(([k, l]) => l.highlights.map((h) => <div key={k + h} className="oc-small">• {h}</div>))}</div>
          )}
        </Card>
      </div>
      {Object.entries(sections).map(([k, s]) => (
        <Card key={k} title={k.charAt(0).toUpperCase() + k.slice(1)} icon="dataset">
          <pre className="oc-code" style={{ whiteSpace: 'pre-wrap', margin: 0, maxHeight: 280, overflow: 'auto' }}>{JSON.stringify(s, null, 2)}</pre>
        </Card>
      ))}
      <Card title="Interaction History" icon="forum">
        <DataTable rows={v.data.interactions as unknown as Row[]} columns={[
          { key: 'occurredAt', header: 'When', render: (r) => formatDateTime(String(r.occurredAt)) }, { key: 'channel', header: 'Channel' },
          { key: 'subject', header: 'Subject' }, { key: 'source', header: 'Source' }]} />
      </Card>
    </div>
  );
}

// ── routes (added to P1's router in main.tsx) ─────────────────────────────

/** Back Office routes of the P2 hubs, Customer 360 across lines and the KPI dashboards. */
export const P2_ROUTES = [
  ...HUBS.flatMap((h) => [
    { path: h.path, element: <HubPage hub={h} /> },
    { path: `${h.path}/m/:key`, element: <HubMasterPage /> },
    ...h.lists.map((l) => ({ path: `${h.path}/${l.slug}`, element: <OpListPage hub={h} slug={l.slug} /> })),
  ]),
  { path: 'crm/customers/:id', element: <CustomerLines360Page /> },
  ...DASHBOARDS.map(([code]) => ({ path: `dashboards/${code}`, element: <KPIDashboardPage key={code} code={code} /> })),
];

/** Management dashboards P2 adds next to P1's golf, membership and booking pages. */
export const P2_MANAGEMENT_ROUTES = DASHBOARDS.filter(([code]) => ['sport-club-performance', 'commercial-performance', 'crm-performance'].includes(code))
  .map(([code]) => ({ path: code, element: <KPIDashboardPage key={code} code={code} /> }));
