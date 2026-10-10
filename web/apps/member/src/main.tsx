import React from 'react';
import ReactDOM from 'react-dom/client';
import { createBrowserRouter, Link, Navigate, Outlet, RouterProvider } from 'react-router';
import '@oneclub/shell/shell.css';
import './member.css';
import './journey/journey.css';
import {
  AppProviders, ErrorBoundary, LoginPage, NotFoundPage, NotificationsPage, RequireShell, ResetPasswordPage, TopNavLayout, setMemberProgram, useFlag,
} from '@oneclub/shell';
import { BenefitsPage, CardPage, FamilyPage, MemberProfilePage, MyChargesPage, MyPaymentsPage, OtpLoginPage, StatementsPage } from './golf';
import { P2_MEMBER_ROUTES } from './p2';
import { CourseGuidePage } from './journey/course';
import { P3_MEMBER_ROUTES } from './p3';
import { ActivityPage, GolfHistoryPage, StayHistoryPage } from './journey/activity';
import { MyStayPage } from './journey/mystay';
import { BookHub } from './journey/book';
import { GolfBookingPage } from './journey/booking';
import { MemberHome } from './journey/home';
import { ApplicationStatusPage, ApplyPage, JoinPage } from './journey/join';
import { GuestsPage, MembershipOverviewPage, TransactionsPage } from './journey/membership';
import { BungalowWizard, MeetingRoomWizard } from './journey/resort';
import { LeaderboardPage } from './journey/leaderboard';
import { RangeWizard } from './journey/range';
import { TeeTimeWizard } from './journey/teetime';
import { HubFrame } from './journey/ui';
import { MemberSignIn, MemberSignUp } from './journey/auth';
import { PromoDetailPage } from './journey/promo';
import {
  MemberDemoPage, MyClassesPage, MyCourtsPage, MyVisitsPage, SportAccessPage, SportCourtsPage, SportGuestsPage, SportPackagesPage, setProgramDomains,
} from './journey/sport';

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
      // the consumer sign-in of members and guests (demo feedback 10 Oct 2026 #37); the full page for MFA / password change
      { path: '/login', element: <MemberSignIn /> },
      { path: '/login/full', element: <MemberLogin /> },
      { path: '/register/member', element: <MemberSignUp kind="member" /> },
      { path: '/register/guest', element: <MemberSignUp kind="guest" /> },
      { path: '/login/code', element: <OtpLoginPage /> },
      { path: '/reset-password', element: <ResetPasswordPage /> },
      // Discover → Join Membership (before the member has an account)
      { path: '/join', element: <JoinPage /> },
      // demo access: pick a member persona, log in with the e-mail filled in (404 on a live instance)
      { path: '/demo', element: <MemberDemoPage /> },
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
          { path: 'book/driving-range', element: <RangeWizard /> },
          { path: 'book/bungalow', element: <BungalowWizard /> },
          { path: 'book/meeting-room', element: <MeetingRoomWizard /> },
          { path: 'bookings/:id', element: <GolfBookingPage /> },
          // My Activity
          { path: 'activity', element: <ActivityPage /> },
          { path: 'activity/golf', element: <GolfHistoryPage /> },
          { path: 'activity/leaderboard', element: <LeaderboardPage /> },
          { path: 'activity/stays', element: <StayHistoryPage /> },
          { path: 'activity/stays/:id', element: <MyStayPage /> },
          { path: 'golf/course-guide', element: <CourseGuidePage /> },
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
          { path: 'promo/:id', element: <PromoDetailPage /> },
          // earlier Member App paths (links in e-mails and notifications)
          { path: 'golf', element: to('/book/tee-time') },
          { path: 'golf/tee-time', element: to('/book/tee-time') },
          { path: 'golf/my-flights', element: to('/activity') },
          { path: 'golf/my-caddy', element: to('/activity') },
          { path: 'golf/my-golf-cart', element: to('/activity') },
          { path: 'bookings', element: to('/activity') },
          { path: 'stay', element: to('/book/bungalow') },
          // Sport Club Member App (docs/requirement-booking-sportclub-mgcc.md §8.4)
          { path: 'sport-club', element: to('/sport-club/courts') },
          { path: 'sport-club/courts', element: <SportCourtsPage /> },
          { path: 'sport-club/access', element: <SportAccessPage /> },
          { path: 'activity/courts', element: <MyCourtsPage /> },
          { path: 'activity/classes', element: <MyClassesPage /> },
          { path: 'activity/visits', element: <MyVisitsPage /> },
          { path: 'membership/sport-guests', element: <SportGuestsPage /> },
          { path: 'membership/packages', element: <SportPackagesPage /> },
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

/**
 * Golf and Sport Club are two Member Apps on their own domains (FR-108..112):
 * the domain says the program in /surface.json; without one (local, a
 * custom domain) ?program=, a sportmember.* host or the member's last choice.
 */
async function loadProgram() {
  let program = new URLSearchParams(window.location.search).get('program') ?? '';
  try {
    const r = await fetch('/surface.json', { cache: 'no-store' });
    if (r.ok && (r.headers.get('content-type') ?? '').includes('json')) {
      const s = (await r.json()) as { program?: string; domains?: Record<string, string> };
      program ||= s.program ?? '';
      setProgramDomains(s.domains);
    }
  } catch { /* offline: the last choice */ }
  if (!program && window.location.hostname.startsWith('sportmember.')) program = 'sport_club';
  try {
    program ||= localStorage.getItem('oneclub.member.program') ?? '';
  } catch { /* private window */ }
  if (program === 'golf' || program === 'sport_club') setMemberProgram(program);
}

void loadProgram().then(() => {
  ReactDOM.createRoot(document.getElementById('root')!).render(
    <React.StrictMode>
      <AppProviders>
        <RouterProvider router={router} />
      </AppProviders>
    </React.StrictMode>,
  );
});
