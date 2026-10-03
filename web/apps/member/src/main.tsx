import React from 'react';
import ReactDOM from 'react-dom/client';
import { createBrowserRouter, Outlet, RouterProvider } from 'react-router';
import '@oneclub/shell/shell.css';
import { useGet, type Page, type Schemas } from '@oneclub/api-client';
import { formatRelative, useTranslation } from '@oneclub/i18n';
import {
  AppProviders, Card, ComingSoonPage, ErrorBoundary, Icon, LoginPage, NotFoundPage, NotificationsPage, ProfilePage, RequireShell, ResetPasswordPage,
  TopNavLayout, useAuth, useBootstrap, useFlag,
} from '@oneclub/shell';

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
        <div className="oc-card oc-card-ink" style={{ minHeight: 200, display: 'flex', flexDirection: 'column', justifyContent: 'space-between' }}>
          <div className="oc-row"><strong>{boot.branding.appName}</strong><span className="oc-spacer" /><Icon name="contactless" size={24} /></div>
          <div>
            <div className="oc-small" style={{ opacity: 0.7 }}>Digital Member Card</div>
            <div style={{ fontSize: 22, fontWeight: 600 }}>{me?.fullName}</div>
            <div className="oc-small" style={{ opacity: 0.7 }}>{t('common.comingSoon', { phase: 'P1' })}</div>
          </div>
        </div>
        <Card title="Notifications" icon="notifications">
          {notes.data?.items.length === 0 && <div className="oc-small oc-muted">{t('shell.noNotifications')}</div>}
          <div className="oc-stack">
            {notes.data?.items.map((n) => (
              <div key={n.id}><div style={{ fontWeight: n.readAt ? 400 : 700 }}>{n.title}</div><div className="oc-small oc-muted">{formatRelative(n.createdAt)}</div></div>
            ))}
          </div>
        </Card>
      </div>
      <div className="oc-grid">
        {[['golf_course', 'Book Golf'], ['sports_tennis', 'Book Facility'], ['hotel', 'Book Bungalow'], ['receipt_long', 'My Transactions']].map(([icon, label]) => (
          <div key={label} className="oc-card">
            <div className="oc-card-head" style={{ marginBottom: 6 }}><span className="oc-icon-circle"><Icon name={icon} size={20} /></span><h3>{label}</h3></div>
            <span className="oc-nav-soon">P1</span>
          </div>
        ))}
      </div>
    </div>
  );
}

function MemberLogin() {
  const signup = useFlag('member.self_registration') === true;
  return <LoginPage shell="member" footer={signup ? <p className="oc-small oc-muted" style={{ margin: 0 }}>Don't have an account? <strong>Create an Account</strong> (P1)</p> : null} />;
}

const router = createBrowserRouter([
  {
    element: <ErrorBoundary><Outlet /></ErrorBoundary>,
    children: [
      { path: '/login', element: <MemberLogin /> },
      { path: '/reset-password', element: <ResetPasswordPage /> },
      {
        path: '/',
        element: <RequireShell shell="member"><TopNavLayout shell="member" bottomNav property={false} /></RequireShell>,
        children: [
          { index: true, element: <HomePage /> },
          { path: 'golf', element: <ComingSoonPage title="Golf" phase="P1" features={['Book Golf', 'Tee Time', 'My Flights', 'Scorecard', 'Handicap']} /> },
          { path: 'bookings', element: <ComingSoonPage title="Bookings" phase="P1" /> },
          { path: 'membership', element: <ComingSoonPage title="Membership" phase="P1" features={['My Membership', 'Digital Member Card', 'Family Members', 'Membership Statement']} /> },
          { path: 'profile', element: <ProfilePage /> },
          { path: 'notifications', element: <NotificationsPage /> },
          { path: '*', element: <NotFoundPage /> },
        ],
      },
    ],
  },
]);

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <AppProviders>
      <RouterProvider router={router} />
    </AppProviders>
  </React.StrictMode>,
);
