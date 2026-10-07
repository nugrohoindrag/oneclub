import React from 'react';
import ReactDOM from 'react-dom/client';
import { createBrowserRouter, Link, Outlet, RouterProvider } from 'react-router';
import '@oneclub/shell/shell.css';
import './member.css';
import { useGet, type Page, type Schemas } from '@oneclub/api-client';
import { formatRelative, useTranslation } from '@oneclub/i18n';
import {
  AppProviders, Card, ComingSoonPage, ErrorBoundary, Icon, LoginPage, NotFoundPage, NotificationsPage, ProfilePage, RequireShell, ResetPasswordPage,
  TopNavLayout, useAuth, useBootstrap, useFlag,
} from '@oneclub/shell';
import {
  BenefitsPage, BookGolfPage, CardPage, FamilyPage, HomeShortcuts, MemberCard, MemberProfilePage, MyBookingsPage, MyChargesPage, MyFlightsPage, MyMembershipPage,
  MyPaymentsPage, OtpLoginPage, StatementsPage, TransactionsPage,
} from './golf';
import { P2_MEMBER_ROUTES } from './p2';
import { P3_MEMBER_ROUTES } from './p3';

/** Member home: digital card placeholder, latest notifications (dashboard style). */
function HomePage() {
  const { me } = useAuth();
  const boot = useBootstrap();
  const { t } = useTranslation();
  const notes = useGet<Page<Schemas['Notification']>>('/api/v1/platform/notifications?limit=5');
  return (
    <div className="oc-stack">
      <div className="oc-page-head"><div><h1>Halo, {me?.fullName.split(' ')[0]}</h1><p>{boot.branding.appName}</p></div></div>
      <div className="oc-grid-2">
        <HomeCard />
        <Card title="Notifications" icon="notifications">
          {notes.data?.items.length === 0 && <div className="oc-small oc-muted">{t('shell.noNotifications')}</div>}
          <div className="oc-stack">
            {notes.data?.items.map((n) => (
              <div key={n.id}><div style={{ fontWeight: n.readAt ? 400 : 700 }}>{n.title}</div><div className="oc-small oc-muted">{formatRelative(n.createdAt)}</div></div>
            ))}
          </div>
        </Card>
      </div>
      <HomeShortcuts />
    </div>
  );
}

function HomeCard() {
  const { me } = useAuth();
  const boot = useBootstrap();
  const m = useGet<{ profile: Record<string, unknown>; card?: Record<string, unknown>; clubName: string }>('/api/v1/member/membership', { retry: false });
  return <MemberCard name={me?.fullName ?? ''} club={m.data?.clubName ?? boot.branding.appName} card={m.data?.card as never} />;
}

function MemberLogin() {
  const signup = useFlag('member.self_registration') === true;
  return <LoginPage shell="member" footer={<p className="oc-small oc-muted" style={{ margin: 0 }}><Link to="/login/code">Log in with a one-time code</Link>{signup ? ' · Activate your account from the invitation e-mail' : ''}</p>} />;
}

const router = createBrowserRouter([
  {
    element: <ErrorBoundary><Outlet /></ErrorBoundary>,
    children: [
      { path: '/login', element: <MemberLogin /> },
      { path: '/login/code', element: <OtpLoginPage /> },
      { path: '/reset-password', element: <ResetPasswordPage /> },
      {
        path: '/',
        element: <RequireShell shell="member"><TopNavLayout shell="member" bottomNav property={false} /></RequireShell>,
        children: [
          { index: true, element: <HomePage /> },
          { path: 'golf', element: <BookGolfPage /> },
          { path: 'golf/tee-time', element: <BookGolfPage browseOnly /> },
          { path: 'golf/my-flights', element: <MyFlightsPage /> },
          { path: 'golf/my-caddy', element: <MyFlightsPage focus="caddy" /> },
          { path: 'golf/my-golf-cart', element: <MyFlightsPage focus="cart" /> },
          { path: 'bookings', element: <MyBookingsPage /> },
          { path: 'membership', element: <MyMembershipPage /> },
          { path: 'membership/card', element: <CardPage /> },
          { path: 'membership/benefits', element: <BenefitsPage /> },
          { path: 'membership/family', element: <FamilyPage /> },
          { path: 'membership/statements', element: <StatementsPage /> },
          { path: 'transactions', element: <TransactionsPage /> },
          { path: 'transactions/payments', element: <MyPaymentsPage /> },
          { path: 'transactions/member-charges', element: <MyChargesPage /> },
          { path: 'profile', element: <MemberProfilePage /> },
          ...P2_MEMBER_ROUTES,
          ...P3_MEMBER_ROUTES,
          { path: 'notifications', element: <NotificationsPage /> },
          { path: '*', element: <NotFoundPage /> },
        ],
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
