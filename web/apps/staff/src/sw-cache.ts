// Runtime cache of the Staff App service worker (Technical Doc §6.4, PRD FR-SH-05), used by vite.config.ts.
//
// Only the API data the offline areas need is kept on the device: the app bootstrap, the session, the navigation, the outlets
// and the Caddy Tablet data. PRD P5 FR-OPS-P5-05: Employee Self Service is installable as a PWA on personal phones, but payroll
// and salary data (payslips, payroll runs, salary structures, pay components, payouts) and personal HR data are never cached
// offline — the deny-list wins over the allow-list, and sw-cache.test.ts guards both.

/** API path prefixes the service worker may cache (NetworkFirst). */
export const STAFF_API_CACHE = [
  '/api/v1/public/bootstrap', '/api/v1/auth/me', '/api/v1/platform/navigation', '/api/v1/commercial/outlets', '/api/v1/golf/my-assignments',
  '/api/v1/golf/my-earnings', '/api/v1/golf/rounds', '/api/v1/golf/course-maps',
];

/** Path fragments that are never cached, whatever the allow-list says (payroll, salary and personal HR data). */
export const NEVER_CACHE = [
  '/api/v1/hris/payslips', '/api/v1/ess/payslips', '/api/v1/hris/payroll', '/api/v1/hris/salary', '/api/v1/hris/pay-components', '/api/v1/hris/payout',
  '/api/v1/hris/statutory', '/api/v1/hris/bank-accounts', '/api/v1/hris/service-charge', '/api/v1/hris/contracts', '/api/v1/hris/employees',
  '/api/v1/ess/me', '/payslip', '/payroll', '/salary',
];

/** Whether the service worker may cache an API response. */
export function cacheableApi(pathname: string): boolean {
  if (NEVER_CACHE.some((p) => pathname.includes(p))) return false;
  return STAFF_API_CACHE.some((p) => pathname.startsWith(p));
}
