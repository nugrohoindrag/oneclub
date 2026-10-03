import React from 'react';
import ReactDOM from 'react-dom/client';
import { createBrowserRouter, Outlet, RouterProvider } from 'react-router';
import '@oneclub/shell/shell.css';
import {
  AppProviders, ApiKeysPage, ApprovalDetailPage, ApprovalWorkflowsPage, ApprovalsPage, AuditLogsPage, BackgroundJobsPage, BrandingPage, BridgeAgentsPage,
  CoursesPage, CustomerInstancePage, DepartmentsPage, DevicesPage, EmployeesPage, ErrorBoundary, FeatureFlagsPage, FeaturesPage, IntegrationLogsPage,
  IntegrationsPage, LocalizationPage, LoginPage, MasterDataImportPage, NotFoundPage, NotificationHistoryPage, NotificationSettingsPage, NotificationsPage,
  OrganizationPage, PaymentMethodsPage, ProfilePage, PropertiesPage, RequirePermission, RequireShell, ResetPasswordPage, RolesPage, RulesPage, SidebarLayout,
  SystemSettingsPage, TaxServicePage, TopNavLayout, UsersPage, VenuesPage, ComingSoonPage,
} from '@oneclub/shell';
import {
  DashboardPage, ExecutiveOverviewPage, ExportsPage, MODULE_PAGES, ModulePage, ReportRunPage, ReportsPage, SettingsHomePage,
} from './pages';

const settings = [
  { path: 'settings', element: <SettingsHomePage /> },
  { path: 'settings/organization', element: <RequirePermission perm="platform.organization.view"><OrganizationPage /></RequirePermission> },
  { path: 'settings/organization/properties', element: <RequirePermission perm="platform.property.view"><PropertiesPage /></RequirePermission> },
  { path: 'settings/organization/departments', element: <RequirePermission perm="platform.department.view"><DepartmentsPage /></RequirePermission> },
  { path: 'settings/organization/employees', element: <RequirePermission perm="platform.employee.view"><EmployeesPage /></RequirePermission> },
  { path: 'settings/customer-instance', element: <RequirePermission perm="platform.instance.view"><CustomerInstancePage /></RequirePermission> },
  { path: 'settings/venues', element: <RequirePermission perm="platform.venue.view"><VenuesPage /></RequirePermission> },
  { path: 'settings/courses', element: <RequirePermission perm="golf.course.view"><CoursesPage /></RequirePermission> },
  { path: 'settings/users', element: <RequirePermission perm="platform.user.view"><UsersPage /></RequirePermission> },
  { path: 'settings/roles', element: <RequirePermission perm="platform.role.view"><RolesPage /></RequirePermission> },
  { path: 'settings/features', element: <RequirePermission perm="platform.module.view"><FeaturesPage /></RequirePermission> },
  { path: 'settings/feature-configuration', element: <RequirePermission perm="platform.feature_flag.view"><FeatureFlagsPage /></RequirePermission> },
  { path: 'settings/business-rules', element: <RequirePermission perm="platform.business_rule.view"><RulesPage kind="business_rule" /></RequirePermission> },
  { path: 'settings/club-policies', element: <RequirePermission perm="platform.club_policy.view"><RulesPage kind="club_policy" /></RequirePermission> },
  { path: 'settings/notifications', element: <RequirePermission perm="platform.notification_template.view"><NotificationSettingsPage /></RequirePermission> },
  { path: 'settings/integrations', element: <RequirePermission perm="platform.integration.view"><IntegrationsPage /></RequirePermission> },
  { path: 'settings/payment-methods', element: <RequirePermission perm="billing.payment_method.view"><PaymentMethodsPage /></RequirePermission> },
  { path: 'settings/tax-service', element: <RequirePermission perm="commercial.tax_service.view"><TaxServicePage /></RequirePermission> },
  { path: 'settings/approval-workflows', element: <RequirePermission perm="platform.approval_workflow.view"><ApprovalWorkflowsPage /></RequirePermission> },
  { path: 'settings/audit-logs', element: <RequirePermission perm="audit.log.view"><AuditLogsPage /></RequirePermission> },
  { path: 'settings/localization', element: <RequirePermission perm="platform.localization.update"><LocalizationPage /></RequirePermission> },
  { path: 'settings/branding', element: <RequirePermission perm="platform.branding.update"><BrandingPage /></RequirePermission> },
  { path: 'settings/system', element: <RequirePermission perm="platform.system_settings.view"><SystemSettingsPage /></RequirePermission> },
  { path: 'settings/system/background-jobs', element: <RequirePermission perm="platform.job.view"><BackgroundJobsPage /></RequirePermission> },
  { path: 'settings/system/devices', element: <RequirePermission perm="platform.device.view"><DevicesPage /></RequirePermission> },
  { path: 'settings/system/master-data-import', element: <RequirePermission perm="platform.import.view"><MasterDataImportPage /></RequirePermission> },
  { path: 'settings/system/api-keys', element: <RequirePermission perm="platform.api_key.view"><ApiKeysPage /></RequirePermission> },
  { path: 'settings/system/bridge-agents', element: <RequirePermission perm="platform.bridge_agent.view"><BridgeAgentsPage /></RequirePermission> },
  { path: 'settings/system/integration-logs', element: <RequirePermission perm="platform.integration_log.view"><IntegrationLogsPage /></RequirePermission> },
  { path: 'settings/system/notification-history', element: <RequirePermission perm="platform.notification_delivery.view"><NotificationHistoryPage /></RequirePermission> },
];

const router = createBrowserRouter([
  {
    element: <ErrorBoundary><Outlet /></ErrorBoundary>,
    children: [
      { path: '/login', element: <LoginPage shell="backoffice" /> },
      { path: '/reset-password', element: <ResetPasswordPage /> },
      {
        path: '/management',
        element: <RequireShell shell="management"><TopNavLayout shell="management" home="/management" /></RequireShell>,
        children: [
          { index: true, element: <ExecutiveOverviewPage /> },
          ...['golf', 'membership', 'booking', 'financial'].map((p) => ({
            path: p, element: <ComingSoonPage title={`${p === 'booking' ? 'Booking' : p.charAt(0).toUpperCase() + p.slice(1)} Performance`} phase={p === 'financial' ? 'P2' : 'P1'} />,
          })),
          { path: '*', element: <NotFoundPage /> },
        ],
      },
      {
        path: '/',
        element: <RequireShell shell="backoffice"><SidebarLayout /></RequireShell>,
        children: [
          { index: true, element: <DashboardPage /> },
          { path: 'approvals', element: <ApprovalsPage /> },
          { path: 'approvals/:id', element: <ApprovalDetailPage /> },
          ...MODULE_PAGES.map((m) => ({ path: m.path, element: <ModulePage path={m.path} /> })),
          { path: 'reports', element: <RequirePermission perm="reporting.report.view"><ReportsPage /></RequirePermission> },
          { path: 'reports/exports', element: <RequirePermission perm="reporting.export.create"><ExportsPage /></RequirePermission> },
          { path: 'reports/:code', element: <RequirePermission perm="reporting.report.view"><ReportRunPage /></RequirePermission> },
          ...settings,
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
