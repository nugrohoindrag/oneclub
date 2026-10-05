import { useRoutes } from 'react-router';
import { NotFoundPage, NotificationsPage, ProfilePage, TopNavLayout } from '@oneclub/shell';
import { ExecutiveOverviewPage } from '../pages';
import { BookingPerformancePage, GolfPerformancePage, MembershipPerformancePage } from '../p1/business';
import { KPIDashboardPage, P2_MANAGEMENT_ROUTES } from '../p2';

/** Management Dashboard area (`/management`): KPI dashboards with top pill navigation. */
const routes = [
  {
    element: <TopNavLayout shell="management" />,
    children: [
      { index: true, element: <ExecutiveOverviewPage /> },
      { path: 'golf', element: <GolfPerformancePage /> },
      { path: 'membership', element: <MembershipPerformancePage /> },
      { path: 'booking', element: <BookingPerformancePage /> },
      ...P2_MANAGEMENT_ROUTES,
      { path: 'inventory-performance', element: <KPIDashboardPage code="inventory-performance" /> },
      { path: 'banquet-performance', element: <KPIDashboardPage code="banquet-performance" /> },
      { path: 'procurement-performance', element: <KPIDashboardPage code="procurement-performance" /> },
      { path: 'financial', element: <KPIDashboardPage code="financial-performance" /> },
      { path: 'profile', element: <ProfilePage /> },
      { path: 'notifications', element: <NotificationsPage /> },
      { path: '*', element: <NotFoundPage /> },
    ],
  },
];

export default function ManagementArea() {
  return useRoutes(routes);
}
