import { Navigate, useRoutes } from 'react-router';
import { NotFoundPage, NotificationsPage, ProfilePage, RequirePermission, SidebarLayout } from '@oneclub/shell';
import { KPIDashboardPage, P2_MANAGEMENT_ROUTES } from '../p2';
import { BI_MANAGEMENT_ROUTES } from '../p5/bi';
import { DomainDashboard, ExecutiveDashboard, GolfToday } from '../p5/bi-dash';

/** Management Dashboard area (`/management`): KPI dashboards with sidebar navigation. */
const routes = [
  {
    element: <SidebarLayout shell="management" />,
    children: [
      // PRD P5 EP-21: the Executive Overview across domains with targets (the P0 live counts stay under its Today tab).
      { index: true, element: <ExecutiveDashboard /> },
      { path: 'overview-today', element: <Navigate to="/management?view=today" replace /> },
      { path: 'golf', element: <DomainDashboard domain="golf" title="Golf Performance" extra={<GolfToday />} /> },
      { path: 'membership', element: <DomainDashboard domain="membership" title="Membership Performance" /> },
      { path: 'booking', element: <DomainDashboard domain="booking" title="Booking Performance" /> },
      { path: 'hr-performance', element: <RequirePermission perm="reporting.hr_performance.view"><DomainDashboard domain="hr" title="HR Performance" /></RequirePermission> },
      ...P2_MANAGEMENT_ROUTES,
      { path: 'inventory-performance', element: <KPIDashboardPage code="inventory-performance" /> },
      { path: 'banquet-performance', element: <KPIDashboardPage code="banquet-performance" /> },
      { path: 'procurement-performance', element: <KPIDashboardPage code="procurement-performance" /> },
      { path: 'financial', element: <KPIDashboardPage code="financial-performance" /> },
      ...BI_MANAGEMENT_ROUTES,
      { path: 'profile', element: <ProfilePage /> },
      { path: 'notifications', element: <NotificationsPage /> },
      { path: '*', element: <NotFoundPage /> },
    ],
  },
];

export default function ManagementArea() {
  return useRoutes(routes);
}
