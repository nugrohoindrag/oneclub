import React from 'react';
import ReactDOM from 'react-dom/client';
import { createBrowserRouter, Outlet, RouterProvider } from 'react-router';
import '@oneclub/shell/shell.css';
import {
  AppProviders, BrandingPage, CustomDomainPage, CustomerInstancePage, ErrorBoundary, FeatureFlagsPage, FeaturesPage, IntegrationsPage, LoginPage,
  NotFoundPage, NotificationsPage, PermissionsPage, ProfilePage, RequirePermission, RequireShell, ResetPasswordPage, RolesPage, SidebarLayout, UsersPage,
} from '@oneclub/shell';

/**
 * Platform Administration (PRD §6.2) runs inside each customer instance and
 * is restricted to Platform Admin. The cross-instance console is P6.
 */
const router = createBrowserRouter([
  {
    element: <ErrorBoundary><Outlet /></ErrorBoundary>,
    children: [
      { path: '/login', element: <LoginPage shell="platform-admin" title="Platform Administration" /> },
      { path: '/reset-password', element: <ResetPasswordPage /> },
      {
        path: '/',
        element: <RequireShell shell="platform-admin"><SidebarLayout shell="platform-admin" /></RequireShell>,
        children: [
          { index: true, element: <CustomerInstancePage title="Customer Instances" /> },
          { path: 'instance-configuration', element: <RequirePermission perm="platform.instance.update"><CustomerInstancePage title="Instance Configuration" /></RequirePermission> },
          { path: 'enabled-modules', element: <RequirePermission perm="platform.module.update"><FeaturesPage title="Enabled Modules" /></RequirePermission> },
          { path: 'feature-configuration', element: <RequirePermission perm="platform.feature_flag.update"><FeatureFlagsPage title="Feature Configuration" /></RequirePermission> },
          { path: 'branding', element: <RequirePermission perm="platform.branding.update"><BrandingPage /></RequirePermission> },
          { path: 'custom-domain', element: <RequirePermission perm="platform.domain.manage"><CustomDomainPage /></RequirePermission> },
          { path: 'users', element: <RequirePermission perm="platform.user.view"><UsersPage /></RequirePermission> },
          { path: 'roles', element: <RequirePermission perm="platform.role.view"><RolesPage title="Roles" /></RequirePermission> },
          { path: 'permissions', element: <RequirePermission perm="platform.permission.view"><PermissionsPage /></RequirePermission> },
          { path: 'integrations', element: <RequirePermission perm="platform.integration.manage"><IntegrationsPage /></RequirePermission> },
          { path: 'feature-flags', element: <RequirePermission perm="platform.feature_flag.update"><FeatureFlagsPage title="Feature Flags" /></RequirePermission> },
          { path: 'locale', element: <RequirePermission perm="platform.instance.update"><CustomerInstancePage title="Locale" focus="locale" /></RequirePermission> },
          { path: 'currency', element: <RequirePermission perm="platform.instance.update"><CustomerInstancePage title="Currency" focus="currency" /></RequirePermission> },
          { path: 'timezone', element: <RequirePermission perm="platform.instance.update"><CustomerInstancePage title="Timezone" focus="timezone" /></RequirePermission> },
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
