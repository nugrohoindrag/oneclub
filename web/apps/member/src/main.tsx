import React from 'react';
import ReactDOM from 'react-dom/client';
import { createBrowserRouter, Link, Navigate, Outlet, RouterProvider } from 'react-router';
import '@oneclub/shell/shell.css';
import './member.css';
import './journey/journey.css';
import {
  AppProviders, ErrorBoundary, LoginPage, NotFoundPage, NotificationsPage, RequireShell, ResetPasswordPage, TopNavLayout, useFlag,
} from '@oneclub/shell';
import { BenefitsPage, CardPage, FamilyPage, MemberProfilePage, MyChargesPage, MyPaymentsPage, OtpLoginPage, StatementsPage } from './golf';
import { P2_MEMBER_ROUTES } from './p2';
import { P3_MEMBER_ROUTES } from './p3';
import { ActivityPage, GolfHistoryPage, StayHistoryPage } from './journey/activity';
import { BookHub } from './journey/book';
import { GolfBookingPage } from './journey/booking';
import { MemberHome } from './journey/home';
import { ApplicationStatusPage, ApplyPage, JoinPage } from './journey/join';
import { GuestsPage, MembershipOverviewPage, TransactionsPage } from './journey/membership';
import { BungalowWizard, MeetingRoomWizard } from './journey/resort';
import { TeeTimeWizard } from './journey/teetime';
import { HubFrame } from './journey/ui';

function MemberLogin() {
  const signup = useFlag('member.self_registration') === true;
  return (
    <LoginPage shell="member" footer={
      <p className="oc-small oc-muted" style={{ margin: 0 }}>
        <Link to="/login/code">Log in with a one-time code</Link>{signup ? ' · Activate your account from the invitation e-mail' : ''}
        <br />Not a member yet? <Link to="/join">Become a member</Link> · <Link to="/join/status">Track my application</Link>
      </p>
    } />
  );
}

const to = (path: string) => <Navigate to={path} replace />;

const router = createBrowserRouter([
  {
    element: <ErrorBoundary><Outlet /></ErrorBoundary>,
    children: [
      { path: '/login', element: <MemberLogin /> },
      { path: '/login/code', element: <OtpLoginPage /> },
      { path: '/reset-password', element: <ResetPasswordPage /> },
      // Discover → Join Membership (before the member has an account)
      { path: '/join', element: <JoinPage /> },
      { path: '/join/apply/:typeId', element: <ApplyPage /> },
      { path: '/join/status', element: <ApplicationStatusPage /> },
      {
        path: '/',
        element: <RequireShell shell="member"><TopNavLayout shell="member" bottomNav property={false} /></RequireShell>,
        children: [{
         // the sub-menu tabs above every page of a top menu
         element: <HubFrame />,
         children: [
          { index: true, element: <MemberHome /> },
          // Book (Plan Your Visit)
          { path: 'book', element: <BookHub /> },
          { path: 'book/tee-time', element: <TeeTimeWizard /> },
          { path: 'book/bungalow', element: <BungalowWizard /> },
          { path: 'book/meeting-room', element: <MeetingRoomWizard /> },
          { path: 'bookings/:id', element: <GolfBookingPage /> },
          // My Activity
          { path: 'activity', element: <ActivityPage /> },
          { path: 'activity/golf', element: <GolfHistoryPage /> },
          { path: 'activity/stays', element: <StayHistoryPage /> },
          // Membership
          { path: 'membership', element: <MembershipOverviewPage /> },
          { path: 'membership/card', element: <CardPage /> },
          { path: 'membership/benefits', element: <BenefitsPage /> },
          { path: 'membership/guests', element: <GuestsPage /> },
          { path: 'membership/family', element: <FamilyPage /> },
          { path: 'membership/statements', element: <StatementsPage /> },
          // Transactions
          { path: 'transactions', element: <TransactionsPage /> },
          { path: 'transactions/payments', element: <MyPaymentsPage /> },
          { path: 'transactions/member-charges', element: <MyChargesPage /> },
          { path: 'profile', element: <MemberProfilePage /> },
          // earlier Member App paths (links in e-mails and notifications)
          { path: 'golf', element: to('/book/tee-time') },
          { path: 'golf/tee-time', element: to('/book/tee-time') },
          { path: 'golf/my-flights', element: to('/activity') },
          { path: 'golf/my-caddy', element: to('/activity') },
          { path: 'golf/my-golf-cart', element: to('/activity') },
          { path: 'bookings', element: to('/activity') },
          { path: 'stay', element: to('/book/bungalow') },
          ...P2_MEMBER_ROUTES,
          ...P3_MEMBER_ROUTES,
          { path: 'notifications', element: <NotificationsPage /> },
          { path: '*', element: <NotFoundPage /> },
        ] }],
      },
    ],
  },
]);

// Member App theme in the POS look (member.css)
document.documentElement.dataset.app = 'member';

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <AppProviders>
      <RouterProvider router={router} />
    </AppProviders>
  </React.StrictMode>,
);
