import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import { cacheableApi, STAFF_API_CACHE } from './sw-cache';

// PRD P5 FR-OPS-P5-05: Employee Self Service is installable as a PWA, but payroll data is never cached offline.
describe('staff service worker cache', () => {
  it('never caches payroll, payslip or salary data', () => {
    for (const p of ['/api/v1/hris/payslips/123/pdf', '/api/v1/ess/payslips', '/api/v1/hris/payroll-runs', '/api/v1/hris/payroll-runs/1:calculate',
      '/api/v1/hris/salary-structures', '/api/v1/hris/payout-runs/1/bank-file', '/api/v1/hris/statutory-rates', '/api/v1/hris/bank-accounts',
      '/api/v1/hris/contracts', '/api/v1/ess/me', '/api/v1/golf/my-earnings/payslip']) {
      expect(cacheableApi(p), p).toBe(false);
    }
  });
  it('keeps the offline areas working', () => {
    for (const p of STAFF_API_CACHE) expect(cacheableApi(`${p}/x`.replace('//', '/')), p).toBe(true);
    expect(cacheableApi('/api/v1/golf/my-assignments')).toBe(true);
    expect(cacheableApi('/api/v1/ess/schedule')).toBe(false); // ESS schedule is kept in IndexedDB by the app, not by the worker
  });
  it('is the rule the service worker is built with', () => {
    const cfg = readFileSync(resolve(__dirname, '../vite.config.ts'), 'utf8');
    expect(cfg).toContain('cacheableApi(url.pathname)');
    expect(cfg).not.toMatch(/['"]\/api\/v1\/(hris|ess)\//);
  });
});
