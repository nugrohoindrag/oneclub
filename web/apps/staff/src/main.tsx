import React, { Suspense, lazy } from 'react';
import ReactDOM from 'react-dom/client';
import { createBrowserRouter, Outlet, RouterProvider } from 'react-router';
import '@oneclub/shell/shell.css';
import { AppProviders, ErrorBoundary, RequireArea, ResetPasswordPage, Skeleton, StaffAreas, loadSurface, type AreaCode } from '@oneclub/shell';
import { DeviceEnrollPage, PasswordLoginPage, StaffLoginPage } from './login';
import { clearOfflineData } from './offline';

/*
 * Staff App (Technical Doc §6.1): one login, one session, one build for every
 * staff area, served on four domains (dashboard, cashier, caddy, kitchen).
 * The domain's surface is read before the first render. Each area is loaded
 * lazily, so a caddy tablet only downloads the tablet code; the area guard
 * runs before the download.
 */

const AREA_MODULES = {
  backoffice: lazy(() => import('./areas/backoffice')),
  management: lazy(() => import('./areas/management')),
  platform: lazy(() => import('./areas/platform')),
  screen: lazy(() => import('./areas/screen')),
  ops: lazy(() => import('./areas/ops')),
  tablet: lazy(() => import('./areas/tablet')),
  kitchen: lazy(() => import('./areas/kitchen')),
} satisfies Record<AreaCode, React.ComponentType>;

function AreaRoute({ code }: { code: AreaCode }) {
  const Area = AREA_MODULES[code];
  return (
    <RequireArea code={code}>
      <Suspense fallback={<div className="oc-content"><Skeleton rows={6} /></div>}>
        <Area />
      </Suspense>
    </RequireArea>
  );
}

const router = createBrowserRouter([
  {
    element: <ErrorBoundary><StaffAreas><Outlet /></StaffAreas></ErrorBoundary>,
    children: [
      { path: '/login', element: <StaffLoginPage /> },
      { path: '/login/password', element: <PasswordLoginPage /> },
      { path: '/login/device', element: <DeviceEnrollPage /> },
      { path: '/reset-password', element: <ResetPasswordPage /> },
      { path: '/management/*', element: <AreaRoute code="management" /> },
      { path: '/platform/*', element: <AreaRoute code="platform" /> },
      { path: '/screen/*', element: <AreaRoute code="screen" /> },
      { path: '/ops/*', element: <AreaRoute code="ops" /> },
      { path: '/tablet/*', element: <AreaRoute code="tablet" /> },
      { path: '/kitchen/*', element: <AreaRoute code="kitchen" /> },
      { path: '/*', element: <AreaRoute code="backoffice" /> },
    ],
  },
]);

void loadSurface().then(() =>
  ReactDOM.createRoot(document.getElementById('root')!).render(
    <React.StrictMode>
      <AppProviders onLogout={clearOfflineData}>
        <RouterProvider router={router} />
      </AppProviders>
    </React.StrictMode>,
  ),
);
