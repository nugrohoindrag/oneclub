import { useRoutes } from 'react-router';
import {
  BrandingPage, CustomDomainPage, CustomerInstancePage, FeatureFlagsPage, FeaturesPage, IntegrationsPage, NotFoundPage, NotificationsPage,
  PermissionsPage, ProfilePage, RequirePermission, RolesPage, SidebarLayout, UsersPage,
} from '@oneclub/shell';

/**
 * Platform Administration area (`/platform`, PRD §6.2) runs inside each
 * customer instance and is restricted to Platform Admin. The cross-instance
 * console is P6.
 */
const routes = [
  {
    element: <SidebarLayout shell="platform-admin" />,
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
];

export default function PlatformArea() {
  return useRoutes(routes);
}
