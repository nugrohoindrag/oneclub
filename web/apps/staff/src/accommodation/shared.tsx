import React, { useMemo, useState } from 'react';
import { Link, useLocation } from 'react-router';
import { qs, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate, formatDateTime, formatNumber } from '@oneclub/i18n';
import {
  Checkbox, DataTable, Drawer, Empty, ErrorAlert, Icon, Modal, MoneyField, SelectField, Skeleton, StatusPill, TextArea, TextField, useAuth, useToast,
  type StatusTone,
} from '@oneclub/shell';
import { FolioDrawer } from '../p1/business';
import { KV, Tabs } from '../p1/common';
import { IdentityPhoto, MoveModal, SignaturePad, UpgradeModal, VoidModal, openPdf } from './mgcc';

// Accommodation & Bungalow Management (docs/oneclub-accommodation-bungalow-
// management-requirements.md): the types of the accommodation API and the
// pieces shared by the Back Office (Accommodation) and Ops (Front Office,
// Housekeeping, Maintenance) pages — the reservation drawer and the check-in,
// check-out, reschedule, charge, request and cancel dialogs.

export type Row = Record<string, unknown>;

export interface Stay extends Row {
  id: string; propertyId: string; stayNo: string; kind: string; reservationId: string; reservationCode: string; customerId: string | null;
  customerName: string | null; guestName: string | null; guestPhone: string | null; guestEmail: string | null; corporateName: string | null;
  unitId: string; unitName: string; unitCode: string | null; unitTypeId: string | null; typeName: string | null; unitAssigned: boolean;
  start: string; end: string; actualEnd: string | null; adults: number; children: number; ratePlan: string | null; specialRequests: string | null;
  status: string; checkedInAt: string | null; checkedOutAt: string | null; folioId: string | null; channel: string; createdAt: string;
  roomPosting: string; nights: number; reservationStatus: string; bookingSource: string | null; corporateAccountId: string | null;
  billingArrangement: string | null; promotionCode: string | null; discount: string; notes: string | null; vip: boolean; expectedArrival: string | null;
  earlyCheckIn: boolean; lateCheckOutUntil: string | null; cancelReason: string | null; noShowFee: string | null; totalDue: string; paid: string;
  paymentStatus: string; idNumberMasked: string | null;
  bookingStatus: string; holdExpiresAt: string | null; groupId: string | null; groupNo: string | null; publicToken: string | null; occupantName: string | null;
  nationality: string | null; registeredAt: string | null; hasIdPhoto: boolean; keysIssued: number; keysReturned: number; keyNumbers: string | null;
  voidReason: string | null; taxIncluded: boolean | null;
}
export interface StayAddon { id: string; addonId: string; name: string; category: string; quantity: number; units: number; unitPrice: string; total: string;
  included: boolean; source: string; voidedAt: string | null }
export interface StayResult { stay: Stay; reservation: Row & { depositRequired: string; cancellationFee: string | null }; folio: (Row & { id: string; status: string;
  summary: Row; lines: Row[]; payments: Row[] }) | null; total: string; depositRequired: string; addons: StayAddon[] }
export interface RoomState extends Row {
  id: string; code: string; name: string; typeId: string; typeName: string; location: string | null; view: string | null; capacity: number; status: string;
  hkStatus: string; hkUpdatedAt: string | null; blockKind: string | null; blockReason: string | null; blockUntil: string | null; operationalStatus: string;
  reservationStatus: string; currentStayId: string | null; currentStayNo: string | null; currentGuest: string | null; currentDeparture: string | null;
  arrivalStayId: string | null; arrivalStayNo: string | null; arrivalGuest: string | null; arrivalAt: string | null; openWorkOrders: number; openHkTasks: number;
  notes: string | null;
}
export interface ChecklistItem { area: string; item: string; done: boolean; note?: string }
export interface HKTask extends Row {
  id: string; taskNo: string; bungalowId: string; bungalowCode: string; bungalowName: string; hkStatus: string; stayId: string | null; stayNo: string | null;
  taskType: string; priority: string; taskDate: string; assignedTo: string | null; status: string; startedAt: string | null; completedAt: string | null;
  inspectedAt: string | null; reworks: number; checklist: ChecklistItem[]; notes: string | null; source: string; createdAt: string; minutesTaken: number | null;
}
export interface WorkOrder extends Row {
  id: string; woNo: string; bungalowId: string | null; bungalowCode: string | null; bungalowName: string | null; category: string; title: string;
  description: string | null; priority: string; status: string; source: string; assignedTo: string | null; reportedBy: string | null; closesUnit: boolean;
  blockId: string | null; expectedEndAt: string | null; scheduleName: string | null; dueDate: string | null; cost: string | null; resolution: string | null;
  createdAt: string; resolvedAt: string | null; closedAt: string | null; hoursOpen: string | null;
}
export interface GuestRequest extends Row {
  id: string; requestNo: string; stayId: string; stayNo: string; guest: string | null; bungalowId: string | null; bungalowCode: string | null;
  requestType: string; description: string | null; quantity: number; addonName: string | null; chargeAmount: string | null; status: string;
  assignedTo: string | null; source: string; requestedAt: string; completedAt: string | null; folioLineId: string | null; notes: string | null;
  minutesToComplete: number | null;
}
export interface UnitOption { id: string; code: string; name: string; typeId: string; typeName: string; sameType: boolean; free: boolean; hkStatus: string;
  operationalStatus: string; capacity: number; warnings: string[]; current: boolean }

export const INV = ['/api/v1/stay', '/api/v1/billing', '/api/v1/reservation'];

export const label = (s: string | null | undefined) => (s ? s.replace(/_/g, ' ').replace(/^./, (c) => c.toUpperCase()) : '—');
export const money = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : `Rp ${formatNumber(Number(v))}`);
export const guestOf = (s: Pick<Stay, 'customerName' | 'guestName' | 'corporateName'>) => s.customerName ?? s.guestName ?? s.corporateName ?? '—';
export const todayISO = (offset = 0) => {
  const d = new Date();
  d.setDate(d.getDate() + offset);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
};
export const addDays = (day: string, n: number) => {
  const d = new Date(`${day}T00:00:00`);
  d.setDate(d.getDate() + n);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
};
export const timeOf = (iso: string | null | undefined) => (iso ? new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : '—');
export const dayOf = (iso: string | null | undefined) => (iso ? formatDate(iso) : '—');

/** Reservation status of a stay (§4): Confirmed, Pending Payment, Checked-in … */
export function stayStatus(s: Stay): [string, StatusTone] {
  // one status table for every screen (docs/requirement-booking-hotel-mgcc.md §12.3)
  switch (s.bookingStatus ?? s.status) {
    case 'requested': return ['Requested', 'warning'];
    case 'awaiting_payment': return ['Awaiting Payment', 'warning'];
    case 'confirmed': return ['Confirmed', 'info'];
    case 'reserved': return s.reservationStatus === 'confirmed' ? ['Confirmed', 'info'] : ['Awaiting Payment', 'warning'];
    case 'in_house': case 'checked_in': return ['In-house', 'success'];
    case 'checked_out': return ['Checked-out', 'neutral'];
    case 'cancelled': return ['Cancelled', 'neutral'];
    case 'no_show': return ['No-show', 'error'];
    case 'expired': return ['Expired', 'neutral'];
    case 'void': return ['Void', 'neutral'];
  }
  return [label(s.status), 'neutral'];
}
export function StayStatus({ s }: { s: Stay }) {
  const [l, t] = stayStatus(s);
  return <StatusPill status={s.status} label={l} tone={t} />;
}

/** Payment status (§25). */
export const PAYMENT: Record<string, [string, StatusTone]> = {
  unpaid: ['Unpaid', 'error'], pending: ['Pending', 'warning'], paid: ['Paid', 'success'], partially_paid: ['Deposit Paid', 'warning'],
  overpaid: ['Overpaid', 'info'],
  refund_pending: ['Refund Pending', 'warning'], refunded: ['Refunded', 'neutral'], failed: ['Failed', 'error'],
};
export function PaymentStatus({ status }: { status: string }) {
  const [l, t] = PAYMENT[status] ?? [label(status), 'neutral'];
  return <StatusPill status={status} label={l} tone={t} />;
}

/** Operational status of a bungalow (§10). */
export const ROOM: Record<string, [string, StatusTone, string]> = {
  ready: ['Ready', 'success', 'var(--dash-green, #16a34a)'], inspected: ['Inspected', 'info', 'var(--dash-sky, #38bdf8)'],
  cleaned: ['Cleaned', 'info', 'var(--dash-blue, #2563eb)'], cleaning: ['Cleaning', 'warning', 'var(--dash-amber, #f59e0b)'],
  dirty: ['Dirty', 'error', 'var(--dash-red, #dc2626)'], maintenance: ['Maintenance', 'warning', '#a16207'], out_of_order: ['Out of Order', 'error', '#7f1d1d'],
  blocked: ['Blocked', 'neutral', 'var(--dash-grey, #6b7280)'],
};
export function RoomStatus({ status }: { status: string }) {
  const [l, t] = ROOM[status] ?? [label(status), 'neutral'];
  return <StatusPill status={status} label={l} tone={t} />;
}
export const OCCUPANCY: Record<string, [string, StatusTone]> = {
  available: ['Available', 'success'], reserved: ['Reserved', 'info'], occupied: ['Occupied', 'warning'], due_out: ['Due out', 'warning'],
  checked_out: ['Checked-out', 'neutral'],
};
export const SOURCES: [string, string][] = [['front_desk', 'Front Desk'], ['phone', 'Phone'], ['walk_in', 'Walk-in'], ['website', 'Direct Website'],
  ['member_app', 'Member App'], ['guest_app', 'Guest App'], ['corporate', 'Corporate']];
export const sourceLabel = (s: string | null) => SOURCES.find((x) => x[0] === s)?.[1] ?? label(s);
export const REQUEST_TYPES: [string, string, string][] = [['extra_towel', 'Extra Towel', 'dry_cleaning'], ['extra_bed', 'Extra Bed', 'bed'],
  ['room_cleaning', 'Room Cleaning', 'cleaning_services'], ['maintenance', 'Maintenance', 'build'], ['laundry', 'Laundry', 'local_laundry_service'],
  ['transportation', 'Transportation', 'airport_shuttle'], ['food_beverage', 'Food / Beverage', 'room_service'], ['other', 'Other', 'more_horiz']];
export const HK_TYPES: [string, string][] = [['checkout_cleaning', 'Checkout Cleaning'], ['stayover_cleaning', 'Stayover Cleaning'],
  ['deep_cleaning', 'Deep Cleaning'], ['turndown', 'Turndown'], ['inspection', 'Inspection'], ['other', 'Other']];
export const PRIORITIES = ['low', 'normal', 'high', 'urgent'];
export const WO_CATEGORIES: [string, string][] = [['ac', 'AC'], ['plumbing', 'Plumbing'], ['electrical', 'Electrical'], ['water_heater', 'Water Heater'],
  ['furniture', 'Furniture'], ['appliance', 'Appliance'], ['structure', 'Structure'], ['pest', 'Pest Control'], ['other', 'Other']];

// ── reservation drawer ─────────────────────────────────────────────────────

/** The reservation detail (§4) with its folio, add-ons, requests, history and actions. */
export function StayDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const r = useGet<StayResult>(`/api/v1/stay/stays/${id}`);
  const [tab, setTab] = useState('details');
  const [dialog, setDialog] = useState<string | null>(null);
  const [folio, setFolio] = useState(false);
  const x = r.data;
  const s = x?.stay;
  return (
    <Drawer open onClose={onClose} title={s ? `${s.stayNo} · ${guestOf(s)}` : 'Reservation'}>
      {r.isLoading && <Skeleton rows={8} />}
      <ErrorAlert error={r.error} />
      {x && s && (
        <div className="oc-stack">
          <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
            <StayStatus s={s} /><PaymentStatus status={s.paymentStatus} />
            {s.vip && <StatusPill status="vip" label="VIP" tone="warning" />}
            {s.earlyCheckIn && <StatusPill status="early" label="Early check-in" tone="info" />}
            {s.lateCheckOutUntil && <StatusPill status="late" label={`Late check-out ${formatDateTime(s.lateCheckOutUntil)}`} tone="info" />}
          </div>
          <StayActions s={s} onDialog={setDialog} />
          <Tabs value={tab} onChange={setTab} tabs={[{ value: 'details', label: 'Details' }, { value: 'folio', label: 'Folio' },
            { value: 'requests', label: 'Requests' }, { value: 'history', label: 'History' }]} />
          {tab === 'details' && (
            <>
              <KV items={[
                ['Reservation', `${s.stayNo} · ${s.reservationCode}`], ['Guest', <GuestLink key="g" s={s} />], ['Contact', [s.guestPhone, s.guestEmail].filter(Boolean).join(' · ') || '—'],
                ['Room type', s.typeName ?? '—'], ['Bungalow', s.unitAssigned ? s.unitName : <span key="u">{s.unitName} <span className="oc-muted oc-small">(not assigned)</span></span>],
                ['Check-in', formatDateTime(s.start)], ['Check-out', formatDateTime(s.end)], ['Nights', String(s.nights)],
                ['Guests', `${s.adults} adult(s)${s.children ? `, ${s.children} child(ren)` : ''}`], ['Rate plan', s.ratePlan ?? '—'],
                ['Promotion', s.promotionCode ? `${s.promotionCode} (−${money(s.discount)})` : '—'], ['Booking source', sourceLabel(s.bookingSource)],
                ['Corporate', s.corporateName ? `${s.corporateName}${s.billingArrangement ? ` · ${label(s.billingArrangement)}` : ''}` : '—'],
                ['Total', <strong key="t">{money(s.totalDue)}</strong>], ['Paid', money(s.paid)], ['Deposit required', money(x.depositRequired)],
                ['Special request', s.specialRequests ?? '—'], ['Notes', s.notes ?? '—'], ['Expected arrival', s.expectedArrival ?? '—'],
                ['Group', s.groupNo ?? '—'], ['Guest staying', s.occupantName ?? '—'], ['Nationality', s.nationality ?? '—'],
                ['Keys', s.keysIssued ? `${s.keysIssued} handed over · ${s.keysReturned} back${s.keyNumbers ? ` (${s.keyNumbers})` : ''}` : '—'],
                ['Registration card', s.registeredAt ? `signed ${formatDateTime(s.registeredAt)}` : '—'],
                ...(s.holdExpiresAt ? [['Held until', formatDateTime(s.holdExpiresAt)] as [string, React.ReactNode]] : []),
                ...(s.voidReason ? [['Void reason', s.voidReason] as [string, React.ReactNode]] : []),
                ['Identity', s.idNumberMasked ?? '—'], ['Created', `${formatDateTime(s.createdAt)} · ${label(s.channel)}`],
                ...(s.cancelReason ? [['Cancel / no-show reason', s.cancelReason] as [string, React.ReactNode]] : []),
                ...(s.noShowFee ? [['No-show charge', money(s.noShowFee)] as [string, React.ReactNode]] : []),
              ]} />
              <div className="oc-row-wrap">
                {s.status !== 'cancelled' && s.status !== 'expired' && s.status !== 'void' && (
                  <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => void openPdf(`/api/v1/stay/stays/${s.id}/registration-card.pdf`, `registration-${s.stayNo}.pdf`)}>
                    <Icon name="badge" size={16} /> Registration card</button>)}
                {s.folioId && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => void openPdf(`/api/v1/stay/stays/${s.id}/invoice.pdf`, `invoice-${s.stayNo}.pdf`)}>
                  <Icon name="receipt" size={16} /> Invoice / folio</button>}
              </div>
              {(s.status === 'reserved' || s.status === 'checked_in') && can('stay.stay.check_in') && <IdentityPhoto stay={s} onDone={() => void r.refetch()} />}
              {x.addons.length > 0 && (
                <DataTable rows={x.addons as unknown as Row[]} rowKey={(a) => String(a.id)} columns={[
                  { key: 'name', header: 'Add-on', render: (a) => <>{String(a.name)}{a.included ? <span className="oc-small oc-muted"> · included</span> : null}</> },
                  { key: 'quantity', header: 'Qty', align: 'right', render: (a) => `${a.quantity}${Number(a.units) > 1 ? ` × ${a.units}` : ''}` },
                  { key: 'total', header: 'Total', align: 'right', render: (a) => (a.voidedAt ? <StatusPill status="cancelled" label="Removed" /> : money(a.total)) }]}
                  actions={(a) => !a.voidedAt && !a.included && can('stay.stay.charge') && ['reserved', 'checked_in'].includes(s.status) ? <VoidAddon stay={s.id} addon={String(a.id)} /> : null} />
              )}
            </>
          )}
          {tab === 'folio' && (
            <div className="oc-stack">
              <KV items={[['Charges', money(x.folio?.summary.charges)], ['Not posted yet (room nights)', money(Number(s.totalDue) - Number(x.folio?.summary.charges ?? 0))],
                ['Paid', money(x.folio?.summary.payments)], ['Deposits held', money(x.folio?.summary.heldDeposits)], ['Balance', <strong key="b">{money(x.folio?.summary.balance)}</strong>]]} />
              <DataTable rows={(x.folio?.lines ?? []) as Row[]} rowKey={(l) => String(l.id)} empty={<Empty title="No charges yet" icon="receipt_long" />} columns={[
                { key: 'description', header: 'Description' }, { key: 'quantity', header: 'Qty', align: 'right' },
                { key: 'total', header: 'Total', align: 'right', render: (l) => (l.voidedAt ? <s>{money(l.total)}</s> : money(l.total)) }]} />
              {s.folioId && can('billing.folio.view') && <div><button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setFolio(true)}><Icon name="receipt_long" size={18} /> Open folio (payments, refunds)</button></div>}
            </div>
          )}
          {tab === 'requests' && <StayRequests stay={s} />}
          {tab === 'history' && <StayHistory id={s.id} />}
        </div>
      )}
      {s && dialog === 'check-in' && <CheckInModal stay={s} onClose={() => setDialog(null)} />}
      {s && dialog === 'check-out' && <CheckOutModal stay={s} onClose={() => setDialog(null)} />}
      {s && dialog === 'reschedule' && <RescheduleModal stay={s} onClose={() => setDialog(null)} />}
      {s && dialog === 'assign' && <AssignModal stay={s} onClose={() => setDialog(null)} />}
      {s && dialog === 'cancel' && <CancelModal stay={s} kind="cancel" onClose={() => setDialog(null)} />}
      {s && dialog === 'no-show' && <CancelModal stay={s} kind="no-show" onClose={() => setDialog(null)} />}
      {s && dialog === 'charge' && <ChargeModal stay={s} onClose={() => setDialog(null)} />}
      {s && dialog === 'addon' && <AddonModal stay={s} onClose={() => setDialog(null)} />}
      {s && dialog === 'request' && <RequestModal stay={s} onClose={() => setDialog(null)} />}
      {s && dialog === 'late' && <LateCheckoutModal stay={s} onClose={() => setDialog(null)} />}
      {s && dialog === 'edit' && <EditStayModal stay={s} onClose={() => setDialog(null)} />}
      {s && dialog === 'pay' && <PayModal stay={s} onClose={() => setDialog(null)} />}
      {s && dialog === 'move' && <MoveModal stay={s} onClose={() => setDialog(null)} />}
      {s && dialog === 'upgrade' && <UpgradeModal stay={s} onClose={() => setDialog(null)} />}
      {s && dialog === 'void' && <VoidModal stay={s} onClose={() => setDialog(null)} />}
      {folio && s?.folioId && <FolioDrawer id={s.folioId} onClose={() => setFolio(false)} />}
    </Drawer>
  );
}

function GuestLink({ s }: { s: Stay }) {
  const { can } = useAuth();
  const ops = useLocation().pathname.startsWith('/ops');
  if (s.customerId && can('stay.guest.view') && !ops) return <Link to={`/accommodation/guests/${s.customerId}`}>{guestOf(s)}</Link>;
  return <>{guestOf(s)}</>;
}

/** The actions of a reservation by its status and the user's permissions. */
export function StayActions({ s, onDialog, compact }: { s: Stay; onDialog: (d: string) => void; compact?: boolean }) {
  const { can } = useAuth();
  const toast = useToast();
  const confirm = useSend<{ id: string }>('POST', (b) => `/api/v1/stay/stays/${b.id}:confirm`, INV);
  const btn = (d: string, text: string, icon: string, kind = 'oc-btn-neutral') => (
    <button key={d} className={`oc-btn oc-btn-sm ${kind}`} onClick={() => onDialog(d)}><Icon name={icon} size={16} /> {text}</button>
  );
  const pre = (s.status === 'reserved' && s.bookingStatus !== 'expired') || s.status === 'requested';
  const items: React.ReactNode[] = [];
  if (s.status === 'requested' && can('stay.stay.update')) {
    items.push(<button key="confirm" className="oc-btn oc-btn-sm oc-btn-ink" disabled={confirm.isPending}
      onClick={() => confirm.mutate({ id: s.id }, { onSuccess: () => toast('Confirmed') })}>Confirm request</button>);
  }
  if (s.status === 'reserved' && can('stay.stay.check_in')) items.push(btn('check-in', 'Check in', 'login', 'oc-btn-ink'));
  if (s.status === 'checked_in' && can('stay.stay.check_out')) items.push(btn('check-out', 'Check out', 'logout', 'oc-btn-primary'));
  if (pre && can('stay.stay.update') && !compact) items.push(btn('assign', s.unitAssigned ? 'Change bungalow' : 'Assign bungalow', 'meeting_room'));
  if ((pre || s.status === 'checked_in') && can('stay.stay.reschedule') && !compact) items.push(btn('reschedule', s.status === 'checked_in' ? 'Change departure' : 'Reschedule', 'event_repeat'));
  if ((pre || s.status === 'checked_in') && s.folioId && can('billing.payment.create')) items.push(btn('pay', 'Payment / deposit', 'payments'));
  if ((pre || s.status === 'checked_in') && can('stay.stay.charge') && !compact) items.push(btn('charge', 'Charge to room', 'add_card'), btn('addon', 'Add-on', 'add_shopping_cart'));
  if ((pre || s.status === 'checked_in') && can('stay.guest_request.create') && !compact) items.push(btn('request', 'Guest request', 'support_agent'));
  if (s.status === 'checked_in' && can('stay.stay.update') && !compact) items.push(btn('late', 'Late check-out', 'more_time'));
  if (s.status === 'checked_in' && can('stay.stay.update') && !compact) items.push(btn('move', 'Room move', 'swap_horiz'));
  if (s.status === 'reserved' && can('stay.stay.update') && !compact) items.push(btn('upgrade', 'Upgrade', 'upgrade'));
  if ((pre || s.status === 'checked_in') && can('stay.stay.update') && !compact) items.push(btn('edit', 'Edit details', 'edit'));
  if (s.status === 'reserved' && can('stay.stay.update') && !compact) items.push(btn('no-show', 'No-show', 'person_off'));
  if (pre && can('stay.stay.cancel') && !compact) items.push(btn('cancel', 'Cancel', 'cancel', 'oc-btn-text'));
  if (pre && can('stay.stay.supervise') && !compact) items.push(btn('void', 'Void', 'block', 'oc-btn-text'));
  if (!items.length) return null;
  return <div className="oc-row-wrap">{items}<ErrorAlert error={confirm.error} /></div>;
}

function VoidAddon({ stay, addon }: { stay: string; addon: string }) {
  const send = useSend<Row>('POST', `/api/v1/stay/stays/${stay}/addons/${addon}:void`, INV);
  return <button className="oc-btn oc-btn-text oc-btn-sm" disabled={send.isPending} onClick={() => send.mutate({ reason: 'removed at the front office' })}>Remove</button>;
}

function StayRequests({ stay }: { stay: Stay }) {
  const list = useGet<Page<GuestRequest>>(`/api/v1/stay/guest-requests${qs({ stayId: stay.id, limit: 100 })}`);
  return (
    <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error} rowKey={(r) => r.id} empty={<Empty title="No requests" icon="support_agent" />}
      columns={[{ key: 'requestNo', header: 'Request', render: (r) => <>{r.requestNo}<div className="oc-small oc-muted">{formatDateTime(r.requestedAt)}</div></> },
        { key: 'requestType', header: 'Type', render: (r) => <>{label(r.requestType)} × {r.quantity}{r.description ? <div className="oc-small oc-muted">{r.description}</div> : null}</> },
        { key: 'status', header: 'Status', render: (r) => <StatusPill status={r.status.replace(/_/g, '-')} /> }]}
      actions={(r) => <RequestMove r={r} />} />
  );
}

/** The next step of a guest request. */
export function RequestMove({ r }: { r: GuestRequest }) {
  const { can } = useAuth();
  const toast = useToast();
  const [assigning, setAssigning] = useState(false);
  const [who, setWho] = useState('');
  const move = useSend<{ id: string; op: string; assignedTo?: string }>('POST', (b) => `/api/v1/stay/guest-requests/${b.id}:${b.op}`, INV);
  if (!can('stay.guest_request.work') || r.status === 'completed' || r.status === 'cancelled') return null;
  const run = (op: string, extra: Record<string, string> = {}) => move.mutate({ id: r.id, op, ...extra }, { onSuccess: () => toast(`${label(op)}: done`), onError: (e) => toast(e.message, 'error') });
  return (
    <div className="oc-row">
      {r.status === 'requested' && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setAssigning(true)}>Assign</button>}
      {r.status !== 'in_progress' && <button className="oc-btn oc-btn-neutral oc-btn-sm" disabled={move.isPending} onClick={() => run('start')}>Start</button>}
      <button className="oc-btn oc-btn-primary oc-btn-sm" disabled={move.isPending} onClick={() => run('complete')}>Done</button>
      <button className="oc-btn oc-btn-text oc-btn-sm" disabled={move.isPending} onClick={() => run('cancel')}>Cancel</button>
      {assigning && (
        <Modal open onClose={() => setAssigning(false)} title={`Assign ${r.requestNo}`} actions={<><button className="oc-btn oc-btn-neutral" onClick={() => setAssigning(false)}>Cancel</button>
          <button className="oc-btn oc-btn-ink" disabled={!who} onClick={() => { run('assign', { assignedTo: who }); setAssigning(false); }}>Assign</button></>}>
          <TextField label="Staff / team" value={who} onChange={setWho} placeholder="e.g. Housekeeping Sari" />
        </Modal>
      )}
    </div>
  );
}

interface HistoryItem { at: string; action: string; entity: string; label: string | null; actor: string | null; reason: string | null; source: string;
  changes: { field: string; before: unknown; after: unknown }[] }

/** The audit trail of a reservation (§33). */
export function StayHistory({ id }: { id: string }) {
  const h = useGet<Page<HistoryItem>>(`/api/v1/stay/stays/${id}/history`);
  const show = (v: unknown) => (v === null || v === undefined ? '—' : typeof v === 'object' ? JSON.stringify(v).slice(0, 80) : String(v));
  if (h.isLoading) return <Skeleton rows={6} />;
  if (!h.data?.items.length) return <Empty title="No history" icon="history" />;
  return (
    <ol className="acc-history">
      {h.data.items.map((e, i) => (
        <li key={i}>
          <div><strong>{label(e.action)}</strong> <span className="oc-muted oc-small">· {e.entity.replace(/^[a-z]+\./, '').replace(/_/g, ' ')} · {formatDateTime(e.at)}</span></div>
          <div className="oc-small oc-muted">{e.actor ?? 'system'}{e.reason ? ` — ${e.reason}` : ''}</div>
          {e.changes.filter((c) => !['folio', 'stay', 'reservation', 'lines', 'history'].includes(c.field)).slice(0, 6).map((c) => (
            <div key={c.field} className="oc-small"><code>{c.field}</code>: {show(c.before)} → <strong>{show(c.after)}</strong></div>
          ))}
        </li>
      ))}
    </ol>
  );
}

// ── dialogs ────────────────────────────────────────────────────────────────

function DialogActions({ onClose, onOk, ok, busy, disabled, danger }: { onClose: () => void; onOk: () => void; ok: string; busy?: boolean; disabled?: boolean; danger?: boolean }) {
  return (
    <>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button>
      <button className={`oc-btn ${danger ? 'oc-btn-danger' : 'oc-btn-ink'}`} disabled={busy || disabled} onClick={onOk}>{ok}</button>
    </>
  );
}

/** Units for an assignment with their warnings (§16). */
function UnitPicker({ stay, value, onChange }: { stay: Stay; value: string; onChange: (v: string) => void }) {
  const opts = useGet<Page<UnitOption>>(`/api/v1/stay/stays/${stay.id}/unit-options`);
  if (opts.isLoading) return <Skeleton rows={3} />;
  return (
    <div className="acc-units">
      {(opts.data?.items ?? []).filter((o) => o.sameType || !stay.unitTypeId).map((o) => (
        <button key={o.id} type="button" className="acc-unit" aria-pressed={value === o.id} data-warn={o.warnings.length > 0} onClick={() => onChange(o.id)}>
          <strong>{o.code}</strong> <span className="oc-small">{o.typeName}</span>
          <span className="oc-row-wrap" style={{ gap: 4 }}>
            <RoomStatus status={o.operationalStatus} />
            {o.current && <StatusPill status="current" label="Current" tone="info" />}
            {o.warnings.map((w) => <span key={w} className="acc-warn"><Icon name="warning" size={14} /> {label(w)}</span>)}
          </span>
        </button>
      ))}
    </div>
  );
}

export function AssignModal({ stay, onClose }: { stay: Stay; onClose: () => void }) {
  const toast = useToast();
  const [unit, setUnit] = useState(stay.unitAssigned ? stay.unitId : '');
  const send = useSend<Row>('POST', `/api/v1/stay/stays/${stay.id}:assign-unit`, INV);
  return (
    <Modal open wide onClose={onClose} title={`Assign bungalow · ${stay.stayNo}`}
      actions={<DialogActions onClose={onClose} ok="Assign" busy={send.isPending} disabled={!unit || unit === stay.unitId && stay.unitAssigned}
        onOk={() => send.mutate({ unitId: unit }, { onSuccess: () => { toast('Bungalow assigned'); onClose(); } })} />}>
      <p className="oc-muted oc-small">{stay.typeName} · {formatDate(stay.start)} – {formatDate(stay.end)} · {stay.adults + stay.children} guest(s). Warnings: occupied, maintenance, blocked, dirty or too small.</p>
      <UnitPicker stay={stay} value={unit} onChange={setUnit} />
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

const ID_TYPES = [{ value: 'ktp', label: 'KTP' }, { value: 'passport', label: 'Passport' }, { value: 'sim', label: 'SIM' }, { value: 'kitas', label: 'KITAS' },
  { value: 'other', label: 'Other' }];

/** Check-in (§17; docs/requirement-booking-hotel-mgcc.md FR-H47–H50): identity with nationality and photo, deposit, the bungalow, the registration
 * card signed on the tablet and the keys handed over. A refusal names the reason and the next step (FR-H98). */
export function CheckInModal({ stay, onClose }: { stay: Stay; onClose: () => void }) {
  const toast = useToast();
  const { can } = useAuth();
  const [v, setV] = useState({ idType: 'ktp', idNumber: '', nationality: stay.nationality ?? 'Indonesia', unitId: stay.unitAssigned ? stay.unitId : '',
    adults: String(stay.adults), children: String(stay.children), deposit: '', method: 'cash', notes: '', waiveEarlyFee: false, overrideStatus: false,
    keys: '2', keyNumbers: '', signature: '', reason: '' });
  const [done, setDone] = useState<StayResult | null>(null);
  const early = new Date(stay.start).getTime() > Date.now();
  const send = useSend<Row, StayResult>('POST', `/api/v1/stay/stays/${stay.id}:check-in`, INV);
  const due = Number(stay.totalDue) - Number(stay.paid);
  const supervisor = v.waiveEarlyFee || v.overrideStatus;
  const code = (send.error as { code?: string } | null)?.code;
  if (done) {
    return (
      <Modal open onClose={onClose} title={`Checked in · ${done.stay.unitName}`} actions={<button className="oc-btn oc-btn-ink" onClick={onClose}>Done</button>}>
        <p>{guestOf(done.stay)} is in {done.stay.unitName} until {formatDateTime(done.stay.end)}. Keys: {done.stay.keysIssued}.</p>
        <button className="oc-btn oc-btn-neutral" onClick={() => void openPdf(`/api/v1/stay/stays/${stay.id}/registration-card.pdf`, `registration-${stay.stayNo}.pdf`)}>
          <Icon name="print" size={18} /> Print the registration card</button>
      </Modal>
    );
  }
  return (
    <Modal open wide onClose={onClose} title={`Check in · ${guestOf(stay)} · ${stay.stayNo}`}
      actions={<DialogActions onClose={onClose} ok="Check in" busy={send.isPending} disabled={!v.idNumber || (supervisor && !v.reason)}
        onOk={() => send.mutate({ idType: v.idType, idNumber: v.idNumber, nationality: v.nationality || undefined, unitId: v.unitId || undefined,
          adults: Number(v.adults) || undefined, children: Number(v.children), notes: v.notes || undefined, waiveEarlyFee: v.waiveEarlyFee || undefined,
          overrideStatus: v.overrideStatus || undefined, supervisorReason: v.reason || undefined, signature: v.signature || undefined,
          keysIssued: Number(v.keys) || 0, keyNumbers: v.keyNumbers || undefined,
          deposit: v.deposit && Number(v.deposit) > 0 ? { methodType: v.method, amount: v.deposit } : undefined },
        { onSuccess: (r) => { toast('Checked in'); setDone(r); } })} />}>
      <div className="oc-stack">
        {early && <div className="oc-alert oc-alert-info"><Icon name="schedule" size={18} /> Early check-in: possible from the Stay Policies time when the bungalow is Ready; the early check-in fee goes to the folio.</div>}
        <strong>1 · Guest identity</strong>
        <div className="oc-form">
          <SelectField label="Identity" value={v.idType} onChange={(x) => setV({ ...v, idType: x })} options={ID_TYPES} />
          <TextField label="ID number" value={v.idNumber} onChange={(x) => setV({ ...v, idNumber: x })} required help="Stored masked (UU PDP)" />
          <TextField label="Nationality" value={v.nationality} onChange={(x) => setV({ ...v, nationality: x })} />
          <TextField label="Adults" type="number" value={v.adults} onChange={(x) => setV({ ...v, adults: x })} />
          <TextField label="Children" type="number" value={v.children} onChange={(x) => setV({ ...v, children: x })} />
        </div>
        <IdentityPhoto stay={stay} />
        <strong>2 · Deposit / guarantee</strong>
        <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
          <PaymentStatus status={stay.paymentStatus} /><span>Total {money(stay.totalDue)} · paid {money(stay.paid)} · open {money(Math.max(due, 0))}</span>
        </div>
        {can('billing.payment.create') && (
          <div className="oc-form">
            <MoneyField label="Deposit now" value={v.deposit} onChange={(x) => setV({ ...v, deposit: x })} placeholder="0" />
            <SelectField label="Method" value={v.method} onChange={(x) => setV({ ...v, method: x })} options={['cash', 'card', 'qris', 'bank_transfer'].map((m) => ({ value: m, label: label(m) }))} />
          </div>
        )}
        <strong>3 · Bungalow</strong>
        <UnitPicker stay={stay} value={v.unitId} onChange={(x) => setV({ ...v, unitId: x })} />
        <strong>4 · Registration card and keys</strong>
        <SignaturePad onChange={(x) => setV({ ...v, signature: x })} />
        <div className="oc-form">
          <TextField label="Keys / key cards handed over" type="number" value={v.keys} onChange={(x) => setV({ ...v, keys: x })} />
          <TextField label="Key numbers" value={v.keyNumbers} onChange={(x) => setV({ ...v, keyNumbers: x })} />
        </div>
        <TextArea label="Notes" rows={2} value={v.notes} onChange={(x) => setV({ ...v, notes: x })} />
        {can('stay.stay.supervise') && early && <Checkbox label="Waive the early check-in fee (supervisor)" checked={v.waiveEarlyFee} onChange={(x) => setV({ ...v, waiveEarlyFee: x })} />}
        {can('stay.stay.supervise') && <Checkbox label="Check in although the bungalow is not Ready (supervisor)" checked={v.overrideStatus} onChange={(x) => setV({ ...v, overrideStatus: x })} />}
        {(supervisor || code === 'deposit_required') && can('stay.stay.supervise') && <TextField label="Supervisor reason" value={v.reason} onChange={(x) => setV({ ...v, reason: x })} required />}
        {code === 'unit_not_ready' && <div className="oc-alert oc-alert-warning">Choose another Ready bungalow above, or ask housekeeping to finish this one (Housekeeping board).</div>}
        {code === 'deposit_required' && <div className="oc-alert oc-alert-warning">Take the deposit above, or a supervisor accepts it later with a reason.</div>}
        {code === 'too_early' && <div className="oc-alert oc-alert-warning">Too early for the early check-in: wait until the time of Stay Policies, or reschedule.</div>}
        <ErrorAlert error={send.error} />
      </div>
    </Modal>
  );
}

/** Check-out (§18; FR-H56–H59): folio, extra charges, keys back, payment on the payment screen (cash, card, QRIS, transfer, city ledger), invoice. */
export function CheckOutModal({ stay: s, onClose }: { stay: Stay; onClose: () => void }) {
  const toast = useToast();
  const { can } = useAuth();
  const [late, setLate] = useState(false);
  const [notes, setNotes] = useState('');
  const [method, setMethod] = useState('card');
  const [amount, setAmount] = useState('');
  const [keys, setKeys] = useState(String(s.keysIssued));
  const [overrideKeys, setOverrideKeys] = useState(false);
  const [reason, setReason] = useState('');
  const [charging, setCharging] = useState(false);
  const [done, setDone] = useState(false);
  const r = useGet<StayResult>(`/api/v1/stay/stays/${s.id}`);
  const stay = r.data?.stay ?? s;
  const balance = Number(stay.totalDue) - Number(stay.paid);
  const pay = useSend<Row>('POST', '/api/v1/billing/payments', INV);
  const company = useSend<Row>('POST', `/api/v1/billing/folios/${s.folioId}:charge-to-account`, INV);
  const out = useSend<Row>('POST', `/api/v1/stay/stays/${s.id}:check-out`, INV);
  const lines = (r.data?.folio?.lines ?? []).filter((l) => !l.voidedAt);
  const code = (out.error as { code?: string } | null)?.code;
  if (done) {
    return (
      <Modal open onClose={onClose} title={`Checked out · ${s.unitName}`} actions={<button className="oc-btn oc-btn-ink" onClick={onClose}>Done</button>}>
        <p>The folio is closed and {s.unitName} is Dirty with a cleaning task for housekeeping.</p>
        <button className="oc-btn oc-btn-neutral" onClick={() => void openPdf(`/api/v1/stay/stays/${s.id}/invoice.pdf`, `invoice-${s.stayNo}.pdf`)}><Icon name="print" size={18} /> Invoice / receipt</button>
      </Modal>
    );
  }
  const take = () => {
    const amt = amount || String(balance);
    if (method === 'city_ledger') {
      company.mutate({ corporateAccountId: stay.corporateAccountId, amount: amt }, { onSuccess: () => { toast('Charged to the company (city ledger)'); setAmount(''); void r.refetch(); } });
      return;
    }
    pay.mutate({ folioId: s.folioId, amount: amt, methodType: method, channel: method === 'member_account' ? 'member_account' : ['qris', 'virtual_account'].includes(method) ? 'online' : 'venue' },
      { onSuccess: () => { toast('Payment taken'); setAmount(''); void r.refetch(); } });
  };
  const methods = [['cash', 'Cash'], ['card', 'Card / EDC'], ['qris', 'QRIS'], ['bank_transfer', 'Transfer'], ['member_account', 'Member charge'],
    ...(stay.corporateAccountId ? [['city_ledger', `City ledger · ${stay.corporateName ?? 'company'}`]] : [])];
  return (
    <Modal open wide onClose={onClose} title={`Check out · ${guestOf(s)} · ${s.unitName}`}
      actions={<DialogActions onClose={onClose} ok="Check out" busy={out.isPending} disabled={(late || overrideKeys) && !reason}
        onOk={() => out.mutate({ waiveLateFee: late || undefined, notes: notes || undefined, keysReturned: stay.keysIssued ? Number(keys) || 0 : undefined,
          overrideKeys: overrideKeys || undefined, supervisorReason: reason || undefined }, { onSuccess: () => { toast('Checked out'); setDone(true); } })} />}>
      <div className="oc-stack">
        <strong>1 · Minibar and damage</strong>
        <div className="oc-row-wrap">
          {can('stay.stay.charge') && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setCharging(true)}><Icon name="add_card" size={16} /> Charge to room (minibar, damage …)</button>}
          {stay.lateCheckOutUntil && <span className="oc-small">Late check-out approved until {formatDateTime(stay.lateCheckOutUntil)}</span>}
        </div>
        <strong>2 · Folio</strong>
        <DataTable rows={lines as Row[]} rowKey={(l) => String(l.id)} empty={<span className="oc-muted">Only the room nights (posted at check-out).</span>} columns={[
          { key: 'description', header: 'Charge' }, { key: 'total', header: 'Total', align: 'right', render: (l) => money(l.total) }]} />
        <KV items={[['Total (with the nights posted at check-out)', <strong key="t">{money(stay.totalDue)}</strong>], ['Paid (deposits included)', money(stay.paid)],
          ['To pay', <strong key="b">{money(Math.max(balance, 0))}</strong>], ...(balance < 0 ? [['Deposit / overpayment to return', money(-balance)] as [string, React.ReactNode]] : [])]} />
        <strong>3 · Payment</strong>
        {balance > 0 && can('billing.payment.create') ? (
          <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
            <SelectField label="Method" value={method} onChange={setMethod} options={methods.map(([value, l]) => ({ value, label: l }))} />
            <MoneyField label="Amount" value={amount} onChange={setAmount} placeholder={String(balance)} />
            <button className="oc-btn oc-btn-primary" disabled={pay.isPending || company.isPending} onClick={take}>Take {money(amount || balance)}</button>
          </div>
        ) : balance > 0 ? <span className="oc-muted">A cashier takes the payment.</span> : <span className="oc-muted">The folio is settled.</span>}
        {stay.keysIssued > 0 && (
          <>
            <strong>4 · Keys</strong>
            <div className="oc-form">
              <TextField label={`Keys back (of ${stay.keysIssued})`} type="number" value={keys} onChange={setKeys} />
            </div>
          </>
        )}
        {can('stay.stay.supervise') && <Checkbox label="Waive the late check-out fee (supervisor)" checked={late} onChange={setLate} />}
        {(code === 'keys_outstanding' || overrideKeys) && can('stay.stay.supervise') && (
          <Checkbox label="Check out with keys missing (supervisor)" checked={overrideKeys} onChange={setOverrideKeys} />
        )}
        {(late || overrideKeys) && <TextField label="Supervisor reason" value={reason} onChange={setReason} required />}
        <p className="oc-small oc-muted" style={{ margin: 0 }}>Late check-out follows Stay Policies (grace, fee until the cut-off, one more night after it): the fee is added at check-out — take the payment again if one shows.</p>
        <TextArea label="Notes" rows={2} value={notes} onChange={setNotes} />
        {code === 'folio_unsettled' && <div className="oc-alert oc-alert-warning">Take the payment above first: the folio must be settled.</div>}
        <ErrorAlert error={pay.error ?? company.error ?? out.error} />
      </div>
      {charging && <ChargeModal stay={stay} onClose={() => { setCharging(false); void r.refetch(); }} />}
    </Modal>
  );
}

export function RescheduleModal({ stay, onClose }: { stay: Stay; onClose: () => void }) {
  const toast = useToast();
  const inHouse = stay.status === 'checked_in';
  const types = useGet<Page<Row>>('/api/v1/stay/bungalow-types?limit=100&filter[status]=active');
  const plans = useGet<Page<Row>>('/api/v1/stay/rate-plans?limit=100&filter[status]=active');
  const [v, setV] = useState({ arrival: stay.start.slice(0, 10), departure: stay.end.slice(0, 10), typeId: stay.unitTypeId ?? '', adults: String(stay.adults),
    children: String(stay.children), ratePlan: stay.ratePlan ?? '', reason: '' });
  const send = useSend<Row>('POST', `/api/v1/stay/stays/${stay.id}:reschedule`, INV);
  return (
    <Modal open onClose={onClose} title={inHouse ? `Change departure · ${stay.stayNo}` : `Reschedule · ${stay.stayNo}`}
      actions={<DialogActions onClose={onClose} ok="Save" busy={send.isPending} disabled={!v.reason}
        onOk={() => send.mutate(inHouse ? { departureDate: v.departure, reason: v.reason } : { arrivalDate: v.arrival, departureDate: v.departure,
          bungalowTypeId: v.typeId || undefined, adults: Number(v.adults) || undefined, children: Number(v.children), ratePlan: v.ratePlan || undefined, reason: v.reason },
        { onSuccess: (r) => { toast(`Saved — new total ${money((r as StayResult).total)}`); onClose(); } })} />}>
      <div className="oc-form">
        {!inHouse && <TextField label="Check-in" type="date" value={v.arrival} onChange={(x) => setV({ ...v, arrival: x })} />}
        <TextField label="Check-out" type="date" value={v.departure} onChange={(x) => setV({ ...v, departure: x })} />
        {!inHouse && <SelectField label="Room type" value={v.typeId} onChange={(x) => setV({ ...v, typeId: x })}
          options={(types.data?.items ?? []).map((t) => ({ value: String(t.id), label: String(t.name) }))} />}
        {!inHouse && <SelectField label="Rate plan" value={v.ratePlan} onChange={(x) => setV({ ...v, ratePlan: x })} placeholder="Keep"
          options={(plans.data?.items ?? []).map((p) => ({ value: String(p.code), label: String(p.name) }))} />}
        {!inHouse && <TextField label="Adults" type="number" value={v.adults} onChange={(x) => setV({ ...v, adults: x })} />}
        {!inHouse && <TextField label="Children" type="number" value={v.children} onChange={(x) => setV({ ...v, children: x })} />}
        <TextArea label="Reason" span rows={2} value={v.reason} onChange={(x) => setV({ ...v, reason: x })} required />
      </div>
      <p className="oc-small oc-muted">Availability is checked and the price recalculated with the rates of the new nights.</p>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

export function CancelModal({ stay, kind, onClose }: { stay: Stay; kind: 'cancel' | 'no-show'; onClose: () => void }) {
  const toast = useToast();
  const [reason, setReason] = useState('');
  const [waive, setWaive] = useState(false);
  const { can } = useAuth();
  const send = useSend<Row>('POST', `/api/v1/stay/stays/${stay.id}:${kind}`, INV);
  return (
    <Modal open onClose={onClose} title={kind === 'cancel' ? `Cancel ${stay.stayNo}` : `No-show ${stay.stayNo}`}
      actions={<DialogActions danger onClose={onClose} ok={kind === 'cancel' ? 'Cancel reservation' : 'Mark No-show'} busy={send.isPending} disabled={!reason}
        onOk={() => send.mutate(kind === 'cancel' ? { reason, waiveFee: waive || undefined } : { reason }, { onSuccess: () => { toast(kind === 'cancel' ? 'Cancelled' : 'Marked No-show'); onClose(); } })} />}>
      <p className="oc-muted">{kind === 'cancel'
        ? `The cancellation policy of the rate plan ${stay.ratePlan ?? ''} applies (free until the deadline, then the cancellation fee; non-refundable rates keep the stay). Payments above the fee are refunded.`
        : 'The no-show charge of the rate plan is posted, the bungalow released and the rest of the payments refunded.'}</p>
      <TextArea label="Reason" rows={2} value={reason} onChange={setReason} required />
      {kind === 'cancel' && can('stay.stay.cancel') && <Checkbox label="Waive the cancellation fee" checked={waive} onChange={setWaive} />}
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

export function ChargeModal({ stay, onClose }: { stay: Stay; onClose: () => void }) {
  const toast = useToast();
  const [v, setV] = useState({ category: 'minibar', description: '', unitPrice: '', quantity: '1', tax: false, serviceCharge: false, pricingMode: 'nett' });
  const send = useSend<Row>('POST', `/api/v1/stay/stays/${stay.id}:charge`, INV);
  return (
    <Modal open onClose={onClose} title={`Charge to room · ${stay.unitName}`}
      actions={<DialogActions onClose={onClose} ok="Charge" busy={send.isPending} disabled={!v.description || !v.unitPrice}
        onOk={() => send.mutate({ ...v, quantity: Number(v.quantity) || 1 }, { onSuccess: () => { toast('Charged to the folio'); onClose(); } })} />}>
      <div className="oc-form">
        <SelectField label="Category" value={v.category} onChange={(x) => setV({ ...v, category: x })}
          options={['fnb', 'minibar', 'laundry', 'transportation', 'damage', 'extra_bed', 'other'].map((c) => ({ value: c, label: c === 'fnb' ? 'Food & Beverage' : label(c) }))} />
        <TextField label="Description" value={v.description} onChange={(x) => setV({ ...v, description: x })} />
        <MoneyField label="Unit price" value={v.unitPrice} onChange={(x) => setV({ ...v, unitPrice: x })} />
        <TextField label="Quantity" type="number" value={v.quantity} onChange={(x) => setV({ ...v, quantity: x })} />
        <SelectField label="Price" value={v.pricingMode} onChange={(x) => setV({ ...v, pricingMode: x })}
          options={[{ value: 'nett', label: 'Includes tax & service (nett)' }, { value: 'plus_plus', label: 'Before tax & service (++)' }]} />
      </div>
      <div className="oc-row-wrap">
        <Checkbox label="Tax" checked={v.tax} onChange={(x) => setV({ ...v, tax: x })} />
        <Checkbox label="Service charge" checked={v.serviceCharge} onChange={(x) => setV({ ...v, serviceCharge: x })} />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

export function AddonModal({ stay, onClose }: { stay: Stay; onClose: () => void }) {
  const toast = useToast();
  const addons = useGet<Page<Row>>('/api/v1/stay/addons?limit=100&filter[status]=active');
  const [v, setV] = useState({ addonId: '', quantity: '1' });
  const send = useSend<Row>('POST', `/api/v1/stay/stays/${stay.id}/addons`, INV);
  const list = (addons.data?.items ?? []).filter((a) => stay.status !== 'checked_in' || a.availability !== 'booking');
  return (
    <Modal open onClose={onClose} title={`Add-on · ${stay.stayNo}`}
      actions={<DialogActions onClose={onClose} ok="Add" busy={send.isPending} disabled={!v.addonId}
        onOk={() => send.mutate({ addonId: v.addonId, quantity: Number(v.quantity) || 1 }, { onSuccess: () => { toast('Add-on charged'); onClose(); } })} />}>
      <div className="oc-form">
        <SelectField label="Add-on" value={v.addonId} onChange={(x) => setV({ ...v, addonId: x })}
          options={list.map((a) => ({ value: String(a.id), label: `${a.name} · ${money(a.price)} ${label(String(a.unit)).toLowerCase()}` }))} />
        <TextField label="Quantity" type="number" value={v.quantity} onChange={(x) => setV({ ...v, quantity: x })} />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

export function RequestModal({ stay, onClose }: { stay: Stay; onClose: () => void }) {
  const toast = useToast();
  const addons = useGet<Page<Row>>('/api/v1/stay/addons?limit=100&filter[status]=active');
  const [v, setV] = useState({ requestType: 'extra_towel', description: '', quantity: '1', addonId: '', chargeAmount: '', assignedTo: '', source: 'front_desk' });
  const send = useSend<Row>('POST', '/api/v1/stay/guest-requests', INV);
  return (
    <Modal open onClose={onClose} title={`Guest request · ${stay.unitName} · ${guestOf(stay)}`}
      actions={<DialogActions onClose={onClose} ok="Save request" busy={send.isPending}
        onOk={() => send.mutate({ stayId: stay.id, requestType: v.requestType, description: v.description || undefined, quantity: Number(v.quantity) || 1,
          addonId: v.addonId || undefined, chargeAmount: v.chargeAmount || undefined, assignedTo: v.assignedTo || undefined, source: v.source },
        { onSuccess: () => { toast('Request saved'); onClose(); } })} />}>
      <div className="acc-chips">
        {REQUEST_TYPES.map(([k, l, icon]) => (
          <button key={k} type="button" className="oc-chip" aria-pressed={v.requestType === k} onClick={() => setV({ ...v, requestType: k })}><Icon name={icon} size={16} /> {l}</button>
        ))}
      </div>
      <div className="oc-form">
        <TextField label="Quantity" type="number" value={v.quantity} onChange={(x) => setV({ ...v, quantity: x })} />
        <SelectField label="Source" value={v.source} onChange={(x) => setV({ ...v, source: x })} options={[{ value: 'front_desk', label: 'Front desk' }, { value: 'phone', label: 'Phone' }]} />
        <SelectField label="Paid service (add-on)" value={v.addonId} onChange={(x) => setV({ ...v, addonId: x })} placeholder="Free"
          options={(addons.data?.items ?? []).filter((a) => a.availability !== 'booking').map((a) => ({ value: String(a.id), label: `${a.name} · ${money(a.price)}` }))} />
        {!v.addonId && <MoneyField label="Or a charge amount" value={v.chargeAmount} onChange={(x) => setV({ ...v, chargeAmount: x })} placeholder="0 = free" />}
        <TextField label="Assign to" value={v.assignedTo} onChange={(x) => setV({ ...v, assignedTo: x })} />
        <TextArea label="Description" span rows={2} value={v.description} onChange={(x) => setV({ ...v, description: x })} />
      </div>
      <p className="oc-small oc-muted">Room cleaning becomes a housekeeping task, maintenance a work order; a paid request is charged to the folio when completed.</p>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

export function LateCheckoutModal({ stay, onClose }: { stay: Stay; onClose: () => void }) {
  const toast = useToast();
  const [until, setUntil] = useState('14:00');
  const [reason, setReason] = useState('');
  const send = useSend<Row>('POST', `/api/v1/stay/stays/${stay.id}:late-checkout`, INV);
  return (
    <Modal open onClose={onClose} title={`Late check-out · ${stay.unitName}`}
      actions={<DialogActions onClose={onClose} ok="Approve" busy={send.isPending}
        onOk={() => send.mutate({ until, reason: reason || undefined }, { onSuccess: () => { toast('Late check-out approved'); onClose(); } })} />}>
      <p className="oc-muted">Check-out is {formatDateTime(stay.end)}. The bungalow is kept for the guest (checked against the next arrival); the late check-out fee of Stay Policies is charged at check-out.</p>
      <div className="oc-form">
        <TextField label="Until" type="time" value={until} onChange={setUntil} />
        <TextField label="Reason" value={reason} onChange={setReason} />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

export function EditStayModal({ stay, onClose }: { stay: Stay; onClose: () => void }) {
  const toast = useToast();
  const [v, setV] = useState({ guestName: stay.guestName ?? '', guestPhone: stay.guestPhone ?? '', guestEmail: stay.guestEmail ?? '',
    specialRequests: stay.specialRequests ?? '', notes: stay.notes ?? '', expectedArrival: stay.expectedArrival ?? '', vip: stay.vip, bookingSource: stay.bookingSource ?? 'front_desk' });
  const send = useSend<Row>('POST', `/api/v1/stay/stays/${stay.id}:update`, INV);
  return (
    <Modal open wide onClose={onClose} title={`Edit ${stay.stayNo}`}
      actions={<DialogActions onClose={onClose} ok="Save" busy={send.isPending} onOk={() => send.mutate(v, { onSuccess: () => { toast('Saved'); onClose(); } })} />}>
      <div className="oc-form">
        {!stay.customerId && <TextField label="Guest name" value={v.guestName} onChange={(x) => setV({ ...v, guestName: x })} />}
        <TextField label="Phone" value={v.guestPhone} onChange={(x) => setV({ ...v, guestPhone: x })} />
        <TextField label="E-mail" value={v.guestEmail} onChange={(x) => setV({ ...v, guestEmail: x })} />
        <TextField label="Expected arrival" type="time" value={v.expectedArrival} onChange={(x) => setV({ ...v, expectedArrival: x })} />
        <SelectField label="Booking source" value={v.bookingSource} onChange={(x) => setV({ ...v, bookingSource: x })} options={SOURCES.map(([value, l]) => ({ value, label: l }))} />
        <TextArea label="Special request" span rows={2} value={v.specialRequests} onChange={(x) => setV({ ...v, specialRequests: x })} />
        <TextArea label="Internal notes" span rows={2} value={v.notes} onChange={(x) => setV({ ...v, notes: x })} />
      </div>
      <Checkbox label="VIP / priority guest" checked={v.vip} onChange={(x) => setV({ ...v, vip: x })} />
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

/** Payment or deposit on the stay folio (§25). */
export function PayModal({ stay, onClose }: { stay: Stay; onClose: () => void }) {
  const toast = useToast();
  const open = Math.max(Number(stay.totalDue) - Number(stay.paid), 0);
  const [v, setV] = useState({ purpose: stay.status === 'checked_in' ? 'settlement' : 'deposit', amount: String(open), methodType: 'bank_transfer', reference: '' });
  const send = useSend<Row>('POST', '/api/v1/billing/payments', INV);
  return (
    <Modal open onClose={onClose} title={`Payment · ${stay.stayNo}`}
      actions={<DialogActions onClose={onClose} ok="Take payment" busy={send.isPending} disabled={!Number(v.amount)}
        onOk={() => send.mutate({ folioId: stay.folioId, purpose: v.purpose, amount: v.amount, methodType: v.methodType, reference: v.reference || undefined,
          channel: ['qris', 'virtual_account'].includes(v.methodType) ? 'online' : 'venue' }, { onSuccess: () => { toast('Payment recorded'); onClose(); } })} />}>
      <p className="oc-muted">Total {money(stay.totalDue)} · paid {money(stay.paid)} · open {money(open)}. A deposit is held until check-out; refunds are made from the folio.</p>
      <div className="oc-form">
        <SelectField label="Purpose" value={v.purpose} onChange={(x) => setV({ ...v, purpose: x })} options={[{ value: 'deposit', label: 'Deposit' }, { value: 'settlement', label: 'Payment' }]} />
        <MoneyField label="Amount" value={v.amount} onChange={(x) => setV({ ...v, amount: x })} />
        <SelectField label="Method" value={v.methodType} onChange={(x) => setV({ ...v, methodType: x })}
          options={['cash', 'card', 'bank_transfer', 'qris', 'virtual_account'].map((m) => ({ value: m, label: label(m) }))} />
        <TextField label="Reference" value={v.reference} onChange={(x) => setV({ ...v, reference: x })} />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

/** Reservation rows of a table. */
export function useStayColumns(opts: { unit?: boolean; payment?: boolean; dates?: boolean } = {}) {
  return useMemo(() => [
    { key: 'stayNo', header: 'Reservation', render: (s: Stay) => <><strong>{s.stayNo}</strong>{s.vip ? <span className="acc-vip">VIP</span> : null}<div className="oc-small oc-muted">{sourceLabel(s.bookingSource)}</div></> },
    { key: 'guest', header: 'Guest', render: (s: Stay) => <>{guestOf(s)}<div className="oc-small oc-muted">{s.adults + s.children} guest(s){s.corporateName && s.customerName ? ` · ${s.corporateName}` : ''}</div></> },
    ...(opts.unit === false ? [] : [{ key: 'unit', header: 'Bungalow', render: (s: Stay) => <>{s.unitAssigned ? s.unitCode ?? s.unitName : <span className="oc-muted">—</span>}<div className="oc-small oc-muted">{s.typeName}</div></> }]),
    ...(opts.dates === false ? [] : [{ key: 'start', header: 'Stay', render: (s: Stay) => <>{dayOf(s.start)} → {dayOf(s.end)}<div className="oc-small oc-muted">{s.nights} night(s) · {s.ratePlan}</div></> }]),
    { key: 'totalDue', header: 'Total', align: 'right' as const, render: (s: Stay) => money(s.totalDue) },
    ...(opts.payment === false ? [] : [{ key: 'paymentStatus', header: 'Payment', render: (s: Stay) => <PaymentStatus status={s.paymentStatus} /> }]),
    { key: 'status', header: 'Status', render: (s: Stay) => <StayStatus s={s} /> },
  ], [opts.unit, opts.payment, opts.dates]);
}
