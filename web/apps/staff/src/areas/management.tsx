import { useRoutes } from 'react-router';
import { NotFoundPage, NotificationsPage, ProfilePage, SidebarLayout } from '@oneclub/shell';
import { ExecutiveOverviewPage } from '../pages';
import { BookingPerformancePage, GolfPerformancePage, MembershipPerformancePage } from '../p1/business';
import { KPIDashboardPage, P2_MANAGEMENT_ROUTES } from '../p2';
import { BI_MANAGEMENT_ROUTES, BIExecutiveOverviewPage } from '../p5/bi';

/** Management Dashboard area (`/management`): KPI dashboards with sidebar navigation. */
const routes = [
  {
    element: <SidebarLayout shell="management" />,
    children: [
      // PRD P5 EP-21: the Executive Overview across domains with targets (the P0 live counts stay under its Today tab).
      { index: true, element: <BIExecutiveOverviewPage /> },
      { path: 'overview-today', element: <ExecutiveOverviewPage /> },
      { path: 'golf', element: <GolfPerformancePage /> },
      { path: 'membership', element: <MembershipPerformancePage /> },
      { path: 'booking', element: <BookingPerformancePage /> },
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
