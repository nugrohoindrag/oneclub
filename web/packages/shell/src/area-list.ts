import type { Shell } from './context';

// Kept free of React so the Staff App build (vite.config.ts) reads the same list.

export type AreaCode = 'tablet' | 'ops' | 'management' | 'backoffice' | 'platform';

/** One area of the Staff App (Technical Doc §6.1). */
export interface Area {
  /** Also the name of the area module in the Staff App (`src/areas/<code>.tsx`). */
  code: AreaCode;
  label: string;
  icon: string;
  /** URL prefix; Back Office keeps the module paths at the root. */
  path: string;
  /** Server shell code: `GET /auth/me` lists it in `shells` when the user holds `permission`. */
  shell: Shell;
  permission: string;
  layout: 'sidebar' | 'top' | 'touch';
  /** Precached by the service worker and usable without a connection. */
  offline: boolean;
}

/** Staff App areas in landing order: after login a user opens the first area they have. */
export const AREAS: readonly Area[] = [
  { code: 'tablet', label: 'Caddy Tablet', icon: 'tablet_android', path: '/tablet', shell: 'caddy', permission: 'golf.tablet.use', layout: 'touch', offline: true },
  { code: 'ops', label: 'Operational', icon: 'point_of_sale', path: '/ops', shell: 'ops', permission: 'platform.ops.access', layout: 'touch', offline: true },
  { code: 'management', label: 'Management Dashboard', icon: 'insights', path: '/management', shell: 'management', permission: 'reporting.dashboard.view', layout: 'top', offline: false },
  { code: 'backoffice', label: 'Back Office', icon: 'dashboard', path: '/', shell: 'backoffice', permission: 'platform.backoffice.access', layout: 'sidebar', offline: false },
  { code: 'platform', label: 'Platform Administration', icon: 'admin_panel_settings', path: '/platform', shell: 'platform-admin', permission: 'platform.platform_admin.access', layout: 'sidebar', offline: false },
];
