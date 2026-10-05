import React, { useMemo, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router';
import { qs, request, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate, formatDateTime } from '@oneclub/i18n';
import { enqueue } from '@oneclub/offline';
import {
  AutoResourcePage, Card, Checkbox, DataTable, Empty, ErrorAlert, Modal, PageHeader, SelectField, Skeleton, StatusPill, TextArea, TextField,
  useAuth, useToast, type Option,
} from '@oneclub/shell';
import { ActionButton, KV, ListPage, Tabs, money, today, type R } from '../p1/common';
import type { AreaRoute, OpsRoute, OpsTile } from './types';

// Banquet & Event (PRD P3 EP-12–15, Naming Convention §11): Events, Banquet,
// MICE, Weddings, Packages, Venues, BEO, Event Schedule, Event Checklist,
// Event Billing, Event Reports; the Event Operations workstation (Today's
// Events, BEO, checklist, guest check-in with QR and offline queue,
// incident notes) and the Kitchen Banquet Production list.

type B = Record<string, unknown>;
const API = '/api/v1/banquet';
const INV = [API];
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const opts = (vals: string[]): Option[] => vals.map((v) => ({ value: v, label: label(v) }));
const pill = (k: string) => (r: R) => <StatusPill status={String(r[k] ?? '')} />;
const LAYOUTS = opts(['round_table', 'classroom', 'u_shape', 'theater', 'boardroom', 'standing', 'banquet', 'cocktail']);
const DEPARTMENTS = opts(['banquet', 'sales', 'kitchen', 'fnb_service', 'venue', 'engineering', 'front_desk', 'golf', 'finance', 'security', 'housekeeping', 'other']);
const CHARGE_KINDS = opts(['corkage', 'outdoor_venue', 'electricity', 'overtime', 'decoration', 'av_equipment', 'vendor_fee', 'additional_fnb', 'venue_rental',
  'damage', 'other']);
const STATUSES = opts(['inquiry', 'tentative', 'definite', 'completed', 'cancelled']);
const toLocalInput = (d: Date) => new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
const iso = (local: string) => (local ? new Date(local).toISOString() : undefined);
const num = (v: string) => (v === '' ? undefined : Number(v));
const opt = (v: string) => (v === '' ? undefined : v);
const list = (v: unknown) => (Array.isArray(v) ? (v as R[]) : []);
const obj = (v: unknown) => (v && typeof v === 'object' ? (v as R) : ({} as R));

function useOptions(path: string, lbl: (r: R) => string = (r) => `${String(r.name)}${r.code ? ` (${String(r.code)})` : ''}`): Option[] {
  const q = useGet<Page<R>>(path);
  return (q.data?.items ?? []).map((r) => ({ value: r.id, label: lbl(r) }));
}

function Actions({ children }: { children: React.ReactNode }) {
  return <div className="oc-row-wrap">{children}</div>;
}

/** Customer search + select. */
function CustomerPicker({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const [q, setQ] = useState('');
  const found = useGet<Page<R>>(`/api/v1/crm/customers${qs({ q, limit: 20, 'filter[status]': 'active' })}`);
  return (
    <>
      <TextField label="Find customer" value={q} onChange={setQ} placeholder="Name, phone, e-mail or code" />
      <SelectField label="Customer" value={value} onChange={onChange} placeholder="Select"
        options={(found.data?.items ?? []).map((c) => ({ value: c.id, label: `${String(c.name)}${c.code ? ` (${String(c.code)})` : ''}` }))} />
    </>
  );
}

// ── Events, Banquet, MICE, Weddings (FR-EVT-01, FR-EVT-10) ───────────────

const CATEGORY_TITLES: Record<string, [string, string]> = {
  '': ['Events', 'Every event: corporate gatherings, tournaments, weddings, birthdays, seminars, community and social events.'],
  'banquet,social': ['Banquet', 'Banquets and social events with packages, menus and BEO.'],
  mice: ['MICE', 'Meetings, incentives, conferences and exhibitions: multi-day, multi-room, breakout rooms, accommodation.'],
  wedding: ['Weddings', 'Wedding inquiries to final billing: package, menu, food tasting, technical meeting, BEO.'],
};

export function EventsPage() {
  const [params] = useSearchParams();
  const { can } = useAuth();
  const navigate = useNavigate();
  const category = params.get('category') ?? '';
  const [title, help] = CATEGORY_TITLES[category] ?? CATEGORY_TITLES[''];
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [modal, setModal] = useState(false);
  return (
    <>
      <ListPage title={title} help={help} path={`${API}/events`} statuses={STATUSES}
        extraQuery={{ 'filter[category]': category, from, to }}
        filters={<>
          <TextField label="From" type="date" value={from} onChange={setFrom} />
          <TextField label="To" type="date" value={to} onChange={setTo} />
        </>}
        actions={can('banquet.event.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setModal(true)}>New Event</button> : undefined}
        onRowClick={(r) => navigate(`/banquet-event/events/${r.id}`)}
        columns={[{ key: 'number', header: 'Event' },
          { key: 'title', header: 'Title', render: (r) => <>{String(r.title)}<div className="oc-small oc-muted">{String(r.eventTypeName ?? '')}</div></> },
          { key: 'start', header: 'Date', render: (r) => formatDateTime(String(r.start)) },
          { key: 'customerName', header: 'Customer', render: (r) => String(r.corporateName ?? r.customerName ?? r.contactName ?? '—') },
          { key: 'expectedPax', header: 'Pax', align: 'right', render: (r) => String(r.finalPax ?? r.guaranteedPax ?? r.expectedPax) },
          { key: 'salesOwnerName', header: 'Sales', render: (r) => String(r.salesOwnerName ?? '—') },
          { key: 'contractTotal', header: 'Contract', align: 'right', render: (r) => money(r.contractTotal) },
          { key: 'status', header: 'Status', render: pill('status') }]} />
      {modal && <EventForm onClose={() => setModal(false)} onDone={(id) => navigate(`/banquet-event/events/${id}`)} />}
    </>
  );
}

function EventForm({ onClose, onDone }: { onClose: () => void; onDone: (id: string) => void }) {
  const types = useOptions(`${API}/event-types?filter[status]=active&limit=200`);
  const packages = useOptions(`${API}/packages?filter[status]=active&limit=200`);
  const venues = useOptions(`${API}/venues?filter[status]=active&limit=200`);
  const start = new Date(Date.now() + 30 * 86400000);
  start.setHours(10, 0, 0, 0);
  const [f, setF] = useState<Record<string, string>>({ title: '', eventTypeId: '', customerId: '', contactName: '', contactPhone: '', contactEmail: '',
    start: toLocalInput(start), end: toLocalInput(new Date(start.getTime() + 5 * 3600000)), expectedPax: '', layout: '', packageId: '', venueId: '',
    capacity: '', registrationFee: '', description: '', specialRequests: '', powerWatt: '' });
  const [waitlist, setWaitlist] = useState(false);
  const [pub, setPub] = useState(false);
  const [membersOnly, setMembersOnly] = useState(false);
  const set = (k: string) => (v: string) => setF((x) => ({ ...x, [k]: v }));
  const send = useSend<B, R>('POST', `${API}/events`, INV);
  const fe = send.error?.fieldErrors ?? {};
  const body: Record<string, unknown> = {
    title: f.title, eventTypeId: f.eventTypeId, customerId: opt(f.customerId), contactName: opt(f.contactName), contactPhone: opt(f.contactPhone),
    contactEmail: opt(f.contactEmail), start: iso(f.start), end: iso(f.end), expectedPax: num(f.expectedPax) ?? 0, layout: opt(f.layout),
    packageId: opt(f.packageId), powerWatt: num(f.powerWatt), description: opt(f.description), specialRequests: opt(f.specialRequests),
    venues: f.venueId ? [{ venueId: f.venueId, waitlist: waitlist || undefined }] : undefined,
  };
  if (pub) Object.assign(body, { public: true, registrationOpen: true, capacity: num(f.capacity), registrationFee: opt(f.registrationFee), membersOnly });
  return (
    <Modal open onClose={onClose} title="New Event" wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!f.title || !f.eventTypeId || send.isPending}
        onClick={() => send.mutate(body as R, { onSuccess: (e) => onDone(e.id) })}>Create</button>
    </>}>
      <ErrorAlert error={send.error} />
      <div className="oc-form">
        <TextField label="Title" value={f.title} onChange={set('title')} required error={fe.title} />
        <SelectField label="Event type" value={f.eventTypeId} onChange={set('eventTypeId')} options={types} required error={fe.eventTypeId} />
        <CustomerPicker value={f.customerId} onChange={set('customerId')} />
        <TextField label="Contact name" value={f.contactName} onChange={set('contactName')} help="When there is no customer record yet" />
        <TextField label="Contact phone" value={f.contactPhone} onChange={set('contactPhone')} />
        <TextField label="Contact e-mail" type="email" value={f.contactEmail} onChange={set('contactEmail')} />
        <TextField label="Start" type="datetime-local" value={f.start} onChange={set('start')} required />
        <TextField label="End" type="datetime-local" value={f.end} onChange={set('end')} required error={fe.end} />
        <TextField label="Expected pax" type="number" value={f.expectedPax} onChange={set('expectedPax')} error={fe.expectedPax} />
        <SelectField label="Layout" value={f.layout} onChange={set('layout')} options={LAYOUTS} placeholder="—" />
        <SelectField label="Package" value={f.packageId} onChange={set('packageId')} options={packages} placeholder="None" />
        <SelectField label="Venue (tentative hold)" value={f.venueId} onChange={set('venueId')} options={venues} placeholder="None" />
        {f.venueId && <Checkbox label="Waitlist when the venue is held" checked={waitlist} onChange={setWaitlist} />}
        <TextField label="Electricity needed (W)" type="number" value={f.powerWatt} onChange={set('powerWatt')} />
        <TextArea label="Special requests (allergy, VIP)" value={f.specialRequests} onChange={set('specialRequests')} />
        <Checkbox label="Open event: website and Member App registration" checked={pub} onChange={setPub} />
        {pub && <>
          <TextField label="Capacity (seats)" type="number" value={f.capacity} onChange={set('capacity')} />
          <TextField label="Registration fee per seat" type="number" value={f.registrationFee} onChange={set('registrationFee')} />
          <Checkbox label="Members only" checked={membersOnly} onChange={setMembersOnly} />
          <TextArea label="Description (website)" value={f.description} onChange={set('description')} />
        </>}
      </div>
    </Modal>
  );
}

// ── Event detail ──────────────────────────────────────────────────────────

const TABS: Option[] = [{ value: 'overview', label: 'Overview' }, { value: 'menu', label: 'Menu' }, { value: 'schedule', label: 'Run-of-show' },
  { value: 'resources', label: 'Resources & Vendors' }, { value: 'checklist', label: 'Checklist' }, { value: 'guests', label: 'Guests' },
  { value: 'billing', label: 'Event Billing' }];
// Food Cost tab (PRD P4 §9.2): only with the banquet food cost report.
const FOOD_COST_PERM = 'reporting.inventory_banquet_food_cost.view';

export function EventDetailPage() {
  const { can } = useAuth();
  const { id = '' } = useParams();
  const [params, setParams] = useSearchParams();
  const tab = params.get('tab') ?? 'overview';
  const ev = useGet<R>(`${API}/events/${id}`);
  if (ev.isLoading) return <Skeleton rows={8} />;
  if (ev.error || !ev.data) return <ErrorAlert error={ev.error} />;
  const e = ev.data;
  return (
    <div className="oc-stack">
      <PageHeader title={`${String(e.number)} · ${String(e.title)}`} help={`${String(e.eventTypeName)} · ${formatDateTime(String(e.start))} – ${formatDateTime(String(e.end))}`}
        actions={<StatusPill status={String(e.status)} />} />
      <Tabs tabs={can(FOOD_COST_PERM) ? [...TABS, { value: 'food_cost', label: 'Food Cost' }] : TABS} value={tab} onChange={(v) => setParams({ tab: v })} />
      {tab === 'overview' && <Overview e={e} />}
      {tab === 'menu' && <MenuTab e={e} />}
      {tab === 'schedule' && <RundownTab e={e} />}
      {tab === 'resources' && <ResourcesTab e={e} />}
      {tab === 'checklist' && <ChecklistTab e={e} />}
      {tab === 'guests' && <GuestsTab e={e} />}
      {tab === 'billing' && <BillingTab e={e} />}
      {tab === 'food_cost' && can(FOOD_COST_PERM) && <FoodCostTab e={e} />}
    </div>
  );
}

function Overview({ e }: { e: R }) {
  const { can } = useAuth();
  const id = e.id;
  const venues = useOptions(`${API}/venues?filter[status]=active&limit=200`);
  const packages = useOptions(`${API}/packages?filter[status]=active&limit=200`);
  const [hold, setHold] = useState({ venueId: '', functionName: '', layout: '' });
  const [waitlist, setWaitlist] = useState(false);
  const [pkg, setPkg] = useState({ packageId: String(e.packageId ?? ''), pax: '' });
  const [pax, setPax] = useState('');
  const [finalPax, setFinalPax] = useState('');
  const [option, setOption] = useState('');
  const [override, setOverride] = useState(false);
  const [modal, setModal] = useState<'' | 'definite' | 'cancel'>('');
  const holdVenue = useSend<B>('POST', `${API}/events/${id}/venues`, INV);
  const setPackage = useSend<B>('POST', `${API}/events/${id}/package`, INV);
  const guarantee = useSend<B>('POST', `${API}/events/${id}:guarantee-pax`, INV);
  const extend = useSend<B>('POST', `${API}/events/${id}:extend-option`, INV);
  const complete = useSend<B>('POST', `${API}/events/${id}:complete`, INV);
  const open = ['inquiry', 'tentative', 'definite'].includes(String(e.status));
  const b = obj(e.billing);
  const beo = e.beo ? obj(e.beo) : null;
  return (
    <div className="oc-grid" style={{ gridTemplateColumns: 'repeat(auto-fit, minmax(340px, 1fr))' }}>
      <Card title="Event" icon="celebration">
        <KV items={[['Customer', String(e.corporateName ?? e.customerName ?? e.contactName ?? '—')], ['Contact', `${String(e.contactName ?? '')} ${String(e.contactPhone ?? '')}`],
          ['Sales', String(e.salesOwnerName ?? '—')], ['Pax', `${String(e.expectedPax)} expected · ${String(e.guaranteedPax ?? '—')} guaranteed · ${String(e.finalPax ?? '—')} final`],
          ['Final pax cut-off', e.paxDeadline ? formatDate(String(e.paxDeadline)) : '—'], ['Package', String(e.packageName ?? '—')],
          ['Option date', e.optionDate ? formatDateTime(String(e.optionDate)) : '—'], ['Quotation', String(e.quotationNumber ?? '—')],
          ['Source', label(e.source)], ['Special requests', String(e.specialRequests ?? '—')], ['Notes', String(e.notes ?? '—')]]} />
        {open && <Actions>
          {can('banquet.event.confirm') && e.status !== 'definite' && <button className="oc-btn oc-btn-primary oc-btn-sm" onClick={() => setModal('definite')}>Make Definite</button>}
          {can('banquet.event.cancel') && <button className="oc-btn oc-btn-danger oc-btn-sm" onClick={() => setModal('cancel')}>Cancel Event</button>}
        </Actions>}
        {e.status === 'definite' && can('banquet.event.complete') && (
          <form className="oc-row-wrap" onSubmit={(ev) => { ev.preventDefault(); complete.mutate({ finalPax: Number(finalPax) }); }}>
            <TextField label="Final pax (actual)" type="number" value={finalPax} onChange={setFinalPax} />
            <button className="oc-btn oc-btn-ink oc-btn-sm" disabled={!finalPax || complete.isPending}>Complete Event</button>
          </form>
        )}
        <ErrorAlert error={complete.error} />
      </Card>
      <Card title="Venues" icon="meeting_room">
        <DataTable rows={list(e.venues)} columns={[{ key: 'venueName', header: 'Venue', render: (h) => <>{String(h.venueName)}<div className="oc-small oc-muted">{String(h.functionName ?? '')} {label(h.layout)}</div></> },
          { key: 'start', header: 'Period', render: (h) => `${formatDateTime(String(h.start))} – ${formatDateTime(String(h.end))}` },
          { key: 'status', header: 'Hold', render: (h) => <>{<StatusPill status={String(h.status)} />}{h.waitlistRank ? ` #${String(h.waitlistRank)}` : ''}</> }]}
          actions={(h) => (['tentative', 'definite', 'waitlisted'].includes(String(h.status)) && can('banquet.event.hold')
            ? <ActionButton label="Release" path={`${API}/events/${id}/venues/${h.id}:release`} invalidate={INV} reason="optional" /> : null)} />
        {open && can('banquet.event.hold') && (
          <form className="oc-row-wrap" onSubmit={(ev) => { ev.preventDefault(); holdVenue.mutate({ ...hold, layout: opt(hold.layout), functionName: opt(hold.functionName), waitlist: waitlist || undefined }); }}>
            <SelectField label="Venue" value={hold.venueId} onChange={(v) => setHold((x) => ({ ...x, venueId: v }))} options={venues} />
            <TextField label="Function" value={hold.functionName} onChange={(v) => setHold((x) => ({ ...x, functionName: v }))} placeholder="Akad, Reception …" />
            <SelectField label="Layout" value={hold.layout} onChange={(v) => setHold((x) => ({ ...x, layout: v }))} options={LAYOUTS} placeholder="Event layout" />
            <Checkbox label="Waitlist" checked={waitlist} onChange={setWaitlist} />
            <button className="oc-btn oc-btn-neutral oc-btn-sm" disabled={!hold.venueId || holdVenue.isPending}>Hold venue</button>
          </form>
        )}
        <ErrorAlert error={holdVenue.error} />
        {e.status === 'tentative' && can('banquet.event.hold') && (
          <form className="oc-row-wrap" onSubmit={(ev) => { ev.preventDefault(); extend.mutate({ optionDate: iso(option) }); }}>
            <TextField label="New option date" type="datetime-local" value={option} onChange={setOption} />
            <button className="oc-btn oc-btn-neutral oc-btn-sm" disabled={!option}>Extend option</button>
          </form>
        )}
        <ErrorAlert error={extend.error} />
      </Card>
      <Card title="Package & Pax" icon="groups">
        {open && can('banquet.event.update') && <>
          <form className="oc-row-wrap" onSubmit={(ev) => { ev.preventDefault(); setPackage.mutate({ packageId: pkg.packageId, pax: num(pkg.pax) }); }}>
            <SelectField label="Package" value={pkg.packageId} onChange={(v) => setPkg((x) => ({ ...x, packageId: v }))} options={packages} />
            <TextField label="Pax" type="number" value={pkg.pax} onChange={(v) => setPkg((x) => ({ ...x, pax: v }))} />
            <button className="oc-btn oc-btn-neutral oc-btn-sm" disabled={!pkg.packageId}>Price</button>
          </form>
          <ErrorAlert error={setPackage.error} />
          <form className="oc-row-wrap" onSubmit={(ev) => { ev.preventDefault(); guarantee.mutate({ pax: Number(pax) }); }}>
            <TextField label="Guaranteed pax" type="number" value={pax} onChange={setPax} help="Locked at the cut-off; later increases are charged" />
            <button className="oc-btn oc-btn-neutral oc-btn-sm" disabled={!pax}>Guarantee</button>
          </form>
          <ErrorAlert error={guarantee.error} />
        </>}
        <h4>Package inclusions</h4>
        <DataTable rows={list(e.inclusions)} empty="No inclusions" columns={[{ key: 'label', header: 'Inclusion' }, { key: 'quantity', header: 'Qty', align: 'right' },
          { key: 'status', header: 'Status', render: (i) => <>{<StatusPill status={String(i.status)} />}<div className="oc-small oc-muted">{list(i.voucherCodes).join(', ')} {String(i.note ?? '')}</div></> }]} />
      </Card>
      <Card title="Money & BEO" icon="payments">
        <KV items={[['Contract', money(e.contractTotal)], ['Received', money(b.received)], ['Balance', money(b.balance)],
          ['DP required', `${money(b.downPaymentRequired)} ${b.downPaymentReceived ? '(received)' : ''}`],
          ['BEO', beo ? <Link to={`/banquet-event/beo?id=${String(beo.id)}`}>{`${String(beo.number)} v${String(beo.version)} (${String(beo.status)})`}</Link> : '—'],
          ['Departments to confirm', beo ? list(beo.pendingDepartments).map(label).join(', ') || 'none' : '—'],
          ['Checklist', `${String(obj(e.checklist).done)}/${String(obj(e.checklist).total)} done · ${String(obj(e.checklist).overdue)} overdue`],
          ['Guests', `${String(obj(e.participants).seats)} seats · ${String(obj(e.participants).waitlisted)} waitlisted · ${String(obj(e.participants).checkedIn)} checked in`]]} />
      </Card>
      <Card title="Food Tasting & Technical Meeting" icon="restaurant">
        <Meetings e={e} />
      </Card>
      {list(e.incidents).length > 0 && <Card title="Incidents" icon="report">
        <DataTable rows={list(e.incidents)} columns={[{ key: 'createdAt', header: 'Time', render: (i) => formatDateTime(String(i.createdAt)) },
          { key: 'severity', header: 'Severity', render: pill('severity') }, { key: 'note', header: 'Note' }, { key: 'reporter', header: 'By' }]} />
      </Card>}
      {modal === 'definite' && (
        <Modal open onClose={() => setModal('')} title="Make Definite" actions={<>
          <button className="oc-btn oc-btn-neutral" onClick={() => setModal('')}>Close</button>
          <ActionButton label="Make Definite" kind="primary" path={`${API}/events/${id}:make-definite`} invalidate={INV} body={{ override: override || undefined }}
            reason={override ? 'required' : 'optional'} onDone={() => setModal('')} />
        </>}>
          <p>The event becomes Definite when the down payment is received. Before that, request the <strong>Definite without Deposit</strong> approval.</p>
          <Checkbox label="Override: Definite before the down payment (approval)" checked={override} onChange={setOverride} />
        </Modal>
      )}
      {modal === 'cancel' && <CancelEvent id={id} onClose={() => setModal('')} />}
    </div>
  );
}

function CancelEvent({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const [reason, setReason] = useState('');
  const [waive, setWaive] = useState(false);
  const send = useSend<B, R>('POST', `${API}/events/${id}:cancel`, INV);
  return (
    <Modal open onClose={onClose} title="Cancel Event" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button>
      <button className="oc-btn oc-btn-danger" disabled={!reason || send.isPending} onClick={() => send.mutate({ reason, waiveFee: waive || undefined })}>Cancel Event</button>
    </>}>
      <p>Banquet Policies decide the forfeited share of the down payment; the rest is refunded, venues and resources are released.</p>
      <TextArea label="Reason" value={reason} onChange={setReason} required />
      {can('banquet.billing.manage') && <Checkbox label="Waive the cancellation fee" checked={waive} onChange={setWaive} />}
      <ErrorAlert error={send.error} />
      {send.data && <KV items={[['Fee kept', money(send.data.fee)], ['Refunded', money(send.data.refunded)],
        ['Tier', `${String(obj(send.data.tier).minDaysBefore ?? '—')} days+ · ${String(obj(send.data.tier).forfeitPercent ?? '0')}% of ${label(obj(send.data.tier).basis)}`]]} />}
    </Modal>
  );
}

function Meetings({ e }: { e: R }) {
  const [f, setF] = useState({ kind: 'food_tasting', scheduledAt: '', location: '' });
  const [rec, setRec] = useState<{ id: string; outcome: string; changes: string } | null>(null);
  const add = useSend<B>('POST', `${API}/events/${e.id}/meetings`, INV);
  const record = useSend<B>('POST', (b) => `${API}/event-meetings/${String((b as R).id)}:record`, INV);
  return (
    <>
      <DataTable rows={list(e.meetings)} empty="None scheduled" columns={[{ key: 'kind', header: 'Meeting', render: (m) => label(m.kind) },
        { key: 'scheduledAt', header: 'When', render: (m) => formatDateTime(String(m.scheduledAt)) }, { key: 'status', header: 'Status', render: pill('status') },
        { key: 'changes', header: 'Changes for the BEO', render: (m) => list(m.changes).join('; ') || String(m.outcome ?? '') }]}
        actions={(m) => (m.status === 'scheduled' ? <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setRec({ id: m.id, outcome: '', changes: '' })}>Record</button> : null)} />
      <form className="oc-row-wrap" onSubmit={(ev) => { ev.preventDefault(); add.mutate({ kind: f.kind, scheduledAt: iso(f.scheduledAt), location: opt(f.location) }); }}>
        <SelectField label="Kind" value={f.kind} onChange={(v) => setF((x) => ({ ...x, kind: v }))} options={opts(['food_tasting', 'technical_meeting', 'site_visit'])} />
        <TextField label="When" type="datetime-local" value={f.scheduledAt} onChange={(v) => setF((x) => ({ ...x, scheduledAt: v }))} />
        <TextField label="Location" value={f.location} onChange={(v) => setF((x) => ({ ...x, location: v }))} />
        <button className="oc-btn oc-btn-neutral oc-btn-sm" disabled={!f.scheduledAt}>Schedule</button>
      </form>
      <ErrorAlert error={add.error} />
      {rec && (
        <Modal open onClose={() => setRec(null)} title="Meeting outcome" actions={<>
          <button className="oc-btn oc-btn-neutral" onClick={() => setRec(null)}>Close</button>
          <button className="oc-btn oc-btn-primary" onClick={() => record.mutate({ id: rec.id, outcome: rec.outcome,
            changes: rec.changes.split('\n').map((s) => s.trim()).filter(Boolean) } as unknown as R, { onSuccess: () => setRec(null) })}>Save</button>
        </>}>
          <TextArea label="Outcome" value={rec.outcome} onChange={(v) => setRec({ ...rec, outcome: v })} />
          <TextArea label="Changes for the BEO (one per line)" value={rec.changes} onChange={(v) => setRec({ ...rec, changes: v })} />
          <ErrorAlert error={record.error} />
        </Modal>
      )}
    </>
  );
}

// Menu Selection with category quotas (FR-BQT-02/03).
function MenuTab({ e }: { e: R }) {
  const menus = useOptions(`${API}/menus?filter[status]=active&limit=200`);
  const [menuId, setMenuId] = useState('');
  const cats = useGet<Page<R>>(menuId ? `${API}/menu-categories?filter[menuId]=${menuId}&limit=200` : null);
  const items = useGet<Page<R>>(menuId ? `${API}/menu-items?filter[menuId]=${menuId}&filter[status]=active&limit=500` : null);
  const current = list(e.menus).find((m) => m.menuId === menuId);
  const [picked, setPicked] = useState<Record<string, boolean>>({});
  const send = useSend<B>('PUT', `${API}/events/${e.id}/menu-selection`, INV);
  const choose = (v: string) => {
    setMenuId(v);
    const cur = list(e.menus).find((m) => m.menuId === v);
    setPicked(Object.fromEntries(list(cur?.items).map((i) => [String(i.menuItemId), true])));
  };
  return (
    <div className="oc-stack">
      {list(e.menus).map((m) => (
        <Card key={String(m.menuId)} title={String(m.menuName)} icon="restaurant_menu">
          <div className="oc-row-wrap">{list(m.categories).map((c) => (
            <span key={String(c.categoryId)} className="oc-chip">{String(c.name)}: {String(c.selected)}/{String(c.quota)}{Number(c.extra) > 0 ? ` (+${String(c.extra)} extra)` : ''}</span>))}</div>
          <ul>{list(m.items).map((i) => <li key={String(i.menuItemId)}>{String(i.category ?? '')} · {String(i.name)} {list(i.allergens).length ? `(${list(i.allergens).join(', ')})` : ''}</li>)}</ul>
        </Card>
      ))}
      <Card title="Select dishes" icon="checklist">
        <SelectField label="Menu" value={menuId} onChange={choose} options={menus} placeholder="Select a menu" />
        {menuId && list(cats.data?.items).map((c) => {
          const n = (items.data?.items ?? []).filter((i) => i.categoryId === c.id && picked[i.id]).length;
          return (
            <fieldset key={c.id} style={{ border: 0, padding: 0, margin: '8px 0' }}>
              <legend><strong>{String(c.name)}</strong> — {n}/{String(c.quota)} {c.extraChoicePrice ? `(extra choice ${money(c.extraChoicePrice)}/pax)` : '(no extras)'}</legend>
              <div className="oc-row-wrap">{(items.data?.items ?? []).filter((i) => i.categoryId === c.id).map((i) => (
                <Checkbox key={i.id} label={String(i.name)} checked={!!picked[i.id]} onChange={(v) => setPicked((x) => ({ ...x, [i.id]: v }))} />))}</div>
            </fieldset>
          );
        })}
        {menuId && (items.data?.items ?? []).filter((i) => !i.categoryId).map((i) => (
          <Checkbox key={i.id} label={String(i.name)} checked={!!picked[i.id]} onChange={(v) => setPicked((x) => ({ ...x, [i.id]: v }))} />))}
        <ErrorAlert error={send.error} />
        {menuId && <button className="oc-btn oc-btn-primary" disabled={send.isPending || !Object.values(picked).some(Boolean)}
          onClick={() => send.mutate({ menuId, itemIds: Object.keys(picked).filter((k) => picked[k]) })}>{current ? 'Update selection' : 'Save selection'}</button>}
      </Card>
    </div>
  );
}

// Run-of-show (FR-EVT-02).
function RundownTab({ e }: { e: R }) {
  const venues = useOptions(`${API}/venues?filter[status]=active&limit=200`);
  const [rows, setRows] = useState<Record<string, string>[]>(() => list(e.schedule).map((s) => ({ start: toLocalInput(new Date(String(s.start))),
    end: s.end ? toLocalInput(new Date(String(s.end))) : '', title: String(s.title), venueId: String(s.venueId ?? ''), ownerName: String(s.ownerName ?? ''),
    department: String(s.department ?? '') })));
  const send = useSend<B>('PUT', `${API}/events/${e.id}/schedule`, INV);
  const upd = (i: number, k: string) => (v: string) => setRows((x) => x.map((r, j) => (j === i ? { ...r, [k]: v } : r)));
  return (
    <Card title="Run-of-show" icon="schedule">
      {rows.map((r, i) => (
        <div key={i} className="oc-row-wrap">
          <TextField label="Start" type="datetime-local" value={r.start} onChange={upd(i, 'start')} />
          <TextField label="End" type="datetime-local" value={r.end} onChange={upd(i, 'end')} />
          <TextField label="Session" value={r.title} onChange={upd(i, 'title')} />
          <SelectField label="Venue" value={r.venueId} onChange={upd(i, 'venueId')} options={venues} placeholder="—" />
          <TextField label="Person in charge" value={r.ownerName} onChange={upd(i, 'ownerName')} />
          <SelectField label="Department" value={r.department} onChange={upd(i, 'department')} options={DEPARTMENTS} placeholder="—" />
          <button className="oc-btn oc-btn-text oc-btn-sm" aria-label="Remove session" onClick={() => setRows((x) => x.filter((_, j) => j !== i))}>Remove</button>
        </div>
      ))}
      <ErrorAlert error={send.error} />
      <Actions>
        <button className="oc-btn oc-btn-neutral" onClick={() => setRows((x) => [...x, { start: toLocalInput(new Date(String(e.start))), end: '', title: '', venueId: '', ownerName: '', department: '' }])}>Add session</button>
        <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => send.mutate({ items: rows.map((r) => ({ start: iso(r.start), end: iso(r.end),
          title: r.title, venueId: opt(r.venueId), ownerName: opt(r.ownerName), department: opt(r.department) })) })}>Save run-of-show</button>
      </Actions>
    </Card>
  );
}

function ResourcesTab({ e }: { e: R }) {
  const vendors = useOptions(`${API}/vendors?filter[status]=active&limit=200`);
  const resources = useOptions('/api/v1/reservation/resources?filter[status]=active&limit=500', (r) => `${String(r.name)} (${label(r.resourceType)})`);
  const [res, setRes] = useState({ resourceId: '', description: '', amount: '' });
  const [vd, setVd] = useState({ vendorId: '', service: '', fee: '' });
  const [charge, setCharge] = useState(false);
  const addRes = useSend<B>('POST', `${API}/events/${e.id}/resources`, INV);
  const addVendor = useSend<B>('POST', `${API}/events/${e.id}/vendors`, INV);
  return (
    <div className="oc-grid" style={{ gridTemplateColumns: 'repeat(auto-fit, minmax(380px, 1fr))' }}>
      <Card title="Resources (bungalow, meeting room, equipment)" icon="inventory">
        <DataTable rows={list(e.resources)} columns={[{ key: 'resourceName', header: 'Resource', render: (r) => <>{String(r.description ?? r.resourceName)}<div className="oc-small oc-muted">{label(r.resourceType)}{r.inclusionId ? ' · package inclusion' : ''}</div></> },
          { key: 'start', header: 'Period', render: (r) => `${formatDateTime(String(r.start))} – ${formatDateTime(String(r.end))}` }, { key: 'status', header: 'Status', render: pill('status') }]}
          actions={(r) => (['held', 'confirmed'].includes(String(r.status)) ? <ActionButton label="Release" path={`${API}/event-resources/${r.id}:release`} invalidate={INV} reason="optional" /> : null)} />
        <form className="oc-row-wrap" onSubmit={(ev) => { ev.preventDefault(); addRes.mutate({ resourceId: res.resourceId, description: opt(res.description), amount: opt(res.amount) }); }}>
          <SelectField label="Resource" value={res.resourceId} onChange={(v) => setRes((x) => ({ ...x, resourceId: v }))} options={resources} />
          <TextField label="Description" value={res.description} onChange={(v) => setRes((x) => ({ ...x, description: v }))} />
          <TextField label="Charge (not in the package)" type="number" value={res.amount} onChange={(v) => setRes((x) => ({ ...x, amount: v }))} />
          <button className="oc-btn oc-btn-neutral oc-btn-sm" disabled={!res.resourceId}>Book</button>
        </form>
        <ErrorAlert error={addRes.error} />
      </Card>
      <Card title="Vendors (decoration, MC, band, photographer)" icon="storefront">
        <DataTable rows={list(e.vendors)} columns={[{ key: 'vendorName', header: 'Vendor' }, { key: 'service', header: 'Service' }, { key: 'fee', header: 'Fee', align: 'right', render: (v) => money(v.fee) },
          { key: 'status', header: 'Status', render: pill('status') }]}
          actions={(v) => (v.status === 'confirmed' ? <ActionButton label="Cancel" path={`${API}/event-vendors/${v.id}:cancel`} invalidate={INV} reason="optional" /> : null)} />
        <form className="oc-row-wrap" onSubmit={(ev) => { ev.preventDefault(); addVendor.mutate({ vendorId: vd.vendorId, service: vd.service, fee: opt(vd.fee), chargeToCustomer: charge || undefined }); }}>
          <SelectField label="Vendor" value={vd.vendorId} onChange={(v) => setVd((x) => ({ ...x, vendorId: v }))} options={vendors} />
          <TextField label="Service" value={vd.service} onChange={(v) => setVd((x) => ({ ...x, service: v }))} />
          <TextField label="Fee" type="number" value={vd.fee} onChange={(v) => setVd((x) => ({ ...x, fee: v }))} />
          <Checkbox label="Charge the customer" checked={charge} onChange={setCharge} />
          <button className="oc-btn oc-btn-neutral oc-btn-sm" disabled={!vd.vendorId || !vd.service}>Book vendor</button>
        </form>
        <ErrorAlert error={addVendor.error} />
      </Card>
    </div>
  );
}

function ChecklistTab({ e }: { e: R }) {
  const tasks = useGet<Page<R>>(`${API}/events/${e.id}/checklist`);
  const templates = useOptions(`${API}/checklist-templates?filter[status]=active&limit=200`);
  const [tpl, setTpl] = useState('');
  const [f, setF] = useState({ task: '', department: 'banquet', ownerName: '', dueDate: '' });
  const add = useSend<B>('POST', `${API}/events/${e.id}/checklist`, INV);
  const apply = useSend<B>('POST', `${API}/events/${e.id}/checklist:apply-template`, INV);
  const toggle = useSend<B>('POST', (b) => `${API}/checklist-items/${String((b as R).id)}:toggle`, INV);
  return (
    <Card title="Event Checklist" icon="checklist">
      <DataTable rows={tasks.data?.items} loading={tasks.isLoading} columns={[
        { key: 'status', header: 'Done', render: (c) => <Checkbox label={String(c.task)} checked={c.status === 'done'} onChange={(v) => toggle.mutate({ id: c.id, done: v } as B)} /> },
        { key: 'department', header: 'Department', render: (c) => label(c.department) }, { key: 'ownerName', header: 'PIC', render: (c) => String(c.ownerName ?? '—') },
        { key: 'dueDate', header: 'Due', render: (c) => <>{c.dueDate ? formatDate(String(c.dueDate)) : '—'}{c.overdue ? <> <StatusPill status="overdue" /></> : null}</> }]} />
      <form className="oc-row-wrap" onSubmit={(ev) => { ev.preventDefault(); add.mutate({ ...f, ownerName: opt(f.ownerName), dueDate: opt(f.dueDate) }); }}>
        <TextField label="Task" value={f.task} onChange={(v) => setF((x) => ({ ...x, task: v }))} />
        <SelectField label="Department" value={f.department} onChange={(v) => setF((x) => ({ ...x, department: v }))} options={DEPARTMENTS} />
        <TextField label="PIC" value={f.ownerName} onChange={(v) => setF((x) => ({ ...x, ownerName: v }))} />
        <TextField label="Due" type="date" value={f.dueDate} onChange={(v) => setF((x) => ({ ...x, dueDate: v }))} />
        <button className="oc-btn oc-btn-neutral oc-btn-sm" disabled={!f.task}>Add task</button>
      </form>
      <form className="oc-row-wrap" onSubmit={(ev) => { ev.preventDefault(); apply.mutate({ templateId: tpl }); }}>
        <SelectField label="Template" value={tpl} onChange={setTpl} options={templates} />
        <button className="oc-btn oc-btn-neutral oc-btn-sm" disabled={!tpl}>Apply template</button>
      </form>
      <ErrorAlert error={add.error ?? apply.error ?? toggle.error} />
    </Card>
  );
}

/** Splits a CSV / tab separated text into objects by the header row. */
function parseTable(text: string): Record<string, string>[] {
  const lines = text.split(/\r?\n/).filter((l) => l.trim() !== '');
  if (lines.length < 2) return [];
  const sep = lines[0].includes('\t') ? '\t' : ',';
  const cells = (l: string) => {
    const out: string[] = [];
    let cur = '';
    let q = false;
    for (let i = 0; i < l.length; i++) {
      const ch = l[i];
      if (ch === '"') {
        if (q && l[i + 1] === '"') { cur += '"'; i++; } else q = !q;
      } else if (ch === sep && !q) { out.push(cur); cur = ''; } else cur += ch;
    }
    out.push(cur);
    return out.map((s) => s.trim());
  };
  const head = cells(lines[0]);
  return lines.slice(1).map((l) => Object.fromEntries(cells(l).map((v, i) => [head[i], v])));
}

function GuestsTab({ e }: { e: R }) {
  const { can } = useAuth();
  const [q, setQ] = useState('');
  const guests = useGet<Page<R>>(`${API}/events/${e.id}/participants${qs({ q })}`);
  const [f, setF] = useState({ name: '', phone: '', email: '', company: '', partySize: '1', tableNo: '' });
  const [vip, setVip] = useState(false);
  const [csv, setCsv] = useState('');
  const add = useSend<B>('POST', `${API}/events/${e.id}/participants`, INV);
  const imp = useSend<B, R>('POST', `${API}/events/${e.id}/participants:import`, INV);
  return (
    <div className="oc-stack">
      <Card title="Guest Registration" icon="how_to_reg">
        <TextField label="Search" value={q} onChange={setQ} placeholder="Name, company or ticket code" />
        <DataTable rows={guests.data?.items} loading={guests.isLoading} columns={[{ key: 'name', header: 'Guest', render: (p) => <>{String(p.name)}{p.vip ? ' ★' : ''}<div className="oc-small oc-muted">{String(p.company ?? '')}</div></> },
          { key: 'partySize', header: 'Seats', align: 'right' }, { key: 'ticketCode', header: 'Ticket' }, { key: 'tableNo', header: 'Table' },
          { key: 'paymentStatus', header: 'Payment', render: pill('paymentStatus') },
          { key: 'status', header: 'Status', render: (p) => <>{<StatusPill status={String(p.status)} />}{p.waitlistRank ? ` #${String(p.waitlistRank)}` : ''}</> }]}
          actions={(p) => <Actions>
            {p.status === 'registered' && can('banquet.participant.check_in') && <ActionButton label="Check in" kind="ink" path={`${API}/participants/${p.id}:check-in`} invalidate={INV} />}
            {['registered', 'waitlisted'].includes(String(p.status)) && can('banquet.participant.manage') && <ActionButton label="Withdraw" path={`${API}/participants/${p.id}:withdraw`} invalidate={INV} reason="optional" />}
          </Actions>} />
      </Card>
      {can('banquet.participant.manage') && <div className="oc-grid" style={{ gridTemplateColumns: 'repeat(auto-fit, minmax(340px, 1fr))' }}>
        <Card title="Register a guest" icon="person_add">
          <div className="oc-form">
            <TextField label="Name" value={f.name} onChange={(v) => setF((x) => ({ ...x, name: v }))} required />
            <TextField label="Phone" value={f.phone} onChange={(v) => setF((x) => ({ ...x, phone: v }))} />
            <TextField label="E-mail" type="email" value={f.email} onChange={(v) => setF((x) => ({ ...x, email: v }))} />
            <TextField label="Company" value={f.company} onChange={(v) => setF((x) => ({ ...x, company: v }))} />
            <TextField label="Seats" type="number" value={f.partySize} onChange={(v) => setF((x) => ({ ...x, partySize: v }))} />
            <TextField label="Table" value={f.tableNo} onChange={(v) => setF((x) => ({ ...x, tableNo: v }))} />
            <Checkbox label="VIP" checked={vip} onChange={setVip} />
          </div>
          <ErrorAlert error={add.error} />
          <button className="oc-btn oc-btn-primary" disabled={!f.name || add.isPending} onClick={() => add.mutate({ name: f.name, phone: opt(f.phone), email: opt(f.email),
            company: opt(f.company), partySize: num(f.partySize), tableNo: opt(f.tableNo), vip: vip || undefined }, { onSuccess: () => setF({ name: '', phone: '', email: '', company: '', partySize: '1', tableNo: '' }) })}>Register</button>
        </Card>
        <Card title="Import guest list" icon="upload">
          <TextArea label="CSV (name, email, phone, company, partySize, tableNo)" value={csv} onChange={setCsv} rows={6} />
          <input type="file" accept=".csv,.txt" aria-label="Guest list file" onChange={(ev) => { const file = ev.target.files?.[0]; if (file) void file.text().then(setCsv); }} />
          <ErrorAlert error={imp.error} />
          <button className="oc-btn oc-btn-neutral" disabled={!csv || imp.isPending} onClick={() => imp.mutate({ participants: parseTable(csv).map((r) => ({ name: r.name ?? '',
            email: opt(r.email ?? ''), phone: opt(r.phone ?? ''), company: opt(r.company ?? ''), partySize: num(r.partySize ?? ''), tableNo: opt(r.tableNo ?? '') })) })}>Import</button>
          {imp.data && <p className="oc-small">{String(imp.data.registered)} registered · {String(imp.data.waitlisted)} waitlisted · {list(imp.data.skipped).length} skipped
            {list(imp.data.skipped).length > 0 && <>: {(imp.data.skipped as string[]).join('; ')}</>}</p>}
        </Card>
      </div>}
    </div>
  );
}

// Food cost actual vs theoretical of the event (PRD P4 §9.2, report
// inventory.banquet_food_cost): theoretical = issued BEO for the final pax at
// standard cost; actual = stock deducted on completion at valuation cost
// (= banquet cost of sales journal).
function FoodCostTab({ e }: { e: R }) {
  const day = (d: number) => {
    const t = new Date(String(e.start));
    t.setDate(t.getDate() + d);
    return t.toISOString().slice(0, 10);
  };
  const q = `?params[from]=${day(-1)}&params[to]=${day(1)}&params[event]=${encodeURIComponent(String(e.number))}`;
  const sum = useGet<{ rows: R[] }>(`/api/v1/reporting/reports/inventory.banquet_food_cost${q}`);
  const lines = useGet<{ rows: R[] }>(`/api/v1/reporting/reports/inventory.banquet_food_cost_lines${q}`);
  if (sum.error) return <ErrorAlert error={sum.error} />;
  if (sum.isLoading || lines.isLoading) return <Skeleton />;
  const s = sum.data?.rows?.[0];
  const pct = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : `${String(v)} %`);
  return (
    <div className="oc-stack">
      <Card title="Food cost" icon="restaurant">
        {s ? <KV items={[['Pax', String(s.pax ?? '—')], ['F&B revenue', money(s.fnbRevenue)], ['Theoretical cost (BEO × standard cost)', money(s.theoreticalCost)],
          ['Actual cost (stock deducted)', money(s.actualCost)], ['Variance', money(s.variance)], ['Theoretical food cost %', pct(s.theoreticalPercent)],
          ['Actual food cost %', pct(s.actualPercent)], ['Actual cost / pax', money(s.actualPerPax)]]} />
          : <Empty title="No food cost yet" icon="restaurant" help="The theoretical cost appears when the BEO is issued; the actual cost when the event is completed and the stock is deducted." />}
      </Card>
      <Card title="Ingredients" icon="kitchen">
        <DataTable rows={lines.data?.rows ?? []} empty="No ingredients" columns={[{ key: 'item', header: 'Ingredient', render: (l) => <>{String(l.item)}<div className="oc-small oc-muted">{String(l.itemCode)}</div></> },
          { key: 'theoreticalQuantity', header: 'Theoretical', align: 'right', render: (l) => `${String(l.theoreticalQuantity)} ${String(l.uom)}` },
          { key: 'actualQuantity', header: 'Actual', align: 'right', render: (l) => `${String(l.actualQuantity)} ${String(l.uom)}` },
          { key: 'theoreticalCost', header: 'Theoretical cost', align: 'right', render: (l) => money(l.theoreticalCost) },
          { key: 'actualCost', header: 'Actual cost', align: 'right', render: (l) => money(l.actualCost) },
          { key: 'variance', header: 'Variance', align: 'right', render: (l) => money(l.variance) }]} />
      </Card>
    </div>
  );
}

// Event Billing (FR-EVT-08, FR-BQT-08/12).
function BillingTab({ e }: { e: R }) {
  const { can } = useAuth();
  const bill = useGet<R>(`${API}/events/${e.id}/billing`);
  const types = useOptions(`${API}/charge-types?filter[status]=active&limit=200`);
  const [c, setC] = useState({ chargeTypeId: '', kind: '', quantity: '', unitPrice: '', description: '' });
  const [method, setMethod] = useState('cash');
  const [invoice, setInvoice] = useState(false);
  const add = useSend<B>('POST', `${API}/events/${e.id}/charges`, INV);
  const schedule = useSend<B>('POST', `${API}/events/${e.id}/payment-schedule`, INV);
  const final = useSend<B, R>('POST', `${API}/events/${e.id}:final-billing`, INV);
  if (!bill.data) return <Skeleton />;
  const b = bill.data;
  const sc = b.schedule ? obj(b.schedule) : null;
  const manage = can('banquet.billing.manage');
  return (
    <div className="oc-stack">
      <div className="oc-grid" style={{ gridTemplateColumns: 'repeat(auto-fit, minmax(300px, 1fr))' }}>
        <Card title="Summary" icon="account_balance_wallet">
          <KV items={[['Contract', money(b.contractTotal)], ['Received (payments & deposits)', money(b.received)], ['Balance', money(b.balance)],
            ['Down payment', `${money(b.downPaymentRequired)} ${b.downPaymentReceived ? '· received' : ''}`], ['Folio', String(obj(b.folio).number ?? b.folioStatus ?? '—')],
            ['Final invoice', b.finalInvoice ? String(obj(b.finalInvoice).number) : '—'], ['Settled', b.settledAt ? formatDateTime(String(b.settledAt)) : '—']]} />
        </Card>
        <Card title="Revenue by component" icon="pie_chart">
          <DataTable rows={list(b.byComponent)} columns={[{ key: 'revenueComponent', header: 'Component', render: (x) => label(x.revenueComponent) },
            { key: 'net', header: 'Net', align: 'right', render: (x) => money(x.net) }, { key: 'total', header: 'Total', align: 'right', render: (x) => money(x.total) }]} />
        </Card>
        <Card title="Payment Schedule" icon="event">
          {sc ? <DataTable rows={list(sc.lines)} columns={[{ key: 'label', header: 'Term' }, { key: 'dueDate', header: 'Due', render: (l) => formatDate(String(l.dueDate)) },
            { key: 'amount', header: 'Amount', align: 'right', render: (l) => money(l.amount) }, { key: 'paidAmount', header: 'Paid', align: 'right', render: (l) => money(l.paidAmount) },
            { key: 'status', header: 'Status', render: pill('status') }]} />
            : manage && <button className="oc-btn oc-btn-primary" disabled={schedule.isPending} onClick={() => schedule.mutate({})}>Build schedule (Banquet Policies)</button>}
          {sc && <Link to="/billing/payment-schedules">Take payments in Billing → Payment Schedule</Link>}
          <ErrorAlert error={schedule.error} />
        </Card>
      </div>
      <Card title="Charges" icon="receipt_long">
        <DataTable rows={list(b.charges)} columns={[{ key: 'description', header: 'Charge', render: (x) => <>{String(x.description)}<div className="oc-small oc-muted">{label(x.source)} · {label(x.revenueComponent)}</div></> },
          { key: 'quantity', header: 'Qty', align: 'right' }, { key: 'net', header: 'Net', align: 'right', render: (x) => money(x.net) },
          { key: 'total', header: 'Total', align: 'right', render: (x) => money(x.total) }, { key: 'status', header: 'Status', render: pill('status') }]}
          actions={(x) => (x.status === 'posted' && manage ? <ActionButton label="Void" path={`${API}/event-charges/${x.id}:void`} invalidate={INV} reason="required" danger /> : null)} />
        {manage && e.status !== 'cancelled' && (
          <form className="oc-row-wrap" onSubmit={(ev) => { ev.preventDefault(); add.mutate({ chargeTypeId: opt(c.chargeTypeId), kind: opt(c.kind), quantity: opt(c.quantity),
            unitPrice: opt(c.unitPrice), description: opt(c.description) }); }}>
            <SelectField label="Extra charge" value={c.chargeTypeId} onChange={(v) => setC((x) => ({ ...x, chargeTypeId: v }))} options={types} placeholder="—" />
            <SelectField label="or kind" value={c.kind} onChange={(v) => setC((x) => ({ ...x, kind: v }))} options={CHARGE_KINDS} placeholder="—" />
            <TextField label="Quantity" type="number" value={c.quantity} onChange={(v) => setC((x) => ({ ...x, quantity: v }))} />
            <TextField label="Unit price" type="number" value={c.unitPrice} onChange={(v) => setC((x) => ({ ...x, unitPrice: v }))} />
            <TextField label="Description" value={c.description} onChange={(v) => setC((x) => ({ ...x, description: v }))} />
            <button className="oc-btn oc-btn-neutral oc-btn-sm" disabled={!c.chargeTypeId && !c.kind}>Post charge</button>
          </form>
        )}
        <ErrorAlert error={add.error} />
      </Card>
      {e.status === 'completed' && manage && !b.finalBilledAt && (
        <Card title="Final Billing" icon="request_quote">
          <p>Deposits (DP and terms) are applied to the event folio; an overpayment is refunded; the balance is paid now or invoiced.</p>
          <div className="oc-row-wrap">
            <Checkbox label="Issue the final invoice" checked={invoice} onChange={setInvoice} />
            {!invoice && <SelectField label="Payment method" value={method} onChange={setMethod} options={opts(['cash', 'bank_transfer', 'card', 'qris'])} />}
            <button className="oc-btn oc-btn-primary" disabled={final.isPending} onClick={() => final.mutate(invoice ? { invoice: true } : { payment: { methodType: method } })}>Final billing</button>
          </div>
          <ErrorAlert error={final.error} />
          {final.data && <KV items={[['Charges', money(final.data.charges)], ['Deposits applied', money(final.data.depositsApplied)], ['Refunded', money(final.data.refunded)],
            ['Due', money(final.data.due)], ['Folio closed', final.data.folioClosed ? 'yes' : 'no']]} />}
        </Card>
      )}
    </div>
  );
}

// ── BEO (EP-14) ───────────────────────────────────────────────────────────

export function BEOPage() {
  const [params, setParams] = useSearchParams();
  const [status, setStatus] = useState('');
  const [newFor, setNewFor] = useState('');
  const events = useOptions(`${API}/events?filter[status]=tentative,definite&limit=200`, (r) => `${String(r.number)} · ${String(r.title)}`);
  const { can } = useAuth();
  const create = useSend<B, R>('POST', `${API}/beos`, INV);
  const open = params.get('id');
  return (
    <>
      <ListPage title="Banquet Event Order" help="Function sheets per event: issue, revise with marked changes, departments confirm each version."
        path={`${API}/beos`} search extraQuery={{ 'filter[status]': status }}
        filters={<SelectField label="Status" value={status} onChange={setStatus} options={opts(['draft', 'issued', 'superseded'])} placeholder="Current" />}
        actions={can('banquet.beo.manage') ? <div className="oc-row-wrap">
          <SelectField label="Event" value={newFor} onChange={setNewFor} options={events} placeholder="Event" />
          <button className="oc-btn oc-btn-primary" disabled={!newFor || create.isPending} onClick={() => create.mutate({ eventId: newFor }, { onSuccess: (b) => setParams({ id: b.id }) })}>New BEO</button>
        </div> : undefined}
        onRowClick={(r) => setParams({ id: r.id })}
        columns={[{ key: 'number', header: 'BEO', render: (r) => `${String(r.number)} v${String(r.version)}` }, { key: 'eventNumber', header: 'Event', render: (r) => `${String(r.eventNumber)} · ${String(r.eventTitle)}` },
          { key: 'eventDate', header: 'Date', render: (r) => formatDate(String(r.eventDate)) }, { key: 'pax', header: 'Pax', align: 'right' },
          { key: 'departments', header: 'Confirmed', render: (r) => `${list(r.departments).filter((d) => d.acknowledgedAt).length}/${list(r.departments).length}` },
          { key: 'status', header: 'Status', render: pill('status') }]} />
      <ErrorAlert error={create.error} />
      {open && <BEODrawer id={open} onClose={() => setParams({})} />}
    </>
  );
}

function BEODrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const toast = useToast();
  const q = useGet<R>(`${API}/beos/${id}`);
  const b = q.data;
  const rev = b ? `"${String(b.rev)}"` : '';
  const [reason, setReason] = useState('');
  const [notes, setNotes] = useState('');
  const [instr, setInstr] = useState({ department: 'kitchen', text: '' });
  const [dept, setDept] = useState('kitchen');
  const patch = useSend<B>('PATCH', `${API}/beos/${id}`, INV, () => ({ 'If-Match': rev }));
  const issue = useSend<B>('POST', `${API}/beos/${id}:issue`, INV, () => ({ 'If-Match': rev }));
  const revise = useSend<B, R>('POST', `${API}/beos/${id}:revise`, INV, () => ({ 'If-Match': rev }));
  const ack = useSend<B>('POST', `${API}/beos/${id}:acknowledge`, INV);
  if (!b) return <Modal open onClose={onClose} title="BEO"><Skeleton /></Modal>;
  const c = obj(b.content);
  const ev = obj(c.event);
  return (
    <Modal open onClose={onClose} wide title={`BEO ${String(b.number)} v${String(b.version)} · ${String(b.status)}`} actions={<>
      <a className="oc-btn oc-btn-neutral" href={`${API}/beos/${id}/pdf`} target="_blank" rel="noreferrer">PDF</a>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button>
    </>}>
      <div className="oc-stack">
        <KV items={[['Event', `${String(ev.number)} · ${String(ev.title)}`], ['Date', formatDateTime(String(ev.start))], ['Customer', String(ev.customer)],
          ['Pax', `${String(obj(c.pax).guaranteed)} guaranteed · basis ${String(obj(c.pax).basis)}`], ['Layout', label(ev.layout)], ['Special requests', String(ev.specialRequests || '—')],
          ['Notes', String(b.notes ?? '—')], ['Electricity', `${String(obj(c.electricity).requiredWatt)} W required / ${String(obj(c.electricity).includedWatt)} W included`],
          ['Locked', b.lockedAt ? formatDateTime(String(b.lockedAt)) : '—']]} />
        <Card title="Venues & run-of-show" icon="schedule">
          <ul>{list(c.venues).map((v, i) => <li key={i}>{String(v.venue)} · {String(v.function)} · {label(v.layout)} · {String(v.pax)} pax</li>)}</ul>
          <ul>{list(c.schedule).map((s, i) => <li key={i}>{formatDateTime(String(s.start))} {String(s.title)} {String(s.venue ?? '')} {String(s.owner ?? '')}</li>)}</ul>
        </Card>
        <Card title="Menu & production" icon="restaurant_menu">
          {list(c.menus).map((m, i) => <div key={i}><strong>{String(m.menu)}</strong><ul>{list(m.items).map((it, j) => (
            <li key={j}>{String(it.category)} · {String(it.name)} — {String(it.portions)} portions {it.station ? `@ ${String(it.station)}` : ''} {list(it.allergens).length ? `(${list(it.allergens).join(', ')})` : ''}</li>))}</ul></div>)}
          {list(c.meetingChanges).length > 0 && <p><strong>Agreed changes:</strong> {list(c.meetingChanges).join('; ')}</p>}
          {list(b.requirements).length > 0 && <DataTable rows={list(b.requirements)} columns={[{ key: 'itemName', header: 'Ingredient' }, { key: 'quantity', header: 'Quantity', align: 'right' },
            { key: 'uom', header: 'UOM' }, { key: 'neededBy', header: 'Needed by' }]} />}
        </Card>
        {list(b.changes).length > 0 && <Card title={`Changes since v${Number(b.version) - 1}`} icon="difference">
          <ul>{list(b.changes).map((x, i) => <li key={i}><StatusPill status={String(x.change)} /> {String(x.section)} · {String(x.item)}</li>)}</ul>
        </Card>}
        <Card title="Distribution" icon="forward_to_inbox">
          <DataTable rows={list(b.departments)} columns={[{ key: 'department', header: 'Department', render: (d) => label(d.department) },
            { key: 'instructions', header: 'Instructions', render: (d) => String(d.instructions ?? '') },
            { key: 'acknowledgedAt', header: 'Confirmed', render: (d) => (d.acknowledgedAt ? `${formatDateTime(String(d.acknowledgedAt))} ${String(d.acknowledgedBy ?? '')}` : <StatusPill status="pending" />) }]} />
          {b.status === 'issued' && can('banquet.beo.acknowledge') && <div className="oc-row-wrap">
            <SelectField label="Department" value={dept} onChange={setDept} options={list(b.departments).map((d) => ({ value: String(d.department), label: label(d.department) }))} />
            <button className="oc-btn oc-btn-ink" onClick={() => ack.mutate({ department: dept }, { onSuccess: () => toast('Confirmed') })}>Confirm read</button>
          </div>}
          <ErrorAlert error={ack.error} />
        </Card>
        {b.status === 'draft' && can('banquet.beo.manage') && <Card title="Edit draft" icon="edit">
          <div className="oc-row-wrap">
            <SelectField label="Department" value={instr.department} onChange={(v) => setInstr((x) => ({ ...x, department: v }))} options={DEPARTMENTS} />
            <TextField label="Instructions" value={instr.text} onChange={(v) => setInstr((x) => ({ ...x, text: v }))} />
            <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => patch.mutate({ instructions: { [instr.department]: instr.text } })}>Save</button>
            <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => patch.mutate({ refresh: true })}>Refresh from event</button>
          </div>
          <ErrorAlert error={patch.error} />
        </Card>}
        {b.status === 'draft' && can('banquet.beo.issue') && <><button className="oc-btn oc-btn-primary" disabled={issue.isPending} onClick={() => issue.mutate({})}>Issue BEO</button><ErrorAlert error={issue.error} /></>}
        {b.status === 'issued' && !b.lockedAt && can('banquet.beo.issue') && <Card title="Revise" icon="history_edu">
          <TextField label="Reason" value={reason} onChange={setReason} required />
          <TextArea label="Notes (allergy, VIP)" value={notes} onChange={setNotes} />
          <ErrorAlert error={revise.error} />
          <button className="oc-btn oc-btn-primary" disabled={!reason || revise.isPending} onClick={() => revise.mutate({ reason, notes: opt(notes) }, { onSuccess: (nb) => { toast(`Version ${String(nb.version)} issued`); onClose(); } })}>Issue next version</button>
        </Card>}
      </div>
    </Modal>
  );
}

// ── Venues, Event Schedule (venue calendar), Event Checklist ─────────────

export function VenuesPage() {
  const [tab, setTab] = useState('banquet.venue');
  const [p, setP] = useState({ start: '', end: '', pax: '', layout: '' });
  const avail = useGet<Page<R>>(p.start && p.end ? `${API}/venue-availability${qs({ start: iso(p.start), end: iso(p.end), pax: p.pax, layout: p.layout })}` : null);
  return (
    <div className="oc-stack">
      <PageHeader title="Venues" help="Ballroom, function rooms, outdoor and VIP Suite with capacities per layout; each venue is a bookable resource with setup / teardown buffers." />
      <Card title="Availability" icon="event_available">
        <div className="oc-row-wrap">
          <TextField label="Start" type="datetime-local" value={p.start} onChange={(v) => setP((x) => ({ ...x, start: v }))} />
          <TextField label="End" type="datetime-local" value={p.end} onChange={(v) => setP((x) => ({ ...x, end: v }))} />
          <TextField label="Pax" type="number" value={p.pax} onChange={(v) => setP((x) => ({ ...x, pax: v }))} />
          <SelectField label="Layout" value={p.layout} onChange={(v) => setP((x) => ({ ...x, layout: v }))} options={LAYOUTS} placeholder="Any" />
        </div>
        {avail.data && <DataTable rows={avail.data.items} columns={[{ key: 'name', header: 'Venue' }, { key: 'capacity', header: 'Capacity', align: 'right' },
          { key: 'minPax', header: 'Min pax', align: 'right' }, { key: 'fits', header: 'Fits', render: (a) => (a.fits ? 'yes' : 'no') },
          { key: 'available', header: 'Available', render: (a) => <StatusPill status={a.available ? 'available' : 'booked'} /> }, { key: 'waitlist', header: 'Waitlist', align: 'right' }]} />}
      </Card>
      <Tabs tabs={[{ value: 'banquet.venue', label: 'Venues' }, { value: 'banquet.venue_layout', label: 'Layout Capacities' }]} value={tab} onChange={setTab} />
      <AutoResourcePage key={tab} resourceKey={tab} />
    </div>
  );
}

export function EventSchedulePage() {
  const [from, setFrom] = useState(today());
  const [to, setTo] = useState(() => { const d = new Date(); d.setDate(d.getDate() + 13); return d.toISOString().slice(0, 10); });
  const cal = useGet<Page<R>>(`${API}/venue-calendar${qs({ from, to })}`);
  const evs = useGet<Page<R>>(`${API}/events${qs({ from, to, 'filter[status]': 'inquiry,tentative,definite,completed', limit: 500 })}`);
  return (
    <div className="oc-stack">
      <PageHeader title="Event Schedule" help="Venue Calendar across venues (tentative, definite and waitlisted holds, Booking Calendar allocations) and the event calendar." />
      <div className="oc-row-wrap">
        <TextField label="From" type="date" value={from} onChange={setFrom} />
        <TextField label="To" type="date" value={to} onChange={setTo} />
      </div>
      <Card title="Venue Calendar" icon="calendar_month">
        {cal.isLoading ? <Skeleton /> : (cal.data?.items ?? []).length === 0 ? <Empty title="No venues" /> : (
          <div className="oc-stack">{(cal.data?.items ?? []).map((v) => (
            <div key={String(v.venueId)}>
              <strong>{String(v.name)}</strong> <span className="oc-small oc-muted">{label(v.venueType)}{v.parentVenueId ? ' · part' : ''}</span>
              {list(v.events).length === 0 ? <div className="oc-small oc-muted">Free</div> : (
                <ul>{list(v.events).map((x) => (
                  <li key={String(x.holdId)}>{formatDateTime(String(x.start))} – {formatDateTime(String(x.end))} · <Link to={`/banquet-event/events/${String(x.eventId)}`}>{String(x.eventNumber)} {String(x.title)}</Link>{' '}
                    <StatusPill status={String(x.holdStatus)} />{x.waitlistRank ? ` #${String(x.waitlistRank)}` : ''}</li>))}</ul>
              )}
            </div>
          ))}</div>
        )}
      </Card>
      <Card title="Events" icon="event">
        <DataTable rows={evs.data?.items} loading={evs.isLoading} columns={[{ key: 'start', header: 'Start', render: (r) => formatDateTime(String(r.start)) },
          { key: 'title', header: 'Event', render: (r) => <Link to={`/banquet-event/events/${r.id}`}>{`${String(r.number)} · ${String(r.title)}`}</Link> },
          { key: 'category', header: 'Category', render: (r) => label(r.category) }, { key: 'expectedPax', header: 'Pax', align: 'right' }, { key: 'status', header: 'Status', render: pill('status') }]} />
      </Card>
    </div>
  );
}

export function EventChecklistPage() {
  const [overdue, setOverdue] = useState(false);
  const [mine, setMine] = useState(false);
  const [department, setDepartment] = useState('');
  const toggle = useSend<B>('POST', (b) => `${API}/checklist-items/${String((b as R).id)}:toggle`, INV);
  return (
    <ListPage title="Event Checklist" help="Tasks of every live event with PIC and due date; overdue tasks are reminded every morning." path={`${API}/checklist-items`}
      search={false} statuses={opts(['open', 'done'])} extraQuery={{ overdue: overdue ? 'true' : '', mine: mine ? 'true' : '', 'filter[department]': department }}
      filters={<>
        <SelectField label="Department" value={department} onChange={setDepartment} options={DEPARTMENTS} placeholder="All" />
        <Checkbox label="Overdue" checked={overdue} onChange={setOverdue} />
        <Checkbox label="My tasks" checked={mine} onChange={setMine} />
      </>}
      columns={[{ key: 'task', header: 'Task', render: (c) => <Checkbox label={String(c.task)} checked={c.status === 'done'} onChange={(v) => toggle.mutate({ id: c.id, done: v } as B)} /> },
        { key: 'eventNumber', header: 'Event', render: (c) => <Link to={`/banquet-event/events/${String(c.eventId)}?tab=checklist`}>{`${String(c.eventNumber)} · ${String(c.eventTitle)}`}</Link> },
        { key: 'department', header: 'Department', render: (c) => label(c.department) }, { key: 'ownerName', header: 'PIC', render: (c) => String(c.ownerName ?? '—') },
        { key: 'dueDate', header: 'Due', render: (c) => <>{c.dueDate ? formatDate(String(c.dueDate)) : '—'}{c.overdue ? <> <StatusPill status="overdue" /></> : null}</> }]} />
  );
}

export function EventBillingPage() {
  const navigate = useNavigate();
  return (
    <ListPage title="Event Billing" help="Contract, received and balance per event; open an event for its charges, payment schedule and final billing." path={`${API}/events`}
      statuses={opts(['tentative', 'definite', 'completed'])} onRowClick={(r) => navigate(`/banquet-event/events/${r.id}?tab=billing`)}
      columns={[{ key: 'number', header: 'Event' }, { key: 'title', header: 'Title' }, { key: 'start', header: 'Date', render: (r) => formatDate(String(r.start)) },
        { key: 'contractTotal', header: 'Contract', align: 'right', render: (r) => money(r.contractTotal) },
        { key: 'finalBilledAt', header: 'Final billing', render: (r) => (r.settledAt ? 'settled' : r.finalBilledAt ? 'billed' : '—') }, { key: 'status', header: 'Status', render: pill('status') }]} />
  );
}

const MASTER: Option[] = [{ value: 'banquet.event_type', label: 'Event Types' }, { value: 'banquet.menu', label: 'Menus' },
  { value: 'banquet.menu_category', label: 'Menu Categories' }, { value: 'banquet.menu_item', label: 'Menu Items' }, { value: 'banquet.charge_type', label: 'Extra Charges' },
  { value: 'banquet.vendor', label: 'Vendors' }, { value: 'banquet.checklist_template', label: 'Checklist Templates' },
  { value: 'banquet.checklist_template_item', label: 'Checklist Tasks' }];

export function BanquetMasterPage() {
  const [tab, setTab] = useState(MASTER[0].value);
  return (
    <div className="oc-stack">
      <PageHeader title="Banquet Master Data" help="Event types, menus with category quotas and recipes (BOM per pax), extra charges, vendors and checklist templates." />
      <Tabs tabs={MASTER} value={tab} onChange={setTab} />
      <AutoResourcePage key={tab} resourceKey={tab} />
    </div>
  );
}

export function PackagesPage() {
  return (
    <div className="oc-stack">
      <AutoResourcePage resourceKey="banquet.package" />
      <p className="oc-small oc-muted">Inclusions: a JSON list, e.g. {'[{"kind":"resource","label":"Bungalow suite","resourceType":"bungalow","nights":1},{"kind":"voucher","label":"F&B voucher","voucherTypeCode":"FNB500","quantity":2},{"kind":"service","label":"Food tasting"}]'}</p>
    </div>
  );
}

// Migration wave 3 (FR-MIG-P3-01/05).
export function EventImportPage() {
  const [csv, setCsv] = useState('');
  const rows = useMemo(() => parseTable(csv).map((r) => ({ ...r, pax: num(r.pax ?? '') ?? 0 })), [csv]);
  const run = useSend<B, R>('POST', `${API}/events:import`, INV);
  const rec = useGet<R>(`${API}/migration/reconciliation`);
  return (
    <div className="oc-stack">
      <PageHeader title="Event Import" help="Future banquets and events of the banquet book with the DP already received: preview first, then import (idempotent per reference)." />
      <Card title="Banquet book" icon="upload">
        <p className="oc-small">Columns: legacyRef, title, eventType, customerName, customerPhone, customerEmail, corporateAccountCode, date, startTime, endTime, pax, packageCode, venueCode,
          layout, menu, contractTotal, downPayment, downPaymentDate, balanceDueDate, notes</p>
        <input type="file" accept=".csv,.txt" aria-label="Banquet book file" onChange={(e) => { const f = e.target.files?.[0]; if (f) void f.text().then(setCsv); }} />
        <TextArea label="or paste CSV" value={csv} onChange={setCsv} rows={6} />
        <div className="oc-row-wrap">
          <button className="oc-btn oc-btn-neutral" disabled={!rows.length || run.isPending} onClick={() => run.mutate({ dryRun: true, rows })}>Preview ({rows.length} rows)</button>
          <button className="oc-btn oc-btn-primary" disabled={!rows.length || run.isPending} onClick={() => run.mutate({ rows }, { onSuccess: () => void rec.refetch() })}>Import</button>
        </div>
        <ErrorAlert error={run.error} />
      </Card>
      {run.data && <Card title={run.data.dryRun ? 'Preview' : 'Imported'} icon="fact_check">
        <p>{String(run.data.imported)} imported · {String(run.data.existing)} existing · {String(run.data.errors)} errors</p>
        <DataTable rows={list(run.data.rows)} columns={[{ key: 'row', header: 'Row', align: 'right' }, { key: 'legacyRef', header: 'Reference' },
          { key: 'status', header: 'Result', render: pill('status') }, { key: 'eventNumber', header: 'Event' }, { key: 'downPayment', header: 'DP', align: 'right', render: (r) => money(r.downPayment) },
          { key: 'balance', header: 'Balance', align: 'right', render: (r) => money(r.balance) }, { key: 'message', header: 'Message' }]} />
      </Card>}
      {rec.data && <Card title="Reconciliation" icon="balance">
        <KV items={[['As of', String(rec.data.asOf)], ['Imported events', String(rec.data.importedEvents)], ['Future events (all / imported)', `${String(rec.data.futureEvents)} / ${String(rec.data.importedFutureEvents)}`],
          ['DP in the banquet book', money(rec.data.migratedDownPayment)], ['Deposit liability (held)', money(rec.data.depositLiability)], ['Contract value', money(rec.data.contractTotal)],
          ['Outstanding balance', money(rec.data.outstandingBalance)], ['Balanced', rec.data.balanced ? <StatusPill status="balanced" /> : <StatusPill status="difference" />]]} />
      </Card>}
    </div>
  );
}

/** Back Office routes of the area. */
export const BANQUET_ROUTES: AreaRoute[] = [
  { path: 'banquet-event', perm: 'banquet.event.view', element: <EventsPage /> },
  { path: 'banquet-event/events', perm: 'banquet.event.view', element: <EventsPage /> },
  { path: 'banquet-event/events/:id', perm: 'banquet.event.view', element: <EventDetailPage /> },
  { path: 'banquet-event/packages', perm: 'banquet.package.view', element: <PackagesPage /> },
  { path: 'banquet-event/venues', perm: 'banquet.venue.view', element: <VenuesPage /> },
  { path: 'banquet-event/beo', perm: 'banquet.beo.view', element: <BEOPage /> },
  { path: 'banquet-event/schedule', perm: 'banquet.event.view', element: <EventSchedulePage /> },
  { path: 'banquet-event/checklist', perm: 'banquet.checklist.view', element: <EventChecklistPage /> },
  { path: 'banquet-event/billing', perm: 'banquet.billing.view', element: <EventBillingPage /> },
  { path: 'banquet-event/master', perm: 'banquet.menu.view', element: <BanquetMasterPage /> },
  { path: 'banquet-event/import', perm: 'banquet.event.import', element: <EventImportPage /> },
];

// ── Event Operations & Banquet Production (ops, FR-OPS-P3-01/05, §7.2) ───

function TodayEventsPage() {
  const [day, setDay] = useState(today());
  const q = useGet<Page<R>>(`${API}/today${qs({ date: day })}`, { refetchInterval: 60_000 });
  return (
    <div className="oc-stack">
      <div className="oc-page-head"><div><h1>Today&apos;s Events</h1><p>Venues, run-of-show, the latest BEO, checklist and guests of the day.</p></div>
        <span className="oc-spacer" /><Link className="oc-btn oc-btn-ink" to="/ops/events/check-in">Event Check-in</Link></div>
      <TextField label="Date" type="date" value={day} onChange={setDay} />
      {q.isLoading ? <Skeleton /> : (q.data?.items ?? []).length === 0 ? <Empty title="No events" icon="celebration" /> : (q.data?.items ?? []).map((t) => <TodayCard key={String(obj(t.event).id)} t={t} />)}
    </div>
  );
}

function TodayCard({ t }: { t: R }) {
  const e = obj(t.event);
  const { can } = useAuth();
  const toast = useToast();
  const [open, setOpen] = useState(false);
  const [note, setNote] = useState('');
  const [sev, setSev] = useState('low');
  const tasks = useGet<Page<R>>(open ? `${API}/events/${String(e.id)}/checklist` : null);
  const toggle = useSend<B>('POST', (b) => `${API}/checklist-items/${String((b as R).id)}:toggle`, INV);
  const incident = useSend<B>('POST', `${API}/events/${String(e.id)}/incidents`, INV);
  const beo = t.beo ? obj(t.beo) : null;
  const p = obj(t.participants);
  return (
    <Card title={`${String(e.number)} · ${String(e.title)}`} icon="celebration" actions={<StatusPill status={String(e.status)} />}>
      <KV items={[['Time', `${formatDateTime(String(e.start))} – ${formatDateTime(String(e.end))}`], ['Venues', list(t.venues).filter((v) => ['tentative', 'definite', 'completed'].includes(String(v.status))).map((v) => String(v.venueName)).join(', ') || '—'],
        ['Pax', String(e.finalPax ?? e.guaranteedPax ?? e.expectedPax)], ['Guests', `${String(p.seats)} seats · ${String(p.checkedIn)} checked in · ${String(p.waitlisted)} waitlisted`],
        ['BEO', beo ? `${String(beo.number)} v${String(beo.version)} (${String(beo.status)}) · to confirm: ${list(beo.pendingDepartments).map(label).join(', ') || 'none'}` : '—'],
        ['Checklist', `${String(obj(t.checklist).done)}/${String(obj(t.checklist).total)} · ${String(obj(t.checklist).overdue)} overdue`], ['Incidents', String(t.incidents)]]} />
      <ul>{list(t.schedule).map((s) => <li key={String(s.id)}>{formatDateTime(String(s.start))} {String(s.title)} {String(s.venueName ?? '')} {String(s.ownerName ?? '')}</li>)}</ul>
      <div className="oc-row-wrap">
        {beo && <Link className="oc-btn oc-btn-neutral oc-btn-sm" to={`/ops/events/beo/${String(beo.id)}`}>Open BEO</Link>}
        <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setOpen(!open)}>{open ? 'Hide checklist' : 'Checklist'}</button>
      </div>
      {open && <DataTable rows={tasks.data?.items} columns={[{ key: 'task', header: 'Task', render: (c) => <Checkbox label={String(c.task)} checked={c.status === 'done'}
        onChange={(v) => toggle.mutate({ id: c.id, done: v } as B)} /> }, { key: 'department', header: 'Dept', render: (c) => label(c.department) }]} />}
      {can('banquet.event.operate') && (
        <form className="oc-row-wrap" onSubmit={(ev) => { ev.preventDefault(); incident.mutate({ note, severity: sev }, { onSuccess: () => { setNote(''); toast('Incident noted'); } }); }}>
          <SelectField label="Severity" value={sev} onChange={setSev} options={opts(['low', 'medium', 'high'])} />
          <div style={{ flex: 1, minWidth: 220 }}><TextField label="Incident note" value={note} onChange={setNote} /></div>
          <button className="oc-btn oc-btn-ink oc-btn-sm" disabled={!note}>Add note</button>
        </form>
      )}
      <ErrorAlert error={incident.error ?? toggle.error} />
    </Card>
  );
}

function OpsBEOPage() {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  return <BEODrawer id={id} onClose={() => navigate('/ops/events')} />;
}

/** QR check-in at the door; scans are queued offline and synced idempotently (FR-OPS-P3-05). */
function CheckInPage() {
  const toast = useToast();
  const { propertyId } = useAuth();
  const events = useGet<Page<R>>(`${API}/today`, { refetchInterval: 60_000 });
  const [eventId, setEventId] = useState(() => { try { return localStorage.getItem('oneclub.ops.event') ?? ''; } catch { return ''; } });
  const [code, setCode] = useState('');
  const [result, setResult] = useState<R | null>(null);
  const [error, setError] = useState<unknown>(null);
  const scan = async (ev: React.FormEvent) => {
    ev.preventDefault();
    setError(null);
    setResult(null);
    try {
      if (!navigator.onLine) {
        await enqueue('banquet.event_check_in', { eventId, code }, propertyId);
        toast('Offline: check-in recorded in the sync queue');
      } else {
        setResult(await request<R>('POST', `${API}/events/${eventId}:check-in`, { code }));
      }
      setCode('');
    } catch (err) {
      setError(err);
    }
  };
  const participant = result ? obj(result.participant) : null;
  return (
    <div className="oc-stack">
      <div className="oc-page-head"><div><h1>Event Check-in</h1><p>Scan the QR ticket. Works offline: scans are sent when the device is back online.</p></div></div>
      <Card title="Scan" icon="qr_code_scanner">
        <form className="oc-row-wrap" onSubmit={scan}>
          <div style={{ width: 300 }}><SelectField label="Event" value={eventId} onChange={(v) => { setEventId(v); try { localStorage.setItem('oneclub.ops.event', v); } catch { /* ignore */ } }}
            options={(events.data?.items ?? []).map((t) => ({ value: String(obj(t.event).id), label: `${String(obj(t.event).number)} · ${String(obj(t.event).title)}` }))} placeholder="Select event" /></div>
          <div style={{ flex: 1, minWidth: 240 }}><TextField label="Ticket code" value={code} onChange={setCode} autoFocus /></div>
          <button className="oc-btn oc-btn-ink oc-btn-lg" style={{ alignSelf: 'flex-end', minHeight: 48 }} disabled={!eventId || !code}>Check in</button>
        </form>
        <ErrorAlert error={error} />
        {participant && (
          <div className={`oc-alert ${result?.alreadyCheckedIn ? 'oc-alert-warning' : 'oc-alert-success'}`} style={{ marginTop: 12 }} role="status">
            <strong>{result?.alreadyCheckedIn ? 'Already checked in' : 'Welcome'}</strong> · {String(participant.name)} · {String(participant.partySize)} seat(s)
            {participant.tableNo ? ` · table ${String(participant.tableNo)}` : ''}{participant.vip ? ' · VIP' : ''}{participant.dietaryNotes ? ` · ${String(participant.dietaryNotes)}` : ''}
          </div>
        )}
      </Card>
    </div>
  );
}

/** Kitchen Banquet Production from the issued BEOs (FR-BEO-04). */
function ProductionPage() {
  const [day, setDay] = useState(today());
  const [station, setStation] = useState('');
  const q = useGet<Page<R>>(`${API}/production${qs({ date: day, 'filter[station]': station })}`, { refetchInterval: 30_000 });
  const move = useSend<B>('POST', (b) => `${API}/production-items/${String((b as R).id)}:status`, INV);
  const next: Record<string, [string, string]> = { pending: ['in_progress', 'Start'], in_progress: ['ready', 'Ready'], ready: ['served', 'Served'] };
  const cols: [string, string][] = [['pending', 'To produce'], ['in_progress', 'In progress'], ['ready', 'Ready'], ['served', 'Served']];
  return (
    <div className="oc-stack">
      <div className="oc-page-head"><div><h1>Banquet Production</h1><p>Dishes of the issued BEOs by serving time; a BEO revision replaces the list.</p></div>
        <span className="oc-spacer" />
        <div className="oc-row-wrap"><TextField label="Date" type="date" value={day} onChange={setDay} /><TextField label="Station" value={station} onChange={setStation} placeholder="buffet, kitchen…" /></div></div>
      <ErrorAlert error={move.error} />
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(240px, 1fr))', gap: 16 }}>
        {cols.map(([s, title]) => (
          <div key={s} className="oc-stack">
            <h3>{title}</h3>
            {(q.data?.items ?? []).filter((p) => p.status === s).map((p) => (
              <div key={p.id} className="oc-card">
                <div className="oc-row"><strong>{String(p.quantity)} × {String(p.name)}</strong></div>
                <div className="oc-small oc-muted">{formatDateTime(String(p.serveAt))} · {String(p.eventNumber)} {String(p.eventTitle)} · BEO v{String(p.beoVersion)}{p.station ? ` · ${String(p.station)}` : ''}</div>
                {next[s] && <button className="oc-btn oc-btn-ink oc-btn-sm oc-btn-block" style={{ marginTop: 8, minHeight: 44 }}
                  onClick={() => move.mutate({ id: p.id, status: next[s][0] } as B)}>{next[s][1]}</button>}
              </div>
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}

/** Ops workstation tiles and routes of the area. */
export const BANQUET_OPS_TILES: OpsTile[] = [
  ['celebration', 'Event Operations', '/ops/events', 'banquet.event.operate'],
  ['qr_code_scanner', 'Event Check-in', '/ops/events/check-in', 'banquet.participant.check_in'],
  ['skillet', 'Banquet Production', '/ops/banquet-production', 'banquet.production.view'],
];
export const BANQUET_OPS_ROUTES: OpsRoute[] = [
  { path: 'events', element: <TodayEventsPage /> },
  { path: 'events/check-in', element: <CheckInPage /> },
  { path: 'events/beo/:id', element: <OpsBEOPage /> },
  { path: 'banquet-production', element: <ProductionPage /> },
];
