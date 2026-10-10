import React from 'react';
import { RequirePermission } from '@oneclub/shell';
import { CashierPage } from '../p3/billing';
import { SportPackagesPage, SportTicketsPage } from '../ops/sport';
import {
  SportBlocksPage, SportCalendarPage, SportOverviewPage, SportPackagesAdminPage, SportRecurringPage, SportReportsPage, SportReservationsPage,
} from './admin';
import {
  CourtBoardPage, CourtCheckInPage, CourtFinishPage, CourtHistoryPage, CourtPaymentsPage, CourtStaffPage, NewCourtBookingPage, SportIncidentsPage,
} from './desk';
import {
  ContentSettingsPage, CourtsSettingsPage, HoursSettingsPage, PaymentSettingsPage, RatesSettingsPage, RulesSettingsPage, SportsSettingsPage,
} from './settings';

// Routes of the Sport Club court booking (docs/requirement-booking-sportclub-
// mgcc.md): the front desk area on the Ops shell (/ops/sport/…, unique paths
// apart from golf, FR-140) and the Dashboard Admin Sport Club (/sport-club/…).

const rp = (perm: string, el: React.ReactNode) => <RequirePermission perm={perm}>{el}</RequirePermission>;

/** Ops: the one Sport Club front desk for every sport (§5.3). */
export const SPORT_OPS_ROUTES = [
  { path: 'sport', element: rp('sportclub.booking.view', <CourtBoardPage />) },
  { path: 'sport/new', element: rp('sportclub.booking.create', <NewCourtBookingPage />) },
  { path: 'sport/check-in', element: rp('sportclub.booking.operate', <CourtCheckInPage />) },
  { path: 'sport/payments', element: rp('sportclub.booking.operate', <CourtPaymentsPage />) },
  { path: 'sport/finish', element: rp('sportclub.booking.operate', <CourtFinishPage />) },
  { path: 'sport/tickets', element: rp('sportclub.access.validate', <SportTicketsPage />) },
  { path: 'sport/packages', element: rp('sportclub.booking.create', <SportPackagesPage />) },
  { path: 'sport/history', element: rp('sportclub.booking.view', <CourtHistoryPage />) },
  { path: 'sport/incidents', element: rp('sportclub.incident.create', <SportIncidentsPage />) },
  { path: 'sport/cashier', element: rp('billing.cashier_shift.operate', <CashierPage defaultStation="sport_reception" />) },
  { path: 'sport/court-staff', element: rp('sportclub.court_report.create', <CourtStaffPage />) },
];

/** Back Office: Dashboard Admin Sport Club (§6.2). */
export const SPORT_ADMIN_ROUTES = [
  { path: 'sport-club', element: rp('sportclub.dashboard.view', <SportOverviewPage />) },
  { path: 'sport-club/calendar', element: rp('sportclub.booking.view', <SportCalendarPage />) },
  { path: 'sport-club/reservations', element: rp('sportclub.booking.view', <SportReservationsPage />) },
  { path: 'sport-club/recurring', element: rp('sportclub.booking.view', <SportRecurringPage />) },
  { path: 'sport-club/blocks', element: rp('sportclub.booking.view', <SportBlocksPage />) },
  { path: 'sport-club/packages', element: rp('sportclub.booking.view', <SportPackagesAdminPage />) },
  { path: 'sport-club/reports', element: rp('sportclub.dashboard.view', <SportReportsPage />) },
  { path: 'sport-club/incidents', element: rp('sportclub.incident.view', <SportIncidentsPage />) },
  { path: 'sport-club/settings/sports', element: rp('sportclub.facility.view', <SportsSettingsPage />) },
  { path: 'sport-club/settings/courts', element: rp('sportclub.court.view', <CourtsSettingsPage />) },
  { path: 'sport-club/settings/hours', element: rp('sportclub.facility.view', <HoursSettingsPage />) },
  { path: 'sport-club/settings/rules', element: rp('sportclub.booking.view', <RulesSettingsPage />) },
  { path: 'sport-club/settings/rates', element: rp('sportclub.booking.view', <RatesSettingsPage />) },
  { path: 'sport-club/settings/payment', element: rp('sportclub.booking.view', <PaymentSettingsPage />) },
  { path: 'sport-club/settings/content', element: rp('sportclub.facility.view', <ContentSettingsPage />) },
];
