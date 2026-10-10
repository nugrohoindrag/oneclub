import React, { useState } from 'react';
import { qs, useGet, type Page } from '@oneclub/api-client';
import { AutoResourcePage, DataTable, Empty, ErrorAlert, PageHeader, RequirePermission, useAuth } from '@oneclub/shell';
import { FolioDrawer } from '../p1/business';
import { Tabs } from '../p1/common';
import { AccommodationOverviewPage } from './overview';
import { NewReservationPage, ReservationsPage } from './reservations';
import { FrontOfficePage, RequestsPage } from './frontoffice';
import { BungalowsPage, RoomRackPage, RoomStatusBoard } from './rooms';
import { HousekeepingPage } from './housekeeping';
import { StayMaintenancePage } from './maintenance';
import { GuestProfilePage, GuestsPage, WaitlistPage } from './guests';
import { RatesPage } from './rates';
import { AccommodationReportsPage } from './reports';
import { GroupDetailPage, GroupsPage, IncidentsPage, NewGroupPage, NightAuditPage, RateCheckPage, StayCashierPage } from './mgcc';
import { PaymentStatus, StayStatus, dayOf, guestOf, money, type Stay } from './shared';

// Routes of the Accommodation module (requirements §2): Back Office
// /accommodation/… and the Ops shell (Stay Front Desk, Housekeeping,
// Maintenance).

const rp = (perm: string, el: React.ReactNode) => <RequirePermission perm={perm}>{el}</RequirePermission>;

/** Billing (§24, §25): the folios of the stays with what is still to pay. */
function StayBillingPage() {
  const { can } = useAuth();
  const [tab, setTab] = useState('checked_in');
  const [folio, setFolio] = useState<string | null>(null);
  const list = useGet<Page<Stay>>(`/api/v1/stay/reservations${qs({ tab, limit: 200 })}`);
  const rows = (list.data?.items ?? []).filter((s) => tab !== 'checked_out' || Number(s.totalDue) - Number(s.paid) !== 0);
  return (
    <div className="oc-stack">
      <PageHeader title="Accommodation Billing" help="Guest folios of the stays: room nights, add-ons, services, charges, payments, deposits and refunds. Open a folio to take a payment or refund." />
      <Tabs value={tab} onChange={setTab} tabs={[{ value: 'checked_in', label: 'In-house' }, { value: 'pending_payment', label: 'Pending payment' },
        { value: 'confirmed', label: 'Confirmed (deposits)' }, { value: 'balance', label: 'Balance ≠ 0' }, { value: 'checked_out', label: 'Checked-out with a balance' }]} />
      <ErrorAlert error={list.error} />
      <div className="oc-card">
        <DataTable rows={rows} loading={list.isLoading} rowKey={(s) => s.id} onRowClick={(s) => s.folioId && can('billing.folio.view') && setFolio(s.folioId)}
          empty={<Empty title="Nothing here" icon="receipt_long" />} columns={[
            { key: 'stayNo', header: 'Reservation', render: (s) => <><strong>{s.stayNo}</strong><div className="oc-small oc-muted">{s.unitCode ?? s.typeName}</div></> },
            { key: 'guest', header: 'Guest', render: (s) => <>{guestOf(s)}{s.corporateName && s.customerName ? <div className="oc-small oc-muted">{s.corporateName}</div> : null}</> },
            { key: 'dates', header: 'Stay', render: (s) => `${dayOf(s.start)} → ${dayOf(s.end)}` },
            { key: 'total', header: 'Total', align: 'right', render: (s) => money(s.totalDue) }, { key: 'paid', header: 'Paid', align: 'right', render: (s) => money(s.paid) },
            { key: 'balance', header: 'Balance', align: 'right', render: (s) => <strong>{money(Number(s.totalDue) - Number(s.paid))}</strong> },
            { key: 'pay', header: 'Payment', render: (s) => <PaymentStatus status={s.paymentStatus} /> }, { key: 'status', header: 'Status', render: (s) => <StayStatus s={s} /> }]} />
      </div>
      {folio && <FolioDrawer id={folio} onClose={() => setFolio(null)} />}
    </div>
  );
}

function RoomsOpsPage() {
  return <div className="oc-stack"><PageHeader title="Room Status" help="Housekeeping, operational and reservation status of every bungalow." /><RoomStatusBoard /></div>;
}

/** Back Office routes (Accommodation menu). */
export const ACCOMMODATION_ROUTES = [
  { path: 'accommodation', element: rp('stay.dashboard.view', <AccommodationOverviewPage />) },
  { path: 'accommodation/overview', element: rp('stay.dashboard.view', <AccommodationOverviewPage />) },
  { path: 'accommodation/reservations', element: rp('stay.stay.view', <ReservationsPage />) },
  { path: 'accommodation/reservations/new', element: rp('stay.stay.create', <NewReservationPage />) },
  { path: 'accommodation/front-office', element: rp('stay.stay.view', <FrontOfficePage />) },
  { path: 'accommodation/room-rack', element: rp('stay.room_status.view', <RoomRackPage />) },
  { path: 'accommodation/bungalows', element: rp('stay.room_status.view', <BungalowsPage />) },
  { path: 'accommodation/bungalows/types', element: rp('stay.bungalow_type.view', <AutoResourcePage resourceKey="stay.bungalow_type" />) },
  { path: 'accommodation/bungalows/units', element: rp('stay.bungalow.view', <AutoResourcePage resourceKey="stay.bungalow" />) },
  { path: 'accommodation/housekeeping', element: rp('stay.housekeeping.view', <HousekeepingPage />) },
  { path: 'accommodation/maintenance', element: rp('stay.work_order.view', <StayMaintenancePage />) },
  { path: 'accommodation/guests', element: rp('stay.guest.view', <GuestsPage />) },
  { path: 'accommodation/guests/:id', element: rp('stay.guest.view', <GuestProfilePage />) },
  { path: 'accommodation/requests', element: rp('stay.guest_request.view', <RequestsPage />) },
  { path: 'accommodation/waitlist', element: rp('stay.waitlist.view', <WaitlistPage />) },
  { path: 'accommodation/rates', element: rp('stay.rate_plan.view', <RatesPage />) },
  { path: 'accommodation/billing', element: rp('stay.stay.view', <StayBillingPage />) },
  { path: 'accommodation/reports', element: rp('stay.dashboard.view', <AccommodationReportsPage />) },
  { path: 'accommodation/groups', element: rp('stay.stay.view', <GroupsPage />) },
  { path: 'accommodation/groups/new', element: rp('stay.stay.create', <NewGroupPage />) },
  { path: 'accommodation/groups/:id', element: rp('stay.stay.view', <GroupDetailPage />) },
  { path: 'accommodation/restrictions', element: rp('stay.rate_restriction.view', <AutoResourcePage resourceKey="stay.rate_restriction" />) },
  { path: 'accommodation/rate-check', element: rp('stay.stay.view', <RateCheckPage />) },
  { path: 'accommodation/night-audit', element: rp('stay.night_audit.view', <NightAuditPage />) },
  { path: 'accommodation/incidents', element: rp('stay.incident.view', <IncidentsPage />) },
];

/** Ops routes (Stay Front Desk, Housekeeping, Maintenance). */
export const ACCOMMODATION_OPS_ROUTES = [
  { path: 'stay-desk', element: <FrontOfficePage ops /> },
  { path: 'stay-desk/new', element: <NewReservationPage ops /> },
  { path: 'stay-desk/reservations', element: <ReservationsPage ops /> },
  { path: 'stay-desk/requests', element: <RequestsPage /> },
  { path: 'stay-desk/rooms', element: <RoomsOpsPage /> },
  { path: 'stay-desk/waitlist', element: <WaitlistPage /> },
  { path: 'stay-desk/cashier', element: rp('billing.cashier_shift.operate', <StayCashierPage />) },
  { path: 'stay-desk/groups', element: rp('stay.stay.view', <GroupsPage ops />) },
  { path: 'stay-desk/groups/new', element: rp('stay.stay.create', <NewGroupPage ops />) },
  { path: 'stay-desk/groups/:id', element: rp('stay.stay.view', <GroupDetailPage ops />) },
  { path: 'stay-desk/incidents', element: rp('stay.incident.view', <IncidentsPage />) },
  { path: 'stay-desk/night-audit', element: rp('stay.night_audit.view', <NightAuditPage />) },
  { path: 'housekeeping', element: <HousekeepingPage ops /> },
  { path: 'stay-maintenance', element: <StayMaintenancePage ops /> },
];
