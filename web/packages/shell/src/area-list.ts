import type { Shell } from './context';

// Kept free of React so the Staff App build (vite.config.ts) reads the same list.

export type AreaCode = 'management' | 'backoffice' | 'platform' | 'screen' | 'tablet' | 'kitchen' | 'ops';

/**
 * Domain the Staff App is served on (Technical Doc §6.1): one build, five
 * domains. The surface locks the areas that open and the way to log in;
 * permissions still decide what opens inside. The presence domain has no
 * area: it serves only the Attendance Form (clock in / out with GPS).
 */
export type Surface = 'dashboard' | 'cashier' | 'caddy' | 'kitchen' | 'presence';

export const SURFACES: readonly Surface[] = ['dashboard', 'cashier', 'caddy', 'kitchen', 'presence'];

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
  /** The domain the area opens on. */
  surface: Surface;
  layout: 'sidebar' | 'top' | 'touch' | 'fullscreen';
  /** Precached by the service worker and usable without a connection. */
  offline: boolean;
}

/**
 * Staff App areas in landing order: after login a user opens the first area
 * they have on the surface. Office areas come first, so an administrator
 * holding every permission never lands on a device area.
 */
export const AREAS: readonly Area[] = [
  { code: 'management', label: 'Management Dashboard', icon: 'insights', path: '/management', shell: 'management', permission: 'reporting.dashboard.view', surface: 'dashboard', layout: 'sidebar', offline: false },
  { code: 'backoffice', label: 'Back Office', icon: 'dashboard', path: '/', shell: 'backoffice', permission: 'platform.backoffice.access', surface: 'dashboard', layout: 'sidebar', offline: false },
  { code: 'platform', label: 'Platform Administration', icon: 'admin_panel_settings', path: '/platform', shell: 'platform-admin', permission: 'platform.platform_admin.access', surface: 'dashboard', layout: 'sidebar', offline: false },
  { code: 'screen', label: 'Clubhouse Screen', icon: 'tv', path: '/screen', shell: 'screen', permission: 'platform.screen.access', surface: 'dashboard', layout: 'fullscreen', offline: false },
  { code: 'tablet', label: 'Caddy Tablet', icon: 'tablet_android', path: '/tablet', shell: 'caddy', permission: 'golf.tablet.use', surface: 'caddy', layout: 'touch', offline: true },
  { code: 'kitchen', label: 'Kitchen Display', icon: 'skillet', path: '/kitchen', shell: 'kitchen', permission: 'commercial.kitchen.update', surface: 'kitchen', layout: 'touch', offline: false },
  { code: 'ops', label: 'Operational', icon: 'point_of_sale', path: '/ops', shell: 'ops', permission: 'platform.ops.access', surface: 'cashier', layout: 'touch', offline: true },
];
