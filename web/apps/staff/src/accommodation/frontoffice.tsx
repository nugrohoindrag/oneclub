import React, { useState } from 'react';
import { Link, useSearchParams } from 'react-router';
import { qs, useGet, type Page } from '@oneclub/api-client';
import { formatDateTime } from '@oneclub/i18n';
import { DataTable, Empty, ErrorAlert, Icon, PageHeader, StatTile, StatusPill, TextField, useAuth } from '@oneclub/shell';
import { Tabs } from '../p1/common';
import {
  CheckInModal, CheckOutModal, timeOf as formatTime, OCCUPANCY, PaymentStatus, RequestModal, RequestMove, RoomStatus, StayDrawer, guestOf, label, money, todayISO,
  type GuestRequest, type RoomState, type Stay,
} from './shared';
import { RoomStatusBoard } from './rooms';
import './accommodation.css';

// Front Office (requirements §15–§18): today's arrivals with the
// preparation status of their bungalow, departures, in-house guests, the
// room status and the guest requests. Used by the Back Office
// (Accommodation › Front Office) and the Ops shell (Stay Front Desk).

interface FrontOffice { date: string; arrivals: Stay[]; departures: Stay[]; inHouse: Stay[]; counts: Record<string, number>; rooms: Record<string, number> }

export function FrontOfficePage({ ops }: { ops?: boolean }) {
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const tab = params.get('tab') ?? 'arrivals';
  const [date, setDate] = useState(todayISO());
  const [open, setOpen] = useState<string | null>(null);
  const [dialog, setDialog] = useState<{ kind: string; stay: Stay } | null>(null);
  const fo = useGet<FrontOffice>(`/api/v1/stay/front-office${qs({ date })}`, { refetchInterval: 30_000 });
  const rooms = useGet<Page<RoomState>>(`/api/v1/stay/room-status${qs({ date })}`);
  const ready = new Map((rooms.data?.items ?? []).map((r) => [r.id, r]));
  const x = fo.data;
  const setTab = (v: string) => { const n = new URLSearchParams(params); n.set('tab', v); setParams(n, { replace: true }); };
  const newLink = ops ? '/ops/stay-desk/new' : '/accommodation/reservations/new';
  const act = (s: Stay) => (
    <div className="oc-row">
      {s.status === 'reserved' && can('stay.stay.check_in') && <button className="oc-btn oc-btn-ink oc-btn-sm" onClick={(e) => { e.stopPropagation(); setDialog({ kind: 'in', stay: s }); }}>Check in</button>}
      {s.status === 'checked_in' && can('stay.stay.check_out') && <button className="oc-btn oc-btn-primary oc-btn-sm" onClick={(e) => { e.stopPropagation(); setDialog({ kind: 'out', stay: s }); }}>Check out</button>}
      {s.status === 'checked_in' && can('stay.guest_request.create') && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={(e) => { e.stopPropagation(); setDialog({ kind: 'req', stay: s }); }}>Request</button>}
    </div>
  );
  const prep = (s: Stay) => {
    if (!s.unitAssigned) return <StatusPill status="unassigned" label="No bungalow" tone="warning" />;
    const r = ready.get(s.unitId);
    return r ? <RoomStatus status={r.operationalStatus} /> : '—';
  };
  return (
    <div className="oc-stack">
      <PageHeader title="Front Office" help="Arrivals, departures and in-house guests of the day; check in and out, room assignment and guest requests."
        actions={can('stay.stay.create') ? <Link className="oc-btn oc-btn-primary" to={newLink}><Icon name="add" size={18} /> Walk-in / new reservation</Link> : undefined} />
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <TextField label="Date" type="date" value={date} onChange={setDate} />
        {x && (
          <>
            <StatTile label="Arrivals" value={x.counts.arrivals ?? 0} icon="flight_land" />
            <StatTile label="Departures" value={x.counts.departures ?? 0} icon="flight_takeoff" />
            <StatTile label="In-house" value={`${x.counts.inHouse ?? 0} · ${x.counts.inHouseGuests ?? 0} guests`} icon="hotel" />
            <StatTile label="Pending payment" value={x.counts.pendingPayment ?? 0} icon="payments" />
            <StatTile label="Room preparation" value={x.counts.pendingPreparation ?? 0} icon="cleaning_services" />
          </>
        )}
      </div>
      <Tabs value={tab} onChange={setTab} tabs={[{ value: 'arrivals', label: "Today's Arrival" }, { value: 'departures', label: "Today's Departure" },
        { value: 'in_house', label: 'In-house Guests' }, { value: 'rooms', label: 'Room Status' }, { value: 'requests', label: 'Guest Requests' }]} />
      <ErrorAlert error={fo.error} />
      {tab === 'arrivals' && (
        <div className="oc-card">
          <DataTable rows={x?.arrivals} loading={fo.isLoading} rowKey={(s) => s.id} onRowClick={(s) => setOpen(s.id)} inlineActions actions={act}
            empty={<Empty title="No arrivals" icon="flight_land" />} columns={[
              { key: 'guest', header: 'Guest', render: (s) => <>{s.vip && <span className="acc-vip">VIP</span>}<strong>{guestOf(s)}</strong><div className="oc-small oc-muted">{s.stayNo} · {s.guestPhone ?? ''}</div></> },
              { key: 'type', header: 'Room type / bungalow', render: (s) => <>{s.typeName}<div className="oc-small oc-muted">{s.unitAssigned ? s.unitName : 'not assigned'}</div></> },
              { key: 'guests', header: 'Guests', render: (s) => `${s.adults}${s.children ? ` + ${s.children}` : ''}` },
              { key: 'time', header: 'Check-in', render: (s) => <>{formatTime(s.start)}{s.expectedArrival ? <div className="oc-small oc-muted">ETA {s.expectedArrival}</div> : null}</> },
              { key: 'pay', header: 'Payment', render: (s) => <PaymentStatus status={s.paymentStatus} /> },
              { key: 'req', header: 'Special request', render: (s) => s.specialRequests ?? '—' },
              { key: 'prep', header: 'Preparation', render: prep }]} />
        </div>
      )}
      {tab === 'departures' && (
        <div className="oc-card">
          <DataTable rows={x?.departures} loading={fo.isLoading} rowKey={(s) => s.id} onRowClick={(s) => setOpen(s.id)} inlineActions actions={act}
            empty={<Empty title="No departures" icon="flight_takeoff" />} columns={[
              { key: 'unit', header: 'Bungalow', render: (s) => <strong>{s.unitCode ?? s.unitName}</strong> },
              { key: 'guest', header: 'Guest', render: (s) => <>{guestOf(s)}<div className="oc-small oc-muted">{s.stayNo}</div></> },
              { key: 'end', header: 'Check-out', render: (s) => <>{formatDateTime(s.end)}{s.lateCheckOutUntil ? <div className="oc-small">late until {formatTime(s.lateCheckOutUntil)}</div> : null}</> },
              { key: 'due', header: 'Balance', align: 'right', render: (s) => money(Math.max(Number(s.totalDue) - Number(s.paid), 0)) },
              { key: 'pay', header: 'Payment', render: (s) => <PaymentStatus status={s.paymentStatus} /> }]} />
        </div>
      )}
      {tab === 'in_house' && (
        <div className="oc-card">
          <DataTable rows={x?.inHouse} loading={fo.isLoading} rowKey={(s) => s.id} onRowClick={(s) => setOpen(s.id)} inlineActions actions={act}
            empty={<Empty title="No in-house guests" icon="hotel" />} columns={[
              { key: 'unit', header: 'Bungalow', render: (s) => <strong>{s.unitCode ?? s.unitName}</strong> },
              { key: 'guest', header: 'Guest', render: (s) => <>{s.vip && <span className="acc-vip">VIP</span>}{guestOf(s)}<div className="oc-small oc-muted">{s.adults + s.children} guest(s) · {s.stayNo}</div></> },
              { key: 'dates', header: 'Stay', render: (s) => <>{formatDateTime(s.checkedInAt ?? s.start)} → {formatDateTime(s.end)}</> },
              { key: 'due', header: 'Folio', align: 'right', render: (s) => money(s.totalDue) },
              { key: 'pay', header: 'Payment', render: (s) => <PaymentStatus status={s.paymentStatus} /> }]} />
        </div>
      )}
      {tab === 'rooms' && <RoomStatusBoard date={date} onOpenStay={setOpen} />}
      {tab === 'requests' && <RequestsBoard />}
      {open && <StayDrawer id={open} onClose={() => setOpen(null)} />}
      {dialog?.kind === 'in' && <CheckInModal stay={dialog.stay} onClose={() => setDialog(null)} />}
      {dialog?.kind === 'out' && <CheckOutModal stay={dialog.stay} onClose={() => setDialog(null)} />}
      {dialog?.kind === 'req' && <RequestModal stay={dialog.stay} onClose={() => setDialog(null)} />}
      {x && <p className="oc-small oc-muted">Room status: {Object.entries(x.rooms).map(([k, n]) => `${label(k)} ${n}`).join(' · ')}. {OCCUPANCY.available[0]} bungalows can be sold.</p>}
    </div>
  );
}

/** Guest requests (§23): open first, then completed. */
export function RequestsBoard() {
  const [status, setStatus] = useState('open');
  const list = useGet<Page<GuestRequest>>(`/api/v1/stay/guest-requests${qs({ status: status === 'all' ? '' : status, limit: 200 })}`, { refetchInterval: 30_000 });
  return (
    <div className="oc-stack">
      <Tabs value={status} onChange={setStatus} tabs={[{ value: 'open', label: 'Open' }, { value: 'completed', label: 'Completed' }, { value: 'all', label: 'All' }]} />
      <ErrorAlert error={list.error} />
      <div className="oc-card">
        <DataTable rows={list.data?.items} loading={list.isLoading} rowKey={(r) => r.id} inlineActions actions={(r) => <RequestMove r={r} />}
          empty={<Empty title="No requests" icon="support_agent" />} columns={[
            { key: 'requestNo', header: 'Request', render: (r) => <><strong>{r.requestNo}</strong><div className="oc-small oc-muted">{formatDateTime(r.requestedAt)} · {label(r.source)}</div></> },
            { key: 'where', header: 'Bungalow / guest', render: (r) => <>{r.bungalowCode ?? '—'}<div className="oc-small oc-muted">{r.guest ?? r.stayNo}</div></> },
            { key: 'type', header: 'Request', render: (r) => <>{label(r.requestType)} × {r.quantity}{r.description ? <div className="oc-small oc-muted">{r.description}</div> : null}</> },
            { key: 'charge', header: 'Charge', render: (r) => (r.addonName ? r.addonName : r.chargeAmount ? money(r.chargeAmount) : 'Free') },
            { key: 'assigned', header: 'Assigned', render: (r) => r.assignedTo ?? '—' },
            { key: 'status', header: 'Status', render: (r) => <>{<StatusPill status={r.status.replace(/_/g, '-')} />}{r.minutesToComplete != null ? <div className="oc-small oc-muted">{r.minutesToComplete} min</div> : null}</> }]} />
      </div>
    </div>
  );
}

export function RequestsPage() {
  return (
    <div className="oc-stack">
      <PageHeader title="Guest Requests" help="Extra towel, extra bed, cleaning, maintenance, laundry, transportation, food & beverage: Requested → Assigned → In Progress → Completed." />
      <RequestsBoard />
    </div>
  );
}
